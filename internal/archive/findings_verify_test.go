package archive

import (
	"context"
	"strings"
	"testing"
	"time"
)

// copiedFinding builds four weeks of work where every other conversation
// fails, copies the python finding's prompt, and returns its ID and plan.
func copiedFinding(t *testing.T) (*findingFixture, string, findingPlan, time.Time) {
	t.Helper()
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
	rows, err := loadFindingRows(ctx, catalog.DB, "WHERE detector='failure'")
	if err != nil || len(rows) != 1 {
		t.Fatalf("failure findings: %v %v", rows, err)
	}
	id := ""
	for key := range rows {
		id = key
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.CopyCart(ctx, "repo:repo-1", []string{id}, "pr"); err != nil {
		t.Fatal(err)
	}
	interventions, _ := loadInterventions(ctx, catalog.DB, "")
	return fixture, id, interventions[0].Plan, now
}

// closeWindow adds the after window's conversations, moves the clock past
// it, and measures.
func closeWindow(t *testing.T, fixture *findingFixture, plan findingPlan, now time.Time, fail func(day int) bool) *findingIntervention {
	t.Helper()
	for day := 1; day <= plan.AfterDays; day++ {
		if fail != nil {
			fixture.conversation(dayTime(now.Format("2006-01-02")).AddDate(0, 0, day), fail(day))
		}
	}
	later := now.AddDate(0, 0, plan.AfterDays+1)
	fixture.catalog.now = func() time.Time { return later }
	if err := fixture.catalog.RefreshFindings(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	interventions, _ := loadInterventions(context.Background(), fixture.catalog.DB, "")
	return interventions[len(interventions)-1]
}

func TestFindingUnchangedMovesToTheNextChange(t *testing.T) {
	fixture, id, plan, now := copiedFinding(t)
	result := closeWindow(t, fixture, plan, now, func(day int) bool { return day%2 == 0 })
	if result.Status != interventionUnchanged {
		t.Fatalf("status %q %v", result.Status, result.Result)
	}
	detail, _ := fixture.catalog.FindingDetail(context.Background(), id)
	if detail["state"] != findingOpen || detail["attempt"] != 2 || detail["reopened"] == nil {
		t.Fatalf("an unchanged finding reopens with its next attempt: state=%v attempt=%v", detail["state"], detail["attempt"])
	}
	steps := detail["steps"].([]findingStep)
	if detail["change"] != steps[1].Change {
		t.Fatalf("the next attempt proposes the stronger change: %q", detail["change"])
	}
	if !strings.Contains(firstString(detail["prompt"]), "This is attempt 2") {
		t.Fatalf("the prompt says an earlier change didn't help:\n%s", detail["prompt"])
	}
}

func TestFindingWorseAsksToUndo(t *testing.T) {
	ctx := context.Background()
	fixture, id, plan, now := copiedFinding(t)
	result := closeWindow(t, fixture, plan, now, func(int) bool { return true })
	if result.Status != interventionWorse {
		t.Fatalf("status %q %v", result.Status, result.Result)
	}
	catalog := fixture.catalog
	detail, _ := catalog.FindingDetail(ctx, id)
	if detail["undo"] == nil || !strings.Contains(firstString(detail["prompt"]), "revert it") {
		t.Fatalf("a worse result asks to undo the change: %v\n%s", detail["undo"], detail["prompt"])
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.CopyCart(ctx, "repo:repo-1", []string{id}, "pr"); err != nil {
		t.Fatal(err)
	}
	detail, _ = catalog.FindingDetail(ctx, id)
	if detail["undo"] != nil || detail["state"] != findingOpen {
		t.Fatalf("after copying the undo, the finding is open with its next change: %v %v", detail["undo"], detail["state"])
	}
}

func TestFindingInconclusiveWithoutWork(t *testing.T) {
	fixture, _, plan, now := copiedFinding(t)
	if result := closeWindow(t, fixture, plan, now, nil); result.Status != interventionInconclusive {
		t.Fatalf("no work after the copy is inconclusive: %q", result.Status)
	}
}

func TestFindingNotAppliedWithdraws(t *testing.T) {
	ctx := context.Background()
	fixture, id, _, _ := copiedFinding(t)
	if err := fixture.catalog.FindingAction(ctx, id, map[string]any{"action": "not_applied"}); err != nil {
		t.Fatal(err)
	}
	detail, _ := fixture.catalog.FindingDetail(ctx, id)
	if detail["state"] != findingOpen || detail["watching"] != nil || detail["attempt"] != 1 {
		t.Fatalf("withdrawn: state=%v watching=%v attempt=%v", detail["state"], detail["watching"], detail["attempt"])
	}
}

func TestFindingWinRegresses(t *testing.T) {
	ctx := context.Background()
	fixture, id, plan, now := copiedFinding(t)
	if result := closeWindow(t, fixture, plan, now, func(int) bool { return false }); result.Status != interventionImproved {
		t.Fatalf("status %q", result.Status)
	}
	// The pattern comes back two weeks later.
	decided := now.AddDate(0, 0, plan.AfterDays+1)
	for day := 1; day <= 14; day++ {
		fixture.conversation(dayTime(decided.Format("2006-01-02")).AddDate(0, 0, day), true)
	}
	fixture.catalog.now = func() time.Time { return decided.AddDate(0, 0, 15) }
	if err := fixture.catalog.RefreshFindings(ctx, false); err != nil {
		t.Fatal(err)
	}
	interventions, _ := loadInterventions(ctx, fixture.catalog.DB, "")
	if interventions[0].RegressedAt == "" || interventions[0].Savings.Weeks == 0 {
		t.Fatalf("the win should regress and keep its savings: %+v", interventions[0])
	}
	detail, _ := fixture.catalog.FindingDetail(ctx, id)
	if detail["state"] != findingOpen || detail["regressed"] == nil {
		t.Fatalf("a regressed win reopens: %v", detail["state"])
	}
}

func TestFindingCartMoveAndTick(t *testing.T) {
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
	for _, body := range []map[string]any{{"action": "cart_add"}, {"action": "cart_move", "target": "global:host-a:claude"}, {"action": "cart_tick", "ticked": false}} {
		if err := catalog.FindingAction(ctx, id, body); err != nil {
			t.Fatal(err)
		}
	}
	overview, _ := catalog.FindingsOverview(ctx, "")
	carts := overview["cart"].([]map[string]any)
	if len(carts) != 1 || carts[0]["target"] != "global:host-a:claude" || carts[0]["ticked"] != 0 || carts[0]["handoff"] != "diff" {
		t.Fatalf("cart: %v", carts)
	}
	if _, err := catalog.CopyCart(ctx, "global:host-a:claude", nil, "pr"); err == nil {
		t.Fatal("copying nothing ticked should be refused")
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_move", "target": "nowhere"}); err == nil {
		t.Fatal("an unknown target should be refused")
	}
}

func TestFindingCheckpointsRecommend(t *testing.T) {
	row := func(affected, exposure int) *findingRow {
		return &findingRow{ID: stableID("row", affected, exposure), Active: true, Metric: findingMetric{Kind: "rate", Unit: "conversations"},
			Stats: findingStats{Affected: affected, Exposure: exposure, Recent: 2, Live: 20, Days: 8, Workspaces: 8, DailyExposure: float64(exposure) / 28}}
	}
	view := &findingView{rows: map[string]*findingRow{}, states: map[string]*findingUserState{}}
	// Small baselines pass a low threshold but can't show a clear result.
	for _, item := range []*findingRow{row(40, 200), row(25, 150), row(12, 100), row(6, 400), row(7, 400), row(5, 300), row(3, 50)} {
		view.rows[item.ID] = item
	}
	checks, recommended := findingCheckpointPreview(view, 500)
	if checks[0]["shown"] != 7 || checks[1]["shown"] != 6 || checks[2]["shown"] != 3 {
		t.Fatalf("checkpoints: %v", checks)
	}
	if recommended != 10 {
		t.Fatalf("recommended %d: %v", recommended, checks)
	}
	if !findingDetectable(row(10, 100)) || findingDetectable(row(4, 100)) {
		t.Fatal("a baseline of 10 can show a 70% drop; 4 can't")
	}
}
