package querytable

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func combinedRequest() MetricQuery {
	specs := []AggSpec{
		{ID: "sum", Expression: "SUM([x])", GroupBy: []string{"g"}},
		{ID: "same", Expression: "SUM([x])", GroupBy: []string{"g"}},
		{ID: "plus", Expression: "SUM([x])+2", GroupBy: []string{"g"}},
		{ID: "top", Expression: "SUM([x])+5", GroupBy: []string{"g"}, Sort: []MetricSort{{Key: "value", Dir: "asc", Nulls: "first"}}, GroupLimit: 1},
		{ID: "avg", Expression: "AVG([x])", GroupBy: []string{"g"}},
		{ID: "min", Expression: "MIN([x])", GroupBy: []string{"g"}},
		{ID: "max", Expression: "MAX([x])", GroupBy: []string{"g"}},
		{ID: "distinct", Expression: "COUNT_DISTINCT([x])", GroupBy: []string{"g"}},
		{ID: "median", Expression: "MEDIAN([x])", GroupBy: []string{"g"}},
		{ID: "paired", Expression: "SUM([x])/SUM([y])", ExpressionY: "AVG([x])", GroupBy: []string{"g"}},
		{ID: "shown", Expression: "SUM([x])", Scope: "shownRows", GroupBy: []string{"g"}},
		{ID: "keys", Expression: "COUNT()", GroupBy: []string{"id"}},
		{ID: "box", Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: "tukey"}, GroupBy: []string{"g"}},
		{ID: "histogram", Distribution: &MetricDistribution{Kind: "histogram", Input: "[x]", Bins: 3}, GroupBy: []string{"g"}, GroupLimit: 1},
		{ID: "lazy", Expression: "IF(TRUE,COUNT(),SUM([x])/0)"},
	}
	// id grouping needs an explicit numeric opt-in in the fixture schema.
	return MetricQuery{Version: 2, Profile: ServerExpressionProfile, Limit: 1, Offset: 1, OrderBy: []OrderBy{{Field: "id", Dir: "asc"}}, Where: []WhereTerm{{Field: "x", Op: ">", Value: "0"}}, Metrics: specs}
}
func combinedSchema() Schema {
	s := metricSchema()
	f := s.Fields["id"]
	yes := true
	f.Groupable = &yes
	s.Fields["id"] = f
	return s
}

func TestCombinedMetricPlanMetadata(t *testing.T) {
	q := combinedRequest()
	p, err := CompileCombinedMetrics(context.Background(), q, combinedSchema(), metricOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Metrics) != len(q.Metrics) || !p.RequiresSnapshot || p.Profile != ServerExpressionProfile || p.Fingerprint == "" {
		t.Fatal(p)
	}
	fusions := 0
	for _, stage := range p.Stages {
		if strings.HasPrefix(stage.Name, "qtf") {
			fusions++
		}
	}
	if fusions == 0 {
		t.Fatal("compatible scalar reductions not fused")
	}
	if len(p.Stages) >= 1000 {
		t.Fatal("unbounded stage growth")
	}
	if strings.Contains(p.SQL, " LIMIT 1 ") || strings.Contains(p.SQL, " SUM([") {
		t.Fatal("literal/request expression leaked into SQL")
	}
	same, err := CompileCombinedMetrics(context.Background(), q, combinedSchema(), metricOptions())
	if err != nil || p.Fingerprint != same.Fingerprint || p.SQL != same.SQL {
		t.Fatal("nondeterministic combined plan", err)
	}
}

