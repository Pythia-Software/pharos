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
{"type":"assistant.message","data":{"messageId":"a1","apiCallId":"api-1","model":"gpt-6-luna","content":"","reasoningText":"Checking the files","reasoningBlocks":{"provider":"openai-responses"},"toolRequests":[{"toolCallId":"c1","name":"bash","arguments":{"command":"ls"}},{"toolCallId":"c2","name":"view","arguments":{"path":"missing.py"}}]},"id":"e7","timestamp":"2026-11-03T15:00:05.100Z"}
{"type":"tool.execution_start","data":{"toolCallId":"c1","toolName":"bash"},"id":"e8","timestamp":"2026-11-03T15:00:05.200Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c1","success":true,"result":{"content":"app.py"},"shellExecution":{"exitCode":0}},"id":"e9","timestamp":"2026-11-03T15:00:05.700Z"}
{"type":"tool.execution_start","data":{"toolCallId":"c2","toolName":"view"},"id":"e10","timestamp":"2026-11-03T15:00:05.800Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c2","success":false,"error":{"message":"no such file"}},"id":"e11","timestamp":"2026-11-03T15:00:05.900Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":1200,"outputTokens":80,"cacheReadTokens":900,"cacheWriteTokens":250,"reasoningTokens":0,"apiCallId":"api-2"}},"id":"e12","timestamp":"2026-11-03T15:00:08.000Z"}
{"type":"assistant.message","data":{"messageId":"a2","apiCallId":"api-2","model":"gpt-6-luna","content":"Asking for a review.","toolRequests":[{"toolCallId":"c3","name":"task","arguments":{"prompt":"Review app.py"}}]},"id":"e13","timestamp":"2026-11-03T15:00:08.100Z"}
{"type":"subagent.started","data":{"toolCallId":"c3","agentName":"code-review","agentDisplayName":"app-review","model":"gpt-6-luna"},"agentId":"agent-1","id":"e14","timestamp":"2026-11-03T15:00:08.200Z"}
{"type":"user.message","data":{"content":"Review app.py","source":"agent-sess-1","messageId":"u2"},"agentId":"agent-1","id":"e15","timestamp":"2026-11-03T15:00:08.300Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":500,"outputTokens":40,"cacheReadTokens":0,"cacheWriteTokens":450,"reasoningTokens":10,"apiCallId":"api-3"}},"agentId":"agent-1","id":"e16","timestamp":"2026-11-03T15:00:10.000Z"}
{"type":"assistant.message","data":{"messageId":"a3","apiCallId":"api-3","model":"gpt-6-luna","content":"Looks fine.","toolRequests":[]},"agentId":"agent-1","id":"e17","timestamp":"2026-11-03T15:00:10.100Z"}
{"type":"subagent.completed","data":{"toolCallId":"c3"},"agentId":"agent-1","id":"e18","timestamp":"2026-11-03T15:00:10.200Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c3","success":true,"result":{"content":"Looks fine."}},"id":"e19","timestamp":"2026-11-03T15:00:10.300Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":1300,"outputTokens":30,"cacheReadTokens":1150,"cacheWriteTokens":140,"reasoningTokens":0,"apiCallId":"api-4"}},"id":"e20","timestamp":"2026-11-03T15:00:12.000Z"}
{"type":"assistant.message","data":{"messageId":"a4","apiCallId":"api-4","model":"gpt-6-luna","content":"Added the counter.","toolRequests":[]},"id":"e21","timestamp":"2026-11-03T15:00:12.100Z"}
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
	// An upgraded session's flat log, which its folder's log repeats.
	writeSourceFile(t, filepath.Join(sessions, "sess-1.jsonl"), strings.Join(strings.SplitAfter(copilotSessionEvents, "\n")[:3], ""))
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
	if call, result := byID["a1:block:0"], byID["result:c1"]; call.CallID != "c1" || call.CreatedAt != "2026-11-03T15:00:05.200Z" || result.Text != `{"content":"app.py","details":{"duration_ms":500,"exit_code":0},"is_error":false,"tool":"bash"}` {
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
	if usage.Kind != "metadata" || usage.Model != "claude-sonnet-4.5" || !strings.Contains(usage.Text, `"source":"shutdown_residual"`) {
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
	// Each call shares the output of the request that made it: c1 and c2
	// split api-1's 50, and c3 has api-2's 80, not the next request's.
	rows, err := c.DB.Query(`SELECT call_id, output_tokens FROM tool_calls WHERE provider='copilot' ORDER BY call_id`)
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
	if strings.Join(got, " ") != "c1=25 c2=25 c3=80" {
		t.Fatalf("output tokens per call: %v", got)
	}
}

// copilotUpgraded is a session begun by a release that wrote no receipts and
// resumed by one that does: each shutdown totals the whole session. Its
// requests are reported more than once, first in part. One is reported under
// the provider's ID, then the API's with its model, then corrected under the
// provider's, completed under the API's, and joined by a report of both. Another's model is corrected
// after the shutdown. A prompt quotes usage JSON.
const copilotUpgraded = `{"type":"session.start","data":{"sessionId":"sess-3","copilotVersion":"0.0.330"},"id":"m1","timestamp":"2026-01-02T10:00:00.000Z"}
{"type":"user.message","data":{"content":"Hello"},"id":"m2","timestamp":"2026-01-02T10:00:01.000Z"}
{"type":"assistant.message","data":{"messageId":"b1","content":"Hi","model":"claude-sonnet-4.5"},"id":"m3","timestamp":"2026-01-02T10:00:02.000Z"}
{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.5":{"usage":{"inputTokens":300,"outputTokens":20,"cacheReadTokens":100,"cacheWriteTokens":0}}}},"id":"m4","timestamp":"2026-01-02T10:00:03.000Z"}
{"type":"session.resume","data":{},"id":"m5","timestamp":"2026-01-03T10:00:00.000Z"}
{"type":"user.message","data":{"content":"{\"usage\":{\"input_tokens\":1000,\"output_tokens\":200}}"},"id":"m6","timestamp":"2026-01-03T10:00:01.000Z"}
{"type":"session.usage_record","data":{"usage":{"model":"claude-sonnet-4.5","inputTokens":90,"cacheReadTokens":80,"providerCallId":"p-9"}},"id":"m7a","timestamp":"2026-01-03T10:00:01.500Z"}
{"type":"session.usage_record","data":{"usage":{"model":"claude-sonnet-4.5","inputTokens":90,"outputTokens":10,"cacheReadTokens":80,"cacheWriteTokens":0,"apiCallId":"api-9","providerCallId":"p-9"}},"id":"m7","timestamp":"2026-01-03T10:00:02.000Z"}
{"type":"assistant.message","data":{"messageId":"b2","content":"Hi again","model":"claude-sonnet-4.5","apiCallId":"api-9"},"id":"m8","timestamp":"2026-01-03T10:00:02.100Z"}
{"type":"session.usage_record","data":{"usage":{"inputTokens":40,"providerCallId":"p-10"}},"id":"m10a","timestamp":"2026-01-03T10:00:02.500Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-5-mini","inputTokens":40,"apiCallId":"api-10"}},"id":"m10b","timestamp":"2026-01-03T10:00:02.550Z"}
{"type":"session.usage_record","data":{"usage":{"inputTokens":50,"providerCallId":"p-10"}},"id":"m10c","timestamp":"2026-01-03T10:00:02.560Z"}
{"type":"session.usage_record","data":{"usage":{"outputTokens":5,"apiCallId":"api-10"}},"id":"m10d","timestamp":"2026-01-03T10:00:02.570Z"}
{"type":"session.usage_record","data":{"usage":{"apiCallId":"api-10","providerCallId":"p-10"}},"id":"m10e","timestamp":"2026-01-03T10:00:02.600Z"}
{"type":"session.usage_record","data":{"usage":{"inputTokens":20,"apiCallId":"api-11"}},"id":"m11a","timestamp":"2026-01-03T10:00:02.700Z"}
{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.5":{"usage":{"inputTokens":390,"outputTokens":30,"cacheReadTokens":180,"cacheWriteTokens":0}},"gpt-5-mini":{"usage":{"inputTokens":70,"outputTokens":5}}}},"id":"m9","timestamp":"2026-01-03T10:00:03.000Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-5-mini","inputTokens":20,"apiCallId":"api-11"}},"id":"m11","timestamp":"2026-01-03T10:00:03.100Z"}
`

func TestCopilotUsageBeyondReceiptsIsKeptOnce(t *testing.T) {
	c, _ := testCatalog(t)
	root := t.TempDir()
	source := SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true}
	total := func() int {
		return countRows(t, c, `SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage`)
	}
	// The receiptless leg's 320 plus the receipts' 100, 55 and 20, and
	// nothing from the quoted JSON: while the session is open, and once its
	// last shutdown totals it.
	log := filepath.Join(root, "session-state", "sess-3", "events.jsonl")
	writeSourceFile(t, log, copilotUpgraded[:strings.Index(copilotUpgraded, `{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.5":{"usage":{"inputTokens":390`)])
	ingestSource(t, c, source)
	if got := total(); got != 495 {
		t.Fatalf("open session: %d tokens, want 495", got)
	}
	writeSourceFile(t, log, copilotUpgraded)
	ingestSource(t, c, source)
	if got := total(); got != 495 {
		t.Fatalf("upgraded session: %d tokens, want 495", got)
	}
	// A receipt takes its request's message and model from the later report.
	for id, model := range map[string]string{"b2:usage": "claude-sonnet-4.5", "m10a": "gpt-5-mini", "m11a": "gpt-5-mini"} {
		if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id=? AND model=?`, id, model); n != 1 {
			t.Fatalf("receipt %s under %s: %d", id, model, n)
		}
	}

	// A flat log moved into a folder and resumed there: its larger total
	// replaces or leaves the earlier one, never adds to it.
	flat := filepath.Join(root, "session-state", "sess-0.jsonl")
	writeSourceFile(t, flat, copilotLegacy)
	ingestSource(t, c, source)
	if got := total(); got != 495+320 {
		t.Fatalf("flat session: %d tokens, want %d", got, 495+320)
	}
	resumed := copilotLegacy + strings.ReplaceAll(strings.ReplaceAll(copilotLegacy, `"id":"l`, `"id":"r`), `"inputTokens":300,"outputTokens":20`, `"inputTokens":600,"outputTokens":40`)
	writeSourceFile(t, filepath.Join(root, "session-state", "sess-0", "events.jsonl"), resumed)
	if err := os.Remove(flat); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, c, source)
	if got := countRows(t, c, `SELECT COALESCE(SUM(u.total_tokens),0) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN conversations v ON v.id=a.conversation_id WHERE v.native_id='sess-0'`); got > 640 {
		t.Fatalf("moved session: %d tokens, more than its last total", got)
	}
}

// A capture can copy a session's log before an append and its
// workspace.yaml after; reading that capture must not undo the append.
func TestCopilotCaptureOfAnOlderLogLeavesTheNewerOne(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "capture", true: "capture kept until indexed"}[pending], func(t *testing.T) {
			useHost(t, "host-a")
			root := copilotFixture(t)
			source := SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true}
			session := filepath.Join(root, "session-state", "sess-1")
			c, config := testCatalog(t)
			config.Sources, config.PendingCapture = []SourceConfig{source}, pending
			// The yaml copied after the log, with a later time.
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(session, "workspace.yaml"), later, later); err != nil {
				t.Fatal(err)
			}
			if result := runCapture(t, config); !result.OK {
				t.Fatalf("capture = %+v", result)
			}
			log := filepath.Join(session, "events.jsonl")
			appendSourceFile(t, log, `{"type":"assistant.message","data":{"messageId":"a5","model":"gpt-6-luna","content":"You're welcome.","toolRequests":[]},"id":"e25","timestamp":"2026-11-03T15:05:02.000Z"}`+"\n")
			appended := time.Now().Add(30 * time.Minute)
			if err := os.Chtimes(log, appended, appended); err != nil {
				t.Fatal(err)
			}
			useHost(t, "host-a")
			ingestSource(t, c, source)
			// Another Mac sharing the library reads this Mac's capture.
			useHost(t, "host-b")
			indexCaptures(t, c, config.CaptureRoot, "host-a")
			if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE native_id='a5'`); n != 1 {
				t.Fatalf("the appended reply was removed: %d", n)
			}
		})
	}
}

