package archive

import (
	"runtime"
	"slices"
	"sync"
	"time"
)

// syncMeasurementLimits is what the run history's resource figures can and
// cannot say, ending with where this platform reads them.
const syncMeasurementLimits = "CPU is process-wide plus completed child processes and includes concurrent Pharos work. CPU percent is relative to one core. RSS is sampled whole-process resident memory, not sync allocations; peaks between samples can be missed. Go allocation counters are process-wide. OS block counts are not byte counts. Telemetry overhead is included. A figure that could not be read, or whose counter reset during the run, is unavailable rather than zero. " + platformMeasurementLimits

// Resource figures that can be unavailable are pointers: null in the JSON
// (and in the stored history) when not measured, the same numeric field
// when it was. History recorded before this kept its numbers; a field it
// lacks stays missing.
type syncPhase struct {
	Name       string                 `json:"name"`
	Seconds    float64                `json:"seconds"`
	CPUSeconds *float64               `json:"cpu_seconds"`
	Work       []workStageMeasurement `json:"work,omitempty"`
}

type syncMeasurement struct {
	Forecasts     []forecastSnapshot `json:"forecasts,omitempty"`
	RunID         string             `json:"run_id"`
	Trigger       string             `json:"trigger,omitempty"`
	FinishedAt    string             `json:"finished_at,omitempty"`
	Sources       []string           `json:"sources,omitempty"`
	SourceResults []IngestResult     `json:"source_results,omitempty"`
	State         string             `json:"state"`
	Class         string             `json:"class"`
	ColdStart     bool               `json:"cold_start"`
	Seconds       float64            `json:"seconds"`
	CPUSeconds    *float64           `json:"cpu_seconds"`
	CPUPercent    *float64           `json:"cpu_percent_one_core"`
	RSSBaseline   *int64             `json:"rss_baseline_bytes"`
	RSSPeak       *int64             `json:"rss_observed_peak_bytes"`
	HeapBaseline  uint64             `json:"heap_baseline_bytes"`
	HeapPeak      uint64             `json:"heap_observed_peak_bytes"`
	Allocated     uint64             `json:"allocated_bytes"`
	ReadBlocks    *int64             `json:"read_blocks"`
	WrittenBlocks *int64             `json:"written_blocks"`
	Workspaces    int                `json:"changed_groups"`
	Conversations int                `json:"conversations"`
	Messages      int                `json:"messages_in_changed_groups"`
	Audit         auditResult        `json:"audit"`
	Phases        []syncPhase        `json:"phases"`
}

// processResources is one cumulative observation of this process's OS
// counters (processResourceUsage): own CPU, completed children's CPU, and
// Unix block counts (ru_inblock, ru_oublock of both), each available only
// when known names it. A platform with byte counters instead must not put
// them here.
type processResources struct {
	cpu, childCPU float64
	read, written int64
	known         resourceCounters
}

// totalCPU is own plus completed children's CPU, available only when both are.
func (r processResources) totalCPU() (float64, bool) {
	return r.cpu + r.childCPU, r.known.has(counterOwnCPU | counterChildCPU)
}

// counterDelta is a cumulative counter's change between two observations:
// unavailable when either end lacks it, and when it went backwards (a reset
// or a different counter), which is not a negative cost. A zero change is a
// real zero.
func counterDelta[T int64 | float64](start, end T, startKnown, endKnown bool) *T {
	if !startKnown || !endKnown || end < start {
		return nil
	}
	return new(end - start)
}

func cpuDelta(start, end processResources) *float64 {
	from, fromKnown := start.totalCPU()
	to, toKnown := end.totalCPU()
	return counterDelta(from, to, fromKnown, toKnown)
}

// syncMeterSources are where a meter reads: processResourceUsage,
// processRSS and the clock, scripted by tests.
type syncMeterSources struct {
	observe func() processResources
	rss     func() (int64, bool)
	now     func() time.Time
}

type syncMeter struct {
	sources     syncMeterSources
	started     time.Time
	initial     processResources
	allocated   uint64
	measurement syncMeasurement
	mu          sync.Mutex
	// done and stopped run the once-a-second memory sampler; a meter
	// without them is sampled by its caller.
	done    chan struct{}
	stopped chan struct{}
}

type phaseStart struct {
	name    string
	started time.Time
	start   processResources
}

// newSyncMeter takes the run's baselines. A baseline that could not be read
// stays unavailable: a later reading never becomes a delta from zero.
func newSyncMeter(sources syncMeterSources) *syncMeter {
	meter := &syncMeter{sources: sources, started: sources.now(), initial: sources.observe()}
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	meter.allocated = heap.TotalAlloc
	meter.measurement.HeapBaseline, meter.measurement.HeapPeak = heap.HeapAlloc, heap.HeapAlloc
	if rss, ok := sources.rss(); ok {
		meter.measurement.RSSBaseline, meter.measurement.RSSPeak = new(rss), new(rss)
	}
	return meter
}

