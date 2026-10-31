package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
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
			if item["id"] == "writing" && (item["column"] != cadenceHourly || item["temporary_cadence"] == nil || len(item["cadence_moves"].([]any)) != 2) {
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
		if len(settings.Temporary) != 0 || settings.cadence(writing) != cadenceManual || len(itemCadenceMoves(writing, settings)) != 0 {
			t.Fatal("a permanent choice must end the temporary schedule")
		}
		post("/api/background/settings", map[string]any{"overrides": map[string]any{"writing": ""}}, http.StatusOK)
	})
	for _, body := range []map[string]any{
		{"temporary_cadences": "hourly"},
		{"temporary_cadences": map[string]any{"writing": nil}},
		{"temporary_cadences": map[string]any{"writing": cadenceManual}},
		{"temporary_cadences": map[string]any{"writing": "sometimes"}},
		{"temporary_cadences": map[string]any{"search": cadenceDaily}},
		{"temporary_cadences": map[string]any{"backup": cadenceDaily}},
		{"temporary_cadences": map[string]any{"nothing": cadenceHourly}},
	} {
		post("/api/background/settings", body, http.StatusBadRequest)
	}
	post("/api/background/run", map[string]any{"item": "writing"}, http.StatusAccepted)
	if !server.peekRequested()["writing"] {
		t.Fatal("Run now asks the next pass for the item")
	}
	post("/api/background/run", map[string]any{"item": "conversations"}, http.StatusAccepted)
	if !server.indexRequested() {
		t.Fatal("Run now of new conversations asks for an index whatever the schedule")
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
		"INSERT INTO background_settings(host_id,overrides_json,placements_json) VALUES("+host+",'{\"repositories\":\"manual\"}','{\"findings\":\"frequent\"}')",
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
