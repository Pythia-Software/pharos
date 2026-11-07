package archive

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func init() {
	// A programming error in the registry (an invalid transition, an
	// unknown kind) fails the test that makes it.
	strictOperations = true
}

// The lifecycle's states and the handle's methods (§2.2, §2.3): Finish maps
// a result to succeeded, failed, cancelled (after a user stop) or
// interrupted (once the context is done), and a terminal state never
// changes.
func TestOperationLifecycleFollowsTheContract(t *testing.T) {
	reg := newOpRegistry("service")
	state := func(op *Op, want string) {
		t.Helper()
		if got := op.State(); got != want {
			t.Fatalf("%s: state %q, want %q", op.spec.Kind, got, want)
		}
	}
	_, op := reg.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser, Queued: true})
	state(op, opQueued)
	op.Plan(StepPlan{Name: "Discover inputs", Total: -1}, StepPlan{Name: "Claude Code", Unit: "byte", Total: 100})
	op.Step("Claude Code", "byte", 100)
	state(op, opRunning)
	op.Advance(40)
	op.Advance(1)
	if view := op.view(); view.Step.Index != 2 || view.Step.Done != 41 || view.Step.Total != 100 || len(view.Plan) != 2 {
		t.Fatalf("step: %+v", view.Step)
	}
	// Worker phases describe the active task; they do not add planned tasks.
	reportWork(op.Context(), "Reading input records", "record", 3, 10)
	reportWork(op.Context(), "Saving indexed records", "record", 4, 10)
	if step := operationStep(op.view()); step["index"] != 2 || step["count"] != 2 || step["done"] != float64(4) {
		t.Fatalf("worker phases changed the planned position or lost their counts: %+v", step)
	}
	// Revising a plan preserves the active task even while a worker's phase
	// name differs from its task name, then the next explicit task advances.
	op.Plan(StepPlan{Name: "Discover inputs"}, StepPlan{Name: "Claude Code"}, StepPlan{Name: "Verify"})
	if step := operationStep(op.view()); step["index"] != 2 || step["count"] != 3 {
		t.Fatalf("replanning lost the active task: %+v", step)
	}
	op.Step("Verify", "input", 1)
	reportWork(op.Context(), "Reading verification inputs", "input", 1, 1)
	if step := operationStep(op.view()); step["index"] != 3 || step["count"] != 3 {
		t.Fatalf("the next planned task did not advance: %+v", step)
	}
	op.Block("capture-running", "the capture on Mac B")
	state(op, opBlocked)
	op.Unblock()
	state(op, opRunning)
	op.Finish(nil)
	state(op, opSucceeded)
	op.Finish(errors.New("late"))
	state(op, opSucceeded)

	_, failed := reg.begin(context.Background(), OperationSpec{Kind: "backup.local"})
	failed.Finish(errors.New("disk full"))
	if view := failed.view(); view.State != opFailed || view.Error != "disk full" {
		t.Fatalf("failed: %+v", view)
	}

	_, stopped := reg.begin(context.Background(), OperationSpec{Kind: "backup.local"})
	stopped.markStopping()
	state(stopped, opStopping)
	stopped.Finish(context.Canceled)
	state(stopped, opCancelled)

	ctx, cancel := context.WithCancel(context.Background())
	_, released := reg.begin(ctx, OperationSpec{Kind: "backup.local"})
	cancel()
	released.Finish(context.Canceled)
	state(released, opInterrupted)
	_, quiet := reg.begin(ctx, OperationSpec{Kind: "maintenance.search.text"})
	quiet.Finish(nil)
	state(quiet, opInterrupted) // returned without finishing once released

	_, skipped := reg.begin(context.Background(), OperationSpec{Kind: "analysis.findings", Trigger: triggerUser})
	skipped.Skip("already-running")
	skipped.Finish(nil)
	if view := skipped.view(); view.State != opSkipped || view.Reason != "already-running" {
		t.Fatalf("skipped: %+v", view)
	}

	// A run's own record decides its outcome when the work returns nil.
	for _, test := range []struct{ run, want string }{{"complete", opSucceeded}, {"deferred", opSucceeded}, {"failed", opFailed}, {"interrupted", opInterrupted}} {
		_, op := reg.begin(context.Background(), OperationSpec{Kind: "index.captures"})
		op.legacyOutcome(test.run, "x")
		op.Finish(nil)
		state(op, test.want)
	}

	// Children nest under the operation their context carries.
	ctx, parent := reg.begin(context.Background(), OperationSpec{Kind: "index.captures"})
	_, chain := reg.begin(ctx, OperationSpec{Kind: "analysis.chain", Trigger: triggerChained})
	_, stage := chain.Child(OperationSpec{Kind: "analysis.git", Trigger: triggerChained})
	if view := stage.view(); view.Parent != chain.ID() || view.Root != parent.ID() || view.Spec.Priority != priorityForeground {
		t.Fatalf("stage: %+v", view)
	}
	if view := parent.view(); len(view.Children) != 1 || view.Children[0] != chain.ID() {
		t.Fatalf("parent: %+v", view)
	}
}

