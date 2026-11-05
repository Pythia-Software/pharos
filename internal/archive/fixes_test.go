package archive

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// fixesFixture builds in-scope events: one per (workspace, sentence).
type fixesFixture struct {
	events []fixesEvent
	next   int64
}

func (f *fixesFixture) add(family, repository, workspace, day, sentence string) fixesEvent {
	f.next++
	e := fixesEvent{ID: f.next, MessageID: fmt.Sprintf("m%d", f.next), ConversationID: "c-" + workspace, Workspace: workspace, Repository: repository, RepositoryName: repository,
		Model: "model-a", Provider: "claude", Family: family, Source: "reply", Sentence: sentence, Subject: sentence, At: day + "T12:00:00Z", Day: day, Probability: 0.9 - float64(f.next)/1000}
	f.events = append(f.events, e)
	return e
}

// fixesNoise is unrelated friction, so that specific terms are rare.
func (f *fixesFixture) noise(n int) {
	topics := []string{"the dashboard legend overlaps the axis labels", "a websocket reconnect loop in the sync client", "the invoice totals round half away from zero",
		"pagination cursors expire after an hour", "the importer skips empty spreadsheet rows", "thumbnail generation drops transparent pixels",
		"keyboard focus escapes the settings dialog", "the scheduler double counts daylight saving days", "a stale cache key in the profile endpoint",
		"translations miss plural forms for Polish", "the uploader retries forever on quota errors", "markdown tables lose their alignment row"}
	for i := 0; i < n; i++ {
		f.add("obstacle", "noise", fmt.Sprintf("noise-%d", i), "2026-10-01", fmt.Sprintf("Noticed that %s (case %d).", topics[i%len(topics)], i))
	}
}

func fixesFind(items []fixItem, term string) *fixItem {
	for i := range items {
		for _, e := range items[i].events {
			if strings.Contains(e.Sentence, term) {
				return &items[i]
			}
		}
		if strings.Contains(items[i].Title, term) {
			return &items[i]
		}
	}
	return nil
}

func TestFixesTokensFoldParaphrases(t *testing.T) {
	a, b := fixesTokens("The codegen run hit its ten-minute limit."), fixesTokens("The codegen run stopped at the 10 minutes limit.")
	if !containsString(a.terms, "10minute") || !containsString(b.terms, "10minute") {
		t.Fatalf("durations fold: %v / %v", a.terms, b.terms)
	}
	plain, path := fixesTokens("the flame-graph export"), fixesTokens("on branch gbdubs/tl1-agy")
	if !containsString(plain.terms, "flame graph") || containsString(path.terms, "tl1 agy") {
		t.Fatalf("plain hyphens split, paths keep theirs: %v / %v", plain.terms, path.terms)
	}
	// Cue phrases and discourse words are how every event of a kind is
	// worded, so they are not compared.
	cue := fixesTokens("Turns out the root cause was the trimesh import.")
	if containsString(cue.terms, "root") || containsString(cue.terms, "turns") || !containsString(cue.terms, "trimesh") {
		t.Fatalf("cues and discourse words are dropped: %v", cue.terms)
	}
	code := fixesTokens("Calling `loadWorkbook` failed again")
	if !containsString(code.codeWords, "loadworkbook") {
		t.Fatalf("code-like words count double: %v", code)
	}
}

