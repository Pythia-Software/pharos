package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// labelingFixtureNow is the snapshot time the fixture's fresh window counts from.
var labelingFixtureNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// seedLabelingCatalog writes a small invented library: a root conversation
// with a delegated child, a mirror of the root, a later conversation that
// re-sends the root's request and answers a question form, and a recent one.
func seedLabelingCatalog(t *testing.T, catalog *Catalog) {
	t.Helper()
	// Long enough (over labelingCopyLinkBytes) for its re-sending to link families.
	request := "Please investigate why the export button disappears on mobile when the sidebar is collapsed, " +
		"and check whether the same thing happens on tablets in portrait orientation, because the support team reported both cases this week"
	tx := seedTx(t, catalog.DB)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	for _, w := range [][2]string{{"ws-main", "claude"}, {"ws-mirror", "conductor"}, {"ws-other", "claude"}, {"ws-new", "codex"}, {"ws-late", "claude"},
		{"ws-daily1", "claude"}, {"ws-daily2", "claude"}} {
		exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,?,?,?,?,?)`, w[0], w[1], "local", w[0], w[0], "2026-10-01")
	}
	for _, c := range []struct {
		id, workspace, provider, parent, started string
		depth                                    int
	}{
		{"c-root", "ws-main", "claude", "", "2026-09-01T12:00:00.000Z", 0},
		{"c-child", "ws-main", "claude", "c-root", "2026-09-01T12:05:00.000Z", 1},
		{"c-mirror", "ws-mirror", "claude", "", "2026-09-01T12:00:00.000Z", 0},
		{"c-other", "ws-other", "claude", "", "2026-09-03T12:00:00.000Z", 0},
		{"c-new", "ws-new", "codex", "", "2026-10-20T12:00:00.000Z", 0},
		{"c-fork", "ws-main", "codex", "c-root", "2026-09-02T12:00:00.000Z", 0},
		{"c-fork2", "ws-main", "codex", "c-root", "2026-09-02T12:01:00.000Z", 0},
		{"c-fork3", "ws-main", "codex", "c-root", "", 0},
		{"c-fork4", "ws-main", "codex", "c-root", "2026-09-02T00:00:00.000Z", 0},
		{"c-fork5", "ws-main", "codex", "c-root", "2026-09-02T12:02:00.000Z", 0},
		{"c-late", "ws-late", "claude", "", "2026-10-21T12:00:00.000Z", 0},
		{"c-daily1", "ws-daily1", "claude", "", "2026-09-05T12:00:00.000Z", 0},
		{"c-daily2", "ws-daily2", "claude", "", "2026-09-06T12:00:00.000Z", 0},
	} {
		exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES(?,?,?,?,?,?,?,?)`,
			c.id, c.workspace, c.provider, "local", c.id, nilIfEmpty(c.parent), c.depth, c.started)
	}
	exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('c-root','c-mirror','mirror',1,'{}')`)
	order := 0
	message := func(id, conversation, role, kind, text, at string, extra map[string]any) {
		t.Helper()
		order++
		selected := 1
		if extra["selected"] == 0 {
			selected = 0
		}
		var created any = at
		if at == "" {
			created = nil
		}
		exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,sender,source_order,call_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, conversation, id, role, kind, text, "hash-"+id, created, selected, extra["sender"], order, extra["call"])
	}
	at := func(day, minute int) string { return fmt.Sprintf("2026-09-%02dT12:%02d:00.000Z", day, minute) }
	subPrompt := "Check the CSS files for media queries that hide the export button on small screens please"
	message("m-h1", "c-root", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(1, 0), map[string]any{"sender": "account:me"})
	message("m-a1", "c-root", "assistant", "message", "I will look into the export button.", at(1, 1), nil)
	message("m-think", "c-root", "assistant", "thinking", "Maybe a media query hides it.", at(1, 2), nil)
	message("m-unavailable", "c-root", "assistant", "metadata", `{"reason":"encrypted","type":"reasoning_unavailable"}`, at(1, 2), nil)
	message("m-del", "c-root", "assistant", "delegation", `{"tool":"Task","input":{"prompt":"Find the CSS rule that hides the export button"}}`, at(1, 3), map[string]any{"call": "call-1"})
	message("m-tool", "c-root", "assistant", "tool_call", `{"tool":"Bash","input":{"command":"ls"}}`, at(1, 4), map[string]any{"call": "call-2"})
	message("m-tool-result", "c-root", "tool", "tool_result", `{"content":"ok","tool":"Bash"}`, at(1, 4), map[string]any{"call": "call-2"})
	message("m-delres", "c-root", "tool", "delegation_result", `{"content":"The rule is in mobile.css. I also checked the desktop styles.","tool":"Task"}`, at(1, 6), map[string]any{"call": "call-1"})
	message("m-branch", "c-root", "user", "message", "An abandoned branch", at(1, 7), map[string]any{"selected": 0})
	message("m-h2", "c-root", "user", "message", "?", at(1, 8), nil)
	message("m-auto", "c-root", "user", "message", "Summarize the repository layout", at(1, 9), map[string]any{"sender": "automation:codex-exec"})
	message("m-untimed", "c-root", "user", "message", "A message without a send time", "", nil)
	message("m-harness-only", "c-root", "user", "message", "<system-reminder>\nOnly harness.\n</system-reminder>", at(1, 9), map[string]any{"sender": "automation:codex-exec"})
	message("m-bad", "c-root", "assistant", "message", "broken \xff\xfe bytes", at(1, 9), nil)
	// An embedded sub-agent's exchange: its own context, not the main session's.
	message("m-sub1", "c-root", "user", "message", subPrompt, at(1, 9), nil)
	message("m-sub2", "c-root", "assistant", "message", "Checked: one media query hides it.", at(1, 9), nil)
	message("m-a2", "c-root", "assistant", "message", "Done: the rule was removed.", at(1, 10), nil)
	message("m-h3", "c-root", "user", "message", "continue", at(1, 11), nil)
	message("m-c1", "c-child", "user", "message", "Find the CSS rule that hides the export button", at(1, 5), nil)
	message("m-c2", "c-child", "assistant", "message", "The rule is in mobile.css.", at(1, 6), nil)
	// A question a delegated agent asked: no person answers it.
	message("m-c-ask", "c-child", "assistant", "tool_call", `{"tool":"AskUserQuestion","input":{"questions":[{"question":"Which file?","options":[{"label":"mobile.css"}]}]}}`, at(1, 6), map[string]any{"call": "call-5"})
	message("m-c-ask-result", "c-child", "tool", "tool_result", `{"content":"User has answered your questions: \"Which file?\"=\"mobile.css\". You can now continue.","tool":"AskUserQuestion"}`, at(1, 6), map[string]any{"call": "call-5"})
	message("m-mirror", "c-mirror", "user", "message", request, at(1, 0), nil)
	message("m-o1", "c-other", "user", "message", request, at(3, 0), nil)
	message("m-ask", "c-other", "assistant", "tool_call", `{"tool":"AskUserQuestion","input":{"questions":[{"question":"Which mode?","options":[{"label":"Records"},{"label":"Text"}]}]}}`, at(3, 1), map[string]any{"call": "call-3"})
	message("m-ask-result", "c-other", "tool", "tool_result", `{"content":"User has answered your questions: \"Which mode?\"=\"Records\". You can now continue.","tool":"AskUserQuestion"}`, at(3, 2), map[string]any{"call": "call-3"})
	message("m-o2", "c-other", "user", "message", "Which mode?\nRecords", at(3, 3), nil)
	message("m-ask2", "c-other", "assistant", "tool_call", `{"tool":"AskUserQuestion","input":{"questions":[{"question":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}}`, at(3, 4), map[string]any{"call": "call-4"})
	message("m-ask2-result", "c-other", "tool", "tool_result", `{"content":"User has answered your questions: \"Ship it?\"=\"Yes\". You can now continue with the user's answers in mind.","tool":"AskUserQuestion"}`, at(3, 5), map[string]any{"call": "call-4"})
	message("m-ask3", "c-other", "assistant", "tool_call", `{"tool":"AskUserQuestion","input":{"questions":[{"question":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}}`, at(3, 5), map[string]any{"call": "call-6", "selected": 0})
	message("m-ask3-result", "c-other", "tool", "tool_result", `{"content":"User has answered your questions: \"Ship it?\"=\"No\". You can now continue.","tool":"AskUserQuestion"}`, at(3, 5), map[string]any{"call": "call-6", "selected": 0})
	message("m-o-think", "c-other", "assistant", "thinking", "Extraction has not caught up here.", at(3, 6), nil)
	message("m-o3", "c-other", "assistant", "message", "Open https://example.test/build?token=abcd1234efgh5678 to see it.", at(3, 7), nil)
	panicLog := "Here is the failure:\n```\npanic: runtime error: index out of range while rendering the mobile sidebar layout in the export handler today\n```"
	claim := "The export button disappears because the sidebar collapse handler removes every child of the toolbar container on narrow screens."
	message("m-o4", "c-other", "user", "message", panicLog, at(3, 10), nil)
	message("m-o5", "c-other", "assistant", "message", claim, at(3, 11), nil)
	message("m-o6", "c-other", "user", "message", claim+"\n\nIs that right?", at(3, 12), nil)
	// Weeks later, the log and the agent's claim are sent again: neither
	// becomes the person's words.
	message("m-n3", "c-late", "user", "message", panicLog, "2026-10-21T12:00:00.000Z", nil)
	message("m-n4", "c-late", "user", "message", claim, "2026-10-21T12:10:00.000Z", nil)
	// A person re-sends a sub-agent's prompt: it stays the agent's text.
	message("m-o7", "c-other", "user", "message", subPrompt+"\n\nthanks", at(3, 20), nil)
	daily := "Summarize yesterday's merged pull requests for the team channel"
	message("m-d1", "c-daily1", "user", "message", daily, at(5, 0), map[string]any{"sender": "automation:claude-sdk-cli"})
	message("m-d2", "c-daily2", "user", "message", daily, at(6, 0), map[string]any{"sender": "automation:claude-sdk-cli"})
	message("m-n1", "c-new", "user", "message", "Add a dark mode toggle to settings", "2026-10-20T12:00:00.000Z", nil)
	long := strings.Repeat("we should check how the layout behaves on narrow screens today ", 60) + "\n```\nfunc main() {}\n```\n" +
		strings.Repeat("then compare the result with the desktop version and report back ", 55)
	message("m-n2", "c-new", "user", "message", long, "2026-10-20T13:00:00.000Z", nil)
	// A fork copies the root's history, then continues.
	message("m-fork-1", "c-fork", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(1, 0), nil)
	message("m-fork-2", "c-fork", "assistant", "message", "I will look into the export button.", at(1, 1), nil)
	message("m-fork-3", "c-fork", "user", "message", "continue", at(2, 1), nil)
	// A fork taken from an earlier point that lacks one of the parent's
	// records; the person then types what the parent later had.
	message("m-f2-1", "c-fork2", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(1, 0), nil)
	message("m-f2-2", "c-fork2", "user", "message", "?", at(1, 8), nil)
	message("m-f2-3", "c-fork2", "user", "message", "continue", at(2, 2), nil)
	// Without times, the copy is found in order.
	message("m-f3-1", "c-fork3", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(1, 0), nil)
	message("m-f3-2", "c-fork3", "user", "message", "Another idea entirely", at(2, 3), nil)
	// A catalog that stamps copies when it forks: found in order, too.
	message("m-f4-1", "c-fork4", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(2, 5), nil)
	message("m-f4-2", "c-fork4", "user", "message", "Fresh question here", at(2, 6), nil)
	// A copy whose first record differs from the parent's: later copies are
	// still marked as duplicates.
	message("m-f5-1", "c-fork5", "user", "message", "Regenerated instructions for this fork", at(2, 10), nil)
	message("m-f5-2", "c-fork5", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(2, 11), nil)
	message("m-f5-3", "c-fork5", "user", "message", "?", at(2, 12), nil)
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_id,call_message_id,result_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at)
		VALUES('t-ask','ws-other','c-other','call-3','m-ask','m-ask-result',1,'claude','tool','AskUserQuestion','user','ok',?,?)`, at(3, 1), at(3, 2))
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_id,call_message_id,result_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at)
		VALUES('t-ask2','ws-other','c-other','call-4','m-ask2','m-ask2-result',2,'claude','tool','AskUserQuestion','user','ok',?,?)`, at(3, 4), at(3, 5))
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_id,call_message_id,result_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at)
		VALUES('t-ask3','ws-other','c-other','call-6','m-ask3','m-ask3-result',3,'claude','tool','AskUserQuestion','user','ok',?,?)`, at(3, 5), at(3, 5))
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_id,call_message_id,result_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at)
		VALUES('t-c-ask','ws-main','c-child','call-5','m-c-ask','m-c-ask-result',1,'claude','tool','AskUserQuestion','user','ok',?,?)`, at(1, 6), at(1, 6))
	// The root's Task session names the delegation and the child it started.
	for _, s := range [][7]string{
		{"s-root", "c-root", "main", "root", "", "", ""},
		{"s-task", "c-root", "task-1", "subagent", "s-root", "m-del", "c-child"},
		{"s-child", "c-child", "main", "root", "", "", ""},
		{"s-embed", "c-root", "agent-2", "subagent", "s-root", "", ""},
	} {
		exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,parent_id,delegation_message_id,child_conversation_id,provider) VALUES(?,'ws-main',?,?,?,?,?,?,'claude')`,
			s[0], s[1], s[2], s[3], nilIfEmpty(s[4]), nilIfEmpty(s[5]), nilIfEmpty(s[6]))
	}
	exec(`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('s-embed','m-sub1',1),('s-embed','m-sub2',2)`)
	// Extraction is current where thinking was recorded; messages written
	// after would clear it.
	exec(`INSERT INTO reasoning_extraction_state(conversation_id,version,retained_count,retained_bytes,summary_count,unavailable_count) VALUES('c-root',?,1,29,0,1)`, reasoningExtractionVersion)
	commitSeed(t, tx)
}

