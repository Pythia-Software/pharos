package archive

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"time"
)

// Resources (docs/background-work.md §10): CPU and memory per Pharos
// process, for the status snapshots. A sampler reads each process with one
// system call (proc_pid_rusage on macOS, resources_darwin.go), never by
// forking ps. It samples only when a snapshot is computed, and at most once
// per resourceSampleInterval per process: with no page open nothing is
// sampled, and with any number open a process is read at most every
// interval. So the sampler is not a loop and starts no goroutine.
//
// CPU is the change in user plus system time over the wall time between
// samples. On macOS active descendants have separate rows; reaped child
// time is included in the parent after deducting CPU already sampled from
// that child. CPU is displayed as a percentage of
// one core. Memory is the physical footprint, what Activity Monitor shows.
// Disk is the change in bytes the process read and wrote over the same
// wall time (its own: the kernel keeps no count for exited children).
// Attribution stops at the process: which operation used the CPU is not
// measured here (work_resources.go records what a task can honestly claim).
//
// Under the scheduled engine one owner per process tree also runs a history
// collector (resources_history.go) on this same sampler. While it runs,
// snapshots reuse its latest readings instead of sampling again.
//
// Each reading names the counters it has (processUsage.Known). On Linux a
// process reads only itself, through getrusage and the Go runtime
// (resources_linux.go); elsewhere no OS counter is implemented and only the
// Go runtime's memory is read (resources_portable.go). A counter a reading
// lacks is unavailable: its rate and total are null, never zero.

// resourceSampleInterval is the least time between two readings of one
// process; a snapshot computed sooner reuses the last reading.
var resourceSampleInterval = 1500 * time.Millisecond

// resourceForget is how long a process not asked about keeps its last
// reading before it is dropped.
var resourceForget = 5 * time.Minute

// resourceStaleGap is the longest gap between two readings that gives a
// CPU figure: after a longer one (no page was open), the reading is a new
// baseline and CPU is null until the next.
var resourceStaleGap = 10 * time.Second

// resourceCores bounds a CPU figure: no process uses more than every core.
var resourceCores = runtime.NumCPU

// resourceCounters are the counters an observation read. A counter it
// does not name is unavailable (unsupported here, or its read failed): its
// value is meaningless, where a named counter's zero is a real zero.
type resourceCounters uint8

const (
	// counterOwnCPU is the process's own user and system time.
	counterOwnCPU resourceCounters = 1 << iota
	// counterChildCPU is that of its completed, reaped children.
	counterChildCPU
	// counterMemory is processUsage.Footprint, the memory a reading reports:
	// the physical footprint on macOS, elsewhere the Go runtime's own
	// approximation (resourceBasis says which).
	counterMemory
	// counterResident is processUsage.Resident, a true resident set size.
	counterResident
	// counterDisk is processUsage.DiskBytes, bytes read and written.
	counterDisk
	// counterBlocks is processResources' Unix block counts, which are not
	// byte counts.
	counterBlocks
)

func (c resourceCounters) has(want resourceCounters) bool { return c&want == want }

// processUsage is one reading of a process.
type processUsage struct {
	// CPUSeconds is the process's own user and system time; ChildCPUSeconds
	// that of its exited, reaped children.
	CPUSeconds, ChildCPUSeconds float64
	// Footprint is the memory the reading reports (counterMemory); Resident
	// the resident set size, which is what ps -o rss reports.
	Footprint, Resident uint64
	// Started identifies the process across PID reuse (0: unknown).
	Started uint64
	// DiskBytes is what the process has read from and written to disk.
	DiskBytes uint64
	// Known names the counters this reading has.
	Known resourceCounters
}

// cpuKnown is whether the reading has the CPU a rate needs: its own, and
// unless ownOnly its reaped children's.
func (u processUsage) cpuKnown(ownOnly bool) bool {
	return u.Known.has(counterOwnCPU) && (ownOnly || u.Known.has(counterChildCPU))
}

// goRuntimeUsage is what the Go runtime reports of this process: the memory
// it manages (/memory/classes/total:bytes), an approximation that leaves out
// memory the runtime does not manage, so it is not a resident set size.
func goRuntimeUsage() processUsage {
	usage := processUsage{}
	sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() == metrics.KindUint64 {
		usage.Footprint, usage.Known = sample[0].Value.Uint64(), counterMemory
	}
	return usage
}

