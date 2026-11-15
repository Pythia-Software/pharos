package archive

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/gbdubs/pharos/internal/platform/filelock"
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
	live := leaseProbe{Path: "/x/a.json", Live: true, Info: &leaseInfo{Instance: "a", Process: "cli:capture", Kinds: []string{"capture.cli"}, Holds: resolveHolds(parseHolds("D:w C(this):w")),
		Locks: []Hold{{Resource: "capture:" + currentHost().ID, Mode: "write"}}}}
	for _, test := range []struct {
		name, verdict, eta, summary string
		in                          verdictInput
	}{
		{"nothing, polled", verdictSafe, "null", "checks everything again when you eject", verdictInput{}},
		{"nothing, fresh", verdictSafe, "null", "Safe to eject: nothing is writing", verdictInput{fresh: true}},
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
	// A lock is named only by an exact write hold on it: one an operation
	// took (a concrete hold) or a lease declares with this Mac resolved.
	// Holding the ingest coordinator names nothing: an index may still be
	// waiting for the lock an older build's capture holds.
	host := currentHost().ID
	locks := []heldLock{{Kind: "capture", Path: "/c/" + host + "/.capture.lock"}, {Kind: "index", Path: "/c/" + host + "/claude/.index.lock"}}
	if left := unnamedLocks(locks, []opView{runningView("index.captures", nil)}, nil); len(left) != 2 {
		t.Fatalf("an index that took no lock named %d of 2: %+v", 2-len(left), left)
	}
	indexing := runningView("index.captures", func(view *opView) {
		view.Locks = []Hold{{Resource: "index:" + host + "/claude", Mode: "write"}}
	})
	if left := unnamedLocks(locks, []opView{indexing}, nil); len(left) != 1 || left[0].Kind != "capture" {
		t.Fatalf("an index holding one source's lock: %+v", left)
	}
	if left := unnamedLocks(locks, nil, []leaseProbe{live}); len(left) != 1 || left[0].Kind != "index" {
		t.Fatalf("a Terminal capture names this Mac's capture lock only: %+v", left)
	}
	if left := unnamedLocks(locks, []opView{runningView("backup.local", nil)}, nil); len(left) != 2 {
		t.Fatalf("a backup does not take the exclusive locks: %+v", left)
	}
}

// An update stops this process's work, as an eject does, but leaves the
// drive mounted: another process's work does not hold it, and work that
// stops at its next safe point lets it go ahead.
func TestUpdateRefusesOnlyWorkItWouldInterrupt(t *testing.T) {
	terminal := leaseProbe{Path: "/x/a.json", Live: true, Info: &leaseInfo{Instance: "a", Process: "cli:capture", Kinds: []string{"capture.cli"}, Holds: resolveHolds(parseHolds("D:w"))}}
	foreign := runningView("index.captures", func(view *opView) { view.Foreign, view.SafePoint, view.SafePointSet = true, time.Minute, true })
	for _, test := range []struct {
		name, refusal string
		in            verdictInput
	}{
		{"nothing", "", verdictInput{}},
		{"a batch writer", "", verdictInput{ops: []opView{runningView("index.changes.manual", nil)}}},
		{"another process", "", verdictInput{ops: []opView{foreign}, leases: []leaseProbe{terminal}, locks: []heldLock{{Kind: "capture", Path: "/c/.capture.lock"}}}},
		{"a step-latency writer", "waits for prepare the catalog", verdictInput{ops: []opView{runningView("catalog.initialize", nil)}}},
		{"a broker operation", "waits for copy library", verdictInput{ops: []opView{runningView("library.transfer", func(view *opView) { view.Spec.Variant = "copy" })}}},
		{"unregistered work", "waits for indexing sources", verdictInput{unregistered: []string{"Indexing sources"}}},
	} {
		got := updateRefusal(computeEjectVerdict(updateInputs(test.in)))
		if test.refusal == "" && got != "" || test.refusal != "" && !strings.Contains(got, test.refusal) {
			t.Errorf("%s: %q; want %q", test.name, got, test.refusal)
		}
	}
}

