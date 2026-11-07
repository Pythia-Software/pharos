package archive

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Resource history (docs/work-resource-accounting.md): a bounded rolling
// record of what one sampled process tree used, so reopening the panel shows
// the work that ran while it was closed. One collector runs per owner of a
// process tree. In the app that is the broker, with each library as a subset
// scope. A standalone service collects its own tree. A service the broker
// manages, and every CLI command, runs none.
//
// The collector reads through the process's existing resourceSampler (and
// the shared GPU sampler), never through a sampler of its own. While it runs,
// status snapshots reuse its latest summary (latest), so no process is read
// twice. It starts only when its owner calls run, which the parent gates on
// the scheduled engine. Classic execution keeps on-demand sampling.
//
// A sample is stored only when its reading is newer than the last one: a
// gap is missing samples and an explicit segment, never an interpolated or
// repeated value. GPU samples keep their own timestamps and cadence.

// resourceHistoryWindow is how far back the history reaches.
var resourceHistoryWindow = 15 * time.Minute

// resourceHistoryActive and resourceHistoryIdle are the cadences while work
// runs and while it does not.
var (
	resourceHistoryActive = 2 * time.Second
	resourceHistoryIdle   = 10 * time.Second
)

// resourceHistoryGap is the stale gap while a collector runs: 2.5 times the
// idle cadence, so idle readings still give a rate. A longer interval
// between ticks (the Mac slept) starts a new segment.
var resourceHistoryGap = 25 * time.Second

// resourceTaskCarry bounds how long a library's known tasks carry forward
// while its service does not answer: past it they end (observed), so a
// wedged service neither keeps stale running tasks nor the active cadence.
var resourceTaskCarry = time.Minute

// resourceHistoryBusyCPU (percent of one core) keeps the active cadence for
// load the owner's operations do not show, such as another library's
// service or a Terminal command.
var resourceHistoryBusyCPU = 10.0

// resourceHistoryMarkerLimit bounds task markers, resourceHistoryScopeLimit
// the library scopes one collector keeps.
var (
	resourceHistoryMarkerLimit = 400
	resourceHistoryScopeLimit  = 64
)

// Scope labels, as the panel shows them.
const (
	resourceScopeService  = "this library service"
	resourceScopeApp      = "Pharos app (covered processes)"
	resourceOmitNativeApp = "Pharos Mac app: ownership is not verified, so it is not measured"
	resourceOmitSelfOnly  = "Other processes: this platform reads only this process's own usage"
)

// resourceHistoryScope is one scope's reading for a tick: a summary from
// summarizeResources, and what it knows it left out.
type resourceHistoryScope struct {
	// Key is "" for the collector's whole scope, else a library ID.
	Key, Label string
	Summary    *resourceSummary
	Omitted    []string
}

// resourceHistoryTask is a top-level operation that has not ended, for
// markers. Only a running one gets a start marker: queued and blocked work is
// not load.
type resourceHistoryTask struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Variant   string    `json:"variant,omitempty"`
	Priority  string    `json:"priority"`
	Trigger   string    `json:"trigger"`
	State     string    `json:"state"`
	Libraries []string  `json:"libraries,omitempty"`
	Started   time.Time `json:"started_at"`
	// RunStarted is the recorded first transition to running. Older task
	// reporters may omit it; their start is the first tick that saw them run.
	RunStarted time.Time `json:"run_started_at,omitzero"`
	// source is the library whose service reported the task ("" for the
	// collector's own registry).
	source string
	// seen is when the history last knew it running: the latest frame that
	// listed it, or its start mark.
	seen time.Time
}

// resourceHistoryFrame is what the owner's collect callback returns.
type resourceHistoryFrame struct {
	Scopes []resourceHistoryScope
	Tasks  []resourceHistoryTask
	// Active selects the two-second cadence.
	Active bool
	// UnknownLibraries are libraries whose services' tasks could not be
	// read this tick: their known tasks carry forward, neither ended nor
	// restarted.
	UnknownLibraries []string
}

// resourceHistoryMarker is a task boundary on the chart's time axis.
type resourceHistoryMarker struct {
	At                          time.Time
	Event                       string // start or end
	ID, Kind, Variant, Priority string
	Trigger                     string
	Libraries                   []string
	// Observed is true when At is the tick that first saw the task running
	// (start) or first no longer saw it (end), not its recorded time.
	Observed bool
}

