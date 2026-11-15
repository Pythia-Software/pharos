package archive

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A startup upgrade finds its one-off work before running it, prices it by
// the rows it reads, and names it: an index this build adds to a large table,
// the statistics that index needs, and usage estimates cleared for a new
// estimator. Steps with nothing one-off are not given any.
func TestInitializePlanFindsOneOffWork(t *testing.T) {
	catalog, _ := testCatalog(t)
	execTx(t, catalog.DB,
		"DROP INDEX tool_calls_artifact_idx",
		"DELETE FROM sqlite_stat1 WHERE tbl IN ('tool_calls','model_requests','agent_sessions','agent_session_usage')",
		// A partial index's row counts only its entries; listed first, so a
		// read of any one row would take it.
		"INSERT INTO sqlite_stat1(tbl,idx,stat) VALUES('tool_calls','tool_calls_path_absolute_idx','3000 2'),('tool_calls','tool_calls_conversation_idx','2000000 40'),('model_requests','model_requests_timing_idx','1500000 30'),('agent_sessions',NULL,'60000'),('agent_session_usage',NULL,'90000')",
		"INSERT INTO meta(key,value) VALUES('usage_estimation_derivation_version','an-older-estimator') ON CONFLICT(key) DO UPDATE SET value=excluded.value",
	)
	tracker := catalog.startInitializeProgress()
	defer tracker.close()
	names := map[int][]string{}
	for i, step := range tracker.progress.Steps {
		for _, item := range step.OneOffs {
			names[i] = append(names[i], item.Name)
		}
	}
	if !slices.Equal(names[initStepTables], []string{"Build a new index on tool_calls"}) || tracker.progress.Steps[initStepTables].rows() != 2000000 {
		t.Fatalf("index work: %v, %d rows", names, tracker.progress.Steps[initStepTables].rows())
	}
	if !slices.Contains(names[initStepPlanner], "Gather statistics for tool_calls") {
		t.Fatalf("statistics work: %v", names)
	}
	// Statistics read every index of the table.
	for _, item := range tracker.progress.Steps[initStepPlanner].OneOffs {
		if item.Name == "Gather statistics for tool_calls" && (item.Kind != oneOffStatistics || item.Units < 10*float64(item.Rows)) {
			t.Fatalf("statistics priced by %+v", item)
		}
	}
	clear := tracker.progress.Steps[initStepDerivations]
	if !slices.Equal(names[initStepDerivations], []string{"Clear usage estimates for the new estimator"}) || clear.rows() != 2000000+1500000+60000+90000 {
		t.Fatalf("estimate clearing: %v, %d rows", names, clear.rows())
	}
	// Without earlier upgrades the default per-row range prices it.
	rate := initializeDefaultRates[oneOffScan]
	if clear.OneOffs[0].Kind != oneOffScan || clear.LowSeconds < float64(clear.rows())*rate[0] || clear.HighSeconds < float64(clear.rows())*rate[1] {
		t.Fatalf("estimate clearing priced at %.0f–%.0f s", clear.LowSeconds, clear.HighSeconds)
	}
	if len(tracker.progress.Steps[initStepTimings].OneOffs) != 0 {
		t.Fatalf("routine step with one-off work: %+v", tracker.progress.Steps[initStepTimings])
	}
}

// Initialize publishes each step while it runs, removes the file when it
// ends, and keeps each step's time for the next upgrade's estimate.
func TestInitializeReportsAndRecordsSteps(t *testing.T) {
	catalog, _ := testCatalog(t)
	path := filepath.Join(filepath.Dir(catalog.Path), initializeProgressName)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("progress file left behind: %v", err)
	}
	history := loadInitializeHistory(context.Background(), catalog.DB)
	if len(history.Runs) != 1 || len(history.Runs[0].Steps) != len(initializeSteps) {
		t.Fatalf("history: %+v", history)
	}
	for i, step := range history.Runs[0].Steps {
		if step.Name != initializeSteps[i] || step.Seconds <= 0 {
			t.Fatalf("step %d: %+v", i, step)
		}
	}
	// The next upgrade estimates every step from this one.
	next := catalog.startInitializeProgress()
	defer next.close()
	for _, step := range next.progress.Steps {
		if !step.Known || step.Basis != "earlier upgrades" {
			t.Fatalf("not estimated from history: %+v", step)
		}
	}
	next.begin(initStepTables)
	var published initializeProgress
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &published) != nil || published.Current != initStepTables || published.PID != os.Getpid() {
		t.Fatalf("published %s (%v)", data, err)
	}
	if progress, ok := readInitializeProgress(catalog.Path); !ok || progress.Steps[0].State != "running" {
		t.Fatalf("read back: %+v", progress)
	}
}

