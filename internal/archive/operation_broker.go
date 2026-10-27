package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The broker's registry (docs/background-work.md §6.6). Broker operations
// (transfer, recovery, delivery, library creation, service starts, volume
// releases) are app-level, not library data: they live in the broker's
// memory and in operations.json in its application support directory,
// written on start and end and at most every 10 s between. They are never
// written into a library's catalog.
//
// GET /api/app/operations serves them, with each connected library's own
// snapshot from its service, without taking the broker's main mutex: create,
// connect and child starts hold that for seconds to minutes, and the menu
// must still be able to tell that something is running.

// brokerOperationsPersistEvery spaces writes between starts and ends.
var brokerOperationsPersistEvery = 10 * time.Second

// brokerKeepEnded is how many ended operations the state file keeps.
const brokerKeepEnded = 50

// brokerOperationRecord is a broker operation as the state file and the
// snapshot store it.
type brokerOperationRecord struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Variant   string   `json:"variant,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Libraries []string `json:"libraries,omitempty"`
	Trigger   string   `json:"trigger"`
	State     string   `json:"state"`
	Reason    string   `json:"reason,omitempty"`
	Error     string   `json:"error,omitempty"`
	Step      string   `json:"step,omitempty"`
	StartedAt string   `json:"started_at"`
	EndedAt   string   `json:"ended_at,omitempty"`
}

// brokerLibrary is what the snapshot needs about one registered library,
// published by the broker whenever it changes, under brokerOperations.mu.
type brokerLibrary struct {
	ID, Name string
	Selected bool
	// Child is the library's running service, if any.
	Child *applicationEndpoint
	// ChildStarted is when that service became ready.
	ChildStarted time.Time
	// Config is its configuration's path, and LeaseDirs where Terminal
	// commands writing it take their leases.
	Config    string
	LeaseDirs []string
	// Canonical is Config resolved (canonical), once, when it is published.
	Canonical string
}

// brokerFreshTimeout bounds the eject check's request to the selected
// library's service (a full lock check), inside the 10 s the broker gives
// the whole check.
var brokerFreshTimeout = 8 * time.Second

type brokerOperations struct {
	reg  *opRegistry
	path string

	mu        sync.Mutex
	previous  []brokerOperationRecord // from earlier broker lifetimes
	libraries []brokerLibrary
	// published counts library-list changes, for the status hub.
	published uint64
	lastWrite time.Time
	client    *http.Client
	// status shares the latest /api/app/status document among its readers.
	status statusHub
	// unavailable counts, per library, the documents in a row that found it
	// unreachable or not open (rule 8).
	unavailable map[string]int
	// resources samples the broker, each library's service and the
	// Terminal commands writing them, for /api/app/status (§10).
	resources *resourceSampler
}

func brokerOperationsPath(installation string) string {
	return filepath.Join(filepath.Dir(installationPath(installation)), "operations.json")
}

// newBrokerOperations loads the state file: operations an earlier broker
// left running become interrupted.
func newBrokerOperations(path string) *brokerOperations {
	b := &brokerOperations{reg: newOpRegistry("broker"), path: path, client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}},
		resources: newResourceSampler()}
	b.reg.wake = make(chan struct{}, 1)
	// Ended operations stay listed (and in the state file) until newer ones
	// push them out, not for the service's ten minutes.
	b.reg.keepEnded = 30 * 24 * time.Hour
	var stored struct {
		Operations []brokerOperationRecord `json:"operations"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &stored) == nil {
		for _, record := range stored.Operations {
			if !opTerminal(record.State) {
				record.State, record.Reason = opInterrupted, "owner-exited"
				record.EndedAt = defaultString(record.EndedAt, now())
			}
			b.previous = append(b.previous, record)
		}
	}
	return b
}

// begin registers a broker operation on the libraries it touches.
func (b *brokerOperations) begin(spec OperationSpec, libraries ...string) *Op {
	if b == nil {
		return nil
	}
	spec.Libraries = slices.DeleteFunc(slices.Clone(libraries), func(id string) bool { return id == "" })
	if spec.Trigger == "" {
		spec.Trigger = triggerUser
	}
	_, op := b.reg.begin(context.Background(), spec)
	return op
}

