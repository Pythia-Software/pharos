package archive

import (
	"context"
	"errors"
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
