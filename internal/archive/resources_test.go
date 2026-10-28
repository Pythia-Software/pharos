package archive

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeUsage is a scripted process table for the sampler.
type fakeUsage struct {
	mu    sync.Mutex
	procs map[int]processUsage
}

func (f *fakeUsage) read(pid int) (processUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	usage, ok := f.procs[pid]
	if !ok {
		return processUsage{}, syscall.ESRCH
	}
	return usage, nil
}

func (f *fakeUsage) set(pid int, usage processUsage) {
	f.mu.Lock()
	f.procs[pid] = usage
	f.mu.Unlock()
}

// The sampler (§10): CPU is the change in own plus exited children's time
// over wall time, as a percentage of one core, from the second reading on;
// memory is the footprint. A reading younger than the interval is reused, a
// reused PID starts again, a process that cannot be read is left out, and
// the totals carry the server's line.
func TestResourceSamplerComputesLoadFromDeltas(t *testing.T) {
	source := &fakeUsage{procs: map[int]processUsage{
		10: {CPUSeconds: 5, ChildCPUSeconds: 1, Footprint: 300e6, Started: 1},
		20: {CPUSeconds: 2, Footprint: 41_234_567, Started: 7},
	}}
	clock := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	sampler := &resourceSampler{read: source.read, basis: resourceBasisKernel, now: func() time.Time { return clock }, byPID: map[int]*resourceTrack{}}
	processes := []resourceProcess{{PID: 10, Role: "service"}, {PID: 20, Role: "cli:ingest"}, {PID: 30, Role: "cli:capture"}, {PID: 10, Role: "service"}}

	first := sampler.sample(processes)
	if len(first) != 2 || first[0].PID != 10 || first[1].PID != 20 || sampler.reads != 3 {
		t.Fatalf("each PID read once, the unreadable one left out: %+v (%d reads)", first, sampler.reads)
	}
	if first[0].CPUPercent != nil || first[0].MemoryBytes != 300e6 || first[1].MemoryBytes != 41e6 {
		t.Fatalf("no CPU from one reading; memory rounded to two figures: %+v", first)
	}
	summary := summarizeResources("Pharos", first)
	if summary.CPUPercent != nil || summary.MemoryBytes != 340e6 || summary.Label != "Pharos is using 340 MB" || summary.Approximate || summary.Basis != resourceBasisKernel {
		t.Fatalf("totals before CPU is known: %+v", summary)
	}
	if first[1].Text != "pharos ingest (Terminal) · 41 MB" {
		t.Fatalf("each process has its line: %q", first[1].Text)
	}

	// Within the interval: the same readings, no system call.
	clock = clock.Add(resourceSampleInterval / 2)
	if again := sampler.sample(processes[:2]); sampler.reads != 3 || again[0].SampledAt != first[0].SampledAt {
		t.Fatalf("a reading younger than the interval is reused: %d reads", sampler.reads)
	}

	// Two seconds after the first: 1 s of own CPU and 0.5 s of a child that
	// exited (a git call) is 75 % of one core; 20 used 0.004 s.
	clock = clock.Add(2*time.Second - resourceSampleInterval/2)
	source.set(10, processUsage{CPUSeconds: 6, ChildCPUSeconds: 1.5, Footprint: 1_234_000_000, Started: 1})
	source.set(20, processUsage{CPUSeconds: 2.004, Footprint: 41e6, Started: 7})
	second := sampler.sample(processes[:2])
	if second[0].CPUPercent == nil || *second[0].CPUPercent != 75 || *second[1].CPUPercent != 0 {
		t.Fatalf("CPU over the interval: %+v", second)
	}
	summary = summarizeResources("Pharos", second)
	if *summary.CPUPercent != 75 || summary.Label != "Pharos is using ~75% of one core · 1.3 GB" || summary.SampledAt != formatTime(clock) {
		t.Fatalf("totals: %+v", summary)
	}
	if second[1].Text != "pharos ingest (Terminal) · under 1% of one core · 41 MB" {
		t.Fatalf("a nearly idle process: %q", second[1].Text)
	}

	// While one process has no figure yet, the total has none either:
	// memory only, never a CPU total that silently leaves one out.
	clock = clock.Add(2 * time.Second)
	source.set(30, processUsage{CPUSeconds: 1, Footprint: 10e6, Started: 3})
	mixed := sampler.sample(processes[:3])
	if total := summarizeResources("Pharos", mixed); len(mixed) != 3 || total.CPUPercent != nil || total.Label != "Pharos is using 1.3 GB" {
		t.Fatalf("a total with a process still measuring: %+v", total)
	}

	// PID 20 exits and its number is reused: no delta across processes.
	clock = clock.Add(2 * time.Second)
	source.set(20, processUsage{CPUSeconds: 0.1, Footprint: 5e6, Started: 99})
	if reused := sampler.sample(processes[1:2]); reused[0].CPUPercent != nil {
		t.Fatalf("a reused PID is a new process: %+v", reused)
	}
	// A process not asked about is forgotten after resourceForget.
	clock = clock.Add(resourceForget + time.Second)
	sampler.sample(processes[1:2])
	if _, ok := sampler.byPID[10]; ok {
		t.Fatal("a process nobody asks about is kept forever")
	}
	// Children: a reaped child that ran longer than the interval could (a
	// service that ran for hours, a long agent call) starts a new baseline;
	// a figure never exceeds every core; and the broker leaves out its
	// children, the services it reads itself.
	override(t, &resourceCores, func() int { return 4 })
	source.set(40, processUsage{CPUSeconds: 10, ChildCPUSeconds: 0, Started: 4, Footprint: 1e6})
	source.set(41, processUsage{CPUSeconds: 10, Started: 5, Footprint: 1e6})
	source.set(42, processUsage{CPUSeconds: 10, Started: 6, Footprint: 1e6})
	children := []resourceProcess{{PID: 40, Role: "service"}, {PID: 41, Role: "service"}, {PID: 42, Role: "broker", OwnOnly: true}}
	sampler.sample(children)
	clock = clock.Add(2 * time.Second)
	source.set(40, processUsage{CPUSeconds: 10.2, ChildCPUSeconds: 2400, Started: 4, Footprint: 1e6})
	source.set(41, processUsage{CPUSeconds: 30, Started: 5, Footprint: 1e6})
	source.set(42, processUsage{CPUSeconds: 10.1, ChildCPUSeconds: 2400, Started: 6, Footprint: 1e6})
	jumped := sampler.sample(children)
	if jumped[0].CPUPercent != nil || *jumped[1].CPUPercent != 400 || *jumped[2].CPUPercent != 5 {
		t.Fatalf("child jump, clamp and own-only: %v %v %v", jumped[0].cpu, jumped[1].cpu, jumped[2].cpu)
	}
	clock = clock.Add(2 * time.Second)
	source.set(40, processUsage{CPUSeconds: 10.4, ChildCPUSeconds: 2400.2, Started: 4, Footprint: 1e6})
	if after := sampler.sample(children[:1]); after[0].CPUPercent == nil || *after[0].CPUPercent != 20 {
		t.Fatalf("after the jump, the next interval counts again: %+v", after)
	}
	// A gap longer than resourceStaleGap (no page open) is a new baseline,
	// not an average shown as the current load.
	clock = clock.Add(resourceStaleGap + time.Second)
	source.set(40, processUsage{CPUSeconds: 20, ChildCPUSeconds: 2400.2, Started: 4, Footprint: 1e6})
	if stale := sampler.sample(children[:1]); stale[0].CPUPercent != nil {
		t.Fatalf("an average over a long gap: %+v", stale)
	}
	clock = clock.Add(2 * time.Second)
	source.set(40, processUsage{CPUSeconds: 20.1, ChildCPUSeconds: 2400.2, Started: 4, Footprint: 1e6})
	if fresh := sampler.sample(children[:1]); fresh[0].CPUPercent == nil || *fresh[0].CPUPercent != 5 {
		t.Fatalf("the reading after a gap: %+v", fresh)
	}

	// A total never exceeds every core either.
	capped := summarizeResources("Pharos", []resourceReading{{PID: 1, Basis: resourceBasisKernel, cpu: new(300.0)}, {PID: 2, Basis: resourceBasisKernel, cpu: new(300.0)}})
	if *capped.CPUPercent != 400 {
		t.Fatalf("the total is clamped to every core: %v", *capped.CPUPercent)
	}

	if summarizeResources("Pharos", nil) != nil {
		t.Fatal("nothing readable keeps the Load row out")
	}
	approximate := summarizeResources("Pharos", []resourceReading{{PID: 1, Basis: "self-reported", memory: 1e6}})
	if !approximate.Approximate || !strings.HasSuffix(approximate.Label, "(approximate)") {
		t.Fatalf("a process's own report is marked approximate: %+v", approximate)
	}
}

