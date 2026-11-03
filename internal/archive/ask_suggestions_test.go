package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Ask Pharos suggestions (docs/ask-suggestions.md): signals find something
// only in substantive data, ranking turns them into generic questions, and a
// question sent as offered gives the agent what Pharos noticed.

func TestAskSuggestionCatalogNamesImplementedSignals(t *testing.T) {
	catalog, err := loadedAskSuggestions()
	if err != nil || len(catalog.Families) == 0 {
		t.Fatalf("catalog: %v", err)
	}
	named := map[string]bool{}
	for _, area := range catalog.Areas {
		for _, signal := range area.Signals {
			named[signal] = true
			if askSignalFuncs[signal] == nil && signal != askUnseenSignal {
				t.Errorf("area %s names unimplemented signal %s", area.ID, signal)
			}
		}
	}
	for signal := range askSignalFuncs {
		if !named[signal] {
			t.Errorf("signal %s feeds no area", signal)
		}
	}
}

// seedAskActivity records failing tests, mostly in alpha, and compactions in
// two of its conversations.
func seedAskActivity(t *testing.T, c *Catalog, now time.Time) {
	t.Helper()
	statements := []string{
		`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-a','alpha','2026-01-01','2026-01-01'),('repo-b','beta','2026-01-01','2026-01-01')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES('ws-a','codex','me','a','repo-a','Alpha work','2026-01-01'),('ws-b','codex','me','b','repo-b','Beta work','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('conv-a1','ws-a','codex','me','a1'),('conv-a2','ws-a','codex','me','a2'),('conv-b1','ws-b','codex','me','b1')`,
	}
	for day := 0; day < 10; day++ {
		statements = append(statements, fmt.Sprintf(`INSERT INTO tool_usage_daily(id,day,repository_name,command_category,tool_category,command_name,call_count,error_count,total_duration_ms)
 VALUES('a-%d','%s','alpha','test','command','go test',20,4,600000),('b-%d','%s','beta','test','command','npm test',10,1,60000)`, day, askDay(now, day), day, askDay(now, day)))
	}
	for i, conversation := range []string{"conv-a1", "conv-a1", "conv-a2", "conv-b1"} {
		statements = append(statements, fmt.Sprintf(`INSERT INTO model_requests(id,conversation_id,sequence,requested_at,compacted_before) VALUES('req-%d','%s',%d,'%s',1)`,
			i, conversation, i, now.UTC().Add(-time.Duration(i+1)*time.Hour).Format(timeLayout)))
	}
	execTx(t, c.DB, statements...)
}

func TestAskSignalsNeedSubstantiveData(t *testing.T) {
	catalog, _ := testCatalog(t)
	ctx := context.Background()
	now := time.Now()
	ask, err := loadedAskSuggestions()
	if err != nil {
		t.Fatal(err)
	}
	empty := computeAskSignals(ctx, catalog, nil, now)
	if len(empty.Errors) > 0 {
		t.Fatalf("signals failed on an empty library: %v", empty.Errors)
	}
	if len(empty.Signals) != 0 {
		t.Fatalf("an empty library has nothing worth asking about: %+v", empty.Signals)
	}
	if slate := rankAskSuggestions(ask, empty, askRankInput{Now: now, Visits: 100}); len(slate) != 0 {
		t.Fatalf("suggested questions without data: %+v", slate)
	}

	seedAskActivity(t, catalog, now)
	state := computeAskSignals(ctx, catalog, nil, now)
	if len(state.Errors) > 0 {
		t.Fatalf("signals failed: %v", state.Errors)
	}
	failures := state.Signals["verification.failures"]
	if failures.Score <= 0 || failures.Repository != "alpha" || !strings.Contains(failures.Lead, "go test") {
		t.Fatalf("test failures, mostly in alpha, were not noticed: %+v", failures)
	}
	if compaction := state.Signals["context.compaction"]; compaction.Score <= 0 || compaction.Support != 3 {
		t.Fatalf("compactions in three conversations: %+v", compaction)
	}
	for _, missing := range []string{"spend.tokens", "agent.friction", "risk.safety", "writing.volume"} {
		if _, ok := state.Signals[missing]; ok {
			t.Errorf("%s noticed something without data", missing)
		}
	}
	slate := rankAskSuggestions(ask, state, askRankInput{Now: now, Repositories: 2})
	areas := map[string]bool{}
	for _, item := range slate {
		areas[item.Area] = true
		// Questions stay generic: the failing command and the counts are
		// private leads, never the question.
		if strings.Contains(item.Text, "go test") || regexp.MustCompile(`[0-9{}]`).MatchString(item.Text) {
			t.Errorf("question names specifics: %q", item.Text)
		}
	}
	if !areas["verification"] || !areas["context"] || areas["spend"] || areas["risk"] {
		t.Fatalf("slate follows the evidence: %+v", slate)
	}
}

