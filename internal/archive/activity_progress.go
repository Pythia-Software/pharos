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

// Work describes one measured stage, not an invented fraction of the whole job.
// A nonpositive total is unknown unless TotalKnown explicitly marks an empty
// workload. Never extrapolate a source count:
// one source can contain a handful of sessions and another several thousand.
type activityWork struct {
	Phase      string        `json:"phase"`
	Source     string        `json:"source,omitempty"`
	Unit       string        `json:"unit,omitempty"`
	Done       float64       `json:"done"`
	Total      float64       `json:"total"`
	TotalKnown bool          `json:"total_known,omitempty"`
	Features   workFeatures  `json:"features,omitempty"`
	StartedAt  string        `json:"started_at,omitempty"`
	Next       string        `json:"next,omitempty"`
	Size       *activitySize `json:"size,omitempty"`
	// Model separates providers and extractor versions, without exposing paths.
	Model string `json:"-"`
	Kind  string `json:"-"`
}

// Transcript bytes and database message counts are different measures. A
// source mixing them falls back to groups rather than adding unlike units.
type activitySize struct {
	Unit  string  `json:"unit"`
	Done  float64 `json:"done"`
	Total float64 `json:"total"`
}

func (w activityWork) measured() activityWork {
	if w.Size != nil && w.Size.Total > 0 {
		w.Unit, w.Done, w.Total = w.Size.Unit, w.Size.Done, w.Size.Total
	}
	return w
}

func partWorkSize(parts []sourcePart) (float64, string) {
	var size float64
	unit := ""
	for _, part := range parts {
		if strings.HasPrefix(part.item, "group:") || strings.HasPrefix(part.item, "sqlite:") && part.sizeUnit != "message" {
			continue
		}
		next := defaultString(part.sizeUnit, "byte")
		if part.size < 0 || (unit != "" && unit != next) {
			return 0, ""
		}
		unit = next
		size += float64(part.size)
	}
	return size, unit
}

func (r *SyncRun) setWork(phase, source, unit string, done, total float64) {
	started := now()
	if r.Work != nil && r.Work.Phase == phase && r.Work.Source == source && r.Work.Unit == unit && done >= r.Work.Done {
		started = r.Work.StartedAt
	}
	r.Work = &activityWork{Phase: phase, Source: source, Unit: unit, Done: done, Total: total, StartedAt: started}
}

type activityEstimate struct {
	State       string  `json:"state"` // learning, estimating, unknown, stalled, finishing, stopping
	Basis       string  `json:"basis"`
	LowSeconds  float64 `json:"low_seconds,omitempty"`
	HighSeconds float64 `json:"high_seconds,omitempty"`
	Rate        float64 `json:"units_per_second,omitempty"`
	Samples     int     `json:"samples,omitempty"`
	Unit        string  `json:"unit,omitempty"`
}

type workTiming struct {
	Units    float64      `json:"units"`
	Seconds  float64      `json:"seconds"`
	Features workFeatures `json:"features,omitempty"`
}

func workTimingKey(kind string, work activityWork) string {
	work = work.measured()
	return activityTimingKey("work-v1:" + kind + ":" + work.Model + ":" + work.Phase + ":" + work.Unit)
}

func activityTimingKey(kind string) string {
	return "activity_durations:" + currentHost().ID + ":" + kind
}

func activityTimingKind(runKind string) string {
	switch runKind {
	case "source-sync":
		return "sync"
	case indexRunKind:
		return "index"
	default:
		return runKind
	}
}

// Successful, measured stages only. Old whole-run durations deliberately do
// not seed this model: they have no workload, phase, or extractor information.
func (s *Server) recordWorkTiming(kind string, work activityWork, completed time.Time) {
	work = work.measured()
	started, err := time.Parse(time.RFC3339Nano, work.StartedAt)
	seconds := completed.Sub(started).Seconds()
	if err != nil || seconds < 1 || work.Total <= 0 || work.Done < work.Total || s.Catalog == nil {
		return
	}
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	key := workTimingKey(kind, work)
	history := append(s.workTimings(key), workTiming{Units: work.Done, Seconds: seconds, Features: work.Features})
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	if data, err := json.Marshal(history); err == nil {
		_ = setMeta(s.Catalog.DB, key, string(data))
	}
}

