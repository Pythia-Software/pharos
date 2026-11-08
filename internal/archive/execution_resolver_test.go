package archive

import (
	"context"
	"fmt"
	"maps"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Shared execution fixtures (docs/execution-identity-audit.md). Each case
// states the semantic answer: which executions exist, their parents and
// certainty, occurrence owners and conserved accounting, and checks it
// through the pure resolver and the production consumers built on it.

type executionFixtureConversation struct {
	id, workspace, parent, path, provider string
	depth                                 int
}

// executionFixture seeds conversations, each with its main session
// "s-<id>" reporting 100 tokens. Launches are added with fixtureLaunch.
func executionFixture(t *testing.T, conversations ...executionFixtureConversation) *Catalog {
	t.Helper()
	c, _ := testCatalog(t)
	tx := seedTx(t, c.DB)
	work := map[string]bool{}
	for i, cv := range conversations {
		w := cv.workspace
		if w == "" {
			w = "work"
		}
		provider := cv.provider
		if provider == "" {
			provider = "codex"
		}
		if !work[w] {
			if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, w, w, w); err != nil {
				t.Fatal(err)
			}
			work[w] = true
		}
		at := fmt.Sprintf("2026-01-01T00:%02d:00Z", i)
		if _, err := tx.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,agent_path,started_at,ended_at) VALUES(?,?,?,'local',?,?,?,?,?,?)`,
			cv.id, w, provider, cv.id, nilIfEmpty(cv.parent), cv.depth, cv.path, at, at); err != nil {
			t.Fatal(err)
		}
		kind := "root"
		if cv.depth > 0 {
			kind = "subagent"
		}
		if _, err := tx.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens,started_at,ended_at) VALUES(?,?,?,'main',?,?,'reported-reconciled',100,?,?)`,
			"s-"+cv.id, w, cv.id, kind, provider, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return c
}