// resourceProcess is one process a snapshot asks about.
type resourceProcess struct {
	PID    int
	Parent int
	// Role is service, broker, or the process name of a lease (cli:ingest).
	Role string
	// Library is the registered library a broker reading belongs to.
	Library, LibraryName string
	// OwnOnly is for callers that intentionally want only this process's CPU.
	OwnOnly bool
}

type resourceSampler struct {
	mu    sync.Mutex
	read  func(pid int) (processUsage, error)
	basis string
	now   func() time.Time
	byPID map[int]*resourceTrack
	// reads counts system calls, for tests.
	reads int
	// staleGap overrides resourceStaleGap while a history collector reads
	// at its idle cadence (0: resourceStaleGap).
	staleGap time.Duration
	// history is the collector sharing this sampler, if one was made.
	history *resourceHistory
}

// gap is the longest interval that still gives a rate. Callers hold r.mu.
func (r *resourceSampler) gap() time.Duration {
	if r.staleGap > 0 {
		return r.staleGap
	}
	return resourceStaleGap
}

func (r *resourceSampler) setStaleGap(gap time.Duration) {
	r.mu.Lock()
	r.staleGap = gap
	r.mu.Unlock()
}

type resourceTrack struct {
	at, asked time.Time
	usage     processUsage
	// The first reading separates child CPU before and after the interval
	// when a sampled child is later reaped by its parent.
	firstAt       time.Time
	firstCPU      float64
	parent        int
	reapedCovered bool
	// percent is the CPU over the interval that ended at at, and disk its
	// bytes per second; nil after the first reading, or when the counters
	// of either end are unavailable.
	percent, disk *float64
	err           error
}

func newResourceSampler() *resourceSampler {
	return &resourceSampler{read: processUsageOf, basis: resourceBasis, now: time.Now, byPID: map[int]*resourceTrack{}}
}

// resourceReading is one process in a snapshot's resources.
type resourceReading struct {
	PID        int      `json:"pid"`
	Role       string   `json:"role"`
	Library    string   `json:"library,omitempty"`
	CPUPercent *float64 `json:"cpu_percent"`
	// MemoryBytes is null when the process's memory could not be read.
	MemoryBytes *uint64 `json:"memory_bytes"`
	// DiskBytesPerSecond is what it read and wrote per second, over the
	// same interval as CPU (null until a second reading).
	DiskBytesPerSecond *float64 `json:"disk_bytes_per_second"`
	SampledAt          string   `json:"sampled_at"`
	Basis              string   `json:"basis"`
	// Text is the server's line for the process ("Library service · 12% of
	// one core · 340 MB").
	Text string `json:"text"`
	// exact values, before rounding for the document; started identifies
	// the process across PID reuse (0: unknown).
	cpu, disk *float64
	memory    *uint64
	at        time.Time
	started   uint64
	// cpuUnavailable is a CPU that cannot be read, as opposed to one
	// waiting for its second reading.
	cpuUnavailable bool
}

// resourceSummary is a snapshot's resources: the totals of its processes,
// and each of them.
type resourceSummary struct {
	// GPU measures only this summary's Pharos process tree.
	GPU                *gpuReading `json:"gpu,omitempty"`
	CPUPercent         *float64    `json:"cpu_percent"`
	MemoryBytes        *uint64     `json:"memory_bytes"`
	DiskBytesPerSecond *float64    `json:"disk_bytes_per_second"`
	// A process without a second sample has no rate yet, and one whose
	// counters cannot be read has none at all. Keep the measured part
	// visible, and say explicitly when it is a lower bound. Each total is
	// null when no process measured it.
	CPUPartial    bool   `json:"cpu_partial"`
	DiskPartial   bool   `json:"disk_partial"`
	MemoryPartial bool   `json:"memory_partial"`
	SampledAt     string `json:"sampled_at"`
	Basis         string `json:"basis"`
	// Approximate is true when a reading is the process's own report (off
	// macOS) rather than the kernel's.
	Approximate bool `json:"approximate"`
	// Label is the panel's Load line.
	Label     string            `json:"label"`
	Processes []resourceReading `json:"processes"`
}

// sample reads each process (once per PID), reusing a reading younger than
// resourceSampleInterval. A process that cannot be read (it exited, or
// belongs to another user) is left out.
func (r *resourceSampler) sample(processes []resourceProcess) []resourceReading {
	return r.sampleWithTable(processes, resourceProcessTable())
}

