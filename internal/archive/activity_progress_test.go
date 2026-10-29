package archive

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStageEstimateLearnsCurrentPaceAndHandlesPauses(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	o := &progressObservation{}
	w := activityWork{Phase: "indexing", Unit: "conversation", Total: 100}
	if got := o.estimate(w, start); got.State != "learning" || got.HighSeconds != 0 {
		t.Fatalf("cold start invented an ETA: %+v", got)
	}
	for _, seconds := range []int{4, 8, 12} {
		w.Done = float64(seconds)
		o.estimate(w, start.Add(time.Duration(seconds)*time.Second))
	}
	got := o.estimate(w, start.Add(12*time.Second))
	if got.State != "estimating" || got.Rate != 1 || got.LowSeconds > 88 || got.HighSeconds < 88 {
		t.Fatalf("one conversation per second should cover 88 seconds left: %+v", got)
	}
	// A slow current conversation should lower the observed rate, then withdraw
	// the countdown, never display an overdue timestamp or creep toward 100%.
	slow := o.estimate(w, start.Add(30*time.Second))
	if slow.Rate >= got.Rate || slow.HighSeconds <= got.HighSeconds {
		t.Fatalf("did not respond to slower work: fast=%+v slow=%+v", got, slow)
	}
	if got := o.estimate(w, start.Add(60*time.Second)); got.State != "stalled" || got.HighSeconds != 0 {
		t.Fatalf("stale pace retained an ETA: %+v", got)
	}
	w.Done = 60
	if got := o.estimate(w, start.Add(61*time.Second)); got.State != "estimating" {
		t.Fatalf("progress did not resume: %+v", got)
	}
	w.Done = w.Total
	if got := o.estimate(w, start.Add(62*time.Second)); got.State != "finishing" || got.HighSeconds != 0 {
		t.Fatalf("all counted work should await the next stage: %+v", got)
	}
}

func TestStageEstimateUsesWorkSizeAndRequiresComparableHistory(t *testing.T) {
	start := time.Now()
	history := []workTiming{{Units: 100, Seconds: 100}, {Units: 200, Seconds: 200}, {Units: 400, Seconds: 400}, {Units: 100, Seconds: 10000}}
	estimate := func(total float64) activityEstimate {
		return (&progressObservation{history: history}).estimate(activityWork{Unit: "conversation", Total: total}, start)
	}
	small, large := estimate(100), estimate(400)
	if small.State != "estimating" || large.State != "estimating" || large.HighSeconds != small.HighSeconds*4 {
		t.Fatalf("workload not scaled: small=%+v large=%+v", small, large)
	}
	if small.Rate != 1 {
		t.Fatalf("outlier overwhelmed median throughput: %+v", small)
	}
	if got := estimate(100000); got.State != "learning" {
		t.Fatalf("tiny update was extrapolated to a rebuild: %+v", got)
	}
	if got := (&progressObservation{history: history[:2]}).estimate(activityWork{Total: 100}, start); got.State != "learning" {
		t.Fatalf("two past runs were presented as a reliable estimate: %+v", got)
	}
	// A very fast history must give way to this run's actual slower pace.
	o := &progressObservation{history: []workTiming{{Units: 100, Seconds: 1}, {Units: 100, Seconds: 1}, {Units: 100, Seconds: 1}}}
	w := activityWork{Total: 100}
	o.estimate(w, start)
	for _, seconds := range []int{10, 20, 30} {
		w.Done = float64(seconds)
		got := o.estimate(w, start.Add(time.Duration(seconds)*time.Second))
		if seconds == 30 && got.Rate != 1 {
			t.Fatalf("history outweighed observed performance: %+v", got)
		}
	}
	for _, priorSeconds := range []float64{1, 10000} {
		o := &progressObservation{history: []workTiming{{Units: 100, Seconds: priorSeconds}, {Units: 100, Seconds: priorSeconds}, {Units: 100, Seconds: priorSeconds}}}
		w := activityWork{Total: 100}
		o.estimate(w, start)
		for _, seconds := range []int{4, 8} {
			w.Done = float64(seconds)
			got := o.estimate(w, start.Add(time.Duration(seconds)*time.Second))
			if seconds == 8 && (got.Rate < 0.875 || got.Rate > 1.25 || got.LowSeconds > 92 || got.HighSeconds < 92) {
				t.Fatalf("outdated prior still dominates a measured run: %+v", got)
			}
		}
	}
}

