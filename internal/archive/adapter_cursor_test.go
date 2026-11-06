package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cursorSecret stands for Cursor's sign-in tokens and each composer's
// encryption keys; no capture may hold it.
const cursorSecret = "cursor-secret-do-not-capture"

const (
	cursorOld    = "c0000000-0000-4000-8000-000000000001"
	cursorNew    = "c0000000-0000-4000-8000-000000000002"
	cursorChild  = "c0000000-0000-4000-8000-000000000003"
	cursorFork   = "c0000000-0000-4000-8000-000000000004"
	cursorEcho   = "task-c0000000-0000-4000-8000-000000000005"
	cursorSide   = "c0000000-0000-4000-8000-000000000006"
	cursorInline = "c0000000-0000-4000-8000-000000000007"
	cursorPrompt = "b1000000-0000-4000-8000-000000000001"
)

// cursorRow is a cursorDiskKV row: a key and its JSON value.
type cursorRow struct {
	key   string
	value any
}

// cursorComposerRows are the global database's rows in the shapes this
// Mac's holds:
//   - a composer from before Cursor 3, listing its messages itself, placed
//     by its window's list, with a request's token count on its last reply;
//   - a Cursor 3 composer, listing them in a chunk of headers and naming its
//     folder, with a subagent, a fork of itself and a side chat, its
//     encryption keys and encrypted state beside them;
//   - the copy of its subagent that the fork started from its own copy of
//     the composer's messages;
//   - a composer from releases before either, holding its messages, a
//     draft without messages, and a composer that was cleared.
func cursorComposerRows() []cursorRow {
	ms := func(offset int) int { return 1793722000000 + offset*1000 }
	tool := func(name, call, status string, fields map[string]any) map[string]any {
		data := map[string]any{"tool": "15", "name": name, "toolCallId": call, "status": status, "additionalData": map[string]any{}}
		for key, value := range fields {
			data[key] = value
		}
		return data
	}
	rows := []cursorRow{
		// A chat in fast mode, as its selected model's parameter.
		{"composerData:" + cursorOld, map[string]any{"_v": 3, "composerId": cursorOld, "name": "Fix the build", "unifiedMode": "agent", "createdAt": ms(0), "lastUpdatedAt": ms(20),
			"modelConfig": map[string]any{"modelName": "grok-4.6", "selectedModels": []any{map[string]any{"modelId": "grok-4.6", "parameters": []any{map[string]any{"id": "fast", "value": "true"}}}}},
			"fullConversationHeadersOnly": []any{map[string]any{"bubbleId": "o1", "type": 1}, map[string]any{"bubbleId": "o2", "type": 2}, map[string]any{"bubbleId": "o3", "type": 2},
				map[string]any{"bubbleId": "o4", "type": 2}, map[string]any{"bubbleId": "o5", "type": 2}}}},
		// The prompt quotes usage JSON, which is not usage.
		{"bubbleId:" + cursorOld + ":o1", map[string]any{"_v": 2, "type": 1, "bubbleId": "o1", "text": `{"usage":{"input_tokens":1000000}} Fix the build`, "tokenCount": map[string]any{"inputTokens": 0, "outputTokens": 0}}},
		{"bubbleId:" + cursorOld + ":o2", map[string]any{"_v": 2, "type": 2, "bubbleId": "o2", "thinking": map[string]any{"text": "Run make first.", "signature": ""}, "timingInfo": map[string]any{"clientRpcSendTime": ms(2)}}},
		{"bubbleId:" + cursorOld + ":o3", map[string]any{"_v": 2, "type": 2, "bubbleId": "o3", "capabilityType": 15, "timingInfo": map[string]any{"clientRpcSendTime": ms(3)},
			"toolFormerData": tool("run_terminal_cmd", "toolu_1", "completed", map[string]any{"rawArgs": `{"command":"make","is_background":false}`, "params": `{"command":"make","requireUserApproval":true}`,
				"result": `{"output":"make: *** No rule","exitCode":2,"rejected":false}`})}},
		{"bubbleId:" + cursorOld + ":o4", map[string]any{"_v": 2, "type": 2, "bubbleId": "o4", "text": "Fixed.", "timingInfo": map[string]any{"clientRpcSendTime": ms(4)},
			"tokenCount": map[string]any{"inputTokens": 4000, "outputTokens": 300}, "usageUuid": "u1"}},
		{"bubbleId:" + cursorOld + ":o5", map[string]any{"_v": 2, "type": 2, "bubbleId": "o5", "errorDetails": map[string]any{"message": "No response from model"}}},

		{"composerData:" + cursorNew, map[string]any{"_v": 18, "composerId": cursorNew, "name": "Word statistics", "unifiedMode": "agent", "createdAt": ms(100), "lastUpdatedAt": ms(110),
			"fullConversationHeadersOnly": []any{}, "headerLog": map[string]any{"chunkSize": 256, "chunkCount": 1, "count": 8},
			"modelConfig": map[string]any{"modelName": "grok-4.6"}, "contextTokensUsed": 32736, "contextTokenLimit": 256000,
			"workspaceIdentifier": map[string]any{"id": "w2", "uri": map[string]any{"$mid": 1, "fsPath": "/work/app", "external": "file:///work/app", "path": "/work/app", "scheme": "file"}},
			"subagentComposerIds": []any{cursorChild, cursorFork}, "blobEncryptionKey": cursorSecret, "speculativeSummarizationEncryptionKey": cursorSecret, "conversationState": "~" + cursorSecret}},
		{"composerConvHeaders:" + cursorNew + ":000000", []any{
			map[string]any{"bubbleId": cursorPrompt, "type": 1}, map[string]any{"bubbleId": cursorPrompt + "/s:0", "type": 2}, map[string]any{"bubbleId": cursorPrompt + "/s:1", "type": 2},
			map[string]any{"bubbleId": cursorPrompt + "/t:call-1\nfc_1", "type": 2}, map[string]any{"bubbleId": cursorPrompt + "/t:call-2\nfc_2", "type": 2},
			map[string]any{"bubbleId": cursorPrompt + "/t:call-3\nfc_3", "type": 2}, map[string]any{"bubbleId": cursorPrompt + "/t:call-4\nfc_4", "type": 2}, map[string]any{"bubbleId": cursorPrompt + "/s:7", "type": 2}}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt, map[string]any{"_v": 3, "type": 1, "text": "Write wordstats.py and test it", "createdAt": "2026-11-03T16:08:20.000Z", "modelInfo": map[string]any{"modelName": "grok-4.6"}}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/s:0", map[string]any{"_v": 3, "type": 2, "capabilityType": 30, "thinking": map[string]any{"text": "Plan the file.", "signature": ""}, "createdAt": "2026-11-03T16:08:21.000Z"}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/s:1", map[string]any{"_v": 3, "type": 2, "text": `{"usage":{"input_tokens":2000000,"output_tokens":7}}`, "createdAt": "2026-11-03T16:08:22.000Z"}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/t:call-1\nfc_1", map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:23.000Z", "startedAtMs": ms(103), "completedAtMs": ms(103) + 250,
			"toolFormerData": tool("run_terminal_command_v2", "call-1\nfc_1", "completed", map[string]any{"rawArgs": "", "params": `{"command":"python3 -m pytest -q","cwd":"/work/app"}`,
				"result": `{"output":"3 passed","exitCodeV2":0,"notInterrupted":true}`})}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/t:call-2\nfc_2", map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:24.000Z",
			"toolFormerData": tool("task_v2", "call-2\nfc_2", "completed", map[string]any{"params": `{"description":"Review","prompt":"Review wordstats.py"}`,
				"additionalData": map[string]any{"status": "success", "subagentComposerId": cursorChild}, "result": `{"agentId":"` + cursorChild + `"}`})}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/t:call-3\nfc_3", map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:25.000Z",
			"toolFormerData": tool("edit_file_v2", "call-3\nfc_3", "error", map[string]any{"params": `{"relativeWorkspacePath":".env"}`,
				"error": `{"clientVisibleErrorMessage":"Blocked","modelVisibleErrorMessage":"Editing this file is blocked by globalIgnore"}`})}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/t:call-4\nfc_4", map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:26.000Z",
			"toolFormerData": tool("task_v2", "call-4\nfc_4", "completed", map[string]any{"params": `{"description":"Check again","prompt":"Check again"}`, "additionalData": map[string]any{"subagentComposerId": cursorFork}})}},
		{"bubbleId:" + cursorNew + ":" + cursorPrompt + "/s:7", map[string]any{"_v": 3, "type": 2, "text": "There are 74 words.", "createdAt": "2026-11-03T16:08:30.000Z"}},

		// The subagent the composer started.
		{"composerData:" + cursorChild, map[string]any{"_v": 18, "composerId": cursorChild, "name": "Review", "createdAt": ms(124), "headerLog": map[string]any{"chunkCount": 1, "count": 2},
			"subagentInfo": map[string]any{"subagentType": 3, "parentComposerId": cursorNew, "toolCallId": "call-2\nfc_2", "subagentTypeName": "explore"}}},
		{"composerConvHeaders:" + cursorChild + ":000000", []any{map[string]any{"bubbleId": "s1", "type": 1}, map[string]any{"bubbleId": "s2", "type": 2}}},
		{"bubbleId:" + cursorChild + ":s1", map[string]any{"_v": 3, "type": 1, "text": "Review wordstats.py", "createdAt": "2026-11-03T16:08:24.500Z", "subagentSpawnTaskToolCallId": "call-2\nfc_2"}},
		{"bubbleId:" + cursorChild + ":s2", map[string]any{"_v": 3, "type": 2, "text": `{"usage":{"input_tokens":3000000}} Looks fine.`, "createdAt": "2026-11-03T16:08:24.800Z"}},

		// A fork: a copy of the composer's first five messages, up to the
		// call that started its subagent, then its own. Cursor holds some of
		// the copied messages.
		{"composerData:" + cursorFork, map[string]any{"_v": 18, "composerId": cursorFork, "name": "Check again", "createdAt": ms(100), "headerLog": map[string]any{"chunkCount": 1, "count": 7},
			"subagentInfo": map[string]any{"subagentType": 3, "parentComposerId": cursorNew, "forkedFromComposerId": cursorNew, "conversationLengthAtSpawn": 5, "toolCallId": "call-4\nfc_4", "subagentTypeName": "generalPurpose"}}},
		{"composerConvHeaders:" + cursorFork + ":000000", []any{map[string]any{"bubbleId": cursorPrompt, "type": 1}, map[string]any{"bubbleId": cursorPrompt + "/s:0", "type": 2},
			map[string]any{"bubbleId": cursorPrompt + "/s:1", "type": 2}, map[string]any{"bubbleId": cursorPrompt + "/t:call-1\nfc_1", "type": 2}, map[string]any{"bubbleId": cursorPrompt + "/t:call-2\nfc_2", "type": 2},
			map[string]any{"bubbleId": "f1", "type": 1}, map[string]any{"bubbleId": "f2", "type": 2}}},
		{"bubbleId:" + cursorFork + ":" + cursorPrompt, map[string]any{"_v": 3, "type": 1, "text": "Write wordstats.py and test it", "createdAt": "2026-11-03T16:08:20.000Z"}},
		{"bubbleId:" + cursorFork + ":" + cursorPrompt + "/s:0", map[string]any{"_v": 3, "type": 2, "thinking": map[string]any{"text": "Plan the file."}, "createdAt": "2026-11-03T16:08:21.000Z"}},
		{"bubbleId:" + cursorFork + ":" + cursorPrompt + "/t:call-2\nfc_2", map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:24.000Z",
			"toolFormerData": tool("task_v2", "call-2\nfc_2", "completed", map[string]any{"params": `{"description":"Review","prompt":"Review wordstats.py"}`})}},
		{"bubbleId:" + cursorFork + ":f1", map[string]any{"_v": 3, "type": 1, "text": "Check again", "createdAt": "2026-11-03T16:08:26.500Z"}},
		{"bubbleId:" + cursorFork + ":f2", map[string]any{"_v": 3, "type": 2, "text": "Still fine.", "createdAt": "2026-11-03T16:08:27.000Z"}},

		// The fork's copy of the composer's subagent.
		{"composerData:" + cursorEcho, map[string]any{"_v": 18, "composerId": cursorEcho, "name": "Review", "createdAt": ms(124), "headerLog": map[string]any{"chunkCount": 1, "count": 2},
			"subagentInfo": map[string]any{"subagentType": 3, "parentComposerId": cursorFork, "toolCallId": "call-2\nfc_2", "subagentTypeName": "explore"}}},
		{"composerConvHeaders:" + cursorEcho + ":000000", []any{map[string]any{"bubbleId": "s1", "type": 1}, map[string]any{"bubbleId": "s2", "type": 2}}},
		{"bubbleId:" + cursorEcho + ":s1", map[string]any{"_v": 3, "type": 1, "text": "Review wordstats.py", "createdAt": "2026-11-03T16:08:24.500Z"}},
		{"bubbleId:" + cursorEcho + ":s2", map[string]any{"_v": 3, "type": 2, "text": "Looks fine.", "createdAt": "2026-11-03T16:08:24.800Z"}},

		// A side chat: the person's own questions beside the composer.
		{"composerData:" + cursorSide, map[string]any{"_v": 18, "composerId": cursorSide, "name": "What does it do", "createdAt": ms(150), "headerLog": map[string]any{"chunkCount": 1, "count": 2},
			"subagentInfo": map[string]any{"subagentType": 0, "parentComposerId": cursorNew, "subagentTypeName": "side-chat"}}},
		{"composerConvHeaders:" + cursorSide + ":000000", []any{map[string]any{"bubbleId": "q1", "type": 1}, map[string]any{"bubbleId": "q2", "type": 2}}},
		{"bubbleId:" + cursorSide + ":q1", map[string]any{"_v": 3, "type": 1, "text": "What does wordstats.py do?", "createdAt": "2026-11-03T16:09:10.000Z"}},
		{"bubbleId:" + cursorSide + ":q2", map[string]any{"_v": 3, "type": 2, "text": "It counts words.", "createdAt": "2026-11-03T16:09:11.000Z"}},

		{"composerData:" + cursorInline, map[string]any{"_v": 1, "composerId": cursorInline, "createdAt": ms(-1000), "lastUpdatedAt": ms(-990), "conversation": []any{
			map[string]any{"bubbleId": "i1", "type": 1, "text": "Hello"}, map[string]any{"bubbleId": "i2", "type": 2, "text": "Hi."}}}},
		{"composerData:empty-state-draft", map[string]any{"_v": 18, "composerId": "empty-state-draft", "createdAt": ms(200), "headerLog": map[string]any{"chunkCount": 0, "count": 0}, "blobEncryptionKey": cursorSecret}},
		{"composerData:c0000000-0000-4000-8000-000000000009", nil},

		// What Cursor keeps beside them.
		{"agentKv:blob:00ff", cursorSecret},
		{"checkpointId:" + cursorNew + ":x", map[string]any{"files": cursorSecret}},
		{"messageRequestContext:" + cursorNew + ":" + cursorPrompt, map[string]any{"terminal": cursorSecret}},
	}
	return rows
}