func TestPostgresCombinedMetricsAgreeWithIndependentPlans(t *testing.T) {
	for _, source := range []string{
		metricOptions().SourceSQL,
		"SELECT * FROM (VALUES (1,NULL::double precision,0,'a'),(2,9,3,'a'),(3,20,2,'b')) AS fixture(id,x,y,g)",
		"SELECT * FROM (VALUES (1,9007199254740992::double precision,0,'a'),(2,-9007199254740992::double precision,3,'a'),(3,20,2,'b')) AS fixture(id,x,y,g)",
		"SELECT * FROM (VALUES (1,1::double precision,0,'a')) AS fixture(id,x,y,g) WHERE FALSE",
	} {
		t.Run(source, func(t *testing.T) {
			q := combinedRequest()
			q.Where = nil
			o := metricOptions()
			o.SourceSQL = source
			p, err := CompileCombinedMetrics(context.Background(), q, combinedSchema(), o)
			if err != nil {
				t.Fatal(err)
			}
			got := executePlan(t, p.SQLPlan)
			if len(got) != len(q.Metrics) {
				t.Fatal(got)
			}
			for i, metric := range p.Metrics {
				want := executePlan(t, metric.SQLPlan)
				bucketRows := got[i]["buckets"].([]any)
				actual := []map[string]any{}
				for _, row := range bucketRows {
					actual = append(actual, row.(map[string]any))
				}
				if got[i]["metric_index"] != float64(i) || !reflect.DeepEqual(actual, want) {
					t.Fatalf("metric %s: got %+v want %+v", metric.ID, actual, want)
				}
				count := float64(0)
				if len(want) > 0 {
					count = want[0]["group_count"].(float64)
				}
				if got[i]["group_count"] != count {
					t.Fatal("group budget metadata lost", metric.ID, got[i])
				}
			}
		})
	}
}

func TestPostgresCombinedMetricsSharePopulation(t *testing.T) {
	q := MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "sum", Expression: "SUM([x])"}, {ID: "avg", Expression: "AVG([x])"}, {ID: "min", Expression: "MIN([x])"}, {ID: "max", Expression: "MAX([x])"}, {ID: "median", Expression: "MEDIAN([x])"}}}
	o := metricOptions()
	o.SourceSQL = "SELECT i AS id,i AS x,1 AS y,'a'::text AS g FROM generate_series(1,1000) AS population(i)"
	p, err := CompileCombinedMetrics(context.Background(), q, metricSchema(), o)
	if err != nil {
		t.Fatal(err)
	}
	plan := explainPostgresPlan(t, p.SQLPlan)
	visits := float64(0)
	scans := 0
	visitPostgresPlan(plan, func(node map[string]any) {
		if node["Node Type"] == "Function Scan" && node["Function Name"] == "generate_series" {
			scans++
			visits += node["Actual Rows"].(float64) * node["Actual Loops"].(float64)
		}
	})
	if scans != 1 || visits != 1000 {
		t.Fatalf("scalar batch source scans=%d visits=%g", scans, visits)
	}
}

func TestCombinedMetricsBindingsAndRevisionUnion(t *testing.T) {
	q := MetricQuery{Version: 2, Where: []WhereTerm{{Field: "g", Op: "=", Value: "a"}}, Metrics: []AggSpec{{ID: "one", Expression: "SUM([@computed/one])+2"}, {ID: "two", Expression: "AVG([@computed/two])+3"}}}
	q.ExpectedRevisions = map[string]string{"one": "1", "two": "1", "base": "1"}
	o := metricOptions()
	o.SourceSQL = "SELECT * FROM (VALUES (1,1,1,'a'),(2,9,3,'a'),(3,20,2,'b')) AS fixture(id,x,y,g) WHERE id>$1"
	o.SourceArgs = []any{0}
	calls := map[string]int{}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
		calls[id]++
		source := "[@computed/base]+1"
		if id == "base" {
			source = "[x]"
		}
		return ComputedColumn{ID: id, Label: id, Revision: "1", Expression: ComputedExpression{"qt-expr", 1, source}}, nil
	})
	p, err := CompileCombinedMetrics(context.Background(), q, metricSchema(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.ResolvedRevisions, q.ExpectedRevisions) || calls["base"] != 1 || p.Args[0] != 0 {
		t.Fatal("revision/parameter union incorrect", p.ResolvedRevisions, calls, p.Args)
	}
	// Different final literal bindings must remain distinct after parameter interning.
	got := executePlan(t, p.SQLPlan)
	if got[0]["buckets"].([]any)[0].(map[string]any)["value"] != float64(14) || got[1]["buckets"].([]any)[0].(map[string]any)["value"] != float64(9) {
		t.Fatal(got)
	}
}

