package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func metricTotal(t *testing.T, metric qt.SQLiteMetricV2) float64 {
	t.Helper()
	total := 0.0
	for _, bucket := range metric.Buckets {
		total += bucket.Value.(float64)
	}
	return total
}

// Query Table 0.8 metric populations: each metric's own WHERE and minCount
// run through every Pharos host path, and one failing metric leaves its
// siblings intact.
func TestQueryMetricPopulationsHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 300)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
		r.Header.Set("Authorization", "Bearer "+config.APIToken)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	w := call(http.MethodGet, "/api/query/tool_calls/capabilities", nil)
	var handshake struct {
		MetricCapabilities qt.SQLiteMetricCapabilities `json:"metricCapabilities"`
		QueryCapabilities  qt.QueryCapabilitiesV3      `json:"queryCapabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &handshake); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if !handshake.MetricCapabilities.Filters || !handshake.MetricCapabilities.MinCount || !handshake.QueryCapabilities.MetricFilters || !handshake.QueryCapabilities.MetricMinCount {
		t.Fatalf("population controls are not advertised: %+v %+v", handshake.MetricCapabilities, handshake.QueryCapabilities)
	}

	var expected struct{ total, errors int }
	if err := catalog.DB.QueryRow(`SELECT COUNT(*),SUM(status='error') FROM tool_calls`).Scan(&expected.total, &expected.errors); err != nil {
		t.Fatal(err)
	}
	errors := []qt.WhereTerm{{Field: "status", Op: "=", Value: "error"}}
	// Plain COUNT metrics take the count-only fast path; a metric WHERE must leave it.
	q := metricRequest(qt.AggSpec{ID: "all", Expression: "COUNT()"}, qt.AggSpec{ID: "errors", Expression: "COUNT()", Where: errors})
	w = call(http.MethodPost, "/api/query/tool_calls/metrics", q)
	var result qt.SQLiteMetricsV2Result
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if got := []float64{metricTotal(t, result.Metrics[0]), metricTotal(t, result.Metrics[1])}; got[0] != float64(expected.total) || got[1] != float64(expected.errors) || expected.errors == 0 {
		t.Fatalf("counts=%v, want [%d %d]", got, expected.total, expected.errors)
	}

	// minCount hides small groups and reports them; an invalid sibling fails alone.
	q = metricRequest(
		qt.AggSpec{ID: "programs", Expression: "COUNT()", GroupBy: []string{"program"}, MinCount: qt.MetricMinCount{Min: 70}},
		qt.AggSpec{ID: "invalid", Expression: "COUNT()", MinCount: qt.MetricMinCount{Min: 2}},
		qt.AggSpec{ID: "error_kinds", Expression: "COUNT()", GroupBy: []string{"error_type"}, Where: errors},
	)
	w = call(http.MethodPost, "/api/query/tool_calls/metrics", q)
	result = qt.SQLiteMetricsV2Result{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	programs := result.Metrics[0]
	if programs.HiddenGroups == nil || *programs.HiddenGroups == 0 {
		t.Fatalf("hiddenGroups=%v", programs.HiddenGroups)
	}
	for _, bucket := range programs.Buckets {
		if bucket.Count < 70 {
			t.Fatalf("bucket %v kept below minCount", bucket.Keys)
		}
	}
	if result.Metrics[1].Error == "" || len(result.Metrics[1].Buckets) != 0 {
		t.Fatalf("invalid metric=%+v", result.Metrics[1])
	}
	if got := metricTotal(t, result.Metrics[2]); got != float64(expected.errors) {
		t.Fatalf("error kinds=%v, want %d", got, expected.errors)
	}

	// The dashboards' minimums over summed fields and distinct values: Tools
	// counts calls over its daily rollup, and a records view counts a field's
	// distinct values (Tokens counts conversations). Each hides exactly the groups whose measure is short of
	// the minimum, and says how many.
	for _, c := range []struct {
		dataset string
		measure qt.AggSpec
		minimum qt.MetricMinCount
	}{
		{"tools", qt.AggSpec{Op: "sum", Field: "call_count"}, qt.MetricMinCount{Sum: "call_count"}},
		{"tool_calls", qt.AggSpec{Op: "count_distinct", Field: "ended_at"}, qt.MetricMinCount{Distinct: "ended_at"}},
	} {
		run := func(spec qt.AggSpec) qt.SQLiteMetricV2 {
			t.Helper()
			spec.ID, spec.GroupBy = "m", []string{"tool_name"}
			w := call(http.MethodPost, "/api/query/"+c.dataset+"/metrics", metricRequest(spec))
			var result qt.SQLiteMetricsV2Result
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Metrics[0].Error != "" {
				t.Fatalf("%s: %d %s", c.dataset, w.Code, w.Body.String())
			}
			return result.Metrics[0]
		}
		measures := map[string]float64{}
		values := []float64{}
		for _, bucket := range run(c.measure).Buckets {
			measures[fmt.Sprint(bucket.Keys[0])] = bucket.Value.(float64)
			values = append(values, bucket.Value.(float64))
		}
		slices.Sort(values)
		if len(values) < 3 || values[0] == values[len(values)-1] {
			t.Fatalf("%s: the fixture needs groups of different sizes: %v", c.dataset, measures)
		}
		c.minimum.Min = int(values[len(values)/2])
		kept := run(qt.AggSpec{Op: "count", MinCount: c.minimum})
		want := []string{}
		for key, value := range measures {
			if value >= float64(c.minimum.Min) {
				want = append(want, key)
			}
		}
		got := []string{}
		for _, bucket := range kept.Buckets {
			got = append(got, fmt.Sprint(bucket.Keys[0]))
		}
		slices.Sort(want)
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) || kept.HiddenGroups == nil || int(*kept.HiddenGroups) != len(measures)-len(want) {
			t.Fatalf("%s %+v: kept %v (hidden %v), want %v of %v", c.dataset, c.minimum, got, kept.HiddenGroups, want, measures)
		}
	}

	// V3 calendar metrics carry the same controls on every dataset path.
	v3 := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(),
		Result: qt.WireQuery{Limit: 1},
		Metrics: []qt.AggSpec{{ID: "daily_errors", Op: "count", GroupBy: []string{"started_at"}, Where: errors,
			DateGrouping: &qt.MetricDateGrouping{Field: "started_at", Interval: "day", TimeZone: "UTC", Range: "observed"}}}}
	w = call(http.MethodPost, "/api/query/tool_calls/query-v3", v3)
	var grouped qt.QueryResultV3
	if err := json.Unmarshal(w.Body.Bytes(), &grouped); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if len(grouped.Metrics) != 1 || metricTotal(t, grouped.Metrics[0]) != float64(expected.errors) {
		t.Fatalf("v3 metrics=%+v", grouped.Metrics)
	}
}

// Map-backed datasets stage only the fields a request reads, so a metric's own
// WHERE must be part of discovery. A metric that cannot compile does not hide
// its siblings' inputs.
func TestQueryMetricPopulationDependencies(t *testing.T) {
	catalog, _ := libraryFixture(t)
	schema, _ := sqliteQuerySchema("library")
	q := metricRequest(
		qt.AggSpec{ID: "by_kind", Expression: "SUM([token_count])", GroupBy: []string{"source_kind"}, MinCount: qt.MetricMinCount{Min: 2},
			Where: []qt.WhereTerm{{Field: "purpose", Op: "contains", Value: "purpose"}}},
		qt.AggSpec{ID: "invalid", Expression: "SUM([tool_use_count])", MinCount: qt.MetricMinCount{Min: 2}},
	)
	fields, err := queryV2SourceFields(context.Background(), catalog.DB, schema, nil, &q, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"id", "purpose", "source_kind", "token_count"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields=%v, want %v", fields, want)
	}
}

// Usage metrics choose hourly populations from everything they read, including
// their own WHERE. An invalid metric fails alone here too.
func TestQueryMetricPopulationUsageHour(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	server := testServer(t, config, catalog)
	schema, _ := sqliteQuerySchema("usage")
	hour := []qt.WhereTerm{{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}}
	// Tokens' records minimum counts the conversations a group's usage
	// belongs to.
	conversations := func(min int) qt.AggSpec {
		return qt.AggSpec{ID: fmt.Sprint("conversations-", min), Op: "sum", Field: "total_tokens", GroupBy: []string{"provider"}, MinCount: qt.MetricMinCount{Min: min, Distinct: "conversation_id"}}
	}
	q := metricRequest(qt.AggSpec{ID: "all", Expression: "SUM([total_tokens])"}, qt.AggSpec{ID: "hour", Expression: "SUM([total_tokens])", Where: hour},
		qt.AggSpec{ID: "invalid", Expression: "SUM([total_tokens])", MinCount: qt.MetricMinCount{Min: 2}}, conversations(1), conversations(1000))
	result, err := server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &q)
	if err != nil {
		t.Fatal(err)
	}
	// A table WHERE on hour selects whole days (2130); a metric WHERE reads the hour itself.
	if all, inHour := metricTotal(t, result.Metrics.Metrics[0]), metricTotal(t, result.Metrics.Metrics[1]); all != 3150 || inHour != 100 {
		t.Fatalf("all=%v hour=%v, want 3150 and 100", all, inHour)
	}
	if result.Metrics.Metrics[2].Error == "" {
		t.Fatalf("invalid metric=%+v", result.Metrics.Metrics[2])
	}
	kept, none := result.Metrics.Metrics[3], result.Metrics.Metrics[4]
	if kept.Error != "" || none.Error != "" || metricTotal(t, kept) != 3150 || len(none.Buckets) != 0 || none.HiddenGroups == nil || int(*none.HiddenGroups) != len(kept.Buckets) {
		t.Fatalf("conversation minimums: kept %+v, none %+v", kept, none)
	}
}
