package archive

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFindingProvisionalResult(t *testing.T) {
	for _, test := range []struct {
		name, kind, signal string
		after, hits        int
		tokens             int64
	}{
		{"enough work supports improvement", "rate", "promising", 8, 0, 100},
		{"share uses token exposure", "share", "promising", 8, 0, 100},
		{"mean uses median and unit values", "mean", "promising", 8, 0, 25},
		{"little work only describes direction", "rate", "lower", 2, 0, 100},
		{"no work is unknown", "rate", "too_early", 0, 0, 100},
		{"unchanged", "rate", "similar", 8, 4, 100},
		{"higher", "rate", "higher", 8, 8, 100},
		{"guard blocks reassuring signal", "rate", "mixed", 8, 0, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := dayTime("2026-11-10")
			env := &findingEnv{now: now, today: "2026-11-10", from: now.AddDate(0, 0, -90), convs: map[string]*findingConversation{}}
			metric := findingMetric{Kind: test.kind, Unit: "conversations"}
			candidate := &findingCandidate{Metric: metric, Obs: map[string]*findingObs{}}
			add := func(prefix, day string, count, hits int, tokens int64) {
				for index := 0; index < count; index++ {
					id := fmt.Sprintf("%s-%d", prefix, index)
					env.convs[id] = &findingConversation{ID: id, Day: day, Tokens: tokens}
					candidate.Obs[id] = &findingObs{Conversation: id, Day: day, Hit: index < hits, Value: float64(tokens), Total: float64(tokens)}
				}
			}
			add("before", "2026-10-20", 8, 4, 100)
			add("after", "2026-11-03", test.after, test.hits, test.tokens)
			// Incomplete today must not leak into a provisional comparison.
			add("today", env.today, 8, 8, 10000)
			baseline := 0.5
			if test.kind == "mean" {
				baseline = 100
			}
			exposure := 8.0
			if test.kind == "share" {
				exposure *= 100
			}
			item := &findingIntervention{Status: interventionWatching, Plan: findingPlan{BeforeFrom: "2026-10-05", AfterFrom: "2026-11-02", AfterTo: "2026-11-17", Metric: metric,
				BeforeRate: baseline, BeforeExposure: exposure, USDPerEvent: 1, TokensPerEvent: 10, Persists: 0.8}}
			progress := env.provisionalResult(candidate, item)
			if progress["signal"] != test.signal || progress["after"].(windowStats).Units != test.after || progress["through"] != "2026-11-09" {
				t.Fatalf("provisional read: %+v", progress)
			}
			if item.Status != interventionWatching || item.Plan.AfterTo != "2026-11-17" || progress["outcome"] != nil {
				t.Fatalf("an early read changed the fixed result: %+v", item)
			}
		})
	}
}

