package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// historyHarness drives a collector on a scripted process table and clock,
// without its loop: each tick is explicit.
type historyHarness struct {
	clock   time.Time
	source  *fakeUsage
	sampler *resourceSampler
	history *resourceHistory
	active  bool
	tasks   []resourceHistoryTask
	roots   []resourceProcess
}

func newHistoryHarness(t *testing.T) *historyHarness {
	t.Helper()
	h := &historyHarness{clock: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC),
		source: &fakeUsage{procs: map[int]processUsage{10: {CPUSeconds: 1, Footprint: 300e6, Started: 1, DiskBytes: 1e6, DiskKnown: true}}},
		roots:  []resourceProcess{{PID: 10, Role: "service"}}}
	h.sampler = &resourceSampler{read: h.source.read, basis: resourceBasisKernel, now: func() time.Time { return h.clock }, byPID: map[int]*resourceTrack{}}
	h.history = newResourceHistory(h.sampler, nil, func(time.Time) resourceHistoryFrame {
		table := []processNode{}
		for pid := range h.source.procs {
			table = append(table, processNode{PID: pid, Parent: 1})
		}
		summary := summarizeResources("Pharos", h.sampler.sampleWithTable(h.roots, table))
		return resourceHistoryFrame{Active: h.active, Tasks: h.tasks, Scopes: []resourceHistoryScope{{Label: resourceScopeService, Summary: summary}}}
	})
	// What run does before its first tick.
	h.history.running, h.history.fresh, h.history.instance = true, true, "test"
	h.sampler.setStaleGap(resourceHistoryGap)
	return h
}

func (h *historyHarness) advance(d time.Duration, cpu float64) bool {
	h.clock = h.clock.Add(d)
	if usage, ok := h.source.procs[10]; ok {
		usage.CPUSeconds += cpu
		usage.DiskBytes += 1e6
		h.source.set(10, usage)
	}
	return h.history.tick()
}

func (h *historyHarness) doc(t *testing.T, since time.Time) resourceHistoryDocument {
	t.Helper()
	doc, ok := h.history.document("", since)
	if !ok || !doc.Available {
		t.Fatalf("no history: %+v", doc)
	}
	samples := doc.Samples
	for _, column := range []int{len(samples.CPU), len(samples.Memory), len(samples.Disk), len(samples.CPUPartial), len(samples.DiskPartial), len(samples.Processes)} {
		if column != len(samples.T) {
			t.Fatalf("columns must align with sample times: %+v", samples)
		}
	}
	for i := 1; i < len(samples.T); i++ {
		if samples.T[i] <= samples.T[i-1] {
			t.Fatalf("sample times must strictly increase: %v", samples.T)
		}
	}
	return doc
}

