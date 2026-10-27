package archive

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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
// CPU is the change in user plus system time (the process's own and that of
// its exited children: git, gh, diskutil and the CLIs count when they exit,
// all at once) over the wall time between two samples, as a percentage of
// one core. Memory is the physical footprint, what Activity Monitor shows.
// Attribution stops at the process: which operation used the CPU is not
// measured.

// resourceSampleInterval is the least time between two readings of one
// process; a snapshot computed sooner reuses the last reading.
var resourceSampleInterval = 1500 * time.Millisecond

// resourceForget is how long a process not asked about keeps its last
// reading, so a snapshot that skipped it can still compute a delta.
var resourceForget = 5 * time.Minute

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
}

// resourceProcess is one process a snapshot asks about.
type resourceProcess struct {
	PID int
	// Role is service, broker, or the process name of a lease (cli:ingest).
	Role string
	// Library is the registered library a broker reading belongs to.
	Library, LibraryName string
}

type resourceSampler struct {
	mu    sync.Mutex
	read  func(pid int) (processUsage, error)
	basis string
	now   func() time.Time
	byPID map[int]*resourceTrack
	// reads counts system calls, for tests.
	reads int
}

type resourceTrack struct {
	at, asked time.Time
	usage     processUsage
	// percent is the CPU over the interval that ended at at; nil after the
	// first reading.
	percent *float64
	err     error
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
	SampledAt   string   `json:"sampled_at"`
	Basis       string   `json:"basis"`
	// Text is the server's line for the process ("Library service · 12% of
	// one core · 340 MB").
	Text string `json:"text"`
	// exact values, before rounding for the document.
	cpu    *float64
	memory uint64
	at     time.Time
}

// resourceSummary is a snapshot's resources: the totals of its processes,
// and each of them.
type resourceSummary struct {
	CPUPercent  *float64 `json:"cpu_percent"`
	MemoryBytes uint64   `json:"memory_bytes"`
	SampledAt   string   `json:"sampled_at"`
	Basis       string   `json:"basis"`
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
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	readings := []resourceReading{}
	seen := map[int]bool{}
	for _, process := range processes {
		if process.PID <= 0 || seen[process.PID] {
			continue
		}
		seen[process.PID] = true
		track := r.byPID[process.PID]
		if track == nil || now.Sub(track.at) >= resourceSampleInterval {
			r.reads++
			usage, err := r.read(process.PID)
			switch {
			case err != nil:
				track = &resourceTrack{at: now, err: err}
			case track == nil || track.err != nil || track.usage.Started != usage.Started:
				track = &resourceTrack{at: now, usage: usage}
			default:
				elapsed := now.Sub(track.at).Seconds()
				used := usage.CPUSeconds + usage.ChildCPUSeconds - track.usage.CPUSeconds - track.usage.ChildCPUSeconds
				percent := max(0, 100*used/elapsed)
				track = &resourceTrack{at: now, usage: usage, percent: &percent}
			}
			r.byPID[process.PID] = track
		}
		track.asked = now
		if track.err != nil {
			continue
		}
		reading := resourceReading{PID: process.PID, Role: process.Role, Library: process.Library, Basis: r.basis,
			cpu: track.percent, memory: track.usage.Footprint, at: track.at}
		reading.CPUPercent, reading.MemoryBytes, reading.SampledAt = roundedPercent(track.percent), roundedBytes(track.usage.Footprint), formatTime(track.at)
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

// summarizeResources totals readings under label's subject ("Pharos").
// With nothing readable it is nil, which keeps the panel's Load row out.
func summarizeResources(subject string, readings []resourceReading) *resourceSummary {
	if len(readings) == 0 {
		return nil
	}
	summary := &resourceSummary{Processes: readings, Basis: readings[0].Basis}
	var cpu *float64
	var memory uint64
	var at time.Time
	for _, reading := range readings {
		if reading.cpu != nil {
			cpu = new(float64(cpuOrZero(cpu) + *reading.cpu))
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
	summary.CPUPercent, summary.MemoryBytes, summary.SampledAt = roundedPercent(cpu), roundedBytes(memory), formatTime(at)
	summary.Label = subject + " is using " + loadWords(cpu, memory)
	if summary.Approximate {
		summary.Label += " (approximate)"
	}
	return summary
}

// resourceBasisKernel is the basis of a kernel reading of any process.
const resourceBasisKernel = "proc_pid_rusage"

func cpuOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

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
	case process.Role == "service" && process.LibraryName != "":
		return "Library service (" + process.LibraryName + ")"
	case process.Role == "service":
		return "Library service"
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
func (s *Server) serviceResources() *resourceSummary {
	processes := []resourceProcess{{PID: os.Getpid(), Role: "service"}}
	processes = append(processes, leaseProcesses(s.leases.probe(false), "", "")...)
	return summarizeResources("Pharos", s.resources.sample(processes))
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
