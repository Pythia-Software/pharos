package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Ordinary maintenance and analysis under population execution
// (docs/work-execution-plan.md, PR 3). In scheduled mode the projection,
// search-index and analysis-chain loops stop executing on their own: they
// become coordinators that submit demand, and every batch runs as an admitted
// job of the common runner. Each adapter wraps an existing bounded domain
// step; dirty state, locks, generations and publication stay in the domain.
// The classic comparison path is unchanged.
//
// Every adapter here is Coarse: none has a proven per-unit publication
// boundary, so each is credited only when its own cheap pending check says
// the library is current. Exact authorship, tool rollups, findings, Git and
// identity reconciliation stay whole-library passes.

// maintenanceSlice bounds one admitted batch of a looping step, so a long
// catch-up yields its grant (and its execution's batch operation) regularly.
var maintenanceSlice = 2 * time.Second

// deferredStage is one stage of the analysis chain and its classic work.
type deferredStage struct {
	name string
	work func(context.Context) error
}

// maintenanceTask is a coarse adapter around an existing bounded step.
type maintenanceTask struct {
	routing *maintenanceRouting
	catalog *Catalog
	kind    string
	phase   string
	cpus    int
	// hold is a concrete reason not to start now (Stop, a publishing index).
	hold func(context.Context) workBlocker
	// probe is a cheap indexed pending check. A task without one is either
	// a one-time backfill (once: settled for this process after its step
	// reports nothing more) or runs only when armed.
	probe func(context.Context) (bool, error)
	once  bool
	// step runs one bounded domain step: whether more remains and how much
	// it did. A step that loops gets maintenanceSlice per batch.
	step func(context.Context) (more bool, work int64, err error)
	loop bool

	mu       sync.Mutex
	armed    int
	settled  bool
	findings findingsMode // strongest mode armed since the last pass
	progress maintenanceProgress
}

// findingsStrength orders findings modes: measuring only, the daily pass
// when due, the full pass anyway.
func findingsStrength(mode findingsMode) int {
	switch mode {
	case findingsForce:
		return 2
	case findingsAuto:
		return 1
	}
	return 0
}

// arm asks for at least one pass even when the probe reports nothing
// pending (an hourly findings measurement, a daily refinement, Run now).
func (t *maintenanceTask) arm(mode findingsMode) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.armed == 0 || findingsStrength(mode) > findingsStrength(t.findings) {
		t.findings = mode
	}
	t.armed++
}

func (t *maintenanceTask) Next(ctx context.Context, _ *workPopulation) (workSelection, error) {
	if t.hold != nil {
		if blocker := t.hold(ctx); blocker.Reason != "" {
			return workSelection{Blocker: blocker}, nil
		}
	}
	t.mu.Lock()
	armed, settled := t.armed > 0, t.settled
	t.mu.Unlock()
	if !armed {
		switch {
		case t.probe != nil:
			pending, err := t.probe(ctx)
			if err != nil || !pending {
				return workSelection{}, err
			}
		case !t.once || settled:
			return workSelection{}, nil
		}
	}
	return workSelection{Phase: t.phase, Coarse: true, CPUs: max(1, t.cpus)}, nil
}

