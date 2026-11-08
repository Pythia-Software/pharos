package labeler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The runtime API, independent of any ledger: a Runner runs framed rows
// through one model's workers and returns outputs by name; a Manager keeps
// one Runner per model under one thread and memory budget shared by every
// model, starting workers on demand and stopping idle ones. The labels
// ledger is one client; another ledger with its own pass and table uses the
// same Manager.

// Runner runs framed rows through one model's pool of workers.
type Runner struct {
	Model *Model
	pool  *Pool
	cfg   RunnerConfig
}

// RunnerConfig says how to run a model's workers.
type RunnerConfig struct {
	// Worker and Args start a worker; Env, when set, replaces its
	// environment.
	Worker string
	Args   []string
	Env    []string
	// GPU reserves the single shared Metal device and at least 3072 MB.
	GPU bool
	// Workers processes, each with Threads intra-op threads, and InFlight
	// batches outstanding at each (default 2; 1 for one at a time).
	Workers, Threads, InFlight int
	// MaxBatch is the most rows per batch the worker is told to expect.
	MaxBatch int
	// Pool options: restart backoff and its hook.
	Backoff   func(n int) time.Duration
	OnRestart func(slot int, cause error)
}

// NewRunner starts a model's workers.
func NewRunner(ctx context.Context, model *Model, cfg RunnerConfig) (*Runner, error) {
	cfg.Workers, cfg.Threads = max(1, cfg.Workers), max(1, cfg.Threads)
	pool, err := NewPool(ctx, PoolConfig{Workers: cfg.Workers, Backoff: cfg.Backoff, OnRestart: cfg.OnRestart,
		Worker: WorkerConfig{Path: cfg.Worker, Args: cfg.Args, Env: cfg.Env, Model: model.Path, ModelSHA256: model.SHA256, Threads: cfg.Threads,
			VocabSize: model.Tokenizer.VocabSize(), MaxBatch: cfg.MaxBatch, MaxSeq: model.MaxSeq, Inputs: model.Inputs, InFlight: cfg.InFlight}})
	if err != nil {
		return nil, err
	}
	limits := pool.Ready().Limits
	if limits.MaxSeq > 0 && limits.MaxSeq < model.MaxSeq {
		pool.Kill()
		return nil, fmt.Errorf("labeler worker takes sequences of at most %d tokens; model %s needs %d", limits.MaxSeq, model.Manifest.Name, model.MaxSeq)
	}
	return &Runner{Model: model, pool: pool, cfg: cfg}, nil
}

// Pool is the runner's pool (its restarts, worker PIDs and handshake).
func (r *Runner) Pool() *Pool { return r.pool }

// Legacy workers omit Device and retain the CPU thread contract. Auto sessions
// that actually fell back to CPU must still retune, despite reserving GPU capacity.
func (r *Runner) UsesCPUThreads() bool { return r.pool.Ready().Worker.Device != "gpu" }

// Threads is the CPU thread target used to start (and restart) these workers.
func (r *Runner) Threads() int { return r.cfg.Threads }

// MaxBatch is the most rows a batch may hold: the configured limit, lowered
// to what the worker announced.
func (r *Runner) MaxBatch() int {
	batch := max(1, r.cfg.MaxBatch)
	if limit := r.pool.Ready().Limits.MaxBatch; limit > 0 && limit < batch {
		batch = limit
	}
	return batch
}

// TokenBudget caps a caller's token budget to the backend's memory limit.
func (r *Runner) TokenBudget(requested int) int {
	if limit := r.pool.Ready().Limits.MaxTokens; limit > 0 && (requested <= 0 || requested > limit) {
		return limit
	}
	return requested
}

// Capacity is how many batches the runner keeps in flight: InFlight per
// worker (default 2).
func (r *Runner) Capacity() int {
	inflight := r.cfg.InFlight
	if inflight <= 0 {
		inflight = 2
	}
	return r.cfg.Workers * inflight
}

// RunBatch runs one batch of rows: the model's inputs made by its contract,
// padded to its multiple; the outputs asked for (which the model must
// declare), batch-first.
func (r *Runner) RunBatch(ctx context.Context, rows []Framed, outputs []string) (*RunResult, error) {
	if err := r.Model.HasOutputs(outputs); err != nil {
		return nil, err
	}
	return r.pool.Run(ctx, BatchInputs(r.Model.Inputs, rows, r.Model.PadMultiple), outputs)
}

// RowOutputs are one row's outputs by name: its slice of each tensor.
type RowOutputs map[string][]float32

// Run runs any number of rows: packed by length into batches under
// tokenBudget padded tokens, Capacity at a time, results in row order.
func (r *Runner) Run(ctx context.Context, rows []Framed, outputs []string, tokenBudget int) ([]RowOutputs, error) {
	return r.RunWithProgress(ctx, rows, outputs, tokenBudget, nil)
}

