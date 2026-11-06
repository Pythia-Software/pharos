package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

func TestLabelerDeviceSettings(t *testing.T) {
	catalog, config := testCatalog(t)
	workerDir := t.TempDir()
	worker := filepath.Join(workerDir, "worker")
	probeCount := filepath.Join(workerDir, "probes")
	writeWorker := func(reply string) {
		t.Helper()
		// A capability probe must not load a model or start the framed worker.
		script := "#!/bin/sh\ncase \" $* \" in *' --probe-mlx '*) ;; *) exit 1;; esac\necho probe >> '" + probeCount + "'\nprintf '%s\\n' '" + reply + "'\n"
		if err := os.WriteFile(worker, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeWorker(`{"available":true}`)
	model := func() string {
		dir := t.TempDir()
		for _, name := range []string{"model.fp32.onnx", "model.int8.onnx", "weights.safetensors"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("stand-in"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		man := labelrt.Manifest{Files: map[string]string{"onnx_fp32": "model.fp32.onnx", "onnx_int8": "model.int8.onnx"}, MLX: &labelrt.MLXArtifact{Weights: "weights.safetensors"}}
		raw, _ := json.Marshal(man)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	config.Labels = LabelerSettings{Worker: worker, Backend: "auto", ModelDir: model(), Variant: "fp32", Threads: 4}
	config.AgentEvents = AgentEventSettings{ModelDir: model(), Variant: "int8", Threads: 2}
	server := testServer(t, config, catalog)
	check := func(document map[string]any, device string, confirmed, threads bool) {
		t.Helper()
		execution := document["execution"].(labelerDevice)
		choices, _ := document["thread_choices"].([]int)
		if execution.Device != device || execution.Confirmed != confirmed || document["threads_can_change"] != threads || (len(choices) > 0) != (device == "cpu") {
			t.Fatalf("device=%s confirmed=%t threads=%t: %+v", device, confirmed, threads, document)
		}
		if execution.Status == "" || execution.Detail == "" {
			t.Fatalf("unexplained device: %+v", execution)
		}
	}
	check(server.labelsSettings(), "gpu", false, false)
	if server.labelsSettings()["notice"] != "" {
		t.Fatal("routine off status became a notice")
	}
	check(server.agentEventsSettings(t.Context()), "cpu", false, true)
	check(server.labelsSettings(), "gpu", false, false)
	probes, err := os.ReadFile(probeCount)
	if err != nil || strings.Count(string(probes), "probe") != 1 {
		t.Fatalf("uncached GPU probes: %s %v", probes, err)
	}
	settings := server.Config().Labels
	path := filepath.Join(settings.ModelDir, "model.fp32.onnx")
	storeDevice := func(device string) {
		catalog.labelerWorkers.Store(path, labelerWorkerObservation{labelerDeviceKey(settings, path), func() labelrt.WorkerInfo { return labelrt.WorkerInfo{Device: device} }})
	}
	// Confirmed CPU fallback overrides a successful preflight GPU check.
	storeDevice("cpu")
	check(server.labelsSettings(), "cpu", true, true)
	storeDevice("gpu")
	check(server.labelsSettings(), "gpu", true, false)
	// Replacing the worker invalidates observations and cached capability checks.
	writeWorker(`{"available":false,"reason":"unsupported OS"}`)
	check(server.labelsSettings(), "cpu", false, true)
	setBackend := func(backend string) {
		server.configMu.Lock()
		server.config.Labels.Backend = backend
		server.configMu.Unlock()
	}
	setBackend("mlx")
	check(server.labelsSettings(), "unavailable", false, false)
	setBackend("auto")
	writeWorker(`{"available":null,"reason":"older engine"}`)
	check(server.labelsSettings(), "unknown", false, false)
	setBackend("cpu")
	check(server.labelsSettings(), "cpu", false, true)
	server.configMu.Lock()
	server.config.Labels.ModelDir = ""
	server.config.Labels.Enabled = true
	server.configMu.Unlock()
	check(server.labelsSettings(), "unknown", false, false)
	if server.labelsSettings()["notice"] == "" {
		t.Fatal("a pending download lost its notice")
	}
	if catalog.hasLabelsLedger() {
		t.Fatal("reading device settings ran labeling")
	}
}

// Background Processing (docs/background-work.md §11): where measured cost
// places an item, when each cadence's work is due, the pass that runs it,
// and the settings that steer it.

// An adaptive item takes the most frequent cadence its cost fits, with a
// quarter's margin each way so a cost near a limit stays put.
func TestPlaceCadenceMovesByMeasuredCostWithoutFlapping(t *testing.T) {
	for _, test := range []struct {
		name, current string
		seconds       float64
		runs          int
		want          string
	}{
		{"too few runs to move", cadenceFrequent, 900, placementRuns - 1, cadenceFrequent},
		{"a slow frequent item moves to hourly", cadenceFrequent, 300, 5, cadenceHourly},
		{"far past hourly goes to daily", cadenceFrequent, 900, 5, cadenceDaily},
		{"just over its limit stays", cadenceFrequent, 70, 5, cadenceFrequent},
		{"a fast hourly item moves to frequent", cadenceHourly, 30, 5, cadenceFrequent},
		{"just under the faster limit stays", cadenceHourly, 55, 5, cadenceHourly},
		{"a daily item that got faster", cadenceDaily, 120, 5, cadenceHourly},
	} {
		if got := placeCadence(test.current, itemCost{Seconds: test.seconds, Runs: test.runs}); got != test.want {
			t.Errorf("%s: placed %s, want %s", test.name, got, test.want)
		}
	}
}

// An item's typical time is the middle of its recent runs, in one unit, so
// an outlier at either end does not widen it.
func TestTypicalWordsSpanTheMiddleOfRecentRuns(t *testing.T) {
	for _, test := range []struct {
		runs []float64
		want string
	}{
		{[]float64{3, 4, 5}, "Typically a few seconds"},
		{[]float64{20, 25, 30}, "Typically 20–30 s"},
		{[]float64{2, 30, 200}, "Typically up to 3 min"},
		{[]float64{360, 370, 380}, "Typically about 6 min"},
		{[]float64{60, 7200, 8000, 9000, 9500, 10800, 90000}, "Typically 2–3 h"},
	} {
		_, low, high := typicalRange(test.runs)
		if got := typicalWords(itemCost{Low: low, High: high}); got != test.want {
			t.Errorf("%v: %q, want %q", test.runs, got, test.want)
		}
	}
}

// Each cadence's rule, at a fixed moment: Frequently at once, Hourly an hour
// after its last run once the library is quiet (and no later than 90 min),
// Daily at the daily time; a failure waits before it runs again; an
// override, a pause or Off stops it, and Run now runs it anyway.
func TestBackgroundPlanRunsEachCadenceWhenDue(t *testing.T) {
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.Local)
	ago := func(d time.Duration) string { return formatTime(at.Add(-d)) }
	settings := func() backgroundSettings {
		return backgroundSettings{Mode: "on", Interval: 300, DailyAt: "03:00", Overrides: map[string]string{"writing": cadenceHourly}, Placements: map[string]string{}}
	}
	// A stage with work written changed ago, last caught up completed ago.
	pending := func(changed, completed time.Duration) deferredRow {
		return deferredRow{generation: 2, completed: 1, changedAt: ago(changed), completedAt: ago(completed), pendingSince: ago(changed)}
	}
	plan := func(settings backgroundSettings, in backgroundInputs, id string) itemPlan {
		t.Helper()
		if in.runs == nil {
			in.runs = map[string]backgroundRunRow{}
		}
		for _, plan := range backgroundPlan(settings, in, at) {
			if plan.ID == id {
				return plan
			}
		}
		t.Fatalf("%s is not in the plan", id)
		return itemPlan{}
	}
	stage := func(name string, row deferredRow) backgroundInputs {
		return backgroundInputs{deferred: map[string]deferredRow{name: row}}
	}

	if got := plan(settings(), stage("tools", pending(time.Minute, 2*time.Hour)), "tools"); !got.Due || got.Cadence != cadenceFrequent {
		t.Fatalf("frequent work runs at once: %+v", got)
	}
	if got := plan(settings(), stage("tools", deferredRow{generation: 1, completed: 1, completedAt: ago(time.Hour)}), "tools"); got.Due || got.Pending || !got.Next.IsZero() {
		t.Fatalf("with nothing waiting, nothing runs, and the next run waits for new work: %+v", got)
	}

	busy := plan(settings(), stage("authorship", pending(2*time.Minute, 70*time.Minute)), "writing")
	if busy.Due || !busy.Next.Equal(at.Add(-2*time.Minute).Add(hourlyQuiet)) {
		t.Fatalf("hourly work waits for a quiet library: %+v", busy)
	}
	if got := plan(settings(), stage("authorship", pending(15*time.Minute, 70*time.Minute)), "writing"); !got.Due {
		t.Fatalf("hourly work runs once the library is quiet: %+v", got)
	}
	if got := plan(settings(), stage("authorship", pending(time.Minute, 95*time.Minute)), "writing"); !got.Due {
		t.Fatalf("hourly work runs 90 min after its last run, quiet or not: %+v", got)
	}
	if got := plan(settings(), stage("authorship", pending(15*time.Minute, 30*time.Minute)), "writing"); got.Due || !got.Next.Equal(at.Add(30*time.Minute)) {
		t.Fatalf("hourly work runs at most once an hour: %+v", got)
	}

	daily := func(succeeded time.Duration) backgroundInputs {
		return backgroundInputs{runs: map[string]backgroundRunRow{"preserve": {succeeded: ago(succeeded), finished: ago(succeeded)}}}
	}
	if got := plan(settings(), daily(30*time.Hour), "preserve"); !got.Due || !got.Missed {
		t.Fatalf("daily work not done since today's 03:00 runs, and says it missed its time: %+v", got)
	}
	tomorrow := time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local)
	if got := plan(settings(), daily(time.Hour), "preserve"); got.Due || got.Missed || !got.Next.Equal(tomorrow) {
		t.Fatalf("daily work done today waits for tomorrow's time: %+v", got)
	}
	late := settings()
	late.DailyAt = "14:55"
	quietless := daily(30 * time.Hour)
	quietless.deferred = map[string]deferredRow{"tools": pending(time.Minute, time.Minute)}
	if got := plan(late, quietless, "preserve"); got.Due {
		t.Fatalf("daily work waits for a quiet library after its time: %+v", got)
	}

	failed := stage("authorship", pending(15*time.Minute, 2*time.Hour))
	failed.runs = map[string]backgroundRunRow{"writing": {finished: ago(5 * time.Minute), err: "boom"}}
	if got := plan(settings(), failed, "writing"); got.Due || !got.Failed || !got.Next.Equal(at.Add(backgroundRetry-5*time.Minute)) {
		t.Fatalf("a failed item waits %s before it runs again: %+v", backgroundRetry, got)
	}

	manual := settings()
	manual.Overrides["writing"] = cadenceManual
	quiet := stage("authorship", pending(15*time.Minute, 2*time.Hour))
	if got := plan(manual, quiet, "writing"); got.Due || got.Cadence != cadenceManual {
		t.Fatalf("an item set to only when you ask never runs on its own: %+v", got)
	}
	paused := settings()
	paused.Mode = "paused"
	if got := plan(paused, quiet, "writing"); got.Due {
		t.Fatalf("nothing runs while paused: %+v", got)
	}
	quiet.requested = map[string]bool{"writing": true}
	if got := plan(paused, quiet, "writing"); !got.Due {
		t.Fatalf("Run now runs an item whatever the schedule: %+v", got)
	}

	review := backgroundInputs{findingsBuilt: true}
	if got := plan(settings(), review, "review"); !got.Due {
		t.Fatalf("today's findings review runs after the daily time: %+v", got)
	}
	review.findingsToday = true
	if got := plan(settings(), review, "review"); got.Due {
		t.Fatalf("a review done today does not run again: %+v", got)
	}

	for _, plan := range backgroundPlan(settings(), backgroundInputs{runs: map[string]backgroundRunRow{}}, at) {
		if plan.ID == "other-sources" {
			t.Fatalf("with no source read in full there is no other-sources item: %+v", plan)
		}
	}
	other := backgroundInputs{otherSources: []string{"tl1"}, runs: map[string]backgroundRunRow{"other-sources": {succeeded: ago(65 * time.Minute)}}}
	if got := plan(settings(), other, "other-sources"); !got.Due || got.Cadence != cadenceHourly {
		t.Fatalf("sources read in full are read hourly: %+v", got)
	}
}

func TestBackgroundVerificationQueueRespectsCadence(t *testing.T) {
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.Local)
	settings := backgroundSettings{Mode: "on", DailyAt: defaultDailyAt}
	input := backgroundInputs{verificationNext: at.Add(-time.Minute)}
	plan := func() itemPlan {
		for _, plan := range backgroundPlan(settings, input, at) {
			if plan.ID == "verification" {
				return plan
			}
		}
		t.Fatal("verification item missing")
		return itemPlan{}
	}
	if got := plan(); !got.Due || got.Cadence != cadenceHourly {
		t.Fatalf("queued work was not due on the larger cadence: %+v", got)
	}
	input.deferred = map[string]deferredRow{"tools": {generation: 2, completed: 1, changedAt: formatTime(at)}}
	if got := plan(); got.Due || !got.Next.Equal(at.Add(hourlyQuiet)) {
		t.Fatalf("larger verification did not wait for a quiet library: %+v", got)
	}
	input.deferred = nil
	settings.Mode = "manual"
	if got := plan(); got.Due {
		t.Fatalf("only-when-I-ask mode started queued work: %+v", got)
	}
	settings.Mode = "on"
	input.verificationNext = time.Time{}
	if got := plan(); got.Due || got.Pending {
		t.Fatalf("an empty queue scheduled verification: %+v", got)
	}
	input.verificationNext = at.Add(-time.Minute)
	input.runs = map[string]backgroundRunRow{"verification": {succeeded: formatTime(at)}}
	if got := plan(); got.Due || got.Next.Before(at.Add(hourlyEvery)) {
		t.Fatalf("larger checks ran more than hourly: %+v", got)
	}
}

// Every declared item uses the chosen column, including work whose default
// cadence used to be fixed or opt-in. Scheduling and Run now remain separate.
func TestBackgroundPlanMovesEveryItem(t *testing.T) {
	at := time.Date(2026, 10, 29, 13, 0, 0, 0, time.Local)
	ago := formatTime(at.Add(-48 * time.Hour))
	in := backgroundInputs{otherSources: []string{"tl1"}, backupDestination: "/backup", lastBackup: ago,
		upgradePending: true, verificationNext: at.Add(-time.Hour), labels: true, agentEvents: true, ask: true, suggestionsStale: true,
		runs: map[string]backgroundRunRow{}, deferred: map[string]deferredRow{}}
	for _, item := range backgroundItems {
		in.runs[item.ID] = backgroundRunRow{succeeded: ago}
	}
	for _, stage := range stageOrder {
		in.deferred[stage] = deferredRow{generation: 2, completed: 1, changedAt: ago, completedAt: ago}
	}
	for _, cadence := range append(slices.Clone(cadences), cadenceManual) {
		settings := backgroundSettings{Mode: "on", Interval: 300, DailyAt: defaultDailyAt, Temporary: map[string]temporaryCadence{}}
		settings.sync = syncSettings{Interval: 300, NextAt: formatTime(at.Add(-48*time.Hour + 5*time.Minute))}
		settings.lastConversationCheck = at.Add(-48 * time.Hour)
		for _, item := range backgroundItems {
			settings.Temporary[item.ID] = temporaryCadence{Cadence: cadence}
		}
		for _, plan := range backgroundPlan(settings, in, at) {
			wantDue := cadence != cadenceManual && plan.ID != "conversations" && !(plan.ID == "search" && cadence == cadenceFrequent)
			if plan.Cadence != cadence || plan.Due != wantDue || (cadence == cadenceManual && !plan.Next.IsZero()) {
				t.Errorf("%s on %s: %+v, want due=%v", plan.ID, cadence, plan, wantDue)
			}
		}
		if got := automaticDue(settings, powerState{}, false, at); got != (cadence != cadenceManual) {
			t.Errorf("scheduled conversations on %s: due=%v", cadence, got)
		}
		in.requested = map[string]bool{}
		for _, item := range backgroundItems {
			in.requested[item.ID] = true
		}
		for _, plan := range backgroundPlan(settings, in, at) {
			if plan.ID != "conversations" && !plan.Due {
				t.Errorf("Run now ignored %s on %s", plan.ID, cadence)
			}
		}
		in.requested = nil
	}
}

func TestBackgroundStandaloneCadencesWaitAfterCompletion(t *testing.T) {
	at := time.Date(2026, 10, 29, 13, 0, 0, 0, time.Local)
	for _, cadence := range cadences {
		settings := backgroundSettings{Mode: "on", Interval: 300, DailyAt: defaultDailyAt, Temporary: map[string]temporaryCadence{}}
		in := backgroundInputs{otherSources: []string{"tl1"}, backupDestination: "/backup", upgradePending: true,
			verificationNext: at.Add(-time.Hour), runs: map[string]backgroundRunRow{}}
		for _, id := range []string{"search", "preserve", "verification", "other-sources", "upgrade", "backup"} {
			settings.Temporary[id] = temporaryCadence{Cadence: cadence}
			in.runs[id] = backgroundRunRow{succeeded: formatTime(at.Add(-time.Minute))}
		}
		for _, plan := range backgroundPlan(settings, in, at) {
			if _, tracked := in.runs[plan.ID]; tracked && plan.Due {
				t.Errorf("%s on %s ran again too soon: %+v", plan.ID, cadence, plan)
			}
		}
	}
}

func TestConversationsMovedScheduleWaitsForItsCadenceAndQuiet(t *testing.T) {
	t.Run("settings saves preserve overdue checks", func(t *testing.T) {
		catalog, config := testCatalog(t)
		server := testServer(t, config, catalog)
		ctx := context.Background()
		at := time.Now()
		last := at.Add(-48 * time.Hour)
		execTx(t, catalog.DB,
			"INSERT INTO sync_history(host_id,run_id,finished_at,sample_json) VALUES("+sqlQuote(currentHost().ID)+",'last-check',"+sqlQuote(formatTime(last))+",'{}')",
			"INSERT INTO sync_history(host_id,run_id,finished_at,sample_json) VALUES('another-mac','other-check',"+sqlQuote(formatTime(at))+",'{}')")
		post := func(body map[string]any) {
			t.Helper()
			response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/background/settings", body)
			if response.Code != http.StatusOK {
				t.Fatalf("settings save: %s", response.Body.String())
			}
		}
		for _, cadence := range []string{cadenceHourly, cadenceDaily} {
			post(map[string]any{"mode": "pause", "daily_at": at.Add(-time.Hour).Format("15:04"),
				"temporary_cadences": map[string]any{"conversations": cadence}})
			for _, body := range []map[string]any{{"mode": "on"}, {"hold_when_busy": false}, {"interval_seconds": 600}} {
				post(body)
				settings, err := catalog.backgroundSettings(ctx, at)
				if err != nil {
					t.Fatal(err)
				}
				if !automaticDue(settings, powerState{}, false, at) {
					t.Errorf("%s overdue check was postponed by %v: %+v", cadence, body, settings.conversationsPlan(time.Time{}, at))
				}
			}
		}
		// A real completed check, unlike a settings save, advances the schedule.
		execTx(t, catalog.DB, "INSERT INTO sync_history(host_id,run_id,finished_at,sample_json) VALUES("+sqlQuote(currentHost().ID)+",'completed-check',"+sqlQuote(formatTime(at))+",'{}')")
		settings, err := catalog.backgroundSettings(ctx, at)
		if err != nil || automaticDue(settings, powerState{}, false, at) {
			t.Fatalf("completed daily check did not advance the schedule: %+v (%v)", settings, err)
		}
	})
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.Local)
	for _, cadence := range []string{cadenceHourly, cadenceDaily, cadenceManual} {
		settings := backgroundSettings{Mode: "on", DailyAt: defaultDailyAt,
			Temporary: map[string]temporaryCadence{"conversations": {Cadence: cadence}}}
		settings.sync = syncSettings{Interval: 300, NextAt: formatTime(at.Add(4 * time.Minute))}
		settings.lastConversationCheck = at.Add(-time.Minute)
		if automaticDue(settings, powerState{}, false, at) {
			t.Errorf("%s ran again before its next window", cadence)
		}
		if !automaticDue(settings, powerState{}, true, at) {
			t.Errorf("Run now did not bypass %s", cadence)
		}
		settings.lastConversationCheck = at.Add(-2 * time.Hour)
		if cadence == cadenceDaily {
			settings.lastConversationCheck = at.Add(-24 * time.Hour)
		}
		quiet := at.Add(9 * time.Minute)
		plan := settings.conversationsPlan(quiet, at)
		if cadence != cadenceManual && !plan.Next.Equal(quiet) {
			t.Errorf("%s did not wait for quiet: %+v", cadence, plan)
		}
	}
}

func TestBackgroundSearchFollowsScheduleAndRunNow(t *testing.T) {
	catalog, config := libraryFixture(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/background/settings",
		map[string]any{"temporary_cadences": map[string]any{"search": cadenceManual}})
	if response.Code != http.StatusOK {
		t.Fatalf("moving Search: %s", response.Body.String())
	}
	gate := searchScheduleGate{}
	waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	pass := catalog.beginPass(ctx, "maintenance.search.text", triggerVersion)
	pass.worked(1)
	if err := gate.wait(waiting, catalog, &pass); err != context.DeadlineExceeded {
		t.Fatalf("continuous Search did not wait on its manual schedule: %v", err)
	}
	if live, _ := runningItems(server.ops.views()); live["search"] {
		t.Fatal("a held Search daemon left an operation running")
	}
	if dirtyCount(t, catalog) == 0 {
		t.Fatal("fixture must have pending Search work")
	}
	// Scheduled Search must leave stopped projection work pending.
	if !catalog.stopLibraryRefresh() {
		t.Fatal("could not stop Search refresh")
	}
	pending := dirtyCount(t, catalog)
	for _, cadence := range []string{cadenceHourly, cadenceDaily} {
		response = apiCall(t, server, config.APIToken, http.MethodPost, "/api/background/settings",
			map[string]any{"hold_when_busy": false, "temporary_cadences": map[string]any{"search": cadence}})
		if response.Code != http.StatusOK {
			t.Fatalf("moving Search to %s: %s", cadence, response.Body.String())
		}
		settings, err := catalog.backgroundSettings(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, plan := range backgroundPlan(settings, backgroundInputs{}, time.Now()) {
			if plan.ID == "search" {
				server.runBackgroundPass(ctx, cadence, []string{"search"}, []itemPlan{plan})
			}
		}
		if !catalog.libraryRefreshStopped() || dirtyCount(t, catalog) != pending {
			t.Errorf("%s Search bypassed Stop: pending=%d, want %d", cadence, dirtyCount(t, catalog), pending)
		}
	}
	response = apiCall(t, server, config.APIToken, http.MethodPost, "/api/background/settings",
		map[string]any{"temporary_cadences": map[string]any{"search": cadenceManual}})
	if response.Code != http.StatusOK {
		t.Fatalf("moving Search back to manual: %s", response.Body.String())
	}
	response = apiCall(t, server, config.APIToken, http.MethodPost, "/api/background/run", map[string]any{"item": "search"})
	if response.Code != http.StatusAccepted || !server.peekRequested()["search"] {
		t.Fatalf("Search Run now was not queued: %s", response.Body.String())
	}
	if !server.backgroundPass(ctx, time.Now()) {
		t.Fatal("Search Run now did not run")
	}
	if dirtyCount(t, catalog) != 0 {
		t.Fatal("Search Run now left workspaces waiting")
	}
	settings, err := catalog.backgroundSettings(ctx, time.Now())
	search, _ := backgroundItemByID("search")
	if err != nil || settings.cadence(search) != cadenceManual {
		t.Fatal("Run now changed the Search schedule")
	}
	if run := catalog.backgroundRuns(ctx)["search"]; run.succeeded == "" || run.err != "" {
		t.Fatalf("Search catch-up failed: %+v", run)
	}
	response = apiCall(t, server, config.APIToken, http.MethodPost, "/api/background/settings",
		map[string]any{"temporary_cadences": map[string]any{"search": ""}})
	if response.Code != http.StatusOK {
		t.Fatalf("restoring Search: %s", response.Body.String())
	}
	gate = searchScheduleGate{}
	if err := gate.wait(ctx, catalog); err != nil {
		t.Fatalf("Search did not resume its continuous default: %v", err)
	}
}

// An override can only slow an item down; a fixed item has none; an
// opt-in item runs only when chosen.
func TestBackgroundOverridesOnlySlowItemsDown(t *testing.T) {
	settings := backgroundSettings{Overrides: map[string]string{"writing": cadenceFrequent, "tools": cadenceDaily, "search": cadenceManual}, Placements: map[string]string{}}
	for id, want := range map[string]string{"writing": cadenceFrequent, "tools": cadenceDaily, "search": cadenceFrequent, "backup": cadenceManual} {
		item, _ := backgroundItemByID(id)
		if got := settings.cadence(item); got != want {
			t.Errorf("%s runs %s, want %s", id, got, want)
		}
	}
	settings.Overrides["backup"] = cadenceDaily
	if item, _ := backgroundItemByID("backup"); settings.cadence(item) != cadenceDaily {
		t.Error("choosing Daily turns the backup on")
	}
}

// An item's cost is the median of its latest successful journaled runs,
// summed over its kinds; scheduled indexing's comes from its run history.
func TestBackgroundCostsAreMediansOfJournaledRuns(t *testing.T) {
	catalog, _ := testCatalog(t)
	host := sqlQuote(currentHost().ID)
	statements := []string{}
	run := 0
	journal := func(kind, state string, seconds int) {
		run++
		start := time.Date(2026, 10, 28, 0, run, 0, 0, time.UTC)
		statements = append(statements, fmt.Sprintf(`INSERT INTO operation_journal(id,root_id,kind,trigger,host_id,process,pid,instance,state,started_at,updated_at,ended_at)
			VALUES('j%d','j%d','%s','chained',%s,'service',1,'i','%s','%s','%s','%s')`, run, run, kind, host, state, formatTime(start), formatTime(start), formatTime(start.Add(time.Duration(seconds)*time.Second))))
	}
	for _, seconds := range []int{300, 400, 350} {
		journal("analysis.authorship", "succeeded", seconds)
	}
	journal("analysis.authorship", "failed", 10)
	for _, kind := range []string{"analysis.github", "analysis.identities", "analysis.git"} {
		for range 3 {
			journal(kind, "succeeded", map[string]int{"analysis.github": 1, "analysis.identities": 5, "analysis.git": 4}[kind])
		}
	}
	for index, sample := range []string{`{"trigger":"automatic","seconds":10}`, `{"trigger":"automatic","seconds":20}`, `{"trigger":"automatic","seconds":30}`, `{"trigger":"manual","seconds":900}`} {
		statements = append(statements, fmt.Sprintf("INSERT INTO sync_history(host_id,run_id,finished_at,sample_json) VALUES(%s,'r%d','2026-10-28T00:00:00Z','%s')", host, index, sample))
	}
	execTx(t, catalog.DB, statements...)
	costs := catalog.backgroundCosts(context.Background())
	near := func(got, want float64) bool { return got > want-0.01 && got < want+0.01 }
	if cost := costs["writing"]; !near(cost.Seconds, 350) || cost.Runs != 3 {
		t.Fatalf("writing: %+v, want the median of its successful runs", cost)
	}
	if cost := costs["repositories"]; !near(cost.Seconds, 10) || cost.Runs != 3 {
		t.Fatalf("repositories: %+v, want its three stages' medians summed", cost)
	}
	if cost := costs["conversations"]; !near(cost.Seconds, 20) || cost.Runs != 3 {
		t.Fatalf("conversations: %+v, want the median of scheduled runs only", cost)
	}
	if _, ok := costs["findings"]; ok {
		t.Fatal("an item with no runs has no cost")
	}
}

// A pass runs the most frequent cadence's due work through the chain, only
// the stages due (and those before them with work waiting), records each
// item, and leaves the rest; Run now runs an item outside its cadence.
// Settings and the status panel list items in backgroundItems order, so it
// is the order a pass runs them: standalone items, then the chain's in
// chain order, then those that read the chain's results.
func TestBackgroundItemsAreListedInRunOrder(t *testing.T) {
	chained, after, last := false, false, -1
	for _, item := range backgroundItems {
		chain := len(item.Stages) > 0 || item.ID == "review"
		if chained && !chain && !item.afterChain || after && !item.afterChain {
			t.Fatalf("%s runs before the chain but is listed after it", item.ID)
		}
		after = after || item.afterChain
		chained = chained || chain
		for _, stage := range item.Stages {
			index := slices.Index(stageOrder, stage)
			if index < last {
				t.Fatalf("%s's stage %s is listed out of chain order", item.ID, stage)
			}
			last = index
		}
	}
}

func TestBackgroundPassRunsDueStagesAndRecordsThem(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	execSQL(t, catalog.DB, "INSERT INTO background_settings(host_id,overrides_json) VALUES("+sqlQuote(currentHost().ID)+",'{\"writing\":\"hourly\"}')")
	stamp := func(d time.Duration) string { return sqlQuote(formatTime(time.Now().Add(-d))) }
	execTx(t, catalog.DB, "DELETE FROM sync_deferred",
		// Today's daily work is done.
		"INSERT INTO background_runs(host_id,item,succeeded_at) VALUES("+sqlQuote(currentHost().ID)+",'preserve',"+stamp(0)+")",
		"INSERT INTO meta(key,value) VALUES('findings_generation',"+sqlQuote(catalog.findingsGeneration())+") ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		"INSERT INTO sync_deferred(name,generation,completed_generation,changed_at,completed_at,pending_since) VALUES('tools',2,1,"+stamp(time.Minute)+","+stamp(2*time.Hour)+","+stamp(time.Minute)+")",
		// Hourly, and the library is not quiet: it waits.
		"INSERT INTO sync_deferred(name,generation,completed_generation,changed_at,completed_at,pending_since) VALUES('authorship',2,1,"+stamp(time.Minute)+","+stamp(10*time.Minute)+","+stamp(time.Minute)+")")
	completed := func(name string) int64 {
		var generation int64
		if err := catalog.DB.QueryRow("SELECT completed_generation FROM sync_deferred WHERE name=?", name).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	if !server.backgroundPass(ctx, time.Now()) {
		t.Fatal("the pass ran nothing with frequent work waiting")
	}
	if completed("tools") != 2 || completed("authorship") != 1 {
		t.Fatalf("the pass caught up tools only: tools %d, authorship %d", completed("tools"), completed("authorship"))
	}
	if !slices.ContainsFunc(server.ops.views(), func(view opView) bool {
		return view.Spec.Kind == "analysis.tools" && view.Step.Name != ""
	}) {
		t.Fatal("scheduled analysis lost its worker progress before reaching the operation registry")
	}
	runs := catalog.backgroundRuns(ctx)
	if runs["tools"].succeeded == "" || runs["tools"].err != "" {
		t.Fatalf("the pass records each item it ran: %+v", runs["tools"])
	}
	if _, ran := runs["writing"]; ran {
		t.Fatalf("an item that was not due has no run: %+v", runs)
	}
	if server.backgroundPass(ctx, time.Now()) {
		t.Fatal("a pass with nothing due ran something")
	}
	server.requestBackground("writing")
	if !server.backgroundPass(ctx, time.Now()) || completed("authorship") != 2 {
		t.Fatalf("Run now runs an item outside its cadence: authorship %d", completed("authorship"))
	}
	if len(server.peekRequested()) != 0 {
		t.Fatal("a request is taken once it runs")
	}
	// A manual index waiting for the coordinator takes it first.
	server.auto.mu.Lock()
	server.auto.manualWaiters++
	server.auto.mu.Unlock()
	server.requestBackground("tools")
	if server.backgroundPass(ctx, time.Now()) {
		t.Fatal("a pass ran while a manual index waited")
	}
	server.auto.mu.Lock()
	server.auto.manualWaiters--
	server.auto.mu.Unlock()
}

// The settings API: modes, the daily time, the interval, overrides within
// the allowed choices, and Run now; the status snapshot carries the
// schedule.
func TestMessageLabelsSettings(t *testing.T) {
	useSupportDir(t)
	catalog, config := testCatalog(t)
	original := "# Library settings\nlabels_threads = 2\n\n[[sources]]\nname = \"example\"\nkind = \"claude\"\npath = \"/nowhere\"\nenabled = true\n"
	if err := os.WriteFile(config.Path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	read := func() map[string]any {
		t.Helper()
		response := apiCall(t, server, config.APIToken, http.MethodGet, "/api/background", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("read settings: %d %s", response.Code, response.Body.String())
		}
		var document map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	if labels := read()["labels"].(map[string]any); labels["enabled"] != false || labels["can_change"] != true {
		t.Fatalf("disabled settings must remain visible: %v", labels)
	}
	// Agent events have their own step; their switch is a preference.
	if events := read()["agent_events"].(map[string]any); events["enabled"] != false || events["can_change"] != true {
		t.Fatalf("agent events settings must remain visible while off: %v", events)
	}
	for _, id := range []string{"labels", "agent_events"} {
		view := read()["view"].(map[string]any)
		found := false
		for _, value := range view["items"].([]any) {
			item := value.(map[string]any)
			if item["id"] == id {
				found = true
				if item["state"] != "disabled" || item["cadence"] != cadenceManual || item["next_at"] != nil {
					t.Fatalf("disabled model must be accessible without being scheduled: %v", item)
				}
			}
		}
		if !found {
			t.Fatalf("disabled model %s has no step for its settings", id)
		}
		if !slices.ContainsFunc(view["unscheduled"].([]any), func(value any) bool { return value == id }) {
			t.Fatalf("disabled model %s is not reachable in the unscheduled column", id)
		}
	}
	if response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/preferences", map[string]any{"set": map[string]any{agentEventsFlag: true}}); response.Code != http.StatusOK {
		t.Fatalf("turn agent events on: %d %s", response.Code, response.Body.String())
	}
	if events := read()["agent_events"].(map[string]any); events["enabled"] != true || !strings.Contains(firstString(events["status"]), "worker") {
		t.Fatalf("agent events switch is not read back, or a missing worker is not explained: %v", events)
	}
	for _, body := range []string{`{}`, `{"enabled":"true"}`, `{"enabled":null}`} {
		if response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/labels/settings", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid setting %s: %d", body, response.Code)
		}
	}
	for _, enabled := range []bool{true, false, true} {
		response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/labels/settings", map[string]any{"enabled": enabled})
		if response.Code != http.StatusOK {
			t.Fatalf("toggle %t: %d %s", enabled, response.Code, response.Body.String())
		}
		reloaded, err := LoadConfig(config.Path)
		if err != nil || reloaded.Labels.Enabled != enabled || server.Config().Labels.Enabled != enabled || reloaded.Labels.Threads != 2 {
			t.Fatalf("toggle did not persist/apply: %+v %v", reloaded.Labels, err)
		}
		if len(reloaded.Sources) != 1 || !reloaded.Sources[0].Enabled {
			t.Fatal("the setting changed a source")
		}
		document := read()
		labels := document["labels"].(map[string]any)
		if labels["enabled"] != enabled {
			t.Fatalf("readback: %v", labels)
		}
		if enabled && (!strings.Contains(firstString(labels["status"]), "download") || !strings.Contains(firstString(labels["status"]), "worker")) {
			t.Fatalf("missing runtime assets are not explained: %v", labels)
		}
		var step map[string]any
		for _, item := range document["view"].(map[string]any)["items"].([]any) {
			if item.(map[string]any)["id"] == "labels" {
				step = item.(map[string]any)
			}
		}
		if step == nil || (step["state"] != "disabled") != enabled {
			t.Fatalf("labels step must remain reachable and reflect enabled = %t: %v", enabled, step)
		}
	}
	// Each model's threads: saved to the configuration and read back, and
	// changing labels' keeps agent events' where it was.
	for _, body := range []string{`{"threads":0}`, `{"threads":1.5}`, `{"threads":"2"}`, `{"agent_events_threads":100000}`} {
		if response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/labels/settings", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid threads %s: %d", body, response.Code)
		}
	}
	unchosen := max(1, server.Config().Labels.Threads)
	labelsTarget := server.labelerThreadSettings(t.Context(), false)
	eventsTarget := server.labelerThreadSettings(t.Context(), true)
	for _, body := range []map[string]any{{"threads": 1}, {"agent_events_threads": 1}} {
		if response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/labels/settings", body); response.Code != http.StatusOK {
			t.Fatalf("threads %v: %d %s", body, response.Code, response.Body.String())
		}
		reloaded, err := LoadConfig(config.Path)
		if err != nil || reloaded.Labels.Threads != 1 || reloaded.AgentEvents.Threads != map[bool]int{true: unchosen, false: 1}[body["threads"] != nil] || !reloaded.Labels.Enabled {
			t.Fatalf("threads %v were not saved: %+v %+v %v", body, reloaded.Labels, reloaded.AgentEvents, err)
		}
		document := read()
		if labels, events := document["labels"].(map[string]any), document["agent_events"].(map[string]any); labels["threads"] != 1.0 || events["threads"] != float64(reloaded.AgentEvents.Threads) {
			t.Fatalf("threads readback: %v %v", labels, events)
		}
		labels, events := labelsTarget(), eventsTarget()
		if labels.Threads != reloaded.Labels.Threads || events.Threads != reloaded.AgentEvents.Threads ||
			labels.PeerThreads != max(1, labels.Workers)*events.Threads || events.PeerThreads != max(1, labels.Workers)*labels.Threads {
			t.Fatalf("active runs did not see the updated settings and peer budget: %+v %+v", labels, events)
		}
	}
	deferred, err := catalog.freshnessDeferred(t.Context())
	if err != nil || deferred["labels"].generation <= deferred["labels"].completed {
		t.Fatalf("earlier messages were not scheduled: %v %v", deferred, err)
	}
	if catalog.hasLabelsLedger() {
		t.Fatal("changing settings ran inference")
	}
	text, _ := os.ReadFile(config.Path)
	if !strings.HasPrefix(string(text), "# Library settings\n") || strings.Count(string(text), "labels_enabled =") != 1 {
		t.Fatalf("configuration was not preserved: %s", text)
	}
	// A failed persistence must leave the running opt-in unchanged.
	if err := os.Remove(config.Path); err != nil {
		t.Fatal(err)
	}
	if response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/labels/settings", map[string]any{"enabled": false}); response.Code != http.StatusInternalServerError || !server.Config().Labels.Enabled {
		t.Fatalf("failed save changed the running choice: %d", response.Code)
	}
}

