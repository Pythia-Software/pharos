package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The schedule's words and state for surfaces: the status snapshot's
// background section (the drive panel) and GET /api/background (Settings →
// Background updates). Every sentence is the server's; pages add ages.

// runningItem maps an operation kind (or kind/variant) to the item whose
// work it is.
var runningItem = map[string]string{
	"index.changes.automatic": "conversations", "index.changes.manual": "conversations", "index.sources.legacy": "conversations",
	"maintenance.library-view": "search", "analysis.github": "repositories", "analysis.identities": "repositories", "analysis.git": "repositories",
	"analysis.tools": "tools", "analysis.authorship": "writing", "analysis.findings": "findings",
	"background.capture/sources": "other-sources", "background.capture/preserve": "preserve", "capture.this-mac": "preserve", "index.captures": "preserve", "library.update": "preserve",
	"library.upgrade": "upgrade", "tools.ledger.build": "upgrade", "backup.local": "backup",
}

func itemOfView(view opView) string {
	if item, ok := runningItem[view.Spec.Kind+"/"+view.Spec.Variant]; ok {
		return item
	}
	if strings.HasPrefix(view.Spec.Kind, "maintenance.search.") {
		return "search"
	}
	return runningItem[view.Spec.Kind]
}

func liveState(state string) bool {
	return state == opRunning || state == opQueued || state == opBlocked || state == opStopping
}

// runningItems is the items live operations are working on, the deepest
// first (a pass's stage before the pass), with the operation of each.
func runningItems(views []opView) (map[string]bool, []string) {
	items, order := map[string]bool{}, []string{}
	for index := len(views) - 1; index >= 0; index-- {
		view := views[index]
		if !liveState(view.State) {
			continue
		}
		if item := itemOfView(view); item != "" && !items[item] {
			items[item] = true
			order = append(order, item)
		}
	}
	return items, order
}

// durationWords is a typical duration: "a few seconds", "about 40 s",
// "about 6 min", "about 1 h 20 min".
func durationWords(seconds float64) string {
	switch {
	case seconds < 10:
		return "a few seconds"
	case seconds < 60:
		return fmt.Sprintf("about %d s", int(math.Round(seconds/5)*5))
	case seconds < 3600:
		return fmt.Sprintf("about %d min", int(math.Round(seconds/60)))
	}
	hours, minutes := int(seconds/3600), int(math.Round(math.Mod(seconds, 3600)/60))
	if minutes == 0 {
		return fmt.Sprintf("about %d h", hours)
	}
	return fmt.Sprintf("about %d h %d min", hours, minutes)
}

// clockWords is a local time of day: "5:00 PM".
func clockWords(at time.Time) string {
	return at.In(time.Local).Format("3:04 PM")
}

func dailyAtWords(dailyAt string) string {
	clock, err := time.Parse("15:04", dailyAt)
	if err != nil {
		return dailyAt
	}
	return clock.Format("3:04 PM")
}

// cadenceDescription is when each cadence runs, in words.
func cadenceDescription(cadence string, settings backgroundSettings) string {
	switch cadence {
	case cadenceFrequent:
		minutes := max(1, settings.Interval/60)
		return fmt.Sprintf("Every %d min, as soon as there’s new work", minutes)
	case cadenceHourly:
		return fmt.Sprintf("At most once an hour, after %d quiet minutes", int(hourlyQuiet.Minutes()))
	case cadenceDaily:
		return "Overnight at " + dailyAtWords(settings.DailyAt) + ", or the first quiet moment after"
	}
	return ""
}