type resourceHistoryPoint struct {
	at                      time.Time
	cpu, disk               *float64
	memory                  *uint64
	cpuPartial, diskPartial bool
	processes               int
}

type resourceHistoryGPUPoint struct {
	at      time.Time
	percent *float64
	partial bool
}

type resourceHistorySegment struct {
	at             time.Time
	reason, detail string
}

type resourceHistoryCoverage struct {
	at      time.Time
	covered []resourceCovered
	omitted []string
	key     string
}

// resourceCovered is one measured process.
type resourceCovered struct {
	PID     int    `json:"pid"`
	Role    string `json:"role"`
	Library string `json:"library,omitempty"`
}

type resourceSeries struct {
	label     string
	summary   *resourceSummary
	summaryAt time.Time
	seenAt    time.Time
	basis     string
	points    []resourceHistoryPoint
	gpu       []resourceHistoryGPUPoint
	gpuSeen   bool
	diskSeen  bool
	segments  []resourceHistorySegment
	coverage  []resourceHistoryCoverage
	identity  string
}

type resourceHistory struct {
	sampler *resourceSampler
	gpu     *gpuSampler
	collect func(time.Time) resourceHistoryFrame
	now     func() time.Time
	wake    chan struct{}

	mu       sync.Mutex
	running  bool
	fresh    bool
	instance string
	lastTick time.Time
	active   bool
	scopes   map[string]*resourceSeries
	markers  []resourceHistoryMarker
	tasks    map[string]resourceHistoryTask
	// ticks counts collections, for tests.
	ticks int
}

// newResourceHistory makes the collector for sampler's process tree and
// attaches it, so the sampler's snapshots can reuse its readings. Nothing
// runs until run.
func newResourceHistory(sampler *resourceSampler, gpu *gpuSampler, collect func(time.Time) resourceHistoryFrame) *resourceHistory {
	clock := time.Now
	h := &resourceHistory{sampler: sampler, gpu: gpu, collect: collect, wake: make(chan struct{}, 1),
		scopes: map[string]*resourceSeries{}, tasks: map[string]resourceHistoryTask{}}
	if sampler != nil {
		sampler.mu.Lock()
		sampler.history = h
		if sampler.now != nil {
			clock = sampler.now
		}
		sampler.mu.Unlock()
	}
	// History compares wall times: Go's monotonic clock pauses while the Mac
	// sleeps, which would hide the gap and keep pre-sleep samples in the
	// window. The sampler's rates keep monotonic time.
	h.now = func() time.Time { return wallTime(clock()) }
	return h
}

// run collects until ctx ends: every two seconds while work is active, every
// ten otherwise. A second concurrent run returns at once.
func (h *resourceHistory) run(ctx context.Context) {
	if h == nil || h.collect == nil {
		return
	}
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return
	}
	h.running, h.fresh = true, true
	h.instance = fmt.Sprintf("%d-%d", os.Getpid(), h.now().UnixNano())
	h.mu.Unlock()
	if h.sampler != nil {
		h.sampler.setStaleGap(resourceHistoryGap)
	}
	defer h.gpu.widen(resourceHistoryGap)()
	defer func() {
		if h.sampler != nil {
			h.sampler.setStaleGap(0)
		}
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-h.wake:
			// Work started: tick now, unless the last tick was more recent
			// than the active cadence.
			if wait := h.untilActive(); wait > 0 {
				timer.Reset(wait)
				continue
			}
			timer.Stop()
		}
		delay := resourceHistoryIdle
		if h.tick() {
			delay = resourceHistoryActive
		}
		timer.Reset(delay)
	}
}

// wallTime strips the monotonic reading, so comparisons use the wall clock.
func wallTime(at time.Time) time.Time { return at.Round(0) }

// poke asks a running collector to tick soon (an operation started).
func (h *resourceHistory) poke() {
	if h == nil {
		return
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *resourceHistory) untilActive() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastTick.IsZero() {
		return 0
	}
	return h.lastTick.Add(resourceHistoryActive).Sub(h.now())
}

// tick collects one frame and records it; it reports whether the next tick
// uses the active cadence. collect runs outside the lock: it samples.
func (h *resourceHistory) tick() bool {
	now := h.now()
	frame := h.collect(now)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(now, frame)
	return h.active
}

