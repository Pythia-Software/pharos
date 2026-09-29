package archive

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A win's savings are kept once its 90 days end, though the pass no longer
// holds the weeks they were measured in.
func TestFindingSavingsSurviveTheWindow(t *testing.T) {
	ctx := context.Background()
	fixture, _, plan, now := copiedFinding(t)
	if result := closeWindow(t, fixture, plan, now, func(int) bool { return false }); result.Status != interventionImproved {
		t.Fatalf("status %q", result.Status)
	}
	savings := func(at time.Time) findingSavings {
		t.Helper()
		fixture.catalog.now = func() time.Time { return at }
		if err := fixture.catalog.RefreshFindings(ctx, true); err != nil {
			t.Fatal(err)
		}
		interventions, _ := loadInterventions(ctx, fixture.catalog.DB, "")
		return interventions[0].Savings
	}
	after := dayTime(plan.AfterFrom)
	done := savings(after.AddDate(0, 0, 95))
	if !done.Done || done.Tokens <= 0 || len(done.ByWeek) == 0 {
		t.Fatalf("savings at the end of the 90 days: %+v", done)
	}
	if later := savings(after.AddDate(0, 0, 185)); later.Tokens != done.Tokens || later.USD != done.USD {
		t.Fatalf("savings changed after they were done: %v → %v", done.Tokens, later.Tokens)
	}
}

func TestFindingHandoffEverywhereWins(t *testing.T) {
	settings := findingSettings{Handoff: "pr", RepositoryHandoff: map[string]string{"r1": "diff"}}
	if handoff, _ := handoffFor("repo:r1", settings, nil); handoff != "pr" {
		t.Fatalf("\"always open a pull request\" should win over a repository's choice: %q", handoff)
	}
	settings.Handoff = "auto"
	if handoff, _ := handoffFor("repo:r1", settings, nil); handoff != "diff" {
		t.Fatalf("with auto, the repository's choice applies: %q", handoff)
	}
}

func TestFindingCartRowsUnderEarlierIDs(t *testing.T) {
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
	for _, statement := range []string{"INSERT INTO finding_aliases(alias,finding_id) VALUES('failure:repo:gone:old',?)",
		"INSERT INTO finding_cart(finding_id,target,ticked,added_at) VALUES('failure:repo:gone:old','repo:repo-1',1,'2026-09-27T00:00:00Z')"} {
		if _, err := catalog.DB.Exec(strings.ReplaceAll(statement, "?", "'"+id+"'")); err != nil {
			t.Fatal(err)
		}
	}
	// A full pass keeps the earlier ID and moves the cart row to the
	// current one.
	if err := catalog.RefreshFindings(ctx, true); err != nil {
		t.Fatal(err)
	}
	aliases, _ := findingAliasMap(ctx, catalog.DB)
	if aliases["failure:repo:gone:old"] != id {
		t.Fatalf("the earlier ID was dropped: %v", aliases)
	}
	if _, err := catalog.DB.Exec("UPDATE finding_cart SET finding_id='failure:repo:gone:old'"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_remove"}); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM finding_cart").Scan(&left); err != nil || left != 0 {
		t.Fatalf("a cart row under an earlier ID should be removable: %d %v", left, err)
	}
}

func TestFindingSnoozeUntilWorseStaysWoken(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	for back := 27; back >= 0; back-- {
		fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, -back), back%4 == 0)
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
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "snooze", "until": "until_worse"}); err != nil {
		t.Fatal(err)
	}
	week := func(start time.Time, fail bool) time.Time {
		for day := 1; day <= 7; day++ {
			fixture.conversation(dayTime(start.Format("2006-01-02")).AddDate(0, 0, day), fail)
		}
		later := start.AddDate(0, 0, 7)
		catalog.now = func() time.Time { return later }
		if err := catalog.RefreshFindings(ctx, true); err != nil {
			t.Fatal(err)
		}
		return later
	}
	worse := week(now, true)
	if detail, _ := catalog.FindingDetail(ctx, id); detail["state"] == findingSnoozed {
		t.Fatal("a doubled rate should wake the snooze")
	}
	week(worse, false)
	if detail, _ := catalog.FindingDetail(ctx, id); detail["state"] == findingSnoozed {
		t.Fatal("a woken snooze stays woken when the rate eases")
	}
}

