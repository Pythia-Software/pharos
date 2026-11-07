package archive

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"sync"
	"time"
)

// The runner (docs/work-execution-plan.md §3, §6.2, §6.6–6.9): select the
// highest-priority eligible request, find its first ready batch in
// dependency order, acquire its allocation, run it to its safe boundary
// through the registry, apply what it committed, and reconsider. All of its
// state is memory: restart rediscovers work from the domains.
//
// One batch per kind runs at a time, so two revisions of a unit never compute
// concurrently and a kind's Next is never asked while its batch is in flight;
// different kinds run concurrently within the allowance. The runner starts no
// goroutines: batches start through workHooks.Start (the registry) and Run is
// the parent's daemon.

var (
	// workRetryDelays are the waits after the first and second failed
	// attempts of a unit revision; the third failure exhausts it.
	workRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second}
	// workFailureLimit bounds remembered failures across kinds. A kind that
	// would exceed it is held rather than forgetting failures.
	workFailureLimit = 256
	// workDecisionLimit bounds the recent decision trace.
	workDecisionLimit = 64
	// workEndedRequestLimit bounds finished-request summaries kept for
	// inspection; they hold counts, not membership.
	workEndedRequestLimit = 16
	// workQueuedRequestLimit bounds request intent waiting for admission.
	workQueuedRequestLimit = 16
	// workLiveRequestLimit bounds every request not yet ended, queued or
	// admitted, so inspection and per-commit bookkeeping stay bounded.
	workLiveRequestLimit = 64
	// workSelectionLimit bounds the units in one batch.
	workSelectionLimit = 4096
	// workHistoryReserve is the live-lane time after which one backfill
	// batch goes first (§6.2), outside an explicit recent request.
	workHistoryReserve = 60 * time.Second
	// workReconcileInterval reconsiders blockers that have no event.
	workReconcileInterval = 30 * time.Second
	// workSlowSelect flags a Next that is not a cheap lookup.
	workSlowSelect = 50 * time.Millisecond
)

// workBlockedReserved is admitLocked's refusal when only another kind's
// reserved capacity wait stood in the way; requests see workBlockedCapacity.
const workBlockedReserved = "reserved"

var (
	errWorkStopped          = errors.New("the task was stopped")
	errWorkNoDemand         = errors.New("no remaining request needs this batch")
	errWorkRequestCancelled = errors.New("the request was cancelled")
	errWorkNoProgress       = errors.New("the batch committed nothing and reported no blocker")
	errWorkPanicked         = errors.New("the batch panicked")
	errWorkQueueFull        = errors.New("too many work requests are waiting for admission")
	errWorkKeyConflict      = errors.New("a live work request with this key has a different intent")
)

// workHooks connect the runner to the operation registry.
type workHooks struct {
	// Start runs an admitted job on a registry-owned goroutine: it calls run
	// exactly once with a context derived from the service lifetime (never
	// a requester's), or returns an error and never calls it.
	Start func(job workJob, run func(context.Context)) error
	// ExecutionEnded closes the executor-owned root for a kind's execution.
	ExecutionEnded func(workExecutionEnd)
	// RequestEnded reports a request's terminal state. A request whose
	// membership read is still running reports after that read's
	// BatchEnded.
	RequestEnded func(workRequestFacts)
	// BatchEnded (optional) reports how a job whose run was called ended,
	// after its work drained and its grant was released. A kind's
	// ExecutionEnded comes after the BatchEnded of every batch in it.
	BatchEnded func(workBatchEnd)
	// DemandChanged (optional) reports a running batch's new demanders when
	// a request joins or leaves it. It may arrive after that batch's
	// BatchEnded, never after its ExecutionEnded; ignore it for an ended
	// batch.
	DemandChanged func(workDemand)
	// RequestChanged (optional) reports a live request's new state
	// (running or blocked) or blocker. Ignore it once that request's
	// RequestEnded was delivered.
	RequestChanged func(workRequestFacts)
}

// Every hook runs outside the runner's lock, so it may call back into the
// runner. Start is called once per job; the others at most once per event.

// workBatchEnd is a job's outcome: succeeded, blocked, failed, cancelled
// (stopped, no remaining demand or the request was cancelled) or
// interrupted (the service context ended).
type workBatchEnd struct {
	Job    workJob `json:"job"`
	State  string  `json:"state"`
	Reason string  `json:"reason,omitempty"`
	Err    error   `json:"-"`
}

type workDemand struct {
	Kind      string   `json:"kind"`
	Execution uint64   `json:"execution"`
	Batch     uint64   `json:"batch"`
	Demanders []uint64 `json:"demanders"`
}

// workJob identifies admitted work for the registry. Request is set for a
// membership build; otherwise Execution is the kind's executor-owned root
// and Batch its child.
// workMembershipKind is the operation kind of a request's membership read:
// the request's own chain, not one of the analyses it asks for.
const workMembershipKind = "analysis.chain"

type workJob struct {
	Kind      string       `json:"kind"`
	Execution uint64       `json:"execution,omitempty"`
	Batch     uint64       `json:"batch"`
	Request   uint64       `json:"request,omitempty"`
	Phase     string       `json:"phase,omitempty"`
	Units     int          `json:"units"`
	CPUs      int          `json:"cpus"`
	Coarse    bool         `json:"coarse,omitempty"`
	Priority  workPriority `json:"-"`
	Demanders []uint64     `json:"demanders,omitempty"`
}

