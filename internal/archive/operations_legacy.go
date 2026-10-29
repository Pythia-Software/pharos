package archive

import "slices"

// Dual-write (docs/background-work.md §2.3): the existing run records stay
// exactly as they are, and the registry follows them. A SyncRun linked to an
// operation mirrors each update into it (updateRun), so the operation's step
// is the run's phase and its measured progress, and the run's terminal state
// becomes the operation's outcome. Nothing here writes to the run.

// runOperationKinds maps a SyncRun kind (and Capture & index mode) to its
// operation kind and variant (§8.1).
func runOperationKind(kind, mode string) (string, string) {
	switch kind {
	case automaticRunKind:
		return "index.changes.automatic", ""
	case manualSyncRunKind:
		return "index.changes.manual", ""
	case indexRunKind:
		return "index.captures", ""
	case "verification":
		return "index.verify", "all"
	case "source-sync":
		return "index.sources.legacy", ""
	case "library-update":
		if mode == "full" || mode == "retained" {
			return "library.rebuild", mode
		}
		return "library.update", ""
	}
	return unknownOperationKind, ""
}

// startRunFor starts a SyncRun shadowed by op.
func (s *Server) startRunFor(op *Op, kind string, names []string) *SyncRun {
	run := s.startRunNamed(kind, names)
	if op != nil {
		op.reg.mu.Lock()
		op.spec.Legacy = run.ID
		op.reg.changedLocked(op, false)
		op.reg.mu.Unlock()
		s.runsMu.Lock()
		run.op = op
		s.runsMu.Unlock()
		s.updateRun(run.ID, func(*SyncRun) {})
	}
	return run
}

// mirrorRun follows run's state, phase and progress. updateRun calls it with
// runsMu held; it takes only the registry's lock.
func (op *Op) mirrorRun(run *SyncRun) {
	if op == nil {
		return
	}
	forecast := run.operation
	stop := run.StopRequested && run.State == "running"
	if op.lock() {
		if !slices.Equal(op.sources, run.Sources) {
			op.sources = slices.Clone(run.Sources)
			op.unlock(false)
		} else {
			op.reg.mu.Unlock()
		}
	}
	if run.State != "running" {
		op.legacyOutcome(run.State, run.Error)
		return
	}
	op.reg.mu.Lock()
	if forecast != nil && op.forecast == nil {
		op.forecast = forecast
	}
	if stop && (op.state == opRunning || op.state == opBlocked) {
		op.userStop = true
		op.transitionLocked(opStopping)
		op.reg.changedLocked(op, false)
	}
	op.reg.mu.Unlock()
	switch {
	case run.Work != nil:
		op.mirrorWork(*run.Work)
	case run.Kind == indexRunKind && run.Phase == "indexing" && run.TotalConversations > 0:
		op.progress(run.Phase, firstString(run.CurrentSource), "conversation", float64(run.DoneConversations), float64(run.TotalConversations), true)
	default:
		op.progress(run.Phase, firstString(run.CurrentSource), "", 0, -1, false)
	}
}
