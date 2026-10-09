package querytable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func metricSchema() Schema {
	return Schema{Name: "fixture", IDField: "id", Fields: map[string]FieldSpec{
		"id": {Name: "id", Kind: FieldNumber, Expr: "r.id", Sortable: true, ServerFilter: true, ExpressionNumeric: true},
		"x":  {Name: "x", Kind: FieldNumber, Expr: "r.x", Sortable: true, ServerFilter: true, ExpressionNumeric: true},
		"y":  {Name: "y", Kind: FieldNumber, Expr: "r.y", Sortable: true, ServerFilter: true, ExpressionNumeric: true},
		"g":  {Name: "g", Kind: FieldText, Expr: "r.g", Sortable: true, ServerFilter: true},
	}}
}
func metricOptions() PlanOptions {
	return PlanOptions{SourceSQL: "SELECT * FROM (VALUES (1,1,1,'a'),(2,9,3,'a'),(3,20,2,'b')) AS fixture(id,x,y,g)", Identity: "test/fixture/schema1/snapshot1"}
}
func metricPlan(t *testing.T, spec AggSpec, o PlanOptions) SQLPlan {
	t.Helper()
	if spec.ID == "" {
		spec.ID = "m"
	}
	p, e := CompileMetrics(context.Background(), MetricQuery{Version: 2, Limit: 1, Metrics: []AggSpec{spec}}, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	return p.Metrics[0].SQLPlan
}
func TestExpressionSecurityAndBudgets(t *testing.T) {
	s := metricSchema()
	ctx := context.Background()
	for _, source := range []string{"[missing]", "SUM([x])", "REGEX_TEST([g],\"x\")", "1; DROP TABLE t", "[x]::text", "PG_SLEEP(1)", strings.Repeat("(", 51) + "1" + strings.Repeat(")", 51), strings.Repeat("1+", 600) + "1", "1e999", "1e-301", "IF(TRUE,1,\"text\")", "[g]+1"} {
		t.Run(source[:min(len(source), 30)], func(t *testing.T) {
			if _, e := CompileRowExpression(ctx, source, s, metricOptions()); e == nil {
				t.Fatal("accepted unsafe/unsupported source")
			}
		})
	}
	p, e := CompileRowExpression(ctx, `IF([g] = "'; DROP TABLE victims; --", [x] / NULLIF([y],0), NULL)`, s, metricOptions())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(p.SQL, "DROP TABLE") {
		t.Fatal("literal leaked into SQL")
	}
	if !strings.Contains(p.SQL, "$1::text") {
		t.Fatal("missing parameter")
	}
	s.Fields["x"] = FieldSpec{Kind: FieldNumber, Expr: "r.x"}
	if _, e = CompileRowExpression(ctx, "[x]+1", s, metricOptions()); e == nil {
		t.Fatal("unbounded numeric binding admitted")
	}
}

func TestFormulaParserDistinguishesLiteralAndFieldTokenKinds(t *testing.T) {
	for _, source := range []string{`SUM([x]) "+" 2`, `COUNT(")"`, `SUM([x]) [AND] 2`, `SUM([x]) "OR" 2`} {
		if _, err := parseExpression(source); err == nil {
			t.Fatalf("accepted token-kind ambiguity: %s", source)
		}
	}
	s := metricSchema()
	f := s.Fields["g"]
	f.Name = ","
	s.Fields[","] = f
	for _, source := range []string{`MIN(COALESCE(")", [g]))`, `MIN(COALESCE([,], "+"))`, `COUNT("AND")`, `COUNT(")")`} {
		if _, err := CompileMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "m", Expression: source}}}, s, metricOptions()); err != nil {
			t.Fatalf("rejected valid literal/field: %s: %v", source, err)
		}
	}
}

