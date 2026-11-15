package archive

import (
	"context"
	"database/sql"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Background work's processor time and memory, by the work that used them
// (docs/background-work.md §11, "What the work costs"). Settings shows what
// each schedule choice would cost an item a day and a chart of CPU time by
// analysis; both read what this file measures.
//
// Go work shares the service process, and the runtime keeps no CPU time per
// goroutine. The kernel does keep it per thread, so the ledger measures the
// thread a piece of work runs on:
//
//   - Every operation, and every background item's run, is a span. The body
//     of its work (Op.RunIO, and a pass's call of an item) runs locked to
//     its thread, and the thread's CPU clock is read before and after
//     (cpuSpan.measure). That figure is measured, not divided: other work
//     running at the same time, on other threads, is not in it.
//   - A measured body inside another on the same thread (an operation an
//     item starts) is counted to its own source, and taken out of the outer
//     one's own figure; an item's run still reports everything inside it.
//   - What the body's thread did is all that is counted to it. Goroutines the
//     work starts, and the runtime's memory management on its behalf, run on
//     other threads. So does everything that is not a span's body: pages and
//     other requests, and loops that looked and found nothing. The
//     process's CPU time (getrusage) less everything measured is kept as
//     cpuSourceOther rather than left out, so the sources sum to what the
//     process used.
//   - The time a span was running (not queued or blocked, and with no span
//     running inside it) is its running time, and the most memory the
//     process held then is its peak.
//
// Worker processes (the labeler) are read directly: their counters are read
// for the lease that used them, so their CPU, memory and GPU time go to the
// span that held the lease.
//
// Nothing is written per sample. Totals are kept in memory per day and
// source and stored with the next background run's own write, or every few
// minutes once work has ended (flush); a request never writes them.

// cpuSourceOther is the source of CPU no span was running for, and
// cpuSourcePass a background pass's own work between its items.
const (
	cpuSourceOther = "other"
	cpuSourcePass  = "kind:background.update"
)

// cpuUse is what a span, or a day of a source, used. A nil figure was not
// measured, which is different from zero.
type cpuUse struct {
	// Seconds is the time the work was running.
	Seconds float64
	// CPU is the service's CPU time the work's own threads used.
	CPU *float64
	// Worker and GPU are its worker processes' CPU and GPU time, measured
	// over the leases the work held.
	Worker, GPU *float64
	// Peak is the most memory Pharos held while the work ran: the service,
	// and the workers the work was using.
	Peak *int64
}

func addTo(total **float64, value float64) {
	if *total == nil {
		*total = new(float64)
	}
	**total += value
}

func (use *cpuUse) raisePeak(bytes int64) {
	if bytes > 0 && (use.Peak == nil || bytes > *use.Peak) {
		use.Peak = &bytes
	}
}

// add folds other into use.
func (use *cpuUse) add(other cpuUse) {
	use.Seconds += other.Seconds
	for _, pair := range []struct {
		to   **float64
		from *float64
	}{{&use.CPU, other.CPU}, {&use.Worker, other.Worker}, {&use.GPU, other.GPU}} {
		if pair.from != nil {
			addTo(pair.to, *pair.from)
		}
	}
	if other.Peak != nil {
		use.raisePeak(*other.Peak)
	}
}

// total is the CPU time of the work: the service's share and its workers'.
// It is nil when neither was measured.
func (use cpuUse) total() *float64 {
	if use.CPU == nil && use.Worker == nil {
		return nil
	}
	sum := 0.0
	if use.CPU != nil {
		sum += *use.CPU
	}
	if use.Worker != nil {
		sum += *use.Worker
	}
	return &sum
}

// cpuDayKey is one day's total for one source.
type cpuDayKey struct{ day, source string }

type cpuDay struct {
	cpuUse
	runs int
}

// cpuScope is one measured body in progress on a thread; inner is the CPU
// time of the measured bodies that ran inside it.
type cpuScope struct{ inner float64 }

// cpuLedger keeps what each span's threads used, and the rest of the
// process's CPU.
type cpuLedger struct {
	mu  sync.Mutex
	now func() time.Time
	// readCPU is the process's own CPU seconds so far, and readMemory its
	// memory; ok is false where the platform has no such counter.
	readCPU    func() (float64, bool)
	readMemory func() (int64, bool)
	// readProcess reads a worker's counters; readGPU every GPU client's time
	// so far (nil where it cannot be read).
	readProcess func(int) (processUsage, error)
	readGPU     func() (gpuSnapshot, error)
	// readThread is the calling thread's CPU seconds so far and its id.
	readThread func() (float64, uint64, bool)

	last   float64
	lastOK bool
	lastAt time.Time
	spans  map[*cpuSpan]struct{}
	leases map[*cpuLease]struct{}
	// scopes is the measured body in progress on each thread, and pool the
	// process's CPU time not yet counted to a span or to the rest.
	scopes map[uint64]*cpuScope
	pool   float64
	// pending are the totals not yet stored; runsPending is whether any of
	// them is a finished run, which is what makes a flush worth a write.
	pending     map[cpuDayKey]*cpuDay
	runsPending bool
	flushedAt   time.Time
}

// cpuTick is how often a running span's CPU is settled and memory read, and
// cpuFlushEvery how long finished runs may wait in memory to be stored.
var (
	cpuTick       = 5 * time.Second
	cpuFlushEvery = 5 * time.Minute
)

// cpuDaysKept is how long a day's totals are kept.
const cpuDaysKept = 35 * 24 * time.Hour

func newCPULedger() *cpuLedger {
	ledger := &cpuLedger{now: time.Now, readProcess: processUsageOf, readGPU: platformGPUClients, readThread: threadCPUTime,
		spans: map[*cpuSpan]struct{}{}, leases: map[*cpuLease]struct{}{}, scopes: map[uint64]*cpuScope{}, pending: map[cpuDayKey]*cpuDay{}}
	ledger.readCPU = func() (float64, bool) {
		usage := processResourceUsage()
		return usage.cpu, usage.known.has(counterOwnCPU)
	}
	pid := os.Getpid()
	ledger.readMemory = func() (int64, bool) {
		usage, err := processUsageOf(pid)
		if err != nil || !usage.Known.has(counterMemory) {
			return 0, false
		}
		return int64(usage.Footprint), true
	}
	ledger.flushedAt = ledger.now()
	return ledger
}

// cpuSpan is one piece of work the ledger attributes CPU to.
type cpuSpan struct {
	ledger *cpuLedger
	source string
	parent *cpuSpan
	// open counts the spans inside this one that have not ended; paused is
	// work that is queued or blocked.
	open   int
	paused bool
	ended  bool
	// adopt is an item's span that takes the spans the pass's operation
	// starts while the item runs (Op.Child starts them from the pass's own
	// context, not the item's).
	adopt *cpuSpan
	// use is the span's own work and everything inside it.
	use cpuUse
	// counted are the sources a top-level span has counted a run of.
	counted map[string]bool
}

// used is what the span and the work inside it have used so far.
func (span *cpuSpan) used() cpuUse {
	if span == nil {
		return cpuUse{}
	}
	span.ledger.mu.Lock()
	defer span.ledger.mu.Unlock()
	return span.use
}

// cpuKindItems are the continuous maintenance kinds that are a background
// item's work without being listed as its kinds: Search is the Library view
// and the search indexes, kept by their own loops.
var cpuKindItems = map[string]string{"maintenance.library-view": "search", "maintenance.search.substring": "search", "maintenance.search.tool-output": "search",
	"maintenance.search.text": "search", "maintenance.search.tools": "search", "maintenance.search.query": "search"}

type cpuSpanKey struct{}

func withCPUSpan(ctx context.Context, span *cpuSpan) context.Context {
	if span == nil {
		return ctx
	}
	return context.WithValue(ctx, cpuSpanKey{}, span)
}

func cpuSpanFrom(ctx context.Context) *cpuSpan {
	if ctx == nil {
		return nil
	}
	span, _ := ctx.Value(cpuSpanKey{}).(*cpuSpan)
	return span
}

// cpuSourceFor is the source an operation's CPU is counted to: the
// background item its kind belongs to, else what it runs inside, else its
// own kind. The schedule's own operations are the pass, not an analysis.
func cpuSourceFor(kind, variant string, parent *cpuSpan) string {
	if kind == "analysis.findings" && variant == "full" {
		return "item:review"
	}
	if item, ok := cpuKindItems[kind]; ok {
		return "item:" + item
	}
	for _, item := range backgroundItems {
		for _, owned := range item.Kinds {
			name, _, _ := strings.Cut(owned, "/")
			if name == kind && (kind != "analysis.findings" || item.ID == "findings") {
				return "item:" + item.ID
			}
		}
	}
	if parent != nil {
		return parent.source
	}
	return "kind:" + kind
}

// begin starts a span inside parent (nil: top-level work). source names it
// once the span it is really inside is known (cpuSpan.adopt).
func (l *cpuLedger) begin(parent *cpuSpan, source func(parent *cpuSpan) string, paused bool) *cpuSpan {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settleLocked()
	for parent != nil && parent.adopt != nil && !parent.adopt.ended {
		parent = parent.adopt
	}
	if parent != nil && (parent.ended || parent.ledger != l) {
		parent = nil
	}
	span := &cpuSpan{ledger: l, source: source(parent), parent: parent, paused: paused}
	if parent != nil {
		parent.open++
	}
	l.spans[span] = struct{}{}
	return span
}

// beginItem starts a background item's span within the pass ctx runs in.
func beginItemSpan(ctx context.Context, item string) (context.Context, *cpuSpan) {
	parent := cpuSpanFrom(ctx)
	if parent == nil {
		return ctx, nil
	}
	span := parent.ledger.begin(parent, func(*cpuSpan) string { return "item:" + item }, false)
	parent.ledger.mu.Lock()
	if span.parent != nil {
		span.parent.adopt = span
	}
	parent.ledger.mu.Unlock()
	return withCPUSpan(ctx, span), span
}

// setPaused marks the span's work as waiting or running again.
func (span *cpuSpan) setPaused(paused bool) {
	if span == nil {
		return
	}
	l := span.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if span.ended || span.paused == paused {
		return
	}
	l.settleLocked()
	span.paused = paused
}

// measure runs fn, the body of the span's work, locked to its thread, and
// counts the CPU time the thread used to the span. Where the thread's clock
// cannot be read it runs fn and counts nothing. Safe on a nil span.
func (span *cpuSpan) measure(fn func()) {
	if span == nil {
		fn()
		return
	}
	l := span.ledger
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	start, thread, ok := l.readThread()
	if !ok {
		fn()
		return
	}
	scope := &cpuScope{}
	l.mu.Lock()
	outer := l.scopes[thread]
	l.scopes[thread] = scope
	l.mu.Unlock()
	defer func() {
		end, _, ok := l.readThread()
		l.mu.Lock()
		defer l.mu.Unlock()
		if outer != nil {
			l.scopes[thread] = outer
		} else {
			delete(l.scopes, thread)
		}
		if !ok || end < start {
			return
		}
		if outer != nil {
			outer.inner += end - start
		}
		own := max(0, end-start-scope.inner)
		addTo(&l.dayLocked(span.source).CPU, own)
		for at := span; at != nil; at = at.parent {
			addTo(&at.use.CPU, own)
		}
		l.pool -= own
	}()
	fn()
}

// end closes the span and reports what it and the work inside it used. ran
// is whether it counts as a run of its source: a pass that found nothing to
// do and was discarded does not.
func (span *cpuSpan) end(ran bool) cpuUse {
	if span == nil {
		return cpuUse{}
	}
	l := span.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if span.ended {
		return span.use
	}
	l.settleLocked()
	span.ended = true
	delete(l.spans, span)
	if span.parent != nil {
		span.parent.open--
		if span.parent.adopt == span {
			span.parent.adopt = nil
		}
	}
	// A run is counted once per source within one top-level piece of work:
	// an item's stages are one run of the item.
	if ran && (span.parent == nil || span.parent.source != span.source) {
		top := span
		for top.parent != nil {
			top = top.parent
		}
		if top.counted == nil {
			top.counted = map[string]bool{}
		}
		if !top.counted[span.source] {
			top.counted[span.source] = true
			l.dayLocked(span.source).runs++
			// The pass's own bookkeeping ends after its items were stored;
			// it waits for the next write rather than asking for one.
			l.runsPending = l.runsPending || span.source != cpuSourcePass
		}
	}
	return span.use
}

func (l *cpuLedger) dayLocked(source string) *cpuDay {
	key := cpuDayKey{l.now().Local().Format("2006-01-02"), source}
	day := l.pending[key]
	if day == nil {
		day = &cpuDay{}
		l.pending[key] = day
	}
	return day
}

// running are the spans whose own work is running now.
func (l *cpuLedger) runningLocked() []*cpuSpan {
	running := []*cpuSpan{}
	for span := range l.spans {
		if span.open == 0 && !span.paused {
			running = append(running, span)
		}
	}
	return running
}

// settleLocked counts the time since the last settlement to the spans that
// were running, reads memory for them, and moves the process's CPU time no
// span measured to the rest.
func (l *cpuLedger) settleLocked() {
	at := l.now()
	cpu, ok := l.readCPU()
	running := l.runningLocked()
	elapsed := at.Sub(l.lastAt).Seconds()
	if l.lastAt.IsZero() || elapsed < 0 {
		elapsed = 0
	}
	if ok && l.lastOK && cpu >= l.last {
		l.pool += cpu - l.last
	}
	// A measured body takes its CPU from the pool when it ends. With none in
	// progress, all that is left was used outside them.
	if len(l.scopes) == 0 {
		if l.pool > 0 {
			addTo(&l.dayLocked(cpuSourceOther).CPU, l.pool)
		}
		l.pool = 0
	}
	l.last, l.lastOK, l.lastAt = cpu, ok, at
	if len(running) == 0 {
		return
	}
	memory, memoryOK := l.readMemory()
	for _, span := range running {
		l.dayLocked(span.source).Seconds += elapsed
		for at := span; at != nil; at = at.parent {
			at.use.Seconds += elapsed
		}
		held, known := memory, memoryOK
		for lease := range l.leases {
			if lease.span == span {
				if bytes, ok := lease.observe(); ok {
					held, known = held+bytes, true
				}
			}
		}
		if !known {
			continue
		}
		l.dayLocked(span.source).raisePeak(held)
		for at := span; at != nil; at = at.parent {
			at.use.raisePeak(held)
		}
	}
}

// tick settles while work runs, so a long run's memory is read and its
// running time lands on the day it was used.
func (l *cpuLedger) tick() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.runningLocked()) > 0 {
		l.settleLocked()
	}
}

