package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func lines(value int64) *int64 { return &value }

func TestDeriveSessionFacts(t *testing.T) {
	edit := func(path, repoPath, repository, at string, added, removed *int64) outcomeCall {
		return outcomeCall{Category: "edit", Status: "ok", Path: path, RepoPath: repoPath, RepositoryID: repository, Scope: "repo", StartedAt: at, LinesAdded: added, LinesRemoved: removed}
	}
	command := func(category, status, text, result, at string) outcomeCall {
		return outcomeCall{Category: "command", Status: status, CommandCategory: category, Command: text, Result: result, StartedAt: at}
	}
	facts := deriveSessionFacts([]outcomeCall{
		command("test", "ok", "go test ./...", "", "2026-09-01T00:00:00Z"),
		edit("/src/app/main.go", "main.go", "repo", "2026-09-01T00:01:00Z", lines(10), lines(2)),
		// The same file in another worktree of the repository is the same file.
		edit("/src/app-worktree/main.go", "main.go", "repo", "2026-09-01T00:02:00Z", lines(3), lines(1)),
		edit("/Users/x/notes/plan.md", "", "", "2026-09-01T00:03:00Z", nil, nil),
		// Failed edits, scratch files and the agent's own files change nothing.
		{Category: "edit", Status: "error", Path: "/src/app/other.go", Scope: "repo", StartedAt: "2026-09-01T00:04:00Z"},
		{Category: "edit", Status: "ok", Path: "/tmp/scratch.txt", Scope: "temp", StartedAt: "2026-09-01T00:04:00Z"},
		{Category: "edit", Status: "ok", Path: "/Users/x/.claude/plans/p.md", Scope: "agent_home", StartedAt: "2026-09-01T00:04:00Z"},
		command("build", "error", "go build ./...", "", "2026-09-01T00:05:00Z"),
		command("test", "ok", "go test ./...", "", "2026-09-01T00:06:00Z"),
		command("vcs", "ok", "git add -A && git commit -m 'Add login'", "[feature/login 1a2b3c4] Add login\n 2 files changed\n", "2026-09-01T00:07:00Z"),
		command("vcs", "ok", "git commit --amend", "[detached HEAD 5d6e7f8] Add login\n", "2026-09-01T00:07:30Z"),
		command("vcs", "ok", "git push -u origin feature/login", "To github.com:acme/app.git\n * [new branch]      feature/login -> feature/login\n", "2026-09-01T00:08:00Z"),
		command("vcs", "ok", "gh pr create --fill", "https://github.com/acme/app/pull/42\n", "2026-09-01T00:09:00Z"),
		// A failed commit printed nothing that counts.
		command("vcs", "error", "git commit -m x", "[main 9999999] x\n", "2026-09-01T00:10:00Z"),
	})
	if len(facts.Files) != 2 || facts.Files[0].Key != "path:/Users/x/notes/plan.md" || facts.Files[1].Key != "repository:repo:main.go" || facts.Files[1].Edits != 2 {
		t.Fatalf("files: %+v", facts.Files)
	}
	if *facts.Files[1].Added != 13 || facts.Files[0].Added != nil || *facts.Added != 13 || *facts.Removed != 3 {
		t.Fatalf("lines: %+v %v %v", facts.Files, *facts.Added, *facts.Removed)
	}
	if facts.FirstEdit != "2026-09-01T00:01:00Z" || facts.LastEdit != "2026-09-01T00:03:00Z" {
		t.Fatalf("edit times: %s %s", facts.FirstEdit, facts.LastEdit)
	}
	// The last test or build after the last edit decides: the build failed,
	// then the tests passed.
	if facts.Verification != "passed" || facts.VerifiedBy != "test" {
		t.Fatalf("verification: %s by %s", facts.Verification, facts.VerifiedBy)
	}
	if len(facts.Commits) != 2 || facts.Commits[0] != (outcomeCommit{"1a2b3c4", "feature/login", "2026-09-01T00:07:00Z"}) || facts.Commits[1].Branch != "" {
		t.Fatalf("commits: %+v", facts.Commits)
	}
	if !facts.Pushed || len(facts.PullRequests) != 1 || facts.PullRequests[0] != "https://github.com/acme/app/pull/42" {
		t.Fatalf("push and pull requests: %v %v", facts.Pushed, facts.PullRequests)
	}

	if facts := deriveSessionFacts([]outcomeCall{edit("/a.go", "", "", "2026-09-01T00:00:00Z", lines(1), lines(0))}); facts.Verification != "not_run" {
		t.Fatalf("no test after the last edit: %q", facts.Verification)
	}
	if facts := deriveSessionFacts([]outcomeCall{command("test", "ok", "go test", "", "")}); facts.Verification != "" || len(facts.Files) != 0 {
		t.Fatalf("a session without edits has nothing to verify: %+v", facts)
	}
}

