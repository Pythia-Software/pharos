package archive

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
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
			// A sibling in the parent's workspace is family context: opening the
			// child keeps it, without its accounting.
			run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('sibling','level-0','codex','local','sibling','level-0',1)`)
			s, err := c.delegationSnapshot(context.Background(), childID)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Nodes) != tc.depth+2 || len(s.Roots) != 1 {
				t.Fatalf("expected the child, its ancestor chain and the sibling: %+v", s.Nodes)
			}
			chain := []*delegationNode{}
			child := s.ByID[childID+"-session"]
			if tc.fallback {
				child = s.ByID["conversation:"+childID]
			}
			for n := child; n != nil; n = s.ByID[n.ParentID] {
				chain = append([]*delegationNode{n}, chain...)
			}
			if sibling := s.ByID["conversation:sibling"]; sibling == nil || !sibling.ContextOnly || sibling.ParentID != "level-0-session" || sibling.Ancestry.Depth == nil || *sibling.Ancestry.Depth != 1 || sibling.Self["tokens"].Value != nil {
				t.Fatalf("sibling context lost: %+v", sibling)
			}
			for i, n := range chain {
				if i > 0 && (n.ParentID != s.Nodes[i-1].ID || n.AncestorIDs[0] != n.ParentID || len(n.AncestorIDs) != i) {
					t.Fatalf("level %d lost its parent: %+v", i, n)
				}
				if n.Depth != i || n.Ancestry.Depth == nil || *n.Ancestry.Depth != i || n.ConversationID != fmt.Sprintf("level-%d", i) {
					t.Fatalf("incorrect nested identity at level %d: %+v", i, n)
				}
				if i < tc.depth {
					if !n.ContextOnly || n.Navigation == nil || n.Navigation.WorkspaceID != n.ConversationID || n.Model != "own-model" || n.ChildCount != 1+min(1, max(0, 1-i)) {
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
				// The leaf archived elsewhere is family context, not outside's own session.
				{"outside", "nested", "nested-session", 2, 1, 300},
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
					found = found || row["id"] == selected.session && row["parent_id"] == n.ParentID && row["depth"] == float64(n.Depth)
				}
				if !found {
					t.Fatal("reader focus lost the nested session's parent", focus)
				}
			}
		})
	}
}

func TestDelegationIncomingAcknowledgementsRemainUnambiguous(t *testing.T) {
	for _, conflict := range []string{"another provider's suffix", "conflicting results"} {
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
			if conflict == "another provider's suffix" {
				// A suffix in another provider's namespace is not a candidate (F8).
				run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('ambiguous','outside','antigravity','local','another-root:subagent:agent-exact-child','root',1)`)
			} else {
				run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('conflict','child','conflict','tool','delegation_result','{"details":{"agentId":"other-child"}}','nested-call','conflict')`)
			}
			s, err := c.delegationSnapshot(context.Background(), "outside")
			if err != nil {
				t.Fatal(err)
			}
			n := s.ByID["nested-session"]
			want, evidence := "root", "conversation-parent"
			if conflict == "another provider's suffix" {
				want, evidence = "child", "exact-delegation-handle"
			}
			if parent := s.ByID[n.ParentID]; parent == nil || parent.ConversationID != want || n.ParentEvidence != evidence {
				t.Fatalf("acknowledgement resolved outside its namespace or despite a conflict: %+v", n)
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
	run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('child-reply','child','child-reply','assistant','message','Audit done.','2026-01-02T00:00:00Z','child-reply')`)
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
	// Children archived elsewhere without a launch here are family context:
	// navigable, nested under their resolved parent, without workspace usage.
	fallback := s.ByID["unacknowledged-session"]
	if fallback == nil || !fallback.ContextOnly || fallback.Navigation == nil || fallback.ParentID != "parent-session" || fallback.ParentEvidence != "conversation-parent" {
		t.Fatal("reverse conversation-parent evidence must survive a missing launch acknowledgement", fallback)
	}
	grandchild := s.ByID["conversation:grandchild"]
	if grandchild == nil || grandchild.ParentID != success.ID || grandchild.Depth != success.Depth+1 || grandchild.Navigation == nil {
		t.Fatal("outside descendants must nest beneath their actual parent", grandchild)
	}
	if *s.Totals["tokens"].Value != 100 || s.Totals["tokens"].Quality != "partial" {
		t.Fatalf("outside usage must stay outside workspace accounting: %+v", s.Totals["tokens"])
	}
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/parent-work/delegation?conversation=parent&limit=100")
	if status != 200 || len(body["focus"].([]any)) != len(s.ByID["parent-session"].children)+1 {
		t.Fatal("external children must be present in navigation focus", status, body)
	}
	if s.Coverage.Saved != 0 || s.Coverage.Unqueried == 0 {
		t.Fatal("without a saved summary the child stays unqueried", s.Coverage)
	}

	// Maintenance saves what the child's own workspace reads of it. The
	// parent's page then shows that, outside the parent's own totals.
	reread := func() *delegationNode {
		t.Helper()
		if s, err = c.delegationSnapshot(t.Context(), "parent-work"); err != nil {
			t.Fatal(err)
		}
		return s.ByID[canonical("success-session")]
	}
	if _, err := c.refreshDelegationSummaries(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	success = reread()
	if success.Saved == nil || success.Saved.Pending || !slices.Equal(success.Saved.Workspaces, []string{"child-work"}) {
		t.Fatalf("the child must show its own workspace's saved summary: %+v", success.Saved)
	}
	if m := success.Self["tokens"]; m.Value == nil || *m.Value != 999 {
		t.Fatalf("saved tokens: %+v", m)
	}
	if m := success.Self["messages"]; m.Value == nil || *m.Value != 1 || success.Start == nil {
		t.Fatalf("saved activity: %+v start %v", m, success.Start)
	}
	if success.ParentID != "parent-session" || success.ExternalChild == nil {
		t.Fatal("a saved summary must not change the row's place or navigation", success)
	}
	if *s.Totals["tokens"].Value != 100 {
		t.Fatalf("saved summaries must stay outside workspace totals: %+v", s.Totals["tokens"])
	}
	if g := s.GroupTotals["tokens"]; g.Value == nil || *g.Value != 1099 {
		t.Fatalf("group totals count what is shown: %+v", g)
	}
	if s.Coverage.Saved != 1 {
		t.Fatal("saved rows are counted apart from unqueried ones", s.Coverage)
	}
	// Its lane reads the child's transcript where it is held.
	lanes, _, err := c.delegationTimelineMessages(t.Context(), s, []string{success.ID}, *s.Start-1, *s.End+1)
	if err != nil {
		t.Fatal(err)
	}
	if messages, _ := lanes[success.ID]["messages"].([]map[string]any); len(messages) != 1 || messages[0]["id"] != "child-reply" {
		t.Fatalf("the lane must show the child's own messages: %+v", lanes[success.ID])
	}

	// A change to the child leaves its saved summary shown, marked as being
	// updated, until maintenance reads the child's workspace again.
	run(`UPDATE agent_sessions SET total_tokens=1500 WHERE id='child-session'`)
	if success = reread(); success.Saved == nil || !success.Saved.Pending || *success.Self["tokens"].Value != 999 {
		t.Fatalf("a changed child keeps its summary while it is updated: %+v %+v", success.Saved, success.Self["tokens"])
	}
	if _, err := c.refreshDelegationSummaries(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if success = reread(); success.Saved.Pending || *success.Self["tokens"].Value != 1500 {
		t.Fatalf("the summary must follow the child: %+v %+v", success.Saved, success.Self["tokens"])
	}

	// Exercise summary maintenance through the same cross-workspace launch
	// with timed tools, usage remainders and later source corrections.
	fixture := func(t *testing.T) *Catalog {
		t.Helper()
		c, _ := testCatalog(t)
		seedDelegation(t, c)
		execTx(t, c.DB,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('remote','codex','local','remote','Remote','2026-01-01')`,
			`UPDATE conversations SET workspace_id='remote' WHERE id='child'`,
			`UPDATE agent_sessions SET workspace_id='remote' WHERE conversation_id='child'`,
			`UPDATE tool_calls SET workspace_id='remote' WHERE conversation_id='child'`)
		return c
	}
	refresh := func(t *testing.T, c *Catalog) {
		t.Helper()
		if _, err := c.refreshDelegationSummaries(t.Context(), 100); err != nil {
			t.Fatal(err)
		}
	}
	read := func(t *testing.T, c *Catalog, workspace string) *delegationSnapshot {
		t.Helper()
		s, err := c.readDelegationSnapshot(t.Context(), workspace)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Run("saved activity replaces the launch envelope", func(t *testing.T) {
		c := fixture(t)
		execSQL(t, c.DB, `UPDATE agent_sessions SET started_at='2025-01-01T00:00:00Z',ended_at='2027-01-01T00:00:00Z' WHERE id='launch-alias'`)
		refresh(t, c)
		local, remote := read(t, c, "delegation-work"), read(t, c, "remote")
		got, want := local.ByID["child-session"], remote.ByID["child-session"]
		if got.Saved == nil || got.Start == nil || got.End == nil || *got.Start != *want.Start || *got.End != *want.End || !near(got.All["clock"].Value, *got.SubtreeEnd-*got.SubtreeStart) {
			t.Fatalf("saved bounds %v–%v must match transcript %v–%v", got.Start, got.End, want.Start, want.End)
		}
		if *local.Start < *delegationTime("2026-01-01T00:00:00Z") || *local.End > *delegationTime("2026-01-01T00:02:00Z") {
			t.Fatal("launch envelope still stretches the timeline viewport", local.Start, local.End)
		}
	})
	t.Run("older summaries are rebuilt and initialization is idempotent", func(t *testing.T) {
		c := fixture(t)
		refresh(t, c)
		// Remove this build's column-aware cache triggers before simulating
		// the previous schema, which never had the revision column.
		triggers, err := queryMaps(c.DB, "SELECT name FROM sqlite_schema WHERE type='trigger' AND tbl_name='delegation_summaries'")
		if err != nil {
			t.Fatal(err)
		}
		for _, trigger := range triggers {
			execSQL(t, c.DB, "DROP TRIGGER "+sqlIdentifier(firstString(trigger["name"])))
		}
		execSQL(t, c.DB, `ALTER TABLE delegation_summaries DROP COLUMN revision;
			UPDATE meta SET value='1' WHERE key='delegation_summaries_version'`)
		if err := c.ensureDelegationSummaries(); err != nil {
			t.Fatal(err)
		}
		if err := c.ensureQueryCacheGeneration(); err != nil {
			t.Fatal(err)
		}
		refresh(t, c)
		before := read(t, c, "delegation-work")
		if n := before.ByID["child-session"]; n.Saved == nil || !near(n.Self["tokens"].Value, 200) {
			t.Fatal("upgrade did not rebuild the summary", n.Saved)
		}
		if err := c.ensureDelegationSummaries(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.savedActivity(t.Context(), before, []string{"child-session"}); err != nil {
			t.Fatal("initialization replaced an unchanged summary", err)
		}
	})
	t.Run("saved branch retains token and cost remainders", func(t *testing.T) {
		c := fixture(t)
		if err := c.replacePricing(testPricing(), "summary-pricing"); err != nil {
			t.Fatal(err)
		}
		if err := c.seedSessionUsage(); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `UPDATE agent_session_usage SET attribution='run-remainder',model='gpt-5.5',usage_hour='2026-07-02T00:00:00Z' WHERE agent_session_id='child-session'`)
		refresh(t, c)
		local, remote := read(t, c, "delegation-work"), read(t, c, "remote")
		n := local.ByID["child-session"]
		if n.Saved == nil || n.TokenAttribution == nil || n.TokenAttribution.Unallocated != 200 {
			t.Fatal("saved branch lost its unallocated usage", n.TokenAttribution)
		}
		if !near(local.GroupTotals["tokens"].Value, *local.Totals["tokens"].Value+*remote.Totals["tokens"].Value) || !near(local.GroupTotals["cost"].Value, *remote.Totals["cost"].Value) {
			t.Fatalf("saved group lost accounted usage: tokens=%+v cost=%+v", local.GroupTotals["tokens"], local.GroupTotals["cost"])
		}
		// Combining segments must also preserve scoped and legacy accounting.
		sum := delegationCombineSummaries([]delegationSummary{remote.ByID[n.ID].summary(), {Self: map[string]delegationMetric{"tokens": delegationNumber(10, "reported", 1, 1), "cost": delegationNumber(1, "estimated", 1, 1)}}})
		combined := &delegationNode{ID: n.ID, Self: sum.Self, TokenAttribution: sum.TokenAttribution, accountedTokens: sum.AccountedTokens}
		if !near(delegationTokenTotal([]*delegationNode{combined}).Value, 210) || !near(delegationAggregate([]*delegationNode{combined})["cost"].Value, *remote.Totals["cost"].Value+1) {
			t.Fatal("combined summaries lost accounting", sum)
		}
		observed := delegationSummary{Self: map[string]delegationMetric{"tokens": delegationNumber(200, "reported-minimum", 1, 1)}, TokenAttribution: &sessionTokenAttribution{Observed: 200}}
		minimum := delegationCombineSummaries([]delegationSummary{observed, observed})
		if minimum.TokenAttribution.Observed != 200 || !near(minimum.Self["tokens"].Value, 200) {
			t.Fatal("overlapping last-request observations were added", minimum)
		}
		execSQL(t, c.DB, `INSERT INTO agent_session_token_evidence(agent_session_id,message_id,scope,total_tokens) VALUES('child-session','nested-result','last-request',400)`)
		refresh(t, c)
		if a := read(t, c, "delegation-work").ByID[n.ID].TokenAttribution; a == nil || a.Observed != 400 {
			t.Fatal("corrected token evidence left the saved minimum stale", a)
		}
		execSQL(t, c.DB, `UPDATE agent_session_usage SET attribution='request' WHERE agent_session_id='child-session'`)
		refresh(t, c)
		if !near(read(t, c, "delegation-work").ByID[n.ID].Self["remainder"].Value, 50) {
			t.Fatal("legacy ledger must retain its previous remainder interpretation")
		}
		execSQL(t, c.DB, `INSERT INTO usage_attribution_state(workspace_id,version) VALUES('remote','6') ON CONFLICT(workspace_id) DO UPDATE SET version=excluded.version`)
		refresh(t, c)
		if !near(read(t, c, "delegation-work").ByID[n.ID].Self["remainder"].Value, 0) {
			t.Fatal("saved summary did not follow the ledger interpretation")
		}
	})
	t.Run("moved and deleted hosts retire their old summaries", func(t *testing.T) {
		c := fixture(t)
		execTx(t, c.DB,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('remote2','codex','local','remote2','Remote 2','2026-01-01')`,
			`UPDATE conversations SET workspace_id='remote2' WHERE id='nested'`,
			`UPDATE agent_sessions SET workspace_id='remote2' WHERE conversation_id='nested'`,
			`UPDATE tool_calls SET workspace_id='remote2' WHERE conversation_id='nested'`)
		refresh(t, c)
		execTx(t, c.DB,
			`UPDATE conversations SET workspace_id='remote2' WHERE id='child'`,
			`UPDATE agent_sessions SET workspace_id='remote2' WHERE conversation_id='child'`,
			`UPDATE tool_calls SET workspace_id='remote2' WHERE conversation_id='child'`)
		// The source moved after the sweep, into a workspace already hosting
		// another summary. A step must not publish the moved transcript twice.
		if _, err := c.refreshDelegationSummariesFor(t.Context(), []string{"child"}); err != nil {
			t.Fatal(err)
		}
		if countRows(t, c, "SELECT count(*) FROM delegation_summaries WHERE node_id='child-session'") > 1 {
			t.Fatal("move between sweep and refresh duplicated saved usage")
		}
		refresh(t, c)
		n := read(t, c, "delegation-work").ByID["child-session"]
		if n.Saved == nil || !slices.Equal(n.Saved.Workspaces, []string{"remote2"}) || !near(n.Self["tokens"].Value, 200) {
			t.Fatal("moved transcript counted its old workspace", n.Saved, n.Self["tokens"])
		}
		execSQL(t, c.DB, `DELETE FROM conversations WHERE id='child'`)
		refresh(t, c)
		if countRows(t, c, "SELECT count(*) FROM delegation_summaries WHERE workspace_id IN ('remote','remote2')") != 0 {
			t.Fatal("deleted host left saved usage behind")
		}
	})
	t.Run("pricing replacements refresh saved costs", func(t *testing.T) {
		c := fixture(t)
		override(t, &time.Local, time.FixedZone("summary-pricing", -7*60*60))
		if err := c.replacePricing(testPricing(), "summary-before"); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,total_tokens) VALUES('child-session','2026-07-02T00:00:00Z','gpt-5.5','session-start',1000000,1000000,1000000)`)
		refresh(t, c)
		doc := testPricing()
		doc.Changes = append(doc.Changes, priceChange{Provider: "openai", Model: "gpt-5.5", EffectiveFrom: "2026-07-02", Input: rate(4), CacheRead: rate(.2), Output: rate(16), Status: "confirmed", SourceURL: "https://example.com/new-rate"})
		if err := c.replacePricing(doc, "summary-after"); err != nil {
			t.Fatal(err)
		}
		refresh(t, c)
		if got := read(t, c, "delegation-work").ByID["child-session"]; !near(got.Self["cost"].Value, 4) || got.Saved.Pending {
			t.Fatal("saved pricing did not follow the price book", got.Self["cost"], got.Saved)
		}
	})
	t.Run("empty event completion clears saved pending state", func(t *testing.T) {
		c := fixture(t)
		// The optional ledger is created after the summary schema.
		if err := c.ensureAgentEventsLedger(t.Context()); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `INSERT INTO agent_events_dirty(conversation_id,token) VALUES('child','pending') ON CONFLICT(conversation_id) DO UPDATE SET token=excluded.token`)
		refresh(t, c)
		if !read(t, c, "delegation-work").ByID["child-session"].EventsPending {
			t.Fatal("pending events must remain qualified in a summary")
		}
		// A completed scan with no candidates changes no agent_events rows.
		execSQL(t, c.DB, `DELETE FROM agent_events_dirty WHERE conversation_id='child'`)
		refresh(t, c)
		if read(t, c, "delegation-work").ByID["child-session"].EventsPending {
			t.Fatal("completed empty scan left saved events pending")
		}
	})
	t.Run("repository repair deduplicates saved files", func(t *testing.T) {
		c := fixture(t)
		execSQL(t, c.DB, `UPDATE tool_calls SET file_path='foo.go',path_absolute='/checkout/foo.go' WHERE id='t-child';
			UPDATE tool_calls SET file_path='foo.go',repo_path='foo.go',path_repository_id='repo' WHERE id='t-root'`)
		refresh(t, c)
		if !near(read(t, c, "delegation-work").GroupTotals["filesRead"].Value, 2) {
			t.Fatal("unresolved checkouts must keep separate file identities")
		}
		execSQL(t, c.DB, `UPDATE tool_calls SET repo_path='foo.go',path_repository_id='repo',path_repository='repo' WHERE id='t-child'`)
		refresh(t, c)
		if !near(read(t, c, "delegation-work").GroupTotals["filesRead"].Value, 1) {
			t.Fatal("repository repair left the saved file identity stale")
		}
	})
	t.Run("timeline rejects replaced or removed summary versions", func(t *testing.T) {
		c := fixture(t)
		c.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
		refresh(t, c)
		before := read(t, c, "delegation-work")
		// An identical publication keeps activity available to the old snapshot.
		execSQL(t, c.DB, `UPDATE agent_sessions SET model=model WHERE id='child-session'`)
		refresh(t, c)
		if _, err := c.savedActivity(t.Context(), before, []string{"child-session"}); err != nil {
			t.Fatal("unchanged activity must remain readable", err)
		}
		// A name changes the lane without changing any summary metric, even
		// when both saves have exactly the same timestamp.
		execSQL(t, c.DB, `UPDATE tool_calls SET tool_name='Search' WHERE id='t-child'`)
		refresh(t, c)
		if _, err := c.savedActivity(t.Context(), before, []string{"child-session"}); !errors.Is(err, errDelegationSnapshotChanged) {
			t.Fatal("old snapshot silently read a replacement summary", err)
		}
		server := testServer(t, Config{}, c)
		response := httptest.NewRecorder()
		server.writeDelegationTimeline(response, httptest.NewRequest("GET", "/?sessions=child-session", nil), before)
		if response.Code != 409 || !strings.Contains(response.Body.String(), "snapshot_changed") {
			t.Fatal("timeline must ask the client to reload", response.Code, response.Body.String())
		}
		after := read(t, c, "delegation-work")
		if after.ID == before.ID {
			t.Fatal("replaced activity must change the snapshot token")
		}
		if _, err := c.savedActivity(t.Context(), after, []string{"child-session"}); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `DELETE FROM delegation_summaries WHERE workspace_id='remote'`)
		if _, err := c.savedActivity(t.Context(), after, []string{"child-session"}); !errors.Is(err, errDelegationSnapshotChanged) {
			t.Fatal("removed summary must invalidate its activity", err)
		}
	})
}

func TestDelegationCopiedLaunchesAcrossWorkspaces(t *testing.T) {
	for _, copiedResult := range []bool{false, true} {
		t.Run(fmt.Sprintf("copied acknowledgement=%v", copiedResult), func(t *testing.T) {
			c, config := testCatalog(t)
			tx := seedTx(t, c.DB)
			run := func(q string, args ...any) {
				t.Helper()
				if _, err := tx.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			for _, conversation := range []struct {
				id, parent, path, nickname string
				depth                      int
			}{{id: "parent"}, {"child", "parent", "/root/explore", "Peirce", 1}} {
				id := conversation.id
				run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, id, id, id)
				run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,agent_path,agent_nickname) VALUES(?,?,'codex','local',?,?,?,?,?)`, id, id, id, nilIfEmpty(conversation.parent), conversation.depth, conversation.path, conversation.nickname)
				run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens) VALUES(?,?,?,'main','subagent','codex','reported-reconciled',100)`, id+"-session", id, id)
				// Forked history includes the launch that created the fork, but
				// its acknowledgement may only exist in the parent transcript.
				run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,?,?,'assistant','delegation','{"input":{"task_name":"explore"},"tool":"spawn_agent"}','spawn-call','launch')`, id+"-launch", id, id+"-launch")
				run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,delegation_message_id,kind,provider,usage_status) VALUES(?,?,?,'spawn-call',?,?,'subagent','codex','unavailable')`, id+"-launch-session", id, id, id+"-session", id+"-launch")
				if id == "parent" || copiedResult {
					run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,?,?,'tool','delegation_result','{"content":"{\"task_name\":\"/root/explore\"}"}','spawn-call','result')`, id+"-result", id, id+"-result")
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			parent, err := c.readDelegationSnapshot(t.Context(), "parent")
			if err != nil {
				t.Fatal(err)
			}
			child, err := c.readDelegationSnapshot(t.Context(), "child")
			if err != nil {
				t.Fatal(err)
			}
			// The child keeps one row identity in both workspaces; its launch
			// handle is an alias of it.
			outgoing, incoming := parent.ByID["child-session"], child.ByID["child-session"]
			if alias, _ := parent.resolveAlias("parent-launch-session"); alias != "child-session" {
				t.Fatal("the launch handle must resolve to the child execution", alias)
			}
			if outgoing == nil || incoming == nil || outgoing.ParentID != incoming.ParentID || outgoing.Depth != incoming.Depth || incoming.Ancestry.Depth == nil || *incoming.Ancestry.Depth != 1 || outgoing.Label != incoming.Label || incoming.Label != "explore" {
				t.Fatalf("opening a child changed its identity: outgoing=%+v incoming=%+v", outgoing, incoming)
			}
			if len(parent.Nodes) != 2 || len(child.Nodes) != 2 || child.ByID["child-launch-session"] != nil || incoming.ChildCount != 0 {
				t.Fatalf("inherited launch became another execution: parent=%+v child=%+v", parent.Nodes, child.Nodes)
			}
			status, body := delegationGET(t, testServer(t, config, c), "/api/work/child/delegation?conversation=child")
			if status != 200 || body["session_count"] != float64(1) || len(body["focus"].([]any)) != 2 || *child.Totals["tokens"].Value != 100 {
				t.Fatal("copied launch changed navigation or accounting", status, body)
			}
			// An execution with its own usage must survive, even if its launch
			// repeats the same parent call. Likewise, a different call body is
			// not sufficient evidence of copied history.
			for _, change := range []struct {
				query string
				// The launch has its own usage, or its retained acknowledgement
				// matches its call; otherwise it is an unresolved attempt.
				execution bool
			}{
				{`UPDATE agent_sessions SET total_tokens=10,usage_status='reported-reconciled' WHERE id='child-launch-session'`, true},
				{`UPDATE agent_sessions SET total_tokens=0,usage_status='unavailable' WHERE id='child-launch-session'; UPDATE messages SET text='{"input":{"task_name":"another task"}}' WHERE id='child-launch'`, copiedResult},
				{`UPDATE messages SET text=(SELECT text FROM messages WHERE id='parent-launch'),call_id='new-call' WHERE id='child-launch'`, false},
				{`UPDATE messages SET call_id='spawn-call' WHERE id='child-launch'; UPDATE conversations SET account='other' WHERE id='child'`, copiedResult},
				{`UPDATE conversations SET account='local',provider='other' WHERE id='child'`, copiedResult},
			} {
				if _, err := c.DB.Exec(change.query); err != nil {
					t.Fatal(err)
				}
				s, err := c.readDelegationSnapshot(t.Context(), "child")
				if err != nil {
					t.Fatal(err)
				}
				// Not a copy, so never discarded: an execution row, or an
				// attempt visible on its issuer rather than a phantom row.
				attempt := s.ByID["child-session"] != nil && slices.ContainsFunc(s.ByID["child-session"].Attempts, func(a delegationAttempt) bool {
					return a.ID == "child-launch-session" && a.Outcome == string(attemptUnresolved) && a.MessageID == "child-launch"
				})
				if change.execution != (s.ByID["child-launch-session"] != nil) || change.execution == attempt {
					t.Fatalf("launch neither an execution nor a visible attempt after %q: %+v", change.query, s.Nodes)
				}
			}
		})
	}
}

