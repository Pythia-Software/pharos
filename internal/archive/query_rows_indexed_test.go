package archive

import (
	"context"
	"reflect"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func TestQueryCountCacheUsesTransactionSnapshot(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 31)
	ctx := context.Background()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	query := "SELECT COUNT(*) FROM workspaces"
	before, err := c.queryCount(ctx, tx, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at)
 VALUES('later','codex','local','later','Later','2026-10-15T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	old, err := c.queryCount(ctx, tx, query, nil)
	if err != nil || old != before {
		t.Fatal("old snapshot count changed", old, before, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	next, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Rollback()
	after, err := c.queryCount(ctx, next, query, nil)
	if err != nil || after != before+1 {
		t.Fatal("cached total survived a source write", before, after, err)
	}
}

func TestQueryCountCubeRequiresFreshSnapshot(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 31)
	ctx := context.Background()
	if err := c.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	read := func() (int64, bool) {
		t.Helper()
		tx, err := c.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		n, fresh, err := c.freshToolCubeCount(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return n, fresh
	}
	if n, fresh := read(); !fresh || n != 31 {
		t.Fatal(n, fresh)
	}
	if _, err := c.DB.Exec("DELETE FROM tool_calls WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	if _, fresh := read(); fresh {
		t.Fatal("stale cube accepted after a raw ledger change")
	}
	if err := c.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	if n, fresh := read(); !fresh || n != 30 {
		t.Fatal(n, fresh)
	}
	if _, err := c.DB.Exec("UPDATE meta SET value='different' WHERE key='tool_rollup_built_full_revision'"); err != nil {
		t.Fatal(err)
	}
	if _, fresh := read(); fresh {
		t.Fatal("cube accepted with a mismatched full revision")
	}
}

func TestQueryCountOnlyMetricRetainsEnvelope(t *testing.T) {
	c, cfg := testCatalog(t)
	seedToolCalls(t, c, 31)
	s := testServer(t, cfg, c)
	q := metricRequest(qt.AggSpec{ID: "count", Op: "count"}, qt.AggSpec{ID: "formula", Expression: "COUNT()"})
	result, err := s.executeQueryV2(context.Background(), "tool_calls", nil, PharosToolCallsSchema(), nil, &q)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range result.Metrics.Metrics {
		if metric.ProcessedRows != 31 || metric.Coverage != "exact" || metric.GroupCount != 1 || metric.Buckets[0].Count != 31 || metric.Buckets[0].Value != float64(31) {
			t.Fatal(metric)
		}
	}
	q.ExpectedRevisions = map[string]string{"missing": "stale"}
	if _, err := s.executeQueryV2(context.Background(), "tool_calls", nil, PharosToolCallsSchema(), nil, &q); err == nil {
		t.Fatal("count bypassed expected revisions")
	}
	q.ExpectedRevisions = nil
	if _, err := s.executeQueryV2(context.Background(), "tool_calls", nil, PharosToolCallsSchema(), nil, &q, 20); err == nil {
		t.Fatal("count bypassed explicit population budget")
	}
}

func TestQueryIndexedRowsMatchTypedExecutor(t *testing.T) {
	c, cfg := testCatalog(t)
	seedToolCalls(t, c, 131)
	s := testServer(t, cfg, c)
	ctx := context.Background()
	schema := PharosToolCallsSchema()
	for _, order := range []qt.OrderBy{{Field: "started_at", Dir: "desc"}, {Field: "duration_ms", Dir: "asc", Nulls: "first"}, {Field: "duration_ms", Dir: "desc", Nulls: "last"}} {
		q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{
			Select: []string{"id", "duration_ms", "provider", "started_at"}, Limit: 17, Offset: 11, OrderBy: qt.OrderBys{order},
			Where: []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "provider", Op: "=", Value: "claude"}, {Field: "duration_ms", Op: ">", Value: "100"}}}},
		}}
		tx, release, err := beginQueryV2(ctx, c.DB)
		if err != nil {
			t.Fatal(err)
		}
		d := queryV2Dataset("tool_calls", schema)
		d.SourceSQL = toolCallDataset.v2Source(schema)
		o := qt.PlanOptions{Now: time.Now()}
		want, err := d.ExecuteV2In(ctx, tx, &q, nil, o)
		if err != nil {
			release()
			t.Fatal(err)
		}
		got, used, err := s.indexedQueryRowsV2(ctx, tx, toolCallDataset, d, q, o, nil, 1000)
		if err != nil || !used {
			release()
			t.Fatal(used, err)
		}
		if got.Rows.Total != want.Rows.Total || !reflect.DeepEqual(got.Rows.Rows, want.Rows.Rows) || !reflect.DeepEqual(got.Rows.Computed, want.Rows.Computed) {
			release()
			t.Fatalf("%+v: page differs: got %+v want %+v", order, got.Rows, want.Rows)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}
}

// The page's filter follows the source's numbered arguments; question marks
// in literals and identifiers are not parameters.
func TestNumberQueryParameters(t *testing.T) {
	if got := numberQueryParameters(`a=? AND b='?''?' AND "?"=?`, 3); got != `a=?4 AND b='?''?' AND "?"=?5` {
		t.Fatal(got)
	}
}
