package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Deterministic runner tests: jobs the runner admits wait in the harness
// until the test runs them, and time moves only when the test advances the
// clock.

type workTestClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []workTestTimer
	armed  chan time.Duration
}

type workTestTimer struct {
	at time.Time
	ch chan time.Time
}

func newWorkTestClock() *workTestClock {
	return &workTestClock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), armed: make(chan time.Duration, 64)}
}

func (c *workTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *workTestClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.timers = append(c.timers, workTestTimer{at: c.now.Add(d), ch: ch})
	}
	select {
	case c.armed <- d:
	default:
	}
	return ch
}

func (c *workTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, timer := range c.timers {
		if timer.at.After(c.now) {
			kept = append(kept, timer)
			continue
		}
		timer.ch <- c.now
	}
	c.timers = kept
}

type workTestJob struct {
	job workJob
	run func(context.Context)
}

type workHarness struct {
	t         *testing.T
	clock     *workTestClock
	authority *workAuthority
	runner    *workRunner
	jobs      []workTestJob
	refuse    error
	ended     []workExecutionEnd
	batches   []workBatchEnd
	demands   []workDemand
	changes   []workRequestFacts
	// hooks is the delivery order: "batch <kind>", "execution <kind>",
	// "request <id>".
	hooks []string
}

func newWorkHarness(t *testing.T, cpus int, defs ...workDefinition) *workHarness {
	t.Helper()
	h := &workHarness{t: t, clock: newWorkTestClock(), authority: newWorkAuthority(cpus)}
	runner, err := newWorkRunner(context.Background(), h.clock, h.authority, workHooks{
		Start: func(job workJob, run func(context.Context)) error {
			if h.refuse != nil {
				return h.refuse
			}
			h.jobs = append(h.jobs, workTestJob{job: job, run: run})
			return nil
		},
		ExecutionEnded: func(end workExecutionEnd) {
			h.ended = append(h.ended, end)
			h.hooks = append(h.hooks, "execution "+end.Kind)
		},
		RequestEnded: func(facts workRequestFacts) { h.hooks = append(h.hooks, fmt.Sprint("request ", facts.ID)) },
		BatchEnded: func(end workBatchEnd) {
			h.batches = append(h.batches, end)
			h.hooks = append(h.hooks, "batch "+end.Job.Kind)
		},
		DemandChanged:  func(demand workDemand) { h.demands = append(h.demands, demand) },
		RequestChanged: func(facts workRequestFacts) { h.changes = append(h.changes, facts) },
	}, defs)
	if err != nil {
		t.Fatal(err)
	}
	h.runner = runner
	return h
}

