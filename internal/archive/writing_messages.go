package archive

import (
	"context"
	"encoding/json"
	"unicode/utf8"
)

// The Human Words message view lists one row per classified user message (see
// authorship.go), filtered, sorted, and paged in SQLite. Each page row also
// carries its text split into the spans the classification labelled, so the
// page can highlight what counted as what, and why.

// writingMessageDataset serves one row per user message in message_authorship.
// Title, repository, and source are those of the Library work the message
// counts toward, as in the per-conversation writing table.
var writingMessageDataset = func() sqlDataset {
	columns := map[string]string{
		"id": "ma.message_id", "conversation_id": "ma.conversation_id", "workspace_id": "ma.workspace_id",
		"work_id": "COALESCE(ma.work_id,ma.workspace_id)",
		"title":   "w.title", "repository_name": "r.display_name", "source_kind": "w.source_kind",
		"provider": "c.provider", "subagent": "(c.parent_id IS NOT NULL)", "sender": "m.sender",
		"sent_at": "ma.sent_at", "day": "ma.day", "week": "date(ma.day,'-6 days','weekday 1')", "month": "substr(ma.day,1,7)",
		"text": "m.text", "chars": "ma.total_chars", "words": "ma.words", "message_count": "1",
		"typed_message": "(ma.typed_words>0)",
		"copied_words":  "(ma.quoted_words+ma.resent_words)",
		"prompt_words":  "(ma.template_words+ma.attachment_words)",
		"machine_words": "(ma.harness_words+ma.automated_words)",
		"other_words":   "(ma.words-ma.typed_words)",
		"typed_share":   "CASE WHEN ma.words>0 THEN round(ma.typed_words*100.0/ma.words,1) END",
		"main_category": "ma.main_category", "categories": "ma.categories", "rules": "ma.rules",
		"has_source": "EXISTS(SELECT 1 FROM message_authorship_spans s WHERE s.message_id=ma.message_id AND s.source_conversation_id IS NOT NULL)",
	}
	for _, category := range authorshipCategories {
		columns[category+"_words"] = "ma." + category + "_words"
	}
	return sqlDataset{
		from: `FROM message_authorship ma JOIN messages m ON m.id=ma.message_id JOIN conversations c ON c.id=ma.conversation_id
			LEFT JOIN workspaces w ON w.id=COALESCE(ma.work_id,ma.workspace_id) LEFT JOIN repositories r ON r.id=w.repository_id`,
		countFrom: "FROM message_authorship ma", countAlias: "ma",
		lookups: map[string]bool{"id": true, "conversation_id": true, "workspace_id": true, "work_id": true, "day": true},
		columns: columns,
	}
}()

// authoredTextLimit bounds the bytes of a message a page row carries; the
// reader shows the rest.
const authoredTextLimit = 16000

// attachAuthoredText replaces each page row's text with at most
// authoredTextLimit bytes of it, split into its labelled spans: segments of
// {text, category, rule, reason, words, source}. text_truncated marks a row
// whose text was cut.
func (c *Catalog) attachAuthoredText(ctx context.Context, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]any, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row["id"])
	}
	spans, err := queryMapsContext(ctx, c.DB, `SELECT message_id,category,rule,reason,start_byte,end_byte,words,source_conversation_id,source_message_id
		FROM message_authorship_spans WHERE message_id IN (`+placeholders(len(ids))+`) ORDER BY message_id,position`, ids...)
	if err != nil {
		return err
	}
	byMessage := map[string][]map[string]any{}
	sources := map[string]bool{}
	for _, span := range spans {
		id := firstString(span["message_id"])
		byMessage[id] = append(byMessage[id], span)
		if source := firstString(span["source_conversation_id"]); source != "" {
			sources[source] = true
		}
	}
	titles := map[string]map[string]any{}
	if len(sources) > 0 {
		list := make([]any, 0, len(sources))
		for id := range sources {
			list = append(list, id)
		}
		found, err := queryMapsContext(ctx, c.DB, `SELECT c.id,c.workspace_id,w.title FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			WHERE c.id IN (`+placeholders(len(list))+`)`, list...)
		if err != nil {
			return err
		}
		for _, row := range found {
			titles[firstString(row["id"])] = row
		}
	}
	for _, row := range rows {
		text := firstString(row["text"])
		limit := len(text)
		if limit > authoredTextLimit {
			limit = authoredTextLimit
			for limit > 0 && !utf8.RuneStart(text[limit]) {
				limit--
			}
		}
		segments := []map[string]any{}
		plain := func(start, end int) {
			if start < end {
				segments = append(segments, map[string]any{"text": text[start:end]})
			}
		}
		at := 0
		for _, span := range byMessage[firstString(row["id"])] {
			start, end := int(integer(span["start_byte"])), min(int(integer(span["end_byte"])), limit)
			if start < at || start >= limit || end > len(text) {
				break
			}
			plain(at, start)
			segment := map[string]any{"text": text[start:end], "category": span["category"], "rule": span["rule"], "reason": span["reason"], "words": span["words"]}
			if source := titles[firstString(span["source_conversation_id"])]; source != nil {
				segment["source"] = map[string]any{"workspace_id": source["workspace_id"], "conversation_id": source["id"],
					"message_id": span["source_message_id"], "title": source["title"]}
			}
			segments = append(segments, segment)
			at = end
		}
		plain(at, limit)
		row["text"], row["segments"], row["text_truncated"] = text[:limit], segments, limit < len(text)
		row["subagent"], row["typed_message"], row["has_source"] = integer(row["subagent"]) != 0, integer(row["typed_message"]) != 0, integer(row["has_source"]) != 0
		for _, field := range []string{"categories", "rules"} {
			var values []string
			_ = json.Unmarshal([]byte(firstString(row[field])), &values)
			row[field] = values
		}
	}
	return nil
}
