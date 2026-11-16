package archive

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// Operation forecasts are separate from measured progress. Steps describe the
// requested work, including opaque preparation and saving, and may have unknown
// quantities. Dependencies compose concurrent paths using max, not a sum.
const forecastVersion = "operation-v1"

// forecastEstimator names the model that produced a stored snapshot. Step
// durations train every estimator, but whole-operation calibration reads only
// snapshots of the current one, so the intervals of an older model never
// calibrate this one. Rows keep their key, version and shape: executables
// sharing a library decode each other's history and ignore fields they do not
// know. An older executable drops those fields from the rows it rewrites,
// which costs calibration samples and nothing else.
const forecastEstimator = 2

type forecastStep struct {
	ID          string               `json:"id"`
	Model       string               `json:"model"`
	Label       string               `json:"label"`
	After       []string             `json:"after,omitempty"`
	Features    workFeatures         `json:"features"`
	Inventory   bool                 `json:"inventory_known"`
	State       string               `json:"state"` // pending, running, complete, skipped, failed
	Started     time.Time            `json:"-"`
	Seconds     float64              `json:"seconds"`
	Observation *progressObservation `json:"-"`
	Work        *activityWork        `json:"-"`
	// Units is a quantity a step declares for itself when its Features do not
	// carry one (the audit's eligible inputs). Absent means none was declared,
	// which is different from a declared zero.
	Units *float64 `json:"units,omitempty"`
	// What a step that failed or was interrupted while running had counted, in
	// WorkUnit. It is evidence for later fitting, not a completed duration.
	DoneUnits  float64 `json:"done_units,omitempty"`
	TotalUnits float64 `json:"total_units,omitempty"`
	WorkUnit   string  `json:"work_unit,omitempty"`
}

type operationEstimate struct {
	State string `json:"state"` // learning, partial, provisional, estimating, stopping, complete
	Basis string `json:"basis"`
	// Progress estimates elapsed / (elapsed + remaining) for the entire
	// operation. It is absent until discovery and every remaining step have
	// a forecast; measured stage progress remains in libraryActivity.Progress.
	Progress    *float64 `json:"progress,omitempty"`
	LowSeconds  float64  `json:"low_seconds,omitempty"`
	HighSeconds float64  `json:"high_seconds,omitempty"`
	Point       float64  `json:"point_seconds,omitempty"`
	Predicted   int      `json:"predicted_steps"`
	Remaining   int      `json:"remaining_steps"`
	Unknown     []string `json:"unknown_steps,omitempty"`
	Inventory   bool     `json:"inventory_known"`
	Modeled     int      `json:"feature_model_steps,omitempty"`
	CurrentStep int      `json:"current_step,omitempty"`
	TotalSteps  int      `json:"total_steps,omitempty"`
}

type forecastSnapshot struct {
	At       float64           `json:"elapsed_seconds"`
	Reason   string            `json:"reason"`
	Estimate operationEstimate `json:"estimate"`
	Actual   *float64          `json:"actual_remaining_seconds,omitempty"`
	// Estimator is absent on snapshots from before forecastEstimator existed.
	Estimator int `json:"estimator,omitempty"`
}

type forecastRun struct {
	Version    string             `json:"version"`
	Model      string             `json:"model"`
	FinishedAt string             `json:"finished_at"`
	RunID      string             `json:"run_id"`
	State      string             `json:"state"`
	Seconds    float64            `json:"seconds"`
	Steps      []forecastStep     `json:"steps"`
	Forecasts  []forecastSnapshot `json:"forecasts"`
}

type operationForecast struct {
	mu        sync.Mutex
	server    *Server
	model     string
	runID     string
	started   time.Time
	steps     []forecastStep
	history   []forecastRun // immutable, from completed runs before this run
	snapshots []forecastSnapshot
	closed    bool
	clock     func() time.Time
}

func (op *operationForecast) time() time.Time {
	if op.clock != nil {
		return op.clock()
	}
	return time.Now()
}

type operationForecastKey struct{}

type forecastStore struct {
	sync.Mutex
	history map[string][]forecastRun
}

