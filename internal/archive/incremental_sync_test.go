package archive

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func stableCodexSource(t *testing.T, count int) SourceConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout-fixture.jsonl")
	writeSourceFile(t, path, codexLines("incremental-test", count))
	stamp := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return SourceConfig{Name: "codex", Kind: "codex", Path: path, Account: "local", Enabled: true}
}

func automaticTestServer(t *testing.T, source SourceConfig) *Server {
	t.Helper()
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	server := NewServer(config, catalog)
	t.Cleanup(func() {
		if err := server.stop(5 * time.Second); err != nil {
			t.Error(err)
		}
	})
	return server
}

func firstSyncUnit(t *testing.T, catalog *Catalog, source SourceConfig) syncUnit {
	t.Helper()
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	var unit syncUnit
	err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
		if unit.ID == "" {
			unit = unitOf(source, parts, false)
		}
		return true
	}, func(*WorkspaceRecord, []sourcePart) error { return fmt.Errorf("unexpected parse") })
	if err != nil {
		t.Fatal(err)
	}
	return unit
}

func TestChangesAPIReadsNoBodiesOrState(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 3)
	server := automaticTestServer(t, source)
	parses := countParses(t)
	report := server.Catalog.sourceChanges(t.Context(), source)
	if !report.Complete || len(report.Changed) != 1 || parses.Load() != 0 {
		t.Fatalf("change check parsed or failed: %+v, parses=%d", report, parses.Load())
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM source_item_states") != 0 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM source_states") != 0 {
		t.Fatal("check advanced indexed state")
	}
	ingestSource(t, server.Catalog, source)
	parses.Store(0)
	report = server.Catalog.sourceChanges(t.Context(), source)
	if !report.Complete || len(report.Changed) != 0 || parses.Load() != 0 {
		t.Fatalf("unchanged check: %+v", report)
	}
}

func TestAutomaticRefreshKeepsIntentionalStateAndAuditsNoops(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 3)
	server := automaticTestServer(t, source)
	manual := ingestSource(t, server.Catalog, source)
	if manual.Workspaces != 1 {
		t.Fatal(manual)
	}
	before, err := queryMaps(server.Catalog.DB, "SELECT * FROM source_states")
	if err != nil {
		t.Fatal(err)
	}
	settings := syncSettings{Enabled: true, Interval: 60}
	if !server.runAutomatic(server.life.ctx, settings) {
		t.Fatal("automatic run not admitted")
	}
	after, _ := queryMaps(server.Catalog.DB, "SELECT * FROM source_states")
	if jsonText(before) != jsonText(after) {
		t.Fatal("automatic refresh overwrote intentional source state")
	}
	var text string
	if err := server.Catalog.DB.QueryRow("SELECT sample_json FROM sync_history ORDER BY id DESC LIMIT 1").Scan(&text); err != nil {
		t.Fatal(err)
	}
	var sample syncMeasurement
	if err := json.Unmarshal([]byte(text), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Audit.Outcome != "passed" || sample.Workspaces != 0 {
		t.Fatalf("noop must verify: %+v", sample)
	}
	if sample.FinishedAt == "" || !slices.Equal(sample.Sources, []string{"codex"}) || len(sample.SourceResults) != 1 || sample.SourceResults[0].Source != "codex" {
		t.Fatalf("sync must record completion time and source breakdown: %+v", sample)
	}
	for _, result := range server.runs[0].Results {
		if result.WriteRows != 0 {
			t.Fatal("unchanged discovery wrote conversation rows")
		}
	}
	appendSourceFile(t, source.Path, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"freshincrementalzebra"}}`+"\n")
	if !server.runAutomatic(server.life.ctx, settings) {
		t.Fatal("append not admitted")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM messages WHERE text='freshincrementalzebra'") != 1 {
		t.Fatal("appended message missing")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation") != 5 {
		t.Fatal("deferred library work was not marked")
	}
	after, _ = queryMaps(server.Catalog.DB, "SELECT * FROM source_states")
	if jsonText(before) != jsonText(after) {
		t.Fatal("append overwrote intentional state")
	}
	response := httptest.NewRecorder()
	server.getIncrementalSync(response, httptest.NewRequest("GET", "/api/sync/history", nil))
	var history struct {
		Samples []syncMeasurement `json:"samples"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(history.Samples) != 2 {
		t.Fatalf("sync history: %d %s", response.Code, response.Body.String())
	}
	latest := history.Samples[0]
	if len(latest.SourceResults) != 1 || latest.SourceResults[0].Conversations != latest.Conversations || latest.SourceResults[0].Messages != latest.Messages || latest.Conversations == 0 || latest.FinishedAt == "" {
		t.Fatalf("source counts must match refreshed totals: %+v", latest)
	}
}

