package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

// TestAuthorshipEvalLiveCatalog classifies a real catalog read-only and
// writes per-category totals and each message's spans to a JSON file, so rule
// changes can be compared on real data. It runs only when
// PHAROS_AUTHORSHIP_EVAL_DB names a catalog and PHAROS_AUTHORSHIP_EVAL_OUT a
// destination.
func TestAuthorshipEvalLiveCatalog(t *testing.T) {
	path, out := os.Getenv("PHAROS_AUTHORSHIP_EVAL_DB"), os.Getenv("PHAROS_AUTHORSHIP_EVAL_OUT")
	if path == "" || out == "" {
		t.Skip("set PHAROS_AUTHORSHIP_EVAL_DB and PHAROS_AUTHORSHIP_EVAL_OUT")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &Catalog{DB: db}
	ctx := context.Background()
	workspaces, err := queryMapsContext(ctx, db, `SELECT w.id,w.source_kind,w.activity_at FROM workspaces w`)
	if err != nil {
		t.Fatal(err)
	}
	suppressed := map[string]bool{}
	for _, row := range c.suppressMirrors(workspaces) {
		if mirrors, ok := row["mirrored_workspace_ids"].([]string); ok {
			for _, id := range mirrors {
				suppressed[id] = true
			}
		}
	}
	inputs, err := c.authorInputs(ctx, suppressed)
	if err != nil {
		t.Fatal(err)
	}
	results, err := c.classifyInputs(ctx, inputs)
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]int{}
	type row struct {
		ID    string         `json:"id"`
		Words map[string]int `json:"words"`
		Spans []authorSpan   `json:"spans"`
	}
	rows := make([]row, 0, len(results))
	for _, result := range results {
		counts := countSpans(result.Input.Text, result.Spans)
		for category, words := range counts.words {
			totals[category] += words
		}
		rows = append(rows, row{result.Input.ID, counts.words, result.Spans})
	}
	t.Logf("%s messages=%d words=%v", authorshipVersion, len(results), totals)
	data, _ := json.Marshal(map[string]any{"version": authorshipVersion, "totals": totals, "messages": rows})
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