func catalogFileSums(t *testing.T, path string) string {
	t.Helper()
	var sums []string
	for _, name := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		sums = append(sums, hashBytes(data))
	}
	return strings.Join(sums, "|")
}

func labelingRows(t *testing.T, db *sql.DB, query string, args ...any) []map[string]any {
	t.Helper()
	rows, err := queryMaps(db, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestLabelingPrepareBuildsTheDatasetWithoutWritingTheCatalog(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	// A structured submission must record the same one action as its tool
	// result, with source ranges in the original JSON message.
	choice := "<send_user_message_question_reply>\n" + `[{"questionItemId":"call-3","question":"Which mode?","answer":"Records"}]` + "\n</send_user_message_question_reply>"
	if _, err := catalog.DB.Exec(`UPDATE messages SET text=? WHERE id='m-o2'`, choice); err != nil {
		t.Fatal(err)
	}
	before := catalogFileSums(t, catalog.Path)
	dir := filepath.Join(t.TempDir(), "dataset")
	var progress bytes.Buffer
	opts := labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, FreshDays: 14, Progress: &progress, Now: func() time.Time { return labelingFixtureNow }}
	manifest, err := prepareLabelingDataset(context.Background(), opts, false)
	if err != nil {
		t.Fatal(err)
	}
	if after := catalogFileSums(t, catalog.Path); after != before {
		t.Fatal("preparation changed the catalog's files")
	}
	if !strings.Contains(progress.String(), "reading authorship inputs") {
		t.Fatalf("progress = %q", progress.String())
	}
	if _, err := os.Stat(filepath.Join(dir, labelingIncompleteFile)); !os.IsNotExist(err) {
		t.Fatal("a complete preparation left its incomplete marker")
	}
	published, err := os.ReadFile(filepath.Join(dir, labelingManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(published, &stored); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"population", "scope", "snapshot", "contracts", "counts", "cohorts", "exclusions", "duplicates", "splits", "reasoning", "authorship", "long", "invalid_utf8", "actions", "coverage"} {
		if stored[key] == nil {
			t.Errorf("manifest lacks %s", key)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, labelingDatasetFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("dataset mode: %v %v", info, err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, labelingDatasetFile)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	units := map[string]map[string]any{}
	for _, row := range labelingRows(t, db, `SELECT * FROM units`) {
		units[firstString(row["message_id"])] = row
	}
	want := map[string]string{
		"m-h1": "human", "m-a1": "agent_reply", "m-think": "thinking", "m-del": "delegation", "m-delres": "delegation",
		"m-h2": "human", "m-auto": "agent_in_user_role", "m-a2": "agent_reply", "m-c1": "agent_in_user_role", "m-c2": "agent_reply",
		"m-o1": "human", "m-n1": "human", "m-n2": "human", "m-o3": "agent_reply", "m-fork-3": "human", "m-h3": "human",
		"m-sub1": "agent_in_user_role", "m-sub2": "agent_reply", "m-o4": "human", "m-o5": "agent_reply", "m-o6": "human", "m-n3": "human",
		"m-d1": "agent_in_user_role", "m-d2": "agent_in_user_role", "m-f2-3": "human", "m-f3-2": "human", "m-f4-2": "human", "m-f5-1": "human", "m-f5-2": "human", "m-f5-3": "human", "m-o7": "human",
	}
	for message, cohort := range want {
		if units[message]["cohort"] != cohort {
			t.Errorf("%s: cohort %v, want %s", message, units[message]["cohort"], cohort)
		}
	}
	if len(units) != len(want) {
		t.Errorf("units for %d messages, want %d", len(units), len(want))
	}
	if visible := firstString(units["m-h1"]["visible_text"]); !strings.HasPrefix(visible, "Please investigate why") || strings.Contains(visible, "reminder") {
		t.Errorf("human visible text = %q", units["m-h1"]["visible_text"])
	}
	if units["m-o1"]["visible_text"] != units["m-h1"]["visible_text"] {
		t.Errorf("re-sent request = %q", units["m-o1"]["visible_text"])
	}
	// Depth comes from the usage lineage; children and their parents share a family.
	if integer(units["m-a1"]["depth"]) != 0 || integer(units["m-c2"]["depth"]) != 1 || units["m-c2"]["depth_status"] != "resolved" {
		t.Errorf("depths: root %v, child %v (%v)", units["m-a1"]["depth"], units["m-c2"]["depth"], units["m-c2"]["depth_status"])
	}
	if units["m-c2"]["root_conversation_id"] != "c-root" || units["m-c2"]["family_id"] != units["m-a1"]["family_id"] {
		t.Errorf("child lineage: %v", units["m-c2"])
	}
	// A copy links the later conversation into the same family; the recent one is fresh.
	if units["m-o1"]["family_id"] != units["m-h1"]["family_id"] || units["m-o1"]["split"] != units["m-h1"]["split"] {
		t.Errorf("copy family: %v vs %v", units["m-o1"]["family_id"], units["m-h1"]["family_id"])
	}
	if units["m-n1"]["split"] != "fresh" || units["m-h1"]["split"] == "fresh" {
		t.Errorf("splits: recent %v, old %v", units["m-n1"]["split"], units["m-h1"]["split"])
	}
	// The delegation and its result repeat the child's messages.
	if units["m-del"]["duplicate_of"] != "m-c1" || units["m-delres"]["duplicate_of"] != "m-c2" || units["m-delres"]["duplicate_relation"] != "delegation_result_contains_child_message" {
		t.Errorf("duplicates: %v, %v (%v)", units["m-del"]["duplicate_of"], units["m-delres"]["duplicate_of"], units["m-delres"]["duplicate_relation"])
	}
	if units["m-think"]["channel"] != "analysis" || units["m-a1"]["channel"] != "unspecified" {
		t.Errorf("channels: %v %v", units["m-think"]["channel"], units["m-a1"]["channel"])
	}
	// Context holds only earlier messages, built as measured.
	// The previous request may be an agent's; an invalid reply is left out.
	if context := firstString(units["m-a2"]["context_text"]); context != "Previous user request:\nSummarize the repository layout" {
		t.Errorf("context = %q", context)
	}
	if context := firstString(units["m-c2"]["context_text"]); context != "Previous user request:\nFind the CSS rule that hides the export button" {
		t.Errorf("sub-agent context = %q", context)
	}
	// A fork's copied history is excluded; the fork joins the root's family.
	if units["m-fork-3"]["family_id"] != units["m-h1"]["family_id"] {
		t.Errorf("fork family %v, root %v", units["m-fork-3"]["family_id"], units["m-h1"]["family_id"])
	}
	// Re-sent material keeps its source's attribution.
	if visible := firstString(units["m-n3"]["visible_text"]); !strings.HasPrefix(visible, "Here is the failure:") || !strings.Contains(visible, "re-sent text of unverified origin") {
		t.Errorf("re-sent log = %q", visible)
	}
	if unverified := labelingRows(t, db, `SELECT p.status FROM pieces p JOIN units u ON u.id=p.unit_id WHERE u.message_id='m-n3' AND p.status='reused_unverified' AND p.role='context'`); len(unverified) == 0 {
		t.Error("the re-sent log is not unverified context")
	}
	// Identical prompts do not link families; their items are flagged when
	// they span splits, and a held-out copy of a training text is seen_in_train.
	if units["m-d1"]["family_id"] == units["m-d2"]["family_id"] {
		t.Error("identical prompts were linked into one family")
	}
	daily := labelingRows(t, db, `SELECT i.target_hash,i.cross_split_duplicate,i.seen_in_train,u.split FROM items i JOIN units u ON u.id=i.unit_id
		WHERE u.message_id IN ('m-d1','m-d2') ORDER BY u.message_id`)
	if len(daily) != 2 || daily[0]["target_hash"] != daily[1]["target_hash"] {
		t.Fatalf("daily items = %v", daily)
	}
	differ := daily[0]["split"] != daily[1]["split"]
	for index, row := range daily {
		other := daily[1-index]
		seen := differ && row["split"] != "train" && other["split"] == "train"
		if (integer(row["cross_split_duplicate"]) == 1) != differ || (integer(row["seen_in_train"]) == 1) != seen {
			t.Errorf("duplicate flags %v (other %v)", row, other)
		}
	}
	// The sub-agent's exchange stays out of the main session's context.
	if firstString(units["m-sub2"]["context_text"]) != "Previous user request:\nCheck the CSS files for media queries that hide the export button on small screens please" {
		t.Errorf("sub-agent context = %q", units["m-sub2"]["context_text"])
	}
	// Mirrors share the root's family; a delegation shows its decoded prompt;
	// a re-sent sub-agent prompt is not the person's.
	families := map[string]string{}
	for _, row := range labelingRows(t, db, `SELECT conversation_id,family_id FROM families`) {
		families[firstString(row["conversation_id"])] = firstString(row["family_id"])
	}
	if families["c-mirror"] == "" || families["c-mirror"] != families["c-root"] {
		t.Errorf("mirror family %q, root %q", families["c-mirror"], families["c-root"])
	}
	if units["m-del"]["visible_text"] != "Find the CSS rule that hides the export button" {
		t.Errorf("delegation visible = %q", units["m-del"]["visible_text"])
	}
	if visible := firstString(units["m-o7"]["visible_text"]); !strings.Contains(visible, "re-sent text of unverified origin") || !strings.HasSuffix(visible, "thanks") {
		t.Errorf("re-sent sub-agent prompt = %q", visible)
	}
	// A short repeat ("?") is not taken for a copy.
	if units["m-f5-2"]["duplicate_of"] != "m-h1" || units["m-f5-3"]["duplicate_of"] != nil || units["m-f5-1"]["duplicate_of"] != nil {
		t.Errorf("later fork copies: %v %v %v", units["m-f5-1"]["duplicate_of"], units["m-f5-2"]["duplicate_of"], units["m-f5-3"]["duplicate_of"])
	}
	// The first new turn of a fork sees the copied request and reply.
	if context := firstString(units["m-fork-3"]["context_text"]); !strings.HasPrefix(context, "Previous user request:\nPlease investigate why") ||
		!strings.HasSuffix(context, "Previous assistant message:\nI will look into the export button.") {
		t.Errorf("fork context = %q", context)
	}
	// Secrets never reach visible text.
	if visible := firstString(units["m-o3"]["visible_text"]); strings.Contains(visible, "abcd1234") || !strings.Contains(visible, "⟦excluded: redacted secret") {
		t.Errorf("secret visible: %q", visible)
	}
	// A long human unit splits into items whose ranges cross pieces and a marker.
	if integer(units["m-n2"]["chunk_count"]) < 2 || !strings.Contains(firstString(units["m-n2"]["visible_text"]), "⟦likely pasted text, context only") {
		t.Errorf("long unit: %v chunks, %q", units["m-n2"]["chunk_count"], firstString(units["m-n2"]["visible_text"])[:80])
	}
	for _, row := range labelingRows(t, db, `SELECT i.source_ranges,u.message_text FROM items i JOIN units u ON u.id=i.unit_id WHERE u.message_id='m-n2'`) {
		var ranges []map[string]int
		if err := json.Unmarshal([]byte(firstString(row["source_ranges"])), &ranges); err != nil || len(ranges) == 0 {
			t.Fatalf("ranges %v: %v", row["source_ranges"], err)
		}
		for _, r := range ranges {
			if r["end"] <= r["start"] || r["end"] > len(firstString(row["message_text"])) {
				t.Errorf("range %v", r)
			}
		}
	}
	if sentences := labelingRows(t, db, `SELECT s.start_byte,s.end_byte,u.message_text FROM sentences s JOIN units u ON u.id=s.unit_id WHERE u.message_id='m-a2'`); len(sentences) != 1 ||
		firstString(sentences[0]["message_text"])[integer(sentences[0]["start_byte"]):integer(sentences[0]["end_byte"])] != "Done: the rule was removed." {
		t.Errorf("sentences = %v", sentences)
	}
	family := stored["splits"].(map[string]any)["family_sizes"].(map[string]any)
	if family["largest_units"] == nil || family["families_with_units"].(float64) < 2 || len(family["top10_unit_shares"].([]any)) == 0 {
		t.Errorf("family sizes = %v", family)
	}
	if context := firstString(units["m-h1"]["context_text"]); context != "" {
		t.Errorf("first message has context %q", context)
	}

	exclusions := map[string]string{}
	for _, row := range labelingRows(t, db, `SELECT message_id,reason FROM exclusions`) {
		exclusions[firstString(row["message_id"])] = firstString(row["reason"])
	}
	duplicates := map[string]string{}
	for _, row := range labelingRows(t, db, `SELECT message_id,COALESCE(duplicate_of,'') duplicate_of FROM exclusions`) {
		duplicates[firstString(row["message_id"])] = firstString(row["duplicate_of"])
	}
	if duplicates["m-fork-1"] != "m-h1" || duplicates["m-fork-2"] != "m-a1" || duplicates["m-f2-1"] != "m-h1" || duplicates["m-f2-2"] != "m-h2" ||
		duplicates["m-f3-1"] != "m-h1" || duplicates["m-f4-1"] != "m-h1" {
		t.Errorf("inherited history = %v", duplicates)
	}
	for message, reason := range map[string]string{"m-branch": "unselected_branch", "m-untimed": "authorship_missing", "m-o2": "action_only",
		"m-fork-1": "inherited_history", "m-fork-2": "inherited_history", "m-o-think": "reasoning_extraction_pending", "m-harness-only": "no_target_text",
		"m-bad": "invalid_utf8", "m-n4": "residual_punctuation"} {
		if exclusions[message] != reason {
			t.Errorf("%s excluded as %q, want %s", message, exclusions[message], reason)
		}
	}
	counted := map[string]int{}
	for _, row := range stored["exclusions"].([]any) {
		row := row.(map[string]any)
		counted[firstString(row["reason"])] += int(row["records"].(float64))
	}
	for reason, count := range map[string]int{"mirrored_work": 1, "reasoning_unavailable": 1, "kind_tool_call": 4, "kind_tool_result": 1,
		"tool_result_answer": 1, "tool_result_answer_duplicate": 1, "tool_result_answer_unverified": 1, "inherited_history": 6, "unselected_branch": 3} {
		if counted[reason] != count {
			t.Errorf("%s counted %d, want %d (%v)", reason, counted[reason], count, counted)
		}
	}
	actions := labelingRows(t, db, `SELECT * FROM actions ORDER BY message_id`)
	if len(actions) != 3 || actions[2]["message_id"] != "m-o2" || actions[2]["option"] != "Records" || actions[2]["question_message_id"] != "m-ask" ||
		actions[2]["actor"] != "human" || actions[0]["message_id"] != "m-ask2-result" || actions[0]["status"] != "tool_result_answer" || actions[0]["option"] != "Yes" ||
		actions[0]["actor"] != "human" || actions[0]["question_call_id"] != "call-4" || actions[0]["actor_evidence"] != "interactive_session_question_tool" ||
		actions[1]["actor_evidence"] != "delegated_or_unresolved_run" || actions[2]["actor_evidence"] != "flattened_question_form_message" ||
		actions[1]["message_id"] != "m-c-ask-result" || actions[1]["actor"] != "unknown" || actions[1]["status"] != "tool_result_answer_unverified" {
		t.Errorf("actions = %v", actions)
	}

	if len(actions) == 3 {
		action := actions[2]
		if choice[integer(action["title_start"]):integer(action["title_end"])] != "Which mode?" ||
			choice[integer(action["answer_start"]):integer(action["answer_end"])] != "Records" {
			t.Errorf("structured action ranges = %v", action)
		}
	}

	// Every visible piece slices the stored message exactly, and items
	// concatenate to the visible text.
	for _, row := range labelingRows(t, db, `SELECT u.message_text,u.visible_text,p.start_byte,p.end_byte,p.visible_start,p.visible_end FROM pieces p
		JOIN units u ON u.id=p.unit_id WHERE p.visible_start IS NOT NULL AND p.transform IS NULL`) {
		text, visible := firstString(row["message_text"]), row["visible_text"].(string)
		if text[integer(row["start_byte"]):integer(row["end_byte"])] != visible[integer(row["visible_start"]):integer(row["visible_end"])] {
			t.Errorf("piece %v does not slice its message", row)
		}
	}
	for _, row := range labelingRows(t, db, `SELECT u.visible_text,group_concat(i.text,'') joined FROM units u JOIN (SELECT * FROM items ORDER BY chunk_index) i
		ON i.unit_id=u.id GROUP BY u.id`) {
		if row["visible_text"] != row["joined"] {
			t.Errorf("items lose text: %v", row)
		}
	}

	// A second preparation needs --replace; replacing keeps the same units.
	if _, err := prepareLabelingDataset(context.Background(), opts, false); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("prepared into a non-empty directory: %v", err)
	}
	again, err := prepareLabelingDataset(context.Background(), opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(again["counts"]) != fmt.Sprint(manifest["counts"]) || fmt.Sprint(again["splits"]) != fmt.Sprint(manifest["splits"]) {
		t.Fatal("a repeated preparation differs")
	}
	if after := catalogFileSums(t, catalog.Path); after != before {
		t.Fatal("preparation changed the catalog's files")
	}
}

func TestLabelingPrepareDryRunAndInterruption(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	dir := filepath.Join(t.TempDir(), "dataset")
	manifest, err := prepareLabeling(context.Background(), labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, FreshDays: 14, DryRun: true,
		Conversations: []string{"c-root", "c-mirror"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a dry run created the dataset directory")
	}
	scope := manifest["scope"].(map[string]any)
	if scope["conversations"] != 2 || scope["mirrored_conversations"] != 1 || !strings.HasPrefix(manifest["population"].(string), "A restricted sample") {
		t.Fatalf("restricted scope = %v; %v", scope, manifest["population"])
	}
	if manifest["cohorts"].(map[string]int)["human"] != 3 {
		t.Fatalf("cohorts = %v", manifest["cohorts"])
	}
	// The child is outside the sample, but its duplicates are still found.
	if duplicates := manifest["duplicates"].(map[string]any); fmt.Sprint(duplicates["delegation"]) != "map[equal:1 linked:1 units:1]" ||
		fmt.Sprint(duplicates["delegation_result"]) != "map[contains:1 linked:1 units:1]" {
		t.Fatalf("duplicates = %v", duplicates)
	}
	// A repeated --conversation is prepared once.
	if again, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, Live: true, DryRun: true,
		Conversations: []string{"c-root", "c-root"}}); err != nil || again["scope"].(map[string]any)["conversations"] != 1 {
		t.Fatalf("repeated conversation: %v", err)
	}
	if manifest, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, Live: true, DryRun: true, Limit: 2}); err != nil ||
		manifest["scope"].(map[string]any)["conversations"] != 2 || manifest["scope"].(map[string]any)["mirrored_conversations"] != 0 {
		t.Fatalf("limited sample = %v, %v", manifest["scope"], err)
	}

	// An interrupted preparation leaves no manifest, and says so.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareLabelingDataset(ctx, labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true}, false); err == nil {
		t.Fatal("a cancelled preparation succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, labelingManifestFile)); !os.IsNotExist(err) {
		t.Fatal("an interrupted preparation published a manifest")
	}
	var status bytes.Buffer
	if err := labelingStatus(dir, &status); err != nil || !strings.HasPrefix(status.String(), "Preparation incomplete") || !strings.Contains(status.String(), `"interrupted"`) {
		t.Fatalf("status = %q, %v", status.String(), err)
	}

	// A catalog newer than this build, or older than the tool ledger, is refused.
	for _, version := range []string{"22", "6"} {
		if _, err := catalog.DB.Exec(`UPDATE meta SET value=? WHERE key='schema_version'`, version); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, Live: true, DryRun: true}); err == nil || !strings.Contains(err.Error(), "schema") {
			t.Fatalf("schema %s = %v", version, err)
		}
	}
}

