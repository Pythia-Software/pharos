package archive

import (
	"context"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func skillTestCall(id, name string, input any) MessageRecord {
	return MessageRecord{NativeID: id, CallID: id, Role: "assistant", Kind: "tool_call", Text: jsonText(map[string]any{"tool": name, "input": input})}
}

func skillTestResult(id string, content any, failed bool) MessageRecord {
	return MessageRecord{NativeID: id + ":result", CallID: id, Role: "tool", Kind: "tool_result", Text: jsonText(map[string]any{"content": content, "is_error": failed})}
}

func TestSkillUsageEvidence(t *testing.T) {
	messages := []MessageRecord{
		{NativeID: "listing", Role: "developer", Kind: "message", Text: "<skills_instructions>\n- not-used: Offered. (file: /skills/not-used/SKILL.md)\n</skills_instructions>"},
		skillTestCall("invoke", "Skill", map[string]any{"skill": "plugin:alpha", "args": "work"}),
		skillTestResult("invoke", "Launching skill: alpha", false),
		{NativeID: "body", CallID: "invoke", Role: "system", Kind: contextKind, Text: jsonText(map[string]any{"type": "injected_context", "source": "skill", "text": "Base directory for this skill: /skills/alpha\n\n# Alpha\nInstructions"})},
		skillTestCall("failed", "Skill", map[string]any{"skill": "missing"}),
		skillTestResult("failed", "Unknown skill: missing", true),
		skillTestCall("files", "exec_command", map[string]any{"cmd": "cat '/skills/alpha/SKILL.md' && sed -n '1,20p' /skills/beta/SKILL.md && cat /skills/alpha/SKILL.md", "workdir": "/repo"}),
		skillTestResult("files", "skill bodies", false),
		skillTestCall("read", "view_file", map[string]any{"AbsolutePath": "/skills/gamma/SKILL.md"}),
		skillTestResult("read", "# Gamma", false),
		skillTestCall("edit", "Write", map[string]any{"file_path": "/skills/edited/SKILL.md"}),
		skillTestResult("edit", "written", false),
		skillTestCall("search", "exec_command", map[string]any{"cmd": "rg name /skills/searched/SKILL.md; echo /skills/echoed/SKILL.md; wc -l /skills/counted/SKILL.md; sed -i 's/a/b/' /skills/changed/SKILL.md"}),
		skillTestResult("search", "search output", false),
		skillTestCall("list", "ls", map[string]any{"path": "/skills/listed/SKILL.md"}),
		skillTestResult("list", "SKILL.md", false),
		skillTestCall("command", "SlashCommand", map[string]any{"command": "/clear"}),
		skillTestResult("command", "cleared", false),
		{NativeID: "snapshot", Role: "system", Kind: "metadata", Text: `{"type":"attachment","attachment":{"type":"invoked_skills","skills":[{"name":"alpha","path":"/skills/alpha/SKILL.md","content":"# Alpha\nInstructions"},{"name":"delta","path":"bundled:delta","content":"# Delta"}]}}`},
		{NativeID: "replay", Role: "system", Kind: "metadata", Text: `{"type":"attachment","attachment":{"type":"invoked_skills","skills":[{"name":"delta","path":"bundled:delta","content":"# Delta"}]}}`},
		{NativeID: "changed", Role: "system", Kind: "metadata", Text: `{"type":"attachment","attachment":{"type":"invoked_skills","skills":[{"name":"alpha","path":"/skills/alpha/SKILL.md","content":"# Alpha v2"}]}}`},
	}
	_, calls := buildToolLedger(messages, "test")
	usages := buildSkillUsages(messages, calls)
	if len(usages) != 7 {
		t.Fatalf("usages = %#v", usages)
	}
	alpha := usages[0]
	if alpha.Name != "plugin:alpha" || alpha.Evidence != "explicit_invocation" || alpha.Path != "/skills/alpha/SKILL.md" || alpha.BodyNativeID != "body" || alpha.ContentBytes == nil || *alpha.ContentBytes != int64(len("\n# Alpha\nInstructions")) {
		t.Fatalf("invocation = %#v", alpha)
	}
	if usages[1].Status != "error" || usages[1].BodyNativeID != "" {
		t.Fatalf("failed invocation = %#v", usages[1])
	}
	for _, usage := range usages[2:5] {
		if usage.Evidence != "file_load" || usage.CallKey == "" {
			t.Fatalf("file load = %#v", usage)
		}
	}
	if usages[5].Name != "delta" || usages[5].Evidence != "harness_load" || usages[5].CallKey != "" || *usages[5].ContentBytes != 7 {
		t.Fatalf("snapshot = %#v", usages[5])
	}
	if usages[6].Evidence != "harness_load" || usages[6].BodyNativeID != "changed" {
		t.Fatalf("changed skill content was lost = %#v", usages[6])
	}
}

func TestSkillReadPaths(t *testing.T) {
	cases := []struct {
		command string
		paths   string
	}{
		{`/bin/zsh -lc "cd sub && cat .agents/skills/alpha/SKILL.md"`, "/repo/sub/.agents/skills/alpha/SKILL.md"},
		{`cat '/skills/with spaces/SKILL.md' /skills/beta/SKILL.md`, "/skills/with spaces/SKILL.md,/skills/beta/SKILL.md"},
		{`cat "$SKILLS/alpha/SKILL.md"; cat /skills/*/SKILL.md`, ""},
		{`cat <<EOF
/skills/mentioned/SKILL.md
EOF`, ""},
		{`python -c 'print("/skills/mentioned/SKILL.md")'`, ""},
	}
	for _, test := range cases {
		call := toolCall{CWD: "/repo", commandTexts: []string{test.command}}
		if got := strings.Join(skillReadPaths(call), ","); got != test.paths {
			t.Errorf("%q: got %q, want %q", test.command, got, test.paths)
		}
	}
}

func TestHarnessSkillLoadCompactionReplay(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, name := range []string{"alpha", "plugin:alpha"} {
			t.Run(firstString(wrapped)+"/"+name, func(t *testing.T) {
				text := "Base directory for this skill: /skills/alpha\n\n# Alpha\nInstructions\n"
				body := MessageRecord{NativeID: "body", Role: "user", Kind: "message", Text: text}
				if wrapped {
					body = injectedContext(body, "skill", body.Text)
				}
				snapshot := func(id string, content any) MessageRecord {
					skill := map[string]any{"name": name, "path": "/skills/alpha/SKILL.md"}
					if content != nil {
						skill["content"] = content
					}
					return MessageRecord{NativeID: id, Role: "system", Kind: "metadata", Text: jsonText(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "invoked_skills", "skills": []any{skill}}})}
				}
				messages := []MessageRecord{
					{NativeID: "command", Role: "user", Kind: "message", Text: "/alpha"},
					body,
					{NativeID: "compact", Role: "system", Kind: "metadata", Text: `{"type":"system","subtype":"compact_boundary"}`},
					snapshot("snapshot", "# Alpha\nInstructions"),
					snapshot("replay", "\n# Alpha\nInstructions\n"),
					snapshot("headed-replay", text),
					snapshot("without-content", nil),
				}
				_, calls := buildToolLedger(messages, "test")
				usages := buildSkillUsages(messages, calls)
				if len(calls) != 0 || len(usages) != 1 || usages[0].Evidence != "harness_load" || usages[0].EvidenceNativeID != "body" || usages[0].BodyNativeID != "body" || usages[0].ContentBytes == nil || *usages[0].ContentBytes != int64(len("\n# Alpha\nInstructions\n")) {
					t.Fatalf("compaction duplicated or replaced the typed load: calls=%#v usages=%#v", calls, usages)
				}
				messages = append(messages, snapshot("changed", "# Alpha v2"))
				usages = buildSkillUsages(messages, calls)
				if len(usages) != 2 || usages[1].BodyNativeID != "changed" {
					t.Fatalf("changed content should remain separate: %#v", usages)
				}
				body.NativeID = "second-body"
				messages = append(messages, MessageRecord{NativeID: "second-command", Role: "user", Kind: "message", Text: "/alpha"}, body, snapshot("second-replay", "# Alpha\nInstructions"))
				usages = buildSkillUsages(messages, calls)
				if len(usages) != 3 || usages[2].EvidenceNativeID != "second-body" {
					t.Fatalf("distinct typed activations should stay separate: %#v", usages)
				}
			})
		}
	}
}

