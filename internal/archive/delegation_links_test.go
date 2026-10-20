package archive

import (
	"context"
	"fmt"
	"testing"
)

func TestDelegationIncomingAncestorsRemainContext(t *testing.T) {
	for _, tc := range []struct {
		name             string
		depth            int
		inferred         bool
		inferredAncestor bool
		sessionParents   bool
		fallback         bool
	}{
		{name: "separate child workspace", depth: 1},
		{name: "nested child without derived session", depth: 2, fallback: true},
		{name: "inferred command parent", depth: 2, inferred: true},
		{name: "command-launched ancestors", depth: 3, inferredAncestor: true},
		{name: "recorded session parents", depth: 2, sessionParents: true},
		{name: "entire command-launch chain", depth: 3, inferred: true, inferredAncestor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, config := testCatalog(t)
			run := func(q string, args ...any) {
				t.Helper()
				if _, err := c.DB.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i <= tc.depth; i++ {
				id := fmt.Sprintf("level-%d", i)
				run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, id, id, id)
				parent := ""
				if i > 0 && !tc.sessionParents && !(tc.inferred && i == tc.depth) && !(tc.inferredAncestor && i < tc.depth) {
					parent = fmt.Sprintf("level-%d", i-1)
				}
				run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,model,effort) VALUES(?,?,'codex','local',?,?,?,'own-model','medium')`, id, id, id, nilIfEmpty(parent), i)
				if !tc.fallback || i < tc.depth {
					run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens,started_at,ended_at) VALUES(?,?,?,'main','subagent','codex','reported-reconciled',100,'2026-01-01T00:00:00Z','2026-01-01T00:01:00Z')`, id+"-session", id, id)
				}
			}
			childID := fmt.Sprintf("level-%d", tc.depth)
			for i := 1; i <= tc.depth; i++ {
				if tc.sessionParents {
					run(`UPDATE agent_sessions SET parent_id=? WHERE id=?`, fmt.Sprintf("level-%d-session", i-1), fmt.Sprintf("level-%d-session", i))
				}
				if !(tc.inferred && i == tc.depth) && !(tc.inferredAncestor && i < tc.depth) {
					continue
				}
				parent, child := fmt.Sprintf("level-%d", i-1), fmt.Sprintf("level-%d", i)
				run(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status) VALUES(?,?,?,0,'codex','tool_call','exec_command','shell','success')`, child+"-launch", parent, parent)
				run(`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES(?,?,?,'codex')`, child, parent, child+"-launch")
			}
			// A sibling is not part of the selected workspace's ancestor context.
			run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('sibling','level-0','codex','local','sibling','level-0',1)`)
			s, err := c.delegationSnapshot(context.Background(), childID)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Nodes) != tc.depth+1 || len(s.Roots) != 1 {
				t.Fatalf("expected only child and its ancestor chain: %+v", s.Nodes)
			}
			outline := "1"
			for i, n := range s.Nodes {
				if i > 0 {
					outline += ".1"
					if n.ParentID != s.Nodes[i-1].ID {
						t.Fatalf("level %d lost its parent: %+v", i, n)
					}
				}
				if n.Depth != i || n.Outline != outline || n.ConversationID != fmt.Sprintf("level-%d", i) {
					t.Fatalf("incorrect nested identity at level %d: %+v", i, n)
				}
				if i < tc.depth {
					if !n.ContextOnly || n.Navigation == nil || n.Navigation.WorkspaceID != n.ConversationID || n.Model != "own-model" || n.ChildCount != 1 {
						t.Fatalf("ancestor lost navigation or hierarchy: %+v", n)
					}
					for _, key := range []string{"tokens", "calls", "clock", "messages", "requests", "files"} {
						if n.Self[key].Value != nil {
							t.Fatalf("unqueried ancestor %s must remain unknown: %+v", key, n.Self[key])
						}
					}
				} else if n.ContextOnly || n.Navigation != nil {
					t.Fatalf("local child became navigation-only: %+v", n)
				}
			}
			if !tc.fallback {
				if m := s.Totals["tokens"]; m.Value == nil || *m.Value != 100 || m.Quality != "reported" || m.Known != 1 || m.Total != 1 {
					t.Fatalf("ancestor context changed workspace usage: %+v", m)
				}
				if *s.Totals["clock"].Value != 60000 || *s.Totals["children"].Value != 0 {
					t.Fatal("ancestor context changed workspace clock or child count", s.Totals)
				}
			}
			status, body := delegationGET(t, testServer(t, config, c), "/api/work/"+childID+"/delegation?conversation="+childID+"&only_matching=1")
			if status != 200 || len(body["focus"].([]any)) != 2 || body["session_count"] != float64(1) {
				t.Fatal("direct links must include selected child and parent context", status, body)
			}
			if !tc.fallback {
				m := body["totals"].(map[string]any)["tokens"].(map[string]any)
				if m["value"] != float64(100) || m["quality"] != "reported" {
					t.Fatal("matching-only totals included ancestor context", m)
				}
			}
		})
	}
}

