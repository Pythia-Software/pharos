package labeler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WorkerConfig says how to start a worker and what to ask it to load.
type WorkerConfig struct {
	// Path and Args start the worker; Env, when set, replaces the environment.
	Path string
	Args []string
	Env  []string
	// Model and ModelSHA256 go in the hello, with the limits below.
	Model, ModelSHA256 string
	Threads, VocabSize int
	// Inputs are the model's input contract (Model.Inputs); empty means
	// DefaultInputs.
	Inputs                     []InputSpec
	MaxBatch, MaxSeq           int
	Options                    map[string]any
	StartTimeout, CloseTimeout time.Duration
	// InFlight bounds the run requests outstanding at the worker (default 2):
	// enough that it never waits for the next batch, few enough that a stop
	// waits for little.
	InFlight int
}

// ErrWorkerExited reports a worker that exited or closed its output: a crash
// as far as the work in flight is concerned.
var ErrWorkerExited = errors.New("labeler worker exited")

// WorkerExitError is a worker's exit, with the end of its diagnostics.
type WorkerExitError struct {
	Status, Stderr string
	Cause          error
}

func (e *WorkerExitError) Error() string {
	text := "labeler worker exited"
	if e.Status != "" {
		text += " (" + e.Status + ")"
	}
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	if e.Stderr != "" {
		text += "; stderr: " + e.Stderr
	}
	return text
}

func (e *WorkerExitError) Is(target error) bool { return target == ErrWorkerExited }

// Worker is one running worker process.
type Worker struct {
	cfg     WorkerConfig
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	slots   chan struct{}
	stderr  *tailBuffer
	// Ready is the worker's handshake reply.
	Ready Ready

	mu      sync.Mutex
	pending map[uint64]chan workerReply
	pings   map[uint64]chan struct{}
	nextID  atomic.Uint64
	failure error
	done    chan struct{}
	exited  chan struct{}
	reaped  sync.Once
}

// reap waits for the process once its output has been read to the end
// (exec.Cmd.Wait must not close the pipe under a reader), then closes exited.
func (w *Worker) reap() {
	w.reaped.Do(func() {
		go func() {
			_ = w.cmd.Wait()
			close(w.exited)
		}()
	})
}

type workerReply struct {
	result *RunResult
	err    error
}

// StartWorker starts a worker and completes the handshake.
func StartWorker(ctx context.Context, cfg WorkerConfig) (*Worker, error) {
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 60 * time.Second
	}
	if cfg.CloseTimeout <= 0 {
		cfg.CloseTimeout = 5 * time.Second
	}
	if cfg.InFlight <= 0 {
		cfg.InFlight = 2
	}
	w := &Worker{cfg: cfg, slots: make(chan struct{}, cfg.InFlight), stderr: &tailBuffer{limit: 4096},
		pending: map[uint64]chan workerReply{}, pings: map[uint64]chan struct{}{}, done: make(chan struct{}), exited: make(chan struct{})}
	w.cmd = exec.Command(cfg.Path, cfg.Args...)
	if cfg.Env != nil {
		w.cmd.Env = cfg.Env
	}
	w.cmd.Stderr = w.stderr
	var err error
	if w.stdin, err = w.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := w.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := w.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start labeler worker %s: %w", cfg.Path, err)
	}
	reader := NewFrameReader(stdout)
	hello := Hello{Protocol: []int{ProtocolVersion}, Model: cfg.Model, ModelSHA256: cfg.ModelSHA256, Threads: cfg.Threads,
		VocabSize: cfg.VocabSize, MaxBatch: cfg.MaxBatch, MaxSeq: cfg.MaxSeq, Options: cfg.Options, Inputs: cfg.Inputs}
	handshake := make(chan error, 1)
	go func() {
		if err := w.writeJSON(FrameHello, hello); err != nil {
			handshake <- err
			return
		}
		kind, payload, err := ReadFrame(reader, 0)
		switch {
		case err != nil:
			handshake <- err
		case kind == FrameError:
			var reply ErrorReply
			_ = json.Unmarshal(payload, &reply)
			handshake <- &reply
		case kind != FrameReady:
			handshake <- fmt.Errorf("labeler worker answered the hello with frame %q", kind)
		default:
			handshake <- json.Unmarshal(payload, &w.Ready)
		}
	}()
	timer := time.NewTimer(cfg.StartTimeout)
	defer timer.Stop()
	select {
	case err = <-handshake:
	case <-timer.C:
		err = fmt.Errorf("labeler worker did not become ready within %s", cfg.StartTimeout)
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil && w.Ready.Protocol != ProtocolVersion {
		err = fmt.Errorf("labeler worker chose protocol %d; this build speaks %d", w.Ready.Protocol, ProtocolVersion)
	}
	if err != nil {
		w.Kill()
		w.reap()
		<-w.exited
		return nil, w.exitError(err)
	}
	go w.readLoop(reader)
	return w, nil
}