func TestStageEstimateDoesNotCountResumedWorkOrUnknownTotals(t *testing.T) {
	start := time.Now()
	o := &progressObservation{}
	w := activityWork{Unit: "conversation", Done: 800, Total: 1000}
	o.estimate(w, start)
	for _, seconds := range []int{4, 8, 12} {
		w.Done = 800 + float64(seconds)
		o.estimate(w, start.Add(time.Duration(seconds)*time.Second))
	}
	if got := o.estimate(w, start.Add(12*time.Second)); got.Rate != 1 {
		t.Fatalf("included previously completed work in throughput: %+v", got)
	}
	w.Total = 2000
	if got := o.estimate(w, start.Add(14*time.Second)); got.State != "learning" {
		t.Fatalf("changed workload retained an old rate: %+v", got)
	}
	w.Total = 0
	if got := o.estimate(w, start.Add(16*time.Second)); got.State != "unknown" || got.HighSeconds != 0 {
		t.Fatalf("unknown denominator generated an ETA: %+v", got)
	}
	w.Done, w.Total = 0, 100
	if got := o.estimate(w, start.Add(18*time.Second)); got.State != "learning" {
		t.Fatalf("reset counter retained an old rate: %+v", got)
	}
}

func TestMeasuredHistoryPersistsAndSeparatesWork(t *testing.T) {
	useHost(t, "timing-host")
	catalog, _ := testCatalog(t)
	s := &Server{Catalog: catalog}
	start := time.Now().Add(-time.Minute)
	w := activityWork{Phase: "indexing", Unit: "conversation group", Model: "codex:v1", Done: 100, Total: 100, StartedAt: formatTime(start)}
	for range 22 {
		s.recordWorkTiming("index", w, start.Add(50*time.Second))
	}
	key := workTimingKey("index", w)
	if got := (&Server{Catalog: catalog}).workTimings(key); len(got) != 20 || got[0].Units != 100 {
		t.Fatalf("bounded persisted workload history: %+v", got)
	}
	w.Done = 90
	s.recordWorkTiming("index", w, start.Add(time.Minute))
	w.Done, w.Total = 0, 0
	s.recordWorkTiming("index", w, start.Add(time.Minute))
	if got := len(s.workTimings(key)); got != 20 {
		t.Fatalf("incomplete/no-op stages recorded: %d", got)
	}
	for _, kind := range []string{"capture", "automatic-sync"} {
		if got := s.workTimings(workTimingKey(kind, w)); len(got) != 0 {
			t.Fatalf("%s reused index timings", kind)
		}
	}
	w.Model = "codex:v2"
	if got := s.workTimings(workTimingKey("index", w)); len(got) != 0 {
		t.Fatal("new extractor reused old timings")
	}
	useHost(t, "another-host")
	w.Model = "codex:v1"
	if got := s.workTimings(workTimingKey("index", w)); len(got) != 0 {
		t.Fatal("another Mac reused timings")
	}
}

func TestActivityEstimatesResetWithStageSourceAndRun(t *testing.T) {
	s := &Server{}
	start := time.Now()
	activities := []libraryActivity{{ID: "one", Kind: "index", Work: &activityWork{Phase: "indexing", Source: "claude", Unit: "conversation", Total: 100}}}
	for _, seconds := range []int{0, 4, 8, 12} {
		activities[0].Work.Done = float64(seconds)
		s.estimateActivities(activities, start.Add(time.Duration(seconds)*time.Second))
	}
	if activities[0].Estimate.State != "estimating" {
		t.Fatalf("never learned: %+v", activities[0])
	}
	// Without an operation forecast, the completion time is the stage's.
	if activities[0].EstimatedCompletionAt == "" || activities[0].EstimatedCompletionScope != "stage" {
		t.Fatalf("stage completion: %q, scope %q", activities[0].EstimatedCompletionAt, activities[0].EstimatedCompletionScope)
	}
	activities[0].Work.Source = "codex"
	s.estimateActivities(activities, start.Add(14*time.Second))
	if activities[0].Estimate.State != "learning" || len(s.estimator.observations) != 1 {
		t.Fatal("another source inherited throughput")
	}
	activities[0].Work.Phase = "waiting for catalog"
	activities[0].Work.Total = 0
	s.estimateActivities(activities, start.Add(16*time.Second))
	if activities[0].Progress != nil || !strings.Contains(activities[0].Estimate.Basis, "write lock") {
		t.Fatalf("waiting phase: %+v", activities[0])
	}
	activities[0].Stopping = true
	s.estimateActivities(activities, start.Add(18*time.Second))
	if activities[0].Estimate.State != "stopping" {
		t.Fatal("stop was not reflected")
	}
	s.estimateActivities(nil, start.Add(20*time.Second))
	if len(s.estimator.observations) != 0 {
		t.Fatal("completed jobs retain observations")
	}
}

