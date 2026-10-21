package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// GET /api/library/status is the one answer to "what holds or writes the
// library right now, and how do I disconnect its drive?", for the UI and the
// app.
//
// While the service runs it has the catalog open (and SQLite its -wal and
// -shm files), whatever else is running, so the way to disconnect the drive
// is always Eject: the app first asks the service to release the library
// (POST /api/release), which stops running work at a safe point and closes
// the catalog, or refuses the eject with the reason. Unplugging without
// ejecting is only "probably fine" when nothing is running: every commit is
// atomic and flushed, so nothing committed is lost, but work in progress is,
// and the next run has to redo it.

// libraryActivity is one piece of work that holds or writes the library.
type libraryActivity struct {
	operation  *operationForecast
	Forecast   *operationEstimate `json:"forecast,omitempty"`
	timingKind string
	ID         string `json:"id,omitempty"`
	// Kind is sync, index, capture, capture-other (a capture by another
	// process, such as the CLI), backup, git, or maintenance.
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Progress is the measured fraction of Work's current stage, not of the
	// whole operation. nil means no trustworthy denominator is available.
	Progress              *float64          `json:"progress"`
	StartedAt             string            `json:"started_at,omitempty"`
	EstimatedCompletionAt string            `json:"estimated_completion_at,omitempty"`
	Work                  *activityWork     `json:"work,omitempty"`
	Estimate              *activityEstimate `json:"estimate,omitempty"`
	// Writes is true for work that writes to the library's drive; a backup
	// only reads it.
	Writes bool `json:"writes"`
	// OnEject says what a release before an eject does to it.
	OnEject string `json:"on_eject"`
	// Stoppable is true for work that POST StopPath stops: an index or sync
	// (/api/index/cancel), or the Library view refresh
	// (/api/library/refresh/stop). Stopping is true once it has been asked to.
	Stoppable bool   `json:"stoppable,omitempty"`
	StopPath  string `json:"stop_path,omitempty"`
	Stopping  bool   `json:"stopping,omitempty"`
	// Automatic is true for Index changes (an incremental run), which
	// indexes what changed in this Mac's sources without capturing them.
	Automatic bool `json:"automatic,omitempty"`
}

// libraryDrive identifies the volume holding the library.
type libraryDrive struct {
	Name       string `json:"name"`
	MountPoint string `json:"mount_point"`
	VolumeUUID string `json:"volume_uuid,omitempty"`
	// Location is external, disk-image, internal or unknown.
	Location string `json:"location"`
	Mounted  bool   `json:"mounted"`
	// Ejectable is true for a volume under /Volumes that is not the internal
	// disk: the app can release the library and eject it.
	Ejectable bool `json:"ejectable"`
	// Pinned is true when volume_id pins the library to this volume.
	Pinned bool `json:"pinned"`
}

// backgroundTasks counts the service's background writers that are not
// runs: the Git merge lookups after a sync or an index.
type backgroundTasks struct {
	mu        sync.Mutex
	git       int
	gitSince  string
	gitReason string
	gitWork   *activityWork
}

func (t *backgroundTasks) addGit(delta int, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.git == 0 && delta > 0 {
		t.gitSince, t.gitReason = now(), reason
		t.gitWork = nil
	}
	t.git += delta
	if t.git == 0 {
		t.gitWork = nil
	}
}

func (t *backgroundTasks) gitRunning() (bool, string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.git > 0, t.gitSince, t.gitReason
}

func (t *backgroundTasks) setGitWork(w activityWork) {
	t.mu.Lock()
	defer t.mu.Unlock()
	copy := cloneWork(w)
	t.gitWork = &copy
}
func (t *backgroundTasks) gitProgress() *activityWork {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gitWork == nil {
		return nil
	}
	copy := cloneWork(*t.gitWork)
	return &copy
}