// outcomeFixture seeds sessions and their tool calls directly.
type outcomeFixture struct {
	t       *testing.T
	catalog *Catalog
	calls   int
}

func newOutcomeFixture(t *testing.T) *outcomeFixture {
	catalog, _ := testCatalog(t)
	return &outcomeFixture{t: t, catalog: catalog}
}

func daysAgo(days float64) string {
	return time.Now().UTC().Add(-time.Duration(days * float64(24*time.Hour))).Format(time.RFC3339)
}

func (f *outcomeFixture) session(id, source string) {
	f.t.Helper()
	execTx(f.t, f.catalog.DB,
		fmt.Sprintf(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,activity_at,indexed_at) VALUES('%s','%s','local','%s','%s','%s','x')`, id, source, id, id, daysAgo(0)),
		fmt.Sprintf(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('%s-c','%s','claude','local','%s')`, id, id, id))
}

// edit records a successful edit of path (in repository at repoPath when
// repository is set) by session at a time days ago, replacing old with new.
func (f *outcomeFixture) edit(session, path, repository, repoPath string, days float64, old, new string) {
	f.t.Helper()
	f.call(session, path, repository, repoPath, days, "Edit", map[string]any{"file_path": path, "old_string": old, "new_string": new}, countLines(new), countLines(old))
}

// write records a successful write of the whole of path.
func (f *outcomeFixture) write(session, path string, days float64, content string) {
	f.t.Helper()
	f.call(session, path, "", "", days, "Write", map[string]any{"file_path": path, "content": content}, countLines(content), 0)
}

