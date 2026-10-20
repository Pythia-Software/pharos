package archive

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

// carbonVariant mirrors carbon.js settings() for one saved preference value.
type carbonVariant struct {
	name                  string
	prefs                 map[string]any
	scenario, pueScenario string
	scale, grid           float64
}

var carbonVariants = []carbonVariant{
	{"central default", nil, "central", "central", 1, 350},
	{"high, low PUE, third", map[string]any{"scenario": "high", "pueScenario": "low", "inputSensitivity": "third"}, "high", "low", 5.0 / 3, 350},
	{"custom grid", map[string]any{"grid": "custom", "customGrid": "123.5", "scenario": "low"}, "low", "central", 1, 123.5},
	{"custom grid zero", map[string]any{"grid": "custom", "customGrid": 0}, "central", "central", 1, 0},
	{"named grid", map[string]any{"grid": "world", "pueScenario": "high"}, "central", "high", 1, 458},
}

// calculate is carbon.js calculate(): IT Wh, then facility Wh and grams.
func (v carbonVariant) calculate(doc carbonDocument, tokens map[string]map[string]int64) (float64, float64) {
	wh := 0.0
	for _, tier := range doc.Tiers {
		for _, category := range carbonCategories {
			ratio := 1.0
			if category != "output" {
				ratio = doc.CategoryRatios[category].get(v.scenario) * v.scale
			}
			wh += float64(tokens[tier.Key][category]) / 1e6 * tier.OutputWhPerMTok.get(v.scenario) * ratio
		}
	}
	pue := doc.PUE.get(v.pueScenario)
	return wh * pue, wh * pue * v.grid / 1000
}

func nearlyEqual(got, want float64) bool {
	return math.Abs(got-want) <= 1e-12*math.Max(1, math.Abs(want))
}

func setCarbonPreferences(t *testing.T, catalog *Catalog, prefs map[string]any) {
	t.Helper()
	body := map[string]any{"delete": []any{"pharos-carbon-v1"}}
	if prefs != nil {
		body = map[string]any{"set": map[string]any{"pharos-carbon-v1": prefs}}
	}
	if err := catalog.SetUIPreferences(t.Context(), body); err != nil {
		t.Fatal(err)
	}
}

// carbonUsageFixture has one model per tier: large Sep 20, small and medium Sep 21.
func carbonUsageFixture(t *testing.T) (*Catalog, *Server, carbonDocument) {
	t.Helper()
	previousLocation := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previousLocation })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog, `{"type":"token_usage_record","timestamp":"2026-09-21T03:05:00Z","payload":{"response_id":"r3","usage":{"input_tokens":90,"output_tokens":10,"total_tokens":100},"thread_token_usage":{"total_tokens":3150}}}`)
	if _, err := catalog.DB.Exec(`UPDATE agent_session_usage SET model=CASE output_tokens WHEN 20 THEN 'claude-opus-5-5' WHEN 30 THEN 'gpt-5.1-codex-mini' ELSE 'claude-sonnet-4-5-20250929' END`); err != nil {
		t.Fatal(err)
	}
	doc, err := loadCarbonDocument()
	if err != nil {
		t.Fatal(err)
	}
	return catalog, testServer(t, config, catalog), doc
}

func carbonRowTokens(row map[string]any) map[string]map[string]int64 {
	return map[string]map[string]int64{firstString(row["carbon_tier"]): carbonTokens(row)}
}