func TestFindingProvisionalSavingsBeforeFirstWeek(t *testing.T) {
	ctx := context.Background()
	fixture, id, plan, now := copiedFinding(t)
	// Three complete days are enough to show a provisional estimate, but
	// neither a win nor full-week savings should be recorded yet.
	for day := 1; day <= 3; day++ {
		fixture.conversation(dayTime(plan.AfterFrom).AddDate(0, 0, day-1), false)
	}
	fixture.catalog.now = func() time.Time { return now.AddDate(0, 0, 4) }
	if err := fixture.catalog.RefreshFindings(ctx, false); err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.catalog.FindingDetail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	progress := detail["watching"].(map[string]any)["provisional"].(map[string]any)
	saved := progress["savings"].(map[string]any)
	if floatOr(saved["tokens"]) <= 0 || progress["after"].(map[string]any)["units"] != float64(3) {
		t.Fatalf("missing first-days estimate: %+v", progress)
	}
	items, _ := loadInterventions(ctx, fixture.catalog.DB, "")
	if detail["state"] != findingWatching || items[0].Plan.AfterTo != plan.AfterTo || items[0].DecidedAt != "" || items[0].Savings.Weeks != 0 || detail["result"] != nil {
		t.Fatalf("provisional estimate finalized the attempt: %+v", items[0])
	}
	overview, _ := fixture.catalog.FindingsOverview(ctx, "")
	if summary := overview["summary"].(map[string]any); summary["won"] != 0 || summary["saved"].(findingSavings).USD != 0 {
		t.Fatalf("provisional savings entered confirmed totals: %+v", summary)
	}
	// The fixed-date decision replaces, rather than retaining, the snapshot.
	result := closeWindow(t, fixture, plan, now, func(int) bool { return false })
	if result.Status != interventionImproved || result.Result["provisional"] != nil {
		t.Fatalf("final result did not replace early read: %+v", result)
	}
}

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
	if detail["lever"] != steps[1].Lever || steps[1].Lever == "" {
		t.Fatalf("the card names the kind of change it proposes: %q, want %q", detail["lever"], steps[1].Lever)
	}
	if !strings.Contains(firstString(detail["prompt"]), "This is attempt 2") {
		t.Fatalf("the prompt says an earlier change didn't help:\n%s", detail["prompt"])
	}
	// Time alone must retire the recommendation between detector passes,
	// without deleting its prior attempt or claiming the change worked.
	later := fixture.catalog.clock().AddDate(0, 0, findingRecentDays)
	fixture.catalog.now = func() time.Time { return later }
	detail, err := fixture.catalog.FindingDetail(context.Background(), id)
	if err != nil || detail["state"] != findingHidden || detail["reopened"] == nil {
		t.Fatalf("quiet finding: state=%v reopened=%v err=%v", detail["state"], detail["reopened"], err)
	}
	overview, err := fixture.catalog.FindingsOverview(context.Background(), findingOpen)
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range overview["findings"].([]map[string]any) {
		if card["id"] == id {
			t.Fatal("quiet finding is still recommended")
		}
	}
	// A fresh recurrence can qualify again using the same finding ID.
	for day := 0; day < findingRecentDays; day++ {
		fixture.conversation(dayTime(later.Format("2006-01-02")).AddDate(0, 0, -day), true)
	}
	if err := fixture.catalog.RefreshFindings(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	detail, err = fixture.catalog.FindingDetail(context.Background(), id)
	if err != nil || detail["state"] != findingOpen || detail["attempt"] != 2 {
		t.Fatalf("recurring finding: state=%v attempt=%v err=%v", detail["state"], detail["attempt"], err)
	}
}

func TestFindingWorseAsksToUndo(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		name := "recent evidence"
		if quiet {
			name = "expired evidence"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture, id, plan, now := copiedFinding(t)
			result := closeWindow(t, fixture, plan, now, func(int) bool { return true })
			if result.Status != interventionWorse {
				t.Fatalf("status %q", result.Status)
			}
			catalog := fixture.catalog
			if quiet {
				later := catalog.clock().AddDate(0, 0, findingRecentDays)
				catalog.now = func() time.Time { return later }
			}
			detail, err := catalog.FindingDetail(ctx, id)
			if err != nil || detail["state"] != findingOpen || detail["undo"] == nil || !strings.Contains(firstString(detail["prompt"]), "revert it") {
				t.Fatalf("pending undo: state=%v undo=%v err=%v", detail["state"], detail["undo"] != nil, err)
			}
			overview, err := catalog.FindingsOverview(ctx, findingOpen)
			if err != nil {
				t.Fatal(err)
			}
			visible := false
			for _, card := range overview["findings"].([]map[string]any) {
				if card["id"] == id && card["undo"] != nil {
					visible = true
				}
			}
			if !visible {
				t.Fatal("pending undo disappeared from open findings")
			}
			if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_add"}); err != nil {
				t.Fatal(err)
			}
			copied, err := catalog.CopyCart(ctx, "repo:repo-1", []string{id}, "pr")
			if err != nil || !strings.Contains(firstString(copied["prompt"]), "revert it") {
				t.Fatalf("copy undo prompt: count=%v err=%v", copied["count"], err)
			}
			want := findingOpen
			if quiet {
				want = findingHidden
			}
			detail, err = catalog.FindingDetail(ctx, id)
			if err != nil || detail["undo"] != nil || detail["state"] != want {
				t.Fatalf("after undo: state=%v, want %v; undo=%v err=%v", detail["state"], want, detail["undo"] != nil, err)
			}
		})
	}
}

