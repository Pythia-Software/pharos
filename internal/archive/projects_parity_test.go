package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// The parity check of docs/projects-and-outcomes.md: every route and MCP tool
// that groups or filters by project answers a library without Git as fully as
// one with it, and keeps "no project" apart from a folder.

// projectDatasets are the query tables that carry project_name.
var projectDatasets = []string{"library", "usage", "agent_events", "agent_event_messages", "conversation_activity", "skill_usages",
	"writing", "writing_messages", "tool_calls", "tools", "rhythm", "parallelism", "model_requests", "messages", "compactions",
	"agent_event_rates", "session_shape", "message_labels", "people_message_labels", "outcomes", "branch_usage"}

// projectDatasetsWithoutRows are the project datasets the fixtures' short
// sessions leave empty: they need skills, or the agent event pass.
var projectDatasetsWithoutRows = []string{"agent_events", "agent_event_messages", "agent_event_rates", "skill_usages"}

// projectDatasetsOfRepositories have rows only for repository sessions, so a
// library without Git leaves them empty.
var projectDatasetsOfRepositories = []string{"branch_usage"}

// projectServer serves a fixture library with its tool summaries and writing
// classification built and local Git asked, as the background builds leave
// them.
func projectServer(t *testing.T, library projectLibrary) *Server {
	t.Helper()
	if err := library.catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	want, _, err := library.catalog.authorshipGenerations()
	if err != nil {
		t.Fatal(err)
	}
	if err := library.catalog.RebuildAuthorship(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if err := library.catalog.refreshSessionGit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return testServer(t, library.config, library.catalog)
}

// queryProjects posts a query for project_name to a dataset and returns its rows.
func queryProjects(t *testing.T, server *Server, dataset string, fields ...string) []map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"select": append([]string{"project_name"}, fields...), "limit": 100, "offset": 0})
	request := httptest.NewRequest(http.MethodPost, "/api/query/"+dataset, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", dataset, response.Code, response.Body.String())
	}
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("%s: %v", dataset, err)
	}
	return result.Rows
}

// addProjectWork appends a model request that reads a file, its result and a
// compaction to each named session of the library (in its folder of the same name), so
// the usage, tool, request and compaction datasets have rows to check.
func addProjectWork(t *testing.T, library projectLibrary, folders ...string) {
	t.Helper()
	for index, folder := range folders {
		session, cwd := "s-"+folder, library.folders[folder]
		path := filepath.Join(library.claude, "project", session+".jsonl")
		existing, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeSourceFile(t, path, string(existing)+
			fmt.Sprintf(`{"type":"assistant","uuid":"%s-w","sessionId":%q,"cwd":%q,"timestamp":"2026-09-02T00:%02d:00Z","message":{"id":"%s-m","role":"assistant","model":"claude-test","usage":{"input_tokens":100,"output_tokens":20},`+
				`"content":[{"type":"tool_use","id":"%s-t","name":"Read","input":{"file_path":"README.md"}}]}}`+"\n", session, session, cwd, index, session, session)+
			fmt.Sprintf(`{"type":"user","uuid":"%s-r","sessionId":%q,"cwd":%q,"timestamp":"2026-09-02T00:%02d:10Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"%s-t","content":"# readme"}]}}`+"\n", session, session, cwd, index, session)+
			fmt.Sprintf(`{"type":"system","subtype":"compact_boundary","uuid":"%s-c","sessionId":%q,"cwd":%q,"timestamp":"2026-09-02T00:%02d:20Z"}`+"\n", session, session, cwd, index))
	}
	library.ingestClaude(t)
}

// checkDatasetProjects queries project_name from every project dataset and
// checks that each dataset with rows names only the fixture's projects (or
// none) and at least one of them. A dataset whose rows the fixture's short
// transcripts do not produce must say so in projectDatasetsWithoutRows.
func checkDatasetProjects(t *testing.T, server *Server, library projectLibrary, projects ...string) {
	t.Helper()
	for _, dataset := range projectDatasets {
		rows := queryProjects(t, server, dataset)
		if len(rows) == 0 {
			_, repositories := library.folders["app"]
			if !slices.Contains(projectDatasetsWithoutRows, dataset) && (repositories || !slices.Contains(projectDatasetsOfRepositories, dataset)) {
				t.Errorf("%s: no rows from the fixture library", dataset)
			}
			continue
		}
		named := 0
		for _, row := range rows {
			if name := firstString(row["project_name"]); name != "" {
				if !slices.Contains(projects, name) {
					t.Errorf("%s: project %q is not one of %q", dataset, name, projects)
				}
				named++
			}
		}
		if named == 0 {
			t.Errorf("%s: %d rows and none names a project", dataset, len(rows))
		}
	}
}

