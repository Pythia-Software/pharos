package archive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The two libraries every projects-and-outcomes test reads (see
// docs/projects-and-outcomes.md): one without Git at all, and one mixing
// repositories with plain folders. Both are indexed through the real
// adapters, from transcripts written under t.TempDir().

// projectLibrary is an indexed fixture library and the folders it names.
type projectLibrary struct {
	catalog *Catalog
	config  Config
	// folders by name: notes, research, later (no Git), and in the mixed
	// library app (a repository), gone (a checkout since deleted).
	folders map[string]string
	claude  string
}

// claudeSession is a Claude Code transcript of one prompt and answer in cwd
// (none when empty), on branch when one is given.
func claudeSession(session, cwd, branch, text string, minute int) string {
	extra := ""
	if cwd != "" {
		extra += fmt.Sprintf(`,"cwd":%q`, cwd)
	}
	if branch != "" {
		extra += fmt.Sprintf(`,"gitBranch":%q`, branch)
	}
	return fmt.Sprintf(`{"type":"user","uuid":"%s-u","sessionId":%q%s,"timestamp":"2026-09-01T00:%02d:00Z","message":{"role":"user","content":%q}}`+"\n", session, session, extra, minute, text) +
		fmt.Sprintf(`{"type":"assistant","uuid":"%s-a","sessionId":%q%s,"timestamp":"2026-09-01T00:%02d:30Z","message":{"role":"assistant","model":"claude-test","content":"Done."}}`+"\n", session, session, extra, minute)
}

func writeClaudeSession(t *testing.T, dir, session, content string) {
	t.Helper()
	writeSourceFile(t, filepath.Join(dir, "project", session+".jsonl"), content)
}

// ingestClaude indexes the fixture's Claude transcripts again.
func (l projectLibrary) ingestClaude(t *testing.T) {
	t.Helper()
	ingestSource(t, l.catalog, SourceConfig{Name: "claude", Kind: "claude", Path: l.claude, Account: "local", Enabled: true})
}

// noGitLibrary is sessions in plain folders, a session with no working
// directory, and a ChatGPT export. Nothing in it is a Git repository.
func noGitLibrary(t *testing.T) projectLibrary {
	t.Helper()
	override(t, &temporaryFolders, nil)
	catalog, config := testCatalog(t)
	base := t.TempDir()
	library := projectLibrary{catalog: catalog, config: config, claude: filepath.Join(base, "claude"), folders: map[string]string{}}
	for _, name := range []string{"notes", "research", "later"} {
		library.folders[name] = filepath.Join(base, "home", name)
		if err := os.MkdirAll(library.folders[name], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeClaudeSession(t, library.claude, "s-notes", claudeSession("s-notes", library.folders["notes"], "", "Tidy my meeting notes", 1))
	writeClaudeSession(t, library.claude, "s-research", claudeSession("s-research", library.folders["research"], "", "Summarize these papers", 2))
	writeClaudeSession(t, library.claude, "s-later", claudeSession("s-later", library.folders["later"], "", "Draft a plan", 3))
	writeClaudeSession(t, library.claude, "s-nowhere", claudeSession("s-nowhere", "", "", "What is a monad?", 4))
	library.ingestClaude(t)
	export := filepath.Join(base, "chatgpt")
	writeSourceFile(t, filepath.Join(export, "conversations.json"), `[{"id":"chat-1","title":"Trip ideas","create_time":1788220800,"update_time":1788224400,
		"mapping":{"n1":{"message":{"id":"m1","author":{"role":"user"},"content":{"parts":["Where should I go in spring?"]},"create_time":1788220800}},
		"n2":{"parent":"n1","message":{"id":"m2","author":{"role":"assistant"},"content":{"parts":["Lisbon."]},"create_time":1788220860}}}}]`)
	ingestSource(t, catalog, SourceConfig{Name: "chatgpt", Kind: "chatgpt-export", Path: export, Account: "local", Enabled: true})
	return library
}

// mixedLibrary is the no-Git library plus a repository on a branch, a session
// in a subfolder of it, and a session whose checkout has since been deleted.
func mixedLibrary(t *testing.T) projectLibrary {
	t.Helper()
	library := noGitLibrary(t)
	base := filepath.Dir(library.folders["notes"])
	library.folders["app"] = filepath.Join(base, "src", "app")
	gitInit(t, library.folders["app"])
	library.folders["gone"] = filepath.Join(base, "src", "gone")
	gitInit(t, library.folders["gone"])
	writeClaudeSession(t, library.claude, "s-app", claudeSession("s-app", library.folders["app"], "feature/login", "Add a login form", 10))
	writeClaudeSession(t, library.claude, "s-app-docs", claudeSession("s-app-docs", filepath.Join(library.folders["app"], "docs"), "main", "Fix the docs", 11))
	writeClaudeSession(t, library.claude, "s-gone", claudeSession("s-gone", library.folders["gone"], "main", "Try an idea", 12))
	library.ingestClaude(t)
	if err := os.RemoveAll(library.folders["gone"]); err != nil {
		t.Fatal(err)
	}
	return library
}

// gitInit makes dir a repository with one commit, using no user configuration.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+filepath.Base(dir)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."},
		{"-c", "user.name=Pharos Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "Start"}} {
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
}

// projects reads each workspace's project by source ID.
func (l projectLibrary) projects(t *testing.T) map[string]map[string]any {
	t.Helper()
	rows, err := queryMaps(l.catalog.DB, `SELECT w.source_id,`+projectIDSQL("w")+` project_id,`+projectNameSQL("w")+` project_name,`+projectKindSQL("w")+` project_kind,
		r.display_name repository_name FROM workspaces w`+projectJoin("w")+` LEFT JOIN repositories r ON r.id=w.repository_id`)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]map[string]any{}
	for _, row := range rows {
		result[firstString(row["source_id"])] = row
	}
	return result
}
