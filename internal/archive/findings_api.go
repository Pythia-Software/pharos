package archive

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// The Findings API:
//
//	GET  /api/findings                     cards, cart, wins, savings, settings, threshold preview
//	GET  /api/findings/{id}                one finding: weekly chart, evidence, attempts, prompt
//	POST /api/findings/{id}/action         dismiss, dismiss_detector, disable_detector, snooze, restore, cart_*, not_applied
//	POST /api/findings/cart/prompt         preview a target's prompt (starts nothing)
//	POST /api/findings/cart/copy           the copy: starts measuring the ticked findings
//	POST /api/findings/settings            threshold, rank, handoff, per-repository handoff, detector_enabled
//	POST /api/findings/seen                the Findings view was opened ("New" chips)
//	POST /api/findings/refresh             run the full pass now
//
// Reading never changes a finding's state; only the copy starts measuring.

func (s *Server) getFindings(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/findings"), "/")
	if path == "" {
		value, err := s.Catalog.FindingsOverview(r.Context(), r.URL.Query().Get("state"))
		writeResult(w, value, err)
		return
	}
	id, _ := url.PathUnescape(strings.TrimPrefix(path, "/"))
	value, err := s.Catalog.FindingDetail(r.Context(), id)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
	} else if value == nil {
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
	} else {
		writeJSON(w, value, http.StatusOK)
	}
}

func (s *Server) postFindings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	path := strings.TrimPrefix(r.URL.Path, "/api/findings/")
	ctx := r.Context()
	ids := func() []string {
		values := []string{}
		if items, ok := body["ids"].([]any); ok {
			for _, item := range items {
				values = append(values, firstString(item))
			}
		}
		return values
	}
	switch {
	case path == "cart/prompt":
		value, err := s.Catalog.CartPrompt(ctx, firstString(body["target"]), ids(), firstString(body["handoff"]))
		writeResult(w, value, err)
	case path == "cart/copy":
		value, err := s.Catalog.CopyCart(ctx, firstString(body["target"]), ids(), firstString(body["handoff"]))
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, value, http.StatusOK)
	case path == "settings":
		if err := s.Catalog.SetFindingSettings(ctx, body); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		settings, err := s.Catalog.findingSettings(ctx)
		writeResult(w, settings, err)
	case path == "seen":
		err := s.Catalog.MarkFindingsSeen(ctx)
		writeResult(w, map[string]any{"ok": err == nil}, err)
	case path == "refresh":
		started := s.spawn(func(ctx context.Context) {
			if err := s.Catalog.RefreshFindings(ctx, true); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "Findings: %v\n", err)
			}
		})
		if !started {
			writeJSON(w, map[string]any{"error": "Pharos is stopping"}, http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"started": true}, http.StatusAccepted)
	case strings.HasSuffix(path, "/action"):
		id, _ := url.PathUnescape(strings.TrimSuffix(path, "/action"))
		if err := s.Catalog.FindingAction(ctx, id, body); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true}, http.StatusOK)
	default:
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
	}
}

// findingView is what the reads need about every finding at once.
type findingView struct {
	cart     *cartContext
	rows     map[string]*findingRow
	states   map[string]*findingUserState
	now      time.Time
	thresh   int
	rec      int
	checks   []map[string]any
	hosts    int
	thisHost string
}

func (c *Catalog) loadFindingView(ctx context.Context) (*findingView, error) {
	cart, err := c.loadCartContext(ctx)
	if err != nil {
		return nil, err
	}
	view := &findingView{cart: cart, rows: cart.rows, states: cart.states, now: c.clock(), hosts: len(cart.hosts), thisHost: c.currentHostID()}
	weekly, _ := c.metaValue(ctx, "findings_weekly_conversations")
	view.checks, view.rec = findingCheckpointPreview(view, parseFloat(weekly))
	view.thresh = cart.settings.Threshold
	if view.thresh <= 0 {
		view.thresh = view.rec
	}
	return view, nil
}

func parseFloat(text string) float64 {
	var value float64
	fmt.Sscan(text, &value)
	return value
}

