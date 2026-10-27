package archive

import (
	"context"
	"database/sql"
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
	if got := edges([]string{stableID("workspace", "codex", "local", "unrelated")}); len(got) != 0 {
		t.Fatalf("unlinked workspace has edges %v", got)
	}
	if got := edges([]string{}); len(got) != 0 {
		t.Fatalf("no workspaces has edges %v", got)
	}
}
