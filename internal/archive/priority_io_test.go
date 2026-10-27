package archive

import (
	"context"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"testing"
)

// Priority classes (§10): with the flag on, a background operation's body
// runs under the utility IO policy and a foreground one's does not; with the
// flag off nothing is wrapped. The policy function is injected; the last
// case checks the real one sets the thread's policy, without measuring IO.
func TestBackgroundIOFollowsTheFlag(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	var mu sync.Mutex
	wrapped := []string{}
	override(t, &utilityIO, func(op *Op, fn func()) {
		mu.Lock()
		wrapped = append(wrapped, op.spec.Kind)
		mu.Unlock()
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
	if got := everything(); len(got) != 0 {
		t.Fatalf("with the flag off, IO policy is as before: %v", got)
	}
	if response := serveTestBody(server, http.MethodPost, "/api/preferences", `{"set":{"pharos-unified-activity-v1":true}}`); response.Code != http.StatusOK {
		t.Fatalf("saving: %d %s", response.Code, response.Body.String())
	}
	got := everything()
	slices.Sort(got)
	if want := []string{"analysis.chain", "maintenance.search.text", "maintenance.upgrade-count"}; !slices.Equal(got, want) {
		t.Fatalf("wrapped %v, want the background ones %v", got, want)
	}

	if runtime.GOOS != "darwin" {
		return
	}
	override(t, &utilityIO, func(_ *Op, fn func()) { withCaptureIOPolicy(fn) })
	policy := func(spec OperationSpec) int32 {
		t.Helper()
		_, op := server.begin(context.Background(), spec)
		defer op.Finish(nil)
		var during int32
		var err error
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		op.RunIO(func() { during, err = currentIOPolicy() })
		if err != nil {
			t.Fatal(err)
		}
		return during
	}
	if background, foreground := policy(OperationSpec{Kind: "analysis.git", Trigger: triggerChained}), policy(OperationSpec{Kind: "backup.local", Trigger: triggerUser}); background != ioPolicyUtility || foreground == ioPolicyUtility {
		t.Fatalf("thread policy: background %d, foreground %d", background, foreground)
	}
}
