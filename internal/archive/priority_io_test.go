package archive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Priority classes (§10): with the flag on, a background operation's body
// runs under the utility IO policy and a foreground one's does not; with the
// flag off nothing is wrapped. The policy function is injected;
// TestUtilityIOPolicyIsRestored checks the real one, without measuring IO.
func TestBackgroundIOFollowsTheFlag(t *testing.T) {
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

	// The work path reads the flag from memory only. A change another
	// process makes in the catalog reaches it with the next read (the
	// watchFeatureFlags daemon's, every featureFlagTTL), not before.
	override(t, &featureFlagTTL, 0)
	if _, err := catalog.DB.Exec("UPDATE ui_preferences SET value='false' WHERE key=?", unifiedActivityFlag); err != nil {
		t.Fatal(err)
	}
	if !catalog.unifiedActivityNow() {
		t.Fatal("the work path read the catalog for the flag")
	}
	catalog.unifiedActivity(context.Background())
	if catalog.unifiedActivityNow() {
		t.Fatal("a read of the flag did not refresh the work path's copy")
	}
	if _, err := catalog.DB.Exec("UPDATE ui_preferences SET value='true' WHERE key=?", unifiedActivityFlag); err != nil {
		t.Fatal(err)
	}
	catalog.unifiedActivity(context.Background())

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

// The broker has no catalog: its operations' IO follows the flag the
// selected library's service reports, read before a delivery and with every
// status document.
func TestBrokerIOFollowsTheSelectedLibrarysFlag(t *testing.T) {
	var wrapped atomic.Int32
	override(t, &utilityIO, func(_ *Op, fn func()) { wrapped.Add(1); fn() })
	on := atomic.Bool{}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": 1, "operations": []any{}, "eject": computeEjectVerdict(verdictInput{}), "features": map[string]any{"unified_activity": on.Load()}}, http.StatusOK)
	}))
	defer service.Close()
	broker := newBrokerOperations(filepath.Join(t.TempDir(), "operations.json"))
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()}})
	deliver := broker.begin(OperationSpec{Kind: "library.deliver", Trigger: triggerSchedule}, "lib-a", "lib-b")
	defer deliver.Finish(nil)
	broker.refreshFlag(context.Background())
	deliver.RunIO(func() {})
	if wrapped.Load() != 0 {
		t.Fatal("with the flag off, a delivery's IO is as before")
	}
	on.Store(true)
	broker.refreshFlag(context.Background())
	deliver.RunIO(func() {})
	if wrapped.Load() != 1 {
		t.Fatal("with the flag on, a delivery's IO runs at utility")
	}
	on.Store(false)
	broker.statusDocument(context.Background())
	deliver.RunIO(func() {})
	if wrapped.Load() != 1 {
		t.Fatal("the status document did not carry the flag back")
	}

	// The flag is the selected library's: another selection starts off,
	// and a selected library whose service cannot confirm it is off.
	on.Store(true)
	broker.refreshFlag(context.Background())
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()},
		{ID: "lib-b", Name: "Archive", Selected: true}})
	deliver.RunIO(func() {})
	if wrapped.Load() != 1 {
		t.Fatal("another library's flag outlived the selection change")
	}
	broker.refreshFlag(context.Background())
	deliver.RunIO(func() {})
	if wrapped.Load() != 1 {
		t.Fatal("a selected library with no service to ask is not off")
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	defer failing.Close()
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: service.URL, Token: "t"}, ChildStarted: time.Now()}})
	broker.refreshFlag(context.Background())
	deliver.RunIO(func() {})
	if wrapped.Load() != 2 {
		t.Fatal("selected again, its flag is read again")
	}
	broker.publish([]brokerLibrary{{ID: "lib-a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: failing.URL, Token: "t"}, ChildStarted: time.Now()}})
	broker.refreshFlag(context.Background())
	deliver.RunIO(func() {})
	if wrapped.Load() != 2 {
		t.Fatal("a failed refresh kept the last value")
	}
}
