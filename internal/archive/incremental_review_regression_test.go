package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFullRecapturePreservesOnlyChangedFileVersions(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	config := captureTestConfig(t, source)
	runCapture(t, config)
	original := readCaptureManifest(t, config, source.Name).Files[0]
	for _, generation := range []string{"unchanged-1", "unchanged-2"} {
		config.CaptureRecoveryID = generation
		result := captureResult(t, runCapture(t, config), source.Name)
		entry := readCaptureManifest(t, config, source.Name).Files[0]
		if result.FilesCopied != 1 || result.VersionsPreserved != 0 || entry.SHA256 != original.SHA256 || len(entry.Previous) != 0 {
			t.Fatalf("identical full recapture created history: result=%+v entry=%+v", result, entry)
		}
	}
	writeSourceFile(t, source.Path, codexLines("incremental-test", 3))
	config.CaptureRecoveryID = "changed"
	result := captureResult(t, runCapture(t, config), source.Name)
	entry := readCaptureManifest(t, config, source.Name).Files[0]
	if result.VersionsPreserved != 1 || len(entry.Previous) != 1 || entry.Previous[0].SHA256 != original.SHA256 {
		t.Fatalf("changed full recapture lost previous evidence: result=%+v entry=%+v", result, entry)
	}
	previous := readText(t, capturedPath(config, source.Name, entry.Previous[0].Captured))
	if previous != codexLines("incremental-test", 2) {
		t.Fatal("preserved evidence does not contain the original transcript")
	}
	config.CaptureRecoveryID = "unchanged-3"
	result = captureResult(t, runCapture(t, config), source.Name)
	entry = readCaptureManifest(t, config, source.Name).Files[0]
	if result.FilesCopied != 1 || result.VersionsPreserved != 0 || len(entry.Previous) != 1 {
		t.Fatalf("unchanged full recapture duplicated genuine history: result=%+v entry=%+v", result, entry)
	}
}

func TestAntigravityMovingGroupDoesNotAbortHealthySibling(t *testing.T) {
	for _, movement := range []string{"append", "remove", "remove-before-open", "remove-before-stat"} {
		t.Run(movement, func(t *testing.T) {
			directory := t.TempDir()
			content := antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "stable message"})
			rootPath := antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", content)
			childPath := antigravityTranscript(t, directory, antigravityChild, "transcript_full.jsonl", content)
			antigravityTranscript(t, directory, antigravityOther, "transcript_full.jsonl", content)
			antigravitySummaryRows(t, directory,
				[]any{antigravityRoot, "Root", "", "", 0, ""},
				[]any{antigravityChild, "Child", "", antigravityRoot, 1, ""},
				[]any{antigravityOther, "Sibling", "", "", 0, ""})
			adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity", Kind: "antigravity", Path: directory}}}
			previousHook := parseHook
			t.Cleanup(func() { parseHook = previousHook })
			parseHook = func(path string) {
				if path != rootPath {
					return
				}
				switch movement {
				case "append":
					appendSourceFile(t, rootPath, content)
				case "remove":
					if err := os.Remove(rootPath); err != nil {
						t.Fatal(err)
					}
				case "remove-before-open":
					if err := os.Remove(childPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			var emitted []string
			unchanged := func(parts []sourcePart) bool {
				if movement == "remove-before-stat" && parts[0].item == adapter.original(rootPath) {
					if err := os.Remove(filepath.Join(directory, "brain", antigravityOther, antigravityLogs, "transcript_full.jsonl")); err != nil {
						t.Fatal(err)
					}
				}
				return false
			}
			err := adapter.discoverParts(unchanged, func(record *WorkspaceRecord, _ []sourcePart) error {
				emitted = append(emitted, record.SourceID)
				return nil
			})
			want := []string{antigravityOther}
			if movement == "remove-before-stat" {
				want = []string{antigravityRoot}
			}
			if err != nil || !slices.Equal(emitted, want) {
				t.Fatalf("moving group aborted discovery or emitted partial state: emitted=%v error=%v", emitted, err)
			}
			parseHook = nil
			if movement == "append" {
				emitted = nil
				if err := adapter.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
					emitted = append(emitted, record.SourceID)
					return nil
				}); err != nil || !slices.Contains(emitted, antigravityRoot) {
					t.Fatalf("stable group was not eligible on retry: emitted=%v error=%v", emitted, err)
				}
			}
		})
	}
}