func analysisForecastModel(name string) string {
	switch name {
	case "tools":
		// The tools stage's cost per unit changes with the derivation, not with
		// which conversations a scoped revision rebuilds (toolLedgerScopes): that
		// rebuild is the upgrade's step, which has no forecast here.
		return "analysis-v1:" + toolLedgerVersion + ":" + toolRollupVersion
	case "authorship":
		return "analysis-v1:" + authorshipVersion
	case "findings":
		return "analysis-v1:" + findingsVersion
	default:
		return "analysis-v1"
	}
}

// deferredAnalysisNames lists the stages finishDeferredStages runs, in its
// order, so a plan covers the whole chain instead of reporting "complete"
// while related conversations, labels or agent events are still running.
func (s *Server) deferredAnalysisNames(ctx context.Context) []string {
	names := []string{"github", "identities", "git", "tools", "authorship", "peers", "findings"}
	if s.Config().Labels.Enabled {
		names = append(names, "labels")
	}
	if s.Catalog != nil && s.agentEventsEnabled(ctx) {
		names = append(names, "agent_events")
	}
	if s.Config().Engagement.Enabled {
		names = append(names, "engagement")
	}
	return names
}

func addAnalysisForecast(op *operationForecast, forceFindings bool) {
	features := op.server.analysisForecastFeatures()
	for _, name := range op.server.deferredAnalysisNames(context.Background()) {
		f := op.server.analysisStepFeatures(context.Background(), name, features, forceFindings, false)
		// Before source discovery a currently reusable result can be invalidated
		// by incoming work. Don't classify it as a guaranteed cache hit yet.
		if f.Dirty != nil && !*f.Dirty {
			f.Dirty = nil
		}
		op.add("analysis:"+name, analysisForecastModel(name), "Refresh "+name, f, true)
	}
}

func (s *Server) analysisStepFeatures(ctx context.Context, name string, features workFeatures, forceFindings, willWrite bool) workFeatures {
	var dirty bool
	var err error
	switch name {
	case "tools":
		_, _, _, dirty, err = s.Catalog.toolRollupStale(ctx)
		dirty = dirty || willWrite
	case "authorship":
		var want, built string
		want, built, err = s.Catalog.authorshipGenerations()
		dirty = want != built || willWrite
	case "findings":
		var built string
		built, err = s.Catalog.metaValue(ctx, "findings_generation")
		dirty = forceFindings || built != s.Catalog.findingsGeneration()
	default:
		return features
	}
	if err == nil {
		features.Dirty = &dirty
	}
	return features
}

func (op *operationForecast) reviseAnalyses(ctx context.Context, forceFindings, willWrite bool) {
	if op == nil {
		return
	}
	features := op.server.analysisForecastFeatures()
	for _, name := range op.server.deferredAnalysisNames(ctx) {
		op.revise("analysis:"+name, "", op.server.analysisStepFeatures(ctx, name, features, forceFindings, willWrite), true)
	}
}

func (s *Server) analysisForecastFeatures() workFeatures {
	f := workFeatures{LoadOneMinute: machineLoad()}
	if s.Catalog != nil {
		// Messages already has a maintained count: never scan the transcript
		// table just to forecast a run. The other two are small covering scans.
		_ = s.Catalog.reader().QueryRow(`SELECT (SELECT COUNT(*) FROM workspaces), (SELECT COUNT(*) FROM conversations), COALESCE((SELECT value FROM catalog_counts WHERE name='messages'),0)`).Scan(&f.Groups, &f.Conversations, &f.Messages)
	}
	return f
}

func (p *indexPlan) forecastFeatures() workFeatures {
	f := workFeatures{LoadOneMinute: machineLoad()}
	mixed := false
	for _, plan := range p.plans {
		if plan == nil {
			continue
		}
		f.Groups += plan.features.Groups
		f.Files += plan.features.Files
		f.Messages += plan.features.Messages
		f.SinceLastIndexSeconds = max(f.SinceLastIndexSeconds, plan.features.SinceLastIndexSeconds)
		if plan.features.BytesBasis != "" {
			if f.BytesBasis != "" && f.BytesBasis != plan.features.BytesBasis {
				mixed = true
			}
			f.BytesBasis = plan.features.BytesBasis
			f.Bytes += plan.features.Bytes
		}
	}
	if mixed {
		f.Bytes, f.BytesBasis = 0, ""
	}
	return f
}

func withOperationForecast(ctx context.Context, op *operationForecast) context.Context {
	return context.WithValue(ctx, operationForecastKey{}, op)
}

