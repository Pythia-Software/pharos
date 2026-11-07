package archive

import (
	"context"
	"testing"
)

// An ordinary serial maintenance step joins population execution by one
// declaration: no daemon, no scheduler branch. A one-time backfill runs its
// steps under admission until it reports nothing more, then covers later
// requests without running; an armed task runs only when asked.
func TestMaintenanceTaskIsAnOrdinarySerialTask(t *testing.T) {
	steps := 0
	backfill := &maintenanceTask{kind: kindSubstring, phase: "build", once: true, step: func(context.Context) (bool, int64, error) {
		steps++
		return steps < 3, 1, nil
	}}
	refined := 0
	armed := &maintenanceTask{kind: kindRefine, phase: "refine", step: func(context.Context) (bool, int64, error) {
		refined++
		return false, 1, nil
	}}
	h := newWorkHarness(t, 1,
		workDefinition{Kind: backfill.kind, Coarse: true, Task: backfill},
		workDefinition{Kind: armed.kind, Coarse: true, Task: armed})

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

	unarmed := h.submit(workRequestSpec{Kinds: []string{armed.kind}, Priority: workPriorityLive})
	h.runner.Pump()
	if h.pending(armed.kind) || h.request(unarmed).State != opSucceeded || refined != 0 {
		t.Fatal("an unarmed refinement ran")
	}
	armed.arm(findingsMeasure)
	asked := h.submit(workRequestSpec{Kinds: []string{armed.kind}, Priority: workPriorityLive})
	h.runner.Pump()
	h.complete(armed.kind)
	if q := h.request(asked); q.State != opSucceeded || refined != 1 || h.pending(armed.kind) {
		t.Fatalf("request %+v, refined %d", q, refined)
	}
}

// Findings run in the strongest mode armed since their last pass, and only
// measure when their pending generation alone started them.
func TestMaintenanceFindingsModeIsTheStrongestArmed(t *testing.T) {
	var modes []findingsMode
	task := &maintenanceTask{kind: kindFindings, step: func(ctx context.Context) (bool, int64, error) {
		modes = append(modes, findingsModeFrom(ctx))
		return false, 1, nil
	}}
	task.arm(findingsMeasure)
	task.arm(findingsForce)
	task.arm(findingsAuto)
	if _, err := task.RunBatch(context.Background(), workSelection{}, workAllocation{CPUs: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := task.RunBatch(context.Background(), workSelection{}, workAllocation{CPUs: 1}); err != nil {
		t.Fatal(err)
	}
	if len(modes) != 2 || modes[0] != findingsForce || modes[1] != findingsMeasure {
		t.Fatalf("modes %v", modes)
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
