package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type heldQueryResponse struct {
	*httptest.ResponseRecorder
	started chan struct{}
	release chan struct{}
}

func (w *heldQueryResponse) Write(body []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
		<-w.release
	}
	return w.ResponseRecorder.Write(body)
}

func waitForIndexPause(t *testing.T, c *Catalog) {
	t.Helper()
	waitUntil(t, 5*time.Second, "indexing to yield to the query", func() bool {
		c.queries.mu.Lock()
		defer c.queries.mu.Unlock()
		return c.queries.waiting > 0
	})
}

func TestInteractiveReadDemand(t *testing.T) {
	for _, route := range []struct {
		method, path string
		query        bool
	}{
		{http.MethodPost, "/api/query/library/rows-v2", true},
		{http.MethodPost, "/api/query/usage/metrics", true},
		{http.MethodPost, "/api/query/library/query-v3", true},
		{http.MethodGet, "/api/search", true},
		{http.MethodGet, "/api/library/find", true},
		{http.MethodPost, "/api/search/text", true},
		{http.MethodGet, "/api/work/id/conversations/id", true},
		{http.MethodGet, "/api/usage/parallelism", true},
		{http.MethodGet, "/api/status", false},
		{http.MethodGet, "/api/operations", false},
		{http.MethodGet, "/api/menu-bar/usage", false},
		{http.MethodGet, "/api/ready", false},
		{http.MethodGet, "/assets/query-tables.js", false},
		{http.MethodPost, "/api/sync/run", false},
		{http.MethodPost, "/api/query-computed-columns", false},
	} {
		if got := interactiveRead(httptest.NewRequest(route.method, route.path, nil)); got != route.query {
			t.Errorf("%s %s: query demand=%t, want %t", route.method, route.path, got, route.query)
		}
	}
}

func TestQueryPausingPreference(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	// Toggle the rule while the same query stays active throughout.
	defer catalog.beginInteractiveRead(t.Context())()
	for _, step := range []struct {
		name, body string
		on         bool
	}{
		{"default off", "", false},
		{"enable", `{"set":{"pharos-pause-indexing-for-queries-v1":true}}`, true},
		{"disable", `{"set":{"pharos-pause-indexing-for-queries-v1":false}}`, false},
		{"enable again", `{"set":{"pharos-pause-indexing-for-queries-v1":true}}`, true},
		{"nonboolean is off", `{"set":{"pharos-pause-indexing-for-queries-v1":"true"}}`, false},
		{"enable before delete", `{"set":{"pharos-pause-indexing-for-queries-v1":true}}`, true},
		{"delete is off", `{"delete":["pharos-pause-indexing-for-queries-v1"]}`, false},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.body != "" {
				if response := serveTestBody(server, http.MethodPost, "/api/preferences", step.body); response.Code != http.StatusOK {
					t.Fatalf("save: %d %s", response.Code, response.Body.String())
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- catalog.queries.wait(ctx) }()
			if step.on {
				waitForIndexPause(t, catalog)
				cancel()
			}
			select {
			case err := <-done:
				if step.on && !errors.Is(err, context.Canceled) || !step.on && err != nil {
					t.Fatalf("index wait: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("index failed to progress with query pausing off")
			}
		})
	}

	// A fresh service loads the saved opt-in before admitting its first read.
	if err := catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{pauseIndexingForQueriesFlag: true}}); err != nil {
		t.Fatal(err)
	}
	other, err := OpenCatalog(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	defer other.beginInteractiveRead(t.Context())()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- other.queries.wait(ctx) }()
	waitForIndexPause(t, other)
	// A different process's saved toggle is applied by the flag watcher too.
	if err := catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{pauseIndexingForQueriesFlag: false}}); err != nil {
		t.Fatal(err)
	}
	other.flags.invalidate()
	watchCtx, stop := context.WithCancel(t.Context())
	watched := make(chan struct{})
	go func() { defer close(watched); other.watchFeatureFlags(watchCtx) }()
	defer func() { stop(); <-watched }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not release paused work after disabling")
	}
}

