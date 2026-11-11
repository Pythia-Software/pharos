package archive

import (
	"context"
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

func TestObservedEffortDoesNotAllocateUnknownAggregate(t *testing.T) {
	c, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "observed-effort", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{{
		NativeID: "observed-effort", Provider: "codex", Model: "gpt-test", Account: "local",
		Messages: []MessageRecord{
			{NativeID: "startup", Role: "user", Kind: "message", Text: "before recorded configuration", Selected: true},
			{NativeID: "request", Role: "system", Kind: "metadata", Effort: "high", Text: `{"type":"token_usage_record","response_id":"r1","usage":{"input_tokens":100,"output_tokens":20}}`, Selected: true},
			{NativeID: "result", Role: "system", Kind: "metadata", Effort: "high", Text: `{"type":"result","usage":{"input_tokens":300,"output_tokens":60}}`, Selected: true},
		},
	}}}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var summary string
	if err := c.DB.QueryRow(`SELECT summary_json FROM agent_session_effort_summaries`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if got := readSessionEffortSummary(summary); got.KnownRequests != 1 || got.UnknownRequests != 0 || len(got.Levels) != 1 || got.Levels[0] != "high" {
		t.Fatalf("observed effort: %s", summary)
	}
	rows, err := queryMaps(c.DB, `SELECT effort,SUM(total_tokens) tokens FROM agent_session_usage GROUP BY effort`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, row := range rows {
		got[firstString(row["effort"])] = integer(row["tokens"])
	}
	if len(got) != 2 || got["high"] != 120 || got[""] != 240 {
		t.Fatalf("observed summary relabeled uncertain aggregate: %v", got)
	}
}

func TestAgentSessionTimeBounds(t *testing.T) {
	if decodeSessionBounds(`{"source":"attempt-envelope","started_at":"2026-09-20T10:00:00Z","ended_at":123}`) != nil {
		t.Fatal("malformed retained bounds must not supply partial proof")
	}
	start, end := "2026-09-20T10:00:00.000Z", "2026-09-20T10:20:00.000Z"
	request := MessageRecord{NativeID: "request", Kind: "metadata", RawText: `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`}
	for _, tc := range []struct {
		name, start, end, timestamp, wantStart, wantEnd, source string
		depth                                                   int
		coverage                                                string
	}{
		{name: "completed", start: start, end: end, wantStart: start, wantEnd: end, source: "attempt-envelope"},
		{name: "ongoing", start: start, wantStart: start, source: "attempt-envelope"},
		{name: "invalid end", start: start, end: "unknown", wantStart: start, source: "attempt-envelope"},
		{name: "reversed end", start: start, end: "2026-09-19T10:00:00Z", wantStart: start, source: "attempt-envelope"},
		{name: "invalid start", start: "unknown", end: end, source: "unavailable"},
		{name: "explicit message", start: start, end: end, timestamp: "2026-09-20T10:05:00.000Z", wantStart: "2026-09-20T10:05:00.000Z", wantEnd: "2026-09-20T10:05:00.000Z", source: "message-span"},
		{name: "invalid message", start: start, end: end, timestamp: "unknown", wantStart: start, wantEnd: end, source: "attempt-envelope"},
		{name: "child conversation", start: start, end: end, depth: 1, source: "unavailable"},
		{name: "partial transcript", start: start, end: end, coverage: "partial", source: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := request
			m.CreatedAt = tc.timestamp
			messages := []MessageRecord{m, {NativeID: "launch", CallID: "child", Kind: "delegation"}, {NativeID: "child-message", ParentNativeID: "child", Kind: "message"}}
			c := ConversationRecord{AgentDepth: tc.depth, Coverage: tc.coverage, SessionBounds: attemptSessionBounds(tc.start, tc.end), Messages: messages}
			before := jsonText(messages)
			summaries := agentSessionSummaries(messages)
			for i := range summaries {
				applySessionEnvelope(&summaries[i], c)
			}
			root := summaries[0]
			if root.startedAt != tc.wantStart || root.endedAt != tc.wantEnd || root.timeBoundsSource != tc.source {
				t.Fatalf("bounds = %q / %q (%s)", root.startedAt, root.endedAt, root.timeBoundsSource)
			}
			if child := summaries[1]; child.startedAt != "" || child.endedAt != "" || child.timeBoundsSource != "unavailable" {
				t.Fatalf("child inherited root timing: %+v", child)
			}
			if root.counts["total_tokens"] != 12 || jsonText(messages) != before {
				t.Fatal("timing policy changed requests or token facts")
			}
		})
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
	t.Run("structured Agent result fallback", func(t *testing.T) {
		launch := MessageRecord{NativeID: "launch", Kind: "delegation", CallID: "child", Model: "claude-opus-5", Text: `{"tool":"Agent","input":{"model":"sonnet"}}`}
		result := MessageRecord{NativeID: "handback", Kind: "delegation_result", CallID: "child", Model: "claude-opus-5", CreatedAt: "2026-08-05T13:17:21Z", Text: `{"details":{"status":"completed","totalToolUseCount":0,"totalTokens":110,"usage":{"input_tokens":5,"cache_read_input_tokens":90,"cache_creation_input_tokens":5,"output_tokens":10}},"content":"Finished"}`}
		request := MessageRecord{NativeID: "request", ParentNativeID: "child", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":200,"output_tokens":20}}}`}
		for _, check := range []struct {
			name     string
			messages []MessageRecord
			want     float64
		}{
			{"result only", []MessageRecord{launch, result}, 110},
			{"duplicate result", []MessageRecord{launch, result, result}, 110},
			{"request already retained", []MessageRecord{launch, request, result}, 220},
			{"task total already retained", []MessageRecord{launch, result, {Text: `{"type":"system","subtype":"task_notification","tool_use_id":"child","usage":{"total_tokens":300}}`}}, 300},
			{"SDK aggregate includes child", []MessageRecord{launch, result, {Text: `{"type":"result","usage":{"total_tokens":150}}`}}, 150},
			{"ordinary tool output", []MessageRecord{launch, {Kind: "tool_result", CallID: "child", Text: result.Text}}, 0},
			{"unproved child identity", []MessageRecord{result}, 0},
			{"multi-step result is only a last request", []MessageRecord{launch, {Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, `"totalToolUseCount":0`, `"totalToolUseCount":12`, 1)}}, 0},
			{"incomplete result", []MessageRecord{launch, {Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, `"completed"`, `"async_launched"`, 1)}}, 0},
			{"inconsistent result counters", []MessageRecord{launch, {Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, `"totalTokens":110`, `"totalTokens":999`, 1)}}, 0},
			{"quoted usage", []MessageRecord{launch, {Kind: "delegation_result", CallID: "child", Text: `{"content":"{\"details\":{\"usage\":{\"total_tokens\":999}}}"}`}}, 0},
			{"parent launch usage stays parent", []MessageRecord{{Kind: "delegation", CallID: "child", Text: `{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":16751,"cache_creation_input_tokens":3137,"output_tokens":3}}}`}}, 19893},
		} {
			t.Run(check.name, func(t *testing.T) {
				if got := conversationTokenCounts(check.messages)["total_tokens"]; got != check.want {
					t.Fatalf("tokens = %v, want %v", got, check.want)
				}
			})
		}
		summaries := agentSessionSummaries([]MessageRecord{launch, result})
		if len(summaries) != 2 || summaries[0].counts["total_tokens"] != 0 || summaries[1].counts["total_tokens"] != 110 {
			t.Fatalf("result belongs to child: %#v", summaries)
		}
		for bucket, counts := range summaries[1].usage {
			if bucket.model != "sonnet" || bucket.hour != "2026-08-05T13:00:00Z" || counts["uncached_input_tokens"] != 5 || counts["cache_read_input_tokens"] != 90 || counts["cache_creation_input_tokens"] != 5 || counts["output_tokens"] != 10 {
				t.Fatalf("result dimensions = %#v %#v", bucket, counts)
			}
		}
		// A resume inherits the original agent, not a later parent model.
		original := launch
		original.CallID, original.NativeID = "original", "original-launch"
		originalResult := MessageRecord{Kind: "delegation_result", CallID: "original", Text: `{"details":{"agentId":"agent-a","status":"async_launched"}}`}
		resume := launch
		resume.Text = `{"tool":"Agent","input":{"resume":"agent-a"}}`
		for _, summary := range agentSessionSummaries([]MessageRecord{original, originalResult, resume, result}) {
			if summary.nativeID == "child" {
				for bucket := range summary.usage {
					if bucket.model != "sonnet" {
						t.Fatalf("resume model = %q, want original sonnet", bucket.model)
					}
				}
			}
		}
		claim := MessageRecord{Kind: "result", Model: "<synthetic>", Text: `{"type":"result","modelUsage":{"claude-opus-5":{"inputTokens":100,"outputTokens":10},"claude-sonnet-5":{"inputTokens":5,"cacheReadInputTokens":90,"cacheCreationInputTokens":5,"outputTokens":10}}}`}
		for _, summary := range agentSessionSummaries([]MessageRecord{launch, result, claim}) {
			if summary.nativeID == "main" {
				for bucket, counts := range summary.usage {
					if counts["total_tokens"] > 0 && bucket.model != "claude-opus-5" {
						t.Fatalf("SDK remainder lost its model after child alias subtraction: %q", bucket.model)
					}
				}
			}
		}
	})
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
	t.Run("aggregate source models survive synthetic replies", func(t *testing.T) {
		claim := MessageRecord{Kind: "result", Model: "<synthetic>", Text: `{"type":"result","modelUsage":{"claude-opus-5":{"inputTokens":100,"outputTokens":10},"claude-haiku-5-5":{"inputTokens":50,"outputTokens":5}}}`}
		request := MessageRecord{Kind: "metadata", Model: "claude-opus-5", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":60,"output_tokens":5}}}`}
		for _, check := range []struct {
			name     string
			messages []MessageRecord
			want     map[string]float64
		}{
			{"only aggregate", []MessageRecord{claim}, map[string]float64{"claude-opus-5": 110, "claude-haiku-5-5": 55}},
			{"retained request and aggregate", []MessageRecord{request, claim}, map[string]float64{"claude-opus-5": 110, "claude-haiku-5-5": 55}},
			{"cumulative aggregates", []MessageRecord{claim, {Kind: "result", Model: "<synthetic>", Text: `{"type":"result","modelUsage":{"claude-opus-5":{"inputTokens":200,"outputTokens":20},"claude-haiku-5-5":{"inputTokens":100,"outputTokens":10}}}`}}, map[string]float64{"claude-opus-5": 220, "claude-haiku-5-5": 110}},
			{"unmatched request model stays unpriced", []MessageRecord{{Kind: "metadata", Text: request.Text}, claim}, map[string]float64{"": 65, "<synthetic>": 100}},
			{"one model with cache TTL", []MessageRecord{{Kind: "result", Model: "<synthetic>", Text: `{"type":"result","usage":{"input_tokens":5,"output_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":20}},"modelUsage":{"claude-opus-5":{"inputTokens":5,"outputTokens":10,"cacheCreationInputTokens":20}}}`}}, map[string]float64{"claude-opus-5": 35}},
			{"model identity without redundant counters", []MessageRecord{{Kind: "result", Model: "<synthetic>", Text: `{"type":"result","usage":{"input_tokens":5,"output_tokens":10},"modelUsage":{"claude-opus-5":{"contextWindow":200000}}}`}}, map[string]float64{"claude-opus-5": 15}},
			{"context windows do not allocate aggregate", []MessageRecord{request, {Kind: "result", Model: "<synthetic>", Text: `{"type":"result","usage":{"input_tokens":100,"output_tokens":10},"modelUsage":{"claude-opus-5":{"contextWindow":200000},"claude-haiku-5-5":{"contextWindow":200000}}}`}}, map[string]float64{"claude-opus-5": 65, "<synthetic>": 45}},
			{"context windows do not inherit real model", []MessageRecord{request, {Kind: "result", Model: "claude-opus-5", Text: `{"type":"result","usage":{"input_tokens":100,"output_tokens":10},"modelUsage":{"claude-opus-5":{"contextWindow":200000},"claude-haiku-5-5":{"contextWindow":200000}}}`}}, map[string]float64{"claude-opus-5": 65, "Unknown model": 45}},
			{"several context windows cannot allocate aggregate usage", []MessageRecord{{Kind: "result", Model: "<synthetic>", Text: `{"type":"result","usage":{"input_tokens":100,"output_tokens":10},"modelUsage":{"claude-opus-5":{"contextWindow":1000000},"claude-haiku-5-5":{"contextWindow":200000}}}`}}, map[string]float64{"<synthetic>": 110}},
			{"known request leaves ambiguous multi-model remainder", []MessageRecord{request, {Kind: "result", Model: "<synthetic>", Text: `{"type":"result","usage":{"input_tokens":100,"output_tokens":10},"modelUsage":{"claude-opus-5":{"contextWindow":1000000},"claude-haiku-5-5":{"contextWindow":200000}}}`}}, map[string]float64{"claude-opus-5": 65, "<synthetic>": 45}},
		} {
			t.Run(check.name, func(t *testing.T) {
				got := map[string]float64{}
				for _, summary := range agentSessionSummaries(check.messages) {
					for bucket, counts := range summary.usage {
						got[bucket.model] += counts["total_tokens"]
					}
				}
				if jsonText(got) != jsonText(check.want) {
					t.Fatalf("model usage = %v, want %v", got, check.want)
				}
			})
		}
	})
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
	marker := `{"type":"session_meta","payload":{"forked_from_id":"parent"}}`
	// Conductor stores pasted JSON as metadata under the sender's role.
	for _, quoted := range []MessageRecord{{Role: "user", Kind: "message", Text: marker}, {Role: "user", Kind: "metadata", Text: marker}} {
		if got := conversationTokenCounts([]MessageRecord{quoted, count(1100, 100), count(1300, 200)}); got["total_tokens"] != 1300 {
			t.Fatalf("%s %s: got %v; want 1300", quoted.Role, quoted.Kind, got)
		}
	}
}

func agentFooterResult(total int64) string {
	footer := fmt.Sprintf("agentId: agent-one (use SendMessage with to: 'agent-one', summary: '<5-10 word recap>' to continue this agent)\n<usage>subagent_tokens: %d\ntool_uses: 83\nduration_ms: 835048</usage>", total)
	return jsonText(map[string]any{"content": []any{map[string]any{"type": "text", "text": "Report"}, map[string]any{"type": "text", "text": footer}}, "is_error": false})
}

func TestNestedAgentTokenEvidence(t *testing.T) {
	launch := MessageRecord{NativeID: "launch", Kind: "delegation", CallID: "child", Text: `{"tool":"Agent","input":{}}`}
	result := MessageRecord{NativeID: "result", Kind: "delegation_result", CallID: "child", Text: agentFooterResult(116837)}
	for _, check := range []struct {
		name           string
		launch, result MessageRecord
		want           int
	}{
		{"harness footer", launch, result, 1},
		{"ordinary tool", MessageRecord{Kind: "delegation", CallID: "child", Text: `{"tool":"Read"}`}, result, 0},
		{"not delegated", MessageRecord{}, result, 0},
		{"ordinary output", launch, MessageRecord{Kind: "tool_result", CallID: "child", Text: result.Text}, 0},
		{"quoted footer", launch, MessageRecord{Kind: "delegation_result", CallID: "child", Text: jsonText(map[string]any{"content": result.Text})}, 0},
		{"prose prefix", launch, MessageRecord{Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, "agentId:", "Here is an example: agentId:", 1)}, 0},
		{"wrong resume", MessageRecord{Kind: "delegation", CallID: "child", Text: `{"tool":"Agent","input":{"resume":"other"}}`}, result, 0},
		{"identity mismatch", launch, MessageRecord{Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, `"is_error":false`, `"details":{"agentId":"other"},"is_error":false`, 1)}, 0},
		{"duplicate footer", launch, MessageRecord{Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, `"Report"`, `"<usage>subagent_tokens: 9"`, 1)}, 0},
		{"zero", launch, MessageRecord{Kind: "delegation_result", CallID: "child", Text: agentFooterResult(0)}, 0},
		{"overflow", launch, MessageRecord{Kind: "delegation_result", CallID: "child", Text: strings.Replace(result.Text, "116837", "99999999999999999999999", 1)}, 0},
	} {
		t.Run(check.name, func(t *testing.T) {
			messages := []MessageRecord{check.launch, check.result}
			if evidence := conversationTokenEvidence(messages); len(evidence) != check.want {
				t.Fatalf("evidence = %+v, want %d", evidence, check.want)
			}
			// The typed producer accepts the launch from the same footer and
			// nothing else; a Codex record also reads tool_result rows.
			for _, provider := range []string{"claude", "codex"} {
				footers := 0
				for _, claim := range conversationExecutionEvidence(ConversationRecord{Provider: provider, NativeID: "root", Messages: messages}) {
					if claim.Kind == evidenceLaunchOutcome && claim.Rule == "harness-usage-footer/v1" && claim.Detail["handle"] == "agent-one" {
						footers++
					}
				}
				if footers != check.want {
					t.Fatalf("%s typed footer outcomes = %d, want %d", provider, footers, check.want)
				}
			}
			if got := conversationTokenCounts(messages)["total_tokens"]; got != 0 {
				t.Fatalf("untyped observation entered accounting: %v", got)
			}
		})
	}
	c, _ := testCatalog(t)
	if err := c.replacePricing(testPricing(), "test"); err != nil {
		t.Fatal(err)
	}
	messages := []MessageRecord{launch, result, {NativeID: "aggregate", Kind: "result", CreatedAt: "2026-07-02T00:00:00Z", Text: `{"type":"result","usage":{"input_tokens":199990,"output_tokens":10}}`}}
	record := WorkspaceRecord{SourceID: "scoped", SourceKind: "canonical", Account: "local", Title: "Scoped usage", Conversations: []ConversationRecord{{NativeID: "scoped", Provider: "codex", Account: "local", Model: "gpt-5.5", Messages: messages}}}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	workspace := stableID("workspace", "canonical", "local", "scoped")
	s, err := c.delegationSnapshot(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	var child, root *delegationNode
	for _, n := range s.Nodes {
		if n.TokenAttribution != nil && n.TokenAttribution.Observed > 0 {
			child = n
		} else {
			root = n
		}
	}
	if child == nil || root == nil {
		t.Fatalf("nodes=%+v", s.Nodes)
	}
	if child.Self["tokens"].Value == nil || *child.Self["tokens"].Value != 116837 || child.Self["tokens"].Quality != "reported-minimum" || child.Self["cost"].Value != nil {
		t.Fatalf("child=%+v", child)
	}
	if root.Self["tokens"].Value != nil || root.TokenAttribution.Unallocated != 200000 || root.Self["cost"].Value != nil {
		t.Fatalf("aggregate ascribed to parent self: %+v", root)
	}
	if *s.Totals["tokens"].Value != 200000 || *root.All["tokens"].Value != 200000 || root.All["cost"].Value == nil {
		t.Fatalf("overlapping evidence inflated totals: %+v", s.Totals)
	}
	// The footer also creates the child when the transcript's typed evidence
	// predates it (an older catalog): the stored-row reading accepts it too.
	if _, err = c.DB.Exec(`DELETE FROM execution_claims; UPDATE execution_evidence_state SET version='execution-evidence-v3'`); err != nil {
		t.Fatal(err)
	}
	if s, err = c.delegationSnapshot(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if n := s.ByID[child.ID]; len(s.Nodes) != 2 || n == nil || n.Self["tokens"].Value == nil || *n.Self["tokens"].Value != 116837 || *s.Totals["tokens"].Value != 200000 {
		t.Fatalf("stored-row reading lost the footer's child: nodes=%d child=%+v", len(s.Nodes), n)
	}
	var ledger int64
	if err = c.DB.QueryRow(`SELECT SUM(total_tokens) FROM agent_session_usage`).Scan(&ledger); err != nil || ledger != 200000 {
		t.Fatalf("ledger=%d %v", ledger, err)
	}
	// Membership refresh preserves the separate evidence, and repair also
	// handles non-Claude workspaces. A later source correction removes it.
	var conversation string
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE workspace_id=?`, workspace).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	tx, err = c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = refreshAgentSessionMembership(tx, workspace, conversation, record.Conversations[0]); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`DELETE FROM usage_attribution_state`); err != nil {
		t.Fatal(err)
	}
	if repaired, err := c.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 1 {
		t.Fatalf("repair=%d %v", repaired, err)
	}
	var observations int
	if err = c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_token_evidence`).Scan(&observations); err != nil || observations != 1 {
		t.Fatalf("evidence=%d %v", observations, err)
	}
	if _, err = c.DB.Exec(`DELETE FROM agent_sessions WHERE native_id='child'`); err != nil {
		t.Fatal(err)
	}
	tx, err = c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = refreshAgentSessionMembership(tx, workspace, conversation, record.Conversations[0]); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_token_evidence`).Scan(&observations); err != nil || observations != 1 {
		t.Fatalf("newly materialized session lost evidence: %d %v", observations, err)
	}
	tx, err = c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	record.Conversations[0].Messages[1].Text = `{"content":[]}`
	if err = replaceAgentSessions(tx, workspace, conversation, record.Conversations[0]); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_token_evidence`).Scan(&observations); err != nil || observations != 0 {
		t.Fatalf("stale evidence=%d %v", observations, err)
	}
}
