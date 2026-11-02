package labeler

import (
	"errors"
	"os"
	"path/filepath"
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
	outputs, err := runner.Run(t.Context(), rows, []string{"probs"}, 64)
	if err != nil {
		t.Fatal(err)
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
}