func askRankState() askSuggestionState {
	signals := map[string]askSignal{}
	for name := range askSignalFuncs {
		signals[name] = askSignal{Score: 0.6, Support: 10, Period: "week"}
	}
	signals["verification.time"] = askSignal{Score: 0.9, Support: 10, Repository: "alpha"}
	return askSuggestionState{Signals: signals}
}

func TestAskSuggestionRanking(t *testing.T) {
	catalog, err := loadedAskSuggestions()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	state := askRankState()
	in := askRankInput{Now: now, Repositories: 3}
	first := rankAskSuggestions(catalog, state, in)
	if len(first) != askSlateSize {
		t.Fatalf("got %d questions, want %d", len(first), askSlateSize)
	}
	if again := rankAskSuggestions(catalog, state, in); !slices.Equal(first, again) {
		t.Fatal("the same signals and input gave a different slate")
	}
	refreshed := rankAskSuggestions(catalog, state, askRankInput{Now: now, Repositories: 3, Seed: 1})
	if slices.Equal(first, refreshed) {
		t.Fatal("Refresh offered the same slate")
	}
	for _, slate := range [][]askSuggestion{first, refreshed} {
		perArea := map[string]int{}
		for i, item := range slate {
			perArea[item.Area]++
			if i > 0 && slate[i-1].Area == item.Area {
				t.Errorf("two questions in a row about %s", item.Area)
			}
			if strings.ContainsAny(item.Text, "{}") {
				t.Errorf("unfilled template: %q", item.Text)
			}
		}
		for area, n := range perArea {
			if n > 2 {
				t.Errorf("%d questions about %s", n, area)
			}
		}
	}

	// A scope names the repository holding most of what was seen, and only
	// in a library with several.
	scoped := func(in askRankInput) bool {
		for seed := 0; seed < 20; seed++ {
			in.Seed = seed
			for _, item := range rankAskSuggestions(catalog, state, in) {
				if strings.Contains(item.Text, "in the alpha repository") {
					return true
				}
			}
		}
		return false
	}
	if !scoped(askRankInput{Now: now, Repositories: 3}) || scoped(askRankInput{Now: now, Repositories: 1}) {
		t.Fatal("repository scope ignores the library's repositories")
	}

	// A question asked lately gives way; the page the user is on draws its
	// questions forward.
	appearances := func(family string, in askRankInput) int {
		count := 0
		for seed := 0; seed < 40; seed++ {
			in.Seed = seed
			for _, item := range rankAskSuggestions(catalog, state, in)[:4] {
				if item.ID == family {
					count++
				}
			}
		}
		return count
	}
	base := askRankInput{Now: now, Repositories: 3}
	asked := base
	asked.Asked = []askAsked{{Family: "risk-review", Prompt: "Which agent actions should I double-check?", At: now.Add(-time.Hour)}}
	if appearances("risk-review", asked) >= appearances("risk-review", base) {
		t.Fatal("a question asked an hour ago was not demoted")
	}
	onPage := base
	onPage.Page = "analyze/carbon"
	if appearances("carbon-footprint", onPage) <= appearances("carbon-footprint", base) {
		t.Fatal("the current page's questions were not drawn forward")
	}

	// Pharos's own views are suggested only when the visits retained here
	// show several areas worth asking about whose pages are unvisited.
	discovery := func(in askRankInput) bool {
		for seed := 0; seed < 20; seed++ {
			in.Seed = seed
			for _, item := range rankAskSuggestions(catalog, state, in) {
				if item.Area == "discovery" {
					return true
				}
			}
		}
		return false
	}
	if discovery(askRankInput{Now: now, Visits: 5}) {
		t.Fatal("a few visits are not evidence of unseen views")
	}
	if !discovery(askRankInput{Now: now, Visits: 50, Visited: map[string]time.Time{"home": now}}) {
		t.Fatal("unseen views with data were not suggested")
	}
	seen := map[string]time.Time{}
	for _, area := range catalog.Areas {
		for _, page := range area.Pages {
			seen[page] = now
		}
	}
	if discovery(askRankInput{Now: now, Visits: 50, Visited: seen}) {
		t.Fatal("suggested unseen views when every page was visited")
	}

	// Outcomes adjust priors: a family whose questions keep coming back
	// without answers is offered less.
	no := false
	failing := base
	for i := 0; i < 6; i++ {
		failing.Asked = append(failing.Asked, askAsked{Family: "verification-latency", At: now.Add(-30 * 24 * time.Hour), Answered: &no})
	}
	if appearances("verification-latency", failing) >= appearances("verification-latency", base) {
		t.Fatal("unanswered questions did not lower their family")
	}
}