func (h *workHarness) submit(spec workRequestSpec) uint64 {
	h.t.Helper()
	id, err := h.runner.Submit(spec)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// take removes the pending job for kind (or for a request's membership).
func (h *workHarness) take(kind string, request uint64) workTestJob {
	h.t.Helper()
	for i, job := range h.jobs {
		if job.job.Kind == kind && job.job.Request == request {
			h.jobs = slices.Delete(h.jobs, i, i+1)
			return job
		}
	}
	h.t.Fatalf("no pending %s job (request %d); pending: %+v", kind, request, h.jobs)
	return workTestJob{}
}

func (h *workHarness) pending(kind string) bool {
	return slices.ContainsFunc(h.jobs, func(job workTestJob) bool { return job.job.Kind == kind && job.job.Request == 0 })
}

// complete runs the pending batch of kind and pumps.
func (h *workHarness) complete(kind string) {
	h.t.Helper()
	h.take(kind, 0).run(context.Background())
	h.runner.Pump()
}

// build runs a request's pending membership read and pumps.
func (h *workHarness) build(request uint64) {
	h.t.Helper()
	for _, job := range h.jobs {
		if job.job.Request == request {
			h.take(job.job.Kind, request).run(context.Background())
			h.runner.Pump()
			return
		}
	}
	h.t.Fatalf("request %d has no membership job", request)
}

func (h *workHarness) request(id uint64) workRequestFacts {
	h.t.Helper()
	in := h.runner.Inspect()
	for _, q := range slices.Concat(in.Requests, in.Ended) {
		if q.ID == id {
			return q
		}
	}
	h.t.Fatalf("no request %d", id)
	return workRequestFacts{}
}

func (h *workHarness) task(kind string) workTaskFacts {
	for _, task := range h.runner.Inspect().Tasks {
		if task.Kind == kind {
			return task
		}
	}
	h.t.Fatalf("no task %s", kind)
	return workTaskFacts{}
}

func workMembers(members ...workMember) func(context.Context, *workInventoryBuilder) error {
	return func(_ context.Context, b *workInventoryBuilder) error {
		for _, m := range members {
			if err := b.Add(m.ID, m.Revision); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestWorkOptionalPrerequisitesAreFrozenForEachRequest(t *testing.T) {
	enabled := false
	writing, labels, facts := newFakeWorkDomain("u@1"), newFakeWorkDomain("u@1"), newFakeWorkDomain("u@1")
	definition := workDefinition{Kind: "maintenance.labels.query", Task: facts, Dependencies: []string{"analysis.authorship", "analysis.labels"},
		DependenciesForRequest: func(workRequestSpec) []string {
			if enabled {
				return []string{"analysis.authorship", "analysis.labels"}
			}
			return []string{"analysis.authorship"}
		}}
	h := newWorkHarness(t, 2, workDefinition{Kind: "analysis.authorship", Task: writing}, workDefinition{Kind: "analysis.labels", Task: labels}, definition)
	first := h.submit(workRequestSpec{Kinds: []string{definition.Kind}, Priority: workPriorityLive})
	h.runner.Pump()
	if h.pending("analysis.labels") {
		t.Fatal("disabled optional input was required")
	}
	enabled = true
	h.complete("analysis.authorship")
	h.complete("maintenance.labels.query")
	if h.request(first).State != opSucceeded {
		t.Fatal("a request's accepted policy changed after submission")
	}
	writing.set("u", "2")
	labels.set("u", "2")
	facts.set("u", "2")
	second := h.submit(workRequestSpec{Kinds: []string{definition.Kind}, Scope: "recent", Priority: workPriorityForeground, Build: workMembers(workMember{ID: "u", Revision: "2"})})
	h.runner.Pump()
	h.build(second)
	h.complete("analysis.authorship")
	if !h.pending("analysis.labels") || h.pending("maintenance.labels.query") {
		t.Fatal("the newly enabled producer did not gate its consumer")
	}
	h.complete("analysis.labels")
	if !h.pending("maintenance.labels.query") {
		t.Fatal("the consumer did not follow its optional producer")
	}
	h.runner.InterruptKinds([]string{definition.Kind}, "policy changed")
	if result := h.request(second); result.State != opInterrupted || result.Reason != "configuration-changed" || result.Scope != "recent" {
		t.Fatalf("changed policy: %+v", result)
	}
	h.complete("maintenance.labels.query")
	definition.DependenciesForRequest = func(workRequestSpec) []string { return []string{"not.declared"} }
	h.runner.byKind[definition.Kind].def = definition
	if _, err := h.runner.Submit(workRequestSpec{Kinds: []string{definition.Kind}, Priority: workPriorityLive}); err == nil {
		t.Fatal("a dynamic edge escaped the static dependency graph")
	}
}

// fakeWorkDomain is a domain with a dirty queue (unit → pending revision).
// Its readiness waits for an upstream domain's dirty mark on the same unit,
// and committing a unit marks its downstream dirty at the same revision.
type fakeWorkDomain struct {
	mu            sync.Mutex
	dirty         map[string]string
	upstream      *fakeWorkDomain
	downstream    *fakeWorkDomain
	batch         int
	cpus, maxCPUs int
	fail          map[string]int // failures left per unit
	blocker       workBlocker    // a domain blocker on everything
	during        func(context.Context)
	superseded    []workMember // reported extra by the next batch
	boundary      bool         // finishes its units even if cancelled during them
	nexts, runs   int
	ran           []string
	grants        []int
}

func newFakeWorkDomain(units ...string) *fakeWorkDomain {
	d := &fakeWorkDomain{dirty: map[string]string{}, batch: 1, fail: map[string]int{}}
	for _, unit := range units {
		id, revision, _ := strings.Cut(unit, "@")
		d.dirty[id] = revision
	}
	return d
}

func (d *fakeWorkDomain) Next(_ context.Context, pop *workPopulation) (workSelection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nexts++
	if d.blocker.Reason != "" {
		return workSelection{Blocker: d.blocker}, nil
	}
	candidates := map[string]bool{}
	for id := range d.dirty {
		candidates[id] = true
	}
	if d.upstream != nil {
		d.upstream.mu.Lock()
		defer d.upstream.mu.Unlock()
		for id := range d.upstream.dirty {
			candidates[id] = true
		}
	}
	sel := workSelection{CPUs: d.cpus, MaxCPUs: d.maxCPUs}
	var waiting workBlocker
	for _, id := range slices.Sorted(maps.Keys(candidates)) {
		if !pop.Library() {
			if _, ok := pop.Eligible(id); !ok {
				continue
			}
		}
		if d.upstream != nil {
			if _, ok := d.upstream.dirty[id]; ok {
				waiting = workBlocker{Reason: workBlockedDependency, DependsOn: "upstream", Detail: id}
				continue
			}
		}
		revision, ok := d.dirty[id]
		if !ok || pop.Excluded(workMember{id, revision}) {
			continue
		}
		sel.Units = append(sel.Units, workMember{id, revision})
		if len(sel.Units) == d.batch {
			break
		}
	}
	if len(sel.Units) == 0 {
		return workSelection{Blocker: waiting}, nil
	}
	return sel, nil
}

func (d *fakeWorkDomain) RunBatch(ctx context.Context, sel workSelection, allocation workAllocation) (workBatchResult, error) {
	d.mu.Lock()
	d.runs++
	d.grants = append(d.grants, allocation.CPUs)
	for _, m := range sel.Units {
		d.ran = append(d.ran, m.ID)
	}
	during := d.during
	d.mu.Unlock()
	if during != nil {
		during(ctx)
	}
	if err := ctx.Err(); err != nil && !d.boundary {
		return workBatchResult{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	result := workBatchResult{Superseded: d.superseded}
	d.superseded = nil
	var failed []workMember
	for _, m := range sel.Units {
		if d.fail[m.ID] > 0 {
			d.fail[m.ID]--
			failed = append(failed, m)
			continue
		}
		if revision := d.dirty[m.ID]; revision != m.Revision {
			result.Superseded = append(result.Superseded, workMember{m.ID, revision})
			continue
		}
		delete(d.dirty, m.ID)
		result.Committed = append(result.Committed, m)
		if d.downstream != nil {
			d.downstream.mu.Lock()
			d.downstream.dirty[m.ID] = m.Revision
			d.downstream.mu.Unlock()
		}
	}
	if len(failed) > 0 {
		return result, workUnitFailed(errors.New("unreadable input"), failed...)
	}
	return result, nil
}

func (d *fakeWorkDomain) set(id, revision string) {
	d.mu.Lock()
	d.dirty[id] = revision
	d.mu.Unlock()
}

func workPipeline(producer, consumer []string) (*fakeWorkDomain, *fakeWorkDomain, []workDefinition) {
	authorship, labels := newFakeWorkDomain(producer...), newFakeWorkDomain(consumer...)
	authorship.downstream, labels.upstream = labels, authorship
	return authorship, labels, []workDefinition{
		{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: labels},
		{Kind: "analysis.authorship", Task: authorship},
	}
}

func TestWorkTaskTableIsValidatedAndOrdered(t *testing.T) {
	task := newFakeWorkDomain()
	for name, defs := range map[string][]workDefinition{
		"unregistered kind":  {{Kind: "analysis.nonsense", Task: task}},
		"missing adapter":    {{Kind: "analysis.labels"}},
		"duplicate kind":     {{Kind: "analysis.labels", Task: task}, {Kind: "analysis.labels", Task: task}},
		"unknown dependency": {{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: task}},
		"cycle": {{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: task},
			{Kind: "analysis.authorship", Dependencies: []string{"analysis.labels"}, Task: task}},
	} {
		if _, err := orderWorkDefinitions(defs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ordered, err := orderWorkDefinitions([]workDefinition{
		{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: task},
		{Kind: "maintenance.parallelism", Task: task},
		{Kind: "analysis.authorship", Task: task},
	})
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, def := range ordered {
		kinds = append(kinds, def.Kind)
	}
	if !slices.Equal(kinds, []string{"maintenance.parallelism", "analysis.authorship", "analysis.labels"}) {
		t.Fatalf("order %v", kinds)
	}
	if _, err := newWorkRunner(context.Background(), newWorkTestClock(), newWorkAuthority(1), workHooks{}, ordered); err == nil {
		t.Fatal("a runner without a registry Start hook was accepted")
	}
}

func TestWorkInventoryIsSortedCompactAndCharged(t *testing.T) {
	b := &workInventoryBuilder{limit: 1 << 20}
	for _, m := range []workMember{{"c", "1"}, {"a", "1"}, {"b", "2"}, {"a", "1"}} {
		if err := b.Add(m.ID, m.Revision); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := b.freeze(time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inv.members, []workMember{{"a", "1"}, {"b", "2"}, {"c", "1"}}) {
		t.Fatalf("members %v", inv.members)
	}
	if inv.bytes < 16*workMemberSize {
		t.Fatalf("charged %d bytes, less than the allocated slice capacity", inv.bytes)
	}
	pop := &workPopulation{inventory: inv, excluded: map[string]string{"b": "2"}}
	if got := pop.Page("", 2); !slices.Equal(got, inv.members[:2]) {
		t.Errorf("first page %v", got)
	}
	if got := pop.Page("a", 5); !slices.Equal(got, inv.members[1:]) {
		t.Errorf("page after a %v", got)
	}
	if got := pop.Page("bb", 5); !slices.Equal(got, inv.members[2:]) {
		t.Errorf("page after a missing key %v", got)
	}
	if m, ok := pop.Lookup("b"); !ok || m.Revision != "2" || pop.Library() || pop.Len() != 3 || !pop.FrozenAt().Equal(time.Unix(10, 0)) {
		t.Errorf("lookup %v %v", m, ok)
	}
	if !pop.Excluded(workMember{"b", "2"}) || pop.Excluded(workMember{"b", "3"}) {
		t.Error("exclusion must name the failing revision only")
	}

	conflicting := &workInventoryBuilder{limit: 1 << 20}
	conflicting.Add("a", "1")
	conflicting.Add("a", "2")
	if _, err := conflicting.freeze(time.Now()); err == nil {
		t.Error("a unit at two revisions was frozen")
	}

	small := &workInventoryBuilder{limit: 600}
	var tooLarge *workInventoryTooLarge
	for i := 0; ; i++ {
		if err := small.Add(strings.Repeat("x", 3)+string(rune('a'+i)), "1"); err != nil {
			if !errors.As(err, &tooLarge) {
				t.Fatal(err)
			}
			break
		}
	}
	// 16 slots fit in 600 bytes; growing to 32 slots is refused before it
	// is allocated.
	if tooLarge.Members != 17 || tooLarge.Bytes <= tooLarge.Limit || cap(small.members) != 16 {
		t.Fatalf("refusal %+v with capacity %d", tooLarge, cap(small.members))
	}

	// Growth holds the old and the new array at once: 17 members fit in a
	// 32-slot array under 1200 bytes, but copying 16 slots into it needs
	// 48 slots' worth for a moment.
	peak := &workInventoryBuilder{limit: 1200}
	var err17 error
	for i := range 17 {
		if err17 = peak.Add(fmt.Sprintf("u%02d", i), "1"); err17 != nil {
			break
		}
	}
	if !errors.As(err17, &tooLarge) || tooLarge.Members != 17 || tooLarge.Bytes < 48*workMemberSize || cap(peak.members) != 16 {
		t.Fatalf("growth peak was not charged: %v, capacity %d", err17, cap(peak.members))
	}
	if peak.bytes != 16*workMemberSize+16*4 {
		t.Fatalf("charged %d bytes for 16 slots and their strings", peak.bytes)
	}
}

// Next sees only what it may select: eligible members at their target
// revision, so an adapter that takes the first page advances.
func TestWorkPopulationPagesEligibleMembersAtTheirTarget(t *testing.T) {
	b := &workInventoryBuilder{limit: 1 << 20}
	for i := range 130 {
		b.Add(fmt.Sprintf("u%03d", i), "1")
	}
	inv, err := b.freeze(time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	eligible := newWorkBits(130)
	for _, i := range []int{1, 64, 65, 129} {
		eligible.set(i)
	}
	pop := &workPopulation{inventory: inv, eligible: eligible, pending: 4, waiting: 7,
		target: func(i int) (string, bool) { return "2", i == 64 }}
	if got := pop.Page("", 2); !slices.Equal(got, []workMember{{"u001", "1"}, {"u064", "2"}}) {
		t.Fatalf("first page %v", got)
	}
	if got := pop.Page("u064", 5); !slices.Equal(got, []workMember{{"u065", "1"}, {"u129", "1"}}) {
		t.Fatalf("page after u064 %v", got)
	}
	if got := pop.Page("u100x", 5); !slices.Equal(got, []workMember{{"u129", "1"}}) {
		t.Fatalf("page after a missing key %v", got)
	}
	if _, ok := pop.Eligible("u002"); ok {
		t.Error("an ineligible member was eligible")
	}
	if m, ok := pop.Eligible("u064"); !ok || m.Revision != "2" {
		t.Errorf("eligible %v %v", m, ok)
	}
	if m, ok := pop.Lookup("u002"); !ok || m.Revision != "1" || pop.Len() != 130 || pop.Pending() != 4 || pop.Waiting() != 7 {
		t.Errorf("lookup %v %v", m, ok)
	}
	var library *workPopulation
	if library.Excluded(workMember{"u001", "1"}) || !library.Library() || library.Page("", 1) != nil {
		t.Error("a nil population is the library with nothing excluded")
	}
}

// The internal proof's shape: a recent request's units flow through the
// producer and its consumer while historical backfill waits, and the
// consumer starts on a ready unit before the producer finishes the rest.
func TestWorkRecentRequestFlowsThroughConsumersBeforeHistory(t *testing.T) {
	authorship, labels, defs := workPipeline([]string{"h1@1", "h2@1", "r1@1", "r2@1"}, nil)
	h := newWorkHarness(t, 2, defs...)
	history := h.submit(workRequestSpec{Key: "catch-up", Kinds: []string{"analysis.labels"}, Priority: workPriorityBackfill})
	h.runner.Pump()
	first := h.take("analysis.authorship", 0)
	if first.job.Units != 1 || first.job.Execution == 0 {
		t.Fatalf("job %+v", first.job)
	}
	{
		recent := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityForeground, Explicit: true,
			Trigger: "last two days", Build: workMembers(workMember{"r2", "1"}, workMember{"r1", "1"})})
		h.runner.Pump()
		h.build(recent)
		if q := h.request(recent); q.Members != 2 || !slices.Equal(q.Expanded, []string{"analysis.authorship"}) || q.FrozenAt.IsZero() {
			t.Fatalf("recent request %+v", q)
		}
		first.run(context.Background()) // h1's authorship commits
		h.runner.Pump()
		// labels for h1 is ready, and a CPU is free, but history waits.
		if h.pending("analysis.labels") {
			t.Fatal("historical labels started during an explicit recent request")
		}
		if q := h.request(history); q.Blocker == nil || q.Blocker.Reason != workBlockedRecentFirst {
			t.Fatalf("history blocker %+v", q.Blocker)
		}
		if task := h.request(recent).Tasks[1]; task.Kind != "analysis.labels" || task.Blocker == nil || task.Blocker.Reason != workBlockedDependency {
			t.Fatalf("labels for the recent request %+v", task)
		}
		h.complete("analysis.authorship") // r1
		// r1's labels run while the producer works on r2.
		if !h.pending("analysis.labels") || !h.pending("analysis.authorship") {
			t.Fatalf("expected the pipeline to overlap; pending %+v", h.jobs)
		}
		h.complete("analysis.authorship") // r2
		h.complete("analysis.labels")     // r1
		h.complete("analysis.labels")     // r2
		if q := h.request(recent); q.State != opSucceeded || q.Sealed != 2 || q.Unfinished != 0 {
			t.Fatalf("recent request %+v", q)
		}
	}
	for len(h.jobs) > 0 {
		h.complete(h.jobs[0].job.Kind)
	}
	if q := h.request(history); q.State != opSucceeded {
		t.Fatalf("history %+v", q)
	}
	if !slices.Equal(labels.ran[:2], []string{"r1", "r2"}) || len(authorship.dirty)+len(labels.dirty) != 0 {
		t.Fatalf("labels ran %v; left %v %v", labels.ran, authorship.dirty, labels.dirty)
	}
	if len(h.ended) != 2 {
		t.Fatalf("executions ended %+v", h.ended)
	}
}

func TestWorkSharedBatchOutlivesItsRequester(t *testing.T) {
	domain := newFakeWorkDomain("u1@1", "u2@1")
	domain.batch = 2
	h := newWorkHarness(t, 4, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	members := workMembers(workMember{"u1", "1"}, workMember{"u2", "1"})
	first := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Explicit: true, Build: members})
	second := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Explicit: true, Build: members})
	h.runner.Pump()
	h.build(first)
	h.build(second)
	batch := h.take("maintenance.parallelism", 0)
	if running := h.task("maintenance.parallelism").Running; running == nil || !slices.Equal(running.Demanders, []uint64{first, second}) {
		t.Fatalf("running %+v", running)
	}
	h.runner.Cancel(first)
	var cause error
	domain.during = func(ctx context.Context) { cause = context.Cause(ctx) }
	batch.run(context.Background())
	h.runner.Pump()
	if cause != nil {
		t.Fatalf("cancelling one requester cancelled shared work: %v", cause)
	}
	if q := h.request(first); q.State != opCancelled {
		t.Fatalf("first %+v", q)
	}
	if q := h.request(second); q.State != opSucceeded || q.Sealed != 2 {
		t.Fatalf("second %+v", q)
	}
	if len(h.ended) != 1 || h.ended[0].State != opSucceeded {
		t.Fatalf("execution ends %+v", h.ended)
	}

	// The last demander leaving stops the batch at its boundary, without a
	// failure and with the domain still dirty.
	domain.set("u3", "1")
	only := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Build: workMembers(workMember{"u3", "1"})})
	h.runner.Pump()
	h.build(only)
	batch = h.take("maintenance.parallelism", 0)
	h.runner.Cancel(only)
	batch.run(context.Background())
	if !errors.Is(cause, errWorkNoDemand) || domain.dirty["u3"] != "1" || h.task("maintenance.parallelism").Failures != 0 {
		t.Fatalf("cause %v, dirty %v, task %+v", cause, domain.dirty, h.task("maintenance.parallelism"))
	}
	if h.authority.facts().CPUAllocated != 0 || h.authority.facts().MemoryReserved != 0 {
		t.Fatalf("authority %+v", h.authority.facts())
	}
}

func TestWorkStopHoldsTheTaskUntilResume(t *testing.T) {
	domain := newFakeWorkDomain("u1@1", "u2@1")
	h := newWorkHarness(t, 2, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	id := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	batch := h.take("maintenance.parallelism", 0)
	h.runner.Stop("maintenance.parallelism")
	// A request admitted now does not join the batch that is ending.
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	if len(h.demands) != 0 {
		t.Fatalf("a request joined a cancelled batch: %+v", h.demands)
	}
	var cause error
	domain.during = func(ctx context.Context) { cause = context.Cause(ctx) }
	batch.run(context.Background())
	h.runner.Wake()
	if h.runner.Pump() != 0 || len(h.jobs) != 0 {
		t.Fatal("a stopped task restarted from rediscovered dirty work")
	}
	if !errors.Is(cause, errWorkStopped) || len(domain.dirty) != 2 {
		t.Fatalf("cause %v, dirty %v", cause, domain.dirty)
	}
	if q := h.request(id); q.State != opBlocked || q.Blocker.Reason != workBlockedHeld {
		t.Fatalf("request %+v", q)
	}
	if len(h.ended) != 1 || h.ended[0].State != opCancelled || h.task("maintenance.parallelism").Failures != 0 {
		t.Fatalf("executions %+v", h.ended)
	}
	h.runner.Resume("maintenance.parallelism")
	h.runner.Pump()
	h.complete("maintenance.parallelism")
	h.complete("maintenance.parallelism")
	if q := h.request(id); q.State != opSucceeded || len(domain.dirty) != 0 {
		t.Fatalf("after resume %+v", q)
	}
	// A Resume while the stopped batch drains waits for its execution to end
	// as stopped; work resumes in a new execution, never the stopping one.
	domain.during = nil
	domain.set("u3", "1")
	later := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	draining := h.take("maintenance.parallelism", 0)
	h.runner.Stop("maintenance.parallelism")
	h.runner.Resume("maintenance.parallelism")
	if h.runner.Pump() != 0 {
		t.Fatal("work started under a stopping execution")
	}
	draining.run(context.Background())
	h.runner.Pump()
	if last := h.ended[len(h.ended)-1]; last.State != opCancelled || last.Execution != draining.job.Execution {
		t.Fatalf("the stopped execution did not end first: %+v", h.ended)
	}
	if next := h.take("maintenance.parallelism", 0); next.job.Execution == draining.job.Execution {
		t.Fatal("resumed work reused the stopped execution")
	} else {
		next.run(context.Background())
		h.runner.Pump()
	}
	if q := h.request(later); q.State != opSucceeded {
		t.Fatalf("after a resume during the drain %+v", q)
	}
}

func TestWorkFailingFirstUnitBacksOffWithoutStarvingOthers(t *testing.T) {
	domain := newFakeWorkDomain("u1@1", "u2@1", "u3@1")
	domain.fail["u1"] = 99
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	id := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Trigger: triggerUser,
		Build: workMembers(workMember{"u1", "1"}, workMember{"u2", "1"}, workMember{"u3", "1"})})
	h.runner.Pump()
	// The first hook to name a request carries its origin.
	if len(h.jobs) != 1 || h.jobs[0].job.Triggers[id] != triggerUser {
		t.Fatalf("membership job %+v", h.jobs)
	}
	h.build(id)
	h.complete("maintenance.parallelism") // u1 fails
	h.complete("maintenance.parallelism") // u2
	h.complete("maintenance.parallelism") // u3
	if !slices.Equal(domain.ran, []string{"u1", "u2", "u3"}) {
		t.Fatalf("ran %v", domain.ran)
	}
	for range 5 {
		if h.runner.Pump() != 0 {
			t.Fatal("a unit in backoff ran again before its delay")
		}
	}
	if q := h.request(id); q.State != opBlocked || q.Blocker.Reason != workBlockedBackoff || q.Sealed != 2 {
		t.Fatalf("request %+v", q)
	}
	h.clock.Advance(5 * time.Second)
	h.runner.Pump()
	h.complete("maintenance.parallelism") // u1, second attempt
	h.clock.Advance(29 * time.Second)
	if h.runner.Pump() != 0 {
		t.Fatal("the third attempt came before the 30-second delay")
	}
	h.clock.Advance(time.Second)
	h.runner.Pump()
	h.complete("maintenance.parallelism") // u1, third attempt exhausts it
	if q := h.request(id); q.State != opFailed || q.Failed != 1 || q.Sealed != 2 || q.Reason != workUnitsFailed {
		t.Fatalf("request %+v", q)
	}
	if domain.runs != 5 || domain.dirty["u1"] != "1" {
		t.Fatalf("runs %d, dirty %v", domain.runs, domain.dirty)
	}
	if task := h.task("maintenance.parallelism"); task.Failures != 1 || !task.Examples[0].Exhausted || task.Examples[0].Attempts != 3 {
		t.Fatalf("task %+v", task)
	}
	// GET /api/work names a failed unit with the API's lower-case keys.
	if unit, _ := json.Marshal(h.task("maintenance.parallelism").Examples[0].Unit); string(unit) != `{"id":"u1","revision":"1"}` {
		t.Fatalf("failed unit JSON %s", unit)
	}

	// A later request does not retry the exhausted revision; an explicit
	// Retry does.
	later := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	if q := h.request(later); q.State != opFailed || len(h.jobs) != 0 {
		t.Fatalf("later request %+v", q)
	}
	h.runner.Retry("maintenance.parallelism")
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	if !h.pending("maintenance.parallelism") {
		t.Fatal("Retry did not attempt the failed unit")
	}
	h.complete("maintenance.parallelism")
	h.runner.InterruptKinds([]string{"maintenance.parallelism"}, "the input policy changed")
	if task := h.task("maintenance.parallelism"); task.Failures != 0 {
		t.Fatal("attempts at the former policy survived a configuration change")
	}
	domain.fail["u1"] = 0
	changedPolicy := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete("maintenance.parallelism")
	if request := h.request(changedPolicy); request.State != opSucceeded {
		t.Fatalf("new policy inherited the old failure: %+v", request)
	}
}

func TestWorkChangingInputsGetOneReplacement(t *testing.T) {
	authorship, labels, defs := workPipeline([]string{"a@1", "b@1"}, nil)
	authorship.batch, labels.batch = 2, 2
	h := newWorkHarness(t, 2, defs...)
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityForeground,
		Build: workMembers(workMember{"a", "1"}, workMember{"b", "1"})})
	h.runner.Pump()
	h.build(id)
	authorship.set("a", "2") // edited while the batch runs
	h.complete("analysis.authorship")
	h.complete("analysis.labels")     // b@1
	h.complete("analysis.authorship") // a@2, the replacement target
	if q := h.request(id); q.Sealed != 1 || q.State == opSucceeded {
		t.Fatalf("after replacement %+v", q)
	}
	labels.during = func(context.Context) {
		authorship.set("a", "3")
		labels.set("a", "3")
	}
	labels.superseded = []workMember{{"b", "9"}} // b is sealed: a later edit is new demand
	h.complete("analysis.labels")
	q := h.request(id)
	if q.State != opInterrupted || q.Reason != workInputsKeepChanging || q.Exhausted != 1 || q.Sealed != 1 {
		t.Fatalf("request %+v", q)
	}
	if authorship.dirty["a"] != "3" {
		t.Fatal("newer work must stay pending in the domain")
	}
}