// cpuLease is one use of worker processes by a span.
type cpuLease struct {
	ledger *cpuLedger
	span   *cpuSpan
	pids   func() []int
	// start is each worker's counters when the lease first saw it (no CPU
	// time for one the pool started during the lease: all it used counts),
	// and last its latest reading. A worker that exits before the lease ends
	// (a crash and restart, a stopped run) keeps what it had used when last
	// read; its counters cannot be read once it is gone.
	start, last map[int]processUsage
	// banked is the CPU time of a worker whose pid another process then took.
	banked float64
	// gpuStart and gpuLast are the same for GPU time, in nanoseconds; gpuOK
	// is whether the lease reads it at all.
	gpuStart, gpuLast map[int]uint64
	gpuOK             bool
	// sharers is the most leases that held the same workers at once.
	sharers int
	peak    int64
	peakOK  bool
	once    sync.Once
}

// observe reads the lease's workers now, the ones the pool reports and the
// ones seen before, and reports their memory. The ledger's lock is held.
func (lease *cpuLease) observe() (int64, bool) {
	pids := lease.pids()
	for pid := range lease.last {
		if !slices.Contains(pids, pid) {
			pids = append(pids, pid)
		}
	}
	total, known := int64(0), false
	for _, pid := range pids {
		usage, err := lease.ledger.readProcess(pid)
		if err != nil {
			continue
		}
		if usage.Known.has(counterMemory) {
			total, known = total+int64(usage.Footprint), true
		}
		if !usage.Known.has(counterOwnCPU) {
			continue
		}
		before, held := lease.start[pid]
		if held && before.Started != usage.Started {
			lease.banked += max(0, lease.last[pid].CPUSeconds-before.CPUSeconds)
		}
		if !held || before.Started != usage.Started {
			lease.start[pid] = processUsage{Started: usage.Started}
			delete(lease.gpuStart, pid)
			delete(lease.gpuLast, pid)
		}
		lease.last[pid] = usage
	}
	if known && total > lease.peak {
		lease.peak, lease.peakOK = total, true
	}
	return total, known
}

