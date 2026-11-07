package archive

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// readyGPU samples a fresh interval without querying this Mac's registry.
func readyGPU(pid int, percent float64) (*gpuSampler, *int) {
	clock, reads := time.Now(), 0
	previous := gpuClient{id: 1, pid: pid, known: true}
	current := previous
	current.ns = uint64(percent / 100 * float64(gpuSampleInterval))
	return &gpuSampler{now: func() time.Time { return clock }, attempted: true, at: clock.Add(-gpuSampleInterval), supported: true,
		clients: map[uint64]gpuClientRate{1: {gpuClient: previous}},
		read: func() (gpuSnapshot, error) {
			reads++
			return gpuSnapshot{supported: true, clients: []gpuClient{current}}, nil
		}}, &reads
}

func TestGPUResourceSampling(t *testing.T) {
	clock := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	reads := 0
	own := gpuClient{id: 1, pid: 10, ns: 1e9, known: true}
	other := gpuClient{id: 2, pid: 20, ns: 100e9, known: true}
	snapshot, failure := gpuSnapshot{supported: true, clients: []gpuClient{own, other}}, error(nil)
	sampler := &gpuSampler{now: func() time.Time { return clock }, read: func() (gpuSnapshot, error) {
		reads++
		return snapshot, failure
	}}
	processes := []resourceReading{{PID: 10}, {PID: 10}} // repeated roots cannot double count
	first := sampler.sample(processes)
	if first.UtilizationPercent != nil || !first.Partial || first.SampledAt != formatTime(clock) || first.Basis != gpuResourceBasis {
		t.Fatalf("a GPU client needs a baseline: %+v", first)
	}
	var clients sync.WaitGroup
	for range 10 {
		clients.Go(func() { sampler.sample(processes) })
	}
	clients.Wait()
	if reads != 1 {
		t.Fatalf("%d queries for one sample", reads)
	}
	clock = clock.Add(gpuSampleInterval)
	own.ns += uint64(2 * time.Second)
	other.ns += uint64(5 * time.Second)
	snapshot.clients = []gpuClient{own, other}
	second := sampler.sample(processes)
	if second.UtilizationPercent == nil || *second.UtilizationPercent != 40 || second.Partial {
		t.Fatalf("only the selected process's GPU time: %+v", second)
	}
	if idle := sampler.sample([]resourceReading{{PID: 30}}); idle.UtilizationPercent == nil || *idle.UtilizationPercent != 0 {
		t.Fatalf("no GPU clients in the process tree means idle: %+v", idle)
	}
	if total := sampler.sample([]resourceReading{{PID: 10}, {PID: 20}}); total.UtilizationPercent == nil || *total.UtilizationPercent != 100 || reads != 2 {
		t.Fatalf("process totals share a sample and are capped at 100: %+v (%d reads)", total, reads)
	}
	// A new client gets no lifetime charge, but keeps a measured lower bound.
	clock = clock.Add(gpuSampleInterval)
	own.ns += uint64(time.Second)
	snapshot.clients = []gpuClient{own, {id: 3, pid: 10, ns: 1000e9, known: true}}
	if partial := sampler.sample(processes); partial.UtilizationPercent == nil || *partial.UtilizationPercent != 20 || !partial.Partial {
		t.Fatalf("new clients do not invent a spike: %+v", partial)
	}
	// Losing contexts can reset a client's total; start a new baseline.
	clock = clock.Add(gpuSampleInterval)
	own.ns = 0
	snapshot.clients = []gpuClient{own}
	if reset := sampler.sample(processes); reset.UtilizationPercent != nil || !reset.Partial {
		t.Fatalf("counter reset: %+v", reset)
	}
	clock = clock.Add(gpuSampleInterval)
	failure = errors.New("ioreg timed out")
	failed := sampler.sample(processes)
	if failed.UtilizationPercent != nil || sampler.sample(processes).UtilizationPercent != nil || reads != 5 {
		t.Fatalf("failures replace stale utilization and are cached: %+v (%d reads)", failed, reads)
	}
	clock = clock.Add(gpuSampleInterval)
	failure = nil
	if got := sampler.sample(processes); got.UtilizationPercent != nil {
		t.Fatalf("failure clears the baseline: %+v", got)
	}
	clock = clock.Add(gpuSampleInterval)
	if got := sampler.sample(processes); got.UtilizationPercent == nil || *got.UtilizationPercent != 0 {
		t.Fatalf("true idle: %+v", got)
	}
	clock = clock.Add(time.Hour)
	if got := sampler.sample(processes); got.UtilizationPercent != nil {
		t.Fatalf("long gaps are not current load: %+v", got)
	}
	clock = clock.Add(gpuSampleInterval)
	own.pid = 30 // a client cannot transfer its baseline to another PID
	snapshot.clients = []gpuClient{own}
	if got := sampler.sample([]resourceReading{{PID: 30}}); got.UtilizationPercent != nil {
		t.Fatalf("changed owner: %+v", got)
	}
	if (&gpuSampler{}).sample(processes) != nil {
		t.Fatal("a platform without a reader has no GPU metric")
	}
}

