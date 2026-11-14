package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func seedDelegation(t *testing.T, c *Catalog) {
	t.Helper()
	run := func(sql string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('delegation-work','claude','local','d','Delegated work','2026-01-01')`)
	for _, cv := range []struct{ id, provider, native, parent string }{{"root", "claude", "root-native", ""}, {"child", "codex", "codex-native", "root"}, {"nested", "codex", "exact-child", "root"}} {
		// Recorded children carry their agent depth; a Codex parent at depth zero
		// is a continuation.
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES(?,'delegation-work',?,'local',?,?,?)`, cv.id, cv.provider, cv.native, nilIfEmpty(cv.parent), min(1, len(cv.parent)))
	}
	for _, m := range []struct{ id, conv, kind, text, call string }{{"launch", "root", "delegation", `{"input":{"description":"Review with Codex"}}`, "launch-id"}, {"nested-launch", "child", "delegation", `{"input":{"description":"Check network"}}`, "nested-call"}, {"nested-result", "child", "delegation_result", `{"details":{"agentId":"exact-child"}}`, "nested-call"}, {"user", "root", "message", "Implement it", ""}} {
		run(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash) VALUES(?,?,?,'user',?,?,?,?)`, m.id, m.conv, m.id, m.kind, m.text, nilIfEmpty(m.call), m.id)
	}
	for _, s := range []struct {
		id, conv, native, parent, child, message, provider, status, start, end string
		tokens                                                                 int
	}{
		{"root-session", "root", "main", "", "", "", "claude", "reported-reconciled", "2026-01-01T00:00:00Z", "2026-01-01T00:00:10Z", 100},
		{"launch-alias", "root", "launch-id", "root-session", "child", "launch", "claude", "in-child-conversation", "2026-01-01T00:00:01Z", "2026-01-01T00:00:01Z", 0},
		{"child-session", "child", "main", "", "", "", "codex", "reported-reconciled", "2026-01-01T00:00:02Z", "2026-01-01T00:00:20Z", 200},
		{"nested-alias", "child", "nested-call", "child-session", "", "nested-launch", "codex", "unavailable", "2026-01-01T00:00:04Z", "2026-01-01T00:00:04Z", 0},
		{"nested-session", "nested", "main", "", "", "", "codex", "reported-reconciled", "2026-01-01T00:00:05Z", "2026-01-01T00:00:07Z", 300},
		{"embedded", "root", "other-execution", "root-session", "", "", "codex", "unavailable", "2026-01-01T00:01:00Z", "2026-01-01T00:01:05Z", 0},
	} {
		run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,delegation_message_id,kind,provider,usage_status,started_at,ended_at,total_tokens,input_tokens) VALUES(?,'delegation-work',?,?,?,?,?,'subagent',?,?,?,?,?,?)`, s.id, s.conv, s.native, nilIfEmpty(s.parent), nilIfEmpty(s.child), nilIfEmpty(s.message), s.provider, s.status, s.start, s.end, s.tokens, s.tokens)
	}
	for _, tc := range []struct {
		id, conv, session, start, end, status string
		duration                              int
	}{{"t-root", "root", "root-session", "2026-01-01T00:00:00Z", "2026-01-01T00:00:05Z", "ok", 5000}, {"t-child", "child", "child-session", "2026-01-01T00:00:02Z", "2026-01-01T00:00:08Z", "ok", 6000}, {"t-error", "nested", "nested-session", "2026-01-01T00:00:06Z", "2026-01-01T00:00:07Z", "error", 1000}} {
		run(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at,duration_ms) VALUES(?,'delegation-work',?,?,0,'test','tool_call','Read','read',?,?,?,?)`, tc.id, tc.conv, tc.session, tc.status, tc.start, tc.end, tc.duration)
	}
	run(`INSERT INTO model_requests(id,conversation_id,agent_session_id,sequence,requested_at,total_tokens) VALUES('request','child','child-session',0,'2026-01-01T00:00:03Z',150)`)
}
func TestDelegationCanonicalHierarchyAndAccounting(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	s, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 4 || len(s.Aliases) != 2 {
		t.Fatalf("aliases must collapse: %d nodes, %v", len(s.Nodes), s.Aliases)
	}
	child, nested := s.ByID["child-session"], s.ByID["nested-session"]
	// The Claude root's launch names a Codex child through a persisted link; the
	// Codex child's handle names its own child in the same provider namespace.
	if root := s.ByID["root-session"]; root.Provider != "claude" || child.Provider != "codex" || child.ParentID != "root-session" || nested.ParentID != child.ID {
		t.Fatalf("cross-provider nesting: %+v / %+v", child, nested)
	}
	if nested.ParentEvidence != "exact-delegation-handle" || nested.MessageID != "nested-launch" {
		t.Fatal("exact nested launch must override flattened transcript parent")
	}
	root := s.ByID["root-session"]
	if root.SubtreeEnd == nil || *root.SubtreeEnd <= *root.End {
		t.Fatal("children can outlive the parent's own evidence")
	}
	assertValue := func(m delegationMetric, want float64) {
		t.Helper()
		if m.Value == nil || *m.Value != want {
			t.Fatalf("got %+v; want %v", m, want)
		}
	}
	assertValue(s.Totals["tokens"], 600)
	if s.Totals["tokens"].Quality != "partial" {
		t.Fatal("unavailable embedded usage must leave totals partial")
	}
	assertValue(s.Totals["toolTime"], 8000)
	assertValue(s.Totals["toolAgent"], 12000)
	assertValue(s.Totals["toolSum"], 12000)
	assertValue(child.Self["remainder"], 50)
	assertValue(s.Totals["calls"], 3)
	assertValue(s.Totals["errors"], 1)
	if root.Self["modelTime"].Value != nil || root.Self["wait"].Value != nil || root.Self["backgroundTime"].Value != nil {
		t.Fatal("inference must not manufacture measured time")
	}
	if s.ByID["embedded"].ConversationID != root.ConversationID {
		t.Fatal("multiple sessions per conversation")
	}
}

func TestDelegationMessageCountsRetainMembershipAndUnknownTimes(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`UPDATE messages SET created_at='2026-01-01T00:00:03Z' WHERE id='user';
		INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES
		('root-session','user',0),('embedded','user',0),('child-session','nested-launch',0),('child-session','nested-result',1)`); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(c.DB, delegationMessageFactsSQL, "delegation-work", "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("read only recorded membership: %v", rows)
	}
	s, err := c.readDelegationSnapshot(t.Context(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"root-session", "embedded"} {
		n := s.ByID[id]
		if !near(n.Self["messages"].Value, 1) || !near(n.Self["turns"].Value, 1) || n.Start == nil {
			t.Fatalf("each session retains its own membership: %+v", n.Self)
		}
	}
	if child := s.ByID["child-session"]; !near(child.Self["messages"].Value, 2) || !near(child.Self["turns"].Value, 0) {
		t.Fatalf("events with unknown timestamps still count: %+v", child.Self)
	}
}

func TestDelegationTopLevelConversationLabels(t *testing.T) {
	for _, tc := range []struct {
		name, secondPrompt             string
		second, derived, parentOmitted bool
	}{
		{name: "single root"},
		{name: "child parent recovered from launch", parentOmitted: true},
		{name: "multiple derived roots", second: true, derived: true, secondPrompt: "Second root prompt"},
		{name: "root without derived session", second: true, secondPrompt: "Second root prompt"},
		{name: "root without prompt", second: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCatalog(t)
			seedDelegation(t, c)
			run := func(query string, args ...any) {
				t.Helper()
				if _, err := c.DB.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			run(`INSERT INTO conversation_documents(conversation_id,initiation,vector_json,model,indexed_at) VALUES('root','First root prompt','[]','test','2026-01-01')`)
			run(`INSERT INTO conversation_documents(conversation_id,initiation,vector_json,model,indexed_at) VALUES('child','Child opening request','[]','test','2026-01-01')`)
			if tc.parentOmitted {
				run(`UPDATE conversations SET parent_id=NULL WHERE id='child'`)
			}
			if tc.second {
				run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('root-two','delegation-work','codex','local','root-two-native')`)
				if tc.secondPrompt != "" {
					run(`INSERT INTO conversation_documents(conversation_id,initiation,vector_json,model,indexed_at) VALUES('root-two',?,'[]','test','2026-01-01')`, tc.secondPrompt)
				}
				if tc.derived {
					run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status) VALUES('root-two-session','delegation-work','root-two','main','main','codex','unavailable')`)
				}
			}
			s, err := c.delegationSnapshot(context.Background(), "delegation-work")
			if err != nil {
				t.Fatal(err)
			}
			// Every row uses the resolver's name. A source title can name its
			// one root; a shared workspace title cannot name several roots,
			// and an unclassified document preview is not human text.
			for _, n := range s.Nodes {
				if x := s.projection.res.Executions[n.execution]; x == nil || n.Label != x.Label.Text {
					t.Fatalf("%s label %q is not the resolver's %+v", n.ID, n.Label, x)
				}
			}
			want, subtitle := "Delegated work", "First root prompt"
			if tc.second {
				want, subtitle = "Conversation without retained request", "Delegated work"
			}
			if got, sub := s.ByID["root-session"].Label, s.ByID["root-session"].Subtitle; got != want || sub != subtitle {
				t.Fatalf("first root label = %q (%q)", got, sub)
			}
			if tc.second {
				id := "conversation:root-two"
				if tc.derived {
					id = "root-two-session"
				}
				if got, sub := s.ByID[id].Label, s.ByID[id].Subtitle; got != "Conversation without retained request" || sub != "Delegated work" {
					t.Fatalf("second root label = %q (%q)", got, sub)
				}
			}
			if got, sub := s.ByID["child-session"].Label, s.ByID["child-session"].Subtitle; got != "Review with Codex" || sub != "Child opening request" {
				t.Fatalf("child label %q, subtitle %q: want the assignment, with its opening request as context", got, sub)
			}
		})
	}
}

func TestDelegationAssignmentLabels(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"codex task name after opaque message", `{"tool":"spawn_agent","input":{"fork_turns":"all","message":"gAAAA` + strings.Repeat("x", 12000) + `","task_name":"tooling_independent_review"}}`, "tooling_independent_review"},
		{"task name before longer description", `{"input":{"task_name":"review","description":"Review all of the implementation"}}`, "review"},
		{"task path uses retained message", `{"input":{"task_name":"/root/explore","message":"Inspect URL restoration"}}`, "Inspect URL restoration"},
		{"task identifier uses retained prompt", `{"input":{"task_name":"01a128b8-395c-7121-bf40-9a1903a6f306","prompt":"Inspect URL restoration"}}`, "Inspect URL restoration"},
		{"weak description uses retained prompt", `{"input":{"task_name":"Untitled","description":"/root/explore","prompt":"Inspect URL restoration"}}`, "Inspect URL restoration"},
		{"claude description", `{"tool":"Task","input":{"description":"Check network","prompt":"Longer instructions"}}`, "Check network"},
		{"prompt", `{"input":{"prompt":"Review the implementation"}}`, "Review the implementation"},
		{"antigravity prompt", `{"tool":"invoke_subagent","input":{"Prompt":"check tests"}}`, "check tests"},
		{"antigravity prompt before task", `{"input":{"Prompt":"Check the tests","task":"Longer task instructions"}}`, "Check the tests"},
		{"task", `{"input":{"task":"Check the build"}}`, "Check the build"},
		{"plaintext codex message", `{"input":{"message":"Inspect the renderer"}}`, "Inspect the renderer"},
		{"blank names use prompt", `{"input":{"task_name":"\t\n ","description":"","prompt":"Check the CLI"}}`, "Check the CLI"},
		{"structured fields use prose", `{"input":{"task_name":{},"description":["bad"],"prompt":false,"task":12,"message":"Check the API"}}`, "Check the API"},
		{"legacy prose", "Review the release", "Review the release"},
		{"bounded prose", `{"input":{"message":"` + strings.Repeat("é", 200) + `"}}`, strings.Repeat("é", 180)},
		{"unknown payload", `{"tool":"spawn_agent","input":{"fork_turns":"all"}}`, ""},
		{"opaque message only", `{"input":{"message":"gAAAA` + strings.Repeat("x", 300) + `"}}`, ""},
		{"structured task only", `{"input":{"task":{"instructions":"Check the build"}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testCatalog(t)
			seedDelegation(t, c)
			for _, update := range []struct {
				query string
				args  []any
			}{
				{`UPDATE messages SET text=? WHERE id='launch'`, []any{tc.text}},
				// Its own usage makes the launch an execution, not an attempt.
				{`UPDATE agent_sessions SET delegation_message_id='launch',total_tokens=5,usage_status='reported-reconciled' WHERE id='embedded'`, nil},
				{`UPDATE conversations SET agent_nickname='Parent nickname' WHERE id='root'`, nil},
				{`UPDATE conversations SET agent_nickname='Child nickname' WHERE id='child'`, nil},
				{`INSERT INTO conversation_documents(conversation_id,initiation,vector_json,model,indexed_at) VALUES('root','Parent request','[]','test','2026-01-01')`, nil},
			} {
				if _, err := c.DB.Exec(update.query, update.args...); err != nil {
					t.Fatal(err)
				}
			}
			s, err := c.delegationSnapshot(context.Background(), "delegation-work")
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range []struct{ id, fallback string }{{"child-session", "Child nickname"}, {"embedded", "Conversation without retained request"}} {
				want := firstString(tc.want, node.fallback)
				if got := s.ByID[node.id].Label; got != want {
					t.Fatalf("%s label = %q, want %q", node.id, got, want)
				}
			}
		})
	}
}