// state is a finding's place in the lifecycle.
func (view *findingView) state(row *findingRow) string {
	state := view.states[row.ID]
	if state != nil {
		if state.Dismissed {
			return findingDismissed
		}
		if state.SnoozeUntil != "" && state.SnoozeUntil > formatTime(view.now) {
			return findingSnoozed
		}
		if state.SnoozeWorse && !view.snoozeWoke(row, state) {
			return findingSnoozed
		}
		if latest := state.latest(); latest != nil {
			switch {
			case latest.Status == interventionWatching:
				return findingWatching
			case latest.Status == interventionImproved && latest.RegressedAt == "":
				return findingWon
			}
			// Unchanged, worse, inconclusive, and regressed findings reopen
			// with their next attempt, whatever the gate says now.
			return findingOpen
		}
	}
	if row.Hidden || !row.Active || view.detectorOff(row.Detector) || !row.Stats.passes(view.thresh) {
		return findingHidden
	}
	return findingOpen
}

// snoozeWoke reports a snoozed-until-worse finding whose weekly rate has
// more than doubled since it was snoozed.
func (view *findingView) snoozeWoke(row *findingRow, state *findingUserState) bool {
	return row.Stats.Recent > 0 && state.SnoozeRate >= 0 && row.recentRate > 2*state.SnoozeRate && row.recentRate > 0
}

// findingCheckpointPreview computes, for each threshold the slider snaps
// to, how many findings would show, the typical wait for a result, and the
// share of results expected to be clear; and recommends the smallest
// threshold at which a typical finding can reach a clear result within 30
// days (product decisions 13 and 17).
func findingCheckpointPreview(view *findingView, weekly float64) ([]map[string]any, int) {
	checks := []map[string]any{}
	recommended := 0
	for _, threshold := range findingCheckpoints {
		waits := []float64{}
		clear := 0
		for _, row := range view.rows {
			if row.Hidden || !row.Active || view.detectorOff(row.Detector) || !row.Stats.passes(threshold) {
				continue
			}
			if state := view.states[row.ID]; state != nil && (state.Dismissed || state.latest() != nil) {
				continue
			}
			wait := findingExpectedWait(row)
			waits = append(waits, wait)
			if wait <= findingAfterMaxDays && findingDetectable(row) {
				clear++
			}
		}
		share := 0.0
		if len(waits) > 0 {
			share = float64(clear) / float64(len(waits))
		}
		typical := median(waits)
		checks = append(checks, map[string]any{"threshold": threshold, "shown": len(waits), "typical_wait_days": math.Round(typical), "clear_share": share})
		if recommended == 0 && threshold >= 5 && len(waits) > 0 && share >= 0.75 && typical <= 30 {
			recommended = threshold
		}
	}
	if recommended == 0 {
		recommended = 5
		if weekly >= 300 {
			recommended = 10
		}
	}
	return checks, recommended
}

// findingExpectedWait is how long a result would take if copied today.
func findingExpectedWait(row *findingRow) float64 {
	units := findingUnits(row)
	if row.Stats.DailyExposure <= 0 || units <= 0 {
		return findingAfterMaxDays
	}
	return math.Min(math.Max(math.Ceil(units/row.Stats.DailyExposure), 7), findingAfterMaxDays)
}

// findingDetectable reports whether a 70% drop from the finding's baseline
// would be significant with as much work after the change as before.
func findingDetectable(row *findingRow) bool {
	if row.Metric.Kind == "mean" {
		return row.Stats.Exposure >= 10
	}
	hits, units := row.Stats.Affected, max(row.Stats.Exposure, row.Stats.Affected)
	if hits == 0 {
		return false
	}
	after := int(math.Floor(0.3 * float64(hits)))
	lower, _ := hypergeometric(after, units, hits+after, 2*units)
	return lower < 0.05
}

