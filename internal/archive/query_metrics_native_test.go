package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
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
			if err != nil || !same(got, want) {
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
	if err != nil || wantErr != nil || !handled || !same(got, want) || got.Metrics.Metrics[0].Error == "" {
		t.Fatalf("group budget: %v %v %+v %+v", err, wantErr, got.Metrics, want.Metrics)
	}
	budgeted = d
	budgeted.MaxPopulation = 10
	if _, handled, err = native(budgeted, q); !handled || err == nil {
		t.Fatal("population budget ignored")
	} else if _, wantErr = budgeted.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{}); wantErr == nil || wantErr.Error() != err.Error() {
		t.Fatalf("population budget: %v, adapter %v", err, wantErr)
	}
	// Anything the native path cannot answer exactly falls through.
	for name, spec := range map[string]qt.AggSpec{
		"real values":        {ID: "x", Op: "sum", Field: "share"},
		"an unsafe sum":      {ID: "x", Op: "sum", Field: "huge"},
		"negative values":    {ID: "x", Op: "sum", Field: "signed", GroupBy: []string{"kind"}},
		"a text count":       {ID: "x", Op: "count", Field: "kind"},
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
	// Usage rows in memory: the same answers, and the same refusals.
	rows := []map[string]any{}
	if err := func() error {
		result, err := db.Query("SELECT id,kind,side,n,m,share,huge,signed FROM facts")
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var id string
			var kind, side, n, m, share, huge, signed any
			if err := result.Scan(&id, &kind, &side, &n, &m, &share, &huge, &signed); err != nil {
				return err
			}
			rows = append(rows, map[string]any{"id": id, "kind": kind, "side": side, "n": n, "m": m, "share": share, "huge": huge, "signed": signed})
		}
		return result.Err()
	}(); err != nil {
		t.Fatal(err)
	}
	inMemory := func(q qt.MetricQuery, rows []map[string]any) (qt.SQLiteV2ExecutionResult, bool, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return nativeRowMetricsV2(ctx, tx, d, q, qt.PlanOptions{}, rows)
	}
	for _, groups := range [][]string{{}, {"kind"}, {"kind", "side"}} {
		q := metricRequest(qt.AggSpec{ID: "rows", Op: "count", GroupBy: groups}, qt.AggSpec{ID: "valued", Op: "count", Field: "n", GroupBy: groups}, qt.AggSpec{ID: "n", Op: "sum", Field: "n", GroupBy: groups})
		for _, population := range [][]map[string]any{rows, {}} {
			got, handled, err := inMemory(q, population)
			staged := d
			if len(population) == 0 {
				staged.SourceSQL += " WHERE 0"
			}
			want, wantErr := staged.ExecuteV2(ctx, db, nil, &q, qt.PlanOptions{})
			if err != nil || wantErr != nil || !handled || !same(got, want) {
				left, _ := json.Marshal(got)
				right, _ := json.Marshal(want)
				t.Fatalf("in memory %v over %d rows: %v %v\n got %s\nwant %s", groups, len(population), err, wantErr, left, right)
			}
		}
	}
	for name, spec := range map[string]qt.AggSpec{
		"real values": {ID: "x", Op: "sum", Field: "share"}, "an unsafe sum": {ID: "x", Op: "sum", Field: "huge"}, "negative values": {ID: "x", Op: "sum", Field: "signed"},
	} {
		if _, handled, err := inMemory(metricRequest(spec), rows); handled || err != nil {
			t.Errorf("in memory, %s: answered natively (%v)", name, err)
		}
	}
	if _, handled, _ := inMemory(filtered, rows); handled {
		t.Error("rows in memory cannot apply a filter")
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
	for _, sample := range []struct {
		name, dataset string
		q             qt.MetricQuery
		unstaged      bool // answered natively without staging rows
	}{
		{"message mix", "writing_messages", mix, true},
		{"message mix, filtered", "writing_messages", filtered(mix, qt.WhereTerm{Field: "provider", Op: "=", Value: "codex"}), true},
		{"tool tokens by tool", "tool_calls", sums([]string{"result_tokens", "call_count"}, "tool_name"), true},
		{"tool errors by status", "tool_calls", filtered(sums([]string{"error_count"}, "status", "tool_category"), qt.WhereTerm{Field: "provider", Op: "=", Value: "codex"}), true},
		{"hourly token types", "usage", sums(types, "hour"), true},
		{"hourly tokens by model", "usage", sums([]string{"total_tokens"}, "hour", "model_family"), true},
		{"hourly tokens in one hour", "usage", filtered(sums([]string{"total_tokens"}, "hour"), qt.WhereTerm{Field: "hour", Op: "=", Value: "2026-09-21T03:00:00Z"}), true},
		{"weekly tokens by repository", "usage", sums([]string{"total_tokens"}, "week", "repository_name"), true},
		// A daily filter is applied to staged rows, by the native statement.
		{"weekly tokens over a threshold", "usage", filtered(sums([]string{"total_tokens"}, "week"), qt.WhereTerm{Field: "total_tokens", Op: ">", Value: "1500"}), false},
		// Hourly rows are selected before, as the adapter's are.
		{"hourly tokens over a threshold", "usage", filtered(sums([]string{"total_tokens"}, "hour"), qt.WhereTerm{Field: "total_tokens", Op: ">", Value: "1500"}), true},
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
			if err != nil || !same(got, want) {
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
				local, _ := server.queryTableSchema("usage")
				if hours, err = catalog.hourlyUsageForAggregation(context.Background(), sample.q.Where, local); err != nil {
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
