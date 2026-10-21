package archive

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/carbon"
)

// The shipped factors file must satisfy its own contract.
func TestCarbonFactorsFileIsValid(t *testing.T) {
	doc, problems := parseCarbon(carbon.Factors)
	if len(problems) > 0 {
		t.Fatalf("carbon/co2_factors.json:\n%s", strings.Join(problems, "\n"))
	}
	for model, want := range map[string]string{
		"claude-haiku-4-5-20251001": "small", "haiku": "small", "gpt-5.3-codex-spark": "small", "gpt-6-luna": "small",
		"claude-sonnet-5": "medium", "gpt-5.3-codex": "medium", "gpt-6-sol": "medium",
		"claude-opus-5-5[1m]": "large", "opus": "large", "claude-fable-5-1": "large", "gpt-6-astra": "large", "gpt-5-pro": "large",
		"gpt-5-mini": "small", "gemini-2.5-pro": "large",
	} {
		if got, _ := doc.tier(model); got != want {
			t.Errorf("tier(%q) = %q, want %q", model, got, want)
		}
	}
	if tier, matched := doc.tier("mystery-model"); matched || tier != doc.DefaultTier {
		t.Errorf("unrecognized model: %q %v", tier, matched)
	}
}

func TestCarbonValidationRejectsUncitedAndDisorderedFactors(t *testing.T) {
	doc := testCarbon()
	doc.Tiers[0].OutputWhPerMTok = carbonRange{Low: 3, Central: 2, High: 4}
	doc.Tiers[0].Sources = []string{"missing"}
	doc.PUE.Low = 0.9
	problems := strings.Join(validateCarbon(doc), "\n")
	for _, want := range []string{"tiers[0] small: need 0 < low ≤ central ≤ high", `unknown source "missing"`, "pue: cannot be below 1"} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing %q in:\n%s", want, problems)
		}
	}
}

// Energy is tokens ÷ 1M × the tier's Wh per 1M output tokens × the category
// ratio; emissions multiply by PUE and the default grid.
func TestCarbonEnergyArithmetic(t *testing.T) {
	doc := testCarbon()
	window := &carbonWindow{Tokens: map[string]map[string]int64{
		"small": {"uncached_input": 2_000_000, "cache_read": 10_000_000, "output": 1_000_000},
		"large": {"cache_write": 1_000_000, "output": 500_000},
	}}
	doc.energy(window)
	// small: 2×100×0.1 + 10×100×0.01 + 1×100 = 20 + 10 + 100 = 130 Wh
	// large: 1×1000×0.2 + 0.5×1000 = 200 + 500 = 700 Wh
	central := window.EnergyWh["central"]
	for key, want := range map[string]float64{"uncached_input": 20, "cache_read": 10, "cache_write": 200, "output": 600, "total": 830} {
		if math.Abs(central[key]-want) > 1e-9 {
			t.Errorf("central %s = %v, want %v", key, central[key], want)
		}
	}
	// 830 Wh × PUE 1.2 × 400 g/kWh = 398.4 g
	if got := window.CO2eGrams["central"]; math.Abs(got-398.4) > 1e-9 {
		t.Errorf("central CO2e = %v, want 398.4", got)
	}
	if !(window.CO2eGrams["low"] < window.CO2eGrams["central"] && window.CO2eGrams["central"] < window.CO2eGrams["high"]) {
		t.Errorf("scenarios out of order: %v", window.CO2eGrams)
	}
}

