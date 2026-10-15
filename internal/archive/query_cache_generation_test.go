package archive

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryCachesSurviveMaintenanceAndAuditCommits(t *testing.T) {
	catalog, _ := libraryFixture(t)
	other := otherProcess(t, catalog)
	ctx := context.Background()
	before := libraryRowsByID(t, catalog)
	var builds int
	compute := func(context.Context) (int, error) { builds++; return 42, nil }
	if _, err := cachedValue(ctx, catalog, "usage:probe", compute); err != nil {
		t.Fatal(err)
	}
	version, err := catalog.catalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.refreshAllLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO meta(key,value) VALUES('text_search_documents_cursor','2000')`,
		`INSERT INTO mcp_calls(called_at,tool_name,status,duration_ms,response_bytes,estimated_output_tokens) VALUES('x','search','ok',1,1,1)`,
		`INSERT INTO ui_preferences(key,value) VALUES('cache-test','{}')`,
	} {
		if _, err := other.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	after := libraryRowsByID(t, catalog)
	if !sameRow(before["c"], after["c"]) {
		t.Fatal("maintenance or audit writes rebuilt the Library cache")
	}
	if _, err := cachedValue(ctx, catalog, "usage:probe", compute); err != nil || builds != 1 {
		t.Fatalf("maintenance or audit writes rebuilt derived data: %d builds, %v", builds, err)
	}
	if current, err := catalog.catalogVersion(ctx); err != nil || current != version {
		t.Fatalf("maintenance changed input generation: %d -> %d, %v", version, current, err)
	}
	// A real source edit from another process still invalidates both caches.
	if _, err := other.DB.Exec(`UPDATE workspaces SET title='Updated' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	if rows := libraryRowsByID(t, catalog); rows["c"]["title"] != "Updated" || sameRow(before["c"], rows["c"]) {
		t.Fatal("source edit did not rebuild Library data")
	}
	if _, err := cachedValue(ctx, catalog, "usage:probe", compute); err != nil || builds != 2 {
		t.Fatalf("source edit did not rebuild derived data: %d builds, %v", builds, err)
	}
}

func TestQueryCacheGenerationRollsBackWithSourceChanges(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	before, err := catalog.catalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE workspaces SET title='Never committed' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after, err := catalog.catalogVersion(ctx); err != nil || after != before {
		t.Fatalf("rolled-back edit changed cache generation: %d -> %d, %v", before, after, err)
	}
}

