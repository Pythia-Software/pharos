package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseJSONL(t *testing.T, provider, path string, lines ...string) []MessageRecord {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, ok, err := (&jsonlAdapter{provider: provider}).parse(path)
	if err != nil || !ok {
		t.Fatalf("parse: ok=%v err=%v", ok, err)
	}
	return record.Conversations[0].Messages
}

func byNativeID(messages []MessageRecord) map[string]MessageRecord {
	result := map[string]MessageRecord{}
	for _, message := range messages {
		result[message.NativeID] = message
	}
	return result
}

func humanTurns(messages []MessageRecord) []string {
	turns := []string{}
	for _, message := range messages {
		if message.Role == "user" && message.Kind == "message" {
			turns = append(turns, message.Text)
		}
	}
	return turns
}

func TestClaudeSeparatesInjectedInputFromHumanTurns(t *testing.T) {
	messages := parseJSONL(t, "claude", filepath.Join(t.TempDir(), "session.jsonl"),
		`{"sessionId":"s","uuid":"prompt","type":"user","message":{"role":"user","content":"Show me the header"}}`,
		`{"sessionId":"s","uuid":"read","parentUuid":"prompt","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"/tmp/header.png"}}]}}`,
		`{"sessionId":"s","uuid":"result","parentUuid":"read","type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_read","type":"tool_result","content":[{"type":"image","source":{"type":"base64","data":"AAAA","media_type":"image/png"}}]}]},"sourceToolAssistantUUID":"read"}`,
		`{"sessionId":"s","uuid":"caption","parentUuid":"result","type":"user","message":{"role":"user","content":"[Image: original 3414x1894, displayed at 2000x1110. Multiply coordinates by 1.71 to map to original image.]"},"isMeta":true,"turnCompanion":true}`,
		`{"sessionId":"s","uuid":"skill","type":"user","message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /skills/run"}]},"isMeta":true}`,
		`{"sessionId":"s","uuid":"notice","type":"user","message":{"role":"user","content":"<task-notification>\n<task-id>1</task-id></task-notification>"},"origin":{"kind":"task-notification"}}`,
		`{"sessionId":"s","uuid":"summary","type":"user","message":{"role":"user","content":"This session is being continued from a previous conversation."},"isCompactSummary":true,"isVisibleInTranscriptOnly":true}`,
		`{"sessionId":"s","uuid":"stdout","type":"user","message":{"role":"user","content":"<local-command-stdout>Compacted</local-command-stdout>"}}`,
		`{"sessionId":"s","uuid":"pasted","type":"user","message":{"role":"user","content":[{"type":"text","text":"What about this one?"},{"type":"image","source":{"type":"base64","data":"AAAA","media_type":"image/jpeg"}}]}}`,
		`{"sessionId":"s","uuid":"only-image","type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AAAA","media_type":"image/png"}}]}}`,
	)
	if turns := humanTurns(messages); strings.Join(turns, "|") != "Show me the header|What about this one?|(image attachment)" {
		t.Fatalf("human turns = %q", turns)
	}
	found := byNativeID(messages)
	for id, source := range map[string]string{"caption": "image_note", "skill": "skill", "notice": "task_notification", "summary": "compaction_summary", "stdout": "command_output"} {
		message := found[id]
		if message.Role != "system" || message.Kind != contextKind || !strings.Contains(message.Text, `"source":"`+source+`"`) {
			t.Fatalf("%s = %#v, want %s context", id, message, source)
		}
	}
	if found["caption"].CallID != "toolu_read" {
		t.Fatalf("image caption is not linked to its tool call: %#v", found["caption"])
	}
	if attachment := found["pasted:attachments"]; attachment.Role != "user" || attachment.Kind != "attachment" || !strings.Contains(attachment.Text, "image/jpeg") || strings.Contains(attachment.Text, "AAAA") {
		t.Fatalf("user attachment = %#v", attachment)
	}
	if purpose, _ := agentPurposeOutcome(messages); purpose != "Show me the header" {
		t.Fatalf("purpose = %q", purpose)
	}
}

func TestClaudeSubagentPromptComesFromAgent(t *testing.T) {
	root := t.TempDir()
	messages := parseJSONL(t, "claude", filepath.Join(root, "root-session", "subagents", "agent-1.jsonl"),
		`{"sessionId":"root-session","uuid":"brief","type":"user","message":{"role":"user","content":"Audit the parser"}}`,
		`{"sessionId":"root-session","uuid":"answer","type":"assistant","message":{"role":"assistant","content":"Audited"}}`,
	)
	if brief := byNativeID(messages)["brief"]; brief.Role != agentRole || brief.Kind != "message" {
		t.Fatalf("sub-agent prompt = %#v", brief)
	}
	if purpose, _ := agentPurposeOutcome(messages); purpose != "Audit the parser" {
		t.Fatalf("sub-agent purpose = %q", purpose)
	}
}

