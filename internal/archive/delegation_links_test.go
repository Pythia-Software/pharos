package archive

import (
	"context"
	"testing"
)

func TestDelegationCodexChildrenAcrossWorkspaces(t *testing.T) {
	c, config := testCatalog(t)
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"parent-work", "child-work", "other-work"} {
		run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, id, id, id)
	}
	run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('parent','parent-work','codex','local','parent-native')`)
	for _, cv := range []struct{ id, account, path string }{
		{"child", "local", "/root/v8_cost_audit"},
		{"unacknowledged", "local", "/root/missing_ack"},
		{"other-account", "other", "/root/v8_cost_audit"},
	} {
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,agent_path,model,effort) VALUES(?,'child-work','codex',?,?,'parent',1,?,'child-model','low')`, cv.id, cv.account, cv.id, cv.path)
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens) VALUES(?,'child-work',?,'main','subagent','codex','reported-reconciled',999)`, cv.id+"-session", cv.id)
	}
	run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,agent_path) VALUES('grandchild','other-work','codex','local','grandchild','child',2,'/root/v8_cost_audit/details')`)
	// A task path under another parent must not make the acknowledgement ambiguous.
	run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,agent_path) VALUES('unrelated','other-work','codex','local','unrelated',1,'/root/v8_cost_audit')`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens) VALUES('parent-session','parent-work','parent','main','root','codex','reported-reconciled',100)`)
	launches := []struct{ id, task, result string }{
		{"success", "v8_cost_audit", `{"content":"{\"task_name\":\"/root/v8_cost_audit\"}","tool":"spawn_agent"}`},
		{"repeat-success", "v8_cost_audit", `{"content":"{\"task_name\":\"/root/v8_cost_audit\"}"}`},
		{"failed-limit", "holdout_visual_b", `{"content":"collab spawn failed: agent thread limit reached"}`},
		{"failed-reuse", "v8_cost_audit", `{"content":"agent path ` + "`/root/v8_cost_audit`" + ` already exists"}`},
		{"unresolved", "no_transcript", `{"content":"{\"task_name\":\"/root/no_transcript\"}"}`},
	}
	for _, l := range launches {
		run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,'parent',?,'assistant','delegation',?,?,?)`, l.id, l.id, `{"input":{"task_name":"`+l.task+`","fork_turns":"none","message":"opaque assignment"},"tool":"spawn_agent"}`, l.id, l.id)
		run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,'parent',?,'tool','delegation_result',?,?,?)`, l.id+"-result", l.id+"-result", l.result, l.id, l.id+"-result")
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,delegation_message_id,kind,provider,usage_status) VALUES(?,'parent-work','parent',?,'parent-session',?,'subagent','codex','unavailable')`, l.id+"-session", l.id, l.id)
	}
	s, err := c.delegationSnapshot(context.Background(), "parent-work")
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(id string) string {
		if alias := s.Aliases[id]; alias != "" {
			return alias
		}
		return id
	}
	success := s.ByID[canonical("success-session")]
	if success == nil || success.ExternalChild == nil || success.ExternalChild.ConversationID != "child" || success.ExternalChild.WorkspaceID != "child-work" {
		t.Fatalf("Codex acknowledgement did not resolve: %+v", success)
	}
	if success.ConversationID != "child" || success.WorkspaceID != "child-work" {
		t.Fatal("references must retain the child's navigation identity")
	}
	if canonical("repeat-success-session") != success.ID {
		t.Fatal("repeated acknowledgements must not duplicate an outside transcript", s.Aliases)
	}
	if success.ParentID != "parent-session" || (success.MessageID != "repeat-success" && success.MessageID != "success") || success.Label != "v8_cost_audit" || success.Model != "child-model" || success.Effort != "low" {
		t.Fatalf("launch identity or child configuration lost: %+v", success)
	}
	for _, key := range []string{"tokens", "calls", "messages", "clock"} {
		if success.Self[key].Value != nil {
			t.Fatalf("unqueried child %s must be unknown: %+v", key, success.Self[key])
		}
	}
	if s.ByID["failed-limit-session"] != nil || s.ByID["failed-reuse-session"] != nil {
		t.Fatal("failed spawn attempts must not count as execution sessions")
	}
	if s.ByID["unresolved-session"].ExternalChild != nil {
		t.Fatal("missing transcript must not acquire a fake navigation target")
	}
	fallback := s.ByID["external-conversation:unacknowledged"]
	if fallback == nil || fallback.ExternalChild == nil || fallback.ParentID != "parent-session" || fallback.ParentEvidence != "conversation-parent" {
		t.Fatal("reverse conversation-parent evidence must survive a missing launch acknowledgement")
	}
	grandchild := s.ByID["external-conversation:grandchild"]
	if grandchild == nil || grandchild.ParentID != success.ID || grandchild.Depth != success.Depth+1 {
		t.Fatal("outside descendants must nest beneath their actual parent", grandchild)
	}
	if *s.Totals["tokens"].Value != 100 || s.Totals["tokens"].Quality != "partial" {
		t.Fatalf("outside usage must stay outside workspace accounting: %+v", s.Totals["tokens"])
	}
	status, body := delegationGET(t, NewServer(config, c), "/api/work/parent-work/delegation?conversation=parent&limit=100")
	if status != 200 || len(body["focus"].([]any)) != len(s.ByID["parent-session"].children)+1 {
		t.Fatal("external children must be present in navigation focus", status, body)
	}
}

func TestDelegationCodexLocalHandlesAndAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		ambiguous    bool
	}{
		{"task path", `{"content":"{\"task_name\":\"/root/review\"}"}`, false},
		{"agent id", `{"content":"{\"agent_id\":\"codex-native\"}"}`, false},
		{"ambiguous path", `{"content":"{\"task_name\":\"/root/review\"}"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCatalog(t)
			seedDelegation(t, c)
			run := func(q string, args ...any) {
				t.Helper()
				if _, err := c.DB.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			run(`UPDATE conversations SET provider='codex' WHERE id='root'`)
			run(`UPDATE conversations SET agent_path='/root/review' WHERE id='child'`)
			run(`UPDATE agent_sessions SET child_conversation_id=NULL,usage_status='unavailable' WHERE id='launch-alias'`)
			run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('launch-result','root','launch-result','tool','delegation_result',?,'launch-id','result')`, tc.result)
			if tc.ambiguous {
				run(`UPDATE conversations SET provider='codex',agent_path='/root/review' WHERE id='nested'`)
			}
			s, err := c.delegationSnapshot(context.Background(), "delegation-work")
			if err != nil {
				t.Fatal(err)
			}
			if tc.ambiguous {
				if s.Aliases["launch-alias"] != "" {
					t.Fatal("ambiguous paths must stay unmerged")
				}
			} else if s.Aliases["launch-alias"] != "child-session" || s.ByID["child-session"].MessageID != "launch" {
				t.Fatal("local Codex launches must reconcile with the actual transcript", s.Aliases)
			}
		})
	}
}

func TestDelegationClaudeTaskReportErrorsPreserveLaunch(t *testing.T) {
	for _, tc := range []struct{ name, result string }{
		{"report error", `{"details":{"agentId":"exact-child"},"content":[{"type":"text","text":"{\"error\":\"validation failed\"}"}]}`},
		{"report is_error", `{"details":{"agentId":"exact-child"},"content":"{\"is_error\":true}"}`},
		{"text handle and report error", `{"content":[{"type":"text","text":"agentId: exact-child"},{"type":"text","text":"{\"error\":true,\"is_error\":true}"}]}`},
		{"task error with spawned agent", `{"is_error":true,"details":{"agentId":"exact-child"},"content":"Validation failed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCatalog(t)
			seedDelegation(t, c)
			if _, err := c.DB.Exec(`UPDATE conversations SET provider='claude' WHERE id IN ('child','nested');
				UPDATE messages SET text=? WHERE id='nested-result'`, tc.result); err != nil {
				t.Fatal(err)
			}
			s, err := c.delegationSnapshot(context.Background(), "delegation-work")
			if err != nil {
				t.Fatal(err)
			}
			if s.Aliases["nested-alias"] != "nested-session" {
				t.Fatal("a subagent report must not discard its acknowledged launch", s.Aliases)
			}
			nested := s.ByID["nested-session"]
			if nested.ParentID != "child-session" || nested.ParentEvidence != "exact-delegation-handle" || nested.MessageID != "nested-launch" || nested.Label != "Check network" {
				t.Fatalf("successful launch lost its nesting or label: %+v", nested)
			}
		})
	}
}

func TestDelegationLaunchErrorsStaySeparateFromReportErrors(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		failed       bool
	}{
		{"top-level error", `{"error":"launch rejected"}`, true},
		{"top-level is_error", `{"is_error":true,"content":"launch rejected"}`, true},
		{"nested report error", `{"content":[{"type":"text","text":"{\"error\":\"validation failed\"}"}]}`, false},
		{"nested report is_error", `{"content":"{\"is_error\":true}"}`, false},
		{"acknowledged task path", `{"is_error":true,"content":"{\"task_name\":\"/root/review\"}"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readDelegationHandle(tc.result); got.failed != tc.failed {
				t.Fatalf("launch failure = %v, want %v", got.failed, tc.failed)
			}
		})
	}
}