func TestDelegationEffortUsesOwnSessionAndConversation(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`UPDATE conversations SET effort='high'; UPDATE agent_sessions SET effort='low' WHERE id='child-session'`); err != nil {
		t.Fatal(err)
	}
	s, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if s.ByID["root-session"].Effort != "high" || s.ByID["child-session"].Effort != "low" || s.ByID["embedded"].Effort != "" {
		t.Fatal("effort must use own session, fall back for main, and never inherit for embedded children")
	}
	if s.ByID["root-session"].EffortSummary != nil {
		t.Fatal("pending request summary must not fall back to conversation effort")
	}
	execTx(t, c.DB,
		`UPDATE model_requests SET effort='low' WHERE id='request'`,
		`INSERT INTO model_requests(id,conversation_id,agent_session_id,sequence,effort) VALUES('root-effort','root','root-session',0,'high'),('embedded-effort','root','embedded',1,NULL)`)
	if _, err := c.RepairSessionEffortSummaries(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	after, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]string{"root-session": "single", "child-session": "single", "embedded": "unknown"} {
		summary := after.ByID[id].EffortSummary
		if summary == nil || summary.State != state || summary.Coverage != "recorded-requests" {
			t.Fatalf("%s summary: %+v", id, summary)
		}
	}
	if got := after.ByID["embedded"].EffortSummary; len(got.Levels) != 0 || got.UnknownRequests != 1 {
		t.Fatalf("embedded summary inherited parent effort: %+v", got)
	}
	if after == s {
		t.Fatal("summary backfill left delegation cache stale")
	}
}

func TestDelegationLegacyMessagesExcludeAlreadyOwnedEvents(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('embedded','user',0)`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if !near(snapshot.Totals["messages"].Value, 4) || !near(snapshot.ByID["root-session"].Self["messages"].Value, 1) {
		t.Fatal("legacy root fallback must exclude events already owned by embedded sessions")
	}
}
func delegationGET(t *testing.T, s *Server, path string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+s.Config().APIToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return w.Code, body
}
func TestDelegationPaginationFilteringAndSnapshot(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	server := testServer(t, config, c)
	base := "/api/work/delegation-work/delegation"
	status, first := delegationGET(t, server, base+"?limit=1")
	if status != 200 {
		t.Fatal(first)
	}
	cursor := first["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("missing bounded continuation")
	}
	status, next := delegationGET(t, server, base+"?limit=1&cursor="+url.QueryEscape(cursor))
	if status != 200 || len(next["rows"].([]any)) != 1 || len(next["ancestors"].([]any)) != 1 {
		t.Fatalf("page needs separate ancestor context: %d %v", status, next)
	}
	_, filtered := delegationGET(t, server, base+"?search=Check%20network")
	if filtered["total"].(float64) != 3 {
		t.Fatal("search must retain two ancestors")
	}
	_, collapsed := delegationGET(t, server, base+"?search=Check%20network&collapsed=root-session")
	if collapsed["total"].(float64) != 1 {
		t.Fatal("a filtered branch must still be collapsible")
	}
	// Saved links that name a launch handle restore on its execution. A
	// canonical row ID needs no alias; an ID that names no row here is null,
	// so the client keeps it unresolved rather than choosing a session.
	_, legacy := delegationGET(t, server, base+"?collapsed=launch-alias&resolve=launch-alias&resolve=child-session&resolve=unknown")
	_, canonical := delegationGET(t, server, base+"?collapsed=child-session")
	if jsonText(legacy["aliases"]) != `{"child-session":"child-session","launch-alias":"child-session","unknown":null}` || legacy["total"] != canonical["total"] || canonical["total"].(float64) >= first["total"].(float64) {
		t.Fatal("legacy session IDs must restore on their execution", legacy["aliases"], legacy["total"], canonical["total"])
	}
	if _, plain := delegationGET(t, server, base); plain["aliases"] != nil {
		t.Fatal("aliases are answered only on request")
	}
	if jsonText(filtered["maxima"]) != jsonText(first["maxima"]) {
		t.Fatal("filtering must preserve column maxima")
	}
	if jsonText(filtered["totals"]) != jsonText(first["totals"]) {
		t.Fatal("row filters must preserve totals")
	}
	_, counted := delegationGET(t, server, base+"?search=Check%20network&only_matching=1")
	if counted["totals"].(map[string]any)["tokens"].(map[string]any)["value"] != float64(300) {
		t.Fatal("matching-only accounting excludes ancestor self usage")
	}
	status, _ = delegationGET(t, server, base+"?sort=errors&cursor="+url.QueryEscape(cursor))
	if status != 409 {
		t.Fatal("cursor must be bound to query")
	}
	if _, err := c.DB.Exec("UPDATE agent_sessions SET total_tokens=101 WHERE id='root-session'"); err != nil {
		t.Fatal(err)
	}
	status, _ = delegationGET(t, server, base+"?cursor="+url.QueryEscape(cursor))
	if status != 409 {
		t.Fatal("stale snapshot cursor must be rejected")
	}
	status, _ = delegationGET(t, server, base+"?sort=made-up")
	if status != 400 {
		t.Fatal("unknown metric must fail")
	}
	status, _ = delegationGET(t, server, "/api/work/missing/delegation")
	if status != 404 {
		t.Fatal("workspace scope must be enforced")
	}
}
func TestDelegationTimelineSnapshotDependencies(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('unrelated-work','codex','local','unrelated','Other work','2026-01-01')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('other-unrelated-work','codex','local','other-unrelated','Another work','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('unrelated','unrelated-work','codex','local','unrelated')`)
	server := testServer(t, config, c)
	base := "/api/work/delegation-work/delegation?limit=1"
	_, page := delegationGET(t, server, base)
	before, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	claim := `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES(?,'late','record','launch_outcome','launch:call-late','','derived','test/v1')`
	fixtureExec(t, c, claim, "unrelated")
	fixtureExec(t, c, `UPDATE conversations SET workspace_id='other-unrelated-work' WHERE id='unrelated'`)
	after, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if after == before || after.Revision == before.Revision || after.ID != before.ID {
		t.Fatalf("unrelated evidence must refresh the cached resolution but preserve the view token: %s -> %s, revisions %v -> %v", before.ID, after.ID, before.Revision, after.Revision)
	}
	if _, _, err := c.delegationTimelineMessages(t.Context(), before, []string{"root-session"}, 0, 4e12); err != nil {
		t.Fatal("unrelated evidence invalidated the lane transaction", err)
	}
	if status, body := delegationGET(t, server, "/api/work/delegation-work/parallel?sessions=root-session&labels=1&snapshot="+before.ID); status != 200 || body["snapshot"] != before.ID {
		t.Fatal("unrelated evidence invalidated the timeline", status, body)
	}
	if status, body := delegationGET(t, server, base+"&cursor="+url.QueryEscape(firstString(page["next_cursor"]))); status != 200 || body["snapshot"] != before.ID {
		t.Fatal("unrelated evidence invalidated pagination", status, body)
	}
	// Evidence-only changes still expire the view even if the visible rows
	// happen to be identical: later lane ownership reads depend on it.
	fixtureExec(t, c, claim, "root")
	changed, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil || changed.ID == before.ID {
		t.Fatal("local execution evidence must expire the view", err)
	}
	if _, _, err := c.delegationTimelineMessages(t.Context(), before, []string{"root-session"}, 0, 4e12); !errors.Is(err, errDelegationSnapshotChanged) {
		t.Fatal("changed dependencies must reject a stale lane transaction", err)
	}
	if status, body := delegationGET(t, server, "/api/work/delegation-work/parallel?sessions=root-session&labels=1&snapshot="+before.ID); status != 409 || body["code"] != "snapshot_changed" {
		t.Fatal("changed dependencies must reject a stale timeline", status, body)
	}
}