// Agent events count as Pharos shows them: confident ones only, and each
// conversation once however many kinds it has.
func TestAskAgentEventSignalsCountConfidentConversations(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.ensureAgentEventsSchema(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seedAskActivity(t, catalog, now)
	event := func(id, conversation, family string, probability float64) string {
		return fmt.Sprintf(`INSERT INTO agent_events(message_id,conversation_id,source,day,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model)
 VALUES('%s','%s','reply','%s','%s',%g,0,1,'s','','','[]','[]','m')`, id, conversation, askDay(now, 1), family, probability)
	}
	friction := func() askSignal {
		t.Helper()
		signal, err := askAgentFriction(context.Background(), &askSignalRun{c: catalog, now: now})
		if err != nil {
			t.Fatal(err)
		}
		return signal
	}
	// One conversation with three kinds, and unconfident candidates in two
	// more: not three conversations.
	execTx(t, catalog.DB, event("m1", "conv-a1", "obstacle", 0.9), event("m2", "conv-a1", "stuck", 0.9), event("m3", "conv-a1", "confusion", 0.9),
		event("m4", "conv-a2", "obstacle", agentEventThreshold/2), event("m5", "conv-b1", "stuck", agentEventThreshold/2))
	if signal := friction(); signal.Score != 0 {
		t.Fatalf("one conversation and unconfident candidates are not three conversations: %+v", signal)
	}
	execTx(t, catalog.DB, event("m6", "conv-a2", "obstacle", 0.9), event("m7", "conv-b1", "stuck", agentEventThreshold))
	if signal := friction(); signal.Score <= 0 || signal.Support != 3 {
		t.Fatalf("three conversations with confident events: %+v", signal)
	}
}

// A domain that is behind but being caught up is not a library-health lead.
func TestAskFreshnessSignalIgnoresWorkUnderWay(t *testing.T) {
	f := newFreshnessFixture(t)
	f.scheduled(true)
	server := testServer(t, f.config, f.catalog)
	health := func() askSignal {
		t.Helper()
		signal, err := askFreshnessSignal(context.Background(), &askSignalRun{c: f.catalog, server: server, now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return signal
	}
	if signal := health(); signal.Score <= 0 || !strings.Contains(signal.Lead, "nothing catching up") {
		t.Fatalf("a never-indexed source is behind: %+v", signal)
	}
	_, index := server.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser})
	defer index.Finish(nil)
	if signal := health(); signal.Score != 0 {
		t.Fatalf("an index is catching it up: %+v", signal)
	}
}

func TestAskSuggestionsEndpointAndLeads(t *testing.T) {
	s := askTestServer(t)
	get := func() map[string]any {
		t.Helper()
		r := askCall(t, s, http.MethodGet, "suggestions?seed=0&page="+"/analyze/carbon", nil)
		if r.Code != 200 {
			t.Fatalf("suggestions: %d %s", r.Code, r.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if body := get(); len(body["suggestions"].([]any)) != 0 || body["pending"] != nil {
		t.Fatalf("suggestions before Ask is enabled: %v", body)
	}
	askEnable(t, s)
	if body := get(); len(body["suggestions"].([]any)) != 0 || body["pending"] != true {
		t.Fatalf("before the first run, nothing is offered and the run is requested: %v", body)
	}
	if !s.peekRequested()["suggestions"] {
		t.Fatal("the background item was not requested")
	}
	now := time.Now()
	seedAskActivity(t, s.Catalog, now)
	if !s.Catalog.askSuggestionsStale(t.Context(), now) {
		t.Fatal("no stored suggestions should be stale")
	}
	if err := s.refreshAskSuggestions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.Catalog.askSuggestionsStale(t.Context(), now) {
		t.Fatal("freshly chosen suggestions are stale")
	}
	if s.Catalog.askSuggestionInputs(t.Context(), now) == s.Catalog.askSuggestionInputs(t.Context(), now.Add(48*time.Hour)) {
		t.Fatal("suggestions do not follow the days' windows")
	}
	var offered map[string]any
	for _, item := range get()["suggestions"].([]any) {
		if item.(map[string]any)["id"] == "verification-reliability" {
			offered = item.(map[string]any)
		}
	}
	if offered == nil {
		t.Fatal("tests failing in alpha were not suggested")
	}

	var leads []string
	override(t, &askRunCLI, func(ctx context.Context, p askProvider, selection askSelection, prompt string) ([]byte, error) {
		_, raw, _ := strings.Cut(prompt, "Context JSON:\n")
		var data struct {
			Leads []string `json:"suggestionLeads"`
		}
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		leads = data.Leads
		return askReply("analyze"), nil
	})
	input := askInput()
	input["prompt"], input["suggestion"] = offered["text"], map[string]any{"id": offered["id"], "text": offered["text"]}
	askOK(t, s, "send", input)
	session, err := s.askSession(t.Context(), "chat-1")
	if err != nil || session.Conversation[0].Suggestion != "verification-reliability" || session.Conversation[0].SuggestionEdited {
		t.Fatalf("the suggestion was not recorded: %+v %v", session.Conversation, err)
	}
	if len(leads) == 0 || !strings.Contains(strings.Join(leads, " "), "go test") {
		t.Fatalf("the agent did not get what Pharos noticed: %v", leads)
	}
	var indexed int
	if err := s.Catalog.reader().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?", `"failed"`).Scan(&indexed); err != nil || indexed != 0 {
		t.Fatalf("leads entered the conversation index: %d %v", indexed, err)
	}

	edited := askInput()
	edited["id"], edited["turnID"] = "chat-2", "turn-2"
	edited["prompt"], edited["suggestion"] = "Summarize my week of pull requests", map[string]any{"id": offered["id"], "text": offered["text"]}
	leads = nil
	askOK(t, s, "send", edited)
	session, err = s.askSession(t.Context(), "chat-2")
	if err != nil || !session.Conversation[0].SuggestionEdited || leads != nil {
		t.Fatalf("a rewritten question kept its suggestion's leads: %+v %v %v", session.Conversation, leads, err)
	}
}

func TestBackgroundSuggestionsItemFollowsAsk(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)
	settings := backgroundSettings{Mode: "on", DailyAt: "03:00"}
	plan := func(in backgroundInputs) (itemPlan, bool) {
		for _, plan := range backgroundPlan(settings, in, at) {
			if plan.ID == "suggestions" {
				return plan, true
			}
		}
		return itemPlan{}, false
	}
	if _, ok := plan(backgroundInputs{suggestionsStale: true}); ok {
		t.Fatal("suggestions are chosen where Ask Pharos is off")
	}
	if item, ok := plan(backgroundInputs{ask: true}); !ok || item.Pending || item.Due {
		t.Fatalf("current suggestions have no work: %+v", item)
	}
	if item, _ := plan(backgroundInputs{ask: true, suggestionsStale: true}); !item.Pending || !item.Due {
		t.Fatalf("stale suggestions that never ran are due: %+v", item)
	}
	ran := backgroundInputs{ask: true, suggestionsStale: true, runs: map[string]backgroundRunRow{"suggestions": {succeeded: at.Add(-10 * time.Minute).UTC().Format(time.RFC3339)}}}
	if item, _ := plan(ran); item.Due || !item.Next.Equal(at.Add(50*time.Minute)) {
		t.Fatalf("hourly suggestions wait an hour after their last run: %+v", item)
	}
}
