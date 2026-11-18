package archive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// backupRuns tracks the service's backups; one runs at a time.
type backupRuns struct {
	// locations serializes removing a location with claiming its next run.
	locations sync.Mutex
	mu        sync.Mutex
	runs      []*BackupRun
	cancel    context.CancelFunc
	activeID  string
}

var errBackupBusy = errors.New("a backup is already running")

func (r *backupRuns) begin(run BackupRun, cancel context.CancelFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return errBackupBusy
	}
	r.cancel = cancel
	r.activeID = run.ID
	r.pushLocked(run)
	return nil
}

func (r *backupRuns) push(run BackupRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushLocked(run)
}

func (r *backupRuns) pushLocked(run BackupRun) {
	r.runs = append([]*BackupRun{&run}, r.runs...)
	seen := map[string]bool{}
	kept := r.runs[:0]
	for index, item := range r.runs {
		if index < 10 || !seen[item.Destination] || r.cancel != nil && item.ID == r.activeID {
			kept = append(kept, item)
		}
		seen[item.Destination] = true
	}
	clear(r.runs[len(kept):])
	r.runs = kept
}

func (r *backupRuns) update(run BackupRun, done bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.runs {
		if item.ID == run.ID {
			*item = run
		}
	}
	if done && run.ID == r.activeID {
		r.cancel = nil
		r.activeID = ""
	}
}

// recordRefused keeps a backup that could not start, as its destination's
// latest run.
func (r *backupRuns) recordRefused(destination string, err error) BackupRun {
	at := now()
	run := BackupRun{ID: randomToken(), Kind: "backup", State: "failed", Phase: "failed", Destination: destination, StartedAt: at, UpdatedAt: at, CompletedAt: at,
		Errors: []captureError{{Error: err.Error()}}, Warnings: []string{}, Missing: []string{}}
	r.push(run)
	return run
}

// forget drops a removed location's runs, and with them any failure reported
// for it.
func (r *backupRuns) forget(destination string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = slices.DeleteFunc(r.runs, func(run *BackupRun) bool { return run.Destination == destination })
}

func (r *backupRuns) stop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return false
	}
	r.cancel()
	return true
}

func (r *backupRuns) status() (bool, []BackupRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := make([]BackupRun, 0, len(r.runs))
	if r.cancel != nil {
		for _, run := range r.runs {
			if run.ID == r.activeID {
				runs = append(runs, run.clone())
				break
			}
		}
	}
	for _, run := range r.runs {
		if r.cancel != nil && run.ID == r.activeID {
			continue
		}
		runs = append(runs, run.clone())
	}
	return r.cancel != nil, runs
}

func (s *Server) backupStatus() map[string]any {
	active, runs := s.backups.status()
	history := loadBackupHistory(s.Config())
	runs = currentBackupRuns(history, runs)
	var latest any
	if len(runs) > 0 {
		latest = runs[0]
	}
	var last any
	if len(history.Backups) > 0 {
		last = history.Backups[0]
	}
	return map[string]any{"active": active, "run": latest, "runs": runs, "last_success": last, "record": backupRecordPath(s.Config()),
		"locations": s.backupLocationsStatus(history, runs), "suggested_destination": suggestedBackupDestination(s.Config(), history)}
}

// suggestedBackupDestination is the first location the UI proposes: for a
// library on another drive, a folder in this Mac's home. A library with a
// location already, or on this Mac's own disk, has none.
func suggestedBackupDestination(config Config, history backupHistory) string {
	if len(history.locations()) > 0 {
		return ""
	}
	home, err := os.UserHomeDir()
	if !config.Library || err != nil || libraryDriveOf(config).Location == "internal" {
		return ""
	}
	return filepath.Join(home, "Pharos Backup")
}

// startBackup checks the destination, adds it to the backup locations, then
// backs up in the background; progress is polled with GET /api/backup, and
// POST /api/backup/cancel or a release (before an eject) stops it between
// files or catalog pages.
func (s *Server) startBackup(w http.ResponseWriter, body map[string]any) {
	destination, _ := body["destination"].(string)
	prune, _ := body["prune"].(bool)
	if _, ok := body["prune"]; ok && body["prune"] != true && body["prune"] != false {
		writeError(w, fmt.Errorf("prune must be true or false"), http.StatusBadRequest)
		return
	}
	job, err := prepareBackup(s.Config(), destination, prune)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	started := job.run.clone()
	ctx, cancel := context.WithCancel(s.life.ctx)
	s.backups.locations.Lock()
	defer s.backups.locations.Unlock()
	if active, _ := s.backups.status(); active {
		cancel()
		writeError(w, errBackupBusy, http.StatusConflict)
		return
	}
	// A failed first backup stays listed, with its error, so it can be retried.
	if err := addBackupLocation(s.Config(), started.Destination); err != nil {
		cancel()
		status := http.StatusInternalServerError
		if errors.Is(err, errBackupLocationOverlap) {
			status = http.StatusBadRequest
		}
		writeError(w, err, status)
		return
	}
	if err := s.backups.begin(started, cancel); err != nil {
		cancel()
		writeError(w, err, http.StatusConflict)
		return
	}
	s.forgetBackupSchedule()
	_, handle := s.begin(s.life.ctx, OperationSpec{Kind: "backup.local", Subject: started.Destination, Trigger: triggerUser, Legacy: started.ID})
	s.launch(handle, func(context.Context, *Op) error {
		defer cancel()
		run := started
		defer func() {
			if value := recover(); value != nil {
				s.exitIfFault(value)
				run.State, run.Phase, run.CompletedAt = "failed", "failed", now()
				run.Errors = append(run.Errors, captureError{Error: fmt.Sprintf("backup stopped: %v", value)})
			}
			s.backups.update(run, true)
			s.forgetBackupSchedule()
		}()
		run = job.Run(ctx, func(progress BackupRun) {
			s.backups.update(progress, false)
			mirrorBackup(handle, progress)
		})
		return backupOutcome(ctx, run)
	})
	writeJSON(w, map[string]any{"ok": true, "run": started}, http.StatusAccepted)
}

// mirrorBackup follows a backup's progress: its phase and bytes, and the
// wait for a capture as a block.
func mirrorBackup(op *Op, run BackupRun) {
	if run.Phase == backupWaitingForCapture {
		op.Block("capture-running", "a capture on this Mac to finish")
		return
	}
	op.Unblock()
	op.progress(defaultString(run.Phase, "starting"), "", "byte", float64(run.BytesCopied), float64(run.BytesTotal), run.BytesTotal > 0)
}

// backupOutcome is a finished backup's result for its operation: a partial
// backup succeeded with what it could copy, a cancelled one ends from its
// context (a user stop or a release).
func backupOutcome(ctx context.Context, run BackupRun) error {
	switch run.State {
	case "cancelled":
		return defaultError(ctx.Err(), errors.New("backup cancelled"))
	case "failed":
		if len(run.Errors) > 0 {
			return errors.New(run.Errors[0].Error)
		}
		return errors.New("backup failed")
	}
	return nil
}