func TestQueryCacheNoopIndexPreservesGenerationAndMetricIDs(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := incrementalFixture(5)
	writeIncrementalFixture(t, catalog, record)
	before, err := catalog.catalogVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := queryMaps(catalog.DB, `SELECT id,name,value FROM metrics ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	writeIncrementalFixture(t, catalog, record)
	after, err := catalog.catalogVersion(context.Background())
	if err != nil || after != before {
		t.Fatalf("no-op index evicted cached views: generation %d -> %d, %v", before, after, err)
	}
	updated, err := queryMaps(catalog.DB, `SELECT id,name,value FROM metrics ORDER BY name`)
	if err != nil || jsonText(updated) != jsonText(metrics) {
		t.Fatalf("no-op index rewrote metric identities: %v -> %v, %v", metrics, updated, err)
	}
}

func TestCancelledRequestDoesNotStartDerivedBuild(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := cachedValue(ctx, catalog, "usage:cancelled", func(context.Context) (int, error) {
		called = true
		return 42, nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled request started a build: called=%v error=%v", called, err)
	}
}

func TestDerivedCacheWaitsForOlderBuildBeforeRecomputing(t *testing.T) {
	catalog, _ := libraryFixture(t)
	other := otherProcess(t, catalog)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	var calls atomic.Int32
	compute := func(context.Context) (string, error) {
		n := calls.Add(1)
		var title string
		err := catalog.DB.QueryRow(`SELECT title FROM workspaces WHERE id='c'`).Scan(&title)
		if n == 1 {
			close(started)
			<-release
		}
		return title, err
	}
	first := make(chan error, 1)
	go func() { _, err := cachedValue(ctx, catalog, "usage:overlap", compute); first <- err }()
	<-started
	if _, err := other.DB.Exec(`UPDATE workspaces SET title='Latest' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	latest, err := catalog.catalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second := make(chan string, 1)
	go func() {
		value, err := cachedValue(ctx, catalog, "usage:overlap", compute)
		if err != nil {
			second <- err.Error()
			return
		}
		second <- value
	}()
	// While the old value is blocked, the new caller must wait, not compute.
	select {
	case value := <-second:
		t.Fatalf("overlapping computation returned %q before the first finished", value)
	case <-time.After(25 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatalf("started %d overlapping builds", calls.Load())
	}
	unblock.Do(func() { close(release) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if value := <-second; value != "Latest" || calls.Load() != 2 {
		t.Fatalf("new caller got %q after %d builds; generation %d", value, calls.Load(), latest)
	}
}

func TestConversationChildrenUseCoveringIndexAfterUpgrade(t *testing.T) {
	catalog, _ := testCatalog(t)
	if _, err := catalog.DB.Exec(`DROP INDEX conversations_parent_idx`); err != nil {
		t.Fatal(err)
	}
	// Existing catalogs receive the index on Initialize, not just new installs.
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at)
 VALUES('w','codex','local','w','Work','x');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id)
 VALUES('cb','w','codex','local','parent'),('cc','w','codex','local','child')`); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(catalog.DB, `EXPLAIN QUERY PLAN SELECT EXISTS(SELECT 1 FROM conversations WHERE parent_id=?)`, "cb")
	if err != nil || !strings.Contains(jsonText(rows), "USING COVERING INDEX conversations_parent_idx") {
		t.Fatalf("child lookup plan: %v, %v", rows, err)
	}
	if _, err := catalog.DB.Exec(`UPDATE conversations SET parent_id='cb' WHERE id='cc'`); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"cb": true, "cc": false, "missing": false} {
		var got bool
		if err := catalog.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM conversations WHERE parent_id=?)`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("children of %s: %v, %v; want %v", id, got, err, want)
		}
	}
}

func TestReadySeparatesLivenessFromProjectionCatchUp(t *testing.T) {
	catalog, config := libraryFixture(t)
	server := NewServer(config, catalog)
	if status := libraryStatusCall(t, server, "/api/ready"); status["ok"] != true {
		t.Fatalf("liveness during catch-up: %v", status)
	}
	status := libraryStatusCall(t, server, "/api/ready?projections=1")
	if status["projections_ready"] != false || status["library_pending"] != float64(3) || status["metrics_pending"] != float64(3) {
		t.Fatalf("projection catch-up: %v", status)
	}
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := catalog.indexTextSearchBatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	status = libraryStatusCall(t, server, "/api/ready?projections=1")
	if status["projections_ready"] != true || status["library_pending"] != float64(0) || status["metrics_pending"] != float64(0) || status["text_search_ready"] != true {
		t.Fatalf("caught-up projections: %v", status)
	}
}

func TestMetricProjectionHonorsWriterQueueCancellation(t *testing.T) {
	catalog, _ := libraryFixture(t)
	release, err := catalog.acquireWriteGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := catalog.refreshMetricProjections(ctx, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("metrics writer bypassed the queue: %v", err)
	}
}

func TestHostRenameRefreshesCachedTL1Attempts(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestTL1Fixture(t, catalog, 0)
	ctx := context.Background()
	rows, err := catalog.cachedTL1AttemptRows(ctx)
	if err != nil || len(rows) == 0 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	before, err := catalog.catalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.DB.Exec(`UPDATE hosts SET first_seen_at='earlier',last_seen_at='later'`); err != nil {
		t.Fatal(err)
	}
	if after, err := catalog.catalogVersion(ctx); err != nil || after != before {
		t.Fatalf("host observation evicted views: %d -> %d, %v", before, after, err)
	}
	if _, err = catalog.DB.Exec(`UPDATE hosts SET label='Renamed Mac'`); err != nil {
		t.Fatal(err)
	}
	updated, err := catalog.cachedTL1AttemptRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range updated {
		if row["mac"] != "Renamed Mac" {
			t.Fatal(row)
		}
	}
}