func TestCarbonLinkedQueryAssumptions(t *testing.T) {
	c, config := testCatalog(t)
	ingestUsageFixture(t, c)
	if _, err := c.DB.Exec(`INSERT INTO ui_preferences(key,value) VALUES('pharos-carbon-v1','{"scenario":"high","grid":"custom","customGrid":900}')`); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, config, c)
	request := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"co2e_g"}, Limit: 100}}
	read := func(settings string) float64 {
		t.Helper()
		result, err := s.executeQueryV2(t.Context(), "usage", url.Values{"carbon_settings": {settings}}, PharosUsageSchema(), &request, nil)
		if err != nil {
			t.Fatal(err)
		}
		var sum float64
		for _, row := range result.Rows.Rows {
			n, _ := number(row["co2e_g"])
			sum += n
		}
		return sum
	}
	saved := read("")
	linked := read(`{"scenario":"high","grid":"custom","customGrid":100}`)
	if saved <= 0 || math.Abs(linked*9-saved) > 1e-8 {
		t.Fatalf("linked estimate=%v saved=%v", linked, saved)
	}
	q := groupRequest("provider", qt.SummaryColumn{ID: "co2", Op: "sum", Field: "co2e_g"})
	grouped, _, err := s.executeQueryV3(t.Context(), "usage", url.Values{"carbon_settings": {`{"scenario":"high","grid":"custom","customGrid":100}`}}, PharosUsageSchema(), q)
	if err != nil || math.Abs(grouped.Rows[0]["co2"].(float64)-linked) > 1e-8 {
		t.Fatalf("grouped estimate=%+v err=%v", grouped, err)
	}
	itemRequest := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "co2e_g"}, Limit: 1}}
	assumptions := `{"scenario":"high","grid":"custom","customGrid":100}`
	itemRows, itemErr := s.executeQueryV2(t.Context(), "usage", url.Values{"carbon_settings": {assumptions}}, PharosUsageSchema(), &itemRequest, nil)
	if itemErr != nil || len(itemRows.Rows.Rows) != 1 {
		t.Fatal(itemErr)
	}
	source := itemRows.Rows.Rows[0]
	response := queryExplorationCall(s, config.APIToken, "GET", "/api/query/usage/item?"+url.Values{"id": {firstString(source["id"])}, "carbon_settings": {assumptions}}.Encode(), "")
	var item struct {
		Row map[string]any `json:"row"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &item) != nil || item.Row["co2e_g"] != source["co2e_g"] {
		t.Fatalf("Details lost linked assumptions: %d %s", response.Code, response.Body.String())
	}
	if read("") != saved {
		t.Fatal("following a link changed saved assumptions")
	}
	for _, invalid := range []string{"[]", "null", "{", `{"scenario":"invalid"}`, `{"customGrid":-1}`} {
		if _, err := withCarbonPreferences(context.Background(), invalid); err == nil {
			t.Fatalf("accepted invalid assumptions %s", invalid)
		}
	}
}

func TestCarbonHealthSplitsWindowsAndCategories(t *testing.T) {
	catalog, _ := testCatalog(t)
	// Codex usage: 3000 input of which 2300 cached, 50 output, no model.
	ingestUsageFixture(t, catalog)
	catalog.now = func() time.Time { return time.Date(2026, 9, 21, 2, 15, 0, 0, time.UTC) }
	health, err := catalog.carbonHealth()
	if err != nil {
		t.Fatal(err)
	}
	windows := health["windows"].([]*carbonWindow)
	if len(windows) != 2 || windows[0].Key != "all" || windows[1].Key != "30d" {
		t.Fatalf("windows: %#v", windows)
	}
	doc := health["factors"].(carbonDocument)
	tokens := windows[0].Tokens[doc.DefaultTier]
	if tokens["uncached_input"] != 700 || tokens["cache_read"] != 2300 || tokens["cache_write"] != 0 || tokens["output"] != 50 {
		t.Fatalf("tokens: %#v", windows[0].Tokens)
	}
	if windows[1].Tokens[doc.DefaultTier]["output"] != 50 {
		t.Fatalf("last 30 days: %#v", windows[1].Tokens)
	}
	if windows[0].CO2eGrams["central"] <= 0 {
		t.Fatalf("no emissions: %#v", windows[0])
	}
	// Usage with no model is a placeholder, not an unrecognized model.
	if models := health["default_tier_models"].([]map[string]any); len(models) != 0 {
		t.Fatalf("default-tier models: %#v", models)
	}
	catalog.now = func() time.Time { return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) }
	health, err = catalog.carbonHealth()
	if err != nil {
		t.Fatal(err)
	}
	if recent := health["windows"].([]*carbonWindow)[1]; len(recent.Tokens) != 0 || recent.EnergyWh["central"]["total"] != 0 {
		t.Fatalf("usage older than 30 days counted as recent: %#v", recent)
	}
}

func TestCarbonSeriesFiltersAndHourlyTotals(t *testing.T) {
	previousLocation := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previousLocation })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('carbon-repo','example/repo','','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`UPDATE workspaces SET repository_id='carbon-repo'`); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	for _, sample := range []struct {
		name, where, split string
		want, points       int64
	}{
		{"all", `[]`, "", 3150, 2},
		{"repository and provider", `[{"field":"repository_name","op":"=","value":"example/repo"},{"field":"provider","op":"=","value":"codex"}]`, "provider", 3150, 2},
		// Both requests on Sep 21 share a daily row. The date bounds must
		// count just the first hour, even when charting whole days.
		{"partial day with offset", `[{"field":"last_usage_at","op":">=","value":"2026-09-20T19:00:00-06:00"},{"field":"first_usage_at","op":"<","value":"2026-09-20T21:00:00-06:00"}]`, "repository_name", 2030, 1},
		{"model tier", `[]`, "tier", 3150, 2},
		{"no match", `[{"field":"repository_name","op":"=","value":"other/repo"}]`, "", 0, 0},
		{"wrong provider", `[{"field":"provider","op":"=","value":"claude"}]`, "", 0, 0},
	} {
		t.Run(sample.name, func(t *testing.T) {
			for _, hourly := range []bool{false, true} {
				body := `{"where":` + sample.where + `,"split":"` + sample.split + `","hourly":` + strconv.FormatBool(hourly) + `}`
				response := httptest.NewRecorder()
				server.postQueryTable(response, httptest.NewRequest(http.MethodPost, "/api/query/usage/carbon", strings.NewReader(body)), "usage", "carbon")
				if response.Code != http.StatusOK {
					t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
				}
				var result struct {
					Factors            carbonDocument      `json:"factors"`
					Windows            []carbonWindow      `json:"windows"`
					Series             []carbonPoint       `json:"series"`
					Choices            map[string][]string `json:"choices"`
					UnknownModelTokens int64               `json:"default_tier_unknown_tokens"`
					UnclassifiedTokens int64               `json:"unclassified_tokens"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				var total, chart int64
				for _, counts := range result.Windows[0].Tokens {
					for _, count := range counts {
						total += count
					}
				}
				for _, point := range result.Series {
					for _, counts := range point.Tokens {
						for _, count := range counts {
							chart += count
						}
					}
					if hourly != strings.Contains(point.Day, "T") {
						t.Fatalf("wrong time key: %s", point.Day)
					}
					if sample.split == "tier" && point.Value != result.Factors.DefaultTier {
						t.Fatalf("placeholder model should take default tier: %#v", point)
					}
				}
				if total != sample.want || chart != sample.want {
					t.Fatalf("hourly=%v total=%d chart=%d want=%d", hourly, total, chart, sample.want)
				}
				if result.UnknownModelTokens != sample.want || result.UnclassifiedTokens != 0 {
					t.Fatalf("incomplete calculation provenance: unknown=%d unclassified=%d", result.UnknownModelTokens, result.UnclassifiedTokens)
				}
				if !hourly && int64(len(result.Series)) != sample.points {
					t.Fatalf("daily points: %#v", result.Series)
				}
				if strings.Join(result.Choices["repository_name"], ",") != "example/repo" || strings.Join(result.Choices["provider"], ",") != "codex" {
					t.Fatalf("filters hid choices: %#v", result.Choices)
				}
			}
		})
	}
	for _, body := range []string{`{"where":[{"field":"not-a-field","op":"=","value":"x"}]}`, `{"split":"model"}`, `{"where":"invalid"}`} {
		response := httptest.NewRecorder()
		server.postQueryTable(response, httptest.NewRequest(http.MethodPost, "/api/query/usage/carbon", strings.NewReader(body)), "usage", "carbon")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected rejected request %s: HTTP %d", body, response.Code)
		}
	}
}

