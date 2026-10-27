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
	request := "Please investigate why the export button disappears on mobile when the sidebar is collapsed"
	tx := seedTx(t, catalog.DB)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	for _, w := range [][2]string{{"ws-main", "claude"}, {"ws-mirror", "conductor"}, {"ws-other", "claude"}, {"ws-new", "codex"}} {
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
	message("m-h1", "c-root", "user", "message", "<system-reminder>\nBe brief.\n</system-reminder>\n"+request, at(1, 0), map[string]any{"sender": "account:me"})
	message("m-a1", "c-root", "assistant", "message", "I will look into the export button.", at(1, 1), nil)
	message("m-think", "c-root", "assistant", "thinking", "Maybe a media query hides it.", at(1, 2), nil)
	message("m-unavailable", "c-root", "assistant", "metadata", `{"reason":"encrypted","type":"reasoning_unavailable"}`, at(1, 2), nil)
	message("m-del", "c-root", "assistant", "delegation", `{"tool":"Task","input":{"prompt":"Find the CSS rule that hides the export button"}}`, at(1, 3), map[string]any{"call": "call-1"})
	message("m-tool", "c-root", "assistant", "tool_call", `{"tool":"Bash","input":{"command":"ls"}}`, at(1, 4), map[string]any{"call": "call-2"})
	message("m-tool-result", "c-root", "tool", "tool_result", `{"content":"ok","tool":"Bash"}`, at(1, 4), map[string]any{"call": "call-2"})
	message("m-delres", "c-root", "tool", "delegation_result", `{"content":"The rule is in mobile.css.","tool":"Task"}`, at(1, 6), map[string]any{"call": "call-1"})
	message("m-branch", "c-root", "user", "message", "An abandoned branch", at(1, 7), map[string]any{"selected": 0})
	message("m-h2", "c-root", "user", "message", "?", at(1, 8), nil)
	message("m-auto", "c-root", "user", "message", "Summarize the repository layout", at(1, 9), map[string]any{"sender": "automation:codex-exec"})
	message("m-untimed", "c-root", "user", "message", "A message without a send time", "", nil)
	message("m-a2", "c-root", "assistant", "message", "Done: the rule was removed.", at(1, 10), nil)
	message("m-c1", "c-child", "user", "message", "Find the CSS rule that hides the export button", at(1, 5), nil)
	message("m-c2", "c-child", "assistant", "message", "The rule is in mobile.css.", at(1, 6), nil)
	message("m-mirror", "c-mirror", "user", "message", request, at(1, 0), nil)
	message("m-o1", "c-other", "user", "message", request, at(3, 0), nil)
	message("m-ask", "c-other", "assistant", "tool_call", `{"tool":"AskUserQuestion","input":{"questions":[{"question":"Which mode?","options":[{"label":"Records"},{"label":"Text"}]}]}}`, at(3, 1), map[string]any{"call": "call-3"})
	message("m-ask-result", "c-other", "tool", "tool_result", `{"content":"Records","tool":"AskUserQuestion"}`, at(3, 2), map[string]any{"call": "call-3"})
	message("m-o2", "c-other", "user", "message", "Which mode?\nRecords", at(3, 3), nil)
	message("m-n1", "c-new", "user", "message", "Add a dark mode toggle to settings", "2026-10-20T12:00:00.000Z", nil)
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_id,call_message_id,result_message_id,sequence,provider,kind,tool_name,tool_category,status,started_at,ended_at)
		VALUES('t-ask','ws-other','c-other','call-3','m-ask','m-ask-result',1,'claude','tool','AskUserQuestion','user','ok',?,?)`, at(3, 1), at(3, 2))
	// The root's Task session names the delegation and the child it started.
	for _, s := range [][7]string{
		{"s-root", "c-root", "main", "root", "", "", ""},
		{"s-task", "c-root", "task-1", "subagent", "s-root", "m-del", "c-child"},
		{"s-child", "c-child", "main", "root", "", "", ""},
	} {
		exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,parent_id,delegation_message_id,child_conversation_id,provider) VALUES(?,'ws-main',?,?,?,?,?,?,'claude')`,
			s[0], s[1], s[2], s[3], nilIfEmpty(s[4]), nilIfEmpty(s[5]), nilIfEmpty(s[6]))
	}
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
	before := catalogFileSums(t, catalog.Path)
	dir := filepath.Join(t.TempDir(), "dataset")
	var progress bytes.Buffer
	opts := labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, FreshDays: 14, Progress: &progress, Now: func() time.Time { return labelingFixtureNow }}
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
	for _, key := range []string{"population", "scope", "snapshot", "contracts", "counts", "cohorts", "exclusions", "duplicates", "splits", "reasoning", "authorship", "long", "invalid_utf8", "actions"} {
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
		"m-o1": "human", "m-n1": "human",
	}
	for message, cohort := range want {
		if units[message]["cohort"] != cohort {
			t.Errorf("%s: cohort %v, want %s", message, units[message]["cohort"], cohort)
		}
	}
	if len(units) != len(want) {
		t.Errorf("units for %d messages, want %d", len(units), len(want))
	}
	if units["m-h1"]["visible_text"] != "Please investigate why the export button disappears on mobile when the sidebar is collapsed" {
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
	if units["m-del"]["duplicate_of"] != "m-c1" || units["m-delres"]["duplicate_of"] != "m-c2" {
		t.Errorf("duplicates: %v, %v", units["m-del"]["duplicate_of"], units["m-delres"]["duplicate_of"])
	}
	if units["m-think"]["channel"] != "analysis" || units["m-a1"]["channel"] != "unspecified" {
		t.Errorf("channels: %v %v", units["m-think"]["channel"], units["m-a1"]["channel"])
	}
	// Context holds only earlier messages, built as measured.
	if context := firstString(units["m-a2"]["context_text"]); context != "Previous user request:\n?\nPrevious assistant message:\nI will look into the export button." {
		t.Errorf("context = %q", context)
	}
	if context := firstString(units["m-h1"]["context_text"]); context != "" {
		t.Errorf("first message has context %q", context)
	}

	exclusions := map[string]string{}
	for _, row := range labelingRows(t, db, `SELECT message_id,reason FROM exclusions`) {
		exclusions[firstString(row["message_id"])] = firstString(row["reason"])
	}
	for message, reason := range map[string]string{"m-branch": "unselected_branch", "m-untimed": "authorship_missing", "m-o2": "action_only"} {
		if exclusions[message] != reason {
			t.Errorf("%s excluded as %q, want %s", message, exclusions[message], reason)
		}
	}
	counted := map[string]int{}
	for _, row := range stored["exclusions"].([]any) {
		row := row.(map[string]any)
		counted[firstString(row["reason"])] += int(row["records"].(float64))
	}
	for reason, count := range map[string]int{"mirrored_work": 1, "reasoning_unavailable": 1, "kind_tool_call": 2, "kind_tool_result": 1, "human_answer_in_tool_result": 1} {
		if counted[reason] != count {
			t.Errorf("%s counted %d, want %d (%v)", reason, counted[reason], count, counted)
		}
	}
	actions := labelingRows(t, db, `SELECT * FROM actions`)
	if len(actions) != 1 || actions[0]["message_id"] != "m-o2" || actions[0]["option"] != "Records" || actions[0]["question_message_id"] != "m-ask" {
		t.Errorf("actions = %v", actions)
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
	manifest, err := prepareLabeling(context.Background(), labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, FreshDays: 14, DryRun: true,
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
	if manifest["cohorts"].(map[string]int)["human"] != 2 {
		t.Fatalf("cohorts = %v", manifest["cohorts"])
	}
	// The child is outside the sample, but its duplicates are still found.
	if duplicates := manifest["duplicates"].(map[string]any); duplicates["delegation"] != 1 || duplicates["delegation_result"] != 1 {
		t.Fatalf("duplicates = %v", duplicates)
	}
	if manifest, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, DryRun: true, Limit: 2}); err != nil ||
		manifest["scope"].(map[string]any)["conversations"] != 2 || manifest["scope"].(map[string]any)["mirrored_conversations"] != 0 {
		t.Fatalf("limited sample = %v, %v", manifest["scope"], err)
	}

	// An interrupted preparation leaves no manifest, and says so.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareLabelingDataset(ctx, labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path}, false); err == nil {
		t.Fatal("a cancelled preparation succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, labelingManifestFile)); !os.IsNotExist(err) {
		t.Fatal("an interrupted preparation published a manifest")
	}
	var status bytes.Buffer
	if err := labelingStatus(dir, &status); err != nil || !strings.HasPrefix(status.String(), "Preparation incomplete") || !strings.Contains(status.String(), `"interrupted"`) {
		t.Fatalf("status = %q, %v", status.String(), err)
	}

	// A catalog of another schema is refused before anything is read.
	if _, err := catalog.DB.Exec(`UPDATE meta SET value='20' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLabeling(context.Background(), labelingPrepareOptions{CatalogPath: catalog.Path, DryRun: true}); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("schema 20 = %v", err)
	}
}

func TestLabelingPlanReadsOnlyTheDataset(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	dir := filepath.Join(t.TempDir(), "dataset")
	if _, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, FreshDays: 14}, false); err != nil {
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
	before, err := os.ReadFile(dataset)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	plan, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "claude", Model: "claude-haiku-5-5", Effort: "high",
		ItemsPerRequest: 4, MaxRequestChars: 12000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil {
		t.Fatal(err)
	}
	if plan["items"] != 12 || plan["requests"] != 3 {
		t.Fatalf("plan = %v items in %v requests", plan["items"], plan["requests"])
	}
	price := plan["price"].(map[string]any)
	if price["status"] != "priced" || price["cost_usd"].(float64) <= 0 {
		t.Fatalf("price = %v", price)
	}
	luna, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "codex", Model: "gpt-6-luna", Effort: "medium", Cohorts: []string{"human"},
		ItemsPerRequest: 4, MaxRequestChars: 12000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil || luna["items"] != 4 || luna["price"].(map[string]any)["status"] != "priced" {
		t.Fatalf("luna plan = %v, %v", luna, err)
	}
	unknown, err := planLabeling(context.Background(), labelingPlanOptions{Dir: dir, Route: "codex", Model: "made-up-model", Effort: "high",
		ItemsPerRequest: 4, MaxRequestChars: 12000, Concurrency: 3, CharsPerToken: labelingCharsPerToken, TokensPerMinute: 2_000_000, Today: today})
	if err != nil || unknown["price"].(map[string]any)["status"] != "unpriced" || unknown["price"].(map[string]any)["cost_usd"] != nil {
		t.Fatalf("unpriced plan = %v, %v", unknown["price"], err)
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatal("planning started a model CLI")
	}
	if after, err := os.ReadFile(dataset); err != nil || !bytes.Equal(after, before) {
		t.Fatal("planning changed the dataset")
	}
}