// RunWithProgress is Run with a callback for each successful batch's row count.
// Callbacks may overlap; the caller must synchronize any shared progress state.
func (r *Runner) RunWithProgress(ctx context.Context, rows []Framed, outputs []string, tokenBudget int, progress func(int)) ([]RowOutputs, error) {
	lengths := make([]int, len(rows))
	for i, row := range rows {
		lengths[i] = len(row.IDs)
	}
	results := make([]RowOutputs, len(rows))
	batches := PackBatches(lengths, r.TokenBudget(tokenBudget), r.MaxBatch(), r.Model.PadMultiple)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var failure error
	slots := make(chan struct{}, r.Capacity())
	for _, batch := range batches {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			framed := make([]Framed, len(batch))
			for k, index := range batch {
				framed[k] = rows[index]
			}
			result, err := r.RunBatch(ctx, framed, outputs)
			if err != nil {
				once.Do(func() { failure = err; cancel() })
				return
			}
			for k, index := range batch {
				row := RowOutputs{}
				for _, tensor := range result.Outputs {
					width := len(tensor.Float32) / len(batch)
					row[tensor.Name] = tensor.Float32[k*width : (k+1)*width]
				}
				results[index] = row
			}
			if progress != nil {
				progress(len(batch))
			}
		}()
	}
	wg.Wait()
	if failure == nil && ctx.Err() != nil {
		failure = ctx.Err()
	}
	return results, failure
}

// Budget bounds what every model's workers use together: intra-op threads
// and estimated memory (Model.MemoryMB per worker).
type Budget struct {
	Threads, MemoryMB int
}

// ErrBudget reports a runner that does not fit the budget even after idle
// runners are stopped.
var ErrBudget = errors.New("labeler budget exhausted")

// Manager keeps one Runner per model, within one Budget shared by all.
type Manager struct {
	budget Budget
	idle   time.Duration
	mu     sync.Mutex
	// runners by model (file and SHA-256).
	runners         map[string]*managed
	threads, memory int
	gpu             int
	closed          bool
}

type managed struct {
	runner          *Runner
	users           int
	threads, memory int
	gpu             int
	lastUsed        time.Time
	timer           *time.Timer
	retired         bool
}

// NewManager makes a manager; idle is how long an unused runner keeps its
// workers (0: stop them as soon as the last user releases it).
func NewManager(budget Budget, idle time.Duration) *Manager {
	return &Manager{budget: budget, idle: idle, runners: map[string]*managed{}}
}

func modelKey(model *Model) string { return model.Path + "@" + model.SHA256 }

// runnerKey is a model with how its workers run: a different configuration
// (one at a time, other thread counts) is another runner.
func runnerKey(model *Model, cfg RunnerConfig) string {
	args, _ := json.Marshal(struct{ Args, Env []string }{cfg.Args, cfg.Env})
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d|%t|%s", modelKey(model), cfg.Worker, max(1, cfg.Workers), max(1, cfg.Threads), cfg.InFlight, cfg.MaxBatch, cfg.GPU, args)
}

// Runner returns model's runner, starting it within the budget when it is
// not running: idle runners of other models are stopped to make room, and a
// runner that still does not fit gets fewer workers (at least one) or fails
// with ErrBudget. Call release when done; the runner then stays warm for
// the idle period.
func (m *Manager) Runner(ctx context.Context, model *Model, cfg RunnerConfig) (*Runner, func(), error) {
	var dead *Runner
	defer func() { reapRunner(dead) }() // after the unlock below
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, errors.New("labeler manager is closed")
	}
	key := runnerKey(model, cfg)
	if entry := m.runners[key]; entry != nil && entry.runner.pool.alive() {
		if entry.retired {
			return nil, nil, fmt.Errorf("%w: the previous runner is draining after a deliberate settings change", ErrBudget)
		}
		entry.users++
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		return entry.runner, m.releaser(key, entry), nil
	} else if entry != nil {
		dead = m.dropLocked(key, entry, true)
	}
	cfg.Workers, cfg.Threads = max(1, cfg.Workers), max(1, cfg.Threads)
	memoryMB := model.MemoryMB
	gpu := 0
	if cfg.GPU {
		gpu = 1
		cfg.Workers = 1
		memoryMB = max(memoryMB, 3072)
	}
	fits := func(workers int) bool {
		if m.gpu+gpu > 1 {
			return false
		}
		return (m.budget.Threads <= 0 || m.threads+workers*cfg.Threads <= m.budget.Threads) &&
			(m.budget.MemoryMB <= 0 || m.memory+workers*memoryMB <= m.budget.MemoryMB)
	}
	for !fits(cfg.Workers) {
		if m.evictIdleLocked() {
			continue
		}
		if cfg.Workers > 1 {
			cfg.Workers--
			continue
		}
		if gpu > 0 && m.gpu > 0 {
			return nil, nil, fmt.Errorf("%w: the shared GPU is in use", ErrBudget)
		}
		return nil, nil, fmt.Errorf("%w: model %s needs %d threads and %d MB; %d threads and %d MB of %d and %d are in use",
			ErrBudget, model.Manifest.Name, cfg.Threads, memoryMB, m.threads, m.memory, m.budget.Threads, m.budget.MemoryMB)
	}
	runner, err := NewRunner(ctx, model, cfg)
	if err != nil {
		return nil, nil, err
	}
	entry := &managed{runner: runner, users: 1, threads: cfg.Workers * cfg.Threads, memory: cfg.Workers * memoryMB, gpu: gpu}
	m.threads += entry.threads
	m.memory += entry.memory
	m.gpu += entry.gpu
	m.runners[key] = entry
	return runner, m.releaser(key, entry), nil
}