func (h *resourceHistory) record(now time.Time, frame resourceHistoryFrame) {
	now = wallTime(now)
	h.ticks++
	gap := !h.lastTick.IsZero() && now.Sub(h.lastTick) > resourceHistoryGap
	busy := false
	for _, scope := range frame.Scopes {
		series := h.scopes[scope.Key]
		if series == nil {
			if len(h.scopes) >= resourceHistoryScopeLimit {
				continue
			}
			series = &resourceSeries{}
			h.scopes[scope.Key] = series
		}
		series.label, series.seenAt = scope.Label, now
		series.summary, series.summaryAt = scope.Summary, now
		// A tick without a readable process is an explicit empty sample at
		// the tick's time; otherwise the sample's time is its readings'.
		point := resourceHistoryPoint{at: now}
		covered := []resourceCovered{}
		identity := ""
		if summary := scope.Summary; summary != nil {
			series.basis = summary.Basis
			point = historyPoint(summary)
			for _, reading := range summary.Processes {
				covered = append(covered, resourceCovered{PID: reading.PID, Role: reading.Role, Library: reading.Library})
				if identity == "" && (reading.Role == "service" || reading.Role == "broker") {
					identity = fmt.Sprintf("%s:%d:%d", reading.Role, reading.PID, reading.started)
				}
			}
			if gpu := summary.GPU; gpu != nil {
				// The row exists once a value was read: a sampler that only
				// ever fails (Intel, a VM) gives no all-null GPU row.
				series.gpuSeen = series.gpuSeen || gpu.percent != nil
				at := wallTime(gpu.at)
				if last := len(series.gpu); last == 0 || at.After(series.gpu[last-1].at) {
					series.gpu = append(series.gpu, resourceHistoryGPUPoint{at: at, percent: gpu.percent, partial: gpu.Partial})
				}
			}
			if point.cpu != nil && *point.cpu >= resourceHistoryBusyCPU {
				busy = true
			}
		}
		// Segment boundaries sit at the first sample after them.
		switch {
		case h.fresh:
			series.segments = append(series.segments, resourceHistorySegment{at: point.at, reason: "started", detail: h.instance})
		case gap:
			series.segments = append(series.segments, resourceHistorySegment{at: point.at, reason: "gap",
				detail: fmt.Sprintf("No samples for %.0f s", now.Sub(h.lastTick).Seconds())})
		case identity != "" && series.identity != "" && series.identity != identity:
			series.segments = append(series.segments, resourceHistorySegment{at: point.at, reason: "restart", detail: identity})
		}
		if identity != "" {
			series.identity = identity
		}
		if point.disk != nil {
			series.diskSeen = true
		}
		if last := len(series.points); last == 0 || point.at.After(series.points[last-1].at) {
			series.points = append(series.points, point)
		}
		omitted := slices.Clone(scope.Omitted)
		if scope.Summary != nil && scope.Summary.Approximate {
			omitted = append(omitted, resourceOmitSelfOnly)
		}
		slices.SortFunc(covered, func(a, b resourceCovered) int { return a.PID - b.PID })
		key := coverageKey(covered, omitted)
		if last := len(series.coverage); last == 0 || series.coverage[last-1].key != key {
			series.coverage = append(series.coverage, resourceHistoryCoverage{at: point.at, covered: covered, omitted: omitted, key: key})
		}
	}
	h.observeTasks(now, frame.Tasks, frame.UnknownLibraries)
	h.active = frame.Active || busy
	for _, task := range h.tasks {
		if task.State == opRunning {
			h.active = true // includes tasks carried through a metadata timeout
			break
		}
	}
	h.fresh = false
	h.lastTick = now
	h.trim(now)
}

// historyPoint is a summary's exact totals: the rounded ones are for the
// status document. Its time is its newest reading's.
func historyPoint(summary *resourceSummary) resourceHistoryPoint {
	point := resourceHistoryPoint{cpuPartial: summary.CPUPartial, diskPartial: summary.DiskPartial, processes: len(summary.Processes)}
	cpu, disk, memory := 0.0, 0.0, uint64(0)
	cpuKnown, diskKnown := false, false
	for _, reading := range summary.Processes {
		if reading.cpu != nil {
			cpu += *reading.cpu
			cpuKnown = true
		}
		if reading.disk != nil {
			disk += *reading.disk
			diskKnown = true
		}
		memory += reading.memory
		if at := wallTime(reading.at); at.After(point.at) {
			point.at = at
		}
	}
	if cpuKnown {
		point.cpu = new(min(cpu, 100*float64(resourceCores())))
	}
	if diskKnown {
		point.disk = &disk
	}
	if len(summary.Processes) > 0 {
		point.memory = &memory
	}
	return point
}

