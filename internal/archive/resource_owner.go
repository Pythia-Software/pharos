package archive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
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

func (b *brokerOperations) collectResourceHistory(at time.Time) resourceHistoryFrame {
	b.mu.Lock()
	libraries := slices.Clone(b.libraries)
	b.mu.Unlock()
	groups := []resourceGroup{}
	tasks := resourceHistoryTasks(b.reg.running())
	unknown := []string{}
	appLeases := probeLeases([]string{appLeaseDir()})
	omitted := []string{"Native UI helper ownership is not independently verified"}
	// Memory-only task metadata from each service: never /status/freshness
	// or a catalog query on the sampler path. One deadline covers every
	// library, asked in turn, so a slow service delays the process readings
	// by at most resourceTaskDeadline in all, not per library. A library not
	// answered by then is unknown this tick: its tasks carry forward.
	ctx, cancel := context.WithTimeout(context.Background(), resourceTaskDeadline)
	defer cancel()
	for _, library := range libraries {
		processes := []resourceProcess{}
		if library.Child != nil {
			processes = append(processes, resourceProcess{PID: library.Child.PID, Role: "service", Library: library.ID, LibraryName: library.Name})
		}
		processes = append(processes, leaseProcesses(probeLeases(library.LeaseDirs), library.ID, library.Name)...)
		for _, lease := range appLeases {
			if lease.Info != nil && slices.ContainsFunc(lease.Info.Libraries, func(path string) bool { return sameConfig(path, library) }) {
				processes = append(processes, leaseProcesses([]leaseProbe{lease}, library.ID, library.Name)...)
			}
		}
		group := resourceGroup{Key: library.ID, Label: library.Name, Processes: processes}
		if library.Child != nil {
			active, err := b.resourceTasks(ctx, *library.Child)
			if err != nil {
				unknown = append(unknown, library.ID)
				group.Omitted = append(group.Omitted, "Task markers could not be read for this library")
			} else {
				for i := range active {
					active[i].Libraries, active[i].source = []string{library.ID}, library.ID
				}
				tasks = append(tasks, active...)
			}
		}
		groups = append(groups, group)
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
