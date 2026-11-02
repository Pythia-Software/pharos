package archive

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func TestFindingFixKind(t *testing.T) {
	for _, item := range []struct{ lever, label, want string }{
		{"repo-instructions", "A line in AGENTS.md", "Repository instructions"},
		{"something-new", "A line", "Other change"},
		{"", "An entry in settings.json", "Entry in settings.json"},
		{"", "one script", "Script"},
		{"", "", "Other change"},
	} {
		if got := findingFixKind(item.lever, item.label); got != item.want {
			t.Errorf("findingFixKind(%q, %q) = %q, want %q", item.lever, item.label, got, item.want)
		}
	}
}

// The findings query table carries the Findings view's own values: its open
// rows match /api/findings?state=open, and its results sum to the summary.
func TestFindingsQueryTableMatchesTheView(t *testing.T) {
	ctx := context.Background()
	fixture, id, plan, now := copiedFinding(t)
	if result := closeWindow(t, fixture, plan, now, func(int) bool { return false }); result.Status != interventionImproved {
		t.Fatalf("status %q", result.Status)
	}
	// The win regresses: open again, its savings kept.
	decided := now.AddDate(0, 0, plan.AfterDays+1)
	for day := 1; day <= 14; day++ {
		fixture.conversation(dayTime(decided.Format("2006-01-02")).AddDate(0, 0, day), true)
	}
	fixture.catalog.now = func() time.Time { return decided.AddDate(0, 0, 15) }
	if err := fixture.catalog.RefreshFindings(ctx, false); err != nil {
		t.Fatal(err)
	}
	_, config := testCatalog(t)
	server := testServer(t, config, fixture.catalog)
	call := func(method, path string, body any, into any) {
		t.Helper()
		w := apiCall(t, server, config.APIToken, method, path, body)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), into) != nil {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	near := func(a, b any) bool { return math.Abs(floatOr(a)-floatOr(b)) < 1e-9 }

	var overview struct {
		Settings struct{ Rank string } `json:"settings"`
		Summary  map[string]any        `json:"summary"`
		Findings []map[string]any      `json:"findings"`
	}
	call(http.MethodGet, "/api/findings?state=open", nil, &overview)
	if len(overview.Findings) == 0 {
		t.Fatal("no open findings")
	}
	schema, _ := sqliteQuerySchema("findings")
	fields := []string{}
	for name := range schema.Fields {
		fields = append(fields, name)
	}
	var rows struct {
		Rows []map[string]any `json:"rows"`
	}
	call(http.MethodPost, "/api/query/findings/rows-v2", qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile,
		WireQuery: qt.WireQuery{Select: fields, Where: []qt.WhereTerm{{Field: "state", Op: "=", Value: "open"}}, Limit: 100}}, &rows)
	if len(rows.Rows) != len(overview.Findings) {
		t.Fatalf("%d open rows, %d open cards", len(rows.Rows), len(overview.Findings))
	}
	byID := map[string]map[string]any{}
	for _, row := range rows.Rows {
		byID[firstString(row["id"])] = row
	}
	for _, card := range overview.Findings {
		row := byID[firstString(card["id"])]
		if row == nil || row["state"] != card["state"] || row["title"] != card["title"] || row["detector_label"] != card["detector_label"] ||
			firstString(row["lever"]) != firstString(card["lever"]) || row["lever_label"] != findingFixKind(firstString(card["lever"]), firstString(card["change_label"])) ||
			firstString(row["target_label"]) != firstString(card["target_label"]) || (row["is_new"] == true) != (card["new"] == true) || row["rank_unit"] != overview.Settings.Rank {
			t.Fatalf("row %v\ncard %v", row, card)
		}
		expected, impact := mapValueDefault(card["expected"]), mapValueDefault(card["impact"])
		for _, unit := range findingImpactUnits {
			if !near(row["likely_"+unit], expected[unit]) || !near(row["at_stake_"+unit], impact[unit]) {
				t.Fatalf("%s: row %v card expected %v impact %v", unit, row, expected, impact)
			}
		}
		if !near(row["likely_ranked"], expected[overview.Settings.Rank]) {
			t.Fatalf("ranked: %v", row)
		}
	}
	if row := byID[id]; row == nil || row["regressed"] != true || floatOr(row["wins"]) != 1 || floatOr(row["saved_tokens"]) <= 0 || row["repository_name"] != "alpha" {
		t.Fatalf("regressed win: %v", row)
	}

	// The Net savings card's sums, before and after the finding is
	// dismissed: the summary keeps a dismissed finding's results.
	results := func() {
		t.Helper()
		var summary struct {
			Summary struct {
				Open  int            `json:"open"`
				Saved findingSavings `json:"saved"`
			} `json:"summary"`
		}
		call(http.MethodGet, "/api/findings?state=summary", nil, &summary)
		var metrics qt.SQLiteMetricsV2Result
		call(http.MethodPost, "/api/query/findings/metrics", metricRequest(
			qt.AggSpec{ID: "usd", Expression: "SUM([saved_usd])"}, qt.AggSpec{ID: "added", Expression: "SUM([saved_added_usd])"},
			qt.AggSpec{ID: "net", Expression: "SUM([saved_net_usd])"}, qt.AggSpec{ID: "tokens", Expression: "SUM([saved_tokens])"},
			qt.AggSpec{ID: "weeks", Expression: "MAX([saved_weeks])"}, qt.AggSpec{ID: "open", Expression: "COUNT()", Where: []qt.WhereTerm{{Field: "state", Op: "=", Value: "open"}}}), &metrics)
		saved := summary.Summary.Saved
		want := []float64{saved.USD, saved.AddedUSD, saved.USD - saved.AddedUSD, saved.Tokens, float64(saved.Weeks), float64(summary.Summary.Open)}
		for index, metric := range metrics.Metrics {
			if metric.Error != "" || len(metric.Buckets) != 1 || metric.Buckets[0].Error != "" {
				t.Fatalf("%s: %+v", metric.ID, metric)
			}
			if got := floatOr(metric.Buckets[0].Value); math.Abs(got-want[index]) > 1e-9 {
				t.Fatalf("%s = %v, summary %v", metric.ID, got, want[index])
			}
		}
		if saved.Tokens <= 0 || saved.Weeks == 0 {
			t.Fatalf("no savings: %+v", saved)
		}
	}
	results()
	if err := fixture.catalog.FindingAction(ctx, id, map[string]any{"action": "dismiss", "reason": "not_real"}); err != nil {
		t.Fatal(err)
	}
	results()
}

// An unbuilt catalog has no findings rows, and reading them starts no build.
func TestFindingsQueryTableEmpty(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	spawned := false
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { spawned = true; return true }
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/findings/rows-v2",
		qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "state"}, Limit: 10}})
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Rows) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	if spawned {
		t.Fatal("reading rows must not start a findings build")
	}
}