func coverageKey(covered []resourceCovered, omitted []string) string {
	parts := make([]string, 0, len(covered)+len(omitted))
	for _, process := range covered {
		parts = append(parts, fmt.Sprintf("%d/%s/%s", process.PID, process.Role, process.Library))
	}
	return strings.Join(parts, ",") + "|" + strings.Join(omitted, "|")
}

// observeTasks turns the reported tasks into start and end markers. A task
// starts when it is first seen running (queued work is not load) and, once
// started, ends only when it is no longer reported. Tasks from a library
// whose list could not be read are carried forward unchanged, for at most
// resourceTaskCarry since they were last reported.
func (h *resourceHistory) observeTasks(now time.Time, tasks []resourceHistoryTask, unknown []string) {
	seen := make(map[string]resourceHistoryTask, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			continue
		}
		// Collection happens outside the history lock. An owner can finish
		// after the running-set read but before this frame is recorded.
		// Its exact end outranks that older snapshot.
		if slices.ContainsFunc(h.markers, func(marker resourceHistoryMarker) bool {
			return marker.ID == task.ID && marker.Event == "end" && !marker.Observed
		}) {
			continue
		}
		task.seen = now
		if _, known := h.tasks[task.ID]; known {
			seen[task.ID] = task
			continue
		}
		if task.State != opRunning {
			continue
		}
		seen[task.ID] = task
		at, observed := wallTime(task.RunStarted), false
		if at.IsZero() || at.After(now) {
			at, observed = now, true
		}
		// A recovered report of the same task disproves its inferred end.
		// Keep its original start, rather than inventing a second start at
		// RunStarted. Exact ends were already excluded above.
		if slices.ContainsFunc(h.markers, func(marker resourceHistoryMarker) bool {
			return marker.ID == task.ID && marker.Event == "end" && marker.Observed
		}) {
			h.markers = slices.DeleteFunc(h.markers, func(marker resourceHistoryMarker) bool {
				return marker.ID == task.ID && marker.Event == "end" && marker.Observed
			})
			if slices.ContainsFunc(h.markers, func(marker resourceHistoryMarker) bool {
				return marker.ID == task.ID && marker.Event == "start"
			}) {
				continue
			}
			at, observed = now, true // the original start aged out of retention
		}
		h.appendMarker(resourceHistoryMarker{At: at, Event: "start", ID: task.ID, Kind: task.Kind, Variant: task.Variant,
			Priority: task.Priority, Trigger: task.Trigger, Libraries: task.Libraries, Observed: observed})
	}
	for id, task := range h.tasks {
		if _, still := seen[id]; still {
			continue
		}
		// A start marked after this frame's collection began is newer than
		// its task list, which could not include it.
		if task.seen.After(now) {
			seen[id] = task
			continue
		}
		if task.source != "" && slices.Contains(unknown, task.source) && now.Sub(task.seen) < resourceTaskCarry {
			seen[id] = task
			continue
		}
		h.appendMarker(resourceHistoryMarker{At: now, Event: "end", ID: id, Kind: task.Kind, Variant: task.Variant,
			Priority: task.Priority, Trigger: task.Trigger, Libraries: task.Libraries, Observed: true})
	}
	h.tasks = seen
}

// mark records an explicit marker, such as an operation's exact end. An
// observed end marker for the same task is replaced.
func (h *resourceHistory) mark(marker resourceHistoryMarker) {
	if h == nil || marker.Event == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	marker.At = wallTime(marker.At)
	if marker.At.IsZero() {
		marker.At = h.now()
	}
	if marker.Event == "end" {
		h.markers = slices.DeleteFunc(h.markers, func(m resourceHistoryMarker) bool {
			return m.Event == "end" && m.Observed && m.ID == marker.ID
		})
		delete(h.tasks, marker.ID)
	}
	if marker.Event == "start" {
		if _, known := h.tasks[marker.ID]; known {
			return
		}
		h.tasks[marker.ID] = resourceHistoryTask{ID: marker.ID, Kind: marker.Kind, Variant: marker.Variant, Priority: marker.Priority,
			Trigger: marker.Trigger, Libraries: marker.Libraries, State: opRunning, Started: marker.At, RunStarted: marker.At, seen: h.now()}
	}
	h.appendMarker(marker)
}

