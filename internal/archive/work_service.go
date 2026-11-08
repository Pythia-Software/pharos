package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"time"
)

// workService joins the small runner to the existing registry/lifecycle. Request
// operations express demand. Actual execution is rooted independently in the
// service lifetime; finishing or pruning a requester cannot retire shared work.
type workService struct {
	server        *Server
	runner        *workRunner
	authority     *workAuthority
	once          sync.Once
	startErr      error
	mu            sync.Mutex
	executions    map[uint64]*workOperation
	requests      map[uint64]*workOperation
	batches       map[uint64]*workBatchOperation
	endedRequests []uint64
}

type workBatchOperation struct {
	op       *Op
	owner    *workOperation
	returned bool
	end      *workBatchEnd
}

type workOperation struct {
	op            *Op
	id            uint64
	execution     uint64
	active        int
	state, reason string
	done          chan struct{}
	facts         workRequestFacts
	waitKind      string
	endWait       func()
}

func (s *Server) configureWork() error {
	if s.Catalog == nil || s.Config().WorkEngine != "scheduled" {
		return nil
	}
	w := &workService{server: s, authority: newWorkAuthority(max(1, s.Config().BackgroundCPUs)), executions: map[uint64]*workOperation{}, requests: map[uint64]*workOperation{}, batches: map[uint64]*workBatchOperation{}}
	s.work = w
	defs, err := s.workDefinitions(w.authority)
	if err != nil {
		return err
	}
	w.runner, err = newWorkRunner(s.life.ctx, workSystemClock{}, w.authority, workHooks{Start: w.start, ExecutionEnded: w.executionEnded, RequestEnded: w.requestEnded, RequestChanged: w.requestChanged, BatchEnded: w.batchEnded, DemandChanged: w.demandChanged,
		Paused: s.Catalog.workPaused}, defs)
	if err != nil {
		return err
	}
	s.Catalog.work = w
	s.ops.telemetry, s.ops.executorRoots = true, true
	s.ops.resourceLedger = newTaskResourceLedger(20, 200)
	// The broker advertises collection explicitly. Merely having a parent
	// process does not establish that somebody else is sampling this service.
	s.resourceBrokerOwned = os.Getenv("PHAROS_RESOURCE_OWNER") == "broker"
	if !s.resourceBrokerOwned {
		s.ops.resourceHistory = newResourceHistory(s.resources, processGPU, s.collectResourceHistory)
	}
	return nil
}

func (w *workService) startLoop() error {
	w.once.Do(func() {
		if history := w.server.resources.history; history != nil {
			w.server.daemon("resourceHistory", history.run)
		}
		if !w.server.daemon("workExecutor", func(ctx context.Context) { w.runner.Run(ctx) }) {
			w.startErr = errServiceStopping
		}
	})
	if err := w.server.life.ctx.Err(); err != nil {
		return errServiceStopping
	}
	return w.startErr
}

// workPaused is the runner's pause policy: this Mac's background schedule
// paused (until its time) or set to manual indexing (until changed).
func (c *Catalog) workPaused(ctx context.Context, now time.Time) (bool, time.Time, error) {
	settings, err := c.backgroundSettings(ctx, now)
	if err != nil {
		return false, time.Time{}, err
	}
	until, _ := parseTime(settings.PausedUntil)
	return settings.Mode != "on", until, nil
}

func workOpPriority(priority workPriority) string {
	if priority <= workPriorityForeground {
		return priorityForeground
	}
	return priorityBackground
}

func (w *workService) requestLocked(id uint64, trigger string) *workOperation {
	if record := w.requests[id]; record != nil {
		return record
	}
	_, op := w.server.begin(w.server.life.ctx, OperationSpec{Kind: "analysis.chain", Variant: "population-request", Subject: fmt.Sprintf("Work request %d", id), Trigger: defaultString(trigger, triggerChained), Queued: true})
	record := &workOperation{op: op, id: id, done: make(chan struct{})}
	record.waitKind, record.endWait = waitQueue, op.resources.waitSpan(waitQueue)
	w.requests[id] = record
	op.setWorkControl(func() { w.runner.Cancel(id) })
	return record
}

