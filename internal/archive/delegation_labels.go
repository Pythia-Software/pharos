package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Timeline messages are a bounded, read-only projection. Include nearby
// observations outside the viewport so zooming does not change attribution at
// its edges. Bodies remain excerpts; the reader owns the original messages.
var delegationMessageLimit = 2400

// Extract familiar envelope content inside SQLite, before clipping. Large source
// bodies never have to be loaded into Go just to decode an excerpt. Unknown
// structured payloads retain their JSON rather than losing domain fields.
func timelineContentSQL(source string) string {
	parts := []string{}
	for _, path := range []string{"$.content", "$.message.content", "$.text", "$.output_text", "$.message", "$.output", "$.result", "$.thinking"} {
		parts = append(parts, fmt.Sprintf(`CASE json_type(%[1]s,'%[2]s')
WHEN 'text' THEN json_extract(%[1]s,'%[2]s')
WHEN 'array' THEN (SELECT group_concat(timeline_piece,char(10)||char(10)) FROM (
 SELECT CASE block.type WHEN 'text' THEN block.value WHEN 'object' THEN
 CASE WHEN COALESCE(json_extract(block.value,'$.type'),'text') IN ('text','input_text','output_text','thinking','reasoning','tool_result') THEN
 COALESCE(CASE WHEN json_type(block.value,'$.text')='text' THEN json_extract(block.value,'$.text') END,
 CASE WHEN json_type(block.value,'$.thinking')='text' THEN json_extract(block.value,'$.thinking') END,
 CASE WHEN json_type(block.value,'$.content')='text' THEN json_extract(block.value,'$.content') END)
 WHEN json_extract(block.value,'$.type') IN ('image','image_url','input_image') THEN '[Image]'
 WHEN json_extract(block.value,'$.type') IN ('audio','input_audio') THEN '[Audio]' END END timeline_piece
 FROM json_each(%[1]s,'%[2]s') block ORDER BY CAST(block.key AS INTEGER)) WHERE timeline_piece IS NOT NULL)
END`, source, path))
	}
	return fmt.Sprintf(`CASE WHEN json_valid(%[1]s) THEN CASE WHEN json_type(%[1]s)='object' AND NOT EXISTS (
SELECT 1 FROM json_each(%[1]s) WHERE key NOT IN ('content','message','text','output_text','output','result','thinking','type','role','tool','name','id','details','is_error','status','metadata','usage','model','created_at','timestamp','parent_tool_use_id','session_id','agent_id','tool_use_id','call_id'))
THEN COALESCE(%[2]s,%[1]s) ELSE %[1]s END ELSE %[1]s END`, source, strings.Join(parts, ","))
}

func timelineInputSQL(source string) string {
	// json_extract returns the same JSON text for an argument object and an
	// argument object encoded inside a string. Decode either before clipping.
	input := fmt.Sprintf(`COALESCE(json_extract(%[1]s,'$.input'),json_extract(%[1]s,'$.arguments'),%[1]s)`, source)
	parts := []string{}
	for _, path := range []string{"$.prompt", "$.message", "$.command", "$.cmd", "$.code"} {
		parts = append(parts, fmt.Sprintf(`CASE WHEN json_type(%s,'%s')='text' THEN json_extract(%s,'%s') END`, input, path, input, path))
	}
	parts = append(parts, fmt.Sprintf(`CAST(%s AS TEXT)`, input))
	return fmt.Sprintf(`CASE WHEN json_valid(%[1]s) THEN CASE WHEN json_valid(%[2]s) THEN COALESCE(%[3]s) ELSE CAST(%[2]s AS TEXT) END ELSE %[1]s END`, source, input, strings.Join(parts, ","))
}

