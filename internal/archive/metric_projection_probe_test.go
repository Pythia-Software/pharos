//go:build probe || metricprobe

// Timing probe for the workspace metrics projection ("Updating workspace
// metrics", the second phase of Index the Search view). It WRITES to the
// catalog it is given, so point it at a disposable clone, never a library:
//
//	cp -c <replica>/catalog/catalog.sqlite3 <scratch>/catalog.sqlite3
//	PHAROS_METRIC_PROBE=<scratch>/catalog.sqlite3 PHAROS_METRIC_PROBE_N=2000 \
//	  go test -tags metricprobe -vet=off -run TestMetricProjectionProbe -v ./internal/archive
//
// The metricprobe tag builds this file alone; -tags probe also works once
// every other probe compiles. PHAROS_METRIC_PROBE_DIFF=1 logs stored rows
// that differ from what is recomputed.
//
// No initialization or migrations run; the clone must already be at this
// build's schema version.

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMetricProjectionProbe(t *testing.T) {
	path := os.Getenv("PHAROS_METRIC_PROBE")
	if path == "" {
		t.Skip("set PHAROS_METRIC_PROBE to a disposable catalog clone")
	}
	n := 2000
	if v, err := strconv.Atoi(os.Getenv("PHAROS_METRIC_PROBE_N")); err == nil && v > 0 {
		n = v
	}
	c, err := openCatalog(path, func(*Catalog) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer c.DB.Close()
	ctx := context.Background()
	sample, err := queryMaps(c.DB, `SELECT id FROM workspaces ORDER BY substr(id,-9,6) LIMIT ?`, 2*n)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(sample))
	for _, row := range sample {
		ids = append(ids, firstString(row["id"]))
	}
	first, second := ids[:len(ids)/2], ids[len(ids)/2:]

	// Compute only, from one snapshot, comparing with what is stored.
	func() {
		tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		book, err := c.loadPriceBookFrom(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		entities, same := 0, 0
		start := time.Now()
		if err := c.projectMetrics(ctx, tx, book, first, func(_, entity, entityID string, values map[string]statMetricValue) error {
			entities++
			data, _ := json.Marshal(values)
			var stored string
			if tx.QueryRowContext(ctx, `SELECT metrics_json FROM mcp_metric_projection WHERE entity=? AND entity_id=?`, entity, entityID).Scan(&stored) == nil && stored == string(data) {
				same++
			} else if stored != "" && os.Getenv("PHAROS_METRIC_PROBE_DIFF") != "" && entities < 40 {
				t.Logf("differs %s %s\n stored   %s\n computed %s", entity, entityID, stored, data)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		d := time.Since(start)
		t.Logf("compute only: %d workspaces, %d entities in %s (%.1f ms/workspace); %d entities identical to the stored projection",
			len(first), entities, d.Round(time.Millisecond), float64(d.Milliseconds())/float64(len(first)), same)
	}()

	for _, run := range []struct {
		batch int
		ids   []string
	}{{10, first}, {200, second}} {
		if _, err := c.DB.Exec(`INSERT OR IGNORE INTO mcp_metric_dirty SELECT value FROM json_each(?)`, jsonText(run.ids)); err != nil {
			t.Fatal(err)
		}
		var computed, published time.Duration
		var mark time.Time
		metricProjectionComputedHook = func() { mark = time.Now() }
		batches := 0
		start := time.Now()
		for {
			began := time.Now()
			mark = time.Time{}
			done, err := c.refreshMetricProjections(ctx, run.batch)
			if err != nil {
				t.Fatal(err)
			}
			if done == 0 {
				break
			}
			batches++
			computed += mark.Sub(began)
			published += time.Since(mark)
		}
		metricProjectionComputedHook = nil
		d := time.Since(start)
		t.Logf("refresh batch=%d: %d workspaces in %s (%.1f ms/workspace, %.0f/s), %d batches; claim+compute %s, publish %s",
			run.batch, len(run.ids), d.Round(time.Millisecond), float64(d.Milliseconds())/float64(len(run.ids)),
			float64(len(run.ids))/d.Seconds(), batches, computed.Round(time.Millisecond), published.Round(time.Millisecond))
	}
}

// TestMetricProjectionDump writes what the projection computes for a sample
// of workspaces (every N-th by ID, plus those with the most conversations) to
// PHAROS_METRIC_PROBE_DUMP, one sorted line per entity, so two builds can be
// compared with diff. It only reads.
func TestMetricProjectionDump(t *testing.T) {
	path, out := os.Getenv("PHAROS_METRIC_PROBE"), os.Getenv("PHAROS_METRIC_PROBE_DUMP")
	if path == "" || out == "" {
		t.Skip("set PHAROS_METRIC_PROBE and PHAROS_METRIC_PROBE_DUMP")
	}
	c, err := openCatalog(path, func(*Catalog) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer c.DB.Close()
	ctx := context.Background()
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	sample, err := queryMapsContext(ctx, tx, `SELECT id FROM (SELECT id FROM workspaces ORDER BY substr(id,-6) LIMIT 6000)
		UNION SELECT workspace_id FROM (SELECT workspace_id FROM conversations GROUP BY workspace_id ORDER BY COUNT(*) DESC LIMIT 300)
		UNION SELECT workspace_id FROM (SELECT DISTINCT workspace_id FROM metrics LIMIT 500)
		UNION SELECT workspace_id FROM (SELECT DISTINCT workspace_id FROM agent_sessions WHERE native_id<>'main' LIMIT 1500) ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	book, err := c.loadPriceBookFrom(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	start := time.Now()
	ids := make([]string, 0, len(sample))
	for _, row := range sample {
		ids = append(ids, firstString(row["id"]))
	}
	if err := c.projectMetrics(ctx, tx, book, ids, func(id, entity, entityID string, values map[string]statMetricValue) error {
		data, _ := json.Marshal(values)
		lines = append(lines, id+" "+entity+" "+entityID+" "+string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d workspaces, %d entities in %s", len(sample), len(lines), time.Since(start).Round(time.Millisecond))
	sort.Strings(lines)
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
