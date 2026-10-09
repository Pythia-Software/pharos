package querytable

import (
	"context"
	"fmt"
	"strings"
)

// SQLiteExpressionProfile has distinct numeric/storage/collation semantics from
// PostgreSQL. Requires SQLite 3.38+ and both v1 and v2 function descriptors.
const SQLiteExpressionProfile = "qt-sqlite-v1"

func (b *planBuilder) profile() string {
	if b.sqlite {
		return SQLiteExpressionProfile
	}
	return ServerExpressionProfile
}
func (b *planBuilder) collation() string {
	if b.sqlite {
		return " COLLATE BINARY"
	}
	return ` COLLATE "C"`
}

// CompileSQLiteMetrics validates the entire v2 batch before returning any plans.
// Execute every plan and resolve definitions in the same SQLite transaction.
// SourceSQL uses numbered ?1..?N parameters reserved for SourceArgs.
func CompileSQLiteMetrics(ctx context.Context, q MetricQuery, s Schema, o PlanOptions) (MetricBatchPlan, error) {
	return compileMetricsDialect(ctx, q, s, o, true)
}
func CompileSQLiteComputedRows(ctx context.Context, q WireQuery, s Schema, o PlanOptions) (SQLPlan, error) {
	return compileComputedRowsDialect(ctx, q, s, o, true)
}
func CompileSQLiteRowsV2(ctx context.Context, q ServerQueryV2, s Schema, o PlanOptions) (SQLPlan, error) {
	if q.Version != 2 {
		return SQLPlan{}, diagnostic("version", "row protocol version must be 2")
	}
	if q.Profile == "" {
		return SQLPlan{}, diagnostic("profile_required", "computed row request must specify profile")
	}
	if len(q.Diagnostics) > 0 {
		return SQLPlan{}, diagnostic("residual_query", "row query contains unresolved diagnostics")
	}
	o, e := requestOptionsDialect(ctx, true, q.Profile, q.PlanToken, q.Snapshot, q.ExpectedRevisions, o)
	if e != nil {
		return SQLPlan{}, e
	}
	return CompileSQLiteComputedRows(ctx, q.WireQuery, s, o)
}
func CompileSQLiteRowExpression(ctx context.Context, source string, s Schema, o PlanOptions) (SQLPlan, error) {
	return compileRowExpressionDialect(ctx, source, s, o, true)
}
func (b *planBuilder) sqliteField(name string, v ValueSQL) (ValueSQL, error) {
	f := b.schema.Fields[name]
	if v.Type == "number" && !f.ExpressionNumeric {
		return v, &PlanDiagnostic{Code: "numeric_domain_required", Message: "numeric binding must opt into bounded arithmetic", Field: name}
	}
	if v.Type == "unsupported" {
		return v, diagnostic("unsupported_type", "arrays are outside the expression profile")
	}
	raw := v.Value
	if v.Type == "datetime" {
		payload := b.emit("qt_v2_datetime("+raw+","+b.param(f.SQLiteDatetimeFormat, "text")+")", "NULL", "text")
		out := b.emit("json_extract("+payload.Value+",'$.value') COLLATE BINARY", "json_extract("+payload.Value+",'$.error')", "datetime")
		b.fields[name] = out
		b.checked[name] = true
		return out, nil
	}
	// Storage failures remain per-value diagnostics for lazy formula branches.
	errSQL := "qt_v2_field_error(" + raw + ",'" + v.Type + "')"
	out := b.emit("CASE WHEN "+errSQL+" IS NULL THEN "+raw+" END"+b.collation(), errSQL, v.Type)
	b.fields[name] = out
	b.checked[name] = true
	return out, nil
}
func (b *planBuilder) sqliteArithmetic(op string, a []ValueSQL, e string) (ValueSQL, error) {
	right := "NULL"
	if len(a) > 1 {
		right = a[1].Value
	}
	payload := b.emit("qt_v2_arithmetic('"+op+"',"+a[0].Value+","+right+")", e, "text")
	out := b.emit("json_extract("+payload.Value+",'$.value')", "COALESCE("+payload.Error+",json_extract("+payload.Value+",'$.error'))", "number")
	return b.emit("CASE WHEN "+out.Error+" IS NULL THEN "+out.Value+" END", out.Error, "number"), nil
}
func (b *planBuilder) sqliteReduction(op, raw, inputError, typ string) (string, string) {
	errSQL := "MIN(" + inputError + ")"
	switch op {
	case "count":
		return "CAST(COUNT(" + raw + ") AS REAL)", errSQL
	case "count_distinct":
		return "CAST(COUNT(DISTINCT " + raw + ") AS REAL)", errSQL
	case "sum", "avg", "median":
		p := "qt_v2_" + op + "(" + raw + ")"
		return "json_extract(" + p + ",'$.value')", "COALESCE(" + errSQL + ",json_extract(" + p + ",'$.error'))"
	default:
		v := strings.ToUpper(op) + "(" + raw + ")"
		if typ == "number" {
			errSQL = "COALESCE(" + errSQL + ",qt_v2_field_error(" + v + ",'number'))"
		}
		return v, errSQL
	}
}
func (b *planBuilder) sqliteFilter(where []WhereTerm) error {
	s := b.schema
	s.Fields = map[string]FieldSpec{}
	for name, f := range b.schema.Fields {
		f.Expr = b.fields[name].Value
		f.SortExpr = f.Expr
		s.Fields[name] = f
	}
	p, e := CompileSQLiteWhere(where, s, SQLiteOptions{Now: b.options.Now})
	if e != nil {
		return e
	}
	sql := sqliteNumberParameters(p.SQL, len(b.args))
	b.args = append(b.args, p.Args...)
	if sql != "" {
		b.stage("SELECT * FROM " + b.rel + " WHERE " + sql)
		// Filters over raw bindings can be flattened into an indexed window.
		b.stages[len(b.stages)-1].Materialized = false
	}
	return nil
}

