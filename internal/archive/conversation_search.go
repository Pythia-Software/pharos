package archive

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// LibraryFind searches the evidence itself. Text results are grouped by the
// conversation that owns each message; file and URL results retain their
// provenance instead of inheriting a workspace's semantic score.
type LibraryFindOptions struct {
	Kind, Query                      string
	Fuzzy, CaseSensitive, Separators bool
	Limit, Offset                    int
	limited                          *bool
	Search                           TextSearchOptions
	searchResult                     *textSearchResult
}

type libraryHit struct {
	WorkspaceID    string `json:"workspace_id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Title          string `json:"title"`
	Provider       string `json:"provider,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	Role           string `json:"role,omitempty"`
	MessageKind    string `json:"message_kind,omitempty"`
	Snippet        string `json:"snippet,omitempty"`
	Path           string `json:"path,omitempty"`
	OldPath        string `json:"old_path,omitempty"`
	URL            string `json:"url,omitempty"`
	URLSource      string `json:"url_source,omitempty"`
	ToolName       string `json:"tool_name,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
	Attribution    string `json:"attribution,omitempty"`
	Count          int    `json:"count"`
}

var libraryWords = regexp.MustCompile(`[\pL\pN]+`)

func quoteFTS(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func (c *Catalog) LibraryFind(ctx context.Context, o LibraryFindOptions) (map[string]any, error) {
	var searchResult textSearchResult
	o.searchResult = &searchResult
	hits, limited, err := c.libraryFindHits(ctx, o)
	if err != nil {
		return nil, err
	}
	limit := clamp(o.Limit, 1, 100)
	offset := max(o.Offset, 0)
	total := len(hits)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	searchResult.Items = nil
	return map[string]any{"items": hits[offset:end], "total": total, "limit": limit, "offset": offset, "limited": limited, "search": searchResult}, nil
}

// libraryFindHits returns a bounded batch and whether more work remains.
// Text uses reverse index order and execution budgets; files and URLs retain
// their existing ordering and 5,000-record cap.
func (c *Catalog) libraryFindHits(ctx context.Context, o LibraryFindOptions) ([]libraryHit, bool, error) {
	o.Query = strings.TrimSpace(o.Query)
	if o.Query == "" && o.Search.Where == nil {
		return []libraryHit{}, false, nil
	}
	if o.Search.Where != nil && o.Kind != "" && o.Kind != "text" {
		return nil, false, fmt.Errorf("visual text rules cannot be used for file or URL searches")
	}
	if len(o.Query) > 2000 {
		return nil, false, fmt.Errorf("search query is too long (maximum 2,000 bytes)")
	}
	limited := false
	o.limited = &limited
	var hits []libraryHit
	var err error
	switch o.Kind {
	case "", "text":
		hits, err = c.findText(ctx, o)
	case "file":
		hits, err = c.findFiles(ctx, o)
	case "url":
		hits, err = c.findURLs(ctx, o)
	default:
		return nil, false, fmt.Errorf("unknown search kind %q", o.Kind)
	}
	return hits, limited, err
}

// A small edit distance supplements FTS word lookup. This works on vocabulary
// terms, so a typo need not force a scan of every message body.
func editDistance(a, b string, maxDistance int) int {
	x, y := []rune(a), []rune(b)
	if max(len(x)-len(y), len(y)-len(x)) > maxDistance {
		return maxDistance + 1
	}
	previous := make([]int, len(y)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, left := range x {
		current := make([]int, len(y)+1)
		current[0] = i + 1
		minimum := current[0]
		for j, right := range y {
			cost := 0
			if left != right {
				cost = 1
			}
			current[j+1] = min(previous[j+1]+1, current[j]+1, previous[j]+cost)
			minimum = min(minimum, current[j+1])
		}
		if minimum > maxDistance {
			return maxDistance + 1
		}
		previous = current
	}
	return previous[len(y)]
}

func fuzzyRoot(s string) string {
	s = strings.ToLower(s)
	// Deceive and deception have different Latin stems. Treat their common
	// family explicitly rather than claiming edit distance relates them.
	if strings.HasPrefix(s, "deceiv") || strings.HasPrefix(s, "deciev") || strings.HasPrefix(s, "decept") || strings.HasPrefix(s, "deceit") {
		return "deceive"
	}
	for _, suffix := range []string{"ation", "ments", "ment", "ingly", "ing", "edly", "ed", "ies", "es", "s"} {
		if strings.HasSuffix(s, suffix) && len(s) > len(suffix)+3 {
			return strings.TrimSuffix(s, suffix)
		}
	}
	return s
}

func sameCasing(a, b string) bool {
	// Exact spellings with different case must never become fuzzy hits.
	if strings.ToLower(a) == strings.ToLower(b) {
		return a == b
	}
	style := func(s string) string {
		letters := []rune(s)
		upper := 0
		for _, r := range letters {
			if unicode.IsUpper(r) {
				upper++
			}
		}
		if upper == 0 {
			return "lower"
		}
		if upper == len(letters) {
			return "upper"
		}
		if unicode.IsUpper(letters[0]) && upper == 1 {
			return "title"
		}
		return "mixed:" + s
	}
	return style(a) == style(b)
}

func (c *Catalog) fuzzyTermsWith(ctx context.Context, word string, prefixLength, edits, expansionLimit int) ([]string, error) {
	terms := []string{strings.ToLower(word)}
	if len([]rune(word)) < 4 {
		return terms, nil
	}
	// Restrict expansion to the requested prefix and the edit-distance length
	// band. Do not cap the vocabulary scan: common prefixes can have more than
	// 10,000 terms, with the actual correction beyond the first 10,000.
	prefix := strings.ToLower(string([]rune(word)[:min(prefixLength, len([]rune(word)))]))
	maxDistance := 1
	if len([]rune(word)) >= 7 {
		maxDistance = 2
	}
	if edits > 0 {
		maxDistance = edits
	}
	rows, err := c.reader().QueryContext(ctx, `SELECT term FROM messages_vocab WHERE term>=? AND term<? AND length(term) BETWEEN ? AND ?`,
		prefix, prefix+"\uffff", len([]rune(word))-maxDistance, len([]rune(word))+maxDistance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		term     string
		distance int
	}
	candidates := []candidate{}
	seen := map[string]bool{terms[0]: true}
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			return nil, err
		}
		if seen[term] {
			continue
		}
		distance := editDistance(strings.ToLower(word), term, maxDistance)
		if fuzzyRoot(word) == fuzzyRoot(term) {
			distance = 0
		}
		if distance <= maxDistance {
			candidates = append(candidates, candidate{term, distance})
			seen[term] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distance == candidates[j].distance {
			return candidates[i].term < candidates[j].term
		}
		return candidates[i].distance < candidates[j].distance
	})
	for _, item := range candidates[:min(expansionLimit, len(candidates))] {
		terms = append(terms, item.term)
	}
	return terms, nil
}

func (c *Catalog) findText(ctx context.Context, o LibraryFindOptions) ([]libraryHit, error) {
	options := o.Search
	options.Query = o.Query
	options.Fuzzy = o.Fuzzy
	options.CaseSensitive = o.CaseSensitive
	options.Separators = o.Separators
	options.SelectedOnly = true
	options.Limit = 5000
	result, err := c.SearchText(ctx, options)
	if err != nil {
		return nil, err
	}
	if o.limited != nil {
		*o.limited = !result.Complete
	}
	if o.searchResult != nil {
		*o.searchResult = result
	}
	groups := map[string]int{}
	hits := []libraryHit{}
	for _, item := range result.Items {
		if i, ok := groups[item.ConversationID]; ok {
			hits[i].Count++
			continue
		}
		groups[item.ConversationID] = len(hits)
		hits = append(hits, item.libraryHit)
	}
	return hits, nil
}

func searchSnippet(body string, start, end int) string {
	runes := []rune(body)
	start = len([]rune(body[:min(max(start, 0), len(body))]))
	end = len([]rune(body[:min(max(end, 0), len(body))]))
	lo, hi := max(0, start-90), min(len(runes), end+130)
	text := strings.TrimSpace(string(runes[lo:hi]))
	if lo > 0 {
		text = "…" + text
	}
	if hi < len(runes) {
		text += "…"
	}
	return text
}

func (c *Catalog) findFiles(ctx context.Context, o LibraryFindOptions) ([]libraryHit, error) {
	comparison := "instr(lower(cf.path),lower(?))>0 OR instr(lower(COALESCE(cf.old_path,'')),lower(?))>0"
	if o.CaseSensitive {
		comparison = "instr(cf.path,?)>0 OR instr(COALESCE(cf.old_path,''),?)>0"
	}
	scope := ""
	args := []any{o.Query, o.Query}
	if o.Search.WorkspaceIDs != nil {
		scope = " AND cs.workspace_id IN (SELECT value FROM json_each(?))"
		args = append(args, jsonText(o.Search.WorkspaceIDs))
	}
	rows, err := queryMapsContext(ctx, c.reader(), `WITH matching_changes AS (
		SELECT cf.path,cf.old_path,cs.id change_set_id,cs.workspace_id,cs.created_at,cs.attempt_id
		FROM change_files cf JOIN change_sets cs ON cs.id=cf.change_set_id
		WHERE (`+comparison+`)`+scope+`
		ORDER BY cs.created_at DESC LIMIT 5001
	) SELECT cf.path,cf.old_path,cf.change_set_id,cf.workspace_id,cf.created_at,cf.attempt_id,w.title,
		c.id conversation_id,c.provider,c.started_at,c.ended_at,c.work_item_id
		FROM matching_changes cf JOIN workspaces w ON w.id=cf.workspace_id
		LEFT JOIN conversations c ON c.workspace_id=w.id`, args...)
	if err != nil {
		return nil, err
	}
	// A change set has workspace scope. Link it to a conversation only when
	// its work item, time interval, or the sole conversation identifies one.
	byWork := map[string][]map[string]any{}
	for _, row := range rows {
		key := firstString(row["change_set_id"]) + "\x00" + firstString(row["path"])
		byWork[key] = append(byWork[key], row)
	}
	if len(byWork) > 5000 {
		*o.limited = true
	}
	hits := []libraryHit{}
	for _, group := range byWork {
		row := group[0]
		chosen := []map[string]any{}
		if len(group) == 1 && firstString(row["conversation_id"]) != "" {
			chosen = group
		}
		if len(chosen) == 0 && firstString(row["attempt_id"]) != "" {
			var workItem string
			_ = c.reader().QueryRowContext(ctx, `SELECT work_item_id FROM task_attempts WHERE id=?`, row["attempt_id"]).Scan(&workItem)
			for _, candidate := range group {
				if workItem != "" && firstString(candidate["work_item_id"]) == workItem {
					chosen = append(chosen, candidate)
				}
			}
		}
		if len(chosen) == 0 {
			at := firstString(row["created_at"])
			for _, candidate := range group {
				if at != "" && firstString(candidate["started_at"]) != "" && firstString(candidate["started_at"]) <= at && (firstString(candidate["ended_at"]) == "" || at <= firstString(candidate["ended_at"])) {
					chosen = append(chosen, candidate)
				}
			}
		}
		if len(chosen) != 1 {
			chosen = []map[string]any{row}
			if len(group) > 1 {
				chosen[0] = map[string]any{}
				for k, v := range row {
					chosen[0][k] = v
				}
				chosen[0]["conversation_id"] = ""
			}
		}
		for _, candidate := range chosen {
			hit := libraryHit{WorkspaceID: firstString(row["workspace_id"]), ConversationID: firstString(candidate["conversation_id"]), Title: firstString(row["title"]), Provider: firstString(candidate["provider"]), Path: firstString(row["path"]), OldPath: firstString(row["old_path"]), Count: 1}
			if hit.ConversationID == "" {
				hit.Attribution = "workspace"
			} else {
				hit.Attribution = "conversation"
			}
			hits = append(hits, hit)
		}
	}
	unique := map[string]*libraryHit{}
	result := []libraryHit{}
	for _, hit := range hits {
		key := hit.WorkspaceID + "\x00" + hit.ConversationID + "\x00" + hit.Path
		if existing := unique[key]; existing != nil {
			existing.Count++
			continue
		}
		copy := hit
		unique[key] = &copy
		result = append(result, hit)
	}
	for i := range result {
		hit := &result[i]
		hit.Count = unique[hit.WorkspaceID+"\x00"+hit.ConversationID+"\x00"+hit.Path].Count
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Title+result[i].Path+result[i].ConversationID < result[j].Title+result[j].Path+result[j].ConversationID
	})
	return result, nil
}

func (c *Catalog) findURLs(ctx context.Context, o LibraryFindOptions) ([]libraryHit, error) {
	needle := o.Query
	if !o.CaseSensitive {
		needle = strings.ToLower(needle)
	}
	comparison := "instr(lower(u.url),?)>0 OR instr(lower(COALESCE(u.host,'')),?)>0"
	if o.CaseSensitive {
		comparison = "instr(u.url,?)>0 OR instr(COALESCE(u.host,''),?)>0"
	}
	scope := ""
	args := []any{needle, needle}
	if o.Search.WorkspaceIDs != nil {
		scope = " AND t.workspace_id IN (SELECT value FROM json_each(?))"
		args = append(args, jsonText(o.Search.WorkspaceIDs))
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT u.url,u.source,t.id tool_call_id,t.tool_name,t.call_message_id,t.conversation_id,
		t.workspace_id,w.title,c.provider,c.started_at FROM tool_urls u JOIN tool_calls t ON t.id=u.tool_call_id
		JOIN conversations c ON c.id=t.conversation_id JOIN workspaces w ON w.id=t.workspace_id
		WHERE u.source<>'search_result' AND (`+comparison+`)`+scope+` ORDER BY t.started_at DESC LIMIT 5001`, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) > 5000 {
		*o.limited = true
		rows = rows[:5000]
	}
	hits := []libraryHit{}
	for _, row := range rows {
		hits = append(hits, libraryHit{WorkspaceID: firstString(row["workspace_id"]), ConversationID: firstString(row["conversation_id"]), Title: firstString(row["title"]), Provider: firstString(row["provider"]), StartedAt: firstString(row["started_at"]), MessageID: firstString(row["call_message_id"]), URL: firstString(row["url"]), URLSource: firstString(row["source"]), ToolName: firstString(row["tool_name"]), ToolCallID: firstString(row["tool_call_id"]), Count: 1})
	}
	return hits, nil
}