func TestBackgroundSettingsAPI(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	get := func() map[string]any {
		t.Helper()
		response := apiCall(t, server, config.APIToken, http.MethodGet, "/api/background", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/background: %d %s", response.Code, response.Body.String())
		}
		var document map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	post := func(path string, body any, want int) {
		t.Helper()
		if response := apiCall(t, server, config.APIToken, http.MethodPost, path, body); response.Code != want {
			t.Fatalf("POST %s %v: %d %s, want %d", path, body, response.Code, response.Body.String(), want)
		}
	}
	mode := func(document map[string]any) string {
		return firstString(document["settings"].(map[string]any)["mode"])
	}

	document := get()
	if mode(document) != "on" {
		t.Fatalf("background updates are on until turned off: %v", document["settings"])
	}
	view := document["view"].(map[string]any)
	if cadences, _ := view["cadences"].([]any); len(cadences) != 3 {
		t.Fatalf("one row per cadence: %v", view["cadences"])
	}
	// Back up is off unless chosen: on no schedule, so in no cadence's row.
	if unscheduled, _ := view["unscheduled"].([]any); !slices.Contains(unscheduled, any("backup")) {
		t.Fatalf("back up is not listed as unscheduled: %v", view["unscheduled"])
	}
	for _, row := range view["cadences"].([]any) {
		if items, _ := row.(map[string]any)["items"].([]any); slices.Contains(items, any("backup")) {
			t.Fatalf("an unscheduled item is in a cadence's row: %v", row)
		}
	}
	if headline, _ := view["headline"].(map[string]any); headline["tone"] == nil || headline["title"] == "" {
		t.Fatalf("the view has a headline: %v", view["headline"])
	}

	post("/api/background/settings", map[string]any{"mode": "pause"}, http.StatusOK)
	if mode(get()) != "paused" || !catalog.backgroundPaused(ctx, time.Now()) {
		t.Fatal("a pause pauses scheduled indexing too")
	}
	post("/api/background/settings", map[string]any{"mode": "manual"}, http.StatusOK)
	if settings, _ := catalog.syncSettings(ctx); settings.Enabled || mode(get()) != "manual" {
		t.Fatal("only when I ask turns scheduled indexing off")
	}
	post("/api/background/settings", map[string]any{"mode": "on", "daily_at": "05:30", "interval_seconds": 600, "hold_when_busy": false, "overrides": map[string]any{"writing": "daily"}}, http.StatusOK)
	settings, err := catalog.backgroundSettings(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writing, _ := backgroundItemByID("writing")
	if settings.Mode != "on" || settings.DailyAt != "05:30" || settings.Interval != 600 || settings.HoldWhenBusy || settings.cadence(writing) != cadenceDaily {
		t.Fatalf("the settings were not stored: %+v", settings)
	}
	post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": ""}}, http.StatusOK)
	if settings, _ := catalog.backgroundSettings(ctx, time.Now()); settings.cadence(writing) != cadenceFrequent {
		t.Fatal("an empty override returns an item to automatic")
	}
	for _, body := range []map[string]any{
		{"overrides": map[string]any{"writing": "frequent"}}, // faster than its placement
		{"overrides": map[string]any{"search": "daily"}},     // fixed
		{"overrides": map[string]any{"nothing": "daily"}},
		{"daily_at": "25:00"}, {"interval_seconds": 30}, {"mode": "sometimes"},
	} {
		post("/api/background/settings", body, http.StatusBadRequest)
	}
	post("/api/background/settings", map[string]any{"battery": map[string]any{"frequent": "wait", "hourly": "same", "low_battery_waits": false}}, http.StatusOK)
	if settings, _ := catalog.backgroundSettings(ctx, time.Now()); settings.Battery != (batteryPolicy{Frequent: batteryWait, Hourly: batterySame, Daily: batteryWait}) {
		t.Fatalf("the battery policy was not stored: %+v", settings.Battery)
	}
	for _, body := range []map[string]any{
		{"battery": map[string]any{"hourly": "slower"}}, // only Frequently runs slower
		{"battery": map[string]any{"frequent": "sometimes"}},
		{"battery": map[string]any{"low_battery_waits": "yes"}},
	} {
		post("/api/background/settings", body, http.StatusBadRequest)
	}
	t.Run("temporary promotion and demotion", func(t *testing.T) {
		execSQL(t, catalog.DB, `UPDATE background_settings SET placements_json='{"writing":"daily"}' WHERE host_id=`+sqlQuote(currentHost().ID))
		post("/api/background/settings", map[string]any{"temporary_cadences": map[string]any{"writing": cadenceHourly}}, http.StatusOK)
		settings, err := catalog.backgroundSettings(ctx, time.Now())
		if err != nil || settings.cadence(writing) != cadenceHourly {
			t.Fatalf("promoted schedule: %+v (%v)", settings, err)
		}
		temporary := settings.Temporary["writing"]
		expires, ok := parseTime(temporary.ExpiresAt)
		if !ok || time.Until(expires) < temporaryCadenceDuration-time.Minute || time.Until(expires) > temporaryCadenceDuration {
			t.Fatalf("temporary schedule must last one week: %+v", temporary)
		}
		// The scheduler and rendered column use the same effective cadence.
		plans := backgroundPlan(settings, backgroundInputs{deferred: map[string]deferredRow{"authorship": {generation: 2, completed: 1, changedAt: formatTime(time.Now().Add(-2 * time.Hour))}}}, time.Now())
		for _, plan := range plans {
			if plan.ID == "writing" && plan.Cadence != cadenceHourly {
				t.Fatalf("scheduler ignored the temporary cadence: %+v", plan)
			}
		}
		items := get()["view"].(map[string]any)["items"].([]any)
		for _, raw := range items {
			item := raw.(map[string]any)
			if item["id"] == "writing" && (item["column"] != cadenceHourly || item["temporary_cadence"] == nil || len(item["cadence_moves"].([]any)) != 3) {
				t.Fatalf("temporary schedule missing from view: %+v", item)
			}
		}
		// Normal settings saves and automatic placement changes cannot end or
		// extend a temporary choice. Expiration is checked on read, after restart too.
		post("/api/background/settings", map[string]any{"daily_at": "06:00"}, http.StatusOK)
		execSQL(t, catalog.DB, `UPDATE background_settings SET placements_json='{"writing":"frequent"}' WHERE host_id=`+sqlQuote(currentHost().ID))
		settings, err = catalog.backgroundSettings(ctx, time.Now())
		if err != nil || settings.cadence(writing) != cadenceHourly || settings.Temporary["writing"] != temporary {
			t.Fatalf("temporary schedule did not survive settings reload: %+v (%v)", settings, err)
		}
		settings, err = catalog.backgroundSettings(ctx, expires)
		if err != nil || settings.cadence(writing) != cadenceFrequent || len(settings.Temporary) != 0 {
			t.Fatalf("expiration did not restore automatic placement: %+v (%v)", settings, err)
		}
		post("/api/background/settings", map[string]any{"temporary_cadences": map[string]any{"writing": cadenceDaily}}, http.StatusOK)
		settings, _ = catalog.backgroundSettings(ctx, time.Now())
		if settings.cadence(writing) != cadenceDaily {
			t.Fatal("temporary demotion was not applied")
		}
		post("/api/background/settings", map[string]any{"temporary_cadences": map[string]any{"writing": ""}}, http.StatusOK)
		settings, _ = catalog.backgroundSettings(ctx, time.Now())
		if settings.cadence(writing) != cadenceFrequent || len(settings.Temporary) != 0 {
			t.Fatal("ending a temporary schedule did not restore automatic placement")
		}
		post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": cadenceDaily}, "temporary_cadences": map[string]any{"writing": cadenceHourly}}, http.StatusOK)
		settings, _ = catalog.backgroundSettings(ctx, time.Now())
		expires, _ = parseTime(settings.Temporary["writing"].ExpiresAt)
		settings, _ = catalog.backgroundSettings(ctx, expires)
		if settings.cadence(writing) != cadenceDaily {
			t.Fatal("expiration did not restore the persistent override")
		}
		post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": cadenceManual}}, http.StatusOK)
		settings, _ = catalog.backgroundSettings(ctx, time.Now())
		if len(settings.Temporary) != 0 || settings.cadence(writing) != cadenceManual || len(itemCadenceMoves(writing, settings)) != 3 {
			t.Fatal("a permanent choice must end the temporary schedule")
		}
		post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": ""}}, http.StatusOK)
	})
	for _, body := range []map[string]any{
		{"temporary_cadences": "hourly"},
		{"temporary_cadences": map[string]any{"writing": nil}},
		{"temporary_cadences": map[string]any{"writing": "sometimes"}},
		{"temporary_cadences": map[string]any{"nothing": cadenceHourly}},
	} {
		post("/api/background/settings", body, http.StatusBadRequest)
	}
	t.Run("all items move between all four columns", func(t *testing.T) {
		for _, item := range backgroundItems {
			for _, cadence := range append(slices.Clone(cadences), cadenceManual) {
				post("/api/background/settings", map[string]any{"temporary_cadences": map[string]any{item.ID: cadence}}, http.StatusOK)
				settings, err := catalog.backgroundSettings(ctx, time.Now())
				if err != nil || settings.cadence(item) != cadence || len(itemCadenceMoves(item, settings)) != 3 {
					t.Fatalf("%s did not move to %s: %+v (%v)", item.ID, cadence, settings, err)
				}
				later, err := catalog.backgroundSettings(ctx, time.Now().Add(temporaryCadenceDuration+time.Minute))
				if err != nil || later.cadence(item) != later.usual(item) {
					t.Fatalf("%s did not return to its usual schedule: %+v (%v)", item.ID, later, err)
				}
			}
			post("/api/background/settings", map[string]any{"temporary_cadences": map[string]any{item.ID: ""}}, http.StatusOK)
		}
	})
	post("/api/background/run", map[string]any{"item": "writing"}, http.StatusAccepted)
	if !server.peekRequested()["writing"] {
		t.Fatal("Run now asks the next pass for the item")
	}
	t.Run("manual work stays queued after a pass accepts it", func(t *testing.T) {
		post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": cadenceManual}}, http.StatusOK)
		state := func() string {
			t.Helper()
			_, views := server.operationsDocumentViews(ctx, false)
			view := server.backgroundView(ctx, views, nil, time.Now())
			for _, item := range view["items"].([]map[string]any) {
				if item["id"] == "writing" {
					return asString(item["state"])
				}
			}
			return ""
		}
		if got := state(); got != "queued" {
			t.Fatalf("requested manual item: %s", got)
		}
		server.takeRequested()
		_, pass := server.begin(ctx, OperationSpec{Kind: "background.update", Variant: "requested", Trigger: triggerUser})
		pass.Plan(backgroundSteps([]string{"upgrade", "writing"})...)
		pass.Step("Library upgrade", "", -1)
		if got := state(); got != "queued" {
			t.Fatalf("accepted manual item behind upgrade: %s", got)
		}
		pass.Finish(nil)
		if got := state(); got != "manual" {
			t.Fatalf("after the pass, the saved manual cadence resumes: %s", got)
		}
		post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": ""}}, http.StatusOK)
		// A successful bounded pass can leave messages waiting until its next
		// interval. That backlog must not be presented as updated.
		catalog.recordBackgroundRun(ctx, "writing", time.Now(), true, nil)
		execSQL(t, catalog.DB, `INSERT INTO authorship_pending(message_id,revision) VALUES('waiting','revision')`)
		if got := state(); got != "queued" {
			t.Fatalf("remaining writing backlog after a successful pass: %s", got)
		}
		execSQL(t, catalog.DB, `DELETE FROM authorship_pending WHERE message_id='waiting'`)
	})
	post("/api/background/run", map[string]any{"item": "conversations"}, http.StatusAccepted)
	if !server.indexRequested() {
		t.Fatal("Run now of new conversations asks for an index whatever the schedule")
	}
	post("/api/background/run", map[string]any{"item": "nothing"}, http.StatusBadRequest)
	// Ad hoc's run: every item set to only when you ask.
	post("/api/background/run", map[string]any{"cadence": cadenceManual}, http.StatusAccepted)
	if requested := server.peekRequested(); !requested["backup"] || requested["findings"] {
		t.Fatalf("running the manual cadence asks for exactly its items: %v", requested)
	}
	if status := getStatus(t, server, "/api/status"); status.raw["background"] == nil {
		t.Fatal("the status snapshot carries the schedule")
	}
}

// pmset's answers on a desktop, a laptop on battery, a laptop charging and
// in Low Power Mode.
func TestParsePowerReadsTheSourceAndCharge(t *testing.T) {
	for _, test := range []struct {
		name, batt, settings string
		want                 powerState
	}{
		{"a Mac with no battery", "Now drawing from 'AC Power'\n", " lowpowermode         0\n", powerState{Percent: -1}},
		{"on battery", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=4653155)\t85%; discharging; 5:01 remaining present: true\n", "", powerState{Battery: true, OnBattery: true, Percent: 85}},
		{"charging", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=4653155)\t12%; charging; 1:10 remaining present: true\n", " lowpowermode         1\n", powerState{Battery: true, Percent: 12, LowPower: true}},
	} {
		if got := parsePower(test.batt, test.settings); got != test.want {
			t.Errorf("%s: %+v, want %+v", test.name, got, test.want)
		}
	}
	// Tests never run pmset: an unreadable source counts as plugged in.
	if got := readPower(context.Background()); got.OnBattery || got.Battery {
		t.Fatalf("an unreadable power source counts as plugged in: %+v", got)
	}
}

// On battery each cadence runs as plugged in, less often or waits; a low
// battery or Low Power Mode makes everything wait. Plugged in, nothing
// changes.
func TestBatteryPolicyByPowerSource(t *testing.T) {
	policy := defaultBatteryPolicy()
	plugged := powerState{Battery: true, Percent: 80}
	battery := powerState{Battery: true, OnBattery: true, Percent: 80}
	low := powerState{Battery: true, OnBattery: true, Percent: lowBatteryPercent - 1}
	saving := powerState{Battery: true, OnBattery: true, Percent: 80, LowPower: true}
	for _, test := range []struct {
		name    string
		power   powerState
		cadence string
		want    string
	}{
		{"plugged in", plugged, cadenceHourly, batterySame},
		{"frequent on battery, by default", battery, cadenceFrequent, batterySlower},
		{"hourly on battery, by default", battery, cadenceHourly, batteryWait},
		{"daily on battery, by default", battery, cadenceDaily, batteryWait},
		{"low battery", low, cadenceFrequent, batteryWait},
		{"Low Power Mode", saving, cadenceFrequent, batteryWait},
	} {
		if got := policy.onPower(test.cadence, test.power); got != test.want {
			t.Errorf("%s: %s, want %s", test.name, got, test.want)
		}
	}
	policy.LowWaits, policy.Hourly = false, batterySame
	if policy.onPower(cadenceFrequent, low) != batterySlower || policy.onPower(cadenceHourly, battery) != batterySame {
		t.Fatal("the stored choices apply on battery")
	}
}

// On battery, work the policy holds waits (and says so), Frequently's
// scheduled index runs less often, and Run now still runs.
func TestBackgroundPlanFollowsTheBatteryPolicy(t *testing.T) {
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.Local)
	settings := backgroundSettings{Mode: "on", Interval: 300, DailyAt: "03:00", Overrides: map[string]string{"writing": cadenceHourly}, Placements: map[string]string{}, Battery: defaultBatteryPolicy()}
	settings.sync.NextAt = formatTime(at.Add(time.Minute))
	in := backgroundInputs{runs: map[string]backgroundRunRow{}, power: powerState{Battery: true, OnBattery: true, Percent: 80},
		deferred: map[string]deferredRow{"authorship": {generation: 2, completed: 1, changedAt: formatTime(at.Add(-time.Hour)), completedAt: formatTime(at.Add(-2 * time.Hour))}}}
	find := func(id string) itemPlan {
		for _, plan := range backgroundPlan(settings, in, at) {
			if plan.ID == id {
				return plan
			}
		}
		t.Fatalf("%s is not in the plan", id)
		return itemPlan{}
	}
	if writing := find("writing"); writing.Due || !writing.WaitsForPower || !writing.Next.IsZero() {
		t.Fatalf("hourly work waits until plugged in: %+v", writing)
	}
	if conversations := find("conversations"); !conversations.Next.Equal(at.Add(time.Minute + batterySlowerInterval - 5*time.Minute)) {
		t.Fatalf("scheduled indexing runs every %s on battery: next %s", batterySlowerInterval, conversations.Next)
	}
	in.requested = map[string]bool{"writing": true}
	if !find("writing").Due {
		t.Fatal("Run now runs on battery")
	}
	in.requested, in.power.OnBattery = nil, false
	if writing := find("writing"); !writing.Due || writing.WaitsForPower {
		t.Fatalf("plugged in, hourly work runs: %+v", writing)
	}
}

// The headline says what waits on battery and what still runs.
func TestBatteryHeadline(t *testing.T) {
	battery := powerState{Battery: true, OnBattery: true, Percent: 80}
	headline := batteryHeadline(defaultBatteryPolicy(), battery)
	if headline["title"] != "On battery" || headline["text"] != "Hourly and daily work waits until this Mac is plugged in. New conversations come in every 15 min." {
		t.Fatalf("on battery: %v", headline)
	}
	if low := batteryHeadline(defaultBatteryPolicy(), powerState{Battery: true, OnBattery: true, Percent: 9}); low["title"] != "Battery at 9%" {
		t.Fatalf("a low battery says so: %v", low)
	}
}

// With background updates on by default there may be no schedule row: the
// first scheduled index stores one, so the next waits its interval instead
// of running again a second later.
func TestScheduledIndexStoresTheDefaultSchedule(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	settings, err := catalog.syncSettings(ctx)
	if err != nil || !settings.Enabled || settings.NextAt != "" {
		t.Fatalf("a Mac with no schedule row has the default, unstored: %+v %v", settings, err)
	}
	if !server.runAutomatic(ctx, settings) {
		t.Fatal("the scheduled index did not run")
	}
	stored, err := catalog.syncSettings(ctx)
	next, ok := parseTime(stored.NextAt)
	if err != nil || !stored.Enabled || !ok || next.Before(time.Now().Add(4*time.Minute)) {
		t.Fatalf("the default schedule was not stored with the next run an interval away: %+v %v", stored, err)
	}
	background, _ := catalog.backgroundSettings(ctx, time.Now())
	if automaticDue(background, powerState{}, false, time.Now()) {
		t.Fatal("the scheduled index is due again at once")
	}
}

// The scheduled index runs when its time comes, on battery only as its
// policy allows; Run now runs it whatever the mode, power or time.
func TestAutomaticIndexIsDueByModePowerAndRunNow(t *testing.T) {
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.Local)
	on := backgroundSettings{Mode: "on", Battery: defaultBatteryPolicy()}
	on.sync = syncSettings{Enabled: true, Interval: 300, NextAt: formatTime(at.Add(-time.Second))}
	battery := powerState{Battery: true, OnBattery: true, Percent: 80}
	if !automaticDue(on, powerState{}, false, at) {
		t.Fatal("a due index does not run")
	}
	if automaticDue(on, battery, false, at) {
		t.Fatal("on battery, Frequently runs every 15 min, not every 5")
	}
	if !automaticDue(on, battery, false, at.Add(slowerBy(300))) {
		t.Fatal("on battery, the index runs 15 min after the last")
	}
	paused := on
	paused.Mode = "paused"
	waiting := on
	waiting.Battery.Frequent = batteryWait
	for name, settings := range map[string]backgroundSettings{"paused": paused, "waiting for power": waiting} {
		if automaticDue(settings, battery, false, at) {
			t.Errorf("%s: the index ran", name)
		}
		if !automaticDue(settings, battery, true, at) {
			t.Errorf("%s: Run now did not run the index", name)
		}
	}
}

