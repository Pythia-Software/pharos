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
	ID      string       `json:"id"`
	Parts   []sourcePart `json:"-"`
	Changed bool         `json:"changed"`
	Bytes   int64        `json:"bytes"`
	Version int64        `json:"version"`
	Source  SourceConfig `json:"-"`
	Host    string       `json:"host,omitempty"`
	Reader  Adapter      `json:"-"`
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
	err := c.DB.QueryRowContext(ctx, "SELECT enabled,interval_seconds,sources_json,COALESCE(next_at,'') FROM sync_settings WHERE host_id=?", currentHost().ID).
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
		choices = append(choices, map[string]any{"name": source.Name, "kind": source.Kind, "account": source.Account, "enabled": source.Enabled})
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
func (s *Server) runIncremental(parent context.Context, settings syncSettings, manual bool) bool {
	if manual && !s.acquireManualIngest() {
		return false
	}
	s.auto.mu.Lock()
	if !manual && (s.auto.cancel != nil || s.auto.manualWaiters > 0 || !s.ingestMu.TryLock()) {
		s.auto.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(parent)
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
	run := s.startRunNamed(kind, names)
	registered, end := s.beginIngest(run.ID)
	defer end()
	stop := context.AfterFunc(registered, cancel)
	defer stop()
	meter := beginSyncMeasurement()
	results := []IngestResult{}
	attemptedSources := []string{}
	var failure any
	pool := newAuditPool(s.Catalog)
	changedWorkspaces := map[string]bool{}
	for index, source := range sources {
		if ctx.Err() != nil {
			break
		}
		attemptedSources = append(attemptedSources, source.Name)
		phase := meter.beginPhase(source.Name)
		s.updateRun(run.ID, func(run *SyncRun) {
			run.CurrentSource = source.Name
			run.Phase = "checking"
			run.CompletedSources = index
		})
		adapter, err := syncAdapter(source)
		if err != nil {
			failure = err.Error()
			s.Catalog.recordAutomaticCheck(source.Name, err)
			meter.endPhase(phase)
			continue
		}
		setAdapterContext(adapter, ctx)
		preflight := ""
		if source.Kind == "conductor" {
			preflight, err = conductorPreflight(adapter)
			if err == nil {
				version, versionErr := s.Catalog.effectiveIndexVersion(currentHost().ID, source.Name, adapter)
				err = versionErr
				preflight = hashBytes([]byte(preflight + ":" + version))
			}
			if err != nil {
				failure = err.Error()
				s.Catalog.recordAutomaticCheck(source.Name, err)
				meter.endPhase(phase)
				continue
			}
			var previous string
			s.Catalog.DB.QueryRowContext(ctx, "SELECT COALESCE(preflight,'') FROM automatic_source_states WHERE host_id=? AND source_name=?", currentHost().ID, source.Name).Scan(&previous)
			if preflight == previous {
				if err := s.Catalog.recordAutomaticCheck(source.Name, nil); err != nil {
					failure = err.Error()
				}
				results = append(results, IngestResult{Source: source.Name, Host: currentHost().ID, SkippedUnchanged: true})
				meter.endPhase(phase)
				pool.preflightSkipped(source)
				continue
			}
		}
		mode := ingestMode{automatic: true, discovered: func(parts []sourcePart, changed bool) { pool.consider(unitOf(source, parts, changed)) }, written: func(id string) { changedWorkspaces[id] = true }}
		baseW, baseC, baseM := totals(results)
		result := s.Catalog.IngestContext(withIngestMode(ctx, mode), adapter, func(phase string, w, c, m, skipped, processed int) {
			s.updateRun(run.ID, func(run *SyncRun) {
				run.Phase = phase
				run.Workspaces = baseW + w
				run.Conversations = baseC + c
				run.Messages = baseM + m
			})
		})
		results = append(results, result)
		if result.Error != nil {
			failure = result.Error
		}
		completed := result.Error == nil && ctx.Err() == nil
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
		meter.endPhase(phase)
		s.updateRun(run.ID, func(run *SyncRun) { run.Results = slices.Clone(results); run.CompletedSources = index + 1 })
	}
	if len(changedWorkspaces) > 0 && ctx.Err() == nil {
		phase := meter.beginPhase("projections")
		ids := []string{}
		for id := range changedWorkspaces {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		if err := s.Catalog.refreshLibraryWorkspaces(ctx, ids); err != nil {
			failure = err.Error()
		}
		meter.endPhase(phase)
	}
	audit := auditResult{Outcome: "deferred", Detail: "run did not complete"}
	if ctx.Err() == nil && failure == nil {
		s.updateRun(run.ID, func(run *SyncRun) { run.Phase = "verifying"; run.CurrentSource = nil })
		phase := meter.beginPhase("audit")
		audit = s.Catalog.auditPool(ctx, pool, run.ID)
		meter.endPhase(phase)
	}
	if manual && ctx.Err() == nil {
		err := s.finishDeferredSyncPhases(ctx, true, func(name string, work func() error) error {
			s.updateRun(run.ID, func(run *SyncRun) { run.Phase = "refreshing " + name; run.CurrentSource = nil })
			phase := meter.beginPhase(name)
			err := work()
			meter.endPhase(phase)
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
		rows, err := queryMapsContext(r.Context(), s.Catalog.DB, "SELECT * FROM sync_integrity_issues ORDER BY last_at DESC LIMIT 100")
		writeResult(w, map[string]any{"issues": rows}, err)
		return
	}
	if r.URL.Path == "/api/sync/recovery" {
		if r.URL.Query().Get("estimate") == "true" {
			value, err := s.recoveryEstimate(r.Context(), r.URL.Query()["source"])
			writeResult(w, value, err)
			return
		}
		rows, err := queryMapsContext(r.Context(), s.Catalog.DB, "SELECT * FROM sync_recovery_jobs ORDER BY created_at DESC LIMIT 30")
		writeResult(w, map[string]any{"jobs": rows}, err)
		return
	}
	history, err := queryMapsContext(r.Context(), s.Catalog.DB, "SELECT finished_at,sample_json FROM sync_history WHERE host_id=? ORDER BY id DESC LIMIT 200", currentHost().ID)
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
	if r.URL.Path == "/api/sync/history" {
		writeJSON(w, map[string]any{"samples": samples, "statistics": syncStatistics(samples, settings.Interval)}, 200)
		return
	}
	states, err := queryMapsContext(r.Context(), s.Catalog.DB, `SELECT a.*,s.last_success_at AS intentional_index_at FROM automatic_source_states a LEFT JOIN source_states s ON s.host_id=a.host_id AND s.source_name=a.source_name WHERE a.host_id=?`, currentHost().ID)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	for _, row := range states {
		lastCapture := lastCaptureAt(s.Config().CaptureRoot, currentHost().ID, firstString(row["source_name"]))
		row["last_capture_at"] = lastCapture
		indexed, _ := parseTime(firstString(row["indexed_at"]))
		captured, _ := parseTime(lastCapture)
		row["pending_preservation"] = !indexed.IsZero() && indexed.After(captured)
	}
	deferred, _ := queryMaps(s.Catalog.DB, "SELECT * FROM sync_deferred WHERE generation>completed_generation")
	coverage, _ := queryMaps(s.Catalog.DB, "SELECT source_name,outcome,COUNT(*) units,MAX(checked_at) last_verification FROM sync_verifications WHERE host_id=? GROUP BY source_name,outcome", currentHost().ID)
	issues, _ := queryMaps(s.Catalog.DB, "SELECT id,source_name,unit,last_at FROM sync_integrity_issues WHERE state='open' ORDER BY last_at DESC LIMIT 100")
	jobs, _ := queryMapsContext(r.Context(), s.Catalog.DB, "SELECT * FROM sync_recovery_jobs WHERE state!='complete' ORDER BY updated_at DESC LIMIT 30")
	pause := ""
	if !settings.Enabled {
		pause = "Off"
	} else if s.captureActive() {
		pause = "Capturing"
	} else if s.syncActive() {
		pause = "Indexing"
	}
	writeJSON(w, map[string]any{"settings": settings, "sources": syncSourceChoices(s.autoSources(settings)), "available_sources": syncSourceChoices(s.autoSources(syncSettings{})), "source_states": states, "history": samples, "statistics": syncStatistics(samples, settings.Interval), "pause_reason": pause, "busy": s.syncActive() || s.captureActive(), "recovery_jobs": jobs, "deferred": deferred, "coverage": coverage, "issues": issues, "measurement_limits": syncMeasurementLimits}, 200)
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
		if !s.spawn(func(ctx context.Context) { s.runManualSync(ctx, settings) }) {
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