func TestSyncHistoryRestoresLegacyCompletionTime(t *testing.T) {
	useHost(t, "host-a")
	server := automaticTestServer(t, stableCodexSource(t, 3))
	stamp := "2026-09-30T18:00:00Z"
	legacy := syncMeasurement{RunID: "legacy", State: "complete", Class: "no_changes"}
	if _, err := server.Catalog.DB.Exec("INSERT INTO sync_history(host_id,run_id,finished_at,sample_json) VALUES(?,?,?,?)", currentHost().ID, legacy.RunID, stamp, jsonText(legacy)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/sync/history", "/api/sync/status"} {
		response := httptest.NewRecorder()
		server.getIncrementalSync(response, httptest.NewRequest("GET", path, nil))
		var body struct {
			Samples []syncMeasurement `json:"samples"`
			History []syncMeasurement `json:"history"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		samples := append(body.Samples, body.History...)
		if response.Code != 200 || len(samples) != 1 || samples[0].FinishedAt != stamp || len(samples[0].SourceResults) != 0 {
			t.Fatalf("legacy history must retain its timestamp without inventing source counts: %s", response.Body.String())
		}
	}
}

func TestIndependentAuditDetectsCorruptionAndResolvesOnlyAfterRepair(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 3)
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "initial", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("initial audit: %+v", result)
	}
	if _, err := server.Catalog.DB.Exec("UPDATE messages SET text='corruptedtext' WHERE source_order=0"); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		result := server.Catalog.auditUnit(t.Context(), unit, fmt.Sprint(attempt), automaticAuditBytes)
		if result.Outcome != "mismatch" || !slices.Contains(result.Differences, "messages") {
			t.Fatalf("corruption not confirmed: %+v", result)
		}
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
		t.Fatal("issue was not deduplicated")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM messages WHERE text='corruptedtext'") != 1 {
		t.Fatal("audit mutated live messages")
	}
	if err := server.Catalog.advanceSyncGeneration(t.Context(), currentHost().ID, source.Name); err != nil {
		t.Fatal(err)
	}
	adapter, _ := MakeAdapter(source)
	result := server.Catalog.IngestContext(withIngestMode(t.Context(), ingestMode{force: true}), adapter, nil)
	if result.Error != nil || result.Workspaces != 1 {
		t.Fatalf("force did not repair: %+v", result)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
		t.Fatal("repair cleared unverified evidence")
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "repaired", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("repair failed verification: %+v", result)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='resolved'") != 1 {
		t.Fatal("verified repair did not resolve issue")
	}
}

func TestAuditDetectsMissingTrigramAndUnindexedStableUnit(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "omission", automaticAuditBytes); result.Outcome != "mismatch" {
		t.Fatalf("unindexed unit not detected: %+v", result)
	}
	ingestSource(t, server.Catalog, source)
	if _, err := server.Catalog.DB.Exec("DELETE FROM messages_trigram"); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "trigram", automaticAuditBytes); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "search") {
		t.Fatalf("trigram corruption not detected: %+v", result)
	}
}

type cancellingAdapter struct {
	baseAdapter
	entered chan struct{}
}

func (adapter *cancellingAdapter) Fingerprint() (string, error)               { return "fixture", nil }
func (adapter *cancellingAdapter) Discover(func(WorkspaceRecord) error) error { return nil }
func (adapter *cancellingAdapter) partExtractor() string                      { return "fixture" }
func (adapter *cancellingAdapter) discoverParts(func([]sourcePart) bool, func(*WorkspaceRecord, []sourcePart) error) error {
	close(adapter.entered)
	<-adapter.context().Done()
	return adapter.context().Err()
}

func TestManualActionPreemptsAutomaticWithoutConflict(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 1)
	server := automaticTestServer(t, source)
	entered := make(chan struct{})
	prior := syncAdapter
	syncAdapter = func(config SourceConfig) (Adapter, error) {
		return &cancellingAdapter{baseAdapter: baseAdapter{config: config}, entered: entered}, nil
	}
	t.Cleanup(func() { syncAdapter = prior })
	done := make(chan struct{})
	go func() { server.runAutomatic(server.life.ctx, syncSettings{Enabled: true, Interval: 60}); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic discovery did not start")
	}
	if !server.acquireManualIngest() {
		t.Fatal("background run blocked manual action")
	}
	server.ingestMu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic cancellation did not finish")
	}
}

func TestSyncCadenceValidationAndPersistence(t *testing.T) {
	server := automaticTestServer(t, stableCodexSource(t, 1))
	post := func(body map[string]any) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/sync/settings", nil)
		server.postIncrementalSync(response, request, body)
		return response
	}
	if response := post(map[string]any{"enabled": true, "interval_seconds": float64(59)}); response.Code != 400 {
		t.Fatal(response.Code)
	}
	if response := post(map[string]any{"enabled": true, "interval_seconds": float64(60)}); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	settings, err := server.Catalog.syncSettings(t.Context())
	if err != nil || !settings.Enabled || settings.Interval != 60 || settings.NextAt == "" {
		t.Fatalf("not persisted: %+v %v", settings, err)
	}
	if response := post(map[string]any{"enabled": false}); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	settings, _ = server.Catalog.syncSettings(t.Context())
	if settings.Enabled {
		t.Fatal("Off was not saved")
	}
}

func TestFullRecaptureBypassesReuseAndResumesVerifiedFiles(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	config := captureTestConfig(t, source)
	runCapture(t, config)
	config.CaptureRecoveryID = "repair-1"
	first := captureResult(t, runCapture(t, config), source.Name)
	if first.FilesCopied != 1 || first.FilesUnchanged != 0 {
		t.Fatalf("force reused capture: %+v", first)
	}
	if entry := readCaptureManifest(t, config, source.Name).Files[0]; entry.RecoveryID != "repair-1" || len(entry.Previous) != 0 || first.VersionsPreserved != 0 {
		t.Fatal("force generation duplicated unchanged evidence")
	}
	second := captureResult(t, runCapture(t, config), source.Name)
	if second.FilesCopied != 0 || second.FilesUnchanged != 1 {
		t.Fatalf("resume recopied verified file: %+v", second)
	}
	config.CaptureRecoveryID = "repair-2"
	third := captureResult(t, runCapture(t, config), source.Name)
	if third.FilesCopied != 1 || third.VersionsPreserved != 0 || len(readCaptureManifest(t, config, source.Name).Files[0].Previous) != 0 {
		t.Fatal("new force generation reused old verification")
	}
}

func TestConductorAutomaticWALAndSameSignalAudit(t *testing.T) {
	useHost(t, "host-a")
	dir := t.TempDir()
	exec := conductorFixture(t, dir)
	exec(addConductorSession("session", 2)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	settings := syncSettings{Enabled: true, Interval: 60}
	server.runAutomatic(server.life.ctx, settings)
	exec("UPDATE session_messages SET content='same-signal-correction' WHERE id='session-m0'")
	server.runAutomatic(server.life.ctx, settings)
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM messages WHERE text='same-signal-correction'") != 0 {
		t.Fatal("fixture unexpectedly changed the session aggregate")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
		t.Fatal("same-signal edit was not caught by independent audit")
	}
	if err := server.Catalog.advanceSyncGeneration(t.Context(), currentHost().ID, source.Name); err != nil {
		t.Fatal(err)
	}
	adapter, _ := MakeAdapter(source)
	result := server.Catalog.IngestContext(withIngestMode(t.Context(), ingestMode{force: true}), adapter, nil)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM messages WHERE text='same-signal-correction'") != 1 {
		t.Fatal("force failed to repair same-signal edit")
	}
}

func TestAuditBudgetsAndSourceMovementDoNotCreateIssues(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "budget", 1); result.Outcome != "deferred" {
		t.Fatal(result)
	}
	appendSourceFile(t, source.Path, strings.Repeat(" ", 3))
	if result := server.Catalog.auditUnit(t.Context(), unit, "movement", automaticAuditBytes); result.Outcome != "deferred" {
		t.Fatal(result)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues") != 0 {
		t.Fatal("unstable input was reported as corruption")
	}
}

func TestVerificationVisitsEveryStableInputAndWorkspaceMetrics(t *testing.T) {
	useHost(t, "host-a")
	directory := t.TempDir()
	for index := range 2 {
		path := filepath.Join(directory, fmt.Sprintf("rollout-%d.jsonl", index))
		writeSourceFile(t, path, codexLines(fmt.Sprintf("verify-%d", index), 3))
		stamp := time.Now().Add(-time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	source := SourceConfig{Name: "codex", Kind: "codex", Path: directory, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	if _, err := server.Catalog.DB.Exec("UPDATE metrics SET value=value+100 WHERE name='user_turns'"); err != nil {
		t.Fatal(err)
	}
	summary := server.verifyAllInputs(t.Context(), "exhaustive", nil)
	if summary.Checked != 2 || summary.Mismatch != 2 || summary.Errors != 0 || summary.Deferred != 0 {
		t.Fatalf("verification did not visit both corrupted scopes: %+v", summary)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 2 {
		t.Fatal("both mismatches must remain visible")
	}
}

func TestVerificationReadsOffHostCapturesAfterSourceRemoval(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 3)
	config := captureTestConfig(t, source)
	runCapture(t, config)
	catalog, _ := testCatalog(t)
	targets, err := captureTargets(config.CaptureRoot, nil, true, nil)
	if err != nil || len(targets) != 1 {
		t.Fatalf("capture targets: %v, %v", targets, err)
	}
	if result := catalog.IndexCapture(t.Context(), targets[0], nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	if err := os.Remove(source.Path); err != nil {
		t.Fatal(err)
	}
	useHost(t, "host-b")
	config.Sources = nil
	server := NewServer(config, catalog)
	t.Cleanup(func() { server.stop(5 * time.Second) })
	summary := server.verifyAllInputs(t.Context(), "retained", nil)
	if summary.Checked != 1 || summary.Passed != 1 || summary.Errors != 0 || summary.Deferred != 0 {
		t.Fatalf("retained off-host verification: %+v", summary)
	}
	if countRows(t, catalog, "SELECT COUNT(*) FROM sync_verifications WHERE host_id='host-a' AND outcome='passed'") != 1 {
		t.Fatal("verification was attributed to the wrong host")
	}
}

func TestAutomaticProjectionsAreBatchedAndRollupRemainsDeferred(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 3)
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	if err := server.Catalog.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := server.Catalog.metaValue(t.Context(), "tool_rollup_generation")
	appendSourceFile(t, source.Path, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"batched projection update"}}`+"\n")
	server.runAutomatic(server.life.ctx, syncSettings{Interval: 300})
	if server.Catalog.projectionBatches.Load() != 0 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM workspace_library_dirty") != 0 {
		t.Fatal("batch did not release and publish its projections")
	}
	if err := server.Catalog.currentToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, _ := server.Catalog.metaValue(t.Context(), "tool_rollup_generation")
	if before != after || server.Catalog.tools.refreshing {
		t.Fatal("automatic writes triggered library-wide tools rebuild")
	}
}