func TestDelegationTimelineExternalDependencies(t *testing.T) {
	for _, change := range []string{"parent evidence", "late original", "deleted original", "conversation workspace", "session workspace", "workspace move", "parent workspace"} {
		t.Run(change, func(t *testing.T) {
			c := executionFixture(t,
				executionFixtureConversation{id: "parent", workspace: "parent-work"},
				executionFixtureConversation{id: "fork", workspace: "fork-work", parent: "parent", depth: 1})
			message := `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected) VALUES(?,?,'evt_copy','user','message','x','x','2026-01-01T00:00:30Z',1)`
			fixtureExec(t, c, message, "fork-copy", "fork")
			if change == "deleted original" {
				fixtureExec(t, c, message, "parent-original", "parent")
			}
			before, err := c.delegationSnapshot(t.Context(), "fork-work")
			if err != nil {
				t.Fatal(err)
			}
			if change == "workspace move" {
				lanes, _, err := c.delegationTimelineMessages(t.Context(), before, []string{"s-fork"}, 0, 4e12)
				if err != nil || len(lanes["s-fork"]["messages"].([]map[string]any)) != 1 {
					t.Fatal("fixture must have a populated lane before regrouping", err, lanes)
				}
			}
			switch change {
			case "parent evidence":
				fixtureExec(t, c, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('parent','late','record','launch_outcome','launch:call-late','','derived','test/v1')`)
			case "late original":
				// This append changes occurrence ownership alone. Execution
				// identity and its revision remain the same.
				fixtureExec(t, c, message, "parent-original", "parent")
			case "deleted original":
				fixtureExec(t, c, `DELETE FROM messages WHERE id='parent-original'`)
			case "conversation workspace":
				fixtureExec(t, c, `UPDATE conversations SET workspace_id='parent-work' WHERE id='fork'`)
			case "session workspace":
				fixtureExec(t, c, `UPDATE agent_sessions SET workspace_id='parent-work' WHERE id='s-fork'`)
			case "workspace move":
				execTx(t, c.DB,
					`UPDATE conversations SET workspace_id='parent-work' WHERE id='fork'`,
					`UPDATE agent_sessions SET workspace_id='parent-work' WHERE id='s-fork'`)
			case "parent workspace":
				execTx(t, c.DB,
					`UPDATE conversations SET workspace_id='fork-work' WHERE id='parent'`,
					`UPDATE agent_sessions SET workspace_id='fork-work' WHERE id='s-parent'`)
			}
			after, err := c.delegationSnapshot(t.Context(), "fork-work")
			if err != nil || after.ID == before.ID {
				t.Fatal("external dependencies must expire the token", err)
			}
			if change == "late original" && after.Revision != before.Revision {
				t.Fatal("fixture must preserve execution identity while changing copied-message ownership")
			}
			// A warm execution digest cache must not bypass placement checks.
			if _, err := executionDependencyDigests(t.Context(), c.DB); err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.delegationTimelineMessages(t.Context(), before, []string{"s-fork"}, 0, 4e12); !errors.Is(err, errDelegationSnapshotChanged) {
				t.Fatal("external dependency change must reject stale lanes", err)
			}
			if change == "late original" {
				lanes, _, err := c.delegationTimelineMessages(t.Context(), after, []string{"s-fork"}, 0, 4e12)
				if err != nil || len(lanes["s-fork"]["messages"].([]map[string]any)) != 0 || lanes["s-fork"]["inherited_messages"] != 1 {
					t.Fatal("refreshed lane must exclude the newly recognized copy", err, lanes)
				}
			}
			if change == "workspace move" {
				moved, err := c.delegationSnapshot(t.Context(), "parent-work")
				if err != nil {
					t.Fatal(err)
				}
				lanes, _, err := c.delegationTimelineMessages(t.Context(), moved, []string{"s-fork"}, 0, 4e12)
				if err != nil || len(lanes["s-fork"]["messages"].([]map[string]any)) != 1 {
					t.Fatal("a refreshed lane must show its messages in the destination workspace", err, lanes)
				}
			}
		})
	}
}

func TestDelegationTimelineMissingReferentArrives(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "root", workspace: "root-work"})
	fixtureExec(t, c, `INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('root','late','record','launch_outcome','launch:call-late',?,'derived','test/v1')`, executionNativeKey("codex", "local", "missing"))
	before, err := c.delegationSnapshot(t.Context(), "root-work")
	if err != nil {
		t.Fatal(err)
	}
	// There was no conversation ID to capture for the referent at the time
	// of the first read. Its arrival joins the component through the lookup key.
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('new-work','codex','local','new','New work','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('arrived','new-work','codex','local','missing')`)
	after, err := c.delegationSnapshot(t.Context(), "root-work")
	if err != nil || after.ID == before.ID {
		t.Fatal("a newly answered dependency must expire the token", err)
	}
	if _, _, err := c.delegationTimelineMessages(t.Context(), before, []string{"s-root"}, 0, 4e12); !errors.Is(err, errDelegationSnapshotChanged) {
		t.Fatal("a newly answered dependency must reject stale lanes", err)
	}
	fixtureExec(t, c, `DELETE FROM conversations WHERE id='arrived'`)
	deleted, err := c.delegationSnapshot(t.Context(), "root-work")
	if err != nil || deleted.ID == after.ID {
		t.Fatal("a removed referent must expire the token", err)
	}
	if _, _, err := c.delegationTimelineMessages(t.Context(), after, []string{"s-root"}, 0, 4e12); !errors.Is(err, errDelegationSnapshotChanged) {
		t.Fatal("a removed referent must reject stale lanes", err)
	}
}

func TestDelegationTimelineWindowEvidenceAndErrorReferences(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`UPDATE tool_calls SET ended_at=NULL,duration_ms=NULL WHERE id='t-child'`); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('tool-input','root','tool-input','assistant','tool_call','{"tool":"Bash","input":{"command":"npm test","workdir":"/repo"}}','tool-input');
INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,call_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at) VALUES
('short-a','delegation-work','root','root-session',NULL,10,'test','tool_call','Bash','shell','ok','2026-01-01T00:00:02Z','2026-01-01T00:00:02.020Z'),
('short-b','delegation-work','root','root-session','tool-input',11,'test','tool_call','Bash','shell','ok','2026-01-01T00:00:02.001Z','2026-01-01T00:00:02.021Z');`)
	command := "  " + strings.Repeat("echo 😎; ", 100)
	if _, err := c.DB.Exec(`UPDATE tool_calls SET command=? WHERE id='short-a'`, command); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, config, c)
	_, body := delegationGET(t, s, "/api/work/delegation-work/delegation")
	snapshot := body["snapshot"].(string)
	_, timeline := delegationGET(t, s, "/api/work/delegation-work/parallel?sessions=child-session,nested-session&snapshot="+snapshot)
	lanes := timeline["lanes"].([]any)
	if len(lanes) != 2 {
		t.Fatal("requested lane scope")
	}
	childMark := lanes[0].(map[string]any)["marks"].([]any)[0].(map[string]any)
	if childMark["end_known"] != false || childMark["start"] != childMark["end"] {
		t.Fatalf("missing end evidence must be represented as a point, not a duration: %v", childMark)
	}
	mark := lanes[1].(map[string]any)["marks"].([]any)[0].(map[string]any)
	if mark["error"] != true || mark["tool_id"] != "t-error" {
		t.Fatal("errors must preserve a stable tool reference")
	}
	if !strings.Contains(timeline["inference"].(string), "not measured") {
		t.Fatal("evidence contract must accompany inference")
	}
	_, plain := delegationGET(t, s, "/api/work/delegation-work/parallel?sessions=root-session&width=100")
	plainMarks := plain["lanes"].([]any)[0].(map[string]any)["marks"].([]any)
	if len(plainMarks) != 2 {
		t.Fatalf("ordinary parallel view should retain short-call grouping: %v", plainMarks)
	}
	_, native := delegationGET(t, s, "/api/work/delegation-work/parallel?sessions=root-session&width=100&labels=1")
	nativeMarks := native["lanes"].([]any)[0].(map[string]any)["marks"].([]any)
	if len(nativeMarks) != 3 {
		t.Fatalf("native Timeline must keep overlapping calls distinct: %v", nativeMarks)
	}
	for _, value := range nativeMarks {
		mark := value.(map[string]any)
		if mark["count"] != float64(1) {
			t.Fatalf("individual call lost: %v", mark)
		}
		if mark["tool_id"] == "short-a" && (mark["preview"] != string([]rune(command)[:640]) || mark["preview_truncated"] != true) {
			t.Fatalf("bounded command preview: %v", mark)
		}
		if mark["tool_id"] == "short-b" && mark["preview"] != "npm test" {
			t.Fatalf("primary command content: %v", mark)
		}
	}
	code, _ := delegationGET(t, s, "/api/work/delegation-work/parallel?start=NaN")
	if code != 400 {
		t.Fatal("invalid range")
	}
	for _, query := range []string{
		"start=1trailing", "start=2&end=1", "start=1&end=2",
		"start=9999999999999&end=99999999999999",
	} {
		code, _ = delegationGET(t, s, "/api/work/delegation-work/parallel?"+query)
		if code != 400 {
			t.Fatalf("invalid or out-of-workspace range accepted: %s", query)
		}
	}
	code, clamped := delegationGET(t, s, "/api/work/delegation-work/parallel?start=0")
	if code != 200 || clamped["start"] != body["start"] {
		t.Fatal("intersecting viewport must clamp to the archive envelope")
	}
	code, _ = delegationGET(t, s, "/api/work/delegation-work/parallel?sessions="+strings.Repeat("child-session,", 101))
	if code != 400 {
		t.Fatal("bounded timeline lanes")
	}
}

// The Timeline tab asks the existing parallel endpoint for local labels. It
// must keep embedded and aliased executions separate, and never run inference.
func TestDelegationTimelineMessageLabels(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	execTx(t, c.DB, `UPDATE messages SET created_at='2026-01-01T00:00:03Z';
 INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) VALUES('user','root','delegation-work','2026-01-01T00:00:03Z','2026-01-01',12,'[{"start":0,"end":5,"category":"typed"},{"start":5,"end":9,"category":"pasted"},{"start":9,"end":12,"category":"typed"}]');
 INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('root-session','user',0),('embedded','launch',1),('child-session','nested-launch',0),('nested-alias','nested-result',1)`)
	server := testServer(t, config, c)
	read := func(query string) map[string]any {
		t.Helper()
		status, body := delegationGET(t, server, "/api/work/delegation-work/parallel?"+query)
		if status != 200 {
			t.Fatalf("timeline: %d %v", status, body)
		}
		return body
	}
	body := read("sessions=root-session,embedded,child-session,nested-session&labels=1")
	lanes := body["lanes"].([]any)
	want := [][]string{{"user"}, {"launch"}, {"nested-launch"}, {"nested-result"}}
	for i, lane := range lanes {
		messages := lane.(map[string]any)["messages"].([]any)
		if i == 2 { // unowned delegation results cannot also belong to the parent
			if len(messages) != 1 {
				t.Fatalf("child ownership: %v", messages)
			}
		}
		if len(messages) != len(want[i]) {
			t.Fatalf("lane %d ownership: %v", i, messages)
		}
		for j, message := range messages {
			if message.(map[string]any)["id"] != want[i][j] {
				t.Fatalf("lane %d: %v", i, messages)
			}
		}
	}
	aliasMessage := lanes[3].(map[string]any)["messages"].([]any)[0].(map[string]any)
	if aliasMessage["conversation_id"] != "child" {
		t.Fatal("canonical lane lost its message's source transcript")
	}
	// Human events: a person's message carries the engagement spans the page
	// can show with an excerpt of the stored text around them, only while
	// the preview is on in a library that has the tables.
	userMessage := func() map[string]any {
		return read("sessions=root-session&labels=1")["lanes"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)
	}
	c.EngagementEnabled = true
	if got := userMessage(); got["engagement"] != nil || got["engagement_text"] != nil {
		t.Fatalf("engagement without the tables: %v", got)
	}
	if err := c.ensureEngagementSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A long message with one span near its start and one near its end: the
	// lane carries the text around each, not the megabyte between. A span
	// below the display threshold and a label the page never shows are left out.
	userText := "Implement it 🙂 " + strings.Repeat("pasted ", 20000) + "then ship it"
	shipAt := strings.Index(userText, "ship")
	execTx(t, c.DB, fmt.Sprintf(`UPDATE messages SET text=%s WHERE id='user'`, sqlQuote(userText)),
		fmt.Sprintf(`UPDATE message_authorship SET spans_json='[{"start":0,"end":12,"category":"typed"},{"start":18,"end":%d,"category":"pasted"},{"start":%d,"end":%d,"category":"typed"}]' WHERE message_id='user'`, shipAt-5, shipAt-5, len(userText)),
		fmt.Sprintf(`INSERT INTO engagement_spans(message_id,position,label,start_byte,end_byte,score) VALUES('user',0,'intent.task',0,9,0.9),('user',1,'intent.task',0,2,0.69),('user',2,'register.filler',0,2,0.9),('user',3,'rights.go_ahead',%d,%d,0.8),('launch',0,'intent.task',0,2,0.9)`, shipAt, shipAt+4))
	got := userMessage()
	spans, _ := got["engagement"].([]any)
	excerpt, _ := got["engagement_text"].(string)
	head, tail := userText[:9+engagementLaneContextBytes], userText[shipAt-engagementLaneContextBytes:]
	if len(spans) != 2 || excerpt != head+engagementLaneGap+tail {
		t.Fatalf("lane engagement: %v, excerpt %q", got["engagement"], excerpt)
	}
	// UTF-16 units: the emoji is two, so the second part starts one unit
	// later in the message than its bytes say, less the emoji's two extra bytes.
	units := func(value string) int { return len(utf16.Encode([]rune(value))) }
	first, second := spans[0].(map[string]any), spans[1].(map[string]any)
	secondAt := units(head + engagementLaneGap)
	if first["label"] != "intent.task" || first["dimension"] != "intent" || first["start"] != float64(0) || first["end"] != float64(9) ||
		second["label"] != "rights.go_ahead" || second["start"] != float64(secondAt+engagementLaneContextBytes) || second["end"] != float64(secondAt+engagementLaneContextBytes+4) {
		t.Fatalf("lane spans: %v", spans)
	}
	if fmt.Sprint(got["engagement_typed"]) != fmt.Sprintf("[[0 12] [%d %d]]", secondAt+engagementLaneContextBytes-5, units(excerpt)) ||
		fmt.Sprint(got["engagement_at"]) != fmt.Sprintf("[[0 0] [%d %d]]", secondAt, units(userText[:shipAt-engagementLaneContextBytes])) || got["engagement_length"] != float64(units(userText)) {
		t.Fatalf("lane excerpt places: typed %v, at %v, length %v", got["engagement_typed"], got["engagement_at"], got["engagement_length"])
	}
	if launch := read("sessions=embedded&labels=1")["lanes"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any); launch["engagement"] != nil {
		t.Fatalf("a delegation carries spans: %v", launch)
	}
	// The setting off: the tables and spans stay, and the lane is as it was.
	c.EngagementEnabled = false
	if got := userMessage(); got["engagement"] != nil || got["engagement_text"] != nil {
		t.Fatalf("engagement with the setting off: %v", got["engagement"])
	}
	execTx(t, c.DB, `UPDATE messages SET text='Implement it' WHERE id='user'`,
		`UPDATE message_authorship SET spans_json='[{"start":0,"end":5,"category":"typed"},{"start":5,"end":9,"category":"pasted"},{"start":9,"end":12,"category":"typed"}]' WHERE message_id='user'`)
	// Unwrap display content before clipping, without rewriting the retained
	// source excerpt that authorship positions and source navigation refer to.
	ack := strings.Repeat("Async agent launched successfully.\n", 40)
	envelope := jsonText(map[string]any{"content": []any{map[string]any{"text": ack}}, "tool": "Task", "details": map[string]any{"agentId": "exact-child"}})
	if _, err := c.DB.Exec(`UPDATE messages SET text=? WHERE id='nested-result'`, envelope); err != nil {
		t.Fatal(err)
	}
	result := read("sessions=nested-session&labels=1")["lanes"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)
	if result["preview"] != string([]rune(ack)[:640]) || result["preview_truncated"] != true || !strings.HasPrefix(firstString(result["text"]), `{"content":`) {
		t.Fatalf("readable result content or raw source lost: %v", result)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"untyped text blocks", `{"content":[{"text":"First\nline"},{"type":"text","text":"Second"}],"is_error":false,"details":{"agentId":"opaque"}}`, "First\nline\n\nSecond"},
		{"nested message", `{"message":{"content":[{"type":"output_text","text":"Reply"}]},"usage":{"tokens":9}}`, "Reply"},
		{"thinking", `{"content":[{"type":"thinking","thinking":"Check the premise"}]}`, "Check the premise"},
		{"media placeholders", `{"content":[{"type":"image","source":{"data":"opaque"}},{"text":"Caption"}]}`, "[Image]\n\nCaption"},
		{"plain text", "Keep \nline breaks", "Keep \nline breaks"},
		{"domain JSON", `{"text":"Keep me","records":[1,2]}`, `{"text":"Keep me","records":[1,2]}`},
		{"malformed prefix", `{"content":[{"text":"unfinished`, `{"content":[{"text":"unfinished`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if err := c.reader().QueryRowContext(t.Context(), `WITH source(body) AS (VALUES(?)) SELECT `+timelineContentSQL("body")+` FROM source`, tc.body).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("primary content = %q; want %q", got, tc.want)
			}
		})
	}

	for _, tc := range []struct{ name, body, want string }{
		{"encoded arguments", jsonText(map[string]any{"tool": "exec_command", "arguments": jsonText(map[string]any{"cmd": "npm test", "timeout_ms": 1000})}), "npm test"},
		{"delegation prompt", `{"tool":"Task","input":{"description":"Check","prompt":"Read the source\nCheck tests","subagent_type":"Reviewer"}}`, "Read the source\nCheck tests"},
		{"literal script", `{"tool":"exec","input":"\"literal\""}`, `"literal"`},
		{"structured edit", `{"tool":"Edit","input":{"file_path":"a.go","old_string":"before","new_string":"after"}}`, `{"file_path":"a.go","old_string":"before","new_string":"after"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if err := c.reader().QueryRowContext(t.Context(), `WITH source(body) AS (VALUES(?)) SELECT `+timelineInputSQL("source.body")+` FROM source`, tc.body).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("primary input = %q; want %q", got, tc.want)
			}
		})
	}

	input := lanes[0].(map[string]any)["messages"].([]any)[0].(map[string]any)
	parts := input["authorship"].([]any)
	if len(parts) != 3 || parts[0].(map[string]any)["category"] != "typed" || parts[0].(map[string]any)["bytes"] != float64(5) || parts[1].(map[string]any)["category"] != "pasted" || parts[1].(map[string]any)["bytes"] != float64(4) || parts[2].(map[string]any)["category"] != "typed" || parts[2].(map[string]any)["bytes"] != float64(3) {
		t.Fatalf("retained text origins: %v", parts)
	}
	for i, text := range []string{"Imple", "ment", " it"} {
		if parts[i].(map[string]any)["text"] != text || parts[i].(map[string]any)["truncated"] != false {
			t.Fatalf("span excerpt %d: %v", i, parts[i])
		}
	}
	if _, exposed := input["authorship_spans"]; exposed {
		t.Fatal("raw authorship offsets escaped compact projection")
	}
	if c.hasLabelsLedger() {
		t.Fatal("reading created the labels ledger")
	}
	// Positions use original UTF-8 bytes, even beyond the clipped excerpt.
	if _, err := c.DB.Exec(`UPDATE messages SET text=? WHERE id='user'`, strings.Repeat("é", 400)+"!"); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `UPDATE message_authorship SET spans_json='[{"start":4,"end":800,"category":"quoted"},{"start":0,"end":2,"category":"typed"},{"start":801,"end":900,"category":"pasted"}]' WHERE message_id='user'`)
	positioned := read("sessions=root-session&labels=1")["lanes"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)
	positionParts := positioned["authorship"].([]any)
	wantOrigins := []string{"typed", "unknown", "quoted", "unknown"}
	wantBytes := []float64{2, 2, 796, 1}
	wantText := []string{"é", "é", strings.Repeat("é", 240), "!"}
	if len(positionParts) != len(wantOrigins) {
		t.Fatalf("positioned text origins: %v", positionParts)
	}
	for i, part := range positionParts {
		p := part.(map[string]any)
		if p["category"] != wantOrigins[i] || p["bytes"] != wantBytes[i] {
			t.Fatalf("span %d lost its position or full byte length: %v", i, p)
		}
		if p["text"] != wantText[i] || p["truncated"] != (i == 2) {
			t.Fatalf("span %d must have its own bounded Unicode excerpt: %v", i, p)
		}
	}
	// An excerpt byte cap must not expose a partial UTF-8 rune at its edge.
	if _, err := c.DB.Exec(`UPDATE messages SET text=? WHERE id='user'`, "a"+strings.Repeat("😎", 300)+"tail"); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `UPDATE message_authorship SET spans_json='[{"start":0,"end":1201,"category":"typed"},{"start":1201,"end":1205,"category":"quoted"}]' WHERE message_id='user'`)
	emoji := read("sessions=root-session&labels=1")["lanes"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)["authorship"].([]any)
	if emoji[0].(map[string]any)["text"] != "a"+strings.Repeat("😎", 239) || emoji[1].(map[string]any)["text"] != "tail" {
		t.Fatalf("Unicode excerpt boundary: %v", emoji)
	}
	if _, ok := read("sessions=root-session")["taxonomy"]; ok {
		t.Fatal("parallel view loaded message labels without opting in")
	}
	// A small ledger names probabilities in its stored model order.
	if err := c.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	// As a labels run records: the stored labels below rest on the current
	// execution evidence.
	digests, err := executionDependencyDigestsFor(t.Context(), c.DB, []string{"root"})
	if err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('test','["P","P.request","P.request.work.implement"]');
 INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('root','test',1,'2026-01-01');
 DELETE FROM message_labels_dirty`)
	execTx(t, c.DB, `UPDATE message_labels_conversations SET execution_digest='`+digests["root"]+`' WHERE conversation_id='root'`)
	if _, err := c.DB.Exec(`INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES('user','root','H-typed',7,?,1,1,'test','test','test','2026-01-01')`, encodeLabelProbs([]float32{1, .98, .95})); err != nil {
		t.Fatal(err)
	}
	labeled := read("sessions=root-session&labels=1")
	message := labeled["lanes"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)
	if message["labels"].(map[string]any)["ids"].([]any)[2] != "P.request.work.implement" || len(labeled["taxonomy"].([]any)) != 3 {
		t.Fatalf("labels/taxonomy: %v", labeled)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('root','pending')`)
	pending := read("sessions=root-session&labels=1")["lanes"].([]any)[0].(map[string]any)
	if pending["labels_pending"] != true {
		t.Fatal("pending labels hidden")
	}
	if _, ok := pending["messages"].([]any)[0].(map[string]any)["labels"]; ok {
		t.Fatal("stale labels presented as current")
	}
	override(t, &delegationMessageLimit, 1)
	execTx(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('legacy','root','legacy','assistant','message','Legacy reply','legacy','2026-01-01T00:00:04Z')`)
	bounded := read("sessions=root-session&labels=1")["lanes"].([]any)[0].(map[string]any)
	if bounded["messages_truncated"] != true || len(bounded["messages"].([]any)) != 1 {
		t.Fatalf("unbounded messages: %v", bounded)
	}
	start := *delegationTime("2026-01-01T00:00:04Z")
	zoom := read(fmt.Sprintf("sessions=root-session&labels=1&start=%.0f&end=%.0f", start, start+500))["lanes"].([]any)[0].(map[string]any)
	if zoom["messages"].([]any)[0].(map[string]any)["id"] != "legacy" {
		t.Fatal("preceding context crowded the visible message out of the cap")
	}

	// A fork whose window starts with history copied from its parent: the cap
	// counts the fork's own messages, so the copies cannot hide them.
	override(t, &delegationMessageLimit, 2)
	execTx(t, c.DB, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('fork','delegation-work','codex','local','fork-native','child',2);
 INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,usage_status) VALUES('fork-session','delegation-work','fork','main','main','codex','unavailable');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('child-c1','child','evt-c1','assistant','message','one','c1','2026-01-01T00:00:01Z'),('child-c2','child','evt-c2','assistant','message','two','c2','2026-01-01T00:00:01Z'),
 ('child-c3','child','evt-c3','assistant','message','three','c3','2026-01-01T00:00:01Z'),
 ('fork-c1','fork','evt-c1','assistant','message','one','c1','2026-01-01T00:00:01Z'),('fork-c2','fork','evt-c2','assistant','message','two','c2','2026-01-01T00:00:01Z'),
 ('fork-c3','fork','evt-c3','assistant','message','three','c3','2026-01-01T00:00:01Z'),('fork-own','fork','evt-own','user','message','mine','own','2026-01-01T00:00:04Z')`)
	forkLane := func() map[string]any {
		t.Helper()
		return read("sessions=fork-session&labels=1")["lanes"].([]any)[0].(map[string]any)
	}
	lane := forkLane()
	if ids := lane["messages"].([]any); len(ids) != 1 || ids[0].(map[string]any)["id"] != "fork-own" || lane["messages_truncated"] != false || lane["inherited_messages"] != float64(3) {
		t.Fatalf("copied prefix hid the fork's own message: %v", lane)
	}
	// Recorded ancestry without typed evidence: the lane says copies may
	// remain, and why, although no single row is unresolved.
	if lane["occurrence_partial"] != true || !slices.Contains(lane["occurrence_reasons"].([]any), any("untyped-ancestry/v1")) {
		t.Fatalf("untyped fork lane coverage: %v %v", lane["occurrence_partial"], lane["occurrence_reasons"])
	}
	// A scan budget that ends before the window does is reported as truncation.
	override(t, &delegationMessageScanBatches, 1)
	if lane := forkLane(); len(lane["messages"].([]any)) != 0 || lane["messages_truncated"] != true {
		t.Fatalf("a scan stopped early must say so: %v", lane)
	}
}

