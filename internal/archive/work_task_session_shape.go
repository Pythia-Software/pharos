package archive

import (
	"context"
)

// Session Shape under population execution: stored per-conversation facts,
// a projection like stored activity (parallelism). Its unit is the
// conversation; the domain keeps its dirty revisions and revision-guarded
// publication (session_shape.go).
//
// It has no prerequisite. Facts that need writing estimates stay unknown
// until estimates land, and the message_authorship triggers then dirty the
// conversation again, so waiting on analysis.authorship would only hold the
// projection behind a pausable analysis without changing what is stored.
func (s *Server) sessionShapeWorkDefinition() workDefinition {
	c := s.Catalog
	task := &conversationWorkTask{server: s, kind: kindSessionShape, cpus: 1, pending: c.sessionShapePendingFor, filterFailures: true,
		selectSQL: sessionShapeSelectionSQL(),
		step: func(ctx context.Context, ids []string, _ workAllocation) (workDomainStep, error) {
			return c.refreshSessionShapeFor(ctx, ids)
		}}
	return workDefinition{Kind: kindSessionShape, Task: projectionHeldTask{catalog: c, workTask: task}}
}

// projectionHeldTask makes a stored-projection task honor the Search view's
// Stop, as the classic maintainLibrary pass that ran it did: no new batch
// while stopped, and Stop cancels a batch in progress, whose units stay
// dirty. Resume (resumeLibraryRefresh) wakes the runner.
type projectionHeldTask struct {
	workTask
	catalog *Catalog
}

var projectionStopped = workBlocker{Reason: workBlockedHeld, Detail: "the Search view refresh is stopped"}

func (t projectionHeldTask) Next(ctx context.Context, population *workPopulation) (workSelection, error) {
	if t.catalog.libraryRefreshStopped() {
		return workSelection{Blocker: projectionStopped}, nil
	}
	return t.workTask.Next(ctx, population)
}

func (t projectionHeldTask) RunBatch(ctx context.Context, selection workSelection, allocation workAllocation) (workBatchResult, error) {
	pass, done, running := t.catalog.libraryPass(ctx)
	if !running {
		return workBatchResult{}, workBlocked(projectionStopped)
	}
	defer done()
	result, err := t.workTask.RunBatch(pass, selection, allocation)
	if err != nil && pass.Err() != nil && ctx.Err() == nil {
		return workBatchResult{}, workBlocked(projectionStopped)
	}
	return result, err
}