// itemAsOf is when an item's data was last brought current, from the
// freshness ledger where it says, else its last scheduled run.
func (s *Server) itemAsOf(item backgroundItem, plan itemPlan, rows map[string]freshnessRow, in backgroundInputs) string {
	oldest := func(domains ...string) string {
		found := ""
		for _, domain := range domains {
			row, ok := rows[domain]
			if !ok || row.AsOf == nil {
				continue
			}
			if found == "" || canonicalTime(*row.AsOf) < canonicalTime(found) {
				found = *row.AsOf
			}
		}
		return found
	}
	sources := func(other bool) []string {
		domains := []string{}
		for _, source := range s.Config().Sources {
			if source.Enabled && automaticProvider(source.Kind) != other {
				domains = append(domains, "conv:"+source.Name)
			}
		}
		return domains
	}
	value := ""
	switch item.ID {
	case "conversations", "search":
		value = oldest(sources(false)...)
	case "other-sources":
		value = oldest(sources(true)...)
	case "repositories":
		value = oldest("an.identities", "an.git")
	case "tools":
		value = oldest("an.tools")
	case "writing":
		value = oldest("an.authorship")
	case "findings", "review":
		value = oldest("findings")
	case "preserve":
		prefix := "capt:" + currentHost().ID + "/"
		domains := []string{}
		for domain := range rows {
			if strings.HasPrefix(domain, prefix) {
				domains = append(domains, domain)
			}
		}
		value = oldest(domains...)
	case "upgrade":
		value = oldest("upgrade")
	case "backup":
		value = in.lastBackup
	}
	if value == "" && !plan.LastDone.IsZero() {
		value = formatTime(plan.LastDone)
	}
	return value
}

// itemFailure is a freshness row saying the item's last update failed,
// outside the schedule's own record (a manual index's chain).
func itemFailure(item backgroundItem, rows map[string]freshnessRow) string {
	domains := map[string][]string{"repositories": {"an.identities", "an.git"}, "tools": {"an.tools"}, "writing": {"an.authorship"}, "findings": {"findings"}}[item.ID]
	for _, domain := range domains {
		if row, ok := rows[domain]; ok && row.Reason != nil && (row.Reason.Code == "failed" || row.Reason.Code == "blocked") {
			return row.Reason.Text
		}
	}
	return ""
}

// stateLabels are the item and cadence states' words.
var stateLabels = map[string]string{"running": "Updating now", "queued": "Up next", "failed": "Failed", "missed": "Missed its daily time", "paused": "Paused",
	"manual": "Only when you ask", "current": "Updated", "never": "Not run yet", "unavailable": "Not set up", "power": "Waits until plugged in"}

// stateRank orders states from the one a cadence's row should show first.
var stateRank = []string{"failed", "running", "missed", "queued", "never", "power", "current", "paused", "manual", "unavailable"}