// The collector (docs/work-resource-accounting.md): exact rates at the idle
// cadence, no repeated timestamps, explicit gaps, restarts and unreadable
// ticks, and a bounded window.
func TestResourceHistoryRecordsSegmentsAndGaps(t *testing.T) {
	h := newHistoryHarness(t)
	if h.history.tick() {
		t.Fatal("an idle service uses the idle cadence")
	}
	// Ten seconds idle is beyond resourceStaleGap but within the
	// collector's gap, so the idle cadence still yields a rate.
	h.advance(resourceHistoryIdle+time.Second, 0.11)
	// A tick sooner than the sampler interval reuses its reading: no
	// second sample at the same time.
	h.history.tick()
	doc := h.doc(t, time.Time{})
	if len(doc.Samples.T) != 2 || doc.Samples.CPU[0] != nil || doc.Samples.CPU[1] == nil || *doc.Samples.CPU[1] != 1 {
		t.Fatalf("a baseline, then 1%% of one core: %+v", doc.Samples)
	}
	if doc.Samples.Memory[1] == nil || *doc.Samples.Memory[1] != 300e6 || doc.Samples.Disk[1] == nil {
		t.Fatalf("exact memory and disk: %+v", doc.Samples)
	}
	if len(doc.Segments) != 1 || doc.Segments[0].Reason != "started" || doc.Segments[0].T != doc.Samples.T[0] {
		t.Fatalf("one segment from the first sample: %+v", doc.Segments)
	}
	if doc.Scope != resourceScopeService || doc.Series["cpu"].Unit != "percent_one_core" || doc.GPU != nil || !doc.Series["disk"].Available {
		t.Fatalf("scope and series: %+v", doc)
	}
	if len(doc.Coverage) != 1 || len(doc.Coverage[0].Covered) != 1 || doc.Coverage[0].Covered[0].PID != 10 {
		t.Fatalf("coverage names the measured process once: %+v", doc.Coverage)
	}
	if latest, ok := h.history.latest(""); !ok || latest == nil || latest.CPUPercent == nil {
		t.Fatalf("status reuses the collector's reading: %+v %v", latest, ok)
	}

	// Busy CPU keeps the active cadence even without a running operation.
	if !h.advance(resourceHistoryActive, 1) {
		t.Fatal("50% of one core is active")
	}
	h.active = true
	if !h.advance(resourceHistoryActive, 0) {
		t.Fatal("a running operation is active")
	}
	h.active = false

	// The Mac slept: a gap segment and a new baseline, never an average.
	h.advance(time.Minute, 30)
	doc = h.doc(t, time.Time{})
	last := len(doc.Samples.T) - 1
	if doc.Samples.CPU[last] != nil || doc.Segments[len(doc.Segments)-1].Reason != "gap" || doc.Segments[len(doc.Segments)-1].T != doc.Samples.T[last] {
		t.Fatalf("a gap is explicit: %+v %+v", doc.Samples.CPU, doc.Segments)
	}

	// PID reuse: a new process under the same PID is a restart.
	h.source.set(10, processUsage{CPUSeconds: 0.5, Footprint: 100e6, Started: 2})
	h.advance(resourceHistoryIdle, 0)
	doc = h.doc(t, time.Time{})
	if doc.Segments[len(doc.Segments)-1].Reason != "restart" || doc.Samples.CPU[len(doc.Samples.T)-1] != nil {
		t.Fatalf("a restart starts a new segment: %+v", doc.Segments)
	}

	// Nothing readable: an explicit empty sample at the tick's time.
	delete(h.source.procs, 10)
	h.advance(resourceHistoryIdle, 0)
	doc = h.doc(t, time.Time{})
	last = len(doc.Samples.T) - 1
	if doc.Samples.Memory[last] != nil || doc.Samples.CPU[last] != nil || doc.Samples.T[last] != h.clock.UnixMilli() || doc.Samples.Processes[last] != 0 {
		t.Fatalf("an unreadable tick is a null sample: %+v", doc.Samples)
	}
	if latest, ok := h.history.latest(""); !ok || latest != nil {
		t.Fatalf("nothing readable is a nil summary: %+v", latest)
	}

	// since: only newer samples, with the coverage in effect then.
	since := time.UnixMilli(doc.Samples.T[last-1])
	newer := h.doc(t, since)
	if len(newer.Samples.T) != 1 || len(newer.Coverage) == 0 || newer.Coverage[0].T > newer.Samples.T[0] {
		t.Fatalf("since keeps the state at since: %+v", newer)
	}

	// A client passes back the last millisecond t it received; a sample
	// taken within that millisecond is not sent again.
	h.advance(resourceHistoryIdle+700*time.Microsecond, 0.1)
	doc = h.doc(t, time.Time{})
	since = time.UnixMilli(doc.Samples.T[len(doc.Samples.T)-1])
	if again := h.doc(t, since); len(again.Samples.T) != 0 || len(again.Segments) != 0 || len(again.Coverage) != 1 || again.Coverage[0].T > since.UnixMilli() {
		t.Fatalf("the last received sample came back: %+v", again)
	}

	// The window is bounded by time, keeping the coverage in effect at its
	// start; a stopped collector serves nothing.
	override(t, &resourceHistoryWindow, 30*time.Second)
	h.source.set(10, processUsage{CPUSeconds: 1, Footprint: 100e6, Started: 3})
	for range 10 {
		h.advance(resourceHistoryIdle, 0.1)
	}
	doc = h.doc(t, time.Time{})
	if span := time.Duration(doc.Samples.T[len(doc.Samples.T)-1]-doc.Samples.T[0]) * time.Millisecond; span > resourceHistoryWindow || len(doc.Coverage) == 0 || len(doc.Segments) == 0 {
		t.Fatalf("bounded window: %v, %+v", span, doc)
	}
	if data, _ := json.Marshal(doc); len(data) > 64<<10 {
		t.Fatalf("payload %d bytes", len(data))
	}
	h.history.running = false
	if _, ok := h.history.latest(""); ok {
		t.Fatal("a stopped collector gives status no reading")
	}
	if doc, ok := h.history.document("", time.Time{}); ok || doc.Available {
		t.Fatalf("stopped: %+v", doc)
	}
}

