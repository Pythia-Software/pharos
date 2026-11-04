package archive

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
)

// An agent's route through the outcome tools, in a repository and in a
// plain folder: find its project from its working directory, find the
// sessions that edited a file through their tool calls, and read what became
// of them.
func TestOutcomeToolsForAgents(t *testing.T) {
	library := mixedLibrary(t)
	app, notes := library.folders["app"], library.folders["notes"]
	writeClaudeSession(t, library.claude, "s-app-edit", claudeEditSession("s-app-edit", app, filepath.Join(app, "cmd", "serve.go"), []string{"package main", "", "func serve() {}"}, 30))
	writeClaudeSession(t, library.claude, "s-notes-edit", claudeEditSession("s-notes-edit", notes, filepath.Join(notes, "serve-plan.md"), []string{"# Serving", "- one"}, 31))
	library.ingestClaude(t)
	ctx := context.Background()
	if err := library.catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := library.catalog.refreshSessionGit(ctx); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, row := range queryRows(t, library.catalog, `SELECT id,source_id FROM workspaces`) {
		ids[filepath.Base(firstString(row["source_id"]))] = firstString(row["id"])
	}

	// A working directory below any session's, in a repository or a folder,
	// is in its project.
	for path, want := range map[string]string{filepath.Join(app, "cmd", "deeper"): "app", filepath.Join(notes, "drafts"): folderKey(notes)} {
		found := callProjectTool(t, library.catalog, "list_projects", map[string]any{"path": path})
		if items := projectItems(found); len(items) != 1 || items[want] == nil {
			t.Fatalf("list_projects path=%s: %#v", path, found)
		}
	}

	// Claude Code records no change sets: every file filter finds its edits
	// from its tool calls.
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"search_conversations", map[string]any{"file": "cmd/serve.go"}},
		{"search_work", map[string]any{"file": "cmd/serve.go"}},
		{"trace", map[string]any{"file": "cmd/serve.go"}},
		{"query_statistics", map[string]any{"file": "cmd/serve.go"}},
		{"search_conversations", map[string]any{"file": "serve-plan.md", "project": notes}},
	} {
		result := callProjectTool(t, library.catalog, call.tool, call.args)
		if items, _ := result["items"].([]any); len(items) != 1 {
			t.Fatalf("%s %v: %#v", call.tool, call.args, result)
		}
	}

	// Outcome labels filter and group the statistics, and the search tools.
	grouped := callProjectTool(t, library.catalog, "query_statistics", map[string]any{"result_type": "aggregate",
		"aggregations": []any{map[string]any{"id": "n", "op": "count"}}, "group_by": []any{"project", "outcome_label"}})
	counts := map[string]int64{}
	for _, item := range grouped["groups"].([]any) {
		group := item.(map[string]any)
		key := group["keys"].(map[string]any)
		counts[firstString(key["project"])+" "+firstString(key["outcome_label"])] = integer(group["values"].(map[string]any)["n"])
	}
	if counts["app changed_kept"] != 1 || counts[folderKey(notes)+" changed_kept"] != 1 || counts["app no_changes"] != 2 {
		t.Fatalf("statistics by project and outcome: %#v", counts)
	}
	kept := callProjectTool(t, library.catalog, "search_work", map[string]any{"outcome_label": "changed_kept", "project": "app"})
	if items := kept["items"].([]any); len(items) != 1 {
		t.Fatalf("search_work outcome_label: %#v", kept)
	}
	if _, err := callMCP(library.catalog, "query_statistics", map[string]any{"outcome_label": "merged"}); err == nil {
		t.Fatal("an unknown outcome label should be an error")
	}

	// A session's outcome, edited files and commit links are work sections.
	outcome := callProjectTool(t, library.catalog, "get_work_detail", map[string]any{"workspace_id": ids["s-app-edit"], "section": "session_outcome"})
	row := outcome["items"].([]any)[0].(map[string]any)
	if row["outcome_label"] != "changed_kept" || row["project"] != "app" || firstString(row["outcome_why"]) == "" || row["git_check"] == nil {
		t.Fatalf("session_outcome: %#v", outcome)
	}
	edited := callProjectTool(t, library.catalog, "get_work_detail", map[string]any{"workspace_id": ids["s-app-edit"], "section": "edited_files"})
	if items := edited["items"].([]any); len(items) != 1 || items[0].(map[string]any)["path"] != "cmd/serve.go" || integer(items[0].(map[string]any)["lines_added"]) != 3 {
		t.Fatalf("edited_files: %#v", edited)
	}
	if links := callProjectTool(t, library.catalog, "get_work_detail", map[string]any{"workspace_id": ids["s-app-edit"], "section": "commit_links"}); integer(links["total"]) != 0 {
		t.Fatalf("commit_links without commits: %#v", links)
	}
}

// The skill agents install names only tools the server registers, and leads
// them to the project, outcome and file tools.
func TestSearchSkillNamesRegisteredTools(t *testing.T) {
	skill, err := assets.ReadFile("assets/skills/search-prior-coding-sessions/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, tool := range mcpTools {
		registered[firstString(tool["name"])] = true
	}
	named := map[string]bool{}
	for _, match := range regexp.MustCompile("`((?:list|get|search|query|trace|browse|site)_[a-z_]+|trace)`").FindAllStringSubmatch(string(skill), -1) {
		named[match[1]] = true
		if !registered[match[1]] {
			t.Errorf("the skill names %s, which the server does not register", match[1])
		}
	}
	for _, tool := range []string{"list_projects", "get_file_history", "list_outcomes", "get_branch_spend", "query_statistics"} {
		if !named[tool] {
			t.Errorf("the skill never names %s", tool)
		}
	}
}
