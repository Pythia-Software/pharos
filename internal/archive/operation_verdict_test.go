package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func runningView(kind string, change func(*opView)) opView {
	row := operationKindIndex[kind]
	view := opView{ID: OpID("op-" + kind), Root: OpID("op-" + kind), Kind: row, State: opRunning, Process: "service",
		Spec: OperationSpec{Kind: kind, Holds: parseHolds(row.Holds)}}
	if change != nil {
		change(&view)
	}
	return view
}

// The verdict (§4.2) errs toward not safe: the worst rule wins, a missing
// estimate never improves it, and only read-only holders leave it safe.
func TestEjectVerdictErrsTowardNotSafe(t *testing.T) {
	eta := func(v ejectVerdict) string {
		if v.ETASeconds == nil {
			return "null"
		}
		return fmt.Sprint(*v.ETASeconds)
	}
	live := leaseProbe{Path: "/x/a.json", Live: true, Info: &leaseInfo{Instance: "a", Process: "cli:capture", Kinds: []string{"capture.cli"}, Holds: parseHolds("D:w C(this):w")}}
	for _, test := range []struct {
		name, verdict, eta, summary string
		in                          verdictInput
	}{
		{"nothing", verdictSafe, "null", "nothing is writing", verdictInput{}},
		{"a reader", verdictSafe, "null", "ejecting stops warm page caches", verdictInput{ops: []opView{runningView("maintenance.cache-warm", nil)}}},
		{"a record writer", verdictSafeIn, "2", "within about 2 s", verdictInput{ops: []opView{runningView("maintenance.library-view", nil)}}},
		{"a batch writer without an estimate", verdictSafeIn, "null", "after the current workspace", verdictInput{ops: []opView{
			runningView("index.changes.manual", func(view *opView) { view.Step.Unit = "workspace" }), runningView("maintenance.library-view", nil)}}},
		{"a measured safe point", verdictSafeIn, "5", "within about 5 s", verdictInput{ops: []opView{
			runningView("index.captures", func(view *opView) { view.SafePoint, view.SafePointSet = 5*time.Second, true })}}},
		{"a safe point past the budget", verdictBusy, "null", "cannot stop in time", verdictInput{ops: []opView{
			runningView("index.captures", func(view *opView) { view.SafePoint, view.SafePointSet = 30*time.Second, true })}}},
		{"a step-latency writer", verdictBusy, "null", "", verdictInput{ops: []opView{runningView("catalog.initialize", nil)}}},
		{"a broker operation", verdictBusy, "null", "", verdictInput{ops: []opView{runningView("library.transfer", nil)}}},
		{"another process", verdictBusy, "null", "capture (Terminal), in another process (pharos capture)", verdictInput{leases: []leaseProbe{live}}},
		{"a dead lease", verdictSafe, "null", "", verdictInput{leases: []leaseProbe{{Path: "/x/b.json"}}}},
		{"an unreadable lease", verdictUnsafe, "null", "", verdictInput{leases: []leaseProbe{{Path: "/x/c.json", Live: true, Err: os.ErrInvalid}}}},
		{"an unknown kind", verdictUnsafe, "null", "", verdictInput{ops: []opView{runningView(unknownOperationKind, nil)}}},
		{"an unnamed lock", verdictUnsafe, "null", "", verdictInput{locks: []heldLock{{Kind: "capture", Path: "/c/.capture.lock"}}}},
		{"unregistered work", verdictUnsafe, "null", "", verdictInput{unregistered: []string{"Indexing sources"}}},
		{"worst wins", verdictUnsafe, "null", "", verdictInput{ops: []opView{runningView("maintenance.library-view", nil), runningView(unknownOperationKind, nil)}, leases: []leaseProbe{live}}},
		{"an ended writer", verdictSafe, "null", "", verdictInput{ops: []opView{runningView("index.captures", func(view *opView) { view.State = opSucceeded })}}},
	} {
		got := computeEjectVerdict(test.in)
		if got.Verdict != test.verdict || eta(got) != test.eta || !strings.Contains(got.Summary, test.summary) {
			t.Errorf("%s: %s eta %s %q; want %s eta %s containing %q", test.name, got.Verdict, eta(got), got.Summary, test.verdict, test.eta, test.summary)
		}
	}
	// A capture lock is named by the ingest coordinator's holder, or by a
	// lease that declares capture work.
	locks := []heldLock{{Kind: "capture", Path: "/c/host/.capture.lock"}, {Kind: "index", Path: "/c/host/claude/.index.lock"}}
	if left := unnamedLocks(locks, []opView{runningView("index.captures", nil)}, nil); len(left) != 0 {
		t.Fatalf("locks an index holds: %+v", left)
	}
	if left := unnamedLocks(locks, nil, []leaseProbe{live}); len(left) != 0 {
		t.Fatalf("locks a Terminal capture holds: %+v", left)
	}
	if left := unnamedLocks(locks, []opView{runningView("backup.local", nil)}, nil); len(left) != 2 {
		t.Fatalf("a backup does not take the exclusive locks: %+v", left)
	}
}

