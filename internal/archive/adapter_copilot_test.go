package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copilotSessionEvents is a Copilot CLI session in the shape release 1.0.105
// writes: a turn with reasoning and two tool calls, one failing; a subagent
// whose events sit in the same log; a receipt per request; and a shutdown
// that totals them all again.
const copilotSessionEvents = `{"type":"session.start","data":{"sessionId":"sess-1","copilotVersion":"1.0.105","context":{"cwd":"/work/app","branch":"main"}},"id":"e1","timestamp":"2026-11-03T15:00:00.000Z"}
{"type":"session.model_change","data":{"newModel":"auto","reasoningEffort":null},"id":"e2","timestamp":"2026-11-03T15:00:01.000Z"}
{"type":"user.message","data":{"content":"Add a word counter","transformedContent":"<current_datetime>x</current_datetime>\n\nAdd a word counter","messageId":"u1","responsesReasoning":{"model":"gpt-6-luna","effort":"medium"}},"id":"e3","timestamp":"2026-11-03T15:00:02.000Z"}
{"type":"system.message","data":{"role":"system","content":"You are Copilot."},"id":"e4","timestamp":"2026-11-03T15:00:02.100Z"}
{"type":"assistant.turn_start","data":{"turnId":"0"},"id":"e5","timestamp":"2026-11-03T15:00:02.200Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":1000,"outputTokens":50,"cacheReadTokens":0,"cacheWriteTokens":900,"reasoningTokens":20,"reasoningEffort":"medium","apiCallId":"api-1"}},"id":"e6","timestamp":"2026-11-03T15:00:05.000Z"}
{"type":"assistant.message","data":{"messageId":"a1","model":"gpt-6-luna","content":"","reasoningText":"Checking the files","reasoningBlocks":{"provider":"openai-responses"},"toolRequests":[{"toolCallId":"c1","name":"bash","arguments":{"command":"ls"}},{"toolCallId":"c2","name":"view","arguments":{"path":"missing.py"}}]},"id":"e7","timestamp":"2026-11-03T15:00:05.100Z"}
{"type":"tool.execution_start","data":{"toolCallId":"c1","toolName":"bash"},"id":"e8","timestamp":"2026-11-03T15:00:05.200Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c1","success":true,"result":{"content":"app.py"},"shellExecution":{"exitCode":0}},"id":"e9","timestamp":"2026-11-03T15:00:05.700Z"}
{"type":"tool.execution_start","data":{"toolCallId":"c2","toolName":"view"},"id":"e10","timestamp":"2026-11-03T15:00:05.800Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c2","success":false,"error":{"message":"no such file"}},"id":"e11","timestamp":"2026-11-03T15:00:05.900Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":1200,"outputTokens":80,"cacheReadTokens":900,"cacheWriteTokens":250,"reasoningTokens":0,"apiCallId":"api-2"}},"id":"e12","timestamp":"2026-11-03T15:00:08.000Z"}
{"type":"assistant.message","data":{"messageId":"a2","model":"gpt-6-luna","content":"Asking for a review.","toolRequests":[{"toolCallId":"c3","name":"task","arguments":{"prompt":"Review app.py"}}]},"id":"e13","timestamp":"2026-11-03T15:00:08.100Z"}
{"type":"subagent.started","data":{"toolCallId":"c3","agentName":"code-review","agentDisplayName":"app-review","model":"gpt-6-luna"},"agentId":"agent-1","id":"e14","timestamp":"2026-11-03T15:00:08.200Z"}
{"type":"user.message","data":{"content":"Review app.py","source":"agent-sess-1","messageId":"u2"},"agentId":"agent-1","id":"e15","timestamp":"2026-11-03T15:00:08.300Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":500,"outputTokens":40,"cacheReadTokens":0,"cacheWriteTokens":450,"reasoningTokens":10,"apiCallId":"api-3"}},"agentId":"agent-1","id":"e16","timestamp":"2026-11-03T15:00:10.000Z"}
{"type":"assistant.message","data":{"messageId":"a3","model":"gpt-6-luna","content":"Looks fine.","toolRequests":[]},"agentId":"agent-1","id":"e17","timestamp":"2026-11-03T15:00:10.100Z"}
{"type":"subagent.completed","data":{"toolCallId":"c3"},"agentId":"agent-1","id":"e18","timestamp":"2026-11-03T15:00:10.200Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c3","success":true,"result":{"content":"Looks fine."}},"id":"e19","timestamp":"2026-11-03T15:00:10.300Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":1300,"outputTokens":30,"cacheReadTokens":1150,"cacheWriteTokens":140,"reasoningTokens":0,"apiCallId":"api-4"}},"id":"e20","timestamp":"2026-11-03T15:00:12.000Z"}
{"type":"assistant.message","data":{"messageId":"a4","model":"gpt-6-luna","content":"Added the counter.","toolRequests":[]},"id":"e21","timestamp":"2026-11-03T15:00:12.100Z"}
{"type":"session.shutdown","data":{"modelMetrics":{"gpt-6-luna":{"usage":{"inputTokens":4000,"outputTokens":200,"cacheReadTokens":2050,"cacheWriteTokens":1740}}}},"id":"e22","timestamp":"2026-11-03T15:00:13.000Z"}
{"type":"session.resume","data":{"context":{"cwd":"/work/app"}},"id":"e23","timestamp":"2026-11-03T15:05:00.000Z"}
{"type":"user.message","data":{"content":"Thanks","messageId":"u3"},"id":"e24","timestamp":"2026-11-03T15:05:01.000Z"}
`

