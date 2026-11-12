package archive

import (
	"archive/zip"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	chatGPTCodeID   = "c0000000-0000-4000-8000-000000000001"
	chatGPTSearchID = "c0000000-0000-4000-8000-000000000002"
)

// chatGPTNodeOf is one mapping node: a message from role, with its content,
// under parent; at 0, the message has no time, as ChatGPT's hidden ones.
func chatGPTNodeOf(id, parent string, children []string, role string, at float64, content map[string]any, extra map[string]any) map[string]any {
	var created any
	if at != 0 {
		created = at
	}
	message := map[string]any{"id": id, "author": map[string]any{"role": role}, "create_time": created, "content": content, "metadata": map[string]any{}, "recipient": "all"}
	for key, value := range extra {
		message[key] = value
	}
	var parentID any
	if parent != "" {
		parentID = parent
	}
	return map[string]any{"id": id, "parent": parentID, "children": children, "message": message}
}

func chatGPTText(parts ...any) map[string]any {
	return map[string]any{"content_type": "text", "parts": parts}
}

// chatGPTConversations is an export's conversations, in the shapes ChatGPT
// writes:
//   - one with custom instructions, an image, a reply the person
//     regenerated, the model's thoughts, and code it ran;
//   - one that searched the web, whose export names no current node.
func chatGPTConversations() []any {
	gpt := map[string]any{"metadata": map[string]any{"model_slug": "gpt-5"}}
	hidden := map[string]any{"metadata": map[string]any{"is_visually_hidden_from_conversation": true}}
	hiddenGPT := map[string]any{"metadata": map[string]any{"model_slug": "gpt-5", "is_visually_hidden_from_conversation": true}}
	code := map[string]any{
		"id": chatGPTCodeID, "conversation_id": chatGPTCodeID, "title": "Primes", "create_time": 1793720000.5, "update_time": 1793720100.25,
		"current_node": "answer", "default_model_slug": "gpt-5",
		"mapping": map[string]any{
			"root":         map[string]any{"id": "root", "parent": nil, "children": []string{"system"}, "message": nil},
			"system":       chatGPTNodeOf("system", "root", []string{"instructions"}, "system", 0, chatGPTText("You are ChatGPT."), hidden),
			"instructions": chatGPTNodeOf("instructions", "system", []string{"ask"}, "user", 0, map[string]any{"content_type": "user_editable_context", "user_profile": "", "user_instructions": "Answer briefly"}, hidden),
			"ask": chatGPTNodeOf("ask", "instructions", []string{"first", "thoughts"}, "user", 1793720001, map[string]any{"content_type": "multimodal_text", "parts": []any{
				map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "file-service://file-abc", "width": 640, "height": 480}, "List the primes below 10",
				map[string]any{"content_type": "real_time_user_audio_video_asset_pointer", "audio_asset_pointer": map[string]any{"content_type": "audio_asset_pointer", "asset_pointer": "sediment://file-voice"}, "frames_asset_pointers": []any{}},
				map[string]any{"content_type": "audio_transcription", "text": "Only the odd ones?"}}}, nil),
			"first":    chatGPTNodeOf("first", "ask", nil, "assistant", 1793720002, chatGPTText("Two, three, five."), gpt),
			"thoughts": chatGPTNodeOf("thoughts", "ask", []string{"recap"}, "assistant", 1793720010, map[string]any{"content_type": "thoughts", "thoughts": []any{map[string]any{"summary": "Primes", "content": "Check each number below 10"}}}, hiddenGPT),
			"recap":    chatGPTNodeOf("recap", "thoughts", []string{"call"}, "assistant", 1793720011, map[string]any{"content_type": "reasoning_recap", "content": "Thought for 2 seconds", "parts": []any{"Thought for 2 seconds"}}, gpt),
			"call": chatGPTNodeOf("call", "recap", []string{"output"}, "assistant", 1793720012, map[string]any{"content_type": "code", "language": "python", "text": "print([2, 3, 5, 7])"},
				map[string]any{"recipient": "python", "metadata": map[string]any{"model_slug": "gpt-5"}}),
			"output": chatGPTNodeOf("output", "call", []string{"answer"}, "tool", 1793720013, map[string]any{"content_type": "execution_output", "text": "[2, 3, 5, 7]"},
				map[string]any{"author": map[string]any{"role": "tool", "name": "python"}}),
			"answer": chatGPTNodeOf("answer", "output", nil, "assistant", 1793720014, chatGPTText("2, 3, 5 and 7."), gpt),
		},
	}
	search := map[string]any{
		"id": chatGPTSearchID, "title": "News", "create_time": 1793721000, "update_time": 1793721100,
		"mapping": map[string]any{
			"ask": chatGPTNodeOf("ask", "", []string{"search", "blank"}, "user", 1793721001, chatGPTText("What's in the news?"), nil),
			// Parts that are only empty text, which the adapter before
			// chatgpt-v2 read as a line break.
			"blank":   chatGPTNodeOf("blank", "ask", nil, "system", 1793721002.5, chatGPTText("", ""), hidden),
			"search":  chatGPTNodeOf("search", "ask", []string{"results"}, "assistant", 1793721002, chatGPTText(`search("news")`), map[string]any{"recipient": "browser"}),
			"results": chatGPTNodeOf("results", "search", []string{"older", "newer"}, "tool", 1793721003, map[string]any{"content_type": "tether_browsing_display", "result": "Headlines"}, map[string]any{"author": map[string]any{"role": "tool", "name": "browser"}}),
			// A part in another shape is read as text too.
			"older": chatGPTNodeOf("older", "results", nil, "assistant", 1793721004, chatGPTText("Some", map[string]any{"type": "text", "text": "headlines."}), gpt),
			"newer": chatGPTNodeOf("newer", "results", []string{"placeholder"}, "assistant", 1793721005, chatGPTText("Today's headlines."), gpt),
			// A node without a message ends the branch.
			"placeholder": map[string]any{"id": "placeholder", "parent": "newer", "children": []any{}, "message": nil},
		},
	}
	return []any{code, search}
}