func (s *Server) workTimings(key string) []workTiming {
	var value string
	var history []workTiming
	if s.Catalog != nil && s.Catalog.reader().QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&value) == nil {
		if json.Unmarshal([]byte(value), &history) != nil {
			return nil
		}
	}
	return slices.DeleteFunc(history, func(item workTiming) bool {
		return !finitePositive(item.Units) || !finitePositive(item.Seconds)
	})
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

type progressSample struct {
	at   time.Time
	done float64
}

type progressObservation struct {
	samples []progressSample
	changed time.Time
	total   float64
	history []workTiming
}

type activityEstimator struct {
	mu           sync.Mutex
	observations map[string]*progressObservation
}

// Called with a clock argument so pauses, changing workloads and phase changes
// can be tested without sleeps. Sampling starts when observed, not at the job's
// start: discovery, earlier sources and a resumed backlog must not dilute rate.
func (o *progressObservation) estimate(work activityWork, at time.Time) activityEstimate {
	work = work.measured()
	result := activityEstimate{State: "learning", Basis: "Measuring the pace of this stage", Unit: work.Unit}
	if len(o.samples) == 0 || work.Done < o.samples[len(o.samples)-1].done || work.Total != o.total {
		o.samples = []progressSample{{at, work.Done}}
		o.changed, o.total = at, work.Total
	}
	last := o.samples[len(o.samples)-1]
	if work.Done > last.done {
		o.changed = at
		if at.Sub(last.at) >= time.Second {
			o.samples = append(o.samples, progressSample{at, work.Done})
		}
	}
	// Keep at most one minute of observations; retaining the boundary sample
	// makes both polling gaps and time spent in a large current unit count.
	for len(o.samples) > 2 && at.Sub(o.samples[1].at) > time.Minute {
		o.samples = o.samples[1:]
	}
	first := o.samples[0]
	span := at.Sub(first.at).Seconds()
	delta := work.Done - first.done
	live := span >= 8 && len(o.samples) >= 3 && delta > 0
	rates := []float64{}
	for _, item := range o.history {
		// Do not extrapolate a tiny warm-cache update to an enormous rebuild.
		if work.Total >= item.Units/4 && work.Total <= item.Units*4 {
			rates = append(rates, item.Units/item.Seconds)
		}
	}
	slices.Sort(rates)
	rate, uncertainty := float64(0), 0.5
	if len(rates) >= 3 {
		rate = rates[len(rates)/2]
		result.Basis = "Size-adjusted history for this stage on this Mac"
		result.Samples = len(rates)
		uncertainty = min(2, max(0.5, (rates[(len(rates)-1)*4/5]-rates[(len(rates)-1)/5])/rate))
	}
	if live {
		liveRate := delta / span
		// Fade out history quickly; current disk speed and conversation size win.
		weight := min(1, max(0.75, span/30))
		if rate > 0 {
			// Bound the prior's influence when this run is dramatically faster
			// or slower. Even a large history cannot overrule measured work.
			prior := min(max(rate, liveRate/2), liveRate*2)
			rate = prior*(1-weight) + liveRate*weight
		} else {
			rate = liveRate
		}
		uncertainty = 0.25
		for i := 1; i < len(o.samples); i++ {
			previous, current := o.samples[i-1], o.samples[i]
			intervalRate := (current.done - previous.done) / current.at.Sub(previous.at).Seconds()
			uncertainty = max(uncertainty, math.Abs(intervalRate-liveRate)/liveRate)
		}
		uncertainty = min(1.5, uncertainty)
		result.Basis = "Recent pace of this stage; adjusts as work completes"
		result.Samples = len(o.samples) - 1
	}
	if finitePositive(rate) {
		result.Rate = rate
	}
	if work.Total <= 0 && !work.TotalKnown {
		result.State, result.Basis = "unknown", "Still discovering work; total size is not known yet"
		return result
	}
	if work.Done >= work.Total {
		result.State, result.Basis, result.Rate = "finishing", "Counted work processed; finishing this stage", 0
		return result
	}
	stallAfter := 45.0
	if rate > 0 && work.Unit != "byte" {
		stallAfter = max(stallAfter, 3/rate)
	}
	if at.Sub(o.changed).Seconds() > stallAfter {
		result.State, result.Basis, result.Rate = "stalled", "No recent completed work; the current item may be large or waiting", 0
		return result
	}
	if rate <= 0 {
		return result
	}
	remaining := (work.Total - work.Done) / rate
	result.State = "estimating"
	result.LowSeconds = max(1, remaining/(1+uncertainty))
	result.HighSeconds = max(result.LowSeconds, remaining*(1+uncertainty))
	return result
}

