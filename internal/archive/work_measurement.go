package archive

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Workload features are retained with timings so models can be fitted and
// evaluated against input volume, freshness and machine conditions later.
type workFeatures struct {
	Dirty                 *bool    `json:"analysis_dirty,omitempty"`
	Conversations         float64  `json:"conversations,omitempty"`
	BytesBasis            string   `json:"bytes_basis,omitempty"`
	Groups                float64  `json:"groups,omitempty"`
	Files                 float64  `json:"files,omitempty"`
	Bytes                 float64  `json:"bytes,omitempty"`
	Messages              float64  `json:"messages,omitempty"`
	SinceLastIndexSeconds float64  `json:"since_last_index_seconds,omitempty"`
	LoadOneMinute         *float64 `json:"load_one_minute,omitempty"`
}

type workStageMeasurement struct {
	Phase      string       `json:"phase"`
	Unit       string       `json:"unit,omitempty"`
	Done       float64      `json:"done"`
	Total      float64      `json:"total"`
	TotalKnown bool         `json:"total_known"`
	Seconds    float64      `json:"seconds"`
	Completed  bool         `json:"completed"`
	Features   workFeatures `json:"features"`
}

type workProgressKey struct{}

func withWorkProgress(ctx context.Context, report func(activityWork)) context.Context {
	return context.WithValue(ctx, workProgressKey{}, report)
}

// Progress hooks never persist or acquire a catalog write lock: they may run
// inside a transaction. The coordinator saves measurements after work returns.
func reportWork(ctx context.Context, phase, unit string, done, total int) {
	if ctx == nil {
		return
	}
	if report, ok := ctx.Value(workProgressKey{}).(func(activityWork)); ok {
		report(activityWork{Phase: phase, Unit: unit, Done: float64(done), Total: float64(total), TotalKnown: total >= 0})
	}
}

type runWorkMeter struct {
	mu                 sync.Mutex
	server             *Server
	runID, kind, model string
	current            *activityWork
	baseline           activityWork
	stages             []workStageMeasurement
	timings            []struct {
		work activityWork
		end  time.Time
	}
	clock   func() time.Time
	closed  bool
	publish func(activityWork)
}

func (m *runWorkMeter) time() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func (m *runWorkMeter) release(success bool) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	m.finish(success)
	m.server.estimator.mu.Lock()
	for key := range m.server.estimator.observations {
		if strings.HasPrefix(key, m.runID+"\x1f") {
			delete(m.server.estimator.observations, key)
		}
	}
	m.server.estimator.mu.Unlock()
}

func (m *runWorkMeter) closeStage(at time.Time, success bool) {
	if m.current == nil {
		return
	}
	w := cloneWork(*m.current)
	started, _ := time.Parse(time.RFC3339Nano, w.StartedAt)
	measured := w.measured()
	known := w.TotalKnown || measured.Total > 0
	// A restarted clock covers only newly completed work; worker counters
	// remain cumulative across catalog waits and other stage interruptions.
	baseline := m.baseline.measured()
	if measured.Unit == baseline.Unit {
		measured.Done -= baseline.Done
		measured.Total -= baseline.Done
	}
	measured.Size = nil
	completed := success && known && measured.Done >= measured.Total
	m.stages = append(m.stages, workStageMeasurement{Phase: w.Phase, Unit: measured.Unit, Done: measured.Done, Total: measured.Total, TotalKnown: known, Seconds: at.Sub(started).Seconds(), Completed: completed, Features: w.Features})
	if completed {
		m.timings = append(m.timings, struct {
			work activityWork
			end  time.Time
		}{measured, at})
	}
	m.current = nil
}

func (m *runWorkMeter) report(w activityWork) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	at := m.time()
	refining := m.current != nil && m.current.Phase == w.Phase && m.current.Source == w.Source && m.current.Unit == "" && !m.current.TotalKnown && m.current.Total <= 0
	if m.current == nil || m.current.Phase != w.Phase || m.current.Source != w.Source || !refining && (m.current.Unit != w.Unit || w.Done < m.current.Done || w.Total != m.current.Total) {
		m.closeStage(at, w.Phase != "failed" && w.Phase != "interrupted")
		m.baseline = cloneWork(w)
		w.StartedAt = formatTime(at)
		if w.Features.LoadOneMinute == nil {
			w.Features.LoadOneMinute = machineLoad()
		}
	} else {
		w.StartedAt = m.current.StartedAt
		if w.Features.LoadOneMinute == nil {
			w.Features.LoadOneMinute = m.current.Features.LoadOneMinute
		}
	}
	if w.Model == "" {
		w.Model = m.model
	}
	// Analysis workers use the same implementation through manual, capture and
	// background entry points; their phase/unit already identifies the work.
	if w.Model == "analysis-v1" || strings.HasPrefix(w.Model, "analysis-v1:") {
		w.Model = "analysis-v1"
		if w.Kind == "" {
			w.Kind = "analysis"
		}
	}
	if w.Kind == "" {
		w.Kind = m.kind
	}
	copied := cloneWork(w)
	m.current = &copied
	if m.publish != nil {
		m.publish(cloneWork(w))
	} else {
		m.server.updateRun(m.runID, func(run *SyncRun) {
			run.Phase = w.Phase
			run.CurrentSource = nilIfEmpty(w.Source)
			snapshot := cloneWork(w)
			run.Work = &snapshot
		})
	}
	// Worker events train the live window even when no UI is polling.
	m.server.observeWork(m.runID, m.kind, w, at)
}

func (m *runWorkMeter) finish(success bool) []workStageMeasurement {
	m.mu.Lock()
	m.closeStage(m.time(), success)
	stages, timings := m.stages, m.timings
	m.stages, m.timings = nil, nil
	m.mu.Unlock()
	for _, timing := range timings {
		m.server.recordWorkTiming(defaultString(timing.work.Kind, m.kind), timing.work, timing.end)
	}
	return stages
}

func (s *Server) observeWork(id, kind string, work activityWork, at time.Time) {
	s.estimator.mu.Lock()
	defer s.estimator.mu.Unlock()
	if s.estimator.observations == nil {
		s.estimator.observations = map[string]*progressObservation{}
	}
	key := observationKey(id, kind, work)
	for prior := range s.estimator.observations {
		if prior != key && strings.HasPrefix(prior, id+"\x1f") {
			delete(s.estimator.observations, prior)
		}
	}
	observation := s.estimator.observations[key]
	if observation == nil {
		observation = &progressObservation{history: s.workTimings(workTimingKey(defaultString(work.Kind, kind), work))}
		s.estimator.observations[key] = observation
	}
	observation.estimate(work, at)
}

func observationKey(id, kind string, work activityWork) string {
	return id + "\x1f" + kind + "\x1f" + work.Phase + "\x1f" + work.Source + "\x1f" + work.measured().Unit + "\x1f" + work.StartedAt
}
