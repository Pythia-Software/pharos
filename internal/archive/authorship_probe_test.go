//go:build probe

package archive

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs classification and its write plan without writing a catalog. Only an
// explicitly named backup is accepted, opened immutable and read-only; no
// Initialize, migration, checkpoint, or source-file backfill runs.
//
//	PHAROS_AUTHORSHIP_BACKUP='/path/to/backup/catalog.sqlite3' \
//	  go test -tags probe -run '^TestAuthorshipBackupProbe$' -v -timeout 30m ./internal/archive
func TestAuthorshipBackupProbe(t *testing.T) {
	path := os.Getenv("PHAROS_AUTHORSHIP_BACKUP")
	if path == "" {
		t.Skip("set PHAROS_AUTHORSHIP_BACKUP to an immutable backup")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(path), "backup") || strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe requires a backup, never the live catalog")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1&mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &Catalog{DB: db}
	started := time.Now()
	inputs, launches, err := c.authorshipInputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("inputs=%d launches=%d inputs_seconds=%.3f", len(inputs), len(launches), time.Since(started).Seconds())
	started = time.Now()
	results, err := c.classifyInputs(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("classify_seconds=%.3f", time.Since(started).Seconds())
	started = time.Now()
	plan, err := c.planAuthorship(t.Context(), results, launches)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, change := range plan {
		counts[change.table]++
	}
	t.Logf("plan_seconds=%.3f would_write=%v", time.Since(started).Seconds(), counts)
}
