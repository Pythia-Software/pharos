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
		return "analysis-v1:" + toolLedgerVersion + ":" + toolRollupVersion
	case "authorship":
		return "analysis-v1:" + authorshipVersion
	case "findings":
		return "analysis-v1:" + findingsVersion
	default:
		return "analysis-v1"
	}
}

func addAnalysisForecast(op *operationForecast, forceFindings bool) {
	features := op.server.analysisForecastFeatures()
	for _, name := range []string{"github", "identities", "git", "tools", "authorship", "findings"} {
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
	for _, name := range []string{"github", "identities", "git", "tools", "authorship", "findings"} {
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

func (s *Server) beginOperation(runID, model string) *operationForecast {
	history := slices.Clone(s.forecastHistory(model))
	for _, peerModel := range []string{automaticRunKind, manualSyncRunKind, indexRunKind, "library-update:update", "library-update:full"} {
		if peerModel == model {
			continue
		}
		for _, run := range s.forecastHistory(peerModel) {
			if run.State != "complete" {
				continue
			}
			steps := []forecastStep{}
			for _, step := range run.Steps {
				sharedIncremental := (model == automaticRunKind || model == manualSyncRunKind) && (peerModel == automaticRunKind || peerModel == manualSyncRunKind)
				if sharedIncremental || strings.HasPrefix(step.ID, "analysis:") {
					steps = append(steps, step)
				}
			}
			// Shared stage evidence never supplies another operation's error ratios.
			history = append(history, forecastRun{State: "complete", Steps: steps})
		}
	}
	op := &operationForecast{server: s, runID: runID, model: model, started: time.Now(), history: history}
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
			s.Seconds = op.time().Sub(s.Started).Seconds()
			s.State = "failed"
			if success {
				s.State = "complete"
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
	op.snapshots = append(op.snapshots, forecastSnapshot{At: max(0, at.Sub(op.started).Seconds()), Reason: reason, Estimate: op.estimateLocked(at, reason)})
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
	return &value
}

// Use the planned order, including skipped steps, so numbering stays stable
// as cached work is skipped. A step can contain several measured substeps.
func (op *operationForecast) stepPositionLocked() (current, total int) {
	total = len(op.steps)
	for i, step := range op.steps {
		if step.State == "running" {
			return i + 1, total
		}
		if current == 0 && step.State != "complete" && step.State != "skipped" {
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
		if step.State != "complete" && step.State != "skipped" {
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
				if step.State == "running" {
					elapsed = max(0, at.Sub(step.Started).Seconds())
				}
				low, high, point := max(0, prediction.low-elapsed), max(0, prediction.high-elapsed), max(0, prediction.point-elapsed)
				// A slow/blocked item must never count down through zero. Retain a
				// residual-life forecast and widen after the expected time passes.
				if elapsed >= prediction.point {
					point = max(prediction.point/2, (elapsed-prediction.point)/2)
					high = max(high, 3*point)
					provisional = true
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
			result.Basis = "Workload and machine-load models for remaining work; updates with recent pace"
		}
		if provisional {
			result.State, result.Basis = "provisional", "Broad forecast; discovery or comparable history is still limited"
		}
		// Calibrate with PREVIOUS whole-operation outcomes at the same checkpoint.
		// Ratios are prequential: a run never calibrates its own predictions.
		ratios := []float64{}
		reason := checkpoint
		if reason == "" || reason == "stage" {
			reason = "progress"
			elapsed := at.Sub(op.started).Seconds()
			if elapsed < 15 {
				reason = "launch"
			}
			for _, snapshot := range op.snapshots {
				if snapshot.Reason == "discovered" && elapsed-snapshot.At < 15 {
					reason = "discovered"
					break
				}
			}
		}
		for _, run := range op.history {
			if run.State != "complete" {
				continue
			}
			for _, snapshot := range run.Forecasts {
				if snapshot.Reason == reason && snapshot.Actual != nil && snapshot.Estimate.Point > 0 {
					ratios = append(ratios, *snapshot.Actual/snapshot.Estimate.Point)
					break // one observation per run, not one vote per poll
				}
			}
		}
		if len(ratios) >= 8 {
			slices.Sort(ratios)
			result.LowSeconds = min(result.LowSeconds, result.Point*ratios[(len(ratios)-1)/10])
			result.HighSeconds = max(result.HighSeconds, result.Point*ratios[(len(ratios)-1)*9/10])
		}
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
			step.State, step.Seconds = "failed", at.Sub(step.Started).Seconds()
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

// Log-space ridge regression bounds sparse/correlated features. Workload is
// useful immediately as a throughput prior; multivariable weights require at
// least 20 complete samples. Missing/different byte bases never share weights.
func predictStep(history []forecastRun, target forecastStep) stepPrediction {
	var samples []forecastStep
	for _, run := range history {
		if run.State != "complete" {
			continue
		}
		for _, step := range run.Steps {
			if target.Features.Dirty != nil && step.Features.Dirty != nil && *target.Features.Dirty != *step.Features.Dirty {
				continue
			}
			if step.ID == target.ID && (target.Model == "" || step.Model == target.Model) && step.State == "complete" && finitePositive(step.Seconds) && (!target.Inventory || target.Features.BytesBasis == "" || step.Features.BytesBasis == target.Features.BytesBasis) {
				samples = append(samples, step)
			}
		}
	}
	if len(samples) == 0 {
		return stepPrediction{}
	}
	// Quantity known at prediction time also selects the fallback. A known
	// empty inventory uses only empty peers instead of scaling a rebuild to 0.
	quantity := func(s forecastStep) float64 {
		switch {
		case target.Inventory && target.Features.Bytes > 0:
			return s.Features.Bytes
		case target.Inventory && target.Features.Messages > 0:
			return s.Features.Messages
		case target.Inventory:
			return s.Features.Groups
		default:
			return 0
		}
	}
	values := []float64{}
	peers := []forecastStep{}
	minQuantity, maxQuantity := math.Inf(1), 0.0
	for _, sample := range samples {
		value := sample.Seconds
		if target.Inventory && sample.Inventory {
			want, had := quantity(target), quantity(sample)
			if (want == 0) != (had == 0) {
				continue
			}
			if want > 0 && had > 0 {
				// Retain fixed overhead; don't assume all duration is byte-linear.
				value *= .2 + .8*(want/had)
				minQuantity, maxQuantity = min(minQuantity, had), max(maxQuantity, had)
			}
		}
		if !finitePositive(value) {
			continue
		}
		peers = append(peers, sample)
		values = append(values, value)
	}
	samples = peers
	extrapolating := maxQuantity > 0 && (quantity(target) > maxQuantity*4 || quantity(target) < minQuantity/4)
	if len(values) == 0 {
		return stepPrediction{}
	}
	slices.Sort(values)
	point := values[len(values)/2]
	spread := math.Log(3.0)
	if len(values) >= 5 {
		spread = max(math.Log(1.5), math.Log(max(values[(len(values)-1)*9/10], .001)/max(values[(len(values)-1)/10], .001)))
	}
	if extrapolating {
		spread = max(spread, math.Log(4))
	}
	fittedModel := false
	if len(samples) >= 20 && !extrapolating {
		if fitted, errorSpread, ok := ridgeDuration(samples, target); ok {
			point, spread, fittedModel = fitted, max(spread/2, errorSpread), true
		}
	}
	return stepPrediction{point: point, low: point / math.Exp(spread), high: point * math.Exp(spread), samples: len(values), provisional: extrapolating, fitted: fittedModel}
}

func durationVector(f workFeatures) []float64 {
	load := 0.0
	if f.LoadOneMinute != nil {
		load = *f.LoadOneMinute
	}
	values := []float64{f.Groups, f.Bytes, f.Messages, f.Conversations, f.Files, f.SinceLastIndexSeconds, load}
	for i, v := range values {
		values[i] = math.Log1p(max(0, v))
	}
	return values
}

func ridgeDuration(samples []forecastStep, target forecastStep) (float64, float64, bool) {
	// Only fit columns observed on the target and most peers; zero/missing
	// bytes, load, or conversations cannot silently become a useful signal.
	want := durationVector(target.Features)
	means, scales := make([]float64, len(want)), make([]float64, len(want))
	rows := make([][]float64, len(samples))
	for i, sample := range samples {
		rows[i] = durationVector(sample.Features)
		for j, v := range rows[i] {
			means[j] += v / float64(len(samples))
		}
	}
	columns := []int{}
	for j := range want {
		present := 0
		for i, row := range rows {
			scales[j] += math.Pow(row[j]-means[j], 2) / float64(len(rows))
			if row[j] > 0 || j == 6 && samples[i].Features.LoadOneMinute != nil {
				present++
			}
		}
		scales[j] = math.Sqrt(scales[j])
		if (want[j] > 0 || j == 6 && target.Features.LoadOneMinute != nil) && scales[j] > .05 && present*5 >= len(rows)*4 {
			columns = append(columns, j)
		}
	}
	if len(columns) == 0 {
		return 0, 0, false
	}
	vector := func(row []float64) []float64 {
		x := []float64{1}
		for _, j := range columns {
			x = append(x, min(4, max(-4, (row[j]-means[j])/scales[j])))
		}
		return x
	}
	n := len(columns) + 1
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, n+1)
		if i > 0 {
			a[i][i] = 4
		}
	}
	yMean := 0.0
	for i, row := range rows {
		x, y := vector(row), math.Log(samples[i].Seconds)
		yMean += y / float64(len(rows))
		for j := range x {
			for k := range x {
				a[j][k] += x[j] * x[k]
			}
			a[j][n] += x[j] * y
		}
	}
	for i := 0; i < n; i++ {
		pivot := i
		for j := i + 1; j < n; j++ {
			if math.Abs(a[j][i]) > math.Abs(a[pivot][i]) {
				pivot = j
			}
		}
		a[i], a[pivot] = a[pivot], a[i]
		if math.Abs(a[i][i]) < 1e-9 {
			return 0, 0, false
		}
		divisor := a[i][i]
		for k := i; k <= n; k++ {
			a[i][k] /= divisor
		}
		for j := 0; j < n; j++ {
			if j != i {
				factor := a[j][i]
				for k := i; k <= n; k++ {
					a[j][k] -= factor * a[i][k]
				}
			}
		}
	}
	predict := func(row []float64) float64 {
		y := 0.0
		for j, x := range vector(row) {
			y += x * a[j][n]
		}
		return y
	}
	sse, baseline := 0.0, 0.0
	residuals := []float64{}
	for i, row := range rows {
		y := math.Log(samples[i].Seconds)
		error := y - predict(row)
		sse += error * error
		baseline += math.Pow(y-yMean, 2)
		residuals = append(residuals, math.Abs(error))
	}
	if sse >= baseline*.9 {
		return 0, 0, false
	}
	slices.Sort(residuals)
	point := math.Exp(predict(want))
	return point, max(math.Log(1.5), residuals[(len(residuals)-1)*9/10]*1.5), finitePositive(point)
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
				if snapshot.Reason != reason {
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
