package archive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"
	"time"
)

// The operation registry (docs/background-work.md §2) records every piece of
// structural background work a process runs: what it is, what it holds, which
// step it is on, and how it ended. In phases 1–3 it only observes: the work
// keeps its own locks, contexts and stoppers, and the registry is told about
// them. The library service, each writing CLI command and the broker each
// keep one registry; the service and CLI commands journal theirs into the
// catalog (operation_journal.go), and the broker persists its own
// (operation_broker.go).

// OpID names one operation: 32 hex characters, random.
type OpID string

func newOpID() OpID {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	return OpID(hex.EncodeToString(value))
}

// Triggers say what started an operation.
const (
	triggerSchedule   = "schedule"
	triggerUser       = "user"
	triggerChained    = "chained"
	triggerStartup    = "startup"
	triggerVersion    = "version"
	triggerRead       = "read"
	triggerDevice     = "device"
	triggerCLI        = "cli"
	triggerRespawn    = "respawn"
	triggerOnboarding = "onboarding"
)

// OperationSpec is fixed for an operation's life (§2.1).
type OperationSpec struct {
	Kind    string
	Variant string
	Subject string
	Parent  OpID
	Trigger string
	// Holds, Affects and Priority default from the kind row.
	Holds    []Hold
	Affects  []string
	Priority string
	// Legacy is the ID of the record the operation shadows (a SyncRun ID, a
	// backup run ID, a cloud run ID…).
	Legacy string
	// Queued registers the operation as waiting for its turn rather than
	// running.
	Queued bool
}

// StepPlan is one step declared up front (§2.3 Plan).
type StepPlan struct {
	Name  string `json:"name"`
	Unit  string `json:"unit,omitempty"`
	Total int64  `json:"total"`
}

// Lifecycle states (§2.2).
const (
	opQueued      = "queued"
	opRunning     = "running"
	opBlocked     = "blocked"
	opStopping    = "stopping"
	opSucceeded   = "succeeded"
	opFailed      = "failed"
	opCancelled   = "cancelled"
	opInterrupted = "interrupted"
	opSkipped     = "skipped"
)

var opTransitions = map[string][]string{
	opQueued:   {opRunning, opSkipped, opFailed, opCancelled, opInterrupted},
	opRunning:  {opBlocked, opStopping, opSucceeded, opFailed, opSkipped, opInterrupted},
	opBlocked:  {opRunning, opStopping, opFailed, opSkipped, opInterrupted},
	opStopping: {opCancelled, opSucceeded, opFailed, opSkipped, opInterrupted},
}

func opTerminal(state string) bool {
	switch state {
	case opSucceeded, opFailed, opCancelled, opInterrupted, opSkipped:
		return true
	}
	return false
}

// strictOperations makes a programming error in the registry (an invalid
// transition, an unknown kind) panic. Tests set it; production logs instead.
var strictOperations = false

// errOperationSkipped, returned by work, ends its operation as skipped
// (with the reason given to Skip, or nothing-to-do).
var errOperationSkipped = errors.New("operation skipped")

// opStep is the operation's current step.
type opStep struct {
	Index  int
	Name   string
	Source string
	Unit   string
	Done   float64
	Total  float64 // < 0: unknown
}

// Op is the handle work uses to report itself (§2.3). Every method is safe on
// a nil handle, which records nothing, so code that may run without a
// registry (a test catalog, a read-only open) needs no special case. Methods
// never touch SQLite or take a catalog lock: they can run inside a write
// transaction. They update memory under the registry's mutex and wake its
// subscribers and journal writer.
type Op struct {
	reg    *opRegistry
	id     OpID
	spec   OperationSpec
	kind   *operationKind
	parent *Op
	root   *Op
	ctx    context.Context

	// Guarded by reg.mu.
	state, reason, detail, errText string
	started, updated, ended        time.Time
	plan                           []StepPlan
	step                           opStep
	safePoint                      time.Duration
	safePointKnown                 bool
	wrote                          bool
	userStop                       bool
	duringRelease                  bool
	outcome                        *opOutcome
	children                       []*Op
	forecast                       *operationForecast
	version                        uint64 // bumped on every change, for the journal
	written                        uint64 // version last written to the journal
	writtenEver                    bool
	// tentative is set while a daemon pass's operation is unpublished.
	tentative bool
}