// Read previews only for the visible, bounded set of native Timeline calls.
// The hierarchy snapshot remains compact and never holds argument bodies.
func (c *Catalog) delegationToolPreviews(ctx context.Context, workspace string, marks []delegationMark) error {
	if len(marks) == 0 {
		return nil
	}
	ids := make([]string, 0, len(marks))
	byID := map[string]*delegationMark{}
	for i := range marks {
		ids = append(ids, marks[i].ToolID)
		byID[marks[i].ToolID] = &marks[i]
	}
	encoded, _ := json.Marshal(ids)
	rows, err := queryMapsContext(ctx, c.reader(), `WITH calls AS (
SELECT t.id,t.status,COALESCE(NULLIF(t.command,''),
CASE WHEN m.kind IN ('tool_call','delegation') THEN NULLIF(`+timelineInputSQL("m.text")+`,'') END,
NULLIF(t.file_path,''),'') input
FROM tool_calls t LEFT JOIN messages m ON m.id=t.call_message_id
WHERE t.workspace_id=? AND t.id IN (SELECT value FROM json_each(?)))
SELECT id,status,substr(input,1,640) preview,length(input)>640 truncated FROM calls`, workspace, string(encoded))
	if err != nil {
		return err
	}
	for _, row := range rows {
		if mark := byID[firstString(row["id"])]; mark != nil {
			mark.Preview, _ = row["preview"].(string)
			mark.PreviewTruncated = integer(row["truncated"]) != 0
			mark.Status = firstString(row["status"])
		}
	}
	return nil
}