func TestOperationSkippedResourcesAndSharedReferencesStayFinite(t *testing.T) {
	clock := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	reg := newOpRegistry("service")
	reg.now = func() time.Time { return clock }
	reg.telemetry, reg.resourceLedger = true, newTaskResourceLedger(20, 200)
	_, op := reg.begin(t.Context(), OperationSpec{Kind: "analysis.labels", Variant: "batch"})
	if op.resources.identity.Batch != string(op.ID()) {
		t.Fatal("the profile lost its batch identity")
	}
	for range 100 {
		op.linkWork(newOpID())
	}
	if view := op.view(); len(view.SharedWork) != 64 || view.SharedWorkOmitted != 36 {
		t.Fatalf("unbounded shared references: %d + %d", len(view.SharedWork), view.SharedWorkOmitted)
	}
	clock = clock.Add(3 * time.Second)
	op.Skip("nothing-to-do")
	clock = clock.Add(time.Hour)
	op.linkWork(newOpID())
	op.setNeededBy([]OpID{newOpID()})
	op.Finish(nil)
	view := op.view()
	if view.Resources.Time["elapsed"].Value == nil || *view.Resources.Time["elapsed"].Value != 3 || len(view.SharedWork) != 64 || len(view.NeededBy) != 0 {
		t.Fatalf("a terminal operation continued accounting: %+v", view.Resources)
	}
	if summaries := reg.resourceLedger.export("analysis.labels", clock).Tasks; len(summaries) != 1 || summaries[0].State != opSkipped {
		t.Fatalf("skip did not finalize its resource record once: %+v", summaries)
	}
}

// An invalid transition or an unknown kind fails a test; in production it is
// logged, the old state is kept, and the unknown kind records as unknown.
func TestOperationProgrammingErrorsAreLoggedInProduction(t *testing.T) {
	reg := newOpRegistry("service")
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("an unknown kind did not fail the test")
			}
		}()
		reg.begin(context.Background(), OperationSpec{Kind: "no.such.kind"})
	}()
	override(t, &strictOperations, false)
	_, op := reg.begin(context.Background(), OperationSpec{Kind: "no.such.kind"})
	if op.spec.Kind != unknownOperationKind {
		t.Fatalf("kind %q", op.spec.Kind)
	}
	op.Finish(nil)
	reg.mu.Lock()
	ok := op.transitionLocked(opRunning)
	reg.mu.Unlock()
	if ok || op.State() != opSucceeded || reg.invalid != 3 { // both unknown kinds and the transition
		t.Fatalf("invalid transition: ok=%v state=%q invalid=%d", ok, op.State(), reg.invalid)
	}
}

// Subscribers hear of changes, coalesced: a burst while nobody reads is one
// wake-up. Advance allocates nothing, so ingest may call it per record.
func TestRegistryNotifiesSubscribersAndAdvanceAllocatesNothing(t *testing.T) {
	reg := newOpRegistry("service")
	changes, unsubscribe := reg.subscribe()
	_, op := reg.begin(context.Background(), OperationSpec{Kind: "index.captures"})
	op.Step("Index captured inputs", "conversation", 1000)
	for range 50 {
		op.Advance(1)
	}
	select {
	case <-changes:
	default:
		t.Fatal("no notification")
	}
	select {
	case <-changes:
		t.Fatal("a burst was not coalesced")
	default:
	}
	unsubscribe()
	op.Advance(1)
	select {
	case <-changes:
		t.Fatal("notified after unsubscribing")
	default:
	}
	if allocs := testing.AllocsPerRun(1000, func() { op.Advance(1) }); allocs != 0 {
		t.Fatalf("Advance allocates %.1f times", allocs)
	}
	// A nil handle records nothing and never panics.
	var none *Op
	none.Step("x", "", -1)
	none.Advance(1)
	none.Finish(errors.New("x"))
}

