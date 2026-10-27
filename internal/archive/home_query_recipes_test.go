package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Execute the documented production recipes through the authenticated host,
// rather than validating only generic sample fields or the browser interpreter.
func TestHomeQueryRecipesV2(t *testing.T) {
	slowTest(t)
	data, err := os.ReadFile("../../docs/homepage-query-cards.json")
	if err != nil {
		t.Fatal(err)
	}
	var tracker struct {
		Recipes []struct {
			ID       string         `json:"id"`
			Dataset  string         `json:"dataset"`
			Where    []qt.WhereTerm `json:"where"`
			Measures []qt.AggSpec   `json:"measures"`
		} `json:"implemented_recipes"`
	}
	if err = json.Unmarshal(data, &tracker); err != nil {
		t.Fatal(err)
	}
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 300)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	for _, recipe := range tracker.Recipes {
		t.Run(recipe.ID, func(t *testing.T) {
			path := "/api/query/" + recipe.Dataset + "/metrics"
			var request any = qt.MetricQuery{Version: 2, Profile: qt.SQLiteExpressionProfile, Where: recipe.Where, Limit: 50, Metrics: recipe.Measures}
			if slices.ContainsFunc(recipe.Measures, func(metric qt.AggSpec) bool { return metric.DateGrouping != nil }) {
				path = "/api/query/" + recipe.Dataset + "/query-v3"
				request = qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: false,
					Result: qt.WireQuery{Where: recipe.Where, Limit: 50}, Metrics: recipe.Measures}
			} else if len(recipe.Measures) == 0 {
				path = "/api/query/" + recipe.Dataset + "/rows-v2"
				request = qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Where: recipe.Where, Limit: 3}}
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer "+config.APIToken)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			if len(recipe.Measures) > 0 {
				var result struct {
					Metrics []struct {
						ID      string `json:"id"`
						Buckets []struct {
							Error  string `json:"error"`
							YError string `json:"yError"`
						} `json:"buckets"`
					} `json:"metrics"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Metrics) != len(recipe.Measures) {
					t.Fatalf("got %d metrics, want %d", len(result.Metrics), len(recipe.Measures))
				}
				for i, metric := range result.Metrics {
					if metric.ID != recipe.Measures[i].ID {
						t.Fatalf("got metric %q, want %q", metric.ID, recipe.Measures[i].ID)
					}
					for _, bucket := range metric.Buckets {
						if bucket.Error != "" || bucket.YError != "" {
							t.Fatalf("metric %s: %s %s", metric.ID, bucket.Error, bucket.YError)
						}
					}
				}
			}
		})
	}
}

func TestHomeQueryCardGroupedDestinations(t *testing.T) {
	slowTest(t)
	data, err := os.ReadFile("../../docs/homepage-query-cards.json")
	if err != nil {
		t.Fatal(err)
	}
	var tracker struct {
		Recipes []struct {
			ID          string         `json:"id"`
			Dataset     string         `json:"dataset"`
			Where       []qt.WhereTerm `json:"where"`
			Exploration *struct {
				qt.GroupingDefinition
				ValueField  string         `json:"valueField"`
				Having      []qt.WhereTerm `json:"having"`
				CoverMetric *qt.AggSpec    `json:"coverMetric"`
			} `json:"exploration"`
		} `json:"implemented_recipes"`
	}
	if err := json.Unmarshal(data, &tracker); err != nil {
		t.Fatal(err)
	}
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 300)
	ingestUsageFixture(t, catalog)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	for _, recipe := range tracker.Recipes {
		if recipe.Exploration == nil {
			continue
		}
		t.Run(recipe.ID, func(t *testing.T) {
			view := recipe.Exploration
			fields := append([]string{}, view.Keys...)
			for _, summary := range view.Summaries {
				fields = append(fields, summary.ID)
			}
			request := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
				Source: qt.WireQuery{Where: recipe.Where, Limit: 50}, Grouping: &view.GroupingDefinition,
				Result: qt.WireQuery{Select: fields, Where: view.Having, OrderBy: qt.OrderBys{{Field: view.ValueField, Dir: "desc", Nulls: "last"}}, Limit: 3},
			}
			if view.CoverMetric != nil {
				request.Metrics = []qt.AggSpec{*view.CoverMetric}
			}
			w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/"+recipe.Dataset+"/query-v3", request)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			// Distribution payloads are a tagged JSON union on the wire;
			// decoding them into the Go execution interface is not supported.
			var result struct {
				Schema qt.ProjectedSchema `json:"schema"`
				Rows   []map[string]any   `json:"rows"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Schema.IDField != qt.GroupIdentityField {
				t.Fatal(result.Schema)
			}
			if len(result.Rows) > 3 {
				t.Fatal("preview exceeded row budget")
			}
		})
	}
}
