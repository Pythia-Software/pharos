package archive

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSessionNamesSurviveCaptureAndTitleOnlyRename(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := t.TempDir()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("native", 1))
	index := filepath.Join(root, "session_index.jsonl")
	writeSourceFile(t, index, `{"id":"native","thread_name":"Old title","updated_at":"2026-09-01T12:00:00Z"}`+"\n"+
		`{"id":"native","thread_name":"Fix login timeouts","updated_at":"2026-09-01T12:01:00Z"}`+"\n")
	if result := ingestSource(t, c, source); result.Error != nil || result.Workspaces != 1 {
		t.Fatalf("initial names = %+v", result)
	}
	id := stableID("workspace", "codex", "local", "native")
	if got := cachedWorkTitle(t, c, id); got != "Fix login timeouts" {
		t.Fatalf("index name = %q", got)
	}
	if result := ingestSource(t, c, source); !result.SkippedUnchanged {
		t.Fatalf("unchanged naming input = %+v", result)
	}
	writeSourceFile(t, index, `{"id":"native","thread_name":"Repair expired login sessions","updated_at":"2026-09-01T12:02:00Z"}`+"\n")
	if result := ingestSource(t, c, source); result.Error != nil || result.Workspaces != 1 || result.Parsed != 1 {
		t.Fatalf("title-only rename = %+v", result)
	}
	if got := cachedWorkTitle(t, c, id); got != "Repair expired login sessions" {
		t.Fatalf("renamed index title = %q", got)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if _, err := os.Stat(capturedPath(config, "codex", "files/session_index.jsonl")); err != nil {
		t.Fatal("session names not captured:", err)
	}
	// The source goes away. Another host must get the same name from captures.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got := cachedWorkTitle(t, other, id); got != "Repair expired login sessions" {
		t.Fatalf("off-host captured title = %q", got)
	}
}

func TestCodexStateNamesPreferNewestSchemaAndDoNotUsePromptTitles(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := t.TempDir()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	writeSourceFile(t, filepath.Join(root, "archived_sessions", "rollout.jsonl"), codexLines("native", 1))
	writeSourceFile(t, filepath.Join(root, "session_index.jsonl"), `{"id":"native","thread_name":"Outdated index name"}`+"\n")
	state := func(name, title string) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY,name TEXT,title TEXT,rollout_path TEXT);
			INSERT INTO threads VALUES('native',?,'Injected prompt text',?)`, title, filepath.Join(root, "archived_sessions", "rollout.jsonl")); err != nil {
			t.Fatal(err)
		}
		return db
	}
	state("state_9.sqlite", "Older schema name")
	db := state("state_10.sqlite", "Repair the token counter")
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	id := stableID("workspace", "codex", "local", "native")
	if got := cachedWorkTitle(t, c, id); got != "Repair the token counter" {
		t.Fatalf("state name = %q", got)
	}
	if _, err := db.Exec("UPDATE threads SET name='Fix counter resets'"); err != nil {
		t.Fatal(err)
	}
	if result := ingestSource(t, c, source); result.Error != nil || result.Workspaces != 1 {
		t.Fatalf("database-only rename = %+v", result)
	}
	if got := cachedWorkTitle(t, c, id); got != "Fix counter resets" {
		t.Fatalf("renamed state name = %q", got)
	}
	if result := runCapture(t, config); !result.OK || captureResult(t, result, "codex").SnapshotsTaken != 2 {
		t.Fatalf("state capture = %+v", result)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got := cachedWorkTitle(t, other, id); got != "Fix counter resets" {
		t.Fatalf("captured database name = %q", got)
	}
	useHost(t, "host-a")
	if _, err := db.Exec("UPDATE threads SET name='Untitled'"); err != nil {
		t.Fatal(err)
	}
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := cachedWorkTitle(t, c, id); got != "Codex native" {
		t.Fatalf("placeholder name retained previous semantic title = %q", got)
	}
	if _, err := db.Exec("UPDATE threads SET name='Fix counter resets'"); err != nil {
		t.Fatal(err)
	}
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := cachedWorkTitle(t, c, id); got != "Fix counter resets" {
		t.Fatalf("restored semantic title = %q", got)
	}
	if _, err := db.Exec("UPDATE threads SET name=NULL"); err != nil {
		t.Fatal(err)
	}
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := cachedWorkTitle(t, c, id); got != "Codex native" {
		t.Fatalf("cleared name resurrected index or prompt title = %q", got)
	}
	// Reindexing an older capture cannot undo the latest live rename/clear.
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if got := cachedWorkTitle(t, c, id); got != "Codex native" {
		t.Fatalf("older capture regressed name = %q", got)
	}
}

func TestCodexNamingPathCannotOverrideDifferentSessionIdentity(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	writeSourceFile(t, file, codexLines("native", 1))
	db, err := sql.Open("sqlite", filepath.Join(root, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT,name TEXT,rollout_path TEXT);
		INSERT INTO threads VALUES('different-session','Wrong name',?)`, file); err != nil {
		t.Fatal(err)
	}
	records := discoverAll(t, SourceConfig{Kind: "codex", Path: root, Account: "local"})
	if got := records["native"].Title; got != "Codex native" {
		t.Fatalf("stale metadata path supplied another session's name: %q", got)
	}
}

