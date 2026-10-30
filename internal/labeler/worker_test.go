package labeler_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/labeler"
	"github.com/gbdubs/pharos/internal/labeler/labelertest"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(labelertest.FakeWorkerEnv); mode != "" {
		os.Exit(labelertest.FakeWorker(mode))
	}
	os.Exit(m.Run())
}

func fakeConfig(t *testing.T, mode string) labeler.WorkerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), labelertest.FakeWorkerEnv+"="+mode, labelertest.FakeCountEnv+"="+filepath.Join(t.TempDir(), "count"))
	return labeler.WorkerConfig{Path: exe, Env: env, Model: "fake.onnx", VocabSize: 50368, StartTimeout: 20 * time.Second}
}

func fakeBatch(rows ...[]int32) []labeler.Tensor { return labeler.BatchTensors(rows, 8) }

func fakeProb(ids []int32, j int) float32 { return labelertest.FakeProb(ids, j) }

func TestWorkerRunsBatchesConcurrently(t *testing.T) {
	worker, err := labeler.StartWorker(t.Context(), fakeConfig(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if worker.Ready.Worker.Name != "fake" {
		t.Fatalf("ready %+v", worker.Ready)
	}
	if _, err := worker.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows := [][]int32{{labeler.ClsID, int32(n), labeler.SepID}, {labeler.ClsID, int32(n * 7), int32(n), labeler.SepID}}
			result, err := worker.Run(t.Context(), fakeBatch(rows...), []string{"probs"})
			if err != nil {
				t.Error(err)
				return
			}
			probs := result.Tensor("probs")
			for row := range rows {
				if got, want := probs.Float32[row*4+1], fakeProb(rows[row], 1); got != want {
					t.Errorf("batch %d row %d: %v, want %v", n, row, got, want)
				}
			}
		}()
	}
	wg.Wait()
	// A malformed request is the worker's error for that request only.
	bad := fakeBatch([]int32{labeler.ClsID, 99999, labeler.SepID})
	if _, err := worker.Run(t.Context(), bad, nil); err == nil {
		t.Fatal("an out-of-vocabulary id was accepted")
	} else if reply := (*labeler.ErrorReply)(nil); !errors.As(err, &reply) || reply.Code != labeler.ErrCodeBadRequest {
		t.Fatalf("bad request: %v", err)
	}
	if _, err := worker.Run(t.Context(), fakeBatch([]int32{labeler.ClsID, labeler.SepID}), nil); err != nil {
		t.Fatalf("after a bad request: %v", err)
	}
}

func TestWorkerStartFailureNamesTheCause(t *testing.T) {
	_, err := labeler.StartWorker(t.Context(), fakeConfig(t, "refuse"))
	if err == nil || !strings.Contains(err.Error(), "no model at fake.onnx") {
		t.Fatalf("refused start: %v", err)
	}
	cfg := fakeConfig(t, "ok")
	cfg.Path = filepath.Join(t.TempDir(), "missing-worker")
	if _, err := labeler.StartWorker(t.Context(), cfg); err == nil {
		t.Fatal("a missing worker started")
	}
}

func TestPoolRestartsACrashedWorker(t *testing.T) {
	var restarts atomic.Int32
	cfg := labeler.PoolConfig{Worker: fakeConfig(t, "crash-after:3"), Backoff: func(int) time.Duration { return 10 * time.Millisecond },
		OnRestart: func(int, error) { restarts.Add(1) }}
	pool, err := labeler.NewPool(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Every fourth run crashes the worker; each batch still gets its result.
	for n := range 10 {
		row := []int32{labeler.ClsID, int32(n), labeler.SepID}
		result, err := pool.Run(t.Context(), fakeBatch(row), nil)
		if err != nil {
			t.Fatalf("batch %d: %v", n, err)
		}
		if got := result.Tensor("probs").Float32[0]; got != fakeProb(row, 0) {
			t.Fatalf("batch %d: %v", n, got)
		}
	}
	if restarts.Load() < 2 || pool.Restarts() != int(restarts.Load()) {
		t.Fatalf("restarts %d / %d", restarts.Load(), pool.Restarts())
	}
}

// A batch that keeps crashing workers fails as a crash, which its caller
// can split; the pool keeps serving other batches. A worker that crashes on
// everything exhausts the restarts in a row and stops the pool.
func TestPoolSurvivesAPoisonBatchButNotAWorkerThatCannotRun(t *testing.T) {
	pool, err := labeler.NewPool(t.Context(), labeler.PoolConfig{Worker: fakeConfig(t, "crash-on-id:777"), Backoff: func(int) time.Duration { return time.Millisecond }})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for range 3 {
		_, err = pool.Run(t.Context(), fakeBatch([]int32{labeler.ClsID, 777, labeler.SepID}), nil)
		if !errors.Is(err, labeler.ErrWorkerExited) || errors.Is(err, labeler.ErrWorkersUnavailable) || !strings.Contains(err.Error(), "exit status 4") {
			t.Fatalf("poison batch: %v", err)
		}
		// Each success resets the restarts in a row.
		if _, err := pool.Run(t.Context(), fakeBatch([]int32{labeler.ClsID, 1, labeler.SepID}), nil); err != nil {
			t.Fatalf("after the poison batch: %v", err)
		}
	}
	broken, err := labeler.NewPool(t.Context(), labeler.PoolConfig{Worker: fakeConfig(t, "crash-after:0"), MaxRestarts: 4, Backoff: func(int) time.Duration { return time.Millisecond }})
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	for attempt := 0; ; attempt++ {
		_, err := broken.Run(t.Context(), fakeBatch([]int32{labeler.ClsID, 1, labeler.SepID}), nil)
		if errors.Is(err, labeler.ErrWorkersUnavailable) {
			break
		}
		if attempt > 5 || !errors.Is(err, labeler.ErrWorkerExited) {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if broken.Restarts() != 4 {
		t.Fatalf("%d restarts, want 4", broken.Restarts())
	}
}

// A worker that stops answering is killed after the batch timeout and the
// batch goes to a restarted one.
func TestPoolKillsASilentWorker(t *testing.T) {
	pool, err := labeler.NewPool(t.Context(), labeler.PoolConfig{Worker: fakeConfig(t, "slow"), BatchTimeout: 20 * time.Millisecond, Retries: 1,
		Backoff: func(int) time.Duration { return time.Millisecond }})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Run(t.Context(), fakeBatch([]int32{labeler.ClsID, labeler.SepID}), nil); !errors.Is(err, labeler.ErrWorkerExited) || !strings.Contains(err.Error(), "no reply within") {
		t.Fatalf("silent worker: %v", err)
	}
	if pool.Restarts() == 0 {
		t.Fatal("the silent worker was not replaced")
	}
}

func TestWorkerStopsWithItsContext(t *testing.T) {
	worker, err := labeler.StartWorker(t.Context(), fakeConfig(t, "slow"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 1)
	go func() {
		_, err := worker.Run(ctx, fakeBatch([]int32{labeler.ClsID, labeler.SepID}), nil)
		errs <- err
	}()
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run: %v", err)
	}
	started := time.Now()
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("close took %s", took)
	}
}
