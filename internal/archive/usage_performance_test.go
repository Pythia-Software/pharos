//go:build probe

// Usage timing probes on an isolated tools/dev.py snapshot. Probes build only
// with the probe tag, so they stay out of go test ./...:
//
//	PHAROS_USAGE_PROBE=<dev library>/catalog/catalog.sqlite3 \
//	  go test -tags probe -run 'TestUsagePerformance(Preparation|MachineTime)' -v ./internal/archive
//
// TestUsagePerformanceGroup also needs PHAROS_USAGE_REQUEST, a V3 request
// file. TestUsagePerformanceServer serves the snapshot at
// PHAROS_USAGE_SERVER (a loopback address) until it is interrupted;
// PHAROS_USAGE_ASSETS serves a checkout's assets and PHAROS_USAGE_TIMING a
// concurrency prototype file. The Machine Time probe and the server run as of
// the day the snapshot's tool cube was built.

package archive

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
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

// usagePerformanceDay pins c's clock to the day its tool cube was built, and
// returns that day's local midnight. A read-only snapshot cannot rebuild the
// cube, which is fresh, and answers Machine Time, only on that day.
func usagePerformanceDay(t *testing.T, c *Catalog) time.Time {
	t.Helper()
	var built string
	if err := c.DB.QueryRow("SELECT value FROM meta WHERE key='tool_rollup_day'").Scan(&built); err != nil {
		t.Fatal(err)
	}
	midnight, err := time.ParseInLocation("2006-01-02", built, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return midnight.Add(12 * time.Hour) }
	return midnight
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

// TestUsagePerformanceMachineTime times the fact cubes that each Machine Time
// period asks /metrics for, as the projection accelerator answers them: the
// current and previous windows of 7, 14, 30 and 90 days, and all time by
// month. It logs the largest metric's groups, the warm median latency and CPU
// of the page's request, and of the single cube crossing time with operations
// that the page asked for until October 2026.
func TestUsagePerformanceMachineTime(t *testing.T) {
	c := usagePerformanceCatalog(t)
	ctx := context.Background()
	midnight := usagePerformanceDay(t, c)
	for _, days := range []int{7, 14, 30, 90, 0} {
		for _, previous := range []bool{false, true} {
			if days == 0 && previous {
				continue
			}
			period, where := "month", []qt.WhereTerm(nil)
			if days > 0 {
				end := midnight.AddDate(0, 0, map[bool]int{false: 0, true: -days}[previous])
				period, where = "day", []qt.WhereTerm{
					{Field: "started_at", Op: ">=", Value: end.AddDate(0, 0, -days).UTC().Format(time.RFC3339)},
					{Field: "started_at", Op: "<", Value: end.UTC().Format(time.RFC3339)},
				}
			}
			for _, shape := range []struct {
				name  string
				cubes [][]string
			}{
				{"crossed", [][]string{{"repository_name", "activity", period, "status", "operation_key", "operation_name", "tool_name", "mcp_server"}}},
				{"page", [][]string{{"repository_name", "activity", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}, {period, "status"}}},
			} {
				specs := []qt.AggSpec{{ID: "population", Op: "count"}}
				for i, groups := range shape.cubes {
					specs = append(specs, qt.AggSpec{ID: fmt.Sprint("time", i), Op: "sum", Field: "duration_ms", GroupBy: groups}, qt.AggSpec{ID: fmt.Sprint("timed", i), Op: "count", Field: "duration_ms", GroupBy: groups})
				}
				q := metricRequest(specs...)
				q.Where = where
				var elapsed, cpu []time.Duration
				var result qt.SQLiteV2ExecutionResult
				for range 5 {
					start, startCPU := time.Now(), processCPU()
					tx, release, err := beginQueryV2(ctx, c.reader())
					if err != nil {
						t.Fatal(err)
					}
					var handled bool
					result, handled, err = c.machineTimeMetricsV2(ctx, tx, queryV2Dataset("tool_calls", PharosToolCallsSchema()), q, qt.PlanOptions{Now: time.Now()})
					release()
					if err != nil || !handled {
						t.Fatal(err, handled)
					}
					elapsed, cpu = append(elapsed, time.Since(start)), append(cpu, processCPU()-startCPU)
				}
				var groups int64
				var failure string
				for _, metric := range result.Metrics.Metrics {
					groups, failure = max(groups, metric.GroupCount), cmp.Or(failure, metric.Error)
				}
				t.Logf("days=%d previous=%v %s: groups=%d error=%q cold=%v warm median=%v cpu=%v", days, previous, shape.name, groups, failure, elapsed[0], medianDuration(elapsed[1:]), medianDuration(cpu[1:]))
			}
		}
	}
}

func processCPU() time.Duration {
	var usage syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

func medianDuration(values []time.Duration) time.Duration {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

func TestUsagePerformanceServer(t *testing.T) {
	if os.Getenv("PHAROS_USAGE_SERVER") == "" {
		t.Skip("set PHAROS_USAGE_SERVER to a loopback address")
	}
	c := usagePerformanceCatalog(t)
	usagePerformanceDay(t, c)
	s := testServer(t, Config{Host: "127.0.0.1", APIToken: "usage-probe"}, c)
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
