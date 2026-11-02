//go:build probe

// Times compacting a scratch copy of a real catalog, step by step. It
// replaces the catalog it is given, so like the other scratch probes it
// refuses one outside PHAROS_PROBE_SCRATCH (see probeScratchPath):
//
//	PHAROS_PROBE_SCRATCH=/scratch PHAROS_COMPACT_CATALOG=/scratch/catalog.sqlite3 \
//	  go test -tags probe -run TestCompactLibraryProbe -v -timeout 600m ./internal/archive

package archive

import (
	"os"
	"testing"
	"time"
)

func TestCompactLibraryProbe(t *testing.T) {
	path := os.Getenv("PHAROS_COMPACT_CATALOG")
	if path == "" {
		t.Skip("set PHAROS_COMPACT_CATALOG to a scratch copy of a catalog")
	}
	if err := probeScratchPath(os.Getenv("PHAROS_PROBE_SCRATCH"), path); err != nil {
		t.Fatal(err)
	}
	started, step, at := time.Now(), "", time.Now()
	receipt, err := CompactLibrary(t.Context(), Config{CatalogPath: path}, func(phase string, done, total int64) {
		if phase != step {
			if step != "" {
				t.Logf("%s: %s", step, time.Since(at).Round(time.Second))
			}
			step, at = phase, time.Now()
		}
	})
	if step != "" {
		t.Logf("%s: %s", step, time.Since(at).Round(time.Second))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compacted %s to %s (%s free) in %s", humanBytes(receipt.BeforeBytes), humanBytes(receipt.AfterBytes), humanBytes(receipt.FreeBytes), time.Since(started).Round(time.Second))
}