func TestLabelingPlanReadsOnlyTheDataset(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	dir := filepath.Join(t.TempDir(), "dataset")
	if _, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, FreshDays: 14}, false); err != nil {
		t.Fatal(err)
	}
	// Model CLIs on PATH record any start; the plan must start none.
	bin := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	for _, name := range []string{"claude", "codex", "agy"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho "+name+" >> "+started+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	dataset := filepath.Join(dir, labelingDatasetFile)
	listing := func() string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return strings.Join(names, ",")
	}
	files := listing()
	if strings.Contains(files, "-wal") || strings.Contains(files, "-shm") {
		t.Fatalf("a finished dataset keeps journal files: %s", files)
	}
	before, err := os.ReadFile(dataset)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	plan, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "claude", Model: "claude-haiku-5-5", Effort: "high",
		ItemsPerRequest: 4, MaxRequestTokens: 24000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil {
		t.Fatal(err)
	}
	// The seeded conversations share few families; one item per family
	// and request takes more requests than packing four at a time.
	packing := plan["packing"].(map[string]any)
	if plan["items"] != 29 || plan["requests"] != 24 || packing["requests_without_family_separation"] != 8 || packing["added_requests"] != 16 ||
		plan["selection"].(map[string]any)["duplicate_items_skipped"] != 3 {
		t.Fatalf("plan = %v items in %v requests", plan["items"], plan["requests"])
	}
	price := plan["price"].(map[string]any)
	if price["status"] != "priced" || price["cost_usd"].(float64) <= 0 {
		t.Fatalf("price = %v", price)
	}
	luna, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "codex", Model: "gpt-6-luna", Effort: "medium", Cohorts: []string{"human"},
		ItemsPerRequest: 4, MaxRequestTokens: 24000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil || luna["items"] != 17 || luna["price"].(map[string]any)["status"] != "priced" {
		t.Fatalf("luna plan = %v, %v", luna, err)
	}
	unknown, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "codex", Model: "made-up-model", Effort: "high",
		ItemsPerRequest: 4, MaxRequestTokens: 24000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil || unknown["price"].(map[string]any)["status"] != "unpriced" || unknown["price"].(map[string]any)["cost_usd"] != nil {
		t.Fatalf("unpriced plan = %v, %v", unknown["price"], err)
	}
	// An alias or a dated snapshot name resolves in the price book, but run
	// prices the labeling model under its exact name only and refuses it:
	// the plan shows it unpriced too.
	for _, model := range []string{"haiku", "claude-haiku-5-5-20261007"} {
		aliased, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "claude", Model: model, Effort: "high",
			ItemsPerRequest: 4, MaxRequestTokens: 24000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
		book, _ := embeddedPriceBook(context.Background(), today)
		if err != nil || aliased["price"].(map[string]any)["status"] != "unpriced" || aliased["price"].(map[string]any)["cost_usd"] != nil ||
			labelingRequestEstimate(book, model, "2026-10-27", 1000, 1000) != nil {
			t.Fatalf("%s plan = %v, %v", model, aliased["price"], err)
		}
	}
	// Each route's calibration was measured at its own effort; only another
	// effort is flagged.
	for _, c := range []struct {
		route, model, effort, warning string
	}{{"claude", "claude-haiku-5-5", "high", ""}, {"claude", "claude-haiku-5-5", "medium", "at effort high"}, {"codex", "gpt-6-luna", "high", ""},
		{"agy", "gemini-3.8-flash", "low", ""}, {"agy", "gemini-3.8-flash", "high", "at effort low"}} {
		planned, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: c.route, Model: c.model, Effort: c.effort,
			ItemsPerRequest: 4, MaxRequestTokens: 24000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
		if err != nil {
			t.Fatal(err)
		}
		assumptions := strings.Join(planned["assumptions"].([]string), "\n")
		if flagged := strings.Contains(assumptions, "was not calibrated"); flagged != (c.warning != "") || !strings.Contains(assumptions, c.warning) {
			t.Fatalf("%s at %s: %s", c.route, c.effort, assumptions)
		}
	}
	all, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "claude", Model: "claude-haiku-5-5", Effort: "high", IncludeDuplicates: true,
		ItemsPerRequest: 4, MaxRequestTokens: 24000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil || all["items"] != 32 {
		t.Fatalf("with duplicates = %v items, %v", all["items"], err)
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatal("planning started a model CLI")
	}
	if after, err := os.ReadFile(dataset); err != nil || !bytes.Equal(after, before) || listing() != files {
		t.Fatal("planning changed the dataset directory")
	}
}

