package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func executionClaimsOf(t *testing.T, q queryer, conversation string) []storedExecutionClaim {
	t.Helper()
	claims, err := loadStoredExecutionClaims(context.Background(), q, `WHERE conversation_id=?`, conversation)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func findClaim(claims []storedExecutionClaim, kind, subject string) *storedExecutionClaim {
	for index := range claims {
		if claims[index].Kind == kind && (subject == "" || claims[index].Subject == subject) {
			return &claims[index]
		}
	}
	return nil
}

func workflowAck(root, run string) string {
	return jsonText(map[string]any{"tool": "Workflow", "details": map[string]any{
		"runId": run, "status": "async_launched", "taskType": "local_workflow",
		"transcriptDir": "/logs/" + root + "/subagents/workflows/" + run,
	}})
}

func TestClaudeWorkflowAcknowledgement(t *testing.T) {
	ack := workflowAck("root", "wf_run")
	for _, tc := range []struct {
		name, native, result string
		want                 bool
	}{
		{"root launcher", "root", ack, true},
		{"nested launcher", "root:subagent:agent-owner", ack, true},
		{"different session", "other", ack, false},
		{"failed launch", "root", strings.Replace(ack, `"tool":"Workflow"`, `"tool":"Workflow","is_error":true`, 1), false},
		{"report from Bash", "root", strings.Replace(ack, `"tool":"Workflow"`, `"tool":"Bash"`, 1), false},
		{"prose quoting acknowledgement", "root", jsonText(map[string]any{"tool": "Workflow", "content": ack}), false},
		{"different run directory", "root", strings.Replace(ack, `"runId":"wf_run"`, `"runId":"wf_other"`, 1), false},
		{"missing session directory", "root", `{"tool":"Workflow","details":{"runId":"wf_run","status":"async_launched","taskType":"local_workflow"}}`, false},
		{"invalid run", "root", workflowAck("root", "../wf_run"), false},
		{"unknown status", "root", strings.Replace(ack, "async_launched", "failed", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeWorkflowAcknowledgement(tc.native, tc.result); (got != "") != tc.want {
				t.Fatalf("acknowledgement = %q, accepted want %v", got, tc.want)
			}
		})
	}
	for _, native := range []string{
		"root:subagent:workflows/wf_run/agent-one/nested/agent-two",
		"root:subagent:workflows/wf_run/agent-",
		"root:subagent:agent-one",
	} {
		if _, _, ok := claudeWorkflowOf(native); ok {
			t.Fatalf("not an immediate workflow member: %s", native)
		}
	}
}

func TestClaudeWorkflowEvidenceIngestAndBackfill(t *testing.T) {
	c, _ := testCatalog(t)
	root, owner := "workflow-root", "workflow-root:subagent:agent-owner"
	id := func(native string) string { return stableID("conversation", "claude", "local", native) }
	record := WorkspaceRecord{SourceID: root, SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{
		{NativeID: root, Provider: "claude", Account: "local", Messages: []MessageRecord{
			{NativeID: "launch-owner", Role: "assistant", Kind: "delegation", CallID: "owner-call", Text: `{"tool":"Agent","input":{"prompt":"Check tools"}}`},
			{NativeID: "owner-ack", Role: "tool", Kind: "delegation_result", CallID: "owner-call", Text: `{"tool":"Agent","details":{"agentId":"owner"}}`},
		}},
		{NativeID: owner, ParentNativeID: root, Provider: "claude", Account: "local", AgentDepth: 1, Messages: []MessageRecord{
			{NativeID: "assignment", Role: "agent", Kind: "message", Text: "Check tools"},
			{NativeID: "workflow-call", Role: "assistant", Kind: "tool_call", CallID: "wf-call", Text: `{"tool":"Workflow","input":{"script":"return parallel([agent('one'), agent('two')])"}}`},
			{NativeID: "workflow-result", Role: "tool", Kind: "tool_result", CallID: "wf-call", Text: workflowAck(root, "wf_run")},
		}},
	}}
	children := []string{}
	for _, agent := range []string{"one", "two"} {
		native := root + ":subagent:workflows/wf_run/agent-" + agent
		children = append(children, id(native))
		record.Conversations = append(record.Conversations, ConversationRecord{NativeID: native, ParentNativeID: root, Provider: "claude", Account: "local", AgentDepth: 1,
			Messages: []MessageRecord{{NativeID: "task-" + agent, Role: "agent", Kind: "message", Text: "[Workflow harness — computed task] " + agent}}})
	}
	reconcileClaudeLineage(&record)
	ingestRecordTx(t, c, record)
	check := func(stage string) {
		t.Helper()
		r := fixtureResolution(t, c)
		for _, child := range children {
			binding := r.BindConversation(child)
			x := r.Execution(binding.ExecutionID)
			if x == nil || binding.ExecutionID != executionID("conversation:"+child) || x.Parent.State != parentResolved ||
				x.Parent.ParentID != executionID("conversation:"+id(owner)) || x.Parent.Certainty < evidenceDerived {
				t.Fatalf("%s child %s = %+v", stage, child, x)
			}
			if ancestry := r.Ancestry(x.ID); !ancestry.Known || ancestry.Depth != 2 {
				t.Fatalf("%s ancestry = %+v", stage, ancestry)
			}
		}
	}
	check("ingest")
	claim := findClaim(executionClaimsOf(t, c.DB, id(owner)), evidenceWorkflowLaunch, "launch:wf-call")
	if claim == nil || claim.Authority != authorityVerified || claim.Object != claudeWorkflowKey("local", root, "wf_run") {
		t.Fatalf("workflow claim = %+v", claim)
	}
	// A Workflow is an orchestration call, not an extra child execution or a
	// single launch handle that could merge its two independently retained agents.
	var handles int
	if err := c.DB.QueryRow(`SELECT count(*) FROM agent_sessions WHERE conversation_id=? AND native_id='wf-call'`, id(owner)).Scan(&handles); err != nil || handles != 0 {
		t.Fatalf("workflow launch handles = %d (%v)", handles, err)
	}
	fixtureExec(t, c, `DELETE FROM execution_claims`)
	fixtureExec(t, c, `DELETE FROM execution_evidence_state`)
	check("legacy rows")
	if _, err := c.BackfillExecutionEvidence(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	check("retained backfill")
	if claim := findClaim(executionClaimsOf(t, c.DB, id(owner)), evidenceWorkflowLaunch, "launch:wf-call"); claim == nil || claim.Authority != authorityVerified {
		t.Fatalf("backfilled workflow claim = %+v", claim)
	}
	before := sourceGeneration(t, c)
	if n, err := c.BackfillExecutionEvidence(t.Context(), 100); err != nil || n != 0 || sourceGeneration(t, c) != before {
		t.Fatalf("repeat backfill: n=%d err=%v", n, err)
	}
}

func TestRetainedWorkflowEvidenceFollowsCallChanges(t *testing.T) {
	for _, tc := range []struct{ name, change string }{
		{"deleted call", `DELETE FROM messages WHERE id='workflow-call'`},
		{"renamed tool", `UPDATE messages SET text='{"tool":"Bash"}' WHERE id='workflow-call'`},
		{"malformed call", `UPDATE messages SET text='invalid JSON' WHERE id='workflow-call'`},
		{"changed call ID", `UPDATE messages SET call_id='other-call' WHERE id='workflow-call'`},
		{"cleared call ID", `UPDATE messages SET call_id='' WHERE id='workflow-call'`},
		{"changed kind", `UPDATE messages SET kind='reasoning' WHERE id='workflow-call'`},
		{"duplicate call", `INSERT INTO messages(id,conversation_id,native_id,role,kind,call_id,text,content_hash)
		 VALUES('duplicate','root','duplicate','assistant','tool_call','call','{"tool":"Workflow"}','duplicate')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := executionFixture(t, executionFixtureConversation{id: "root", provider: "claude"},
				executionFixtureConversation{id: "child", depth: 1, provider: "claude"})
			fixtureExec(t, c, `UPDATE conversations SET native_id='root:subagent:workflows/wf_run/agent-child' WHERE id='child'`)
			insertCall := func() {
				fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,call_id,text,content_hash)
				 VALUES('workflow-call','root','workflow-call','assistant','tool_call','call','{"tool":"Workflow"}','call')`)
			}
			insertCall()
			fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,call_id,text,content_hash)
			 VALUES('result','root','result','tool','tool_result','call',?,'result')`, workflowAck("root", "wf_run"))
			backfill := func() int {
				t.Helper()
				n, err := c.BackfillExecutionEvidence(t.Context(), 100)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			checkParent := func(want parentState) {
				t.Helper()
				r := fixtureResolution(t, c)
				x := r.Execution(r.BindConversation("child").ExecutionID)
				if x == nil || x.Parent.State != want || want == parentResolved && x.Parent.ParentID != "conversation:root" {
					t.Fatalf("child parent = %+v, want %s", x, want)
				}
			}
			backfill()
			checkParent(parentResolved) // warm the cache with the saved claim
			fixtureExec(t, c, tc.change)
			var version string
			if err := c.DB.QueryRow(`SELECT version FROM execution_evidence_state WHERE conversation_id='root'`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != executionEvidenceStale+executionEvidenceVersion {
				t.Fatalf("changed Workflow call left evidence current: %s", version)
			}
			checkParent(parentMissing)
			if backfill() == 0 {
				t.Fatal("backfill skipped the changed Workflow call")
			}
			checkParent(parentMissing)
			if claim := findClaim(executionClaimsOf(t, c.DB, "root"), evidenceWorkflowLaunch, ""); claim != nil {
				t.Fatalf("repair retained an obsolete workflow claim: %+v", claim)
			}
			if backfill() != 0 {
				t.Fatal("repaired evidence did not stay current")
			}
			// Inserting the valid call again must also invalidate the repaired
			// state, so the acknowledgement can regain its parent link.
			fixtureExec(t, c, `DELETE FROM messages WHERE id IN ('workflow-call','duplicate')`)
			insertCall()
			checkParent(parentResolved)
			if backfill() == 0 {
				t.Fatal("backfill skipped the restored Workflow call")
			}
			checkParent(parentResolved)
		})
	}
}

// stampExecutionDigests records on seeded ledger rows the execution
// dependency digest a run would have built them at, and propagates, as a
// fixture that stands for built derivatives needs: a row without a digest
// reads as pending.
func stampExecutionDigests(t *testing.T, c *Catalog) {
	t.Helper()
	ctx := t.Context()
	digests, err := executionDependencyDigests(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeTransaction(ctx, "stamp digests", func(tx *sql.Tx) error {
		ledgers, err := executionLedgersPresent(ctx, tx)
		if err != nil {
			return err
		}
		for _, ledger := range ledgers {
			rows, err := queryMapsContext(ctx, tx, `SELECT conversation_id FROM `+ledger[0])
			if err != nil {
				return err
			}
			for _, row := range rows {
				id := firstString(row["conversation_id"])
				if _, err := tx.Exec(`UPDATE `+ledger[0]+` SET execution_digest=? WHERE conversation_id=?`, digests[id], id); err != nil {
					return err
				}
			}
		}
		return markExecutionDependents(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func ingestRecordTx(t *testing.T, catalog *Catalog, record WorkspaceRecord) {
	t.Helper()
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
}

func TestParseLaunchOutcome(t *testing.T) {
	for _, c := range []struct {
		name, provider, text, outcome, handle, path, origin, authority string
	}{
		{"structured agent ID", "claude", `{"tool":"Task","details":{"agentId":"a1"}}`, "accepted", "a1", "", "derived", "verified"},
		{"codex thread", "codex", `{"tool":"spawn_agent","content":"{\"agent_id\":\"019a-thread\",\"nickname\":\"Peirce\"}"}`, "accepted", "019a-thread", "", "derived", "verified"},
		{"codex task path", "codex", `{"tool":"spawn_agent","content":{"task_name":"/root/explore"}}`, "accepted", "", "/root/explore", "derived", "verified"},
		{"thread limit", "codex", `{"tool":"spawn_agent","content":"collab spawn failed: agent thread limit reached"}`, "rejected", "", "", "derived", "verified"},
		{"path exists", "codex", "{\"tool\":\"spawn_agent\",\"content\":\"agent path `/root/a` already exists\"}", "rejected", "", "", "derived", "verified"},
		{"envelope error", "claude", `{"tool":"Task","is_error":true,"content":"refused"}`, "rejected", "", "", "derived", "verified"},
		// A task that failed after creation is still an execution.
		{"task failed after creation", "claude", `{"tool":"Task","is_error":true,"details":{"agentId":"a2"}}`, "accepted", "a2", "", "derived", "verified"},
		// Report prose is not an acknowledgement (audit F7).
		{"report prose", "claude", `{"tool":"Task","content":"During review I compared agentId: target with the old logs."}`, "unknown", "", "", "derived", "inferred"},
		{"claude text acknowledgement", "claude", `{"tool":"Task","content":"Async agent launched successfully.\nagentId: a3 (internal ID 42)\nThe agent is working."}`, "accepted", "a3", "", "derived", "verified"},
		{"no result envelope", "codex", `not json`, "unknown", "", "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := parseLaunchOutcome(c.provider, c.text)
			if got.Outcome != c.outcome || got.Handle != c.handle || got.Path != c.path || (c.origin != "" && got.Origin.String() != c.origin) {
				t.Fatalf("got %+v, want outcome=%s handle=%s path=%s origin=%s", got, c.outcome, c.handle, c.path, c.origin)
			}
			// A parsed envelope is derived, yet a structured handle or refusal
			// verifies the outcome.
			claim := EvidenceRecord{Kind: evidenceLaunchOutcome, Origin: got.Origin.String(), Rule: got.Rule, Detail: map[string]any{"outcome": got.Outcome}}
			if authority := evidenceAuthority(claim); c.authority != "" && authority != c.authority {
				t.Fatalf("authority = %s, want %s", authority, c.authority)
			}
		})
	}
}

func TestMessageIDKind(t *testing.T) {
	for _, c := range []struct{ policy, native, locator, want string }{
		{"line-locator/v1", "line-12", "/r/rollout.jsonl:12", idKindLocator},
		{"line-locator/v1", "line-12:result", "/r/rollout.jsonl:12", idKindLocator},
		// A line-shaped ID its locator does not corroborate is the provider's.
		{"line-locator/v1", "line-12", "/r/rollout.jsonl:40", idKindNativeEvent},
		{"line-locator/v1", "resp_abc", "/r/rollout.jsonl:12", idKindNativeEvent},
		{"provider-event/v1", "f4c1-uuid", "", idKindNativeEvent},
		{"unclassified/v1", "line-3", "", idKindLocator},
		{"unclassified/v1", "msg-3", "", idKindUnknown},
		// Legacy rows with no recorded policy.
		{"", "resp_abc", "", idKindUnknown},
	} {
		if got := messageIDKind(c.policy, c.native, c.locator); got != c.want {
			t.Errorf("messageIDKind(%q,%q,%q) = %q, want %q", c.policy, c.native, c.locator, got, c.want)
		}
	}
}

// A Claude child without an accepted acknowledgement keeps the root as its
// stored parent (that is what earlier versions displayed), but the typed
// evidence says the edge is a synthesized fallback, never a verified one.
// Report prose naming another agent is not an acknowledgement either.
func TestClaudeLineageEvidenceDistinguishesFallback(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := "claude-root"
	child := func(agent, prompt string, messages ...MessageRecord) ConversationRecord {
		all := append([]MessageRecord{{NativeID: agent + "-prompt", Role: "user", Kind: "message", Text: prompt}}, messages...)
		return ConversationRecord{NativeID: root + ":subagent:agent-" + agent, ParentNativeID: root, Provider: "claude", Account: "local", AgentDepth: 1, Messages: all}
	}
	record := WorkspaceRecord{SourceID: root, SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{
		{NativeID: root, Provider: "claude", Account: "local", Messages: []MessageRecord{
			{NativeID: "u1", Role: "user", Kind: "message", Text: "Review the logs"},
			{NativeID: "d1", Role: "assistant", Kind: "delegation", CallID: "call-1", Text: `{"tool":"Task","input":{"prompt":"Explore"}}`},
			{NativeID: "r1", Role: "tool", Kind: "delegation_result", CallID: "call-1", Text: `{"tool":"Task","details":{"agentId":"explore"}}`},
			{NativeID: "d2", Role: "assistant", Kind: "delegation", CallID: "call-2", Text: `{"tool":"Task","input":{"prompt":"Compare"}}`},
			{NativeID: "r2", Role: "tool", Kind: "delegation_result", CallID: "call-2", Text: `{"tool":"Task","content":"During review I compared agentId: target with the old logs."}`},
		}},
		child("explore", "Explore"),
		child("target", "Something else"),
		child("orphan", "No launch retained"),
	}}
	reconcileClaudeLineage(&record)
	ingestRecordTx(t, catalog, record)
	id := func(native string) string { return stableID("conversation", "claude", "local", native) }

	explore := findClaim(executionClaimsOf(t, catalog.DB, id(root+":subagent:agent-explore")), evidenceParent, "")
	if explore == nil || explore.Origin != "derived" || explore.Rule != "claude-launch-ack/v1" || explore.Locator != "launch:call-1" {
		t.Fatalf("accepted child = %+v", explore)
	}
	for _, name := range []string{"target", "orphan"} {
		conversation := id(root + ":subagent:agent-" + name)
		claim := findClaim(executionClaimsOf(t, catalog.DB, conversation), evidenceParent, "")
		if claim == nil || claim.Origin != "synthesized" || claim.Rule != "claude-root-fallback/v1" || firstString(claim.Detail["reason"]) != "no-acknowledgement" {
			t.Fatalf("%s parent claim = %+v", name, claim)
		}
		var parent string
		if err := catalog.DB.QueryRow(`SELECT COALESCE(parent_id,'') FROM conversations WHERE id=?`, conversation).Scan(&parent); err != nil || parent != id(root) {
			t.Fatalf("%s stored parent = %q (%v): the stored graph is preserved", name, parent, err)
		}
	}
	launches := executionClaimsOf(t, catalog.DB, id(root))
	if prose := findClaim(launches, evidenceLaunchOutcome, "launch:call-2"); prose == nil || firstString(prose.Detail["outcome"]) != "unknown" {
		t.Fatalf("prose result = %+v", prose)
	}
	if ack := findClaim(launches, evidenceLaunchOutcome, "launch:call-1"); ack == nil || ack.Origin != "derived" || ack.Authority != authorityVerified || ack.Object != claudeAgentKey("local", root, "explore") {
		t.Fatalf("acknowledged launch = %+v", ack)
	}

	// The loader passes the fallback on as synthesized (a hint, never proof)
	// and the acknowledged edge as derived.
	evidence, err := loadExecutionEvidence(context.Background(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	strengths := map[string][]evidenceStrength{}
	for _, claim := range evidence.Claims {
		if claim.Kind == claimTranscriptParent {
			strengths[claim.Subject.ID] = append(strengths[claim.Subject.ID], claim.Strength)
		}
	}
	if s := strengths[id(root+":subagent:agent-orphan")]; !slices.Contains(s, evidenceSynthesized) || slices.Contains(s, evidenceDerived) || slices.Contains(s, evidenceNative) {
		t.Fatalf("orphan parent strengths = %v", s)
	}
	if s := strengths[id(root+":subagent:agent-explore")]; !slices.Contains(s, evidenceDerived) {
		t.Fatalf("explore parent strengths = %v", s)
	}
	if slices.Contains(evidence.Missing, missingTypedEvidence) {
		t.Fatalf("every conversation has current evidence, yet: %v", evidence.Missing)
	}

	// Re-deriving from retained rows is repeatable and changes nothing.
	generation := func() int64 {
		var value int64
		if err := catalog.DB.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='execution_evidence_generation'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := generation()
	if _, err := catalog.DB.Exec(`UPDATE execution_evidence_state SET version='old'`); err != nil {
		t.Fatal(err)
	}
	if n, err := catalog.BackfillExecutionEvidence(context.Background(), 2); err != nil || n != 4 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	if after := generation(); after != before {
		t.Fatalf("re-derivation from retained rows changed claims: generation %d -> %d", before, after)
	}
	if n, err := catalog.BackfillExecutionEvidence(context.Background(), 2); err != nil || n != 0 {
		t.Fatalf("second backfill = %d, %v", n, err)
	}

	// A catalog indexed before typed evidence: backfill recovers the Claude
	// distinction from retained delegation messages without changing parents.
	if _, err := catalog.DB.Exec(`DELETE FROM execution_claims; DELETE FROM execution_evidence_state`); err != nil {
		t.Fatal(err)
	}
	evidence, err = loadExecutionEvidence(context.Background(), catalog.DB)
	if err != nil || !slices.Contains(evidence.Missing, missingTypedEvidence) {
		t.Fatalf("legacy catalog coverage = %v, %v", evidence.Missing, err)
	}
	exploreID := id(root + ":subagent:agent-explore")
	derived := func() bool {
		t.Helper()
		evidence, err := loadExecutionEvidence(context.Background(), catalog.DB)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(evidence.Claims, func(claim executionClaim) bool {
			return claim.Kind == claimTranscriptParent && claim.Subject.ID == exploreID && claim.Strength == evidenceDerived
		})
	}
	// The resolved parent, through the production resolution: a supported
	// acknowledgement resolves it as derived; without support it is not
	// derived, and the root stays a candidate hint.
	ancestry := func(c *Catalog) parentResolution {
		t.Helper()
		r := fixtureResolution(t, c)
		x := r.Executions[r.BindConversation(exploreID).ExecutionID]
		if x == nil {
			t.Fatal("explore is not an execution")
		}
		return x.Parent
	}
	acknowledged := func(a parentResolution) bool { return a.State == parentResolved && a.Certainty == evidenceDerived }
	unsupported := func(a parentResolution) bool {
		return a.Certainty != evidenceDerived && a.Certainty != evidenceNative && slices.ContainsFunc(a.Candidates, func(p parentClaim) bool { return string(p.Parent) == "conversation:"+id(root) })
	}
	// A first backfill in bounded batches: the batch holding the root writes
	// the group's claims for every member, but a state only for the root.
	if err := catalog.writeTransaction(context.Background(), "first batch", func(tx *sql.Tx) error {
		if err := backfillExecutionEvidence(context.Background(), tx, []string{id(root)}, true); err != nil {
			return err
		}
		return markExecutionDependents(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	var states int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM execution_evidence_state WHERE conversation_id=?`, exploreID).Scan(&states); err != nil || states != 0 || !derived() {
		t.Fatalf("between batches: explore has %d states (%v), derived parent read %v", states, err, derived())
	}
	if a := ancestry(catalog); !acknowledged(a) {
		t.Fatalf("between batches, the supported parent: %+v", a)
	}
	// Correcting the root's acknowledgement before the next batch withdraws
	// the unprocessed member's derived parent, also after reopening and after
	// the acknowledgement is restored, until the backfill continues.
	acknowledgement := `{"tool":"Task","details":{"agentId":"explore"}}`
	if _, err := catalog.DB.Exec(`UPDATE messages SET text='{"tool":"Task","content":"done"}' WHERE conversation_id=? AND native_id='r1'`, id(root)); err != nil {
		t.Fatal(err)
	}
	if derived() {
		t.Fatal("an unprocessed member's parent reads as current after its acknowledgement was corrected")
	}
	// The stored launch link is the same inference: it does not resolve the
	// parent in the acknowledgement's place.
	if a := ancestry(catalog); !unsupported(a) {
		t.Fatalf("after the correction, the parent rests on the materialized link: %+v", a)
	}
	if err := catalog.ensureExecutionSourceGeneration(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`UPDATE messages SET text=? WHERE conversation_id=? AND native_id='r1'`, acknowledgement, id(root)); err != nil {
		t.Fatal(err)
	}
	if derived() {
		t.Fatal("after reopening, the group's claims are read before re-derivation")
	}
	if n, err := catalog.BackfillExecutionEvidence(context.Background(), 1); err != nil || n != 4 {
		t.Fatalf("continued backfill = %d, %v", n, err)
	}
	if !derived() || !acknowledged(ancestry(catalog)) {
		t.Fatalf("the continued backfill did not re-derive the acknowledged parent: %+v", ancestry(catalog))
	}
	claim := findClaim(executionClaimsOf(t, catalog.DB, id(root+":subagent:agent-orphan")), evidenceParent, "")
	if claim == nil || claim.Origin != "synthesized" {
		t.Fatalf("backfilled orphan claim = %+v", claim)
	}
	var input string
	if err := catalog.DB.QueryRow(`SELECT input FROM execution_evidence_state WHERE conversation_id=?`, id(root)).Scan(&input); err != nil || input != "retained" {
		t.Fatalf("backfill input = %q, %v", input, err)
	}

	// Group lineage re-derived from retained rows reads every member's
	// messages: a correction of the root's acknowledgement marks the whole
	// group stale, and the child's derived parent is not read until backfill
	// re-derives it as a fallback.
	if _, err := catalog.DB.Exec(`DELETE FROM messages WHERE conversation_id=? AND native_id='r1'`, id(root)); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM execution_evidence_state WHERE version=?`, executionEvidenceStale+executionEvidenceVersion).Scan(&stale); err != nil || stale != 4 {
		t.Fatalf("stale group members = %d, %v", stale, err)
	}
	if a := ancestry(catalog); derived() || !unsupported(a) {
		t.Fatalf("a stale group acknowledgement is read as current: %+v", a)
	}
	if n, err := catalog.BackfillExecutionEvidence(context.Background(), 10); err != nil || n != 4 {
		t.Fatalf("group re-derivation = %d, %v", n, err)
	}
	if claim := findClaim(executionClaimsOf(t, catalog.DB, id(root+":subagent:agent-explore")), evidenceParent, ""); claim == nil || claim.Origin != "synthesized" || derived() {
		t.Fatalf("re-derived explore claim = %+v", claim)
	}
	// After the full reconstruction the group reads as one that never had the
	// acknowledgement, though the launch still links explore in stored rows.
	if a := ancestry(catalog); !unsupported(a) {
		t.Fatalf("after reconstruction, the parent rests on the materialized link: %+v", a)
	}

	// A provider read's acknowledgement is the provider's record: pruning its
	// result leaves the parent resolved.
	ingested, _ := testCatalog(t)
	ingestRecordTx(t, ingested, record)
	if _, err := ingested.DB.Exec(`DELETE FROM messages WHERE conversation_id=? AND native_id='r1'`, id(root)); err != nil {
		t.Fatal(err)
	}
	if a := ancestry(ingested); !acknowledged(a) {
		t.Fatalf("a provider read's pruned acknowledgement: %+v", a)
	}

	// A nested agent's stored non-root parent is the same inference: once
	// its acknowledgement is gone and the group re-derived, it is a hint.
	nested, _ := testCatalog(t)
	deep := child("deep", "Dig")
	explorer := child("explore", "Explore",
		MessageRecord{NativeID: "d3", Role: "assistant", Kind: "delegation", CallID: "call-3", Text: `{"tool":"Task","input":{"prompt":"Dig"}}`},
		MessageRecord{NativeID: "r3", Role: "tool", Kind: "delegation_result", CallID: "call-3", Text: `{"tool":"Task","details":{"agentId":"deep"}}`})
	group := WorkspaceRecord{SourceID: root, SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{record.Conversations[0], explorer, deep}}
	reconcileClaudeLineage(&group)
	ingestRecordTx(t, nested, group)
	deepID := id(root + ":subagent:agent-deep")
	deepParent := func() parentResolution {
		t.Helper()
		r := fixtureResolution(t, nested)
		return r.Executions[r.BindConversation(deepID).ExecutionID].Parent
	}
	var stored string
	if err := nested.DB.QueryRow(`SELECT COALESCE(parent_id,'') FROM conversations WHERE id=?`, deepID).Scan(&stored); err != nil || stored != exploreID || !acknowledged(deepParent()) {
		t.Fatalf("nested agent: stored parent %q (%v), resolved %+v", stored, err, deepParent())
	}
	if _, err := nested.DB.Exec(`DELETE FROM execution_claims; DELETE FROM execution_evidence_state;
	 DELETE FROM messages WHERE conversation_id=? AND native_id='r3'`, exploreID); err != nil {
		t.Fatal(err)
	}
	if _, err := nested.BackfillExecutionEvidence(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if a := deepParent(); a.Certainty == evidenceDerived || a.Certainty == evidenceNative {
		t.Fatalf("nested agent after reconstruction: %+v", a)
	}
}

// Evidence backfill derived from stored rows is only as current as those
// rows. A later correction or deletion by a writer other than ingestion marks
// it stale, readers use the stored rows until backfill re-derives it, and
// the mark survives a restart. A provider read's evidence is the provider's
// record: a later pruning of the transcript does not withdraw it.
func TestRetainedEvidenceFollowsStoredRows(t *testing.T) {
	ctx := t.Context()
	c := executionFixture(t, executionFixtureConversation{id: "root"}, executionFixtureConversation{id: "actual", parent: "root", depth: 1},
		executionFixtureConversation{id: "other", parent: "root", depth: 1})
	fixtureLaunch(t, c, "launch", "root", "s-root", "", `{"agent_id":"actual"}`, 0)
	backfill := func(want int) {
		t.Helper()
		if n, err := c.BackfillExecutionEvidence(ctx, 10); err != nil || n != want {
			t.Fatalf("backfill = %d, %v; want %d", n, err, want)
		}
	}
	version := func(id string) string {
		t.Helper()
		var version string
		if err := c.DB.QueryRow(`SELECT version FROM execution_evidence_state WHERE conversation_id=?`, id).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	outcome := func() string {
		if claim := findClaim(executionClaimsOf(t, c.DB, "root"), evidenceLaunchOutcome, "launch:call-launch"); claim != nil {
			return claim.Object
		}
		return ""
	}
	bound := func() string {
		return string(fixtureResolution(t, c).Bind(sourceRef{sourceSession, "launch"}).ExecutionID)
	}
	backfill(3)
	if version("root") != executionEvidenceVersion || outcome() != "native:codex|local|actual" || bound() != "conversation:actual" {
		t.Fatalf("backfilled: %s %q %q", version("root"), outcome(), bound())
	}

	// A correction of the result: the old acknowledgement is no longer read
	// as current, and the change reaches the source generation.
	generation, _, err := executionSourceGeneration(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	fixtureExec(t, c, `UPDATE messages SET text='{"agent_id":"other"}' WHERE id='result-launch'`)
	if version("root") != executionEvidenceStale+executionEvidenceVersion || version("actual") != executionEvidenceVersion {
		t.Fatalf("corrected result: root %s, actual %s", version("root"), version("actual"))
	}
	if changed, err := executionChangedSince(ctx, c.DB, generation); err != nil || !slices.Contains(changed, "root") {
		t.Fatalf("changed since %d = %v, %v", generation, changed, err)
	}
	if got := bound(); got != "conversation:other" {
		t.Fatalf("before re-derivation the stored rows decide, not the old claim: %q", got)
	}
	// Reopening reinstalls the triggers and keeps the mark.
	if err := c.ensureExecutionSourceGeneration(); err != nil {
		t.Fatal(err)
	}
	if version("root") != executionEvidenceStale+executionEvidenceVersion {
		t.Fatalf("after reopening: %s", version("root"))
	}
	backfill(1)
	if version("root") != executionEvidenceVersion || outcome() != "native:codex|local|other" || bound() != "conversation:other" {
		t.Fatalf("re-derived: %s %q %q", version("root"), outcome(), bound())
	}

	// A deletion of the result leaves no acknowledgement, before and after
	// re-derivation; an unrelated message changes nothing.
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('note','root','note','assistant','reasoning','thinking','note')`)
	fixtureExec(t, c, `INSERT INTO messages(id,conversation_id,native_id,role,kind,call_id,text,content_hash)
	 VALUES('unrelated-call','root','unrelated-call','assistant','tool_call','unrelated','{"tool":"Bash"}','bash')`)
	if version("root") != executionEvidenceVersion {
		t.Fatalf("an unread message marked the evidence stale: %s", version("root"))
	}
	fixtureExec(t, c, `DELETE FROM messages WHERE id='result-launch'`)
	if got := bound(); got == "conversation:other" || got == "conversation:actual" {
		t.Fatalf("deleted result still binds: %q", got)
	}
	backfill(1)
	if outcome() != "" || bound() == "conversation:other" {
		t.Fatalf("after deletion: %q %q", outcome(), bound())
	}
	backfill(0)

	// A provider read keeps its acknowledgement when the result is pruned.
	ingested, _ := testCatalog(t)
	ingestRecordTx(t, ingested, WorkspaceRecord{SourceID: "thread-a", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "thread-a", Provider: "codex", Account: "local", Messages: []MessageRecord{
			{NativeID: "d1", Role: "assistant", Kind: "delegation", CallID: "c1", Text: `{"tool":"spawn_agent"}`},
			{NativeID: "r1", Role: "tool", Kind: "delegation_result", CallID: "c1", Text: `{"tool":"spawn_agent","content":"{\"agent_id\":\"thread-b\"}"}`},
		}}}})
	thread := stableID("conversation", "codex", "local", "thread-a")
	fixtureExec(t, ingested, `DELETE FROM messages WHERE conversation_id=? AND kind='delegation_result'`, thread)
	var state, input string
	if err := ingested.DB.QueryRow(`SELECT version,input FROM execution_evidence_state WHERE conversation_id=?`, thread).Scan(&state, &input); err != nil || state != executionEvidenceVersion || input != "ingest" {
		t.Fatalf("provider read after pruning: %s/%s %v", state, input, err)
	}
	if claim := findClaim(executionClaimsOf(t, ingested.DB, thread), evidenceLaunchOutcome, "launch:c1"); claim == nil || claim.Object != "native:codex|local|thread-b" {
		t.Fatalf("provider acknowledgement after pruning = %+v", claim)
	}
}