// launch runs work for op in the background and finishes op with its
// result: the broker's equivalent of Server.launch.
func (b *brokerOperations) launch(op *Op, work func(op *Op) error) {
	go runOperation(op, func(context.Context, *Op) error { return work(op) })
}

// records lists the current registry's operations and the earlier ones,
// newest first, ended ones capped at brokerKeepEnded.
func (b *brokerOperations) records() []brokerOperationRecord {
	records := []brokerOperationRecord{}
	for _, view := range b.reg.views() {
		record := brokerOperationRecord{ID: string(view.ID), Kind: view.Spec.Kind, Variant: view.Spec.Variant, Trigger: view.Spec.Trigger, State: view.State,
			Reason: view.Reason, Error: view.Error, Step: view.Step.Name, StartedAt: formatTime(view.Started)}
		record.Libraries = view.Spec.Libraries
		record.Subject = view.Spec.Subject
		if !view.Ended.IsZero() {
			record.EndedAt = formatTime(view.Ended)
		}
		records = append(records, record)
	}
	b.mu.Lock()
	records = append(records, b.previous...)
	b.mu.Unlock()
	sort.SliceStable(records, func(i, j int) bool { return records[i].StartedAt > records[j].StartedAt })
	kept, ended := []brokerOperationRecord{}, 0
	for _, record := range records {
		if opTerminal(record.State) {
			if ended >= brokerKeepEnded {
				continue
			}
			ended++
		}
		kept = append(kept, record)
	}
	return kept
}

// persist writes the state file.
func (b *brokerOperations) persist() error {
	records := b.records()
	b.mu.Lock()
	b.lastWrite = time.Now()
	b.mu.Unlock()
	return durableJSON(b.path, map[string]any{"version": 1, "operations": records})
}

// takeBoundary reports and clears a start or end since the last call.
func (r *opRegistry) takeBoundary() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	boundary := r.boundary
	r.boundary = false
	return boundary
}

// run persists on every start and end, and at most every
// brokerOperationsPersistEvery for other changes, until ctx ends.
func (b *brokerOperations) run(ctx context.Context) {
	ticker := time.NewTicker(brokerOperationsPersistEvery)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			_ = b.persist()
			return
		case <-b.reg.wake:
			dirty = true
			if !b.reg.takeBoundary() {
				b.mu.Lock()
				recent := time.Since(b.lastWrite) < brokerOperationsPersistEvery
				b.mu.Unlock()
				if recent {
					continue
				}
			}
		case <-ticker.C:
			cleanDeadLeases(appLeaseDir())
			if !dirty {
				continue
			}
		}
		if err := b.persist(); err != nil {
			fmt.Fprintf(os.Stderr, "Broker operations: %v\n", err)
		}
		dirty = false
	}
}

// publish replaces the library list the snapshot reads.
func (b *brokerOperations) publish(libraries []brokerLibrary) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.libraries = libraries
	b.published++
	b.mu.Unlock()
}

// publishLocked publishes m's libraries and children; m.mu is held.
func (m *applicationManager) publishLocked() {
	if m.operations == nil {
		return
	}
	libraries := make([]brokerLibrary, 0, len(m.registry.Libraries))
	for _, l := range m.registry.Libraries {
		library := brokerLibrary{ID: l.ID, Name: l.Name, Selected: l.ID == m.registry.Selected, Config: l.resolve()}
		if resolved, ok := m.canonicalCache[library.Config]; ok {
			library.Canonical = resolved
		} else {
			library.Canonical = canonical(library.Config)
			if m.canonicalCache != nil {
				m.canonicalCache[library.Config] = library.Canonical
			}
		}
		if dirs, ok := m.leaseDirCache[library.Config]; ok {
			library.LeaseDirs = dirs
		} else if config, err := LoadConfig(library.Config); err == nil {
			library.LeaseDirs = leaseDirs(config)
			m.leaseDirCache[library.Config] = library.LeaseDirs
		}
		if c := m.children[l.ID]; c != nil {
			select {
			case <-c.done:
			default:
				endpoint := c.endpoint
				library.Child, library.ChildStarted = &endpoint, c.started
			}
		}
		libraries = append(libraries, library)
	}
	m.operations.publish(libraries)
}

