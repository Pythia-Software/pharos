package archive

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// shapeScript builds a session's inputs in order: each step adds messages at
// a second offset from 10:00 UTC, so positions follow the steps.
type shapeScript struct {
	in sessionShapeInput
}

var shapeStart = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).UnixMilli()

func (s *shapeScript) message(role, kind string, at int) *shapeMessage {
	s.in.messages = append(s.in.messages, shapeMessage{id: fmt.Sprintf("m%d", len(s.in.messages)), role: role, kind: kind, at: shapeStart + int64(at)*1000, timed: true})
	return &s.in.messages[len(s.in.messages)-1]
}

// typed is a user message with typed text; pasted one with none.
func (s *shapeScript) typed(at int) *shapeScript {
	m := s.message("user", "message", at)
	m.authored, m.typed = true, true
	return s
}

func (s *shapeScript) correction(at int) *shapeScript {
	m := s.message("user", "message", at)
	m.authored, m.typed, m.correction = true, true, true
	return s
}

func (s *shapeScript) pasted(at int) *shapeScript {
	s.message("user", "message", at).authored = true
	return s
}

func (s *shapeScript) reply(at int) *shapeScript {
	s.message("assistant", "message", at)
	return s
}

// call adds a tool call and its result. spec is "tool category" with
// optional key=value settings: status, error, command (a command category),
// file, input, test.
func (s *shapeScript) call(at int, spec string) *shapeScript {
	fields := strings.Fields(spec)
	call := shapeCall{tool: fields[0], category: fields[1], status: "ok", input: spec}
	for _, setting := range fields[2:] {
		key, value, _ := strings.Cut(setting, "=")
		switch key {
		case "status":
			call.status = value
		case "error":
			call.status, call.errors = "error", value
		case "command":
			call.commandCategory = value
		case "file":
			call.file = value
		case "input":
			call.input = value
		case "test":
			call.testFailure = true
		case "interrupted":
			call.interrupted = true
		}
	}
	call.message, call.position = fmt.Sprintf("m%d", len(s.in.messages)), len(s.in.messages)
	s.message("assistant", "tool_call", at)
	s.message("tool", "tool_result", at)
	s.in.calls = append(s.in.calls, call)
	s.in.toolMessages++
	return s
}

func (s *shapeScript) compaction() *shapeScript {
	s.in.compactions = append(s.in.compactions, shapeCompaction{id: fmt.Sprintf("c%d", len(s.in.compactions)), position: len(s.in.messages)})
	return s
}

func (s *shapeScript) derive() (sessionShapeFacts, sessionShapeEvidence) {
	return deriveSessionShape(s.in)
}

