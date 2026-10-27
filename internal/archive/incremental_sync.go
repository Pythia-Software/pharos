package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"
)

const automaticRunKind = "automatic-sync"
const manualSyncRunKind = "manual-sync"

type syncSettings struct {
	Enabled  bool     `json:"enabled"`
	Interval int      `json:"interval_seconds"`
	Sources  []string `json:"sources"`
	NextAt   string   `json:"next_at,omitempty"`
}

type automaticSync struct {
	mu            sync.Mutex
	cancel        context.CancelFunc
	done          chan struct{}
	cold          bool
	manualWaiters int
}

type syncUnit struct {
	ID      string           `json:"id"`
	Parts   []sourcePart     `json:"-"`
	Changed bool             `json:"changed"`
	Bytes   int64            `json:"bytes"`
	Version int64            `json:"version"`
	Source  SourceConfig     `json:"-"`
	Host    string           `json:"host,omitempty"`
	Reader  Adapter          `json:"-"`
	Record  *WorkspaceRecord `json:"-"`
}

type preparedSyncSource struct {
	adapter   Adapter
	plan      *sourceWorkPlan
	preflight string
	skipped   bool
	err       error
	close     func()
	progress  ProgressFunc
}

// Discover every selected source before reading transcripts. The retained
// inventory is the execution plan, so estimation adds no second filesystem walk.
func (s *Server) prepareSyncSource(ctx context.Context, source SourceConfig, p *preparedSyncSource) {
	input, err := s.Catalog.acquireRetainedInput(ctx, p.adapter, p.progress)
	if err != nil {
		p.err = err
		return
	}
	p.adapter, p.close = input.Adapter, input.Close
	setAdapterContext(p.adapter, ctx)
	if isConductorSource(source.Kind) {
		p.preflight, p.err = conductorPreflight(p.adapter)
		if p.err == nil {
			var version string
			version, p.err = s.Catalog.effectiveIndexVersion(currentHost().ID, source.Name, p.adapter)
			p.preflight = hashBytes([]byte(p.preflight + ":" + version))
		}
		if p.err != nil {
			return
		}
		var previous string
		_ = s.Catalog.reader().QueryRowContext(ctx, "SELECT COALESCE(preflight,'') FROM automatic_source_states WHERE host_id=? AND source_name=?", currentHost().ID, source.Name).Scan(&previous)
		current := true
		if source.Options["sqlite_record_capture"] == true {
			current, p.err = s.Catalog.sqliteReconciliationCurrent(source)
			if p.err != nil {
				return
			}
		}
		if p.preflight == previous && current {
			p.err = s.Catalog.recordInputIndexed(currentHost().ID, source.Name, "")
			p.skipped = p.err == nil
			return
		}
	}
	_ = s.Catalog.registerHost(currentHost(), "")
	p.plan, p.err = s.Catalog.prepareSourcePlan(withIngestMode(ctx, ingestMode{automatic: true}), p.adapter)
	if p.err != nil {
		_ = s.Catalog.recordInputAcquisition(currentHost().ID, source, "", p.err)
	}
}

func (unit syncUnit) hostID() string {
	return defaultString(unit.Host, currentHost().ID)
}

type sourceChangeReport struct {
	Source    string     `json:"source"`
	UnitKind  string     `json:"unit_kind"`
	CheckedAt string     `json:"checked_at"`
	Seconds   float64    `json:"seconds"`
	Complete  bool       `json:"complete"`
	Units     int        `json:"units"`
	Changed   []syncUnit `json:"changed"`
	Error     string     `json:"error,omitempty"`
}

func unitOf(source SourceConfig, parts []sourcePart, changed bool) syncUnit {
	unit := syncUnit{Source: source, Parts: slices.Clone(parts), Changed: changed}
	if len(parts) > 0 {
		unit.ID = parts[0].item
	}
	for _, part := range parts {
		unit.Bytes += part.size
		unit.Version = max(unit.Version, part.version)
	}
	return unit
}

