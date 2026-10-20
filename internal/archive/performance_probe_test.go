//go:build probe

// Query plan and timing probes for the Tools datasets, on an isolated
// tools/dev.py snapshot. Probes build only with the probe tag, so they stay
// out of go test ./...:
//
//	PHAROS_QUERY_PROBE=<dev library>/catalog/catalog.sqlite3 \
//	  go test -tags probe -run 'TestQuery(PerformanceProbe|PerformanceProfile|IndexedPerformanceProfile)' -v ./internal/archive
//
// PHAROS_QUERY_PROBE_SQL=<file> also writes the compiled rows query there.

package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// No initialization, migrations or source writes run against the selected
// library.
func TestQueryPerformanceProbe(t *testing.T) {
	path := os.Getenv("PHAROS_QUERY_PROBE")
	if path == "" {
		t.Skip("set PHAROS_QUERY_PROBE to a dev catalog")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), ".pharos-dev")); err != nil {
		t.Fatal("probe requires an isolated dev library")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	schema := PharosToolCallsSchema()
	d := toolCallDataset.project([]string{"id", "provider", "started_at"})
	source := d.v2Source(schema)
	q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "provider"}, Limit: 50}}
	p, err := qt.CompileSQLiteRowsV2(ctx, q, schema, qt.PlanOptions{SourceSQL: source})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		label, sql string
		args       []any
	}{
		{"count", "SELECT COUNT(*) FROM (SELECT 1 FROM (" + source + ") r LIMIT 250001)", nil},
		{"rows", p.SQL, p.Args},
		{"minimal-count", "SELECT COUNT(*) FROM tool_calls t WHERE " + d.base, nil},
	} {
		plan, err := queryMaps(db, "EXPLAIN QUERY PLAN "+query.sql, query.args...)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(query.label, plan)
		if query.label != "rows" {
			start := time.Now()
			var n int64
			if err := db.QueryRow(query.sql, query.args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			t.Log(query.label, n, time.Since(start))
		}
	}
	if output := os.Getenv("PHAROS_QUERY_PROBE_SQL"); output != "" {
		if err := os.WriteFile(output, []byte(fmt.Sprint(p.SQL)), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueryPerformanceProfile(t *testing.T) {
	path := os.Getenv("PHAROS_QUERY_PROBE")
	if path == "" {
		t.Skip("set PHAROS_QUERY_PROBE to a dev catalog")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), ".pharos-dev")); err != nil {
		t.Fatal("profile requires an isolated dev library")
	}
	db, err := sql.Open("sqlite", catalogDSN(path)+"&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	c := &Catalog{Path: path, DB: db}
	defer c.closeQuery()
	s := &Server{Catalog: c}
	q := groupRequest("provider", qt.SummaryColumn{ID: "records", Op: "count"}, qt.SummaryColumn{ID: "amount", Op: "sum", Field: "call_count"})
	for range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		start := time.Now()
		result, _, err := s.executeQueryV3(ctx, "tools", nil, PharosToolsSchema(), q)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		t.Log(result.SourceTotal, result.Total, time.Since(start))
	}
}

func TestQueryIndexedPerformanceProfile(t *testing.T) {
	path := os.Getenv("PHAROS_QUERY_PROBE")
	if path == "" {
		t.Skip("set PHAROS_QUERY_PROBE to a dev catalog")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), ".pharos-dev")); err != nil {
		t.Fatal("profile requires an isolated dev library")
	}
	db, err := sql.Open("sqlite", catalogDSN(path)+"&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	c := &Catalog{Path: path, DB: db}
	defer c.closeQuery()
	s := &Server{Catalog: c}
	q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "provider"}, Limit: 50}}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	start := time.Now()
	result, err := s.executeQueryV2(ctx, "tool_calls", nil, PharosToolCallsSchema(), &q, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(result.Rows.Total, len(result.Rows.Rows), time.Since(start))
}
