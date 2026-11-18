package archive

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

// A library can be backed up to several folders, usually on different drives.
// The list lives in backups.json, so it travels with the library. Each location
// is a complete, independent backup with its own manifest; none depends on
// another, and a drive that is away only delays its own copy.
//
// Libraries from before the list have no "locations" key: their one location
// is the last backup's destination, which is what the schedule used. The first
// change to the list writes it out, and from then on an empty list means the
// owner removed every location.
type backupLocation struct {
	Path    string `json:"path"`
	AddedAt string `json:"added_at,omitempty"`
}

func (h backupHistory) locations() []backupLocation {
	if h.Locations != nil {
		return h.Locations
	}
	if len(h.Backups) == 0 {
		return []backupLocation{}
	}
	return []backupLocation{{Path: h.Backups[0].Destination, AddedAt: h.Backups[0].FinishedAt}}
}

// lastBackupTo is the newest finished backup to path, if any.
func (h backupHistory) lastBackupTo(path string) *backupRecord {
	for index := range h.Backups {
		if h.Backups[index].Destination == path {
			return &h.Backups[index]
		}
	}
	return nil
}

// Failed and stopped outcomes travel with the library. A success or removal
// clears only its own location; an empty list distinguishes reconciled history from older
// libraries whose background failure did not name its destinations separately.
func clearBackupOutcome(history *backupHistory, path string) {
	if history.UnfinishedRuns == nil {
		history.UnfinishedRuns = []BackupRun{}
	}
	history.UnfinishedRuns = slices.DeleteFunc(history.UnfinishedRuns, func(run BackupRun) bool { return run.Destination == path })
}

func recordBackupOutcome(config Config, run BackupRun) error {
	_, err := updateBackupHistory(config, func(history *backupHistory) bool {
		locations := history.locations()
		index := slices.IndexFunc(locations, func(location backupLocation) bool { return location.Path == run.Destination })
		if index < 0 {
			return false
		}
		// Removing and re-adding a folder starts a new registration. A run
		// from its earlier registration must not restore that outcome.
		started, startedOK := parseTime(run.StartedAt)
		added, addedOK := parseTime(locations[index].AddedAt)
		if startedOK && addedOK && started.Before(added) {
			return false
		}
		updated, _ := parseTime(defaultString(run.UpdatedAt, run.StartedAt))
		if record := history.lastBackupTo(run.Destination); record != nil {
			if finished, ok := parseTime(record.FinishedAt); ok && finished.After(updated) {
				return false
			}
		}
		for _, saved := range history.UnfinishedRuns {
			if saved.Destination == run.Destination {
				previous, _ := parseTime(defaultString(saved.UpdatedAt, saved.StartedAt))
				if previous.After(updated) {
					return false
				}
			}
		}
		known := history.UnfinishedRuns != nil
		before := len(history.UnfinishedRuns)
		clearBackupOutcome(history, run.Destination)
		if run.State == "failed" || run.State == "cancelled" {
			history.UnfinishedRuns = append([]BackupRun{run.clone()}, history.UnfinishedRuns...)
			return true
		}
		return !known || len(history.UnfinishedRuns) != before
	})
	return err
}

// currentBackupRuns reconciles process history with persisted outcomes, so a
// CLI success can resolve a service failure and a restart retains failures.
func currentBackupRuns(history backupHistory, runs []BackupRun) []BackupRun {
	current := make([]BackupRun, 0, len(runs)+len(history.UnfinishedRuns))
	seen := map[string]bool{}
	superseded := func(run BackupRun) bool {
		if record := history.lastBackupTo(run.Destination); record != nil && (run.State == "failed" || run.State == "partial" || run.State == "cancelled") {
			finished, finishedOK := parseTime(record.FinishedAt)
			updated, updatedOK := parseTime(defaultString(run.UpdatedAt, run.StartedAt))
			if finishedOK && updatedOK && finished.After(updated) {
				return true
			}
		}
		return false
	}
	for _, run := range runs {
		seen[run.ID] = true
		if !superseded(run) {
			current = append(current, run)
		}
	}
	for _, run := range history.UnfinishedRuns {
		if seen[run.ID] || superseded(run) {
			continue
		}
		// A CLI failure may be newer than this process's last run. Keep an
		// active run first, then merge persisted outcomes chronologically.
		index := slices.IndexFunc(current, func(other BackupRun) bool {
			return other.State != "pending" && other.State != "running" && defaultString(other.UpdatedAt, other.StartedAt) < defaultString(run.UpdatedAt, run.StartedAt)
		})
		if index < 0 {
			index = len(current)
		}
		current = slices.Insert(current, index, run)
	}
	return current
}