func TestGPUResourcePlist(t *testing.T) {
	xml := `<plist><array><dict><key>PerformanceStatistics</key><dict><key>Device Utilization %</key><integer>99</integer></dict>
 <key>IORegistryEntryChildren</key><array>
 <dict><key>IORegistryEntryID</key><integer>100</integer><key>IOUserClientCreator</key><string>pid 123, pharos-labeler-o</string>
 <key>AppUsage</key><array><dict><key>accumulatedGPUTime</key><integer>1000000000</integer></dict><dict><key>accumulatedGPUTime</key><integer>2000000000</integer></dict></array></dict>
 <dict><key>IORegistryEntryID</key><integer>200</integer><key>IOUserClientCreator</key><string>pid 456, Other</string>
 <key>AppUsage</key><array><dict><key>accumulatedGPUTime</key><integer>0</integer></dict></array></dict>
 <dict><key>IORegistryEntryID</key><integer>300</integer><key>IOUserClientCreator</key><string>pid 789, Unknown</string></dict>
 </array></dict></array></plist>`
	got, err := gpuClientsFromPlist([]byte(xml))
	if err != nil || !got.supported || len(got.clients) != 3 {
		t.Fatalf("GPU clients: %+v (%v)", got, err)
	}
	if got.clients[0] != (gpuClient{id: 100, pid: 123, ns: 3e9, known: true}) || !got.clients[1].known || got.clients[1].ns != 0 || got.clients[2].known {
		t.Fatalf("owner, summed contexts, true idle and missing counters: %+v", got)
	}
	for _, data := range []string{`<plist><array/></plist>`, `<plist><array><dict><key>PerformanceStatistics</key><dict><key>Device Utilization %</key><integer>99</integer></dict></dict></array></plist>`} {
		if got, err := gpuClientsFromPlist([]byte(data)); err != nil || got.supported {
			t.Fatalf("no process counters must not use whole-device load: %+v (%v)", got, err)
		}
	}
	if _, err := gpuClientsFromPlist([]byte(`<plist><array><dict>`)); err == nil {
		t.Fatal("malformed output must fail")
	}
}

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
		10: {CPUSeconds: 5, ChildCPUSeconds: 1, Footprint: 300e6, Started: 1, DiskBytes: 1e9, DiskKnown: true},
		20: {CPUSeconds: 2, Footprint: 41_234_567, Started: 7, DiskBytes: 5e6, DiskKnown: true},
	}}
	clock := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	sampler := &resourceSampler{read: source.read, basis: resourceBasisKernel, now: func() time.Time { return clock }, byPID: map[int]*resourceTrack{}}
	sample := func(processes []resourceProcess) []resourceReading {
		// The scripted PIDs are not in this Mac's real process table.
		table := make([]processNode, 0, len(processes))
		for _, process := range processes {
			table = append(table, processNode{PID: process.PID, Parent: 1})
		}
		return sampler.sampleWithTable(processes, table)
	}
	processes := []resourceProcess{{PID: 10, Role: "service"}, {PID: 20, Role: "cli:ingest"}, {PID: 30, Role: "cli:capture"}, {PID: 10, Role: "service"}}

	first := sample(processes)
	if len(first) != 2 || first[0].PID != 10 || first[1].PID != 20 || sampler.reads != 3 {
		t.Fatalf("each PID read once, the unreadable one left out: %+v (%d reads)", first, sampler.reads)
	}
	if first[0].CPUPercent != nil || first[0].DiskBytesPerSecond != nil || first[0].MemoryBytes != 300e6 || first[1].MemoryBytes != 41e6 {
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
	if again := sample(processes[:2]); sampler.reads != 3 || again[0].SampledAt != first[0].SampledAt {
		t.Fatalf("a reading younger than the interval is reused: %d reads", sampler.reads)
	}

	// Two seconds after the first: 1 s of own CPU and 0.5 s of a child that
	// exited (a git call) is 75 % of one core; 20 used 0.004 s.
	clock = clock.Add(2*time.Second - resourceSampleInterval/2)
	// Disk is bytes read and written per second: 10 moved 24 MB, 20 1 MB.
	source.set(10, processUsage{CPUSeconds: 6, ChildCPUSeconds: 1.5, Footprint: 1_234_000_000, Started: 1, DiskBytes: 1.024e9, DiskKnown: true})
	source.set(20, processUsage{CPUSeconds: 2.004, Footprint: 41e6, Started: 7, DiskBytes: 6e6, DiskKnown: true})
	second := sample(processes[:2])
	if second[0].CPUPercent == nil || *second[0].CPUPercent != 75 || *second[1].CPUPercent != 0 {
		t.Fatalf("CPU over the interval: %+v", second)
	}
	if second[0].DiskBytesPerSecond == nil || *second[0].DiskBytesPerSecond != 12e6 || *second[1].DiskBytesPerSecond != 500e3 {
		t.Fatalf("disk over the interval: %v %v", second[0].DiskBytesPerSecond, second[1].DiskBytesPerSecond)
	}
	summary = summarizeResources("Pharos", second)
	if *summary.DiskBytesPerSecond != 13e6 {
		t.Fatalf("disk total, to two figures: %v", *summary.DiskBytesPerSecond)
	}
	if *summary.CPUPercent != 75 || summary.Label != "Pharos is using ~75% of one core · 1.3 GB" || summary.SampledAt != formatTime(clock) {
		t.Fatalf("totals: %+v", summary)
	}
	if second[1].Text != "pharos ingest (Terminal) · under 1% of one core · 41 MB" {
		t.Fatalf("a nearly idle process: %q", second[1].Text)
	}

	// A new worker does not erase the measured CPU and I/O rates. Both
	// totals are explicitly lower bounds until that worker has a delta.
	clock = clock.Add(2 * time.Second)
	source.set(30, processUsage{CPUSeconds: 1, Footprint: 10e6, Started: 3})
	mixed := sample(processes[:3])
	if total := summarizeResources("Pharos", mixed); len(mixed) != 3 || total.CPUPercent == nil || !total.CPUPartial || !total.DiskPartial ||
		!strings.Contains(total.Label, "lower bound") || total.DiskBytesPerSecond == nil {
		t.Fatalf("a total with a process still measuring: %+v", total)
	}

	// PID 20 exits and its number is reused: no delta across processes.
	clock = clock.Add(2 * time.Second)
	source.set(20, processUsage{CPUSeconds: 0.1, Footprint: 5e6, Started: 99})
	if reused := sample(processes[1:2]); reused[0].CPUPercent != nil {
		t.Fatalf("a reused PID is a new process: %+v", reused)
	}
	// A process not asked about is forgotten after resourceForget.
	clock = clock.Add(resourceForget + time.Second)
	sample(processes[1:2])
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
	sample(children)
	clock = clock.Add(2 * time.Second)
	source.set(40, processUsage{CPUSeconds: 10.2, ChildCPUSeconds: 2400, Started: 4, Footprint: 1e6})
	source.set(41, processUsage{CPUSeconds: 30, Started: 5, Footprint: 1e6})
	source.set(42, processUsage{CPUSeconds: 10.1, ChildCPUSeconds: 2400, Started: 6, Footprint: 1e6})
	jumped := sample(children)
	if jumped[0].CPUPercent != nil || *jumped[1].CPUPercent != 400 || *jumped[2].CPUPercent != 5 {
		t.Fatalf("child jump, clamp and own-only: %v %v %v", jumped[0].cpu, jumped[1].cpu, jumped[2].cpu)
	}
	clock = clock.Add(2 * time.Second)
	source.set(40, processUsage{CPUSeconds: 10.4, ChildCPUSeconds: 2400.2, Started: 4, Footprint: 1e6})
	if after := sample(children[:1]); after[0].CPUPercent == nil || *after[0].CPUPercent != 20 {
		t.Fatalf("after the jump, the next interval counts again: %+v", after)
	}
	// A gap longer than resourceStaleGap (no page open) is a new baseline,
	// not an average shown as the current load.
	clock = clock.Add(resourceStaleGap + time.Second)
	source.set(40, processUsage{CPUSeconds: 20, ChildCPUSeconds: 2400.2, Started: 4, Footprint: 1e6})
	if stale := sample(children[:1]); stale[0].CPUPercent != nil {
		t.Fatalf("an average over a long gap: %+v", stale)
	}
	clock = clock.Add(2 * time.Second)
	source.set(40, processUsage{CPUSeconds: 20.1, ChildCPUSeconds: 2400.2, Started: 4, Footprint: 1e6})
	if fresh := sample(children[:1]); fresh[0].CPUPercent == nil || *fresh[0].CPUPercent != 5 {
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
	found := false
	for _, process := range expandResourceProcesses([]resourceProcess{{PID: os.Getpid(), Role: "service"}}, resourceProcessTable()) {
		if process.PID == child.Process.Pid {
			found = process.Parent == os.Getpid() && strings.HasPrefix(process.Role, "worker:")
		}
	}
	if !found {
		t.Fatal("live subprocess missing from resource rows")
	}
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

// Sampling's cost is measured on demand; the scripted sampler tests above
// cover its behavior without repeatedly querying this Mac's process table.
func BenchmarkResourceSampling(b *testing.B) {
	override(b, &resourceSampleInterval, 0)
	sampler := newResourceSampler()
	processes := []resourceProcess{{PID: os.Getpid(), Role: "broker"}}
	if runtime.GOOS == "darwin" {
		// Ten processes: more than a broker with several libraries and
		// Terminal commands has.
		for range 9 {
			child := exec.Command("sleep", "30")
			if err := child.Start(); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { child.Process.Kill(); child.Wait() })
			processes = append(processes, resourceProcess{PID: child.Process.Pid, Role: "service"})
		}
	}
	cpu := func() float64 {
		var usage syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
		return float64(usage.Utime.Nano()+usage.Stime.Nano()) / 1e9
	}
	before := cpu()
	b.ResetTimer()
	for range b.N {
		summarizeResources("Pharos", sampler.sample(processes))
	}
	b.StopTimer()
	perSample := (cpu() - before) / float64(b.N)
	share := 100 * perSample / (1500 * time.Millisecond).Seconds()
	b.ReportMetric(perSample*1e6, "cpu-µs/sample")
	b.ReportMetric(share, "core-%")
}

func TestResourceWorkerTree(t *testing.T) {
	roots := []resourceProcess{{PID: 10, Role: "service", Library: "a"}, {PID: 20, Role: "cli:ingest", Library: "b"}}
	table := []processNode{{PID: 12, Parent: 11, Name: "model"}, {PID: 11, Parent: 10, Name: "labeler"}, {PID: 20, Parent: 10}, {PID: 21, Parent: 20, Name: "git"}, {PID: 10, Parent: 1}, {PID: 99, Parent: 1}}
	processes := expandResourceProcesses(roots, table)
	if len(processes) != 5 {
		t.Fatalf("process tree: %+v", processes)
	}
	source := &fakeUsage{procs: map[int]processUsage{}}
	clock := time.Now()
	sampler := &resourceSampler{read: source.read, now: func() time.Time { return clock }, byPID: map[int]*resourceTrack{}}
	for _, p := range processes {
		if p.OwnOnly || (p.PID == 21 && p.Library != "b") || (p.PID == 12 && p.Library != "a") {
			t.Fatalf("attribution: %+v", p)
		}
		source.set(p.PID, processUsage{CPUSeconds: 1})
	}
	sampler.sampleWithTable(processes, table)
	clock = clock.Add(2 * time.Second)
	for _, p := range processes {
		source.set(p.PID, processUsage{CPUSeconds: 2})
	}
	summary := summarizeResources("Pharos", sampler.sampleWithTable(processes, table))
	if summary.CPUPercent == nil || *summary.CPUPercent != 250 {
		t.Fatalf("workers counted once: %+v", summary)
	}
}

func TestResourceSamplerCountsReapedHelpersWithoutRepeatingLiveChildren(t *testing.T) {
	source := &fakeUsage{procs: map[int]processUsage{10: {Started: 1, CPUSeconds: 10}, 11: {Started: 2, CPUSeconds: 5}}}
	clock := time.Now()
	sampler := &resourceSampler{read: source.read, basis: resourceBasisKernel, now: func() time.Time { return clock }, byPID: map[int]*resourceTrack{}}
	roots := []resourceProcess{{PID: 10, Role: "service"}}
	live := []processNode{{PID: 10, Parent: 1}, {PID: 11, Parent: 10, Name: "worker"}}
	sampler.sampleWithTable(roots, live)
	clock = clock.Add(2 * time.Second)
	source.set(10, processUsage{Started: 1, CPUSeconds: 10.2})
	source.set(11, processUsage{Started: 2, CPUSeconds: 5.4})
	if got := summarizeResources("Pharos", sampler.sampleWithTable(roots, live)); got.CPUPercent == nil || *got.CPUPercent != 30 {
		t.Fatalf("both live processes counted once: %+v", got)
	}
	// The worker exits with 6 s of lifetime CPU. Its already sampled 5.4 s
	// must be deducted from the parent's child counter. A 0.4 s helper
	// that lived entirely between samples contributes through that counter.
	clock = clock.Add(2 * time.Second)
	source.set(10, processUsage{Started: 1, CPUSeconds: 10.4, ChildCPUSeconds: 6.4})
	if got := summarizeResources("Pharos", sampler.sampleWithTable(roots, live[:1])); got.CPUPercent == nil || *got.CPUPercent != 60 {
		t.Fatalf("reaped worker and short helper, without old CPU: %+v", got)
	}
	clock = clock.Add(2 * time.Second)
	source.set(10, processUsage{Started: 1, CPUSeconds: 10.6, ChildCPUSeconds: 6.4})
	if got := summarizeResources("Pharos", sampler.sampleWithTable(roots, live[:1])); got.CPUPercent == nil || *got.CPUPercent != 10 {
		t.Fatalf("reaped CPU is charged only once: %+v", got)
	}
	t.Run("nested workers exit together", func(t *testing.T) {
		source := &fakeUsage{procs: map[int]processUsage{
			10: {Started: 1, CPUSeconds: 10}, 11: {Started: 2, CPUSeconds: 5}, 12: {Started: 3, CPUSeconds: 10.2},
		}}
		clock := time.Now()
		sampler := &resourceSampler{read: source.read, basis: resourceBasisKernel, now: func() time.Time { return clock }, byPID: map[int]*resourceTrack{}}
		roots := []resourceProcess{{PID: 10, Role: "service"}}
		tree := []processNode{{PID: 10, Parent: 1}, {PID: 11, Parent: 10, Name: "worker"}, {PID: 12, Parent: 11, Name: "nested"}}
		sampler.sampleWithTable(roots, tree)
		clock = clock.Add(2 * time.Second)
		source.set(10, processUsage{Started: 1, CPUSeconds: 10.2})
		source.set(11, processUsage{Started: 2, CPUSeconds: 5.2})
		source.set(12, processUsage{Started: 3, CPUSeconds: 10.4})
		sampler.sampleWithTable(roots, tree)
		// The worker reaps its nested child, then the service reaps the
		// worker. Both lifetimes arrive in the service's child counter.
		clock = clock.Add(2 * time.Second)
		source.set(10, processUsage{Started: 1, CPUSeconds: 10.4, ChildCPUSeconds: 16.0})
		got := summarizeResources("Pharos", sampler.sampleWithTable(roots, tree[:1]))
		if got.CPUPercent == nil || *got.CPUPercent != 30 {
			t.Fatalf("the whole exited subtree has 30%% current CPU, not its lifetime: %+v", got)
		}
	})
	t.Run("cached snapshot still names the exited worker", func(t *testing.T) {
		source := &fakeUsage{procs: map[int]processUsage{
			10: {Started: 1, CPUSeconds: 10}, 11: {Started: 2, CPUSeconds: 5.2},
		}}
		clock := time.Now()
		sampler := &resourceSampler{read: source.read, basis: resourceBasisKernel, now: func() time.Time { return clock }, byPID: map[int]*resourceTrack{}}
		// Put the cached worker first: it must not be shown as live or
		// discard its counters before the parent is sampled.
		roots := []resourceProcess{{PID: 11, Role: "worker:git"}, {PID: 10, Role: "service"}}
		tree := []processNode{{PID: 10, Parent: 1}, {PID: 11, Parent: 10, Name: "git"}}
		sampler.sampleWithTable(roots, tree)
		clock = clock.Add(2 * time.Second)
		source.set(10, processUsage{Started: 1, CPUSeconds: 10.2})
		source.set(11, processUsage{Started: 2, CPUSeconds: 5.4})
		sampler.sampleWithTable(roots, tree)
		clock = clock.Add(2 * time.Second)
		source.set(10, processUsage{Started: 1, CPUSeconds: 10.6, ChildCPUSeconds: 5.8})
		source.mu.Lock()
		delete(source.procs, 11)
		source.mu.Unlock()
		got := summarizeResources("Pharos", sampler.sampleWithTable(roots, tree[:1]))
		if got.CPUPercent == nil || *got.CPUPercent != 40 {
			t.Fatalf("an exited cached root has 40%% current CPU, not 310%%: %+v", got)
		}
		if len(got.Processes) != 1 || got.Processes[0].PID != 10 {
			t.Fatalf("the cached worker is no longer live: %+v", got.Processes)
		}
	})
}
