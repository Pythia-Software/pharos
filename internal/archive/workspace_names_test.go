package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

func namingRecord(kind, id, title, text string, aliases ...string) WorkspaceRecord {
	return WorkspaceRecord{SourceKind: kind, SourceID: id, Account: "local", Title: title, Conversations: []ConversationRecord{{
		NativeID: kind + ":" + id, Provider: "codex", Account: "local", Harness: "codex", Aliases: aliases,
		Messages: []MessageRecord{{NativeID: "prompt", Role: "user", Kind: "message", Text: text, CreatedAt: "2026-09-01T12:00:00Z", Selected: true}},
	}}}
}

func ingestNamingRecord(t *testing.T, c *Catalog, record WorkspaceRecord) string {
	t.Helper()
	if err := c.writeTransaction(t.Context(), "naming fixture", func(tx *sql.Tx) error {
		_, _, err := ingestWorkspace(tx, record, false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return stableID("workspace", record.SourceKind, record.Account, record.SourceID)
}

func cachedWorkTitle(t *testing.T, c *Catalog, id string) string {
	t.Helper()
	var title string
	if err := c.DB.QueryRow("SELECT title FROM workspaces WHERE id=?", id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return title
}

func TestWorkspaceNamesPreferSpecificSourcesAndPreserveRenames(t *testing.T) {
	c, _ := testCatalog(t)
	native := namingRecord("codex", "native", "Codex native", "Fix login timeouts")
	id := ingestNamingRecord(t, c, native)
	task := namingRecord("tl1", "task", "Improve authentication", "", "codex:native")
	ingestNamingRecord(t, c, task)
	if got := cachedWorkTitle(t, c, id); got != task.Title {
		t.Fatalf("task fallback = %q", got)
	}
	app := namingRecord("conductor", "app", "Fix login timeout", "Fix login timeouts", "codex:native")
	ingestNamingRecord(t, c, app)
	if got := cachedWorkTitle(t, c, id); got != app.Title {
		t.Fatalf("specific session fallback = %q", got)
	}
	app.Title = "Repair expired login sessions"
	ingestNamingRecord(t, c, app)
	if got := cachedWorkTitle(t, c, id); got != app.Title {
		t.Fatalf("source rename = %q", got)
	}
	native.Title = "Correct cache expiry"
	before, _, _ := c.authorshipGenerations()
	ingestNamingRecord(t, c, native)
	if after, _, _ := c.authorshipGenerations(); after != before {
		t.Fatalf("title-only edit invalidated expensive authorship: %q -> %q", before, after)
	}
	if got := cachedWorkTitle(t, c, id); got != native.Title {
		t.Fatalf("native name must win = %q", got)
	}
	for _, conversation := range []ConversationRecord{native.Conversations[0], app.Conversations[0]} {
		cid := stableID("conversation", conversation.Provider, conversation.Account, conversation.NativeID)
		if got := conversationDisplayName(t, c, cid); got.Text != native.Title {
			t.Fatalf("native/app mirror selected a different primary name: %+v", got)
		}
	}
	app.Title = "Different session name"
	ingestNamingRecord(t, c, app)
	if got := cachedWorkTitle(t, c, id); got != native.Title {
		t.Fatalf("meaningful native name replaced = %q", got)
	}
	broad := namingRecord("conductor", "broad", "Improve authentication", "", "codex:native")
	broad.Metadata = map[string]any{"title_scope": "workspace"}
	broadID := ingestNamingRecord(t, c, broad)
	if got := cachedWorkTitle(t, c, broadID); got != native.Title {
		t.Fatalf("broad workspace fallback blocked a specific session name = %q", got)
	}
	if _, err := c.DB.Exec("UPDATE workspaces SET title='My pinned name' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	native.Title = "Another native rename"
	ingestNamingRecord(t, c, native)
	if got := cachedWorkTitle(t, c, id); got != "My pinned name" {
		t.Fatalf("explicit override replaced = %q", got)
	}
}

func TestWorkspaceNamesKeepPreviousChoiceWhenSourcesConflict(t *testing.T) {
	c, _ := testCatalog(t)
	id := ingestNamingRecord(t, c, namingRecord("codex", "native", "Codex native", "Fix login"))
	ingestNamingRecord(t, c, namingRecord("conductor", "one", "Fix login", "", "codex:native"))
	ingestNamingRecord(t, c, namingRecord("conductor", "two", "Fix logout", "", "codex:native"))
	if got := cachedWorkTitle(t, c, id); got != "Fix login" {
		t.Fatalf("conflict replaced previous title = %q", got)
	}
	var origin, from string
	var conflicts int
	if err := c.DB.QueryRow("SELECT display_source,display_workspace_id,display_conflicts FROM workspace_names WHERE workspace_id=?", id).
		Scan(&origin, &from, &conflicts); err != nil || origin != "linked" || from != stableID("workspace", "conductor", "local", "one") || conflicts != 2 {
		t.Fatalf("retained title lost its provenance: %q %q %d, %v", origin, from, conflicts, err)
	}
	// A new record that sees both equally specific candidates must not choose
	// based on query/map iteration order or inherit a broader task instead.
	other := namingRecord("codex", "other", "Codex other", "")
	other.Conversations[0].Aliases = []string{"conductor:one", "conductor:two"}
	otherID := ingestNamingRecord(t, c, other)
	if got := cachedWorkTitle(t, c, otherID); got != other.Title {
		t.Fatalf("ambiguous title selected = %q", got)
	}
}

func TestWorkspaceNamesCacheFirstHumanWordsAcrossSyncAndReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "catalog.sqlite3")
	c := openTestCatalog(t, file)
	defer func() { c.Close() }()
	text := "<system_instruction>Injected instructions</system_instruction>\n\nPlease fix the login bug @⟦pasted_text_2026-09-01.txt⟧(.context%2Fattachments%2Fab%2Fpasted_text_2026-09-01.txt)"
	record := namingRecord("codex", "native", "Codex native", text)
	id := ingestNamingRecord(t, c, record)
	want, _, err := c.authorshipGenerations()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RebuildAuthorship(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if got := cachedWorkTitle(t, c, id); got != "Please fix the login bug" {
		t.Fatalf("typed fallback = %q", got)
	}
	var cacheTitle, sourceTitle, version, message string
	if err := c.DB.QueryRow("SELECT authored_title,source_title,authored_version,authored_message_id FROM workspace_names WHERE workspace_id=?", id).
		Scan(&cacheTitle, &sourceTitle, &version, &message); err != nil || sourceTitle != record.Title || version != workspaceNamesVersion || message == "" {
		t.Fatalf("cache provenance = %q %q %q %q, %v", cacheTitle, sourceTitle, version, message, err)
	}
	record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{NativeID: "later", Role: "user", Kind: "message",
		Text: "Now improve the account settings instead", CreatedAt: "2026-09-01T12:30:00Z", Selected: true})
	ingestNamingRecord(t, c, record)
	if got := cachedWorkTitle(t, c, id); got != cacheTitle {
		t.Fatalf("sync replaced cached label = %q", got)
	}
	c.Close()
	c, err = OpenCatalog(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := cachedWorkTitle(t, c, id); got != cacheTitle {
		t.Fatalf("reopen replaced cached label = %q", got)
	}
	// Library reads never start or repeat the authorship classifier.
	runs := 0
	c.background = func(OperationSpec, func(context.Context, *Op) error) bool { runs++; return false }
	for range 3 {
		if _, err := c.libraryRows(t.Context(), "w.id=?", []any{id}, libraryFields{"title": true}); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 0 {
		t.Fatalf("Library reads started %d authorship rebuilds", runs)
	}
	// An edited/deleted first message invalidates its cached excerpt. It must
	// not be displayed as if it still came from the saved classification.
	record.Conversations[0].Messages[0].Text = "Find the new authentication error"
	ingestNamingRecord(t, c, record)
	if got := cachedWorkTitle(t, c, id); got != record.Title {
		t.Fatalf("edited prompt retained stale name = %q", got)
	}
	want, _, _ = c.authorshipGenerations()
	if err := c.RebuildAuthorship(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if got := cachedWorkTitle(t, c, id); got != record.Conversations[0].Messages[0].Text {
		t.Fatalf("updated typed fallback = %q", got)
	}
}

func TestWorkspaceNamesUpgradeUsesSavedAuthorshipAndSkipsAutomatedWords(t *testing.T) {
	c, _ := testCatalog(t)
	record := namingRecord("codex", "native", "Codex native", "Automated starting instructions")
	record.Conversations[0].Messages[0].Sender = "automation:test"
	record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{NativeID: "human", Role: "user", Kind: "message",
		Sender: "account:me", Text: "Repair the token counter", CreatedAt: "2026-09-01T12:30:00Z", Selected: true})
	id := ingestNamingRecord(t, c, record)
	generation, _, _ := c.authorshipGenerations()
	if err := c.RebuildAuthorship(t.Context(), generation); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("DELETE FROM meta WHERE key='workspace_names_version'"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE workspace_names SET authored_title=NULL,authored_message_id=NULL WHERE workspace_id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureWorkspaceNames(); err != nil {
		t.Fatal(err)
	}
	if got := cachedWorkTitle(t, c, id); got != "Repair the token counter" {
		t.Fatalf("upgrade title = %q", got)
	}
	if built, _ := c.metaValue(t.Context(), "authorship_generation"); built != generation {
		t.Fatalf("upgrade reran classification: %q", built)
	}
}

func TestAuthoredWorkspaceTitleBoundsUnicodeWithoutPastedText(t *testing.T) {
	text := strings.Repeat("é", 100) + " omitted copied output"
	title := authoredWorkspaceTitle(text, []authorSpan{{Start: 0, End: 200, Category: spanTyped}, {Start: 200, End: len(text), Category: spanPasted}})
	if !strings.HasSuffix(title, "…") || strings.Contains(title, "copied") || len([]rune(title)) != 81 {
		t.Fatalf("bounded title = %q", title)
	}
}

func drainConversationNames(t *testing.T, c *Catalog) {
	t.Helper()
	for {
		count, err := c.refreshConversationNames(t.Context(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return
		}
	}
}

func conversationDisplayName(t *testing.T, c *Catalog, id string) executionLabel {
	t.Helper()
	r, err := c.executionResolution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	x := r.Executions[r.BindConversation(id).ExecutionID]
	if x == nil {
		t.Fatalf("conversation %s has no execution", id)
	}
	return x.Label
}

func TestConversationNamesPreferHumanWordsAfterAutomatedInstructions(t *testing.T) {
	c, _ := testCatalog(t)
	record := namingRecord("codex", "01a128b8-395c-7121-bf40-9a1903a6f306", "Codex 01a128b8",
		"<system_instruction>Ignore this harness</system_instruction>\n\nReply with one line: your model name, and whether you have a web search tool available in this session (yes/no). Do not run any commands.")
	record.Conversations[0].Messages[0].Sender = "automation:test"
	workspace := ingestNamingRecord(t, c, record)
	cid := stableID("conversation", "codex", "local", record.Conversations[0].NativeID)
	rebuild := func() {
		t.Helper()
		generation, _, err := c.authorshipGenerations()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.RebuildAuthorship(t.Context(), generation); err != nil {
			t.Fatal(err)
		}
		drainConversationNames(t, c)
	}
	rebuild()
	if got := conversationDisplayName(t, c, cid); got.Source != "assignment" || !strings.HasPrefix(got.Text, "Reply with one line:") || strings.Contains(got.Text, "harness") {
		t.Fatalf("automated request name = %+v", got)
	}
	if got := cachedWorkTitle(t, c, workspace); !strings.HasPrefix(got, "Reply with one line:") {
		t.Fatalf("single-conversation workspace name = %q", got)
	}
	if got := countRows(t, c, "SELECT SUM(typed_words) FROM message_authorship WHERE conversation_id=?", cid); got != 0 {
		t.Fatalf("naming turned automated words into %d human words", got)
	}
	record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{
		NativeID: "human", Sender: "account:me", Role: "user", Kind: "message", CreatedAt: "2026-09-01T13:00:00Z", Selected: true,
		Text: "<system_instruction>More injected setup</system_instruction>\n\nPlease fix the login failure",
	})
	ingestNamingRecord(t, c, record)
	rebuild()
	if got := conversationDisplayName(t, c, cid); got.Source != "typed" || got.Text != "Please fix the login failure" {
		t.Fatalf("first human words must beat the earlier automation: %+v", got)
	}
	// Appends preserve the first human excerpt; source renames supersede it
	// without a full classifier run or a cold execution cache.
	record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{NativeID: "later", Role: "user", Kind: "message", Sender: "account:me", Text: "Now update the tests", CreatedAt: "2026-09-01T14:00:00Z", Selected: true})
	ingestNamingRecord(t, c, record)
	drainConversationNames(t, c)
	if got := conversationDisplayName(t, c, cid); got.Text != "Please fix the login failure" {
		t.Fatalf("append replaced the original human name: %+v", got)
	}
	fixtureExec(t, c, `UPDATE authorship_message_state SET provisional=1 WHERE message_id IN
 (SELECT id FROM messages WHERE conversation_id=? AND native_id='human')`, cid)
	if got := conversationDisplayName(t, c, cid); got.Source == "typed" {
		t.Fatal("provisional classification supplied human words to a primary name")
	}
	fixtureExec(t, c, `UPDATE authorship_message_state SET provisional=0 WHERE message_id IN
 (SELECT id FROM messages WHERE conversation_id=? AND native_id='human')`, cid)
	if got := conversationDisplayName(t, c, cid); got.Text != "Please fix the login failure" {
		t.Fatalf("refined classification did not refresh a warm name: %+v", got)
	}
	before, _, _ := c.authorshipGenerations()
	record.Title = "Repair login failures"
	ingestNamingRecord(t, c, record)
	if got := conversationDisplayName(t, c, cid); got.Source != "authored" || got.Text != record.Title {
		t.Fatalf("source rename under a warm cache = %+v", got)
	}
	if after, _, _ := c.authorshipGenerations(); after != before {
		t.Fatal("source rename reclassified human writing")
	}
}

func TestConversationNamesBackfillInvalidateAndResume(t *testing.T) {
	c, _ := testCatalog(t)
	record := namingRecord("claude", "worker", "Claude worker", "Inspect message naming")
	record.Conversations[0].Messages[0].Role = "agent"
	workspace := ingestNamingRecord(t, c, record)
	cid := stableID("conversation", "codex", "local", record.Conversations[0].NativeID)
	before := sourceGeneration(t, c)
	var output, errors bytes.Buffer
	if err := runConversationNamesCLI(Config{CatalogPath: c.Path}, []string{"audit", "--conversation", cid}, &output, &errors); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		DryRun        bool `json:"dry_run"`
		Conversations []struct {
			ProposedName string `json:"proposed_name"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(output.Bytes(), &audit); err != nil || !audit.DryRun || len(audit.Conversations) != 1 || audit.Conversations[0].ProposedName != "Inspect message naming" {
		t.Fatalf("read-only audit: %s, %v", output.String(), err)
	}
	if sourceGeneration(t, c) != before || countRows(t, c, "SELECT COUNT(*) FROM conversation_name_candidates") != 0 {
		t.Fatal("audit initialized or published names")
	}
	drainConversationNames(t, c)
	if got := conversationDisplayName(t, c, cid); got.Text != "Inspect message naming" {
		t.Fatal(got)
	}
	// A changed input is rejected before the background queue catches up.
	record.Conversations[0].Messages[0].Text = "Inspect conversation ownership"
	ingestNamingRecord(t, c, record)
	if got := conversationDisplayName(t, c, cid); got.Text == "Inspect message naming" {
		t.Fatal("edited assignment reused a stale excerpt")
	}
	drainConversationNames(t, c)
	if got := conversationDisplayName(t, c, cid); got.Text != "Inspect conversation ownership" {
		t.Fatal(got)
	}
	// An upgrade queues completed conversations once, without re-ingest.
	fixtureExec(t, c, `UPDATE conversation_names_state SET version='older'`)
	if err := c.ensureConversationNames(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, `SELECT COUNT(*) FROM conversation_names_state WHERE dirty=1`) != 1 {
		t.Fatal("policy upgrade did not queue the retained conversation")
	}
	drainConversationNames(t, c)
	if count, err := c.refreshConversationNames(t.Context(), 1); count != 0 || err != nil {
		t.Fatalf("settled migration repeated: %d %v", count, err)
	}
	fixtureExec(t, c, `DELETE FROM messages WHERE conversation_id=?`, cid)
	if got := conversationDisplayName(t, c, cid); meaningfulWorkspaceTitle(got.Text, "") && strings.Contains(got.Text, "ownership") {
		t.Fatal("deleted assignment survived in a primary name")
	}
	drainConversationNames(t, c)
	if got := cachedWorkTitle(t, c, workspace); strings.Contains(got, "ownership") {
		t.Fatal("deleted assignment survived in the workspace fallback")
	}
}

func TestConversationNamesRejectIdentifiersAndHarness(t *testing.T) {
	for _, title := range []string{
		"01a128b8-395c-7121-bf40-9a1903a6f306", "{01a128b8-395c-7121-bf40-9a1903a6f306}",
		"conductor:01a128b8-395c-7121-bf40-9a1903a6f306", "conversation_a123456789abcdef", "Codex a1234567", "deadbeef98765432",
		"/Users/example/transcripts/task.jsonl", "gAAAAencryptedmessage", "Untitled",
	} {
		if meaningfulWorkspaceTitle(title, "a different source id") {
			t.Errorf("identifier/placeholder considered a useful title: %q", title)
		}
	}
	for _, title := range []string{"Fix conversation 01a128b8", "Codex SDK naming", "Inspect renderer", "tooling_independent_review"} {
		if !meaningfulWorkspaceTitle(title, "unrelated") {
			t.Errorf("meaningful name rejected: %q", title)
		}
	}
	for _, text := range []string{"<system_instruction>Setup only</system_instruction>", "gAAAAencryptedmessage", "```sh\nls -la\n```", "# AGENTS.md instructions for /repo\n\nInstructions"} {
		if got := assignmentNameExcerpt(text); got != "" {
			t.Errorf("harness/payload became a name: %q", got)
		}
	}
}

func TestConversationNamesUseRecordedTaskAttemptsWithoutNamingHelpers(t *testing.T) {
	c := executionFixture(t, executionFixtureConversation{id: "one"}, executionFixtureConversation{id: "two"}, executionFixtureConversation{id: "helper", parent: "one", depth: 1})
	fixtureExec(t, c, `UPDATE workspaces SET source_kind='tl1',source_id='install:task',title='Repair login' WHERE id='work'`)
	fixtureExec(t, c, `INSERT INTO tl1_attempts(installation_id,attempt_id,task_id,attempt_number,conversation_native_id) VALUES('install','attempt-one','task',1,'one'),('install','attempt-two','task',2,'two')`)
	if got := conversationDisplayName(t, c, "one"); got.Text != "Repair login · attempt 1" || got.Source != "context" {
		t.Fatal(got)
	}
	if got := conversationDisplayName(t, c, "two"); got.Text != "Repair login · attempt 2" {
		t.Fatal(got)
	}
	if got := conversationDisplayName(t, c, "helper"); strings.Contains(got.Text, "Repair login") || got.Text == "helper" {
		t.Fatalf("helper inherited its parent's task or native ID: %+v", got)
	}
	// A workspace's previously derived excerpt cannot become task context
	// once the originating candidate has been withdrawn.
	for _, origin := range []string{"typed", "assignment"} {
		fixtureExec(t, c, `INSERT INTO workspace_names(workspace_id,source_title,source_scope,display_title,display_source)
 VALUES('work','Untitled','task','Old request',?) ON CONFLICT(workspace_id) DO UPDATE SET display_source=excluded.display_source`, origin)
		fixtureExec(t, c, `UPDATE workspaces SET title='Old request' WHERE id='work'`)
		if got := conversationDisplayName(t, c, "one"); strings.Contains(got.Text, "Old request") {
			t.Fatalf("withdrawn %s excerpt survived as task context: %+v", origin, got)
		}
	}
}

func TestConversationNamesScopeChangesInvalidateWarmNames(t *testing.T) {
	c, _ := testCatalog(t)
	record := namingRecord("codex", "original", "Named original conversation", "")
	workspace := ingestNamingRecord(t, c, record)
	cid := stableID("conversation", "codex", "local", record.Conversations[0].NativeID)
	if got := conversationDisplayName(t, c, cid); got.Text != record.Title {
		t.Fatal(got)
	}
	fixtureExec(t, c, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('another',?,'codex','local','another')`, workspace)
	for _, id := range []string{cid, "another"} {
		if got := conversationDisplayName(t, c, id); got.Text == record.Title {
			t.Fatalf("shared workspace title named a different conversation: %+v", got)
		}
	}
	fixtureExec(t, c, `DELETE FROM conversations WHERE id='another'`)
	if got := conversationDisplayName(t, c, cid); got.Text != record.Title {
		t.Fatalf("restored source scope was not reflected in a warm reader: %+v", got)
	}
}

// A bounded lookup finds an edge whether the given workspace is on the left
// or the right of a native-alias link, the same edges the full scan finds,
// and one edge however many links or sides found it.
func TestWorkspaceNameEdgesFromEitherSide(t *testing.T) {
	c, _ := testCatalog(t)
	native := ingestNamingRecord(t, c, namingRecord("codex", "native", "Codex native", "Fix login"))
	task := namingRecord("tl1", "task", "Improve authentication", "", "codex:native")
	second := task.Conversations[0]
	second.NativeID = "tl1:task-2"
	task.Conversations = append(task.Conversations, second)
	taskID := ingestNamingRecord(t, c, task)
	ingestNamingRecord(t, c, namingRecord("codex", "unrelated", "Unrelated", "Other work"))
	if n := countRows(t, c, "SELECT COUNT(*) FROM conversation_identity_links WHERE relationship='native-alias'"); n < 2 {
		t.Fatalf("fixture has %d native-alias links, want one per task conversation", n)
	}
	edges := func(ids []string) []workspaceNameEdge {
		t.Helper()
		var found []workspaceNameEdge
		if err := c.writeTransaction(t.Context(), "edges", func(tx *sql.Tx) error {
			var err error
			found, err = workspaceNameEdges(tx, ids)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return found
	}
	all := edges(nil)
	// Links run both ways here (their direction follows conversation IDs), so
	// each workspace is on the left of one edge and the right of the other.
	if len(all) != 2 || all[0].left != all[1].right || all[0].right != all[1].left {
		t.Fatalf("full scan edges = %v, want one each way", all)
	}
	sides := map[string]bool{all[0].left: true, all[0].right: true}
	if !sides[native] || !sides[taskID] {
		t.Fatalf("edges %v do not join %s and %s", all, native, taskID)
	}
	for _, ids := range [][]string{{native}, {taskID}, {native, taskID}} {
		got := edges(ids)
		seen := map[workspaceNameEdge]int{}
		for _, edge := range got {
			seen[edge]++
		}
		if len(got) != len(all) || seen[all[0]] != 1 || seen[all[1]] != 1 {
			t.Fatalf("edges from %v = %v, want %v once each", ids, got, all)
		}
	}
	// Two links between conversations of the same two workspaces, in the
	// same orientation, are one edge.
	var left, right string
	if err := c.DB.QueryRow(`SELECT l.left_id,l.right_id FROM conversation_identity_links l JOIN conversations a ON a.id=l.left_id
		WHERE l.relationship='native-alias' AND a.workspace_id=?`, all[0].left).Scan(&left, &right); err != nil {
		t.Fatal(err)
	}
	// The task workspace has a second conversation; link it on its side.
	sibling := func(workspace, except string) string {
		var id string
		_ = c.DB.QueryRow(`SELECT id FROM conversations WHERE workspace_id=? AND id<>? ORDER BY id LIMIT 1`, workspace, except).Scan(&id)
		return id
	}
	if other := sibling(all[0].right, right); other != "" {
		right = other
	} else if other := sibling(all[0].left, left); other != "" {
		left = other
	} else {
		t.Fatal("no second conversation on either side")
	}
	execTx(t, c.DB, `INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('`+left+`','`+right+`','native-alias',1,'{}')`)
	if n := countRows(t, c, "SELECT COUNT(*) FROM conversation_identity_links l JOIN conversations a ON a.id=l.left_id JOIN conversations b ON b.id=l.right_id WHERE l.relationship='native-alias' AND a.workspace_id='"+all[0].left+"' AND b.workspace_id='"+all[0].right+"'"); n != 2 {
		t.Fatalf("fixture has %d same-orientation links, want 2", n)
	}
	for _, ids := range [][]string{{all[0].left}, {all[0].right}, {native, taskID}} {
		got := edges(ids)
		seen := map[workspaceNameEdge]int{}
		for _, edge := range got {
			seen[edge]++
		}
		if len(got) != 2 || seen[all[0]] != 1 || seen[all[1]] != 1 {
			t.Fatalf("duplicate links from %v = %v, want %v once each", ids, got, all)
		}
	}
	if got := edges([]string{stableID("workspace", "codex", "local", "unrelated")}); len(got) != 0 {
		t.Fatalf("unlinked workspace has edges %v", got)
	}
	if got := edges([]string{}); len(got) != 0 {
		t.Fatalf("no workspaces has edges %v", got)
	}
}

// Reconciling identities links conversations from before alias declarations
// and resolves the names of just the workspaces the new links join.
func TestReconcileIdentitiesNamesNewlyLinkedWorkspaces(t *testing.T) {
	c, _ := testCatalog(t)
	native := ingestNamingRecord(t, c, namingRecord("codex", "native", "Codex native", "Fix login"))
	task := ingestNamingRecord(t, c, namingRecord("tl1", "task", "Improve authentication", "", "codex:native"))
	names := func() map[string]string {
		t.Helper()
		rows, err := queryMaps(c.DB, "SELECT workspace_id,display_title,COALESCE(display_workspace_id,'') shown FROM workspace_names WHERE workspace_id IN (?,?)", native, task)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]string{}
		for _, row := range rows {
			found[firstString(row["workspace_id"])] = firstString(row["display_title"]) + "|" + firstString(row["shown"])
		}
		return found
	}
	linked := names()
	// A library from before declarations: the link and its declaration are
	// missing, and the names were resolved without it.
	execTx(t, c.DB, "DELETE FROM conversation_identity_links", "DELETE FROM declared_native_aliases")
	if err := c.writeTransaction(t.Context(), "names", func(tx *sql.Tx) error { return refreshWorkspaceNames(tx, nil) }); err != nil {
		t.Fatal(err)
	}
	if unlinked := names(); maps.Equal(unlinked, linked) {
		t.Fatalf("fixture names do not depend on the link: %v", linked)
	}
	added, err := c.ReconcileIdentitiesContext(t.Context())
	if err != nil || added == 0 {
		t.Fatalf("reconcile added %d links: %v", added, err)
	}
	if got := names(); !maps.Equal(got, linked) {
		t.Fatalf("names after reconcile = %v, want %v", got, linked)
	}
}
