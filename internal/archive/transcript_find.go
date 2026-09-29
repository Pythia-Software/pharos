package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The reader's Show filter sorts transcript events into these kinds, and its
// Find searches only the kinds shown. Workspace find counts matches in other
// conversations the same way, so this file mirrors the reader's
// normalizeMessages, transcriptStructure, transcriptCategory, and
// findableText (internal/archive/assets/ui.py). tests/transcript-find-fixtures.json
// holds cases both implementations must agree on.
var transcriptKinds = []string{"prompts", "response", "replies", "thinking", "edits", "commands", "reads", "tools", "agents", "events"}

// transcriptEvent is one event as the reader shows it: a stored message, or
// one block of a message that holds several.
type transcriptEvent struct {
	kind, role, nativeID, parentNativeID, callID string
	// row is the index of the stored message the event comes from.
	row int
	// text is the event's text; value is its parsed JSON, when it has any.
	text     string
	value    any
	hasValue bool
}

type transcriptFindOptions struct {
	Term                         string
	Show                         []string
	Output, Regex, CaseSensitive bool
}

type conversationFindCount struct {
	ID          string   `json:"id"`
	Shown       int      `json:"shown"`
	Hidden      int      `json:"hidden"`
	HiddenKinds []string `json:"hidden_kinds"`
}

func parseReaderJSON(text string) (any, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, false
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) != nil {
		return nil, false
	}
	return value, true
}

func jsonObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// jsString is String(value) for the values the reader reads as names and text.
func jsString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprint(typed)
	}
	return asString(value)
}

// truthy is JavaScript truthiness for decoded JSON.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case float64:
		return typed != 0
	}
	return true
}

// orValue is a ?? b: the first value that is present and not null.
func orValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func textBlocks(blocks []any) []string {
	texts := []string{}
	for _, block := range blocks {
		object := jsonObject(block)
		if text, ok := object["text"].(string); ok && object != nil {
			if kind := object["type"]; kind == "text" || kind == "input_text" || kind == "output_text" {
				texts = append(texts, text)
			}
		}
	}
	return texts
}

// commonEventText mirrors the reader's commonEventText.
func commonEventText(value any) string {
	object := jsonObject(value)
	if object == nil {
		return ""
	}
	message := object["message"]
	blocks, ok := jsonObject(message)["content"].([]any)
	if !ok {
		blocks, _ = object["content"].([]any)
	}
	if texts := textBlocks(blocks); len(texts) > 0 {
		return strings.Join(texts, "\n\n")
	}
	if text, ok := message.(string); ok {
		return text
	}
	if text, ok := jsonObject(message)["content"].(string); ok {
		return text
	}
	if text, ok := object["text"].(string); ok {
		return text
	}
	if text, ok := object["output_text"].(string); ok {
		return text
	}
	if kind := object["type"]; kind == "agent_message" || kind == "user_message" || kind == "message" {
		if text, ok := object["content"].(string); ok {
			return text
		}
	}
	return ""
}

// eventDescription mirrors the reader's eventDescription.
func eventDescription(value any) string {
	if items, ok := value.([]any); ok {
		return fmt.Sprintf("%d recorded items", len(items))
	}
	object := jsonObject(value)
	if object == nil {
		return jsString(value)
	}
	parts := []string{}
	for _, part := range []any{
		firstTruthy(object["type"], object["kind"], "Recorded event"),
		object["subtype"],
		firstTruthy(object["description"], object["summary"], object["status"]),
	} {
		if truthy(part) {
			parts = append(parts, jsString(part))
		}
	}
	return readerShortText(strings.Join(parts, " · "), 180)
}

func firstTruthy(values ...any) any {
	for _, value := range values {
		if truthy(value) {
			return value
		}
	}
	return nil
}

func readerShortText(value string, limit int) string {
	text := strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(text) > limit {
		return string([]rune(text)[:limit-1]) + "…"
	}
	return text
}

// displayText mirrors the reader's displayText for an event.
func (event transcriptEvent) displayText() string {
	value, ok := event.value, event.hasValue
	if text := commonEventText(value); text != "" {
		return text
	}
	if ok {
		return eventDescription(value)
	}
	return event.text
}