// Claude's invoked_skills attachments repeat the injected body verbatim,
// base-directory header included, under a bundled: path for built-in skills.
func TestSkillInvocationCompactionReplayWithHeader(t *testing.T) {
	text := "Base directory for this skill: /private/tmp/claude-501/bundled-skills/2.1.280/c409bc04/dataviz\n\n# Dataviz\nInstructions\n"
	body := injectedContext(MessageRecord{NativeID: "body", CallID: "invoke", Role: "user", Kind: "message"}, "skill", text)
	messages := []MessageRecord{
		skillTestCall("invoke", "Skill", map[string]any{"skill": "dataviz"}),
		skillTestResult("invoke", "Launching skill: dataviz", false),
		body,
		{NativeID: "compact", Role: "system", Kind: "metadata", Text: `{"type":"system","subtype":"compact_boundary"}`},
		{NativeID: "snapshot", Role: "system", Kind: "metadata", Text: jsonText(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "invoked_skills",
			"skills": []any{map[string]any{"name": "dataviz", "path": "bundled:dataviz", "content": text}}}})},
	}
	_, calls := buildToolLedger(messages, "test")
	usages := buildSkillUsages(messages, calls)
	if len(usages) != 1 || usages[0].Evidence != "explicit_invocation" || usages[0].BodyNativeID != "body" || usages[0].ContentBytes == nil || *usages[0].ContentBytes != int64(len("\n# Dataviz\nInstructions\n")) {
		t.Fatalf("compaction replay of an invoked skill should enrich the invocation: %#v", usages)
	}
}

