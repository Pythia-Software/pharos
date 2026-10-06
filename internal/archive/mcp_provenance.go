package archive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var workDetailSections = map[string]string{
	"identity":               `SELECT w.*,r.display_name repository_name,r.canonical_remote FROM workspaces w LEFT JOIN repositories r ON r.id=w.repository_id WHERE w.id=?`,
	"conversations":          `SELECT c.*,(SELECT COUNT(*) FROM messages m WHERE m.conversation_id=c.id) message_count FROM conversations c WHERE c.workspace_id=? ORDER BY c.started_at,c.id`,
	"lineage":                `SELECT c.id conversation_id,c.native_id,c.parent_id,c.agent_depth,c.agent_path,c.agent_nickname,c.origin,p.native_id parent_native_id,p.workspace_id parent_workspace_id FROM conversations c LEFT JOIN conversations p ON p.id=c.parent_id WHERE c.workspace_id=? ORDER BY c.agent_depth,c.started_at,c.id`,
	"agent_sessions":         `SELECT a.* FROM agent_sessions a WHERE a.workspace_id=? ORDER BY a.depth,a.started_at,a.id`,
	"changes":                `SELECT cs.*,cf.path,cf.old_path,cf.status,cf.tracked,cf.bytes,cf.evidence_locator FROM change_sets cs LEFT JOIN change_files cf ON cf.change_set_id=cs.id WHERE cs.workspace_id=? ORDER BY cs.id,cf.path`,
	"outcomes":               `SELECT * FROM summaries WHERE workspace_id=?`,
	"receipts":               `SELECT * FROM receipts WHERE workspace_id=? ORDER BY created_at DESC,id`,
	"metrics":                `SELECT * FROM metrics WHERE workspace_id=? ORDER BY name,id`,
	"metric_ledger":          `SELECT * FROM metric_ledger WHERE workspace_id=? ORDER BY sequence`,
	"attempts":               `SELECT a.* FROM task_attempts a JOIN work_items i ON i.id=a.work_item_id WHERE i.workspace_id=? ORDER BY a.attempt_no,a.started_at,a.id`,
	"handoffs":               `SELECT * FROM handoffs WHERE workspace_id=? ORDER BY created_at,id`,
	"prs":                    `SELECT pr.*,l.relationship,l.confidence,l.evidence_json FROM work_pr_links l JOIN pull_requests pr ON pr.id=l.pr_id WHERE l.workspace_id=? ORDER BY pr.id`,
	"sightings":              `SELECT * FROM workspace_sightings WHERE workspace_id=? ORDER BY host_id,source_name`,
	"conversation_sightings": `SELECT s.* FROM conversation_sightings s JOIN conversations c ON c.id=s.conversation_id WHERE c.workspace_id=? ORDER BY s.conversation_id,s.host_id,s.origin`,
	"identity_links":         `SELECT l.* FROM conversation_identity_links l WHERE l.left_id IN (SELECT id FROM conversations WHERE workspace_id=?) OR l.right_id IN (SELECT id FROM conversations WHERE workspace_id=?) ORDER BY l.left_id,l.right_id`,
}

func workDetailTool() map[string]any {
	sections := workSectionNames()
	return map[string]any{"name": "get_work_detail", "description": "Read one bounded workspace section at a time. Defaults to identity. Page each section with next_offset; truncated_fields names long fields that can be read losslessly with field, the item's offset, and text_offset. Conversations and agent sessions carry structured parent lineage. Does not read transcripts or inspect live Git state.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"workspace_id"}, "properties": map[string]any{
			"workspace_id":      map[string]any{"type": "string"},
			"section":           map[string]any{"type": "string", "enum": sections, "default": "identity"},
			"field":             map[string]any{"type": "string", "description": "Read a long string field from the single row at offset. Use next_text_offset to continue."},
			"text_offset":       map[string]any{"type": "integer", "minimum": 0},
			"offset":            map[string]any{"type": "integer", "minimum": 0},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000},
		}}}
}

