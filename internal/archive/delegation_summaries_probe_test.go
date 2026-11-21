//go:build probe

// Times the saved delegation summaries' sweep and backfill on a scratch
// clone of a library catalog:
//
//	PHAROS_DELEGATION_REPLICA=/path/to/scratch/catalog.sqlite3 \
//	  go test -tags probe -run TestDelegationSummariesReplica -v -timeout 120m ./internal/archive
//
// PHAROS_DELEGATION_WORKSPACE names a workspace whose page is timed before
// and after its children's summaries are saved.
package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Never point this at an installed catalog: it writes summaries.
func TestDelegationSummariesReplica(t *testing.T) {
	path := os.Getenv("PHAROS_DELEGATION_REPLICA")
	if path == "" {
		t.Skip("set PHAROS_DELEGATION_REPLICA to a scratch catalog clone")
	}
	absolute, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if absolute == "/Volumes/euclid/Pharos/catalog/catalog.sqlite3" {
		t.Fatal("the probe only writes scratch clones")
	}
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	c.ensureDelegationSummaries()
	count := func(q string) int {
		var n int
		if err := c.reader().QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	page := func(label string) {
		ws := os.Getenv("PHAROS_DELEGATION_WORKSPACE")
		if ws == "" {
			return
		}
		start := time.Now()
		s, err := c.readDelegationSnapshotRead(ctx, ws, nil, true)
		if err != nil {
			t.Fatalf("%s page: %v", label, err)
		}
		known := 0
		for _, n := range s.Nodes {
			if m, ok := n.Self["tokens"]; ok && m.Value != nil {
				known++
			}
		}
		t.Logf("%s page: %d rows, %d with tokens, saved=%d unqueried=%d, %s", label, len(s.Nodes), known, s.Coverage.Saved, s.Coverage.Unqueried, time.Since(start).Round(time.Millisecond))
	}
	page("before")
	start := time.Now()
	if err := c.sweepDelegationSummaries(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("sweep: %s, hosts=%d in %d workspaces, dirty=%d", time.Since(start).Round(time.Millisecond),
		count("SELECT COUNT(*) FROM delegation_summary_hosts"), count("SELECT COUNT(DISTINCT workspace_id) FROM delegation_summary_hosts"),
		count("SELECT COUNT(*) FROM delegation_summary_dirty"))
	start = time.Now()
	total := 0
	for {
		n, err := c.refreshDelegationSummaries(ctx, 64)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n == 0 {
			break
		}
		t.Logf("  %d hosts summarized, %s, dirty=%d", total, time.Since(start).Round(time.Second), count("SELECT COUNT(*) FROM delegation_summary_dirty"))
	}
	t.Logf("backfill: %s, summaries=%d", time.Since(start).Round(time.Millisecond), count("SELECT COUNT(*) FROM delegation_summaries"))
	page("after")
	start = time.Now()
	if err := c.sweepDelegationSummaries(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("second sweep: %s, dirty=%d", time.Since(start).Round(time.Millisecond), count("SELECT COUNT(*) FROM delegation_summary_dirty"))
}
