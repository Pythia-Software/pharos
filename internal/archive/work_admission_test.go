package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLeafAdmissionSharesTheCallersGrant(t *testing.T) {
	a := newWorkAuthority(2)
	ctx := withWorkAuthority(t.Context(), a)
	err := admitWorkCPU(ctx, nil, 1, 2, func(ctx context.Context, outer workAllocation) error {
		if outer.CPUs != 2 || a.facts().CPUAllocated != 2 {
			t.Fatalf("outer grant %d, allocated %d", outer.CPUs, a.facts().CPUAllocated)
		}
		// A nested leaf asking for more runs inside the grant and takes nothing.
		inner, err := acquireWorkCPU(ctx, nil, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		if inner.Allocation.CPUs != 2 || a.facts().CPUAllocated != 2 {
			t.Fatalf("nested grant %d, allocated %d", inner.Allocation.CPUs, a.facts().CPUAllocated)
		}
		inner.Release()
		if a.facts().CPUAllocated != 2 {
			t.Fatal("a nested release gave back the caller's slots")
		}
		// A nested leaf that wants less gets less, still inside the grant.
		return admitWorkCPU(ctx, nil, 1, 1, func(_ context.Context, small workAllocation) error {
			if small.CPUs != 1 || a.facts().CPUAllocated != 2 {
				t.Fatalf("narrow nested grant %d, allocated %d", small.CPUs, a.facts().CPUAllocated)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts := a.facts(); facts.CPUAllocated != 0 {
		t.Fatalf("slots left allocated after the leaf: %+v", facts)
	}
}

func TestLeafAdmissionClampsToALowCapAndReleasesOnce(t *testing.T) {
	a := newWorkAuthority(1)
	ctx := withWorkAuthority(t.Context(), a)
	grant, err := acquireWorkCPU(ctx, nil, 1, captureCompressThreads)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Allocation.CPUs != 1 {
		t.Fatalf("a one-slot allowance granted %d", grant.Allocation.CPUs)
	}
	grant.Release()
	grant.Release()
	if facts := a.facts(); facts.CPUAllocated != 0 {
		t.Fatalf("double release: %+v", facts)
	}
	if _, err := acquireWorkCPU(ctx, nil, 2, 2); !errors.Is(err, errWorkGrantExceedsCap) {
		t.Fatalf("a minimum above the allowance: %v", err)
	}
}

func TestLeafAdmissionCancelledWhileWaitingLeavesNothing(t *testing.T) {
	a := newWorkAuthority(1)
	held, err := acquireWorkCPU(withWorkAuthority(t.Context(), a), nil, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(withWorkAuthority(t.Context(), a))
	done := make(chan error, 1)
	go func() {
		done <- admitWorkCPU(ctx, nil, 1, 1, func(context.Context, workAllocation) error {
			t.Error("a cancelled waiter ran")
			return nil
		})
	}()
	waitUntil(t, 5*time.Second, "the caller to wait", func() bool {
		select {
		case err := <-done:
			t.Fatalf("returned before waiting: %v", err)
		default:
		}
		return a.facts().CPUWaiters != 0
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	held.Release()
	if facts := a.facts(); facts.CPUAllocated != 0 || facts.CPUWaiters != 0 {
		t.Fatalf("after a cancelled wait: %+v", facts)
	}
	// A failed admission is that source's pending result, not a run.
	ran := false
	result := (&Catalog{}).admittedIngest(ctx, "conductor", "host-a", func(context.Context) IngestResult { ran = true; return IngestResult{} })
	if ran || !result.Pending || result.Error == nil || result.Source != "conductor" {
		t.Fatalf("cancelled ingest admission: ran %v, %+v", ran, result)
	}
}

func TestLeafAdmissionWithoutAnAuthorityIsUnchanged(t *testing.T) {
	grant, err := acquireWorkCPU(t.Context(), &Catalog{}, 1, captureCompressThreads)
	if err != nil {
		t.Fatal(err)
	}
	defer grant.Release()
	if grant.Allocation.CPUs != captureCompressThreads {
		t.Fatalf("classic leaf got %d slots", grant.Allocation.CPUs)
	}
}

func TestScheduledCommandsShareOneProcessAuthority(t *testing.T) {
	override(t, &cliAuthority, nil)
	if cliWorkAuthority(Config{WorkEngine: "classic", BackgroundCPUs: 2}) != nil {
		t.Fatal("the classic engine got an authority")
	}
	first := cliWorkAuthority(Config{WorkEngine: "scheduled", BackgroundCPUs: 2})
	second := cliWorkAuthority(Config{WorkEngine: "scheduled", BackgroundCPUs: 3})
	if first == nil || first != second || first.cap() != 3 {
		t.Fatalf("commands' authorities: %p %p cap %d", first, second, second.cap())
	}
}

func TestScheduledCommandDetachedWritingSharesItsAllowance(t *testing.T) {
	override(t, &cliAuthority, nil)
	c, config := testCatalog(t)
	config.WorkEngine, config.BackgroundCPUs = "scheduled", 1
	ops, err := beginCLIOperations(config, c, "index", "analysis.authorship")
	if err != nil {
		t.Fatal(err)
	}
	defer ops.close()
	if workAuthorityFor(t.Context(), c) != ops.authority {
		t.Fatal("detached work lost the command's authority")
	}
	held, err := acquireWorkCPU(withWorkAuthority(t.Context(), ops.authority), nil, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if !c.startAuthorshipInContext("test-generation", ctx) {
		t.Fatal("writing did not start")
	}
	waitUntil(t, 5*time.Second, "detached writing's CPU admission", func() bool { return ops.authority.facts().CPUWaiters == 1 })
	cancel()
	waitUntil(t, 5*time.Second, "detached writing to stop while waiting", func() bool { return !c.authorshipRunning() })
	if facts := ops.authority.facts(); facts.CPUAllocated != 1 || facts.CPUWaiters != 0 {
		t.Fatalf("detached work escaped or leaked its allowance: %+v", facts)
	}
}

// A slot count saved on a drive, an engine name in a library's config, and a
// stale environment value never stop a library opening on another Mac.
func TestWorkSettingsArePortableAcrossMacs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pharos.toml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	load := func() Config {
		t.Helper()
		config, err := loadConfig(path, false)
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	write(fmt.Sprintf("work_engine = \"comparison\"\nbackground_cpu_slots = %d\n", runtime.NumCPU()+8))
	t.Setenv("PHAROS_WORK_ENGINE", "")
	t.Setenv("PHAROS_BACKGROUND_CPUS", "many")
	if config := load(); config.WorkEngine != "classic" {
		t.Fatalf("a library's config chose the engine: %q", config.WorkEngine)
	}
	t.Setenv("PHAROS_WORK_ENGINE", "scheduled")
	if config := load(); config.WorkEngine != "scheduled" || config.BackgroundCPUs != runtime.NumCPU() {
		t.Fatalf("saved slots were not clamped to this Mac: %+v", config.BackgroundCPUs)
	}
	t.Setenv("PHAROS_BACKGROUND_CPUS", "0")
	if config := load(); config.BackgroundCPUs != 1 {
		t.Fatalf("environment slots were not clamped: %d", config.BackgroundCPUs)
	}
}

// One encoding holds a grant clamped to the allowance while it encodes, one
// slot while it verifies, and none during its durable sync or while it waits
// for the capture lock and commits.
func TestCaptureCompressionAdmitsItsEncoding(t *testing.T) {
	config, target := compressionFixture(t)
	manifest := readCaptureManifest(t, config, "conductor")
	snapshot := manifest.Snapshots[0]
	marker := loadIndexMarker(target.Dir)
	marker.add(snapshot.Path, snapshot.Generations[0].CapturedAt)
	if err := marker.save(target.Dir, &manifest); err != nil {
		t.Fatal(err)
	}
	a := newWorkAuthority(1)
	seen := map[string]int{}
	override(t, &captureFault, func(stage, _ string) error {
		seen[stage] = a.facts().CPUAllocated
		return nil
	})
	synced := -1
	override(t, &syncToDrive, func(*os.File) error {
		synced = a.facts().CPUAllocated
		return nil
	})
	var done []compressedGeneration
	if err := compressCaptures(withWorkAuthority(t.Context(), a), config, nil, func(g compressedGeneration) { done = append(done, g) }); err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("compressed %+v", done)
	}
	if seen["compress-encode"] != 1 || seen["compress-verify"] != 1 || seen["compress-manifest"] != 0 || synced != 0 {
		t.Fatalf("slots held at each step: %v, durable sync %d", seen, synced)
	}
	if facts := a.facts(); facts.CPUAllocated != 0 {
		t.Fatalf("after the pass: %+v", facts)
	}
}
