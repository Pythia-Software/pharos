package archive

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	vscodeSessionID = "5f1d2c3b-0000-4000-8000-000000000001"
	vscodeEmptyID   = "5f1d2c3b-0000-4000-8000-000000000002"
	vscodeRemoteID  = "5f1d2c3b-0000-4000-8000-000000000003"
	vscodeBlankID   = "5f1d2c3b-0000-4000-8000-000000000004"
	vscodeSecret    = "vscode-secret-do-not-capture"
)

// vscodeJournal is a session's journal: the session, a request sent twice
// (the second replacing the first), a title, a response pushed in two parts
// (the second cutting the first back to where a tool call changed), the
// request's result and output, a deletion, a kind VS Code may add later, and
// a second request that ran a subagent with two models. Its prompt, its
// reply and the subagent's answer quote usage JSON.
var vscodeJournal = []string{
	`{"kind":0,"v":{"version":3,"creationDate":1793718000000,"sessionId":"` + vscodeSessionID + `","requests":[],"inputState":{"mode":{"id":"agent"}}}}`,
	`{"kind":2,"k":["requests"],"v":[{"requestId":"request_a","timestamp":1793718001000,"modelId":"copilot/auto","agent":{"extensionVersion":"0.51.0"},"message":{"text":"Count the words"},"response":[{"kind":"progressMessage","content":{"value":"Working"}}]}]}`,
	`{"kind":2,"k":["requests"],"i":0,"v":[{"requestId":"request_a","timestamp":1793718001000,"modelId":"copilot/auto","agent":{"extensionVersion":"0.51.0"},"message":{"text":"Count the words"},"response":[]}]}`,
	`{"kind":1,"k":["customTitle"],"v":"Word counter"}`,
	`{"kind":2,"k":["requests",0,"response"],"v":[{"value":"Looking at "},{"kind":"inlineReference","name":"words.py","inlineReference":{"path":"/work/app/words.py"}},{"value":" first."},` +
		`{"kind":"thinking","value":"Plan: ","id":"t1"},{"kind":"thinking","value":"run wc.","id":"t1"},{"kind":"thinking","value":""},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"c1","toolId":"run_in_terminal","isComplete":false,"toolSpecificData":{"kind":"terminal","commandLine":{"original":"wc -w words.py"}}}]}`,
	`{"kind":2,"k":["requests",0,"response"],"i":6,"v":[` +
		`{"kind":"toolInvocationSerialized","toolCallId":"c1","toolId":"run_in_terminal","isComplete":true,"toolSpecificData":{"kind":"terminal","commandLine":{"original":"wc -w words.py"},"terminalCommandState":{"exitCode":1,"duration":40},"terminalCommandOutput":{"text":"wc: words.py: No such file"}}},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"c2","toolId":"copilot_fetchWebPage","presentation":"hidden","isComplete":true},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"c2","toolId":"vscode_fetchWebPage_internal","isComplete":true,"pastTenseMessage":{"value":"Fetched example.com"}},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"c3","toolId":"copilot_multiReplaceString","presentation":"hidden","isComplete":true},` +
		`{"value":"\n` + "```" + `\n"},{"kind":"codeblockUri","uri":{"path":"/work/app/words.py"}},{"kind":"textEditGroup","uri":{"path":"/work/app/words.py"},"edits":[]},{"value":"\n` + "```" + `\n"},{"value":"There are 74 words."}]}`,
	`{"kind":1,"k":["requests",0,"result"],"v":{"timings":{"totalElapsed":9000},"metadata":{"resolvedModel":"gpt-5-mini","promptTokens":1500,"outputTokens":30,"toolCallRounds":[` +
		`{"timestamp":1793718002000,"toolCalls":[{"id":"c1__vscode-1","name":"run_in_terminal","arguments":"{\"command\":\"wc -w words.py\",\"explanation\":\"Count\"}"}]},` +
		`{"timestamp":1793718003000,"toolCalls":[{"id":"c2__vscode-2","name":"fetch_webpage","arguments":"{\"urls\":[\"https://example.com\"]}"},{"id":"c3__vscode-3","name":"multi_replace_string_in_file","arguments":"{\"filePath\":\"/work/app/words.py\"}"}]},` +
		`{"timestamp":1793718004000,"toolCalls":[]}],` +
		`"toolCallResults":{"c1__vscode-1":{"content":[{"value":"wc: words.py: No such file"}]},"c3__vscode-3":{"content":[{"value":{"node":{"children":[{"type":2,"text":"Edited:"},{"type":2,"text":" /work/app/words.py"}]}}}]}}}}}`,
	`{"kind":1,"k":["requests",0,"completionTokens"],"v":90}`,
	`{"kind":1,"k":["requests",0,"modelState"],"v":{"value":1,"completedAt":1793718010000}}`,
	`{"kind":3,"k":["inputState"]}`,
	`{"kind":9,"k":["requests"],"v":[]}`,
	`{"kind":2,"k":["requests"],"v":[{"requestId":"request_b","timestamp":1793718020000,"modelId":"copilot/claude-sonnet-4.5","agent":{"extensionVersion":"0.52.0"},` +
		`"message":{"text":"{\"usage\":{\"input_tokens\":1000000}} Review it with a subagent"},"response":[` +
		`{"kind":"toolInvocationSerialized","toolCallId":"s1","toolId":"runSubagent","isComplete":true,"toolSpecificData":{"kind":"subagent","agentName":"Explore","description":"Review","prompt":"Review words.py","result":"{\"usage\":{\"input_tokens\":2000000,\"output_tokens\":7}}"}},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"s2","toolId":"copilot_readFile","subAgentInvocationId":"s1","isComplete":true,"pastTenseMessage":{"value":"Read words.py"}},` +
		`{"value":"{\"usage\":{\"input_tokens\":1000000,\"output_tokens\":100}}"}],"completionTokens":50,` +
		`"modelTotals":{"claude-sonnet-4.5":{"inputTokens":3000,"cachedTokens":2000,"outputTokens":40},"gpt-5-mini":{"inputTokens":500,"cachedTokens":0,"outputTokens":10}},` +
		`"result":{"metadata":{"resolvedModel":"claude-sonnet-4.5","promptTokens":2200,"outputTokens":25}}}]}`,
}