type workExecutionEnd struct {
	Kind      string `json:"kind"`
	Execution uint64 `json:"execution"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

// workRequestSpec is a request: bring these analyses current for a
// population. Build reads a closed membership under admission; nil is
// whole-library demand (automatic catch-up, backfill).
//
// Key is the request's identity: the population it reads plus its intent.
// A non-empty Key returns the live request with that Key instead of adding
// one. The same Key must mean the same set of Kinds, Priority, Explicit and
// library-or-finite Build (sameIntent); a Key reused with a different
// intent is refused with errWorkKeyConflict rather than silently merged.
// Callers derive keys from what they request, so a different window or
// class is a different Key.
type workRequestSpec struct {
	Key      string
	Scope    string // diagnostic: recent, backfill, selection or automatic
	Kinds    []string
	Priority workPriority
	Explicit bool
	Trigger  string
	Build    func(context.Context, *workInventoryBuilder) error
}

type workRunner struct {
	ctx       context.Context
	clock     workClock
	hooks     workHooks
	authority *workAuthority
	kinds     []*workKindState
	byKind    map[string]*workKindState
	wake      chan struct{}

	mu             sync.Mutex
	requests       []*workRequest // queued and live, in arrival order
	ended          []workRequestFacts
	failureCount   int
	decisions      []workDecision
	decisionsTotal int64
	events         []func()
	nextID         uint64
	explicit       int
	paused         bool
	closed         bool
	reconsider     bool
	liveRun        time.Duration
	pumping, again bool
	// capacityWait is the minimum allocation a kind waited for in the last
	// pass (a capacity wait ends the pass, so there is at most one). Other
	// kinds of requests ranked at or below the waiter's must fit beside it,
	// so a fixed envelope is not starved by smaller grants that keep
	// arriving first; a higher-ranked request is not held back by it.
	capacityWait workCapacityWait
}

type workCapacityWait struct {
	kind       *workKindState
	cpus, rank int
}

type workKindState struct {
	def       workDefinition
	index     int
	running   *workBatch
	held      bool
	full      bool // the failure table had no room
	wait      workBlocker
	waitUntil time.Time
	failures  map[string]*workFailure
	execution uint64
	lastState string
	// draining counts queued hook calls for this kind; its execution ends
	// only after they are delivered.
	draining int
	// Counters for inspection; domains report nothing extra.
	batches, committed, work int64
	active, selectTime       time.Duration
	slowSelects              int64
}

type workBatch struct {
	id        uint64
	job       workJob
	kind      *workKindState
	request   *workRequest // a membership build
	sel       workSelection
	cpus      int
	priority  workPriority
	demanders []uint64
	started   time.Time
	parent    context.Context
	ctx       context.Context
	cancel    context.CancelCauseFunc
	cause     error
}

type workRequest struct {
	id          uint64
	spec        workRequestSpec
	state       string
	reason      string
	detail      string
	blocker     workBlocker
	requestedAt time.Time
	tasks       []*workRequestTask
	expanded    []string
	inv         *workInventory
	build       *workBatch
	reserved    int64 // process memory held for this request
	sealed      workBits
	failed      workBits
	exhausted   workBits
	replaced    workBits
	targets     map[int]string    // replacement revisions by ordinal
	mask        workBits          // eligibility scratch, refilled only by a pass
	endFacts    *workRequestFacts // RequestEnded waiting for the membership read
	// changes counts retargets. An empty Next answered for a snapshot whose
	// count has since moved covers nothing: it may have seen a revision the
	// request no longer targets.
	changes, maskChanges uint64
	reported             workBlocker // state and blocker last sent to RequestChanged
	reportedState        string
	nSealed              int
	nFailed              int
	nExhausted           int
	libraryFail          int
	admitted             bool
}

type workRequestTask struct {
	kind     *workKindState
	prereqs  []*workRequestTask // direct prerequisites in this request
	covered  workBits
	done     bool
	failed   string // the failed kind: itself or a prerequisite
	blocker  workBlocker
	reported workBlocker
}

// workFailure is one remembered failure of a unit revision (or of a
// unit-less phase, whose ID is "phase:<name>").
type workFailure struct {
	Kind      string     `json:"kind"`
	Unit      workMember `json:"unit"`
	Attempts  int        `json:"attempts"`
	NextAt    time.Time  `json:"next_at,omitzero"`
	Error     string     `json:"error"`
	Exhausted bool       `json:"exhausted,omitempty"`
	phase     bool
}

type workDecision struct {
	At      time.Time `json:"at"`
	Event   string    `json:"event"`
	Kind    string    `json:"kind,omitempty"`
	Request uint64    `json:"request,omitempty"`
	Batch   uint64    `json:"batch,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

type workBits []uint64

func newWorkBits(n int) workBits        { return make(workBits, (n+63)/64) }
func (b workBits) has(i int) bool       { return b[i/64]&(1<<(i%64)) != 0 }
func (b workBits) set(i int)            { b[i/64] |= 1 << (i % 64) }
func (b workBits) unset(i int)          { b[i/64] &^= 1 << (i % 64) }
func workBitsBytes(n, arrays int) int64 { return int64((n+63)/64*8) * int64(arrays) }

// next is the first set bit at or after i, or -1.
func (b workBits) next(i int) int {
	for w := i / 64; w < len(b); w++ {
		word := b[w]
		if w == i/64 {
			word &^= 1<<(i%64) - 1
		}
		if word != 0 {
			return w*64 + bits.TrailingZeros64(word)
		}
	}
	return -1
}

func (b workBits) count() int {
	n := 0
	for _, word := range b {
		n += bits.OnesCount64(word)
	}
	return n
}

// workSparseEntryCost charges one replacement-revision map entry.
const workSparseEntryCost = 64

func newWorkRunner(ctx context.Context, clock workClock, authority *workAuthority, hooks workHooks, defs []workDefinition) (*workRunner, error) {
	if hooks.Start == nil {
		return nil, errors.New("the work runner needs a registry Start hook")
	}
	ordered, err := orderWorkDefinitions(defs)
	if err != nil {
		return nil, err
	}
	r := &workRunner{ctx: ctx, clock: clock, hooks: hooks, authority: authority, byKind: map[string]*workKindState{}, wake: make(chan struct{}, 1)}
	for i, def := range ordered {
		ks := &workKindState{def: def, index: i, failures: map[string]*workFailure{}}
		r.kinds = append(r.kinds, ks)
		r.byKind[def.Kind] = ks
	}
	authority.notify = r.signal
	return r, nil
}

func (r *workRunner) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Wake reconsiders every blocker (a lock owner finished, a setting, asset or
// power changed, new dirty work) and pumps.
func (r *workRunner) Wake() {
	r.mu.Lock()
	r.reconsider = true
	r.mu.Unlock()
	r.signal()
}

// unlock releases the lock and then runs queued hook calls, so a hook may
// call back into the runner.
func (r *workRunner) unlock() {
	events := r.events
	r.events = nil
	r.mu.Unlock()
	for _, event := range events {
		event()
	}
}

func (r *workRunner) decideLocked(event, kind string, request, batch uint64, reason, detail string) {
	d := workDecision{At: r.clock.Now(), Event: event, Kind: kind, Request: request, Batch: batch, Reason: reason, Detail: detail}
	if len(r.decisions) < workDecisionLimit {
		r.decisions = append(r.decisions, d)
	} else {
		r.decisions[r.decisionsTotal%int64(len(r.decisions))] = d
	}
	r.decisionsTotal++
}

// Submit records a request's intent. It is admitted, in priority order, when
// the explicit-request and memory limits allow.
func (r *workRunner) Submit(spec workRequestSpec) (uint64, error) {
	if spec.Priority < workPrioritySafety || spec.Priority > workPriorityOptional {
		return 0, fmt.Errorf("work request priority %d is not a priority class", spec.Priority)
	}
	if len(spec.Kinds) == 0 {
		return 0, errors.New("a work request names at least one task kind")
	}
	needed := make([]bool, len(r.kinds))
	dependencies := make([][]string, len(r.kinds))
	for index, kind := range r.kinds {
		dependencies[index] = kind.def.Dependencies
		if kind.def.DependenciesForRequest != nil {
			dependencies[index] = slices.Clone(kind.def.DependenciesForRequest())
			for _, dependency := range dependencies[index] {
				if !slices.Contains(kind.def.Dependencies, dependency) {
					return 0, fmt.Errorf("%s selected an undeclared prerequisite %s", kind.def.Kind, dependency)
				}
			}
		}
	}
	var mark func(kind string)
	mark = func(kind string) {
		ks := r.byKind[kind]
		if needed[ks.index] {
			return
		}
		needed[ks.index] = true
		for _, dependency := range dependencies[ks.index] {
			mark(dependency)
		}
	}
	for _, kind := range spec.Kinds {
		if r.byKind[kind] == nil {
			return 0, fmt.Errorf("%s is not in the work task table", kind)
		}
		mark(kind)
	}
	spec.Kinds = slices.Clone(spec.Kinds)
	r.mu.Lock()
	defer r.unlock()
	if r.closed || r.ctx.Err() != nil {
		return 0, errServiceStopping
	}
	queued := 0
	for _, q := range r.requests {
		if spec.Key != "" && q.spec.Key == spec.Key {
			if !q.spec.sameIntent(spec) {
				return 0, fmt.Errorf("%w: %s", errWorkKeyConflict, spec.Key)
			}
			return q.id, nil
		}
		if !q.admitted {
			queued++
		}
	}
	if queued >= workQueuedRequestLimit || len(r.requests) >= workLiveRequestLimit {
		return 0, errWorkQueueFull
	}
	r.nextID++
	q := &workRequest{id: r.nextID, spec: spec, state: opQueued, requestedAt: r.clock.Now()}
	for i, ks := range r.kinds {
		if needed[i] {
			t := &workRequestTask{kind: ks}
			for _, dependency := range dependencies[ks.index] {
				// Dependencies precede their consumers in r.kinds.
				t.prereqs = append(t.prereqs, q.task(r.byKind[dependency]))
			}
			q.tasks = append(q.tasks, t)
			if !slices.Contains(spec.Kinds, ks.def.Kind) {
				q.expanded = append(q.expanded, ks.def.Kind)
			}
		}
	}
	r.requests = append(r.requests, q)
	r.decideLocked("requested", "", q.id, 0, spec.Priority.String(), spec.Trigger)
	r.signal()
	return q.id, nil
}

// sameIntent reports whether two specs describe the same request apart
// from Trigger (see workRequestSpec.Key).
func (s workRequestSpec) sameIntent(o workRequestSpec) bool {
	return s.Scope == o.Scope && s.Priority == o.Priority && s.Explicit == o.Explicit && (s.Build == nil) == (o.Build == nil) &&
		slices.Equal(slices.Sorted(slices.Values(s.Kinds)), slices.Sorted(slices.Values(o.Kinds)))
}

// Pump runs one scheduling pass and returns how many jobs it started. Only
// one pass runs at a time; a call during a pass makes it repeat.
func (r *workRunner) Pump() int {
	r.mu.Lock()
	if r.pumping {
		r.again = true
		r.mu.Unlock()
		return 0
	}
	r.pumping = true
	started := 0
	for !r.closed && r.ctx.Err() == nil {
		r.again = false
		started += r.passLocked()
		if !r.again {
			break
		}
	}
	r.pumping = false
	r.unlock()
	return started
}

func (r *workRunner) rank(q *workRequest) int {
	rank := int(q.spec.Priority) * 2
	if q.spec.Priority == workPriorityBackfill && r.liveRun >= workHistoryReserve {
		rank = int(workPriorityLive)*2 - 1
	}
	return rank
}

func (r *workRunner) passLocked() int {
	now := r.clock.Now()
	if r.reconsider {
		r.reconsider = false
		for _, ks := range r.kinds {
			ks.wait = workBlocker{}
		}
	}
	// A foreground wait for capacity keeps its claim on the authority
	// across passes; any other pass ends it.
	claimed := false
	// A kind refused only because of the last pass's capacity wait: when
	// that wait is not renewed this pass, pass again without the reserve.
	reserved := false
	defer func() {
		if !claimed {
			r.authority.dropClaim()
		}
		if reserved && r.capacityWait.kind == nil {
			r.again = true
		}
	}()
	order := slices.Clone(r.requests)
	slices.SortStableFunc(order, func(a, b *workRequest) int { return r.rank(a) - r.rank(b) })
	wait := r.capacityWait
	r.capacityWait = workCapacityWait{}
	if wait.cpus > r.authority.cap() {
		wait = workCapacityWait{} // the cap shrank below it: it reports exceeds-cap now
	}
	// reserveFor is what a batch of ks for q must leave free.
	reserveFor := func(q *workRequest, ks *workKindState) int {
		if wait.kind == nil || wait.kind == ks || r.rank(q) < wait.rank {
			return 0
		}
		return wait.cpus
	}
	started := 0
	foregroundBusy := false
	for _, q := range order {
		if opTerminal(q.state) {
			continue
		}
		foreground := q.spec.Priority <= workPriorityForeground
		switch {
		case r.paused && q.spec.Priority != workPrioritySafety && !q.spec.Explicit:
			r.blockRequestLocked(q, workBlocker{Reason: workBlockedPaused})
			continue
		case q.spec.Priority >= workPriorityBackfill && foregroundBusy:
			r.blockRequestLocked(q, workBlocker{Reason: workBlockedRecentFirst, Detail: "an explicit recent request goes first"})
			continue
		case q.build != nil:
			foregroundBusy = foregroundBusy || foreground
			continue
		case !q.admitted:
			switch r.admitLocked(q, now, reserveFor(q, nil)) {
			case workBlockedCapacity:
				claimed = claimed || foreground
				return started
			case workBlockedReserved:
				reserved = true
			}
			if q.build != nil {
				started++
				foregroundBusy = foregroundBusy || foreground
			}
			if !q.admitted || q.build != nil {
				continue
			}
		}
		progressing, capacity := false, false
		for _, t := range q.tasks {
			if opTerminal(q.state) || capacity {
				break
			}
			if t.done || t.failed != "" {
				continue
			}
			ks := t.kind
			if ks.running != nil {
				progressing = progressing || slices.Contains(ks.running.demanders, q.id)
				continue
			}
			if blocker := r.kindBlockerLocked(ks, now); blocker.Reason != "" {
				t.blocker = blocker
				continue
			}
			if blocker := q.gateLocked(t); blocker.Reason != "" {
				t.blocker = blocker
				continue
			}
			pop := r.populationLocked(q, t, now)
			r.mu.Unlock()
			began := r.clock.Now()
			sel, err := ks.def.Task.Next(r.ctx, pop)
			elapsed := r.clock.Now().Sub(began)
			r.mu.Lock()
			ks.selectTime += elapsed
			if elapsed > workSlowSelect {
				ks.slowSelects++
			}
			if opTerminal(q.state) || ks.running != nil || ks.held {
				continue
			}
			if err != nil {
				t.blocker = workBlocker{Reason: workBlockedBackoff, Detail: err.Error()}
				r.failLocked(ks, workMember{ID: "phase:select"}, true, err, false, now)
				continue
			}
			r.clearFailureLocked(ks, "phase:select")
			if sel.Blocker.Reason != "" {
				t.blocker = sel.Blocker
				continue
			}
			if !sel.ready() {
				r.coverTaskLocked(q, t, now)
				continue
			}
			if blocker := r.validateLocked(q, t, sel, pop, now); blocker.Reason != "" {
				t.blocker = blocker
				continue
			}
			least, most := sel.grant()
			if cap := r.authority.cap(); least > cap {
				t.blocker = workBlocker{Reason: workBlockedExceedsCap, Detail: fmt.Sprintf("needs %d CPUs; the allowance is %d", least, cap)}
				continue
			}
			granted, ok, kept := r.authority.tryCPUs(least, most, reserveFor(q, ks), foreground)
			if kept {
				// Not a capacity wait: the pass goes on to the waiting kind.
				t.blocker = workBlocker{Reason: workBlockedCapacity, DependsOn: wait.kind.def.Kind,
					Detail: fmt.Sprintf("%d CPUs are kept for %s, which waits for them", wait.cpus, wait.kind.def.Kind)}
				reserved = true
				continue
			}
			if !ok {
				t.blocker = workBlocker{Reason: workBlockedCapacity, Detail: fmt.Sprintf("waits for %d CPUs", least)}
				r.capacityWait = workCapacityWait{kind: ks, cpus: least, rank: r.rank(q)}
				capacity = true
				claimed = claimed || foreground
				break
			}
			if wait.kind == ks {
				wait = workCapacityWait{}
			}
			t.blocker = workBlocker{}
			r.launchLocked(r.startLocked(ks, q, sel, granted, now))
			started++
			progressing = true
		}
		r.updateRequestLocked(q)
		foregroundBusy = foregroundBusy || (foreground && (progressing || capacity))
		if capacity {
			// Strict priority: lower demand does not take the capacity this
			// batch waits for.
			return started
		}
	}
	return started
}

func (r *workRunner) blockRequestLocked(q *workRequest, blocker workBlocker) {
	if q.blocker != blocker {
		r.decideLocked("blocked", "", q.id, 0, blocker.Reason, blocker.Detail)
	}
	q.blocker = blocker
	if q.state == opRunning {
		q.state = opBlocked
	}
	r.requestChangedLocked(q)
}

// requestChangedLocked queues RequestChanged when a live request's state
// or blocker differs from what it last reported.
func (r *workRunner) requestChangedLocked(q *workRequest) {
	if r.hooks.RequestChanged == nil || opTerminal(q.state) || (q.state == q.reportedState && q.blocker == q.reported) {
		return
	}
	q.reportedState, q.reported = q.state, q.blocker
	facts := r.requestFactsLocked(q)
	r.events = append(r.events, func() { r.hooks.RequestChanged(facts) })
}

// admitLocked admits a queued request: the explicit-request limit, a full
// request-memory reservation, and one CPU for its membership read. It
// returns "" when admitted or the blocker reason.
func (r *workRunner) admitLocked(q *workRequest, now time.Time, reserve int) string {
	if q.spec.Explicit && r.explicit >= workExplicitRequestLimit {
		r.blockRequestLocked(q, workBlocker{Reason: workBlockedRequestLimit, Detail: fmt.Sprintf("%d explicit requests are admitted", r.explicit)})
		return workBlockedRequestLimit
	}
	if q.spec.Build == nil {
		r.markAdmittedLocked(q, now)
		return ""
	}
	// Lower classes leave one request's worth of memory for an explicit
	// recent request, so earlier history cannot hold it out.
	foreground := q.spec.Priority <= workPriorityForeground
	headroom := workRequestMemoryLimit
	if foreground {
		headroom = 0
	}
	if !r.authority.reserveMemory(workRequestMemoryLimit, headroom) {
		r.blockRequestLocked(q, workBlocker{Reason: workBlockedRequestMemory, Detail: "request bookkeeping is at this process's memory limit"})
		return workBlockedRequestMemory
	}
	granted, ok, kept := r.authority.tryCPUs(1, 1, reserve, foreground)
	if !ok {
		// A rollback, not a release: signalling would wake Run into the
		// same refusal.
		r.authority.returnMemory(workRequestMemoryLimit)
		r.blockRequestLocked(q, workBlocker{Reason: workBlockedCapacity, Detail: "the membership read waits for a CPU"})
		if kept {
			return workBlockedReserved
		}
		return workBlockedCapacity
	}
	q.reserved = workRequestMemoryLimit
	r.markAdmittedLocked(q, now)
	r.nextID++
	b := &workBatch{id: r.nextID, request: q, cpus: granted, priority: q.spec.Priority, demanders: []uint64{q.id}, started: now,
		sel: workSelection{Phase: "membership"}}
	q.build = b
	r.launchLocked(b)
	return ""
}

func (r *workRunner) markAdmittedLocked(q *workRequest, now time.Time) {
	q.admitted, q.state, q.blocker = true, opRunning, workBlocker{}
	if q.spec.Explicit {
		r.explicit++
	}
	if q.spec.Build == nil {
		r.joinRunningLocked(q)
	}
	r.decideLocked("admitted", "", q.id, 0, "", "")
	r.requestChangedLocked(q)
}

// joinRunningLocked adds a newly admitted request to the demand of batches
// already running for its units. A batch already cancelled is ending; the
// request selects that work again itself.
func (r *workRunner) joinRunningLocked(q *workRequest) {
	for _, t := range q.tasks {
		if b := t.kind.running; b != nil && b.cause == nil && r.demandsLocked(q, t.kind, b.sel) && !slices.Contains(b.demanders, q.id) {
			b.demanders = append(b.demanders, q.id)
			r.demandChangedLocked(b)
		}
	}
}

// demandsLocked reports whether q needs a batch of ks over sel.
func (r *workRunner) demandsLocked(q *workRequest, ks *workKindState, sel workSelection) bool {
	if opTerminal(q.state) || !q.admitted || q.build != nil {
		return false
	}
	t := q.task(ks)
	if t == nil || t.done || t.failed != "" {
		return false
	}
	if q.inv == nil || ks.def.Coarse || len(sel.Units) == 0 {
		return true
	}
	return slices.ContainsFunc(sel.Units, func(m workMember) bool { _, ok := q.inv.index(m.ID); return ok })
}

func (q *workRequest) task(ks *workKindState) *workRequestTask {
	for _, t := range q.tasks {
		if t.kind == ks {
			return t
		}
	}
	return nil
}

func (q *workRequest) target(i int) string {
	if revision, ok := q.targets[i]; ok {
		return revision
	}
	return q.inv.members[i].Revision
}

func (q *workRequest) settled(i int) bool {
	return q.sealed.has(i) || q.failed.has(i) || q.exhausted.has(i)
}

// kindBlockerLocked is why a kind cannot be asked for a batch now.
func (r *workRunner) kindBlockerLocked(ks *workKindState, now time.Time) workBlocker {
	switch {
	case ks.held:
		return workBlocker{Reason: workBlockedHeld, Detail: "stopped until Resume or a service restart"}
	case ks.full:
		return workBlocker{Reason: workBlockedFailuresFull, Detail: fmt.Sprintf("%d failures are remembered; Retry clears them", workFailureLimit)}
	}
	if ks.wait.Reason != "" {
		if now.Before(ks.waitUntil) {
			return ks.wait
		}
		ks.wait = workBlocker{}
	}
	for _, f := range ks.failures {
		if f.phase && !f.Exhausted && now.Before(f.NextAt) {
			return workBlocker{Reason: workBlockedBackoff, Detail: f.Error}
		}
	}
	return workBlocker{}
}

// gateLocked is the dependency wait that applies before Next is asked. A
// coarse task, or a task whose prerequisite is coarse, needs that
// prerequisite done for the whole request: coarse coverage is global. A
// unit-scoped prerequisite gates each unit instead (eligibleLocked), so a
// consumer starts on a covered unit while its producer works on the rest.
func (q *workRequest) gateLocked(t *workRequestTask) workBlocker {
	for _, p := range t.prereqs {
		if !p.done && (t.kind.def.Coarse || p.kind.def.Coarse) {
			return workBlocker{Reason: workBlockedDependency, DependsOn: p.kind.def.Kind, Detail: "waits for the whole prerequisite"}
		}
	}
	return workBlocker{}
}

// coveredByPrereqs reports whether every prerequisite covered member i at
// its current target. Replacement clears every task's coverage, so a set
// bit always refers to the target revision.
func (t *workRequestTask) coveredByPrereqs(i int) bool {
	for _, p := range t.prereqs {
		if !p.covered.has(i) {
			return false
		}
	}
	return true
}

// firstUncovered names a prerequisite that has not covered member i.
func (t *workRequestTask) firstUncovered(i int) string {
	for _, p := range t.prereqs {
		if !p.covered.has(i) {
			return p.kind.def.Kind
		}
	}
	return ""
}

// eligibleWord is word w of the members t can work on now before failures
// are applied (ready), and of the unsettled members t has not covered
// (open); open &^ ready are waiting for a prerequisite.
func (q *workRequest) eligibleWord(t *workRequestTask, w int) (ready, open uint64) {
	open = ^(q.sealed[w] | q.failed[w] | q.exhausted[w] | t.covered[w])
	if rest := len(q.inv.members) - w*64; rest < 64 {
		open &= 1<<rest - 1
	}
	ready = open
	for _, p := range t.prereqs {
		ready &= p.covered[w]
	}
	return ready, open
}

// heldBy reports the member a failure of t holds back now (backoff or
// exhausted at its target revision).
func (q *workRequest) heldBy(f *workFailure, now time.Time) (int, bool) {
	if f.phase || !(f.Exhausted || now.Before(f.NextAt)) {
		return 0, false
	}
	i, ok := q.inv.index(f.Unit.ID)
	return i, ok && q.target(i) == f.Unit.Revision
}

// eligibleLocked fills q.mask with the members t can work on now (see
// workPopulation.eligible) and counts them and the members waiting for a
// prerequisite.
func (r *workRunner) eligibleLocked(q *workRequest, t *workRequestTask, now time.Time) (pending, waiting int) {
	for w := range q.mask {
		ready, open := q.eligibleWord(t, w)
		q.mask[w] = ready
		pending += bits.OnesCount64(ready)
		waiting += bits.OnesCount64(open &^ ready)
	}
	for _, f := range t.kind.failures {
		if i, ok := q.heldBy(f, now); ok && q.mask.has(i) {
			q.mask.unset(i)
			pending--
		}
	}
	return pending, waiting
}

// populationLocked is Next's snapshot. A finite request's eligibility is
// written to its scratch mask, which only a pass rewrites; Next reads it
// unlocked, and passes run one at a time.
func (r *workRunner) populationLocked(q *workRequest, t *workRequestTask, now time.Time) *workPopulation {
	ks := t.kind
	pop := &workPopulation{}
	for _, f := range ks.failures {
		if !f.phase {
			if pop.failures == nil {
				pop.failures = map[string]bool{}
			}
			pop.failures[f.Unit.ID] = true
		}
		if !f.phase && (f.Exhausted || now.Before(f.NextAt)) {
			if pop.excluded == nil {
				pop.excluded = map[string]string{}
			}
			pop.excluded[f.Unit.ID] = f.Unit.Revision
		}
	}
	if q.inv != nil {
		q.maskChanges = q.changes
	}
	if ks.def.Coarse || q.inv == nil {
		return pop
	}
	pop.inventory, pop.eligible = q.inv, q.mask
	pop.pending, pop.waiting = r.eligibleLocked(q, t, now)
	if len(q.targets) > 0 {
		targets := q.targets
		pop.target = func(i int) (string, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if q.targets == nil {
				return "", false // the request ended
			}
			revision, ok := targets[i]
			return revision, ok
		}
	}
	return pop
}

// validateLocked refuses a selection that would hot-loop, is unbounded, or
// runs a request's unit ahead of its prerequisites. In a library selection,
// a unit at a new revision retires the failures of its older revisions.
func (r *workRunner) validateLocked(q *workRequest, t *workRequestTask, sel workSelection, pop *workPopulation, now time.Time) workBlocker {
	ks := t.kind
	if len(sel.Units) > workSelectionLimit {
		err := fmt.Errorf("the selection has %d units; the limit is %d", len(sel.Units), workSelectionLimit)
		r.failLocked(ks, workMember{ID: "phase:select"}, true, err, true, now)
		return workBlocker{Reason: workBlockedFailed, Detail: err.Error()}
	}
	for _, m := range sel.Units {
		if pop.Excluded(m) {
			// The adapter ignored the exclusion: hold the kind rather than
			// rerun a failing unit.
			ks.wait, ks.waitUntil = workBlocker{Reason: workBlockedBackoff, Detail: "the selection repeats " + m.ID + ", which is in retry backoff"}, now.Add(workReconcileInterval)
			return ks.wait
		}
	}
	if q.inv != nil && !ks.def.Coarse {
		for _, m := range sel.Units {
			if i, ok := q.inv.index(m.ID); ok && !q.settled(i) && !t.coveredByPrereqs(i) {
				return workBlocker{Reason: workBlockedDependency, DependsOn: t.firstUncovered(i), Detail: m.ID + " is not yet covered by its prerequisite"}
			}
		}
	}
	if pop.Library() && len(ks.failures) > 0 {
		// A library selection names the domain's current revision: older
		// revisions' failures no longer describe pending work.
		selected := make(map[string]string, len(sel.Units))
		for _, m := range sel.Units {
			selected[m.ID] = m.Revision
		}
		for key, f := range ks.failures {
			if revision, ok := selected[f.Unit.ID]; ok && !f.phase && revision != f.Unit.Revision {
				delete(ks.failures, key)
				r.failureCount--
			}
		}
	}
	return workBlocker{}
}

func (r *workRunner) startLocked(ks *workKindState, q *workRequest, sel workSelection, cpus int, now time.Time) *workBatch {
	r.nextID++
	b := &workBatch{id: r.nextID, kind: ks, sel: sel, cpus: cpus, priority: q.spec.Priority, started: now, demanders: []uint64{q.id}}
	for _, other := range r.requests {
		if other != q && r.demandsLocked(other, ks, sel) {
			b.demanders = append(b.demanders, other.id)
			b.priority = min(b.priority, other.spec.Priority)
		}
	}
	if b.priority == workPriorityBackfill {
		r.liveRun = 0
	}
	if ks.execution == 0 {
		r.nextID++
		ks.execution = r.nextID
		r.decideLocked("execution", ks.def.Kind, q.id, 0, "", "")
	}
	ks.running = b
	r.decideLocked("started", ks.def.Kind, q.id, b.id, sel.Phase, fmt.Sprintf("%d units, %d CPUs", len(sel.Units), cpus))
	return b
}

// launchLocked hands a job to the registry. The lock is released around
// Start so a hook may run the job before returning.
func (r *workRunner) launchLocked(b *workBatch) {
	job := workJob{Batch: b.id, Phase: b.sel.Phase, Units: len(b.sel.Units), CPUs: b.cpus, Coarse: b.sel.Coarse, Priority: b.priority, Demanders: slices.Clone(b.demanders)}
	var run func(context.Context)
	if b.request != nil {
		job.Kind, job.Request = workMembershipKind, b.request.id
		run = func(ctx context.Context) { r.runBuild(b, ctx) }
	} else {
		job.Kind, job.Execution = b.kind.def.Kind, b.kind.execution
		job.Coarse = job.Coarse || b.kind.def.Coarse
		run = func(ctx context.Context) { r.runBatch(b, ctx) }
	}
	b.job = job
	r.mu.Unlock()
	err := r.hooks.Start(job, run)
	r.mu.Lock()
	if err == nil {
		return
	}
	// Start refused (the service is stopping): run was never called.
	now := r.clock.Now()
	r.authority.releaseCPUs(b.cpus)
	if q := b.request; q != nil {
		q.build = nil
		if opTerminal(q.state) {
			// Ended while Start ran (Cancel or shutdown): run will never
			// return the builder's memory or the deferred RequestEnded.
			r.endBuildLocked(q)
			return
		}
		r.finishRequestLocked(q, opFailed, workMembershipUnavailable, err.Error())
		return
	}
	b.kind.running = nil
	b.kind.wait, b.kind.waitUntil = workBlocker{Reason: workBlockedExternal, Detail: err.Error()}, now.Add(workReconcileInterval)
	r.decideLocked("refused", b.kind.def.Kind, 0, b.id, workBlockedExternal, err.Error())
	r.closeExecutionsLocked()
}

// kindEventLocked queues a hook call about ks. Its execution cannot end
// until the call is delivered, so ExecutionEnded follows every BatchEnded
// and DemandChanged of the execution.
func (r *workRunner) kindEventLocked(ks *workKindState, event func()) {
	ks.draining++
	r.events = append(r.events, func() {
		defer func() {
			r.mu.Lock()
			ks.draining--
			r.closeExecutionsLocked()
			r.unlock()
		}()
		event()
	})
}

func (r *workRunner) demandChangedLocked(b *workBatch) {
	if r.hooks.DemandChanged == nil || b.kind == nil {
		return
	}
	demand := workDemand{Kind: b.kind.def.Kind, Execution: b.job.Execution, Batch: b.id, Demanders: slices.Clone(b.demanders)}
	r.kindEventLocked(b.kind, func() { r.hooks.DemandChanged(demand) })
}

func (r *workRunner) batchEndedLocked(b *workBatch, state, reason string, err error) {
	if r.hooks.BatchEnded == nil {
		return
	}
	end := workBatchEnd{Job: b.job, State: state, Reason: reason, Err: err}
	if b.kind == nil {
		r.events = append(r.events, func() { r.hooks.BatchEnded(end) })
		return
	}
	r.kindEventLocked(b.kind, func() { r.hooks.BatchEnded(end) })
}

// cancelReason names a runner cancellation for outcomes.
func cancelReason(cause error) string {
	switch {
	case errors.Is(cause, errWorkStopped):
		return "stopped"
	case errors.Is(cause, errWorkNoDemand):
		return "no-demand"
	case errors.Is(cause, errWorkRequestCancelled):
		return "request-cancelled"
	case errors.Is(cause, errServiceStopping):
		return "service-stopping"
	}
	return ""
}

func (r *workRunner) bindLocked(b *workBatch, parent context.Context) context.Context {
	ctx, cancel := context.WithCancelCause(parent)
	b.parent, b.ctx, b.cancel = parent, ctx, cancel
	if b.cause != nil {
		cancel(b.cause)
	}
	return ctx
}

func (r *workRunner) cancelBatchLocked(b *workBatch, cause error) {
	if b.cause == nil {
		b.cause = cause
		if b.cancel != nil {
			b.cancel(cause)
		}
	}
}

func (r *workRunner) runBuild(b *workBatch, ctx context.Context) {
	r.mu.Lock()
	ctx = r.bindLocked(b, ctx)
	q := b.request
	r.mu.Unlock()
	builder := &workInventoryBuilder{limit: workRequestMemoryLimit}
	var inv *workInventory
	err := errWorkPanicked
	defer func() {
		r.finishBuild(b, inv, err)
		b.cancel(nil)
	}()
	err = q.spec.Build(ctx, builder)
	if err == nil {
		inv, err = builder.freeze(r.clock.Now())
	}
}

func (r *workRunner) finishBuild(b *workBatch, inv *workInventory, err error) {
	r.mu.Lock()
	defer r.unlock()
	q := b.request
	q.build = nil
	r.authority.releaseCPUs(b.cpus)
	defer r.signal()
	stopping := r.closed || b.parent.Err() != nil
	if opTerminal(q.state) {
		// Ended while reading: the builder's memory and the deferred
		// RequestEnded are released only now that the read has returned.
		state, reason := opCancelled, cancelReason(b.cause)
		if errors.Is(b.cause, errServiceStopping) {
			state = opInterrupted
		}
		r.batchEndedLocked(b, state, reason, err)
		r.endBuildLocked(q)
		return
	}
	if err != nil && stopping {
		r.batchEndedLocked(b, opInterrupted, "service-stopping", err)
		r.finishRequestLocked(q, opInterrupted, "service-stopping", workShutdownDetail)
		return
	}
	if err != nil {
		r.batchEndedLocked(b, opFailed, workMembershipUnavailable, err)
		r.finishRequestLocked(q, opFailed, workMembershipUnavailable, err.Error())
		return
	}
	n := len(inv.members)
	// Coverage per task, sealed, failed, exhausted, replaced and the
	// eligibility scratch, charged before they are allocated.
	used := inv.bytes + workBitsBytes(n, len(q.tasks)+5)
	if used > workRequestMemoryLimit {
		err := &workInventoryTooLarge{Members: n, Bytes: used, Limit: workRequestMemoryLimit}
		r.batchEndedLocked(b, opFailed, workMembershipUnavailable, err)
		r.finishRequestLocked(q, opFailed, workMembershipUnavailable, err.Error())
		return
	}
	r.batchEndedLocked(b, opSucceeded, "", nil)
	q.inv = inv
	q.sealed, q.failed, q.exhausted, q.replaced, q.mask = newWorkBits(n), newWorkBits(n), newWorkBits(n), newWorkBits(n), newWorkBits(n)
	for _, t := range q.tasks {
		t.covered = newWorkBits(n)
	}
	r.authority.releaseMemory(q.reserved - used)
	q.reserved = used
	r.joinRunningLocked(q)
	r.decideLocked("frozen", "", q.id, 0, "", fmt.Sprintf("%d units, %d bytes", n, used))
	r.updateRequestLocked(q)
}

// endBuildLocked releases what an ended request kept for its membership
// read once that read can no longer touch it.
func (r *workRunner) endBuildLocked(q *workRequest) {
	r.authority.releaseMemory(q.reserved)
	q.reserved = 0
	if facts := q.endFacts; facts != nil && r.hooks.RequestEnded != nil {
		r.events = append(r.events, func() { r.hooks.RequestEnded(*facts) })
	}
	q.endFacts = nil
}

func (r *workRunner) runBatch(b *workBatch, ctx context.Context) {
	r.mu.Lock()
	ctx = r.bindLocked(b, ctx)
	r.mu.Unlock()
	var result workBatchResult
	err := errWorkPanicked
	defer func() {
		r.finishBatch(b, result, err)
		b.cancel(nil)
	}()
	result, err = b.kind.def.Task.RunBatch(ctx, b.sel, workAllocation{CPUs: b.cpus})
}

// finishBatch applies a joined batch: the grant is released only now, after
// RunBatch (and every worker it started) returned or panicked.
func (r *workRunner) finishBatch(b *workBatch, result workBatchResult, err error) {
	r.mu.Lock()
	defer r.unlock()
	now := r.clock.Now()
	ks := b.kind
	r.authority.releaseCPUs(b.cpus)
	ks.running = nil
	ks.batches++
	ks.work += result.Work
	elapsed := now.Sub(b.started)
	ks.active += elapsed
	if b.priority == workPriorityLive {
		r.liveRun += elapsed
	}
	for _, m := range result.Committed {
		r.commitLocked(ks, m)
	}
	for _, m := range result.Superseded {
		r.supersedeLocked(m)
	}
	progress := len(result.Committed) > 0 || len(result.Superseded) > 0 || result.Work > 0
	panicked := err == errWorkPanicked
	var classified *workError
	errors.As(err, &classified)
	state, reason := opSucceeded, ""
	stopping := r.closed || b.parent.Err() != nil || errors.Is(b.cause, errServiceStopping)
	switch {
	case stopping && !panicked && (err != nil || !progress):
		// The service is stopping: not the batch's failure.
		state, reason = opInterrupted, "service-stopping"
		r.decideLocked("interrupted", ks.def.Kind, 0, b.id, "", fmt.Sprint(context.Cause(b.ctx)))
	case b.cause != nil && !panicked && (err != nil || !progress):
		// Stopped, or nobody needs it, before it finished: not a failed
		// attempt; its domain stays dirty. A batch that finished anyway is
		// judged by its result below.
		state, reason = opCancelled, cancelReason(b.cause)
		r.decideLocked("cancelled", ks.def.Kind, 0, b.id, reason, b.cause.Error())
	case classified != nil && classified.blocker.Reason != "":
		state, reason = opBlocked, classified.blocker.Reason
		r.waitLocked(ks, classified.blocker, b, now)
	case err != nil:
		state, reason = opFailed, workBlockedFailed
		if panicked {
			reason = "panicked"
		}
		units := b.sel.Units
		if classified != nil && len(classified.units) > 0 {
			units = classified.units
		}
		permanent := classified != nil && classified.permanent
		if len(units) == 0 {
			r.failLocked(ks, workMember{ID: "phase:" + b.sel.Phase}, true, err, permanent, now)
		}
		for _, m := range units {
			r.failLocked(ks, m, false, err, permanent, now)
		}
	case result.Blocker.Reason != "":
		state, reason = opBlocked, result.Blocker.Reason
		r.waitLocked(ks, result.Blocker, b, now)
	case !progress:
		state, reason, err = opFailed, "no-progress", errWorkNoProgress
		r.failLocked(ks, workMember{ID: "phase:" + b.sel.Phase}, true, errWorkNoProgress, false, now)
	default:
		r.clearFailureLocked(ks, "phase:"+b.sel.Phase)
		r.decideLocked("finished", ks.def.Kind, 0, b.id, "", fmt.Sprintf("%d committed, %d superseded", len(result.Committed), len(result.Superseded)))
	}
	ks.lastState = state
	r.batchEndedLocked(b, state, reason, err)
	for _, q := range slices.Clone(r.requests) {
		r.updateRequestLocked(q)
	}
	r.closeExecutionsLocked()
	r.signal()
}

func (r *workRunner) waitLocked(ks *workKindState, blocker workBlocker, b *workBatch, now time.Time) {
	ks.wait, ks.waitUntil = blocker, now.Add(workReconcileInterval)
	r.decideLocked("blocked", ks.def.Kind, 0, b.id, blocker.Reason, blocker.Detail)
}

// commitLocked credits a committed unit to every live request targeting
// that revision. A different revision uses the unit's one replacement; a
// late commit at the original revision after a replacement is stale and
// ignored. A consumer's commit counts only when every prerequisite covers
// the unit at the same target, so producer and consumer output at
// different revisions never seal a unit.
func (r *workRunner) commitLocked(ks *workKindState, m workMember) {
	ks.committed++
	for key, f := range ks.failures {
		if f.Unit.ID == m.ID && !f.phase {
			delete(ks.failures, key)
			r.failureCount--
		}
	}
	if ks.def.Coarse {
		return
	}
	for _, q := range r.requests {
		t := q.task(ks)
		if q.inv == nil || t == nil || !q.admitted {
			continue
		}
		i, ok := q.inv.index(m.ID)
		if !ok || q.settled(i) || q.stale(i, m.Revision) {
			continue
		}
		if m.Revision != q.target(i) && !r.replaceLocked(q, i, m.Revision) {
			continue
		}
		if !t.coveredByPrereqs(i) {
			continue
		}
		t.covered.set(i)
		r.sealLocked(q, i)
	}
}

func (r *workRunner) supersedeLocked(m workMember) {
	for _, q := range r.requests {
		if q.inv == nil || !q.admitted {
			continue
		}
		if i, ok := q.inv.index(m.ID); ok && !q.settled(i) && !q.stale(i, m.Revision) && q.target(i) != m.Revision {
			r.replaceLocked(q, i, m.Revision)
		}
	}
}

// stale reports a revision the request already replaced: the frozen one,
// once member i was retargeted. Revisions are opaque, so this is the only
// older revision the runner can recognize.
func (q *workRequest) stale(i int, revision string) bool {
	return q.replaced.has(i) && revision == q.inv.members[i].Revision
}

// replaceLocked retargets a unit at a newer revision once (§6.7): coverage
// restarts for every task. A second change exhausts it.
func (r *workRunner) replaceLocked(q *workRequest, i int, revision string) bool {
	id := q.inv.members[i].ID
	cost := int64(len(revision)) + workSparseEntryCost
	if q.replaced.has(i) || q.reserved+cost > workRequestMemoryLimit || !r.authority.reserveMemory(cost, 0) {
		q.exhausted.set(i)
		q.nExhausted++
		r.decideLocked("exhausted", "", q.id, 0, workInputsKeepChanging, id)
		return false
	}
	q.reserved += cost
	if q.targets == nil {
		q.targets = map[int]string{}
	}
	q.targets[i] = strings.Clone(revision)
	q.replaced.set(i)
	q.changes++
	for _, t := range q.tasks {
		t.covered.unset(i)
		t.done = false
	}
	r.decideLocked("superseded", "", q.id, 0, "", id)
	return true
}

func (r *workRunner) sealLocked(q *workRequest, i int) {
	for _, t := range q.tasks {
		if !t.covered.has(i) {
			return
		}
	}
	q.sealed.set(i)
	q.nSealed++
}

// coverTaskLocked handles Next reporting nothing pending. Only what the
// task could have selected is current: a finite request covers members that
// were eligible in the snapshot Next saw (q.mask) and still are, never
// members still waiting for a prerequisite or held back by a failure, and a
// library request is done only once its prerequisites are. Members that
// became eligible while Next ran (a producer committed), or a retarget since
// the snapshot, make the pass ask again instead. Exhausted failures settle
// their members as failed; backoff failures past their delay are retired,
// since the domain no longer reports them pending.
func (r *workRunner) coverTaskLocked(q *workRequest, t *workRequestTask, now time.Time) {
	ks := t.kind
	if q.inv != nil && q.changes != q.maskChanges {
		r.again = true
		return
	}
	unitScoped := q.inv != nil && !ks.def.Coarse
	if !unitScoped {
		for _, p := range t.prereqs {
			if !p.done {
				t.blocker = workBlocker{Reason: workBlockedDependency, DependsOn: p.kind.def.Kind, Detail: "nothing is ready until the prerequisite is current"}
				return
			}
		}
	}
	var backoff workBlocker
	libraryFailed := 0
	for key, f := range ks.failures {
		if f.phase {
			continue
		}
		i := -1
		if unitScoped {
			var ok bool
			if i, ok = q.inv.index(f.Unit.ID); !ok || q.target(i) != f.Unit.Revision || q.settled(i) || !t.coveredByPrereqs(i) {
				continue
			}
		}
		switch {
		case !f.Exhausted && now.Before(f.NextAt):
			backoff = workBlocker{Reason: workBlockedBackoff, Detail: f.Unit.ID + ": " + f.Error}
		case !f.Exhausted:
			// Selectable, yet the domain reports nothing pending.
			delete(ks.failures, key)
			r.failureCount--
		case i < 0:
			libraryFailed++
		default:
			q.failed.set(i)
			q.nFailed++
		}
	}
	waiting, arrived := 0, 0
	switch {
	case unitScoped:
		// Members held by a failure are absent from the snapshot and from
		// what Next could select now; they are not new arrivals.
		for _, f := range ks.failures {
			if i, ok := q.heldBy(f, now); ok && !q.mask.has(i) && !q.settled(i) && !t.covered.has(i) && t.coveredByPrereqs(i) {
				arrived--
			}
		}
		for w := range q.mask {
			ready, open := q.eligibleWord(t, w)
			waiting += bits.OnesCount64(open &^ ready)
			arrived += bits.OnesCount64(ready &^ q.mask[w])
			for cover := ready & q.mask[w]; cover != 0; cover &= cover - 1 {
				i := w*64 + bits.TrailingZeros64(cover)
				t.covered.set(i)
				r.sealLocked(q, i)
			}
		}
	case q.inv != nil:
		// A coarse task after gateLocked: every prerequisite is done.
		for i := range q.inv.members {
			if !q.settled(i) {
				t.covered.set(i)
				r.sealLocked(q, i)
			}
		}
	}
	if arrived > 0 {
		r.again = true // Next has not seen these members yet
	}
	switch {
	case backoff.Reason != "":
		t.blocker = backoff
	case arrived > 0:
		t.blocker = workBlocker{}
	case waiting > 0:
		dependsOn := ""
		for i := range q.inv.members {
			if !q.settled(i) && !t.covered.has(i) && !t.coveredByPrereqs(i) {
				dependsOn = t.firstUncovered(i)
				break
			}
		}
		t.blocker = workBlocker{Reason: workBlockedDependency, DependsOn: dependsOn, Detail: fmt.Sprintf("%d units wait for a prerequisite", waiting)}
	default:
		// Counted once, when the task is done: a backoff that keeps the
		// task open must not count the same exhausted unit again.
		q.libraryFail += libraryFailed
		t.done, t.blocker = true, workBlocker{}
		r.decideLocked("covered", ks.def.Kind, q.id, 0, "", "")
	}
}

// failLocked records one failed attempt. Transient failures back off; the
// last attempt, or a permanent failure, exhausts the unit revision (or the
// phase) until its input changes or Retry.
func (r *workRunner) failLocked(ks *workKindState, m workMember, phase bool, err error, permanent bool, now time.Time) {
	key := m.ID + "\x00" + m.Revision
	f := ks.failures[key]
	if f == nil {
		if r.failureCount >= workFailureLimit {
			ks.full = true
			r.decideLocked("held", ks.def.Kind, 0, 0, workBlockedFailuresFull, err.Error())
			return
		}
		f = &workFailure{Kind: ks.def.Kind, Unit: m, phase: phase}
		ks.failures[key] = f
		r.failureCount++
	}
	f.Attempts++
	f.Error = err.Error()
	if len(f.Error) > 200 {
		f.Error = f.Error[:200]
	}
	if !permanent && f.Attempts <= len(workRetryDelays) {
		f.NextAt = now.Add(workRetryDelays[f.Attempts-1])
		r.decideLocked("retry", ks.def.Kind, 0, 0, workBlockedBackoff, m.ID+": "+f.Error)
		return
	}
	f.Exhausted, f.NextAt = true, time.Time{}
	r.decideLocked("failed", ks.def.Kind, 0, 0, workBlockedFailed, m.ID+": "+f.Error)
	for _, q := range r.requests {
		t := q.task(ks)
		if t == nil || !q.admitted || q.build != nil || t.done {
			continue
		}
		if phase {
			r.failTaskLocked(q, ks.def.Kind)
			continue
		}
		// A library request (and a coarse task) counts its failed units
		// when it is covered. A failure at another revision is not the
		// request's target.
		if q.inv == nil || ks.def.Coarse {
			continue
		}
		if i, ok := q.inv.index(m.ID); ok && !q.settled(i) && q.target(i) == m.Revision {
			q.failed.set(i)
			q.nFailed++
		}
	}
}

// failTaskLocked fails a task for a request, and every task in the request
// that depends on it (the skipped scope, §5.3).
func (r *workRunner) failTaskLocked(q *workRequest, kind string) {
	failed := map[string]bool{kind: true}
	for _, t := range q.tasks {
		if t.done || t.failed != "" {
			continue
		}
		if failed[t.kind.def.Kind] || slices.ContainsFunc(t.kind.def.Dependencies, func(d string) bool { return failed[d] }) {
			failed[t.kind.def.Kind] = true
			t.failed = kind
			reason := workBlockedFailed
			if t.kind.def.Kind != kind {
				reason = workDependencyFailed
			}
			t.blocker = workBlocker{Reason: reason, DependsOn: kind}
		}
	}
}

func (r *workRunner) clearFailureLocked(ks *workKindState, id string) {
	if _, ok := ks.failures[id+"\x00"]; ok {
		delete(ks.failures, id+"\x00")
		r.failureCount--
	}
}

// updateRequestLocked moves a live request between running and blocked,
// or ends it once every task is covered or failed.
func (r *workRunner) updateRequestLocked(q *workRequest) {
	if opTerminal(q.state) || !q.admitted || q.build != nil {
		return
	}
	remaining, blocked := 0, 0
	var first workBlocker
	failedTask := false
	for _, t := range q.tasks {
		if t.failed != "" {
			failedTask = true
		}
		if t.done || t.failed != "" {
			continue
		}
		remaining++
		if t.kind.running == nil && t.blocker.Reason != "" {
			blocked++
			if first.Reason == "" {
				first = t.blocker
			}
		}
		if t.blocker != t.reported {
			t.reported = t.blocker
			if t.blocker.Reason != "" {
				r.decideLocked("blocked", t.kind.def.Kind, q.id, 0, t.blocker.Reason, t.blocker.Detail)
			}
		}
	}
	if q.inv != nil && q.nSealed+q.nFailed+q.nExhausted == len(q.inv.members) {
		// Every member is sealed or settled unsuccessfully: pending work on
		// a unit that keeps changing is live demand, not this request's.
		remaining = 0
	}
	if remaining == 0 {
		switch {
		case failedTask || q.nFailed > 0 || q.libraryFail > 0:
			r.finishRequestLocked(q, opFailed, workUnitsFailed, fmt.Sprintf("%d units failed", q.nFailed+q.libraryFail))
		case q.nExhausted > 0:
			r.finishRequestLocked(q, opInterrupted, workInputsKeepChanging, fmt.Sprintf("%d units kept changing", q.nExhausted))
		default:
			r.finishRequestLocked(q, opSucceeded, "", "")
		}
		return
	}
	q.blocker = first
	if blocked == remaining {
		q.state = opBlocked
	} else {
		q.state = opRunning
	}
	r.requestChangedLocked(q)
}

// finishRequestLocked ends a request: its memory and explicit slot return,
// it leaves the demand of running batches (a batch nobody else needs stops
// at its safe boundary), and a count-only summary remains.
func (r *workRunner) finishRequestLocked(q *workRequest, state, reason, detail string) {
	q.state, q.reason, q.detail = state, reason, detail
	if q.admitted && q.spec.Explicit {
		r.explicit--
	}
	building := q.build != nil
	if building {
		// The builder still holds its memory until the read returns, so
		// the reservation (and RequestEnded) stay until finishBuild.
		cause := errWorkRequestCancelled
		if r.closed {
			cause = errServiceStopping
		}
		r.cancelBatchLocked(q.build, cause)
	} else {
		r.authority.releaseMemory(q.reserved)
		q.reserved = 0
	}
	for _, ks := range r.kinds {
		b := ks.running
		if b == nil {
			continue
		}
		if i := slices.Index(b.demanders, q.id); i >= 0 {
			b.demanders = slices.Delete(b.demanders, i, i+1)
			if len(b.demanders) == 0 {
				r.decideLocked("no-demand", ks.def.Kind, q.id, b.id, "", "")
				r.cancelBatchLocked(b, errWorkNoDemand)
			} else {
				r.demandChangedLocked(b)
			}
		}
	}
	facts := r.requestFactsLocked(q)
	r.requests = slices.DeleteFunc(r.requests, func(other *workRequest) bool { return other == q })
	if len(r.ended) >= workEndedRequestLimit {
		r.ended = slices.Delete(r.ended, 0, 1)
	}
	r.ended = append(r.ended, facts)
	q.inv, q.targets, q.tasks, q.mask = nil, nil, nil, nil
	q.sealed, q.failed, q.exhausted, q.replaced = nil, nil, nil, nil
	r.decideLocked("ended", "", q.id, 0, state, reason)
	switch {
	case building:
		q.endFacts = &facts
	case r.hooks.RequestEnded != nil:
		r.events = append(r.events, func() { r.hooks.RequestEnded(facts) })
	}
	r.closeExecutionsLocked()
	r.signal()
}

// closeExecutionsLocked ends the executor root of a kind with no running
// batch that is held or no longer demanded.
func (r *workRunner) closeExecutionsLocked() {
	for _, ks := range r.kinds {
		if ks.execution == 0 || ks.running != nil || ks.draining > 0 {
			continue
		}
		demanded := slices.ContainsFunc(r.requests, func(q *workRequest) bool {
			t := q.task(ks)
			return q.admitted && t != nil && !t.done && t.failed == ""
		})
		if demanded && !ks.held {
			continue
		}
		end := workExecutionEnd{Kind: ks.def.Kind, Execution: ks.execution, State: ks.lastState}
		switch {
		case ks.held:
			end.State, end.Reason = opCancelled, "stopped"
		case r.closed:
			end.State, end.Reason = opInterrupted, "service-stopping"
		case end.State == "" || end.State == opBlocked:
			end.State = opSucceeded
		}
		ks.execution = 0
		r.decideLocked("execution-ended", ks.def.Kind, 0, end.Execution, end.State, end.Reason)
		if r.hooks.ExecutionEnded != nil {
			r.events = append(r.events, func() { r.hooks.ExecutionEnded(end) })
		}
	}
}

// Cancel detaches a request's demand. Shared execution continues for other
// demand; the request itself becomes cancelled.
func (r *workRunner) Cancel(id uint64) bool {
	r.mu.Lock()
	defer r.unlock()
	for _, q := range r.requests {
		if q.id == id {
			r.finishRequestLocked(q, opCancelled, "", "")
			return true
		}
	}
	return false
}

// InterruptKinds ends requests whose accepted input policy changed. It does
// not hold the task or erase domain work: a new request discovers the new
// policy, while old requests cannot combine coverage across that change.
func (r *workRunner) InterruptKinds(kinds []string, detail string) {
	r.mu.Lock()
	defer r.unlock()
	for _, request := range slices.Clone(r.requests) {
		if slices.ContainsFunc(request.tasks, func(task *workRequestTask) bool { return slices.Contains(kinds, task.kind.def.Kind) }) {
			r.finishRequestLocked(request, opInterrupted, "configuration-changed", detail)
		}
	}
	for _, kind := range kinds {
		if state := r.byKind[kind]; state != nil {
			// Attempts at the old model/quality policy do not describe the
			// new policy, even when canonical conversation inputs agree.
			r.failureCount -= len(state.failures)
			clear(state.failures)
			state.full, state.wait = false, workBlocker{}
		}
	}
	r.signal()
}

// Stop drains a kind's running batch at its safe boundary and holds the kind
// until Resume, so dirty rediscovery cannot restart it. Its domain stays
// dirty. The hold is runtime only: a service restart ends it.
func (r *workRunner) Stop(kind string) bool {
	r.mu.Lock()
	defer r.unlock()
	ks := r.byKind[kind]
	if ks == nil {
		return false
	}
	ks.held = true
	if ks.running != nil {
		r.cancelBatchLocked(ks.running, errWorkStopped)
	}
	r.decideLocked("stopped", kind, 0, 0, workBlockedHeld, "")
	r.closeExecutionsLocked()
	return true
}

// Resume clears a Stop hold.
func (r *workRunner) Resume(kind string) bool {
	r.mu.Lock()
	defer r.unlock()
	ks := r.byKind[kind]
	if ks == nil {
		return false
	}
	ks.held = false
	ks.wait = workBlocker{}
	r.decideLocked("resumed", kind, 0, 0, "", "")
	r.signal()
	return true
}

// Retry forgets a kind's failures (an explicit user retry) so live requests
// attempt the failed units and phases again.
func (r *workRunner) Retry(kind string) bool {
	r.mu.Lock()
	defer r.unlock()
	ks := r.byKind[kind]
	if ks == nil {
		return false
	}
	for _, q := range r.requests {
		for _, t := range q.tasks {
			if t.failed == kind {
				t.failed, t.blocker = "", workBlocker{}
			}
		}
		if q.inv == nil {
			continue
		}
		for _, f := range ks.failures {
			if i, ok := q.inv.index(f.Unit.ID); ok && f.Exhausted && q.failed.has(i) {
				q.failed.unset(i)
				q.nFailed--
			}
		}
	}
	r.failureCount -= len(ks.failures)
	clear(ks.failures)
	ks.full, ks.wait = false, workBlocker{}
	r.decideLocked("retry-requested", kind, 0, 0, "", "")
	r.signal()
	return true
}

// SetPaused applies the library pause to new batches; safety work still
// runs, and running batches finish at their boundary.
func (r *workRunner) SetPaused(paused bool) {
	r.mu.Lock()
	if r.paused == paused {
		r.mu.Unlock()
		return
	}
	r.paused = paused
	r.decideLocked(map[bool]string{true: "paused", false: "unpaused"}[paused], "", 0, 0, "", "")
	r.unlock()
	r.signal()
}

const workShutdownDetail = "reissue explicit requests after restart; domain work remains pending"

// Run is the service loop: pump on every wake, on the earliest retry or
// wait deadline, and on the reconcile tick, until ctx ends.
func (r *workRunner) Run(ctx context.Context) {
	defer r.authority.dropClaim()
	defer func() {
		r.mu.Lock()
		r.closed = true
		// Running batches stop as interrupted, not as having lost demand.
		for _, ks := range r.kinds {
			if ks.running != nil {
				r.cancelBatchLocked(ks.running, errServiceStopping)
			}
		}
		for _, request := range slices.Clone(r.requests) {
			r.finishRequestLocked(request, opInterrupted, "service-stopping", workShutdownDetail)
		}
		r.unlock()
	}()
	for ctx.Err() == nil {
		r.Pump()
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-r.clock.After(r.nextDeadline()):
			r.mu.Lock()
			r.reconsider = true
			r.mu.Unlock()
		}
	}
}

func (r *workRunner) nextDeadline() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	wait := workReconcileInterval
	// Only future deadlines: a failure whose delay passed but whose unit is
	// not selected again must not spin the loop.
	for _, ks := range r.kinds {
		if ks.wait.Reason != "" && ks.waitUntil.After(now) {
			wait = min(wait, ks.waitUntil.Sub(now))
		}
		for _, f := range ks.failures {
			if !f.Exhausted && f.NextAt.After(now) {
				wait = min(wait, f.NextAt.Sub(now))
			}
		}
	}
	return wait
}