func TestPostgresV2UnicodeFiltersAgreeWithHostCollation(t *testing.T) {
	s := metricSchema()
	o := metricOptions()
	o.SourceSQL = "SELECT * FROM (VALUES (1,1,1,'Éclair'),(2,1,1,'other')) AS fixture(id,x,y,g)"
	for _, filter := range []WhereTerm{{Field: "g", Op: "contains", Value: "Éclair"}, {Field: "g", Op: "starts_with", Value: "É"}, {Field: "g", Op: "ends_with", Value: "CLAIR"}} {
		legacy, _, err := Compile(WireQuery{Where: []WhereTerm{filter}}, s, 1)
		if err != nil {
			t.Fatal(err)
		}
		baseline := executePlan(t, SQLPlan{SQL: "SELECT COUNT(*) AS total FROM (" + o.SourceSQL + ") r WHERE " + legacy.WhereSQL, Args: legacy.Args})
		plan, err := CompileComputedRows(context.Background(), WireQuery{Select: []string{"id"}, Where: []WhereTerm{filter}, Limit: 10}, s, o)
		if err != nil {
			t.Fatal(err)
		}
		rows := executePlan(t, plan)
		if float64(len(rows)) != baseline[0]["total"] || len(rows) != 1 || rows[0]["column0"] != float64(1) {
			t.Fatalf("v1/v2 text filter disagreement for %#v: %#v / %#v", filter, baseline, rows)
		}
	}
}
func TestMetricValidationAndRestrictions(t *testing.T) {
	s := metricSchema()
	f := s.Fields["x"]
	f.AggregateOps = []string{"avg"}
	s.Fields["x"] = f
	for _, spec := range []AggSpec{{ID: "m", Expression: "SUM([x])"}, {ID: "m", Expression: "SUM(AVG([y]))"}, {ID: "m", Expression: "[y]+1"}, {ID: "m", Expression: "SUM([y])", Sort: []MetricSort{{Key: "value;DROP", Dir: "asc"}}}, {ID: "m", Expression: "SUM([y])", Scope: "pageMaybe"}, {ID: "m", Distribution: &MetricDistribution{Kind: "histogram", Input: "SUM([y])"}}} {
		if _, e := CompileMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{spec}}, s, metricOptions()); e == nil {
			t.Fatalf("accepted %#v", spec)
		}
	}
	if _, e := CompileAggregation(AggSpec{ID: "v1", Op: "sum", Field: "x"}, s); e == nil {
		t.Fatal("v1 bypassed aggregate restriction")
	}
	if _, e := CompileAggregation(AggSpec{ID: "v1", Op: "sum", Field: "y", Expression: "SUM([y])/2"}, s); e == nil {
		t.Fatal("v1 silently downgraded expression")
	}
	p := metricPlan(t, AggSpec{Expression: "SUM([x])/SUM([y])", ExpressionY: "AVG([x]/[y])", GroupBy: []string{"g"}, Sort: []MetricSort{{Key: "value", Dir: "desc"}}, GroupLimit: 1}, metricOptions())
	if !strings.Contains(p.SQL, "y_error") || !strings.Contains(p.SQL, "value DESC") {
		t.Fatal("paired/final sorting missing")
	}
	if !p.RequiresSnapshot {
		t.Fatal("snapshot requirement absent")
	}
}
func TestComputedResolution(t *testing.T) {
	defs := map[string]ComputedColumn{"a": {ID: "a", Label: "a", Revision: "1", Expression: ComputedExpression{"qt-expr", 1, "[@computed/b] / 2"}}, "b": {ID: "b", Label: "b", Revision: "2", Expression: ComputedExpression{"qt-expr", 1, "[x]+[y]"}}}
	o := metricOptions()
	calls := map[string]int{}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) { calls[id]++; return defs[id], nil })
	o.ExpectedRevisions = map[string]string{"a": "1", "b": "2"}
	p, e := CompileComputedRows(context.Background(), WireQuery{Select: []string{"id"}, OrderBy: []OrderBy{{Field: "@computed/a", Dir: "desc"}}, Limit: 1}, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.ResolvedRevisions) != 2 || calls["a"] != 1 || calls["b"] != 1 {
		t.Fatal("transitive revisions not resolved once")
	}
	if strings.Index(p.SQL, "ORDER BY") > strings.Index(p.SQL, " LIMIT ") {
		t.Fatal("pagination before global sort")
	}
	delete(o.ExpectedRevisions, "b")
	_, e = CompileComputedRows(context.Background(), WireQuery{Select: []string{"@computed/a"}}, metricSchema(), o)
	var d *PlanDiagnostic
	if !errors.As(e, &d) || d.Code != "definition_changed" {
		t.Fatalf("expected definition_changed: %v", e)
	}
	o.ExpectedRevisions["b"] = "2"
	c := defs["b"]
	c.Expression.Source = "[@computed/a]"
	defs["b"] = c
	if _, e = CompileRowExpression(context.Background(), "[@computed/a]", metricSchema(), o); e == nil {
		t.Fatal("cycle admitted")
	}
}

