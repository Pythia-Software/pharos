package archive

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestReingestReplacesSessionMessageLinks(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{
		SourceID: "tl1-task", SourceKind: "tl1", Account: "local", Title: "Task",
		Conversations: []ConversationRecord{{
			NativeID: "tl1-transcript", Provider: "claude", Account: "local",
			Messages: []MessageRecord{{NativeID: "event-1", Role: "user", Kind: "message", Text: "First event", Selected: true}},
		}},
	}
	for run := 0; run < 2; run++ {
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatalf("ingest run %d: %v", run+1, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var links int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM agent_session_messages").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("reingest left %d links; want 1", links)
	}
}

func TestReingestFTSUsesRowidsAndSkipsUnchangedText(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{
		SourceID: "session", SourceKind: "conductor", Account: "local", Title: "Session",
		Conversations: []ConversationRecord{{NativeID: "thread", Provider: "codex", Account: "local",
			Messages: []MessageRecord{{NativeID: "one", Role: "user", Kind: "message", Text: "alpha", Selected: true},
				{NativeID: "output", Role: "tool", Kind: "tool_result", Text: "tool output", Selected: true}}}},
	}
	// Tool output is never word-indexed, whether ingest indexes the whole
	// conversation or one changed message.
	unindexedOutput := func() {
		t.Helper()
		var mapped, indexed int
		if err := catalog.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM message_fts_rows r JOIN messages m ON m.id=r.message_id WHERE m.kind='tool_result'),
			(SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'output')`).Scan(&mapped, &indexed); err != nil || mapped != 0 || indexed != 0 {
			t.Fatalf("tool output word-indexed: %d mapped, %d indexed, %v", mapped, indexed, err)
		}
	}
	ingest := func() {
		t.Helper()
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	ftsRow := func() (int64, string) {
		t.Helper()
		var rowid int64
		var text string
		if err := catalog.DB.QueryRow(`SELECT f.rowid,f.text FROM messages_fts f
			JOIN message_fts_rows r ON r.fts_rowid=f.rowid`).Scan(&rowid, &text); err != nil {
			t.Fatal(err)
		}
		return rowid, text
	}
	ingest()
	unindexedOutput()
	first, value := ftsRow()
	if value != "alpha" {
		t.Fatalf("FTS text = %q", value)
	}
	ingest()
	second, _ := ftsRow()
	if second != first {
		t.Fatalf("unchanged text rebuilt FTS row %d as %d", first, second)
	}
	record.Conversations[0].Messages[0].Text = "beta"
	record.Conversations[0].Messages[1].Text = "changed tool output"
	ingest()
	unindexedOutput()
	_, value = ftsRow()
	if value != "beta" {
		t.Fatalf("updated FTS text = %q", value)
	}
	record.Conversations[0].Messages = nil
	ingest()
	var count int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM messages_fts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pruned conversation has %d FTS rows", count)
	}
}

func TestFTSRowidMigrationIndexesExistingRows(t *testing.T) {
	catalog, _ := testCatalog(t)
	if _, err := catalog.DB.Exec("INSERT INTO messages_fts(message_id,text) VALUES('legacy','old text')"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("DELETE FROM meta WHERE key='message_fts_rows_version'"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := catalog.DB.QueryRow(`SELECT f.text FROM messages_fts f JOIN message_fts_rows r
		ON r.fts_rowid=f.rowid WHERE r.message_id='legacy'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "old text" {
		t.Fatalf("migrated FTS text = %q", text)
	}
}

func TestIngestSkipsUnchangedRecordsWhenSourceFingerprintChanges(t *testing.T) {
	catalog, _ := testCatalog(t)
	path := filepath.Join(t.TempDir(), "export.json")
	adapter, err := MakeAdapter(SourceConfig{Name: "fixture", Kind: "canonical", Path: path, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	write := func(title, padding string) {
		t.Helper()
		payload := `{"workspaces":[{"id":"work","title":"` + title + `","conversations":[{"id":"thread","provider":"codex","messages":[{"id":"one","role":"user","text":"hello"}]}]}]}` + padding
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("First", "")
	first := catalog.Ingest(adapter, nil)
	if first.Error != nil || first.Workspaces != 1 {
		t.Fatalf("initial ingest: %+v", first)
	}
	write("First", " ")
	second := catalog.Ingest(adapter, nil)
	if second.Error != nil || second.Workspaces != 0 || second.SkippedCurrent != 1 {
		t.Fatalf("unchanged record was reingested: %+v", second)
	}
	write("Updated", "  ")
	third := catalog.Ingest(adapter, nil)
	if third.Error != nil || third.Workspaces != 1 {
		t.Fatalf("changed record was skipped: %+v", third)
	}
	var title string
	if err := catalog.DB.QueryRow("SELECT title FROM workspaces WHERE source_kind='canonical'").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Updated" {
		t.Fatalf("catalog title = %q", title)
	}
}

func TestLegacyConductorResumeChecksActivityTime(t *testing.T) {
	catalog, _ := testCatalog(t)
	adapter, err := MakeAdapter(SourceConfig{Name: "conductor", Kind: "conductor", Path: t.TempDir(), Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	record := WorkspaceRecord{SourceID: "session", SourceKind: "conductor", Account: "local", Title: "Session",
		ActivityAt: "2026-09-20T00:00:00Z", Conversations: []ConversationRecord{{
			NativeID: "thread", Provider: "codex", Account: "local", Coverage: conductorExtractor,
			Messages: []MessageRecord{{NativeID: "one", Role: "user", Kind: "message", Text: "hello", Selected: true}},
		}},
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !catalog.recordUsesCurrentExtractor(adapter, record, currentHost().ID) {
		t.Fatal("recently indexed record was not resumable")
	}
	record.ActivityAt = "2099-01-01T00:00:00Z"
	if catalog.recordUsesCurrentExtractor(adapter, record, currentHost().ID) {
		t.Fatal("new source activity was skipped")
	}
}

func TestCodexJSONLReadsLargeEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-large.jsonl")
	message := strings.Repeat("x", 17*1024*1024)
	data := `{"type":"session_meta","payload":{"id":"large-session"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"` + message + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: path, Account: "local"}}, provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(record.Conversations) != 1 || len(record.Conversations[0].Messages) != 1 || len(record.Conversations[0].Messages[0].Text) != len(message) {
		t.Fatalf("large JSONL event was not retained: ok=%v conversations=%d", ok, len(record.Conversations))
	}
}

func TestCodexModelFollowsTurnContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-models.jsonl")
	data := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"model-session"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-5.6-terra"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"}]}}`,
		`{"type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: path, Account: "local"}}, provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	conversation := record.Conversations[0]
	if conversation.Model != "gpt-5.6-sol" || len(conversation.Messages) != 2 || conversation.Messages[0].Model != "gpt-5.6-terra" || conversation.Messages[1].Model != "gpt-5.6-sol" {
		t.Fatalf("model transitions lost: %#v", conversation)
	}
	catalog, _ := testCatalog(t)
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := catalog.DB.QueryRow("SELECT model FROM messages WHERE text='first'").Scan(&model); err != nil || model != "gpt-5.6-terra" {
		t.Fatalf("stored first model = %q, err=%v", model, err)
	}
}

