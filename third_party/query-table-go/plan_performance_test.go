package querytable

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSelectOnlyComputationRunsAfterWindow(t *testing.T) {
	o := metricOptions()
	o.Resolver = DefinitionResolverFunc(func(context.Context, string) (ComputedColumn, error) {
		return ComputedColumn{ID: "ratio", Label: "Ratio", Revision: "1", Expression: ComputedExpression{"qt-expr", 1, "[x]/NULLIF([y],0)"}}, nil
	})
	o.ExpectedRevisions = map[string]string{"ratio": "1"}
	q := WireQuery{Select: []string{"id", "@computed/ratio"}, OrderBy: []OrderBy{{Field: "id", Dir: "asc"}}, Limit: 1}
	p, e := CompileComputedRows(context.Background(), q, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	window := -1
	division := -1
	for i, s := range p.Stages {
		if strings.Contains(s.SQL, " LIMIT ") {
			window = i
		}
		if strings.Contains(s.SQL, "divide_by_zero") {
			division = i
		}
	}
	if window < 0 || division <= window {
		t.Fatal("select-only division evaluated before pagination")
	}
	q.OrderBy = []OrderBy{{Field: "@computed/ratio", Dir: "desc"}}
	p, e = CompileComputedRows(context.Background(), q, metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	window, division = -1, -1
	for i, s := range p.Stages {
		if strings.Contains(s.SQL, " LIMIT ") {
			window = i
		}
		if strings.Contains(s.SQL, "divide_by_zero") {
			division = i
		}
	}
	if division < 0 || division >= window {
		t.Fatal("computed sort evaluated after pagination")
	}
}
func TestRowStagesDoNotCarryUnusedBindings(t *testing.T) {
	p, e := CompileRowExpression(context.Background(), "[x]/NULLIF([y],0)", metricSchema(), metricOptions())
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range p.Stages[1:] {
		if strings.HasPrefix(s.SQL, "SELECT *,") {
			t.Fatal("row stage still copies all preceding columns")
		}
	}
	if !strings.Contains(p.SQL, "numeric_range") || !strings.Contains(p.SQL, "divide_by_zero") {
		t.Fatal("guards lost")
	}
}
func TestHistogramCountsSamplesOnce(t *testing.T) {
	p := metricPlan(t, AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: "histogram", Input: "[x]", Bins: 30}}, metricOptions())
	if strings.Count(p.SQL, "width_bucket(") != 1 {
		t.Fatal("missing single bin assignment")
	}
	if strings.Contains(p.SQL, "r.sample>=x.edges[i]") {
		t.Fatal("histogram still rescans population for each bin")
	}
}

func TestFilteredWindowsPruneUnusedBindings(t *testing.T) {
	s := metricSchema()
	s.Fields["unused"] = FieldSpec{Name: "unused", Kind: FieldText, Expr: "repeat('wide', 10000)"}
	for _, shown := range []bool{false, true} {
		scope := "allMatching"
		if shown {
			scope = "shownRows"
		}
		p, err := CompileMetrics(context.Background(), MetricQuery{Version: 2, Limit: 2, Where: []WhereTerm{{Field: "g", Op: "=", Value: "a"}}, Metrics: []AggSpec{{ID: "m", Expression: "SUM([x])", Scope: scope}}}, s, metricOptions())
		if err != nil {
			t.Fatal(err)
		}
		unused := "f2" // alphabetic bindings: g, id, unused, x, y
		for _, stage := range p.Metrics[0].Stages[1:] {
			if strings.Contains(stage.SQL, unused) || strings.HasPrefix(stage.SQL, "SELECT * FROM") {
				t.Fatalf("dead filtered/window binding retained: %s", stage.SQL)
			}
		}
		r := executePlan(t, p.Metrics[0].SQLPlan)
		if len(r) != 1 || r[0]["value"] != float64(10) {
			t.Fatal(r)
		}
	}
}
func TestTukeyVisitsOnlyEachGroupsSamples(t *testing.T) {
	p := metricPlan(t, AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: "tukey"}}, metricOptions())
	if strings.Contains(p.SQL, "IS NOT DISTINCT FROM s.") || strings.Count(p.SQL, "unnest(s.sorted_samples)") != 1 {
		t.Fatal("Tukey rescans entire population")
	}
}

func TestPostgresGroupedBoxPopulation(t *testing.T) {
	// Reproduces the reviewed 200k-row/500-group shape without creating tables.
	// Timing is reported, not asserted: hardware/CI load vary. The plan-shape
	// regression above rules out per-group scans of the global population.
	o := metricOptions()
	o.SourceSQL = "SELECT i AS id,CASE WHEN i%100=0 THEN 1000 ELSE i%17 END AS x,1 AS y,(i%500)::text AS g FROM generate_series(1,200000) i"
	for _, whiskers := range []string{"minmax", "tukey"} {
		start := time.Now()
		r := executePlan(t, metricPlan(t, AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: whiskers}}, o))
		if len(r) != 500 {
			t.Fatal(len(r))
		}
		for _, row := range r {
			if row["error"] != nil || row["count"] != float64(400) {
				t.Fatal(row)
			}
		}
		t.Logf("%s: 200k rows, 500 groups in %s", whiskers, time.Since(start))
	}
}