func TestFindingInconclusiveWithoutWork(t *testing.T) {
	fixture, id, plan, now := copiedFinding(t)
	if result := closeWindow(t, fixture, plan, now, nil); result.Status != interventionInconclusive {
		t.Fatalf("no work after the copy is inconclusive: %q", result.Status)
	}
	detail, err := fixture.catalog.FindingDetail(context.Background(), id)
	if err != nil || detail["state"] != findingHidden {
		t.Fatalf("an inconclusive result without recent evidence must not reopen: state=%v err=%v", detail["state"], err)
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
	later := fixture.catalog.clock().AddDate(0, 0, findingRecentDays)
	fixture.catalog.now = func() time.Time { return later }
	detail, err := fixture.catalog.FindingDetail(ctx, id)
	if err != nil || detail["state"] != findingHidden {
		t.Fatalf("expired recommendation without a detector pass: state=%v err=%v", detail["state"], err)
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
	before, err := fixture.catalog.FindingsOverview(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeSummary := before["summary"].(map[string]any)
	saved := beforeSummary["saved"].(findingSavings)
	if saved.Tokens <= 0 || integer(beforeSummary["regressed"]) != 1 {
		t.Fatalf("regressed win must have saved history: %v", beforeSummary)
	}
	later := fixture.catalog.clock().AddDate(0, 0, findingRecentDays)
	fixture.catalog.now = func() time.Time { return later }
	// Both the complete overview and the header's summary-only poll keep
	// every historical total when the recommendation becomes hidden.
	for _, filter := range []string{"", "summary"} {
		after, err := fixture.catalog.FindingsOverview(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		summary := after["summary"].(map[string]any)
		if !reflect.DeepEqual(summary["saved"], saved) || integer(summary["regressed"]) != 1 {
			t.Fatalf("hidden finding lost historical results (%q): %v, want saved %+v", filter, summary, saved)
		}
		if filter == "" {
			for _, card := range after["findings"].([]map[string]any) {
				if card["id"] == id {
					t.Fatal("a quiet regressed win should not be recommended")
				}
			}
			wins := after["wins"].([]map[string]any)
			if len(wins) != 1 || wins[0]["id"] != id || wins[0]["saved"].(findingSavings).Tokens != saved.Tokens {
				t.Fatalf("wins and summary disagree: %v", wins)
			}
		}
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

func TestFindingCartCloneAndReorder(t *testing.T) {
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
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for key := range rows {
		id = key
		break
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_add"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_clone"}); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.FindingsOverview(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	group := view["cart"].([]map[string]any)[0]
	items := group["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("cart items: %v", items)
	}
	cloneID := firstString(items[1]["id"])
	if !strings.HasPrefix(cloneID, "clone:") || items[1]["ticked"] != false {
		t.Fatalf("clone: %v", items[1])
	}
	if err := catalog.FindingAction(ctx, id, map[string]any{"action": "cart_reorder", "target": group["target"], "ids": []any{cloneID, id}}); err != nil {
		t.Fatal(err)
	}
	view, _ = catalog.FindingsOverview(ctx, "")
	items = view["cart"].([]map[string]any)[0]["items"].([]map[string]any)
	if items[0]["id"] != cloneID {
		t.Fatalf("reordered items: %v", items)
	}
	if err := catalog.FindingAction(ctx, cloneID, map[string]any{"action": "cart_move", "target": "global:host-a:claude"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.FindingAction(ctx, cloneID, map[string]any{"action": "cart_tick", "ticked": true}); err != nil {
		t.Fatal(err)
	}
	view, _ = catalog.FindingsOverview(ctx, "")
	if len(view["cart"].([]map[string]any)) != 2 {
		t.Fatalf("cart groups: %v", view["cart"])
	}
	preview, err := catalog.CartPrompt(ctx, "global:host-a:claude", []string{cloneID}, "diff")
	if err != nil || preview["count"] != 1 {
		t.Fatalf("clone prompt: %v %v", preview, err)
	}
	if _, err := catalog.CopyCart(ctx, "global:host-a:claude", []string{cloneID}, "diff"); err != nil {
		t.Fatal(err)
	}
	view, _ = catalog.FindingsOverview(ctx, "")
	if len(view["cart"].([]map[string]any)) != 1 {
		t.Fatalf("original cart item after copying clone: %v", view["cart"])
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