// The kernel's reading of this process agrees with getrusage (the timebase
// conversion), reads another of this user's processes, and its resident
// size is the figure ps reports, so the index runs' RSS samples stay
// comparable with those taken by forking ps.
func TestProcessUsageMatchesTheKernel(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("proc_pid_rusage is macOS only")
	}
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
	}
	usage, err := processUsageOf(os.Getpid())
	var self syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	getrusage := float64(self.Utime.Nano()+self.Stime.Nano()) / 1e9
	if err != nil || usage.CPUSeconds < getrusage*0.9 || usage.CPUSeconds > getrusage*1.1+0.05 {
		t.Fatalf("own CPU %.3f s, getrusage %.3f s (%v)", usage.CPUSeconds, getrusage, err)
	}
	if usage.Footprint == 0 || usage.Resident == 0 || usage.Started == 0 {
		t.Fatalf("memory and start time: %+v", usage)
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Wait()
	defer child.Process.Kill()
	if other, err := processUsageOf(child.Process.Pid); err != nil || other.Footprint == 0 {
		t.Fatalf("another of this user's processes: %+v %v", other, err)
	}
	if _, err := processUsageOf(1); err == nil && os.Geteuid() != 0 {
		t.Fatal("launchd's usage should not be readable by this user")
	}
	kernel, ps := processRSS(), processRSSFromPS()
	if kernel <= 0 || ps <= 0 || float64(kernel) < float64(ps)*0.8 || float64(kernel) > float64(ps)*1.25 {
		t.Fatalf("resident size %d, ps %d", kernel, ps)
	}
}

