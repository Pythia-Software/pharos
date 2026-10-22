package archive

import (
	"context"
	"strings"
	"time"
)

// usageSummaryWindows are the header summary's trailing windows, newest
// first; a zero span is all time.
var usageSummaryWindows = []struct {
	key  string
	span time.Duration
}{
	{"1h", time.Hour}, {"6h", 6 * time.Hour}, {"24h", 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}, {"all", 0},
}

// usageSummaryWindow is one window's topline totals.
type usageSummaryWindow struct {
	Key          string                        `json:"key"`
	HumanWords   int64                         `json:"human_words"`
	Messages     int64                         `json:"messages"`
	Tokens       int64                         `json:"tokens"`
	CostUSD      any                           `json:"cost_usd"`
	CarbonTokens map[string]map[string]float64 `json:"carbon_tokens"`
}

// UsageHistory gives charts the library's unfiltered conversation history.
// Keep it separate from the usage summary's more expensive ledger reads.
func (c *Catalog) UsageHistory(ctx context.Context) (map[string]any, error) {
	return cachedValue(ctx, c, "usage:history", func(ctx context.Context) (map[string]any, error) {
		rows, err := queryMapsContext(ctx, c.reader(), `SELECT started_at FROM conversations
			WHERE julianday(started_at) IS NOT NULL ORDER BY julianday(started_at),started_at LIMIT 1`)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"oldest_conversation_at": nil}
		if len(rows) > 0 {
			result["oldest_conversation_at"] = rows[0]["started_at"]
		}
		return result, nil
	})
}

