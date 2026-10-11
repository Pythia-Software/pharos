package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func TestEffortParsingPreservesTurnChangesAndUnknowns(t *testing.T) {
	parser := &jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}, provider: "codex"}
	events := []map[string]any{}
	for index, line := range []string{
		`{"type":"session_meta","payload":{"id":"effort","model":"gpt-6"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Unknown"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6","effort":"LOW"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Low"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6","reasoning_effort":"xhigh"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Extra high"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6","effort":null}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Reset"}}`,
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		event["_line"] = index + 1
		events = append(events, event)
	}
	record, ok, err := parser.codex("rollout.jsonl", events)
	if err != nil || !ok {
		t.Fatalf("parse: %v %v", ok, err)
	}
	efforts := []string{}
	for _, message := range record.Conversations[0].Messages {
		efforts = append(efforts, message.Effort)
	}
	if !reflect.DeepEqual(efforts, []string{"", "low", "xhigh", ""}) {
		t.Fatalf("efforts = %v", efforts)
	}
	if record.Conversations[0].Effort != "" {
		t.Fatal("reset effort should remain unknown")
	}
	// A numeric thinking budget, prose, or future unrecognized level is not coerced into a known default.
	if recordedEffort(map[string]any{"thinking": map[string]any{"budget_tokens": 32000}}) != "" {
		t.Fatal("budget is not effort")
	}
	if recordedEffort(map[string]any{"reasoning": map[string]any{"effort": " Future "}}) != "future" {
		t.Fatal("lost provider level")
	}
}

