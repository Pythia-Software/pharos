package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Background updates (docs/background-work.md §11): where measured cost
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

// Each cadence's rule, at a fixed moment: Frequently at once, Hourly an hour
// after its last run once the library is quiet (and no later than 90 min),
// Daily at the daily time; a failure waits before it runs again; an
// override, a pause or Off stops it, and Run now runs it anyway.
func TestBackgroundPlanRunsEachCadenceWhenDue(t *testing.T) {
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.Local)
	ago := func(d time.Duration) string { return formatTime(at.Add(-d)) }
	settings := func() backgroundSettings {
		return backgroundSettings{Mode: "on", Interval: 300, DailyAt: "03:00", Overrides: map[string]string{}, Placements: map[string]string{}}
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

// An override can only slow an item down; a fixed item has none; an
// opt-in item runs only when chosen.
func TestBackgroundOverridesOnlySlowItemsDown(t *testing.T) {
	settings := backgroundSettings{Overrides: map[string]string{"writing": cadenceFrequent, "tools": cadenceDaily, "search": cadenceManual}, Placements: map[string]string{}}
	for id, want := range map[string]string{"writing": cadenceHourly, "tools": cadenceDaily, "search": cadenceFrequent, "backup": cadenceManual} {
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
func TestBackgroundPassRunsDueStagesAndRecordsThem(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx := context.Background()
	stamp := func(d time.Duration) string { return sqlQuote(formatTime(time.Now().Add(-d))) }
	execTx(t, catalog.DB, "DELETE FROM sync_deferred",
		// Today's daily work is done.
		"INSERT INTO background_runs(host_id,item,succeeded_at) VALUES("+sqlQuote(currentHost().ID)+",'preserve',"+stamp(0)+")",
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
	if settings, _ := catalog.backgroundSettings(ctx, time.Now()); settings.cadence(writing) != cadenceHourly {
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
	post("/api/background/run", map[string]any{"item": "writing"}, http.StatusAccepted)
	if !server.peekRequested()["writing"] {
		t.Fatal("Run now asks the next pass for the item")
	}
	post("/api/background/run", map[string]any{"item": "nothing"}, http.StatusBadRequest)
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
	settings := backgroundSettings{Mode: "on", Interval: 300, DailyAt: "03:00", Overrides: map[string]string{}, Placements: map[string]string{}, Battery: defaultBatteryPolicy()}
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