func shapeValue[T int64 | float64](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestSessionShapeSignals(t *testing.T) {
	edit := func(file string) string { return "Edit edit file=" + file + " input=" + file + "-change" }
	cases := []struct {
		name   string
		script *shapeScript
		check  func(sessionShapeFacts, sessionShapeEvidence) any
		want   any
	}{
		{"a 300 s gap is active, a 301 s gap idle", new(shapeScript).typed(0).reply(300).reply(601),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{shapeValue(f.ActiveSeconds), shapeValue(f.WallSeconds)}
			}, []any{300.0, 601.0}},
		{"pasted text is no typed message and no correction", new(shapeScript).pasted(0).reply(5),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.TypedHumanMessages), shapeValue(f.Corrections), f.HasAuthorship, len(e["corrections"])}
			}, []any{int64(0), int64(0), true, 0}},
		{"a typed correction counts with its message", new(shapeScript).typed(0).reply(5).correction(60).reply(70),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.Corrections), e["corrections"]}
			}, []any{int64(1), []string{"m2"}}},
		{"a prompt awaiting authorship leaves typed counts empty", func() *shapeScript {
			s := new(shapeScript).typed(0).reply(5)
			s.message("user", "message", 60)
			return s.reply(65)
		}(),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{f.HasAuthorship, shapeValue(f.TypedHumanMessages), shapeValue(f.Corrections)}
			}, []any{false, nil, nil}},
		{"a rejection is not a failure", new(shapeScript).typed(0).call(1, "Bash command error=user_rejected").call(2, "Bash command error=hook_blocked").call(3, "Bash command interrupted").call(4, "Bash command error=nonzero_exit"),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.FailedCalls), shapeValue(f.Rejections), shapeValue(f.HookBlocks), shapeValue(f.Interrupts), e["failed_calls"]}
			}, []any{int64(1), int64(1), int64(1), int64(1), []string{"m7"}}},
		{"failure streaks count consecutive failures only", new(shapeScript).typed(0).call(1, "Bash command error=nonzero_exit input=a").call(2, "Bash command error=nonzero_exit input=b").call(3, "Read read").call(4, "Bash command error=timeout input=c"),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.FailureStreakMax), e["failure_streak"]}
			}, []any{int64(2), []string{"m1", "m3"}}},
		{"three identical calls are two retries; two are none", new(shapeScript).typed(0).call(1, "Read read input=x").call(2, "Read read input=x").call(3, "Read read input=x").call(4, "Grep search input=y").call(5, "Grep search input=y"),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.IdenticalRetries), e["identical_retries"]}
			}, []any{int64(2), []string{"m3", "m5"}}},
		{"edit, test, edit is thrash", new(shapeScript).typed(0).call(1, edit("a.go")).call(2, "Bash command command=test input=go-test").call(3, edit("a.go")).reply(4),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.ThrashRetries), shapeValue(f.EditTurns), shapeValue(f.OneShotEditTurns), e["thrash_retries"]}
			}, []any{int64(1), int64(1), int64(0), []string{"m5"}}},
		{"a read-only command between two edits is not thrash", new(shapeScript).typed(0).call(1, edit("a.go")).call(2, "Bash command command=read input=cat").call(3, "Bash command command=search input=rg").call(4, edit("a.go")),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{shapeValue(f.ThrashRetries), shapeValue(f.OneShotEditTurns)}
			}, []any{int64(0), int64(1)}},
		{"a typed message ends the turn, so its next edit is no thrash", new(shapeScript).typed(0).call(1, edit("a.go")).call(2, "Bash command command=build").reply(3).typed(60).call(61, edit("a.go")),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{shapeValue(f.ThrashRetries), shapeValue(f.EditTurns)}
			}, []any{int64(0), int64(2)}},
		{"a failed test command still verifies", new(shapeScript).typed(0).call(1, edit("a.go")).call(2, "Bash command command=run test input=pytest").call(3, edit("a.go")),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any { return shapeValue(f.ThrashRetries) }, int64(1)},
		{"three nearby edits of one file churn; spread out they do not", new(shapeScript).typed(0).call(1, edit("a.go")).call(2, edit("a.go")+"2").call(3, edit("a.go")+"3").
			call(4, edit("b.go")).reply(5).reply(6).reply(7).reply(8).reply(9).reply(10).reply(11).reply(12).call(13, edit("b.go")+"2").reply(14).reply(15).reply(16).reply(17).reply(18).reply(19).reply(20).reply(21).call(22, edit("b.go")+"3"),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.ChurnedFiles), shapeValue(f.FilesEdited), e["churned_files"]}
			},
			[]any{int64(1), int64(2), []string{"m1", "m3", "m5"}}},
		{"a compaction amid shared tools is mid-task", new(shapeScript).typed(0).call(1, "Read read input=1").call(2, "Edit edit file=x input=2").call(3, "Bash command input=3").compaction().call(4, "Read read input=4").call(5, "Edit edit file=x input=5"),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.MidTaskCompactions), f.Compactions, e["mid_task_compactions"]}
			},
			[]any{int64(1), int64(1), []string{"c0"}}},
		{"a compaction between unrelated work is not", new(shapeScript).typed(0).call(1, "Read read input=1").call(2, "Edit edit file=x input=2").reply(3).compaction().typed(60).call(61, "Read read input=4").call(62, "Grep search input=5"),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any { return shapeValue(f.MidTaskCompactions) }, int64(0)},
		{"handbacks after edits are verified only by a check after the last edit", new(shapeScript).
			typed(0).call(1, edit("a.go")).call(2, "Bash command command=test").reply(3).
			typed(60).call(61, "Bash command command=test").call(62, edit("b.go")).reply(63),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.VerifiedHandbacks), shapeValue(f.EditHandbacks), e["unverified_handbacks"]}
			}, []any{int64(1), int64(2), []string{"m11"}}},
		{"ending on a failed call, with nothing typed after it", new(shapeScript).typed(0).call(1, "Bash command error=nonzero_exit").reply(2),
			func(f sessionShapeFacts, e sessionShapeEvidence) any {
				return []any{shapeValue(f.EndedOnFailure), e["ended_on_failure"]}
			}, []any{int64(1), []string{"m1"}}},
		{"a typed message after the failure is no ending on it", new(shapeScript).typed(0).call(1, "Bash command error=nonzero_exit").typed(60),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any { return shapeValue(f.EndedOnFailure) }, int64(0)},
		{"turn cycles skip gaps over 1800 s; the first response is the first turn's", new(shapeScript).typed(0).reply(10).typed(100).call(130, "Read read").typed(2000).reply(3801).typed(4000).reply(4020),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{shapeValue(f.TurnCycles), shapeValue(f.TurnCycleP50), shapeValue(f.TurnCycleP90), shapeValue(f.FirstResponse)}
			}, []any{int64(3), 20.0, 30.0, 10.0}},
		{"autonomy and runs count calls between typed messages", new(shapeScript).pasted(0).call(1, "Read read input=1").call(2, "Read read input=2").typed(10).call(11, "Read read input=3"),
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{shapeValue(f.TypedHumanMessages), shapeValue(f.ToolCalls), shapeValue(f.MaxUnpromptedToolCalls), shapeValue(f.LongUnpromptedRuns)}
			}, []any{int64(1), int64(3), int64(2), int64(0)}},
		{"tool-call messages without ledger rows leave tool signals empty", &shapeScript{in: sessionShapeInput{toolMessages: 2, messages: []shapeMessage{{id: "a", role: "assistant", kind: "tool_call"}}}},
			func(f sessionShapeFacts, _ sessionShapeEvidence) any {
				return []any{f.HasToolData, shapeValue(f.ToolCalls), shapeValue(f.FailedCalls), shapeValue(f.ThrashRetries), f.HasTimestamps, shapeValue(f.WallSeconds)}
			}, []any{false, nil, nil, nil, false, nil}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.check(c.script.derive()); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
	// 150 calls without typed input are one long run.
	long := new(shapeScript).typed(0)
	for i := range 151 {
		long.call(i+1, fmt.Sprintf("Read read input=%d", i))
	}
	if facts, evidence := long.derive(); shapeValue(facts.LongUnpromptedRuns) != int64(1) || shapeValue(facts.MaxUnpromptedToolCalls) != int64(151) || len(evidence["long_unprompted_runs"]) != 1 {
		t.Fatal(shapeValue(facts.LongUnpromptedRuns), shapeValue(facts.MaxUnpromptedToolCalls), evidence["long_unprompted_runs"])
	}
}