func TestWorkCPUGrantsAreExplicitAndHonest(t *testing.T) {
	session := newFakeWorkDomain("s1@1", "s2@1")
	session.cpus = 3 // a stable model session's fixed envelope
	pool := newFakeWorkDomain("p1@1", "p2@1")
	pool.cpus, pool.maxCPUs = 1, 4
	h := newWorkHarness(t, 4, workDefinition{Kind: "analysis.labels", Task: session}, workDefinition{Kind: "maintenance.parallelism", Task: pool})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityLive})
	h.runner.Pump()
	if h.pending("analysis.labels") {
		t.Fatal("the fixed session started on a partial grant")
	}
	if task := h.task("analysis.labels"); task.State != opQueued {
		t.Fatalf("session %+v", task)
	}
	pool.during = func(context.Context) {
		if got := h.authority.facts().CPUAllocated; got != 4 {
			t.Errorf("the grant was released before RunBatch returned: %d allocated", got)
		}
	}
	h.complete("maintenance.parallelism")
	if !h.pending("analysis.labels") {
		t.Fatal("the session did not get its envelope")
	}
	h.authority.SetCPUs(2)
	// The session (3) and the pool's reduced grant (1) drain at their
	// boundaries.
	if facts := h.authority.facts(); facts.CPUOverBudget != 2 {
		t.Fatalf("authority %+v", facts)
	}
	pool.during = nil
	pool.set("p3", "1")
	h.complete("analysis.labels")
	// The session's minimum exceeds the new cap: an explicit blocker that
	// does not hold back work that fits.
	if task := h.task("analysis.labels"); task.Blocker != nil {
		t.Fatalf("a task blocker for a per-request condition: %+v", task.Blocker)
	}
	in := h.runner.Inspect()
	labelsRequest := in.Requests[len(in.Requests)-1]
	if labelsRequest.Blocker == nil || labelsRequest.Blocker.Reason != workBlockedExceedsCap {
		t.Fatalf("labels request %+v", labelsRequest)
	}
	h.complete("maintenance.parallelism") // p2; p3 takes what the cap allows
	if !h.pending("maintenance.parallelism") || h.pending("analysis.labels") {
		t.Fatalf("pending %+v", h.jobs)
	}
	// The pool left the waiting session its envelope (4, then 1), and later
	// took what the smaller cap allows.
	h.complete("maintenance.parallelism") // execute p3's admitted grant
	if !slices.Equal(pool.grants, []int{4, 1, 2}) || !slices.Equal(session.grants, []int{3}) {
		t.Fatalf("grants: pool %v, session %v", pool.grants, session.grants)
	}
}

