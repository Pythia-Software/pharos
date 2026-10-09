package querytable

// compile.go — WireQuery → parameterized SQL fragments.
//
// This is the single chokepoint where a query becomes SQL. It supports
// multi-sort (ORDER BY a, b, … + tiebreaker), synthetic expressions via
// FieldSpec, computed SELECT columns, and text/array operators. Callers splice
// the fragments into their own FROM/JOIN tree.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CompileResult holds the SQL fragments plus their ordered bound args.
type CompileResult struct {
	// WhereSQL is "" when no filters apply, else "(c1) AND (c2) ...". Never
	// includes the WHERE keyword, so callers AND it into their own predicates.
	WhereSQL string
	// OrderSQL is "" to use the schema default, else
	// "expr DIR NULLS x, expr2 DIR2 NULLS y, <tiebreak...>".
	OrderSQL string
	// SelectExprs are "<expr> AS <safe_alias>" for each requested backend field.
	SelectExprs []string
	// Args are the $N bound values, in placeholder order starting at startIdx.
	Args []any
	// SQLite-only parameter partitions, for composing count and row statements.
	WhereArgs []any
	OrderArgs []any
}

// Compile validates q against schema and emits SQL fragments. startIdx is the
// 1-based pgx placeholder index for the first bound value, so callers can
// interleave their own params; the returned int is the next free index.
//
// Errors (never a panic) on: unknown field, an operator not allowed for a
// field's kind or per-field operator allowlist, a filter on a
// non-server-filterable field, a sort on a non-sortable field, an unknown
// select field, or a value that fails coercion.
func Compile(q WireQuery, schema Schema, startIdx int) (CompileResult, int, error) {
	return CompileAt(q, schema, startIdx, time.Now())
}

// CompileAt captures relative datetime values using one explicit reference time.
// Share now across row/count/metric compilations for a consistent snapshot.
// Relative values are resolved to bound UTC timestamps, never interpolated SQL.
func CompileAt(q WireQuery, schema Schema, startIdx int, now time.Time) (CompileResult, int, error) {
	var res CompileResult
	idx := startIdx
	if err := q.Validate(); err != nil {
		return res, idx, fmt.Errorf("invalid query: %w", err)
	}

	// WHERE — conjunctive normal form: the AND of terms, each a single predicate
	// or an OR group of predicates.
	clauses := make([]string, 0, len(q.Where))
	for _, term := range q.Where {
		var (
			sql  string
			args []any
			next int
			err  error
		)
		if term.IsGroup() {
			sql, args, next, err = compileOrGroup(term.Any, schema, idx, now)
		} else {
			sql, args, next, err = compileLiteral(term.Literal(), schema, idx, now)
		}
		if err != nil {
			return res, idx, err
		}
		if sql == "" {
			continue // skipped (empty value / always-true group)
		}
		clauses = append(clauses, "("+sql+")")
		res.Args = append(res.Args, args...)
		idx = next
	}
	if len(clauses) > 0 {
		res.WhereSQL = strings.Join(clauses, " AND ")
	}

	// ORDER BY (multi-sort) + tiebreak
	terms := make([]string, 0, len(q.OrderBy)+len(schema.TiebreakSort))
	for _, ob := range q.OrderBy {
		spec, ok := schema.Fields[ob.Field]
		if !ok {
			return res, idx, fmt.Errorf("unknown sort field %q", ob.Field)
		}
		if !spec.Sortable {
			return res, idx, fmt.Errorf("field %q is not sortable", ob.Field)
		}
		t, args, next, err := compileOrderTerm(spec.SortExpr, ob, idx)
		if err != nil {
			return res, idx, fmt.Errorf("field %q: %w", ob.Field, err)
		}
		terms = append(terms, t)
		res.Args = append(res.Args, args...)
		idx = next
	}
	if len(terms) > 0 {
		for _, tb := range schema.TiebreakSort {
			if spec, ok := schema.Fields[tb.Field]; ok {
				if t, args, next, err := compileOrderTerm(spec.SortExpr, tb, idx); err == nil {
					terms = append(terms, t)
					res.Args = append(res.Args, args...)
					idx = next
				}
			}
		}
		res.OrderSQL = strings.Join(terms, ", ")
	}

	// SELECT (computed/backed columns the caller asked to project)
	for _, name := range q.Select {
		spec, ok := schema.Fields[name]
		if !ok {
			return res, idx, fmt.Errorf("unknown select field %q", name)
		}
		res.SelectExprs = append(res.SelectExprs, fmt.Sprintf("%s AS %s", spec.Expr, safeIdent(name)))
	}

	return res, idx, nil
}