func TestAntigravityMembershipRemovalReparentingAndRootPromotion(t *testing.T) {
	useHost(t, "host-a")
	directory := antigravityFixture(t)
	source := SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: directory, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	childPath := filepath.Join(directory, "brain", antigravityChild, antigravityLogs, "transcript_full.jsonl")
	content, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(childPath); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, server.Catalog, source)
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM conversations") != 3 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM conversation_group_membership WHERE active=0") != 1 {
		t.Fatal("removed child history or current membership is incorrect")
	}
	if err := server.Catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	var conversations int
	if err := server.Catalog.DB.QueryRow(`SELECT l.conversation_count FROM workspace_library l JOIN workspaces w ON w.id=l.workspace_id WHERE w.source_id=?`, antigravityRoot).Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("removed child still contributes to current group: %d: %v", conversations, err)
	}
	writeSourceFile(t, childPath, string(content))
	antigravitySummaryRows(t, directory, []any{antigravityChild, "Reparented", `[]`, antigravityOther, 1, "tester"})
	ingestSource(t, server.Catalog, source)
	if countRows(t, server.Catalog, `SELECT COUNT(*) FROM conversations c JOIN conversations p ON p.id=c.parent_id JOIN workspaces w ON w.id=c.workspace_id WHERE c.native_id='`+antigravityChild+`' AND p.native_id='`+antigravityOther+`' AND w.source_id='`+antigravityOther+`'`) != 1 {
		t.Fatal("reparented child has the wrong current association")
	}
	antigravitySummaryRows(t, directory, []any{antigravityChild, "Promoted root", `[]`, "", 0, ""})
	ingestSource(t, server.Catalog, source)
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM conversations WHERE native_id='"+antigravityChild+"' AND parent_id IS NULL") != 1 {
		t.Fatal("promoted root retained its obsolete parent")
	}
}

func TestConductorNativeAliasesLinkInEitherArrivalOrder(t *testing.T) {
	for _, conductorFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(conductorFirst), func(t *testing.T) {
			catalog, _ := testCatalog(t)
			native := incrementalFixture(2)
			native.Conversations[0].NativeID = "native-session"
			mirror := incrementalFixture(2)
			mirror.SourceKind, mirror.SourceID = "conductor", "conductor-session"
			mirror.Conversations[0].NativeID = "conductor-session"
			mirror.Conversations[0].Aliases = []string{"native-session"}
			if conductorFirst {
				writeIncrementalFixture(t, catalog, mirror)
				writeIncrementalFixture(t, catalog, native)
			} else {
				writeIncrementalFixture(t, catalog, native)
				writeIncrementalFixture(t, catalog, mirror)
			}
			if countRows(t, catalog, "SELECT COUNT(*) FROM conversation_identity_links WHERE relationship='native-alias'") != 1 {
				t.Fatal("alias waited for global reconciliation")
			}
		})
	}
}
