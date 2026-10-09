package querytable

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SQLiteOptions captures one clock for filters in rows, counts, and metrics.
// Zero Now captures time.Now once per compilation. Share it across a batch.
type SQLiteOptions struct{ Now time.Time }

// CompileSQLite emits fragments with anonymous ? parameters. Args are ordered
// for WHERE followed by ORDER BY; SELECT expressions contain no parameters.
// FROM/JOIN and field expressions are trusted host input, never query input.
// Requires SQLite 3.38+ with JSON and the functions from SQLiteFunctions.
// This is a v1 row/basic-aggregation adapter, not the qt-postgres-v1 v2 profile.
func CompileSQLite(q WireQuery, s Schema, o SQLiteOptions) (CompileResult, error) {
	var res CompileResult
	if err := q.Validate(); err != nil {
		return res, err
	}
	for _, metric := range q.Aggregations {
		if err := validateBasicMetricShape(metric); err != nil {
			return res, err
		}
	}
	if err := validateSQLiteSchema(s); err != nil {
		return res, err
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	where, err := CompileSQLiteWhere(q.Where, s, o)
	if err != nil {
		return res, err
	}
	res.WhereSQL, res.WhereArgs = where.SQL, where.Args
	res.Args = append([]any{}, where.Args...)
	orders := []OrderBy(q.OrderBy)
	if len(orders) == 0 {
		orders = s.DefaultSort
	}
	orders = append(append([]OrderBy{}, orders...), s.TiebreakSort...)
	// Always end in stable identity, including an otherwise unsorted request.
	seenID := false
	for _, order := range orders {
		if order.Field == s.IDField && order.Extract == nil {
			seenID = true
		}
	}
	if !seenID {
		orders = append(orders, OrderBy{Field: s.IDField, Dir: "asc"})
	}
	var terms []string
	for _, order := range orders {
		f, ok := s.Fields[order.Field]
		if !ok || !f.Sortable {
			return CompileResult{}, fmt.Errorf("unknown or disabled sort field %q", order.Field)
		}
		expr := f.SortExpr
		if expr == "" {
			expr = f.Expr
		}
		if f.SQLiteSortField != "" {
			f = s.Fields[f.SQLiteSortField]
		}
		f.Expr = expr
		expr = sqliteOrderValue(f)
		if order.Extract != nil {
			if _, err := sqlitePattern(order.Extract.Regex); err != nil {
				return CompileResult{}, err
			}
			expr = "qt_regexp_extract(?,CAST(" + expr + " AS TEXT))"
			res.Args = append(res.Args, order.Extract.Regex)
			// Captures are text even for a numeric source, as in the frontend.
		}
		if order.Dir != "asc" && order.Dir != "desc" {
			return CompileResult{}, fmt.Errorf("invalid sort direction %q", order.Dir)
		}
		sql, err := orderTerm(expr, order.Dir, order.Nulls)
		if err != nil {
			return CompileResult{}, err
		}
		terms = append(terms, sql)
	}
	res.OrderSQL = strings.Join(terms, ", ")
	res.OrderArgs = append([]any{}, res.Args[len(res.WhereArgs):]...)
	for _, name := range q.Select {
		f, ok := s.Fields[name]
		if !ok {
			return CompileResult{}, fmt.Errorf("unknown select field %q", name)
		}
		res.SelectExprs = append(res.SelectExprs, f.Expr+" AS "+sqliteIdent(name))
	}
	return res, nil
}

// SQLiteWhereResult can be composed with host-owned FROM trees and optimized
// count/rollup queries. It requires only bindings for fields actually filtered;
// a row identity and row ordering are not needed for predicate compilation.
type SQLiteWhereResult struct {
	SQL  string
	Args []any
}

func CompileSQLiteWhere(terms []WhereTerm, s Schema, o SQLiteOptions) (SQLiteWhereResult, error) {
	result := SQLiteWhereResult{}
	if err := (WireQuery{Where: terms}).Validate(); err != nil {
		return result, err
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	var parts []string
	for _, term := range terms {
		if term.IsGroup() && (len(term.Any) == 0 || term.Field != "" || term.Op != "" || term.Value != "" || term.Negated) {
			return SQLiteWhereResult{}, fmt.Errorf("invalid OR group")
		}
		var alternatives []string
		var groupArgs []any
		noOp := false
		for _, c := range term.Predicates() {
			sql, args, err := sqliteLiteral(c, s, now)
			if err != nil {
				return SQLiteWhereResult{}, err
			}
			if sql == "" {
				noOp = true
			} else {
				alternatives = append(alternatives, "("+sql+")")
			}
			groupArgs = append(groupArgs, args...)
		}
		if noOp {
			continue
		}
		result.Args = append(result.Args, groupArgs...)
		parts = append(parts, "("+strings.Join(alternatives, " OR ")+")")
	}
	result.SQL = strings.Join(parts, " AND ")
	return result, nil
}

func validateSQLiteSchema(s Schema) error {
	id, ok := s.Fields[s.IDField]
	if s.Name == "" || s.IDField == "" || !ok || id.Expr == "" || !id.Sortable || (id.Kind != FieldText && id.Kind != FieldEnum && id.Kind != FieldNumber) {
		return fmt.Errorf("SQLite schema requires a bound, sortable idField and a name")
	}
	for name, f := range s.Fields {
		if name == "" || strings.ContainsRune(name, 0) || strings.TrimSpace(f.Expr) == "" {
			return fmt.Errorf("invalid SQLite field %q", name)
		}
		if f.Kind < FieldText || f.Kind > FieldTextArray {
			return fmt.Errorf("invalid kind for SQLite field %q", name)
		}
		if f.SQLiteSortField != "" {
			if target, ok := s.Fields[f.SQLiteSortField]; !ok || target.Expr != f.SortExpr {
				return fmt.Errorf("invalid SQLite sort target for %q", name)
			}
		}
		switch f.SQLiteDatetimeFormat {
		case "", "rfc3339", "utc-millis", "unix-seconds", "unix-millis":
		default:
			return fmt.Errorf("invalid SQLite datetime format for %q", name)
		}
		if f.SQLiteDatetimeFormat != "" && f.Kind != FieldDatetime {
			return fmt.Errorf("datetimeFormat on non-datetime field %q", name)
		}
	}
	for _, order := range append(append([]OrderBy{}, s.DefaultSort...), s.TiebreakSort...) {
		if f, ok := s.Fields[order.Field]; !ok || !f.Sortable {
			return fmt.Errorf("invalid SQLite schema sort field %q", order.Field)
		}
	}
	return nil
}
func sqliteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func sqlitePattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > maxRegexBytes {
		return nil, fmt.Errorf("regex too long")
	}
	re, e := regexp.Compile(pattern)
	if e != nil {
		return nil, fmt.Errorf("invalid Go/RE2 regex: %w", e)
	}
	return re, nil
}
func sqliteArray(f FieldSpec) string { return "qt_array_json(" + f.Expr + ")" }
func sqliteNull(f FieldSpec) string {
	if f.Kind == FieldTextArray {
		return "json_array_length(" + sqliteArray(f) + ") = 0"
	}
	if f.Kind == FieldText || f.Kind == FieldEnum || f.Kind == FieldDatetime {
		return "(" + f.Expr + " IS NULL OR " + f.Expr + " = '')"
	}
	return f.Expr + " IS NULL"
}
func sqliteOrderValue(f FieldSpec) string {
	switch f.Kind {
	case FieldText, FieldEnum:
		return f.Expr + " COLLATE BINARY"
	case FieldDatetime:
		if f.SQLiteDatetimeFormat == "" || f.SQLiteDatetimeFormat == "rfc3339" {
			return "qt_datetime(" + f.Expr + ")"
		}
	}
	return f.Expr
}
func sqliteDateText(f FieldSpec) string {
	switch f.SQLiteDatetimeFormat {
	case "unix-seconds":
		return "qt_unix_datetime(" + f.Expr + ",1)"
	case "unix-millis":
		return "qt_unix_datetime(" + f.Expr + ",1000)"
	default:
		return "qt_datetime(" + f.Expr + ")"
	}
}
func sqliteLiteral(c WhereClause, s Schema, now time.Time) (string, []any, error) {
	f, ok := s.Fields[c.Field]
	if !ok || !f.ServerFilter {
		return "", nil, fmt.Errorf("unknown or disabled filter field %q", c.Field)
	}
	if strings.TrimSpace(f.Expr) == "" || f.Kind < FieldText || f.Kind > FieldTextArray {
		return "", nil, fmt.Errorf("invalid SQLite binding for %q", c.Field)
	}
	if !opAllowed(f.Kind, c.Op) || !fieldOpEnabled(f, c.Op) {
		return "", nil, fmt.Errorf("field %q: op %q is not enabled", c.Field, c.Op)
	}
	if c.Value == "" && c.Op != "is_null" && c.Op != "is_not_null" && ((c.Op != "=" && c.Op != "!=") || (f.Kind != FieldText && f.Kind != FieldEnum)) {
		return "", nil, nil
	}
	expr := f.Expr
	var sql string
	var args []any
	switch c.Op {
	case "is_null":
		sql = sqliteNull(f)
	case "is_not_null":
		sql = "NOT (" + sqliteNull(f) + ")"
	case "=", "!=", ">", ">=", "<", "<=":
		var value any = c.Value
		comparisonOp := c.Op
		constant := ""
		if f.Kind == FieldDatetime {
			var t time.Time
			var err error
			if strings.HasPrefix(c.Value, "+") || strings.HasPrefix(c.Value, "-") {
				offset, e := parseRelativeDuration(c.Value)
				err = e
				if e == nil {
					t, err = relativeTimestamp(now, offset)
				}
			} else {
				t, err = sqliteParseTime(c.Value)
			}
			if err != nil {
				return "", nil, err
			}
			if t.UTC().Year() < 0 || t.UTC().Year() > 9999 {
				return "", nil, fmt.Errorf("datetime outside years 0000..9999")
			}
			switch f.SQLiteDatetimeFormat {
			case "utc-millis":
				// Keep indexed comparisons exact when a filter lies between stored milliseconds.
				if remainder := t.Nanosecond() % 1_000_000; remainder != 0 {
					switch c.Op {
					case "=":
						constant = "0"
					case "!=":
						constant = "1"
					case ">", ">=":
						comparisonOp = ">="
						t = t.Add(time.Duration(1_000_000 - remainder))
						if t.UTC().Year() > 9999 {
							constant = "0"
						}
					case "<", "<=":
						comparisonOp = "<="
						t = t.Add(-time.Duration(remainder))
					}
				}
				value = t.UTC().Format("2006-01-02T15:04:05.000Z")
			case "unix-millis":
				value = float64(t.Unix())*1000 + float64(t.Nanosecond())/1e6
			case "unix-seconds":
				value = float64(t.Unix()) + float64(t.Nanosecond())/1e9
			default:
				expr = "qt_datetime(" + expr + ")"
				value = t.UTC().Format("2006-01-02T15:04:05.000000000Z")
			}
		} else if f.Kind == FieldNumber || f.Kind == FieldBool {
			v, e := coerce(f.Kind, c.Value)
			if e != nil {
				return "", nil, e
			}
			value = v
			if n, ok := v.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
				return "", nil, fmt.Errorf("filter requires a finite number")
			}
		} else {
			expr = "COALESCE(" + expr + ",'') COLLATE BINARY"
		}
		op := comparisonOp
		if op == "!=" {
			op = "<>"
		}
		sql = expr + " " + op + " ?"
		args = []any{value}
		if c.Op == "!=" && f.Kind != FieldText && f.Kind != FieldEnum {
			sql = "(" + expr + " IS NULL OR " + sql + ")"
		}
		if constant != "" {
			sql = constant
			args = nil
		}
	case "contains", "starts_with":
		cmp := "> 0"
		if c.Op == "starts_with" {
			cmp = "= 1"
		}
		sql = "instr(qt_lower(" + expr + "),qt_lower(?)) " + cmp
		args = []any{c.Value}
	case "ends_with":
		sql = "qt_ends_with(" + expr + ",?)"
		args = []any{c.Value}
	case "length_gt", "length_lt", "length_eq":
		limit, e := strconv.ParseInt(c.Value, 10, 64)
		if e != nil || limit < 0 || limit > sqliteSafeInteger || strings.HasPrefix(c.Value, "+") || (len(c.Value) > 1 && c.Value[0] == '0') {
			return "", nil, fmt.Errorf("invalid string length %q", c.Value)
		}
		op := map[string]string{"length_gt": ">", "length_lt": "<", "length_eq": "="}[c.Op]
		sql = "qt_length(" + expr + ") " + op + " ?"
		args = []any{limit}
	case "matches_regex", "not_matches_regex":
		if _, e := sqlitePattern(c.Value); e != nil {
			return "", nil, e
		}
		sql = "qt_regexp(?,CAST(" + expr + " AS TEXT))"
		if c.Op == "not_matches_regex" {
			sql = "NOT (" + sql + ")"
		}
		args = []any{c.Value}
	case "includes":
		comparison := "_qt_element.value COLLATE BINARY = ?"
		if !f.ArrayCaseSensitive {
			comparison = "qt_lower(_qt_element.value) = qt_lower(?)"
		}
		sql = "EXISTS (SELECT 1 FROM json_each(" + sqliteArray(f) + ") AS _qt_element WHERE " + comparison + ")"
		args = []any{c.Value}
	}
	if c.Negated {
		sql = "(NOT (" + sql + ") AND NOT (" + sqliteNull(f) + "))"
	}
	return sql, args, nil
}