// The window's view counts down the steps left, rescaled once finished steps
// show how fast this drive is today, withdraws the countdown when a step
// runs well past its estimate, and gives none when a step has no basis.
func TestInitializeViewEstimatesTheRest(t *testing.T) {
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	step := func(name string, low, high float64) initializeStep {
		return initializeStep{Name: name, State: "pending", Known: true, LowSeconds: low, HighSeconds: high, Basis: "earlier upgrades", Weight: (low + high) / 2}
	}
	tracker := &initializeTracker{path: filepath.Join(t.TempDir(), initializeProgressName), started: start,
		planned: [][2]float64{{10, 20}, {60, 120}, {100, 200}},
		progress: initializeProgress{StartedAt: formatTime(start), Current: -1, Steps: []initializeStep{
			step("Update tables and indexes", 10, 20), step("Update request timings", 60, 120), step("Clear estimates", 100, 200),
		}}}
	tracker.progress.Steps[2].OneOffs = []initializeOneOff{{Name: "Clear usage estimates for the new estimator", Rows: 3_650_000}}
	tracker.progress.Steps[2].Basis = "earlier upgrades; table sizes"
	tracker.begin(0)
	tracker.progress.Steps[0].StartedAt = formatTime(start)
	tracker.stepStarted = start
	view := tracker.progress.view(start.Add(5 * time.Second))
	if view.Step != 1 || view.Steps != 3 || view.Remaining == nil || view.Remaining.Low != 165 || view.Remaining.High != 335 {
		t.Fatalf("first step: %+v %+v", view, view.Remaining)
	}
	if view.Label != "About 3–6 min left" || view.Basis != "Estimated from earlier upgrades and table sizes" {
		t.Fatalf("label %q, basis %q", view.Label, view.Basis)
	}
	// The bar lays the steps out by their planned estimates, the quick
	// first one at no less than the minimum; nothing is filled yet.
	if len(view.Segments) != 3 || view.Done != 0 || view.Segments[0].State != "running" {
		t.Fatalf("segments: %+v", view.Segments)
	}
	var whole float64
	for _, segment := range view.Segments {
		whole += segment.Fraction
	}
	if math.Abs(whole-1) > 1e-9 || view.Segments[0].Fraction < initializeMinimumSegment || view.Segments[2].Fraction <= view.Segments[1].Fraction {
		t.Fatalf("segment fractions: %+v", view.Segments)
	}
	// The drive runs steadily three times slower than planned. The first
	// step takes three times its estimate's middle: the rest triple.
	tracker.finishCurrent(start.Add(45 * time.Second))
	if got := tracker.progress.Steps[2]; got.LowSeconds != 300 || got.HighSeconds != 600 {
		t.Fatalf("rescaled: %+v", got)
	}
	second := start.Add(45 * time.Second)
	tracker.progress.Current, tracker.stepStarted = 1, second
	tracker.progress.Steps[1].State, tracker.progress.Steps[1].StartedAt = "running", formatTime(second)
	// The finished step fills its segment, which rescaling did not move.
	if again := tracker.progress.view(second.Add(time.Second)); again.Done != view.Segments[0].Fraction || again.Segments[2].Fraction != view.Segments[2].Fraction {
		t.Fatalf("after a step: %+v, done %v", again.Segments, again.Done)
	}
	view = tracker.progress.view(second.Add(361*time.Second + initializeOverrunSeconds*time.Second))
	if !view.Overrun || view.Remaining != nil || view.Label != "This step is taking longer than estimated" {
		t.Fatalf("overrun: %+v", view)
	}
	// The second step is three times slow too, measured against its planned
	// estimate rather than the tripled one it waited with: the last stays
	// tripled.
	tracker.finishCurrent(second.Add(270 * time.Second))
	if got := tracker.progress.Steps[2]; got.LowSeconds != 300 || got.HighSeconds != 600 {
		t.Fatalf("rescaled again: %+v", got)
	}
	tracker.progress.Current = 2
	tracker.progress.Steps[2].Known = false
	view = tracker.progress.view(start.Add(time.Minute))
	if view.Remaining != nil || !strings.HasPrefix(view.Label, "No estimate yet") || len(view.Work) != 1 || view.Work[0] != "Clear usage estimates for the new estimator (3.6M rows)" {
		t.Fatalf("unknown: %+v", view)
	}
}