// vscodeTornLine is a last line VS Code is still writing.
const vscodeTornLine = `{"kind":1,"k":["customTitle"],"v":"Half writ`

// vscodeFixture writes a VS Code user folder: the session's journal and an
// older .json of it, a window without a folder, a remote window, a window
// whose session holds no request, and the settings and state databases
// beside them.
func vscodeFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Code", "User")
	window := filepath.Join(root, "workspaceStorage", "a1")
	writeSourceFile(t, filepath.Join(window, "workspace.json"), `{"folder":"file:///work/app"}`)
	writeSourceFile(t, filepath.Join(window, "chatSessions", vscodeSessionID+".jsonl"), strings.Join(vscodeJournal, "\n")+"\n"+vscodeTornLine)
	writeSourceFile(t, filepath.Join(window, "chatSessions", vscodeSessionID+".json"), `{"version":3,"sessionId":"`+vscodeSessionID+`","requests":[{"requestId":"old","message":{"text":"Older copy"},"response":[]}]}`)
	writeSourceFile(t, filepath.Join(window, "state.vscdb"), vscodeSecret)
	writeSourceFile(t, filepath.Join(root, "globalStorage", "emptyWindowChatSessions", vscodeEmptyID+".json"),
		`{"version":3,"sessionId":"`+vscodeEmptyID+`","creationDate":1793718100000,"requests":[{"requestId":"request_c","timestamp":1793718100000,"message":{"text":"Hello"},"response":[{"value":"Hi."}]}]}`)
	remote := filepath.Join(root, "workspaceStorage", "b2")
	writeSourceFile(t, filepath.Join(remote, "workspace.json"), `{"folder":"vscode-remote://ssh-remote%2Bbox/home/me/app"}`)
	writeSourceFile(t, filepath.Join(remote, "chatSessions", vscodeRemoteID+".jsonl"),
		`{"kind":0,"v":{"version":3,"sessionId":"`+vscodeRemoteID+`","requests":[{"requestId":"request_d","timestamp":1793718200000,"message":{"text":"Where am I?"},"response":[{"value":"On box."}]}]}}`+"\n")
	writeSourceFile(t, filepath.Join(root, "workspaceStorage", "c3", "chatSessions", vscodeBlankID+".json"), `{"version":3,"sessionId":"`+vscodeBlankID+`","requests":[]}`)
	writeSourceFile(t, filepath.Join(root, "globalStorage", "state.vscdb"), vscodeSecret)
	writeSourceFile(t, filepath.Join(root, "settings.json"), `{"token":"`+vscodeSecret+`"}`)
	return root
}

