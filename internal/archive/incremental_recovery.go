package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"
)

func (s *Server) recoveryEstimate(ctx context.Context, names []string) (map[string]any, error) {
	result := map[string]any{"files": 0, "bytes": int64(0), "groups": int64(0), "capture_seconds": nil, "index_seconds": nil,
		"note": "Estimates use measured copy and committed-ingest throughput, not parse speed alone. Verification and library-wide analysis add time. Missing measurements are shown as unknown; other Macs use retained evidence."}
	targets, err := captureTargets(s.Config().CaptureRoot, nil, true, nil)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var captureSeconds float64
	captureKnown := true
	for _, target := range targets {
		if len(names) > 0 && !slices.Contains(names, target.Source) {
			continue
		}
		manifest, err := loadCaptureManifest(target.Dir)
		if err != nil {
			return nil, err
		}
		var bytes int64
		for _, file := range manifest.Files {
			bytes += file.Size
		}
		for _, snapshot := range manifest.Snapshots {
			bytes += snapshot.Size
		}
		result["files"] = result["files"].(int) + len(manifest.Files) + len(manifest.Snapshots)
		result["bytes"] = result["bytes"].(int64) + bytes
		if target.Host.ID != currentHost().ID {
			continue
		}
		if run := manifest.LastRun; run != nil && run.BytesCopied+run.SnapshotBytes > 0 && len(run.Errors) == 0 {
			started, _ := parseTime(run.StartedAt)
			finished, _ := parseTime(run.FinishedAt)
			if !started.IsZero() && finished.After(started) {
				captureSeconds += finished.Sub(started).Seconds() * float64(bytes) / float64(run.BytesCopied+run.SnapshotBytes)
				continue
			}
		}
		captureKnown = false
	}
	if captureKnown && len(targets) > 0 {
		result["capture_seconds"] = captureSeconds
	}
	where, args := "", []any{}
	if len(names) > 0 {
		where = " WHERE source_name IN (" + placeholders(len(names)) + ")"
		for _, name := range names {
			args = append(args, name)
		}
	}
	groups, err := queryMapsContext(ctx, s.Catalog.DB, "SELECT source_name,COUNT(*) AS groups FROM source_record_states"+where+" GROUP BY source_name", args...)
	if err != nil {
		return nil, err
	}
	rates := map[string]float64{}
	jobs, err := queryMapsContext(ctx, s.Catalog.DB, "SELECT progress_json FROM sync_recovery_jobs WHERE mode='full' AND state='complete' ORDER BY updated_at DESC LIMIT 30")
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		var results []IngestResult
		if json.Unmarshal([]byte(firstString(job["progress_json"])), &results) != nil {
			continue
		}
		for _, sample := range results {
			if sample.Workspaces > 0 && sample.WriteSeconds+sample.DiscoveryParseSeconds > 0 && rates[sample.Source] == 0 {
				rates[sample.Source] = (sample.WriteSeconds + sample.DiscoveryParseSeconds) / float64(sample.Workspaces)
			}
		}
	}
	indexSeconds, indexKnown := float64(0), len(groups) > 0
	for _, group := range groups {
		count := integer(group["groups"])
		result["groups"] = result["groups"].(int64) + count
		rate := rates[firstString(group["source_name"])]
		indexKnown = indexKnown && rate > 0
		indexSeconds += rate * float64(count)
	}
	if indexKnown {
		result["index_seconds"] = indexSeconds
	}
	return result, nil
}

