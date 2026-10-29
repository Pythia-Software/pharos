package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduledSyncDefersAnalysesAndManualSyncRefreshesGitHub(t *testing.T) {
	useHost(t, "host-a")
	calls := stubGitHub(t, true, map[string]string{"acme/sync": `{"id":42,"full_name":"acme/sync"}`})
	server := automaticTestServer(t, stableCodexSource(t, 1))
	catalog := server.Catalog
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,canonical_remote,normalized_remote,display_name,created_at,updated_at)
		VALUES('github-sync','https://github.com/acme/sync.git','github.com/acme/sync','sync','t','t')`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"authorship_generation", "tool_rollup_generation", "findings_generation"} {
		if err := setMeta(catalog.DB, key, "old"); err != nil {
			t.Fatal(err)
		}
	}
	background := catalog.background
	started := 0
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { started++; return false }
	if !server.runAutomatic(server.life.ctx, syncSettings{Interval: 60}) {
		t.Fatal("scheduled sync not admitted")
	}
	var encoded string
	if err := catalog.DB.QueryRow("SELECT sample_json FROM sync_history ORDER BY id DESC LIMIT 1").Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var sample syncMeasurement
	if err := json.Unmarshal([]byte(encoded), &sample); err != nil {
		t.Fatal(err)
	}
	measured := false
	for _, phase := range sample.Phases {
		for _, work := range phase.Work {
			if work.Phase == "indexing" {
				measured = work.TotalKnown && work.Total > 0 && work.Done == work.Total && work.Features.Groups == 1 && work.Features.Bytes > 0
			}
		}
	}
	if !measured {
		t.Fatalf("incremental history lost its measured input workload: %+v", sample.Phases)
	}
	if started != 0 || stubbedCalls(t, calls) != "" {
		t.Fatal("scheduled sync started an expensive analysis or GitHub lookup")
	}
	for _, key := range []string{"authorship_generation", "tool_rollup_generation", "findings_generation"} {
		if value, err := catalog.metaValue(t.Context(), key); err != nil || value != "old" {
			t.Fatalf("scheduled sync refreshed %s: %q %v", key, value, err)
		}
	}
	if countRows(t, catalog, "SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation") != 5 {
		t.Fatal("scheduled sync lost deferred work")
	}
	catalog.background = background
	// A manual no-op must still finish what the scheduled run left pending,
	// and force a full findings pass even when today's generation exists.
	if err := setMeta(catalog.DB, "findings_generation", catalog.findingsGeneration()); err != nil {
		t.Fatal(err)
	}
	if !server.runManualSync(server.life.ctx, syncSettings{Interval: 60}) {
		t.Fatal("manual sync not admitted")
	}
	var forge string
	if err := catalog.DB.QueryRow("SELECT forge_id FROM repositories WHERE id='github-sync'").Scan(&forge); err != nil || forge != "42" {
		t.Fatalf("manual sync did not resolve GitHub: %q %v", forge, err)
	}
	if !strings.Contains(stubbedCalls(t, calls), "api repos/acme/sync") {
		t.Fatal("manual sync did not call GitHub")
	}
	if value, err := catalog.metaValue(t.Context(), "findings_built_at"); err != nil || value == "" {
		t.Fatalf("manual sync skipped today's full findings update: %q %v", value, err)
	}
	if countRows(t, catalog, "SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation") != 0 || catalog.ensureAuthorship() {
		t.Fatal("manual no-op left analyses stale")
	}
}

func TestSyncCheckStartsManualRun(t *testing.T) {
	server := automaticTestServer(t, stableCodexSource(t, 1))
	response := httptest.NewRecorder()
	server.postIncrementalSync(response, httptest.NewRequest("POST", "/api/sync/check", nil), map[string]any{})
	if response.Code != 202 {
		t.Fatalf("manual button request failed: %d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.runsMu.RLock()
		finished := len(server.runs) > 0 && server.runs[0].State != "running"
		if finished && (server.runs[0].Kind != manualSyncRunKind || server.runs[0].State != "complete") {
			t.Errorf("button ran the wrong pipeline: %+v", server.runs[0])
		}
		server.runsMu.RUnlock()
		if finished {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("manual button run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManualSyncFailedAnalysisRetainsDirtyGeneration(t *testing.T) {
	server := automaticTestServer(t, stableCodexSource(t, 1))
	server.Catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
	if !server.runManualSync(server.life.ctx, syncSettings{Interval: 60}) {
		t.Fatal("manual sync not admitted")
	}
	if server.runs[0].State != "failed" || !strings.Contains(firstString(server.runs[0].Error), "authorship rebuild did not complete") {
		t.Fatalf("analysis failure reported success: %+v", server.runs[0])
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_deferred WHERE name IN ('authorship','findings') AND generation>completed_generation") != 2 {
		t.Fatal("failed analyses were marked complete")
	}
}

func TestManualSyncRefreshesRecentHumanWords(t *testing.T) {
	for _, alreadyIndexed := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_indexed=%v", alreadyIndexed), func(t *testing.T) {
			useHost(t, "host-a")
			source := stableCodexSource(t, 1)
			server := automaticTestServer(t, source)
			catalog := server.Catalog
			catalog.now = func() time.Time { return time.Date(2026, 9, 1, 1, 15, 0, 0, time.UTC) }
			ingestSource(t, catalog, source)
			generation, _, err := catalog.authorshipGenerations()
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.RebuildAuthorship(t.Context(), generation); err != nil {
				t.Fatal(err)
			}
			appendSourceFile(t, source.Path, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"Please check why my recent words disappeared"}}`+"\n")
			if alreadyIndexed {
				// A prior index committed messages but never refreshed authorship.
				ingestSource(t, catalog, source)
			}
			if !server.runManualSync(server.life.ctx, syncSettings{Interval: 60}) {
				t.Fatal("manual sync not admitted")
			}
			if catalog.ensureAuthorship() {
				t.Fatal("authorship still stale after sync's rebuild")
			}
			summary, err := catalog.UsageSummary(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			hour := summary["windows"].([]*usageSummaryWindow)[0]
			if hour.HumanWords != 7 || hour.Messages != 1 {
				t.Fatalf("recent typed message missing from summary: %+v", hour)
			}
			if countRows(t, catalog, "SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation") != 0 {
				t.Fatal("manual sync left analyses pending")
			}
			var encoded string
			if err := catalog.DB.QueryRow("SELECT sample_json FROM sync_history ORDER BY id DESC LIMIT 1").Scan(&encoded); err != nil {
				t.Fatal(err)
			}
			var sample syncMeasurement
			if err := json.Unmarshal([]byte(encoded), &sample); err != nil {
				t.Fatal(err)
			}
			phases := []string{}
			for _, phase := range sample.Phases {
				phases = append(phases, phase.Name)
			}
			for _, name := range []string{"github", "identities", "git", "tools", "authorship", "findings"} {
				if !slices.Contains(phases, name) {
					t.Fatalf("manual sync did not measure %s: %+v", name, sample)
				}
			}
			if sample.Trigger != "manual" || sample.State != "complete" || server.runs[0].Kind != manualSyncRunKind {
				t.Fatalf("manual sync incorrectly recorded: %+v", sample)
			}

		})
	}
}

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