func workSectionNames() []string {
	names := make([]string, 0, len(workDetailSections))
	for name := range workDetailSections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) boundedWorkDetail(args map[string]any) (map[string]any, error) {
	id := firstString(args["workspace_id"])
	var exists int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM workspaces WHERE id=?", id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, fmt.Errorf("workspace not found")
	}
	section := firstString(args["section"], "identity")
	query, valid := workDetailSections[section]
	if !valid {
		return nil, fmt.Errorf("unknown section; use one of %s", strings.Join(workSectionNames(), ", "))
	}
	values := []any{id}
	if section == "identity_links" {
		values = append(values, id)
	}
	transaction, err := c.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	var total int
	if err := transaction.QueryRow("SELECT COUNT(*) FROM ("+query+")", values...).Scan(&total); err != nil {
		return nil, err
	}
	offset := min(total, max(0, int(integer(args["offset"]))))
	limit := clamp(int(integer(valueOr(args["limit"], 10))), 1, 100)
	budget := mcpBudget(args, 1800)
	field := firstString(args["field"])
	fieldOffset := max(0, int(integer(args["text_offset"])))
	pageQuery := query + " LIMIT ? OFFSET ?"
	pageValues := append(append([]any{}, values...), limit, offset)
	if field != "" {
		columnsQuery, err := transaction.Query(query+" LIMIT 0", values...)
		if err != nil {
			return nil, err
		}
		columns, err := columnsQuery.Columns()
		columnsQuery.Close()
		if err != nil {
			return nil, err
		}
		validField := false
		for _, column := range columns {
			if column == field {
				validField = true
			}
		}
		if !validField {
			return nil, fmt.Errorf("field must name a string field on the selected item")
		}
		limit = 1
		quotedField := `"` + strings.ReplaceAll(field, `"`, `""`) + `"`
		pageQuery = "SELECT substr(" + quotedField + ",?,?) text,length(" + quotedField + ") text_length,typeof(" + quotedField + ") field_type FROM (" + query + ") LIMIT ? OFFSET ?"
		pageValues = append(append([]any{fieldOffset + 1, budget}, values...), limit, offset)
	}
	items, err := queryMaps(transaction, pageQuery, pageValues...)
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	result := map[string]any{"workspace_id": id, "section": section, "available_sections": workSectionNames(), "items": items, "total": total, "offset": offset, "next_offset": nil}
	if field != "" {
		if len(items) == 0 {
			return nil, fmt.Errorf("no item at offset")
		}
		if items[0]["field_type"] != "text" {
			return nil, fmt.Errorf("field must name a string field on the selected item")
		}
		characters := []rune(asString(items[0]["text"]))
		textLength := int(integer(items[0]["text_length"]))
		start := min(textLength, fieldOffset)
		end := start + len(characters)
		result["items"] = []map[string]any{}
		result["field"], result["text_offset"], result["text_length"] = field, start, textLength
		result["text"], result["next_text_offset"] = string(characters), nil
		if end < textLength {
			result["next_text_offset"] = end
		}
		for len(jsonText(result)) > budget*3 && end > start {
			end = start + (end-start)/2
			result["text"], result["next_text_offset"] = string(characters[:end-start]), end
		}
		if end == start && start < textLength || len(jsonText(result)) > budget*3 {
			return nil, fmt.Errorf("max_output_tokens is too small for a field chunk; increase the output budget")
		}
		return result, nil
	}
	for index, item := range items {
		item["item_offset"] = offset + index
		truncated := map[string]any{}
		for field, value := range item {
			if text, valid := value.(string); valid && len([]rune(text)) > 240 {
				item[field] = string([]rune(text)[:240])
				truncated[field] = map[string]any{"text_length": len([]rune(text)), "next_text_offset": 240}
			}
		}
		if len(truncated) > 0 {
			item["truncated_fields"] = truncated
		}
		if section == "conversations" {
			item["conversation_id"] = item["id"]
			item["message_access"] = map[string]any{"tool": "get_conversation_messages", "conversation_id": item["id"]}
		}
	}
	if section == "conversations" {
		if err := c.attachConversationProvenance(items); err != nil {
			return nil, err
		}
	}
	if err := fitAuditPage(result, budget, offset, total); err != nil {
		return nil, err
	}
	return result, nil
}

