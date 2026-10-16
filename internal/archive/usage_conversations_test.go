package archive

import (
	"testing"
	"time"
)

func TestUsageWholeConversationOwnership(t *testing.T) {
	catalog, _ := testCatalog(t)
	run := func(sql string, args ...any) {
		t.Helper()
		if _, err := catalog.DB.Exec(sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, workspace := range []struct{ id, source string }{{"native", "codex"}, {"mirror", "conductor"}, {"outside", "claude"}} {
		run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,?,'local',?,?, '2026-10-15')`, workspace.id, workspace.source, workspace.id, workspace.id)
	}
	run(`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('root-repo','Root repository','2026-10-15','2026-10-15'),('child-repo','Child repository','2026-10-15','2026-10-15')`)
	run(`UPDATE workspaces SET repository_id='root-repo' WHERE id='native'`)
	run(`UPDATE workspaces SET repository_id='child-repo' WHERE id='outside'`)
	for _, cv := range []struct {
		id, workspace, model, effort, parent string
		depth                                int
	}{
		{"root", "native", "gpt-6-sol", "high", "", 0},
		{"mirror-root", "mirror", "gpt-6-sol", "high", "", 0},
		{"independent", "native", "gpt-6-astra", "low", "", 0},
		{"child", "native", "gpt-6-astra", "low", "root", 1},
		{"nested", "outside", "claude-opus-5-5", "medium", "child", 2},
		{"handle-child", "outside", "claude-opus-5-5", "low", "", 1},
		{"launched", "outside", "claude-opus-5-5", "medium", "", 0},
		{"continuation", "native", "gpt-6-astra", "low", "root", 0},
		{"child-continuation", "native", "gpt-6-astra", "low", "child", 0},
		{"orphan", "outside", "claude-opus-5-5", "low", "", 1},
		{"cycle-a", "native", "gpt-6-sol", "high", "", 0},
		{"cycle-b", "native", "gpt-6-sol", "high", "cycle-a", 1},
		{"self-cycle", "native", "gpt-6-sol", "high", "", 0},
	} {
		provider := "codex"
		if cv.workspace == "outside" {
			provider = "claude"
		}
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,effort,parent_id,agent_depth) VALUES(?,?,?,'local',?,?,?,?,?)`, cv.id, cv.workspace, provider, cv.id, cv.model, cv.effort, nilIfEmpty(cv.parent), cv.depth)
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,effort,started_at,input_tokens,total_tokens) VALUES(?,?,?,'main','root',?,?,?,'2026-10-14T10:00:00Z',1000,1000)`, "s-"+cv.id, cv.workspace, cv.id, provider, cv.model, cv.effort)
	}
	run(`UPDATE conversations SET model=NULL,effort=NULL WHERE id='independent'`)
	run(`UPDATE conversations SET parent_id='cycle-b' WHERE id='cycle-a'`)
	run(`UPDATE conversations SET parent_id='self-cycle' WHERE id='self-cycle'`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider,model,effort,started_at,input_tokens,total_tokens) VALUES('embedded','native','root','embedded','s-root','subagent','codex','gpt-6-astra','low','2026-10-14T10:00:00Z',2000,2000)`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider,model,started_at,input_tokens,total_tokens) VALUES('embedded-child','native','child','embedded-child','s-child','subagent','codex','gpt-6-astra','2026-10-14T10:00:00Z',100,100)`)
	// The transcript flattened nested's parent to child; the exact execution
	// handle shows that an embedded child actually launched it one level deeper.
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,kind,provider) VALUES('nested-handle','native','child','nested-handle','embedded-child','nested','subagent','codex')`)
	// An exact delegation handle can link a conversation with no transcript parent.
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,kind,provider) VALUES('handle','native','child','handle','s-child','handle-child','subagent','codex')`)
	executionCycles := []struct{ id, parent string }{{"self-parent", "self-parent"}, {"embedded-cycle-a", "embedded-cycle-b"}, {"embedded-cycle-b", "embedded-cycle-a"}, {"below-cycle", "embedded-cycle-a"}}
	for _, session := range executionCycles {
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES(?,'native','root',?,'subagent','codex')`, session.id, session.id)
	}
	for _, session := range executionCycles {
		run(`UPDATE agent_sessions SET parent_id=? WHERE id=?`, session.parent, session.id)
	}
	run(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('root','mirror-root','mirror',1,'{}')`)
	// A launch known only to the suppressed mirror still belongs to the retained root.
	run(`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('launched','mirror-root','launch-call','claude')`)
	if err := catalog.seedSessionUsage(); err != nil {
		t.Fatal(err)
	}
	// Several facts for a run must remain separate facts, with the same owner.
	run(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,input_tokens,uncached_input_tokens,total_tokens) VALUES('s-root','2026-10-15T10:00:00Z','gpt-6-astra','low','request',3000,3000,3000)`)
	ledger, err := catalog.representativeLedger()
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := catalog.usageConversationRoots(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if lineage.delegatedRuns["handle"] != lineage.delegatedRuns["s-handle-child"] || lineage.delegatedRuns["handle"] == "" {
		t.Fatal("an exact handle and its transcript must name one delegated run")
	}
	for _, session := range executionCycles {
		if _, known := lineage.delegatedDepth[session.id]; known {
			t.Fatalf("cyclic execution ancestry must have unavailable depth: %s", session.id)
		}
	}
	expectedDelegates := map[string]string{
		"embedded": "agent:embedded", "s-child": "conversation:child", "s-child-continuation": "conversation:child",
		"embedded-child": "agent:embedded-child",
		"s-nested":       "conversation:nested", "s-handle-child": "conversation:handle-child", "s-launched": "conversation:launched",
	}
	expectedRoles := map[string]string{"s-root": "parent", "s-continuation": "parent", "s-child": "parent", "s-child-continuation": "parent", "s-independent": "singleton", "s-nested": "singleton", "s-handle-child": "singleton", "s-launched": "singleton", "embedded": "subagent", "embedded-child": "subagent"}
	expectedRunRoles := map[string]string{"s-root": "parent", "s-continuation": "parent", "s-independent": "standalone", "s-child": "delegated", "s-child-continuation": "delegated", "s-nested": "delegated", "s-handle-child": "delegated", "s-launched": "delegated", "embedded": "delegated", "embedded-child": "delegated"}
	expectedDepths := map[string]int64{"embedded": 1, "embedded-child": 2, "s-child": 1, "s-child-continuation": 1, "s-nested": 3, "s-handle-child": 2, "s-launched": 1}
	for _, hourly := range []bool{false, true} {
		rows, err := catalog.usageRowsByPeriod(time.UTC, hourly)
		if err != nil {
			t.Fatal(err)
		}
		counts, tokens := map[string]int{}, map[string]int64{}
		var allTokens int64
		for _, row := range rows {
			cv, root := firstString(row["conversation_id"]), firstString(row["root_conversation_id"])
			if cv == "mirror-root" {
				t.Fatal("mirror usage was counted twice")
			}
			counts[root]++
			tokens[root] += integer(row["total_tokens"])
			allTokens += integer(row["total_tokens"])
			if got, want := firstString(row["delegated_session_id"]), expectedDelegates[firstString(row["agent_session_id"])]; got != want {
				t.Fatalf("wrong delegated run identity: got %q want %q for %#v", got, want, row)
			}
			if got, want := firstString(row["session_role"]), expectedRoles[firstString(row["agent_session_id"])]; got != want {
				t.Fatalf("wrong session role: got %q want %q for %#v", got, want, row)
			}
			if got, want := firstString(row["run_role"]), expectedRunRoles[firstString(row["agent_session_id"])]; got != want {
				t.Fatalf("wrong canonical run role: got %q want %q for %#v", got, want, row)
			}
			if root != "" && integer(row["delegated_run_depth"]) != expectedDepths[firstString(row["agent_session_id"])] {
				t.Fatalf("wrong resolved execution depth: %#v", row)
			}
			if root != "" && (row["root_repository_name"] != "Root repository" || row["root_title"] != "native") {
				t.Fatalf("children must retain their root title/repository, including across workspaces (hourly=%v): %#v", hourly, row)
			}
			if root == "" && (row["root_repository_name"] != nil || row["root_title"] != nil) {
				t.Fatalf("unresolved ancestry must not invent task metadata: %#v", row)
			}
			if root == "root" && (row["root_model_family"] != "gpt-6-sol" || row["root_effort"] != "high" || row["root_provider"] != "codex") {
				t.Fatalf("delegated/changed model must keep root cohort: %#v", row)
			}
			if root == "independent" && (row["root_model_family"] != "gpt-6-astra" || row["root_effort"] != "low") {
				t.Fatalf("missing conversation configuration must fall back to its main run: %#v", row)
			}
			if root == "" && cv != "orphan" && cv != "cycle-a" && cv != "cycle-b" && cv != "self-cycle" {
				t.Fatalf("unexpected unresolved lineage: %#v", row)
			}
		}
		if counts["root"] != 10 || tokens["root"] != 12100 || counts["independent"] != 1 || tokens["independent"] != 1000 || counts[""] != 4 || allTokens != 17100 {
			t.Fatalf("whole conversation ownership must conserve facts once (hourly=%v): counts=%v tokens=%v total=%d", hourly, counts, tokens, allTokens)
		}
	}
}