func (c *Catalog) syncSettings(ctx context.Context) (syncSettings, error) {
	settings := syncSettings{Interval: 300, Sources: []string{}}
	var enabled int
	var sources string
	err := c.reader().QueryRowContext(ctx, "SELECT enabled,interval_seconds,sources_json,COALESCE(next_at,'') FROM sync_settings WHERE host_id=?", currentHost().ID).
		Scan(&enabled, &settings.Interval, &sources, &settings.NextAt)
	if err == sql.ErrNoRows {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	settings.Enabled = enabled != 0
	err = json.Unmarshal([]byte(sources), &settings.Sources)
	return settings, err
}

func automaticProvider(kind string) bool {
	return slices.Contains([]string{"claude", "codex", "conductor", "antigravity"}, kind)
}

func (s *Server) autoSources(settings syncSettings) []SourceConfig {
	sources := []SourceConfig{}
	for _, source := range s.Config().Sources {
		if source.Enabled && automaticProvider(source.Kind) && (len(settings.Sources) == 0 || slices.Contains(settings.Sources, source.Name)) {
			sources = append(sources, source)
		}
	}
	return sources
}

func syncSourceChoices(sources []SourceConfig) []map[string]any {
	choices := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		choices = append(choices, map[string]any{"name": source.Name, "kind": source.Kind, "account": source.Account, "enabled": source.Enabled, "preservation_mode": preservationMode(source)})
	}
	return choices
}

func (s *Server) acquireManualIngest() bool {
	s.auto.mu.Lock()
	s.auto.manualWaiters++
	defer func() { s.auto.manualWaiters--; s.auto.mu.Unlock() }()
	if s.auto.cancel != nil {
		s.auto.cancel()
		done := s.auto.done
		s.auto.mu.Unlock()
		select {
		case <-done:
		case <-s.life.ctx.Done():
			s.auto.mu.Lock()
			return false
		}
		s.auto.mu.Lock()
	}
	return s.ingestMu.TryLock()
}