// operationTouches reports whether a broker operation names library.
func operationTouches(record brokerOperationRecord, library string) bool {
	return slices.Contains(record.Libraries, library)
}

// brokerOperationView turns a record into the snapshot's operation shape.
func brokerOperationEntry(record brokerOperationRecord) map[string]any {
	kind, ok := lookupOperationKind(record.Kind)
	if !ok {
		kind = operationKindIndex[unknownOperationKind]
	}
	view := opView{ID: OpID(record.ID), Kind: kind, State: record.State, Reason: record.Reason, Error: record.Error,
		Spec: OperationSpec{Kind: record.Kind, Variant: record.Variant, Subject: record.Subject, Trigger: record.Trigger, Holds: parseHolds(kind.Holds), Priority: kind.Priority, Libraries: record.Libraries}}
	view.Started, _ = parseTime(record.StartedAt)
	view.Ended, _ = parseTime(record.EndedAt)
	if record.Step != "" {
		view.Step = opStep{Index: 1, Name: record.Step, Total: -1}
	}
	view.Process = "broker"
	entry := operationEntry(view, nil, time.Now())
	entry["libraries"] = record.Libraries
	return entry
}

// brokerLibraryVerdict combines a library's own verdict with the broker
// operations that touch it (§4.4), which make it busy, and with the
// service being unreachable, which makes it unsafe.
func brokerLibraryVerdict(own *ejectVerdict, records []brokerOperationRecord, library string, unreachable string, leases []leaseProbe) ejectVerdict {
	in := verdictInput{unreachable: unreachable, leases: leases}
	extra := computeEjectVerdict(in)
	// Every running broker operation that touches the library (a transfer,
	// a recovery, a delivery, its service starting) makes it at least busy,
	// whatever its holds declare: the broker runs it and Pharos cannot stop
	// it. A release is the eject itself, not a holder.
	for _, record := range records {
		if opTerminal(record.State) || !operationTouches(record, library) || record.Kind == "library.release" {
			continue
		}
		kind, ok := lookupOperationKind(record.Kind)
		if !ok {
			kind = operationKindIndex[unknownOperationKind]
		}
		verdict := verdictBusy
		if kind.Kind == unknownOperationKind {
			verdict = verdictUnsafe
		}
		extra.Blockers = append(extra.Blockers, ejectBlocker{Operation: record.ID, Kind: record.Kind, Label: kind.label(record.Variant), OnEject: kind.OnEject,
			Latency: kind.Latency, Verdict: verdict, Process: "broker"})
		if verdictRank[verdict] > verdictRank[extra.Verdict] {
			extra.Verdict, extra.ETASeconds = verdict, nil
		}
	}
	sort.SliceStable(extra.Blockers, func(i, j int) bool {
		return verdictRank[extra.Blockers[i].Verdict] > verdictRank[extra.Blockers[j].Verdict]
	})
	extra.Summary, extra.Headline = verdictSummary(extra), verdictHeadline(extra)
	if own == nil {
		return extra
	}
	if len(extra.Blockers) == 0 {
		return *own
	}
	merged := ejectVerdict{Verdict: own.Verdict, Checked: own.Checked, Blockers: append(slices.Clone(extra.Blockers), own.Blockers...)}
	for _, blocker := range merged.Blockers {
		if verdictRank[blocker.Verdict] > verdictRank[merged.Verdict] {
			merged.Verdict = blocker.Verdict
		}
	}
	if merged.Verdict == own.Verdict {
		merged.ETASeconds = own.ETASeconds
	}
	sort.SliceStable(merged.Blockers, func(i, j int) bool {
		return verdictRank[merged.Blockers[i].Verdict] > verdictRank[merged.Blockers[j].Verdict]
	})
	merged.Summary, merged.Headline = verdictSummary(merged), verdictHeadline(merged)
	return merged
}

// start runs the persistence loop until ctx ends; the channel closes once
// it has written its last state.
func (b *brokerOperations) start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.run(ctx)
	}()
	return done
}

// fetchServiceOperations asks a library's service for its snapshot at path
// (/api/operations, or /api/status for the status document).
func (b *brokerOperations) fetchServiceOperations(ctx context.Context, client *http.Client, endpoint applicationEndpoint, path string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.URL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the library's service answered %s", response.Status)
	}
	var document map[string]any
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return nil, err
	}
	return document, nil
}