func TestAntigravityDependencyFailureDoesNotAbortHealthySibling(t *testing.T) {
	directory := t.TempDir()
	content := antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "dependency test"})
	antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", content)
	antigravityTranscript(t, directory, antigravityOther, "transcript_full.jsonl", content)
	antigravityRequestRows(t, directory, antigravityRoot)
	wal := filepath.Join(directory, "conversations", antigravityRoot+".db-wal")
	if err := os.Symlink(filepath.Base(wal), wal); err != nil {
		t.Fatal(err)
	}
	adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity", Kind: "antigravity", Path: directory}}}
	var checked, emitted []string
	err := adapter.discoverParts(func(parts []sourcePart) bool {
		checked = append(checked, parts[0].item)
		return false
	}, func(record *WorkspaceRecord, _ []sourcePart) error {
		emitted = append(emitted, record.SourceID)
		return nil
	})
	if err == nil || len(checked) != 1 || !slices.Equal(emitted, []string{antigravityOther}) {
		t.Fatalf("dependency failure advanced bad group or aborted sibling: checked=%v emitted=%v error=%v", checked, emitted, err)
	}
}

type failingTranscriptEntry struct {
	os.DirEntry
	err error
}

func (entry failingTranscriptEntry) Info() (fs.FileInfo, error) { return nil, entry.err }

func TestJSONLDiscoveryToleratesOnlyDisappearingEntries(t *testing.T) {
	source := stableCodexSource(t, 2)
	entries, err := os.ReadDir(filepath.Dir(source.Path))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &jsonlAdapter{baseAdapter: baseAdapter{config: source}, provider: "codex"}
	for _, test := range []struct {
		name    string
		path    string
		entry   os.DirEntry
		walkErr error
		wantErr error
	}{
		{"missing entry", source.Path, failingTranscriptEntry{entries[0], &os.PathError{Op: "stat", Path: source.Path, Err: os.ErrNotExist}}, nil, nil},
		{"missing subtree", filepath.Join(source.Path, "deleted"), nil, os.ErrNotExist, nil},
		{"missing source", source.Path, nil, os.ErrNotExist, os.ErrNotExist},
		{"unreadable entry", source.Path, failingTranscriptEntry{entries[0], os.ErrPermission}, nil, os.ErrPermission},
		{"unreadable subtree", filepath.Join(source.Path, "private"), nil, os.ErrPermission, os.ErrPermission},
	} {
		t.Run(test.name, func(t *testing.T) {
			info, err := adapter.transcriptInfo(test.path, test.entry, test.walkErr)
			if info != nil || !errors.Is(err, test.wantErr) {
				t.Fatalf("incorrect discovery race handling: info=%v error=%v", info, err)
			}
		})
	}
}

func TestConductorObservedVersionFollowsBlockedSnapshotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conductor.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	execSQL(t, db, "PRAGMA journal_mode=DELETE",
		"CREATE TABLE sessions(id TEXT PRIMARY KEY, title TEXT, created_at TEXT, updated_at TEXT)",
		"CREATE TABLE session_messages(id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, created_at TEXT, sent_at TEXT, cancelled_at TEXT)")
	execSQL(t, db, addConductorSession("snapshot", 1)...)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, "BEGIN EXCLUSIVE", "UPDATE session_messages SET content='committed while waiting'")
	defer db.Exec("ROLLBACK")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	adapter := &conductorAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "conductor", Kind: "conductor", Path: path}, ctx: ctx}}
	type readResult struct {
		record *WorkspaceRecord
		err    error
	}
	done := make(chan readResult, 1)
	go func() {
		result := readResult{}
		result.err = adapter.database(path, nil, nil, func(record *WorkspaceRecord, _ []sourcePart) error {
			result.record = record
			return nil
		})
		done <- result
	}()
	select {
	case result := <-done:
		t.Fatalf("snapshot did not wait for writer: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	fresh := time.Now().Add(-time.Second)
	if err := os.Chtimes(path, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, "COMMIT")
	select {
	case result := <-done:
		if result.err != nil || result.record == nil {
			t.Fatalf("read failed: %+v", result)
		}
		if result.record.Observed < fresh.UnixNano() || result.record.Conversations[0].Observed != result.record.Observed || result.record.Conversations[0].Messages[0].Text != "committed while waiting" {
			t.Fatalf("snapshot was stamped with a pre-read version: %+v", result.record)
		}
	case <-ctx.Done():
		t.Fatal("snapshot did not finish after writer committed")
	}
}

func TestVerifyAllEndpointChecksEveryInputIncludingLargeConductorSession(t *testing.T) {
	useHost(t, "host-a")
	codex := stableCodexSource(t, 3)
	directory := t.TempDir()
	execute := conductorFixture(t, directory)
	execute(addConductorSession("large", 2001)...)
	execute(addConductorSession("small", 2)...)
	conductor := SourceConfig{Name: "conductor", Kind: "conductor", Path: directory, Account: "local", Enabled: true}
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{codex, conductor}
	server := NewServer(config, catalog)
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

func TestSyncSettingsRouteServesUI(t *testing.T) {
	server := automaticTestServer(t, stableCodexSource(t, 2))
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/settings/sync", nil)
	request.Header.Set("Authorization", "Bearer "+server.Config().APIToken)
	server.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `href="/settings/sync"`) || !strings.Contains(response.Body.String(), `id="syncDashboard"`) {
		t.Fatalf("Sync Settings deep link did not serve its navigation and dashboard: status=%d", response.Code)
	}
}

func TestAntigravityAuditExcludesUnrelatedRunLogEvidence(t *testing.T) {
	useHost(t, "host-a")
	directory := t.TempDir()
	antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "bounded real CLI evidence"}))
	writeSourceFile(t, filepath.Join(directory, "log", "cli-relevant.log"), "Print mode: conversation="+antigravityRoot+"\n")
	writeSourceFile(t, filepath.Join(directory, "log", "cli-unrelated.log"), strings.Repeat("unrelated log data", 1<<20))
	source := SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: directory, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "cli-evidence", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("unrelated CLI logs defeated a bounded group audit: %+v", result)
	}
}

