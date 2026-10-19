package querytable

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMetricPopulationValidation(t *testing.T) {
	where := []WhereTerm{}
	for i := 0; i < MaxWhereClauses+1; i++ {
		where = append(where, WhereTerm{Field: "g", Op: "=", Value: "a"})
	}
	for name, spec := range map[string]AggSpec{
		"negative minCount": {ID: "m", Op: "count", GroupBy: []string{"g"}, MinCount: -1},
		"huge minCount":     {ID: "m", Op: "count", GroupBy: []string{"g"}, MinCount: MaxMetricMinCount + 1},
		"filter budget":     {ID: "m", Op: "count", GroupBy: []string{"g"}, Where: where},
		"empty OR group":    {ID: "m", Op: "count", GroupBy: []string{"g"}, Where: []WhereTerm{{Any: []WhereClause{}}}},
		"long field":        {ID: "m", Op: "count", Where: []WhereTerm{{Field: strings.Repeat("f", 257), Op: "=", Value: "a"}}},
		"long value":        {ID: "m", Op: "count", Where: []WhereTerm{{Field: "g", Op: "=", Value: strings.Repeat("v", maxFilterValueBytes+1)}}},
	} {
		if err := (WireQuery{Aggregations: []AggSpec{spec}}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (WireQuery{Aggregations: []AggSpec{{ID: "m", Op: "count", GroupBy: []string{"g"}, MinCount: MaxMetricMinCount, Where: where[:MaxWhereClauses]}}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMetricPopulationRefusals(t *testing.T) {
	compile := func(spec AggSpec) error {
		spec.ID = "m"
		_, err := CompileMetrics(context.Background(), MetricQuery{Version: 2, Limit: 1, Metrics: []AggSpec{spec}}, metricSchema(), metricOptions())
		return err
	}
	code := func(err error) string {
		var d *PlanDiagnostic
		if errors.As(err, &d) {
			return d.Code
		}
		return ""
	}
	if err := compile(AggSpec{Op: "count", MinCount: 2}); code(err) != "min_count_grouping" {
		t.Fatalf("ungrouped minCount: %v", err)
	}
	if err := compile(AggSpec{Op: "count", GroupBy: []string{"g"}, Where: []WhereTerm{{Field: "g", Op: "matches_regex", Value: "a"}}}); code(err) != "unsupported_regex" {
		t.Fatalf("regex metric filter: %v", err)
	}
	if err := compile(AggSpec{Op: "count", GroupBy: []string{"g"}, Where: []WhereTerm{{Field: "missing", Op: "=", Value: "a"}}}); err == nil {
		t.Fatal("unknown metric filter field accepted")
	}
	s := metricSchema()
	field := s.Fields["g"]
	field.ServerFilter = false
	s.Fields["g"] = field
	if _, err := CompileMetrics(context.Background(), MetricQuery{Version: 2, Limit: 1, Metrics: []AggSpec{{ID: "m", Op: "count", Where: []WhereTerm{{Field: "g", Op: "=", Value: "a"}}}}}, s, metricOptions()); err == nil {
		t.Fatal("non-pushdown metric filter accepted")
	}
	// The legacy single-aggregate compiler never drops population controls.
	for _, spec := range []AggSpec{{ID: "m", Op: "count", GroupBy: []string{"g"}, MinCount: 2}, {ID: "m", Op: "count", Where: []WhereTerm{{Field: "g", Op: "=", Value: "a"}}}} {
		if _, err := CompileAggregation(spec, s); err == nil || !strings.Contains(err.Error(), "CompileMetrics") {
			t.Fatalf("v1 accepted %#v: %v", spec, err)
		}
	}
}

func TestMetricPopulationPlans(t *testing.T) {
	plain := metricPlan(t, AggSpec{Op: "count", GroupBy: []string{"g"}}, metricOptions())
	filtered := metricPlan(t, AggSpec{Op: "count", GroupBy: []string{"g"}, Where: []WhereTerm{{Field: "g", Op: "=", Value: "a"}}}, metricOptions())
	if plain.Fingerprint == filtered.Fingerprint || len(filtered.Stages) <= len(plain.Stages) {
		t.Fatal("a metric filter must add a predicate stage and change the fingerprint")
	}
	hidden := metricPlan(t, AggSpec{Op: "count", GroupBy: []string{"g"}, MinCount: 2, GroupLimit: 1}, metricOptions())
	if !strings.Contains(hidden.SQL, "hidden_groups") || !strings.Contains(hidden.SQL, "LEFT JOIN") || plain.Fingerprint == hidden.Fingerprint {
		t.Fatalf("minCount plan: %s", hidden.SQL)
	}
	if strings.Contains(plain.SQL, "hidden_groups") {
		t.Fatal("plans without minCount keep their result shape")
	}
	sqlite, err := CompileSQLiteMetrics(context.Background(), MetricQuery{Version: 2, Limit: 1, Metrics: []AggSpec{{ID: "m", Op: "count", GroupBy: []string{"g"}, MinCount: 2}}}, metricSchema(), metricOptions())
	if err != nil || !strings.Contains(sqlite.Metrics[0].SQL, "hidden_groups") {
		t.Fatalf("sqlite minCount plan: %v", err)
	}
	combined, err := CompileCombinedMetrics(context.Background(), MetricQuery{Version: 2, Limit: 1, Metrics: []AggSpec{{ID: "a", Op: "count", GroupBy: []string{"g"}}, {ID: "b", Op: "count", GroupBy: []string{"g"}, MinCount: 2}}}, metricSchema(), metricOptions())
	if err != nil || strings.Count(combined.SQL, "AS hidden_groups FROM (") != 2 || !strings.Contains(combined.SQL, "FILTER (WHERE r.count IS NOT NULL)") {
		t.Fatalf("combined envelope: %v\n%s", err, combined.SQL)
	}
}