// opOutcome is the terminal state the work's own record decided (a SyncRun's
// state), used when the work returns without an error.
type opOutcome struct {
	state, reason, err string
}

// ID returns the operation's ID, or "" for a nil handle.
func (op *Op) ID() OpID {
	if op == nil {
		return ""
	}
	return op.id
}

// Context is the context the operation was registered with, carrying it.
func (op *Op) Context() context.Context {
	if op == nil {
		return context.Background()
	}
	return op.ctx
}

type opContextKey struct{}

// withOp makes op the parent of operations begun from ctx.
func withOp(ctx context.Context, op *Op) context.Context {
	if op == nil {
		return ctx
	}
	return context.WithValue(ctx, opContextKey{}, op)
}

// opFrom returns the operation ctx carries, or nil.
func opFrom(ctx context.Context) *Op {
	if ctx == nil {
		return nil
	}
	op, _ := ctx.Value(opContextKey{}).(*Op)
	return op
}

// opRegistry holds one process's operations.
type opRegistry struct {
	mu       sync.Mutex
	process  string // service | cli:<command> | broker | mcp
	instance string // random per process lifetime
	ops      map[OpID]*Op
	revision uint64
	subs     map[chan struct{}]struct{}
	// wake is signalled on every change, for the journal writer or the
	// broker's store; nil when nothing persists this registry.
	wake chan struct{}
	// boundary is signalled when an operation starts or ends.
	boundary bool
	now      func() time.Time
	// releasing reports a release in progress (§2.3 detail.during_release).
	releasing func() bool
	// invalid counts programming errors (invalid transitions, unknown kinds).
	invalid  int
	loggedAt time.Time
	// keepEnded is how long an ended top-level operation stays listed.
	keepEnded time.Duration
	// journaled is true when a journal writer drains this registry; without
	// one, ended operations need not wait to be written before they are
	// forgotten.
	journaled bool
}

func newOpRegistry(process string) *opRegistry {
	return &opRegistry{process: process, instance: string(newOpID()), ops: map[OpID]*Op{}, subs: map[chan struct{}]struct{}{},
		now: time.Now, keepEnded: 10 * time.Minute}
}

func (r *opRegistry) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// programmingError reports misuse of the registry: a test fails, production
// logs (at most once a minute) and carries on.
func (r *opRegistry) programmingErrorLocked(format string, args ...any) {
	r.invalid++
	message := fmt.Sprintf(format, args...)
	if strictOperations {
		panic("operation registry: " + message)
	}
	if time.Since(r.loggedAt) >= time.Minute {
		r.loggedAt = time.Now()
		fmt.Fprintf(os.Stderr, "Operation registry: %s\n", message)
	}
}