// Only compiler-owned anonymous parameters are rewritten, never quoted literals.
func sqliteNumberParameters(sql string, start int) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					i++
					out.WriteByte(quote)
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			out.WriteByte(c)
		} else if c == '?' {
			start++
			fmt.Fprintf(&out, "?%d", start)
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}

func (b *planBuilder) sqliteOrder(terms []OrderBy) (string, error) {
	if len(terms) == 0 {
		terms = b.schema.DefaultSort
	}
	terms = append(append([]OrderBy{}, terms...), b.schema.TiebreakSort...)
	if b.schema.IDField == "" {
		return "", diagnostic("identity_required", "stable ID field required for row windows")
	}
	terms = append(terms, OrderBy{Field: b.schema.IDField, Dir: "asc"})
	out := []string{}
	for _, term := range terms {
		if term.Dir != "asc" && term.Dir != "desc" {
			return "", diagnostic("sort", "direction must be asc or desc")
		}
		name := term.Field
		if !strings.HasPrefix(name, "@computed/") {
			f, ok := b.schema.Fields[name]
			if !ok || !f.Sortable {
				return "", diagnostic("sort", "field is not sortable: "+name)
			}
			if f.SQLiteSortField != "" || f.SortExpr != "" && f.SortExpr != f.Expr {
				target, ok := b.schema.Fields[f.SQLiteSortField]
				if !ok || target.Expr != f.SortExpr {
					return "", diagnostic("sort", "alternate sort binding requires SQLiteSortField naming a bound schema field")
				}
				name = f.SQLiteSortField
			}
		} else if term.Extract != nil {
			return "", diagnostic("unsupported_regex", "computed sorts do not support extraction")
		}
		// IDs are guaranteed scalar and canonical utc-millis is trusted host
		// storage. Their raw BINARY keys preserve indexes; selected values and
		// formula operands still pass the ordinary value/error guards.
		f, base := b.schema.Fields[name]
		if base && term.Extract == nil && (name == b.schema.IDField && f.Kind == FieldText || f.Kind == FieldDatetime && f.SQLiteDatetimeFormat == "utc-millis") {
			raw := b.fields[name].Value
			b.dependencies[name] = true
			sql, err := orderTerm(raw+" COLLATE BINARY", term.Dir, term.Nulls)
			if err != nil {
				return "", err
			}
			out = append(out, sql)
			continue
		}
		v, e := b.field(name)
		if e != nil {
			return "", e
		}
		v = b.key(v)
		if term.Extract != nil {
			if _, e = sqlitePattern(term.Extract.Regex); e != nil {
				return "", e
			}
			v = b.emit("qt_regexp_extract("+b.param(term.Extract.Regex, "text")+",CAST("+v.Value+" AS TEXT))", v.Error, "text")
		}
		sql, e := orderTerm(v.Value, term.Dir, term.Nulls)
		if e != nil {
			return "", e
		}
		out = append(out, sql)
	}
	return strings.Join(out, ","), nil
}

func (b *planBuilder) nullError() string {
	if b.sqlite {
		return "NULL"
	}
	return "NULL::text"
}
func (b *planBuilder) errorLiteral(code string) string {
	suffix := "::text"
	if b.sqlite {
		suffix = ""
	}
	return "'" + code + "'" + suffix
}
