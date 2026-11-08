package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// peerFixture seeds top-level work with messages and ledger calls.
type peerFixture struct {
	t    *testing.T
	tx   *sql.Tx
	base time.Time
	seq  int
}

func newPeerFixture(t *testing.T, db *sql.DB) *peerFixture {
	return &peerFixture{t: t, tx: seedTx(t, db), base: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
}

func (f *peerFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.tx.Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *peerFixture) at(minutes int) string {
	return formatTime(f.base.Add(time.Duration(minutes) * time.Minute))
}

func (f *peerFixture) work(id, title string) {
	f.exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'codex','local',?,?,'2026-01-01')`, id, id, title)
	f.exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES(?,?,'codex','local',?)`, id+"-c", id, id+"-c")
}

func (f *peerFixture) message(id, conversation, role, kind, text string, minute int) {
	f.exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES(?,?,?,?,?,?,?,?)`, id, conversation, id, role, kind, text, f.at(minute), id)
}

// edit records an edit call in conversation writing lines.
func (f *peerFixture) edit(conversation string, minute int, lines ...string) {
	f.seq++
	id := fmt.Sprintf("edit-%d", f.seq)
	f.message(id, conversation, "assistant", "tool_call", jsonText(map[string]any{"input": map[string]any{"file_path": "/repo/a.go", "old_string": "", "new_string": strings.Join(lines, "\n")}}), minute)
	f.exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,call_message_id,started_at,status)
 SELECT ?,workspace_id,id,?,'codex','tool_call','Edit','edit',?,?,'ok' FROM conversations WHERE id=?`, id, f.seq, id, f.at(minute), conversation)
}

// read records a call in conversation that showed output; diff calls show
// it as git diff added lines.
func (f *peerFixture) read(conversation string, minute int, diff bool, lines ...string) string {
	f.seq++
	id := fmt.Sprintf("read-%d", f.seq)
	body := strings.Join(lines, "\n")
	program, subcommand, category := "", "", "read"
	if diff {
		body = "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n+" + strings.Join(lines, "\n+")
		program, subcommand, category = "git", "diff", "command"
	}
	f.message(id+"-call", conversation, "assistant", "tool_call", `{"input":{"command":"git diff"}}`, minute)
	f.message(id, conversation, "tool", "tool_result", jsonText(map[string]any{"content": body}), minute)
	f.exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,program,subcommand,call_message_id,result_message_id,started_at,status)
 SELECT ?,workspace_id,id,?,'codex','tool_call','Bash',?,?,?,?,?,?,'ok' FROM conversations WHERE id=?`, id, f.seq, category, nilIfEmpty(program), nilIfEmpty(subcommand), id+"-call", id, f.at(minute), conversation)
	return id
}

func peerLines(prefix string, n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("\tresult := compute%s(value%d, options.Threshold) // %s line %d", prefix, i, prefix, i)
	}
	return lines
}

type storedPeerLink struct {
	left, right, kind, confidence, leftMessage, rightMessage, evidence string
}

func storedPeerLinks(t *testing.T, c *Catalog) map[string]storedPeerLink {
	t.Helper()
	rows, err := c.DB.Query(`SELECT left_workspace_id,right_workspace_id,kind,confidence,left_message_id,right_message_id,evidence_json FROM work_peer_links`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	links := map[string]storedPeerLink{}
	for rows.Next() {
		var l storedPeerLink
		if err := rows.Scan(&l.left, &l.right, &l.kind, &l.confidence, &l.leftMessage, &l.rightMessage, &l.evidence); err != nil {
			t.Fatal(err)
		}
		links[l.kind+":"+l.left+">"+l.right] = l
	}
	return links
}

// seedPeerReview seeds an implementation, a review whose helper read its
// diff and whose findings were pasted back, unrelated and stale work, and two
// TL1 tasks on one candidate. It returns the diff result and written lines.
func seedPeerReview(t *testing.T, c *Catalog) (string, []string) {
	t.Helper()
	f := newPeerFixture(t, c.DB)
	f.work("impl", "Implement sorting")
	f.work("review", "Workspace changes review")
	f.work("other", "Unrelated work")
	f.work("late", "Week-later reader")
	shared := "\treturn errors.Join(firstFailure, secondFailure, thirdFailure)"
	written := peerLines("Impl", 5)
	f.message("impl-ask", "impl-c", "user", "message", "Sort the running rows by start time", 0)
	f.edit("impl-c", 4, append(written, shared)...)
	f.message("impl-done", "impl-c", "assistant", "message", "Sorted by start time.", 10)
	f.message("other-ask", "other-c", "user", "message", "Something else", 1)
	f.edit("other-c", 5, append(peerLines("Other", 4), shared)...)
	f.message("other-done", "other-c", "assistant", "message", "Done.", 9)
	// The review's helper ran the diff; the read belongs to the review's family.
	f.exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth) VALUES('review-helper','review','codex','local','review-helper','review-c',1)`)
	f.message("review-ask", "review-c", "user", "message", "Please review the changes in this workspace", 12)
	diff := f.read("review-helper", 13, true, append(written, shared)...)
	f.message("review-done", "review-c", "assistant", "message", "P1: the sort ignores ties between equal start times in the running list.", 20)
	f.message("impl-fix", "impl-c", "user", "message", "Fix this: P1: the sort ignores ties between equal start times in the running list.", 24)
	f.exec(`INSERT INTO message_authorship_spans(message_id,position,category,rule,reason,start_byte,end_byte,chars,words,source_conversation_id,source_message_id)
 VALUES('impl-fix',0,'quoted','agent-output','Matches recent agent output',10,80,70,60,'review-c','review-done')`)
	// Eight days later the lines are too old to count. Its prompt reuses the
	// implementation's prompt: a person resending their own words is no hand-off.
	f.message("late-ask", "late-c", "user", "message", "Look at the diff. Sort the running rows by start time", 8*24*60)
	f.exec(`INSERT INTO message_authorship_spans(message_id,position,category,rule,reason,start_byte,end_byte,chars,words,source_conversation_id,source_message_id)
 VALUES('late-ask',0,'resent','resent','Already sent earlier',18,53,35,30,'impl-c','impl-ask')`)
	f.read("late-c", 8*24*60+1, true, written...)
	// Two TL1 tasks on one candidate.
	f.work("task-a", "Implement candidate")
	f.work("task-b", "Review candidate")
	f.exec(`INSERT INTO tl1_tasks(installation_id,task_id,workspace_id,candidate_id,created_at) VALUES('i','a','task-a','cand','2026-11-06T10:00:00Z'),('i','b','task-b','cand','2026-11-06T11:00:00Z')`)
	commitSeed(t, f.tx)
	return diff, written
}