// One session's flat log and folder log are read together: the folder's need
// not repeat the turns the flat one holds. Events without IDs, here each
// log's prompt, stay apart, and each record points to its own log.
func TestCopilotFlatAndFolderLogsOfOneSessionAreReadTogether(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "session-state")
	writeSourceFile(t, filepath.Join(sessions, "sess-5.jsonl"), strings.ReplaceAll(strings.ReplaceAll(copilotLegacy, "sess-0", "sess-5"), `,"id":"l2"`, ""))
	// Another flat log is moved into a folder rather than joined by one.
	moved := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(copilotLegacy, "sess-0", "sess-6"), `,"id":"l2"`, ""), "Hello", "Moved")
	writeSourceFile(t, filepath.Join(sessions, "sess-6.jsonl"), moved)
	// The flat logs are indexed alone first, then with the folders a later
	// release starts: their records keep their IDs.
	c, _ := testCatalog(t)
	source := SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	writeSourceFile(t, filepath.Join(sessions, "sess-5", "events.jsonl"), `{"type":"session.resume","data":{},"id":"r1","timestamp":"2026-01-03T10:00:00.000Z"}
{"type":"user.message","data":{"content":"Again"},"timestamp":"2026-01-03T10:00:01.000Z"}
`)
	writeSourceFile(t, filepath.Join(sessions, "sess-6", "events.jsonl"), moved+`{"type":"user.message","data":{"content":"Moved again"},"timestamp":"2026-01-03T10:00:01.000Z"}
`)
	if err := os.Remove(filepath.Join(sessions, "sess-6.jsonl")); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, c, source)
	for _, text := range []string{"Hello", "Again", "Moved", "Moved again"} {
		if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE role='user' AND text=?`, text); n != 1 {
			t.Fatalf("%d prompts %q", n, text)
		}
	}
	records := discoverCopilot(t, root)
	texts, ids, residual := []string{}, map[string]bool{}, ""
	for _, message := range records["sess-5"].Conversations[0].Messages {
		if ids[message.NativeID] {
			t.Fatalf("two records are %s", message.NativeID)
		}
		ids[message.NativeID] = true
		if message.Kind == "message" {
			texts = append(texts, message.Text)
		}
		if strings.Contains(message.Text, "shutdown_residual") {
			residual = message.EvidenceLocator
		}
	}
	if strings.Join(texts, "|") != "Hello|Hi|Again" {
		t.Fatalf("messages = %v", texts)
	}
	if !strings.HasSuffix(residual, "sess-5.jsonl:4") {
		t.Fatalf("shutdown usage points to %q", residual)
	}
}

// A session renamed in Copilot is renamed in Pharos too, though its log, which
// versions it, is unchanged; also when sessions are read from retained copies.
func TestCopilotRenameIsReadWithoutANewTurn(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "retained"}[retained], func(t *testing.T) {
			c, _ := testCatalog(t)
			root := copilotFixture(t)
			source := SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": retained}}
			ingestSource(t, c, source)
			yaml := filepath.Join(root, "session-state", "sess-1", "workspace.yaml")
			writeSourceFile(t, yaml, strings.Replace(copilotWorkspace, "Word counter: first pass", "Word counter, renamed", 1))
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(yaml, later, later); err != nil {
				t.Fatal(err)
			}
			ingestSource(t, c, source)
			if n := countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE title='Word counter, renamed'`); n != 1 {
				t.Fatalf("renamed workspaces: %d", n)
			}
		})
	}
}