func (r *resourceSampler) sampleWithTable(processes []resourceProcess, table []processNode) []resourceReading {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	processes = expandResourceProcesses(processes, table)
	// A service snapshot can keep naming a worker after it exits. Only the
	// current OS table establishes liveness for reaped-child accounting.
	present := make(map[int]bool, len(table))
	if table == nil {
		for _, process := range processes {
			present[process.PID] = true
		}
	} else {
		for _, node := range table {
			present[node.PID] = true
		}
	}
	readings := []resourceReading{}
	seen := map[int]bool{}
	for _, process := range processes {
		if process.PID <= 0 || seen[process.PID] {
			continue
		}
		seen[process.PID] = true
		track := r.byPID[process.PID]
		if table != nil && !present[process.PID] && track != nil {
			// A cached snapshot can still request a reaped worker. Keep its
			// counters for the parent, but do not show it as a live process.
			track.asked = now
			continue
		}
		if track == nil || now.Sub(track.at) >= resourceSampleInterval {
			r.reads++
			usage, err := r.read(process.PID)
			previous := track
			parent := process.Parent
			if !present[process.PID] && previous != nil && (err != nil || previous.usage.Started == usage.Started) {
				parent = previous.parent
			}
			switch {
			case err != nil:
				// Keep the last good counters for the parent's next sample;
				// this process can still be in a cached snapshot's roots.
				track = &resourceTrack{at: now, err: err, parent: parent}
				if previous != nil {
					track.usage, track.firstAt, track.firstCPU = previous.usage, previous.firstAt, previous.firstCPU
					track.reapedCovered = previous.reapedCovered
				}
			case track == nil || track.err != nil || track.usage.Started != usage.Started:
				track = &resourceTrack{at: now, usage: usage}
			case now.Sub(track.at) > r.gap():
				// An average over a long gap is not the current load.
				track = &resourceTrack{at: now, usage: usage}
			default:
				elapsed := now.Sub(track.at).Seconds()
				percent, reset := r.cpuPercent(process, track, usage, elapsed, present)
				if reset {
					// A CPU counter went backwards, or reaped children ran
					// longer than this interval could (a reaped service, a
					// long agent call): neither is this interval's load.
					// Start again from here.
					track = &resourceTrack{at: now, usage: usage}
					break
				}
				// Disk is independent of CPU: either can be unavailable
				// (percent or disk nil) while the other has its rate.
				var disk *float64
				if previous := track.usage; usage.Known.has(counterDisk) && previous.Known.has(counterDisk) && usage.DiskBytes >= previous.DiskBytes {
					disk = new(float64(usage.DiskBytes-previous.DiskBytes) / elapsed)
				}
				track = &resourceTrack{at: now, usage: usage, percent: percent, disk: disk,
					firstAt: previous.firstAt, firstCPU: previous.firstCPU, parent: parent}
			}
			if track.firstAt.IsZero() && err == nil && usage.cpuKnown(false) {
				track.firstAt, track.firstCPU = now, usage.CPUSeconds+usage.ChildCPUSeconds
			}
			track.parent = parent
			r.byPID[process.PID] = track
		}
		track.asked = now
		if track.err != nil {
			continue
		}
		var memory *uint64
		if track.usage.Known.has(counterMemory) {
			memory = new(track.usage.Footprint)
		}
		reading := resourceReading{PID: process.PID, Role: process.Role, Library: process.Library, Basis: r.basis,
			cpu: track.percent, disk: track.disk, memory: memory, at: track.at, started: track.usage.Started, cpuUnavailable: !track.usage.cpuKnown(process.OwnOnly)}
		reading.CPUPercent, reading.MemoryBytes, reading.SampledAt = roundedPercent(track.percent), roundedMemory(memory), formatTime(track.at)
		reading.DiskBytesPerSecond = roundedRate(track.disk)
		reading.Text = strings.Join(slices.DeleteFunc([]string{resourceRoleLabel(process), loadWords(track.percent, memory)}, func(s string) bool { return s == "" }), " · ")
		readings = append(readings, reading)
	}
	for pid, track := range r.byPID {
		if now.Sub(track.asked) > resourceForget {
			delete(r.byPID, pid)
		}
	}
	return readings
}

