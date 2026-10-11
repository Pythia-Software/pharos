package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Execute the documented production recipes through the authenticated host,
// rather than validating only generic sample fields or the browser interpreter.
func TestHomeQueryRecipesV2(t *testing.T) {
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
	server := NewServer(config, catalog)
	for _, recipe := range tracker.Recipes {
		t.Run(recipe.ID, func(t *testing.T) {
			path := "/api/query/" + recipe.Dataset + "/metrics"
			var request any = qt.MetricQuery{Version: 2, Profile: qt.SQLiteExpressionProfile, Where: recipe.Where, Limit: 50, Metrics: recipe.Measures}
			if len(recipe.Measures) == 0 {
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