func callProjectTool(t *testing.T, catalog *Catalog, name string, args map[string]any) map[string]any {
	t.Helper()
	value, err := callMCP(catalog, name, args)
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	data, _ := json.Marshal(value)
	result := map[string]any{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func projectItems(result map[string]any) map[string]map[string]any {
	items := map[string]map[string]any{}
	for _, item := range result["items"].([]any) {
		row := item.(map[string]any)
		items[firstString(row["project"])] = row
	}
	return items
}

func TestProjectParityWithoutGit(t *testing.T) {
	library := noGitLibrary(t)
	addProjectWork(t, library, "notes", "research", "later")
	server := projectServer(t, library)
	notes, research := folderKey(library.folders["notes"]), folderKey(library.folders["research"])
	checkDatasetProjects(t, server, library, notes, research, folderKey(library.folders["later"]))
	kinds := map[string]int{}
	for _, row := range queryProjects(t, server, "library", "project_kind") {
		kinds[firstString(row["project_name"])+"|"+firstString(row["project_kind"])]++
	}
	// The session without a folder and the ChatGPT export have no project.
	if kinds[notes+"|folder"] != 1 || kinds["|none"] != 2 || len(kinds) != 4 {
		t.Fatalf("library projects: %#v", kinds)
	}
	// Every session has an outcome, and no Git count reads as zero outside a
	// repository, nor as not checked: local Git does not apply. The Claude
	// sessions edited nothing, and the export cannot tell.
	outcomes := map[string]int{}
	for _, row := range queryProjects(t, server, "library", "project_kind", "outcome_label", "pr_count", "branch_count", "source_kind",
		"git_check", "git_check_reason", "linked_commits", "landed_commits") {
		if row["pr_count"] != nil || row["branch_count"] != nil || row["linked_commits"] != nil || row["landed_commits"] != nil {
			t.Fatalf("a Git count outside a repository: %#v", row)
		}
		if row["git_check"] != "not_applicable" || row["git_check_reason"] != nil {
			t.Fatalf("local Git outside a repository: %#v", row)
		}
		outcomes[firstString(row["source_kind"])+"|"+firstString(row["outcome_label"])]++
	}
	if outcomes["claude|no_changes"] != 4 || outcomes["chatgpt|unknown"] != 1 {
		t.Fatalf("library outcomes: %#v", outcomes)
	}
	// A count shown without the columns it is read with still carries them.
	request := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Result: qt.WireQuery{Select: []string{"id", "linked_commits"}, Limit: 50}}
	w := apiCall(t, server, library.config.APIToken, http.MethodPost, "/api/query/library/query-v3", request)
	var shown qt.QueryResultV3
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &shown) != nil || len(shown.Rows) == 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, row := range shown.Rows {
		if _, ok := row["project_kind"]; !ok || row["git_check"] != "not_applicable" {
			t.Fatalf("a linked commit count without what it reads: %#v", row)
		}
	}
	// Analyze → Outcomes counts the same sessions with the same labels, from
	// their transcripts: no branch, and no session at the local Git tier.
	analyzed := map[string]int{}
	for _, row := range queryProjects(t, server, "outcomes", "source_kind", "outcome_label", "evidence_tier", "git_check", "branch") {
		if row["branch"] != nil || row["git_check"] != "not_applicable" || row["evidence_tier"] == "local_git" {
			t.Fatalf("Git facts outside a repository: %#v", row)
		}
		analyzed[firstString(row["source_kind"])+"|"+firstString(row["outcome_label"])]++
	}
	if !maps.Equal(analyzed, outcomes) {
		t.Fatalf("outcomes dataset %#v, library %#v", analyzed, outcomes)
	}
	// Spend by branch is for Git sessions only: without Git it has no rows,
	// and none of them reads as a zero branch.
	if rows := queryProjects(t, server, "branch_usage"); len(rows) != 0 {
		t.Fatalf("spend by branch without Git: %#v", rows)
	}
	// Agents reach the dashboard and its values through the site tools.
	link, _, err := callSiteMCP(context.Background(), library.catalog, "site_link", map[string]any{"page": "analyze/outcomes", "options": map[string]any{"view": "compare", "metric": "cost_per_kept_change", "split": "evidence_tier"}})
	if err != nil || !strings.Contains(link.(map[string]any)["url"].(string), "analyze_topic=outcomes") {
		t.Fatalf("site_link outcomes: %v %v", link, err)
	}
	if values, _ := browseText(t, library.catalog, map[string]any{"path": "analyze/outcomes/filters/outcome_label"}); len(values["items"].([]any)) == 0 {
		t.Fatalf("outcome_label values: %v", values)
	}
	usage := map[string]bool{}
	for _, row := range queryProjects(t, server, "rhythm") {
		usage[firstString(row["project_name"])] = true
	}
	if !usage[notes] || !usage[research] {
		t.Fatalf("rhythm projects: %#v", usage)
	}

	projects := callProjectTool(t, library.catalog, "list_projects", map[string]any{})
	items := projectItems(projects)
	if len(items) != 3 || items[notes]["kind"] != "folder" || integer(items[notes]["conversations"]) != 1 {
		t.Fatalf("list_projects: %#v", projects)
	}
	// The session without a folder and the ChatGPT export.
	if integer(projects["conversations_without_project"]) != 2 {
		t.Fatalf("conversations without a project: %#v", projects["conversations_without_project"])
	}
	if count := countRows(t, library.catalog, `SELECT COUNT(*) FROM session_git`) + countRows(t, library.catalog, `SELECT COUNT(*) FROM git_repository_scans`); count != 0 {
		t.Fatalf("local Git ran without a repository: %d rows", count)
	}
	if repositories := callProjectTool(t, library.catalog, "list_repositories", map[string]any{}); integer(repositories["total"]) != 0 {
		t.Fatalf("list_repositories without Git: %#v", repositories)
	}
	for _, project := range []string{notes, library.folders["notes"], "notes"} {
		found := callProjectTool(t, library.catalog, "search_conversations", map[string]any{"project": project})
		if items := found["items"].([]any); len(items) != 1 {
			t.Fatalf("search_conversations project=%q: %#v", project, found)
		}
	}
	if messages := callProjectTool(t, library.catalog, "search_messages", map[string]any{"query": "papers", "project": research}); integer(messages["total"]) != 1 {
		t.Fatalf("search_messages by project: %#v", messages)
	}
	if text := callProjectTool(t, library.catalog, "search_text", map[string]any{"query": "papers", "project": notes}); len(text["items"].([]any)) != 0 {
		t.Fatalf("search_text by another project: %#v", text)
	}
	grouped := callProjectTool(t, library.catalog, "query_statistics", map[string]any{"entity": "conversation", "result_type": "aggregate",
		"aggregations": []any{map[string]any{"id": "n", "op": "count"}}, "group_by": []any{"project"}})
	if groups := grouped["groups"].([]any); len(groups) != 4 {
		t.Fatalf("query_statistics by project, three folders and none: %#v", grouped)
	}
	rows := callProjectTool(t, library.catalog, "query_statistics", map[string]any{"entity": "conversation", "project": notes, "select": []any{"project"}})
	if items := rows["items"].([]any); len(items) != 1 || items[0].(map[string]any)["project"] != notes {
		t.Fatalf("query_statistics select project: %#v", rows)
	}
	if _, err := callMCP(library.catalog, "search_conversations", map[string]any{"project": "~/nowhere-at-all"}); err == nil || !strings.Contains(err.Error(), "list_projects") {
		t.Fatalf("an unknown project should be an error naming list_projects: %v", err)
	}

	// A session that wrote a file: its outcome and the file's history come
	// from the transcript alone, with nothing reported missing.
	plan := filepath.Join(library.folders["notes"], "plan.md")
	writeClaudeSession(t, library.claude, "s-plan", claudeEditSession("s-plan", library.folders["notes"], plan, []string{"# Plan", "", "- one", "- two"}, 20))
	library.ingestClaude(t)
	if err := library.catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	listed := callProjectTool(t, library.catalog, "list_outcomes", map[string]any{"project": notes})
	if integer(listed["total"]) != 2 {
		t.Fatalf("list_outcomes in a folder: %#v", listed)
	}
	for _, item := range listed["items"].([]any) {
		row := item.(map[string]any)
		if row["git_check"] != "not_applicable" || row["git_check_reason"] != nil || firstString(row["outcome_why"]) == "" || row["outcome_label"] == "unknown" {
			t.Fatalf("an outcome without Git: %#v", row)
		}
	}
	// The project is named as any project filter takes it: its name, its
	// folder's absolute path, or part of it.
	for _, project := range []string{library.folders["notes"], "notes"} {
		if files := callProjectTool(t, library.catalog, "get_file_history", map[string]any{"path": "plan.md", "project": project})["files"].([]any); len(files) != 1 {
			t.Fatalf("get_file_history project=%q: %#v", project, files)
		}
	}
	history := callProjectTool(t, library.catalog, "get_file_history", map[string]any{"path": "plan.md", "project": notes})
	files := history["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("get_file_history without Git: %#v", history)
	}
	file := files[0].(map[string]any)
	session := file["sessions"].([]any)[0].(map[string]any)
	if file["project"] != notes || file["git"].(map[string]any)["status"] != "not_applicable" || integer(session["lines_added"]) != 4 ||
		!strings.Contains(firstString(session["url"]), "call=") || strings.Contains(firstString(history["note"]), "No workspace") {
		t.Fatalf("file history without Git: %#v", history)
	}
	// Another project hides the file: the answer says it is edited
	// elsewhere, not that nobody edited it.
	hidden := callProjectTool(t, library.catalog, "get_file_history", map[string]any{"path": "plan.md", "project": research})
	if len(hidden["files"].([]any)) != 0 || !strings.Contains(firstString(hidden["note"]), "other projects") {
		t.Fatalf("get_file_history in another project: %#v", hidden)
	}
	// The endpoint answers as the tool does, and a project that names
	// nothing is an error naming list_projects, as for every project filter.
	for _, check := range []struct {
		project string
		code    int
		want    string
	}{{notes, http.StatusOK, `"not_applicable"`}, {"~/nowhere-at-all", http.StatusBadRequest, "list_projects"}} {
		request := httptest.NewRequest(http.MethodGet, "/api/file-history?path=plan.md&project="+url.QueryEscape(check.project), nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != check.code || !strings.Contains(response.Body.String(), check.want) {
			t.Fatalf("file history in %s: %d %s", check.project, response.Code, response.Body.String())
		}
	}
}

func TestProjectParityMixed(t *testing.T) {
	library := mixedLibrary(t)
	addProjectWork(t, library, "notes", "app")
	server := projectServer(t, library)
	checkDatasetProjects(t, server, library, "app", "gone", folderKey(library.folders["notes"]), folderKey(library.folders["research"]), folderKey(library.folders["later"]))
	items := projectItems(callProjectTool(t, library.catalog, "list_projects", map[string]any{}))
	// The session in app/docs joins the app repository it is checked out in.
	if items["app"]["kind"] != "repository" || integer(items["app"]["conversations"]) != 2 || items[folderKey(library.folders["notes"])]["kind"] != "folder" {
		t.Fatalf("mixed list_projects: %#v", items)
	}
	if folders := callProjectTool(t, library.catalog, "list_projects", map[string]any{"kind": "folder"}); integer(folders["total"]) != 3 {
		t.Fatalf("folder projects: %#v", folders)
	}
	found := callProjectTool(t, library.catalog, "search_conversations", map[string]any{"project": "app"})
	if items := found["items"].([]any); len(items) != 2 {
		t.Fatalf("search_conversations project=app: %#v", found)
	}
	// A repository checked out inside the notes folder is its own project:
	// the notes folder's name does not reach into it.
	tracker := filepath.Join(library.folders["notes"], "tracker")
	gitInit(t, tracker)
	writeClaudeSession(t, library.claude, "s-tracker", claudeSession("s-tracker", tracker, "main", "Track the notes", 20))
	library.ingestClaude(t)
	if items := projectItems(callProjectTool(t, library.catalog, "list_projects", map[string]any{})); items["tracker"]["kind"] != "repository" {
		t.Fatalf("nested repository project: %#v", items)
	}
	for _, project := range []string{folderKey(library.folders["notes"]), library.folders["notes"]} {
		found := callProjectTool(t, library.catalog, "search_conversations", map[string]any{"project": project})
		if items := found["items"].([]any); len(items) != 1 {
			t.Fatalf("search_conversations project=%q reached the nested repository: %#v", project, found)
		}
		grouped := callProjectTool(t, library.catalog, "query_statistics", map[string]any{"entity": "conversation", "result_type": "aggregate", "project": project,
			"aggregations": []any{map[string]any{"id": "n", "op": "count"}}, "group_by": []any{"project"}})
		if groups := grouped["groups"].([]any); len(groups) != 1 {
			t.Fatalf("query_statistics project=%q: %#v", project, grouped)
		}
	}
}
