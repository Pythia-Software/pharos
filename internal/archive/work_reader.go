package archive

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The transcript reader loads a workspace in pieces: an overview without
// messages, then one conversation at a time with oversized bodies shortened,
// then a shortened message's original text only when the reader shows it.
// Sending a whole workspace at once reached hundreds of megabytes for long
// sessions with many sub-agents, which crashed the page before it rendered.

const (
	// Bodies up to this many bytes are always sent whole.
	readerBodyBytes = 2 << 10
	// String values inside a shortened JSON body keep this many runes, then a
	// tighter limit applies if the body is still large.
	readerLeafRunes      = 500
	readerTightLeafRunes = 120
	// Diffs are kept longer: the reader counts their added and removed lines.
	readerDiffRunes = 64 << 10
)

// Kinds whose bodies the reader shows only when an event is expanded. Prose,
// tool calls, and delegations stay whole: their text is shown or measured
// (edit diffs) while collapsed.
var readerClippedKinds = map[string]bool{"tool_result": true, "metadata": true, "result": true, "context": true, "error": true}

const readerMessageColumns = "id,native_id,role,kind,model,text,raw_text,created_at,parent_native_id,call_id"

// WorkOverview is the workspace detail page's first request: everything but
// the messages, plus what the page used to derive by scanning all of them.
func (c *Catalog) WorkOverview(id string) (map[string]any, error) {
	result, err := c.workDetail(id, false)
	if err != nil || result == nil {
		return result, err
	}
	// The page does not show these; metric_ledger alone can be megabytes.
	for _, key := range []string{"agent_sessions", "metric_ledger", "sightings"} {
		delete(result, key)
	}
	for _, conversation := range result["conversations"].([]map[string]any) {
		delete(conversation, "message_access")
		var prompt *string
		// The prompt labels the conversation picker; a sub-agent's is its
		// assignment. The unary + keeps SQLite on the conversation index
		// instead of scanning every prompt in the catalog.
		if err := c.DB.QueryRow(`SELECT text FROM messages WHERE conversation_id=? AND +role IN ('user','agent') AND +kind='message'
			ORDER BY source_order IS NULL,source_order,created_at,id LIMIT 1`, conversation["id"]).Scan(&prompt); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if prompt != nil {
			conversation["first_prompt"], _ = clipReaderText(*prompt)
		}
	}
	if result["discovered_prs"], err = c.discoveredPullRequests(id); err != nil {
		return nil, err
	}
	if result["files"], err = c.workFileTargets(id); err != nil {
		return nil, err
	}
	return result, nil
}

var githubPullPattern = regexp.MustCompile(`(?i)https?://github\.com/[^/\s"'\\]+/[^/\s"'\\]+/pull/(\d+)`)

