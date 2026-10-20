package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func queryV2Fixture(t testing.TB, n int) (*sql.DB, qt.SQLiteV2Dataset) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "metrics.db")+"?_pragma=journal_mode(WAL)&_pragma=temp_store(MEMORY)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(4)
	for _, statement := range []string{qt.SQLiteComputedColumnsDDL, `CREATE TABLE samples(id TEXT PRIMARY KEY,category TEXT,other TEXT,x REAL,y REAL,at TEXT,tags TEXT,flag INTEGER) STRICT`, `CREATE INDEX samples_at ON samples(at,id)`} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM seq WHERE n+1<?)
 INSERT INTO samples SELECT printf('%08d',n),printf('g%d',n%10),printf('h%d',n%3),CASE WHEN n%17=0 THEN NULL ELSE n%1000 END,n%100+1,'2026-10-08T00:00:00.000Z','["Bug","École"]',n%2 FROM seq`, n)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := qt.LoadSQLiteSchema([]byte(`{"name":"samples","idField":"id","fields":[
 {"name":"id","label":"ID","type":"text","bindings":{"sqlite":{"expr":"r.id"}}},
 {"name":"category","label":"Category","type":"text","bindings":{"sqlite":{"expr":"r.category"}}},
 {"name":"other","label":"Other","type":"text","bindings":{"sqlite":{"expr":"r.other"}}},
 {"name":"x","label":"X","type":"number","bindings":{"sqlite":{"expr":"r.x","expressionNumeric":true}},"aggregate":{"groupable":true}},
 {"name":"y","label":"Y","type":"number","bindings":{"sqlite":{"expr":"r.y","expressionNumeric":true}}},
 {"name":"fraction","label":"Fraction","type":"number","bindings":{"sqlite":{"expr":"r.y/1000.0","expressionNumeric":true}}},
 {"name":"at","label":"At","type":"datetime","bindings":{"sqlite":{"expr":"r.at","datetimeFormat":"utc-millis"}}},
 {"name":"tags","label":"Tags","type":"textarray","bindings":{"sqlite":{"expr":"r.tags"}}},
 {"name":"flag","label":"Flag","type":"bool","bindings":{"sqlite":{"expr":"r.flag"}}}
 ]}`))
	if err != nil {
		t.Fatal(err)
	}
	return db, qt.SQLiteV2Dataset{Schema: schema, SourceSQL: "SELECT * FROM samples", Scope: "local", Dataset: "samples", MaxPopulation: queryTablePopulation}
}
func metricRequest(specs ...qt.AggSpec) qt.MetricQuery {
	return qt.MetricQuery{Version: 2, Profile: qt.SQLiteExpressionProfile, Limit: 50, Metrics: specs}
}

func TestGeneratedQuerySchemas(t *testing.T) {
	for _, dataset := range []string{"library", "activity", "usage", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts", "findings", "parallelism"} {
		t.Run(dataset, func(t *testing.T) {
			doc, err := querySchemaDocument(dataset)
			if err != nil {
				t.Fatal(err)
			}
			want, err := qt.LoadSQLiteSchema(doc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := sqliteQuerySchema(dataset)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("generated schema differs: %v", err)
			}
		})
	}
}

func BenchmarkQueryTableV2(b *testing.B) {
	for _, n := range []int{10_000, 100_000, 250_000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			db, d := queryV2Fixture(b, n)
			cases := map[string]qt.MetricQuery{}
			for _, formula := range []string{"COUNT()", "SUM([x])", "AVG([x])", "MEDIAN([x])", "SUM([x])/NULLIF(SUM([y]),0)"} {
				cases[formula] = metricRequest(qt.AggSpec{ID: "m", Expression: formula, GroupBy: []string{"category"}})
			}
			for _, kind := range []string{"box", "histogram"} {
				cases[kind] = metricRequest(qt.AggSpec{ID: "m", Distribution: &qt.MetricDistribution{Kind: kind, Input: "[x]", Bins: 10, Whiskers: "tukey"}, GroupBy: []string{"category"}})
			}
			many := []qt.AggSpec{}
			for i := range 20 {
				many = append(many, qt.AggSpec{ID: fmt.Sprint(i), Expression: "SUM([x])/COUNT()", GroupBy: []string{"category", "other"}})
			}
			cases["20_metrics"] = metricRequest(many...)
			varied := []qt.AggSpec{}
			for i := range 20 {
				varied = append(varied, qt.AggSpec{ID: fmt.Sprint("varied", i), Expression: fmt.Sprintf("SUM([x])+%d", i), GroupBy: []string{"category", "other"}})
			}
			cases["20_distinct_metrics"] = metricRequest(varied...)
			mixed := []qt.AggSpec{}
			for i, formula := range []string{"COUNT()", "COUNT([x])", "COUNT_DISTINCT([x])", "COUNT_DISTINCT([category])", "SUM([x])", "SUM([y])", "AVG([x])", "AVG([y])", "MIN([x])", "MAX([x])", "MIN([y])", "MAX([y])", "MEDIAN([x])", "MEDIAN([y])", "SUM([x])/NULLIF(SUM([y]),0)", "AVG(IF([flag],[x],NULL))", "SUM([x])/COUNT()", "SUM([y])/COUNT()"} {
				mixed = append(mixed, qt.AggSpec{ID: fmt.Sprint("mixed", i), Expression: formula, GroupBy: []string{"category", "other"}})
			}
			mixed = append(mixed, qt.AggSpec{ID: "mixed_box", Distribution: &qt.MetricDistribution{Kind: "box", Input: "[x]", Whiskers: "tukey"}, GroupBy: []string{"category"}}, qt.AggSpec{ID: "mixed_histogram", Distribution: &qt.MetricDistribution{Kind: "histogram", Input: "[y]", Bins: 20}, GroupBy: []string{"category"}})
			cases["20_mixed_metrics"] = metricRequest(mixed...)
			cases["fractional_sum"] = metricRequest(qt.AggSpec{ID: "fraction", Expression: "SUM([fraction])", GroupBy: []string{"category"}})
			shown := metricRequest(qt.AggSpec{ID: "m", Expression: "SUM([x])", Scope: "shownRows"})
			shown.OrderBy = []qt.OrderBy{{Field: "at", Dir: "desc"}}
			cases["shown_indexed"] = shown
			for name, q := range cases {
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ctx, cancel := context.WithTimeout(context.Background(), queryTableDeadline)
						_, err := d.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{})
						cancel()
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

// Usage keeps daily rows, and selects each request hour for metrics that read
// hour: a filter on hour picks the days holding those hours, and a metric
// grouped by hour counts hours, not days.
func TestQueryV2UsageHourly(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	rows, err := catalog.usageRows(time.UTC)
	if err != nil || len(rows) != 2 {
		t.Fatalf("daily table changed: %v %#v", err, rows)
	}
	hourly, err := catalog.usageRowsByPeriod(time.UTC, true)
	if err != nil || len(hourly) != 3 {
		t.Fatalf("expected three request hours: %v %#v", err, hourly)
	}
	var tokens int64
	for _, row := range hourly {
		tokens += integer(row["total_tokens"])
	}
	if tokens != 3150 {
		t.Fatalf("hourly tokens = %d", tokens)
	}
	server := testServer(t, config, catalog)
	schema, _ := sqliteQuerySchema("usage")
	for _, sample := range []struct {
		name                      string
		where                     []qt.WhereTerm
		days, hours, average, sum float64
		last                      string
	}{
		{"unfiltered", nil, 2, 3, 1575, 3150, "2026-09-21T03:00:00Z"},
		{"daily threshold", []qt.WhereTerm{{Field: "total_tokens", Op: ">", Value: "1500"}}, 1, 2, 2130, 2130, "2026-09-21T03:00:00Z"},
		{"timestamp", []qt.WhereTerm{{Field: "first_usage_at", Op: "<", Value: "2026-09-21T02:00:00Z"}}, 2, 2, 1575, 3150, "2026-09-21T01:00:00Z"},
		{"hour filter", []qt.WhereTerm{{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}}, 1, 1, 2130, 2130, "2026-09-21T03:00:00Z"},
		{"mixed hour OR", []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}, {Field: "total_tokens", Op: ">", Value: "2000"}}}}, 1, 2, 2130, 2130, "2026-09-21T03:00:00Z"},
		// Hours and days are filtered alike: a NULL repository contains no text.
		{"repository OR threshold", []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "repository_name", Op: "contains", Value: "nil"}, {Field: "total_tokens", Op: ">", Value: "2000"}}}}, 1, 2, 2130, 2130, "2026-09-21T03:00:00Z"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			q := metricRequest(qt.AggSpec{ID: "days", Op: "count", GroupBy: []string{"model"}}, qt.AggSpec{ID: "hours", Expression: "COUNT()", GroupBy: []string{"hour"}},
				qt.AggSpec{ID: "average", Op: "avg", Field: "total_tokens"}, qt.AggSpec{ID: "sum", Expression: "SUM([total_tokens])"}, qt.AggSpec{ID: "last", Op: "max", Field: "hour"},
				qt.AggSpec{ID: "newest", Op: "sum", Field: "total_tokens", GroupBy: []string{"hour"}, Sort: []qt.MetricSort{{Key: "group0", Dir: "desc"}}})
			q.Where = sample.where
			result, err := server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &q)
			if err != nil {
				t.Fatal(err)
			}
			metrics := result.Metrics.Metrics
			for i, want := range []float64{sample.days, sample.hours, sample.average, sample.sum} {
				var got float64
				for _, bucket := range metrics[i].Buckets {
					got += bucket.Value.(float64)
					// Daily metrics count days, whatever their hours.
					if i != 1 && bucket.Count != int64(sample.days) {
						t.Fatalf("%s counted hours: %#v", metrics[i].ID, bucket)
					}
				}
				if metrics[i].Error != "" || got != want {
					t.Fatalf("%s=%v want %v (%s)", metrics[i].ID, got, want, metrics[i].Error)
				}
			}
			if len(metrics[4].Buckets) != 1 || metrics[4].Buckets[0].Value != sample.last {
				t.Fatalf("latest hour: %#v", metrics[4])
			}
			newest := metrics[5].Buckets
			if len(newest) != int(sample.hours) || newest[0].Keys[0] != sample.last {
				t.Fatalf("hours newest first: %#v", newest)
			}
		})
	}
	// Usage has its own group budget, beyond other datasets'; past it a metric
	// fails rather than dropping groups.
	override(t, &queryV2MaxGroups, 1)
	hours := metricRequest(qt.AggSpec{ID: "hours", Expression: "COUNT()", GroupBy: []string{"hour"}})
	result, err := server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &hours)
	if err != nil || result.Metrics.Metrics[0].Error != "" || len(result.Metrics.Metrics[0].Buckets) != 3 {
		t.Fatal("usage group budget", err, result.Metrics)
	}
	override(t, &queryV2UsageMaxGroups, 2)
	if result, err = server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &hours); err != nil || !strings.Contains(result.Metrics.Metrics[0].Error, "group population exceeds host budget") {
		t.Fatal("usage groups beyond the budget", err, result.Metrics)
	}
	// Temp staging, enrichment and canonical-definition reads must not consume a
	// second connection while holding the sole connection in a transaction.
	catalog.DB.SetMaxOpenConns(1)
	q := metricRequest(qt.AggSpec{ID: "hours", Expression: "COUNT()", GroupBy: []string{"hour"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := server.executeQueryV2(ctx, "usage", nil, schema, nil, &q); err != nil {
		t.Fatal(err)
	}
}

func TestQueryV2IndexedCandidates(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 150)
	for i, local := range []string{"2026-03-07T23:59:00", "2026-03-08T00:00:00", "2026-03-08T23:59:00", "2026-03-09T00:00:00", "2026-10-31T23:59:00", "2026-11-01T00:00:00", "2026-11-01T23:59:00", "2026-11-02T00:00:00"} {
		if _, err := catalog.DB.Exec(`UPDATE tool_calls SET started_at=strftime('%Y-%m-%dT%H:%M:%fZ',?,'utc') WHERE id=?`, local, fmt.Sprint("call-", i)); err != nil {
			t.Fatal(err)
		}
	}
	server := testServer(t, config, catalog)
	schema, _ := sqliteQuerySchema("tool_calls")
	for _, where := range [][]qt.WhereTerm{
		{{Field: "command", Op: "contains", Value: "TEST ./"}},
		{{Field: "command", Op: "matches_regex", Value: `go.*test`}},
		{{Any: []qt.WhereClause{{Field: "program", Op: "contains", Value: "git"}, {Field: "command", Op: "contains", Value: "go test"}}}},
		{{Any: []qt.WhereClause{{Field: "command", Op: "contains", Value: "go test"}, {Field: "status", Op: "=", Value: "error"}}}},
		{{Field: "command", Op: "contains", Value: "go test", Negated: true}},
		{{Field: "day", Op: "=", Value: "2026-09-01"}},
		{{Field: "day", Op: "=", Value: "2026-03-08"}},
		{{Field: "day", Op: "=", Value: "2026-11-01"}},
		{{Field: "week", Op: "=", Value: "2026-08-31"}},
		{{Field: "month", Op: "=", Value: "2026-09"}},
	} {
		q := metricRequest(qt.AggSpec{ID: "count", Expression: "COUNT()"})
		q.Where = where
		got, err := server.executeQueryV2(context.Background(), "tool_calls", nil, schema, nil, &q)
		if err != nil {
			t.Fatal(err)
		}
		d := queryV2Dataset("tool_calls", schema)
		d.SourceSQL = toolCallDataset.v2Source(schema)
		want, err := d.ExecuteV2(context.Background(), catalog.DB, nil, &q, qt.PlanOptions{})
		if err != nil || !same(got.SQLiteV2ExecutionResult, want) {
			t.Fatalf("candidate result differs: %v %v", where, err)
		}
	}
}

func TestQueryV2IndexedWindow(t *testing.T) {
	db, d := queryV2Fixture(t, 100)
	q := metricRequest(qt.AggSpec{ID: "shown", Expression: "SUM([x])", Scope: "shownRows"})
	q.OrderBy = []qt.OrderBy{{Field: "at", Dir: "desc"}}
	plan, err := qt.CompileSQLiteMetrics(context.Background(), q, d.Schema, qt.PlanOptions{SourceSQL: d.SourceSQL})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryMapsContext(context.Background(), db, "EXPLAIN QUERY PLAN "+plan.Metrics[0].SQL, plan.Metrics[0].Args...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(rows), "samples_at") {
		t.Fatalf("timestamp index missing: %v", rows)
	}
	if len(plan.Metrics[0].Stages) > 0 && plan.Metrics[0].Stages[0].Materialized {
		t.Fatal("source unexpectedly materialized")
	}
}

var queryV2ProbeCalls atomic.Int64

func init() {
	if err := sqlite.RegisterDeterministicScalarFunction("pharos_metric_probe", 1, func(_ *sqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
		queryV2ProbeCalls.Add(1)
		return values[0], nil
	}); err != nil {
		panic(err)
	}
}
func TestQueryV2ReductionReuse(t *testing.T) {
	db, d := queryV2Fixture(t, 1000)
	d.SourceSQL = "SELECT *, pharos_metric_probe(x) AS sample_x FROM samples"
	f := d.Schema.Fields["x"]
	f.Expr = "r.sample_x"
	d.Schema.Fields["x"] = f
	specs := []qt.AggSpec{
		{ID: "sum", Expression: "SUM([x])", GroupBy: []string{"category"}},
		{ID: "same", Expression: "SUM([x])", GroupBy: []string{"category"}},
		{ID: "plus", Expression: "SUM([x])+2", GroupBy: []string{"category"}},
		{ID: "top", Expression: "SUM([x])+5", GroupBy: []string{"category"}, Sort: []qt.MetricSort{{Key: "value", Dir: "desc"}}, GroupLimit: 3},
		{ID: "shown", Expression: "SUM([x])+2", Scope: "shownRows", GroupBy: []string{"category"}},
		{ID: "avg", Expression: "AVG([x])", GroupBy: []string{"category"}},
		{ID: "min", Expression: "MIN([x])", GroupBy: []string{"category"}},
	}
	q := metricRequest(specs...)
	q.Where = []qt.WhereTerm{{Field: "other", Op: "=", Value: "h1"}}
	q.Offset = 2
	q.Limit = 7
	want := []qt.SQLiteMetricV2{}
	singleCalls := int64(0)
	for i, spec := range specs {
		single := q
		single.Metrics = []qt.AggSpec{spec}
		queryV2ProbeCalls.Store(0)
		result, err := d.ExecuteV2(context.Background(), db, nil, &single, qt.PlanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, result.Metrics.Metrics[0])
		if i == 0 {
			singleCalls = queryV2ProbeCalls.Load()
		}
	}
	// Independently verify an identical batch executes the population once.
	repeated := q
	repeated.Metrics = specs[:2]
	queryV2ProbeCalls.Store(0)
	if _, err := d.ExecuteV2(context.Background(), db, nil, &repeated, qt.PlanOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := queryV2ProbeCalls.Load(); n != singleCalls || n == 0 {
		t.Fatalf("duplicate population visits=%d want %d", n, singleCalls)
	}
	queryV2ProbeCalls.Store(0)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := d.ExecuteV2In(context.Background(), tx, nil, &q, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !same(result.Metrics.Metrics, want) {
		t.Fatalf("shared reductions differ: %+v want %+v", result.Metrics.Metrics, want)
	}
	if n := queryV2ProbeCalls.Load(); n >= singleCalls*3 {
		t.Fatalf("shared reduction repeated population: %d (single %d)", n, singleCalls)
	}
	var temp int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_temp_master WHERE name LIKE '_qt_reduction_%'").Scan(&temp); err != nil || temp != 0 {
		t.Fatal("temporary reductions leaked", temp, err)
	}
	// An invalid metric fails alone; its siblings keep their shared results.
	q.Metrics = append(q.Metrics, qt.AggSpec{ID: "bad", Expression: "SUM([missing])"})
	result, err = d.ExecuteV2In(context.Background(), tx, nil, &q, qt.PlanOptions{})
	if err != nil || len(result.Metrics.Metrics) != len(want)+1 {
		t.Fatal("invalid metric failed its batch", err)
	}
	if bad := result.Metrics.Metrics[len(want)]; bad.ID != "bad" || bad.Error == "" || len(bad.Buckets) != 0 {
		t.Fatalf("invalid metric result: %+v", bad)
	}
	if !same(result.Metrics.Metrics[:len(want)], want) {
		t.Fatalf("siblings of an invalid metric differ: %+v want %+v", result.Metrics.Metrics, want)
	}
}

func TestQueryV2UsageShownHours(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	server := testServer(t, config, catalog)
	schema, _ := sqliteQuerySchema("usage")
	q := metricRequest(qt.AggSpec{ID: "shownHours", Expression: "COUNT()", GroupBy: []string{"hour"}, Scope: "shownRows"})
	q.Limit = 1
	q.OrderBy = []qt.OrderBy{{Field: "total_tokens", Dir: "desc"}}
	result, err := server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &q)
	if err != nil {
		t.Fatal(err)
	}
	if m := result.Metrics.Metrics[0]; m.Scope != "shownRows" || m.ProcessedRows != 2 || len(m.Buckets) != 2 {
		t.Fatalf("daily window should expand into two hours: %+v", m)
	}
	q.Where = []qt.WhereTerm{{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}}
	rows := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Where: q.Where, Select: []string{"total_tokens"}, Limit: 1}}
	result, err = server.executeQueryV2(context.Background(), "usage", nil, schema, &rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows.Total != 1 || integer(result.Rows.Rows[0]["total_tokens"]) != 2130 {
		t.Fatal("hour filter changed daily rows", result.Rows)
	}
	result, err = server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &q)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metrics.Metrics[0].ProcessedRows != 1 {
		t.Fatal("shown hour filter", result.Metrics)
	}
}

func BenchmarkPharosToolLedgerV2(b *testing.B) {
	for _, n := range []int{10_000, 100_000, 250_000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			catalog, config := testCatalog(b)
			seedToolCalls(b, catalog, n)
			server := testServer(b, config, catalog)
			schema, _ := sqliteQuerySchema("tool_calls")
			if err := catalog.prepareSQLDataset(context.Background(), "tool_calls"); err != nil {
				b.Fatal(err)
			}
			cases := map[string]qt.MetricQuery{}
			cases["paired"] = metricRequest(qt.AggSpec{ID: "m", Expression: "SUM([duration_ms])/NULLIF(COUNT([duration_ms]),0)", ExpressionY: "SUM([result_bytes])", GroupBy: []string{"tool_name", "provider"}})
			cases["histogram"] = metricRequest(qt.AggSpec{ID: "m", Distribution: &qt.MetricDistribution{Kind: "histogram", Input: "[duration_ms]", Bins: 20}, GroupBy: []string{"tool_name"}})
			cases["command_trigram"] = metricRequest(qt.AggSpec{ID: "m", Expression: "COUNT()"})
			filtered := cases["command_trigram"]
			filtered.Where = []qt.WhereTerm{{Field: "command", Op: "contains", Value: "go test"}}
			cases["command_trigram"] = filtered
			cases["day_index"] = metricRequest(qt.AggSpec{ID: "m", Expression: "COUNT()"})
			filtered = cases["day_index"]
			filtered.Where = []qt.WhereTerm{{Field: "day", Op: "=", Value: "2026-09-01"}}
			cases["day_index"] = filtered
			cases["shown_indexed"] = metricRequest(qt.AggSpec{ID: "m", Expression: "SUM([duration_ms])", Scope: "shownRows"})
			filtered = cases["shown_indexed"]
			filtered.OrderBy = []qt.OrderBy{{Field: "started_at", Dir: "desc"}}
			cases["shown_indexed"] = filtered
			many := []qt.AggSpec{}
			for i := range 20 {
				many = append(many, qt.AggSpec{ID: fmt.Sprint(i), Expression: fmt.Sprintf("SUM([duration_ms])+%d", i), GroupBy: []string{"tool_name", "provider"}})
			}
			cases["20_distinct"] = metricRequest(many...)
			for name, q := range cases {
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ctx, cancel := context.WithTimeout(context.Background(), queryTableDeadline)
						_, err := server.executeQueryV2(ctx, "tool_calls", nil, schema, nil, &q)
						cancel()
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

// TestQueryTempStorageLease: each engine's transaction sets the temp store
// its queries need, and on release restores the connection's own (also
// after cancellation, and when released twice) and leaves no scratch tables.
func TestQueryTempStorageLease(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.DB.SetMaxOpenConns(1)
	for _, engine := range []struct {
		name  string
		begin func(context.Context, *sql.DB) (*sql.Tx, func() error, error)
		mode  int    // what the engine's queries need
		prior string // the connection's own setting
	}{
		{"V2 stages in memory", beginQueryV2, 2, "DEFAULT"},
		{"large V3 copies spill to disk", beginQueryV3, 1, "MEMORY"},
	} {
		t.Run(engine.name, func(t *testing.T) {
			if _, err := catalog.DB.Exec("PRAGMA temp_store=" + engine.prior); err != nil {
				t.Fatal(err)
			}
			var original, mode int
			if err := catalog.DB.QueryRow("PRAGMA temp_store").Scan(&original); err != nil {
				t.Fatal(err)
			}
			tx, release, err := engine.begin(context.Background(), catalog.DB)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow("PRAGMA temp_store").Scan(&mode); err != nil || mode != engine.mode {
				t.Fatalf("temp_store=%d during the query, want %d: %v", mode, engine.mode, err)
			}
			if _, err := tx.Exec("CREATE TEMP TABLE lease_probe(n)"); err != nil {
				t.Fatal(err)
			}
			if err := release(); err != nil {
				t.Fatal(err)
			}
			if err := release(); err != nil {
				t.Fatal("second release:", err)
			}
			if err := catalog.DB.QueryRow("PRAGMA temp_store").Scan(&mode); err != nil || mode != original {
				t.Fatal("connection settings leaked", mode, original, err)
			}
			var tables int
			if err := catalog.DB.QueryRow("SELECT count(*) FROM sqlite_temp_master").Scan(&tables); err != nil || tables != 0 {
				t.Fatal("staging leaked", tables, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			_, release, err = engine.begin(ctx, catalog.DB)
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			_ = release()
			if err := catalog.DB.QueryRow("PRAGMA temp_store").Scan(&mode); err != nil || mode != original {
				t.Fatal("cancelled connection leaked", mode, original, err)
			}
		})
	}
}

// Metrics answered without staging read in a plain transaction, on one pooled
// connection; anything the adapter executes still leases memory-backed
// temporary storage.
func TestQueryV2LeasesTempStorageOnlyToStage(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 30)
	ingestUsageFixture(t, catalog)
	if err := catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	pool := catalog.reader()
	previous := pool.Stats().MaxOpenConnections
	t.Cleanup(func() { pool.SetMaxOpenConns(previous) })
	leases := 0
	override(t, &leaseQueryV2, func(ctx context.Context, db *sql.DB) (*sql.Tx, func() error, error) {
		leases++
		tx, release, err := beginQueryV2(ctx, db)
		var mode int
		if err == nil && (tx.QueryRow("PRAGMA temp_store").Scan(&mode) != nil || mode != 2) {
			t.Errorf("staged work without memory-backed temporary storage: temp_store=%d", mode)
		}
		return tx, release, err
	})
	machine := []string{"repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}
	for _, sample := range []struct {
		name, dataset string
		q             qt.MetricQuery
		staged        bool
	}{
		{"a bare count", "tool_calls", metricRequest(qt.AggSpec{ID: "n", Op: "count"}), false},
		{"a Machine Time cube", "tool_calls", metricRequest(qt.AggSpec{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: machine}), false},
		{"native tool sums", "tool_calls", metricRequest(qt.AggSpec{ID: "calls", Op: "sum", Field: "call_count", GroupBy: []string{"tool_name"}}), false},
		{"Usage in memory", "usage", metricRequest(qt.AggSpec{ID: "tokens", Op: "sum", Field: "total_tokens", GroupBy: []string{"week"}}, qt.AggSpec{ID: "sessions", Op: "count_distinct", Field: "agent_session_id"}), false},
		{"an average", "tool_calls", metricRequest(qt.AggSpec{ID: "mean", Op: "avg", Field: "duration_ms"}), true},
		{"filtered Usage", "usage", func() qt.MetricQuery {
			q := metricRequest(qt.AggSpec{ID: "tokens", Op: "sum", Field: "total_tokens", GroupBy: []string{"week"}})
			q.Where = []qt.WhereTerm{{Field: "total_tokens", Op: ">", Value: "1"}}
			return q
		}(), false},
		{"a Usage formula", "usage", metricRequest(qt.AggSpec{ID: "per day", Expression: "SUM([total_tokens]) / COUNT()"}), true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			schema, _ := sqliteQuerySchema(sample.dataset)
			for _, connections := range []int{previous, 1} {
				// One pooled connection is enough: nothing reads beside the transaction.
				pool.SetMaxOpenConns(connections)
				leases = 0
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				result, err := server.executeQueryV2(ctx, sample.dataset, nil, schema, nil, &sample.q)
				cancel()
				if err != nil {
					t.Fatal(connections, err)
				}
				for _, metric := range result.Metrics.Metrics {
					if metric.Error != "" || len(metric.Buckets) == 0 {
						t.Fatalf("%d connections: %+v", connections, metric)
					}
				}
				if staged := leases > 0; staged != sample.staged {
					t.Fatalf("%d connections: leased temporary storage %d times", connections, leases)
				}
			}
			pool.SetMaxOpenConns(previous)
		})
	}
}

// TestQueryMetadataQueue: metadata scans (distinct values, field stats) wait
// in one queue. A scan's deadline starts when it leaves the queue, a
// cancelled request stops waiting, and foreground queries do not wait at all.
func TestQueryMetadataQueue(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 10)
	server := testServer(t, config, catalog)
	t.Run("deadline starts after the queue", func(t *testing.T) {
		_, release, err := server.beginQueryMetadata(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		type acquired struct {
			ctx     context.Context
			release func()
			err     error
		}
		next := make(chan acquired, 1)
		go func() {
			ctx, release, err := server.beginQueryMetadata(context.Background())
			next <- acquired{ctx, release, err}
		}()
		select {
		case result := <-next:
			release()
			if result.release != nil {
				result.release()
			}
			t.Fatal("metadata scan bypassed the queue")
		case <-time.After(30 * time.Millisecond):
		}
		release()
		result := <-next
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.release()
		deadline, ok := result.ctx.Deadline()
		if !ok || time.Until(deadline) < queryTableDeadline-time.Second {
			t.Fatalf("scan lost its execution budget: %v", deadline)
		}
		waiting, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err = server.beginQueryMetadata(waiting); err != context.Canceled {
			t.Fatalf("cancelled queued metadata: %v", err)
		}
	})
	t.Run("foreground queries do not wait", func(t *testing.T) {
		server.queryMetadata <- struct{}{}
		defer func() { <-server.queryMetadata }()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest(http.MethodGet, "/api/query/tool_calls/distinct?field=tool_name", nil).WithContext(ctx)
		request.Header.Set("Authorization", "Bearer "+config.APIToken)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusRequestTimeout {
			t.Fatalf("metadata wait ignored cancellation: %d", response.Code)
		}
		foreground := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/metrics", metricRequest(qt.AggSpec{ID: "calls", Expression: "COUNT()"}))
		if foreground.Code != http.StatusOK {
			t.Fatal(foreground.Code, foreground.Body.String())
		}
	})
}

func TestQueryIndexedDatetimeCandidatesPreserveTypedResults(t *testing.T) {
	db, dataset := queryV2Fixture(t, 0)
	clock := time.Date(2026, 11, 2, 8, 30, 0, 123000000, time.UTC)
	for i, at := range []string{
		"2024-01-01T00:00:00Z", "2026-10-26T08:30:00.122Z", "2026-10-26T08:30:00.123Z",
		"2026-10-26T08:30:00.124Z", "2026-11-01T01:30:00-06:00", "2026-11-01T01:30:00-07:00",
		"2026-11-02T08:30:00.123Z", "2026-11-03T00:00:00+14:00", "2028-01-01T00:00:00Z",
	} {
		if _, err := db.Exec("INSERT INTO samples(id,at,category) VALUES(?,?,?)", fmt.Sprint(i), at, "same"); err != nil {
			t.Fatal(err)
		}
	}
	// RFC3339 sources can retain explicit offsets; narrowing must remain safe.
	field := dataset.Schema.Fields["at"]
	field.SQLiteDatetimeFormat = "rfc3339"
	dataset.Schema.Fields["at"] = field
	columns := map[string]string{}
	for _, name := range []string{"id", "category", "other", "x", "y", "at", "tags", "flag"} {
		columns[name] = "s." + name
	}
	table := sqlDataset{from: "FROM samples s", columns: columns, timestamps: map[string]string{"at": "s.at"}}
	for _, where := range [][]qt.WhereTerm{
		{{Field: "at", Op: ">=", Value: "-7d"}, {Field: "at", Op: "<", Value: "+0s"}},
		{{Field: "at", Op: "=", Value: "-7d"}},
		{{Field: "at", Op: "<=", Value: "2026-11-01T01:30:00-07:00"}},
		{{Field: "at", Op: ">", Value: "2026-11-01T01:30:00-06:00"}},
		{{Any: []qt.WhereClause{{Field: "at", Op: ">=", Value: "-7d"}, {Field: "category", Op: "=", Value: "same"}}}},
		{{Field: "at", Op: ">=", Value: "-7d", Negated: true}},
	} {
		request := metricRequest(qt.AggSpec{ID: "count", Expression: "COUNT()"})
		request.Where = where
		plain := dataset
		plain.SourceSQL = table.v2Source(dataset.Schema)
		want, err := plain.ExecuteV2(context.Background(), db, nil, &request, qt.PlanOptions{Now: clock})
		if err != nil {
			t.Fatal(err)
		}
		indexed := dataset
		indexed.SourceSQL, indexed.SourceArgs = table.v2WindowedSource(dataset.Schema, where, clock)
		got, err := indexed.ExecuteV2(context.Background(), db, nil, &request, qt.PlanOptions{Now: clock})
		if err != nil || !same(got, want) {
			t.Fatalf("candidate changes typed results for %v: %v", where, err)
		}
	}
	source, args := table.v2WindowedSource(dataset.Schema, []qt.WhereTerm{{Field: "at", Op: ">=", Value: "-7d"}, {Field: "at", Op: "<", Value: "+0s"}}, clock)
	plan, err := queryMapsContext(context.Background(), db, "EXPLAIN QUERY PLAN SELECT count(*) FROM ("+source+")", args...)
	if err != nil || !strings.Contains(fmt.Sprint(plan), "samples_at") {
		t.Fatalf("timestamp index not used: %v %v", plan, err)
	}
}

func TestQueryV2ComputedCatalogueRecovery(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 3)
	server := testServer(t, config, catalog)
	store := qt.SQLiteComputedColumnStore{DB: catalog.DB}
	columns := map[string]qt.ComputedColumn{}
	// Metadata left by an older schema can no longer compile. Include two
	// unrelated failures so the UI must be able to repair them incrementally.
	for id, source := range map[string]string{"good": "[duration_ms]/1000", "good_child": "[@computed/good]*2", "broken": "[removed_field]", "broken_child": "[@computed/broken]*2", "other": "[another_removed_field]"} {
		column, err := store.Save(context.Background(), queryTableScope, "pharos_tool_calls", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: id, Label: id, Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: source}}})
		if err != nil {
			t.Fatal(err)
		}
		columns[id] = column
	}
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		return apiCall(t, server, config.APIToken, method, path, value)
	}
	capabilities := func() (qt.SQLiteComputedExecution, map[string]*qt.PlanDiagnostic) {
		t.Helper()
		w := call(http.MethodGet, "/api/query/tool_calls/capabilities", nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct {
			Execution   qt.SQLiteComputedExecution    `json:"computedExecution"`
			Unavailable map[string]*qt.PlanDiagnostic `json:"computedDiagnostics"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Execution, response.Unavailable
	}
	execution, unavailable := capabilities()
	if len(execution.Fields) != 2 || len(execution.ResolvedRevisions) != 2 || len(unavailable) != 3 {
		t.Fatalf("partial handshake: %+v, %+v", execution, unavailable)
	}
	for _, id := range []string{"good", "good_child"} {
		field := execution.Fields["@computed/"+id]
		if !field.Select || !field.Group || !field.Filter {
			t.Fatalf("healthy canonical field lost V3 permissions: %s, %+v", id, field)
		}
	}
	for _, id := range []string{"broken", "broken_child", "other"} {
		if unavailable[id] == nil || execution.ResolvedRevisions[id] != "" {
			t.Fatalf("invalid graph advertised: %s", id)
		}
	}
	w := call(http.MethodGet, "/api/query-computed-columns?dataset=pharos_tool_calls", nil)
	var listed []qt.ComputedColumn
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 5 {
		t.Fatal("repair catalogue lost definitions", w.Code, w.Body.String())
	}
	rows := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, ExpectedRevisions: execution.ResolvedRevisions, WireQuery: qt.WireQuery{Select: []string{"id", "@computed/good_child"}, Limit: 2}}
	if w = call(http.MethodPost, "/api/query/tool_calls/rows-v2", rows); w.Code != 200 {
		t.Fatal("valid rows blocked by unrelated formula", w.Code, w.Body.String())
	}
	save := func(id, source string) *httptest.ResponseRecorder {
		column := columns[id]
		column.Expression.Source = source
		w := call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", qt.SaveComputedColumnRequest{Column: column, ExpectedRevision: &column.Revision})
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &column); err != nil {
				t.Fatal(err)
			}
			columns[id] = column
		}
		return w
	}
	// A valid replacement may not invalidate an already valid dependent.
	if w = save("good", `"text"`); w.Code != 400 {
		t.Fatal("new dependent failure committed", w.Code, w.Body.String())
	}
	execution, _ = capabilities()
	if execution.ResolvedRevisions["good"] != "1" {
		t.Fatal("rejected edit changed revision", execution)
	}
	if w = save("broken", "[duration_ms]*2"); w.Code != 200 {
		t.Fatal("unrelated invalid definition prevented repair", w.Code, w.Body.String())
	}
	execution, unavailable = capabilities()
	if len(unavailable) != 1 || unavailable["other"] == nil || execution.ResolvedRevisions["broken"] != "2" || !execution.Fields["@computed/broken_child"].Select {
		t.Fatalf("repair did not restore dependent graph: %+v, %+v", execution, unavailable)
	}
	metrics := metricRequest(qt.AggSpec{ID: "restored", Expression: "SUM([@computed/broken_child])"})
	metrics.ExpectedRevisions = execution.ResolvedRevisions
	if w = call(http.MethodPost, "/api/query/tool_calls/metrics", metrics); w.Code != 200 {
		t.Fatal("restored graph unusable", w.Code, w.Body.String())
	}
	if w = save("other", "[missing_again]"); w.Code != 400 {
		t.Fatal("repair accepted another invalid formula", w.Code, w.Body.String())
	}
	if w = save("other", "[duration_ms]"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, unavailable = capabilities()
	if len(unavailable) != 0 {
		t.Fatal("repaired definitions still unavailable", unavailable)
	}
}

