//go:build probe

// Times Ask Pharos's suggestion signals on a copy of a real library, and
// prints the slate they produce. The copy is opened read-only; no
// initialization or migration runs, so create model_requests_compacted_idx
// on the copy first (catalog.go creates it on open):
//
//	PHAROS_ASK_SUGGESTIONS_PROBE=/path/to/copy/catalog.sqlite3 \
//	  go test -tags probe -run '^TestAskSuggestionsProbe$' -v ./internal/archive
//
// PHAROS_ASK_SUGGESTIONS_NOW (RFC 3339) moves "now" to a copy's last day.

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAskSuggestionsProbe(t *testing.T) {
	path := os.Getenv("PHAROS_ASK_SUGGESTIONS_PROBE")
	if path == "" {
		t.Skip("set PHAROS_ASK_SUGGESTIONS_PROBE to a copy of a library catalog")
	}
	if strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe a copy, never the live catalog")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if value := os.Getenv("PHAROS_ASK_SUGGESTIONS_NOW"); value != "" {
		if now, err = time.Parse(time.RFC3339, value); err != nil {
			t.Fatal(err)
		}
	}
	c := &Catalog{Path: path, DB: db, readOnly: true, queryOnly: true}
	started := time.Now()
	state := computeAskSignals(context.Background(), c, nil, now)
	t.Logf("signals took %s", time.Since(started))
	out, _ := json.MarshalIndent(state, "", "  ")
	t.Log(string(out))
	catalog, err := loadedAskSuggestions()
	if err != nil {
		t.Fatal(err)
	}
	for seed := 0; seed < 3; seed++ {
		for _, item := range rankAskSuggestions(catalog, state, askRankInput{Seed: seed, Now: now, Repositories: 5}) {
			t.Logf("seed %d  %-18s %-10s %s", seed, item.Area, item.Form, item.Text)
		}
	}
}