// A rebuild a page started before an index does not follow up while the
// index runs, so the index's own authorship stage starts the rebuild that
// brings the ledger current, rather than failing on the page's stale one.
func TestManualSyncRebuildsAuthorshipAfterAPageRebuild(t *testing.T) {
	server := automaticTestServer(t, stableCodexSource(t, 1))
	start := server.Catalog.background
	var page func(context.Context, *Op) error
	server.Catalog.background = func(spec OperationSpec, work func(context.Context, *Op) error) bool {
		if page == nil {
			page = work
			return true
		}
		return start(spec, work)
	}
	if !server.Catalog.ensureAuthorship() || page == nil {
		t.Fatal("a page did not start a rebuild of the missing ledger")
	}
	done := make(chan bool)
	go func() { done <- server.runManualSync(server.life.ctx, syncSettings{Interval: 60}) }()
	waitUntil(t, 30*time.Second, "the index's authorship stage", func() bool {
		return slices.ContainsFunc(server.ops.running(), func(op *Op) bool { return op.spec.Kind == "analysis.authorship" })
	})
	// The page's rebuild ends during the index, for the inputs before it.
	if err := page(server.life.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !<-done {
		t.Fatal("manual sync not admitted")
	}
	if server.runs[0].State != "complete" {
		t.Fatalf("index after a page's rebuild: %+v", server.runs[0])
	}
	if server.Catalog.ensureAuthorship() {
		t.Fatal("the index left authorship stale")
	}
}

func automaticTestServer(t *testing.T, source SourceConfig) *Server {
	t.Helper()
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	server := testServer(t, config, catalog)
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
	var calls atomic.Int32
	syncAdapter = func(config SourceConfig) (Adapter, error) {
		if calls.Add(1) == 1 {
			return &cancellingAdapter{baseAdapter: baseAdapter{config: config}, entered: entered}, nil
		}
		return prior(config)
	}
	t.Cleanup(func() { syncAdapter = prior })
	done := make(chan struct{})
	go func() { server.runAutomatic(server.life.ctx, syncSettings{Enabled: true, Interval: 60}); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic discovery did not start")
	}
	if !server.runManualSync(server.life.ctx, syncSettings{Interval: 60}) {
		t.Fatal("background run blocked manual sync")
	}
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

// TestFullRecapture: a full recapture (a recovery ID) copies every file once
// rather than reusing the capture, and a resumed one does not copy it again.
// Recapturing an identical file adds no history; a changed file keeps its
// previous version.
func TestFullRecapture(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	config := captureTestConfig(t, source)
	runCapture(t, config)
	original := readCaptureManifest(t, config, source.Name).Files[0]
	config.CaptureRecoveryID = "repair-1"
	first := captureResult(t, runCapture(t, config), source.Name)
	if first.FilesCopied != 1 || first.FilesUnchanged != 0 {
		t.Fatalf("force reused capture: %+v", first)
	}
	if entry := readCaptureManifest(t, config, source.Name).Files[0]; entry.RecoveryID != "repair-1" || entry.SHA256 != original.SHA256 || len(entry.Previous) != 0 || first.VersionsPreserved != 0 {
		t.Fatalf("identical full recapture created history: result=%+v entry=%+v", first, entry)
	}
	second := captureResult(t, runCapture(t, config), source.Name)
	if second.FilesCopied != 0 || second.FilesUnchanged != 1 {
		t.Fatalf("resume recopied verified file: %+v", second)
	}
	config.CaptureRecoveryID = "repair-2"
	third := captureResult(t, runCapture(t, config), source.Name)
	if entry := readCaptureManifest(t, config, source.Name).Files[0]; third.FilesCopied != 1 || third.VersionsPreserved != 0 || entry.SHA256 != original.SHA256 || len(entry.Previous) != 0 {
		t.Fatalf("new force generation reused old verification or created history: result=%+v entry=%+v", third, entry)
	}
	writeSourceFile(t, source.Path, codexLines("incremental-test", 3))
	config.CaptureRecoveryID = "changed"
	result := captureResult(t, runCapture(t, config), source.Name)
	entry := readCaptureManifest(t, config, source.Name).Files[0]
	if result.VersionsPreserved != 1 || len(entry.Previous) != 1 || entry.Previous[0].SHA256 != original.SHA256 {
		t.Fatalf("changed full recapture lost previous evidence: result=%+v entry=%+v", result, entry)
	}
	if previous := readText(t, capturedPath(config, source.Name, entry.Previous[0].Captured)); previous != codexLines("incremental-test", 2) {
		t.Fatal("preserved evidence does not contain the original transcript")
	}
	config.CaptureRecoveryID = "unchanged-3"
	result = captureResult(t, runCapture(t, config), source.Name)
	entry = readCaptureManifest(t, config, source.Name).Files[0]
	if result.FilesCopied != 1 || result.VersionsPreserved != 0 || len(entry.Previous) != 1 {
		t.Fatalf("unchanged full recapture duplicated genuine history: result=%+v entry=%+v", result, entry)
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

// stableCodexDirectory is a codex source of count stable transcripts and
// their units, ingested and each audited once, oldest check first.
func stableCodexDirectory(t *testing.T, count int) (*Server, []syncUnit) {
	t.Helper()
	directory := t.TempDir()
	for index := range count {
		path := filepath.Join(directory, fmt.Sprintf("rollout-%d.jsonl", index))
		writeSourceFile(t, path, codexLines(fmt.Sprintf("retry-%d", index), 3))
		stamp := time.Now().Add(-time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	source := SourceConfig{Name: "codex", Kind: "codex", Path: directory, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	adapter, _ := MakeAdapter(source)
	units := []syncUnit{}
	adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { units = append(units, unitOf(source, parts, false)); return true }, nil)
	if len(units) != count {
		t.Fatalf("units: %d", len(units))
	}
	for index, unit := range units {
		result := server.Catalog.auditUnit(t.Context(), unit, fmt.Sprint(index), automaticAuditBytes)
		if result.Outcome != "passed" {
			t.Fatal(result)
		}
		if err := server.Catalog.recordVerification(unit, result); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return server, units
}

// openIssueFoundBy records an open mismatch for unit found by the given index
// version, as its latest verification.
func openIssueFoundBy(t *testing.T, server *Server, unit syncUnit, version string) {
	t.Helper()
	detail := jsonText(map[string]any{"categories": []string{"metric_ledger"}, "index_version": version, "source_signature": partsSignature(unit.Parts)})
	if _, err := server.Catalog.DB.Exec(`INSERT INTO sync_integrity_issues(id,host_id,source_name,unit,first_at,last_at,state,detail_json) VALUES(?,?,?,?,?,?,'open',?)`,
		stableID("sync-integrity", unit.hostID(), unit.Source.Name, unit.ID), unit.hostID(), unit.Source.Name, unit.ID, now(), now(), detail); err != nil {
		t.Fatal(err)
	}
	if err := server.Catalog.recordVerification(unit, auditResult{Outcome: "mismatch"}); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticAuditRetriesOpenMismatchFoundByOtherCode(t *testing.T) {
	useHost(t, "host-a")
	server, units := stableCodexDirectory(t, 3)
	// The most recently checked unit: the rotation alone would reach it last.
	fixed := units[2]
	openIssueFoundBy(t, server, fixed, "codex:an-older-parser")
	if !server.runAutomatic(server.life.ctx, syncSettings{Enabled: true, Interval: 60}) {
		t.Fatal("automatic run not admitted")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='resolved' AND unit=?", fixed.ID) != 1 {
		t.Fatal("a mismatch found by older code was not audited again")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verifications WHERE outcome='passed' AND unit=?", fixed.ID) != 1 {
		t.Fatal("the retry was not recorded")
	}
}

func TestAutomaticAuditLeavesUnchangedMismatchToTheRotation(t *testing.T) {
	useHost(t, "host-a")
	server, units := stableCodexDirectory(t, 3)
	adapter, _ := MakeAdapter(units[2].Source)
	openIssueFoundBy(t, server, units[2], sourceIndexVersion(adapter))
	if !server.runAutomatic(server.life.ctx, syncSettings{Enabled: true, Interval: 60}) {
		t.Fatal("automatic run not admitted")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 1 {
		t.Fatal("a mismatch nothing changed for took the run's audit")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_verifications WHERE outcome='passed' AND unit=?", units[0].ID) != 1 {
		t.Fatal("the least recently checked unit was not audited")
	}
}

func TestRecoveryAuditsEveryOpenIssueAgain(t *testing.T) {
	useHost(t, "host-a")
	server, units := stableCodexDirectory(t, 3)
	adapter, _ := MakeAdapter(units[0].Source)
	for _, unit := range units[1:] {
		openIssueFoundBy(t, server, unit, sourceIndexVersion(adapter))
	}
	server.auditOpenIssues(t.Context(), "recovery", []string{"codex"})
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='resolved'") != 2 {
		t.Fatal("recovery left issues its rebuilt rows no longer show")
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
	server := testServer(t, config, catalog)
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

func TestSyncStatusExposesLowercaseSourceChoices(t *testing.T) {
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	readStatus := func(server *Server) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		server.getIncrementalSync(response, httptest.NewRequest("GET", "/api/sync/status", nil))
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		var status map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	status := readStatus(server)
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
	if status["library_empty"] != true || status["has_sources"] != true {
		t.Fatalf("configured sources before first index: %+v", status)
	}
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	if status := readStatus(server); status["library_empty"] != false || status["has_sources"] != true {
		t.Fatalf("indexed conversations must reveal page content: %+v", status)
	}
	disabled := source
	disabled.Enabled = false
	for name, sources := range map[string][]SourceConfig{"without configured sources": nil, "with only disabled sources": {disabled}} {
		catalog, config := testCatalog(t)
		config.Sources = sources
		if status := readStatus(testServer(t, config, catalog)); status["library_empty"] != true || status["has_sources"] != false {
			t.Fatalf("new library %s: %+v", name, status)
		}
	}
	// "Needs capture" still means indexed since the last capture, without the
	// opt-in retained acquisition; a later capture clears it.
	indexed := time.Now().UTC()
	if _, err := server.Catalog.DB.Exec("INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,indexed_at) VALUES(?,?,?,?,?)",
		currentHost().ID, source.Name, indexed.Format(time.RFC3339Nano), indexed.Format(time.RFC3339Nano), indexed.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	pending := func() any {
		t.Helper()
		response := httptest.NewRecorder()
		server.getIncrementalSync(response, httptest.NewRequest("GET", "/api/sync/status", nil))
		var status struct {
			SourceStates []map[string]any `json:"source_states"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || len(status.SourceStates) != 1 {
			t.Fatal(response.Body.String(), err)
		}
		return status.SourceStates[0]["pending_preservation"]
	}
	if pending() != true {
		t.Fatal("a source indexed after its last capture is not flagged as needing capture")
	}
	config := server.Config()
	config.Sources = []SourceConfig{source}
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	if pending() != false {
		t.Fatal("a capture after the index did not clear Needs capture")
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

type inventoryOrderAdapter struct {
	*jsonlAdapter
	planned, read func()
}

func (a *inventoryOrderAdapter) planParts() (*sourceWorkPlan, error) {
	plan, err := a.jsonlAdapter.planParts()
	if err != nil {
		return plan, err
	}
	a.planned()
	for i := range plan.groups {
		read := plan.groups[i].read
		plan.groups[i].read = func(emit partEmitter) error { a.read(); return read(emit) }
	}
	return plan, nil
}

func TestIncrementalDiscoversAllSourcesBeforeReadingAndRetainsForecasts(t *testing.T) {
	catalog, config := testCatalog(t)
	first, second := stableCodexSource(t, 1), stableCodexSource(t, 1)
	second.Name = "second"
	config.Sources = []SourceConfig{first, second}
	s := testServer(t, config, catalog)
	planned := map[string]bool{}
	reads := 0
	prior := syncAdapter
	override(t, &syncAdapter, func(config SourceConfig) (Adapter, error) {
		adapter, err := prior(config)
		if err != nil {
			return adapter, err
		}
		return &inventoryOrderAdapter{jsonlAdapter: adapter.(*jsonlAdapter), planned: func() { planned[config.Name] = true }, read: func() {
			reads++
			if len(planned) != 2 {
				t.Error("transcript parsing began before every source was discovered")
			}
		}}, nil
	})
	if !s.runAutomatic(t.Context(), syncSettings{Interval: 60}) || reads == 0 {
		t.Fatal("run did not execute its retained inventories")
	}
	history := s.forecastHistory(automaticRunKind)
	if len(history) != 1 || history[0].State != "complete" {
		t.Fatalf("operation outcome: %+v", history)
	}
	for _, step := range history[0].Steps {
		if step.ID != "audit" && step.State != "complete" && step.State != "skipped" {
			t.Errorf("operation completed with unfinished step: %+v", step)
		}
		if strings.HasPrefix(step.ID, "source:") && (!step.Inventory || step.Features.Groups != 1 || step.Features.Bytes <= 0) {
			t.Errorf("discovered workload lost: %+v", step)
		}
	}
	response := httptest.NewRecorder()
	s.getIncrementalSync(response, httptest.NewRequest("GET", "/api/sync/history", nil))
	var result struct {
		Samples  []syncMeasurement `json:"samples"`
		Accuracy map[string]any    `json:"forecast_accuracy"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Samples) != 1 || len(result.Samples[0].Forecasts) == 0 || result.Accuracy[automaticRunKind] == nil {
		t.Fatalf("forecast evidence missing from history API: %s (%v)", response.Body.String(), err)
	}
}

func TestAutomaticCaptureBusyDefersWithoutFailure(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 1)
	source.Options = map[string]any{"retained_input_capture": true}
	server := automaticTestServer(t, source)
	lock, err := beginCapture(server.Config())
	if err != nil {
		t.Fatal(err)
	}
	if !server.runAutomatic(t.Context(), syncSettings{Interval: 60}) {
		t.Fatal("automatic tick was not admitted")
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM automatic_source_states") != 0 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM source_item_states") != 0 {
		t.Fatal("capture contention advanced source state")
	}
	var encoded string
	if err := server.Catalog.DB.QueryRow("SELECT sample_json FROM sync_history ORDER BY id DESC LIMIT 1").Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var sample syncMeasurement
	if err := json.Unmarshal([]byte(encoded), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.State != "deferred" || len(sample.SourceResults) != 1 || !sample.SourceResults[0].Deferred || sample.SourceResults[0].Error != nil {
		t.Fatalf("contention was treated as failure: %+v", sample)
	}
	lock.Close()
	if !server.runAutomatic(t.Context(), syncSettings{Interval: 60}) || conversationMessages(t, server.Catalog, "incremental-test") != 1 {
		t.Fatal("pending input did not resume")
	}
}

func TestUnchangedIngestDoesNotCertifyLegacyEvidence(t *testing.T) {
	t.Run("live-planning-recovery", func(t *testing.T) {
		useHost(t, "host-a")
		source := stableCodexSource(t, 1)
		server := automaticTestServer(t, source)
		catalog := server.Catalog
		file := readText(t, source.Path)
		if err := os.Remove(source.Path); err != nil {
			t.Fatal(err)
		}
		adapter, _ := MakeAdapter(source)
		prepared := preparedSyncSource{adapter: adapter}
		server.prepareSyncSource(t.Context(), source, &prepared)
		if prepared.close != nil {
			prepared.close()
		}
		if prepared.err == nil {
			t.Fatal("missing live input did not fail planning")
		}
		if status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name); status["pending_newer_inputs"] != false {
			t.Fatalf("live planning failure requested capture: %#v", status)
		}
		// Heal pending errors written by builds that treated every plan failure
		// as an acquisition failure, including the unchanged ingest fast path.
		writeSourceFile(t, source.Path, file)
		for _, unchanged := range []bool{false, true} {
			if _, err := catalog.DB.Exec(`INSERT INTO source_input_states(host_id,source_name,preservation_mode,pending,error) VALUES('host-a',?,'live-rollout-gap',1,'old planning error') ON CONFLICT(host_id,source_name) DO UPDATE SET pending=1,error=excluded.error`, source.Name); err != nil {
				t.Fatal(err)
			}
			result := ingestSource(t, catalog, source)
			if result.Error != nil || result.SkippedUnchanged != unchanged {
				t.Fatal("successful live ingest", result)
			}
			status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name)
			if status["pending_newer_inputs"] != false || status["error"] != nil {
				t.Fatalf("successful ingest left stale capture status: %#v", status)
			}
		}
		// Reading live data cannot acknowledge a different retained revision.
		if _, err := catalog.DB.Exec(`UPDATE source_input_states SET acquired_revision='unindexed-evidence',pending=1 WHERE host_id='host-a' AND source_name=?`, source.Name); err != nil {
			t.Fatal(err)
		}
		if result := ingestSource(t, catalog, source); result.Error != nil {
			t.Fatal(result)
		}
		if status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name); status["pending_newer_inputs"] != true {
			t.Fatalf("live index acknowledged unindexed retained evidence: %#v", status)
		}
	})
	for _, reason := range []string{"force", "generation", "parser"} {
		t.Run("legacy-reparse/"+reason, func(t *testing.T) {
			useHost(t, "host-a")
			source := stableCodexSource(t, 1)
			catalog, _ := testCatalog(t)
			ingestSource(t, catalog, source)
			execSQL(t, catalog.DB, "UPDATE messages SET text='stale parser output' WHERE kind='message'")
			source.Options = map[string]any{"retained_input_capture": true}
			mode := ingestMode{force: reason == "force"}
			if reason == "generation" {
				if err := catalog.advanceSyncGeneration(t.Context(), "host-a", source.Name); err != nil {
					t.Fatal(err)
				}
			}
			if reason == "parser" {
				execTx(t, catalog.DB, "UPDATE source_item_states SET extractor='older-parser'", "UPDATE source_states SET index_version='older-parser'", "UPDATE source_record_states SET digest='older-parser:old'")
			}
			adapter, err := MakeAdapter(source)
			if err != nil {
				t.Fatal(err)
			}
			result := catalog.IngestContext(withIngestMode(t.Context(), mode), adapter, nil)
			if result.Error != nil || result.Older != 0 || result.Workspaces != 1 || countRows(t, catalog, "SELECT COUNT(*) FROM copy_evidence") == 0 {
				t.Fatal("legacy reparse was blocked", result)
			}
			if countRows(t, catalog, "SELECT COUNT(*) FROM messages WHERE text='stale parser output'") != 0 {
				t.Fatal("legacy parser output survived reparse")
			}
		})
	}
	useHost(t, "host-a")
	source := stableCodexSource(t, 1)
	catalog, _ := testCatalog(t)
	ingestSource(t, catalog, source)
	execSQL(t, catalog.DB, "DELETE FROM copy_evidence")
	result := ingestSource(t, catalog, source)
	if !result.SkippedUnchanged || countRows(t, catalog, "SELECT COUNT(*) FROM copy_evidence") != 0 {
		t.Fatalf("no-op certified historical content: %+v", result)
	}
	status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name)
	if status["status"] != "untracked" || status["pending_newer_inputs"] != false {
		t.Fatalf("ordinary live indexing claimed preservation or requested capture: %#v", status)
	}
	for _, key := range []string{"indexed_copies", "preserved_copies", "legacy_preservation_gaps"} {
		if _, present := status[key]; present {
			t.Fatalf("polling status included historical coverage %s: %#v", key, status)
		}
	}
	source.Options = map[string]any{"retained_input_capture": true}
	if err := catalog.recordInputAcquisition("host-a", source, "retained-revision", nil); err != nil {
		t.Fatal(err)
	}
	if status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name); status["status"] != "acquired" || status["pending_newer_inputs"] != true {
		t.Fatalf("missing pending acquisition: %#v", status)
	}
	if err := catalog.recordInputIndexed("host-a", source.Name, "retained-revision"); err != nil {
		t.Fatal(err)
	}
	if status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name); status["status"] != "indexed" || status["pending_newer_inputs"] != false {
		t.Fatalf("missing indexed acquisition: %#v", status)
	}
}