func TestReasoningNormalization(t *testing.T) {
	for _, block := range []string{`{"type":"redacted_thinking","data":"secret"}`, `{"type":"thinking","thinking":" "}`} {
		t.Run(block, func(t *testing.T) {
			body := `{"type":"assistant","message":{"role":"assistant","content":[` + block + `,{"type":"text","text":"Reply"}],"usage":{"input_tokens":10,"output_tokens":4}}}`
			items := normalizeReasoningMessages([]MessageRecord{{NativeID: "event", Role: "assistant", Kind: "metadata", Text: body}})
			if len(items) != 2 || items[0].NativeID != "event" || items[0].Kind != "metadata" || items[1].Text != "Reply" {
				t.Fatalf("unavailable first block was duplicated: %+v", items)
			}
			if again := normalizeReasoningMessages(items); jsonText(again) != jsonText(items) || conversationTokenCounts(again)["total_tokens"] != 14 {
				t.Fatalf("unavailable reasoning changed on replay: %+v", again)
			}
			// A legacy reply with the base ID still needs its missing first block.
			legacy := normalizeReasoningMessages([]MessageRecord{{NativeID: "event", Role: "assistant", Kind: "message", Text: "Reply", RawText: body}})
			if len(legacy) != 2 || legacy[0].Kind != "metadata" || legacy[0].NativeID != "event:block:0" || legacy[1].Text != "Reply" {
				t.Fatalf("missing unavailable block was not recovered: %+v", legacy)
			}
		})
	}
	thought := "I'm puzzled about why the mobile output seems missing. Actually I think I understand now: desktop failed before mobile ran."
	envelope := jsonText(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": thought}, map[string]any{"type": "text", "text": "Checking the layout."},
		map[string]any{"type": "tool_use", "id": "read", "name": "Read", "input": map[string]any{"path": "layout.ts"}},
	}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4}}})
	base := MessageRecord{NativeID: "saved", Role: "assistant", Kind: "metadata", Text: envelope, CreatedAt: "2026-10-18T17:10:35Z", EvidenceLocator: "fixture:328", ParentNativeID: "child", Selected: true}
	got := normalizeReasoningMessages([]MessageRecord{base})
	if len(got) != 3 || got[0].Kind != "reasoning" || got[0].Text != thought || got[0].NativeID != base.NativeID || got[0].RawText != envelope || got[0].ReasoningFormat != "text" || got[0].ParentNativeID != "child" {
		t.Fatalf("lost reasoning provenance: %+v", got)
	}
	if got[1].Kind != "message" || got[2].Kind != "tool_call" || got[2].CallID != "read" || got[1].RawText != "" || got[2].RawText != "" || conversationTokenCounts(got)["total_tokens"] != 14 {
		t.Fatalf("lost order or duplicated accounting: %+v", got)
	}
	if again := normalizeReasoningMessages(got); jsonText(again) != jsonText(got) {
		t.Fatalf("normalization is not idempotent: %+v", again)
	}
	replyFirst := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Looking"},{"type":"thinking","thinking":"Now I understand"}]}}`
	items := normalizeReasoningMessages([]MessageRecord{{NativeID: "reply-first", Role: "assistant", Kind: "metadata", Text: replyFirst}})
	if len(items) != 2 || items[0].Kind != "message" || items[1].Kind != "reasoning" {
		t.Fatalf("a reply's raw envelope duplicated thinking: %+v", items)
	}
	if again := normalizeReasoningMessages(items); jsonText(again) != jsonText(items) {
		t.Fatalf("reply-first normalization changed on replay: %+v", again)
	}
	// Historical mixed envelopes often survive only as accounting evidence on
	// the already extracted reply. Recover the omitted block in source order.
	oldReply := MessageRecord{NativeID: "old:block:1", Role: "assistant", Kind: "message", Text: "Checking the layout.", RawText: envelope, EvidenceLocator: "fixture:328", Selected: true}
	recovered := normalizeReasoningMessages([]MessageRecord{oldReply})
	if len(recovered) != 2 || recovered[0].Kind != "reasoning" || recovered[0].Text != thought || recovered[0].NativeID != "old:block:0" || recovered[0].RawText != "" || recovered[1].NativeID != oldReply.NativeID {
		t.Fatalf("raw-only thinking not recovered: %+v", recovered)
	}
	if again := normalizeReasoningMessages(recovered); jsonText(again) != jsonText(recovered) || conversationTokenCounts(again)["total_tokens"] != 14 {
		t.Fatalf("raw recovery duplicated thinking or accounting: %+v", again)
	}
	oldCodex := MessageRecord{NativeID: "old-codex", Role: "system", Kind: "metadata", Text: "Accounting evidence", RawText: `{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Now I see"}]}}`}
	if items := normalizeReasoningMessages([]MessageRecord{oldCodex}); len(items) != 1 || items[0].Kind != "reasoning" || items[0].ReasoningFormat != "summary" || items[0].Text != "Now I see" || items[0].NativeID != oldCodex.NativeID {
		t.Fatalf("Codex raw reasoning envelope was not recovered: %+v", items)
	}
	for _, tc := range []struct{ name, role, kind, body, want, format string }{
		{"summary", "assistant", "metadata", `{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Now I see the issue"}],"encrypted_content":"secret"}}`, "reasoning", "summary"},
		{"full text", "assistant", "metadata", `{"type":"reasoning","content":[{"type":"reasoning_text","text":"Now I understand"}],"summary":[{"type":"summary_text","text":"shorter"}]}`, "reasoning", "text"},
		{"exec summary", "assistant", "metadata", `{"type":"reasoning","text":"I understand now"}`, "reasoning", "summary"},
		{"analysis channel", "assistant", "metadata", `{"type":"message","role":"assistant","channel":"analysis","content":[{"type":"output_text","text":"Now I understand"}]}`, "reasoning", "text"},
		{"encrypted", "assistant", "metadata", `{"type":"reasoning","encrypted_content":"not searchable thought"}`, "metadata", ""},
		{"redacted", "assistant", "metadata", `{"type":"assistant","message":{"content":[{"type":"redacted_thinking","data":"not searchable thought"}]}}`, "metadata", ""},
		{"empty", "assistant", "metadata", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":" "}]}}`, "metadata", ""},
		{"user quotation", "user", "message", envelope, "message", ""},
		{"assistant quotation", "assistant", "message", envelope, "message", ""},
		{"tool quotation", "tool", "tool_result", envelope, "tool_result", ""},
		{"assistant tool input", "assistant", "tool_call", envelope, "tool_call", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := MessageRecord{NativeID: "event", Role: tc.role, Kind: tc.kind, Text: tc.body}
			items := normalizeReasoningMessage(input)
			if len(items) != 1 || items[0].Kind != tc.want || items[0].ReasoningFormat != tc.format {
				t.Fatalf("normalization: %+v", items)
			}
			if tc.want != "reasoning" && tc.role == "assistant" && tc.kind == "metadata" && strings.Contains(items[0].Text, "not searchable thought") {
				t.Fatal("unavailable payload became searchable")
			}
		})
	}
}

func TestNativeReasoningRetention(t *testing.T) {
	claude := []map[string]any{{"type": "assistant", "uuid": "event", "message": map[string]any{"role": "assistant", "model": "opus", "content": []any{
		map[string]any{"type": "thinking", "thinking": "Now I understand"},
		map[string]any{"type": "text", "text": "A reply"},
		map[string]any{"type": "thinking", "thinking": "Now I see"},
		map[string]any{"type": "text", "text": "Another reply"},
	}}}}
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}, provider: "claude"}
	record, ok, err := adapter.claude("fixture.jsonl", claude)
	if err != nil || !ok {
		t.Fatalf("Claude: %v %v", ok, err)
	}
	messages := record.Conversations[0].Messages
	if len(messages) != 4 || messages[0].Kind != "reasoning" || messages[1].NativeID != "event" || messages[2].Kind != "reasoning" || messages[3].Text != "Another reply" {
		t.Fatalf("Claude order/identity: %+v", messages)
	}
	conductor := conductorEvents("event", "assistant", jsonText(claude[0]), "", "fixture:1", map[string]bool{})
	if len(conductor) != 4 || conductor[0].Kind != "reasoning" || conductor[2].Kind != "reasoning" {
		t.Fatalf("Conductor: %+v", conductor)
	}
	thinkingOnly := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"First"},{"type":"thinking","thinking":"Second"}]}}`
	backfilled := normalizeReasoningMessages([]MessageRecord{{NativeID: "thinking-only", Role: "assistant", Kind: "metadata", Text: thinkingOnly}})
	reindexed := conductorEvents("thinking-only", "assistant", thinkingOnly, "", "", map[string]bool{})
	if len(reindexed) != 2 || reindexed[0].NativeID != backfilled[0].NativeID || reindexed[1].NativeID != backfilled[1].NativeID {
		t.Fatalf("Conductor reindex changed recovered identities: %+v vs %+v", reindexed, backfilled)
	}
	adapter.provider = "codex"
	code, ok, err := adapter.codex("rollout.jsonl", []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "reasoning-session"}},
		{"type": "response_item", "payload": map[string]any{"id": "reason", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Now I understand"}}}},
		{"type": "response_item", "payload": map[string]any{"id": "analysis", "type": "message", "role": "assistant", "channel": "analysis", "content": []any{map[string]any{"type": "output_text", "text": "Now I see"}}}},
		{"type": "response_item", "payload": map[string]any{"id": "reply", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Done"}}}},
	})
	if err != nil || !ok {
		t.Fatalf("Codex: %v %v", ok, err)
	}
	messages = code.Conversations[0].Messages
	if len(messages) != 3 || messages[0].Kind != "reasoning" || messages[0].ReasoningFormat != "summary" || messages[1].Kind != "reasoning" || messages[2].Kind != "message" {
		t.Fatalf("Codex: %+v", messages)
	}
	path := filepath.Join(t.TempDir(), "exec.jsonl")
	writeSourceFile(t, path, "{\"type\":\"thread.started\",\"thread_id\":\"exec\"}\n{\"type\":\"item.completed\",\"item\":{\"id\":\"thought\",\"type\":\"reasoning\",\"text\":\"Now I see\"}}\n")
	exec, ok, err := parseCodexExec(path, path)
	if err != nil || !ok || len(exec.Messages) != 1 || exec.Messages[0].Kind != "reasoning" || exec.Messages[0].ReasoningFormat != "summary" {
		t.Fatalf("Codex exec: %+v %v %v", exec.Messages, ok, err)
	}
}

func TestCodexCompactionTrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-compaction.jsonl")
	data := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"compaction-session"}}`,
		`{"type":"compacted","payload":{"message":"replayed"}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"work"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{}"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10}}}}`,
		`{"type":"compacted","payload":{"message":"automatic"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10}}}}`,
		`{"type":"compacted","payload":{"message":"requested"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: path, Account: "local"}}, provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	triggers := []string{}
	for _, message := range record.Conversations[0].Messages {
		if strings.Contains(message.Text, `"type":"compacted"`) {
			var event map[string]any
			if err := json.Unmarshal([]byte(message.Text), &event); err != nil {
				t.Fatal(err)
			}
			triggers = append(triggers, firstString(event["compaction_trigger"]))
		}
	}
	if strings.Join(triggers, ",") != ",auto,manual" {
		t.Fatalf("compaction triggers = %q", triggers)
	}
}

func TestCodexMessagesToExistingAgentDoNotCreateSubagents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-agents.jsonl")
	data := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"agent-session"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn","arguments":"{\"task_name\":\"review\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"spawn","output":"{\"task_name\":\"/root/review\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"send_message","call_id":"message","arguments":"{\"target\":\"review\",\"message\":\"Update\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"message","output":""}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := jsonlAdapter{provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	messages := record.Conversations[0].Messages
	if len(messages) != 4 || messages[0].Kind != "delegation" || messages[1].Kind != "delegation_result" || messages[2].Kind != "tool_call" || messages[3].Kind != "tool_result" {
		t.Fatalf("agent event kinds: %#v", messages)
	}
	if got := len(agentSessionSummaries(messages)); got != 2 {
		t.Fatalf("agent sessions = %d, want root and spawned child", got)
	}
}

func TestWorkflowToolIsDelegation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow-root.jsonl")
	data := strings.Join([]string{
		`{"sessionId":"workflow-root","uuid":"call","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Workflow","id":"workflow-1","input":{"script":"agents run here"}}]}}`,
		`{"sessionId":"workflow-root","uuid":"result","type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"workflow-1","content":"done"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := jsonlAdapter{provider: "claude"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	messages := record.Conversations[0].Messages
	if len(messages) != 2 || messages[0].Kind != "delegation" || messages[1].Kind != "delegation_result" {
		t.Fatalf("workflow event kinds: %#v", messages)
	}
}