func (t *maintenanceTask) RunBatch(ctx context.Context, _ workSelection, _ workAllocation) (workBatchResult, error) {
	t.mu.Lock()
	// A pass the probe started without an arm only measures, as a
	// background update's findings item does.
	mode := findingsMeasure
	if t.armed > 0 {
		mode = t.findings
	}
	t.armed = 0
	t.mu.Unlock()
	ctx = context.WithValue(withFindingsMode(ctx, mode), maintenanceProgressKey{}, &t.progress)
	op := opFrom(ctx)
	started := time.Now()
	var work int64
	for {
		if t.catalog != nil {
			// Interactive queries go first, as in the classic daemons. The
			// wait holds this batch's CPU grant but no catalog lock.
			if err := t.catalog.queries.wait(ctx); err != nil {
				return workBatchResult{Work: work, Remaining: true}, err
			}
		}
		var more bool
		var did int64
		var err error
		op.RunIO(func() { more, did, err = t.step(ctx) })
		work += did
		if err != nil {
			if errors.Is(err, errWorkDomainBusy) || errors.Is(err, errLabelsRunning) {
				return workBatchResult{Work: work, Remaining: true}, workBlocked(workBlocker{Reason: workBlockedWriter, Detail: err.Error()})
			}
			var blocked *workError
			if errors.As(err, &blocked) {
				return workBatchResult{Work: work, Remaining: true}, err
			}
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", t.kind, err)
			}
			return workBatchResult{Work: work, Remaining: true}, err
		}
		if !more {
			t.mu.Lock()
			if t.once {
				t.settled = true
			}
			t.progress = maintenanceProgress{}
			t.mu.Unlock()
			// A completed pass is the coarse unit of work, even when the
			// domain found nothing to change.
			return workBatchResult{Work: max(work, 1)}, nil
		}
		if !t.loop || time.Since(started) >= maintenanceSlice || ctx.Err() != nil {
			return workBatchResult{Work: max(work, 1), Remaining: true}, ctx.Err()
		}
	}
}

// findingsMode travels to the findings step through its batch context: the
// strongest mode armed since the last pass.
type findingsModeKey struct{}

func withFindingsMode(ctx context.Context, mode findingsMode) context.Context {
	return context.WithValue(ctx, findingsModeKey{}, mode)
}
func findingsModeFrom(ctx context.Context) findingsMode {
	mode, _ := ctx.Value(findingsModeKey{}).(findingsMode)
	return mode
}