func (s *Server) backupRunsStatus(cachedOnly bool) (bool, []BackupRun) {
	active, runs := s.backups.status()
	if cachedOnly {
		if files := s.freshnessFiles(s.Config(), nil, true); files != nil && files.backupHistory != nil {
			runs = currentBackupRuns(*files.backupHistory, runs)
		}
		return active, runs
	}
	return active, currentBackupRuns(loadBackupHistory(s.Config()), runs)
}

// trimBackupHistory keeps the newest records, and each location's newest record
// however old: a drive connected once a season must not lose its last backup
// to the daily backups of another.
func trimBackupHistory(history *backupHistory) {
	slices.SortStableFunc(history.Backups, func(a, b backupRecord) int {
		left, _ := parseTime(a.FinishedAt)
		right, _ := parseTime(b.FinishedAt)
		return right.Compare(left)
	})
	if len(history.Backups) <= backupHistoryLimit {
		return
	}
	locations := map[string]bool{}
	for _, location := range history.locations() {
		locations[location.Path] = true
	}
	kept := make([]backupRecord, 0, backupHistoryLimit+len(locations))
	for index, record := range history.Backups {
		if index < backupHistoryLimit || locations[record.Destination] {
			kept = append(kept, record)
		}
		delete(locations, record.Destination)
	}
	history.Backups = kept
}

var errBackupLocationOverlap = errors.New("backup locations overlap")

// A reservation outlives removal from the scheduled locations. Its shared
// file lock is held through copying and pruning, including in CLI processes.
// The OS releases it after a crash; stale metadata can then be reclaimed.
func backupReservationPath(config Config, path string) string {
	return filepath.Join(filepath.Dir(backupRecordPath(config)), backupReservationsDir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(path))))
}

func liveBackupReservations(config Config, history *backupHistory) error {
	kept := make([]string, 0, len(history.Reservations))
	for _, path := range history.Reservations {
		name := backupReservationPath(config, path)
		file, err := os.OpenFile(name, os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot check the active backup to %s: %w", path, err)
		}
		err = filelock.Lock(file, filelock.Exclusive, filelock.Try)
		switch {
		case errors.Is(err, filelock.ErrContended):
			kept = append(kept, path)
		case err == nil:
			// All reservation opens and removals hold the history lock, so
			// nobody can start using this lock file while it is reclaimed.
			err = os.Remove(name)
		}
		file.Close()
		if err != nil && !errors.Is(err, filelock.ErrContended) && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot check the active backup to %s: %w", path, err)
		}
	}
	history.Reservations = kept
	return nil
}

func backupReservationOverlap(config Config, history *backupHistory, path string) error {
	if err := liveBackupReservations(config, history); err != nil {
		return err
	}
	for _, active := range history.Reservations {
		if active != path && (within(active, path) || within(path, active)) {
			return fmt.Errorf("%w: %s overlaps the running backup to %s; wait for it to finish", errBackupLocationOverlap, path, active)
		}
	}
	return nil
}