// document builds GET /api/app/operations (§7.2). It reads only what the
// broker published under its own lock and asks each connected child.
//
// Without withLibraries (?libraries=0, the native menu's poll) it lists only
// the broker's own operations and app-level leases: no child is asked, no
// library's leases are read.
func (b *brokerOperations) document(ctx context.Context, withLibraries bool) map[string]any {
	return b.documentFrom(ctx, withLibraries, "/api/operations", false)
}

// documentFrom is document, with each child's snapshot read from path.
// fresh is the eject check: only the selected library is asked, with the
// longer timeout a full lock check may need.
func (b *brokerOperations) documentFrom(ctx context.Context, withLibraries bool, path string, fresh bool) map[string]any {
	b.mu.Lock()
	libraries := slices.Clone(b.libraries)
	b.mu.Unlock()
	// Every registered library, before the filters below, for naming the
	// libraries a lease touches.
	registered := slices.Clone(libraries)
	if !withLibraries {
		libraries = nil
	}
	client := b.client
	if fresh {
		libraries = slices.DeleteFunc(libraries, func(library brokerLibrary) bool { return !library.Selected })
		client = &http.Client{Timeout: brokerFreshTimeout, Transport: b.client.Transport}
	}
	records := b.records()
	snapshots := make([]map[string]any, len(libraries))
	failures := make([]error, len(libraries))
	var wait sync.WaitGroup
	for index, library := range libraries {
		if library.Child == nil {
			continue
		}
		wait.Go(func() {
			snapshots[index], failures[index] = b.fetchServiceOperations(ctx, client, *library.Child, path)
		})
	}
	wait.Wait()
	cutoff := time.Now().Add(-10 * time.Minute)
	appLeases := probeLeases([]string{appLeaseDir()})
	// The status document carries resources (§10); the operations document
	// and the eject check do not.
	withResources := withLibraries && !fresh && strings.HasPrefix(path, "/api/status")
	sampled := []resourceProcess{{PID: os.Getpid(), Role: "broker"}}
	entries := []map[string]any{}
	for index, library := range libraries {
		service := map[string]any{"state": "stopped", "pid": nil, "started_at": nil, "restarts_10m": 0}
		restarts := 0
		starting := false
		for _, record := range records {
			if record.Kind != "service.start" || !operationTouches(record, library.ID) {
				continue
			}
			if started, ok := parseTime(record.StartedAt); ok && record.Trigger == triggerRespawn && started.After(cutoff) {
				restarts++
			}
			starting = starting || !opTerminal(record.State)
		}
		service["restarts_10m"] = restarts
		var own *ejectVerdict
		unreachable := ""
		var errorText any
		switch {
		case library.Child != nil && failures[index] == nil:
			service["state"], service["pid"], service["started_at"] = "running", library.Child.PID, formatTime(library.ChildStarted)
			if data, err := json.Marshal(snapshots[index]["eject"]); err == nil {
				var verdict ejectVerdict
				if json.Unmarshal(data, &verdict) == nil && verdict.Verdict != "" {
					own = &verdict
				}
			}
			if own == nil {
				unreachable = "Its service answered without an eject verdict (an older Pharos), so Pharos cannot tell what it is doing."
			}
		case library.Child != nil:
			service["state"] = "running"
			unreachable = "Its service did not answer: " + failures[index].Error()
			errorText = failures[index].Error()
		case starting:
			service["state"] = "starting"
		}
		// Without a service to ask, the broker reads the leases of Terminal
		// commands writing this library itself (lease files, never a capture
		// or index lock), and those of app-level commands naming it.
		leases := []leaseProbe{}
		if own == nil {
			leases = probeLeases(library.LeaseDirs)
		}
		for _, probe := range appLeases {
			if probe.Info != nil && slices.ContainsFunc(probe.Info.Libraries, func(path string) bool { return sameConfig(path, library) }) {
				leases = append(leases, probe)
			}
		}
		verdict := brokerLibraryVerdict(own, records, library.ID, unreachable, leases)
		var snapshot any
		if snapshots[index] != nil {
			snapshot = snapshots[index]
		}
		entry := map[string]any{"id": library.ID, "name": library.Name, "selected": library.Selected,
			"connected": library.Child != nil && failures[index] == nil, "service": service, "snapshot": snapshot,
			"eject": verdict, "error": errorText}
		if withResources {
			// The broker reads the service itself, so a service too busy
			// to answer is still measured, and the Terminal commands its
			// snapshot names (or, without one, the leases read above).
			processes := []resourceProcess{}
			if library.Child != nil {
				processes = append(processes, resourceProcess{PID: library.Child.PID, Role: "service", Library: library.ID, LibraryName: library.Name})
			}
			if snapshots[index] != nil {
				processes = append(processes, snapshotProcesses(snapshots[index], library.ID, library.Name)...)
			}
			processes = append(processes, leaseProcesses(leases, library.ID, library.Name)...)
			entry["resources"] = processes
			sampled = append(sampled, processes...)
		}
		entries = append(entries, entry)
	}
	app := []map[string]any{}
	for _, record := range records {
		if ended, ok := parseTime(record.EndedAt); ok && ended.Before(cutoff) {
			continue
		}
		app = append(app, brokerOperationEntry(record))
	}
	// App-level Terminal commands (a transfer, a recovery) under a live lease.
	for _, probe := range appLeases {
		if !probe.Live || probe.Info == nil {
			continue
		}
		kind := firstOf(probe.Info.Kinds)
		entry := brokerOperationEntry(brokerOperationRecord{ID: "lease:" + probe.Info.Instance, Kind: kind, Trigger: triggerCLI, State: opRunning, StartedAt: probe.Info.StartedAt})
		// A lease names libraries by configuration; surfaces group by the
		// registered IDs.
		// No lock is held: canonical reads the filesystem.
		ids := []string{}
		for _, path := range probe.Info.Libraries {
			for _, library := range registered {
				if sameConfig(path, library) && !slices.Contains(ids, library.ID) {
					ids = append(ids, library.ID)
				}
			}
		}
		entry["process"], entry["libraries"], entry["library_configs"] = probe.Info.Process, ids, probe.Info.Libraries
		entry["detail"] = "In another process (" + strings.Replace(probe.Info.Process, "cli:", "pharos ", 1) + ")"
		app = append(app, entry)
	}
	document := map[string]any{"version": 1, "revision": b.reg.currentRevision(), "generated_at": now(), "libraries": entries,
		"app": map[string]any{"operations": app, "update": nil}, "attention": []any{}}
	if withResources {
		sampled = append(sampled, leaseProcesses(appLeases, "", "")...)
		document["resources"] = b.libraryResources(entries, sampled)
	}
	return document
}

