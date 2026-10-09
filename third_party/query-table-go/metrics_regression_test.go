package querytable

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPostgresHistogramFloatingBoundaries(t *testing.T) {
	// Exact expected edges/counts from core histogramEdges/histogramCounts.
	// Counts must match exactly, even when edges differ by only one ULP.
	for _, tc := range []struct{ name, rows, edges, counts string }{
		{"decimal_boundary", "(1,0.1),(2,0.3),(3,0.4)", "[0.1 0.2 0.30000000000000004 0.4]", "[1 1 1]"},
		{"exact_endpoints", "(1,-0.1),(2,0),(3,0.1),(4,0.2)", "[-0.1 0 0.1 0.2]", "[1 1 2]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := metricOptions()
			o.SourceSQL = "SELECT id,x,1 AS y,'a'::text AS g FROM (VALUES " + tc.rows + ") fixture(id,x)"
			r := executePlan(t, metricPlan(t, AggSpec{Distribution: &MetricDistribution{Kind: "histogram", Input: "[x]", Bins: 3}}, o))
			if len(r) != 1 || r[0]["error"] != nil {
				t.Fatal(r)
			}
			d := r[0]["distribution"].(map[string]any)
			if fmt.Sprint(d["edges"]) != tc.edges || fmt.Sprint(d["counts"]) != tc.counts || d["n"] != r[0]["count"] {
				t.Fatal(r)
			}
		})
	}
}

func TestPostgresNullExpressionDistributions(t *testing.T) {
	for _, input := range []string{"NULL", "COALESCE(NULL,NULL)"} {
		for _, kind := range []string{"box", "histogram"} {
			for _, empty := range []bool{false, true} {
				for _, grouped := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/empty=%t/grouped=%t", input, kind, empty, grouped), func(t *testing.T) {
						o := metricOptions()
						o.SourceSQL = "SELECT i AS id,i AS x,1 AS y,'a'::text AS g FROM generate_series(1,3) i"
						count := float64(3)
						if empty {
							o.SourceSQL += " WHERE FALSE"
							count = 0
						}
						spec := AggSpec{Distribution: &MetricDistribution{Kind: kind, Input: input, Bins: 3}}
						if grouped {
							spec.GroupBy = []string{"g"}
						}
						r := executePlan(t, metricPlan(t, spec, o))
						if grouped && empty {
							if len(r) != 0 {
								t.Fatal(r)
							}
							return
						}
						if len(r) != 1 || r[0]["error"] != nil || r[0]["count"] != count || r[0]["null_count"] != count || r[0]["samples"] != float64(0) {
							t.Fatal(r)
						}
						d := r[0]["distribution"].(map[string]any)
						if d["kind"] != kind {
							t.Fatal(d)
						}
						if kind == "box" {
							if d["summary"] != nil || r[0]["value"] != nil || r[0]["quartiles"] != nil {
								t.Fatal(r)
							}
						} else {
							if len(d["edges"].([]any)) != 0 || len(d["counts"].([]any)) != 0 || d["n"] != float64(0) || r[0]["value"] != float64(0) {
								t.Fatal(r)
							}
						}
					})
				}
			}
		}
	}
}