// FindingsOverview answers GET /api/findings. state filters the cards (open,
// watching, won, dismissed, snoozed); empty returns every visible finding.
func (c *Catalog) FindingsOverview(ctx context.Context, filter string) (map[string]any, error) {
	built, _ := c.metaValue(ctx, "findings_generation")
	// Only the service builds findings; an MCP server just reads them.
	if built == "" && c.background != nil {
		c.refreshFindingsInBackground(false)
	}
	view, err := c.loadFindingView(ctx)
	if err != nil {
		return nil, err
	}
	cards := []map[string]any{}
	counts := map[string]int{}
	saved := findingSavings{}
	atStake := 0.0
	regressed := 0
	newCount := 0
	for _, row := range view.rows {
		state := view.state(row)
		if state == findingHidden {
			continue
		}
		counts[state]++
		card := view.card(row, state)
		if state == findingOpen {
			atStake += floatOr(row.Impact["usd"])
			// New since the last visit: when it first passed the gate, or was
			// first seen when a lower threshold made it visible.
			if firstString(row.GatePassedAt, row.FirstSeenAt) > view.cart.settings.SeenAt {
				newCount++
				card["new"] = true
			}
		}
		if user := view.states[row.ID]; user != nil {
			for _, item := range user.Interventions {
				if item.Status == interventionImproved {
					saved.USD += item.Savings.USD
					saved.Tokens += item.Savings.Tokens
					saved.Minutes += item.Savings.Minutes
					saved.Failures += item.Savings.Failures
					saved.AddedUSD += item.Savings.AddedUSD
					saved.AddedTokens += item.Savings.AddedTokens
					saved.Weeks = max(saved.Weeks, item.Savings.Weeks)
					if item.RegressedAt != "" {
						regressed++
					}
				}
			}
		}
		if filter == "" || filter == state {
			cards = append(cards, card)
		}
	}
	rank := view.cart.settings.Rank
	sort.SliceStable(cards, func(i, j int) bool {
		left, right := floatOr(mapValueDefault(cards[i]["impact"])[rank]), floatOr(mapValueDefault(cards[j]["impact"])[rank])
		if left != right {
			return left > right
		}
		return firstString(cards[i]["title"]) < firstString(cards[j]["title"])
	})
	cartCount := 0
	for _, state := range view.states {
		if state.Cart != nil && view.rows[state.Cart.FindingID] != nil {
			cartCount++
		}
	}
	for _, clone := range view.cart.clones {
		if view.rows[clone.FindingID] != nil {
			cartCount++
		}
	}
	summary := map[string]any{"saved": saved, "open": counts[findingOpen], "watching": counts[findingWatching], "won": counts[findingWon],
		"dismissed": counts[findingDismissed], "snoozed": counts[findingSnoozed], "regressed": regressed, "at_stake_usd": atStake, "new": newCount,
		"next_result_days": view.nextResult()}
	// The header's badge and cart button poll this; it skips the cards.
	if filter == "summary" {
		return map[string]any{"summary": summary, "cart_count": cartCount}, nil
	}
	status := map[string]any{}
	for _, key := range []string{"findings_built_at", "findings_measured_at", "findings_generation"} {
		value, _ := c.metaValue(ctx, key)
		status[strings.TrimPrefix(key, "findings_")] = nilIfEmpty(value)
	}
	for _, key := range []string{"findings_took_ms", "findings_conversations_28d", "findings_weekly_conversations"} {
		value, _ := c.metaValue(ctx, key)
		if value == "" {
			status[strings.TrimPrefix(key, "findings_")] = nil
		} else {
			status[strings.TrimPrefix(key, "findings_")] = parseFloat(value)
		}
	}
	c.findings.mu.Lock()
	status["running"], status["phase"], status["error"] = c.findings.running, nilIfEmpty(c.findings.phase), nilIfEmpty(c.findings.lastError)
	c.findings.mu.Unlock()
	status["current"] = built == c.findingsGeneration()
	detectors := []map[string]any{}
	for _, detector := range findingDetectors {
		detectors = append(detectors, map[string]any{"name": detector.name, "label": findingDetectorLabels[detector.name], "enabled": !view.cart.settings.detectorOff(detector.name)})
	}
	return map[string]any{
		"status": status, "settings": view.cart.settings, "threshold": view.thresh, "recommended": view.rec, "checkpoints": view.checks,
		"summary": summary, "cart_count": cartCount, "findings": cards, "cart": view.carts(), "targets": view.targets(), "repositories": view.repositories(), "near": view.nearMisses(),
		"wins": view.wins(), "detectors": detectors,
	}, nil
}

