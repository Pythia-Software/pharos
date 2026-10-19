package archive

import (
	"context"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

// findingLeverLabels name where a lever's change is made. Step labels such as
// "A line in AGENTS.md" describe one step and are written to follow "Suggests:".
var findingLeverLabels = map[string]string{
	"repo-instructions":   "Repository instructions",
	"global-instructions": "Global instructions",
	"repo-tooling":        "Repository tools & skills",
	"harness-settings":    "Harness settings",
	"automation-prompt":   "Automation prompts",
}

var findingLabelArticle = regexp.MustCompile(`(?i)^(an?|one)\s+`)

// findingFixKind groups a card's change by where it is made.
func findingFixKind(lever, changeLabel string) string {
	if lever != "" {
		return defaultString(findingLeverLabels[lever], "Other change")
	}
	// Rows from before the lever field: name the step without its article.
	label := findingLabelArticle.ReplaceAllString(changeLabel, "")
	if label == "" {
		return "Other change"
	}
	first, size := utf8.DecodeRuneInString(label)
	return string(unicode.ToUpper(first)) + label[size:]
}

var findingImpactUnits = []string{"usd", "tokens", "minutes", "failures"}

// findingQueryRows backs the findings query table: one row per finding the
// Findings view lists, valued as its card is. Unlike GET /api/findings it
// never starts a build, so an unbuilt catalog simply has no rows. State
// follows the clock (snoozes end), so the rows are not cached.
func (c *Catalog) findingQueryRows(ctx context.Context) ([]map[string]any, error) {
	view, err := c.loadFindingView(ctx)
	if err != nil {
		return nil, err
	}
	rank := view.cart.settings.Rank
	rows := make([]map[string]any, 0, len(view.rows))
	for _, row := range view.rows {
		state := view.state(row)
		if state == findingHidden {
			continue
		}
		card := view.card(row, state)
		lever, change := firstString(card["lever"]), firstString(card["change_label"])
		item := map[string]any{"id": row.ID, "title": row.Card.Title, "detector": row.Detector, "detector_label": nilIfEmpty(findingDetectorLabels[row.Detector]),
			"state": state, "is_new": state == findingOpen && view.isNew(row), "lever": nilIfEmpty(lever), "lever_label": findingFixKind(lever, change),
			"change_label": nilIfEmpty(change), "target_label": nilIfEmpty(firstString(card["target_label"])), "scope_label": nilIfEmpty(firstString(card["scope_label"])),
			"repository_name": nilIfEmpty(view.cart.repos[row.RepositoryID]), "rate": nilIfEmpty(row.Card.Rate), "first_seen_at": nilIfEmpty(row.FirstSeenAt),
			"day": nil, "rank_unit": rank, "finding_count": 1}
		if at, ok := parseTime(row.FirstSeenAt); ok {
			item["day"] = at.In(time.Local).Format("2006-01-02")
		}
		expected := mapValueDefault(card["expected"])
		for _, unit := range findingImpactUnits {
			item["likely_"+unit] = floatOr(expected[unit])
			item["at_stake_"+unit] = floatOr(row.Impact[unit])
		}
		item["likely_ranked"], item["at_stake_ranked"] = item["likely_"+rank], item["at_stake_"+rank]
		var saved findingSavings
		wins, regressed := 0, 0
		if user := view.states[row.ID]; user != nil {
			saved, wins, regressed = user.improved()
		}
		item["saved_usd"], item["saved_added_usd"], item["saved_net_usd"] = saved.USD, saved.AddedUSD, saved.USD-saved.AddedUSD
		item["saved_tokens"], item["saved_added_tokens"], item["saved_minutes"], item["saved_failures"] = saved.Tokens, saved.AddedTokens, saved.Minutes, saved.Failures
		item["saved_weeks"], item["wins"], item["regressed"] = saved.Weeks, wins, regressed > 0
		rows = append(rows, item)
	}
	return rows, nil
}
