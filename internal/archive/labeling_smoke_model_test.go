package archive

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
)

// TestLabelingSmokeModelParity checks that Go featurizes and scores the
// shared fixture's tricky strings exactly as tools/labeling_smoke.py does.
func TestLabelingSmokeModelParity(t *testing.T) {
	data, err := os.ReadFile("../../testdata/labeling-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		FNV []struct {
			UTF8 string `json:"utf8"`
			Hash string `json:"hash"`
		} `json:"fnv1a64"`
		Model json.RawMessage `json:"model"`
		Cases []struct {
			Name     string             `json:"name"`
			Text     string             `json:"text"`
			Features [][2]float64       `json:"features"`
			Scores   map[string]float64 `json:"scores"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, check := range fixture.FNV {
		if got := fmt.Sprintf("%016x", fnv1a64([]byte(check.UTF8))); got != check.Hash {
			t.Errorf("fnv1a64(%q) = %s, want %s", check.UTF8, got, check.Hash)
		}
	}
	model, err := loadLabelingSmokeModel(fixture.Model)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 15 {
		t.Fatalf("only %d cases", len(fixture.Cases))
	}
	for _, c := range fixture.Cases {
		features, err := labelingSmokeFeatures(c.Text)
		if err != nil {
			t.Fatal(err)
		}
		if len(features) != len(c.Features) {
			t.Fatalf("%s: %d features, Python %d", c.Name, len(features), len(c.Features))
		}
		for index, feature := range features {
			want := c.Features[index]
			if float64(feature.Bucket) != want[0] || feature.Value != want[1] {
				t.Fatalf("%s: feature %d = %d:%v, Python %v:%v", c.Name, index, feature.Bucket, feature.Value, want[0], want[1])
			}
		}
		scores, err := model.score(c.Text)
		if err != nil {
			t.Fatal(err)
		}
		if len(scores) != len(c.Scores) {
			t.Fatalf("%s: scores %v, Python %v", c.Name, scores, c.Scores)
		}
		for label, want := range c.Scores {
			if got, ok := scores[label]; !ok || math.Abs(got-want) > 1e-9 {
				t.Errorf("%s: %s = %v, Python %v", c.Name, label, got, want)
			}
		}
	}
	// Another featurizer is refused rather than approximated.
	var raw map[string]any
	json.Unmarshal(fixture.Model, &raw)
	raw["featurizer"].(map[string]any)["lowercase"] = "unicode"
	changed, _ := json.Marshal(raw)
	if _, err := loadLabelingSmokeModel(changed); err == nil {
		t.Fatal("a model with another featurizer loaded")
	}
	if _, err := labelingSmokeFeatures("bad \xff"); err == nil {
		t.Fatal("invalid UTF-8 featurized")
	}
}