func (s *Server) estimateActivities(activities []libraryActivity, at time.Time) {
	s.estimator.mu.Lock()
	defer s.estimator.mu.Unlock()
	if s.estimator.observations == nil {
		s.estimator.observations = map[string]*progressObservation{}
	}
	active := map[string]bool{}
	for i := range activities {
		a := &activities[i]
		a.Forecast = a.operation.estimate(at, a.Stopping)
		a.EstimatedCompletionAt = ""
		a.Estimate = &activityEstimate{State: "unknown", Basis: "This stage does not report a measurable total"}
		if a.Work != nil {
			work := *a.Work
			measure := work.measured()
			key := observationKey(a.ID, a.Kind, work)
			active[key] = true
			o := s.estimator.observations[key]
			if o == nil {
				o = &progressObservation{history: s.workTimings(workTimingKey(defaultString(work.Kind, defaultString(a.timingKind, a.Kind)), work))}
				s.estimator.observations[key] = o
			}
			estimate := o.estimate(work, at)
			a.Estimate = &estimate
			if measure.Total == 0 && work.TotalKnown {
				a.Progress = fraction(1, 1)
			} else if measure.Total > 0 {
				a.Progress = fraction(measure.Done, measure.Total)
			} else {
				a.Progress = nil
			}
		}
		if a.Stopping {
			a.Estimate = &activityEstimate{State: "stopping", Basis: "Stopping after the current record is safe"}
		} else if a.Estimate.State == "unknown" {
			phase := ""
			if a.Work != nil {
				phase = a.Work.Phase
			}
			switch {
			case strings.Contains(phase, "waiting"):
				a.Estimate.Basis = "Waiting for the catalog write lock; timing resumes when records can be saved"
			case strings.Contains(phase, "saving"), phase == "finishing":
				a.Estimate.Basis = "Saving and flushing data; no reliable remaining count"
			case a.Kind == "capture-other":
				a.Estimate.Basis = "The other process does not report its progress to this service"
			case a.Kind == "git":
				a.Estimate.Basis = "Checking repository history and refreshing derived data; remaining work varies by repository"
			case a.Kind == "backup":
				a.Estimate.Basis = "Copying the catalog and discovering files; the full backup size is not known yet"
			case strings.Contains(phase, "database snapshots"):
				a.Estimate.Basis = "Taking consistent database snapshots; file-copy progress does not measure this stage"
			}
		}
		// Prefer the whole-operation forecast for the native menu bar.
		if a.Forecast != nil {
			if a.Forecast.Point > 0 {
				a.EstimatedCompletionAt = formatTime(at.Add(time.Duration(a.Forecast.Point * float64(time.Second))))
			}
			continue
		}
		// Compatibility for activities without a complete operation forecast.
		if a.Estimate.State == "estimating" {
			seconds := (a.Estimate.LowSeconds + a.Estimate.HighSeconds) / 2
			a.EstimatedCompletionAt = formatTime(at.Add(time.Duration(seconds * float64(time.Second))))
		}
	}
	for key := range s.estimator.observations {
		if !active[key] {
			delete(s.estimator.observations, key)
		}
	}
}

func nonemptyParts(parts ...string) []string {
	return slices.DeleteFunc(parts, func(part string) bool { return part == "" })
}