// AggCompileResult holds the SQL fragments for one metric's GROUP BY query. The
// caller splices them into its own FROM/JOIN, sharing the rows query's WHERE so
// the metric covers the same filtered set (scope = whole set, no paging). Use
// the same now passed to CompileAt for the rows query when compiling this WHERE:
//
//	SELECT <SelectExprs joined by ", ">
//	FROM   <caller FROM/JOIN>
//	[WHERE <CompileAt(WireQuery{Where: req.Where}, …, now).WhereSQL>]
//	[GROUP BY <GroupBySQL>]
//
// SelectExprs is, in order: one `expr AS "g0"/"g1"/…` per group field, then the
// aggregate `AS "value"`, then `COUNT(*) AS "count"`. GroupBySQL lists the same
// group expressions (empty string ⇒ a single grand-total row, no GROUP BY).
type AggCompileResult struct {
	SelectExprs []string
	GroupBySQL  string
}

// CompileAggregation validates one AggSpec against the schema allowlist and emits
// its SELECT + GROUP BY fragments. Like Compile, the only request-influenced
// tokens that reach SQL are the validated op and the schema-defined field
// expressions — never request input. No bound args are produced (aggregations
// carry no values; the shared WHERE is compiled separately via CompileAt using
// the rows query's clock).
//
// Errors on: unknown op, unknown measure/group field, a missing measure for an
// op that needs one, or an op not allowed for the measure field's kind.
func CompileAggregation(spec AggSpec, schema Schema) (AggCompileResult, error) {
	var res AggCompileResult
	if err := validateBasicMetricShape(spec); err != nil {
		return res, err
	}
	if !aggOpKnown(spec.Op) {
		return res, fmt.Errorf("unknown aggregate op %q", spec.Op)
	}

	groupExprs := make([]string, 0, len(spec.GroupBy))
	for i, g := range spec.GroupBy {
		gs, ok := schema.Fields[g]
		if ok && gs.Groupable != nil && !*gs.Groupable {
			return res, fmt.Errorf("grouping disabled for %q", g)
		}
		if !ok {
			return res, fmt.Errorf("unknown group field %q", g)
		}
		res.SelectExprs = append(res.SelectExprs, fmt.Sprintf("%s AS %s", gs.Expr, safeIdent(fmt.Sprintf("g%d", i))))
		groupExprs = append(groupExprs, gs.Expr)
	}

	valueExpr, err := aggValueExpr(spec, schema)
	if err != nil {
		return res, err
	}
	res.SelectExprs = append(res.SelectExprs, valueExpr+` AS "value"`, `COUNT(*) AS "count"`)

	if len(groupExprs) > 0 {
		res.GroupBySQL = strings.Join(groupExprs, ", ")
	}
	return res, nil
}

// aggValueExpr builds the aggregate SELECT expression. `count` with no field is
// COUNT(*) (counts rows); with a field it counts non-null values.
func aggValueExpr(spec AggSpec, schema Schema) (string, error) {
	if spec.Op == "count" && spec.Field == "" {
		return "COUNT(*)", nil
	}
	if spec.Field == "" {
		return "", fmt.Errorf("aggregate op %q requires a measure field", spec.Op)
	}
	fs, ok := schema.Fields[spec.Field]
	if !ok {
		return "", fmt.Errorf("unknown measure field %q", spec.Field)
	}
	if fs.AggregateOps != nil {
		allowed := false
		for _, op := range fs.AggregateOps {
			allowed = allowed || op == spec.Op
		}
		if !allowed {
			return "", fmt.Errorf("aggregate op %q disabled for %q", spec.Op, spec.Field)
		}
	}
	if !aggOpAllowed(fs.Kind, spec.Op) {
		return "", fmt.Errorf("aggregate op %q not allowed on %s field", spec.Op, kindName(fs.Kind))
	}
	switch spec.Op {
	case "count":
		return "COUNT(" + fs.Expr + ")", nil
	case "count_distinct":
		return "COUNT(DISTINCT " + fs.Expr + ")", nil
	case "sum":
		return "SUM(" + fs.Expr + ")", nil
	case "avg":
		return "AVG(" + fs.Expr + ")", nil
	case "min":
		return "MIN(" + fs.Expr + ")", nil
	case "max":
		return "MAX(" + fs.Expr + ")", nil
	default:
		return "", fmt.Errorf("unknown aggregate op %q", spec.Op)
	}
}