// The deferred-stage ledger, shared by the classic chain and the coarse
// stage adapters: a pass acknowledges only the generation it read before
// doing its work.
func (c *Catalog) deferredPendingGeneration(ctx context.Context, name string) (int64, error) {
	var value int64
	err := c.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT generation FROM sync_deferred WHERE name=? AND generation>completed_generation),0)`, name).Scan(&value)
	return value, err
}

func (c *Catalog) completeDeferred(ctx context.Context, name string, generation int64) error {
	if generation <= 0 {
		return nil
	}
	return c.writeTransaction(ctx, "complete deferred "+name, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sync_deferred SET completed_generation=MAX(completed_generation,?),completed_at=? WHERE name=?", generation, now(), name)
		return err
	})
}

// stageStep is a whole-library chain stage as a coarse step: it reads the
// pending generation first and acknowledges exactly that generation after
// the stage's own algorithm finished, as the classic chain does.
func stageStep(c *Catalog, name string, work func(context.Context) error) func(context.Context) (bool, int64, error) {
	return func(ctx context.Context) (bool, int64, error) {
		generation, err := c.deferredPendingGeneration(ctx, name)
		if err != nil {
			return false, 0, err
		}
		err = work(ctx)
		if errors.Is(err, errStageContinues) {
			return false, 1, nil
		}
		if err != nil {
			return false, 0, err
		}
		return false, 1, c.completeDeferred(ctx, name, generation)
	}
}

func catalogExists(ctx context.Context, c *Catalog, query string, args ...any) (bool, error) {
	var found bool
	err := c.reader().QueryRowContext(ctx, "SELECT EXISTS("+query+")", args...).Scan(&found)
	return found, err
}

// Task kinds this file declares. The population kinds (authorship, labels,
// label query facts, parallelism, agent events) are declared beside them in
// work_task_adapters.go and work_domain_agent_events.go.
const (
	kindOrigins        = "maintenance.origins"
	kindLibraryView    = "maintenance.library-view"
	kindPreviews       = "maintenance.previews"
	kindSubstring      = "maintenance.search.substring"
	kindToolOutput     = "maintenance.search.tool-output"
	kindTextSearch     = "maintenance.search.text"
	kindToolSearch     = "maintenance.search.tools"
	kindQueryText      = "maintenance.search.query"
	kindIdentities     = "analysis.identities"
	kindGit            = "analysis.git"
	kindTools          = "analysis.tools"
	kindFindings       = "analysis.findings"
	kindRefine         = "analysis.authorship.refine"
	kindParallelism    = "maintenance.parallelism"
	kindLabelFacts     = "maintenance.labels.query"
	kindAuthorship     = "analysis.authorship"
	kindLabels         = "analysis.labels"
	kindAgentEvents    = "analysis.agent_events"
	maintenanceRecent  = 48 * time.Hour
	maintenanceTick    = 2 * time.Second
	maintenanceWarmGap = 2 * time.Second
)

// searchKinds are what the Search item (and the continuous Frequently
// schedule) keeps current: the stored projection, then the index backfills.
var searchKinds = []string{kindOrigins, kindLibraryView, kindPreviews, kindParallelism, kindSubstring, kindToolOutput, kindTextSearch, kindToolSearch, kindQueryText}

// projectionKinds are held by the Library refresh's Stop.
var projectionKinds = []string{kindOrigins, kindLibraryView, kindPreviews, kindParallelism}

// stageKinds maps the analysis chain's stage names to their task kinds.
var stageKinds = map[string]string{"identities": kindIdentities, "git": kindGit, "tools": kindTools, "authorship": kindAuthorship,
	"findings": kindFindings, "labels": kindLabels, "agent_events": kindAgentEvents}

// populationStages have unit adapters: recent work is a finite request, and
// the historical remainder is ordinary backfill demand.
var populationStages = map[string]bool{"authorship": true, "labels": true, "agent_events": true}

// maintenanceRouting is the coordinator state that outlives one pass: the
// backfill request each population stage submitted and the generation it
// may acknowledge once that request succeeds. It is disposable; a restart
// rediscovers pending generations from sync_deferred.
type maintenanceRouting struct {
	mu        sync.Mutex
	backfills map[string]*stageBackfill
	tasks     map[string]*maintenanceTask
	warmedAt  time.Time
}

type stageBackfill struct {
	id         uint64
	record     *workOperation
	generation int64
}

// maintenanceWorkDefinitions declares the coarse maintenance and analysis
// tasks. The caller appends them to the population definitions.
func (s *Server) maintenanceWorkDefinitions(_ *workAuthority) ([]workDefinition, error) {
	c := s.Catalog
	routing := &maintenanceRouting{backfills: map[string]*stageBackfill{}, tasks: map[string]*maintenanceTask{}}
	projectionHold := func(context.Context) workBlocker {
		if c.libraryRefreshStopped() {
			return workBlocker{Reason: workBlockedHeld, Detail: "the Search view refresh is stopped"}
		}
		if c.projectionBatches.Load() > 0 {
			return workBlocker{Reason: workBlockedWriter, Detail: "an index is publishing the projection"}
		}
		return workBlocker{}
	}
	// libraryStep runs a projection step inside a pass Stop can cancel.
	libraryStep := func(step func(context.Context) (int, error)) func(context.Context) (bool, int64, error) {
		return func(ctx context.Context) (bool, int64, error) {
			pass, done, running := c.libraryPass(ctx)
			if !running {
				return false, 0, workBlocked(workBlocker{Reason: workBlockedHeld, Detail: "the Search view refresh is stopped"})
			}
			defer done()
			count, err := step(pass)
			if err != nil && pass.Err() != nil && ctx.Err() == nil {
				return false, int64(count), workBlocked(workBlocker{Reason: workBlockedHeld, Detail: "the Search view refresh is stopped"})
			}
			return count > 0, int64(count), err
		}
	}
	var originsAt atomic.Int64 // unix nanoseconds of the last publication attempt
	migration := newToolOutputMigration()
	queryIndexes := []queryTextIndex{humanQueryText, toolQueryText, toolQueryFacts}
	queryIndex := 0
	tasks := []*maintenanceTask{
		{kind: kindOrigins, phase: "publish",
			hold: func(ctx context.Context) workBlocker {
				if blocker := projectionHold(ctx); blocker.Reason == workBlockedHeld {
					return blocker
				}
				// While this process ingests, publications are spaced: each reads
				// every conversation and the next commit dirties it again.
				if c.ingestRuns.Load() > 0 && time.Since(time.Unix(0, originsAt.Load())) < originsRunInterval {
					return workBlocker{Reason: workBlockedWriter, Detail: "an index is running; origins publish at most once a minute meanwhile"}
				}
				return workBlocker{}
			},
			probe: func(ctx context.Context) (bool, error) {
				return catalogExists(ctx, c, "SELECT 1 FROM origin_state WHERE id=1 AND revision<>built")
			},
			step: func(ctx context.Context) (bool, int64, error) {
				originsAt.Store(time.Now().UnixNano())
				published, err := c.publishOrigins(ctx)
				if published {
					return false, 1, err
				}
				return false, 0, err
			}},
		{kind: kindLibraryView, phase: "refresh", hold: projectionHold, loop: true,
			probe: func(ctx context.Context) (bool, error) {
				return catalogExists(ctx, c, "SELECT 1 FROM workspace_library_dirty UNION ALL SELECT 1 FROM mcp_metric_dirty")
			},
			step: libraryStep(func(ctx context.Context) (int, error) {
				count, err := c.RefreshLibrary(ctx, 10)
				c.noteLibraryRefresh(count, err)
				return count, err
			})},
		{kind: kindPreviews, phase: "fill", hold: projectionHold, loop: true,
			probe: func(ctx context.Context) (bool, error) {
				return catalogExists(ctx, c, `SELECT 1 FROM workspace_library l WHERE l.turn_count+l.tool_use_count>200
					AND l.workspace_id NOT IN (SELECT workspace_id FROM workspace_previews)
					AND l.workspace_id NOT IN (SELECT workspace_id FROM workspace_library_dirty)`)
			},
			step: libraryStep(func(ctx context.Context) (int, error) { return c.refreshPreviews(ctx, 25) })},
		{kind: kindSubstring, phase: "build", once: true, loop: true, step: cursorStep(c.indexSubstringBatch)},
		{kind: kindToolOutput, phase: "remove", once: true, loop: true, step: cursorStep(func(ctx context.Context) (bool, error) { return migration.step(ctx, c) })},
		{kind: kindTextSearch, phase: "build", once: true, loop: true, step: cursorStep(c.indexTextSearchBatch)},
		{kind: kindToolSearch, phase: "build", once: true, loop: true, step: cursorStep(c.indexToolSearchBatch)},
		{kind: kindQueryText, phase: "build", once: true, loop: true, step: func(ctx context.Context) (bool, int64, error) {
			if queryIndex >= len(queryIndexes) {
				return false, 0, nil
			}
			more, err := c.indexQueryTextBatch(ctx, queryIndexes[queryIndex])
			if err == nil && !more {
				queryIndex++
				more = queryIndex < len(queryIndexes)
			}
			return more, 1, err
		}},
		{kind: kindIdentities, phase: "reconcile", probe: deferredProbe(c, "identities"),
			step: stageStep(c, "identities", func(ctx context.Context) error { _, err := c.ReconcileIdentitiesContext(ctx); return err })},
		{kind: kindGit, phase: "merges", probe: deferredProbe(c, "git"), step: stageStep(c, "git", c.refreshMainIntegrations)},
		{kind: kindTools, phase: "rollup", probe: deferredProbe(c, "tools"), step: stageStep(c, "tools", c.ensureToolRollup)},
		{kind: kindFindings, phase: "measure", probe: deferredProbe(c, "findings"),
			step: stageStep(c, "findings", func(ctx context.Context) error {
				// A page or another action may be discovering findings: wait
				// without the grant rather than polling inside it.
				if running, _ := c.findingsRunning(); running {
					return workBlocked(workBlocker{Reason: workBlockedWriter, Detail: "findings are already being updated"})
				}
				switch findingsModeFrom(ctx) {
				case findingsMeasure:
					return c.MeasureFindings(ctx)
				case findingsForce:
					return c.RefreshFindings(ctx, true)
				default:
					return c.RefreshFindings(ctx, false)
				}
			})},
		// The daily refinement has no dirty ledger of its own; it runs only
		// when its schedule (or Run now) arms it.
		{kind: kindRefine, phase: "refine", step: func(ctx context.Context) (bool, int64, error) {
			return false, 1, c.refineAuthorship(ctx, false)
		}},
	}
	defs := make([]workDefinition, 0, len(tasks))
	for _, task := range tasks {
		task.routing, task.catalog = routing, c
		routing.tasks[task.kind] = task
		defs = append(defs, workDefinition{Kind: task.kind, Coarse: true, Task: task})
	}
	return defs, nil
}

func cursorStep(step func(context.Context) (bool, error)) func(context.Context) (bool, int64, error) {
	return func(ctx context.Context) (bool, int64, error) {
		more, err := step(ctx)
		if more {
			return true, 1, err
		}
		return false, 0, err
	}
}

func deferredProbe(c *Catalog, name string) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		return catalogExists(ctx, c, "SELECT 1 FROM sync_deferred WHERE name=? AND generation>completed_generation", name)
	}
}

// routing returns the maintenance coordinator state, or nil when this
// process runs the classic path (or the table lacks the maintenance tasks).
func (w *workService) routing() *maintenanceRouting {
	if w == nil || w.runner == nil {
		return nil
	}
	if ks := w.runner.byKind[kindOrigins]; ks != nil {
		if task, ok := ks.def.Task.(*maintenanceTask); ok {
			return task.routing
		}
	}
	return nil
}

// declares reports whether kind is in the task table. The table is fixed
// when the runner is built.
func (w *workService) declares(kind string) bool {
	return w != nil && w.runner != nil && w.runner.byKind[kind] != nil
}

func (w *workService) arm(kind string, mode findingsMode) {
	if routing := w.routing(); routing != nil {
		if task := routing.tasks[kind]; task != nil {
			task.arm(mode)
		}
	}
}

// await waits for a request this caller owns (submitted without a Key) and
// cancels it if the caller gives up, so no abandoned demand keeps running.
func (w *workService) await(ctx context.Context, id uint64, record *workOperation) (workRequestFacts, error) {
	select {
	case <-ctx.Done():
		w.runner.Cancel(id)
		return workRequestFacts{}, ctx.Err()
	case <-record.done:
		w.mu.Lock()
		facts := record.facts
		w.mu.Unlock()
		return facts, nil
	}
}

func workRequestError(facts workRequestFacts) error {
	if facts.State == opSucceeded {
		return nil
	}
	return fmt.Errorf("work request %d %s: %s", facts.ID, facts.State, defaultString(facts.Reason, facts.Detail))
}

// runLibrary submits whole-library demand for each kind, one request per
// kind so a blocked or slow kind does not hold the others' completion, and
// waits for all of them. Explicit marks Run now; history never becomes
// foreground through it, since every request here is Live.
func (w *workService) runLibrary(ctx context.Context, kinds []string, explicit bool, trigger string) error {
	type pending struct {
		id     uint64
		record *workOperation
	}
	submitted := []pending{}
	var failure error
	for _, kind := range kinds {
		if !w.declares(kind) {
			continue
		}
		id, record, err := w.submit(ctx, workRequestSpec{Kinds: []string{kind}, Priority: workPriorityLive, Explicit: explicit, Trigger: trigger})
		if err != nil {
			failure = errors.Join(failure, fmt.Errorf("%s: %w", kind, err))
			continue
		}
		submitted = append(submitted, pending{id, record})
	}
	for index, request := range submitted {
		facts, err := w.await(ctx, request.id, request.record)
		if err != nil {
			for _, rest := range submitted[index+1:] {
				w.runner.Cancel(rest.id)
			}
			return err
		}
		failure = errors.Join(failure, workRequestError(facts))
	}
	return failure
}

// submitContinuing keeps one live library request per kind and class: the
// same Key coalesces with a request still running. It does not wait.
func (w *workService) submitContinuing(ctx context.Context, kind string, priority workPriority) (uint64, *workOperation, error) {
	return w.submit(ctx, workRequestSpec{Key: fmt.Sprintf("maintenance:%s:%s", kind, priority), Kinds: []string{kind}, Priority: priority, Trigger: triggerChained})
}

// backgroundSearchWork is the Search item under population execution. A
// stopped Library refresh holds the projection kinds; the index backfills
// still run, as in the classic pass.
func (w *workService) backgroundSearchWork(ctx context.Context, c *Catalog) error {
	kinds := searchKinds
	if c.libraryRefreshStopped() {
		kinds = []string{}
		for _, kind := range searchKinds {
			if !slices.Contains(projectionKinds, kind) {
				kinds = append(kinds, kind)
			}
		}
	}
	return w.runLibrary(ctx, kinds, backgroundExplicit(ctx), triggerChained)
}

// maintainWork is the scheduled-mode maintenance coordinator. It replaces
// the projection, search-index and label-query-fact daemons: on each tick it
// asks each task's cheap pending check and submits continuing demand for what
// is pending. It executes nothing itself except reading the page caches warm,
// which it does inside an admitted call once nothing is pending.
func (s *Server) maintainWork(ctx context.Context) {
	w, c := s.work, s.Catalog
	routing := w.routing()
	gate := searchScheduleGate{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(maintenanceTick):
		}
		if settings, err := c.backgroundSettings(ctx, time.Now()); err == nil {
			w.runner.SetPaused(settings.Mode != "on")
		}
		if gate.continuousNow(ctx, c) {
			s.submitPendingMaintenance(ctx, routing)
		}
		s.submitPendingFacts(ctx)
	}
}

func (s *Server) submitPendingMaintenance(ctx context.Context, routing *maintenanceRouting) {
	w, c := s.work, s.Catalog
	stopped := c.libraryRefreshStopped()
	submitted := false
	for _, kind := range searchKinds {
		if !w.declares(kind) || stopped && slices.Contains(projectionKinds, kind) {
			continue
		}
		pending := false
		if kind == kindParallelism {
			pending, _ = catalogExists(ctx, c, "SELECT 1 FROM parallelism_dirty")
		} else if routing != nil && routing.tasks[kind] != nil {
			selection, err := routing.tasks[kind].Next(ctx, nil)
			pending = err == nil && (selection.ready() || selection.Blocker.Reason == workBlockedWriter)
		}
		if !pending {
			continue
		}
		if _, _, err := w.submitContinuing(ctx, kind, workPriorityLive); err != nil && ctx.Err() == nil && !errors.Is(err, errWorkQueueFull) {
			fmt.Fprintf(os.Stderr, "Maintenance %s: %v\n", kind, err)
		}
		submitted = true
	}
	if submitted || stopped || routing == nil {
		return
	}
	routing.mu.Lock()
	due := time.Since(routing.warmedAt) >= maintenanceWarmGap
	if due {
		routing.warmedAt = time.Now()
	}
	routing.mu.Unlock()
	if !due {
		return
	}
	// Warming only reads; its operation is never published.
	_ = w.call(ctx, 1, func(ctx context.Context, _ workAllocation) error {
		warm := c.beginPass(ctx, "maintenance.cache-warm", triggerChained)
		warm.runIO(ctx, func(ctx context.Context) { c.warmCaches(ctx) })
		warm.end(nil)
		return nil
	})
}

// submitPendingFacts keeps label query facts current, as their classic daemon
// did on every schedule. Their library request also brings their
// prerequisites' pending history, so it is backfill, and with labels on it
// waits for power that allows a labeling backlog.
func (s *Server) submitPendingFacts(ctx context.Context) {
	w, c := s.work, s.Catalog
	if !w.declares(kindLabelFacts) {
		return
	}
	pending, err := catalogExists(ctx, c, "SELECT 1 FROM message_label_facts_dirty")
	if err == nil && !pending {
		return
	}
	if settings := s.Config().Labels; settings.Enabled {
		if _, bulk := labelsForPower(settings, s.cachedPower(ctx)); !bulk {
			return
		}
	}
	if _, _, err := w.submitContinuing(ctx, kindLabelFacts, workPriorityBackfill); err != nil && ctx.Err() == nil && !errors.Is(err, errWorkQueueFull) {
		fmt.Fprintf(os.Stderr, "Maintenance %s: %v\n", kindLabelFacts, err)
	}
}

// continuousNow is the search schedule gate without a wait: whether the
// Search item is on the continuous Frequently schedule.
func (gate *searchScheduleGate) continuousNow(ctx context.Context, c *Catalog) bool {
	if time.Since(gate.checked) >= time.Second {
		settings, err := c.backgroundSettings(ctx, time.Now())
		if err == nil {
			item, _ := backgroundItemByID("search")
			gate.checked, gate.continuous = time.Now(), settings.cadence(item) == cadenceFrequent
		}
	}
	return gate.continuous
}

// routeDeferredStages is the analysis chain under population execution, in
// chain order. Whole-library stages are armed coarse requests the chain
// waits for. Population stages submit their recent activity as a finite
// request the chain waits for, then their historical remainder as backfill
// it does not wait for; their sync_deferred generation is acknowledged only
// after a library request (whole-library demand) succeeded, never from a
// selected population's progress. A stage whose kind is not in the task
// table keeps its classic algorithm (classic).
func (s *Server) routeDeferredStages(ctx context.Context, findings findingsMode, only map[string]bool, stages []deferredStage, step func(string, func(context.Context) error) error) error {
	w := s.work
	explicit := findings == findingsForce || backgroundExplicit(ctx) || only == nil
	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if only != nil && !only[stage.name] {
			continue
		}
		kind := stageKinds[stage.name]
		var run func(context.Context) error
		switch {
		case !w.declares(kind):
			run = stageStepError(s.Catalog, stage.name, stage.work)
		case populationStages[stage.name]:
			name := stage.name
			run = func(ctx context.Context) error { return s.routePopulationStage(ctx, name, kind, explicit) }
		default:
			mode := findings
			run = func(ctx context.Context) error {
				w.arm(kind, mode)
				return w.runLibrary(ctx, []string{kind}, explicit, triggerChained)
			}
		}
		if err := step(stage.name, run); err != nil {
			return err
		}
	}
	return nil
}

// stageStepError runs a classic stage with the same ledger rules.
func stageStepError(c *Catalog, name string, work func(context.Context) error) func(context.Context) error {
	run := stageStep(c, name, work)
	return func(ctx context.Context) error { _, _, err := run(ctx); return err }
}

func (s *Server) routePopulationStage(ctx context.Context, name, kind string, explicit bool) error {
	w, c := s.work, s.Catalog
	// Exact refinement owns a relational library-wide snapshot and its lease.
	// As in the classic chain, keep these stages pending rather than holding
	// the indexing coordinator until that indivisible pass finishes.
	c.authorship.mu.Lock()
	refining := c.authorship.refining
	c.authorship.mu.Unlock()
	if refining {
		return nil
	}
	routing := w.routing()
	// Model stages first make sure their assets are here, as the classic
	// stages do; a download in progress leaves the stage pending.
	switch err := s.stageAssetsReady(ctx, name); {
	case errors.Is(err, errStageContinues):
		return nil
	case err != nil:
		return err
	}
	// A backfill that finished since the last pass acknowledges what it read.
	if err := s.settleStageBackfill(ctx, routing, name); err != nil {
		return err
	}
	generation, err := c.deferredPendingGeneration(ctx, name)
	if err != nil {
		return err
	}
	until := c.clock()
	from := until.Add(-maintenanceRecent)
	priority := workPriorityLive
	if explicit {
		priority = workPriorityForeground
	}
	id, record, err := w.submit(ctx, workRequestSpec{Kinds: []string{kind}, Priority: priority, Explicit: explicit, Trigger: triggerChained,
		Build: func(ctx context.Context, b *workInventoryBuilder) error {
			return c.freezeWorkPopulation(ctx, b, from, until, nil)
		}})
	if err != nil {
		return err
	}
	facts, err := w.await(ctx, id, record)
	if err != nil {
		return err
	}
	if err := workRequestError(facts); err != nil && facts.Reason != workUnitsFailed {
		return err
	}
	if name == "agent_events" {
		// Fixes group these events; a rebuild is a no-op when nothing changed.
		if err := w.call(ctx, 1, func(ctx context.Context, _ workAllocation) error { return c.refreshFixesSoon(ctx) }); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Fixes: %v\n", err)
		}
	}
	// Failed units stay pending in their domain, and so does the stage.
	if generation == 0 {
		return nil
	}
	if !s.stageBackfillAllowed(ctx, name) {
		if routing != nil {
			routing.mu.Lock()
			previous := routing.backfills[name]
			delete(routing.backfills, name)
			routing.mu.Unlock()
			if previous != nil {
				w.runner.Cancel(previous.id)
			}
		}
		return nil
	}
	backfill, backfillRecord, err := w.submit(ctx, workRequestSpec{Key: fmt.Sprintf("stage-backfill:%s:%t", kind, explicit), Scope: "backfill", Kinds: []string{kind}, Priority: workPriorityBackfill, Explicit: explicit, Trigger: triggerChained})
	if err != nil {
		if errors.Is(err, errWorkQueueFull) {
			return nil
		}
		return err
	}
	if routing != nil {
		routing.mu.Lock()
		previous := routing.backfills[name]
		// A coalesced request keeps the generation it was submitted for;
		// newer inputs are acknowledged by a later backfill.
		if previous == nil || previous.id != backfill {
			routing.backfills[name] = &stageBackfill{id: backfill, record: backfillRecord, generation: generation}
		}
		routing.mu.Unlock()
	}
	return s.settleStageBackfill(ctx, routing, name)
}

// stageAssetsReady is the model stages' asset check from chainLabels and
// chainAgentEvents: errStageContinues while assets are missing or loading.
func (s *Server) stageAssetsReady(ctx context.Context, name string) error {
	config := s.Config()
	unpinned := func(asset labelsAsset) bool { return !asset.pinned() }
	var missing []labelsAsset
	switch name {
	case "labels":
		missing = config.Labels.missingAssets()
		if len(missing) > 0 && (config.Labels.Worker == "" || slices.ContainsFunc(missing, unpinned)) {
			return errStageContinues
		}
	case "agent_events":
		if config.Labels.Worker == "" {
			return errStageContinues
		}
		missing = config.AgentEvents.missingAssets(config.Labels)
		if slices.ContainsFunc(missing, unpinned) {
			return errStageContinues
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := fetchLabelsAssets(ctx, missing, nil, nil); errors.Is(err, errLabelsFetching) {
		return errStageContinues
	} else if err != nil {
		return err
	}
	return nil
}

// stageBackfillAllowed keeps a model backlog to the power its classic stage
// allowed: plugged in, or the battery policy's bulk setting.
func (s *Server) stageBackfillAllowed(ctx context.Context, name string) bool {
	config := s.Config()
	switch name {
	case "labels":
		_, bulk := labelsForPower(config.Labels, s.cachedPower(ctx))
		return bulk
	case "agent_events":
		_, bulk := labelsForPower(config.AgentEvents.labeler(config.Labels), s.cachedPower(ctx))
		return bulk
	}
	return true
}

func (s *Server) settleStageBackfill(ctx context.Context, routing *maintenanceRouting, name string) error {
	if routing == nil {
		return nil
	}
	routing.mu.Lock()
	backfill := routing.backfills[name]
	routing.mu.Unlock()
	if backfill == nil {
		return nil
	}
	select {
	case <-backfill.record.done:
	default:
		return nil
	}
	routing.mu.Lock()
	if routing.backfills[name] == backfill {
		delete(routing.backfills, name)
	}
	routing.mu.Unlock()
	s.work.mu.Lock()
	facts := backfill.record.facts
	s.work.mu.Unlock()
	if facts.State != opSucceeded {
		return nil
	}
	if name == "authorship" {
		// The library's estimate generation is authorship's own global
		// readiness; a drained queue alone does not establish it.
		want, built, err := s.Catalog.authorshipEstimateGenerations()
		if err != nil || want != built || s.Catalog.authorshipQuickPending() {
			return err
		}
	}
	return s.Catalog.completeDeferred(ctx, name, backfill.generation)
}

// refreshAnalysesWork is refreshGitInBackground's refresh as demand: Git,
// then tools, then findings (the full pass once a day).
func (w *workService) refreshAnalysesWork(ctx context.Context) error {
	var failure error
	for _, kind := range []string{kindGit, kindTools, kindFindings} {
		w.arm(kind, findingsAuto)
		if err := w.runLibrary(ctx, []string{kind}, false, triggerChained); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failure = errors.Join(failure, err)
		}
	}
	return failure
}

// runRefineWork is the daily writing refinement as armed coarse demand.
func (w *workService) runRefineWork(ctx context.Context, explicit bool) error {
	w.arm(kindRefine, findingsMeasure)
	return w.runLibrary(ctx, []string{kindRefine}, explicit, triggerChained)
}