// refreshGitInBackground looks up main-branch merges in local Git checkouts
// (thousands of git calls on a large catalog) without holding up the sync or
// index that asked for it, then rebuilds the Tools rollup from what it wrote.
// backfill runs the one-time scan instead, which does nothing once it has
// completed.
func (s *Server) refreshGitInBackground(backfill bool) {
	reason := "after the last index"
	if backfill {
		reason = "first scan of this catalog"
	}
	s.tasks.addGit(1, reason)
	started := s.spawn(func(ctx context.Context) {
		defer s.tasks.addGit(-1, "")
		_, since, _ := s.tasks.gitRunning()
		meter := &runWorkMeter{server: s, runID: "git:" + since, kind: "git", model: "analysis-v1:git", publish: s.tasks.setGitWork}
		defer func() { meter.release(ctx.Err() == nil) }()
		ctx = withWorkProgress(ctx, meter.report)
		meter.report(activityWork{Phase: "waiting for Git refresh"})
		refresh, name := s.Catalog.refreshMainIntegrations, "refresh"
		if backfill {
			refresh, name = s.Catalog.backfillMainIntegrations, "backfill"
		}
		if err := refresh(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Git integration %s: %v\n", name, err)
		}
		meter.finish(ctx.Err() == nil)
		if backfill {
			return
		}
		meter.model = "analysis-v1:tools"
		meter.report(activityWork{Phase: "refreshing tools"})
		// Rebuild the Tools rollup now rather than on the next page view.
		if err := s.Catalog.ensureToolRollup(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Tool rollup: %v\n", err)
		}
		meter.finish(ctx.Err() == nil)
		meter.model = "analysis-v1:findings"
		meter.report(activityWork{Phase: "refreshing findings"})
		// Then findings, from the fresh rollup: the full pass once a day,
		// and between them only the findings being measured.
		if err := s.Catalog.RefreshFindings(ctx, false); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Findings: %v\n", err)
		}
	})
	if !started {
		s.tasks.addGit(-1, "")
	}
}

// libraryPending counts workspaces whose Library row maintainLibrary has yet
// to recompute, up to limit.
func (c *Catalog) libraryPending(limit int) (int, error) {
	var count int
	err := c.reader().QueryRow("SELECT COUNT(*) FROM (SELECT 1 FROM workspace_library_dirty LIMIT ?)", limit).Scan(&count)
	return count, err
}

// libraryDir is the directory the service holds: the one with library.toml,
// or for a per-user configuration the catalog's.
func libraryDir(config Config) string {
	if config.Library && config.Path != "" {
		return filepath.Dir(config.Path)
	}
	return filepath.Dir(config.CatalogPath)
}

// libraryDriveOf names the library's volume from statfs, and from the drive
// checks' cached diskutil facts once they are known; it never shells out.
func libraryDriveOf(config Config) libraryDrive {
	dir := libraryDir(config)
	drive := libraryDrive{Location: "unknown", Pinned: config.VolumeID != ""}
	_, err := os.Stat(dir)
	drive.Mounted = err == nil
	mount, _, err := volumeMount(nearestExisting(dir))
	if err == nil {
		drive.MountPoint = mount
		drive.Name = filepath.Base(mount)
		if strings.HasPrefix(mount, "/Volumes/") {
			drive.Location = "external"
		} else {
			drive.Location = "internal"
		}
	}
	if config.Library {
		if state := drives.peek(config); state != nil && state.facts.MountPoint == mount && state.facts.Error == "" {
			drive.Name = defaultString(state.facts.Name, drive.Name)
			drive.VolumeUUID = state.facts.UUID
			drive.Location = defaultString(state.facts.Location, drive.Location)
		}
	}
	if drive.VolumeUUID == "" && strings.HasPrefix(config.VolumeID, "uuid:") {
		drive.VolumeUUID = strings.TrimPrefix(config.VolumeID, "uuid:")
	}
	if drive.Location == "internal" {
		drive.Name = "this Mac's internal disk"
	}
	drive.Ejectable = drive.Mounted && strings.HasPrefix(drive.MountPoint, "/Volumes/") && drive.Location != "internal"
	return drive
}

func fraction(done, total float64) *float64 {
	if total <= 0 {
		return nil
	}
	value := min(max(done/total, 0), 1)
	return &value
}