func TestQueryComputedCapabilitiesLargeCatalogue(t *testing.T) {
	catalog, config := testCatalog(t)
	store := qt.SQLiteComputedColumnStore{DB: catalog.DB}
	for i := range 70 {
		id := fmt.Sprintf("field_%02d", i)
		_, err := store.Save(context.Background(), queryTableScope, "pharos_tool_calls", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: id, Label: id, Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[duration_ms]/1000"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	w := apiCall(t, testServer(t, config, catalog), config.APIToken, http.MethodGet, "/api/query/tool_calls/capabilities", nil)
	var response struct {
		Execution qt.SQLiteComputedExecution `json:"computedExecution"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Execution.Fields) != 70 || len(response.Execution.ResolvedRevisions) != 70 {
		t.Fatal("catalogue description truncated or rejected", w.Code, w.Body.String())
	}
}

func TestQueryV2LibraryCompactRequests(t *testing.T) {
	catalog, config := libraryFixture(t)
	catalog.DB.SetMaxOpenConns(1)
	server := testServer(t, config, catalog)
	rowRequest := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"title"}, Limit: 1}}
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/rows-v2", rowRequest)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page qt.SQLiteRowsV2Result
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Rows) != 1 {
		t.Fatal("page", err, w.Body.String())
	}
	for _, name := range []string{"purpose", "outcome", "pr_details", "cost_usd", "first_input"} {
		if _, found := page.Rows[0][name]; !found {
			t.Fatalf("page enrichment lost %s", name)
		}
	}
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/metrics", metricRequest(qt.AggSpec{ID: "count", Expression: "COUNT()"}))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, row := range catalog.library.rows {
		for _, name := range append(slices.Clone(libraryLargeFields), libraryCostFields...) {
			if _, found := row[name]; found {
				t.Fatalf("compact v2 request loaded %s for the whole population", name)
			}
		}
	}
}

func TestQueryV2LibraryDependencies(t *testing.T) {
	catalog, _ := libraryFixture(t)
	schema, _ := sqliteQuerySchema("library")
	store := qt.SQLiteComputedColumnStore{DB: catalog.DB}
	revisions := map[string]string{}
	for id, source := range map[string]string{"text": "COALESCE([purpose],[outcome])", "nested": "[@computed/text]", "number": "[token_count]*2"} {
		column, err := store.Save(context.Background(), queryTableScope, schema.Name, qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: id, Label: id, Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: source}}})
		if err != nil {
			t.Fatal(err)
		}
		revisions[id] = column.Revision
	}
	// Keep a wide cache: staging must still omit unused values from these maps.
	wide, err := catalog.searchRows(SearchOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rowQuery := func(selects []string, where []qt.WhereTerm, order qt.OrderBys) *qt.ServerQueryV2 {
		return &qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, ExpectedRevisions: revisions, WireQuery: qt.WireQuery{Select: selects, Where: where, OrderBy: order, Limit: 2}}
	}
	metricQuery := func(spec qt.AggSpec, where []qt.WhereTerm, order []qt.OrderBy) *qt.MetricQuery {
		q := metricRequest(spec)
		q.ExpectedRevisions, q.Where, q.OrderBy = revisions, where, order
		return &q
	}
	for _, sample := range []struct {
		name    string
		rows    *qt.ServerQueryV2
		metrics *qt.MetricQuery
		fields  []string
	}{
		{"default row order", rowQuery([]string{"title"}, nil, nil), nil, []string{"activity_at", "id", "title"}},
		{"OR filter and cost sort", rowQuery([]string{"outcome"}, []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "purpose", Op: "contains", Value: "purpose"}, {Field: "changed_files", Op: "contains", Value: "parser"}}}}, qt.OrderBys{{Field: "cost_usd", Dir: "desc"}}), nil, []string{"changed_files", "cost_usd", "id", "outcome", "purpose"}},
		{"transitive computed select and sort", rowQuery([]string{"@computed/number"}, nil, qt.OrderBys{{Field: "@computed/nested", Dir: "asc"}}), nil, []string{"id", "outcome", "purpose", "token_count"}},
		{"ratio and grouping", nil, metricQuery(qt.AggSpec{ID: "ratio", Expression: "SUM([token_count])/NULLIF(SUM([tool_use_count]),0)", GroupBy: []string{"source_kind"}}, []qt.WhereTerm{{Field: "purpose", Op: "contains", Value: "purpose"}}, nil), []string{"id", "purpose", "source_kind", "token_count", "tool_use_count"}},
		{"paired shown and computed grouping", nil, metricQuery(qt.AggSpec{ID: "paired", Expression: "AVG([token_count])", ExpressionY: "SUM([cost_usd])", GroupBy: []string{"@computed/number"}, Scope: "shownRows"}, nil, []qt.OrderBy{{Field: "@computed/nested", Dir: "desc"}}), []string{"cost_usd", "id", "outcome", "purpose", "token_count"}},
		{"distribution input", nil, metricQuery(qt.AggSpec{ID: "hist", Distribution: &qt.MetricDistribution{Kind: "histogram", Input: `IF([purpose]="Summarized purpose",[token_count],0)`, Bins: 4}, GroupBy: []string{"source_kind"}, Scope: "shownRows"}, nil, nil), []string{"activity_at", "id", "purpose", "source_kind", "token_count"}},
		{"count", nil, metricQuery(qt.AggSpec{ID: "count", Expression: "COUNT()"}, nil, nil), []string{"id"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			clock := time.Now()
			fields, err := queryV2SourceFields(context.Background(), catalog.DB, schema, sample.rows, sample.metrics, clock)
			if err != nil || !reflect.DeepEqual(fields, sample.fields) {
				t.Fatalf("fields=%v, want=%v, err=%v", fields, sample.fields, err)
			}
			tx, release, err := beginQueryV2(context.Background(), catalog.DB)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			d := queryV2Dataset("library", schema)
			d.ComputedGroupable = map[string]bool{"text": true, "nested": true, "number": true}
			d.SourceSQL, err = stageQueryRows(context.Background(), tx, "wide_rows", wide, schema)
			if err != nil {
				t.Fatal(err)
			}
			full, err := d.ExecuteV2In(context.Background(), tx, sample.rows, sample.metrics, qt.PlanOptions{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			d.SourceSQL, err = stageQueryRows(context.Background(), tx, "narrow_rows", wide, schema, fields)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := tx.Query("SELECT name FROM pragma_table_info('narrow_rows') ORDER BY name")
			if err != nil {
				t.Fatal(err)
			}
			var staged []string
			for columns.Next() {
				var name string
				if err := columns.Scan(&name); err != nil {
					t.Fatal(err)
				}
				staged = append(staged, name)
			}
			if err := columns.Err(); err != nil {
				t.Fatal(err)
			}
			columns.Close()
			if !reflect.DeepEqual(staged, fields) {
				t.Fatalf("staged unused values: %v, want %v", staged, fields)
			}
			narrow, err := d.ExecuteV2In(context.Background(), tx, sample.rows, sample.metrics, qt.PlanOptions{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(full.Metrics, narrow.Metrics) || full.Rows != nil && (full.Rows.Total != narrow.Rows.Total || !reflect.DeepEqual(full.Rows.Rows, narrow.Rows.Rows) || !reflect.DeepEqual(full.Rows.Computed, narrow.Rows.Computed)) {
				t.Fatalf("selective staging changed results: full=%+v narrow=%+v", full, narrow)
			}
		})
	}
}

func BenchmarkQueryV2LibraryStaging(b *testing.B) {
	catalog, _ := testCatalog(b)
	schema, _ := sqliteQuerySchema("library")
	rows := make([]map[string]any, 10_000)
	large := strings.Repeat("long library text ", 512)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprint(i), "title": "Work", "activity_at": "2026-10-08T00:00:00Z", "purpose": large, "outcome": large, "changed_files": large}
	}
	for _, selective := range []bool{false, true} {
		b.Run(fmt.Sprintf("selective_%t", selective), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tx, release, err := beginQueryV2(context.Background(), catalog.DB)
				if err != nil {
					b.Fatal(err)
				}
				var fields [][]string
				if selective {
					fields = [][]string{{"id", "title", "activity_at"}}
				}
				_, err = stageQueryRows(context.Background(), tx, "library_bench", rows, schema, fields...)
				releaseError := release()
				if err != nil || releaseError != nil {
					b.Fatal(err, releaseError)
				}
			}
		})
	}
}