// A daemon pass that finds nothing to do is forgotten without a
// notification; one that works is published.
func TestDaemonPassIsForgottenWhenItDidNothing(t *testing.T) {
	catalog := &Catalog{ops: newOpRegistry("service")}
	before := catalog.ops.currentRevision()
	idle := catalog.beginPass(context.Background(), "maintenance.previews", triggerChained)
	idle.worked(0)
	if len(catalog.ops.running()) != 1 {
		t.Fatal("a pass is not registered while it runs")
	}
	idle.end(nil)
	if len(catalog.ops.views()) != 0 || catalog.ops.currentRevision() != before {
		t.Fatalf("an idle pass left a trace: %d operations, revision %d → %d", len(catalog.ops.views()), before, catalog.ops.currentRevision())
	}
	busy := catalog.beginPass(context.Background(), "maintenance.previews", triggerChained)
	busy.worked(25)
	busy.end(nil)
	if views := catalog.ops.views(); len(views) != 1 || views[0].State != opSucceeded || views[0].Step.Done != 25 {
		t.Fatalf("a working pass: %+v", views)
	} else if step := operationStep(views[0]); step == nil || step["unit"] == nil || step["done"] != float64(25) {
		t.Fatalf("a working pass lost its high-level counts: %+v", step)
	}
}

func TestWorkerProgressReachesOperationAndObserver(t *testing.T) {
	reg := newOpRegistry("service")
	ctx, parent := reg.begin(t.Context(), OperationSpec{Kind: "background.update"})
	var observed activityWork
	ctx = withWorkProgress(ctx, func(w activityWork) { observed = w })
	ctx, child := parent.childFrom(ctx, OperationSpec{Kind: "analysis.git"})
	reportWork(ctx, "Checking repositories", "repository", 2, 5, "Scanning local Git history")
	view := reg.views()[slices.IndexFunc(reg.views(), func(v opView) bool { return v.ID == child.ID() })]
	step := operationStep(view)
	if step == nil || step["fraction"] != 0.4 || step["detail"] != observed.Detail || observed.Detail == "" || observed.Done != 2 || observed.Total != 5 {
		t.Fatalf("worker progress lost its stage or observer: step=%+v observer=%+v", step, observed)
	}
	previousIndex := view.Step.Index
	reportWork(ctx, "Checking repositories", "repository", 2, 5, "Saving repository links")
	if changed := child.view().Step; changed.Index != previousIndex || changed.Detail != observed.Detail {
		t.Fatalf("a detail update lost its step: %+v", changed)
	}
	child.Finish(nil)
	revision := reg.currentRevision()
	reportWork(ctx, "Late worker", "repository", 5, 5)
	if reg.currentRevision() != revision {
		t.Fatal("a late worker changed an ended operation")
	}
	parent.Finish(nil)
}