// libraryActivities lists what is running now.
func (s *Server) libraryActivities() []libraryActivity {
	activities := []libraryActivity{}
	s.runsMu.RLock()
	for _, run := range s.runs {
		if run.State != "running" {
			continue
		}
		activity := libraryActivity{operation: run.operation, ID: run.ID, Kind: "sync", Label: "Indexing sources", StartedAt: run.StartedAt, Writes: true, timingKind: activityTimingKind(run.Kind),
			OnEject: "Stops between workspaces; the next index resumes it.", Stoppable: true, StopPath: "/api/index/cancel", Stopping: run.StopRequested}
		if run.Kind == automaticRunKind {
			activity.Automatic = true
			activity.Label = "Indexing changes: checking for changes"
			if run.Phase == "indexing" {
				activity.Label = fmt.Sprintf("Indexing changes: %s", plural(run.Conversations, "conversation"))
			}
			if run.Phase == "verifying" {
				activity.Label = "Indexing changes: verifying a conversation"
			}
		}
		if run.Kind == manualSyncRunKind {
			activity.Label = "Manual sync: " + run.Phase
		}
		if run.Kind == "library-update" {
			activity.Label = "Capture & index"
		}
		if run.Kind == "verification" {
			activity.Label = "Verifying retained inputs"
		}
		if run.Kind == indexRunKind {
			activity.Kind, activity.Label = "index", "Indexing captures"
			activity.OnEject = "Stops between records; the next index resumes it."
			// An index counts the conversations it has to parse first, and
			// measures itself against them rather than the sources.
			if run.Phase == "counting" {
				activity.Progress = nil
			} else if run.Phase == "indexing" && run.Progress != nil {
				activity.Progress = run.Progress
			}
		}
		parts := []string{}
		if current := firstString(run.CurrentSource); current != "" {
			parts = append(parts, current)
		}
		switch {
		case run.Kind == indexRunKind && run.Phase == "counting":
			parts = append(parts, "counting the conversations to index")
		case run.Kind == indexRunKind && run.TotalConversations > 0:
			parts = append(parts, fmt.Sprintf("%d of %s", run.DoneConversations, plural(run.TotalConversations, "conversation")),
				fmt.Sprintf("%d of %d sources", run.CompletedSources, run.TotalSources))
		default:
			parts = append(parts, fmt.Sprintf("%d of %d sources", run.CompletedSources, run.TotalSources))
			if run.Conversations > 0 {
				parts = append(parts, plural(run.Conversations, "conversation")+" written")
			}
		}
		activity.Detail = strings.Join(parts, " · ")
		sourceSummary := ""
		if run.TotalSources > 0 {
			sourceSummary = fmt.Sprintf("%d of %d sources finished", run.CompletedSources, run.TotalSources)
		}
		if run.Work != nil {
			work := cloneWork(*run.Work) // snapshots must not share mutable counters
			activity.Work = &work
			activity.Detail = strings.Join(nonemptyParts(work.Source, sourceSummary), " · ")
		} else if run.Phase != "starting" && run.Phase != "counting" {
			activity.Detail = strings.Join(nonemptyParts(firstString(run.CurrentSource), run.Phase, sourceSummary), " · ")
		}
		if run.InputsChecked > 0 && run.CurrentSource != nil {
			activity.Detail += fmt.Sprintf(" · %d input groups checked", run.InputsChecked)
		}
		if run.Messages > 0 {
			activity.Detail += fmt.Sprintf(" · %d messages written", run.Messages)
		}
		activities = append(activities, activity)
	}
	s.runsMu.RUnlock()
	serviceCapture := false
	for _, run := range s.captures.list() {
		if run.State != "running" {
			continue
		}
		serviceCapture = true
		var work *activityWork
		parts := []string{}
		for _, result := range run.Results {
			if result.State == "running" {
				parts = append(parts, result.Name)
				w := captureWork(result)
				work = &w
			}
		}
		parts = append(parts, fmt.Sprintf("%d of %d sources", run.CompletedSources, run.TotalSources), humanBytes(run.BytesCopied)+" copied")
		activities = append(activities, libraryActivity{ID: run.ID, Kind: "capture", Label: "Capturing " + currentHost().Label, Detail: strings.Join(nonemptyParts(parts...), " · "),
			Work: work, StartedAt: run.StartedAt, Writes: true,
			OnEject: "Stops between files; the next capture resumes it, and what was captured stays valid."})
	}
	if !serviceCapture && s.captureActive() {
		activities = append(activities, libraryActivity{Kind: "capture-other", Label: "Capture by another process",
			Detail: "Another process on this Mac, such as pharos capture, is capturing into the library.", Writes: true,
			OnEject: "Pharos cannot stop it: wait for it to finish, or stop it, before ejecting; until then it keeps the drive busy."})
	}
	if active, runs := s.backups.status(); active && len(runs) > 0 {
		run := runs[0]
		detail := fmt.Sprintf("To %s · %s · %s copied", run.Destination, defaultString(run.Phase, "starting"), humanBytes(run.BytesCopied))
		activities = append(activities, libraryActivity{ID: run.ID, Kind: "backup", Label: "Backing up", Detail: detail, StartedAt: run.StartedAt,
			Work:    &activityWork{Phase: run.Phase, Unit: "byte", Done: float64(run.BytesCopied)},
			OnEject: "Stops; the next backup carries on from what was copied."})
	}
	if running, since, reason := s.tasks.gitRunning(); running {
		activities = append(activities, libraryActivity{Kind: "git", Label: "Looking up merges in Git", Detail: "Main-branch merges of indexed work, " + reason,
			ID: "git:" + since, Work: s.tasks.gitProgress(), StartedAt: since, Writes: true, OnEject: "Stops; it runs again after the next index."})
	}
	if upgrade := s.Catalog.upgrade(); upgrade != nil {
		upgrade.mu.Lock()
		running, step, done, total, since := upgrade.running, upgrade.step, upgrade.done, upgrade.total, upgrade.startedAt
		unit, label := "item", step
		for _, planned := range upgrade.planned {
			if planned.ID == step {
				unit, label = defaultString(planned.Unit, "item"), defaultString(planned.Label, step)
			}
		}
		upgrade.mu.Unlock()
		if running {
			detail := "Step: " + step
			if total > 0 {
				detail = fmt.Sprintf("Step: %s, %d of %d", step, done, total)
			}
			activities = append(activities, libraryActivity{Kind: "maintenance", Label: "Upgrading the library", Detail: detail,
				ID: "upgrade:" + since, Work: &activityWork{Phase: label, Unit: unit, Done: float64(done), Total: float64(total), Next: "Remaining library upgrade steps"},
				Progress: fraction(float64(done), float64(total)), StartedAt: since, Writes: true,
				OnEject: "Stops after the unit in progress; starting the upgrade again resumes it."})
		}
	}
	if tools := s.Catalog.toolLedgerProgress(); tools.running && !s.Catalog.upgradeRunning() {
		activities = append(activities, libraryActivity{Kind: "maintenance", Label: "Building the Tools ledger",
			ID: "tools", Work: &activityWork{Phase: "Building the Tools ledger", Unit: "conversation", Done: float64(tools.done), Total: float64(tools.total)},
			Detail: fmt.Sprintf("%d of %d conversations", tools.done, tools.total), Progress: fraction(float64(tools.done), float64(tools.total)),
			Writes: true, OnEject: "Stops between conversations; building the ledger again resumes it."})
	}
	if running, phase := s.Catalog.findingsRunning(); running {
		activities = append(activities, libraryActivity{Kind: "maintenance", Label: "Updating findings",
			Detail: "Looking for recurring patterns and measuring the ones you're watching (" + phase + ")", Writes: true,
			OnEject: "Stops; it runs again after the next index."})
	}
	if s.Catalog.authorshipRunning() {
		activities = append(activities, libraryActivity{Kind: "maintenance", Label: "Classifying your writing",
			Detail: "Rebuilding human authorship for Usage", Writes: true,
			OnEject: "Stops; it runs again after the next index."})
	}
	if progress, err := s.Catalog.substringIndexProgress(context.Background()); err == nil && !progress.Ready && progress.Total > 0 {
		activities = append(activities, libraryActivity{Kind: "maintenance", Label: "Building substring search",
			ID: "substring", Work: &activityWork{Phase: "Building substring search", Unit: "conversation", Done: float64(progress.Done), Total: float64(progress.Total)},
			Detail:   fmt.Sprintf("%d of %d conversations", progress.Done, progress.Total),
			Progress: fraction(float64(progress.Done), float64(progress.Total)), Writes: true,
			OnEject: "Stops between batches; it carries on when Pharos next opens the library."})
	}
	// Only a refresh that has run for a while: an index's own writes are
	// refreshed within seconds of each commit.
	if s.Catalog.libraryCatchingUp() {
		if pending, err := s.Catalog.libraryPending(100_000); err == nil && pending > 0 {
			activities = append(activities, libraryActivity{Kind: "maintenance", Label: "Indexing the Library view",
				Detail: plural(pending, "workspace") + " to index", Writes: true,
				OnEject: "Stops; it carries on when Pharos next opens the library.", Stoppable: true, StopPath: "/api/library/refresh/stop"})
		}
	}
	return activities
}