// The broker shows the catalog upgrade a service start waits for in GET
// /api/app/operations, only while that start runs.
func TestBrokerOperationsShowTheCatalogUpgrade(t *testing.T) {
	broker := newBrokerOperations(filepath.Join(t.TempDir(), "operations.json"))
	start := broker.begin(OperationSpec{Kind: "service.start"}, "lib-a")
	start.Step("Upgrade the library's catalog", "Step 1 of 1: Update tables and indexes", -1)
	progress := initializeProgress{StartedAt: now(), Current: 0, Steps: []initializeStep{{Name: "Update tables and indexes", State: "running", StartedAt: now(), Known: true, LowSeconds: 60, HighSeconds: 120, Basis: "earlier upgrades"}}}
	broker.setCatalogUpgrade(start, &progress)
	entry := func() map[string]any {
		for _, record := range broker.records() {
			if record.Kind == "service.start" {
				return brokerOperationEntry(record)
			}
		}
		t.Fatal("no service start")
		return nil
	}
	view, ok := entry()["catalog_upgrade"].(initializeView)
	if !ok || view.StepName != "Update tables and indexes" || view.Remaining == nil {
		t.Fatalf("catalog upgrade: %+v", entry())
	}
	broker.setCatalogUpgrade(start, nil)
	if _, ok := entry()["catalog_upgrade"]; ok {
		t.Fatal("upgrade still shown once detached")
	}
	// The broker reads the service's progress file while it starts, and
	// detaches the upgrade once the file is gone: the upgrade ended.
	manager := &applicationManager{operations: broker}
	catalog := filepath.Join(t.TempDir(), "catalog.sqlite3")
	progress.PID = os.Getpid()
	data, _ := json.Marshal(progress)
	if err := os.WriteFile(filepath.Join(filepath.Dir(catalog), initializeProgressName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	named := manager.reportCatalogUpgrade(start, catalog, os.Getpid(), "")
	if named != "Step 1 of 1: Update tables and indexes" || entry()["catalog_upgrade"] == nil {
		t.Fatalf("while upgrading: %q, %+v", named, entry())
	}
	// A file another process left behind, without the lock, is stale.
	if stale := manager.reportCatalogUpgrade(start, catalog, os.Getpid()+1, named); stale != "" || entry()["catalog_upgrade"] != nil {
		t.Fatalf("stale file shown: %q", stale)
	}
	named = manager.reportCatalogUpgrade(start, catalog, os.Getpid(), "")
	_ = os.Remove(filepath.Join(filepath.Dir(catalog), initializeProgressName))
	named = manager.reportCatalogUpgrade(start, catalog, os.Getpid(), named)
	if named != "" || entry()["catalog_upgrade"] != nil {
		t.Fatalf("after the upgrade: %q, %+v", named, entry())
	}
	if step, _ := entry()["step"].(map[string]any); step == nil || step["name"] != "Start the library's service" {
		t.Fatalf("start's step after the upgrade: %+v", entry()["step"])
	}
	start.Finish(nil)
}

// A kind of one-off work this library has done before is priced from its
// time past the step's routine estimate; other kinds keep the default.
func TestInitializeRatesFromHistory(t *testing.T) {
	routine := make([]initializeEstimate, len(initializeSteps))
	routine[initStepTables] = initializeEstimate{known: true, low: 1, high: 3}
	history := initializeHistory{Runs: []initializeRun{{Steps: []initializeRunStepLog{
		{Name: initializeSteps[initStepTables], Seconds: 30, OneOffs: []initializeOneOff{{Kind: oneOffIndex, Rows: 1_000_000, Units: 1_000_000}}},
		// Mixed work says nothing about either kind.
		{Name: initializeSteps[initStepPlanner], Seconds: 90, OneOffs: []initializeOneOff{{Kind: oneOffIndex, Units: 1}, {Kind: oneOffScan, Units: 1}}},
	}}}}
	rates := history.rates(routine)
	if got := rates[oneOffIndex]; math.Abs(got[0]-0.7*28e-6) > 1e-12 || math.Abs(got[1]-3*28e-6) > 1e-12 {
		t.Fatalf("index rate %v", got)
	}
	if rates[oneOffScan] != initializeDefaultRates[oneOffScan] || rates[oneOffStatistics] != initializeDefaultRates[oneOffStatistics] {
		t.Fatalf("defaults changed: %v", rates)
	}
}