func (w *workService) start(job workJob, run func(context.Context)) error {
	w.mu.Lock()
	var record *workOperation
	if job.Request != 0 {
		record = w.requestLocked(job.Request, job.Triggers[job.Request])
	} else {
		record = w.executions[job.Execution]
		if record == nil {
			_, op := w.server.begin(w.server.life.ctx, OperationSpec{Kind: job.Kind, Variant: "scheduled", Trigger: triggerChained, Priority: workOpPriority(job.Priority)})
			record = &workOperation{op: op, execution: job.Execution, done: make(chan struct{})}
			w.executions[job.Execution] = record
			op.setWorkControl(func() { w.runner.Stop(job.Kind) })
		}
	}
	record.active++
	if record.op.State() == opBlocked {
		record.op.Unblock()
	}
	demanders := []OpID{}
	for _, id := range job.Demanders {
		if id == job.Request {
			continue // membership belongs to this request; it is not shared work
		}
		request := w.requestLocked(id, job.Triggers[id])
		demanders = append(demanders, request.op.id)
		request.op.linkWork(record.op.id)
	}
	record.op.setNeededBy(demanders)
	_, batch := record.op.Child(OperationSpec{Kind: job.Kind, Variant: "batch", Subject: job.Phase, Trigger: triggerChained, Priority: workOpPriority(job.Priority)})
	batch.setNeededBy(demanders)
	batchRecord := &workBatchOperation{op: batch, owner: record}
	w.batches[job.Batch] = batchRecord
	w.mu.Unlock()
	if !w.server.spawnRegistered(nil, func() {
		defer func() {
			w.mu.Lock()
			batchRecord.returned = true
			w.finishBatchLocked(job.Batch, batchRecord)
			if job.Request == 0 && record.state != "" && record.active == 0 {
				delete(w.executions, job.Execution)
			}
			w.mu.Unlock()
		}()
		res := batch.resources
		defer res.allocate(taskAllocation{CPUSlots: job.CPUs})()
		stopActive := res.activeSpan()
		defer stopActive()
		ctx := withWorkAdmission(batch.Context(), w.authority, workAllocation{CPUs: job.CPUs})
		if res != nil {
			pprof.Do(ctx, taskProfileLabels(res.identity), run)
		} else {
			run(ctx)
		}
	}) {
		batch.Finish(errServiceStopping)
		w.mu.Lock()
		delete(w.batches, job.Batch)
		record.active--
		w.finishLocked(record)
		w.mu.Unlock()
		return errServiceStopping
	}
	if history := w.server.resources.history; history != nil {
		history.poke()
	}
	return nil
}

func (w *workService) finishBatchLocked(id uint64, batch *workBatchOperation) {
	if !batch.returned || batch.end == nil {
		return
	}
	if batch.op.lock() {
		text := ""
		if batch.end.Err != nil {
			text = batch.end.Err.Error()
		}
		// Blocked is a completed scheduling attempt. Keeping it live would
		// retain a child forever after its grant and workers had drained.
		state := batch.end.State
		if state == opBlocked {
			state = opInterrupted
		}
		batch.op.finishLocked(state, batch.end.Reason, text)
		batch.op.unlock(true)
	}
	delete(w.batches, id)
	batch.owner.active--
	w.finishLocked(batch.owner)
	w.pruneRequestsLocked()
}

func (w *workService) batchEnded(end workBatchEnd) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if batch := w.batches[end.Job.Batch]; batch != nil {
		batch.end = &end
		w.finishBatchLocked(end.Job.Batch, batch)
	}
}

func (w *workService) demandChanged(demand workDemand) {
	w.mu.Lock()
	defer w.mu.Unlock()
	batch := w.batches[demand.Batch]
	if batch == nil || batch.end != nil {
		return
	}
	ids := make([]OpID, 0, len(demand.Demanders))
	for _, id := range demand.Demanders {
		request := w.requestLocked(id, demand.Triggers[id])
		ids = append(ids, request.op.id)
		request.op.linkWork(batch.owner.op.id)
	}
	batch.owner.op.setNeededBy(ids)
	batch.op.setNeededBy(ids)
}

func (w *workService) executionEnded(end workExecutionEnd) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record := w.executions[end.Execution]
	if record == nil {
		return
	}
	record.state, record.reason = end.State, end.Reason
	w.finishLocked(record)
	if record.active == 0 {
		delete(w.executions, end.Execution)
	}
}

func (w *workService) requestEnded(facts workRequestFacts) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record := w.requestLocked(facts.ID, facts.Trigger)
	if record.endWait != nil {
		record.endWait()
		record.endWait, record.waitKind = nil, ""
	}
	record.facts = facts
	record.state, record.reason = facts.State, facts.Reason
	w.endedRequests = append(w.endedRequests, facts.ID)
	w.finishLocked(record)
	w.pruneRequestsLocked()
}

func (w *workService) requestChanged(facts workRequestFacts) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record := w.requestLocked(facts.ID, facts.Trigger)
	if record.state != "" {
		return // an older notification may follow terminal delivery
	}
	waitKind := ""
	if facts.State == opQueued || facts.State == opBlocked {
		waitKind = waitQueue
		if facts.Blocker != nil {
			switch facts.Blocker.Reason {
			case workBlockedCapacity:
				waitKind = waitCapacity
			case workBlockedDependency:
				waitKind = waitDependency
			case workBlockedWriter:
				waitKind = waitWriter
			case workBlockedPower:
				waitKind = waitPower
			}
		}
	}
	if record.waitKind != waitKind {
		if record.endWait != nil {
			// End the former reason before starting the next; these are
			// demand waits, never CPU attributed to an execution batch.
			record.endWait()
		}
		record.waitKind, record.endWait = waitKind, nil
		if waitKind != "" {
			record.endWait = record.op.resources.waitSpan(waitKind)
		}
	}
	record.facts = facts
	record.op.progress("Updating requested work", "", "conversation", float64(facts.Sealed), float64(facts.Members), !facts.Library)
	record.op.Unblock()
	if facts.Blocker != nil {
		detail := facts.Blocker.Detail
		if detail == "" {
			detail = facts.Blocker.Reason
		}
		record.op.Block(facts.Blocker.Reason, detail)
	}
}

func (w *workService) pruneRequestsLocked() {
	for len(w.endedRequests) > workEndedRequestLimit {
		index := slices.IndexFunc(w.endedRequests, func(id uint64) bool {
			return w.requests[id] == nil || w.requests[id].active == 0
		})
		if index < 0 {
			break // only bounded admitted membership jobs can still be draining
		}
		delete(w.requests, w.endedRequests[index])
		w.endedRequests = slices.Delete(w.endedRequests, index, index+1)
	}
}

func (w *workService) finishLocked(record *workOperation) {
	if record.state == "" || record.active > 0 {
		return
	}
	if record.op.lock() {
		if !opTerminal(record.op.state) {
			record.op.finishLocked(record.state, record.reason, "")
		}
		record.op.unlock(true)
	}
	if record.execution != 0 {
		delete(w.executions, record.execution)
	}
	select {
	case <-record.done:
	default:
		close(record.done)
	}
}

func (w *workService) submit(ctx context.Context, spec workRequestSpec) (uint64, *workOperation, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if err := w.startLoop(); err != nil {
		return 0, nil, err
	}
	id, err := w.runner.Submit(spec)
	if err != nil {
		return 0, nil, err
	}
	w.mu.Lock()
	record := w.requestLocked(id, spec.Trigger)
	w.mu.Unlock()
	return id, record, nil
}