func (s *Server) automaticLoop(ctx context.Context) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		settings, err := s.Catalog.syncSettings(ctx)
		if err == nil && settings.Enabled {
			due, _ := time.Parse(time.RFC3339Nano, settings.NextAt)
			if due.IsZero() || !time.Now().Before(due) {
				s.runAutomatic(ctx, settings)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Server) runAutomatic(parent context.Context, settings syncSettings) bool {
	return s.runIncremental(parent, settings, false)
}

func (s *Server) runManualSync(parent context.Context, settings syncSettings) bool {
	return s.runIncremental(parent, settings, true)
}

// Scheduled indexing leaves library-wide analysis pending. A manual request
// uses the same incremental writer, then refreshes every derived analysis.
// A manual run's operation (queued until it has the ingest coordinator)
// comes with parent; an automatic run registers its own once it has it.
func (s *Server) runIncremental(parent context.Context, settings syncSettings, manual bool) bool {
	handle := opFrom(parent)
	if manual && !s.acquireManualIngest() {
		if parent.Err() == nil {
			handle.Skip("already-running")
		}
		return false
	}
	handle.Run()
	s.auto.mu.Lock()
	if !manual && (s.auto.cancel != nil || s.auto.manualWaiters > 0 || !s.ingestMu.TryLock()) {
		s.auto.mu.Unlock()
		return false
	}
	if !manual {
		parent, handle = s.begin(parent, OperationSpec{Kind: "index.changes.automatic", Trigger: triggerSchedule})
		// Deferred first, so it runs last, after the run's final state.
		defer handle.Finish(nil)
	}
	ctx, cancel := context.WithCancel(parent)
	mode := modeOf(ctx)
	mode.automatic = !manual
	ctx = withIngestMode(ctx, mode)
	done := make(chan struct{})
	if !manual {
		s.auto.cancel, s.auto.done = cancel, done
	}
	cold := !s.auto.cold
	s.auto.cold = true
	s.auto.mu.Unlock()
	defer func() {
		cancel()
		s.auto.mu.Lock()
		if !manual {
			s.auto.cancel, s.auto.done = nil, nil
		}
		s.ingestMu.Unlock()
		close(done)
		s.auto.mu.Unlock()
	}()
	sources := s.autoSources(settings)
	names := []string{}
	for _, source := range sources {
		names = append(names, source.Name)
	}
	kind := automaticRunKind
	if manual {
		kind = manualSyncRunKind
	}
	run := s.startRunFor(handle, kind, names)
	registered, end := s.beginIngest(run.ID)
	defer end()
	stop := context.AfterFunc(registered, cancel)
	defer stop()
	meter := beginSyncMeasurement()
	workMeter := &runWorkMeter{server: s, runID: run.ID, kind: "sync"}
	defer func() { workMeter.release(ctx.Err() == nil) }()
	ctx = withWorkProgress(ctx, workMeter.report)
	results := []IngestResult{}
	attemptedSources := []string{}
	var failure any
	pool := newAuditPool(s.Catalog)
	changedWorkspaces := map[string]bool{}
	op := s.beginOperation(run.ID, kind)
	ctx = withOperationForecast(ctx, op)
	op.add("discovery", "incremental-discovery-v1", "Discover selected sources", workFeatures{}, false)
	prepared := make([]preparedSyncSource, len(sources))
	defer func() {
		for _, p := range prepared {
			if p.close != nil {
				p.close()
			}
		}
	}()
	for index, source := range sources {
		prepared[index].adapter, prepared[index].err = syncAdapter(source)
		model := source.Kind
		if prepared[index].adapter != nil {
			model = plannedIndexModel(prepared[index].adapter, false)
		}
		op.add("source:"+source.Name, model, "Index "+source.Name, workFeatures{}, false)
	}
	op.add("projections", "analysis-v1", "Refresh the Library view", workFeatures{}, true)
	op.add("audit", "audit-v1", "Verify changed inputs", workFeatures{}, true)
	if manual {
		addAnalysisForecast(op, true)
	}
	op.checkpoint("launch")
	op.begin("discovery")
	discoveryPhase := meter.beginPhase("discovery")
	for index, source := range sources {
		if ctx.Err() != nil {
			break
		}
		workMeter.model = "discovery-v1:" + source.Kind
		workMeter.report(activityWork{Phase: "discovering inputs", Source: source.Name})
		p := &prepared[index]
		p.progress = func(phase string, _, _, _, _, _ int) {
			s.updateRun(run.ID, func(run *SyncRun) { run.Phase = phase; run.CurrentSource = source.Name })
			workMeter.report(activityWork{Phase: phase, Source: source.Name})
		}
		if p.err == nil {
			// Discovery walks the source: an automatic run's walks run at
			// its priority class (Op.RunIO), a manual run's as they are.
			handle.RunIO(func() { s.prepareSyncSource(ctx, source, p) })
		}
		if p.err != nil {
			continue
		}
		if p.skipped {
			op.revise("source:"+source.Name, "", workFeatures{}, true)
			op.skip("source:" + source.Name)
		} else if p.plan != nil {
			op.revise("source:"+source.Name, plannedIndexModel(p.adapter, false), p.plan.features, p.plan.discoveryError == nil)
			if p.plan.discoveryError == nil {
				features := p.plan.features
				features.Groups = float64(len(p.plan.groups))
				workMeter.report(activityWork{Phase: "discovering inputs", Source: source.Name, Unit: "conversation group", Done: features.Groups, Total: features.Groups, TotalKnown: true, Features: features})
			}
		}
	}
	meter.endPhaseWork(discoveryPhase, workMeter.finish(ctx.Err() == nil))
	op.end("discovery", ctx.Err() == nil)
	willWrite := false
	for _, p := range prepared {
		willWrite = willWrite || p.plan != nil && p.plan.needed > 0
	}
	if manual {
		op.reviseAnalyses(ctx, true, willWrite)
	}
	op.checkpoint("discovered")
	for index, source := range sources {
		if ctx.Err() != nil {
			break
		}
		attemptedSources = append(attemptedSources, source.Name)
		phase := meter.beginPhase(source.Name)
		workMeter.model = "incremental-v1:" + source.Kind
		p := &prepared[index]
		adapter, err, preflight := p.adapter, p.err, p.preflight
		op.begin("source:" + source.Name)
		s.updateRun(run.ID, func(run *SyncRun) {
			run.CurrentSource, run.Phase = source.Name, "checking"
			run.CompletedSources, run.InputsChecked = index, 0
		})
		if errors.Is(err, errCaptureBusy) {
			results = append(results, IngestResult{Source: source.Name, Host: currentHost().ID, Pending: true, Deferred: true})
			op.end("source:"+source.Name, false)
			continue
		}
		if err != nil {
			failure = err.Error()
			results = append(results, IngestResult{Source: source.Name, Host: currentHost().ID, Pending: true, Error: err.Error(), Preservation: preservationMode(source)})
			s.Catalog.recordAutomaticCheck(source.Name, err)
			op.end("source:"+source.Name, false)
			continue
		}
		if p.skipped {
			if err := s.Catalog.recordAutomaticCheck(source.Name, nil); err != nil {
				failure = err.Error()
			}
			results = append(results, IngestResult{Source: source.Name, Host: currentHost().ID, SkippedUnchanged: true})
			op.skip("source:" + source.Name)
			pool.preflightSkipped(source)
			continue
		}
		checked := 0
		var plan *sourceWorkPlan
		model := ""
		var previousIndex string
		_ = s.Catalog.reader().QueryRowContext(ctx, "SELECT COALESCE(indexed_at,'') FROM automatic_source_states WHERE host_id=? AND source_name=?", currentHost().ID, source.Name).Scan(&previousIndex)
		elapsedSince := 0.0
		if indexed, ok := parseTime(previousIndex); ok {
			elapsedSince = max(0, time.Since(indexed).Seconds())
		}
		load := machineLoad()
		mode := ingestMode{automatic: true, plan: p.plan, planned: func(planned *sourceWorkPlan, key string) {
			plan, model = planned, key
			if elapsedSince > 0 {
				plan.features.SinceLastIndexSeconds = elapsedSince
			}
			plan.features.LoadOneMinute = load

		}, processed: func(parts []sourcePart) {
			if plan != nil && plan.size != nil {
				amount, unit := partWorkSize(parts)
				if unit == plan.size.Unit {
					plan.size.Done += amount
				}
			}
		}, discovered: func(parts []sourcePart, changed bool) {
			unit := unitOf(adapter.Config(), parts, changed)
			unit.Reader = adapter
			pool.consider(unit)
			checked++
			// This visits the retained inventory; it does not walk the source again.
			if checked == 1 || checked%100 == 0 {
				s.updateRun(run.ID, func(run *SyncRun) { run.InputsChecked = checked })
			}
		}, written: func(id string) { changedWorkspaces[id] = true }}
		baseW, baseC, baseM := totals(results)
		var result IngestResult
		handle.RunIO(func() {
			result = s.Catalog.IngestContext(withIngestMode(ctx, mode), adapter, func(phase string, w, c, m, skipped, processed int) {
				s.updateRun(run.ID, func(run *SyncRun) {
					run.Phase = phase
					run.Workspaces = baseW + w
					run.Conversations = baseC + c
					run.Messages = baseM + m
					run.InputsChecked = checked
				})
				work := activityWork{Phase: phase, Source: source.Name}
				if plan != nil && phase == "indexing" {
					work = plan.work(phase, source.Name, model, processed)
					work.Kind = "incremental"
				}
				work.Next = "Refresh the Library view and verify changed inputs"
				workMeter.report(work)
			})
		})
		op.end("source:"+source.Name, result.Error == nil && ctx.Err() == nil)
		results = append(results, result)
		if result.Error != nil {
			failure = result.Error
		}
		if result.Deferred {
			continue
		}
		completed := result.Error == nil && !result.Pending && ctx.Err() == nil
		stamp := now()
		_, err = s.Catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error,indexed_at,preflight,counts_json)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(host_id,source_name) DO UPDATE SET attempted_at=excluded.attempted_at,
			succeeded_at=COALESCE(excluded.succeeded_at,automatic_source_states.succeeded_at),error=excluded.error,
			indexed_at=COALESCE(excluded.indexed_at,automatic_source_states.indexed_at),
			preflight=COALESCE(excluded.preflight,automatic_source_states.preflight),counts_json=excluded.counts_json`, currentHost().ID, source.Name, stamp,
			conditionalString(completed, stamp), nilIfEmpty(firstString(result.Error)), conditionalString(result.Workspaces > 0, stamp), conditionalString(completed, preflight), jsonText(result))
		if err != nil {
			failure = err.Error()
		}
		meter.endPhaseWork(phase, workMeter.finish(ctx.Err() == nil))
		s.updateRun(run.ID, func(run *SyncRun) { run.Results = slices.Clone(results); run.CompletedSources = index + 1 })
	}
	if len(changedWorkspaces) == 0 {
		op.skip("projections")
	}
	if len(changedWorkspaces) > 0 && ctx.Err() == nil {
		op.begin("projections")
		s.updateRun(run.ID, func(run *SyncRun) { run.Phase, run.CurrentSource = "refreshing the Library view", nil })
		phase := meter.beginPhase("projections")
		workMeter.model = "analysis-v1:projections"
		workMeter.report(activityWork{Phase: "refreshing the Library view"})
		ids := []string{}
		for id := range changedWorkspaces {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		var err error
		handle.RunIO(func() { err = s.Catalog.refreshLibraryWorkspaces(ctx, ids) })
		if err != nil {
			failure = err.Error()
		}
		meter.endPhaseWork(phase, workMeter.finish(ctx.Err() == nil))
		op.end("projections", failure == nil && ctx.Err() == nil)
	}
	audit := auditResult{Outcome: "deferred", Detail: "run did not complete"}
	if ctx.Err() == nil && failure == nil {
		s.updateRun(run.ID, func(run *SyncRun) { run.Phase = "verifying"; run.CurrentSource = nil })
		phase := meter.beginPhase("audit")
		workMeter.model = "analysis-v1:audit"
		workMeter.report(activityWork{Phase: "verifying", Unit: "input", TotalKnown: true, Total: float64(min(1, len(pool.units)))})
		op.begin("audit")
		handle.RunIO(func() { audit = s.Catalog.auditPool(ctx, pool, run.ID) })
		op.end("audit", ctx.Err() == nil && audit.Outcome != "read_error")
		if audit.Outcome == "passed" {
			workMeter.report(activityWork{Phase: "verifying", Unit: "input", TotalKnown: true, Done: 1, Total: 1})
		}
		meter.endPhaseWork(phase, workMeter.finish(ctx.Err() == nil))
	}
	if manual && ctx.Err() == nil {
		err := s.finishDeferredSyncPhases(ctx, true, func(name string, work func() error) error {
			s.updateRun(run.ID, func(run *SyncRun) { run.Phase = "refreshing " + name; run.CurrentSource = nil })
			phase := meter.beginPhase(name)
			workMeter.model = "analysis-v1:" + name
			workMeter.report(activityWork{Phase: "refreshing " + name})
			err := work()
			meter.endPhaseWork(phase, workMeter.finish(err == nil && ctx.Err() == nil))
			return err
		})
		if err != nil {
			failure = err.Error()
		}
	}
	state := "complete"
	if ctx.Err() != nil {
		state = "interrupted"
	} else if failure != nil {
		state = "failed"
	} else {
		for _, result := range results {
			if result.Pending {
				state = "deferred"
				break
			}
		}
	}
	sample := meter.finish()
	sample.RunID, sample.State, sample.ColdStart, sample.Audit = run.ID, state, cold, audit
	sample.Trigger = "automatic"
	if manual {
		sample.Trigger = "manual"
	}
	sample.FinishedAt, sample.Sources, sample.SourceResults = now(), attemptedSources, results
	sample.Workspaces, sample.Conversations, sample.Messages = totals(results)
	sample.Class = "no_changes"
	if sample.Workspaces > 0 {
		sample.Class = "changes"
	}
	if state != "complete" {
		sample.Class = state
	} else if cold {
		sample.Class = "cold_start"
	} else if audit.Outcome == "deferred" || audit.Outcome == "read_error" {
		sample.Class += "_audit_deferred"
	}
	if manual {
		// Keep expensive manual-run costs out of automatic-run distributions.
		sample.Class = "manual_" + sample.Class
	}
	if _, err := s.Catalog.DB.Exec(`INSERT INTO sync_history(host_id,run_id,finished_at,sample_json) VALUES(?,?,?,?)`, currentHost().ID, run.ID, sample.FinishedAt, jsonText(sample)); err != nil {
		failure = err.Error()
		state = "failed"
	}
	s.Catalog.DB.Exec(`DELETE FROM sync_history WHERE host_id=? AND id NOT IN (SELECT id FROM sync_history WHERE host_id=? ORDER BY id DESC LIMIT 200)`, currentHost().ID, currentHost().ID)
	next := time.Now().Add(time.Duration(settings.Interval) * time.Second).UTC().Format(time.RFC3339Nano)
	s.Catalog.DB.Exec("UPDATE sync_settings SET next_at=? WHERE host_id=? AND interval_seconds=? AND enabled=1 AND COALESCE(next_at,'')=?", next, currentHost().ID, settings.Interval, settings.NextAt)
	workMeter.release(state == "complete")
	op.finish(state)
	s.updateRun(run.ID, func(run *SyncRun) {
		run.State, run.Phase = state, state
		run.Results = slices.Clone(results)
		run.Error = failure
		run.CompletedAt = now()
		run.CurrentSource = nil
	})
	return true
}

func (c *Catalog) recordAutomaticCheck(source string, failure error) error {
	stamp := now()
	_, err := c.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error)
		VALUES(?,?,?,?,?) ON CONFLICT(host_id,source_name) DO UPDATE SET attempted_at=excluded.attempted_at,
		succeeded_at=COALESCE(excluded.succeeded_at,automatic_source_states.succeeded_at),error=excluded.error`, currentHost().ID, source, stamp, conditionalString(failure == nil, stamp), nilIfEmpty(errorString(failure)))
	return err
}

