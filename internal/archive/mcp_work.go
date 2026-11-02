package archive

import "context"

// searchWorkContext keeps the descriptive search path within the same card
// budget as metric search. Advance by returned cards, including budget cuts.
func (c *Catalog) searchWorkContext(ctx context.Context, args map[string]any) (map[string]any, error) {
	limit := clamp(int(integer(valueOr(args["limit"], 50))), 1, 100)
	offset := max(0, int(integer(args["offset"])))
	options := SearchOptions{Query: firstString(args["query"]), Repository: firstString(args["repository"]), Source: firstString(args["source"]), File: firstString(args["file"]), Owner: firstString(args["owner"]), Provider: firstString(args["provider"]), Model: firstString(args["model"]), Effort: firstString(args["effort"]), From: firstString(args["from"]), To: firstString(args["to"]), Flavor: firstString(args["flavor"]), Version: firstString(args["version"]), Outcome: firstString(args["outcome"]), Error: firstString(args["error"]), Metric: firstString(args["metric"]), Limit: limit, Offset: offset, ctx: ctx}
	if args["pr"] != nil {
		value := int(integer(args["pr"]))
		options.PR = &value
	}
	if value, ok := number(args["minimum"]); ok {
		options.Minimum = &value
	}
	if value, ok := number(args["maximum"]); ok {
		options.Maximum = &value
	}
	rows, err := c.searchRows(options, libraryCompactFields)
	if err != nil {
		return nil, err
	}
	start := min(offset, len(rows))
	end := start + min(limit, len(rows)-start)
	items, err := c.libraryPage(ctx, rows[start:end])
	if err != nil {
		return nil, err
	}
	result := map[string]any{"items": items, "limit": limit, "offset": offset, "total": len(rows), "next_offset": nil,
		"details_tool": "get_work_detail"}
	// Descriptive fields are previews. Full fields remain available through
	// get_work_detail; a large preview must not consume a whole CLI stream.
	for _, item := range items {
		// Full lists and duplicate summaries belong in get_work_detail.
		// Use the Library's long-field classification so new large columns
		// cannot silently bypass the card budget's preview handling.
		for _, key := range libraryLargeFields {
			if key != "purpose" && key != "outcome" {
				delete(item, key)
			}
		}
		for _, key := range []string{"purpose", "outcome", "first_input", "last_response"} {
			if value, ok := item[key].(string); ok {
				item[key] = clipText(value, 160)
			}
		}
	}
	if err := c.attachWorkspaceFreshness(result["items"].([]map[string]any)); err != nil {
		return nil, err
	}
	freshness, err := c.archiveSourceStatus()
	if err != nil {
		return nil, err
	}
	result["freshness"] = map[string]any{"status": freshness["status"], "stale_sources": freshness["stale_sources"]}
	result["search_mode"] = "ranked_lexical_and_semantic"
	if options.Query != "" {
		result["coverage"] = toolOutputCoverage
		// Library search results are cached, so whether this one's
		// window was cut short is not known here: say it may be.
		if left, err := c.toolOutputPostingsLeft(ctx); err == nil && left {
			result["coverage"] = toolOutputCoverage + " Old tool-output postings are still being removed from the word index, so this search may rank fewer recent messages than usual."
		}
	}
	if err := fitAuditPage(result, mcpBudget(args, 4000), offset, len(rows)); err != nil {
		return nil, err
	}
	return result, nil
}
