package archive

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Priority classes (§10): a background operation's body runs under the
// utility IO policy and a foreground one's does not. The policy function is
// injected; TestUtilityIOPolicyIsRestored checks the real one, without
// measuring IO.
func TestBackgroundIORunsAtUtility(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	var mu sync.Mutex
	wrapped := []string{}
	var inside atomic.Bool
	override(t, &utilityIO, func(op *Op, fn func()) {
		mu.Lock()
		wrapped = append(wrapped, op.spec.Kind)
		mu.Unlock()
		inside.Store(true)
		defer inside.Store(false)
		fn()
	})
	run := func(spec OperationSpec) {
		t.Helper()
		done := make(chan struct{})
		if !server.start(spec, func(context.Context, *Op) error { close(done); return nil }) {
			t.Fatal("not started")
		}
		<-done
	}
	everything := func() []string {
		t.Helper()
		mu.Lock()
		wrapped = wrapped[:0]
		mu.Unlock()
		run(OperationSpec{Kind: "maintenance.upgrade-count", Trigger: triggerVersion})
		run(OperationSpec{Kind: "backup.local", Trigger: triggerUser})
		// A kind that is background by default, started for a user.
		run(OperationSpec{Kind: "analysis.findings", Trigger: triggerUser, Priority: priorityForeground})
		// A daemon pass's batch, a stage begun inline, and its child, which
		// takes the parent's class.
		pass := catalog.beginPass(context.Background(), "maintenance.search.text", triggerVersion)
		pass.runIO(func() {})
		pass.worked(1)
		pass.end(nil)
		ctx, chain := server.begin(context.Background(), OperationSpec{Kind: "analysis.refresh", Trigger: triggerChained})
		_, child := chain.childFrom(ctx, OperationSpec{Kind: "analysis.chain", Trigger: triggerChained})
		child.RunIO(func() {})
		child.Finish(nil)
		chain.Finish(nil)
		var nilOp *Op
		nilOp.RunIO(func() {})
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(wrapped)
	}
	got := everything()
	slices.Sort(got)
	if want := []string{"analysis.chain", "maintenance.search.text", "maintenance.upgrade-count"}; !slices.Equal(got, want) {
		t.Fatalf("wrapped %v, want the background ones %v", got, want)
	}

	// The WAL bound's checkpoint takes its caller's class: inside a manual
	// index (foreground) it keeps the default tier, as it does for a caller
	// with no operation (a close); only keepWALSmall's ticker runs it as
	// background.
	mu.Lock()
	wrapped = wrapped[:0]
	mu.Unlock()
	indexCtx, index := server.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser})
	// A limit of -1 crosses it whatever the WAL's size; a busy checkpoint
	// delays the next, and these are not about retries.
	catalog.boundWAL(indexCtx, -1)
	catalog.wal.retryAt = time.Time{}
	catalog.boundWAL(context.Background(), -1)
	catalog.wal.retryAt = time.Time{}
	index.Finish(nil)
	mu.Lock()
	if len(wrapped) != 0 {
		t.Fatalf("a foreground caller's checkpoint ran at utility: %v", wrapped)
	}
	mu.Unlock()
	if !slices.ContainsFunc(server.ops.views(), func(view opView) bool {
		return view.Spec.Kind == "maintenance.checkpoint" && view.Parent == index.ID() && view.Spec.Priority == priorityForeground
	}) {
		t.Fatal("the index's checkpoint is not its foreground child")
	}
	// The ticker's checkpoint: only its passive pass runs at utility; the
	// truncating pass, which holds writers off, runs at the default tier.
	truncated, truncatedInside := 0, false
	override(t, &checkpointTruncating, func() { truncated++; truncatedInside = truncatedInside || inside.Load() })
	catalog.boundWALAt(context.Background(), -1, priorityBackground)
	mu.Lock()
	if !slices.Equal(wrapped, []string{"maintenance.checkpoint"}) {
		t.Fatalf("the ticker's checkpoint: %v", wrapped)
	}
	mu.Unlock()
	if truncated != 1 || truncatedInside {
		t.Fatalf("the truncating pass ran %d times, at utility: %v", truncated, truncatedInside)
	}
}

// The broker's background operations, such as a delivery, run their IO at
// utility as the services' do.
func TestBrokerBackgroundIORunsAtUtility(t *testing.T) {
	var wrapped atomic.Int32
	override(t, &utilityIO, func(_ *Op, fn func()) { wrapped.Add(1); fn() })
	broker := newBrokerOperations(filepath.Join(t.TempDir(), "operations.json"))
	deliver := broker.begin(OperationSpec{Kind: "library.deliver", Trigger: triggerSchedule}, "lib-a", "lib-b")
	defer deliver.Finish(nil)
	deliver.RunIO(func() {})
	if wrapped.Load() != 1 {
		t.Fatal("a delivery's IO did not run at utility")
	}
}