const copilotWorkspace = `id: sess-1
cwd: /work/app
git_root: /work/app
branch: main
client_name: github/cli
name: "Word counter: first pass"
user_named: true
summary: |-
  Adds a word counter
  and reviews it
created_at: 2026-11-03T15:00:00.000Z
`

// copilotLegacy is a flat log from a release that wrote no receipts, only a
// shutdown total.
const copilotLegacy = `{"type":"session.start","data":{"sessionId":"sess-0","copilotVersion":"0.0.330","context":{"cwd":"/work/old"}},"id":"l1","timestamp":"2026-01-02T10:00:00.000Z"}
{"type":"user.message","data":{"content":"Hello"},"id":"l2","timestamp":"2026-01-02T10:00:01.000Z"}
{"type":"assistant.message","data":{"messageId":"b1","content":"Hi","model":"claude-sonnet-4.5"},"id":"l3","timestamp":"2026-01-02T10:00:02.000Z"}
{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.5":{"usage":{"inputTokens":300,"outputTokens":20,"cacheReadTokens":100,"cacheWriteTokens":0}}}},"id":"l4","timestamp":"2026-01-02T10:00:03.000Z"}
`

func copilotFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sessions := filepath.Join(root, "session-state")
	writeSourceFile(t, filepath.Join(sessions, "sess-1", "events.jsonl"), copilotSessionEvents)
	writeSourceFile(t, filepath.Join(sessions, "sess-1", "workspace.yaml"), copilotWorkspace)
	writeSourceFile(t, filepath.Join(sessions, "sess-1", "rewind-file-snapshots", "backups", "abc"), "file contents")
	writeSourceFile(t, filepath.Join(sessions, "sess-0.jsonl"), copilotLegacy)
	// An upgraded session's flat log is replaced by its folder.
	writeSourceFile(t, filepath.Join(sessions, "sess-1.jsonl"), strings.ReplaceAll(copilotLegacy, "sess-0", "sess-1"))
	// A session started and left before anything was said has no log.
	writeSourceFile(t, filepath.Join(sessions, "sess-2", "workspace.yaml"), "id: sess-2\n")
	writeSourceFile(t, filepath.Join(root, "config.json"), `{"token":"secret"}`)
	return root
}

