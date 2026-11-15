package archive

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatTokensMatchesWebCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "token-count-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Cases [][2]any }
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, item := range fixture.Cases {
		value, want := item[0].(float64), item[1].(string)
		if got := formatTokens(value); got != want {
			t.Errorf("formatTokens(%v) = %q, want %q", value, got, want)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := formatTokens(value); got != "" {
			t.Errorf("formatTokens(%v) = %q, want blank", value, got)
		}
	}
}