func TestEffortUsageAccountingQueriesAndExports(t *testing.T) {
	catalog, config := testCatalog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-effort.jsonl")
	fixture := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-09-20T12:00:00Z","payload":{"id":"effort-usage","model":"gpt-6-sol"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:01:00Z","payload":{"response_id":"r0","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6-sol","effort":"low"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:02:00Z","payload":{"type":"function_call","call_id":"call-low","name":"exec_command","arguments":"{\"cmd\":\"cat SKILL.md\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:02:01Z","payload":{"type":"function_call_output","call_id":"call-low","output":"a skill"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:03:00Z","payload":{"response_id":"r1","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6-sol","reasoning_effort":"high"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:04:00Z","payload":{"type":"function_call","call_id":"call-high","name":"exec_command","arguments":"{\"cmd\":\"git status\"}"}}`,
		`{"type":"response_item","timestamp":"2026-09-20T12:04:01Z","payload":{"type":"function_call_output","call_id":"call-high","output":"clean"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:05:00Z","payload":{"response_id":"r2","usage":{"input_tokens":200,"output_tokens":30,"total_tokens":230}}}`,
	}, "\n")
	writeSourceFile(t, path, fixture)
	adapter, err := MakeAdapter(SourceConfig{Name: "effort", Kind: "codex", Path: dir, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ingest := func() {
		t.Helper()
		if result := catalog.Ingest(adapter, nil); result.Error != nil {
			t.Fatal(result.Error)
		}
	}
	ingest()
	verify := func(high string) {
		t.Helper()
		rows, err := catalog.usageRows(time.Local)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		for _, row := range rows {
			got[firstString(row["effort"])] += integer(row["total_tokens"])
		}
		want := map[string]int64{"": 11, "low": 120, high: 230}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("usage %v want %v", got, want)
		}
		hours, err := catalog.usageRowsByPeriod(time.Local, true)
		if err != nil || len(hours) != 3 {
			t.Fatalf("hourly buckets %v: %v", hours, err)
		}
		schema, err := NewServer(config, catalog).queryTableSchema("usage")
		if err != nil {
			t.Fatal(err)
		}
		result, err := catalog.aggregateUsage(context.Background(), rows, querytable.AggregationRequest{
			Where:        []querytable.WhereTerm{{Field: "effort", Op: "=", Value: high}},
			Aggregations: []querytable.Aggregation{{ID: "effort-tokens", Op: "sum", Field: "total_tokens", GroupBy: []string{"hour", "effort"}}},
		}, schema)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Metrics) != 1 || len(result.Metrics[0].Buckets) != 1 || integer(result.Metrics[0].Buckets[0].Value) != 230 {
			t.Fatalf("filtered hourly metrics: %#v", result)
		}
		v2Schema, err := sqliteQuerySchema("usage")
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"", "shownRows"} {
			q := metricRequest(qt.AggSpec{ID: "effort-tokens", Expression: "SUM([total_tokens])", GroupBy: []string{"hour", "effort"}, Scope: scope})
			q.Where = []qt.WhereTerm{{Field: "effort", Op: "=", Value: high}}
			q.Limit = 1
			v2Result, err := NewServer(config, catalog).executeQueryV2(context.Background(), "usage", nil, v2Schema, nil, &q)
			if err != nil {
				t.Fatal(err)
			}
			if len(v2Result.Metrics.Metrics) != 1 || len(v2Result.Metrics.Metrics[0].Buckets) != 1 || integer(v2Result.Metrics.Metrics[0].Buckets[0].Value) != 230 {
				t.Fatalf("V2 filtered hourly metrics (%s): %#v", scope, v2Result)
			}
		}
		requests, err := queryMaps(catalog.DB, "SELECT effort,total_tokens FROM model_requests")
		if err != nil {
			t.Fatal(err)
		}
		requestTotals := map[string]int64{}
		for _, row := range requests {
			requestTotals[firstString(row["effort"])] += integer(row["total_tokens"])
		}
		if !reflect.DeepEqual(requestTotals, want) {
			t.Fatalf("requests: %v", requestTotals)
		}
	}
	verify("high")
	// No totals change on an idempotent ingest; metadata-only corrections rebuild the affected rows.
	ingest()
	verify("high")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(fixture, `"reasoning_effort":"high"`, `"reasoning_effort":"max"`)), 0600); err != nil {
		t.Fatal(err)
	}
	ingest()
	verify("max")
	calls, err := queryMaps(catalog.DB, "SELECT effort FROM tool_calls ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0]["effort"] != "low" || calls[1]["effort"] != "max" {
		t.Fatalf("calls: %v", calls)
	}
	if err := catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	rollup, err := queryMaps(catalog.DB, "SELECT effort,SUM(call_count) calls FROM tool_usage_daily GROUP BY effort")
	if err != nil || len(rollup) != 2 {
		t.Fatalf("rollup: %v %v", rollup, err)
	}
	works, err := catalog.searchRows(SearchOptions{Effort: "low"}, nil)
	if err != nil || len(works) != 1 {
		t.Fatalf("search: %v %v", works, err)
	}
	for effort, count := range map[string]int{"low": 1, "max": 1, "high": 0, " LOW ": 1, " Max ": 1, " High ": 0, "   ": 1} {
		rows, err := catalog.searchRows(SearchOptions{Effort: effort}, nil)
		if err != nil || len(rows) != count {
			t.Fatalf("work effort filter %q: %v %v", effort, rows, err)
		}
		result, err := catalog.searchConversations(map[string]any{"effort": effort})
		if err != nil || len(result["items"].([]map[string]any)) != count {
			t.Fatalf("MCP effort filter %q: %v %v", effort, result, err)
		}
	}
	id := firstString(works[0]["id"])
	library, err := catalog.cachedLibraryRows(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(library) != 1 || !strings.Contains(firstString(library[0]["efforts"]), "low") || !strings.Contains(firstString(library[0]["efforts"]), "max") {
		t.Fatalf("library: %v", library)
	}
	page, _, err := catalog.SharedHTML(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `"effort":"low"`) || !strings.Contains(string(page), `"effort":"max"`) {
		t.Fatal("share dropped effort")
	}
}