func TestFixesGroupingAndDestination(t *testing.T) {
	f := &fixesFixture{}
	f.noise(40)
	// One problem in one repository, across four work items.
	for i, sentence := range []string{
		"Runtime mesh validation could not run because `trimesh` is not installed in this environment.",
		"Mesh validation is skipped: `trimesh` is not installed, so the runtime check cannot run.",
		"The runtime mesh validation needs `trimesh`, which is not installed here.",
		"Could not run mesh validation at runtime since `trimesh` is not installed.",
	} {
		f.add("env_friction", "corpus", fmt.Sprintf("corpus-%d", i), "2026-10-2"+fmt.Sprint(i), sentence)
	}
	// One problem in two repositories, evenly: the shared setup's to fix.
	for i, sentence := range []string{
		"The `gh pr edit` call failed on the deprecated Projects classic GraphQL API.",
		"`gh pr edit` hit the Projects classic deprecation in GraphQL again.",
		"Editing the PR body with `gh pr edit` fails because of the Projects classic GraphQL error.",
		"The Projects classic GraphQL error from `gh pr edit` stopped the body update.",
	} {
		repository := []string{"alpha", "beta"}[i%2]
		f.add("obstacle", repository, fmt.Sprintf("%s-%d", repository, i), "2026-10-28", sentence)
	}
	// The same words in one work item only: not recurring.
	for _, sentence := range []string{"The flamegraph exporter rejects `pprof` profiles from arm64 builds.", "The flamegraph exporter again rejects arm64 `pprof` profiles."} {
		f.add("obstacle", "corpus", "corpus-solo", "2026-10-28", sentence)
	}
	// About the request, and a safeguard workaround: left out.
	f.add("ambiguous_request", "corpus", "corpus-0", "2026-10-20", "The user's instructions conflict about the trimesh version.")
	f.add("assumption", "corpus", "corpus-0", "2026-10-20", "I assume the user wants the trimesh path kept.")
	f.add("circumventing_constraint", "corpus", "corpus-1", "2026-10-21", "I skipped the mesh test to get past the gate.")
	reads := map[[2]string]int{}
	for _, day := range []string{"2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-28"} {
		for _, repository := range []string{"corpus", "alpha", "beta"} {
			reads[[2]string{repository, day}] = 50
		}
	}
	input := fixesInput{Events: f.events, Reads: reads, End: "2026-10-28",
		Repositories: map[string]*fixesRepository{"corpus": {Name: "corpus", Guides: []fixesGuide{{Name: "AGENTS.md", Lines: []string{"# Corpus", "Install trimesh with `uv sync` before validating meshes."}}}, Setup: []string{"pyproject.toml"}}}}
	items, excluded, err := fixesBuild(input)
	if err != nil {
		t.Fatal(err)
	}
	if excluded["ambiguous_request"] != 1 || excluded["assumption"] != 1 {
		t.Fatalf("request events are left out: %v", excluded)
	}
	for _, item := range items {
		for _, e := range item.events {
			if e.Family == "circumventing_constraint" || e.Family == "ambiguous_request" {
				t.Fatalf("%s is never grouped into a fix: %s", e.Family, item.Title)
			}
		}
		if item.WorkItems < 2 {
			t.Fatalf("a fix recurs across work items: %+v", item.Title)
		}
	}
	mesh := fixesFind(items, "trimesh")
	if mesh == nil {
		t.Fatalf("the trimesh problem is one fix: %v", fixesTitles(items))
	}
	// Titles name the problem; the lead quotes the clearest member.
	if mesh.Title != "`trimesh` missing from corpus's environment" || !strings.Contains(mesh.Lead, "trimesh") || mesh.Quotes[0].Sentence != mesh.Lead {
		t.Fatalf("title %q, lead %q", mesh.Title, mesh.Lead)
	}
	if mesh.ScopeReason != "4 of its 4 events are in corpus, so the fix probably lives there." {
		t.Fatalf("the place's reason counts every event: %s", mesh.ScopeReason)
	}
	if mesh.WorkItems != 4 || mesh.Category != "env" || mesh.Target != "repo:corpus" || mesh.Scope != "repo" {
		t.Fatalf("trimesh: %d work items, %s, %s", mesh.WorkItems, mesh.Category, mesh.Target)
	}
	if !strings.Contains(mesh.DestWhere, "pyproject.toml") || !strings.Contains(mesh.DestWhere, "corpus/AGENTS.md") {
		t.Fatalf("an environment fix in a repository names its setup and guidance: %s", mesh.DestWhere)
	}
	if len(mesh.GuidanceHits) == 0 || mesh.GuidanceHits[0].File != "AGENTS.md" || mesh.GuidanceHits[0].Line != 2 {
		t.Fatalf("the current guidance already names trimesh: %+v", mesh.GuidanceHits)
	}
	if mesh.Safeguards != 1 {
		t.Fatalf("a safeguard workaround in its conversations links to Risky actions: %d", mesh.Safeguards)
	}
	gh := fixesFind(items, "gh pr edit")
	if gh == nil || gh.Scope != "global" || gh.Target != "global" || gh.Category != "tool" || gh.Title != "`gh pr edit` keeps failing for agents" {
		t.Fatalf("a problem spread over repositories is the global setup's: %+v", gh)
	}
	if fixesFind(items, "flamegraph") != nil {
		t.Fatalf("one work item is not a recurring problem")
	}
	if mesh.ID == gh.ID || mesh.ID == "" {
		t.Fatalf("stable distinct ids: %q %q", mesh.ID, gh.ID)
	}
	again, _, _ := fixesBuild(input)
	if fixesFind(again, "trimesh").ID != mesh.ID {
		t.Fatalf("ids are stable across rebuilds")
	}
}