func TestCarbonSeriesNoRepository(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestUsageFixture(t, catalog)
	var request carbonSeriesRequest
	if err := json.Unmarshal([]byte(`{"where":[{"field":"repository_name","op":"is_null","value":""}],"split":"repository_name"}`), &request); err != nil {
		t.Fatal(err)
	}
	result, err := catalog.carbonSeries(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if series := result["series"].([]*carbonPoint); len(series) == 0 || series[0].Value != "" {
		t.Fatalf("no-repository usage omitted: %#v", series)
	}
}

func TestCarbonSeriesNamesFallbacksAndUnclassifiedTokens(t *testing.T) {
	catalog, _ := testCatalog(t)
	ingestUsageFixture(t, catalog)
	if _, err := catalog.DB.Exec(`UPDATE agent_sessions SET model='mystery-model'`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`UPDATE agent_session_usage SET unclassified_tokens=10,total_tokens=total_tokens+10 WHERE output_tokens=30`); err != nil {
		t.Fatal(err)
	}
	result, err := catalog.carbonSeries(t.Context(), carbonSeriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	models := result["default_tier_models"].([]map[string]any)
	if len(models) != 1 || models[0]["model"] != "mystery-model" || integer(models[0]["tokens"]) != 3060 || integer(result["default_tier_unknown_tokens"]) != 0 {
		t.Fatalf("named fallback provenance: %#v", result)
	}
	if integer(result["unclassified_tokens"]) != 10 {
		t.Fatalf("unclassified provenance: %#v", result)
	}
	tokens := result["windows"].([]*carbonWindow)[0].Tokens["medium"]
	if tokens["uncached_input"] != 710 {
		t.Fatalf("unclassified tokens not counted as uncached input: %#v", tokens)
	}
}

func testCarbon() carbonDocument {
	cite := []string{"s"}
	ratio := func(low, central, high float64) carbonRatio {
		return carbonRatio{carbonRange: carbonRange{low, central, high}, Basis: "test", Sources: cite}
	}
	return carbonDocument{
		Version: 1, RetrievedAt: "2026-09-25", DefaultTier: "medium",
		Tiers: []carbonTier{
			{Key: "small", Label: "Small", Match: []string{"haiku"}, OutputWhPerMTok: carbonRange{50, 100, 200}, Basis: "test", Sources: cite},
			{Key: "medium", Label: "Medium", Match: []string{"sonnet"}, OutputWhPerMTok: carbonRange{200, 400, 800}, Basis: "test", Sources: cite},
			{Key: "large", Label: "Large", Match: []string{"opus"}, OutputWhPerMTok: carbonRange{500, 1000, 2000}, Basis: "test", Sources: cite},
		},
		CategoryRatios: map[string]carbonRatio{
			"uncached_input": ratio(0.05, 0.1, 0.2), "cache_write": ratio(0.1, 0.2, 0.3), "cache_read": ratio(0.005, 0.01, 0.02),
		},
		PUE:         ratio(1.1, 1.2, 1.4),
		Grids:       []carbonGrid{{Key: "test", Label: "Test grid", GramsPerKWh: 400, Basis: "test", Sources: cite}},
		DefaultGrid: "test",
		Sources:     []carbonSource{{ID: "s", Title: "Source", URL: "https://example.com", Finding: "test"}},
	}
}