// A review that read an implementer's fresh diff, and whose findings the
// implementer quoted, links both ways; a parallel conversation that wrote
// other code does not, and a line both wrote is shared code, not evidence.
func TestPeerLinksFromReadDiffsCopiesAndTL1(t *testing.T) {
	c, _ := testCatalog(t)
	diff, written := seedPeerReview(t, c)

	ctx := context.Background()
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	links := storedPeerLinks(t, c)
	read, ok := links["read_written_by:impl>review"]
	if !ok || read.confidence != peerHigh || read.rightMessage != diff || !strings.Contains(read.evidence, `"basis":"diff"`) || !strings.Contains(read.evidence, `"lines":5`) || !strings.Contains(read.evidence, `"timing":"start"`) {
		t.Fatalf("review did not link to the work it read: %+v (all %v)", read, links)
	}
	copy, ok := links["copied_into:review>impl"]
	if !ok || copy.confidence != peerHigh || copy.leftMessage != "review-done" || copy.rightMessage != "impl-fix" {
		t.Fatalf("quoted findings did not link back: %+v", copy)
	}
	if _, ok := links["tl1_candidate:task-a>task-b"]; !ok {
		t.Fatalf("TL1 candidate tasks not linked: %v", links)
	}
	for key := range links {
		if strings.Contains(key, "other") || strings.Contains(key, "late") {
			t.Fatalf("unrelated or stale link %s", key)
		}
	}
	if len(links) != 3 {
		t.Fatalf("links = %v", links)
	}

	// Nothing new: nothing changes. A new read is linked without disturbing
	// the others.
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	f := newPeerFixture(t, c.DB)
	f.seq = 100
	f.work("second", "Second review")
	f.message("second-ask", "second-c", "user", "message", "Review again", 30)
	f.read("second-c", 31, true, written...)
	f.message("second-done", "second-c", "assistant", "message", "Looks good.", 35)
	commitSeed(t, f.tx)
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	links = storedPeerLinks(t, c)
	if second, ok := links["read_written_by:impl>second"]; !ok || second.confidence != peerLow || !strings.Contains(second.evidence, `"timing":""`) {
		// A diff read long after the writing, with nothing to time it by, is uncertain.
		t.Fatalf("second review: %+v (all %v)", second, links)
	}
	if len(links) != 4 {
		t.Fatalf("incremental refresh changed other links: %v", links)
	}

	// The implementer's next prompt follows the second review: the review is
	// derived again and becomes certain, though no tool call is new.
	f = newPeerFixture(t, c.DB)
	f.seq = 200
	f.message("impl-next", "impl-c", "user", "message", "Rename the helper", 40)
	commitSeed(t, f.tx)
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	links = storedPeerLinks(t, c)
	if second := links["read_written_by:impl>second"]; second.confidence != peerHigh || !strings.Contains(second.evidence, `"timing":"return"`) {
		t.Fatalf("second review after the implementer's next prompt: %+v", second)
	}
	// A diff stored before its output came back gets the output in place,
	// keeping its rowid; the refresh still reads it.
	f = newPeerFixture(t, c.DB)
	f.seq = 250
	f.work("pending", "Pending review")
	f.message("pending-ask", "pending-c", "user", "message", "Review it", 42)
	pending := f.read("pending-c", 43, true, written...)
	f.exec(`UPDATE tool_calls SET result_message_id=NULL WHERE id=?`, pending)
	commitSeed(t, f.tx)
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := storedPeerLinks(t, c)["read_written_by:impl>pending"]; ok {
		t.Fatal("a diff without output linked")
	}
	if _, err := c.DB.Exec(`UPDATE tool_calls SET result_message_id=? WHERE id=?`, pending, pending); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if l, ok := storedPeerLinks(t, c)["read_written_by:impl>pending"]; !ok || l.rightMessage != pending {
		t.Fatalf("a diff whose output arrived later was not read: %+v", l)
	}
	if err := c.DB.QueryRow(`SELECT 1 FROM work_peer_links_dirty`).Scan(new(int)); err != sql.ErrNoRows {
		t.Fatalf("late output stays marked after the refresh read it: %v", err)
	}
	// A family that joins another (here, launched by the review) stops being
	// related work of its own.
	if _, err := c.DB.Exec(`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('second-c','review-c','launch','codex')`); err != nil {
		t.Fatal(err)
	}
	f = newPeerFixture(t, c.DB)
	f.seq = 300
	f.edit("other-c", 50, "\tanother := unrelatedChangeElsewhere(value, options.Threshold)")
	commitSeed(t, f.tx)
	if err := c.RefreshPeerLinks(ctx); err != nil {
		t.Fatal(err)
	}
	for key := range storedPeerLinks(t, c) {
		if strings.HasSuffix(key, ">second") {
			t.Fatalf("a launched family kept its own link: %s", key)
		}
	}
}