func TestPostgresNullComputedKeys(t *testing.T) {
	for _, source := range []string{"NULL", "COALESCE(NULL,NULL)"} {
		t.Run(source, func(t *testing.T) {
			o := metricOptions()
			// Reverse physical order and page past the first 100 rows to verify global
			// stable-ID ordering when every computed sort value is NULL.
			o.SourceSQL = "SELECT i AS id,i AS x,1 AS y,'a'::text AS g FROM generate_series(205,1,-1) i"
			o.ExpectedRevisions = map[string]string{"nil": "r1"}
			o.ComputedGroupable = map[string]bool{"nil": true}
			o.Resolver = DefinitionResolverFunc(func(_ context.Context, id string) (ComputedColumn, error) {
				return ComputedColumn{ID: id, Label: id, Revision: "r1", Expression: ComputedExpression{"qt-expr", 1, source}}, nil
			})
			for _, dir := range []string{"asc", "desc"} {
				for _, nulls := range []string{"first", "last"} {
					for _, selected := range []bool{false, true} {
						q := WireQuery{Select: []string{"id"}, OrderBy: []OrderBy{{Field: "@computed/nil", Dir: dir, Nulls: nulls}}, Limit: 2, Offset: 100}
						if selected {
							q.Select = append(q.Select, "@computed/nil")
						}
						p, e := CompileComputedRows(context.Background(), q, metricSchema(), o)
						if e != nil {
							t.Fatal(e)
						}
						r := executePlan(t, p)
						if len(r) != 2 || r[0]["column0"] != float64(101) || r[1]["column0"] != float64(102) {
							t.Fatal(r)
						}
						if selected && (r[0]["column1"] != nil || r[0]["column1_error"] != nil || p.Columns[1].Type != "null") {
							t.Fatal(r, p.Columns)
						}
					}
					mq := MetricQuery{Version: 2, OrderBy: []OrderBy{{Field: "@computed/nil", Dir: dir, Nulls: nulls}}, Limit: 2, Offset: 100, Metrics: []AggSpec{{ID: "m", Expression: "SUM([x])", Scope: "shownRows"}}}
					batch, e := CompileMetrics(context.Background(), mq, metricSchema(), o)
					if e != nil {
						t.Fatal(e)
					}
					r := executePlan(t, batch.Metrics[0].SQLPlan)
					if len(r) != 1 || r[0]["error"] != nil || r[0]["value"] != float64(203) || r[0]["count"] != float64(2) {
						t.Fatal(r)
					}
				}
			}
			for _, empty := range []bool{false, true} {
				if empty {
					o.SourceSQL += " WHERE FALSE"
				}
				r := executePlan(t, metricPlan(t, AggSpec{Expression: "SUM([x])", GroupBy: []string{"@computed/nil"}}, o))
				if empty {
					if len(r) != 0 {
						t.Fatal(r)
					}
					continue
				}
				if len(r) != 1 || r[0]["group0"] != nil || r[0]["group0_error"] != nil || r[0]["error"] != nil || r[0]["count"] != float64(205) || r[0]["value"] != float64(21115) || r[0]["group_count"] != float64(1) {
					t.Fatal(r)
				}
			}
		})
	}
}

func TestPostgresBoxFloatingFences(t *testing.T) {
	// Golden quartiles and memberships from core boxSummary. Exact comparisons
	// are intentional: tolerating a ULP here can change the observed whiskers.
	for _, tc := range []struct {
		name           string
		values         []float64
		q1, median, q3 float64
		low, high      float64
		outliers       []float64
	}{
		{"review_upper", []float64{2.1, 2.4, 2.5, 3.2}, 2.3249999999999997, 2.45, 2.675, 2.1, 3.2, nil},
		{"review_lower", []float64{-3.2, -2.5, -2.4, -2.1}, -2.675, -2.45, -2.3249999999999997, -3.2, -2.1, nil},
		{"both_on_fence", []float64{1, 4, 5, 6, 9}, 4, 5, 6, 1, 9, nil},
		{"both_just_inside", []float64{1.0000000000000002, 4, 5, 6, 8.999999999999998}, 4, 5, 6, 1.0000000000000002, 8.999999999999998, nil},
		{"both_just_outside", []float64{0.9999999999999999, 4, 5, 6, 9.000000000000002}, 4, 5, 6, 4, 6, []float64{0.9999999999999999, 9.000000000000002}},
		{"two_samples", []float64{0.1, 0.2}, 0.125, 0.15000000000000002, 0.17500000000000002, 0.1, 0.2, nil},
		{"singleton", []float64{0.1}, 0.1, 0.1, 0.1, 0.1, 0.1, nil},
		{"constant", []float64{0.1, 0.1, 0.1, 0.1}, 0.1, 0.1, 0.1, 0.1, 0.1, nil},
	} {
		for _, whiskers := range []string{"tukey", "minmax"} {
			for _, grouped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/grouped=%t", tc.name, whiskers, grouped), func(t *testing.T) {
					// Reverse input order; NULL samples must not affect n or positions.
					rows := []string{"(0,NULL::double precision,NULL::text)"}
					for i := len(tc.values) - 1; i >= 0; i-- {
						rows = append(rows, fmt.Sprintf("(%d,%.17g,NULL::text)", i+1, tc.values[i]))
					}
					spec := AggSpec{Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: whiskers}}
					if grouped {
						spec.GroupBy = []string{"g"}
						rows = append(rows, "(100,100,'other')")
					}
					o := metricOptions()
					o.SourceSQL = "SELECT id,x,1 AS y,g FROM (VALUES " + strings.Join(rows, ",") + ") fixture(id,x,g)"
					result := executePlan(t, metricPlan(t, spec, o))
					wantGroups := 1
					if grouped {
						wantGroups = 2
					}
					if len(result) != wantGroups {
						t.Fatal(result)
					}
					var row map[string]any
					for _, r := range result {
						if r["group0"] == nil {
							row = r
						}
					}
					if row == nil || row["error"] != nil || row["count"] != float64(len(tc.values)+1) || row["samples"] != float64(len(tc.values)) || row["null_count"] != float64(1) || row["group_count"] != float64(wantGroups) {
						t.Fatal(result)
					}
					if _, leaked := row["sorted_samples"]; leaked {
						t.Fatal("working array leaked into output")
					}
					s := row["distribution"].(map[string]any)["summary"].(map[string]any)
					lo, hi, outliers := tc.low, tc.high, tc.outliers
					if whiskers == "minmax" {
						lo, hi, outliers = tc.values[0], tc.values[len(tc.values)-1], nil
					}
					for key, want := range map[string]any{"q1": tc.q1, "median": tc.median, "q3": tc.q3, "low": lo, "high": hi, "min": tc.values[0], "max": tc.values[len(tc.values)-1], "n": float64(len(tc.values)), "outlierCount": float64(len(outliers)), "method": "exact-linear", "whiskers": whiskers} {
						if s[key] != want {
							t.Fatalf("%s: got %v want %v; %v", key, s[key], want, s)
						}
					}
					if row["value"] != tc.median {
						t.Fatal(row)
					}
					actual := s["outliers"].([]any)
					if len(actual) != len(outliers) {
						t.Fatal(s)
					}
					for i, want := range outliers {
						if actual[i] != want {
							t.Fatal(s)
						}
					}
				})
			}
		}
	}
}