// Cursor drops a fork's copy of its parent's first messages before indexing;
// the claim keeps where the copy ends, how its messages map to the parent's,
// and which tool calls it held.
func TestCursorForkRetainsCopyEvidence(t *testing.T) {
	root := cursorFixture(t)
	var fork *ConversationRecord
	for _, record := range cursorRecords(t, root) {
		for index := range record.Conversations {
			if record.Conversations[index].NativeID == cursorFork {
				fork = &record.Conversations[index]
			}
		}
	}
	if fork == nil {
		t.Fatal("the fixture's fork was not read")
	}
	var copied *EvidenceRecord
	for index := range fork.Evidence {
		if fork.Evidence[index].Kind == evidenceHistoryCopy {
			copied = &fork.Evidence[index]
		}
	}
	if copied == nil || copied.Object != executionNativeKey("cursor", "local", cursorNew) || copied.Origin != "native" ||
		copied.Detail["mapping"] != "shared-native-ids" || copied.Detail["copied"].(int) == 0 {
		t.Fatalf("fork copy evidence = %+v", copied)
	}
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true})
	stored := findClaim(executionClaimsOf(t, c.DB, stableID("conversation", "cursor", "local", cursorFork)), evidenceHistoryCopy, "")
	if stored == nil || stored.Source != evidenceSourceAdapter || stored.Rule != "cursor-fork-length-at-spawn/v1" {
		t.Fatalf("stored fork copy claim = %+v", stored)
	}
	var policy string
	if err := c.DB.QueryRow(`SELECT id_policy FROM execution_evidence_state WHERE conversation_id=?`, stableID("conversation", "cursor", "local", cursorFork)).Scan(&policy); err != nil || policy != "provider-event/v1" {
		t.Fatalf("cursor id policy = %q, %v", policy, err)
	}
}