func aggOpKnown(op string) bool {
	switch op {
	case "count", "count_distinct", "sum", "avg", "min", "max":
		return true
	}
	return false
}

// aggOpAllowed mirrors @pythia-software/query-table-core AGG_OPS_BY_TYPE — the server-side
// enforcement of which aggregate ops a field's kind permits. Keep in lockstep.
func aggOpAllowed(kind FieldKind, op string) bool {
	switch kind {
	case FieldNumber:
		switch op {
		case "count", "count_distinct", "sum", "avg", "min", "max":
			return true
		}
	case FieldDatetime, FieldEnum, FieldText:
		switch op {
		case "count", "count_distinct", "min", "max":
			return true
		}
	case FieldBool:
		switch op {
		case "count", "count_distinct":
			return true
		}
	case FieldTextArray:
		return op == "count"
	}
	return false
}

func orderTerm(expr, dir, nulls string) (string, error) {
	d := "ASC"
	if strings.EqualFold(dir, "desc") {
		d = "DESC"
	}
	n := "NULLS LAST"
	switch strings.ToLower(nulls) {
	case "", "last":
		n = "NULLS LAST"
	case "first":
		n = "NULLS FIRST"
	default:
		return "", fmt.Errorf("unknown nulls position %q", nulls)
	}
	return expr + " " + d + " " + n, nil
}

func compileOrderTerm(expr string, term OrderBy, idx int) (string, []any, int, error) {
	var args []any
	if term.Extract != nil {
		expr = fmt.Sprintf("substring((%s)::text FROM $%d)", expr, idx)
		args = []any{term.Extract.Regex}
		idx++
	}
	t, err := orderTerm(expr, term.Dir, term.Nulls)
	return t, args, idx, err
}

// compileLiteral validates one predicate against the schema allowlist and emits
// its SQL. A negated predicate is wrapped in a null-exclusive NOT
// (`NOT (<base>) AND <expr> IS NOT NULL`) so a NULL value satisfies neither the
// predicate nor its negation — matching applyQuery and the not_matches_regex
// convention. Returns "" (no SQL) for an empty-value no-op, exactly like the
// positive form.
func compileLiteral(c WhereClause, schema Schema, idx int, reference ...time.Time) (string, []any, int, error) {
	spec, ok := schema.Fields[c.Field]
	if !ok {
		return "", nil, idx, fmt.Errorf("unknown filter field %q", c.Field)
	}
	if !spec.ServerFilter {
		return "", nil, idx, fmt.Errorf("field %q is not server-filterable", c.Field)
	}
	if !fieldOpEnabled(spec, c.Op) {
		return "", nil, idx, fmt.Errorf("field %q: op %q is not enabled", c.Field, c.Op)
	}
	sql, args, next, err := compileWhere(spec, c.Op, c.Value, idx, reference...)
	if err != nil {
		return "", nil, idx, fmt.Errorf("field %q: %w", c.Field, err)
	}
	if sql == "" {
		return "", nil, idx, nil // skipped (empty value)
	}
	if c.Negated {
		if spec.Kind == FieldTextArray {
			sql = fmt.Sprintf("NOT (%s) AND COALESCE(cardinality(%s), 0) > 0", sql, spec.Expr)
		} else {
			sql = fmt.Sprintf("NOT (%s) AND %s IS NOT NULL", sql, spec.Expr)
		}
	}
	return sql, args, next, nil
}

// compileOrGroup compiles a disjunction: `(p1) OR (p2) OR ...`. Every member is
// validated (so an unknown field or disabled op is reported) even when the group
// is dropped. If any disjunct is an always-true no-op (empty value), the whole
// group is always true and is dropped — mirroring applyQuery, where an
// always-true member short-circuits the OR to "match everything".
func compileOrGroup(lits []WhereClause, schema Schema, idx int, reference ...time.Time) (string, []any, int, error) {
	start := idx
	parts := make([]string, 0, len(lits))
	var args []any
	alwaysTrue := false
	for _, c := range lits {
		sql, a, next, err := compileLiteral(c, schema, idx, reference...)
		if err != nil {
			return "", nil, start, err
		}
		if sql == "" {
			alwaysTrue = true // keep validating the rest, then drop the group
			continue
		}
		parts = append(parts, "("+sql+")")
		args = append(args, a...)
		idx = next
	}
	if alwaysTrue || len(parts) == 0 {
		return "", nil, start, nil
	}
	return strings.Join(parts, " OR "), args, idx, nil
}