// A transcript whose copied history the evidence cannot account for leaves
// its execution's Self observation metrics and its lane partial, with the
// reason, as quick stats does: untyped ancestry, retained rows without a
// history declaration, and a declared copy source that is not retained.
func TestDelegationConversationOccurrenceCoverage(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	execTx(t, c.DB, `UPDATE messages SET created_at='2026-01-01T00:00:03Z'`)
	server := testServer(t, config, c)
	check := func(step, reason string) {
		t.Helper()
		s, err := c.delegationSnapshot(t.Context(), "delegation-work")
		if err != nil {
			t.Fatal(err)
		}
		n := s.ByID["child-session"]
		if n.OccurrencePartial != reason {
			t.Fatalf("%s: occurrence reason %q, want %q", step, n.OccurrencePartial, reason)
		}
		for _, metric := range []string{"messages", "clock"} {
			if q := n.Self[metric].Quality; (reason != "") != (q == "partial") {
				t.Fatalf("%s: %s quality %q", step, metric, q)
			}
		}
		// The root's own transcript has no recorded ancestry: complete.
		if root := s.ByID["root-session"]; root.OccurrencePartial != "" || root.Self["messages"].Quality != "observed" {
			t.Fatalf("%s: root coverage %q %+v", step, root.OccurrencePartial, root.Self["messages"])
		}
		status, body := delegationGET(t, server, "/api/work/delegation-work/parallel?sessions=child-session&labels=1")
		if status != 200 {
			t.Fatal(status, body)
		}
		lane := body["lanes"].([]any)[0].(map[string]any)
		if (lane["occurrence_partial"] == true) != (reason != "") || reason != "" && !slices.Contains(lane["occurrence_reasons"].([]any), any(reason)) {
			t.Fatalf("%s: lane coverage %v %v", step, lane["occurrence_partial"], lane["occurrence_reasons"])
		}
	}
	check("untyped", "untyped-ancestry/v1")
	if _, err := c.DB.Exec(`INSERT INTO execution_evidence_state(conversation_id,version,input,id_policy) VALUES('child',?,'retained','line-locator/v1')`, executionEvidenceVersion); err != nil {
		t.Fatal(err)
	}
	check("retained without a declaration", "retained-history-undeclared/v1")
	execTx(t, c.DB, `UPDATE execution_evidence_state SET input='ingest' WHERE conversation_id='child';
 INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('child','copy','record','history_copy','conversation','native:codex|local|gone','native','codex-session-meta/v1')`)
	check("declared source not retained", "declared-copy-source-missing/v1")
	execTx(t, c.DB, `DELETE FROM execution_claims WHERE conversation_id='child'`)
	check("provider read without a history declaration", "")
	// A lane that keeps no rows from a partial transcript (all copies, or
	// none read) still says it is partial, as the Self metrics do.
	execTx(t, c.DB, `DELETE FROM execution_evidence_state WHERE conversation_id='child';
 DELETE FROM agent_session_messages WHERE message_id IN (SELECT id FROM messages WHERE conversation_id='child');
 DELETE FROM messages WHERE conversation_id='child'`)
	status, body := delegationGET(t, server, "/api/work/delegation-work/parallel?sessions=child-session&labels=1")
	if status != 200 {
		t.Fatal(status, body)
	}
	lane := body["lanes"].([]any)[0].(map[string]any)
	if len(lane["messages"].([]any)) != 0 || lane["occurrence_partial"] != true || !slices.Contains(lane["occurrence_reasons"].([]any), any("untyped-ancestry/v1")) {
		t.Fatalf("an empty lane from a partial transcript reads complete: %v %v %v", len(lane["messages"].([]any)), lane["occurrence_partial"], lane["occurrence_reasons"])
	}
}