func operationOf(ctx context.Context) *operationForecast {
	op, _ := ctx.Value(operationForecastKey{}).(*operationForecast)
	return op
}

// Add sequential steps explicitly; add() is also used for a discovered set of
// index targets. Stable IDs identify scope, while Model isolates versions and
// rebuild/update semantics. No filenames or conversation contents are stored.
func (op *operationForecast) add(id, model, label string, features workFeatures, inventory bool) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if slices.ContainsFunc(op.steps, func(s forecastStep) bool { return s.ID == id }) {
		return
	}
	after := []string(nil)
	if len(op.steps) > 0 {
		after = []string{op.steps[len(op.steps)-1].ID}
	}
	op.steps = append(op.steps, forecastStep{ID: id, Model: model, Label: label, After: after, Features: features, Inventory: inventory, State: "pending"})
}

// operationHistory is what one operation learns from: its own model's runs,
// and the steps other operations share with it. Runs of every outcome are
// kept, because the steps an interrupted or failed run did complete are
// measured durations. Shared runs carry no forecasts: stage evidence never
// supplies another operation's error ratios.
func operationHistory(model string, load func(string) []forecastRun) []forecastRun {
	history := slices.Clone(load(model))
	for _, peerModel := range []string{automaticRunKind, manualSyncRunKind, indexRunKind, "library-update:update", "library-update:full"} {
		if peerModel == model {
			continue
		}
		sharedIncremental := (model == automaticRunKind || model == manualSyncRunKind) && (peerModel == automaticRunKind || peerModel == manualSyncRunKind)
		for _, run := range load(peerModel) {
			steps := []forecastStep{}
			for _, step := range run.Steps {
				if sharedIncremental || strings.HasPrefix(step.ID, "analysis:") {
					steps = append(steps, step)
				}
			}
			history = append(history, forecastRun{Model: peerModel, FinishedAt: run.FinishedAt, State: run.State, Steps: steps})
		}
	}
	// Newest last across models: a step reads its most recent samples.
	slices.SortStableFunc(history, func(a, b forecastRun) int { return strings.Compare(a.FinishedAt, b.FinishedAt) })
	return history
}

func (s *Server) beginOperation(runID, model string) *operationForecast {
	op := &operationForecast{server: s, runID: runID, model: model, started: time.Now(), history: operationHistory(model, s.forecastHistory)}
	s.updateRun(runID, func(run *SyncRun) { run.operation = op })
	return op
}

func (op *operationForecast) revise(id, model string, features workFeatures, known bool) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	for i := range op.steps {
		if op.steps[i].ID == id {
			s := &op.steps[i]
			if model != "" {
				s.Model = model
			}
			s.Features, s.Inventory = features, known
			return
		}
	}
}

// declareUnits records a quantity a step learns only as it starts.
func (op *operationForecast) declareUnits(id string, units float64) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	for i := range op.steps {
		if op.steps[i].ID == id {
			op.steps[i].Units = &units
		}
	}
}

// settled reports that a step has no work left in this run. A failed step is
// not retried, so it is not remaining work either.
func (s forecastStep) settled() bool {
	return s.State == "complete" || s.State == "skipped" || s.State == "failed"
}

// stopRunning closes a running step that did not complete, keeping what it
// had counted.
func (s *forecastStep) stopRunning(at time.Time) {
	s.Seconds, s.State = at.Sub(s.Started).Seconds(), "failed"
	if s.Work != nil {
		if work := s.Work.measured(); work.Total > 0 {
			s.DoneUnits, s.TotalUnits, s.WorkUnit = work.Done, work.Total, work.Unit
		}
	}
}

func (op *operationForecast) begin(id string) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.closed {
		return
	}
	for i := range op.steps {
		if op.steps[i].ID == id {
			op.steps[i].State, op.steps[i].Started = "running", op.time()
		}
	}
	op.snapshotLocked("stage", op.time())
}

func (op *operationForecast) end(id string, success bool) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	for i := range op.steps {
		if s := &op.steps[i]; s.ID == id && s.State == "running" {
			s.stopRunning(op.time())
			if success {
				s.State, s.DoneUnits, s.TotalUnits, s.WorkUnit = "complete", 0, 0, ""
			}
			s.Work, s.Observation = nil, nil
		}
	}
}

func (op *operationForecast) skip(id string) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	for i := range op.steps {
		if op.steps[i].ID == id {
			op.steps[i].State = "skipped"
		}
	}
}