func captureWork(result CaptureSourceResult) activityWork {
	work := activityWork{Phase: defaultString(result.Phase, "discovering files"), Source: result.Name,
		StartedAt: result.PhaseStartedAt, Model: result.Kind, Kind: "capture", Next: "Database snapshots and saving the capture"}
	if result.Phase == "copying files" {
		work.Unit, work.Done, work.Total = "byte", float64(result.BytesCopied), float64(result.BytesPlanned)
	}
	if result.Phase == "saving capture manifest" {
		work.Next = "Remaining sources, then indexing if requested"
	}
	return work
}

// Both the explicit capture index and Capture & index/recovery use this meter.
// Keep retained snapshot work outside the measured current-transcript stage.
type indexProgressMeter struct {
	planned   func(context.Context, *sourceWorkPlan, string)
	needed    int
	plan      *sourceWorkPlan
	model     string
	size      *activitySize
	started   string
	finished  time.Time
	processed int
}

func (p *indexPlan) meter(index int) *indexProgressMeter {
	m := &indexProgressMeter{needed: p.needed[index], model: p.models[index]}
	if index < len(p.plans) {
		m.plan = p.plans[index]
	}
	if p.sizes[index] != nil {
		copied := *p.sizes[index]
		m.size = &copied
	}
	m.planned = func(ctx context.Context, plan *sourceWorkPlan, model string) {
		needed := plan.needed
		if plan.discoveryError != nil {
			needed = -1
		}
		p.totalUnits += planUnits(needed) - planUnits(p.needed[index])
		p.needed[index], p.models[index] = needed, model
		if index < len(p.plans) {
			p.plans[index] = plan
		}
		if index < len(p.sizes) {
			p.sizes[index] = nil
			if plan.discoveryError == nil {
				p.sizes[index] = plan.size
			}
		}
		p.reviseForecast(ctx)
	}
	return m
}

func (m *indexProgressMeter) context(ctx context.Context) context.Context {
	mode := modeOf(ctx)
	mode.plan = m.plan
	mode.planned = func(plan *sourceWorkPlan, model string) {
		changed := plan != m.plan || model != m.model
		m.plan, m.needed, m.model, m.size = plan, plan.needed, model, nil
		if plan.discoveryError != nil {
			m.needed = -1
		}
		if plan.size != nil && plan.discoveryError == nil {
			copied := *plan.size
			m.size = &copied
		}
		if changed && m.planned != nil {
			m.planned(ctx, plan, model)
		}
	}
	mode.processed = func(parts []sourcePart) {
		if !m.finished.IsZero() || m.size == nil {
			return
		}
		amount, unit := partWorkSize(parts)
		if unit != m.size.Unit {
			m.size = nil
		} else {
			m.size.Done += amount
			if m.plan != nil && m.plan.size != nil {
				m.size.Total = m.plan.size.Total
			}
		}
	}
	return withIngestMode(ctx, mode)
}

func (m *indexProgressMeter) report(run *SyncRun, phase, source string, processed int) {
	run.setWork(phase, source, "conversation group", float64(processed), float64(max(0, m.needed)))
	run.Work.Model, run.Work.Kind = m.model, "index"
	run.Work.TotalKnown = m.needed >= 0
	if m.plan != nil {
		run.Work.Features = m.plan.features
	}
	run.Work.Next = "Remaining sources, identity links, and library maintenance"
	if phase == "indexing" {
		if m.started == "" {
			m.started = run.Work.StartedAt
		}
		if m.size != nil {
			copied := *m.size
			run.Work.Size = &copied
		}
	} else {
		run.Work.TotalKnown = false
		run.Work.Total = 0
	}
	if phase == "complete" {
		m.finished, m.processed = time.Now(), processed
	}
}

func (m *indexProgressMeter) record(s *Server, ctx context.Context, result IngestResult) {
	if result.Error == nil && ctx.Err() == nil && m.needed > 0 {
		features := workFeatures{}
		if m.plan != nil {
			features = m.plan.features
		}
		s.recordWorkTiming("index", activityWork{Phase: "indexing", Model: m.model, Unit: "conversation group", StartedAt: m.started,
			Done: float64(m.processed), Total: float64(m.needed), Size: m.size, Features: features}, m.finished)
	}
}
