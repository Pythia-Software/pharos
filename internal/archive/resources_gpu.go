package archive

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// GPU time is scoped to the same process tree as CPU and RAM. Driver client
// IDs keep newly created/reused processes from inheriting another client's
// lifetime GPU time. These best-effort counters are not device saturation.
type gpuReading struct {
	UtilizationPercent *float64 `json:"utilization_percent"`
	Partial            bool     `json:"partial"`
	SampledAt          string   `json:"sampled_at"`
	Basis              string   `json:"basis"`
}

type gpuClient struct {
	id    uint64
	pid   int
	ns    uint64
	known bool
}

type gpuSnapshot struct {
	clients   []gpuClient
	supported bool
}

type gpuClientRate struct {
	gpuClient
	percent *float64
}

const gpuSampleInterval = 5 * time.Second
const gpuResourceBasis = "IOAccelerator.AppUsage.accumulatedGPUTime"

// Share one registry query among all status readers and library summaries in
// this process. With no requests nothing runs. Failures clear both the rates
// and their baselines, and are cached too. Never fall back to device-wide load.
var processGPU = &gpuSampler{read: platformGPUClients, now: time.Now}

type gpuSampler struct {
	mu        sync.Mutex
	read      func() (gpuSnapshot, error) // nil on platforms without a reader
	now       func() time.Time
	at        time.Time
	attempted bool
	supported bool
	clients   map[uint64]gpuClientRate
}

func (s *gpuSampler) sample(processes []resourceReading) *gpuReading {
	if s == nil || s.read == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.attempted || now.Sub(s.at) >= gpuSampleInterval {
		snapshot, err := s.read()
		elapsed := now.Sub(s.at)
		continuous := s.supported && elapsed > 0 && elapsed <= resourceStaleGap
		current := make(map[uint64]gpuClientRate, len(snapshot.clients))
		if err == nil && snapshot.supported {
			for _, client := range snapshot.clients {
				rate := gpuClientRate{gpuClient: client}
				if previous, ok := s.clients[client.id]; ok && continuous && client.known && previous.known && client.pid == previous.pid && client.ns >= previous.ns {
					rate.percent = new(100 * float64(client.ns-previous.ns) / float64(elapsed))
				}
				current[client.id] = rate
			}
		}
		s.at, s.attempted, s.supported, s.clients = now, true, err == nil && snapshot.supported, current
	}
	reading := &gpuReading{SampledAt: formatTime(s.at), Basis: gpuResourceBasis}
	if !s.supported {
		return reading
	}
	pids := make(map[int]bool, len(processes))
	for _, process := range processes {
		pids[process.PID] = true
	}
	percent, matched, measured := 0.0, 0, 0
	for _, client := range s.clients {
		if !pids[client.pid] {
			continue
		}
		matched++
		if client.percent == nil {
			reading.Partial = true
			continue
		}
		percent += *client.percent
		measured++
	}
	// No GPU clients in the selected tree means idle. Existing clients need
	// two readings; keep any measured portion visible while new clients warm up.
	if matched == 0 || measured > 0 {
		reading.UtilizationPercent = roundedPercent(new(min(percent, 100)))
	}
	return reading
}

func withProcessGPU(summary *resourceSummary) *resourceSummary {
	if summary != nil {
		summary.GPU = processGPU.sample(summary.Processes)
	}
	return summary
}

// gpuClientsFromPlist visits only registry entries, matching each GPU client's
// owner PID to its AppUsage counters. Client names (often truncated) and the
// accelerator's device-wide PerformanceStatistics never enter attribution.
func gpuClientsFromPlist(data []byte) (gpuSnapshot, error) {
	value, err := parsePlist(data)
	if err != nil {
		return gpuSnapshot{}, err
	}
	result := gpuSnapshot{}
	var walk func([]any)
	walk = func(entries []any) {
		for _, entry := range entries {
			properties, _ := entry.(map[string]any)
			creator, hasOwner := strings.CutPrefix(plistString(properties, "IOUserClientCreator"), "pid ")
			pidText, _, _ := strings.Cut(creator, ",")
			pid, _ := strconv.Atoi(pidText)
			id, _ := properties["IORegistryEntryID"].(int64)
			if hasOwner && pid > 0 && id > 0 {
				client := gpuClient{id: uint64(id), pid: pid}
				usage, _ := properties["AppUsage"].([]any)
				client.known = len(usage) > 0
				for _, item := range usage {
					stats, _ := item.(map[string]any)
					ns, ok := stats["accumulatedGPUTime"].(int64)
					if !ok || ns < 0 || client.ns > ^uint64(0)-uint64(ns) {
						client.known = false
						break
					}
					client.ns += uint64(ns)
				}
				result.supported = result.supported || client.known
				result.clients = append(result.clients, client)
			}
			children, _ := properties["IORegistryEntryChildren"].([]any)
			walk(children)
		}
	}
	entries, _ := value.([]any)
	walk(entries)
	return result, nil
}