func TestCombinedMetricsValidation(t *testing.T) {
	ctx := context.Background()
	q := MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "ok", Expression: "SUM([x])"}, {ID: "bad", Expression: "SUM([missing])"}}}
	if _, err := CompileCombinedMetrics(ctx, q, metricSchema(), metricOptions()); err == nil {
		t.Fatal("invalid batch accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := CompileCombinedMetrics(cancelled, MetricQuery{Version: 2, Metrics: q.Metrics[:1]}, metricSchema(), metricOptions()); err == nil {
		t.Fatal("cancellation ignored")
	}
	for _, flag := range []string{"token", "snapshot", "profile"} {
		q.Metrics = q.Metrics[:1]
		switch flag {
		case "token":
			q.PlanToken = "unvalidated"
		case "snapshot":
			q.PlanToken = ""
			q.Snapshot = "unvalidated"
		case "profile":
			q.Snapshot = ""
			q.Profile = SQLiteExpressionProfile
		}
		if _, err := CompileCombinedMetrics(ctx, q, metricSchema(), metricOptions()); err == nil {
			t.Fatal("invalid envelope accepted", flag)
		}
	}
}

func TestPostgresSQLTokenRewriting(t *testing.T) {
	input := `SELECT $1, qt1.x, '$2 qt1', "$2 qt1", $$ $2 qt1 $$, $tag$ $2 qt1 $tag$, E'it\'s $2 qt1' FROM qt1 /* $2 qt1 /* nested */ */ -- $2 qt1
WHERE qt1.x=$2`
	got, err := rewritePostgresSQL(input, []int{3, 4}, map[string]string{"qt1": "qtb1"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(input, "SELECT $1, qt1.x", "SELECT $3, qtb1.x", 1)
	want = strings.Replace(want, " FROM qt1 /*", " FROM qtb1 /*", 1)
	want = strings.Replace(want, "WHERE qt1.x=$2", "WHERE qtb1.x=$4", 1)
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	for _, sql := range []string{"SELECT $0", "SELECT $99999999999999999999999999", "SELECT $3"} {
		if _, err := rewritePostgresSQL(sql, []int{1, 2}, nil); err == nil {
			t.Fatal("invalid bind accepted", sql)
		}
	}
}

func TestCombinedMetricTwentyCardsShareStages(t *testing.T) {
	q := MetricQuery{Version: 2}
	for i := 0; i < 20; i++ {
		q.Metrics = append(q.Metrics, AggSpec{ID: fmt.Sprint(i), Expression: fmt.Sprintf("SUM([x])+%d", i)})
	}
	p, err := CompileCombinedMetrics(context.Background(), q, metricSchema(), metricOptions())
	if err != nil {
		t.Fatal(err)
	}
	independent := 0
	for _, metric := range p.Metrics {
		independent += len(metric.Stages)
	}
	if len(p.Stages) >= independent/2 {
		t.Fatalf("20 cards retained %d of %d independent stages", len(p.Stages), independent)
	}
}

func TestCombinedMetricFusionExpressionLimit(t *testing.T) {
	s := metricSchema()
	source := []string{"1 AS id"}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("x%d", i)
		s.Fields[name] = FieldSpec{Kind: FieldNumber, Expr: "r." + name, ExpressionNumeric: true}
		source = append(source, "1 AS "+name)
	}
	o := metricOptions()
	o.SourceSQL = "SELECT " + strings.Join(source, ",")
	q := MetricQuery{Version: 2}
	for _, op := range []string{"SUM", "AVG", "MIN", "MAX", "COUNT_DISTINCT", "MEDIAN"} {
		leaves := []string{}
		for i := 0; i < 12; i++ {
			leaves = append(leaves, fmt.Sprintf("%s([x%d])", op, i))
		}
		q.Metrics = append(q.Metrics, AggSpec{ID: op, Expression: strings.Join(leaves, "+")})
	}
	p, err := CompileCombinedMetrics(context.Background(), q, s, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range p.Stages {
		if strings.HasPrefix(stage.Name, "qtf") {
			t.Fatal("fused projection exceeded 128-expression budget")
		}
	}
}

func TestCombinedMetricSourceNames(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT * FROM qtb0"
	p, err := CompileCombinedMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "sum", Expression: "SUM([x])"}}}, metricSchema(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range p.Stages {
		if stage.Name == "qtb0" {
			t.Fatal("authorized source table shadowed")
		}
	}
}
