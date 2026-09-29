package archive

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Verification. Copying a prompt is the intervention; its analysis plan is
// fixed at that moment so nobody checks every day until the numbers look
// good (docs/auto-optimization-design.md, "Verification"):
//
//   - before: the 28 days before the copy;
//   - after: from the copy until it holds as much relevant work as the before
//     window, capped at 60 days, its length set at the copy from the recent
//     rate of relevant work;
//   - test: one-sided exact (Fisher) on units for rates, a one-sided
//     Mann-Whitney test on per-unit values for means.
//
// Improved needs the after rate at most half the before rate, p < 0.05, and
// no guard metric more than 10% worse. Worse needs at least 1.5 times, p <
// 0.05. Unchanged is a completed window with neither; inconclusive is a
// window that ended with less than half the before window's relevant work.

// findingPlan is fixed at the copy. Windows are local days: the before
// window is the 28 days ending on the copy day, since a fix takes effect
// after the agent makes it; the after window starts the next day.
type findingPlan struct {
	CopyDay    string        `json:"copy_day"`
	BeforeFrom string        `json:"before_from"`
	AfterFrom  string        `json:"after_from"`
	AfterDays  int           `json:"after_days"`
	AfterTo    string        `json:"after_to"`
	Metric     findingMetric `json:"metric"`
	Scope      string        `json:"scope"`
	// Before is the baseline at the copy, for display until the result.
	BeforeEvents   float64 `json:"before_events"`
	BeforeExposure float64 `json:"before_exposure"`
	BeforeRate     float64 `json:"before_rate"`
	// Per-event costs from the 28 days before the copy, for savings.
	USDPerEvent      float64 `json:"usd_per_event"`
	TokensPerEvent   float64 `json:"tokens_per_event"`
	MinutesPerEvent  float64 `json:"minutes_per_event"`
	FailuresPerEvent float64 `json:"failures_per_event"`
	// Files are the instruction files the target had at the copy, with
	// their sizes, for the context a change adds.
	Files    map[string]int64 `json:"files,omitempty"`
	Location string           `json:"location,omitempty"`
	// Others are the findings copied in the same prompt.
	Others []string `json:"others,omitempty"`
	// Change is the proposed change the prompt carried.
	Change string `json:"change,omitempty"`
	Undo   bool   `json:"undo,omitempty"`
}

// findingSavings accrue weekly from the copy (product decision 10).
type findingSavings struct {
	USD      float64 `json:"usd"`
	Tokens   float64 `json:"tokens"`
	Minutes  float64 `json:"minutes"`
	Failures float64 `json:"failures"`
	// Added is the context the change put in front of every later request.
	AddedTokens float64 `json:"added_tokens"`
	AddedUSD    float64 `json:"added_usd"`
	Weeks       int     `json:"weeks"`
	Through     string  `json:"through,omitempty"`
	Done        bool    `json:"done,omitempty"`
}

// hypergeometric returns P(X <= x) and P(X >= x) for x successes in n draws
// from a population of total with successes good: Fisher's exact test.
func hypergeometric(x, n, good, total int) (lower, upper float64) {
	if total <= 0 || n <= 0 {
		return 1, 1
	}
	logChoose := func(n, k int) float64 {
		if k < 0 || k > n {
			return math.Inf(-1)
		}
		a, _ := math.Lgamma(float64(n + 1))
		b, _ := math.Lgamma(float64(k + 1))
		c, _ := math.Lgamma(float64(n - k + 1))
		return a - b - c
	}
	denominator := logChoose(total, n)
	low, high := max(0, n+good-total), min(n, good)
	for k := low; k <= high; k++ {
		p := math.Exp(logChoose(good, k) + logChoose(total-good, n-k) - denominator)
		if k <= x {
			lower += p
		}
		if k >= x {
			upper += p
		}
	}
	return math.Min(lower, 1), math.Min(upper, 1)
}