func TestPostgresMetricSafeIntegerBoundary(t *testing.T) {
	for _, op := range []string{"SUM", "AVG", "MIN", "MAX", "COUNT_DISTINCT", "MEDIAN"} {
		for _, sample := range []string{"9007199254740992", "-9007199254740992", "9007199254740993", "1e100"} {
			t.Run(op+"/"+sample, func(t *testing.T) {
				o := metricOptions()
				// Cancellation/averaging/extrema/distinct cardinality cannot hide an
				// unsafe sample. A separate valid group keeps its independent result.
				o.SourceSQL = "SELECT id,x,1 AS y,g FROM (VALUES (1," + sample + "::double precision,'bad'),(2,-(" + sample + "::double precision),'bad'),(3,0,'bad'),(4,NULL,'bad'),(5,2,'ok')) fixture(id,x,g)"
				r := executePlan(t, metricPlan(t, AggSpec{Expression: op + "([x])", ExpressionY: "COUNT([x])", GroupBy: []string{"g"}}, o))
				if len(r) != 2 {
					t.Fatal(r)
				}
				for _, row := range r {
					if row["group0"] == "bad" {
						if row["error"] != "unsafe_integer" || row["value"] != nil || row["y"] != float64(3) || row["y_error"] != nil || row["count"] != float64(4) {
							t.Fatal(r)
						}
					} else if row["error"] != nil || row["value"] == nil {
						t.Fatal(r)
					}
				}
			})
		}
		for _, sample := range []string{"9007199254740991", "-9007199254740991", "0.125", "1e-300", "NULL"} {
			t.Run(op+"/safe/"+sample, func(t *testing.T) {
				o := metricOptions()
				o.SourceSQL = "SELECT 1 AS id," + sample + "::double precision AS x,1 AS y,'a'::text AS g"
				r := executePlan(t, metricPlan(t, AggSpec{Expression: op + "([x])"}, o))
				if len(r) != 1 || r[0]["error"] != nil {
					t.Fatal(r)
				}
				if sample != "NULL" && r[0]["value"] == nil {
					t.Fatal(r)
				}
			})
		}
	}
}

