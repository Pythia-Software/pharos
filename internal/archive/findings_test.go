package archive

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindingStatistics(t *testing.T) {
	// 10 of 100 before, 3 of 100 after: significant, one-sided.
	if lower, _ := hypergeometric(3, 100, 13, 200); lower >= 0.05 {
		t.Fatalf("10 -> 3 in 100 each should be significant, p=%.3f", lower)
	}
	// 10 -> 6 is not.
	if lower, _ := hypergeometric(6, 100, 16, 200); lower < 0.05 {
		t.Fatalf("10 -> 6 should not be significant, p=%.3f", lower)
	}
	// Getting worse shows in the upper tail.
	if _, upper := hypergeometric(25, 100, 35, 200); upper >= 0.05 {
		t.Fatalf("10 -> 25 should be significantly worse, p=%.3f", upper)
	}
	before := []float64{100, 120, 90, 110, 130, 95, 105, 115, 125, 98, 102, 108}
	after := []float64{40, 50, 45, 55, 38, 42, 47, 51, 44, 49, 46, 43}
	if less, greater := mannWhitney(before, after); less >= 0.01 || greater < 0.9 {
		t.Fatalf("clearly smaller values: less=%.4f greater=%.4f", less, greater)
	}
	if less, _ := mannWhitney(before, before); less < 0.2 {
		t.Fatalf("identical samples should not differ, p=%.3f", less)
	}
}

func TestFindingWording(t *testing.T) {
	for rate, want := range map[float64]string{0.34: "1 of every 3 conversations", 0.5: "about half of the conversations", 0.7: "7 of every 10 conversations",
		0.02: "1 of every 50 conversations", 0.97: "nearly all conversations", 0.4: "2 of every 5 conversations", 0.26: "1 of every 4 conversations"} {
		if got := fractionPhrase(rate, "conversations"); got != want {
			t.Errorf("fractionPhrase(%v) = %q, want %q", rate, got, want)
		}
	}
	if got := fractionPhrase(0.33, ""); got != "1 in 3" {
		t.Errorf("bare ratio = %q", got)
	}
	if countNoun(2, "search") != "2 searches" || countNoun(1, "lookup") != "1 lookup" || countNoun(12000, "token") != "12,000 tokens" {
		t.Errorf("countNoun: %q %q %q", countNoun(2, "search"), countNoun(1, "lookup"), countNoun(12000, "token"))
	}
	if dollars(41.3) != "$41" || dollars(0.4) != "under $1" || dollars(1234) != "$1,230" {
		t.Errorf("dollars: %q %q %q", dollars(41.3), dollars(0.4), dollars(1234))
	}
}

func TestInstructionHunt(t *testing.T) {
	cases := []struct {
		provider, category, tool, program, command, path string
		kind, file                                       string
	}{
		{"codex", "command", "exec_command", "rg", "rg --files -g 'AGENTS.md'", "", "search", "AGENTS.md"},
		{"codex", "command", "exec_command", "find", "find .. -name AGENTS.md", "", "search", "AGENTS.md"},
		{"codex", "command", "exec_command", "cat", "cat CLAUDE.md", "", "read", "CLAUDE.md"},
		{"codex", "command", "exec_command", "sed", "sed -n '1,80p' .claude/skills/deploy/SKILL.md", "", "skill", ".claude/skills"},
		// Its own file read by hand, content searches, and global files don't count.
		{"codex", "command", "exec_command", "cat", "cat AGENTS.md", "", "", ""},
		{"codex", "command", "exec_command", "rg", "rg -n 'AGENTS.md' docs", "", "", ""},
		{"codex", "command", "exec_command", "cat", "cat ~/.claude/CLAUDE.md", "", "", ""},
		{"claude", "read", "Read", "", "", "/Users/me/repo/AGENTS.md", "read", "AGENTS.md"},
		{"claude", "read", "Read", "", "", "/Users/me/.codex/AGENTS.md", "", ""},
		{"claude", "read", "Read", "", "", "/Users/me/repo/CLAUDE.md", "", ""},
	}
	for _, item := range cases {
		kind, file := instructionHunt(item.provider, item.category, item.tool, item.program, item.command, item.path)
		if kind != item.kind || file != item.file {
			t.Errorf("%s %q %q: got %q %q, want %q %q", item.provider, item.command, item.path, kind, file, item.kind, item.file)
		}
	}
}