// changedLocked records a change to op: a new revision for subscribers and
// a new version for the journal.
func (r *opRegistry) changedLocked(op *Op, boundary bool) {
	r.revision++
	if op != nil {
		op.version++
		op.updated = r.clock()
		op.tentative = false
	}
	if boundary {
		r.boundary = true
	}
	for sub := range r.subs {
		select {
		case sub <- struct{}{}:
		default:
		}
	}
	if r.wake != nil {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// subscribe returns a channel that receives a value after changes, coalesced:
// a subscriber that has not read since the last change gets one value for
// all of them. unsubscribe stops it.
func (r *opRegistry) subscribe() (<-chan struct{}, func()) {
	if r == nil {
		return make(chan struct{}), func() {}
	}
	sub := make(chan struct{}, 1)
	r.mu.Lock()
	r.subs[sub] = struct{}{}
	r.mu.Unlock()
	return sub, func() {
		r.mu.Lock()
		delete(r.subs, sub)
		r.mu.Unlock()
	}
}

// currentRevision is the registry's change counter.
func (r *opRegistry) currentRevision() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revision
}

// begin registers an operation as running (or queued), with ctx, before its
// work starts, and returns ctx carrying it. A nil registry registers nothing
// and returns a nil handle.
func (r *opRegistry) begin(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	return r.register(ctx, spec, false)
}

// beginTentative registers a daemon pass's operation before the pass runs
// without telling subscribers yet: a pass that turns out to have nothing to
// do is discarded and leaves no trace, and the first change publishes it.
// While registered it counts for the verdict like any other.
func (r *opRegistry) beginTentative(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	return r.register(ctx, spec, true)
}

func (r *opRegistry) register(ctx context.Context, spec OperationSpec, tentative bool) (context.Context, *Op) {
	if r == nil {
		return ctx, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	kind, ok := lookupOperationKind(spec.Kind)
	releasing := r.releasing != nil && r.releasing()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ok {
		r.programmingErrorLocked("unknown operation kind %q", spec.Kind)
		kind = operationKindIndex[unknownOperationKind]
		spec.Kind = unknownOperationKind
	}
	if spec.Holds == nil {
		spec.Holds = parseHolds(kind.Holds)
	}
	if spec.Affects == nil {
		spec.Affects = parseAffects(kind.Affects)
	}
	op := &Op{reg: r, id: newOpID(), spec: spec, kind: kind, state: opRunning}
	if spec.Queued {
		op.state = opQueued
	}
	if parent := r.ops[spec.Parent]; spec.Parent != "" && parent != nil {
		op.parent = parent
	} else if parent := opFrom(ctx); spec.Parent == "" && parent != nil && parent.reg == r {
		op.parent = parent
	}
	if op.parent != nil {
		op.spec.Parent = op.parent.id
		op.root = op.parent.root
		op.parent.children = append(op.parent.children, op)
		if op.spec.Priority == "" {
			op.spec.Priority = op.parent.spec.Priority
		}
	} else {
		op.root = op
	}
	if op.spec.Priority == "" {
		op.spec.Priority = defaultString(kind.Priority, priorityBackground)
	}
	op.started = r.clock()
	op.updated = op.started
	op.duringRelease = releasing
	op.ctx = withOp(ctx, op)
	r.ops[op.id] = op
	r.pruneLocked()
	if tentative {
		op.tentative = true
		op.version++
	} else {
		r.changedLocked(op, true)
	}
	return op.ctx, op
}

// discard forgets a tentative operation that did nothing. One that was
// published since is finished as succeeded instead.
func (op *Op) discard() {
	if !op.lock() {
		return
	}
	if !op.tentative || opTerminal(op.state) {
		op.reg.mu.Unlock()
		if !opTerminal(op.State()) {
			op.Finish(nil)
		}
		return
	}
	delete(op.reg.ops, op.id)
	if op.parent != nil {
		op.parent.children = slices.DeleteFunc(op.parent.children, func(child *Op) bool { return child == op })
	}
	op.state = opSkipped
	op.reg.mu.Unlock()
}

// pruneLocked forgets ended top-level operations (and their children) once
// they are older than keepEnded, or past the newest 200, and once the journal
// has written them.
func (r *opRegistry) pruneLocked() {
	ended := []*Op{}
	for _, op := range r.ops {
		if op.parent == nil && opTerminal(op.state) {
			ended = append(ended, op)
		}
	}
	if len(ended) == 0 {
		return
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].ended.After(ended[j].ended) })
	cutoff := r.clock().Add(-r.keepEnded)
	for index, op := range ended {
		if index < 200 && op.ended.After(cutoff) {
			continue
		}
		if r.journaled && op.treeUnwrittenLocked() {
			continue
		}
		op.forgetLocked()
	}
}

func (op *Op) treeUnwrittenLocked() bool {
	if op.version != op.written && op.reg.journalEligibleLocked(op) {
		return true
	}
	return slices.ContainsFunc(op.children, (*Op).treeUnwrittenLocked)
}

func (op *Op) forgetLocked() {
	for _, child := range op.children {
		child.forgetLocked()
	}
	delete(op.reg.ops, op.id)
}

// transitionLocked moves op to state, or records a programming error and
// keeps the old state.
func (op *Op) transitionLocked(state string) bool {
	if op.state == state {
		return true
	}
	if !slices.Contains(opTransitions[op.state], state) {
		op.reg.programmingErrorLocked("%s %s: invalid transition %s → %s", op.spec.Kind, op.id, op.state, state)
		return false
	}
	op.state = state
	if opTerminal(state) {
		op.ended = op.reg.clock()
		op.safePointKnown = false
	}
	return true
}

