package archive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToolRollupPublicationRetriesWithoutRebuildingTemporaryCube(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.DB.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	book, err := catalog.loadPriceBook()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := catalog.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, statement := range []string{"PRAGMA busy_timeout=1", "CREATE TEMP TABLE tool_mirror_build(workspace_id TEXT PRIMARY KEY)", "CREATE TEMP TABLE tool_cube_build AS " + toolCubeSelect("temp.tool_mirror_build")} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	locker, err := catalog.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback()
	firstFailure, completed := make(chan error, 1), make(chan error, 1)
	attempts := 0
	go func() {
		completed <- catalog.retryCatalogWrite(ctx, "test tool publication", nil, func() error {
			attempts++
			err := catalog.publishToolRollup(ctx, connection, book, "test-generation", "2026-10-01")
			if catalogBusy(err) {
				select {
				case firstFailure <- err:
				default:
				}
			}
			return err
		})
	}()
	select {
	case <-firstFailure:
	case err := <-completed:
		t.Fatalf("publication did not encounter the held writer: %v", err)
	case <-ctx.Done():
		t.Fatal("publication did not report contention")
	}
	if err := locker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil || attempts < 2 {
			t.Fatalf("publication did not retry the existing temporary cube: attempts=%d error=%v", attempts, err)
		}
	case <-ctx.Done():
		t.Fatal("publication did not resume after the writer released")
	}
	var generation string
	if err := catalog.DB.QueryRow("SELECT value FROM meta WHERE key='tool_rollup_generation'").Scan(&generation); err != nil || generation != "test-generation" {
		t.Fatalf("rollup generation was not atomically published: %q %v", generation, err)
	}
}

func TestReferenceAuditIgnoresMaterializedDeferredToolCube(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	appendSourceFile(t, source.Path, `{"type":"response_item","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"printf audit\"}","call_id":"audit-call"}}`+"\n"+
		`{"type":"response_item","timestamp":"2026-09-01T01:00:01Z","payload":{"type":"function_call_output","call_id":"audit-call","output":"audit"}}`+"\n")
	stamp := time.Now().Add(-time.Minute)
	if err := os.Chtimes(source.Path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	if err := server.Catalog.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_call_cube") == 0 {
		t.Fatal("fixture has no materialized tool cube")
	}
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "cube", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("deferred cube broke immediate reference verification: %+v", result)
	}
	if _, err := server.Catalog.DB.Exec("DELETE FROM messages_trigram"); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "corruption", automaticAuditBytes); result.Outcome != "mismatch" {
		t.Fatalf("search corruption was hidden by deferred rollup state: %+v", result)
	}
}

func TestSyncStatusExposesLowercaseSourceChoices(t *testing.T) {
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	response := httptest.NewRecorder()
	server.getIncrementalSync(response, httptest.NewRequest("GET", "/api/sync/status", nil))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sources", "available_sources"} {
		choices, ok := status[key].([]any)
		if !ok || len(choices) != 1 {
			t.Fatalf("%s has no source choices: %+v", key, status[key])
		}
		choice := choices[0].(map[string]any)
		if choice["name"] != source.Name || choice["kind"] != source.Kind || choice["enabled"] != true {
			t.Fatalf("%s does not match the Sync UI contract: %+v", key, choice)
		}
	}
}

func TestNativeAliasLookupUsesIndexedAccountAndNativeID(t *testing.T) {
	catalog, _ := testCatalog(t)
	plan, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN SELECT id,provider FROM conversations WHERE account=? AND native_id=? AND id<>?", "local", "session", "wrapper")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || !strings.Contains(firstString(plan[0]["detail"]), "SEARCH conversations USING INDEX conversations_native_alias_idx") {
		t.Fatalf("alias lookup regressed to a full conversation scan: %+v", plan)
	}
}

func TestDeferredIdentityReconciliationHonorsCancellation(t *testing.T) {
	catalog, _ := testCatalog(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := catalog.ReconcileIdentitiesContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("identity reconciliation ignored cancellation: %v", err)
	}
	if err := catalog.backfillHarnessVersionsContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("harness backfill ignored cancellation: %v", err)
	}
}