func vscodeRecords(t *testing.T, root string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestVSCodeCopilotSessionReplaysItsJournal(t *testing.T) {
	records := vscodeRecords(t, vscodeFixture(t))
	if len(records) != 3 {
		t.Fatalf("records = %v", records)
	}
	session := records[vscodeSessionID]
	if session.SourceKind != "vscode-copilot" || session.Title != "Word counter" || session.Location != "/work/app" || session.Metadata["client_name"] != "Code" || len(session.Conversations) != 2 {
		t.Fatalf("session = %+v", session)
	}
	main := session.Conversations[0]
	if main.Provider != "vscode-copilot" || main.Harness != "vscode-copilot" || main.HarnessVersionFirst != "0.51.0" || main.HarnessVersionLast != "0.52.0" ||
		main.Model != "claude-sonnet-4.5" || main.StartedAt != "2026-11-03T15:00:00.000Z" || main.EndedAt != "2026-11-03T15:00:20.000Z" {
		t.Fatalf("conversation = %+v", main)
	}
	kinds := []string{}
	byID := map[string]MessageRecord{}
	for _, message := range main.Messages {
		kinds = append(kinds, message.Role+":"+message.Kind)
		byID[message.NativeID] = message
	}
	if got, want := strings.Join(kinds, " "), "user:message assistant:message assistant:reasoning assistant:tool_call tool:tool_result assistant:tool_call tool:tool_result assistant:tool_call tool:tool_result assistant:message system:metadata "+
		"user:message assistant:delegation tool:delegation_result assistant:message system:metadata system:metadata"; got != want {
		t.Fatalf("messages\n got %s\nwant %s", got, want)
	}
	if prose := byID["request_a:block:0"]; prose.Text != "Looking at `words.py` first." {
		t.Fatalf("prose = %+v", prose)
	}
	if thought := byID["request_a:block:3"]; thought.Text != "Plan: run wc." || thought.Model != "gpt-5-mini" {
		t.Fatalf("thought = %+v", thought)
	}
	// The empty code blocks VS Code shows edits in are not prose.
	if answer := byID["request_a:block:10"]; answer.Text != "There are 74 words." {
		t.Fatalf("answer = %+v", answer)
	}
	// Each call once, at its first place, as it was last shown, with its
	// input from the model's rounds.
	if call := byID["request_a:block:6"]; call.CallID != "c1" || call.Text != `{"input":{"command":"wc -w words.py","explanation":"Count"},"tool":"run_in_terminal"}` || call.CreatedAt != "2026-11-03T15:00:02.000Z" {
		t.Fatalf("command = %+v", call)
	}
	if result := byID["request_a:block:6:result"]; result.Text != `{"content":"wc: words.py: No such file","details":{"duration_ms":40,"exit_code":1},"is_error":false,"tool":"run_in_terminal"}` {
		t.Fatalf("command result = %+v", result)
	}
	if fetch := byID["request_a:block:7"]; fetch.CallID != "c2" || !strings.Contains(fetch.Text, `"tool":"vscode_fetchWebPage_internal"`) || !strings.Contains(byID["request_a:block:7:result"].Text, "Fetched example.com") {
		t.Fatalf("fetch = %+v", fetch)
	}
	if edit := byID["request_a:block:9:result"]; !strings.Contains(edit.Text, `"content":"Edited: /work/app/words.py"`) {
		t.Fatalf("hidden edit result = %+v", edit)
	}
	if usage := byID["request_a:usage"]; usage.Text != `{"input_scope":"last_round","type":"token_usage","usage":{"input_tokens":1500,"output_tokens":90},"usage_message_id":"request_a"}` {
		t.Fatalf("usage = %+v", usage)
	}
	if other := byID["request_b:gpt-5-mini:usage"]; other.Model != "gpt-5-mini" || !strings.Contains(other.Text, `"input_tokens":500`) {
		t.Fatalf("second model's usage = %+v", other)
	}
	// The subagent's prompt, its own calls, and what it returned.
	child := session.Conversations[1]
	kinds = kinds[:0]
	for _, message := range child.Messages {
		kinds = append(kinds, message.Role+":"+message.Kind)
	}
	if child.NativeID != "s1" || child.ParentNativeID != vscodeSessionID || child.AgentNickname != "Explore" ||
		strings.Join(kinds, " ") != "agent:message assistant:tool_call tool:tool_result assistant:message" || child.Messages[0].Sender != "agent:"+vscodeSessionID {
		t.Fatalf("subagent = %+v", child)
	}
	if channel, _ := harnessChannel(main.Harness, session.SourceKind); channel != "ide" {
		t.Fatalf("channel = %q", channel)
	}
	if empty := records[vscodeEmptyID]; empty.Location != "" || empty.Title != "Hello" {
		t.Fatalf("window without a folder = %+v", empty)
	}
	if remote := records[vscodeRemoteID]; remote.Location != "/home/me/app" || remote.Metadata["remote"] != "ssh-remote+box" {
		t.Fatalf("remote window = %+v", remote)
	}
}

func TestVSCodeCopilotUsageCountsEachRequestOnce(t *testing.T) {
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: vscodeFixture(t), Account: "local", Enabled: true})
	// The first request's output over its rounds and its last round's
	// input; each model's totals for the second, not its last round as well.
	// Nothing from the JSON a prompt, reply or subagent quotes.
	if total := countRows(t, c, `SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage`); total != 1590+3040+510 {
		t.Fatalf("usage %d tokens", total)
	}
	rows, err := c.DB.Query(`SELECT call_id, tool_category, COALESCE(program,''), COALESCE(exit_code,-1), output_tokens FROM tool_calls WHERE provider='vscode-copilot' ORDER BY call_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var id, category, program string
		var exit int
		var tokens float64
		if err := rows.Scan(&id, &category, &program, &exit, &tokens); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%s:%s:%d:%g", id, category, program, exit, tokens))
	}
	if strings.Join(got, " ") != "c1=command:wc:1:30 c2=web::-1:30 c3=edit::-1:30 s1=agent::-1:40 s2=read::-1:0" {
		t.Fatalf("calls: %v", got)
	}
}

// A journal that cannot be replayed whole never replaces what was indexed.
func TestVSCodeCopilotDamagedJournalLeavesWhatWasIndexed(t *testing.T) {
	for name, journal := range map[string]string{
		"no session first":    strings.Join(vscodeJournal[1:], "\n") + "\n",
		"a damaged line":      strings.Join(vscodeJournal[:3], "\n") + "\n{\"kind\":1,\n" + strings.Join(vscodeJournal[3:], "\n") + "\n",
		"no list of requests": `{"kind":0,"v":{"version":3,"sessionId":"` + vscodeSessionID + `"}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := testCatalog(t)
			root := vscodeFixture(t)
			source := SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local", Enabled: true}
			ingestSource(t, c, source)
			before := countRows(t, c, `SELECT COUNT(*) FROM messages`)
			file := filepath.Join(root, "workspaceStorage", "a1", "chatSessions", vscodeSessionID+".jsonl")
			writeSourceFile(t, file, journal)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(file, later, later); err != nil {
				t.Fatal(err)
			}
			adapter, _ := MakeAdapter(source)
			c.Ingest(adapter, nil)
			if after := countRows(t, c, `SELECT COUNT(*) FROM messages`); after != before {
				t.Fatalf("messages %d, before %d", after, before)
			}
		})
	}
}

