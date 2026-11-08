package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"syscall"
	"testing"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// The manager's shared warm worker is attributed only during verified
// exclusive leases, including overlap with a caller without task telemetry.
func TestTaskResourcesTrackActualLabelerOwnership(t *testing.T) {
	c, _ := testCatalog(t)
	c.ops = newOpRegistry("service")
	c.ops.telemetry = true
	settings := labelsTestSettings(t, "ok")
	model, err := labelrt.LoadModel(settings.ModelDir, settings.Variant)
	if err != nil {
		t.Fatal(err)
	}
	cfg := labelrt.RunnerConfig{Worker: settings.Worker, Workers: 1, Threads: 1}
	resources := newTaskResources(taskIdentity{Kind: "analysis.labels", ID: "first"}, time.Now)
	runner, release, err := c.labelerRunner(withTaskResources(t.Context(), resources), settings, model, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other, releaseOther, err := c.labelerRunner(t.Context(), settings, model, cfg)
	if err != nil || other != runner {
		t.Fatalf("a shared warm runner: %v", err)
	}
	releaseOther()
	release()
	if len(resources.helpers) == 0 || resources.helpers[0].Exclusive {
		t.Fatal("overlapping use was attributed exclusively")
	}
	exclusive := newTaskResources(taskIdentity{Kind: "analysis.labels", ID: "second"}, time.Now)
	warm, releaseWarm, err := c.labelerRunner(withTaskResources(t.Context(), exclusive), settings, model, cfg)
	if err != nil || warm != runner {
		t.Fatalf("a worker changed at an ordinary batch boundary: %v", err)
	}
	releaseWarm()
	if len(exclusive.helpers) == 0 || !exclusive.helpers[0].Exclusive || len(c.labelerUses) != 0 {
		t.Fatal("the next exclusive lease lost attribution or retained ownership")
	}
}

// A task's record (docs/work-resource-accounting.md): the grant is kept apart
// from usage, Go process CPU and memory stay shared rather than guessed, and
// a helper's CPU is measured only over an exclusive ownership interval.
func TestTaskResourcesKeepBasisAndAllocationApart(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	// A task on a path that opens no active span (a backup, a parent
	// execution) has unmeasured active time, never a measured zero.
	untracked := newTaskResources(taskIdentity{Kind: "backup", ID: "op0"}, now)
	clock = clock.Add(time.Hour)
	if active := untracked.view(clock).Time["active"]; active.Value != nil || active.Basis != basisUnavailable || active.Unit != "seconds" {
		t.Fatalf("active time without spans is unknown: %+v", active)
	}
	r := newTaskResources(taskIdentity{Kind: "analysis.labels", ID: "op1", Batch: "b1"}, now)
	ctx := withTaskResources(context.Background(), r)
	if taskResourcesFrom(ctx) != r || taskResourcesFrom(context.Background()) != nil {
		t.Fatal("the record follows ctx")
	}
	var missing *taskResources
	missing.addRead(1)
	missing.activeSpan()()
	missing.holdBuffer(10)()
	missing.helperStart(1, "x", true).end()

	done := r.waitSpan(waitCapacity)
	clock = clock.Add(3 * time.Second)
	done()
	done()
	// Two concurrent admissions: 3 + 1 slots for five seconds, then 3 for
	// five more, is 35 slot-seconds and a peak of four.
	releaseThree := r.allocate(taskAllocation{CPUSlots: 3, GPU: 1})
	releaseOne := r.allocate(taskAllocation{CPUSlots: 1})
	outer := r.activeSpan()
	inner := r.activeSpan()
	clock = clock.Add(5 * time.Second)
	if live := r.view(clock).Allocation.Current; live == nil || live.CPUSlots != 4 || live.GPU != 1 {
		t.Fatalf("open grants add up: %+v", live)
	}
	releaseOne()
	releaseOne()
	inner()
	clock = clock.Add(5 * time.Second)
	outer()
	releaseThree()
	r.release()
	clock = clock.Add(2 * time.Second)
	r.progress("labels", "message", 40, 100)
	r.progress("labels", "message", 30, 100)
	r.addRead(1000)
	r.addWritten(200)
	free := r.holdBuffer(64)
	r.holdBuffer(32)()
	free()

	usage := map[int]processUsage{77: {Known: counterOwnCPU | counterMemory, CPUSeconds: 10, Footprint: 500e6, Started: 9}}
	read := func(pid int) (processUsage, error) {
		value, ok := usage[pid]
		if !ok {
			return processUsage{}, syscall.ESRCH
		}
		return value, nil
	}
	helper := r.helperStartWith(77, "labeler", true, read)
	usage[77] = processUsage{Known: counterOwnCPU | counterMemory, CPUSeconds: 12.5, Footprint: 600e6, Started: 9}
	helper.end()
	helper.end()
	sharedHelper := r.helperStartWith(77, "labeler", true, read)
	sharedHelper.shared()
	sharedHelper.end()

	view := r.view(clock)
	if got := *view.Time["active"].Value; got != 10 {
		t.Fatalf("overlapping spans count wall time once: %v", got)
	}
	if got := *view.Allocation.SlotSeconds.Value; got != 35 || view.Allocation.SlotSeconds.Basis != basisAllocated || view.Allocation.Current != nil || view.Allocation.PeakSlots != 4 {
		t.Fatalf("slot-seconds are a grant, not use: %+v", view.Allocation)
	}
	if got := view.Waits[waitCapacity]; got.Value == nil || *got.Value != 3 {
		t.Fatalf("capacity wait: %+v", view.Waits)
	}
	if view.Work["labels"].Done != 40 {
		t.Fatalf("progress is cumulative: %+v", view.Work)
	}
	if *view.IO["logical_read_bytes"].Value != 1000 || view.IO["logical_read_bytes"].Basis != basisCounted {
		t.Fatalf("logical IO is counted: %+v", view.IO)
	}
	if *view.Memory["owned_buffers_peak_bytes"].Value != 96 || *view.Memory["owned_buffers_bytes"].Value != 0 {
		t.Fatalf("owned buffers: %+v", view.Memory)
	}
	if process := view.Memory["process"]; process.Value != nil || process.Basis != basisShared {
		t.Fatalf("process memory is shared, not this task's: %+v", process)
	}
	if goCPU := view.CPU["go_process"]; goCPU.Value != nil || goCPU.Basis != basisShared {
		t.Fatalf("Go CPU is shared: %+v", goCPU)
	}
	if helpers := view.CPU["helpers"]; helpers.Value == nil || *helpers.Value != 2.5 || helpers.Basis != basisMeasured || !strings.Contains(helpers.Coverage, "lower bound") {
		t.Fatalf("exclusive helper CPU is measured; a shared interval makes it a lower bound: %+v", helpers)
	}
	if len(view.Helpers) != 2 || view.Helpers[1].CPU.Basis != basisShared {
		t.Fatalf("each interval is listed with its basis: %+v", view.Helpers)
	}
	if gpu := view.GPU["time"]; gpu.Value != nil || gpu.Basis != basisUnavailable {
		t.Fatalf("no GPU evidence is unavailable, not zero: %+v", gpu)
	}

	// A replaced helper is unavailable, never a negative or foreign delta.
	replaced := r.helperStartWith(77, "labeler", true, read)
	usage[77] = processUsage{Known: counterOwnCPU | counterMemory, CPUSeconds: 1, Started: 10}
	replaced.end()
	if last := r.view(clock).Helpers[2]; last.CPU.Value != nil || last.CPU.Basis != basisUnavailable {
		t.Fatalf("replaced helper: %+v", last)
	}

	final := r.finish(opSucceeded, nil, "")
	clock = clock.Add(time.Hour)
	if again := r.view(clock); *again.Time["elapsed"].Value != *final.Time["elapsed"].Value || again.EndedAt == "" {
		t.Fatalf("a finished record stops its clock: %+v", again.Time)
	}
	compact := final.compact()
	data, err := json.Marshal(compact)
	if err != nil || !strings.Contains(string(data), `"basis":"shared"`) || strings.Contains(string(data), "null") {
		t.Fatalf("the journal record keeps bases and drops nulls: %s %v", data, err)
	}
}

// The ledger keeps a bounded number of finished summaries per kind and in
// all, and exports them with the basis vocabulary.
func TestTaskResourceLedgerBounds(t *testing.T) {
	ledger := newTaskResourceLedger(2, 3)
	for i, kind := range []string{"a", "a", "a", "b", "b"} {
		ledger.record(taskResourceView{Version: 1, Task: taskIdentity{Kind: kind, ID: string(rune('0' + i))}})
	}
	ledger.record(taskResourceView{})
	all := ledger.export("", time.Now())
	if len(all.Tasks) != 3 || all.Tasks[0].Task.ID != "2" || all.Tasks[2].Task.ID != "4" || all.Bases[basisShared] == "" {
		t.Fatalf("bounded per kind and in total, newest last: %+v", all.Tasks)
	}
	if only := ledger.export("a", time.Now()); len(only.Tasks) != 1 || only.Kind != "a" {
		t.Fatalf("one kind: %+v", only.Tasks)
	}
}

// Profile labels name the task; empty fields are left out.
func TestTaskProfileLabels(t *testing.T) {
	got := map[string]string{}
	pprof.Do(context.Background(), taskProfileLabels(taskIdentity{Kind: "analysis.labels", ID: "op1"}), func(ctx context.Context) {
		pprof.ForLabels(ctx, func(key, value string) bool { got[key] = value; return true })
	})
	if len(got) != 2 || got["op_kind"] != "analysis.labels" || got["op_id"] != "op1" {
		t.Fatalf("labels: %v", got)
	}
}

// History collection reuses lease probes, so it does not open lease files on
// every library drive each tick; running work shortens the reuse.
func TestResourceLeaseProbesAreReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases")
	cache := resourceLeaseCache{}
	at := time.Unix(1000, 0)
	if probes := cache.probe([]string{path}, at); len(probes) != 0 {
		t.Fatalf("missing lease directory: %+v", probes)
	}
	// A file where the directory was cannot be listed: a probe reports it.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if probes := cache.probe([]string{path}, at.Add(resourceLeaseIdle-time.Second)); len(probes) != 0 {
		t.Fatal("an idle collector probed the drive again within its interval")
	}
	if probes := cache.probe([]string{path}, at.Add(resourceLeaseIdle)); len(probes) != 1 {
		t.Fatal("the idle interval passed without a probe")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cache.active = true
	if probes := cache.probe([]string{path}, at.Add(resourceLeaseIdle+resourceLeaseActive)); len(probes) != 0 {
		t.Fatal("running work did not shorten the interval")
	}

	// A process may exit and its PID be reused before the disk is probed
	// again. Cached ownership must not include that replacement's load.
	lease := filepath.Join(path, "lease")
	identity := uint64(7)
	cache = resourceLeaseCache{
		probes: map[string][]leaseProbe{path: {{Path: lease, Live: true, Info: &leaseInfo{PID: 42, HostID: currentHost().ID}}}},
		at:     map[string]time.Time{path: at}, started: map[string]uint64{lease: identity},
		read: func(int) (processUsage, error) { return processUsage{Started: identity}, nil },
	}
	if !cache.probe([]string{path}, at.Add(time.Second))[0].Live {
		t.Fatal("the original process lost its cached ownership")
	}
	identity++
	if cache.probe([]string{path}, at.Add(2*time.Second))[0].Live {
		t.Fatal("a replacement PID inherited the old lease's ownership")
	}

	// An unavailable reader or process identity is unknown coverage, never
	// proof of an absent process. A later good read can recover the same
	// cached lease without waking the drive to re-probe its file.
	cache.started[lease] = identity
	cache.read = func(int) (processUsage, error) { return processUsage{}, nil }
	override(t, &observeProcess, func(int) (processPresence, error) { return processUnknown, errProcessLivenessUnsupported })
	if cache.probe([]string{path}, at.Add(3*time.Second))[0].Live || len(cache.omitted) != 1 {
		t.Fatal("an unknown lease must be unmeasured with explicit coverage")
	}
	cache.read = func(int) (processUsage, error) { return processUsage{Started: identity}, nil }
	if !cache.probe([]string{path}, at.Add(4*time.Second))[0].Live || len(cache.omitted) != 0 {
		t.Fatal("a transient observation failure erased cached lease ownership")
	}
	cache.read = func(int) (processUsage, error) { return processUsage{}, os.ErrNotExist }
	override(t, &observeProcess, func(int) (processPresence, error) { return processAbsent, nil })
	if cache.probe([]string{path}, at.Add(5*time.Second))[0].Live || len(cache.omitted) != 0 {
		t.Fatal("confirmed process absence is not missing coverage")
	}

	// The collector already samples itself directly. Its own lease needs no
	// external-process identity reader, which may be unavailable on this OS.
	cache = resourceLeaseCache{
		probes: map[string][]leaseProbe{path: {{Path: lease, Live: true, Info: &leaseInfo{PID: os.Getpid(), HostID: currentHost().ID}}}},
		at:     map[string]time.Time{path: at},
		read: func(int) (processUsage, error) {
			t.Fatal("own lease was read as an external process")
			return processUsage{}, nil
		},
	}
	if !cache.probe([]string{path}, at.Add(time.Second))[0].Live || len(cache.omitted) != 0 {
		t.Fatal("the collector's own lease created a false coverage gap")
	}

	// Standalone collection must use the same idle-drive policy as the
	// app broker, rather than its status reader's one-second lease cache.
	servicePath := filepath.Join(t.TempDir(), "leases")
	service := &Server{leases: leaseWatch{dirs: func() []string { return []string{servicePath} }}}
	service.serviceHistoryProcesses(at)
	if err := os.WriteFile(servicePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	service.serviceHistoryProcesses(at.Add(time.Second))
	if probes := service.historyLeases.probe([]string{servicePath}, at.Add(2*time.Second)); len(probes) != 0 {
		t.Fatal("standalone history re-probed the drive within the idle interval")
	}

	// Status keeps its own fresh lease probe. A history summary that lacks
	// a live lease's process (a Terminal command started since the
	// collector's last probe) is not status's, and the collector probes
	// again on its next tick.
	self := resourceProcess{PID: os.Getpid(), Role: "service"}
	terminal := resourceProcess{PID: os.Getpid() + 1, Role: "cli"}
	summary := &resourceSummary{Processes: []resourceReading{{PID: self.PID, Role: "service"}}}
	if !coversResourceRoots(summary, []resourceProcess{self}) || coversResourceRoots(summary, []resourceProcess{self, terminal}) || coversResourceRoots(nil, []resourceProcess{self, terminal}) {
		t.Fatal("status reused a summary without every live lease")
	}
	service.historyLeases.stale.Store(true)
	if probes := service.historyLeases.probe([]string{servicePath}, at.Add(3*time.Second)); len(probes) != 1 {
		t.Fatal("a stale lease cache was not probed again")
	}
	if probes := service.historyLeases.probe([]string{servicePath}, at.Add(4*time.Second)); len(probes) != 1 || service.historyLeases.at[servicePath] != at.Add(3*time.Second) {
		t.Fatal("one stale mark forced more than one probe")
	}
}

// Platform readers can provide any subset of counters. Missing data or an
// unverifiable identity must never become measured zero, while CPU failure
// does not erase a valid endpoint memory reading.
func TestTaskHelperCountersAndIdentity(t *testing.T) {
	base := processUsage{Known: counterOwnCPU | counterMemory, CPUSeconds: 10, Footprint: 500, Started: 9}
	end := processUsage{Known: counterOwnCPU | counterMemory, CPUSeconds: 12, Footprint: 600, Started: 9}
	for _, tc := range []struct {
		name          string
		start, finish processUsage
		cpu, memory   bool
	}{
		{"all", base, end, true, true},
		{"real zero", processUsage{Known: counterOwnCPU | counterMemory, Started: 9}, processUsage{Known: counterOwnCPU | counterMemory, Started: 9}, true, true},
		{"no counters", processUsage{Started: 9}, processUsage{Started: 9}, false, false},
		{"CPU only", base, processUsage{Known: counterOwnCPU, CPUSeconds: 12, Started: 9}, true, false},
		{"memory only", base, processUsage{Known: counterMemory, Footprint: 600, Started: 9}, false, true},
		{"missing initial CPU", processUsage{Known: counterMemory, Footprint: 500, Started: 9}, end, false, true},
		{"reset CPU", base, processUsage{Known: counterOwnCPU | counterMemory, CPUSeconds: 1, Footprint: 600, Started: 9}, false, true},
		{"unknown identity", processUsage{Known: base.Known}, processUsage{Known: end.Known}, false, false},
		{"lost identity", base, processUsage{Known: end.Known}, false, false},
		{"replaced", base, processUsage{Known: end.Known, Started: 10}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTaskResources(taskIdentity{Kind: "analysis.labels"}, time.Now)
			usage := tc.start
			scope := r.helperStartWith(77, "labeler", true, func(int) (processUsage, error) { return usage, nil })
			usage = tc.finish
			scope.end()
			view := r.view(time.Now())
			helper := view.Helpers[0]
			if (helper.CPU.Value != nil) != tc.cpu || (helper.Footprint.Value != nil) != tc.memory {
				t.Fatalf("counter availability: %+v", helper)
			}
			cpu := view.CPU["helpers"]
			if tc.cpu {
				if cpu.Basis != basisMeasured || cpu.Value == nil || *cpu.Value != tc.finish.CPUSeconds-tc.start.CPUSeconds {
					t.Fatalf("own CPU delta: %+v", cpu)
				}
			} else if cpu.Value != nil || cpu.Basis != basisUnavailable || helper.CPU.Basis != basisUnavailable {
				t.Fatalf("unavailable CPU counted as measured: %+v %+v", cpu, helper)
			}
			if tc.memory && (*helper.Footprint.Value != float64(tc.finish.Footprint) || helper.CounterBasis != resourceBasis) {
				t.Fatalf("endpoint memory with reader basis: %+v", helper)
			}
		})
	}
}