// cpuPercent is process's CPU over the interval from track's reading to
// usage, as a percentage of one core: nil when either reading lacks the CPU
// counters it needs (an unavailable interval, not a zero), and reset when a
// counter went backwards or reaped children's time exceeds what the interval
// could hold, which needs a new baseline rather than a negative or inflated
// figure.
func (r *resourceSampler) cpuPercent(process resourceProcess, track *resourceTrack, usage processUsage, elapsed float64, present map[int]bool) (percent *float64, reset bool) {
	if !usage.cpuKnown(process.OwnOnly) || !track.usage.cpuKnown(process.OwnOnly) {
		return nil, false
	}
	cores := float64(resourceCores())
	own := usage.CPUSeconds - track.usage.CPUSeconds
	children := usage.ChildCPUSeconds - track.usage.ChildCPUSeconds
	if process.OwnOnly {
		children = 0
	}
	if own < 0 || children < 0 {
		return nil, true
	}
	if !process.OwnOnly {
		children -= r.reapedChildOverlap(process.PID, children, track.at, present)
	}
	if children > elapsed*cores {
		return nil, true
	}
	return new(min(max(0, 100*(own+children)/elapsed), 100*cores)), false
}

// reapedChildOverlap removes only CPU already counted in a child's own
// samples. A helper first seen in this interval contributes its initial CPU
// through the parent's reaped-child counter; an older child contributes only
// CPU after its last sample. The parent's counter advances only on reap.
func (r *resourceSampler) reapedChildOverlap(parent int, childCPU float64, since time.Time, present map[int]bool) float64 {
	if childCPU <= 0 {
		return 0
	}
	remaining, overlap := childCPU, 0.0
	children := map[int][]int{}
	for pid, child := range r.byPID {
		// A reading without both CPU counters has nothing to deduct.
		if !present[pid] && !child.reapedCovered && !child.firstAt.IsZero() && child.usage.cpuKnown(false) {
			children[child.parent] = append(children[child.parent], pid)
		}
	}
	for _, pids := range children {
		slices.Sort(pids)
	}
	// A reaped child transfers the CPU of descendants it reaped to its
	// parent. Its last sample may predate their exit, so include any
	// outstanding sampled descendants before comparing with that transfer.
	var subtree func(int, map[int]bool) (float64, float64, []int)
	subtree = func(pid int, visited map[int]bool) (float64, float64, []int) {
		if visited[pid] {
			return 0, 0, nil
		}
		visited[pid] = true
		child := r.byPID[pid]
		total := child.usage.CPUSeconds + child.usage.ChildCPUSeconds
		counted := total - child.firstCPU
		if !child.firstAt.After(since) {
			counted = total
		}
		members := []int{pid}
		for _, descendant := range children[pid] {
			part, alreadyCounted, ids := subtree(descendant, visited)
			total += part
			counted += alreadyCounted
			members = append(members, ids...)
		}
		return total, counted, members
	}
	for _, pid := range children[parent] {
		total, counted, members := subtree(pid, map[int]bool{})
		if total > remaining+1e-6 {
			continue
		}
		remaining -= total
		overlap += counted
		for _, member := range members {
			r.byPID[member].reapedCovered = true
		}
	}
	return overlap
}

// processNode is the OS process tree entry needed to find active workers.
type processNode struct {
	PID, Parent int
	Name        string
}

// expandResourceProcesses attributes descendants to their nearest known Pharos
// process. Keep the direct parent so reaped child CPU can be reconciled.
func expandResourceProcesses(roots []resourceProcess, table []processNode) []resourceProcess {
	if table == nil {
		return roots
	}
	processes := append([]resourceProcess(nil), roots...)
	known := map[int]resourceProcess{}
	parents := map[int]int{}
	for _, node := range table {
		parents[node.PID] = node.Parent
	}
	for i := range processes {
		if parent, ok := parents[processes[i].PID]; ok {
			processes[i].Parent = parent
		}
		known[processes[i].PID] = processes[i]
	}
	for changed := true; changed; {
		changed = false
		for _, node := range table {
			if _, exists := known[node.PID]; exists {
				continue
			}
			if parent, ok := known[node.Parent]; ok {
				child := resourceProcess{PID: node.PID, Parent: node.Parent, Role: "worker:" + node.Name, Library: parent.Library, LibraryName: parent.LibraryName}
				known[node.PID] = child
				processes = append(processes, child)
				changed = true
			}
		}
	}
	return processes
}

