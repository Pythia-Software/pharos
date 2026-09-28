//go:build prototype

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

// TestDumpAuthorship classifies a real catalog with the current rules and
// writes one JSON line per user message, for offline tuning.
// PHAROS_CATALOG=… PHAROS_DUMP=… go test -tags prototype -run TestDumpAuthorship ./internal/archive
func TestDumpAuthorship(t *testing.T) {
	path, out := os.Getenv("PHAROS_CATALOG"), os.Getenv("PHAROS_DUMP")
	if path == "" || out == "" {
		t.Skip("PHAROS_CATALOG and PHAROS_DUMP not set")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?immutable=1&mode=ro")
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
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, result := range results {
		encoder.Encode(map[string]any{"id": result.Input.ID, "conversation": result.Input.ConversationID, "workspace": result.Input.WorkspaceID,
			"sent_at": result.Input.SentAt, "text": result.Input.Text, "spans": result.Spans})
	}
	t.Logf("dumped %d messages", len(results))
}