func TestWorkInlineCallsShareTheAllowanceInOrder(t *testing.T) {
	a := newWorkAuthority(3)
	if err := a.Call(context.Background(), 4, 4, func(context.Context, workAllocation) error { return nil }); !errors.Is(err, errWorkGrantExceedsCap) {
		t.Fatalf("oversize call: %v", err)
	}
	waited := make(chan workAllocation)
	err := a.Call(context.Background(), 1, 2, func(ctx context.Context, allocation workAllocation) error {
		if allocation.CPUs != 2 {
			t.Errorf("allocation %+v", allocation)
		}
		go func() {
			a.Call(context.Background(), 2, 2, func(_ context.Context, inner workAllocation) error {
				waited <- inner
				return nil
			})
		}()
		waitUntil(t, 5*time.Second, "the inline caller to wait", func() bool { return a.facts().CPUWaiters == 1 })
		// One CPU is free, but the waiter came first.
		if _, ok, _ := a.tryCPUs(1, 1, 0, false); ok {
			t.Error("the runner overtook a waiting inline caller")
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := a.acquireCPUs(cancelled, 1, 1); !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled wait: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inner := <-waited; inner.CPUs != 2 {
		t.Fatalf("waiter %+v", inner)
	}
	waitUntil(t, 5*time.Second, "the waiter's release", func() bool { return a.facts().CPUAllocated == 0 })
	if facts := a.facts(); facts.CPUWaiters != 0 {
		t.Fatalf("authority %+v", facts)
	}
}

func TestWorkRequestAdmissionIsBounded(t *testing.T) {
	override(t, &workExplicitRequestLimit, 1)
	override(t, &workRequestMemoryLimit, 4096)
	// Three request limits less a byte: lower classes leave one free, so
	// only one of them reads membership at a time.
	override(t, &workProcessMemoryLimit, 3*4096-1)
	override(t, &workQueuedRequestLimit, 2)
	domain := newFakeWorkDomain()
	domain.blocker = workBlocker{Reason: workBlockedPolicy, Detail: "disabled"}
	h := newWorkHarness(t, 4, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	kinds := []string{"maintenance.parallelism"}
	first := h.submit(workRequestSpec{Key: "run-now", Kinds: kinds, Priority: workPriorityForeground, Explicit: true})
	if again := h.submit(workRequestSpec{Key: "run-now", Kinds: kinds, Priority: workPriorityForeground, Explicit: true, Trigger: "again"}); again != first {
		t.Fatal("identical intent was not coalesced")
	}
	for _, conflicting := range []workRequestSpec{
		{Key: "run-now", Kinds: kinds, Priority: workPriorityBackfill, Explicit: true},
		{Key: "run-now", Kinds: kinds, Priority: workPriorityForeground},
		{Key: "run-now", Kinds: kinds, Priority: workPriorityForeground, Explicit: true, Build: workMembers(workMember{"x", "1"})},
	} {
		if _, err := h.runner.Submit(conflicting); !errors.Is(err, errWorkKeyConflict) {
			t.Errorf("a different intent under one key: %v", err)
		}
	}
	second := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityForeground, Explicit: true})
	h.runner.Pump()
	if q := h.request(first); q.State != opBlocked || q.Blocker.Reason != workBlockedPolicy {
		t.Fatalf("first %+v", q)
	}
	if q := h.request(second); q.State != opQueued || q.Blocker.Reason != workBlockedRequestLimit {
		t.Fatalf("second %+v", q)
	}
	// RequestChanged reported both, once per change.
	reported := map[uint64]workRequestFacts{}
	for i, change := range h.changes {
		if prev, ok := reported[change.ID]; ok && prev.State == change.State && fmt.Sprint(prev.Blocker) == fmt.Sprint(change.Blocker) {
			t.Errorf("change %d repeats %+v", i, change)
		}
		reported[change.ID] = change
	}
	if f := reported[first]; f.State != opBlocked || f.Blocker == nil || f.Blocker.Reason != workBlockedPolicy {
		t.Fatalf("first reported %+v", f)
	}
	if f := reported[second]; f.State != opQueued || f.Blocker == nil || f.Blocker.Reason != workBlockedRequestLimit {
		t.Fatalf("second reported %+v", f)
	}
	h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive, Build: workMembers(workMember{"x", "1"})})
	if _, err := h.runner.Submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive}); !errors.Is(err, errWorkQueueFull) {
		t.Fatalf("queue bound: %v", err)
	}
	h.runner.Cancel(first)
	h.runner.Pump()
	if q := h.request(second); q.State != opBlocked {
		t.Fatalf("second after cancel %+v", q)
	}
	h.runner.Cancel(second)

	// Memory: a membership read reserves the full request limit, so a
	// second waits until the first has frozen at its actual size.
	finite := h.runner.Inspect().Requests[0].ID
	other := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive, Build: workMembers(workMember{"y", "1"})})
	h.runner.Pump()
	if q := h.request(other); q.State != opQueued || q.Blocker.Reason != workBlockedRequestMemory {
		t.Fatalf("other %+v", q)
	}
	// An explicit recent request still gets the memory history left free.
	recent := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityForeground, Explicit: true, Build: workMembers(workMember{"z", "1"})})
	h.runner.Pump()
	if q := h.request(recent); q.State == opQueued {
		t.Fatalf("recent %+v", q)
	}
	if q := h.request(other); q.State != opQueued || q.Blocker.Reason != workBlockedRequestMemory {
		t.Fatalf("other after recent %+v", q)
	}
	h.build(recent)
	h.runner.Cancel(recent)
	h.build(finite)
	if q := h.request(finite); q.Bytes >= 4096 || q.Bytes <= 0 {
		t.Fatalf("finite request charged %d bytes", q.Bytes)
	}
	h.build(other)

	tooLarge := make([]workMember, 200)
	for i := range tooLarge {
		tooLarge[i] = workMember{strings.Repeat("z", 20) + string(rune('A'+i)), "1"}
	}
	h.runner.Cancel(finite)
	h.runner.Cancel(other)
	big := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive, Build: workMembers(tooLarge...)})
	h.runner.Pump()
	h.build(big)
	if q := h.request(big); q.State != opFailed || q.Reason != workMembershipUnavailable || !strings.Contains(q.Detail, "narrow the request") {
		t.Fatalf("oversized request %+v", q)
	}
	if facts := h.authority.facts(); facts.MemoryReserved != 0 || facts.CPUAllocated != 0 {
		t.Fatalf("authority %+v", facts)
	}
}

func TestWorkBatchesWithoutProgressOrSelectorsThatFailAreBounded(t *testing.T) {
	stuck := &scriptedWorkTask{
		next: func(*workPopulation) (workSelection, error) { return workSelection{Phase: "discover"}, nil },
		run: func(context.Context, workSelection, workAllocation) (workBatchResult, error) {
			return workBatchResult{}, nil
		},
	}
	broken := &scriptedWorkTask{next: func(*workPopulation) (workSelection, error) { return workSelection{}, errors.New("index missing") }}
	waiting := &scriptedWorkTask{next: func(*workPopulation) (workSelection, error) {
		return workSelection{Blocker: workBlocker{Reason: workBlockedDependency, DependsOn: "maintenance.previews"}}, nil
	}}
	h := newWorkHarness(t, 2, workDefinition{Kind: "maintenance.parallelism", Task: stuck},
		workDefinition{Kind: "maintenance.previews", Task: broken}, workDefinition{Kind: "maintenance.library-view", Dependencies: []string{"maintenance.previews"}, Task: waiting})
	first := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	second := h.submit(workRequestSpec{Kinds: []string{"maintenance.library-view"}, Priority: workPriorityLive})
	h.runner.Pump()
	for _, delay := range []time.Duration{0, 5 * time.Second, 30 * time.Second} {
		h.clock.Advance(delay)
		h.runner.Pump()
		if len(h.jobs) > 0 {
			h.complete("maintenance.parallelism")
		}
		if h.runner.Pump() != 0 {
			t.Fatal("a batch without progress ran again immediately")
		}
	}
	if q := h.request(first); q.State != opFailed || stuck.runs != 3 {
		t.Fatalf("stuck request %+v after %d runs", q, stuck.runs)
	}
	q := h.request(second)
	if q.State != opFailed || broken.nexts != 3 {
		t.Fatalf("broken request %+v after %d selections", q, broken.nexts)
	}
	if q.Tasks[1].Blocker.Reason != workDependencyFailed || q.Tasks[1].Blocker.DependsOn != "maintenance.previews" {
		t.Fatalf("dependent task %+v", q.Tasks[1])
	}
}

func TestWorkFullFailureTableHoldsTheKind(t *testing.T) {
	override(t, &workFailureLimit, 1)
	domain := newFakeWorkDomain("u1@1", "u2@1")
	domain.batch = 2
	domain.fail["u1"], domain.fail["u2"] = 9, 9
	other := newFakeWorkDomain("v1@1")
	other.fail["v1"] = 1
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain}, workDefinition{Kind: "analysis.authorship", Task: other})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete("maintenance.parallelism")
	h.clock.Advance(time.Hour)
	if h.runner.Pump() != 0 {
		t.Fatal("a kind with forgotten failures kept running")
	}
	if task := h.task("maintenance.parallelism"); task.Blocker == nil || task.Blocker.Reason != workBlockedFailuresFull {
		t.Fatalf("task %+v", task)
	}
	// The limit is each kind's: another kind still records its own failure
	// and retries it.
	authorship := h.submit(workRequestSpec{Kinds: []string{"analysis.authorship"}, Priority: workPriorityLive, Build: workMembers(workMember{"v1", "1"})})
	h.runner.Pump()
	h.build(authorship)
	h.complete("analysis.authorship")
	if task := h.task("analysis.authorship"); task.Failures != 1 || task.Blocker != nil && task.Blocker.Reason == workBlockedFailuresFull {
		t.Fatalf("one kind's full table held another: %+v", task)
	}
	h.clock.Advance(5 * time.Second)
	h.runner.Pump()
	h.complete("analysis.authorship")
	if q := h.request(authorship); q.State != opSucceeded {
		t.Fatalf("the other kind's request %+v", q)
	}
	h.runner.Retry("maintenance.parallelism")
	if h.runner.Pump() != 1 {
		t.Fatal("Retry did not release the hold")
	}
}