// PostgreSQL fixtures require only the optional psql CLI, never a Go driver or
// runtime dependency. Set QT_TEST_POSTGRES to a disposable database connstring.
func executePlan(t *testing.T, p SQLPlan) []map[string]any {
	t.Helper()
	dsn := os.Getenv("QT_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set QT_TEST_POSTGRES for PostgreSQL semantic fixtures")
	}
	literals := []string{}
	for _, a := range p.Args {
		switch v := a.(type) {
		case string:
			literals = append(literals, "'"+strings.ReplaceAll(v, "'", "''")+"'")
		case bool:
			literals = append(literals, strconv.FormatBool(v))
		case float64:
			literals = append(literals, strconv.FormatFloat(v, 'g', -1, 64))
		case int:
			literals = append(literals, strconv.Itoa(v))
		default:
			t.Fatalf("unsupported fixture arg %T", a)
		}
	}
	sql := "PREPARE qt_fixture AS SELECT row_to_json(t) FROM (" + p.SQL + ") t; EXECUTE qt_fixture"
	if len(literals) > 0 {
		sql += "(" + strings.Join(literals, ",") + ")"
	}
	sql += ";"
	cmd := exec.Command("psql", dsn, "-X", "-qAt", "-v", "ON_ERROR_STOP=1")
	cmd.Stdin = strings.NewReader(sql)
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("postgres: %v\n%s\n%s", e, output, sql)
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var r map[string]any
		if e = json.Unmarshal([]byte(line), &r); e != nil {
			t.Fatalf("decode %s: %v", line, e)
		}
		rows = append(rows, r)
	}
	return rows
}
func TestPostgresRowSemantics(t *testing.T) {
	cases := []struct {
		source string
		value  any
		err    any
	}{{"NULL+NULL", nil, nil}, {"1e100/1e-300", nil, "numeric_range"}, {"1/2", 0.5, nil}, {"1/0", nil, "divide_by_zero"}, {"1/NULLIF(0,0)", nil, nil}, {"IF(TRUE,7,1/0)", float64(7), nil}, {"IF(FALSE,1/0,9)", float64(9), nil}, {"COALESCE(NULL,3,1/0)", float64(3), nil}, {"COALESCE(1/0,3)", nil, "divide_by_zero"}, {"FALSE AND (1/0=0)", false, nil}, {"TRUE OR (1/0=0)", true, nil}, {"NOT NULL", nil, nil}, {"IS_NULL(NULL)", true, nil}, {"ABS(-2)", float64(2), nil}, {"IF(TRUE,COALESCE(NULL,NULL),1)", nil, nil}, {"1e100*10", nil, "numeric_range"}, {"1e-300/10", nil, "numeric_range"}}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			p, e := CompileRowExpression(context.Background(), c.source, metricSchema(), metricOptions())
			if e != nil {
				t.Fatal(e)
			}
			r := executePlan(t, p)[0]
			if r["value"] != c.value || r["error"] != c.err {
				t.Fatalf("got %#v want %v/%v", r, c.value, c.err)
			}
		})
	}
}
func TestPostgresComposedPairedScopes(t *testing.T) {
	spec := AggSpec{Expression: "SUM([x])/SUM([y])", ExpressionY: "AVG([x]/[y])", GroupBy: []string{"g"}, Sort: []MetricSort{{Key: "group0", Dir: "asc"}}}
	r := executePlan(t, metricPlan(t, spec, metricOptions()))
	if r[0]["value"] != 2.5 || r[0]["y"] != float64(2) {
		t.Fatalf("ratio vs mean: %#v", r)
	}
	spec.Scope = "shownRows"
	r = executePlan(t, metricPlan(t, spec, metricOptions()))
	if len(r) != 1 || r[0]["value"] != float64(1) {
		t.Fatal(r)
	}
	spec.Scope = "allMatching"
	spec.Sort = []MetricSort{{Key: "value", Dir: "desc"}}
	spec.GroupLimit = 1
	r = executePlan(t, metricPlan(t, spec, metricOptions()))
	if r[0]["group0"] != "b" || r[0]["group_count"] != float64(2) {
		t.Fatal(r)
	}
}
func TestPostgresDistribution(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT * FROM (VALUES (1,0,1,'a'),(2,10,1,'a'),(3,20,1,'a'),(4,30,1,'a'),(5,NULL,1,'b')) AS fixture(id,x,y,g)"
	spec := AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: "tukey"}, Sort: []MetricSort{{Key: "group0", Dir: "asc"}}}
	r := executePlan(t, metricPlan(t, spec, o))
	d := r[0]["distribution"].(map[string]any)
	if summary, ok := d["summary"]; ok {
		d = summary.(map[string]any)
	}
	if d["q1"] != 7.5 || d["median"] != float64(15) || d["q3"] != 22.5 || d["low"] != float64(0) || d["high"] != float64(30) {
		t.Fatal(r)
	}
	spec.Distribution = &MetricDistribution{Kind: "histogram", Input: "[x]", Bins: 3}
	r = executePlan(t, metricPlan(t, spec, o))
	d = r[0]["distribution"].(map[string]any)
	if fmt.Sprint(d["edges"]) != "[0 10 20 30]" || fmt.Sprint(d["counts"]) != "[1 1 2]" {
		t.Fatal(r)
	}
	d = r[1]["distribution"].(map[string]any)
	if fmt.Sprint(d["counts"]) != "[0 0 0]" {
		t.Fatal(r)
	}
}

