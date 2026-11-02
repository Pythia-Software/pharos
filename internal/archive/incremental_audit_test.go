package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAutomaticAuditFallsBackAndPersistsLargerChecks(t *testing.T) {
	useHost(t, "host-a")
	server, units := stableCodexDirectory(t, 2)
	// A single oversized input beside a small one; lower the audit limit
	// instead of manufacturing thousands of conversations.
	appendSourceFile(t, units[0].ID, strings.Repeat(" ", 64<<10)+"\n")
	stamp := time.Now().Add(-time.Minute)
	if err := os.Chtimes(units[0].ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if result := ingestSource(t, server.Catalog, units[0].Source); result.Error != nil {
		t.Fatal(result.Error)
	}
	adapter, _ := MakeAdapter(units[0].Source)
	units = nil
	if err := adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
		units = append(units, unitOf(adapter.Config(), parts, false))
		return true
	}, nil); err != nil {
		t.Fatal(err)
	}
	override(t, &automaticAuditBytes, int64(64<<10))
	var verified string
	if err := server.Catalog.DB.QueryRow("SELECT verified_at FROM sync_verification_progress WHERE unit=?", units[0].ID).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	pool := newAuditPool(server.Catalog)
	pool.units = units
	result := server.Catalog.auditPool(t.Context(), pool, "fallback")
	if result.Outcome != "passed" || len(result.Attempts) != 2 || result.Attempts[0].Reason != "evidence_size" || result.Unit != units[1].ID {
		t.Fatalf("an oversized candidate prevented verification of a small one: %+v", result)
	}
	var retained string
	if err := server.Catalog.DB.QueryRow("SELECT verified_at FROM sync_verification_progress WHERE unit=?", units[0].ID).Scan(&retained); err != nil || retained != verified {
		t.Fatalf("an attempt advanced completed verification: %q -> %q (%v)", verified, retained, err)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verification_progress WHERE heavy=1") != 1 {
		t.Fatal("large check was not durably queued")
	}
	restored := newAuditPool(server.Catalog)
	restored.consider(units[0])
	if len(restored.units) != 0 {
		t.Fatal("a queued large check consumed the next quick audit")
	}
	if !server.runBackgroundPass(t.Context(), "requested", []string{"verification"}, nil) {
		t.Fatal("larger verification pass did not run")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verification_progress WHERE heavy=1") != 0 {
		t.Fatal("completed larger check remained queued")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verifications WHERE outcome='passed'") != 2 {
		t.Fatal("larger pass did not verify the queued input")
	}

	// Cancellation leaves durable work for the next pass, with no completed
	// check invented for an input the cancelled pass never read.
	if err := server.Catalog.recordVerification(units[0], auditResult{Outcome: "deferred", Reason: "evidence_size"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !server.runBackgroundPass(ctx, "requested", []string{"verification"}, nil) {
		t.Fatal("cancelled pass was not admitted")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verification_progress WHERE heavy=1") != 1 {
		t.Fatal("cancellation discarded pending verification")
	}

	for _, kind := range []string{"codex", "antigravity"} {
		t.Run("retained-"+kind, func(t *testing.T) {
			root := t.TempDir()
			if kind == "antigravity" {
				root = antigravityFixture(t)
			} else {
				writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("queued-session", 1))
				db, err := sql.Open("sqlite", filepath.Join(root, "state_1.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				execSQL(t, db, "CREATE TABLE threads(id TEXT,name TEXT);INSERT INTO threads VALUES('queued-session','Queued name')")
				db.Close()
			}
			source := SourceConfig{Name: kind, Kind: kind, Path: root, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
			server := automaticTestServer(t, source)
			override(t, &automaticAuditBytes, int64(1))
			var queued string
			func() {
				adapter, err := MakeAdapter(source)
				if err != nil {
					t.Fatal(err)
				}
				input, err := server.Catalog.acquireRetainedInput(t.Context(), adapter, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				if result := server.Catalog.IngestContext(t.Context(), input.Adapter, nil); result.Error != nil {
					t.Fatal(result.Error)
				}
				pool := newAuditPool(server.Catalog)
				if err := input.Adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
					unit := unitOf(input.Adapter.Config(), parts, false)
					unit.Reader = input.Adapter
					pool.units = append(pool.units, unit)
					return true
				}, nil); err != nil {
					t.Fatal(err)
				}
				if len(pool.units) != 1 || !strings.HasPrefix(pool.units[0].ID, "sqlite:") {
					t.Fatalf("fixture did not discover a retained database group: %+v", pool.units)
				}
				queued = pool.units[0].ID
				if result := server.Catalog.auditPool(t.Context(), pool, "retained-queue"); result.Outcome != "deferred" || result.Reason != "evidence_size" {
					t.Fatalf("retained group did not enter the larger-check queue: %+v", result)
				}
			}() // Release the original reader before the worker rediscovers it.
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verification_progress WHERE unit=? AND heavy=1", queued) != 1 {
				t.Fatal("retained group was not durably queued")
			}
			if !server.runBackgroundPass(t.Context(), "requested", []string{"verification"}, nil) {
				t.Fatal("retained group verification pass did not run")
			}
			var outcome, reason string
			var pending int
			if err := server.Catalog.DB.QueryRow(`SELECT v.outcome,p.heavy,COALESCE(p.reason,'') FROM sync_verifications v
				JOIN sync_verification_progress p USING(host_id,source_name,unit) WHERE v.unit=?`, queued).Scan(&outcome, &pending, &reason); err != nil {
				t.Fatal(err)
			}
			if outcome != "passed" || pending != 0 {
				t.Fatalf("available retained group remained pending: outcome=%s heavy=%d reason=%s", outcome, pending, reason)
			}
		})
	}
}

func TestAutomaticAuditBoundsAttemptsAndSharesCancellation(t *testing.T) {
	useHost(t, "host-a")
	server, units := stableCodexDirectory(t, 2)
	pool := newAuditPool(server.Catalog)
	pool.units = nil
	for range automaticAuditAttempts + 1 {
		unit := units[0]
		unit.Bytes = automaticAuditBytes + 1
		pool.units = append(pool.units, unit)
	}
	result := server.Catalog.auditPool(t.Context(), pool, "attempt-bound")
	if len(result.Attempts) != automaticAuditAttempts || result.Outcome != "deferred" {
		t.Fatalf("unbounded fallback: %+v", result)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	previous := parseHook
	t.Cleanup(func() { parseHook = previous })
	parseHook = func(string) { cancel() }
	pool = newAuditPool(server.Catalog)
	pool.units = units
	result = server.Catalog.auditPool(ctx, pool, "cancel-fallback")
	if len(result.Attempts) != 1 || result.Reason != "cancelled" {
		t.Fatalf("cancellation did not stop the shared sampling budget: %+v", result)
	}
}

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
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_query_facts") == 0 {
		t.Fatal("fixture has no materialized tool query facts")
	}
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "cube", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("deferred cube broke immediate reference verification: %+v", result)
	}
	// A catalog still removing the postings the old writer gave tool output
	// differs from the reference ingest only in what neither should hold.
	var output string
	if err := server.Catalog.DB.QueryRow("SELECT id FROM messages WHERE kind='tool_result' LIMIT 1").Scan(&output); err != nil {
		t.Fatalf("fixture has no tool output: %v", err)
	}
	execTx(t, server.Catalog.DB, "INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages WHERE id="+sqlQuote(output),
		"INSERT INTO message_fts_rows(message_id,fts_rowid) SELECT message_id,rowid FROM messages_fts WHERE message_id="+sqlQuote(output))
	if result := server.Catalog.auditUnit(t.Context(), unit, "legacy", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("an old tool-output posting failed reference verification: %+v", result)
	}
	// Legacy JSON null means no aliases, just like the current empty array.
	// An actual extra alias remains a detectable source-data mismatch.
	execTx(t, server.Catalog.DB, "UPDATE conversations SET aliases_json='null'")
	if result := server.Catalog.auditUnit(t.Context(), unit, "legacy-aliases", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("equivalent empty aliases failed reference verification: %+v", result)
	}
	execTx(t, server.Catalog.DB, `UPDATE conversations SET aliases_json='["unexpected-alias"]'`)
	if result := server.Catalog.auditUnit(t.Context(), unit, "alias-corruption", automaticAuditBytes); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "conversations") {
		t.Fatalf("alias corruption was hidden: %+v", result)
	}
	var toolName string
	if err := server.Catalog.DB.QueryRow("SELECT tool_name FROM tool_calls LIMIT 1").Scan(&toolName); err != nil {
		t.Fatal(err)
	}
	execTx(t, server.Catalog.DB, "UPDATE conversations SET aliases_json='[]'", "UPDATE tool_calls SET tool_name='damaged-tool'")
	if result := server.Catalog.auditUnit(t.Context(), unit, "tool-corruption", automaticAuditBytes); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "tool_calls") {
		t.Fatalf("tool corruption was hidden by deferred query facts: %+v", result)
	}
	execTx(t, server.Catalog.DB, "UPDATE tool_calls SET tool_name="+sqlQuote(toolName))
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
		// The capture lock lives beside the host's sources, never in one.
		if _, err := os.Stat(capturedPath(config, "conductor", captureLockName)); !os.IsNotExist(err) {
			t.Error("verification locked a capture lock inside a source directory", err)
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