// mannWhitney returns one-sided p-values that after tends to be smaller
// (less) or larger (greater) than before, by the normal approximation with
// tie correction.
func mannWhitney(before, after []float64) (less, greater float64) {
	n1, n2 := len(after), len(before)
	if n1 == 0 || n2 == 0 {
		return 1, 1
	}
	type ranked struct {
		value float64
		after bool
	}
	all := make([]ranked, 0, n1+n2)
	for _, value := range after {
		all = append(all, ranked{value, true})
	}
	for _, value := range before {
		all = append(all, ranked{value, false})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].value < all[j].value })
	rankSum, ties := 0.0, 0.0
	for start := 0; start < len(all); {
		end := start
		for end < len(all) && all[end].value == all[start].value {
			end++
		}
		rank := float64(start+end+1) / 2
		size := float64(end - start)
		ties += size*size*size - size
		for index := start; index < end; index++ {
			if all[index].after {
				rankSum += rank
			}
		}
		start = end
	}
	u := rankSum - float64(n1*(n1+1))/2
	mean := float64(n1*n2) / 2
	total := float64(n1 + n2)
	variance := float64(n1*n2) / 12 * ((total + 1) - ties/(total*(total-1)))
	if variance <= 0 {
		return 1, 1
	}
	z := (u - mean) / math.Sqrt(variance)
	return normalCDF(z + 0.5/math.Sqrt(variance)), 1 - normalCDF(z-0.5/math.Sqrt(variance))
}

func normalCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	if len(sorted)%2 == 1 {
		return sorted[len(sorted)/2]
	}
	return (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
}

// windowStats is a metric over a set of days.
type windowStats struct {
	Units         int     `json:"units"`
	Hits          int     `json:"hits"`
	Events        float64 `json:"events"`
	Exposure      float64 `json:"exposure"`
	Rate          float64 `json:"rate"`
	Median        float64 `json:"median,omitempty"`
	TokensPerConv float64 `json:"tokens_per_conversation"`
	ErrorRate     float64 `json:"error_rate,omitempty"`
	SuccessRate   float64 `json:"success_rate,omitempty"`
	HelpRate      float64 `json:"help_rate,omitempty"`
	UsageRate     float64 `json:"usage_rate,omitempty"`
	values        []float64
	hitFlags      []bool
}

func (env *findingEnv) window(metric findingMetric, observations map[string]*findingObs, from, to string) windowStats {
	stats := windowStats{}
	conversations := map[string]bool{}
	calls, errors, successes, engaged, help, usage := 0, 0, 0, 0, 0, 0
	for _, obs := range observations {
		if obs.Day < from || obs.Day >= to {
			continue
		}
		stats.Units++
		if obs.Hit {
			stats.Hits++
		}
		switch metric.Kind {
		case "rate":
			stats.Exposure++
			if obs.Hit {
				stats.Events++
			}
		case "mean":
			stats.Exposure++
			stats.Events += obs.Value
		case "share":
			stats.Exposure += obs.Total
			if obs.Hit {
				stats.Events += obs.Value
			}
		}
		stats.values = append(stats.values, obs.Value)
		stats.hitFlags = append(stats.hitFlags, obs.Hit)
		conversations[obs.Conversation] = true
		calls += obs.Calls
		errors += obs.Errors
		if obs.Success != nil {
			engaged++
			if *obs.Success {
				successes++
			}
		}
		if obs.HelpCalls > 0 {
			help++
		}
		if obs.UsageErrors > 0 {
			usage++
		}
	}
	if stats.Exposure > 0 {
		stats.Rate = stats.Events / stats.Exposure
	}
	stats.Median = median(stats.values)
	tokens, counted := int64(0), 0
	for id := range conversations {
		if conv := env.convs[id]; conv != nil {
			tokens += conv.Tokens
			counted++
		}
	}
	if counted > 0 {
		stats.TokensPerConv = float64(tokens) / float64(counted)
	}
	if calls > 0 {
		stats.ErrorRate = float64(errors) / float64(calls)
	}
	if engaged > 0 {
		stats.SuccessRate = float64(successes) / float64(engaged)
		stats.HelpRate = float64(help) / float64(engaged)
		stats.UsageRate = float64(usage) / float64(engaged)
	}
	return stats
}

