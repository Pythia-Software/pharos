package archive

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
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

// processUsage is one reading of a process.
type processUsage struct {
	// CPUSeconds is the process's own user and system time; ChildCPUSeconds
	// that of its exited, reaped children.
	CPUSeconds, ChildCPUSeconds float64
	// Footprint is the physical footprint; Resident the resident set size,
	// which is what ps -o rss reports.
	Footprint, Resident uint64
	// Started identifies the process across PID reuse (0: unknown).
	Started uint64
	// DiskBytes is what the process has read from and written to disk;
	// DiskKnown is false where it cannot be read (off macOS).
	DiskBytes uint64
	DiskKnown bool
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
	// bytes per second; nil after the first reading.
	percent, disk *float64
	err           error
}

func newResourceSampler() *resourceSampler {
	return &resourceSampler{read: processUsageOf, basis: resourceBasis, now: time.Now, byPID: map[int]*resourceTrack{}}
}

// resourceReading is one process in a snapshot's resources.
type resourceReading struct {
	PID         int      `json:"pid"`
	Role        string   `json:"role"`
	Library     string   `json:"library,omitempty"`
	CPUPercent  *float64 `json:"cpu_percent"`
	MemoryBytes uint64   `json:"memory_bytes"`
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
	memory    uint64
	at        time.Time
	started   uint64
}

// resourceSummary is a snapshot's resources: the totals of its processes,
// and each of them.
type resourceSummary struct {
	// GPU measures only this summary's Pharos process tree.
	GPU                *gpuReading `json:"gpu,omitempty"`
	CPUPercent         *float64    `json:"cpu_percent"`
	MemoryBytes        uint64      `json:"memory_bytes"`
	DiskBytesPerSecond *float64    `json:"disk_bytes_per_second"`
	// A process without a second sample has no rate yet. Keep the measured part
	// visible, and say explicitly when it is a lower bound.
	CPUPartial  bool   `json:"cpu_partial"`
	DiskPartial bool   `json:"disk_partial"`
	SampledAt   string `json:"sampled_at"`
	Basis       string `json:"basis"`
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
				cores := float64(resourceCores())
				own := usage.CPUSeconds - track.usage.CPUSeconds
				children := usage.ChildCPUSeconds - track.usage.ChildCPUSeconds
				if process.OwnOnly {
					children = 0
				} else {
					children -= r.reapedChildOverlap(process.PID, children, track.at, present)
				}
				if children > elapsed*cores {
					// A child that ran longer than this interval could
					// (a reaped service, a long agent call): its time is
					// not this interval's load. Start again from here.
					track = &resourceTrack{at: now, usage: usage}
					break
				}
				percent := min(max(0, 100*(own+children)/elapsed), 100*cores)
				var disk *float64
				if previous := track.usage; usage.DiskKnown && previous.DiskKnown && usage.DiskBytes >= previous.DiskBytes {
					disk = new(float64(usage.DiskBytes-previous.DiskBytes) / elapsed)
				}
				track = &resourceTrack{at: now, usage: usage, percent: &percent, disk: disk,
					firstAt: previous.firstAt, firstCPU: previous.firstCPU, parent: parent}
			}
			if track.firstAt.IsZero() && err == nil {
				track.firstAt, track.firstCPU = now, usage.CPUSeconds+usage.ChildCPUSeconds
			}
			track.parent = parent
			r.byPID[process.PID] = track
		}
		track.asked = now
		if track.err != nil {
			continue
		}
		reading := resourceReading{PID: process.PID, Role: process.Role, Library: process.Library, Basis: r.basis,
			cpu: track.percent, disk: track.disk, memory: track.usage.Footprint, at: track.at, started: track.usage.Started}
		reading.CPUPercent, reading.MemoryBytes, reading.SampledAt = roundedPercent(track.percent), roundedBytes(track.usage.Footprint), formatTime(track.at)
		reading.DiskBytesPerSecond = roundedRate(track.disk)
		reading.Text = strings.Join(slices.DeleteFunc([]string{resourceRoleLabel(process), loadWords(track.percent, track.usage.Footprint)}, func(s string) bool { return s == "" }), " · ")
		readings = append(readings, reading)
	}
	for pid, track := range r.byPID {
		if now.Sub(track.asked) > resourceForget {
			delete(r.byPID, pid)
		}
	}
	return readings
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
		if !present[pid] && !child.reapedCovered && !child.firstAt.IsZero() {
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
	cpu, disk := 0.0, 0.0
	cpuCount, diskCount := 0, 0
	var memory uint64
	var at time.Time
	for _, reading := range readings {
		if reading.cpu == nil {
			summary.CPUPartial = true
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
		memory += reading.memory
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
	if cpuCount > 0 {
		// A total, like each figure, never exceeds every core (§10).
		cpuRate = new(min(cpu, 100*float64(resourceCores())))
	}
	if diskCount > 0 {
		diskRate = &disk
	}
	summary.CPUPercent, summary.MemoryBytes, summary.SampledAt = roundedPercent(cpuRate), roundedBytes(memory), formatTime(at)
	summary.DiskBytesPerSecond = roundedRate(diskRate)
	summary.Label = subject + " is using " + loadWords(cpuRate, memory)
	if summary.CPUPartial && cpuRate != nil {
		summary.Label += " (CPU is a lower bound while a process is being measured)"
	}
	if summary.Approximate {
		summary.Label += " (approximate)"
	}
	return summary
}

// resourceBasisKernel is the basis of a kernel reading of any process.
const resourceBasisKernel = "proc_pid_rusage"

// loadWords is "~45% of one core · 1.2 GB", "under 1% of one core · 340 MB",
// or the memory alone before a second reading gives CPU.
func loadWords(cpu *float64, memory uint64) string {
	parts := []string{}
	if cpu != nil {
		if percent := math.Round(*cpu); percent < 1 {
			parts = append(parts, "under 1% of one core")
		} else {
			parts = append(parts, fmt.Sprintf("~%.0f%% of one core", percent))
		}
	}
	parts = append(parts, compactBytes(memory))
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
// summary is the snapshot's, so the two never read the processes twice.
func (s *Server) serviceResources() *resourceSummary {
	if s.resourceBrokerOwned {
		return nil
	}
	if latest, ok := s.resources.history.latest(""); ok {
		return latest
	}
	return withProcessGPU(summarizeResources("Pharos", s.resources.sample(s.serviceResourceProcesses())))
}

// serviceResourceProcesses are the roots a service samples: itself and its
// library's live leases on this Mac.
func (s *Server) serviceResourceProcesses() []resourceProcess {
	processes := []resourceProcess{{PID: os.Getpid(), Role: "service"}}
	return append(processes, leaseProcesses(s.leases.probe(false), "", "")...)
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