func (m *Manager) releaser(key string, entry *managed) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			entry.users--
			entry.lastUsed = time.Now()
			if entry.users > 0 || m.runners[key] != entry {
				return
			}
			if m.idle <= 0 || entry.retired {
				m.dropLocked(key, entry, false)
				return
			}
			entry.timer = time.AfterFunc(m.idle, func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.runners[key] == entry && entry.users == 0 {
					m.dropLocked(key, entry, false)
				}
			})
		})
	}
}

// Retire ends cached configurations after a deliberate allowance/settings
// change. In-use workers drain before their last release; ordinary transient
// capacity changes never call this, so batching keeps models warm.
func (m *Manager) Retire() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, entry := range m.runners {
		if entry.users == 0 {
			m.dropLocked(key, entry, false)
		} else {
			entry.retired = true
		}
	}
}

// evictIdleLocked stops the least recently used runner nobody is using.
func (m *Manager) evictIdleLocked() bool {
	var oldest string
	for key, entry := range m.runners {
		if entry.users == 0 && (oldest == "" || entry.lastUsed.Before(m.runners[oldest].lastUsed)) {
			oldest = key
		}
	}
	if oldest == "" {
		return false
	}
	m.dropLocked(oldest, m.runners[oldest], false)
	return true
}

// dropLocked removes a runner. A GPU runner is killed and reaped before the
// manager is released, so the GPU it held is free when the budget says so.
// A killed CPU runner is only signalled here and returned: the caller reaps
// it (reapRunner) after releasing the manager and before it returns, so a
// worker slow to exit holds up no other model's Runner or release, and a
// caller's CPU grant still outlives the workers. Otherwise the workers
// finish in the background.
func (m *Manager) dropLocked(key string, entry *managed, kill bool) (reap *Runner) {
	m.forgetLocked(key, entry)
	switch {
	case entry.gpu > 0:
		entry.runner.pool.Kill()
		entry.runner.pool.Close()
	case kill:
		entry.runner.pool.Kill()
		return entry.runner
	default:
		go entry.runner.pool.Close()
	}
	return nil
}

// reapRunner waits for a killed runner's workers to exit (nil: none). Tests
// replace it to check that the manager is free meanwhile.
var reapRunner = func(r *Runner) {
	if r != nil {
		r.pool.Close()
	}
}

// forgetLocked removes a runner from the manager and its budget.
func (m *Manager) forgetLocked(key string, entry *managed) {
	delete(m.runners, key)
	m.threads -= entry.threads
	m.memory -= entry.memory
	m.gpu -= entry.gpu
	if entry.timer != nil {
		entry.timer.Stop()
	}
}

// Kill stops a runner's workers at once (a stop, where results in flight
// are not wanted); its users' calls fail. It returns once the workers are
// reaped, so a caller's CPU grant outlives them, but reaps them after
// releasing the manager: other models' Runner calls do not wait for it. A
// GPU runner is reaped before the lock is released, as on any drop.
func (m *Manager) Kill(runner *Runner) {
	m.mu.Lock()
	reap := []*Runner{}
	for key, entry := range m.runners {
		if entry.runner == runner {
			reap = append(reap, m.dropLocked(key, entry, true))
		}
	}
	m.mu.Unlock()
	for _, killed := range reap {
		reapRunner(killed)
	}
}

// SetBudget changes the budget for runners started from now on (a power
// change: fewer threads on battery). Idle runners over the new budget stop;
// runners in use keep their workers until released, and a smaller budget
// then holds the next start to it.
func (m *Manager) SetBudget(budget Budget) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.budget = budget
	over := func() bool {
		return budget.Threads > 0 && m.threads > budget.Threads || budget.MemoryMB > 0 && m.memory > budget.MemoryMB
	}
	for over() && m.evictIdleLocked() {
	}
}

// Budget is the current budget.
func (m *Manager) Budget() Budget {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.budget
}

// Usage is the threads and memory the running workers hold.
func (m *Manager) Usage() (threads, memoryMB, runners int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.threads, m.memory, len(m.runners)
}

// Close stops every runner and refuses new ones.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	entries := m.runners
	m.runners = map[string]*managed{}
	m.threads, m.memory, m.gpu = 0, 0, 0
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, entry := range entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		wg.Add(1)
		go func() { defer wg.Done(); entry.runner.pool.Close() }()
	}
	wg.Wait()
}