func floatOr(value any) float64 {
	number, _ := number(value)
	return number
}

// card is a finding as the Findings view shows it, in the detector's words.
func (view *findingView) card(row *findingRow, state string) map[string]any {
	user := view.states[row.ID]
	step, attempt := findingStepFor(row, user)
	card := map[string]any{"id": row.ID, "state": state, "title": row.Card.Title, "detector": row.Detector, "detector_label": findingDetectorLabels[row.Detector], "explanation": row.Card.Explanation, "impact": row.Impact,
		"impact_note": row.Card.ImpactNote, "rate": row.Card.Rate, "change": step.Change, "change_label": step.Label, "attempt": attempt,
		"target": row.Target, "target_label": view.cart.label(row.Target), "scope_label": view.scopeLabel(row), "repository_id": nilIfEmpty(row.RepositoryID),
		"first_seen_at": row.FirstSeenAt, "last_seen": nilIfEmpty(row.Stats.LastSeen), "active": row.Active}
	if user == nil {
		return card
	}
	if user.Cart != nil {
		card["cart"] = map[string]any{"target": user.Cart.Target, "target_label": view.cart.label(user.Cart.Target), "ticked": user.Cart.Ticked}
	}
	if user.Dismissed {
		card["dismissed"] = map[string]any{"reason": nilIfEmpty(user.DismissReason), "at": user.DismissedAt}
	}
	if user.SnoozeUntil != "" || user.SnoozeWorse {
		card["snoozed"] = map[string]any{"until": nilIfEmpty(user.SnoozeUntil), "until_worse": user.SnoozeWorse, "at": user.SnoozedAt}
	}
	if undo := findingNeedsUndo(user); undo != nil {
		card["undo"] = map[string]any{"copied_at": undo.CopiedAt, "change": undo.Change, "sentence": undo.Result["sentence"]}
	}
	latest := user.latest()
	if latest == nil {
		return card
	}
	if latest.Status == interventionWatching || latest.Status == interventionImproved && latest.RegressedAt == "" {
		// The change being measured, not the next one.
		card["change"], card["change_label"], card["attempt"] = defaultString(latest.Plan.Change, latest.Change), latest.Change, latest.Attempt
	}
	switch {
	case latest.Status == interventionWatching:
		copied, _ := parseTime(latest.CopiedAt)
		day := int(view.now.Sub(copied).Hours()/24) + 1
		card["watching"] = map[string]any{"copied_at": latest.CopiedAt, "day": day, "of_days": latest.Plan.AfterDays, "result_on": latest.Plan.AfterTo,
			"before": ratePhrase(defaultString(latest.Plan.Metric.Kind, row.Metric.Kind), latest.Plan.BeforeRate), "change": latest.Change, "saved_usd": latest.Savings.USD, "target_label": view.cart.label(latest.Target)}
	case latest.RegressedAt != "":
		card["regressed"] = map[string]any{"at": latest.RegressedAt, "change": latest.Change, "saved_usd": latest.Savings.USD}
	case latest.Status == interventionUnchanged:
		card["reopened"] = map[string]any{"change": latest.Change, "sentence": latest.Result["sentence"]}
	case latest.Status == interventionInconclusive:
		card["inconclusive"] = map[string]any{"change": latest.Change, "sentence": latest.Result["sentence"]}
	}
	if latest.Status != interventionWatching && latest.Result != nil {
		card["result"] = map[string]any{"outcome": latest.Status, "sentence": latest.Result["sentence"], "decided_at": latest.DecidedAt}
	}
	return card
}

// scopeLabel names where a finding applies.
func (view *findingView) scopeLabel(row *findingRow) string {
	kind, rest, _ := strings.Cut(row.Scope, ":")
	switch kind {
	case "repo":
		return view.cart.repos[rest]
	case "global":
		host, provider, _ := strings.Cut(rest, ":")
		label := "All repositories · " + providerLabel(provider)
		if view.hosts > 1 {
			label += " · " + defaultString(view.cart.hosts[host], "another Mac")
		}
		return label
	}
	return view.cart.label(row.Scope)
}