// A source whose latest index failed makes New conversations failed, and
// the headline says so; the sources scheduled indexing reads are listed,
// with a chosen subset in the item's line.
func TestBackgroundViewShowsSourceFailuresAndTheSourceFilter(t *testing.T) {
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Enabled: true}, {Name: "codex", Kind: "codex", Enabled: true}}
	server := testServer(t, config, catalog)
	host := sqlQuote(currentHost().ID)
	stamp := func(d time.Duration) string { return sqlQuote(formatTime(time.Now().Add(-d))) }
	execTx(t, catalog.DB, "INSERT INTO sync_settings(host_id,enabled,interval_seconds,sources_json,next_at) VALUES("+host+",1,300,'[\"claude\"]',"+stamp(-time.Minute)+")",
		"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error) VALUES("+host+",'claude',"+stamp(time.Minute)+","+stamp(time.Hour)+",'unreadable transcript')")
	response := apiCall(t, server, config.APIToken, http.MethodGet, "/api/background", nil)
	var document struct {
		View struct {
			Items    []map[string]any `json:"items"`
			Headline map[string]any   `json:"headline"`
		} `json:"view"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	var conversations map[string]any
	for _, item := range document.View.Items {
		if item["id"] == "conversations" {
			conversations = item
		}
	}
	if conversations["state"] != "failed" || conversations["error"] == nil || document.View.Headline["tone"] != "bad" {
		t.Fatalf("a failed source index is not reported: %v; headline %v", conversations, document.View.Headline)
	}
	sources, _ := conversations["sources"].([]any)
	if len(sources) != 2 || fmt.Sprint(conversations["sub"]) != "claude (1 of 2 sources)" {
		t.Fatalf("the source filter is not shown: %v, %v", sources, conversations["sub"])
	}
}

// A pass appears in /api/activity, marked changed when an item succeeded,
// so open pages refresh their data when it ends.
func TestBackgroundPassIsAnActivityRun(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	server.requestBackground("tools")
	if !server.backgroundPass(context.Background(), time.Now()) {
		t.Fatal("Run now ran nothing")
	}
	runs := server.activity()["runs"].([]map[string]any)
	if len(runs) == 0 || runs[0]["kind"] != backgroundRunKind || runs[0]["state"] != "complete" || runs[0]["changed"] != true {
		t.Fatalf("the pass is not a finished, changed run in /api/activity: %v", runs)
	}
}

// Stages before a due one come along only when their own item could run in
// this pass: not set to only when you ask, and not on a slower cadence.
func TestBackgroundPassHonorsHeldPrerequisites(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	host := sqlQuote(currentHost().ID)
	stamp := func(d time.Duration) string { return sqlQuote(formatTime(time.Now().Add(-d))) }
	pending := func(name string, changed time.Duration) string {
		return "INSERT INTO sync_deferred(name,generation,completed_generation,changed_at,completed_at,pending_since) VALUES('" + name + "',2,1," + stamp(changed) + "," + stamp(10*time.Minute) + "," + stamp(changed) + ")"
	}
	execTx(t, catalog.DB, "DELETE FROM sync_deferred", pending("identities", time.Minute), pending("git", time.Minute), pending("tools", time.Minute),
		// Your writing is hourly, and the library is not quiet.
		pending("authorship", time.Minute), pending("findings", time.Minute),
		"INSERT INTO background_runs(host_id,item,succeeded_at) VALUES("+host+",'preserve',"+stamp(0)+"),("+host+",'review',"+stamp(0)+")",
		"INSERT INTO background_settings(host_id,overrides_json,placements_json) VALUES("+host+",'{\"repositories\":\"manual\",\"writing\":\"hourly\"}','{\"findings\":\"frequent\"}')",
		setMetaSQL("findings_generation", findingsVersion+"/2026-10-26"))
	completed := func(name string) int64 {
		var generation int64
		if err := catalog.DB.QueryRow("SELECT completed_generation FROM sync_deferred WHERE name=?", name).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	if !server.backgroundPass(ctx, time.Now()) {
		t.Fatal("the frequent pass ran nothing")
	}
	if completed("tools") != 2 || completed("findings") != 2 {
		t.Fatalf("the frequent items did not run: tools %d, findings %d", completed("tools"), completed("findings"))
	}
	if completed("identities") != 1 || completed("git") != 1 {
		t.Fatal("a pass brought along stages set to only when you ask")
	}
	if completed("authorship") != 1 {
		t.Fatal("a frequent pass brought along an hourly stage that was not due")
	}
	// Findings measured; today's full pass is the daily review's.
	if generation, _ := catalog.metaValue(ctx, "findings_generation"); generation != findingsVersion+"/2026-10-26" {
		t.Fatalf("the findings item ran today's full pass: %s", generation)
	}
}

// The daily review reads what the captures before it wrote: the stages
// with work waiting come along, read from the catalog when the chain
// starts, not from the plan made before the pass.
func TestBackgroundReviewBringsAlongStagesPendingWhenTheChainStarts(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	settings, _ := catalog.backgroundSettings(ctx, time.Now())
	// The plan, made while nothing was waiting.
	execTx(t, catalog.DB, "DELETE FROM sync_deferred")
	plans := backgroundPlan(settings, server.backgroundInputsFor(ctx, nil), time.Now())
	// What a capture in the same pass writes.
	if err := catalog.writeTransaction(ctx, "test", markSyncDeferred); err != nil {
		t.Fatal(err)
	}
	if !server.runBackgroundPass(ctx, cadenceDaily, []string{"review"}, plans) {
		t.Fatal("the pass did not run")
	}
	for _, view := range server.ops.views() {
		if view.Spec.Kind != "background.update" {
			continue
		}
		for _, id := range []string{"repositories", "tools", "writing", "review"} {
			item, _ := backgroundItemByID(id)
			if !slices.ContainsFunc(view.Plan, func(step StepPlan) bool { return step.Name == item.Label }) {
				t.Fatalf("discovered prerequisite %s missing from pass plan: %+v", id, view.Plan)
			}
		}
		review, _ := backgroundItemByID("review")
		if view.Step.Name != review.Label || view.Step.Index != len(view.Plan) {
			t.Fatalf("pass still shows an earlier task after its chain: %+v", view.Step)
		}
	}
	var waiting int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM sync_deferred WHERE generation>completed_generation").Scan(&waiting); err != nil || waiting != 0 {
		t.Fatalf("%d stages were left waiting under today's review: %v", waiting, err)
	}
	if generation, _ := catalog.metaValue(ctx, "findings_generation"); generation != catalog.findingsGeneration() {
		t.Fatalf("the review did not run today's full pass: %s", generation)
	}
}

// Preserve originals on a Mac with no enabled sources still indexes what
// is captured in the library.
func TestPreserveIndexesCapturesWithoutLocalSources(t *testing.T) {
	catalog, config := testCatalog(t)
	ctx := context.Background()
	claude := filepath.Join(t.TempDir(), "claude")
	session := "11111111-1111-4111-8111-111111111111"
	writeSourceFile(t, filepath.Join(claude, "-proj", session+".jsonl"), claudeLine(session, "u1", "captured before", "/proj", 1))
	capturing := config
	capturing.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Enabled: true}}
	if summary, err := Capture(ctx, capturing, capturing.Sources, nil); err != nil || !summary.OK {
		t.Fatalf("capture: %+v %v", summary, err)
	}
	config.Sources = nil
	server := testServer(t, config, catalog)
	_, op := server.begin(ctx, OperationSpec{Kind: "background.update", Variant: cadenceDaily, Trigger: triggerSchedule})
	err := server.captureAndIndex(ctx, op, nil)
	op.Finish(err)
	var found int
	if err != nil || catalog.DB.QueryRow("SELECT COUNT(*) FROM messages WHERE text='captured before'").Scan(&found) != nil || found != 1 {
		t.Fatalf("the captured conversation was not indexed: %d, %v", found, err)
	}
}

// The day's Frequently cost counts the analysis passes after scheduled
// indexing, not only the indexing.
func TestBackgroundDayCostCountsFrequentPasses(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	start := time.Now().Add(-time.Hour)
	execTx(t, catalog.DB, fmt.Sprintf(`INSERT INTO operation_journal(id,root_id,kind,variant,trigger,host_id,process,pid,instance,state,started_at,updated_at,ended_at)
		VALUES('p','p','background.update','frequent','schedule',%s,'service',1,'i','succeeded','%s','%s','%s')`, sqlQuote(currentHost().ID), formatTime(start), formatTime(start), formatTime(start.Add(time.Minute))))
	settings, _ := catalog.backgroundSettings(context.Background(), time.Now())
	cost := server.backgroundDayCost(context.Background(), settings, map[string]itemCost{}, time.Now())
	frequent := cost["cadences"].([]map[string]any)[0]
	if seconds := frequent["seconds"].(float64); seconds < 59 || seconds > 61 {
		t.Fatalf("the frequent pass is not counted: %v", frequent)
	}
}

func TestBackgroundWritingEstimatesAndRefinementHaveIndependentSchedules(t *testing.T) {
	at := time.Date(2026, 11, 5, 4, 0, 0, 0, time.Local)
	settings := backgroundSettings{Mode: "on", Interval: 300, DailyAt: "03:00", Overrides: map[string]string{}, Placements: map[string]string{"writing": cadenceDaily}}
	in := backgroundInputs{writingPending: true, writingRefinedAt: formatTime(at.Add(-24 * time.Hour)), runs: map[string]backgroundRunRow{"writing": {succeeded: formatTime(at.Add(-time.Minute))}}}
	plans := func() map[string]itemPlan {
		out := map[string]itemPlan{}
		for _, plan := range backgroundPlan(settings, in, at) {
			out[plan.ID] = plan
		}
		return out
	}
	p := plans()
	if quick := p["writing"]; quick.Cadence != cadenceFrequent || quick.Due || !quick.Next.Equal(at.Add(4*time.Minute)) {
		t.Fatalf("quick estimates ignore obsolete adaptive placement and wait for their interval: %+v", quick)
	}
	if daily := p["writing-refine"]; daily.Cadence != cadenceDaily || !daily.Due {
		t.Fatalf("recent estimates suppressed daily refinement: %+v", daily)
	}
	in.writingRefinedAt = formatTime(at.Add(-30 * time.Minute))
	if daily := plans()["writing-refine"]; daily.Due || daily.Pending == false || !daily.Next.After(at) {
		t.Fatalf("completed daily work ran again: %+v", daily)
	}
}