// An execution's span is its own activity. Copied history stays in the
// transcript, but neither counts nor widens the span, whatever the stream's
// recorded envelope (derived with the copies) says. Only a stream with no
// timed observation keeps its envelope, as reported.
func TestDelegationActivitySpanFollowsOwnActivity(t *testing.T) {
	at := func(clock string) float64 {
		v, err := time.Parse(time.RFC3339, "2026-01-01T"+clock+"Z")
		if err != nil {
			t.Fatal(err)
		}
		return float64(v.UnixMilli())
	}
	message := func(id, clock string, usage bool) MessageRecord {
		m := MessageRecord{NativeID: id, Role: "assistant", Kind: "message", Text: id, CreatedAt: "2026-01-01T" + clock + "Z", Selected: true}
		if usage {
			m.Text, m.RawText = "", `{"type":"assistant","message":{"id":"`+id+`","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100,"output_tokens":10}}}`
		}
		return m
	}
	snapshot := func(t *testing.T, usage bool, child ...string) (*Catalog, *delegationSnapshot, string) {
		t.Helper()
		c, _ := testCatalog(t)
		copied := message("native-old", "10:00:00", usage)
		messages := []MessageRecord{}
		for _, id := range child {
			messages = append(messages, map[string]MessageRecord{"copy": copied, "own": message("native-own", "12:00:00", usage)}[id])
		}
		ingestRecordTx(t, c, WorkspaceRecord{SourceID: "span", SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: "root", Provider: "codex", Account: "local", Messages: []MessageRecord{copied}},
			{NativeID: "child", Provider: "codex", Account: "local", ParentNativeID: "root", AgentDepth: 1, Messages: messages},
		}})
		var workspace, root string
		if err := c.DB.QueryRow(`SELECT workspace_id,parent_id FROM conversations WHERE native_id='child'`).Scan(&workspace, &root); err != nil {
			t.Fatal(err)
		}
		// A launched agent whose transcript is not retained: a handle with
		// only its recorded envelope.
		if _, err := c.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at,ended_at) VALUES('handle',?,'codex','local','handle',?,1,'2026-01-01T11:00:00Z','2026-01-01T11:30:00Z')`, workspace, root); err != nil {
			t.Fatal(err)
		}
		s, err := c.readDelegationSnapshot(t.Context(), workspace)
		if err != nil {
			t.Fatal(err)
		}
		return c, s, workspace
	}
	node := func(t *testing.T, c *Catalog, s *delegationSnapshot, native string) *delegationNode {
		t.Helper()
		var id string
		if err := c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id=?`, native).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, n := range s.Nodes {
			if n.ConversationID == id {
				return n
			}
		}
		t.Fatalf("no row for %s", native)
		return nil
	}
	value := func(m delegationMetric) float64 {
		if m.Value == nil {
			return -1
		}
		return *m.Value
	}
	own := func(t *testing.T, n *delegationNode, requests float64) {
		t.Helper()
		if value(n.Self["messages"]) != 1 || value(n.Self["requests"]) != requests {
			t.Fatalf("own activity: messages=%+v requests=%+v", n.Self["messages"], n.Self["requests"])
		}
		if n.Start == nil || n.End == nil || *n.Start != at("12:00:00") || *n.End != at("12:00:00") || value(n.Self["clock"]) != 0 {
			t.Fatalf("copied history widened the span: start=%v end=%v clock=%+v", n.Start, n.End, n.Self["clock"])
		}
	}
	t.Run("without requests", func(t *testing.T) {
		c, s, _ := snapshot(t, false, "copy", "own")
		own(t, node(t, c, s, "child"), 0)
		// The handle has no timed observation: its envelope is its reported span.
		handle := node(t, c, s, "handle")
		if m := handle.Self["clock"]; value(m) != 30*60*1000 || m.Quality != "reported" {
			t.Fatalf("handle envelope: %+v", m)
		}
	})
	t.Run("with requests", func(t *testing.T) {
		c, s, _ := snapshot(t, true, "copy", "own")
		n := node(t, c, s, "child")
		own(t, n, 1)
		// Token facts are untouched: the copied request is not an occurrence of
		// the child, but its row's usage still reconciles the stream's total.
		var rows int
		var tokens float64
		if err := c.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(total_tokens),0) FROM model_requests WHERE conversation_id=?`, n.ConversationID).Scan(&rows, &tokens); err != nil {
			t.Fatal(err)
		}
		if rows != 2 {
			t.Fatalf("fixture: %d request rows", rows)
		}
		if total := n.Self["tokens"]; total.Value != nil && value(n.Self["remainder"]) != max(0, *total.Value-tokens) {
			t.Fatalf("remainder %+v of %+v excludes copied request usage (%v)", n.Self["remainder"], total, tokens)
		}
	})
	t.Run("inherited only", func(t *testing.T) {
		c, s, _ := snapshot(t, false, "copy")
		n := node(t, c, s, "child")
		if n.Start != nil || n.End != nil || n.Self["clock"].Value != nil || value(n.Self["messages"]) > 0 {
			t.Fatalf("a copy-only stream has its own span: start=%v end=%v clock=%+v messages=%+v", n.Start, n.End, n.Self["clock"], n.Self["messages"])
		}
	})
}

func TestDelegationAttemptEnvelopeWithoutTimedMessages(t *testing.T) {
	for _, ongoing := range []bool{false, true} {
		t.Run(fmt.Sprint("ongoing=", ongoing), func(t *testing.T) {
			c, _ := testCatalog(t)
			start, end := "2026-01-01T10:00:00.000Z", "2026-01-01T10:20:00.000Z"
			if ongoing {
				end = ""
			}
			ingestRecordTx(t, c, WorkspaceRecord{SourceID: "attempt-envelope", SourceKind: "tl1", Account: "local", Conversations: []ConversationRecord{{
				NativeID: "root", Provider: "codex", Account: "local", SessionBounds: attemptSessionBounds(start, end),
				Messages: []MessageRecord{{NativeID: "request", Kind: "metadata", RawText: `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}`}, {NativeID: "launch", CallID: "child", Kind: "delegation"}},
			}}})
			var workspace, conversation string
			if err := c.DB.QueryRow(`SELECT workspace_id,id FROM conversations WHERE native_id='root'`).Scan(&workspace, &conversation); err != nil {
				t.Fatal(err)
			}
			snapshot, err := c.readDelegationSnapshot(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range snapshot.Nodes {
				if node.NativeID != "root" {
					if node.Self["clock"].Value != nil {
						t.Fatalf("child inherited timing: %+v", node.Self["clock"])
					}
					continue
				}
				if node.TimeBoundsSource != "attempt-envelope" || node.Self["start"].Value == nil || node.Self["start"].Quality != "reported" {
					t.Fatalf("root timing: %+v", node)
				}
				clock := node.Self["clock"]
				if ongoing {
					if clock.Value != nil || node.End != nil {
						t.Fatalf("ongoing duration: %+v", clock)
					}
				} else if clock.Value == nil || *clock.Value != 20*60*1000 || clock.Quality != "reported" {
					t.Fatalf("attempt envelope: %+v", clock)
				}
				if node.Self["modelTime"].Value != nil {
					t.Fatal("attempt envelope manufactured processing time")
				}
			}
			shape, _, err := deriveSessionShapeOf(t.Context(), c.DB, conversation)
			if err != nil || shape == nil || shape.HasTimestamps || shape.WallSeconds != nil || shape.ActiveSeconds != nil {
				t.Fatalf("message-derived shape acquired timing: %+v, %v", shape, err)
			}
		})
	}
}

func TestDelegationCostFollowsUsageModelAndHistoricalDate(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	if err := c.replacePricing(testPricing(), "test"); err != nil {
		t.Fatal(err)
	}
	// One execution can change models. Its latest model cannot price all history.
	if _, err := c.DB.Exec(`UPDATE agent_sessions SET model='gpt-5.5' WHERE id='child-session';
		INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,total_tokens) VALUES
		('child-session','2026-03-01T00:00:00Z','claude-opus-4-8','session-start',1000000,1000000,1000000),
		('child-session','2026-07-01T00:00:00Z','claude-opus-4-8','session-start',1000000,1000000,1000000),
		('child-session','2026-07-02T00:00:00Z','gpt-5.5','session-start',1000000,1000000,1000000)`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if metric := snapshot.ByID["child-session"].Self["cost"]; !near(metric.Value, 17) || metric.Quality != "estimated" {
		t.Fatalf("usage-specific model/date prices should sum to $10 + $5 + $2: %+v", metric)
	}
	if _, err := c.RepairAgentSessionModels(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err = c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if node := snapshot.ByID["child-session"]; node.Model != multipleSessionModels || !near(node.Self["cost"].Value, 17) {
		t.Fatalf("mixed model summary changed historical pricing: %+v", node)
	}
}

func TestDelegationDeepAncestorContextIsBounded(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	parent := "root-session"
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("deep-%03d", i)
		_, err := c.DB.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,kind,provider,usage_status) VALUES(?,'delegation-work','root',?,?,'subagent','codex','unavailable')`, id, id, parent)
		if err != nil {
			t.Fatal(err)
		}
		parent = id
	}
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?mode=rank&search=deep-104&limit=1")
	if status != 200 || len(body["rows"].([]any)) != 1 || len(body["ancestors"].([]any)) != 100 || body["ancestors_truncated"] != true {
		t.Fatal("deep ancestry must be bounded and disclose omitted context", status)
	}
}

