package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAuditDefersChangesWaitingForIndex(t *testing.T) {
	for _, change := range []string{"append", "subagent", "name"} {
		t.Run(change, func(t *testing.T) {
			useHost(t, "host-a")
			root := t.TempDir()
			source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local", Enabled: true}
			path := filepath.Join(root, "session.jsonl")
			writeSourceFile(t, path, claudeLine("session", "first", "original question", "", 1))
			server := automaticTestServer(t, source)
			if result := ingestSource(t, server.Catalog, source); result.Error != nil {
				t.Fatal(result.Error)
			}
			initial := firstSyncUnit(t, server.Catalog, source)
			if err := server.Catalog.recordVerification(initial, server.Catalog.auditUnit(t.Context(), initial, "initial", 0)); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "append":
				appendSourceFile(t, path, claudeLine("session", "second", "another question", "", 2))
			case "subagent":
				writeSourceFile(t, filepath.Join(root, "session", "subagents", "agent-child.jsonl"), claudeLine("session", "child", "agent assignment", "", 2))
			case "name":
				writeSourceFile(t, filepath.Join(root, "sessions-index.json"), `{"entries":[{"sessionId":"session","summary":"A new source title"}]}`)
			}
			unit := firstSyncUnit(t, server.Catalog, source)
			// A queued large check rediscovers current evidence independently of
			// indexing. Stable new evidence must wait for the index to catch up.
			if err := server.Catalog.recordVerification(initial, auditResult{Outcome: "deferred", Reason: "evidence_size"}); err != nil {
				t.Fatal(err)
			}
			if !server.runBackgroundPass(t.Context(), "requested", []string{"verification"}, nil) {
				t.Fatal("queued verification did not run")
			}
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues") != 0 {
				t.Fatal("pending input changes were reported as corruption")
			}
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verifications WHERE outcome='deferred'") != 1 {
				t.Fatal("pending input was incorrectly certified")
			}
			if result := ingestSource(t, server.Catalog, source); result.Error != nil {
				t.Fatal(result.Error)
			}
			if result := server.Catalog.auditUnit(t.Context(), unit, "indexed", 0); result.Outcome != "passed" {
				t.Fatalf("indexed input did not pass verification: %+v", result)
			}
			if _, err := server.Catalog.DB.Exec("UPDATE messages SET text='corrupted text' WHERE native_id='first'"); err != nil {
				t.Fatal(err)
			}
			if result := server.Catalog.auditUnit(t.Context(), unit, "corrupt", 0); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "messages") {
				t.Fatalf("current indexed input failed to expose corruption: %+v", result)
			}
		})
	}
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprintf("ledger-upgrade/repair=%t", repair), func(t *testing.T) {
			useHost(t, "host-a")
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeSourceFile(t, path, claudeLine("session", "question", "question", "", 1)+
				`{"type":"assistant","uuid":"answer","sessionId":"session","timestamp":"2026-09-01T00:02:00Z","message":{"id":"request","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":10,"output_tokens":2}}}`+"\n")
			writeSourceFile(t, filepath.Join(filepath.Dir(path), "zzz-other.jsonl"), claudeLine("other", "other-question", "unrelated question", "", 1))
			source := SourceConfig{Name: "claude", Kind: "claude", Path: filepath.Dir(path), Account: "local", Enabled: true}
			server := automaticTestServer(t, source)
			ingestSource(t, server.Catalog, source)
			unit := firstSyncUnit(t, server.Catalog, source)
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM model_requests WHERE source_message_id IS NOT NULL") != 1 {
				t.Fatal("fixture has no request message evidence")
			}
			// Another input may share this workspace. Its old ledger must not
			// block or be upgraded by verification of this selected input.
			execTx(t, server.Catalog.DB, "UPDATE model_requests SET source_message_id=NULL,final_message_id=NULL",
				"UPDATE tool_ledger_state SET version='tools-v12-inference-speed-policy'")
			openIssueFoundBy(t, server, unit, "old-verifier")
			if result := server.Catalog.auditUnit(t.Context(), unit, "upgrade-pending", 0); result.Outcome != "deferred" || result.Reason != "derivation_pending" {
				t.Fatalf("pending upgrade reported corruption: %+v", result)
			}
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
				t.Fatal("deferral dismissed an unverified issue")
			}
			if !repair {
				if _, err := server.Catalog.BackfillToolLedger(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			}
			// A library that has caught up must acquire new scheduled work
			// when repair upgrades a ledger without reindexing its input.
			execTx(t, server.Catalog.DB, "UPDATE sync_deferred SET completed_generation=generation")
			result := server.Catalog.auditUnitMode(t.Context(), unit, "upgrade-complete", 0, repair)
			if result.Outcome != "passed" || result.Reindexed != 0 {
				t.Fatalf("upgraded ledger failed verification: %+v", result)
			}
			if repair && countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_ledger_state s JOIN conversations c ON c.id=s.conversation_id WHERE c.native_id='other' AND s.version='tools-v12-inference-speed-policy'") != 1 {
				t.Fatal("verification upgraded another input's ledger")
			}
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 0 {
				t.Fatal("verified upgrade did not resolve its issue")
			}
			if repair {
				for _, stage := range []string{"tools", "findings"} {
					if pending, err := deferredProbe(server.Catalog, stage)(t.Context()); err != nil || !pending {
						t.Fatalf("ledger upgrade did not queue %s: pending=%t err=%v", stage, pending, err)
					}
				}
				execTx(t, server.Catalog.DB, "UPDATE sync_deferred SET completed_generation=generation")
				if result := server.Catalog.auditUnitMode(t.Context(), unit, "already-upgraded", 0, true); result.Outcome != "passed" || result.Reindexed != 0 {
					t.Fatalf("healthy repair: %+v", result)
				}
				if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation") != 0 {
					t.Fatal("healthy repair queued unnecessary analyses")
				}
			}
			execTx(t, server.Catalog.DB, "UPDATE model_requests SET source_message_id=NULL")
			if result := server.Catalog.auditUnit(t.Context(), unit, "current-ledger-corruption", 0); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "model_requests") {
				t.Fatalf("current ledger corruption was hidden: %+v", result)
			}
			// Upgrading a ledger must still compare the retained source and
			// detect unrelated corruption before resolving this input.
			execTx(t, server.Catalog.DB, "UPDATE tool_ledger_state SET version='tools-v12-inference-speed-policy'",
				"UPDATE messages SET text='corrupted question' WHERE native_id='question'")
			if result := server.Catalog.auditUnitMode(t.Context(), unit, "upgrade-with-corruption", 0, true); result.Outcome != "passed" || result.Reindexed != 1 {
				t.Fatalf("ledger upgrade hid a genuine source mismatch: %+v", result)
			}
		})
	}
}

