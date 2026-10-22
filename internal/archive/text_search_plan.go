package archive

import (
	"context"
	"slices"
	"strings"
	"time"
)

var textSearchPlanMaxRows = 256

// Try metadata first when a filter can leave only a few messages. The prose
// role/time index covers this read without fetching message bodies. Cap both
// rows and planning time: a broad predicate should keep streaming the word
// index, not enumerate the whole metadata population before its first hit.
// A nil result means use that general plan; an empty result proves no matches.
func (c *Catalog) textSearchFilteredRows(ctx context.Context, o TextSearchOptions) ([]int64, error) {
	planCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	roles := []string{"user", "assistant"}
	if o.Surface == "user" || textSpanSurface(o.Surface) {
		roles = []string{"user"}
	}
	if o.Surface == "assistant" {
		roles = []string{"assistant"}
	}
	if o.Roles != nil {
		roles = slices.DeleteFunc(roles, func(role string) bool { return !slices.Contains(o.Roles, role) })
	}
	filters, args, err := messageFilters(textFilterArgs(o), "m.", false)
	if err != nil {
		return nil, err
	}
	parts := []string{filters}
	for _, bound := range []struct {
		value, op string
		end       bool
	}{{o.From, ">=", false}, {o.To, "<=", true}} {
		if bound.value != "" {
			// Stored timestamps are canonical UTC (timeLayout). A wider ISO
			// date guard also admits legacy offsets and space separators;
			// julianday still makes the exact decision. The raw guard lets
			// messages_prose_idx seek rather than walk every dated message.
			instant, _ := parseTime(timeBound(bound.value, bound.end))
			if instant.Year() > 1 && instant.Year() < 9999 {
				days, op := -2, ">="
				if bound.end {
					days, op = 3, "<"
				}
				parts = append(parts, "m.created_at"+op+"?")
				args = append(args, instant.AddDate(0, 0, days).Format("2006-01-02"))
			}
			parts = append(parts, "julianday(m.created_at)"+bound.op+"julianday(?)")
			args = append(args, timeBound(bound.value, bound.end))
		}
	}
	predicate := strings.Join(parts, " AND ")
	sources := []string{}
	values := []any{}
	if len(roles) > 0 && o.Surface != "reasoning" && (len(o.Kinds) == 0 || slices.Contains(o.Kinds, "message") || slices.Contains(o.Kinds, "all")) {
		quoted := make([]string, len(roles))
		for i, role := range roles {
			quoted[i] = quoteSQLText(role)
		}
		sources = append(sources, `SELECT m.rowid FROM messages m WHERE m.kind='message' AND m.role IN (`+strings.Join(quoted, ",")+`) AND `+predicate)
		values = append(values, args...)
	}
	if o.Surface == "prose" || o.Surface == "reasoning" {
		sources = append(sources, `SELECT m.rowid FROM text_search_documents d CROSS JOIN messages m ON m.rowid=d.message_rowid WHERE d.surface='reasoning' AND `+predicate)
		values = append(values, args...)
	}
	if len(sources) == 0 {
		return []int64{}, nil
	}
	rows, err := c.reader().QueryContext(planCtx, strings.Join(sources, " UNION ALL ")+" LIMIT ?", append(values, textSearchPlanMaxRows+1)...)
	if err != nil {
		if ctx.Err() == nil && planCtx.Err() != nil {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			if ctx.Err() == nil && planCtx.Err() != nil {
				return nil, nil
			}
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	if ctx.Err() == nil && planCtx.Err() != nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(ids) > textSearchPlanMaxRows {
		return nil, nil
	}
	return ids, nil
}