func (w *workService) wait(ctx context.Context, record *workOperation) error {
	select {
	case <-ctx.Done():
		w.runner.Cancel(record.id)
		return ctx.Err()
	case <-record.done:
		w.mu.Lock()
		facts := record.facts
		w.mu.Unlock()
		if facts.State == opSucceeded {
			return nil
		}
		return fmt.Errorf("work request %d %s: %s", facts.ID, facts.State, defaultString(facts.Reason, facts.Detail))
	}
}

// workAdmission makes nested structural helpers share their caller's grant.
// It is only a runtime ownership token; no correctness or completion is cached.
type workAdmission struct {
	authority  *workAuthority
	allocation workAllocation
}
type workAdmissionKey struct{}

func withWorkAdmission(ctx context.Context, a *workAuthority, allocation workAllocation) context.Context {
	return context.WithValue(ctx, workAdmissionKey{}, workAdmission{a, allocation})
}
func workAllocationFrom(ctx context.Context) (workAllocation, bool) {
	value, ok := ctx.Value(workAdmissionKey{}).(workAdmission)
	return value.allocation, ok
}
func (w *workService) call(ctx context.Context, cpus int, fn func(context.Context, workAllocation) error) error {
	if value, ok := ctx.Value(workAdmissionKey{}).(workAdmission); ok && value.authority == w.authority {
		return fn(ctx, value.allocation)
	}
	res := taskResourcesFrom(ctx)
	endWait := res.waitSpan(waitCapacity)
	defer endWait()
	return w.authority.Call(ctx, cpus, cpus, func(ctx context.Context, allocation workAllocation) error {
		endWait()
		defer res.allocate(taskAllocation{CPUSlots: allocation.CPUs})()
		active := res.activeSpan()
		defer active()
		return fn(withWorkAdmission(ctx, w.authority, allocation), allocation)
	})
}

func (op *Op) setWorkControl(stop func()) {
	if !op.lock() {
		return
	}
	op.workControlled = true
	op.stopper = stop
	op.unlock(true)
}
func (op *Op) setNeededBy(ids []OpID) {
	if !op.lock() {
		return
	}
	if opTerminal(op.state) {
		op.reg.mu.Unlock()
		return
	}
	if slices.Equal(op.neededBy, ids) {
		op.reg.mu.Unlock()
		return
	}
	op.neededBy = slices.Clone(ids)
	op.unlock(false)
}
func (op *Op) linkWork(id OpID) {
	if !op.lock() {
		return
	}
	if opTerminal(op.state) {
		op.reg.mu.Unlock()
		return
	}
	if slices.Contains(op.sharedWork, id) {
		op.reg.mu.Unlock()
		return
	}
	op.sharedWork = append(op.sharedWork, id)
	if len(op.sharedWork) > 64 {
		op.sharedWorkOmitted += len(op.sharedWork) - 64
		op.sharedWork = slices.Clone(op.sharedWork[len(op.sharedWork)-64:])
	}
	op.unlock(false)
}

// requestRecent freezes the activity population once. New arrivals become later
// demand; a large rebuild's historical remainder keeps backfill priority.
func (w *workService) requestRecent(ctx context.Context, kinds []string, from, until time.Time, explicit bool) (uint64, *workOperation, error) {
	priority := workPriorityLive
	if explicit {
		priority = workPriorityForeground
	}
	return w.submit(ctx, workRequestSpec{Key: "recent:" + strconv.FormatInt(from.UnixNano(), 10) + ":" + strconv.FormatInt(until.UnixNano(), 10) + ":" + fmt.Sprint(kinds), Scope: "recent", Kinds: kinds, Priority: priority, Explicit: explicit, Trigger: triggerUser, Build: func(ctx context.Context, b *workInventoryBuilder) error {
		return w.server.Catalog.freezeWorkPopulation(ctx, b, from, until, nil)
	}})
}

var errWorkUnavailable = errors.New("population execution is not enabled for this process")