func TestUsageSessionRoles(t *testing.T) {
	catalog, _ := testCatalog(t)
	run := func(sql string, args ...any) {
		t.Helper()
		if _, err := catalog.DB.Exec(sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('work','codex','local','work','Work','2026-10-15')`)
	for _, cv := range []struct{ id, parent string }{{"single", ""}, {"single-continuation", "single"}, {"parent", ""}, {"parent-continuation", "parent"}, {"launch-parent", ""}, {"launched", ""}} {
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES(?,'work','codex','local',?,?,0)`, cv.id, cv.id, nilIfEmpty(cv.parent))
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES(?,'work',?,'main','root','codex')`, "s-"+cv.id, cv.id)
	}
	// Neither child needs token usage to prove its root execution is a parent.
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider) VALUES('child','work','parent','child','s-parent','subagent','codex')`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider) VALUES('grandchild','work','parent','grandchild','child','subagent','codex')`)
	run(`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('launched','launch-parent','launch','codex')`)
	lineage, err := catalog.usageConversationRoots(nil)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"s-single": "singleton", "s-single-continuation": "singleton", "s-parent": "parent", "s-parent-continuation": "parent", "s-launch-parent": "parent", "s-launched": "singleton", "child": "subagent", "grandchild": "subagent"} {
		if got := lineage.sessionRoles[id]; got != want {
			t.Fatalf("role for %s: got %q want %q", id, got, want)
		}
	}
}