func TestDelegationIncomingExactOwnership(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		declared, omitParent bool
	}{
		{name: "acknowledged child"},
		{name: "declared child", declared: true},
		{name: "declared child without transcript parent", declared: true, omitParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, config := testCatalog(t)
			seedDelegation(t, c)
			run := func(q string, args ...any) {
				t.Helper()
				if _, err := c.DB.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('outside','antigravity','local','outside','Outside','2026-01-01'),('leaf-work','codex','local','leaf','Leaf','2026-01-01')`)
			// The transcript flattened this parent to root; the retained launch
			// in child identifies the actual owning execution.
			run(`UPDATE conversations SET workspace_id='outside',agent_depth=2 WHERE id='nested'`)
			run(`UPDATE agent_sessions SET workspace_id='outside' WHERE id='nested-session'`)
			if tc.declared {
				run(`UPDATE agent_sessions SET child_conversation_id='nested' WHERE id='nested-alias'`)
				run(`DELETE FROM messages WHERE id='nested-result'`)
			}
			if tc.omitParent {
				run(`UPDATE conversations SET parent_id=NULL WHERE id='nested'`)
			}
			run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('leaf','leaf-work','codex','local','leaf','nested',3)`)
			run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens) VALUES('leaf-session','leaf-work','leaf','main','subagent','codex','reported-reconciled',50)`)
			for _, selected := range []struct {
				workspace, conversation, session string
				depth, accounted                 int
				tokens                           float64
			}{
				{"outside", "nested", "nested-session", 2, 2, 300},
				{"leaf-work", "leaf", "leaf-session", 3, 1, 50},
			} {
				s, err := c.delegationSnapshot(context.Background(), selected.workspace)
				if err != nil {
					t.Fatal(err)
				}
				n := s.ByID[selected.session]
				if n.Depth != selected.depth || n.ParentID == "" {
					t.Fatalf("lost recovered incoming chain: %+v", n)
				}
				parent := s.ByID[n.ParentID]
				if selected.conversation == "leaf" {
					parent = s.ByID[parent.ParentID]
				}
				if parent == nil || parent.ConversationID != "child" || !parent.ContextOnly {
					t.Fatalf("exact launch must override flattened transcript parent: %+v", parent)
				}
				for _, ancestor := range s.Nodes {
					if ancestor.ContextOnly && (ancestor.Navigation == nil || ancestor.Self["tokens"].Value != nil) {
						t.Fatal("incoming context must remain navigable with unknown self usage", ancestor)
					}
					if ancestor.ConversationID == "nested" && (ancestor.ParentEvidence != "exact-delegation-handle" || ancestor.MessageID != "nested-launch") {
						t.Fatal("incoming edge lost its exact launch evidence", ancestor)
					}
				}
				if n.ExternalParent == nil || n.ExternalParent.ConversationID != s.ByID[n.ParentID].ConversationID {
					t.Fatal("parent navigation must follow recovered ownership", n.ExternalParent)
				}
				if m := s.Totals["tokens"]; m.Value == nil || *m.Value != selected.tokens || m.Known != 1 || m.Total != selected.accounted {
					// Outside also retains the unqueried leaf reference.
					t.Fatalf("ancestor context changed workspace usage: %+v", m)
				}
				status, body := delegationGET(t, testServer(t, config, c), "/api/work/"+selected.workspace+"/delegation?conversation="+selected.conversation)
				if status != 200 || body["session_count"] != float64(selected.accounted) {
					t.Fatal("incoming context changed execution count", status, body)
				}
				focus := body["focus"].([]any)
				found := false
				for _, entry := range focus {
					row := entry.(map[string]any)
					found = found || row["id"] == selected.session && row["outline"] == n.Outline
				}
				if !found {
					t.Fatal("reader focus lost the nested session outline", focus)
				}
			}
		})
	}
}

func TestDelegationIncomingAcknowledgementsRemainUnambiguous(t *testing.T) {
	for _, conflict := range []string{"another child", "conflicting results"} {
		t.Run(conflict, func(t *testing.T) {
			c, _ := testCatalog(t)
			seedDelegation(t, c)
			run := func(q string) {
				t.Helper()
				if _, err := c.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('outside','antigravity','local','outside','Outside','2026-01-01')`)
			run(`UPDATE conversations SET workspace_id='outside',agent_depth=2 WHERE id='nested'`)
			run(`UPDATE agent_sessions SET workspace_id='outside' WHERE id='nested-session'`)
			if conflict == "another child" {
				run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('ambiguous','outside','antigravity','local','another-root:subagent:agent-exact-child','root',1)`)
			} else {
				run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('conflict','child','conflict','tool','delegation_result','{"details":{"agentId":"other-child"}}','nested-call','conflict')`)
			}
			s, err := c.delegationSnapshot(context.Background(), "outside")
			if err != nil {
				t.Fatal(err)
			}
			n := s.ByID["nested-session"]
			if parent := s.ByID[n.ParentID]; parent == nil || parent.ConversationID != "root" || n.ParentEvidence != "conversation-parent" {
				t.Fatalf("ambiguous acknowledgement must retain the transcript fallback: %+v", n)
			}
		})
	}
}

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
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/parent-work/delegation?conversation=parent&limit=100")
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

