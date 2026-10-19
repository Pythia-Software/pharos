package querytable

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func postgresFixtureSQL(t *testing.T, p SQLPlan, explain bool) string {
	t.Helper()
	args := []string{}
	for _, arg := range p.Args {
		switch v := arg.(type) {
		case string:
			args = append(args, "'"+strings.ReplaceAll(v, "'", "''")+"'")
		case float64:
			args = append(args, strconv.FormatFloat(v, 'g', -1, 64))
		case int:
			args = append(args, strconv.Itoa(v))
		case bool:
			args = append(args, strconv.FormatBool(v))
		default:
			t.Fatalf("unsupported test parameter %T", arg)
		}
	}
	sql := "PREPARE qt_fixture AS " + p.SQL + "; "
	if explain {
		sql += "EXPLAIN (ANALYZE, FORMAT JSON) "
	}
	sql += "EXECUTE qt_fixture"
	if len(args) > 0 {
		sql += "(" + strings.Join(args, ",") + ")"
	}
	return sql + ";"
}

func explainPostgresPlan(t *testing.T, p SQLPlan) map[string]any {
	t.Helper()
	dsn := os.Getenv("QT_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set QT_TEST_POSTGRES for PostgreSQL performance fixtures")
	}
	cmd := exec.Command("psql", dsn, "-X", "-qAt", "-v", "ON_ERROR_STOP=1")
	cmd.Stdin = strings.NewReader(postgresFixtureSQL(t, p, true))
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("EXPLAIN: %v\n%s\n%s", err, data, p.SQL)
	}
	var result []map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err, string(data))
	}
	return result[0]["Plan"].(map[string]any)
}

func visitPostgresPlan(plan map[string]any, visit func(map[string]any)) {
	visit(plan)
	if children, ok := plan["Plans"].([]any); ok {
		for _, child := range children {
			visitPostgresPlan(child.(map[string]any), visit)
		}
	}
}

func lateBindingFixture() (Schema, PlanOptions) {
	s := metricSchema()
	s.Fields["status"] = FieldSpec{Kind: FieldNumber, Expr: "(SELECT sum(e.i) FROM generate_series(1,10) AS e(i) WHERE e.i <= r.id)", ExpressionNumeric: true}
	s.Fields["unused"] = FieldSpec{Kind: FieldNumber, Expr: "(SELECT sum(unreachable_probe.i) FROM generate_series(1,100) AS unreachable_probe(i))", ExpressionNumeric: true}
	o := metricOptions()
	o.SourceSQL = "SELECT i AS id,i AS x,1 AS y,'a'::text AS g FROM generate_series(1,1000) AS source(i)"
	o.ExpectedRevisions = map[string]string{"display": "1", "sort": "1"}
	o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
		source := "[status]+1"
		if id == "sort" {
			source = "IF([id]<=995,-[id],NULL)"
		}
		return ComputedColumn{ID: id, Label: id, Revision: "1", Expression: ComputedExpression{"qt-expr", 1, source}}, nil
	})
	return s, o
}

func TestPostgresSelectedBindingsFollowWindow(t *testing.T) {
	s, o := lateBindingFixture()
	for _, computedSort := range []bool{false, true} {
		q := WireQuery{Select: []string{"id", "status", "@computed/display"}, OrderBy: []OrderBy{{Field: "id", Dir: "asc"}}, Limit: 3, Offset: 7, Where: []WhereTerm{{Field: "id", Op: ">", Value: "2"}}}
		if computedSort {
			q.OrderBy = []OrderBy{{Field: "@computed/sort", Dir: "desc", Nulls: "first"}}
		}
		p, err := CompileComputedRows(context.Background(), q, s, o)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.SQL, "unreachable_probe") {
			t.Fatal("unreachable binding projected")
		}
		if strings.Contains(p.Stages[0].SQL, "sum(e.i)") {
			t.Fatal("SELECT binding evaluated on full population")
		}
		window, lookup := -1, -1
		for i, stage := range p.Stages {
			if strings.Contains(stage.SQL, " LIMIT ") {
				window = i
			}
			if strings.Contains(stage.SQL, "sum(e.i)") {
				lookup = i
			}
		}
		if window < 0 || lookup <= window {
			t.Fatal("late binding precedes page", p.Stages)
		}
		if computedSort && p.ResolvedRevisions["sort"] != "1" {
			t.Fatal("hidden sort revision missing")
		}
		if p.ResolvedRevisions["display"] != "1" {
			t.Fatal("SELECT revision missing")
		}
	}
}

