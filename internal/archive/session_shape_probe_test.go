//go:build probe

package archive

// Read-only full-derivation timing of Session shape against Parallelism on a
// static copy of a large library (immutable, so it takes no locks; never the
// live catalog):
// PHAROS_SHAPE_PROBE=<dev library>/catalog/catalog.sqlite3 \
//   go test -tags probe -run '^TestSessionShapeRebuildProbe$' -v -timeout 2h ./internal/archive
import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionShapeRebuildProbe(t *testing.T) {
	path := os.Getenv("PHAROS_SHAPE_PROBE")
	if path == "" {
		t.Skip("set PHAROS_SHAPE_PROBE to an isolated static catalog copy")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), ".pharos-dev")); err != nil {
		t.Fatal("requires a marked isolated dev library")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&immutable=1&_pragma=cache_size(-262144)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := []string{}
	rows, err := queryMaps(db, "SELECT id FROM conversations ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		ids = append(ids, firstString(row["id"]))
	}
	ctx := t.Context()
	measure := func(name string, derive func(id string) error) {
		started, slowest, slowestID := time.Now(), time.Duration(0), ""
		for _, id := range ids {
			at := time.Now()
			if err := derive(id); err != nil {
				t.Fatal(name, id, err)
			}
			if took := time.Since(at); took > slowest {
				slowest, slowestID = took, id
			}
		}
		t.Logf("%s: %d conversations in %s (slowest %s: %s)", name, len(ids), time.Since(started).Round(time.Millisecond), slowestID, slowest.Round(time.Millisecond))
	}
	// Warm both alike: Parallelism first reads the same message index pages.
	measure("parallelism", func(id string) error { _, err := readParallelismActivity(ctx, db, id); return err })
	measure("session shape", func(id string) error { _, _, err := deriveSessionShapeOf(ctx, db, id); return err })
	measure("parallelism, again", func(id string) error { _, err := readParallelismActivity(ctx, db, id); return err })
}