// carts lists each target's prompt: the findings in it and which are ticked.
func (view *findingView) carts() []map[string]any {
	targets := map[string][]map[string]any{}
	for id, state := range view.states {
		row := view.rows[id]
		if row == nil || state.Cart == nil {
			continue
		}
		step, _ := findingStepFor(row, state)
		item := map[string]any{"id": id, "title": row.Card.Title, "ticked": state.Cart.Ticked, "change_label": step.Label, "added_at": state.Cart.AddedAt,
			"suggested_target": row.Target, "undo": findingNeedsUndo(state) != nil}
		targets[state.Cart.Target] = append(targets[state.Cart.Target], item)
	}
	for _, clone := range view.cart.clones {
		row := view.rows[clone.FindingID]
		if row == nil {
			continue
		}
		state := view.states[clone.FindingID]
		step, _ := findingStepFor(row, state)
		item := map[string]any{"id": clone.ID, "finding_id": clone.FindingID, "title": row.Card.Title, "ticked": clone.Ticked, "change_label": step.Label, "added_at": clone.AddedAt,
			"suggested_target": row.Target, "undo": findingNeedsUndo(state) != nil, "clone": true}
		targets[clone.Target] = append(targets[clone.Target], item)
	}
	output := []map[string]any{}
	for _, target := range sortedFindingKeys(targets) {
		items := targets[target]
		sort.Slice(items, func(i, j int) bool { return firstString(items[i]["added_at"]) < firstString(items[j]["added_at"]) })
		ticked := 0
		for _, item := range items {
			if item["ticked"] == true {
				ticked++
			}
		}
		handoff, reason := handoffFor(target, view.cart.settings, view.cart.habits)
		output = append(output, map[string]any{"target": target, "label": view.cart.label(target), "items": items, "ticked": ticked, "handoff": handoff, "handoff_reason": reason})
	}
	sort.SliceStable(output, func(i, j int) bool {
		return strings.HasPrefix(firstString(output[i]["target"]), "repo:") && !strings.HasPrefix(firstString(output[j]["target"]), "repo:")
	})
	return output
}

// targets lists every place a finding's fix can go, for the cart menu.
func (view *findingView) targets() []map[string]any {
	seen := map[string]bool{}
	output := []map[string]any{}
	add := func(target string) {
		if target == "" || seen[target] {
			return
		}
		seen[target] = true
		output = append(output, map[string]any{"target": target, "label": view.cart.label(target)})
	}
	for _, row := range view.rows {
		add(row.Target)
		if strings.HasPrefix(row.Scope, "global:") {
			add(row.Scope)
		}
	}
	for id, name := range view.cart.repos {
		if name != "" && view.cart.habits[id].Work > 0 {
			add("repo:" + id)
		}
	}
	sort.SliceStable(output, func(i, j int) bool { return firstString(output[i]["label"]) < firstString(output[j]["label"]) })
	return output
}

// repositories lists each repository with work, its pull request habit, and
// what prompts ask for there.
func (view *findingView) repositories() []map[string]any {
	output := []map[string]any{}
	for id, habit := range view.cart.habits {
		if habit.Work < 3 {
			continue
		}
		handoff, _ := handoffFor("repo:"+id, view.cart.settings, view.cart.habits)
		output = append(output, map[string]any{"id": id, "name": view.cart.repos[id], "work": habit.Work, "pr_share": habit.Share, "handoff": handoff,
			"override": nilIfEmpty(view.cart.settings.RepositoryHandoff[id])})
	}
	sort.Slice(output, func(i, j int) bool { return integer(output[i]["work"]) > integer(output[j]["work"]) })
	return output
}

// nearMisses are the two patterns closest to the threshold, for a library
// with nothing to show yet.
func (view *findingView) nearMisses() []map[string]any {
	rows := []*findingRow{}
	for _, row := range view.rows {
		if row.Active && !row.Hidden && !view.detectorOff(row.Detector) && !row.Stats.passes(view.thresh) && row.Stats.Recent > 0 {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Stats.Affected > rows[j].Stats.Affected })
	output := []map[string]any{}
	for _, row := range rows[:min(2, len(rows))] {
		output = append(output, map[string]any{"id": row.ID, "title": row.Card.Title, "affected": row.Stats.Affected})
	}
	return output
}