// cursorDatabase writes a Cursor database at path: ItemTable's rows, as
// given, and cursorDiskKV's.
func cursorDatabase(t *testing.T, path string, items map[string]string, rows []cursorRow) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);
		CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);
		CREATE TABLE composerHeaders (composerId TEXT PRIMARY KEY, workspaceId TEXT, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	cursorPut(t, db, rows...)
	for key, value := range items {
		if _, err := db.Exec(`INSERT INTO ItemTable VALUES(?,?)`, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

// cursorPut writes cursorDiskKV rows: a string value as it is, a nil one as
// NULL, anything else as JSON.
func cursorPut(t *testing.T, db *sql.DB, rows ...cursorRow) {
	t.Helper()
	for _, row := range rows {
		var value any
		switch item := row.value.(type) {
		case nil:
		case string:
			value = []byte(item)
		default:
			value = jsonText(item)
		}
		if _, err := db.Exec(`INSERT INTO cursorDiskKV VALUES(?,?)`, row.key, value); err != nil {
			t.Fatal(err)
		}
	}
}

// cursorFixture writes a Cursor user folder: the global database, a window
// listing the older composer, a window without a database, and Cursor's
// settings.
func cursorFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Cursor", "User")
	cursorDatabase(t, filepath.Join(root, "globalStorage", "state.vscdb"), map[string]string{"cursorAuth/accessToken": cursorSecret, "src.vs.platform.reactivestorage": `{"key":"` + cursorSecret + `"}`}, cursorComposerRows())
	window := filepath.Join(root, "workspaceStorage", "w1")
	cursorDatabase(t, filepath.Join(window, "state.vscdb"), map[string]string{
		"composer.composerData":   `{"allComposers":[{"type":"head","composerId":"` + cursorOld + `"}],"selectedComposerIds":[]}`,
		"cursorAuth/refreshToken": cursorSecret,
	}, nil)
	writeSourceFile(t, filepath.Join(window, "workspace.json"), `{"folder":"file:///work/old"}`)
	writeSourceFile(t, filepath.Join(root, "workspaceStorage", "w2", "workspace.json"), `{"folder":"file:///work/app"}`)
	writeSourceFile(t, filepath.Join(root, "settings.json"), `{"token":"`+cursorSecret+`"}`)
	return root
}

func cursorRecords(t *testing.T, root string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

func cursorKinds(messages []MessageRecord) string {
	out := []string{}
	for _, message := range messages {
		out = append(out, message.Role+":"+message.Kind)
	}
	return strings.Join(out, " ")
}

func TestCursorComposerKeepsItsTurnsToolsAndSubagents(t *testing.T) {
	records := cursorRecords(t, cursorFixture(t))
	if len(records) != 3 {
		t.Fatalf("records = %v", records)
	}
	old := records[cursorOld]
	if old.SourceKind != "cursor" || old.Title != "Fix the build" || old.Location != "/work/old" || old.Metadata["mode"] != "agent" || len(old.Conversations) != 1 {
		t.Fatalf("older composer = %+v", old)
	}
	conversation := old.Conversations[0]
	if conversation.Provider != "cursor" || conversation.Harness != "cursor" || conversation.Coverage != "complete" || conversation.StartedAt != "2026-11-03T16:06:40.000Z" || conversation.EndedAt != "2026-11-03T16:07:00.000Z" {
		t.Fatalf("older conversation = %+v", conversation)
	}
	if got, want := cursorKinds(conversation.Messages), "user:message assistant:reasoning assistant:tool_call tool:tool_result assistant:message system:metadata system:error"; got != want {
		t.Fatalf("older messages\n got %s\nwant %s", got, want)
	}
	byID := map[string]MessageRecord{}
	for _, message := range conversation.Messages {
		byID[message.NativeID] = message
	}
	// A message without a time of its own takes the one before it.
	if prompt := byID["o1"]; prompt.CreatedAt != "2026-11-03T16:06:40.000Z" || byID["o2:thinking"].CreatedAt != "2026-11-03T16:06:42.000Z" || byID["o5:error"].CreatedAt != "2026-11-03T16:06:44.000Z" {
		t.Fatalf("times %+v %+v", prompt, byID["o5:error"])
	}
	if call, result := byID["o3:call"], byID["o3:result"]; call.CallID != "toolu_1" || call.Text != `{"input":{"command":"make","is_background":false},"tool":"run_terminal_cmd"}` ||
		result.Text != `{"content":"make: *** No rule","details":{"exit_code":2},"is_error":false,"tool":"run_terminal_cmd"}` {
		t.Fatalf("call %+v result %+v", call, result)
	}
	if usage := byID["o4:usage"]; usage.Text != `{"type":"token_usage","usage":{"input_tokens":4000,"output_tokens":300},"usage_message_id":"o4"}` || usage.RawText != "" {
		t.Fatalf("usage = %+v", usage)
	}

	composer := records[cursorNew]
	if composer.Title != "Word statistics" || composer.Location != "/work/app" || len(composer.Conversations) != 4 {
		t.Fatalf("composer = %+v", composer)
	}
	main := composer.Conversations[0]
	if main.Model != "grok-4.6" || main.NativeContext["context_tokens_used"] != float64(32736) || main.EndedAt != "2026-11-03T16:08:30.000Z" {
		t.Fatalf("main = %+v", main)
	}
	if got, want := cursorKinds(main.Messages), "user:message assistant:reasoning assistant:message assistant:tool_call tool:tool_result assistant:delegation tool:delegation_result assistant:tool_call tool:tool_result assistant:delegation tool:delegation_result assistant:message"; got != want {
		t.Fatalf("messages\n got %s\nwant %s", got, want)
	}
	byID = map[string]MessageRecord{}
	for _, message := range main.Messages {
		byID[message.NativeID] = message
	}
	command := cursorPrompt + "/t:call-1\nfc_1"
	if call, result := byID[command+":call"], byID[command+":result"]; call.CallID != "call-1\nfc_1" || call.Text != `{"input":{"command":"python3 -m pytest -q","cwd":"/work/app"},"tool":"run_terminal_command_v2"}` ||
		result.Text != `{"content":"3 passed","details":{"duration_ms":250,"exit_code":0},"is_error":false,"tool":"run_terminal_command_v2"}` || result.CreatedAt != "2026-11-03T16:08:23.250Z" {
		t.Fatalf("command %+v result %+v", call, result)
	}
	if result := byID[cursorPrompt+"/t:call-3\nfc_3:result"]; result.Text != `{"content":"Editing this file is blocked by globalIgnore","details":{"error":"Editing this file is blocked by globalIgnore"},"is_error":true,"tool":"edit_file_v2"}` {
		t.Fatalf("failed edit = %+v", result)
	}
	if result := byID[cursorPrompt+"/t:call-2\nfc_2:result"]; !strings.Contains(result.Text, `"subagent":"`+cursorChild+`"`) {
		t.Fatalf("delegation result = %+v", result)
	}
	// The subagent, with its prompt its parent's; the fork, without the copy
	// of its parent's messages; not the fork's copy of the subagent; the side
	// chat, with the person's own prompt.
	children := map[string]ConversationRecord{}
	for _, child := range composer.Conversations[1:] {
		children[child.NativeID] = child
	}
	if child := children[cursorChild]; child.ParentNativeID != cursorNew || child.AgentDepth != 1 || child.AgentPath != "explore" || child.AgentNickname != "Review" ||
		cursorKinds(child.Messages) != "agent:message assistant:message" || child.Messages[0].Sender != "agent:"+cursorNew {
		t.Fatalf("subagent = %+v", child)
	}
	if fork := children[cursorFork]; fork.ParentNativeID != cursorNew || fork.AgentDepth != 1 || cursorKinds(fork.Messages) != "agent:message assistant:message" || fork.Messages[0].Text != "Check again" || fork.NativeContext["forked_from"] != cursorNew {
		t.Fatalf("fork = %+v", fork)
	}
	if side := children[cursorSide]; side.ParentNativeID != cursorNew || side.AgentDepth != 0 || side.AgentPath != "side-chat" || cursorKinds(side.Messages) != "user:message assistant:message" {
		t.Fatalf("side chat = %+v", side)
	}
	if _, echoed := children[cursorEcho]; echoed {
		t.Fatal("the fork's copy of the subagent is read")
	}
	if channel, _ := harnessChannel(main.Harness, composer.SourceKind); channel != "ide" {
		t.Fatalf("channel = %q", channel)
	}
	if inline := records[cursorInline]; inline.Title != "Hello" || inline.Location != "" || cursorKinds(inline.Conversations[0].Messages) != "user:message assistant:message" {
		t.Fatalf("composer holding its messages = %+v", inline)
	}
}

func TestCursorUsageCountsEachRequestOnce(t *testing.T) {
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "cursor", Kind: "cursor", Path: cursorFixture(t), Account: "local", Enabled: true})
	// The older composer's one token count; nothing from the JSON a prompt
	// or reply quotes, and nothing for Cursor 3, which keeps none.
	if total := countRows(t, c, `SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage`); total != 4300 {
		t.Fatalf("usage %d tokens", total)
	}
	// and was served in the fast mode its chat is set to.
	if fast := countRows(t, c, `SELECT COUNT(*) FROM model_requests WHERE speed='fast' AND speed_source='speed'`); fast != 1 {
		t.Fatalf("%d fast requests", fast)
	}
	rows, err := c.DB.Query(`SELECT call_id, tool_category, COALESCE(program,''), COALESCE(exit_code,-1) FROM tool_calls WHERE provider='cursor' ORDER BY call_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var id, category, program string
		var exit int
		if err := rows.Scan(&id, &category, &program, &exit); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%s:%s:%d", strings.ReplaceAll(id, "\n", "|"), category, program, exit))
	}
	if strings.Join(got, " ") != "call-1|fc_1=command:python3:0 call-2|fc_2=agent::-1 call-3|fc_3=edit::-1 call-4|fc_4=agent::-1 toolu_1=command:make:2" {
		t.Fatalf("calls: %v", got)
	}
}

// A capture holds the composers, their messages and the windows' lists of
// them, not Cursor's sign-in tokens, settings, other rows or a composer's
// encryption keys. Another Mac indexes the same messages from it, as from
// rows copied before indexing.
func TestCursorCaptureHoldsOnlyTheChats(t *testing.T) {
	useHost(t, "host-a")
	root := cursorFixture(t)
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	plan, err := planCapture(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range append(plan.databases, plan.files...) {
		got = append(got, item.rel)
	}
	if strings.Join(got, ",") != "globalStorage/state.vscdb,workspaceStorage/w1/state.vscdb,workspaceStorage/w1/workspace.json" {
		t.Fatalf("capture plan = %v", got)
	}
	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	// A capture stopped as a snapshot is written, before it is kept, leaves
	// nothing behind it should not hold.
	stopped := 0
	override(t, &captureFault, func(stage, rel string) error {
		if stage != "snapshot-written" {
			return nil
		}
		stopped++
		if strings.Contains(capturedText(t, config.CaptureRoot), cursorSecret) {
			t.Errorf("a capture stopped at %s holds Cursor's credentials or keys", rel)
		}
		return errors.New("stopped")
	})
	runCapture(t, config)
	captureFault = nil
	if stopped == 0 {
		t.Fatal("no snapshot was written")
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	for _, rel := range []string{"files/globalStorage/state.vscdb", "files/workspaceStorage/w1/state.vscdb"} {
		db, err := openReadOnlySQLite(capturedPath(config, "cursor", rel))
		if err != nil {
			t.Fatal(err)
		}
		tables, err := tableNames(db)
		var keys []string
		if err == nil {
			rows, _ := queryMapsContext(t.Context(), db, `SELECT key FROM ItemTable UNION ALL SELECT substr(key, 1, instr(key, ':')) FROM cursorDiskKV`)
			for _, row := range rows {
				keys = append(keys, firstString(row["key"]))
			}
		}
		db.Close()
		for table := range tables {
			if _, kept := cursorCaptureTables[table]; !kept {
				err = errors.Join(err, fmt.Errorf("captured %s", table))
			}
		}
		for _, key := range keys {
			if key != "composer.composerData" && key != "composerData:" && key != "bubbleId:" && key != "composerConvHeaders:" {
				err = errors.Join(err, fmt.Errorf("captured %s", key))
			}
		}
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	if strings.Contains(capturedText(t, config.CaptureRoot), cursorSecret) {
		t.Fatal("the capture holds Cursor's credentials or keys")
	}
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='cursor'`)
	}
	locations := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE location IN ('/work/old','/work/app')`)
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 || locations(other) != 2 {
		t.Fatalf("captured %d messages (%d placed), live %d", captured, locations(other), live)
	}

	// With rows exported from each snapshot as well: they are the scrubbed
	// snapshot's.
	useHost(t, "host-a")
	_, recordedConfig := testCatalog(t)
	source.Options = map[string]any{"sqlite_record_capture": true}
	recordedConfig.Sources = []SourceConfig{source}
	if result := runCapture(t, recordedConfig); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if strings.Contains(capturedText(t, recordedConfig.CaptureRoot), cursorSecret) {
		t.Fatal("the rows exported from a capture hold Cursor's credentials or keys")
	}
	useHost(t, "host-b")
	exported, _ := testCatalog(t)
	indexCaptures(t, exported, recordedConfig.CaptureRoot, "host-a")
	if got := messages(exported); got != live {
		t.Fatalf("from a capture with rows exported: %d messages, live %d", got, live)
	}
}

// cursorWrite changes the fixture's global database.
func cursorWrite(t *testing.T, root string, write func(db *sql.DB) error) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "globalStorage", "state.vscdb"))
	if err == nil {
		err = write(db)
		db.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestCursorReadsOnlyTheComposerThatChanged(t *testing.T) {
	c, _ := testCatalog(t)
	root := cursorFixture(t)
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	if parsed := ingestSource(t, c, source).Parsed; parsed != 0 {
		t.Fatalf("unchanged: %d composers parsed", parsed)
	}
	// A new message, listed in the composer's own headers.
	cursorWrite(t, root, func(db *sql.DB) error {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV VALUES(?, ?)`, "bubbleId:"+cursorOld+":o6", `{"_v":2,"type":2,"text":"Also cleaned up."}`); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=json_insert(value, '$.fullConversationHeadersOnly[#]', json('{"bubbleId":"o6","type":2}')) WHERE key=?`, "composerData:"+cursorOld)
		return err
	})
	parsed := ingestSource(t, c, source).Parsed
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='o6'`); n != 1 || parsed != 1 {
		t.Fatalf("after a new message: %d copies, %d composers parsed", n, parsed)
	}
	// A message changing alone, as a running call's does when it finishes.
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=json_set(value, '$.text', 'Also cleaned up the tests.') WHERE key=?`, "bubbleId:"+cursorOld+":o6")
		return err
	})
	parsed = ingestSource(t, c, source).Parsed
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='o6' AND text='Also cleaned up the tests.'`); n != 1 || parsed != 1 {
		t.Fatalf("after a message changed: %d, %d composers parsed", n, parsed)
	}
	// A change that keeps every size: a request's token count, and an exit
	// code.
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(replace(value, '"outputTokens":300', '"outputTokens":900'), '"exitCode":2', '"exitCode":0') WHERE key IN (?, ?)`, "bubbleId:"+cursorOld+":o4", "bubbleId:"+cursorOld+":o3")
		return err
	})
	parsed = ingestSource(t, c, source).Parsed
	if total := countRows(t, c, `SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage`); total != 4900 || parsed != 1 {
		t.Fatalf("after a same-size change: %d tokens, %d composers parsed", total, parsed)
	}
}

// A message Cursor no longer holds, though the composer's headers name it,
// stays as it was indexed; the others are read as they are now.
func TestCursorMessageCursorDroppedStaysIndexed(t *testing.T) {
	c, _ := testCatalog(t)
	root := cursorFixture(t)
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	before := countRows(t, c, `SELECT COUNT(*) FROM messages`)
	// o4 is gone; o2, read again, is a reply with usage of its own.
	cursorWrite(t, root, func(db *sql.DB) error {
		if _, err := db.Exec(`UPDATE cursorDiskKV SET value=NULL WHERE key=?`, "bubbleId:"+cursorOld+":o4"); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=? WHERE key=?`, `{"_v":2,"type":2,"bubbleId":"o2","text":"Run make first.","tokenCount":{"inputTokens":60,"outputTokens":40}}`, "bubbleId:"+cursorOld+":o2")
		return err
	})
	ingestSource(t, c, source)
	if after := countRows(t, c, `SELECT COUNT(*) FROM messages`); after != before+1 || countRows(t, c, `SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage`) != 4400 {
		t.Fatalf("messages %d, before %d", after, before)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE native_id=? AND coverage='partial'`, cursorOld); n != 1 {
		t.Fatalf("the conversation is not partial: %d", n)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages a JOIN messages b ON b.conversation_id=a.conversation_id
		WHERE a.native_id='o4' AND b.native_id LIKE 'o5%' AND a.source_order>=b.source_order`); n != 0 {
		t.Fatalf("the dropped message moved after %d later ones", n)
	}
	// What a message read again no longer holds is removed.
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='o2:thinking'`); n != 0 {
		t.Fatalf("o2's old thinking is kept")
	}
	if n := countRows(t, c, `SELECT COALESCE(SUM(m.value),0) FROM metrics m JOIN workspaces w ON w.id=m.workspace_id WHERE w.source_id=? AND m.unit='tokens' AND m.name='total_tokens'`, cursorOld); n != 4400 {
		t.Fatalf("the workspace counts %d tokens", n)
	}
	// A delivery checkpoint matches the conversation as Cursor gives it.
	var workspace, fingerprint string
	if err := c.DB.QueryRow(`SELECT v.workspace_id, i.fingerprint FROM conversations v JOIN conversation_derivation_inputs i ON i.conversation_id=v.id WHERE v.native_id=?`, cursorOld).Scan(&workspace, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if read := cursorRecords(t, root)[cursorOld].Conversations[0]; fingerprint != conversationDeliveryDigest(workspace, read) {
		t.Fatalf("the delivery fingerprint is not the read's")
	}
}

// A fork of a fork drops the copy of its parent's messages, which begin
// with its grandparent's.
func TestCursorForkOfAForkDropsWhatItCopied(t *testing.T) {
	root := cursorFixture(t)
	const nested = "c0000000-0000-4000-8000-000000000008"
	cursorWrite(t, root, func(db *sql.DB) error {
		cursorPut(t, db,
			cursorRow{"composerData:" + nested, map[string]any{"_v": 18, "composerId": nested, "name": "Once more", "createdAt": 1793722100000, "headerLog": map[string]any{"chunkCount": 1, "count": 7},
				"subagentInfo": map[string]any{"subagentType": 3, "parentComposerId": cursorFork, "forkedFromComposerId": cursorFork, "conversationLengthAtSpawn": 6, "toolCallId": "call-9"}}},
			cursorRow{"composerConvHeaders:" + nested + ":000000", []any{map[string]any{"bubbleId": cursorPrompt}, map[string]any{"bubbleId": cursorPrompt + "/s:0"}, map[string]any{"bubbleId": cursorPrompt + "/s:1"},
				map[string]any{"bubbleId": cursorPrompt + "/t:call-1\nfc_1"}, map[string]any{"bubbleId": cursorPrompt + "/t:call-2\nfc_2"}, map[string]any{"bubbleId": "f1"}, map[string]any{"bubbleId": "g1"}}},
			cursorRow{"bubbleId:" + nested + ":" + cursorPrompt, map[string]any{"type": 1, "text": "Write wordstats.py and test it"}},
			cursorRow{"bubbleId:" + nested + ":" + cursorPrompt + "/s:0", map[string]any{"type": 2, "thinking": map[string]any{"text": "Plan the file."}}},
			cursorRow{"bubbleId:" + nested + ":f1", map[string]any{"type": 1, "text": "Check again"}},
			cursorRow{"bubbleId:" + nested + ":g1", map[string]any{"type": 2, "text": "Once more, fine."}})
		return nil
	})
	for _, conversation := range cursorRecords(t, root)[cursorNew].Conversations {
		if conversation.NativeID == nested && (conversation.AgentDepth != 2 || cursorKinds(conversation.Messages) != "assistant:message" || conversation.Coverage != "complete") {
			t.Fatalf("fork of a fork = %+v", conversation)
		}
	}
}

// The ledger reads what Cursor's commands printed, a cancelled or declined
// call as not run to completion, and Cursor's file arguments; an older
// composer's messages in conversationMap are read.
func TestCursorToolResultsAndLegacyComposers(t *testing.T) {
	root := cursorFixture(t)
	const legacy = "c0000000-0000-4000-8000-00000000000b"
	tool := func(name, call, status, params, result string) map[string]any {
		return map[string]any{"tool": "15", "name": name, "toolCallId": call, "status": status, "params": params, "result": result, "additionalData": map[string]any{}}
	}
	cursorWrite(t, root, func(db *sql.DB) error {
		cursorPut(t, db, cursorRow{"composerData:" + legacy, map[string]any{"_v": 2, "composerId": legacy, "name": "Legacy", "createdAt": 1793721000000, "lastUpdatedAt": 1793721005000,
			"fullConversationHeadersOnly": []any{map[string]any{"bubbleId": "l1", "type": 1}, map[string]any{"bubbleId": "l2", "type": 2}, map[string]any{"bubbleId": "l3", "type": 2},
				map[string]any{"bubbleId": "l4", "type": 2}, map[string]any{"bubbleId": "l5", "type": 2}, map[string]any{"bubbleId": "l6", "type": 2}},
			"conversationMap": map[string]any{
				"l1": map[string]any{"type": 1, "text": "Read main.py and test it"},
				"l2": map[string]any{"type": 2, "capabilityType": 15, "toolFormerData": tool("read_file_v2", "lc1", "completed", `{"targetFile":"/work/app/main.py"}`, "")},
				"l3": map[string]any{"type": 2, "capabilityType": 15, "toolFormerData": tool("run_terminal_command_v2", "lc2", "cancelled", `{"command":"sleep 100"}`, "")},
				"l4": map[string]any{"type": 2, "capabilityType": 15, "toolFormerData": tool("run_terminal_command_v2", "lc3", "completed", `{"command":"python3 -m pytest -q"}`,
					`{"output":"F.\n1 failed, 1 passed","exitCodeV2":0,"notInterrupted":true}`)},
				"l5": map[string]any{"type": 2, "capabilityType": 15, "toolFormerData": tool("run_terminal_command_v2", "lc4", "completed", `{"command":"rm -rf build"}`, `{"output":"","rejected":true}`)},
				"l6": map[string]any{"type": 2, "capabilityType": 15, "toolFormerData": tool("run_terminal_command_v2", "lc5", "completed", `{"command":"npm run dev"}`, `{"output":"ready","notInterrupted":false}`)}}}})
		return nil
	})
	if conversation := cursorRecords(t, root)[legacy].Conversations[0]; conversation.Coverage != "complete" || conversation.Messages[0].Text != "Read main.py and test it" {
		t.Fatalf("legacy composer = %+v", conversation)
	}
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true})
	rows, err := c.DB.Query(`SELECT call_id, status, interrupted, test_failure, COALESCE(file_path,''), result_bytes > 0 FROM tool_calls WHERE provider='cursor' AND call_id IN ('lc1','lc2','lc3','lc4','lc5','call-1'||char(10)||'fc_1','call-3'||char(10)||'fc_3') ORDER BY call_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var id, status, path string
		var interrupted, failure, output bool
		if err := rows.Scan(&id, &status, &interrupted, &failure, &path, &output); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%s:%v:%v:%s:%v", strings.ReplaceAll(id, "\n", "|"), status, interrupted, failure, path, output))
	}
	if want := "call-1|fc_1=ok:false:false::true call-3|fc_3=error:false:false:.env:true lc1=ok:false:false:/work/app/main.py:false lc2=error:true:false::false lc3=ok:false:true::true lc4=error:false:false::false lc5=error:true:false::true"; strings.Join(got, " ") != want {
		t.Fatalf("calls\n got %v\nwant %s", strings.Join(got, " "), want)
	}
}