// observeGPU reads the GPU time of the lease's workers. A worker with no
// GPU client that is still running has used none; one that is gone keeps its
// last reading, or has none. The ledger's lock is not held: the read forks.
func (lease *cpuLease) observeGPU() {
	l := lease.ledger
	l.mu.Lock()
	pids := make([]int, 0, len(lease.last))
	for pid := range lease.last {
		pids = append(pids, pid)
	}
	l.mu.Unlock()
	times, ok := l.gpuTimes(pids)
	if !ok {
		return
	}
	alive := map[int]bool{}
	for _, pid := range pids {
		usage, err := l.readProcess(pid)
		alive[pid] = err == nil && usage.Known.has(counterOwnCPU)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, pid := range pids {
		if ns, found := times[pid]; found {
			lease.gpuLast[pid] = max(ns, lease.gpuLast[pid])
		} else if _, read := lease.gpuLast[pid]; !read && alive[pid] {
			lease.gpuLast[pid] = lease.gpuStart[pid]
		}
	}
}

// observeLeases reads every open lease's workers, GPU time included. A run
// that is about to kill its workers calls it first.
func (l *cpuLedger) observeLeases() {
	if l == nil {
		return
	}
	l.mu.Lock()
	leases := make([]*cpuLease, 0, len(l.leases))
	for lease := range l.leases {
		lease.observe()
		leases = append(leases, lease)
	}
	l.mu.Unlock()
	for _, lease := range leases {
		if lease.gpuOK {
			lease.observeGPU()
		}
	}
}

func (l *cpuLedger) gpuTimes(pids []int) (map[int]uint64, bool) {
	if l.readGPU == nil {
		return nil, false
	}
	snapshot, err := l.readGPU()
	if err != nil || !snapshot.supported {
		return nil, false
	}
	times := map[int]uint64{}
	for _, client := range snapshot.clients {
		if client.known && slices.Contains(pids, client.pid) {
			times[client.pid] += client.ns
		}
	}
	return times, true
}

// leaseWorkers opens a lease on the worker processes pids reports for the
// span ctx runs in; the returned function closes it and counts what the
// workers used to that span. gpu is whether the workers may use the GPU:
// its time is read only then (one registry query at each end).
func leaseWorkers(ctx context.Context, pids func() []int, gpu bool) func() {
	span := cpuSpanFrom(ctx)
	if span == nil {
		return func() {}
	}
	l := span.ledger
	lease := &cpuLease{ledger: l, span: span, pids: pids, start: map[int]processUsage{}, last: map[int]processUsage{},
		gpuStart: map[int]uint64{}, gpuLast: map[int]uint64{}, sharers: 1}
	current := pids()
	for _, pid := range current {
		if usage, err := l.readProcess(pid); err == nil && usage.Known.has(counterOwnCPU) {
			lease.start[pid], lease.last[pid] = usage, usage
		}
	}
	if gpu {
		if times, ok := l.gpuTimes(current); ok {
			lease.gpuStart, lease.gpuOK = times, true
		}
	}
	l.mu.Lock()
	for other := range l.leases {
		if slices.ContainsFunc(current, func(pid int) bool { _, held := other.start[pid]; return held }) {
			lease.sharers++
			other.sharers = max(other.sharers, lease.sharers)
		}
	}
	l.leases[lease] = struct{}{}
	l.mu.Unlock()
	return func() { lease.once.Do(lease.end) }
}

func (lease *cpuLease) end() {
	l := lease.ledger
	l.mu.Lock()
	lease.observe()
	l.mu.Unlock()
	if lease.gpuOK {
		lease.observeGPU()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settleLocked()
	delete(l.leases, lease)
	cpu, measured := lease.banked, len(lease.last) > 0
	for pid, usage := range lease.last {
		cpu += max(0, usage.CPUSeconds-lease.start[pid].CPUSeconds)
	}
	// GPU time is measured when some worker's was read after the start.
	var gpu *float64
	for pid, ns := range lease.gpuLast {
		if gpu == nil {
			gpu = new(float64)
		}
		if ns >= lease.gpuStart[pid] {
			*gpu += float64(ns-lease.gpuStart[pid]) / 1e9
		}
	}
	// Workers several leases held at once: an equal part each.
	share := 1 / float64(max(1, lease.sharers))
	day := l.dayLocked(lease.span.source)
	credit := func(use *cpuUse) {
		if measured {
			addTo(&use.Worker, cpu*share)
		}
		if gpu != nil {
			addTo(&use.GPU, *gpu*share)
		}
	}
	credit(&day.cpuUse)
	for at := lease.span; at != nil; at = at.parent {
		credit(&at.use)
	}
}

// takePending hands over the unstored totals; restore puts them back when
// they could not be stored.
func (l *cpuLedger) takePending() map[cpuDayKey]*cpuDay {
	l.mu.Lock()
	defer l.mu.Unlock()
	taken := l.pending
	l.pending, l.runsPending, l.flushedAt = map[cpuDayKey]*cpuDay{}, false, l.now()
	return taken
}

func (l *cpuLedger) restore(taken map[cpuDayKey]*cpuDay) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, day := range taken {
		if into := l.pending[key]; into != nil {
			into.add(day.cpuUse)
			into.runs += day.runs
		} else {
			l.pending[key] = day
		}
		l.runsPending = l.runsPending || day.runs > 0
	}
}

// snapshot is a copy of the unstored totals, for readers.
func (l *cpuLedger) snapshot() map[cpuDayKey]cpuDay {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	copied := map[cpuDayKey]cpuDay{}
	for key, day := range l.pending {
		value := cpuDay{runs: day.runs}
		value.add(day.cpuUse)
		copied[key] = value
	}
	return copied
}

// flushDue is whether finished runs have waited long enough to store.
func (l *cpuLedger) flushDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runsPending && l.now().Sub(l.flushedAt) >= cpuFlushEvery
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// storeCPUDays adds totals to the days stored for this Mac, and drops days
// too old to be read again.
func storeCPUDays(ctx context.Context, tx *sql.Tx, totals map[cpuDayKey]*cpuDay) error {
	if len(totals) == 0 {
		return nil
	}
	for key, day := range totals {
		var peak any
		if day.Peak != nil {
			peak = *day.Peak
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO background_cpu_days(host_id,day,source,runs,seconds,cpu_seconds,worker_cpu_seconds,gpu_seconds,peak_bytes)
			VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(host_id,day,source) DO UPDATE SET runs=runs+excluded.runs, seconds=seconds+excluded.seconds,
			cpu_seconds=CASE WHEN excluded.cpu_seconds IS NULL THEN cpu_seconds ELSE COALESCE(cpu_seconds,0)+excluded.cpu_seconds END,
			worker_cpu_seconds=CASE WHEN excluded.worker_cpu_seconds IS NULL THEN worker_cpu_seconds ELSE COALESCE(worker_cpu_seconds,0)+excluded.worker_cpu_seconds END,
			gpu_seconds=CASE WHEN excluded.gpu_seconds IS NULL THEN gpu_seconds ELSE COALESCE(gpu_seconds,0)+excluded.gpu_seconds END,
			peak_bytes=CASE WHEN excluded.peak_bytes IS NULL THEN peak_bytes ELSE MAX(COALESCE(peak_bytes,0),excluded.peak_bytes) END`,
			currentHost().ID, key.day, key.source, day.runs, day.Seconds, nullFloat(day.CPU), nullFloat(day.Worker), nullFloat(day.GPU), peak); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM background_cpu_days WHERE host_id=? AND day<?", currentHost().ID, time.Now().Add(-cpuDaysKept).Local().Format("2006-01-02"))
	return err
}

// takeCPU hands over the ledger's unstored totals for a write to store
// (storeCPUDays); restoreCPU returns them when the write did not commit.
func (c *Catalog) takeCPU() map[cpuDayKey]*cpuDay {
	if c == nil || c.ops == nil || c.ops.cpu == nil {
		return nil
	}
	return c.ops.cpu.takePending()
}

func (c *Catalog) restoreCPU(taken map[cpuDayKey]*cpuDay) {
	if len(taken) > 0 && c != nil && c.ops != nil && c.ops.cpu != nil {
		c.ops.cpu.restore(taken)
	}
}

// cpuLedgerLoop keeps running work's CPU and memory current and stores
// finished runs' totals: with a write of their own only when no background
// run's write carried them for cpuFlushEvery.
func (s *Server) cpuLedgerLoop(ctx context.Context) {
	ledger := s.ops.cpu
	if ledger == nil {
		return
	}
	ticker := time.NewTicker(cpuTick)
	defer ticker.Stop()
	store := func(ctx context.Context) {
		taken := s.Catalog.takeCPU()
		if err := s.Catalog.writeTransaction(ctx, "background cpu", func(tx *sql.Tx) error { return storeCPUDays(ctx, tx, taken) }); err != nil {
			s.Catalog.restoreCPU(taken)
		}
	}
	for {
		select {
		case <-ctx.Done():
			// What finished since the last write, before the catalog closes.
			if ledger.flushDueNow() {
				final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				store(final)
				cancel()
			}
			return
		case <-ticker.C:
		}
		ledger.tick()
		if ledger.flushDue() {
			store(ctx)
		}
	}
}

// flushDueNow is whether any finished run is unstored.
func (l *cpuLedger) flushDueNow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, day := range l.pending {
		if day.runs > 0 {
			return true
		}
	}
	return false
}

// cpuDays are this Mac's totals per day and source since the day since
// falls on, stored and not yet stored. A catalog without the table (an older
// one opened read-only) has only the latter.
func (c *Catalog) cpuDays(ctx context.Context, since time.Time) map[cpuDayKey]cpuDay {
	first := since.Local().Format("2006-01-02")
	days := map[cpuDayKey]cpuDay{}
	rows, err := c.reader().QueryContext(ctx, `SELECT day,source,runs,seconds,cpu_seconds,worker_cpu_seconds,gpu_seconds,peak_bytes
		FROM background_cpu_days WHERE host_id=? AND day>=?`, currentHost().ID, first)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key cpuDayKey
			var day cpuDay
			var cpu, worker, gpu sql.NullFloat64
			var peak sql.NullInt64
			if rows.Scan(&key.day, &key.source, &day.runs, &day.Seconds, &cpu, &worker, &gpu, &peak) != nil {
				continue
			}
			for _, pair := range []struct {
				to   **float64
				from sql.NullFloat64
			}{{&day.CPU, cpu}, {&day.Worker, worker}, {&day.GPU, gpu}} {
				if pair.from.Valid {
					*pair.to = &pair.from.Float64
				}
			}
			if peak.Valid {
				day.Peak = &peak.Int64
			}
			days[key] = day
		}
	}
	if c.ops != nil {
		for key, pending := range c.ops.cpu.snapshot() {
			if key.day < first {
				continue
			}
			day := days[key]
			day.runs += pending.runs
			day.add(pending.cpuUse)
			days[key] = day
		}
	}
	return days
}

// cpuWindowDays is how many days the chart of CPU by analysis covers.
const cpuWindowDays = 7

// cpuSourceRow is one bar of Settings' chart: a source's use a day over the
// days measured, and each day's.
type cpuSourceRow struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	// Item is the background item the source is, where it is one.
	Item string `json:"item,omitempty"`
	// Per day, over the days measured. CPUSeconds is the service's share and
	// the workers' together; WorkerCPUSeconds the workers' part of it.
	CPUSeconds       *float64 `json:"cpu_seconds"`
	WorkerCPUSeconds *float64 `json:"worker_cpu_seconds"`
	GPUSeconds       *float64 `json:"gpu_seconds"`
	Seconds          float64  `json:"seconds"`
	Runs             float64  `json:"runs"`
	// PeakBytes is the most over the window, not a daily figure.
	PeakBytes *int64       `json:"peak_bytes"`
	Days      []cpuDayCell `json:"days"`
}

type cpuDayCell struct {
	Day        string   `json:"day"`
	CPUSeconds *float64 `json:"cpu_seconds"`
	Seconds    float64  `json:"seconds"`
}

// cpuSourceLabel names a source as Settings does: the background item's
// name, else the operation kind's.
func cpuSourceLabel(source string) (label, item string) {
	switch name, id, _ := strings.Cut(source, ":"); name {
	case "item":
		if known, ok := backgroundItemByID(id); ok {
			return known.Label, id
		}
		return id, ""
	case "kind":
		if source == cpuSourcePass {
			return "Scheduling the updates", ""
		}
		if kind, ok := lookupOperationKind(id); ok {
			return kind.Label, ""
		}
		return id, ""
	}
	return "Pages and everything else", ""
}

// cpuUsage is the chart's document: each source's CPU a day over the last
// cpuWindowDays local days ending at at, largest first, with the unnamed
// rest last. A day counts from the first one with anything measured, so a
// Mac measured for two days is averaged over two.
func cpuUsage(days map[cpuDayKey]cpuDay, at time.Time) map[string]any {
	window := []string{}
	for back := cpuWindowDays - 1; back >= 0; back-- {
		window = append(window, at.Local().AddDate(0, 0, -back).Format("2006-01-02"))
	}
	first := ""
	for key := range days {
		if slices.Contains(window, key.day) && (first == "" || key.day < first) {
			first = key.day
		}
	}
	if first == "" {
		return map[string]any{"window_days": cpuWindowDays, "measured_days": 0, "days": window, "rows": []cpuSourceRow{}, "cpu_seconds": nil, "since": nil}
	}
	window = window[slices.Index(window, first):]
	measured := float64(len(window))
	sources := map[string]*cpuSourceRow{}
	totals := map[string]*cpuUse{}
	for key, day := range days {
		if !slices.Contains(window, key.day) {
			continue
		}
		row := sources[key.source]
		if row == nil {
			row = &cpuSourceRow{Source: key.source}
			row.Label, row.Item = cpuSourceLabel(key.source)
			sources[key.source], totals[key.source] = row, &cpuUse{}
		}
		totals[key.source].add(day.cpuUse)
		row.Runs += float64(day.runs)
	}
	rows, whole := []cpuSourceRow{}, (*float64)(nil)
	for source, row := range sources {
		total := totals[source]
		// Work that used nothing worth a bar: a tenth of a second all week,
		// or bookkeeping with no measured body that ran under a second.
		if cpu := total.total(); source != cpuSourceOther && (cpu == nil || *cpu < 0.1) && total.Seconds < 1 {
			continue
		}
		row.Seconds, row.Runs = total.Seconds/measured, row.Runs/measured
		perDay := func(value *float64) *float64 {
			if value == nil {
				return nil
			}
			return new(*value / measured)
		}
		row.CPUSeconds, row.WorkerCPUSeconds, row.GPUSeconds, row.PeakBytes = perDay(total.total()), perDay(total.Worker), perDay(total.GPU), total.Peak
		for _, name := range window {
			day := days[cpuDayKey{name, source}]
			row.Days = append(row.Days, cpuDayCell{Day: name, CPUSeconds: day.total(), Seconds: day.Seconds})
		}
		if row.CPUSeconds != nil {
			addTo(&whole, *row.CPUSeconds)
		}
		rows = append(rows, *row)
	}
	value := func(row cpuSourceRow) float64 {
		if row.CPUSeconds == nil {
			return -1
		}
		return *row.CPUSeconds
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if other := rows[i].Source == cpuSourceOther; other != (rows[j].Source == cpuSourceOther) {
			return !other
		}
		if value(rows[i]) != value(rows[j]) {
			return value(rows[i]) > value(rows[j])
		}
		return rows[i].Label < rows[j].Label
	})
	return map[string]any{"window_days": cpuWindowDays, "measured_days": len(window), "days": window, "rows": rows, "cpu_seconds": whole, "since": first}
}