func TestClaudeCustomTitleAndIndexSummaryAreCaptured(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	file := filepath.Join(project, "session.jsonl")
	transcript := claudeLine("session", "prompt", "Repair the counter", "", 1) +
		`{"type":"custom-title","customTitle":"My counter investigation","sessionId":"session"}` + "\n" +
		`{"type":"ai-title","aiTitle":"A later automatic title","sessionId":"session"}` + "\n"
	writeSourceFile(t, file, transcript)
	index := filepath.Join(project, "sessions-index.json")
	writeSourceFile(t, index, `{"entries":[{"sessionId":"session","customTitle":"Stale indexed name","summary":"Counter investigation"}]}`)
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	id := stableID("workspace", "claude", "local", "session")
	if got := cachedWorkTitle(t, c, id); got != "My counter investigation" {
		t.Fatalf("custom title lost to AI or index title = %q", got)
	}
	generation, _, _ := c.authorshipGenerations()
	writeSourceFile(t, file, transcript+`{"type":"custom-title","customTitle":"Renamed investigation","sessionId":"session","timestamp":"2026-09-02T12:00:00Z"}`+"\n")
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := cachedWorkTitle(t, c, id); got != "Renamed investigation" {
		t.Fatalf("timestamped custom rename = %q", got)
	}
	if after, _, _ := c.authorshipGenerations(); after != generation {
		t.Fatalf("custom rename invalidated authorship: %q -> %q", generation, after)
	}
	writeSourceFile(t, file, transcript+`{"type":"custom-title","customTitle":"","sessionId":"session"}`+"\n")
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := cachedWorkTitle(t, c, id); got != "A later automatic title" {
		t.Fatalf("cleared custom title resurrected stale index = %q", got)
	}
	// A transcript without a native name can use a source summary, but an
	// orphan index entry must not manufacture a conversation.
	writeSourceFile(t, file, claudeLine("session", "prompt", "Repair the counter", "", 1))
	writeSourceFile(t, index, `{"entries":[{"sessionId":"session","summary":"Counter investigation"},{"sessionId":"missing","summary":"Orphan session"}]}`)
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	if got := cachedWorkTitle(t, c, id); got != "Counter investigation" {
		t.Fatalf("summary fallback = %q", got)
	}
	writeSourceFile(t, index, `{"entries":[{"sessionId":"session","summary":"Repair counter resets"}]}`)
	if result := ingestSource(t, c, source); result.Error != nil || result.Parsed != 1 {
		t.Fatalf("index-only change = %+v", result)
	}
	if got := cachedWorkTitle(t, c, id); got != "Repair counter resets" {
		t.Fatalf("index-only rename = %q", got)
	}
	if n := countRows(t, c, "SELECT COUNT(*) FROM workspaces"); n != 1 {
		t.Fatalf("orphan summary made a work: %d", n)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if _, err := os.Stat(capturedPath(config, "claude", "files/project/sessions-index.json")); err != nil {
		t.Fatal("Claude index not captured:", err)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got := cachedWorkTitle(t, other, id); got != "Repair counter resets" {
		t.Fatalf("captured summary = %q", got)
	}
}

func TestConductorWorkspaceNameOnlyFillsPlaceholderSessionTitle(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "conductor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY,title TEXT,workspace_id TEXT,agent_type TEXT);
		CREATE TABLE session_messages(id TEXT,session_id TEXT,role TEXT,content TEXT);
		CREATE TABLE workspaces(id TEXT PRIMARY KEY,workspace_name TEXT,directory_name TEXT);
		INSERT INTO workspaces VALUES('w','Improve search','ljubljana');
		INSERT INTO sessions VALUES('one','Untitled','w','codex'),('two','Fix query matching','w','claude');
		INSERT INTO session_messages VALUES('m1','one','user','hello'),('m2','two','user','hello');`); err != nil {
		t.Fatal(err)
	}
	records := discoverAll(t, SourceConfig{Kind: "conductor", Path: root, Account: "local"})
	if records["one"].Title != "Improve search" || records["two"].Title != "Fix query matching" {
		t.Fatalf("session/workspace titles = %q / %q", records["one"].Title, records["two"].Title)
	}
	if _, err := db.Exec("UPDATE workspaces SET workspace_name=directory_name"); err != nil {
		t.Fatal(err)
	}
	records = discoverAll(t, SourceConfig{Kind: "conductor", Path: root, Account: "local"})
	if records["one"].Title != "Untitled" {
		t.Fatalf("geographic directory used as semantic title = %q", records["one"].Title)
	}
}
