package archive

import "sort"

func statisticsProperties() map[string]any {
	names := []string{}
	for _, d := range metricDefinitions {
		names = append(names, d.Name)
	}
	for alias := range metricAliases {
		names = append(names, alias)
	}
	sort.Strings(names)
	metric := map[string]any{"type": "string", "enum": names}
	return map[string]any{
		"metric_scope":    map[string]any{"type": "string", "enum": []string{"conversation", "workspace"}},
		"metric_quality":  map[string]any{"type": "string", "enum": []string{"complete", "available"}, "default": "complete", "description": "Presence, comparisons and ordering use complete ready values by default; available also accepts ready partial/assumed values."},
		"metric_filters":  map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "object", "required": []string{"name", "op"}, "additionalProperties": false, "properties": map[string]any{"name": metric, "op": map[string]any{"type": "string", "enum": []string{"gt", "gte", "lt", "lte", "eq", "is_missing", "is_present"}}, "value": map[string]any{"type": "number"}}}},
		"order_by":        map[string]any{"type": "object", "required": []string{"metric"}, "additionalProperties": false, "properties": map[string]any{"metric": metric, "direction": map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "default": "desc"}}},
		"result_mode":     map[string]any{"type": "string", "enum": []string{"cards", "metadata"}, "default": "cards", "description": "Metadata returns selected numeric fields and IDs without preview/card reads; cursor paging uses a five-minute snapshot."},
		"select":          map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string"}, "description": "Allowlisted metrics or small context fields. Empty selects IDs only. Filter/order metrics are always returned. conversation_ids optionally returns up to 20 IDs with explicit completeness and batch access."},
		"workspace_ids":   map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1}},
		"cursor":          map[string]any{"type": "string", "description": "Copy next_cursor and the effective original request. Do not supply offset."},
		"snapshot_id":     map[string]any{"type": "string", "description": "Reuse captured population/values for compatible projections or aggregates; uncaptured fields require a new snapshot."},
		"retain_snapshot": map[string]any{"type": "boolean", "default": false, "description": "Keep a five-minute snapshot even for a complete one-shot response, so a later query can reuse captured values. Pagination and truncated conversation-ID follow-ups retain snapshots automatically."},
	}
}

func init() {
	for _, tool := range mcpTools {
		name := firstString(tool["name"])
		if name == "get_conversation_overview" {
			tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["include_metrics"] = map[string]any{"type": "boolean", "description": "Include the full own-stream metric set with quality evidence; increase max_output_tokens if needed."}
		}
		if name == "query_metrics" {
			tool["description"] = "Legacy metric records with historical suppression behavior. Prefer query_statistics for scoped, sorted metrics and aggregates."
		}
		if name != "search_conversations" && name != "search_work" {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		properties := schema["properties"].(map[string]any)
		for _, key := range []string{"channel", "machine_id", "execution_machine_id"} {
			properties[key] = map[string]any{"type": "string"}
		}
		for k, v := range statisticsProperties() {
			properties[k] = v
		}
		properties["offset"] = map[string]any{"type": "integer", "minimum": 0, "description": "For cards, copy next_offset with the original filters; metadata uses next_cursor instead."}
		properties["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 500}
		properties["max_output_tokens"] = map[string]any{"type": "integer", "minimum": 200, "maximum": 8000}
		cardMax := 100
		if name == "search_conversations" {
			cardMax = 20
		}
		schema["allOf"] = []any{map[string]any{"if": map[string]any{"required": []string{"result_mode"}, "properties": map[string]any{"result_mode": map[string]any{"const": "metadata"}}}, "else": map[string]any{"properties": map[string]any{"limit": map[string]any{"maximum": cardMax}, "max_output_tokens": map[string]any{"maximum": 4000}}}}}
		tool["description"] = firstString(tool["description"]) + " Metric filters and sorting work with or without query text. Choose result_mode=metadata for compact IDs and selected statistics; use query_statistics for bulk rows and full-population aggregates."
	}
	properties := statisticsProperties()
	properties["result_mode"] = map[string]any{"type": "string", "const": "metadata"}
	properties["release_snapshot"] = map[string]any{"type": "boolean", "description": "Release a finished snapshot before expiry; provide only release_snapshot=true and snapshot_id."}
	for _, key := range []string{"query", "repository", "source", "provider", "channel", "machine_id", "execution_machine_id", "model", "effort", "file", "from", "to", "owner", "flavor", "version", "outcome", "error"} {
		properties[key] = map[string]any{"type": "string"}
	}
	properties["pr"] = map[string]any{"type": "integer"}
	properties["entity"] = map[string]any{"type": "string", "enum": []string{"workspace", "conversation"}, "default": "workspace"}
	properties["result_type"] = map[string]any{"type": "string", "enum": []string{"rows", "aggregate"}, "default": "rows"}
	properties["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100}
	properties["max_output_tokens"] = map[string]any{"type": "integer", "minimum": 200, "maximum": 8000, "default": 4000}
	properties["aggregations"] = map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "object", "required": []string{"id", "op"}, "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "op": map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "min", "max"}}, "metric": map[string]any{"type": "string"}}}}
	properties["group_by"] = map[string]any{"type": "array", "maxItems": 3, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []string{"repository", "source", "provider", "channel", "machine_id", "execution_machine_id", "channels", "machines"}}}
	mcpTools = append(mcpTools, map[string]any{"name": "query_statistics", "description": "Query compact workspace/conversation IDs and selected metrics, or count/sum/avg/min/max over the full filtered canonical population. No previews or transcripts. Use entity=workspace for workspace totals including subagents, conversation for own-stream metrics. Omit query for exhaustive numeric discovery; text retrieval reports population_complete=false and matches word-indexed message text, which leaves out tool output (tool_result). Cursor pages freeze values for five minutes; copy the effective original arguments. Missing/partial evidence and aggregate denominators stay explicit.", "inputSchema": map[string]any{"type": "object", "properties": properties, "additionalProperties": false}}, map[string]any{"name": "list_metrics", "description": "Discover metric names, units, definitions, aliases, scopes and quality semantics before filtering or aggregating.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"metric_scope": map[string]any{"type": "string", "enum": []string{"workspace", "conversation"}}}, "additionalProperties": false}})
}