// Retry reopens a unit its kind gave up on even after that kind's task was
// done, so the request does not succeed around it; another kind's failure of
// the same unit is not cleared by it.
func TestWorkRetryReopensADoneTask(t *testing.T) {
	authorship := newFakeWorkDomain("u1@1", "u2@1")
	authorship.fail["u1"] = 3
	parallelism := newFakeWorkDomain("u1@1", "u2@1")
	parallelism.blocker = workBlocker{Reason: workBlockedWriter, Detail: "another run is doing this work"}
	h := newWorkHarness(t, 1, workDefinition{Kind: "analysis.authorship", Task: authorship}, workDefinition{Kind: "maintenance.parallelism", Task: parallelism})
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.authorship", "maintenance.parallelism"}, Priority: workPriorityLive,
		Build: workMembers(workMember{"u1", "1"}, workMember{"u2", "1"})})
	h.runner.Pump()
	h.build(id)
	drain := func(kind string) {
		for h.pending(kind) {
			h.complete(kind)
		}
	}
	for _, wait := range []time.Duration{0, 5 * time.Second, 30 * time.Second} {
		h.clock.Advance(wait)
		h.runner.Pump()
		drain("analysis.authorship")
	}
	if q := h.request(id); q.Failed != 1 || opTerminal(q.State) {
		t.Fatalf("after exhausting u1: %+v", q)
	}
	if task := h.task("analysis.authorship"); task.Failures != 1 || !task.Examples[0].Exhausted {
		t.Fatalf("authorship %+v", task)
	}
	ran := len(authorship.ran)
	h.runner.Retry("maintenance.parallelism")
	if q := h.request(id); q.Failed != 1 {
		t.Fatalf("another kind's Retry cleared authorship's failure: %+v", q)
	}
	h.runner.Retry("analysis.authorship")
	h.runner.Pump()
	drain("analysis.authorship")
	if !slices.Contains(authorship.ran[ran:], "u1") {
		t.Fatalf("Retry did not ask the done task again: ran %v", authorship.ran[ran:])
	}
	parallelism.mu.Lock()
	parallelism.blocker = workBlocker{}
	parallelism.mu.Unlock()
	h.runner.Wake()
	h.runner.Pump()
	drain("maintenance.parallelism")
	if q := h.request(id); q.State != opSucceeded || q.Sealed != 2 || q.Failed != 0 {
		t.Fatalf("request after Retry: %+v", q)
	}
}

func TestWorkDecisionTraceIsBounded(t *testing.T) {
	override(t, &workDecisionLimit, 4)
	domain := newFakeWorkDomain("u1@1", "u2@1", "u3@1")
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	for len(h.jobs) > 0 {
		h.complete("maintenance.parallelism")
	}
	in := h.runner.Inspect()
	if len(in.Decisions) == 0 || len(in.Decisions) > 4 || in.DecisionsTotal <= int64(len(in.Decisions)) {
		t.Fatalf("%d decisions of %d", len(in.Decisions), in.DecisionsTotal)
	}
	// A new decision pushes out the oldest and keeps the order.
	newest := in.Decisions[len(in.Decisions)-1]
	h.runner.Retry("maintenance.parallelism")
	after := h.runner.Inspect()
	if len(after.Decisions) > 4 || after.DecisionsTotal <= in.DecisionsTotal || !slices.Contains(after.Decisions, newest) || after.Decisions[len(after.Decisions)-1] == newest {
		t.Fatalf("the trace did not keep the newest decisions: %+v then %+v", in.Decisions, after.Decisions)
	}
}

// A coarse pass covers its requests at its own boundary: a step that
// reports Remaining keeps draining, a completed pass covers the requests it
// ran for without waiting for the library to be quiet, and a request made
// while it ran waits for a pass of its own.
func TestWorkCoarseTaskCoversOnlyAtItsBoundary(t *testing.T) {
	var seen []bool
	steps := 0
	global := &scriptedWorkTask{
		next: func(pop *workPopulation) (workSelection, error) {
			seen = append(seen, pop.Library())
			return workSelection{Phase: "compare", Coarse: true}, nil
		},
		run: func(context.Context, workSelection, workAllocation) (workBatchResult, error) {
			steps++
			return workBatchResult{Work: 1, Committed: []workMember{{"a", "1"}}, Remaining: steps%2 == 1}, nil
		},
	}
	h := newWorkHarness(t, 1, workDefinition{Kind: "analysis.findings", Coarse: true, Task: global})
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.findings"}, Priority: workPriorityForeground,
		Build: workMembers(workMember{"a", "1"}, workMember{"b", "1"})})
	h.runner.Pump()
	h.build(id)
	job := h.take("analysis.findings", 0)
	if !job.job.Coarse {
		t.Fatalf("job %+v", job.job)
	}
	job.run(context.Background())
	if q := h.request(id); q.Sealed != 0 {
		t.Fatal("a coarse step's committed units covered the request before its boundary")
	}
	h.runner.Pump()
	last := h.take("analysis.findings", 0)
	library := h.submit(workRequestSpec{Kinds: []string{"analysis.findings"}, Priority: workPriorityLive})
	h.runner.Pump()
	last.run(context.Background())
	if q := h.request(id); q.State != opSucceeded || q.Sealed != 2 || !slices.Equal(seen, []bool{true, true}) {
		t.Fatalf("request %+v, library populations %v", q, seen)
	}
	if q := h.request(library); opTerminal(q.State) {
		t.Fatalf("a pass that started earlier covered a later request: %+v", q)
	}
	h.runner.Pump()
	h.complete("analysis.findings")
	h.complete("analysis.findings")
	if q := h.request(library); q.State != opSucceeded {
		t.Fatalf("the later request's own pass: %+v", q)
	}
}

// A request's mode reaches the kinds it named, not their dependencies; a
// batch selected in a mode is shared only with demand in that mode, and
// unpausable demand runs through the library pause.
func TestWorkRequestModesAndUnpausableDemand(t *testing.T) {
	modes := map[string][]string{}
	moded := func(kind string, domain *fakeWorkDomain) *scriptedWorkTask {
		return &scriptedWorkTask{
			next: func(pop *workPopulation) (workSelection, error) {
				modes[kind] = append(modes[kind], pop.Mode())
				sel, err := domain.Next(context.Background(), pop)
				sel.Mode = pop.Mode()
				return sel, err
			},
			run: func(ctx context.Context, sel workSelection, allocation workAllocation) (workBatchResult, error) {
				return domain.RunBatch(ctx, sel, allocation)
			},
		}
	}
	writing, facts := newFakeWorkDomain("u1@1"), newFakeWorkDomain("u1@1", "u2@1")
	h := newWorkHarness(t, 2, workDefinition{Kind: "analysis.authorship", Task: moded("analysis.authorship", writing)},
		workDefinition{Kind: "maintenance.labels.query", Dependencies: []string{"analysis.authorship"}, Task: moded("maintenance.labels.query", facts),
			DependenciesForRequest: func(spec workRequestSpec) []string {
				if spec.Mode == "projection" {
					return nil
				}
				return []string{"analysis.authorship"}
			}})
	h.runner.SetPaused(true)
	paused := h.submit(workRequestSpec{Kinds: []string{"maintenance.labels.query"}, Priority: workPriorityBackfill})
	projection := h.submit(workRequestSpec{Kinds: []string{"maintenance.labels.query"}, Priority: workPriorityBackfill, Mode: "projection", Unpausable: true})
	if _, err := h.runner.Submit(workRequestSpec{Key: "k", Kinds: []string{"maintenance.labels.query"}, Priority: workPriorityBackfill, Mode: "projection"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runner.Submit(workRequestSpec{Key: "k", Kinds: []string{"maintenance.labels.query"}, Priority: workPriorityBackfill}); !errors.Is(err, errWorkKeyConflict) {
		t.Fatalf("a key reused for another mode: %v", err)
	}
	h.runner.Pump()
	if !h.pending("maintenance.labels.query") || h.pending("analysis.authorship") {
		t.Fatalf("unpausable projection: jobs %+v", h.jobs)
	}
	if q := h.request(paused); opTerminal(q.State) || q.Blocker == nil || q.Blocker.Reason != workBlockedPaused {
		t.Fatalf("paused demand %+v", q)
	}
	if q := h.request(projection); q.Mode != "projection" || !q.Unpausable || len(q.Expanded) != 0 {
		t.Fatalf("projection request %+v", q)
	}
	job := h.take("maintenance.labels.query", 0)
	if slices.Contains(job.job.Demanders, paused) {
		t.Fatal("a batch selected in a mode was shared with demand in another")
	}
	job.run(context.Background())
	h.runner.SetPaused(false)
	h.runner.Pump()
	for h.pending("analysis.authorship") || h.pending("maintenance.labels.query") {
		for _, kind := range []string{"analysis.authorship", "maintenance.labels.query"} {
			if h.pending(kind) {
				h.complete(kind)
			}
		}
	}
	if !slices.Contains(modes["maintenance.labels.query"], "projection") || slices.Contains(modes["analysis.authorship"], "projection") {
		t.Fatalf("modes seen: %v", modes)
	}
	if q := h.request(paused); q.State != opSucceeded {
		t.Fatalf("paused demand after unpause %+v", q)
	}

	t.Run("default coarse pass cannot satisfy force", func(t *testing.T) {
		blocked := true
		task := &scriptedWorkTask{
			next: func(*workPopulation) (workSelection, error) {
				if blocked {
					return workSelection{Blocker: workBlocker{Reason: workBlockedExternal}}, nil
				}
				return workSelection{Phase: "pass"}, nil
			},
			run: func(context.Context, workSelection, workAllocation) (workBatchResult, error) {
				return workBatchResult{Work: 1}, nil
			},
		}
		h := newWorkHarness(t, 1, workDefinition{Kind: "analysis.findings", Task: task, Coarse: true})
		h.submit(workRequestSpec{Kinds: []string{"analysis.findings"}, Priority: workPriorityLive})
		force := h.submit(workRequestSpec{Kinds: []string{"analysis.findings"}, Priority: workPriorityBackfill, Mode: "force"})
		h.runner.Pump() // both requests become admitted before a pass selects
		blocked = false
		h.runner.Wake()
		h.runner.Pump()
		job := h.take("analysis.findings", 0)
		if slices.Contains(job.job.Demanders, force) {
			t.Fatal("a default pass accepted forced demand")
		}
		job.run(context.Background())
		h.runner.Pump()
		if q := h.request(force); opTerminal(q.State) || !h.pending("analysis.findings") {
			t.Fatalf("force did not get its own pass: %+v", q)
		}
		h.complete("analysis.findings")
		if q := h.request(force); q.State != opSucceeded {
			t.Fatalf("forced pass %+v", q)
		}
	})
}

// Repeated demand reconsiders a blocker that has no event of its own.
func TestWorkCoalescedSubmitReconsidersBlockers(t *testing.T) {
	domain := newFakeWorkDomain("u1@1")
	domain.blocker = workBlocker{Reason: workBlockedWriter, Detail: "another run is doing this work"}
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	spec := workRequestSpec{Key: "auto", Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive}
	h.submit(spec)
	h.runner.Pump()
	h.runner.byKind["maintenance.parallelism"].wait = workBlocker{Reason: workBlockedWriter}
	h.runner.byKind["maintenance.parallelism"].waitUntil = h.clock.Now().Add(time.Hour)
	domain.mu.Lock()
	domain.blocker = workBlocker{}
	domain.mu.Unlock()
	h.submit(spec)
	h.runner.Pump()
	if !h.pending("maintenance.parallelism") {
		t.Fatal("repeated demand waited out a writer that had finished")
	}
}

func TestWorkHistoryGetsABatchAfterLiveTime(t *testing.T) {
	live := newFakeWorkDomain("l1@1", "l2@1", "l3@1")
	history := newFakeWorkDomain("h1@1", "h2@1")
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: live}, workDefinition{Kind: "maintenance.previews", Task: history})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.previews"}, Priority: workPriorityBackfill})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.clock.Advance(10 * time.Second)
	h.complete("maintenance.parallelism") // l1: 10 s of live time
	h.clock.Advance(55 * time.Second)
	h.complete("maintenance.parallelism") // l2: 65 s
	if !h.pending("maintenance.previews") {
		t.Fatalf("history did not get its reserved batch; pending %+v", h.jobs)
	}
	h.complete("maintenance.previews")
	if !h.pending("maintenance.parallelism") {
		t.Fatal("live work did not resume after the reserved batch")
	}
}

