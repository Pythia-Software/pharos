package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// configureResourceHistory runs before the broker handles requests. The
// verified parent route comes from the same native-app lifetime contract as
// the broker watchdog, without consuming/clearing its environment variable.
func (b *brokerOperations) configureResourceHistory() {
	pid, _ := strconv.Atoi(os.Getenv(parentEnv))
	if pid > 1 {
		for _, node := range resourceProcessTable() {
			if node.PID == os.Getpid() && node.Parent == pid {
				if usage, err := processUsageOf(pid); err == nil && usage.Started != 0 {
					b.nativePID, b.nativeStarted = pid, usage.Started
				}
				break
			}
		}
	}
	b.history = newResourceHistory(b.resources, processGPU, b.collectResourceHistory)
	b.reg.telemetry = true
	b.reg.resourceLedger = newTaskResourceLedger(20, 200)
	b.reg.resourceHistory = b.history
}

// Lease files live on each library's drive. Opening them every history
// tick kept every drive awake, so a probe is reused for resourceLeaseActive
// while work runs and resourceLeaseIdle otherwise; a Terminal command's
// lease is long-lived, and its process is sampled every tick once known.
var (
	resourceLeaseActive = 30 * time.Second
	resourceLeaseIdle   = 30 * time.Minute
)

// resourceLeaseCache is the history collector's last lease probes, by
// directory list. Only the collector's goroutine uses it, apart from stale.
type resourceLeaseCache struct {
	probes  map[string][]leaseProbe
	at      map[string]time.Time
	active  bool              // the previous frame had running work
	started map[string]uint64 // OS process identity observed with each lease
	read    func(int) (processUsage, error)
	// omitted describes unverified local leases from the most recent probe.
	// It is diagnostic coverage, never a lifecycle verdict.
	omitted []string
	// stale makes the next probe read the drive again: status's own fresh
	// probe found a live lease the cached one lacks. Any goroutine sets it.
	stale atomic.Bool
}

func (c *resourceLeaseCache) probe(dirs []string, at time.Time) []leaseProbe {
	c.omitted = nil
	key := strings.Join(dirs, "\x00")
	every := resourceLeaseIdle
	if c.active {
		every = resourceLeaseActive
	}
	if c.stale.Swap(false) {
		clear(c.at) // the broker has one cache key per library, not just one
	}
	probed, known := c.at[key]
	fresh := !known || at.Sub(probed) >= every || at.Before(probed)
	if fresh {
		if c.probes == nil {
			c.probes, c.at = map[string][]leaseProbe{}, map[string]time.Time{}
		}
		for _, old := range c.probes[key] {
			delete(c.started, old.Path)
		}
		c.probes[key], c.at[key] = probeLeases(dirs), at
	}
	if c.started == nil {
		c.started = map[string]uint64{}
	}
	read := c.read
	if read == nil {
		read = processUsageOf
	}
	probes := slices.Clone(c.probes[key])
	for i := range probes {
		probe := &probes[i]
		if !probe.Live || probe.Info == nil || probe.Info.PID == os.Getpid() || probe.Info.HostID != currentHost().ID {
			continue
		}
		usage, err := read(probe.Info.PID)
		if fresh && err == nil {
			c.started[probe.Path] = usage.Started
		}
		// A cached flock says nothing about a replacement process with the
		// same PID. Once its observed OS identity ends, forget this lease
		// until the next real probe instead of attributing unrelated load.
		if err != nil || usage.Started == 0 || c.started[probe.Path] == 0 {
			probe.Live = false
			if presence, _ := observeProcess(probe.Info.PID); presence != processAbsent {
				c.omitted = append(c.omitted, fmt.Sprintf("Lease process (PID %d): usage or process identity could not be verified", probe.Info.PID))
			}
		} else if c.started[probe.Path] != usage.Started {
			probe.Live = false
		}
	}
	return probes
}