func TestPostgresEmptyAndErrors(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT 1 AS id, NULL::double precision AS x,1 AS y,NULL::text AS g WHERE FALSE"
	for _, expression := range []string{"SUM([x])", "AVG([x])", "MIN([x])", "COUNT()", "IF(TRUE,MIN(NULL),SUM([x]))"} {
		r := executePlan(t, metricPlan(t, AggSpec{Expression: expression}, o))
		if len(r) != 1 || r[0]["count"] != float64(0) || r[0]["error"] != nil {
			t.Fatal(r)
		}
		want := any(nil)
		if expression == "COUNT()" {
			want = float64(0)
		}
		if r[0]["value"] != want {
			t.Fatal(r)
		}
	}
	for _, kind := range []string{"box", "histogram"} {
		r := executePlan(t, metricPlan(t, AggSpec{Distribution: &MetricDistribution{Kind: kind, Input: "[x]"}}, o))
		d := r[0]["distribution"].(map[string]any)
		if kind == "box" && d["summary"] != nil {
			t.Fatal(r)
		}
		if kind == "histogram" && (fmt.Sprint(d["edges"]) != "[]" || fmt.Sprint(d["counts"]) != "[]") {
			t.Fatal(r)
		}
	}
	o.SourceSQL = "SELECT * FROM (VALUES (1,1,0,'bad'),(2,3,1,'ok'),(3,NULL,1,'null')) fixture(id,x,y,g)"
	for _, kind := range []string{"box", "histogram"} {
		r := executePlan(t, metricPlan(t, AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: kind, Input: "[x]/[y]"}}, o))
		for _, row := range r {
			if kind == "histogram" || row["group0"] == "bad" {
				if row["error"] != "divide_by_zero" || row["distribution"] != nil {
					t.Fatal(r)
				}
			} else if row["error"] != nil {
				t.Fatal(r)
			}
		}
	}
	r := executePlan(t, metricPlan(t, AggSpec{Expression: "SUM([x]/[y])", ExpressionY: "COUNT()", GroupBy: []string{"g"}}, o))
	for _, row := range r {
		if row["group0"] == "bad" && (row["value"] != nil || row["error"] != "divide_by_zero" || row["y"] != float64(1) || row["y_error"] != nil) {
			t.Fatal(r)
		}
	}
	o.SourceSQL = "SELECT 1 AS id,9007199254740991::double precision AS x,1 AS y,'a' AS g UNION ALL SELECT 2,1,1,'a'"
	r = executePlan(t, metricPlan(t, AggSpec{Expression: "SUM([x])"}, o))
	if r[0]["value"] != nil || r[0]["error"] != "unsafe_integer" {
		t.Fatal(r)
	}
}
func TestPostgresHistogramGlobalEdgesAndConstants(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT * FROM (VALUES (1,0,1,'a'),(2,10,1,'a'),(3,100,1,'b')) fixture(id,x,y,g)"
	spec := AggSpec{GroupBy: []string{"g"}, GroupLimit: 1, Sort: []MetricSort{{Key: "group0", Dir: "asc"}}, Distribution: &MetricDistribution{Kind: "histogram", Input: "[x]", Bins: 2}}
	r := executePlan(t, metricPlan(t, spec, o))
	d := r[0]["distribution"].(map[string]any)
	if fmt.Sprint(d["edges"]) != "[0 50 100]" || fmt.Sprint(d["counts"]) != "[2 0]" {
		t.Fatal(r)
	}
	o.SourceSQL = "SELECT * FROM (VALUES (1,4,1,'a'),(2,4,1,'a')) fixture(id,x,y,g)"
	r = executePlan(t, metricPlan(t, spec, o))
	d = r[0]["distribution"].(map[string]any)
	if fmt.Sprint(d["edges"]) != "[4 4]" || fmt.Sprint(d["counts"]) != "[2]" {
		t.Fatal(r)
	}
	o.SourceSQL = "SELECT * FROM (VALUES (1,1::double precision,1,'a'),(2,1.0000000000000002::double precision,1,'a')) fixture(id,x,y,g)"
	r = executePlan(t, metricPlan(t, spec, o))
	if r[0]["error"] != "histogram_precision" || r[0]["distribution"] != nil {
		t.Fatal(r)
	}
}
func TestPostgresTukeyOutliers(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT i AS id, CASE WHEN i<=15 THEN -100-i WHEN i>85 THEN 100+i ELSE 0 END AS x,1 AS y,'a'::text AS g FROM generate_series(1,100) i"
	r := executePlan(t, metricPlan(t, AggSpec{Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: "tukey"}}, o))
	s := r[0]["distribution"].(map[string]any)["summary"].(map[string]any)
	xs := s["outliers"].([]any)
	if s["outlierCount"] != float64(30) || len(xs) != 20 || s["low"] != float64(0) || s["high"] != float64(0) || xs[0].(float64) >= 0 || xs[len(xs)-1].(float64) <= 0 {
		t.Fatal(s)
	}
}
func TestPostgresComputedGlobalSortAndFilters(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT i AS id,i AS x,1 AS y,'a'::text AS g FROM generate_series(1,201) i"
	o.ExpectedRevisions = map[string]string{"twice": "r1"}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
		return ComputedColumn{ID: id, Label: "Twice", Revision: "r1", Expression: ComputedExpression{"qt-expr", 1, "[x]*2"}}, nil
	})
	q := WireQuery{Select: []string{"id", "@computed/twice"}, OrderBy: []OrderBy{{Field: "@computed/twice", Dir: "desc"}}, Limit: 1}
	p, e := CompileComputedRows(context.Background(), q, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	r := executePlan(t, p)
	if r[0]["column0"] != float64(201) || r[0]["column1"] != float64(402) {
		t.Fatal(r)
	}
	q.Where = []WhereTerm{{Field: "x", Op: "<", Value: "101"}}
	p, e = CompileComputedRows(context.Background(), q, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	r = executePlan(t, p)
	if r[0]["column0"] != float64(100) {
		t.Fatal(r)
	}
	mq := MetricQuery{Version: 2, Where: q.Where, OrderBy: q.OrderBy, Limit: 2, Offset: 1, Metrics: []AggSpec{{ID: "m", Expression: "SUM([@computed/twice])", Scope: "shownRows"}}}
	batch, e := CompileMetrics(context.Background(), mq, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	r = executePlan(t, batch.Metrics[0].SQLPlan)
	if r[0]["value"] != float64(394) {
		t.Fatal(r)
	}
}

func TestPostgresV2HostTiebreakControlsRowAndMetricWindows(t *testing.T) {
	s := metricSchema()
	s.TiebreakSort = []OrderBy{{Field: "id", Dir: "desc"}}
	o := metricOptions()
	o.SourceSQL = "SELECT i AS id,1 AS x,1 AS y,'a'::text AS g FROM generate_series(1,3) i"
	order := []OrderBy{{Field: "x", Dir: "asc"}}
	p, err := CompileComputedRows(context.Background(), WireQuery{Select: []string{"id"}, OrderBy: order, Limit: 1}, s, o)
	if err != nil {
		t.Fatal(err)
	}
	row := executePlan(t, p)
	if len(row) != 1 || row[0]["column0"] != float64(3) {
		t.Fatalf("host ID DESC tie-break must select row 3: %#v", row)
	}
	batch, err := CompileMetrics(context.Background(), MetricQuery{Version: 2, OrderBy: order, Limit: 1, Metrics: []AggSpec{{ID: "m", Expression: "SUM([id])", Scope: "shownRows"}}}, s, o)
	if err != nil {
		t.Fatal(err)
	}
	metric := executePlan(t, batch.Metrics[0].SQLPlan)
	if len(metric) != 1 || metric[0]["value"] != float64(3) {
		t.Fatalf("shown-row metric must use the same host tie-break: %#v", metric)
	}
}
func TestSchemaAggregateMetadata(t *testing.T) {
	doc := []byte(`{"name":"s","idField":"id","fields":[{"name":"id","type":"number","aggregate":{"measure":false,"groupable":true},"bindings":{"postgres":{"expr":"r.id","expressionNumeric":true}}}]}`)
	s, e := LoadSchema(doc)
	if e != nil {
		t.Fatal(e)
	}
	f := s.Fields["id"]
	if f.AggregateOps == nil || len(f.AggregateOps) != 0 || f.Groupable == nil || !*f.Groupable || !f.ExpressionNumeric {
		t.Fatalf("metadata lost: %#v", f)
	}
	if _, e = CompileMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "m", Expression: "SUM([id])"}}}, s, PlanOptions{SourceSQL: "SELECT 1 AS id"}); e == nil {
		t.Fatal("measure:false bypassed")
	}
}