// Confidence follows what was measured on a full library: a diff is certain
// with timing (the reader began as the writer's turn ended without writing
// anything itself, or the writer's next prompt followed it), uncertain
// without, and dropped when the reader had been writing code of its own and
// nothing times it. Lines only read need timing; equal evidence is no choice.
func TestPeerLinkConfidence(t *testing.T) {
	c, _ := testCatalog(t)
	f := newPeerFixture(t, c.DB)
	for _, id := range []string{"writer", "busy", "untimed", "busy-late", "reader", "rival", "tied", "writer2", "returner", "writer3", "rereader", "compound"} {
		f.work(id, id)
	}
	lines := peerLines("Writer", 6)
	f.message("writer-ask", "writer-c", "user", "message", "Write it", 0)
	f.edit("writer-c", 2, lines...)
	f.message("writer-done", "writer-c", "assistant", "message", "Written.", 3)
	// Began as the writer finished, but wrote its own code first.
	f.message("busy-ask", "busy-c", "user", "message", "Other feature", 4)
	f.edit("busy-c", 5, peerLines("Busy", 4)...)
	f.read("busy-c", 10, true, lines...)
	f.message("busy-done", "busy-c", "assistant", "message", "Done.", 11)
	// Read the diff hours later, having written nothing.
	f.message("untimed-ask", "untimed-c", "user", "message", "Look at the changes", 200)
	f.read("untimed-c", 201, true, lines...)
	// Hours later, after writing its own code.
	f.message("busy-late-ask", "busy-late-c", "user", "message", "Another feature", 300)
	f.edit("busy-late-c", 301, peerLines("BusyLate", 4)...)
	f.read("busy-late-c", 302, true, lines...)
	// Only conversations that looked at changes are read at all; this one's
	// file read has no timing.
	f.message("reader-ask", "reader-c", "user", "message", "Check the file", 400)
	f.read("reader-c", 400, true, "\tthis diff shows nothing that anyone here wrote at any point")
	f.read("reader-c", 401, false, lines...)
	f.message("reader-done", "reader-c", "assistant", "message", "Checked.", 402)
	// Equal evidence from two writers is not a choice.
	f.message("rival-ask", "rival-c", "user", "message", "Write more", 500)
	f.edit("rival-c", 501, peerLines("Rival", 6)...)
	f.message("tied-ask", "tied-c", "user", "message", "Check both", 600)
	f.read("tied-c", 601, true, append(lines, peerLines("Rival", 6)...)...)
	// The writer's next prompt followed this reader: certain, even though it
	// had written code of its own.
	lines2 := peerLines("Second", 5)
	f.message("writer2-ask", "writer2-c", "user", "message", "Write the second part", 700)
	f.edit("writer2-c", 701, lines2...)
	f.message("writer2-done", "writer2-c", "assistant", "message", "Written.", 702)
	f.message("returner-ask", "returner-c", "user", "message", "Review the second part", 800)
	f.edit("returner-c", 800, peerLines("Returner", 3)...)
	f.read("returner-c", 801, true, lines2...)
	f.message("returner-done", "returner-c", "assistant", "message", "P2: rename it.", 805)
	f.message("writer2-fix", "writer2-c", "user", "message", "Rename it", 810)
	// Opening the files first does not weaken the diff that shows the same
	// lines as added.
	lines3 := peerLines("Third", 5)
	f.message("writer3-ask", "writer3-c", "user", "message", "Write the third part", 900)
	f.edit("writer3-c", 901, lines3...)
	f.message("writer3-done", "writer3-c", "assistant", "message", "Written.", 902)
	f.message("rereader-ask", "rereader-c", "user", "message", "Review the third part", 903)
	f.read("rereader-c", 903, false, lines3...)
	rediff := f.read("rereader-c", 904, true, lines3...)
	// A diff after another command in one call still shows changes.
	f.message("compound-ask", "compound-c", "user", "message", "Check the third part", 905)
	compound := f.read("compound-c", 905, true, lines3...)
	f.exec(`UPDATE tool_calls SET subcommand='status',command_count=2 WHERE id=?`, compound)
	f.exec(`INSERT INTO tool_commands(tool_call_id,position,command,program,subcommand) VALUES(?,0,'git status --short','git','status'),(?,1,'git diff','git','diff')`, compound, compound)
	// A short quote is uncertain; a reply quoted days later is not linked.
	for _, quote := range []struct {
		id            string
		minute, words int
	}{{"short", 30, 14}, {"stale", 3 * 24 * 60, 120}} {
		f.work(quote.id, quote.id)
		f.message(quote.id+"-ask", quote.id+"-c", "user", "message", "Following up on: Written.", quote.minute)
		f.exec(`INSERT INTO message_authorship_spans(message_id,position,category,rule,reason,start_byte,end_byte,chars,words,source_conversation_id,source_message_id)
 VALUES(?,0,'quoted','agent-output','Matches recent agent output',0,20,20,?,'writer-c','writer-done')`, quote.id+"-ask", quote.words)
	}
	commitSeed(t, f.tx)
	if err := c.RefreshPeerLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	links := storedPeerLinks(t, c)
	want := map[string]string{
		"copied_into:writer>short":         peerLow + "/",
		"read_written_by:writer>busy":      peerLow + "/start",
		"read_written_by:writer>untimed":   peerLow + "/",
		"read_written_by:writer2>returner": peerHigh + "/return",
		"read_written_by:writer3>rereader": peerHigh + "/start",
		"read_written_by:writer3>compound": peerHigh + "/start",
	}
	if l := links["read_written_by:writer3>rereader"]; l.rightMessage != rediff {
		t.Fatalf("a diff link is anchored on %q, not the diff %q", l.rightMessage, rediff)
	}
	got := map[string]string{}
	for key, l := range links {
		var evidence struct{ Timing string }
		_ = json.Unmarshal([]byte(l.evidence), &evidence)
		got[key] = l.confidence + "/" + evidence.Timing
	}
	if len(got) != len(want) {
		t.Fatalf("links = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q (all %v)", key, got[key], value, got)
		}
	}
}