func TestEffortUsageMigrationPreservesLegacyAccounting(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestUsageFixture(t, catalog)
	// Recreate the previous layout using exactly its old key, then initialize again.
	if _, err := catalog.DB.Exec(`ALTER TABLE agent_session_usage RENAME TO usage_new;
 CREATE TABLE agent_session_usage AS SELECT agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,
 cache_read_input_tokens,cache_creation_input_tokens,cache_creation_5m_input_tokens,cache_creation_1h_input_tokens,
 output_tokens,reasoning_output_tokens,unclassified_tokens,total_tokens FROM usage_new;
 DROP TABLE usage_new;`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	var total, unknown int64
	if err := catalog.DB.QueryRow("SELECT SUM(total_tokens),SUM(effort='') FROM agent_session_usage").Scan(&total, &unknown); err != nil {
		t.Fatal(err)
	}
	if total != 3050 || unknown != 2 {
		t.Fatalf("migrated total=%d unknown=%d", total, unknown)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens)
 SELECT agent_session_id,usage_hour,model,'high',attribution,1 FROM agent_session_usage LIMIT 1`); err != nil {
		t.Fatal("effort is not part of the new primary key:", err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT SUM(total_tokens) FROM agent_session_usage").Scan(&total); err != nil || total != 3051 {
		t.Fatalf("second initialization: %d %v", total, err)
	}
}

func TestEffortFromClaudeAndCanonicalSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	writeSourceFile(t, path, strings.Join([]string{
		`{"type":"system","effortLevel":"medium","sessionId":"effort-claude"}`,
		`{"type":"assistant","uuid":"a1","message":{"id":"r1","model":"claude-opus-5-5","output_config":{"effort":"max"},"content":[{"type":"text","text":"Done"}],"usage":{"input_tokens":20,"output_tokens":10}}}`,
	}, "\n"))
	parser := &jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}, provider: "claude"}
	record, ok, err := parser.parse(path)
	if err != nil || !ok {
		t.Fatalf("claude parse: %v %v", ok, err)
	}
	conversation := record.Conversations[0]
	if conversation.Effort != "max" {
		t.Fatalf("claude effort: %q", conversation.Effort)
	}
	for _, message := range conversation.Messages {
		if message.Effort != "max" {
			t.Fatalf("claude message effort: %q", message.Effort)
		}
	}
	export := &canonicalAdapter{baseAdapter: baseAdapter{config: SourceConfig{Kind: "canonical", Account: "local"}}}
	row := map[string]any{"id": "canonical-effort", "conversations": []any{map[string]any{
		"id": "canonical", "model": "gpt-6", "effort": "high", "messages": []any{
			map[string]any{"id": "inherit", "role": "assistant", "text": "inherited"},
			map[string]any{"id": "override", "role": "assistant", "text": "low", "effort": "low"},
			map[string]any{"id": "reset", "role": "assistant", "text": "unknown", "effort": nil},
		}}}}
	record, err = export.workspace(row, "export.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, message := range record.Conversations[0].Messages {
		got = append(got, message.Effort)
	}
	if !reflect.DeepEqual(got, []string{"high", "low", ""}) {
		t.Fatalf("canonical effort: %v", got)
	}
}

func TestEffortFromConductorColumnsAndEnvelopes(t *testing.T) {
	catalog, _ := testCatalog(t)
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "conductor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY,model TEXT,codex_thinking_level TEXT,claude_effort_level TEXT,agent_type TEXT);
 CREATE TABLE session_messages(id TEXT PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,created_at TEXT);
 INSERT INTO sessions VALUES('effort-codex','gpt-6',' HIGH ','medium','codex');
 INSERT INTO sessions VALUES('effort-claude','claude-opus-5-5','high',' medium ','claude');
 INSERT INTO session_messages VALUES('m1','effort-codex','assistant','{"type":"assistant","message":{"id":"r-codex","usage":{"input_tokens":5,"output_tokens":10},"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call-codex","name":"Read","input":{}}]}}','2026-09-20T12:00:00Z');
 INSERT INTO session_messages VALUES('m2','effort-codex','assistant','{"type":"assistant","message":{"reasoning":{"effort":"max"},"content":[{"type":"text","text":"changed"}]}}','2026-09-20T12:01:00Z');
 INSERT INTO session_messages VALUES('m3','effort-codex','assistant','still max','2026-09-20T12:02:00Z');
 INSERT INTO session_messages VALUES('m4','effort-codex','assistant','{"type":"assistant","message":{"effort":null,"content":[{"type":"text","text":"reset"}]}}','2026-09-20T12:03:00Z');
 INSERT INTO session_messages VALUES('m5','effort-claude','assistant','{"type":"assistant","message":{"id":"r-claude","usage":{"input_tokens":5,"output_tokens":10},"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call-claude","name":"Read","input":{}}]}}','2026-09-20T12:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	adapter, err := MakeAdapter(SourceConfig{Name: "conductor-effort", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result := catalog.Ingest(adapter, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	rows, err := queryMaps(catalog.DB, "SELECT m.text,m.effort FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.provider='codex' AND m.kind='message' ORDER BY m.source_order")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0]["effort"] != "high" || rows[1]["effort"] != "max" || rows[2]["effort"] != "max" || rows[3]["effort"] != nil {
		t.Fatalf("conductor efforts: %v", rows)
	}
	conversations, err := queryMaps(catalog.DB, "SELECT provider,effort FROM conversations ORDER BY provider")
	if err != nil || len(conversations) != 2 || conversations[0]["effort"] != "medium" || conversations[1]["effort"] != nil {
		t.Fatalf("conductor conversation efforts: %v %v", conversations, err)
	}
	for _, table := range []string{"messages", "model_requests", "tool_calls"} {
		rows, err := queryMaps(catalog.DB, "SELECT c.provider,x.effort FROM "+table+" x JOIN conversations c ON c.id=x.conversation_id WHERE x.effort IN ('high','medium') ORDER BY c.provider")
		if err != nil || len(rows) < 2 || rows[0]["provider"] != "claude" || rows[len(rows)-1]["provider"] != "codex" {
			t.Fatalf("%s missing session effort: %v %v", table, rows, err)
		}
		for _, row := range rows {
			want := map[string]string{"claude": "medium", "codex": "high"}[firstString(row["provider"])]
			if row["effort"] != want {
				t.Fatalf("%s used the inactive agent's setting: %v", table, rows)
			}
		}
	}
	usage, err := queryMaps(catalog.DB, "SELECT effort,SUM(total_tokens) total FROM agent_session_usage GROUP BY effort ORDER BY effort")
	if err != nil || len(usage) != 2 || usage[0]["effort"] != "high" || usage[1]["effort"] != "medium" || integer(usage[0]["total"]) != 15 || integer(usage[1]["total"]) != 15 {
		t.Fatalf("conductor usage efforts: %v %v", usage, err)
	}
}

func TestConductorSessionEffortUsesOnlyActiveAgentSetting(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		session map[string]any
		want    string
	}{
		{"codex", map[string]any{"agent_type": "codex", "codex_thinking_level": " HIGH ", "claude_effort_level": "medium"}, "high"},
		{"claude", map[string]any{"agent_type": "claude", "codex_thinking_level": "high", "claude_effort_level": " Medium "}, "medium"},
		{"unset", map[string]any{"agent_type": "codex", "codex_thinking_level": nil, "claude_effort_level": "high", "effort": "max"}, ""},
		{"empty", map[string]any{"agent_type": "claude", "codex_thinking_level": "high", "claude_effort_level": " "}, ""},
		{"unknown agent", map[string]any{"agent_type": "other", "codex_thinking_level": "high", "claude_effort_level": "medium"}, ""},
		{"legacy", map[string]any{"agent_type": "codex", "reasoning_effort": " Low "}, "low"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if got := conductorSessionEffort(fixture.session); got != fixture.want {
				t.Fatalf("effort = %q, want %q", got, fixture.want)
			}
		})
	}
}

func TestEffortLookupsUseCoveringMessageIndex(t *testing.T) {
	catalog, _ := testCatalog(t)
	for name, query := range map[string]string{
		"library":             libraryDerivedSelect("w.id=?"),
		"work filter":         "SELECT w.id FROM workspaces w WHERE w.id IN (SELECT ce.workspace_id FROM conversations ce WHERE ce.effort=? UNION SELECT ce.workspace_id FROM messages me JOIN conversations ce ON ce.id=me.conversation_id WHERE me.effort=?)",
		"conversation filter": "SELECT c.id FROM conversations c WHERE c.effort=? OR EXISTS(SELECT 1 FROM messages me WHERE me.conversation_id=c.id AND me.effort=?)",
	} {
		t.Run(name, func(t *testing.T) {
			args := []any{"high", "high"}
			if name == "library" {
				args = []any{"workspace"}
			}
			plan, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if text := jsonText(plan); !strings.Contains(text, "COVERING INDEX messages_conversation_model_effort_idx") {
				t.Fatalf("effort lookup reads message rows: %s", text)
			}
		})
	}
}

func TestEffortMessageIndexCreatedAfterLegacyColumnMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(strings.ReplaceAll(schemaSQL(), "  effort TEXT,\n", "")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	var index string
	if err := catalog.DB.QueryRow("SELECT sql FROM sqlite_master WHERE name='messages_conversation_model_effort_idx'").Scan(&index); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "WHERE model IS NOT NULL OR effort IS NOT NULL") {
		t.Fatalf("missing partial effort index: %s", index)
	}
}

func TestEffortDoesNotAssignMixedAggregateToFinalSetting(t *testing.T) {
	messages := []MessageRecord{
		{NativeID: "low", Role: "assistant", Kind: "message", Text: "low", Effort: "low"},
		{NativeID: "high", Role: "assistant", Kind: "message", Text: "high", Effort: "high"},
		{NativeID: "result", Role: "system", Kind: "metadata", Text: `{"type":"result","usage":{"input_tokens":100,"output_tokens":20}}`, Effort: "high"},
	}
	sessions := agentSessionSummaries(messages)
	for bucket := range sessions[0].usage {
		if bucket.effort != "" {
			t.Fatalf("aggregate mislabeled %q", bucket.effort)
		}
	}
	for index := range messages {
		messages[index].Effort = "high"
	}
	sessions = agentSessionSummaries(messages)
	for bucket := range sessions[0].usage {
		if bucket.effort != "high" {
			t.Fatalf("uniform attempt lost effort: %q", bucket.effort)
		}
	}
	for name, want := range map[string]string{"Gemini 3.8 Flash (High)": "high", "Gemini Pro (Low)": "low", "Gemini Flash": "", "Claude (Thinking)": ""} {
		if got := antigravitySelectedEffort(name); got != want {
			t.Fatalf("selector %q = %q, want %q", name, got, want)
		}
	}
}

func TestEffortTL1InitialConfigurationRespectsTranscriptChanges(t *testing.T) {
	for _, fixture := range []struct {
		name, provider, content string
	}{
		{"codex-rollout.jsonl", "codex", strings.Join([]string{
			`{"type":"session_meta","payload":{"id":"effort-tl1","model":"gpt-6"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"initial"}}`,
			`{"type":"turn_context","payload":{"model":"gpt-6","effort":"low"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"changed"}}`,
			`{"type":"turn_context","payload":{"model":"gpt-6","effort":null}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"reset"}}`,
		}, "\n")},
		{"claude.jsonl", "claude", strings.Join([]string{
			`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"initial"}]}}`,
			`{"type":"assistant","uuid":"a2","message":{"role":"assistant","effort":"low","content":[{"type":"text","text":"changed"}]}}`,
			`{"type":"assistant","uuid":"a3","message":{"role":"assistant","effort":null,"content":[{"type":"text","text":"reset"}]}}`,
		}, "\n")},
		{"codex-exec.jsonl", "codex", strings.Join([]string{
			`{"type":"thread.started","thread_id":"effort-tl1"}`,
			`{"type":"item.completed","item":{"id":"a1","type":"agent_message","text":"initial"}}`,
			`{"type":"item.completed","item":{"id":"a2","type":"agent_message","text":"changed","effort":"low"}}`,
			`{"type":"item.completed","item":{"id":"a3","type":"agent_message","text":"reset","effort":null}}`,
		}, "\n")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSourceFile(t, filepath.Join(dir, "task", fixture.name), fixture.content)
			adapter := &tl1Adapter{baseAdapter: baseAdapter{config: SourceConfig{Account: "local"}}}
			conversation, ok, err := adapter.transcript(tl1Installation{ID: "test", Transcripts: dir}, "task", map[string]any{"id": "attempt", "executor": fixture.provider, "effort": "high"}, fixture.name)
			if err != nil || !ok {
				t.Fatalf("transcript: %v %v", ok, err)
			}
			efforts := []string{}
			for _, message := range conversation.Messages {
				efforts = append(efforts, message.Effort)
			}
			if !reflect.DeepEqual(efforts, []string{"high", "low", ""}) || conversation.Effort != "" {
				t.Fatalf("initial setting overwrote transcript changes: %v (final %q)", efforts, conversation.Effort)
			}
		})
	}
}