func TestExpressionUnicodeAndCancellation(t *testing.T) {
	for _, source := range []string{`"\u0000"`, `"\ud800"`, `"\udc00"`, `"\ud800\u0041"`, string([]byte{0xff})} {
		if _, e := CompileRowExpression(context.Background(), source, metricSchema(), metricOptions()); e == nil {
			t.Fatalf("invalid Unicode admitted: %q", source)
		}
	}
	if _, e := CompileRowExpression(context.Background(), `"\ud83d\ude00"`, metricSchema(), metricOptions()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := CompileRowExpression(ctx, "1+2", metricSchema(), metricOptions()); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func FuzzExpressionParser(f *testing.F) {
	for _, s := range []string{"[x]/NULLIF([y],0)", "IF(TRUE,1,0)", "[a]]b]", `"\ud800"`, "SUM([x])", "", "1e+", strings.Repeat("(", 100)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, source string) {
		_, _ = CompileRowExpression(context.Background(), source, metricSchema(), metricOptions())
	})
}

func TestV2EnvelopeAndExecutionUnion(t *testing.T) {
	var q ServerQueryV2
	if e := json.Unmarshal([]byte(`{"version":2,"profile":"qt-postgres-v1","select":["id"],"where":[],"limit":1,"expectedRevisions":{},"planToken":"opaque"}`), &q); e != nil {
		t.Fatal(e)
	}
	o := metricOptions()
	if _, e := CompileRowsV2(context.Background(), q, metricSchema(), o); e == nil {
		t.Fatal("token validation bypassed")
	}
	calls := 0
	o.ValidatePlanToken = func(_ context.Context, token string) error {
		calls++
		if token != "opaque" {
			return errors.New("bad token")
		}
		return nil
	}
	if _, e := CompileRowsV2(context.Background(), q, metricSchema(), o); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	q.Profile = "unknown"
	if _, e := CompileRowsV2(context.Background(), q, metricSchema(), o); e == nil {
		t.Fatal("unknown profile accepted")
	}
	o.ExpectedRevisions = map[string]string{"a": "1", "b": "1"}
	resolved := map[string]int{}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
		resolved[id]++
		return ComputedColumn{ID: id, Label: id, Revision: "1", Expression: ComputedExpression{"qt-expr", 1, "[x]"}}, nil
	})
	p, e := CompileExecution(context.Background(), WireQuery{Select: []string{"@computed/a"}, Limit: 1}, MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "m", Expression: "SUM([@computed/a])+SUM([@computed/b])"}}}, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.ResolvedRevisions) != 2 || resolved["a"] != 1 || resolved["b"] != 1 {
		t.Fatal(p.ResolvedRevisions, resolved)
	}
}
func TestStageDedupAndNestedRestrictions(t *testing.T) {
	p := metricPlan(t, AggSpec{Expression: "SUM([x]/[y])+SUM([x]/[y])"}, metricOptions())
	if strings.Count(p.SQL, " AS a0,") != 1 || strings.Contains(p.SQL, " AS a1,") {
		t.Fatal("aggregate dedup missing")
	}
	s := metricSchema()
	f := s.Fields["x"]
	f.AggregateOps = []string{}
	s.Fields["x"] = f
	o := metricOptions()
	o.ExpectedRevisions = map[string]string{"a": "1"}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
		return ComputedColumn{ID: id, Label: id, Revision: "1", Expression: ComputedExpression{"qt-expr", 1, "[x]+1"}}, nil
	})
	if _, e := CompileMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "m", Expression: "SUM([@computed/a])"}}}, s, o); e == nil {
		t.Fatal("computed dependency bypassed disabled measure")
	}
	for _, spec := range []AggSpec{{ID: "m", Expression: "1+2"}, {ID: "m", Expression: "COUNT()", ExpressionY: "1"}, {ID: "m", Expression: "COUNT()", Display: &MetricDisplayHint{Kind: "scatter"}}, {ID: "m", Expression: "COUNT()", Diagnostics: []string{"invalid"}}} {
		if _, e := CompileMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{spec}}, metricSchema(), metricOptions()); e == nil {
			t.Fatal("invalid metric accepted", spec)
		}
	}
}