func reserveBackupLocation(config Config, path string) (func(), error) {
	var file *os.File
	var validation error
	_, err := updateBackupHistory(config, func(history *backupHistory) bool {
		if validation = backupLocationOverlap(*history, path); validation != nil {
			return false
		}
		if validation = backupReservationOverlap(config, history, path); validation != nil {
			return false
		}
		name := backupReservationPath(config, path)
		if validation = os.MkdirAll(filepath.Dir(name), 0o700); validation != nil {
			return false
		}
		file, validation = os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
		if validation == nil {
			validation = filelock.Lock(file, filelock.Shared, filelock.Try)
		}
		if validation != nil {
			return false
		}
		if !slices.Contains(history.Reservations, path) {
			history.Reservations = append(history.Reservations, path)
		}
		return true
	})
	if err = errors.Join(err, validation); err != nil {
		if file != nil {
			file.Close()
		}
		return nil, err
	}
	return func() {
		file.Close()
		// A failed cleanup leaves a stale entry, not a live reservation:
		// the next registration reclaims it using the released OS lock.
		_, _ = updateBackupHistory(config, func(history *backupHistory) bool {
			before := slices.Clone(history.Reservations)
			return liveBackupReservations(config, history) == nil && !slices.Equal(before, history.Reservations)
		})
	}, nil
}

// backupLocationOverlap refuses a folder inside another location, or holding
// one: pruning either backup would delete the other.
func backupLocationOverlap(history backupHistory, dest string) error {
	for _, location := range history.locations() {
		if location.Path != dest && (within(dest, location.Path) || within(location.Path, dest)) {
			return fmt.Errorf("%w: %s overlaps the backup location %s; choose a separate folder", errBackupLocationOverlap, dest, location.Path)
		}
	}
	return nil
}

// addBackupLocation rechecks overlap under the history lock: another process
// may have registered a folder since prepareBackup checked the destination.
func addBackupLocation(config Config, path string) error {
	var validation error
	_, err := updateBackupHistory(config, func(history *backupHistory) bool {
		if validation = backupLocationOverlap(*history, path); validation != nil {
			return false
		}
		if validation = backupReservationOverlap(config, history, path); validation != nil {
			return false
		}
		locations := history.locations()
		known := slices.ContainsFunc(locations, func(location backupLocation) bool { return location.Path == path })
		if known && history.Locations != nil {
			return false
		}
		history.Locations = slices.Clone(locations)
		if !known {
			history.Locations = append(history.Locations, backupLocation{Path: path, AddedAt: now()})
		}
		return true
	})
	return errors.Join(err, validation)
}

// removeBackupLocation stops backing up to path. The backup already there
// stays, and adding the folder again carries on from it.
func removeBackupLocation(config Config, path string) (bool, error) {
	removed := false
	_, err := updateBackupHistory(config, func(history *backupHistory) bool {
		kept := slices.DeleteFunc(slices.Clone(history.locations()), func(location backupLocation) bool { return location.Path == path })
		removed = len(kept) != len(history.locations())
		if removed {
			history.Locations = kept
			clearBackupOutcome(history, path)
		}
		return removed
	})
	return removed, err
}

// backupLocationProbeLimit bounds the check of one location, so a network
// share that stopped answering cannot hold up a status request.
var backupLocationProbeLimit = 2 * time.Second

// backupLocationConnected reports whether path holds a backup right now. It
// asks for the manifest rather than the folder: scheduled backups go only to a
// backup that is already there, never into a same-named folder on whatever
// drive is mounted at that path today, and never onto the startup disk under a
// missing mount point.
func backupLocationConnected(path string) bool {
	found := make(chan bool, 1)
	go func() {
		_, err := os.Stat(filepath.Join(path, backupManifestName))
		found <- err == nil
	}()
	select {
	case connected := <-found:
		return connected
	case <-time.After(backupLocationProbeLimit):
		return false
	}
}

// backupSchedule is what the background schedule needs from the locations.
type backupSchedule struct {
	locations []string
	connected int
	// lastDone is the oldest newest-backup among connected locations, so a
	// drive that has been away is due as soon as it returns. With none
	// connected it is the newest backup anywhere, for display.
	lastDone           string
	failureKnown       bool
	failureAt, failure string
}

