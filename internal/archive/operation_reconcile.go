package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The service's reconcile pass (docs/background-work.md §6.4, §6.5) runs on
// its own timer, apart from the journal writer's rounds and from any read:
// it applies the outcomes dead CLI processes left in their leases, deletes
// those leases once the outcomes are written, and ends open rows whose owner
// is gone (a previous service instance, a CLI that crashed) as interrupted,
// always with conditional updates that leave a row its owner ended alone.

// leaseReconcileInterval spaces reconcile passes; tests shorten it.
var leaseReconcileInterval = 10 * time.Second

// startJournal starts the journal writer and the reconcile pass, and asks
// for the retention rule at start.
func (s *Server) startJournal() {
	if s.journal == nil {
		return
	}
	s.journal.requestPrune()
	s.daemon("journalWriter", s.journal.run)
	s.daemon("leaseReconciler", s.reconcileLoop)
}

func (s *Server) reconcileLoop(ctx context.Context) {
	s.reconcileOperations(ctx)
	ticker := time.NewTicker(leaseReconcileInterval)
	defer ticker.Stop()
	pruned := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if time.Since(pruned) >= 24*time.Hour {
			s.journal.requestPrune()
			pruned = time.Now()
		}
		s.reconcileOperations(ctx)
	}
}

// reconcileOperations runs one pass and reports whether its writes landed.
func (s *Server) reconcileOperations(ctx context.Context) bool {
	if s.journal == nil {
		return true
	}
	dirs := leaseDirs(s.Config())
	host := currentHost().ID
	// Dead leases are claimed only long enough to read them, so a verdict
	// probe meanwhile does not take them for live; they are claimed again to
	// be deleted once their outcomes are written.
	deadPaths := []string{}
	dead := map[string]*leaseInfo{}
	// Unheld lease files: their recorded outcomes first.
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, done, ok := claimDeadLease(filepath.Join(dir, entry.Name()))
			if !ok {
				continue
			}
			done(false)
			// Another Mac's is never touched here. One without a host (a
			// direct MCP call's) is this Mac's: only a process here could
			// have written it while holding its flock.
			if info != nil && info.HostID != "" && info.HostID != host {
				continue
			}
			instance := strings.TrimSuffix(entry.Name(), ".json")
			deadPaths = append(deadPaths, filepath.Join(dir, entry.Name()))
			dead[instance] = info
			if info == nil {
				continue // unreadable and unheld: nothing to apply
			}
			for id, outcome := range info.Outcomes {
				s.journal.queueReconcile(journalReconcile{ID: id, Instance: info.Instance, State: outcome.State, Reason: outcome.Reason,
					Error: outcome.Error, EndedAt: defaultString(outcome.EndedAt, now())})
			}
		}
	}
	// Open rows of other instances on this Mac, each checked against its own
	// lease at this moment.
	rows, err := s.Catalog.openJournal(ctx, s.ops.instance)
	if err == nil {
		for _, row := range rows {
			info, claimedDead := dead[row.Instance]
			if !claimedDead {
				probe, found := findLease(dirs, row.Instance)
				if found && (probe.Live || probe.Err != nil) {
					continue // alive, or unknown: leave it
				}
				info = probe.Info
			}
			entry := journalReconcile{ID: row.ID, Instance: row.Instance, State: opInterrupted, Reason: "owner-exited", EndedAt: now()}
			if info != nil {
				if outcome, ok := info.Outcomes[row.ID]; ok {
					entry.State, entry.Reason, entry.Error = outcome.State, outcome.Reason, outcome.Error
					entry.EndedAt = defaultString(outcome.EndedAt, entry.EndedAt)
				}
			}
			s.journal.queueReconcile(entry)
		}
	}
	written := s.journal.round(ctx, 0) == nil && !s.journal.hasReconciles()
	if written {
		// The outcomes are in the journal: delete the dead leases, unlinked
		// while their lock is held.
		for _, path := range deadPaths {
			if _, done, ok := claimDeadLease(path); ok {
				done(true)
			}
		}
	}
	return written
}

// hasReconciles reports conditional updates still waiting for a round.
func (j *journalWriter) hasReconciles() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.reconciles) > 0
}