// GPU samples keep their own timestamps; an unmeasured GPU value is null,
// not zero; markers come from the running set; CPU integrates per segment.
func TestResourceHistoryGPUMarkersAndIntegration(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	frame := resourceHistoryFrame{}
	h := newResourceHistory(nil, nil, func(time.Time) resourceHistoryFrame { return frame })
	h.now = func() time.Time { return clock }
	h.running, h.fresh = true, true
	summary := func(at time.Time, cpu *float64, gpuAt time.Time, gpu *float64) *resourceSummary {
		return &resourceSummary{Processes: []resourceReading{{PID: 10, Role: "service", cpu: cpu, memory: 1e9, at: at}},
			GPU: &gpuReading{at: gpuAt, percent: gpu, Partial: gpu == nil}}
	}
	tick := func(at time.Time, cpu *float64, gpuAt time.Time, gpu *float64, tasks ...resourceHistoryTask) {
		clock = at
		frame = resourceHistoryFrame{Tasks: tasks, Scopes: []resourceHistoryScope{{Label: resourceScopeApp, Summary: summary(at, cpu, gpuAt, gpu)},
			{Key: "lib", Label: "Library", Summary: summary(at, cpu, gpuAt, gpu)}}}
		h.tick()
	}
	t0 := clock
	task := resourceHistoryTask{ID: "op1", Kind: "analysis.labels", Libraries: []string{"lib"}, Started: t0.Add(-time.Second), RunStarted: t0.Add(-time.Second), State: opRunning}
	tick(t0, nil, t0.Add(-3*time.Second), nil, task)
	tick(t0.Add(2*time.Second), new(50.0), t0.Add(-3*time.Second), nil, task) // same GPU sample
	tick(t0.Add(4*time.Second), new(100.0), t0.Add(3*time.Second), new(40.0), task)
	tick(t0.Add(6*time.Second), new(100.0), t0.Add(3*time.Second), new(40.0))
	doc, ok := h.document("", time.Time{})
	if !ok || doc.GPU == nil || len(doc.GPU.T) != 2 || doc.GPU.T[0] != t0.Add(-3*time.Second).UnixMilli() || doc.GPU.Value[0] != nil || *doc.GPU.Value[1] != 40 {
		t.Fatalf("GPU keeps its own sample times, and unmeasured is null: %+v", doc.GPU)
	}
	if len(doc.Samples.T) != 4 || !doc.Series["gpu"].Available {
		t.Fatalf("CPU samples: %+v", doc.Samples)
	}
	if len(doc.Markers) != 2 || doc.Markers[0].Event != "start" || doc.Markers[0].T != task.RunStarted.UnixMilli() || doc.Markers[1].Event != "end" || !doc.Markers[1].Observed {
		t.Fatalf("start at the task's own time, end when first unseen: %+v", doc.Markers)
	}
	h.mark(resourceHistoryMarker{At: t0.Add(5 * time.Second), Event: "end", ID: "op1", Kind: "analysis.labels", Libraries: []string{"lib"}})
	// The owner can finish after collect reads the registry, before tick
	// records that older running set. It must not resurrect the task.
	tick(t0.Add(7*time.Second), new(100.0), t0.Add(3*time.Second), new(40.0), task)
	if doc, _ := h.document("lib", time.Time{}); len(doc.Markers) != 2 || doc.Markers[1].Observed || doc.Markers[1].T != t0.Add(5*time.Second).UnixMilli() {
		t.Fatalf("an exact end replaces the observed one, and library scopes keep their markers: %+v", doc.Markers)
	}
	if doc, _ := h.document("", time.Time{}); len(doc.Scopes) != 1 || doc.Scopes[0].Key != "lib" {
		t.Fatalf("the whole scope lists its subsets: %+v", doc.Scopes)
	}
	if _, ok := h.document("other", time.Time{}); ok {
		t.Fatal("an unknown scope is unavailable")
	}
	seconds, coverage, _ := h.cpuSeconds("", t0, t0.Add(6*time.Second))
	if seconds != 5 || coverage != 1 {
		t.Fatalf("0.5×2 + 1×2 + 1×2 = 5 CPU seconds over full coverage: %v %v", seconds, coverage)
	}
	if seconds, coverage, _ := h.cpuSeconds("", t0.Add(-4*time.Second), t0.Add(2*time.Second)); seconds != 1 || coverage <= 0.3 || coverage >= 0.4 {
		t.Fatalf("time before the first rate is uncovered: %v %v", seconds, coverage)
	}
}