func (s *Server) startRecovery(w http.ResponseWriter, r *http.Request, body map[string]any) {
	mode := firstString(body["mode"])
	if r.URL.Path == "/api/library/update" {
		mode = "update"
	}
	if mode == "" {
		mode = "full"
	}
	if !slices.Contains([]string{"update", "full", "retained"}, mode) {
		writeError(w, errors.New("mode must be full or retained"), 400)
		return
	}
	values, valid := sliceValue(body["sources"])
	if !valid {
		writeError(w, errors.New("sources must be a list of names"), 400)
		return
	}
	names := stringSlice(values)
	resume := firstString(body["resume"])
	if resume != "" {
		var scope, state string
		if err := s.Catalog.DB.QueryRowContext(r.Context(), "SELECT mode,scope_json,state FROM sync_recovery_jobs WHERE id=?", resume).Scan(&mode, &scope, &state); err != nil {
			writeError(w, err, 400)
			return
		}
		if state == "complete" {
			writeJSON(w, map[string]any{"complete": true, "job_id": resume}, 200)
			return
		}
		if err := json.Unmarshal([]byte(scope), &names); err != nil {
			writeError(w, err, 500)
			return
		}
	}
	if !s.acquireManualIngest() {
		writeError(w, errors.New("a capture or index is already running"), 409)
		return
	}
	// Updating the library resumes a Library view refresh the user stopped.
	if r.URL.Path == "/api/library/update" {
		s.Catalog.resumeLibraryRefresh()
	}
	kind, variant := runOperationKind("library-update", mode)
	trigger := triggerUser
	if resume != "" {
		trigger = "resume"
	}
	_, handle := s.begin(s.life.ctx, OperationSpec{Kind: kind, Variant: variant, Trigger: trigger})
	run := s.startRunFor(handle, "library-update", names)
	ctx, end := s.beginIngest(run.ID)
	ctx = withOp(ctx, handle)
	job := resume
	if job == "" {
		job = run.ID
	}
	if resume == "" {
		if err := s.Catalog.writeTransaction(ctx, "sync recovery generation", func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES(?,?,?,'running',?,?)", job, mode, jsonText(names), now(), now()); err != nil {
				return err
			}
			if mode != "full" {
				return nil
			}
			keys := map[string][2]string{}
			for _, source := range s.Config().Sources {
				if source.Enabled && (len(names) == 0 || slices.Contains(names, source.Name)) {
					keys[currentHost().ID+"/"+source.Name] = [2]string{currentHost().ID, source.Name}
				}
			}
			targets, err := captureTargets(s.Config().CaptureRoot, nil, true, nil)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			for _, target := range targets {
				if len(names) > 0 && !slices.Contains(names, target.Source) {
					continue
				}
				keys[target.Host.ID+"/"+target.Source] = [2]string{target.Host.ID, target.Source}
			}
			for _, key := range keys {
				if _, err := tx.Exec(`INSERT INTO sync_generations(host_id,source_name,generation) VALUES(?,?,1)
					ON CONFLICT(host_id,source_name) DO UPDATE SET generation=generation+1`, key[0], key[1]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			end()
			s.ingestMu.Unlock()
			s.updateRun(run.ID, func(run *SyncRun) { run.State = "failed"; run.Error = err.Error(); run.CompletedAt = now() })
			handle.Finish(err)
			writeError(w, err, 500)
			return
		}
	} else if _, err := s.Catalog.DB.ExecContext(ctx, "UPDATE sync_recovery_jobs SET state='running',updated_at=?,error=NULL WHERE id=?", now(), job); err != nil {
		end()
		s.ingestMu.Unlock()
		s.updateRun(run.ID, func(run *SyncRun) { run.State = "failed"; run.Error = err.Error(); run.CompletedAt = now() })
		handle.Finish(err)
		writeError(w, err, 500)
		return
	}
	recovery := func() {
		defer s.ingestMu.Unlock()
		defer end()
		op := s.beginOperation(run.ID, "library-update:"+mode)
		ctx := withOperationForecast(ctx, op)
		if mode == "retained" {
			op.add("repair", "retained-v1", "Rebuild retained messages", s.analysisForecastFeatures(), true)
		} else {
			op.add("capture", "capture-v1", "Capture selected sources", workFeatures{}, false)
			op.add("discovery", "capture-discovery-v1", "Discover captured inputs", workFeatures{}, false)
			op.add("index", "", "Index captured inputs", workFeatures{}, false)
			addAnalysisForecast(op, false)
			op.add("verification", "audit-v1", "Verify retained inputs", workFeatures{}, true)
		}
		op.checkpoint("launch")
		workMeter := &runWorkMeter{server: s, runID: run.ID, kind: "sync", model: "analysis-v1"}
		defer func() { workMeter.release(ctx.Err() == nil) }()
		ctx = withWorkProgress(ctx, workMeter.report)
		var failure error
		results := []IngestResult{}
		finish := func() {
			state := "complete"
			if ctx.Err() != nil {
				state = "interrupted"
			} else if failure != nil {
				state = "failed"
			}
			workMeter.release(state == "complete")
			op.finish(state)
			s.Catalog.DB.Exec("UPDATE sync_recovery_jobs SET state=?,updated_at=?,error=?,progress_json=? WHERE id=?", state, now(), nilIfEmpty(errorString(failure)), jsonText(results), job)
			s.updateRun(run.ID, func(run *SyncRun) {
				run.State, run.Phase = state, state
				run.CompletedAt = now()
				run.CurrentSource = nil
				run.Results = slices.Clone(results)
				run.Error = nilIfEmpty(errorString(failure))
			})
		}
		defer finish()
		if mode == "retained" {
			op.begin("repair")
			failure = s.Catalog.repairRetained(ctx, job, names, func(done, total int) {
				s.updateRun(run.ID, func(run *SyncRun) {
					run.Phase = "rebuilding retained messages"
					run.Progress = fraction(float64(done), float64(total))
					run.setWork(run.Phase, firstString(run.CurrentSource), "workspace", float64(done), float64(total))
				})
			})
			op.end("repair", failure == nil && ctx.Err() == nil)
			return
		}
		config := s.Config()
		if mode == "full" {
			config.CaptureRecoveryID = job
		}
		sources := []SourceConfig{}
		for _, source := range config.Sources {
			if source.Enabled && (len(names) == 0 || slices.Contains(names, source.Name)) {
				sources = append(sources, source)
			}
		}
		s.updateRun(run.ID, func(run *SyncRun) { run.Phase = "capturing fresh evidence" })
		op.begin("capture")
		if len(sources) > 0 {
			completed := 0
			var previous CaptureSourceResult
			summary, err := Capture(ctx, config, sources, func(result CaptureSourceResult) {
				if previous.Phase == "copying files" && result.Phase != previous.Phase && result.Name == previous.Name && len(result.Errors) == 0 && ctx.Err() == nil {
					s.recordWorkTiming("capture", captureWork(previous), time.Now())
				}
				previous = result
				if result.State != "running" {
					completed++
				}
				s.updateRun(run.ID, func(run *SyncRun) {
					work := captureWork(result)
					run.Phase, run.CurrentSource, run.Work = work.Phase, result.Name, &work
					run.TotalSources, run.CompletedSources = len(sources), completed
				})
			})
			if err != nil {
				failure = err
			} else if !summary.OK {
				failure = errors.New("some selected inputs could not be freshly captured; retained evidence is still indexed")
			}
		}
		op.end("capture", failure == nil && ctx.Err() == nil)
		if ctx.Err() != nil {
			return
		}
		targets, err := captureTargets(config.CaptureRoot, nil, true, names)
		if err != nil {
			failure = err
			return
		}
		s.updateRun(run.ID, func(run *SyncRun) { run.CurrentSource = nil; run.TotalSources, run.CompletedSources = len(targets), 0 })
		indexCtx := withIngestMode(ctx, ingestMode{force: mode == "full", deferGit: true})
		op.begin("discovery")
		plan := s.countIndexWork(indexCtx, run.ID, targets)
		op.end("discovery", ctx.Err() == nil)
		op.checkpoint("discovered")
		op.begin("index")
		for index, target := range targets {
			if ctx.Err() != nil {
				return
			}
			baseW, baseC, baseM := totals(results)
			meter := plan.meter(index)
			result := s.Catalog.IndexCapture(meter.context(indexCtx), target, func(phase string, w, c, m, skipped, processed int) {
				s.updateRun(run.ID, func(run *SyncRun) {
					run.Phase = phase
					run.CurrentSource = target.label()
					run.TotalSources = len(targets)
					run.CompletedSources = index
					plan.report(run, index, processed)
					meter.report(run, phase, target.label(), processed)
					run.Workspaces = baseW + w
					run.Conversations = baseC + c
					run.Messages = baseM + m
				})
			})
			meter.record(s, ctx, result)
			plan.finish(index)
			s.updateRun(run.ID, func(run *SyncRun) { run.CompletedSources = index + 1; plan.report(run, index+1, 0) })
			results = append(results, result)
			if result.Error != nil {
				failure = fmt.Errorf("%s: %v", target.label(), result.Error)
			}
			s.Catalog.DB.Exec("UPDATE sync_recovery_jobs SET progress_json=?,updated_at=? WHERE id=?", jsonText(results), now(), job)
		}
		op.end("index", failure == nil && ctx.Err() == nil)
		if ctx.Err() == nil {
			s.updateRun(run.ID, func(run *SyncRun) {
				run.Phase, run.CurrentSource = "reconciling deferred work", nil
				run.Progress = nil
			})
			if err := s.finishDeferredSyncPhases(ctx, false, func(name string, work func() error) error {
				workMeter.model = "analysis-v1:" + name
				workMeter.report(activityWork{Phase: "refreshing " + name})
				err := work()
				workMeter.finish(err == nil)
				return err
			}); err != nil {
				failure = err
			}
			op.begin("verification")
			s.updateRun(run.ID, func(run *SyncRun) { run.Phase = "verifying" })
			s.auditOpenIssues(ctx, run.ID, names)
			s.auditIntentional(ctx, run.ID, names)
			op.end("verification", ctx.Err() == nil)
		}
	}
	started := s.launch(handle, func(context.Context, *Op) error { recovery(); return nil })
	if !started {
		end()
		s.ingestMu.Unlock()
		writeError(w, errors.New("service stopping"), 503)
		return
	}
	writeJSON(w, map[string]any{"accepted": true, "run_id": run.ID, "job_id": job}, 202)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) finishDeferredSync(ctx context.Context) error {
	return s.finishDeferredSyncPhases(ctx, false, nil)
}

// runPhase lets manual sync expose and measure each expensive stage. Keep the
// same completion guards for Capture & index and manual incremental indexing.
//
// The chain is an analysis.chain stage of the run ctx carries, and each of
// its stages a child of it (docs/background-work.md §2.4).
func (s *Server) finishDeferredSyncPhases(ctx context.Context, forceFindings bool, runPhase func(string, func() error) error) (err error) {
	ctx, chain := s.Catalog.beginOp(ctx, OperationSpec{Kind: "analysis.chain", Trigger: triggerChained})
	defer func() { chain.Finish(err) }()
	measure := runPhase
	if measure == nil {
		measure = func(_ string, work func() error) error { return work() }
	}
	var stage *Op
	invoke := func(name string, work func() error) error {
		_, stage = chain.childFrom(ctx, OperationSpec{Kind: "analysis." + name, Trigger: triggerChained})
		err := measure(name, work)
		stage.Finish(err)
		return err
	}
	op := operationOf(ctx)
	runPhase = invoke
	if op != nil {
		runPhase = func(name string, work func() error) error {
			features := s.analysisStepFeatures(ctx, name, s.analysisForecastFeatures(), forceFindings, false)
			op.revise("analysis:"+name, "", features, true)
			op.begin("analysis:" + name)
			err := invoke(name, work)
			op.end("analysis:"+name, err == nil && ctx.Err() == nil)
			return err
		}
	}
	if err := runPhase("github", func() error { return s.Catalog.RefreshRepositoryForgeIDs(ctx) }); err != nil {
		return err
	}
	// Repository merges can invalidate analyses, so snapshot dirty generations
	// after GitHub resolution and before the remaining stages.
	rows, err := queryMapsContext(ctx, s.Catalog.DB, "SELECT name,generation FROM sync_deferred WHERE generation>completed_generation")
	if err != nil {
		return err
	}
	generation := map[string]int64{}
	for _, row := range rows {
		generation[firstString(row["name"])] = integer(row["generation"])
	}
	complete := func(name string) error {
		return s.Catalog.writeTransaction(ctx, "complete deferred "+name, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE sync_deferred SET completed_generation=MAX(completed_generation,?),completed_at=? WHERE name=?", generation[name], now(), name)
			return err
		})
	}
	stages := []struct {
		name string
		work func() error
	}{
		{"identities", func() error { _, err := s.Catalog.ReconcileIdentitiesContext(ctx); return err }},
		{"git", func() error { return s.Catalog.refreshMainIntegrations(ctx) }},
		{"tools", func() error { return s.Catalog.ensureToolRollup(ctx) }},
		{"authorship", func() error {
			// The rebuild runs in the background, as a child of this stage.
			s.Catalog.refreshAuthorshipInContext(withOp(ctx, stage))
			for s.Catalog.authorshipRunning() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
			}
			want, built, err := s.Catalog.authorshipGenerations()
			if err != nil {
				return err
			}
			if want != built {
				return errors.New("authorship rebuild did not complete; dirty generation retained")
			}
			return nil
		}},
		{"findings", func() error {
			// A page or another explicit action may already be discovering
			// findings. Wait before asking for this run's fresh pass.
			for {
				running, _ := s.Catalog.findingsRunning()
				if !running {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
			}
			return s.Catalog.RefreshFindings(ctx, forceFindings)
		}},
	}
	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runPhase(stage.name, func() error {
			if err := stage.work(); err != nil {
				return err
			}
			return complete(stage.name)
		}); err != nil {
			return err
		}
	}
	// The freshness ledger's upgrade count (§5), cheap next to the chain.
	s.Catalog.storeUpgradeCount(ctx)
	return nil
}

