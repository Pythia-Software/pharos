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
// Background Processing). Every sentence is the server's; pages add ages.

// runningItem maps an operation kind (or kind/variant) to the item whose
// work it is.
var runningItem = map[string]string{
	"index.changes.automatic": "conversations", "index.changes.manual": "conversations", "index.sources.legacy": "conversations",
	"maintenance.library-view": "search", kindMetrics: "search", "analysis.github": "repositories", "analysis.identities": "repositories", "analysis.git": "repositories",
	"analysis.authorship.refine": "writing-refine", "analysis.tools": "tools", "analysis.authorship": "writing", "analysis.peers": "writing", "analysis.findings": "findings", "analysis.labels": "labels", "analysis.agent_events": "agent_events", "analysis.engagement": "engagement",
	"background.capture/sources": "other-sources", "background.capture/preserve": "preserve", "capture.this-mac": "preserve", "index.captures": "preserve", "library.update": "preserve",
	"forge.reviews":   "pull-request-reviews",
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

// waitingItems includes items accepted by a pass but not yet reached. A
// manual cadence describes future scheduling, not the state of an accepted
// Run now while its prerequisites are running.
func waitingItems(views []opView) map[string]bool {
	waiting := map[string]bool{}
	for _, view := range views {
		if view.Spec.Kind != "background.update" || !liveState(view.State) {
			continue
		}
		for index, step := range view.Plan {
			if index+1 <= view.Step.Index {
				continue
			}
			for _, item := range backgroundItems {
				if item.Label == step.Name {
					waiting[item.ID] = true
				}
			}
		}
	}
	return waiting
}

// stoppableItems is the operation that Stop ends for each item a stoppable
// operation is working on: a top-level one, as the snapshot offers Stop.
func stoppableItems(views []opView) map[string]OpID {
	stops := map[string]OpID{}
	for _, view := range views {
		if view.Kind != nil && view.Kind.Stop != "" && view.Parent == "" && !view.Foreign && (view.State == opRunning || view.State == opBlocked) {
			if item := itemOfView(view); item != "" {
				stops[item] = view.ID
			}
		}
	}
	return stops
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

// typicalWords is the middle of an item's recent runs: "Typically 2–3 h",
// "Typically about 6 min", "Typically up to 30 s".
func typicalWords(cost itemCost) string {
	unit, size, step := "h", 3600.0, 0.5
	switch {
	case cost.High < 10:
		return "Typically a few seconds"
	case cost.High < 60:
		unit, size, step = "s", 1, 5
	case cost.High < 3600:
		unit, size, step = "min", 60, 1
	}
	round := func(seconds float64) float64 { return math.Round(seconds/size/step) * step }
	low, high := round(cost.Low), round(cost.High)
	switch {
	case low == high:
		return fmt.Sprintf("Typically about %g %s", high, unit)
	case low == 0:
		return fmt.Sprintf("Typically up to %g %s", high, unit)
	}
	return fmt.Sprintf("Typically %g–%g %s", low, high, unit)
}

// clockWords is a local time of day: "5:00 PM".
func clockWords(at time.Time) string {
	return at.In(time.Local).Format("3:04 PM")
}

// untilWords is a time ahead of now: its clock time, with "tomorrow" or
// its weekday when it is not today.
func untilWords(until, now time.Time) string {
	until, now = until.In(time.Local), now.In(time.Local)
	day := func(at time.Time) time.Time {
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.Local)
	}
	switch days := int(day(until).Sub(day(now)).Hours()/24 + 0.5); {
	case days <= 0:
		return clockWords(until)
	case days == 1:
		return "tomorrow at " + clockWords(until)
	case days < 7:
		return until.Format("Monday") + " at " + clockWords(until)
	}
	return until.Format("Jan 2") + " at " + clockWords(until)
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
		return "Daily at " + dailyAtWords(settings.DailyAt) + ", or the first quiet moment after"
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
	value := ""
	switch item.ID {
	case "conversations", "search":
		value = oldest(sourceDomains(s.Config(), false)...)
	case "other-sources":
		value = oldest(sourceDomains(s.Config(), true)...)
	case "repositories":
		value = oldest("an.identities", "an.git")
	case "tools":
		value = oldest("an.tools")
	case "writing":
		value = oldest("an.authorship")
	case "writing-refine":
		value = oldest("an.authorship.refined")
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
	if value == "" && !plan.LastDone.IsZero() && item.ID != "writing" {
		value = formatTime(plan.LastDone)
	}
	return value
}

// itemFailure is a freshness row saying the item's last update failed,
// outside the schedule's own record: a manual index's chain, or a source
// whose latest index failed (scheduled indexing records that per source,
// not as a background run).
func (s *Server) itemFailure(item backgroundItem, rows map[string]freshnessRow) string {
	domains := map[string][]string{"repositories": {"an.identities", "an.git"}, "tools": {"an.tools"}, "writing": {"an.authorship"}, "findings": {"findings"}}[item.ID]
	failed := map[string]bool{"failed": true, "blocked": true}
	if item.ID == "conversations" || item.ID == "other-sources" {
		domains, failed = sourceDomains(s.Config(), item.ID == "other-sources"), map[string]bool{"source-error": true, "interrupted": true}
	}
	for _, domain := range domains {
		if row, ok := rows[domain]; ok && row.Reason != nil && failed[row.Reason.Code] {
			if item.ID == "conversations" || item.ID == "other-sources" {
				return row.Label + ": " + row.Reason.Text
			}
			return row.Reason.Text
		}
	}
	return ""
}

// sourceDomains are the conv:<source> domains of this Mac's enabled
// sources that scheduled indexing reads, or (other) those read in full.
func sourceDomains(config Config, other bool) []string {
	domains := []string{}
	for _, source := range config.Sources {
		if source.Enabled && automaticProvider(source.Kind) != other {
			domains = append(domains, "conv:"+source.Name)
		}
	}
	return domains
}

// stateLabels are the item and cadence states' words.
var stateLabels = map[string]string{"running": "Updating now", "queued": "Up next", "failed": "Failed", "missed": "Missed its daily time", "paused": "Paused",
	"manual": "Only when you ask", "current": "Updated", "never": "Not run yet", "unavailable": "Not set up", "disabled": "Off", "power": "Waits until plugged in"}

// stateRank orders states from the one a cadence's row should show first.
var stateRank = []string{"failed", "running", "missed", "queued", "never", "power", "current", "paused", "manual", "unavailable", "disabled"}

// backgroundView is the schedule as surfaces show it.
func (s *Server) backgroundView(ctx context.Context, views []opView, freshness []freshnessRow, at time.Time) map[string]any {
	settings, err := s.Catalog.backgroundSettings(ctx, at)
	if err != nil {
		return nil
	}
	costs, perDay := s.cachedMeasures(ctx)
	in := s.backgroundInputsFor(ctx, s.peekRequested())
	plans := backgroundPlan(settings, in, at)
	// The step details own model settings. Keep disabled models reachable in
	// the unscheduled column without adding them to the execution plan.
	for _, item := range backgroundItems {
		if item.requiresLabels && !in.labels || item.requiresAgentEvents && !in.agentEvents || item.requiresPullRequestReviews && !in.pullRequestReviews {
			plans = append(plans, itemPlan{ID: item.ID, Cadence: cadenceManual})
		}
	}
	rows := map[string]freshnessRow{}
	for _, row := range freshness {
		rows[row.Domain] = row
	}
	live, order := runningItems(views)
	queued := waitingItems(views)
	stops := stoppableItems(views)
	items := []map[string]any{}
	byCadence := map[string][]map[string]any{}
	unscheduled := []string{}
	failed, missed, waiting := []map[string]any{}, false, false
	for _, plan := range plans {
		item, _ := backgroundItemByID(plan.ID)
		cost, measured := costs[item.ID]
		state := "current"
		asOf := s.itemAsOf(item, plan, rows, in)
		problem := plan.Error
		if problem == "" {
			problem = s.itemFailure(item, rows)
		}
		switch {
		case live[item.ID]:
			state = "running"
		case item.requiresLabels && !in.labels || item.requiresAgentEvents && !in.agentEvents || item.requiresPullRequestReviews && !in.pullRequestReviews:
			state = "disabled"
		case in.requested[item.ID] || queued[item.ID]:
			state = "queued"
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
		case item.ID == "writing" && plan.Pending:
			state = "queued"
		case asOf == "" && plan.Pending:
			state = "never"
		}
		column := plan.Cadence
		if column == cadenceManual {
			column = settings.placed(item)
		}
		entry := map[string]any{"id": item.ID, "label": item.Label, "verb": item.Verb, "feeds": item.Feeds, "why": item.Why, "sub": itemSub(item, in),
			"cadence": plan.Cadence, "cadence_label": cadenceLabels[plan.Cadence], "column": column, "placed": settings.placed(item), "usual_cadence": settings.usual(item),
			"adaptive": item.Adaptive, "fixed": item.Fixed, "opt_in": item.OptIn, "override": settings.Overrides[item.ID],
			"state": state, "state_label": stateLabels[state], "as_of": nullable(asOf), "next_at": nullableTime(plan.Next), "error": nullable(problem),
			"choices": itemChoices(item, settings), "cost": nil, "note": nil}
		entry["cadence_moves"] = itemCadenceMoves(item, settings)
		// The running operation Stop ends, where it can be stopped.
		entry["stop"] = nil
		if id, ok := stops[item.ID]; ok {
			entry["stop"] = map[string]any{"id": id}
		}
		entry["temporary_cadence"] = nil
		if temporary, ok := settings.Temporary[item.ID]; ok {
			entry["temporary_cadence"] = temporary
			entry["note"] = "Temporary schedule; your usual schedule resumes after one week."
		}
		if measured {
			entry["cost"] = map[string]any{"seconds": cost.Seconds, "runs": cost.Runs, "class": costClass(cost, true), "text": durationWords(cost.Seconds),
				"typical": typicalWords(cost)}
		}
		if note := placementNote(item, settings.placed(item), cost, measured, perDay); note != "" && entry["temporary_cadence"] == nil {
			entry["note"] = note
		}
		if item.ID == "writing" && in.writingBacklog.Pending > 0 {
			entry["backlog"] = in.writingBacklog
			entry["note"] = in.writingBacklog.note(at)
		}
		if item.ID == "backup" && in.backupDestination != "" {
			entry["label"] = "Back up to " + in.backupDestination
		}
		if item.ID == "conversations" {
			// The sources scheduled indexing reads (sync_settings.sources_json,
			// empty for all), which Settings lets you choose.
			sources, chosen := []map[string]any{}, []string{}
			for _, source := range s.autoSources(syncSettings{}) {
				selected := len(settings.sync.Sources) == 0 || slices.Contains(settings.sync.Sources, source.Name)
				sources = append(sources, map[string]any{"name": source.Name, "selected": selected})
				if selected {
					chosen = append(chosen, source.Name)
				}
			}
			entry["sources"] = sources
			if len(chosen) < len(sources) {
				entry["sub"] = fmt.Sprintf("%s (%d of %d sources)", strings.Join(chosen, ", "), len(chosen), len(sources))
			}
		}
		items = append(items, entry)
		if plan.Cadence == cadenceManual {
			// Only when you ask: on no schedule, so in no cadence's row.
			unscheduled = append(unscheduled, item.ID)
		} else {
			byCadence[column] = append(byCadence[column], entry)
		}
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
	// Index changes now: a manual index, which runs the whole chain.
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
		everythingText = "Index changes takes " + durationWords(everything)
		if heaviestSeconds > everything/2 {
			everythingText += ", mostly “" + heaviest + "”"
		}
		everythingText += "."
	}
	return map[string]any{"mode": settings.Mode, "paused_until": nullable(settings.PausedUntil), "items": items, "cadences": rowsOut, "unscheduled": unscheduled,
		"headline": backgroundHeadline(settings, items, order, failed, missed, waiting, in.power, at), "power": in.power,
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
		return "Your automatic sources"
	case "other-sources":
		return strings.Join(in.otherSources, ", ")
	case "labels":
		if in.labelerProblem != "" {
			return "Labeling cannot run: " + in.labelerProblem
		}
		if in.labelsWaiting != "" {
			return "Labeling is " + in.labelsWaiting
		}
	case "agent_events":
		if in.labelerProblem != "" {
			return "Finding agent events cannot run: " + in.labelerProblem
		}
		if in.agentEventsWaiting != "" {
			return "Finding agent events is " + in.agentEventsWaiting
		}
	}
	return item.Feeds
}

// placementNote says, for an adaptive item measurement has not placed at
// Frequently, what keeps it from the next faster schedule: the sentence under
// its line in Settings. A run's own length, or what a day of runs would add
// up to, with the numbers the rule used (placeCadence).
func placementNote(item backgroundItem, placed string, cost itemCost, measured bool, perDay map[string]float64) string {
	if !item.Adaptive || cadenceRank(placed) == 0 || cadenceRank(placed) >= len(cadences) {
		return ""
	}
	faster := cadences[cadenceRank(placed)-1]
	name := cadenceLabels[faster]
	switch {
	case item.fastest != "" && cadenceRank(faster) < cadenceRank(item.fastest):
		return fmt.Sprintf("Runs %s at most: its results would be no better for running more often.", cadenceLabels[item.fastest])
	case !measured || cost.Runs < placementRuns:
		return fmt.Sprintf("Moves to %s once its runs are measured and fit there.", name)
	}
	fixed := max(cost.Slow, cost.Fixed)
	run, day := cadenceLoad(faster, cost, fixed, perDay)
	switch {
	case day > 0.75*cadenceDayBudget[faster]:
		return fmt.Sprintf("Stays %s: each run costs %s before any new work, and %s would repeat that about %d times a day (%s a day).",
			cadenceLabels[placed], durationWords(fixed), name, int(math.Round(perDay[faster])), durationWords(day))
	case run > 0.75*cadenceLimit[faster]:
		return fmt.Sprintf("Stays %s: with the work that arrives, a run at %s would take %s, and %s is for runs well under %d min.",
			cadenceLabels[placed], name, durationWords(run), name, int(cadenceLimit[faster]/60))
	}
	return fmt.Sprintf("Moves to %s at the next update: its runs now fit there.", name)
}

// itemChoices are the cadences an item may be set to: its placement or
// slower, or only when you ask.
func itemChoices(item backgroundItem, settings backgroundSettings) []map[string]any {
	if item.Fixed {
		return nil
	}
	if item.OptIn {
		return []map[string]any{{"value": "", "label": "Only when you ask"}, {"value": cadenceDaily, "label": "Daily"}}
	}
	placed := settings.placed(item)
	choices := []map[string]any{{"value": "", "label": "Automatically (" + strings.ToLower(cadenceLabels[placed]) + ")"}}
	for _, cadence := range cadences[cadenceRank(placed)+1:] {
		choices = append(choices, map[string]any{"value": cadence, "label": cadenceLabels[cadence]})
	}
	return append(choices, map[string]any{"value": cadenceManual, "label": "Only when you ask"})
}

// Every item can move to any of the other columns for a week. These
// buttons offer the same choices as dragging, including without a pointer.
func itemCadenceMoves(item backgroundItem, settings backgroundSettings) []map[string]any {
	moves := []map[string]any{}
	for _, cadence := range append(slices.Clone(cadences), cadenceManual) {
		if cadence != settings.cadence(item) {
			moves = append(moves, map[string]any{"cadence": cadence, "label": "Move to " + cadenceLabels[cadence] + " for a week"})
		}
	}
	return moves
}

// backgroundHeadline is the panel's first line: what is happening, and what
// it means for the pages.
func backgroundHeadline(settings backgroundSettings, items []map[string]any, running []string, failed []map[string]any, missed, waiting bool, power powerState, at time.Time) map[string]any {
	byID := map[string]map[string]any{}
	for _, item := range items {
		byID[item["id"].(string)] = item
	}
	switch {
	case settings.Mode == "paused":
		until, _ := parseTime(settings.PausedUntil)
		return map[string]any{"tone": "paused", "title": "Background Processing paused until " + untilWords(until, at),
			"text":   "New conversations are not being added, and nothing else updates until then.",
			"action": map[string]any{"label": "Resume now", "path": "/api/background/settings", "body": map[string]any{"mode": "on"}}}
	case settings.Mode == "manual":
		return map[string]any{"tone": "paused", "title": "Background Processing is off",
			"text":   "Nothing updates until you choose Index changes now, or turn Background Processing on.",
			"action": map[string]any{"label": "Turn on", "path": "/api/background/settings", "body": map[string]any{"mode": "on"}}}
	case len(running) > 0:
		item := byID[running[0]]
		return map[string]any{"tone": "busy", "title": item["verb"], "text": "Updates when this finishes: " + firstString(item["feeds"]) + "."}
	case len(failed) > 0:
		item := failed[0]
		text := "Everything else is current."
		if len(failed) > 1 {
			text = fmt.Sprintf("%d other updates failed too.", len(failed)-1)
		}
		if next, ok := parseTime(firstString(item["next_at"])); ok {
			text += " Pharos tries again at " + clockWords(next) + "."
		}
		return map[string]any{"tone": "bad", "title": fmt.Sprintf("%s could not update", item["label"]), "text": text, "tech": item["error"]}
	case waiting:
		return batteryHeadline(settings.Battery, power)
	case missed:
		return map[string]any{"tone": "behind", "title": "Catching up on daily work",
			"text":   "Daily work did not run at its time, perhaps because this Mac was asleep or the library was away. It runs at the next quiet moment.",
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
		return map[string]any{"tone": "paused", "title": title, "text": "Background Processing waits until this Mac is plugged in."}
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
	// peak is null when no run in the window read its RSS.
	frequent, checks, idle, peak := 0.0, 0, 0, (*int64)(nil)
	activeHours := map[string]bool{}
	for _, sample := range s.Catalog.automaticSamples(ctx, 400) {
		finished, ok := parseTime(sample.FinishedAt)
		if !ok || finished.Before(since) {
			continue
		}
		frequent += sample.Seconds
		checks++
		if sample.Peak != nil && (peak == nil || *sample.Peak > *peak) {
			peak = sample.Peak
		}
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
	// Frequently is scheduled indexing and the analysis passes after it.
	passes, passRuns := measured(cadenceFrequent)
	frequentText := fmt.Sprintf("%d checks, %d found nothing new", checks, idle)
	if passRuns > 0 {
		frequentText += fmt.Sprintf(", %d analysis passes", passRuns)
	}
	cadenceCosts := []map[string]any{{"id": cadenceFrequent, "label": cadenceLabels[cadenceFrequent], "seconds": frequent + passes, "measured": true, "text": frequentText}}
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
	schedules, usage := s.backgroundCostsView(r.Context(), settings, at)
	writeJSON(w, map[string]any{
		"work":                 s.workSettings(),
		"labels":               s.labelsSettings(),
		"agent_events":         s.agentEventsSettings(r.Context()),
		"pull_request_reviews": s.pullRequestReviewsSettings(r.Context()),
		"settings": map[string]any{"mode": settings.Mode, "paused_until": nullable(settings.PausedUntil), "interval_seconds": settings.Interval,
			"daily_at": settings.DailyAt, "hold_when_busy": settings.HoldWhenBusy, "overrides": settings.Overrides, "temporary_cadences": settings.Temporary, "battery": settings.Battery,
			"battery_choices": batteryChoices, "battery_slower_minutes": int(batterySlowerInterval.Minutes()), "low_battery_percent": lowBatteryPercent},
		"power": s.cachedPower(r.Context()),
		"view":  s.backgroundView(r.Context(), views, freshness, at),
		"rule": map[string]any{"frequent_seconds": cadenceLimit[cadenceFrequent], "hourly_seconds": cadenceLimit[cadenceHourly],
			"frequent_day_seconds": cadenceDayBudget[cadenceFrequent], "hourly_day_seconds": cadenceDayBudget[cadenceHourly]},
		"cost":           s.backgroundDayCost(r.Context(), settings, s.cachedCosts(r.Context()), at),
		"schedule_costs": schedules, "usage": usage,
		"descriptions": map[string]any{cadenceFrequent: cadenceDescription(cadenceFrequent, settings), cadenceHourly: cadenceDescription(cadenceHourly, settings),
			cadenceDaily: cadenceDescription(cadenceDaily, settings)},
	}, 200)
}

// postBackgroundSettings answers POST /api/background/settings: mode (on,
// pause, manual; pause_minutes, default 60), interval_seconds, daily_at
// ("HH:MM"), hold_when_busy, and overrides ({item: cadence, or "" for
// automatic}), temporary_cadences ({item: cadence for seven days,
// or "" to end the temporary schedule}).
func (s *Server) postBackgroundSettings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	ctx := r.Context()
	at := time.Now()
	settings, err := s.Catalog.backgroundSettings(ctx, at)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	temporaryChanges := map[string]string{}
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
			temporaryChanges[id] = ""
		}
	}
	if raw, ok := body["temporary_cadences"]; ok {
		values, valid := raw.(map[string]any)
		if !valid {
			writeError(w, errors.New("temporary_cadences must be an object of item names to cadences"), 400)
			return
		}
		for id, rawCadence := range values {
			_, known := backgroundItemByID(id)
			cadence, valid := rawCadence.(string)
			if !known || !valid || (cadence != "" && !validBackgroundCadence(cadence)) {
				writeError(w, fmt.Errorf("%q cannot temporarily run on %q", id, cadence), 400)
				return
			}
			temporaryChanges[id] = cadence
		}
	}
	indexing.NextAt = at.Add(time.Duration(indexing.Interval) * time.Second).UTC().Format(time.RFC3339Nano)
	s.auto.mu.Lock()
	if (!indexing.Enabled || paused != "") && s.auto.cancel != nil {
		s.auto.cancel(errAutomaticPaused)
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
		if err != nil {
			return err
		}
		for id, cadence := range temporaryChanges {
			if cadence == "" {
				_, err = tx.ExecContext(ctx, "DELETE FROM background_temporary_cadences WHERE host_id=? AND item=?", currentHost().ID, id)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO background_temporary_cadences(host_id,item,cadence,expires_at) VALUES(?,?,?,?)
				 ON CONFLICT(host_id,item) DO UPDATE SET cadence=excluded.cadence,expires_at=excluded.expires_at`, currentHost().ID, id, cadence, formatTime(at.Add(temporaryCadenceDuration)))
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	s.auto.mu.Unlock()
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if s.work != nil {
		s.work.runner.readPause(context.WithoutCancel(r.Context()))
	}
	s.wakeBackground()
	s.getBackground(w, r)
}

// postBackgroundRun answers POST /api/background/run: {"item": ID} runs one
// item now, {"cadence": "daily"} every item of a cadence ("manual": every
// item set to only when you ask, the panel's Ad hoc).
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
		writeError(w, errors.New(`body is {"item": ID} or {"cadence": "frequent" | "hourly" | "daily" | "manual"}`), 400)
		return
	}
	run := []string{}
	for _, id := range ids {
		item, _ := backgroundItemByID(id)
		switch {
		case item.ID == "conversations":
			// One scheduled index now, whatever the mode, power or time;
			// the saved schedule is unchanged.
			s.requestIndex()
		case item.continuous:
			s.Catalog.resumeLibraryRefresh()
			run = append(run, id)
		case item.requiresLabels && !s.Config().Labels.Enabled:
			// Labels are off here (the broker asks every library after a
			// download).
		case item.requiresAgentEvents && !s.agentEventsEnabled(r.Context()):
		case item.requiresEngagement && !s.Config().Engagement.Enabled:
		case item.requiresAsk && !s.askEnabled(r.Context()):
		case item.requiresPullRequestReviews && !s.Config().PullRequestReviews:
		default:
			run = append(run, id)
		}
	}
	s.requestBackground(run...)
	writeJSON(w, map[string]any{"accepted": true, "items": ids}, 202)
}

// scheduleCost is what one schedule choice would cost an item: a day of its
// runs there, or one run for "only when you ask". A nil figure is not
// measured yet.
type scheduleCost struct {
	Cadence string `json:"cadence"`
	Label   string `json:"label"`
	// Current is the schedule the item runs on now.
	Current bool `json:"current"`
	// Per is "day", or "run" where nothing runs until asked.
	Per string `json:"per"`
	// Bound says how to read Seconds and the figures that follow from it:
	// "about" (worked out from the item's fixed and per-unit costs, or
	// measured), "up_to" (each run would have less to do than the measured
	// ones) or "at_least" (each run would have more).
	Bound      string   `json:"bound"`
	RunsPerDay *float64 `json:"runs_per_day"`
	Seconds    *float64 `json:"seconds"`
	CPUSeconds *float64 `json:"cpu_seconds"`
	GPUSeconds *float64 `json:"gpu_seconds"`
	PeakBytes  *int64   `json:"peak_bytes"`
	// Measured is true for a day that was measured rather than projected.
	Measured bool `json:"measured"`
}

// scheduleCosts is what each schedule the item can be given would cost a
// day, most frequent first, from its own measured runs. A day at a cadence
// is its runs there times the run's fixed cost, plus the day's work at its
// per-unit cost (cadenceLoad): the work costs the same whatever the cadence.
// An item that does not count its work is all fixed cost as far as its runs
// say, so away from the cadence they were measured at the figure is a
// bound: fewer, larger runs cost at least it, and more, smaller ones at most.
// CPU and GPU time follow the running time at the runs' measured rates, and
// memory is the most any run held.
//
// A fixed or continuous item has one row, its own schedule, with the day
// the ledger measured (day; nil when it has none).
func scheduleCosts(item backgroundItem, settings backgroundSettings, cost itemCost, measured bool, perDay map[string]float64, day *cpuSourceRow) []scheduleCost {
	current := settings.cadence(item)
	if item.Fixed || item.continuous {
		row := scheduleCost{Cadence: current, Label: cadenceLabels[current], Current: true, Per: "day", Bound: "about", Measured: day != nil}
		if current == cadenceManual {
			row.Per, row.Measured = "run", false
			return []scheduleCost{row}
		}
		if day != nil {
			row.Seconds, row.CPUSeconds, row.GPUSeconds, row.PeakBytes, row.RunsPerDay = &day.Seconds, day.CPUSeconds, day.GPUSeconds, day.PeakBytes, &day.Runs
		}
		return []scheduleCost{row}
	}
	offered := map[string]bool{current: true}
	if item.OptIn {
		offered[cadenceManual], offered[cadenceDaily] = true, true
	} else {
		for _, cadence := range append(slices.Clone(cadences), cadenceManual) {
			offered[cadence] = true
		}
	}
	rows := []scheduleCost{}
	for _, cadence := range append(slices.Clone(cadences), cadenceManual) {
		if !offered[cadence] {
			continue
		}
		row := scheduleCost{Cadence: cadence, Label: cadenceLabels[cadence], Current: cadence == current, Per: "day", Bound: "about", PeakBytes: cost.PeakBytes}
		var seconds *float64
		switch {
		case cadence == cadenceManual:
			row.Per = "run"
			if measured {
				seconds = &cost.Seconds
			}
		case measured:
			runs := max(1, perDay[cadence])
			row.RunsPerDay = &runs
			_, repeated := cadenceLoad(cadence, cost, cost.Fixed, perDay)
			seconds = new(repeated + cost.PerUnit*cost.UnitsPerDay)
			if at := defaultString(cost.RunsAt, current); !cost.Counted && at != cadence && cadenceRank(at) < len(cadences) {
				row.Bound = "at_least"
				if cadenceRank(cadence) < cadenceRank(at) {
					row.Bound = "up_to"
				}
			}
		}
		row.Seconds = seconds
		if seconds != nil && cost.CPUPerSecond != nil {
			row.CPUSeconds = new(*seconds * *cost.CPUPerSecond)
		}
		if seconds != nil && cost.GPUPerSecond != nil {
			row.GPUSeconds = new(*seconds * *cost.GPUPerSecond)
		}
		rows = append(rows, row)
	}
	return rows
}

// placementReason says why an item runs where it does: your choice, or for
// an adaptive item what measurement found (placementNote says what keeps it
// from the next faster schedule).
func placementReason(item backgroundItem, settings backgroundSettings, cost itemCost, measured bool, perDay map[string]float64) string {
	placed, current := settings.placed(item), settings.cadence(item)
	if _, temporary := settings.Temporary[item.ID]; temporary {
		return fmt.Sprintf("You moved it to %s for a week.", cadenceLabels[current])
	}
	switch {
	case item.Fixed:
		return ""
	case item.OptIn && current == cadenceManual:
		return "Off until you give it a schedule."
	case current != placed && item.Adaptive:
		return fmt.Sprintf("You chose %s. Left to its measured cost it would run %s.", cadenceLabels[current], cadenceLabels[placed])
	case current != placed || !item.Adaptive:
		return ""
	}
	if note := placementNote(item, placed, cost, measured, perDay); note != "" {
		return note
	}
	if !measured || cost.Runs < placementRuns {
		return fmt.Sprintf("Runs %s until its runs are measured.", cadenceLabels[placed])
	}
	return fmt.Sprintf("Runs %s: its measured runs fit there, under %s a run and %s a day of repeated work.",
		cadenceLabels[placed], durationPlain(cadenceLimit[placed]), durationPlain(cadenceDayBudget[placed]))
}

// durationPlain is a limit in words: "1 min", "2 h".
func durationPlain(seconds float64) string {
	return strings.TrimPrefix(durationWords(seconds), "about ")
}

// backgroundCostsView is what each item's schedule choices would cost, and
// the chart of CPU by analysis: GET /api/background's schedule_costs and
// usage.
func (s *Server) backgroundCostsView(ctx context.Context, settings backgroundSettings, at time.Time) (map[string]any, map[string]any) {
	costs, perDay := s.cachedMeasures(ctx)
	usage := cpuUsage(s.Catalog.cpuDays(ctx, at.AddDate(0, 0, -cpuWindowDays)), at)
	days := map[string]*cpuSourceRow{}
	for _, row := range usage["rows"].([]cpuSourceRow) {
		if row.Item != "" {
			days[row.Item] = &row
		}
	}
	schedules := map[string]any{}
	for _, item := range backgroundItems {
		cost, measured := costs[item.ID]
		schedules[item.ID] = map[string]any{"rows": scheduleCosts(item, settings, cost, measured, perDay, days[item.ID]),
			"reason": nullable(placementReason(item, settings, cost, measured, perDay))}
	}
	return schedules, usage
}
