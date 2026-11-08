//go:build probe

package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Production durability, captured at package initialization before TestMain
// swaps in the disposable-test settings. A replica measures what the service
// does, including its flushes.
var (
	replicaDurability  = catalogDurability
	replicaLedgerSync  = labelingLedgerSynchronous
	replicaSyncToDrive = syncToDrive
)

// replicaStalledRounds is how many consecutive classic rounds may leave a
// queue no smaller before the stage fails instead of spinning.
const replicaStalledRounds = 3

// replicaKinds is the dependency order the classic path runs a workload's
// kinds in, mapped to the classic domain methods below.
var replicaKinds = []string{"maintenance.parallelism", "analysis.authorship", "analysis.labels", "maintenance.labels.query"}

// The classic methods drain their production queues. Restricted labels runs
// force rebuilds and omit dependent invalidation, so they are not comparable.
var replicaScopedKinds = []string{}

type replicaPhase struct {
	Name   string   `json:"name"`
	Action string   `json:"action"`
	Kinds  []string `json:"kinds"`
	From   string   `json:"from,omitempty"`
	Until  string   `json:"until,omitempty"`
}

// TestWorkReplica measures real domain work on an explicitly prepared arm. It
// opens no installed library, serves no network port and starts no service
// schedules. The same file compiles against the retained source and the
// candidate: the scheduled engine is driven only through its HTTP API, so the
// probe imports no runtime helper and a build without that API cannot
// pretend to have run it. Compile once with `go test -c -tags probe`.
//
//	PHAROS_WORK_REPLICA=/.../arm PHAROS_WORK_DESCRIPTOR=/.../workload.json \
//	PHAROS_WORK_ENGINE=classic|scheduled \
//	  archive.test -test.run '^TestWorkReplica$' -test.timeout 24h -test.v
//
// tools/work-experiment.py probe-run prepares the arm and runs this.
//
// An optional replay schedules recent arrivals from a frozen arm input during
// the work, through the service's background Capture & index item; settlement
// then includes them. Physical memory is sampled through the work and an
// optional idle period after it, so retained memory can be measured.
func TestWorkReplica(t *testing.T) {
	root := os.Getenv("PHAROS_WORK_REPLICA")
	if root == "" {
		t.Skip("set PHAROS_WORK_REPLICA to a freshly prepared experiment arm")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	// tools/work-experiment.py refuses every registered library before it
	// makes an arm (this process's HOME is the arm's, so the registry is not
	// visible here); the marker below is the arm's evidence. This is a backstop.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || root != resolved || root == "/Volumes/euclid/Pharos" || strings.HasPrefix(root, "/Volumes/euclid/Pharos/") {
		t.Fatalf("unsafe replica directory: %v", err)
	}
	var marker struct {
		Version int    `json:"version"`
		Role    string `json:"role"`
	}
	raw, err := os.ReadFile(filepath.Join(root, ".pharos-work-experiment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Version != 3 || marker.Role != "arm" {
		t.Fatal("not a version-3 experiment arm; older artifacts are diagnostic only")
	}
	var descriptor struct {
		CPUSlots int            `json:"cpu_slots"`
		Name     string         `json:"name"`
		Clock    string         `json:"clock"`
		Stages   []string       `json:"stages"`
		Phases   []replicaPhase `json:"phases"`
		Product  struct {
			Route       string          `json:"route"`
			Query       json.RawMessage `json:"query"`
			LatencyMS   float64         `json:"latency_ms"`
			Consecutive int             `json:"consecutive"`
			Expected    string          `json:"expected_sha256"`
			MinRows     *int            `json:"min_expected_rows"`
		} `json:"product"`
		// Read-only queries per kind or stage whose result must be empty once
		// its required work has settled.
		Pending map[string][]string `json:"pending_checks"`
		// Recent arrivals replayed during the work window (optional).
		Replay *replicaReplay `json:"replay"`
		// Physical-memory sampling and the post-settlement idle (optional).
		Memory *replicaMemoryPlan `json:"memory"`
	}
	raw, err = os.ReadFile(os.Getenv("PHAROS_WORK_DESCRIPTOR"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		t.Fatal(err)
	}
	clock, err := time.Parse(time.RFC3339, descriptor.Clock)
	if err != nil {
		t.Fatal("workload must pin its calendar clock")
	}
	if (len(descriptor.Stages) == 0) == (len(descriptor.Phases) == 0) || descriptor.Product.LatencyMS <= 0 || descriptor.Product.Consecutive < 1 {
		t.Fatal("pin either phases or legacy stages, product latency and observation window before running")
	}
	if descriptor.Product.Route != "/api/query/message_labels/query-v3" {
		t.Fatal("first product gate is pinned to the real message-label query route")
	}
	// An empty table can be a correct answer, so the descriptor says how many
	// rows the pinned surface has; the busy recent 48 hours pins at least one.
	if descriptor.Product.MinRows == nil || *descriptor.Product.MinRows < 0 {
		t.Fatal("pin product.min_expected_rows")
	}
	units := slices.Clone(descriptor.Stages)
	windows := make([][2]time.Time, len(descriptor.Phases))
	for i, phase := range descriptor.Phases {
		if phase.Action != "recent" && phase.Action != "backfill" || len(phase.Kinds) == 0 || (phase.From == "") != (phase.Until == "") ||
			(phase.From != "" && phase.Action != "backfill") {
			t.Fatalf("phase %q: action recent, or backfill with an optional from/until window", phase.Name)
		}
		for _, kind := range phase.Kinds {
			if !slices.Contains(replicaKinds, kind) {
				t.Fatalf("phase %q: unsupported kind %q", phase.Name, kind)
			}
		}
		switch {
		case phase.Action == "recent":
			// The service's recent request: the 48 hours before its clock.
			windows[i] = [2]time.Time{clock.Add(-48 * time.Hour), clock}
		case phase.From != "":
			from, errFrom := time.Parse(time.RFC3339, phase.From)
			until, errUntil := time.Parse(time.RFC3339, phase.Until)
			if errFrom != nil || errUntil != nil || !from.Before(until) {
				t.Fatalf("phase %q: malformed window", phase.Name)
			}
			windows[i] = [2]time.Time{from, until}
		}
		units = append(units, phase.Kinds...)
	}
	if _, err := replicaKindPopulations(descriptor.Phases, windows, nil); err != nil {
		t.Fatal(err)
	}
	for _, unit := range units {
		if len(descriptor.Pending[unit]) == 0 {
			t.Fatalf("%q needs pending_checks: returning is not settlement", unit)
		}
		for _, query := range descriptor.Pending[unit] {
			if !readOnlyCheck(query) {
				t.Fatalf("%q: a pending check is one SELECT or WITH statement", unit)
			}
		}
	}
	requested := defaultString(os.Getenv("PHAROS_WORK_ENGINE"), "classic")
	if requested == "scheduled" && len(descriptor.Stages) > 0 {
		t.Fatal("legacy classic stages cannot run on the scheduled engine")
	}
	replay := descriptor.Replay
	if replay != nil {
		if len(descriptor.Stages) > 0 {
			t.Fatal("replay needs phases: legacy stages have no follow-up demand")
		}
		if err := replay.check(); err != nil {
			t.Fatal(err)
		}
		if err := replicaReplayClock(clock); err != nil {
			t.Fatal(err)
		}
		// Every primary and arrival request must still be inspectable when
		// the probe awaits it, within the work API's ended-request summaries.
		if requested == "scheduled" && len(descriptor.Phases)+len(replay.Arrivals) > replicaRequestLimit {
			t.Fatalf("a scheduled replay has at most %d phases and arrivals together", replicaRequestLimit)
		}
	}
	memoryPlan := replicaMemoryPlan{IntervalSeconds: 5}
	if descriptor.Memory != nil {
		memoryPlan = *descriptor.Memory
		if memoryPlan.IntervalSeconds == 0 {
			memoryPlan.IntervalSeconds = 5
		}
		if memoryPlan.IntervalSeconds < 1 || memoryPlan.IdleSeconds < 0 {
			t.Fatal("memory: interval_seconds is at least 1 and idle_seconds is not negative")
		}
	}
	memory := &replicaMemory{}
	// Restore production durability before the catalog opens.
	savedDurability, savedLedgerSync, savedSyncToDrive := catalogDurability, labelingLedgerSynchronous, syncToDrive
	catalogDurability, labelingLedgerSynchronous, syncToDrive = replicaDurability, replicaLedgerSync, replicaSyncToDrive
	defer func() {
		catalogDurability, labelingLedgerSynchronous, syncToDrive = savedDurability, savedLedgerSync, savedSyncToDrive
	}()
	config, err := loadConfig(filepath.Join(root, "archive.toml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(config.CatalogPath) != filepath.Join(root, "catalog/catalog.sqlite3") || len(config.Sources) > 0 {
		t.Fatal("domain probe requires a private catalog and no external source declarations")
	}
	if slices.Contains(units, "analysis.labels") && !config.Labels.Enabled {
		t.Fatal("a labels workload pins labels_enabled and its frozen model inputs")
	}
	// The replay source exists only in this process's server configuration,
	// over an arm-private folder the probe writes from the frozen input; the
	// arm's own configuration still declares no source.
	serverConfig := config
	var replayInput, replayDir string
	if replay != nil {
		replayInput = filepath.Join(root, "inputs", replay.Input)
		if info, err := os.Lstat(replayInput); err != nil || !info.IsDir() {
			t.Fatalf("replay input %q is not staged in this arm: %v", replay.Input, err)
		}
		replayDir = filepath.Join(root, "replay", replay.Source.Name)
		if err := os.MkdirAll(replayDir, 0o700); err != nil {
			t.Fatal(err)
		}
		source := SourceConfig{Name: replay.Source.Name, Kind: replay.Source.Kind, Path: replayDir, Account: replay.Source.Account,
			Enabled: true, Options: replay.Source.Options, File: config.Path}
		if _, err := MakeAdapter(source); err != nil {
			t.Fatalf("replay source: %v", err)
		}
		serverConfig.Sources = []SourceConfig{source}
	}
	memory.sample("process")
	// Opening initializes/migrates the catalog: startup is measured apart
	// from the work it precedes, so a fixed cost cannot dilute a stage.
	opening := replicaCPUNow()
	c, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.evidenceOnly = true
	// Arrival demand advances clockOffset by nanoseconds (see the demand
	// hook); with no replay it stays 0 and the clock stays pinned.
	var clockOffset atomic.Int64
	c.now = func() time.Time { return clock.Add(time.Duration(clockOffset.Load())) }
	server := testServer(t, serverConfig, c)
	replayHost := currentHost().ID
	if replay != nil {
		// A fresh source: every replayed record is an arrival in this run.
		if _, err := os.Lstat(filepath.Join(config.CaptureRoot, replayHost, replay.Source.Name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the arm already has a %s capture of replay source %q", replayHost, replay.Source.Name)
		}
	}
	checks, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: config.CatalogPath}).String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer checks.Close()
	startup := opening.until(replicaCPUNow())
	memory.sample("startup")

	// The executed engine is what this process's work API reports, never the
	// request. A build without the API answers 404 and runs classic.
	status, workState, _ := probeJSON(server, config.APIToken, http.MethodGet, "/api/work", nil)
	executed := "classic"
	if status == http.StatusOK && workState["engine"] == "scheduled" {
		executed = "scheduled"
	}
	if executed != requested {
		t.Fatalf("requested the %s engine but this process configured %s", requested, executed)
	}

	// The population each phase demands, frozen from the same snapshot in
	// both arms. Each kind runs the union of the phases that demand it, so the
	// classic arm never does another phase's work.
	population := map[string]any{}
	phaseIDs := make([][]string, len(descriptor.Phases))
	phases := []map[string]any{}
	for i, phase := range descriptor.Phases {
		ids, err := replicaPopulation(t.Context(), checks, windows[i])
		if err != nil {
			t.Fatal(err)
		}
		phaseIDs[i] = ids
		entry := map[string]any{"phase": phase.Name, "action": phase.Action, "conversations": len(ids), "sha256": replicaIDsDigest(ids)}
		if !windows[i][0].IsZero() {
			entry["from"], entry["until"] = formatTime(windows[i][0]), formatTime(windows[i][1])
		}
		phases = append(phases, entry)
	}
	kindPopulation, err := replicaKindPopulations(descriptor.Phases, windows, phaseIDs)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []map[string]any{}
	for _, kind := range replicaKinds {
		if selected, ok := kindPopulation[kind]; ok {
			kinds = append(kinds, map[string]any{"kind": kind, "whole": selected.whole, "conversations": len(selected.ids), "sha256": replicaIDsDigest(selected.ids)})
		}
	}
	population["phases"], population["kinds"] = phases, kinds
	work := replicaCPUNow()
	started := work.at
	memory.begin(started)
	memory.sample("work-start")

	type observation struct {
		Seconds   float64 `json:"seconds"`
		LatencyMS float64 `json:"latency_ms"`
		Status    int     `json:"status"`
		Rows      int     `json:"rows"`
		Digest    string  `json:"sha256,omitempty"`
		Invalid   string  `json:"invalid,omitempty"`
	}
	var mu sync.Mutex
	observations := []observation{}
	// Runs on the polling goroutine too, so it reports errors, never t.Fatal.
	observe := func() {
		at := time.Now()
		o := observation{}
		status, rows, err := probeProductRows(server, config.APIToken, descriptor.Product.Route, descriptor.Product.Query)
		o.Seconds, o.LatencyMS, o.Status = time.Since(started).Seconds(), float64(time.Since(at).Microseconds())/1000, status
		if err == nil {
			// The oracle is the UI page's actual rows, not the projection queue
			// and not duration/diagnostic metadata. No values leave this process.
			var data []byte
			data, err = json.Marshal(rows)
			hash := sha256.Sum256(data)
			o.Rows, o.Digest = len(rows), hex.EncodeToString(hash[:])
		}
		if err != nil {
			o.Invalid = err.Error()
		}
		mu.Lock()
		observations = append(observations, o)
		mu.Unlock()
	}
	observe()
	pollCtx, stopPoll := context.WithCancel(t.Context())
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				observe()
			}
		}
	}()
	// Memory samples, arrivals and their readiness run beside the work until
	// it settles, on the work window's clock.
	background := sync.WaitGroup{}
	background.Go(func() {
		ticker := time.NewTicker(time.Duration(memoryPlan.IntervalSeconds * float64(time.Second)))
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				memory.sample("interval")
			}
		}
	})
	// The work's context: a failed arrival cancels it at once with its own
	// error as the cause, so the history work stops instead of finishing
	// first, and the report names the arrival's error.
	ctx, cancelWork := context.WithCancelCause(t.Context())
	defer cancelWork(nil)
	var arrivals *replicaArrivals
	arrivalsDone := make(chan error, 1)
	demanded := []string{}
	for _, kind := range replicaKinds {
		if slices.Contains(units, kind) {
			demanded = append(demanded, kind)
		}
	}
	if replay != nil {
		arrivals = newReplicaArrivals(replay, started, cancelWork)
		// The scheduled engine hears of each arrival as new recent demand,
		// sent once the arrival is indexed and its evidence recorded. This
		// simulates explicit recent demand on arrival, not the service's
		// full daemon cadence; classic continues its whole pass instead.
		if executed != "classic" {
			arrivals.demandKinds = demanded
			arrivals.demand = func(_ context.Context, index int) (uint64, error) {
				// A fresh arrival needs a fresh finite request, not a join of
				// an earlier live one with the same key. Advancing the
				// request clock by index+1 ns gives each arrival its own key;
				// replicaReplayClock guarantees that the millisecond
				// formatTime population bounds do not move, and the product
				// route's bounds are day-granular.
				clockOffset.Store(int64(index + 1))
				return replicaDemandRecent(server, config.APIToken, demanded)
			}
		}
		background.Go(func() {
			arrivalsDone <- arrivals.run(pollCtx, func(index int) (string, error) {
				memory.sample("arrival:" + replay.Arrivals[index].Name)
				return replicaApplyArrival(pollCtx, server, replay, replayInput, replayDir, index, clock)
			}, func(ctx context.Context) (int64, error) {
				var count int64
				err := checks.QueryRowContext(ctx, replay.CountQuery).Scan(&count)
				return count, err
			}, func(ctx context.Context) (bool, error) {
				ready, err := checksEmpty(ctx, checks, replay.ReadyChecks)
				return !ready, err
			}, func(ctx context.Context) (replicaCensus, error) {
				return replay.identityCensus(ctx, checks)
			})
		})
		background.Go(func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-ticker.C:
					if !arrivals.waiting() {
						continue
					}
					checked := time.Now()
					if ready, err := checksEmpty(pollCtx, checks, replay.ReadyChecks); err == nil && ready {
						arrivals.ready(checked, time.Now())
					}
				}
			}
		})
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			stopPoll()
			<-pollDone
			background.Wait()
		}
	}
	defer stop()

	engine := map[string]any{"value": executed, "requested": requested}
	if executed == "classic" {
		engine["basis"] = "the work API did not report a scheduled service; the probe invoked classic domain methods"
	} else {
		engine["basis"] = "work-api"
	}
	labels := config.Labels
	report := map[string]any{"version": 3, "name": descriptor.Name, "clock": descriptor.Clock, "engine_actual": engine,
		"startup": startup, "settled": false, "resource_basis": replicaResourceBasis, "population": population,
		"cpu_policy":      replicaCPUPolicy(workState, descriptor.CPUSlots),
		"expected_sha256": descriptor.Product.Expected, "min_expected_rows": *descriptor.Product.MinRows,
		"durability": "production catalog durability, labeling ledger synchronous=" + replicaLedgerSync + " and drive flushes restored",
		"labels": map[string]any{"enabled": labels.Enabled, "variant": labels.Variant, "backend": labels.Backend,
			"threads": labels.Threads, "workers": labels.Workers, "model": filepath.Base(labels.ModelDir)}}
	save := func() {
		mu.Lock()
		report["observations"] = append([]observation(nil), observations...)
		mu.Unlock()
		if arrivals != nil {
			report["replay"] = arrivals.report(replayHost)
		}
		samples := memory.snapshot()
		report["memory"] = map[string]any{"samples": samples, "retention": replicaRetention(samples, memoryPlan)}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(root, "probe-report.json"), append(data, '\n'), 0o600)
		}
		if err != nil {
			t.Error(err)
		}
	}
	// A failed stage still leaves its private report, unsettled. When a
	// failed arrival stopped the work, its error is the failure and what it
	// interrupted is kept beside it.
	fail := func(format string, args ...any) {
		stop()
		failure, interrupted := replicaFailure(ctx, fmt.Sprintf(format, args...))
		report["failure"] = failure
		if interrupted != "" {
			report["interrupted"] = interrupted
		}
		save()
		t.Fatal(failure)
	}
	settle := func(ctx context.Context, unit string) {
		// Independent of what the method returned: its required work is done.
		for index, query := range descriptor.Pending[unit] {
			if pending, err := checkPending(ctx, checks, query); err != nil || pending {
				fail("%s: pending check %d has not settled (%v)", unit, index, err)
			}
		}
	}
	measured := func(name string, run func() error) map[string]any {
		at := replicaCPUNow()
		if err := run(); err != nil {
			fail("%s: %v", name, err)
		}
		step := at.until(replicaCPUNow())
		step["name"], step["started_seconds"] = name, at.at.Sub(started).Seconds()
		memory.sample("step:" + name)
		return step
	}
	steps := []map[string]any{}
	report["steps"] = &steps
	// submit posts one phase's demand to the work API and returns its id.
	submit := func(phase replicaPhase) uint64 {
		body := map[string]any{"action": phase.Action, "kinds": phase.Kinds}
		if phase.From != "" {
			body["from"], body["until"] = phase.From, phase.Until
		}
		status, value, err := probeJSON(server, config.APIToken, http.MethodPost, "/api/work", body)
		if err != nil || status != http.StatusAccepted {
			fail("phase %s: work request refused (%d %v)", phase.Name, status, err)
		}
		// A build that ignored the window would silently demand everything.
		if phase.From != "" && (value["from"] != phase.From || value["until"] != phase.Until) {
			fail("phase %s: this build does not scope backfill windows", phase.Name)
		}
		id, ok := value["request_id"].(json.Number)
		parsed, err := id.Int64()
		if !ok || err != nil || parsed <= 0 {
			fail("phase %s: no request id", phase.Name)
		}
		return uint64(parsed)
	}
	// await polls the work API until every request in ids has ended and
	// returns their ended facts. Every poll also watches the arrival
	// requests sent so far, so each one's timings come from the polls
	// around its own end, and one that ends other than succeeded fails the
	// run at once, even while the primary history is still running. What
	// the polls saw is kept across awaits in watch.
	watch := replicaRequestWatch{seenLive: map[uint64]float64{}, terminal: map[uint64]map[string]any{}}
	await := func(ids []uint64, phase func(int) string) map[uint64]map[string]any {
		for {
			tracked := map[uint64]string{}
			if arrivals != nil {
				arrivalIDs, firsts, _ := arrivals.requestIDs()
				for i, id := range arrivalIDs {
					tracked[id] = "arrival:" + firsts[i]
				}
			}
			for i, id := range ids {
				tracked[id] = phase(i)
			}
			status, value, err := probeJSON(server, config.APIToken, http.MethodGet, "/api/work", nil)
			if err != nil || status != http.StatusOK {
				fail("work state: %d %v", status, err)
			}
			state, _ := value["state"].(map[string]any)
			for _, id := range watch.observe(state, tracked, time.Since(started).Seconds()) {
				if facts := watch.terminal[id]; strings.HasPrefix(tracked[id], "arrival:") && facts["state"] != "succeeded" {
					fail("arrival request %v ended %v: %v", facts["id"], facts["state"], facts["reason"])
				}
			}
			if !slices.ContainsFunc(ids, func(id uint64) bool { return watch.terminal[id] == nil }) {
				ended := map[uint64]map[string]any{}
				for _, id := range ids {
					ended[id] = watch.terminal[id]
				}
				return ended
			}
			select {
			case <-ctx.Done():
				fail("work requests did not end: %v", ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	// primaryPhases names the primary phases by request id, so an arrival
	// request that joined one is reported as such.
	primaryPhases := map[uint64][]string{}
	switch {
	case len(descriptor.Stages) > 0:
		for _, stage := range descriptor.Stages {
			steps = append(steps, measured(stage, func() error { return replicaStage(ctx, c, config, clock, stage) }))
			settle(ctx, stage)
		}
	case executed == "classic":
		for _, kind := range replicaKinds {
			if !slices.Contains(units, kind) {
				continue
			}
			steps = append(steps, measured(kind, func() error { return replicaClassicKind(ctx, c, config, clock, kind, kindPopulation[kind]) }))
			// An arrival can dirty a kind that already ran; with a replay,
			// every kind is checked once the arrivals have settled.
			if replay == nil {
				settle(ctx, kind)
			}
		}
	default:
		// Submit every phase as the product would, then wait for all of
		// them: recent demand and the historical remainder share the engine.
		ids := make([]uint64, len(descriptor.Phases))
		for i, phase := range descriptor.Phases {
			ids[i] = submit(phase)
			primaryPhases[ids[i]] = append(primaryPhases[ids[i]], phase.Name)
		}
		ended := await(ids, func(i int) string { return descriptor.Phases[i].Name })
		requests := []any{}
		for _, id := range ids {
			requests = append(requests, ended[id])
		}
		engine["requests"] = requests
		for _, facts := range ended {
			if facts["state"] != "succeeded" {
				fail("work request %v ended %v: %v", facts["id"], facts["state"], facts["reason"])
			}
		}
		steps = append(steps, map[string]any{"name": "scheduled", "wall_seconds": time.Since(started).Seconds()})
		if replay == nil {
			for _, kind := range replicaKinds {
				if slices.Contains(units, kind) {
					settle(ctx, kind)
				}
			}
		}
	}
	if replay != nil {
		// Settlement includes every arrival: all applied, their ready checks
		// and every kind's pending checks empty. Work the engine has not
		// picked up gets its next pass: classic re-drains the demanded kinds,
		// scheduled receives recent demand, as the service would send.
		select {
		case err := <-arrivalsDone:
			if err != nil {
				fail("replay: %v", err)
			}
		case <-ctx.Done():
			fail("replay arrivals did not finish: %v", ctx.Err())
		}
		// Every arrival's own recent demand must also have ended, succeeded.
		if arrivals.demand != nil {
			ids, firsts, total := arrivals.requestIDs()
			if total != len(replay.Arrivals) {
				fail("replay: %d of %d arrivals sent recent demand", total, len(replay.Arrivals))
			}
			ended := await(ids, func(i int) string { return "arrival:" + firsts[i] })
			arrivals.requestsEnded(ended, primaryPhases)
			for _, id := range ids {
				if facts := ended[id]; facts["state"] != "succeeded" {
					fail("arrival request %v ended %v: %v", facts["id"], facts["state"], facts["reason"])
				}
			}
		}
		deadline := time.Now().Add(replay.settleTimeout())
		rounds, followups := 0, []any{}
		for {
			checked := time.Now()
			ready, err := checksEmpty(ctx, checks, replay.ReadyChecks)
			for _, kind := range demanded {
				if err == nil && ready {
					ready, err = checksEmpty(ctx, checks, descriptor.Pending[kind])
				}
			}
			if err != nil {
				fail("replay: settlement check: %v", err)
			}
			if ready {
				arrivals.ready(checked, time.Now())
				break
			}
			switch {
			case rounds < replicaFollowupRounds:
				rounds++
				if executed == "classic" {
					for _, kind := range demanded {
						steps = append(steps, measured("followup:"+kind, func() error { return replicaClassicKind(ctx, c, config, clock, kind, kindPopulation[kind]) }))
					}
					continue
				}
				at := time.Now()
				id := submit(replicaPhase{Name: fmt.Sprintf("followup-%d", rounds), Action: "recent", Kinds: demanded})
				facts := await([]uint64{id}, func(int) string { return fmt.Sprintf("followup-%d", rounds) })[id]
				if facts["state"] != "succeeded" {
					fail("follow-up request %v ended %v: %v", facts["id"], facts["state"], facts["reason"])
				}
				followups = append(followups, facts)
				steps = append(steps, map[string]any{"name": "followup:scheduled", "started_seconds": at.Sub(started).Seconds(), "wall_seconds": time.Since(at).Seconds()})
			case time.Now().After(deadline):
				fail("replay: arrivals did not settle within %s of the work ending, after %d follow-up rounds", replay.settleTimeout(), rounds)
			default:
				select {
				case <-ctx.Done():
					fail("replay did not settle: %v", ctx.Err())
				case <-time.After(time.Second):
				}
			}
		}
		arrivals.settled(rounds, followups, time.Since(started).Seconds())
		for _, kind := range demanded {
			settle(ctx, kind)
		}
	}
	// The scheduled engine's tasks, authority and resource ledger, read once
	// every primary, arrival and follow-up request has ended, so they cover
	// all of the run's work.
	if executed != "classic" && len(descriptor.Stages) == 0 {
		_, value, _ := probeJSON(server, config.APIToken, http.MethodGet, "/api/work", nil)
		state, _ := value["state"].(map[string]any)
		engine["tasks"], engine["authority"] = state["tasks"], state["authority"]
		engine["decisions_total"] = state["decisions_total"]
		if _, ledger, err := probeJSON(server, config.APIToken, http.MethodGet, "/api/operations/resources", nil); err == nil {
			report["task_resources"] = ledger
		}
	}
	execution := work.until(replicaCPUNow())
	memory.sample("settled")
	settled := execution["wall_seconds"].(float64)
	stop()
	// Ensure a short run still has the pinned number of product observations.
	// These post-settlement queries are outside execution resource/wall totals.
	for range descriptor.Product.Consecutive {
		observe()
	}
	mu.Lock()
	snapshots := append([]observation(nil), observations...)
	mu.Unlock()
	var correct, usable *float64
	consecutive := 0
	for _, o := range snapshots {
		valid := o.Status == http.StatusOK && o.Invalid == "" && o.Rows >= *descriptor.Product.MinRows &&
			descriptor.Product.Expected != "" && o.Digest == descriptor.Product.Expected
		if valid && correct == nil {
			value := o.Seconds
			correct = &value
		}
		if valid && o.LatencyMS <= descriptor.Product.LatencyMS {
			consecutive++
		} else {
			consecutive = 0
		}
		if usable == nil && consecutive >= descriptor.Product.Consecutive {
			value := o.Seconds
			usable = &value
		}
	}
	last := snapshots[len(snapshots)-1]
	if last.Invalid != "" || last.Rows < *descriptor.Product.MinRows {
		fail("the settled product query is invalid or has fewer than the pinned rows: %s", last.Invalid)
	}
	if descriptor.Product.Expected != "" && last.Digest != descriptor.Product.Expected {
		fail("the settled product rows differ from the pinned oracle")
	}
	// The windows let the harness scope its process-tree memory, disk and
	// host-noise samples to startup, execution and the wait for readiness.
	spans := map[string]any{"startup": []float64{replicaUnix(opening.at), startup["until_unix"].(float64)},
		"work": []float64{replicaUnix(started), execution["until_unix"].(float64)}}
	for _, ready := range []*float64{correct, usable} {
		if ready != nil {
			// The later milestone ends the wait for readiness.
			spans["readiness"] = []float64{replicaUnix(started), replicaUnix(started) + *ready}
		}
	}
	// Idle after settlement: what the process keeps (model workers,
	// preparation contexts, caches) once nothing is demanded. The last
	// sample follows a collection that returns free memory to the system,
	// so live retained memory is told apart from collectable garbage.
	idleFrom := time.Now()
	if memoryPlan.IdleSeconds > 0 {
		idleEnd := idleFrom.Add(time.Duration(memoryPlan.IdleSeconds * float64(time.Second)))
		for next := idleFrom; ; {
			next = next.Add(time.Duration(memoryPlan.IntervalSeconds * float64(time.Second)))
			if next.After(idleEnd) {
				next = idleEnd
			}
			select {
			case <-ctx.Done():
				fail("idle period: %v", ctx.Err())
			case <-time.After(time.Until(next)):
			}
			if next.Equal(idleEnd) {
				break
			}
			memory.sample("idle-interval")
		}
		spans["idle"] = []float64{replicaUnix(idleFrom), replicaUnix(time.Now())}
	}
	memory.sample("idle")
	runtime.GC()
	debug.FreeOSMemory()
	memory.sample("idle-released")
	results := map[string]any{"t_correct": correct, "t_usable": usable, "t_settled": settled, "settled": true,
		"work": execution, "windows": spans, "final_product_sha256": last.Digest, "final_product_rows": last.Rows}
	for key, value := range results {
		report[key] = value
	}
	save()
	fmt.Printf("work replica %s (%s): settled=%.3fs cpu=%.3fs observations=%d; private report saved\n", descriptor.Name, executed, settled, execution["cpu_seconds"], len(snapshots))
}

// replicaKindPopulation is the conversations one kind runs over: the
// whole corpus, or the union of its phases' windows.
type replicaKindPopulation struct {
	whole bool
	ids   []string
}

// replicaKindPopulations is what each demanded kind runs over in either
// arm: the union of the phases that demand it, or the whole corpus when one
// of them has no window. A kind whose classic pass cannot be scoped must be
// demanded whole. phaseIDs (nil to only check) are each phase's conversations.
func replicaKindPopulations(phases []replicaPhase, windows [][2]time.Time, phaseIDs [][]string) (map[string]replicaKindPopulation, error) {
	populations := map[string]replicaKindPopulation{}
	for _, kind := range replicaKinds {
		var selected replicaKindPopulation
		demanded := false
		for i, phase := range phases {
			if slices.Contains(phase.Kinds, kind) {
				demanded = true
				selected.whole = selected.whole || windows[i][0].IsZero()
				if phaseIDs != nil {
					selected.ids = append(selected.ids, phaseIDs[i]...)
				}
			}
		}
		if !demanded {
			continue
		}
		if !selected.whole && !slices.Contains(replicaScopedKinds, kind) {
			return nil, fmt.Errorf("%s: its classic pass drains the whole queue, so a phase without a window must demand it", kind)
		}
		slices.Sort(selected.ids)
		populations[kind] = replicaKindPopulation{whole: selected.whole, ids: slices.Compact(selected.ids)}
	}
	return populations, nil
}

// replicaClassicKind runs the production classic methods until their whole
// queues are empty; the descriptor must demand each kind over that scope.
func replicaClassicKind(ctx context.Context, c *Catalog, config Config, clock time.Time, kind string, population replicaKindPopulation) error {
	switch kind {
	case "maintenance.parallelism":
		return replicaStage(ctx, c, config, clock, "timing")
	case "analysis.authorship":
		return replicaStage(ctx, c, config, clock, "authorship-estimates")
	case "analysis.labels":
		return replicaStage(ctx, c, config, clock, "labels")
	case "maintenance.labels.query":
		return replicaStage(ctx, c, config, clock, "label-facts")
	}
	return fmt.Errorf("unclassified kind %q", kind)
}

// replicaStage is one legacy classic stage (diagnostic workloads only).
func replicaStage(ctx context.Context, c *Catalog, config Config, clock time.Time, stage string) error {
	switch stage {
	case "timing":
		for {
			n, err := c.refreshParallelism(ctx, parallelismBatch, true)
			if err != nil || n == 0 {
				return err
			}
		}
	case "tools":
		return c.ensureToolRollup(ctx)
	case "identities":
		_, err := c.ReconcileIdentitiesContext(ctx)
		return err
	case "authorship-estimates":
		// A read error or a queue that stops shrinking fails the stage rather
		// than spinning until the probe's timeout.
		remaining, stalled := -1, 0
		for {
			if err := c.UpdateAuthorship(ctx, formatTime(clock)); err != nil {
				return err
			}
			var pending int
			if err := c.reader().QueryRowContext(ctx, `SELECT count(*) FROM authorship_pending`).Scan(&pending); err != nil || pending == 0 {
				return err
			}
			if remaining >= 0 && pending >= remaining {
				if stalled++; stalled >= replicaStalledRounds {
					return fmt.Errorf("writing estimates made no progress in %d rounds: %d messages still pending", stalled, pending)
				}
			} else {
				stalled = 0
			}
			remaining = pending
		}
	case "authorship-exact":
		return c.refineAuthorship(ctx, false)
	case "labels":
		stats, err := c.RunLabels(ctx, config.Labels, LabelsRunOptions{})
		if err == nil && !stats.Complete {
			err = errors.New("labels returned before full settlement")
		}
		return err
	case "agent-events":
		_, err := c.RunAgentEvents(ctx, config.Labels, config.AgentEvents, AgentEventsRunOptions{})
		return err
	case "label-facts":
		for {
			n, err := c.refreshMessageLabelFacts(ctx)
			if err != nil || n == 0 {
				return err
			}
		}
	case "findings":
		return c.RefreshFindings(ctx, true)
	}
	return fmt.Errorf("unclassified experiment stage %q", stage)
}

// replicaResourceBasis says what the probe's own CPU figures cover.
const replicaResourceBasis = "Each window's CPU is the user+system time of this probe process and every live descendant (model workers) between its boundaries, read from the kernel with each process's reaped children; cpu_complete is false where another process cannot be read. Startup is catalog open; work runs from the frozen population to settlement and includes the 1 s product poll both arms run; the post-settlement product observations are in neither. Memory, disk and host noise come from the harness's process-tree samples within the same windows."

// replicaCPU is a CPU reading at one window boundary: each live process in
// this process's tree, by pid and start time, with its own CPU plus that of
// the children it reaped.
type replicaCPU struct {
	at       time.Time
	usage    map[string]float64
	complete bool
}

func replicaCPUNow() replicaCPU {
	reading := replicaCPU{at: time.Now(), usage: map[string]float64{}, complete: true}
	read := func(pid int) {
		usage, err := processUsageOf(pid)
		if err != nil || !usage.cpuKnown(false) || usage.Started == 0 {
			reading.complete = false
			return
		}
		reading.usage[fmt.Sprintf("%d:%d", pid, usage.Started)] = usage.CPUSeconds + usage.ChildCPUSeconds
	}
	self := os.Getpid()
	read(self)
	nodes := resourceProcessTable()
	if nodes == nil {
		// Off macOS no other process can be read, so live children are unknown.
		reading.complete = false
		return reading
	}
	children := map[int][]int{}
	for _, node := range nodes {
		children[node.Parent] = append(children[node.Parent], node.PID)
	}
	for frontier := []int{self}; len(frontier) > 0; frontier = frontier[1:] {
		for _, child := range children[frontier[0]] {
			read(child)
			frontier = append(frontier, child)
		}
	}
	return reading
}

// until is the window from start to end. The tree's total at the end less
// its total at the start is the CPU used inside the window: a process alive
// at both counts its difference, one born inside counts all of its own, and
// one reaped inside moved its whole total into its parent, which was alive
// at the end, so its start reading is subtracted.
func (start replicaCPU) until(end replicaCPU) map[string]any {
	total := func(reading replicaCPU) (sum float64) {
		for _, cpu := range reading.usage {
			sum += cpu
		}
		return sum
	}
	return map[string]any{"wall_seconds": end.at.Sub(start.at).Seconds(), "cpu_seconds": total(end) - total(start),
		"cpu_complete": start.complete && end.complete, "until_unix": replicaUnix(end.at)}
}

func replicaUnix(at time.Time) float64 { return float64(at.UnixNano()) / 1e9 }

func replicaIDsDigest(ids []string) string {
	data, _ := json.Marshal(ids)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// replicaPopulation is the service's activity population: conversations
// with a message created in [from, until). A zero window is every
// conversation. Sorted IDs, so both arms hash the same list.
func replicaPopulation(ctx context.Context, db *sql.DB, window [2]time.Time) ([]string, error) {
	query, args := `SELECT id FROM conversations ORDER BY id`, []any{}
	if !window[0].IsZero() {
		query = `SELECT c.id FROM conversations c WHERE EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=c.id AND m.created_at>=? AND m.created_at<?) ORDER BY c.id`
		args = []any{formatTime(window[0]), formatTime(window[1])}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// probeJSON sends one in-process API request and decodes a JSON object.
func probeJSON(server *Server, token, method, route string, body any) (int, map[string]any, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return 0, nil, err
		}
	}
	request := httptest.NewRequest(method, route, bytes.NewReader(payload))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	value := map[string]any{}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return response.Code, nil, fmt.Errorf("undecodable %s %s: %w", method, route, err)
	}
	return response.Code, value, nil
}

// probeProductRows posts one product query and decodes its rows. Anything
// but a 200 version-3 result with a rows array of objects is an error.
func probeProductRows(server *Server, token, route string, query json.RawMessage) (int, []map[string]any, error) {
	request := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(query))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		return response.Code, nil, fmt.Errorf("status %d", response.Code)
	}
	var body struct {
		Version int               `json:"version"`
		Rows    *[]map[string]any `json:"rows"`
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return response.Code, nil, fmt.Errorf("undecodable result: %w", err)
	}
	if body.Version != 3 || body.Rows == nil {
		return response.Code, nil, errors.New("not a version-3 result with rows")
	}
	for _, row := range *body.Rows {
		if row == nil {
			return response.Code, nil, errors.New("a null row")
		}
	}
	return response.Code, *body.Rows, nil
}

// readOnlyCheck admits one SELECT or WITH statement. The connection is also
// read-only; refusing a second statement keeps ATTACH out of the probe.
func readOnlyCheck(query string) bool {
	query = strings.TrimSuffix(strings.TrimSpace(query), ";")
	words := strings.Fields(query)
	return len(words) > 0 && (strings.EqualFold(words[0], "SELECT") || strings.EqualFold(words[0], "WITH")) && !strings.Contains(query, ";")
}

func checkPending(ctx context.Context, db *sql.DB, query string) (bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	pending := rows.Next()
	return pending, rows.Err()
}

// Each kind runs over the phases that demand it, never over another phase's
// population; a kind the classic path cannot scope is refused unless whole.
func TestWorkReplicaKindPopulations(t *testing.T) {
	recent := [2]time.Time{time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	phases := []replicaPhase{{Name: "recent", Action: "recent", Kinds: []string{"analysis.labels", "maintenance.parallelism"}},
		{Name: "history", Action: "backfill", Kinds: []string{"maintenance.parallelism", "analysis.labels"}}}
	windows := [][2]time.Time{recent, {}}
	got, err := replicaKindPopulations(phases, windows, [][]string{{"b", "a"}, {"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if labels := got["analysis.labels"]; !labels.whole || !slices.Equal(labels.ids, []string{"a", "b", "c"}) {
		t.Fatalf("labels must include the whole production queue: %+v", labels)
	}
	if !got["maintenance.parallelism"].whole {
		t.Fatal("a kind demanded by an unwindowed phase runs over the whole corpus")
	}
	if _, ok := got["analysis.authorship"]; ok {
		t.Fatal("an undemanded kind has no population")
	}
	// The reviewed case: authorship demanded only in the recent window would
	// drain its whole queue in the classic arm.
	phases[0].Kinds = replicaKinds
	if _, err := replicaKindPopulations(phases, windows, nil); err == nil || !strings.Contains(err.Error(), "analysis.authorship") {
		t.Fatalf("an unscoped classic kind in a window only must be refused: %v", err)
	}
	// Even an empty labels window is unsafe: RunLabels{Conversations} has
	// different invalidation and reuse behavior from the production path.
	_, err = replicaKindPopulations([]replicaPhase{{Name: "recent", Action: "recent", Kinds: []string{"analysis.labels"}}}, windows[:1], [][]string{{}})
	if err == nil || !strings.Contains(err.Error(), "analysis.labels") {
		t.Fatalf("window-only labels must be refused: %v", err)
	}
}

// A retained build without admission reports unsupported instead of claiming
// that an environment variable controlled its work. Defaults remain comparable
// as defaults; a pinned allowance requires equal supported grants in both arms.
func replicaCPUPolicy(workState map[string]any, requested int) map[string]any {
	policy := map[string]any{"requested_slots": requested, "supported": false,
		"effective_slots": nil, "gomaxprocs": runtime.GOMAXPROCS(0), "logical_cpus": runtime.NumCPU()}
	if workState["engine"] == "scheduled" {
		state, _ := workState["state"].(map[string]any)
		authority, _ := state["authority"].(map[string]any)
		cap, _ := authority["cpu_cap"].(json.Number)
		if slots, err := cap.Int64(); err == nil && slots > 0 {
			policy["supported"], policy["effective_slots"] = true, slots
		}
	}
	return policy
}

func TestWorkReplicaCPUPolicy(t *testing.T) {
	classic := replicaCPUPolicy(nil, 2)
	if classic["supported"] != false || classic["effective_slots"] != nil {
		t.Fatalf("classic cannot enforce a global allowance: %v", classic)
	}
	state := map[string]any{"engine": "scheduled", "state": map[string]any{"authority": map[string]any{"cpu_cap": json.Number("2")}}}
	if policy := replicaCPUPolicy(state, 2); policy["supported"] != true || policy["effective_slots"] != int64(2) {
		t.Fatalf("report the actual authority cap: %v", policy)
	}
}

// A window's CPU counts children the probe reaps inside it, and only the part
// of a running child's CPU spent after the window opened.
func TestWorkReplicaCPUWindow(t *testing.T) {
	// date has whole seconds, so this burns between seconds and seconds+1.
	busy := func(seconds int) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf(`end=$(($(date +%%s) + %d + 1)); while [ "$(date +%%s)" -lt "$end" ]; do :; done`, seconds))
	}
	start := replicaCPUNow()
	if !start.complete {
		t.Skip("descendants cannot be read on this platform; the probe reports cpu_complete=false")
	}
	if err := busy(1).Run(); err != nil {
		t.Fatal(err)
	}
	inside := start.until(replicaCPUNow())
	if cpu := inside["cpu_seconds"].(float64); cpu < 0.3 || inside["cpu_complete"] != true {
		t.Fatalf("a child reaped inside the window used %.2fs: %v", cpu, inside)
	}
	running := busy(2)
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond) // the child burns CPU before the window opens
	opened := replicaCPUNow()
	if err := running.Wait(); err != nil {
		t.Fatal(err)
	}
	window := opened.until(replicaCPUNow())
	child := (running.ProcessState.UserTime() + running.ProcessState.SystemTime()).Seconds()
	if cpu := window["cpu_seconds"].(float64); cpu > child-0.3 || cpu < 0 {
		t.Fatalf("window CPU %.2fs should exclude the %.2fs child's CPU before the window opened", cpu, child)
	}
}

// replicaFollowupRounds bounds the next passes settlement gives arrivals
// that the engine has not picked up by the time its demanded work ends.
const replicaFollowupRounds = 3

// replicaRequestLimit is the work API's ended-request summaries
// (workEndedRequestLimit), restated so the probe compiles on a baseline
// without the scheduled engine.
const replicaRequestLimit = 16

// replicaReplayClock refuses a replay clock whose arrival offsets (at most
// replicaRequestLimit nanoseconds) could change its millisecond formatTime
// value, which bounds the stored populations.
func replicaReplayClock(clock time.Time) error {
	if clock.Nanosecond()%int(time.Millisecond) != 0 {
		return fmt.Errorf("a replay clock must be whole milliseconds, so arrival request offsets cannot move its stored bounds: %s", clock.Format(time.RFC3339Nano))
	}
	return nil
}

// replicaReplay is a deterministic schedule of recent arrivals. Each one
// writes production-derived provider files from a frozen arm input into an
// arm-private source folder, then runs the service's background Capture &
// index item for that one source. Conversation identity is provider,
// account and native ID, so a session already in the snapshot gains only
// the lines it did not have; Identity says which sessions the arrivals
// carry and whether they extend the snapshot or are new to it.
type replicaReplay struct {
	Input  string `json:"input"`
	Source struct {
		Name    string         `json:"name"`
		Kind    string         `json:"kind"`
		Account string         `json:"account"`
		Options map[string]any `json:"options"`
	} `json:"source"`
	// CountQuery returns one integer that every arrival must increase.
	CountQuery string `json:"count_query"`
	// ReadyChecks must all be empty once every applied arrival is settled.
	ReadyChecks   []string         `json:"ready_checks"`
	SettleSeconds float64          `json:"settle_timeout_seconds"`
	Identity      *replicaIdentity `json:"identity"`
	Arrivals      []replicaArrival `json:"arrivals"`
}

// replicaIdentity is the conversations the arrivals carry, by provider and
// native ID under the replay source's account: all new to the snapshot, or
// all already in it and extended.
type replicaIdentity struct {
	Provider  string   `json:"provider"`
	NativeIDs []string `json:"native_ids"`
	Expect    string   `json:"expect"`
}

type replicaArrival struct {
	Name      string               `json:"name"`
	AtSeconds float64              `json:"at_seconds"`
	Files     []replicaArrivalFile `json:"files"`
}

// replicaArrivalFile is one input file as of an arrival: its first Lines
// newline-terminated lines, or all of it.
type replicaArrivalFile struct {
	Path  string `json:"path"`
	Lines *int   `json:"lines"`
}

func (r *replicaReplay) settleTimeout() time.Duration {
	if r.SettleSeconds == 0 {
		return 30 * time.Minute
	}
	return time.Duration(r.SettleSeconds * float64(time.Second))
}

// check refuses a schedule that could read outside its input, replay
// nothing, or shrink a file an earlier arrival wrote.
func (r *replicaReplay) check() error {
	if r.Input == "" || r.Input == "." || r.Input == ".." || strings.ContainsAny(r.Input, `/\`) {
		return errors.New("replay.input names one frozen input")
	}
	if !validCaptureName(r.Source.Name) || r.Source.Kind == "" || r.Source.Account == "" {
		return errors.New("replay.source needs a capture-safe name, a kind and an explicit account")
	}
	if id := r.Identity; id == nil || id.Provider == "" || len(id.NativeIDs) == 0 || id.Expect != "new" && id.Expect != "extend" ||
		slices.Contains(id.NativeIDs, "") || len(slices.Compact(slices.Sorted(slices.Values(id.NativeIDs)))) != len(id.NativeIDs) {
		return errors.New(`replay.identity needs a provider, distinct non-empty native_ids and expect "new" or "extend"`)
	}
	if !readOnlyCheck(r.CountQuery) {
		return errors.New("replay.count_query is one SELECT or WITH statement")
	}
	if len(r.ReadyChecks) == 0 || slices.ContainsFunc(r.ReadyChecks, func(query string) bool { return !readOnlyCheck(query) }) {
		return errors.New("replay.ready_checks are read-only statements: arrivals need a settlement proof")
	}
	if r.SettleSeconds < 0 || len(r.Arrivals) == 0 {
		return errors.New("replay needs arrivals and a non-negative settle_timeout_seconds")
	}
	names, written := map[string]bool{}, map[string]*int{}
	previous := 0.0
	for _, arrival := range r.Arrivals {
		if arrival.Name == "" || names[arrival.Name] || arrival.AtSeconds < previous || len(arrival.Files) == 0 {
			return fmt.Errorf("arrival %q: unique names, files, and at_seconds in order from 0", arrival.Name)
		}
		names[arrival.Name], previous = true, arrival.AtSeconds
		paths := map[string]bool{}
		for _, file := range arrival.Files {
			if !filepath.IsLocal(file.Path) || filepath.Clean(file.Path) != file.Path || paths[file.Path] || (file.Lines != nil && *file.Lines < 0) {
				return fmt.Errorf("arrival %q: %q is not a clean relative input path listed once", arrival.Name, file.Path)
			}
			paths[file.Path] = true
			if earlier, ok := written[file.Path]; ok && file.Lines != nil && (earlier == nil || *file.Lines < *earlier) {
				return fmt.Errorf("arrival %q shrinks %q: a source only grows", arrival.Name, file.Path)
			}
			written[file.Path] = file.Lines
		}
	}
	return nil
}

// replicaCensus is what the catalog holds under the replay's provider and
// account: how many declared conversations exist, their messages, and how
// many conversations there are that the identity does not declare.
type replicaCensus struct {
	Present    int
	Messages   int64
	Undeclared int64
}

// identityCensus counts the identity's conversations by the exact
// (provider, account, native_id) key, independently of count_query. Both
// queries seek the UNIQUE(provider, account, native_id) index; the declared
// messages then come from messages_conversation_idx, so the cost is the
// declared conversations' messages plus one index range of the account.
func (r *replicaReplay) identityCensus(ctx context.Context, db *sql.DB) (replicaCensus, error) {
	args := []any{r.Identity.Provider, r.Source.Account}
	for _, id := range r.Identity.NativeIDs {
		args = append(args, id)
	}
	ids := `(?` + strings.Repeat(",?", len(r.Identity.NativeIDs)-1) + `)`
	var census replicaCensus
	if err := db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum((SELECT count(*) FROM messages m WHERE m.conversation_id=c.id)),0)
		FROM conversations c WHERE c.provider=? AND c.account=? AND c.native_id IN `+ids, args...).Scan(&census.Present, &census.Messages); err != nil {
		return census, err
	}
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE provider=? AND account=? AND native_id NOT IN `+ids, args...).Scan(&census.Undeclared)
	return census, err
}

// replicaLinePrefix is data's first n newline-terminated lines.
func replicaLinePrefix(data []byte, n int) ([]byte, bool) {
	end := 0
	for range n {
		next := bytes.IndexByte(data[end:], '\n')
		if next < 0 {
			return nil, false
		}
		end += next + 1
	}
	return data[:end], true
}

// replicaStageArrival writes an arrival's files from input into replay,
// all with modification time at, and returns a digest of the bytes written.
// Inputs are read only through their exact paths: a symlink is refused.
func replicaStageArrival(input, replay string, files []replicaArrivalFile, at time.Time) (string, error) {
	digest := sha256.New()
	for _, file := range files {
		source := filepath.Join(input, file.Path)
		if resolved, err := filepath.EvalSymlinks(source); err != nil || resolved != source {
			return "", fmt.Errorf("input %s is unreadable or a symlink: %v", file.Path, err)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return "", err
		}
		if file.Lines != nil {
			prefix, ok := replicaLinePrefix(data, *file.Lines)
			if !ok {
				return "", fmt.Errorf("input %s has fewer than %d lines", file.Path, *file.Lines)
			}
			data = prefix
		}
		target := filepath.Join(replay, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return "", err
		}
		if err := os.Chtimes(target, at, at); err != nil {
			return "", err
		}
		fmt.Fprintf(digest, "%s\x00%d\x00", file.Path, len(data))
		digest.Write(data)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// replicaApplyArrival is one arrival in the product's own way: the staged
// bytes, then the background Capture & index item for the replay source,
// which runs each build's own capture, ingest admission and deferred marks.
func replicaApplyArrival(ctx context.Context, server *Server, replay *replicaReplay, input, dir string, index int, clock time.Time) (string, error) {
	// Monotonic, at most the pinned clock, the same in every arm.
	at := clock.Add(time.Duration(index+1-len(replay.Arrivals)) * time.Second)
	digest, err := replicaStageArrival(input, dir, replay.Arrivals[index].Files, at)
	if err != nil {
		return "", err
	}
	opCtx, op := server.begin(ctx, OperationSpec{Kind: "background.update", Variant: "requested", Trigger: triggerUser})
	err = server.backgroundCapture(opCtx, op, "sources", []string{replay.Source.Name})
	op.Finish(err)
	return digest, err
}

// replicaArrivalReport is one arrival's evidence. Times are seconds from the
// start of the work window; ready is measured from the arrival's start.
type replicaArrivalReport struct {
	Name                string   `json:"name"`
	AtSeconds           float64  `json:"at_seconds"`
	StartedSeconds      *float64 `json:"started_seconds"`
	LateSeconds         *float64 `json:"late_seconds"`
	CaptureIndexSeconds *float64 `json:"capture_index_seconds"`
	AppliedSeconds      *float64 `json:"applied_seconds"`
	CountBefore         *int64   `json:"count_before"`
	CountAfter          *int64   `json:"count_after"`
	// IdentityMessagesBefore and After are the declared conversations'
	// messages around the arrival, which must strictly increase.
	IdentityMessagesBefore *int64 `json:"identity_message_count_before"`
	IdentityMessagesAfter  *int64 `json:"identity_message_count_after"`
	// PendingAtApply says the ready checks saw this arrival's work: false
	// means they were already empty, so its ready time proves nothing.
	PendingAtApply *bool    `json:"ready_pending_at_apply"`
	ReadySeconds   *float64 `json:"ready_seconds"`
	FilesSHA256    string   `json:"files_sha256,omitempty"`
	began, applied time.Time
}

// replicaArrivalRequest is the recent demand one applied arrival sent.
// Repeated demand with the same intent joins a live request, so its id can
// be an earlier arrival's (JoinedArrival) or, found once the requests have
// ended, a primary phase's (JoinedPhases).
type replicaArrivalRequest struct {
	Arrival          string         `json:"arrival"`
	Scope            string         `json:"scope"`
	Kinds            []string       `json:"kinds"`
	RequestID        uint64         `json:"request_id"`
	SubmittedSeconds float64        `json:"submitted_seconds"`
	JoinedArrival    *string        `json:"joined_arrival"`
	JoinedPhases     []string       `json:"joined_phases"`
	Facts            map[string]any `json:"facts"`
}

type replicaArrivals struct {
	mu        sync.Mutex
	plan      *replicaReplay
	started   time.Time
	stop      context.CancelCauseFunc
	identity  map[string]any
	entries   []replicaArrivalReport
	rounds    int
	followups []any
	settledAt *float64
	// demand, when set before run, sends recent demand for demandKinds once
	// each arrival is applied and its evidence recorded, and returns the
	// work request's id; its error fails the arrival like any other.
	demand      func(ctx context.Context, index int) (uint64, error)
	demandKinds []string
	requests    []replicaArrivalRequest
}

// newReplicaArrivals schedules plan's arrivals from started; stop cancels
// the work they run beside when one fails.
func newReplicaArrivals(plan *replicaReplay, started time.Time, stop context.CancelCauseFunc) *replicaArrivals {
	ids := slices.Sorted(slices.Values(plan.Identity.NativeIDs))
	arrivals := &replicaArrivals{plan: plan, started: started, stop: stop, identity: map[string]any{"provider": plan.Identity.Provider,
		"account": plan.Source.Account, "expect": plan.Identity.Expect, "native_ids": len(ids), "native_ids_sha256": replicaIDsDigest(ids),
		"present_before": nil, "present_after": nil, "other_conversations_before": nil, "other_conversations_after": nil}}
	for _, arrival := range plan.Arrivals {
		arrivals.entries = append(arrivals.entries, replicaArrivalReport{Name: arrival.Name, AtSeconds: arrival.AtSeconds})
	}
	return arrivals
}

func replicaSeconds(d time.Duration) *float64 { value := d.Seconds(); return &value }

// replicaArrivalFailure is a failed arrival's error as the cause that
// cancelled the work.
type replicaArrivalFailure struct{ err error }

func (f replicaArrivalFailure) Error() string { return "replay: " + f.err.Error() }
func (f replicaArrivalFailure) Unwrap() error { return f.err }

// replicaFailure is the failure to report for message: the arrival error
// that cancelled ctx, with message as what it interrupted, or message.
func replicaFailure(ctx context.Context, message string) (failure, interrupted string) {
	var arrival replicaArrivalFailure
	if errors.As(context.Cause(ctx), &arrival) {
		return arrival.Error(), message
	}
	return message, ""
}

// run applies every arrival at its time, in order, and fails, cancelling
// the work, on the first error. One that starts after its time because the
// previous arrival was still running records how late. Before the first
// arrival none (new) or all (extend) of the identity's conversations are
// in the catalog, and after the last all of them are. Every arrival must
// add messages to the declared conversations, whatever count_query says,
// and the replay must leave the account's undeclared conversations as it
// found them: unrelated sessions cannot satisfy the identity.
func (a *replicaArrivals) run(ctx context.Context, apply func(int) (string, error), count func(context.Context) (int64, error), pending func(context.Context) (bool, error), census func(context.Context) (replicaCensus, error)) error {
	err := a.arrive(ctx, apply, count, pending, census)
	if err != nil && a.stop != nil {
		a.stop(replicaArrivalFailure{err})
	}
	return err
}

func (a *replicaArrivals) arrive(ctx context.Context, apply func(int) (string, error), count func(context.Context) (int64, error), pending func(context.Context) (bool, error), census func(context.Context) (replicaCensus, error)) error {
	identity := a.plan.Identity
	initial, err := census(ctx)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	a.mu.Lock()
	a.identity["present_before"], a.identity["other_conversations_before"] = initial.Present, initial.Undeclared
	a.mu.Unlock()
	if want := map[string]int{"new": 0, "extend": len(identity.NativeIDs)}[identity.Expect]; initial.Present != want {
		return fmt.Errorf("identity: expected %s conversations, but %d of %d are already in the catalog", identity.Expect, initial.Present, len(identity.NativeIDs))
	}
	for index, arrival := range a.plan.Arrivals {
		due := a.started.Add(time.Duration(arrival.AtSeconds * float64(time.Second)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(due)):
		}
		began := time.Now()
		before, err := count(ctx)
		if err != nil {
			return fmt.Errorf("arrival %s: count_query: %w", arrival.Name, err)
		}
		declared, err := census(ctx)
		if err != nil {
			return fmt.Errorf("arrival %s: identity: %w", arrival.Name, err)
		}
		a.mu.Lock()
		entry := &a.entries[index]
		entry.began, entry.StartedSeconds, entry.LateSeconds, entry.CountBefore = began, replicaSeconds(began.Sub(a.started)), replicaSeconds(max(0, began.Sub(due))), &before
		entry.IdentityMessagesBefore = &declared.Messages
		a.mu.Unlock()
		applying := time.Now()
		digest, err := apply(index)
		took := time.Since(applying)
		if err != nil {
			return fmt.Errorf("arrival %s: %w", arrival.Name, err)
		}
		after, err := count(ctx)
		if err != nil {
			return fmt.Errorf("arrival %s: count_query: %w", arrival.Name, err)
		}
		grown, err := census(ctx)
		if err != nil {
			return fmt.Errorf("arrival %s: identity: %w", arrival.Name, err)
		}
		a.mu.Lock()
		entry.CountAfter, entry.IdentityMessagesAfter = &after, &grown.Messages
		a.mu.Unlock()
		if after <= before {
			return fmt.Errorf("arrival %s is not a real arrival: count_query went from %d to %d", arrival.Name, before, after)
		}
		if grown.Messages <= declared.Messages {
			return fmt.Errorf("arrival %s did not add to the declared conversations: their messages went from %d to %d", arrival.Name, declared.Messages, grown.Messages)
		}
		observed, err := pending(ctx)
		if err != nil {
			return fmt.Errorf("arrival %s: ready check: %w", arrival.Name, err)
		}
		applied := time.Now()
		a.mu.Lock()
		entry.FilesSHA256, entry.CaptureIndexSeconds, entry.PendingAtApply = digest, replicaSeconds(took), &observed
		entry.applied, entry.AppliedSeconds = applied, replicaSeconds(applied.Sub(a.started))
		a.mu.Unlock()
		if a.demand == nil {
			continue
		}
		submitted := time.Now()
		id, err := a.demand(ctx, index)
		if err != nil {
			return fmt.Errorf("arrival %s: recent demand: %w", arrival.Name, err)
		}
		a.mu.Lock()
		request := replicaArrivalRequest{Arrival: arrival.Name, Scope: "recent", Kinds: a.demandKinds, RequestID: id, SubmittedSeconds: submitted.Sub(a.started).Seconds()}
		if i := slices.IndexFunc(a.requests, func(earlier replicaArrivalRequest) bool { return earlier.RequestID == id }); i >= 0 {
			request.JoinedArrival = &a.requests[i].Arrival
		}
		a.requests = append(a.requests, request)
		a.mu.Unlock()
	}
	final, err := census(ctx)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	a.mu.Lock()
	a.identity["present_after"], a.identity["other_conversations_after"] = final.Present, final.Undeclared
	a.mu.Unlock()
	if final.Present != len(identity.NativeIDs) {
		return fmt.Errorf("identity: after the arrivals %d of %d conversations are in the catalog", final.Present, len(identity.NativeIDs))
	}
	if final.Undeclared != initial.Undeclared {
		return fmt.Errorf("identity: the arrivals changed the undeclared conversations under provider %s, account %s from %d to %d; declare every session they carry, subagents included",
			identity.Provider, a.plan.Source.Account, initial.Undeclared, final.Undeclared)
	}
	return nil
}

// waiting is whether an applied arrival has no ready time yet.
func (a *replicaArrivals) waiting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.entries, func(entry replicaArrivalReport) bool { return !entry.applied.IsZero() && entry.ReadySeconds == nil })
}

// ready records at as the ready time of every arrival applied before the
// checks that found nothing pending began.
func (a *replicaArrivals) ready(checked, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.entries {
		entry := &a.entries[i]
		if !entry.applied.IsZero() && !entry.applied.After(checked) && entry.ReadySeconds == nil {
			entry.ReadySeconds = replicaSeconds(at.Sub(entry.began))
		}
	}
}

// replicaRequestWatch is what the probe's polls of the work API saw of the
// requests it tracks, kept across awaits: the last poll that saw each
// request live, and its facts from the first poll that saw it terminal.
type replicaRequestWatch struct {
	seenLive map[uint64]float64
	terminal map[uint64]map[string]any
}

// observe records one poll of the work state, taken seconds after work
// start, for the requests in tracked (id to phase name), and returns the
// ids it saw terminal for the first time. A terminal request's facts gain
// ended_seconds (this poll, an upper bound on its end),
// history_active_until_seconds (the last poll that saw it live, a lower
// bound; null if none did) and phase; later polls leave them unchanged.
func (w *replicaRequestWatch) observe(state map[string]any, tracked map[uint64]string, seconds float64) (ended []uint64) {
	for _, list := range []string{"requests", "ended"} {
		items, _ := state[list].([]any)
		for _, item := range items {
			facts, _ := item.(map[string]any)
			number, _ := facts["id"].(json.Number)
			parsed, _ := number.Int64()
			id := uint64(parsed)
			phase, ok := tracked[id]
			if !ok || w.terminal[id] != nil {
				continue
			}
			switch facts["state"] {
			case "succeeded", "failed", "cancelled", "interrupted":
				facts["ended_seconds"] = seconds
				facts["history_active_until_seconds"] = nil
				if seen, ok := w.seenLive[id]; ok {
					facts["history_active_until_seconds"] = seen
				}
				facts["phase"] = phase
				w.terminal[id] = facts
				ended = append(ended, id)
			default:
				w.seenLive[id] = seconds
			}
		}
	}
	return ended
}

// requestIDs is the distinct work requests the arrivals' demand reached,
// with the arrival that first reached each, and how many arrivals sent it.
func (a *replicaArrivals) requestIDs() (ids []uint64, firsts []string, total int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, request := range a.requests {
		if !slices.Contains(ids, request.RequestID) {
			ids, firsts = append(ids, request.RequestID), append(firsts, request.Arrival)
		}
	}
	return ids, firsts, len(a.requests)
}

// requestsEnded records each arrival request's ended facts and the primary
// phases (by request id) that share its request.
func (a *replicaArrivals) requestsEnded(ended map[uint64]map[string]any, phases map[uint64][]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.requests {
		request := &a.requests[i]
		request.Facts, request.JoinedPhases = ended[request.RequestID], append([]string{}, phases[request.RequestID]...)
	}
}

func (a *replicaArrivals) settled(rounds int, followups []any, seconds float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rounds, a.followups, a.settledAt = rounds, followups, &seconds
}

func (a *replicaArrivals) report(host string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{"input": a.plan.Input, "source": a.plan.Source.Name, "kind": a.plan.Source.Kind, "host": host,
		"identity": maps.Clone(a.identity), "arrivals": slices.Clone(a.entries), "requests": append([]replicaArrivalRequest{}, a.requests...),
		"followup_rounds": a.rounds, "followups": a.followups, "settled_seconds": a.settledAt,
		"basis": "Each arrival writes its frozen input bytes into an arm-private source folder and runs the service's background Capture & index item for that source; count_query and the declared conversations' messages (by provider, account and native ID) must increase, and the account's undeclared conversations must not change. On the scheduled engine each applied arrival then submits explicit recent demand for every demanded kind (requests), as new recent demand on arrival, not the service's full daemon cadence; classic continues its whole pass and gets no arrival demand. Settlement needs every arrival applied, every arrival request succeeded, ready_checks and every kind's pending_checks empty; classic follow-ups re-drain the demanded kinds and scheduled follow-ups submit recent demand."}
}

// replicaDemandRecent submits recent demand for kinds through the work API
// and returns the request's id. It reports instead of failing the test, so
// an arrival's goroutine can call it.
func replicaDemandRecent(server *Server, token string, kinds []string) (uint64, error) {
	status, value, err := probeJSON(server, token, http.MethodPost, "/api/work", map[string]any{"action": "recent", "kinds": kinds})
	if err != nil {
		return 0, err
	}
	if status != http.StatusAccepted || value["scope"] != "recent" {
		return 0, fmt.Errorf("work request refused (%d %v)", status, value["error"])
	}
	number, _ := value["request_id"].(json.Number)
	id, err := number.Int64()
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("no request id in %v", value)
	}
	return uint64(id), nil
}

func checksEmpty(ctx context.Context, db *sql.DB, queries []string) (bool, error) {
	for _, query := range queries {
		if pending, err := checkPending(ctx, db, query); err != nil || pending {
			return false, err
		}
	}
	return true, nil
}

// replicaMemoryPlan sets the sampling interval and the idle period after
// settlement over which retained memory is observed.
type replicaMemoryPlan struct {
	IntervalSeconds float64 `json:"interval_seconds"`
	IdleSeconds     float64 `json:"idle_seconds"`
}

const replicaMemoryBasis = "Physical footprint (proc_pid_rusage phys_footprint) of this probe process and each live descendant, read at labeled moments and every interval_seconds during work and idle; descendants are counted by process name. A spike between samples can be missed and footprints are not lifetime peaks: the harness's process-tree samples remain the gated figures. idle-released follows runtime.GC and debug.FreeOSMemory, so retained_bytes (idle-released less work-start, whole tree) counts live retained memory, not collectable Go garbage. go holds runtime.MemStats of this process."

type replicaMemorySample struct {
	Label                string            `json:"label"`
	Unix                 float64           `json:"unix"`
	Seconds              *float64          `json:"seconds"`
	SelfFootprint        *uint64           `json:"self_footprint"`
	SelfResident         *uint64           `json:"self_resident"`
	DescendantsFootprint *uint64           `json:"descendants_footprint"`
	Descendants          map[string]int    `json:"descendants"`
	TreeFootprint        *uint64           `json:"tree_footprint"`
	Complete             bool              `json:"complete"`
	Go                   map[string]uint64 `json:"go"`
}

// replicaMemoryNow reads this process tree's physical memory. A process
// that cannot be read leaves the sample incomplete, never smaller.
func replicaMemoryNow(label string) replicaMemorySample {
	return replicaMemorySnapshot(label, resourceBasis, processUsageOf, resourceProcessTable())
}

// replicaMemorySnapshot keeps native observation separate from tree arithmetic.
// Only a physical-footprint reader can populate the physical fields; runtime
// memory remains available independently in Go. Unknown values serialize null.
func replicaMemorySnapshot(label, basis string, read func(int) (processUsage, error), nodes []processNode) replicaMemorySample {
	sample := replicaMemorySample{Label: label, Unix: replicaUnix(time.Now()), Descendants: map[string]int{}, Complete: true}
	self := os.Getpid()
	if usage, err := read(self); err == nil && basis == resourceBasisKernel {
		if usage.Known.has(counterMemory) {
			sample.SelfFootprint = new(usage.Footprint)
		}
		if usage.Known.has(counterResident) {
			sample.SelfResident = new(usage.Resident)
		}
	}
	descendants, descendantsKnown := uint64(0), nodes != nil && basis == resourceBasisKernel
	children, names := map[int][]int{}, map[int]string{}
	for _, node := range nodes {
		children[node.Parent], names[node.PID] = append(children[node.Parent], node.PID), node.Name
	}
	for frontier := []int{self}; len(frontier) > 0; frontier = frontier[1:] {
		for _, child := range children[frontier[0]] {
			frontier = append(frontier, child)
			sample.Descendants[names[child]]++
			usage, err := read(child)
			if err != nil || !usage.Known.has(counterMemory) || basis != resourceBasisKernel {
				descendantsKnown = false
				continue
			}
			descendants += usage.Footprint
		}
	}
	if descendantsKnown {
		sample.DescendantsFootprint = &descendants
		if sample.SelfFootprint != nil {
			sample.TreeFootprint = new(*sample.SelfFootprint + descendants)
		}
	}
	sample.Complete = sample.TreeFootprint != nil && sample.SelfResident != nil
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	sample.Go = map[string]uint64{"heap_alloc": stats.HeapAlloc, "heap_inuse": stats.HeapInuse, "heap_idle": stats.HeapIdle,
		"heap_released": stats.HeapReleased, "sys": stats.Sys, "stack_inuse": stats.StackInuse, "num_gc": uint64(stats.NumGC)}
	return sample
}

type replicaMemory struct {
	mu      sync.Mutex
	started time.Time
	samples []replicaMemorySample
}

// begin sets the work window's start; earlier samples get negative seconds.
func (m *replicaMemory) begin(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = at
	for i := range m.samples {
		seconds := m.samples[i].Unix - replicaUnix(at)
		m.samples[i].Seconds = &seconds
	}
}

func (m *replicaMemory) sample(label string) {
	sample := replicaMemoryNow(label)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started.IsZero() {
		seconds := sample.Unix - replicaUnix(m.started)
		sample.Seconds = &seconds
	}
	m.samples = append(m.samples, sample)
}

func (m *replicaMemory) snapshot() []replicaMemorySample {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.samples)
}

// replicaRetention summarizes what the tree held before work, at
// settlement, at the end of idle and after releasing free memory. Retained
// figures are unavailable, never zero, without complete samples at both ends.
func replicaRetention(samples []replicaMemorySample, plan replicaMemoryPlan) map[string]any {
	last := func(label string) *replicaMemorySample {
		for i := len(samples) - 1; i >= 0; i-- {
			if samples[i].Label == label {
				return &samples[i]
			}
		}
		return nil
	}
	complete := !slices.ContainsFunc(samples, func(sample replicaMemorySample) bool { return !sample.Complete })
	retention := map[string]any{"basis": replicaMemoryBasis, "retained_bytes": nil, "retained_self_bytes": nil, "retained_descendants_bytes": nil, "retained_go_heap_inuse_bytes": nil,
		"coverage": map[string]any{"samples": len(samples), "interval_seconds": plan.IntervalSeconds, "idle_seconds": plan.IdleSeconds, "complete": complete}}
	for key, label := range map[string]string{"work_start": "work-start", "settled": "settled", "idle_end": "idle", "idle_released": "idle-released"} {
		retention[key] = nil
		if sample := last(label); sample != nil && sample.TreeFootprint != nil {
			retention[key] = *sample.TreeFootprint
		}
	}
	if idle := last("idle"); idle != nil {
		retention["descendants_at_idle_end"] = idle.Descendants
	}
	start, released := last("work-start"), last("idle-released")
	if start != nil && released != nil && start.Complete && released.Complete && start.TreeFootprint != nil && released.TreeFootprint != nil && start.SelfFootprint != nil && released.SelfFootprint != nil && start.DescendantsFootprint != nil && released.DescendantsFootprint != nil {
		retention["retained_bytes"] = int64(*released.TreeFootprint) - int64(*start.TreeFootprint)
		retention["retained_self_bytes"] = int64(*released.SelfFootprint) - int64(*start.SelfFootprint)
		retention["retained_descendants_bytes"] = int64(*released.DescendantsFootprint) - int64(*start.DescendantsFootprint)
		retention["retained_go_heap_inuse_bytes"] = int64(released.Go["heap_inuse"]) - int64(start.Go["heap_inuse"])
	}
	return retention
}

func replicaTestReplay(arrivals ...replicaArrival) *replicaReplay {
	replay := &replicaReplay{Input: "arrivals", CountQuery: "SELECT count(*) FROM messages", ReadyChecks: []string{"SELECT 1 WHERE 0"}, Arrivals: arrivals}
	replay.Source.Name, replay.Source.Kind, replay.Source.Account = "replay-claude", "claude", "local"
	replay.Identity = &replicaIdentity{Provider: "claude", NativeIDs: []string{"s1"}, Expect: "new"}
	return replay
}

func replicaLines(n int) *int { return &n }

// A schedule may only grow its sources from inside its frozen input.
func TestWorkReplicaReplayPlan(t *testing.T) {
	file := func(path string, lines *int) []replicaArrivalFile {
		return []replicaArrivalFile{{Path: path, Lines: lines}}
	}
	valid := replicaTestReplay(replicaArrival{Name: "a1", AtSeconds: 0, Files: file("p/s.jsonl", replicaLines(1))},
		replicaArrival{Name: "a2", AtSeconds: 5, Files: file("p/s.jsonl", replicaLines(3))},
		replicaArrival{Name: "a3", AtSeconds: 5, Files: file("p/s.jsonl", nil)})
	if err := valid.check(); err != nil {
		t.Fatal(err)
	}
	refused := map[string]func(*replicaReplay){
		"absolute path": func(r *replicaReplay) { r.Arrivals[0].Files = file("/etc/hosts", nil) },
		"parent path":   func(r *replicaReplay) { r.Arrivals[0].Files = file("../x.jsonl", nil) },
		"unclean path":  func(r *replicaReplay) { r.Arrivals[0].Files = file("p/./s.jsonl", replicaLines(1)) },
		"fewer lines":   func(r *replicaReplay) { r.Arrivals[1].Files = file("p/s.jsonl", replicaLines(0)) },
		"prefix after all": func(r *replicaReplay) {
			r.Arrivals = append(r.Arrivals, replicaArrival{Name: "a4", AtSeconds: 9, Files: file("p/s.jsonl", replicaLines(5))})
		},
		"out of order":   func(r *replicaReplay) { r.Arrivals[1].AtSeconds = -1 },
		"duplicate name": func(r *replicaReplay) { r.Arrivals[1].Name = "a1" },
		"input path":     func(r *replicaReplay) { r.Input = "../inputs" },
		"source name":    func(r *replicaReplay) { r.Source.Name = "a/b" },
		"writing count":  func(r *replicaReplay) { r.CountQuery = "DELETE FROM messages" },
		"no settlement":  func(r *replicaReplay) { r.ReadyChecks = nil },
		"no arrivals":    func(r *replicaReplay) { r.Arrivals = nil },
		"negative lines": func(r *replicaReplay) { r.Arrivals[0].Files = file("p/s.jsonl", replicaLines(-1)) },
		"listed twice": func(r *replicaReplay) {
			r.Arrivals[0].Files = append(file("p/s.jsonl", nil), file("p/s.jsonl", nil)...)
		},
		"negative deadline": func(r *replicaReplay) { r.SettleSeconds = -1 },
		"implicit account":  func(r *replicaReplay) { r.Source.Account = "" },
		"no identity":       func(r *replicaReplay) { r.Identity = nil },
		"no provider":       func(r *replicaReplay) { r.Identity.Provider = "" },
		"no native ids":     func(r *replicaReplay) { r.Identity.NativeIDs = nil },
		"empty native id":   func(r *replicaReplay) { r.Identity.NativeIDs = []string{"s1", ""} },
		"repeated id":       func(r *replicaReplay) { r.Identity.NativeIDs = []string{"s1", "s2", "s1"} },
		"unknown intent":    func(r *replicaReplay) { r.Identity.Expect = "either" },
	}
	extend := replicaTestReplay(valid.Arrivals...)
	extend.Identity.NativeIDs, extend.Identity.Expect = []string{"s1", "s2"}, "extend"
	if err := extend.check(); err != nil {
		t.Fatal(err)
	}
	for name, change := range refused {
		replay := replicaTestReplay(slices.Clone(valid.Arrivals)...)
		change(replay)
		if err := replay.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Arrival offsets never move a whole-millisecond clock's stored bounds;
	// a clock between milliseconds is refused, since they could.
	for _, value := range []string{"2026-11-07T23:59:59Z", "2026-11-07T23:59:59.123Z"} {
		clock, _ := time.Parse(time.RFC3339, value)
		if err := replicaReplayClock(clock); err != nil {
			t.Errorf("%s: %v", value, err)
		}
		if moved := clock.Add(replicaRequestLimit * time.Nanosecond); formatTime(moved) != formatTime(clock) {
			t.Errorf("%s: offset moved the stored bound to %s", value, formatTime(moved))
		}
	}
	for _, value := range []string{"2026-11-07T23:59:59.999999990Z", "2026-11-07T23:59:59.000000001Z"} {
		if clock, _ := time.Parse(time.RFC3339, value); replicaReplayClock(clock) == nil {
			t.Errorf("%s: accepted", value)
		}
	}
}

// The watch keeps a request's timings from the polls around its own end,
// across awaits, including a request tracked only after it was first seen.
func TestWorkReplicaRequestWatch(t *testing.T) {
	poll := func(states ...string) map[string]any {
		live, ended := []any{}, []any{}
		for i, state := range states {
			facts := map[string]any{"id": json.Number(fmt.Sprint(i + 1)), "state": state}
			if state == "running" || state == "queued" {
				live = append(live, facts)
			} else if state != "" {
				ended = append(ended, facts)
			}
		}
		return map[string]any{"requests": live, "ended": ended}
	}
	watch := replicaRequestWatch{seenLive: map[uint64]float64{}, terminal: map[uint64]map[string]any{}}
	primary := map[uint64]string{1: "history"}
	both := map[uint64]string{1: "history", 2: "arrival:a1"}
	steps := []struct {
		state   map[string]any
		tracked map[uint64]string
		ended   []uint64
	}{
		{poll("running", "running", "running"), primary, nil},
		{poll("running", "running", "failed"), both, nil}, // 3 is never tracked
		{poll("running", "running"), both, nil},
		{poll("running", "succeeded"), both, []uint64{2}},
		{poll("succeeded", "succeeded"), both, []uint64{1}},
		{poll("succeeded", "succeeded"), both, nil},
	}
	for i, step := range steps {
		if ended := watch.observe(step.state, step.tracked, float64(i+1)); !slices.Equal(ended, step.ended) {
			t.Fatalf("poll %d: newly ended %v, want %v", i+1, ended, step.ended)
		}
	}
	for id, want := range map[uint64][3]any{1: {5.0, 4.0, "history"}, 2: {4.0, 3.0, "arrival:a1"}} {
		facts := watch.terminal[id]
		if got := [3]any{facts["ended_seconds"], facts["history_active_until_seconds"], facts["phase"]}; got != want {
			t.Errorf("request %d: ended, last live, phase = %v, want %v", id, got, want)
		}
	}
	if _, ok := watch.terminal[3]; ok {
		t.Error("an untracked request was recorded")
	}
	// A request first seen already terminal has no live lower bound.
	late := replicaRequestWatch{seenLive: map[uint64]float64{}, terminal: map[uint64]map[string]any{}}
	late.observe(poll("cancelled"), primary, 1)
	if facts := late.terminal[1]; facts["history_active_until_seconds"] != nil || facts["ended_seconds"] != 1.0 {
		t.Errorf("first seen terminal: %v", facts)
	}
}

// Staging writes the pinned prefix with a pinned time and reads no symlink.
func TestWorkReplicaArrivalStaging(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input, replay := filepath.Join(root, "input"), filepath.Join(root, "replay")
	writeSourceFile(t, filepath.Join(input, "p", "s.jsonl"), "one\ntwo\nthree\npartial")
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	first, err := replicaStageArrival(input, replay, []replicaArrivalFile{{Path: "p/s.jsonl", Lines: replicaLines(2)}}, at)
	if err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(replay, "p", "s.jsonl")); got != "one\ntwo\n" {
		t.Fatalf("a two-line arrival wrote %q", got)
	}
	if info, err := os.Stat(filepath.Join(replay, "p", "s.jsonl")); err != nil || !info.ModTime().Equal(at) {
		t.Fatalf("staged files carry the pinned time: %v %v", info, err)
	}
	again, _ := replicaStageArrival(input, replay, []replicaArrivalFile{{Path: "p/s.jsonl", Lines: replicaLines(2)}}, at)
	whole, err := replicaStageArrival(input, replay, []replicaArrivalFile{{Path: "p/s.jsonl"}}, at)
	if err != nil || again != first || whole == first || readText(t, filepath.Join(replay, "p", "s.jsonl")) != "one\ntwo\nthree\npartial" {
		t.Fatalf("digests follow the staged bytes: %s %s %s %v", first, again, whole, err)
	}
	if _, err := replicaStageArrival(input, replay, []replicaArrivalFile{{Path: "p/s.jsonl", Lines: replicaLines(4)}}, at); err == nil {
		t.Fatal("an unterminated line is not a whole line")
	}
	outside := filepath.Join(root, "outside.jsonl")
	writeSourceFile(t, outside, "secret\n")
	if err := os.Symlink(outside, filepath.Join(input, "p", "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := replicaStageArrival(input, replay, []replicaArrivalFile{{Path: "p/link.jsonl"}}, at); err == nil {
		t.Fatal("a symlinked input must be refused")
	}
}

// Arrivals go through the service's own Capture & index item: each adds the
// session's new lines to one conversation, and one that adds nothing fails.
// The declared identity is checked before the first arrival and after the
// last, every arrival must add to the declared conversations themselves,
// undeclared ones must not appear, and a failed arrival cancels the work
// with its own error.
func TestWorkReplicaArrivals(t *testing.T) {
	useHost(t, "replay-host")
	catalog, config := testCatalog(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input, dir := filepath.Join(root, "inputs", "arrivals"), filepath.Join(root, "replay", "replay-claude")
	writeSourceFile(t, filepath.Join(input, "-proj", "s1.jsonl"),
		claudeLine("s1", "u1", "first", "/proj", 1)+claudeLine("s1", "u2", "second", "/proj", 2)+claudeLine("s1", "u3", "third", "/proj", 3))
	writeSourceFile(t, filepath.Join(input, "-proj", "s2.jsonl"), claudeLine("s2", "v1", "first", "/proj", 4)+claudeLine("s2", "v2", "second", "/proj", 5))
	writeSourceFile(t, filepath.Join(input, "-proj", "s3.jsonl"), claudeLine("s3", "w1", "first", "/proj", 7)+claudeLine("s3", "w2", "second", "/proj", 8))
	for _, session := range []string{"s5", "s6", "s8", "s9"} {
		writeSourceFile(t, filepath.Join(input, "-proj", session+".jsonl"), claudeLine(session, session+"-1", "first", "/proj", 6))
	}
	replay := replicaTestReplay(
		replicaArrival{Name: "a1", AtSeconds: 0, Files: []replicaArrivalFile{{Path: "-proj/s1.jsonl", Lines: replicaLines(1)}}},
		replicaArrival{Name: "a2", AtSeconds: 0.2, Files: []replicaArrivalFile{{Path: "-proj/s1.jsonl"}}})
	if err := replay.check(); err != nil {
		t.Fatal(err)
	}
	config.Sources = []SourceConfig{{Name: replay.Source.Name, Kind: replay.Source.Kind, Path: dir, Account: replay.Source.Account, Enabled: true}}
	server := testServer(t, config, catalog)
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	count := func(ctx context.Context) (int64, error) {
		var n int64
		err := catalog.DB.QueryRowContext(ctx, replay.CountQuery).Scan(&n)
		return n, err
	}
	pending := func(context.Context) (bool, error) { return false, nil }
	// play runs a schedule beside a work context it may cancel, with demand,
	// when set, as its arrival demand; playing is the schedule it runs.
	var demand func(context.Context, int) (uint64, error)
	var playing *replicaArrivals
	play := func(plan *replicaReplay) (*replicaArrivals, context.Context, error) {
		work, cancel := context.WithCancelCause(t.Context())
		t.Cleanup(func() { cancel(nil) })
		arrivals := newReplicaArrivals(plan, time.Now(), cancel)
		arrivals.demand, arrivals.demandKinds, playing = demand, []string{"analysis.timing"}, arrivals
		err := arrivals.run(t.Context(), func(index int) (string, error) {
			return replicaApplyArrival(t.Context(), server, plan, input, dir, index, clock)
		}, count, pending, func(ctx context.Context) (replicaCensus, error) { return plan.identityCensus(ctx, catalog.DB) })
		return arrivals, work, err
	}
	// Arrival demand is sent only once the arrival's evidence is recorded;
	// a second arrival reaching the same live request joins it.
	demand = func(_ context.Context, index int) (uint64, error) {
		entry := playing.report("replay-host")["arrivals"].([]replicaArrivalReport)[index]
		if entry.AppliedSeconds == nil || entry.PendingAtApply == nil || entry.CountAfter == nil || entry.IdentityMessagesAfter == nil || entry.FilesSHA256 == "" {
			return 0, fmt.Errorf("demanded before the evidence was recorded: %+v", entry)
		}
		return 7, nil
	}
	arrivals, work, err := play(replay)
	demand = nil
	if err != nil || work.Err() != nil {
		t.Fatal(err, work.Err())
	}
	result := arrivals.report("replay-host")
	if requests := result["requests"].([]replicaArrivalRequest); len(requests) != 2 || requests[0].Arrival != "a1" || requests[0].RequestID != 7 || requests[0].JoinedArrival != nil ||
		requests[1].JoinedArrival == nil || *requests[1].JoinedArrival != "a1" || requests[1].Scope != "recent" || requests[1].Kinds[0] != "analysis.timing" || requests[1].Facts != nil {
		t.Fatalf("each applied arrival records its request: %+v", requests)
	}
	if ids, firsts, total := arrivals.requestIDs(); !slices.Equal(ids, []uint64{7}) || !slices.Equal(firsts, []string{"a1"}) || total != 2 {
		t.Fatalf("a joined request is awaited once: %v %v %d", ids, firsts, total)
	}
	arrivals.requestsEnded(map[uint64]map[string]any{7: {"state": "succeeded"}}, map[uint64][]string{7: {"recent"}})
	if requests := arrivals.report("replay-host")["requests"].([]replicaArrivalRequest); requests[1].Facts["state"] != "succeeded" || !slices.Equal(requests[1].JoinedPhases, []string{"recent"}) {
		t.Fatalf("ended facts and a shared primary phase are reported: %+v", requests)
	}
	report := result["arrivals"].([]replicaArrivalReport)
	if *report[0].CountBefore != 0 || *report[0].CountAfter != 1 || *report[1].CountAfter != 3 || report[1].LateSeconds == nil {
		t.Fatalf("each arrival adds only its new lines: %+v", report)
	}
	if *report[0].IdentityMessagesBefore != 0 || *report[0].IdentityMessagesAfter != 1 || *report[1].IdentityMessagesBefore != 1 || *report[1].IdentityMessagesAfter != 3 {
		t.Fatalf("the declared conversation's own messages are counted around each arrival: %+v", report)
	}
	if identity := result["identity"].(map[string]any); identity["expect"] != "new" || identity["present_before"] != 0 || identity["present_after"] != 1 ||
		identity["other_conversations_before"] != int64(0) || identity["other_conversations_after"] != int64(0) ||
		identity["account"] != "local" || identity["native_ids_sha256"] != replicaIDsDigest([]string{"s1"}) {
		t.Fatalf("a new conversation is absent before and present after, by hash: %v", identity)
	}
	var conversations int
	if err := catalog.DB.QueryRow(`SELECT count(DISTINCT conversation_id) FROM messages`).Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("the growing session is one conversation: %d %v", conversations, err)
	}
	if _, err := os.Stat(filepath.Join(config.CaptureRoot, "replay-host", replay.Source.Name)); err != nil {
		t.Fatalf("the arrival was captured like a source: %v", err)
	}
	arrivals.ready(time.Now(), time.Now())
	if arrivals.waiting() || report[0].ReadySeconds != nil {
		t.Fatal("ready marks the live entries, not a returned copy")
	}
	schedule := func(expect string, ids []string, path string, lines *int) *replicaReplay {
		plan := replicaTestReplay(replicaArrival{Name: "again", Files: []replicaArrivalFile{{Path: path, Lines: lines}}})
		plan.Identity.Expect, plan.Identity.NativeIDs = expect, ids
		return plan
	}
	// failed checks that a schedule fails with want, applies nothing when
	// refused up front, and cancels the work with its own error.
	failed := func(name string, plan *replicaReplay, want string, applied, changed bool) *replicaArrivals {
		t.Helper()
		var before int64
		_ = catalog.DB.QueryRow(replay.CountQuery).Scan(&before)
		clock = clock.Add(time.Minute)
		arrivals, work, err := play(plan)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", name, want, err)
		}
		select {
		case <-work.Done():
		default:
			t.Fatalf("%s: a failed arrival leaves the work running", name)
		}
		if failure, interrupted := replicaFailure(work, "labels: context canceled"); failure != "replay: "+err.Error() || interrupted != "labels: context canceled" {
			t.Fatalf("%s: the arrival's error is the failure: %q (interrupted %q)", name, failure, interrupted)
		}
		if applied != (arrivals.report("replay-host")["arrivals"].([]replicaArrivalReport)[0].AppliedSeconds != nil) {
			t.Fatalf("%s: applied=%v expected", name, applied)
		}
		var after int64
		if _ = catalog.DB.QueryRow(replay.CountQuery).Scan(&after); changed != (after != before) {
			t.Fatalf("%s: changed=%v expected, count %d -> %d", name, changed, before, after)
		}
		return arrivals
	}
	// The same bytes again extend s1 by nothing: no arrival.
	failed("stale", schedule("extend", []string{"s1"}, "-proj/s1.jsonl", nil), "not a real arrival", false, false)
	// s1 is in the catalog, so it cannot be new; s2 is not, so it cannot extend.
	failed("not new", schedule("new", []string{"s1"}, "-proj/s1.jsonl", nil), "already in the catalog", false, false)
	failed("not extended", schedule("extend", []string{"s2"}, "-proj/s2.jsonl", nil), "already in the catalog", false, false)
	// An arrival that never brings a declared conversation fails after it.
	failed("missing", schedule("new", []string{"s2", "s7"}, "-proj/s2.jsonl", replicaLines(1)), "1 of 2 conversations", true, true)
	// s2 now exists with one line; the rest of it extends that conversation.
	// A later run stages with a later clock, as a later file write would.
	clock = clock.Add(time.Minute)
	extended, work, err := play(schedule("extend", []string{"s2"}, "-proj/s2.jsonl", nil))
	if err != nil || work.Err() != nil {
		t.Fatalf("an existing conversation extends: %v", err)
	}
	extendedReport := extended.report("replay-host")
	if entry, identity := extendedReport["arrivals"].([]replicaArrivalReport)[0], extendedReport["identity"].(map[string]any); *entry.IdentityMessagesBefore != 1 || *entry.IdentityMessagesAfter != 2 ||
		identity["present_before"] != 1 || identity["other_conversations_before"] != int64(1) || identity["other_conversations_after"] != int64(1) {
		t.Fatalf("an extension grows the declared conversation and leaves s1 alone: %+v %v", entry, identity)
	}
	// Unrelated sessions raise count_query but cannot stand in for the
	// declared ones: s5 is not the new s7, and s6 does not extend s1.
	wrong := failed("wrong new session", schedule("new", []string{"s7"}, "-proj/s5.jsonl", nil), "did not add to the declared conversations", false, true)
	if entry := wrong.report("replay-host")["arrivals"].([]replicaArrivalReport)[0]; *entry.CountAfter <= *entry.CountBefore || *entry.IdentityMessagesAfter != 0 {
		t.Fatalf("the refused arrival reports both counts: %+v", entry)
	}
	failed("wrong extended session", schedule("extend", []string{"s1"}, "-proj/s6.jsonl", nil), "did not add to the declared conversations", false, true)
	// A declared session that arrives beside an undeclared one is refused.
	beside := schedule("new", []string{"s8"}, "-proj/s8.jsonl", nil)
	beside.Arrivals[0].Files = append(beside.Arrivals[0].Files, replicaArrivalFile{Path: "-proj/s9.jsonl"})
	if identity := failed("undeclared beside", beside, "changed the undeclared conversations", true, true).report("replay-host")["identity"].(map[string]any); identity["other_conversations_before"] != int64(4) || identity["other_conversations_after"] != int64(5) {
		t.Fatalf("undeclared growth is reported: %v", identity)
	}
	if failure, interrupted := replicaFailure(t.Context(), "labels: boom"); failure != "labels: boom" || interrupted != "" {
		t.Fatal("without a failed arrival the work's own failure stands")
	}
	if requests := extendedReport["requests"].([]replicaArrivalRequest); requests == nil || len(requests) != 0 {
		t.Fatalf("without arrival demand no requests are recorded: %v", requests)
	}
	// Refused demand fails its applied arrival, cancels the work with that
	// cause, and stops the schedule before the next arrival.
	demand = func(context.Context, int) (uint64, error) { return 0, errors.New("work request refused (409 boom)") }
	refused := replicaTestReplay(replicaArrival{Name: "b1", Files: []replicaArrivalFile{{Path: "-proj/s3.jsonl", Lines: replicaLines(1)}}},
		replicaArrival{Name: "b2", Files: []replicaArrivalFile{{Path: "-proj/s3.jsonl"}}})
	refused.Identity.NativeIDs = []string{"s3"}
	stopped := failed("refused demand", refused, "arrival b1: recent demand: work request refused (409 boom)", true, true).report("replay-host")
	demand = nil
	if entries := stopped["arrivals"].([]replicaArrivalReport); entries[0].PendingAtApply == nil || entries[1].StartedSeconds != nil || len(stopped["requests"].([]replicaArrivalRequest)) != 0 {
		t.Fatalf("the refused arrival keeps its evidence and the next never starts: %+v %v", entries, stopped["requests"])
	}
	// The helper reports a refusal rather than failing the test: this server
	// runs no scheduled work.
	if _, err := replicaDemandRecent(server, config.APIToken, []string{"analysis.timing"}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refused demand is an error: %v", err)
	}
}

// Samples cover live descendants by name; retention is unavailable, never
// zero, without both complete ends.
func TestWorkReplicaMemory(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("physical footprint and process-tree reader are macOS only")
		}
		child := exec.Command("/bin/sleep", "30")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
		sample := replicaMemoryNow("work-start")
		if sample.Go["sys"] == 0 {
			t.Fatalf("this process's footprint and runtime: %+v", sample)
		}
		if !sample.Complete {
			t.Fatalf("native footprint observation was incomplete: %+v", sample)
		}
		if sample.Descendants["sleep"] != 1 || sample.DescendantsFootprint == nil || *sample.DescendantsFootprint == 0 || sample.TreeFootprint == nil || *sample.TreeFootprint != *sample.SelfFootprint+*sample.DescendantsFootprint {
			t.Fatalf("a live child is in the tree: %+v", sample)
		}
	})

	self := os.Getpid()
	for _, tc := range []struct {
		name, basis                                  string
		own, child                                   resourceCounters
		ownMemory, childMemory, treeMemory, complete bool
	}{
		{"known zero", resourceBasisKernel, counterMemory | counterResident, counterMemory, true, true, true, true},
		{"unknown self memory", resourceBasisKernel, counterResident, counterMemory, false, true, false, false},
		{"unknown child memory", resourceBasisKernel, counterMemory | counterResident, 0, true, false, false, false},
		{"unknown resident", resourceBasisKernel, counterMemory, counterMemory, true, true, true, false},
		{"runtime approximation", "go-runtime", counterMemory, counterMemory, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(pid int) (processUsage, error) {
				if pid == self {
					return processUsage{Known: tc.own}, nil
				}
				return processUsage{Known: tc.child}, nil
			}
			sample := replicaMemorySnapshot("test", tc.basis, read, []processNode{{PID: self + 1, Parent: self, Name: "worker"}})
			if (sample.SelfFootprint != nil) != tc.ownMemory || (sample.DescendantsFootprint != nil) != tc.childMemory ||
				(sample.TreeFootprint != nil) != tc.treeMemory || sample.Complete != tc.complete || sample.Go["sys"] == 0 {
				t.Fatalf("physical and runtime counter coverage: %+v", sample)
			}
			if sample.TreeFootprint != nil && *sample.TreeFootprint != 0 {
				t.Fatalf("known zero memory changed: %+v", sample)
			}
		})
	}
	memory := &replicaMemory{}
	memory.sample("startup")
	memory.begin(time.Now())
	memory.sample("work-start")
	if samples := memory.snapshot(); *samples[0].Seconds > 0 || samples[1].Seconds == nil {
		t.Fatalf("samples before work have negative seconds: %+v", samples)
	}
	at := func(label string, self, descendants uint64, complete bool) replicaMemorySample {
		return replicaMemorySample{Label: label, SelfFootprint: &self, DescendantsFootprint: &descendants, TreeFootprint: new(self + descendants), Complete: complete,
			Descendants: map[string]int{"pharos-labeler-ort": 1}, Go: map[string]uint64{"heap_inuse": self / 2}}
	}
	plan := replicaMemoryPlan{IntervalSeconds: 5, IdleSeconds: 60}
	samples := []replicaMemorySample{at("work-start", 100, 0, true), at("settled", 400, 300, true), at("idle", 300, 300, true), at("idle-released", 250, 300, true)}
	retention := replicaRetention(samples, plan)
	if retention["retained_bytes"] != int64(450) || retention["retained_descendants_bytes"] != int64(300) || retention["retained_self_bytes"] != int64(150) ||
		retention["retained_go_heap_inuse_bytes"] != int64(75) || retention["settled"] != uint64(700) {
		t.Fatalf("retention is the released tree less the tree before work: %v", retention)
	}
	samples[0].Complete = false
	if retention := replicaRetention(samples, plan); retention["retained_bytes"] != nil || retention["coverage"].(map[string]any)["complete"] != false {
		t.Fatalf("an incomplete end leaves retention unavailable: %v", retention)
	}
	for _, key := range []string{"retained_bytes", "retained_self_bytes", "retained_descendants_bytes", "retained_go_heap_inuse_bytes"} {
		if value, ok := replicaRetention(samples, plan)[key]; !ok || value != nil {
			t.Fatalf("%s is null, not absent, when unavailable: %v %v", key, value, ok)
		}
	}
	if retention := replicaRetention(samples, plan); retention["retained_bytes"] != nil {
		t.Fatalf("an incomplete end leaves retention unavailable: %v", retention)
	}
	if retention := replicaRetention(samples[1:], plan); retention["retained_bytes"] != nil || retention["work_start"] != nil {
		t.Fatalf("a missing end leaves retention unavailable: %v", retention)
	}
}