func compileWhere(spec FieldSpec, op, value string, idx int, reference ...time.Time) (string, []any, int, error) {
	switch op {
	case "is_null":
		if spec.Kind == FieldTextArray {
			return fmt.Sprintf("COALESCE(cardinality(%s), 0) = 0", spec.Expr), nil, idx, nil
		}
		return spec.Expr + " IS NULL", nil, idx, nil
	case "is_not_null":
		if spec.Kind == FieldTextArray {
			return fmt.Sprintf("COALESCE(cardinality(%s), 0) > 0", spec.Expr), nil, idx, nil
		}
		return spec.Expr + " IS NOT NULL", nil, idx, nil
	}
	if !opAllowed(spec.Kind, op) {
		return "", nil, idx, fmt.Errorf("op %q not allowed on %s field", op, kindName(spec.Kind))
	}
	// Empty value: a cleared input is a "no filter" signal (matches the frontend
	// + applyQuery), except =/!= on text/enum where "" is a legitimate compare.
	if value == "" {
		switch op {
		case "=", "!=":
			if spec.Kind != FieldText && spec.Kind != FieldEnum {
				return "", nil, idx, nil
			}
		default:
			return "", nil, idx, nil
		}
	}

	switch op {
	case "=", "!=", ">", ">=", "<", "<=":
		sqlOp := op
		if op == "!=" {
			sqlOp = "<>"
		}
		var v any
		var err error
		if spec.Kind == FieldDatetime && (strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-")) {
			offset, parseErr := parseRelativeDuration(value)
			if parseErr != nil {
				return "", nil, idx, parseErr
			}
			var now time.Time
			if len(reference) > 0 {
				now = reference[0]
			} else {
				now = time.Now()
			}
			v, err = relativeTimestamp(now, offset)
		} else {
			v, err = coerce(spec.Kind, value)
		}
		if err != nil {
			return "", nil, idx, err
		}
		return fmt.Sprintf("%s %s $%d", spec.Expr, sqlOp, idx), []any{v}, idx + 1, nil

	case "contains":
		// Literal case-insensitive substring (POSITION, not ILIKE) so % and _ in
		// the user's value don't silently become wildcards.
		return fmt.Sprintf("POSITION(LOWER($%d) IN LOWER(%s)) > 0", idx, spec.Expr), []any{value}, idx + 1, nil
	case "starts_with":
		return fmt.Sprintf("POSITION(LOWER($%d) IN LOWER(%s)) = 1", idx, spec.Expr), []any{value}, idx + 1, nil
	case "ends_with":
		return fmt.Sprintf("RIGHT(LOWER(%s), LENGTH($%d)) = LOWER($%d)", spec.Expr, idx, idx), []any{value}, idx + 1, nil
	case "length_gt", "length_lt", "length_eq":
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil || limit < 0 || limit > 9007199254740991 || strings.HasPrefix(value, "+") || (len(value) > 1 && value[0] == '0') {
			return "", nil, idx, fmt.Errorf("invalid string length %q: expected a non-negative integer", value)
		}
		comparison := ">"
		if op == "length_lt" {
			comparison = "<"
		} else if op == "length_eq" {
			comparison = "="
		}
		return fmt.Sprintf("CHAR_LENGTH(%s) %s $%d::bigint", spec.Expr, comparison, idx), []any{limit}, idx + 1, nil
	case "matches_regex":
		return fmt.Sprintf("(%s)::text ~ $%d", spec.Expr, idx), []any{value}, idx + 1, nil
	case "not_matches_regex":
		return fmt.Sprintf("(%s)::text !~ $%d", spec.Expr, idx), []any{value}, idx + 1, nil
	case "includes":
		if spec.ArrayCaseSensitive {
			return fmt.Sprintf("EXISTS (SELECT 1 FROM unnest(%s) AS _e WHERE _e = $%d)", spec.Expr, idx), []any{value}, idx + 1, nil
		}
		// Case-insensitive membership in a text[] column (ARRAY_HAS semantics).
		return fmt.Sprintf("EXISTS (SELECT 1 FROM unnest(%s) AS _e WHERE LOWER(_e) = LOWER($%d))", spec.Expr, idx),
			[]any{value}, idx + 1, nil
	default:
		return "", nil, idx, fmt.Errorf("unknown op %q", op)
	}
}

func fieldOpEnabled(spec FieldSpec, op string) bool {
	if spec.FilterOps == nil {
		return true
	}
	for _, enabled := range spec.FilterOps {
		if enabled == op {
			return true
		}
	}
	return false
}