// findableText mirrors the reader's findableText: a message's prose, or the
// string values any other event carries, with object keys in sorted order.
func (event transcriptEvent) findableText() string {
	if event.kind == "message" {
		return event.displayText()
	}
	if !event.hasValue {
		return event.text
	}
	return strings.Join(jsonStrings(event.value, nil), "\n")
}

func jsonStrings(value any, out []string) []string {
	switch typed := value.(type) {
	case string:
		out = append(out, typed)
	case []any:
		for _, item := range typed {
			out = jsonStrings(item, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = jsonStrings(typed[key], out)
		}
	}
	return out
}

func newTranscriptEvent(base MessageRecord, kind, role, callID, text string) transcriptEvent {
	value, ok := parseReaderJSON(text)
	return transcriptEvent{kind: kind, role: role, nativeID: base.NativeID, parentNativeID: base.ParentNativeID, callID: callID, text: text, value: value, hasValue: ok}
}

func valueEvent(base MessageRecord, kind, role, callID string, value any) transcriptEvent {
	return transcriptEvent{kind: kind, role: role, nativeID: base.NativeID, parentNativeID: base.ParentNativeID, callID: callID, value: value, hasValue: true}
}

var (
	delegationToolName = regexp.MustCompile(`(?i)^(Task|Agent)$`)
	delegationCallName = regexp.MustCompile(`spawn_agent|delegate`)
)

// normalizeTranscript mirrors the reader's normalizeMessages and
// trimTrailingConversationNoise.
func normalizeTranscript(messages []MessageRecord) []transcriptEvent {
	events := make([]transcriptEvent, 0, len(messages))
	for row, message := range messages {
		start := len(events)
		events = normalizeRecord(message, events)
		for index := start; index < len(events); index++ {
			events[index].row = row
		}
	}
	return trimTranscriptNoise(events)
}

// normalizeRecord appends the events one stored message holds. Each message
// normalizes on its own, so any one can be re-read to recover its events.
func normalizeRecord(message MessageRecord, events []transcriptEvent) []transcriptEvent {
	parsed, _ := parseReaderJSON(message.Text)
	value := parsed
	if payload := jsonObject(jsonObject(parsed)["payload"]); payload != nil {
		value = payload
	}
	object := jsonObject(value)
	var blocks []any
	if content, ok := jsonObject(object["message"])["content"].([]any); ok {
		blocks = content
	} else if content, ok := object["content"].([]any); ok {
		blocks = content
	} else if list, ok := value.([]any); ok {
		blocks = list
	}
	if message.Kind != "tool_result" && message.Kind != "delegation_result" && message.Kind != "error" && hasReaderBlocks(blocks) {
		for _, item := range blocks {
			block := jsonObject(item)
			if block == nil {
				continue
			}
			switch block["type"] {
			case "tool_use":
				kind := "tool_call"
				if delegationToolName.MatchString(jsString(block["name"])) {
					kind = "delegation"
				}
				events = append(events, valueEvent(message, kind, "assistant", firstNonEmpty(jsString(firstTruthy(block["id"])), message.CallID), map[string]any{"tool": block["name"], "input": block["input"]}))
			case "tool_result":
				result := map[string]any{"content": block["content"], "is_error": block["is_error"]}
				if truthy(object["tool_use_result"]) {
					result["details"] = object["tool_use_result"]
				}
				events = append(events, valueEvent(message, "tool_result", "tool", firstNonEmpty(jsString(firstTruthy(block["tool_use_id"])), message.CallID), result))
			case "text", "input_text", "output_text":
				role := jsString(firstTruthy(jsonObject(object["message"])["role"], object["role"], message.Role))
				text, _ := block["text"].(string)
				events = append(events, newTranscriptEvent(message, "message", role, message.CallID, text))
			case "thinking":
				text := jsString(firstTruthy(block["thinking"], block["text"], "Reasoning not retained"))
				events = append(events, newTranscriptEvent(message, "reasoning", message.Role, message.CallID, text))
			default:
				events = append(events, valueEvent(message, "metadata", message.Role, message.CallID, block))
			}
		}
		return events
	}
	switch object["type"] {
	case "function_call", "custom_tool_call":
		kind := "tool_call"
		if delegationCallName.MatchString(jsString(object["name"])) {
			kind = "delegation"
		}
		// parsedJSON(arguments) || arguments || input
		input := object["input"]
		if truthy(object["arguments"]) {
			input = object["arguments"]
		}
		if arguments, ok := object["arguments"].(string); ok {
			if parsedArguments, ok := parseReaderJSON(arguments); ok {
				input = parsedArguments
			}
		}
		events = append(events, valueEvent(message, kind, "assistant", firstNonEmpty(jsString(firstTruthy(object["call_id"])), message.CallID), map[string]any{"tool": object["name"], "input": input}))
	case "function_call_output", "custom_tool_call_output":
		events = append(events, valueEvent(message, "tool_result", "tool", firstNonEmpty(jsString(firstTruthy(object["call_id"])), message.CallID), map[string]any{"output": object["output"]}))
	case "todo_list":
		if items, ok := object["items"].([]any); ok {
			events = append(events, valueEvent(message, "tool_call", message.Role, message.CallID, map[string]any{"tool": "TodoWrite", "input": map[string]any{"todos": items}}))
			break
		}
		events = append(events, newTranscriptEvent(message, message.Kind, message.Role, message.CallID, message.Text))
	case "agent_message", "user_message", "message":
		role := jsString(firstTruthy(object["role"], message.Role))
		if object["type"] == "user_message" {
			role = "user"
		}
		text := commonEventText(value)
		if text == "" {
			text = eventDescription(value)
		}
		events = append(events, newTranscriptEvent(message, "message", role, message.CallID, text))
	default:
		events = append(events, newTranscriptEvent(message, message.Kind, message.Role, message.CallID, message.Text))
	}
	return events
}

func hasReaderBlocks(blocks []any) bool {
	for _, block := range blocks {
		switch jsonObject(block)["type"] {
		case "text", "input_text", "output_text", "tool_use", "tool_result", "thinking":
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// trimTranscriptNoise drops a final "aborted by user" error that follows a
// successful result, as the reader does.
func trimTranscriptNoise(events []transcriptEvent) []transcriptEvent {
	if len(events) < 2 {
		return events
	}
	valueOf := func(event transcriptEvent) map[string]any {
		value := event.value
		if payload := jsonObject(jsonObject(value)["payload"]); payload != nil {
			return payload
		}
		return jsonObject(value)
	}
	last, previous := valueOf(events[len(events)-1]), valueOf(events[len(events)-2])
	content, _ := last["content"].(string)
	aborted := events[len(events)-1].kind == "error" && last["type"] == "error" && strings.ToLower(strings.TrimSpace(content)) == "aborted by user"
	succeeded := previous["type"] == "result" && strings.ToLower(jsString(firstTruthy(previous["subtype"], previous["status"], ""))) == "success"
	if aborted && succeeded {
		return events[:len(events)-1]
	}
	return events
}

var (
	todoToolName    = regexp.MustCompile(`todo|task(create|update|delete|list|get)|update_plan`)
	editToolName    = regexp.MustCompile(`patch|edit|write|replace`)
	commandToolName = regexp.MustCompile(`exec|command|shell|terminal|bash`)
	searchToolName  = regexp.MustCompile(`grep|search|find|glob|query`)
	webToolName     = regexp.MustCompile(`browser|web|navigate|click`)
	readToolName    = regexp.MustCompile(`read|open|view|list`)
	agentToolName   = regexp.MustCompile(`^(task|agent)$|spawn_agent`)
	// Antigravity names tool arguments in PascalCase; the reader looks for the conventional names.
	inputAliases = map[string]string{"CommandLine": "command", "AbsolutePath": "file_path", "TargetFile": "file_path", "DirectoryPath": "path", "SearchPath": "path", "Url": "url", "Query": "query", "Prompt": "prompt"}
)

// toolCategory mirrors the reader's toolCategory.
func (event transcriptEvent) toolCategory() string {
	object := jsonObject(event.value)
	name, input := "", map[string]any{}
	if object != nil {
		name = strings.ToLower(jsString(firstTruthy(object["tool"], object["name"], "")))
		source := orValue(object["input"], object["arguments"])
		if source == nil {
			source = object
		}
		if text, ok := source.(string); ok {
			if parsed, ok := parseReaderJSON(text); ok && truthy(parsed) {
				source = parsed
			}
		}
		if fields := jsonObject(source); fields != nil {
			input = fields
		}
	}
	// An argument under its conventional name, or under the alias the reader renames to it.
	field := func(name string) any {
		if value, ok := input[name]; ok {
			return value
		}
		for key, alias := range inputAliases {
			if value, ok := input[key]; ok && alias == name {
				return value
			}
		}
		return nil
	}
	switch {
	case event.kind == "error" || truthy(object["is_error"]):
		return "error"
	case event.kind == "reasoning":
		return "reasoning"
	case event.kind == "metadata" || event.kind == "result":
		return "run"
	case event.kind == "context" || event.kind == "attachment":
		return "context"
	case strings.Contains(event.kind, "delegation") || agentToolName.MatchString(name):
		return "agent"
	case todoToolName.MatchString(name):
		return "todo"
	case editToolName.MatchString(name):
		return "edit"
	case commandToolName.MatchString(name) || truthy(field("cmd")) || truthy(field("command")):
		return "command"
	case searchToolName.MatchString(name):
		return "search"
	case webToolName.MatchString(name) || truthy(field("url")):
		return "web"
	case readToolName.MatchString(name):
		return "read"
	}
	return "tool"
}

// transcriptStructure mirrors the reader's transcriptStructure: which results
// pair with calls, each turn's final reply, and each event's category path.
type transcriptStructure struct {
	events   []transcriptEvent
	consumed map[int]int
	paths    [][]string
}

func newTranscriptStructure(events []transcriptEvent) transcriptStructure {
	delegationCalls, toolCalls, delegations := map[string]int{}, map[string]int{}, map[string]int{}
	for index, event := range events {
		if event.callID == "" {
			continue
		}
		if _, seen := delegationCalls[event.callID]; event.kind == "delegation" && !seen {
			delegationCalls[event.callID] = index
		}
		if _, seen := toolCalls[event.callID]; event.kind == "tool_call" && !seen {
			toolCalls[event.callID] = index
		}
	}
	consumed := map[int]int{}
	for index, event := range events {
		if event.kind == "delegation" {
			for _, key := range []string{event.nativeID, event.callID} {
				if _, seen := delegations[key]; key != "" && !seen {
					delegations[key] = index
				}
			}
		}
		if event.callID == "" || (event.kind != "delegation_result" && event.kind != "tool_result" && event.kind != "error") {
			continue
		}
		if call, ok := delegationCalls[event.callID]; ok {
			consumed[index] = call
		} else if call, ok := toolCalls[event.callID]; ok && event.kind != "delegation_result" {
			consumed[index] = call
		}
	}
	finalReplies := map[int]bool{}
	last := -1
	for index, event := range events {
		if _, ok := consumed[index]; ok {
			continue
		}
		if _, nested := delegations[event.parentNativeID]; event.parentNativeID != "" && nested {
			continue
		}
		if event.role == "user" && event.kind == "message" {
			if last >= 0 {
				finalReplies[last] = true
			}
			last = -1
		} else if event.kind == "message" && event.role != "agent" {
			last = index
		}
	}
	if last >= 0 {
		finalReplies[last] = true
	}
	structure := transcriptStructure{events: events, consumed: consumed, paths: make([][]string, len(events))}
	visiting := map[int]bool{}
	var pathOf func(int) []string
	pathOf = func(index int) []string {
		if structure.paths[index] != nil {
			return structure.paths[index]
		}
		if visiting[index] {
			return []string{}
		}
		visiting[index] = true
		var path []string
		if call, ok := consumed[index]; ok {
			path = pathOf(call)
		} else {
			path = []string{transcriptCategory(events[index], finalReplies[index])}
			if parent, ok := delegations[events[index].parentNativeID]; ok && events[index].parentNativeID != "" {
				path = append(path, pathOf(parent)...)
			}
		}
		structure.paths[index] = path
		return path
	}
	for index := range events {
		pathOf(index)
	}
	return structure
}

// transcriptCategory mirrors the reader's transcriptCategory.
func transcriptCategory(event transcriptEvent, finalReply bool) string {
	switch event.kind {
	case "delegation":
		return "agents"
	case "reasoning":
		return "thinking"
	case "message":
		// A sub-agent's conversation opens with its parent agent's request.
		if event.role == "user" || event.role == "agent" {
			return "prompts"
		}
		if finalReply {
			return "response"
		}
		return "replies"
	}
	switch event.toolCategory() {
	case "edit":
		return "edits"
	case "command":
		return "commands"
	case "read", "search":
		return "reads"
	case "error", "run", "context":
		return "events"
	}
	return "tools"
}

// find mirrors the reader's transcriptFind: matching events the shown kinds
// show, and those hidden with the kinds hiding them.
func (structure transcriptStructure) find(pattern *regexp.Regexp, shown map[string]bool, output bool) (int, int, []string) {
	visible, hidden, kinds := 0, 0, map[string]bool{}
	for index, event := range structure.events {
		if _, ok := structure.consumed[index]; ok && !output {
			continue
		}
		if !pattern.MatchString(event.findableText()) {
			continue
		}
		missing := false
		for _, key := range structure.paths[index] {
			if !shown[key] {
				missing, kinds[key] = true, true
			}
		}
		if missing {
			hidden++
		} else {
			visible++
		}
	}
	hiddenKinds := []string{}
	for _, key := range transcriptKinds {
		if kinds[key] {
			hiddenKinds = append(hiddenKinds, key)
		}
	}
	return visible, hidden, hiddenKinds
}

func transcriptFindPattern(term string, useRegex, caseSensitive bool) (*regexp.Regexp, error) {
	pattern := term
	if !useRegex {
		pattern = regexp.QuoteMeta(term)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

// storedLiterally reports whether term appears unchanged inside any JSON
// string that contains it, so a text scan of stored messages cannot miss it.
func storedLiterally(term string) bool {
	for _, r := range term {
		if r < 0x20 || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029 || !unicode.IsPrint(r) && r != ' ' {
			return false
		}
	}
	return true
}

// transcriptFindCache keeps the reader structure of recently searched
// workspaces, so each keystroke of a workspace find reads and classifies only
// the messages that can match. Any catalog change discards it.
type transcriptFindCache struct {
	mu         sync.Mutex
	version    int64
	workspaces []*workspaceTranscripts // most recently searched first
}

const transcriptFindWorkspaces = 4

type workspaceTranscripts struct {
	id            string
	order         []string // conversation IDs in the workspace's order
	conversations map[string]*conversationTranscript
}

type conversationTranscript struct {
	spans    map[string][2]int // message ID → first event and event count
	paths    [][]string
	consumed []bool
}

// workspaceTranscripts returns the workspace's structure, from the cache while
// the catalog is unchanged.
func (c *Catalog) workspaceTranscripts(ctx context.Context, workspaceID string) (*workspaceTranscripts, error) {
	cache := &c.transcripts
	version, versionErr := c.catalogVersion(ctx)
	cache.mu.Lock()
	if versionErr != nil || cache.version != version {
		cache.workspaces, cache.version = nil, version
	}
	for index, workspace := range cache.workspaces {
		if workspace.id == workspaceID {
			cache.workspaces = append(append([]*workspaceTranscripts{workspace}, cache.workspaces[:index]...), cache.workspaces[index+1:]...)
			cache.mu.Unlock()
			return workspace, nil
		}
	}
	cache.mu.Unlock()
	workspace, err := c.readWorkspaceTranscripts(ctx, workspaceID)
	if err != nil || versionErr != nil {
		return workspace, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.version == version {
		cache.workspaces = append([]*workspaceTranscripts{workspace}, cache.workspaces[:min(len(cache.workspaces), transcriptFindWorkspaces-1)]...)
	}
	return workspace, nil
}

// readWorkspaceTranscripts classifies every conversation in the workspace in
// one pass, in the reader's message order. Result bodies are not read: results
// neither start turns nor name tools, so the structure does not need them.
func (c *Catalog) readWorkspaceTranscripts(ctx context.Context, workspaceID string) (*workspaceTranscripts, error) {
	workspace := &workspaceTranscripts{id: workspaceID, conversations: map[string]*conversationTranscript{}}
	rows, err := c.DB.QueryContext(ctx, `SELECT c.id,COALESCE(m.id,''),COALESCE(m.native_id,''),COALESCE(m.role,''),COALESCE(m.kind,''),COALESCE(m.parent_native_id,''),COALESCE(m.call_id,''),
		CASE WHEN m.kind IN ('tool_result','delegation_result') THEN '' ELSE COALESCE(m.text,'') END
		FROM conversations c LEFT JOIN messages m ON m.conversation_id=c.id WHERE c.workspace_id=?
		ORDER BY c.started_at,c.id,m.source_order IS NULL,m.source_order,m.created_at,m.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	current, ids, records := "", []string{}, []MessageRecord{}
	finish := func() {
		if current == "" {
			return
		}
		structure := newTranscriptStructure(normalizeTranscript(records))
		conversation := &conversationTranscript{spans: map[string][2]int{}, paths: structure.paths, consumed: make([]bool, len(structure.events))}
		for index, event := range structure.events {
			span := conversation.spans[ids[event.row]]
			if span[1] == 0 {
				span[0] = index
			}
			span[1]++
			conversation.spans[ids[event.row]] = span
			_, conversation.consumed[index] = structure.consumed[index]
		}
		workspace.order = append(workspace.order, current)
		workspace.conversations[current] = conversation
	}
	for rows.Next() {
		var conversationID, id string
		var record MessageRecord
		if err := rows.Scan(&conversationID, &id, &record.NativeID, &record.Role, &record.Kind, &record.ParentNativeID, &record.CallID, &record.Text); err != nil {
			return nil, err
		}
		if conversationID != current {
			finish()
			current, ids, records = conversationID, ids[:0], records[:0]
		}
		if id != "" {
			ids, records = append(ids, id), append(records, record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	finish()
	return workspace, nil
}

// WorkConversationMatches counts, for each conversation in the workspace with
// a match, the matching events in the kinds shown and in those hidden, in the
// workspace's conversation order. An empty Show counts every kind as shown.
func (c *Catalog) WorkConversationMatches(ctx context.Context, workspaceID string, o transcriptFindOptions) ([]conversationFindCount, error) {
	matches := []conversationFindCount{}
	if o.Term == "" {
		return matches, nil
	}
	expression, err := transcriptFindPattern(o.Term, o.Regex, o.CaseSensitive)
	if err != nil {
		return nil, err
	}
	shown := map[string]bool{}
	for _, key := range o.Show {
		shown[key] = true
	}
	if len(o.Show) == 0 {
		for _, key := range transcriptKinds {
			shown[key] = true
		}
	}
	workspace, err := c.workspaceTranscripts(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// A literal term narrows the scan in SQL before the exact match in Go.
	// LIKE folds only ASCII case, so other terms rely on the Go match.
	narrow, args := "", []any{workspaceID}
	if !o.Regex && storedLiterally(o.Term) {
		if o.CaseSensitive {
			narrow, args = " AND instr(m.text,?)>0", append(args, o.Term)
		} else if isASCII(o.Term) {
			narrow, args = ` AND m.text LIKE ? ESCAPE '\'`, append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(o.Term)+"%")
		}
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT m.conversation_id,m.id,COALESCE(m.native_id,''),COALESCE(m.role,''),COALESCE(m.kind,''),COALESCE(m.parent_native_id,''),COALESCE(m.call_id,''),COALESCE(m.text,'')
		FROM conversations c JOIN messages m ON m.conversation_id=c.id WHERE c.workspace_id=?`+narrow, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type tally struct {
		shown, hidden int
		kinds         map[string]bool
	}
	tallies := map[string]*tally{}
	for rows.Next() {
		var conversationID, id string
		var record MessageRecord
		if err := rows.Scan(&conversationID, &id, &record.NativeID, &record.Role, &record.Kind, &record.ParentNativeID, &record.CallID, &record.Text); err != nil {
			return nil, err
		}
		conversation := workspace.conversations[conversationID]
		if conversation == nil {
			continue
		}
		span, ok := conversation.spans[id]
		if !ok || !o.Output && span[1] == 1 && conversation.consumed[span[0]] {
			continue
		}
		for offset, event := range normalizeRecord(record, nil) {
			index := span[0] + offset
			if offset >= span[1] || conversation.consumed[index] && !o.Output || !expression.MatchString(event.findableText()) {
				continue
			}
			count := tallies[conversationID]
			if count == nil {
				count = &tally{kinds: map[string]bool{}}
				tallies[conversationID] = count
			}
			missing := false
			for _, key := range conversation.paths[index] {
				if !shown[key] {
					missing, count.kinds[key] = true, true
				}
			}
			if missing {
				count.hidden++
			} else {
				count.shown++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range workspace.order {
		if count := tallies[id]; count != nil {
			kinds := []string{}
			for _, key := range transcriptKinds {
				if count.kinds[key] {
					kinds = append(kinds, key)
				}
			}
			matches = append(matches, conversationFindCount{ID: id, Shown: count.shown, Hidden: count.hidden, HiddenKinds: kinds})
		}
	}
	return matches, nil
}