// Worker-driven snapshots make accuracy independent of whether a panel is open.
// This path does no catalog I/O, including when called inside a transaction.
func (op *operationForecast) observe(work *activityWork, at time.Time) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.closed || len(op.steps) == 0 {
		return
	}
	if work != nil {
		for i := range op.steps {
			s := &op.steps[i]
			if s.State != "running" {
				continue
			}
			if s.Work == nil || observationKey("", "", *s.Work) != observationKey("", "", *work) {
				s.Observation = &progressObservation{}
			}
			copy := cloneWork(*work)
			s.Work = &copy
			s.Observation.estimate(copy, at)
		}
	}
	if len(op.snapshots) == 0 || at.Sub(op.started).Seconds()-op.snapshots[len(op.snapshots)-1].At >= 15 {
		op.snapshotLocked("progress", at)
	}
}

func (op *operationForecast) checkpoint(reason string) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	op.snapshotLocked(reason, op.time())
}

func (op *operationForecast) snapshotLocked(reason string, at time.Time) {
	if op.closed {
		return
	}
	if len(op.snapshots) >= 48 {
		remove := -1
		for i, snapshot := range op.snapshots {
			if snapshot.Reason == "progress" {
				remove = i
				break
			}
		}
		if remove < 0 {
			for i, snapshot := range op.snapshots {
				if snapshot.Reason == "stage" {
					remove = i
					break
				}
			}
		}
		if remove < 0 {
			return
		}
		op.snapshots = slices.Delete(op.snapshots, remove, remove+1)
	}
	op.snapshots = append(op.snapshots, forecastSnapshot{At: max(0, at.Sub(op.started).Seconds()), Reason: reason, Estimate: op.estimateLocked(at, reason), Estimator: forecastEstimator})
}