func TestPostgresTypedKeysAndParameterOffsets(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT * FROM (VALUES (1,1,1,NULL::text),(2,2,1,''),(3,3,1,'é'),(4,4,1,'😀')) fixture(id,x,y,g) WHERE id>$1"
	o.SourceArgs = []any{0}
	p := metricPlan(t, AggSpec{Expression: "SUM([x])+0.5", GroupBy: []string{"g"}, Sort: []MetricSort{{Key: "group0", Dir: "asc", Nulls: "first"}}}, o)
	r := executePlan(t, p)
	if len(r) != 4 || r[0]["group0"] != nil || r[1]["group0"] != "" || r[0]["value"] != 1.5 {
		t.Fatal(r)
	}
	o.SourceSQL = "SELECT 1 AS id,1 AS x,1 AS y,repeat('x',100001) AS g"
	o.SourceArgs = nil
	rp, e := CompileRowExpression(context.Background(), "[g]", metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	r = executePlan(t, rp)
	if r[0]["error"] != "text_range" || r[0]["value"] != nil {
		t.Fatal(r)
	}
}

func TestPostgresComputedGroupingPolicy(t *testing.T) {
	o := metricOptions()
	o.ExpectedRevisions = map[string]string{"bucket": "1"}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
		return ComputedColumn{ID: id, Label: id, Revision: "1", Expression: ComputedExpression{"qt-expr", 1, `IF([x] < 10,"small","large")`}}, nil
	})
	q := MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "m", Expression: "COUNT()", GroupBy: []string{"@computed/bucket"}}}}
	if _, e := CompileMetrics(context.Background(), q, metricSchema(), o); e == nil {
		t.Fatal("computed grouping admitted without host policy")
	}
	o.ComputedGroupable = map[string]bool{"bucket": true}
	p, e := CompileMetrics(context.Background(), q, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	r := executePlan(t, p.Metrics[0].SQLPlan)
	if len(r) != 2 || r[0]["group0"] != "small" || r[0]["value"] != float64(2) {
		t.Fatal(r)
	}
}