// discoveredPullRequests finds GitHub pull request links mentioned anywhere in
// the workspace's messages, in first-mention order.
func (c *Catalog) discoveredPullRequests(workspaceID string) ([]map[string]any, error) {
	rows, err := c.DB.Query(`SELECT m.text,COALESCE(m.raw_text,'') FROM messages m JOIN conversations c ON c.id=m.conversation_id
		WHERE c.workspace_id=? AND (instr(m.text,'/pull/')>0 OR instr(m.raw_text,'/pull/')>0)
		ORDER BY c.started_at,c.id,m.source_order IS NULL,m.source_order,m.created_at,m.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []map[string]any{}
	seen := map[string]bool{}
	for rows.Next() {
		var text, raw string
		if err := rows.Scan(&text, &raw); err != nil {
			return nil, err
		}
		for _, match := range githubPullPattern.FindAllStringSubmatch(text+" "+raw, -1) {
			if seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			var number int
			_ = json.Unmarshal([]byte(match[1]), &number)
			found = append(found, map[string]any{"host": "github.com", "number": number, "url": match[0]})
		}
	}
	return found, rows.Err()
}

// workFileTargets lists files the workspace's tool calls acted on, each with
// the first conversation that did, so the page's file index can open it.
func (c *Catalog) workFileTargets(workspaceID string) ([]map[string]any, error) {
	rows, err := queryMaps(c.DB, `SELECT t.file_path path,t.conversation_id FROM tool_calls t
		JOIN conversations c ON c.id=t.conversation_id
		WHERE t.workspace_id=? AND t.file_path IS NOT NULL AND t.file_path<>''
		ORDER BY c.started_at,t.conversation_id,t.sequence`, workspaceID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	files := []map[string]any{}
	for _, row := range rows {
		if path := firstString(row["path"]); !seen[path] {
			seen[path] = true
			files = append(files, row)
		}
	}
	return files, nil
}

// WorkConversation returns one conversation of a workspace with its messages
// ready for the reader. Large tool output and event bodies are shortened;
// such messages carry text_clipped/raw_clipped and are fetched whole with
// MessageOriginal when the reader expands them.
func (c *Catalog) WorkConversation(workspaceID, conversationID string) (map[string]any, error) {
	rows, err := queryMaps(c.DB, "SELECT * FROM conversations WHERE id=? AND workspace_id=?", conversationID, workspaceID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	conversation := rows[0]
	messages, err := queryMaps(c.DB, "SELECT "+readerMessageColumns+" FROM messages WHERE conversation_id=? ORDER BY source_order IS NULL,source_order,created_at,id", conversationID)
	if err != nil {
		return nil, err
	}
	// Authorship spans index into the original text, and token accounting
	// reads whole records, so both run before any body is shortened.
	if err := c.attachAuthorship(conversationID, messages); err != nil {
		return nil, err
	}
	records := make([]MessageRecord, 0, len(messages))
	for _, m := range messages {
		records = append(records, MessageRecord{
			NativeID: firstString(m["native_id"]), Role: firstString(m["role"]), Kind: firstString(m["kind"]),
			Text: firstString(m["text"]), RawText: firstString(m["raw_text"]), CreatedAt: firstString(m["created_at"]),
			ParentNativeID: firstString(m["parent_native_id"]), CallID: firstString(m["call_id"]),
		})
	}
	conversation["token_usage"] = conversationTokenCounts(records)
	sessions, err := queryMaps(c.DB, `SELECT * FROM agent_sessions WHERE workspace_id=? AND conversation_id=? AND usage_status<>'in-child-conversation'`, workspaceID, conversationID)
	if err != nil {
		return nil, err
	}
	if len(sessions) > 0 {
		usage := tokenCounts{}
		for _, session := range sessions {
			for _, key := range []string{"input_tokens", "uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens", "output_tokens", "reasoning_output_tokens", "unclassified_tokens", "total_tokens"} {
				usage[key] += float64(integer(session[key]))
			}
		}
		conversation["token_usage"] = usage
	}
	for _, message := range messages {
		clipReaderMessage(message)
	}
	conversation["messages"] = messages
	return conversation, nil
}

// MessageOriginal returns a message's complete text and raw source record.
func (c *Catalog) MessageOriginal(id string) (map[string]any, error) {
	rows, err := queryMaps(c.DB, "SELECT id,text,raw_text FROM messages WHERE id=?", id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// clipReaderMessage shortens a message row in place for the reader.
func clipReaderMessage(message map[string]any) {
	text, raw := firstString(message["text"]), firstString(message["raw_text"])
	textClipped := false
	if kind := firstString(message["kind"]); readerClippedKinds[kind] && !readerReinterprets(kind, text) {
		if clipped, ok := clipReaderText(text); ok {
			message["text"], message["text_bytes"], textClipped = clipped, len(text), true
		}
	}
	switch {
	case raw == "" || raw == text:
		// The reader falls back to text when raw_text is absent.
		message["raw_text"] = nil
		if textClipped {
			message["raw_clipped"] = true
		}
	default:
		if clipped, ok := clipReaderText(raw); ok {
			message["raw_text"], message["raw_bytes"], message["raw_clipped"] = clipped, len(raw), true
		}
	}
	if textClipped {
		message["text_clipped"] = true
	}
}

// readerReinterprets reports whether the reader's normalizeMessages turns
// this body into prose or a tool call, which must arrive whole. It mirrors
// normalizeMessages in ui.py: provider envelopes with text/tool blocks (split
// for every kind but results and errors), and Codex function calls,
// messages, and todo lists.
func readerReinterprets(kind, text string) bool {
	if len(text) <= readerBodyBytes {
		return false
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return false
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) != nil {
		return false
	}
	if object, ok := value.(map[string]any); ok {
		if payload, ok := object["payload"].(map[string]any); ok {
			value = payload
		}
	}
	object, _ := value.(map[string]any)
	switch firstString(object["type"]) {
	case "function_call", "custom_tool_call", "agent_message", "user_message", "message", "todo_list":
		return true
	}
	if kind == "tool_result" || kind == "delegation_result" || kind == "error" {
		return false
	}
	var blocks any = value
	if message, ok := object["message"].(map[string]any); ok && message["content"] != nil {
		blocks = message["content"]
	} else if object != nil {
		blocks = object["content"]
	}
	list, _ := blocks.([]any)
	for _, block := range list {
		if item, ok := block.(map[string]any); ok {
			switch firstString(item["type"]) {
			case "text", "input_text", "output_text", "tool_use", "tool_result", "thinking":
				return true
			}
		}
	}
	return false
}

// clipReaderText shortens a body larger than readerBodyBytes. JSON keeps its
// structure, so the fields the reader interprets while an event is collapsed
// (tool names, error flags, exit codes, token usage, working directories)
// survive and only long string values are cut. Other text keeps its start.
func clipReaderText(text string) (string, bool) {
	if len(text) <= readerBodyBytes {
		return text, false
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil && errors.Is(decoder.Decode(new(any)), io.EOF) {
			var encoded []byte
			for _, limit := range []int{readerLeafRunes, readerTightLeafRunes} {
				value = clipJSONStrings(value, "", limit)
				encoded = marshalReaderJSON(value)
				if len(encoded) <= 4*readerBodyBytes {
					break
				}
			}
			return string(encoded), true
		}
	}
	return clipRunes(text, readerBodyBytes) + "…", true
}

func clipJSONStrings(value any, key string, limit int) any {
	switch typed := value.(type) {
	case string:
		if key == "diff" || key == "patch" {
			limit = readerDiffRunes
		}
		if utf8.RuneCountInString(typed) > limit {
			return clipRunes(typed, limit) + "…"
		}
		return typed
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for name := range typed {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			typed[name] = clipJSONStrings(typed[name], name, limit)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = clipJSONStrings(item, key, limit)
		}
		return typed
	}
	return value
}

// clipRunes returns at most limit bytes of value, cut on a rune boundary.
func clipRunes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func marshalReaderJSON(value any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

// WorkConversationMatches lists, in the workspace's conversation order, the
// conversations with a message matching term at the reader's search depth.
func (c *Catalog) WorkConversationMatches(workspaceID, term, depth string, useRegex, caseSensitive bool) ([]string, error) {
	if term == "" {
		return []string{}, nil
	}
	kinds := map[string]string{
		"messages": "'message'",
		"thinking": "'message','thinking','reasoning'",
		"tools":    "'message','thinking','reasoning','tool_call','delegation'",
	}[depth]
	filter := ""
	if kinds != "" {
		filter = " AND m.kind IN (" + kinds + ")"
	}
	pattern := term
	if !useRegex {
		pattern = regexp.QuoteMeta(term)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	// A literal term narrows the scan in SQL before the exact match in Go.
	narrow, args := "", []any{workspaceID}
	if !useRegex {
		// LIKE folds only ASCII case, so other terms rely on the Go match.
		if caseSensitive {
			narrow = " AND instr(m.text,?)>0"
			args = append(args, term)
		} else if isASCII(term) {
			narrow = " AND m.text LIKE ? ESCAPE '\\'"
			args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(term)+"%")
		}
	}
	rows, err := c.DB.Query(`SELECT c.id,m.text FROM conversations c JOIN messages m ON m.conversation_id=c.id
		WHERE c.workspace_id=?`+filter+narrow+` ORDER BY c.started_at,c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := []string{}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, err
		}
		if len(matches) > 0 && matches[len(matches)-1] == id {
			continue
		}
		if expression.MatchString(text) {
			matches = append(matches, id)
		}
	}
	return matches, rows.Err()
}

func isASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