func (c *Catalog) repairRetained(ctx context.Context, job string, names []string, progress func(int, int)) error {
	var cursor string
	if err := c.DB.QueryRowContext(ctx, "SELECT COALESCE(cursor,'') FROM sync_recovery_jobs WHERE id=?", job).Scan(&cursor); err != nil {
		return err
	}
	where, args := "", []any{}
	if len(names) > 0 {
		where = " WHERE EXISTS (SELECT 1 FROM workspace_sightings s WHERE s.workspace_id=w.id AND s.source_name IN (" + placeholders(len(names)) + "))"
		for _, name := range names {
			args = append(args, name)
		}
	}
	workspaces, err := queryMapsContext(ctx, c.DB, "SELECT w.id FROM workspaces w"+where+" ORDER BY w.id", args...)
	if err != nil {
		return err
	}
	for index, workspace := range workspaces {
		id := firstString(workspace["id"])
		if id <= cursor {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.writeTransaction(ctx, "repair retained "+id, func(tx *sql.Tx) error {
			rows, err := queryMapsContext(ctx, tx, "SELECT * FROM conversations WHERE workspace_id=? ORDER BY started_at,id", id)
			if err != nil {
				return err
			}
			merged := map[string]bool{}
			for _, row := range rows {
				conversationID := firstString(row["id"])
				messages, err := storedMessages(ctx, tx, conversationID)
				if err != nil {
					return err
				}
				conversation := ConversationRecord{Provider: firstString(row["provider"]), Model: firstString(row["model"]), Effort: firstString(row["effort"]), NativeID: firstString(row["native_id"]), Account: firstString(row["account"]), Origin: firstString(row["origin"]), StartedAt: firstString(row["started_at"]), EndedAt: firstString(row["ended_at"]), AgentDepth: int(integer(row["agent_depth"])), AgentPath: firstString(row["agent_path"]), AgentNickname: firstString(row["agent_nickname"]), Coverage: firstString(row["coverage"]), Harness: firstString(row["harness"]), HarnessVersionFirst: firstString(row["harness_version_first"]), HarnessVersionLast: firstString(row["harness_version_last"]), HarnessVersionSource: firstString(row["harness_version_source"]), Messages: messages}
				if err := deleteConversationFTS(tx, conversationID); err != nil {
					return err
				}
				if err := insertConversationFTS(tx, conversationID); err != nil {
					return err
				}
				if err := upsertConversationDocument(tx, conversationID); err != nil {
					return err
				}
				for _, table := range []string{"agent_sessions", "skill_usages", "tool_calls", "model_requests", "tool_ledger_inputs"} {
					if _, err := tx.Exec("DELETE FROM "+table+" WHERE conversation_id=?", conversationID); err != nil {
						return err
					}
				}
				if err := replaceAgentSessions(tx, id, conversationID, conversation); err != nil {
					return err
				}
				if err := replaceToolLedger(tx, id, conversationID, conversation); err != nil {
					return err
				}
				if _, err := tx.Exec("DELETE FROM conversation_derivation_inputs WHERE conversation_id=?", conversationID); err != nil {
					return err
				}
				merged[conversationID] = true
			}
			if err := rederiveWorkspaceMode(tx, id, merged, true); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO workspace_library_dirty(workspace_id) VALUES(?)", id); err != nil {
				return err
			}
			_, err = tx.Exec("UPDATE sync_recovery_jobs SET cursor=?,updated_at=? WHERE id=?", id, now(), job)
			return err
		}); err != nil {
			return err
		}
		c.boundWAL(walSizeLimit)
		if progress != nil {
			progress(index+1, len(workspaces))
		}
	}
	c.refreshAuthorship()
	return nil
}