func (op *operationForecast) estimate(at time.Time, stopping bool) *operationEstimate {
	if op == nil {
		return nil
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if stopping {
		current, total := op.stepPositionLocked()
		return &operationEstimate{State: "stopping", Basis: "Stopping after committed work is safe", CurrentStep: current, TotalSteps: total}
	}
	value := op.estimateLocked(at, "")
	if !value.Inventory && value.HighSeconds > forecastCheckingSpread*value.LowSeconds {
		// Before discovery a run is either nothing to do or real work, and a
		// range spanning both says nothing. Surfaces show the step being
		// checked and no time. Stored snapshots keep the numbers: they are
		// what later runs measure this checkpoint's error against.
		value.State, value.Basis = "learning", "Checking what has changed; the time depends on what is found"
		value.LowSeconds, value.HighSeconds, value.Point = 0, 0, 0
	}
	return &value
}

// forecastCheckingSpread is how many times its low a range's high may be,
// while the inventory is unknown, and still be shown.
const forecastCheckingSpread = 10

// Use the planned order, including skipped steps, so numbering stays stable
// as cached work is skipped. A step can contain several measured substeps.
func (op *operationForecast) stepPositionLocked() (current, total int) {
	total = len(op.steps)
	for i, step := range op.steps {
		if step.State == "running" {
			return i + 1, total
		}
		if current == 0 && !step.settled() {
			current = i + 1
		}
	}
	if current == 0 {
		current = total
	}
	return current, total
}

func (op *operationForecast) estimateLocked(at time.Time, checkpoint string) operationEstimate {
	result := operationEstimate{State: "learning", Inventory: true, Basis: "Learning durations for the remaining sources and analyses on this Mac"}
	result.CurrentStep, result.TotalSteps = op.stepPositionLocked()
	paths := map[string][3]float64{}
	provisional := false
	for _, step := range op.steps {
		path := [3]float64{}
		for _, dependency := range step.After {
			parent := paths[dependency]
			for j := range path {
				path[j] = max(path[j], parent[j])
			}
		}
		if !step.settled() {
			result.Remaining++
			result.Inventory = result.Inventory && step.Inventory
			prediction := predictStep(op.history, step)
			if prediction.point == 0 && strings.HasPrefix(step.ID, "source:") && step.State == "running" && step.Work != nil && step.Work.Phase == "indexing" && step.Observation != nil {
				live := step.Observation.estimate(*step.Work, at)
				if live.State == "estimating" {
					elapsed := max(0, at.Sub(step.Started).Seconds())
					prediction = stepPrediction{point: elapsed + (live.LowSeconds+live.HighSeconds)/2, low: elapsed + live.LowSeconds, high: elapsed + live.HighSeconds*2, provisional: true}
				}
			}
			if prediction.point == 0 {
				result.Unknown = append(result.Unknown, step.Label)
			} else {
				result.Predicted++
				if prediction.fitted {
					result.Modeled++
				}
				provisional = provisional || prediction.samples < 5 || prediction.provisional || !step.Inventory
				elapsed := 0.0
				low, high, point := prediction.low, prediction.high, prediction.point
				if step.State == "running" {
					elapsed = max(0, at.Sub(step.Started).Seconds())
					point -= elapsed
					// A slow/blocked item must never count down through zero: past
					// its expected time, expect half as long again as it has taken.
					if point <= 0 {
						point = elapsed / 2
						provisional = true
					}
					low, high = max(0, low-elapsed), max(high-elapsed, 2*point)
				}
				// A source's indexing stage supplies live pace. Retain a historical
				// tail for saving/retained generations instead of claiming its last
				// counted item completes the entire source.
				if strings.HasPrefix(step.ID, "source:") && step.Work != nil && step.Work.Phase == "indexing" && step.Observation != nil {
					live := step.Observation.estimate(*step.Work, at)
					if live.State == "estimating" {
						tail := prediction.point * .1
						low, high, point = live.LowSeconds, live.HighSeconds+tail*2, (live.LowSeconds+live.HighSeconds)/2+tail
						if prediction.samples == 0 {
							low /= 2
							high = live.HighSeconds*2 + tail*2
						}
					} else if live.State == "stalled" {
						provisional = true
						high = max(high, elapsed)
					}
				}
				path[0] += low
				path[1] += high
				path[2] += point
			}
		}
		paths[step.ID] = path
		result.LowSeconds = max(result.LowSeconds, path[0])
		result.HighSeconds = max(result.HighSeconds, path[1])
		result.Point = max(result.Point, path[2])
	}
	if result.Remaining == 0 {
		result.State, result.Basis = "complete", "Planned work finished"
	} else if len(result.Unknown) > 0 {
		if result.Predicted > 0 {
			result.State = "partial"
		}
		result.Basis = "Some remaining steps have no comparable history yet"
		// Partial paths are not a whole-operation countdown.
		result.LowSeconds, result.HighSeconds, result.Point = 0, 0, 0
	} else {
		result.State, result.Basis = "estimating", "History of remaining sources and analyses on this Mac; updates with recent pace"
		if result.Modeled > 0 {
			result.Basis = "Workload models for remaining work; updates with recent pace"
		}
		if provisional {
			result.State, result.Basis = "provisional", "Broad forecast; discovery or comparable history is still limited"
		}
		// The interval comes from how PREVIOUS runs of this operation ended
		// relative to their forecast at the same checkpoint, so it narrows as
		// well as widens. Ratios are prequential: a run never calibrates its
		// own predictions. Summed step ranges bound it only until then.
		result.LowSeconds, result.HighSeconds = forecastInterval(result.Point, result.LowSeconds, result.HighSeconds,
			calibrationRatios(op.history, op.calibrationReasonLocked(at, checkpoint)))
		result.LowSeconds = max(.001, result.LowSeconds)
		result.HighSeconds = max(result.LowSeconds, result.HighSeconds)
	}
	if result.State == "complete" {
		result.Progress = fraction(1, 1)
	} else if result.Inventory && finitePositive(result.Point) && !op.started.IsZero() {
		elapsed := max(0, at.Sub(op.started).Seconds())
		result.Progress = fraction(elapsed, elapsed+result.Point)
	}
	return result
}

// calibrationReasonLocked names the checkpoint whose past errors describe an
// estimate made now: before discovery, at it, or during the work after it.
func (op *operationForecast) calibrationReasonLocked(at time.Time, checkpoint string) string {
	if checkpoint == "launch" || checkpoint == "discovered" {
		return checkpoint
	}
	for _, snapshot := range op.snapshots {
		if snapshot.Reason == "discovered" {
			return "progress"
		}
	}
	if at.Sub(op.started).Seconds() < 15 {
		return "launch"
	}
	for _, step := range op.steps {
		if step.ID == "discovery" && (step.State == "pending" || step.State == "running") {
			return "launch"
		}
	}
	return "progress"
}

// calibrationRatios returns ln(actual remaining / forecast point) of completed
// runs, oldest first. Launch and discovery give one ratio a run. Progress gives
// one for each quarter of a run, from the first snapshot at or past it, so
// neither a long run nor a frequently polled one outvotes the others.
func calibrationRatios(history []forecastRun, reason string) []float64 {
	ratios := []float64{}
	for _, run := range history {
		if run.State != "complete" {
			continue
		}
		quarter := 1
		for _, snapshot := range run.Forecasts {
			if snapshot.Estimator != forecastEstimator || snapshot.Actual == nil || !finitePositive(snapshot.Estimate.Point) {
				continue
			}
			ratio := math.Log(max(.5, *snapshot.Actual) / snapshot.Estimate.Point)
			if reason != "progress" {
				if snapshot.Reason == reason {
					ratios = append(ratios, ratio)
					break
				}
				continue
			}
			for ; quarter <= 3 && run.Seconds > 0 && snapshot.At >= float64(quarter)/4*run.Seconds; quarter++ {
				ratios = append(ratios, ratio)
			}
		}
	}
	return ratios
}

// forecastInterval aims at about 80% coverage. Fewer than 20 past outcomes use
// wider quantiles, and fewer than 8 only a factor of two around the point,
// extended by the summed step ranges.
func forecastInterval(point, low, high float64, ratios []float64) (float64, float64) {
	if len(ratios) < 8 {
		return min(low, point/2), max(high, point*2)
	}
	ratios = slices.Clone(ratios[max(0, len(ratios)-forecastSamples):])
	slices.Sort(ratios)
	lower, upper := .05, .95
	if len(ratios) >= 20 {
		lower, upper = .1, .9
	}
	return point * math.Exp(quantile(ratios, lower)), point * math.Exp(quantile(ratios, upper))
}

func (op *operationForecast) finish(state string) {
	if op == nil {
		return
	}
	op.mu.Lock()
	if op.closed {
		op.mu.Unlock()
		return
	}
	op.closed = true
	at := op.time()
	record := forecastRun{Version: forecastVersion, Model: op.model, FinishedAt: formatTime(at), RunID: op.runID, State: state, Seconds: at.Sub(op.started).Seconds(), Forecasts: op.snapshots}
	for _, step := range op.steps {
		if step.State == "running" {
			step.stopRunning(at)
		}
		step.Observation, step.Work = nil, nil
		record.Steps = append(record.Steps, step)
	}
	for i := range record.Forecasts {
		if state == "complete" {
			actual := max(0, record.Seconds-record.Forecasts[i].At)
			record.Forecasts[i].Actual = &actual
		}
	}
	op.mu.Unlock()
	if op.server.Catalog == nil {
		return
	}
	op.server.timingMu.Lock()
	defer op.server.timingMu.Unlock()
	history := append(slices.Clone(op.server.forecastHistory(op.model)), record)
	if len(history) > 80 {
		history = history[len(history)-80:]
	}
	if data, err := json.Marshal(history); err == nil {
		key := activityTimingKey(forecastVersion + ":" + op.model)
		if setMeta(op.server.Catalog.DB, key, string(data)) == nil {
			op.server.forecasts.Lock()
			op.server.forecasts.history[key] = history
			op.server.forecasts.Unlock()
		}
	}
}

func (s *Server) forecastHistory(model string) []forecastRun {
	s.forecasts.Lock()
	defer s.forecasts.Unlock()
	key := activityTimingKey(forecastVersion + ":" + model)
	if s.forecasts.history == nil {
		s.forecasts.history = map[string][]forecastRun{}
	}
	if history, ok := s.forecasts.history[key]; ok {
		return history
	}
	var value string
	var history []forecastRun
	if s.Catalog != nil && s.Catalog.reader().QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&value) == nil {
		_ = json.Unmarshal([]byte(value), &history)
	}
	s.forecasts.history[key] = history
	return history
}