func TestCaptureProgressUsesCurrentCopyBytes(t *testing.T) {
	result := CaptureSourceResult{Name: "codex", Kind: "codex", captureRunRecord: captureRunRecord{
		Phase: "copying files", FilesCopied: 1, FilesPlanned: 100, BytesCopied: 900, BytesPlanned: 1000,
	}}
	w := captureWork(result)
	activities := []libraryActivity{{ID: "capture", Kind: "capture", Work: &w}}
	(&Server{}).estimateActivities(activities, time.Now())
	if *activities[0].Progress != 0.9 {
		t.Fatal("counted files rather than bytes")
	}
	result.Phase = "copying database snapshots"
	w = captureWork(result)
	(&Server{}).estimateActivities(activities, time.Now())
	if activities[0].Progress != nil || !strings.Contains(activities[0].Estimate.Basis, "snapshots") {
		t.Fatal("file copy progress leaked into snapshots")
	}
}

func TestIndexProgressWeightsUnequalTranscripts(t *testing.T) {
	// Nine tiny transcripts are 90% of the conversations but only 9% of
	// the input. The remaining large one must dominate the remaining time.
	w := activityWork{Phase: "indexing", Unit: "conversation group", Done: 9, Total: 10,
		Size: &activitySize{Unit: "byte", Done: 90, Total: 1000}}
	a := []libraryActivity{{ID: "index", Kind: "index", Work: &w}}
	s := &Server{}
	s.estimateActivities(a, time.Now())
	if *a[0].Progress != 0.09 || a[0].Estimate.Unit != "byte" {
		t.Fatalf("ignored transcript sizes: %+v", a[0])
	}
	o := &progressObservation{history: []workTiming{{Units: 1000, Seconds: 100}, {Units: 1000, Seconds: 100}, {Units: 1000, Seconds: 100}}}
	if got := o.estimate(w, time.Now()); got.LowSeconds > 91 || got.HighSeconds < 91 || got.Rate != 10 {
		t.Fatalf("estimate should cover 910 bytes at 10/sec: %+v", got)
	}
	for _, test := range []struct {
		parts  []sourcePart
		amount float64
		unit   string
	}{
		{[]sourcePart{{item: "/session.jsonl", size: 123}, {item: "group:session", size: 99}}, 123, "byte"},
		{[]sourcePart{{item: "sqlite:db:sessions:one", size: 10, sizeUnit: "message"}, {item: "sqlite:db:sessions:two", size: 20, sizeUnit: "message"}}, 30, "message"},
		{[]sourcePart{{item: "/session.jsonl", size: 10}, {item: "sqlite:db:sessions:one", size: 10, sizeUnit: "message"}}, 0, ""},
		{[]sourcePart{{item: "/session.jsonl", size: 10}, {item: "sqlite:/requests.db", size: 4096}}, 10, "byte"},
	} {
		if amount, unit := partWorkSize(test.parts); amount != test.amount || unit != test.unit {
			t.Fatalf("mixed size units: %v %s, want %v %s", amount, unit, test.amount, test.unit)
		}
	}
}