func TestWorkBatchPanicAndStartRefusalReleaseTheGrant(t *testing.T) {
	panicking := &scriptedWorkTask{
		next: func(*workPopulation) (workSelection, error) { return workSelection{Phase: "run"}, nil },
		run:  func(context.Context, workSelection, workAllocation) (workBatchResult, error) { panic("adapter bug") },
	}
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: panicking})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	job := h.take("maintenance.parallelism", 0)
	func() {
		defer func() { recover() }()
		job.run(context.Background())
	}()
	if facts := h.authority.facts(); facts.CPUAllocated != 0 {
		t.Fatalf("authority %+v", facts)
	}
	if task := h.task("maintenance.parallelism"); task.Failures != 1 || task.Examples[0].Error != errWorkPanicked.Error() {
		t.Fatalf("task %+v", task)
	}

	h.refuse = errors.New("the service is stopping")
	h.clock.Advance(5 * time.Second)
	h.runner.Pump()
	if facts := h.authority.facts(); facts.CPUAllocated != 0 {
		t.Fatalf("authority after refusal %+v", facts)
	}
	if task := h.task("maintenance.parallelism"); task.Blocker == nil || task.Blocker.Reason != workBlockedExternal || task.Running != nil {
		t.Fatalf("task %+v", task)
	}
}

func TestWorkRunLoopWakesAndReconsidersOnItsClock(t *testing.T) {
	domain := newFakeWorkDomain("u1@1")
	domain.fail["u1"] = 1
	clock := newWorkTestClock()
	jobs := make(chan workTestJob, 4)
	authority := newWorkAuthority(1)
	runner, err := newWorkRunner(context.Background(), clock, authority, workHooks{Start: func(job workJob, run func(context.Context)) error {
		jobs <- workTestJob{job: job, run: run}
		return nil
	}}, []workDefinition{{Kind: "maintenance.parallelism", Task: domain}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(stopped)
	}()
	id, _ := runner.Submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	(<-jobs).run(context.Background()) // fails; retry in 5 s
	for d := range clock.armed {
		if d == 5*time.Second {
			break
		}
	}
	clock.Advance(5 * time.Second)
	(<-jobs).run(context.Background())
	waitUntil(t, 5*time.Second, "the retried request to end", func() bool { return len(runner.Inspect().Ended) == 1 })
	if ended := runner.Inspect().Ended[0]; ended.ID != id || ended.State != opSucceeded {
		t.Fatalf("request %+v", ended)
	}
	runner.SetPaused(true)
	queuedID, err := runner.Submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-stopped
	in := runner.Inspect()
	if len(in.Requests) != 0 || !slices.ContainsFunc(in.Ended, func(f workRequestFacts) bool { return f.ID == queuedID && f.State == opInterrupted }) {
		t.Fatalf("shutdown retained unfinished demand: %+v", in)
	}
	if _, err := runner.Submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive}); !errors.Is(err, errServiceStopping) {
		t.Fatalf("stopped runner accepted demand: %v", err)
	}
}

// The stored pause policy is read, not pushed: a timed pause ends at its
// time without a settings save, and the loop wakes for it.
func TestWorkTimedPauseEndsOnItsOwn(t *testing.T) {
	domain := newFakeWorkDomain("u1@1")
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	until := h.clock.Now().Add(10 * time.Second)
	reads := 0
	h.runner.hooks.Paused = func(_ context.Context, now time.Time) (bool, time.Time, error) {
		reads++
		return now.Before(until), until, nil
	}
	h.runner.readPause(t.Context())
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	if h.runner.Pump() != 0 || !h.runner.Inspect().Paused {
		t.Fatal("a paused runner started a batch")
	}
	if wait := h.runner.nextDeadline(); wait != 10*time.Second {
		t.Fatalf("the loop sleeps %v past a pause ending in 10s", wait)
	}
	h.clock.Advance(10 * time.Second)
	h.runner.readPause(t.Context())
	if h.runner.Inspect().Paused || h.runner.Pump() != 1 || reads != 2 {
		t.Fatalf("the expired pause still held new batches (%d reads)", reads)
	}

	t.Run("reconcile while demand keeps waking the loop", func(t *testing.T) {
		h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: newFakeWorkDomain()})
		changedAt := h.clock.Now().Add(workReconcileInterval)
		h.runner.hooks.Paused = func(_ context.Context, now time.Time) (bool, time.Time, error) {
			return !now.Before(changedAt), time.Time{}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); h.runner.Run(ctx) }()
		defer func() { cancel(); <-done }()
		for range 40 {
			select {
			case <-h.clock.armed:
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not wait for its next event")
			}
			h.clock.Advance(time.Second)
			h.runner.Wake()
		}
		waitUntil(t, 5*time.Second, "pause policy despite frequent demand", func() bool { return h.runner.Inspect().Paused })
	})
}

type idleWorkTask struct {
	*fakeWorkDomain
	idles int
}

func (i *idleWorkTask) Idle(time.Time) { i.idles++ }

// A task that keeps state hears it is idle only while it has neither demand
// nor a batch.
func TestWorkIdleTasksHearItBetweenExecutions(t *testing.T) {
	task := &idleWorkTask{fakeWorkDomain: newFakeWorkDomain("u1@1")}
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: task})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	if !h.pending("maintenance.parallelism") || task.idles != 0 {
		t.Fatalf("a demanded task was told it is idle (%d)", task.idles)
	}
	h.complete("maintenance.parallelism")
	h.runner.Pump()
	if task.idles == 0 {
		t.Fatal("a task without demand or a batch never heard it is idle")
	}
}

// stuckWorkTask's Next waits for its context, like a read stuck behind a
// busy catalog.
type stuckWorkTask struct{ fakeWorkDomain }

func (s *stuckWorkTask) Next(ctx context.Context, _ *workPopulation) (workSelection, error) {
	<-ctx.Done()
	return workSelection{}, ctx.Err()
}

// One stuck Next does not hold admission for every kind: it times out and
// waits without charging unattempted units, and the pass goes on.
func TestWorkStuckSelectDoesNotHoldOtherKinds(t *testing.T) {
	override(t, &workSelectTimeout, 10*time.Millisecond)
	other := newFakeWorkDomain("u1@1")
	h := newWorkHarness(t, 2, workDefinition{Kind: "analysis.authorship", Task: &stuckWorkTask{}}, workDefinition{Kind: "maintenance.parallelism", Task: other})
	h.submit(workRequestSpec{Kinds: []string{"analysis.authorship", "maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	if !h.pending("maintenance.parallelism") {
		t.Fatal("a stuck select held another kind's admission")
	}
	for range 3 {
		if task := h.task("analysis.authorship"); task.Blocker == nil || task.Blocker.Reason != workBlockedExternal || task.Failures != 0 {
			t.Fatalf("the stuck kind %+v", task)
		}
		h.clock.Advance(workReconcileInterval)
		h.runner.Pump()
	}
	if len(h.runner.Inspect().Requests) == 0 {
		t.Fatal("slow selection exhausted work that had not been attempted")
	}
}

type scriptedWorkTask struct {
	next        func(*workPopulation) (workSelection, error)
	run         func(context.Context, workSelection, workAllocation) (workBatchResult, error)
	nexts, runs int
}

func (s *scriptedWorkTask) Next(_ context.Context, pop *workPopulation) (workSelection, error) {
	s.nexts++
	return s.next(pop)
}

func (s *scriptedWorkTask) RunBatch(ctx context.Context, sel workSelection, allocation workAllocation) (workBatchResult, error) {
	s.runs++
	return s.run(ctx, sel, allocation)
}

// pagedWorkTask is an ordinary finite-population adapter: it selects the
// first eligible page and commits what it ran, at the revision overridden
// once in revision (an edit it observed) or the selected one.
type pagedWorkTask struct {
	revision map[string]string
	selected []workMember
	ran      []string
	nexts    int
}

func (p *pagedWorkTask) Next(_ context.Context, pop *workPopulation) (workSelection, error) {
	p.nexts++
	page := pop.Page("", 1)
	if len(page) == 0 {
		return workSelection{}, nil
	}
	p.selected = append(p.selected, page...)
	return workSelection{Units: page}, nil
}

func (p *pagedWorkTask) RunBatch(_ context.Context, sel workSelection, _ workAllocation) (workBatchResult, error) {
	var result workBatchResult
	for _, m := range sel.Units {
		p.ran = append(p.ran, m.ID)
		if revision, ok := p.revision[m.ID]; ok {
			m.Revision = revision
			delete(p.revision, m.ID)
		}
		result.Committed = append(result.Committed, m)
	}
	return result, nil
}

func workPagedPipeline(t *testing.T) (*workHarness, *pagedWorkTask, *pagedWorkTask) {
	producer, consumer := &pagedWorkTask{revision: map[string]string{}}, &pagedWorkTask{revision: map[string]string{}}
	h := newWorkHarness(t, 2,
		workDefinition{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: consumer},
		workDefinition{Kind: "analysis.authorship", Task: producer})
	return h, producer, consumer
}

// A consumer starts on a unit as soon as its producer covered it, never
// before, and its empty Next does not cover units still waiting.
func TestWorkConsumersFollowTheirProducersUnitByUnit(t *testing.T) {
	h, producer, consumer := workPagedPipeline(t)
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityForeground,
		Build: workMembers(workMember{"a", "1"}, workMember{"b", "1"}, workMember{"c", "1"})})
	h.runner.Pump()
	h.build(id)
	if h.pending("analysis.labels") || !h.pending("analysis.authorship") {
		t.Fatalf("pending %+v", h.jobs)
	}
	if task := h.request(id).Tasks[1]; task.Covered != 0 || task.State != opBlocked || task.Blocker.Reason != workBlockedDependency ||
		task.Blocker.DependsOn != "analysis.authorship" {
		t.Fatalf("an empty Next covered units still waiting for their producer: %+v", task)
	}
	h.complete("analysis.authorship") // a
	if !h.pending("analysis.labels") || !h.pending("analysis.authorship") {
		t.Fatalf("the consumer did not start on a while the producer works on b; pending %+v", h.jobs)
	}
	h.complete("analysis.labels") // a
	if task := h.request(id).Tasks[1]; task.Covered != 1 || task.State != opBlocked {
		t.Fatalf("consumer after a %+v", task)
	}
	for len(h.jobs) > 0 {
		h.complete(h.jobs[0].job.Kind)
	}
	if q := h.request(id); q.State != opSucceeded || q.Sealed != 3 {
		t.Fatalf("request %+v", q)
	}
	// Each page advanced past covered units: every unit ran once.
	if !slices.Equal(producer.ran, []string{"a", "b", "c"}) || !slices.Equal(consumer.ran, []string{"a", "b", "c"}) {
		t.Fatalf("producer ran %v, consumer ran %v", producer.ran, consumer.ran)
	}
}

