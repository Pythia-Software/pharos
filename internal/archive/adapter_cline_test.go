package archive

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clineSecret stands for the provider credentials Cline keeps beside its
// sessions.
const clineSecret = "sk-cline-secret-key"

const clineSessionID = "1793000000000_abcde"

// clineManifest is a session's <id>.json in Cline 4.1's shape.
const clineManifest = `{"version":1,"session_id":"1793000000000_abcde","source":"cli","pid":1,"started_at":"2026-11-03T15:00:00.000Z","status":"idle",
"interactive":true,"provider":"anthropic","model":"claude-sonnet-4.5","cwd":"/work/app","workspace_root":"/work/app","prompt":"Count the words",
"metadata":{"sessionHistoryOrigin":{"mode":"user","version":"4.1.26"},"title":"Word counter","totalCost":0.12,"modelId":"claude-sonnet-4.5",
"aggregateUsage":{"inputTokens":4900,"outputTokens":150,"cacheReadTokens":4100,"cacheWriteTokens":0,"totalCost":0.12},"git":{"branch":"main"}},
"messages_path":"/elsewhere/1793000000000_abcde.messages.json"}`

// clineMessages is its <id>.messages.json: four requests, each ended by an
// assistant message carrying its metrics; tool inputs as JSON text; results
// as Cline's operation lists, and an edit's as JSON text; a failed command
// flagged only by its operation; a command given as a program and its
// arguments; a web search the provider ran; an empty reply; hook context
// and a reminder Cline wrote as user messages; and a prompt sent after a
// switch to plan mode, which quotes usage JSON.
const clineMessages = `{"version":1,"updated_at":"2026-11-03T15:01:00.000Z","agent":"lead","sessionId":"1793000000000_abcde",
"origin":{"source":"cli","mode":"user","sessionId":"1793000000000_abcde","version":"4.1.26"},"system_prompt":"You are Cline.","messages":[
{"id":"h1","role":"user","ts":1793717999000,"content":[{"type":"text","text":"<hook_context>SessionStart: the tree is clean</hook_context>"}],"metadata":{"userRunSpan":0,"displayRole":"system"}},
{"id":"u1","role":"user","ts":1793718000000,"content":[{"type":"text","text":"<user_input mode=\"act\">Count the words</user_input>"}]},
{"id":"a1","role":"assistant","ts":1793718001000,"content":[{"type":"thinking","thinking":"Look at the files first."},
  {"type":"tool_use","id":"c1","name":"run_commands","input":"{\"commands\":[\"ls\"]}"},
  {"type":"tool_use","id":"c5","name":"read_files","input":"{\"files\":[{\"path\":\"wordstats.py\"}]}"}],
  "modelInfo":{"id":"claude-sonnet-4.5","provider":"anthropic"},"metrics":{"inputTokens":1000,"outputTokens":40,"cacheReadTokens":600,"cacheWriteTokens":0}},
{"id":"r1","role":"user","ts":1793718002000,"content":[{"type":"tool_result","tool_use_id":"c1","name":"run_commands","content":[{"query":"ls","result":"wordstats.py","success":true}]},
  {"type":"tool_result","tool_use_id":"c5","name":"read_files","content":"{\"query\":\"wordstats.py\",\"result\":\"import sys\",\"success\":true}"}]},
{"id":"a2","role":"assistant","ts":1793718003000,"content":[
  {"type":"tool_use","id":"c2","name":"run_commands","input":"{\"commands\":[\"wc -w wordstats.py\"]}"},
  {"type":"tool_use","id":"c3","name":"run_commands","input":"{\"commands\":[\"cat missing.txt\"]}"},
  {"type":"tool_use","id":"c4","name":"run_commands","input":{"commands":[{"command":"git","args":["status","--short"]}]}}],
  "modelInfo":{"id":"claude-sonnet-4.5","provider":"anthropic"},"metrics":{"inputTokens":1200,"outputTokens":60,"cacheReadTokens":1000,"cacheWriteTokens":0}},
{"id":"r2","role":"user","ts":1793718004000,"content":[{"type":"tool_result","tool_use_id":"c2","name":"run_commands","content":[{"query":"wc -w wordstats.py","result":"74 wordstats.py","success":true}]},
  {"type":"tool_result","tool_use_id":"c3","name":"run_commands","content":[{"query":"cat missing.txt","result":"[Command exited with code 1]\ncat: missing.txt: No such file","error":"Command exited with code 1","success":false}]},
  {"type":"tool_result","tool_use_id":"c4","name":"run_commands","content":[{"query":"git status --short","result":"","success":true}]}]},
{"id":"a3","role":"assistant","ts":1793718005000,"content":[{"type":"text","text":"There are 74 words."}],
  "metadata":{"modelToolActivities":[{"toolCallId":"ws1","toolName":"web_search","execution":"provider","input":{"query":"word count"},"output":"Counting words","isError":false}]},
  "modelInfo":{"id":"claude-sonnet-4.5","provider":"anthropic"},"metrics":{"inputTokens":1300,"outputTokens":20,"cacheReadTokens":1200,"cacheWriteTokens":0}},
{"id":"e1","role":"assistant","ts":1793718005500,"content":[]},
{"id":"k1","role":"user","ts":1793718005800,"content":[{"type":"text","text":"Remember to finish with a summary."}],"metadata":{"kind":"completion_reminder"}},
{"id":"u2","role":"user","ts":1793718006000,"content":[{"type":"text","text":"<user_input mode=\"plan\"><mode_notice>The user switched from act mode to plan mode before sending this message.</mode_notice>\n{\"usage\":{\"input_tokens\":1000,\"output_tokens\":200}} Plan the next step</user_input>"}]},
{"id":"a4","role":"assistant","ts":1793718007000,"content":[{"type":"text","text":"Next, add tests."}],
  "modelInfo":{"id":"claude-opus-4.5","provider":"anthropic"},"metrics":{"inputTokens":1400,"outputTokens":30,"cacheReadTokens":1300,"cacheWriteTokens":0}}]}`