func TestDelegationLinksAcrossWorkspacesAndProviderBoundaries(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	run := func(query string) {
		t.Helper()
		if _, err := c.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('outside','antigravity','local','outside','Outside workspace','2026-01-01')`)
	// An outside parent of the root, and a different outside child of the
	// child (one transcript as both would be an ownership cycle).
	run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('outside-parent','outside','antigravity','local','outside-parent-native')`)
	run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('outside-conversation','outside','antigravity','local','outside-native','child',1)`)
	run(`UPDATE conversations SET parent_id='outside-parent',agent_depth=1 WHERE id='root'`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,kind,provider,usage_status) VALUES('outside-link','delegation-work','child','outside-handle','child-session','outside-conversation','subagent','codex','in-child-conversation')`)
	run(`INSERT INTO agent_session_token_evidence(agent_session_id,message_id,scope,total_tokens) VALUES('outside-link','nested-result','last-request',900)`)
	snapshot, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	root := snapshot.ByID["root-session"]
	if root.ExternalParent == nil || root.ExternalParent.WorkspaceID != "outside" || root.ExternalParent.Provider != "antigravity" {
		t.Fatal("outside parent must remain a navigable reference", root.Ancestry, root.ParentID)
	}
	// The launch handle is an alias of the outside child it created.
	id, _ := snapshot.resolveAlias("outside-link")
	link := snapshot.ByID[id]
	if link == nil || link.ExternalChild == nil || link.ExternalChild.ConversationID != "outside-conversation" || link.ExternalChild.Provider != "antigravity" {
		t.Fatal("child provider must come from its own transcript")
	}
	if link.Self["tokens"].Value != nil {
		t.Fatal("an outside child's unqueried usage must not appear as zero")
	}
	if *snapshot.Totals["tokens"].Value != 600 {
		t.Fatal("external child observation entered this workspace's total")
	}
	// A stored parent of unknown provenance is shown as not verified; an
	// acknowledged launch is not.
	if root.ParentCertainty != "legacy" || snapshot.ByID["child-session"].ParentCertainty == "legacy" || snapshot.Coverage.UnverifiedParents == 0 {
		t.Fatal("parent certainty must distinguish stored links from verified launches", root.ParentCertainty, snapshot.ByID["child-session"].ParentCertainty, snapshot.Coverage)
	}
	// Usage reported by the handle is only part of an execution whose
	// transcript is read elsewhere: partial, never a complete total.
	run(`UPDATE agent_sessions SET total_tokens=7,usage_status='reported-reconciled' WHERE id='outside-link'`)
	snapshot, err = c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	link = snapshot.ByID[id]
	if m := link.Self["tokens"]; m.Value == nil || *m.Value != 7 || m.Quality != "partial" || snapshot.Coverage.Unqueried == 0 {
		t.Fatal("usage of an execution read partly elsewhere must be partial", m, snapshot.Coverage)
	}
	if m := link.Self["messages"]; m.Quality == "observed" {
		t.Fatal("unqueried activity must not be an observed count", m)
	}
}
func TestDelegationOwnershipCyclesDoNotBreakAnalysis(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`UPDATE agent_sessions SET parent_id='nested-session' WHERE id='root-session'`); err != nil {
		t.Fatal(err)
	}
	s, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) == 0 || len(s.Nodes) != 4 {
		t.Fatal("cycles must not lose sessions", s.Warnings, s.Nodes)
	}
	// No member of the cycle becomes a known root or gets an observed depth,
	// and neither does a descendant whose chain passes through it. No edge of
	// the cycle is chosen to cut.
	for _, n := range s.Nodes {
		want, parent := "cycle", ""
		if n.ID == "embedded" {
			want, parent = "resolved", "root-session"
		}
		if n.Ancestry.State != want || n.Ancestry.Depth != nil || n.Self["depth"].Value != nil || n.ParentID != parent {
			t.Fatalf("cyclic ownership became a resolved depth: %+v", n)
		}
	}
	status, body := delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?search=Check&collapsed=child-session")
	if status != 200 || body["total"] == float64(0) {
		t.Fatal("hierarchy filters must terminate on cycles", status, body)
	}
	// A recorded transcript parent that is no longer retained is missing
	// ancestry, not a known root.
	conn, err := c.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF; UPDATE agent_sessions SET parent_id=NULL WHERE id='root-session'; UPDATE conversations SET parent_id='deleted' WHERE id='root'; PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if s, err = c.readDelegationSnapshot(context.Background(), "delegation-work"); err != nil {
		t.Fatal(err)
	}
	if root := s.ByID["root-session"]; root.Ancestry.State != "missing" || root.Ancestry.Depth != nil || s.ByID["child-session"].Ancestry.Depth != nil || len(s.ByID["nested-session"].AncestorIDs) != 2 {
		t.Fatalf("missing ancestry became a root: root=%+v child=%+v", root, s.ByID["child-session"])
	}
}