// Without a collector (classic execution) the route answers, unavailable.
func TestResourceHistoryRouteWithoutCollector(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveResourceHistory(recorder, httptest.NewRequest("GET", "/api/resources/history", nil), nil, "")
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"available":false`) {
		t.Fatalf("%d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	serveResourceHistory(recorder, httptest.NewRequest("GET", "/api/resources/history?since=yesterday", nil), nil, "")
	if recorder.Code != 400 {
		t.Fatalf("a bad since is refused: %d", recorder.Code)
	}
	if at, err := parseHistorySince("1762516800000"); err != nil || at.UnixMilli() != 1762516800000 {
		t.Fatalf("Unix milliseconds: %v %v", at, err)
	}
}

// run collects at once, stops with its context and restores the sampler's
// stale gap; a second run while one is going returns immediately.
func TestResourceHistoryRunLoop(t *testing.T) {
	sampler := newResourceSampler()
	ticks := make(chan struct{}, 8)
	h := newResourceHistory(sampler, nil, func(time.Time) resourceHistoryFrame {
		select {
		case ticks <- struct{}{}:
		default:
		}
		return resourceHistoryFrame{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.run(ctx); close(done) }()
	<-ticks
	h.run(ctx) // already running
	sampler.mu.Lock()
	gap := sampler.gap()
	sampler.mu.Unlock()
	if gap != resourceHistoryGap {
		t.Fatalf("stale gap while collecting: %v", gap)
	}
	cancel()
	<-done
	sampler.mu.Lock()
	gap = sampler.gap()
	sampler.mu.Unlock()
	if gap != resourceStaleGap {
		t.Fatalf("classic stale gap restored: %v", gap)
	}
}

// History times are wall times: Go's monotonic clock pauses while the Mac
// sleeps, so a monotonic comparison would hide the gap and keep pre-sleep
// samples in the window.
func TestResourceHistoryUsesWallTime(t *testing.T) {
	reading := time.Now()
	h := newResourceHistory(nil, nil, func(time.Time) resourceHistoryFrame {
		return resourceHistoryFrame{
			Tasks: []resourceHistoryTask{{ID: "op1", State: opRunning, RunStarted: time.Now().Add(-time.Second)}},
			Scopes: []resourceHistoryScope{{Summary: &resourceSummary{Processes: []resourceReading{{PID: 10, Role: "service", memory: 1, at: reading}},
				GPU: &gpuReading{at: reading, percent: new(1.0)}}}}}
	})
	h.running, h.fresh = true, true
	h.tick()
	h.mark(resourceHistoryMarker{At: time.Now(), Event: "end", ID: "op1"})
	wall := func(at time.Time) bool { return at == at.Round(0) }
	series := h.scopes[""]
	if !wall(h.lastTick) || !wall(series.points[0].at) || !wall(series.gpu[0].at) || !wall(series.segments[0].at) {
		t.Fatalf("stored times carry a monotonic reading: %v %+v", h.lastTick, series)
	}
	for _, marker := range h.markers {
		if !wall(marker.At) {
			t.Fatalf("marker time carries a monotonic reading: %+v", marker)
		}
	}
}

// A task list that could not be read leaves its library's tasks as they were:
// a 100 ms timeout is not an end and a restart. Queued work gets no marker;
// it starts when first seen running, and a GPU sampler that never read a
// value gives no GPU row.
func TestResourceHistoryTaskStatesAndUnknownLibraries(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	frame := resourceHistoryFrame{}
	h := newResourceHistory(nil, nil, func(time.Time) resourceHistoryFrame { return frame })
	h.now = func() time.Time { return clock }
	h.running, h.fresh = true, true
	scopes := []resourceHistoryScope{{Summary: &resourceSummary{Processes: []resourceReading{{PID: 10, Role: "broker", memory: 1, at: clock}},
		GPU: &gpuReading{at: clock, Partial: true}}}}
	tick := func(unknown []string, tasks ...resourceHistoryTask) {
		clock = clock.Add(2 * time.Second)
		scopes[0].Summary.Processes[0].at = clock
		frame = resourceHistoryFrame{Scopes: scopes, Tasks: tasks, UnknownLibraries: unknown}
		h.tick()
	}
	t0 := clock
	service := resourceHistoryTask{ID: "svc", Kind: "backup", State: opRunning, Libraries: []string{"lib"}, RunStarted: t0.Add(-time.Minute), source: "lib"}
	queued := resourceHistoryTask{ID: "q", Kind: "analysis.labels", State: opQueued, Started: t0}
	tick(nil, service, queued)
	tick([]string{"lib"}, queued) // the service missed the deadline
	tick(nil, service, queued)
	tick(nil, service, func() resourceHistoryTask { q := queued; q.State = opRunning; return q }())
	running := clock
	doc, _ := h.document("", time.Time{})
	events := []string{}
	for _, marker := range doc.Markers {
		events = append(events, marker.ID+":"+marker.Event)
	}
	if strings.Join(events, ",") != "svc:start,q:start" {
		t.Fatalf("one start per task, no end for an unknown library, none while queued: %v", events)
	}
	if doc.Markers[0].T != t0.Add(-time.Minute).UnixMilli() || doc.Markers[0].Observed || doc.Markers[1].T != running.UnixMilli() || !doc.Markers[1].Observed {
		t.Fatalf("a known run start is exact; otherwise the first tick that saw it running: %+v", doc.Markers)
	}
	// Blocked after starting is still the same task, not an end.
	tick(nil, service, func() resourceHistoryTask { q := queued; q.State = opBlocked; return q }())
	// A library whose list is read again without the task: it ended.
	tick(nil)
	doc, _ = h.document("", time.Time{})
	if len(doc.Markers) != 4 || doc.Markers[2].Event != "end" || doc.Markers[3].Event != "end" {
		t.Fatalf("ends once no longer reported: %+v", doc.Markers)
	}
	if doc.GPU != nil || doc.Series["gpu"].Available {
		t.Fatalf("no GPU value was ever read, so no GPU row: %+v %+v", doc.GPU, doc.Series["gpu"])
	}
	// A task can first be reported blocked, then running with an earlier
	// exact start. An event-time sample cursor must not hide that start.
	late := resourceHistoryTask{ID: "late", Kind: "analysis.labels", State: opBlocked, RunStarted: t0.Add(time.Second)}
	tick(nil, late)
	since := clock
	late.State = opRunning
	tick(nil, late)
	delta, _ := h.document("", since)
	if !delta.MarkersComplete {
		t.Fatal("incremental readings need an authoritative bounded marker snapshot")
	}
	found := false
	for _, marker := range delta.Markers {
		if marker.ID == late.ID && marker.Event == "start" {
			found = marker.T == late.RunStarted.UnixMilli() && marker.T < since.UnixMilli()
		}
	}
	if !found {
		t.Fatalf("late learned start was omitted behind the sample cursor: %+v", delta.Markers)
	}

	// A start marked while a frame is collected is newer than the frame's
	// task list: no observed end before it, and no second start.
	during := func() {
		clock = clock.Add(time.Millisecond)
		h.mark(resourceHistoryMarker{At: clock, Event: "start", ID: "racing", Kind: "backup", Libraries: []string{"lib"}})
	}
	h.collect = func(time.Time) resourceHistoryFrame { during(); during = func() {}; return frame }
	tick(nil)
	racing := resourceHistoryTask{ID: "racing", Kind: "backup", State: opRunning, Libraries: []string{"lib"}}
	tick(nil, racing)
	doc, _ = h.document("", time.Time{})
	events = []string{}
	for _, marker := range doc.Markers {
		if marker.ID == "racing" {
			events = append(events, marker.Event)
		}
	}
	if strings.Join(events, ",") != "start" {
		t.Fatalf("a start marked during collection was ended or restarted: %v", events)
	}

	// A registry's start mark names its operation's libraries, which the
	// broker's library scopes filter markers by.
	reg := newOpRegistry("broker")
	reg.resourceHistory = h
	_, op := reg.begin(context.Background(), OperationSpec{Kind: "analysis.labels", Libraries: []string{"lib"}})
	h.scopes["lib"] = &resourceSeries{label: "lib"}
	if lib, _ := h.document("lib", time.Time{}); !slices.ContainsFunc(lib.Markers, func(m resourceHistoryMarkerView) bool { return m.ID == string(op.ID()) }) {
		t.Fatalf("a top-level operation is missing from its library's history: %+v", lib.Markers)
	}
	op.Finish(nil)

	// A service that stops answering carries its tasks for a bounded time,
	// then they end, so the cadence can fall back to idle.
	override(t, &resourceTaskCarry, 5*time.Second)
	wedged := resourceHistoryTask{ID: "wedged", Kind: "backup", State: opRunning, RunStarted: clock, Libraries: []string{"lib"}, source: "lib"}
	tick(nil, wedged)
	tick([]string{"lib"})
	tick([]string{"lib"})
	if _, carried := h.tasks["wedged"]; !carried {
		t.Fatal("an unknown library's task ended within the carry bound")
	}
	tick([]string{"lib"})
	if _, carried := h.tasks["wedged"]; carried || h.active {
		t.Fatalf("an unanswered library kept its task past the carry bound: active %v", h.active)
	}
	// Recovery disproves the inferred end; retain the original start.
	tick(nil, wedged)
	doc, _ = h.document("", time.Time{})
	var recovered []resourceHistoryMarkerView
	for _, marker := range doc.Markers {
		if marker.ID == wedged.ID {
			recovered = append(recovered, marker)
		}
	}
	if len(recovered) != 1 || recovered[0].Event != "start" || recovered[0].Observed || recovered[0].T != wedged.RunStarted.UnixMilli() {
		t.Fatalf("recovery must keep the original start and retract the inferred end: %+v", recovered)
	}
}

// The broker asks every library's service at once under one deadline, after
// the lease probes: a slow first library cannot leave the same later ones
// unknown on every tick.
func TestBrokerResourceTasksShareTheDeadline(t *testing.T) {
	override(t, &resourceTaskDeadline, 200*time.Millisecond)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	answering := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"tasks": []resourceHistoryTask{{ID: id, Kind: "backup", State: opRunning}}}, http.StatusOK)
		}))
	}
	b1, b2 := answering("b1"), answering("b2")
	defer b1.Close()
	defer b2.Close()
	b := &brokerOperations{reg: newOpRegistry("broker"), client: &http.Client{}}
	for _, server := range []*httptest.Server{slow, b1, b2} {
		b.libraries = append(b.libraries, brokerLibrary{ID: server.URL, Name: server.URL, Child: &applicationEndpoint{URL: server.URL}})
	}
	frame := b.collectResourceHistory(time.Now())
	if len(frame.UnknownLibraries) != 1 || frame.UnknownLibraries[0] != slow.URL {
		t.Fatalf("only the slow library is unknown: %v", frame.UnknownLibraries)
	}
	ids := []string{}
	for _, task := range frame.Tasks {
		ids = append(ids, task.ID+"@"+task.source)
	}
	slices.Sort(ids)
	if strings.Join(ids, ",") != "b1@"+b1.URL+",b2@"+b2.URL || !frame.Active {
		t.Fatalf("each answering library's tasks, attributed to it: %v", ids)
	}
}