// nextResult is the days until the next watched finding has its result.
func (view *findingView) nextResult() any {
	next := ""
	for _, state := range view.states {
		if latest := state.latest(); latest != nil && latest.Status == interventionWatching && (next == "" || latest.Plan.AfterTo < next) {
			next = latest.Plan.AfterTo
		}
	}
	if next == "" {
		return nil
	}
	return max(0, int(math.Ceil(dayTime(next).Sub(view.now).Hours()/24)))
}

// wins lists every improved result, newest first, with its savings.
func (view *findingView) wins() []map[string]any {
	output := []map[string]any{}
	for id, state := range view.states {
		row := view.rows[id]
		if row == nil {
			continue
		}
		for _, item := range state.Interventions {
			if item.Status != interventionImproved {
				continue
			}
			before, after := resultRates(item.Result)
			copied, _ := parseTime(item.CopiedAt)
			left := findingSavingsDays - int(view.now.Sub(copied).Hours()/24)
			output = append(output, map[string]any{"id": id, "title": row.Card.Title, "where": view.scopeLabel(row), "change": item.Change,
				"before": ratePhrase(row.Metric.Kind, before), "now": nowPhrase(row.Metric.Kind, after), "saved": item.Savings, "days_left": max(left, 0),
				"regressed_at": nilIfEmpty(item.RegressedAt), "decided_at": item.DecidedAt, "copied_at": item.CopiedAt, "kind": row.Metric.Kind})
		}
	}
	sort.Slice(output, func(i, j int) bool {
		return firstString(output[i]["decided_at"]) > firstString(output[j]["decided_at"])
	})
	return output
}

func resultRates(result map[string]any) (before, after float64) {
	before = floatOr(mapValueDefault(result["before"])["rate"])
	after = floatOr(mapValueDefault(result["after"])["rate"])
	return before, after
}

func nowPhrase(kind string, rate float64) string {
	if rate <= 0 {
		return "none"
	}
	return ratePhrase(kind, rate)
}

// ratePhrase words a finding's rate: tokens for a mean, else a fraction.
func ratePhrase(kind string, rate float64) string {
	if kind == "mean" {
		return tokensPhrase(rate)
	}
	return fractionPhrase(rate, "")
}

// FindingDetail answers GET /api/findings/{id}.
func (c *Catalog) FindingDetail(ctx context.Context, id string) (map[string]any, error) {
	view, err := c.loadFindingView(ctx)
	if err != nil {
		return nil, err
	}
	aliases, err := findingAliasMap(ctx, c.DB)
	if err != nil {
		return nil, err
	}
	id = resolveFindingID(aliases, id)
	row := view.rows[id]
	if row == nil {
		return nil, nil
	}
	state := view.state(row)
	card := view.card(row, state)
	daily, err := findingDailyRows(ctx, c.DB, id)
	if err != nil {
		return nil, err
	}
	user := view.states[id]
	anchor := view.now.Local().AddDate(0, 0, 1).Format("2006-01-02")
	copies := []string{}
	if user != nil {
		for _, item := range user.Interventions {
			if item.Status != interventionWithdrawn {
				copies = append(copies, item.Plan.CopyDay)
			}
		}
	}
	if len(copies) > 0 {
		anchor = copies[len(copies)-1]
	}
	chart := findingChart(row, daily, anchor, view.now, copies)
	if latest := user.latestOrNil(); latest != nil && latest.Plan.BeforeExposure > 0 {
		// The baseline the result is judged against, fixed at the copy.
		chart["baseline"] = latest.Plan.BeforeRate
		chart["baseline_phrase"] = ratePhrase(row.Metric.Kind, latest.Plan.BeforeRate)
	}
	card["chart"] = chart
	card["evidence_count"] = row.Facts["evidence_count"]
	card["evidence"] = row.Evidence
	card["facts"] = row.Facts
	attempts := []map[string]any{}
	if user != nil {
		for _, item := range user.Interventions {
			attempts = append(attempts, map[string]any{"attempt": item.Attempt, "change": item.Change, "lever": item.Lever, "copied_at": item.CopiedAt,
				"status": item.Status, "result": item.Result, "savings": item.Savings, "plan": item.Plan, "regressed_at": nilIfEmpty(item.RegressedAt),
				"target_label": view.cart.label(item.Target)})
		}
	}
	card["attempts"] = attempts
	step, attempt := findingStepFor(row, user)
	location := ""
	if kind, rest, _ := strings.Cut(row.Target, ":"); kind == "repo" {
		location = repositoryClone(view.cart.locations[rest])
	}
	handoff, _ := handoffFor(row.Target, view.cart.settings, view.cart.habits)
	card["prompt"] = renderFindingPrompt(row.Target, view.cart.label(row.Target), location, handoff, []cartItem{{Row: row, State: user, Step: step, Attempt: attempt, Undo: findingNeedsUndo(user)}})
	card["steps"] = row.Card.Steps
	card["metric"] = row.Metric
	card["baseline"] = row.Baseline
	card["gate"] = row.Stats
	card["threshold"] = view.thresh
	return card, nil
}