// libraryResources reads every process once and fills each library entry's
// resources (its service and the Terminal commands writing it) from the
// same readings; it returns the machine's total: the broker, every library's
// processes and the app-level Terminal commands, each process counted once.
func (b *brokerOperations) libraryResources(entries []map[string]any, processes []resourceProcess) *resourceSummary {
	readings := b.resources.sample(processes)
	byPID := map[int]resourceReading{}
	for _, reading := range readings {
		byPID[reading.PID] = reading
	}
	for _, entry := range entries {
		asked, _ := entry["resources"].([]resourceProcess)
		own := []resourceReading{}
		for _, process := range asked {
			if reading, ok := byPID[process.PID]; ok && !slices.ContainsFunc(own, func(r resourceReading) bool { return r.PID == process.PID }) {
				own = append(own, reading)
			}
		}
		name, _ := entry["name"].(string)
		entry["resources"] = summarizeResources(defaultString(name, "This library"), own)
	}
	return summarizeResources("Pharos", readings)
}

// cleanDeadLeases deletes the unheld leases in dir: app-level commands (a
// crashed transfer or recovery) have no journal rows to reconcile, so their
// dead leases go at once, under the deletion protocol (§6.5).
func cleanDeadLeases(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if _, done, ok := claimDeadLease(filepath.Join(dir, entry.Name())); ok {
			done(true)
		}
	}
}

// sameConfig reports whether a lease's library path is library's
// configuration, using the canonical path stored when it was published.
func sameConfig(path string, library brokerLibrary) bool {
	if path == library.Config {
		return true
	}
	resolved := library.Canonical
	if resolved == "" {
		resolved = canonical(library.Config) // published without one (tests)
	}
	return canonical(path) == resolved
}