func readBackupSchedule(config Config) backupSchedule {
	history := loadBackupHistory(config)
	schedule := backupSchedule{}
	schedule.failureKnown = history.UnfinishedRuns != nil
	for _, run := range currentBackupRuns(history, nil) {
		stamp := defaultString(run.UpdatedAt, run.StartedAt)
		if stamp > schedule.failureAt {
			schedule.failureAt = stamp
		}
		if run.State == "failed" {
			for _, problem := range run.Errors {
				schedule.failure = joinDetail(schedule.failure, run.Destination+": "+problem.Error)
			}
		}
	}
	oldest := time.Time{}
	unfinished := false
	for _, location := range history.locations() {
		schedule.locations = append(schedule.locations, location.Path)
		if !backupLocationConnected(location.Path) {
			continue
		}
		schedule.connected++
		record := history.lastBackupTo(location.Path)
		if record == nil {
			unfinished = true
			continue
		}
		if finished, ok := parseTime(record.FinishedAt); !ok {
			unfinished = true
		} else if oldest.IsZero() || finished.Before(oldest) {
			oldest, schedule.lastDone = finished, record.FinishedAt
		}
	}
	if unfinished {
		schedule.lastDone = ""
	} else if schedule.connected == 0 && len(history.Backups) > 0 {
		schedule.lastDone = history.Backups[0].FinishedAt
	}
	return schedule
}

// backupLocationDue applies the item's cadence to one location, so a retry
// after another drive failed does not copy again to the drives that succeeded.
func backupLocationDue(settings backgroundSettings, item backgroundItem, last *backupRecord, at time.Time) bool {
	plan := itemPlan{ID: item.ID, Cadence: settings.cadence(item)}
	if last != nil {
		plan.LastDone, _ = parseTime(last.FinishedAt)
	}
	standaloneNext(&plan, settings, at, at)
	return !plan.Next.After(at)
}

// latestBackupRuns is each destination's newest run; runs are newest first.
func latestBackupRuns(runs []BackupRun) map[string]BackupRun {
	latest := map[string]BackupRun{}
	for _, run := range runs {
		if _, seen := latest[run.Destination]; !seen {
			latest[run.Destination] = run
		}
	}
	return latest
}

// failedBackupRuns are the runs that are their destination's latest and
// failed, newest first. A success at one location says nothing about another.
func failedBackupRuns(runs []BackupRun) []BackupRun {
	latest := latestBackupRuns(runs)
	failed := []BackupRun{}
	for _, run := range runs {
		if run.State == "failed" && latest[run.Destination].ID == run.ID {
			failed = append(failed, run)
		}
	}
	return failed
}

// backupLocationsStatus is the list Settings shows.
func (s *Server) backupLocationsStatus(history backupHistory, runs []BackupRun) []map[string]any {
	latest := latestBackupRuns(runs)
	locations := []map[string]any{}
	for _, location := range history.locations() {
		entry := map[string]any{"path": location.Path, "added_at": location.AddedAt, "connected": backupLocationConnected(location.Path), "last_backup": nil, "last_run": nil}
		if record := history.lastBackupTo(location.Path); record != nil {
			entry["last_backup"] = *record
		}
		if run, ok := latest[location.Path]; ok {
			entry["last_run"] = run
		}
		locations = append(locations, entry)
	}
	return locations
}

// removeBackupLocationRequest handles POST /api/backup/locations/remove.
func (s *Server) removeBackupLocationRequest(w http.ResponseWriter, body map[string]any) {
	path, _ := body["path"].(string)
	if strings.TrimSpace(path) == "" {
		writeError(w, errors.New("a backup location is required"), http.StatusBadRequest)
		return
	}
	s.backups.locations.Lock()
	defer s.backups.locations.Unlock()
	if active, runs := s.backups.status(); active && len(runs) > 0 && runs[0].Destination == path {
		writeError(w, errors.New("a backup to this location is running; stop it first"), http.StatusConflict)
		return
	}
	removed, err := removeBackupLocation(s.Config(), path)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if !removed {
		writeError(w, fmt.Errorf("%s is not a backup location", path), http.StatusNotFound)
		return
	}
	s.backups.forget(path)
	s.forgetBackupSchedule()
	writeJSON(w, map[string]any{"removed": true}, http.StatusOK)
}