func TestLabelingSnapshotRefusesWrites(t *testing.T) {
	catalog, _ := testCatalog(t)
	db, tx, _, err := openLabelingSnapshot(context.Background(), catalog.Path, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('labeling-test','x')`); err == nil {
		t.Fatal("the snapshot connection wrote the catalog")
	}
}

func TestLabelingLineageUsesDerivedLaunches(t *testing.T) {
	catalog, _ := testCatalog(t)
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','claude','local','w','w','2026-10-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c-parent','w','claude','local','p'),('c-run','w','claude','local','r')`)
	ledger := []map[string]any{{"workspace_id": "w"}}
	stored, err := usageConversationRootsWith(catalog.DB, ledger, nil)
	if err != nil || firstString(stored.roots["c-run"]["id"]) != "c-run" {
		t.Fatalf("without a launch, the run is its own root: %v", err)
	}
	derived, err := usageConversationRootsWith(catalog.DB, ledger, []map[string]any{{"child_id": "c-run", "parent_id": "c-parent"}})
	if err != nil || firstString(derived.roots["c-run"]["id"]) != "c-parent" {
		t.Fatalf("a derived launch does not reach lineage: %v", derived.roots["c-run"])
	}
}

func TestLabelingSplitsFromAnEarlierDataset(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	first := filepath.Join(t.TempDir(), "first")
	now := func() time.Time { return labelingFixtureNow }
	if _, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: first, CatalogPath: catalog.Path, Live: true, FreshDays: 14, Now: now}, false); err != nil {
		t.Fatal(err)
	}
	// Without the earlier splits, a zero-day window would make nothing fresh.
	manifest, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, Live: true, DryRun: true, FreshDays: 0, Now: now, SplitsFrom: first})
	if err != nil {
		t.Fatal(err)
	}
	splits := manifest["splits"].(map[string]any)
	coverage := splits["coverage"].(map[string]map[string]map[string]int)
	if coverage["cohort"]["human"]["fresh"] == 0 || splits["assignment"].(map[string]int)["inherited_conversations"] == 0 {
		t.Fatalf("splits = %v", splits)
	}
	// Linked work the earlier dataset split apart is quarantined.
	db, err := sql.Open("sqlite", filepath.Join(first, labelingDatasetFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE families SET split=CASE WHEN conversation_id='c-fork' THEN 'test' ELSE 'train' END WHERE conversation_id IN ('c-root','c-fork')`); err != nil {
		t.Fatal(err)
	}
	// The identical daily prompt, once in train and once in test.
	if _, err := db.Exec(`UPDATE families SET split=CASE WHEN conversation_id='c-daily1' THEN 'train' ELSE 'test' END WHERE conversation_id IN ('c-daily1','c-daily2')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	manifest, err = prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, Live: true, DryRun: true, FreshDays: 14, Now: now, SplitsFrom: first})
	if err != nil {
		t.Fatal(err)
	}
	splits = manifest["splits"].(map[string]any)
	if seen := splits["cross_split_duplicates"].(map[string]any)["seen_in_train"].(map[string]int); seen["test"] != 1 {
		t.Fatalf("seen in train = %v", seen)
	}
	if splits["assignment"].(map[string]int)["quarantined_conversations"] == 0 || splits["coverage"].(map[string]map[string]map[string]int)["cohort"]["human"]["quarantine"] == 0 {
		t.Fatalf("conflicting splits = %v", splits)
	}
}