func TestCommandRecovery(t *testing.T) {
	item, ok := commandRecovery("pytest -q tests/test_x.py", "PYTHONPATH=. pytest -q tests/test_x.py")
	if !ok || item.iteration || item.key != "+env:PYTHONPATH=." {
		t.Fatalf("PYTHONPATH recovery: %+v", item)
	}
	if phrase := recoveryPhrase(item.key); phrase != "setting `PYTHONPATH=.`" {
		t.Fatalf("phrase %q", phrase)
	}
	item, _ = commandRecovery("gh pr create --fill", "git push -u origin HEAD && gh pr create --fill")
	if item.iteration || !strings.Contains(item.key, "+step:git push") {
		t.Fatalf("push-first recovery: %+v", item)
	}
	item, _ = commandRecovery("pytest tests/test_a.py", "pytest tests/test_b.py")
	if !item.iteration {
		t.Fatalf("a different test target is iteration: %+v", item)
	}
	item, _ = commandRecovery("python3 - <<'EOF'\nprint(x)\nEOF", "python3 - <<'EOF'\nprint(1)\nEOF")
	if !item.iteration {
		t.Fatalf("a different script body is iteration: %+v", item)
	}
}

func TestHeavyShape(t *testing.T) {
	for command, want := range map[string]string{
		"rg -n TODO":                    "search-everything",
		"rg -l TODO":                    "",
		"rg -n TODO src/":               "",
		"rg -n TODO | head -20":         "",
		"git diff":                      "full-diff",
		"git diff --stat":               "",
		"git diff -- internal/file.go":  "",
		"sed -n '1,400p' main.go":       "long-range",
		"sed -n '1,120p' main.go":       "",
		"cat package-lock.json":         "generated-file",
		"cat README.md":                 "",
		"git log -p -3":                 "full-log",
		"grep -rn needle":               "search-everything",
		"grep -rn needle internal/":     "",
		"sed -n '200,$p' big_file.rs":   "long-range",
		"git show HEAD --stat":          "",
		"git show HEAD":                 "full-diff",
		"git diff --name-only origin/m": "",
	} {
		parsed := parseShellCommand(command).primary()
		if got := heavyShape(parsed.Program, parsed.Subcommand, command); got != want {
			t.Errorf("heavyShape(%q) = %q, want %q", command, got, want)
		}
	}
}

// findingFixture writes a repository with a month of conversations, some of
// which call python and fail, as the tool ledger records them.
type findingFixture struct {
	t       *testing.T
	catalog *Catalog
	next    int
}

func newFindingFixture(t *testing.T, now time.Time) *findingFixture {
	t.Helper()
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	catalog.now = func() time.Time { return now }
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-1','alpha',?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	return &findingFixture{t: t, catalog: catalog}
}

