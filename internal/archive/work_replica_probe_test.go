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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// replicaScopedKinds are the kinds whose classic pass takes a population.
// Every other classic pass drains its whole queue, so a workload must demand
// those kinds from a phase without a window: otherwise the classic arm would
// do more work than the scheduled arm was asked for.
var replicaScopedKinds = []string{"analysis.labels"}

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
		Name    string         `json:"name"`
		Clock   string         `json:"clock"`
		Stages  []string       `json:"stages"`
		Phases  []replicaPhase `json:"phases"`
		Product struct {
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
	// Opening initializes/migrates the catalog: startup is measured apart
	// from the work it precedes, so a fixed cost cannot dilute a stage.
	opening := replicaCPUNow()
	c, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.evidenceOnly = true
	c.now = func() time.Time { return clock }
	server := testServer(t, config, c)
	checks, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: config.CatalogPath}).String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer checks.Close()
	startup := opening.until(replicaCPUNow())

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
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			stopPoll()
			<-pollDone
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
		"expected_sha256": descriptor.Product.Expected, "min_expected_rows": *descriptor.Product.MinRows,
		"durability": "production catalog durability, labeling ledger synchronous=" + replicaLedgerSync + " and drive flushes restored",
		"labels": map[string]any{"enabled": labels.Enabled, "variant": labels.Variant, "backend": labels.Backend,
			"threads": labels.Threads, "workers": labels.Workers, "model": filepath.Base(labels.ModelDir)}}
	save := func() {
		mu.Lock()
		report["observations"] = append([]observation(nil), observations...)
		mu.Unlock()
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(root, "probe-report.json"), append(data, '\n'), 0o600)
		}
		if err != nil {
			t.Error(err)
		}
	}
	// A failed stage still leaves its private report, unsettled.
	fail := func(format string, args ...any) {
		stop()
		report["failure"] = fmt.Sprintf(format, args...)
		save()
		t.Fatalf(format, args...)
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
		return step
	}
	steps := []map[string]any{}
	report["steps"] = &steps
	ctx := t.Context()
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
			settle(ctx, kind)
		}
	default:
		// Submit every phase as the product would, then wait for all of
		// them: recent demand and the historical remainder share the engine.
		ids := make([]uint64, len(descriptor.Phases))
		for i, phase := range descriptor.Phases {
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
			ids[i] = uint64(parsed)
		}
		ended := map[uint64]map[string]any{}
		for len(ended) < len(ids) {
			status, value, err := probeJSON(server, config.APIToken, http.MethodGet, "/api/work", nil)
			if err != nil || status != http.StatusOK {
				fail("work state: %d %v", status, err)
			}
			state, _ := value["state"].(map[string]any)
			for _, list := range []string{"requests", "ended"} {
				items, _ := state[list].([]any)
				for _, item := range items {
					facts, _ := item.(map[string]any)
					number, _ := facts["id"].(json.Number)
					id, _ := number.Int64()
					terminal := facts["state"] == "succeeded" || facts["state"] == "failed" || facts["state"] == "cancelled" || facts["state"] == "interrupted"
					if i := slices.Index(ids, uint64(id)); i >= 0 && terminal && ended[uint64(id)] == nil {
						facts["ended_seconds"] = time.Since(started).Seconds()
						facts["phase"] = descriptor.Phases[i].Name
						ended[uint64(id)] = facts
					}
				}
			}
			if len(ended) < len(ids) {
				select {
				case <-ctx.Done():
					fail("work requests did not end: %v", ctx.Err())
				case <-time.After(time.Second):
				}
			}
		}
		requests := []any{}
		for _, id := range ids {
			requests = append(requests, ended[id])
		}
		engine["requests"] = requests
		_, value, _ := probeJSON(server, config.APIToken, http.MethodGet, "/api/work", nil)
		state, _ := value["state"].(map[string]any)
		engine["tasks"], engine["authority"] = state["tasks"], state["authority"]
		engine["decisions_total"] = state["decisions_total"]
		if _, ledger, err := probeJSON(server, config.APIToken, http.MethodGet, "/api/operations/resources", nil); err == nil {
			report["task_resources"] = ledger
		}
		for _, facts := range ended {
			if facts["state"] != "succeeded" {
				fail("work request %v ended %v: %v", facts["id"], facts["state"], facts["reason"])
			}
		}
		steps = append(steps, map[string]any{"name": "scheduled", "wall_seconds": time.Since(started).Seconds()})
		for _, kind := range replicaKinds {
			if slices.Contains(units, kind) {
				settle(ctx, kind)
			}
		}
	}
	execution := work.until(replicaCPUNow())
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

// replicaClassicKind runs one kind through the classic domain methods until
// its own queue is empty. Only labels take a population (replicaScopedKinds):
// the other classic passes drain their whole queues, as the classic service
// does, and the descriptor demands them over the whole corpus.
func replicaClassicKind(ctx context.Context, c *Catalog, config Config, clock time.Time, kind string, population replicaKindPopulation) error {
	switch kind {
	case "maintenance.parallelism":
		return replicaStage(ctx, c, config, clock, "timing")
	case "analysis.authorship":
		return replicaStage(ctx, c, config, clock, "authorship-estimates")
	case "analysis.labels":
		if population.whole {
			return replicaStage(ctx, c, config, clock, "labels")
		}
		// An empty restriction would label every conversation.
		if len(population.ids) == 0 {
			return nil
		}
		// A restricted run never reports Complete; its pending checks decide.
		stats, err := c.RunLabels(ctx, config.Labels, LabelsRunOptions{Conversations: population.ids})
		if err == nil && (stats.Cut || stats.MessagesFailed > 0 || stats.ConversationsFailed > 0) {
			err = fmt.Errorf("labels left %d messages and %d conversations failed", stats.MessagesFailed, stats.ConversationsFailed)
		}
		return err
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
		if err != nil {
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
		{Name: "history", Action: "backfill", Kinds: []string{"maintenance.parallelism"}}}
	windows := [][2]time.Time{recent, {}}
	got, err := replicaKindPopulations(phases, windows, [][]string{{"b", "a"}, {"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if labels := got["analysis.labels"]; labels.whole || !slices.Equal(labels.ids, []string{"a", "b"}) {
		t.Fatalf("labels ran over %+v, not only the recent window", labels)
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
	// An empty window is an empty population, never the whole corpus.
	got, err = replicaKindPopulations([]replicaPhase{{Name: "recent", Action: "recent", Kinds: []string{"analysis.labels"}}}, windows[:1], [][]string{{}})
	if err != nil || got["analysis.labels"].whole || len(got["analysis.labels"].ids) != 0 {
		t.Fatalf("empty window: %+v %v", got, err)
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