func TestLabelingPreparesAnOlderSnapshotCopy(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	// Make it look like schema 14: no reasoning store, no senders, no harness.
	// Triggers name the dropped columns; a copy is never written, so they go too.
	statements := []string{}
	for _, row := range labelingRows(t, catalog.DB, `SELECT name FROM sqlite_master WHERE type='trigger'`) {
		statements = append(statements, `DROP TRIGGER "`+firstString(row["name"])+`"`)
	}
	execTx(t, catalog.DB, statements...)
	execTx(t, catalog.DB, `DROP TABLE reasoning_extraction_state`, `ALTER TABLE messages DROP COLUMN sender`, `DROP INDEX conversations_usage_idx`, `ALTER TABLE conversations DROP COLUMN harness`,
		`UPDATE meta SET value='14' WHERE key='schema_version'`)
	if _, err := catalog.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "catalog.sqlite3")
	if err := os.WriteFile(backup, data, 0o444); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: backup, SnapshotCopy: true, DryRun: true, FreshDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	coverage := strings.Join(manifest["coverage"].([]string), "\n")
	for _, want := range []string{"thinking: unavailable from this snapshot; catalog schema 14", "messages.sender is absent", "conversations.harness is absent"} {
		if !strings.Contains(coverage, want) {
			t.Errorf("coverage lacks %q:\n%s", want, coverage)
		}
	}
	excluded := map[string]int{}
	for _, row := range manifest["exclusions"].([]map[string]any) {
		excluded[row["reason"].(string)] += row["records"].(int)
	}
	if excluded["reasoning_store_absent"] != 2 || manifest["cohorts"].(map[string]int)["human"] == 0 {
		t.Fatalf("exclusions %v, cohorts %v", excluded, manifest["cohorts"])
	}
	access := manifest["snapshot"].(map[string]any)["access"].(map[string]any)
	if access["mode"] != "snapshot_copy" || access["bytes"] != int64(len(data)) {
		t.Fatalf("access = %v", access)
	}
	// Immutable: the copy is unchanged and gets no shared-memory or WAL file.
	if after, err := os.ReadFile(backup); err != nil || !bytes.Equal(after, data) {
		t.Fatal("the copy changed")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(backup + suffix); !os.IsNotExist(err) {
			t.Fatalf("reading the copy created %s", suffix)
		}
	}
	// A non-empty -shm may mean something has the file open.
	if err := os.WriteFile(backup+"-shm", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: backup, SnapshotCopy: true, DryRun: true}); err == nil || !strings.Contains(err.Error(), "-shm") {
		t.Fatalf("copy with a -shm = %v", err)
	}
	if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: backup, SnapshotCopy: true, AllowStaleShm: true, DryRun: true}); err != nil {
		t.Fatalf("stale -shm allowed = %v", err)
	}
	// A library's catalog folder, with Pharos's lock files, is never read.
	if err := os.WriteFile(filepath.Join(filepath.Dir(backup), ".pharos-use.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: backup, SnapshotCopy: true, AllowStaleShm: true, DryRun: true}); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("catalog beside a lock = %v", err)
	}
	os.Remove(filepath.Join(filepath.Dir(backup), ".pharos-use.lock"))
	os.Remove(backup + "-shm")
	// A copy with a non-empty WAL is not complete.
	if err := os.WriteFile(backup+"-wal", []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: backup, SnapshotCopy: true, DryRun: true}); err == nil || !strings.Contains(err.Error(), "write-ahead log") {
		t.Fatalf("copy with a WAL = %v", err)
	}
}