func (f *outcomeFixture) call(session, path, repository, repoPath string, days float64, tool string, input map[string]any, added, removed int) {
	f.t.Helper()
	f.calls++
	message := ""
	if input != nil {
		text, _ := json.Marshal(map[string]any{"input": input})
		message = fmt.Sprintf("'call-%d-m'", f.calls)
		execTx(f.t, f.catalog.DB, fmt.Sprintf(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES(%s,'%s-c',%s,'assistant','tool_call','%s','x','h')`,
			message, session, message, strings.ReplaceAll(string(text), "'", "''")))
	} else {
		message = "NULL"
	}
	execTx(f.t, f.catalog.DB, fmt.Sprintf(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,started_at,
		path_absolute,repo_path,path_repository_id,path_scope,lines_added,lines_removed,call_message_id) VALUES('call-%d','%s','%s-c',%d,'claude','tool','%s','edit','ok','%s','%s',%s,%s,'repo',%d,%d,%s)`,
		f.calls, session, session, f.calls, tool, daysAgo(days), path, sqlNullable(repoPath), sqlNullable(repository), added, removed, message))
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// code is n distinct lines of a file, from line start.
func code(name string, start, n int) string {
	lines := make([]string, n)
	for index := range lines {
		lines[index] = fmt.Sprintf("value := %s(%d)", name, start+index)
	}
	return strings.Join(lines, "\n")
}

func sqlNullable(value string) string {
	if value == "" {
		return "NULL"
	}
	return "'" + value + "'"
}

func (f *outcomeFixture) refresh() {
	f.t.Helper()
	if err := f.catalog.refreshSessionOutcomes(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

// labels reads each session's label, its evidence and rework.
func (f *outcomeFixture) labels() map[string][3]string {
	f.t.Helper()
	rows, err := queryMaps(f.catalog.DB, `SELECT w.id,`+outcomeLabelSQL("w")+` label,`+outcomeWhySQL("w")+` why,`+outcomeFactSQL("w", "so.reworked_files")+` reworked FROM workspaces w`)
	if err != nil {
		f.t.Fatal(err)
	}
	result := map[string][3]string{}
	for _, row := range rows {
		result[firstString(row["id"])] = [3]string{firstString(row["label"]), firstString(row["why"]), fmt.Sprint(row["reworked"])}
	}
	return result
}

func TestSessionOutcomeLabels(t *testing.T) {
	f := newOutcomeFixture(t)
	for _, id := range []string{"research", "kept", "recent", "reworked", "reworker", "light", "light-later", "continued", "continuer", "slow", "slow-later",
		"mirrored", "mirror", "tree-a", "tree-b", "rewritten", "rewriter", "opaque"} {
		f.session(id, "claude")
	}
	f.session("chat", "chatgpt-export")
	execTx(t, f.catalog.DB, `INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status,started_at)
		VALUES('read','research','research-c',1,'claude','tool','Read','read','ok','`+daysAgo(30)+`')`)
	f.edit("kept", "/notes/kept.md", "", "", 60, "", code("kept", 0, 5))
	f.edit("recent", "/notes/recent.md", "", "", 1, "", code("recent", 0, 5))
	// A later session removes 8 of the 12 lines added.
	f.edit("reworked", "/notes/plan.md", "", "", 60, "", code("plan", 0, 10))
	f.edit("reworked", "/notes/notes.md", "", "", 60, "", code("notes", 0, 2))
	f.edit("reworker", "/notes/plan.md", "", "", 55, code("plan", 0, 8), code("draft", 0, 2))
	// A later session removes 5 of 300 lines: rework, but light.
	f.edit("light", "/notes/a.md", "", "", 60, "", code("a", 0, 100))
	f.edit("light", "/notes/b.md", "", "", 60, "", code("b", 0, 100))
	f.edit("light", "/notes/c.md", "", "", 60, "", code("c", 0, 100))
	f.edit("light-later", "/notes/a.md", "", "", 58, code("a", 0, 5), "")
	// A later session that keeps building on the same file, around its
	// lines, reworks nothing.
	f.edit("continued", "/notes/next.md", "", "", 60, "", code("next", 0, 10))
	f.edit("continuer", "/notes/next.md", "", "", 59, code("next", 9, 1), code("next", 9, 1)+"\n"+code("more", 0, 40))
	f.edit("continuer", "/notes/next.md", "", "", 59, code("next", 0, 1), "  "+code("next", 0, 1))
	// Removed after the window closed.
	f.edit("slow", "/notes/slow.md", "", "", 60, "", code("slow", 0, 10))
	f.edit("slow-later", "/notes/slow.md", "", "", 30, code("slow", 0, 10), "")
	// A mirror repeats its workspace's edits; it is not rework.
	f.edit("mirrored", "/notes/mirror.md", "", "", 60, "", code("mirror", 0, 10))
	f.edit("mirror", "/notes/mirror.md", "", "", 59, code("mirror", 0, 10), "")
	execTx(t, f.catalog.DB, `INSERT INTO tool_mirror_workspaces VALUES('mirror')`)
	// Two worktrees of one repository edit the same file.
	f.edit("tree-a", "/src/app/main.go", "repo", "main.go", 60, "", code("main", 0, 10))
	f.edit("tree-b", "/src/app-feature/main.go", "repo", "main.go", 59, code("main", 0, 9), code("main", 20, 3))
	// A later session writes the file whole, keeping 2 of its 10 lines.
	f.edit("rewritten", "/notes/whole.md", "", "", 60, "", code("whole", 0, 10))
	f.write("rewriter", "/notes/whole.md", 59, code("whole", 8, 2)+"\n"+code("fresh", 0, 5))
	// An edit whose input was not recorded cannot be judged.
	f.call("opaque", "/notes/opaque.md", "", "", 60, "Edit", nil, 3, 0)
	f.refresh()
	labels := f.labels()
	for id, want := range map[string]string{
		"chat": "unknown", "research": "no_changes", "kept": "changed_kept", "recent": "changed_recent", "reworked": "reworked", "reworker": "changed_kept",
		"light": "changed_kept", "continued": "changed_kept", "slow": "changed_kept", "mirrored": "changed_kept", "tree-a": "reworked", "rewritten": "reworked",
		"opaque": "changed_kept",
	} {
		if labels[id][0] != want {
			t.Errorf("%s: %s, want %s (%s)", id, labels[id][0], want, labels[id][1])
		}
	}
	for id, want := range map[string]string{
		"chat":      "This source records no tool calls, so its changes are unknown.",
		"research":  "No files edited (transcript).",
		"reworked":  "Later sessions removed 8 of the 12 lines it added within 14 days (transcript).",
		"light":     "3 files changed; later sessions removed 5 of the 300 lines it added within 14 days (transcript).",
		"continued": "1 file changed; no later session removed its lines within 14 days (transcript).",
		"rewritten": "Later sessions removed 8 of the 10 lines it added within 14 days (transcript).",
		"opaque":    "1 file changed; its edits could not be read line by line, so rework was not judged (transcript).",
		"recent":    "1 file changed; rework can be judged after " + time.Now().UTC().Add(13*24*time.Hour).Format("2006-01-02") + " (transcript).",
	} {
		if labels[id][1] != want {
			t.Errorf("%s evidence: %q, want %q", id, labels[id][1], want)
		}
	}
	if labels["light"][2] != "1" || labels["continued"][2] != "0" {
		t.Errorf("reworked files: %v %v", labels["light"], labels["continued"])
	}
}

func TestSessionOutcomesJudgeAgainWhenSessionsChange(t *testing.T) {
	f := newOutcomeFixture(t)
	f.session("first", "claude")
	f.edit("first", "/notes/plan.md", "", "", 20, "", code("plan", 0, 10))
	f.refresh()
	if label := f.labels()["first"][0]; label != "changed_kept" {
		t.Fatalf("before: %s", label)
	}
	// A later session reworks the file: only it is marked, and the earlier
	// session sharing its file is judged again.
	f.session("second", "codex")
	f.edit("second", "/notes/plan.md", "", "", 18, code("plan", 0, 9), "")
	var marked int
	if err := f.catalog.DB.QueryRow("SELECT COUNT(*) FROM session_outcome_dirty").Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("marked %d, %v", marked, err)
	}
	f.refresh()
	if label := f.labels()["first"][0]; label != "reworked" {
		t.Fatalf("after a later session: %s", label)
	}
	// Removing the later session undoes its rework.
	execTx(t, f.catalog.DB, `DELETE FROM workspaces WHERE id='second'`)
	f.refresh()
	if label := f.labels()["first"][0]; label != "changed_kept" {
		t.Fatalf("after removing it: %s", label)
	}
	var files int
	if err := f.catalog.DB.QueryRow("SELECT COUNT(*) FROM session_files WHERE workspace_id='second'").Scan(&files); err != nil || files != 0 {
		t.Fatalf("the removed session's files remain: %d %v", files, err)
	}

	// A shorter window judges every session again: the rework 2 days later
	// falls outside a 1-day window.
	f.session("third", "claude")
	f.edit("third", "/notes/plan.md", "", "", 18, code("plan", 0, 9), "")
	f.refresh()
	if label := f.labels()["first"][0]; label != "reworked" {
		t.Fatalf("with a 14-day window: %s", label)
	}
	f.catalog.ReworkDays = 1
	f.refresh()
	if label := f.labels()["first"]; label[0] != "changed_kept" || label[2] != "0" {
		t.Fatalf("with a 1-day window: %v", label)
	}
}

func TestReadEditChange(t *testing.T) {
	read := func(input any, path string) (editChange, bool) {
		text, _ := json.Marshal(map[string]any{"input": input})
		return readEditChange(string(text), path)
	}
	fingerprints := func(text string) lineSet {
		set := lineSet{}
		fingerprintLines(text, set)
		return set
	}
	same := func(name string, got lineSet, want string) {
		t.Helper()
		expected := fingerprints(want)
		if len(got) != len(expected) {
			t.Errorf("%s: %d lines, want %d", name, len(got), len(expected))
		}
		for line := range expected {
			if !hasLine(got, line) {
				t.Errorf("%s: missing a line of %q", name, want)
			}
		}
	}
	// Short lines say nothing; a line both removed and added only moved.
	change, ok := read(map[string]any{"file_path": "/a.go", "old_string": "\tif done {\n\t\treturn total\n\t}", "new_string": "\tif done {\n\t\treturn total + 1\n\t}\n// counted twice"}, "/a.go")
	if !ok || change.Rewrite {
		t.Fatalf("edit: %+v", change)
	}
	same("edit added", change.Added, "return total + 1\n// counted twice")
	same("edit removed", change.Removed, "return total")

	change, _ = read(map[string]any{"file_path": "/a.go", "edits": []any{map[string]any{"old_string": "first line here", "new_string": "first line there"}, map[string]any{"old_string": "", "new_string": "another line"}}}, "/a.go")
	same("multi-edit added", change.Added, "first line there\nanother line")
	change, _ = read(map[string]any{"TargetFile": "/a.go", "TargetContent": "the old content", "ReplacementContent": "the new content"}, "/a.go")
	same("antigravity removed", change.Removed, "the old content")
	change, _ = read(map[string]any{"file_path": "/a.go", "content": "package main\nfunc main() {}"}, "/a.go")
	if !change.Rewrite {
		t.Fatalf("write: %+v", change)
	}
	same("write", change.Added, "package main\nfunc main() {}")

	// A patch changes only the sections of this call's file, named absolutely
	// or relative to the working directory, also inside a code-mode script.
	patch := "*** Begin Patch\n*** Update File: src/a.go\n@@\n-old line of a\n+new line of a\n*** Add File: /repo/src/b.go\n+a line of file b\n*** End Patch"
	for name, input := range map[string]any{
		"patch":       patch,
		"patch field": map[string]any{"patch": patch},
		"code mode":   "text(await tools.apply_patch(" + fmt.Sprintf("%q", patch) + "))",
	} {
		change, ok = read(input, "/repo/src/a.go")
		if !ok {
			t.Fatalf("%s: unread", name)
		}
		same(name+" added", change.Added, "new line of a")
		same(name+" removed", change.Removed, "old line of a")
	}
	if change, ok = read(patch, "/repo/src/c.go"); ok {
		t.Fatalf("a patch of other files: %+v", change)
	}
	if _, ok = read(map[string]any{"changes": []any{map[string]any{"kind": "update", "path": "/a.go"}}}, "/a.go"); ok {
		t.Fatalf("a change without its lines")
	}
}

func TestSessionOutcomesReadCommitsFromToolOutput(t *testing.T) {
	f := newOutcomeFixture(t)
	f.session("work", "claude")
	f.edit("work", "/src/app/main.go", "", "", 20, "", code("main", 0, 4))
	at := daysAgo(19)
	execTx(t, f.catalog.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('result','work-c','result','tool','tool_result','[main 0123abc] Fix the parser','x','h')`,
		`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,command,command_category,status,started_at,result_message_id)
			VALUES('commit','work','work-c',99,'claude','tool','Bash','command','git commit -am "Fix the parser"','vcs','ok','`+at+`','result')`)
	f.refresh()
	var commits string
	var verification sql.NullString
	if err := f.catalog.DB.QueryRow("SELECT commits_json,verification FROM session_outcomes WHERE workspace_id='work'").Scan(&commits, &verification); err != nil {
		t.Fatal(err)
	}
	if commits != `[{"hash":"0123abc","branch":"main","at":"`+at+`"}]` || verification.String != "not_run" {
		t.Fatalf("facts: %s %v", commits, verification)
	}
	// A commit recorded only in the transcript is a Tier 1 fact; the label
	// stays a transcript label until local Git confirms it.
	if label := f.labels()["work"][0]; label != "changed_kept" {
		t.Fatalf("label: %s", label)
	}
}
