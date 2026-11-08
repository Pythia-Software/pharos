//go:build probe

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/labeler/labelertest"
)

// The lifecycle trial drives the scheduled engine's real controls (Stop,
// Resume, Cancel, shared demand, the CPU allowance and a graceful service
// restart) over real domain work in an isolated version-3 experiment arm.
// It is candidate only: unlike TestWorkReplica it uses runtime types. It
// reuses the replica probe's guards, durability and API helpers unchanged.
// docs: .context/validation/trial-api.md (root coordination).
//
//	PHAROS_WORK_ENGINE=scheduled PHAROS_WORK_TRIAL_ARM=/.../arm \
//	PHAROS_WORK_TRIAL_DESCRIPTOR=/.../trial.json \
//	  archive.test -test.run '^TestWorkTrial$' -test.timeout 8h -test.v

// trialKinds are the kinds a trial may demand: the unit-scoped chain
// authorship -> labels -> label facts, stored timing, and identities as the
// coarse representative. Git, tools and findings read this Mac's checkouts
// or GitHub, so they are refused.
var trialKinds = []string{kindAuthorship, kindLabels, kindLabelFacts, kindParallelism, kindIdentities}

// trialEnvironment is every PHAROS_* variable a trial process may see: its
// own inputs and what TestMain sets for disposable test state.
var trialEnvironment = []string{"PHAROS_WORK_ENGINE", "PHAROS_WORK_TRIAL_ARM", "PHAROS_WORK_TRIAL_DESCRIPTOR",
	"PHAROS_TEST_SUPPORT_DIR", "PHAROS_SUPPORT_DIR", "PHAROS_HOST_ID", "PHAROS_HOST_LABEL"}

// trialCancellationErrors are the causes a control-ended batch carries; a
// remembered failure with one of them would charge a control as a failure.
var trialCancellationErrors = []string{errWorkStopped.Error(), errWorkNoDemand.Error(), context.Canceled.Error()}

type trialDescriptor struct {
	Name          string              `json:"name"`
	Clock         string              `json:"clock"`
	Kinds         []string            `json:"kinds"`
	PendingCounts map[string]string   `json:"pending_counts"`
	RecentChecks  map[string][]string `json:"recent_checks"`
	MaxSeconds    int                 `json:"max_seconds"`
	WaitSeconds   int                 `json:"wait_seconds"`
	HoldSeconds   int                 `json:"hold_seconds"`
	SampleSeconds int                 `json:"sample_seconds"`
	Cycles        int                 `json:"cycles"`
}

func (d trialDescriptor) validate() error {
	if d.Name == "" || len(d.Kinds) == 0 {
		return errors.New("name the trial and its kinds")
	}
	if d.Clock != "" {
		if _, err := time.Parse(time.RFC3339, d.Clock); err != nil {
			return errors.New("clock is RFC3339")
		}
	}
	for _, kind := range d.Kinds {
		if !slices.Contains(trialKinds, kind) {
			return fmt.Errorf("kind %q is not a trial kind (%v)", kind, trialKinds)
		}
		if !readOnlyCheck(d.PendingCounts[kind]) {
			return fmt.Errorf("%s needs one read-only pending_counts query", kind)
		}
	}
	for kind, queries := range d.RecentChecks {
		if !slices.Contains(d.Kinds, kind) {
			return fmt.Errorf("recent_checks names an undemanded kind %q", kind)
		}
		for _, query := range queries {
			if !readOnlyCheck(query) {
				return fmt.Errorf("%s: a recent check is one SELECT or WITH statement", kind)
			}
		}
	}
	for _, bound := range []struct {
		name          string
		value, lo, hi int
	}{{"max_seconds", d.MaxSeconds, 300, 21600}, {"wait_seconds", d.WaitSeconds, 10, 3600}, {"hold_seconds", d.HoldSeconds, 5, 600},
		{"sample_seconds", d.SampleSeconds, 1, 60}, {"cycles", d.Cycles, 1, 5}} {
		if bound.value < bound.lo || bound.value > bound.hi {
			return fmt.Errorf("%s must be %d..%d", bound.name, bound.lo, bound.hi)
		}
	}
	return nil
}

// trialArm admits only a canonical version-3 arm outside the development
// library. tools/work_experiment.py refused every registered library when it
// made the arm; this is the backstop, as in TestWorkReplica.
func trialArm(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("the arm path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != filepath.Clean(root) || root != filepath.Clean(root) {
		return fmt.Errorf("the arm path must be canonical: %v", err)
	}
	if root == "/Volumes/euclid/Pharos" || strings.HasPrefix(root, "/Volumes/euclid/Pharos/") {
		return errors.New("refusing the development library")
	}
	var marker struct {
		Version int    `json:"version"`
		Role    string `json:"role"`
	}
	raw, err := os.ReadFile(filepath.Join(root, ".pharos-work-experiment.json"))
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &marker) != nil || marker.Version != 3 || marker.Role != "arm" {
		return errors.New("not a version-3 experiment arm")
	}
	return nil
}

// trialEnv refuses settings that would reach installed state, replace a
// frozen model (fake workers, download URLs, path overrides) or lock the
// CPU allowance the trial changes.
func trialEnv(root string, environ []string) error {
	values := map[string]string{}
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
		if strings.HasPrefix(name, "PHAROS_") && !slices.Contains(trialEnvironment, name) {
			return fmt.Errorf("%s is set; a trial runs with only its own PHAROS_ settings", name)
		}
	}
	if values[labelertest.FakeWorkerEnv] != "" || values[labelingFakeEnv] != "" {
		return errors.New("a fake model is configured")
	}
	if values["PHAROS_WORK_ENGINE"] != "scheduled" {
		return errors.New("a trial requires PHAROS_WORK_ENGINE=scheduled")
	}
	for _, name := range []string{"HOME", "TMPDIR"} {
		if !trialInside(root, values[name]) {
			return fmt.Errorf("%s must be inside the arm", name)
		}
	}
	return nil
}