func TestCodexParserUpgradeDoesNotSkipUnchangedFile(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	record := WorkspaceRecord{SourceID: "session", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{{Origin: path}}}
	if _, err := catalog.DB.Exec(`INSERT INTO source_record_states(host_id,source_name,source_kind,source_account,source_id,digest,updated_at)
		VALUES(?,'codex','codex','local','session','old-digest',?)`, currentHost().ID, now()); err != nil {
		t.Fatal(err)
	}
	if catalog.recordUsesCurrentExtractor(adapter, record, currentHost().ID) {
		t.Fatal("old Codex record suppressed a parser repair")
	}
	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if catalog.recordUsesCurrentExtractor(adapter, record, currentHost().ID) {
		t.Fatal("modified Codex file was skipped")
	}
}

func TestRepeatedNativeMessageIDDoesNotDuplicateSessionLink(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "claude", SourceKind: "claude", Account: "local", Title: "Claude",
		Conversations: []ConversationRecord{{NativeID: "thread", Provider: "claude", Account: "local",
			Messages: []MessageRecord{
				{NativeID: "same", Role: "assistant", Kind: "message", Text: "draft", Selected: true},
				{NativeID: "same", Role: "assistant", Kind: "message", Text: "final", Selected: true},
			},
		}},
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var links int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM agent_session_messages").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("got %d duplicate session links", links)
	}
	var text string
	if err := catalog.DB.QueryRow("SELECT text FROM messages").Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "final" {
		t.Fatalf("stored message = %q", text)
	}
}

