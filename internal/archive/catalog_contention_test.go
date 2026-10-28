package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

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