func fixtureExec(t *testing.T, c *Catalog, q string, args ...any) {
	t.Helper()
	if _, err := c.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// fixtureLaunch records a launch call "m-<id>" in parent, issued by owner,
// with session id, an optional stored child link, result envelope and tokens.
func fixtureLaunch(t *testing.T, c *Catalog, id, parent, owner, child, result string, tokens int) {
	t.Helper()
	var w string
	if err := c.DB.QueryRow(`SELECT workspace_id FROM conversations WHERE id=?`, parent).Scan(&w); err != nil {
		t.Fatal(err)
	}
	body := `{"tool":"spawn_agent","input":{"task_name":"` + id + `"}}`
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash,created_at) VALUES(?,?,?,'assistant','delegation',?,?,?,'2026-01-01T00:00:10Z')`, "m-"+id, parent, "m-"+id, body, "call-"+id, id)
	status := "unavailable"
	if tokens > 0 {
		status = "reported-reconciled"
	}
	fixtureExec(t, c, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,delegation_message_id,kind,provider,usage_status,total_tokens,started_at) VALUES(?,?,?,?,?,?,?,'subagent','codex',?,?,'2026-01-01T00:00:10Z')`,
		id, w, parent, "call-"+id, owner, nilIfEmpty(child), "m-"+id, status, tokens)
	if result != "" {
		fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,?,?,'tool','delegation_result',?,?,?)`, "result-"+id, parent, "result-"+id, result, "call-"+id, "result-"+id)
	}
}

func fixtureResolution(t *testing.T, c *Catalog) *executionResolution {
	t.Helper()
	r, err := resolveExecutionsFrom(t.Context(), c.DB)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fixtureLineage(t *testing.T, c *Catalog) *usageConversationAttribution {
	t.Helper()
	l, err := c.usageConversationRoots(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func fixtureCounts(t *testing.T, c *Catalog) *quickStatsCounts {
	t.Helper()
	w := &usageSummaryWindow{Until: "2027-01-01T00:00:00Z"}
	if err := c.addQuickStatsCounts(t.Context(), map[string]any{"windows": []*usageSummaryWindow{w}}); err != nil {
		t.Fatal(err)
	}
	return w.quickStatsCounts
}

func confirmedExecutions(r *executionResolution) int { return len(r.Executions) }

func TestExecutionAuditCases(t *testing.T) {
	t.Run("acknowledged handle and its transcript are one execution", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", path: "/root/explore", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"content":"{\"task_name\":\"/root/explore\"}"}`, 0)
		r := fixtureResolution(t, c)
		if b := r.BindSession("launch"); b.State != bindingResolved || b.ExecutionID != "conversation:child" {
			t.Fatalf("launch binding: %+v", b)
		}
		if confirmedExecutions(r) != 2 || fixtureCounts(t, c).DelegatedRuns != 1 {
			t.Fatalf("one acknowledged agent counts once: executions=%d", confirmedExecutions(r))
		}
		l := fixtureLineage(t, c)
		if l.delegatedRuns["launch"] != l.delegatedRuns["s-child"] || l.delegatedParents["s-child"] != "conversation:root" {
			t.Fatalf("handle and transcript must share run and parent: %v %v", l.delegatedRuns, l.delegatedParents)
		}
	})
	t.Run("copied launch binds to the original attempt", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", path: "/root/explore", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
		fixtureLaunch(t, c, "copy", "child", "s-child", "", "", 0)
		fixtureExec(t, c, `UPDATE agent_sessions SET native_id='call-launch' WHERE id='copy'; UPDATE messages SET text=(SELECT text FROM messages WHERE id='m-launch'),call_id='call-launch' WHERE id='m-copy'`)
		r := fixtureResolution(t, c)
		if b := r.BindSession("copy"); b.State != bindingInherited || b.ExecutionID != "conversation:child" {
			t.Fatalf("copied launch: %+v", b)
		}
		if confirmedExecutions(r) != 2 || fixtureCounts(t, c).DelegatedRuns != 1 || len(r.Children("conversation:child")) != 0 {
			t.Fatal("copied history must not add executions")
		}
	})
	t.Run("rejected launch is an attempt", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"})
		fixtureLaunch(t, c, "failed", "root", "s-root", "", `{"content":"collab spawn failed: agent thread limit reached"}`, 0)
		r := fixtureResolution(t, c)
		if a := r.Attempts[sourceRef{sourceSession, "failed"}]; a == nil || a.Outcome != attemptRejected || a.Owner != "conversation:root" {
			t.Fatalf("attempt: %+v", a)
		}
		if fixtureCounts(t, c).DelegatedRuns != 0 || fixtureLineage(t, c).sessionRoles["s-root"] != "singleton" {
			t.Fatal("a rejected attempt must not count or make its issuer a parent")
		}
		// A task that fails after creation was still an execution.
		fixtureExec(t, c, `UPDATE messages SET text='{"agent_id":"handle","is_error":false,"content":"task failed"}' WHERE id='result-failed'`)
		if r := fixtureResolution(t, c); r.BindSession("failed").State != bindingResolved || r.Executions["agent:failed"].Presence != "verified-creation" {
			t.Fatalf("acknowledged launch whose task failed: %+v", r.BindSession("failed"))
		}
	})
	t.Run("launch call without an outcome stays unresolved", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"})
		fixtureLaunch(t, c, "pending", "root", "s-root", "", "", 0)
		r := fixtureResolution(t, c)
		if a := r.Attempts[sourceRef{sourceSession, "pending"}]; a == nil || a.Outcome != attemptUnresolved || fixtureCounts(t, c).DelegatedRuns != 0 {
			t.Fatalf("unresolved attempt: %+v", a)
		}
		// Observed stream activity proves the agent ran, tokens or not.
		fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('work','root','work','assistant','message','x','x');
			INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('pending','work',0)`)
		if r := fixtureResolution(t, c); r.BindSession("pending").State != bindingResolved || fixtureCounts(t, c).DelegatedRuns != 1 {
			t.Fatal("a tokenless agent with observed activity is an execution")
		}
	})
	t.Run("acknowledgement nests a flattened transcript parent", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1}, executionFixtureConversation{id: "nested", parent: "root", depth: 2})
		fixtureLaunch(t, c, "launch", "root", "s-root", "child", "", 0)
		fixtureLaunch(t, c, "nested-launch", "child", "s-child", "", `{"agent_id":"nested"}`, 0)
		r := fixtureResolution(t, c)
		x := r.Executions["conversation:nested"]
		if x.Parent.State != parentResolved || x.Parent.ParentID != "conversation:child" || r.Ancestry(x.ID).Depth != 2 {
			t.Fatalf("nested parent: %+v ancestry %+v", x.Parent, r.Ancestry(x.ID))
		}
		if l := fixtureLineage(t, c); l.delegatedDepth["s-nested"] != 2 || l.delegatedParents["s-nested"] != "conversation:child" {
			t.Fatalf("analytics depth: %v %v", l.delegatedDepth, l.delegatedParents)
		}
	})
	t.Run("continuation is a segment of the same execution", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "continued", parent: "root"})
		r := fixtureResolution(t, c)
		x := r.Executions["conversation:root"]
		if r.BindConversation("continued").ExecutionID != x.ID || !slices.Equal(x.Segments, []string{"root", "continued"}) || len(r.Children(x.ID)) != 0 {
			t.Fatalf("continuation: %+v", x)
		}
		if l := fixtureLineage(t, c); l.delegatedDepth["s-continued"] != 0 || l.runs["continued"] != "" || fixtureCounts(t, c).DelegatedRuns != 0 {
			t.Fatal("a continuation adds no delegation")
		}
	})
	t.Run("siblings keep identity and order from every reader", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root", workspace: "root-work"},
			executionFixtureConversation{id: "first", workspace: "first-work", parent: "root", depth: 1},
			executionFixtureConversation{id: "second", workspace: "second-work", parent: "root", depth: 1})
		r := fixtureResolution(t, c)
		want := []executionID{"conversation:root", "conversation:first", "conversation:second"}
		for _, from := range want {
			if family, complete := r.Family(from, 0); !complete || !slices.Equal(family, want) {
				t.Fatalf("family from %s: %v complete=%v", from, family, complete)
			}
		}
		if !r.Less("conversation:first", "conversation:second") || r.Less("conversation:second", "conversation:first") {
			t.Fatal("sibling order must not depend on the reader")
		}
	})
	t.Run("equally explicit parents are ambiguous", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "a"}, executionFixtureConversation{id: "b"}, executionFixtureConversation{id: "child", parent: "a", depth: 1})
		fixtureLaunch(t, c, "a-launch", "a", "s-a", "child", "", 0)
		fixtureLaunch(t, c, "b-launch", "b", "s-b", "child", "", 0)
		r := fixtureResolution(t, c)
		x := r.Executions["conversation:child"]
		if x.Parent.State != parentAmbiguous || x.Parent.ParentID != "" || len(x.Parent.Candidates) < 2 {
			t.Fatalf("conflicting exact parents: %+v", x.Parent)
		}
		if a := r.Ancestry(x.ID); a.Known || fixtureLineage(t, c).unresolved["child"] != "conflict" {
			t.Fatalf("ambiguous ancestry must stay unknown: %+v", a)
		}
	})
	t.Run("missing ancestry is not an observed root", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "orphan", depth: 2})
		r := fixtureResolution(t, c)
		if x := r.Executions["conversation:orphan"]; x.Parent.State != parentMissing || r.Ancestry(x.ID).Known {
			t.Fatalf("orphan: %+v", x.Parent)
		}
		if l := fixtureLineage(t, c); l.roots["orphan"] != nil || l.unresolved["orphan"] != "missing_parent" {
			t.Fatal("an orphan has no known root")
		}
	})
	t.Run("acknowledgement recovers a missing transcript parent across workspaces", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root", workspace: "root-work"}, executionFixtureConversation{id: "child", workspace: "child-work", path: "/root/explore", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
		r := fixtureResolution(t, c)
		if x := r.Executions["conversation:child"]; x.Parent.ParentID != "conversation:root" || r.BindSession("launch").ExecutionID != x.ID {
			t.Fatalf("exact handle: %+v", x.Parent)
		}
	})
	t.Run("explicit child links join family discovery", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root", workspace: "root-work"}, executionFixtureConversation{id: "child", workspace: "child-work", depth: 1},
			executionFixtureConversation{id: "grandchild", workspace: "grand-work", parent: "child", depth: 2})
		fixtureLaunch(t, c, "launch", "root", "s-root", "child", "", 0)
		r := fixtureResolution(t, c)
		family, complete := r.Family("conversation:root", 0)
		if !complete || !slices.Equal(family, []executionID{"conversation:root", "conversation:child", "conversation:grandchild"}) {
			t.Fatalf("family: %v", family)
		}
	})
	t.Run("independent usage stays separate accounting for one execution", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "child", "", 10)
		r := fixtureResolution(t, c)
		x := r.Executions["conversation:child"]
		if r.BindSession("launch").ExecutionID != x.ID || !slices.Contains(x.Streams, sourceRef{sourceSession, "launch"}) || confirmedExecutions(r) != 2 {
			t.Fatalf("identity: %+v", x)
		}
		if err := c.seedSessionUsage(); err != nil {
			t.Fatal(err)
		}
		var total, streams int
		if err := c.DB.QueryRow(`SELECT SUM(total_tokens),COUNT(DISTINCT agent_session_id) FROM agent_session_usage`).Scan(&total, &streams); err != nil {
			t.Fatal(err)
		}
		if total != 210 || streams != 3 {
			t.Fatalf("both usage streams must be preserved: total=%d streams=%d", total, streams)
		}
	})
	t.Run("copied tool call counts once at its original", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1})
		for _, cv := range []string{"root", "child"} {
			fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash,created_at) VALUES(?,?,'call_7f3a9c2e','assistant','delegation','{"input":{"task_name":"explore"}}','same-call','same-body','2026-01-01T00:00:10Z')`, "copied-"+cv, cv)
			fixtureExec(t, c, `INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES(?,?,0)`, "s-"+cv, "copied-"+cv)
			fixtureExec(t, c, `INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,call_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at,duration_ms) VALUES(?,'work',?,?,?,0,'codex','delegation','spawn_agent','delegation','ok','2026-01-01T00:00:10Z','2026-01-01T00:00:11Z',1000)`, "tool-"+cv, cv, "s-"+cv, "copied-"+cv)
		}
		r := fixtureResolution(t, c)
		o, err := r.Occurrences(t.Context(), c.DB, []string{"child"})
		if err != nil {
			t.Fatal(err)
		}
		got := o.Lookup(sourceRef{sourceMessage, "copied-child"}, sourceRef{sourceSession, "s-child"})
		if got.Role != occurrenceInherited || got.Owner != "conversation:root" || got.Navigate != (sourceRef{sourceMessage, "copied-root"}) || o.Counts(got.Observation, sourceRef{sourceSession, "s-child"}) {
			t.Fatalf("copied occurrence: %+v", got)
		}
		if root := o.Lookup(sourceRef{sourceMessage, "copied-root"}, sourceRef{sourceSession, "s-root"}); root.Role != occurrenceOriginal || root.Owner != "conversation:root" {
			t.Fatalf("original occurrence: %+v", root)
		}
		q := fixtureCounts(t, c)
		// The proven copy counts once; the child has no typed evidence, so a
		// copy whose original is gone could still hide in it: partial.
		if q.ToolCalls != 1 || q.ToolCallObservations != 2 || q.PartialConversations != 1 || q.OccurrenceCoverage != "partial" || q.ToolTime == nil || *q.ToolTime != 1000 {
			t.Fatalf("one physical call: %+v", q)
		}
	})
	t.Run("cycles stay unresolved", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "a", depth: 1}, executionFixtureConversation{id: "b", parent: "a", depth: 2})
		fixtureExec(t, c, `UPDATE conversations SET parent_id='b' WHERE id='a'`)
		r := fixtureResolution(t, c)
		for _, id := range []executionID{"conversation:a", "conversation:b"} {
			if x := r.Executions[id]; x.Parent.State != parentCycle || r.Ancestry(id).Known {
				t.Fatalf("%s: %+v", id, x.Parent)
			}
		}
		if l := fixtureLineage(t, c); l.unresolved["a"] != "cycle" || l.unresolved["b"] != "cycle" {
			t.Fatalf("analytics: %v", l.unresolved)
		}
	})
	t.Run("handles resolve only inside their namespace", func(t *testing.T) {
		for _, native := range []string{"other", "unrelated:subagent:agent-handle"} {
			c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "other", depth: 1})
			fixtureExec(t, c, `UPDATE conversations SET native_id=?,provider='other',account='other' WHERE id='other'`, native)
			fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"details":{"agentId":"handle"},"agent_id":"other"}`, 0)
			if b := fixtureResolution(t, c).BindSession("launch"); b.ExecutionID == "conversation:other" {
				t.Fatalf("%s: acknowledgement crossed provider/account", native)
			}
		}
	})
	t.Run("report prose is not an acknowledgement", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root", provider: "claude"}, executionFixtureConversation{id: "actor", parent: "root", depth: 1, provider: "claude"},
			executionFixtureConversation{id: "target", parent: "root", depth: 1, provider: "claude"})
		fixtureExec(t, c, `UPDATE conversations SET native_id='root:subagent:agent-target' WHERE id='target'; UPDATE conversations SET native_id='root:subagent:agent-actor' WHERE id='actor'`)
		fixtureLaunch(t, c, "report", "actor", "s-actor", "", `{"content":"During review I compared agentId: target with the old logs."}`, 0)
		r := fixtureResolution(t, c)
		if r.BindSession("report").ExecutionID == "conversation:target" || r.Executions["conversation:target"].Parent.ParentID == "conversation:actor" {
			t.Fatal("report prose became ownership evidence")
		}
		// The stored root parent may be Claude's fallback: the family is known,
		// the direct parent is not.
		target := r.Executions["conversation:target"]
		if a := r.Ancestry(target.ID); target.Parent.State != parentMissing || a.Root != "conversation:root" || a.Known {
			t.Fatalf("unverified root parent: %+v %+v", target.Parent, a)
		}
		if l := fixtureLineage(t, c); l.roots["target"] == nil || firstString(l.roots["target"]["id"]) != "root" {
			t.Fatal("family membership keeps the root for analytics")
		}
	})
	t.Run("copied launch across an intermediate compaction", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "middle", parent: "root", depth: 1}, executionFixtureConversation{id: "leaf", parent: "middle", depth: 2})
		fixtureLaunch(t, c, "original", "root", "s-root", "middle", "", 0)
		fixtureLaunch(t, c, "copy", "leaf", "s-leaf", "", "", 0)
		fixtureExec(t, c, `UPDATE agent_sessions SET native_id='call-original' WHERE id='copy'; UPDATE messages SET text=(SELECT text FROM messages WHERE id='m-original'),call_id='call-original',native_id='m-original' WHERE id='m-copy'`)
		r := fixtureResolution(t, c)
		if b := r.BindSession("copy"); b.State != bindingInherited || b.ExecutionID != "conversation:middle" || len(r.Children("conversation:leaf")) != 0 {
			t.Fatalf("grandparent's copied launch: %+v", b)
		}
	})
	t.Run("continuation metadata does not erase a verified delegation", func(t *testing.T) {
		// A depth-0 Codex transcript with a parent reads as a continuation, but
		// the parent's acknowledgement says the launch created it.
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root"})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
		r := fixtureResolution(t, c)
		if x := r.Executions["conversation:child"]; x == nil || x.Parent.ParentID != "conversation:root" || r.BindConversation("root").ExecutionID == x.ID {
			t.Fatalf("delegation erased: %v", executionSummary(r))
		}
		if fixtureCounts(t, c).DelegatedRuns != 1 || fixtureLineage(t, c).delegatedParents["s-child"] != "conversation:root" {
			t.Fatal("analytics must keep the delegation")
		}
		// Provider metadata asserting the same continuation natively is as
		// authoritative as the acknowledgement: cardinality stays unresolved.
		saved := executionClaimSources
		t.Cleanup(func() { executionClaimSources = saved })
		executionClaimSources = append(slices.Clone(saved), func(_ context.Context, _ queryer, e *executionEvidence) error {
			e.Claims = append(e.Claims, executionClaim{Kind: claimContinuation, Subject: sourceRef{sourceConversation, "child"}, Object: sourceRef{sourceConversation, "root"},
				Strength: evidenceNative, Namespace: "codex/local", Rule: "test-native-continuation"})
			return nil
		})
		r = fixtureResolution(t, c)
		if len(r.Conflicts) == 0 || r.Executions["conversation:child"] == nil || r.Executions["conversation:child"].Parent.State != parentConflict {
			t.Fatalf("native contradiction: %v", executionSummary(r))
		}
		// The hook changes no rows, so read past the per-generation cache.
		l, err := usageConversationRootsFrom(c.DB, nil)
		if err != nil {
			t.Fatal(err)
		}
		if l.unresolved["child"] != "conflict" || l.unresolved["root"] != "conflict" {
			t.Fatalf("analytics must not choose a cardinality: %v", l.unresolved)
		}
	})
	t.Run("current typed evidence replaces the legacy reading", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
		r := fixtureResolution(t, c)
		if r.BindSession("launch").ExecutionID != "conversation:child" || r.Executions["conversation:child"].Evidence != "legacy" {
			t.Fatalf("legacy acknowledgement: %+v", r.Executions["conversation:child"])
		}
		// Typed extraction found no structured outcome in this transcript.
		fixtureExec(t, c, `CREATE TABLE IF NOT EXISTS execution_evidence_state(conversation_id TEXT PRIMARY KEY,version TEXT NOT NULL,input TEXT NOT NULL,id_policy TEXT NOT NULL)`)
		fixtureExec(t, c, `INSERT INTO execution_evidence_state VALUES('root',?,'','')`, executionEvidenceVersion)
		r = fixtureResolution(t, c)
		if b := r.BindSession("launch"); b.State != bindingAttempt {
			t.Fatalf("legacy reading overrode current typed evidence: %+v", b)
		}
		if root, child := r.Executions["conversation:root"].Evidence, r.Executions["conversation:child"].Evidence; root != "typed" || child != "legacy" {
			t.Fatalf("evidence kinds root=%s child=%s", root, child)
		}
	})
	t.Run("a stored session fallback parent is a hint", func(t *testing.T) {
		// Copilot stores the session as parent when the call that started an
		// agent is not found (adapter_copilot.go).
		c := executionFixture(t, executionFixtureConversation{id: "session", provider: "copilot"},
			executionFixtureConversation{id: "nested", parent: "session", depth: 1, provider: "copilot"})
		fixtureExec(t, c, `INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id) VALUES('nested','k',1,'test','session')`)
		r := fixtureResolution(t, c)
		x := r.Executions["conversation:nested"]
		if a := r.Ancestry(x.ID); x.Parent.State != parentMissing || a.Known || a.Root != "conversation:session" {
			t.Fatalf("fallback parent confirmed: %+v %+v", x.Parent, a)
		}
		// A launch that names the agent corroborates the parent.
		fixtureLaunch(t, c, "start", "session", "s-session", "nested", "", 0)
		fixtureExec(t, c, `UPDATE agent_sessions SET provider='copilot' WHERE id='start'`)
		if x := fixtureResolution(t, c).Executions["conversation:nested"]; x.Parent.State != parentResolved || x.Parent.ParentID != "conversation:session" {
			t.Fatalf("corroborated parent: %+v", x.Parent)
		}
	})
	t.Run("a retained parent reference names a namespace only when recorded", func(t *testing.T) {
		// An import's observation names the parent's native ID without its
		// namespace; a Claude child names the Codex thread that launched it.
		c := executionFixture(t, executionFixtureConversation{id: "p"}, executionFixtureConversation{id: "imported", workspace: "import", depth: 1},
			executionFixtureConversation{id: "claude-child", provider: "claude", depth: 1})
		fixtureExec(t, c, `UPDATE workspaces SET source_kind='canonical' WHERE id='import';
			INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id) VALUES('imported','export',1,'test','p');
			INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id,parent_provider,parent_account) VALUES('claude-child','export',1,'test','p','codex','local')`)
		r := fixtureResolution(t, c)
		for _, id := range []string{"imported", "claude-child"} {
			if x := r.Executions[executionID("conversation:"+id)]; x.Parent.State != parentResolved || x.Parent.ParentID != "conversation:p" || x.Parent.Certainty != evidenceLegacy {
				t.Fatalf("%s: %+v", id, x.Parent)
			}
		}
		// The same native ID in another namespace: either could be meant.
		fixtureExec(t, c, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('p-elsewhere','work','cursor','local','p')`)
		if x := fixtureResolution(t, c).Executions["conversation:imported"]; x.Parent.State != parentAmbiguous {
			t.Fatalf("an unrecorded namespace picked one of two parents: %+v", x.Parent)
		}
	})
	t.Run("a child retaining the call that spawned it, across workspaces", func(t *testing.T) {
		// The reported shape: the parent's launch is acknowledged as a child
		// in another workspace, whose transcript retains the spawn call.
		for _, proof := range []bool{true, false} {
			c := executionFixture(t, executionFixtureConversation{id: "parent", workspace: "parent-work"},
				executionFixtureConversation{id: "child", workspace: "child-work", parent: "parent", depth: 1, path: "/root/explore"})
			fixtureLaunch(t, c, "launch", "parent", "s-parent", "", `{"content":"{\"task_name\":\"/root/explore\"}"}`, 0)
			fixtureLaunch(t, c, "copy", "child", "s-child", "", "", 0)
			fixtureExec(t, c, `UPDATE agent_sessions SET native_id='call-launch' WHERE id='copy';
				UPDATE messages SET call_id='call-launch',text=(SELECT text FROM messages WHERE id='m-launch') WHERE id='m-copy';
				INSERT INTO tool_calls(id,workspace_id,conversation_id,call_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at) VALUES
				('t-launch','parent-work','parent','m-launch',0,'codex','delegation','spawn_agent','delegation','ok','2026-01-01T00:00:10Z'),
				('t-copy','child-work','child','m-copy',0,'codex','delegation','spawn_agent','delegation','ok','2026-01-01T00:00:10Z')`)
			// The child's own work, and one copied message of the parent's
			// history, identified by a provider event ID or by a line locator.
			native := "evt_hello"
			if !proof {
				native = "line-4"
			}
			fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
				('p-hello','parent',?,'user','message','hello','h','2026-01-01T00:00:01Z'),('c-hello','child',?,'user','message','hello','h','2026-01-01T00:00:01Z'),
				('c-own','child','evt_own','assistant','message','found it','o','2026-01-01T00:01:00Z')`, native, native)
			r := fixtureResolution(t, c)
			child := r.BindConversation("child").ExecutionID
			holding := 0
			for _, x := range r.Executions {
				if slices.Contains(x.Segments, "child") {
					holding++
				}
			}
			if holding != 1 || r.BindSession("launch").ExecutionID != child || r.BindSession("s-child").ExecutionID != child {
				t.Fatalf("proof=%v: one child execution: holding=%d launch=%+v", proof, holding, r.BindSession("launch"))
			}
			if b := r.BindSession("copy"); b.State != bindingInherited || b.ExecutionID != child {
				t.Fatalf("proof=%v: the retained spawn is the parent's call: %+v", proof, b)
			}
			// One physical spawn, owned by the caller.
			o, err := r.Occurrences(t.Context(), c.DB, []string{"parent", "child"})
			if err != nil {
				t.Fatal(err)
			}
			parent := r.BindConversation("parent").ExecutionID
			if occ := o.Lookup(sourceRef{sourceMessage, "m-launch"}, sourceRef{sourceConversation, "parent"}); occ.Role != occurrenceOriginal || occ.Owner != parent {
				t.Fatalf("proof=%v: original spawn %+v", proof, occ)
			}
			if occ := o.Lookup(sourceRef{sourceMessage, "m-copy"}, sourceRef{sourceConversation, "child"}); occ.Role != occurrenceInherited || occ.Owner != parent || occ.Navigate.ID != "m-launch" {
				t.Fatalf("proof=%v: retained spawn %+v", proof, occ)
			}
			// No copied self activity: the copy counts once, at the parent;
			// unproven history is counted but marked. Without typed evidence
			// the child's history is never complete either way.
			counts := fixtureCounts(t, c)
			want := int64(2) // p-hello and c-own; c-hello is a proven copy
			if !proof {
				want = 3 // counted, with partial coverage
			}
			if counts.ToolCalls != 1 || counts.ToolCallObservations != 2 || counts.Messages != want || counts.OccurrenceCoverage != "partial" || o.Partial("child") != "untyped-ancestry/v1" {
				t.Fatalf("proof=%v: counts %+v", proof, counts)
			}
			hello := o.Lookup(sourceRef{sourceMessage, "c-hello"}, sourceRef{sourceConversation, "child"})
			if proof != (hello.Role == occurrenceInherited) {
				t.Fatalf("proof=%v: copied history %+v partial=%q", proof, hello, o.Partial("child"))
			}
			// Identity, family and order do not depend on the reader.
			var answers []string
			for _, kept := range []string{"parent-work", "child-work"} {
				lineage, err := usageConversationRootsFrom(c.DB, []map[string]any{{"workspace_id": kept}})
				if err != nil {
					t.Fatal(err)
				}
				answers = append(answers, fmt.Sprint(lineage.roots["child"]["id"], lineage.runs["child"], lineage.delegatedRuns["launch"], lineage.delegatedDepth["launch"], lineage.delegatedParents["launch"]))
			}
			if answers[0] != answers[1] || r.Executions[child].Label.Text != "launch" || r.Executions[child].Parent.ParentID != parent {
				t.Fatalf("proof=%v: reader-dependent or wrong identity: %v %+v", proof, answers, r.Executions[child])
			}
		}
	})
	t.Run("a copied launch outside the ancestry walk is not a second owner", func(t *testing.T) {
		// The fork retains root's launch call and acknowledgement, with a body
		// normalized differently, so only the shared call ID and the child
		// both acknowledge link them.
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", depth: 1},
			executionFixtureConversation{id: "fork", parent: "root", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
		fixtureLaunch(t, c, "copy", "fork", "s-fork", "", `{"agent_id":"child"}`, 0)
		fixtureExec(t, c, `UPDATE agent_sessions SET native_id='call-launch' WHERE id='copy'; UPDATE messages SET call_id='call-launch',text='{"tool":"spawn_agent"}' WHERE id='m-copy';
			UPDATE messages SET call_id='call-launch' WHERE id='result-copy'`)
		r := fixtureResolution(t, c)
		if x := r.Executions["conversation:child"]; x.Parent.State != parentResolved || x.Parent.ParentID != "conversation:root" {
			t.Fatalf("child parent: %+v", x.Parent)
		}
		if b := r.BindSession("copy"); b.State != bindingInherited || b.ExecutionID != "conversation:child" {
			t.Fatalf("copied launch: %+v", b)
		}
		// Unrelated transcripts sharing a call ID prove nothing: still ambiguous.
		fixtureExec(t, c, `UPDATE conversations SET parent_id=NULL,agent_depth=0 WHERE id='fork'`)
		if x := fixtureResolution(t, c).Executions["conversation:child"]; x.Parent.State != parentAmbiguous {
			t.Fatalf("unrelated duplicate call: %+v", x.Parent)
		}
	})
	t.Run("a copied launch with its own observed activity is an execution", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", path: "/root/explore", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"child"}`, 0)
		fixtureLaunch(t, c, "copy", "child", "s-child", "", "", 0)
		fixtureExec(t, c, `UPDATE agent_sessions SET native_id='call-launch' WHERE id='copy'; UPDATE messages SET text=(SELECT text FROM messages WHERE id='m-launch'),call_id='call-launch' WHERE id='m-copy';
			INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('own','child','own','assistant','message','x','x');
			INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('copy','own',0)`)
		if b := fixtureResolution(t, c).BindSession("copy"); b.State != bindingResolved || b.ExecutionID == "conversation:child" {
			t.Fatalf("observed activity discarded as a copy: %+v", b)
		}
	})
	t.Run("ambiguous acknowledgements do not merge", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "a", parent: "root", path: "/root/review", depth: 1},
			executionFixtureConversation{id: "b", parent: "root", path: "/root/review", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"task_name":"/root/review"}`, 0)
		if b := fixtureResolution(t, c).BindSession("launch"); b.State != bindingAttempt {
			t.Fatalf("ambiguous handle: %+v", b)
		}
	})
}

// executionSummary is a comparable rendering of a resolution's decisions.
func executionSummary(r *executionResolution) map[string]string {
	out := map[string]string{}
	for id, x := range r.Executions {
		a := r.Ancestry(id)
		out[string(id)] = fmt.Sprintf("%s|%s|%v|%d|%s|%s|%v|%v|%v", x.Parent.State, x.Parent.ParentID, a.Known, a.Depth, a.Root, x.Label.Text, x.Streams, x.Segments, r.Children(id))
	}
	for ref, b := range r.bindings {
		out["bind:"+ref.String()] = fmt.Sprintf("%s|%s", b.State, b.ExecutionID)
	}
	return out
}

func pureStream(kind sourceKind, id, conversation string) executionStream {
	return executionStream{Ref: sourceRef{kind, id}, ConversationID: conversation, Provider: "codex", Account: "local", NativeID: id, SourcePosition: -1}
}

func claimOf(kind claimKind, subject, object sourceRef, strength evidenceStrength) executionClaim {
	return executionClaim{Kind: kind, Subject: subject, Object: object, Strength: strength, Rule: "test"}
}

func TestExecutionResolverIdentityConstraints(t *testing.T) {
	conv := func(id string) sourceRef { return sourceRef{sourceConversation, id} }
	launch := sourceRef{sourceSession, "launch"}
	base := func() *executionEvidence {
		l := pureStream(sourceSession, "launch", "a")
		l.Launch = true
		return &executionEvidence{
			Streams: []executionStream{pureStream(sourceConversation, "a", "a"), pureStream(sourceConversation, "b", "b"), l},
			Claims: []executionClaim{
				claimOf(claimLaunchOwner, launch, conv("a"), evidenceDerived),
				claimOf(claimLaunchAccepted, launch, conv("b"), evidenceDerived),
			},
		}
	}
	// The acknowledgement and launch ownership are parser-derived, as real
	// extraction makes them, yet they record a verified creation.
	for _, strength := range []evidenceStrength{evidenceLegacy, evidenceDerived} {
		t.Run("weaker continuation cannot undo a verified delegation/"+strength.String(), func(t *testing.T) {
			e := base()
			e.Claims = append(e.Claims, claimOf(claimContinuation, conv("b"), conv("a"), strength))
			r := resolveExecutions(e)
			if len(r.Executions) != 2 || r.Executions["conversation:b"].Parent.ParentID != "conversation:a" || len(r.Conflicts) != 0 {
				t.Fatalf("delegation lost: %v", executionSummary(r))
			}
		})
	}
	for _, kind := range []claimKind{claimMirror, claimContinuation} {
		t.Run("native metadata contradicting a verified acknowledgement is a conflict/"+string(kind), func(t *testing.T) {
			e := base()
			e.Claims = append(e.Claims, claimOf(kind, conv("b"), conv("a"), evidenceNative))
			r := resolveExecutions(e)
			if len(r.Conflicts) != 1 {
				t.Fatalf("conflicts: %+v", r.Conflicts)
			}
			for _, id := range []executionID{"conversation:a", "conversation:b"} {
				if x := r.Executions[id]; x == nil || !x.Conflicted || x.Parent.State != parentConflict || r.Ancestry(id).Known {
					t.Fatalf("%s must keep unresolved cardinality: %v", id, executionSummary(r))
				}
			}
		})
	}
	t.Run("a conflict elsewhere in the component", func(t *testing.T) {
		// c mirrors b while also continuing a, each as authoritative as a's
		// creation of b: the chain a ≡ c ≡ b would make a delegate to itself.
		e := base()
		e.Streams = append(e.Streams, pureStream(sourceConversation, "c", "c"))
		e.Claims = append(e.Claims, claimOf(claimMirror, conv("c"), conv("b"), evidenceNative), claimOf(claimContinuation, conv("c"), conv("a"), evidenceNative))
		r := resolveExecutions(e)
		if len(r.Conflicts) == 0 || r.BindConversation("a").ExecutionID == r.BindConversation("b").ExecutionID {
			t.Fatalf("component conflict: %v", executionSummary(r))
		}
		for _, id := range []string{"a", "b", "c"} {
			if r.BindConversation(id).ExecutionID == "" {
				t.Fatalf("every source stays navigable: %s", id)
			}
		}
	})
	t.Run("one launch acknowledging two transcripts identifies neither", func(t *testing.T) {
		e := base()
		e.Streams = append(e.Streams, pureStream(sourceConversation, "c", "c"))
		e.Claims = append(e.Claims, claimOf(claimLaunchAccepted, launch, conv("c"), evidenceDerived))
		r := resolveExecutions(e)
		if r.BindConversation("b").ExecutionID == r.BindConversation("c").ExecutionID || len(r.Conflicts) != 1 {
			t.Fatalf("split acknowledgement merged transcripts: %v", executionSummary(r))
		}
	})
	// Precedence is across all of a launch's targets, in any claim order.
	shuffles := func(t *testing.T, e *executionEvidence, check func(*executionResolution)) {
		t.Helper()
		random := rand.New(rand.NewSource(11))
		for range 12 {
			shuffled := &executionEvidence{Streams: slices.Clone(e.Streams), Claims: slices.Clone(e.Claims)}
			random.Shuffle(len(shuffled.Streams), func(i, j int) { shuffled.Streams[i], shuffled.Streams[j] = shuffled.Streams[j], shuffled.Streams[i] })
			random.Shuffle(len(shuffled.Claims), func(i, j int) { shuffled.Claims[i], shuffled.Claims[j] = shuffled.Claims[j], shuffled.Claims[i] })
			check(resolveExecutions(shuffled))
		}
	}
	t.Run("a weaker link to another transcript joins nothing", func(t *testing.T) {
		e := base()
		e.Streams = append(e.Streams, pureStream(sourceConversation, "c", "c"))
		stale := claimOf(claimChildLink, launch, conv("c"), evidenceLegacy)
		e.Claims = append(e.Claims, stale)
		shuffles(t, e, func(r *executionResolution) {
			b, c := r.BindConversation("b").ExecutionID, r.BindConversation("c").ExecutionID
			if b == c || r.Bind(launch).ExecutionID != b || len(r.Conflicts) != 0 {
				t.Fatalf("stale link merged or conflicted: %v %+v", executionSummary(r), r.Conflicts)
			}
			if !slices.ContainsFunc(r.Bind(launch).Evidence, func(claim executionClaim) bool { return claim.Kind == claimChildLink && claim.Object == conv("c") }) {
				t.Fatalf("the rejected link is hidden from the launch's evidence: %+v", r.Bind(launch).Evidence)
			}
		})
	})
	t.Run("equally strong targets that are one execution identify it", func(t *testing.T) {
		e := base()
		e.Streams = append(e.Streams, pureStream(sourceConversation, "c", "c"))
		e.Claims = append(e.Claims, claimOf(claimLaunchAccepted, launch, conv("c"), evidenceDerived), claimOf(claimMirror, conv("c"), conv("b"), evidenceNative))
		shuffles(t, e, func(r *executionResolution) {
			b := r.BindConversation("b").ExecutionID
			if b == "" || r.BindConversation("c").ExecutionID != b || r.Bind(launch).ExecutionID != b || len(r.Conflicts) != 0 {
				t.Fatalf("aliases of one execution read as a conflict: %v %+v", executionSummary(r), r.Conflicts)
			}
		})
	})
	t.Run("equally strong targets that are two executions stay a conflict", func(t *testing.T) {
		e := base()
		e.Streams = append(e.Streams, pureStream(sourceConversation, "c", "c"))
		e.Claims = append(e.Claims, claimOf(claimLaunchAccepted, launch, conv("c"), evidenceDerived), claimOf(claimChildLink, launch, conv("d"), evidenceLegacy))
		shuffles(t, e, func(r *executionResolution) {
			if r.BindConversation("b").ExecutionID == r.BindConversation("c").ExecutionID || len(r.Conflicts) != 1 || r.Conflicts[0].Reason != "one launch acknowledges several transcripts" {
				t.Fatalf("split acknowledgement: %v %+v", executionSummary(r), r.Conflicts)
			}
		})
	})
	// The reviewed reproducer, through the stored rows before and after
	// backfill: the provider's acknowledgement names "actual", the stored child
	// link "stale". Until "actual" is retained the launch is a verified
	// execution of its own; once it arrives the launch identifies it.
	t.Run("the acknowledgement outranks a stale stored link before and after backfill", func(t *testing.T) {
		for _, backfill := range []string{"before arrival", "after arrival"} {
			c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "stale", parent: "root", depth: 1})
			fixtureLaunch(t, c, "launch", "root", "s-root", "stale", `{"agent_id":"actual"}`, 0)
			launch := sourceRef{sourceSession, "launch"}
			for _, phase := range []string{"missing", "arrived"} {
				if phase == "arrived" {
					fixtureExec(t, c, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at,ended_at) VALUES('actual','work','codex','local','actual','root',1,'2026-01-01T00:05:00Z','2026-01-01T00:05:00Z')`)
					fixtureExec(t, c, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens,started_at,ended_at) VALUES('s-actual','work','actual','main','subagent','codex','reported-reconciled',100,'2026-01-01T00:05:00Z','2026-01-01T00:05:00Z')`)
				}
				for _, step := range []string{"legacy", "backfilled"} {
					if step == "backfilled" || backfill == "before arrival" && phase == "arrived" {
						if _, err := c.BackfillExecutionEvidence(t.Context(), 10); err != nil {
							t.Fatal(err)
						}
					}
					if step == "legacy" && backfill == "before arrival" && phase == "arrived" {
						continue // already backfilled: no legacy reading remains
					}
					e, err := loadExecutionEvidence(t.Context(), c.DB)
					if err != nil {
						t.Fatal(err)
					}
					shuffles(t, e, func(r *executionResolution) {
						name := backfill + "/" + phase + "/" + step
						at, stale := r.Bind(launch), r.BindConversation("stale")
						if at.State == bindingAttempt || at.ExecutionID == "" || at.ExecutionID == stale.ExecutionID || stale.ExecutionID == "" || len(r.Conflicts) != 0 {
							t.Fatalf("%s: the launch must stay a verified execution apart from stale: launch=%+v stale=%+v %v", name, at, stale, executionSummary(r))
						}
						if x := r.Executions[at.ExecutionID]; x == nil || phase == "missing" && x.Presence != "verified-creation" {
							t.Fatalf("%s: a returned handle records a creation: %+v", name, x)
						}
						if actual := r.BindConversation("actual"); phase == "arrived" && actual.ExecutionID != at.ExecutionID {
							t.Fatalf("%s: the launch must identify the acknowledged transcript: launch=%+v actual=%+v", name, at, actual)
						}
						if !slices.ContainsFunc(at.Evidence, func(claim executionClaim) bool { return claim.Kind == claimChildLink && claim.Object == conv("stale") }) {
							t.Fatalf("%s: the rejected stored link is hidden from the launch's evidence: %+v", name, at.Evidence)
						}
					})
				}
			}
		}
	})
	// Derived outputs key on dependency digests, so a linked launch's result
	// is one of its launcher's inputs: correcting or deleting it changes them.
	t.Run("a linked launch's result is a dependency of its launcher", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "actual", parent: "root", depth: 1}, executionFixtureConversation{id: "stale", parent: "root", depth: 1})
		fixtureLaunch(t, c, "launch", "root", "s-root", "stale", `{"agent_id":"actual"}`, 0)
		seen := map[string]bool{}
		for _, step := range []struct{ label, write, bound string }{
			{"acknowledged", "", "actual"},
			{"corrected", `UPDATE messages SET text='{"agent_id":"stale"}',content_hash='corrected' WHERE id='result-launch'`, "stale"},
			{"deleted", `DELETE FROM messages WHERE id='result-launch'`, "stale"},
		} {
			if step.write != "" {
				fixtureExec(t, c, step.write)
			}
			digests, err := executionDependencyDigests(t.Context(), c.DB)
			if err != nil {
				t.Fatal(err)
			}
			if want := rereadDependencyDigests(t, c.DB); !reflect.DeepEqual(digests, want) {
				t.Fatalf("%s: patched digests %v, rows %v", step.label, digests, want)
			}
			if seen[digests["root"]] || step.label == "acknowledged" && digests["actual"] != digests["root"] {
				t.Fatalf("%s: the launcher's digest must follow its result: %v", step.label, digests)
			}
			seen[digests["root"]] = true
			r := fixtureResolution(t, c)
			if r.Bind(sourceRef{sourceSession, "launch"}).ExecutionID != r.BindConversation(step.bound).ExecutionID {
				t.Fatalf("%s: the launch must identify %s: %v", step.label, step.bound, executionSummary(r))
			}
		}
	})
	t.Run("synthesized fallback parent is a hint", func(t *testing.T) {
		e := &executionEvidence{Streams: []executionStream{pureStream(sourceConversation, "root", "root"), pureStream(sourceConversation, "agent", "agent")},
			Claims: []executionClaim{claimOf(claimTranscriptParent, conv("agent"), conv("root"), evidenceLegacy), claimOf(claimTranscriptParent, conv("agent"), conv("root"), evidenceSynthesized)}}
		r := resolveExecutions(e)
		if x := r.Executions["conversation:agent"]; x.Parent.State != parentMissing || len(x.Parent.Candidates) != 1 {
			t.Fatalf("known fallback must not confirm a parent: %+v", x.Parent)
		}
		// A corroborating launch resolves it.
		l := pureStream(sourceSession, "spawn", "root")
		l.Launch = true
		e.Streams = append(e.Streams, l)
		e.Claims = append(e.Claims, claimOf(claimLaunchOwner, l.Ref, conv("root"), evidenceNative), claimOf(claimLaunchAccepted, l.Ref, conv("agent"), evidenceDerived))
		if x := resolveExecutions(e).Executions["conversation:agent"]; x.Parent.State != parentResolved || x.Parent.Certainty != evidenceNative {
			t.Fatalf("corroborated parent: %+v", x.Parent)
		}
	})
}

func TestExecutionResolverIsOrderIndependent(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "a"}, executionFixtureConversation{id: "b"},
		executionFixtureConversation{id: "child", parent: "a", depth: 1}, executionFixtureConversation{id: "continued", parent: "root"},
		executionFixtureConversation{id: "nested", parent: "root", depth: 2})
	fixtureLaunch(t, c, "a-launch", "a", "s-a", "child", "", 0)
	fixtureLaunch(t, c, "b-launch", "b", "s-b", "child", "", 0)
	fixtureLaunch(t, c, "root-launch", "root", "s-root", "", `{"agent_id":"nested"}`, 0)
	fixtureLaunch(t, c, "failed", "root", "s-root", "", `{"content":"collab spawn failed: limit"}`, 0)
	e, err := loadExecutionEvidence(t.Context(), c.DB)
	if err != nil {
		t.Fatal(err)
	}
	// Two launches each acknowledged as two transcripts: two conflicts,
	// listed in the same order whatever the load order.
	conv := func(id string) sourceRef { return sourceRef{sourceConversation, id} }
	for launch, targets := range map[string][2]string{"a-launch": {"a", "b"}, "b-launch": {"continued", "nested"}} {
		for _, target := range targets {
			e.Claims = append(e.Claims, claimOf(claimLaunchAccepted, sourceRef{sourceSession, launch}, conv(target), evidenceDerived))
		}
	}
	summary := func(r *executionResolution) map[string]string {
		out := executionSummary(r)
		for i, conflict := range r.Conflicts {
			out[fmt.Sprintf("conflict:%d", i)] = fmt.Sprintf("%s|%v", conflict.Reason, conflict.Sources)
		}
		return out
	}
	want := summary(resolveExecutions(e))
	if want["conflict:1"] == "" {
		t.Fatalf("the fixture has fewer than two conflicts: %v", want)
	}
	random := rand.New(rand.NewSource(7))
	for range 20 {
		shuffled := &executionEvidence{Generation: e.Generation, Streams: slices.Clone(e.Streams), Claims: slices.Clone(e.Claims)}
		random.Shuffle(len(shuffled.Streams), func(i, j int) { shuffled.Streams[i], shuffled.Streams[j] = shuffled.Streams[j], shuffled.Streams[i] })
		random.Shuffle(len(shuffled.Claims), func(i, j int) { shuffled.Claims[i], shuffled.Claims[j] = shuffled.Claims[j], shuffled.Claims[i] })
		// Duplicate claims, as several sources may report, change nothing.
		shuffled.Claims = append(shuffled.Claims, shuffled.Claims[:3]...)
		if got := summary(resolveExecutions(shuffled)); !reflect.DeepEqual(got, want) {
			t.Fatalf("resolution depends on load order:\n got %v\nwant %v", got, want)
		}
	}
}

func TestExecutionResolverLateEvidenceAndAliases(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root", workspace: "root-work"}, executionFixtureConversation{id: "child", workspace: "child-work", depth: 1})
	fixtureLaunch(t, c, "launch", "root", "s-root", "", "", 0)
	before := fixtureResolution(t, c)
	if before.BindSession("launch").State != bindingAttempt || before.Executions["conversation:child"].Parent.State != parentMissing {
		t.Fatal("without an acknowledgement the launch is an attempt and the child's parent unknown")
	}
	// The acknowledgement arrives in the parent's transcript; the child's own
	// rows do not change, yet its identity and ancestry do.
	var childRows string
	_ = c.DB.QueryRow(`SELECT json_group_array(json_array(id,parent_id,agent_depth)) FROM conversations WHERE id='child'`).Scan(&childRows)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES('result-launch','root','result-launch','tool','delegation_result','{"agent_id":"child"}','call-launch','r')`)
	after := fixtureResolution(t, c)
	var unchanged string
	_ = c.DB.QueryRow(`SELECT json_group_array(json_array(id,parent_id,agent_depth)) FROM conversations WHERE id='child'`).Scan(&unchanged)
	if unchanged != childRows || after.Executions["conversation:child"].Parent.ParentID != "conversation:root" {
		t.Fatalf("late acknowledgement: %+v", after.Executions["conversation:child"].Parent)
	}
	if before.Fingerprint("child") == after.Fingerprint("child") || before.Fingerprint("root") == after.Fingerprint("root") {
		t.Fatal("persisted consumers of both transcripts must see the change")
	}
	// Old identifiers keep resolving: the launch session, its old analytics
	// identity and the explorer's synthetic rows.
	for _, alias := range []string{"launch", "agent:launch", "s-child", "conversation:child", "external-conversation:child", "ancestor-conversation:child"} {
		if b := after.ResolveAlias(alias); b.ExecutionID != "conversation:child" {
			t.Fatalf("alias %s: %+v", alias, b)
		}
	}
	if b := after.ResolveAlias("not-a-source"); b.State != bindingUnknown {
		t.Fatalf("unknown alias: %+v", b)
	}
	// Removing the evidence again restores the earlier decision.
	fixtureExec(t, c, `DELETE FROM messages WHERE id='result-launch'`)
	if !reflect.DeepEqual(executionSummary(fixtureResolution(t, c)), executionSummary(before)) {
		t.Fatal("deleted evidence must not leave its decision behind")
	}
}