func TestMCPToolAttribution(t *testing.T) {
	messages := []MessageRecord{
		skillTestCall("read", "read_mcp_resource", map[string]any{"server": "local", "uri": "file:///file"}),
		skillTestResult("read", "content", false),
		skillTestCall("list", "list_mcp_resources", map[string]any{}),
		skillTestResult("list", "resources", false),
		skillTestCall("browser", "mcp__browser__navigate", map[string]any{"url": "https://example.com"}),
		skillTestResult("browser", map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "navigation failed"}}}, false),
		skillTestCall("script", "exec", `const result = await tools.mcp__conductor__GetWorkspaceDiff({stat:true}); text(result);`),
		skillTestResult("script", `{"isError":true,"content":[{"type":"text","text":"MCP failed"}]}`, false),
		skillTestCall("many", "exec", `await tools.mcp__first__one({}); await tools.mcp__second__two({});`),
		skillTestResult("many", "done", false),
		skillTestCall("json-file", "Read", map[string]any{"file_path": "/repo/data.json"}),
		skillTestResult("json-file", `{"isError":true,"content":"file data, not a failed MCP result"}`, false),
	}
	_, calls := buildToolLedger(messages, "test")
	if calls[0].Category != "mcp" || calls[0].MCPServer != "local" || calls[1].Category != "mcp" || calls[1].MCPServer != "" {
		t.Fatalf("resource attribution = %#v", calls[:2])
	}
	if calls[2].Category != "web" || calls[2].MCPServer != "browser" || calls[2].Status != "error" {
		t.Fatalf("browser attribution = %#v", calls[2])
	}
	if calls[3].ToolName != "mcp__conductor__GetWorkspaceDiff" || calls[3].MCPServer != "conductor" || calls[3].Status != "error" {
		t.Fatalf("code mode attribution = %#v", calls[3])
	}
	if calls[4].ToolName != "exec" || calls[4].MCPServer != "" {
		t.Fatalf("multi-tool script should stay opaque = %#v", calls[4])
	}
	if calls[5].Status != "ok" {
		t.Fatalf("JSON file data should not be an MCP error = %#v", calls[5])
	}
}