func (w *Worker) writeJSON(kind byte, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.write(kind, payload)
}

func (w *Worker) write(kind byte, payload []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return WriteFrame(w.stdin, kind, payload)
}

// writeCtx writes a frame, giving up when ctx ends. A worker that stops
// reading its input lets a large frame fill the pipe and block the write, so
// the write must not outlast a stop, an eject or a batch timeout. A frame cut
// off midway leaves the stream unusable: the worker is killed and its input
// closed, which also unblocks the write.
func (w *Worker) writeCtx(ctx context.Context, kind byte, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() { written <- w.write(kind, payload) }()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		w.Kill()
		_ = w.stdin.Close()
		<-written
		return ctx.Err()
	}
}

// readLoop dispatches replies until the worker's output ends, then fails
// everything still waiting.
func (w *Worker) readLoop(reader io.Reader) {
	var failure error
	for failure == nil {
		kind, payload, err := ReadFrame(reader, 0)
		if err != nil {
			failure = err
			break
		}
		switch kind {
		case FrameResult:
			result, err := DecodeResult(payload)
			if err != nil {
				failure = fmt.Errorf("undecodable result: %w", err)
				break
			}
			w.deliver(result.ID, workerReply{result: result})
		case FrameError:
			reply := &ErrorReply{}
			if err := json.Unmarshal(payload, reply); err != nil {
				failure = fmt.Errorf("undecodable error frame: %w", err)
				break
			}
			if reply.Fatal {
				failure = reply
				break
			}
			if reply.ID == 0 {
				// A complaint about no request (an unsupported frame) is
				// diagnostics, as the protocol allows; the worker carries on.
				fmt.Fprintf(w.stderr, "\nworker: %s: %s\n", reply.Code, reply.Message)
				continue
			}
			w.deliver(reply.ID, workerReply{err: reply})
		case FramePong:
			nonce, err := DecodeNonce(payload)
			if err == nil {
				w.mu.Lock()
				if ch := w.pings[nonce]; ch != nil {
					close(ch)
					delete(w.pings, nonce)
				}
				w.mu.Unlock()
			}
		case FrameGoodbye:
			failure = io.EOF
		default:
			// Unknown replies are ignored, as the protocol allows.
		}
	}
	w.reap()
	w.fail(failure)
}

func (w *Worker) deliver(id uint64, reply workerReply) {
	w.mu.Lock()
	ch := w.pending[id]
	delete(w.pending, id)
	w.mu.Unlock()
	if ch != nil {
		ch <- reply
	}
}

// fail marks the worker dead and fails every request still waiting.
func (w *Worker) fail(cause error) {
	// Give the process a moment to exit, so its status and last stderr
	// lines are in the error.
	select {
	case <-w.exited:
	case <-time.After(time.Second):
	}
	w.mu.Lock()
	if w.failure != nil {
		w.mu.Unlock()
		return
	}
	w.failure = w.exitError(cause)
	pending := w.pending
	w.pending = map[uint64]chan workerReply{}
	for _, ch := range w.pings {
		close(ch)
	}
	w.pings = map[uint64]chan struct{}{}
	close(w.done)
	w.mu.Unlock()
	for _, ch := range pending {
		ch <- workerReply{err: w.failure}
	}
}

func (w *Worker) exitError(cause error) error {
	var reply *ErrorReply
	if errors.As(cause, &reply) && !reply.Fatal && reply.ID != 0 {
		return cause
	}
	status := ""
	select {
	case <-w.exited:
		if w.cmd.ProcessState != nil {
			status = w.cmd.ProcessState.String()
		}
	default:
	}
	if errors.Is(cause, io.EOF) {
		cause = nil
	}
	return &WorkerExitError{Status: status, Stderr: w.stderr.String(), Cause: cause}
}

// Done is closed once the worker has failed or exited; Err says why.
func (w *Worker) Done() <-chan struct{} { return w.done }

func (w *Worker) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failure
}