func fixesTitles(items []fixItem) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, fmt.Sprintf("%s (%d)", item.Title, item.WorkItems))
	}
	return out
}

func TestFixesCategories(t *testing.T) {
	event := func(family, sentence string) fixesEvent { return fixesEvent{Family: family, Sentence: sentence} }
	for _, test := range []struct {
		source string
		events []fixesEvent
		want   string
	}{
		{"stale", []fixesEvent{event("stale_guidance", "README says to run make, but there is no Makefile")}, "stale"},
		{"friction", []fixesEvent{event("preexisting_failure", "The parity test is flaky and unrelated to my change"), event("obstacle", "Same flaky parity test")}, "flaky"},
		{"friction", []fixesEvent{event("obstacle", "The sandbox denied writing outside the worktree"), event("obstacle", "Read-only sandbox denied the go cache")}, "harness"},
		{"friction", []fixesEvent{event("env_friction", "node is not installed at the right version"), event("missing_capability", "npm is missing from PATH")}, "env"},
		{"learning", []fixesEvent{event("discovery", "The importer keys rows by sheet name"), event("discovery", "Rows are keyed by sheet name in the importer")}, "knowledge"},
		{"friction", []fixesEvent{event("obstacle", "The merge left a conflict in the generated map")}, "obstacle"},
	} {
		if got := fixesCategorize(test.source, test.events); got != test.want {
			t.Errorf("%s %v: got %s, want %s", test.source, test.events[0].Sentence, got, test.want)
		}
	}
	if !fixesAboutRequest("confusion", "I'm unsure what the user meant by fast") || fixesAboutRequest("confusion", "Unsure why the cache misses") {
		t.Fatal("confusion about the request is the person's, confusion about the code is a fix")
	}
}