func TestCodexSeparatesInjectedContextFromHumanTurns(t *testing.T) {
	messages := parseJSONL(t, "codex", filepath.Join(t.TempDir(), "rollout.jsonl"),
		`{"type":"session_meta","payload":{"id":"root"}}`,
		`{"type":"response_item","payload":{"type":"message","id":"tagged","role":"user","content":[{"type":"input_text","text":"<recommended_plugins>x</recommended_plugins>"},{"type":"input_text","text":"<environment_context><cwd>/w</cwd></environment_context>"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["plugins.recommendations","environments.environment_context"]}}}`,
		`{"type":"response_item","payload":{"type":"message","id":"legacy","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","id":"prompt","role":"user","content":[{"type":"input_text","text":"Fix the tests"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text","user.image"]}}}`,
		`{"type":"response_item","payload":{"type":"message","id":"aborted","role":"user","content":[{"type":"input_text","text":"<turn_aborted>\nThe user interrupted.\n</turn_aborted>"}]}}`,
	)
	if turns := humanTurns(messages); strings.Join(turns, "|") != "Fix the tests" {
		t.Fatalf("human turns = %q", turns)
	}
	found := byNativeID(messages)
	for id, source := range map[string]string{"tagged:block:0": "plugins.recommendations", "tagged:block:1": "environments.environment_context", "legacy:block:0": "environments.environment_context", "aborted:block:0": "turn_aborted"} {
		if message := found[id]; message.Kind != contextKind || !strings.Contains(message.Text, `"source":"`+source+`"`) {
			t.Fatalf("%s = %#v, want %s context", id, message, source)
		}
	}
	if attachment := found["prompt:attachments"]; attachment.Kind != "attachment" || strings.Contains(attachment.Text, "AAAA") {
		t.Fatalf("user attachment = %#v", attachment)
	}
}

func TestCodexSubagentPromptComesFromAgent(t *testing.T) {
	messages := parseJSONL(t, "codex", filepath.Join(t.TempDir(), "rollout-child.jsonl"),
		`{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}}}`,
		`{"type":"response_item","payload":{"type":"message","id":"brief","role":"user","content":[{"type":"input_text","text":"Review the diff"}]}}`,
	)
	if brief := byNativeID(messages)["brief"]; brief.Role != agentRole {
		t.Fatalf("sub-agent prompt = %#v", brief)
	}
}

func TestClaudeSkipsReplayedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	call := func(uuid, parent, id, at string) string {
		return `{"sessionId":"s","uuid":"` + uuid + `","parentUuid":"` + parent + `","timestamp":"` + at + `","type":"assistant","message":{"id":"` + id + `","model":"claude-test","content":[{"type":"tool_use","id":"toolu_` + uuid + `","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`
	}
	result := func(uuid, parent, at, stdout string) string {
		return `{"sessionId":"s","uuid":"` + uuid + `","parentUuid":"` + parent + `","timestamp":"` + at + `","type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_` + parent + `","content":"listing"}]},"toolUseResult":{"stdout":"` + stdout + `","exitCode":0}}`
	}
	prompt := `{"sessionId":"s","uuid":"prompt","timestamp":"2026-09-24T17:08:00Z","type":"user","message":{"role":"user","content":"List the files"}}`
	lines := []string{
		prompt, call("a1", "prompt", "msg_1", "2026-09-24T17:08:01Z"), result("r1", "a1", "2026-09-24T17:08:02Z", "main.go"),
		`{"sessionId":"s","uuid":"boundary","timestamp":"2026-09-24T17:09:00Z","type":"system","subtype":"compact_boundary"}`,
		call("a2", "boundary", "msg_2", "2026-09-24T17:09:01Z"), result("r2", "a2", "2026-09-24T17:09:02Z", "go.mod"),
		// Claude Code writes the earlier entries again, tool output stripped.
		prompt, call("a1", "prompt", "msg_1", "2026-09-24T17:08:01Z"), result("r1", "a1", "2026-09-24T17:08:02Z", ""),
		call("a3", "r1", "msg_3", "2026-09-24T17:10:01Z"),
	}
	messages := parseJSONL(t, "claude", path, lines...)
	seen := map[string]bool{}
	for _, message := range messages {
		if seen[message.NativeID] {
			t.Fatalf("%s parsed twice: %#v", message.NativeID, messages)
		}
		seen[message.NativeID] = true
	}
	if result := byNativeID(messages)["r1:block:0"]; !strings.HasSuffix(result.EvidenceLocator, ":3:block:0") {
		t.Fatalf("replay replaced the original tool result: %#v", result)
	}

	// Rebuilding derived rows from the stored messages must reproduce what
	// the parse derived; the integrity audit compares the two.
	catalog, _ := testCatalog(t)
	record, _, err := (&jsonlAdapter{provider: "claude", baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}}).parse(path)
	if err != nil {
		t.Fatal(err)
	}
	ingestRecords(t, catalog, record)
	snapshot := func() string {
		rows, err := queryMaps(catalog.DB, `SELECT 'session' kind,message_id id,source_order position,NULL carried FROM agent_session_messages
			UNION ALL SELECT 'request',native_id,sequence,NULL FROM model_requests
			UNION ALL SELECT 'call',call_id,sequence,carried_requests FROM tool_calls ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		return jsonText(rows)
	}
	parsed := snapshot()
	conversation, err := queryMaps(catalog.DB, "SELECT id,workspace_id,provider,model,started_at,agent_depth FROM conversations")
	if err != nil || len(conversation) != 1 {
		t.Fatalf("conversations = %v, %v", conversation, err)
	}
	if err := catalog.rebuildAgentSessions(conversation[0]); err != nil {
		t.Fatal(err)
	}
	stored, err := storedMessages(context.Background(), catalog.DB, conversation[0]["id"])
	if err != nil {
		t.Fatal(err)
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceToolLedger(tx, firstString(conversation[0]["workspace_id"]), firstString(conversation[0]["id"]), ConversationRecord{Provider: "claude", Model: firstString(conversation[0]["model"]), Messages: stored}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if rebuilt := snapshot(); rebuilt != parsed {
		t.Fatalf("rebuilt from stored messages:\n%s\nparsed:\n%s", rebuilt, parsed)
	}
}