// A chunk of a fork's headers that is lost, damaged or null leaves the rest of
// its group read, and the fork's own messages its own: what it copied can
// no longer be told by position.
func TestCursorLostHeaderChunkLeavesTheGroup(t *testing.T) {
	for name, damage := range map[string]string{"lost": `DELETE FROM cursorDiskKV WHERE key=?`, "damaged": `UPDATE cursorDiskKV SET value='[{"bubbleId":' WHERE key=?`, "null": `UPDATE cursorDiskKV SET value='null' WHERE key=?`} {
		t.Run(name, func(t *testing.T) {
			root := cursorFixture(t)
			cursorWrite(t, root, func(db *sql.DB) error {
				// The fork's headers in two chunks, the first lost or damaged,
				// and its parent deleted.
				var headers string
				if err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key=?`, "composerConvHeaders:"+cursorFork+":000000").Scan(&headers); err != nil {
					return err
				}
				var list []any
				if err := json.Unmarshal([]byte(headers), &list); err != nil {
					return err
				}
				cursorPut(t, db, cursorRow{"composerConvHeaders:" + cursorFork + ":000000", list[:2]}, cursorRow{"composerConvHeaders:" + cursorFork + ":000001", list[2:]})
				if _, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, '"chunkCount":1', '"chunkCount":2') WHERE key=?`, "composerData:"+cursorFork); err != nil {
					return err
				}
				if _, err := db.Exec(damage, "composerConvHeaders:"+cursorFork+":000000"); err != nil {
					return err
				}
				_, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+cursorNew+":*", "composerData:"+cursorNew)
				return err
			})
			conversations := map[string]ConversationRecord{}
			for _, conversation := range cursorRecords(t, root)[cursorNew].Conversations {
				conversations[conversation.NativeID] = conversation
			}
			fork := conversations[cursorFork]
			if fork.Coverage != "partial" || !slices.ContainsFunc(fork.Messages, func(message MessageRecord) bool { return message.Text == "Still fine." }) {
				t.Fatalf("fork = %s %+v", fork.Coverage, fork.Messages)
			}
			if _, ok := conversations[cursorSide]; !ok {
				t.Fatalf("the rest of the group = %v", slices.Collect(maps.Keys(conversations)))
			}
		})
	}
}