// summarizeResources totals readings under label's subject ("Pharos").
// With nothing readable it is nil, which keeps the panel's Load row out.
func summarizeResources(subject string, readings []resourceReading) *resourceSummary {
	if len(readings) == 0 {
		return nil
	}
	summary := &resourceSummary{Processes: readings, Basis: readings[0].Basis}
	// New workers need two samples for a rate. Do not clear the whole
	// machine's rate every time one starts; report the measured lower bound.
	// A process whose counters cannot be read is left out of a total the
	// same way, and the total says it is a lower bound.
	cpu, disk := 0.0, 0.0
	cpuCount, diskCount, memoryCount := 0, 0, 0
	cpuUnavailable := false
	var memory uint64
	var at time.Time
	for _, reading := range readings {
		if reading.cpu == nil {
			summary.CPUPartial = true
			cpuUnavailable = cpuUnavailable || reading.cpuUnavailable
		} else {
			cpu += *reading.cpu
			cpuCount++
		}
		if reading.disk == nil {
			summary.DiskPartial = true
		} else {
			disk += *reading.disk
			diskCount++
		}
		if reading.memory == nil {
			summary.MemoryPartial = true
		} else {
			memory += *reading.memory
			memoryCount++
		}
		if reading.at.After(at) {
			at = reading.at
		}
		if reading.Basis != resourceBasisKernel {
			summary.Approximate = true
		}
		if reading.Basis != summary.Basis {
			summary.Basis = "mixed"
		}
	}
	var cpuRate, diskRate *float64
	var memoryTotal *uint64
	if memoryCount > 0 {
		memoryTotal = &memory
	}
	if cpuCount > 0 {
		// A total, like each figure, never exceeds every core (§10).
		cpuRate = new(min(cpu, 100*float64(resourceCores())))
	}
	if diskCount > 0 {
		diskRate = &disk
	}
	summary.CPUPercent, summary.MemoryBytes, summary.SampledAt = roundedPercent(cpuRate), roundedMemory(memoryTotal), formatTime(at)
	summary.DiskBytesPerSecond = roundedRate(diskRate)
	if words := loadWords(cpuRate, memoryTotal); words != "" {
		summary.Label = subject + " is using " + words
	} else {
		summary.Label = subject + "'s CPU and memory could not be measured"
	}
	switch {
	case summary.CPUPartial && cpuRate != nil && cpuUnavailable:
		summary.Label += " (CPU is a lower bound: a process's CPU could not be read)"
	case summary.CPUPartial && cpuRate != nil:
		summary.Label += " (CPU is a lower bound while a process is being measured)"
	}
	if summary.MemoryPartial && memoryTotal != nil {
		summary.Label += " (memory is a lower bound: a process's memory could not be read)"
	}
	if summary.Approximate {
		summary.Label += " (approximate)"
	}
	return summary
}

// resourceBasisKernel is the basis of a kernel reading of any process.
const resourceBasisKernel = "proc_pid_rusage"

// loadWords is "~45% of one core · 1.2 GB", "under 1% of one core · 340 MB",
// or the memory alone before a second reading gives CPU; a figure that
// could not be read is left out.
func loadWords(cpu *float64, memory *uint64) string {
	parts := []string{}
	if cpu != nil {
		if percent := math.Round(*cpu); percent < 1 {
			parts = append(parts, "under 1% of one core")
		} else {
			parts = append(parts, fmt.Sprintf("~%.0f%% of one core", percent))
		}
	}
	if memory != nil {
		parts = append(parts, compactBytes(*memory))
	}
	return strings.Join(parts, " · ")
}

// resourceRoleLabel names a process for its line.
func resourceRoleLabel(process resourceProcess) string {
	switch {
	case process.Role == "broker":
		return "Pharos app"
	case process.Role == "native-app":
		return "Pharos (Mac app)"
	case process.Role == "service" && process.LibraryName != "":
		return "Library service (" + process.LibraryName + ")"
	case process.Role == "service":
		return "Library service"
	case strings.HasPrefix(process.Role, "worker:"):
		return "Worker (" + strings.TrimPrefix(process.Role, "worker:") + fmt.Sprintf(", PID %d)", process.PID)
	case strings.HasPrefix(process.Role, "cli:"):
		return "pharos " + strings.TrimPrefix(process.Role, "cli:") + " (Terminal)"
	}
	return process.Role
}

// roundedPercent is CPU to a whole percent, and roundedBytes memory to two
// significant figures: what the panel shows, and coarse enough that an idle
// library's snapshot does not change with every reading.
func roundedPercent(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(math.Round(*value))
}

// roundedRate is a disk rate in bytes per second, to two significant
// figures.
func roundedRate(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(float64(roundedBytes(uint64(math.Round(*value)))))
}