// Inspection: structured, bounded facts for activity, CLI and agents.

type workInspection struct {
	At             time.Time          `json:"at"`
	Paused         bool               `json:"paused,omitempty"`
	Authority      workAuthorityFacts `json:"authority"`
	Requests       []workRequestFacts `json:"requests"`
	Ended          []workRequestFacts `json:"ended,omitempty"`
	Tasks          []workTaskFacts    `json:"tasks"`
	Decisions      []workDecision     `json:"decisions"`
	DecisionsTotal int64              `json:"decisions_total"`
}

type workRequestFacts struct {
	ID          uint64                 `json:"id"`
	Key         string                 `json:"key,omitempty"`
	Scope       string                 `json:"scope,omitempty"`
	Trigger     string                 `json:"trigger,omitempty"`
	Kinds       []string               `json:"kinds"`
	Expanded    []string               `json:"expanded,omitempty"`
	Priority    string                 `json:"priority"`
	Explicit    bool                   `json:"explicit,omitempty"`
	State       string                 `json:"state"`
	Reason      string                 `json:"reason,omitempty"`
	Detail      string                 `json:"detail,omitempty"`
	Blocker     *workBlocker           `json:"blocker,omitempty"`
	Library     bool                   `json:"library,omitempty"`
	RequestedAt time.Time              `json:"requested_at"`
	FrozenAt    time.Time              `json:"membership_frozen_at,omitzero"`
	Members     int                    `json:"members"`
	Sealed      int                    `json:"sealed"`
	Failed      int                    `json:"failed,omitempty"`
	Exhausted   int                    `json:"exhausted,omitempty"`
	Unfinished  int                    `json:"unfinished"`
	Bytes       int64                  `json:"bytes"`
	Tasks       []workRequestTaskFacts `json:"tasks,omitempty"`
}