func (h *resourceHistory) appendMarker(marker resourceHistoryMarker) {
	// Keep markers in time order: a start can predate later ones.
	index, _ := slices.BinarySearchFunc(h.markers, marker.At, func(m resourceHistoryMarker, at time.Time) int { return m.At.Compare(at) })
	for index < len(h.markers) && !h.markers[index].At.After(marker.At) {
		index++
	}
	h.markers = slices.Insert(h.markers, index, marker)
	if extra := len(h.markers) - resourceHistoryMarkerLimit; extra > 0 {
		h.markers = slices.Delete(h.markers, 0, extra)
	}
}

// trim drops what is older than the window, keeping the coverage and the
// segment in effect at its start, and scopes not seen for a whole window.
func (h *resourceHistory) trim(now time.Time) {
	cutoff := now.Add(-resourceHistoryWindow)
	for key, series := range h.scopes {
		if series.seenAt.Before(cutoff) {
			delete(h.scopes, key)
			continue
		}
		series.points = slices.DeleteFunc(series.points, func(p resourceHistoryPoint) bool { return p.at.Before(cutoff) })
		series.gpu = slices.DeleteFunc(series.gpu, func(p resourceHistoryGPUPoint) bool { return p.at.Before(cutoff) })
		series.coverage = keepFrom(series.coverage, func(c resourceHistoryCoverage) bool { return c.at.Before(cutoff) })
		series.segments = keepFrom(series.segments, func(s resourceHistorySegment) bool { return s.at.Before(cutoff) })
	}
	h.markers = slices.DeleteFunc(h.markers, func(m resourceHistoryMarker) bool { return m.At.Before(cutoff) })
}

// keepFrom drops leading entries before the cutoff except the last of them:
// the state in effect when the window starts.
func keepFrom[T any](entries []T, before func(T) bool) []T {
	drop := 0
	for drop+1 < len(entries) && before(entries[drop+1]) {
		drop++
	}
	return slices.Delete(entries, 0, drop)
}

// latest is the scope's summary from the last tick, while the collector runs
// and that tick is recent. A nil summary with ok means nothing was readable.
func (h *resourceHistory) latest(key string) (*resourceSummary, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	series := h.scopes[key]
	if !h.running || series == nil || h.now().Sub(series.summaryAt) > resourceHistoryIdle+resourceHistoryActive {
		return nil, false
	}
	if series.summary == nil {
		return nil, true
	}
	copied := *series.summary
	return &copied, true
}