// Model requests take their occurrence from the stored messages whose usage
// events opened and closed them, through ordinary ingestion: a request a
// child copied with its parent's message is inherited from the parent's
// request, its own request is original, and one streamed across the copy
// boundary is unresolved. Request IDs are never matched: a provider without
// event IDs proves nothing, and a copy whose original is gone stays the
// child's own with partial coverage. Accounting rows are unchanged.
func TestRequestOccurrencesFollowUsageMessages(t *testing.T) {
	ctx := t.Context()
	c, _ := testCatalog(t)
	usage := func(native, request, at string) MessageRecord {
		return MessageRecord{NativeID: native, Role: "assistant", Kind: "message", Selected: true, CreatedAt: at,
			RawText: `{"type":"assistant","message":{"id":"` + request + `","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100,"output_tokens":10}}}`}
	}
	old, own := usage("native-old", "native-old", "2026-01-01T10:00:00Z"), usage("native-own", "native-own", "2026-01-01T12:00:00Z")
	streamed, streamedOn := usage("native-stream-1", "req-stream", "2026-01-01T10:30:00Z"), usage("native-stream-2", "req-stream", "2026-01-01T12:30:00Z")
	family := func(source, provider, root, child string, rootMessages []MessageRecord, childMessages ...MessageRecord) WorkspaceRecord {
		return WorkspaceRecord{SourceID: source, SourceKind: provider, Account: "local", Conversations: []ConversationRecord{
			{NativeID: root, Provider: provider, Account: "local", Messages: rootMessages},
			{NativeID: child, Provider: provider, Account: "local", ParentNativeID: root, AgentDepth: 1, Messages: childMessages}}}
	}
	ingestRecordTx(t, c, family("codex", "codex", "root", "child", []MessageRecord{old, streamed}, old, streamed, streamedOn, own))
	// The original of the child's first message is not retained.
	ingestRecordTx(t, c, family("missing", "codex", "root-gone", "child-gone", []MessageRecord{own}, old))
	// A provider whose message IDs are not event IDs.
	ingestRecordTx(t, c, family("opencode", "opencode", "oc-root", "oc-child", []MessageRecord{old}, old))
	conversation := func(native string) string {
		var id string
		if err := c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id=?`, native).Scan(&id); err != nil {
			t.Fatal(native, err)
		}
		return id
	}
	request := func(native, requestID string) sourceRef {
		var id string
		if err := c.DB.QueryRow(`SELECT id FROM model_requests WHERE conversation_id=? AND native_id=?`, conversation(native), requestID).Scan(&id); err != nil {
			t.Fatal(native, requestID, err)
		}
		return sourceRef{sourceRequest, id}
	}
	index := func() *occurrenceIndex {
		r, err := resolveExecutionsFrom(ctx, c.DB)
		if err != nil {
			t.Fatal(err)
		}
		o, err := r.Occurrences(ctx, c.DB, nil)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	o := index()
	if got := o.Lookup(request("child", "native-old"), sourceRef{}); got.Role != occurrenceInherited || got.Canonical != request("root", "native-old") {
		t.Fatalf("the copied request: %+v", got)
	}
	if got := o.Lookup(request("child", "native-own"), sourceRef{}); got.Role != occurrenceOriginal {
		t.Fatalf("the child's own request: %+v", got)
	}
	if got := o.Lookup(request("child", "req-stream"), sourceRef{}); got.Role != occurrenceUnresolved || o.Partial(conversation("child")) == "" {
		t.Fatalf("a request streamed across the copy boundary: %+v, partial %q", got, o.Partial(conversation("child")))
	}
	if got := o.Lookup(request("root", "native-old"), sourceRef{}); got.Role != occurrenceOriginal {
		t.Fatalf("the parent's request: %+v", got)
	}
	if got := o.Lookup(request("child-gone", "native-old"), sourceRef{}); got.Role != occurrenceOriginal || o.Partial(conversation("child-gone")) == "" {
		t.Fatalf("a copy whose original is gone: %+v, partial %q", got, o.Partial(conversation("child-gone")))
	}
	if got := o.Lookup(request("oc-child", "native-old"), sourceRef{}); got.Role != occurrenceOriginal {
		t.Fatalf("an equal request ID proved a copy: %+v", got)
	}
	// Codex stores token_count events as line-N metadata, which proves no
	// copy: a fork's copied request stays unresolved, counted with partial
	// coverage, never inherited by position; its own request is original.
	tokenCount := func(file, line, input, at string) MessageRecord {
		raw := `{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":` + input + `,"output_tokens":10}}}}`
		return MessageRecord{NativeID: "line-" + line, Role: "system", Kind: "metadata", Text: raw, RawText: raw, EvidenceLocator: "/r/" + file + ".jsonl:" + line, CreatedAt: at, Selected: true}
	}
	ingestRecordTx(t, c, WorkspaceRecord{SourceID: "tc-parent", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "tc-parent", Provider: "codex", Account: "local", Messages: []MessageRecord{
			{NativeID: "resp_1", Role: "user", Kind: "message", Text: "Explore", EvidenceLocator: "/r/parent.jsonl:2", CreatedAt: "2026-01-02T10:00:00Z", Selected: true},
			tokenCount("parent", "3", "100", "2026-01-02T10:01:00Z")}}}})
	ingestRecordTx(t, c, WorkspaceRecord{SourceID: "tc-fork", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "tc-fork", ParentNativeID: "tc-parent", Provider: "codex", Account: "local", Messages: []MessageRecord{
			{NativeID: "line-1", Role: "system", Kind: "metadata", EvidenceLocator: "/r/fork.jsonl:1", CreatedAt: "2026-01-02T11:00:00Z", Selected: true,
				Text: jsonText(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "tc-fork", "forked_from_id": "tc-parent", "forked_from_ordinal_exclusive": 4}})},
			{NativeID: "resp_1", Role: "user", Kind: "message", Text: "Explore", EvidenceLocator: "/r/fork.jsonl:2", CreatedAt: "2026-01-02T10:00:00Z", Selected: true},
			tokenCount("fork", "3", "100", "2026-01-02T10:01:00Z"),
			{NativeID: "resp_2", Role: "user", Kind: "message", Text: "Now test", EvidenceLocator: "/r/fork.jsonl:4", CreatedAt: "2026-01-02T11:01:00Z", Selected: true},
			tokenCount("fork", "5", "300", "2026-01-02T11:02:00Z")}}}})
	byUsage := func(line string) sourceRef {
		var id string
		if err := c.DB.QueryRow(`SELECT id FROM model_requests WHERE conversation_id=? AND source_message_id=?`, conversation("tc-fork"), messageCatalogID(conversation("tc-fork"), line)).Scan(&id); err != nil {
			t.Fatal(line, err)
		}
		return sourceRef{sourceRequest, id}
	}
	o = index()
	if got := o.Lookup(byUsage("line-3"), sourceRef{}); got.Role != occurrenceUnresolved || o.Partial(conversation("tc-fork")) == "" {
		t.Fatalf("a Codex request copied as line-N: %+v, partial %q", got, o.Partial(conversation("tc-fork")))
	}
	if got := o.Lookup(byUsage("line-5"), sourceRef{}); got.Role != occurrenceOriginal {
		t.Fatalf("a Codex fork's own request: %+v", got)
	}
	var requests, tokens int
	if err := c.DB.QueryRow(`SELECT COUNT(*),SUM(total_tokens) FROM model_requests WHERE conversation_id=?`, conversation("child")).Scan(&requests, &tokens); err != nil || requests != 3 || tokens != 330 {
		t.Fatalf("the child's request rows: %d requests, %d tokens, %v", requests, tokens, err)
	}

	// The stored link is a source input: removing it is logged under the
	// child, changes its occurrence facts, and leaves the request unresolved
	// rather than the child's own.
	before, err := occurrenceDependencyLines(ctx, c.DB, []string{conversation("child")})
	if err != nil {
		t.Fatal(err)
	}
	generation, _, err := executionSourceGeneration(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `UPDATE model_requests SET source_message_id=NULL WHERE id='`+request("child", "native-old").ID+`'`)
	changed, err := executionChangedSince(ctx, c.DB, generation)
	if err != nil || !slices.Equal(changed, []string{conversation("child")}) {
		t.Fatalf("changes after the link was removed: %v, %v", changed, err)
	}
	after, err := occurrenceDependencyLines(ctx, c.DB, []string{conversation("child")})
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, after) {
		t.Fatalf("occurrence facts did not change: %v", after)
	}
	if got := index().Lookup(request("child", "native-old"), sourceRef{}); got.Role != occurrenceUnresolved {
		t.Fatalf("a request without its usage message: %+v", got)
	}
}