// backgroundView is the schedule as surfaces show it.
func (s *Server) backgroundView(ctx context.Context, views []opView, freshness []freshnessRow, at time.Time) map[string]any {
	settings, err := s.Catalog.backgroundSettings(ctx, at)
	if err != nil {
		return nil
	}
	costs := s.cachedCosts(ctx)
	in := s.backgroundInputsFor(ctx, s.peekRequested())
	plans := backgroundPlan(settings, in, at)
	rows := map[string]freshnessRow{}
	for _, row := range freshness {
		rows[row.Domain] = row
	}
	live, order := runningItems(views)
	items := []map[string]any{}
	byCadence := map[string][]map[string]any{}
	failed, missed, waiting := []map[string]any{}, false, false
	for _, plan := range plans {
		item, _ := backgroundItemByID(plan.ID)
		cost, measured := costs[item.ID]
		state := "current"
		asOf := s.itemAsOf(item, plan, rows, in)
		problem := plan.Error
		if problem == "" {
			problem = itemFailure(item, rows)
		}
		switch {
		case live[item.ID]:
			state = "running"
		case item.ID == "backup" && in.backupDestination == "":
			state = "unavailable"
		case plan.Cadence == cadenceManual:
			state = "manual"
		case settings.Mode == "paused":
			state = "paused"
		case settings.Mode == "manual":
			state = "manual"
		case problem != "":
			state = "failed"
		case plan.WaitsForPower:
			state = "power"
		case plan.Due:
			state = "queued"
		case plan.Missed:
			state = "missed"
		case asOf == "" && plan.Pending:
			state = "never"
		}
		column := plan.Cadence
		if column == cadenceManual {
			column = settings.placed(item)
		}
		entry := map[string]any{"id": item.ID, "label": item.Label, "verb": item.Verb, "feeds": item.Feeds, "why": item.Why, "sub": itemSub(item, in),
			"cadence": plan.Cadence, "cadence_label": cadenceLabels[plan.Cadence], "column": column, "placed": settings.placed(item),
			"adaptive": item.Adaptive, "fixed": item.Fixed, "opt_in": item.OptIn, "override": settings.Overrides[item.ID],
			"state": state, "state_label": stateLabels[state], "as_of": nullable(asOf), "next_at": nullableTime(plan.Next), "error": nullable(problem),
			"choices": itemChoices(item, settings), "cost": nil, "note": nil}
		if measured {
			entry["cost"] = map[string]any{"seconds": cost.Seconds, "runs": cost.Runs, "class": costClass(cost, true), "text": durationWords(cost.Seconds)}
		}
		if item.Adaptive && settings.placed(item) != cadenceFrequent {
			entry["note"] = "Moves to Frequently once a run usually takes under a minute."
		}
		if item.ID == "backup" && in.backupDestination != "" {
			entry["label"] = "Back up to " + in.backupDestination
		}
		items = append(items, entry)
		byCadence[column] = append(byCadence[column], entry)
		if state == "failed" {
			failed = append(failed, entry)
		}
		missed = missed || state == "missed"
		waiting = waiting || state == "power"
	}
	rowsOut := []map[string]any{}
	for _, cadence := range cadences {
		entries := byCadence[cadence]
		if len(entries) == 0 {
			continue
		}
		state, asOf, next := "unavailable", "", time.Time{}
		for _, entry := range entries {
			if slices.Index(stateRank, entry["state"].(string)) < slices.Index(stateRank, state) {
				state = entry["state"].(string)
			}
			if value, ok := entry["as_of"].(string); ok && entry["state"] == "current" && (asOf == "" || canonicalTime(value) < canonicalTime(asOf)) {
				asOf = value
			}
			if value, ok := parseTime(firstString(entry["next_at"])); ok && (next.IsZero() || value.Before(next)) {
				next = value
			}
		}
		label := stateLabels[state]
		if state == "failed" {
			count := 0
			for _, entry := range entries {
				if entry["state"] == "failed" {
					count++
				}
			}
			label = fmt.Sprintf("%d failed", count)
		}
		ids := []string{}
		for _, entry := range entries {
			ids = append(ids, entry["id"].(string))
		}
		rowsOut = append(rowsOut, map[string]any{"id": cadence, "label": cadenceLabels[cadence], "description": cadenceDescription(cadence, settings),
			"state": state, "state_label": label, "as_of": nullable(asOf), "next_at": nullableTime(next), "items": ids})
	}
	// Update everything now: a manual index, which runs the whole chain.
	everything, heaviest, heaviestSeconds := 0.0, "", 0.0
	for _, item := range backgroundItems {
		if cost, ok := costs[item.ID]; ok && (item.ID == "conversations" || len(item.Stages) > 0) {
			everything += cost.Seconds
			if cost.Seconds > heaviestSeconds {
				heaviest, heaviestSeconds = item.Label, cost.Seconds
			}
		}
	}
	everythingText := ""
	if everything > 0 {
		everythingText = "Everything now takes " + durationWords(everything)
		if heaviestSeconds > everything/2 {
			everythingText += ", mostly “" + heaviest + "”"
		}
		everythingText += "."
	}
	return map[string]any{"mode": settings.Mode, "paused_until": nullable(settings.PausedUntil), "items": items, "cadences": rowsOut,
		"headline": backgroundHeadline(settings, items, order, failed, missed, waiting, in.power), "power": in.power,
		"everything": map[string]any{"seconds": everything, "text": everythingText}}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

// itemSub is the line under an item's name.
func itemSub(item backgroundItem, in backgroundInputs) string {
	switch item.ID {
	case "conversations":
		return "Claude Code, Codex, Conductor and Antigravity"
	case "other-sources":
		return strings.Join(in.otherSources, ", ")
	}
	return item.Feeds
}

// itemChoices are the cadences an item may be set to: its placement or
// slower, or only when you ask.
func itemChoices(item backgroundItem, settings backgroundSettings) []map[string]any {
	if item.Fixed {
		return nil
	}
	if item.OptIn {
		return []map[string]any{{"value": "", "label": "Only when I ask"}, {"value": cadenceDaily, "label": "Daily"}}
	}
	placed := settings.placed(item)
	choices := []map[string]any{{"value": "", "label": "Automatically (" + strings.ToLower(cadenceLabels[placed]) + ")"}}
	for _, cadence := range cadences[cadenceRank(placed)+1:] {
		choices = append(choices, map[string]any{"value": cadence, "label": cadenceLabels[cadence]})
	}
	return append(choices, map[string]any{"value": cadenceManual, "label": "Only when I ask"})
}

// backgroundHeadline is the panel's first line: what is happening, and what
// it means for the pages.
func backgroundHeadline(settings backgroundSettings, items []map[string]any, running []string, failed []map[string]any, missed, waiting bool, power powerState) map[string]any {
	byID := map[string]map[string]any{}
	for _, item := range items {
		byID[item["id"].(string)] = item
	}
	switch {
	case settings.Mode == "paused":
		until, _ := parseTime(settings.PausedUntil)
		return map[string]any{"tone": "paused", "title": "Background updates paused until " + clockWords(until),
			"text":   "New conversations aren’t being added, and nothing else updates until then.",
			"action": map[string]any{"label": "Resume now", "path": "/api/background/settings", "body": map[string]any{"mode": "on"}}}
	case settings.Mode == "manual":
		return map[string]any{"tone": "paused", "title": "Background updates are off",
			"text":   "Nothing updates until you choose Update everything now, or turn background updates on.",
			"action": map[string]any{"label": "Turn on", "path": "/api/background/settings", "body": map[string]any{"mode": "on"}}}
	case len(running) > 0:
		item := byID[running[0]]
		return map[string]any{"tone": "busy", "title": item["verb"], "text": firstString(item["feeds"]) + " update when this finishes."}
	case len(failed) > 0:
		item := failed[0]
		text := "Everything else is current."
		if len(failed) > 1 {
			text = fmt.Sprintf("%d other updates failed too.", len(failed)-1)
		}
		if next, ok := parseTime(firstString(item["next_at"])); ok {
			text += " Pharos tries again at " + clockWords(next) + "."
		}
		return map[string]any{"tone": "bad", "title": fmt.Sprintf("%s couldn’t update", item["label"]), "text": text, "tech": item["error"]}
	case waiting:
		return batteryHeadline(settings.Battery, power)
	case missed:
		return map[string]any{"tone": "behind", "title": "Catching up on daily work",
			"text":   "Daily work didn’t run at its time, perhaps because this Mac was asleep or the library was away. It runs at the next quiet moment.",
			"action": map[string]any{"label": "Run daily work now", "path": "/api/background/run", "body": map[string]any{"cadence": cadenceDaily}}}
	}
	return map[string]any{"tone": "good", "title": "Up to date", "text": "Everything is on schedule."}
}

// batteryHeadline says what waits on battery, and what still runs.
func batteryHeadline(policy batteryPolicy, power powerState) map[string]any {
	if policy.onPower(cadenceDaily, power) == batteryWait && policy.onPower(cadenceHourly, power) == batteryWait && policy.onPower(cadenceFrequent, power) == batteryWait {
		title := "On battery"
		if power.LowPower {
			title = "Low Power Mode"
		} else if power.Percent >= 0 && power.Percent < lowBatteryPercent {
			title = fmt.Sprintf("Battery at %d%%", power.Percent)
		}
		return map[string]any{"tone": "paused", "title": title, "text": "Background updates wait until this Mac is plugged in."}
	}
	waits := []string{}
	for _, cadence := range []string{cadenceHourly, cadenceDaily} {
		if policy.of(cadence) == batteryWait {
			waits = append(waits, cadence)
		}
	}
	text := ""
	if len(waits) > 0 {
		text = strings.ToUpper(waits[0][:1]) + waits[0][1:]
		if len(waits) > 1 {
			text += " and " + waits[1]
		}
		text += " work waits until this Mac is plugged in."
	}
	switch policy.of(cadenceFrequent) {
	case batterySlower:
		text += fmt.Sprintf(" New conversations come in every %d min.", int(batterySlowerInterval.Minutes()))
	case batterySame:
		text += " New conversations come in as usual."
	}
	return map[string]any{"tone": "paused", "title": "On battery", "text": strings.TrimSpace(text)}
}

// backgroundDayCost is the last 24 hours of background work on this Mac:
// measured for scheduled indexing and for passes the journal kept, else
// projected from the items' measured costs.
func (s *Server) backgroundDayCost(ctx context.Context, settings backgroundSettings, costs map[string]itemCost, at time.Time) map[string]any {
	since := at.Add(-24 * time.Hour)
	frequent, checks, idle, peak := 0.0, 0, 0, int64(0)
	activeHours := map[string]bool{}
	for _, sample := range s.Catalog.automaticSamples(ctx, 400) {
		finished, ok := parseTime(sample.FinishedAt)
		if !ok || finished.Before(since) {
			continue
		}
		frequent += sample.Seconds
		checks++
		peak = max(peak, sample.Peak)
		if sample.Workspaces == 0 {
			idle++
		} else {
			activeHours[finished.Format("2006-01-02T15")] = true
		}
	}
	measured := func(variant string) (float64, int) {
		var seconds sql.NullFloat64
		var runs int
		_ = s.Catalog.reader().QueryRowContext(ctx, `SELECT SUM((julianday(ended_at)-julianday(started_at))*86400),COUNT(*) FROM operation_journal
			WHERE host_id=? AND kind='background.update' AND variant=? AND started_at>=? AND ended_at IS NOT NULL`, currentHost().ID, variant, formatTime(since)).Scan(&seconds, &runs)
		return seconds.Float64, runs
	}
	projected := func(cadence string) (float64, string) {
		total, heaviest, heaviestSeconds := 0.0, "", 0.0
		for _, item := range backgroundItems {
			cost, ok := costs[item.ID]
			if !ok || settings.cadence(item) != cadence || item.ID == "conversations" {
				continue
			}
			total += cost.Seconds
			if cost.Seconds > heaviestSeconds {
				heaviest, heaviestSeconds = item.Label, cost.Seconds
			}
		}
		return total, heaviest
	}
	cadenceCosts := []map[string]any{{"id": cadenceFrequent, "label": cadenceLabels[cadenceFrequent], "seconds": frequent, "measured": true,
		"text": fmt.Sprintf("%d checks, %d found nothing new", checks, idle)}}
	for _, cadence := range []string{cadenceHourly, cadenceDaily} {
		seconds, runs := measured(cadence)
		entry := map[string]any{"id": cadence, "label": cadenceLabels[cadence], "seconds": seconds, "measured": runs > 0, "text": fmt.Sprintf("%d runs", runs)}
		if runs == 0 {
			each, heaviest := projected(cadence)
			times := 1
			if cadence == cadenceHourly {
				times = max(1, len(activeHours))
			}
			entry["seconds"] = each * float64(times)
			entry["text"] = "projected"
			if heaviest != "" {
				entry["text"] = fmt.Sprintf("projected: %s × %d, mostly %s", durationWords(each), times, heaviest)
			}
		}
		cadenceCosts = append(cadenceCosts, entry)
	}
	total := 0.0
	for _, entry := range cadenceCosts {
		total += entry["seconds"].(float64)
	}
	return map[string]any{"window_hours": 24, "seconds": total, "text": durationWords(total), "peak_bytes": peak, "cadences": cadenceCosts}
}

// getBackground answers GET /api/background.
func (s *Server) getBackground(w http.ResponseWriter, r *http.Request) {
	at := time.Now()
	settings, err := s.Catalog.backgroundSettings(r.Context(), at)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	_, views := s.operationsDocumentViews(r.Context(), false)
	freshness := s.freshness(r.Context(), views, false)
	writeJSON(w, map[string]any{
		"settings": map[string]any{"mode": settings.Mode, "paused_until": nullable(settings.PausedUntil), "interval_seconds": settings.Interval,
			"daily_at": settings.DailyAt, "hold_when_busy": settings.HoldWhenBusy, "overrides": settings.Overrides, "battery": settings.Battery,
			"battery_choices": batteryChoices, "battery_slower_minutes": int(batterySlowerInterval.Minutes()), "low_battery_percent": lowBatteryPercent},
		"power": s.cachedPower(r.Context()),
		"view":  s.backgroundView(r.Context(), views, freshness, at),
		"rule":  map[string]any{"frequent_seconds": cadenceLimit[cadenceFrequent], "hourly_seconds": cadenceLimit[cadenceHourly]},
		"cost":  s.backgroundDayCost(r.Context(), settings, s.cachedCosts(r.Context()), at),
		"descriptions": map[string]any{cadenceFrequent: cadenceDescription(cadenceFrequent, settings), cadenceHourly: cadenceDescription(cadenceHourly, settings),
			cadenceDaily: cadenceDescription(cadenceDaily, settings)},
	}, 200)
}

// postBackgroundSettings answers POST /api/background/settings: mode (on,
// pause, manual; pause_minutes, default 60), interval_seconds, daily_at
// ("HH:MM"), hold_when_busy, and overrides ({item: cadence, or "" for
// automatic}).
func (s *Server) postBackgroundSettings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	ctx := r.Context()
	at := time.Now()
	settings, err := s.Catalog.backgroundSettings(ctx, at)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	indexing := settings.sync
	paused := settings.PausedUntil
	if value, ok := body["mode"]; ok {
		switch value {
		case "on":
			indexing.Enabled, paused = true, ""
		case "pause":
			minutes := 60.0
			if raw, ok := body["pause_minutes"]; ok {
				number, valid := number(raw)
				if !valid || number < 1 || number > 24*60 {
					writeError(w, errors.New("pause_minutes must be from 1 to 1440"), 400)
					return
				}
				minutes = number
			}
			indexing.Enabled, paused = true, formatTime(at.Add(time.Duration(minutes*float64(time.Minute))))
		case "manual":
			indexing.Enabled, paused = false, ""
		default:
			writeError(w, errors.New("mode must be on, pause or manual"), 400)
			return
		}
	}
	if raw, ok := body["interval_seconds"]; ok {
		value, valid := number(raw)
		if !valid || value < 60 || value > 3600 || value != float64(int(value)) {
			writeError(w, errors.New("interval_seconds must be an integer from 60 to 3600"), 400)
			return
		}
		indexing.Interval = int(value)
	}
	if raw, ok := body["daily_at"]; ok {
		value, _ := raw.(string)
		if _, err := time.Parse("15:04", value); err != nil || len(value) != 5 {
			writeError(w, errors.New("daily_at must be a time such as 03:00"), 400)
			return
		}
		settings.DailyAt = value
	}
	if raw, ok := body["hold_when_busy"]; ok {
		value, valid := raw.(bool)
		if !valid {
			writeError(w, errors.New("hold_when_busy must be true or false"), 400)
			return
		}
		settings.HoldWhenBusy = value
	}
	if raw, ok := body["battery"]; ok {
		values, valid := raw.(map[string]any)
		if !valid {
			writeError(w, errors.New(`battery must be an object such as {"hourly": "wait"}`), 400)
			return
		}
		for key, value := range values {
			if key == "low_battery_waits" {
				waits, ok := value.(bool)
				if !ok {
					writeError(w, errors.New("low_battery_waits must be true or false"), 400)
					return
				}
				settings.Battery.LowWaits = waits
				continue
			}
			choice, _ := value.(string)
			if !slices.Contains(batteryChoices[key], choice) {
				writeError(w, fmt.Errorf("on battery, %q cannot be %q", key, choice), 400)
				return
			}
			switch key {
			case cadenceFrequent:
				settings.Battery.Frequent = choice
			case cadenceHourly:
				settings.Battery.Hourly = choice
			case cadenceDaily:
				settings.Battery.Daily = choice
			}
		}
	}
	if raw, ok := body["overrides"]; ok {
		values, valid := raw.(map[string]any)
		if !valid {
			writeError(w, errors.New("overrides must be an object of item names to cadences"), 400)
			return
		}
		for id, rawCadence := range values {
			item, known := backgroundItemByID(id)
			cadence, _ := rawCadence.(string)
			if !known || !slices.ContainsFunc(itemChoices(item, settings), func(choice map[string]any) bool { return choice["value"] == cadence }) {
				writeError(w, fmt.Errorf("%q cannot be set to %q", id, cadence), 400)
				return
			}
			if cadence == "" {
				delete(settings.Overrides, id)
			} else {
				settings.Overrides[id] = cadence
			}
		}
	}
	indexing.NextAt = at.Add(time.Duration(indexing.Interval) * time.Second).UTC().Format(time.RFC3339Nano)
	s.auto.mu.Lock()
	if (!indexing.Enabled || paused != "") && s.auto.cancel != nil {
		s.auto.cancel()
	}
	err = s.Catalog.writeTransaction(ctx, "background settings", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_settings(host_id,enabled,interval_seconds,sources_json,next_at) VALUES(?,?,?,?,?)
			ON CONFLICT(host_id) DO UPDATE SET enabled=excluded.enabled,interval_seconds=excluded.interval_seconds,next_at=excluded.next_at`,
			currentHost().ID, indexing.Enabled, indexing.Interval, jsonText(indexing.Sources), indexing.NextAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO background_settings(host_id,paused_until,daily_at,hold_when_busy,overrides_json,placements_json,battery_json,updated_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(host_id) DO UPDATE SET paused_until=excluded.paused_until,daily_at=excluded.daily_at,hold_when_busy=excluded.hold_when_busy,
			overrides_json=excluded.overrides_json,battery_json=excluded.battery_json,updated_at=excluded.updated_at`,
			currentHost().ID, nilIfEmpty(paused), settings.DailyAt, settings.HoldWhenBusy, jsonText(settings.Overrides), jsonText(settings.Placements), jsonText(settings.Battery), now())
		return err
	})
	s.auto.mu.Unlock()
	if err != nil {
		writeError(w, err, 500)
		return
	}
	s.wakeBackground()
	s.getBackground(w, r)
}

