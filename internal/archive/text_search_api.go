package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func textOptionsFromValues(values url.Values, prefix string) (TextSearchOptions, error) {
	get := func(k string) string { return values.Get(prefix + k) }
	list := func(k string) []string {
		if get(k) == "" {
			return nil
		}
		parts := strings.Split(get(k), ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	o := TextSearchOptions{Mode: get("mode"), Surface: get("surface"), Repository: get("repository"), ConversationID: get("conversation_id"), WorkspaceID: get("workspace_id"), Source: get("source"), Provider: get("provider"), Roles: list("roles"), Kinds: list("kinds"), From: get("from"), To: get("to"), Cursor: get("cursor")}
	if raw := get("where"); raw != "" {
		if len(raw) > 32000 {
			return o, fmt.Errorf("visual search is too large; remove some rules")
		}
		if err := json.Unmarshal([]byte(raw), &o.Where); err != nil || o.Where == nil {
			return o, fmt.Errorf("invalid visual search; edit the rules and search again")
		}
	}
	return o, nil
}

// Metadata choices avoid forcing users to memorize provider/source spellings.
// Neither query reads message text or waits for a search index to finish.
func (c *Catalog) textSearchEditorOptions(ctx context.Context) (map[string]any, error) {
	result := map[string]any{}
	for _, item := range []struct{ key, query string }{
		{"providers", `SELECT DISTINCT provider FROM conversations WHERE provider<>'' ORDER BY provider`},
		{"sources", `SELECT DISTINCT source_kind FROM workspaces WHERE source_kind<>'' ORDER BY source_kind`},
	} {
		rows, err := c.DB.QueryContext(ctx, item.query)
		if err != nil {
			return nil, err
		}
		values := []string{}
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		result[item.key] = values
	}
	return result, nil
}

var textSearchTool = map[string]any{
	"name":        "search_text",
	"description": "Search stored text with the same engine as Library Find. Uppercase AND, OR, binary NOT, groups, quoted token phrases, trailing * prefixes, and NEAR/0..100 (unordered token gaps), PRE/0..100 (ordered gaps). AND/NOT bind tighter than OR; NEAR binds tighter than AND. terms treats operators literally. literal and Go/RE2 regex search original text. Surfaces select prose, user, assistant, retained reasoning, tools, all, or individual authorship spans. Fuzzy expands words only, not phrases/NEAR. Stable reverse index order, not relevance. Budgets yield lower-bound counts; continue with next_cursor even after an empty page until it is empty. A time_budget_no_progress stop has no cursor: narrow the search or raise timeout_ms and retry. Oversized skipped records and missing source history prevent proving absence. Use get_search_capabilities for limits and coverage. Existing search_messages remains the legacy exhaustive lexical interface.",
	"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
		"query":                map[string]any{"type": "string", "maxLength": 2000},
		"where":                map[string]any{"type": "object", "description": "Alternative to query: typed tree with op (term, phrase, prefix, and, or, not, near, pre, literal, regex), text for leaves, children for Boolean/near nodes, distance for near. NOT has two children: positive then excluded; literal/regex only at root."},
		"mode":                 map[string]any{"type": "string", "enum": []string{"advanced", "terms", "phrase", "literal", "regex"}, "default": "advanced"},
		"surface":              map[string]any{"type": "string", "enum": []string{"prose", "user", "assistant", "reasoning", "tools", "all", "typed", "pasted", "quoted", "resent", "template", "harness", "automated", "attachment"}, "default": "prose"},
		"fuzzy_prefix_length":  map[string]any{"type": "integer", "minimum": 2, "maximum": 8, "default": 3, "description": "Initial characters that must match exactly before typo expansion. A smaller prefix is more expensive."},
		"fuzzy_max_edits":      map[string]any{"type": "integer", "minimum": 0, "maximum": 2, "default": 0, "description": "0 selects 1 edit for 4–6 character words and 2 for longer words."},
		"fuzzy_max_expansions": map[string]any{"type": "integer", "minimum": 1, "maximum": 64, "default": 24},
		"case_sensitive":       map[string]any{"type": "boolean"}, "separators": map[string]any{"type": "boolean", "description": "Require original separators in quoted phrases."}, "fuzzy": map[string]any{"type": "boolean", "default": false},
		"repository": map[string]any{"type": "string"}, "conversation_id": map[string]any{"type": "string"}, "workspace_id": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"},
		"roles": messageListSchema("Exact roles."), "kinds": messageListSchema("Exact kinds; all is allowed alone."), "selected_only": map[string]any{"type": "boolean", "default": false},
		"from": map[string]any{"type": "string", "description": "Inclusive UTC date or timestamp."}, "to": map[string]any{"type": "string", "description": "Inclusive UTC date or timestamp; date includes the whole day."},
		"cursor":            map[string]any{"type": "string", "description": "Copy next_cursor unchanged with the same query and filters. Ordinary cursors survive appends and no-op writes. Restart after edits, deletes, or index reordering; fuzzy cursors also expire on appends, and span cursors on classification changes."},
		"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50},
		"max_candidates":    map[string]any{"type": "integer", "minimum": 1, "maximum": 50000, "default": 10000},
		"max_bytes":         map[string]any{"type": "integer", "minimum": 1, "maximum": 67108864, "default": 16777216, "description": "Hard byte limit per page. Messages larger than this limit are skipped and counted in skipped_oversize. Increase the limit and restart to include them, subject to the per-message and regex work limits."},
		"timeout_ms":        map[string]any{"type": "integer", "minimum": 1, "maximum": 5000, "default": 1500},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 500, "maximum": 8000, "default": 4000},
	}},
}
var textSearchCapabilitiesTool = map[string]any{"name": "get_search_capabilities", "description": "Inspect text search operators, execution limits, surface index readiness and coverage without starting indexing.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}

func (c *Catalog) textSearchCapabilities(ctx context.Context) (map[string]any, error) {
	ready, e := c.textSearchReady(ctx)
	if e != nil {
		return nil, e
	}
	trigram, e := c.substringIndexProgress(ctx)
	if e != nil {
		return nil, e
	}
	return map[string]any{"version": 1, "small_surface_index_ready": ready, "substring_index_ready": trigram.Ready, "modes": []string{"advanced", "terms", "phrase", "literal", "regex"}, "operators": []string{"AND", "OR", "NOT", "()", "\"phrase\"", "prefix*", "NEAR/n", "PRE/n"}, "precedence": "NEAR/PRE > AND/NOT > OR; adjacent atoms AND; NOT has a positive left operand", "match_unit": "message; individual classified span for authorship surfaces", "order": "reverse index order", "regex": "Go/RE2, no lookaround or backreferences; (?m) multiline and (?s) dot-all supported; no cross-message matching", "normalization": "FTS5 unicode61 token search; strict case checks original token spelling and accents; literal/regex use Go Unicode simple folding", "surfaces": []string{"prose", "user", "assistant", "reasoning", "tools", "all", "typed", "pasted", "quoted", "resent", "template", "harness", "automated", "attachment"}, "coverage": "Only stored text. Reasoning means explicit retained thinking/reasoning/analysis kinds, not hidden or discarded thought. Authorship spans may be stale or missing. No classification is run by search.", "limits": map[string]any{"query_bytes": 2000, "nodes": 128, "depth": 16, "default_candidates": 10000, "max_candidates": 50000, "default_bytes": 16 << 20, "max_bytes": 64 << 20, "max_message_bytes": 1 << 20, "regex_max_instructions": 1000, "regex_instruction_byte_budget": 8 << 20, "default_timeout_ms": 1500, "max_timeout_ms": 5000}, "byte_budget": "Messages larger than max_bytes are skipped with skipped_oversize; increase it and restart to revisit them.", "scan_scope": "literal/regex over all/tools requires a conversation or up to 100 workspaces; small surfaces use the incremental metadata index; oversize records are explicitly skipped", "count": "lower_bound until all candidates finish with no oversize skips; next_cursor continues work even for empty pages, but time_budget_no_progress has no cursor and requires a narrower search or higher time budget"}, nil
}
func (c *Catalog) searchTextMCP(ctx context.Context, args map[string]any) (textSearchResult, error) {
	var o TextSearchOptions
	raw, e := json.Marshal(args)
	if e != nil {
		return textSearchResult{}, e
	}
	if e = json.Unmarshal(raw, &o); e != nil {
		return textSearchResult{}, e
	}
	o.Limit = clamp(int(integer(valueOr(args["limit"], 50))), 1, 100)
	result, e := c.SearchText(ctx, o)
	if e != nil {
		return result, e
	}
	budget := clamp(int(integer(valueOr(args["max_output_tokens"], 4000))), 500, 8000) * 3
	for len(jsonText(result)) > budget && len(result.Items) > 1 {
		result.Items = result.Items[:len(result.Items)-1]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = last.cursor
		result.Complete = false
		result.StopReason = "output_budget"
		result.CountRelation = "lower_bound"
		var cursor textSearchCursor
		data, _ := base64Cursor(last.cursor)
		_ = json.Unmarshal(data, &cursor)
		result.Count = cursor.Count
		result.SkippedOversize = cursor.Skipped
	}
	if len(jsonText(result)) > budget {
		return textSearchResult{}, fmt.Errorf("max_output_tokens is too small; increase it to return one result and search metadata")
	}
	return result, nil
}
func base64Cursor(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }
