package archive

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

var searchMessagesTool = map[string]any{
	"name":        "search_messages",
	"description": "Audit indexed message text across conversations with lexical-only search. Quoted phrases are exact token sequences; match_mode phrase treats the entire query as a phrase. Filters apply before pagination, with no candidate cutoff or semantic matches. Returns timestamps and evidence handles. Page using next_offset until null; archive coverage may still be incomplete. " + toolOutputNotIndexed,
	"inputSchema": map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{
		"query":           map[string]any{"type": "string", "minLength": 1},
		"match_mode":      map[string]any{"type": "string", "enum": []string{"terms", "phrase"}, "default": "terms"},
		"repository":      map[string]any{"type": "string", "description": "A repository name or remote from list_repositories, not a worktree name."},
		"conversation_id": map[string]any{"type": "string"},
		"source":          map[string]any{"type": "string"},
		"provider":        map[string]any{"type": "string"},
		"channel":         map[string]any{"type": "string"}, "machine_id": map[string]any{"type": "string"}, "execution_machine_id": map[string]any{"type": "string"},
		"roles":             messageListSchema("Exact message roles, such as user, assistant, tool, or system."),
		"kinds":             messageListSchema("Exact message kinds. Defaults to every word-indexed kind, including raw metadata and tool calls; tool output (tool_result) is not word-indexed, so asking for only it is an error."),
		"from":              map[string]any{"type": "string", "description": "Inclusive message timestamp or UTC date; messages without timestamps are excluded."},
		"to":                map[string]any{"type": "string", "description": "Inclusive message timestamp or UTC date; a date includes the whole day."},
		"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"offset":            map[string]any{"type": "integer", "minimum": 0},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000},
	}},
}

var archiveStatusTool = map[string]any{
	"name":        "get_archive_status",
	"description": "Inspect archive freshness and coverage without starting capture or indexing. Lists known catalog sources across hosts with capture, index and successful sync times, errors, pending counts and stale reasons. Summary status and stale_sources refer to this host; other-host totals are separate. Page items with next_offset. Unknown capture times or coverage are not proof of completeness. domains is the freshness of each data domain on this Mac (conversations per source, captures, the Search view, search indexes, library-wide analyses, findings, the upgrade, backups): state current, behind or unknown, why (reason, e.g. indexing now or deferred by scheduled indexing), and catching_up, the running operation that will bring it current; running lists those operations. unknown means Pharos cannot tell, not that the data is stale.",
	"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
		"source":            map[string]any{"type": "string", "description": "Exact source name."},
		"host_id":           map[string]any{"type": "string"},
		"stale_only":        map[string]any{"type": "boolean", "default": false},
		"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"offset":            map[string]any{"type": "integer", "minimum": 0},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000},
	}},
}

func conversationMessagesTool(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": "Read a bounded conversation window of readable messages by default. Anchored reads with around_message_id include all kinds unless kinds is specified, preserving cited tool evidence. Set kinds to include tool_call, tool_result, metadata, result, or use [all] for every kind; roles further narrows the window. Offsets count filtered messages. Windows return effective kinds and any roles; page forward with offset=next_offset, copying those filters and omitting around_message_id. Keep the conversation_id, limit and output budget unchanged. Explicit filters can exclude an anchor while showing nearby included turns. An explicit message_id reads that exact message regardless of the default kind, and next_text_offset continues long text.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"conversation_id"}, "properties": map[string]any{
			"conversation_id":   map[string]any{"type": "string"},
			"around_message_id": map[string]any{"type": "string"},
			"message_id":        map[string]any{"type": "string"},
			"roles":             messageListSchema("Exact roles to include. Omit for all roles."),
			"kinds":             messageListSchema("Kinds to include, e.g. [message,tool_call,tool_result]. Defaults to [message] for unanchored windows. around_message_id and message_id reads default to all kinds; [all] includes raw metadata and every other kind."),
			"text_offset":       map[string]any{"type": "integer", "minimum": 0},
			"offset":            map[string]any{"type": "integer", "minimum": 0},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 12},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000},
		}},
	}
}

func messageListSchema(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 100}, "minItems": 1, "maxItems": 16, "description": description}
}

func messageFilters(args map[string]any, alias string, proseDefault bool) (string, []any, error) {
	clauses := []string{}
	values := []any{}
	for _, field := range []string{"roles", "kinds"} {
		value := args[field]
		if value == nil {
			if field == "kinds" && proseDefault {
				clauses = append(clauses, alias+"kind='message'")
			}
			continue
		}
		labels := []string{}
		switch list := value.(type) {
		case []string:
			labels = list
		case []any:
			for _, item := range list {
				label, ok := item.(string)
				if !ok {
					return "", nil, fmt.Errorf("%s must be an array of nonempty strings", field)
				}
				labels = append(labels, label)
			}
		default:
			return "", nil, fmt.Errorf("%s must be an array of nonempty strings", field)
		}
		if len(labels) == 0 || len(labels) > 16 {
			return "", nil, fmt.Errorf("%s must contain 1 to 16 strings", field)
		}
		if field == "kinds" && len(labels) == 1 && labels[0] == "all" {
			continue
		}
		placeholders := make([]string, len(labels))
		for index, label := range labels {
			if strings.TrimSpace(label) == "" || len([]rune(label)) > 100 || (field == "kinds" && label == "all") {
				return "", nil, fmt.Errorf("%s must contain strings of 1 to 100 characters; kinds all must be used alone", field)
			}
			placeholders[index] = "?"
			values = append(values, label)
		}
		column := strings.TrimSuffix(field, "s")
		clauses = append(clauses, alias+column+" IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(clauses) == 0 {
		return "1=1", values, nil
	}
	return strings.Join(clauses, " AND "), values, nil
}

var auditWords = regexp.MustCompile(`[\p{L}\p{N}\p{M}_./:-]+`)

func auditQueryWords(query string) []string {
	candidates := auditWords.FindAllString(query, -1)
	tokens := candidates[:0]
	for _, candidate := range candidates {
		if strings.ContainsFunc(candidate, func(character rune) bool {
			return unicode.IsLetter(character) || unicode.IsNumber(character) || unicode.IsMark(character)
		}) {
			tokens = append(tokens, candidate)
		}
	}
	return tokens
}

func auditMessageQuery(args map[string]any) (string, error) {
	query := strings.TrimSpace(firstString(args["query"]))
	mode := defaultString(args["match_mode"], "terms")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if mode != "terms" && mode != "phrase" {
		return "", fmt.Errorf("match_mode must be terms or phrase")
	}
	if strings.Count(query, `"`)%2 != 0 {
		return "", fmt.Errorf("query has an unmatched quote")
	}
	queryWords := auditQueryWords(query)
	if len(queryWords) == 0 {
		return "", fmt.Errorf("query must contain searchable words")
	}
	if mode == "phrase" {
		return libraryPhraseQuery([]string{strings.Join(queryWords, " ")}), nil
	}
	parts := []string{}
	for index, chunk := range strings.Split(query, `"`) {
		tokens := auditQueryWords(chunk)
		if len(tokens) == 0 {
			continue
		}
		if index%2 == 1 {
			tokens = []string{strings.Join(tokens, " ")}
		}
		parts = append(parts, libraryPhraseQuery(tokens))
	}
	return strings.Join(parts, " AND "), nil
}

func fitAuditPage(result map[string]any, budget, offset, total int) error {
	items := result["items"].([]map[string]any)
	if offset+len(items) < total {
		result["next_offset"] = offset + len(items)
	}
	fitConversationItems(result, budget)
	kept := len(result["items"].([]map[string]any))
	if len(items) > 0 && kept == 0 {
		return fmt.Errorf("max_output_tokens is too small for one result; increase the output budget")
	}
	result["next_offset"] = nil
	if offset+kept < total {
		result["next_offset"] = offset + kept
	}
	if len(jsonText(result)) > budget*3 {
		return fmt.Errorf("max_output_tokens is too small for the response; increase the output budget")
	}
	return nil
}

func (c *Catalog) searchMessages(args map[string]any) (map[string]any, error) {
	parsed, err := auditMessageQuery(args)
	if err != nil {
		return nil, err
	}
	filters, values, err := messageFilters(args, "m.", false)
	if err != nil {
		return nil, err
	}
	// Tool output is not word-indexed; a catalog still removing its old
	// postings answers as one that has.
	if unindexedOnly(kindArgument(args["kinds"])) {
		return nil, fmt.Errorf("%s", toolOutputNotIndexed)
	}
	clauses := []string{"messages_fts MATCH ?", filters, wordIndexedKind("m.")}
	values = append([]any{parsed}, values...)
	for _, filter := range []struct{ key, column string }{
		{"conversation_id", "m.conversation_id"}, {"source", "w.source_kind"}, {"provider", "c.provider"},
		{"channel", "(SELECT channel FROM conversation_origins oo WHERE oo.conversation_id=c.id)"}, {"machine_id", "(SELECT machine_id FROM conversation_origins oo WHERE oo.conversation_id=c.id)"}, {"execution_machine_id", "(SELECT execution_machine_id FROM conversation_origins oo WHERE oo.conversation_id=c.id)"},
	} {
		if value := firstString(args[filter.key]); value != "" {
			clauses = append(clauses, filter.column+"=?")
			values = append(values, value)
		}
	}
	if repository := firstString(args["repository"]); repository != "" {
		clauses = append(clauses, repositoryNamePredicate("r"))
		values = append(values, repository, repository)
	}
	for _, key := range []string{"from", "to"} {
		if value := firstString(args[key]); value != "" {
			if _, valid := parseTime(strings.TrimSpace(value)); !valid {
				return nil, fmt.Errorf("%s must be a valid timestamp or date", key)
			}
			operator := ">="
			if key == "to" {
				operator = "<="
			}
			clauses = append(clauses, "julianday(m.created_at)"+operator+"julianday(?)")
			values = append(values, timeBound(value, key == "to"))
		}
	}
	if from, to := firstString(args["from"]), firstString(args["to"]); from != "" && to != "" && timeBound(from, false) > timeBound(to, true) {
		return nil, fmt.Errorf("from must not be later than to")
	}
	base := ` FROM messages_fts JOIN messages m ON m.id=messages_fts.message_id
		JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id
		LEFT JOIN repositories r ON r.id=w.repository_id WHERE ` + strings.Join(clauses, " AND ")
	transaction, err := c.reader().Begin()
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	prefix, originArgs, err := c.originReadSource(context.Background(), transaction)
	if err != nil {
		return nil, err
	}
	values = append(originArgs, values...)
	var total int
	if err := transaction.QueryRow(prefix+"SELECT COUNT(*)"+base, values...).Scan(&total); err != nil {
		return nil, err
	}
	offset := min(total, max(0, int(integer(args["offset"]))))
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	items, err := queryMaps(transaction, prefix+`SELECT m.id message_id,m.conversation_id,c.workspace_id,
		m.role,m.kind,m.created_at,m.source_order,m.evidence_locator,c.coverage,
		substr(w.title,1,101) title,substr(r.display_name,1,101) repository,c.provider,w.source_kind source,
		snippet(messages_fts,1,'','',' … ',28) snippet`+base+`
		ORDER BY COALESCE(julianday(m.created_at),0),m.id LIMIT ? OFFSET ?`, append(values, limit, offset)...)
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	for _, item := range items {
		item["snippet"] = clipText(lexicalSnippet(firstString(item["snippet"])), 240)
	}
	if err := c.attachConversationProvenance(items); err != nil {
		return nil, err
	}
	compactConversationProvenance(items)
	freshness, err := c.archiveSourceStatus()
	if err != nil {
		return nil, err
	}
	result := map[string]any{"items": items, "total": total, "offset": offset, "next_offset": nil,
		"search_scope": "indexed_messages", "search_mode": "lexical", "coverage": toolOutputCoverage,
		"freshness": map[string]any{"status": freshness["status"], "stale_sources": freshness["stale_sources"]}}
	if err := fitAuditPage(result, mcpBudget(args, 1800), offset, total); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Catalog) archiveSourceStatus() (map[string]any, error) {
	host := currentHost()
	rows, err := queryMaps(c.DB, `WITH source_keys AS (
		SELECT host_id,source_name FROM source_states UNION SELECT host_id,source_name FROM automatic_source_states
	) SELECT k.host_id,k.source_name source,COALESCE(h.label,k.host_id) host_label,
		s.kind,s.coverage,s.pending_count,s.last_attempt_at last_index_attempt_at,s.last_success_at last_successful_index_at,s.error index_error,
		a.attempted_at last_sync_attempt_at,a.succeeded_at last_sync_at,a.indexed_at automatic_index_at,a.error sync_error
		FROM source_keys k LEFT JOIN hosts h ON h.id=k.host_id
		LEFT JOIN source_states s ON s.host_id=k.host_id AND s.source_name=k.source_name
		LEFT JOIN automatic_source_states a ON a.host_id=k.host_id AND a.source_name=k.source_name
		ORDER BY k.source_name,k.host_id`)
	if err != nil {
		return nil, err
	}
	localSources, localStale, otherSources, otherStale := 0, 0, 0, 0
	for _, row := range rows {
		indexAt := firstString(row["last_successful_index_at"])
		if automatic := firstString(row["automatic_index_at"]); automatic != "" && canonicalTime(automatic) > canonicalTime(indexAt) {
			indexAt = automatic
			if firstString(row["sync_error"]) == "" && canonicalTime(row["last_sync_at"]) >= canonicalTime(automatic) {
				row["last_successful_index_at"] = automatic
			}
		}
		row["last_index_at"] = nilIfEmpty(indexAt)
		delete(row, "automatic_index_at")
		reasons := []string{}
		row["last_checked_at"] = nil
		if indexed, valid := parseTime(firstString(row["last_successful_index_at"])); valid {
			checkedAt := indexed
			row["last_checked_at"] = row["last_successful_index_at"]
			if synced, valid := parseTime(firstString(row["last_sync_at"])); valid && synced.After(checkedAt) {
				checkedAt = synced
				row["last_checked_at"] = row["last_sync_at"]
			}
			lag := max(0, int64(time.Since(checkedAt).Seconds()))
			row["lag_seconds"] = lag
			if lag > 86400 {
				reasons = append(reasons, "last_index_older_than_24h")
			}
		} else {
			reasons = append(reasons, "no_successful_index")
		}
		if firstString(row["coverage"]) != "complete" {
			reasons = append(reasons, "incomplete_or_unknown_coverage")
		}
		if integer(row["pending_count"]) != 0 {
			reasons = append(reasons, "pending_records")
		}
		for _, field := range []string{"index_error", "sync_error"} {
			if text := firstString(row[field]); text != "" {
				row[field] = clipText(text, 300)
				reasons = append(reasons, field)
			}
		}
		row["stale"] = len(reasons) != 0
		row["stale_reasons"] = reasons
		row["is_current_host"] = row["host_id"] == host.ID
		if row["is_current_host"] == true {
			localSources++
			if row["stale"] == true {
				localStale++
			}
		} else {
			otherSources++
			if row["stale"] == true {
				otherStale++
			}
		}
	}
	status := "stale"
	if localSources > 0 && localStale == 0 {
		status = "current"
	}
	return map[string]any{"items": rows, "status": status, "host_id": host.ID,
		"local_sources": localSources, "stale_sources": localStale,
		"other_host_sources": otherSources, "other_host_stale_sources": otherStale}, nil
}

func (c *Catalog) archiveStatus(args map[string]any) (map[string]any, error) {
	result, err := c.archiveSourceStatus()
	if err != nil {
		return nil, err
	}
	root := c.captureRootPath()
	items := []map[string]any{}
	for _, row := range result["items"].([]map[string]any) {
		if source := firstString(args["source"]); source != "" && source != row["source"] {
			continue
		}
		if hostID := firstString(args["host_id"]); hostID != "" && hostID != row["host_id"] {
			continue
		}
		if args["stale_only"] == true && row["stale"] != true {
			continue
		}
		row["last_capture_at"] = nil
		if root != "" {
			row["last_capture_at"] = nilIfEmpty(lastCaptureAt(root, firstString(row["host_id"]), firstString(row["source"])))
		}
		row["preservation"] = c.inputPreservationStatus(context.Background(), firstString(row["host_id"]), firstString(row["source"]))
		items = append(items, row)
	}
	sort.SliceStable(items, func(left, right int) bool {
		return items[left]["stale"] == true && items[right]["stale"] != true
	})
	total := len(items)
	offset := min(total, max(0, int(integer(args["offset"]))))
	limit := clamp(int(integer(valueOr(args["limit"], 20))), 1, 100)
	result["items"] = items[offset:min(total, offset+limit)]
	result["total"], result["offset"], result["next_offset"] = total, offset, nil
	result["capture_times_available"] = root != ""
	return fitArchiveStatus(c, result, mcpBudget(args, 1800), offset, total)
}

// archiveStatusLedger turns the ledger off, for a test that compares the
// page with and without it.
var archiveStatusLedger = true

// archiveStatusLedgerMinimum is about the smallest ledger worth sending (a
// few behind domains); below it the ledger is not computed.
const archiveStatusLedgerMinimum = 300

// fitArchiveStatus adds the freshness ledger (docs/background-work.md §5)
// to the page: domains, each with its state, reason and the operation
// catching it up, and running, the open operations affecting them. The
// older fields are unchanged: the page is fitted first, as before, and the
// ledger gets only the budget left, dropping the current domains first,
// then itself.
func fitArchiveStatus(c *Catalog, result map[string]any, budget, offset, total int) (map[string]any, error) {
	if !archiveStatusLedger {
		return result, fitAuditPage(result, budget, offset, total)
	}
	// The page first, exactly as without the ledger; the ledger only takes
	// what budget is left, and is not computed when nothing would fit.
	if err := fitAuditPage(result, budget, offset, total); err != nil {
		return nil, err
	}
	omitted := func() (map[string]any, error) {
		page := maps.Clone(result)
		page["domains_omitted"] = "increase max_output_tokens to see the freshness domains"
		if len(jsonText(page)) <= budget*3 {
			return page, nil
		}
		return result, nil
	}
	if len(jsonText(result))+archiveStatusLedgerMinimum > budget*3 {
		return omitted()
	}
	rows, ops := c.mcpFreshness(context.Background())
	domains, behind := []map[string]any{}, []map[string]any{}
	for _, row := range rows {
		domain := map[string]any{"domain": row.Domain, "state": row.Behind.State}
		for key, value := range map[string]*string{"as_of": row.AsOf, "unit": row.Behind.Unit, "catching_up": row.CatchingUp, "next": row.Next} {
			if value != nil {
				domain[key] = *value
			}
		}
		if row.Behind.Count != nil {
			domain["count"] = *row.Behind.Count
		}
		if row.Reason != nil {
			domain["reason"], domain["reason_text"] = row.Reason.Code, row.Reason.Text
		}
		domains = append(domains, domain)
		if row.Behind.State != freshCurrent {
			behind = append(behind, domain)
		}
	}
	running := []map[string]any{}
	for _, op := range ops {
		affected := []string{}
		for _, row := range rows {
			if slices.ContainsFunc(op.Affects, func(pattern string) bool { return affectsDomain(pattern, op.Subject, currentHost().ID, row.Domain) }) {
				affected = append(affected, row.Domain)
			}
		}
		if len(affected) > 0 {
			running = append(running, map[string]any{"id": op.ID, "kind": op.Kind, "label": op.Label, "state": op.State, "started_at": formatTime(op.Started), "domains": affected})
		}
	}
	for _, attempt := range []func(map[string]any){
		func(page map[string]any) { page["domains"], page["running"] = domains, running },
		func(page map[string]any) {
			page["domains"], page["running"], page["domains_partial"] = behind, running, true
		},
	} {
		page := maps.Clone(result)
		attempt(page)
		if len(jsonText(page)) <= budget*3 {
			return page, nil
		}
	}
	return omitted()
}