func TestMaintenanceProgressCountsOnlyCommittedWorkAndRefreshesInventory(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.ops = newOpRegistry("service")
	override(t, &maintenanceInventoryInterval, time.Duration(0))
	execTx(t, catalog.DB, "CREATE TABLE progress_items(id INTEGER PRIMARY KEY); INSERT INTO progress_items VALUES(1),(2),(3)")
	pass := catalog.beginPass(t.Context(), "maintenance.labels.query", triggerChained)
	defer pass.end(nil)
	var beforeCommit activityWork
	ctx := withWorkProgress(t.Context(), func(work activityWork) { beforeCommit = work })
	batch := func(commit bool) {
		t.Helper()
		pass.runIO(ctx, func(ctx context.Context) {
			tx, err := catalog.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			completed := maintenanceBatch(ctx, tx, "Indexing message labels", "conversation", 1, "SELECT count(*) FROM progress_items")
			if _, err = tx.ExecContext(ctx, "DELETE FROM progress_items WHERE id=(SELECT min(id) FROM progress_items)"); err != nil {
				t.Fatal(err)
			}
			if !commit {
				completed(errors.New("rolled back"))
				return
			}
			err = tx.Commit()
			completed(err)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	batch(false)
	if beforeCommit.Done != 0 || beforeCommit.Total != 3 {
		t.Fatalf("first batch must be visible before commit and never count rollback: %+v", beforeCommit)
	}
	batch(true)
	if beforeCommit.Done != 1 || beforeCommit.Total != 3 {
		t.Fatalf("committed batch: %+v", beforeCommit)
	}
	execTx(t, catalog.DB, "INSERT INTO progress_items VALUES(4),(5)")
	batch(true)
	if beforeCommit.Done != 2 || beforeCommit.Total != 5 {
		t.Fatalf("new work must revise the denominator: %+v", beforeCommit)
	}
	for range 3 {
		batch(true)
	}
	if beforeCommit.Done != 5 || beforeCommit.Total != 5 {
		t.Fatalf("drained queue: %+v", beforeCommit)
	}
	step := operationStep(catalog.ops.views()[0])
	if step == nil || step["fraction"] != float64(1) || step["unit"] != "conversation" {
		t.Fatalf("committed work did not reach the status API: %+v", step)
	}
}

// Server.start keeps spawn's guarantees: it registers before the work runs,
// refuses (registering nothing) once the service is stopping, and finishes
// the operation with the work's result.
func TestServerStartRegistersBeforeWorkAndRefusesWhileStopping(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	seen := make(chan string, 1)
	if !server.start(OperationSpec{Kind: "tools.ledger.build", Trigger: triggerUser}, func(ctx context.Context, op *Op) error {
		seen <- opFrom(ctx).State()
		return errors.New("ledger failed")
	}) {
		t.Fatal("start refused")
	}
	if state := <-seen; state != opRunning {
		t.Fatalf("work saw state %q", state)
	}
	if !waitFor(5*time.Second, func() {
		for len(server.ops.running()) > 0 {
			time.Sleep(time.Millisecond)
		}
	}) {
		t.Fatal("operation did not finish")
	}
	if views := server.ops.views(); len(views) != 1 || views[0].State != opFailed {
		t.Fatalf("finished: %+v", views)
	}
	server.beginStop()
	if server.start(OperationSpec{Kind: "tools.ledger.build"}, func(context.Context, *Op) error { return nil }) {
		t.Fatal("started while stopping")
	}
	if len(server.ops.views()) != 1 {
		t.Fatal("a refused start registered an operation")
	}
}

// The origins publication is a pass like the others: forgotten when the
// projection is current, an operation when it stores origins.
func TestOriginsPassIsAnOperationOnlyWhenItPublishes(t *testing.T) {
	catalog, _ := testCatalog(t)
	publishOrigins(t, catalog)
	catalog.ops = newOpRegistry("service")
	catalog.originsPass(context.Background())
	if views := catalog.ops.views(); len(views) != 0 {
		t.Fatalf("a pass over current origins left operations: %+v", views)
	}
	execTx(t, catalog.DB, "UPDATE origin_state SET revision=revision+1")
	catalog.originsPass(context.Background())
	if views := catalog.ops.views(); len(views) != 1 || views[0].Spec.Kind != "maintenance.origins" || views[0].State != opSucceeded {
		t.Fatalf("a pass that published: %+v", views)
	}
}

// The hidden writes requests and the schedule hand to the background are
// listed nowhere, but each keeps the drive from being safe while it runs.
func TestHiddenBackgroundWritesCountForTheVerdict(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	for _, kind := range []string{"maintenance.origins", "maintenance.mcp-history", "ask.save", "cloud.reconcile"} {
		_, op := server.ops.begin(context.Background(), OperationSpec{Kind: kind, Trigger: triggerRead})
		verdict := server.ejectVerdictNow(context.Background(), false)
		if verdict.Verdict == verdictSafe || !slices.ContainsFunc(verdict.Blockers, func(b ejectBlocker) bool { return b.Kind == kind }) {
			t.Errorf("a running %s does not count for the verdict: %+v", kind, verdict)
		}
		op.Finish(nil)
	}
}

// The schedule tick's reconcile is a cloud.reconcile pass, forgotten when it
// stored nothing.
func TestCloudReconcilePassIsForgottenWhenNothingChanged(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	if err := server.settleCloudRuns(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, view := range server.ops.views() {
		if view.Spec.Kind == "cloud.reconcile" {
			t.Fatalf("an idle tick left an operation: %+v", view)
		}
	}
	interrupted := jsonText(cloudRun{ID: "lost-run", DestinationID: "cloud", State: "running", OwnerPID: 999999999, OwnerHost: currentHost().ID, UpdatedAt: "earlier"})
	execTx(t, catalog.DB, `INSERT INTO cloud_destinations(id,config_json,credential_json,imported_at) VALUES('cloud','{"name":"Backup"}','{}','2026-10-27')`,
		`INSERT INTO cloud_backup_runs(id,destination_id,status_json,created_at) VALUES('lost-run','cloud','`+interrupted+`','2026-10-27')`)
	if err := server.settleCloudRuns(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(server.ops.views(), func(view opView) bool { return view.Spec.Kind == "cloud.reconcile" && view.State == opSucceeded }) {
		t.Fatalf("a tick that recorded a lost run left no operation: %+v", server.ops.views())
	}
}

// Shared execution uses an independent live root. Retention also defends
// against a legacy caller finishing a parent before its child has drained.
func TestOperationRetentionKeepsActiveDescendants(t *testing.T) {
	reg := newOpRegistry("service")
	reg.executorRoots = true
	at := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	reg.now = func() time.Time { return at }
	reg.keepEnded = time.Minute
	_, requester := reg.begin(context.Background(), OperationSpec{Kind: "analysis.chain"})
	_, child := requester.Child(OperationSpec{Kind: "analysis.labels"})
	requester.Finish(context.Canceled)
	at = at.Add(2 * time.Hour)
	// Force the count and age pruning paths, including unwritten-journal caps.
	for range 205 {
		_, other := reg.begin(context.Background(), OperationSpec{Kind: "analysis.chain"})
		other.Finish(nil)
	}
	reg.mu.Lock()
	reg.pruneLocked()
	reg.mu.Unlock()
	if reg.find(requester.id) == nil || reg.find(child.id) == nil {
		t.Fatal("retention forgot active descendant")
	}
	views := reg.views()
	byID := map[OpID]opView{}
	for _, view := range views {
		byID[view.ID] = view
	}
	root := requester.view()
	if !root.ActiveDescendants {
		t.Fatal("terminal root did not disclose its active child")
	}
	entry := operationEntry(root, byID, at)
	if len(entry["children"].([]map[string]any)) != 1 {
		t.Fatal("activity hid active descendant")
	}
	child.Finish(nil)
	reg.mu.Lock()
	reg.pruneLocked()
	reg.mu.Unlock()
	if reg.find(requester.id) != nil || reg.find(child.id) != nil {
		t.Fatal("drained tree did not retire")
	}

	// Classic views are unchanged: an ended root is listed only within the
	// recent cut, whatever its children are doing.
	classic := newOpRegistry("service")
	_, requester = classic.begin(context.Background(), OperationSpec{Kind: "analysis.chain"})
	_, child = requester.Child(OperationSpec{Kind: "analysis.labels"})
	requester.Finish(context.Canceled)
	if requester.view().ActiveDescendants {
		t.Fatal("classic views gained the execution-root visibility rule")
	}
	child.Finish(nil)
}

func TestOperationBatchChildrenStayBounded(t *testing.T) {
	override(t, &maxEndedChildren, 4)
	// Classic pruning is unchanged: a root keeps every child's detail.
	classic := newOpRegistry("service")
	_, root := classic.begin(context.Background(), OperationSpec{Kind: "analysis.labels"})
	for range 20 {
		_, child := root.Child(OperationSpec{Kind: "analysis.labels"})
		child.Finish(nil)
	}
	classic.mu.Lock()
	classic.pruneLocked()
	classic.mu.Unlock()
	if len(root.view().Children) != 20 {
		t.Fatalf("classic pruning dropped child detail: %d children", len(root.view().Children))
	}
	reg := newOpRegistry("service")
	reg.executorRoots = true
	_, root = reg.begin(context.Background(), OperationSpec{Kind: "analysis.labels"})
	_, active := root.Child(OperationSpec{Kind: "analysis.labels"})
	for range 20 {
		_, child := root.Child(OperationSpec{Kind: "analysis.labels"})
		child.Finish(nil)
	}
	reg.mu.Lock()
	reg.pruneLocked()
	reg.mu.Unlock()
	if reg.find(active.id) == nil {
		t.Fatal("batch pruning lost an active worker")
	}
	if len(reg.views()) > 6 {
		t.Fatal("long-lived execution accumulated every ended batch")
	}
	if len(root.view().Children) > 5 {
		t.Fatal("root retained unbounded batch references")
	}
}