func TestLabelingReplaceKeepsPaidWork(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	dir := filepath.Join(t.TempDir(), "dataset")
	opts := labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, Conversations: []string{"c-other"}}
	if _, err := prepareLabelingDataset(context.Background(), opts, false); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, labelingDatasetFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE attempts(id TEXT); INSERT INTO attempts VALUES('a1')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := prepareLabelingDataset(context.Background(), opts, true); err == nil || !strings.Contains(err.Error(), "attempts") {
		t.Fatalf("replaced a dataset with attempts: %v", err)
	}
}

func TestLabelingPrepareChoosesItsSourceExplicitly(t *testing.T) {
	config := testConfig(t)
	catalog := openTestCatalog(t, config.CatalogPath)
	path := filepath.Join(t.TempDir(), "archive.toml")
	if err := os.WriteFile(path, []byte("catalog_path = \""+catalog.Path+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		var out, errs bytes.Buffer
		return runLabelingCLI(path, append([]string{"prepare", "--dry-run"}, args...), &out, &errs)
	}
	if err := run("--catalog", catalog.Path); err == nil || !strings.Contains(err.Error(), "--snapshot-copy") {
		t.Fatalf("no source flag = %v", err)
	}
	if err := run("--live"); err == nil {
		t.Fatal("--live is accepted")
	}
	if err := run("--snapshot-copy"); err == nil || !strings.Contains(err.Error(), "--catalog") {
		t.Fatalf("snapshot copy without a path = %v", err)
	}
	if err := run("--snapshot-copy", "--catalog", catalog.Path); err == nil || !strings.Contains(err.Error(), "live catalog") {
		t.Fatalf("the configured catalog as a copy = %v", err)
	}
	if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, DryRun: true}); err == nil {
		t.Fatal("prepared without choosing a copy or the live catalog")
	}
}