func TestDelegationFilesDeduplicateSuccessfulPaths(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	add := func(id, conv, session, name, category, status, file, absolute, repoPath, repo string) {
		t.Helper()
		_, err := c.DB.Exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status,file_path,path_absolute,repo_path,path_repository_id)
		VALUES(?,'delegation-work',?,?,0,'test','tool_call',?,?,?,?,?,?,?)`, id, conv, session, name, category, status, nilIfEmpty(file), nilIfEmpty(absolute), nilIfEmpty(repoPath), nilIfEmpty(repo))
		if err != nil {
			t.Fatal(err)
		}
	}
	add("edit-root", "root", "root-session", "Edit", "edit", "ok", "/checkout-a/a.go", "/checkout-a/a.go", "a.go", "repo-one")
	add("edit-repeat", "root", "root-session", "Write", "edit", "ok", "/checkout-a/a.go", "/checkout-a/a.go", "a.go", "repo-one")
	add("edit-shared", "child", "child-session", "Edit", "edit", "ok", "/checkout-b/a.go", "/checkout-b/a.go", "a.go", "repo-one")
	add("edit-second", "child", "child-session", "Write", "edit", "ok", "/checkout-b/b.go", "/checkout-b/b.go", "b.go", "repo-one")
	add("edit-failed", "child", "child-session", "Write", "edit", "error", "/checkout-b/failed.go", "", "failed.go", "repo-one")
	add("edit-no-result", "child", "child-session", "Edit", "edit", "no_result", "/checkout-b/unconfirmed.go", "", "unconfirmed.go", "repo-one")
	add("edit-patch", "nested", "nested-session", "apply_patch", "edit", "ok", "patch.go", "", "patch.go", "repo-one")
	add("edit-other-repo", "nested", "nested-session", "Edit", "edit", "ok", "/other/a.go", "/other/a.go", "a.go", "repo-two")
	add("edit-missing-path", "root", "embedded", "Edit", "edit", "ok", "", "", "", "")
	add("read-excluded", "root", "root-session", "Read", "read", "ok", "/checkout-a/read.go", "", "read.go", "repo-one")
	add("read-repeat", "root", "root-session", "Read", "read", "ok", "/checkout-a/read.go", "", "read.go", "repo-one")
	add("read-shared", "child", "child-session", "Read", "read", "ok", "/checkout-b/read.go", "", "read.go", "repo-one")
	add("read-second", "child", "child-session", "Read", "read", "ok", "/checkout-b/other.go", "", "other.go", "repo-one")
	add("read-other-repo", "nested", "nested-session", "Read", "read", "ok", "/other/read.go", "", "read.go", "repo-two")
	add("read-failed", "root", "root-session", "Read", "read", "error", "failed.go", "", "failed.go", "repo-one")
	add("read-unconfirmed", "child", "child-session", "Read", "read", "no_result", "pending.go", "", "pending.go", "repo-one")
	add("read-no-path", "root", "embedded", "Read", "read", "ok", "", "", "", "")
	add("search-excluded", "root", "root-session", "Grep", "search", "ok", "search.go", "", "search.go", "repo-one")
	add("list-ls", "root", "root-session", "LS", "read", "ok", "/checkout-a/src", "", "src", "repo-one")
	add("list-dir", "root", "root-session", "list_dir", "read", "ok", "/checkout-a/tests", "", "tests", "repo-one")
	add("list-directory", "child", "child-session", "list_directory", "read", "ok", "/checkout-b/docs", "", "docs", "repo-one")
	add("list-mixed-case", "nested", "nested-session", "List_Directory", "read", "ok", "/other/lib", "", "lib", "repo-two")
	add("list-no-path", "nested", "nested-session", "list_dir", "read", "ok", "", "", "", "")
	add("list-shell", "root", "root-session", "Bash", "read", "ok", "/checkout-a/bin", "", "bin", "repo-one")
	if _, err := c.DB.Exec(`UPDATE tool_calls SET command_category='list' WHERE id='list-shell'`); err != nil {
		t.Fatal(err)
	}
	s, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		metric  delegationMetric
		value   float64
		quality string
	}{
		{s.ByID["root-session"].Self["files"], 1, "observed"},
		{s.ByID["child-session"].Self["files"], 2, "partial"},
		{s.ByID["nested-session"].Self["files"], 2, "partial"},
		{s.ByID["root-session"].All["files"], 4, "partial"},
		{s.Totals["files"], 4, "partial"},
		{s.ByID["root-session"].Self["filesRead"], 1, "partial"},
		{s.ByID["child-session"].Self["filesRead"], 2, "partial"},
		// nested has recorded ancestry without typed evidence: copied
		// history may be counted, so its observation metrics are partial.
		{s.ByID["nested-session"].Self["filesRead"], 1, "partial"},
		{s.ByID["root-session"].All["filesRead"], 3, "partial"},
		{s.Totals["filesRead"], 3, "partial"},
	} {
		if !near(check.metric.Value, check.value) || check.metric.Quality != check.quality {
			t.Fatalf("files metric %+v; want %v %s", check.metric, check.value, check.quality)
		}
	}
	if s.ByID["embedded"].Self["files"].Value != nil {
		t.Fatal("missing edit paths must remain unknown")
	}
	if s.ByID["embedded"].Self["filesRead"].Value != nil {
		t.Fatal("missing read paths must remain unknown")
	}
	if s.Maxima["self"]["files"] != 2 || s.Maxima["all"]["files"] != 4 {
		t.Fatal("file metrics need per-column maxima")
	}
	if s.Maxima["self"]["filesRead"] != 2 || s.Maxima["all"]["filesRead"] != 3 {
		t.Fatal("directory listings must not inflate read-count maxima")
	}
	_, ranked := delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?mode=rank&sort=files&scope=all&direction=desc")
	if ranked["rows"].([]any)[0].(map[string]any)["id"] != "root-session" {
		t.Fatal("file counts must support inclusive metric sorting")
	}
	_, ranked = delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?mode=rank&sort=filesRead&scope=self&direction=desc")
	if ranked["rows"].([]any)[0].(map[string]any)["id"] != "child-session" {
		t.Fatal("read counts must support self metric sorting")
	}
	if delegationFileKey("a.go", "", "", "", "root") == delegationFileKey("a.go", "", "", "", "child") {
		t.Fatal("unresolved paths in different conversations must not collide")
	}
}

func TestDelegationAgentEventsFollowMessageOwnership(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	execTx(t, c.DB, `INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES
 ('embedded','user',0),('launch-alias','nested-launch',0),('child-session','nested-launch',0);
 INSERT INTO agent_events(message_id,conversation_id,source,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model) VALUES
 ('user','root','reply','assumption',0.9,0,3,'one','','','[]','[]','test'),
 ('user','root','reply','confusion',0.5,4,7,'two','','','[]','[]','test'),
 ('user','root','reply','surprise',0.49,8,11,'low','','','[]','[]','test'),
 ('nested-launch','child','thinking','assumption',0.8,0,3,'alias','','','[]','[]','test'),
 ('nested-result','child','reply','surprise',0.9,0,3,'legacy','','','[]','[]','test'),
 ('launch','root','reply','assumption',0.9,0,3,'root','','','[]','[]','test')`)
	s, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		metric delegationMetric
		want   float64
	}{
		{s.ByID["embedded"].Self["events"], 2},
		{s.ByID["child-session"].Self["events"], 2},
		{s.ByID["root-session"].Self["events"], 1},
		{s.ByID["nested-session"].Self["events"], 0},
		{s.ByID["root-session"].All["events"], 5},
		{s.Totals["events"], 5},
	} {
		if !near(check.metric.Value, check.want) {
			t.Fatalf("event count %+v; want %v", check.metric, check.want)
		}
	}
	_, ranked := delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?mode=rank&sort=events&scope=all&direction=desc")
	if ranked["rows"].([]any)[0].(map[string]any)["id"] != "root-session" {
		t.Fatal("agent event totals must support sorting")
	}
}

func TestDelegationDefaultPageSizeIsForty(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	for i := range 41 {
		id := fmt.Sprintf("page-%02d", i)
		if _, err := c.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES(?,'delegation-work','claude','local',?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	server := testServer(t, config, c)
	_, first := delegationGET(t, server, "/api/work/delegation-work/delegation")
	if len(first["rows"].([]any)) != 40 || first["total"] != float64(45) {
		t.Fatalf("expected 40 of 45 sessions, got %v rows / %v", len(first["rows"].([]any)), first["total"])
	}
	_, last := delegationGET(t, server, "/api/work/delegation-work/delegation?cursor="+url.QueryEscape(first["next_cursor"].(string)))
	if len(last["rows"].([]any)) != 5 || last["next_cursor"] != "" {
		t.Fatal("last page must retain the remaining sessions")
	}
}

func TestWorkPreviewSummarizesWorkspaceAndConversation(t *testing.T) {
	c, config := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`UPDATE conversations SET model=CASE id WHEN 'root' THEN 'opus' WHEN 'child' THEN 'gpt-5' ELSE 'gemini' END`); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	value := func(scope any, key string) any {
		return scope.(map[string]any)["totals"].(map[string]any)[key].(map[string]any)["value"]
	}
	status, work := delegationGET(t, server, "/api/work/delegation-work/preview")
	if status != 200 || len(work["metrics"].([]any)) != len(delegationMetrics) {
		t.Fatalf("workspace preview: %d %v", status, work)
	}
	workspace := work["workspace"].(map[string]any)
	if workspace["sessions"] != float64(4) || value(workspace, "children") != float64(3) || value(workspace, "tokens") != float64(600) || value(workspace, "errors") != float64(1) || work["conversation"] != nil {
		t.Fatalf("workspace scope: %v", workspace)
	}
	// The embedded session has no own model evidence and must not create a
	// fourth group by borrowing the Claude conversation's Opus model.
	if models := workspace["models"].([]any); len(models) != 3 || models[0].(map[string]any)["model"] != "gemini" {
		t.Fatalf("models must be ordered by tokens: %v", models)
	}
	// A child conversation's preview covers the agents it delegated to.
	_, child := delegationGET(t, server, "/api/work/delegation-work/preview?conversation=child")
	scope := child["conversation"].(map[string]any)
	if scope["sessions"] != float64(2) || value(scope, "children") != float64(1) || value(scope, "tokens") != float64(500) || len(scope["models"].([]any)) != 2 {
		t.Fatalf("conversation scope: %v", scope)
	}
	if child["workspace"].(map[string]any)["sessions"] != float64(4) {
		t.Fatal("a conversation preview keeps the workspace totals")
	}
	if status, _ := delegationGET(t, server, "/api/work/delegation-work/preview?conversation=elsewhere"); status != 404 {
		t.Fatal("a conversation outside the workspace must not fall back to the workspace")
	}
	// A second top-level conversation is another session, not a subagent.
	if _, err := c.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('second','delegation-work','claude','local','second-native')`); err != nil {
		t.Fatal(err)
	}
	_, more := delegationGET(t, server, "/api/work/delegation-work/preview")
	if scope := more["workspace"].(map[string]any); scope["sessions"] != float64(5) || value(scope, "children") != float64(3) {
		t.Fatalf("independent conversation counted as a subagent: %v", scope)
	}
	if status, _ := delegationGET(t, server, "/api/work/missing/preview"); status != 404 {
		t.Fatal("missing workspace")
	}
}

// The timeline groups a shell call by what its primary command does.
func TestDelegationCategoryReadsShellCommands(t *testing.T) {
	for _, test := range []struct{ name, category, command, want string }{
		{"Bash", "command", "read", "read"},
		{"exec_command", "command", "search", "read"},
		{"Bash", "command", "list", "read"},
		{"Bash", "command", "vcs", "git"},
		{"Bash", "command", "file-write", "write"},
		{"Bash", "command", "network", "network"},
		{"Bash", "command", "test", "run"},
		{"Bash", "command", "", "run"},
		{"write_stdin", "command", "", "poll"},
		{"Read", "read", "", "read"},
	} {
		if got := delegationCategory(test.name, test.category, test.command); got != test.want {
			t.Errorf("%s/%s/%s: %q, want %q", test.name, test.category, test.command, got, test.want)
		}
	}
}

// An acknowledged spawn and a native continuation disagree, with equal
// authority, about how many executions two Codex threads make. The resolver
// keeps both partitions; the explorer keeps both transcripts navigable and
// does not present its session count as exact.
func TestDelegationIdentityConflictCountIsNotExact(t *testing.T) {
	c, config := testCatalog(t)
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
	ingestRecordTx(t, c, parse("rollout-parent.jsonl",
		`{"type":"session_meta","payload":{"id":"thread-p"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Split the work"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn","arguments":"{\"message\":\"Review\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"spawn","output":"{\"agent_id\":\"thread-t\"}"}}`))
	ingestRecordTx(t, c, parse("rollout-child.jsonl",
		`{"type":"session_meta","payload":{"id":"thread-t","forked_from_id":"thread-p","forked_from_ordinal_exclusive":1}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Review"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","call_id":"spawn-g","arguments":"{\"message\":\"Check\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"spawn-g","output":"{\"agent_id\":\"thread-g\"}"}}`))
	// The grandchild's own link is exact; only its ancestor is conflicted.
	ingestRecordTx(t, c, parse("rollout-grandchild.jsonl",
		`{"type":"session_meta","payload":{"id":"thread-g","source":{"subagent":{"thread_spawn":{"parent_thread_id":"thread-t"}}}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Check"}]}}`))
	server := testServer(t, config, c)
	conversations := map[string]bool{}
	for _, native := range []string{"thread-p", "thread-t", "thread-g"} {
		var cid, ws string
		if err := c.DB.QueryRow(`SELECT id,workspace_id FROM conversations WHERE provider='codex' AND native_id=?`, native).Scan(&cid, &ws); err != nil {
			t.Fatal(err)
		}
		status, body := delegationGET(t, server, "/api/work/"+url.PathEscape(ws)+"/delegation?limit=100")
		if status != 200 {
			t.Fatal(native, body)
		}
		if body["session_count_uncertain"].(float64) == 0 || body["coverage"].(map[string]any)["conflicts"].(float64) == 0 {
			t.Fatalf("%s: a conflicted count reads as exact: uncertain=%v coverage=%v", native, body["session_count_uncertain"], body["coverage"])
		}
		// Counted in the scope usage marks run_identity uncertain: the session's
		// own conflict or any ancestor's.
		inherited, counted := 0, 0
		for _, row := range body["rows"].([]any) {
			ancestry, _ := row.(map[string]any)["ancestry"].(map[string]any)
			if row.(map[string]any)["context_only"] == true {
				continue
			}
			if ancestry["conflicted"] == true || ancestry["state"] == string(parentConflict) {
				counted++
				if ancestry["state"] != string(parentConflict) {
					inherited++
				}
			}
		}
		if native == "thread-g" && (inherited == 0 || body["session_count_uncertain"].(float64) < float64(counted)) {
			t.Fatalf("an ancestor's conflict is not counted: inherited=%d counted=%d uncertain=%v", inherited, counted, body["session_count_uncertain"])
		}
		for _, row := range append(body["rows"].([]any), body["ancestors"].([]any)...) {
			conversations[firstString(row.(map[string]any)["conversation_id"])] = true
		}
		conversations[cid] = conversations[cid] || false
	}
	for cid, shown := range conversations {
		if !shown {
			t.Fatalf("conflicted transcript %s has no navigable row", cid)
		}
	}
}