func (op *Op) lock() bool {
	if op == nil || op.reg == nil {
		return false
	}
	op.reg.mu.Lock()
	return true
}

func (op *Op) unlock(boundary bool) {
	op.reg.changedLocked(op, boundary)
	op.reg.mu.Unlock()
}

// Run moves a queued operation to running; the first Step implies it.
func (op *Op) Run() {
	if !op.lock() {
		return
	}
	if op.state == opQueued {
		op.transitionLocked(opRunning)
	}
	op.unlock(false)
}

// Plan declares the steps up front, so surfaces can say "step N of M". It
// may be revised as discovery learns more; only steps not yet started should
// change.
func (op *Op) Plan(steps ...StepPlan) {
	if !op.lock() {
		return
	}
	op.plan = slices.Clone(steps)
	op.unlock(false)
}

// Step starts the next step. total < 0 means unknown.
func (op *Op) Step(name, unit string, total int64) {
	if !op.lock() {
		return
	}
	op.stepLocked(name, "", unit, float64(total))
	op.unlock(false)
}

func (op *Op) stepLocked(name, source, unit string, total float64) {
	if op.state == opQueued {
		op.transitionLocked(opRunning)
	}
	index := op.step.Index + 1
	if planned := slices.IndexFunc(op.plan, func(step StepPlan) bool { return step.Name == name }); planned >= 0 {
		index = planned + 1
	}
	op.step = opStep{Index: index, Name: name, Source: source, Unit: unit, Total: total}
}

// Advance adds n to the current step's done count. It allocates nothing, so
// it may be called per record.
func (op *Op) Advance(n int64) {
	if !op.lock() {
		return
	}
	op.step.Done += float64(n)
	op.unlock(false)
}

// SetDone sets the current step's done count, for counters that are already
// cumulative.
func (op *Op) SetDone(n int64) {
	if !op.lock() {
		return
	}
	op.step.Done = float64(n)
	op.unlock(false)
}

// progress mirrors a measured progress report (an activityWork, a SyncRun's
// phase) into the operation: a new phase or source starts a new step, and
// the same one updates its counts. It allocates nothing when nothing changed.
func (op *Op) progress(phase, source, unit string, done, total float64, known bool) {
	if !op.lock() {
		return
	}
	if !known && total <= 0 {
		total = -1
	}
	if op.state == opQueued {
		op.transitionLocked(opRunning)
	}
	if op.step.Name != phase || op.step.Source != source {
		op.stepLocked(phase, source, unit, total)
		op.step.Done = done
		op.unlock(false)
		return
	}
	if op.step.Unit == unit && op.step.Done == done && op.step.Total == total {
		op.reg.mu.Unlock()
		return
	}
	op.step.Unit, op.step.Done, op.step.Total = unit, done, total
	op.unlock(false)
}

// mirrorWork mirrors an activityWork report.
func (op *Op) mirrorWork(w activityWork) {
	if op == nil {
		return
	}
	measured := w.measured()
	op.progress(w.Phase, w.Source, measured.Unit, measured.Done, measured.Total, measured.TotalKnown || measured.Total > 0)
}

// Block moves a running operation to blocked with a reason code (§8.3) and
// a detail for people.
func (op *Op) Block(reason, detail string) {
	if !op.lock() {
		return
	}
	if op.state == opQueued {
		op.transitionLocked(opRunning)
	}
	if op.state == opRunning && op.transitionLocked(opBlocked) {
		op.reason, op.detail = reason, detail
	}
	op.unlock(false)
}

// Unblock returns a blocked operation to running.
func (op *Op) Unblock() {
	if !op.lock() {
		return
	}
	if op.state == opBlocked && op.transitionLocked(opRunning) {
		op.reason, op.detail = "", ""
	}
	op.unlock(false)
}

// SafePoint says how soon a stop would land; 0 means at one now.
func (op *Op) SafePoint(next time.Duration) {
	if !op.lock() {
		return
	}
	op.safePoint, op.safePointKnown = next, true
	op.unlock(false)
}