func conditionalString(condition bool, value string) any {
	if condition {
		return nilIfEmpty(value)
	}
	return nil
}

func markSyncDeferred(tx *sql.Tx) error {
	for _, name := range []string{"identities", "git", "tools", "authorship", "findings"} {
		if _, err := tx.Exec(`INSERT INTO sync_deferred(name,generation,changed_at) VALUES(?,1,?)
			ON CONFLICT(name) DO UPDATE SET generation=generation+1,changed_at=excluded.changed_at`, name, now()); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) sourceChanges(ctx context.Context, source SourceConfig) sourceChangeReport {
	started := time.Now()
	report := sourceChangeReport{Source: source.Name, CheckedAt: now(), Changed: []syncUnit{}, Complete: false, UnitKind: "transcript/group"}
	if source.Kind == "conductor" {
		report.UnitKind = "session"
	} else if source.Kind == "antigravity" {
		report.UnitKind = "root group"
	}
	adapter, err := syncAdapter(source)
	if err == nil {
		setAdapterContext(adapter, ctx)
		partial, ok := adapter.(partialAdapter)
		if !ok {
			err = errors.New("this source has no bounded change discovery")
		} else {
			var tracker *partTracker
			tracker, err = c.newPartTracker(currentHost().ID, source.Name, partial, nil)
			if err == nil {
				err = partial.discoverParts(func(parts []sourcePart) bool {
					report.Units++
					if !tracker.unchanged(parts) {
						report.Changed = append(report.Changed, unitOf(source, parts, true))
					}
					return true
				}, func(*WorkspaceRecord, []sourcePart) error { return errors.New("change discovery cannot parse records") })
			}
		}
	}
	report.Seconds = time.Since(started).Seconds()
	if err != nil {
		report.Error = err.Error()
	} else {
		report.Complete = true
	}
	return report
}

func (s *Server) sourceChanges(w http.ResponseWriter, r *http.Request) {
	name, _ := url.PathUnescape(stringsTrimSourceChanges(r.URL.Path))
	source, err := s.configuredSource(name)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	report := s.Catalog.sourceChanges(r.Context(), source)
	writeJSON(w, report, http.StatusOK)
}

func stringsTrimSourceChanges(path string) string {
	return path[len("/api/sources/") : len(path)-len("/changes")]
}

func (s *Server) getIncrementalSync(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Catalog.syncSettings(r.Context())
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if r.URL.Path == "/api/sync/settings" {
		writeJSON(w, settings, 200)
		return
	}
	if r.URL.Path == "/api/sync/issues" {
		rows, err := queryMapsContext(r.Context(), s.Catalog.reader(), "SELECT * FROM sync_integrity_issues ORDER BY last_at DESC LIMIT 100")
		writeResult(w, map[string]any{"issues": rows}, err)
		return
	}
	if r.URL.Path == "/api/sync/recovery" {
		if r.URL.Query().Get("estimate") == "true" {
			value, err := s.recoveryEstimate(r.Context(), r.URL.Query()["source"])
			writeResult(w, value, err)
			return
		}
		rows, err := queryMapsContext(r.Context(), s.Catalog.reader(), "SELECT * FROM sync_recovery_jobs ORDER BY created_at DESC LIMIT 30")
		writeResult(w, map[string]any{"jobs": rows}, err)
		return
	}
	history, err := queryMapsContext(r.Context(), s.Catalog.reader(), "SELECT finished_at,sample_json FROM sync_history WHERE host_id=? ORDER BY id DESC LIMIT 200", currentHost().ID)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	samples := []syncMeasurement{}
	for _, row := range history {
		var sample syncMeasurement
		if json.Unmarshal([]byte(firstString(row["sample_json"])), &sample) == nil {
			sample.FinishedAt = firstString(row["finished_at"])
			samples = append(samples, sample)
		}
	}
	forecastAccuracy, forecastRuns := s.forecastReports()
	for i := range samples {
		for _, forecast := range forecastRuns {
			if forecast.RunID == samples[i].RunID {
				samples[i].Forecasts = forecast.Forecasts
				break
			}
		}
	}

	if r.URL.Path == "/api/sync/history" {
		writeJSON(w, map[string]any{"samples": samples, "statistics": syncStatistics(samples, settings.Interval), "forecast_accuracy": forecastAccuracy, "forecast_runs": forecastRuns}, 200)
		return
	}
	states, err := queryMapsContext(r.Context(), s.Catalog.reader(), `SELECT a.*,s.last_success_at AS intentional_index_at FROM automatic_source_states a LEFT JOIN source_states s ON s.host_id=a.host_id AND s.source_name=a.source_name WHERE a.host_id=?`, currentHost().ID)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	for _, row := range states {
		lastCapture := lastCaptureAt(s.Config().CaptureRoot, currentHost().ID, firstString(row["source_name"]))
		row["last_capture_at"] = lastCapture
		preservation := s.Catalog.inputPreservationStatus(r.Context(), currentHost().ID, firstString(row["source_name"]))
		row["preservation"] = preservation
		row["pending_preservation"] = capturePending(firstString(row["indexed_at"]), lastCapture, preservation)
	}
	deferred, _ := queryMaps(s.Catalog.reader(), "SELECT * FROM sync_deferred WHERE generation>completed_generation")
	coverage, _ := queryMaps(s.Catalog.reader(), "SELECT source_name,outcome,COUNT(*) units,MAX(checked_at) last_verification FROM sync_verifications WHERE host_id=? GROUP BY source_name,outcome", currentHost().ID)
	issues, _ := queryMaps(s.Catalog.reader(), "SELECT id,source_name,unit,last_at FROM sync_integrity_issues WHERE state='open' ORDER BY last_at DESC LIMIT 100")
	jobs, _ := queryMapsContext(r.Context(), s.Catalog.reader(), "SELECT * FROM sync_recovery_jobs WHERE state!='complete' ORDER BY updated_at DESC LIMIT 30")
	pause := ""
	if !settings.Enabled {
		pause = "Off"
	} else if s.captureActive() {
		pause = "Capturing"
	} else if s.syncActive() {
		pause = "Indexing"
	}
	// Existence checks keep empty-page status cheap even on a large library.
	var libraryEmpty bool
	if err := s.Catalog.reader().QueryRowContext(r.Context(), "SELECT NOT EXISTS(SELECT 1 FROM workspaces) AND NOT EXISTS(SELECT 1 FROM conversations)").Scan(&libraryEmpty); err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"settings": settings, "sources": syncSourceChoices(s.autoSources(settings)), "available_sources": syncSourceChoices(s.autoSources(syncSettings{})), "source_states": states, "history": samples, "statistics": syncStatistics(samples, settings.Interval), "pause_reason": pause, "busy": s.syncActive() || s.captureActive(), "library_empty": libraryEmpty, "has_sources": slices.ContainsFunc(s.Config().Sources, func(source SourceConfig) bool { return source.Enabled }), "recovery_jobs": jobs, "deferred": deferred, "coverage": coverage, "issues": issues, "measurement_limits": syncMeasurementLimits, "forecast_accuracy": forecastAccuracy, "forecast_runs": forecastRuns}, 200)
}

