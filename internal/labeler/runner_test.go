package labeler

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunnerConfig runs the test binary as the fake worker
// (labelertest.FakeWorker), which reads the label count from the model's
// manifest.
func fakeRunnerConfig(t *testing.T) RunnerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PHAROS_LABELER_FAKE_WORKER=ok", "PHAROS_LABELER_FAKE_COUNT="+filepath.Join(t.TempDir(), "count"))
	return RunnerConfig{Worker: exe, Env: env, Workers: 1, Threads: 1, MaxBatch: 4}
}

func fakeRowProb(ids []int32, j int) float32 {
	sum := 0
	for _, id := range ids {
		sum += int(id)
	}
	return float32((sum+j)%100) / 100
}

func TestRunnerRunsRowsInOrderByName(t *testing.T) {
	model, err := LoadModel(writeModelDir(t, func(m map[string]any) {
		m["onnx"] = map[string]any{"outputs": map[string]string{"probs": "float32 [batch, 3]"}}
	}), "int8")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(t.Context(), model, fakeRunnerConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Pool().Close()
	rows := []Framed{}
	for n := range 11 {
		ids := []int32{ClsID}
		for range n % 5 {
			ids = append(ids, int32(100+n))
		}
		rows = append(rows, Framed{IDs: append(ids, SepID)})
	}
	var completed, batches atomic.Int64
	outputs, err := runner.RunWithProgress(t.Context(), rows, []string{"probs"}, 64, func(n int) {
		completed.Add(int64(n))
		batches.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Load() != int64(len(rows)) || batches.Load() < 3 {
		t.Fatalf("batch progress: %d rows in %d batches", completed.Load(), batches.Load())
	}
	for i, row := range rows {
		if got := outputs[i]["probs"]; len(got) != 3 || got[2] != fakeRowProb(row.IDs, 2) {
			t.Fatalf("row %d: %v", i, got)
		}
	}
	if _, err := runner.Run(t.Context(), rows[:1], []string{"spans"}, 64); err == nil {
		t.Fatal("an output the model does not declare was asked for")
	}
}

// One budget for every model: a runner that does not fit gets fewer
// workers, a model that still does not fit waits for idle runners to be
// stopped (or fails while they are in use), and a released runner stays
// warm for the next run.
func TestManagerSharesOneBudgetAcrossModels(t *testing.T) {
	first, err := LoadModel(writeModelDir(t, nil), "int8")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadModel(writeModelDir(t, func(m map[string]any) { m["name"] = "other" }), "int8")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Budget{Threads: 4, MemoryMB: 2000}, time.Minute)
	defer manager.Close()
	cfg := fakeRunnerConfig(t)
	cfg.Workers, cfg.Threads = 2, 2
	a, releaseA, err := manager.Runner(t.Context(), first, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if threads, memory, _ := manager.Usage(); threads != 2 || memory != 1600 {
		t.Fatalf("two 1,600 MB workers in a 2,000 MB budget: %d threads, %d MB", threads, memory)
	}
	if _, _, err := manager.Runner(t.Context(), second, cfg); !errors.Is(err, ErrBudget) {
		t.Fatalf("a second model while the first is in use: %v", err)
	}
	releaseA()
	again, releaseAgain, err := manager.Runner(t.Context(), first, cfg)
	if err != nil || again != a {
		t.Fatalf("a released runner was not reused warm: %v", err)
	}
	releaseAgain()
	b, releaseB, err := manager.Runner(t.Context(), second, cfg)
	if err != nil || b == a {
		t.Fatalf("the idle runner was not stopped for the second model: %v", err)
	}
	defer releaseB()
	if _, memory, runners := manager.Usage(); runners != 1 || memory != 1600 {
		t.Fatalf("%d runners, %d MB", runners, memory)
	}
	// Killed workers are reaped before Kill (or the Runner call that found
	// them dead) returns, and after the manager is released.
	reaped := []*Runner{}
	reap := reapRunner
	t.Cleanup(func() { reapRunner = reap })
	reapRunner = func(r *Runner) {
		if r == nil {
			return
		}
		if !manager.mu.TryLock() {
			t.Error("a killed runner was reaped under the manager's lock")
		} else {
			manager.mu.Unlock()
		}
		reap(r)
		reaped = append(reaped, r)
	}
	requireReaped := func(r *Runner, what string) {
		t.Helper()
		if len(reaped) == 0 || reaped[len(reaped)-1] != r {
			t.Fatalf("%s: reaped %v", what, reaped)
		}
		for _, slot := range r.pool.slots {
			if slot.worker != nil && slot.worker.cmd.ProcessState == nil {
				t.Fatalf("%s returned before the native worker was reaped", what)
			}
		}
	}
	manager.Kill(b)
	requireReaped(b, "Kill")
	c, releaseC, err := manager.Runner(t.Context(), first, cfg)
	if err != nil {
		t.Fatal(err)
	}
	releaseC()
	c.pool.mu.Lock()
	c.pool.closed = true // dead: the next Runner call replaces it
	c.pool.mu.Unlock()
	d, releaseD, err := manager.Runner(t.Context(), first, cfg)
	if err != nil || d == c {
		t.Fatalf("a dead runner was not replaced: %v", err)
	}
	defer releaseD()
	requireReaped(c, "Runner over a dead runner")
}

// A smaller budget (on battery) stops idle runners over it and holds the
// next start to it; a larger one (plugged in) lets the next start use it.
func TestManagerSetBudget(t *testing.T) {
	model, err := LoadModel(writeModelDir(t, func(m map[string]any) { m["memory_mb"] = 10 }), "int8")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Budget{Threads: 4}, time.Minute)
	defer manager.Close()
	cfg := fakeRunnerConfig(t)
	cfg.Workers, cfg.Threads = 2, 2
	_, release, err := manager.Runner(t.Context(), model, cfg)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if threads, _, runners := manager.Usage(); threads != 4 || runners != 1 {
		t.Fatalf("plugged in: %d threads in %d runners", threads, runners)
	}
	manager.SetBudget(Budget{Threads: 2})
	if threads, _, runners := manager.Usage(); threads != 0 || runners != 0 {
		t.Fatalf("the idle runner over the battery budget kept %d threads in %d runners", threads, runners)
	}
	_, release, err = manager.Runner(t.Context(), model, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if threads, _, _ := manager.Usage(); threads != 2 {
		t.Fatalf("on battery the runner took %d threads of 2", threads)
	}
	release()
	manager.SetBudget(Budget{Threads: 4})
	if got := manager.Budget(); got.Threads != 4 {
		t.Fatalf("budget %+v", got)
	}
	active, releaseActive, err := manager.Runner(t.Context(), model, cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager.Retire()
	if _, _, runners := manager.Usage(); runners != 1 {
		t.Fatal("a deliberate change stopped a runner still in use")
	}
	if _, _, err := manager.Runner(t.Context(), model, cfg); !errors.Is(err, ErrBudget) {
		t.Fatalf("a retiring configuration accepted another user: %v", err)
	}
	releaseActive()
	if _, _, runners := manager.Usage(); runners != 0 {
		t.Fatal("the retiring runner stayed resident after its last release")
	}
	fresh, releaseFresh, err := manager.Runner(t.Context(), model, cfg)
	if err != nil || fresh == active {
		t.Fatalf("a deliberate change reused the retired configuration: %v", err)
	}
	releaseFresh()
	manager.Retire()
	if _, _, runners := manager.Usage(); runners != 0 {
		t.Fatal("idle configurations survived retirement")
	}
}

func TestManagerGPUAdmission(t *testing.T) {
	// Auto can reserve the GPU before startup and still select CPU fallback.
	t.Setenv("PHAROS_LABELER_FAKE_DEVICE", "cpu")
	first, err := LoadModel(writeModelDir(t, nil), "int8")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadModel(writeModelDir(t, nil), "int8")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Budget{Threads: 16, MemoryMB: 8192}, time.Minute)
	defer manager.Close()
	cfg := fakeRunnerConfig(t)
	cfg.GPU = true
	cfg.Workers = 4
	a, release, err := manager.Runner(t.Context(), first, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if threads, memory, runners := manager.Usage(); threads != 1 || memory != 3072 || runners != 1 {
		t.Fatalf("GPU reservation: %d threads %d MB %d runners", threads, memory, runners)
	}
	if !a.UsesCPUThreads() {
		t.Fatal("GPU reservation hid the actual CPU fallback thread control")
	}
	if _, _, err := manager.Runner(t.Context(), second, cfg); !errors.Is(err, ErrBudget) {
		t.Fatalf("competing GPU worker admitted: %v", err)
	}
	// CPU work may coexist under the same memory/thread budget.
	cpu := cfg
	cpu.GPU = false
	cpu.Workers = 1
	_, releaseCPU, err := manager.Runner(t.Context(), second, cpu)
	if err != nil {
		t.Fatal(err)
	}
	releaseCPU()
	release()
	b, releaseB, err := manager.Runner(t.Context(), second, cfg)
	if err != nil || b == a {
		t.Fatalf("idle GPU not reclaimed: %v", err)
	}
	releaseB()
	manager.SetBudget(Budget{Threads: 16, MemoryMB: 3000})
	if _, _, err := manager.Runner(t.Context(), first, cfg); !errors.Is(err, ErrBudget) {
		t.Fatalf("GPU memory estimate ignored: %v", err)
	}
}

func TestRunnerIdentityIncludesLaunchConfiguration(t *testing.T) {
	model := &Model{Path: "model", SHA256: "sum"}
	base := RunnerConfig{Worker: "worker", Workers: 1, Threads: 1}
	key := runnerKey(model, base)
	for _, cfg := range []RunnerConfig{
		{Worker: "worker", Workers: 1, Threads: 1, Args: []string{"--backend", "mlx"}},
		{Worker: "worker", Workers: 1, Threads: 1, Env: []string{"PHAROS_MLX_LIBRARY=/engine"}},
		{Worker: "worker", Workers: 1, Threads: 1, GPU: true},
	} {
		if runnerKey(model, cfg) == key {
			t.Fatalf("launch configuration reused the old runner: %+v", cfg)
		}
	}
}