// A Codex fork's session note declares copied history. Its copies match the
// original only by a provider event ID; a generated line-N locator never
// does, at the same line or a shifted one, and repeated identical content
// has several candidates. A copy whose original is not retained gets none.
func TestCodexHistoryCopyOccurrences(t *testing.T) {
	catalog, _ := testCatalog(t)
	meta := func(from string) MessageRecord {
		text := jsonText(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "x", "forked_from_id": from, "forked_from_ordinal_exclusive": 3}})
		return MessageRecord{NativeID: "line-1", Role: "system", Kind: "metadata", Text: text, EvidenceLocator: "/r/fork.jsonl:1"}
	}
	parent := ConversationRecord{NativeID: "thread-parent", Provider: "codex", Account: "local", Messages: []MessageRecord{
		{NativeID: "resp_1", Role: "user", Kind: "message", Text: "Explore the repo", EvidenceLocator: "/r/parent.jsonl:2"},
		{NativeID: "line-3", Role: "assistant", Kind: "message", Text: "ok", EvidenceLocator: "/r/parent.jsonl:3"},
		{NativeID: "line-4", Role: "assistant", Kind: "message", Text: "ok", EvidenceLocator: "/r/parent.jsonl:4"},
	}}
	fork := ConversationRecord{NativeID: "thread-fork", ParentNativeID: "thread-parent", Provider: "codex", Account: "local", Messages: []MessageRecord{
		meta("thread-parent"),
		{NativeID: "resp_1", Role: "user", Kind: "message", Text: "Explore the repo", EvidenceLocator: "/r/fork.jsonl:2"},
		// The same text one line later than in the parent.
		{NativeID: "line-5", Role: "assistant", Kind: "message", Text: "ok", EvidenceLocator: "/r/fork.jsonl:5"},
		{NativeID: "resp_own", Role: "assistant", Kind: "message", Text: "my own reply", EvidenceLocator: "/r/fork.jsonl:6"},
	}}
	orphan := ConversationRecord{NativeID: "thread-orphan", ParentNativeID: "thread-gone", Provider: "codex", Account: "local", Messages: []MessageRecord{
		meta("thread-gone"),
		{NativeID: "resp_9", Role: "user", Kind: "message", Text: "Explore the repo", EvidenceLocator: "/r/orphan.jsonl:2"},
	}}
	for _, conversation := range []ConversationRecord{parent, fork, orphan} {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: conversation.NativeID, SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{conversation}})
	}
	id := func(native string) string { return stableID("conversation", "codex", "local", native) }
	if claim := findClaim(executionClaimsOf(t, catalog.DB, id("thread-fork")), evidenceHistoryCopy, ""); claim == nil || claim.Detail["mapping"] != "ordinal" {
		t.Fatalf("fork history claim = %+v", claim)
	}
	claims, err := persistedOccurrenceClaims(context.Background(), catalog.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, claim := range claims {
		got[string(claim.Kind)] = append(got[string(claim.Kind)], claim.Subject.ID+">"+claim.Object.ID)
	}
	message := func(conversation, native string) string { return messageCatalogID(id(conversation), native) }
	if want := []string{message("thread-fork", "resp_1") + ">" + message("thread-parent", "resp_1")}; !slices.Equal(got[string(claimInheritedCopy)], want) {
		t.Fatalf("inherited copies = %v, want %v", got[string(claimInheritedCopy)], want)
	}
	candidates := slices.DeleteFunc(slices.Clone(got[string(claimCopyCandidate)]), func(pair string) bool {
		return strings.HasPrefix(pair, message("thread-orphan", "resp_9")+">")
	})
	slices.Sort(candidates)
	want := []string{message("thread-fork", "line-5") + ">" + message("thread-parent", "line-3"), message("thread-fork", "line-5") + ">" + message("thread-parent", "line-4")}
	slices.Sort(want)
	if !slices.Equal(candidates, want) {
		t.Fatalf("copy candidates = %v, want %v", candidates, want)
	}
	for _, claim := range claims {
		if claim.Subject.ID == message("thread-fork", "resp_own") {
			t.Fatalf("unexpected occurrence claim %+v", claim)
		}
		// The orphan's original is not retained: its declared copy stays
		// unresolved (TestMissingOriginalCopies), never a match.
		if claim.Subject.ID == message("thread-orphan", "resp_9") && (claim.Kind != claimCopyCandidate || !claim.Object.IsZero()) {
			t.Fatalf("orphan copy claim %+v", claim)
		}
	}

	// Re-derived from the stored note, the copy follows it: removing the
	// note leaves no declared copy to read until backfill re-derives none.
	if _, err := catalog.DB.Exec(`DELETE FROM execution_claims; DELETE FROM execution_evidence_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.BackfillExecutionEvidence(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	inherited := func() bool {
		t.Helper()
		claims, err := persistedOccurrenceClaims(context.Background(), catalog.DB, nil)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(claims, func(claim executionClaim) bool {
			return claim.Kind == claimInheritedCopy && claim.Subject.ID == message("thread-fork", "resp_1")
		})
	}
	if !inherited() {
		t.Fatal("the backfilled copy is not read")
	}
	if _, err := catalog.DB.Exec(`DELETE FROM messages WHERE id=?`, message("thread-fork", "line-1")); err != nil {
		t.Fatal(err)
	}
	if inherited() {
		t.Fatal("a copy whose stored note was removed is read as current")
	}
	if _, err := catalog.BackfillExecutionEvidence(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if inherited() || findClaim(executionClaimsOf(t, catalog.DB, id("thread-fork")), evidenceHistoryCopy, "") != nil {
		t.Fatal("re-derivation kept the removed note's copy")
	}
}

// A declared history copy whose original is not retained is still copied
// history: the Cursor fork's boundary bubbles are inherited copies whose only
// navigation target is the copy, a Codex ordinal region stays unresolved,
// and the canonical counts say they are partial.
func TestMissingOriginalCopies(t *testing.T) {
	catalog, _ := testCatalog(t)
	note := jsonText(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread-orphan", "forked_from_id": "thread-gone", "forked_from_ordinal_exclusive": 1}})
	ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "thread-orphan", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "thread-orphan", Provider: "codex", Account: "local", Messages: []MessageRecord{
			{NativeID: "resp_9", Role: "user", Kind: "message", Text: "Explore the repo", EvidenceLocator: "/r/orphan.jsonl:2", Selected: true},
			{NativeID: "resp_10", Role: "assistant", Kind: "message", Text: "My own answer", EvidenceLocator: "/r/orphan.jsonl:3", Selected: true},
			{NativeID: "line-4", Role: "system", Kind: "metadata", Text: note, EvidenceLocator: "/r/orphan.jsonl:1", Selected: true},
		}}}})
	ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "composer-f", SourceKind: "cursor", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "composer-f", Provider: "cursor", Account: "local", Messages: []MessageRecord{
			{NativeID: "b1", Role: "user", Kind: "message", Text: "Plan the change", Selected: true},
			{NativeID: "b2", Role: "assistant", Kind: "message", Text: "Here is the plan", Selected: true},
			{NativeID: "b3", Role: "user", Kind: "message", Text: "Now in the fork", Selected: true},
		}, Evidence: []EvidenceRecord{{Kind: evidenceHistoryCopy, Subject: "conversation", Object: executionNativeKey("cursor", "local", "composer-gone"),
			Origin: "native", Rule: "cursor-fork-length-at-spawn/v1", IDKind: idKindNativeEvent,
			Detail: map[string]any{"copied": 2, "boundary_ordinal": 2, "mapping": "unverified", "parent_read": false, "parent_deleted": true, "first_id": "b1", "last_id": "b2"}}}}}})
	id := func(provider, native string) string {
		var cid string
		if err := catalog.DB.QueryRow(`SELECT id FROM conversations WHERE provider=? AND native_id=?`, provider, native).Scan(&cid); err != nil {
			t.Fatalf("%s: %v", native, err)
		}
		return cid
	}
	codex, cursor := id("codex", "thread-orphan"), id("cursor", "composer-f")
	message := func(cid, native string) sourceRef { return sourceRef{sourceMessage, messageCatalogID(cid, native)} }
	res, err := resolveExecutionsFrom(context.Background(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	occurrences, err := res.Occurrences(context.Background(), catalog.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		obs    sourceRef
		stream string
		role   occurrenceRole
	}{
		{message(cursor, "b1"), cursor, occurrenceInherited},
		{message(cursor, "b2"), cursor, occurrenceInherited},
		{message(cursor, "b3"), cursor, occurrenceOriginal},
		{message(codex, "resp_9"), codex, occurrenceUnresolved},
		{message(codex, "resp_10"), codex, occurrenceOriginal},
	} {
		got := occurrences.Lookup(c.obs, sourceRef{sourceConversation, c.stream})
		if got.Role != c.role {
			t.Fatalf("%v: role %s, want %s (%+v)", c.obs, got.Role, c.role, got)
		}
		if c.role == occurrenceInherited && (got.Navigate != c.obs || got.Owner != "") {
			t.Fatalf("%v: a copy of a missing original navigates to itself and borrows no owner: %+v", c.obs, got)
		}
	}
	if occurrences.Unresolved() == 0 {
		t.Fatal("unresolved copies are not reported")
	}
	// The production counts: copies are not the fork's own messages, and the
	// unresolved one makes them partial.
	window := &usageSummaryWindow{Until: "2100-01-01T00:00:00Z"}
	if err := catalog.addQuickStatsCounts(t.Context(), map[string]any{"windows": []*usageSummaryWindow{window}}); err != nil {
		t.Fatal(err)
	}
	counts := window.quickStatsCounts
	if counts.MessageObservations-counts.Messages < 2 || counts.UnresolvedOccurrences == 0 || counts.OccurrenceCoverage == "complete" {
		t.Fatalf("quick stats = %+v", counts)
	}
}

func dependencyDigestsBoth(t *testing.T, catalog *Catalog, ids ...string) (map[string]string, map[string]string) {
	t.Helper()
	all, err := executionDependencyDigests(context.Background(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := executionDependencyDigestsFor(context.Background(), catalog.DB, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if all[id] != scoped[id] {
			t.Fatalf("%s: catalog-wide digest %q, component walk %q", id, all[id], scoped[id])
		}
	}
	return all, scoped
}

// A late acknowledgement in A changes B's digest though B's rows did not
// change; an unrelated conversation keeps its digest; a claim naming a
// transcript not yet indexed changes its owner's digest when the transcript
// arrives; deleting a member or splitting the component changes the rest.
func TestExecutionDependencyDigests(t *testing.T) {
	catalog, _ := testCatalog(t)
	tx := seedTx(t, catalog.DB)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','w','2026-11-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('a','w','codex','local','thread-a'),('b','w','codex','local','thread-b'),
		 ('u','w','codex','local','thread-u'),('g','w','codex','local','thread-g')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('g2','w','codex','local','thread-g2','g',1)`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s-a','w','a','call-1','delegated','codex')`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ids := []string{"a", "b", "u", "g", "g2"}
	before, _ := dependencyDigestsBoth(t, catalog, ids...)
	if before["g"] != before["g2"] || before["a"] == before["b"] {
		t.Fatalf("components: %v", before)
	}

	write := func(statements ...string) {
		t.Helper()
		tx := seedTx(t, catalog.DB)
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// A late child link in a's transcript: b's rows are untouched.
	write(`UPDATE agent_sessions SET child_conversation_id='b' WHERE id='s-a'`)
	linked, _ := dependencyDigestsBoth(t, catalog, ids...)
	if linked["b"] == before["b"] || linked["a"] != linked["b"] || linked["u"] != before["u"] || linked["g"] != before["g"] {
		t.Fatalf("after a late link: before %v after %v", before, linked)
	}

	// A launch outcome naming a thread no conversation has yet.
	pending := executionNativeKey("codex", "local", "thread-late")
	write(`INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('u','k1','record','launch_outcome','launch:call-9','` + pending + `','native','launch-envelope/v1')`)
	named, _ := dependencyDigestsBoth(t, catalog, ids...)
	if named["u"] == linked["u"] {
		t.Fatal("a new claim did not change its owner's digest")
	}
	write(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('late','w','codex','local','thread-late')`)
	arrived, _ := dependencyDigestsBoth(t, catalog, append(ids, "late")...)
	if arrived["u"] == named["u"] || arrived["u"] != arrived["late"] {
		t.Fatalf("the missing transcript's arrival: named %v arrived %v", named, arrived)
	}

	// Deletion and a split.
	write(`DELETE FROM conversations WHERE id='late'`)
	deleted, _ := dependencyDigestsBoth(t, catalog, ids...)
	if deleted["u"] != named["u"] {
		t.Fatalf("deleting the arrival restores the unresolved dependency: %q, want %q", deleted["u"], named["u"])
	}
	write(`UPDATE conversations SET parent_id=NULL WHERE id='g2'`)
	split, _ := dependencyDigestsBoth(t, catalog, ids...)
	if split["g"] == before["g"] || split["g2"] == before["g2"] || split["g"] == split["g2"] {
		t.Fatalf("split: before %v after %v", before, split)
	}

	// A component past the walk's limit takes the catalog-wide digest: still
	// exact, so its publication is current until its evidence changes.
	override(t, &executionDependencyLimit, 1)
	limited, err := executionDependencyDigestsFor(context.Background(), catalog.DB, []string{"a", "g"})
	if err != nil || limited["a"] != split["a"] || limited["g"] != split["g"] {
		t.Fatalf("limited digests = %v, want %v (%v)", limited, split, err)
	}
	current, err := executionDependencyCurrent(context.Background(), catalog.DB, map[string]string{"a": split["a"]})
	if err != nil || !current["a"] {
		t.Fatalf("limited component not current: %v %v", current, err)
	}
	write(`UPDATE conversations SET parent_id='a' WHERE id='g2'`)
	if current, err := executionDependencyCurrent(context.Background(), catalog.DB, map[string]string{"a": split["a"]}); err != nil || current["a"] {
		t.Fatalf("limited component missed a change: %v %v", current, err)
	}
}

// A snapshot of a catalog from before typed evidence declares the capability
// missing instead of failing.
func TestExecutionEvidenceAbsentTables(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, statement := range []string{`DROP TABLE execution_claims`, `DROP TABLE execution_evidence_state`} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	evidence := &executionEvidence{}
	if err := persistedExecutionClaims(context.Background(), catalog.DB, evidence); err != nil || !slices.Contains(evidence.Missing, missingTypedEvidence) {
		t.Fatalf("missing = %v, %v", evidence.Missing, err)
	}
	if claims, err := persistedOccurrenceClaims(context.Background(), catalog.DB, nil); err != nil || len(claims) != 0 {
		t.Fatalf("occurrence claims = %v, %v", claims, err)
	}
	if _, err := executionDependencyDigests(context.Background(), catalog.DB); err != nil {
		t.Fatal(err)
	}
	// Offline labeling declares the gap in its manifest's coverage.
	if evidence, gap, err := labelingExecutionEvidence(context.Background(), catalog.DB); err != nil || evidence["present"] != false || !strings.Contains(gap, "unavailable") {
		t.Fatalf("prepare evidence = %v %q %v", evidence, gap, err)
	}

	// A snapshot from before authorities were recorded, opened without
	// Initialize: claims read with an unknown authority.
	older, _ := testCatalog(t)
	ingestRecordTx(t, older, WorkspaceRecord{SourceID: "thread-a", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "thread-a", Provider: "codex", Account: "local", Messages: []MessageRecord{
			{NativeID: "d1", Role: "assistant", Kind: "delegation", CallID: "c1", Text: `{"tool":"spawn_agent"}`},
			{NativeID: "r1", Role: "tool", Kind: "delegation_result", CallID: "c1", Text: `{"tool":"spawn_agent","content":"{\"agent_id\":\"thread-b\"}"}`},
		}}}})
	triggers, err := queryMaps(older.DB, `SELECT name FROM sqlite_master WHERE type='trigger' AND sql LIKE '%authority%'`)
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range triggers {
		if _, err := older.DB.Exec(`DROP TRIGGER ` + firstString(trigger["name"])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := older.DB.Exec(`ALTER TABLE execution_claims DROP COLUMN authority`); err != nil {
		t.Fatal(err)
	}
	claims, err := loadStoredExecutionClaims(context.Background(), older.DB, "")
	if err != nil || len(claims) == 0 || claims[0].Authority != "" {
		t.Fatalf("claims without authorities = %+v, %v", claims, err)
	}
	if _, err := loadExecutionEvidence(context.Background(), older.DB); err != nil {
		t.Fatal(err)
	}
}

func TestLabelingSnapshotExecutionEvidenceCoverage(t *testing.T) {
	catalog := labeledCatalog(t)
	evidence, gap, err := labelingExecutionEvidence(context.Background(), catalog.DB)
	if err != nil || evidence["present"] != true || !strings.Contains(gap, "legacy relationships only") {
		t.Fatalf("seeded rows without typed evidence: %v %q %v", evidence, gap, err)
	}
	if _, err := catalog.BackfillExecutionEvidence(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if evidence, gap, err = labelingExecutionEvidence(context.Background(), catalog.DB); err != nil || gap != "" || evidence["current"] != evidence["conversations"] {
		t.Fatalf("after backfill: %v %q %v", evidence, gap, err)
	}
}

// A link recorded in another conversation's transcript changes which
// execution c-events is without changing any c-events row, so no dirty
// trigger marks it. Its agent events follow anyway, reusing every stored
// verdict whose input is unchanged. Evidence that changes again while a run
// is building is not published against the old attribution: the build is
// discarded, requeued persistently, and completed after a restart.
func TestAgentEventsFollowExecutionEvidenceElsewhere(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	if _, err := catalog.DB.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s-other','ws-other','c-other','call-late','delegated','claude')`); err != nil {
		t.Fatal(err)
	}
	first, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !first.Complete {
		t.Fatalf("first run: %+v %v", first, err)
	}
	structure := func(c *Catalog) string {
		t.Helper()
		var value string
		if err := c.DB.QueryRow(`SELECT structure FROM agent_events_conversations WHERE conversation_id='c-events'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	dirty := func(c *Catalog) bool {
		t.Helper()
		var n int
		if err := c.DB.QueryRow(`SELECT COUNT(*) FROM agent_events_dirty WHERE conversation_id='c-events'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	built := structure(catalog)
	current := func(c *Catalog) bool {
		t.Helper()
		ok, err := agentEventsExecutionCurrent(t.Context(), c.DB, "c-events")
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	stale := func(c *Catalog) int {
		t.Helper()
		status, err := c.agentEventsStatus(t.Context(), Config{Labels: labels, AgentEvents: settings})
		if err != nil {
			t.Fatal(err)
		}
		return status["conversations_stale"].(int)
	}
	var digest string
	if err := catalog.DB.QueryRow(`SELECT execution_digest FROM agent_events_conversations WHERE conversation_id='c-events'`).Scan(&digest); err != nil || digest == "" {
		t.Fatalf("stored digest %q (%v)", digest, err)
	}
	if !current(catalog) || stale(catalog) != 0 {
		t.Fatal("a fresh build reads as stale")
	}
	// Publication writes no evidence: a further run has nothing to rebuild.
	again, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !again.Complete || again.ConversationsBuilt != 0 {
		t.Fatalf("a run after publication rebuilt its own output: %+v %v", again, err)
	}

	if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET child_conversation_id='c-events' WHERE id='s-other'`); err != nil {
		t.Fatal(err)
	}
	if dirty(catalog) {
		t.Fatal("the fixture no longer tests a dependency the row triggers miss")
	}
	// Before any run, reads already know the stored events are stale.
	if current(catalog) || stale(catalog) == 0 {
		t.Fatal("events built before the link elsewhere still read as current")
	}
	_, summary, _, err := catalog.agentEventsAttacher("c-events")
	if err != nil {
		t.Fatal(err)
	}
	if read, err := summary(); err != nil || read == nil || read["pending"] != true {
		t.Fatalf("reader summary = %v (%v), want pending", read, err)
	}
	followed, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !followed.Complete || followed.ConversationsBuilt == 0 || followed.MessagesScored != 0 || followed.MessagesUnchanged == 0 {
		t.Fatalf("after a link elsewhere: %+v %v", followed, err)
	}
	if relinked := structure(catalog); relinked == built {
		t.Fatal("c-events was not rebuilt after its execution evidence changed")
	}
	if !current(catalog) || stale(catalog) != 0 {
		t.Fatal("the rebuild did not make the stored events current")
	}
	linked := structure(catalog)

	// The link is retracted after the run read the catalog, before it stores.
	override(t, &beforeDerivedPublication, func() {
		beforeDerivedPublication = nil
		if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET child_conversation_id=NULL WHERE id='s-other'`); err != nil {
			t.Error(err)
		}
	})
	if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET child_conversation_id='c-events', kind='delegated' WHERE id='s-other'`); err != nil {
		t.Fatal(err)
	}
	// Force a rebuild of c-events with the link present.
	if _, err := catalog.DB.Exec(`INSERT INTO agent_events_dirty(conversation_id,token) VALUES('c-events','force') ON CONFLICT(conversation_id) DO UPDATE SET token='force'`); err != nil {
		t.Fatal(err)
	}
	raced, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || raced.ConversationsStale == 0 || raced.Complete {
		t.Fatalf("raced run: %+v %v", raced, err)
	}
	if structure(catalog) != linked || !dirty(catalog) {
		t.Fatal("a build against retracted evidence was published, or its work was not kept pending")
	}
	if current(catalog) {
		t.Fatal("the retraction left the stored events reading as current")
	}

	// A restart: only what the catalog holds carries the pending work.
	reopened, err := OpenCatalog(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	closeAtEnd(t, reopened)
	settled, err := reopened.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !settled.Complete || settled.ConversationsStale != 0 || settled.MessagesScored != 0 {
		t.Fatalf("after restart: %+v %v", settled, err)
	}
	if structure(reopened) == linked || dirty(reopened) || !current(reopened) {
		t.Fatal("the pending rebuild did not complete after a restart")
	}
}

// The labels ledger follows execution evidence elsewhere like agent events:
// membership and structure refresh, while labels whose full inputs (framing,
// context, model and contract) are unchanged are reused, not inferred again.
func TestLabelsFollowExecutionEvidenceElsewhere(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	if _, err := catalog.DB.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s-late','ws-other','c-other','call-late','delegated','claude')`); err != nil {
		t.Fatal(err)
	}
	first, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !first.Complete {
		t.Fatalf("first run: %+v %v", first, err)
	}
	structure := func() string {
		t.Helper()
		var value string
		if err := catalog.DB.QueryRow(`SELECT structure FROM message_labels_conversations WHERE conversation_id='c-new'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	built := structure()
	pending := func() bool {
		t.Helper()
		_, summary, err := catalog.messageLabelsAttacher("c-new")
		if err != nil {
			t.Fatal(err)
		}
		value, err := summary()
		if err != nil {
			t.Fatal(err)
		}
		return value != nil && value["pending"] == true
	}
	if pending() {
		t.Fatal("freshly built labels read as pending")
	}
	if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET child_conversation_id='c-new' WHERE id='s-late'`); err != nil {
		t.Fatal(err)
	}
	// Before the background run: read as pending, not as current attribution.
	if !pending() {
		t.Fatal("labels built against superseded execution evidence read as current")
	}
	followed, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !followed.Complete || followed.ConversationsBuilt == 0 || followed.MessagesLabeled != 0 || followed.MessagesUnchanged == 0 {
		t.Fatalf("after a link elsewhere: %+v %v", followed, err)
	}
	linked := structure()
	if linked == built || pending() {
		t.Fatal("c-new was not rebuilt after its execution evidence changed")
	}

	override(t, &beforeDerivedPublication, func() {
		beforeDerivedPublication = nil
		if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET child_conversation_id=NULL WHERE id='s-late'`); err != nil {
			t.Error(err)
		}
	})
	if _, err := catalog.DB.Exec(`INSERT INTO message_labels_dirty(conversation_id,token) VALUES('c-new','force') ON CONFLICT(conversation_id) DO UPDATE SET token='force'`); err != nil {
		t.Fatal(err)
	}
	raced, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || raced.ConversationsStale == 0 || raced.Complete {
		t.Fatalf("raced run: %+v %v", raced, err)
	}
	if structure() != linked {
		t.Fatal("a build against retracted evidence was published")
	}
	settled, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !settled.Complete || settled.ConversationsStale != 0 || structure() == linked {
		t.Fatalf("settled run: %+v %v", settled, err)
	}
}

// A verified creation acknowledgement read by a rule (derived origin, verified
// authority) contradicted by continuation metadata the provider recorded:
// the parent spawned the thread, yet the thread's session_meta says it was
// forked from that parent without a delegating parent field. Through the
// Codex adapter, ingestion and the loader, equally authoritative evidence
// leaves the identity unresolved instead of silently folding the thread into
// its parent (or dropping the acknowledgement).
func TestVerifiedAcknowledgementAgainstNativeContinuation(t *testing.T) {
	catalog, _ := testCatalog(t)
	dir := t.TempDir()
	parse := func(name string, lines ...string) WorkspaceRecord {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		adapter := jsonlAdapter{provider: "codex"}
		record, ok, err := adapter.parse(path)
		if err != nil || !ok {
			t.Fatalf("parse %s: ok=%v err=%v", name, ok, err)
		}
		return record
	}
	parent := parse("rollout-parent.jsonl",
		`{"type":"session_meta","payload":{"id":"thread-p"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Split the work"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn","arguments":"{\"message\":\"Review\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"spawn","output":"{\"agent_id\":\"thread-t\"}"}}`)
	child := parse("rollout-child.jsonl",
		`{"type":"session_meta","payload":{"id":"thread-t","forked_from_id":"thread-p","forked_from_ordinal_exclusive":1}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Review"}]}}`)
	for _, record := range []WorkspaceRecord{parent, child} {
		ingestRecordTx(t, catalog, record)
	}
	id := func(native string) string {
		var cid string
		if err := catalog.DB.QueryRow(`SELECT id FROM conversations WHERE provider='codex' AND native_id=?`, native).Scan(&cid); err != nil {
			t.Fatalf("%s: %v", native, err)
		}
		return cid
	}
	launch := ""
	for _, claim := range executionClaimsOf(t, catalog.DB, id("thread-p")) {
		if claim.Kind == evidenceLaunchOutcome {
			if claim.Origin != "derived" || claim.Authority != authorityVerified || firstString(claim.Detail["outcome"]) != "accepted" {
				t.Fatalf("acknowledgement = %+v", claim)
			}
			launch = claim.Subject
		}
	}
	if launch == "" {
		t.Fatal("no acknowledgement recorded")
	}
	history := findClaim(executionClaimsOf(t, catalog.DB, id("thread-t")), evidenceHistoryCopy, "")
	if history == nil || history.Origin != "native" || history.Authority != authorityVerified || history.Detail["forked_from"] != true {
		t.Fatalf("history = %+v", history)
	}
	res, err := resolveExecutionsFrom(context.Background(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	// The conflict names the continuation and every source it binds,
	// including the acknowledged launch, which is not erased.
	found := false
	for _, conflict := range res.Conflicts {
		if slices.Contains(conflict.Sources, sourceRef{sourceConversation, id("thread-p")}) && slices.Contains(conflict.Sources, sourceRef{sourceConversation, id("thread-t")}) &&
			slices.ContainsFunc(conflict.Sources, func(ref sourceRef) bool { return ref.Kind == sourceSession }) {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflicts = %+v, want the continuation against the acknowledged launch", res.Conflicts)
	}
	p, c := res.BindConversation(id("thread-p")), res.BindConversation(id("thread-t"))
	if p.ExecutionID != "" && p.ExecutionID == c.ExecutionID {
		t.Fatalf("the spawned thread was folded into its parent: %+v", p)
	}
	if x := res.Executions[c.ExecutionID]; x == nil || x.Parent.State != "conflict" {
		t.Fatalf("child execution = %+v, want its parent unresolved by the conflict", x)
	}
	if res.Coverage.Conflicts == 0 {
		t.Fatalf("coverage does not report the conflict: %+v", res.Coverage)
	}
}

// A parent in another provider keeps its recorded namespace through a
// retained-row backfill; when the namespace was never recorded, the
// backfill says so instead of naming the child's provider as established.
func TestRetainedParentNamespace(t *testing.T) {
	catalog, _ := testCatalog(t)
	imported := WorkspaceRecord{SourceID: "export-1", SourceKind: "jsonl", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "claude-root", Provider: "claude", Account: "local", Messages: []MessageRecord{{NativeID: "u1", Role: "user", Kind: "message", Text: "Plan"}}},
		{NativeID: "codex-child", Provider: "codex", Account: "local", ParentNativeID: "claude-root", ParentProvider: "claude", ParentAccount: "local", AgentDepth: 1,
			Messages: []MessageRecord{{NativeID: "u2", Role: "user", Kind: "message", Text: "Do it"}}},
		// A Claude agent an export says a Codex thread launched: not Claude's
		// own (post-lineage) parent field, so it is kept.
		{NativeID: "claude-agent", Provider: "claude", Account: "local", ParentNativeID: "codex-child", ParentProvider: "codex", ParentAccount: "local", AgentDepth: 2,
			Messages: []MessageRecord{{NativeID: "u5", Role: "user", Kind: "message", Text: "Check"}}},
	}}
	native := WorkspaceRecord{SourceID: "thread-a", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "thread-a", Provider: "codex", Account: "local", Messages: []MessageRecord{{NativeID: "u3", Role: "user", Kind: "message", Text: "Start"}}},
		{NativeID: "thread-b", Provider: "codex", Account: "local", ParentNativeID: "thread-a", AgentDepth: 1, Messages: []MessageRecord{{NativeID: "u4", Role: "user", Kind: "message", Text: "Sub"}}},
	}}
	ingestRecordTx(t, catalog, imported)
	ingestRecordTx(t, catalog, native)
	id := func(provider, nativeID string) string {
		var cid string
		if err := catalog.DB.QueryRow(`SELECT id FROM conversations WHERE provider=? AND native_id=?`, provider, nativeID).Scan(&cid); err != nil {
			t.Fatalf("%s: %v", nativeID, err)
		}
		return cid
	}
	parentOf := func(cid string) *storedExecutionClaim {
		return findClaim(executionClaimsOf(t, catalog.DB, cid), evidenceParent, "")
	}
	child, sub := id("codex", "codex-child"), id("codex", "thread-b")
	want := executionNativeKey("claude", "local", "claude-root")
	if claim := parentOf(child); claim == nil || claim.Object != want || claim.Origin != "native" {
		t.Fatalf("ingested cross-provider parent = %+v", claim)
	}
	backfill := func() {
		t.Helper()
		if _, err := catalog.DB.Exec(`UPDATE execution_evidence_state SET version='old'`); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.BackfillExecutionEvidence(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	agent := id("claude", "claude-agent")
	if claim := parentOf(agent); claim == nil || claim.Source != evidenceSourceRecord || claim.Object != executionNativeKey("codex", "local", "codex-child") || claim.Origin != "native" {
		t.Fatalf("explicit Codex parent of a Claude agent = %+v", claim)
	}
	backfill()
	if claim := parentOf(child); claim == nil || claim.Object != want || claim.Origin != "native" {
		t.Fatalf("backfilled cross-provider parent = %+v, want the recorded namespace kept", claim)
	}
	if claim := parentOf(agent); claim == nil || claim.Source != evidenceSourceRecord || claim.Object != executionNativeKey("codex", "local", "codex-child") {
		t.Fatalf("backfilled explicit Codex parent of a Claude agent = %+v", claim)
	}
	if claim := parentOf(sub); claim == nil || claim.Object != executionNativeKey("codex", "local", "thread-a") || claim.Origin != "native" {
		t.Fatalf("backfilled same-provider parent = %+v", claim)
	}
	// An observation from before parent namespaces were recorded.
	if _, err := catalog.DB.Exec(`UPDATE origin_observations SET parent_provider=NULL,parent_account=NULL WHERE conversation_id=?`, child); err != nil {
		t.Fatal(err)
	}
	backfill()
	claim := parentOf(child)
	if claim == nil || claim.Origin != "legacy" || claim.Authority != authorityUnverified || claim.Detail["parent_namespace"] != "unrecorded" {
		t.Fatalf("parent with an unrecorded namespace = %+v", claim)
	}
	if claim := parentOf(sub); claim == nil || claim.Origin != "native" {
		t.Fatalf("a native adapter's parent stays established: %+v", claim)
	}
	// An unrecorded namespace never becomes a native parent in resolution.
	evidence, err := loadExecutionEvidence(context.Background(), catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range evidence.Claims {
		if c.Kind == claimTranscriptParent && c.Subject.ID == child && c.Strength == evidenceNative && c.Rule != "retained-native-parent" {
			t.Fatalf("native parent claim from an unrecorded namespace: %+v", c)
		}
	}
}

// The digest covers what the legacy readings read outside typed claims: a
// launch result's body (its handle names a transcript elsewhere) and a
// retained origin parent. Arrival of the named transcript, a correction of
// the result and its deletion each change the digest of everyone the reading
// could attribute, and the component walk agrees with the catalog-wide one.
func TestExecutionDependencyDigestLegacyInputs(t *testing.T) {
	catalog, _ := testCatalog(t)
	codex := func(native, parent string, depth int, messages ...MessageRecord) WorkspaceRecord {
		if len(messages) == 0 {
			messages = []MessageRecord{{NativeID: "u-" + native, Role: "user", Kind: "message", Text: "Hi " + native}}
		}
		return WorkspaceRecord{SourceID: native, SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: native, Provider: "codex", Account: "local", ParentNativeID: parent, AgentDepth: depth, Messages: messages}}}
	}
	ingestRecordTx(t, catalog, codex("thread-p", "", 0,
		MessageRecord{NativeID: "u1", Role: "user", Kind: "message", Text: "Split"},
		MessageRecord{NativeID: "d1", Role: "assistant", Kind: "delegation", CallID: "c1", Text: `{"tool":"spawn_agent","input":{"message":"Review"}}`},
		MessageRecord{NativeID: "r1", Role: "tool", Kind: "delegation_result", CallID: "c1", Text: `{"tool":"spawn_agent","content":"{\"agent_id\":\"thread-x\"}"}`}))
	ingestRecordTx(t, catalog, codex("thread-k", "thread-z", 1))
	id := func(native string) string {
		var cid string
		if err := catalog.DB.QueryRow(`SELECT id FROM conversations WHERE native_id=?`, native).Scan(&cid); err != nil {
			t.Fatalf("%s: %v", native, err)
		}
		return cid
	}
	p, k := id("thread-p"), id("thread-k")
	// A catalog from before typed evidence: only the legacy readings remain.
	legacy := func() {
		t.Helper()
		if _, err := catalog.DB.Exec(`DELETE FROM execution_claims; DELETE FROM execution_evidence_state; UPDATE agent_sessions SET child_conversation_id=NULL`); err != nil {
			t.Fatal(err)
		}
	}
	legacy()
	var launches int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE conversation_id=? AND delegation_message_id IS NOT NULL`, p).Scan(&launches); err != nil || launches == 0 {
		t.Fatalf("fixture has no launch session (%v)", err)
	}
	digests, _ := dependencyDigestsBoth(t, catalog, p, k)
	before := digests[p]

	// The transcript the result names arrives.
	ingestRecordTx(t, catalog, codex("thread-x", "", 0))
	legacy()
	x := id("thread-x")
	digests, _ = dependencyDigestsBoth(t, catalog, p, k, x)
	if digests[p] == before || digests[x] != digests[p] {
		t.Fatalf("arrival of the named transcript: p %s → %s, x %s", before, digests[p], digests[x])
	}
	arrived := digests[x]

	// The result is corrected to name another thread: x's attribution changes
	// though none of x's rows did.
	if _, err := catalog.DB.Exec(`UPDATE messages SET text=?,content_hash='corrected' WHERE conversation_id=? AND kind='delegation_result'`,
		`{"tool":"spawn_agent","content":"{\"agent_id\":\"thread-y\"}"}`, p); err != nil {
		t.Fatal(err)
	}
	digests, _ = dependencyDigestsBoth(t, catalog, p, x)
	if digests[x] == arrived || digests[p] == digests[x] {
		t.Fatalf("after a correction elsewhere, x %s → %s (p %s)", arrived, digests[x], digests[p])
	}
	corrected := digests[p]

	// The result is deleted.
	if _, err := catalog.DB.Exec(`DELETE FROM messages WHERE conversation_id=? AND kind='delegation_result'`, p); err != nil {
		t.Fatal(err)
	}
	digests, _ = dependencyDigestsBoth(t, catalog, p)
	if digests[p] == corrected {
		t.Fatal("deleting the result left the digest unchanged")
	}

	// A retained origin parent names a transcript not yet indexed; when it
	// arrives, the child's digest changes through the origin row alone.
	var origins int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM origin_observations WHERE conversation_id=? AND parent_native_id='thread-z'`, k).Scan(&origins); err != nil || origins == 0 {
		t.Fatalf("fixture has no retained origin parent (%v)", err)
	}
	if _, err := catalog.DB.Exec(`DELETE FROM conversation_parent_refs`); err != nil {
		t.Fatal(err)
	}
	digests, _ = dependencyDigestsBoth(t, catalog, k)
	waiting := digests[k]
	ingestRecordTx(t, catalog, codex("thread-z", "", 0))
	legacy()
	if _, err := catalog.DB.Exec(`UPDATE conversations SET parent_id=NULL WHERE id=?`, k); err != nil {
		t.Fatal(err)
	}
	digests, _ = dependencyDigestsBoth(t, catalog, k, id("thread-z"))
	if digests[k] == waiting || digests[k] != digests[id("thread-z")] {
		t.Fatalf("arrival of a retained origin parent: k %s → %s, z %s", waiting, digests[k], digests[id("thread-z")])
	}
}

// A Copilot agent whose starting call is not in the log gets the session as
// its parent field. The adapter now says so, and that default stays a hint:
// only an agent whose call was read keeps a native parent.
func TestCopilotSessionParentFallback(t *testing.T) {
	ctx := context.Background()
	events := `{"type":"session.start","data":{"sessionId":"sess-f","copilotVersion":"1.0.105","context":{"cwd":"/work/f"}},"id":"f1","timestamp":"2026-11-04T10:00:00.000Z"}
{"type":"user.message","data":{"content":"Split the work","messageId":"u1"},"id":"f2","timestamp":"2026-11-04T10:00:01.000Z"}
{"type":"assistant.message","data":{"messageId":"m1","model":"gpt-6-luna","content":"","toolRequests":[{"toolCallId":"c1","name":"task","arguments":{"prompt":"Check q.sql"}}]},"id":"f3","timestamp":"2026-11-04T10:00:02.000Z"}
{"type":"subagent.started","data":{"toolCallId":"c1","agentName":"explore"},"agentId":"outer","id":"f4","timestamp":"2026-11-04T10:00:02.100Z"}
{"type":"user.message","data":{"content":"Check q.sql","messageId":"o1","source":"agent-sess-f"},"agentId":"outer","id":"f5","timestamp":"2026-11-04T10:00:02.200Z"}
{"type":"assistant.message","data":{"messageId":"m2","model":"gpt-6-luna","content":"","toolRequests":[{"toolCallId":"c2","name":"task","arguments":{"prompt":"Read the schema"}}]},"agentId":"outer","id":"f6","timestamp":"2026-11-04T10:00:03.000Z"}
{"type":"subagent.started","data":{"toolCallId":"c2","agentName":"explore"},"agentId":"inner","id":"f7","timestamp":"2026-11-04T10:00:03.100Z"}
{"type":"user.message","data":{"content":"Read the schema","messageId":"i1","source":"agent-outer"},"agentId":"inner","id":"f8","timestamp":"2026-11-04T10:00:03.200Z"}
{"type":"subagent.started","data":{"toolCallId":"c-rotated","agentName":"explore"},"agentId":"orphan","id":"f9","timestamp":"2026-11-04T10:00:04.000Z"}
{"type":"user.message","data":{"content":"Started by a call this log lacks","messageId":"x1","source":"agent-inner"},"agentId":"orphan","id":"f10","timestamp":"2026-11-04T10:00:04.100Z"}
{"type":"assistant.message","data":{"messageId":"m3","model":"gpt-6-luna","content":"Without a start event."},"agentId":"unstarted","id":"f11","timestamp":"2026-11-04T10:00:05.000Z"}
`
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "session-state", "sess-f", "events.jsonl"), events)
	record := discoverCopilot(t, root)["sess-f"]
	catalog, _ := testCatalog(t)
	ingestRecordTx(t, catalog, record)
	id := func(native string) string { return stableID("conversation", "copilot", "local", native) }

	parentClaims := func(native string) []storedExecutionClaim {
		out := []storedExecutionClaim{}
		for _, claim := range executionClaimsOf(t, catalog.DB, id(native)) {
			if claim.Kind == evidenceParent {
				out = append(out, claim)
			}
		}
		return out
	}
	// Matched calls: the provider's record, the session's or another agent's.
	for native, parent := range map[string]string{"outer": "sess-f", "inner": "outer"} {
		claims := parentClaims(native)
		if len(claims) != 1 || claims[0].Origin != "native" || claims[0].Authority != authorityVerified || claims[0].Rule != "provider-parent-field/v1" ||
			claims[0].Object != executionNativeKey("copilot", "local", parent) {
			t.Fatalf("%s parent claims = %+v", native, claims)
		}
	}
	// Unmatched or missing start: the session default, recorded as such, and
	// never also as the provider's field.
	for native, reason := range map[string]string{"orphan": "unmatched-tool-call", "unstarted": "no-start-event"} {
		claims := parentClaims(native)
		if len(claims) != 1 || claims[0].Source != evidenceSourceAdapter || claims[0].Origin != "synthesized" || claims[0].Authority != authorityFallback ||
			claims[0].Rule != "copilot-session-fallback/v1" || firstString(claims[0].Detail["reason"]) != reason {
			t.Fatalf("%s parent claims = %+v", native, claims)
		}
		// The raw field is preserved.
		var stored string
		if err := catalog.DB.QueryRow(`SELECT parent_native_id FROM conversation_parent_refs WHERE provider='copilot' AND native_id=?`, native).Scan(&stored); err != nil || stored != "sess-f" {
			t.Fatalf("%s retained parent ref = %q (%v)", native, stored, err)
		}
	}

	// Resolution: the matched agents' parents are native; the fallback is at
	// most a hint, whatever core's legacy reading of the same edge.
	resolution, err := catalog.executionResolution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parentOf := func(native string) parentResolution {
		binding := resolution.BindConversation(id(native))
		x := resolution.Execution(binding.ExecutionID)
		if x == nil {
			t.Fatalf("%s binds to %+v", native, binding)
		}
		return x.Parent
	}
	for _, native := range []string{"outer", "inner"} {
		if p := parentOf(native); p.State != parentResolved || p.Certainty != evidenceNative {
			t.Fatalf("%s parent = %s/%s", native, p.State, p.Certainty)
		}
	}
	for _, native := range []string{"orphan", "unstarted"} {
		if p := parentOf(native); p.Certainty > evidenceSynthesized {
			t.Fatalf("%s parent = %s/%s, a session fallback reads as proof", native, p.State, p.Certainty)
		}
	}
	var workspace string
	if err := catalog.DB.QueryRow(`SELECT workspace_id FROM conversations WHERE id=?`, id("orphan")).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.delegationSnapshot(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	certainty := map[string]string{}
	for _, node := range snapshot.Nodes {
		certainty[node.ConversationID] = node.ParentCertainty
	}
	if certainty[id("inner")] != evidenceNative.String() || certainty[id("orphan")] == evidenceNative.String() || certainty[id("unstarted")] == evidenceNative.String() {
		t.Fatalf("explorer parent certainty = %v", certainty)
	}

	// A re-derivation from retained rows (a non-writer merge or a version
	// change) keeps the adapter's fallback and the matched native parent.
	if err := catalog.writeTransaction(ctx, "rederive", func(tx *sql.Tx) error {
		return backfillExecutionEvidence(ctx, tx, []string{id("orphan"), id("outer")}, false)
	}); err != nil {
		t.Fatal(err)
	}
	if claims := parentClaims("orphan"); len(claims) != 1 || claims[0].Authority != authorityFallback {
		t.Fatalf("orphan after re-derivation = %+v", claims)
	}
	if claims := parentClaims("outer"); len(claims) != 1 || claims[0].Origin != "native" {
		t.Fatalf("outer after re-derivation = %+v", claims)
	}

	// Rows from before the adapter recorded fallbacks: a session parent is
	// indistinguishable, so the backfill records it unverified; an agent
	// parent came from a matched call and stays the provider's.
	if _, err := catalog.DB.Exec(`DELETE FROM execution_claims; UPDATE execution_evidence_state SET version='execution-evidence-v2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.BackfillExecutionEvidence(ctx, 10); err != nil {
		t.Fatal(err)
	}
	for _, native := range []string{"outer", "orphan", "unstarted"} {
		if claims := parentClaims(native); len(claims) != 1 || claims[0].Origin != "legacy" || claims[0].Authority != authorityUnverified || claims[0].Rule != "copilot-retained-session-parent/v1" {
			t.Fatalf("backfilled %s = %+v", native, claims)
		}
	}
	if claims := parentClaims("inner"); len(claims) != 1 || claims[0].Origin != "native" || claims[0].Rule != "provider-parent-field/v1" {
		t.Fatalf("backfilled inner = %+v", claims)
	}
	resolution, err = catalog.executionResolution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := parentOf("orphan"); p.Certainty > evidenceLegacy {
		t.Fatalf("backfilled orphan parent = %s/%s", p.State, p.Certainty)
	}
}

// VS Code Copilot sets the session as a subagent's parent until the call
// that started it is read: only an agent whose call is in the session keeps
// the provider's parent; one known only from its own calls is a fallback.
func TestVSCodeCopilotSessionParentFallback(t *testing.T) {
	orphan := `{"kind":"toolInvocationSerialized","toolCallId":"n4","toolId":"copilot_readFile","subAgentInvocationId":"gone","isComplete":true,"pastTenseMessage":{"value":"Read words.py"}},`
	session := strings.Replace(vscodeNestedSession, `{"kind":"toolInvocationSerialized","toolCallId":"n3"`, orphan+`{"kind":"toolInvocationSerialized","toolCallId":"n3"`, 1)
	root := filepath.Join(t.TempDir(), "Code", "User")
	writeSourceFile(t, filepath.Join(root, "globalStorage", "emptyWindowChatSessions", "nested-1.json"), session)
	record := vscodeRecords(t, root)["nested-1"]
	byID := map[string]ConversationRecord{}
	for _, conversation := range record.Conversations {
		byID[conversation.NativeID] = conversation
	}
	for _, native := range []string{"n1", "n2"} {
		if parentFallback(byID[native].Evidence) {
			t.Fatalf("%s was started by a call in the session: %+v", native, byID[native].Evidence)
		}
	}
	gone := byID["gone"]
	if gone.ParentNativeID != "nested-1" || !parentFallback(gone.Evidence) {
		t.Fatalf("agent without its starting call = parent %q, evidence %+v", gone.ParentNativeID, gone.Evidence)
	}
	claims := conversationExecutionEvidence(gone)
	if slices.ContainsFunc(claims, func(claim EvidenceRecord) bool { return claim.Kind == evidenceParent }) {
		t.Fatalf("the record also claims the defaulted field: %+v", claims)
	}
	if claims := conversationExecutionEvidence(byID["n2"]); len(claims) == 0 || claims[0].Kind != evidenceParent || claims[0].Origin != "native" {
		t.Fatalf("started subagent claims = %+v", claims)
	}
}

// cursorForkVariant writes the Cursor fixture with the fork's composer,
// headers and held bubbles replaced, so each case is read from provider
// input by the adapter.
func cursorForkVariant(t *testing.T, length int, headers []string, held map[string]map[string]any) string {
	t.Helper()
	fork := "bubbleId:" + cursorFork + ":"
	rows := []cursorRow{}
	for _, row := range cursorComposerRows() {
		switch {
		case strings.HasPrefix(row.key, fork) || row.key == "composerConvHeaders:"+cursorFork+":000000":
			continue
		case row.key == "composerData:"+cursorFork:
			data := row.value.(map[string]any)
			data["headerLog"] = map[string]any{"chunkCount": 1, "count": len(headers)}
			data["subagentInfo"].(map[string]any)["conversationLengthAtSpawn"] = length
		}
		rows = append(rows, row)
	}
	list := []any{}
	for index, id := range headers {
		list = append(list, map[string]any{"bubbleId": id, "type": 1 + min(index, 1)})
	}
	rows = append(rows, cursorRow{"composerConvHeaders:" + cursorFork + ":000000", list})
	for id, bubble := range held {
		rows = append(rows, cursorRow{fork + id, bubble})
	}
	root := filepath.Join(t.TempDir(), "Cursor", "User")
	cursorDatabase(t, filepath.Join(root, "globalStorage", "state.vscdb"), nil, rows)
	writeSourceFile(t, filepath.Join(root, "workspaceStorage", "w2", "workspace.json"), `{"folder":"file:///work/app"}`)
	return root
}

// A Cursor fork's copies stay in it when the mapping is not proven. Read
// from provider input, a copy carrying the parent's bubble ID is the
// parent's occurrence; a placed copy whose ID diverged, and every copy past
// an unplaced length, is unresolved; the fork's own later messages stay its
// own, whichever copies the capture lacks; and copied tool calls are not the
// fork's calls. Quick stats count the copies out or report partial coverage.
func TestCursorCopyBoundariesFromProviderInput(t *testing.T) {
	ctx := context.Background()
	p := cursorPrompt
	tool := map[string]any{"_v": 3, "type": 2, "capabilityType": 15, "createdAt": "2026-11-03T16:08:24.000Z",
		"toolFormerData": map[string]any{"tool": "15", "name": "task_v2", "toolCallId": "call-2\nfc_2", "status": "completed", "params": `{"description":"Review","prompt":"Review wordstats.py"}`}}
	bubbles := map[string]map[string]any{
		p:                     {"_v": 3, "type": 1, "text": "Write wordstats.py and test it", "createdAt": "2026-11-03T16:08:20.000Z"},
		p + "/s:0x":           {"_v": 3, "type": 2, "text": "Plan the file.", "createdAt": "2026-11-03T16:08:21.000Z"},
		p + "/t:call-2\nfc_2": tool,
		"f1":                  {"_v": 3, "type": 1, "text": "Check again", "createdAt": "2026-11-03T16:08:26.500Z"},
		"f2":                  {"_v": 3, "type": 2, "text": "Still fine.", "createdAt": "2026-11-03T16:08:27.000Z"},
	}
	held := func(ids ...string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, id := range ids {
			out[id] = bubbles[id]
		}
		return out
	}
	// The parent's first five bubbles, with the second's ID diverged.
	diverged := []string{p, p + "/s:0x", p + "/s:1", p + "/t:call-1\nfc_1", p + "/t:call-2\nfc_2", "f1", "f2"}
	same := []string{p, p + "/s:0", p + "/s:1", p + "/t:call-1\nfc_1", p + "/t:call-2\nfc_2", "f1", "f2"}
	bubbles[p+"/s:0"] = bubbles[p+"/s:0x"]
	for _, c := range []struct {
		name    string
		length  int
		headers []string
		held    []string
		roles   map[string]occurrenceRole // by bubble
		mapping string
		legacy  map[string]occurrenceRole // the same rows with a claim from before v3
	}{
		{name: "diverged ID, copied tool", length: 5, headers: diverged, held: []string{p, p + "/s:0x", p + "/t:call-2\nfc_2", "f1", "f2"}, mapping: "unverified",
			roles: map[string]occurrenceRole{p: occurrenceInherited, p + "/s:0x": occurrenceUnresolved, p + "/t:call-2\nfc_2": occurrenceInherited, "f1": occurrenceOriginal, "f2": occurrenceOriginal},
			// The last copied ID was captured, so it still bounds the copies;
			// the length does not place them, so none is proven.
			legacy: map[string]occurrenceRole{p + "/s:0x": occurrenceUnresolved, "f1": occurrenceOriginal, "f2": occurrenceOriginal}},
		{name: "first and last copies not captured", length: 5, headers: diverged, held: []string{p + "/s:0x", "f1", "f2"}, mapping: "unverified",
			roles:  map[string]occurrenceRole{p + "/s:0x": occurrenceUnresolved, "f1": occurrenceOriginal, "f2": occurrenceOriginal},
			legacy: map[string]occurrenceRole{p + "/s:0x": occurrenceUnresolved, "f1": occurrenceUnresolved, "f2": occurrenceUnresolved}},
		{name: "copy length past the headers", length: 9, headers: same, held: []string{p, p + "/s:0", "f1", "f2"}, mapping: "unmapped",
			// Nothing places the copies; a bubble carrying the parent's own
			// ID is still the parent's (the provider's event ID).
			roles: map[string]occurrenceRole{p: occurrenceInherited, p + "/s:0": occurrenceUnresolved, "f1": occurrenceUnresolved, "f2": occurrenceUnresolved}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := cursorForkVariant(t, c.length, c.headers, held(c.held...))
			catalog, _ := testCatalog(t)
			ingestSource(t, catalog, SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true})
			fork := stableID("conversation", "cursor", "local", cursorFork)
			claim := findClaim(executionClaimsOf(t, catalog.DB, fork), evidenceHistoryCopy, "")
			if claim == nil || firstString(claim.Detail["mapping"]) != c.mapping {
				t.Fatalf("fork copy claim = %+v", claim)
			}
			check := func(want map[string]occurrenceRole) {
				t.Helper()
				res, err := resolveExecutionsFrom(ctx, catalog.DB)
				if err != nil {
					t.Fatal(err)
				}
				occurrences, err := res.Occurrences(ctx, catalog.DB, []string{fork})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := queryMaps(catalog.DB, `SELECT id,native_id FROM messages WHERE conversation_id=?`, fork)
				if err != nil {
					t.Fatal(err)
				}
				got := map[string]occurrenceRole{}
				for _, row := range rows {
					bubble := cursorBubble(firstString(row["native_id"]))
					role := occurrences.Lookup(sourceRef{sourceMessage, firstString(row["id"])}, sourceRef{sourceConversation, fork}).Role
					if prior, seen := got[bubble]; seen && prior != role {
						t.Fatalf("bubble %q has messages %s and %s", bubble, prior, role)
					}
					got[bubble] = role
				}
				for bubble, role := range want {
					if got[bubble] != role {
						t.Fatalf("bubble %q: %s, want %s (all %v)", bubble, got[bubble], role, got)
					}
				}
				// Production counts never take a copy for the fork's own.
				window := &usageSummaryWindow{Until: "2100-01-01T00:00:00Z"}
				if err := catalog.addQuickStatsCounts(ctx, map[string]any{"windows": []*usageSummaryWindow{window}}); err != nil {
					t.Fatal(err)
				}
				if counts := window.quickStatsCounts; counts.UnresolvedOccurrences == 0 || counts.OccurrenceCoverage == "complete" {
					t.Fatalf("quick stats = %+v", counts)
				}
			}
			check(c.roles)
			if c.roles[p+"/t:call-2\nfc_2"] == occurrenceInherited {
				// The copied call is the parent's: the fork's own calls leave it out.
				var copied int
				if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE conversation_id=?`, fork).Scan(&copied); err != nil || copied == 0 {
					t.Fatalf("the fork's copied call was not retained (%d, %v)", copied, err)
				}
			}
			if c.legacy != nil {
				// A claim recorded before v3 names only the length and its ends.
				if _, err := catalog.DB.Exec(`UPDATE execution_claims SET detail_json=json_remove(detail_json,'$.ids','$.suspected') WHERE conversation_id=? AND kind=?`, fork, evidenceHistoryCopy); err != nil {
					t.Fatal(err)
				}
				check(c.legacy)
			}
		})
	}
}

// Recovering a Cursor workspace whose source is gone restores the adapter's
// claims with the state that says which read made them: the fork's copies
// still resolve and nothing reports Cursor evidence missing. Claims the
// writer re-derives are never resurrected, and an export from an older
// version, or one without the state, records that the read is incomplete.
func TestRecoveredExecutionEvidence(t *testing.T) {
	ctx := context.Background()
	p := cursorPrompt
	held := map[string]map[string]any{
		p:           {"_v": 3, "type": 1, "text": "Write wordstats.py and test it", "createdAt": "2026-11-03T16:08:20.000Z"},
		p + "/s:0x": {"_v": 3, "type": 2, "text": "Plan the file.", "createdAt": "2026-11-03T16:08:21.000Z"},
		"f1":        {"_v": 3, "type": 1, "text": "Check again", "createdAt": "2026-11-03T16:08:26.500Z"},
	}
	root := cursorForkVariant(t, 5, []string{p, p + "/s:0x", p + "/s:1", p + "/t:call-1\nfc_1", p + "/t:call-2\nfc_2", "f1"}, held)
	source, _ := testCatalog(t)
	ingestSource(t, source, SourceConfig{Name: "cursor", Kind: "cursor", Path: root, Account: "local", Enabled: true})
	fork := stableID("conversation", "cursor", "local", cursorFork)
	var workspace string
	if err := source.DB.QueryRow(`SELECT workspace_id FROM conversations WHERE id=?`, fork).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	// A record claim an older extractor wrote, which the current one does not.
	if _, err := source.DB.Exec(`INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,authority,rule)
		VALUES(?,'obsolete','record','parent','conversation','native:cursor|local|gone','native','verified','obsolete-rule/v0')`, fork); err != nil {
		t.Fatal(err)
	}
	tx, err := source.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := exportHistoryRecovery(ctx, tx, historyUnit{Kind: "catalog-workspace", Original: workspace}, "")
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	recover := func(envelope historyRecovery) *Catalog {
		t.Helper()
		fresh, _ := testCatalog(t)
		if outcome, err := fresh.applyHistoryRecovery(ctx, envelope); err != nil || outcome != "catalog-derived" {
			t.Fatal(outcome, err)
		}
		return fresh
	}
	state := func(c *Catalog) (version, input string) {
		t.Helper()
		if err := c.DB.QueryRow(`SELECT version,input FROM execution_evidence_state WHERE conversation_id=?`, fork).Scan(&version, &input); err != nil {
			t.Fatal(err)
		}
		return
	}
	missing := func(c *Catalog) []string {
		t.Helper()
		evidence, err := loadExecutionEvidence(ctx, c.DB)
		if err != nil {
			t.Fatal(err)
		}
		return evidence.Missing
	}

	recovered := recover(envelope)
	claims := executionClaimsOf(t, recovered.DB, fork)
	if copy := findClaim(claims, evidenceHistoryCopy, ""); copy == nil || copy.Source != evidenceSourceAdapter || firstString(copy.Detail["mapping"]) != "unverified" {
		t.Fatalf("recovered copy claim = %+v", copy)
	}
	if slices.ContainsFunc(claims, func(claim storedExecutionClaim) bool { return claim.Rule == "obsolete-rule/v0" }) {
		t.Fatalf("an obsolete record claim was resurrected: %+v", claims)
	}
	if version, input := state(recovered); version != executionEvidenceVersion || input != "ingest" {
		t.Fatalf("recovered state = %s/%s", version, input)
	}
	if slices.Contains(missing(recovered), missingCursorCopyEvidence) {
		t.Fatalf("recovery reports the restored Cursor evidence missing: %v", missing(recovered))
	}
	res, err := resolveExecutionsFrom(ctx, recovered.DB)
	if err != nil {
		t.Fatal(err)
	}
	occurrences, err := res.Occurrences(ctx, recovered.DB, []string{fork})
	if err != nil {
		t.Fatal(err)
	}
	role := func(native string) occurrenceRole {
		return occurrences.Lookup(sourceRef{sourceMessage, messageCatalogID(fork, native)}, sourceRef{sourceConversation, fork}).Role
	}
	if role(p) != occurrenceInherited || role(p+"/s:0x") != occurrenceUnresolved || role("f1") != occurrenceOriginal {
		t.Fatalf("recovered copies: %s %s %s", role(p), role(p+"/s:0x"), role("f1"))
	}

	// An export whose read predates the current Cursor evidence, and one
	// from before the state was exported: the claims are kept, and the
	// state says a provider read is needed.
	older := envelope
	older.Observations = nil
	stateless := envelope
	stateless.Observations = nil
	for _, o := range envelope.Observations {
		if o.Kind == "execution_evidence_state" {
			column := slices.Index(o.Columns, "version")
			rows := [][]sqliteValue{}
			for _, row := range o.Rows {
				row = slices.Clone(row)
				if row[column], err = encodeSQLiteValue("execution-evidence-v2"); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
			}
			older.Observations = append(older.Observations, historyObservation{Kind: o.Kind, Columns: o.Columns, Rows: rows})
			continue
		}
		older.Observations = append(older.Observations, o)
		stateless.Observations = append(stateless.Observations, o)
	}
	for name, envelope := range map[string]historyRecovery{"older version": older, "no state": stateless} {
		recovered := recover(envelope)
		if version, input := state(recovered); version != executionEvidenceVersion || input != "retained" {
			t.Fatalf("%s: state = %s/%s", name, version, input)
		}
		if !slices.Contains(missing(recovered), missingCursorCopyEvidence) {
			t.Fatalf("%s: an incomplete read is not reported: %v", name, missing(recovered))
		}
		if copy := findClaim(executionClaimsOf(t, recovered.DB, fork), evidenceHistoryCopy, ""); copy == nil {
			t.Fatalf("%s: the adapter's claim was not restored", name)
		}
	}
}

// Authority caps the strength a stored claim resolves with, apart from its
// origin: a native field an adapter only defaulted is a hint, an unverified
// one is legacy, and a parsed envelope that verifies creation stays derived
// (identity authority lifts it). A row from before authorities keeps its
// origin's strength.
func TestStoredClaimStrengthFollowsAuthority(t *testing.T) {
	for _, c := range []struct {
		origin, authority string
		want              evidenceStrength
	}{
		{"native", authorityVerified, evidenceNative},
		{"native", authorityFallback, evidenceSynthesized},
		{"native", authorityUnverified, evidenceLegacy},
		{"native", authorityInferred, evidenceDerived},
		{"derived", authorityVerified, evidenceDerived},
		{"derived", authorityCorroborated, evidenceDerived},
		{"legacy", authorityVerified, evidenceLegacy},
		{"synthesized", authorityVerified, evidenceSynthesized},
		{"native", "", evidenceNative},
	} {
		if got := (storedExecutionClaim{Origin: c.origin, Authority: c.authority}).strength(); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.origin, c.authority, got, c.want)
		}
	}
	// Through the loader: the same native parent field with each authority.
	ctx := context.Background()
	catalog, _ := testCatalog(t)
	ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "root", SourceKind: "opencode", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "root", Provider: "opencode", Account: "local", Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "go"}}},
		{NativeID: "child", Provider: "opencode", Account: "local", ParentNativeID: "root", AgentDepth: 1, Messages: []MessageRecord{{NativeID: "c", Role: "user", Kind: "message", Text: "sub"}}},
	}})
	child := stableID("conversation", "opencode", "local", "child")
	for authority, want := range map[string]evidenceStrength{authorityVerified: evidenceNative, authorityFallback: evidenceSynthesized, authorityUnverified: evidenceLegacy} {
		if _, err := catalog.DB.Exec(`UPDATE execution_claims SET authority=? WHERE conversation_id=? AND kind='parent'`, authority, child); err != nil {
			t.Fatal(err)
		}
		evidence, err := loadExecutionEvidence(ctx, catalog.DB)
		if err != nil {
			t.Fatal(err)
		}
		strongest := evidenceStrength(0)
		for _, claim := range evidence.Claims {
			if claim.Kind == claimTranscriptParent && claim.Subject.ID == child && claim.Rule == "provider-parent-field/v1" {
				strongest = max(strongest, claim.Strength)
			}
		}
		if strongest != want {
			t.Fatalf("native/%s parent loads as %s, want %s", authority, strongest, want)
		}
	}
}

