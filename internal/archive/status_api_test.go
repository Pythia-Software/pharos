package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
// operations and the attention list, with stable null placeholders for
// freshness and resources. A cloud upload is a batch writer: safe to eject
// after its current unit. A Terminal command's lease makes the drive busy and
// lists its work, which Pharos cannot stop.
func TestStatusSnapshotComposesVerdictOperationsAndPlaceholders(t *testing.T) {
	override(t, &statusCoalesce, 10*time.Millisecond)
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	idle := getStatus(t, server, "/api/status")
	for _, key := range []string{"freshness", "resources"} {
		if value, ok := idle.raw[key]; !ok || value != nil {
			t.Fatalf("%s must be present and null until its phase: %#v", key, idle.raw[key])
		}
	}
	if idle.Eject.Verdict != verdictSafe || idle.Eject.Checked != "polled" || len(idle.Operations) != 0 || len(idle.Attention) != 0 {
		t.Fatalf("an idle library: %+v %#v %#v", idle.Eject, idle.Operations, idle.Attention)
	}
	if _, ok := idle.Library["drive"].(map[string]any)["location"]; !ok {
		t.Fatalf("the drive says where it is: %#v", idle.Library)
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
		wait.Go(func() { hub.frame(context.Background(), time.Minute, revision.Load, compute) })
	}
	wait.Wait()
	if computed.Load() != 1 {
		t.Fatalf("10 readers computed %d documents", computed.Load())
	}
	first := hub.frame(context.Background(), time.Minute, revision.Load, compute)
	revision.Add(1)
	started := time.Now()
	second := hub.frame(context.Background(), time.Minute, revision.Load, compute)
	if computed.Load() != 2 || first.tag == second.tag {
		t.Fatalf("a change is computed again: %d", computed.Load())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("waited %s", elapsed)
	}
	// generated_at alone never changes the tag.
	if statusTag(map[string]any{"a": 1, "generated_at": "x"}) != statusTag(map[string]any{"a": 1, "generated_at": "y"}) {
		t.Fatal("the tag depends on generated_at")
	}
}

// The stream sends the document on connect and again when an operation
// starts.
func TestStatusStreamSendsOnConnectAndOnChange(t *testing.T) {
	override(t, &statusCoalesce, 20*time.Millisecond)
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
	events := make(chan statusSnapshot, 8)
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
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"version": 1, "operations": []any{}, "eject": computeEjectVerdict(verdictInput{}),
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
	attention, _ = broker.statusDocument(context.Background())["attention"].([]attentionItem)
	if len(attention) != 1 || attention[0].Rule != 8 || attention[0].Library != "lib-b" {
		t.Fatalf("a selected library without a service: %+v", attention)
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
}