// A consumer's output at a newer revision than its producer's does not
// seal the unit, and a late commit at the frozen revision is stale rather
// than a second change.
func TestWorkMixedRevisionsNeverSealAUnit(t *testing.T) {
	h, producer, consumer := workPagedPipeline(t)
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityForeground, Build: workMembers(workMember{"x", "1"})})
	h.runner.Pump()
	h.build(id)
	h.complete("analysis.authorship") // x@1
	consumer.revision["x"] = "2"
	h.complete("analysis.labels") // the consumer read x@2: a replacement
	if q := h.request(id); q.Sealed != 0 || q.Exhausted != 0 || q.Tasks[0].Covered != 0 || q.Tasks[1].Covered != 0 {
		t.Fatalf("a consumer at x@2 over a producer at x@1 counted: %+v", q)
	}
	producer.revision["x"] = "1"
	h.complete("analysis.authorship") // a late commit at the frozen revision
	if q := h.request(id); q.Exhausted != 0 || q.Tasks[0].Covered != 0 || q.State == opInterrupted {
		t.Fatalf("a stale commit counted as coverage or a change: %+v", q)
	}
	h.complete("analysis.authorship") // x@2
	h.complete("analysis.labels")     // x@2
	if q := h.request(id); q.State != opSucceeded || q.Sealed != 1 {
		t.Fatalf("request %+v", q)
	}
	if got := producer.selected[len(producer.selected)-1]; got != (workMember{"x", "2"}) {
		t.Fatalf("Page offered %v after the replacement", got)
	}
}

// Library demand: the consumer's empty Next is current only once its
// producer is. A coarse prerequisite gates its consumer entirely.
func TestWorkLibraryAndCoarseDependenciesWaitForTheWholePrerequisite(t *testing.T) {
	producer := newFakeWorkDomain("a@1")
	consumer := &scriptedWorkTask{next: func(*workPopulation) (workSelection, error) { return workSelection{}, nil }}
	h := newWorkHarness(t, 2, workDefinition{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: consumer},
		workDefinition{Kind: "analysis.authorship", Task: producer})
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityLive})
	h.runner.Pump()
	if task := h.request(id).Tasks[1]; task.State != opBlocked || task.Blocker.Reason != workBlockedDependency {
		t.Fatalf("library consumer covered while its producer has pending work: %+v", task)
	}
	h.complete("analysis.authorship")
	if q := h.request(id); q.State != opSucceeded {
		t.Fatalf("request %+v", q)
	}

	done := false
	global := &scriptedWorkTask{
		next: func(*workPopulation) (workSelection, error) {
			if done {
				return workSelection{}, nil
			}
			return workSelection{Phase: "compare"}, nil
		},
		run: func(context.Context, workSelection, workAllocation) (workBatchResult, error) {
			done = true
			return workBatchResult{Work: 1}, nil
		},
	}
	units := &pagedWorkTask{}
	h = newWorkHarness(t, 2, workDefinition{Kind: "analysis.findings", Coarse: true, Task: global},
		workDefinition{Kind: "maintenance.parallelism", Dependencies: []string{"analysis.findings"}, Task: units})
	id = h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Build: workMembers(workMember{"a", "1"})})
	h.runner.Pump()
	h.build(id)
	if units.nexts != 0 || h.pending("maintenance.parallelism") {
		t.Fatal("a consumer of a coarse task was asked before the coarse task was done")
	}
	h.complete("analysis.findings")
	h.complete("maintenance.parallelism")
	if q := h.request(id); q.State != opSucceeded || q.Sealed != 1 {
		t.Fatalf("request %+v", q)
	}
}

// Hooks run outside the lock and in order: a batch's outcome, then its
// execution's end; demand changes as requesters join and leave; and a
// request cancelled during its membership read keeps its memory until the
// read returns.
func TestWorkHooksReportOutcomesInDrainOrder(t *testing.T) {
	domain := newFakeWorkDomain("u1@1", "u2@1")
	domain.batch = 2
	h := newWorkHarness(t, 4, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	members := workMembers(workMember{"u1", "1"}, workMember{"u2", "1"})
	first := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Build: members})
	second := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityForeground, Build: members})
	h.runner.Pump()
	h.build(first)
	h.build(second)
	batch := h.take("maintenance.parallelism", 0)
	h.runner.Cancel(first)
	if len(h.demands) != 2 || !slices.Equal(h.demands[0].Demanders, []uint64{first, second}) || !slices.Equal(h.demands[1].Demanders, []uint64{second}) ||
		h.demands[1].Execution != batch.job.Execution || h.demands[1].Batch != batch.job.Batch {
		t.Fatalf("demand changes %+v", h.demands)
	}
	// A batch that finishes its work after a late cancellation succeeded.
	domain.boundary = true
	domain.during = func(context.Context) { h.runner.Stop("maintenance.parallelism") }
	batch.run(context.Background())
	end := h.batches[len(h.batches)-1]
	if end.Job.Batch != batch.job.Batch || end.State != opSucceeded || end.Err != nil {
		t.Fatalf("batch end %+v", end)
	}
	last := func(hook string) int {
		for i := len(h.hooks) - 1; i >= 0; i-- {
			if h.hooks[i] == hook {
				return i
			}
		}
		return -1
	}
	if b, e := last("batch maintenance.parallelism"), last("execution maintenance.parallelism"); b < 0 || e < b ||
		!slices.Contains(h.hooks, "request "+fmt.Sprint(second)) {
		t.Fatalf("hook order %v", h.hooks)
	}
	h.runner.Resume("maintenance.parallelism")

	// Cancelled before it finished: cancelled with its reason, no failure.
	domain.during, domain.boundary = nil, false
	domain.set("u3", "1")
	only := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	batch = h.take("maintenance.parallelism", 0)
	h.runner.Cancel(only)
	batch.run(context.Background())
	if end := h.batches[len(h.batches)-1]; end.State != opCancelled || end.Reason != "no-demand" || h.task("maintenance.parallelism").Failures != 0 {
		t.Fatalf("batch end %+v", end)
	}

	// A membership read cancelled midway.
	reading := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive, Build: members})
	h.runner.Pump()
	read := h.take(workMembershipKind, reading)
	h.runner.Cancel(reading)
	if facts := h.authority.facts(); facts.MemoryReserved != workRequestMemoryLimit {
		t.Fatalf("the reservation was released while the read still holds it: %+v", facts)
	}
	if slices.Contains(h.hooks, "request "+fmt.Sprint(reading)) {
		t.Fatal("RequestEnded came before the read returned")
	}
	read.run(context.Background())
	if end := h.batches[len(h.batches)-1]; end.Job.Request != reading || end.State != opCancelled || end.Reason != "request-cancelled" {
		t.Fatalf("read end %+v", end)
	}
	if h.hooks[len(h.hooks)-1] != "request "+fmt.Sprint(reading) {
		t.Fatalf("hook order %v", h.hooks)
	}
	if facts := h.authority.facts(); facts.MemoryReserved != 0 || facts.CPUAllocated != 0 {
		t.Fatalf("authority %+v", facts)
	}
}

// Inline callers that arrive while a foreground batch waits go after it.
func TestWorkForegroundClaimsItsPlaceAmongInlineCallers(t *testing.T) {
	a := newWorkAuthority(1)
	held, _ := a.acquireCPUs(context.Background(), 1, 1)
	if _, ok, _ := a.tryCPUs(1, 1, 0, true); ok {
		t.Fatal("granted beyond the allowance")
	}
	granted := make(chan int)
	go func() {
		n, _ := a.acquireCPUs(context.Background(), 1, 1)
		granted <- n
	}()
	waitUntil(t, 5*time.Second, "the inline caller to wait", func() bool { return a.facts().CPUWaiters == 1 })
	a.releaseCPUs(held)
	if got := a.facts(); got.CPUAllocated != 0 {
		t.Fatalf("a later inline caller took the claimed capacity: %+v", got)
	}
	n, ok, _ := a.tryCPUs(1, 1, 0, true)
	if !ok {
		t.Fatal("the claim was not honoured")
	}
	a.releaseCPUs(n)
	if got := <-granted; got != 1 {
		t.Fatalf("waiter got %d", got)
	}
	a.releaseCPUs(1)

	// A claim the runner drops lets waiters through.
	held, _ = a.acquireCPUs(context.Background(), 1, 1)
	a.tryCPUs(1, 1, 0, true)
	go func() {
		n, _ := a.acquireCPUs(context.Background(), 1, 1)
		granted <- n
	}()
	waitUntil(t, 5*time.Second, "the inline caller to wait", func() bool { return a.facts().CPUWaiters == 1 })
	a.releaseCPUs(held)
	a.dropClaim()
	if got := <-granted; got != 1 {
		t.Fatalf("waiter got %d", got)
	}
}