func TestInteractiveQueriesPauseIngest(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint("enabled=", enabled), func(t *testing.T) {
			catalog, config := testCatalog(t)
			if enabled {
				if err := catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{pauseIndexingForQueriesFlag: true}}); err != nil {
					t.Fatal(err)
				}
			}
			server := testServer(t, config, catalog)
			path := filepath.Join(t.TempDir(), "export.json")
			if err := os.WriteFile(path, []byte(`{"workspaces":[{"id":"first","title":"First workspace","conversations":[{"id":"first-thread","provider":"codex","messages":[{"id":"one","role":"user","text":"hello"}]}]},{"id":"second","title":"Second workspace","conversations":[{"id":"second-thread","provider":"codex","messages":[{"id":"two","role":"user","text":"world"}]}]}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			adapter, err := MakeAdapter(SourceConfig{Name: "fixture", Kind: "canonical", Path: path, Account: "local", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			firstRecord, continueIndex := make(chan struct{}), make(chan struct{})
			first := true
			work := withIngestMode(ctx, ingestMode{written: func(string) {
				if !first {
					return
				}
				first = false
				close(firstRecord)
				select {
				case <-continueIndex:
				case <-ctx.Done():
				}
			}})
			indexed := make(chan IngestResult, 1)
			indexDone := make(chan struct{})
			go func() { defer close(indexDone); indexed <- catalog.IngestContext(work, adapter, nil) }()
			defer func() { cancel(); <-indexDone }()
			select {
			case <-firstRecord:
			case result := <-indexed:
				t.Fatalf("index stopped before the first record: %+v", result)
			case <-time.After(5 * time.Second):
				t.Fatal("first record did not commit")
			}

			// A query arriving after the first commit must pause the next record
			// without retaining either the writer gate or SQLite's write lock.
			query := httptest.NewRequest(http.MethodPost, "/api/query/library/rows-v2", strings.NewReader(`{"version":2,"profile":"qt-sqlite-v1","limit":5}`))
			query.Header.Set("Authorization", "Bearer "+config.APIToken)
			w := &heldQueryResponse{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
			queryDone := make(chan struct{})
			go func() { defer close(queryDone); server.ServeHTTP(w, query) }()
			defer func() { close(w.release); <-queryDone }()
			select {
			case <-w.started:
			case <-time.After(5 * time.Second):
				t.Fatal("query did not start")
			}
			close(continueIndex)
			if !enabled {
				select {
				case result := <-indexed:
					if result.Error != nil || result.Workspaces != 2 {
						t.Fatalf("index with pausing off: %+v", result)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("default setting blocked indexing while a query was running")
				}
				return
			}
			waitForIndexPause(t, catalog)
			writeCtx, stopWrites := context.WithTimeout(t.Context(), 5*time.Second)
			defer stopWrites()
			if err := catalog.writeTransaction(writeCtx, "interactive write during index pause", func(tx *sql.Tx) error {
				_, err := tx.ExecContext(writeCtx, "INSERT INTO meta(key,value) VALUES('local-during-pause','ok')")
				return err
			}); err != nil {
				t.Fatalf("local writer blocked by paused index: %v", err)
			}
			other, err := sql.Open("sqlite", "file:"+catalog.Path+"?_pragma=busy_timeout(20)")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if _, err := other.ExecContext(writeCtx, "INSERT INTO meta(key,value) VALUES('independent-during-pause','ok')"); err != nil {
				t.Fatalf("independent SQLite writer blocked by paused index: %v", err)
			}
			if err := catalog.SetUIPreferences(writeCtx, map[string]any{"set": map[string]any{"pharos-theme": "dark"}}); err != nil {
				t.Fatalf("preference save blocked by paused index: %v", err)
			}
			if response := serveTestBody(server, http.MethodGet, "/api/search", ""); response.Code != http.StatusOK {
				t.Fatalf("read during paused index: %d %s", response.Code, response.Body.String())
			}
			waitForIndexPause(t, catalog)
			cancel()
			select {
			case result := <-indexed:
				if result.Error == nil || result.Workspaces != 1 {
					t.Fatalf("cancelled index did not retain exactly the committed record: %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled index still waiting for a query")
			}
		})
	}
}

func TestIndexCompletesAtomicWorkspaceDuringQuery(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", stop), func(t *testing.T) {
			catalog, _ := testCatalog(t)
			if err := catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{pauseIndexingForQueriesFlag: true}}); err != nil {
				t.Fatal(err)
			}
			writeIncrementalFixture(t, catalog, incrementalFixture(1))
			ctx, cancel := context.WithCancel(t.Context())
			inRecord, continueWriting := make(chan struct{}), make(chan struct{})
			written := make(chan error, 1)
			writeDone := make(chan struct{})
			go func() {
				defer close(writeDone)
				tx, finish, err := catalog.beginTrackedWrite(ctx, "atomic ingest during query")
				if err != nil {
					written <- err
					return
				}
				defer func() { _ = tx.Rollback(); finish() }()
				if _, _, err = ingestCopyModeContext(ctx, tx, incrementalFixture(2), false, currentHost().ID, "fixture", false, false); err != nil {
					written <- err
					return
				}
				close(inRecord)
				select {
				case <-continueWriting:
				case <-ctx.Done():
				}
				_, _, err = ingestCopyModeContext(ctx, tx, incrementalFixture(3), false, currentHost().ID, "fixture", false, false)
				if err == nil {
					err = tx.Commit()
				}
				written <- err
			}()
			defer func() { cancel(); <-writeDone }()
			select {
			case <-inRecord:
			case err := <-written:
				t.Fatalf("record failed before the query started: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("record did not start writing")
			}
			defer catalog.beginInteractiveRead(t.Context())()
			var count int
			if err := catalog.reader().QueryRow("SELECT COUNT(*) FROM messages").Scan(&count); err != nil || count != 1 {
				t.Fatalf("query did not read the previous atomic record: count=%d err=%v", count, err)
			}
			want := 3
			if stop {
				cancel()
				want = 1
			}
			close(continueWriting)
			select {
			case err := <-written:
				if stop && !errors.Is(err, context.Canceled) || !stop && err != nil {
					t.Fatalf("index result: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("an open record waited for the active query")
			}
			<-writeDone
			if err := catalog.reader().QueryRow("SELECT COUNT(*) FROM messages").Scan(&count); err != nil || count != want {
				t.Fatalf("atomic index result: count=%d want=%d err=%v", count, want, err)
			}
			if stop {
				writeIncrementalFixture(t, catalog, incrementalFixture(3))
				if err := catalog.reader().QueryRow("SELECT COUNT(*) FROM messages").Scan(&count); err != nil || count != 3 {
					t.Fatalf("resumed record: count=%d err=%v", count, err)
				}
			}
		})
	}
}

func TestIngestRetriesCatalogWriterContention(t *testing.T) {
	catalog, _ := testCatalog(t)
	// Give ingestion one connection with a short timeout so this test can
	// exercise retries without waiting for the production five seconds.
	catalog.DB.SetMaxOpenConns(1)
	catalog.DB.SetMaxIdleConns(1)
	if _, err := catalog.DB.Exec("PRAGMA busy_timeout=20"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(path, []byte(`{"workspaces":[{"id":"work","title":"Test","conversations":[{"id":"thread","provider":"codex","messages":[{"id":"one","role":"user","text":"hello"}]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "fixture", Kind: "canonical", Path: path, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+catalog.Path+"?_pragma=busy_timeout(20)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var held *sql.Tx
	waiting := false
	released := make(chan error, 1)
	result := catalog.IngestContext(context.Background(), adapter, func(phase string, _, _, _, _, _ int) {
		if phase == "waiting for catalog" {
			waiting = true
		}
		if phase != "indexing" || held != nil {
			return
		}
		var err error
		held, err = other.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := held.Exec("INSERT INTO meta(key,value) VALUES('contention-test','1')"); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			released <- held.Commit()
		}()
	})
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if result.Error != nil || result.Workspaces != 1 || !waiting {
		t.Fatalf("ingest after transient writer conflict: %+v, waiting=%t", result, waiting)
	}
	var count int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspaces WHERE source_kind='canonical'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("workspaces=%d, error=%v", count, err)
	}
}

func TestCatalogWriterSummaryNamesLocalWriter(t *testing.T) {
	catalog, _ := testCatalog(t)
	tx, finish, err := catalog.beginTrackedWrite(context.Background(), "test-writer")
	if err != nil {
		t.Fatal(err)
	}
	if summary := catalog.writerSummary(); !strings.Contains(summary, "test-writer") {
		t.Fatalf("missing active writer: %s", summary)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	finish()
	if summary := catalog.writerSummary(); !strings.Contains(summary, "active_writers=[none tracked") || !strings.Contains(summary, "another Mac") {
		t.Fatalf("stale or misleading writer summary: %s", summary)
	}
}

// A sync holds the catalog writer for seconds at a time. Every page read and
// MCP tool answers from what is stored, leaving projections and history to
// background work, so none of them waits for the writer.
func TestRequestReadsDoNotWaitForTheCatalogWriter(t *testing.T) {
	catalog, config := testCatalog(t)
	ingestMCPFixture(t, catalog)
	seedToolCalls(t, catalog, 20)
	var conversation, workspace string
	if err := catalog.DB.QueryRow("SELECT id,workspace_id FROM conversations WHERE workspace_id IN (SELECT id FROM workspaces WHERE title='Parser maintenance')").Scan(&conversation, &workspace); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A sync's commits leave every projection stale, and a cloud backup this
	// Mac was running has lost its process.
	interrupted := jsonText(cloudRun{ID: "lost-run", DestinationID: "cloud", State: "running", OwnerPID: 999999999, OwnerHost: currentHost().ID, UpdatedAt: "earlier"})
	execTx(t, catalog.DB, "UPDATE origin_state SET revision=revision+1",
		"INSERT OR IGNORE INTO parallelism_dirty SELECT id,'' FROM conversations",
		"INSERT OR IGNORE INTO mcp_metric_dirty SELECT id FROM workspaces",
		"INSERT OR IGNORE INTO tool_rollup_dirty SELECT id,'stale' FROM workspaces",
		`INSERT INTO cloud_destinations(id,config_json,credential_json,imported_at) VALUES('cloud','{"name":"Backup"}','{}','2026-10-27')`,
		`INSERT INTO cloud_backup_runs(id,destination_id,status_json,created_at) VALUES('lost-run','cloud','`+interrupted+`','2026-10-27')`)
	quietDriveCache(t)
	t.Cleanup(func() { quietDriveCache(t) })
	server := testServer(t, config, catalog)

	// Hold the writer as a sync does: SQLite's lock, from another connection,
	// and this process's write gate.
	ctx := context.Background()
	holder, err := sql.Open("sqlite", catalogDSN(catalog.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	lock, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	gate, err := catalog.acquireWriteGate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			gate()
			if _, err := lock.ExecContext(ctx, "ROLLBACK"); err != nil {
				t.Error(err)
			}
		}
	}
	defer release()

	rows := `{"version":2,"profile":"qt-sqlite-v1","limit":5}`
	count := `{"version":2,"profile":"qt-sqlite-v1","limit":5,"metrics":[{"id":"n","op":"count"}]}`
	requests := []struct{ method, path, body string }{
		{"GET", "/api/health/counts", ""}, {"GET", "/api/health/pricing", ""}, {"GET", "/api/health/freshness", ""}, {"GET", "/api/library/summary", ""},
		{"GET", "/api/authorship", ""}, {"GET", "/api/pricing", ""}, {"GET", "/api/origins", ""},
		{"GET", "/api/usage/history", ""}, {"GET", "/api/usage/parallelism", ""}, {"GET", "/api/usage/summary", ""},
		{"GET", "/api/menu-bar/usage", ""}, {"GET", "/api/tools/status", ""}, {"GET", "/api/sources", ""},
		// The first shows the lost run as failed and stores that in the
		// background, which waits for the writer; the next does not.
		{"GET", "/api/cloud-backup", ""}, {"GET", "/api/cloud-backup", ""}, {"GET", "/api/mcp/calls", ""}, {"GET", "/api/search?q=parser", ""},
		{"POST", "/api/search/text", `{"query":"parser"}`}, {"GET", "/api/work/" + workspace, ""}, {"GET", "/api/findings", ""},
		{"GET", "/api/tl1", ""}, {"GET", "/api/preferences", ""}, {"GET", "/api/sync/status", ""},
		// The operations snapshot reads the registry and the journal, which
		// the low-priority lane writes (§6.2).
		{"GET", "/api/operations", ""},
		// The status snapshot, and its fresh check on the release path.
		{"GET", "/api/status", ""}, {"GET", "/api/status?fresh=1", ""},
	}
	for _, dataset := range []string{"library", "usage", "tools", "tool_calls", "writing", "writing_messages", "parallelism",
		"conversation_activity", "activity", "mcp_calls", "skill_usages", "tl1_attempts", "findings"} {
		requests = append(requests, struct{ method, path, body string }{"POST", "/api/query/" + dataset + "/rows-v2", rows},
			struct{ method, path, body string }{"POST", "/api/query/" + dataset + "/metrics", count})
	}
	tools := []struct {
		name string
		args map[string]any
	}{
		{"search_conversations", map[string]any{"query": "parser"}},
		{"get_conversation_overview", map[string]any{"conversation_id": conversation, "include_metrics": true, "max_output_tokens": 4000}},
		{"search_messages", map[string]any{"query": "parser"}},
		{"search_text", map[string]any{"query": "parser"}},
		{"search_work", map[string]any{"query": "parser"}},
		{"get_work_detail", map[string]any{"workspace_id": workspace}},
		{"get_conversation_messages", map[string]any{"conversation_id": conversation}},
		{"trace_worktree", map[string]any{"query": "parser"}},
		{"query_statistics", map[string]any{"entity": "conversation", "select": []string{"total_tokens", "user_turn_count"}}},
		{"list_repositories", map[string]any{}},
		{"get_archive_status", map[string]any{}},
	}
	for id, tool := range tools {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool.name, "arguments": tool.args}})
		requests = append(requests, struct{ method, path, body string }{"POST", "/api/mcp/rpc", string(body)})
	}
	for _, request := range requests {
		read, cancel := context.WithTimeout(ctx, 3*time.Second)
		r := httptest.NewRequestWithContext(read, request.method, request.path, strings.NewReader(request.body))
		r.Header.Set("Authorization", "Bearer "+config.APIToken)
		w := httptest.NewRecorder()
		// Work that ignores the request's context would wait for the writer
		// indefinitely; it finishes once the writer is released below.
		answered := waitFor(2*time.Second, func() { server.ServeHTTP(w, r) })
		cancel()
		if !answered {
			t.Errorf("%s %s %.300s waited for the writer", request.method, request.path, request.body)
			continue
		}
		if answer := w.Body.String(); w.Code != http.StatusOK || strings.Contains(answer, `"isError":true`) {
			t.Errorf("%s %s %.300s while the writer was held: %d: %.300s", request.method, request.path, request.body, w.Code, answer)
		} else if request.path == "/api/cloud-backup" && !strings.Contains(answer, `"phase":"interrupted"`) {
			t.Errorf("the Cloud page did not show the lost run as interrupted: %.500s", answer)
		}
	}

	// The storage breakdown is measured meanwhile: its sizes and shares do not
	// wait for the size history it records.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		r := httptest.NewRequest(http.MethodGet, "/api/health/storage/breakdown", nil)
		r.Header.Set("Authorization", "Bearer "+config.APIToken)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), `"status":"measured"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the storage breakdown was not measured while the writer was held: %d %.300s", w.Code, w.Body.String())
		}
	}
	// That refresh is still waiting to record the size history, so a poll
	// after the sizes go stale starts no second walk of the library.
	drives.mu.Lock()
	stale := *drives.state
	stale.checked = time.Now().Add(-2 * driveFactsTTL)
	drives.state = &stale
	drives.mu.Unlock()
	if _, refresh := drives.cached(config); refresh {
		t.Error("a second drive refresh started while the first still waited for the writer")
	}

	// Meanwhile those background writes are hidden operations that keep the
	// drive from being safe to eject.
	verdict := server.ejectVerdictNow(ctx, false)
	for _, kind := range []string{"cloud.reconcile", "maintenance.mcp-history"} {
		if verdict.Verdict == verdictSafe || !slices.ContainsFunc(verdict.Blockers, func(b ejectBlocker) bool { return b.Kind == kind }) {
			t.Errorf("%s waiting for the writer does not count for the verdict: %+v", kind, verdict)
		}
	}

	// History and the lost run wait for the writer, then are stored.
	release()
	if !waitFor(10*time.Second, func() {
		for countRows(t, catalog, `SELECT COUNT(*) FROM cloud_backup_runs WHERE json_extract(status_json,'$.state')='failed'`) == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}) {
		t.Fatal("the interrupted run was not stored once the writer was free")
	}
	if !waitFor(10*time.Second, func() {
		for mcpCalls(t, catalog) < len(tools) {
			time.Sleep(20 * time.Millisecond)
		}
	}) {
		t.Fatalf("recorded %d of %d MCP calls once the writer was free", mcpCalls(t, catalog), len(tools))
	}
}

func mcpCalls(t *testing.T, catalog *Catalog) int {
	t.Helper()
	var count int
	if err := catalog.reader().QueryRow("SELECT COUNT(*) FROM mcp_calls").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