func trialInside(root, path string) bool {
	if path == "" {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// trialConfig requires the arm-private configuration the harness writes:
// private paths, no sources or tokens, and frozen real model inputs.
func trialConfig(root string, config Config, kinds []string) error {
	if filepath.Clean(config.Path) != filepath.Join(root, "archive.toml") || filepath.Clean(config.CatalogPath) != filepath.Join(root, "catalog/catalog.sqlite3") {
		return errors.New("the configuration and catalog must be the arm's own")
	}
	for _, path := range []string{config.ArchiveRoot, config.CaptureRoot, config.StagingRoot} {
		if !trialInside(root, path) {
			return fmt.Errorf("%s is outside the arm", path)
		}
	}
	if config.Library || len(config.Sources) > 0 || config.GitHubToken != "" || config.TL1Token != "" {
		return errors.New("an arm has no library registration, sources or tokens")
	}
	if slices.Contains(kinds, kindLabels) && !config.Labels.Enabled {
		return errors.New("demanding labels requires labels_enabled")
	}
	inputs := filepath.Join(root, "inputs")
	frozen := func(path string) bool {
		resolved, err := filepath.EvalSymlinks(path)
		return err == nil && trialInside(inputs, resolved)
	}
	if config.Labels.Enabled {
		exe, _ := os.Executable()
		if !frozen(config.Labels.ModelDir) || !frozen(config.Labels.Worker) || config.Labels.Worker == exe {
			return errors.New("labels need a frozen model and worker under the arm's inputs")
		}
		for _, path := range []string{config.Labels.Runtime, config.Labels.MLXRuntime} {
			if path != "" && !frozen(path) {
				return fmt.Errorf("labels runtime %s is not a frozen input", path)
			}
		}
	}
	if config.AgentEvents.Enabled && !frozen(config.AgentEvents.ModelDir) {
		return errors.New("agent events need a frozen model under the arm's inputs")
	}
	return nil
}

// Report shapes. Every check names the contract it restates.
type trialCheck struct {
	Name     string `json:"name"`
	Contract string `json:"contract"`
	Pass     bool   `json:"pass"`
	Evidence any    `json:"evidence"`
}

type trialUntested struct {
	Case   string `json:"case"`
	Reason string `json:"reason"`
}

type trialSample struct {
	Label           string             `json:"label"`
	Seconds         float64            `json:"seconds"`
	Unix            float64            `json:"unix"`
	SelfFootprint   uint64             `json:"self_footprint"`
	SelfResident    uint64             `json:"self_resident"`
	Descendants     map[string]int     `json:"descendants"`
	DescendantsFoot uint64             `json:"descendants_footprint"`
	TreeFootprint   uint64             `json:"tree_footprint"`
	Complete        bool               `json:"complete"`
	Go              map[string]uint64  `json:"go"`
	Authority       workAuthorityFacts `json:"authority"`
	InspectionBytes int                `json:"inspection_bytes"`
	LedgerTasks     int                `json:"ledger_tasks"`
}

type trialState struct {
	Label     string             `json:"label"`
	Seconds   float64            `json:"seconds"`
	Requests  []map[string]any   `json:"requests"`
	Tasks     []workTaskFacts    `json:"tasks"`
	Authority workAuthorityFacts `json:"authority"`
}

type trialReport struct {
	Version    int                  `json:"version"`
	Name       string               `json:"name"`
	Arm        string               `json:"arm"`
	Clock      string               `json:"clock"`
	Kinds      []string             `json:"kinds"`
	Engine     string               `json:"engine"`
	Config     map[string]any       `json:"config"`
	Startup    []map[string]any     `json:"startup"`
	Operations []map[string]any     `json:"operations"`
	States     []trialState         `json:"states"`
	Samples    []trialSample        `json:"samples"`
	Pending    []map[string]any     `json:"pending"`
	Checks     []trialCheck         `json:"checks"`
	Untested   []trialUntested      `json:"untested"`
	Windows    map[string][]float64 `json:"windows"`
	Passed     bool                 `json:"passed"`
	Failure    string               `json:"failure,omitempty"`
}

// trialAlwaysUntested is what this probe never exercises.
var trialAlwaysUntested = []trialUntested{
	{"abrupt process crash", "the restart is the product's graceful release (Server.stop); kill -9 and recovery are orchestrated by root"},
	{"catalog integrity after restart", "quick_check of the full catalog is left to the harness"},
	{"global background pause", "not driven; the trial uses per-kind Stop/Resume"},
	{"disable/re-enable labels", "configuration changes interrupt requests (InterruptKinds) and are not driven"},
	{"continuous edits and source arrivals", "no capture or index runs in the trial"},
	{"multi-process sharing", "each process has its own allowance; only in-process shared demand is tested"},
	{"Retry", "only meaningful after real failures; failures are reported, not retried"},
}

type trial struct {
	t        *testing.T
	ctx      context.Context
	root     string
	d        trialDescriptor
	clock    time.Time
	config   Config
	server   *Server
	checks   *sql.DB
	started  time.Time
	deadline time.Time
	mu       sync.Mutex
	report   trialReport
	bounded  map[string]bool
}

func TestWorkTrial(t *testing.T) {
	root := os.Getenv("PHAROS_WORK_TRIAL_ARM")
	if root == "" {
		t.Skip("set PHAROS_WORK_TRIAL_ARM to a freshly prepared version-3 experiment arm")
	}
	if err := trialArm(root); err != nil {
		t.Fatal(err)
	}
	if err := trialEnv(root, os.Environ()); err != nil {
		t.Fatal(err)
	}
	var d trialDescriptor
	raw, err := os.ReadFile(os.Getenv("PHAROS_WORK_TRIAL_DESCRIPTOR"))
	if err == nil {
		err = json.Unmarshal(raw, &d)
	}
	if err == nil {
		err = d.validate()
	}
	if err != nil {
		t.Fatalf("trial descriptor: %v", err)
	}
	slices.Sort(d.Kinds)
	d.Kinds = slices.Compact(d.Kinds)
	savedDurability, savedLedgerSync, savedSyncToDrive := catalogDurability, labelingLedgerSynchronous, syncToDrive
	catalogDurability, labelingLedgerSynchronous, syncToDrive = replicaDurability, replicaLedgerSync, replicaSyncToDrive
	defer func() {
		catalogDurability, labelingLedgerSynchronous, syncToDrive = savedDurability, savedLedgerSync, savedSyncToDrive
	}()
	tr := &trial{t: t, root: root, d: d, started: time.Now(), bounded: map[string]bool{}}
	tr.deadline = tr.started.Add(time.Duration(d.MaxSeconds) * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), tr.deadline)
	defer cancel()
	tr.ctx = ctx
	if d.Clock != "" {
		tr.clock, _ = time.Parse(time.RFC3339, d.Clock)
	}
	tr.report = trialReport{Version: 1, Name: d.Name, Arm: root, Clock: d.Clock, Kinds: d.Kinds, Windows: map[string][]float64{},
		Untested: slices.Clone(trialAlwaysUntested)}
	tr.open("startup")
	defer func() {
		if tr.checks != nil {
			tr.checks.Close()
		}
	}()
	tr.run()
}

// open loads the arm's configuration and opens its catalog and a server
// without a listener, measuring the open as startup.
func (tr *trial) open(label string) {
	config, err := loadConfig(filepath.Join(tr.root, "archive.toml"), false)
	if err == nil {
		err = trialConfig(tr.root, config, tr.d.Kinds)
	}
	if err != nil {
		tr.fail("%s: %v", label, err)
	}
	opening := replicaCPUNow()
	c, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		tr.fail("%s: %v", label, err)
	}
	c.evidenceOnly = true
	if !tr.clock.IsZero() {
		c.now = func() time.Time { return tr.clock }
	}
	server := testServer(tr.t, config, c)
	if tr.checks == nil {
		tr.checks, err = sql.Open("sqlite", (&url.URL{Scheme: "file", Path: config.CatalogPath}).String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
		if err != nil {
			tr.fail("%s: %v", label, err)
		}
	}
	window := opening.until(replicaCPUNow())
	window["label"] = label
	tr.mu.Lock()
	tr.config, tr.server = config, server
	tr.report.Startup = append(tr.report.Startup, window)
	tr.report.Windows[label] = []float64{replicaUnix(opening.at), window["until_unix"].(float64)}
	tr.mu.Unlock()
	status, value, _ := probeJSON(server, config.APIToken, http.MethodGet, "/api/work", nil)
	if status != http.StatusOK || value["engine"] != "scheduled" {
		tr.fail("%s: this process did not configure the scheduled engine (%d)", label, status)
	}
	labels := config.Labels
	tr.report.Engine = "scheduled"
	tr.report.Config = map[string]any{"cpu_slots": config.BackgroundCPUs, "logical_cpus": runtime.NumCPU(),
		"labels": map[string]any{"enabled": labels.Enabled, "backend": labels.Backend, "threads": labels.Threads, "workers": labels.Workers, "model": filepath.Base(labels.ModelDir)}}
}

func (tr *trial) seconds() float64 { return time.Since(tr.started).Seconds() }

func (tr *trial) save() {
	tr.mu.Lock()
	data, err := json.MarshalIndent(tr.report, "", "  ")
	tr.mu.Unlock()
	if err == nil {
		err = os.WriteFile(filepath.Join(tr.root, "probe-trial-report.json"), append(data, '\n'), 0o600)
	}
	if err != nil {
		tr.t.Error(err)
	}
}

// fail ends the trial with its report saved.
func (tr *trial) fail(format string, args ...any) {
	tr.mu.Lock()
	tr.report.Failure = fmt.Sprintf(format, args...)
	tr.mu.Unlock()
	tr.save()
	tr.t.Fatalf(format, args...)
}

func (tr *trial) check(name, contract string, pass bool, evidence any) bool {
	tr.mu.Lock()
	tr.report.Checks = append(tr.report.Checks, trialCheck{name, contract, pass, evidence})
	tr.mu.Unlock()
	if !pass {
		tr.t.Errorf("check %s failed: %v", name, evidence)
	}
	return pass
}

func (tr *trial) untested(name, reason string) {
	tr.mu.Lock()
	tr.report.Untested = append(tr.report.Untested, trialUntested{name, reason})
	tr.mu.Unlock()
}

// call is one recorded API request; small responses are kept whole.
func (tr *trial) call(method, route string, body any) (int, map[string]any) {
	status, value, err := probeJSON(tr.server, tr.config.APIToken, method, route, body)
	entry := map[string]any{"seconds": tr.seconds(), "method": method, "route": route, "body": body, "status": status}
	if data, _ := json.Marshal(value); len(data) <= 2048 {
		entry["response"] = value
	} else {
		entry["response_bytes"] = len(data)
	}
	if err != nil {
		entry["error"] = err.Error()
	}
	tr.mu.Lock()
	tr.report.Operations = append(tr.report.Operations, entry)
	tr.mu.Unlock()
	return status, value
}

func (tr *trial) submit(action string) uint64 {
	status, value := tr.call(http.MethodPost, "/api/work", map[string]any{"action": action, "kinds": tr.d.Kinds})
	id, _ := value["request_id"].(json.Number)
	parsed, err := id.Int64()
	if status != http.StatusAccepted || err != nil || parsed <= 0 {
		tr.fail("%s request refused: %d %v", action, status, value)
	}
	return uint64(parsed)
}

// inspect reads GET /api/work into the runner's own inspection type.
func (tr *trial) inspect() (workInspection, int) {
	in, bytes, err := tr.read()
	if err != nil {
		tr.fail("%v", err)
	}
	return in, bytes
}

// read is inspect without failing, for the sampling goroutine.
func (tr *trial) read() (workInspection, int, error) {
	request := httptest.NewRequest(http.MethodGet, "/api/work", nil)
	if tr.config.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+tr.config.APIToken)
	}
	response := httptest.NewRecorder()
	tr.server.ServeHTTP(response, request)
	var doc struct {
		State workInspection `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil || response.Code != http.StatusOK {
		return workInspection{}, 0, fmt.Errorf("work inspection: %d %v", response.Code, err)
	}
	return doc.State, response.Body.Len(), nil
}

func (tr *trial) state(label string, in workInspection) {
	entry := trialState{Label: label, Seconds: tr.seconds(), Tasks: in.Tasks, Authority: in.Authority}
	for _, list := range [][]workRequestFacts{in.Requests, in.Ended} {
		for _, q := range list {
			entry.Requests = append(entry.Requests, map[string]any{"id": q.ID, "scope": q.Scope, "state": q.State, "reason": q.Reason,
				"blocker": q.Blocker, "members": q.Members, "sealed": q.Sealed, "failed": q.Failed, "unfinished": q.Unfinished})
		}
	}
	tr.mu.Lock()
	tr.report.States = append(tr.report.States, entry)
	tr.mu.Unlock()
}

// wait polls the inspection until cond holds, within limit and the trial's
// deadline. The final inspection is recorded either way.
func (tr *trial) wait(label string, limit time.Duration, cond func(workInspection) bool) (workInspection, bool) {
	end := time.Now().Add(limit)
	if end.After(tr.deadline) {
		end = tr.deadline
	}
	for {
		in, _ := tr.inspect()
		if cond(in) {
			tr.state(label, in)
			return in, true
		}
		if !time.Now().Before(end) {
			tr.state(label+": timed out", in)
			return in, false
		}
		select {
		case <-tr.ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (tr *trial) waitLimit() time.Duration { return time.Duration(tr.d.WaitSeconds) * time.Second }

func (tr *trial) counts(label string) map[string]int64 {
	counts := map[string]int64{}
	for _, kind := range tr.d.Kinds {
		var n int64
		if err := tr.checks.QueryRowContext(tr.ctx, tr.d.PendingCounts[kind]).Scan(&n); err != nil {
			tr.fail("pending count %s: %v", kind, err)
		}
		counts[kind] = n
	}
	tr.mu.Lock()
	tr.report.Pending = append(tr.report.Pending, map[string]any{"label": label, "seconds": tr.seconds(), "counts": counts})
	tr.mu.Unlock()
	return counts
}

// sample records physical and Go memory with the inspection, and checks the
// inspection's bounds.
func (tr *trial) sample(label string) {
	s := trialSample{Label: label, Seconds: tr.seconds(), Unix: replicaUnix(time.Now()), Descendants: map[string]int{}, Complete: true}
	if self, err := processUsageOf(os.Getpid()); err == nil {
		s.SelfFootprint, s.SelfResident = self.Footprint, self.Resident
	} else {
		s.Complete = false
	}
	nodes := resourceProcessTable()
	if nodes == nil {
		s.Complete = false
	}
	children := map[int][]processNode{}
	for _, node := range nodes {
		children[node.Parent] = append(children[node.Parent], node)
	}
	for frontier := []int{os.Getpid()}; len(frontier) > 0; frontier = frontier[1:] {
		for _, child := range children[frontier[0]] {
			s.Descendants[child.Name]++
			if usage, err := processUsageOf(child.PID); err == nil {
				s.DescendantsFoot += usage.Footprint
			} else {
				s.Complete = false
			}
			frontier = append(frontier, child.PID)
		}
	}
	s.TreeFootprint = s.SelfFootprint + s.DescendantsFoot
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s.Go = map[string]uint64{"heap_alloc": m.HeapAlloc, "heap_inuse": m.HeapInuse, "heap_idle": m.HeapIdle,
		"heap_released": m.HeapReleased, "sys": m.Sys, "stack_inuse": m.StackInuse, "num_gc": uint64(m.NumGC)}
	in, bytes, err := tr.read()
	if err != nil {
		s.Complete = false
	}
	s.Authority, s.InspectionBytes = in.Authority, bytes
	ledger := tr.server.ops.resourceLedger
	s.LedgerTasks = len(ledger.export("", time.Now()).Tasks)
	tr.mu.Lock()
	tr.report.Samples = append(tr.report.Samples, s)
	tr.mu.Unlock()
	if err != nil {
		return
	}
	for _, violation := range trialBounds(in, s.LedgerTasks, ledger.total) {
		// One failed check per bound, with its first evidence.
		name, _, _ := strings.Cut(violation, ":")
		tr.mu.Lock()
		first := !tr.bounded[name]
		tr.bounded[name] = true
		tr.mu.Unlock()
		if first {
			tr.check("bounded inspection: "+name, "workDecisionLimit, workEndedRequestLimit, workLiveRequestLimit, workFailureLimit, five failure examples, workProcessMemoryLimit, the task resource ledger's total", false,
				map[string]any{"label": label, "violation": violation})
		}
	}
}

// boundsHeld records the passing bounds check with what was observed.
func (tr *trial) boundsHeld() {
	tr.mu.Lock()
	breached := len(tr.bounded) > 0
	maxBytes, maxLedger := 0, 0
	for _, s := range tr.report.Samples {
		maxBytes, maxLedger = max(maxBytes, s.InspectionBytes), max(maxLedger, s.LedgerTasks)
	}
	samples := len(tr.report.Samples)
	tr.mu.Unlock()
	if !breached {
		tr.check("bounded inspection", "workDecisionLimit, workEndedRequestLimit, workLiveRequestLimit, workFailureLimit, five failure examples, workProcessMemoryLimit, the task resource ledger's total", true,
			map[string]any{"samples": samples, "max_inspection_bytes": maxBytes, "max_ledger_tasks": maxLedger})
	}
}

// trialBounds lists the inspection's breaches of the runner's documented
// bounds ("name: detail").
func trialBounds(in workInspection, ledgerTasks, ledgerLimit int) []string {
	var out []string
	if len(in.Decisions) > workDecisionLimit {
		out = append(out, fmt.Sprintf("decisions: %d > %d", len(in.Decisions), workDecisionLimit))
	}
	if len(in.Ended) > workEndedRequestLimit {
		out = append(out, fmt.Sprintf("ended: %d > %d", len(in.Ended), workEndedRequestLimit))
	}
	if len(in.Requests) > workLiveRequestLimit {
		out = append(out, fmt.Sprintf("requests: %d > %d", len(in.Requests), workLiveRequestLimit))
	}
	for _, task := range in.Tasks {
		if len(task.Examples) > 5 || task.Failures > workFailureLimit {
			out = append(out, fmt.Sprintf("failures: %s %d examples, %d failures", task.Kind, len(task.Examples), task.Failures))
		}
	}
	if in.Authority.MemoryReserved > in.Authority.MemoryLimit {
		out = append(out, fmt.Sprintf("memory: %d reserved > %d", in.Authority.MemoryReserved, in.Authority.MemoryLimit))
	}
	if ledgerTasks > ledgerLimit {
		out = append(out, fmt.Sprintf("ledger: %d > %d", ledgerTasks, ledgerLimit))
	}
	return out
}

// sampler takes a sample every sample_seconds until the returned stop.
func (tr *trial) sampler() (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Duration(tr.d.SampleSeconds) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-tr.ctx.Done():
				return
			case <-ticker.C:
				tr.sample("interval")
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

func trialTask(in workInspection, kind string) workTaskFacts {
	for _, task := range in.Tasks {
		if task.Kind == kind {
			return task
		}
	}
	return workTaskFacts{Kind: kind}
}

// trialRequest finds a request among the live and ended ones.
func trialRequest(in workInspection, id uint64) (q workRequestFacts, ended, found bool) {
	for _, q := range in.Requests {
		if q.ID == id {
			return q, false, true
		}
	}
	for _, q := range in.Ended {
		if q.ID == id {
			return q, true, true
		}
	}
	return workRequestFacts{}, false, false
}

// trialShared is a kind whose running batch serves every one of ids.
func trialShared(in workInspection, ids ...uint64) string {
	for _, task := range in.Tasks {
		if task.Running != nil && !slices.ContainsFunc(ids, func(id uint64) bool { return !slices.Contains(task.Running.Demanders, id) }) {
			return task.Kind
		}
	}
	return ""
}

// trialCancellationFailures are remembered failures caused by a control.
func trialCancellationFailures(in workInspection) []string {
	var out []string
	for _, task := range in.Tasks {
		for _, f := range task.Examples {
			if slices.ContainsFunc(trialCancellationErrors, func(text string) bool { return strings.Contains(f.Error, text) }) {
				out = append(out, task.Kind+": "+f.Error)
			}
		}
	}
	return out
}

func trialCommitted(in workInspection) (sum int64) {
	for _, task := range in.Tasks {
		sum += task.Committed
	}
	return sum
}

func (tr *trial) running(in workInspection) []string {
	var kinds []string
	for _, task := range in.Tasks {
		if task.Running != nil && slices.Contains(tr.d.Kinds, task.Kind) {
			kinds = append(kinds, task.Kind)
		}
	}
	return kinds
}

// trialAllowance checks a sequence of authority readings after the cap
// changed to target (workAuthority.SetCPUs): the cap is the target, nothing
// new is admitted while over budget, and once under the cap allocation stays
// within it. drained is the first reading within the cap, or -1.
func trialAllowance(readings []workAuthorityFacts, target int) (drained int, violations []string) {
	drained = -1
	for i, r := range readings {
		if r.CPUCap != target {
			violations = append(violations, fmt.Sprintf("reading %d: cap %d, want %d", i, r.CPUCap, target))
		}
		over := r.CPUAllocated > r.CPUCap
		switch {
		case drained < 0 && !over:
			drained = i
		case drained < 0 && i > 0 && r.CPUAllocated > readings[i-1].CPUAllocated:
			violations = append(violations, fmt.Sprintf("reading %d: allocation grew to %d while over budget", i, r.CPUAllocated))
		case drained >= 0 && over:
			violations = append(violations, fmt.Sprintf("reading %d: %d allocated over cap %d after draining", i, r.CPUAllocated, r.CPUCap))
		}
	}
	return drained, violations
}

func (tr *trial) run() {
	tr.counts("baseline")
	tr.sample("startup")
	stopSampling := tr.sampler()
	defer func() { stopSampling() }()
	a := tr.sharing()
	tr.stopResume()
	tr.allowance()
	// The sampler reads the server, which the restart replaces.
	stopSampling()
	tr.restart(a)
	stopSampling = tr.sampler()
	tr.recover()
	stopSampling()
	tr.sample("end")
	tr.boundsHeld()
	if err := tr.server.stop(stopTimeout); err != nil {
		tr.fail("final release: %v", err)
	}
	tr.mu.Lock()
	tr.report.Windows["trial"] = []float64{replicaUnix(tr.started), replicaUnix(time.Now())}
	tr.report.Passed = !slices.ContainsFunc(tr.report.Checks, func(c trialCheck) bool { return !c.Pass })
	passed := tr.report.Passed
	tr.mu.Unlock()
	tr.save()
	fmt.Printf("work trial %s: passed=%v checks=%d; private report saved\n", tr.d.Name, passed, len(tr.report.Checks))
}

// sharing submits whole-history and recent demand for the same kinds, shows
// one batch serving both, then cancels the recent request.
func (tr *trial) sharing() uint64 {
	a := tr.submit("backfill")
	b := tr.submit("recent")
	in, ok := tr.wait("shared batch", tr.waitLimit(), func(in workInspection) bool {
		_, ended, _ := trialRequest(in, b)
		return ended || trialShared(in, a, b) != ""
	})
	if q, _, _ := trialRequest(in, b); q.FrozenAt.IsZero() || q.Members == 0 {
		tr.untested("shared demand", fmt.Sprintf("the recent request froze %d members before %s", q.Members, map[bool]string{true: "ending", false: "the wait ended"}[ok]))
		return a
	}
	kind := trialShared(in, a, b)
	if !tr.check("shared demand", "TestWorkSharedBatchOutlivesItsRequester: one running batch lists every requester as a demander", kind != "",
		map[string]any{"kind": kind, "requests": []uint64{a, b}, "timed_out": !ok}) {
		return a
	}
	tr.sample("shared")
	committed := trialCommitted(in)
	status, value := tr.call(http.MethodPost, "/api/work", map[string]any{"action": "cancel", "request_id": b})
	in, ok = tr.wait("cancelled shared requester", tr.waitLimit(), func(in workInspection) bool {
		q, ended, _ := trialRequest(in, b)
		return ended && q.State == opCancelled
	})
	tr.check("cancel one requester", "postWork cancel / workRunner.Cancel: the request ends cancelled", status == http.StatusOK && value["cancelled"] == true && ok,
		map[string]any{"status": status, "cancelled": value["cancelled"]})
	in, ok = tr.wait("shared work continues", tr.waitLimit(), func(in workInspection) bool {
		q, ended, _ := trialRequest(in, a)
		return trialCommitted(in) > committed || ended && q.State == opSucceeded
	})
	q, ended, _ := trialRequest(in, a)
	tr.check("shared work outlives a requester", "TestWorkServiceSharedExecutionOutlivesItsFirstRequester: the remaining request keeps its work and no failure is charged",
		ok && (!ended || q.State == opSucceeded) && len(trialCancellationFailures(in)) == 0,
		map[string]any{"committed_before": committed, "committed_after": trialCommitted(in), "a_state": q.State, "control_failures": trialCancellationFailures(in)})
	return a
}

// stopResume stops a running kind, holds it and resumes it, cycles times,
// rotating over the trial's kinds.
func (tr *trial) stopResume() {
	next := 0
	for cycle := range tr.d.Cycles {
		label := fmt.Sprintf("stop/resume %d", cycle+1)
		var kind string
		in, ok := tr.wait(label+": a running kind", tr.waitLimit(), func(in workInspection) bool {
			running := tr.running(in)
			for i := range tr.d.Kinds {
				if candidate := tr.d.Kinds[(next+i)%len(tr.d.Kinds)]; slices.Contains(running, candidate) {
					kind = candidate
					return true
				}
			}
			return false
		})
		if !ok {
			tr.untested(label, "no trial kind was running")
			continue
		}
		next = (slices.Index(tr.d.Kinds, kind) + 1) % len(tr.d.Kinds)
		before := trialTask(in, kind)
		status, value := tr.call(http.MethodPost, "/api/work", map[string]any{"action": "stop", "kind": kind})
		stoppedAt := time.Now()
		in, ok = tr.wait(label+": drained", tr.waitLimit(), func(in workInspection) bool {
			task := trialTask(in, kind)
			return task.Running == nil && task.Blocker != nil && task.Blocker.Reason == workBlockedHeld
		})
		tr.check(label+": stop drains at a boundary", "workRunner.Stop: the running batch drains and the kind is held", status == http.StatusOK && value["changed"] == true && ok,
			map[string]any{"kind": kind, "execution": before.Execution, "drain_seconds": time.Since(stoppedAt).Seconds()})
		if !ok {
			tr.call(http.MethodPost, "/api/work", map[string]any{"action": "resume", "kind": kind})
			continue
		}
		tr.sample(label + ": held")
		held, pendingHeld := trialTask(in, kind), tr.counts(label + ": held")[kind]
		var restarted []string
		holdEnd := time.Now().Add(time.Duration(tr.d.HoldSeconds) * time.Second)
		for time.Now().Before(holdEnd) && tr.ctx.Err() == nil {
			in, _ := tr.inspect()
			if task := trialTask(in, kind); task.Running != nil || task.Batches != held.Batches {
				restarted = append(restarted, fmt.Sprintf("%.1fs: running=%v batches=%d", tr.seconds(), task.Running != nil, task.Batches))
				break
			}
			select {
			case <-tr.ctx.Done():
			case <-time.After(time.Second):
			}
		}
		pendingAfter := tr.counts(label + ": hold end")[kind]
		tr.check(label+": held kind stays dirty", "TestWorkStopHoldsTheTaskUntilResume: no batch restarts from rediscovered dirty work and its domain stays dirty",
			len(restarted) == 0 && pendingAfter >= pendingHeld, map[string]any{"kind": kind, "restarted": restarted, "pending_held": pendingHeld, "pending_after": pendingAfter, "hold_seconds": tr.d.HoldSeconds})
		status, value = tr.call(http.MethodPost, "/api/work", map[string]any{"action": "resume", "kind": kind})
		in, ok = tr.wait(label+": resumed", tr.waitLimit(), func(in workInspection) bool {
			task := trialTask(in, kind)
			return task.Running != nil && task.Execution != before.Execution || task.Blocker == nil && task.State == ""
		})
		task := trialTask(in, kind)
		tr.check(label+": resume runs in a new execution", "workRunner.Resume: work resumes in a new execution, never the stopped one; or no demand remains",
			status == http.StatusOK && value["changed"] == true && ok && len(trialCancellationFailures(in)) == 0,
			map[string]any{"kind": kind, "stopped_execution": before.Execution, "execution": task.Execution, "state": task.State, "control_failures": trialCancellationFailures(in)})
	}
}

// allowance halves the CPU allowance while work runs, then restores it.
func (tr *trial) allowance() {
	in, _ := tr.inspect()
	original := in.Authority.CPUCap
	target := max(1, original/2)
	if target == original {
		if runtime.NumCPU() < 2 {
			tr.untested("CPU allowance change", "one logical CPU")
			return
		}
		target = 2
	}
	if len(tr.running(in)) == 0 {
		tr.untested("CPU allowance change while running", "no trial kind was running; the change is still checked")
	}
	status, _ := tr.call(http.MethodPost, "/api/work/settings", map[string]any{"cpu_slots": target})
	readings, exceeds := []workAuthorityFacts{}, []string{}
	end := time.Now().Add(min(tr.waitLimit(), time.Until(tr.deadline)))
	holdEnd := time.Time{}
	for time.Now().Before(end) && tr.ctx.Err() == nil {
		in, _ := tr.inspect()
		readings = append(readings, in.Authority)
		for _, q := range in.Requests {
			if q.Blocker != nil && q.Blocker.Reason == workBlockedExceedsCap && !slices.Contains(exceeds, q.Blocker.Detail) {
				exceeds = append(exceeds, q.Blocker.Detail)
			}
		}
		if drained, _ := trialAllowance(readings, target); drained >= 0 && holdEnd.IsZero() {
			holdEnd = time.Now().Add(time.Duration(tr.d.HoldSeconds) * time.Second)
			tr.sample("allowance drained")
		}
		if !holdEnd.IsZero() && time.Now().After(holdEnd) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	drained, violations := trialAllowance(readings, target)
	maxAllocated := 0
	for _, r := range readings {
		maxAllocated = max(maxAllocated, r.CPUAllocated)
	}
	tr.check("CPU allowance shrinks at boundaries", "workAuthority.SetCPUs / TestWorkCPUGrantsAreExplicitAndHonest: grants drain at their boundary, nothing new is admitted over budget, allocation then stays within the cap",
		status == http.StatusOK && drained >= 0 && len(violations) == 0,
		map[string]any{"from": original, "to": target, "readings": len(readings), "drained_reading": drained, "max_allocated": maxAllocated, "violations": violations, "exceeds_cap_blockers": exceeds})
	status, _ = tr.call(http.MethodPost, "/api/work/settings", map[string]any{"cpu_slots": original})
	in, _ = tr.inspect()
	reloaded, err := loadConfig(filepath.Join(tr.root, "archive.toml"), false)
	tr.check("CPU allowance restored", "postWorkSettings: the cap and the arm's background_cpu_slots follow the setting", status == http.StatusOK && in.Authority.CPUCap == original && err == nil && reloaded.BackgroundCPUs == original,
		map[string]any{"cap": in.Authority.CPUCap, "configured": reloaded.BackgroundCPUs, "error": fmt.Sprint(err)})
}

// restart releases the service while work runs (the product's graceful
// stop, not a crash) and reopens the catalog in a new server.
func (tr *trial) restart(a uint64) {
	in, ok := tr.wait("before release", tr.waitLimit(), func(in workInspection) bool { return len(tr.running(in)) > 0 })
	if !ok {
		tr.untested("release while a batch runs", "no trial kind was running; the release is still checked")
	}
	running := tr.running(in)
	tr.sample("before release")
	released := time.Now()
	err := tr.server.stop(stopTimeout)
	within := err == nil
	if errors.Is(err, errStopTimeout) {
		select {
		case <-tr.server.life.closed:
			err = tr.server.life.closeErr
		case <-time.After(tr.waitLimit()):
		}
	}
	releaseSeconds := time.Since(released).Seconds()
	tr.mu.Lock()
	tr.report.Windows["release"] = []float64{replicaUnix(released), replicaUnix(time.Now())}
	tr.mu.Unlock()
	if err != nil {
		tr.fail("release: %v", err)
	}
	// The released runner still answers; a refused read is recorded as such.
	after, _, readErr := tr.read()
	q, _, _ := trialRequest(after, a)
	if readErr == nil {
		tr.state("after release", after)
	}
	tr.check("release within the stop timeout", "Server.stop(stopTimeout): a release closes the catalog within stopTimeout",
		within, map[string]any{"seconds": releaseSeconds, "stop_timeout_seconds": stopTimeout.Seconds(), "running": running, "backfill_state": q.State})
	closed := tr.counts("closed")
	tr.open("restart")
	reopened := tr.counts("reopened")
	in, _ = tr.inspect()
	held := []string{}
	for _, task := range in.Tasks {
		if task.Blocker != nil && task.Blocker.Reason == workBlockedHeld {
			held = append(held, task.Kind)
		}
	}
	tr.state("reopened", in)
	tr.sample("reopened")
	equal, lost := true, []string{}
	for kind, n := range closed {
		equal = equal && reopened[kind] == n
		if reopened[kind] < n {
			lost = append(lost, kind)
		}
	}
	tr.check("dirty work survives restart", "Stop/release keep the domain dirty: no pending count after reopening is below the count when the catalog closed",
		len(lost) == 0, map[string]any{"closed": closed, "reopened": reopened, "equal": equal, "lost": lost, "interrupted_kinds": running})
	tr.check("holds end with the service", "workRunner.Stop: the hold is runtime only; a restart ends it", len(held) == 0, map[string]any{"held": held})
}

// recover settles recent demand after the restart, then runs whole-history
// demand until the deadline nears and cancels it.
func (tr *trial) recover() {
	r := tr.submit("recent")
	in, ok := tr.wait("recent after restart", tr.waitLimit(), func(in workInspection) bool { _, ended, _ := trialRequest(in, r); return ended })
	q, _, _ := trialRequest(in, r)
	pending := map[string]int{}
	for kind, queries := range tr.d.RecentChecks {
		for i, query := range queries {
			if dirty, err := checkPending(tr.ctx, tr.checks, query); err != nil || dirty {
				pending[fmt.Sprintf("%s#%d", kind, i)]++
			}
		}
	}
	tr.check("recent demand settles after restart", "the recent request succeeds and its recent_checks are empty", ok && q.State == opSucceeded && len(pending) == 0,
		map[string]any{"state": q.State, "reason": q.Reason, "members": q.Members, "sealed": q.Sealed, "failed": q.Failed, "pending_checks": pending})
	a := tr.submit("backfill")
	// Keep room for the cancellation, its drain and the hold.
	room := time.Until(tr.deadline) - 2*tr.waitLimit() - time.Duration(tr.d.HoldSeconds)*time.Second
	in, _ = tr.wait("whole history", max(room, 0), func(in workInspection) bool { _, ended, _ := trialRequest(in, a); return ended })
	if q, ended, _ := trialRequest(in, a); ended {
		tr.check("whole history settles", "the backfill request succeeds", q.State == opSucceeded, map[string]any{"state": q.State, "reason": q.Reason, "failed": q.Failed})
		tr.untested("cancel the last demander", "whole-history demand settled before the deadline")
		return
	}
	status, value := tr.call(http.MethodPost, "/api/work", map[string]any{"action": "cancel", "request_id": a})
	in, ok = tr.wait("last demander cancelled", tr.waitLimit(), func(in workInspection) bool {
		q, ended, _ := trialRequest(in, a)
		return ended && q.State == opCancelled && len(tr.running(in)) == 0
	})
	drained := tr.counts("cancel drained")
	time.Sleep(time.Duration(tr.d.HoldSeconds) * time.Second)
	later := tr.counts("cancel hold end")
	decreased := []string{}
	for kind, n := range drained {
		if later[kind] < n {
			decreased = append(decreased, kind)
		}
	}
	tr.check("the last demander leaving keeps the domain dirty", "TestWorkSharedBatchOutlivesItsRequester: the batch stops at its boundary without a failure, the domain still dirty",
		status == http.StatusOK && value["cancelled"] == true && ok && len(decreased) == 0 && len(trialCancellationFailures(in)) == 0,
		map[string]any{"drained": drained, "later": later, "decreased": decreased, "running": tr.running(in), "control_failures": trialCancellationFailures(in)})
}

// The guards refuse anything but an isolated, frozen, arm-private trial.
func TestWorkTrialGuards(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker := func(value string) {
		if err := os.WriteFile(filepath.Join(root, ".pharos-work-experiment.json"), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if trialArm(root) == nil {
		t.Fatal("an arm without a marker was admitted")
	}
	for _, refused := range []string{`{"version":2,"role":"arm"}`, `{"version":3,"role":"master"}`} {
		marker(refused)
		if trialArm(root) == nil {
			t.Fatalf("admitted %s", refused)
		}
	}
	marker(`{"version":3,"role":"arm"}`)
	if err := trialArm(root); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, refused := range []string{link, root + "/.", "relative/arm", "/Volumes/euclid/Pharos/x"} {
		if trialArm(refused) == nil {
			t.Fatalf("admitted %s", refused)
		}
	}

	env := []string{"PHAROS_WORK_ENGINE=scheduled", "HOME=" + root + "/home", "TMPDIR=" + root + "/tmp", "PHAROS_SUPPORT_DIR=/tmp/x"}
	if err := trialEnv(root, env); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"PHAROS_BACKGROUND_CPUS=2", labelertest.FakeWorkerEnv + "=ok", "PHAROS_LABELS_MODEL_URL=http://x", "PHAROS_LABELS_WORKER=/bin/x", "HOME=/Users/someone", "PHAROS_WORK_ENGINE=classic"} {
		if trialEnv(root, append(slices.Clone(env), extra)) == nil {
			t.Fatalf("admitted %s", extra)
		}
	}

	d := trialDescriptor{Name: "fixture", Kinds: []string{kindParallelism}, PendingCounts: map[string]string{kindParallelism: "SELECT count(*) FROM parallelism_dirty"},
		MaxSeconds: 600, WaitSeconds: 60, HoldSeconds: 5, SampleSeconds: 5, Cycles: 1}
	if err := d.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*trialDescriptor){
		"git reads checkouts":   func(d *trialDescriptor) { d.Kinds = append(d.Kinds, kindGit) },
		"missing pending count": func(d *trialDescriptor) { d.Kinds = append(d.Kinds, kindAuthorship) },
		"a write": func(d *trialDescriptor) {
			d.PendingCounts = map[string]string{kindParallelism: "DELETE FROM parallelism_dirty"}
		},
		"unbounded duration": func(d *trialDescriptor) { d.MaxSeconds = 86400 },
		"too many cycles":    func(d *trialDescriptor) { d.Cycles = 50 },
		"undemanded check":   func(d *trialDescriptor) { d.RecentChecks = map[string][]string{kindLabels: {"SELECT 1"}} },
	} {
		changed := d
		change(&changed)
		if changed.validate() == nil {
			t.Fatalf("admitted a descriptor with %s", name)
		}
	}

	inputs := filepath.Join(root, "inputs", "model")
	if err := os.MkdirAll(inputs, 0o700); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(inputs, "pharos-labeler-ort")
	if err := os.WriteFile(worker, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{Path: filepath.Join(root, "archive.toml"), CatalogPath: filepath.Join(root, "catalog/catalog.sqlite3"),
		ArchiveRoot: filepath.Join(root, "preserved"), CaptureRoot: filepath.Join(root, "captures"), StagingRoot: filepath.Join(root, "staging"),
		Labels: LabelerSettings{Enabled: true, ModelDir: inputs, Worker: worker}}
	kinds := []string{kindLabels}
	if err := trialConfig(root, config, kinds); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	for name, change := range map[string]func(*Config){
		"a source":         func(c *Config) { c.Sources = []SourceConfig{{Name: "claude"}} },
		"a token":          func(c *Config) { c.GitHubToken = "x" },
		"a shared catalog": func(c *Config) { c.CatalogPath = "/Volumes/euclid/Pharos/catalog.sqlite3" },
		"labels disabled":  func(c *Config) { c.Labels.Enabled = false },
		"a host model":     func(c *Config) { c.Labels.ModelDir = t.TempDir() },
		"the fake worker":  func(c *Config) { c.Labels.Worker = exe },
	} {
		changed := config
		change(&changed)
		if trialConfig(root, changed, kinds) == nil {
			t.Fatalf("admitted a configuration with %s", name)
		}
	}
}

// The trial's verdicts over inspection facts: shared batches, control
// failures, the allowance sequence and the inspection bounds.
func TestWorkTrialVerdicts(t *testing.T) {
	in := workInspection{Tasks: []workTaskFacts{
		{Kind: kindLabels, Running: &workJob{Demanders: []uint64{1, 2}}},
		{Kind: kindParallelism, Running: &workJob{Demanders: []uint64{1}}, Examples: []workFailure{{Error: "parse error"}}},
	}}
	if trialShared(in, 1, 2) != kindLabels || trialShared(in, 1, 3) != "" {
		t.Fatal("a shared batch lists every requester")
	}
	if len(trialCancellationFailures(in)) != 0 {
		t.Fatal("a domain failure is not a control failure")
	}
	in.Tasks[1].Examples = append(in.Tasks[1].Examples, workFailure{Error: "batch: " + errWorkStopped.Error()})
	if got := trialCancellationFailures(in); len(got) != 1 {
		t.Fatalf("a stopped batch remembered as a failure: %v", got)
	}

	at := func(allocated int) workAuthorityFacts { return workAuthorityFacts{CPUCap: 2, CPUAllocated: allocated} }
	if drained, violations := trialAllowance([]workAuthorityFacts{at(6), at(4), at(2), at(1)}, 2); drained != 2 || len(violations) != 0 {
		t.Fatalf("a draining allowance: %d %v", drained, violations)
	}
	if _, violations := trialAllowance([]workAuthorityFacts{at(6), at(7), at(2)}, 2); len(violations) != 1 {
		t.Fatalf("growth over budget is a violation: %v", violations)
	}
	if _, violations := trialAllowance([]workAuthorityFacts{at(2), at(3)}, 2); len(violations) != 1 {
		t.Fatalf("over the cap after draining is a violation: %v", violations)
	}
	if drained, _ := trialAllowance([]workAuthorityFacts{at(5)}, 2); drained != -1 {
		t.Fatal("an allowance that never drained")
	}
	if _, violations := trialAllowance([]workAuthorityFacts{{CPUCap: 4}}, 2); len(violations) != 1 {
		t.Fatal("the cap must be the requested allowance")
	}

	bounded := workInspection{Decisions: make([]workDecision, workDecisionLimit), Authority: workAuthorityFacts{MemoryReserved: 1, MemoryLimit: 1}}
	if got := trialBounds(bounded, 200, 200); len(got) != 0 {
		t.Fatalf("within bounds: %v", got)
	}
	bounded.Decisions = append(bounded.Decisions, workDecision{})
	bounded.Tasks = []workTaskFacts{{Kind: kindLabels, Examples: make([]workFailure, 6)}}
	bounded.Authority.MemoryReserved = 2
	if got := trialBounds(bounded, 201, 200); len(got) != 4 {
		t.Fatalf("every breach is reported: %v", got)
	}
}

// A real server reports its inspection through the trial's reader, and the
// report is private.
func TestWorkTrialReport(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	root := t.TempDir()
	tr := &trial{t: t, ctx: t.Context(), root: root, started: time.Now(), deadline: time.Now().Add(time.Minute), config: config,
		server: testServer(t, config, c), bounded: map[string]bool{}, d: trialDescriptor{SampleSeconds: 1, WaitSeconds: 10}}
	tr.report.Windows = map[string][]float64{}
	in, ok := tr.wait("inspection", time.Second, func(in workInspection) bool { return len(in.Tasks) > 0 })
	if !ok || trialTask(in, kindLabels).Kind != kindLabels || in.Authority.CPUCap < 1 {
		t.Fatalf("the scheduled server's inspection: %+v", in)
	}
	tr.sample("fixture")
	tr.check("fixture", "none", true, nil)
	tr.save()
	info, err := os.Stat(filepath.Join(root, "probe-trial-report.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report: %v %v", info, err)
	}
	var report trialReport
	raw, _ := os.ReadFile(filepath.Join(root, "probe-trial-report.json"))
	if err := json.Unmarshal(raw, &report); err != nil || len(report.Samples) != 1 || len(report.States) != 1 || len(report.Checks) != 1 {
		t.Fatalf("report %s: %v", raw, err)
	}
	if s := report.Samples[0]; s.Go["sys"] == 0 || s.InspectionBytes == 0 {
		t.Fatalf("sample %+v", s)
	}
}
