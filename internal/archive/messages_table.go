package archive

import (
	"context"
	"unicode/utf8"
)

// messagesDataset is one row per retained canonical message. Authorship is
// optional: assistant, thinking, tool and unclassified user messages remain in
// the population with NULL authorship measures.
var messagesDataset = func() sqlDataset {
	columns := map[string]string{
		"id": "m.id", "native_id": "m.native_id", "conversation_id": "m.conversation_id",
		"workspace_id": "c.workspace_id", "conversation_native_id": "c.native_id",
		"workspace_source_id": "w.source_id", "source_account": "w.source_account",
		"role": "m.role", "kind": "m.kind", "sender": "m.sender", "selected": "m.selected",
		"source_order": "m.source_order", "call_id": "m.call_id",
		"parent_native_id": "m.parent_native_id", "previous_native_id": "m.previous_native_id",
		"evidence_locator": "m.evidence_locator", "reasoning_format": "m.reasoning_format",
		"model": "NULLIF(m.model,'')", "effort": "NULLIF(m.effort,'')",
		"sent_at": "m.created_at", "day": "date(m.created_at,'localtime')",
		"week":               "date(m.created_at,'localtime','-6 days','weekday 1')",
		"month":              "strftime('%Y-%m',m.created_at,'localtime')",
		"authorship_sent_at": "ma.sent_at", "authorship_day": "ma.day",
		"authorship_week": "date(ma.day,'-6 days','weekday 1')", "authorship_month": "substr(ma.day,1,7)",
		"text": "m.text", "message_count": "1",
		"title": "w.title", "repository_name": "r.display_name", "project_name": projectNameSQL("w"), "source_kind": "w.source_kind",
		"authorship_title": "aw.title", "authorship_repository_name": "ar.display_name", "authorship_source_kind": "aw.source_kind",
		"authorship_project_name": "COALESCE(apj.display_name,aw.project_folder)",
		"provider":                "c.provider", "subagent": "(c.parent_id IS NOT NULL)",
		"has_authorship": "(ma.message_id IS NOT NULL)",
		"work_id":        "COALESCE(ma.work_id,ma.workspace_id)",
		"chars":          "ma.total_chars", "words": "ma.words",
		"typed_message": "CASE WHEN ma.message_id IS NOT NULL THEN ma.typed_words>0 END",
		"copied_words":  "CASE WHEN ma.message_id IS NOT NULL THEN ma.quoted_words+ma.resent_words END",
		"prompt_words":  "CASE WHEN ma.message_id IS NOT NULL THEN ma.template_words+ma.attachment_words END",
		"machine_words": "CASE WHEN ma.message_id IS NOT NULL THEN ma.harness_words+ma.automated_words END",
		"other_words":   "CASE WHEN ma.message_id IS NOT NULL THEN ma.words-ma.typed_words END",
		"typed_share":   "CASE WHEN ma.words>0 THEN round(ma.typed_words*100.0/ma.words,1) END",
		"main_category": "ma.main_category", "categories": "ma.categories", "rules": "ma.rules",
		"has_source":     "CASE WHEN ma.message_id IS NOT NULL THEN EXISTS(SELECT 1 FROM message_authorship_spans s WHERE s.message_id=m.id AND s.source_conversation_id IS NOT NULL) END",
		"correction_cue": "CASE WHEN ma.message_id IS NOT NULL THEN " + authoredCueSQL(correctionCuePhrases) + " END",
		"positive_cue":   "CASE WHEN ma.message_id IS NOT NULL THEN " + authoredCueSQL(positiveCuePhrases) + " END",
		"channel":        "o.channel", "machine_id": "o.machine_id", "machine": "o.machine",
		"machine_kind": "o.machine_kind", "execution_machine_id": "o.execution_machine_id",
		"execution_machine": "o.execution_machine", "execution_machine_kind": "o.execution_machine_kind",
		"channel_status": "o.channel_status", "machine_status": "o.machine_status",
		"execution_machine_status": "o.execution_machine_status",
		"origin_conversation_id":   "o.origin_conversation_id", "origin_evidence": "o.origin_evidence",
		"channels": "COALESCE(o.channel,'Unknown')", "machines": "COALESCE(o.machine_id,'Unknown')",
		"execution_machines": "COALESCE(o.execution_machine_id,'Unknown')",
	}
	for _, category := range authorshipCategories {
		columns[category+"_words"] = "ma." + category + "_words"
	}
	return sqlDataset{
		// One space before each optional join: project removes them by their text.
		from: "FROM messages m JOIN conversations c ON c.id=m.conversation_id" +
			" JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id" +
			" LEFT JOIN message_authorship ma ON ma.message_id=m.id" +
			" LEFT JOIN workspaces aw ON aw.id=COALESCE(ma.work_id,ma.workspace_id)" +
			" LEFT JOIN repositories ar ON ar.id=aw.repository_id" + projectJoin("w") + authorshipProjectJoin +
			" LEFT JOIN conversation_origins o ON o.conversation_id=m.conversation_id",
		base:       "c.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)",
		columns:    columns,
		lookups:    map[string]bool{"id": true, "conversation_id": true, "workspace_id": true},
		timestamps: map[string]string{"sent_at": "m.created_at", "authorship_sent_at": "ma.sent_at"},
		buckets:    map[string]string{"day": "m.created_at", "week": "m.created_at", "month": "m.created_at"},
		optionalJoins: map[string]string{
			"r":   " LEFT JOIN repositories r ON r.id=w.repository_id",
			"ma":  " LEFT JOIN message_authorship ma ON ma.message_id=m.id",
			"aw":  " LEFT JOIN workspaces aw ON aw.id=COALESCE(ma.work_id,ma.workspace_id)",
			"ar":  " LEFT JOIN repositories ar ON ar.id=aw.repository_id",
			"pj":  projectJoin("w"),
			"apj": authorshipProjectJoin,
			"o":   " LEFT JOIN conversation_origins o ON o.conversation_id=m.conversation_id",
		},
		pageFrom:    "FROM messages m",
		newestFirst: map[string]string{"sent_at": "messages_page_idx"},
	}
}()