func TestPostgresSelectedBindingSubplanLoops(t *testing.T) {
	s, o := lateBindingFixture()
	for _, computedSort := range []bool{false, true} {
		for _, offset := range []int{0, 7, 9999} {
			t.Run(fmt.Sprintf("computed=%t/offset=%d", computedSort, offset), func(t *testing.T) {
				q := WireQuery{Select: []string{"id", "status", "@computed/display"}, OrderBy: []OrderBy{{Field: "id", Dir: "asc"}}, Limit: 3, Offset: offset}
				if computedSort {
					q.OrderBy = []OrderBy{{Field: "@computed/sort", Dir: "desc", Nulls: "first"}}
				}
				p, err := CompileComputedRows(context.Background(), q, s, o)
				if err != nil {
					t.Fatal(err)
				}
				plan := explainPostgresPlan(t, p)
				subplans := 0
				visitPostgresPlan(plan, func(node map[string]any) {
					if node["Parent Relationship"] == "SubPlan" {
						subplans++
						want := float64(3)
						if offset == 9999 {
							want = 0
						}
						if loops := node["Actual Loops"]; loops != want {
							t.Fatalf("selected subplan loops=%v want %v", loops, want)
						}
					}
				})
				if subplans != 1 {
					t.Fatalf("expected one selected correlated subplan, got %d", subplans)
				}
				result := executePlan(t, p)
				if offset == 9999 {
					if len(result) != 0 {
						t.Fatal(result)
					}
					return
				}
				if len(result) != 3 {
					t.Fatal(result)
				}
				first := float64(offset + 1)
				if computedSort {
					if offset == 0 {
						first = 996
					} else {
						first = float64(offset - 4)
					}
				}
				if result[0]["column0"] != first {
					t.Fatal("global sorting/window changed", result)
				}
				for _, row := range result {
					if row["column1_error"] != nil || row["column2_error"] != nil || row["column2"] != row["column1"].(float64)+1 {
						t.Fatal(row)
					}
				}
			})
		}
	}
}

func TestPostgresLateBindingSourceAliasCollision(t *testing.T) {
	s := Schema{IDField: "id", Fields: map[string]FieldSpec{
		"id":     {Kind: FieldNumber, Expr: "r.id", Sortable: true, ExpressionNumeric: true},
		"status": {Kind: FieldNumber, Expr: "r.f2", ExpressionNumeric: true},
		"unused": {Kind: FieldNumber, Expr: "r.unreachable", ExpressionNumeric: true},
	}}
	o := PlanOptions{SourceSQL: "SELECT i AS id,20 AS f2 FROM generate_series(1,10) i"}
	p, err := CompileComputedRows(context.Background(), WireQuery{Select: []string{"status"}, Limit: 2}, s, o)
	if err != nil {
		t.Fatal(err)
	}
	result := executePlan(t, p)
	if len(result) != 2 || result[0]["column0"] != float64(20) {
		t.Fatal(result)
	}
}

func TestPostgresLateBindingSourceNames(t *testing.T) {
	s, o := lateBindingFixture()
	o.SourceSQL = "SELECT * FROM qt_source_rows"
	p, err := CompileComputedRows(context.Background(), WireQuery{Select: []string{"status"}, Limit: 1}, s, o)
	if err != nil {
		t.Fatal(err)
	}
	if p.Stages[0].Name == "qt_source_rows" || p.Stages[0].SQL != o.SourceSQL {
		t.Fatal("authorized source table shadowed", p.Stages[0])
	}
}