func TestVerificationIncludesDeletedConductorSessionsFromOlderCaptures(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	directory := filepath.Join(t.TempDir(), "conductor")
	execute := conductorFixture(t, directory)
	execute(addConductorSession("current", 2)...)
	execute(addConductorSession("deleted", 2)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: directory, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	runCapture(t, config)
	execute("DELETE FROM session_messages WHERE session_id='deleted'", "DELETE FROM sessions WHERE id='deleted'", "UPDATE session_messages SET content='newest evidence' WHERE id='current-m0'")
	runCapture(t, config)
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	config.Sources = nil
	server := NewServer(config, catalog)
	t.Cleanup(func() { server.stop(5 * time.Second) })
	summary := server.verifyAllInputs(t.Context(), "history", nil)
	if summary.Checked != 2 || summary.Passed != 2 || summary.Errors != 0 || summary.Deferred != 0 {
		t.Fatalf("historical verification: %+v", summary)
	}
	if _, err := catalog.DB.Exec("UPDATE messages SET text='damaged retained history' WHERE conversation_id IN (SELECT id FROM conversations WHERE native_id='conductor:deleted')"); err != nil {
		t.Fatal(err)
	}
	summary = server.verifyAllInputs(t.Context(), "history-corruption", nil)
	if summary.Checked != 2 || summary.Passed != 1 || summary.Mismatch != 1 {
		t.Fatalf("historical corruption was not detected: %+v", summary)
	}
}

func TestRetainedRepairBypassesLedgerFingerprintsAndTokenMetrics(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	appendSourceFile(t, source.Path, `{"type":"response_item","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"printf repair\"}","call_id":"repair-call"}}`+"\n"+
		`{"type":"response_item","timestamp":"2026-09-01T01:00:01Z","payload":{"type":"function_call_output","call_id":"repair-call","output":"repair"}}`+"\n"+
		`{"type":"event_msg","timestamp":"2026-09-01T01:00:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"output_tokens":40,"total_tokens":1040},"last_token_usage":{"input_tokens":1000,"output_tokens":40,"total_tokens":1040}}}}`+"\n")
	stamp := time.Now().Add(-time.Minute)
	if err := os.Chtimes(source.Path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "before", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatal(result)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_calls") == 0 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_ledger_inputs") == 0 {
		t.Fatal("fixture must contain tool calls and their no-op fingerprint")
	}
	for _, statement := range []string{
		"DELETE FROM tool_calls",
		"UPDATE metrics SET value=999999 WHERE name='input_tokens'",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('retained-repair','retained','[]','running','now','now')",
	} {
		if _, err := server.Catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(source.Path, source.Path+".held"); err != nil {
		t.Fatal(err)
	}
	if err := server.Catalog.repairRetained(t.Context(), "retained-repair", []string{source.Name}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source.Path+".held", source.Path); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "after", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("retained repair did not reconstruct the damaged derivations: %+v", result)
	}
}

func TestFullRecoveryCapturesSourceNotPreviouslyCaptured(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	if err := os.MkdirAll(server.Config().CaptureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.startRecovery(response, httptest.NewRequest("POST", "/api/sync/recovery", nil), map[string]any{"mode": "full", "sources": []any{source.Name}})
	if response.Code != 202 {
		t.Fatalf("recovery rejected a source with no capture yet: %s", response.Body.String())
	}
	var receipt struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var state, detail string
		if err := server.Catalog.DB.QueryRow("SELECT state,COALESCE(error,'') FROM sync_recovery_jobs WHERE id=?", receipt.JobID).Scan(&state, &detail); err != nil {
			t.Fatal(err)
		}
		if state != "running" {
			if state != "complete" {
				t.Fatalf("recovery failed: %s: %s", state, detail)
			}
			if manifest := readCaptureManifest(t, server.Config(), source.Name); len(manifest.Files) != 1 || manifest.Files[0].RecoveryID != receipt.JobID {
				t.Fatal("recovery did not freshly capture the new source")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery did not finish")
}

func TestAntigravityUnreadableRequestWALDoesNotAdvanceDiscovery(t *testing.T) {
	directory := t.TempDir()
	antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "request metadata"}))
	antigravityRequestRows(t, directory, antigravityRoot)
	path := filepath.Join(directory, "conversations", antigravityRoot+".db-wal")
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity", Kind: "antigravity", Path: directory}}}
	checked, emitted := false, false
	err := adapter.discoverParts(func([]sourcePart) bool { checked = true; return true }, func(*WorkspaceRecord, []sourcePart) error { emitted = true; return nil })
	if err == nil || checked || emitted {
		t.Fatalf("unreadable WAL was treated as an absent dependency: error=%v checked=%v emitted=%v", err, checked, emitted)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := adapter.discoverParts(func([]sourcePart) bool { return true }, func(*WorkspaceRecord, []sourcePart) error { return nil }); err != nil {
		t.Fatalf("discovery did not recover after dependency became readable: %v", err)
	}
}