func TestSourceInventoryReportsConversationDocumentCoverage(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	check := func(wantDocs, wantConversations int) {
		t.Helper()
		inventory, err := server.sourceInventory()
		if err != nil {
			t.Fatal(err)
		}
		if inventory["ingested_documents"] != wantDocs || inventory["total_conversations"] != wantConversations {
			t.Fatalf("unexpected coverage: %#v", inventory)
		}
	}
	check(0, 0)
	if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at)
		VALUES('workspace-1','canonical','local','source-1','Test workspace','2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"conversation-1", "conversation-2"} {
		if _, err := catalog.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id)
			VALUES(?,'workspace-1','canonical','local',?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.DB.Exec(`INSERT INTO conversation_documents(conversation_id,vector_json,model,indexed_at)
		VALUES('conversation-1','[]','test','2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	check(1, 2)
}

func TestStableIDsMatchPythonCatalog(t *testing.T) {
	got := stableID("workspace", "conductor", "local", "e9d9806d-fa82-4c6e-97e9-0de3453e32f8")
	want := "workspace_9129eb3d0f4c5356b997a6a284a04745"
	if got != want {
		t.Fatalf("stable ID mismatch: got %s want %s", got, want)
	}
	repository := stableID("repo", "git@github.com:Pythia-Software/explo.git", nil)
	if repository != "repo_aee60273f1995774ae5ab72f826055ee" {
		t.Fatalf("repository ID mismatch: %s", repository)
	}
}

func TestSemanticVectorMatchesPythonImplementation(t *testing.T) {
	vector := semanticEmbed("fix parser bug")
	want := map[int]float64{15: -0.087616400599, 47: -0.486757781107, 48: 0.087616400599, 111: -0.267716779609, 126: 0.486757781107}
	for index, expected := range want {
		difference := vector[index] - expected
		if difference < 0 {
			difference = -difference
		}
		if difference > 1e-11 {
			t.Fatalf("dimension %d got %.12f want %.12f", index, vector[index], expected)
		}
	}
}

func TestCodexRolloutKeepsFirstSessionIdentity(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"child-one", "child-two"} {
		lines := []string{
			`{"type":"session_meta","timestamp":"2026-09-23T00:00:00Z","payload":{"id":"` + id + `","cwd":"/child","git":{"branch":"feature/child","commit_hash":"abc123"}}}`,
			`{"type":"turn_context","timestamp":"2026-09-23T00:00:01Z","payload":{"model":"gpt-test"}}`,
			`{"type":"response_item","timestamp":"2026-09-23T00:00:02Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
			`{"type":"session_meta","timestamp":"2026-09-23T00:00:03Z","payload":{"id":"parent","cwd":"/parent"}}`,
		}
		if err := os.WriteFile(filepath.Join(root, "rollout-"+id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	if err := adapter.Discover(func(record WorkspaceRecord) error {
		seen[record.SourceID] = true
		if record.Location != "/child" || record.Conversations[0].NativeID != record.SourceID ||
			firstString(record.Metadata["branch"]) != "feature/child" || firstString(record.Metadata["head_ref"]) != "abc123" {
			t.Fatalf("rollout identity changed mid-file: %#v", record)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seen["child-one"] || !seen["child-two"] || len(seen) != 2 {
		t.Fatalf("rollouts merged: %#v", seen)
	}
}

func TestCodexForkWithParentSessionMetaUsesFilenameIdentity(t *testing.T) {
	root := t.TempDir()
	parent := "01a09256-2e53-7b13-bd03-02c212671bce"
	child := "01a0925e-e9fc-73f1-a57f-230e71ea742b"
	path := filepath.Join(root, "rollout-2026-09-11T15-28-08-"+parent+"_"+child+".jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-11T21:28:09Z","payload":{"id":"` + parent + `","cwd":"/fork"}}`,
		`{"type":"turn_context","timestamp":"2026-09-11T21:28:10Z","payload":{"model":"gpt-test"}}`,
		`{"type":"response_item","timestamp":"2026-09-11T21:28:11Z","payload":{"type":"message","role":"assistant","content":"fork response"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &jsonlAdapter{provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok || record.SourceID != child || record.Conversations[0].NativeID != child || record.Conversations[0].ParentNativeID != parent || len(record.Conversations[0].Messages) != 1 {
		t.Fatalf("fork identity and parent: %#v, %v, %v", record, ok, err)
	}
	catalog, _ := testCatalog(t)
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := catalog.DB.QueryRow("SELECT kind FROM agent_sessions WHERE native_id='main'").Scan(&kind); err != nil || kind != "root" {
		t.Fatalf("Codex continuation session kind = %q, err=%v", kind, err)
	}
}

func TestCodexSubagentMetadataCreatesChildSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-child.jsonl")
	data := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"child","parent_thread_id":"parent","agent_path":"/root/review/details","agent_nickname":"Ada","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":2}}}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":"reviewed"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := jsonlAdapter{provider: "codex"}
	record, ok, err := adapter.parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	child := record.Conversations[0]
	if child.ParentNativeID != "parent" || child.AgentDepth != 2 || child.AgentPath != "/root/review/details" || child.AgentNickname != "Ada" {
		t.Fatalf("subagent metadata: %#v", child)
	}
	catalog, _ := testCatalog(t)
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var kind string
	var depth int
	if err := catalog.DB.QueryRow("SELECT kind,depth FROM agent_sessions WHERE native_id='main'").Scan(&kind, &depth); err != nil || kind != "subagent" || depth != 2 {
		t.Fatalf("subagent session = %q depth %d, err=%v", kind, depth, err)
	}
	row, err := queryMaps(catalog.DB, "SELECT id,workspace_id,provider,model,started_at,agent_depth FROM conversations")
	if err != nil || len(row) != 1 {
		t.Fatalf("stored child conversation: %#v, %v", row, err)
	}
	if err := catalog.rebuildAgentSessions(row[0]); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT kind,depth FROM agent_sessions WHERE native_id='main'").Scan(&kind, &depth); err != nil || kind != "subagent" || depth != 2 {
		t.Fatalf("rebuilt subagent session = %q depth %d, err=%v", kind, depth, err)
	}
}

func TestClaudeSubagentHasOwnConversation(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	childDir := filepath.Join(root, "root-session", "subagents")
	nestedDir := filepath.Join(childDir, "workflows", "wf-1")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "root-session.jsonl"): `{"sessionId":"root-session","uuid":"root-message","type":"assistant","gitBranch":"feature/root","message":{"model":"claude-test","content":"root answer"}}` + "\n" + `{"sessionId":"root-session","type":"ai-title","aiTitle":"A useful session title"}`,
		filepath.Join(childDir, "agent-1.jsonl"):  `{"sessionId":"root-session","uuid":"child-message","type":"assistant","message":{"model":"claude-test","content":"child answer"}}`,
		filepath.Join(nestedDir, "agent-2.jsonl"): `{"sessionId":"root-session","uuid":"nested-message","type":"assistant","message":{"model":"claude-test","content":"nested answer"}}`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil || result.Workspaces != 1 || result.Conversations != 3 {
		t.Fatalf("subagent ingest: %#v", result)
	}
	var title, branch string
	if err := catalog.DB.QueryRow("SELECT title,branch FROM workspaces WHERE source_kind='claude'").Scan(&title, &branch); err != nil || title != "A useful session title" || branch != "feature/root" {
		t.Fatalf("Claude title or branch missing: %q %q, %v", title, branch, err)
	}
	rows, err := queryMaps(catalog.DB, `SELECT c.native_id,p.native_id parent_native_id,COUNT(m.id) messages
		FROM conversations c LEFT JOIN conversations p ON p.id=c.parent_id
		JOIN messages m ON m.conversation_id=c.id GROUP BY c.id ORDER BY c.native_id`)
	if err != nil || len(rows) != 3 || firstString(rows[0]["native_id"]) != "root-session" ||
		firstString(rows[1]["native_id"]) != "root-session:subagent:agent-1" ||
		firstString(rows[1]["parent_native_id"]) != "root-session" ||
		firstString(rows[2]["native_id"]) != "root-session:subagent:workflows/wf-1/agent-2" ||
		firstString(rows[2]["parent_native_id"]) != "root-session" ||
		integer(rows[0]["messages"]) != 1 || integer(rows[1]["messages"]) != 1 || integer(rows[2]["messages"]) != 1 {
		t.Fatalf("subagent relationship or messages missing: %#v, %v", rows, err)
	}
	kinds, err := queryMaps(catalog.DB, `SELECT c.native_id,a.kind,a.depth FROM conversations c
		JOIN agent_sessions a ON a.conversation_id=c.id WHERE a.native_id='main' ORDER BY c.native_id`)
	if err != nil || len(kinds) != 3 || firstString(kinds[0]["kind"]) != "root" ||
		firstString(kinds[1]["kind"]) != "subagent" || firstString(kinds[2]["kind"]) != "subagent" ||
		integer(kinds[1]["depth"]) != 1 || integer(kinds[2]["depth"]) != 1 {
		t.Fatalf("child session usage kind or depth missing: %#v, %v", kinds, err)
	}
	t.Run("acknowledged nested lineage", func(t *testing.T) {
		catalog, _ := testCatalog(t)
		project := t.TempDir()
		subagents := filepath.Join(project, "session", "subagents")
		if err := os.MkdirAll(subagents, 0o755); err != nil {
			t.Fatal(err)
		}
		write := func(name string, lines ...string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(project, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("session.jsonl",
			`{"sessionId":"session","uuid":"root-launch","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"root-call","name":"Agent","input":{"prompt":"Child assignment"}}]}}`,
			`{"sessionId":"session","uuid":"root-result","type":"user","toolUseResult":{"agentId":"z"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"root-call","content":"launched"}]}}`,
			`{"sessionId":"session","uuid":"root-ambiguous-launch","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"root-ambiguous","name":"Agent","input":{"prompt":"Ambiguous assignment"}}]}}`,
			`{"sessionId":"session","uuid":"root-ambiguous-result","type":"user","toolUseResult":{"agentId":"x"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"root-ambiguous","content":"launched"}]}}`,
			`{"sessionId":"session","uuid":"root-answer","type":"assistant","message":{"role":"assistant","content":"Root done"}}`)
		write("session/subagents/agent-z.jsonl",
			`{"sessionId":"session","uuid":"child-assignment","type":"user","message":{"role":"user","content":"Child assignment"}}`,
			`{"sessionId":"session","uuid":"child-launch","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"child-call","name":"Agent","input":{"prompt":"Grandchild assignment"}}]}}`,
			`{"sessionId":"session","uuid":"child-result","type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"child-call","content":[{"type":"text","text":"Async agent launched successfully.\nagentId: a (internal ID - do not mention to user.)"}]}]}}`,
			`{"sessionId":"session","uuid":"child-ambiguous-launch","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"child-ambiguous","name":"Agent","input":{"prompt":"Ambiguous assignment"}}]}}`,
			`{"sessionId":"session","uuid":"child-ambiguous-result","type":"user","toolUseResult":{"agentId":"x"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"child-ambiguous","content":"launched"}]}}`,
			`{"sessionId":"session","uuid":"child-answer","type":"assistant","message":{"role":"assistant","content":"Child done"}}`)
		write("session/subagents/agent-a.jsonl",
			`{"sessionId":"session","uuid":"grandchild-assignment","type":"user","message":{"role":"user","content":"Grandchild assignment"}}`,
			`{"sessionId":"session","uuid":"grandchild-answer","type":"assistant","message":{"role":"assistant","content":"Grandchild done"}}`)
		write("session/subagents/agent-m.jsonl", `{"sessionId":"session","uuid":"unacknowledged-assignment","type":"user","message":{"role":"user","content":"Unacknowledged"}}`)
		write("session/subagents/agent-x.jsonl", `{"sessionId":"session","uuid":"ambiguous-assignment","type":"user","message":{"role":"user","content":"Ambiguous assignment"}}`)
		adapter, err := MakeAdapter(SourceConfig{Name: "claude", Kind: "claude", Path: project, Account: "local"})
		if err != nil {
			t.Fatal(err)
		}
		if result := catalog.Ingest(adapter, nil); result.Error != nil || result.Conversations != 5 {
			t.Fatalf("nested ingest: %#v", result)
		}
		rows, err := queryMaps(catalog.DB, `SELECT c.id,c.native_id,p.native_id parent,c.agent_depth depth,s.depth session_depth
			FROM conversations c LEFT JOIN conversations p ON p.id=c.parent_id
			JOIN agent_sessions s ON s.conversation_id=c.id AND s.native_id='main' ORDER BY c.native_id`)
		if err != nil || len(rows) != 5 {
			t.Fatalf("lineage rows: %#v, %v", rows, err)
		}
		byNative := map[string]map[string]any{}
		for _, row := range rows {
			byNative[firstString(row["native_id"])] = row
		}
		for _, expected := range []struct {
			native, parent string
			depth          int
		}{
			{"session:subagent:agent-z", "session", 1},
			{"session:subagent:agent-a", "session:subagent:agent-z", 2},
			{"session:subagent:agent-m", "session", 1},
			{"session:subagent:agent-x", "session", 1},
		} {
			row := byNative[expected.native]
			if firstString(row["parent"]) != expected.parent || integer(row["depth"]) != int64(expected.depth) || integer(row["session_depth"]) != int64(expected.depth) {
				t.Fatalf("%s lineage: %#v", expected.native, row)
			}
		}
		grandchild := byNative["session:subagent:agent-a"]
		parent := byNative["session:subagent:agent-z"]
		checkLineage := func(t *testing.T, tool string, item map[string]any) {
			t.Helper()
			lineage, ok := item["lineage"].(map[string]any)
			if !ok || firstString(lineage["parent_conversation_id"]) != firstString(parent["id"]) ||
				firstString(lineage["parent_native_id"]) != firstString(parent["native_id"]) || integer(lineage["depth"]) != 2 {
				t.Fatalf("%s lineage inconsistent: %s", tool, jsonText(item))
			}
		}
		overview, err := callMCP(catalog, "get_conversation_overview", map[string]any{"conversation_id": grandchild["id"], "max_output_tokens": 4000})
		if err != nil {
			t.Fatal(err)
		}
		checkLineage(t, "overview", overview.(map[string]any))
		for _, request := range []struct {
			tool string
			args map[string]any
		}{
			{"trace_worktree", map[string]any{"query": "agent-a", "max_output_tokens": 4000}},
			{"get_work_detail", map[string]any{"workspace_id": stableID("workspace", "claude", "local", "session"), "section": "conversations", "limit": 10, "max_output_tokens": 4000}},
		} {
			result, err := callMCP(catalog, request.tool, request.args)
			if err != nil {
				t.Fatalf("%s: %v", request.tool, err)
			}
			found := false
			for _, item := range result.(map[string]any)["items"].([]map[string]any) {
				if firstString(item["conversation_id"]) == firstString(grandchild["id"]) {
					checkLineage(t, request.tool, item)
					found = true
				}
			}
			if !found {
				t.Fatalf("%s omitted grandchild: %s", request.tool, jsonText(result))
			}
		}
		links, err := queryMaps(catalog.DB, `SELECT c.native_id parent,s.native_id call,p.native_id child
			FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id
			LEFT JOIN conversations p ON p.id=s.child_conversation_id WHERE s.native_id IN ('root-call','child-call','root-ambiguous','child-ambiguous')`)
		if err != nil || len(links) != 4 {
			t.Fatalf("launch links: %#v, %v", links, err)
		}
		for _, link := range links {
			want := map[string]string{"root-call": "session:subagent:agent-z", "child-call": "session:subagent:agent-a"}[firstString(link["call"])]
			if firstString(link["child"]) != want {
				t.Fatalf("unsafe or missing launch link: %#v; want %q", link, want)
			}
		}
		// Existing catalogs retain the launch proof, but older ingesters stored
		// the grandchild under the root and could bind ambiguous launch results.
		root := byNative["session"]
		if _, err := catalog.DB.Exec("UPDATE conversations SET parent_id=?,agent_depth=1 WHERE id=?", root["id"], grandchild["id"]); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec("UPDATE agent_sessions SET depth=1 WHERE conversation_id=?", grandchild["id"]); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec("DELETE FROM message_search_state"); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec("UPDATE agent_sessions SET child_conversation_id=? WHERE native_id='root-ambiguous'", byNative["session:subagent:agent-x"]["id"]); err != nil {
			t.Fatal(err)
		}
		beforeUsage, err := queryMaps(catalog.DB, "SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model")
		if err != nil {
			t.Fatal(err)
		}
		if done, err := catalog.BackfillMessageSearch(t.Context(), nil); err != nil || done != 5 {
			t.Fatalf("retained lineage upgrade: done=%d err=%v", done, err)
		}
		repaired, err := callMCP(catalog, "get_conversation_overview", map[string]any{"conversation_id": grandchild["id"], "max_output_tokens": 4000})
		if err != nil {
			t.Fatal(err)
		}
		checkLineage(t, "upgraded overview", repaired.(map[string]any))
		var depth int
		if err := catalog.DB.QueryRow("SELECT depth FROM agent_sessions WHERE conversation_id=? AND native_id='main'", grandchild["id"]).Scan(&depth); err != nil || depth != 2 {
			t.Fatalf("upgraded session depth=%d err=%v", depth, err)
		}
		afterUsage, err := queryMaps(catalog.DB, "SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model")
		if err != nil || jsonText(beforeUsage) != jsonText(afterUsage) {
			t.Fatalf("lineage repair changed usage: %v", err)
		}
		var ambiguous int
		if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM agent_sessions WHERE native_id IN ('root-ambiguous','child-ambiguous') AND child_conversation_id IS NOT NULL").Scan(&ambiguous); err != nil || ambiguous != 0 {
			t.Fatalf("ambiguous upgrade links=%d err=%v", ambiguous, err)
		}
		t.Run("descendant-only retained upgrade", func(t *testing.T) {
			// A completed catalog can later receive missing launch evidence
			// from another copy. Only the descendant's marker is invalidated.
			messages, err := storedMessages(t.Context(), catalog.DB, parent["id"])
			if err != nil {
				t.Fatal(err)
			}
			var result MessageRecord
			stored := map[string]bool{}
			for _, message := range messages {
				if message.NativeID == "child-result:block:0" {
					result = message
				} else {
					stored[message.NativeID] = true
				}
			}
			if result.NativeID == "" {
				t.Fatal("missing launch acknowledgement fixture")
			}
			tx := seedTx(t, catalog.DB)
			if err := deleteMessageFTS(tx, stableID("message", firstString(parent["id"]), result.NativeID)); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec("DELETE FROM messages WHERE conversation_id=? AND native_id=?", parent["id"], result.NativeID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec("UPDATE conversations SET parent_id=?,agent_depth=1 WHERE id=?", root["id"], grandchild["id"]); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec("UPDATE agent_sessions SET depth=1 WHERE conversation_id=?", grandchild["id"]); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec("UPDATE agent_sessions SET child_conversation_id=NULL,usage_status='unavailable' WHERE conversation_id=? AND native_id='child-call'", parent["id"]); err != nil {
				t.Fatal(err)
			}
			commitSeed(t, tx)
			if _, err := catalog.BackfillMessageSearch(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			tx = seedTx(t, catalog.DB)
			if added, err := mergeConversation(tx, conversationAuthority{id: firstString(parent["id"]), stored: stored}, ConversationRecord{Messages: []MessageRecord{result}}); err != nil || !added {
				t.Fatalf("merge launch evidence: added=%v err=%v", added, err)
			}
			commitSeed(t, tx)
			pending, err := queryMaps(catalog.DB, `SELECT c.id FROM conversations c LEFT JOIN message_search_state s ON s.conversation_id=c.id
			 WHERE s.conversation_id IS NULL OR s.version<>?`, messageSearchVersion)
			if err != nil || len(pending) != 1 || pending[0]["id"] != parent["id"] {
				t.Fatalf("expected only descendant pending: %v %v", pending, err)
			}
			if done, err := catalog.BackfillMessageSearch(t.Context(), nil); err != nil || done != 1 {
				t.Fatalf("descendant-only upgrade: done=%d err=%v", done, err)
			}
			overview, err := callMCP(catalog, "get_conversation_overview", map[string]any{"conversation_id": grandchild["id"], "max_output_tokens": 4000})
			if err != nil {
				t.Fatal(err)
			}
			checkLineage(t, "descendant-only upgrade", overview.(map[string]any))
			var child string
			if err := catalog.DB.QueryRow("SELECT COALESCE(child_conversation_id,'') FROM agent_sessions WHERE conversation_id=? AND native_id='child-call'", parent["id"]).Scan(&child); err != nil || child != firstString(grandchild["id"]) {
				t.Fatalf("upgraded launch link=%q err=%v", child, err)
			}
			if pending, err := catalog.pendingMessageSearch(t.Context()); err != nil || pending != 0 {
				t.Fatalf("upgrade not complete: pending=%d err=%v", pending, err)
			}
			if done, err := catalog.BackfillMessageSearch(t.Context(), nil); err != nil || done != 0 {
				t.Fatalf("completed upgrade replayed: done=%d err=%v", done, err)
			}
		})
		t.Run("reindex retracts rejected links", func(t *testing.T) {
			if result := catalog.Ingest(adapter, nil); result.Error != nil {
				t.Fatal(result.Error)
			}
			var session, child string
			if err := catalog.DB.QueryRow("SELECT id,COALESCE(child_conversation_id,'') FROM agent_sessions WHERE conversation_id=? AND native_id='child-call'", parent["id"]).Scan(&session, &child); err != nil || child != firstString(grandchild["id"]) {
				t.Fatalf("initial launch link=%q err=%v", child, err)
			}
			write("session/subagents/agent-m.jsonl",
				`{"sessionId":"session","uuid":"unacknowledged-assignment","type":"user","message":{"role":"user","content":"Unacknowledged"}}`,
				`{"sessionId":"session","uuid":"competing-launch","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"competing-call","name":"Agent","input":{"prompt":"Grandchild assignment"}}]}}`,
				`{"sessionId":"session","uuid":"competing-result","type":"user","toolUseResult":{"agentId":"a"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"competing-call","content":"launched"}]}}`)
			if result := catalog.Ingest(adapter, nil); result.Error != nil {
				t.Fatal(result.Error)
			}
			var conversationParent, usageStatus string
			var depth int
			if err := catalog.DB.QueryRow("SELECT parent_id,agent_depth FROM conversations WHERE id=?", grandchild["id"]).Scan(&conversationParent, &depth); err != nil || conversationParent != firstString(root["id"]) || depth != 1 {
				t.Fatalf("ambiguous conversation parent=%q depth=%d err=%v", conversationParent, depth, err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(child_conversation_id,''),usage_status FROM agent_sessions WHERE id=?", session).Scan(&child, &usageStatus); err != nil || child != "" || usageStatus != "unavailable" {
				t.Fatalf("rejected launch retained child=%q status=%q err=%v", child, usageStatus, err)
			}
			// The root's independently proven launch remains linked.
			if err := catalog.DB.QueryRow("SELECT COALESCE(child_conversation_id,'') FROM agent_sessions WHERE conversation_id=? AND native_id='root-call'", root["id"]).Scan(&child); err != nil || child != firstString(parent["id"]) {
				t.Fatalf("valid root link=%q err=%v", child, err)
			}
			write("session/subagents/agent-m.jsonl", `{"sessionId":"session","uuid":"unacknowledged-assignment","type":"user","message":{"role":"user","content":"Unacknowledged"}}`)
			if result := catalog.Ingest(adapter, nil); result.Error != nil {
				t.Fatal(result.Error)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(child_conversation_id,''),usage_status FROM agent_sessions WHERE id=?", session).Scan(&child, &usageStatus); err != nil || child != firstString(grandchild["id"]) || usageStatus != "in-child-conversation" {
				t.Fatalf("restored launch child=%q status=%q err=%v", child, usageStatus, err)
			}
		})
	})
	t.Run("text launch acknowledgements", func(t *testing.T) {
		ack := "Async agent launched successfully. (This tool result is internal metadata.)\nagentId: child (internal ID - do not mention to user.)\nThe agent is working in the background."
		for _, test := range []struct {
			name, acknowledgement, assignment string
			failed, linked                    bool
		}{
			{"matched", ack, "Exact assignment", false, true},
			{"different assignment", ack, "Different assignment", false, false},
			{"unacknowledged mention", "Report mentions agentId: child (internal ID)", "Exact assignment", false, false},
			{"failed result", ack, "Exact assignment", true, false},
			{"duplicate IDs", ack + "\nagentId: child (internal ID)", "Exact assignment", false, false},
		} {
			t.Run(test.name, func(t *testing.T) {
				record := &WorkspaceRecord{Conversations: []ConversationRecord{
					{NativeID: "root", Provider: "claude"},
					{NativeID: "root:subagent:agent-parent", Provider: "claude", AgentDepth: 1, Messages: []MessageRecord{
						{Kind: "delegation", CallID: "call", Text: `{"tool":"Agent","input":{"prompt":"Exact assignment"}}`},
						{Kind: "delegation_result", CallID: "call", Text: jsonText(map[string]any{"content": []any{map[string]any{"type": "text", "text": test.acknowledgement}}, "is_error": test.failed})},
					}},
					{NativeID: "root:subagent:agent-child", Provider: "claude", AgentDepth: 1, Messages: []MessageRecord{{Role: "agent", Kind: "message", Text: test.assignment}}},
				}}
				reconcileClaudeLineage(record)
				child := record.Conversations[2]
				want, depth := "root", 1
				if test.linked {
					want, depth = "root:subagent:agent-parent", 2
				}
				if child.ParentNativeID != want || child.AgentDepth != depth {
					t.Fatalf("text acknowledgement lineage: %#v", child)
				}
			})
		}
	})
	t.Run("workflow run acknowledgements", func(t *testing.T) {
		for _, ambiguous := range []bool{false, true} {
			record := &WorkspaceRecord{Conversations: []ConversationRecord{
				{NativeID: "root", Provider: "claude"},
				{NativeID: "root:subagent:agent-parent", Provider: "claude", AgentDepth: 1, Messages: []MessageRecord{
					{Kind: "delegation", CallID: "workflow"},
					{Kind: "delegation_result", CallID: "workflow", Text: `{"details":{"runId":"run"}}`},
				}},
				{NativeID: "root:subagent:workflows/run/agent-one", Provider: "claude", AgentDepth: 1},
			}}
			if ambiguous {
				record.Conversations = append(record.Conversations, ConversationRecord{NativeID: "root:subagent:workflows/run/agent-two", Provider: "claude", AgentDepth: 1})
			}
			reconcileClaudeLineage(record)
			for _, conversation := range record.Conversations {
				if !strings.Contains(conversation.NativeID, ":subagent:workflows/") {
					continue
				}
				want, depth := "root:subagent:agent-parent", 2
				if ambiguous {
					want, depth = "root", 1
				}
				if conversation.ParentNativeID != want || conversation.AgentDepth != depth {
					t.Fatalf("ambiguous=%v workflow lineage: %#v", ambiguous, conversation)
				}
			}
		}
	})
	t.Run("cyclic acknowledgements", func(t *testing.T) {
		launch := func(call, agent string) []MessageRecord {
			return []MessageRecord{
				{Kind: "delegation", CallID: call},
				{Kind: "delegation_result", CallID: call, Text: jsonText(map[string]any{"details": map[string]any{"agentId": agent}})},
			}
		}
		record := &WorkspaceRecord{Conversations: []ConversationRecord{
			{NativeID: "root", Provider: "claude"},
			{NativeID: "root:subagent:agent-a", Provider: "claude", AgentDepth: 1, Messages: launch("a-call", "b")},
			{NativeID: "root:subagent:agent-b", Provider: "claude", AgentDepth: 1, Messages: launch("b-call", "a")},
		}}
		reconcileClaudeLineage(record)
		for _, conversation := range record.Conversations[1:] {
			if conversation.ParentNativeID != "root" || conversation.AgentDepth != 1 {
				t.Fatalf("cycle should keep root fallback: %#v", conversation)
			}
		}
	})
}

func TestNativeToolEventsRetainIdentityResultsAndConversationFlow(t *testing.T) {
	root := t.TempDir()
	codexSessions := filepath.Join(root, "codex", "sessions")
	if err := os.MkdirAll(codexSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	codexLog := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"codex-session"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:00:00Z","payload":{"type":"function_call","id":"call-item","call_id":"call-1","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:00:01Z","payload":{"type":"function_call_output","id":"result-item","call_id":"call-1","output":"ok"}}`,
		`{"type":"event_msg","timestamp":"2026-09-20T12:00:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":12000}}}}`,
		`{"type":"compacted","timestamp":"2026-09-20T12:00:03Z","payload":{"summary":"Reduced context"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(codexSessions, "rollout-test.jsonl"), []byte(codexLog), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "codex", Kind: "codex", Path: filepath.Join(root, "codex"), Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var codex WorkspaceRecord
	if err := adapter.Discover(func(record WorkspaceRecord) error { codex = record; return nil }); err != nil {
		t.Fatal(err)
	}
	messages := codex.Conversations[0].Messages
	if len(messages) != 4 || messages[0].Kind != "tool_call" || messages[1].Kind != "tool_result" {
		t.Fatalf("unexpected Codex tool messages: %#v", messages)
	}
	if !strings.Contains(messages[0].Text, `"tool":"exec_command"`) || messages[0].CallID != "call-1" || messages[1].CallID != "call-1" {
		t.Fatalf("Codex tool identity was not retained: %#v", messages)
	}
	if !strings.Contains(messages[2].Text, `"total_tokens":12000`) || !strings.Contains(messages[3].Text, `"type":"compacted"`) {
		t.Fatalf("Codex usage and compaction missing: %#v", messages)
	}

	claudeRoot := filepath.Join(root, "claude")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeLog := strings.Join([]string{
		`{"sessionId":"claude-session","uuid":"user-1","type":"user","message":{"content":"Please inspect"}}`,
		`{"sessionId":"claude-session","uuid":"assistant-1","parentUuid":"user-1","type":"assistant","message":{"model":"claude-sonnet-test","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"main.go"}}]}}`,
		`{"sessionId":"claude-session","uuid":"result-1","parentUuid":"assistant-1","type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"package main"}]}}`,
		`{"sessionId":"claude-session","uuid":"answer-1","type":"assistant","message":{"id":"request-1","content":"Done","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":1000}}}`,
		`{"sessionId":"claude-session","type":"system","subtype":"compact_boundary"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(claudeRoot, "session.jsonl"), []byte(claudeLog), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err = MakeAdapter(SourceConfig{Name: "claude", Kind: "claude", Path: claudeRoot, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var claude WorkspaceRecord
	if err := adapter.Discover(func(record WorkspaceRecord) error { claude = record; return nil }); err != nil {
		t.Fatal(err)
	}
	messages = claude.Conversations[0].Messages
	foundResult := false
	for _, message := range messages {
		if message.ParentNativeID != "" {
			t.Fatalf("ordinary Claude message-chain parent leaked into visual nesting: %#v", message)
		}
		if message.Kind == "tool_result" && strings.Contains(message.Text, `"tool":"Read"`) {
			foundResult = true
		}
		if message.NativeID == "assistant-1:block:0" && (message.Model != "claude-sonnet-test" || message.PreviousNativeID != "user-1") {
			t.Fatalf("Claude model or event chain missing: %#v", message)
		}
	}
	if !foundResult {
		t.Fatalf("Claude regular tool result or identity missing: %#v", messages)
	}
	foundUsage, foundCompaction := false, false
	for _, message := range messages {
		foundUsage = foundUsage || strings.Contains(message.Text, `"cache_read_input_tokens":1000`)
		foundCompaction = foundCompaction || strings.Contains(message.Text, `"subtype":"compact_boundary"`)
	}
	if !foundUsage || !foundCompaction {
		t.Fatalf("Claude usage or compaction missing: %#v", messages)
	}
}

func TestCanonicalIngestAndAPIContract(t *testing.T) {
	catalog, config := testCatalog(t)
	export := filepath.Join(t.TempDir(), "export.json")
	document := map[string]any{"workspaces": []any{
		map[string]any{
			"id": "work-1", "title": "Fix parser", "activity_at": "2026-09-20T12:00:00Z",
			"repository": map[string]any{"canonical_remote": "github.com/acme/parser", "display_name": "parser"},
			"conversations": []any{map[string]any{
				"id": "conversation-1", "provider": "codex", "messages": []any{
					map[string]any{"id": "message-1", "role": "user", "text": "Please inspect src/parser.go"},
					map[string]any{"id": "message-2", "role": "assistant", "text": "Fixed the parser"},
					map[string]any{"id": "message-3", "role": "assistant", "kind": "reasoning", "reasoning_format": "summary", "text": "Now I understand the parser"},
				},
			}},
			"changes": []any{map[string]any{
				"classification": "attempted", "files": []any{map[string]any{"path": "src/parser.go", "status": "modified"}},
			}},
		},
	}}
	payload, _ := json.Marshal(document)
	if err := os.WriteFile(export, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	source := SourceConfig{Name: "fixture", Kind: "canonical", Path: export, Account: "local", Enabled: true}
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	result := catalog.Ingest(adapter, nil)
	if result.Error != nil {
		t.Fatalf("ingest failed: %v", result.Error)
	}
	if result.Workspaces != 1 || result.Messages != 3 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	var format string
	if err := catalog.DB.QueryRow("SELECT reasoning_format FROM messages WHERE native_id='message-3'").Scan(&format); err != nil || format != "summary" {
		t.Fatalf("canonical reasoning summary lost: %q %v", format, err)
	}
	found, err := catalog.Search(SearchOptions{Query: "parser", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(found["items"].([]map[string]any)) != 1 {
		t.Fatalf("expected search hit: %#v", found)
	}
	server := testServer(t, config, catalog)
	request := httptest.NewRequest(http.MethodGet, "/api/search?q=parser", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body["items"].([]any)) != 1 {
		t.Fatalf("unexpected API body: %s", response.Body.String())
	}
	queryRequest := httptest.NewRequest(http.MethodPost, "/api/query/library", strings.NewReader(`{"select":["title","source_kind","file_edit_count"],"where":[{"field":"source_kind","op":"=","value":"canonical"}],"orderBy":[{"field":"title","dir":"asc"}],"limit":25,"offset":0}`))
	queryRequest.Header.Set("Authorization", "Bearer test-token")
	queryResponse := httptest.NewRecorder()
	server.ServeHTTP(queryResponse, queryRequest)
	if queryResponse.Code != http.StatusOK {
		t.Fatalf("query-table status %d: %s", queryResponse.Code, queryResponse.Body.String())
	}
	var queryBody map[string]any
	if err := json.Unmarshal(queryResponse.Body.Bytes(), &queryBody); err != nil {
		t.Fatal(err)
	}
	if queryBody["total"] != float64(1) || len(queryBody["rows"].([]any)) != 1 {
		t.Fatalf("unexpected query-table body: %s", queryResponse.Body.String())
	}
	row := queryBody["rows"].([]any)[0].(map[string]any)
	if integer(row["file_edit_count"]) != 1 {
		t.Fatalf("file edit count was not exposed in library query: %#v", row)
	}
	if integer(row["turn_count"]) != 1 || firstString(row["first_input"]) != "Please inspect src/parser.go" || firstString(row["last_response"]) != "Fixed the parser" {
		t.Fatalf("conversation preview was not exposed in library query: %#v", row)
	}
}

func TestHealthReportsActiveSync(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	check := func(want bool) {
		t.Helper()
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("health status %d: %s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["sync_active"] != want {
			t.Fatalf("sync_active=%v, want %v", body["sync_active"], want)
		}
	}
	check(false)
	run := server.startRun([]SourceConfig{{Name: "test"}})
	check(true)
	server.updateRun(run.ID, func(run *SyncRun) { run.State = "complete" })
	check(false)
}

func TestAppCSPAllowsOnlyLocalBlobFormulaWorkers(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "worker-src 'self' blob:") {
		t.Fatalf("formula workers are blocked by CSP: %q", policy)
	}
	if strings.Contains(policy, "http:") || strings.Contains(policy, "https:") {
		t.Fatalf("CSP unexpectedly allows remote worker sources: %q", policy)
	}
}

func TestConductorSemanticEventsRepositoryAndPR(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	worktree := filepath.Join(repository, ".conductor", "kiev")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "conductor.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE sessions (id TEXT PRIMARY KEY,status TEXT,claude_session_id TEXT,created_at TEXT,updated_at TEXT,last_user_message_at TEXT,workspace_id TEXT,title TEXT,model TEXT,agent_type TEXT);
CREATE TABLE workspaces (id TEXT PRIMARY KEY,repository_id TEXT,branch TEXT,state TEXT,workspace_path TEXT,archive_commit TEXT,pr_title TEXT);
CREATE TABLE repos (id TEXT PRIMARY KEY,remote_url TEXT,name TEXT,root_path TEXT);
CREATE TABLE session_messages (id TEXT PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,created_at TEXT,model TEXT,turn_id TEXT,sender_session_id TEXT);`
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO sessions VALUES (?,?,?,?,?,?,?,?,?,?)", "session-1", "idle", "claude-native", "2026-09-20T10:00:00Z", "2026-09-20T10:02:00Z", "2026-09-20T10:00:00Z", "workspace-1", "Contextual time", "opus", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO workspaces VALUES (?,?,?,?,?,?,?)", "workspace-1", "repo-1", "feature/time", "archived", worktree, nil, "Contextual timestamps"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO repos VALUES (?,?,?,?)", "repo-1", "https://github.com/acme/library.git", "library", repository); err != nil {
		t.Fatal(err)
	}
	events := []struct{ id, role, content, at string }{{"m1", "user", "Please update timestamps", "2026-09-20T10:00:00Z"}, {"m2", "assistant", jsonText(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "task-call", "name": "Task", "input": map[string]any{"description": "Find timestamp use", "prompt": "Inspect the UI"}}}}}), "2026-09-20T10:00:10Z"}, {"m2-edit", "assistant", jsonText(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "edit-call", "name": "Edit", "input": map[string]any{"file_path": filepath.Join(worktree, "web", "time.ts"), "old_string": "old", "new_string": "new"}}}}}), "2026-09-20T10:00:15Z"}, {"m3", "assistant", jsonText(map[string]any{"type": "assistant", "parent_tool_use_id": "task-call", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Found the formatter"}}}}), "2026-09-20T10:00:20Z"}, {"m4", "assistant", jsonText(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "task-call", "is_error": true, "content": "https://github.com/acme/library/pull/64"}}}}), "2026-09-20T10:00:30Z"}, {"m5", "assistant", jsonText(map[string]any{"type": "result", "subtype": "success", "usage": map[string]any{"input_tokens": 1200, "output_tokens": 350, "cache_creation_input_tokens": 800, "cache_read_input_tokens": 4200, "output_tokens_details": map[string]any{"thinking_tokens": 125}}}), "2026-09-20T10:00:40Z"}}
	for _, event := range events {
		if _, err = db.Exec("INSERT INTO session_messages VALUES (?,?,?,?,?,NULL,NULL,NULL)", event.id, "session-1", event.role, event.content, event.at); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: root, Account: "local", Enabled: true}
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	result := catalog.Ingest(adapter, nil)
	if result.Error != nil {
		t.Fatalf("ingest failed: %v", result.Error)
	}
	id := stableID("workspace", "conductor", "local", "session-1")
	detail, err := catalog.WorkDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail["repository_name"] != "library" {
		t.Fatalf("repository not linked: %#v", detail)
	}
	if detail["purpose"] != "Please update timestamps" || detail["outcome"] != "Found the formatter" {
		t.Fatalf("purpose/outcome not derived: %#v", detail)
	}
	messages := detail["conversations"].([]map[string]any)[0]["messages"].([]map[string]any)
	kinds := []string{}
	for _, message := range messages {
		kinds = append(kinds, firstString(message["kind"]))
	}
	want := []string{"message", "delegation", "tool_call", "message", "delegation_result", "result"}
	if jsonText(kinds) != jsonText(want) {
		t.Fatalf("message kinds got %v want %v", kinds, want)
	}
	for _, message := range messages {
		if firstString(message["model"]) != "opus" {
			t.Fatalf("Conductor session model missing from message: %#v", message)
		}
	}
	prs := detail["prs"].([]map[string]any)
	if len(prs) != 1 || integer(prs[0]["number"]) != 64 || firstString(prs[0]["title"]) != "Contextual timestamps" {
		t.Fatalf("PR not linked: %#v", prs)
	}
	conversations := detail["conversations"].([]map[string]any)
	if firstString(conversations[0]["provider"]) != "claude" || firstString(conversations[0]["model"]) != "opus" {
		t.Fatalf("native provider/model not retained: %#v", conversations[0])
	}
	metrics := detail["metrics"].([]map[string]any)
	foundTokens := map[string]int64{}
	for _, metric := range metrics {
		if firstString(metric["unit"]) == "tokens" {
			foundTokens[firstString(metric["name"])] = integer(metric["value"])
		}
	}
	if foundTokens["input_tokens"] != 6200 || foundTokens["uncached_input_tokens"] != 1200 || foundTokens["output_tokens"] != 350 || foundTokens["cache_creation_input_tokens"] != 800 || foundTokens["cache_read_input_tokens"] != 4200 || foundTokens["reasoning_output_tokens"] != 125 {
		t.Fatalf("token metrics not extracted: %#v", foundTokens)
	}
	libraryRows, err := catalog.searchRows(SearchOptions{}, nil)
	if err != nil || len(libraryRows) != 1 {
		t.Fatalf("library rows: %#v, %v", libraryRows, err)
	}
	row := libraryRows[0]
	var prDetails []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(firstString(row["pr_details"])), &prDetails); err != nil || len(prDetails) != 1 || prDetails[0].Number != 64 || prDetails[0].Title != "Contextual timestamps" || prDetails[0].URL == "" {
		t.Fatalf("library PR details: %#v, %v", prDetails, err)
	}
	for key, want := range map[string]int64{"token_count": 6550, "tool_use_count": 2, "tool_error_count": 1, "pr_count": 1, "branch_count": 1, "file_edit_count": 1} {
		if got := integer(row[key]); got != want {
			t.Errorf("%s = %d, want %d (row %#v)", key, got, want, row)
		}
	}
	tokenUsage := catalog.Health()["token_usage"].([]map[string]any)
	if len(tokenUsage) != 1 || firstString(tokenUsage[0]["service"]) != "claude" ||
		firstString(tokenUsage[0]["model"]) != "opus" || integer(tokenUsage[0]["tokens"]) != 6550 {
		t.Fatalf("health token usage not grouped by service and model: %#v", tokenUsage)
	}
	claudeRoot := filepath.Join(root, "claude")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(claudeRoot, "session-1.jsonl")
	if err := os.WriteFile(claudePath, []byte("{\"sessionId\":\"session-1\",\"uuid\":\"native-message\",\"type\":\"assistant\",\"message\":{\"model\":\"claude-test\",\"content\":\"Native answer\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	native, err := MakeAdapter(SourceConfig{Name: "claude", Kind: "claude", Path: claudeRoot, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(native, nil); result.Error != nil {
		t.Fatalf("native Claude ingest: %v", result.Error)
	}
	rows, err := queryMaps(catalog.DB, `SELECT w.source_kind,c.native_id,COUNT(m.id) message_count
		FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		JOIN messages m ON m.conversation_id=c.id WHERE c.provider='claude'
		GROUP BY c.id ORDER BY w.source_kind`)
	if err != nil || len(rows) != 2 || firstString(rows[0]["source_kind"]) != "claude" ||
		firstString(rows[0]["native_id"]) != "session-1" || integer(rows[0]["message_count"]) != 1 ||
		firstString(rows[1]["source_kind"]) != "conductor" ||
		firstString(rows[1]["native_id"]) != "conductor:session-1" || integer(rows[1]["message_count"]) != 6 {
		t.Fatalf("source conversation identity collision: %#v, %v", rows, err)
	}
}

func TestSourceTogglePreservesOtherConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.toml")
	source := "api_token = \"secret\" # keep\n\n[[sources]]\nname = \"one\"\nkind = \"canonical\"\npath = \"/tmp/one\"\nenabled = true\n\n[[sources]]\nname = \"two\"\nkind = \"canonical\"\npath = \"/tmp/two\"\nenabled = true\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetSourceEnabled(path, "two", false); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Sources[0].Enabled || config.Sources[1].Enabled {
		t.Fatalf("wrong source toggle: %#v", config.Sources)
	}
	if config.APIToken != "secret" {
		t.Fatal("root configuration was changed")
	}
}

func TestTL1RegistryIsReadOnlyAndIndexesNativeTask(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	transcripts := filepath.Join(root, "transcripts")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(repository, "tl1.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "tl1.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY,title TEXT,status TEXT,inputs TEXT,outcome TEXT,candidate_id TEXT,flavor_id TEXT,shape_version TEXT,created_at TEXT,completed_at TEXT,updated_at TEXT,instruction_prepend TEXT,instruction_append TEXT,terminal_kind TEXT);
CREATE TABLE task_flavors (id TEXT PRIMARY KEY,name TEXT NOT NULL);
CREATE TABLE task_attempts (id TEXT PRIMARY KEY,task_id TEXT,attempt_number INTEGER,worktree_id TEXT,executor TEXT,session_id TEXT,model TEXT,effort TEXT,agent TEXT,status TEXT,result_summary TEXT,failure_reason TEXT,files_changed INTEGER,lines_added INTEGER,lines_removed INTEGER,started_at TEXT,completed_at TEXT,harness_id TEXT);
INSERT INTO task_flavors VALUES ('flavor-1','candidate-review');
INSERT INTO tasks VALUES ('task-1','Native TL1 task','completed','{"request":"index"}','Indexed',NULL,'flavor-1','v1','2026-09-20T11:00:00Z','2026-09-20T12:01:00Z','2026-09-20T12:01:00Z','','','completed');
INSERT INTO task_attempts VALUES ('attempt-1','task-1',1,'worktree-1','claude','session-1','sonnet','high','worker','completed','Done',NULL,2,10,1,'2026-09-20T11:00:00Z','2026-09-20T12:01:00Z',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	registry := filepath.Join(root, "registry.json")
	registryValue := map[string]any{"schema_version": 1, "installations": []any{map[string]any{"installation_id": "install-1", "project_name": "fixture", "config_path": configPath, "db_path": database, "code_repo": repository, "transcripts_dir": transcripts}}}
	payload, _ := json.Marshal(registryValue)
	if err := os.WriteFile(registry, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Capability() != "retrieval-only" {
		t.Fatalf("native TL1 must remain read-only")
	}
	result := catalog.Ingest(adapter, nil)
	if result.Error != nil {
		t.Fatalf("ingest failed: %v", result.Error)
	}
	if result.Workspaces != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	var sourceID, flavor string
	var authority int
	if err := catalog.DB.QueryRow("SELECT source_id,flavor,reclamation_authority FROM workspaces WHERE source_kind='tl1'").Scan(&sourceID, &flavor, &authority); err != nil {
		t.Fatal(err)
	}
	if sourceID != "install-1:task-1" || flavor != "candidate-review" || authority != 0 {
		t.Fatalf("unexpected TL1 workspace: %s flavor=%s authority=%d", sourceID, flavor, authority)
	}
}

func TestAppRoutesServeUIOnDirectNavigation(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	for _, path := range []string{"/", "/home", "/cards?card_topic=cost", "/library?search=debug", "/sources", "/settings/sync", "/settings/header", "/settings/layout", "/settings/mcp", "/analyze", "/mcp", "/activity", "/health", "/work/workspace_123", "/work/workspace_123/conversation/conversation_456?message=tool_789"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		// The page is the embedded shell, served without Python.
		if body := response.Body.String(); response.Code != http.StatusOK || !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "id=\"navigator\"") {
			t.Errorf("GET %s: status %d, missing app UI", path, response.Code)
		}
	}
	// The browser fixture serves the asset directory directly. Check the live
	// server serves every script the shell loads, including new classic scripts.
	for _, match := range regexp.MustCompile(`<script src="([^"]+)"`).FindAllStringSubmatch(appHTML(), -1) {
		request := httptest.NewRequest(http.MethodGet, match[1], nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/javascript") || response.Body.Len() == 0 {
			t.Errorf("GET %s: status %d, missing script", match[1], response.Code)
		}
	}
}
