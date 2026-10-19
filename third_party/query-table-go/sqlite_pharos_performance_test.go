package querytable

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func TestPharosIntegerReductionPromotion(t *testing.T) {
	for _, op := range []string{"sum", "avg"} {
		for _, values := range [][]float64{{1, 2, 3}, {1, 2, 0.1, 0.2}, {9007199254740991, -9007199254740991}, {9007199254740991, 1, -9007199254740991}, {1, -0.1, -0.9}, {1e-300, 1, -1}} {
			a := &sqliteV2Aggregate{op: op}
			sum, absolute := sqliteRat(0), sqliteRat(0)
			for _, n := range values {
				if err := a.Step([]driver.Value{n}); err != nil {
					t.Fatal(err)
				}
				r := sqliteRat(n)
				sum.Add(sum, r)
				absolute.Add(absolute, new(big.Rat).Abs(r))
			}
			data, err := a.Value()
			if err != nil {
				t.Fatal(err)
			}
			var result sqliteNumericResult
			if err = json.Unmarshal([]byte(data.(string)), &result); err != nil {
				t.Fatal(err)
			}
			if op == "sum" && absolute.Cmp(sqliteRat(9007199254740991)) > 0 {
				if result.Error != "unsafe_integer" {
					t.Fatal(values, result)
				}
				continue
			}
			if op == "avg" {
				sum.Quo(sum, sqliteRat(float64(len(values))))
			}
			if code := sqliteRatError(sum); code != "" {
				if result.Error != code {
					t.Fatal(values, result, code)
				}
				continue
			}
			want, _ := sum.Float64()
			if result.Error != "" || result.Value == nil || *result.Value != want {
				t.Fatalf("%s %v = %+v, want %g", op, values, result, want)
			}
		}
	}
}
func TestPharosMetricCloneIndependence(t *testing.T) {
	n := int64(2)
	m := SQLiteMetricV2{Buckets: []SQLiteMetricV2Bucket{{Keys: []any{"a"}, NullCount: &n, Distribution: &MetricHistogramDistribution{Edges: []float64{0, 1}, Counts: []int64{2}}}}}
	copy := cloneSQLiteMetric(m)
	copy.Buckets[0].Keys[0] = "b"
	*copy.Buckets[0].NullCount = 8
	copy.Buckets[0].Distribution.(*MetricHistogramDistribution).Counts[0] = 9
	if m.Buckets[0].Keys[0] != "a" || *m.Buckets[0].NullCount != 2 || m.Buckets[0].Distribution.(*MetricHistogramDistribution).Counts[0] != 2 {
		t.Fatal("sibling metric mutated")
	}
}
func TestPharosRelativeDatetimeResolver(t *testing.T) {
	clock := time.Date(2026, 10, 8, 0, 0, 0, 123456789, time.UTC)
	for _, value := range []string{"-1h", "+2d3h10m", "+0s"} {
		resolved, err := ResolveRelativeDatetime(value, clock)
		if err != nil {
			t.Fatal(err)
		}
		offset, err := parseRelativeDuration(value)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := relativeTimestamp(clock, offset)
		if !reflect.DeepEqual(resolved, want.Format(time.RFC3339Nano)) {
			t.Fatal(value, resolved)
		}
	}
	if _, err := ResolveRelativeDatetime("-oops", clock); err == nil {
		t.Fatal("invalid duration accepted")
	}
}

func TestPharosReductionFusionPolicies(t *testing.T) {
	schema := Schema{Name: "samples", IDField: "id", Fields: map[string]FieldSpec{
		"id": {Kind: FieldText, Expr: "r.id", Sortable: true}, "x": {Kind: FieldNumber, Expr: "r.x", ExpressionNumeric: true},
	}}
	q := MetricQuery{Version: 2, Profile: SQLiteExpressionProfile, Limit: 5, Metrics: []AggSpec{{ID: "sum", Expression: "SUM([x])"}, {ID: "plus", Expression: "SUM([x])+2"}, {ID: "avg", Expression: "AVG([x])"}, {ID: "shown", Expression: "AVG([x])", Scope: "shownRows"}}}
	batch, err := CompileSQLiteMetrics(context.Background(), q, schema, PlanOptions{SourceSQL: "SELECT * FROM samples"})
	if err != nil {
		t.Fatal(err)
	}
	cache := newSQLiteReductionCache(batch)
	key := sqliteReductionKey(batch.Metrics[0])
	if !cache.shared[key] {
		t.Fatal("identical reductions were not reusable")
	}
	fusion := cache.fusions[sqliteFusionKey(batch.Metrics[0])]
	if fusion == nil || len(fusion.reductions) != 2 {
		t.Fatal("compatible reductions did not fuse", fusion)
	}
	if sqliteFusionKey(batch.Metrics[0]) == sqliteFusionKey(batch.Metrics[3]) {
		t.Fatal("different scopes shared a population")
	}
}

func TestRelativeDatetimeResolverBoundsAndAbsoluteValues(t *testing.T) {
	clock := time.Date(2026, 10, 8, 0, 0, 0, 123456789, time.UTC)
	for _, value := range []string{"", "2026-10-08T00:00:00Z", "arbitrary absolute operand"} {
		got, err := ResolveRelativeDatetime(value, clock)
		if err != nil || got != value {
			t.Fatal(value, got, err)
		}
	}
	for _, value := range []string{"-oops", "+1.5h", "+8640000000000001ms", "+18446744073709551616ms", "+"} {
		if _, err := ResolveRelativeDatetime(value, clock); err == nil {
			t.Fatal("invalid duration accepted", value)
		}
	}
	if _, err := ResolveRelativeDatetime("+1ms", time.UnixMilli(MaxRelativeTimeMilliseconds)); err == nil {
		t.Fatal("out of range resolved time accepted")
	}
}

func TestMetricCloneBoxAndSidecarIndependence(t *testing.T) {
	y := any(float64(1))
	errors := int64(2)
	original := SQLiteMetricV2{Buckets: []SQLiteMetricV2Bucket{{Keys: []any{"a"}, Y: &y, InputErrorCount: &errors, Distribution: &MetricBoxDistribution{Summary: &MetricBoxSummary{Outliers: []float64{7}}}}}}
	copy := cloneSQLiteMetric(original)
	*copy.Buckets[0].Y = float64(9)
	*copy.Buckets[0].InputErrorCount = 8
	copy.Buckets[0].Distribution.(*MetricBoxDistribution).Summary.Outliers[0] = 10
	if *original.Buckets[0].Y != float64(1) || *original.Buckets[0].InputErrorCount != 2 || original.Buckets[0].Distribution.(*MetricBoxDistribution).Summary.Outliers[0] != 7 {
		t.Fatal("sibling metric mutated")
	}
}

func TestSQLiteReuseParameterKeys(t *testing.T) {
	for _, sql := range []string{
		"SELECT ?1 AS x /* ' ?99 */",
		"SELECT ?1 AS x -- ' ?99\n",
		"SELECT ?1 AS [x?99]",
		"SELECT ?1 AS `x?99`",
		`SELECT ?1 AS "x?99"`,
		"SELECT ?1 AS x, '?99''?' AS literal",
	} {
		p := MetricSQLPlan{SQLPlan: SQLPlan{Stages: []SQLStage{{Name: "qt0", SQL: sql}, {Name: "qt1", SQL: "SELECT x FROM qt0"}}, Args: []any{1}, SQLiteReductionStage: 1}}
		first := sqliteReductionKey(p)
		p.Args = []any{2}
		if first == "" || first == sqliteReductionKey(p) {
			t.Fatalf("numbered values omitted from key: %s", sql)
		}
	}
}