func discoverCopilot(t *testing.T, root string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error {
		records[record.SourceID] = record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestCopilotSessionKeepsItsTurnsToolsAndSubagent(t *testing.T) {
	records := discoverCopilot(t, copilotFixture(t))
	if len(records) != 2 {
		t.Fatalf("records = %v", records)
	}
	session := records["sess-1"]
	if session.SourceKind != "copilot" || session.Title != "Word counter: first pass" || session.Location != "/work/app" || session.Metadata["branch"] != "main" || session.Purpose != "Add a word counter" || session.Outcome != "Added the counter." {
		t.Fatalf("session = %+v", session)
	}
	if session.Metadata["title_kind"] != "custom" {
		t.Fatalf("a title the person chose is not marked: %+v", session.Metadata)
	}
	if len(session.Conversations) != 2 {
		t.Fatalf("conversations = %+v", session.Conversations)
	}
	main, review := session.Conversations[0], session.Conversations[1]
	if main.NativeID != "sess-1" || main.Provider != "copilot" || main.Harness != "copilot-cli" || main.HarnessVersionLast != "1.0.105" || main.Model != "gpt-6-luna" || main.Effort != "medium" {
		t.Fatalf("main = %+v", main)
	}
	if review.NativeID != "agent-1" || review.ParentNativeID != "sess-1" || review.AgentDepth != 1 || review.AgentNickname != "app-review" {
		t.Fatalf("subagent = %+v", review)
	}
	kinds := func(messages []MessageRecord) string {
		out := []string{}
		for _, message := range messages {
			out = append(out, message.Role+":"+message.Kind)
		}
		return strings.Join(out, " ")
	}
	if got, want := kinds(main.Messages), "user:message system:context system:metadata assistant:reasoning assistant:tool_call assistant:tool_call tool:tool_result tool:tool_result system:metadata assistant:message assistant:delegation tool:delegation_result system:metadata assistant:message user:message"; got != want {
		t.Fatalf("main messages\n got %s\nwant %s", got, want)
	}
	if got, want := kinds(review.Messages), agentRole+":message system:metadata assistant:message"; got != want {
		t.Fatalf("subagent messages\n got %s\nwant %s", got, want)
	}
	byID := map[string]MessageRecord{}
	for _, message := range main.Messages {
		byID[message.NativeID] = message
	}
	if reasoning := byID["a1:reasoning"]; reasoning.Text != "Checking the files" || reasoning.ReasoningFormat != "summary" {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if call, result := byID["call:c1"], byID["result:c1"]; call.CallID != "c1" || call.CreatedAt != "2026-11-03T15:00:05.200Z" || result.Text != `{"content":"app.py","details":{"duration_ms":500,"exit_code":0},"is_error":false,"tool":"bash"}` {
		t.Fatalf("call %+v result %+v", call, result)
	}
	if failed := byID["result:c2"]; !strings.Contains(failed.Text, `"is_error":true`) || !strings.Contains(failed.Text, "no such file") {
		t.Fatalf("failed result = %+v", failed)
	}
	if user := review.Messages[0]; user.Sender != "agent:sess-1" || user.Text != "Review app.py" {
		t.Fatalf("subagent prompt = %+v", user)
	}

	// The legacy log has no receipts, so its shutdown total is its usage.
	legacy := records["sess-0"]
	if len(legacy.Conversations) != 1 || legacy.Conversations[0].HarnessVersionLast != "0.0.330" {
		t.Fatalf("legacy = %+v", legacy)
	}
	usage := legacy.Conversations[0].Messages[len(legacy.Conversations[0].Messages)-1]
	if usage.Kind != "metadata" || usage.Model != "claude-sonnet-4.5" || !strings.Contains(usage.Text, `"source":"shutdown_rollup"`) {
		t.Fatalf("legacy usage = %+v", usage)
	}
}

func TestCopilotUsageCountsEachRequestOnce(t *testing.T) {
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "copilot", Kind: "copilot", Path: copilotFixture(t), Account: "local", Enabled: true})
	var total, sessions int
	// Receipts: 1050 + 1280 + 540 (subagent) + 1330; the shutdown's 4200
	// repeats them. The legacy session's shutdown adds 320.
	if err := c.DB.QueryRow(`SELECT COALESCE(SUM(u.total_tokens),0), COUNT(DISTINCT a.conversation_id) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id`).Scan(&total, &sessions); err != nil {
		t.Fatal(err)
	}
	if total != 1050+1280+540+1330+320 || sessions != 3 {
		t.Fatalf("usage %d tokens over %d sessions", total, sessions)
	}
	var subagent int
	if err := c.DB.QueryRow(`SELECT COALESCE(SUM(u.total_tokens),0) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN conversations v ON v.id=a.conversation_id WHERE v.native_id='agent-1'`).Scan(&subagent); err != nil || subagent != 540 {
		t.Fatalf("subagent usage %d %v", subagent, err)
	}
}

func TestCopilotCaptureHoldsOnlyTheSessions(t *testing.T) {
	root := copilotFixture(t)
	plan, err := planCapture(SourceConfig{Name: "copilot", Kind: "copilot", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range plan.files {
		got = append(got, item.rel)
	}
	if strings.Join(got, ",") != "session-state/sess-0.jsonl,session-state/sess-1/events.jsonl,session-state/sess-1/workspace.yaml" {
		t.Fatalf("capture plan = %v", got)
	}
}

func TestCopilotSessionsAreCapturedIndexedElsewhereAndFollowed(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := copilotFixture(t)
	source := SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	messages := func(c *Catalog) int {
		var count int
		if err := c.DB.QueryRow(`SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='copilot'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}

	// A session still running gains events; only its files, the log and
	// workspace.yaml, are read again.
	useHost(t, "host-a")
	log := filepath.Join(root, "session-state", "sess-1", "events.jsonl")
	file, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_, err = file.WriteString(`{"type":"assistant.message","data":{"messageId":"a5","model":"gpt-6-luna","content":"You're welcome.","toolRequests":[]},"id":"e25","timestamp":"2026-11-03T15:05:02.000Z"}` + "\n")
	}
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	parsed := ingestSource(t, c, source).Parsed
	if got := messages(c); got != live+1 || parsed != 2 {
		t.Fatalf("after the append: %d messages (was %d), %d files parsed", got, live, parsed)
	}
}