// postBackgroundRun answers POST /api/background/run: {"item": ID} runs one
// item now, {"cadence": "daily"} every item of a cadence.
func (s *Server) postBackgroundRun(w http.ResponseWriter, r *http.Request, body map[string]any) {
	ids := []string{}
	if id := firstString(body["item"]); id != "" {
		if _, ok := backgroundItemByID(id); !ok {
			writeError(w, fmt.Errorf("unknown item %q", id), 400)
			return
		}
		ids = append(ids, id)
	}
	if cadence := firstString(body["cadence"]); cadence != "" {
		settings, err := s.Catalog.backgroundSettings(r.Context(), time.Now())
		if err != nil {
			writeError(w, err, 500)
			return
		}
		for _, item := range backgroundItems {
			if settings.cadence(item) == cadence {
				ids = append(ids, item.ID)
			}
		}
	}
	if len(ids) == 0 {
		writeError(w, errors.New(`body is {"item": ID} or {"cadence": "frequent" | "hourly" | "daily"}`), 400)
		return
	}
	run := []string{}
	for _, id := range ids {
		item, _ := backgroundItemByID(id)
		switch {
		case item.ID == "conversations":
			// The scheduled index, now.
			_, err := s.Catalog.DB.ExecContext(r.Context(), "UPDATE sync_settings SET next_at=? WHERE host_id=?", now(), currentHost().ID)
			if err != nil {
				writeError(w, err, 500)
				return
			}
		case item.continuous:
		default:
			run = append(run, id)
		}
	}
	s.requestBackground(run...)
	writeJSON(w, map[string]any{"accepted": true, "items": ids}, 202)
}