func TestSessionShapeContextWindow(t *testing.T) {
	session := func(model string, window int64, inputs ...int64) sessionShapeFacts {
		in := new(shapeScript).typed(0).reply(10).in
		in.model, in.window = model, window
		for _, input := range inputs {
			in.requests = append(in.requests, shapeRequest{at: shapeStart + 10_000, input: input, model: model})
		}
		facts, _ := deriveSessionShape(in)
		return facts
	}
	for _, c := range []struct {
		name   string
		facts  sessionShapeFacts
		window any
		ratio  any
	}{
		{"an unknown window is empty, not zero", session("gemini-2.5-pro", 0, 50_000), nil, nil},
		{"a reported window", session("gpt-5-codex", 272_000, 136_000), int64(272_000), 0.5},
		{"a Claude model's standard window", session("claude-sonnet-4-5", 0, 50_000, 190_000), int64(200_000), 0.95},
		{"a Claude model past 200K has the 1M window", session("claude-opus-4-6", 0, 250_000), int64(1_000_000), 0.25},
		{"no requests, no peak", session("claude-opus-4-6", 0), nil, nil},
	} {
		if shapeValue(c.facts.ContextWindow) != c.window || shapeValue(c.facts.ContextPeakRatio) != c.ratio || c.facts.HasContextWindow != (c.ratio != nil) {
			t.Errorf("%s: window %v ratio %v", c.name, shapeValue(c.facts.ContextWindow), shapeValue(c.facts.ContextPeakRatio))
		}
	}
	if pressure := session("claude-sonnet-4-5", 0, 190_000); shapeValue(pressure.ContextPressure) != int64(1) {
		t.Fatal("95% of the window is not context pressure")
	}
	if calm := session("gpt-5-codex", 272_000, 136_000); shapeValue(calm.ContextPressure) != int64(0) {
		t.Fatal("half the window is context pressure")
	}
}