func TestFindingDetectorPatterns(t *testing.T) {
	if a, b := simpleFraction(0.015); a != 1 || b != 67 {
		t.Errorf("1.5%% reads as %d of every %d", a, b)
	}
	for _, signature := range []string{"total 0", "total 64", "total <n>"} {
		if !failureNotice.MatchString(signature) {
			t.Errorf("%q is ls output, not a failure", signature)
		}
	}
	for signature, want := range map[string]string{"(eval):cd:1: no such file or directory: foo": "cd", "ls: foo: No such file or directory": "ls",
		"(eval):<n>: command not found: x": ""} {
		if got := signatureName(signature); got != want {
			t.Errorf("signatureName(%q) = %q, want %q", signature, got, want)
		}
	}
	calls := []failureCall{{Program: "cat"}, {Program: "cat"}}
	if !failureIteration("zsh:1: no such file or directory: notes.txt", calls, false) {
		t.Error("a lowercase missing path while exploring is the agent's own iteration")
	}
	for command, want := range map[string]string{"cat dist/app.js": "generated-file", "cat node_modules/react/package.json": "generated-file",
		"sed -n '1,301p' main.go": "long-range", "sed -n '1,300p' main.go": ""} {
		parsed := parseShellCommand(command).primary()
		if got := heavyShape(parsed.Program, parsed.Subcommand, command); got != want {
			t.Errorf("heavyShape(%q) = %q, want %q", command, got, want)
		}
	}
	if !skillFile.MatchString("/Users/x/repo/.claude/skills/deploy/SKILL.md") || !skillFile.MatchString("cat ./.codex/skills/a/SKILL.md") {
		t.Error("skill files are read by absolute and relative path")
	}
	entries := skillListingEntries("- conductor:conductor: Work in Conductor\n- docx: Word documents\n")
	if entries["conductor:conductor"] == "" || entries["docx"] == "" {
		t.Errorf("plugin skill names keep their namespace: %v", entries)
	}
}

func TestFindingObservationsMerge(t *testing.T) {
	failed, succeeded := false, true
	root := &findingObs{Hit: true, HelpCalls: 1, Calls: 2, Success: &failed, At: "2026-09-28T10:00:00Z"}
	root.merge(&findingObs{UsageErrors: 1, Calls: 3, Success: &succeeded, At: "2026-09-28T11:00:00Z"})
	if root.Calls != 5 || root.HelpCalls != 1 || root.UsageErrors != 1 || !*root.Success || !root.Hit {
		t.Fatalf("merged: %+v", root)
	}
}

func TestFindingDismissAllAndDisableADetector(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.Local)
	fixture := newFindingFixture(t, now)
	for back := 27; back >= 0; back-- {
		fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, -back), back%4 == 0)
	}
	catalog := fixture.catalog
	if err := catalog.RefreshFindings(ctx, true); err != nil {
		t.Fatal(err)
	}
	overview, err := catalog.FindingsOverview(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	cards := mapSlice(overview["findings"])
	if len(cards) == 0 {
		t.Fatal("the fixture should produce findings")
	}
	id := firstString(cards[0]["id"])
	detector := firstString(cards[0]["detector"])
	if detector == "" || firstString(cards[0]["detector_label"]) == "" {
		t.Fatalf("a card names its detector: %v", cards[0])
	}
	open := func() int {
		overview, err := catalog.FindingsOverview(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return int(integer(mapValueDefault(overview["summary"])["open"]))
	}
	before := open()
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "disable_detector"}); err != nil {
		t.Fatal(err)
	}
	afterOff := open()
	if afterOff >= before {
		t.Fatalf("turning a detector off should hide its findings: %d before, %d after", before, afterOff)
	}
	if err := catalog.SetFindingSettings(ctx, map[string]any{"detector_enabled": map[string]any{detector: true}}); err != nil {
		t.Fatal(err)
	}
	if open() != before {
		t.Fatal("turning the detector back on brings its findings back")
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "dismiss_detector"}); err != nil {
		t.Fatal(err)
	}
	remaining := open()
	overview, _ = catalog.FindingsOverview(ctx, "open")
	for _, card := range mapSlice(overview["findings"]) {
		if firstString(card["detector"]) == detector {
			t.Fatalf("every open finding from %s should be dismissed, but %v is open (%d open)", detector, card["id"], remaining)
		}
	}
	if remaining >= before {
		t.Fatalf("dismissing a detector should leave fewer open findings: %d before, %d after", before, remaining)
	}
	if err := catalog.SetFindingSettings(ctx, map[string]any{"detector_enabled": map[string]any{"nonsense": false}}); err == nil {
		t.Fatal("an unknown detector is an error")
	}
}
