package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenMetricsHaveStableDigestOrder(t *testing.T) {
	record := WorkspaceRecord{Conversations: []ConversationRecord{{Messages: []MessageRecord{{
		RawText: `{"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25,"input_tokens_details":{"cached_tokens":10}}}`,
	}}}}}
	first := jsonText(reconciledTokenMetrics(record))
	for range 100 {
		if current := jsonText(reconciledTokenMetrics(record)); current != first {
			t.Fatalf("metric order changed: %s vs %s", first, current)
		}
	}
}

func TestAccountingEvidenceSurvivesParsingStorageAndAPI(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	path := filepath.Join(root, "rollout-usage.jsonl")
	events := []string{
		`{"type":"session_meta","payload":{"id":"usage-session"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:00:00Z","payload":{"response_id":"r1","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"thread_token_usage":{"total_tokens":120}}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-20T12:00:00Z","payload":{"response_id":"r2","usage":{"input_tokens":200,"output_tokens":30,"total_tokens":230},"thread_token_usage":{"total_tokens":350}}}`,
		`{"type":"turn.completed","timestamp":"2026-09-20T12:00:00Z","usage":{"input_tokens":300,"output_tokens":50}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(events, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "usage", Kind: "codex", Path: root, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	result := catalog.Ingest(adapter, nil)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	rows, err := catalog.searchRows(SearchOptions{}, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if integer(rows[0]["token_count"]) != 350 {
		t.Fatalf("lost usage: %v", rows[0]["token_count"])
	}
	detail, err := catalog.WorkDetail(firstString(rows[0]["id"]))
	if err != nil {
		t.Fatal(err)
	}
	messages := detail["conversations"].([]map[string]any)[0]["messages"].([]map[string]any)
	if len(messages) != 3 || !strings.Contains(firstString(messages[0]["raw_text"]), `"response_id":"r1"`) || !strings.Contains(firstString(messages[1]["raw_text"]), `"response_id":"r2"`) {
		t.Fatalf("lost source order/evidence: %v", messages)
	}
	// Full message usage must survive Conductor's display-block extraction.
	parsed := conductorEvents("event", "assistant", `{"type":"assistant","message":{"id":"r1","usage":{"input_tokens":5,"output_tokens":10,"cache_read_input_tokens":100},"content":[{"type":"text","text":"Reply"},{"type":"tool_use","id":"call","name":"Read","input":{}}]}}`, "", "fixture", map[string]bool{})
	if len(parsed) != 2 || parsed[0].RawText == "" || parsed[1].RawText != "" || conversationTokenCounts(parsed)["total_tokens"] != 115 {
		t.Fatalf("lost or duplicated block accounting: %#v", parsed)
	}
	openAI := normalizeTokenCounts(map[string]any{"input_tokens": 1000.0, "output_tokens": 200.0, "input_tokens_details": map[string]any{"cached_tokens": 800.0}, "total_tokens": 1200.0})
	if openAI["uncached_input_tokens"] != 200 || openAI["cache_read_input_tokens"] != 800 {
		t.Fatalf("OpenAI cache subset not retained: %#v", openAI)
	}
	claudeTTL := normalizeTokenCounts(map[string]any{"input_tokens": 10.0, "output_tokens": 5.0, "cache_creation": map[string]any{"ephemeral_5m_input_tokens": 100.0, "ephemeral_1h_input_tokens": 20.0}})
	if claudeTTL["cache_creation_5m_input_tokens"] != 100 || claudeTTL["cache_creation_1h_input_tokens"] != 20 || claudeTTL["total_tokens"] != 135 {
		t.Fatalf("Claude cache TTL dimensions not retained: %#v", claudeTTL)
	}
}

func TestTokenUsageFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/token-usage-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string
		Total  float64
		Events []json.RawMessage
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Fixture events are protocol accounting envelopes, which sources
			// store as metadata messages.
			messages := []MessageRecord{}
			for _, event := range c.Events {
				messages = append(messages, MessageRecord{Role: "system", Kind: "metadata", Text: string(event)})
			}
			got := conversationTokenCounts(messages)
			if got["total_tokens"] != c.Total {
				t.Fatalf("got %#v; want total %v", got, c.Total)
			}
		})
	}
}

func TestNestedSubagentsArePersistedWithCostDimensions(t *testing.T) {
	catalog, _ := testCatalog(t)
	nestedCheckpoints := []MessageRecord{
		{NativeID: "outer-call", Kind: "delegation", CallID: "outer", Text: `{}`},
		{NativeID: "outer-request", ParentNativeID: "outer", Text: `{"type":"assistant","parent_tool_use_id":"outer","message":{"id":"outer-request","usage":{"total_tokens":120}}}`},
		{NativeID: "inner-call", Kind: "delegation", CallID: "inner", ParentNativeID: "outer", Text: `{}`},
		{NativeID: "inner-request", ParentNativeID: "inner", Text: `{"type":"assistant","parent_tool_use_id":"inner","message":{"id":"inner-request","usage":{"total_tokens":60}}}`},
		{NativeID: "inner-total", ParentNativeID: "inner", Text: `{"type":"system","subtype":"task_notification","tool_use_id":"inner","usage":{"total_tokens":80}}`},
		{NativeID: "outer-total", ParentNativeID: "outer", Text: `{"type":"system","subtype":"task_notification","tool_use_id":"outer","usage":{"total_tokens":250}}`},
		{NativeID: "result", Text: `{"type":"result","usage":{"total_tokens":300}}`},
	}
	if got := conversationTokenCounts(nestedCheckpoints)["total_tokens"]; got != 300 {
		t.Fatalf("nested subagent checkpoints counted %v tokens; want 300", got)
	}
	messages := []MessageRecord{
		{NativeID: "delegate-outer", Role: "assistant", Kind: "delegation", CallID: "outer", Text: `{"tool":"spawn_agent"}`, Selected: true},
		{NativeID: "outer-usage", Role: "assistant", Kind: "metadata", ParentNativeID: "outer", Text: `{"type":"assistant","parent_tool_use_id":"outer","message":{"id":"outer-request","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":10}}}`, Selected: true},
		{NativeID: "delegate-inner", Role: "assistant", Kind: "delegation", ParentNativeID: "outer", CallID: "inner", Text: `{"tool":"spawn_agent"}`, Selected: true},
		{NativeID: "inner-usage", Role: "assistant", Kind: "metadata", ParentNativeID: "inner", Text: `{"type":"assistant","parent_tool_use_id":"inner","message":{"id":"inner-request","usage":{"input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":5}}}`, Selected: true},
		{NativeID: "result", Role: "system", Kind: "result", Text: `{"type":"result","usage":{"total_tokens":165}}`, Selected: true},
	}
	record := WorkspaceRecord{SourceID: "nested", SourceKind: "canonical", Account: "local", Title: "Nested agents", Conversations: []ConversationRecord{{NativeID: "conversation", Provider: "claude", Model: "opus", Account: "local", Messages: messages}}}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(catalog.DB, `SELECT native_id,depth,uncached_input_tokens,cache_read_input_tokens,
		cache_creation_input_tokens,output_tokens,total_tokens FROM agent_sessions ORDER BY depth,native_id`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("agent sessions not materialized: %#v", rows)
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[firstString(row["native_id"])] = row
	}
	if integer(byID["outer"]["depth"]) != 1 || integer(byID["outer"]["uncached_input_tokens"]) != 10 ||
		integer(byID["outer"]["cache_read_input_tokens"]) != 90 || integer(byID["outer"]["total_tokens"]) != 110 {
		t.Fatalf("outer subagent usage wrong: %#v", byID["outer"])
	}
	if integer(byID["inner"]["depth"]) != 2 || integer(byID["inner"]["uncached_input_tokens"]) != 20 ||
		integer(byID["inner"]["cache_creation_input_tokens"]) != 30 || integer(byID["inner"]["total_tokens"]) != 55 {
		t.Fatalf("nested subagent usage wrong: %#v", byID["inner"])
	}
	var total int64
	if err := catalog.DB.QueryRow("SELECT SUM(total_tokens) FROM agent_sessions").Scan(&total); err != nil || total != 165 {
		t.Fatalf("agent session totals = %d, %v; want 165", total, err)
	}
	health := catalog.Health()["token_usage"].([]map[string]any)
	if len(health) != 1 || integer(health[0]["tokens"]) != 165 || integer(health[0]["uncached_input_tokens"]) != 30 ||
		integer(health[0]["cache_read_input_tokens"]) != 90 || integer(health[0]["cache_creation_input_tokens"]) != 30 ||
		integer(health[0]["output_tokens"]) != 15 {
		t.Fatalf("cost dimensions missing from health usage: %#v", health)
	}
	var linked int64
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM agent_session_messages").Scan(&linked); err != nil || linked != int64(len(messages)) {
		t.Fatalf("session message links = %d, %v; want %d", linked, err, len(messages))
	}
	workspaceID := stableID("workspace", "canonical", "local", "nested")
	detail, err := catalog.WorkDetail(workspaceID)
	if err != nil || len(detail["agent_sessions"].([]map[string]any)) != 3 {
		t.Fatalf("agent sessions missing from work detail: %#v, %v", detail, err)
	}
	if _, err := catalog.DB.Exec("DELETE FROM agent_sessions"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&linked); err != nil || linked != 3 {
		t.Fatalf("agent session backfill = %d, %v; want 3", linked, err)
	}
}

func TestUsageBucketsWithMissingAndExplicitModelCoalesce(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "usage-model", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{{
		NativeID: "usage-model", Provider: "codex", Model: "gpt-test", Account: "local",
		Messages: []MessageRecord{
			{NativeID: "first", Role: "system", Kind: "metadata", CreatedAt: "2026-09-24T14:00:00Z", Text: `{"type":"token_usage_record","usage":{"input_tokens":10,"output_tokens":2}}`, Selected: true},
			{NativeID: "second", Role: "system", Kind: "metadata", Model: "gpt-test", CreatedAt: "2026-09-24T14:01:00Z", Text: `{"type":"token_usage_record","usage":{"input_tokens":20,"output_tokens":3}}`, Selected: true},
		},
	}}}
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
	var rows, total int
	if err := catalog.DB.QueryRow("SELECT COUNT(*),COALESCE(SUM(total_tokens),0) FROM agent_session_usage").Scan(&rows, &total); err != nil || rows != 1 || total != 35 {
		t.Fatalf("usage rows = %d, total = %d, err=%v", rows, total, err)
	}
}

// A forked subagent's counters start at its parent's usage, which the
// parent's rollout already counts; only the subagent's own requests are its,
// at the effort they ran with.
func TestCodexForkCountsOnlyItsOwnUsage(t *testing.T) {
	c, _ := testCatalog(t)
	root := t.TempDir()
	count := func(total, last int) string {
		return fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-09-01T00:01:00Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d},"last_token_usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}}`+"\n", total, total, last, last)
	}
	writeSourceFile(t, filepath.Join(root, "2026", "09", "01", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl"),
		codexLines(probeID1, 1)+count(1000, 1000))
	writeSourceFile(t, filepath.Join(root, "2026", "09", "01", "rollout-2026-09-01T00-05-00-"+probeID2+".jsonl"),
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-09-01T00:05:00Z","payload":{"id":%q,"forked_from_id":%q,"subagent_history_start_ordinal":3,"source":{"subagent":{"thread_spawn":{"parent_thread_id":%q,"depth":1}}}}}`+"\n", probeID2, probeID1, probeID1)+
			`{"type":"turn_context","timestamp":"2026-09-01T00:05:00Z","payload":{"model":"gpt-test","effort":"high"}}`+"\n"+count(1100, 100)+count(1300, 200))
	ingestSource(t, c, SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true})
	var metric, sessions float64
	if err := c.DB.QueryRow(`SELECT SUM(value) FROM metrics WHERE name='total_tokens' AND conversation_id IS NULL`).Scan(&metric); err != nil {
		t.Fatal(err)
	}
	if err := c.DB.QueryRow(`SELECT SUM(total_tokens) FROM agent_session_usage`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if metric != 1300 || sessions != 1300 {
		t.Fatalf("total tokens: metrics %v, sessions %v; want 1300", metric, sessions)
	}
	var effort string
	if err := c.DB.QueryRow(`SELECT a.effort FROM agent_sessions a JOIN conversations c ON c.id=a.conversation_id WHERE c.native_id=?`, probeID2).Scan(&effort); err != nil || effort != "high" {
		t.Fatalf("continuation effort = %q, %v", effort, err)
	}
}

// Only protocol metadata marks a continuation: a message quoting one does not.
func TestQuotedContinuationMarkerKeepsCounterPrefix(t *testing.T) {
	count := func(total, last int) MessageRecord {
		return MessageRecord{Role: "system", Kind: "metadata", Text: fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"total_tokens":%d}}}}`, total, last)}
	}
	quoted := MessageRecord{Role: "user", Kind: "message", Text: `{"type":"session_meta","payload":{"forked_from_id":"parent"}}`}
	if got := conversationTokenCounts([]MessageRecord{quoted, count(1100, 100), count(1300, 200)}); got["total_tokens"] != 1300 {
		t.Fatalf("got %v; want 1300", got)
	}
}