// A capture holds the chat sessions and their windows' workspace.json alone,
// not VS Code's settings or state databases, nor a session's older .json.
// Another Mac indexes the same messages from it.
func TestVSCodeCopilotCaptureHoldsOnlyTheSessions(t *testing.T) {
	useHost(t, "host-a")
	root := vscodeFixture(t)
	source := SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local", Enabled: true}
	plan, err := planCapture(source)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range plan.files {
		got = append(got, item.rel)
	}
	want := []string{"globalStorage/emptyWindowChatSessions/" + vscodeEmptyID + ".json", "workspaceStorage/a1/chatSessions/" + vscodeSessionID + ".jsonl", "workspaceStorage/a1/workspace.json",
		"workspaceStorage/b2/chatSessions/" + vscodeRemoteID + ".jsonl", "workspaceStorage/b2/workspace.json", "workspaceStorage/c3/chatSessions/" + vscodeBlankID + ".json"}
	if strings.Join(got, ",") != strings.Join(want, ",") || len(plan.databases) != 0 {
		t.Fatalf("capture plan = %v %v", got, plan.databases)
	}
	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if strings.Contains(capturedText(t, config.CaptureRoot), vscodeSecret) {
		t.Fatal("the capture holds VS Code's settings or state")
	}
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='vscode-copilot'`)
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}
}

// VS Code is offered to a Mac set up before Pharos read it, chosen in guided
// setup, and followed by automatic updates, which read a response that grew.
func TestVSCodeCopilotIsSetUpAndFollowedLikeOtherTools(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	window := filepath.Join(home, "Library", "Application Support", "Code", "User", "workspaceStorage", "a1")
	writeSourceFile(t, filepath.Join(window, "chatSessions", vscodeSessionID+".jsonl"), strings.Join(vscodeJournal, "\n")+"\n")
	path := probeLibrary(t, "")
	server := probeServer(t, path)
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before VS Code = %v", status)
	}
	code, result := probeCall(t, server, http.MethodPost, "/api/probe", `{"kinds":["vscode-copilot"]}`)
	if code != 200 || !strings.Contains(jsonText(result["candidates"]), `"kind":"vscode-copilot"`) {
		t.Fatalf("guided probe = %d %v", code, result)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["vscode-copilot"],"accept":["vscode-copilot"]}`); code != 200 || jsonText(result["added"]) != `["vscode-copilot"]` {
		t.Fatalf("accept vscode = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(0) {
		t.Fatalf("a chosen source is still offered: %v", status)
	}

	root := vscodeFixture(t)
	automatic := automaticTestServer(t, SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local", Enabled: true})
	journal := filepath.Join(root, "workspaceStorage", "a1", "chatSessions", vscodeSessionID+".jsonl")
	writeSourceFile(t, journal, strings.Join(vscodeJournal, "\n")+"\n"+`{"kind":2,"k":["requests",1,"response"],"v":[{"value":" All green."}]}`+"\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(journal, later, later); err != nil {
		t.Fatal(err)
	}
	automatic.runAutomatic(automatic.life.ctx, syncSettings{Interval: 300})
	if n := countRows(t, automatic.Catalog, `SELECT COUNT(*) FROM messages WHERE native_id='request_b:block:2' AND text LIKE '%}} All green.'`); n != 1 {
		t.Fatalf("automatic updates skipped the grown response: %d", n)
	}
}

// A session read from another file, a newer copy in another window or a
// journal replacing its .json, is the same session: what grew is read.
func TestVSCodeCopilotSessionMovingFilesKeepsGrowing(t *testing.T) {
	grown := strings.Join(vscodeJournal, "\n") + "\n" + `{"kind":2,"k":["requests",1,"response"],"v":[{"value":" All green."}]}` + "\n"
	for name, move := range map[string]func(root string) string{
		"another window": func(root string) string {
			return filepath.Join(root, "workspaceStorage", "d4", "chatSessions", vscodeSessionID+".jsonl")
		},
		"a journal replacing its .json": func(root string) string {
			journal := filepath.Join(root, "workspaceStorage", "a1", "chatSessions", vscodeSessionID+".jsonl")
			object := filepath.Join(root, "workspaceStorage", "a1", "chatSessions", vscodeSessionID+".json")
			writeSourceFile(t, object, `{"version":3,"sessionId":"`+vscodeSessionID+`","requests":[{"requestId":"request_b","timestamp":1793718020000,"message":{"text":"Review it"},"response":[{"kind":"toolInvocationSerialized","toolCallId":"s1","toolId":"runSubagent","toolSpecificData":{"kind":"subagent"}},{"kind":"progressMessage"},{"value":"Working"}]}]}`)
			if err := os.Remove(journal); err != nil {
				t.Fatal(err)
			}
			return journal
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := testCatalog(t)
			root := vscodeFixture(t)
			source := SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local", Enabled: true}
			file := move(root)
			ingestSource(t, c, source)
			writeSourceFile(t, file, grown)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(file, later, later); err != nil {
				t.Fatal(err)
			}
			ingestSource(t, c, source)
			if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='request_b:block:2' AND text LIKE '%}} All green.'`); n != 1 {
				t.Fatalf("the grown reply was not read: %d", n)
			}
		})
	}
}

// A window whose sessions cannot be listed is reported; every other
// session is still read. A capture, which would miss its sessions, fails.
func TestVSCodeCopilotUnreadableWindowLeavesTheOthers(t *testing.T) {
	root := vscodeFixture(t)
	locked := filepath.Join(root, "workspaceStorage", "b2", "chatSessions")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("running with access to every folder")
	}
	adapter, _ := MakeAdapter(SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local"})
	found := []string{}
	err := adapter.Discover(func(record WorkspaceRecord) error { found = append(found, record.SourceID); return nil })
	if err == nil || strings.Join(found, ",") != vscodeSessionID+","+vscodeEmptyID {
		t.Fatalf("records %v, error %v", found, err)
	}
	if _, err := planCapture(SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root}); err == nil {
		t.Fatal("a capture missing a window's sessions was planned")
	}
}

// While the window holding a session's newest copy cannot be listed, or
// once it is gone, an older copy in another window does not replace what
// was read from the newer.
func TestVSCodeCopilotOlderCopyNeverReplacesTheNewer(t *testing.T) {
	c, _ := testCatalog(t)
	root := vscodeFixture(t)
	source := SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local", Enabled: true}
	older := filepath.Join(root, "workspaceStorage", "d4", "chatSessions", vscodeSessionID+".jsonl")
	writeSourceFile(t, older, strings.Join(vscodeJournal[:11], "\n")+"\n")
	newer, err := os.Stat(filepath.Join(root, "workspaceStorage", "a1", "chatSessions", vscodeSessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if earlier := newer.ModTime().Add(-time.Hour); os.Chtimes(older, earlier, earlier) != nil {
		t.Fatal(err)
	}
	ingestSource(t, c, source)
	before := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id LIKE 'request_b%'`)
	// A folder that cannot be listed, or whose files cannot be looked up,
	// and then the newer copy's window removed.
	for _, mode := range []os.FileMode{0, 0o444, 0o755} {
		locked := filepath.Join(root, "workspaceStorage", "a1", "chatSessions")
		if err := os.Chmod(locked, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		if mode == 0o755 {
			if err := os.RemoveAll(filepath.Dir(locked)); err != nil {
				t.Fatal(err)
			}
		} else if _, err := os.Stat(filepath.Join(locked, vscodeSessionID+".jsonl")); err == nil {
			t.Skip("running with access to every folder")
		}
		adapter, _ := MakeAdapter(source)
		c.Ingest(adapter, nil)
		if after := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id LIKE 'request_b%'`); after != before || before == 0 {
			t.Fatalf("mode %o: the second request's messages: %d, before %d", mode, after, before)
		}
	}
}

// A window opened on another folder moves its sessions there, though their
// files, which version them, are unchanged; also when read from retained
// copies.
func TestVSCodeCopilotFolderChangeIsReadWithoutANewTurn(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "retained"}[retained], func(t *testing.T) {
			c, _ := testCatalog(t)
			root := vscodeFixture(t)
			source := SourceConfig{Name: "vscode", Kind: "vscode-copilot", Path: root, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": retained}}
			ingestSource(t, c, source)
			manifest := filepath.Join(root, "workspaceStorage", "a1", "workspace.json")
			writeSourceFile(t, manifest, `{"folder":"file:///work/app-renamed"}`)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(manifest, later, later); err != nil {
				t.Fatal(err)
			}
			ingestSource(t, c, source)
			if n := countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE location='/work/app-renamed'`); n != 1 {
				t.Fatalf("moved workspaces: %d", n)
			}
		})
	}
}