// Evidence changed elsewhere reaches every reader of the labels and agent
// events built on it before any rerun. The readers are the transcript's
// summaries; the label table and the label facts dashboards read (pending
// labels read as unlabeled); labels status and Ask's label coverage; and
// Fixes, Safety and Ask's agent-event coverage. That holds both when the
// change comes through ingest, which marks dependents in its transaction,
// and when it comes from a writer that does not mark (a repair, or another
// process's older executable). The ordinary runs then rebuild the
// derivatives without a manual step, reusing every inference.
func TestDerivativesExposeEvidenceChangedElsewhere(t *testing.T) {
	// unmarkedWrite is a writer that marks nothing: a repair, a library
	// transfer, or another process's older executable.
	unmarkedWrite := func(t *testing.T, catalog *Catalog, statements ...string) {
		t.Helper()
		if err := catalog.writeTransaction(t.Context(), "repair", func(tx *sql.Tx) error {
			for _, statement := range statements {
				if _, err := tx.Exec(statement); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A claim-only link from c-events to the fixture's root: no parent_id.
	const claimOnly = `INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id,parent_provider,parent_account)
	 SELECT 'c-events','late-link',1,'test',native_id,provider,account FROM conversations WHERE id='c-root'`
	carrier := WorkspaceRecord{SourceID: "elsewhere", SourceKind: "opencode", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "late-child", Provider: "opencode", Account: "local", ParentNativeID: "c-events", ParentProvider: "claude", ParentAccount: "local", AgentDepth: 1,
			Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "Check the report", Selected: true}}}}}
	for _, late := range []struct {
		name string
		// built runs after the first runs, before the change.
		setup, built, change func(*testing.T, *Catalog)
		// ownership: the change moves which of c-events' observations are its
		// own, so the rebuild may label and score other units. unchanged: the
		// change must leave c-events current.
		ownership, unchanged bool
	}{
		// Another tool's transcript arrives naming c-events as its parent:
		// c-events' own rows are unchanged, its execution evidence is not.
		{name: "ingest", change: func(t *testing.T, catalog *Catalog) { ingestRecordTx(t, catalog, carrier) }},
		// A writer that marks nothing moves c-child under c-events.
		{name: "unmarked writer", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `UPDATE conversations SET parent_id='c-events',agent_depth=1 WHERE id='c-child'`)
		}},
		// c-events declares a copy of an original the catalog does not have;
		// a library transfer then brings the original.
		{name: "late original", setup: func(t *testing.T, catalog *Catalog) {
			execTx(t, catalog.DB, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule)
			 VALUES('c-events','copy','record','history_copy','conversation','native:claude|local|late-orig','native','test/v1')`)
		}, change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES('late-orig','ws-main','claude','local','late-orig','2026-10-25T11:00:00.000Z')`)
		}},
		// A child arrives whose only link to c-events is its parent claim.
		{name: "claim-only parent", change: func(t *testing.T, catalog *Catalog) {
			if err := catalog.writeTransaction(t.Context(), "repair", func(tx *sql.Tx) error {
				if _, _, err := ingestRecordContext(t.Context(), tx, carrier, false, currentHost().ID, "", false, false); err != nil {
					return err
				}
				_, err := tx.Exec(`UPDATE conversations SET parent_id=NULL WHERE native_id='late-child'`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}},
		// A conversation carrying the only claim joining c-events' component,
		// with no derivative of its own, is deleted; the process restarts.
		{name: "unledgered carrier deleted", setup: func(t *testing.T, catalog *Catalog) { ingestRecordTx(t, catalog, carrier) },
			built: func(t *testing.T, catalog *Catalog) {
				unmarkedWrite(t, catalog, `DELETE FROM message_labels_conversations WHERE conversation_id IN (SELECT id FROM conversations WHERE native_id='late-child')`,
					`DELETE FROM agent_events_conversations WHERE conversation_id IN (SELECT id FROM conversations WHERE native_id='late-child')`)
			}, change: func(t *testing.T, catalog *Catalog) {
				unmarkedWrite(t, catalog, `DELETE FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE native_id='late-child')`,
					`DELETE FROM conversations WHERE native_id='late-child'`)
				forgetDependencyCaches()
			}},
		// An upgraded catalog that has never marked, then a claim-only change.
		{name: "no cursor", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `DELETE FROM meta WHERE key='`+executionDependentsKey+`'`, claimOnly)
		}},
		// Derivatives built under other resolver or evidence versions: every
		// digest differs, with no source change.
		{name: "versions changed", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `UPDATE meta SET value='older' WHERE key='`+executionDependentsVersionsKey+`'`,
				`UPDATE message_labels_conversations SET execution_digest='older'||execution_digest`,
				`UPDATE agent_events_conversations SET execution_digest='older'||execution_digest`)
			forgetDependencyCaches()
		}},
		// Derivatives from before digests were recorded, with no source
		// change: the rebuild must read current again although the
		// propagation state never moves.
		{name: "no digests", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `UPDATE message_labels_conversations SET execution_digest=''`, `UPDATE agent_events_conversations SET execution_digest=''`)
			forgetDependencyCaches()
		}},
		// The same, then a claim-only change.
		{name: "no digests, then a change", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `UPDATE message_labels_conversations SET execution_digest=''`, `UPDATE agent_events_conversations SET execution_digest=''`, claimOnly)
		}},
		// An older executable republishes c-events after readers cached an
		// empty unmarked set.
		{name: "older executable publishes", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `UPDATE message_labels_conversations SET execution_digest='' WHERE conversation_id='c-events'`,
				`UPDATE agent_events_conversations SET execution_digest='' WHERE conversation_id='c-events'`)
		}},
		// A released catalog opened by this executable (whose Initialize
		// creates the stale-event marks): ledgers built without digests or
		// members, until the next run migrates them.
		{name: "released ledgers", change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog,
				`CREATE TABLE released_labels (conversation_id TEXT PRIMARY KEY, structure TEXT NOT NULL, units INTEGER NOT NULL, built_at TEXT NOT NULL)`,
				`INSERT INTO released_labels SELECT conversation_id,structure,units,built_at FROM message_labels_conversations`,
				`DROP TABLE message_labels_conversations`, `ALTER TABLE released_labels RENAME TO message_labels_conversations`,
				`CREATE TABLE released_events (conversation_id TEXT PRIMARY KEY, structure TEXT NOT NULL, built_at TEXT NOT NULL, since TEXT NOT NULL DEFAULT '')`,
				`INSERT INTO released_events SELECT conversation_id,structure,built_at,since FROM agent_events_conversations`,
				`DROP TABLE agent_events_conversations`, `ALTER TABLE released_events RENAME TO agent_events_conversations`,
				`DROP TABLE execution_dependency_members`, `DROP TABLE execution_dependents_stamp`,
				`DELETE FROM meta WHERE key IN ('labels_schema_version','agent_events_ledger_version','`+executionDependentsKey+`','`+executionDependentsVersionsKey+`')`)
			forgetDependencyCaches()
		}},
		// Occurrence ownership. c-events is a fork of c-root whose first
		// message carries a provider event ID; whether c-root holds that event
		// decides whether c-events' copy is its own.
		{name: "occurrence original arrives", ownership: true, setup: forkOfRoot(false), change: func(t *testing.T, catalog *Catalog) {
			markingWrite(t, catalog, rootOriginal)
		}},
		{name: "occurrence original deleted", ownership: true, setup: forkOfRoot(true), change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `DELETE FROM messages WHERE id='m-occ-original'`)
			forgetDependencyCaches()
		}},
		// The original's recorded stream decides who owns the copy. The
		// session already records another message, so only the copy's
		// owner changes.
		{name: "occurrence original's session", setup: func(t *testing.T, catalog *Catalog) {
			forkOfRoot(true)(t, catalog)
			execTx(t, catalog.DB, `INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('s-occ','m-a1',0)`)
		}, change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('s-occ','m-occ-original',1)`)
		}},
		// A typed copy declaration names a source the catalog holds; only the
		// source's original of the copied event arrives, so the evidence
		// graph is unchanged and the copy's occurrence facts are not.
		{name: "occurrence typed source's original arrives", ownership: true, setup: func(t *testing.T, catalog *Catalog) {
			forkOfRoot(false)(t, catalog)
			execTx(t, catalog.DB, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES('typed-src','ws-main','claude','local','typed-src','2026-09-01T11:00:00.000Z');
			 INSERT INTO execution_evidence_state(conversation_id,version,input,id_policy) VALUES('c-events','`+executionEvidenceVersion+`','ingest','provider-event/v1')
			  ON CONFLICT(conversation_id) DO UPDATE SET id_policy='provider-event/v1';
			 INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule)
			 VALUES('c-events','typed-copy','record','history_copy','conversation','native:claude|local|typed-src','native','test/v1')`)
		}, change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order)
			 VALUES('m-typed-original','typed-src','evt-occ-1','user','message','Fetch the report for October.','h1','2026-09-01T11:00:30.000Z',1,1)`)
		}},
		// A request c-events copied with its first message loses the stored
		// link to the message that carried its usage: its occurrence is
		// unresolved, so what was built on it is pending.
		{name: "occurrence request's usage link", setup: func(t *testing.T, catalog *Catalog) {
			forkOfRoot(true)(t, catalog)
			execTx(t, catalog.DB, `INSERT INTO model_requests(id,conversation_id,sequence,source_message_id,final_message_id,total_tokens)
			 VALUES('r-occ-original','c-root',0,'m-occ-original','m-occ-original',10),('r-occ-copy','c-events',0,'m-ev-user','m-ev-user',10)`)
		}, change: func(t *testing.T, catalog *Catalog) {
			unmarkedWrite(t, catalog, `UPDATE model_requests SET source_message_id=NULL WHERE id='r-occ-copy'`)
		}},
		// An append to the parent whose event ID no copy shares.
		{name: "occurrence unrelated append", unchanged: true, setup: forkOfRoot(false), change: func(t *testing.T, catalog *Catalog) {
			markingWrite(t, catalog, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order)
			 VALUES('m-occ-other','c-root','evt-occ-other','user','message','Another thing','h-occ-other','2026-09-01T12:30:00.000Z',1,1500)`)
		}},
	} {
		t.Run(late.name, func(t *testing.T) {
			exposeEvidenceChangedElsewhere(t, late.setup, late.built, late.change, late.ownership, late.unchanged)
		})
	}
}