// conversation adds one Claude conversation on a day with a few shell
// commands, the first failing with python not found when fail is set.
func (fixture *findingFixture) conversation(day time.Time, fail bool) {
	fixture.t.Helper()
	fixture.next++
	id := fmt.Sprint(fixture.next)
	exec := func(statement string, args ...any) {
		fixture.t.Helper()
		if _, err := fixture.catalog.DB.Exec(statement, args...); err != nil {
			fixture.t.Fatal(err)
		}
	}
	started := day.Add(10 * time.Hour)
	exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES(?,?,?,?,?,?,?)`,
		"ws-"+id, "claude", "local", "source-"+id, "repo-1", "Work "+id, formatTime(started))
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at,harness,harness_version_last) VALUES(?,?,?,?,?,?,?,?)`,
		"conv-"+id, "ws-"+id, "claude", "local", "native-"+id, formatTime(started), "claude-code", "2.1.0")
	commands := []struct{ command, program, status, errorType, signature string }{
		{"ls", "ls", "ok", "", ""}, {"go test ./...", "go", "ok", "", ""},
	}
	if fail {
		commands = append([]struct{ command, program, status, errorType, signature string }{
			{"python scripts/check.py", "python", "error", "nonzero_exit", "zsh:<n>: command not found: python"},
			{"python3 scripts/check.py", "python3", "ok", "", ""},
		}, commands...)
	}
	for index, item := range commands {
		exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,command,program,started_at,status,error_type,error_signature,
			test_failure,result_tokens,carried_tokens,duration_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?,?)`,
			fmt.Sprintf("call-%s-%d", id, index), "ws-"+id, "conv-"+id, index, "claude", "tool_call", "Bash", "command", item.command, item.program,
			formatTime(started.Add(time.Duration(index)*time.Minute)), item.status, nilIfEmpty(item.errorType), nilIfEmpty(item.signature), 200, 4000, 1500)
	}
}

func TestFindingLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	catalog := fixture.catalog
	// Four weeks of work, one conversation a day, every other one failing.
	for back := 27; back >= 0; back-- {
		fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, -back), back%2 == 0)
	}
	if err := catalog.RefreshFindings(ctx, true); err != nil {
		t.Fatal(err)
	}
	overview, err := catalog.FindingsOverview(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	cards := overview["findings"].([]map[string]any)
	var card map[string]any
	for _, item := range cards {
		if strings.Contains(firstString(item["title"]), "`python`") {
			card = item
		}
	}
	if card == nil {
		t.Fatalf("no python finding among %d cards: %v", len(cards), cards)
	}
	if card["state"] != findingOpen || card["new"] != true || card["target"] != "repo:repo-1" || card["target_label"] != "alpha" {
		t.Fatalf("card: %v", card)
	}
	if !strings.Contains(firstString(card["explanation"]), "about half of the conversations that ran shell commands") {
		t.Fatalf("explanation: %q", card["explanation"])
	}
	id := firstString(card["id"])
	if !strings.HasPrefix(id, "failure:repo:repo-1:") {
		t.Fatalf("id %q", id)
	}

	// MCP reads never change state.
	listed, err := callMCP(catalog, "list_findings", map[string]any{"repository": "alpha"})
	if err != nil || len(listed.(map[string]any)["items"].([]map[string]any)) == 0 {
		t.Fatalf("list_findings: %v %v", listed, err)
	}
	detail, err := callMCP(catalog, "get_finding", map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	evidence := detail.(map[string]any)["evidence"].(map[string]any)
	if !strings.Contains(firstString(evidence["trust"]), "untrusted") || len(evidence["items"].([]findingHandle)) == 0 {
		t.Fatalf("evidence: %v", evidence)
	}

	// Into the cart, and the prompt names the finding and its evidence tool.
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	preview, err := catalog.CartPrompt(ctx, "repo:repo-1", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	prompt := firstString(preview["prompt"])
	for _, want := range []string{"Pharos finding " + id, `get_finding("` + id + `")`, "untrusted data", "Show me the diff before changing anything."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if overview, _ := catalog.FindingsOverview(ctx, ""); overview["summary"].(map[string]any)["watching"] != 0 {
		t.Fatal("a preview must not start measuring")
	}

	// The copy starts measuring and empties the cart.
	if _, err := catalog.CopyCart(ctx, "repo:repo-1", []string{id}, "diff"); err != nil {
		t.Fatal(err)
	}
	overview, _ = catalog.FindingsOverview(ctx, "")
	if overview["summary"].(map[string]any)["watching"] != 1 || len(overview["cart"].([]map[string]any)) != 0 {
		t.Fatalf("after copy: %v cart %v", overview["summary"], overview["cart"])
	}
	interventions, _ := loadInterventions(ctx, catalog.DB, "")
	plan := interventions[0].Plan
	if plan.AfterDays < 7 || plan.AfterDays > findingAfterMaxDays || plan.BeforeRate < 0.4 || plan.BeforeRate > 0.6 {
		t.Fatalf("plan: %+v", plan)
	}

	// The fix works: no more failures after the copy, and the window closes.
	for day := 1; day <= plan.AfterDays+1; day++ {
		fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, day), false)
	}
	later := now.AddDate(0, 0, plan.AfterDays+1)
	catalog.now = func() time.Time { return later }
	if err := catalog.RefreshFindings(ctx, false); err != nil {
		t.Fatal(err)
	}
	interventions, _ = loadInterventions(ctx, catalog.DB, "")
	result := interventions[0]
	if result.Status != interventionImproved {
		t.Fatalf("status %q result %v", result.Status, result.Result)
	}
	if !strings.Contains(firstString(result.Result["sentence"]), "Since the change, it hasn't happened in") {
		t.Fatalf("sentence %q", result.Result["sentence"])
	}
	if result.Savings.Weeks == 0 || result.Savings.Failures <= 0 {
		t.Fatalf("savings %+v", result.Savings)
	}
	overview, _ = catalog.FindingsOverview(ctx, "")
	if overview["summary"].(map[string]any)["won"] != 1 || len(overview["wins"].([]map[string]any)) != 1 {
		t.Fatalf("wins: %v", overview["summary"])
	}
	// A full rebuild keeps the user's state.
	if err := catalog.RefreshFindings(ctx, true); err != nil {
		t.Fatal(err)
	}
	if detail, _ := catalog.FindingDetail(ctx, id); detail == nil || detail["state"] != findingWon {
		t.Fatalf("after rebuild: %v", detail)
	}
}

func TestFindingDismissAndSnooze(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	for back := 27; back >= 0; back-- {
		fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, -back), back%2 == 0)
	}
	catalog := fixture.catalog
	if err := catalog.RefreshFindings(ctx, true); err != nil {
		t.Fatal(err)
	}
	rows, _ := loadFindingRows(ctx, catalog.DB, "WHERE detector='failure'")
	id := ""
	for key := range rows {
		id = key
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "dismiss", "reason": "not_real"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RefreshFindings(ctx, true); err != nil {
		t.Fatal(err)
	}
	if detail, _ := catalog.FindingDetail(ctx, id); detail["state"] != findingDismissed {
		t.Fatalf("dismissed finding came back: %v", detail["state"])
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "snooze", "until": "7d"}); err != nil {
		t.Fatal(err)
	}
	if detail, _ := catalog.FindingDetail(ctx, id); detail["state"] != findingSnoozed {
		t.Fatalf("snoozed: %v", detail["state"])
	}
	catalog.now = func() time.Time { return now.AddDate(0, 0, 8) }
	if detail, _ := catalog.FindingDetail(ctx, id); detail["state"] == findingSnoozed {
		t.Fatal("a 7-day snooze should end")
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "dismiss", "reason": "bogus"}); err == nil {
		t.Fatal("an unknown reason should be refused")
	}
}

func TestFindingAliasesFollowRepositoryMerges(t *testing.T) {
	spec := findingSpec{Detector: "failure", Scope: "repo:new", Pattern: "x"}
	old := findingSpec{Detector: "failure", Scope: "repo:old", Pattern: "x"}
	if spec.id() == old.id() || !strings.HasPrefix(spec.id(), "failure:repo:new:") {
		t.Fatalf("ids %s %s", spec.id(), old.id())
	}
	aliases := map[string]string{old.id(): spec.id()}
	if resolveFindingID(aliases, old.id()) != spec.id() || resolveFindingID(aliases, "other") != "other" {
		t.Fatal("aliases should resolve to the current finding")
	}
}

func TestFindingPromptTargets(t *testing.T) {
	row := &findingRow{ID: "failure:global:h:claude:abc", Card: findingCard{Title: "Agents call `python`, which isn't installed", Explanation: "It happens.",
		Steps: []findingStep{{Lever: "global-instructions", Change: "Tell agents to use `python3`.", Label: "A line in CLAUDE.md"}}}}
	global := renderFindingPrompt("global:h:claude", "All repositories · Claude", "", "pr", []cartItem{{Row: row, Step: row.Card.Steps[0], Attempt: 1}})
	for _, want := range []string{"Claude's global instructions on this Mac (~/.claude/CLAUDE.md)", "Pharos finding failure:global:h:claude:abc", "Show me the diff before changing anything."} {
		if !strings.Contains(global, want) {
			t.Fatalf("global prompt lacks %q:\n%s", want, global)
		}
	}
	repo := renderFindingPrompt("repo:r", "alpha", "/src/alpha", "pr", []cartItem{{Row: row, Step: row.Card.Steps[0], Attempt: 1}})
	if !strings.Contains(repo, "You are working in the alpha repository (/src/alpha).") || !strings.HasSuffix(repo, "Open a pull request.") {
		t.Fatalf("repository prompt:\n%s", repo)
	}
	if strings.Count(repo, "smallest") != 1 {
		t.Fatalf("the prompt asks for the smallest change once:\n%s", repo)
	}
	settings := findingSettings{Handoff: "auto", RepositoryHandoff: map[string]string{"r2": "pr"}}
	habits := map[string]repositoryHabit{"r": {Work: 10, WithPR: 9, Share: 0.9}, "r3": {Work: 10, WithPR: 1, Share: 0.1}}
	for target, want := range map[string]string{"repo:r": "pr", "repo:r2": "pr", "repo:r3": "diff", "repo:none": "diff", "global:h:codex": "diff", "automation:tl1:x": "diff"} {
		if got, _ := handoffFor(target, settings, habits); got != want {
			t.Errorf("handoffFor(%s) = %s, want %s", target, got, want)
		}
	}
}