// cpuSeconds integrates the scope's CPU over [from, to]: each sample's rate
// covers the time since the previous sample in the same segment. coverage is
// the fraction of the interval with a rate; partial is true when a covered
// sample was a lower bound. This is the process tree's total, which other
// work shares: context for a task, not its own CPU.
func (h *resourceHistory) cpuSeconds(key string, from, to time.Time) (seconds, coverage float64, partial bool) {
	if h == nil || !to.After(from) {
		return 0, 0, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	series := h.scopes[key]
	if series == nil {
		return 0, 0, false
	}
	covered := 0.0
	for i := 1; i < len(series.points); i++ {
		previous, point := series.points[i-1], series.points[i]
		if point.cpu == nil || segmentBetween(series.segments, previous.at, point.at) {
			continue
		}
		start, end := maxTime(previous.at, from), minTime(point.at, to)
		if !end.After(start) {
			continue
		}
		overlap := end.Sub(start).Seconds()
		seconds += *point.cpu / 100 * overlap
		covered += overlap
		partial = partial || point.cpuPartial
	}
	return seconds, covered / to.Sub(from).Seconds(), partial
}

func segmentBetween(segments []resourceHistorySegment, after, through time.Time) bool {
	return slices.ContainsFunc(segments, func(s resourceHistorySegment) bool { return s.at.After(after) && !s.at.After(through) })
}

// resourceHistoryDocument is GET /api/resources/history and
// /api/app/resources/history. Times are Unix milliseconds; each array in
// samples has one entry per sample time. A null value is a missing reading.
type resourceHistoryDocument struct {
	Version   int    `json:"version"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Pending is true when the collector runs but has no reading for the
	// scope yet: a client asks again, rather than stopping.
	Pending       bool                          `json:"pending,omitempty"`
	Scope         string                        `json:"scope,omitempty"`
	Library       string                        `json:"library,omitempty"`
	Instance      string                        `json:"instance,omitempty"`
	GeneratedAt   string                        `json:"generated_at"`
	Now           int64                         `json:"now"`
	Since         *int64                        `json:"since,omitempty"`
	WindowSeconds float64                       `json:"window_seconds"`
	Cadence       map[string]float64            `json:"cadence,omitempty"`
	Active        bool                          `json:"active"`
	Series        map[string]resourceSeriesInfo `json:"series,omitempty"`
	Samples       *resourceHistorySamples       `json:"samples,omitempty"`
	GPU           *resourceHistoryGPUSamples    `json:"gpu"`
	Segments      []resourceHistorySegmentView  `json:"segments"`
	Coverage      []resourceHistoryCoverageView `json:"coverage"`
	Markers       []resourceHistoryMarkerView   `json:"markers"`
	// Markers are a bounded authoritative snapshot, even when samples use
	// since. Exact start/end times may be learned after newer samples.
	MarkersComplete bool                      `json:"markers_complete"`
	Scopes          []resourceHistoryScopeRef `json:"scopes,omitempty"`
}

type resourceSeriesInfo struct {
	Label     string   `json:"label"`
	Unit      string   `json:"unit"`
	Basis     string   `json:"basis"`
	Available bool     `json:"available"`
	Max       *float64 `json:"max,omitempty"`
	Note      string   `json:"note,omitempty"`
}

type resourceHistorySamples struct {
	T           []int64    `json:"t"`
	CPU         []*float64 `json:"cpu"`
	Memory      []*uint64  `json:"memory"`
	Disk        []*float64 `json:"disk"`
	CPUPartial  []bool     `json:"cpu_partial"`
	DiskPartial []bool     `json:"disk_partial"`
	Processes   []int      `json:"processes"`
}

type resourceHistoryGPUSamples struct {
	T       []int64    `json:"t"`
	Value   []*float64 `json:"value"`
	Partial []bool     `json:"partial"`
}

type resourceHistorySegmentView struct {
	T      int64  `json:"t"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type resourceHistoryCoverageView struct {
	T       int64             `json:"t"`
	Covered []resourceCovered `json:"covered"`
	Omitted []string          `json:"omitted"`
}

type resourceHistoryMarkerView struct {
	T         int64    `json:"t"`
	Event     string   `json:"event"`
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Variant   string   `json:"variant,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	Trigger   string   `json:"trigger,omitempty"`
	Libraries []string `json:"libraries,omitempty"`
	Observed  bool     `json:"observed,omitempty"`
}

type resourceHistoryScopeRef struct {
	Key   string `json:"library"`
	Label string `json:"label"`
}

func unavailableResourceHistory(reason string, now time.Time) resourceHistoryDocument {
	return resourceHistoryDocument{Version: 1, Reason: reason, GeneratedAt: formatTime(now), Now: now.UnixMilli(),
		WindowSeconds: resourceHistoryWindow.Seconds(), Segments: []resourceHistorySegmentView{}, Coverage: []resourceHistoryCoverageView{}, Markers: []resourceHistoryMarkerView{}}
}

// document is the scope's history after since (zero: the whole window). The
// coverage in effect at since is included so a client can label the first
// new sample.
func (h *resourceHistory) document(key string, since time.Time) (resourceHistoryDocument, bool) {
	if h == nil {
		return unavailableResourceHistory("No resource history is collected in this process.", time.Now()), false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if !h.running {
		return unavailableResourceHistory("No resource history is collected in this process.", now), false
	}
	series := h.scopes[key]
	if series == nil {
		doc := unavailableResourceHistory("No resource history for this scope yet.", now)
		doc.Pending = true
		return doc, false
	}
	doc := unavailableResourceHistory("", now)
	doc.Available, doc.Scope, doc.Library, doc.Instance, doc.Active = true, series.label, key, h.instance, h.active
	doc.MarkersComplete = true
	if !since.IsZero() {
		doc.Since = new(since.UnixMilli())
	}
	doc.Cadence = map[string]float64{"active_seconds": resourceHistoryActive.Seconds(), "idle_seconds": resourceHistoryIdle.Seconds(), "gpu_seconds": gpuSampleInterval.Seconds()}
	cores := 100 * float64(resourceCores())
	basis := defaultString(series.basis, resourceBasis)
	doc.Series = map[string]resourceSeriesInfo{
		"cpu":    {Label: "CPU", Unit: "percent_one_core", Basis: basis, Available: true, Max: &cores, Note: "100% is one core; the total can exceed it."},
		"memory": {Label: "RAM", Unit: "bytes", Basis: basis, Available: true, Note: "Physical footprint of the covered processes at each sample."},
		"disk":   {Label: "I/O", Unit: "bytes_per_second", Basis: basis, Available: series.diskSeen, Note: "Bytes the covered processes read and wrote; exited children are not counted."},
		"gpu": {Label: "GPU", Unit: "percent_driver_time", Basis: gpuResourceBasis, Available: series.gpuSeen,
			Note: "Driver GPU-time share of the covered processes, sampled on its own cadence; not device saturation."},
	}
	samples := &resourceHistorySamples{T: []int64{}, CPU: []*float64{}, Memory: []*uint64{}, Disk: []*float64{}, CPUPartial: []bool{}, DiskPartial: []bool{}, Processes: []int{}}
	for _, point := range series.points {
		if !afterMilli(point.at, since) {
			continue
		}
		samples.T = append(samples.T, point.at.UnixMilli())
		samples.CPU = append(samples.CPU, roundTo(point.cpu, 10))
		samples.Memory = append(samples.Memory, point.memory)
		samples.Disk = append(samples.Disk, roundTo(point.disk, 1))
		samples.CPUPartial = append(samples.CPUPartial, point.cpuPartial)
		samples.DiskPartial = append(samples.DiskPartial, point.diskPartial)
		samples.Processes = append(samples.Processes, point.processes)
	}
	doc.Samples = samples
	if series.gpuSeen {
		gpu := &resourceHistoryGPUSamples{T: []int64{}, Value: []*float64{}, Partial: []bool{}}
		for _, point := range series.gpu {
			if afterMilli(point.at, since) {
				gpu.T = append(gpu.T, point.at.UnixMilli())
				gpu.Value = append(gpu.Value, roundTo(point.percent, 10))
				gpu.Partial = append(gpu.Partial, point.partial)
			}
		}
		doc.GPU = gpu
	}
	for _, segment := range series.segments {
		if afterMilli(segment.at, since) {
			doc.Segments = append(doc.Segments, resourceHistorySegmentView{T: segment.at.UnixMilli(), Reason: segment.reason, Detail: segment.detail})
		}
	}
	for i, coverage := range series.coverage {
		// The last entry at or before since is the state since then.
		if afterMilli(coverage.at, since) || i+1 == len(series.coverage) || afterMilli(series.coverage[i+1].at, since) {
			doc.Coverage = append(doc.Coverage, resourceHistoryCoverageView{T: coverage.at.UnixMilli(), Covered: coverage.covered, Omitted: append([]string{}, coverage.omitted...)})
		}
	}
	for _, marker := range h.markers {
		if key != "" && !slices.Contains(marker.Libraries, key) {
			continue
		}
		doc.Markers = append(doc.Markers, resourceHistoryMarkerView{T: marker.At.UnixMilli(), Event: marker.Event, ID: marker.ID, Kind: marker.Kind,
			Variant: marker.Variant, Priority: marker.Priority, Trigger: marker.Trigger, Libraries: marker.Libraries, Observed: marker.Observed})
	}
	if key == "" {
		for other, scope := range h.scopes {
			if other != "" {
				doc.Scopes = append(doc.Scopes, resourceHistoryScopeRef{Key: other, Label: scope.label})
			}
		}
		slices.SortFunc(doc.Scopes, func(a, b resourceHistoryScopeRef) int { return strings.Compare(a.Key, b.Key) })
	}
	return doc, true
}

// afterMilli compares at the document's resolution: a client passes back the
// last t it received, in milliseconds, and must not get that entry again.
func afterMilli(at, since time.Time) bool {
	return since.IsZero() || at.UnixMilli() > since.UnixMilli()
}

// roundTo keeps a rate to 1/scale: exact enough for a chart, and compact.
func roundTo(value *float64, scale float64) *float64 {
	if value == nil {
		return nil
	}
	return new(math.Round(*value*scale) / scale)
}

// serveResourceHistory answers a history request for key. Without a
// collector (classic execution) the document says so, with status 200, so
// the panel simply shows no chart.
func serveResourceHistory(w http.ResponseWriter, r *http.Request, h *resourceHistory, key string) {
	since, err := parseHistorySince(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	doc, _ := h.document(key, since)
	writeJSON(w, doc, http.StatusOK)
}

// parseHistorySince accepts Unix milliseconds or an RFC 3339 time.
func parseHistorySince(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	if at, ok := parseTime(value); ok {
		return at, nil
	}
	return time.Time{}, fmt.Errorf("since must be Unix milliseconds or an RFC 3339 time, not %q", value)
}

// resourceGroup is one subset scope of a collector (a library in the app).
type resourceGroup struct {
	Key, Label string
	Processes  []resourceProcess
	Omitted    []string
}

// collectResourceGroups reads every process once, with one process table,
// and returns the whole scope first, then each group's subset of the same
// readings. Each process is counted once in the whole scope; descendants
// belong to their nearest known root.
func collectResourceGroups(sampler *resourceSampler, gpu *gpuSampler, label string, groups []resourceGroup, extra []resourceProcess, omitted []string) []resourceHistoryScope {
	table := resourceProcessTable()
	all := slices.Clone(extra)
	for _, group := range groups {
		all = append(all, group.Processes...)
	}
	readings := sampler.sampleWithTable(all, table)
	byPID := make(map[int]resourceReading, len(readings))
	for _, reading := range readings {
		byPID[reading.PID] = reading
	}
	scopes := []resourceHistoryScope{{Label: label, Summary: withGPU(gpu, summarizeResources("Pharos", readings)),
		Omitted: append(slices.Clone(omitted), unreadableProcesses(expandResourceProcesses(all, table), byPID, table)...)}}
	for _, group := range groups {
		expanded := expandResourceProcesses(group.Processes, table)
		own := []resourceReading{}
		for _, process := range expanded {
			if reading, ok := byPID[process.PID]; ok && !slices.ContainsFunc(own, func(r resourceReading) bool { return r.PID == process.PID }) {
				own = append(own, reading)
			}
		}
		scopes = append(scopes, resourceHistoryScope{Key: group.Key, Label: defaultString(group.Label, "This library"),
			Summary: withGPU(gpu, summarizeResources(defaultString(group.Label, "This library"), own)),
			Omitted: append(slices.Clone(group.Omitted), unreadableProcesses(expanded, byPID, table)...)})
	}
	return scopes
}

// unreadableProcesses names processes that are alive (or of unknown
// liveness) but gave no reading. An exited process is not an omission.
func unreadableProcesses(processes []resourceProcess, readings map[int]resourceReading, table []processNode) []string {
	present := map[int]bool{}
	for _, node := range table {
		present[node.PID] = true
	}
	omitted, seen := []string{}, map[int]bool{}
	for _, process := range processes {
		if process.PID <= 0 || seen[process.PID] {
			continue
		}
		seen[process.PID] = true
		if _, ok := readings[process.PID]; ok || (table != nil && !present[process.PID]) {
			continue
		}
		omitted = append(omitted, fmt.Sprintf("%s (PID %d) could not be read", resourceRoleLabel(process), process.PID))
	}
	return omitted
}

// resourceHistoryTasks are the top-level operations that have not ended,
// with their states and recorded first transition to running, for markers.
func resourceHistoryTasks(ops []*Op) []resourceHistoryTask {
	tasks := []resourceHistoryTask{}
	for _, op := range ops {
		if op == nil || op.parent != nil || op.reg == nil {
			continue
		}
		op.reg.mu.Lock()
		task := resourceHistoryTask{ID: string(op.id), Kind: op.spec.Kind, Variant: op.spec.Variant, Priority: op.spec.Priority,
			Trigger: op.spec.Trigger, Libraries: op.spec.Libraries, State: op.state, Started: op.started}
		task.RunStarted = op.runningSince
		op.reg.mu.Unlock()
		tasks = append(tasks, task)
	}
	return tasks
}

// collectResourceHistory is a standalone service's collection: itself, its
// library's leases and their descendants. Work is active while an operation
// is running (not queued or blocked).
func (s *Server) collectResourceHistory(at time.Time) resourceHistoryFrame {
	tasks := resourceHistoryTasks(s.ops.running())
	active := slices.ContainsFunc(tasks, func(task resourceHistoryTask) bool { return task.State == opRunning })
	s.historyLeases.active = active
	return resourceHistoryFrame{Tasks: tasks, Active: active,
		Scopes: collectResourceGroups(s.resources, processGPU, resourceScopeService, nil, s.serviceHistoryProcesses(at), nil)}
}