// Events resting on execution evidence that changed elsewhere are withheld in
// the explorer and its Timeline lanes, exactly as the Agent Experience
// datasets withhold them, and the counts read as partial rather than a
// confirmed number, until the ordinary events run rebuilds them.
func TestDelegationEventsFollowAgentEventsFreshness(t *testing.T) {
	ctx := t.Context()
	c := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	run := func() {
		t.Helper()
		if stats, err := c.RunLabels(ctx, labels, LabelsRunOptions{}); err != nil || !stats.Complete {
			t.Fatalf("labels: %+v %v", stats, err)
		}
		if stats, err := c.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{}); err != nil || !stats.Complete {
			t.Fatalf("agent events: %+v %v", stats, err)
		}
	}
	read := func() (delegationMetric, bool, int, bool) {
		t.Helper()
		s, err := c.delegationSnapshot(ctx, "ws-main")
		if err != nil {
			t.Fatal(err)
		}
		var n *delegationNode
		for _, row := range s.Nodes {
			if row.ConversationID == "c-events" {
				n = row
			}
		}
		if n == nil {
			t.Fatal("no row for c-events")
		}
		lanes, _, err := c.delegationTimelineMessages(ctx, s, []string{n.ID}, 0, 4e12)
		if err != nil {
			t.Fatal(err)
		}
		lane := lanes[n.ID]
		return n.Self["events"], n.EventsPending, len(lane["agent_events"].([]map[string]any)), lane["events_pending"] == true
	}
	run()
	built, pending, laneEvents, lanePending := read()
	if built.Value == nil || *built.Value == 0 || built.Quality == "partial" || pending || laneEvents == 0 || lanePending {
		t.Fatalf("current events: %+v pending=%v lane=%d/%v", built, pending, laneEvents, lanePending)
	}
	// A writer that marks nothing moves c-child under c-events.
	if err := c.writeTransaction(ctx, "repair", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE conversations SET parent_id='c-events',agent_depth=1 WHERE id='c-child'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stale, pending, laneEvents, lanePending := read()
	if stale.Quality != "partial" || stale.Value == nil || *stale.Value != 0 || !pending || laneEvents != 0 || !lanePending {
		t.Fatalf("stale events read as current: %+v pending=%v lane=%d/%v", stale, pending, laneEvents, lanePending)
	}
	// The response counts pending rows whatever a collapse leaves out, so the
	// Timeline can announce sessions it draws no lane for.
	server := testServer(t, Config{APIToken: "delegation-events"}, c)
	pendingSessions := func(collapsed string) float64 {
		t.Helper()
		status, body := delegationGET(t, server, "/api/work/ws-main/delegation?mode=hierarchy&limit=100&collapsed="+url.QueryEscape(collapsed))
		if status != 200 {
			t.Fatal(body)
		}
		count, _ := body["events_pending_sessions"].(float64)
		return count
	}
	s, err := c.delegationSnapshot(ctx, "ws-main")
	if err != nil {
		t.Fatal(err)
	}
	all, marked := []string{}, 0
	for _, n := range s.Nodes {
		all = append(all, n.ID)
		if n.EventsPending {
			marked++
		}
	}
	if open, folded := pendingSessions(""), pendingSessions(strings.Join(all, ",")); open == 0 || open != float64(marked) || folded != open {
		t.Fatalf("pending sessions: open=%v collapsed=%v marked=%d", open, folded, marked)
	}
	run()
	again, pending, laneEvents, lanePending := read()
	if again.Value == nil || *again.Value != *built.Value || again.Quality == "partial" || pending || laneEvents == 0 || lanePending {
		t.Fatalf("rebuilt events: %+v pending=%v lane=%d/%v", again, pending, laneEvents, lanePending)
	}
	if count := pendingSessions(""); count != 0 {
		t.Fatalf("rebuilt events still count %v pending sessions", count)
	}
}

func TestDelegationScopedTokenTotals(t *testing.T) {
	makeNode := func(id, parent string, direct, pool, observed float64) *delegationNode {
		m := delegationNumber(max(direct, observed), "reported", 1, 1)
		if observed > direct {
			m.Quality = "reported-minimum"
		}
		return &delegationNode{ID: id, ParentID: parent, Self: map[string]delegationMetric{"tokens": m}, TokenAttribution: &sessionTokenAttribution{Accounted: direct + pool, Direct: direct, Unallocated: pool, Observed: observed}}
	}
	for _, check := range []struct {
		name  string
		nodes []*delegationNode
		want  float64
	}{
		{"parent pool covers child minimum", []*delegationNode{makeNode("root", "", 10, 200, 0), makeNode("child", "root", 0, 0, 150)}, 210},
		{"child exceeds available pool", []*delegationNode{makeNode("root", "", 10, 100, 0), makeNode("child", "root", 0, 0, 150)}, 160},
		{"requests overlap last request", []*delegationNode{makeNode("root", "", 10, 100, 0), makeNode("child", "root", 200, 0, 150)}, 310},
		{"nested pool covers only its branch", []*delegationNode{makeNode("root", "", 10, 0, 0), makeNode("a", "root", 20, 100, 0), makeNode("a1", "a", 0, 0, 80), makeNode("b", "root", 0, 0, 70)}, 200},
		{"unrelated root cannot cover minimum", []*delegationNode{makeNode("root", "", 10, 200, 0), makeNode("other", "", 0, 0, 150)}, 360},
		{"sibling minima combine once", []*delegationNode{makeNode("root", "", 10, 100, 0), makeNode("a", "root", 0, 0, 80), makeNode("b", "root", 0, 0, 70)}, 160},
		{"aggregate and own observation overlap", []*delegationNode{makeNode("root", "", 10, 100, 80)}, 110},
		{"filtered child cannot borrow missing ancestor", []*delegationNode{makeNode("child", "missing", 0, 0, 150)}, 150},
	} {
		t.Run(check.name, func(t *testing.T) {
			got := delegationTokenTotal(check.nodes)
			if got.Value == nil || *got.Value != check.want {
				t.Fatalf("total=%+v want %v", got, check.want)
			}
		})
	}

	// An estimate combined with reported usage keeps its provenance through the
	// same aggregation used for workspace and subtree totals.
	estimated := delegationNumber(10200, "estimated", 1, 1)
	estimated.IsEstimate = true
	estimatedCost := delegationNumber(.2, "partial", 1, 1)
	estimatedCost.IsEstimate = true
	reported := &delegationNode{ID: "reported", Self: map[string]delegationMetric{
		"tokens": delegationNumber(100, "reported", 1, 1), "cost": delegationNumber(.1, "estimated", 1, 1),
		"input": delegationNumber(20, "reported", 1, 1),
	}}
	prediction := &delegationNode{ID: "prediction", Self: map[string]delegationMetric{"tokens": estimated, "cost": estimatedCost}}
	mixed := delegationAggregate([]*delegationNode{reported, prediction})
	if m := mixed["tokens"]; m.Value == nil || *m.Value != 10300 || !m.IsEstimate || m.Quality != "estimated" {
		t.Fatal("mixed token provenance lost", m)
	}
	if m := mixed["cost"]; m.Value == nil || !m.IsEstimate || m.Quality != "partial" {
		t.Fatal("mixed cost provenance lost", m)
	}
	if m := mixed["input"]; m.Value == nil || *m.Value != 20 || m.Quality != "partial" {
		t.Fatal("unknown input was treated as zero", m)
	}

	pooled := makeNode("pooled", "", 10, 100, 0)
	pooled.Self["tokens"] = delegationNumber(10, "partial", 1, 1)
	pooled.accountedTokens = map[string]delegationMetric{"tokens": delegationNumber(110, "reported", 1, 1)}
	if got := delegationTokenTotal([]*delegationNode{pooled}); got.Quality != "reported" {
		t.Fatalf("unallocated location made the accounted total partial: %+v", got)
	}

	t.Run("legacy ledger provenance", func(t *testing.T) {
		c, _ := testCatalog(t)
		seedDelegation(t, c)
		if err := c.seedSessionUsage(); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `UPDATE agent_session_usage SET attribution='request'`)
		execSQL(t, c.DB, `INSERT INTO agent_session_token_evidence(agent_session_id,message_id,scope,total_tokens) VALUES('child-session','nested-result','last-request',250)`)
		for _, version := range []string{"", "4", "5", "6", usageAttributionVersion} {
			execSQL(t, c.DB, `DELETE FROM usage_attribution_state`)
			if version != "" {
				if _, err := c.DB.Exec(`INSERT INTO usage_attribution_state(workspace_id,version) VALUES('delegation-work',?)`, version); err != nil {
					t.Fatal(err)
				}
			}
			s, err := c.delegationSnapshot(context.Background(), "delegation-work")
			if err != nil {
				t.Fatal(err)
			}
			if a := s.ByID["child-session"].TokenAttribution; a == nil || a.Observed != 250 {
				t.Fatalf("version %q lost reported minimum: %+v", version, a)
			}
			want := float64(50)
			if version == "6" || version == usageAttributionVersion {
				want = 0
			}
			if got := s.ByID["child-session"].Self["remainder"]; got.Value == nil || *got.Value != want {
				t.Fatalf("version %q: remainder=%+v want %v", version, got, want)
			}
		}
	})
	t.Run("leaf aggregate retains self usage", func(t *testing.T) {
		c := statisticsFixture(t)
		execSQL(t, c.DB, `UPDATE agent_sessions SET parent_id=(SELECT a.id FROM agent_sessions a WHERE a.conversation_id=agent_sessions.conversation_id AND a.native_id='main') WHERE native_id<>'main'`)
		execSQL(t, c.DB, `UPDATE agent_session_usage SET attribution='run-remainder' WHERE agent_session_id IN (SELECT id FROM agent_sessions WHERE native_id='main')`)
		s, err := c.delegationSnapshot(context.Background(), "w")
		if err != nil {
			t.Fatal(err)
		}
		n := s.ByID[stableID("agent-session", "c2", "main")]
		if got := n.Self["tokens"]; got.Value == nil || *got.Value != 200000 {
			t.Fatalf("leaf self tokens=%+v", got)
		}
		if got := n.Self["cost"]; got.Value == nil || *got.Value <= 0 {
			t.Fatalf("leaf self cost=%+v", got)
		}
		if a := n.TokenAttribution; a == nil || a.Unallocated != 0 || a.Direct != 200000 {
			t.Fatalf("leaf attribution=%+v", a)
		}
		branch := s.ByID[stableID("agent-session", "c1", "main")]
		if branch.Self["tokens"].Value != nil || branch.Self["cost"].Value != nil {
			t.Fatalf("branch aggregate became own usage: tokens=%+v cost=%+v", branch.Self["tokens"], branch.Self["cost"])
		}
	})

	c, _ := testCatalog(t)
	seedDelegation(t, c)
	// Warm the snapshot before evidence-only writes. No message/session edit
	// may be needed for a footer backfill, and every cache must observe it.
	before, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil || before.Totals["tokens"].Value == nil || *before.Totals["tokens"].Value != 600 {
		t.Fatalf("initial snapshot=%+v: %v", before, err)
	}
	// A launch alias's evidence follows its canonical child. Repeated copies
	// remain inspectable but are one observation, not two charges.
	if _, err := c.DB.Exec(`INSERT INTO agent_session_token_evidence(agent_session_id,message_id,scope,total_tokens) VALUES
		('nested-alias','nested-result','last-request',400),('nested-alias','nested-launch','last-request',400)`); err != nil {
		t.Fatal(err)
	}
	s, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	n := s.ByID[s.Aliases["nested-alias"]]
	if n == nil || n.TokenAttribution == nil || n.TokenAttribution.Observed != 400 || len(n.TokenAttribution.Evidence) != 2 {
		t.Fatalf("alias evidence=%+v", n)
	}
	if *n.Self["tokens"].Value != 400 || *s.Totals["tokens"].Value != 700 {
		t.Fatalf("aliased evidence counted twice: %+v", s.Totals["tokens"])
	}
	execSQL(t, c.DB, `UPDATE agent_session_token_evidence SET total_tokens=500`)
	updated, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil || updated.Totals["tokens"].Value == nil || *updated.Totals["tokens"].Value != 800 {
		t.Fatalf("updated evidence snapshot=%+v: %v", updated, err)
	}
	execSQL(t, c.DB, `DELETE FROM agent_session_token_evidence`)
	deleted, err := c.delegationSnapshot(t.Context(), "delegation-work")
	if err != nil || deleted.Totals["tokens"].Value == nil || *deleted.Totals["tokens"].Value != 600 {
		t.Fatalf("deleted evidence snapshot=%+v: %v", deleted, err)
	}

}