// The unmarked sets are cached per ledger stamp. A write to the events
// ledger alone (another executable republishing c-events without a digest)
// must reach a warmed events set and the cached-view generation, and leave
// the labels set alone.
func TestUnmarkedSetFollowsEventLedgerWrites(t *testing.T) {
	ctx := t.Context()
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	if stats, err := catalog.RunLabels(ctx, labels, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("labels: %+v %v", stats, err)
	}
	if stats, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("agent events: %+v %v", stats, err)
	}
	read := func() (events, labeled []string, generation int64) {
		t.Helper()
		events, err := agentEventsWithheld(ctx, catalog.reader())
		if err != nil {
			t.Fatal(err)
		}
		if labeled, err = executionUnmarked(ctx, catalog.reader(), "message_labels_conversations"); err != nil {
			t.Fatal(err)
		}
		if err := catalog.DB.QueryRow(`SELECT value FROM query_cache_generation WHERE id=1`).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return
	}
	if events, labeled, _ := read(); len(events) != 0 || len(labeled) != 0 {
		t.Fatalf("a fresh build is withheld: events %v labels %v", events, labeled)
	}
	_, _, before := read() // warm
	sourceBefore, _, _ := executionSourceGeneration(ctx, catalog.DB)
	if err := catalog.writeTransaction(ctx, "older executable", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE agent_events_conversations SET execution_digest='' WHERE conversation_id='c-events'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if source, _, _ := executionSourceGeneration(ctx, catalog.DB); source != sourceBefore {
		t.Fatalf("precondition: the source generation moved %d → %d", sourceBefore, source)
	}
	events, labeled, after := read()
	if !slices.Equal(events, []string{"c-events"}) || len(labeled) != 0 || after <= before {
		t.Fatalf("after an events-only ledger write: events %v labels %v cached-view generation %d → %d", events, labeled, before, after)
	}
	if stats, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{}); err != nil || !stats.Complete || stats.ConversationsBuilt == 0 {
		t.Fatalf("agent events: %+v %v", stats, err)
	}
	if events, _, _ := read(); len(events) != 0 {
		t.Fatalf("after the run: withheld %v", events)
	}
}

