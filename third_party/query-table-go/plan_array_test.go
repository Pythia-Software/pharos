package querytable

import (
	"context"
	"reflect"
	"testing"
)

func TestPostgresRawArrayProjection(t *testing.T) {
	schema := Schema{IDField: "id", Fields: map[string]FieldSpec{
		"id":   {Name: "id", Kind: FieldNumber, Expr: "r.id", SortExpr: "r.id", Sortable: true, ExpressionNumeric: true},
		"tags": {Name: "tags", Kind: FieldTextArray, Expr: "r.tags"},
	}}
	options := PlanOptions{SourceSQL: "SELECT id,tags FROM records"}
	plan, err := CompileRowsV2(context.Background(), ServerQueryV2{Version: 2, Profile: ServerExpressionProfile, WireQuery: WireQuery{Select: []string{"id", "tags"}, Limit: 10}}, schema, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Columns) != 2 || plan.Columns[1].Type != "textarray" {
		t.Fatalf("columns: %+v", plan.Columns)
	}
	// Raw projection does not expand the formula language or grouping policy.
	if _, err = CompileRowExpression(context.Background(), "[tags]", schema, options); err == nil {
		t.Fatal("arrays must remain unavailable in formulas")
	}
	if _, err = CompileMetrics(context.Background(), MetricQuery{Version: 2, Metrics: []AggSpec{{ID: "groups", Op: "count", GroupBy: []string{"tags"}}}}, schema, options); err == nil {
		t.Fatal("array grouping must remain unavailable")
	}
}

func TestPostgresRawArrayProjectionValues(t *testing.T) {
	schema := Schema{IDField: "id", Fields: map[string]FieldSpec{
		"id":   {Kind: FieldNumber, Expr: "r.id", Sortable: true, ExpressionNumeric: true},
		"tags": {Kind: FieldTextArray, Expr: "r.tags"},
	}}
	options := PlanOptions{SourceSQL: "SELECT * FROM (VALUES (1,ARRAY['alpha','beta']::text[]),(2,ARRAY[]::text[]),(3,NULL::text[])) AS records(id,tags)"}
	plan, err := CompileRowsV2(context.Background(), ServerQueryV2{Version: 2, Profile: ServerExpressionProfile, WireQuery: WireQuery{Select: []string{"id", "tags"}, Limit: 3}}, schema, options)
	if err != nil {
		t.Fatal(err)
	}
	result := executePlan(t, plan)
	if len(result) != 3 || !reflect.DeepEqual(result[0]["column1"], []any{"alpha", "beta"}) || !reflect.DeepEqual(result[1]["column1"], []any{}) || result[2]["column1"] != nil {
		t.Fatal(result)
	}
	for _, row := range result {
		if row["column1_error"] != nil {
			t.Fatal(row)
		}
	}
}