func TestExecutionOccurrencesWithoutNativeIDs(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1})
	insert := func(id, cv, native, role, text string) {
		fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES(?,?,?,?,'message',?,?,'2026-01-01T00:00:20Z',1)`, id, cv, native, role, text, text)
	}
	// Coincident generated locators: a launched agent's first lines look like
	// its parent's, so an equal line number proves nothing by itself.
	insert("root-l1", "root", "line-1", "user", "hello")
	insert("fork-l1", "fork", "line-1", "user", "hello")
	// A shifted copy: different locators, identical text. Not matched.
	insert("root-l2", "root", "line-2", "assistant", "same words")
	insert("fork-l9", "fork", "line-9", "assistant", "same words")
	// Repeated identical messages with genuine distinct IDs are two events.
	insert("root-x", "root", "msg_a1b2c3", "user", "again")
	insert("fork-y", "fork", "msg_d4e5f6", "user", "again")
	r := fixtureResolution(t, c)
	o, err := r.Occurrences(t.Context(), c.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := sourceRef{sourceConversation, "fork"}
	for _, id := range []string{"fork-l1", "fork-l9", "fork-y"} {
		if got := o.Lookup(sourceRef{sourceMessage, id}, stream); got.Role != occurrenceOriginal || got.Owner != "conversation:fork" {
			t.Fatalf("%s: %+v", id, got)
		}
	}
	// Without typed evidence the rows cannot show copies whose originals are
	// gone: the fork's own counts are partial, never confidently exact.
	if o.Unresolved() != 0 || o.Partial("fork") != "untyped-ancestry/v1" || o.Partial("root") != "" {
		t.Fatalf("coverage: unresolved=%d fork=%q root=%q", o.Unresolved(), o.Partial("fork"), o.Partial("root"))
	}
	q := fixtureCounts(t, c)
	if q.Messages != 6 || q.MessageObservations != 6 || q.PartialConversations != 1 || q.OccurrenceCoverage != "partial" {
		t.Fatalf("unaccounted copies keep observations and report partial coverage: %+v", q)
	}
}

// Occurrence coverage through ingestion: a launched agent's coincident first
// lines stay its own, and its coverage stays partial (history-undeclared)
// until its provider declares its history; declared copies keep their
// candidates; a declared copy whose source is not retained, and a child
// without typed evidence or with evidence re-derived from retained rows
// alone, stay partial in every consumer. Only an explicit declaration that
// the child starts without copied history makes it complete.
func TestExecutionOccurrenceCoverageThroughIngestion(t *testing.T) {
	c, _ := testCatalog(t)
	meta := func(from string) MessageRecord {
		text := jsonText(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "x", "forked_from_id": from, "forked_from_ordinal_exclusive": 3}})
		return MessageRecord{NativeID: "line-1", Role: "system", Kind: "metadata", Text: text, EvidenceLocator: "/r/meta.jsonl:1", Selected: true}
	}
	at := func(minute int) string { return fmt.Sprintf("2026-01-01T00:%02d:00Z", minute) }
	message := func(native, role, text string, minute int) MessageRecord {
		return MessageRecord{NativeID: native, Role: role, Kind: "message", Text: text, EvidenceLocator: "/r/x.jsonl:" + strings.TrimPrefix(native, "line-"), CreatedAt: at(minute), Selected: true}
	}
	records := []ConversationRecord{
		{NativeID: "thread-parent", Provider: "codex", Account: "local", Messages: []MessageRecord{
			message("line-2", "user", "Explore the repo", 1), message("line-3", "assistant", "ok", 2)}},
		// A spawned agent whose record makes no history assertion (the
		// provider format's default is not verified, core-requests E4): its
		// first lines coincide with the parent's.
		{NativeID: "thread-spawn", ParentNativeID: "thread-parent", AgentDepth: 1, Provider: "codex", Account: "local", Messages: []MessageRecord{
			message("line-2", "user", "Explore the repo", 3), message("line-3", "assistant", "ok", 4)}},
		{NativeID: "thread-fork", ParentNativeID: "thread-parent", AgentDepth: 1, Provider: "codex", Account: "local", Messages: []MessageRecord{
			meta("thread-parent"), message("line-2", "user", "Explore the repo", 5), message("line-6", "assistant", "own", 6)}},
		{NativeID: "thread-orphan", ParentNativeID: "thread-gone", AgentDepth: 1, Provider: "codex", Account: "local", Messages: []MessageRecord{
			meta("thread-gone"), message("line-2", "user", "Explore the repo", 7)}},
	}
	for _, record := range records {
		ingestRecordTx(t, c, WorkspaceRecord{SourceID: record.NativeID, SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{record}})
	}
	id := func(native string) string { return stableID("conversation", "codex", "local", native) }
	_, o, err := c.usageConversationRootsAndOccurrences(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Typed evidence read at ingestion without a history assertion is not
	// proof of independence: partial, and its coincident lines stay its own.
	if reason := o.Partial(id("thread-spawn")); reason != "history-undeclared/v1" {
		t.Fatalf("a launched agent without a history declaration: %q", reason)
	}
	if reason := o.Partial(id("thread-parent")); reason != "" {
		t.Fatalf("a root without ancestry is complete, got %q", reason)
	}
	for _, native := range []string{"thread-fork", "thread-orphan"} {
		if o.Partial(id(native)) == "" {
			t.Fatalf("%s: declared copied history must leave coverage partial", native)
		}
	}
	// A child whose typed evidence is not current is partial too.
	fixtureExec(t, c, `DELETE FROM execution_evidence_state WHERE conversation_id=?`, id("thread-spawn"))
	if _, o, err = c.usageConversationRootsAndOccurrences(t.Context(), nil); err != nil || o.Partial(id("thread-spawn")) != "untyped-ancestry/v1" {
		t.Fatalf("untyped child: %q %v", o.Partial(id("thread-spawn")), err)
	}
	// Re-deriving it from retained rows cannot read what the provider
	// declared about its history: still partial, with a distinct reason.
	if _, err := c.BackfillExecutionEvidence(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if _, o, err = c.usageConversationRootsAndOccurrences(t.Context(), nil); err != nil || o.Partial(id("thread-spawn")) != "retained-history-undeclared/v1" {
		t.Fatalf("retained child: %q %v", o.Partial(id("thread-spawn")), err)
	}
	q := fixtureCounts(t, c)
	if q.OccurrenceCoverage != "partial" || q.PartialConversations != 3 {
		t.Fatalf("quick stats: %+v", q)
	}
	// A provider declaration that the child starts without copied history
	// (the E4 form: a history_copy claim with mapping "none") is what makes
	// it complete. No adapter emits one yet; this checks the contract.
	fixtureExec(t, c, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,origin,rule,detail_json)
	 VALUES(?,'history-none','test',?,?,'provider','test-declared-no-history/v1','{"mapping":"none"}')`, id("thread-spawn"), evidenceHistoryCopy, "conversation:"+id("thread-spawn"))
	if _, o, err = c.usageConversationRootsAndOccurrences(t.Context(), nil); err != nil || o.Partial(id("thread-spawn")) != "" {
		t.Fatalf("declared without history: %q %v", o.Partial(id("thread-spawn")), err)
	}
	// A Cursor fork declaration from a parent that is not retained: only
	// copies proved left out at ingestion need no source; unmapped, placed
	// and unverified copies stay in the transcript and stay partial.
	for mapping, want := range map[string]string{"shared-native-ids": "", "unmapped": "declared-copy-source-missing/v1",
		"ordinal": "declared-copy-source-missing/v1", "unverified": "declared-copy-source-missing/v1"} {
		fixtureExec(t, c, `DELETE FROM execution_claims WHERE conversation_id=?`, id("thread-spawn"))
		fixtureExec(t, c, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule,detail_json)
		 VALUES(?,'fork','adapter',?,?,'native:cursor|local|gone','native','cursor-fork-length-at-spawn/v1',?)`,
			id("thread-spawn"), evidenceHistoryCopy, "conversation:"+id("thread-spawn"), jsonText(map[string]any{"mapping": mapping}))
		if _, o, err = c.usageConversationRootsAndOccurrences(t.Context(), nil); err != nil || o.Partial(id("thread-spawn")) != want {
			t.Fatalf("cursor mapping %s: %q, want %q (%v)", mapping, o.Partial(id("thread-spawn")), want, err)
		}
	}
}

func TestExecutionOccurrenceWithMissingOriginal(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1})
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('survivor','fork','evt_1','user','message','x','x')`)
	// A provider-declared copy boundary whose original transcript is gone.
	saved := occurrenceClaimSources
	t.Cleanup(func() { occurrenceClaimSources = saved })
	occurrenceClaimSources = append(slices.Clone(saved), func(context.Context, queryer, []string) ([]executionClaim, error) {
		return []executionClaim{{Kind: claimInheritedCopy, Subject: sourceRef{sourceMessage, "survivor"}, Strength: evidenceNative, Rule: "test-boundary",
			Locator: sourceRef{sourceConversation, "fork"}, Context: sourceRef{sourceConversation, "root"}}}, nil
	})
	r := fixtureResolution(t, c)
	o, err := r.Occurrences(t.Context(), c.DB, []string{"fork"})
	if err != nil {
		t.Fatal(err)
	}
	got := o.Lookup(sourceRef{sourceMessage, "survivor"}, sourceRef{sourceConversation, "fork"})
	if got.Role != occurrenceInherited || got.Owner != "conversation:root" || !got.Canonical.IsZero() || got.Navigate != got.Observation {
		t.Fatalf("surviving copy: %+v", got)
	}
	if o.Fingerprint("fork") == "" {
		t.Fatal("occurrence fingerprint")
	}
	// The production counts keep the survivor as an observation, not as the
	// fork's own message.
	if q := fixtureCounts(t, c); q.Messages != 0 || q.MessageObservations != 1 {
		t.Fatalf("surviving copy counted as self activity: %+v", q)
	}
}