func (s *Server) forecastReports() (map[string]any, []forecastRun) {
	accuracy := map[string]any{}
	recent := []forecastRun{}
	for _, model := range []string{automaticRunKind, manualSyncRunKind, indexRunKind, "library-update:update", "library-update:full", "library-update:retained"} {
		history := s.forecastHistory(model)
		if len(history) == 0 {
			continue
		}
		accuracy[model] = forecastAccuracy(history)
		for _, run := range history[max(0, len(history)-10):] {
			// The poll response needs checkpoint evidence, not every live sample.
			// Keep full telemetry in bounded local history for later fitting.
			checkpoints := []forecastSnapshot{}
			seen := map[string]bool{}
			for _, snapshot := range run.Forecasts {
				if (snapshot.Reason == "launch" || snapshot.Reason == "discovered" || snapshot.Reason == "progress") && !seen[snapshot.Reason] {
					checkpoints = append(checkpoints, snapshot)
					seen[snapshot.Reason] = true
				}
			}
			run.Forecasts, run.Steps = checkpoints, nil
			recent = append(recent, run)
		}
	}
	slices.SortFunc(recent, func(a, b forecastRun) int { return strings.Compare(b.FinishedAt, a.FinishedAt) })
	if len(recent) > 30 {
		recent = recent[:30]
	}
	return accuracy, recent
}