// CompileSQLiteAggregation supports the six basic aggregate operations. Modern
// expressions, scopes, distributions and paired metrics are explicitly refused.
// Numeric measures are checked before and after reductions; unsafe integers,
// non-finite values, and nonnumeric SQLite storage fail rather than becoming 0.
func CompileSQLiteAggregation(a AggSpec, s Schema) (AggCompileResult, error) {
	if err := validateSQLiteSchema(s); err != nil {
		return AggCompileResult{}, err
	}
	if err := (WireQuery{Aggregations: []AggSpec{a}}).Validate(); err != nil {
		return AggCompileResult{}, err
	}
	// Reuse the common aggregate allowlists and modern-request rejection.
	res, err := CompileAggregation(a, s)
	if err != nil {
		return res, err
	}
	if a.Field != "" && s.Fields[a.Field].Kind == FieldNumber && a.Op != "count" {
		f := s.Fields[a.Field]
		safe := s
		safe.Fields = make(map[string]FieldSpec, len(s.Fields))
		for k, v := range s.Fields {
			safe.Fields[k] = v
		}
		f.Expr = "qt_number(" + f.Expr + ")"
		safe.Fields[a.Field] = f
		res, err = CompileAggregation(a, safe)
		if err != nil {
			return res, err
		}
		if a.Op != "count_distinct" {
			raw, _ := aggValueExpr(a, safe)
			res.SelectExprs[len(a.GroupBy)] = "qt_number(" + raw + ") AS \"value\""
		}
	}
	if a.Field != "" && (s.Fields[a.Field].Kind == FieldText || s.Fields[a.Field].Kind == FieldEnum) {
		f := s.Fields[a.Field]
		f.Expr = "(" + f.Expr + ") COLLATE BINARY"
		copySchema := s
		copySchema.Fields = map[string]FieldSpec{}
		for k, v := range s.Fields {
			copySchema.Fields[k] = v
		}
		copySchema.Fields[a.Field] = f
		value, err := aggValueExpr(a, copySchema)
		if err != nil {
			return res, err
		}
		res.SelectExprs[len(a.GroupBy)] = value + ` AS "value"`
	}
	// Quote output names losslessly and make text/date group ordering explicit.
	groups := make([]string, 0, len(a.GroupBy))
	for i, name := range a.GroupBy {
		f := s.Fields[name]
		if f.Kind == FieldTextArray {
			return AggCompileResult{}, fmt.Errorf("SQLite cannot group textarray field %q", name)
		}
		expr := sqliteGroupValue(f)
		groups = append(groups, expr)
		res.SelectExprs[i] = expr + " AS " + sqliteIdent(fmt.Sprintf("g%d", i))
	}
	res.GroupBySQL = strings.Join(groups, ", ")
	if a.Field != "" {
		f := s.Fields[a.Field]
		if f.Kind == FieldDatetime {
			f.Expr = sqliteOrderValue(f)
			copySchema := s
			copySchema.Fields = map[string]FieldSpec{}
			for k, v := range s.Fields {
				copySchema.Fields[k] = v
			}
			copySchema.Fields[a.Field] = f
			value, err := aggValueExpr(a, copySchema)
			if err != nil {
				return res, err
			}
			res.SelectExprs[len(a.GroupBy)] = value + ` AS "value"`
		}
	}
	return res, nil
}
func sqliteGroupValue(f FieldSpec) string {
	if f.Kind == FieldText || f.Kind == FieldEnum {
		return f.Expr + " COLLATE BINARY"
	}
	if f.Kind == FieldDatetime {
		return sqliteOrderValue(f)
	}
	return f.Expr
}