// findingChart builds the weekly sparkline: twelve weeks before the copy
// (or today) and up to eight after, plotting the rate. Weeks with less than
// a quarter of the median week's work are drawn hollow.
func findingChart(row *findingRow, daily map[string][3]float64, anchor string, now time.Time, copies []string) map[string]any {
	start := dayTime(anchor).AddDate(0, 0, -7*12)
	end := dayTime(anchor).AddDate(0, 0, 7*8)
	today := now.Local().AddDate(0, 0, 1)
	if end.After(today) {
		end = today
	}
	weeks := []map[string]any{}
	exposures := []float64{}
	for week := start; week.Before(end); week = week.AddDate(0, 0, 7) {
		events, exposure, cost := 0.0, 0.0, 0.0
		for day := week; day.Before(week.AddDate(0, 0, 7)) && day.Before(end); day = day.AddDate(0, 0, 1) {
			values := daily[day.Format("2006-01-02")]
			events += values[0]
			exposure += values[1]
			cost += values[2]
		}
		point := map[string]any{"start": week.Format("2006-01-02"), "events": events, "exposure": exposure, "cost_usd": cost, "after": week.After(dayTime(anchor))}
		if exposure > 0 {
			point["rate"] = events / exposure
			exposures = append(exposures, exposure)
		}
		weeks = append(weeks, point)
	}
	typical := median(exposures)
	for _, point := range weeks {
		point["hollow"] = floatOr(point["exposure"]) < 0.25*typical
	}
	baseline := 0.0
	events, exposure := 0.0, 0.0
	for day, values := range daily {
		if day >= dayTime(anchor).AddDate(0, 0, -(findingGateDays-1)).Format("2006-01-02") && day <= anchor {
			events += values[0]
			exposure += values[1]
		}
	}
	if exposure > 0 {
		baseline = events / exposure
	}
	return map[string]any{"title": row.Card.ChartTitle, "kind": row.Metric.Kind, "weeks": weeks, "anchor": anchor, "copies": copies, "baseline": baseline,
		"baseline_phrase": ratePhrase(row.Metric.Kind, baseline)}
}

// storeFindingsVolume records the library's recent volume, for the
// threshold recommendation and the empty state.
func storeFindingsVolume(tx *sql.Tx, env *findingEnv) error {
	recent := 0
	for _, conv := range env.convs {
		if env.inGate(conv.Day) {
			recent++
		}
	}
	for key, value := range map[string]string{"findings_conversations_28d": fmt.Sprint(recent), "findings_weekly_conversations": fmt.Sprintf("%.0f", float64(recent)/4)} {
		if _, err := tx.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return err
		}
	}
	return nil
}

// detectorOff reports whether the user turned a finding's detector off. The
// view the pass previews thresholds with has no settings yet.
func (view *findingView) detectorOff(detector string) bool {
	return view.cart != nil && view.cart.settings.detectorOff(detector)
}