// A work page lists related work with roles, marks the messages a hand-off
// is anchored on, and shows certain peers' agent sessions in its Hierarchy
// and Timeline beside its own, keeping its own totals.
func TestWorkPeersOnTheWorkPage(t *testing.T) {
	c, config := testCatalog(t)
	diff, _ := seedPeerReview(t, c)
	if err := c.RefreshPeerLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A second certain review of the implementation, and an uncertain one.
	if _, err := c.DB.Exec(`INSERT INTO work_peer_links(left_workspace_id,right_workspace_id,kind,confidence,at,evidence_json) VALUES
		('impl','late','read_written_by','high','2026-11-06T13:00:00Z','{}'),('impl','other','read_written_by','low','2026-11-06T13:05:00Z','{}')`); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	get := func(path string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+config.APIToken)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var body map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body)
		}
		return body
	}
	peers := func(workspace string) map[string]string {
		out := map[string]string{}
		for _, p := range get("/api/work/" + workspace)["peers"].([]any) {
			peer := p.(map[string]any)
			out[firstString(peer["workspace_id"])] = firstString(peer["role"]) + "/" + firstString(peer["confidence"]) + "/" + firstString(peer["via"])
		}
		return out
	}
	if got := peers("impl"); len(got) != 3 || got["review"] != "reviewer/high/" || got["late"] != "reviewer/high/" || got["other"] != "reviewer/low/" {
		t.Fatalf("implementation peers = %v", got)
	}
	// From a review, the implementation is direct and its other certain
	// review one hop on; uncertain links are not followed.
	if got := peers("review"); len(got) != 2 || got["impl"] != "implementer/high/" || got["late"] != "reviewer/high/impl" {
		t.Fatalf("review peers = %v", got)
	}
	handoffs := map[string]string{}
	for _, workspace := range [][2]string{{"impl", "impl-c"}, {"review", "review-c"}, {"review", "review-helper"}} {
		for _, m := range get("/api/work/" + workspace[0] + "/conversations/" + workspace[1])["messages"].([]any) {
			message := m.(map[string]any)
			for _, direction := range []string{"in", "out"} {
				for _, h := range asSlice(message["handoffs_"+direction]) {
					handoff := h.(map[string]any)
					handoffs[firstString(message["id"])+":"+direction] = firstString(handoff["kind"]) + ">" + firstString(handoff["message_id"]) + "/" + firstString(handoff["role"])
				}
			}
		}
	}
	if handoffs["impl-fix:in"] != "copied_into>review-done/related" || handoffs["review-done:out"] != "copied_into>impl-fix/related" ||
		handoffs[diff+":in"] != "read_written_by>edit-1/implementer" || handoffs["edit-1:out"] != "read_written_by>"+diff+"/reviewer" {
		t.Fatalf("hand-offs = %v", handoffs)
	}

	tree := get("/api/work/impl/delegation?limit=100")
	byConversation := map[string]map[string]any{}
	for _, r := range tree["rows"].([]any) {
		row := r.(map[string]any)
		byConversation[firstString(row["conversation_id"])] = row
	}
	if len(byConversation) != 4 || byConversation["impl-c"]["peer"] != nil || byConversation["other-c"] != nil {
		t.Fatalf("hierarchy rows = %v", byConversation)
	}
	review := byConversation["review-c"]
	if peer, _ := review["peer"].(map[string]any); peer == nil || peer["workspace_id"] != "review" || peer["role"] != "reviewer" || review["depth"] != 0.0 {
		t.Fatalf("review root = %v", review)
	}
	if helper := byConversation["review-helper"]; helper["depth"] != 1.0 || helper["parent_id"] != review["id"] {
		t.Fatalf("review helper = %v", helper)
	}
	own, group := tree["totals"].(map[string]any), tree["group_totals"].(map[string]any)
	if own["children"] == nil || jsonText(own["messages"]) == jsonText(group["messages"]) {
		t.Fatalf("totals: own %v group %v", own["messages"], group["messages"])
	}
	lanes := get("/api/work/impl/parallel?sessions=" + firstString(review["id"]))
	if len(lanes["lanes"].([]any)) != 1 {
		t.Fatalf("review lane missing from the timeline: %v", lanes)
	}
	// The peer's lane reads its own transcript at the group's revision.
	labelled := get("/api/work/impl/parallel?labels=1&sessions=" + firstString(review["id"]))["lanes"].([]any)
	messages := map[string]bool{}
	for _, m := range asSlice(labelled[0].(map[string]any)["messages"]) {
		messages[firstString(m.(map[string]any)["id"])] = true
	}
	if !messages["review-ask"] || !messages["review-done"] || messages["impl-ask"] {
		t.Fatalf("review lane messages = %v", messages)
	}

	// The group keeps each part's members and the revision every part shares,
	// and an evidence commit composes it again from the new snapshots.
	reviewID := firstString(review["id"])
	if _, err := c.DB.Exec(`INSERT INTO execution_claims(conversation_id,claim_key,source,kind,subject,object,origin,rule) VALUES('review-c','late','record','launch_outcome','launch:call-late','','derived','test/v1')`); err != nil {
		t.Fatal(err)
	}
	resolvedGroup, err := c.workGroupSnapshot(t.Context(), "impl", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownSnapshot, _ := c.delegationSnapshot(t.Context(), "impl")
	reviewSnapshot, _ := c.delegationSnapshot(t.Context(), "review")
	if resolvedGroup.Revision != ownSnapshot.Revision || resolvedGroup.Revision != reviewSnapshot.Revision || resolvedGroup.projection.res == nil ||
		!slices.Equal(resolvedGroup.members[reviewID], reviewSnapshot.members[reviewID]) || len(resolvedGroup.members[reviewID]) == 0 {
		t.Fatalf("group revision %v (own %v, review %v), members %v", resolvedGroup.Revision, ownSnapshot.Revision, reviewSnapshot.Revision, resolvedGroup.members[reviewID])
	}

	// Parts resolved at different revisions are never one graph: the group is
	// composed again, and while they keep differing the work shows alone and
	// nothing mixed is cached. Relatives reach every part. (Relatives that
	// name no row give each case its own cache key.)
	read, stale := workGroupPart, 0
	asked := map[string][]string{}
	override(t, &workGroupPart, func(c *Catalog, ctx context.Context, workspace string, relatives []string) (*delegationSnapshot, error) {
		s, err := read(c, ctx, workspace, relatives)
		asked[workspace] = relatives
		if err != nil || s == nil || workspace != "review" || stale == 0 {
			return s, err
		}
		stale--
		changed := *s
		changed.Revision.Generation--
		return &changed, nil
	})
	stale = 1
	if resolvedGroup, err = c.workGroupSnapshot(t.Context(), "impl", []string{"retry"}); err != nil || resolvedGroup.ByID[reviewID] == nil || stale != 0 ||
		!slices.Equal(asked["review"], []string{"retry"}) {
		t.Fatal("a retry composes the group", err, stale, asked)
	}
	stale = workGroupAttempts
	if resolvedGroup, err = c.workGroupSnapshot(t.Context(), "impl", []string{"settle"}); err != nil || resolvedGroup.ByID[reviewID] != nil || resolvedGroup.GroupTotals != nil ||
		!slices.Contains(resolvedGroup.Warnings, "Related work is changing; its agent sessions are left out until it settles.") {
		t.Fatal("mixed revisions show the work alone", err, resolvedGroup)
	}
	if shared, _ := read(c, t.Context(), "impl", []string{"settle"}); slices.Contains(shared.Warnings, resolvedGroup.Warnings[len(resolvedGroup.Warnings)-1]) {
		t.Fatal("the warning changed the cached snapshot")
	}
	if resolvedGroup, err = c.workGroupSnapshot(t.Context(), "impl", []string{"settle"}); err != nil || resolvedGroup.ByID[reviewID] == nil {
		t.Fatal("a settled group composes", err)
	}
	// Work without certain peers keeps its own snapshot.
	if rows := get("/api/work/other/delegation")["rows"].([]any); len(rows) != 1 {
		t.Fatalf("unrelated hierarchy = %v", rows)
	}
}

func asSlice(value any) []any {
	list, _ := value.([]any)
	return list
}
