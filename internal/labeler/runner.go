package labeler

import (
	"context"
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

// MaxBatch is the most rows a batch may hold: the configured limit, lowered
// to what the worker announced.
func (r *Runner) MaxBatch() int {
	batch := max(1, r.cfg.MaxBatch)
	if limit := r.pool.Ready().Limits.MaxBatch; limit > 0 && limit < batch {
		batch = limit
	}
	return batch
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
	lengths := make([]int, len(rows))
	for i, row := range rows {
		lengths[i] = len(row.IDs)
	}
	results := make([]RowOutputs, len(rows))
	batches := PackBatches(lengths, tokenBudget, r.MaxBatch(), r.Model.PadMultiple)
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
	closed          bool
}

type managed struct {
	runner          *Runner
	users           int
	threads, memory int
	lastUsed        time.Time
	timer           *time.Timer
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
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d", modelKey(model), cfg.Worker, max(1, cfg.Workers), max(1, cfg.Threads), cfg.InFlight, cfg.MaxBatch)
}

// Runner returns model's runner, starting it within the budget when it is
// not running: idle runners of other models are stopped to make room, and a
// runner that still does not fit gets fewer workers (at least one) or fails
// with ErrBudget. Call release when done; the runner then stays warm for
// the idle period.
func (m *Manager) Runner(ctx context.Context, model *Model, cfg RunnerConfig) (*Runner, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, errors.New("labeler manager is closed")
	}
	key := runnerKey(model, cfg)
	if entry := m.runners[key]; entry != nil && entry.runner.pool.alive() {
		entry.users++
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		return entry.runner, m.releaser(key, entry), nil
	} else if entry != nil {
		m.dropLocked(key, entry, true)
	}
	cfg.Workers, cfg.Threads = max(1, cfg.Workers), max(1, cfg.Threads)
	fits := func(workers int) bool {
		return (m.budget.Threads <= 0 || m.threads+workers*cfg.Threads <= m.budget.Threads) &&
			(m.budget.MemoryMB <= 0 || m.memory+workers*model.MemoryMB <= m.budget.MemoryMB)
	}
	for !fits(cfg.Workers) {
		if m.evictIdleLocked() {
			continue
		}
		if cfg.Workers > 1 {
			cfg.Workers--
			continue
		}
		return nil, nil, fmt.Errorf("%w: model %s needs %d threads and %d MB; %d threads and %d MB of %d and %d are in use",
			ErrBudget, model.Manifest.Name, cfg.Threads, model.MemoryMB, m.threads, m.memory, m.budget.Threads, m.budget.MemoryMB)
	}
	runner, err := NewRunner(ctx, model, cfg)
	if err != nil {
		return nil, nil, err
	}
	entry := &managed{runner: runner, users: 1, threads: cfg.Workers * cfg.Threads, memory: cfg.Workers * model.MemoryMB}
	m.threads += entry.threads
	m.memory += entry.memory
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
			if m.idle <= 0 {
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

func (m *Manager) dropLocked(key string, entry *managed, kill bool) {
	delete(m.runners, key)
	m.threads -= entry.threads
	m.memory -= entry.memory
	if entry.timer != nil {
		entry.timer.Stop()
	}
	if kill {
		entry.runner.pool.Kill()
	} else {
		go entry.runner.pool.Close()
	}
}

// Kill stops a runner's workers at once (a stop, where results in flight
// are not wanted); its users' calls fail.
func (m *Manager) Kill(runner *Runner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, entry := range m.runners {
		if entry.runner == runner {
			m.dropLocked(key, entry, true)
		}
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
	m.threads, m.memory = 0, 0
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