func TestFixesStatusAndBounds(t *testing.T) {
	events := []fixesEvent{{Day: "2026-10-01", Repository: "r"}, {Day: "2026-10-02", Repository: "r"}, {Day: "2026-10-03", Repository: "r"}}
	reads := map[[2]string]int{{"r", "2026-10-01"}: 10, {"r", "2026-10-03"}: 10, {"r", "2026-10-20"}: 100}
	repository := &fixesRepository{Name: "r", Commits: []fixesCommit{{Repository: "r", SHA: "abcdef1234", Day: "2026-10-10", Subject: "Document trimesh"}},
		Added: func(string) []string { return []string{"Install `trimesh` with uv sync."} }}
	input := fixesInput{Reads: reads, End: "2026-10-20", Repositories: map[string]*fixesRepository{"r": repository}}
	status := fixesStatus(input, events, []string{"trimesh"})
	if status.State != "quiet-fixed" || status.DaysQuiet != 17 || status.Expected == nil || *status.Expected != 15 || len(status.CommitsSince) != 1 {
		t.Fatalf("quiet since a guidance change naming its key term: %+v", status)
	}
	if status := fixesStatus(input, events, []string{"unrelated"}); status.State != "quiet" {
		t.Fatalf("a commit that doesn't name a key term doesn't count: %+v", status)
	}
	input.Reads = map[[2]string]int{{"r", "2026-10-01"}: 10, {"r", "2026-10-03"}: 10, {"r", "2026-10-20"}: 1}
	if status := fixesStatus(input, events, nil); status.State != "unclear" {
		t.Fatalf("too little activity since: %+v", status)
	}
	input.End = "2026-10-04"
	if status := fixesStatus(input, events, nil); status.State != "recurring" {
		t.Fatalf("seen in the last three days: %+v", status)
	}

	bounds := fixesBounds{QuietDays: 7, HideDays: 30, MinWorkItems: 3}
	item := func(work, quiet int, hours float64) fixItem {
		return fixItem{WorkItems: work, Cost: fixCost{Hours: hours}, Status: fixStatus{State: "quiet", DaysQuiet: quiet}}
	}
	if p := fixesPriority(item(10, 2, 0), bounds); p != 10 {
		t.Fatalf("active: %v", p)
	}
	if p := fixesPriority(item(10, 10, 0), bounds); p != 6 {
		t.Fatalf("quiet past the bound ranks lower: %v", p)
	}
	if p := fixesPriority(item(10, 25, 1), bounds); p != 5 {
		t.Fatalf("quiet past three times the bound ranks lower again, time lost still counts: %v", p)
	}
	if fixesHidden(item(10, 31, 0), bounds) != "stale" || fixesHidden(item(2, 1, 0), bounds) != "rare" || fixesHidden(item(3, 30, 0), bounds) != "" {
		t.Fatal("bounds take stale and rare fixes off the menu")
	}
	if fixesHidden(item(10, 80, 0), fixesBounds{QuietDays: 7, HideDays: 0, MinWorkItems: 1}) != "" {
		t.Fatal("hide_days 0 keeps quiet fixes")
	}
}