func TestAuditReferenceCatalogsAreDisposableAndIsolated(t *testing.T) {
	useDurableCatalog(t)
	first, err := openAuditReference(filepath.Join(t.TempDir(), "first.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.DB.Exec("INSERT INTO meta(key,value) VALUES('audit-isolation','only-first')"); err != nil {
		t.Fatal(err)
	}
	second, err := openAuditReference(filepath.Join(t.TempDir(), "second.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if countRows(t, second, "SELECT COUNT(*) FROM meta WHERE key='audit-isolation'") != 0 {
		t.Fatal("reference comparisons shared state")
	}
	for _, reference := range []*Catalog{first, second} {
		for pragma, want := range map[string]int{"synchronous": 0, "fullfsync": 0, "checkpoint_fullfsync": 0, "foreign_keys": 1} {
			var got int
			if err := reference.DB.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("scratch %s=%d, want %d: %v", pragma, got, want, err)
			}
		}
	}
	// Scratch settings must never change the production connection defaults.
	library, _ := testCatalog(t)
	var synchronous, flush int
	if err := library.DB.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := library.DB.QueryRow("PRAGMA fullfsync").Scan(&flush); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 || flush != 1 {
		t.Fatalf("library durability weakened: synchronous=%d fullfsync=%d", synchronous, flush)
	}
}

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
	_, op := server.begin(ctx, OperationSpec{Kind: "index.verify", Trigger: triggerUser})
	err := server.verifyQueued(ctx, op)
	op.Finish(err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation was reported as a completed batch: %v", err)
	}

	for _, problem := range []string{"budget", "read-error"} {
		t.Run(problem, func(t *testing.T) {
			server, units := stableCodexDirectory(t, 1)
			unit := units[0]
			if err := server.Catalog.recordVerification(unit, auditResult{Outcome: "deferred", Reason: "evidence_size"}); err != nil {
				t.Fatal(err)
			}
			var verified string
			if err := server.Catalog.DB.QueryRow("SELECT verified_at FROM sync_verification_progress WHERE unit=?", unit.ID).Scan(&verified); err != nil {
				t.Fatal(err)
			}
			if problem == "budget" {
				// Exhaust the batch before discovery, without a timing-dependent
				// fixture or waiting thirty seconds for a large database scan.
				override(t, &heavyAuditWall, time.Duration(0))
			} else if err := os.RemoveAll(unit.Source.Path); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if !server.runBackgroundPass(t.Context(), "requested", []string{"verification"}, nil) {
				t.Fatal("queued verification did not run")
			}
			var problemText, next, reason, retained string
			if err := server.Catalog.DB.QueryRow("SELECT COALESCE(error,'') FROM background_runs WHERE item='verification'").Scan(&problemText); err != nil {
				t.Fatal(err)
			}
			if (problemText != "") != (problem == "read-error") {
				t.Fatalf("batch %s failure: %q", problem, problemText)
			}
			if err := server.Catalog.DB.QueryRow("SELECT next_at,reason,verified_at FROM sync_verification_progress WHERE unit=? AND heavy=1", unit.ID).Scan(&next, &reason, &retained); err != nil {
				t.Fatal(err)
			}
			wantReason := "time_budget"
			if problem == "read-error" {
				wantReason = "source_unavailable"
			}
			if retry, ok := parseTime(next); !ok || !retry.After(started) || reason != wantReason || retained != verified {
				t.Fatalf("unfinished check lost its retry or completed coverage: next=%q reason=%q verified=%q (was %q)", next, reason, retained, verified)
			}
		})
	}

	for _, kind := range []string{"codex", "antigravity", "conductor"} {
		t.Run("retained-"+kind, func(t *testing.T) {
			root := t.TempDir()
			if kind == "antigravity" {
				root = antigravityFixture(t)
			} else if kind == "conductor" {
				conductorFixture(t, root)(addConductorSession("queued-session", 2)...)
			} else {
				writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("queued-session", 1))
				db, err := sql.Open("sqlite", filepath.Join(root, "state_1.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				execSQL(t, db, "CREATE TABLE threads(id TEXT,name TEXT);INSERT INTO threads VALUES('queued-session','Queued name')")
				db.Close()
			}
			// Conductor's live adapter exercises queued session selection;
			// the other sources exercise retained logical group identities.
			source := SourceConfig{Name: kind, Kind: kind, Path: root, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": kind != "conductor"}}
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

// The pool keeps 64 candidates, open mismatches first, then the least
// recently verified, and one from every source however many another has.
func TestAuditPoolRanksAndKeepsEverySource(t *testing.T) {
	pool := &auditPool{checks: map[string]string{}, outcomes: map[string]string{}, issues: map[string]auditIssue{}, retries: map[string]bool{},
		versions: map[string]string{}, progress: map[string]auditProgress{}, sourceAttempts: map[string]string{}}
	busy, quiet := SourceConfig{Name: "busy", Kind: "claude"}, SourceConfig{Name: "quiet", Kind: "claude"}
	old := time.Now().Add(-time.Hour).UnixNano()
	for index := range 80 {
		pool.checks[fmt.Sprintf("busy/u%02d", index)] = fmt.Sprintf("2026-10-%02d", 80-index)
	}
	pool.checks["quiet/last"] = "2026-12-31"
	for index := range 80 {
		pool.consider(unitOf(busy, []sourcePart{{item: fmt.Sprintf("u%02d", index), size: 1, version: old}}, false))
	}
	pool.consider(unitOf(quiet, []sourcePart{{item: "last", size: 1, version: old}}, false))
	if len(pool.units) != 64 {
		t.Fatalf("pool holds %d units", len(pool.units))
	}
	if pool.units[0].ID != "u79" || !slices.ContainsFunc(pool.units, func(unit syncUnit) bool { return unit.Source.Name == "quiet" }) {
		t.Fatalf("pool order starts with %s; holds quiet: %v", pool.units[0].ID, slices.ContainsFunc(pool.units, func(unit syncUnit) bool { return unit.Source.Name == "quiet" }))
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
	summary = server.verifyInputs(t.Context(), "history-repair", nil, nil, true)
	if summary.Checked != 2 || summary.Passed != 2 || summary.Reindexed != 1 || summary.Errors != 0 || summary.Deferred != 0 || summary.Mismatch != 0 {
		t.Fatalf("offline historical repair: %+v", summary)
	}
}

func TestVerificationRepairsOnlyFreshMismatches(t *testing.T) {
	useHost(t, "host-a")
	server, units := stableCodexDirectory(t, 3)
	for _, unit := range units {
		openIssueFoundBy(t, server, unit, "old-verifier")
	}
	const untouched = "2000-01-01T00:00:00.000Z"
	execTx(t, server.Catalog.DB, "UPDATE workspaces SET indexed_at="+sqlQuote(untouched))
	var damaged string
	if err := server.Catalog.DB.QueryRow("SELECT id FROM workspaces ORDER BY id LIMIT 1").Scan(&damaged); err != nil {
		t.Fatal(err)
	}
	execTx(t, server.Catalog.DB,
		"UPDATE messages SET text='broken transcript' WHERE conversation_id IN (SELECT id FROM conversations WHERE workspace_id="+sqlQuote(damaged)+")",
		"UPDATE metrics SET value=999999 WHERE workspace_id="+sqlQuote(damaged)+" AND name='user_turns'",
	)
	// Verification alone dismisses obsolete alerts and makes no repair writes.
	summary := server.verifyAllInputs(t.Context(), "recheck", nil)
	if summary.Checked != 3 || summary.Passed != 2 || summary.Mismatch != 1 || summary.Reindexed != 0 || summary.Errors != 0 {
		t.Fatalf("recheck: %+v", summary)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM workspaces WHERE indexed_at="+sqlQuote(untouched)) != 3 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
		t.Fatal("recheck rewrote an input or left obsolete issues open")
	}
	summary = server.verifyInputs(t.Context(), "selective-repair", nil, nil, true)
	if summary.Checked != 3 || summary.Passed != 3 || summary.Reindexed != 1 || summary.Mismatch != 0 || summary.Errors != 0 || summary.Deferred != 0 {
		checks, _ := queryMaps(server.Catalog.DB, "SELECT outcome,detail FROM sync_verifications")
		t.Fatalf("selective repair: %+v; checks=%+v", summary, checks)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM workspaces WHERE indexed_at="+sqlQuote(untouched)) != 2 {
		t.Fatal("repair rewrote a healthy input")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 0 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM messages WHERE text='broken transcript'") != 0 {
		t.Fatal("repair did not restore and verify the damaged input")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation") == 0 {
		t.Fatal("repair did not invalidate deferred analyses")
	}
	summary = server.verifyInputs(t.Context(), "healthy-repair", nil, nil, true)
	if summary.Passed != 3 || summary.Reindexed != 0 {
		t.Fatalf("healthy recheck wrote inputs: %+v", summary)
	}

	t.Run("retained Codex evidence with an enabled live source", func(t *testing.T) {
		source := stableCodexSource(t, 3)
		config := captureTestConfig(t, source)
		runCapture(t, config)
		catalog, _ := testCatalog(t)
		indexCaptures(t, catalog, config.CaptureRoot, "host-a")
		server := testServer(t, config, catalog)
		before, err := queryMaps(catalog.DB, "SELECT * FROM copy_evidence ORDER BY workspace_id,conversation_id,origin")
		if err != nil || len(before) == 0 {
			t.Fatalf("fixture must have accepted retained evidence: %v, %v", before, err)
		}
		execTx(t, catalog.DB, "UPDATE messages SET text='damaged retained transcript'")
		summary := server.verifyInputs(t.Context(), "repair-live-and-retained", nil, nil, true)
		if summary.Checked != 1 || summary.Passed != 1 || summary.Reindexed != 1 || summary.Mismatch != 0 || summary.Errors != 0 || summary.Deferred != 0 {
			checks, _ := queryMaps(catalog.DB, "SELECT outcome,detail FROM sync_verifications")
			t.Fatalf("repair skipped matching retained evidence: %+v; checks=%+v", summary, checks)
		}
		if countRows(t, catalog, "SELECT COUNT(*) FROM messages WHERE text='damaged retained transcript'") != 0 || countRows(t, catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 0 {
			t.Fatal("retained evidence did not repair and verify the damaged input")
		}
		after, err := queryMaps(catalog.DB, "SELECT * FROM copy_evidence ORDER BY workspace_id,conversation_id,origin")
		if err != nil || jsonText(before) != jsonText(after) {
			t.Fatalf("repair changed accepted evidence authority: before=%v after=%v error=%v", before, after, err)
		}
		summary = server.verifyInputs(t.Context(), "healthy-live-and-retained", nil, nil, true)
		if summary.Checked != 1 || summary.Passed != 1 || summary.Reindexed != 0 || summary.Errors != 0 || summary.Deferred != 0 {
			t.Fatalf("healthy recheck did not deduplicate equivalent inputs: %+v", summary)
		}
	})
}

func TestVerificationRepairKeepsUnresolvedIssues(t *testing.T) {
	for _, problem := range []string{"changed", "unreadable", "older-evidence", "write-error", "still-mismatch", "cancelled"} {
		t.Run(problem, func(t *testing.T) {
			useHost(t, "host-a")
			server, units := stableCodexDirectory(t, 1)
			unit := units[0]
			openIssueFoundBy(t, server, unit, "old-verifier")
			ctx := t.Context()
			want := "deferred"
			reindexed := 0
			switch problem {
			case "changed":
				appendSourceFile(t, unit.ID, "\n")
				unit = firstSyncUnit(t, server.Catalog, unit.Source)
			case "unreadable":
				if err := os.Remove(unit.ID); err != nil {
					t.Fatal(err)
				}
				want = "read_error"
			case "write-error":
				execTx(t, server.Catalog.DB, "UPDATE messages SET text='broken transcript'",
					"CREATE TRIGGER reject_repair BEFORE UPDATE ON messages BEGIN SELECT RAISE(ABORT,'repair write failed'); END")
				want = "read_error"
			case "older-evidence":
				execTx(t, server.Catalog.DB, "UPDATE messages SET text='broken transcript'", "UPDATE copy_versions SET version=version+1")
			case "still-mismatch":
				execTx(t, server.Catalog.DB, "UPDATE messages SET text='broken transcript'",
					"CREATE TRIGGER corrupt_repair AFTER UPDATE ON messages BEGIN UPDATE messages SET text='still broken' WHERE id=NEW.id; END")
				want, reindexed = "mismatch", 1
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result := server.Catalog.auditUnitMode(ctx, unit, "repair-failed", 0, true)
			if result.Outcome != want || result.Reindexed != reindexed {
				t.Fatalf("%s: %+v", problem, result)
			}
			if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
				t.Fatal("incomplete repair dismissed an issue")
			}
		})
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
	verify := func(body string) SyncRun {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/sync/verify", strings.NewReader(body))
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
		var finished SyncRun
		waitUntil(t, 30*time.Second, "verification completion", func() bool {
			server.runsMu.RLock()
			defer server.runsMu.RUnlock()
			for _, run := range server.runs {
				if run.ID == receipt.RunID && run.State != "running" {
					finished = *run
					return true
				}
			}
			return false
		})
		if finished.State != "complete" || finished.Verification == nil {
			t.Fatalf("verification failed: %+v", finished)
		}
		return finished
	}
	checked := verify(`{"all":true}`).Verification
	if checked.Checked != 3 || checked.Mismatch != 3 || checked.Errors != 0 || checked.Deferred != 0 {
		t.Fatalf("Verify all sampled or deferred retained inputs: %+v", checked)
	}
	// A scoped repair must not repair the other source's known mismatches.
	scoped := verify(`{"all":true,"repair":true,"sources":["codex"]}`)
	if got := scoped.Verification; got.Checked != 1 || got.Passed != 1 || got.Reindexed != 1 || got.Mismatch != 0 || got.Errors != 0 || got.Deferred != 0 || scoped.Workspaces != 1 {
		t.Fatalf("scoped repair: %+v", scoped)
	}
	if countRows(t, catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open' AND source_name='conductor'") != 2 {
		t.Fatal("scoped repair dismissed another source's issues")
	}
	repaired := verify(`{"all":true,"repair":true}`).Verification
	if repaired.Checked != 3 || repaired.Passed != 3 || repaired.Reindexed != 2 || repaired.Errors != 0 || repaired.Deferred != 0 || repaired.Mismatch != 0 {
		t.Fatalf("full repair: %+v", repaired)
	}
}

func TestVerificationRepairRequiresFullPass(t *testing.T) {
	server := automaticTestServer(t, stableCodexSource(t, 1))
	response := httptest.NewRecorder()
	server.startVerification(response, httptest.NewRequest("POST", "/api/sync/verify", nil), map[string]any{"repair": true})
	if response.Code != 400 {
		t.Fatalf("sampled repair accepted: %s", response.Body.String())
	}
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