// SQLiteDistinctCompile describes a literal autocomplete query. ArraySQL is a
// CROSS JOIN of individual JSON text-array elements, empty for scalar fields.
// Hosts reserve aliases beginning _qt_. IsNullExpr describes source nullity,
// before the element join, so empty arrays still contribute to hasNull.
type SQLiteDistinctCompile struct {
	Expr       string
	SearchSQL  string
	Args       []any
	ArraySQL   string
	IsNullExpr string
}

func CompileSQLiteDistinct(field, search string, s Schema) (SQLiteDistinctCompile, error) {
	f, ok := s.Fields[field]
	if !ok || !f.ServerFilter || strings.TrimSpace(f.Expr) == "" {
		return SQLiteDistinctCompile{}, fmt.Errorf("unknown or disabled filter field %q", field)
	}
	if len(search) > maxFilterValueBytes {
		return SQLiteDistinctCompile{}, fmt.Errorf("distinct search too long")
	}
	result := SQLiteDistinctCompile{Expr: f.Expr, IsNullExpr: sqliteNull(f)}
	if f.Kind == FieldDatetime {
		result.Expr = sqliteDateText(f)
	}
	if f.Kind == FieldTextArray {
		result.ArraySQL = " CROSS JOIN json_each(" + sqliteArray(f) + ") AS _qt_distinct"
		result.Expr = "_qt_distinct.value"
	}
	if search != "" {
		result.SearchSQL = "instr(qt_lower(CAST(" + result.Expr + " AS TEXT)),qt_lower(?))>0"
		result.Args = []any{search}
	}
	return result, nil
}
