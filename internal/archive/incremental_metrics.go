package archive

import (
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const syncMeasurementLimits = "CPU is process-wide plus completed child processes and includes concurrent Pharos work. CPU percent is relative to one core. RSS is sampled whole-process resident memory, not sync allocations; peaks between samples can be missed. Go allocation counters are process-wide. OS block counts are not byte counts. Telemetry overhead is included."

type syncPhase struct {
	Name       string  `json:"name"`
	Seconds    float64 `json:"seconds"`
	CPUSeconds float64 `json:"cpu_seconds"`
}

type syncMeasurement struct {
	RunID         string      `json:"run_id"`
	State         string      `json:"state"`
	Class         string      `json:"class"`
	ColdStart     bool        `json:"cold_start"`
	Seconds       float64     `json:"seconds"`
	CPUSeconds    float64     `json:"cpu_seconds"`
	CPUPercent    float64     `json:"cpu_percent_one_core"`
	RSSBaseline   int64       `json:"rss_baseline_bytes"`
	RSSPeak       int64       `json:"rss_observed_peak_bytes"`
	HeapBaseline  uint64      `json:"heap_baseline_bytes"`
	HeapPeak      uint64      `json:"heap_observed_peak_bytes"`
	Allocated     uint64      `json:"allocated_bytes"`
	ReadBlocks    int64       `json:"read_blocks"`
	WrittenBlocks int64       `json:"written_blocks"`
	Workspaces    int         `json:"changed_groups"`
	Conversations int         `json:"conversations"`
	Messages      int         `json:"messages_in_changed_groups"`
	Audit         auditResult `json:"audit"`
	Phases        []syncPhase `json:"phases"`
}

type syncMeter struct {
	started     time.Time
	initial     processResources
	allocated   uint64
	measurement syncMeasurement
	mu          sync.Mutex
	done        chan struct{}
	stopped     chan struct{}
}

type processResources struct {
	cpu           float64
	read, written int64
}

type phaseStart struct {
	name    string
	started time.Time
	cpu     float64
}

func processResourceUsage() processResources {
	result := processResources{}
	for _, who := range []int{syscall.RUSAGE_SELF, syscall.RUSAGE_CHILDREN} {
		var usage syscall.Rusage
		if syscall.Getrusage(who, &usage) == nil {
			result.cpu += float64(usage.Utime.Nano()+usage.Stime.Nano()) / 1e9
			result.read += usage.Inblock
			result.written += usage.Oublock
		}
	}
	return result
}

func processRSS() int64 {
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kilobytes, _ := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	return kilobytes * 1024
}

func beginSyncMeasurement() *syncMeter {
	meter := &syncMeter{started: time.Now(), initial: processResourceUsage(), done: make(chan struct{}), stopped: make(chan struct{})}
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	meter.allocated = heap.TotalAlloc
	meter.measurement.HeapBaseline, meter.measurement.HeapPeak = heap.HeapAlloc, heap.HeapAlloc
	rss := processRSS()
	meter.measurement.RSSBaseline, meter.measurement.RSSPeak = rss, rss
	go func() {
		defer close(meter.stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-meter.done:
				return
			case <-ticker.C:
				var heap runtime.MemStats
				runtime.ReadMemStats(&heap)
				rss := processRSS()
				meter.mu.Lock()
				meter.measurement.RSSPeak = max(meter.measurement.RSSPeak, rss)
				meter.measurement.HeapPeak = max(meter.measurement.HeapPeak, heap.HeapAlloc)
				meter.mu.Unlock()
			}
		}
	}()
	return meter
}

func (meter *syncMeter) beginPhase(name string) phaseStart {
	return phaseStart{name: name, started: time.Now(), cpu: processResourceUsage().cpu}
}

func (meter *syncMeter) endPhase(phase phaseStart) {
	meter.measurement.Phases = append(meter.measurement.Phases, syncPhase{Name: phase.name, Seconds: time.Since(phase.started).Seconds(), CPUSeconds: processResourceUsage().cpu - phase.cpu})
}

func (meter *syncMeter) finish() syncMeasurement {
	close(meter.done)
	<-meter.stopped
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	meter.measurement.HeapPeak = max(meter.measurement.HeapPeak, heap.HeapAlloc)
	meter.measurement.RSSPeak = max(meter.measurement.RSSPeak, processRSS())
	resources := processResourceUsage()
	meter.measurement.Seconds = time.Since(meter.started).Seconds()
	meter.measurement.CPUSeconds = resources.cpu - meter.initial.cpu
	meter.measurement.CPUPercent = 100 * meter.measurement.CPUSeconds / meter.measurement.Seconds
	meter.measurement.Allocated = heap.TotalAlloc - meter.allocated
	meter.measurement.ReadBlocks = resources.read - meter.initial.read
	meter.measurement.WrittenBlocks = resources.written - meter.initial.written
	return meter.measurement
}

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
			cpu = append(cpu, item.CPUSeconds)
			ram = append(ram, float64(item.RSSPeak))
		}
		stats := func(values []float64) map[string]float64 {
			last := values[0]
			slices.Sort(values)
			return map[string]float64{"last": last, "median": values[(len(values)-1)/2], "p95": values[min(len(values)-1, (95*len(values)+99)/100-1)]}
		}
		cpuStats := stats(cpu)
		result[class] = map[string]any{"samples": len(items), "elapsed_seconds": stats(elapsed), "cpu_seconds": cpuStats, "rss_observed_peak_bytes": stats(ram), "estimated_cpu_duty_percent_one_core": 100 * cpuStats["median"] / float64(max(interval, 1)), "estimate": true}
	}
	return result
}