// Copilot is offered to a Mac set up before Pharos read it, chosen in
// guided setup, and followed by automatic updates.
func TestCopilotIsSetUpAndFollowedLikeOtherTools(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(home, ".copilot", "session-state", "sess-1", "events.jsonl"), copilotSessionEvents)
	path := probeLibrary(t, "")
	server := probeServer(t, path)
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before Copilot = %v", status)
	}
	code, result := probeCall(t, server, http.MethodPost, "/api/probe", `{"kinds":["copilot"]}`)
	if code != 200 || !strings.Contains(jsonText(result["candidates"]), `"kind":"copilot"`) {
		t.Fatalf("guided probe = %d %v", code, result)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["copilot"],"accept":["copilot"]}`); code != 200 || jsonText(result["added"]) != `["copilot"]` {
		t.Fatalf("accept copilot = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(0) {
		t.Fatalf("a chosen source is still offered: %v", status)
	}

	root := copilotFixture(t)
	automatic := automaticTestServer(t, SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true})
	appendSourceFile(t, filepath.Join(root, "session-state", "sess-1", "events.jsonl"), `{"type":"assistant.message","data":{"messageId":"a5","model":"gpt-6-luna","content":"You're welcome.","toolRequests":[]},"id":"e25","timestamp":"2026-11-03T15:05:02.000Z"}`+"\n")
	automatic.runAutomatic(automatic.life.ctx, syncSettings{Interval: 300})
	if n := countRows(t, automatic.Catalog, `SELECT COUNT(*) FROM messages WHERE native_id='a5'`); n != 1 {
		t.Fatalf("automatic updates skipped Copilot: %d", n)
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
	if strings.Join(got, ",") != "session-state/sess-0.jsonl,session-state/sess-1.jsonl,session-state/sess-1/events.jsonl,session-state/sess-1/workspace.yaml" {
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

	// A session still running gains events; only its files, its logs and
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
	if got := messages(c); got != live+1 || parsed != 3 {
		t.Fatalf("after the append: %d messages (was %d), %d files parsed", got, live, parsed)
	}
}

// copilotNewerEvents is a session in the shapes newer releases write: effort
// set at the start and reset by a model change; a selection attached to a
// prompt; a skill's hidden input and autopilot's continuation; one response
// split into two messages; a subagent that starts another; a compaction; a
// tool the person ran; and a request whose effort a later report corrects.
const copilotNewerEvents = `{"type":"session.start","data":{"sessionId":"sess-n","copilotVersion":"1.0.105","reasoningEffort":"high","context":{"cwd":"/work/n"}},"id":"n1","timestamp":"2026-11-04T10:00:00.000Z"}
{"type":"user.message","data":{"content":"Inspect this","messageId":"u1","attachments":[{"type":"selection","displayName":"q.sql","filePath":"/work/n/q.sql","text":"SELECT secret_column FROM t","selection":{"start":{"line":0,"character":0},"end":{"line":0,"character":27}}}]},"id":"n2","timestamp":"2026-11-04T10:00:01.000Z"}
{"type":"user.message","data":{"content":"Use the PDF tools as follows.","messageId":"u-skill","source":"skill-pdf"},"id":"n3","timestamp":"2026-11-04T10:00:01.500Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":100,"outputTokens":60,"apiCallId":"api-1"}},"id":"n4","timestamp":"2026-11-04T10:00:02.000Z"}
{"type":"assistant.message","data":{"messageId":"m1","apiCallId":"api-1","chunkIndex":0,"chunkCount":2,"model":"gpt-6-luna","content":"First,","toolRequests":[{"toolCallId":"c1","name":"bash","arguments":{"command":"ls"}}]},"id":"n5","timestamp":"2026-11-04T10:00:02.100Z"}
{"type":"assistant.message","data":{"messageId":"m2","apiCallId":"api-1","chunkIndex":1,"chunkCount":2,"model":"gpt-6-luna","content":"then","toolRequests":[{"toolCallId":"c2","name":"bash","arguments":{"command":"pwd"}}]},"id":"n6","timestamp":"2026-11-04T10:00:02.200Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c1","success":true,"result":{"content":"q.sql"}},"id":"n7","timestamp":"2026-11-04T10:00:03.000Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c2","success":true,"result":{"content":"/work/n"}},"id":"n8","timestamp":"2026-11-04T10:00:03.100Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":200,"outputTokens":10,"apiCallId":"api-2"}},"id":"n9","timestamp":"2026-11-04T10:00:04.000Z"}
{"type":"assistant.message","data":{"messageId":"m3","apiCallId":"api-2","model":"gpt-6-luna","content":"","toolRequests":[{"toolCallId":"c3","name":"task","arguments":{"prompt":"Check q.sql"}}]},"id":"n10","timestamp":"2026-11-04T10:00:04.100Z"}
{"type":"subagent.started","data":{"toolCallId":"c3","agentName":"explore"},"agentId":"outer","id":"n11","timestamp":"2026-11-04T10:00:04.200Z"}
{"type":"user.message","data":{"content":"Check q.sql","messageId":"o1","source":"agent-sess-n"},"agentId":"outer","id":"n12","timestamp":"2026-11-04T10:00:04.300Z"}
{"type":"assistant.message","data":{"messageId":"m4","model":"gpt-6-luna","content":"","toolRequests":[{"toolCallId":"c4","name":"task","arguments":{"prompt":"Read the schema"}}]},"agentId":"outer","id":"n13","timestamp":"2026-11-04T10:00:05.000Z"}
{"type":"subagent.started","data":{"toolCallId":"c4","agentName":"explore"},"agentId":"inner","id":"n14","timestamp":"2026-11-04T10:00:05.100Z"}
{"type":"user.message","data":{"content":"Read the schema","messageId":"i1","source":"agent-outer"},"agentId":"inner","id":"n15","timestamp":"2026-11-04T10:00:05.200Z"}
{"type":"assistant.message","data":{"messageId":"m5","model":"gpt-6-luna","content":"One table."},"agentId":"inner","id":"n16","timestamp":"2026-11-04T10:00:06.000Z"}
{"type":"session.compaction_complete","data":{"success":true,"summaryContent":"Inspected q.sql."},"id":"n17","timestamp":"2026-11-04T10:00:07.000Z"}
{"type":"user.message","data":{"content":"Continue with the task.","messageId":"u2","isAutopilotContinuation":true},"id":"n18","timestamp":"2026-11-04T10:00:08.000Z"}
{"type":"tool.user_requested","data":{"toolCallId":"c5","toolName":"bash","arguments":{"command":"git status"}},"id":"n19","timestamp":"2026-11-04T10:00:09.000Z"}
{"type":"tool.execution_start","data":{"toolCallId":"c5","toolName":"bash"},"id":"n20","timestamp":"2026-11-04T10:00:09.100Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"c5","success":true,"result":{"content":"clean"},"isUserRequested":true},"id":"n21","timestamp":"2026-11-04T10:00:09.600Z"}
{"type":"session.model_change","data":{"newModel":"gpt-6-sol","reasoningEffort":null},"id":"n22","timestamp":"2026-11-04T10:00:10.000Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-sol","inputTokens":300,"outputTokens":20,"apiCallId":"api-5"}},"id":"n23","timestamp":"2026-11-04T10:00:11.000Z"}
{"type":"assistant.message","data":{"messageId":"m6","apiCallId":"api-5","model":"gpt-6-sol","content":"Reset."},"id":"n24","timestamp":"2026-11-04T10:00:11.100Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-sol","inputTokens":400,"outputTokens":20,"reasoningEffort":"low","apiCallId":"api-6"}},"id":"n25","timestamp":"2026-11-04T10:00:12.000Z"}
{"type":"assistant.message","data":{"messageId":"m7","apiCallId":"api-6","model":"gpt-6-sol","content":"Done.","toolRequests":[{"toolCallId":"c7","name":"bash","arguments":{"command":"make"}}]},"id":"n27","timestamp":"2026-11-04T10:00:12.200Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-sol","inputTokens":400,"outputTokens":30,"reasoningEffort":"high","apiCallId":"api-6"}},"id":"n26","timestamp":"2026-11-04T10:00:13.000Z"}
`

func TestCopilotNewerEventShapes(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "session-state", "sess-n", "events.jsonl"), copilotNewerEvents)
	session := discoverCopilot(t, root)["sess-n"]
	conversations := map[string]ConversationRecord{}
	for _, conversation := range session.Conversations {
		conversations[conversation.NativeID] = conversation
	}
	main := conversations["sess-n"]
	byID := map[string]MessageRecord{}
	for _, conversation := range session.Conversations {
		for _, message := range conversation.Messages {
			byID[message.NativeID] = message
		}
	}
	// What the person wrote, with what they selected; not the skill's or
	// autopilot's input.
	if session.Purpose != "Inspect this" || !strings.Contains(byID["u1:attachment:0"].Text, "SELECT secret_column FROM t") || byID["u1:attachment:0"].Kind != "attachment" {
		t.Fatalf("purpose %q, selection %+v", session.Purpose, byID["u1:attachment:0"])
	}
	if skill := byID["u-skill"]; skill.Kind != "context" || skill.Role != "system" {
		t.Fatalf("skill input = %+v", skill)
	}
	if continuation := byID["u2"]; continuation.Role != "user" || continuation.Sender != "automation:copilot-autopilot" {
		t.Fatalf("autopilot continuation = %+v", continuation)
	}
	// Effort from the start, until a model change resets it; a request's
	// effort as last reported.
	if byID["m1"].Effort != "high" || byID["m6"].Effort != "" {
		t.Fatalf("efforts: m1 %q m6 %q", byID["m1"].Effort, byID["m6"].Effort)
	}
	// A receipt's corrected effort reaches the request's message and calls.
	if receipt, call := byID["m7:usage"], byID["m7:block:0"]; receipt.Effort != "high" || byID["m7"].Effort != "high" || call.Effort != "high" {
		t.Fatalf("corrected receipt = %+v, call %+v", receipt, call)
	}
	// The inner subagent belongs to the one that started it.
	if inner, outer := conversations["inner"], conversations["outer"]; inner.ParentNativeID != "outer" || inner.AgentDepth != 2 || outer.ParentNativeID != "sess-n" || outer.AgentDepth != 1 {
		t.Fatalf("inner %s/%d, outer %s/%d", inner.ParentNativeID, inner.AgentDepth, outer.ParentNativeID, outer.AgentDepth)
	}
	// One request's two messages share its output; a compaction is marked;
	// the person's own tool run is a call.
	requests, calls := buildToolLedger(main.Messages, main.Model)
	cost := map[string]float64{}
	for _, call := range calls {
		cost[call.CallID] = call.OutputTokens
	}
	// The person's own run took no request's output.
	if spent, ran := cost["c5"]; cost["c1"] != 30 || cost["c2"] != 30 || !ran || spent != 0 {
		t.Fatalf("tool costs %v", cost)
	}
	if compaction := byID["n17"]; !isCompactionMarker(compaction) || !strings.Contains(compaction.Text, "Inspected q.sql.") {
		t.Fatalf("compaction = %+v", compaction)
	}
	if call := byID["c5:user"]; call.Kind != "tool_call" || call.CallID != "c5" || !strings.Contains(call.Text, "git status") {
		t.Fatalf("user-requested call = %+v", call)
	}
	total := 0.0
	for _, request := range requests {
		total += request.Counts["output_tokens"]
	}
	if total != 60+10+20+30 {
		t.Fatalf("requests' output tokens = %v (%+v)", total, requests)
	}
	// Indexed, the split response is counted once.
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "copilot", Kind: "copilot", Path: root, Account: "local", Enabled: true})
	if tokens := countRows(t, c, "SELECT CAST(COALESCE(SUM(total_tokens),0) AS INTEGER) FROM agent_session_usage"); tokens != 160+210+320+430 {
		t.Fatalf("indexed tokens = %d", tokens)
	}
}