type stepPrediction struct {
	fitted           bool
	point, low, high float64
	samples          int
	provisional      bool
}

// forecastSamples bounds what one fit or calibration reads: recent runs
// describe this library and this code better than old ones.
const forecastSamples = 40

// quantile interpolates linearly in sorted values.
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	position := p * float64(len(sorted)-1)
	low := int(position)
	high := min(len(sorted)-1, low+1)
	return sorted[low] + (sorted[high]-sorted[low])*(position-float64(low))
}

// stepQuantity returns how a target and its peers are measured, or nil when
// the target has no known quantity. Incremental sources are measured in
// conversation groups: re-deriving a conversation costs by what is stored for
// it, not by the bytes appended, and file sizes are whole-file lengths. A full
// index keeps its volume measure, and never mixes file bytes with decoded
// database text.
func stepQuantity(target forecastStep) func(forecastStep) (float64, bool) {
	switch {
	case target.Units != nil:
		return func(s forecastStep) (float64, bool) {
			if s.Units == nil {
				return 0, false
			}
			return *s.Units, true
		}
	case !target.Inventory:
		return nil
	case strings.HasPrefix(target.ID, "source:"):
		return func(s forecastStep) (float64, bool) { return s.Features.Groups, s.Inventory }
	case target.ID != "index":
		return nil
	case target.Features.Bytes > 0:
		return func(s forecastStep) (float64, bool) {
			return s.Features.Bytes, s.Inventory && s.Features.BytesBasis == target.Features.BytesBasis
		}
	case target.Features.Messages > 0:
		return func(s forecastStep) (float64, bool) { return s.Features.Messages, s.Inventory }
	default:
		return func(s forecastStep) (float64, bool) { return s.Features.Groups, s.Inventory }
	}
}

// theilSen fits seconds = a + b·units robustly: the median pairwise slope,
// then the median intercept, neither below zero. Equal units give a = median.
func theilSen(units, seconds []float64) (a, b float64) {
	slopes := []float64{}
	for i := range units {
		for j := i + 1; j < len(units); j++ {
			if units[i] != units[j] {
				slopes = append(slopes, (seconds[j]-seconds[i])/(units[j]-units[i]))
			}
		}
	}
	slices.Sort(slopes)
	b = max(0, quantile(slopes, .5))
	intercepts := make([]float64, len(units))
	for i := range units {
		intercepts[i] = seconds[i] - b*units[i]
	}
	slices.Sort(intercepts)
	return max(0, quantile(intercepts, .5)), b
}

