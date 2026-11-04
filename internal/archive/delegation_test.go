package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
	for _, cv := range []struct{ id, provider, native, parent string }{{"root", "claude", "root-native", ""}, {"child", "codex", "codex-native", "root"}, {"nested", "antigravity", "root:subagent:agent-exact-child", "root"}} {
		run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id) VALUES(?,'delegation-work',?,'local',?,?)`, cv.id, cv.provider, cv.native, nilIfEmpty(cv.parent))
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
		{"nested-session", "nested", "main", "", "", "", "antigravity", "reported-reconciled", "2026-01-01T00:00:05Z", "2026-01-01T00:00:07Z", 300},
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
	if child.Provider != "codex" || nested.Provider != "antigravity" || child.ParentID != "root-session" || nested.ParentID != child.ID {
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

func TestDelegationResultEvidenceKeepsConversationAndResultScope(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	// A call ID can be reused in another conversation. Only result messages
	// in the launch's own conversation can supply its exact child handle.
	for _, message := range []struct{ id, conversation, kind, text string }{
		{"root-result", "root", "tool_result", strings.Repeat("r", 9000)},
		{"not-a-result", "child", "message", "must not match"},
	} {
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash)
			VALUES(?,?,?,'tool',?,?,'nested-call',?)`, message.id, message.conversation, message.id, message.kind, message.text, message.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,call_id,content_hash)
		VALUES('root-launch','root','root-launch','assistant','delegation','{}','nested-call','root-launch');
		INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,delegation_message_id,kind,provider,usage_status)
		VALUES('root-launch-session','delegation-work','root','reused-call','root-launch','subagent','codex','unavailable')`); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(c.DB, delegationResultEvidenceSQL, "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, row := range rows {
		byID[firstString(row["id"])] = firstString(row["text"])
	}
	if len(rows) != 2 || byID["nested-alias"] != `{"details":{"agentId":"exact-child"}}` || byID["root-launch-session"] != strings.Repeat("r", 8192) {
		t.Fatalf("match result kind, conversation and bounded body: %v", byID)
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
	rows, err := queryMaps(c.DB, delegationMessageCountsSQL, "delegation-work", "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[firstString(row["agent_session_id"])] = row
	}
	if len(rows) != 3 {
		t.Fatalf("count only recorded membership: %v", rows)
	}
	for _, id := range []string{"root-session", "embedded"} {
		row := byID[id]
		if integer(row["messages"]) != 1 || integer(row["turns"]) != 1 || row["start"] != "2026-01-01T00:00:03Z" || row["end"] != row["start"] {
			t.Fatalf("each session retains its own membership: %v", row)
		}
	}
	if row := byID["child-session"]; integer(row["messages"]) != 2 || integer(row["turns"]) != 0 || row["start"] != nil || row["end"] != nil {
		t.Fatalf("events with unknown timestamps still count: %v", row)
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
			want := "Delegated work"
			if tc.second {
				want = "First root prompt"
			}
			if got := s.ByID["root-session"].Label; got != want {
				t.Fatalf("first root label = %q, want %q", got, want)
			}
			if tc.second {
				id := "conversation:root-two"
				if tc.derived {
					id = "root-two-session"
				}
				want = firstString(tc.secondPrompt, "root-two-native")
				if got := s.ByID[id].Label; got != want {
					t.Fatalf("second root label = %q, want %q", got, want)
				}
			}
			if got := s.ByID["child-session"].Label; got != "Review with Codex" {
				t.Fatalf("child assignment label changed: %q", got)
			}
		})
	}
}

func TestDelegationAssignmentLabels(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"codex task name after opaque message", `{"tool":"spawn_agent","input":{"fork_turns":"all","message":"gAAAA` + strings.Repeat("x", 12000) + `","task_name":"tooling_independent_review"}}`, "tooling_independent_review"},
		{"task name before longer description", `{"input":{"task_name":"review","description":"Review all of the implementation"}}`, "review"},
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
				{`UPDATE agent_sessions SET delegation_message_id='launch' WHERE id='embedded'`, nil},
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
			for _, node := range []struct{ id, fallback string }{{"child-session", "Child nickname"}, {"embedded", "other-execution"}} {
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
	if childMark["end_known"] != false {
		t.Fatal("missing end evidence must not imply a zero-duration call")
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
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('test','["P","P.request","P.request.work.implement"]');
 INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('root','test',1,'2026-01-01');
 DELETE FROM message_labels_dirty`)
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
	run(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id) VALUES('outside-conversation','outside','antigravity','local','outside-native','child')`)
	run(`UPDATE conversations SET parent_id='outside-conversation' WHERE id='root'`)
	run(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,child_conversation_id,kind,provider,usage_status) VALUES('outside-link','delegation-work','child','outside-handle','child-session','outside-conversation','subagent','codex','in-child-conversation')`)
	snapshot, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	root := snapshot.ByID["root-session"]
	if root.ExternalParent == nil || root.ExternalParent.WorkspaceID != "outside" || root.ExternalParent.Provider != "antigravity" {
		t.Fatal("outside parent must remain a navigable reference")
	}
	link := snapshot.ByID["outside-link"]
	if link.ExternalChild == nil || link.ExternalChild.ConversationID != "outside-conversation" || link.ExternalChild.Provider != "antigravity" {
		t.Fatal("child provider must come from its own transcript")
	}
	if link.Self["tokens"].Value != nil {
		t.Fatal("an outside child's unqueried usage must not appear as zero")
	}
}
func TestDelegationOwnershipCyclesDoNotBreakAnalysis(t *testing.T) {
	c, _ := testCatalog(t)
	seedDelegation(t, c)
	if _, err := c.DB.Exec(`UPDATE agent_sessions SET parent_id='nested-session' WHERE id='root-session'`); err != nil {
		t.Fatal(err)
	}
	s, err := c.delegationSnapshot(context.Background(), "delegation-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) == 0 || len(s.Nodes) != 4 {
		t.Fatal("cycles must be excluded without losing sessions")
	}
}

func TestDelegationFilesEditedDeduplicatesSuccessfulPaths(t *testing.T) {
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
	} {
		if !near(check.metric.Value, check.value) || check.metric.Quality != check.quality {
			t.Fatalf("files metric %+v; want %v %s", check.metric, check.value, check.quality)
		}
	}
	if s.ByID["embedded"].Self["files"].Value != nil {
		t.Fatal("missing edit paths must remain unknown")
	}
	if s.Maxima["self"]["files"] != 2 || s.Maxima["all"]["files"] != 4 {
		t.Fatal("file metrics need per-column maxima")
	}
	_, ranked := delegationGET(t, testServer(t, config, c), "/api/work/delegation-work/delegation?mode=rank&sort=files&scope=all&direction=desc")
	if ranked["rows"].([]any)[0].(map[string]any)["id"] != "root-session" {
		t.Fatal("file counts must support inclusive metric sorting")
	}
	if delegationFileKey("a.go", "", "", "", "root") == delegationFileKey("a.go", "", "", "", "child") {
		t.Fatal("unresolved paths in different conversations must not collide")
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
	if models := workspace["models"].([]any); len(models) != 4 || models[0].(map[string]any)["model"] != "gemini" {
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