// recordsMessage finds a message by its ID in any of records.
func recordsMessage(records map[string]WorkspaceRecord, id string) (MessageRecord, bool) {
	for _, record := range records {
		for _, conversation := range record.Conversations {
			for _, message := range conversation.Messages {
				if message.NativeID == id {
					return message, true
				}
			}
		}
	}
	return MessageRecord{}, false
}

func chatGPTSource(path string) SourceConfig {
	return SourceConfig{Name: "chatgpt", Kind: "chatgpt", Path: path, Account: "local", Enabled: true}
}

func chatGPTRecords(t *testing.T, path string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(chatGPTSource(path))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

// chatGPTZip writes a ZIP holding files.
func chatGPTZip(t *testing.T, file string, files map[string]string) {
	t.Helper()
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(out)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		entry, err := archive.Create(name)
		if err == nil {
			_, err = entry.Write([]byte(files[name]))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTExportReadsTheCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText(chatGPTConversations()))
	records := chatGPTRecords(t, dir)
	code := records[chatGPTCodeID]
	if len(records) != 2 || code.Title != "Primes" || code.Purpose != "List the primes below 10\nOnly the odd ones?" {
		t.Fatalf("records %v, code %+v", slices.Sorted(maps.Keys(records)), code)
	}
	conversation := code.Conversations[0]
	if conversation.Model != "gpt-5" || conversation.Harness != "chatgpt-export" {
		t.Fatalf("conversation %+v", conversation)
	}
	got := []string{}
	for _, message := range conversation.Messages {
		selected := ""
		if !message.Selected {
			selected = " (unselected)"
		}
		got = append(got, message.NativeID+" "+message.Role+" "+message.Kind+" <"+message.PreviousNativeID+"> "+message.CallID+selected)
	}
	want := []string{
		chatGPTCodeID + ":export-context system metadata <> ",
		// The hidden system message and the custom instructions are
		// context.
		"system system " + contextKind + " <> ",
		"instructions system " + contextKind + " <system> ",
		"ask user message <instructions> ",
		"ask:part:0 user attachment <instructions> ",
		"ask:part:1 user attachment <instructions> ",
		// The regenerated reply is kept, off the current branch.
		"first assistant message <ask>  (unselected)",
		"thoughts assistant reasoning <ask> ",
		// The recap after them sums them up; it keeps its ID.
		"recap assistant metadata <thoughts> ",
		"call assistant tool_call <recap> call",
		"output tool tool_result <call> call",
		"answer assistant message <output> ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("messages\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	byID := map[string]MessageRecord{}
	for _, message := range conversation.Messages {
		byID[message.NativeID] = message
	}
	if byID["thoughts"].Text != "Check each number below 10" || byID["thoughts"].ReasoningFormat != "text" {
		t.Fatalf("thoughts %+v", byID["thoughts"])
	}
	if !strings.Contains(byID["call"].Text, `"tool":"python"`) || !strings.Contains(byID["call"].Text, "print([2, 3, 5, 7])") || !strings.Contains(byID["output"].Text, "[2, 3, 5, 7]") {
		t.Fatalf("call %s, output %s", byID["call"].Text, byID["output"].Text)
	}
	if !strings.Contains(byID["ask:part:0"].Text, "file-service://file-abc") || !strings.Contains(byID["ask:part:1"].Text, "sediment://file-voice") || !strings.Contains(byID["instructions"].Text, "Answer briefly") {
		t.Fatalf("image %s, voice %s, instructions %s", byID["ask:part:0"].Text, byID["ask:part:1"].Text, byID["instructions"].Text)
	}
	if byID["ask"].Text != "List the primes below 10\nOnly the odd ones?" {
		t.Fatalf("prompt %q", byID["ask"].Text)
	}
	// Every node with text is read under its ID, as the adapter before
	// chatgpt-v2 read it, so a read replaces what that one indexed.
	for _, raw := range chatGPTConversations() {
		for id, node := range raw.(map[string]any)["mapping"].(map[string]any) {
			message := mapValue(node.(map[string]any)["message"])
			if text := messageText(mapValue(message["content"])["parts"]); text != "" {
				if _, ok := recordsMessage(records, id); !ok {
					t.Errorf("node %s, with text %q, is not read under its ID", id, text)
				}
			}
		}
	}

	if older, _ := recordsMessage(records, "older"); older.Text != "Some\nheadlines." {
		t.Fatalf("mixed parts = %q", older.Text)
	}

	// An export that lists only each node's parent reads the same.
	shape := func(records map[string]WorkspaceRecord) []string {
		lines := []string{}
		for _, id := range slices.Sorted(maps.Keys(records)) {
			for _, message := range records[id].Conversations[0].Messages {
				lines = append(lines, fmt.Sprintf("%s <%s> %t", message.NativeID, message.PreviousNativeID, message.Selected))
			}
		}
		return lines
	}
	stripped := chatGPTConversations()
	for _, raw := range stripped {
		for _, node := range raw.(map[string]any)["mapping"].(map[string]any) {
			delete(node.(map[string]any), "children")
		}
	}
	parents := t.TempDir()
	writeSourceFile(t, filepath.Join(parents, "conversations.json"), jsonText(stripped))
	if got, want := shape(chatGPTRecords(t, parents)), shape(records); !slices.Equal(got, want) {
		t.Fatalf("with parents only\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Without a current node, the newest message with none after it ends
	// the branch: not one before it at the same time, nor a node without a
	// message after it.
	timed := func(parent string, at float64) chatGPTNode {
		return chatGPTNode{Parent: parent, Message: map[string]any{"create_time": at}}
	}
	tied := chatGPTConversation{Mapping: map[string]chatGPTNode{"z": timed("", 5), "a": timed("z", 5), "p": {Parent: "a"}}}
	if got := chatGPTSelected(tied, chatGPTChildren(tied.Mapping)); !got["a"] || !got["z"] {
		t.Fatalf("selected %v", got)
	}
	// A message without a time takes its branch's.
	untimed := chatGPTConversation{Mapping: map[string]chatGPTNode{"q": timed("", 1), "old": timed("q", 100), "new": timed("q", 200), "tail": {Parent: "new", Message: map[string]any{}}}}
	if got := chatGPTSelected(untimed, chatGPTChildren(untimed.Mapping)); !got["tail"] || got["old"] {
		t.Fatalf("with an untimed last message: selected %v", got)
	}

	// Without a current node, the newest leaf's branch is selected.
	selected := []string{}
	for _, message := range records[chatGPTSearchID].Conversations[0].Messages {
		if message.Selected {
			selected = append(selected, message.NativeID+" "+message.Kind)
		}
	}
	if want := []string{chatGPTSearchID + ":export-context metadata", "ask message", "search tool_call", "results tool_result", "newer message"}; !slices.Equal(selected, want) {
		t.Fatalf("selected %v, want %v", selected, want)
	}
}

// Reasoning and tool replies in the other shapes ChatGPT writes: o1's
// reasoning summary as a tool's reply to no call, thoughts with and without
// their text, a browser click that quotes several pages (one of them
// regenerated, as an alternative that failed), and Python code
// that raised an exception.
func TestChatGPTExportReadsReasoningAndToolReplies(t *testing.T) {
	const id = "c0000000-0000-4000-8000-000000000003"
	browser := map[string]any{"author": map[string]any{"role": "tool", "name": "browser"}}
	quote := func(id, parent string, children []string, at float64, text string) map[string]any {
		return chatGPTNodeOf(id, parent, children, "tool", at, map[string]any{"content_type": "tether_quote", "url": "https://example.com/" + id, "title": id, "text": text}, browser)
	}
	failed := map[string]any{"author": map[string]any{"role": "tool", "name": "python"}, "metadata": map[string]any{"aggregate_result": map[string]any{
		"status": "failed_with_in_kernel_exception", "in_kernel_exception": map[string]any{"name": "ZeroDivisionError", "traceback": []any{"ZeroDivisionError: division by zero"}}}}}
	conversation := map[string]any{
		"id": id, "title": "Pages", "create_time": 1793722000, "update_time": 1793722100, "current_node": "answer",
		"mapping": map[string]any{
			"ask": chatGPTNodeOf("ask", "", []string{"o1"}, "user", 1793722001, chatGPTText("Compare the pages"), nil),
			"o1": chatGPTNodeOf("o1", "ask", []string{"thoughts"}, "tool", 1793722002, chatGPTText("Weighing the pages"),
				map[string]any{"author": map[string]any{"role": "tool", "name": "a8km123"}, "metadata": map[string]any{"initial_text": "Thinking", "finished_text": "Thought for 3 seconds"}}),
			"thoughts": chatGPTNodeOf("thoughts", "o1", []string{"click"}, "assistant", 1793722003, map[string]any{"content_type": "thoughts", "thoughts": []any{
				map[string]any{"summary": "First", "content": "Open the first page"}, map[string]any{"summary": "Second", "content": ""}, map[string]any{"summary": "Third", "content": "Open the third page"}}}, nil),
			"click": chatGPTNodeOf("click", "thoughts", []string{"q1"}, "assistant", 1793722004, map[string]any{"content_type": "code", "text": "mclick([0, 1, 2])"}, map[string]any{"recipient": "browser"}),
			"q1":    quote("q1", "click", []string{"q2b", "q2"}, 1793722010, "Page one"),
			// The alternative, off the current branch, listed first.
			"q2b": chatGPTNodeOf("q2b", "q1", []string{"q3b"}, "tool", 1793722011, map[string]any{"content_type": "system_error", "text": "Page two failed"}, browser),
			"q3b": quote("q3b", "q2b", nil, 1793722012, "Page three, again"),
			"q2":  quote("q2", "q1", []string{"q3"}, 1793722011, "Page two"),
			"q3":  quote("q3", "q2", []string{"run"}, 1793722012, "Page three"),
			"run": chatGPTNodeOf("run", "q3", []string{"error"}, "assistant", 1793722020, map[string]any{"content_type": "code", "language": "python", "text": "1 / 0"}, map[string]any{"recipient": "python"}),
			// Python wrote the exception as its output.
			"error":  chatGPTNodeOf("error", "run", []string{"answer"}, "tool", 1793722021, map[string]any{"content_type": "execution_output", "text": "ZeroDivisionError: division by zero"}, failed),
			"answer": chatGPTNodeOf("answer", "error", nil, "assistant", 1793722030, chatGPTText("They agree."), nil),
		},
	}
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText([]any{conversation}))
	messages := chatGPTRecords(t, dir)[id].Conversations[0].Messages
	got, byID := []string{}, map[string]MessageRecord{}
	for _, message := range messages {
		got = append(got, message.NativeID+" "+message.Role+" "+message.Kind+" "+message.ReasoningFormat+" "+message.CallID)
		byID[message.NativeID] = message
	}
	want := []string{
		id + ":export-context system metadata  ",
		"ask user message  ",
		"o1 assistant reasoning summary ",
		// A thought without its text is read as its summary.
		"thoughts assistant reasoning summary ",
		"click assistant tool_call  click",
		// Each page the click quotes replies to it.
		"q1 tool tool_result  click",
		"q2b tool tool_result  click",
		"q3b tool tool_result  click",
		"q2 tool tool_result  click",
		"q3 tool tool_result  click",
		"run assistant tool_call  run",
		"error tool tool_result  run",
		"answer assistant message  ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("messages\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if byID["o1"].Text != "Weighing the pages" {
		t.Fatalf("o1 reasoning %q", byID["o1"].Text)
	}
	if text := byID["thoughts"].Text; text != "Open the first page\n\nSecond\n\nOpen the third page" {
		t.Fatalf("thoughts %q", text)
	}
	// The click is one call, whose output is every page it quoted on the
	// current branch, not the alternative, which failed; the
	// exception fails the run.
	_, calls := buildToolLedger(messages, "")
	ledger := map[string]toolCall{}
	for _, call := range calls {
		ledger[call.CallID] = call
	}
	if len(calls) != 2 || ledger["click"].ResultBytes != int64(len("Page one\nPage two\nPage three")) || ledger["click"].Status != "ok" || ledger["click"].ResultNativeID != "q1" || ledger["click"].EndedAt != byID["q3"].CreatedAt {
		t.Fatalf("calls %+v", calls)
	}
	if ledger["run"].Status != "error" {
		t.Fatalf("the run that raised: %+v", ledger["run"])
	}
	// A result that repeats the call's, not marked as continuing it, adds
	// nothing to its output.
	repeated := append(slices.Clone(messages), MessageRecord{NativeID: "again", Role: "tool", Kind: "tool_result", CallID: "click", Text: jsonText(map[string]any{"content": "Page one again"})})
	if _, calls := buildToolLedger(repeated, ""); calls[0].ResultBytes != ledger["click"].ResultBytes {
		t.Fatalf("with a repeated result: %+v", calls[0])
	}
	// Of two results that each continue the same one, alternatives, only
	// the first is more of its output.
	continuing := func(id, text string) MessageRecord {
		return MessageRecord{NativeID: id, Role: "tool", Kind: "tool_result", CallID: "click", Text: jsonText(map[string]any{"content": text, "continues": "q3"})}
	}
	forked := append(slices.Clone(messages), continuing("x1", "More"), continuing("x2", "Other"))
	if _, calls := buildToolLedger(forked, ""); calls[0].ResultBytes != ledger["click"].ResultBytes+int64(len("\nMore")) {
		t.Fatalf("with alternative results: %+v", calls[0])
	}
	// Either the run's status or its exception tells it failed.
	run := failed["metadata"].(map[string]any)["aggregate_result"].(map[string]any)
	for _, field := range []string{"status", "in_kernel_exception"} {
		kept := run[field]
		delete(run, field)
		writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText([]any{conversation}))
		if message, _ := recordsMessage(chatGPTRecords(t, dir), "error"); !strings.Contains(message.Text, `"is_error":true`) {
			t.Fatalf("without its %s: %s", field, message.Text)
		}
		run[field] = kept
	}
}

// An export indexed before chatgpt-v2 read every branch as selected, and
// tool messages as messages; reading it again replaces that.
func TestChatGPTExportReplacesAnOlderIndex(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText(chatGPTConversations()))
	c, _ := testCatalog(t)
	older := chatGPTRecords(t, dir)[chatGPTCodeID]
	messages := []MessageRecord{}
	for _, message := range older.Conversations[0].Messages {
		if message.Kind != "reasoning" && !strings.Contains(message.NativeID, ":") {
			message.Kind, message.Selected, message.CallID = "message", true, ""
			messages = append(messages, message)
		}
	}
	older.Conversations = []ConversationRecord{older.Conversations[0]}
	older.Conversations[0].Messages = messages
	ingestRecords(t, c, older)
	ingestSource(t, c, chatGPTSource(dir))
	selected := func(id string) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id=? AND selected=1`, id)
	}
	if selected("first") != 0 || selected("answer") != 1 || countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='output' AND kind='tool_result'`) != 1 {
		t.Fatalf("the older index stays: first selected %d, answer %d", selected("first"), selected("answer"))
	}
	// A message whose content is then gone keeps what was indexed from it,
	// its attachments too.
	damaged := chatGPTConversations()
	damaged[0].(map[string]any)["mapping"].(map[string]any)["ask"].(map[string]any)["message"].(map[string]any)["content"] = nil
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText(damaged))
	ingestSource(t, c, chatGPTSource(dir))
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id IN ('ask','ask:part:0','ask:part:1')`); n != 4 {
		t.Fatalf("after its content is gone: %d of the prompt's records", n)
	}
}

// Newer exports split the conversations into numbered files, and the export
// arrives as a ZIP: each reads the same.
func TestChatGPTExportReadsShardsAndZIPs(t *testing.T) {
	conversations := chatGPTConversations()
	whole := t.TempDir()
	writeSourceFile(t, filepath.Join(whole, "conversations.json"), jsonText(conversations))
	want := chatGPTRecords(t, whole)

	sharded := t.TempDir()
	shards := map[string]string{"conversations-000.json": jsonText(conversations[:1]), "conversations-001.json": jsonText(conversations[1:]), "user.json": `{"email":"someone@example.com"}`}
	for name, content := range shards {
		writeSourceFile(t, filepath.Join(sharded, name), content)
	}
	zipped := filepath.Join(t.TempDir(), "export.zip")
	chatGPTZip(t, zipped, shards)
	single := filepath.Join(t.TempDir(), "export.zip")
	chatGPTZip(t, single, map[string]string{"conversations.json": jsonText(conversations), "chat.html": "<html></html>"})
	for name, path := range map[string]string{"shards": sharded, "ZIP of shards": zipped, "ZIP": single, "file": filepath.Join(whole, "conversations.json")} {
		got := chatGPTRecords(t, path)
		if len(got) != len(want) {
			t.Fatalf("%s: %d records, want %d", name, len(got), len(want))
		}
		for id, record := range want {
			if got := got[id].Conversations[0].Messages; len(got) != len(record.Conversations[0].Messages) {
				t.Fatalf("%s: %s has %d messages, want %d", name, id, len(got), len(record.Conversations[0].Messages))
			}
		}
	}

	// A capture holds the conversation files, or the ZIP, and indexes the
	// same elsewhere.
	for _, path := range []string{sharded, zipped} {
		useHost(t, "host-a")
		source := chatGPTSource(path)
		plan, err := planCapture(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		captured := []string{}
		for _, item := range plan.files {
			captured = append(captured, item.rel)
		}
		if want := map[string]string{sharded: "conversations-000.json,conversations-001.json", zipped: "export.zip"}[path]; strings.Join(captured, ",") != want {
			t.Fatalf("captured %v, want %s", captured, want)
		}
		c, config := testCatalog(t)
		config.Sources = []SourceConfig{source}
		ingestSource(t, c, source)
		if result := runCapture(t, config); !result.OK {
			t.Fatalf("capture %+v", result)
		}
		count := func(c *Catalog) int {
			return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='chatgpt'`)
		}
		useHost(t, "host-b")
		other, _ := testCatalog(t)
		indexCaptures(t, other, config.CaptureRoot, "host-a")
		if live, got := count(c), count(other); live == 0 || got != live {
			t.Fatalf("%s: captured %d messages, live %d", path, got, live)
		}
	}
}