// compare tests an after window against a before window.
func compareWindows(metric findingMetric, before, after windowStats) (ratio, pLower, pUpper float64) {
	switch metric.Kind {
	case "mean":
		if before.Median > 0 {
			ratio = after.Median / before.Median
		} else if after.Median > 0 {
			ratio = math.Inf(1)
		} else {
			ratio = 1
		}
		pLower, pUpper = mannWhitney(before.values, after.values)
	default:
		if before.Rate > 0 {
			ratio = after.Rate / before.Rate
		} else if after.Rate > 0 {
			ratio = math.Inf(1)
		} else {
			ratio = 1
		}
		pLower, pUpper = hypergeometric(after.Hits, after.Units, before.Hits+after.Hits, before.Units+after.Units)
	}
	return ratio, pLower, pUpper
}

// findingUnits is the relevant work in a finding's last 28 days, in the
// units its daily exposure counts: calls for command shapes, otherwise
// conversations, engagements, or runs.
func findingUnits(row *findingRow) float64 {
	if row.Metric.Unit == "calls" {
		return row.Stats.Denom
	}
	return float64(row.Stats.Exposure)
}

// planFinding fixes the analysis plan for a copy.
func planFinding(row *findingRow, daily map[string][3]float64, copied time.Time) findingPlan {
	copyDay := copied.Local().Format("2006-01-02")
	plan := findingPlan{CopyDay: copyDay, BeforeFrom: dayTime(copyDay).AddDate(0, 0, -(findingGateDays - 1)).Format("2006-01-02"),
		AfterFrom: dayTime(copyDay).AddDate(0, 0, 1).Format("2006-01-02"), Metric: row.Metric, Scope: row.Scope}
	for day, values := range daily {
		if day >= plan.BeforeFrom && day <= copyDay {
			plan.BeforeEvents += values[0]
			plan.BeforeExposure += values[1]
		}
	}
	if plan.BeforeExposure > 0 {
		plan.BeforeRate = plan.BeforeEvents / plan.BeforeExposure
	}
	// The after window should hold as much relevant work as the before
	// window, at the recent rate of relevant work.
	units := findingUnits(row)
	days := findingAfterMaxDays
	if row.Stats.DailyExposure > 0 && units > 0 {
		days = int(math.Ceil(units / row.Stats.DailyExposure))
	}
	plan.AfterDays = min(max(days, 7), findingAfterMaxDays)
	plan.AfterTo = dayTime(plan.AfterFrom).AddDate(0, 0, plan.AfterDays).Format("2006-01-02")
	events := row.Stats.Events
	if row.Metric.Kind == "rate" {
		events = float64(row.Stats.Affected)
	}
	if events > 0 {
		plan.USDPerEvent = row.Stats.CostUSD / events
		plan.TokensPerEvent = float64(row.Stats.Tokens) / events
		plan.MinutesPerEvent = row.Stats.Minutes / events
		if row.Metric.Failures {
			plan.FailuresPerEvent = float64(row.Stats.Occurrences) / events
		}
	}
	if row.Metric.Kind == "mean" && row.Stats.Tokens > 0 {
		// Mean metrics count tokens: price each token of the pattern.
		plan.USDPerEvent = row.Stats.CostUSD / float64(row.Stats.Tokens)
		plan.TokensPerEvent = 1
		plan.MinutesPerEvent = 0
	}
	return plan
}

