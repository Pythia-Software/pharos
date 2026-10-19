package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

// Opt-in probes use a read-only development snapshot, without initialization,
// migrations, background maintenance, or writes to the source catalog.
func usagePerformanceCatalog(t *testing.T) *Catalog {
	t.Helper()
	path := os.Getenv("PHAROS_USAGE_PROBE")
	if path == "" {
		t.Skip("set PHAROS_USAGE_PROBE to an isolated dev catalog")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(path)), ".pharos-dev")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", readerDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	c := &Catalog{Path: path, DB: db}
	t.Cleanup(func() { c.closeQuery(); db.Close() })
	return c
}

func TestUsagePerformancePreparation(t *testing.T) {
	c := usagePerformanceCatalog(t)
	start := time.Now()
	ledger, err := c.representativeLedger()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("ledger", len(ledger), time.Since(start))
	start = time.Now()
	_, err = c.usageConversationRoots(ledger)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("lineage", time.Since(start))
	for range 2 {
		start = time.Now()
		rows, err := c.localUsageRows(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Log("daily", len(rows), time.Since(start))
	}
}

func TestUsagePerformanceGroup(t *testing.T) {
	if os.Getenv("PHAROS_USAGE_REQUEST") == "" {
		t.Skip("set PHAROS_USAGE_REQUEST to a V3 request fixture")
	}
	c := usagePerformanceCatalog(t)
	s := &Server{Catalog: c}
	source, err := os.ReadFile(os.Getenv("PHAROS_USAGE_REQUEST"))
	if err != nil {
		t.Fatal(err)
	}
	var q qt.QueryRequestV3
	if err := json.Unmarshal(source, &q); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		start := time.Now()
		result, _, err := s.executeQueryV3(context.Background(), "usage", nil, PharosUsageSchema(), q)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(result.SourceTotal, result.Total, time.Since(start))
	}
}

func TestUsagePerformanceMachineTime(t *testing.T) {
	c := usagePerformanceCatalog(t)
	schema, err := (&Server{Catalog: c}).queryTableSchema("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		start := time.Now()
		request := querytable.AggregationRequest{RequireComplete: true, Aggregations: []querytable.Aggregation{
			{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: []string{"repository_name", "activity", "month", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}},
			{ID: "timed", Op: "count", Field: "duration_ms", GroupBy: []string{"repository_name", "activity", "month", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}},
			{ID: "population", Op: "count"},
		}}
		result, handled, err := c.machineTimeAggregations(context.Background(), request, schema)
		if err != nil || !handled {
			t.Fatal(err, handled)
		}
		t.Log(len(result.Metrics[0].Buckets), time.Since(start))
	}
}

func TestUsagePerformanceServer(t *testing.T) {
	if os.Getenv("PHAROS_USAGE_SERVER") == "" {
		t.Skip("set PHAROS_USAGE_SERVER to a loopback address")
	}
	c := usagePerformanceCatalog(t)
	s := NewServer(Config{Host: "127.0.0.1", APIToken: "usage-probe"}, c)
	c.background = func(func(context.Context)) bool { return false }
	defer s.life.cancel()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if assets := os.Getenv("PHAROS_USAGE_ASSETS"); assets != "" {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				http.StripPrefix("/assets/", http.FileServer(http.Dir(assets))).ServeHTTP(w, r)
				return
			}
			if isUIPage(r.URL.Path) {
				source, err := os.ReadFile(filepath.Join(assets, "ui.py"))
				if err != nil {
					t.Error(err)
					return
				}
				html := uiHTML(source)
				if os.Getenv("PHAROS_USAGE_TIMING") != "" {
					html = strings.Replace(html, "<html", "<html data-concurrency-prototype", 1)
				}
				writeHTML(w, html)
				return
			}
		}
		if r.URL.Path == "/api/usage/concurrency-prototype" && os.Getenv("PHAROS_USAGE_TIMING") != "" {
			http.ServeFile(w, r, os.Getenv("PHAROS_USAGE_TIMING"))
			return
		}
		r.Header.Set("Authorization", "Bearer usage-probe")
		s.ServeHTTP(w, r)
	})
	t.Fatal(http.ListenAndServe(os.Getenv("PHAROS_USAGE_SERVER"), handler))
}
