package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// nativeFixture is a source with integer, real, unsafe and negative number
// columns, NULLs, and text keys that are NULL or empty.
func nativeFixture(t *testing.T) (*sql.DB, qt.SQLiteV2Dataset) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "native.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tx := seedTx(t, db)
	for _, statement := range []string{qt.SQLiteComputedColumnsDDL, `CREATE TABLE facts(id TEXT PRIMARY KEY,kind TEXT,side TEXT,n INTEGER,m INTEGER,share REAL,huge INTEGER,signed INTEGER)`} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 40 {
		kind := []any{"a", "b", "", nil}[i%4]
		var n any = int64(i % 7)
		if i%5 == 0 {
			n = nil
		}
		if _, err := tx.Exec(`INSERT INTO facts VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprint("f", i), kind, []string{"x", "y"}[i%2], n, i*3, float64(i)/4, int64(1)<<51, i-20); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	field := func(name, kind string) string {
		numeric := ""
		if kind == "number" {
			numeric = `,"expressionNumeric":true`
		}
		return fmt.Sprintf(`{"name":%q,"label":%q,"type":%q,"bindings":{"sqlite":{"expr":"r.\"%s\""%s}}}`, name, name, kind, name, numeric)
	}
	fields := []string{field("id", "text"), field("kind", "text"), field("side", "enum")}
	for _, name := range []string{"n", "m", "share", "huge", "signed"} {
		fields = append(fields, field(name, "number"))
	}
	schema, err := qt.LoadSQLiteSchema([]byte(`{"name":"facts","idField":"id","fields":[` + strings.Join(fields, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return db, qt.SQLiteV2Dataset{Schema: schema, SourceSQL: (sqlDataset{columns: map[string]string{"id": "f.id", "kind": "f.kind", "side": "f.side", "n": "f.n", "m": "f.m", "share": "f.share", "huge": "f.huge", "signed": "f.signed"}, from: "FROM facts f"}).v2Source(schema), Scope: "local", Dataset: "facts"}
}

// The native path answers plain counts and sums exactly as the adapter does,
// in the adapter's order, NULL and empty keys and empty populations included,
// keeps its budgets, and leaves everything else to it.
func TestNativeMetricsMatchTheAdapter(t *testing.T) {
	db, d := nativeFixture(t)
	ctx := context.Background()
	native := func(d qt.SQLiteV2Dataset, q qt.MetricQuery) (qt.SQLiteV2ExecutionResult, bool, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return (&Catalog{}).nativeMetricsV2(ctx, tx, d, q, qt.PlanOptions{}, nil)
	}
	for _, groups := range [][]string{{}, {"kind"}, {"kind", "side"}, {"side", "kind"}} {
		for _, where := range [][]qt.WhereTerm{nil, {{Field: "kind", Op: "=", Value: "a"}}, {{Field: "n", Op: ">", Value: "2"}}, {{Field: "kind", Op: "is_null"}},
			{{Any: []qt.WhereClause{{Field: "side", Op: "!=", Value: "x"}, {Field: "kind", Op: "="}}}}, {{Field: "kind", Op: "=", Value: "absent"}}} {
			q := metricRequest(qt.AggSpec{ID: "rows", Op: "count", GroupBy: groups}, qt.AggSpec{ID: "valued", Op: "count", Field: "n", GroupBy: groups},
				qt.AggSpec{ID: "n", Op: "sum", Field: "n", GroupBy: groups}, qt.AggSpec{ID: "m", Op: "sum", Field: "m", GroupBy: groups}, qt.AggSpec{ID: "total", Op: "sum", Field: "m"})
			q.Where = where
			got, handled, err := native(d, q)
			if err != nil || !handled {
				t.Fatalf("%v/%v not answered: %v", groups, where, err)
			}
			want, err := d.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{})
			if err != nil || !identical(got, want) {
				left, _ := json.Marshal(got)
				right, _ := json.Marshal(want)
				t.Fatalf("%v/%v: %v\n got %s\nwant %s", groups, where, err, left, right)
			}
		}
	}
	// Budgets: a metric over too many groups fails alone; too many rows fail
	// the request, with the adapter's errors.
	budgeted := d
	budgeted.MaxGroups = 2
	q := metricRequest(qt.AggSpec{ID: "kinds", Op: "count", GroupBy: []string{"kind"}}, qt.AggSpec{ID: "rows", Op: "count"})
	got, handled, err := native(budgeted, q)
	want, wantErr := budgeted.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{})
	if err != nil || wantErr != nil || !handled || !identical(got, want) || got.Metrics.Metrics[0].Error == "" {
		t.Fatalf("group budget: %v %v %+v %+v", err, wantErr, got.Metrics, want.Metrics)
	}
	budgeted = d
	budgeted.MaxPopulation = 10
	if _, handled, err = native(budgeted, q); !handled || err == nil {
		t.Fatal("population budget ignored")
	} else if _, wantErr = budgeted.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{}); wantErr == nil || wantErr.Error() != err.Error() {
		t.Fatalf("population budget: %v, adapter %v", err, wantErr)
	}
	// Requests the adapter refuses as it executes are refused alike: stale
	// computed revisions, and a window beyond the host's row budget.
	for name, change := range map[string]func(*qt.MetricQuery){
		"stale revisions": func(q *qt.MetricQuery) { q.ExpectedRevisions = map[string]string{"missing": "stale"} },
		"a wide window":   func(q *qt.MetricQuery) { q.Limit = 20_000 },
	} {
		q := metricRequest(qt.AggSpec{ID: "n", Op: "count", GroupBy: []string{"kind"}})
		change(&q)
		_, handled, err := native(d, q)
		_, wantErr := d.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{})
		// The compiler does not see revisions, so the native path must check them.
		if wantErr == nil || name == "stale revisions" && !handled || handled && (err == nil || err.Error() != wantErr.Error()) {
			t.Errorf("%s: %v, adapter %v", name, err, wantErr)
		}
	}
	// Anything the native path cannot answer exactly falls through.
	for name, spec := range map[string]qt.AggSpec{
		"real values":        {ID: "x", Op: "sum", Field: "share"},
		"an unsafe sum":      {ID: "x", Op: "sum", Field: "huge"},
		"negative values":    {ID: "x", Op: "sum", Field: "signed", GroupBy: []string{"kind"}},
		"a text count":       {ID: "x", Op: "count", Field: "kind"},
		"a distinct count":   {ID: "x", Op: "count_distinct", Field: "kind"},
		"an average":         {ID: "x", Op: "avg", Field: "n"},
		"an expression":      {ID: "x", Expression: "SUM([n])"},
		"a number key":       {ID: "x", Op: "count", GroupBy: []string{"n"}},
		"a group limit":      {ID: "x", Op: "count", GroupBy: []string{"kind"}, GroupLimit: 1},
		"a minimum count":    {ID: "x", Op: "count", GroupBy: []string{"kind"}, MinCount: 2},
		"a sort":             {ID: "x", Op: "count", GroupBy: []string{"kind"}, Sort: []qt.MetricSort{{Key: "group0", Dir: "asc"}}},
		"a metric filter":    {ID: "x", Op: "count", Where: []qt.WhereTerm{{Field: "side", Op: "=", Value: "x"}}},
		"shown rows":         {ID: "x", Op: "count", Scope: "shownRows"},
		"a computed field":   {ID: "x", Op: "sum", Field: "@computed/doubled"},
		"an unknown field":   {ID: "x", Op: "sum", Field: "missing"},
		"a distribution":     {ID: "x", Op: "count", Distribution: &qt.MetricDistribution{Kind: "box", Input: "[n]"}},
		"a calendar groupBy": {ID: "x", Op: "count", DateGrouping: &qt.MetricDateGrouping{}},
	} {
		if _, handled, err := native(d, metricRequest(spec)); handled || err != nil {
			t.Errorf("%s: answered natively (%v)", name, err)
		}
	}
	duplicate := metricRequest(qt.AggSpec{ID: "x", Op: "count"}, qt.AggSpec{ID: "x", Op: "sum", Field: "n"})
	if _, handled, err := native(d, duplicate); handled || err != nil {
		t.Error("a request the compiler refuses must reach the adapter", err)
	}
	filtered := metricRequest(qt.AggSpec{ID: "x", Op: "count"})
	filtered.Where = []qt.WhereTerm{{Field: "@computed/doubled", Op: ">", Value: "1"}}
	if _, handled, err := native(d, filtered); handled || err != nil {
		t.Error("a computed filter must reach the adapter", err)
	}
}

// identical compares results exactly: two floats match only if they are the
// same float64.
func identical(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return string(a) == string(b)
}

// Usage's metrics over its rows in memory, filtered or not, must give the
// adapter's answers over a staged copy of the same rows, as the Usage path
// would stage them: sums of decimals added exactly and rounded once, which
// naive float64 addition does not, distinct counts, NULLs and empty strings,
// and the adapter's errors.
func TestUsageRowMetricsMatchTheStagedAdapter(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "exact.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(qt.SQLiteComputedColumnsDDL); err != nil {
		t.Fatal(err)
	}
	schema, err := qt.LoadSQLiteSchema([]byte(`{"name":"usage_like","idField":"id","fields":[
 {"name":"id","label":"ID","type":"text","bindings":{"sqlite":{"expr":"r.\"id\""}}},
 {"name":"kind","label":"Kind","type":"text","bindings":{"sqlite":{"expr":"r.\"kind\""}}},
 {"name":"session","label":"Session","type":"text","bindings":{"sqlite":{"expr":"r.\"session\""}}},
 {"name":"cost","label":"Cost","type":"number","bindings":{"sqlite":{"expr":"r.\"cost\"","expressionNumeric":true}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := qt.SQLiteV2Dataset{Schema: schema, Scope: "local", Dataset: "usage_like"}
	ctx := context.Background()
	// Each population is compared unfiltered and under filters on NULL, empty
	// and text values, numbers, OR and negation.
	filters := [][]qt.WhereTerm{nil, {{Field: "kind", Op: "=", Value: "a"}}, {{Field: "kind", Op: "is_null"}}, {{Field: "session", Op: "contains", Value: "nil"}},
		{{Field: "session", Op: "!=", Value: "x"}}, {{Field: "cost", Op: ">", Value: "0.2"}}, {{Field: "kind", Op: "=", Value: "b", Negated: true}},
		{{Any: []qt.WhereClause{{Field: "kind", Op: "=", Value: "c"}, {Field: "session", Op: "is_null"}}}}}
	compare := func(name string, rows []map[string]any, specs ...qt.AggSpec) (qt.SQLiteV2ExecutionResult, bool) {
		t.Helper()
		var unfiltered qt.SQLiteV2ExecutionResult
		answered := true
		for _, where := range filters {
			tx, release, err := beginQueryV2(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			q := metricRequest(specs...)
			q.Where = where
			got, handled, err := usageRowMetricsV2(ctx, tx, d, q, qt.PlanOptions{}, rows)
			if err != nil {
				t.Fatal(name, where, err)
			}
			staged := d
			if staged.SourceSQL, err = stageQueryRows(ctx, tx, "exact_rows", rows, schema, []string{"cost", "id", "kind", "session"}); err != nil {
				t.Fatal(err)
			}
			want, err := staged.ExecuteV2In(ctx, tx, nil, &q, qt.PlanOptions{})
			release()
			if err != nil {
				t.Fatal(name, where, err)
			}
			if handled && !identical(got, want) {
				left, _ := json.Marshal(got)
				right, _ := json.Marshal(want)
				t.Fatalf("%s %v:\n got %s\nwant %s", name, where, left, right)
			}
			if where == nil {
				unfiltered = want
			}
			answered = answered && handled
		}
		return unfiltered, answered
	}
	row := func(i int, kind, session, cost any) map[string]any {
		return map[string]any{"id": fmt.Sprint("r", i), "kind": kind, "session": session, "cost": cost}
	}
	byKind := []qt.AggSpec{{ID: "cost", Op: "sum", Field: "cost", GroupBy: []string{"kind"}}, {ID: "costs", Op: "count", Field: "cost", GroupBy: []string{"kind"}},
		{ID: "sessions", Op: "count_distinct", Field: "session", GroupBy: []string{"kind"}}, {ID: "total", Op: "sum", Field: "cost"}, {ID: "all sessions", Op: "count_distinct", Field: "session"}}
	// Many small fractions, as per-request API costs are.
	small := []map[string]any{}
	for i := range 5000 {
		small = append(small, row(i, []any{"a", "b", "", nil}[i%4], fmt.Sprint("s", i%37), float64(i%997)*0.000173+0.0000031*float64(i%13)))
	}
	if _, handled := compare("small fractions", small, byKind...); !handled {
		t.Fatal("small fractions fell through")
	}
	// Ten tenths: naive float64 addition drifts to 0.9999999999999999.
	tenths := []map[string]any{}
	naive := 0.0
	for i := range 10 {
		tenths = append(tenths, row(i, "a", "s", 0.1))
		naive += 0.1
	}
	want, handled := compare("ten tenths", tenths, byKind...)
	if !handled || want.Metrics.Metrics[0].Buckets[0].Value != 1.0 || naive == 1.0 {
		t.Fatalf("ten tenths: handled %v, adapter %v, naive %v", handled, want.Metrics.Metrics[0].Buckets[0].Value, naive)
	}
	// Large and small magnitudes, cancellation and NULLs.
	mixed := []map[string]any{row(0, "a", "s", 2251799813685247.5), row(1, "a", "s", -2251799813685246.25), row(2, "a", nil, 1e-299), row(3, "b", "", 2.5e-250),
		row(4, "b", "t", nil), row(5, "c", nil, 1e15), row(6, "c", "u", 0.3), row(7, "c", "u", -1e15), row(8, "d", nil, nil), row(9, nil, "", 1e-10), row(10, nil, "v", int64(7)), row(11, "e", "w", 123456789.123456789)}
	if _, handled := compare("magnitudes", mixed, byKind...); !handled {
		t.Fatal("magnitudes fell through")
	}
	// Distinct counts: text values and the empty string, never NULL.
	sessions := []map[string]any{row(0, "a", "x", 1.5), row(1, "a", "x", nil), row(2, "a", "", nil), row(3, "a", nil, 0.25), row(4, "b", nil, nil), row(5, "b", "", nil), row(6, "b", "y", 2.0)}
	if _, handled := compare("distinct sessions", sessions, byKind...); !handled {
		t.Fatal("distinct sessions fell through")
	}
	if _, handled := compare("no rows", nil, byKind...); !handled {
		t.Fatal("no rows fell through")
	}
	// Where the adapter reports an error, the answer is the adapter's, from
	// memory or staged.
	for name, rows := range map[string][]map[string]any{
		"an absolute total beyond safe integers": {row(0, "a", "s", 4503599627370495.5), row(1, "a", "s", 4503599627370495.5), row(2, "a", "s", 0.5)},
		"a value out of range":                   {row(0, "a", "s", 1e-301)},
		"an unsafe integer value":                {row(0, "a", "s", 9007199254740992.0)},
		"text in a number field":                 {row(0, "a", "s", "1.5")},
		"a number in a text field":               {row(0, "a", int64(3), 1.5)},
	} {
		compare(name, rows, byKind...)
	}
}

// Usage's rows hold only values Query Table's in-memory executor reads, so
// that its metrics are not staged for want of a type it does not take.
func TestUsageRowsHoldValuesTheExecutorReads(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestUsageFixture(t, catalog)
	schema := PharosUsageSchema()
	columns := slices.Sorted(maps.Keys(schema.Fields))
	ctx := context.Background()
	for name, read := range map[string]func() ([]map[string]any, error){
		"daily":               func() ([]map[string]any, error) { return catalog.localUsageBaseRows(ctx, false) },
		"hourly":              func() ([]map[string]any, error) { return catalog.localUsageBaseRows(ctx, true) },
		"daily with lineage":  func() ([]map[string]any, error) { return catalog.localUsageRows(ctx) },
		"hourly with lineage": func() ([]map[string]any, error) { return catalog.localUsageHourlyRows(ctx) },
	} {
		rows, err := read()
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s: %d rows, %v", name, len(rows), err)
		}
		for _, row := range rows {
			if _, err := qt.SQLiteRowValues(row, schema, columns); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
}

// Over the product's datasets the native path, where it answers, matches the
// adapter on the same request: Human Words' message mix, tool-call sums, and
// Usage's daily and hourly charts.
func TestNativeMetricsMatchTheAdapterOnPharosDatasets(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 60)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	tx := seedTx(t, catalog.DB)
	for i, text := range []string{"Please fix the parser.", "```go\nfunc main() {}\n```", "Continue."} {
		record := WorkspaceRecord{SourceID: fmt.Sprint("mix-", i), SourceKind: "codex", Account: "local", Title: "Mix",
			Conversations: []ConversationRecord{{NativeID: fmt.Sprint("mix-", i), Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: fmt.Sprint("m-", i), Role: "user", Kind: "message", Text: text, CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true}}}}}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	mix := metricRequest(qt.AggSpec{ID: "messages", Op: "count"})
	for _, category := range authorshipCategories {
		mix.Metrics = append(mix.Metrics, qt.AggSpec{ID: category, Op: "sum", Field: category + "_words"})
	}
	sums := func(fields []string, groups ...string) qt.MetricQuery {
		q := metricRequest()
		for _, field := range fields {
			q.Metrics = append(q.Metrics, qt.AggSpec{ID: field, Op: "sum", Field: field, GroupBy: groups})
		}
		return q
	}
	types := []string{"uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "unclassified_tokens"}
	filtered := func(q qt.MetricQuery, where ...qt.WhereTerm) qt.MetricQuery { q.Where = where; return q }
	summary := sums([]string{"total_tokens", "cost_usd", "uncached_input_tokens", "cache_read_input_tokens", "output_tokens"})
	summary.Metrics = append(summary.Metrics, qt.AggSpec{ID: "agent_sessions", Op: "count_distinct", Field: "agent_session_id"})
	for _, sample := range []struct {
		name, dataset string
		q             qt.MetricQuery
		unstaged      bool // answered without staging rows
	}{
		{"message mix", "writing_messages", mix, true},
		{"message mix, filtered", "writing_messages", filtered(mix, qt.WhereTerm{Field: "provider", Op: "=", Value: "codex"}), true},
		{"tool tokens by tool", "tool_calls", sums([]string{"result_tokens", "call_count"}, "tool_name"), true},
		{"tool errors by status", "tool_calls", filtered(sums([]string{"error_count"}, "status", "tool_category"), qt.WhereTerm{Field: "provider", Op: "=", Value: "codex"}), true},
		{"hourly token types", "usage", sums(types, "hour"), true},
		{"hourly tokens by model", "usage", sums([]string{"total_tokens"}, "hour", "model_family"), true},
		{"hourly tokens in one hour", "usage", filtered(sums([]string{"total_tokens"}, "hour"), qt.WhereTerm{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}), true},
		{"weekly tokens by repository", "usage", sums([]string{"total_tokens"}, "week", "repository_name"), true},
		{"token summary", "usage", summary, true},
		// Daily filters are applied in memory too.
		{"token summary, one provider", "usage", filtered(summary, qt.WhereTerm{Field: "provider", Op: "=", Value: "codex"}), true},
		{"weekly tokens over a threshold", "usage", filtered(sums([]string{"total_tokens"}, "week"), qt.WhereTerm{Field: "total_tokens", Op: ">", Value: "1500"}), true},
		{"daily cost by provider since a time", "usage", filtered(sums([]string{"cost_usd"}, "day", "provider"), qt.WhereTerm{Field: "first_usage_at", Op: ">=", Value: "2026-09-21T00:00:00Z"}), true},
		{"weekly cost by model, no repository", "usage", filtered(sums([]string{"cost_usd"}, "week", "model"), qt.WhereTerm{Field: "repository_name", Op: "is_null"}), true},
		// Hourly rows are selected before, as the adapter's are.
		{"hourly tokens over a threshold", "usage", filtered(sums([]string{"total_tokens"}, "hour"), qt.WhereTerm{Field: "total_tokens", Op: ">", Value: "1500"}), true},
		{"hourly cost by provider, one model", "usage", filtered(sums([]string{"cost_usd"}, "hour", "provider"), qt.WhereTerm{Field: "model", Op: "!=", Value: "absent"}), true},
		// A formula is staged.
		{"tokens per day", "usage", metricRequest(qt.AggSpec{ID: "per day", Expression: "SUM([total_tokens]) / COUNT()"}), false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			schema, err := sqliteQuerySchema(sample.dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, err := server.executeQueryV2(context.Background(), sample.dataset, nil, schema, nil, &sample.q)
			if err != nil {
				t.Fatal(err)
			}
			override(t, &nativeMetrics, false)
			want, err := server.executeQueryV2(context.Background(), sample.dataset, nil, schema, nil, &sample.q)
			if err != nil || !identical(got, want) {
				left, _ := json.Marshal(got.Metrics)
				right, _ := json.Marshal(want.Metrics)
				t.Fatalf("%v\n got %s\nwant %s", err, left, right)
			}
			for _, metric := range got.Metrics.Metrics {
				if metric.Error != "" || len(metric.Buckets) == 0 {
					t.Fatalf("metric %s: %+v", metric.ID, metric)
				}
			}
			// The native path answered without staging what it should.
			nativeMetrics = true
			d := queryV2Dataset(sample.dataset, schema)
			table, sqlBacked := sqlDatasetFor(sample.dataset)
			d.SourceSQL = table.v2Source(schema)
			var daily, hours []map[string]any
			if !sqlBacked {
				if daily, err = server.queryTableRows(context.Background(), sample.dataset, nil, nil); err != nil {
					t.Fatal(err)
				}
				if hours, err = catalog.hourlyUsageForAggregation(context.Background(), sample.q.Where); err != nil {
					t.Fatal(err)
				}
			}
			_, handled, err := server.metricsWithoutStaging(context.Background(), sample.dataset, table, d, sample.q, qt.PlanOptions{Now: time.Now()}, daily, hours, false)
			if err != nil || handled != sample.unstaged {
				t.Fatalf("answered without staging = %v, want %v (%v)", handled, sample.unstaged, err)
			}
		})
	}
}