// A retry whose delay passed but whose unit is no longer pending neither
// spins the loop nor stays in the failure table, and a library selection at
// a new revision retires its older failure.
func TestWorkPassedRetriesDoNotSpinAndNewRevisionsRetireFailures(t *testing.T) {
	domain := newFakeWorkDomain("u1@1")
	domain.fail["u1"] = 4 // once now, then three times to exhaust it
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete("maintenance.parallelism") // fails; retry in 5 s
	domain.mu.Lock()
	delete(domain.dirty, "u1") // another writer made it current
	domain.mu.Unlock()
	h.clock.Advance(6 * time.Second)
	if d := h.runner.nextDeadline(); d != workReconcileInterval {
		t.Fatalf("a passed retry deadline made the loop wake in %v", d)
	}
	h.runner.Pump()
	if task := h.task("maintenance.parallelism"); task.Failures != 0 {
		t.Fatalf("a retired retry stayed remembered: %+v", task)
	}

	domain.set("u1", "1")
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	for range 3 {
		h.complete("maintenance.parallelism")
		h.clock.Advance(time.Minute)
		h.runner.Pump()
	}
	if task := h.task("maintenance.parallelism"); task.Failures != 1 || !task.Examples[0].Exhausted {
		t.Fatalf("task %+v", task)
	}
	domain.set("u1", "2")
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	if !h.pending("maintenance.parallelism") || h.task("maintenance.parallelism").Failures != 0 {
		t.Fatalf("the new revision did not retire the exhausted failure: %+v", h.task("maintenance.parallelism"))
	}
}

// An empty Next covers only what its snapshot offered: a producer commit
// that lands while Next runs, or a retarget since the snapshot, makes the
// pass ask again instead of crediting work Next never saw.
func TestWorkEmptyNextCoversOnlyItsSnapshot(t *testing.T) {
	var h *workHarness
	runPending := func(kind string) bool {
		if !h.pending(kind) {
			return false
		}
		h.take(kind, 0).run(context.Background())
		return true
	}
	producer := &pagedWorkTask{revision: map[string]string{}}
	consumer := &scriptedWorkTask{
		next: func(pop *workPopulation) (workSelection, error) {
			if runPending("analysis.authorship") { // x commits while Next runs
				return workSelection{}, nil
			}
			return workSelection{Units: pop.Page("", 1)}, nil
		},
		run: func(_ context.Context, sel workSelection, _ workAllocation) (workBatchResult, error) {
			return workBatchResult{Committed: sel.Units}, nil
		},
	}
	h = newWorkHarness(t, 2, workDefinition{Kind: "analysis.labels", Dependencies: []string{"analysis.authorship"}, Task: consumer},
		workDefinition{Kind: "analysis.authorship", Task: producer})
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityForeground, Build: workMembers(workMember{"x", "1"})})
	h.runner.Pump()
	h.build(id)
	if q := h.request(id); q.State == opSucceeded || q.Tasks[1].Covered != 0 || !h.pending("analysis.labels") {
		t.Fatalf("a commit during Next was credited to the consumer: %+v; pending %+v", q, h.jobs)
	}
	h.complete("analysis.labels")
	if q := h.request(id); q.State != opSucceeded || consumer.runs != 1 {
		t.Fatalf("request %+v after %d consumer runs", q, consumer.runs)
	}

	// Independent tasks: one retargets x while the other's Next runs.
	timing := &pagedWorkTask{revision: map[string]string{"x": "2"}}
	authorship := &scriptedWorkTask{next: func(*workPopulation) (workSelection, error) {
		runPending("maintenance.parallelism")
		return workSelection{}, nil
	}}
	h = newWorkHarness(t, 2, workDefinition{Kind: "maintenance.parallelism", Task: timing}, workDefinition{Kind: "analysis.authorship", Task: authorship})
	id = h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism", "analysis.authorship"}, Priority: workPriorityForeground,
		Build: workMembers(workMember{"x", "1"})})
	h.runner.Pump()
	h.build(id)
	if q := h.request(id); q.State != opSucceeded || q.Sealed != 1 || authorship.nexts != 2 {
		t.Fatalf("request %+v; the outdated empty answer was asked again %d times", q, authorship.nexts-1)
	}
}

// A refused membership read rolls its reservation back without waking the
// run loop into the same refusal.
func TestWorkCapacityWaitsDoNotWakeThemselves(t *testing.T) {
	domain := newFakeWorkDomain("u1@1")
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump() // u1 holds the only CPU
	finite := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive, Build: workMembers(workMember{"u1", "1"})})
	for range 2 {
		select {
		case <-h.runner.wake:
		default:
		}
		h.runner.Pump()
		select {
		case <-h.runner.wake:
			t.Fatal("a pass that changed nothing woke the run loop")
		default:
		}
	}
	if q := h.request(finite); q.State != opQueued || q.Blocker == nil || q.Blocker.Reason != workBlockedCapacity {
		t.Fatalf("request %+v", q)
	}
	if facts := h.authority.facts(); facts.MemoryReserved != 0 || facts.CPUAllocated != 1 {
		t.Fatalf("authority %+v", facts)
	}
}

// A fixed envelope that waited keeps the capacity it waits for: a 1-CPU
// producer with many units does not keep taking it first.
func TestWorkWaitingEnvelopeIsNotStarvedBySmallerGrants(t *testing.T) {
	authorship, labels, defs := workPipeline([]string{"a@1", "b@1", "c@1", "d@1"}, nil)
	labels.cpus = 2
	h := newWorkHarness(t, 2, defs...)
	id := h.submit(workRequestSpec{Kinds: []string{"analysis.labels"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete("analysis.authorship") // a; labels for a waits for 2 CPUs beside b
	if h.pending("analysis.labels") || !h.pending("analysis.authorship") {
		t.Fatalf("pending %+v", h.jobs)
	}
	h.complete("analysis.authorship") // b; c must not take a CPU first
	if !h.pending("analysis.labels") || h.pending("analysis.authorship") {
		t.Fatalf("the producer took the waiting envelope's capacity; pending %+v", h.jobs)
	}
	for len(h.jobs) > 0 {
		h.complete(h.jobs[0].job.Kind)
	}
	if q := h.request(id); q.State != opSucceeded || len(authorship.dirty)+len(labels.dirty) != 0 {
		t.Fatalf("request %+v", q)
	}
	if slices.ContainsFunc(labels.grants, func(n int) bool { return n != 2 }) {
		t.Fatalf("labels grants %v", labels.grants)
	}
}

// Shutdown ends live requests, membership reads and batches as interrupted,
// whether the read sees the service end first or the runner does.
func TestWorkShutdownInterruptsRequestsReadsAndBatches(t *testing.T) {
	domain := newFakeWorkDomain("u1@1")
	h := newWorkHarness(t, 3, workDefinition{Kind: "maintenance.parallelism", Task: domain})
	read := func(ctx context.Context, b *workInventoryBuilder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return b.Add("u1", "1")
	}
	kinds := []string{"maintenance.parallelism"}
	library := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive})
	h.runner.Pump()
	early := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive, Build: read})
	late := h.submit(workRequestSpec{Kinds: kinds, Priority: workPriorityLive, Build: read})
	h.runner.Pump()
	stopped, stop := context.WithCancel(context.Background())
	stop()
	h.take(workMembershipKind, early).run(stopped) // the service context ended first
	if end := h.batches[len(h.batches)-1]; end.Job.Request != early || end.State != opInterrupted || end.Reason != "service-stopping" {
		t.Fatalf("read end %+v", end)
	}
	if q := h.request(early); q.State != opInterrupted || q.Reason != "service-stopping" {
		t.Fatalf("early %+v", q)
	}
	h.runner.Run(stopped) // returns at once and shuts down
	for _, id := range []uint64{library, late} {
		if q := h.request(id); q.State != opInterrupted || q.Reason != "service-stopping" {
			t.Fatalf("request %+v", q)
		}
	}
	if slices.Contains(h.hooks, fmt.Sprint("request ", late)) || h.authority.facts().MemoryReserved != workRequestMemoryLimit {
		t.Fatal("the reading request ended before its read returned")
	}
	h.take(workMembershipKind, late).run(context.Background())
	if end := h.batches[len(h.batches)-1]; end.Job.Request != late || end.State != opInterrupted || end.Reason != "service-stopping" {
		t.Fatalf("read end %+v", end)
	}
	if h.hooks[len(h.hooks)-1] != fmt.Sprint("request ", late) {
		t.Fatalf("hook order %v", h.hooks)
	}
	h.take("maintenance.parallelism", 0).run(context.Background())
	if end := h.batches[len(h.batches)-1]; end.State != opInterrupted || end.Reason != "service-stopping" {
		t.Fatalf("batch end %+v", end)
	}
	if len(h.ended) != 1 || h.ended[0].State != opInterrupted || h.task("maintenance.parallelism").Failures != 0 {
		t.Fatalf("executions %+v", h.ended)
	}
	if facts := h.authority.facts(); facts.MemoryReserved != 0 || facts.CPUAllocated != 0 {
		t.Fatalf("authority %+v", facts)
	}
}

// An exhausted unit of a library request counts once, however many empty
// Nexts a backoff on another unit keeps the task open for.
func TestWorkLibraryFailuresCountOnce(t *testing.T) {
	done, tries := map[string]bool{}, map[string]int{}
	task := &scriptedWorkTask{
		next: func(pop *workPopulation) (workSelection, error) {
			for _, id := range []string{"u1", "u2"} {
				if m := (workMember{id, "1"}); !done[id] && !pop.Excluded(m) {
					return workSelection{Units: []workMember{m}}, nil
				}
			}
			return workSelection{}, nil
		},
		run: func(_ context.Context, sel workSelection, _ workAllocation) (workBatchResult, error) {
			m := sel.Units[0]
			tries[m.ID]++
			switch {
			case m.ID == "u1":
				return workBatchResult{}, workPermanent(errors.New("unsupported input"))
			case tries[m.ID] == 1:
				return workBatchResult{}, errors.New("busy")
			}
			done[m.ID] = true
			return workBatchResult{Committed: sel.Units}, nil
		},
	}
	h := newWorkHarness(t, 1, workDefinition{Kind: "maintenance.parallelism", Task: task})
	id := h.submit(workRequestSpec{Kinds: []string{"maintenance.parallelism"}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete("maintenance.parallelism") // u1 exhausted
	h.complete("maintenance.parallelism") // u2 backs off
	h.runner.Pump()
	h.runner.Pump()
	h.clock.Advance(5 * time.Second)
	h.runner.Pump()
	h.complete("maintenance.parallelism") // u2 commits
	if q := h.request(id); q.State != opFailed || q.Failed != 1 || q.Detail != "1 units failed" {
		t.Fatalf("request %+v", q)
	}
}
