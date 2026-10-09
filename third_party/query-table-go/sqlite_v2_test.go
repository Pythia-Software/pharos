package querytable

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func sqliteV2TestSchema() Schema {
	s := sqliteTestSchema()
	f := s.Fields["n"]
	f.ExpressionNumeric = true
	s.Fields["n"] = f
	return s
}
func TestSQLiteV2PlanSecurityAndProfile(t *testing.T) {
	s := sqliteV2TestSchema()
	o := PlanOptions{SourceSQL: "SELECT * FROM fixture WHERE tenant=?1", SourceArgs: []any{"authorized"}, Identity: "actor/dataset/schema"}
	ctx := context.Background()
	p, e := CompileSQLiteRowExpression(ctx, `IF([text]="'; DROP TABLE victims; --",[n]/2,NULL)`, s, o)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(p.SQL, "DROP TABLE") || strings.Contains(p.SQL, "::") || strings.Contains(p.SQL, "$1") || p.Profile != SQLiteExpressionProfile || !strings.Contains(p.SQL, "?2") {
		t.Fatal(p)
	}
	for _, source := range []string{"[missing]", "SUM([n])", "[tags]", "[text]+1", "IF(TRUE,1,'a')", "[n]::text", "1;DELETE FROM runs", "REGEX_TEST([text],'x')", strings.Repeat("(", 51) + "1" + strings.Repeat(")", 51)} {
		if _, e := CompileSQLiteRowExpression(ctx, source, s, o); e == nil {
			t.Fatal("accepted", source)
		}
	}
	other := o
	other.Identity = "other actor"
	p2, e := CompileSQLiteRowExpression(ctx, `IF([text]="'; DROP TABLE victims; --",[n]/2,NULL)`, s, other)
	if e != nil || p.Fingerprint == p2.Fingerprint {
		t.Fatal("identity absent from fingerprint", e)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := CompileSQLiteRowExpression(cancelCtx, "[n]", s, o); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestSQLiteV2PoliciesAndGraph(t *testing.T) {
	s := sqliteV2TestSchema()
	o := PlanOptions{SourceSQL: "SELECT * FROM fixture", ExpectedRevisions: map[string]string{"a": "1", "b": "1"}}
	defs := map[string]ComputedColumn{"a": {ID: "a", Label: "A", Revision: "1", Expression: ComputedExpression{Language: "qt-expr", Version: 1, Source: "[@computed/b]"}}, "b": {ID: "b", Label: "B", Revision: "1", Expression: ComputedExpression{Language: "qt-expr", Version: 1, Source: "[n]+1"}}}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) { return defs[id], nil })
	q := MetricQuery{Version: 2, Profile: SQLiteExpressionProfile, Metrics: []AggSpec{{ID: "m", Expression: "SUM([@computed/a])"}}}
	p, e := CompileSQLiteMetrics(context.Background(), q, s, o)
	if e != nil || len(p.Metrics[0].ResolvedRevisions) != 2 {
		t.Fatal(p, e)
	}
	q.ExpectedRevisions = o.ExpectedRevisions
	originalExpected := o.ExpectedRevisions
	o.ExpectedRevisions = nil
	combined, e := CompileSQLiteExecution(context.Background(), WireQuery{Select: []string{"id", "@computed/a"}, Limit: 1}, q, s, o)
	if e != nil || combined.ResolvedRevisions["b"] != "1" {
		t.Fatal(combined, e)
	}
	o.ExpectedRevisions = originalExpected
	q.ExpectedRevisions = nil
	f := s.Fields["n"]
	f.AggregateOps = []string{"count"}
	s.Fields["n"] = f
	if _, e := CompileSQLiteMetrics(context.Background(), q, s, o); e == nil {
		t.Fatal("transitive aggregate policy ignored")
	}
	f.AggregateOps = nil
	s.Fields["n"] = f
	o.ExpectedRevisions = map[string]string{"a": "1"}
	if _, e := CompileSQLiteMetrics(context.Background(), q, s, o); e == nil {
		t.Fatal("transitive revision ignored")
	}
	o.ExpectedRevisions = map[string]string{"a": "1", "b": "1"}
	c := defs["b"]
	c.Expression.Source = "[@computed/a]"
	defs["b"] = c
	if _, e := CompileSQLiteMetrics(context.Background(), q, s, o); e == nil {
		t.Fatal("cycle accepted")
	}
}
func TestSQLiteV2AggregateLifecycleAndBounds(t *testing.T) {
	for _, d := range SQLiteV2Aggregates() {
		if d.Arity != 1 {
			continue
		}
		a, b := d.New(), d.New()
		if e := a.Step([]driver.Value{float64(4)}); e != nil {
			t.Fatal(e)
		}
		for _, c := range []struct {
			fn   SQLiteAggregateFunction
			want any
		}{{a, float64(4)}, {b, nil}} {
			v, e := c.fn.Value()
			if e != nil {
				t.Fatal(e)
			}
			var payload struct {
				Value any
				Error string
			}
			if e = json.Unmarshal([]byte(v.(string)), &payload); e != nil || payload.Value != c.want || payload.Error != "" {
				t.Fatal(payload, e)
			}
		}
	}
	funcs := map[string]SQLiteScalarFunction{}
	for _, f := range SQLiteV2Functions() {
		funcs[f.Name] = f
	}
	for _, c := range []struct {
		value     driver.Value
		typ, code string
	}{{"3", "number", "storage_type"}, {int64(2), "bool", "storage_type"}, {strings.Repeat("a", 100001), "text", "text_range"}, {float64(1e101), "number", "numeric_range"}, {nil, "number", ""}} {
		v, e := funcs["qt_v2_field_error"].Call([]driver.Value{c.value, c.typ})
		if e != nil || sqliteErrorCode(v) != c.code {
			t.Fatal(v, e)
		}
	}
}
func TestSQLiteV2EnvelopeAndParameterPartition(t *testing.T) {
	q := ServerQueryV2{Version: 2, Profile: ServerExpressionProfile, WireQuery: WireQuery{Select: []string{"id"}, Limit: 1}}
	o := PlanOptions{SourceSQL: "SELECT * FROM fixture WHERE tenant=?1", SourceArgs: []any{"tenant"}}
	if _, e := CompileSQLiteRowsV2(context.Background(), q, sqliteV2TestSchema(), o); e == nil {
		t.Fatal("Postgres profile accepted")
	}
	q.Profile = SQLiteExpressionProfile
	q.Where = []WhereTerm{{Field: "text", Op: "contains", Value: "' ? literal"}}
	p, e := CompileSQLiteRowsV2(context.Background(), q, sqliteV2TestSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Args) != 4 || p.Args[0] != "tenant" || p.Args[1] != "' ? literal" || !strings.Contains(p.SQL, "qt_lower(?2)") {
		t.Fatal(p)
	}
	if got := sqliteNumberParameters(`x=? AND y='?''?' AND z="?"`, 2); got != `x=?3 AND y='?''?' AND z="?"` {
		t.Fatal(got)
	}
}