// seedFixes puts a recurring problem in a repository into a catalog: four
// work items, each with one event, plus noise and a request event.
func seedFixes(t *testing.T, c *Catalog, day string, workItems int) {
	t.Helper()
	tx := seedTx(t, c.DB)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT OR IGNORE INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-corpus','corpus','2026-10-01','2026-10-01')`)
	sentences := []string{
		"Runtime mesh validation could not run because `trimesh` is not installed in this environment.",
		"Mesh validation is skipped: `trimesh` is not installed, so the runtime check cannot run.",
		"The runtime mesh validation needs `trimesh`, which is not installed here.",
		"Could not run mesh validation at runtime since `trimesh` is not installed.",
	}
	topics := []string{"the dashboard legend overlaps the axis labels", "a websocket reconnect loop in the sync client", "the invoice totals round half away from zero",
		"pagination cursors expire after an hour", "the importer skips empty spreadsheet rows", "thumbnail generation drops transparent pixels"}
	// TL1's Codex transcripts record no message times: the last work item's
	// event has none, only its conversation's start.
	untimed := false
	add := func(workspace, family, sentence, at string, n int) {
		var when any = at
		if untimed {
			when = nil
		}
		exec(`INSERT OR IGNORE INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES(?,'claude','local',?,'repo-corpus','Work',?)`, workspace, workspace, at)
		exec(`INSERT OR IGNORE INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES(?,?,'claude','local',?,?)`, "c-"+workspace, workspace, "c-"+workspace, at)
		message := fmt.Sprintf("m-%s-%d", workspace, n)
		exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES(?,?,?,'assistant','message',?,'h',?)`, message, "c-"+workspace, message, sentence, when)
		exec(`INSERT INTO agent_event_messages(message_id,conversation_id,source,occurred_at,day,candidates,events,model,lexicon,imported_at) VALUES(?,?,'reply',?,?,1,1,'m','l',?)`, message, "c-"+workspace, when, at[:10], at)
		exec(`INSERT INTO agent_events(message_id,conversation_id,source,occurred_at,day,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model)
 VALUES(?,?,'reply',?,?,?,0.9,0,?,?,?,'','[]','[]','m')`, message, "c-"+workspace, when, at[:10], family, len(sentence), sentence, sentence)
	}
	for i := 0; i < 30; i++ {
		add(fmt.Sprintf("noise-%s-%d", day, i), "obstacle", fmt.Sprintf("Noticed that %s (case %d).", topics[i%len(topics)], i), day+"T08:00:00Z", 0)
	}
	for i := 0; i < workItems; i++ {
		untimed = i == workItems-1
		add(fmt.Sprintf("corpus-%s-%d", day, i), "env_friction", sentences[i%len(sentences)], day+"T12:00:00Z", i)
	}
	untimed = false
	add("corpus-asks-"+day, "ambiguous_request", "The user's request about trimesh conflicts with the brief.", day+"T13:00:00Z", 0)
	commitSeed(t, tx)
}

func TestFixesProjectionTriageAndHandoff(t *testing.T) {
	c, config := testCatalog(t)
	override(t, &fixesRepositoryInfo, func(name string, _ []string, _ string) *fixesRepository {
		return &fixesRepository{Name: name, Guides: []fixesGuide{{Name: "CLAUDE.md", Lines: []string{"# Corpus"}}}, Setup: []string{"pyproject.toml"}}
	})
	override(t, &fixesGlobalGuides, func() []fixesGuide { return nil })
	seedFixes(t, c, "2026-10-20", 4)
	if err := c.refreshFixes(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	overview, err := c.FixesOverview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items := overview["items"].([]map[string]any)
	var mesh map[string]any
	for _, item := range items {
		if strings.Contains(firstString(item["title"]), "trimesh") {
			mesh = item
		}
	}
	if mesh == nil || mesh["target"] != "repo:repo-corpus" || mesh["state"] != "open" || mesh["hidden"] != "" {
		t.Fatalf("the projection lists the problem for its repository: %v", items)
	}
	if status := mesh["status"].(map[string]any); status["last"] != "2026-10-20" || status["state"] == "unknown" || mesh["work_items"] != 4 {
		t.Fatalf("an event without a message time takes its conversation's: %v", mesh)
	}
	if overview["excluded_events"] != 1 {
		t.Fatalf("the request event is counted as left out: %v", overview["excluded_events"])
	}
	id := firstString(mesh["id"])

	// Reading changes nothing; a prompt needs the item in its basket.
	entries, _, _ := loadFixes(t.Context(), c.reader())
	if _, err := fixesPreview(entries, "repo:repo-corpus", []string{id}, "propose"); err == nil {
		t.Fatal("a prompt holds only basket items")
	}
	if err := c.FixesAction(t.Context(), []string{id}, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	entries, _, _ = loadFixes(t.Context(), c.reader())
	propose, err := fixesPreview(entries, "repo:repo-corpus", []string{id}, "propose")
	if err != nil {
		t.Fatal(err)
	}
	change, _ := fixesPreview(entries, "repo:repo-corpus", []string{id}, "change")
	prompt := firstString(propose["prompt"])
	if !strings.Contains(prompt, "change nothing") || !strings.Contains(firstString(change["prompt"]), "make the smallest change") || !strings.Contains(prompt, "Pharos fix "+id) || !strings.Contains(prompt, "trimesh") {
		t.Fatalf("investigate and make prompts:\n%s", prompt)
	}
	if strings.Contains(prompt, "user's request") {
		t.Fatalf("request events stay out of the prompt:\n%s", prompt)
	}
	if _, err := c.FixesCopy(t.Context(), "repo:repo-corpus", []string{id}, "change", firstString(propose["preview_token"])); err == nil {
		t.Fatal("a copy must match the reviewed prompt and mode")
	}
	if _, err := c.FixesCopy(t.Context(), "repo:repo-corpus", []string{id}, "propose", firstString(propose["preview_token"])); err != nil {
		t.Fatal(err)
	}
	state := func() map[string]any {
		t.Helper()
		overview, err := c.FixesOverview(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range overview["items"].([]map[string]any) {
			if item["id"] == id {
				return item
			}
		}
		t.Fatalf("%s is no longer listed", id)
		return nil
	}
	if s := state(); s["state"] != "handed_off" || s["cart"] != (*fixCart)(nil) {
		t.Fatalf("copying hands off: %v", s)
	}

	// A rebuild with nothing new is a no-op; a recurrence after the handoff
	// opens it again, and the id holds.
	built, _ := c.metaValue(t.Context(), "fixes_built_at")
	if err := c.refreshFixes(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if again, _ := c.metaValue(t.Context(), "fixes_built_at"); again != built {
		t.Fatal("unchanged inputs don't rebuild")
	}
	seedFixes(t, c, "2026-10-26", 2)
	if err := c.refreshFixes(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if s := state(); s["state"] != "open" || s["recurred"] != true {
		t.Fatalf("a recurrence after the handoff reopens it: %v", s)
	}
	if err := c.FixesAction(t.Context(), []string{id}, map[string]any{"action": "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if s := state(); s["state"] != "dismissed" {
		t.Fatalf("closed: %v", s)
	}
	if err := c.FixesAction(t.Context(), []string{id, "unknown"}, map[string]any{"action": "restore"}); err == nil || state()["state"] != "dismissed" {
		t.Fatal("an unknown id rejects the whole selection")
	}

	// Bounds: a minimum above its work items takes it off the menu, and the
	// overview says how many it hides.
	server := testServer(t, config, c)
	if err := c.FixesAction(t.Context(), []string{id}, map[string]any{"action": "restore"}); err != nil {
		t.Fatal(err)
	}
	response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/findings/fixes/settings", map[string]any{"min_work_items": 20})
	if response.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", response.Code, response.Body)
	}
	overview, _ = c.FixesOverview(t.Context())
	if hidden := overview["hidden"].(map[string]int); hidden["rare"] == 0 || state()["hidden"] != "rare" {
		t.Fatalf("bounds hide rare fixes and count them: %v", hidden)
	}
	// The table lists every fix, hidden ones included, and says why.
	rows, err := c.fixesQueryRows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, row := range rows {
		if row["id"] == id {
			listed = row["shown"] == "rare" && row["state"] == "open" && row["work_items"] == state()["work_items"]
		}
	}
	if len(rows) != len(overview["items"].([]map[string]any)) || !listed {
		t.Fatalf("the table holds hidden fixes with their reason: %v", rows)
	}
	if response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/findings/fixes/settings", map[string]any{"hide_days": -1}); response.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range bounds are refused: %d", response.Code)
	}
	if value, _, err := callFixesMCP(c, "list_fixes", map[string]any{}); err != nil || value.(map[string]any)["hidden_by_bounds"].(int) == 0 {
		t.Fatalf("MCP leaves hidden fixes out and says so: %v %v", value, err)
	}
	if value, _, err := callFixesMCP(c, "list_fixes", map[string]any{"include_hidden": true}); err != nil || len(value.(map[string]any)["items"].([]map[string]any)) == 0 {
		t.Fatalf("MCP can include hidden fixes: %v %v", value, err)
	}
	if value, _, err := callFixesMCP(c, "get_fix", map[string]any{"id": id}); err != nil || value.(map[string]any)["fix"].(map[string]any)["quotes"] == nil {
		t.Fatalf("get_fix returns the evidence: %v %v", value, err)
	}
	if response := apiCall(t, server, config.APIToken, http.MethodGet, "/api/findings/fixes/"+id, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "category_explanation") {
		t.Fatalf("detail: %d", response.Code)
	}
	if response := apiCall(t, server, config.APIToken, http.MethodGet, "/api/findings/fixes/nope", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown detail: %d", response.Code)
	}
}

func TestSafetySafeguardNotices(t *testing.T) {
	c, _ := testCatalog(t)
	at := formatTime(c.clock().AddDate(0, 0, -2))
	execTx(t, c.DB,
		`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-s','service','2026-10-01','2026-10-01')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES('ws-s','claude','local','ws-s','repo-s','Work','2026-10-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c-s','ws-s','claude','local','c-s')`,
		`INSERT INTO agent_events(message_id,conversation_id,source,occurred_at,day,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model)
 VALUES('m-s','c-s','reply','`+at+`','x','circumventing_constraint',0.9,0,10,'I removed the failing assertions so the check passes.','','','[]','[]','m'),
 ('m-t','c-s','thinking','`+at+`','x','circumventing_constraint',0.9,0,10,'The sandbox blocks the write, so I will use a heredoc through python instead.','','','[]','[]','m'),
 ('m-u','c-s','reply','`+at+`','x','circumventing_constraint',0.3,0,10,'Below the threshold: removed the tests.','','','[]','[]','m')`)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, err := loadSafetyCases(t.Context(), c.reader())
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]safetyCase{}
	for _, item := range items {
		rules[item.Rule] = item
	}
	test, restriction := rules["safeguard-test"], rules["safeguard-restriction"]
	if len(items) != 2 || test.Target != "repo:repo-s" || test.Evidence[0].Message != "m-s" || !test.Evidence[0].Quote || restriction.Evidence[0].Outcome != "In the agent's thinking" {
		t.Fatalf("each safeguard event above the threshold is a Risky actions notice under its check: %+v", items)
	}
	if err := c.SetSafetySettings(t.Context(), map[string]any{"rule_enabled": map[string]any{"safeguard-test": false}}); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `DELETE FROM safety_cases`)
	if err := c.refreshSafety(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _ = loadSafetyCases(t.Context(), c.reader())
	if len(items) != 1 || items[0].Rule != "safeguard-restriction" {
		t.Fatalf("a disabled safeguard check raises nothing: %+v", items)
	}
}