// clineFixture writes a Cline data folder: the session, its compacted
// context, and the settings, index and checkpoints beside it.
func clineFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	session := filepath.Join(root, "sessions", clineSessionID)
	writeSourceFile(t, filepath.Join(session, clineSessionID+".json"), clineManifest)
	writeSourceFile(t, filepath.Join(session, clineSessionID+".messages.json"), clineMessages)
	writeSourceFile(t, filepath.Join(session, clineSessionID+".compaction.json"), clineMessages)
	writeSourceFile(t, filepath.Join(root, "settings", "providers.json"), `{"anthropic":{"apiKey":"`+clineSecret+`"}}`)
	writeSourceFile(t, filepath.Join(root, "db", "sessions.db"), "index")
	writeSourceFile(t, filepath.Join(root, "checkpoint-scratch", "d59b", "index"), "checkpoint")
	writeSourceFile(t, filepath.Join(root, "globalState.json"), `{}`)
	return root
}

func TestClineSessionKeepsItsTurnsToolsAndUsage(t *testing.T) {
	adapter, err := MakeAdapter(SourceConfig{Name: "cline", Kind: "cline", Path: clineFixture(t), Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	records := []WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records = append(records, record); return nil }); err != nil || len(records) != 1 {
		t.Fatalf("records = %v %v", records, err)
	}
	session := records[0]
	if session.SourceID != clineSessionID || session.SourceKind != "cline" || session.Title != "Word counter" || session.Location != "/work/app" ||
		session.Metadata["branch"] != "main" || session.Metadata["client_name"] != "cli" || session.Metadata["reported_cost_usd"] != 0.12 {
		t.Fatalf("session = %+v", session)
	}
	main := session.Conversations[0]
	if main.Provider != "cline" || main.Harness != "cline" || main.HarnessVersionLast != "4.1.26" || main.Model != "claude-opus-4.5" ||
		main.StartedAt != "2026-11-03T15:00:00.000Z" || main.EndedAt == "" {
		t.Fatalf("conversation = %+v", main)
	}
	kinds := []string{}
	byID := map[string]MessageRecord{}
	for _, message := range main.Messages {
		kinds = append(kinds, message.Role+":"+message.Kind)
		byID[message.NativeID] = message
	}
	if got, want := strings.Join(kinds, " "), "system:context system:context user:message assistant:reasoning assistant:tool_call assistant:tool_call system:metadata tool:tool_result tool:tool_result "+
		"assistant:tool_call assistant:tool_call assistant:tool_call system:metadata tool:tool_result tool:tool_result tool:tool_result "+
		"assistant:tool_call tool:tool_result assistant:message system:metadata system:context system:context user:message assistant:message system:metadata"; got != want {
		t.Fatalf("messages\n got %s\nwant %s", got, want)
	}
	// What the person typed, without Cline's wrapper and mode notice.
	if prompt := byID["u1"]; prompt.Text != "Count the words" || !strings.Contains(prompt.RawText, "user_input") {
		t.Fatalf("prompt = %+v", prompt)
	}
	if prompt := byID["u2"]; !strings.HasSuffix(prompt.Text, "Plan the next step") || strings.Contains(prompt.Text, "mode_notice") {
		t.Fatalf("plan prompt = %+v", prompt)
	}
	if notice := byID["u2:block:0:notice:0"]; notice.Kind != "context" || !strings.Contains(notice.Text, "plan mode") {
		t.Fatalf("mode notice = %+v", notice)
	}
	if call := byID["a1:block:1"]; call.CallID != "c1" || call.Text != `{"input":{"commands":["ls"]},"tool":"run_commands"}` {
		t.Fatalf("call = %+v", call)
	}
	// Cline's operation results are read as their text; a failed operation
	// fails its call. Hook context and Cline's reminders are not the
	// person's, so the purpose is the first prompt.
	if result := byID["r1:block:0"]; result.Text != `{"content":"wordstats.py","is_error":false,"tool":"run_commands"}` || !strings.Contains(result.RawText, `"success":true`) {
		t.Fatalf("result = %+v", result)
	}
	if read := byID["r1:block:1"]; read.Text != `{"content":"import sys","is_error":false,"tool":"read_files"}` {
		t.Fatalf("read result = %+v", read)
	}
	if failed := byID["r2:block:1"]; failed.CallID != "c3" || !strings.Contains(failed.Text, `"is_error":true`) || !strings.Contains(failed.Text, "No such file") {
		t.Fatalf("failed result = %+v", failed)
	}
	if hook, reminder := byID["h1"], byID["k1"]; hook.Kind != "context" || !strings.Contains(hook.Text, "cline_hook_context") || reminder.Kind != "context" || !strings.Contains(reminder.Text, "cline_completion_reminder") {
		t.Fatalf("hook %+v reminder %+v", hook, reminder)
	}
	if session.Purpose != "Count the words" {
		t.Fatalf("purpose = %q", session.Purpose)
	}
	if search, result := byID["a3:block:1"], byID["a3:activity-result:0"]; search.CallID != "ws1" || search.Text != `{"input":{"query":"word count"},"tool":"web_search"}` || result.CallID != "ws1" || !strings.Contains(result.Text, "Counting words") {
		t.Fatalf("provider search %+v result %+v", search, result)
	}
	if usage := byID["a2:usage"]; usage.Model != "claude-sonnet-4.5" || !strings.Contains(usage.Text, `"input_tokens":1200`) || !strings.Contains(usage.Text, `"cached_input_tokens":1000`) {
		t.Fatalf("usage = %+v", usage)
	}
}