func TestLibraryUpdateReportsMeasuredCaptureAndIndexStages(t *testing.T) {
	useHost(t, "host-a")
	oldCapture, oldParse := captureFault, parseHook
	t.Cleanup(func() { captureFault, parseHook = oldCapture, oldParse })
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	if err := os.MkdirAll(server.Config().CaptureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	copying, parsing := make(chan struct{}), make(chan struct{})
	copyRelease, parseRelease := make(chan struct{}), make(chan struct{})
	var copyOnce, parseOnce, releaseCopy, releaseParse sync.Once
	t.Cleanup(func() { releaseCopy.Do(func() { close(copyRelease) }); releaseParse.Do(func() { close(parseRelease) }) })
	captureFault = func(stage, _ string) error {
		if stage == "copy" {
			copyOnce.Do(func() { close(copying); <-copyRelease })
		}
		return nil
	}
	parseHook = func(string) { parseOnce.Do(func() { close(parsing); <-parseRelease }) }
	response := httptest.NewRecorder()
	server.startRecovery(response, httptest.NewRequest("POST", "/api/library/update", nil), map[string]any{})
	if response.Code != 202 {
		t.Fatalf("start update: %s", response.Body.String())
	}
	wait := func(ready chan struct{}) {
		t.Helper()
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			t.Fatal("stage did not start")
		}
	}
	wait(copying)
	activity := server.libraryStatus()["activities"].([]libraryActivity)[0]
	if activity.Work == nil || activity.Work.Phase != "copying files" || activity.Work.Total <= 0 || activity.Work.Unit != "byte" || activity.Work.Kind != "capture" {
		t.Fatalf("Capture & index did not expose its copy workload: %+v", activity.Work)
	}
	releaseCopy.Do(func() { close(copyRelease) })
	wait(parsing)
	activity = server.libraryStatus()["activities"].([]libraryActivity)[0]
	if activity.Work == nil || activity.Work.Phase != "indexing" || activity.Work.Total != 1 || activity.Work.Done != 0 || activity.Work.Size == nil || activity.Work.Size.Total <= 0 || activity.Work.Kind != "index" {
		t.Fatalf("Capture & index did not expose its indexing workload: %+v", activity.Work)
	}
	if activity.Progress == nil || *activity.Progress != 0 || activity.Estimate.State != "learning" {
		t.Fatalf("copy progress or ETA leaked into indexing: %+v", activity)
	}
	releaseParse.Do(func() { close(parseRelease) })
}