func TestSessionShapeBands(t *testing.T) {
	row := sessionShapeRow(sessionShapeFacts{Messages: 16, TypedHumanMessages: shapeCount(6), ToolCalls: shapeCount(21),
		ActiveSeconds: shapeSeconds(299), WallSeconds: shapeSeconds(4000), HasToolData: true, HasAuthorship: true, HasTimestamps: true})
	want := map[string]any{"length_band": "16-30", "tool_band": "21-50", "active_band": "<5m", "autonomy_band": "2-5", "archetype": "standard",
		"autonomy": 3.5, "autonomy_prompts": int64(6), "idle_seconds": 3701.0}
	for field, expected := range want {
		if row[field] != expected {
			t.Errorf("%s = %v, want %v", field, row[field], expected)
		}
	}
	automated := sessionShapeRow(sessionShapeFacts{Messages: 3, TypedHumanMessages: shapeCount(0), ToolCalls: shapeCount(4), HasToolData: true, HasAuthorship: true})
	if automated["archetype"] != "automated" || automated["autonomy"] != 4.0 || automated["autonomy_prompts"] != int64(1) || automated["active_band"] != nil {
		t.Fatal(automated)
	}
	for _, edge := range []struct {
		got, want string
	}{{lengthBand(5), "1-5"}, {lengthBand(121), "121+"}, {activeBand(300), "5-15m"}, {activeBand(7200), "2h+"}, {toolBand(0), "0"}, {toolBand(101), "101+"},
		{autonomyBand(0.49), "<0.5"}, {autonomyBand(10), "10+"}, {sessionArchetype(5), "quick"}, {sessionArchetype(51), "marathon"}} {
		if edge.got != edge.want {
			t.Errorf("band %q, want %q", edge.got, edge.want)
		}
	}
}