// A message a composer's headers name that is not there makes it partial,
// and a composer that is not JSON is reported; the others are read.
func TestCursorDamagedComposerLeavesTheOthers(t *testing.T) {
	root := cursorFixture(t)
	cursorWrite(t, root, func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key=?`, "bubbleId:"+cursorOld+":o4"); err != nil {
			return err
		}
		_, err := db.Exec(`INSERT INTO cursorDiskKV VALUES('composerData:c0000000-0000-4000-8000-00000000000a', '{"_v":18,')`)
		return err
	})
	adapter, _ := MakeAdapter(SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local"})
	records := map[string]WorkspaceRecord{}
	err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil })
	if !strings.Contains(fmt.Sprint(err), "c0000000-0000-4000-8000-00000000000a") {
		t.Fatalf("the damaged composer is not reported: %v", err)
	}
	if len(records) != 3 || records[cursorOld].Conversations[0].Coverage != "partial" || records[cursorNew].Conversations[0].Coverage != "complete" {
		t.Fatalf("records = %+v", records)
	}
}

// A composer deleted in Cursor between two captures, before either was
// indexed, is indexed from the older one: one that started its own work, and
// one whose composer remains.
func TestCursorComposerDeletedBeforeIndexingIsKept(t *testing.T) {
	useHost(t, "host-a")
	root := cursorFixture(t)
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	runCapture(t, config)
	// A composer, a subagent whose composer remains, and a fork with the
	// copy of a subagent it re-created.
	cursorWrite(t, root, func(db *sql.DB) error {
		for _, id := range []string{cursorOld, cursorSide, cursorFork, cursorEcho} {
			if _, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+id+":*", "composerData:"+id); err != nil {
				return err
			}
		}
		return nil
	})
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ := testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	// Each is read as it was, with what it copied left to its composer.
	if got, want := cursorShape(t, c), cursorShape(t, cursorLive(t)); got != want {
		t.Fatalf("salvaged %s, read live %s", got, want)
	}
	// Placed as it was, by its window, and the side chat by its composer.
	if n := countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE source_id=? AND location='/work/old'`, cursorOld); n != 1 {
		t.Fatalf("the older composer lost its folder")
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE native_id=? AND parent_id IS NOT NULL`, cursorSide); n != 1 {
		t.Fatalf("the side chat lost its composer")
	}
}

// cursorLive is a catalog that indexed the fixture as it is.
func cursorLive(t *testing.T) *Catalog {
	live, _ := testCatalog(t)
	ingestSource(t, live, SourceConfig{Name: "cursor", Kind: "cursor", Path: cursorFixture(t), Account: "local", Enabled: true})
	return live
}

// cursorShape lists each conversation with its number of messages.
func cursorShape(t *testing.T, c *Catalog) (shape string) {
	t.Helper()
	if err := c.DB.QueryRow(`SELECT group_concat(native_id||'='||n, ' ') FROM (SELECT v.native_id, COUNT(m.id) n
		FROM conversations v LEFT JOIN messages m ON m.conversation_id=v.id GROUP BY 1 ORDER BY 1)`).Scan(&shape); err != nil {
		t.Fatal(err)
	}
	return shape
}

// A composer deleted once its newer copy was indexed is not rolled back to
// the copy an older capture holds.
func TestCursorSalvageLeavesANewerCopy(t *testing.T) {
	useHost(t, "host-a")
	root := cursorFixture(t)
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	config := captureTestConfig(t, source)
	runCapture(t, config)
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, 'Looks fine.', 'Looks fine now.') WHERE key=?`, "bubbleId:"+cursorChild+":s2")
		return err
	})
	c, _ := testCatalog(t)
	ingestSource(t, c, source)
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+cursorChild+":*", "composerData:"+cursorChild)
		return err
	})
	runCapture(t, config)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id=? AND m.text LIKE '%Looks fine now.'`, cursorChild); n != 1 {
		t.Fatalf("the subagent was rolled back: %d", n)
	}

	// Nor is a subagent whose composer was deleted while it went on: it
	// stays in its composer's workspace, which the older capture's copy of
	// both is checked against.
	root = cursorFixture(t)
	source.Path = root
	config = captureTestConfig(t, source)
	runCapture(t, config)
	cursorWrite(t, root, func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+cursorNew+":*", "composerData:"+cursorNew); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, 'Looks fine.', 'Looks fine now.') WHERE key=?`, "bubbleId:"+cursorChild+":s2")
		return err
	})
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ = testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id=? AND m.text LIKE '%Looks fine now.'`, cursorChild); n != 1 {
		t.Fatalf("the subagent of a deleted composer was rolled back: %d", n)
	}
	if n := countRows(t, c, `SELECT COUNT(DISTINCT workspace_id) FROM conversations WHERE native_id IN (?, ?)`, cursorNew, cursorChild); n != 1 {
		t.Fatalf("the subagent left its composer's workspace: %d", n)
	}
	// Its fork still leaves out what it copied, the copies of subagents are
	// still left out, and the work is not named for a subagent.
	live := cursorLive(t)
	if got, want := cursorShape(t, c), cursorShape(t, live); got != want {
		t.Fatalf("after the composer was deleted %s, read live %s", got, want)
	}
	purpose := `SELECT COALESCE(purpose,'') || ' / ' || COALESCE((SELECT display_title FROM workspace_names WHERE workspace_id=w.id),'') FROM workspaces w WHERE source_id=?`
	var got, want string
	if err := c.DB.QueryRow(purpose, cursorNew).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if err := live.DB.QueryRow(purpose, cursorNew).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(want, strings.SplitN(got, " / ", 2)[0]+" / ") || strings.HasSuffix(got, " / Review") {
		t.Fatalf("the work is %q, read live %q", got, want)
	}

	// Nor is a subagent whose composer became another's subagent, so that
	// the group is another's when the older capture is read.
	const top = "c0000000-0000-4000-8000-000000000009"
	root = cursorFixture(t)
	source.Path = root
	config = captureTestConfig(t, source)
	useHost(t, "host-a")
	runCapture(t, config)
	cursorWrite(t, root, func(db *sql.DB) error {
		cursorPut(t, db, cursorRow{"composerData:" + top, map[string]any{"_v": 18, "composerId": top, "name": "Above", "createdAt": 1793722090000, "headerLog": map[string]any{"chunkCount": 1, "count": 1}}},
			cursorRow{"composerConvHeaders:" + top + ":000000", []any{map[string]any{"bubbleId": "a1", "type": 1}}},
			cursorRow{"bubbleId:" + top + ":a1", map[string]any{"_v": 3, "type": 1, "text": "Start"}})
		if _, err := db.Exec(`UPDATE cursorDiskKV SET value=json_set(value, '$.subagentInfo', json_object('parentComposerId', ?)) WHERE key=?`, top, "composerData:"+cursorNew); err != nil {
			return err
		}
		if _, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+cursorSide+":*", "composerData:"+cursorSide); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, 'Looks fine.', 'Looks fine now.') WHERE key=?`, "bubbleId:"+cursorChild+":s2")
		return err
	})
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ = testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id=? AND m.text LIKE '%Looks fine now.'`, cursorChild); n != 1 {
		t.Fatalf("a subagent regrouped since was rolled back: %d", n)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE native_id=?`, cursorSide); n != 1 {
		t.Fatalf("the deleted side chat was not kept: %d", n)
	}
	// Its workspace counts only the side chat, which it holds.
	if n := countRows(t, c, `SELECT COALESCE(SUM(m.value),0) FROM metrics m JOIN workspaces w ON w.id=m.workspace_id WHERE w.source_id=? AND m.name='tool_calls'`, cursorNew); n != 0 {
		t.Fatalf("the side chat's workspace counts %d tool calls", n)
	}

	// A newer capture still replaces a conversation another host moved
	// after its host indexed it.
	root = cursorFixture(t)
	source.Path = root
	config = captureTestConfig(t, source)
	useHost(t, "host-a")
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ = testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if _, err := c.DB.Exec(`UPDATE conversations SET workspace_id=(SELECT id FROM workspaces WHERE source_id=?) WHERE native_id=?`, cursorOld, cursorChild); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`UPDATE conversation_sightings SET last_seen_at='2100-01-01T00:00:00Z' WHERE conversation_id=(SELECT id FROM conversations WHERE native_id=?)`, cursorChild); err != nil {
		t.Fatal(err)
	}
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, 'Looks fine.', 'Looks fine now.') WHERE key=?`, "bubbleId:"+cursorChild+":s2")
		return err
	})
	useHost(t, "host-a")
	runCapture(t, config)
	useHost(t, "host-b")
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id=? AND m.text LIKE '%Looks fine now.'`, cursorChild); n != 1 {
		t.Fatalf("a moved subagent's newer capture was refused: %d", n)
	}
}

