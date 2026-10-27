package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type statusSnapshot struct {
	Library    map[string]any   `json:"library"`
	Eject      ejectVerdict     `json:"eject"`
	Operations []map[string]any `json:"operations"`
	Attention  []attentionItem  `json:"attention"`
	Features   map[string]any   `json:"features"`
	raw        map[string]any
}

func getStatus(t *testing.T, server *Server, path string) statusSnapshot {
	t.Helper()
	response := serveTest(server, http.MethodGet, path)
	if response.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
	}
	var document statusSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(response.Body.Bytes(), &document.raw)
	return document
}

// The status snapshot (§7.1) composes the drive, the verdict, the top-level
// operations, the attention list, the freshness ledger and the resources of
// its processes. A cloud upload is a batch writer: safe to eject
// after its current unit. A Terminal command's lease makes the drive busy and
// lists its work, which Pharos cannot stop.
func TestStatusSnapshotComposesVerdictOperationsAndPlaceholders(t *testing.T) {
	override(t, &statusCoalesce, 10*time.Millisecond)
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	idle := getStatus(t, server, "/api/status")
	// Resources (§10): this service, read by the kernel, with the server's
	// Load line; CPU from the second reading on.
	override(t, &resourceSampleInterval, 0)
	resources, _ := idle.raw["resources"].(map[string]any)
	processes, _ := resources["processes"].([]any)
	if len(processes) != 1 || processes[0].(map[string]any)["pid"] != float64(os.Getpid()) || processes[0].(map[string]any)["role"] != "service" ||
		!(resources["memory_bytes"].(float64) > 0) || resources["basis"] != resourceBasis || resources["sampled_at"] == nil ||
		!strings.HasPrefix(firstString(resources["label"]), "Pharos is using ") {
		t.Fatalf("the snapshot carries this service's resources: %#v", idle.raw["resources"])
	}
	// The next computed document (the hub reuses one for statusCoalesce).
	var again map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && again["cpu_percent"] == nil; {
		again = getStatus(t, server, "/api/status").raw["resources"].(map[string]any)
	}
	if again["cpu_percent"] == nil {
		t.Fatalf("a second reading gives CPU: %#v", again)
	}
	if headings, _ := idle.raw["headings"].(map[string]any); headings["work"] != "Work" || headings["data"] != "Data" {
		t.Fatalf("Show all's headings are the server's: %#v", idle.raw["headings"])
	}
	if rows, ok := idle.raw["freshness"].([]any); !ok || len(rows) == 0 {
		t.Fatalf("freshness lists the ledger's domains (§5): %#v", idle.raw["freshness"])
	}
	if summary, ok := idle.raw["freshness_summary"].(map[string]any); !ok || len(summary["groups"].([]any)) == 0 {
		t.Fatalf("freshness_summary groups them for the panel: %#v", idle.raw["freshness_summary"])
	}
	if idle.Eject.Verdict != verdictSafe || idle.Eject.Checked != "polled" || len(idle.Operations) != 0 || len(idle.Attention) != 0 {
		t.Fatalf("an idle library: %+v %#v %#v", idle.Eject, idle.Operations, idle.Attention)
	}
	if _, ok := idle.Library["drive"].(map[string]any)["location"]; !ok {
		t.Fatalf("the drive says where it is: %#v", idle.Library)
	}
	if idle.Eject.Headline == "" || idle.Eject.Summary == "" {
		t.Fatalf("the verdict has a short headline and a full summary: %+v", idle.Eject)
	}
	// A polled snapshot takes no capture or index lock (§4.2); only the
	// release path's fresh check scans them.
	scans, probes := lockScans.Load(), lockProbes.Load()
	for range 3 {
		getStatus(t, server, "/api/status")
	}
	if lockScans.Load() != scans || lockProbes.Load() != probes {
		t.Fatalf("polled reads scanned %d and probed %d locks", lockScans.Load()-scans, lockProbes.Load()-probes)
	}
	getStatus(t, server, "/api/status?fresh=1")
	if lockScans.Load() == scans {
		t.Fatal("the fresh check scanned no lock")
	}
	if idle.Features["unified_activity"] != false {
		t.Fatalf("the flag defaults to off: %#v", idle.Features)
	}
	if _, ok := idle.raw["journal"].(map[string]any)["recent"]; ok {
		t.Fatal("journal history belongs to /api/operations, not the status snapshot")
	}

	_, upload := server.begin(context.Background(), OperationSpec{Kind: "cloud.upload", Variant: "full", Subject: "d1", Trigger: triggerUser, Legacy: "run-1"})
	upload.Step("Uploading", "byte", 1000)
	upload.Advance(250)
	busy := getStatus(t, server, "/api/status")
	if busy.Eject.Verdict != verdictSafeIn || busy.Eject.ETASeconds != nil || len(busy.Operations) != 1 {
		t.Fatalf("a cloud upload: %+v %#v", busy.Eject, busy.Operations)
	}
	entry := busy.Operations[0]
	step, _ := entry["step"].(map[string]any)
	if entry["kind"] != "cloud.upload" || entry["label"] == "" || entry["state_label"] == "" || entry["stoppable"] != true || step["fraction"] != 0.25 {
		t.Fatalf("the upload's entry carries the server's words and its measured fraction: %#v", entry)
	}

	// Another process's Terminal capture, under its lease.
	lease, err := acquireProcessLease(leaseDirs(config)[0], leaseInfo{Instance: "cli-capture", PID: os.Getpid(), HostID: currentHost().ID, Process: "cli:capture",
		Kinds: []string{"capture.cli"}, Holds: []Hold{{Resource: "drive", Mode: "write"}, {Resource: "capture:" + currentHost().ID, Mode: "write"}}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	cli := getStatus(t, server, "/api/status?fresh=1")
	if cli.Eject.Verdict != verdictBusy || cli.Eject.Checked != "fresh" {
		t.Fatalf("a Terminal capture makes eject wait: %+v", cli.Eject)
	}
	var foreign map[string]any
	for _, operation := range cli.Operations {
		if strings.HasPrefix(firstString(operation["process"]), "cli:") {
			foreign = operation
		}
	}
	if foreign == nil || foreign["stoppable"] != false || foreign["kind"] != "capture.cli" {
		t.Fatalf("the Terminal capture is listed and cannot be stopped from Pharos: %#v", cli.Operations)
	}
	lease.release()
	// A Terminal command in its own process counts in the library's
	// resources through its lease; one on another Mac (a shared drive) does
	// not.
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	ingest, err := acquireProcessLease(leaseDirs(config)[0], leaseInfo{Instance: "cli-ingest", PID: command.Process.Pid, HostID: currentHost().ID, Process: "cli:ingest",
		Kinds: []string{"ingest.cli"}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	defer ingest.release()
	elsewhere, err := acquireProcessLease(leaseDirs(config)[0], leaseInfo{Instance: "cli-elsewhere", PID: os.Getppid(), HostID: "another-mac", Process: "cli:ingest",
		Kinds: []string{"ingest.cli"}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	defer elsewhere.release()
	roles := map[float64]string{}
	for _, process := range getStatus(t, server, "/api/status?fresh=1").raw["resources"].(map[string]any)["processes"].([]any) {
		roles[process.(map[string]any)["pid"].(float64)] = firstString(process.(map[string]any)["role"])
	}
	if want := map[float64]string{float64(os.Getpid()): "service", float64(command.Process.Pid): "cli:ingest"}; runtime.GOOS == "darwin" && !maps.Equal(roles, want) {
		t.Fatalf("resources list the service and this Mac's Terminal command: %v", roles)
	}
	upload.Finish(nil)
}

// The flag is read through a short cache that saving preferences clears.
func TestUnifiedActivityFlagFollowsSavedPreferences(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	override(t, &featureFlagTTL, time.Hour)
	ctx := context.Background()
	if catalog.unifiedActivity(ctx) {
		t.Fatal("the flag defaults to off")
	}
	if response := serveTestBody(server, http.MethodPost, "/api/preferences", `{"set":{"pharos-unified-activity-v1":true}}`); response.Code != http.StatusOK {
		t.Fatalf("saving: %d %s", response.Code, response.Body.String())
	}
	if !catalog.unifiedActivity(ctx) {
		t.Fatal("a saved flag is read at once, not after the cache expires")
	}
	if getStatus(t, server, "/api/status").Features["unified_activity"] != true {
		t.Fatal("the snapshot reports the flag")
	}
	var menu map[string]any
	_ = json.Unmarshal(serveTest(server, http.MethodGet, "/api/menu-bar/status").Body.Bytes(), &menu)
	if menu["unified_activity"] != true {
		t.Fatalf("the native menu's poll carries the flag: %#v", menu)
	}
	if err := catalog.SetUIPreferences(ctx, map[string]any{"set": map[string]any{unifiedActivityFlag: "yes"}}); err != nil {
		t.Fatal(err)
	}
	if catalog.unifiedActivity(ctx) {
		t.Fatal("only the boolean true turns the flag on")
	}
}

// The attention rules (§7.3) list only what the user can act on, each with
// server words, a severity and where to act.
func TestAttentionListsWhatTheUserCanActOn(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{{Name: "codex", Kind: "codex", Enabled: true}, {Name: "cursor", Kind: "cursor"}}
	server := testServer(t, config, catalog)
	rules := func() map[string]attentionItem {
		t.Helper()
		found := map[string]attentionItem{}
		for _, item := range server.attention(context.Background()) {
			if item.Title == "" || item.Action.Label == "" || !strings.HasPrefix(item.Action.Path, "/settings") || item.Severity != "error" && item.Severity != "warning" {
				t.Fatalf("an item without words, severity or action: %+v", item)
			}
			found[item.ID] = item
		}
		return found
	}
	if found := rules(); len(found) != 0 {
		t.Fatalf("a new library needs nothing: %+v", found)
	}
	execTx(t, catalog.DB,
		// Rule 1: an enabled source's latest attempt failed; a disabled
		// source's failure and a stop's interruption do not count.
		`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,error,updated_at) VALUES
			('host-a','codex','codex','full','full','2026-10-27T10:00:00Z','could not read sessions','2026-10-27T10:00:00Z'),
			('host-a','cursor','cursor','full','full','2026-10-27T10:00:00Z','disabled source error','2026-10-27T10:00:00Z')`,
		// Rule 2: an open verification mismatch.
		`INSERT INTO sync_integrity_issues(id,host_id,source_name,unit,first_at,last_at,state,detail_json) VALUES('i1','host-a','codex','a.jsonl','2026-10-27T10:00:00Z','2026-10-27T10:00:00Z','open','{}')`)
	server.runsMu.Lock()
	server.runs = []*SyncRun{{ID: "r1", Kind: "manual-sync", State: "failed", Sources: []string{"codex"}, Error: "the drive went away"}}
	server.runsMu.Unlock()
	server.backups.runs = []*BackupRun{{ID: "b1", State: "failed", Errors: []captureError{{Error: "destination full"}}}}
	found := rules()
	for _, id := range []string{"source-error", "integrity", "index-failed", "backup-failed"} {
		if _, ok := found[id]; !ok {
			t.Fatalf("%s is missing: %+v", id, found)
		}
	}
	if strings.Contains(found["source-error"].Detail, "cursor") || !strings.Contains(found["source-error"].Detail, "codex") {
		t.Fatalf("only enabled sources count: %+v", found["source-error"])
	}
	if found["backup-failed"].Action.Path != "/settings/backups" {
		t.Fatalf("a failed backup opens Backups: %+v", found["backup-failed"])
	}

	// A run the user stopped is not a failure; an interruption that was not
	// a stop is a warning; neither counts while an index runs.
	server.runsMu.Lock()
	server.runs = []*SyncRun{{ID: "r2", Kind: "manual-sync", State: "failed", StopRequested: true, Sources: []string{"codex"}}}
	server.runsMu.Unlock()
	if _, ok := rules()["index-failed"]; ok {
		t.Fatal("a stopped index is listed as failed")
	}
	server.runsMu.Lock()
	server.runs = []*SyncRun{{ID: "r3", Kind: "manual-sync", State: "interrupted", Sources: []string{"codex"}}}
	server.runsMu.Unlock()
	if item, ok := rules()["index-interrupted"]; !ok || item.Severity != "warning" {
		t.Fatalf("an interrupted index: %+v", rules())
	}
	server.runsMu.Lock()
	server.runs = []*SyncRun{{ID: "r4", Kind: "manual-sync", State: "running"}, {ID: "r3", Kind: "manual-sync", State: "interrupted"}}
	server.runsMu.Unlock()
	if _, ok := rules()["index-interrupted"]; ok {
		t.Fatal("an earlier interruption counts while another index runs")
	}
	if status := getStatus(t, server, "/api/status"); len(status.Attention) != len(rules()) {
		t.Fatalf("the snapshot lists every item: %+v", status.Attention)
	}

	// Rule 5: the latest capture failed for an enabled source.
	server.captures.runs = []*CaptureRun{{ID: "c1", State: "failed", Sources: []string{"codex"}, Results: []CaptureSourceResult{{Name: "codex", State: "failed"}}}}
	if _, ok := rules()["capture-failed"]; !ok {
		t.Fatal("a failed capture is missing")
	}
	server.captures.runs = []*CaptureRun{{ID: "c2", State: "failed", Sources: []string{"cursor"}, Results: []CaptureSourceResult{{Name: "cursor", State: "failed"}}}}
	if _, ok := rules()["capture-failed"]; ok {
		t.Fatal("a disabled source's failed capture counts")
	}
	// Rule 7: a scheduled cloud backup failed, and the latest verification.
	execTx(t, catalog.DB,
		`INSERT INTO cloud_destinations(id,config_json,credential_json,imported_at) VALUES('d1','{}','{}','2026-10-27T10:00:00Z')`,
		`INSERT INTO cloud_backup_schedules(destination_id,state_json) VALUES('d1','{"destination_id":"d1","enabled":true,"failures":2,"last_error":"access denied"}')`,
		`INSERT INTO cloud_backup_runs(id,destination_id,status_json,created_at) VALUES
			('r-old','d1','{"id":"r-old","destination_id":"d1","verification_state":"complete"}','2026-10-26T10:00:00Z'),
			('r-new','d1','{"id":"r-new","destination_id":"d1","verification_state":"failed","verification_error":"checksum mismatch"}','2026-10-27T10:00:00Z')`)
	found = rules()
	if item := found["cloud-schedule-failed:d1"]; item.Detail != "access denied" || item.Action.Path != "/settings/backups" {
		t.Fatalf("a failed cloud schedule: %+v", found)
	}
	if item := found["cloud-verify-failed:d1"]; item.Detail != "checksum mismatch" {
		t.Fatalf("a failed verification: %+v", found)
	}

	// Rule 9: a reader keeps the checkpoint from folding in a large WAL.
	catalog.wal.pin.Store(&walPin{Since: time.Now().Add(-3 * time.Hour), WALBytes: 14 << 30, Pending: 3_400_000, Holder: "pid 4242 (pharos labeling prepare --dry-run)"})
	item := rules()["wal-pinned"]
	if item.Severity != "warning" || item.Rule != 9 || !strings.Contains(item.Detail, "15.0 GB") || !strings.Contains(item.Detail, "3 hours") || !strings.Contains(item.Detail, "pharos labeling prepare") {
		t.Fatalf("a pinned WAL: %+v", item)
	}
	catalog.noteWALCheckpointed()
	if _, ok := rules()["wal-pinned"]; ok {
		t.Fatal("a pinned WAL is listed after a checkpoint")
	}

	// After a restart or crash this process has no runs; the journal still
	// says the last index was interrupted (the reconcile pass ends a dead
	// process's row so), and an in-memory run that ended later wins.
	restarted := testServer(t, config, catalog)
	execTx(t, catalog.DB, `INSERT INTO operation_journal(id,root_id,kind,trigger,host_id,process,pid,instance,state,reason,started_at,updated_at,ended_at,legacy_ref)
		VALUES('op-crashed','op-crashed','index.changes.manual','user','host-a','service',1,'dead','interrupted','owner-exited','2026-10-27T09:00:00Z','2026-10-27T09:05:00Z','2026-10-27T09:05:00Z','run-crashed')`)
	after := map[string]attentionItem{}
	for _, item := range restarted.attention(context.Background()) {
		after[item.ID] = item
	}
	if item, ok := after["index-interrupted"]; !ok || item.Severity != "warning" {
		t.Fatalf("a crash during an index is forgotten on restart: %+v", after)
	}
	execTx(t, catalog.DB, `UPDATE operation_journal SET state='failed',error='the drive went away' WHERE id='op-crashed'`)
	if item := firstItem(restarted.attention(context.Background()), "index-failed"); item == nil || item.Detail != "the drive went away" {
		t.Fatal("a failed index is forgotten on restart")
	}
	// As in memory: a failure of a source that is not enabled is not shown,
	// and neither is an interruption by a release (eject, quit, switch).
	execTx(t, catalog.DB, `UPDATE operation_journal SET detail_json='{"sources":["cursor"]}' WHERE id='op-crashed'`)
	if item := firstItem(restarted.attention(context.Background()), "index-failed"); item != nil {
		t.Fatalf("a journaled failure of a disabled source: %+v", item)
	}
	execTx(t, catalog.DB, `UPDATE operation_journal SET state='interrupted',reason='released',error=NULL WHERE id='op-crashed'`)
	if item := firstItem(restarted.attention(context.Background()), "index-interrupted"); item != nil {
		t.Fatalf("an index the user's eject interrupted: %+v", item)
	}
	// Both are what the journal records: a mirrored run's sources, and a
	// release's reason.
	ctx, cancelRun := context.WithCancel(context.Background())
	_, run := restarted.begin(ctx, OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser, Legacy: "run-9"})
	run.mirrorRun(&SyncRun{ID: "run-9", State: "running", Sources: []string{"codex"}})
	if image := imageOf(run, run.view()); !strings.Contains(image.detail, `"sources":["codex"]`) {
		t.Fatalf("the journal row's detail lacks the run's sources: %s", image.detail)
	}
	restarted.ops.stopping(triggerUser)
	cancelRun()
	run.Finish(ctx.Err())
	if view := run.view(); view.State != opInterrupted || view.Reason != "released" {
		t.Fatalf("an index a release stopped: %s (%s)", view.State, view.Reason)
	}
	// A stop nobody asked for (the app exited, the listener failed) is not
	// the user's doing: its interruption is reported after a restart.
	for _, trigger := range []string{triggerParentExited, triggerError} {
		restarted.ops.stopping(trigger)
		ctx, cancel := context.WithCancel(context.Background())
		_, cut := restarted.begin(ctx, OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser})
		cancel()
		cut.Finish(ctx.Err())
		if view := cut.view(); view.Reason != "service-stopped" {
			t.Fatalf("an index stopped by %s ended with reason %q", trigger, view.Reason)
		}
	}
	execTx(t, catalog.DB, `UPDATE operation_journal SET reason='service-stopped' WHERE id='op-crashed'`)
	if item := firstItem(restarted.attention(context.Background()), "index-interrupted"); item == nil {
		t.Fatal("an index the app's exit interrupted is forgotten on restart")
	}
	restarted.ops.stopping("")
	restarted.runsMu.Lock()
	restarted.runs = []*SyncRun{{ID: "r9", Kind: "manual-sync", State: "complete", CompletedAt: "2026-10-27T11:00:00Z"}}
	restarted.runsMu.Unlock()
	if item := firstItem(restarted.attention(context.Background()), "index-failed"); item != nil {
		t.Fatalf("a later successful index still shows the journal's failure: %+v", item)
	}
}

func firstItem(items []attentionItem, id string) *attentionItem {
	for _, item := range items {
		if item.ID == id {
			return &item
		}
	}
	return nil
}

// An idle daemon pass is registered tentatively before it looks for work. A
// polled verdict does not count it unless it may be writing, so an idle
// library does not flicker between safe and safe-in; the fresh verdict of
// the release path always counts it.
func TestPolledVerdictIgnoresAnIdleTentativePass(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	pass := catalog.beginPass(context.Background(), "maintenance.library-view", triggerChained)
	defer pass.end(nil)
	if verdict := server.ejectVerdictNow(context.Background(), false); verdict.Verdict != verdictSafe {
		t.Fatalf("an idle pass makes the polled verdict %q", verdict.Verdict)
	}
	if verdict := server.ejectVerdictNow(context.Background(), true); verdict.Verdict != verdictSafeIn {
		t.Fatalf("the fresh verdict must count a pending pass: %q", verdict.Verdict)
	}
	untrack := catalog.trackWriter("library-refresh")
	if verdict := server.ejectVerdictNow(context.Background(), false); verdict.Verdict != verdictSafeIn {
		t.Fatalf("a pass that may be writing must count: %q", verdict.Verdict)
	}
	untrack()
	pass.worked(1) // published: an ordinary running operation
	if verdict := server.ejectVerdictNow(context.Background(), false); verdict.Verdict != verdictSafeIn {
		t.Fatalf("a pass that has worked counts: %q", verdict.Verdict)
	}
}

// Every reader shares one computation: concurrent readers of an unchanged
// registry get the same frame, and a change is computed again only once the
// coalescing window has passed.
func TestStatusHubSharesOneComputation(t *testing.T) {
	override(t, &statusCoalesce, 50*time.Millisecond)
	var hub statusHub
	var computed atomic.Int32
	var revision atomic.Uint64
	compute := func() map[string]any {
		computed.Add(1)
		return map[string]any{"revision": revision.Load(), "generated_at": time.Now().String()}
	}
	var wait sync.WaitGroup
	for range 10 {
		wait.Go(func() {
			hub.frame(context.Background(), statusRead{maxAge: time.Minute, revision: revision.Load, compute: compute})
		})
	}
	wait.Wait()
	if computed.Load() != 1 {
		t.Fatalf("10 readers computed %d documents", computed.Load())
	}
	first := hub.frame(context.Background(), statusRead{maxAge: time.Minute, revision: revision.Load, compute: compute})
	revision.Add(1)
	started := time.Now()
	second := hub.frame(context.Background(), statusRead{maxAge: time.Minute, revision: revision.Load, compute: compute})
	if computed.Load() != 2 || first.tag == second.tag {
		t.Fatalf("a change is computed again: %d", computed.Load())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("waited %s", elapsed)
	}
	// generated_at alone never changes the tag, nested ones included.
	if statusTag(map[string]any{"a": 1, "generated_at": "x", "libraries": []any{map[string]any{"snapshot": map[string]any{"generated_at": "1"}}}}) !=
		statusTag(map[string]any{"a": 1, "generated_at": "y", "libraries": []any{map[string]any{"snapshot": map[string]any{"generated_at": "2"}}}}) {
		t.Fatal("the tag depends on generated_at")
	}
	// A slow computation runs outside the hub's lock: a reader whose request
	// ends gives up with the cached frame instead of waiting for it.
	release := make(chan struct{})
	revision.Add(1)
	go hub.frame(context.Background(), statusRead{maxAge: time.Minute, revision: revision.Load, compute: func() map[string]any { <-release; return compute() }})
	waitUntil(t, 5*time.Second, "the slow computation to start", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return hub.flight != nil })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started = time.Now()
	if frame := hub.frame(ctx, statusRead{maxAge: time.Minute, revision: revision.Load, compute: compute}); frame.tag != second.tag || time.Since(started) > 2*time.Second {
		t.Fatalf("a waiter blocked on a slow computation for %s", time.Since(started))
	}
	close(release)
	// A launched computation runs on: a reader past its deadline gets the
	// cached frame while it finishes.
	slow := make(chan struct{})
	revision.Add(1)
	started = time.Now()
	frame := hub.frame(context.Background(), statusRead{maxAge: time.Minute, deadline: 50 * time.Millisecond, revision: revision.Load,
		compute: func() map[string]any { <-slow; return compute() }, launch: func(run func()) bool { go run(); return true }})
	if frame.data == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("a reader past its deadline waited %s", time.Since(started))
	}
	close(slow)
	// Documents are never published less than statusCoalesce apart, counted
	// from the publish before (server times, not when a client hears).
	// Computations of uneven length (40 ms, then none) would bring two
	// publishes closer than the window if it counted from the start.
	published := []time.Time{}
	for index := range 6 {
		revision.Add(1)
		uneven := func() map[string]any {
			if index%2 == 0 {
				time.Sleep(40 * time.Millisecond)
			}
			return compute()
		}
		hub.frame(context.Background(), statusRead{maxAge: time.Minute, revision: revision.Load, compute: uneven})
		hub.mu.Lock()
		published = append(published, hub.published)
		hub.mu.Unlock()
	}
	for index := 1; index < len(published); index++ {
		if gap := published[index].Sub(published[index-1]); gap < statusCoalesce {
			t.Fatalf("two documents published %s apart, inside the %s window", gap, statusCoalesce)
		}
	}
}

// The stream sends the document on connect and again when an operation
// starts.
func TestStatusStreamSendsOnConnectAndOnChange(t *testing.T) {
	override(t, &statusCoalesce, 200*time.Millisecond)
	override(t, &statusRecheck, 300*time.Millisecond)
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	listener := httptest.NewServer(server)
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, listener.URL+"/api/status/stream", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", response.Header.Get("Content-Type"))
	}
	events := make(chan statusSnapshot, 64)
	go func() {
		reader := bufio.NewReader(response.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(events)
				return
			}
			if data, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "data: "); ok {
				var document statusSnapshot
				if json.Unmarshal([]byte(data), &document) == nil {
					events <- document
				}
			}
		}
	}()
	next := func(what string) statusSnapshot {
		t.Helper()
		select {
		case document, ok := <-events:
			if !ok {
				t.Fatalf("the stream ended before %s", what)
			}
			return document
		case <-time.After(5 * time.Second):
			t.Fatalf("no event for %s", what)
		}
		return statusSnapshot{}
	}
	if first := next("the connect"); len(first.Operations) != 0 {
		t.Fatalf("first event: %#v", first.Operations)
	}
	_, backup := server.begin(context.Background(), OperationSpec{Kind: "backup.local", Trigger: triggerUser})
	defer backup.Finish(nil)
	for {
		if document := next("the backup"); len(document.Operations) == 1 {
			break
		}
	}
	// Coalescing: 40 changes over 800 ms give at most one event per window.
	backup.Step("Copying", "byte", 1000)
	computedBefore := server.status.computations.Load()
	stamps := []time.Time{}
	done := time.After(800 * time.Millisecond)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for changing := true; changing; {
		select {
		case <-ticker.C:
			backup.Advance(1)
		case <-events:
			stamps = append(stamps, time.Now())
		case <-done:
			changing = false
		}
	}
	computed := server.status.computations.Load() - computedBefore
	if computed < 2 || computed > 6 || len(stamps) > int(computed) {
		t.Fatalf("40 changes in 800 ms with a 200 ms window: %d documents computed, %d events", computed, len(stamps))
	}
	// A lease that appears changes no registry revision; the recheck finds it.
	lease, err := acquireProcessLease(leaseDirs(config)[0], leaseInfo{Instance: "cli-index", PID: os.Getpid(), HostID: currentHost().ID, Process: "cli:index",
		Kinds: []string{"ingest.cli"}, Holds: []Hold{{Resource: "drive", Mode: "write"}, {Resource: "catalog-writer", Mode: "write"}}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if document := next("the lease"); document.Eject.Verdict == verdictBusy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream never saw the lease")
		}
	}
	cancel()
}