// predictStep estimates one step from completed steps with its ID, whatever
// became of the run they were in: an interrupted run's finished steps are
// measured durations.
//
// A step key's version string changes cost, so samples of the target's model
// are used alone as soon as one exists. Until then the newest samples of other
// versions stand in, marked provisional, instead of leaving the step (and with
// it the whole operation) without a forecast.
//
// A step with a quantity is a fixed cost plus a cost per unit, and its range
// is the spread of its peers around that fit. A known empty step reads only
// empty peers, never a scaled-down real one, and the reverse.
func predictStep(history []forecastRun, target forecastStep) stepPrediction {
	quantity := stepQuantity(target)
	want, known := 0.0, false
	if quantity != nil {
		want, known = quantity(target)
	}
	// Newest first, and only the most recent forecastSamples of each list: what
	// a step cost lately says more than a longer record does. Then the peers
	// comparable with the target, with their units, or those with no quantity.
	type peers struct {
		read                  int
		seconds, units, loose []float64
	}
	var same, other peers
	for i := len(history) - 1; i >= 0; i-- {
		for _, step := range history[i].Steps {
			if step.ID != target.ID || step.State != "complete" || !finitePositive(step.Seconds) {
				continue
			}
			if target.Features.Dirty != nil && step.Features.Dirty != nil && *target.Features.Dirty != *step.Features.Dirty {
				continue
			}
			list := &other
			if target.Model == "" || step.Model == target.Model {
				list = &same
			}
			if list.read++; list.read > forecastSamples {
				continue
			}
			if !known {
				list.seconds = append(list.seconds, step.Seconds)
			} else if had, ok := quantity(step); !ok {
				list.loose = append(list.loose, step.Seconds)
			} else if (want == 0) == (had == 0) {
				list.seconds, list.units = append(list.seconds, step.Seconds), append(list.units, had)
			}
		}
	}
	list, provisional := same, false
	if len(same.seconds)+len(same.loose) == 0 {
		list, provisional = other, true
	}
	if known && len(list.seconds) == 0 {
		// Peers recorded before this step declared a quantity.
		list.seconds, known, provisional = list.loose, false, true
	}
	seconds := list.seconds
	if len(seconds) == 0 {
		return stepPrediction{}
	}
	sorted := slices.Sorted(slices.Values(seconds))
	result := stepPrediction{point: quantile(sorted, .5), samples: len(seconds), provisional: provisional}
	result.low, result.high = result.point/3, result.point*3
	if len(seconds) < 3 {
		result.provisional = true
		return result
	}
	if !known || want == 0 {
		result.low, result.high = quantile(sorted, .1), quantile(sorted, .9)
		return result
	}
	units := list.units
	a, b := theilSen(units, seconds)
	fit := func(units float64) float64 { return max(.05, a+b*units) }
	residuals := make([]float64, len(seconds))
	for i := range seconds {
		residuals[i] = math.Log(max(.01, seconds[i]) / fit(units[i]))
	}
	slices.Sort(residuals)
	result.point, result.fitted = fit(want), b > 0
	result.low, result.high = result.point*math.Exp(quantile(residuals, .1)), result.point*math.Exp(quantile(residuals, .9))
	if want > slices.Max(units)*4 || want < slices.Min(units)/4 {
		// Outside what was measured: keep the fit, widen and say so.
		result.provisional = true
		result.low, result.high = min(result.low, result.point/3), max(result.high, result.point*3)
	}
	return result
}

// Metrics include unavailable predictions, and exclude censored/interrupted
// outcomes from error and interval coverage. Each checkpoint has at most one
// observation per run, so long operations cannot dominate calibration.
func forecastAccuracy(history []forecastRun) map[string]any {
	result := map[string]any{}
	for _, reason := range []string{"launch", "discovered", "progress"} {
		attempts, predicted, evaluated, covered := 0, 0, 0, 0
		absolute, relative, width := []float64{}, []float64{}, []float64{}
		for _, run := range history {
			for _, snapshot := range run.Forecasts {
				// Another estimator's snapshots say nothing about this one.
				if snapshot.Reason != reason || snapshot.Estimator != forecastEstimator {
					continue
				}
				attempts++
				e := snapshot.Estimate
				if e.Point > 0 {
					predicted++
					if snapshot.Actual != nil && run.State == "complete" {
						evaluated++
						actual := *snapshot.Actual
						if actual >= e.LowSeconds && actual <= e.HighSeconds {
							covered++
						}
						absolute = append(absolute, math.Abs(e.Point-actual))
						relative = append(relative, math.Abs(e.Point-actual)/max(1, actual))
						width = append(width, e.HighSeconds-e.LowSeconds)
					}
				}
				break
			}
		}
		median := func(values []float64) any {
			if len(values) == 0 {
				return nil
			}
			slices.Sort(values)
			return values[len(values)/2]
		}
		result[reason] = map[string]any{"attempts": attempts, "predicted": predicted, "evaluated": evaluated, "covered": covered, "median_absolute_error_seconds": median(absolute), "median_relative_error": median(relative), "median_interval_width_seconds": median(width)}
	}
	return result
}