// attachMessageText keeps row and item responses within the same byte budget
// as writing_messages. The full stored text remains available to SQL filters.
func (c *Catalog) attachMessageText(ctx context.Context, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	missing := make([]any, 0)
	for _, row := range rows {
		if _, ok := row["has_authorship"]; !ok {
			missing = append(missing, row["id"])
		}
	}
	if len(missing) > 0 {
		found, err := queryMapsContext(ctx, c.reader(), `SELECT message_id FROM message_authorship WHERE message_id IN (`+placeholders(len(missing))+`)`, missing...)
		if err != nil {
			return err
		}
		classified := map[string]bool{}
		for _, row := range found {
			classified[firstString(row["message_id"])] = true
		}
		for _, row := range rows {
			if _, ok := row["has_authorship"]; !ok {
				row["has_authorship"] = classified[firstString(row["id"])]
			}
		}
	}
	classified := make([]map[string]any, 0, len(rows))
	original := map[string]map[string]bool{}
	for _, row := range rows {
		if messageBool(row["has_authorship"]) {
			present := map[string]bool{}
			for _, field := range []string{"text", "categories", "rules", "subagent", "typed_message", "has_source", "correction_cue", "positive_cue"} {
				_, present[field] = row[field]
			}
			original[firstString(row["id"])] = present
			// The shared attachment accepts SQLite integer flags. V3 can already
			// have decoded them as booleans before enrichment.
			for _, field := range []string{"subagent", "typed_message", "has_source", "correction_cue", "positive_cue"} {
				if flag, ok := row[field].(bool); ok {
					if flag {
						row[field] = int64(1)
					} else {
						row[field] = int64(0)
					}
				}
			}
			classified = append(classified, row)
			continue
		}
		if raw, ok := row["text"]; ok {
			body, _ := raw.(string)
			end := min(len(body), authoredTextLimit)
			for end > 0 && end < len(body) && !utf8.RuneStart(body[end]) {
				end--
			}
			row["text"] = body[:end]
			row["text_truncated"] = end < len(body)
			segments := []map[string]any{}
			if end > 0 {
				segments = append(segments, map[string]any{"text": body[:end]})
			}
			row["segments"] = segments
		}
		for _, field := range []string{"typed_message", "has_source", "correction_cue", "positive_cue", "categories", "rules"} {
			if _, ok := row[field]; ok {
				row[field] = nil
			}
		}
		row["has_authorship"] = false
		if value, ok := row["subagent"]; ok {
			row["subagent"] = messageBool(value)
		}
		if value, ok := row["selected"]; ok {
			row["selected"] = messageBool(value)
		}
	}
	if err := c.attachAuthoredText(ctx, classified); err != nil {
		return err
	}
	for _, row := range classified {
		row["has_authorship"] = true
		if value, ok := row["selected"]; ok {
			row["selected"] = messageBool(value)
		}
		present := original[firstString(row["id"])]
		if !present["text"] {
			delete(row, "text")
			delete(row, "segments")
			delete(row, "text_truncated")
		}
		for _, field := range []string{"categories", "rules", "subagent", "typed_message", "has_source", "correction_cue", "positive_cue"} {
			if !present[field] {
				delete(row, field)
			}
		}
	}
	return nil
}

func messageBool(value any) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return integer(value) != 0
}

// authorshipProjectJoin finds the project of the workspace a message counts
// toward in Writing, as projectJoin does for its own workspace.
const authorshipProjectJoin = " LEFT JOIN repositories apj ON apj.id=COALESCE(aw.repository_id,aw.folder_repository_id)"