// UsageSummary answers GET /api/usage/summary: words typed by a person, the
// messages carrying them, tokens, API-equivalent cost, and carbon inputs by
// model tier and token category over trailing windows. Human text comes from
// the authorship ledger, which already keeps only each mirror group's
// representative work; usage metrics come from the same de-mirrored ledger as
// the Usage page. That ledger is hourly, so a window's starting hour counts in
// proportion to its overlap.
func (c *Catalog) UsageSummary(ctx context.Context) (map[string]any, error) {
	now := c.clock()
	windows := make([]*usageSummaryWindow, len(usageSummaryWindows))
	cutoffs := make([]time.Time, len(usageSummaryWindows))
	for index, window := range usageSummaryWindows {
		windows[index] = &usageSummaryWindow{Key: window.key}
		if window.span > 0 {
			cutoffs[index] = now.Add(-window.span)
		}
	}

	c.ensureAuthorship()
	rows, err := c.reader().QueryContext(ctx, usageSummaryAuthorshipSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sentAt string
		var words int64
		if err := rows.Scan(&sentAt, &words); err != nil {
			return nil, err
		}
		sent, ok := parseTime(sentAt)
		for index, window := range windows {
			if cutoffs[index].IsZero() || ok && !sent.Before(cutoffs[index]) {
				window.HumanWords += words
				window.Messages++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ledger, err := c.usageSummaryLedger(ctx)
	if err != nil {
		return nil, err
	}
	book, err := c.loadPriceBookFrom(ctx, c.reader())
	if err != nil {
		return nil, err
	}
	carbon, err := loadCarbonDocument()
	if err != nil {
		return nil, err
	}
	for _, entry := range ledger {
		hour, ok := parseTime(entry.hour)
		day := ""
		if ok {
			day = localMidnight(hour, time.Local).Format("2006-01-02")
		}
		priced := book.cost(entry.model, day, entry.tokens)
		tier, _ := carbon.tier(entry.model)
		carbonParts := map[string]int64{
			"uncached_input": entry.tokens["uncached_input_tokens"] + entry.tokens["unclassified_tokens"],
			"cache_write":    entry.tokens["cache_creation_input_tokens"],
			"cache_read":     entry.tokens["cache_read_input_tokens"],
			"output":         entry.tokens["output_tokens"],
		}
		for index, window := range windows {
			share := 1.0
			if !cutoffs[index].IsZero() {
				if !ok {
					continue
				}
				share = min(max(hour.Add(time.Hour).Sub(cutoffs[index]).Hours(), 0), 1)
			}
			if share == 0 {
				continue
			}
			window.Tokens += int64(float64(entry.tokens["total_tokens"])*share + .5)
			if window.CarbonTokens == nil {
				window.CarbonTokens = map[string]map[string]float64{}
			}
			if window.CarbonTokens[tier] == nil {
				window.CarbonTokens[tier] = map[string]float64{}
			}
			for category, count := range carbonParts {
				window.CarbonTokens[tier][category] += float64(count) * share
			}
			if priced.cost != nil {
				cost := *priced.cost * share
				window.CostUSD = addCost(window.CostUSD, &cost)
			}
		}
	}
	return map[string]any{"generated_at": now.UTC().Format(time.RFC3339), "windows": windows, "carbon_factors": carbon}, nil
}

type usageSummaryEntry struct {
	workspaceID, hour, model string
	tokens                   map[string]int64
}

const usageSummaryAuthorshipSQL = `SELECT sent_at,typed_words FROM message_authorship WHERE typed_words>0`

// The summary reads only its aggregate inputs. The general Usage
// ledger joins conversation/repository metadata and reads workspace rows
// whose purpose/outcome can occupy many overflow pages. Covering indexes
// keep this query off those records, and typed scans avoid materializing
// metadata maps for every ledger row. INDEXED BY prevents SQLite from choosing
// the primary-key indexes, which would fetch the full records again.
const usageSummaryLedgerSQL = `SELECT a.workspace_id,w.source_kind,u.usage_hour,u.model,COALESCE(a.model,''),
	u.total_tokens,u.uncached_input_tokens,u.cache_read_input_tokens,u.cache_creation_input_tokens,
	u.cache_creation_1h_input_tokens,u.output_tokens,u.unclassified_tokens
	FROM agent_session_usage u
	JOIN agent_sessions a INDEXED BY agent_sessions_summary_idx ON a.id=u.agent_session_id
	JOIN workspaces w INDEXED BY workspaces_library_idx ON w.id=a.workspace_id
	WHERE u.total_tokens>0`

func (c *Catalog) usageSummaryLedger(ctx context.Context) ([]usageSummaryEntry, error) {
	rows, err := c.reader().QueryContext(ctx, usageSummaryLedgerSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ledger []usageSummaryEntry
	var workspaces []map[string]any
	seen := map[string]bool{}
	for rows.Next() {
		var entry usageSummaryEntry
		var source, sessionModel string
		var total, input, read, write, write1h, output, unclassified int64
		if err := rows.Scan(&entry.workspaceID, &source, &entry.hour, &entry.model, &sessionModel,
			&total, &input, &read, &write, &write1h, &output, &unclassified); err != nil {
			return nil, err
		}
		entry.model = strings.TrimSpace(entry.model)
		if entry.model == "" {
			entry.model = strings.TrimSpace(sessionModel)
		}
		if entry.model == "" {
			entry.model = "Unknown model"
		}
		entry.tokens = map[string]int64{
			"total_tokens": total, "uncached_input_tokens": input, "cache_read_input_tokens": read,
			"cache_creation_input_tokens": write, "cache_creation_1h_input_tokens": write1h,
			"output_tokens": output, "unclassified_tokens": unclassified,
		}
		ledger = append(ledger, entry)
		if !seen[entry.workspaceID] {
			seen[entry.workspaceID] = true
			workspaces = append(workspaces, map[string]any{"id": entry.workspaceID, "source_kind": source})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Match representativeLedger: choose among workspaces with positive usage
	// across all time, even when a mirror has no usage in a trailing window.
	representative := map[string]bool{}
	for _, workspace := range c.suppressMirrors(workspaces) {
		representative[firstString(workspace["id"])] = true
	}
	kept := ledger[:0]
	for _, entry := range ledger {
		if representative[entry.workspaceID] {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}