// Skip ends the operation as skipped(reason); the work must return.
func (op *Op) Skip(reason string) {
	if !op.lock() {
		return
	}
	if opTerminal(op.state) {
		op.reg.mu.Unlock()
		return
	}
	if op.state == opQueued || op.state == opRunning || op.state == opBlocked || op.state == opStopping {
		op.state, op.reason = opSkipped, reason
		op.ended = op.reg.clock()
	}
	op.unlock(true)
}

// Wrote records that the operation changed data (for if-longer-than, §6.2).
func (op *Op) Wrote() {
	if !op.lock() {
		return
	}
	if op.wrote {
		op.reg.mu.Unlock()
		return
	}
	op.wrote = true
	op.unlock(false)
}

// Child registers a stage of this operation.
func (op *Op) Child(spec OperationSpec) (context.Context, *Op) {
	if op == nil {
		return context.Background(), nil
	}
	spec.Parent = op.id
	return op.reg.begin(op.ctx, spec)
}

// child is Child from a context that may carry other values than op's own.
func (op *Op) childFrom(ctx context.Context, spec OperationSpec) (context.Context, *Op) {
	if op == nil {
		return ctx, nil
	}
	spec.Parent = op.id
	return op.reg.begin(ctx, spec)
}

// markStopping records a stop asked for by a user (a stop endpoint).
func (op *Op) markStopping() {
	if !op.lock() {
		return
	}
	if op.state == opQueued || op.state == opRunning || op.state == opBlocked {
		op.userStop = true
		if op.state == opQueued {
			op.state = opRunning
		}
		op.transitionLocked(opStopping)
	}
	op.unlock(false)
}

// legacyOutcome records the terminal state the work's own record reached:
// complete or deferred is succeeded, failed is failed, interrupted is
// cancelled after a user stop and interrupted otherwise. Finish(nil) uses it.
func (op *Op) legacyOutcome(state string, failure any) {
	if !op.lock() {
		return
	}
	outcome := &opOutcome{}
	switch state {
	case "complete", "deferred", "passed":
		outcome.state = opSucceeded
	case "interrupted", "cancelled":
		outcome.state = opInterrupted
		if op.userStop {
			outcome.state = opCancelled
		}
	default:
		outcome.state, outcome.err = opFailed, defaultString(firstString(failure), state)
	}
	if outcome.err == "" && failure != nil && outcome.state == opFailed {
		outcome.err = fmt.Sprint(failure)
	}
	op.outcome = outcome
	op.unlock(false)
}

// Finish ends the operation from err (§2.3): nil is succeeded (or the
// recorded legacy outcome); an error after a user stop is cancelled; any
// other error once the context is done is interrupted; others are failed. An
// operation that already ended (Skip) keeps its state.
func (op *Op) Finish(err error) {
	if !op.lock() {
		return
	}
	defer op.unlock(true)
	if opTerminal(op.state) {
		return
	}
	state, reason, text := opSucceeded, "", ""
	switch {
	case err == nil && op.outcome != nil:
		state, reason, text = op.outcome.state, op.outcome.reason, op.outcome.err
	case err == nil && op.ctx != nil && op.ctx.Err() != nil && op.parent == nil:
		// Work that returns quietly once a release cancelled it did not
		// finish: it is interrupted, and its next run resumes it.
		state = opInterrupted
	case err == nil:
	case errors.Is(err, errOperationSkipped):
		state, reason = opSkipped, defaultString(op.reason, "nothing-to-do")
	case op.userStop:
		state = opCancelled
	case op.ctx != nil && op.ctx.Err() != nil:
		state = opInterrupted
	default:
		state, text = opFailed, err.Error()
	}
	op.finishLocked(state, reason, text)
}

func (op *Op) finishLocked(state, reason, text string) {
	// Finishing implies the steps in between: work that ends while queued
	// or blocked ran, and a cancelled operation was stopping.
	if state == opSucceeded && (op.state == opQueued || op.state == opBlocked) {
		op.state = opRunning
	}
	if state == opCancelled && (op.state == opRunning || op.state == opBlocked) {
		op.state = opStopping
	}
	if state == opSkipped {
		op.state, op.reason, op.ended = opSkipped, reason, op.reg.clock()
		return
	}
	if op.transitionLocked(state) {
		op.reason, op.errText = reason, text
		if state != opBlocked {
			op.detail = ""
		}
	}
}