func TestPostgresMetricBoundaryLazyAndFinalResults(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT 1 AS id,9007199254740992::double precision AS x,1 AS y,'a'::text AS g"
	for _, tc := range []struct {
		expression string
		value, err any
	}{
		{"COUNT([x])", float64(1), nil},
		{"IF(TRUE,COUNT(),MIN([x]))", float64(1), nil},
		{"IF(FALSE,MAX([x]),COUNT())", float64(1), nil},
		{"COALESCE(COUNT(),AVG([x]))", float64(1), nil},
		{"COALESCE(MIN([x]),COUNT())", nil, "unsafe_integer"},
		{"IF(TRUE,AVG([x]),COUNT())", nil, "unsafe_integer"},
		{"MIN(IF(TRUE,1,[x]))", float64(1), nil},
		{"MIN([x]/2)", float64(4503599627370496), nil},
		{"COUNT()+9007199254740991", nil, "unsafe_integer"},
		{"-9007199254740991-COUNT()", nil, "unsafe_integer"},
		{"IF(TRUE,COUNT(),9007199254740992)", float64(1), nil},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			r := executePlan(t, metricPlan(t, AggSpec{Expression: tc.expression}, o))
			if len(r) != 1 || r[0]["value"] != tc.value || r[0]["error"] != tc.err {
				t.Fatal(r)
			}
		})
	}
	// Row expressions keep their bounded arithmetic semantics.
	p, e := CompileRowExpression(context.Background(), "[x]+1", metricSchema(), o)
	if e != nil {
		t.Fatal(e)
	}
	r := executePlan(t, p)
	if r[0]["error"] != nil {
		t.Fatal(r)
	}
}

func TestPostgresDistributionSafeIntegerBoundary(t *testing.T) {
	for _, kind := range []string{"box", "histogram"} {
		for _, sample := range []string{"9007199254740992", "-9007199254740992", "9007199254740993"} {
			t.Run(kind+"/"+sample, func(t *testing.T) {
				o := metricOptions()
				o.SourceSQL = "SELECT id,x,1 AS y,g FROM (VALUES (1,2::double precision,'ok'),(2," + sample + ",'bad'),(3,NULL,'bad')) fixture(id,x,g)"
				spec := AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: kind, Input: "[x]", Bins: 2}}
				r := executePlan(t, metricPlan(t, spec, o))
				if len(r) != 2 {
					t.Fatal(r)
				}
				for _, row := range r {
					if kind == "histogram" || row["group0"] == "bad" {
						if row["error"] != "unsafe_integer" || row["distribution"] != nil || row["value"] != nil {
							t.Fatal(r)
						}
					} else if row["error"] != nil || row["distribution"] == nil {
						t.Fatal(r)
					}
					if row["group0"] == "bad" && (row["null_count"] != float64(1) || row["samples"] != float64(0)) {
						t.Fatal(r)
					}
				}
				// Page selection precedes sample validation and shared extent.
				spec.Scope = "shownRows"
				r = executePlan(t, metricPlan(t, spec, o))
				if len(r) != 1 || r[0]["error"] != nil || r[0]["samples"] != float64(1) {
					t.Fatal(r)
				}
			})
		}
		for _, sample := range []string{"9007199254740991", "-9007199254740991", "0.125"} {
			t.Run(kind+"/safe/"+sample, func(t *testing.T) {
				o := metricOptions()
				o.SourceSQL = "SELECT 1 AS id," + sample + "::double precision AS x,1 AS y,'a'::text AS g"
				r := executePlan(t, metricPlan(t, AggSpec{Distribution: &MetricDistribution{Kind: kind, Input: "[x]"}}, o))
				if len(r) != 1 || r[0]["error"] != nil || r[0]["distribution"] == nil {
					t.Fatal(r)
				}
			})
		}
	}
}

func TestPostgresMetricBoundaryScopeAndPairedOutput(t *testing.T) {
	o := metricOptions()
	o.SourceSQL = "SELECT id,x,1 AS y,'a'::text AS g FROM (VALUES (1,1::double precision),(2,9007199254740992)) fixture(id,x)"
	for _, op := range []string{"SUM", "AVG", "MIN", "MAX", "COUNT_DISTINCT"} {
		for _, scope := range []string{"allMatching", "shownRows"} {
			t.Run(op+"/"+scope, func(t *testing.T) {
				r := executePlan(t, metricPlan(t, AggSpec{Expression: op + "([x])", Scope: scope}, o))
				if scope == "allMatching" {
					if r[0]["error"] != "unsafe_integer" || r[0]["value"] != nil {
						t.Fatal(r)
					}
				} else if r[0]["error"] != nil || r[0]["value"] != float64(1) || r[0]["count"] != float64(1) {
					t.Fatal(r)
				}
			})
		}
	}
	r := executePlan(t, metricPlan(t, AggSpec{Expression: "COUNT()", ExpressionY: "COUNT()+9007199254740991"}, o))
	if r[0]["error"] != nil || r[0]["value"] != float64(2) || r[0]["y_error"] != "unsafe_integer" || r[0]["y"] != nil {
		t.Fatal(r)
	}
}

