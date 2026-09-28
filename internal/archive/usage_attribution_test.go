package archive

import (
	"context"
	"testing"
	"time"
)

func TestClaudeGroupUsageAndDelegation(t *testing.T) {
	for _, claim := range []struct {
		name, costState string
		want            int64
	}{
		{"side call", `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":540,"output_tokens":60},"haiku":{"input_tokens":9,"output_tokens":1}}}`, 610},
		{"claim below children", `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":200,"output_tokens":50}}}`, 600},
	} {
		t.Run(claim.name, func(t *testing.T) {
			catalog, _ := testCatalog(t)
			rootID := "claude-root"
			root := ConversationRecord{NativeID: rootID, Provider: "claude", Model: "opus", Account: "local", StartedAt: "2026-09-20T10:00:00Z", Messages: []MessageRecord{
				{NativeID: "root-request", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"root-request","usage":{"input_tokens":90,"output_tokens":10}}}`},
				{NativeID: "delegate", Kind: "delegation", CallID: "tool-1", Text: `{}`},
				{NativeID: "delegation-result", Kind: "delegation_result", CallID: "tool-1", Text: `{"details":{"agentId":"child1"}}`},
				{NativeID: "claim", Kind: "metadata", Text: claim.costState},
			}}
			child := func(id, request string, input, output int) ConversationRecord {
				text := jsonText(map[string]any{"type": "assistant", "message": map[string]any{"id": request, "usage": map[string]any{"input_tokens": input, "output_tokens": output}}})
				return ConversationRecord{NativeID: rootID + ":subagent:agent-" + id, ParentNativeID: rootID, Provider: "claude", Model: "opus", Account: "local", AgentDepth: 1, Messages: []MessageRecord{{NativeID: request, Kind: "metadata", Model: "opus", Text: text}}}
			}
			record := WorkspaceRecord{SourceID: rootID, SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root, child("child1", "child-request-1", 180, 20), child("child2", "child-request-2", 270, 30)}}
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
			var sessions, ledger, metric, remainder int64
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions").Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage").Scan(&ledger); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow("SELECT value FROM metrics WHERE name='total_tokens' AND conversation_id IS NULL").Scan(&metric); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage WHERE attribution='session-remainder'").Scan(&remainder); err != nil {
				t.Fatal(err)
			}
			if sessions != claim.want || ledger != claim.want || metric != claim.want || remainder != claim.want-600 {
				t.Fatalf("sessions=%d ledger=%d metric=%d remainder=%d, want %d", sessions, ledger, metric, remainder, claim.want)
			}
			var status, linked string
			if err := catalog.DB.QueryRow("SELECT usage_status,child_conversation_id FROM agent_sessions WHERE native_id='tool-1'").Scan(&status, &linked); err != nil {
				t.Fatal(err)
			}
			if status != "in-child-conversation" || linked == "" {
				t.Fatalf("delegation status=%q child=%q", status, linked)
			}
			workspaceID := stableID("workspace", "claude", "local", rootID)
			detail, err := catalog.WorkDetail(workspaceID)
			if err != nil || len(detail["agent_sessions"].([]map[string]any)) != 3 {
				t.Fatalf("visible sessions=%v, err=%v", detail["agent_sessions"], err)
			}
			for _, conversation := range detail["conversations"].([]map[string]any) {
				if firstString(conversation["native_id"]) == rootID && integer(conversation["token_usage"].(tokenCounts)["total_tokens"]) != claim.want-500 {
					t.Fatalf("root detail usage = %v", conversation["token_usage"])
				}
			}
			if _, err := catalog.DB.Exec("UPDATE agent_sessions SET total_tokens=999999 WHERE native_id='main' AND depth=0"); err != nil {
				t.Fatal(err)
			}
			repaired, err := catalog.RepairUsageAttribution(context.Background(), nil)
			if err != nil || repaired != 1 {
				t.Fatalf("repair = %d, %v", repaired, err)
			}
			if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions").Scan(&sessions); err != nil || sessions != claim.want {
				t.Fatalf("repaired sessions = %d, %v; want %d", sessions, err, claim.want)
			}
			if repaired, err = catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 0 {
				t.Fatalf("repair did not resume cleanly: %d, %v", repaired, err)
			}
		})
	}
}

func TestCostStateWithoutRequestsAndConductorAnonymousUsage(t *testing.T) {
	noRequests := []MessageRecord{{NativeID: "claim", Text: `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":40,"output_tokens":10}}}`}}
	if got := conversationTokenCounts(noRequests)["total_tokens"]; got != 50 {
		t.Fatalf("cost-state without requests = %v, want 50", got)
	}
	blocks := []MessageRecord{
		{NativeID: "first", Kind: "metadata", Text: `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":10}}}`},
		{NativeID: "second", Kind: "metadata", Text: `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":20}}}`},
	}
	if got := conversationTokenCounts(blocks)["total_tokens"]; got != 120 {
		t.Fatalf("anonymous session usage = %v, want 120", got)
	}
	requests, _ := buildToolLedger(blocks, "opus")
	if len(requests) != 1 || requests[0].Counts["total_tokens"] != 120 {
		t.Fatalf("anonymous request ledger = %#v", requests)
	}
	separate := append(append([]MessageRecord{}, blocks...),
		MessageRecord{NativeID: "result", Kind: "tool_result", Text: `{}`},
		MessageRecord{NativeID: "third", Kind: "metadata", Text: `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":10}}}`})
	if got := conversationTokenCounts(separate)["total_tokens"]; got != 230 {
		t.Fatalf("separate anonymous requests = %v, want 230", got)
	}
}

func TestUsageAttributionHealth(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := WorkspaceRecord{SourceID: "health", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{{NativeID: "health", Provider: "claude", Model: "opus", Account: "local", StartedAt: time.Now().UTC().Format(time.RFC3339), Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":100,"output_tokens":10}}}`}}}}}
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
	if _, err := catalog.DB.Exec("UPDATE agent_sessions SET total_tokens=200000 WHERE native_id='main'"); err != nil {
		t.Fatal(err)
	}
	aggregate := WorkspaceRecord{SourceID: "aggregate", SourceKind: "conductor", Account: "local", Conversations: []ConversationRecord{{NativeID: "aggregate", Provider: "codex", Model: "gpt-test", Account: "local", StartedAt: time.Now().UTC().Format(time.RFC3339), Messages: []MessageRecord{{NativeID: "total", Kind: "metadata", Text: `{"type":"result","usage":{"total_tokens":500}}`}}}}}
	tx, err = catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, aggregate, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := catalog.UsageAttributionHealth(context.Background())
	if err != nil || len(rows) != 2 || integer(rows[0]["mismatched"]) != 1 || firstString(rows[1]["status"]) != "session totals only" {
		t.Fatalf("health rows=%#v err=%v", rows, err)
	}
}