// capturePending is the Sources table's "Needs capture": the source was indexed
// after its last capture, or an opted-in acquisition has inputs not yet indexed.
func capturePending(indexedAt, lastCapture string, preservation map[string]any) bool {
	indexed, _ := parseTime(indexedAt)
	captured, _ := parseTime(lastCapture)
	return !indexed.IsZero() && indexed.After(captured) || preservation["pending_newer_inputs"] == true
}

func lastCaptureAt(root, host, source string) string {
	manifest, err := loadCaptureManifest(filepath.Join(root, host, source))
	if err != nil {
		return ""
	}
	return manifest.lastCapturedDataAt()
}

func (s *Server) postIncrementalSync(w http.ResponseWriter, r *http.Request, body map[string]any) {
	switch r.URL.Path {
	case "/api/sync/settings":
		settings, err := s.Catalog.syncSettings(r.Context())
		if err != nil {
			writeError(w, err, 500)
			return
		}
		if enabled, ok := body["enabled"]; ok {
			value, valid := enabled.(bool)
			if !valid {
				writeError(w, errors.New("enabled must be boolean"), 400)
				return
			}
			settings.Enabled = value
		}
		if interval, ok := body["interval_seconds"]; ok {
			value, valid := number(interval)
			if !valid || value < 60 || value > 86400 || value != float64(int(value)) {
				writeError(w, errors.New("interval_seconds must be an integer from 60 to 86400"), 400)
				return
			}
			settings.Interval = int(value)
		}
		if names, ok := body["sources"]; ok {
			values, valid := sliceValue(names)
			if !valid {
				writeError(w, errors.New("sources must be source names"), 400)
				return
			}
			settings.Sources = stringSlice(values)
			for _, name := range settings.Sources {
				source, err := s.configuredSource(name)
				if err != nil || !automaticProvider(source.Kind) {
					writeError(w, fmt.Errorf("unsupported automatic source %q", name), 400)
					return
				}
			}
		}
		settings.NextAt = time.Now().Add(time.Duration(settings.Interval) * time.Second).UTC().Format(time.RFC3339Nano)
		s.auto.mu.Lock()
		if !settings.Enabled && s.auto.cancel != nil {
			s.auto.cancel()
		}
		s.Catalog.projectionBatches.Add(1)
		err = s.Catalog.writeTransaction(r.Context(), "sync settings", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), `INSERT INTO sync_settings(host_id,enabled,interval_seconds,sources_json,next_at) VALUES(?,?,?,?,?)
				ON CONFLICT(host_id) DO UPDATE SET enabled=excluded.enabled,interval_seconds=excluded.interval_seconds,sources_json=excluded.sources_json,next_at=excluded.next_at`, currentHost().ID, settings.Enabled, settings.Interval, jsonText(settings.Sources), settings.NextAt)
			return err
		})
		s.Catalog.projectionBatches.Add(-1)
		s.auto.mu.Unlock()
		if err != nil {
			writeError(w, err, 500)
			return
		}
		writeJSON(w, settings, 200)
	case "/api/sync/check":
		settings, err := s.Catalog.syncSettings(r.Context())
		if err != nil {
			writeError(w, err, 500)
			return
		}
		// The run waits (queued) for an automatic index to yield.
		if !s.start(OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser, Queued: true}, func(ctx context.Context, op *Op) error {
			if !s.runManualSync(ctx, settings) && ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}) {
			writeError(w, errors.New("service stopping"), 503)
			return
		}
		writeJSON(w, map[string]any{"accepted": true}, 202)
	case "/api/sync/stop":
		writeJSON(w, map[string]any{"stopped": s.stopIngest()}, 200)
	case "/api/sync/recovery", "/api/library/update":
		s.startRecovery(w, r, body)
	case "/api/sync/verify":
		s.startVerification(w, r, body)
	default:
		writeError(w, errors.New("not found"), 404)
	}
}

func conductorPreflight(adapter Adapter) (string, error) {
	fingerprint, err := adapter.Fingerprint()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(adapter.Config().Path)
	if err != nil {
		return "", err
	}
	identity := ""
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	return hashBytes([]byte(fingerprint + ":" + identity + ":" + sourceIndexVersion(adapter))), nil
}
