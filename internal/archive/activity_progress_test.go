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
	history := []workTiming{{100, 100}, {200, 200}, {400, 400}, {100, 10000}}
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
	o := &progressObservation{history: []workTiming{{100, 1}, {100, 1}, {100, 1}}}
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
		o := &progressObservation{history: []workTiming{{100, priorSeconds}, {100, priorSeconds}, {100, priorSeconds}}}
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
	o := &progressObservation{history: []workTiming{{1000, 100}, {1000, 100}, {1000, 100}}}
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
		{[]sourcePart{{item: "/session.jsonl", size: 10}, {item: "sqlite:/requests.db", size: 4096}}, 4106, "byte"},
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