// rootOriginal is c-root's message with the event ID forkOfRoot gives
// c-events' first message.
const rootOriginal = `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order)
 VALUES('m-occ-original','c-root','evt-occ-1','user','message','Fetch the report for October.','h1','2026-09-01T12:00:30.000Z',1,1400)`

// forkOfRoot makes c-events a fork of c-root with a session there, its first
// message carrying a provider event ID, and (original) c-root's copy of it.
func forkOfRoot(original bool) func(*testing.T, *Catalog) {
	return func(t *testing.T, catalog *Catalog) {
		statements := []string{`UPDATE conversations SET parent_id='c-root',agent_depth=1 WHERE id='c-events'`,
			`UPDATE messages SET native_id='evt-occ-1' WHERE id='m-ev-user'`,
			`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s-occ','ws-main','c-root','s-occ','root','claude')`}
		if original {
			statements = append(statements, rootOriginal)
		}
		execTx(t, catalog.DB, strings.Join(statements, ";\n"))
	}
}

// markingWrite is a writer that marks dependents in its transaction, as
// ingestion does.
func markingWrite(t *testing.T, catalog *Catalog, statements ...string) {
	t.Helper()
	if err := catalog.writeTransaction(t.Context(), "marking writer", func(tx *sql.Tx) error {
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		return markExecutionDependents(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

// forgetDependencyCaches is a process restart for the dependency caches.
func forgetDependencyCaches() {
	dependencyInverseCache.Lock()
	dependencyInverseCache.entries = nil
	dependencyInverseCache.Unlock()
	dependencyDigestCache.Lock()
	dependencyDigestCache.entries = nil
	dependencyDigestCache.Unlock()
	executionUnmarkedCache.Lock()
	executionUnmarkedCache.entries = nil
	executionUnmarkedCache.Unlock()
}

// freshnessAgrees checks every built conversation's labels and events: the
// label table, labels status and the event datasets hold back exactly what
// the transcript reports as pending.
func freshnessAgrees(t *testing.T, catalog *Catalog) {
	t.Helper()
	ctx := t.Context()
	table, _ := catalog.sqlDatasetFor("message_labels")
	rows, err := queryMapsContext(ctx, catalog.reader(), table.project([]string{"id", "conversation_id", "label_status"}).querySourceSQL(PharosMessageLabelsSchema()))
	if err != nil {
		t.Fatal(err)
	}
	shown := map[string]bool{}
	for _, row := range rows {
		if row["label_status"] != "unlabeled" {
			shown[firstString(row["conversation_id"])] = true
		}
	}
	withheld, err := agentEventsWithheld(ctx, catalog.reader())
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	dirty, err := queryMapsContext(ctx, catalog.DB, `SELECT conversation_id FROM message_labels_dirty`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range dirty {
		pending[firstString(row["conversation_id"])] = true
	}
	for _, ledger := range []struct {
		table, dirty string
		current      func(context.Context, queryer, string) (bool, error)
	}{{"message_labels_conversations", "message_labels_dirty", labelsExecutionCurrent}, {"agent_events_conversations", "agent_events_dirty", agentEventsExecutionCurrent}} {
		built, err := queryMapsContext(ctx, catalog.DB, `SELECT conversation_id id,EXISTS(SELECT 1 FROM `+ledger.dirty+` d WHERE d.conversation_id=l.conversation_id) dirty FROM `+ledger.table+` l`)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range built {
			id := firstString(row["id"])
			current, err := ledger.current(ctx, catalog.reader(), id)
			if err != nil {
				t.Fatal(err)
			}
			transcriptPending := !current || integer(row["dirty"]) == 1
			if ledger.dirty == "message_labels_dirty" {
				if transcriptPending {
					pending[id] = true
				}
				if transcriptPending && shown[id] {
					t.Errorf("%s: the label table shows labels the transcript reports pending", id)
				}
			} else if !current && !slices.Contains(withheld, id) {
				t.Errorf("%s: the event datasets show events resting on superseded evidence", id)
			}
		}
	}
	status, err := catalog.LabelsStatus(ctx, LabelerSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if got := int(integer(status["dirty_conversations"])); got != len(pending) {
		t.Errorf("labels status counts %d pending, the transcript %d", got, len(pending))
	}
}

func exposeEvidenceChangedElsewhere(t *testing.T, setup, built, change func(*testing.T, *Catalog), ownership, unchanged bool) {
	ctx := t.Context()
	catalog := agentEventsCatalog(t)
	if setup != nil {
		setup(t, catalog)
	}
	labels, settings := agentEventsTestSettings(t)
	run := func() (*LabelsRunStats, *AgentEventsRunStats) {
		t.Helper()
		labeled, err := catalog.RunLabels(ctx, labels, LabelsRunOptions{})
		if err != nil || !labeled.Complete {
			t.Fatalf("labels: %+v %v", labeled, err)
		}
		events, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{})
		if err != nil || !events.Complete {
			t.Fatalf("agent events: %+v %v", events, err)
		}
		return labeled, events
	}
	run()
	if built != nil {
		built(t, catalog)
	}
	// Dashboards read the label facts once they are built.
	if err := catalog.ensureMessageLabelFacts(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		n, err := catalog.refreshMessageLabelFacts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if ready, _ := catalog.metaValue(ctx, "message_label_facts_ready"); ready != messageLabelFactsVersion() {
		t.Fatalf("label facts not ready: %q", ready)
	}
	type view struct {
		labelsPending, eventsPending bool
		statuses, facts              map[string]int
		changed, labelsDirty         int
		labelCoverage                int
		fixes, safety, askNote       string
		// Agent Experience datasets: c-events' rows through the catalog's
		// dataset routing, and its event count and coverage over HTTP.
		eventRows, eventMessages, apiEvents int
		apiCoverage                         string
	}
	config := testConfig(t)
	config.CatalogPath = catalog.Path
	server := testServer(t, config, catalog)
	read := func() view {
		t.Helper()
		conversation, err := catalog.WorkConversationContext(ctx, "ws-main", "c-events")
		if err != nil || conversation == nil {
			t.Fatalf("transcript: %v", err)
		}
		v := view{statuses: map[string]int{}, facts: map[string]int{}}
		if summary, _ := conversation["labels"].(map[string]any); summary != nil {
			v.labelsPending = summary["pending"] == true
		}
		if summary, _ := conversation["agent_events"].(map[string]any); summary != nil {
			v.eventsPending = summary["pending"] == true
		}
		// The Query Table sources: the live label table, and the stored facts
		// the catalog routes label queries and dashboards to.
		live := messageLabelLiveDatasetFor("", executionUnmarkedSQL(ctx, catalog.reader(), "message_labels_conversations"))
		stored, _ := catalog.sqlDatasetFor("message_labels")
		for table, into := range map[*sqlDataset]map[string]int{&live: v.statuses, &stored: v.facts} {
			projected, _ := table.project([]string{"id", "conversation_id", "label_status"}).preferCoveringScan(nil)
			rows, err := queryMapsContext(ctx, catalog.reader(), projected.querySourceSQL(PharosMessageLabelsSchema()))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row["conversation_id"] == "c-events" {
					into[firstString(row["label_status"])]++
				}
			}
		}
		labelStatus, err := catalog.LabelsStatus(ctx, labels)
		if err != nil {
			t.Fatal(err)
		}
		v.labelsDirty = int(integer(labelStatus["dirty_conversations"]))
		ask, err := askLabelCoverage(ctx, &askSignalRun{c: catalog, now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		v.labelCoverage = ask.Support
		for dataset, into := range map[string]*int{"agent_events": &v.eventRows, "agent_event_messages": &v.eventMessages} {
			table, _ := catalog.sqlDatasetFor(dataset)
			schema := map[string]qt.Schema{"agent_events": PharosAgentEventsSchema(), "agent_event_messages": PharosAgentEventMessagesSchema()}[dataset]
			rows, err := queryMapsContext(ctx, catalog.reader(), table.querySourceSQL(schema))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row["conversation_id"] == "c-events" {
					*into++
				}
			}
		}
		request := groupRequest("conversation_id", qt.SummaryColumn{ID: "events", Op: "count"})
		w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/agent_events/query-v3", request)
		var response struct {
			Rows     []map[string]any `json:"rows"`
			Coverage map[string]any   `json:"coverage"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Coverage == nil {
			t.Fatal(w.Code, w.Body.String())
		}
		v.apiCoverage = firstString(response.Coverage["state"])
		for _, row := range response.Rows {
			if strings.Contains(jsonText(row), `"c-events"`) {
				v.apiEvents = int(integer(row["events"]))
			}
		}
		status, err := catalog.agentEventsStatus(ctx, Config{Labels: labels, AgentEvents: settings})
		if err != nil {
			t.Fatal(err)
		}
		v.changed = status["conversations_changed"].(int)
		fixes, err := catalog.FixesOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		v.fixes = firstString(fixes["events_freshness"].(map[string]any)["state"])
		safety, err := catalog.SafetyOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		v.safety = firstString(safety["events_freshness"].(map[string]any)["state"])
		// What the Ask agent-event signals add to their lead.
		if v.askNote, err = agentEventsFreshnessNote(ctx, catalog.reader()); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	if before.labelsPending || before.eventsPending || before.changed != 0 || before.fixes != "current" || before.safety != "current" || before.askNote != "" {
		t.Fatalf("a fresh build reads as pending: %+v", before)
	}
	labeledRows := before.statuses["matched"] + before.statuses["no_match"]
	if labeledRows == 0 || !maps.Equal(before.facts, before.statuses) || before.labelsDirty != 0 || before.labelCoverage == 0 ||
		before.eventRows == 0 || before.eventMessages == 0 || before.apiEvents != before.eventRows || before.apiCoverage != "current" {
		t.Fatalf("the fixture's transcript has no labels to go stale: %+v", before)
	}

	freshnessAgrees(t, catalog)

	digest := func() string {
		t.Helper()
		digests, err := executionDependencyDigestsFor(ctx, catalog.DB, []string{"c-events"})
		if err != nil {
			t.Fatal(err)
		}
		return digests["c-events"]
	}
	digest0 := digest()
	change(t, catalog)
	stale := read()
	freshnessAgrees(t, catalog)
	if unchanged {
		// c-events' evidence did not change: its digest is equal and it
		// reads current. (The labels ledger's own rule that forks follow
		// their source may still rebuild it.)
		if digest() != digest0 || stale.labelsPending || stale.eventsPending || !maps.Equal(stale.statuses, before.statuses) || !maps.Equal(stale.facts, before.facts) ||
			stale.eventRows != before.eventRows || stale.apiEvents != before.apiEvents {
			t.Fatalf("a change outside c-events' evidence made it pending: %+v", stale)
		}
		run()
		freshnessAgrees(t, catalog)
		return
	}
	if !stale.labelsPending || !stale.eventsPending || stale.statuses["unlabeled"] != before.statuses["unlabeled"]+labeledRows || stale.changed == 0 ||
		stale.fixes != "pending" || stale.safety != "pending" || stale.askNote == "" {
		t.Fatalf("before a rerun, stored derivatives read as current: %+v", stale)
	}
	// Dashboards, labels status and Ask agree with the transcript.
	if !maps.Equal(stale.facts, stale.statuses) || stale.labelsDirty == 0 || stale.labelCoverage >= before.labelCoverage {
		t.Fatalf("before a rerun, label facts, status or Ask read as current: %+v", stale)
	}
	// The event datasets leave c-events' rows out, and the query says why.
	if stale.eventRows != 0 || stale.eventMessages != 0 || stale.apiEvents != 0 || stale.apiCoverage != "pending" {
		t.Fatalf("before a rerun, agent-event rows read as current: %+v", stale)
	}

	labeled, events := run()
	if labeled.ConversationsBuilt == 0 || events.ConversationsBuilt == 0 || !ownership && (labeled.MessagesLabeled != 0 || events.MessagesScored != 0) {
		t.Fatalf("the rebuild did not reuse inference: labels %+v events %+v", labeled, events)
	}
	after := read()
	freshnessAgrees(t, catalog)
	if ownership {
		// Which units are c-events' own may differ now; nothing is pending,
		// and every reader agrees.
		before.statuses, before.facts, before.eventRows, before.eventMessages, before.apiEvents, before.labelCoverage =
			after.statuses, after.statuses, after.eventRows, after.eventMessages, after.eventRows, after.labelCoverage
	}
	if after.labelsPending || after.eventsPending || !maps.Equal(after.statuses, before.statuses) || !maps.Equal(after.facts, before.facts) || after.changed != 0 ||
		after.labelsDirty != 0 || after.labelCoverage != before.labelCoverage || after.fixes != "current" || after.safety != "current" || after.askNote != "" ||
		after.eventRows != before.eventRows || after.eventMessages != before.eventMessages || after.apiEvents != before.apiEvents || after.apiCoverage != "current" {
		t.Fatalf("the ordinary runs left derivatives pending: %+v", after)
	}
	// Publication changed no evidence: nothing is pending again.
	if again, more := run(); again.ConversationsBuilt != 0 || more.ConversationsBuilt != 0 {
		t.Fatalf("publication invalidated its own output: %+v %+v", again, more)
	}
}

// A write transaction's reads never fill the dependency caches. If they did,
// a transaction that rolled back would leave entries keyed by a source
// generation the next commit reaches again with other rows. With the
// component limit lowered, marking inside a transaction reads catalog-wide
// digests, which is what a large family does. A rolled-back ingest of x under
// c-root with alias-A, then a committed ingest of x under c-events with
// alias-B at the same generation, must mark c-events and leave the caches
// with the committed rows.
func TestDependencyCachesIgnoreRolledBackWrites(t *testing.T) {
	ctx := t.Context()
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	if stats, err := catalog.RunLabels(ctx, labels, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("labels: %+v %v", stats, err)
	}
	if stats, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("agent events: %+v %v", stats, err)
	}
	override(t, &executionDependencyLimit, 1)
	record := func(parent, alias string) WorkspaceRecord {
		return WorkspaceRecord{SourceID: "elsewhere", SourceKind: "opencode", Account: "local", Conversations: []ConversationRecord{
			{NativeID: "x", Provider: "opencode", Account: "local", ParentNativeID: parent, ParentProvider: "claude", ParentAccount: "local", AgentDepth: 1, Aliases: []string{alias},
				Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "Check the report", Selected: true}}}}}
	}
	generation := func(q queryer) int64 {
		t.Helper()
		value, _, err := executionSourceGeneration(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	// Reads that cannot be cached: the rows as they are, for comparison.
	type uncached struct{ queryer }
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record("c-root", "alias-A"), false); err != nil {
		t.Fatal(err)
	}
	rolledBack := generation(tx)
	if _, err := dependencyInverseIndex(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := executionDependencyDigests(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	ingestRecordTx(t, catalog, record("c-events", "alias-B"))
	if committed := generation(catalog.DB); committed != rolledBack {
		t.Fatalf("precondition: the commit reached generation %d, the rollback %d", committed, rolledBack)
	}

	inverse, err := dependencyInverseIndex(ctx, catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	exact := rereadDependencyInverse(t, catalog.DB)
	if !reflect.DeepEqual(inverse.aliases, exact.aliases) || !reflect.DeepEqual(inverse.origins, exact.origins) || !reflect.DeepEqual(inverse.handles, exact.handles) {
		t.Fatalf("inverse index after the commit = %v, rows = %v", inverse.aliases, exact.aliases)
	}
	if !slices.ContainsFunc(slices.Collect(maps.Keys(exact.aliases)), func(key string) bool { return strings.Contains(key, "alias-B") }) {
		t.Fatalf("the committed alias is not in the index: %v", exact.aliases)
	}
	digests, err := executionDependencyDigests(ctx, catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := executionDependencyDigests(ctx, uncached{catalog.DB}); err != nil || !maps.Equal(digests, rows) {
		t.Fatalf("digests after the commit differ from the rows (%v)", err)
	}
	// The committed change marked its real dependent, and only the rows decide.
	for _, ledger := range executionDependentLedgers {
		var marked int
		if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM ` + ledger[1] + ` WHERE conversation_id='c-events'`).Scan(&marked); err != nil || marked != 1 {
			t.Fatalf("%s: c-events marked %d (%v)", ledger[1], marked, err)
		}
	}
	if current, err := storedExecutionCurrent(ctx, catalog.DB, "agent_events_conversations", "c-events"); err != nil || current {
		t.Fatalf("c-events' events read as current after its evidence changed (%v)", err)
	}
	// A committed read at the next generation fills the cache again.
	execTx(t, catalog.DB, `UPDATE conversations SET agent_nickname='n' WHERE id='c-root'`)
	if _, err := executionDependencyDigests(ctx, catalog.DB); err != nil {
		t.Fatal(err)
	}
	file, at, err := dependencyCacheKey(ctx, catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	dependencyDigestCache.Lock()
	entry := dependencyDigestCache.entries[file]
	dependencyDigestCache.Unlock()
	if entry.generation != at {
		t.Fatalf("a committed read did not fill the cache: %d, want %d", entry.generation, at)
	}
}

// The transcript's agent-events reads stop with the request.
func TestAgentEventsAttacherStopsWithRequest(t *testing.T) {
	ctx := t.Context()
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	if stats, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("agent events: %+v %v", stats, err)
	}
	if _, summary, _, err := catalog.agentEventsAttacherContext(ctx, "c-events"); err != nil {
		t.Fatal(err)
	} else if out, err := summary(); err != nil || out == nil {
		t.Fatalf("summary = %v (%v)", out, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := catalog.agentEventsAttacherContext(canceled, "c-events"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled request read the events: %v", err)
	}
	live, stop := context.WithCancel(ctx)
	_, summary, _, err := catalog.agentEventsAttacherContext(live, "c-events")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := summary(); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled request read the summary: %v", err)
	}
}

// Cached views follow the unmarked set. A change that moves no digest leaves
// a warmed view current, before and after a mark that only advances the
// cursor (which moves the cached-view generation, not the resolver's). A
// change that does move one, by a writer that marks nothing, turns the
// warmed view pending without a mark, and the mark keeps it so.
func TestCursorOnlyMarkRefreshesCachedViews(t *testing.T) {
	ctx := t.Context()
	catalog := agentEventsCatalog(t)
	labels, _ := agentEventsTestSettings(t)
	if stats, err := catalog.RunLabels(ctx, labels, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("labels: %+v %v", stats, err)
	}
	// A labeled conversation that was one component with c-child when built.
	var sibling string
	if err := catalog.DB.QueryRow(`SELECT me.conversation_id FROM message_labels_conversations me JOIN message_labels_conversations child
	 ON child.conversation_id='c-child' AND me.execution_digest=child.execution_digest AND me.conversation_id<>'c-child'
	 JOIN message_labels l ON l.conversation_id=me.conversation_id ORDER BY 1 LIMIT 1`).Scan(&sibling); err != nil {
		t.Fatalf("no labeled family member of c-child: %v", err)
	}
	labeled := func() (int, error) {
		table, _ := catalog.sqlDatasetFor("message_labels")
		rows, err := queryMapsContext(ctx, catalog.reader(), table.project([]string{"id", "conversation_id", "label_status"}).querySourceSQL(PharosMessageLabelsSchema()))
		n := 0
		for _, row := range rows {
			if row["conversation_id"] == sibling && row["label_status"] != "unlabeled" {
				n++
			}
		}
		return n, err
	}
	cached := func() int {
		t.Helper()
		n, err := cachedValue(ctx, catalog, "test:sibling-labels", func(context.Context) (int, error) { return labeled() })
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	current := cached()
	if current == 0 {
		t.Fatalf("%s has no labels", sibling)
	}
	// A writer that marks nothing edits a reply in c-child: no launch, so no
	// digest moves, and nothing reads as pending.
	if err := catalog.writeTransaction(ctx, "edit", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE messages SET text=text||' (edited)' WHERE id='m-c2'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if behind, err := executionDependentsBehind(ctx, catalog.reader()); err != nil || behind == 0 {
		t.Fatalf("the edit left nothing unmarked (%d, %v)", behind, err)
	}
	if n := cached(); n != current {
		t.Fatalf("an edit that moved no digest hid %s's labels (%d of %d)", sibling, n, current)
	}
	var dirty int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty WHERE conversation_id=?`, sibling).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("precondition: %s is dirty (%d, %v)", sibling, dirty, err)
	}
	resolverBefore, _, err := executionSourceGeneration(ctx, catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.propagateExecutionChanges(ctx); err != nil {
		t.Fatal(err)
	}
	// The mark invalidates cached views, not the resolver's generation.
	if resolverAfter, _, err := executionSourceGeneration(ctx, catalog.DB); err != nil || resolverAfter != resolverBefore {
		t.Fatalf("the mark moved the execution source generation %d → %d (%v)", resolverBefore, resolverAfter, err)
	}
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty WHERE conversation_id=?`, sibling).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("the mark marked %s (%d, %v): not a cursor-only mark", sibling, dirty, err)
	}
	if behind, err := executionDependentsBehind(ctx, catalog.reader()); err != nil || behind != 0 {
		t.Fatalf("cursor behind by %d (%v)", behind, err)
	}
	if n := cached(); n != current {
		t.Fatalf("after a cursor-only mark the cached view shows %d of %d labels", n, current)
	}
	// A claim-only link from the sibling to c-events, unmarked.
	if err := catalog.writeTransaction(ctx, "link", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id,parent_provider,parent_account)
		 SELECT ?,'late-link',1,'test',native_id,provider,account FROM conversations WHERE id='c-events'`, sibling)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := cached(); n != 0 {
		t.Fatalf("an unmarked change left %s's labels current in the warmed view (%d)", sibling, n)
	}
	if err := catalog.propagateExecutionChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n := cached(); n != 0 {
		t.Fatalf("the mark showed %s's superseded labels again (%d)", sibling, n)
	}
}

// rereadDependencyInverse reads the inverse lookups from the rows alone: no
// cached entry, no patch.
func rereadDependencyInverse(t *testing.T, q queryer) *dependencyInverseEntry {
	t.Helper()
	entry := &dependencyInverseEntry{aliases: map[string][]string{}, origins: map[string][]string{}, handles: map[string][]string{}}
	if err := fillDependencyInverse(t.Context(), q, entry, nil); err != nil {
		t.Fatal(err)
	}
	return entry
}

// rereadDependencyDigests is the catalog-wide digests read from the rows,
// bypassing the cache.
func rereadDependencyDigests(t *testing.T, q queryer) map[string]string {
	t.Helper()
	all, err := dependencyRows(t.Context(), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, members := range dependencyComponents(all) {
		digest := componentDigest(members)
		for _, c := range members {
			out[c.id] = digest
		}
	}
	return out
}

// A cached inverse index, and cached catalog-wide digests, patched for the
// conversations changed since equal what the rows read, through arrivals,
// alias changes, origin parents, launches, agent keys, appends and
// deletions, committed or inside a write transaction.
func TestDependencyInversePatchMatchesRows(t *testing.T) {
	ctx := t.Context()
	catalog, _ := testCatalog(t)
	ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "root", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "root", Provider: "codex", Account: "local", Messages: []MessageRecord{
			{NativeID: "d", Role: "assistant", Kind: "delegation", CallID: "call-1", Text: `{"tool":"spawn_agent","input":{"message":"Review"}}`},
			{NativeID: "r", Role: "tool", Kind: "delegation_result", CallID: "call-1", Text: `{"tool":"spawn_agent","content":"{\"agent_id\":\"late-agent\"}"}`}}}}})
	same := func(label string, q queryer) {
		t.Helper()
		patched, err := dependencyInverseIndex(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		rows := rereadDependencyInverse(t, q)
		if !reflect.DeepEqual(patched.aliases, rows.aliases) || !reflect.DeepEqual(patched.origins, rows.origins) || !reflect.DeepEqual(patched.handles, rows.handles) {
			t.Fatalf("%s: patched %v %v %v, rows %v %v %v", label, patched.aliases, patched.origins, patched.handles, rows.aliases, rows.origins, rows.handles)
		}
		digests, err := executionDependencyDigests(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if want := rereadDependencyDigests(t, q); !reflect.DeepEqual(digests, want) {
			t.Fatalf("%s: patched digests %v, rows %v", label, digests, want)
		}
	}
	same("first read", catalog.DB)
	steps := []struct {
		label string
		write func(*sql.Tx) error
	}{
		{"an aliased transcript arrives", func(tx *sql.Tx) error {
			_, _, err := ingestWorkspace(tx, WorkspaceRecord{SourceID: "x", SourceKind: "opencode", Account: "local", Conversations: []ConversationRecord{
				{NativeID: "x", Provider: "opencode", Account: "local", Aliases: []string{"alias-A"}, ParentNativeID: "root", ParentProvider: "codex", ParentAccount: "local", AgentDepth: 1,
					Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "hi"}}}}}, false)
			return err
		}},
		{"its alias changes", func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE conversations SET aliases_json='["alias-B"]' WHERE native_id='x'`)
			return err
		}},
		{"an origin parent is retained", func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO origin_observations(conversation_id,evidence_key,priority,evidence,parent_native_id) SELECT id,'opencode:x',1,'test','elsewhere' FROM conversations WHERE native_id='x'`)
			return err
		}},
		{"the launched transcript arrives", func(tx *sql.Tx) error {
			_, _, err := ingestWorkspace(tx, WorkspaceRecord{SourceID: "late-agent", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
				{NativeID: "late-agent", Provider: "codex", Account: "local", Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "Review"}}}}}, false)
			return err
		}},
		// One root's claims name two of its agents, whose transcripts answer
		// only by agent key.
		{"a root names two agents", func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) SELECT 'c-croot',workspace_id,'claude','local','croot' FROM conversations WHERE native_id='root';
			 INSERT INTO conversations(id,workspace_id,provider,account,native_id) SELECT 'c-a1',workspace_id,'claude','local','croot:subagent:agent-a1' FROM conversations WHERE native_id='root';
			 INSERT INTO conversations(id,workspace_id,provider,account,native_id) SELECT 'c-a2',workspace_id,'claude','local','croot:subagent:agent-a2' FROM conversations WHERE native_id='root';
			 INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES
			  ('c-croot','l1','record','launch_outcome','launch:c1','claude-agent:local|croot|a1','derived','test/v1'),
			  ('c-croot','l2','record','launch_outcome','launch:c2','claude-agent:local|croot|a2','derived','test/v1')`)
			return err
		}},
		{"an append to the root", func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,selected,source_order) VALUES('m-croot','c-croot','m-croot','user','message','hi','h-croot',1,1)`)
			return err
		}},
		{"a conversation is deleted", func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM conversations WHERE native_id='x'`)
			return err
		}},
	}
	for _, step := range steps {
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := step.write(tx); err != nil {
			tx.Rollback()
			t.Fatalf("%s: %v", step.label, err)
		}
		same(step.label+", inside the write", tx)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		same(step.label+", committed", catalog.DB)
	}
	// The walk reaches both agents from the root, as the catalog-wide read
	// joins them.
	walked, err := executionDependencyDigestsFor(ctx, catalog.DB, []string{"c-croot"})
	if err != nil {
		t.Fatal(err)
	}
	if walked["c-a1"] == "" || walked["c-a1"] != walked["c-croot"] || walked["c-a2"] != walked["c-croot"] {
		t.Fatalf("the root's walk: %v", walked)
	}
}
