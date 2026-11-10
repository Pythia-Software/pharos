package archive

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
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
	request := queryFixtureRows{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"co2e_g"}, Limit: 100}}
	read := func(settings string) float64 {
		t.Helper()
		result, err := s.executeQueryFixture(t.Context(), "usage", url.Values{"carbon_settings": {settings}}, PharosUsageSchema(), &request, nil)
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
	itemRequest := queryFixtureRows{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "co2e_g"}, Limit: 1}}
	assumptions := `{"scenario":"high","grid":"custom","customGrid":100}`
	itemRows, itemErr := s.executeQueryFixture(t.Context(), "usage", url.Values{"carbon_settings": {assumptions}}, PharosUsageSchema(), &itemRequest, nil)
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

// TestCarbonSharedEstimates holds the service to the estimates in
// testdata/carbon-estimates.json, which the page's calculator (carbon.js,
// web/tests/unit/page/carbon.test.mjs) must also reproduce, so usage rows
// and the Carbon dashboard's token-type split agree under any settings.
func TestCarbonSharedEstimates(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/carbon-estimates.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Tokens map[string]map[string]float64 `json:"tokens"`
		Cases  []struct {
			Name       string             `json:"name"`
			Settings   map[string]any     `json:"settings"`
			FacilityWh float64            `json:"facility_wh"`
			CO2eG      float64            `json:"co2e_g"`
			ByCategory map[string]float64 `json:"co2e_g_by_category"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	doc, err := loadCarbonDocument()
	if err != nil {
		t.Fatal(err)
	}
	close := func(got, want float64) bool { return math.Abs(got-want) <= 1e-9*math.Max(1, math.Abs(want)) }
	for _, c := range fixture.Cases {
		a := doc.assumptions(c.Settings)
		total, byCategory := 0.0, map[string]float64{}
		for tier, counts := range fixture.Tokens {
			for category, tokens := range counts {
				wh := a.wh(tier, category, tokens)
				total += wh
				byCategory[category] += wh
			}
		}
		if !close(total*a.pue, c.FacilityWh) || !close(a.grams(total), c.CO2eG) {
			t.Errorf("%s: facility %v Wh, %v g; want %v Wh, %v g", c.Name, total*a.pue, a.grams(total), c.FacilityWh, c.CO2eG)
		}
		for category, want := range c.ByCategory {
			if got := a.grams(byCategory[category]); !close(got, want) {
				t.Errorf("%s %s: %v g, want %v g", c.Name, category, got, want)
			}
		}
	}
}

// carbonUsageRequests are Carbon Impact's query-v3 requests on Usage
// (web/src/carbon-usage.ts keeps to this file).
type carbonUsageRequests struct {
	Usage   []qt.AggSpec `json:"usage"`
	Choices []qt.AggSpec `json:"choices"`
}

// carbonUsageRead is what the page reads from them: the estimate's token
// counts by tier and category, and what its notes name.
type carbonUsageRead struct {
	Tokens                         map[string]map[string]int64
	Undated, Unclassified, Unknown int64
	Models                         map[string]int64
	Choices                        map[string][]string
}

func carbonUsageV3(t *testing.T, s *Server, where []qt.WhereTerm) carbonUsageRead {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/carbon-usage-metrics.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests carbonUsageRequests
	if err := json.Unmarshal(raw, &requests); err != nil {
		t.Fatal(err)
	}
	run := func(metrics []qt.AggSpec, where []qt.WhereTerm) map[string]qt.MetricResult {
		q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Result: qt.WireQuery{Where: where, Limit: 1}, Metrics: metrics}
		result, _, err := s.executeQueryV3(t.Context(), "usage", nil, PharosUsageSchema(), q)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]qt.MetricResult{}
		for _, metric := range result.Metrics {
			if metric.Error != "" || metric.GroupCount > int64(len(metric.Buckets)) {
				t.Fatalf("%s: %+v", metric.ID, metric)
			}
			byID[metric.ID] = metric
		}
		return byID
	}
	metrics := run(requests.Usage, where)
	read := carbonUsageRead{Tokens: map[string]map[string]int64{}, Models: map[string]int64{}, Choices: map[string][]string{}}
	category := map[string]string{"uncached_input_tokens": "uncached_input", "unclassified_tokens": "uncached_input",
		"cache_creation_input_tokens": "cache_write", "cache_read_input_tokens": "cache_read", "output_tokens": "output"}
	for _, side := range []string{"dated", "undated"} {
		for field, key := range category {
			for _, bucket := range metrics[side+":"+field].Buckets {
				tier := firstString(bucket.Keys[0])
				if read.Tokens[tier] == nil {
					read.Tokens[tier] = map[string]int64{"uncached_input": 0, "cache_write": 0, "cache_read": 0, "output": 0}
				}
				read.Tokens[tier][key] += integer(bucket.Value)
				if field == "unclassified_tokens" {
					read.Unclassified += integer(bucket.Value)
				}
			}
		}
		for _, bucket := range metrics[side+":default_models"].Buckets {
			read.Models[firstString(bucket.Keys[0])] += integer(bucket.Value)
		}
		for _, bucket := range metrics[side+":unreported"].Buckets {
			read.Unknown += integer(bucket.Value)
		}
	}
	for _, bucket := range metrics["undated:total_tokens"].Buckets {
		read.Undated += integer(bucket.Value)
	}
	for id, metric := range run(requests.Choices, nil) {
		field := strings.TrimPrefix(id, "choices:")
		read.Choices[field] = []string{}
		for _, bucket := range metric.Buckets {
			read.Choices[field] = append(read.Choices[field], firstString(bucket.Keys[0]))
		}
		sort.Strings(read.Choices[field])
	}
	return read
}

// Parity with the endpoint the page read before (POST /api/query/usage/carbon).
func TestCarbonUsageV3MatchesCarbonEndpoint(t *testing.T) {
	previousLocation := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previousLocation })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	execTx(t, catalog.DB, `INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('carbon-repo','example/repo','','')`, `UPDATE workspaces SET repository_id='carbon-repo'`)
	server := testServer(t, config, catalog)
	compare := func(name, where string) {
		t.Helper()
		compareCarbonUsage(t, server, name, where)
	}
	for _, sample := range []struct{ name, where string }{
		{"all", `[]`},
		{"repository and provider", `[{"field":"repository_name","op":"=","value":"example/repo"},{"field":"provider","op":"=","value":"codex"}]`},
		{"partial day with offset", `[{"field":"last_usage_at","op":">=","value":"2026-09-20T19:00:00-06:00"},{"field":"first_usage_at","op":"<","value":"2026-09-20T21:00:00-06:00"}]`},
		{"no match", `[{"field":"repository_name","op":"=","value":"other/repo"}]`},
		{"no repository", `[{"field":"repository_name","op":"is_null","value":""}]`},
	} {
		compare(sample.name, sample.where)
	}
	execTx(t, catalog.DB, `UPDATE agent_sessions SET model='mystery-model'`, `UPDATE agent_session_usage SET unclassified_tokens=10,total_tokens=total_tokens+10 WHERE output_tokens=30`)
	compare("named fallback and unclassified", `[]`)
	compare("named fallback, partial day", `[{"field":"first_usage_at","op":">=","value":"2026-09-21T02:00:00Z"}]`)
	// Every tier, and usage with no date.
	_, tiers, _ := carbonUsageFixture(t)
	compareCarbonUsage(t, tiers, "tiers", `[]`)
	compareCarbonUsage(t, tiers, "tiers, one hour", `[{"field":"hour","op":"=","value":"2026-09-21T01:00:00Z"}]`)
	execTx(t, tiers.Catalog.DB, `UPDATE agent_session_usage SET usage_hour='' WHERE output_tokens=30`)
	compareCarbonUsage(t, tiers, "undated", `[]`)
	compareCarbonUsage(t, tiers, "undated, one day", `[{"field":"day","op":"=","value":"2026-09-21"}]`)
}

func compareCarbonUsage(t *testing.T, server *Server, name, where string) {
	t.Helper()
	{
		response := httptest.NewRecorder()
		server.postQueryTable(response, httptest.NewRequest(http.MethodPost, "/api/query/usage/carbon", strings.NewReader(`{"where":`+where+`}`)), "usage", "carbon")
		var old struct {
			Windows      []carbonWindow      `json:"windows"`
			Choices      map[string][]string `json:"choices"`
			Undated      int64               `json:"undated_tokens"`
			Unclassified int64               `json:"unclassified_tokens"`
			Unknown      int64               `json:"default_tier_unknown_tokens"`
			Models       []struct {
				Model  string `json:"model"`
				Tokens int64  `json:"tokens"`
			} `json:"default_tier_models"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &old) != nil {
			t.Fatalf("%s: HTTP %d %s", name, response.Code, response.Body.String())
		}
		var terms []qt.WhereTerm
		if err := json.Unmarshal([]byte(where), &terms); err != nil {
			t.Fatal(err)
		}
		got := carbonUsageV3(t, server, terms)
		models := map[string]int64{}
		for _, model := range old.Models {
			models[model.Model] = model.Tokens
		}
		want := carbonUsageRead{Tokens: old.Windows[0].Tokens, Undated: old.Undated, Unclassified: old.Unclassified, Unknown: old.Unknown, Models: models, Choices: old.Choices}
		if a, b := must(json.Marshal(got)), must(json.Marshal(want)); string(a) != string(b) {
			t.Fatalf("%s:\n query-v3 %s\n endpoint %s", name, a, b)
		}
		if name != "no match" && name != "no repository" && len(got.Tokens) == 0 {
			t.Fatalf("%s: no usage compared", name)
		}
	}
}