// finishPanic ends the operation as failed when its work panicked; the
// caller re-raises the panic.
func (op *Op) finishPanic(value any) {
	if !op.lock() {
		return
	}
	if !opTerminal(op.state) {
		if op.state == opQueued {
			op.state = opRunning
		}
		op.finishLocked(opFailed, "panic", fmt.Sprint(value))
	}
	op.unlock(true)
}

// linkForecast lets the snapshot read the operation's forecast.
func (op *Op) linkForecast(forecast *operationForecast) {
	if op == nil || forecast == nil {
		return
	}
	op.reg.mu.Lock()
	op.forecast = forecast
	op.reg.mu.Unlock()
}

// running lists the registry's operations that have not ended, top level and
// children alike.
func (r *opRegistry) running() []*Op {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ops := []*Op{}
	for _, op := range r.ops {
		if !opTerminal(op.state) {
			ops = append(ops, op)
		}
	}
	return ops
}

// find returns the operation with id, or nil.
func (r *opRegistry) find(id OpID) *Op {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ops[id]
}

// findLegacy returns the newest operation shadowing legacy, or nil.
func (r *opRegistry) findLegacy(legacy string) *Op {
	if r == nil || legacy == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var found *Op
	for _, op := range r.ops {
		if op.spec.Legacy == legacy && (found == nil || op.started.After(found.started)) {
			found = op
		}
	}
	return found
}

// markKindStopping marks every running operation of kinds stopping, as an
// existing stop endpoint does when it calls their stopper.
func (r *opRegistry) markKindStopping(kinds ...string) {
	for _, op := range r.running() {
		if op.parent == nil && slices.Contains(kinds, op.spec.Kind) {
			op.markStopping()
		}
	}
}

// opView is a copy of an operation's state, taken under the registry lock,
// for snapshots, the verdict and the journal.
type opView struct {
	ID            OpID
	Parent        OpID
	Root          OpID
	Spec          OperationSpec
	Kind          *operationKind
	Process       string
	Instance      string
	State         string
	Reason        string
	Detail        string
	Error         string
	Started       time.Time
	Updated       time.Time
	Ended         time.Time
	Plan          []StepPlan
	Step          opStep
	SafePoint     time.Duration
	SafePointSet  bool
	Wrote         bool
	UserStop      bool
	DuringRelease bool
	Children      []OpID
	Forecast      *operationForecast
	Version       uint64
	// Foreign is true for an operation another process owns (a lease).
	Foreign bool
}

func (op *Op) viewLocked() opView {
	view := opView{ID: op.id, Root: op.root.id, Spec: op.spec, Kind: op.kind, Process: op.reg.process, Instance: op.reg.instance, State: op.state,
		Reason: op.reason, Detail: op.detail, Error: op.errText, Started: op.started, Updated: op.updated, Ended: op.ended, Plan: slices.Clone(op.plan),
		Step: op.step, SafePoint: op.safePoint, SafePointSet: op.safePointKnown, Wrote: op.wrote, UserStop: op.userStop,
		DuringRelease: op.duringRelease, Forecast: op.forecast, Version: op.version}
	if op.parent != nil {
		view.Parent = op.parent.id
	}
	for _, child := range op.children {
		view.Children = append(view.Children, child.id)
	}
	return view
}

// views copies every operation the registry holds.
func (r *opRegistry) views() []opView {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	views := make([]opView, 0, len(r.ops))
	for _, op := range r.ops {
		views = append(views, op.viewLocked())
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Started.After(views[j].Started) })
	return views
}

// view copies one operation.
func (op *Op) view() opView {
	if op == nil {
		return opView{}
	}
	op.reg.mu.Lock()
	defer op.reg.mu.Unlock()
	return op.viewLocked()
}

// state returns the operation's lifecycle state.
func (op *Op) State() string {
	if op == nil {
		return ""
	}
	op.reg.mu.Lock()
	defer op.reg.mu.Unlock()
	return op.state
}