func TestDelegationCommandLaunchedChildrenAcrossWorkspaces(t *testing.T) {
	c, config := testCatalog(t)
	tx := seedTx(t, c.DB)
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"parent", "author", "critic", "launched-critic", "unrelated"} {
		run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, id, id, id)
		provider := "claude"
		if id == "parent" {
			provider = "codex"
		}
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,effort) VALUES(?,?,?,'local',?,'own-model','medium')`, id, id, provider, id)
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens) VALUES(?,?,?,'main','root',?,'reported-reconciled',100)`, id+"-session", id, id, provider)
	}
	run(`UPDATE conversations SET parent_id='author',agent_depth=1 WHERE id='critic'`)
	for _, launch := range []struct{ parent, child string }{{"parent", "author"}, {"author", "launched-critic"}, {"unrelated", "critic"}} {
		id := launch.child + "-launch"
		run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,?,?,'assistant','tool_call','{}',?)`, id, launch.parent, id, id)
		run(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,call_message_id) VALUES(?,?,?,0,'codex','tool_call','exec_command','shell','ok',?)`, id, launch.parent, launch.parent, id)
		run(`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES(?,?,?,'claude')`, launch.child, launch.parent, id)
	}
	commitSeed(t, tx)
	s, err := c.delegationSnapshot(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 4 || len(s.Roots) != 1 || s.Roots[0].ID != "parent-session" || s.Roots[0].ChildCount != 1 {
		t.Fatalf("command-launched family missing from parent hierarchy: %+v", s.Nodes)
	}
	for _, child := range []struct{ id, parent, proof, message string }{
		{"author", "parent-session", "inferred-command-launch", "author-launch"},
		{"critic", "author-session", "conversation-parent", ""},
		{"launched-critic", "author-session", "inferred-command-launch", "launched-critic-launch"},
	} {
		// Children in other workspaces are navigable family context, under the
		// same row IDs their own workspaces use.
		n := s.ByID[child.id+"-session"]
		if n == nil || n.ParentID != child.parent || n.ParentEvidence != child.proof || n.MessageID != child.message {
			t.Fatalf("child %s lost launch ownership: %+v", child.id, n)
		}
		if !n.ContextOnly || n.Navigation == nil || n.Navigation.ConversationID != child.id || n.ConversationID != child.id || n.WorkspaceID != child.id || n.Provider != "claude" || n.Model != "own-model" || n.Effort != "medium" {
			t.Fatalf("child %s lost transcript identity: %+v", child.id, n)
		}
		for _, key := range []string{"tokens", "calls", "messages", "clock"} {
			if n.Self[key].Value != nil {
				t.Fatalf("unqueried child %s %s must remain unknown: %+v", child.id, key, n.Self[key])
			}
		}
	}
	// Workspace totals count its own executions; the subtree that reaches
	// unqueried context is partial.
	if total, all := s.Totals["tokens"], s.Roots[0].All["tokens"]; total.Value == nil || *total.Value != 100 || all.Value == nil || *all.Value != 100 || all.Quality != "partial" {
		t.Fatalf("outside usage changed workspace accounting: %+v %+v", total, all)
	}
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/parent/delegation?conversation=parent")
	// The workspace runs one session; its outside children are context rows.
	if status != 200 || body["session_count"] != float64(1) || body["total"] != float64(4) || len(body["focus"].([]any)) != 2 {
		t.Fatal("command-launched child must restore hierarchy and transcript navigation", status, body["session_count"], body["total"], len(body["focus"].([]any)))
	}
	// An inferred launch cannot steal a child from its recorded parent.
	unrelated, err := c.delegationSnapshot(context.Background(), "unrelated")
	if err != nil || len(unrelated.Nodes) != 1 {
		t.Fatalf("inferred launch overrode recorded ownership: %+v %v", unrelated, err)
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

// Launch results are read once, by the shared evidence (readLegacyLaunchResult).
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
			if got := readLegacyLaunchResult(tc.result); got.failed != tc.failed {
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
		// A handle linked to its child transcript is that execution even with
		// usage of its own; both streams' usage is kept and flagged.
		{name: "handle with usage", independent: true, parent: "child"},
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
				// A Claude agent ID names an agent file under the launching root.
				run(`UPDATE conversations SET provider='claude',native_id='root-native:subagent:agent-parent-child' WHERE id='child'`)
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
			// The launch handle is an alias of the child it created.
			id, _ := outgoing.resolveAlias("nested-alias")
			launch := outgoing.ByID[id]
			if id != "nested-session" || launch == nil || outgoing.ByID[launch.ParentID] == nil || outgoing.ByID[launch.ParentID].ConversationID != tc.parent {
				t.Fatal("outgoing hierarchy lost parent execution", launch)
			}
			if child := outgoing.ByID["child-session"]; tc.independent && (child.Self["tokens"].Value == nil || *child.Self["tokens"].Value != 210 || !slices.ContainsFunc(outgoing.Warnings, func(w string) bool { return strings.Contains(w, "more than one source stream") })) {
				t.Fatal("the handle's own usage must stay counted and flagged", child.Self["tokens"], outgoing.Warnings)
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
			// The ancestor's workspace is past the analysis limit; the child's is not.
			override(t, &delegationAnalysisLimit, 40)
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
				run(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<40) INSERT INTO conversations(id,workspace_id,native_id,provider,account) SELECT 'unrelated-'||n,'parent-work','unrelated-'||n,'codex','local' FROM seq`)
			case "sessions":
				run(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<41) INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) SELECT 'unrelated-'||n,'parent-work','parent','unrelated-'||n,'subagent','codex' FROM seq`)
			case "outside siblings":
				run(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<40) INSERT INTO conversations(id,workspace_id,native_id,provider,account,parent_id,agent_depth) SELECT 'unrelated-'||n,'sibling-work','unrelated-'||n,'codex','local','parent',1 FROM seq`)
			}
			// Siblings are family context up to a bound; past it, coverage says
			// so instead of quietly building a smaller tree.
			override(t, &delegationFamilyLimit, 20)
			s, err := c.delegationSnapshot(context.Background(), "child-work")
			if err != nil {
				t.Fatalf("unrelated ancestor records blocked child hierarchy: %v", err)
			}
			child := s.ByID["conversation:child"]
			if child == nil || child.ContextOnly || child.Depth != 1 || s.ByID[child.ParentID] == nil || s.ByID[child.ParentID].ConversationID != "parent" || s.Nodes[0].ConversationID != "parent" {
				t.Fatal("expected the child beneath its parent context", s.Nodes)
			}
			if siblings := records != "conversations"; len(s.Nodes) > 1+delegationFamilyLimit || s.FamilyComplete == siblings || siblings && len(s.Nodes) != 1+delegationFamilyLimit {
				t.Fatal("family context must be bounded and report its coverage", len(s.Nodes), s.FamilyComplete)
			}
			status, body := delegationGET(t, testServer(t, config, c), "/api/work/child-work/delegation?conversation=child")
			if status != 200 || body["session_count"] != float64(1) {
				t.Fatal(status, body)
			}
		})
	}
}

// A continuation retained in another workspace is a segment of its origin's
// execution: both workspaces show one row under one ID, navigable from either
// transcript, with the other segment's activity left uncounted rather than
// treated as zero. The family stays navigable context in the same order, and
// launch history the continuation copied binds to the original child.
func TestDelegationContinuationSegmentsAcrossWorkspaces(t *testing.T) {
	c, config := testCatalog(t)
	tx := seedTx(t, c.DB)
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []string{"origin-work", "continued-work"} {
		run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, w, w, w)
	}
	for _, cv := range []struct {
		id, workspace, parent, start string
		depth                        int
	}{{"a", "origin-work", "", "2026-01-01T00:00:00Z", 0}, {"b", "continued-work", "a", "2026-01-01T01:00:00Z", 0}, {"kid", "origin-work", "a", "2026-01-01T00:10:00Z", 1}, {"kid2", "origin-work", "a", "2026-01-01T00:20:00Z", 1}} {
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES(?,?,'codex','local',?,?,?,?)`, cv.id, cv.workspace, cv.id, nilIfEmpty(cv.parent), cv.depth, cv.start)
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens,started_at,ended_at) VALUES(?,?,?,'main','root','codex','reported-reconciled',100,?,?)`, cv.id+"-session", cv.workspace, cv.id, cv.start, cv.start)
	}
	// Each launch is in the origin transcript; the continuation retains a
	// copy of the first one (same event ID, call and body).
	for i, kid := range []string{"kid", "kid2"} {
		at := fmt.Sprintf("2026-01-01T00:%02d:00Z", 5+10*i)
		run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash,created_at) VALUES(?,'a',?,'assistant','delegation','{"input":{"task_name":"review"}}',?,?,?)`, "a-"+kid, "evt-"+kid, "call-"+kid, "h-"+kid, at)
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,delegation_message_id,kind,provider,usage_status) VALUES(?,'origin-work','a',?,'a-session',?,?,'subagent','codex','unavailable')`, "a-launch-"+kid, "call-"+kid, kid, "a-"+kid)
	}
	run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash,created_at) VALUES('b-kid','b','evt-kid','assistant','delegation','{"input":{"task_name":"review"}}','call-kid','h-kid','2026-01-01T01:00:01Z')`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,delegation_message_id,kind,provider,usage_status) VALUES('b-launch-kid','continued-work','b','call-kid','b-session','b-kid','subagent','codex','unavailable')`)
	run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('b-own','b','evt-b1','user','message','continue','b-own','2026-01-01T01:00:02Z')`)
	commitSeed(t, tx)

	origin, err := c.readDelegationSnapshot(t.Context(), "origin-work")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := c.readDelegationSnapshot(t.Context(), "continued-work")
	if err != nil {
		t.Fatal(err)
	}
	here, there := continued.ByID["a-session"], origin.ByID["a-session"]
	if here == nil || there == nil || continued.ByID["b-session"] != nil || !slices.Equal(here.Segments, []delegationSegment{{"a", "origin-work"}, {"b", "continued-work"}}) || !slices.Equal(there.Segments, here.Segments) {
		t.Fatalf("a continuation must be a segment of its origin's row in both workspaces: %+v %+v", here, there)
	}
	if here.ConversationID != "b" || here.WorkspaceID != "continued-work" || here.ExternalChild != nil {
		t.Fatalf("a local segment is not an external transcript: %+v", here)
	}
	// Each workspace reads only its own segment.
	for name, n := range map[string]*delegationNode{"continued": here, "origin": there} {
		if m := n.Self["tokens"]; m.Value == nil || *m.Value != 100 || m.Quality != "partial" {
			t.Fatalf("%s: the other segment's usage is not counted here, so tokens are partial: %+v", name, m)
		}
	}
	if continued.Coverage.Unqueried != 1 || origin.Coverage.Unqueried != 1 {
		t.Fatal("unqueried segments must be reported", continued.Coverage, origin.Coverage)
	}
	// The copied launch is history, not the continuation's own message.
	if m := here.Self["messages"]; m.Value == nil || *m.Value != 1 || m.Quality != "partial" {
		t.Fatalf("copied launch counted as the continuation's activity: %+v", m)
	}
	// The family stays navigable context, in the resolution's order, and the
	// copied launch handle opens the original child.
	kids := []*delegationNode{continued.ByID["kid-session"], continued.ByID["kid2-session"]}
	for _, k := range kids {
		if k == nil || !k.ContextOnly || k.Navigation == nil || k.Navigation.WorkspaceID != "origin-work" || k.ParentID != "a-session" {
			t.Fatalf("family context lost navigation or parent: %+v", k)
		}
	}
	if kids[0].Order > kids[1].Order || origin.ByID["kid-session"].Order > origin.ByID["kid2-session"].Order {
		t.Fatal("siblings must keep the resolution's order in every workspace")
	}
	if alias, _ := continued.resolveAlias("b-launch-kid"); alias != "kid-session" {
		t.Fatal("copied launch must open the original child", alias)
	}
	// Opening the continuation's transcript selects the shared row.
	server := testServer(t, config, c)
	status, body := delegationGET(t, server, "/api/work/continued-work/delegation?conversation=b")
	if status != 200 || len(body["focus"].([]any)) == 0 || body["focus"].([]any)[0].(map[string]any)["id"] != "a-session" {
		t.Fatal("the continuation's transcript must focus its execution", status, body["focus"])
	}
	// The Timeline lane keeps the continuation's own message; the copied
	// launch repeats the execution's earlier segment and is counted, not shown.
	status, body = delegationGET(t, server, "/api/work/continued-work/parallel?sessions=a-session&labels=1")
	if status != 200 {
		t.Fatal(status, body)
	}
	lane := body["lanes"].([]any)[0].(map[string]any)
	ids := []string{}
	for _, m := range lane["messages"].([]any) {
		ids = append(ids, m.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{"b-own"}) || lane["repeated_messages"] != float64(1) || lane["inherited_messages"] != float64(0) {
		t.Fatal("copied history must stay out of the lane and be reported", ids, lane["repeated_messages"], lane["inherited_messages"])
	}
	// The origin segment is retained in another workspace: the lane names it
	// instead of looking complete.
	if omitted := lane["omitted_segments"].([]any); len(omitted) != 1 || omitted[0].(map[string]any)["conversation_id"] != "a" || omitted[0].(map[string]any)["workspace_id"] != "origin-work" {
		t.Fatal("omitted segments", lane["omitted_segments"])
	}
	// One name per execution in every reader; local context is a subtitle.
	for _, id := range []string{"a-session", "kid-session", "kid2-session"} {
		if continued.ByID[id].Label != origin.ByID[id].Label {
			t.Fatalf("%s is named %q in one reader and %q in the other", id, continued.ByID[id].Label, origin.ByID[id].Label)
		}
	}
	// An evidence-only commit after the explorer loaded: the cached snapshot's
	// graph is not paired with newer rows. Its lanes are refused, a client
	// holding its ID is told to refresh, and the refresh sees the new graph.
	before := body["snapshot"].(string)
	stale, err := c.delegationSnapshot(t.Context(), "continued-work")
	if err != nil || stale.ID != before {
		t.Fatal("the lane read the cached snapshot", err)
	}
	if _, err := c.DB.Exec(`INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('b','late','record','launch_outcome','launch:call-late','','derived','test/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.delegationTimelineMessages(t.Context(), stale, []string{"a-session"}, 0, 4e12); !errors.Is(err, errDelegationSnapshotChanged) {
		t.Fatal("a lane of a superseded snapshot must be refused", err)
	}
	if status, body = delegationGET(t, server, "/api/work/continued-work/parallel?sessions=a-session&labels=1&snapshot="+before); status != 409 || body["code"] != "snapshot_changed" {
		t.Fatal("a client holding the superseded snapshot must be told to refresh", status, body)
	}
	status, body = delegationGET(t, server, "/api/work/continued-work/parallel?sessions=a-session&labels=1")
	if status != 200 || body["snapshot"] == before {
		t.Fatal("the refresh must resolve the new evidence", status, body["snapshot"])
	}
	lane = body["lanes"].([]any)[0].(map[string]any)
	if len(lane["messages"].([]any)) != 1 || lane["repeated_messages"] != float64(1) {
		t.Fatal("copy exclusion after the refresh", lane)
	}
	refreshed, err := c.delegationSnapshot(t.Context(), "continued-work")
	if err != nil || refreshed.Revision.Generation <= stale.Revision.Generation || !slices.Equal(refreshed.ByID["a-session"].Segments, here.Segments) {
		t.Fatal("the refreshed snapshot keeps segments at the new generation", err)
	}
	if alias, _ := refreshed.resolveAlias("b-launch-kid"); alias != "kid-session" {
		t.Fatal("aliases after the refresh", alias)
	}
}

// A continuation retained in one workspace is one lane: each segment's own
// messages and the agent events said in them are shown.
func TestDelegationLaneShowsEverySegmentWithItsEvents(t *testing.T) {
	c, config := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES('a','w','codex','local','a',NULL,0,'2026-01-01T00:00:00Z'),('b','w','codex','local','b','a',0,'2026-01-01T01:00:00Z')`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status) VALUES('a-session','w','a','main','root','codex','unavailable'),('b-session','w','b','main','root','codex','unavailable')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('a-own','a','evt-a','user','message','start','ha','2026-01-01T00:00:01Z'),('b-own','b','evt-b','assistant','message','I will assume the default','hb','2026-01-01T01:00:01Z')`,
		`INSERT INTO agent_event_messages(message_id,conversation_id,source,candidates,events,model,lexicon,imported_at) VALUES('b-own','b','reply',1,1,'t','t','2026-01-01')`,
		`INSERT INTO agent_events(id,message_id,conversation_id,source,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model)
		 VALUES(7,'b-own','b','reply','assumption',0.9,0,25,'I will assume the default','','','[]','[]','t')`,
	)
	s, err := c.delegationSnapshot(t.Context(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if n := s.ByID["a-session"]; n == nil || s.ByID["b-session"] != nil || len(n.Segments) != 2 {
		t.Fatal("the continuation must be a segment of one row", n)
	}
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/w/parallel?sessions=a-session&labels=1")
	if status != 200 {
		t.Fatal(status, body)
	}
	lane := body["lanes"].([]any)[0].(map[string]any)
	ids := []string{}
	for _, m := range lane["messages"].([]any) {
		ids = append(ids, m.(map[string]any)["id"].(string))
	}
	events := lane["agent_events"].([]any)
	if !slices.Equal(ids, []string{"a-own", "b-own"}) || len(events) != 1 || events[0].(map[string]any)["message_id"] != "b-own" || len(lane["omitted_segments"].([]any)) != 0 {
		t.Fatal("segment messages and events", ids, events, lane["omitted_segments"])
	}
}

// Past the family bound a reader keeps its nearest relatives, and every row
// that leaves children out counts them; expanding those rows
// (delegation_relatives) reaches the whole family from any reader, with
// parentage and order intact. Depth reached through stored, unverified parent
// links is indented but its metric is not an observed depth.
func TestDelegationFamilyBoundKeepsRelativesReachable(t *testing.T) {
	c, config := testCatalog(t)
	family := []struct{ id, parent string }{{"r", ""}, {"a", "r"}, {"b", "r"}, {"c", "r"}, {"a1", "a"}, {"a2", "a"}, {"b1", "b"}}
	tx := seedTx(t, c.DB)
	depth := map[string]int{}
	for _, cv := range family {
		if cv.parent != "" {
			depth[cv.id] = depth[cv.parent] + 1
		}
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, "work-"+cv.id, cv.id, "Work "+cv.id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES(?,?,'codex','local',?,?,?,?)`,
			cv.id, "work-"+cv.id, cv.id, nilIfEmpty(cv.parent), depth[cv.id], fmt.Sprintf("2026-01-01T00:%02d:00Z", len(depth))); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	override(t, &delegationFamilyLimit, 2)
	reach := func(workspace string) map[string]bool {
		t.Helper()
		relatives, shown := []string{}, map[string]bool{}
		for round := 0; ; round++ {
			s, err := c.delegationSnapshotWith(t.Context(), workspace, relatives)
			if err != nil {
				t.Fatal(err)
			}
			more := false
			for _, n := range s.Nodes {
				shown[n.ConversationID] = true
				for _, cv := range family {
					if cv.id == n.ConversationID && cv.parent != "" && s.ByID[n.ParentID] != nil && s.ByID[n.ParentID].ConversationID != cv.parent {
						t.Fatalf("%s: %s is under %s", workspace, cv.id, s.ByID[n.ParentID].ConversationID)
					}
				}
				if n.ChildrenOmitted > 0 && !slices.Contains(relatives, n.ID) {
					relatives, more = append(relatives, n.ID), true
				}
			}
			if round == 0 {
				// The reader's own parent and children are always present.
				if s.FamilyComplete || len(s.Nodes) != 3 {
					t.Fatalf("%s: bounded first view has %d rows (complete %v)", workspace, len(s.Nodes), s.FamilyComplete)
				}
			}
			if !more {
				if !s.FamilyComplete {
					t.Fatalf("%s: every row is expanded but the family is incomplete", workspace)
				}
				for i := 1; i < len(s.Nodes); i++ {
					if s.Nodes[i].Order <= s.Nodes[i-1].Order {
						t.Fatal("rows keep the resolution's order")
					}
				}
				return shown
			}
			if round > len(family) {
				t.Fatal("expansion does not converge")
			}
		}
	}
	for _, workspace := range []string{"work-a1", "work-b1", "work-c"} {
		if shown := reach(workspace); len(shown) != len(family) {
			t.Fatalf("from %s only %v are reachable", workspace, shown)
		}
	}
	// The relatives control is part of the request; its snapshot is another.
	server := testServer(t, config, c)
	_, plain := delegationGET(t, server, "/api/work/work-a1/delegation")
	_, expanded := delegationGET(t, server, "/api/work/work-a1/delegation?relatives=r")
	if plain["snapshot"] == expanded["snapshot"] || expanded["total"].(float64) <= plain["total"].(float64) {
		t.Fatal("relatives must load the omitted children", plain["total"], expanded["total"])
	}
	// Stored parent links are not verified: the depth is laid out, but its
	// metric says how certain it is.
	s, err := c.delegationSnapshot(t.Context(), "work-a1")
	if err != nil {
		t.Fatal(err)
	}
	leaf := s.ByID["conversation:a1"]
	if leaf.Depth != 2 || leaf.Ancestry.Depth == nil || *leaf.Ancestry.Depth != 2 || leaf.Ancestry.Certainty != "legacy" || leaf.Self["depth"].Quality != "unverified" || leaf.All["depth"].Quality != "unverified" {
		t.Fatalf("depth certainty: %+v %+v %+v", leaf.Ancestry, leaf.Self["depth"], leaf.All["depth"])
	}
	// A deep chain against the hard capacity: r → a → a1 → d1 → d2 → d3, read
	// from d3's work with room for two context rows. The nearest ancestors are
	// kept, the snapshot never passes the analysis limit, and the family
	// reads incomplete; the left-out ancestors stay named in the chain.
	tx = seedTx(t, c.DB)
	for i, cv := range []struct{ id, parent string }{{"d1", "a1"}, {"d2", "d1"}, {"d3", "d2"}} {
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, "work-"+cv.id, cv.id, "Work "+cv.id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES(?,?,'codex','local',?,?,?,?)`,
			cv.id, "work-"+cv.id, cv.id, cv.parent, 3+i, fmt.Sprintf("2026-01-02T00:%02d:00Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	override(t, &delegationAnalysisLimit, 3)
	deep, err := c.delegationSnapshot(t.Context(), "work-d3")
	if err != nil {
		t.Fatal(err)
	}
	shown := map[string]bool{}
	for _, n := range deep.Nodes {
		shown[n.ConversationID] = true
	}
	if len(deep.Nodes) > delegationAnalysisLimit || deep.FamilyComplete || !shown["d3"] || !shown["d2"] || !shown["d1"] || shown["r"] {
		t.Fatalf("deep chain at capacity: %d rows %v (complete %v)", len(deep.Nodes), shown, deep.FamilyComplete)
	}
	if chain := deep.ByID["conversation:d3"].AncestorIDs; len(chain) != 5 {
		t.Fatalf("left-out ancestors must stay named in the chain: %v", chain)
	}
}

// A Conductor copy of a Claude Code session retains each Agent call again: its
// acknowledgement names no agent, and Conductor records the agent's usage on
// that launch. The two calls are one execution with the agent's transcript,
// whose usage counts once.
func TestDelegationMirroredLaunches(t *testing.T) {
	c, _ := testCatalog(t)
	tx := seedTx(t, c.DB)
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []string{"native", "conductor"} {
		run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'claude','local',?,?,'2026-01-01')`, w, w, w)
	}
	for _, cv := range []struct{ id, workspace, native, parent string }{
		{"parent", "native", "s1", ""}, {"child", "native", "s1:subagent:agent-x", "parent"},
		{"other", "native", "s1:subagent:agent-y", "parent"}, {"mirror", "conductor", "conductor:s1", ""},
	} {
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id) VALUES(?,?,'claude','local',?,?)`, cv.id, cv.workspace, cv.native, nilIfEmpty(cv.parent))
	}
	run(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('parent','mirror','native-alias',1,'{}')`)
	for _, s := range []struct {
		id, workspace, conversation string
		tokens                      int
	}{{"parent", "native", "parent", 100}, {"child", "native", "child", 40}, {"other", "native", "other", 7}, {"mirror", "conductor", "mirror", 100}} {
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens) VALUES(?,?,?,'main','root','claude','reported-reconciled',?)`, s.id+"-session", s.workspace, s.conversation, s.tokens)
	}
	for _, launch := range []struct {
		id, workspace, conversation, child string
		tokens                             int
	}{{"native-launch", "native", "parent", "child", 0}, {"mirror-launch", "conductor", "mirror", "", 41}} {
		run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,?,?,'assistant','delegation','{"input":{"description":"explore"},"tool":"Agent"}','toolu_1',?)`, launch.id, launch.conversation, launch.id, launch.id)
		usage := "unavailable"
		if launch.tokens > 0 {
			usage = "reported-reconciled"
		}
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,delegation_message_id,kind,provider,usage_status,total_tokens,child_conversation_id) VALUES(?,?,?,'toolu_1',?,?,'subagent','claude',?,?,?)`,
			launch.id+"-session", launch.workspace, launch.conversation, launch.conversation+"-session", launch.id, usage, launch.tokens, nilIfEmpty(launch.child))
	}
	commitSeed(t, tx)
	snapshot := func(workspace string) *delegationSnapshot {
		t.Helper()
		s, err := c.readDelegationSnapshot(t.Context(), workspace)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tokens := func(n *delegationNode) float64 {
		if n == nil || n.Self["tokens"].Value == nil {
			return -1
		}
		return *n.Self["tokens"].Value
	}
	// Each workspace shows the agent once, with the usage it retains.
	native := snapshot("native")
	if alias, _ := native.resolveAlias("mirror-launch-session"); alias != "child-session" || native.ByID["mirror-launch-session"] != nil {
		t.Fatalf("the mirrored launch must resolve to the agent's execution: %q %+v", alias, native.Nodes)
	}
	child := native.ByID["child-session"]
	if child == nil || child.ContextOnly || native.ByID[child.ParentID] == nil || native.ByID[child.ParentID].ParentID != "" || tokens(child) != 40 || child.Self["tokens"].Quality == "partial" || child.MessageID != "native-launch" {
		t.Fatalf("agent row lost its own usage or launch: %+v", child)
	}
	for _, n := range native.Nodes {
		if n.ContextOnly && n.Label == child.Label {
			t.Fatalf("mirrored launch left an empty duplicate row: %+v", n)
		}
	}
	mirrored := snapshot("conductor").ByID["child-session"]
	if mirrored == nil || mirrored.ContextOnly || tokens(mirrored) != 41 || mirrored.ExternalChild == nil || mirrored.ExternalChild.ConversationID != "child" || mirrored.MessageID != "mirror-launch" {
		t.Fatalf("the mirror's launch must show the same agent with its recorded usage: %+v", mirrored)
	}
	// Retained beside the transcript, the mirror's usage is a second report.
	if _, err := c.DB.Exec(`UPDATE conversations SET workspace_id='native' WHERE id='mirror'; UPDATE agent_sessions SET workspace_id='native' WHERE conversation_id='mirror'`); err != nil {
		t.Fatal(err)
	}
	if n := snapshot("native").ByID["child-session"]; tokens(n) != 40 {
		t.Fatalf("a mirrored launch's usage must not add to its transcript's: %+v", n)
	}
	// A launch acknowledging another transcript is another call.
	if _, err := c.DB.Exec(`UPDATE agent_sessions SET child_conversation_id='other' WHERE id='mirror-launch-session'`); err != nil {
		t.Fatal(err)
	}
	if alias, _ := snapshot("native").resolveAlias("mirror-launch-session"); alias != "other-session" {
		t.Fatal("launches naming different transcripts must stay apart", alias)
	}
	// Without the mirror, an equal call ID in another transcript proves nothing.
	if _, err := c.DB.Exec(`UPDATE agent_sessions SET child_conversation_id=NULL WHERE id='mirror-launch-session'; DELETE FROM conversation_identity_links`); err != nil {
		t.Fatal(err)
	}
	if alias, _ := snapshot("native").resolveAlias("mirror-launch-session"); alias == "child-session" {
		t.Fatal("an unmirrored launch must not join by call ID alone")
	}
}
