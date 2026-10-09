package querytable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefinitionResolver must read the authorized catalogue in one coherent snapshot.
// IDs have no @computed/ prefix. Implementations must not trust a client source.
type DefinitionResolver interface {
	ResolveComputed(context.Context, string) (ComputedColumn, error)
}
type DefinitionResolverFunc func(context.Context, string) (ComputedColumn, error)

func (f DefinitionResolverFunc) ResolveComputed(c context.Context, id string) (ComputedColumn, error) {
	return f(c, id)
}

// PlanOptions contains trusted host configuration. SourceSQL is a SELECT whose
// output exposes the aliases used by Schema field bindings (normally alias r).
// SourceArgs occupy $1..$N for PostgreSQL or ?1..?N for SQLite. SourceSQL
// must include authorization predicates.
type PlanOptions struct {
	// ComputedGroupable is a trusted host allowlist of computed IDs permitted as group keys.
	ComputedGroupable map[string]bool
	// ValidatePlanToken must verify the host token against the current actor, dataset, profile, snapshot and expiry.
	ValidatePlanToken func(context.Context, string) error
	// ValidateSnapshot verifies and binds a requested snapshot to the active transaction.
	ValidateSnapshot  func(context.Context, string) error
	SourceSQL         string
	SourceArgs        []any
	Resolver          DefinitionResolver
	ExpectedRevisions map[string]string // required for every reached computed ID
	Now               time.Time
	// Identity binds the fingerprint to actor/dataset/schema and data snapshot.
	Identity string
}
type SQLStage struct {
	Name, SQL    string
	Materialized bool
}
type ValueSQL struct{ Value, Error, Type string }
type OutputColumn struct{ Field, ValueAlias, ErrorAlias, Type string }
type SQLiteReductionColumn struct{ Name, SQL string }
type SQLPlan struct {
	SQLiteReductionColumns []SQLiteReductionColumn
	SQLiteReductionFrom    string
	// Scalar SQLite metrics expose their reduction boundary for transaction-local
	// reuse. Zero means no reusable reduction (rows and distributions).
	SQLiteReductionStage        int
	SQL                         string
	Args                        []any
	Stages                      []SQLStage
	Columns                     []OutputColumn
	Dependencies                []string
	ResolvedRevisions           map[string]string
	Profile, Fingerprint, Scope string
	// A host executing row/metric plans together must provide one data snapshot.
	RequiresSnapshot bool
}
type planBuilder struct {
	sqlite       bool
	projection   []string
	emitInputs   map[int][]string
	rowMemo      map[string]ValueSQL
	ctx          context.Context
	schema       Schema
	options      PlanOptions
	args         []any
	stages       []SQLStage
	rel          string
	serial       int
	fields       map[string]ValueSQL
	revisions    map[string]string
	visiting     map[string]bool
	dependencies map[string]bool
	checked      map[string]bool
	definitions  map[string]ComputedColumn
	nodes        int
}