// Cline's CLI and its editor extensions share its folder; a session's
// channel is the client that ran it. An imported history names only the app
// that imported it, so its channel stays unknown.
func TestClineChannelIsItsClient(t *testing.T) {
	for i, test := range []struct{ client, mode, want string }{
		{"neovim", "user", "ide"}, {"cli", "user", "cli"}, {"vscode", "user", "ide"}, {"jetbrains", "user", "ide"}, {"desktop", "user", "desktop"},
		{"web", "user", "web"}, {"desktop", "import", ""}, {"cli", "automation", ""}, {"core", "user", ""},
	} {
		root := clineFixture(t)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".json") {
				return err
			}
			data, err := os.ReadFile(path)
			if err == nil {
				text := strings.ReplaceAll(string(data), `"source":"cli"`, `"source":"`+test.client+`"`)
				err = os.WriteFile(path, []byte(strings.ReplaceAll(text, `"mode":"user"`, `"mode":"`+test.mode+`"`)), 0o644)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		source := SourceConfig{Name: "cline", Kind: "cline", Path: root, Account: "local", Enabled: true}
		if i == 0 {
			// Once through ingestion, to the published observation.
			c, _ := testCatalog(t)
			ingestSource(t, c, source)
			if n := countRows(t, c, `SELECT COUNT(*) FROM origin_observations o JOIN conversations v ON v.id=o.conversation_id WHERE v.native_id=? AND o.channel=?`, clineSessionID, test.want); n != 1 {
				t.Fatalf("a session %s ran has no channel %q", test.client, test.want)
			}
		}
		adapter, err := MakeAdapter(source)
		if err != nil {
			t.Fatal(err)
		}
		err = adapter.Discover(func(record WorkspaceRecord) error {
			if channel, _ := nativeChannel(record.SourceKind, record.Conversations[0]); channel != test.want {
				t.Errorf("a %s session %s ran has channel %q, not %q", test.mode, test.client, channel, test.want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClineUsageCountsEachRequestOnce(t *testing.T) {
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "cline", Kind: "cline", Path: clineFixture(t), Account: "local", Enabled: true})
	// Cline's input holds its cache reads: 1040 + 1260 + 1320 + 1430, and
	// nothing from the quoted JSON or the compacted context.
	if total := countRows(t, c, `SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage`); total != 1040+1260+1320+1430 {
		t.Fatalf("usage %d tokens", total)
	}
	// Each call shares the output of the request that made it.
	rows, err := c.DB.Query(`SELECT call_id, output_tokens FROM tool_calls WHERE provider='cline' ORDER BY call_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var id string
		var tokens float64
		if err := rows.Scan(&id, &tokens); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%g", id, tokens))
	}
	if strings.Join(got, " ") != "c1=20 c2=20 c3=20 c4=20 c5=20 ws1=20" {
		t.Fatalf("output tokens per call: %v", got)
	}
	// Cline runs commands as a list, each its own command line, or a
	// program and its arguments. Its reads and the provider's search have
	// their categories, and a failed operation fails its call.
	commands, err := c.DB.Query(`SELECT call_id, tool_category, COALESCE(program,''), COALESCE(subcommand,''), command_count, status, result_bytes > 0 FROM tool_calls WHERE provider='cline' ORDER BY call_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	got = got[:0]
	for commands.Next() {
		var id, category, program, subcommand, status string
		var count int
		var output bool
		if err := commands.Scan(&id, &category, &program, &subcommand, &count, &status, &output); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%s:%s:%s:%d:%s:%v", id, category, program, subcommand, count, status, output))
	}
	if strings.Join(got, " ") != "c1=command:ls::1:ok:true c2=command:wc::1:ok:true c3=command:cat::1:error:true c4=command:git:status:1:ok:false c5=read:::0:ok:true ws1=web:::0:ok:true" {
		t.Fatalf("commands per call: %v", got)
	}
}

// A messages file without its list, as one cut short or from another
// release, never replaces what was indexed; nor does an empty list.
func TestClineSessionWithoutMessagesLeavesWhatWasIndexed(t *testing.T) {
	for name, replacement := range map[string]string{"no list": `null`, "not a list": `{}`, "empty list": `[]`} {
		t.Run(name, func(t *testing.T) {
			c, _ := testCatalog(t)
			root := clineFixture(t)
			source := SourceConfig{Name: "cline", Kind: "cline", Path: root, Account: "local", Enabled: true}
			ingestSource(t, c, source)
			before := countRows(t, c, `SELECT COUNT(*) FROM messages`)
			messages := filepath.Join(root, "sessions", clineSessionID, clineSessionID+".messages.json")
			writeSourceFile(t, messages, `{"version":1,"sessionId":"`+clineSessionID+`","system_prompt":"You are Cline.","messages":`+replacement+`}`)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(messages, later, later); err != nil {
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

// A capture holds the sessions' manifests and messages alone: not Cline's
// provider settings, which hold credentials, nor its index, checkpoints or
// compacted context. Another Mac indexes the same messages from it.
func TestClineCaptureHoldsOnlyTheSessions(t *testing.T) {
	useHost(t, "host-a")
	root := clineFixture(t)
	source := SourceConfig{Name: "cline", Kind: "cline", Path: root, Account: "local", Enabled: true}
	plan, err := planCapture(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range plan.files {
		got = append(got, item.rel)
	}
	if strings.Join(got, ",") != "sessions/"+clineSessionID+"/"+clineSessionID+".json,sessions/"+clineSessionID+"/"+clineSessionID+".messages.json" || len(plan.databases) != 0 {
		t.Fatalf("capture plan = %v %v", got, plan.databases)
	}
	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if strings.Contains(capturedText(t, config.CaptureRoot), clineSecret) {
		t.Fatal("the capture holds Cline's credentials")
	}
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='cline'`)
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}
}

// A session renamed in Cline is renamed in Pharos, though its messages, which
// version it, are unchanged; also when read from retained copies.
func TestClineRenameIsReadWithoutANewTurn(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "retained"}[retained], func(t *testing.T) {
			c, _ := testCatalog(t)
			root := clineFixture(t)
			source := SourceConfig{Name: "cline", Kind: "cline", Path: root, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": retained}}
			ingestSource(t, c, source)
			manifest := filepath.Join(root, "sessions", clineSessionID, clineSessionID+".json")
			writeSourceFile(t, manifest, strings.Replace(clineManifest, `"title":"Word counter"`, `"title":"Word counter, renamed"`, 1))
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(manifest, later, later); err != nil {
				t.Fatal(err)
			}
			ingestSource(t, c, source)
			if n := countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE title='Word counter, renamed'`); n != 1 {
				t.Fatalf("renamed workspaces: %d", n)
			}
		})
	}
}

// Cline is offered to a Mac set up before Pharos read it, chosen in guided
// setup, and followed by automatic updates.
func TestClineIsSetUpAndFollowedLikeOtherTools(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	// CLINE_DATA_DIR moves Cline's folder.
	t.Setenv("CLINE_DATA_DIR", filepath.Join(home, "cline"))
	session := filepath.Join(home, "cline", "sessions", clineSessionID)
	writeSourceFile(t, filepath.Join(session, clineSessionID+".json"), clineManifest)
	writeSourceFile(t, filepath.Join(session, clineSessionID+".messages.json"), clineMessages)
	path := probeLibrary(t, "")
	server := probeServer(t, path)
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before Cline = %v", status)
	}
	code, result := probeCall(t, server, http.MethodPost, "/api/probe", `{"kinds":["cline"]}`)
	if code != 200 || !strings.Contains(jsonText(result["candidates"]), `"kind":"cline"`) {
		t.Fatalf("guided probe = %d %v", code, result)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["cline"],"accept":["cline"]}`); code != 200 || jsonText(result["added"]) != `["cline"]` {
		t.Fatalf("accept cline = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(0) {
		t.Fatalf("a chosen source is still offered: %v", status)
	}

	root := clineFixture(t)
	automatic := automaticTestServer(t, SourceConfig{Name: "cline", Kind: "cline", Path: root, Account: "local", Enabled: true})
	messages := filepath.Join(root, "sessions", clineSessionID, clineSessionID+".messages.json")
	writeSourceFile(t, messages, strings.TrimSuffix(clineMessages, "]}")+`,{"id":"a5","role":"assistant","ts":1793718008000,"content":[{"type":"text","text":"Done."}]}]}`)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(messages, later, later); err != nil {
		t.Fatal(err)
	}
	automatic.runAutomatic(automatic.life.ctx, syncSettings{Interval: 300})
	if n := countRows(t, automatic.Catalog, `SELECT COUNT(*) FROM messages WHERE native_id='a5'`); n != 1 {
		t.Fatalf("automatic updates skipped Cline: %d", n)
	}
}

// Cline's operation results, as lists or JSON text, are read as their text;
// an operation that reports failure fails the call even without an error.
// Content of another shape is left as written.
func TestClineOperationResults(t *testing.T) {
	for _, test := range []struct {
		content any
		text    any
		failed  bool
	}{
		{[]any{map[string]any{"query": "a", "result": "one", "success": true}, map[string]any{"query": "b", "result": "two", "success": true}}, "one\ntwo", false},
		{[]any{map[string]any{"query": "go test", "result": "FAIL", "success": false}}, "FAIL", true},
		{`{"query":"edit:a.go","result":"","error":"old_text was omitted","success":false}`, "old_text was omitted", true},
		{`{"error":"Invalid input"}`, "Invalid input", true},
		{"plain output", "plain output", false},
		{`{"stdout":"x"}`, `{"stdout":"x"}`, false},
	} {
		text, failed := clineToolResult(test.content)
		if text != test.text || failed != test.failed {
			t.Errorf("%v: %q %v, want %q %v", test.content, text, failed, test.text, test.failed)
		}
	}
	for name, want := range map[string]string{"read_files": "read", "fetch_web_content": "web", "skills": "meta", "search_codebase": "search", "run_commands": "command"} {
		if got, _ := toolCategory(name, "tool_call"); got != want {
			t.Errorf("%s is %s, want %s", name, got, want)
		}
	}
}