// Recorded ancestry that returns to a transcript proves no original: a
// self-parent never makes a message its own copy, and an event ID shared on
// a cycle is unresolved in each transcript (counted, coverage partial), never
// a copy in both. A transcript below the cycle still copies through its own
// link, as an acyclic fork does, and a launch on the cycle is never a copied
// launch.
func TestExecutionOccurrencesOnCyclicAncestry(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "self"}, executionFixtureConversation{id: "a"}, executionFixtureConversation{id: "b", parent: "a", depth: 1},
		executionFixtureConversation{id: "below", parent: "a", depth: 2}, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1})
	fixtureExec(t, c, `UPDATE conversations SET parent_id='self' WHERE id='self'; UPDATE conversations SET parent_id='b' WHERE id='a'`)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES
	 ('self-m','self','evt-self','assistant','message','x','h1'),
	 ('a-m','a','evt-1','assistant','message','x','h2'),('b-m','b','evt-1','assistant','message','x','h2'),('a-only','a','evt-a','user','message','y','h3'),
	 ('below-m','below','evt-1','assistant','message','x','h2'),
	 ('root-m','root','evt-k','assistant','message','z','h4'),('fork-m','fork','evt-k','assistant','message','z','h4')`)
	// The same launch call in both transcripts of the cycle.
	fixtureLaunch(t, c, "cycle", "a", "s-a", "", "", 0)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash,created_at) SELECT 'm-cycle-b','b','m-cycle-b',role,kind,text,call_id,content_hash,created_at FROM messages WHERE id='m-cycle'`)
	fixtureExec(t, c, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,delegation_message_id,kind,provider,usage_status,total_tokens,started_at)
	 SELECT 'cycle-b',workspace_id,'b',native_id,'s-b','m-cycle-b',kind,provider,usage_status,0,started_at FROM agent_sessions WHERE id='cycle'`)
	r := fixtureResolution(t, c)
	o, err := r.Occurrences(t.Context(), c.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	message := func(id string) sourceRef { return sourceRef{sourceMessage, id} }
	for id, role := range map[string]occurrenceRole{"self-m": occurrenceOriginal, "a-only": occurrenceOriginal, "a-m": occurrenceUnresolved, "b-m": occurrenceUnresolved,
		"below-m": occurrenceInherited, "root-m": occurrenceOriginal, "fork-m": occurrenceInherited} {
		if got := o.Lookup(message(id), sourceRef{}); got.Role != role {
			t.Fatalf("%s: role %s, want %s (%+v)", id, got.Role, role, got)
		}
	}
	for _, id := range []string{"self-m", "a-m", "b-m", "a-only"} {
		if !o.Counts(message(id), sourceRef{}) {
			t.Fatalf("%s: cyclic ancestry proved its original away", id)
		}
	}
	if got := o.Lookup(message("below-m"), sourceRef{}); got.Canonical != message("a-m") {
		t.Fatalf("the copy below the cycle names its nearest ancestor's occurrence: %+v", got)
	}
	for _, cid := range []string{"a", "b"} {
		if o.Partial(cid) == "" {
			t.Fatalf("%s: an unresolved cycle must leave coverage partial", cid)
		}
	}
	for _, id := range []string{"cycle", "cycle-b"} {
		if b := r.Bind(sourceRef{sourceSession, id}); b.State == bindingInherited {
			t.Fatalf("%s: a launch on a cycle became a copied launch: %+v", id, b)
		}
	}
	lines, err := occurrenceDependencyLines(t.Context(), c.DB, []string{"self", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(lines["self"], func(line string) bool { return strings.HasPrefix(line, "occurrence") }) ||
		!slices.ContainsFunc(lines["a"], func(line string) bool { return strings.Contains(line, string(claimCopyCandidate)) }) {
		t.Fatalf("dependency facts: %q", lines)
	}

	// Launches sharing a call ID that acknowledge the same child, with
	// differently normalized bodies, match only by call ID and target. On a
	// cycle each launch keeps its binding and issuer; the child's parent stays
	// ambiguous between them. parents maps transcript to parent; inherits maps
	// a copied launch to its original; silent transcripts hold no launch,
	// sameBody makes the launch bodies identical, and limit lowers
	// sameCallAncestryLimit.
	for _, tc := range []struct {
		name     string
		parents  map[string]string
		inherits map[string]string
		silent   []string
		sameBody bool
		limit    int
	}{
		{"two-transcript cycle", map[string]string{"a": "b", "b": "a"}, nil, nil, false, 0},
		{"two-transcript cycle, same body", map[string]string{"a": "b", "b": "a"}, nil, nil, true, 0},
		// One candidate on the cycle for each launch, and two for d's.
		{"three-transcript cycle and a descendant", map[string]string{"a": "c", "b": "a", "c": "b", "d": "a"}, nil, []string{"c"}, false, 0},
		{"acyclic chain", map[string]string{"a": "", "b": "a", "c": "b"}, map[string]string{"lb": "la", "lc": "lb"}, nil, false, 0},
		// b descends from a, but the walk up from a stops before p's parent:
		// that a does not descend from b is unproved.
		{"acyclic chain past the walk limit", map[string]string{"a": "p", "b": "a", "p": "q", "q": ""}, nil, []string{"p", "q"}, false, 2},
		{"unrelated transcripts", map[string]string{"a": "", "b": ""}, nil, nil, false, 0},
	} {
		owners := slices.Sorted(maps.Keys(tc.parents))
		for _, reversed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s reversed=%v", tc.name, reversed), func(t *testing.T) {
				var conversations []executionFixtureConversation
				for _, id := range owners {
					conversations = append(conversations, executionFixtureConversation{id: id, depth: 1})
				}
				if reversed {
					slices.Reverse(conversations)
				}
				c := executionFixture(t, append(conversations, executionFixtureConversation{id: "child", depth: 1})...)
				for _, id := range owners {
					fixtureExec(t, c, `UPDATE conversations SET parent_id=? WHERE id=?`, nilIfEmpty(tc.parents[id]), id)
				}
				launchers := slices.DeleteFunc(slices.Clone(owners), func(id string) bool { return slices.Contains(tc.silent, id) })
				for _, id := range launchers {
					fixtureLaunch(t, c, "l"+id, id, "s-"+id, "child", `{"agent_id":"child"}`, 0)
				}
				fixtureExec(t, c, `UPDATE messages SET call_id='shared' WHERE kind IN ('delegation','delegation_result'); UPDATE agent_sessions SET native_id='shared' WHERE child_conversation_id='child'`)
				if tc.sameBody {
					fixtureExec(t, c, `UPDATE messages SET text='{"tool":"spawn_agent","input":{"message":"same"}}',content_hash='same-body' WHERE kind='delegation'`)
				}
				if tc.limit > 0 {
					override(t, &sameCallAncestryLimit, tc.limit)
				}
				r := fixtureResolution(t, c)
				var issuers []executionID
				for _, id := range launchers {
					b := r.BindSession("l" + id)
					if original, ok := tc.inherits["l"+id]; ok {
						if b.State != bindingInherited || b.ExecutionID != "conversation:child" || len(b.Evidence) != 1 ||
							b.Evidence[0].Object != (sourceRef{sourceSession, original}) || b.Evidence[0].Rule != "descendant-same-call-id/v1" {
							t.Fatalf("l%s: want a copy of %s through the same call ID: %+v", id, original, b)
						}
						continue
					}
					owner := sourceRef{sourceConversation, id}
					issued := func(e executionClaim) bool { return e.Kind == claimLaunchOwner && e.Object == owner }
					if b.State != bindingResolved || b.ExecutionID != "conversation:child" || !slices.ContainsFunc(b.Evidence, issued) {
						t.Fatalf("l%s lost its execution or issuer: %+v", id, b)
					}
					issuers = append(issuers, executionID("conversation:"+id))
				}
				parent := r.Executions["conversation:child"].Parent
				var candidates []executionID
				for _, p := range parent.Candidates {
					candidates = append(candidates, p.Parent)
				}
				if len(issuers) == 1 {
					if parent.State != parentResolved || parent.ParentID != issuers[0] {
						t.Fatalf("child parent: %+v", parent)
					}
				} else if slices.Sort(candidates); parent.State != parentAmbiguous || !slices.Equal(slices.Compact(candidates), issuers) {
					t.Fatalf("child parent must stay ambiguous between %v: %+v", issuers, parent)
				}
			})
		}
	}
}

// Quick stats count a copy of history as an observation, but only a
// transcript's own observations (and its resolved sessions) make its run,
// root and repository active in a window. A copy's timestamp is when the
// original happened, where its owner is active. Unresolved observations stay
// conservative (active), and a tokenless agent's session keeps it visible.
func TestQuickStatsActivityFollowsOwnObservations(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "child", parent: "root", depth: 1})
	fixtureExec(t, c, `UPDATE agent_sessions SET started_at='2026-01-01T12:00:00Z',ended_at='2026-01-01T12:00:00Z' WHERE conversation_id='child'`)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES('original','root','same-native','assistant','message','old','old','2026-01-01T10:00:00Z',1),('copy','child','same-native','assistant','message','old','old','2026-01-01T10:00:00Z',1),('own','child','own-native','assistant','message','new','new','2026-01-01T12:00:00Z',1)`)
	counts := func(from, until string) *quickStatsCounts {
		t.Helper()
		w := &usageSummaryWindow{From: from, Until: until}
		if err := c.addQuickStatsCounts(t.Context(), map[string]any{"windows": []*usageSummaryWindow{w}}); err != nil {
			t.Fatal(err)
		}
		return w.quickStatsCounts
	}
	early := func() *quickStatsCounts { return counts("2026-01-01T10:00:00Z", "2026-01-01T11:00:00Z") }
	// The reviewed reproducer: copied at 10:00, own at noon.
	if q := early(); q.DelegatedRuns != 0 || q.Conversations != 1 || q.Messages != 1 || q.MessageObservations != 2 {
		t.Fatalf("child active before creation only through its copy: %+v", q)
	}
	if q := counts("2026-01-01T12:00:00Z", "2026-01-01T13:00:00Z"); q.DelegatedRuns != 1 || q.Conversations != 1 || q.Messages != 1 {
		t.Fatalf("the child's own noon activity: %+v", q)
	}
	// A copied tool call is an observation, not activity.
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash,created_at,selected) VALUES('call-original','root','call-native','assistant','tool_call','x','c1','c1','2026-01-01T10:30:00Z',1),('call-copy','child','call-native','assistant','tool_call','x','c1','c1','2026-01-01T10:30:00Z',1)`)
	fixtureExec(t, c, `INSERT INTO tool_calls(id,workspace_id,conversation_id,call_id,call_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at) SELECT 't-'||m.id,c.workspace_id,m.conversation_id,'c1',m.id,0,'codex','function','shell','shell','ok','2026-01-01T10:30:00Z' FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE m.id IN ('call-original','call-copy')`)
	if q := early(); q.DelegatedRuns != 0 || q.ToolCalls != 1 || q.ToolCallObservations != 2 {
		t.Fatalf("a copied tool call made the child active: %+v", q)
	}
	// An observation the evidence cannot tell from a copy stays the child's.
	saved := occurrenceClaimSources
	t.Cleanup(func() { occurrenceClaimSources = saved })
	occurrenceClaimSources = append(slices.Clone(saved), func(context.Context, queryer, []string) ([]executionClaim, error) {
		return []executionClaim{{Kind: claimCopyCandidate, Subject: sourceRef{sourceMessage, "maybe"}, Strength: evidenceDerived, Rule: "test-candidate",
			Locator: sourceRef{sourceConversation, "child"}, Context: sourceRef{sourceConversation, "root"}}}, nil
	})
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES('maybe','child','maybe-native','assistant','message','?','?','2026-01-01T10:45:00Z',1)`)
	if q := early(); q.DelegatedRuns != 1 || q.UnresolvedOccurrences != 1 {
		t.Fatalf("an unresolved observation must stay conservative: %+v", q)
	}
	// An agent that observed only copies has no owned time: its session keeps
	// the run counted, but only in an unbounded window, never at a copy's
	// time or the envelope derived from it.
	occurrenceClaimSources = saved
	fixtureExec(t, c, `DELETE FROM messages WHERE id IN ('own','maybe')`)
	fixtureExec(t, c, `UPDATE agent_sessions SET started_at='2026-01-01T10:00:00Z' WHERE conversation_id='child'`)
	if q := early(); q.DelegatedRuns != 0 {
		t.Fatalf("copies alone: %+v", q)
	}
	if q := counts("", "2027-01-01T00:00:00Z"); q.DelegatedRuns != 1 {
		t.Fatalf("a copy-only agent is still an execution: %+v", q)
	}
	// A tokenless agent with no observations stays visible through its session.
	fixtureExec(t, c, `DELETE FROM tool_calls WHERE conversation_id='child'`)
	fixtureExec(t, c, `DELETE FROM messages WHERE conversation_id='child'`)
	fixtureExec(t, c, `UPDATE agent_sessions SET started_at='2026-01-01T12:00:00Z' WHERE conversation_id='child'`)
	if q := counts("2026-01-01T12:00:00Z", "2026-01-01T13:00:00Z"); q.DelegatedRuns != 1 {
		t.Fatalf("a tokenless agent's session: %+v", q)
	}
	// A handle whose transcript was not retained registers when its launcher
	// issued it (fixture launches are at 00:00:10), and not where it was copied.
	fixtureLaunch(t, c, "handle", "root", "s-root", "", `{"agent_id":"unretained"}`, 0)
	if q := counts("2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z"); q.DelegatedRuns != 1 {
		t.Fatalf("a handle-only execution: %+v", q)
	}

	// The reviewed variant through ordinary ingestion, which derives the
	// child's session start from its first, copied, message.
	c, _ = testCatalog(t)
	old := MessageRecord{NativeID: "native-old", Role: "assistant", Kind: "message", Text: "old", CreatedAt: "2026-01-01T10:00:00Z", Selected: true}
	own := MessageRecord{NativeID: "native-own", Role: "assistant", Kind: "message", Text: "own", CreatedAt: "2026-01-01T12:00:00Z", Selected: true}
	ingestRecordTx(t, c, WorkspaceRecord{SourceID: "copied", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "root", Provider: "codex", Account: "local", Messages: []MessageRecord{old}},
		{NativeID: "child", Provider: "codex", Account: "local", ParentNativeID: "root", AgentDepth: 1, Messages: []MessageRecord{old, own}},
	}})
	var started string
	if err := c.DB.QueryRow(`SELECT a.started_at FROM agent_sessions a JOIN conversations v ON v.id=a.conversation_id WHERE v.native_id='child' AND a.native_id='main'`).Scan(&started); err != nil || !strings.HasPrefix(started, "2026-01-01T10:00") {
		t.Fatalf("fixture: the ingested envelope should start at the copy: %q %v", started, err)
	}
	if q := early(); q.DelegatedRuns != 0 || q.Conversations != 1 {
		t.Fatalf("ingested child active at its copied history: %+v", q)
	}
	if q := counts("2026-01-01T12:00:00Z", "2026-01-01T13:00:00Z"); q.DelegatedRuns != 1 {
		t.Fatalf("ingested child's own activity: %+v", q)
	}
}