func TestIndexProgressCountsOnlySuccessfulRecords(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	source := stableCodexSource(t, 2)
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`CREATE TRIGGER reject_workspace BEFORE INSERT ON workspaces BEGIN SELECT RAISE(ABORT, 'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	handled, reported := 0, 0
	ctx := withIngestMode(t.Context(), ingestMode{processed: func([]sourcePart) { handled++ }})
	progress := func(_ string, _, _, _, _, processed int) { reported = processed }
	result := catalog.IngestContext(ctx, adapter, progress)
	if result.Error == nil || result.Processed != 0 || handled != 0 || reported != 0 {
		t.Fatalf("failed record advanced progress: %+v, handled=%d reported=%d", result, handled, reported)
	}
	if _, err := catalog.DB.Exec("DROP TRIGGER reject_workspace"); err != nil {
		t.Fatal(err)
	}
	result = catalog.IngestContext(ctx, adapter, progress)
	if result.Error != nil || result.Processed != 1 || handled != 1 || reported != 1 {
		t.Fatalf("successful retry did not advance progress once: %+v, handled=%d reported=%d", result, handled, reported)
	}
}

func TestIndexProgressMeasuresDatabaseSessionsByMessages(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	dir := filepath.Join(t.TempDir(), "conductor")
	execute := conductorFixture(t, dir)
	execute(addConductorSession("small", 1)...)
	execute(addConductorSession("large", 4)...)
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	runCapture(t, config)
	targets, err := captureTargets(config.CaptureRoot, nil, false, nil)
	if err != nil || len(targets) != 1 {
		t.Fatalf("capture targets: %v %v", targets, err)
	}
	needed, model, size, ok := catalog.captureIndexWorkMeasured(t.Context(), targets[0])
	if !ok || needed != 2 || size == nil || size.Unit != "message" || size.Total != 5 {
		t.Fatalf("database workload: needed=%d size=%+v ok=%v", needed, size, ok)
	}
	meter := &indexProgressMeter{needed: needed, model: model, size: size}
	run := &SyncRun{}
	result := catalog.IndexCapture(meter.context(t.Context()), targets[0], func(phase string, _, _, _, _, processed int) {
		meter.report(run, phase, targets[0].label(), processed)
		if phase == "indexing" && processed == 1 && (run.Work.Size.Done <= 0 || run.Work.Size.Done >= 5) {
			t.Fatalf("database progress lost message weights: %+v", run.Work.Size)
		}
	})
	if result.Error != nil || meter.processed != 2 || meter.size.Done != 5 {
		t.Fatalf("database progress: %+v, processed=%d size=%+v", result, meter.processed, meter.size)
	}
}

func TestIncrementalWorkLearnsWithoutStatusPollingAndRetainsFeatures(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	run := server.startRunNamed(automaticRunKind, []string{"codex"})
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	meter := &runWorkMeter{server: server, runID: run.ID, kind: "sync", clock: func() time.Time { return clock }}
	t.Cleanup(func() { meter.release(false) })
	load := 2.5
	work := activityWork{Phase: "indexing", Source: "codex", Unit: "byte", Total: 100, TotalKnown: true, Kind: "incremental", Model: "codex:test:changes",
		Features: workFeatures{Groups: 4, Bytes: 100, SinceLastIndexSeconds: 300, LoadOneMinute: &load}}
	for _, seconds := range []int{0, 4, 8, 12} {
		clock = time.Date(2026, 10, 1, 0, 0, seconds, 0, time.UTC)
		work.Done = float64(seconds)
		meter.report(work)
	}
	activities := server.libraryActivities()
	server.estimateActivities(activities, clock)
	if len(activities) != 1 || activities[0].Estimate.State != "estimating" || activities[0].Estimate.Rate != 1 {
		t.Fatalf("worker events did not train the estimator before the first status request: %+v", activities)
	}
	// Interrupting a stage preserves measurements without treating it as a
	// successfully completed workload for future ETA priors.
	stages := meter.finish(false)
	if len(stages) != 1 || stages[0].Completed || stages[0].Done != 12 || stages[0].Features.Bytes != 100 {
		t.Fatalf("partial stage: %+v", stages)
	}
	if history := server.workTimings(workTimingKey("incremental", work)); len(history) != 0 {
		t.Fatalf("partial stage became a completed sample: %+v", history)
	}
	clock = clock.Add(time.Second)
	work.Done = 0
	meter.report(work)
	clock = clock.Add(20 * time.Second)
	work.Done = 100
	meter.report(work)
	stages = meter.finish(true)
	history := server.workTimings(workTimingKey("incremental", work))
	if len(stages) != 1 || !stages[0].Completed || len(history) != 1 || history[0].Seconds != 20 || history[0].Features.Groups != 4 || history[0].Features.LoadOneMinute == nil || *history[0].Features.LoadOneMinute != load {
		t.Fatalf("completed stage lost workload or conditions: stages=%+v history=%+v", stages, history)
	}
	// A retry resumes cumulative source counters, but its clock starts after
	// the wait. Train only that segment's work, including byte-sized progress.
	for _, sized := range []bool{false, true} {
		segment := activityWork{Phase: "indexing", Source: "resumed", Unit: "byte", Total: 100, TotalKnown: true, Kind: "incremental", Model: "resume"}
		if sized {
			segment.Unit, segment.Total, segment.Size = "conversation group", 10, &activitySize{Unit: "byte", Total: 100}
		}
		meter.report(segment)
		clock = clock.Add(90 * time.Second)
		if sized {
			segment.Done, segment.Size.Done = 9, 90
		} else {
			segment.Done = 90
		}
		meter.report(segment)
		meter.report(activityWork{Phase: "waiting for catalog", Source: "resumed"})
		clock = clock.Add(30 * time.Second)
		meter.report(segment)
		clock = clock.Add(10 * time.Second)
		if sized {
			segment.Done, segment.Size.Done = 10, 100
		} else {
			segment.Done = 100
		}
		meter.report(segment)
		stages = meter.finish(true)
		history = server.workTimings(workTimingKey("incremental", segment))
		last := history[len(history)-1]
		if len(stages) != 3 || stages[2].Done != 10 || stages[2].Total != 10 || last.Units != 10 || last.Seconds != 10 {
			t.Fatalf("resumed counters inflated throughput (sized=%v): stages=%+v timing=%+v", sized, stages, last)
		}
	}

	// A subsequent analysis must publish its own progress rather than inheriting
	// the transcript size or source of the stage that just ended.
	meter.model = "analysis-v1:git"
	meter.report(activityWork{Phase: "checking Git history", Unit: "workspace", Total: 8, TotalKnown: true})
	activities = server.libraryActivities()
	if activities[0].Work.Phase != "checking Git history" || activities[0].Work.Source != "" || activities[0].Work.Size != nil || activities[0].Work.Kind != "analysis" || activities[0].Work.Model != "analysis-v1" {
		t.Fatalf("stage handoff: %+v", activities[0].Work)
	}
	empty := activityWork{Phase: "indexing", Unit: "conversation group", TotalKnown: true}
	if got := (&progressObservation{}).estimate(empty, clock); got.State == "unknown" {
		t.Fatalf("known empty workload described as undiscovered: %+v", got)
	}
}

func TestOperationForecastComposesDependenciesAndUnknownSteps(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	op := &operationForecast{started: at}
	for _, item := range []struct {
		id      string
		seconds float64
		after   []string
	}{
		{"index", 20, nil}, {"git", 100, []string{"index"}}, {"tools", 40, []string{"index"}}, {"findings", 10, []string{"git", "tools"}},
	} {
		step := forecastStep{ID: item.id, Model: "v1", Label: item.id, State: "pending", Inventory: true, After: item.after}
		op.steps = append(op.steps, step)
		step.State, step.Seconds = "complete", item.seconds
		op.history = append(op.history, forecastRun{State: "complete", Steps: []forecastStep{step}})
	}
	got := op.estimate(at, false)
	if got.Point != 130 || got.Remaining != 4 || got.State != "provisional" || got.LowSeconds >= 130 || got.HighSeconds <= 130 {
		t.Fatalf("should use the longer concurrent branch and broad sparse-history bounds: %+v", got)
	}
	op.steps = append(op.steps, forecastStep{ID: "new", Label: "New analysis", State: "pending", After: []string{"findings"}})
	got = op.estimate(at, false)
	if got.State != "partial" || got.Point != 0 || got.HighSeconds != 0 || got.Predicted != 4 || len(got.Unknown) != 1 {
		t.Fatalf("a partial path became a whole-operation ETA: %+v", got)
	}
	op.skip("new")
	op.steps[0].State, op.steps[0].Started = "running", at
	got = op.estimate(at.Add(5*time.Minute), false)
	if got.Point <= 110 || got.HighSeconds <= got.Point {
		t.Fatalf("overrunning stage counted through zero: %+v", got)
	}
	if got = op.estimate(at, true); got.State != "stopping" || got.Point != 0 {
		t.Fatalf("stop retained ETA: %+v", got)
	}
}

func TestOperationModelLearnsVolumeAndLoadWithoutMixingVersions(t *testing.T) {
	history := []forecastRun{}
	for i := range 60 {
		groups, load := float64(5+i%10*10), float64(1+i/10)
		step := forecastStep{ID: "source:a", Model: "codex:v1:changes", State: "complete", Inventory: true,
			Features: workFeatures{Groups: groups, Bytes: groups * 1000, BytesBasis: "transcript", LoadOneMinute: &load},
			Seconds:  2 * (1 + groups) * (1 + load)}
		history = append(history, forecastRun{State: "complete", Steps: []forecastStep{step}})
	}
	low, high := 1.5, 5.5
	target := forecastStep{ID: "source:a", Model: "codex:v1:changes", Inventory: true, Features: workFeatures{Groups: 40, Bytes: 40_000, BytesBasis: "transcript", LoadOneMinute: &low}}
	fast := predictStep(history, target)
	target.Features.LoadOneMinute = &high
	slow := predictStep(history, target)
	if slow.point < fast.point*1.8 || fast.point < 150 || fast.point > 280 || slow.low > slow.point || slow.high < slow.point {
		t.Fatalf("did not learn machine load at equal volume: fast=%+v slow=%+v", fast, slow)
	}
	target.Model = "codex:v2:changes"
	if got := predictStep(history, target); got.point != 0 {
		t.Fatalf("extractor versions shared a fit: %+v", got)
	}
	target.Model, target.Features.BytesBasis = "codex:v1:changes", "message_text"
	if got := predictStep(history, target); got.point != 0 {
		t.Fatalf("mixed file bytes and decoded database text: %+v", got)
	}
	target.Features = workFeatures{}
	if got := predictStep(history, target); got.point != 0 {
		t.Fatalf("a known empty inventory borrowed nonempty duration: %+v", got)
	}
	// Interrupted work is never a completed runtime observation.
	history[0].State = "interrupted"
	target.Inventory = false
	target.Model = "codex:v1:changes"
	if got := predictStep(history[:1], target); got.point != 0 {
		t.Fatalf("censored run trained a fit: %+v", got)
	}
}

func TestOperationForecastPersistsWorkerSnapshotsAndEvaluatesOnlyCompletedRuns(t *testing.T) {
	useHost(t, "forecast-host")
	catalog, config := testCatalog(t)
	s := testServer(t, config, catalog)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, state := range []string{"complete", "complete", "interrupted"} {
		run := s.startRunNamed(manualSyncRunKind, nil)
		op := s.beginOperation(run.ID, manualSyncRunKind)
		op.started, op.clock = at, func() time.Time { return at }
		op.add("index", "v1", "Index", workFeatures{Groups: 100}, true)
		op.checkpoint("launch")
		op.begin("index")
		at = at.Add(20 * time.Second)
		op.observe(&activityWork{Phase: "indexing", Unit: "group", Done: 10, Total: 100}, at)
		at = at.Add(80 * time.Second)
		op.end("index", state == "complete")
		op.finish(state)
		op.finish(state) // terminal cleanup must be idempotent
	}
	history := (&Server{Catalog: catalog}).forecastHistory(manualSyncRunKind)
	if len(history) != 3 || len(history[0].Forecasts) < 3 {
		t.Fatalf("worker snapshots were not retained without polling: %+v", history)
	}
	if history[0].Forecasts[0].Estimate.Point != 0 || history[0].Forecasts[0].Actual == nil {
		t.Fatalf("cold prediction borrowed its own final duration: %+v", history[0].Forecasts[0])
	}
	if history[1].Forecasts[0].Estimate.Point != 100 || *history[1].Forecasts[0].Actual != 100 {
		t.Fatalf("second run did not use prior completed work: %+v", history[1].Forecasts[0])
	}
	if history[2].Forecasts[0].Actual != nil {
		t.Fatal("interrupted duration was scored as completion")
	}
	accuracy := forecastAccuracy(history)["launch"].(map[string]any)
	if accuracy["attempts"] != 3 || accuracy["predicted"] != 2 || accuracy["evaluated"] != 1 || accuracy["covered"] != 1 {
		t.Fatalf("availability/error denominators: %+v", accuracy)
	}
	useHost(t, "another-forecast-host")
	if len(s.forecastHistory(manualSyncRunKind)) != 0 {
		t.Fatal("cross-host prediction history")
	}
}

func TestOperationForecastKeepsStageMeasurementSeparateAndUsesWholeCompletion(t *testing.T) {
	at := time.Now()
	step := forecastStep{ID: "analysis", Model: "v1", Label: "Analysis", State: "pending", Inventory: true}
	prior := step
	prior.State, prior.Seconds = "complete", 100
	op := &operationForecast{started: at, steps: []forecastStep{step}, history: []forecastRun{{State: "complete", Steps: []forecastStep{prior}}}}
	activities := []libraryActivity{{ID: "run", Kind: "index", operation: op, Work: &activityWork{Phase: "saving", Done: 0, Total: 0}}}
	s := &Server{}
	s.estimateActivities(activities, at)
	a := activities[0]
	if a.Progress != nil || a.Estimate.State != "unknown" || a.Forecast.Point != 100 || a.EstimatedCompletionAt != formatTime(at.Add(100*time.Second)) || a.EstimatedCompletionScope != "operation" {
		t.Fatalf("opaque measured stage should keep a whole-operation forecast: %+v", a)
	}
	if a.Forecast.Progress == nil || *a.Forecast.Progress != 0 {
		t.Fatalf("new operation should begin at zero overall: %+v", a.Forecast)
	}
	if a.Forecast.CurrentStep != 1 || a.Forecast.TotalSteps != 1 {
		t.Fatalf("first planned step: %+v", a.Forecast)
	}
	// Two equally costly sequential steps: finishing the first is halfway
	// through the operation even as the next step's own counter starts at zero.
	next := step
	next.ID, next.After = "next", []string{step.ID}
	priorNext := prior
	priorNext.ID = next.ID
	op.history[0].Steps = append(op.history[0].Steps, priorNext)
	op.steps = append(op.steps, next)
	op.steps[0].State, op.steps[0].Started = "running", at
	s.estimateActivities(activities, at.Add(50*time.Second))
	if got := activities[0].Forecast.Progress; got == nil || *got != .25 {
		t.Fatalf("half of the first step should be a quarter overall: %+v", activities[0].Forecast)
	}
	op.steps[0].State, op.steps[0].Seconds = "complete", 100
	op.steps[1].State, op.steps[1].Started = "running", at.Add(100*time.Second)
	activities[0].Work = &activityWork{Phase: "next step", Unit: "group", Total: 100}
	s.estimateActivities(activities, at.Add(100*time.Second))
	if a = activities[0]; a.Forecast.Progress == nil || *a.Forecast.Progress != .5 || a.Progress == nil || *a.Progress != 0 {
		t.Fatalf("stage transition reset overall progress or reused stage progress: %+v", a)
	}
	if a.Forecast.CurrentStep != 2 || a.Forecast.TotalSteps != 2 {
		t.Fatalf("step number did not advance: %+v", a.Forecast)
	}
	op.steps[0].State = "skipped"
	if got := op.estimate(at, true); got.CurrentStep != 2 || got.TotalSteps != 2 {
		t.Fatalf("skipped work or stopping changed step numbering: %+v", got)
	}
	op.steps[0].State = "complete"
	op.steps[1].Inventory = false
	s.estimateActivities(activities, at.Add(100*time.Second))
	if activities[0].Forecast.Progress != nil {
		t.Fatal("incomplete discovery published an overall percentage")
	}
	op.steps[1].Inventory = true
	op.steps = append(op.steps, forecastStep{ID: "unknown", Label: "Unseen step", State: "pending"})
	s.estimateActivities(activities, at)
	if activities[0].EstimatedCompletionAt != "" || activities[0].EstimatedCompletionScope != "" || activities[0].Forecast.Progress != nil {
		t.Fatalf("partial forecast published a complete ETA: %+v", activities[0])
	}
	for i := range op.steps {
		op.steps[i].State = "complete"
	}
	if got := op.estimate(at.Add(200*time.Second), false); got.Progress == nil || *got.Progress != 1 {
		t.Fatalf("finished operation did not reach 100%%: %+v", got)
	}
	if got := op.estimate(at, true); got.Progress != nil {
		t.Fatalf("stopping retained an overall percentage: %+v", got)
	}
}

func TestOperationForecastSeparatesReusableAnalysisAndCalibratesPriorErrors(t *testing.T) {
	clean, dirty := false, true
	step := forecastStep{ID: "analysis:tools", Model: "v1", State: "complete", Inventory: true, Seconds: 1, Features: workFeatures{Groups: 100, Dirty: &clean}}
	rebuild := step
	rebuild.Seconds, rebuild.Features.Dirty = 100, &dirty
	history := []forecastRun{{State: "complete", Steps: []forecastStep{step}}, {State: "complete", Steps: []forecastStep{rebuild}}}
	current := predictStep(history, step)
	fresh := predictStep(history, rebuild)
	if current.point != 1 || fresh.point != 100 {
		t.Fatalf("cache hits diluted rebuild durations: current=%+v rebuild=%+v", current, fresh)
	}
	at := time.Now()
	rebuild.State = "pending"
	op := &operationForecast{started: at, steps: []forecastStep{rebuild}, history: history, snapshots: []forecastSnapshot{{Reason: "discovered", At: 0}}}
	before := op.estimate(at, false)
	actual := 1000.0
	for range 8 {
		op.history = append(op.history, forecastRun{State: "complete", Forecasts: []forecastSnapshot{{Reason: "discovered", Estimate: operationEstimate{Point: 100}, Actual: &actual}}})
	}
	after := op.estimate(at, false)
	if after.HighSeconds < 1000 || after.HighSeconds <= before.HighSeconds || after.Point != before.Point {
		t.Fatalf("previous underestimates did not widen bounds: before=%+v after=%+v", before, after)
	}
}

func TestOperationForecastCanUseLiveSourcePaceBeforeSourceHistoryExists(t *testing.T) {
	at := time.Now()
	work := activityWork{Phase: "indexing", Unit: "group", Total: 100}
	o := &progressObservation{}
	for _, seconds := range []int{0, 4, 8, 12} {
		work.Done = float64(seconds)
		o.estimate(work, at.Add(time.Duration(seconds)*time.Second))
	}
	future := forecastStep{ID: "analysis:git", Model: "v1", State: "pending", Inventory: true, After: []string{"source:new"}}
	prior := future
	prior.State, prior.Seconds = "complete", 100
	op := &operationForecast{started: at, steps: []forecastStep{{ID: "source:new", State: "running", Started: at, Inventory: true, Work: &work, Observation: o}, future}, history: []forecastRun{{State: "complete", Steps: []forecastStep{prior}}}}
	got := op.estimate(at.Add(12*time.Second), false)
	if got.State != "provisional" || got.Predicted != 2 || len(got.Unknown) != 0 || got.Point < 180 {
		t.Fatalf("live source pace did not fill the missing source prior: %+v", got)
	}
}
