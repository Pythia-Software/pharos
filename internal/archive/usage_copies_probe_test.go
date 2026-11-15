//go:build probe

// Measures what settling copied Claude replies changes on a scratch copy of a
// library catalog: the Claude totals before and after usage attribution
// repair, the copies it found and how long it took. Probes build only with the
// probe tag, so they stay out of go test ./...:
//
//	PHAROS_USAGE_REPLICA=/path/to/scratch-copy/catalog.sqlite3 \
//	  go test -tags probe -run TestUsageCopiesReplica -v -timeout 600m ./internal/archive

package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Never point this at an installed catalog: the repair rewrites usage.
func TestUsageCopiesReplica(t *testing.T) {
	path := os.Getenv("PHAROS_USAGE_REPLICA")
	if path == "" {
		t.Skip("set PHAROS_USAGE_REPLICA to a scratch copy of a catalog")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if absolute, err = filepath.EvalSymlinks(absolute); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(absolute, "/Volumes/") {
		t.Fatal("the usage probe only writes scratch copies on this Mac's disk")
	}
	db, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	schema := schemaSQL()
	start := strings.Index(schema, "CREATE TABLE IF NOT EXISTS claude_usage_copies")
	end := strings.Index(schema, "claude_usage_copies_conversation_idx")
	end += strings.Index(schema[end:], ";") + 1
	if _, err := db.Exec(schema[start:end]); err != nil {
		t.Fatal(err)
	}
	c := &Catalog{Path: path, DB: db, now: time.Now}
	totals := func(label string) map[string]int64 {
		values := map[string]int64{}
		for name, query := range map[string]string{
			"sessions": "SELECT COALESCE(SUM(s.total_tokens),0) FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id WHERE c.provider='claude'",
			"requests": "SELECT COALESCE(SUM(r.total_tokens),0) FROM model_requests r JOIN conversations c ON c.id=r.conversation_id WHERE c.provider='claude'",
			"metrics": `SELECT CAST(COALESCE(SUM(m.value),0) AS INTEGER) FROM metrics m WHERE m.name='total_tokens' AND m.conversation_id IS NULL
				AND m.workspace_id IN (SELECT workspace_id FROM conversations WHERE provider='claude')`,
			"copies":        `SELECT COUNT(*) FROM messages WHERE kind='metadata' AND raw_text LIKE '{"copied":%'`,
			"keys":          "SELECT COUNT(DISTINCT usage_key) FROM claude_usage_copies",
			"shared keys":   "SELECT COUNT(*) FROM (SELECT usage_key FROM claude_usage_copies GROUP BY usage_key HAVING COUNT(*)>1)",
			"workspaces v4": "SELECT COUNT(*) FROM usage_attribution_state WHERE version='" + usageAttributionVersion + "'",
		} {
			var value int64
			if err := db.QueryRow(query).Scan(&value); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			values[name] = value
		}
		t.Logf("%s: %v", label, values)
		return values
	}
	before := totals("before")
	started := time.Now()
	repaired, err := c.repairUsageAttribution(context.Background(), func(done, total int) {
		if done%500 == 0 || done == total {
			t.Logf("repaired %d/%d workspaces after %s", done, total, time.Since(started).Round(time.Second))
		}
	}, walSizeLimit)
	if err != nil {
		t.Fatalf("repair after %d workspaces: %v", repaired, err)
	}
	t.Logf("repaired %d workspaces in %s", repaired, time.Since(started).Round(time.Second))
	after := totals("after")
	for _, name := range []string{"sessions", "requests", "metrics"} {
		t.Logf("%s: %d -> %d (%+d)", name, before[name], after[name], after[name]-before[name])
	}
}