// backupToLocation runs one location's backup inside a background pass. A
// location refused before it starts is recorded as a failed run, so Settings
// and the status can say which one needs attention.
var errBackupLocationRemoved = errors.New("backup location removed")

func (s *Server) backupToLocation(ctx context.Context, op *Op, config Config, path string) error {
	job, err := prepareBackup(config, path, false)
	s.backups.locations.Lock()
	if !slices.ContainsFunc(loadBackupHistory(config).locations(), func(location backupLocation) bool { return location.Path == path }) {
		s.backups.locations.Unlock()
		return errBackupLocationRemoved
	}
	if err != nil {
		run := s.backups.recordRefused(path, err)
		if saved := recordBackupOutcome(config, run); saved != nil {
			err = errors.Join(err, saved)
		}
		s.backups.locations.Unlock()
		return err
	}
	started := job.run.clone()
	backupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := s.backups.begin(started, cancel); err != nil {
		s.backups.locations.Unlock()
		return err
	}
	s.backups.locations.Unlock()
	_, child := op.Child(OperationSpec{Kind: "backup.local", Subject: started.Destination, Trigger: triggerChained, Legacy: started.ID})
	run := started
	defer func() { s.backups.update(run, true) }()
	run = job.Run(backupCtx, func(progress BackupRun) {
		s.backups.update(progress, false)
		mirrorBackup(child, progress)
	})
	err = backupOutcome(backupCtx, run)
	child.Finish(err)
	return err
}

// backgroundBackup backs up to every connected location that is due, the
// longest-waiting first; Run now takes every connected one. A drive that is
// away waits, and one location's failure does not stop the others. A stop
// ends the whole pass.
func (s *Server) backgroundBackup(ctx context.Context, op *Op) error {
	config := s.Config()
	defer s.forgetBackupSchedule()
	history := loadBackupHistory(config)
	locations := slices.Clone(history.locations())
	if len(locations) == 0 {
		return nil
	}
	finished := func(location backupLocation) time.Time {
		at := time.Time{}
		if record := history.lastBackupTo(location.Path); record != nil {
			at, _ = parseTime(record.FinishedAt)
		}
		return at
	}
	slices.SortStableFunc(locations, func(a, b backupLocation) int { return finished(a).Compare(finished(b)) })
	at := time.Now()
	explicit := backgroundExplicit(ctx)
	settings, settingsErr := s.Catalog.backgroundSettings(ctx, at)
	item, _ := backgroundItemByID("backup")
	connected := 0
	failures := []string{}
	for _, location := range locations {
		if err := ctx.Err(); err != nil {
			return err
		}
		history = loadBackupHistory(config)
		if !slices.ContainsFunc(history.locations(), func(saved backupLocation) bool { return saved.Path == location.Path }) {
			continue
		}
		if !backupLocationConnected(location.Path) {
			continue
		}
		connected++
		if !explicit && settingsErr == nil && !backupLocationDue(settings, item, history.lastBackupTo(location.Path), at) {
			continue
		}
		err := s.backupToLocation(ctx, op, config, location.Path)
		switch {
		case err == nil:
		case errors.Is(err, errBackupLocationRemoved):
		case ctx.Err() != nil, errors.Is(err, context.Canceled), errors.Is(err, errBackupBusy):
			return err
		default:
			failures = append(failures, location.Path+": "+err.Error())
		}
	}
	if connected == 0 && explicit {
		return errors.New("no backup location is connected; connect a backup drive and run this again")
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