type workRequestTaskFacts struct {
	Kind    string       `json:"kind"`
	State   string       `json:"state"`
	Covered int          `json:"covered"`
	Blocker *workBlocker `json:"blocker,omitempty"`
}

type workTaskFacts struct {
	Kind        string        `json:"kind"`
	State       string        `json:"state"`
	Coarse      bool          `json:"coarse,omitempty"`
	Blocker     *workBlocker  `json:"blocker,omitempty"`
	Execution   uint64        `json:"execution,omitempty"`
	Running     *workJob      `json:"running,omitempty"`
	Batches     int64         `json:"batches"`
	Committed   int64         `json:"committed_units"`
	Work        int64         `json:"work"`
	Active      time.Duration `json:"active_ns"`
	SelectTime  time.Duration `json:"select_ns"`
	SlowSelects int64         `json:"slow_selects,omitempty"`
	Failures    int           `json:"failures,omitempty"`
	Examples    []workFailure `json:"failure_examples,omitempty"`
}

func blockerFact(b workBlocker) *workBlocker {
	if b.Reason == "" {
		return nil
	}
	return &b
}

func (r *workRunner) requestFactsLocked(q *workRequest) workRequestFacts {
	f := workRequestFacts{ID: q.id, Key: q.spec.Key, Scope: q.spec.Scope, Trigger: q.spec.Trigger, Kinds: q.spec.Kinds, Expanded: q.expanded,
		Priority: q.spec.Priority.String(), Explicit: q.spec.Explicit, State: q.state, Reason: q.reason, Detail: q.detail,
		Blocker: blockerFact(q.blocker), Library: q.spec.Build == nil, RequestedAt: q.requestedAt, Bytes: q.reserved,
		Sealed: q.nSealed, Failed: q.nFailed + q.libraryFail, Exhausted: q.nExhausted}
	if q.inv != nil {
		f.FrozenAt, f.Members = q.inv.frozenAt, len(q.inv.members)
		f.Unfinished = f.Members - q.nSealed - q.nFailed - q.nExhausted
	}
	for _, t := range q.tasks {
		tf := workRequestTaskFacts{Kind: t.kind.def.Kind, Blocker: blockerFact(t.blocker)}
		switch {
		case t.done:
			tf.State = opSucceeded
		case t.failed != "":
			tf.State = opFailed
		case t.kind.running != nil && slices.Contains(t.kind.running.demanders, q.id):
			tf.State = opRunning
		case t.blocker.Reason != "":
			tf.State = opBlocked
		default:
			tf.State = opQueued
		}
		if t.covered != nil {
			tf.Covered = t.covered.count()
		}
		f.Tasks = append(f.Tasks, tf)
	}
	return f
}