// A fork's copy of a parent Cursor no longer holds is that parent's only
// copy, and a re-created subagent is the only copy of one Cursor deleted.
func TestCursorForkKeepsWhatOnlyItHolds(t *testing.T) {
	root := cursorFixture(t)
	// A second, newer fork holds a copied message the first does not, and
	// the subagent's call as it finished.
	const second = "c0000000-0000-4000-8000-00000000000a"
	cursorWrite(t, root, func(db *sql.DB) error {
		var headers string
		if err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key=?`, "composerConvHeaders:"+cursorFork+":000000").Scan(&headers); err != nil {
			return err
		}
		cursorPut(t, db, cursorRow{"composerData:" + second, map[string]any{"_v": 18, "composerId": second, "name": "Again", "createdAt": 1793722101000, "headerLog": map[string]any{"chunkCount": 1, "count": 7},
			"subagentInfo": map[string]any{"subagentType": 3, "parentComposerId": cursorNew, "forkedFromComposerId": cursorNew, "conversationLengthAtSpawn": 5, "toolCallId": "call-8"}}},
			cursorRow{"bubbleId:" + second + ":" + cursorPrompt + "/s:1", map[string]any{"_v": 3, "type": 2, "text": "Counting words.", "createdAt": "2026-11-03T16:08:22.000Z"}},
			cursorRow{"bubbleId:" + second + ":" + cursorPrompt + "/t:call-2\nfc_2", map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:24.000Z",
				"toolFormerData": map[string]any{"tool": "15", "name": "task_v2", "toolCallId": "call-2\nfc_2", "status": "completed", "additionalData": map[string]any{},
					"params": `{"description":"Review","prompt":"Review wordstats.py"}`, "result": `{"agentId":"review-finished"}`}}})
		_, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "composerConvHeaders:"+second+":000000", headers)
		return err
	})
	// The parent was indexed, and its thinking changed since its forks copied it.
	c, _ := testCatalog(t)
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, 'Plan the file.', 'Plan the file well.') WHERE key=?`, "bubbleId:"+cursorNew+":"+cursorPrompt+"/s:0")
		return err
	})
	ingestSource(t, c, source)
	before := countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id=?`, cursorNew)
	cursorWrite(t, root, func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+cursorNew+":*", "composerData:"+cursorNew); err != nil {
			return err
		}
		_, err := db.Exec(`UPDATE cursorDiskKV SET value=replace(value, 'Still fine.', 'Still fine, twice.') WHERE key=?`, "bubbleId:"+cursorFork+":f2")
		return err
	})
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='Still fine, twice.'`); n != 1 {
		t.Fatalf("the group was not read again: %d", n)
	}
	if after := countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id=? AND (m.text NOT LIKE '%Plan the file.%')`, cursorNew); after != before {
		t.Fatalf("the parent indexed before holds %d of its %d messages as it read them", after, before)
	}
	conversations := map[string]ConversationRecord{}
	for _, conversation := range cursorRecords(t, root)[cursorNew].Conversations {
		conversations[conversation.NativeID] = conversation
	}
	parent, ids := conversations[cursorNew], map[string]bool{}
	for _, message := range parent.Messages {
		ids[message.NativeID] = true
	}
	finished := slices.ContainsFunc(parent.Messages, func(message MessageRecord) bool { return strings.Contains(message.Text, "review-finished") })
	if !finished || parent.Observed != 1793722101000*1e6 || !parent.Copy || parent.Coverage != "partial" || len(parent.Messages) == 0 || parent.Messages[0].NativeID != cursorPrompt || !ids[cursorPrompt+"/s:1"] || !ids[cursorPrompt+"/s:0:thinking"] {
		t.Fatalf("the deleted parent = %+v", parent)
	}
	if fork := conversations[cursorFork]; cursorKinds(fork.Messages) != "agent:message assistant:message" {
		t.Fatalf("the fork holds %s", cursorKinds(fork.Messages))
	}
	if _, ok := conversations[cursorEcho]; ok {
		t.Fatal("the copy of a subagent Cursor still holds is read")
	}

	root = cursorFixture(t)
	cursorWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM cursorDiskKV WHERE key GLOB ? OR key=?`, "*:"+cursorChild+":*", "composerData:"+cursorChild)
		return err
	})
	found := false
	for _, conversation := range cursorRecords(t, root)[cursorNew].Conversations {
		found = found || conversation.NativeID == cursorEcho && conversation.ParentNativeID == cursorFork
	}
	if !found {
		t.Fatal("the only copy of a deleted subagent is not read")
	}
}