func TestUsageRowsCarryCarbonEstimates(t *testing.T) {
	catalog, _, doc := carbonUsageFixture(t)
	for _, variant := range carbonVariants {
		t.Run(variant.name, func(t *testing.T) {
			setCarbonPreferences(t, catalog, variant.prefs)
			for _, hourly := range []bool{false, true} {
				rows, err := catalog.localUsageBaseRows(t.Context(), hourly)
				if err != nil {
					t.Fatal(err)
				}
				lineage, err := catalog.localUsageRows(t.Context())
				if hourly {
					lineage, err = catalog.localUsageHourlyRows(t.Context())
				}
				if err != nil {
					t.Fatal(err)
				}
				tiers := map[string]bool{}
				for _, row := range append(rows, lineage...) {
					tier, _ := doc.tier(firstString(row["model"]))
					energy, grams := variant.calculate(doc, carbonRowTokens(row))
					if row["carbon_tier"] != tier || !nearlyEqual(row["energy_wh"].(float64), energy) || !nearlyEqual(row["co2e_g"].(float64), grams) ||
						!nearlyEqual(row["energy_kwh"].(float64), energy/1000) || !nearlyEqual(row["co2e_kg"].(float64), grams/1000) {
						t.Fatalf("hourly=%v row %v: got %v %v %v, want %s %v %v", hourly, row["model"], row["carbon_tier"], row["energy_wh"], row["co2e_g"], tier, energy, grams)
					}
					if energy <= 0 || (variant.grid > 0) != (grams > 0) {
						t.Fatalf("degenerate estimate: %v %v", energy, grams)
					}
					tiers[tier] = true
				}
				if len(tiers) != 3 {
					t.Fatalf("fixture should cover every tier: %v", tiers)
				}
			}
			// The menu bar shares the assumptions: its 24h tokens give the same total.
			tokens := map[string]map[string]float64{}
			want := map[string]map[string]int64{}
			for _, row := range must(catalog.localUsageBaseRows(t.Context(), false)) {
				tier := firstString(row["carbon_tier"])
				if tokens[tier] == nil {
					tokens[tier], want[tier] = map[string]float64{}, map[string]int64{}
				}
				for category, count := range carbonTokens(row) {
					tokens[tier][category] += float64(count)
					want[tier][category] += count
				}
			}
			if _, grams := variant.calculate(doc, want); !nearlyEqual(menuBarCarbon(doc, tokens, variant.prefs), grams) {
				t.Fatalf("menu bar %v, want %v", menuBarCarbon(doc, tokens, variant.prefs), grams)
			}
		})
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// Sums over a period match the Carbon page: the carbon endpoint's token counts
// through calculate(), with the same saved settings, on every query protocol.
// Changing the settings changes the next answer; nothing stale is served.
func TestUsageCarbonSumsMatchCarbonPageAndFollowSettings(t *testing.T) {
	catalog, server, doc := carbonUsageFixture(t)
	for _, period := range []struct{ name, where string }{
		{"all", `[]`},
		{"one day", `[{"field":"day","op":"=","value":"2026-09-21"}]`},
		{"one hour", `[{"field":"hour","op":"=","value":"2026-09-21T01:00:00Z"}]`},
	} {
		for _, variant := range carbonVariants {
			t.Run(period.name+"/"+variant.name, func(t *testing.T) {
				setCarbonPreferences(t, catalog, variant.prefs)
				response := httptest.NewRecorder()
				server.postQueryTable(response, httptest.NewRequest(http.MethodPost, "/api/query/usage/carbon", strings.NewReader(`{"where":`+period.where+`}`)), "usage", "carbon")
				var carbon struct {
					Windows []carbonWindow `json:"windows"`
				}
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &carbon) != nil {
					t.Fatalf("carbon HTTP %d: %s", response.Code, response.Body.String())
				}
				energy, grams := variant.calculate(doc, carbon.Windows[0].Tokens)
				if energy <= 0 {
					t.Fatalf("empty period: %#v", carbon)
				}
				check := func(protocol string, got any, want float64) {
					t.Helper()
					if value, ok := number(got); !ok || !nearlyEqual(value, want) {
						t.Fatalf("%s = %v, want %v", protocol, got, want)
					}
				}
				sum := func(values ...any) float64 {
					total := 0.0
					for _, value := range values {
						n, _ := number(value)
						total += n
					}
					return total
				}

				// v1 aggregations: daily rows, and the hourly ledger when grouped by hour.
				response = httptest.NewRecorder()
				body := `{"where":` + period.where + `,"aggregations":[{"id":"wh","op":"sum","field":"energy_wh","groupBy":[]},{"id":"g","op":"sum","field":"co2e_g","groupBy":[]},{"id":"hourly","op":"sum","field":"co2e_g","groupBy":["hour"]}]}`
				server.postQueryTable(response, httptest.NewRequest(http.MethodPost, "/api/query/usage/aggregations", strings.NewReader(body)), "usage", "aggregations")
				var v1 querytable.AggregationResult
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &v1) != nil {
					t.Fatalf("aggregations HTTP %d: %s", response.Code, response.Body.String())
				}
				// The hour's daily row holds only that hour, so daily sums match too.
				check("v1 energy", v1.Metrics[0].Buckets[0].Value, energy)
				check("v1 co2e", v1.Metrics[1].Buckets[0].Value, grams)
				hourly := []any{}
				for _, bucket := range v1.Metrics[2].Buckets {
					hourly = append(hourly, bucket.Value)
				}
				check("v1 hourly co2e", sum(hourly...), grams)

				// v2 metrics, including the hourly population.
				var where []qt.WhereTerm
				if err := json.Unmarshal([]byte(period.where), &where); err != nil {
					t.Fatal(err)
				}
				metrics := metricRequest(qt.AggSpec{ID: "wh", Op: "sum", Field: "energy_wh"}, qt.AggSpec{ID: "g", Op: "sum", Field: "co2e_g"},
					qt.AggSpec{ID: "hourly", Op: "sum", Field: "co2e_g", GroupBy: []string{"hour"}})
				metrics.Where = where
				w := queryV2ReviewCall(t, server, server.Config().APIToken, http.MethodPost, "/api/query/usage/metrics", metrics)
				var v2 struct {
					Metrics []struct {
						ID      string `json:"id"`
						Buckets []struct {
							Value any `json:"value"`
						} `json:"buckets"`
					} `json:"metrics"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v2) != nil || len(v2.Metrics) != 3 {
					t.Fatalf("v2 HTTP %d: %s", w.Code, w.Body.String())
				}
				check("v2 energy", v2.Metrics[0].Buckets[0].Value, energy)
				check("v2 co2e", v2.Metrics[1].Buckets[0].Value, grams)
				hourly = hourly[:0]
				for _, bucket := range v2.Metrics[2].Buckets {
					hourly = append(hourly, bucket.Value)
				}
				check("v2 hourly co2e", sum(hourly...), grams)

				// v3 grouped by tier: each tier matches the page's per-tier total.
				q := groupRequest("carbon_tier", qt.SummaryColumn{ID: "wh", Op: "sum", Field: "energy_wh"}, qt.SummaryColumn{ID: "g", Op: "sum", Field: "co2e_g"})
				q.Source.Where = where
				w = queryV2ReviewCall(t, server, server.Config().APIToken, http.MethodPost, "/api/query/usage/query-v3", q)
				var v3 qt.QueryResultV3
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v3) != nil {
					t.Fatalf("v3 HTTP %d: %s", w.Code, w.Body.String())
				}
				if len(v3.Rows) != len(carbon.Windows[0].Tokens) {
					t.Fatalf("v3 tiers %v, page tiers %v", v3.Rows, carbon.Windows[0].Tokens)
				}
				for _, row := range v3.Rows {
					tier := firstString(row["carbon_tier"])
					e, g := variant.calculate(doc, map[string]map[string]int64{tier: carbon.Windows[0].Tokens[tier]})
					check("v3 energy "+tier, row["wh"], e)
					check("v3 co2e "+tier, row["g"], g)
				}
			})
		}
	}
}

// Saving preferences does not advance the cache generation; the next query
// must still use them, without keeping a copy of the rows per setting.
func TestUsageCarbonSettingsInvalidateCachedRows(t *testing.T) {
	catalog, server, _ := carbonUsageFixture(t)
	total := func() float64 {
		t.Helper()
		q := groupRequest("provider", qt.SummaryColumn{ID: "g", Op: "sum", Field: "co2e_g"})
		w := queryV2ReviewCall(t, server, server.Config().APIToken, http.MethodPost, "/api/query/usage/query-v3", q)
		var result qt.QueryResultV3
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Rows) != 1 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		value, _ := number(result.Rows[0]["g"])
		return value
	}
	before := total()
	setCarbonPreferences(t, catalog, map[string]any{"grid": "custom", "customGrid": 700})
	if after := total(); !nearlyEqual(after, before*2) {
		t.Fatalf("cached rows ignored new grid: %v then %v", before, after)
	}
	setCarbonPreferences(t, catalog, nil)
	if again := total(); !nearlyEqual(again, before) {
		t.Fatalf("restored settings: %v, want %v", again, before)
	}
	catalog.derived.mu.Lock()
	defer catalog.derived.mu.Unlock()
	families := map[string]int{}
	for key := range catalog.derived.entries {
		if family, _, ok := strings.Cut(key, "#carbon="); ok {
			families[family]++
		}
	}
	if len(families) == 0 {
		t.Fatal("usage rows were not cached")
	}
	for family, count := range families {
		if count != 1 {
			t.Fatalf("%s kept %d settings' rows", family, count)
		}
	}
}