// decideInterventions updates every measured finding: a watching finding
// whose window closed gets its result, an improved one accrues savings or
// regresses, and a snoozed-until-worse one wakes when its weekly rate doubles.
func (c *Catalog) decideInterventions(ctx context.Context, env *findingEnv, candidates []*findingCandidate) error {
	byID := map[string]*findingCandidate{}
	for _, candidate := range candidates {
		byID[candidate.Spec.id()] = candidate
	}
	aliases, err := findingAliasMap(ctx, c.DB)
	if err != nil {
		return err
	}
	interventions, err := loadInterventions(ctx, c.DB, "WHERE status IN (?,?)", interventionWatching, interventionImproved)
	if err != nil {
		return err
	}
	rows, err := loadFindingRows(ctx, c.DB, "")
	if err != nil {
		return err
	}
	all, err := loadInterventions(ctx, c.DB, "")
	if err != nil {
		return err
	}
	updates := []*findingIntervention{}
	for _, item := range interventions {
		id := resolveFindingID(aliases, item.FindingID)
		candidate := byID[id]
		if candidate == nil {
			continue
		}
		changed := false
		if item.Status == interventionWatching && env.today >= item.Plan.AfterTo {
			item.Result = env.result(candidate, item, all, rows, aliases)
			item.Status = firstString(item.Result["outcome"])
			item.DecidedAt = formatTime(env.now)
			changed = true
		}
		if item.Status == interventionWatching || item.Status == interventionImproved && item.RegressedAt == "" {
			item.Savings = env.savings(candidate, item)
			changed = true
		}
		if item.Status == interventionImproved && item.RegressedAt == "" && env.regressed(candidate, item) {
			item.RegressedAt = formatTime(env.now)
			changed = true
		}
		if changed {
			item.FindingID = id
			updates = append(updates, item)
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return c.writeTransaction(ctx, "finding-results", func(tx *sql.Tx) error {
		for _, item := range updates {
			if _, err := tx.Exec(`UPDATE finding_interventions SET status=?,result_json=?,decided_at=?,savings_json=?,regressed_at=?,updated_at=? WHERE id=?`,
				item.Status, nilIfEmptyJSON(item.Result), nilIfEmpty(item.DecidedAt), jsonText(item.Savings), nilIfEmpty(item.RegressedAt), formatTime(env.now), item.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func nilIfEmptyJSON(value map[string]any) any {
	if len(value) == 0 {
		return nil
	}
	return jsonText(value)
}

// result decides a finished window and says what happened, as observations.
func (env *findingEnv) result(candidate *findingCandidate, item *findingIntervention, all []*findingIntervention, rows map[string]*findingRow, aliases map[string]string) map[string]any {
	plan := item.Plan
	metric := plan.Metric
	if metric.Kind == "" {
		metric = candidate.Metric
	}
	before := env.window(metric, candidate.Obs, plan.BeforeFrom, plan.AfterFrom)
	after := env.window(metric, candidate.Obs, plan.AfterFrom, plan.AfterTo)
	ratio, pLower, pUpper := compareWindows(metric, before, after)
	guards := []map[string]any{}
	guard := func(name string, before, after float64, higherIsWorse bool) bool {
		ok := true
		if before > 0 {
			if higherIsWorse {
				ok = after <= before*(1+findingGuardSlack)
			} else {
				ok = after >= before*(1-findingGuardSlack)
			}
		}
		guards = append(guards, map[string]any{"name": name, "before": before, "after": after, "ok": ok})
		return ok
	}
	guardsOK := guard("tokens per conversation", before.TokensPerConv, after.TokensPerConv, true)
	if metric.GuardErrors {
		guardsOK = guard("error rate", before.ErrorRate, after.ErrorRate, true) && guardsOK
	}
	if metric.GuardSuccess {
		guardsOK = guard("engagements ending in success", before.SuccessRate, after.SuccessRate, false) && guardsOK
		// Help and usage errors must both fall, not trade places.
		guardsOK = guard("help calls", before.HelpRate, after.HelpRate, true) && guardsOK
		guardsOK = guard("usage errors", before.UsageRate, after.UsageRate, true) && guardsOK
	}
	outcome := interventionUnchanged
	switch {
	case after.Exposure < 0.5*before.Exposure || after.Units == 0:
		outcome = interventionInconclusive
	case ratio <= findingFadeFactor && pLower < 0.05 && guardsOK:
		outcome = interventionImproved
	case ratio >= 1.5 && pUpper < 0.05:
		outcome = interventionWorse
	}
	result := map[string]any{"outcome": outcome, "before": before, "after": after, "ratio": finiteOrNil(ratio), "p_lower": pLower, "p_upper": pUpper,
		"guards": guards, "sentence": resultSentence(metric, before, after, outcome)}
	// What else changed: harness versions, other findings copied in the same
	// scope, and the same pattern elsewhere.
	if note := env.harnessNote(candidate, plan); note != "" {
		result["harness_note"] = note
	}
	others := []string{}
	for _, other := range all {
		otherID := resolveFindingID(aliases, other.FindingID)
		if other.ID == item.ID || other.Status == interventionWithdrawn || otherID == resolveFindingID(aliases, item.FindingID) {
			continue
		}
		day := localDay(other.CopiedAt)
		if day < plan.BeforeFrom || day >= plan.AfterTo {
			continue
		}
		if row := rows[otherID]; row != nil && (row.Scope == plan.Scope || row.Target == item.Target) {
			others = append(others, row.Card.Title)
		}
	}
	if len(others) > 0 {
		result["others"], result["alongside"] = others, others
	}
	if candidate.Elsewhere != nil {
		elsewhereBefore := env.window(metric, candidate.Elsewhere, plan.BeforeFrom, plan.AfterFrom)
		elsewhereAfter := env.window(metric, candidate.Elsewhere, plan.AfterFrom, plan.AfterTo)
		if elsewhereBefore.Units > 0 {
			result["elsewhere"] = map[string]any{"before": elsewhereBefore.Rate, "after": elsewhereAfter.Rate,
				"sentence": "Elsewhere, the same thing went from " + fractionPhrase(elsewhereBefore.Rate, "") + " to " + fractionPhrase(elsewhereAfter.Rate, "") + "."}
		}
	}
	return result
}

func finiteOrNil(value float64) any {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return nil
	}
	return value
}

// resultSentence states a result in the finding's own words, as an
// observation rather than proof of cause.
func resultSentence(metric findingMetric, before, after windowStats, outcome string) string {
	if outcome == interventionInconclusive {
		return "There hasn't been enough of this work since the change to tell."
	}
	switch metric.Kind {
	case "mean":
		return fmt.Sprintf("Since the change, a typical %s has %s instead of %s.", strings.TrimSuffix(metric.Unit, "s"), tokensPhrase(after.Median), tokensPhrase(before.Median))
	case "share":
		return fmt.Sprintf("Since the change, this accounts for %s of the total instead of %s.", fractionPhrase(after.Rate, ""), fractionPhrase(before.Rate, ""))
	}
	unit := strings.TrimSuffix(metric.Unit, "s")
	if after.Hits == 0 {
		return fmt.Sprintf("Since the change, it hasn't happened in %s; before, it happened in %s.", countNoun(after.Units, unit), fractionPhrase(before.Rate, ""))
	}
	return fmt.Sprintf("Since the change, this happens in %s instead of %s.", fractionPhrase(after.Rate, metric.Unit), fractionPhrase(before.Rate, ""))
}

// harnessNote says when the harness version changed during a result's
// windows, which could explain the change as well as the fix.
func (env *findingEnv) harnessNote(candidate *findingCandidate, plan findingPlan) string {
	versions := func(from, to string) map[string]string {
		counts := map[string]map[string]int{}
		for _, obs := range candidate.Obs {
			if obs.Day < from || obs.Day >= to {
				continue
			}
			conv := env.convs[obs.Conversation]
			if conv == nil || conv.Version == "" {
				continue
			}
			if counts[conv.Provider] == nil {
				counts[conv.Provider] = map[string]int{}
			}
			counts[conv.Provider][conv.Version]++
		}
		output := map[string]string{}
		for provider, byVersion := range counts {
			output[provider] = topKey(byVersion)
		}
		return output
	}
	before, after := versions(plan.BeforeFrom, plan.AfterFrom), versions(plan.AfterFrom, plan.AfterTo)
	notes := []string{}
	for _, provider := range sortedFindingKeys(after) {
		if previous := before[provider]; previous != "" && previous != after[provider] {
			notes = append(notes, fmt.Sprintf("%s updated from %s to %s during this time", providerLabel(provider), previous, after[provider]))
		}
	}
	return joinWords(notes)
}

// savings accrue per full week since the copy, for 90 days: the decline
// beyond the half an untouched pattern would fade to anyway, times the work
// done that week, times the cost of one occurrence, less the context the
// change added.
func (env *findingEnv) savings(candidate *findingCandidate, item *findingIntervention) findingSavings {
	plan := item.Plan
	metric := plan.Metric
	if metric.Kind == "" {
		metric = candidate.Metric
	}
	baseline := plan.BeforeRate
	if plan.BeforeExposure == 0 || metric.Kind == "mean" {
		baseline = env.window(metric, candidate.Obs, plan.BeforeFrom, plan.AfterFrom).Rate
	}
	savings := findingSavings{}
	start := dayTime(defaultString(plan.AfterFrom, plan.CopyDay))
	end := start.AddDate(0, 0, findingSavingsDays)
	regressed := ""
	if item.RegressedAt != "" {
		regressed = localDay(item.RegressedAt)
	}
	for week := start; week.Before(end); week = week.AddDate(0, 0, 7) {
		to := week.AddDate(0, 0, 7)
		if to.After(end) {
			to = end
		}
		if to.Format("2006-01-02") > env.today || regressed != "" && week.Format("2006-01-02") >= regressed {
			break
		}
		stats := env.window(metric, candidate.Obs, week.Format("2006-01-02"), to.Format("2006-01-02"))
		saved := math.Max(0, findingFadeFactor*baseline-stats.Rate) * stats.Exposure
		savings.USD += saved * plan.USDPerEvent
		savings.Tokens += saved * plan.TokensPerEvent
		savings.Minutes += saved * plan.MinutesPerEvent
		savings.Failures += saved * plan.FailuresPerEvent
		tokens, usd := env.addedContext(candidate, item, week.Format("2006-01-02"), to.Format("2006-01-02"))
		savings.AddedTokens += tokens
		savings.AddedUSD += usd
		savings.Weeks++
		savings.Through = to.AddDate(0, 0, -1).Format("2006-01-02")
	}
	savings.USD -= savings.AddedUSD
	savings.Tokens -= savings.AddedTokens
	savings.Done = env.today >= end.Format("2006-01-02")
	return savings
}

// regressed reports a win whose pattern came back: over the days since the
// result (up to four weeks), the rate is no longer below half the baseline.
func (env *findingEnv) regressed(candidate *findingCandidate, item *findingIntervention) bool {
	decided := localDay(item.DecidedAt)
	if decided == "" {
		return false
	}
	from := dayTime(env.today).AddDate(0, 0, -findingGateDays).Format("2006-01-02")
	if from < decided {
		from = decided
	}
	if dayTime(env.today).Sub(dayTime(from)) < 7*24*time.Hour {
		return false
	}
	metric := item.Plan.Metric
	if metric.Kind == "" {
		metric = candidate.Metric
	}
	recent := env.window(metric, candidate.Obs, from, dayTime(env.today).AddDate(0, 0, 1).Format("2006-01-02"))
	return recent.Hits >= 3 && recent.Rate > findingFadeFactor*item.Plan.BeforeRate
}

// addedContext estimates the context a change added in one week: bytes the
// instruction files grew by since the copy, re-read by every request of the
// sessions that load them. The loaded-instructions record measures it where
// it has the file; otherwise the default branch's file sizes, times the
// requests of the scope's conversations whose harness reads that file.
func (env *findingEnv) addedContext(candidate *findingCandidate, item *findingIntervention, from, to string) (tokens, usd float64) {
	grown := env.instructionGrowth(item)
	if len(grown) == 0 {
		return 0, 0
	}
	readers := map[string]string{"CLAUDE.md": "claude", ".claude/CLAUDE.md": "claude", "AGENTS.md": "codex", "GEMINI.md": "antigravity"}
	for path, bytes := range grown {
		if bytes <= 0 {
			continue
		}
		requests := int64(0)
		if candidate.RepositoryID != "" {
			load, err := env.catalog.instructionLoad(env.ctx, env.db, candidate.RepositoryID, path, formatTime(dayTime(from)), formatTime(dayTime(to)))
			if err == nil && load.Sessions > 0 {
				requests = load.Requests
			}
		}
		models := map[string]int64{}
		if requests == 0 {
			for _, conv := range env.convs {
				if conv.Day >= from && conv.Day < to && env.inScope(item.Plan.Scope, conv) && (readers[filepath.Base(path)] == "" || readers[filepath.Base(path)] == conv.Provider) {
					requests += conv.Requests
					models[conv.Model] += conv.Requests
				}
			}
		}
		added := float64(bytes) / 4 * float64(requests)
		tokens += added
		model := defaultString(topKey64(models), "Unknown model")
		if priced := env.book.cost(model, from, map[string]int64{"cache_read_input_tokens": int64(added)}); priced.cost != nil {
			usd += *priced.cost
		}
	}
	return tokens, usd
}

func topKey64(counts map[string]int64) string {
	best, top := "", int64(0)
	for key, count := range counts {
		if key != "" && (count > top || count == top && key < best) {
			best, top = key, count
		}
	}
	return best
}

// instructionGrowth compares the target's instruction files now with the
// sizes recorded at the copy.
func (env *findingEnv) instructionGrowth(item *findingIntervention) map[string]int64 {
	if len(item.Plan.Files) == 0 {
		return nil
	}
	current := currentInstructionFiles(env, item.Target, item.HostID)
	if current == nil {
		return nil
	}
	grown := map[string]int64{}
	for path, size := range current {
		if strings.HasSuffix(path, "SKILL.md") || strings.HasSuffix(path, ".toml") {
			continue
		}
		grown[path] = size - item.Plan.Files[path]
	}
	return grown
}

// currentInstructionFiles lists a target's instruction files and sizes: a
// repository's default branch, or this Mac's global files for a global
// target on this Mac. nil means they can't be read here.
func currentInstructionFiles(env *findingEnv, target, host string) map[string]int64 {
	kind, rest, _ := strings.Cut(target, ":")
	switch kind {
	case "repo":
		files, ok := sharedInstructionInventories.at(env.locations[rest], time.Time{})
		if !ok {
			return nil
		}
		return files
	case "global":
		targetHost, provider, _ := strings.Cut(rest, ":")
		if targetHost != env.catalog.currentHostID() || host != "" && host != targetHost {
			return nil
		}
		return globalInstructionSizes(provider)
	}
	return nil
}

// globalInstructionSizes reads the sizes of one provider's global
// instruction files on this Mac.
func globalInstructionSizes(provider string) map[string]int64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	paths := map[string][]string{"claude": {".claude/CLAUDE.md"}, "codex": {".codex/AGENTS.md", ".codex/AGENTS.override.md"}, "antigravity": {".gemini/GEMINI.md"}}[provider]
	files := map[string]int64{}
	for _, path := range paths {
		if info, err := os.Stat(filepath.Join(home, path)); err == nil {
			files[path] = info.Size()
		}
	}
	return files
}