func (b *brokerOperations) collectResourceHistory(at time.Time) resourceHistoryFrame {
	b.mu.Lock()
	libraries := slices.Clone(b.libraries)
	b.mu.Unlock()
	groups := []resourceGroup{}
	tasks := resourceHistoryTasks(b.reg.running())
	unknown := []string{}
	appLeases := b.leases.probe([]string{appLeaseDir()}, at)
	omitted := append(slices.Clone(b.leases.omitted), "Native UI helper ownership is not independently verified")
	for _, library := range libraries {
		processes := []resourceProcess{}
		if library.Child != nil {
			processes = append(processes, resourceProcess{PID: library.Child.PID, Role: "service", Library: library.ID, LibraryName: library.Name})
		}
		processes = append(processes, leaseProcesses(b.leases.probe(library.LeaseDirs, at), library.ID, library.Name)...)
		leaseOmissions := slices.Clone(b.leases.omitted)
		for _, lease := range appLeases {
			if lease.Info != nil && slices.ContainsFunc(lease.Info.Libraries, func(path string) bool { return sameConfig(path, library) }) {
				processes = append(processes, leaseProcesses([]leaseProbe{lease}, library.ID, library.Name)...)
			}
		}
		groups = append(groups, resourceGroup{Key: library.ID, Label: library.Name, Processes: processes, Omitted: leaseOmissions})
	}
	// Memory-only task metadata from each service: never /status/freshness
	// or a catalog query on the sampler path. The services are asked at once,
	// after the lease probes (drive I/O), under one deadline, so a slow
	// service delays the process readings by at most resourceTaskDeadline
	// in all and cannot use up another library's time. A library not
	// answered by then is unknown this tick: its tasks carry forward, for at
	// most resourceTaskCarry (observeTasks).
	answers := make([][]resourceHistoryTask, len(libraries))
	failed := make([]bool, len(libraries))
	ctx, cancel := context.WithTimeout(context.Background(), resourceTaskDeadline)
	var asking sync.WaitGroup
	for i, library := range libraries {
		if library.Child != nil {
			endpoint := *library.Child
			asking.Go(func() {
				var err error
				answers[i], err = b.resourceTasks(ctx, endpoint)
				failed[i] = err != nil
			})
		}
	}
	asking.Wait()
	cancel()
	for i, library := range libraries {
		if library.Child == nil {
			continue
		}
		if failed[i] {
			unknown = append(unknown, library.ID)
			groups[i].Omitted = append(groups[i].Omitted, "Task markers could not be read for this library")
			continue
		}
		for _, task := range answers[i] {
			task.Libraries, task.source = []string{library.ID}, library.ID
			tasks = append(tasks, task)
		}
	}
	extra := []resourceProcess{{PID: os.Getpid(), Role: "broker"}}
	extra = append(extra, leaseProcesses(appLeases, "", "")...)
	if b.nativePID > 0 {
		if usage, err := processUsageOf(b.nativePID); err == nil && usage.Started == b.nativeStarted {
			extra = append(extra, resourceProcess{PID: b.nativePID, Role: "native-app", OwnOnly: true})
		} else {
			omitted = append(omitted, resourceOmitNativeApp)
		}
	} else {
		omitted = append(omitted, resourceOmitNativeApp)
	}
	active := slices.ContainsFunc(tasks, func(task resourceHistoryTask) bool { return task.State == opRunning })
	b.leases.active = active
	return resourceHistoryFrame{Active: active, Tasks: tasks, UnknownLibraries: unknown, Scopes: collectResourceGroups(b.resources, processGPU, resourceScopeApp, groups, extra, omitted)}
}

// resourceTaskDeadline bounds one tick's task-metadata requests, all
// libraries together.
var resourceTaskDeadline = 100 * time.Millisecond

var errResourceTaskStatus = errors.New("service task metadata unavailable")

func (b *brokerOperations) resourceTasks(ctx context.Context, endpoint applicationEndpoint) ([]resourceHistoryTask, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.URL+"/api/resources/active", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := b.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errResourceTaskStatus
	}
	var document struct {
		Tasks []resourceHistoryTask `json:"tasks"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&document); err != nil {
		return nil, err
	}
	return document.Tasks, nil
}