// Verification reads a composer as indexing does.
func TestCursorComposerIsVerifiedLive(t *testing.T) {
	c, _ := testCatalog(t)
	// A folder's name can hold the # that separates a composer.
	root := filepath.Join(t.TempDir(), "work#2")
	if err := os.Rename(cursorFixture(t), root); err != nil {
		t.Fatal(err)
	}
	source := SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	if result := c.auditUnit(t.Context(), firstSyncUnit(t, c, source), "manual", 0); result.Outcome != "passed" {
		t.Fatalf("verification: %+v", result)
	}
}

// Cursor is found and counted by the probe, offered to a Mac set up before
// Pharos read it, and chosen in guided setup.
func TestCursorIsFoundAndSetUp(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	cursorDatabase(t, filepath.Join(home, filepath.FromSlash(cursorUserDir), "globalStorage", "state.vscdb"), nil, cursorComposerRows())
	candidate := candidateNamed(t, probeSources(home, Config{}, nil), "cursor")
	// The composers with a message: not the draft or the cleared one.
	if candidate.Kind != "cursor" || candidate.Status != "found" || candidate.Count != 7 || candidate.Unit != "session" || !candidate.Recommended {
		t.Fatalf("candidate = %+v", candidate)
	}
	server := probeServer(t, probeLibrary(t, ""))
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before Cursor = %v", status)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe", `{"kinds":["cursor"]}`); code != 200 || !strings.Contains(jsonText(result["candidates"]), `"kind":"cursor"`) {
		t.Fatalf("guided probe = %d %v", code, result)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["cursor"],"accept":["cursor"]}`); code != 200 || jsonText(result["added"]) != `["cursor"]` {
		t.Fatalf("accept cursor = %d %v", code, result)
	}
}
