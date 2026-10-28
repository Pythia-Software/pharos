package archive

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests of the independent audit and verification of synced inputs
// (incremental_audit.go).

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
	// The older capture is stored compressed once indexed; verification
	// decodes it into a reader lease of its own and removes that afterwards.
	if done := compressGenerations(t, config); len(done) != 1 {
		t.Fatal("indexed generation was not compressed", done)
	}
	defer func() {
		if left := temporariesUnder(t, capturedPath(config, "conductor", "")); len(left) != 0 {
			t.Error("verification left decoded copies", left)
		}
	}()
	config.Sources = nil
	server := testServer(t, config, catalog)
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
		checks, err := queryMaps(catalog.DB, "SELECT outcome,detail FROM sync_verifications")
		t.Fatalf("historical corruption was not detected: %+v; checks=%+v err=%v", summary, checks, err)
	}
}

func TestVerifyAllEndpointChecksEveryInputIncludingLargeConductorSession(t *testing.T) {
	useHost(t, "host-a")
	override(t, &automaticAuditConductorMessages, 4)
	codex := stableCodexSource(t, 3)
	directory := t.TempDir()
	execute := conductorFixture(t, directory)
	execute(addConductorSession("large", int(automaticAuditConductorMessages)+1)...)
	execute(addConductorSession("small", 2)...)
	conductor := SourceConfig{Name: "conductor", Kind: "conductor", Path: directory, Account: "local", Enabled: true}
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{codex, conductor}
	server := testServer(t, config, catalog)
	t.Cleanup(func() { server.stop(5 * time.Second) })
	ingestSource(t, catalog, codex)
	ingestSource(t, catalog, conductor)
	if _, err := catalog.DB.Exec("UPDATE metrics SET value=value+100 WHERE name='user_turns'"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/sync/verify", strings.NewReader(`{"all":true}`))
	request.Header.Set("Authorization", "Bearer "+server.Config().APIToken)
	server.ServeHTTP(response, request)
	if response.Code != 202 {
		t.Fatalf("verification was rejected: %s", response.Body.String())
	}
	var receipt struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		server.runsMu.RLock()
		var state string
		var summary verificationSummary
		for _, run := range server.runs {
			if run.ID == receipt.RunID {
				state = run.State
				if run.Verification != nil {
					summary = *run.Verification
				}
			}
		}
		server.runsMu.RUnlock()
		if state != "running" {
			if state != "complete" || summary.Checked != 3 || summary.Mismatch != 3 || summary.Errors != 0 || summary.Deferred != 0 {
				t.Fatalf("Verify all sampled or deferred retained inputs: state=%s coverage=%+v", state, summary)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Verify all did not complete")
}

func TestUnlimitedManualVerificationBypassesAutomaticSizeGate(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	unit.Bytes = 256<<20 + 1
	if result := server.Catalog.auditUnit(t.Context(), unit, "automatic", automaticAuditBytes); result.Outcome != "deferred" {
		t.Fatalf("automatic size budget was not retained: %+v", result)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "manual", 0); result.Outcome != "passed" {
		t.Fatalf("unlimited manual verification inherited a size gate: %+v", result)
	}
}

func TestIndependentAuditPreservesCrossRepositoryToolPathContext(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	appendSourceFile(t, source.Path, `{"type":"response_item","payload":{"type":"function_call","name":"read_file","arguments":"{\"path\":\"/fixture/foreign/src/example.go\"}","call_id":"cross-repository"}}`+"\n")
	stamp := time.Now().Add(-time.Minute)
	if err := os.Chtimes(source.Path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	server := automaticTestServer(t, source)
	if _, err := server.Catalog.DB.Exec(`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at)
		VALUES('foreign-repository','foreign','["/fixture/foreign"]',?,?)`, now(), now()); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, server.Catalog, source)
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_calls WHERE path_repository_id='foreign-repository' AND repo_path='src/example.go'") != 1 {
		t.Fatal("fixture did not resolve its cross-repository tool path")
	}
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "cross-repository", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("audit lost global repository context: %+v", result)
	}
	if _, err := server.Catalog.DB.Exec("UPDATE tool_calls SET status='corrupted'"); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "cross-repository-corruption", automaticAuditBytes); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "tool_calls") {
		t.Fatalf("repository context hid tool-ledger corruption: %+v", result)
	}
}