// Parity of holds (§4.3): no kind that writes the catalog or the drive can
// leave the verdict safe while it runs.
func TestNoWritingKindLeavesTheVerdictSafe(t *testing.T) {
	for _, kind := range operationKinds {
		view := runningView(kind.Kind, nil)
		// A release is the stop itself, not a holder.
		if !writesDrive(view.Spec.Holds) || kind.Kind == "library.release" {
			continue
		}
		for _, state := range []string{opQueued, opRunning, opBlocked, opStopping} {
			view.State = state
			if got := computeEjectVerdict(verdictInput{ops: []opView{view}}); got.Verdict == verdictSafe {
				t.Errorf("%s %s leaves the verdict safe", kind.Kind, state)
			}
		}
	}
}

// shadowParity checks, during real runs, that every activity
// libraryActivities lists is accounted for by a running operation or a
// live lease, and that the verdict is not safe while it lists a writer
// (§4.3, §9). The registry may have more.
func shadowParity(t *testing.T, server *Server, step string) {
	t.Helper()
	activities := server.libraryActivities()
	if len(activities) == 0 {
		t.Fatalf("%s: nothing is listed; the run is not in progress", step)
	}
	if missing := unregisteredActivities(activities, server.ops.views(), server.leases.probe(true)); len(missing) > 0 {
		t.Fatalf("%s: activities without an operation: %v; operations: %+v", step, missing, server.ops.views())
	}
	writer := false
	for _, activity := range activities {
		writer = writer || activity.Writes
	}
	if verdict := server.ejectVerdictNow(context.Background(), true); writer && verdict.Verdict == verdictSafe {
		t.Fatalf("%s: the verdict is safe while %s writes", step, activities[0].Label)
	}
}