func TestSkillUsageStoreAndBackfill(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-20T10:00:00Z","payload":{"id":"skill-tools","cwd":"/tmp/repo","model":"gpt-5.5"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:01Z","payload":{"type":"function_call","name":"exec_command","call_id":"files","arguments":"{\"cmd\":\"cat /skills/alpha/SKILL.md /skills/beta/SKILL.md\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:02Z","payload":{"type":"function_call_output","call_id":"files","output":"Process exited with code 0\nskill bodies"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:03Z","payload":{"type":"function_call","name":"read_mcp_resource","call_id":"resource","arguments":"{\"server\":\"local\",\"uri\":\"file:///file\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T10:00:04Z","payload":{"type":"function_call_output","call_id":"resource","output":"resource content"}}`,
	}
	writeFixture(t, root, "sessions/rollout-skills.jsonl", lines)
	ingestFixture(t, catalog, "codex", root)
	ctx := context.Background()
	if err := catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	load := func(name string) querytable.Schema {
		t.Helper()
		document, err := querySchemaDocument(name)
		if err != nil {
			t.Fatal(err)
		}
		schema, err := querytable.LoadSchema(document)
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}
	schema := load("skill_usages")
	result, err := skillUsageDataset.Rows(ctx, catalog.DB, querytable.Query{Limit: 10}, schema)
	if err != nil || result.Total != 2 || len(result.Rows) != 2 {
		t.Fatalf("skill rows = %#v, %v", result, err)
	}
	callID := result.Rows[0]["tool_call_id"]
	if callID == nil || callID != result.Rows[1]["tool_call_id"] || result.Rows[0]["evidence_message_id"] == nil {
		t.Fatalf("linked rows = %#v", result.Rows)
	}
	calls, err := toolCallDataset.Rows(ctx, catalog.DB, querytable.Query{Limit: 10}, load("tool_calls"))
	if err != nil || calls.Total != 2 {
		t.Fatalf("call rows = %#v, %v", calls, err)
	}
	for _, call := range calls.Rows {
		if call["id"] == callID && (call["skill_name"] != "alpha, beta" || call["skill_path"] != "/skills/alpha/SKILL.md, /skills/beta/SKILL.md") {
			t.Fatalf("call skill dimensions = %#v", call)
		}
		if call["tool_name"] == "read_mcp_resource" && (call["mcp_server"] != "local" || call["mcp_method"] != "read_mcp_resource") {
			t.Fatalf("MCP dimensions = %#v", call)
		}
	}
	metric, err := skillUsageDataset.Aggregate(ctx, catalog.DB, querytable.AggregationRequest{Aggregations: []querytable.Aggregation{{ID: "skills", Op: "count", GroupBy: []string{"skill_name", "evidence_type"}}}}, schema)
	if err != nil || len(metric.Metrics) != 1 || len(metric.Metrics[0].Buckets) != 2 {
		t.Fatalf("skill metrics = %#v, %v", metric, err)
	}
	page, _, err := catalog.SharedHTML(ctx, []string{firstString(result.Rows[0]["workspace_id"])})
	if err != nil || len(sharedPayload(t, page).Datasets["skill_usages"]) != 2 {
		t.Fatalf("shared skills: %v", err)
	}
	before, err := queryMaps(catalog.DB, "SELECT * FROM skill_usages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("UPDATE tool_ledger_state SET version='old'"); err != nil {
		t.Fatal(err)
	}
	if done, err := catalog.BackfillToolLedger(ctx, nil); err != nil || done != 1 {
		t.Fatalf("backfill = %d, %v", done, err)
	}
	after, err := queryMaps(catalog.DB, "SELECT * FROM skill_usages ORDER BY id")
	if err != nil || jsonText(before) != jsonText(after) {
		t.Fatalf("backfill changed skill evidence = %#v / %#v, %v", before, after, err)
	}
	if done, err := catalog.BackfillToolLedger(ctx, nil); err != nil || done != 0 {
		t.Fatalf("idempotent backfill = %d, %v", done, err)
	}
	transaction, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if err := replaceToolLedger(transaction, firstString(result.Rows[0]["workspace_id"]), firstString(result.Rows[0]["conversation_id"]), ConversationRecord{Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM skill_usages").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("stale skills = %d, %v", remaining, err)
	}
}

func TestClaudeSkillAttachmentsRetained(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	writeFixture(t, root, "project/skills.jsonl", claudeInstructionLines("/repo"))
	ingestFixture(t, catalog, "claude", root)
	rows, err := queryMaps(catalog.DB, "SELECT skill_name,skill_path,evidence_type,content_bytes,evidence_message_id FROM skill_usages")
	if err != nil || len(rows) != 1 || rows[0]["skill_name"] != "dataviz" || rows[0]["skill_path"] != "bundled:dataviz" || integer(rows[0]["content_bytes"]) != 10 {
		t.Fatalf("retained snapshot = %#v, %v", rows, err)
	}
	var body string
	if err := catalog.DB.QueryRow("SELECT text FROM messages WHERE id=?", rows[0]["evidence_message_id"]).Scan(&body); err != nil || !strings.Contains(body, "invoked_skills") {
		t.Fatalf("snapshot evidence = %q, %v", body, err)
	}
}

func TestMCPMethodQueriesMatchCube(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedToolCalls(t, catalog, 30)
	if _, err := catalog.DB.Exec("UPDATE tool_calls SET tool_name='mcp__browser__navigate',mcp_server='browser',tool_category='web' WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	document, err := querySchemaDocument("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	where := []querytable.WhereTerm{{Field: "mcp_method", Op: "=", Value: "navigate"}}
	rows, err := toolCallDataset.Rows(ctx, catalog.DB, querytable.Query{Where: where, Limit: 10}, schema)
	if err != nil || rows.Total != 1 || rows.Rows[0]["mcp_server"] != "browser" {
		t.Fatalf("method filter = %#v, %v", rows, err)
	}
	request := querytable.AggregationRequest{Where: where, Aggregations: []querytable.Aggregation{{ID: "methods", Op: "count", GroupBy: []string{"mcp_server", "mcp_method"}}}}
	cubed, err := toolCallDataset.Aggregate(ctx, catalog.DB, request, schema)
	if err != nil {
		t.Fatal(err)
	}
	plain := toolCallDataset
	plain.cube = nil
	direct, err := plain.Aggregate(ctx, catalog.DB, request, schema)
	if err != nil || !same(cubed, direct) || integer(cubed.Metrics[0].Buckets[0].Value) != 1 {
		t.Fatalf("method aggregation cube=%#v direct=%#v, %v", cubed, direct, err)
	}
	document, err = querySchemaDocument("tools")
	if err != nil {
		t.Fatal(err)
	}
	schema, err = querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := toolRollupDataset.Rows(ctx, catalog.DB, querytable.Query{Where: where, Limit: 10}, schema)
	if err != nil || summary.Total != 1 || summary.Rows[0]["mcp_method"] != "navigate" || integer(summary.Rows[0]["call_count"]) != 1 {
		t.Fatalf("method summary = %#v, %v", summary, err)
	}
}