func beginSyncMeasurement() *syncMeter {
	meter := newSyncMeter(syncMeterSources{observe: processResourceUsage, rss: processRSS, now: time.Now})
	meter.done, meter.stopped = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(meter.stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-meter.done:
				return
			case <-ticker.C:
				meter.sampleMemory()
			}
		}
	}()
	return meter
}

// sampleMemory raises the observed peaks. An RSS reading that fails leaves
// a known peak as it was; a run with no RSS reading has no peak.
func (meter *syncMeter) sampleMemory() {
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	rss, ok := meter.sources.rss()
	meter.mu.Lock()
	defer meter.mu.Unlock()
	meter.measurement.HeapPeak = max(meter.measurement.HeapPeak, heap.HeapAlloc)
	if ok && (meter.measurement.RSSPeak == nil || rss > *meter.measurement.RSSPeak) {
		meter.measurement.RSSPeak = new(rss)
	}
}

func (meter *syncMeter) beginPhase(name string) phaseStart {
	return phaseStart{name: name, started: meter.sources.now(), start: meter.sources.observe()}
}

// endPhase records a phase's elapsed time and its CPU, which follows the
// same rules as the run's: unavailable unless both ends were read and the
// counter did not go backwards.
func (meter *syncMeter) endPhase(phase phaseStart) {
	meter.measurement.Phases = append(meter.measurement.Phases, syncPhase{Name: phase.name, Seconds: meter.sources.now().Sub(phase.started).Seconds(), CPUSeconds: cpuDelta(phase.start, meter.sources.observe())})
}

func (meter *syncMeter) endPhaseWork(phase phaseStart, work []workStageMeasurement) {
	meter.endPhase(phase)
	meter.measurement.Phases[len(meter.measurement.Phases)-1].Work = work
}

func (meter *syncMeter) finish() syncMeasurement {
	if meter.stopped != nil {
		close(meter.done)
		<-meter.stopped
	}
	meter.sampleMemory()
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	resources := meter.sources.observe()
	measurement := &meter.measurement
	measurement.Seconds = meter.sources.now().Sub(meter.started).Seconds()
	measurement.CPUSeconds = cpuDelta(meter.initial, resources)
	if measurement.CPUSeconds != nil && measurement.Seconds > 0 {
		measurement.CPUPercent = new(100 * *measurement.CPUSeconds / measurement.Seconds)
	}
	measurement.Allocated = heap.TotalAlloc - meter.allocated
	blocks := meter.initial.known.has(counterBlocks) && resources.known.has(counterBlocks)
	measurement.ReadBlocks = counterDelta(meter.initial.read, resources.read, blocks, blocks)
	measurement.WrittenBlocks = counterDelta(meter.initial.written, resources.written, blocks, blocks)
	return meter.measurement
}

// syncStatistics is each workload class's distribution of elapsed time, CPU
// and observed RSS peak. A sample whose CPU or RSS was not measured is left
// out of that metric (and of the CPU-duty estimate) while still counting for
// elapsed time; a metric no sample measured is null. Real zeros count.
// Samples arrive newest first, so last is the newest measured value.
func syncStatistics(samples []syncMeasurement, interval int) map[string]any {
	groups := map[string][]syncMeasurement{}
	for _, sample := range samples {
		groups[sample.Class] = append(groups[sample.Class], sample)
	}
	result := map[string]any{}
	for class, items := range groups {
		elapsed, cpu, ram := []float64{}, []float64{}, []float64{}
		for _, item := range items {
			elapsed = append(elapsed, item.Seconds)
			if item.CPUSeconds != nil {
				cpu = append(cpu, *item.CPUSeconds)
			}
			if item.RSSPeak != nil {
				ram = append(ram, float64(*item.RSSPeak))
			}
		}
		stats := func(values []float64) map[string]float64 {
			if len(values) == 0 {
				return nil
			}
			last := values[0]
			slices.Sort(values)
			return map[string]float64{"last": last, "median": values[(len(values)-1)/2], "p95": values[min(len(values)-1, (95*len(values)+99)/100-1)], "samples": float64(len(values))}
		}
		cpuStats := stats(cpu)
		var duty *float64
		if cpuStats != nil {
			duty = new(100 * cpuStats["median"] / float64(max(interval, 1)))
		}
		result[class] = map[string]any{"samples": len(items), "elapsed_seconds": stats(elapsed), "cpu_seconds": cpuStats, "rss_observed_peak_bytes": stats(ram), "estimated_cpu_duty_percent_one_core": duty, "estimate": true}
	}
	return result
}