func (c *Catalog) delegationTimelineMessages(ctx context.Context, snapshot *delegationSnapshot, ids []string, start, end float64) (map[string]map[string]any, []map[string]any, error) {
	lanes := map[string]map[string]any{}
	taxonomy := []map[string]any{}
	named := map[string]bool{}
	type labelReader struct {
		attach  func(map[string]any)
		pending bool
	}
	readers := map[string]labelReader{}
	canonical := func(id string) string {
		for seen := map[string]bool{}; snapshot.Aliases[id] != "" && !seen[id]; {
			seen[id] = true
			id = snapshot.Aliases[id]
		}
		return id
	}
	for _, id := range ids {
		n := snapshot.ByID[id]
		if n == nil || n.ContextOnly {
			continue
		}
		members := []string{id}
		for alias := range snapshot.Aliases {
			if canonical(alias) == id {
				members = append(members, alias)
			}
		}
		memberJSON, _ := json.Marshal(members)
		var root string
		err := c.reader().QueryRowContext(ctx, `SELECT id FROM agent_sessions WHERE conversation_id=? AND native_id='main' LIMIT 1`, n.ConversationID).Scan(&root)
		if err != nil && err != sql.ErrNoRows {
			return nil, nil, err
		}
		fallback := id == "conversation:"+n.ConversationID || canonical(root) == id
		rows, err := queryMapsContext(ctx, c.reader(), `WITH bounds(lo,hi) AS (VALUES(julianday(?,'unixepoch'),julianday(?,'unixepoch'))), candidates AS (
SELECT message_id id FROM agent_session_messages WHERE agent_session_id IN (SELECT value FROM json_each(?))
UNION SELECT id FROM messages m WHERE ? AND m.conversation_id=? AND NOT EXISTS(SELECT 1 FROM agent_session_messages am WHERE am.message_id=m.id)
)
SELECT m.id,m.conversation_id,m.role,m.kind,m.created_at,m.model,m.call_id,substr(COALESCE(m.text,''),1,320) text,
substr((CASE WHEN m.kind='delegation' THEN `+timelineInputSQL("m.text")+` WHEN m.role='user' AND m.kind='message' THEN m.text ELSE `+timelineContentSQL("m.text")+` END),1,640) preview,
length((CASE WHEN m.kind='delegation' THEN `+timelineInputSQL("m.text")+` WHEN m.role='user' AND m.kind='message' THEN m.text ELSE `+timelineContentSQL("m.text")+` END))>640 preview_truncated,
length(CAST(COALESCE(m.text,'') AS BLOB)) text_bytes,a.spans_json authorship_spans
FROM candidates JOIN messages m ON m.id=candidates.id LEFT JOIN message_authorship a ON a.message_id=m.id CROSS JOIN bounds
WHERE m.conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)
AND m.selected=1 AND m.kind IN ('message','reasoning','thinking','delegation','delegation_result')
AND (m.created_at IS NULL OR julianday(m.created_at) BETWEEN lo-30.0/1440 AND hi+30.0/1440)
ORDER BY CASE WHEN julianday(m.created_at) BETWEEN lo AND hi THEN 0 ELSE 1 END,
m.created_at IS NULL,CASE WHEN julianday(m.created_at)<lo THEN lo-julianday(m.created_at) WHEN julianday(m.created_at)>hi THEN julianday(m.created_at)-hi ELSE 0 END,
m.created_at,m.source_order IS NULL,m.source_order,m.id LIMIT ?`, start/1000, end/1000, string(memberJSON), fallback, n.ConversationID, n.WorkspaceID, delegationMessageLimit+1)
		if err != nil {
			return nil, nil, err
		}
		truncated := len(rows) > delegationMessageLimit
		if truncated {
			rows = rows[:delegationMessageLimit]
		}
		// Visible messages take precedence over context at the cap. Restore
		// chronological display order after choosing the nearest context.
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := delegationTime(rows[i]["created_at"]), delegationTime(rows[j]["created_at"])
			return a != nil && (b == nil || *a < *b)
		})
		pending := false
		// Fetch a bounded excerpt for each origin run in one batch. Sub-message
		// hovers must not be limited to the first 320 characters of the message,
		// or require loading complete (potentially very large) message bodies.
		textParts := []map[string]any{}
		partsByMessage := map[string][]map[string]any{}
		for _, row := range rows {
			row["preview_truncated"] = integer(row["preview_truncated"]) != 0
			// Preserve span order (including repeated categories and unknown gaps).
			// Cumulative byte shares retain position without exposing source offsets;
			// they describe text composition, never time spent typing.
			var spans []authorSpan
			if json.Unmarshal([]byte(firstString(row["authorship_spans"])), &spans) == nil {
				parts := []map[string]any{}
				appendPart := func(category string, size int) {
					if size <= 0 {
						return
					}
					if len(parts) > 0 && parts[len(parts)-1]["category"] == category {
						parts[len(parts)-1]["bytes"] = parts[len(parts)-1]["bytes"].(int) + size
					} else {
						parts = append(parts, map[string]any{"category": category, "bytes": size})
					}
				}
				sort.SliceStable(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
				covered, size := 0, int(integer(row["text_bytes"]))
				for _, span := range spans {
					if span.Start < covered || span.End <= span.Start || span.End > size {
						continue
					}
					appendPart("unknown", span.Start-covered)
					appendPart(span.Category, span.End-span.Start)
					covered = span.End
				}
				appendPart("unknown", size-covered)
				row["authorship"] = parts
				message := firstString(row["id"])
				partsByMessage[message] = parts
				offset := 0
				for i, part := range parts {
					size := part["bytes"].(int)
					textParts = append(textParts, map[string]any{"message": message, "part": i, "offset": offset, "bytes": size})
					offset += size
				}
			}
			delete(row, "authorship_spans")
			delete(row, "text_bytes")
			cid := firstString(row["conversation_id"])
			reader, ok := readers[cid]
			if !ok {
				attach, summary, err := c.messageLabelsAttacher(cid)
				if err != nil {
					return nil, nil, err
				}
				info, err := summary()
				if err != nil {
					return nil, nil, err
				}
				reader = labelReader{attach: attach, pending: info["pending"] == true}
				if entries, ok := info["taxonomy"].([]map[string]any); ok {
					for _, entry := range entries {
						if key := firstString(entry["id"]); !named[key] {
							named[key] = true
							taxonomy = append(taxonomy, entry)
						}
					}
				}
				readers[cid] = reader
			}
			pending = pending || reader.pending
			if !reader.pending {
				reader.attach(row)
			}
		}
		if len(textParts) > 0 {
			encoded, _ := json.Marshal(textParts)
			// 960 bytes contain at least 240 UTF-8 characters; the outer
			// substring discards any partial trailing rune beyond that limit.
			excerpts, err := queryMapsContext(ctx, c.reader(), `SELECT json_extract(p.value,'$.message') message,
json_extract(p.value,'$.part') part,
substr(CAST(substr(CAST(COALESCE(m.text,'') AS BLOB),json_extract(p.value,'$.offset')+1,min(json_extract(p.value,'$.bytes'),960)) AS TEXT),1,240) text
FROM json_each(?) p JOIN messages m ON m.id=json_extract(p.value,'$.message')`, string(encoded))
			if err != nil {
				return nil, nil, err
			}
			for _, excerpt := range excerpts {
				parts := partsByMessage[firstString(excerpt["message"])]
				i := int(integer(excerpt["part"]))
				if i >= 0 && i < len(parts) {
					text, _ := excerpt["text"].(string) // Preserve whitespace in the span.
					parts[i]["text"] = text
					parts[i]["truncated"] = len(text) < parts[i]["bytes"].(int)
				}
			}
		}
		lanes[id] = map[string]any{"messages": rows, "messages_truncated": truncated, "labels_pending": pending}
	}
	return lanes, taxonomy, nil
}