var traceWorktreeTool = map[string]any{
	"name": "trace_worktree", "description": "Read-only lookup of a worktree path, basename, branch, or agent ID. Prioritizes exact recorded metadata; only if none matches, searches indexed transcript phrases with a case-sensitive literal check, never semantic fallback or a full archive text scan. Use search_messages for additional transcript references to a known entity. Returns parent lineage, assignment, activity and evidence handles. References do not establish creation, current Git state, integration, or safe deletion. Page with next_offset.",
	"inputSchema": map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{
		"query":             map[string]any{"type": "string", "minLength": 1, "description": "Exact absolute path, worktree basename, branch name, or native agent ID; case-sensitive literal matching."},
		"repository":        map[string]any{"type": "string"},
		"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"offset":            map[string]any{"type": "integer", "minimum": 0},
		"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000},
	}},
}

var reportedCommitPattern = regexp.MustCompile("(?i)\\bcommit(?:\\s+(?:hash|sha|id))?\\s*[:=]?\\s*[`'\"]?([0-9a-f]{7,40})\\b")
var reportedBranchPattern = regexp.MustCompile("(?i)\\bbranch(?:\\s+name)?\\s*[:=]\\s*[`'\"]?([a-z0-9][a-z0-9._/-]{0,150})")

func reportedGitReferences(messages []map[string]any) map[string]any {
	result := map[string]any{}
	for name, pattern := range map[string]*regexp.Regexp{"branches": reportedBranchPattern, "commits": reportedCommitPattern} {
		references := []map[string]any{}
		seen := map[string]bool{}
		for _, message := range messages {
			for _, match := range pattern.FindAllStringSubmatch(firstString(message["text"]), 8) {
				key := match[1] + "\x00" + firstString(message["message_id"])
				if seen[key] || len(references) >= 8 {
					continue
				}
				seen[key] = true
				references = append(references, map[string]any{"value": match[1], "message_id": message["message_id"]})
			}
		}
		result[name] = references
	}
	result["note"] = "Extracted from explicit commit/branch labels in cited messages, not verified Git state; read full messages for context and other references."
	return result
}