func newPlan(ctx context.Context, s Schema, o PlanOptions) (*planBuilder, error) {
	return newPlanDialect(ctx, s, o, false)
}
func newPlanDialect(ctx context.Context, s Schema, o PlanOptions, sqlite bool) (*planBuilder, error) {
	if sqlite {
		if e := validateSQLiteSchema(s); e != nil {
			return nil, e
		}
	}
	if strings.TrimSpace(o.SourceSQL) == "" {
		return nil, diagnostic("source_required", "host must supply authorized source SELECT")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	b := &planBuilder{sqlite: sqlite, emitInputs: map[int][]string{}, rowMemo: map[string]ValueSQL{}, ctx: ctx, schema: s, options: o, args: append([]any{}, o.SourceArgs...), fields: map[string]ValueSQL{}, revisions: map[string]string{}, visiting: map[string]bool{}, dependencies: map[string]bool{}, checked: map[string]bool{}, definitions: map[string]ComputedColumn{}}
	// Project bindings exactly once in the source namespace. Internal aliases do
	// not depend on request names, labels or trusted SQL identifiers.
	names := make([]string, 0, len(s.Fields))
	for n := range s.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	cols := []string{}
	for i, n := range names {
		f := s.Fields[n]
		if f.Expr == "" {
			return nil, diagnostic("schema", "empty field SQL binding")
		}
		v := fmt.Sprintf("f%d", i)
		expr := f.Expr
		if !sqlite && (f.Kind == FieldText || f.Kind == FieldEnum) {
			expr = "(" + expr + ")::text"
		}
		cols = append(cols, expr+" AS "+v)
		b.fields[n] = ValueSQL{Value: v, Error: b.nullError(), Type: expressionType(f.Kind)}
	}
	if len(cols) > 512 {
		return nil, diagnostic("schema_limit", "v2 profile supports at most 512 projected schema fields")
	}
	if len(cols) == 0 {
		return nil, diagnostic("schema", "empty schema")
	}
	b.stage("SELECT " + strings.Join(cols, ",") + " FROM (" + o.SourceSQL + ") AS r")
	b.stages[0].Materialized = false
	for i := range names {
		b.projection = append(b.projection, fmt.Sprintf("f%d", i))
	}
	return b, nil
}
func expressionType(k FieldKind) string {
	switch k {
	case FieldNumber:
		return "number"
	case FieldBool:
		return "bool"
	case FieldDatetime:
		return "datetime"
	case FieldText, FieldEnum:
		return "text"
	}
	return "unsupported"
}
func (b *planBuilder) stage(sql string) {
	// Explicit projections/reductions start a different namespace. Only prune
	// the known row-alias lineage; later grouped/distribution stages stay intact.
	if !strings.HasPrefix(sql, "SELECT *") {
		b.projection = nil
	}

	// Pass-through filters/windows also copy the row namespace. Record their
	// inputs so the same liveness pass can narrow them as formula stages.
	if b.projection != nil && strings.HasPrefix(sql, "SELECT * FROM ") {
		b.emitInputs[len(b.stages)] = append([]string{}, b.projection...)
	}
	name := fmt.Sprintf("qt%d", len(b.stages))
	b.stages = append(b.stages, SQLStage{Name: name, SQL: sql, Materialized: true})
	b.rel = name
}
func (b *planBuilder) param(v any, typ string) string {
	b.args = append(b.args, v)
	if b.sqlite {
		return fmt.Sprintf("?%d", len(b.args))
	}
	return fmt.Sprintf("$%d::%s", len(b.args), typ)
}
func (b *planBuilder) emit(v, e, t string) ValueSQL {
	b.serial++
	a := fmt.Sprintf("v%d", b.serial)
	z := fmt.Sprintf("e%d", b.serial)
	if b.projection != nil {
		b.emitInputs[len(b.stages)] = append([]string{}, b.projection...)
	}
	b.stage("SELECT *, " + v + " AS " + a + ", " + e + " AS " + z + " FROM " + b.rel)
	if b.projection != nil {
		b.projection = append(b.projection, a, z)
	}
	if t == "null" {
		a = "NULL"
	}
	return ValueSQL{a, z, t}
}

var rowAliasPattern = regexp.MustCompile(`\b(?:f|v|e)[0-9]+\b`)

// Keep each guarded expression materialized once, but stop copying dead input
// columns through every intermediate population. Liveness is conservative:
// all later explicit references count, including error, sort, filter and branch
// references. Parameters/literals cannot introduce executable identifiers.
func (b *planBuilder) pruneRowInputs(final string) {
	live := map[string]bool{}
	for _, alias := range rowAliasPattern.FindAllString(final, -1) {
		live[alias] = true
	}
	for i := len(b.stages) - 1; i >= 0; i-- {
		stage := &b.stages[i]
		body := stage.SQL
		if inputs, ok := b.emitInputs[i]; ok {
			passThrough := strings.HasPrefix(body, "SELECT * FROM ")
			suffix := strings.TrimPrefix(body, "SELECT *, ")
			if passThrough {
				suffix = strings.TrimPrefix(body, "SELECT * ")
			}
			for _, alias := range rowAliasPattern.FindAllString(suffix, -1) {
				live[alias] = true
			}
			retained := []string{}
			for _, alias := range inputs {
				if live[alias] {
					retained = append(retained, alias)
				}
			}
			prefix := "SELECT "
			if passThrough {
				// COUNT() can require row cardinality without any source values.
				if len(retained) == 0 {
					prefix += "1 AS qt_row "
				} else {
					prefix += strings.Join(retained, ",") + " "
				}
			} else if len(retained) > 0 {
				prefix += strings.Join(retained, ",") + ", "
			}
			stage.SQL = prefix + suffix
		} else {
			for _, alias := range rowAliasPattern.FindAllString(body, -1) {
				live[alias] = true
			}
		}
	}
}
func (b *planBuilder) finish(sql, scope string, cols []OutputColumn) SQLPlan {
	b.pruneRowInputs(sql)
	ctes := []string{}
	for _, s := range b.stages {
		mode := "MATERIALIZED"
		if !s.Materialized {
			mode = "NOT MATERIALIZED"
		}
		ctes = append(ctes, s.Name+" AS "+mode+" ("+s.SQL+")")
	}
	full := "WITH " + strings.Join(ctes, ",\n") + "\n" + sql
	deps := []string{}
	for d := range b.dependencies {
		deps = append(deps, d)
	}
	sort.Strings(deps)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%#v", b.profile(), b.options.Identity, full, b.args)
	for _, d := range deps {
		fmt.Fprintf(h, "\x00%s=%s", d, b.revisions[strings.TrimPrefix(d, "@computed/")])
	}
	return SQLPlan{SQL: full, Args: b.args, Stages: b.stages, Columns: cols, Dependencies: deps, ResolvedRevisions: b.revisions, Profile: b.profile(), Fingerprint: hex.EncodeToString(h.Sum(nil)), Scope: scope, RequiresSnapshot: true}
}
func (b *planBuilder) field(name string) (ValueSQL, error) {
	if len(b.stages) > 500 {
		return ValueSQL{}, diagnostic("stage_limit", "SQL stage budget exceeded")
	}
	if err := b.ctx.Err(); err != nil {
		return ValueSQL{}, err
	}
	b.dependencies[name] = true
	if v, ok := b.fields[name]; ok {
		if v.Type == "unsupported" {
			return v, diagnostic("unsupported_type", "arrays are outside the server expression profile")
		}
		if b.sqlite && !strings.HasPrefix(name, "@computed/") && !b.checked[name] {
			return b.sqliteField(name, v)
		}
		if v.Type == "number" && !strings.HasPrefix(name, "@computed/") && !b.checked[name] {
			f := b.schema.Fields[name]
			if !f.ExpressionNumeric {
				return v, &PlanDiagnostic{Code: "numeric_domain_required", Message: "numeric SQL binding must opt into the bounded arithmetic profile", Field: name}
			}
			raw := b.emit("("+v.Value+")::text::numeric", b.nullError(), "number").Value
			valid := "(abs(" + raw + ") <= 1e100::numeric AND (" + raw + "=0 OR abs(" + raw + ") >= 1e-300::numeric))"
			v = b.emit("CASE WHEN "+valid+" THEN ("+raw+")::double precision END", "CASE WHEN "+v.Value+" IS NOT NULL AND NOT ("+valid+") THEN 'numeric_range'::text END", "number")
			b.fields[name] = v
			b.checked[name] = true
		}
		if v.Type == "text" && !strings.HasPrefix(name, "@computed/") && !b.checked[name] {
			valid := "octet_length(" + v.Value + ") <= 100000"
			// Formula values use deterministic codepoint ordering. Keep the
			// source projection's native collation for host text filters/LOWER.
			v = b.emit("(CASE WHEN "+valid+" THEN "+v.Value+" END) COLLATE \"C\"", "CASE WHEN NOT ("+valid+") THEN 'text_range'::text END", "text")
			b.fields[name] = v
			b.checked[name] = true
		}
		return v, nil
	}
	if !strings.HasPrefix(name, "@computed/") {
		return ValueSQL{}, &PlanDiagnostic{Code: "unknown_field", Message: "field is not in schema allowlist", Field: name}
	}
	id := strings.TrimPrefix(name, "@computed/")
	if b.visiting[id] {
		return ValueSQL{}, diagnostic("dependency_cycle", "computed dependency cycle at "+id)
	}
	if len(b.revisions) >= 64 || len(b.visiting) >= 32 {
		return ValueSQL{}, diagnostic("dependency_limit", "computed graph budget exceeded")
	}
	if b.options.Resolver == nil {
		return ValueSQL{}, diagnostic("resolver_required", "authoritative computed resolver required")
	}
	c, err := b.options.Resolver.ResolveComputed(b.ctx, id)
	if err != nil {
		return ValueSQL{}, err
	}
	if c.ID != id {
		return ValueSQL{}, diagnostic("definition_invalid", "resolver returned a different ID")
	}
	if err = validateComputed(c); err != nil {
		return ValueSQL{}, err
	}
	expected, ok := b.options.ExpectedRevisions[id]
	if !ok || expected == "" || c.Revision != expected {
		return ValueSQL{}, &PlanDiagnostic{Code: "definition_changed", Message: "missing or mismatched expected revision", Field: name}
	}
	b.definitions[id] = c
	b.visiting[id] = true
	b.revisions[id] = c.Revision
	n, err := parseExpression(c.Expression.Source)
	if err != nil {
		return ValueSQL{}, err
	}
	v, err := b.row(n)
	delete(b.visiting, id)
	if err == nil {
		b.fields[name] = v
	}
	return v, err
}
func mergeType(a, c string) (string, error) {
	if a == "null" {
		return c, nil
	}
	if c == "null" || a == c {
		return a, nil
	}
	return "", diagnostic("type_error", "incompatible expression types "+a+" and "+c)
}
func requireType(v ValueSQL, t string) error {
	if v.Type != t && v.Type != "null" {
		return diagnostic("type_error", "expected "+t+", got "+v.Type)
	}
	return nil
}
func (b *planBuilder) row(n *exprNode) (result ValueSQL, err error) {
	key := expressionKey(n)
	if cached, ok := b.rowMemo[key]; ok {
		return cached, nil
	}
	defer func() {
		if err == nil {
			b.rowMemo[key] = result
		}
	}()

	if err := b.ctx.Err(); err != nil {
		return ValueSQL{}, err
	}
	b.nodes++
	if b.nodes > 2048 {
		return ValueSQL{}, diagnostic("expansion_limit", "expanded expression budget exceeded")
	}
	if n.op == "field" {
		return b.field(n.text)
	}
	if n.op == "literal" {
		switch v := n.value.(type) {
		case nil:
			return ValueSQL{"NULL", b.nullError(), "null"}, nil
		case float64:
			if v > 1e100 || v < -1e100 || (v != 0 && v < 1e-300 && v > -1e-300) {
				return ValueSQL{}, diagnostic("numeric_range", "literal outside profile range")
			}
			return ValueSQL{b.param(v, "double precision"), b.nullError(), "number"}, nil
		case string:
			return ValueSQL{b.param(v, "text") + b.collation(), b.nullError(), "text"}, nil
		case bool:
			return ValueSQL{b.param(v, "boolean"), b.nullError(), "bool"}, nil
		}
	}
	args := []ValueSQL{}
	for _, a := range n.args {
		v, e := b.row(a)
		if e != nil {
			return v, e
		}
		args = append(args, v)
	}
	return b.operation(n.op, args)
}
func (b *planBuilder) operation(op string, a []ValueSQL) (ValueSQL, error) {
	if len(b.stages) > 500 {
		return ValueSQL{}, diagnostic("stage_limit", "SQL stage budget exceeded")
	}
	arity := 2
	switch op {
	case "IF":
		arity = 3
	case "ABS", "IS_NULL", "unary+", "unary-", "unaryNOT":
		arity = 1
	case "COALESCE":
		arity = len(a)
		if arity < 1 || arity > 50 {
			return ValueSQL{}, diagnostic("arity", "COALESCE requires 1–50 arguments")
		}
	}
	if len(a) != arity {
		return ValueSQL{}, diagnostic("arity", "invalid argument count for "+op)
	}
	errors := []string{}
	for _, v := range a {
		errors = append(errors, v.Error)
	}
	e := "COALESCE(" + strings.Join(append(errors, b.nullError()), ",") + ")"
	v := ""
	t := "number"
	numeric := false
	floatValue := ""
	switch op {
	case "IF":
		if err := requireType(a[0], "bool"); err != nil {
			return ValueSQL{}, err
		}
		var err error
		t, err = mergeType(a[1].Type, a[2].Type)
		if err != nil {
			return ValueSQL{}, err
		}
		v = "CASE WHEN " + a[0].Value + " THEN " + a[1].Value + " ELSE " + a[2].Value + " END"
		e = "COALESCE(" + a[0].Error + ",CASE WHEN " + a[0].Value + " THEN " + a[1].Error + " ELSE " + a[2].Error + " END)"
	case "COALESCE":
		t = "null"
		for _, x := range a {
			var err error
			t, err = mergeType(t, x.Type)
			if err != nil {
				return ValueSQL{}, err
			}
		}
		v = "NULL"
		e = b.nullError()
		for i := len(a) - 1; i >= 0; i-- {
			x := a[i]
			cond := x.Error + " IS NOT NULL OR " + x.Value + " IS NOT NULL"
			v = "CASE WHEN " + cond + " THEN " + x.Value + " ELSE " + v + " END"
			e = "CASE WHEN " + cond + " THEN " + x.Error + " ELSE " + e + " END"
		}
	case "NULLIF":
		var err error
		t, err = mergeType(a[0].Type, a[1].Type)
		if err != nil {
			return ValueSQL{}, err
		}
		v = "NULLIF(" + a[0].Value + "," + a[1].Value + ")"
	case "IS_NULL":
		t = "bool"
		v = "(" + a[0].Value + " IS NULL)"
	case "AND", "OR", "unaryNOT":
		for _, x := range a {
			if err := requireType(x, "bool"); err != nil {
				return ValueSQL{}, err
			}
		}
		t = "bool"
		if op == "unaryNOT" {
			v = "NOT (" + a[0].Value + ")"
		} else {
			v = "(" + a[0].Value + " " + op + " " + a[1].Value + ")"
			stop := "FALSE"
			if op == "OR" {
				stop = "TRUE"
			}
			e = "COALESCE(" + a[0].Error + ",CASE WHEN " + a[0].Value + " IS " + stop + " THEN " + b.nullError() + " ELSE " + a[1].Error + " END)"
		}
	case "=", "!=", "<>", "<", ">", "<=", ">=":
		typ, err := mergeType(a[0].Type, a[1].Type)
		if err != nil {
			return ValueSQL{}, err
		}
		if typ == "text" && op != "=" && op != "!=" && op != "<>" {
			return ValueSQL{}, diagnostic("collation_required", "text ordering is outside this profile")
		}
		t = "bool"
		v = "(" + a[0].Value + " " + op + " " + a[1].Value + ")"
	case "+", "-", "*", "/", "unary+", "unary-", "ABS":
		for _, x := range a {
			if err := requireType(x, "number"); err != nil {
				return ValueSQL{}, err
			}
		}
		if b.sqlite {
			return b.sqliteArithmetic(op, a, e)
		}
		numeric = true
		switch op {
		case "ABS":
			floatValue = "abs((" + a[0].Value + ")::double precision)"
		case "unary+":
			floatValue = "(" + a[0].Value + ")::double precision"
		case "unary-":
			floatValue = "-((" + a[0].Value + ")::double precision)"
		case "/":
			floatValue = "(" + a[0].Value + " / NULLIF(" + a[1].Value + ",0))"
		default:
			floatValue = "((" + a[0].Value + ")::double precision " + op + " (" + a[1].Value + ")::double precision)"
		}
		l := "(" + a[0].Value + ")::text::numeric"
		switch op {
		case "ABS":
			v = "abs(" + l + ")"
		case "unary+":
			v = l
		case "unary-":
			v = "-" + l
		case "/":
			r := "(" + a[1].Value + ")::text::numeric"
			v = l + " / NULLIF(" + r + ",0)"
			e = "COALESCE(" + e + ",CASE WHEN " + a[0].Value + " IS NOT NULL AND " + r + "=0 THEN 'divide_by_zero'::text END)"
		default:
			v = l + " " + op + " (" + a[1].Value + ")::text::numeric"
		}
	default:
		return ValueSQL{}, diagnostic("unsupported_function", "function/operator "+op+" is outside the server profile")
	}
	if numeric {
		// Compute decimal-domain arithmetic once. Range guards and the IEEE result
		// use its alias instead of repeating conversions/division per predicate.
		candidate := b.emit(v, e, "number")
		v, e = candidate.Value, candidate.Error
		safe := "(abs(" + v + ") <= 1e100::numeric AND ((" + v + ")=0 OR abs(" + v + ") >= 1e-300::numeric))"
		if op == "/" {
			floatValue = "((" + a[0].Value + ")::double precision / NULLIF(CASE WHEN " + safe + " THEN (" + a[1].Value + ")::double precision END,0))"
		}
		e = "COALESCE(" + e + ",CASE WHEN abs(" + v + ") > 1e100::numeric OR (abs(" + v + ") > 0 AND abs(" + v + ") < 1e-300::numeric) THEN 'numeric_range'::text END)"
		v = "CASE WHEN abs(" + v + ") <= 1e100::numeric AND ((" + v + ")=0 OR abs(" + v + ") >= 1e-300::numeric) THEN " + floatValue + " END"
	}
	// Materialize the error expression as an alias first to keep SQL growth linear.
	out := b.emit(v, e, t)
	clean := b.emit("CASE WHEN "+out.Error+" IS NULL THEN "+out.Value+" END", out.Error, t)
	if t == "null" {
		clean.Value = "NULL"
	}
	return clean, nil
}

func (b *planBuilder) filter(where []WhereTerm) error {
	if b.sqlite {
		return b.sqliteFilter(where)
	}
	for _, term := range where {
		for _, p := range term.Predicates() {
			if p.Op == "matches_regex" || p.Op == "not_matches_regex" {
				return diagnostic("unsupported_regex", "regex filters are outside this server profile")
			}
		}
	}
	s := b.schema
	s.Fields = map[string]FieldSpec{}
	for n, f := range b.schema.Fields {
		f.Expr = b.fields[n].Value
		f.SortExpr = f.Expr
		s.Fields[n] = f
	}
	q := WireQuery{Where: where}
	p, _, err := CompileAt(q, s, len(b.args)+1, b.options.Now)
	if err != nil {
		return err
	}
	b.args = append(b.args, p.Args...)
	if p.WhereSQL != "" {
		b.stage("SELECT * FROM " + b.rel + " WHERE " + p.WhereSQL)
	}
	return nil
}

// key projects NULL-only expressions for ORDER BY/GROUP BY, where a bare
// NULL is rejected as a non-integer constant. Keep formula values polymorphic:
// typing them as text earlier would break numeric/boolean NULL consumers.
func (b *planBuilder) key(v ValueSQL) ValueSQL {
	if v.Type == "null" {
		if b.sqlite {
			return b.emit("CAST("+v.Value+" AS TEXT)", v.Error, "text")
		}
		return b.emit("("+v.Value+")::text", v.Error, "text")
	}
	return v
}

func (b *planBuilder) order(terms []OrderBy) (string, error) {
	if b.sqlite {
		return b.sqliteOrder(terms)
	}
	if len(terms) == 0 {
		terms = b.schema.DefaultSort
	}
	terms = append(append([]OrderBy{}, terms...), b.schema.TiebreakSort...)
	// Always append stable row identity, including when the host configured no ties.
	if b.schema.IDField == "" {
		return "", diagnostic("identity_required", "stable ID field required for row windows")
	}
	terms = append(terms, OrderBy{Field: b.schema.IDField, Dir: "asc"})
	out := []string{}
	for _, term := range terms {
		if term.Extract != nil {
			return "", diagnostic("unsupported_regex", "regex sorting is unavailable in this profile")
		}
		if term.Dir != "asc" && term.Dir != "desc" {
			return "", diagnostic("sort", "direction must be asc or desc")
		}
		if !strings.HasPrefix(term.Field, "@computed/") {
			f, ok := b.schema.Fields[term.Field]
			if !ok || !f.Sortable {
				return "", diagnostic("sort", "field is not sortable: "+term.Field)
			}
			if f.SortExpr != "" && f.SortExpr != f.Expr {
				return "", diagnostic("sort", "alternate sort bindings require an explicit schema field in v2")
			}
		}
		v, err := b.field(term.Field)
		if err != nil {
			return "", err
		}
		v = b.key(v)
		sql, err := orderTerm(v.Value, term.Dir, term.Nulls)
		if err != nil {
			return "", err
		}
		out = append(out, sql)
	}
	return strings.Join(out, ","), nil
}

// CompileComputedRows resolves SELECT and hidden ORDER BY dependencies before
// applying the globally ordered page window. Computed filters are not admitted.
func CompileComputedRows(ctx context.Context, q WireQuery, s Schema, o PlanOptions) (SQLPlan, error) {
	return compileComputedRowsDialect(ctx, q, s, o, false)
}
func compileComputedRowsDialect(ctx context.Context, q WireQuery, s Schema, o PlanOptions, sqlite bool) (SQLPlan, error) {
	if err := q.Validate(); err != nil {
		return SQLPlan{}, err
	}
	b, err := newPlanDialect(ctx, s, o, sqlite)
	if err != nil {
		return SQLPlan{}, err
	}
	if err = b.filter(q.Where); err != nil {
		return SQLPlan{}, err
	}
	order, err := b.order(q.OrderBy)
	if err != nil {
		return SQLPlan{}, err
	}
	window := "SELECT * FROM " + b.rel + " ORDER BY " + order
	if q.Limit > 0 {
		window += " LIMIT " + b.param(q.Limit, "bigint")
	}
	if b.sqlite && q.Limit == 0 {
		window += " LIMIT -1"
	}
	window += " OFFSET " + b.param(q.Offset, "bigint")
	b.stage(window)
	cols := []OutputColumn{}
	selects := []string{}
	for i, n := range q.Select {
		v, e := b.field(n)
		if b.sqlite {
			if f, ok := s.Fields[n]; ok && f.Kind == FieldTextArray {
				v = b.fields[n]
				v.Type = "textarray"
				e = nil
			}
		}
		if e != nil {
			return SQLPlan{}, e
		}
		a := "column" + strconv.Itoa(i)
		z := a + "_error"
		cols = append(cols, OutputColumn{n, a, z, v.Type})
		selects = append(selects, v.Value+" AS "+a, v.Error+" AS "+z)
	}
	if len(selects) == 0 {
		return SQLPlan{}, diagnostic("select_required", "at least one selected field required")
	}
	// ORDER BY is repeated to preserve the selected page's ordering through
	// materialized select-only projections. The population window is already fixed.
	sql := "SELECT " + strings.Join(selects, ",") + " FROM " + b.rel + " ORDER BY " + order
	return b.finish(sql, "shownRows", cols), nil
}

// CompileRowExpression builds a standalone typed value/error projection over an
// authorized source, useful for host-side definition validation and previews.
func CompileRowExpression(ctx context.Context, source string, s Schema, o PlanOptions) (SQLPlan, error) {
	return compileRowExpressionDialect(ctx, source, s, o, false)
}
func compileRowExpressionDialect(ctx context.Context, source string, s Schema, o PlanOptions, sqlite bool) (SQLPlan, error) {
	b, e := newPlanDialect(ctx, s, o, sqlite)
	if e != nil {
		return SQLPlan{}, e
	}
	n, e := parseExpression(source)
	if e != nil {
		return SQLPlan{}, e
	}
	v, e := b.row(n)
	if e != nil {
		return SQLPlan{}, e
	}
	return b.finish("SELECT "+v.Value+" AS value,"+v.Error+" AS error FROM "+b.rel, "allMatching", []OutputColumn{{ValueAlias: "value", ErrorAlias: "error", Type: v.Type}}), nil
}
