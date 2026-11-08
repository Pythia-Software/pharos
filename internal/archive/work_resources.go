package archive

import (
	"context"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Task resource accounting (docs/work-resource-accounting.md): what one task
// can honestly claim about the resources it used, beside the allocation it
// was granted. Every quantity carries its unit and basis:
//
//	measured    read directly for this task (a dedicated helper's counters)
//	counted     counted by the task's own code (logical bytes, units, buffers)
//	sampled     from overlapping samples (GPU while the task alone owned it)
//	allocated   a grant, never evidence of use
//	shared      real usage that other work shares; no per-task figure
//	unavailable not measured
//
// Go work runs in the shared service process with other tasks, interactive
// queries and the runtime, so its CPU and memory are shared: a process delta
// over a task's interval is context (cpuSeconds in resources_history.go),
// not the task's figure, and is never divided among tasks.
//
// Worker paths only touch atomics. Nothing here takes the registry lock,
// writes the journal or calls the OS per message. Helper counters are read
// once at admission and once at release.

const (
	basisMeasured    = "measured"
	basisCounted     = "counted"
	basisSampled     = "sampled"
	basisAllocated   = "allocated"
	basisShared      = "shared"
	basisUnavailable = "unavailable"
)

// Waits a task can spend before or between its admitted work.
const (
	waitQueue      = "queue"
	waitDependency = "dependency"
	waitCapacity   = "capacity"
	waitWriter     = "writer"
	waitPower      = "power"
	waitLock       = "lock"
)

var taskWaitKinds = []string{waitQueue, waitDependency, waitCapacity, waitWriter, waitPower, waitLock}

// resourceQuantity is one figure with its unit, basis and coverage. A nil
// Value is unknown, which is different from zero.
type resourceQuantity struct {
	Value    *float64 `json:"value"`
	Unit     string   `json:"unit"`
	Basis    string   `json:"basis"`
	Coverage string   `json:"coverage,omitempty"`
}

func quantity(value float64, unit, basis, coverage string) resourceQuantity {
	return resourceQuantity{Value: &value, Unit: unit, Basis: basis, Coverage: coverage}
}

func unknownQuantity(unit, basis, coverage string) resourceQuantity {
	return resourceQuantity{Unit: unit, Basis: basis, Coverage: coverage}
}

// taskIdentity names a task for metrics, markers and profile labels.
type taskIdentity struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Root     string `json:"root,omitempty"`
	Batch    string `json:"batch,omitempty"`
	Model    string `json:"model,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Priority string `json:"priority,omitempty"`
	Trigger  string `json:"trigger,omitempty"`
}

// taskAllocation is a grant: what the task may use, not what it used.
type taskAllocation struct {
	CPUSlots            int    `json:"cpu_slots"`
	GPU                 int    `json:"gpu"`
	MemoryEstimateBytes uint64 `json:"memory_estimate_bytes"`
}

// taskHelperObservation is one helper's usage over an ownership interval.
// A native worker can report device timings through observeHelper later.
type taskHelperObservation struct {
	PID        int
	Role       string
	From, To   time.Time
	CPUSeconds *float64
	// FootprintBytes is the reader's memory figure at the end of the interval,
	// not a task peak. CounterBasis names the reader (physical footprint only
	// for the kernel reader); availability is independent of CPU.
	FootprintBytes *uint64
	GPUSeconds     *float64
	CounterBasis   string
	// Exclusive is true when no other task used the helper in the interval.
	Exclusive   bool
	Basis, Note string
}

// taskResources is one task's live record. Methods are safe on nil.
type taskResources struct {
	identity taskIdentity
	clock    func() time.Time
	started  time.Time

	// Counters, updated on worker paths.
	logicalRead, logicalWritten, temporary atomic.Int64
	buffers, buffersPeak                   atomic.Int64
	tracked                                atomic.Uint32   // read, written, temporary, owned buffers
	waits                                  [6]atomic.Int64 // nanoseconds, by taskWaitKinds index

	mu         sync.Mutex
	activeN    int
	activeFrom time.Time
	activeSum  time.Duration
	// activeTracked is true once any span opened: without one, active time
	// is unmeasured (the task runs on a path that does not open spans), not 0.
	activeTracked bool
	// grants are the open allocations; concurrent admissions of one task
	// add up.
	grants    map[int]taskAllocation
	grantID   int
	allocFrom time.Time
	// slotSeconds integrates the CPU grant over time.
	slotSeconds float64
	peakSlots   int
	units       map[string]taskUnits
	helpers     []taskHelperObservation
	gpuSeconds  *float64
	gpuBasis    string
	gpuCoverage string
	ended       time.Time
	state       string
}

type taskUnits struct {
	Unit  string  `json:"unit"`
	Done  float64 `json:"done"`
	Total float64 `json:"total"`
}

func newTaskResources(identity taskIdentity, clock func() time.Time) *taskResources {
	if clock == nil {
		clock = time.Now
	}
	return &taskResources{identity: identity, clock: clock, started: clock(), units: map[string]taskUnits{}, grants: map[int]taskAllocation{}}
}

type taskResourcesKey struct{}

// withTaskResources makes r reachable from domain code through ctx.
func withTaskResources(ctx context.Context, r *taskResources) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, taskResourcesKey{}, r)
}

// taskResourcesFrom is ctx's task record, or nil (every method accepts nil).
func taskResourcesFrom(ctx context.Context) *taskResources {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(taskResourcesKey{}).(*taskResources)
	return r
}

// allocate records a grant until the returned function runs. Concurrent
// grants of one task add up. release ends every open grant (the task is done).
func (r *taskResources) allocate(grant taskAllocation) func() {
	if r == nil {
		return func() {}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeAllocation(r.clock())
	r.grantID++
	id := r.grantID
	r.grants[id] = grant
	r.peakSlots = max(r.peakSlots, r.current().CPUSlots)
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if _, open := r.grants[id]; open {
				r.closeAllocation(r.clock())
				delete(r.grants, id)
			}
		})
	}
}

func (r *taskResources) release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeAllocation(r.clock())
	clear(r.grants)
}

// current is the sum of the open grants. Callers hold r.mu.
func (r *taskResources) current() taskAllocation {
	total := taskAllocation{}
	for _, grant := range r.grants {
		total.CPUSlots += grant.CPUSlots
		total.GPU += grant.GPU
		total.MemoryEstimateBytes += grant.MemoryEstimateBytes
	}
	return total
}

func (r *taskResources) closeAllocation(now time.Time) {
	if slots := r.current().CPUSlots; slots > 0 && now.After(r.allocFrom) {
		r.slotSeconds += float64(slots) * now.Sub(r.allocFrom).Seconds()
	}
	r.allocFrom = now
}

// activeSpan marks the task as doing admitted work until the returned
// function is called. Spans may nest or overlap (parallel workers): active
// time is the wall time with at least one open.
func (r *taskResources) activeSpan() func() {
	if r == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.activeN == 0 {
		r.activeFrom = r.clock()
	}
	r.activeN++
	r.activeTracked = true
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.activeN--
			if r.activeN == 0 {
				r.activeSum += r.clock().Sub(r.activeFrom)
			}
			r.mu.Unlock()
		})
	}
}

// waitSpan records time waiting for kind until the returned function runs.
func (r *taskResources) waitSpan(kind string) func() {
	if r == nil {
		return func() {}
	}
	index := slices.Index(taskWaitKinds, kind)
	if index < 0 {
		return func() {}
	}
	from := r.clock()
	var once sync.Once
	return func() { once.Do(func() { r.waits[index].Add(int64(r.clock().Sub(from))) }) }
}

// progress mirrors reportWork: cumulative done and total for a phase.
func (r *taskResources) progress(phase, unit string, done, total float64) {
	if r == nil || phase == "" {
		return
	}
	r.mu.Lock()
	units := r.units[phase]
	units.Unit = unit
	units.Done = max(units.Done, done)
	units.Total = total
	r.units[phase] = units
	r.mu.Unlock()
}

// addRead and addWritten count logical bytes the task's code read and wrote
// (rows, files, blobs): not OS disk traffic, which caches and SQLite defer.
func (r *taskResources) addRead(bytes int64) {
	if r != nil && bytes >= 0 {
		r.tracked.Or(1)
		r.logicalRead.Add(bytes)
	}
}

func (r *taskResources) addWritten(bytes int64) {
	if r != nil && bytes >= 0 {
		r.tracked.Or(2)
		r.logicalWritten.Add(bytes)
	}
}

func (r *taskResources) addTemporary(bytes int64) {
	if r != nil && bytes >= 0 {
		r.tracked.Or(4)
		r.temporary.Add(bytes)
	}
}

// holdBuffer counts a working buffer the task owns until the returned
// function runs, for its current and peak owned memory.
func (r *taskResources) holdBuffer(bytes int64) func() {
	if r == nil || bytes <= 0 {
		return func() {}
	}
	r.tracked.Or(8)
	current := r.buffers.Add(bytes)
	for peak := r.buffersPeak.Load(); current > peak && !r.buffersPeak.CompareAndSwap(peak, current); peak = r.buffersPeak.Load() {
	}
	var once sync.Once
	return func() { once.Do(func() { r.buffers.Add(-bytes) }) }
}

// taskHelperScope is an open helper ownership interval.
type taskHelperScope struct {
	r         *taskResources
	pid       int
	role      string
	exclusive bool
	from      time.Time
	start     processUsage
	startErr  error
	read      func(int) (processUsage, error)
	once      sync.Once
}

// helperStart opens an ownership interval on a helper process (a native
// model worker) with one counter read; end closes it with another. Its CPU
// is measured only if the task owned the helper exclusively throughout.
func (r *taskResources) helperStart(pid int, role string, exclusive bool) *taskHelperScope {
	return r.helperStartWith(pid, role, exclusive, processUsageOf)
}

func (r *taskResources) helperStartWith(pid int, role string, exclusive bool, read func(int) (processUsage, error)) *taskHelperScope {
	if r == nil || pid <= 0 {
		return nil
	}
	scope := &taskHelperScope{r: r, pid: pid, role: role, exclusive: exclusive, from: r.clock(), read: read}
	scope.start, scope.startErr = read(pid)
	return scope
}

// shared marks the interval as shared with another task (a second user
// joined): its CPU is then reported as shared.
func (s *taskHelperScope) shared() {
	if s != nil {
		s.r.mu.Lock()
		s.exclusive = false
		s.r.mu.Unlock()
	}
}

func (s *taskHelperScope) end() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		finish, err := s.read(s.pid)
		s.r.mu.Lock()
		exclusive := s.exclusive
		s.r.mu.Unlock()
		observation := taskHelperObservation{PID: s.pid, Role: s.role, From: s.from, To: s.r.clock(), Exclusive: exclusive, CounterBasis: resourceBasis}
		switch {
		case s.startErr != nil || err != nil:
			observation.Basis, observation.Note = basisUnavailable, "The helper's counters could not be read."
		case finish.Started == 0 || s.start.Started == 0:
			observation.Basis, observation.Note = basisUnavailable, "The helper's process identity could not be verified."
		case finish.Started != s.start.Started:
			observation.Basis, observation.Note = basisUnavailable, "The helper was replaced during the interval."
		default:
			observation.Basis = basisMeasured
			if !exclusive {
				observation.Basis, observation.Note = basisShared, "Other tasks used the helper in the same interval."
			}
			if s.start.Known.has(counterOwnCPU) && finish.Known.has(counterOwnCPU) && finish.CPUSeconds >= s.start.CPUSeconds {
				observation.CPUSeconds = new(finish.CPUSeconds - s.start.CPUSeconds)
			} else {
				observation.Note += " CPU counters were unavailable or reset during the interval."
			}
			if finish.Known.has(counterMemory) {
				observation.FootprintBytes = new(finish.Footprint)
			}
		}
		s.r.observeHelper(observation)
	})
}

// observeHelper records a helper interval measured elsewhere (a native
// worker's own report).
func (r *taskResources) observeHelper(observation taskHelperObservation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.helpers = append(r.helpers, observation)
	if len(r.helpers) > 64 {
		r.helpers = slices.Delete(r.helpers, 0, len(r.helpers)-64)
	}
	r.mu.Unlock()
}

// observeGPU records GPU time from device events or exclusive samples.
func (r *taskResources) observeGPU(seconds float64, basis, coverage string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	total := seconds
	if r.gpuSeconds != nil {
		total += *r.gpuSeconds
	}
	r.gpuSeconds, r.gpuBasis, r.gpuCoverage = &total, basis, coverage
	r.mu.Unlock()
}

// taskResourceView is the accounting record the API, UI, journal and exports
// share. Allocation is a separate field from usage.
type taskResourceView struct {
	Version    int                         `json:"version"`
	Task       taskIdentity                `json:"task"`
	State      string                      `json:"state,omitempty"`
	StartedAt  string                      `json:"started_at"`
	EndedAt    string                      `json:"ended_at,omitempty"`
	Allocation *taskAllocationView         `json:"allocation"`
	Time       map[string]resourceQuantity `json:"time"`
	Waits      map[string]resourceQuantity `json:"waits"`
	Work       map[string]taskUnits        `json:"work"`
	CPU        map[string]resourceQuantity `json:"cpu"`
	Memory     map[string]resourceQuantity `json:"memory"`
	GPU        map[string]resourceQuantity `json:"gpu"`
	IO         map[string]resourceQuantity `json:"io"`
	Helpers    []taskHelperView            `json:"helpers"`
	// Context is the observed process tree over the task's interval:
	// shared with other work, never this task's figure.
	Context map[string]resourceQuantity `json:"context,omitempty"`
}

type taskAllocationView struct {
	Current     *taskAllocation  `json:"current"`
	SlotSeconds resourceQuantity `json:"slot_seconds"`
	PeakSlots   int              `json:"peak_slots"`
}

type taskHelperView struct {
	CounterBasis string           `json:"counter_basis,omitempty"`
	PID          int              `json:"pid"`
	Role         string           `json:"role"`
	From         string           `json:"from"`
	To           string           `json:"to"`
	CPU          resourceQuantity `json:"cpu_seconds"`
	Footprint    resourceQuantity `json:"footprint_bytes"`
	GPU          resourceQuantity `json:"gpu_seconds"`
	Note         string           `json:"note,omitempty"`
}

const (
	sharedGoCPU    = "Go work shares the service process with other tasks, interactive queries and the runtime; see the process history for the total."
	sharedGoMemory = "Go heap, SQLite and caches are shared by the service process; only owned buffers are counted per task."
	noGPUCounter   = "No device timing or exclusive GPU sample for this task."
	logicalIONote  = "Instrumented logical bytes only; coverage is partial and OS disk traffic is in the process history."
)

// view is the live or final record at now.
func (r *taskResources) view(now time.Time) taskResourceView {
	if r == nil {
		return taskResourceView{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	end := now
	if !r.ended.IsZero() {
		end = r.ended
	}
	active := r.activeSum
	if r.activeN > 0 {
		active += end.Sub(r.activeFrom)
	}
	slotSeconds := r.slotSeconds
	if slots := r.current().CPUSlots; slots > 0 && end.After(r.allocFrom) {
		slotSeconds += float64(slots) * end.Sub(r.allocFrom).Seconds()
	}
	activeTime := unknownQuantity("seconds", basisUnavailable, "This task's work does not record when it runs.")
	if r.activeTracked {
		activeTime = quantity(active.Seconds(), "seconds", basisMeasured, "Wall time with admitted work running.")
	}
	v := taskResourceView{Version: 1, Task: r.identity, State: r.state, StartedAt: formatTime(r.started),
		Allocation: &taskAllocationView{SlotSeconds: quantity(slotSeconds, "slot_seconds", basisAllocated, "CPU slots granted × time; not CPU used."), PeakSlots: r.peakSlots},
		Time: map[string]resourceQuantity{
			"elapsed": quantity(end.Sub(r.started).Seconds(), "seconds", basisMeasured, ""),
			"active":  activeTime,
		},
		Waits: map[string]resourceQuantity{}, Work: map[string]taskUnits{}, Helpers: []taskHelperView{},
		IO: map[string]resourceQuantity{
			"logical_read_bytes":    quantity(float64(r.logicalRead.Load()), "bytes", basisCounted, logicalIONote),
			"logical_written_bytes": quantity(float64(r.logicalWritten.Load()), "bytes", basisCounted, logicalIONote),
			"temporary_bytes":       quantity(float64(r.temporary.Load()), "bytes", basisCounted, ""),
		},
		Memory: map[string]resourceQuantity{
			"owned_buffers_bytes":      quantity(float64(r.buffers.Load()), "bytes", basisCounted, "Working buffers the task tracks."),
			"owned_buffers_peak_bytes": quantity(float64(r.buffersPeak.Load()), "bytes", basisCounted, "Working buffers the task tracks."),
			"process":                  unknownQuantity("bytes", basisShared, sharedGoMemory),
		},
	}
	if !r.ended.IsZero() {
		v.EndedAt = formatTime(r.ended)
	}
	tracked := r.tracked.Load()
	for index, key := range []string{"logical_read_bytes", "logical_written_bytes", "temporary_bytes"} {
		if tracked&(1<<index) == 0 {
			v.IO[key] = unknownQuantity("bytes", basisUnavailable, "This task has no counter for these bytes.")
		}
	}
	if tracked&8 == 0 {
		for _, key := range []string{"owned_buffers_bytes", "owned_buffers_peak_bytes"} {
			v.Memory[key] = unknownQuantity("bytes", basisUnavailable, "This task does not track its owned buffers.")
		}
	}
	if len(r.grants) > 0 {
		current := r.current()
		v.Allocation.Current = &current
	}
	for index, kind := range taskWaitKinds {
		if nanos := r.waits[index].Load(); nanos > 0 {
			v.Waits[kind] = quantity(time.Duration(nanos).Seconds(), "seconds", basisMeasured, "")
		}
	}
	for phase, units := range r.units {
		v.Work[phase] = units
	}
	helperCPU, helperGPU, measured, shared, unavailable := 0.0, 0.0, 0, 0, 0
	gpuMeasured := false
	for _, helper := range r.helpers {
		entry := taskHelperView{PID: helper.PID, Role: helper.Role, From: formatTime(helper.From), To: formatTime(helper.To), Note: helper.Note, CounterBasis: helper.CounterBasis,
			CPU: unknownQuantity("cpu_seconds", basisUnavailable, ""), Footprint: unknownQuantity("bytes", basisUnavailable, ""), GPU: unknownQuantity("gpu_seconds", basisUnavailable, "")}
		if helper.CPUSeconds != nil {
			entry.CPU = quantity(*helper.CPUSeconds, "cpu_seconds", helper.Basis, "")
			if helper.Basis == basisMeasured {
				helperCPU += *helper.CPUSeconds
			}
		}
		if helper.FootprintBytes != nil {
			entry.Footprint = quantity(float64(*helper.FootprintBytes), "bytes", helper.Basis, "End-of-interval memory reading; not a task memory peak.")
		}
		if helper.GPUSeconds != nil {
			entry.GPU = quantity(*helper.GPUSeconds, "gpu_seconds", helper.Basis, "")
			if helper.Basis == basisMeasured || helper.Basis == basisSampled {
				helperGPU += *helper.GPUSeconds
				gpuMeasured = true
			}
		}
		switch {
		case helper.CPUSeconds == nil:
			unavailable++
		case helper.Basis == basisMeasured:
			measured++
		case helper.Basis == basisShared:
			shared++
		default:
			unavailable++
		}
		v.Helpers = append(v.Helpers, entry)
	}
	v.CPU = map[string]resourceQuantity{"go_process": unknownQuantity("cpu_seconds", basisShared, sharedGoCPU)}
	switch {
	case measured > 0:
		coverage := "Dedicated helpers over their ownership intervals."
		if shared+unavailable > 0 {
			coverage = "Some helper intervals were shared or unreadable; this is a lower bound."
		}
		v.CPU["helpers"] = quantity(helperCPU, "cpu_seconds", basisMeasured, coverage)
	case shared > 0:
		v.CPU["helpers"] = unknownQuantity("cpu_seconds", basisShared, "Helpers served other tasks in the same intervals.")
	case unavailable > 0:
		v.CPU["helpers"] = unknownQuantity("cpu_seconds", basisUnavailable, "Helper counters could not be read.")
	}
	switch {
	case r.gpuSeconds != nil:
		v.GPU = map[string]resourceQuantity{"time": quantity(*r.gpuSeconds, "gpu_seconds", r.gpuBasis, r.gpuCoverage)}
	case gpuMeasured:
		v.GPU = map[string]resourceQuantity{"time": quantity(helperGPU, "gpu_seconds", basisMeasured, "From helper reports.")}
	default:
		v.GPU = map[string]resourceQuantity{"time": unknownQuantity("gpu_seconds", basisUnavailable, noGPUCounter)}
	}
	return v
}

// finish ends the record. With a history, the process tree's CPU over the
// task's interval is added as shared context, with its sample coverage.
func (r *taskResources) finish(state string, history *resourceHistory, key string) taskResourceView {
	if r == nil {
		return taskResourceView{}
	}
	r.mu.Lock()
	if r.ended.IsZero() {
		r.ended = r.clock()
		if r.activeN > 0 {
			r.activeSum += r.ended.Sub(r.activeFrom)
			r.activeN = 0
		}
		r.closeAllocation(r.ended)
		clear(r.grants)
	}
	r.state = state
	started, ended := r.started, r.ended
	r.mu.Unlock()
	view := r.view(ended)
	if history != nil {
		if seconds, coverage, partial := history.cpuSeconds(key, started, ended); coverage > 0 {
			note := "The whole covered process tree during this task's interval, shared with any other work. "
			if partial {
				note += "Some samples were lower bounds. "
			}
			note += percentCoverage(coverage)
			view.Context = map[string]resourceQuantity{"process_tree_cpu_seconds": quantity(seconds, "cpu_seconds", basisSampled, note)}
		}
	}
	return view
}

func percentCoverage(coverage float64) string {
	return "Sampled " + compactPercent(coverage) + " of the interval."
}

func compactPercent(fraction float64) string {
	value := min(max(fraction, 0), 1) * 100
	if value >= 99.5 {
		return "100%"
	}
	return strconv.FormatFloat(value, 'f', 0, 64) + "%"
}

// compact is the record for detail_json, written once when the task ends:
// non-null values only, so old binaries reading the journal ignore it.
func (v taskResourceView) compact() map[string]any {
	out := map[string]any{"version": v.Version, "basis_version": 1}
	if v.Allocation != nil {
		out["allocation"] = map[string]any{"slot_seconds": v.Allocation.SlotSeconds.Value, "peak_slots": v.Allocation.PeakSlots}
	}
	section := func(name string, values map[string]resourceQuantity) {
		kept := map[string]any{}
		for key, value := range values {
			if value.Value != nil {
				kept[key] = map[string]any{"value": *value.Value, "unit": value.Unit, "basis": value.Basis}
			} else {
				kept[key] = map[string]any{"unit": value.Unit, "basis": value.Basis}
			}
		}
		if len(kept) > 0 {
			out[name] = kept
		}
	}
	section("time", v.Time)
	section("waits", v.Waits)
	section("cpu", v.CPU)
	section("memory", v.Memory)
	section("gpu", v.GPU)
	section("io", v.IO)
	section("context", v.Context)
	if len(v.Work) > 0 {
		out["work"] = v.Work
	}
	return out
}

// taskResourceLedger keeps the last finished summaries, per kind and in all:
// bounded memory for the task-kind comparison and exports. It is disposable
// diagnostic history, never proof of readiness.
type taskResourceLedger struct {
	mu      sync.Mutex
	perKind int
	total   int
	entries []taskResourceView
}

func newTaskResourceLedger(perKind, total int) *taskResourceLedger {
	return &taskResourceLedger{perKind: max(1, perKind), total: max(1, total)}
}

func (l *taskResourceLedger) record(view taskResourceView) {
	if l == nil || view.Version == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, view)
	count := 0
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].Task.Kind != view.Task.Kind {
			continue
		}
		if count++; count > l.perKind {
			l.entries = slices.Delete(l.entries, i, i+1)
		}
	}
	if extra := len(l.entries) - l.total; extra > 0 {
		l.entries = slices.Delete(l.entries, 0, extra)
	}
}

// taskResourceExport is the export document: summaries with their units and
// bases, and no transcript content.
type taskResourceExport struct {
	Version     int                `json:"version"`
	GeneratedAt string             `json:"generated_at"`
	Kind        string             `json:"kind,omitempty"`
	Bases       map[string]string  `json:"bases"`
	Tasks       []taskResourceView `json:"tasks"`
}

var taskResourceBases = map[string]string{
	basisMeasured:    "Read directly for this task, such as a dedicated helper's counters.",
	basisCounted:     "Counted by the task's own code.",
	basisSampled:     "From overlapping samples; the coverage says how much of the interval.",
	basisAllocated:   "A grant, not evidence of use.",
	basisShared:      "Real usage shared with other work; no per-task figure.",
	basisUnavailable: "Not measured.",
}

// export lists the summaries (of one kind, or all when kind is ""), newest
// last.
func (l *taskResourceLedger) export(kind string, now time.Time) taskResourceExport {
	out := taskResourceExport{Version: 1, GeneratedAt: formatTime(now), Kind: kind, Bases: taskResourceBases, Tasks: []taskResourceView{}}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if kind == "" || entry.Task.Kind == kind {
			out.Tasks = append(out.Tasks, entry)
		}
	}
	return out
}

// taskProfileLabelKeys are the labels CPU profiles carry for task work.
var taskProfileLabelKeys = []string{"op_kind", "op_id", "root", "batch"}

// taskProfileLabels labels a task's goroutines for the bounded diagnostic
// profile. Labels follow ctx into goroutines started under pprof.Do; pooled
// workers need them set where they take the task's work.
func taskProfileLabels(identity taskIdentity) pprof.LabelSet {
	values := []string{}
	for _, pair := range [][2]string{{"op_kind", identity.Kind}, {"op_id", identity.ID}, {"root", identity.Root}, {"batch", identity.Batch}} {
		if pair[1] != "" {
			values = append(values, pair[0], pair[1])
		}
	}
	return pprof.Labels(values...)
}