// An export cut short is read as far as it goes and reported, never taken
// as whole; a conversation not in ChatGPT's shape, or a shard that can't be
// read, is reported, and the rest is read.
func TestChatGPTExportDamageIsReported(t *testing.T) {
	read := func(path string) ([]string, error) {
		adapter, err := MakeAdapter(chatGPTSource(path))
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		err = adapter.Discover(func(record WorkspaceRecord) error { ids = append(ids, record.SourceID); return nil })
		return ids, err
	}
	dir := t.TempDir()
	text := jsonText(chatGPTConversations())
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), text[:len(text)-1])
	if ids, err := read(dir); err == nil || len(ids) != 2 {
		t.Fatalf("cut short: read %v, error %v", ids, err)
	}
	conversations := chatGPTConversations()
	mistyped := map[string]any{"id": "c0000000-0000-4000-8000-000000000009", "title": "Odd", "mapping": "not a mapping"}
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText([]any{conversations[0], mistyped, map[string]any{"title": "No ID", "mapping": map[string]any{}}, conversations[1]}))
	if ids, err := read(dir); err == nil || !slices.Equal(ids, []string{chatGPTCodeID, chatGPTSearchID}) {
		t.Fatalf("mistyped: read %v, error %v", ids, err)
	}
	sharded := t.TempDir()
	writeSourceFile(t, filepath.Join(sharded, "conversations-000.json"), "[{")
	writeSourceFile(t, filepath.Join(sharded, "conversations-001.json"), jsonText(conversations))
	// A message whose content is gone leaves its conversation partial, so
	// what was indexed from it is kept.
	damaged := chatGPTConversations()
	damaged[1].(map[string]any)["mapping"].(map[string]any)["results"].(map[string]any)["message"].(map[string]any)["content"] = nil
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText(damaged))
	if search := chatGPTRecords(t, dir)[chatGPTSearchID].Conversations[0]; search.Coverage != "partial" || !slices.Equal(search.Unread, []string{"results"}) {
		t.Fatalf("without content: coverage %s, unread %v", search.Coverage, search.Unread)
	}
	if ids, err := read(sharded); err == nil || len(ids) != 2 {
		t.Fatalf("a damaged shard: read %v, error %v", ids, err)
	}
	// In a ZIP too: the first shard's own header is damaged, and the ZIP's
	// list of files is whole.
	zipped := filepath.Join(t.TempDir(), "export.zip")
	chatGPTZip(t, zipped, map[string]string{"conversations-000.json": jsonText(conversations[:1]), "conversations-001.json": jsonText(conversations[1:])})
	data, err := os.ReadFile(zipped)
	if err != nil {
		t.Fatal(err)
	}
	copy(data, "XX")
	if err := os.WriteFile(zipped, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if ids, err := read(zipped); err == nil || !slices.Equal(ids, []string{chatGPTSearchID}) {
		t.Fatalf("a damaged shard in a ZIP: read %v, error %v", ids, err)
	}
}