func TestPostgresShownMetricBindingsFollowWindow(t *testing.T) {
	s, o := lateBindingFixture()
	o.ExpectedRevisions["base"] = "1"
	resolver := o.Resolver
	o.Resolver = DefinitionResolverFunc(func(ctx context.Context, id string) (ComputedColumn, error) {
		if id == "display" || id == "base" {
			source := "[status]"
			if id == "display" {
				source = "[@computed/base]+1"
			}
			return ComputedColumn{ID: id, Label: id, Revision: "1", Expression: ComputedExpression{"qt-expr", 1, source}}, nil
		}
		return resolver.ResolveComputed(ctx, id)
	})
	for _, computedSort := range []bool{false, true} {
		for _, offset := range []int{0, 7, 9999} {
			t.Run(fmt.Sprintf("computed=%t/offset=%d", computedSort, offset), func(t *testing.T) {
				q := MetricQuery{Version: 2, Limit: 3, Offset: offset,
					Where:   []WhereTerm{{Field: "id", Op: ">", Value: "2"}},
					OrderBy: []OrderBy{{Field: "id", Dir: "asc"}},
					Metrics: []AggSpec{
						{ID: "scalar", Expression: "SUM([@computed/display])", Scope: "shownRows"},
						{ID: "paired", Expression: "SUM([@computed/display])", ExpressionY: "AVG([@computed/display])", Scope: "shownRows", GroupBy: []string{"g"}},
						{ID: "box", Distribution: &MetricDistribution{Kind: "box", Input: "[@computed/display]"}, Scope: "shownRows"},
						{ID: "histogram", Distribution: &MetricDistribution{Kind: "histogram", Input: "[@computed/display]", Bins: 3}, Scope: "shownRows", GroupBy: []string{"g"}},
					},
				}
				if computedSort {
					q.OrderBy = []OrderBy{{Field: "@computed/sort", Dir: "desc", Nulls: "first"}}
				}
				p, err := CompileCombinedMetrics(context.Background(), q, s, o)
				if err != nil {
					t.Fatal(err)
				}
				got := executePlan(t, p.SQLPlan)
				for i, metric := range p.Metrics {
					window, lookup := -1, -1
					for j, stage := range metric.Stages {
						if strings.Contains(stage.SQL, " LIMIT ") {
							window = j
						}
						if strings.Contains(stage.SQL, "sum(e.i)") {
							lookup = j
						}
					}
					if window < 0 || lookup <= window {
						t.Fatal("measure binding precedes window", metric.Stages)
					}
					if metric.reduction != nil && !strings.Contains(metric.Stages[metric.reduction.stage].SQL, "COUNT(*) AS count") {
						t.Fatal("reduction metadata shifted", metric.reduction)
					}
					rows := executePlan(t, metric.SQLPlan)
					actual := []map[string]any{}
					for _, bucket := range got[i]["buckets"].([]any) {
						actual = append(actual, bucket.(map[string]any))
					}
					if !reflect.DeepEqual(actual, rows) {
						t.Fatalf("combined %s: got %v want %v", metric.ID, actual, rows)
					}
					subplans := 0
					visitPostgresPlan(explainPostgresPlan(t, metric.SQLPlan), func(node map[string]any) {
						if node["Parent Relationship"] == "SubPlan" {
							lookup := false
							visitPostgresPlan(node, func(child map[string]any) {
								if child["Node Type"] == "Function Scan" && child["Alias"] == "e" {
									lookup = true
								}
							})
							if !lookup {
								return
							}
							subplans++
							want := float64(3)
							if offset == 9999 {
								want = 0
							}
							if node["Actual Loops"] != want {
								t.Fatalf("%s lookup loops %v want %v", metric.ID, node["Actual Loops"], want)
							}
						}
					})
					if subplans != 1 {
						t.Fatalf("%s lookup subplans %d", metric.ID, subplans)
					}
				}
				// Compare transitive computed SUM with the row sidecars of the same page.
				rowPlan, err := CompileComputedRows(context.Background(), WireQuery{Select: []string{"@computed/display"}, Where: q.Where, OrderBy: q.OrderBy, Limit: q.Limit, Offset: q.Offset}, s, o)
				if err != nil {
					t.Fatal(err)
				}
				sum := float64(0)
				rowValues := executePlan(t, rowPlan)
				for _, row := range rowValues {
					sum += row["column0"].(float64)
				}
				value := got[0]["buckets"].([]any)[0].(map[string]any)["value"]
				if len(rowValues) > 0 && value != sum {
					t.Fatalf("shown SUM %v sidecars sum %v", value, sum)
				}
				if len(rowValues) == 0 && value != nil {
					t.Fatalf("empty page SUM %v", value)
				}
			})
		}
	}
}