// Run sends one batch and waits for its result. A worker's non-fatal error
// (bad_request, run_failed) is returned as *ErrorReply; a worker that exits
// fails every request in flight with an error matching ErrWorkerExited.
func (w *Worker) Run(ctx context.Context, inputs []Tensor, outputs []string) (*RunResult, error) {
	select {
	case w.slots <- struct{}{}:
	case <-w.done:
		return nil, w.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-w.slots }()
	id := w.nextID.Add(1)
	payload, err := EncodeRun(&RunRequest{ID: id, Inputs: inputs, Outputs: outputs})
	if err != nil {
		return nil, err
	}
	ch := make(chan workerReply, 1)
	w.mu.Lock()
	if w.failure != nil {
		w.mu.Unlock()
		return nil, w.failure
	}
	w.pending[id] = ch
	w.mu.Unlock()
	if err := w.writeCtx(ctx, FrameRun, payload); err != nil {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		if errors.Is(err, ErrFrameTooLarge) {
			return nil, err
		}
		// A broken pipe means the worker is gone; its reader reports why.
		select {
		case <-w.done:
			return nil, w.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case reply := <-ch:
		return reply.result, reply.err
	case <-ctx.Done():
		// The slot is freed; the late reply finds no one waiting.
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Ping checks that the worker answers, and how quickly.
func (w *Worker) Ping(ctx context.Context) (time.Duration, error) {
	nonce := w.nextID.Add(1) | 1<<63
	ch := make(chan struct{})
	w.mu.Lock()
	if w.failure != nil {
		w.mu.Unlock()
		return 0, w.failure
	}
	w.pings[nonce] = ch
	w.mu.Unlock()
	started := time.Now()
	if err := w.writeCtx(ctx, FramePing, EncodeNonce(nonce)); err != nil {
		return 0, err
	}
	select {
	case <-ch:
		if err := w.Err(); err != nil {
			return 0, err
		}
		return time.Since(started), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Close asks the worker to quit, closes its input and waits for it to exit,
// killing it after CloseTimeout.
func (w *Worker) Close() error {
	quit, cancel := context.WithTimeout(context.Background(), w.cfg.CloseTimeout)
	_ = w.writeCtx(quit, FrameQuit, nil)
	cancel()
	_ = w.stdin.Close()
	select {
	case <-w.exited:
	case <-time.After(w.cfg.CloseTimeout):
		w.Kill()
		<-w.exited
	}
	return nil
}

// Kill stops the worker at once.
func (w *Worker) Kill() {
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
}

// PID is the worker's process ID.
func (w *Worker) PID() int {
	if w.cmd.Process == nil {
		return 0
	}
	return w.cmd.Process.Pid
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if extra := t.buf.Len() - t.limit; extra > 0 {
		t.buf.Next(extra)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.buf.String())
}

// PoolConfig configures a pool of identical workers.
type PoolConfig struct {
	Worker  WorkerConfig
	Workers int
	// Retries is how many times a batch whose worker crashed is sent again,
	// to a restarted worker (default 2). A batch that keeps crashing workers
	// fails with an error matching ErrWorkerExited; its caller can split it.
	Retries int
	// MaxRestarts bounds restarts in a row with no batch succeeding between
	// them (default 8); past it the pool fails with ErrWorkersUnavailable.
	// One bad input costs a few restarts, a worker that cannot run at all
	// stops the pool.
	MaxRestarts int
	// BatchTimeout bounds one batch (default 5 min): a worker that stays
	// alive but does not answer is killed, which counts as a crash.
	BatchTimeout time.Duration
	// Backoff is the wait before the nth restart of a worker (default 0.5 s,
	// doubling, at most 30 s).
	Backoff func(n int) time.Duration
	// OnRestart, when set, hears of each restart and its cause.
	OnRestart func(slot int, cause error)
}

// ErrWorkersUnavailable reports a pool that cannot keep a worker running.
var ErrWorkersUnavailable = errors.New("labeler workers keep failing")

// Pool runs batches on a fixed number of workers, restarting a worker that
// crashes (with backoff) and sending the batches it lost again.
type Pool struct {
	cfg   PoolConfig
	slots []*poolSlot
	next  atomic.Uint64
	mu    sync.Mutex
	// restarts counts every restart; consecutive those since a batch last
	// succeeded.
	restarts, consecutive int
	closed                bool
	ready                 Ready
}

type poolSlot struct {
	mu       sync.Mutex
	worker   *Worker
	failures int
}

// NewPool starts cfg.Workers workers.
func NewPool(ctx context.Context, cfg PoolConfig) (*Pool, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.Retries <= 0 {
		cfg.Retries = 2
	}
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = 8
	}
	if cfg.BatchTimeout <= 0 {
		cfg.BatchTimeout = 5 * time.Minute
	}
	if cfg.Backoff == nil {
		cfg.Backoff = func(n int) time.Duration {
			return min(30*time.Second, 500*time.Millisecond<<min(n, 6))
		}
	}
	p := &Pool{cfg: cfg}
	for range cfg.Workers {
		worker, err := StartWorker(ctx, cfg.Worker)
		if err != nil {
			p.Close()
			return nil, err
		}
		p.slots = append(p.slots, &poolSlot{worker: worker})
	}
	p.ready = p.slots[0].worker.Ready
	return p, nil
}

// Ready is the workers' handshake (the first worker's, when the pool
// started; restarted workers run the same binary and model).
func (p *Pool) Ready() Ready { return p.ready }

// Restarts is how many times the pool restarted a worker.
func (p *Pool) Restarts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restarts
}

// PIDs lists the workers' process IDs.
func (p *Pool) PIDs() []int {
	pids := []int{}
	for _, slot := range p.slots {
		slot.mu.Lock()
		if slot.worker != nil {
			pids = append(pids, slot.worker.PID())
		}
		slot.mu.Unlock()
	}
	return pids
}

// Run runs one batch on the next worker. A batch whose worker crashed (or
// timed out) is sent again, up to Retries times, and then fails with an
// error matching ErrWorkerExited; a worker's own error for the batch
// (bad_request, run_failed) is returned as is.
func (p *Pool) Run(ctx context.Context, inputs []Tensor, outputs []string) (*RunResult, error) {
	var last error
	for attempt := 0; attempt <= p.cfg.Retries; attempt++ {
		index := int(p.next.Add(1)-1) % len(p.slots)
		worker, err := p.worker(ctx, index)
		if err != nil {
			return nil, err
		}
		runCtx, cancel := context.WithTimeout(ctx, p.cfg.BatchTimeout)
		result, err := worker.Run(runCtx, inputs, outputs)
		cancel()
		if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			// Alive but silent: kill it, so it restarts like a crash.
			worker.Kill()
			<-worker.Done()
			err = fmt.Errorf("%w: no reply within %s", worker.Err(), p.cfg.BatchTimeout)
		}
		if err == nil || ctx.Err() != nil || !errors.Is(err, ErrWorkerExited) {
			if err == nil {
				p.mu.Lock()
				p.consecutive = 0
				p.mu.Unlock()
			}
			return result, err
		}
		last = err
	}
	return nil, fmt.Errorf("a batch crashed its worker %d times: %w", p.cfg.Retries+1, last)
}

// worker returns slot index's worker, restarting it after a backoff when it
// has died.
func (p *Pool) worker(ctx context.Context, index int) (*Worker, error) {
	slot := p.slots[index]
	slot.mu.Lock()
	defer slot.mu.Unlock()
	for {
		if slot.worker != nil && slot.worker.Err() == nil {
			return slot.worker, nil
		}
		cause := errors.New("worker never started")
		if slot.worker != nil {
			cause = slot.worker.Err()
		}
		p.mu.Lock()
		closed, consecutive := p.closed, p.consecutive
		p.mu.Unlock()
		if closed {
			return nil, errors.New("labeler pool is closed")
		}
		if consecutive >= p.cfg.MaxRestarts {
			return nil, fmt.Errorf("%w: %d restarts in a row; last: %v", ErrWorkersUnavailable, consecutive, cause)
		}
		timer := time.NewTimer(p.cfg.Backoff(slot.failures))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
		p.mu.Lock()
		closed = p.closed
		if !closed {
			p.restarts++
			p.consecutive++
		}
		p.mu.Unlock()
		if closed {
			return nil, errors.New("labeler pool is closed")
		}
		if p.cfg.OnRestart != nil {
			p.cfg.OnRestart(index, cause)
		}
		slot.failures++
		if slot.worker != nil {
			slot.worker.Kill()
		}
		worker, err := StartWorker(ctx, p.cfg.Worker)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slot.worker = nil
			continue
		}
		slot.worker = worker
	}
}

// alive reports a pool that is neither closed nor past its restart budget.
func (p *Pool) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && p.consecutive < p.cfg.MaxRestarts
}

// Kill stops every worker at once: for a stop, where results in flight are
// not wanted and a release must not wait for a long batch.
func (p *Pool) Kill() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	for _, slot := range p.slots {
		slot.mu.Lock()
		if slot.worker != nil {
			slot.worker.Kill()
		}
		slot.mu.Unlock()
	}
}

// Close stops every worker.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, slot := range p.slots {
		slot.mu.Lock()
		worker := slot.worker
		slot.mu.Unlock()
		if worker != nil {
			wg.Add(1)
			go func() { defer wg.Done(); _ = worker.Close() }()
		}
	}
	wg.Wait()
}
