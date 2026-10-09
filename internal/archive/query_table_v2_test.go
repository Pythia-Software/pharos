package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	for _, dataset := range []string{"library", "activity", "usage", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts"} {
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
func TestQueryV2MetricMatrix(t *testing.T) {
	db, d := queryV2Fixture(t, 1001)
	specs := []qt.AggSpec{}
	for _, op := range []string{"count", "count_distinct", "sum", "avg", "min", "max"} {
		specs = append(specs, qt.AggSpec{ID: op, Op: op, Field: "x", GroupBy: []string{"category", "other"}})
	}
	for i, formula := range []string{"COUNT()", "MEDIAN([x])", "SUM([x])/NULLIF(SUM([y]),0)", "AVG(IF([flag],[x],NULL))", "IF(COUNT()>0,SUM([x]),1/0)", "COALESCE(SUM([x]),1/0)"} {
		specs = append(specs, qt.AggSpec{ID: fmt.Sprint("formula", i), Expression: formula, GroupBy: []string{"category"}})
	}
	specs = append(specs, qt.AggSpec{ID: "paired", Expression: "AVG([x])", ExpressionY: "SUM([y])", GroupBy: []string{"category"}, Sort: []qt.MetricSort{{Key: "value", Dir: "desc"}}, GroupLimit: 3}, qt.AggSpec{ID: "box", Distribution: &qt.MetricDistribution{Kind: "box", Input: "[x]+1", Whiskers: "tukey"}, GroupBy: []string{"category"}}, qt.AggSpec{ID: "histogram", Distribution: &qt.MetricDistribution{Kind: "histogram", Input: "[x]", Bins: 30}, GroupBy: []string{"category"}})
	q := metricRequest(specs...)
	result, err := d.ExecuteV2(context.Background(), db, nil, &q, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Metrics.Metrics) != len(specs) {
		t.Fatal(result)
	}
	var edges []float64
	for _, m := range result.Metrics.Metrics {
		if m.ProcessedRows != 1001 || m.Coverage != "exact" || len(m.Buckets) == 0 {
			t.Fatal(m)
		}
		var count int64
		for _, bucket := range m.Buckets {
			if bucket.Error != "" {
				t.Fatalf("%s: %s", m.ID, bucket.Error)
			}
			count += bucket.Count
			if m.ID == "histogram" {
				h := bucket.Distribution.(*qt.MetricHistogramDistribution)
				if edges == nil {
					edges = h.Edges
				}
				if !reflect.DeepEqual(edges, h.Edges) {
					t.Fatal("histogram edges differ")
				}
				var n int64
				for _, c := range h.Counts {
					n += c
				}
				if n != h.N || *bucket.NullCount+h.N != bucket.Count {
					t.Fatal(bucket)
				}
			}
			if m.ID == "paired" && bucket.Y == nil {
				t.Fatal("paired Y missing")
			}
		}
		if m.ID == "paired" {
			if m.GroupCount != 10 || len(m.Buckets) != 3 {
				t.Fatal(m)
			}
		} else if count != 1001 {
			t.Fatalf("%s count=%d", m.ID, count)
		}
	}
	q = metricRequest(qt.AggSpec{ID: "shown", Expression: "COUNT()", Scope: "shownRows"})
	q.Limit = 7
	q.Offset = 3
	q.OrderBy = []qt.OrderBy{{Field: "x", Dir: "desc"}}
	result, err = d.ExecuteV2(context.Background(), db, nil, &q, qt.PlanOptions{})
	if err != nil || result.Metrics.Metrics[0].Buckets[0].Value != float64(7) || result.Metrics.Metrics[0].ProcessedRows != 7 {
		t.Fatalf("shown rows: %+v %v", result, err)
	}
	q = metricRequest(qt.AggSpec{ID: "zero", Expression: "SUM([x])/0"})
	result, err = d.ExecuteV2(context.Background(), db, nil, &q, qt.PlanOptions{})
	if err != nil || result.Metrics.Metrics[0].Buckets[0].Error != "divide_by_zero" {
		t.Fatalf("division diagnostic: %+v %v", result, err)
	}
}
func TestQueryV2ComputedAndLimits(t *testing.T) {
	db, d := queryV2Fixture(t, 100)
	store := qt.SQLiteComputedColumnStore{DB: db}
	revisions := map[string]string{}
	for _, c := range []qt.ComputedColumn{{ID: "double", Label: "Double", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[x]*2"}}, {ID: "next", Label: "Next", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[@computed/double]+1"}}} {
		saved, err := store.Save(context.Background(), "local", "samples", qt.SaveComputedColumnRequest{Column: c})
		if err != nil {
			t.Fatal(err)
		}
		revisions[c.ID] = saved.Revision
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	handshake, err := d.DescribeComputedIn(context.Background(), tx, []string{"next"}, qt.PlanOptions{})
	tx.Rollback()
	if err != nil || !reflect.DeepEqual(handshake.ResolvedRevisions, revisions) {
		t.Fatalf("handshake %+v %v", handshake, err)
	}
	q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, ExpectedRevisions: revisions, WireQuery: qt.WireQuery{Select: []string{"@computed/next"}, Limit: 5, OrderBy: qt.OrderBys{{Field: "@computed/next", Dir: "desc"}}}}
	result, err := d.ExecuteV2(context.Background(), db, &q, nil, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows.Computed) != 5 || result.Rows.Computed[0].Values["next"].Value != float64(199) {
		t.Fatal(result)
	}
	q.ExpectedRevisions = map[string]string{"double": "stale", "next": revisions["next"]}
	if _, err = d.ExecuteV2(context.Background(), db, &q, nil, qt.PlanOptions{}); err == nil {
		t.Fatal("stale revision accepted")
	}
	m := metricRequest(qt.AggSpec{ID: "count", Expression: "COUNT()"})
	d.MaxPopulation = 99
	if _, err = d.ExecuteV2(context.Background(), db, nil, &m, qt.PlanOptions{}); err == nil {
		t.Fatal("population budget ignored")
	}
	d.MaxPopulation = 100
	d.MaxGroups = 2
	m.Metrics[0].GroupBy = []string{"category"}
	m.Metrics[0].GroupLimit = 1
	if _, err = d.ExecuteV2(context.Background(), db, nil, &m, qt.PlanOptions{}); err == nil {
		t.Fatal("top-N hid group budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = d.ExecuteV2(ctx, db, nil, &m, qt.PlanOptions{}); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestQueryV2PharosHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 300)
	server := NewServer(config, catalog)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+config.APIToken)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	for _, dataset := range []string{"library", "activity", "usage", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts"} {
		t.Run(dataset, func(t *testing.T) {
			w := call(http.MethodGet, "/api/query/"+dataset+"/capabilities", "")
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			schema, _ := sqliteQuerySchema(dataset)
			selection := []string{}
			specs := []qt.AggSpec{{ID: "n", Expression: "COUNT()"}}
			for name, field := range schema.Fields {
				if field.Expr != "" {
					selection = append(selection, name)
				}
				if field.Kind == qt.FieldNumber && field.Expr != "" && (field.AggregateOps == nil || len(field.AggregateOps) > 0) {
					specs = append(specs, qt.AggSpec{ID: name, Expression: "SUM([" + name + "])"})
				}
			}
			rowsBody, _ := json.Marshal(qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: selection, Limit: 10}})
			w = call(http.MethodPost, "/api/query/"+dataset+"/rows-v2", string(rowsBody))
			if w.Code != 200 {
				t.Fatal("typed rows", w.Code, w.Body.String())
			}
			for start := 0; start < len(specs); start += 20 {
				metricBody, _ := json.Marshal(metricRequest(specs[start:min(start+20, len(specs))]...))
				w = call(http.MethodPost, "/api/query/"+dataset+"/metrics", string(metricBody))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
	body := `{"column":{"id":"duration_s","label":"Seconds","expression":{"language":"qt-expr","version":1,"source":"[duration_ms]/1000"}},"expectedRevision":null}`
	w := call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var column qt.ComputedColumn
	if err := json.Unmarshal(w.Body.Bytes(), &column); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", body)
	if w.Code != 409 {
		t.Fatal("create conflict", w.Code)
	}
	w = call(http.MethodPost, "/api/query/tool_calls/rows-v2", `{"version":2,"profile":"qt-sqlite-v1","expectedRevisions":{"duration_s":"`+column.Revision+`"},"select":["id","@computed/duration_s"],"orderBy":[{"field":"@computed/duration_s","dir":"desc"}],"limit":5}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, formula := range []string{"SUM([duration_ms])", "[missing]", "[@computed/bad]", "[duration_ms]+"} {
		invalid, _ := json.Marshal(qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: "bad", Label: "Bad", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: formula}}})
		w = call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", string(invalid))
		if w.Code != 400 {
			t.Fatal("invalid definition persisted", formula, w.Code, w.Body.String())
		}
	}
	w = call(http.MethodGet, "/api/query/tool_calls/capabilities", "")
	if w.Code != 200 {
		t.Fatal("catalogue could not load after rejected edit", w.Body.String())
	}
	for _, path := range []string{"/api/query/tool_calls/metrics", "/api/query/tool_calls/rows-v2", "/api/query/tool_calls"} {
		w = call(http.MethodPost, path, `{"version":2,"limit":1,"metrics":[]} {}`)
		if w.Code != 400 {
			t.Fatal("bad envelope accepted", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+config.APIToken)
	r.Header.Set("Origin", "https://other.example")
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin write accepted", w.Code)
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

func TestQueryV2UsageHourly(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	server := NewServer(config, catalog)
	schema, _ := sqliteQuerySchema("usage")
	for _, sample := range []struct {
		where            []qt.WhereTerm
		days, hours, sum float64
	}{
		{nil, 2, 3, 3150},
		{[]qt.WhereTerm{{Field: "total_tokens", Op: ">", Value: "1500"}}, 1, 2, 2130},
		{[]qt.WhereTerm{{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}}, 1, 1, 2130},
	} {
		q := metricRequest(qt.AggSpec{ID: "days", Expression: "COUNT()"}, qt.AggSpec{ID: "hours", Expression: "COUNT()", GroupBy: []string{"hour"}}, qt.AggSpec{ID: "sum", Expression: "SUM([total_tokens])"})
		q.Where = sample.where
		result, err := server.executeQueryV2(context.Background(), "usage", nil, schema, nil, &q)
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range []float64{sample.days, sample.hours, sample.sum} {
			var got float64
			for _, bucket := range result.Metrics.Metrics[i].Buckets {
				got += bucket.Value.(float64)
			}
			if got != want {
				t.Fatalf("%s=%v want %v", q.Metrics[i].ID, got, want)
			}
		}
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
	seedToolCalls(t, catalog, 650)
	for i, local := range []string{"2026-03-07T23:59:00", "2026-03-08T00:00:00", "2026-03-08T23:59:00", "2026-03-09T00:00:00", "2026-10-31T23:59:00", "2026-11-01T00:00:00", "2026-11-01T23:59:00", "2026-11-02T00:00:00"} {
		if _, err := catalog.DB.Exec(`UPDATE tool_calls SET started_at=strftime('%Y-%m-%dT%H:%M:%fZ',?,'utc') WHERE id=?`, local, fmt.Sprint("call-", i)); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(config, catalog)
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
	indexed := toolCallDataset
	indexed.toolSearchIndexed = true
	source, args := indexed.v2IndexedSource(schema, []qt.WhereTerm{{Field: "command", Op: "contains", Value: "go test"}})
	plan, err := queryMapsContext(context.Background(), catalog.DB, "EXPLAIN QUERY PLAN SELECT count(*) FROM ("+source+")", args...)
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprint(plan)
	if !strings.Contains(text, "VIRTUAL TABLE INDEX") {
		t.Fatal("trigram index missing", text)
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
	// Validation of every metric precedes reuse and population execution.
	q.Metrics = append(q.Metrics, qt.AggSpec{ID: "bad", Expression: "SUM([missing])"})
	queryV2ProbeCalls.Store(0)
	if _, err := d.ExecuteV2In(context.Background(), tx, nil, &q, qt.PlanOptions{}); err == nil || queryV2ProbeCalls.Load() != 0 {
		t.Fatal("invalid batch executed")
	}
}

func TestQueryV2UsageShownHours(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	server := NewServer(config, catalog)
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
			server := NewServer(config, catalog)
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

func TestQueryV2TempStorageLease(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.DB.SetMaxOpenConns(1)
	var original int
	if err := catalog.DB.QueryRow("PRAGMA temp_store").Scan(&original); err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginQueryV2(context.Background(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	var mode int
	if err := tx.QueryRow("PRAGMA temp_store").Scan(&mode); err != nil || mode != 2 {
		t.Fatal(mode, err)
	}
	if _, err := tx.Exec("CREATE TEMP TABLE lease_probe(n)"); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("PRAGMA temp_store").Scan(&mode); err != nil || mode != original {
		t.Fatal("connection settings leaked", mode, original, err)
	}
	var tables int
	if err := catalog.DB.QueryRow("SELECT count(*) FROM sqlite_temp_master").Scan(&tables); err != nil || tables != 0 {
		t.Fatal("staging leaked", tables, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, release, err = beginQueryV2(ctx, catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = release()
	if err := catalog.DB.QueryRow("PRAGMA temp_store").Scan(&mode); err != nil || mode != original {
		t.Fatal("cancelled connection leaked", mode, original, err)
	}
}