// seedShapeConversation adds a conversation with messages "role kind second
// [text]" from 2026-10-20T10:00Z, returning their IDs.
func seedShapeConversation(t *testing.T, c *Catalog, id, workspace, provider, model string, messages ...string) []string {
	t.Helper()
	execTx(t, c.DB, fmt.Sprintf(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,started_at) VALUES('%s','%s','%s','local','%s','%s','2026-10-20T10:00:00.000Z')`,
		id, workspace, provider, id, model))
	ids := []string{}
	tx := seedTx(t, c.DB)
	for i, spec := range messages {
		parts := strings.SplitN(spec, " ", 4)
		var seconds int
		fmt.Sscan(parts[2], &seconds)
		text := ""
		if len(parts) > 3 {
			text = parts[3]
		}
		messageID := fmt.Sprintf("%s-%d", id, i)
		if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,source_order,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			messageID, id, fmt.Sprint(i), parts[0], parts[1], text, hashBytes([]byte(text)), i, formatTime(time.UnixMilli(shapeStart).Add(time.Duration(seconds)*time.Second))); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, messageID)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// authored classifies a user message as one span of category over its text.
func authored(t *testing.T, c *Catalog, message, conversation, workspace, category string, words int) {
	t.Helper()
	execTx(t, c.DB, fmt.Sprintf(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,words,%[5]s_words,spans_json)
 VALUES('%[1]s','%[2]s','%[3]s','2026-10-20T10:00:00.000Z','2026-10-20',100,%[4]d,%[4]d,'[]')`, message, conversation, workspace, words, category),
		fmt.Sprintf(`INSERT INTO message_authorship_spans(message_id,position,category,rule,start_byte,end_byte,chars,words)
 SELECT id,0,'%s','test',0,length(CAST(text AS BLOB)),length(text),%d FROM messages WHERE id='%s'`, category, words, message))
}