func TestFixesLeadsTitlesAndKnownFailures(t *testing.T) {
	// The lead stands alone: no "The same…", names the key term.
	events := []fixesEvent{
		{ID: 1, Source: "reply", Sentence: "The same restriction prevented creating the requested report."},
		{ID: 2, Source: "thinking", Sentence: "Odd, so I'll try `uv sync` again."},
		{ID: 3, Source: "reply", Sentence: "The read-only sandbox prevented creating the local HTML report with `uv sync`."},
	}
	if lead := fixesRepresentative(events, nil, []string{"uv sync"}); lead.ID != 3 {
		t.Fatalf("lead: %+v", lead)
	}
	for text, want := range map[string]string{
		"The same restriction prevented creating the requested local HTML report.":       "Restriction prevented creating the requested local HTML report",
		"It seems Conductor already renamed the branch, so I'll use the new name.":       "Conductor already renamed the branch",
		"**Test status:** `npm --prefix web test -- dashboards` could not run.":          "`npm --prefix web test -- dashboards` could not run",
		"Odd, git status shows staged changes I didn't cause — so I'll check the index.": "Git status shows staged changes I didn't cause",
	} {
		if got := fixesLeadTitle(text); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}

	// A key term only a few members name doesn't title the item.
	mixed := &fixItem{Category: "env", Terms: []string{"parallel-hours"}, Target: "repo:app", TargetLabel: "app", Lead: "The shell has Node 20 and the runner requires Node 24."}
	mixed.events = append(mixed.events, fixesEvent{Sentence: "One unrelated `parallel-hours` test failed once."})
	for i := 0; i < 6; i++ {
		mixed.events = append(mixed.events, fixesEvent{Sentence: "The shell has Node 20 and the runner requires Node 24."})
	}
	if title := fixesProblemTitle(mixed); strings.Contains(title, "parallel-hours") {
		t.Fatalf("a rare term titled the item: %s", title)
	}

	f := &fixesFixture{}
	f.noise(40)
	for i, sentence := range []string{
		"One failure, and it looks unrelated: floating-point rounding in a parallelism dashboard parity test.",
		"A floating-point rounding failure in the parallel-hours dashboard test, which my changes don't touch.",
		"Only one failure remains, a floating-point rounding issue in the parallel-hours parity test that's probably unrelated to my change.",
	} {
		f.add("preexisting_failure", "app", fmt.Sprintf("round-a-%d", i), "2026-10-2"+fmt.Sprint(i), sentence)
	}
	for i, sentence := range []string{
		"The bespoke dashboard parity assertion is off at the eleventh decimal place; pre-existing on master.",
		"Pre-existing: the dashboard parity values differ at the eleventh decimal place.",
		"The dashboard parity check differs in the eleventh decimal place, as on master.",
	} {
		f.add("preexisting_failure", "app", fmt.Sprintf("round-b-%d", i), "2026-10-2"+fmt.Sprint(i), sentence)
	}
	for i := 0; i < 3; i++ {
		f.add("preexisting_failure", "app", fmt.Sprintf("menu-%d", i), "2026-10-25", fmt.Sprintf("The browser test \"Mac menu editing preserves focus\" fails on clean master too (run %d).", i))
	}
	// Agents following a rule on purpose: guidance working as intended.
	for i, sentence := range []string{
		"Per my notes I must not hold a read transaction on the live catalog, so I'll read a backup copy.",
		"I can't measure the live catalog without holding a read on it.",
		"The rules say never to hold a read on the live catalog, so a backup copy it is.",
	} {
		f.add("missing_capability", "app", fmt.Sprintf("live-%d", i), "2026-10-26", sentence)
	}
	// A rule naming a requirement the setup misses is the fix, not intended.
	for i := 0; i < 3; i++ {
		f.add("missing_capability", "app", fmt.Sprintf("node-%d", i), "2026-10-26", fmt.Sprintf("The guidance says the runner requires Node 24, but the shell defaults to Node 20 version, so `npm test` did not run (%d).", i))
	}
	input := fixesInput{Events: f.events, End: "2026-10-28"}
	items, _, err := fixesBuild(input)
	if err != nil {
		t.Fatal(err)
	}
	rounding := []*fixItem{}
	for i := range items {
		for _, e := range items[i].events {
			if strings.Contains(e.Sentence, "parity") {
				rounding = append(rounding, &items[i])
				break
			}
		}
	}
	if len(rounding) != 1 || rounding[0].WorkItems != 6 || rounding[0].Category != "flaky" || !strings.HasPrefix(rounding[0].Title, "Known failing check: ") || !strings.Contains(rounding[0].Title, "parity") {
		t.Fatalf("one known failure, however it is described: %v", fixesTitles(items))
	}
	if !strings.HasPrefix(rounding[0].DestWhere, "The failing test itself") {
		t.Fatalf("a known failure is fixed in the test: %s", rounding[0].DestWhere)
	}
	menu := fixesFind(items, "Mac menu")
	if menu == nil || menu == rounding[0] || menu.Title != "Known failing check: “Mac menu editing preserves focus”" {
		t.Fatalf("a quoted test name names its check: %v", fixesTitles(items))
	}
	live := fixesFind(items, "live catalog")
	if live == nil || !live.Intended || fixesHidden(*live, fixesDefaultBounds) != "intended" {
		t.Fatalf("a rule followed on purpose is not a fix: %+v", live)
	}
	if node := fixesFind(items, "Node 24"); node == nil || node.Intended {
		t.Fatalf("a missing requirement the guidance names is a fix: %+v", node)
	}
}

func TestFixesTimeLostInReplies(t *testing.T) {
	// A reply reports a problem after the fact: its time runs from the turn's
	// first failed call to the report.
	timelines := map[string][]fixesMoment{"c": {
		{At: "2026-10-20T10:00:00Z", Message: "p", Prompt: true},
		{At: "2026-10-20T10:01:00Z", Call: true},
		{At: "2026-10-20T10:02:00Z", Call: true, Failed: true},
		{At: "2026-10-20T10:04:00Z", Call: true},
		{At: "2026-10-20T10:05:00Z", Message: "r"},
	}}
	episodes := fixesEpisodes([]fixesEvent{{ID: 1, ConversationID: "c", MessageID: "r", Family: "obstacle", Source: "reply"}}, timelines)
	if episode := episodes[1]; episode.seconds != 180 || episode.calls != 2 || episode.outcome != "reported" {
		t.Fatalf("retrospective episode: %+v", episode)
	}
}