// unplugAdvice says how to disconnect the library's drive given what is
// running: eject, always, and whether unplugging without an eject would
// probably lose nothing (nothing running) or lose work in progress.
func unplugAdvice(drive libraryDrive, activities []libraryActivity) map[string]any {
	writing := []string{}
	for _, activity := range activities {
		if activity.Writes {
			writing = append(writing, activity.Label)
		}
	}
	unplug := map[string]any{"action": "eject", "without_eject": "unsafe"}
	switch {
	case !drive.Ejectable:
		unplug["action"], unplug["without_eject"] = "none", "not-applicable"
		unplug["summary"] = fmt.Sprintf("The library is on %s, which is not ejected.", drive.Name)
	case len(activities) == 0:
		unplug["without_eject"] = "probably-fine"
		unplug["summary"] = fmt.Sprintf("Nothing is running. Pharos still has the library open, so eject %s before unplugging it: Pharos lets go of the library first. Unplugging without ejecting now would probably lose nothing.", drive.Name)
	case len(writing) == 0:
		unplug["summary"] = fmt.Sprintf("%s is reading the library. Eject %s before unplugging it: Pharos stops the work at a safe point and closes the library first, or refuses the eject and says why.", activities[0].Label, drive.Name)
	default:
		unplug["summary"] = fmt.Sprintf("%s: writing to %s now; unplugging it would lose that work. Eject it instead: Pharos stops its own work at a safe point (the next run resumes it) and closes the library first, or refuses and says why.", strings.Join(writing, ", "), drive.Name)
		if slices.ContainsFunc(activities, func(activity libraryActivity) bool { return activity.Kind == "capture-other" }) {
			unplug["summary"] = unplug["summary"].(string) + " A capture by another process keeps the drive busy until it finishes; Pharos cannot stop it."
		}
	}
	return unplug
}

// libraryStatus answers GET /api/library/status.
func (s *Server) libraryStatus() map[string]any {
	config := s.Config()
	drive := libraryDriveOf(config)
	activities := s.libraryActivities()
	s.estimateActivities(activities, time.Now())
	writing := slices.ContainsFunc(activities, func(activity libraryActivity) bool { return activity.Writes })
	// A stopped Library view refresh is not running, so it is not an
	// activity; it is listed with what it left so it can be resumed.
	refresh := map[string]any{"stopped": false}
	if s.Catalog.libraryRefreshStopped() {
		pending, _ := s.Catalog.libraryPending(100_000)
		refresh = map[string]any{"stopped": true, "pending": pending}
	}
	return map[string]any{
		"portable": config.Library, "library_dir": libraryDir(config), "config_path": config.Path,
		"catalog_path": config.CatalogPath, "capture_root": config.CaptureRoot, "host": currentHost(),
		"drive": drive, "catalog_open": true, "activities": activities, "idle": len(activities) == 0, "writing": writing,
		// GET /api/capture has the same field: nothing is running. The
		// catalog is still open, so eject regardless.
		"safe_to_unplug":  len(activities) == 0,
		"unplug":          unplugAdvice(drive, activities),
		"library_refresh": refresh,
	}
}