func TestLabelingPrepareFromACurrentCopyMatchesTheCatalog(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	now := func() time.Time { return labelingFixtureNow }
	live := filepath.Join(t.TempDir(), "live")
	if _, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: live, CatalogPath: catalog.Path, Live: true, FreshDays: 14, Now: now}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "catalog.sqlite3")
	if err := os.WriteFile(backup, data, 0o444); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "copy")
	manifest, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: copied, CatalogPath: backup, SnapshotCopy: true, FreshDays: 14, Now: now}, false)
	if err != nil {
		t.Fatal(err)
	}
	if manifest["snapshot"].(map[string]any)["access"].(map[string]any)["mode"] != "snapshot_copy" {
		t.Fatalf("access = %v", manifest["snapshot"])
	}
	read := func(dir string) string {
		db, err := sql.Open("sqlite", filepath.Join(dir, labelingDatasetFile)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var out strings.Builder
		for _, query := range []string{`SELECT message_id,cohort,visible_text,context_text,split,duplicate_of FROM units ORDER BY message_id`,
			`SELECT id,text,source_ranges,target_hash,cross_split_duplicate,seen_in_train FROM items ORDER BY id`,
			`SELECT message_id,reason FROM exclusions ORDER BY message_id`, `SELECT message_id,position,option,actor FROM actions ORDER BY message_id,position`} {
			out.WriteString(fmt.Sprint(labelingRows(t, db, query)))
		}
		return out.String()
	}
	if read(live) != read(copied) {
		t.Fatal("preparing from a copy differs from preparing from the catalog")
	}
}