func shapeWorkspaces(t *testing.T, c *Catalog, workspaces ...string) {
	t.Helper()
	execTx(t, c.DB, `INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-p','pharos','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
	for _, spec := range workspaces {
		parts := strings.Fields(spec) // id source location
		execTx(t, c.DB, fmt.Sprintf(`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,location,activity_at,indexed_at)
 VALUES('%[1]s','%[2]s','local','%[1]s','repo-p','Title %[1]s','%[3]s','2026-10-20T00:00:00.000Z','2026-10-20T00:00:00.000Z')`, parts[0], parts[1], parts[2]))
	}
}

func shapeRows(t *testing.T, c *Catalog) map[string]map[string]any {
	t.Helper()
	rows, err := c.sessionShapeQueryRows(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		if byID[firstString(row["id"])] != nil {
			t.Fatalf("duplicate row %v", row["id"])
		}
		byID[firstString(row["id"])] = row
	}
	return byID
}

// The catalog inputs: authorship spans decide typed text and corrections,
// tool-call text decides identical input, and embedded sub-agents' messages
// and calls are their own.
func TestSessionShapeCatalogInputs(t *testing.T) {
	c, config := testCatalog(t)
	shapeWorkspaces(t, c, "ws-1 claude /code/one", "ws-2 codex /code/two")
	ids := seedShapeConversation(t, c, "cv-1", "ws-1", "claude", "claude-sonnet-4-5",
		"user message 0 you misunderstood, fix the parser", "assistant message 5 ok",
		"assistant tool_call 6 {\"tool\":\"Read\",\"input\":{\"path\":\"a\"}}", "tool tool_result 7 {}",
		"assistant tool_call 8 {\"tool\":\"Read\",\"input\":{\"path\":\"a\"}}", "tool tool_result 9 {}",
		"assistant tool_call 10 {\"tool\":\"Read\",\"input\":{\"path\":\"a\"}}", "tool tool_result 11 {}",
		"user message 60 i meant the lexer", "assistant message 61 done",
		"assistant tool_call 62 {\"tool\":\"Bash\",\"input\":{\"command\":\"ls\"}}", "tool tool_result 63 {}",
		"metadata metadata 64 {\"type\":\"system\",\"subtype\":\"compact_boundary\"}")
	// The first message's words were pasted, so its cue is no correction.
	authored(t, c, ids[0], "cv-1", "ws-1", "pasted", 6)
	authored(t, c, ids[8], "cv-1", "ws-1", "typed", 4)
	tx := seedTx(t, c.DB)
	for i, call := range [][]any{{2, "Read", "read", nil}, {4, "Read", "read", nil}, {6, "Read", "read", nil}, {10, "Bash", "command", "list"}} {
		if _, err := tx.Exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_message_id,sequence,provider,kind,tool_name,tool_category,command_category,status)
 VALUES(?,'ws-1','cv-1',?,?,'claude','tool_call',?,?,?,'ok')`, fmt.Sprint("call-", i), ids[call[0].(int)], i, call[1], call[2], call[3]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The Bash call belongs to an embedded sub-agent.
	execTx(t, c.DB, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('emb','ws-1','cv-1','agent-1','subagent','claude')`,
		fmt.Sprintf(`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('emb','%s',10),('emb','%s',11)`, ids[10], ids[11]),
		`INSERT INTO model_requests(id,conversation_id,sequence,requested_at,input_tokens) VALUES('r1','cv-1',0,'2026-10-20T10:00:05.000Z',150000)`)
	facts, evidence, err := c.sessionShapeOf(t.Context(), "cv-1")
	if err != nil {
		t.Fatal(err)
	}
	got := []any{facts.Messages, shapeValue(facts.TypedHumanMessages), shapeValue(facts.Corrections), shapeValue(facts.ToolCalls), shapeValue(facts.IdenticalRetries),
		facts.Compactions, shapeValue(facts.ContextPeakRatio), evidence["corrections"], evidence["identical_retries"]}
	want := []any{int64(10), int64(1), int64(1), int64(3), int64(2), int64(1), 0.75, []string{ids[8]}, []string{ids[4], ids[6]}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	// The reader's strip names each signal by its field and lists the
	// messages to step through; another workspace's reader gets nothing.
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/ws-1/conversations/cv-1/signals")
	signals := map[string]any{}
	for _, item := range body["signals"].([]any) {
		signal := item.(map[string]any)
		signals[signal["signal"].(string)] = []any{signal["count"], signal["message_ids"]}
	}
	if want := map[string]any{"corrections": []any{1.0, []any{ids[8]}}, "identical_retries": []any{2.0, []any{ids[4], ids[6]}}}; status != http.StatusOK || !reflect.DeepEqual(signals, want) ||
		body["coverage"].(map[string]any)["has_authorship"] != true {
		t.Fatalf("status %d signals %#v body %v", status, signals, body)
	}
	if other, err := c.SessionSignals(t.Context(), "ws-2", "cv-1"); other != nil || err != nil {
		t.Fatal("another workspace read cv-1's signals", other, err)
	}
	row := shapeRows(t, c)["cv-1"]
	if row["subagents"] != int64(1) || row["repository_name"] != "pharos" || row["model_family"] == nil || row["day"] != "2026-10-20" || row["agent_kind"] != "root" {
		t.Fatal(row)
	}

	// A legacy Codex checkpoint's request row holds a delta of cumulative
	// usage, and a native one can hold the cumulative total; its
	// last_token_usage is the context. A prompt Human Words has not
	// classified yet leaves authorship unknown.
	ids = seedShapeConversation(t, c, "cv-2", "ws-2", "codex", "gpt-5",
		"user message 0 fix the parser", "assistant message 5 ok",
		`metadata metadata 6 {"payload":{"info":{"last_token_usage":{"cached_input_tokens":0,"input_tokens":100000,"total_tokens":100100},"model_context_window":272000,"total_token_usage":{"input_tokens":500000,"total_tokens":500500}},"type":"token_count"},"type":"event_msg"}`,
		"user message 60 now the lexer", "assistant message 65 done")
	authored(t, c, ids[0], "cv-2", "ws-2", "typed", 3)
	execTx(t, c.DB, `INSERT INTO model_requests(id,conversation_id,sequence,requested_at,input_tokens) VALUES('r2','cv-2',0,'2026-10-20T10:00:06.000Z',500000)`,
		`INSERT INTO model_requests(id,conversation_id,sequence,requested_at,input_tokens,native_id) VALUES('r3','cv-2',1,'2026-10-20T10:01:06.000Z',9000000,'turn-1')`)
	facts, _, err = c.sessionShapeOf(t.Context(), "cv-2")
	if err != nil {
		t.Fatal(err)
	}
	if got := []any{shapeValue(facts.ContextPeakTokens), shapeValue(facts.ContextPressure), facts.HasAuthorship, shapeValue(facts.TypedHumanMessages)}; !reflect.DeepEqual(got, []any{int64(100000), int64(0), false, nil}) {
		t.Fatalf("got %#v", got)
	}
	authored(t, c, ids[3], "cv-2", "ws-2", "typed", 3)
	if facts, _, err = c.sessionShapeOf(t.Context(), "cv-2"); err != nil || !facts.HasAuthorship || shapeValue(facts.TypedHumanMessages) != int64(2) {
		t.Fatal("classified prompts were not counted", err)
	}
}

// Mirrors and aliases produce one row; delegated runs carry their root.
func TestSessionShapeMirrorsAndRoots(t *testing.T) {
	c, _ := parallelismTestCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,location,activity_at,indexed_at)
 VALUES('ws-alias','claude','local','ws-alias','repo-a','Alias','/code/alias','2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,started_at) VALUES('cv-alias','ws-alias','claude','other','cv-a2','model-x','2026-11-01T00:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('cv-alias-0','cv-alias','0','user','message','','h','2026-11-01T07:00:00.000Z')`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('cv-a2','cv-alias','native-alias',1,'{}')`)
	rows := shapeRows(t, c)
	for _, gone := range []string{"cv-mirror", "cv-alias"} {
		if rows[gone] != nil {
			t.Errorf("%s has its own row", gone)
		}
	}
	for _, kept := range []string{"cv-a1", "cv-a2", "cv-b1", "cv-old"} {
		if rows[kept] == nil || rows[kept]["root_conversation_id"] != kept || rows[kept]["agent_kind"] != "root" {
			t.Errorf("%s: %v", kept, rows[kept])
		}
	}
	for child, root := range map[string]string{"cv-sub": "cv-a1", "cv-launch": "cv-b1"} {
		if rows[child]["root_conversation_id"] != root || rows[child]["agent_kind"] != "subagent" {
			t.Errorf("%s: %v", child, rows[child])
		}
	}
	// cv-b1's 08:30 response is its embedded sub-agent's.
	if rows["cv-b1"]["messages"] != int64(2) || rows["cv-b1"]["subagents"] != int64(2) {
		t.Fatal(rows["cv-b1"])
	}
}

func TestSessionShapeIncremental(t *testing.T) {
	c, config := parallelismTestCatalog(t)
	all := countRows(t, c, "SELECT COUNT(*) FROM conversations")
	// Pages derive dirty conversations without storing them; maintenance does.
	if len(shapeRows(t, c)) == 0 || countRows(t, c, "SELECT COUNT(*) FROM session_shape_dirty") != all {
		t.Fatal("a page stored session shape")
	}
	if _, err := c.refreshSessionShape(t.Context(), 100, false); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM session_shape_dirty") != 0 || countRows(t, c, "SELECT COUNT(*) FROM session_shape_facts") != all {
		t.Fatal("refresh left conversations unstored")
	}
	// A new message marks only its conversation.
	execTx(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('cv-n1-x','cv-n1','x','assistant','message','','h','2026-11-03T16:50:00.000Z')`)
	if countRows(t, c, "SELECT COUNT(*) FROM session_shape_dirty") != 1 || countRows(t, c, "SELECT COUNT(*) FROM session_shape_dirty WHERE conversation_id='cv-n1'") != 1 {
		t.Fatal("message insert did not mark only its conversation")
	}
	if shapeRows(t, c)["cv-n1"]["messages"] != int64(5) {
		t.Fatal("a page did not see the new message")
	}
	// Tool calls, authorship, requests and ownership mark theirs too.
	for _, statement := range []string{
		`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_message_id,sequence,provider,kind,tool_name,tool_category,status) VALUES('t','ws-a2','cv-a2','cv-a2-1',0,'codex','tool_call','x','other','ok')`,
		`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) VALUES('cv-a3-0','cv-a3','ws-a3','2026-10-30T15:20:00.000Z','2026-10-30',1,'[]')`,
		`INSERT INTO model_requests(id,conversation_id,sequence,input_tokens) VALUES('r','cv-b1',0,10)`,
		`DELETE FROM agent_session_messages WHERE message_id='cv-b1-2'`,
		// Spans are published after their authorship rows, separately.
		`INSERT INTO message_authorship_spans(message_id,position,category,rule,start_byte,end_byte,chars,words) VALUES('cv-launch-0',0,'typed','test',0,0,0,0)`,
	} {
		execTx(t, c.DB, statement)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM session_shape_dirty WHERE conversation_id IN ('cv-n1','cv-a2','cv-a3','cv-b1','cv-launch')") != 5 {
		t.Fatal("inputs did not mark their conversations")
	}
	if _, err := c.refreshSessionShape(t.Context(), 100, true); err != nil {
		t.Fatal(err)
	}
	// Stored facts and evidence equal a fresh derivation.
	facts, err := c.sessionShapeAllFacts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for id, stored := range facts {
		fresh, freshEvidence, err := deriveSessionShapeOf(t.Context(), c.DB, id)
		_, storedEvidence, _ := c.sessionShapeOf(t.Context(), id)
		if err != nil || !reflect.DeepEqual(*fresh, stored) || len(freshEvidence) != len(storedEvidence) {
			t.Fatalf("%s: stored %+v, derived %+v (%v)", id, stored, fresh, err)
		}
	}
	// Damaged facts are derived again rather than failing the page.
	execTx(t, c.DB, `UPDATE session_shape_facts SET facts='{' WHERE conversation_id='cv-a1'`)
	if shapeRows(t, c)["cv-a1"]["messages"] != int64(12) {
		t.Fatal("damaged facts were not derived again")
	}
	// A deleted conversation's facts go at the next refresh.
	execTx(t, c.DB, `DELETE FROM conversations WHERE id='cv-old'`)
	if _, err := c.refreshSessionShape(t.Context(), 100, true); err != nil || countRows(t, c, "SELECT COUNT(*) FROM session_shape_facts WHERE conversation_id='cv-old'") != 0 {
		t.Fatal("deleted conversation kept its facts", err)
	}
	// A new derivation marks every conversation.
	execTx(t, c.DB, `UPDATE meta SET value='0' WHERE key='session_shape_version'`)
	if err := c.ensureSessionShape(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM session_shape_facts") != 0 || countRows(t, c, "SELECT COUNT(*) FROM session_shape_dirty") != countRows(t, c, "SELECT COUNT(*) FROM conversations") {
		t.Fatal("version change did not mark every conversation")
	}
	// The query table serves the rows, windowed by started_at.
	server := testServer(t, config, c)
	w := queryExplorationCall(server, config.APIToken, http.MethodPost, "/api/query/session_shape/metrics",
		`{"version":2,"profile":"qt-sqlite-v1","limit":50,"where":[{"field":"started_at","op":">=","value":"2026-11-01T00:00:00-06:00"}],"metrics":[{"id":"n","expression":"SUM([messages])","groupBy":["length_band"]}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"1-5"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = queryExplorationCall(server, config.APIToken, http.MethodPost, "/api/query/session_shape/query-v3",
		`{"version":3,"profile":"qt-sqlite-v1","rows":true,"result":{"select":["id","archetype","active_seconds","has_authorship"],"limit":50}}`)
	if w.Code != 200 || strings.Count(w.Body.String(), `"archetype"`) < 5 {
		t.Fatal(w.Code, w.Body.String())
	}
}