func TestAntigravityLogReaderHandlesChunkBoundariesAndLongLines(t *testing.T) {
	for _, offset := range []int{0, 32710, 32740, 32768, 65500, 65536} {
		path := filepath.Join(t.TempDir(), "cli.log")
		content := strings.Repeat("x", offset) + "Print mode: conversation=" + antigravityRoot + "\n" + strings.Repeat("x", 128*1024) + "Print mode: conversation=" + antigravityChild + "\nPrint mode: conversation=" + antigravityRoot
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		ids, err := readAntigravityLogConversations(t.Context(), path, make([]byte, 64*1024))
		if err != nil || !slices.Equal(ids, []string{antigravityRoot, antigravityChild}) {
			t.Fatalf("offset %d: streaming print-mode IDs=%v error=%v", offset, ids, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := readAntigravityLogConversations(ctx, path, make([]byte, 64*1024)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled log read did not stop: %v", err)
		}
	}
}

func TestIncrementalActivityEstimatesKeepRunKindsSeparate(t *testing.T) {
	useHost(t, "timing-host")
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
	started := time.Now().Add(-time.Minute)
	server.recordActivityDuration("sync", formatTime(started), formatTime(started.Add(10*time.Minute)))
	for _, kind := range []string{automaticRunKind, "verification", "library-update"} {
		run := server.startRunNamed(kind, []string{"codex"})
		server.updateRun(run.ID, func(run *SyncRun) { run.StartedAt = formatTime(started) })
		activities := server.libraryStatus()["activities"].([]libraryActivity)
		if estimate := activities[0].EstimatedCompletionAt; estimate != "" {
			t.Fatalf("%s reused source-sync duration: %s", kind, estimate)
		}
		server.updateRun(run.ID, func(run *SyncRun) {
			run.State, run.CompletedAt = "complete", formatTime(started.Add(2*time.Minute))
		})
		if duration := server.activityDuration(kind); duration != 2*time.Minute {
			t.Fatalf("%s did not retain its own history: %s", kind, duration)
		}
		if durations := server.activityDurations("sync"); len(durations) != 1 {
			t.Fatalf("%s polluted source-sync history: %v", kind, durations)
		}
		active := server.startRunNamed(kind, []string{"codex"})
		server.updateRun(active.ID, func(run *SyncRun) { run.StartedAt = formatTime(started) })
		activities = server.libraryStatus()["activities"].([]libraryActivity)
		if estimate, want := activities[0].EstimatedCompletionAt, formatTime(started.Add(2*time.Minute)); estimate != want {
			t.Fatalf("%s estimate=%s, want %s", kind, estimate, want)
		}
		server.updateRun(active.ID, func(run *SyncRun) { run.State = "stopped" })
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