// roundedMemory is a memory figure to two significant figures, null when
// it was not read.
func roundedMemory(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	return new(roundedBytes(*value))
}

func roundedBytes(value uint64) uint64 {
	if value < 100 {
		return value
	}
	scale := math.Pow(10, math.Floor(math.Log10(float64(value)))-1)
	return uint64(math.Round(float64(value)/scale) * scale)
}

// compactBytes is a size in two significant figures: "340 MB", "1.2 GB".
func compactBytes(value uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	amount, unit := float64(roundedBytes(value)), 0
	for amount >= 1000 && unit < len(units)-1 {
		amount /= 1000
		unit++
	}
	if amount < 10 && unit > 0 {
		return fmt.Sprintf("%.1f %s", amount, units[unit])
	}
	return fmt.Sprintf("%.0f %s", amount, units[unit])
}

// serviceResources is a library service's resources: itself and the other
// Pharos processes on this Mac holding a live lease on its library (§6.5;
// their PID is read from the lease file while its lock is held). Leases of
// another Mac's processes (a library on a shared drive) are not this Mac's
// to read.
//
// While a history collector runs on this service's sampler, its latest
// summary is the snapshot's, so the two never read the processes twice. The
// leases are still status's own fresh probe: the collector rereads lease
// files only every resourceLeaseIdle when idle, so a summary missing a live
// lease (a command just started in Terminal) is not used, and the collector
// is told to probe again.
func (s *Server) serviceResources() *resourceSummary {
	if s.resourceBrokerOwned {
		return nil
	}
	processes := s.serviceResourceProcesses()
	if latest, ok := s.resources.history.latest(""); ok {
		if coversResourceRoots(latest, processes) {
			return latest
		}
		s.historyLeases.stale.Store(true)
		s.resources.history.poke()
	}
	return withProcessGPU(summarizeResources("Pharos", s.resources.sample(processes)))
}

// coversResourceRoots reports whether summary read every root besides this
// process (a nil summary read none).
func coversResourceRoots(summary *resourceSummary, roots []resourceProcess) bool {
	for _, root := range roots {
		if root.PID == os.Getpid() {
			continue
		}
		if summary == nil || !slices.ContainsFunc(summary.Processes, func(reading resourceReading) bool { return reading.PID == root.PID }) {
			return false
		}
	}
	return true
}

// serviceResourceProcesses are the roots a service samples: itself and its
// library's live leases on this Mac.
func (s *Server) serviceResourceProcesses() []resourceProcess {
	processes := []resourceProcess{{PID: os.Getpid(), Role: "service"}}
	return append(processes, leaseProcesses(s.leases.probe(false), "", "")...)
}

// serviceHistoryProcesses uses the history collector's slower lease cache.
// Status and lifecycle checks retain their existing fresh leaseWatch policy
// (serviceResources).
func (s *Server) serviceHistoryProcesses(at time.Time) []resourceProcess {
	processes := []resourceProcess{{PID: os.Getpid(), Role: "service"}}
	if s.leases.dirs != nil {
		processes = append(processes, leaseProcesses(s.historyLeases.probe(s.leases.dirs(), at), "", "")...)
	}
	return processes
}

// leaseProcesses are the live leases' processes on this Mac.
func leaseProcesses(probes []leaseProbe, library, name string) []resourceProcess {
	processes := []resourceProcess{}
	host := currentHost().ID
	for _, probe := range probes {
		if probe.Live && probe.Info != nil && probe.Info.PID > 0 && probe.Info.PID != os.Getpid() && probe.Info.HostID == host {
			processes = append(processes, resourceProcess{PID: probe.Info.PID, Role: defaultString(probe.Info.Process, "cli"), Library: library, LibraryName: name})
		}
	}
	return processes
}

// snapshotProcesses are the processes a service's snapshot listed besides
// itself: the broker reads them again with its own sampler.
func snapshotProcesses(snapshot map[string]any, library, name string) []resourceProcess {
	var summary resourceSummary
	data, err := json.Marshal(snapshot["resources"])
	if err != nil || json.Unmarshal(data, &summary) != nil {
		return nil
	}
	processes := []resourceProcess{}
	for _, reading := range summary.Processes {
		if reading.Role != "service" && reading.PID > 0 {
			processes = append(processes, resourceProcess{PID: reading.PID, Role: reading.Role, Library: library, LibraryName: name})
		}
	}
	return processes
}