// Inspect returns the runner's facts. Every list is bounded.
func (r *workRunner) Inspect() workInspection {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	in := workInspection{At: now, Paused: r.paused, Authority: r.authority.facts(), Ended: slices.Clone(r.ended), DecisionsTotal: r.decisionsTotal}
	for _, q := range r.requests {
		in.Requests = append(in.Requests, r.requestFactsLocked(q))
	}
	for _, ks := range r.kinds {
		tf := workTaskFacts{Kind: ks.def.Kind, Coarse: ks.def.Coarse, Execution: ks.execution, Batches: ks.batches, Committed: ks.committed,
			Work: ks.work, Active: ks.active, SelectTime: ks.selectTime, SlowSelects: ks.slowSelects, Failures: len(ks.failures)}
		blocker := r.kindBlockerLocked(ks, now)
		tf.Blocker = blockerFact(blocker)
		demanded := slices.ContainsFunc(r.requests, func(q *workRequest) bool { t := q.task(ks); return t != nil && !t.done && t.failed == "" })
		switch {
		case ks.running != nil:
			tf.State = opRunning
			b := ks.running
			tf.Running = &workJob{Kind: ks.def.Kind, Execution: ks.execution, Batch: b.id, Phase: b.sel.Phase, Units: len(b.sel.Units),
				CPUs: b.cpus, Coarse: b.sel.Coarse || ks.def.Coarse, Demanders: slices.Clone(b.demanders)}
		case blocker.Reason != "":
			tf.State = opBlocked
		case demanded:
			tf.State = opQueued
		}
		for _, f := range ks.failures {
			tf.Examples = append(tf.Examples, *f)
		}
		slices.SortFunc(tf.Examples, func(a, b workFailure) int { return strings.Compare(a.Unit.ID, b.Unit.ID) })
		tf.Examples = tf.Examples[:min(len(tf.Examples), 5)]
		in.Tasks = append(in.Tasks, tf)
	}
	if n := len(r.decisions); n > 0 {
		// The ring's next write is its oldest entry.
		start := int(r.decisionsTotal % int64(n))
		in.Decisions = append(slices.Clone(r.decisions[start:]), r.decisions[:start]...)
	}
	return in
}
