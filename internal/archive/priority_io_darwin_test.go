//go:build darwin

package archive

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

// The real policy (§10): a background body runs at utility, a foreground
// one does not, and the thread's policy is back as it was after a return
// and after a panic. A restore that fails keeps the goroutine on its
// thread, which Go then ends rather than reuse at utility.
func TestUtilityIOPolicyIsRestored(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	server.ops.lowerIO = func() bool { return true }
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before, err := currentIOPolicy()
	if err != nil {
		t.Fatal(err)
	}
	policy := func(spec OperationSpec) int32 {
		t.Helper()
		_, op := server.begin(context.Background(), spec)
		defer op.Finish(nil)
		var during int32
		op.RunIO(func() { during, _ = currentIOPolicy() })
		if after, _ := currentIOPolicy(); after != before {
			t.Fatalf("%s left the thread at %d, was %d", spec.Kind, after, before)
		}
		return during
	}
	if background, foreground := policy(OperationSpec{Kind: "analysis.git", Trigger: triggerChained}), policy(OperationSpec{Kind: "backup.local", Trigger: triggerUser}); background != ioPolicyUtility || foreground == ioPolicyUtility {
		t.Fatalf("thread policy: background %d, foreground %d", background, foreground)
	}
	_, op := server.begin(context.Background(), OperationSpec{Kind: "analysis.git", Trigger: triggerChained})
	func() {
		defer func() { _ = recover() }()
		op.RunIO(func() { panic("work failed") })
	}()
	op.Finish(nil)
	if after, _ := currentIOPolicy(); after != before {
		t.Fatalf("a panic left the thread at %d, was %d", after, before)
	}

	override(t, &restoreIOPolicy, func(int32) error { return errors.New("restore failed") })
	done := make(chan int32)
	go func() {
		withCaptureIOPolicy(func() {})
		// Still locked to the utility thread; it ends with this goroutine.
		policy, _ := currentIOPolicy()
		done <- policy
	}()
	if stuck := <-done; stuck != ioPolicyUtility {
		t.Fatalf("after a failed restore the goroutine moved to a thread at %d", stuck)
	}
}

// The tick rate is measured against getrusage and snaps to the known one.
func TestTimebaseIsCalibrated(t *testing.T) {
	hz, ok := calibrateTimebase()
	want := 1e9
	if runtime.GOARCH == "arm64" {
		want = 24e6
	}
	if ok && hz != want {
		t.Fatalf("calibrated %.0f Hz, want %.0f", hz, want)
	}
	if got := ticksPerSecond(); got != want {
		t.Fatalf("ticks per second %.0f, want %.0f", got, want)
	}
}