// Every projection of a root, cohort or depth carries how the resolver
// established it (executionAncestry): the explorer, usage rows, activity
// events and quick stats agree, and none presents a depth or cohort reached
// through an inferred or unverified link as observed. An unresolved
// identity conflict leaves run and cohort counts uncertain, never exact.
func TestExecutionAncestryQualityAcrossConsumers(t *testing.T) {
	t.Run("observed, inferred and unverified chains", func(t *testing.T) {
		c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "obs", depth: 1},
			executionFixtureConversation{id: "leg", parent: "obs", depth: 2}, executionFixtureConversation{id: "inf"})
		// obs: an acknowledged launch (derived); leg: a stored parent of unknown
		// provenance below it; inf: an inferred command launch.
		fixtureLaunch(t, c, "launch-obs", "root", "s-root", "", `{"agent_id":"obs"}`, 0)
		fixtureExec(t, c, `INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('inf','root','call-inf','codex')`)
		if err := c.seedSessionUsage(); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"root": ancestryObserved, "obs": ancestryObserved, "leg": ancestryUnverified, "inf": ancestryInferred}
		r := fixtureResolution(t, c)
		for cv, quality := range want {
			a := r.Ancestry(r.BindConversation(cv).ExecutionID)
			if !a.Known || a.DepthQuality() != quality || a.RootQuality() != quality || a.Conflicted {
				t.Fatalf("%s: %+v quality %q, want %q", cv, a, a.DepthQuality(), quality)
			}
		}
		if a := r.Ancestry("conversation:leg"); a.CertaintyName() != evidenceLegacy.String() || a.Depth != 2 {
			t.Fatalf("leg certainty %s depth %d", a.CertaintyName(), a.Depth)
		}

		// The explorer's ancestry certainty is the same computation.
		snapshot, err := c.delegationSnapshot(t.Context(), "work")
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range snapshot.Nodes {
			if a := r.Ancestry(r.BindConversation(n.ConversationID).ExecutionID); n.Ancestry.Certainty != a.CertaintyName() {
				t.Fatalf("explorer %s certainty %q, resolver %q", n.ConversationID, n.Ancestry.Certainty, a.CertaintyName())
			}
		}

		// Usage rows: depth and cohort qualities beside the values.
		rows, err := c.usageRowsByPeriod(time.UTC, false)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, row := range rows {
			cv := firstString(row["conversation_id"])
			if _, ok := want[cv]; !ok || firstString(row["agent_session_id"]) != "s-"+cv {
				continue
			}
			seen[cv] = true
			if row["root_conversation_id"] != "root" || row["root_quality"] != want[cv] || row["delegated_run_depth_quality"] != want[cv] || row["run_identity"] != "exact" {
				t.Fatalf("usage row for %s: %#v", cv, row)
			}
		}
		if len(seen) != len(want) {
			t.Fatalf("usage rows for %v", seen)
		}
		activity, err := c.conversationActivityRows(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range activity {
			if cv := firstString(row["conversation_id"]); want[cv] != "" && row["root_quality"] != want[cv] {
				t.Fatalf("activity for %s: %#v", cv, row)
			}
		}
		// Quick stats count the cohort once and say part of it is qualified.
		counts := fixtureCounts(t, c)
		if counts.Conversations != 1 || counts.QualifiedConversations != 2 || counts.UnrootedConversations != 0 || counts.UncertainConversations != 0 || counts.AncestryCoverage != "qualified" {
			t.Fatalf("quick stats: %+v", counts)
		}
	})

	t.Run("an identity conflict leaves counts uncertain", func(t *testing.T) {
		// p acknowledges creating q while native metadata says q continues p:
		// equally strong, so how many executions they are is unresolved. Both
		// are native members of top's family, so the cohort stays known.
		c := executionFixture(t, executionFixtureConversation{id: "top"}, executionFixtureConversation{id: "p", depth: 1}, executionFixtureConversation{id: "q", depth: 1},
			executionFixtureConversation{id: "lone", depth: 1})
		fixtureLaunch(t, c, "lq", "p", "s-p", "", `{"agent_id":"q"}`, 0)
		for _, cv := range []string{"top", "q", "lone"} {
			fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES(?,?,'u','user','message','go',?,'2026-01-01T00:00:20Z',1)`, "u-"+cv, cv, "u-"+cv)
		}
		if err := c.seedSessionUsage(); err != nil {
			t.Fatal(err)
		}
		saved := executionClaimSources
		t.Cleanup(func() { executionClaimSources = saved })
		conv := func(id string) sourceRef { return sourceRef{sourceConversation, id} }
		executionClaimSources = append(slices.Clone(saved), func(_ context.Context, _ queryer, e *executionEvidence) error {
			e.Claims = append(e.Claims,
				executionClaim{Kind: claimContinuation, Subject: conv("q"), Object: conv("p"), Strength: evidenceNative, Namespace: "codex/local", Rule: "test-native-continuation"},
				executionClaim{Kind: claimFamilyMember, Subject: conv("p"), Object: conv("top"), Strength: evidenceNative, Namespace: "codex/local", Rule: "test-family"},
				executionClaim{Kind: claimFamilyMember, Subject: conv("q"), Object: conv("top"), Strength: evidenceNative, Namespace: "codex/local", Rule: "test-family"})
			return nil
		})
		r := fixtureResolution(t, c)
		for _, cv := range []string{"p", "q"} {
			a := r.Ancestry(r.BindConversation(cv).ExecutionID)
			// The cohort is known but cannot read as observed.
			if !a.Conflicted || a.Root != "conversation:top" || a.Known || a.DepthQuality() != "" || a.RootQuality() != ancestryUnverified {
				t.Fatalf("%s: %+v root quality %q", cv, a, a.RootQuality())
			}
		}
		// The unconflicted cohort root keeps its own quality.
		if a := r.Ancestry(r.BindConversation("top").ExecutionID); a.Conflicted || a.RootQuality() != ancestryObserved {
			t.Fatalf("top: %+v root quality %q", a, a.RootQuality())
		}
		// Activity root cohorts qualify the conflicted rows as usage does.
		activity, err := c.conversationActivityRows(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		qualified := map[string]bool{}
		for _, row := range activity {
			switch cv := firstString(row["conversation_id"]); cv {
			case "q":
				qualified[cv] = true
				if row["root_conversation_id"] != "top" || row["root_quality"] != ancestryUnverified {
					t.Fatalf("conflicted activity row: %#v", row)
				}
			case "top":
				if row["root_quality"] != ancestryObserved {
					t.Fatalf("root activity row: %#v", row)
				}
			case "lone":
				if row["root_conversation_id"] != nil || row["root_quality"] != nil {
					t.Fatalf("unrooted activity row: %#v", row)
				}
			}
		}
		if !qualified["q"] {
			t.Fatalf("no activity row for the conflicted conversation: %v", activity)
		}
		rows, err := c.usageRowsByPeriod(time.UTC, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			switch firstString(row["conversation_id"]) {
			case "p", "q":
				// Kept and navigable to its cohort, but its identity is uncertain.
				if row["root_conversation_id"] != "top" || row["run_identity"] != "uncertain" || row["root_quality"] != ancestryUnverified || row["delegated_run_depth"] != nil {
					t.Fatalf("conflicted usage row: %#v", row)
				}
			case "lone":
				if row["root_conversation_id"] != nil || row["run_identity"] != nil {
					t.Fatalf("unrooted usage row: %#v", row)
				}
			}
		}
		counts := fixtureCounts(t, c)
		if counts.UncertainConversations != 2 || counts.UnrootedConversations != 1 || counts.AncestryCoverage != "partial" {
			t.Fatalf("quick stats: %+v", counts)
		}
	})
}

// The occurrence facts a derivative's dependency digest records follow what
// Lookup and Partial decide: they change when an original arrives in the
// parent, when the stream that recorded it changes, when it is deleted and
// when the transcript's coverage changes, and not for an unrelated append.
// The facts of a scoped read equal the catalog-wide read's.
func TestOccurrenceDependencyLines(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "fork", parent: "root", depth: 1},
		executionFixtureConversation{id: "other"})
	message := func(id, conversation, native string) string {
		return fmt.Sprintf(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES('%s','%s','%s','user','message','x','h-%s','2026-01-01T00:00:30Z',1)`, id, conversation, native, id)
	}
	fixtureExec(t, c, message("root-a", "root", "evt_a"))
	fixtureExec(t, c, message("fork-a", "fork", "evt_a"))
	fixtureExec(t, c, message("fork-b", "fork", "evt_b"))
	read := func() (map[string][]string, *occurrenceIndex) {
		t.Helper()
		lines, err := occurrenceDependencyLines(t.Context(), c.DB, []string{"fork", "root"})
		if err != nil {
			t.Fatal(err)
		}
		all, err := occurrenceDependencyLines(t.Context(), c.DB, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, cid := range []string{"fork", "root"} {
			if !slices.Equal(lines[cid], all[cid]) {
				t.Fatalf("%s: scoped %q, catalog-wide %q", cid, lines[cid], all[cid])
			}
		}
		o, err := fixtureResolution(t, c).Occurrences(t.Context(), c.DB, []string{"fork"})
		if err != nil {
			t.Fatal(err)
		}
		return lines, o
	}
	role := func(o *occurrenceIndex, id string) executionOccurrence {
		return o.Lookup(sourceRef{sourceMessage, id}, sourceRef{sourceConversation, "fork"})
	}
	named := func(lines []string, id string) bool {
		return slices.ContainsFunc(lines, func(line string) bool { return strings.Contains(line, "\x1fmessage:"+id+"\x1f") })
	}

	start, o := read()
	if role(o, "fork-a").Role != occurrenceInherited || !named(start["fork"], "fork-a") || role(o, "fork-b").Role != occurrenceOriginal || named(start["fork"], "fork-b") {
		t.Fatalf("copies: %q, fork-a %+v", start["fork"], role(o, "fork-a"))
	}
	if len(start["root"]) != 0 || !slices.Contains(start["fork"], "partial\x1funtyped-ancestry/v1") {
		t.Fatalf("root %q fork %q", start["root"], start["fork"])
	}

	// An append whose event ID no copy shares changes nothing.
	fixtureExec(t, c, message("root-z", "root", "evt_z"))
	fixtureExec(t, c, message("other-b", "other", "evt_b"))
	if now, _ := read(); !slices.Equal(now["fork"], start["fork"]) {
		t.Fatalf("an unrelated append changed the copy's facts: %q → %q", start["fork"], now["fork"])
	}

	// The parent's original of fork-b arrives: fork-b becomes inherited.
	fixtureExec(t, c, message("root-b", "root", "evt_b"))
	arrived, o := read()
	if role(o, "fork-b").Role != occurrenceInherited || !named(arrived["fork"], "fork-b") || slices.Equal(arrived["fork"], start["fork"]) {
		t.Fatalf("arrival: %q, fork-b %+v", arrived["fork"], role(o, "fork-b"))
	}

	// The original's recorded stream decides the copy's owner.
	fixtureExec(t, c, `INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('s-root','root-b',1)`)
	membership, o := read()
	if got := role(o, "fork-b"); got.Owner == "" || got.Owner != o.r.Bind(sourceRef{sourceSession, "s-root"}).ExecutionID || slices.Equal(membership["fork"], arrived["fork"]) {
		t.Fatalf("membership: %q, fork-b %+v", membership["fork"], got)
	}

	// The original is deleted: fork-b is its own again.
	fixtureExec(t, c, `DELETE FROM agent_session_messages WHERE message_id='root-b'`)
	fixtureExec(t, c, `DELETE FROM messages WHERE id='root-b'`)
	deleted, o := read()
	if role(o, "fork-b").Role != occurrenceOriginal || named(deleted["fork"], "fork-b") || !slices.Equal(deleted["fork"], start["fork"]) {
		t.Fatalf("deletion: %q, fork-b %+v", deleted["fork"], role(o, "fork-b"))
	}
}