// A known harness block after an unrecognized one is dropped: the item's
// text, its target hash and the next unit's context hold only what was
// typed.
func TestLabelingDropsHarnessAfterAnUnrecognizedTag(t *testing.T) {
	catalog, _ := testCatalog(t)
	typed := "Make the header sticky on narrow screens"
	tx := seedTx(t, catalog.DB)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('ws-a','claude','local','ws-a','ws-a','2026-10-01'),('ws-b','claude','local','ws-b','ws-b','2026-10-01')`, nil},
		{`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,started_at) VALUES('c-a','ws-a','claude','local','c-a',0,'2026-09-01T12:00:00.000Z'),
			('c-b','ws-b','claude','local','c-b',0,'2026-09-02T12:00:00.000Z')`, nil},
	} {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for index, m := range [][4]string{
		{"m-1", "c-a", "user", "<in-app-browser-context source=\"ambient-ui-state\">\nThe browser shows /settings\n</in-app-browser-context>\n<system-reminder>\nPRIVATE HARNESS\n</system-reminder>\n" + typed},
		{"m-2", "c-a", "assistant", "I made the header sticky."},
		{"m-3", "c-a", "user", "Now check it on tablets as well"},
		{"m-4", "c-b", "user", typed},
	} {
		if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order) VALUES(?,?,?,?,'message',?,?,?,1,?)`,
			m[0], m[1], m[0], m[2], m[3], "hash-"+m[0], fmt.Sprintf("2026-09-0%dT12:%02d:00.000Z", 1+index/3, index), index+1); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	dir := filepath.Join(t.TempDir(), "dataset")
	if _, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, FreshDays: 14,
		Now: func() time.Time { return labelingFixtureNow }}, false); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, labelingDatasetFile)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items := map[string]map[string]any{}
	for _, row := range labelingRows(t, db, `SELECT u.message_id,i.text,i.target_hash,u.context_text FROM items i JOIN units u ON u.id=i.unit_id`) {
		items[firstString(row["message_id"])] = row
	}
	first, next, plain := items["m-1"], items["m-3"], items["m-4"]
	if first == nil || next == nil || plain == nil {
		t.Fatalf("items = %v", items)
	}
	if first["text"] != typed || first["target_hash"] != plain["target_hash"] {
		t.Fatalf("item %q (hash %v), typed alone hashes %v", first["text"], first["target_hash"], plain["target_hash"])
	}
	context := firstString(next["context_text"])
	if !strings.Contains(context, "Previous user request:\n"+typed) || strings.Contains(context, "PRIVATE HARNESS") || strings.Contains(context, "/settings") {
		t.Fatalf("next unit's context = %q", context)
	}
}