// Sampling must cost under about 1 % of one core (§10). It runs once per
// snapshot computation, at most every resourceSampleInterval, over the
// processes of every library; this measures samples of ten processes taken
// back to back and states the cost at one sample per interval.
func TestResourceSamplingIsCheap(t *testing.T) {
	override(t, &resourceSampleInterval, 0)
	sampler := newResourceSampler()
	processes := []resourceProcess{{PID: os.Getpid(), Role: "broker"}}
	if runtime.GOOS == "darwin" {
		// Ten processes: more than a broker with several libraries and
		// Terminal commands has.
		for range 9 {
			child := exec.Command("sleep", "30")
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { child.Process.Kill(); child.Wait() })
			processes = append(processes, resourceProcess{PID: child.Process.Pid, Role: "service"})
		}
	}
	const rounds = 2000
	cpu := func() float64 {
		var usage syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
		return float64(usage.Utime.Nano()+usage.Stime.Nano()) / 1e9
	}
	before := cpu()
	for range rounds {
		summarizeResources("Pharos", sampler.sample(processes))
	}
	perSample := (cpu() - before) / rounds
	share := 100 * perSample / (1500 * time.Millisecond).Seconds()
	t.Logf("one sample and its summary: %.1f µs of CPU; at one sample per 1.5 s, %.4f%% of one core", perSample*1e6, share)
	if share > 1 {
		t.Fatalf("sampling costs %.3f%% of one core", share)
	}
}
