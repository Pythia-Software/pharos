package archive

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// An ordinary serial maintenance step joins population execution by one
// declaration: no daemon, no scheduler branch. A one-time backfill runs its
// steps under admission until it reports nothing more, then covers later
// requests without running; a task without a pending check runs only for a
// request that asks for a pass.
func TestMaintenanceTaskIsAnOrdinarySerialTask(t *testing.T) {
	steps := 0
	backfill := &maintenanceTask{kind: kindSubstring, phase: "build", once: true, step: func(context.Context, workAllocation) (bool, int64, error) {
		steps++
		return steps < 3, 1, nil
	}}
	refined := 0
	refine := &maintenanceTask{kind: kindRefine, phase: "refine", step: func(context.Context, workAllocation) (bool, int64, error) {
		refined++
		return false, 1, nil
	}}
	h := newWorkHarness(t, 1,
		workDefinition{Kind: backfill.kind, Coarse: true, Task: backfill},
		workDefinition{Kind: refine.kind, Coarse: true, Task: refine})

	first := h.submit(workRequestSpec{Kinds: []string{backfill.kind}, Priority: workPriorityLive})
	h.runner.Pump()
	for range 3 {
		job := h.take(backfill.kind, 0)
		if !job.job.Coarse || job.job.CPUs != 1 {
			t.Fatalf("job %+v", job.job)
		}
		if q := h.request(first); q.State == opSucceeded {
			t.Fatal("the request covered a backfill that still had steps to run")
		}
		job.run(context.Background())
		h.runner.Pump()
	}
	if q := h.request(first); q.State != opSucceeded || steps != 3 {
		t.Fatalf("request %+v after %d steps", q, steps)
	}
	second := h.submit(workRequestSpec{Kinds: []string{backfill.kind}, Priority: workPriorityLive})
	h.runner.Pump()
	if h.pending(backfill.kind) || h.request(second).State != opSucceeded {
		t.Fatalf("a settled backfill ran again: %+v", h.request(second))
	}

	unasked := h.submit(workRequestSpec{Kinds: []string{refine.kind}, Priority: workPriorityLive})
	h.runner.Pump()
	if h.pending(refine.kind) || h.request(unasked).State != opSucceeded || refined != 0 {
		t.Fatal("a refinement nobody asked for ran")
	}
	asked := h.submit(workRequestSpec{Kinds: []string{refine.kind}, Mode: workModePass, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete(refine.kind)
	if q := h.request(asked); q.State != opSucceeded || refined != 1 || h.pending(refine.kind) {
		t.Fatalf("request %+v, refined %d", q, refined)
	}
}

// A requested pass belongs to its request: a step that is blocked or fails
// leaves the request open, and it succeeds only after a pass ran to the end.
func TestMaintenanceRequestedPassSurvivesBlockAndFailure(t *testing.T) {
	outcomes := []error{
		workBlocked(workBlocker{Reason: workBlockedWriter, Detail: "writing estimates are running"}),
		errors.New("transient"),
		nil,
	}
	ran, finished := 0, 0
	refine := &maintenanceTask{kind: kindRefine, phase: "refine", step: func(context.Context, workAllocation) (bool, int64, error) {
		err := outcomes[ran]
		ran++
		if err == nil {
			finished++
		}
		return false, 1, err
	}}
	h := newWorkHarness(t, 1, workDefinition{Kind: refine.kind, Coarse: true, Task: refine})
	id := h.submit(workRequestSpec{Kinds: []string{refine.kind}, Mode: workModePass, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete(refine.kind)
	if q := h.request(id); opTerminal(q.State) {
		t.Fatalf("a blocked pass ended the request: %+v", q)
	}
	h.clock.Advance(workReconcileInterval)
	h.runner.Pump()
	h.complete(refine.kind)
	if q := h.request(id); opTerminal(q.State) {
		t.Fatalf("a failed pass ended the request: %+v", q)
	}
	h.clock.Advance(workRetryDelays[0])
	h.runner.Pump()
	h.complete(refine.kind)
	if q := h.request(id); q.State != opSucceeded || ran != 3 || finished != 1 {
		t.Fatalf("request %+v after %d steps, %d finished", q, ran, finished)
	}
}

// The findings algorithm is the one the request asked for; demand its
// pending check started only measures.
func TestMaintenanceFindingsModeComesFromTheRequest(t *testing.T) {
	var modes []findingsMode
	task := &maintenanceTask{kind: kindFindings, phase: "measure", probe: func(context.Context) (bool, error) { return true, nil },
		step: func(ctx context.Context, _ workAllocation) (bool, int64, error) {
			modes = append(modes, findingsModeFrom(ctx))
			return false, 1, nil
		}}
	h := newWorkHarness(t, 1, workDefinition{Kind: kindFindings, Coarse: true, Task: task})
	for _, mode := range []string{workModeForce, "", workModeAuto} {
		id := h.submit(workRequestSpec{Kinds: []string{kindFindings}, Mode: mode, Priority: workPriorityLive})
		h.runner.Pump()
		h.complete(kindFindings)
		if q := h.request(id); q.State != opSucceeded {
			t.Fatalf("%q: %+v", mode, q)
		}
	}
	if !slices.Equal(modes, []findingsMode{findingsForce, findingsMeasure, findingsAuto}) {
		t.Fatalf("modes %v", modes)
	}
}

// Steady writes keep a coarse task's pending check true. A library request
// is still one completed pass: it does not repeat whole-library work until
// the library is quiet, and a request that arrives while a pass runs gets a
// pass of its own rather than the one that read older inputs.
func TestMaintenanceCoarseRequestIsOnePassUnderSteadyWrites(t *testing.T) {
	passes := 0
	task := &maintenanceTask{kind: kindTools, phase: "rollup", probe: func(context.Context) (bool, error) { return true, nil },
		step: func(context.Context, workAllocation) (bool, int64, error) {
			passes++
			return false, 1, nil
		}}
	h := newWorkHarness(t, 2, workDefinition{Kind: kindTools, Coarse: true, Task: task})
	first := h.submit(workRequestSpec{Kinds: []string{kindTools}, Priority: workPriorityLive})
	h.runner.Pump()
	running := h.take(kindTools, 0)
	second := h.submit(workRequestSpec{Kinds: []string{kindTools}, Priority: workPriorityLive})
	h.runner.Pump()
	running.run(context.Background())
	h.runner.Pump()
	if q := h.request(first); q.State != opSucceeded || passes != 1 {
		t.Fatalf("first request %+v after %d passes", q, passes)
	}
	if q := h.request(second); opTerminal(q.State) {
		t.Fatalf("a pass that started before the request covered it: %+v", q)
	}
	h.complete(kindTools)
	if q := h.request(second); q.State != opSucceeded || passes != 2 || h.pending(kindTools) {
		t.Fatalf("second request %+v after %d passes", q, passes)
	}
}

// Findings need current tools, tools need Git, Git needs identities. A
// findings request, however it was started, brings its pending
// prerequisites current first, in that order.
func TestMaintenanceStagesDeclareTheirPrerequisites(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	defs, err := s.maintenanceWorkDefinitions(nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	chain := []workDefinition{}
	for _, def := range defs {
		if !slices.Contains([]string{kindIdentities, kindGit, kindTools, kindFindings}, def.Kind) {
			continue
		}
		task, kind, pending := def.Task.(*maintenanceTask), def.Kind, true
		task.probe = func(context.Context) (bool, error) { return pending, nil }
		task.step = func(context.Context, workAllocation) (bool, int64, error) {
			order, pending = append(order, kind), false
			return false, 1, nil
		}
		chain = append(chain, def)
	}
	h := newWorkHarness(t, 4, chain...)
	id := h.submit(workRequestSpec{Kinds: []string{kindFindings}, Priority: workPriorityLive})
	h.runner.Pump()
	for len(h.jobs) > 0 {
		if len(h.jobs) != 1 {
			t.Fatalf("stages ran together: %+v", h.jobs)
		}
		h.complete(h.jobs[0].job.Kind)
	}
	if want := []string{kindIdentities, kindGit, kindTools, kindFindings}; !slices.Equal(order, want) || h.request(id).State != opSucceeded {
		t.Fatalf("order %v, request %+v", order, h.request(id))
	}
	for _, mode := range []string{workModePass, workModeForce} {
		t.Run(mode+" uses committed inputs", func(t *testing.T) {
			order = nil
			for _, def := range chain {
				task, kind := def.Task.(*maintenanceTask), def.Kind
				task.probe = func(context.Context) (bool, error) { return true, nil }
				task.step = func(context.Context, workAllocation) (bool, int64, error) {
					order = append(order, kind)
					if kind == kindGit {
						return false, 0, workPermanent(errors.New("git unavailable"))
					}
					return false, 1, nil
				}
			}
			h := newWorkHarness(t, 4, chain...)
			id := h.submit(workRequestSpec{Kinds: []string{kindFindings}, Mode: mode, Priority: workPriorityLive})
			h.runner.Pump()
			h.complete(kindFindings)
			if !slices.Equal(order, []string{kindFindings}) || h.request(id).State != opSucceeded {
				t.Fatalf("replayed prerequisites: %v, request %+v", order, h.request(id))
			}
		})
	}
}

// Whether a chain is explicit comes from what started it: a person (a
// manual index, Capture & index, a CLI command) or Run now, never from the
// shape of the call.
func TestWorkCallerExplicitFollowsTheOperationAncestry(t *testing.T) {
	c, config := testCatalog(t)
	s := testServer(t, config, c)
	for _, test := range []struct {
		trigger  string
		explicit bool
	}{{triggerUser, true}, {triggerCLI, true}, {triggerSchedule, false}, {triggerRead, false}} {
		ctx, root := s.begin(t.Context(), OperationSpec{Kind: "index.captures", Trigger: test.trigger})
		ctx, chain := root.childFrom(ctx, OperationSpec{Kind: "analysis.chain", Trigger: triggerChained})
		if got := workCallerExplicit(ctx); got != test.explicit {
			t.Errorf("%s: explicit %v", test.trigger, got)
		}
		if !workCallerExplicit(withBackgroundExplicit(ctx, true)) {
			t.Errorf("%s: Run now was not explicit", test.trigger)
		}
		chain.Finish(nil)
		root.Finish(nil)
	}
	if workCallerExplicit(t.Context()) {
		t.Error("a call without an originating action was explicit")
	}
}

// Pausing background updates (or turning indexing off) holds scheduled
// analyses, but not what the classic daemons kept current regardless: the
// stored projections, search indexes and label query facts, and anything a
// page started. Their coordinator demand coalesces, so ticks while paused do
// not fill the request queue.
func TestMaintainWorkKeepsProjectionsAndReadsRunningWhilePaused(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	w := s.work
	w.once.Do(func() {})
	w.runner.SetPaused(true)
	execTx(t, c.DB, `INSERT INTO session_shape_dirty(conversation_id,revision) VALUES('cv-gone','r1')`,
		`INSERT INTO message_label_facts_dirty(conversation_id) VALUES('cv-gone')`)
	analysis, _, err := w.submit(t.Context(), workRequestSpec{Kinds: []string{kindFindings}, Mode: workModeAuto, Priority: workPriorityLive, Trigger: triggerChained})
	if err != nil {
		t.Fatal(err)
	}
	c.refreshToolRollup()
	for range 3 {
		s.submitPendingMaintenance(t.Context(), w.routing())
		s.submitPendingFacts(t.Context())
	}
	in := w.runner.Inspect()
	if len(in.Requests) >= workQueuedRequestLimit {
		t.Fatalf("paused ticks filled the queue: %d requests", len(in.Requests))
	}
	keys := map[string]int{}
	for _, q := range in.Requests {
		keys[q.Key]++
	}
	for _, key := range []string{"maintenance:" + kindSessionShape + ":live", "maintenance:" + kindLabelFacts + ":backfill", "read:" + kindTools} {
		if keys[key] != 1 {
			t.Fatalf("%s: %d requests in %v", key, keys[key], keys)
		}
	}
	w.runner.Pump()
	in = w.runner.Inspect()
	for _, q := range slices.Concat(in.Requests, in.Ended) {
		paused := q.Blocker != nil && q.Blocker.Reason == workBlockedPaused
		if paused != (q.ID == analysis) {
			t.Errorf("request %d (%s %v): paused %v", q.ID, q.Key, q.Kinds, paused)
		}
	}
}

// A conversation whose projection keeps failing keeps its dirty row. The
// coordinator's pending check leaves it to the failure's backoff instead of
// submitting demand that selects nothing on every tick (which also kept
// caches from warming). It rechecks it once per longest retry delay, so an
// edited conversation is reread, and other dirty work is pending at once.
func TestMaintenanceSkipsDirtyConversationsHeldByFailures(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	w := s.work
	w.once.Do(func() {})
	routing := w.routing()
	execTx(t, c.DB, `INSERT INTO session_shape_dirty(conversation_id,revision) VALUES('cv-bad','r1')`)
	fail := func(f workFailure) {
		w.runner.mu.Lock()
		defer w.runner.mu.Unlock()
		f.Kind, f.Unit = kindSessionShape, workMember{ID: "cv-bad", Revision: "c1"}
		w.runner.byKind[kindSessionShape].failures["cv-bad\x00c1"] = &f
	}
	pending := func() bool { return w.dirtyPending(t.Context(), c, routing, kindSessionShape, "session_shape_dirty") }
	if !pending() {
		t.Fatal("a dirty conversation with no failure was not pending")
	}
	fail(workFailure{Attempts: 1, NextAt: time.Now().Add(time.Hour)})
	if !pending() || pending() {
		t.Fatal("a conversation in backoff was not rechecked exactly once")
	}
	fail(workFailure{Attempts: 3, Exhausted: true})
	if pending() {
		t.Fatal("an exhausted conversation resubmitted demand before the longest retry delay")
	}
	routing.mu.Lock()
	routing.rechecked[kindSessionShape] = routing.rechecked[kindSessionShape].Add(-workRetryDelays[len(workRetryDelays)-1])
	routing.mu.Unlock()
	if !pending() {
		t.Fatal("an exhausted conversation was not rechecked after the longest retry delay")
	}
	execTx(t, c.DB, `INSERT INTO session_shape_dirty(conversation_id,revision) VALUES('cv-new','r1')`)
	if !pending() {
		t.Fatal("new dirty work waited behind a held conversation")
	}
	execTx(t, c.DB, `DELETE FROM session_shape_dirty`)
	w.runner.mu.Lock()
	w.runner.byKind[kindSessionShape].full = true
	w.runner.mu.Unlock()
	execTx(t, c.DB, `INSERT INTO session_shape_dirty(conversation_id,revision) VALUES('cv-new','r2')`)
	if pending() {
		t.Fatal("a kind whose failure table is full, held until Retry, was pending")
	}
}

// scheduledTestServer is a scheduled-mode server whose task table is defs,
// so the work service's own submit/await paths run against fake adapters.
func scheduledTestServer(t *testing.T, defs ...workDefinition) *Server {
	t.Helper()
	c, config := testCatalog(t)
	config.WorkEngine, config.BackgroundCPUs = "scheduled", 2
	s := testServer(t, config, c)
	useWorkDefinitions(t, s, defs...)
	return s
}

// useWorkDefinitions replaces the server's runner with one of just defs.
func useWorkDefinitions(t *testing.T, s *Server, defs ...workDefinition) {
	t.Helper()
	w := s.work
	runner, err := newWorkRunner(s.life.ctx, workSystemClock{}, w.authority, workHooks{Start: w.start, ExecutionEnded: w.executionEnded,
		RequestEnded: w.requestEnded, RequestChanged: w.requestChanged, BatchEnded: w.batchEnded, DemandChanged: w.demandChanged}, defs)
	if err != nil {
		t.Fatal(err)
	}
	w.runner = runner
}

// runLibrary waits for every kind's own request: one that fails is
// reported without holding back the others, and a caller that gives up
// withdraws its demand.
func TestRunLibraryReportsEachKindAndWithdrawsOnCancel(t *testing.T) {
	ran := map[string]int{}
	entered := make(chan struct{}, 1)
	task := func(kind string, step func(context.Context) error) workDefinition {
		return workDefinition{Kind: kind, Coarse: true, Task: &maintenanceTask{kind: kind, phase: "pass",
			step: func(ctx context.Context, _ workAllocation) (bool, int64, error) {
				ran[kind]++
				return false, 1, step(ctx)
			}}}
	}
	s := scheduledTestServer(t,
		task(kindTools, func(context.Context) error { return nil }),
		task(kindGit, func(context.Context) error { return workPermanent(errors.New("repository unreadable")) }),
		task(kindFindings, func(ctx context.Context) error {
			entered <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}))
	w := s.work
	err := w.runLibraryMode(t.Context(), []string{kindTools, kindGit}, false, triggerChained, workModePass)
	if err == nil || !strings.Contains(err.Error(), kindGit) || strings.Contains(err.Error(), kindTools) || ran[kindTools] != 1 || ran[kindGit] != 1 {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.runLibraryMode(ctx, []string{kindFindings}, false, triggerChained, workModePass) }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait returned %v", err)
	}
	waitUntil(t, 5*time.Second, "the withdrawn request to end", func() bool { return len(w.runner.Inspect().Requests) == 0 })
}

// Stop and Pause hold a request that would then never end. Its awaiter
// withdraws it, so the Search item, the chain and the background loop move
// on (and Run now does not wait behind them); the work stays pending. Demand
// someone asked for (Run now, the refresh after their sync) is not paused.
func TestAwaitedRequestsWithdrawWhenHeld(t *testing.T) {
	override(t, &workAwaitPoll, 5*time.Millisecond)
	entered, release := make(chan string, 8), make(chan struct{})
	task := func(kind string) *maintenanceTask {
		calls := 0
		return &maintenanceTask{kind: kind, phase: "pass", probe: func(context.Context) (bool, error) { return true, nil },
			step: func(ctx context.Context, _ workAllocation) (bool, int64, error) {
				calls++
				if calls > 1 {
					return false, 1, nil
				}
				entered <- kind
				select {
				case <-release:
					return true, 1, nil
				case <-ctx.Done():
					return false, 0, ctx.Err()
				}
			}}
	}
	s := scheduledTestServer(t)
	c, w := s.Catalog, s.work
	useWorkDefinitions(t, s,
		workDefinition{Kind: kindSessionShape, Coarse: true, Task: projectionHeldTask{catalog: c, workTask: task(kindSessionShape)}},
		workDefinition{Kind: kindGit, Coarse: true, Task: task(kindGit)},
		workDefinition{Kind: kindTools, Coarse: true, Task: task(kindTools)},
		workDefinition{Kind: kindFindings, Coarse: true, Task: task(kindFindings)})
	within := func(what string, run func() error) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- run() }()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not return", what)
			return nil
		}
	}
	ended := func() {
		t.Helper()
		waitUntil(t, 5*time.Second, "the withdrawn request to end", func() bool { return len(w.runner.Inspect().Requests) == 0 })
	}

	// Stopping the Search view refresh while its projection runs.
	err := within("the stopped Search item", func() error {
		go func() { <-entered; c.stopLibraryRefresh() }()
		return w.backgroundSearchWork(t.Context(), c)
	})
	if err != nil {
		t.Fatalf("a stopped Search item: %v", err)
	}
	ended()
	c.resumeLibraryRefresh()

	// Pausing while a scheduled pass waits for its stage.
	err = within("the paused pass", func() error {
		go func() { <-entered; w.runner.SetPaused(true); release <- struct{}{} }()
		return w.runLibraryMode(t.Context(), []string{kindTools}, false, triggerChained, workModePass)
	})
	if !errors.Is(err, errWorkHeld) {
		t.Fatalf("a paused pass returned %v", err)
	}
	// A standalone item (fingerprints, writing refinement) records the
	// withdrawal as neither a failure nor a success; one that also failed
	// still records its failure.
	c.recordBackgroundRun(t.Context(), "writing-refine", time.Now(), true, nil)
	succeeded := c.backgroundRuns(t.Context())["writing-refine"].succeeded
	c.recordBackgroundRun(t.Context(), "writing-refine", time.Now(), true, err)
	if run := c.backgroundRuns(t.Context())["writing-refine"]; run.err != "" || run.succeeded != succeeded || run.finished == "" {
		t.Fatalf("a withdrawn item recorded %+v", run)
	}
	if backgroundWithdrawn(errors.Join(err, errors.New("boom"))) {
		t.Fatal("a held kind hid another kind's failure")
	}
	ended()
	if err := within("Run now while paused", func() error {
		return w.runLibraryMode(t.Context(), []string{kindTools}, true, triggerChained, workModePass)
	}); err != nil {
		t.Fatalf("Run now while paused: %v", err)
	}

	// The refresh after someone's sync runs while paused; an automatic one
	// is held, and returns.
	go func() {
		for range 2 {
			<-entered
			release <- struct{}{}
		}
	}()
	ctx, op := s.begin(t.Context(), OperationSpec{Kind: "analysis.refresh", Trigger: triggerUser})
	err = within("the refresh after a sync", func() error { return w.refreshAnalysesWork(ctx) })
	op.Finish(err)
	if err != nil {
		t.Fatalf("the refresh after a sync, paused: %v", err)
	}
	if err := within("an automatic refresh", func() error { return w.refreshAnalysesWork(t.Context()) }); !errors.Is(err, errWorkHeld) {
		t.Fatalf("an automatic refresh, paused: %v", err)
	}
	ended()
	// A separate model pass (for example Terminal labels/agent-events) may
	// hold its invocation lease for hours. Yield the awaited request without
	// charging failures or holding the pass/index schedule behind that owner.
	for _, kind := range []string{kindLabels, kindAgentEvents} {
		task := &maintenanceTask{kind: kind, phase: "pass", step: func(context.Context, workAllocation) (bool, int64, error) {
			return false, 0, workBlocked(workBlocker{Reason: workBlockedWriter, Detail: "another analysis pass owns the invocation lock"})
		}}
		other := scheduledTestServer(t, workDefinition{Kind: kind, Coarse: true, Task: task})
		err := within("a contended model pass", func() error {
			return other.work.runLibraryMode(t.Context(), []string{kind}, false, triggerChained, workModePass)
		})
		if !errors.Is(err, errWorkHeld) {
			t.Fatalf("%s contention should yield: %v", kind, err)
		}
		waitUntil(t, 5*time.Second, "the contended request to withdraw", func() bool { return len(other.work.runner.Inspect().Requests) == 0 })
		for _, request := range other.work.runner.Inspect().Ended {
			if request.Failed != 0 || request.Exhausted != 0 {
				t.Fatalf("lease contention charged a failure: %+v", request)
			}
		}
	}
	// A model request also includes writing prerequisites. Their writer
	// waits are ordinary contention, not another model invocation owner.
	writer := &workBlocker{Reason: workBlockedWriter}
	if blocker := workRequestHold(workRequestFacts{State: opBlocked, Kinds: []string{kindLabels}, Blocker: writer,
		Tasks: []workRequestTaskFacts{{Kind: kindAuthorship, State: opBlocked, Blocker: writer}}}); blocker != nil {
		t.Fatal("a model request must wait for its writing prerequisite")
	}
	if blocker := workRequestHold(workRequestFacts{State: opBlocked, Kinds: []string{kindGit}, Blocker: &workBlocker{Reason: workBlockedWriter}}); blocker != nil {
		t.Fatal("an ordinary writer wait must not withdraw unrelated work")
	}
}

// A page's writing request records an interruption, such as recent
// conversations that keep changing, as no error, so a later page retries.
func TestPageAuthorshipOutcome(t *testing.T) {
	for _, test := range []struct {
		facts     workRequestFacts
		err       error
		failure   bool
		succeeded bool
	}{
		{facts: workRequestFacts{State: opSucceeded}, succeeded: true},
		{facts: workRequestFacts{State: opCancelled}},
		{facts: workRequestFacts{State: opInterrupted, Reason: workInputsKeepChanging}},
		{err: errWorkHeld},
		{facts: workRequestFacts{State: opFailed, Reason: workUnitsFailed}, failure: true},
	} {
		failure, succeeded := authorshipRequestOutcome(test.facts, test.err)
		if (failure != "") != test.failure || succeeded != test.succeeded {
			t.Errorf("%+v %v: failure %q succeeded %v", test.facts, test.err, failure, succeeded)
		}
	}
}

// A page that asks for writing estimates gets its own request. The pages'
// state follows it as it followed the classic pass: running while it is
// outstanding, lastError after a failure, estimatesAttempted after a
// success. A stopped parent withdraws only its own request; the execution it
// shared keeps running for another request.
func TestPageAuthorshipRequestKeepsClassicState(t *testing.T) {
	outcomes := make(chan error)
	entered := make(chan struct{}, 4)
	task := &fakeConversationTask{entered: entered, outcomes: outcomes}
	s := scheduledTestServer(t, workDefinition{Kind: kindAuthorship, Task: task})
	c := s.Catalog
	seedLabelingCatalog(t, c)
	execTx(t, c.DB, `UPDATE messages SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
	state := func() (bool, string, bool) {
		c.authorship.mu.Lock()
		defer c.authorship.mu.Unlock()
		return c.authorship.running, c.authorship.lastError, c.authorship.estimatesAttempted
	}
	settled := func() {
		waitUntil(t, 5*time.Second, "the page request's waiter", func() bool { running, _, _ := state(); return !running })
	}

	if !c.startAuthorshipWork(nil) || c.startAuthorshipWork(nil) {
		t.Fatal("a page started no request, or a second while one is outstanding")
	}
	<-entered
	outcomes <- workPermanent(errors.New("classifier unavailable"))
	settled()
	if _, failure, attempted := state(); failure == "" || attempted {
		t.Fatalf("failure %q attempted %v", failure, attempted)
	}
	s.work.runner.Retry(kindAuthorship)

	// Another request shares the execution a stopped index's request joined.
	other, otherRecord, err := s.work.submit(t.Context(), workRequestSpec{Kinds: []string{kindAuthorship}, Priority: workPriorityLive,
		Build: func(ctx context.Context, b *workInventoryBuilder) error {
			return c.freezeWorkPopulation(ctx, b, c.clock().Add(-maintenanceRecent), c.clock(), nil)
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	parent, stop := context.WithCancel(t.Context())
	if !c.startAuthorshipWork(parent) {
		t.Fatal("a chained pass did not start")
	}
	stop()
	settled()
	if running, failure, _ := state(); running || failure != "" {
		t.Fatalf("a withdrawn request was a failure: %q", failure)
	}
	select {
	case <-otherRecord.done:
		t.Fatal("withdrawing one request ended another's shared execution")
	default:
	}
	outcomes <- nil
	if err := s.work.wait(t.Context(), otherRecord); err != nil {
		t.Fatalf("request %d: %v", other, err)
	}

	if !c.startAuthorshipWork(nil) {
		t.Fatal("a page could not retry after an interruption")
	}
	<-entered
	outcomes <- nil
	settled()
	if _, failure, attempted := state(); failure != "" || !attempted {
		t.Fatalf("success left failure %q attempted %v", failure, attempted)
	}

	// A library with no recent activity still makes the classic pass's
	// bounded historical progress through the real adapter, without the page
	// draining all of history.
	override(t, &authorshipQuickLimit, 1)
	s = scheduledTestServer(t)
	c = s.Catalog
	defs, err := s.workDefinitions(s.work.authority)
	if err != nil {
		t.Fatal(err)
	}
	useWorkDefinitions(t, s, slices.DeleteFunc(defs, func(def workDefinition) bool { return def.Kind != kindAuthorship })...)
	seedLabelingCatalog(t, c)
	execTx(t, c.DB, `UPDATE messages SET created_at='2026-01-01T00:00:00.000Z'`)
	pending := func() (conversations int) {
		t.Helper()
		if err := c.DB.QueryRow(`SELECT COUNT(DISTINCT m.conversation_id) FROM authorship_pending p JOIN messages m ON m.id=p.message_id`).Scan(&conversations); err != nil {
			t.Fatal(err)
		}
		return conversations
	}
	before := pending()
	if before < 2 {
		t.Fatalf("the fixture has %d conversations awaiting estimates", before)
	}
	if !c.startAuthorshipWork(nil) {
		t.Fatal("a page started no request on a historical library")
	}
	settled()
	if _, failure, attempted := state(); failure != "" || !attempted {
		t.Fatalf("historical pass: failure %q attempted %v", failure, attempted)
	}
	if after := pending(); after != before-1 {
		t.Fatalf("one page pass estimated %d of %d historical conversations, want its bounded share of 1", before-after, before)
	}
}

// fakeConversationTask selects a request's members in one batch and commits
// them, or fails, when the test says so.
type fakeConversationTask struct {
	entered  chan struct{}
	outcomes chan error
}

func (f *fakeConversationTask) Next(_ context.Context, pop *workPopulation) (workSelection, error) {
	members := populationWorkMembers(pop, workDomainWindow)
	if len(members) == 0 {
		return workSelection{}, nil
	}
	return workSelection{Units: members}, nil
}

func (f *fakeConversationTask) RunBatch(ctx context.Context, selection workSelection, _ workAllocation) (workBatchResult, error) {
	f.entered <- struct{}{}
	select {
	case err := <-f.outcomes:
		if err != nil {
			return workBatchResult{}, err
		}
		return workBatchResult{Committed: selection.Units}, nil
	case <-ctx.Done():
		return workBatchResult{}, ctx.Err()
	}
}

// Cache warming takes a free slot or skips the tick; it never waits behind
// busy slots, so the coordinator keeps submitting demand.
func TestMaintenanceCacheWarmNeverWaitsForASlot(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine, config.BackgroundCPUs = "scheduled", 1
	s := testServer(t, config, c)
	w := s.work
	w.once.Do(func() {})
	cpus, ok, _ := w.authority.tryCPUs(1, 1, 0, false)
	if !ok {
		t.Fatal("no slot to occupy")
	}
	defer w.authority.releaseCPUs(cpus)
	routing := w.routing()
	if !waitFor(10*time.Second, func() { s.warmCachesNow(t.Context(), routing) }) {
		t.Fatal("the coordinator waited for a busy slot to warm caches")
	}
	routing.mu.Lock()
	warmed := !routing.warmedAt.IsZero()
	routing.mu.Unlock()
	if !warmed {
		t.Fatal("an idle tick did not consider warming")
	}
}

func TestMaintenanceRecentChainYieldsToActiveRefinement(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	s.work.once.Do(func() {})
	execTx(t, c.DB, `INSERT INTO sync_deferred(name,generation,completed_generation,changed_at) VALUES('authorship',9,0,'2026-11-01T00:00:00Z') ON CONFLICT(name) DO UPDATE SET generation=9,completed_generation=0`)
	c.authorship.mu.Lock()
	c.authorship.refining = true
	c.authorship.mu.Unlock()
	t.Cleanup(func() {
		c.authorship.mu.Lock()
		c.authorship.refining = false
		c.authorship.mu.Unlock()
	})
	for _, name := range []string{"authorship", "labels", "agent_events"} {
		if err := s.routePopulationStage(t.Context(), name, stageKinds[name], true); err != nil {
			t.Fatal(err)
		}
	}
	if requests := s.work.runner.Inspect().Requests; len(requests) != 0 {
		t.Fatalf("the chain waited for refinement through a request: %+v", requests)
	}
	if generation, err := c.deferredPendingGeneration(t.Context(), "authorship"); err != nil || generation != 9 {
		t.Fatalf("yielding acknowledged pending writing: %d %v", generation, err)
	}
}