// opAllowed mirrors @pythia-software/query-table-core OPS_BY_TYPE — the server-side enforcement
// of the operator matrix. Nullity is valid on every kind (incl. bool).
func opAllowed(kind FieldKind, op string) bool {
	switch op {
	case "is_null", "is_not_null":
		return true
	}
	switch kind {
	case FieldText:
		switch op {
		case "=", "!=", "contains", "starts_with", "ends_with", "matches_regex", "not_matches_regex", "length_gt", "length_lt", "length_eq":
			return true
		}
	case FieldEnum:
		switch op {
		case "=", "!=":
			return true
		}
	case FieldNumber, FieldDatetime:
		switch op {
		case "=", "!=", ">", ">=", "<", "<=":
			return true
		}
	case FieldBool:
		switch op {
		case "=", "!=":
			return true
		}
	case FieldTextArray:
		switch op {
		case "includes":
			return true
		}
	}
	return false
}

func coerce(kind FieldKind, v string) (any, error) {
	switch kind {
	case FieldNumber:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("not a number: %q", v)
		}
		return f, nil
	case FieldBool:
		switch strings.ToLower(v) {
		case "true", "1", "t":
			return true, nil
		case "false", "0", "f":
			return false, nil
		default:
			return nil, fmt.Errorf("not a bool: %q", v)
		}
	case FieldDatetime:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		if t, err := time.Parse("2006-01-02", v); err == nil {
			return t, nil
		}
		return nil, fmt.Errorf("not an RFC3339 datetime: %q", v)
	default:
		return v, nil
	}
}

// DistinctCompile holds the fragments for an autocomplete distinct-values query.
// The caller assembles them into its own FROM/JOIN tree:
//
//	SELECT DISTINCT <Expr> AS v FROM ... WHERE <Expr> IS NOT NULL [AND <SearchSQL>]
//	ORDER BY v LIMIT <n+1>   -- fetch one extra to compute hasMore
type DistinctCompile struct {
	Expr      string
	SearchSQL string // "" when search is empty
	Args      []any
}

type DistinctHasNullCompile struct {
	IsNullExpr string
}

// CompileDistinct builds the fragments to back filter-value autocomplete for a
// field (design feedback: every field is an autocomplete by default). The search
// is a literal case-insensitive substring (no wildcard injection).
func CompileDistinct(field, search string, schema Schema, startIdx int) (DistinctCompile, int, error) {
	spec, ok := schema.Fields[field]
	if !ok {
		return DistinctCompile{}, startIdx, fmt.Errorf("unknown field %q", field)
	}
	dc := DistinctCompile{Expr: spec.Expr}
	idx := startIdx
	if search != "" {
		dc.SearchSQL = fmt.Sprintf("POSITION(LOWER($%d) IN LOWER(%s::text)) > 0", idx, spec.Expr)
		dc.Args = append(dc.Args, search)
		idx++
	}
	return dc, idx, nil
}

// CompileDistinctHasNull builds the nullability expression for the same field-aware
// distinct path used by value autocomplete. It is intended for a lightweight
// metadata query that answers “does this field have any nulls?” without another
// independent field lookup path in callers.
func CompileDistinctHasNull(field string, schema Schema) (DistinctHasNullCompile, error) {
	spec, ok := schema.Fields[field]
	if !ok {
		return DistinctHasNullCompile{}, fmt.Errorf("unknown field %q", field)
	}
	return DistinctHasNullCompile{IsNullExpr: spec.Expr + " IS NULL"}, nil
}

func kindName(k FieldKind) string {
	switch k {
	case FieldText:
		return "text"
	case FieldNumber:
		return "number"
	case FieldDatetime:
		return "datetime"
	case FieldBool:
		return "bool"
	case FieldEnum:
		return "enum"
	case FieldTextArray:
		return "textarray"
	default:
		return "unknown"
	}
}

// safeIdent makes a SELECT alias from a (schema-validated) field name. Field
// names already pass the schema's name pattern, but we defensively strip
// anything that isn't an identifier char so an alias can never break out.
func safeIdent(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" {
		s = "col"
	}
	return "\"" + s + "\""
}

// validateBasicMetricShape guards v1 entry points against dropping v2 intent.
func validateBasicMetricShape(spec AggSpec) error {
	if spec.Expression != "" || spec.ExpressionY != "" || spec.Distribution != nil || (spec.Scope != "" && spec.Scope != "allMatching") || len(spec.Sort) > 0 || spec.GroupLimit != 0 || len(spec.Diagnostics) > 0 || (spec.Display != nil && spec.Display.Kind == "scatter") {
		return fmt.Errorf("modern metrics require CompileMetrics")
	}
	return nil
}