func TestDelegationIncomingAliasedParent(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		acknowledged, independent bool
		parent                    string
	}{
		{name: "declared alias", parent: "child"},
		{name: "acknowledged alias", acknowledged: true, parent: "child"},
		{name: "independent execution", independent: true, parent: "root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, config := testCatalog(t)
			seedDelegation(t, c)
			run := func(q string) {
				t.Helper()
				if _, err := c.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('outside','antigravity','local','outside','Outside','2026-01-01')`)
			run(`UPDATE conversations SET workspace_id='outside',parent_id='child',agent_depth=2 WHERE id='nested'`)
			run(`UPDATE agent_sessions SET workspace_id='outside' WHERE id='nested-session'`)
			run(`UPDATE agent_sessions SET conversation_id='root',parent_id='launch-alias',child_conversation_id='nested' WHERE id='nested-alias'`)
			if tc.acknowledged {
				run(`UPDATE conversations SET native_id='root:subagent:agent-parent-child' WHERE id='child'`)
				run(`UPDATE agent_sessions SET child_conversation_id=NULL WHERE id='launch-alias'`)
				run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('parent-result','root','parent-result','tool','delegation_result','{"details":{"agentId":"parent-child"}}','launch-id','parent-result')`)
			}
			if tc.independent {
				run(`UPDATE agent_sessions SET total_tokens=10,usage_status='reported-reconciled' WHERE id='launch-alias'`)
			}
			outgoing, err := c.delegationSnapshot(context.Background(), "delegation-work")
			if err != nil {
				t.Fatal(err)
			}
			launch := outgoing.ByID["nested-alias"]
			if launch == nil || outgoing.ByID[launch.ParentID] == nil || outgoing.ByID[launch.ParentID].ConversationID != tc.parent {
				t.Fatal("outgoing hierarchy lost parent execution", launch)
			}
			incoming, err := c.delegationSnapshot(context.Background(), "outside")
			if err != nil {
				t.Fatal(err)
			}
			n := incoming.ByID["nested-session"]
			parent := incoming.ByID[n.ParentID]
			if parent == nil || parent.ConversationID != tc.parent || n.Depth != parent.Depth+1 || tc.parent == "child" && n.Depth != 2 {
				t.Fatalf("incoming parent must agree with outgoing execution: parent=%+v node=%+v", parent, n)
			}
			if n.ExternalParent == nil || n.ExternalParent.ConversationID != tc.parent || n.ExternalParent.MessageID != "nested-launch" {
				t.Fatal("parent navigation must use the canonical execution", n.ExternalParent)
			}
			status, body := delegationGET(t, testServer(t, config, c), "/api/work/outside/delegation?conversation=nested")
			if status != 200 {
				t.Fatal(status, body)
			}
		})
	}
}

func TestDelegationIncomingLargeAncestorWorkspace(t *testing.T) {
	for _, records := range []string{"conversations", "sessions", "outside siblings"} {
		t.Run(records, func(t *testing.T) {
			c, config := testCatalog(t)
			run := func(q string) {
				t.Helper()
				if _, err := c.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('parent-work','codex','local','parent','Parent','2026-01-01'),('child-work','codex','local','child','Child','2026-01-01'),('sibling-work','codex','local','sibling','Sibling','2026-01-01')`)
			run(`INSERT INTO conversations(id,workspace_id,native_id,provider,account) VALUES('parent','parent-work','parent','codex','local')`)
			run(`INSERT INTO conversations(id,workspace_id,native_id,provider,account,parent_id,agent_depth) VALUES('child','child-work','child','codex','local','parent',1)`)
			switch records {
			case "conversations":
				run(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<10000) INSERT INTO conversations(id,workspace_id,native_id,provider,account) SELECT 'unrelated-'||n,'parent-work','unrelated-'||n,'codex','local' FROM seq`)
			case "sessions":
				run(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<10001) INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) SELECT 'unrelated-'||n,'parent-work','parent','unrelated-'||n,'subagent','codex' FROM seq`)
			case "outside siblings":
				run(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<10000) INSERT INTO conversations(id,workspace_id,native_id,provider,account,parent_id,agent_depth) SELECT 'unrelated-'||n,'sibling-work','unrelated-'||n,'codex','local','parent',1 FROM seq`)
			}
			s, err := c.delegationSnapshot(context.Background(), "child-work")
			if err != nil {
				t.Fatalf("unrelated ancestor records blocked child hierarchy: %v", err)
			}
			if len(s.Nodes) != 2 || s.Nodes[0].ConversationID != "parent" || s.Nodes[1].Depth != 1 {
				t.Fatal("expected only the child and its parent context", s.Nodes)
			}
			status, body := delegationGET(t, testServer(t, config, c), "/api/work/child-work/delegation?conversation=child")
			if status != 200 || body["session_count"] != float64(1) {
				t.Fatal(status, body)
			}
		})
	}
}