func (c *Catalog) traceWorktree(args map[string]any) (map[string]any, error) {
	query := strings.TrimSpace(firstString(args["query"]))
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	joins := ` FROM workspaces w LEFT JOIN conversations c ON w.id=c.workspace_id
		LEFT JOIN repositories r ON r.id=w.repository_id`
	base := joins + ` WHERE (
		c.native_id=? OR c.agent_path=? OR w.branch=? OR w.location=? OR c.origin=? OR w.source_id=?
		OR EXISTS(SELECT 1 FROM json_each(c.aliases_json) WHERE value=?)
		OR substr(w.location,-length(?)-1)='/'||? OR instr(c.origin,'/'||?||'.jsonl')>0
		OR EXISTS(SELECT 1 FROM conversation_sightings s WHERE s.conversation_id=c.id AND (s.origin=? OR instr(s.origin,'/'||?||'.jsonl')>0))
		OR EXISTS(SELECT 1 FROM workspace_sightings s WHERE s.workspace_id=w.id AND (s.location=? OR substr(s.location,-length(?)-1)='/'||?)))`
	values := make([]any, 15)
	for index := range values {
		values[index] = query
	}
	if repository := firstString(args["repository"]); repository != "" {
		base += " AND (r.display_name LIKE ? OR r.canonical_remote LIKE ?)"
		values = append(values, "%"+repository+"%", "%"+repository+"%")
	}
	transaction, err := c.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	var total int
	if err := transaction.QueryRow("SELECT COUNT(*)"+base, values...).Scan(&total); err != nil {
		return nil, err
	}
	scope := "entity_metadata"
	if total == 0 {
		parsed, err := auditMessageQuery(map[string]any{"query": query, "match_mode": "phrase"})
		if err != nil {
			return nil, err
		}
		base = joins + ` WHERE c.id IN (SELECT DISTINCT m.conversation_id FROM messages_fts
			JOIN messages m ON m.id=messages_fts.message_id WHERE messages_fts MATCH ? AND instr(m.text,?)>0)`
		values = []any{parsed, query}
		if repository := firstString(args["repository"]); repository != "" {
			base += " AND (r.display_name LIKE ? OR r.canonical_remote LIKE ?)"
			values = append(values, "%"+repository+"%", "%"+repository+"%")
		}
		if err := transaction.QueryRow("SELECT COUNT(*)"+base, values...).Scan(&total); err != nil {
			return nil, err
		}
		scope = "indexed_message_phrases"
	}
	offset := min(total, max(0, int(integer(args["offset"]))))
	limit := clamp(int(integer(valueOr(args["limit"], 5))), 1, 100)
	items, err := queryMaps(transaction, `SELECT c.id conversation_id,w.id workspace_id,c.native_id agent_id,c.provider,c.coverage,
		c.started_at,c.ended_at,w.indexed_at,w.branch recorded_branch,w.head_ref recorded_head,substr(w.location,1,500) recorded_location,
		substr(w.title,1,121) title,r.display_name repository`+base+` ORDER BY c.started_at,c.id LIMIT ? OFFSET ?`, append(values, limit, offset)...)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		evidence, err := queryMaps(transaction, `SELECT id message_id,created_at,role,kind,evidence_locator,text,
			substr(text,max(1,instr(text,?)-100),400) snippet FROM messages WHERE conversation_id=? AND instr(text,?)>0
			ORDER BY source_order IS NULL,source_order,created_at,id LIMIT 3`, query, item["conversation_id"], query)
		if err != nil {
			return nil, err
		}
		reports, err := queryMaps(transaction, `SELECT id message_id,text FROM messages WHERE conversation_id=? AND +role='assistant' AND kind='message'
			ORDER BY source_order IS NULL,source_order DESC,created_at DESC,id DESC LIMIT 1`, item["conversation_id"])
		if err != nil {
			return nil, err
		}
		item["reported_git_references"] = reportedGitReferences(append(append([]map[string]any{}, evidence...), reports...))
		for _, message := range evidence {
			delete(message, "text")
		}
		if len(reports) > 0 {
			item["report_message_id"] = reports[0]["message_id"]
		}
		item["evidence"], item["relationship"] = evidence, "recorded_reference"
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	metadataOnly := []map[string]any{}
	for _, item := range items {
		if item["conversation_id"] == nil {
			metadataOnly = append(metadataOnly, item)
		}
	}
	if err := c.attachWorkspaceFreshness(metadataOnly); err != nil {
		return nil, err
	}
	if err := c.attachConversationProvenance(items); err != nil {
		return nil, err
	}
	result := map[string]any{"query": query, "search_mode": "literal", "lookup_strategy": "metadata_first", "search_scope": scope, "items": items, "total": total, "offset": offset, "next_offset": nil,
		"limitations": "Recorded references only, not proof of creation or present use. Branches, heads and outcomes are historical reports, not verified integration or permission to delete. Missing evidence is not proof of absence."}
	if scope == "entity_metadata" {
		references := map[string]any{"tool": "search_messages", "query": query, "match_mode": "phrase"}
		if repository := firstString(args["repository"]); repository != "" {
			references["repository"] = repository
		}
		result["reference_search_access"] = references
	}
	if err := fitAuditPage(result, mcpBudget(args, 2400), offset, total); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Catalog) attachConversationProvenance(items []map[string]any) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]any, len(items))
	for index, item := range items {
		ids[index] = item["conversation_id"]
	}
	rows, err := queryMaps(c.DB, `SELECT c.id conversation_id,c.native_id,c.parent_id,c.agent_depth,c.agent_path,c.agent_nickname,
		c.origin,c.origin_host_id,c.coverage,c.workspace_id,p.native_id parent_native_id,p.workspace_id parent_workspace_id,
		w.source_kind,w.source_id,w.indexed_at workspace_indexed_at,d.indexed_at conversation_indexed_at,
		COALESCE(pw.source_id,w.source_id) originating_source_id,substr(COALESCE(pw.title,w.title),1,121) originating_title,
		substr(COALESCE(pw.location,w.location),1,300) originating_location,
		COALESCE(d.initiation_message_id,(SELECT id FROM messages WHERE conversation_id=c.id AND +role='user' AND kind='message' ORDER BY source_order IS NULL,source_order,created_at,id LIMIT 1)) assignment_message_id,
		substr(COALESCE(d.initiation,(SELECT text FROM messages WHERE conversation_id=c.id AND +role='user' AND kind='message' ORDER BY source_order IS NULL,source_order,created_at,id LIMIT 1)),1,240) assignment,
		substr(d.outcome,1,240) reported_outcome,d.outcome_message_id,
		(SELECT MIN(created_at) FROM messages WHERE conversation_id=c.id) first_message_at,
		(SELECT MAX(created_at) FROM messages WHERE conversation_id=c.id) last_message_at
		FROM conversations c JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN conversations p ON p.id=c.parent_id
		LEFT JOIN workspaces pw ON pw.id=p.workspace_id
		LEFT JOIN conversation_documents d ON d.conversation_id=c.id WHERE c.id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return err
	}
	sightings, err := queryMaps(c.DB, `SELECT conversation_id,host_id,source_name,last_seen_at FROM conversation_sightings
		WHERE conversation_id IN (`+placeholders(len(ids))+`) ORDER BY conversation_id,host_id,source_name,last_seen_at DESC`, ids...)
	if err != nil {
		return err
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[firstString(row["conversation_id"])] = row
	}
	for _, item := range items {
		row := byID[firstString(item["conversation_id"])]
		if row == nil {
			continue
		}
		item["lineage"] = map[string]any{"agent_id": row["native_id"], "parent_conversation_id": row["parent_id"], "parent_native_id": row["parent_native_id"],
			"parent_workspace_id": row["parent_workspace_id"], "workspace_id": row["workspace_id"], "originating_workspace": row["originating_source_id"],
			"originating_title": row["originating_title"], "originating_location": row["originating_location"],
			"depth": row["agent_depth"], "agent_path": clipText(firstString(row["agent_path"]), 300), "origin": clipText(firstString(row["origin"]), 500),
			"assignment": row["assignment"], "assignment_message_id": row["assignment_message_id"]}
		item["recorded_activity"] = map[string]any{"first_message_at": row["first_message_at"], "last_message_at": row["last_message_at"]}
		item["reported_outcome"] = map[string]any{"text": row["reported_outcome"], "message_id": row["outcome_message_id"]}
		item["freshness"] = map[string]any{"conversation_indexed_at": row["conversation_indexed_at"], "workspace_indexed_at": row["workspace_indexed_at"], "coverage": row["coverage"]}
	}
	return c.attachSourceFreshness(items, sightings, "conversation_id")
}

func (c *Catalog) attachWorkspaceFreshness(items []map[string]any) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]any, len(items))
	for index, item := range items {
		ids[index] = firstString(item["workspace_id"], item["id"])
		item["workspace_id"] = ids[index]
		item["freshness"] = map[string]any{"workspace_indexed_at": item["indexed_at"]}
	}
	sightings, err := queryMaps(c.DB, `SELECT workspace_id,host_id,source_name,last_seen_at FROM workspace_sightings
		WHERE workspace_id IN (`+placeholders(len(ids))+`) ORDER BY workspace_id,host_id,source_name,last_seen_at DESC`, ids...)
	if err != nil {
		return err
	}
	return c.attachSourceFreshness(items, sightings, "workspace_id")
}

func (c *Catalog) attachSourceFreshness(items, sightings []map[string]any, identityField string) error {
	status, err := c.archiveSourceStatus()
	if err != nil {
		return err
	}
	sources := map[string]map[string]any{}
	for _, source := range status["items"].([]map[string]any) {
		sources[firstString(source["host_id"])+"\x00"+firstString(source["source"])] = source
	}
	captures := map[string]any{}
	captureRoot := c.captureRootPath()
	for _, item := range items {
		if item[identityField] == nil {
			continue
		}
		provenance := []map[string]any{}
		seen := map[string]bool{}
		for _, sighting := range sightings {
			if sighting[identityField] != item[identityField] {
				continue
			}
			key := firstString(sighting["host_id"]) + "\x00" + firstString(sighting["source_name"])
			if seen[key] {
				continue
			}
			seen[key] = true
			entry := map[string]any{"host_id": sighting["host_id"], "source": sighting["source_name"], "last_observed_at": sighting["last_seen_at"], "status": "unknown", "evidence_capture_at": nil}
			if _, found := captures[key]; !found {
				captures[key] = nil
				if captureRoot != "" {
					captures[key] = nilIfEmpty(lastCaptureAt(captureRoot, firstString(sighting["host_id"]), firstString(sighting["source_name"])))
				}
			}
			entry["source_last_capture_at"] = captures[key]
			if source := sources[key]; source != nil {
				entry["status"] = "current"
				if source["stale"] == true {
					entry["status"] = "stale"
				}
				for _, field := range []string{"last_sync_at", "last_sync_attempt_at", "last_index_at", "last_successful_index_at", "last_checked_at", "stale_reasons"} {
					entry[field] = source[field]
				}
			}
			provenance = append(provenance, entry)
		}
		freshStatus := "unknown"
		if len(provenance) > 0 {
			freshStatus = "current"
			for _, entry := range provenance {
				if entry["status"] == "stale" {
					freshStatus = "stale"
					break
				}
				if entry["status"] == "unknown" {
					freshStatus = "unknown"
				}
			}
		}
		freshness, valid := item["freshness"].(map[string]any)
		if !valid {
			freshness = map[string]any{}
			item["freshness"] = freshness
		}
		freshness["status"], freshness["sources"] = freshStatus, provenance
		freshness["source_count"] = len(provenance)
		if len(provenance) > 4 {
			sort.SliceStable(provenance, func(left, right int) bool {
				return provenance[left]["status"] == "stale" && provenance[right]["status"] != "stale"
			})
			freshness["sources"] = provenance[:4]
			freshness["sources_truncated"] = true
			section := "sightings"
			if identityField == "conversation_id" {
				section = "conversation_sightings"
			}
			freshness["source_attribution_access"] = map[string]any{"tool": "get_work_detail", "workspace_id": item["workspace_id"], "section": section}
		}
	}
	return nil
}

func compactConversationProvenance(items []map[string]any) {
	for _, item := range items {
		delete(item, "reported_outcome")
		delete(item, "recorded_activity")
		if lineage, valid := item["lineage"].(map[string]any); valid {
			for _, field := range []string{"assignment", "assignment_message_id", "origin", "agent_path", "workspace_id", "originating_title", "originating_location"} {
				delete(lineage, field)
			}
			for field, value := range lineage {
				if value == nil {
					delete(lineage, field)
				}
			}
		}
	}
}

func fitConversationCards(result map[string]any, budget int) {
	items := result["items"].([]map[string]any)
	if len(items) > 0 {
		probe := map[string]any{"items": items[:1], "total": result["total"], "next_offset": result["next_offset"], "freshness": result["freshness"]}
		if len(jsonText(probe)) > budget*3 {
			for _, item := range items {
				freshness := item["freshness"].(map[string]any)
				item["freshness"] = map[string]any{"status": freshness["status"], "details_tool": "get_conversation_overview"}
				for _, field := range []string{"lineage", "activity_at", "started_at", "ended_at", "indexed_at", "workspace_id", "provider", "model", "coverage", "message_count", "score", "relevance_reason"} {
					delete(item, field)
				}
				item["snippet"] = clipText(firstString(item["snippet"]), 60)
				item["title"] = clipText(firstString(item["title"]), 40)
			}
		}
	}
	fitConversationItems(result, budget)
}