// The broker's status document (§7.2) lists every registered library with
// its service's snapshot and attention, adds rule 8 for a selected library
// whose service is not running, and with no library at all is empty, not
// busy.
func TestBrokerStatusDocument(t *testing.T) {
	broker := newBrokerOperations(filepath.Join(t.TempDir(), "operations.json"))
	empty := broker.statusDocument(context.Background())
	if libraries, _ := empty["libraries"].([]map[string]any); len(libraries) != 0 {
		t.Fatalf("no library: %#v", empty["libraries"])
	}
	if attention, _ := empty["attention"].([]attentionItem); len(attention) != 0 {
		t.Fatalf("no library needs no attention: %#v", empty["attention"])
	}
	var freshAsked atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("fresh") == "1" {
			freshAsked.Add(1)
		}
		writeJSON(w, map[string]any{"version": 1, "operations": []any{}, "eject": computeEjectVerdict(verdictInput{fresh: r.URL.Query().Get("fresh") == "1"}),
			"attention": []attentionItem{{ID: "backup-failed", Rule: 6, Severity: "error", Title: "The last backup failed", Action: openBackups}}}, http.StatusOK)
	}))
	defer service.Close()
	broker.publish([]brokerLibrary{
		{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()},
		{ID: "lib-b", Name: "Archive"},
	})
	document := broker.statusDocument(context.Background())
	attention, _ := document["attention"].([]attentionItem)
	if len(attention) != 1 || attention[0].Library != "lib-a" || attention[0].ID != "backup-failed" {
		t.Fatalf("the union of the libraries' items, each naming its library: %+v", attention)
	}
	broker.publish([]brokerLibrary{{ID: "lib-b", Name: "Archive", Selected: true}})
	rule8 := func() []attentionItem {
		items, _ := broker.statusDocument(context.Background())["attention"].([]attentionItem)
		return items
	}
	if items := rule8(); len(items) != 0 {
		t.Fatalf("one document without a service is not yet a problem: %+v", items)
	}
	if items := rule8(); len(items) != 1 || items[0].Rule != 8 || items[0].Library != "lib-b" {
		t.Fatalf("a selected library without a service, twice: %+v", items)
	}
	// Not while a broker operation touches it (a transfer stops its
	// service on purpose), and the count starts again afterwards.
	transfer := broker.begin(OperationSpec{Kind: "library.transfer", Variant: "copy"}, "lib-b")
	if items := rule8(); len(items) != 0 {
		t.Fatalf("rule 8 during a transfer: %+v", items)
	}
	document = broker.statusDocument(context.Background())
	if entries, _ := document["libraries"].([]map[string]any); entries[0]["state_label"] == "" || entries[0]["state_label"] == nil {
		t.Fatalf("a library touched by a transfer has a state word: %#v", entries)
	}
	transfer.Finish(nil)
	if items := rule8(); len(items) != 0 {
		t.Fatalf("the count did not start again: %+v", items)
	}
	// The endpoint answers without the broker's main lock.
	m := &applicationManager{token: "broker-token", id: "installation", operations: broker, children: map[string]*applicationChild{}}
	m.mu.Lock()
	defer m.mu.Unlock()
	request := httptest.NewRequest(http.MethodGet, "/api/app/status", nil)
	request.AddCookie(&http.Cookie{Name: "pharos_app_installation", Value: "broker-token"})
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { m.ServeHTTP(response, request); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("/api/app/status waited for the broker's main lock")
	}
	if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}

	// The panel's Eject in the app: the broker asks the library's service for
	// its fresh verdict, at this moment, and says so.
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()}})
	fresh := httptest.NewRecorder()
	freshRequest := httptest.NewRequest(http.MethodGet, "/api/app/status?fresh=1", nil)
	freshRequest.AddCookie(&http.Cookie{Name: "pharos_app_installation", Value: "broker-token"})
	m.ServeHTTP(fresh, freshRequest)
	var freshDocument struct {
		Checked   string `json:"checked"`
		Libraries []struct {
			ID    string       `json:"id"`
			Eject ejectVerdict `json:"eject"`
		} `json:"libraries"`
	}
	if err := json.Unmarshal(fresh.Body.Bytes(), &freshDocument); err != nil || freshDocument.Checked != "fresh" || freshAsked.Load() != 1 ||
		len(freshDocument.Libraries) != 1 || freshDocument.Libraries[0].Eject.Checked != "fresh" {
		t.Fatalf("a fresh broker check: %s (fresh asked %d)", fresh.Body.String(), freshAsked.Load())
	}

	freshEject := func() ejectVerdict {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/app/status?fresh=1", nil)
		request.AddCookie(&http.Cookie{Name: "pharos_app_installation", Value: "broker-token"})
		m.ServeHTTP(recorder, request)
		var document struct {
			Libraries []struct {
				ID    string       `json:"id"`
				Eject ejectVerdict `json:"eject"`
			} `json:"libraries"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil || len(document.Libraries) != 1 || document.Libraries[0].ID != "lib-a" {
			t.Fatalf("a fresh check lists only the selected library: %s", recorder.Body.String())
		}
		return document.Libraries[0].Eject
	}
	// A broker operation touching the library makes it busy, with the
	// operation as the blocker, in the fresh check and the polled document.
	transfer = broker.begin(OperationSpec{Kind: "library.transfer", Variant: "copy"}, "lib-a", "lib-b")
	if verdict := freshEject(); verdict.Verdict != verdictBusy || verdict.Checked != "fresh" || !slices.ContainsFunc(verdict.Blockers, func(blocker ejectBlocker) bool { return blocker.Kind == "library.transfer" }) {
		t.Fatalf("a transfer touching the library, fresh: %+v", verdict)
	}
	polled, _ := broker.statusDocument(context.Background())["libraries"].([]map[string]any)
	if verdict, _ := polled[0]["eject"].(ejectVerdict); verdict.Verdict != verdictBusy {
		t.Fatalf("a transfer touching the library, polled: %+v", polled[0]["eject"])
	}
	transfer.Finish(nil)
	// A Terminal transfer's lease names libraries by configuration; the
	// document gives their registered IDs.
	config := filepath.Join(t.TempDir(), "library.toml")
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Config: config, Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()}})
	cli, err := acquireProcessLease(appLeaseDir(), leaseInfo{Instance: "cli-transfer", HostID: currentHost().ID, Process: "cli:library-transfer",
		Kinds: []string{"library.transfer"}, Holds: parseHolds("O:w D(source):w D(target):w"), Libraries: []string{config}, StartedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := broker.statusDocument(context.Background())["app"].(map[string]any)
	operations, _ := app["operations"].([]map[string]any)
	if !slices.ContainsFunc(operations, func(entry map[string]any) bool {
		ids, _ := entry["libraries"].([]string)
		return fmt.Sprint(entry["id"]) == "lease:cli-transfer" && slices.Equal(ids, []string{"lib-a"})
	}) {
		t.Fatalf("a lease's libraries as registered IDs: %#v", operations)
	}
	cli.release()
	// A child that stalls past the fresh timeout gives no fresh verdict, so
	// the panel refuses to eject.
	override(t, &brokerFreshTimeout, 100*time.Millisecond)
	stall := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer stalled.Close()
	defer close(stall)
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: stalled.URL, Token: "t"}, ChildStarted: time.Now()}})
	if verdict := freshEject(); verdict.Checked == "fresh" || verdict.Verdict != verdictUnsafe {
		t.Fatalf("a stalled child: %+v", verdict)
	}
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()}})

	// Its stream, end to end, with the main lock still held.
	listener := httptest.NewServer(m)
	defer listener.Close()
	streamRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, listener.URL+"/api/app/status/stream", nil)
	streamRequest.AddCookie(&http.Cookie{Name: "pharos_app_installation", Value: "broker-token"})
	stream, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	for deadline := time.Now().Add(5 * time.Second); ; {
		line, err := reader.ReadString('\n')
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("no event from the broker's stream: %v", err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var document map[string]any
			if json.Unmarshal([]byte(data), &document) != nil || document["libraries"] == nil {
				t.Fatalf("the broker's stream sent %s", data)
			}
			break
		}
	}
}

// The app's reverse proxy passes a library's event stream through as it is
// written, not when it ends.
func TestBrokerProxyStreamsTheStatusStream(t *testing.T) {
	a, _ := lifecycleLibraries(t)
	app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := app.registry.register(a, "Euclid")
	if err != nil {
		t.Fatal(err)
	}
	app.registry.Selected = id
	release := make(chan struct{})
	defer close(release)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status/stream" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: snapshot\ndata: {\"revision\":1}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer child.Close()
	app.children[id] = &applicationChild{done: make(chan struct{}), endpoint: applicationEndpoint{URL: child.URL}}
	broker := httptest.NewServer(app)
	defer broker.Close()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, broker.URL+"/api/status/stream", nil)
	request.Header.Set("Authorization", "Bearer "+app.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := make(chan string, 4)
	go func() {
		reader := bufio.NewReader(response.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			lines <- line
		}
	}()
	for {
		select {
		case line := <-lines:
			if strings.HasPrefix(line, "data: ") {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the proxied stream held back its first event")
		}
	}
}

// The broker's status document carries resources (§10): it reads itself,
// each library's service (by its PID, answering or not) and the Terminal
// commands the service's snapshot names, each library's from the same
// readings, and the machine's total counts each process once. The
// operations document and the eject check carry none.
func TestBrokerStatusCarriesResources(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("reading other processes needs proc_pid_rusage")
	}
	sleeper := func() int {
		command := exec.Command("sleep", "30")
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { command.Process.Kill(); command.Wait() })
		return command.Process.Pid
	}
	servicePID, commandPID, stuckPID := sleeper(), sleeper(), sleeper()
	answering := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": 1, "operations": []any{}, "eject": computeEjectVerdict(verdictInput{}), "attention": []any{},
			"resources": map[string]any{"processes": []map[string]any{{"pid": servicePID, "role": "service"}, {"pid": commandPID, "role": "cli:ingest"}}}}, http.StatusOK)
	}))
	defer answering.Close()
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	defer stuck.Close()
	broker := newBrokerOperations(filepath.Join(t.TempDir(), "operations.json"))
	broker.publish([]brokerLibrary{
		{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: answering.URL, Token: "t", PID: servicePID}, ChildStarted: time.Now()},
		{ID: "lib-b", Name: "Archive", Child: &applicationEndpoint{URL: stuck.URL, Token: "t", PID: stuckPID}, ChildStarted: time.Now()},
		{ID: "lib-c", Name: "Closed"},
	})
	pids := func(summary any) []int {
		t.Helper()
		resources, ok := summary.(*resourceSummary)
		if !ok || resources == nil {
			return nil
		}
		found := []int{}
		for _, reading := range resources.Processes {
			found = append(found, reading.PID)
		}
		return found
	}
	document := broker.statusDocument(context.Background())
	if got, want := pids(document["resources"]), []int{os.Getpid(), servicePID, commandPID, stuckPID}; !slices.Equal(got, want) {
		t.Fatalf("the machine's total: %v, want %v", got, want)
	}
	if total := document["resources"].(*resourceSummary); !strings.HasPrefix(total.Label, "Pharos is using ") || total.MemoryBytes == 0 {
		t.Fatalf("the total's line: %+v", total)
	}
	entries := document["libraries"].([]map[string]any)
	if got := pids(entries[0]["resources"]); !slices.Equal(got, []int{servicePID, commandPID}) {
		t.Fatalf("Euclid's processes: %v", got)
	}
	if got := pids(entries[1]["resources"]); !slices.Equal(got, []int{stuckPID}) || !strings.HasPrefix(entries[1]["resources"].(*resourceSummary).Label, "Archive is using ") {
		t.Fatalf("a service that does not answer is still measured: %v", entries[1]["resources"])
	}
	if summary := entries[2]["resources"].(*resourceSummary); summary != nil {
		t.Fatalf("a library with no process has no resources: %+v", summary)
	}
	if _, ok := broker.document(context.Background(), true)["resources"]; ok {
		t.Fatal("the operations document carries no resources")
	}
	if _, ok := broker.statusDocumentChecked(context.Background(), true)["resources"]; ok {
		t.Fatal("the eject check carries no resources")
	}
}

// Resources are left out of change detection (§10): a document whose only
// change is its CPU or memory keeps its tag, so polls get 304 and an idle
// stream stays quiet; a stream still sends the latest resources, at most
// once per statusResourcesRefresh.
func TestStatusResourcesDoNotChangeTheTag(t *testing.T) {
	document := func(cpu float64) map[string]any {
		return map[string]any{"eject": map[string]any{"verdict": "safe"}, "resources": map[string]any{"cpu_percent": cpu, "sampled_at": now()},
			"libraries": []any{map[string]any{"resources": map[string]any{"cpu_percent": cpu}}}}
	}
	if statusTag(document(1)) != statusTag(document(40)) {
		t.Fatal("resources changed the tag")
	}
	if statusTag(document(1)) == statusTag(map[string]any{"eject": map[string]any{"verdict": "busy"}}) {
		t.Fatal("content no longer changes the tag")
	}
	stream := func(refresh time.Duration) int {
		t.Helper()
		override(t, &statusResourcesRefresh, refresh)
		ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
		defer cancel()
		request := httptest.NewRequest(http.MethodGet, "/api/status/stream", nil).WithContext(ctx)
		recorder := httptest.NewRecorder()
		var computed atomic.Int64
		streamStatus(recorder, request, nil, 10*time.Millisecond, func(context.Context) statusFrame {
			data, _ := json.Marshal(document(float64(computed.Add(1))))
			return statusFrame{tag: statusTag(document(0)), data: data}
		})
		return strings.Count(recorder.Body.String(), "event: snapshot")
	}
	if sent := stream(time.Hour); sent != 1 {
		t.Fatalf("an unchanged document with new resources was sent %d times inside the refresh", sent)
	}
	if sent := stream(150 * time.Millisecond); sent < 2 || sent > 4 {
		t.Fatalf("resources-only refreshes over 450 ms at 150 ms: %d events", sent)
	}
}