// An operation holds a capture or index lock from when it takes it until
// it closes the file.
func TestOperationHoldsTheLocksItTook(t *testing.T) {
	reg := newOpRegistry("service")
	_, op := reg.begin(context.Background(), OperationSpec{Kind: "index.captures"})
	path := filepath.Join(t.TempDir(), "host-a", "claude", indexLockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	op.holdLock(file)
	if !lockNamed("index:host-a/claude", []opView{op.view()}, nil) {
		t.Fatalf("holds while the lock is open: %+v", op.view().Locks)
	}
	before := op.view().Version
	if err := releaseLockFile(file); err != nil {
		t.Fatal(err)
	}
	if view := op.view(); lockNamed("index:host-a/claude", []opView{view}, nil) || view.Version == before {
		t.Fatalf("after releasing the lock: %+v (version %d → %d)", view.Locks, before, view.Version)
	}
	op.Finish(nil)

	// A Terminal command's lease lists the locks it holds, apart from its
	// declared holds, and drops one when it is released.
	ops, err := beginCLIOperationsIn([]string{filepath.Join(t.TempDir(), leaseDirName)}, nil, nil, "index", "index.captures")
	if err != nil {
		t.Fatal(err)
	}
	defer ops.close()
	_, index := ops.begin(context.Background(), OperationSpec{Kind: "index.captures", Trigger: triggerCLI})
	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lease := func() *leaseInfo { return probeLease(ops.leases[0].path).Info }
	index.holdLock(file)
	if info := lease(); len(info.Locks) != 1 || info.Locks[0].Resource != "index:host-a/claude" || len(info.Holds) == 0 {
		t.Fatalf("lease while indexing: %+v", info)
	}
	releaseLockFile(file)
	if info := lease(); len(info.Locks) != 0 || len(info.Holds) == 0 {
		t.Fatalf("lease after releasing: %+v", info)
	}
	index.Finish(nil)
}

// The release path's lock scan cannot vouch for what it could not read: an
// unreadable capture root, host directory or lock file is unsafe.
func TestFreshLockScanTreatsReadErrorsAsUnsafe(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host-a")
	source := filepath.Join(host, "claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(host, captureLockName)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if held, failures := heldCaptureLocks(root); len(held) != 0 || len(failures) != 0 {
		t.Fatalf("a readable, unlocked capture: %v %v", held, failures)
	}
	if held, failures := heldCaptureLocks(filepath.Join(root, "missing")); len(held) != 0 || len(failures) != 0 {
		t.Fatalf("a missing capture root: %v %v", held, failures)
	}
	for _, path := range []string{lock, host, root} {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		_, failures := heldCaptureLocks(root)
		os.Chmod(path, 0o700)
		if len(failures) == 0 {
			t.Fatalf("an unreadable %s was taken for unlocked", path)
		}
		if verdict := computeEjectVerdict(verdictInput{scanErrors: failures, fresh: true}); verdict.Verdict != verdictUnsafe || verdict.Checked != "fresh" {
			t.Fatalf("verdict with an unreadable %s: %+v", path, verdict)
		}
	}
	// Probes open existing lock files only: the source's missing index lock
	// is not held, and the scan creates none.
	index := filepath.Join(source, indexLockName)
	scans, probes := lockScans.Load(), lockProbes.Load()
	heldCaptureLocks(root)
	if lockScans.Load()-scans != 1 || lockProbes.Load()-probes != 2 {
		t.Fatalf("one scan counted %d scans, %d probes", lockScans.Load()-scans, lockProbes.Load()-probes)
	}
	if _, err := os.Stat(index); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the scan created %s: %v", index, err)
	}
	// Another process's lease is held, not a failure; its close frees it.
	holder, err := fileLease(lock, true)
	if err != nil {
		t.Fatal(err)
	}
	if held, failures := heldCaptureLocks(root); len(held) != 1 || held[0].Path != lock || len(failures) != 0 {
		t.Fatalf("a held capture lock: %v %v", held, failures)
	}
	holder.Close()
	if held, failures := heldCaptureLocks(root); len(held) != 0 || len(failures) != 0 {
		t.Fatalf("a released capture lock: %v %v", held, failures)
	}
	// Only contention is a held lock. A lock the probe cannot take for any
	// other reason, a platform without locks included, is unsafe: the scan
	// never takes it for free.
	for _, tc := range leaseLockFailures() {
		t.Run(tc.name, func(t *testing.T) {
			override(t, &lockProbeFile, func(file *os.File, mode filelock.Mode, wait filelock.WaitMode) error {
				if mode != filelock.Shared || wait != filelock.Try {
					t.Errorf("probe lock: %v, %v", mode, wait)
				}
				return tc.err
			})
			locked, err := probeLockFile(lock)
			held, failures := heldCaptureLocks(root)
			if errors.Is(tc.err, filelock.ErrContended) {
				if !locked || err != nil || len(held) != 1 || len(failures) != 0 {
					t.Fatalf("contention: locked=%v err=%v held=%v failures=%v", locked, err, held, failures)
				}
				return
			}
			if locked || !errors.Is(err, tc.err) || !strings.Contains(err.Error(), lock) {
				t.Fatalf("probe failure: locked=%v err=%v", locked, err)
			}
			if len(held) != 0 || len(failures) != 1 {
				t.Fatalf("scan with a failing probe: %v %v", held, failures)
			}
			if verdict := computeEjectVerdict(verdictInput{scanErrors: failures, fresh: true}); verdict.Verdict != verdictUnsafe {
				t.Fatalf("verdict with a failing probe: %+v", verdict)
			}
			if _, err := os.Stat(index); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a failing scan created %s: %v", index, err)
			}
		})
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

	t.Run("background pass", func(t *testing.T) {
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
		server.requestBackground("preserve")
		done := make(chan struct{})
		go func() { server.backgroundPass(server.life.ctx, time.Now()); close(done) }()
		waitUntil(t, 10*time.Second, "the pass's capture", func() bool { return once.Load() })
		shadowParity(t, server, "background pass")
		var pass opView
		for _, view := range server.ops.views() {
			if view.Spec.Kind == "background.update" && !opTerminal(view.State) {
				pass = view
			}
		}
		// Eject when safe stops what it can first: Stop reaches the pass.
		if response := serveTestBody(server, http.MethodPost, "/api/operations/stop", `{"id":"`+string(pass.ID)+`"}`); response.Code != http.StatusOK {
			t.Fatalf("Stop of a background pass: %d %s", response.Code, response.Body.String())
		}
		released.Do(func() { close(release) })
		<-done
		waitUntil(t, 30*time.Second, "the pass to end", func() bool { return idleOperations(server) })
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

		// So does removing tool output from the word index.
		execTx(t, catalog.DB, "DELETE FROM meta WHERE key IN ('message_fts_tool_output_removed','message_fts_tool_output_merged','message_fts_tool_output_cursor')")
		gate, err = catalog.acquireWriteGate(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		server.daemon("maintainToolOutputIndex", catalog.maintainToolOutputIndex)
		waitUntil(t, 10*time.Second, "the removal", func() bool { return len(server.ops.running()) > 0 })
		shadowParity(t, server, "tool-output removal")
		gate()
		waitUntil(t, 30*time.Second, "the removal to finish", func() bool {
			progress, err := catalog.toolOutputIndexProgress(context.Background())
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
			Kinds: []string{"capture.cli"}, Holds: resolveHolds(parseHolds("D:w C(this):w")), Locks: []Hold{{Resource: "capture:host-a", Mode: "write"}}, StartedAt: now()})
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
	if document.Eject.Verdict != verdictSafeIn || len(document.Eject.Blockers) != 2 || document.Eject.Checked != "polled" {
		t.Fatalf("verdict: %+v", document.Eject)
	}
	var fresh struct {
		Eject ejectVerdict `json:"eject"`
	}
	if err := json.Unmarshal(serveTest(server, http.MethodGet, "/api/operations?fresh=1").Body.Bytes(), &fresh); err != nil || fresh.Eject.Checked != "fresh" {
		t.Fatalf("a fresh verdict: %+v %v", fresh.Eject, err)
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
	// Work with its own stopper (a labels backlog): refused until the work
	// has registered it, then it cancels the work's context.
	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	_, backlog := server.begin(workCtx, OperationSpec{Kind: "analysis.labels", Variant: "backlog", Trigger: triggerChained, Priority: priorityBackground})
	stop := func() int {
		return serveTestBody(server, http.MethodPost, "/api/operations/stop", fmt.Sprintf(`{"id":%q}`, backlog.ID())).Code
	}
	if code := stop(); code != http.StatusConflict || backlog.State() != opRunning {
		t.Fatalf("a stop before the work can stop: %d, state %q", code, backlog.State())
	}
	backlog.setStopper(stopWork)
	if code := stop(); code != http.StatusOK || workCtx.Err() == nil || backlog.State() != opStopping {
		t.Fatalf("the work's stopper did not run: %d, ctx %v, state %q", code, workCtx.Err(), backlog.State())
	}
	backlog.Finish(workCtx.Err())
	if backlog.State() != opCancelled {
		t.Fatalf("a stopped backlog ended %q", backlog.State())
	}
	// A pass can be several levels into its analysis chain. Keep the
	// current rebuild and its measured progress in the public snapshot.
	_, pass := server.begin(context.Background(), OperationSpec{Kind: "background.update", Trigger: triggerUser})
	defer pass.Finish(nil)
	_, upgrade := pass.Child(OperationSpec{Kind: "library.upgrade", Trigger: triggerChained})
	upgrade.Finish(nil)
	_, chain := pass.Child(OperationSpec{Kind: "analysis.chain", Trigger: triggerChained})
	defer chain.Finish(nil)
	_, authorship := chain.Child(OperationSpec{Kind: "analysis.authorship", Trigger: triggerChained})
	defer authorship.Finish(nil)
	_, rebuild := authorship.Child(OperationSpec{Kind: "analysis.authorship", Trigger: triggerChained})
	defer rebuild.Finish(nil)
	rebuild.Step("classifying authorship", "message", 100)
	rebuild.SetDone(42)
	if err := json.Unmarshal(serveTest(server, http.MethodGet, "/api/operations").Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	var nested map[string]any
	for _, entry := range document.Operations {
		if entry["id"] == string(pass.ID()) {
			nested = entry
		}
	}
	for _, id := range []OpID{chain.ID(), authorship.ID(), rebuild.ID()} {
		children, _ := nested["children"].([]any)
		if len(children) != 1 {
			t.Fatalf("live child %s missing from snapshot: %#v", id, nested)
		}
		nested = children[0].(map[string]any)
		if nested["id"] != string(id) || nested["stoppable"] != false {
			t.Fatalf("nested stage: %#v", nested)
		}
	}
	if step := nested["step"].(map[string]any); step["done"] != 42.0 || step["total"] != 100.0 {
		t.Fatalf("nested rebuild progress: %#v", step)
	}
}

// A polled snapshot takes no lock: it knows of another process's capture
// only from a recent status-path probe, and only while that is recent.
func TestPolledVerdictReusesOnlyARecentStatusProbe(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	session, err := beginCapture(config) // another process's capture, no lease
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	polled := func() ejectVerdict {
		var document struct {
			Eject ejectVerdict `json:"eject"`
		}
		if err := json.Unmarshal(serveTest(server, http.MethodGet, "/api/operations").Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		return document.Eject
	}
	if verdict := polled(); verdict.Verdict == verdictUnsafe || verdict.Checked != "polled" {
		t.Fatalf("with no status probe yet, a polled verdict probed: %+v", verdict)
	}
	serveTest(server, http.MethodGet, "/api/library/status")
	if verdict := polled(); verdict.Verdict != verdictUnsafe {
		t.Fatalf("a recent status probe saw the capture, the polled verdict did not: %+v", verdict)
	}
	override(t, &captureProbeReuse, 0)
	if verdict := polled(); verdict.Verdict == verdictUnsafe {
		t.Fatalf("an old status probe was reused: %+v", verdict)
	}
	if verdict := server.ejectVerdictNow(context.Background(), true); verdict.Verdict != verdictUnsafe || verdict.Checked != "fresh" {
		t.Fatalf("the fresh check: %+v", verdict)
	}
}

// A Terminal command with a journal rewrites its lease at once when it takes
// a lock, but coalesces releases onto the journal's tick, and skips a
// rewrite that would not change the lease.
func TestLeaseLockRewritesAreCoalesced(t *testing.T) {
	catalog, _ := testCatalog(t)
	ops, err := beginCLIOperationsIn([]string{filepath.Join(t.TempDir(), leaseDirName)}, nil, nil, "index", "index.captures")
	if err != nil {
		t.Fatal(err)
	}
	ops.journal = newJournalWriter(catalog, ops.reg) // attached, without its loop
	ops.journal.onTick = ops.flushLeaseLocks
	defer ops.close()
	_, index := ops.begin(context.Background(), OperationSpec{Kind: "index.captures", Trigger: triggerCLI})
	defer index.Finish(nil)
	path := filepath.Join(t.TempDir(), "host-a", "claude", indexLockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	locks := func() []Hold { return probeLease(ops.leases[0].path).Info.Locks }
	before := ops.leaseWrites.Load()
	for range 5 {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		index.holdLock(file)
		if got := locks(); len(got) != 1 {
			t.Fatalf("while held: %+v", got)
		}
		releaseLockFile(file)
	}
	if writes := ops.leaseWrites.Load() - before; writes != 1 {
		t.Fatalf("five short locks of the same file rewrote the lease %d times; once, when first taken", writes)
	}
	if got := locks(); len(got) != 1 {
		t.Fatalf("a release was written before the tick: %+v", got)
	}
	ops.journal.onTick()
	if got := locks(); len(got) != 0 {
		t.Fatalf("after the tick: %+v", got)
	}
}