// waitUntil polls condition for at most timeout.
func waitUntil(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); !condition(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func idleOperations(server *Server) bool { return len(server.ops.running()) == 0 }

// During representative runs every activity has an operation: an automatic
// index, Capture & index, a legacy source sync, a backup (waiting for a
// Terminal capture) and the substring backfill.
func TestShadowOperationsCoverLibraryActivities(t *testing.T) {
	t.Run("automatic index", func(t *testing.T) {
		useHost(t, "host-a")
		server := automaticTestServer(t, stableCodexSource(t, 1))
		entered := make(chan struct{})
		var calls atomic.Int32
		prior := syncAdapter
		override(t, &syncAdapter, func(config SourceConfig) (Adapter, error) {
			if calls.Add(1) == 1 {
				return &cancellingAdapter{baseAdapter: baseAdapter{config: config}, entered: entered}, nil
			}
			return prior(config)
		})
		done := make(chan struct{})
		go func() { server.runAutomatic(server.life.ctx, syncSettings{Enabled: true, Interval: 60}); close(done) }()
		<-entered
		shadowParity(t, server, "automatic index")
		server.stopIngest()
		<-done
		views := server.ops.views()
		if len(views) == 0 || views[len(views)-1].Spec.Kind != "index.changes.automatic" || views[len(views)-1].State != opCancelled {
			t.Fatalf("a stopped automatic index: %+v", views)
		}
	})

	t.Run("capture and index", func(t *testing.T) {
		useHost(t, "host-a")
		catalog, config := testCatalog(t)
		claude := filepath.Join(t.TempDir(), "claude")
		writeSourceFile(t, filepath.Join(claude, "p", "s.jsonl"), "{}\n")
		config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
		config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Enabled: true}}
		server := testServer(t, config, catalog)
		release := make(chan struct{})
		var released sync.Once
		t.Cleanup(func() { released.Do(func() { close(release) }) })
		var once atomic.Bool
		override(t, &captureFault, func(stage, rel string) error {
			if once.CompareAndSwap(false, true) {
				<-release
			}
			return nil
		})
		if response := serveTestBody(server, http.MethodPost, "/api/library/update", `{"sources":[]}`); response.Code != http.StatusAccepted {
			t.Fatalf("Capture & index: %d %s", response.Code, response.Body.String())
		}
		waitUntil(t, 10*time.Second, "the capture", func() bool { return once.Load() })
		shadowParity(t, server, "Capture & index")
		released.Do(func() { close(release) })
		waitUntil(t, 30*time.Second, "Capture & index to finish", func() bool { return !server.syncActive() && idleOperations(server) })
	})

	t.Run("legacy source sync and the substring backfill", func(t *testing.T) {
		catalog, config := testCatalog(t)
		record := func(id string) WorkspaceRecord {
			return WorkspaceRecord{SourceID: id, SourceKind: "claude", Account: "local", Title: id, Conversations: []ConversationRecord{{
				NativeID: id + "-thread", Provider: "claude", Account: "local",
				Messages: []MessageRecord{{NativeID: id + "-one", Role: "user", Kind: "message", Text: "hello " + id, Selected: true}},
			}}}
		}
		source := &gatedSource{copyFixture: *newCopyFixture(t, record("first"), record("second")), emitted: make(chan string, 2), gate: make(chan struct{})}
		override(t, &syncAdapter, func(SourceConfig) (Adapter, error) { return source, nil })
		config.Sources = []SourceConfig{source.config}
		server := testServer(t, config, catalog)
		synced := make(chan struct{})
		go func() { serveTest(server, http.MethodPost, "/api/sources/sync"); close(synced) }()
		<-source.emitted
		shadowParity(t, server, "source sync")
		close(source.gate)
		<-synced
		waitUntil(t, 30*time.Second, "the sync's Git refresh", func() bool { return idleOperations(server) })

		// The substring index, rebuilt for what the sync wrote, waits for
		// the writer gate inside its first batch.
		execTx(t, catalog.DB, "DELETE FROM meta WHERE key IN ('message_trigram_version','message_trigram_cursor')")
		gate, err := catalog.acquireWriteGate(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		server.daemon("maintainSubstringIndex", catalog.maintainSubstringIndex)
		waitUntil(t, 10*time.Second, "the backfill", func() bool { return len(server.ops.running()) > 0 })
		shadowParity(t, server, "substring backfill")
		gate()
		waitUntil(t, 30*time.Second, "the backfill to finish", func() bool {
			progress, err := catalog.substringIndexProgress(context.Background())
			return err == nil && progress.Ready && idleOperations(server)
		})
	})

	t.Run("backup waiting for a Terminal capture", func(t *testing.T) {
		config, dest := backupFixture(t)
		server := probeServer(t, config.Path)
		// A Terminal capture: this Mac's capture lock, under a live lease.
		lock, err := os.OpenFile(filepath.Join(config.CaptureRoot, "host-a", captureLockName), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		lease, err := acquireProcessLease(leaseDirs(config)[1], leaseInfo{Instance: "terminal-capture", HostID: "host-a", Process: "cli:capture",
			Kinds: []string{"capture.cli"}, Holds: parseHolds("D:w C(this):w"), StartedAt: now()})
		if err != nil {
			t.Fatal(err)
		}
		if response := serveTestBody(server, http.MethodPost, "/api/backup", fmt.Sprintf(`{"destination":%q}`, dest)); response.Code != http.StatusAccepted {
			t.Fatalf("backup: %d %s", response.Code, response.Body.String())
		}
		waitUntil(t, 20*time.Second, "the backup to wait for the capture", func() bool {
			for _, view := range server.ops.running() {
				if view.State() == opBlocked {
					return true
				}
			}
			return false
		})
		shadowParity(t, server, "backup")
		if verdict := server.ejectVerdictNow(context.Background(), true); verdict.Verdict != verdictBusy {
			t.Fatalf("a Terminal capture makes the verdict busy: %+v", verdict)
		}
		lease.release()
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		waitUntil(t, 30*time.Second, "the backup to finish", func() bool { return idleOperations(server) })
		if views := server.ops.views(); views[0].Spec.Kind != "backup.local" || views[0].State != opSucceeded {
			t.Fatalf("the backup's operation: %+v", views[0])
		}
	})
}

func serveTestBody(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+server.Config().APIToken)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// GET /api/operations lists this Mac's operations with server words, steps
// and the verdict, answers 304 while nothing changed, and POST
// /api/operations/stop calls the kind's own stopper.
func TestOperationsSnapshotAndStop(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.backups.begin(BackupRun{ID: "b1", Kind: "backup", State: "running", Phase: "catalog", Destination: "/Volumes/Other"}, cancel); err != nil {
		t.Fatal(err)
	}
	_, backup := server.begin(ctx, OperationSpec{Kind: "backup.local", Trigger: triggerUser, Legacy: "b1"})
	mirrorBackup(backup, BackupRun{Phase: "copying captures", BytesCopied: 50, BytesTotal: 200})
	_, hidden := server.begin(context.Background(), OperationSpec{Kind: "drive.measure", Trigger: triggerRead})
	defer hidden.Finish(nil)
	response := serveTest(server, http.MethodGet, "/api/operations")
	var document struct {
		Revision   uint64           `json:"revision"`
		Eject      ejectVerdict     `json:"eject"`
		Operations []map[string]any `json:"operations"`
		Journal    map[string]any   `json:"journal"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil || response.Code != http.StatusOK {
		t.Fatalf("%d %s: %v", response.Code, response.Body.String(), err)
	}
	if len(document.Operations) != 1 {
		t.Fatalf("operations (a hidden kind is not listed): %#v", document.Operations)
	}
	entry := document.Operations[0]
	step, _ := entry["step"].(map[string]any)
	if entry["label"] != "Back up" || entry["state_label"] != "Backing up" || entry["stoppable"] != true || step["name"] != "copying captures" || step["fraction"] != 0.25 {
		t.Fatalf("backup entry: %#v", entry)
	}
	if document.Eject.Verdict != verdictSafeIn || len(document.Eject.Blockers) != 2 {
		t.Fatalf("verdict: %+v", document.Eject)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/operations", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("If-None-Match", response.Header().Get("ETag"))
	unchanged := httptest.NewRecorder()
	server.ServeHTTP(unchanged, request)
	if unchanged.Code != http.StatusNotModified {
		t.Fatalf("an unchanged snapshot: %d", unchanged.Code)
	}
	if stopped := serveTestBody(server, http.MethodPost, "/api/operations/stop", fmt.Sprintf(`{"id":%q}`, backup.ID())); stopped.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", stopped.Code, stopped.Body.String())
	}
	if ctx.Err() == nil || backup.State() != opStopping {
		t.Fatalf("the backup's stopper did not run: ctx %v, state %q", ctx.Err(), backup.State())
	}
	backup.Finish(ctx.Err())
	if backup.State() != opCancelled {
		t.Fatalf("a stopped backup ended %q", backup.State())
	}
	if refused := serveTestBody(server, http.MethodPost, "/api/operations/stop", `{"id":"nope"}`); refused.Code != http.StatusNotFound {
		t.Fatalf("stopping an unknown operation: %d", refused.Code)
	}
}
