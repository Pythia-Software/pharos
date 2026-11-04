package archive

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileHistoryHashes reads a history's commits as short hashes, each with
// its linked sessions.
func fileHistoryHashes(file map[string]any) []string {
	result := []string{}
	for _, item := range file["commits"].([]any) {
		commit := item.(map[string]any)
		sessions := []string{}
		for _, link := range commit["sessions"].([]any) {
			sessions = append(sessions, firstString(link.(map[string]any)["workspace_id"]))
		}
		result = append(result, firstString(commit["hash"])[:7]+" "+strings.Join(sessions, ","))
	}
	return result
}

func fileHistorySessions(file map[string]any) []string {
	result := []string{}
	for _, item := range file["sessions"].([]any) {
		result = append(result, firstString(item.(map[string]any)["workspace_id"]))
	}
	return result
}

func TestFileHistoryWithGit(t *testing.T) {
	f := newGitFixture(t)
	repo := f.repo
	for _, id := range []string{"first", "second", "elsewhere"} {
		f.session(id, "")
	}
	f.edit("first", "main.go", 10)
	first := repo.commit(9.99, "First", map[string]string{"main.go": "package main\n"})
	f.edit("second", "main.go", 5)
	f.edit("second", "main.go", 4.9)
	second := repo.commit(4.89, "Second", map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	// A commit long after any session's edit is the file's history too, with
	// no session linked to it. A file no session edited is not kept.
	byHand := repo.commit(2, "By hand", map[string]string{"main.go": "package main\n\nfunc main() { run() }\n"})
	repo.commit(1, "Unrelated", map[string]string{"other.go": "package other\n"})
	f.edit("elsewhere", "util.go", 3)
	repo.git(time.Now(), "update-ref", "refs/remotes/origin/main", "HEAD")
	f.refresh()

	if stored := countRows(t, f.catalog, `SELECT COUNT(*) FROM git_commit_files WHERE path='other.go'`); stored != 0 {
		t.Fatalf("kept %d commits of a file no session edited", stored)
	}
	for _, path := range []string{"main.go", filepath.Join(repo.dir, "main.go")} {
		history := callProjectTool(t, f.catalog, "get_file_history", map[string]any{"path": path})
		files := history["files"].([]any)
		if len(files) != 1 {
			t.Fatalf("%s: %#v", path, history)
		}
		file := files[0].(map[string]any)
		if got := strings.Join(fileHistorySessions(file), " "); got != "second first" {
			t.Fatalf("%s sessions: %s", path, got)
		}
		latest := file["sessions"].([]any)[0].(map[string]any)
		if integer(latest["edits"]) != 2 || latest["outcome_label"] != "landed" || !strings.Contains(firstString(latest["url"]), "message=call-") {
			t.Fatalf("latest session: %#v", latest)
		}
		if git := file["git"].(map[string]any); git["status"] != "checked" {
			t.Fatalf("git: %#v", git)
		}
		want := []string{byHand[:7] + " ", second[:7] + " second", first[:7] + " first"}
		if got := fileHistoryHashes(file); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s commits: %v, want %v", path, got, want)
		}
	}
	// A path in no session's edits says so instead of an empty list.
	none := callProjectTool(t, f.catalog, "get_file_history", map[string]any{"path": "other.go"})
	if len(none["files"].([]any)) != 0 || !strings.Contains(firstString(none["note"]), "No session") {
		t.Fatalf("unedited file: %#v", none)
	}
	if _, err := callMCP(f.catalog, "get_file_history", map[string]any{}); err == nil {
		t.Fatal("a history without a path should be an error")
	}

	outcomes := callProjectTool(t, f.catalog, "list_outcomes", map[string]any{"label": "landed"})
	if integer(outcomes["total"]) != 2 || integer(outcomes["labels"].(map[string]any)["landed"]) != 2 {
		t.Fatalf("landed outcomes: %#v", outcomes)
	}
	asked := callProjectTool(t, f.catalog, "list_outcomes", map[string]any{"workspace_ids": []any{"second"}})
	items := asked["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("outcomes by ID: %#v", asked)
	}
	item := items[0].(map[string]any)
	commits := item["commits"].([]any)
	if item["evidence_tier"] != "local_git" || len(commits) != 1 || commits[0].(map[string]any)["counted"] != true ||
		!strings.HasPrefix(firstString(commits[0].(map[string]any)["hash"]), second) {
		t.Fatalf("second's evidence: %#v", item)
	}
}

func TestFileHistoryWithoutGit(t *testing.T) {
	f := newOutcomeFixture(t)
	f.session("drafter", "claude")
	f.session("editor", "claude")
	folder := filepath.Join(t.TempDir(), "notes")
	plan := filepath.Join(folder, "plan.md")
	f.write("drafter", plan, 20, code("plan", 0, 6))
	f.edit("editor", plan, "", "", 3, code("plan", 0, 2), code("plan", 100, 4))
	f.refresh()

	for _, path := range []string{plan, folderKey(plan), "plan.md", "notes/plan.md"} {
		history := callProjectTool(t, f.catalog, "get_file_history", map[string]any{"path": path})
		files := history["files"].([]any)
		if len(files) != 1 {
			t.Fatalf("%s: %#v", path, history)
		}
		file := files[0].(map[string]any)
		if got := strings.Join(fileHistorySessions(file), " "); got != "editor drafter" {
			t.Fatalf("%s sessions: %s", path, got)
		}
		// Local Git does not apply: no commits, and nothing reads as missing.
		if git := file["git"].(map[string]any); git["status"] != "not_applicable" || len(file["commits"].([]any)) != 0 {
			t.Fatalf("%s git: %#v", path, file)
		}
		drafter := file["sessions"].([]any)[1].(map[string]any)
		if integer(drafter["lines_added"]) != 6 || drafter["outcome_label"] == nil {
			t.Fatalf("drafter: %#v", drafter)
		}
	}
	// Library file search finds the file the sessions edited, each at the
	// conversation of its first edit, and names its project.
	found, err := f.catalog.LibraryFind(t.Context(), LibraryFindOptions{Kind: "file", Query: "plan.md", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if hits := found["items"].([]libraryHit); len(hits) != 2 || hits[0].ConversationID == "" || hits[0].Path != folderKey(plan) {
		t.Fatalf("file search: %#v", found)
	}
	outcomes := callProjectTool(t, f.catalog, "list_outcomes", map[string]any{})
	for _, item := range outcomes["items"].([]any) {
		row := item.(map[string]any)
		if row["git_check"] != "not_applicable" || row["evidence_tier"] == "local_git" || firstString(row["outcome_why"]) == "" {
			t.Fatalf("an outcome without Git: %#v", row)
		}
	}
}