func TestMetricBoundaryParameters(t *testing.T) {
	for _, spec := range []AggSpec{
		{Expression: "AVG([x])+COUNT()"},
		{Expression: "SUM([x])"},
		{Distribution: &MetricDistribution{Kind: "box", Input: "[x]"}},
		{Distribution: &MetricDistribution{Kind: "histogram", Input: "[x]"}},
	} {
		p := metricPlan(t, spec, metricOptions())
		if strings.Contains(p.SQL, "9007199254740991") {
			t.Fatal("metric boundary was inlined instead of bound")
		}
		found := false
		for _, arg := range p.Args {
			if arg == float64(9007199254740991) {
				found = true
			}
		}
		if !found {
			t.Fatal("missing IEEE safe-integer boundary parameter")
		}
	}
}

func TestPostgresRepeatedComputedTextKeepsErrors(t *testing.T) {
	o := metricOptions()
	o.ExpectedRevisions = map[string]string{"text": "1"}
	o.ComputedGroupable = map[string]bool{"text": true}
	o.Resolver = DefinitionResolverFunc(func(context.Context, string) (ComputedColumn, error) {
		return ComputedColumn{ID: "text", Label: "Text", Revision: "1", Expression: ComputedExpression{"qt-expr", 1, `IF([x]/([y]-3)>0,"pos","neg")`}}, nil
	})
	p, err := CompileComputedRows(context.Background(), WireQuery{Select: []string{"id", "@computed/text"}, OrderBy: []OrderBy{{Field: "@computed/text", Dir: "asc"}}, Limit: 10}, metricSchema(), o)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range executePlan(t, p) {
		if r["column0"] == float64(2) {
			found = true
			if r["column1_error"] != "divide_by_zero" || r["column1"] != nil {
				t.Fatal(r)
			}
		}
	}
	if !found {
		t.Fatal("missing errored row")
	}
	for _, spec := range []AggSpec{
		{Expression: "COUNT([@computed/text])", Scope: "shownRows"},
		{Expression: "MIN([@computed/text])", GroupBy: []string{"@computed/text"}},
	} {
		plan, err := CompileMetrics(context.Background(), MetricQuery{Version: 2, Limit: 10, OrderBy: []OrderBy{{Field: "@computed/text", Dir: "asc"}}, Metrics: []AggSpec{func() AggSpec { spec.ID = "m"; return spec }()}}, metricSchema(), o)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, r := range executePlan(t, plan.Metrics[0].SQLPlan) {
			if r["error"] == "divide_by_zero" {
				found = true
			}
		}
		if !found {
			t.Fatal("aggregate lost computed error")
		}
	}
}

func TestPostgresTukeyRetainsBoundedOutlierTails(t *testing.T) {
	for _, mode := range []string{"low", "high", "both"} {
		o := metricOptions()
		samples := "CASE WHEN i<=30 THEN -1000-i ELSE 0 END"
		if mode == "high" {
			samples = "CASE WHEN i>170 THEN 1000+i ELSE 0 END"
		}
		if mode == "both" {
			samples = "CASE WHEN i<=30 THEN -1000-i WHEN i>170 THEN 1000+i ELSE 0 END"
		}
		o.SourceSQL = "SELECT i AS id," + samples + " AS x,1 AS y,CASE WHEN g=1 THEN NULL::text ELSE 'b' END AS g FROM generate_series(1,200) i CROSS JOIN generate_series(1,2) g"
		r := executePlan(t, metricPlan(t, AggSpec{GroupBy: []string{"g"}, Distribution: &MetricDistribution{Kind: "box", Input: "[x]", Whiskers: "tukey"}}, o))
		if len(r) != 2 {
			t.Fatal(r)
		}
		for _, row := range r {
			if _, leaked := row["sorted_samples"]; leaked {
				t.Fatal("sample array leaked")
			}
			summary := row["distribution"].(map[string]any)["summary"].(map[string]any)
			out := summary["outliers"].([]any)
			wantCount := float64(30)
			first, last := -1030.0, -1001.0
			if mode == "high" {
				first, last = 1171, 1200
			}
			if mode == "both" {
				wantCount = 60
				last = 1200
			}
			if summary["outlierCount"] != wantCount || len(out) != 20 || out[0] != first || out[19] != last || summary["low"] != float64(0) || summary["high"] != float64(0) {
				t.Fatal(mode, summary)
			}
			for i := 1; i < len(out); i++ {
				if out[i-1].(float64) >= out[i].(float64) {
					t.Fatal("duplicated/unordered outlier tail", out)
				}
			}
		}
	}
}
