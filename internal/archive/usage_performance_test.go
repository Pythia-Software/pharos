//go:build probe

// Usage timing probes on an isolated tools/dev.py snapshot. Probes build only
// with the probe tag, so they stay out of go test ./...:
//
//	PHAROS_USAGE_PROBE=<dev library>/catalog/catalog.sqlite3 \
//	  go test -tags probe -run 'TestUsagePerformance(Preparation|MachineTime)' -v ./internal/archive
//
// TestUsagePerformanceFilteredMetrics needs no snapshot: it times the Usage
// Token chart's and summary's metrics over a synthetic population, filtered
// and not, in memory and staged:
//
//	go test -tags probe -run TestUsagePerformanceFilteredMetrics -v ./internal/archive
//
// TestUsagePerformanceSQLHost needs no snapshot either. It compares complete
// Human Words HTTP requests with host SQL reductions enabled and disabled:
//
//	PHAROS_TEST_PROCESSES=1 go test -tags probe -run '^TestUsagePerformanceSQLHost$' -v -count=1 ./internal/archive
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
// returns that day's local midnight, so day windows end where the snapshot's
// data does.
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
				var result queryFixtureResult
				for range 5 {
					start, startCPU := time.Now(), processCPU()
					tx, release, err := beginQueryMemory(ctx, c.reader())
					if err != nil {
						t.Fatal(err)
					}
					d := queryFixtureFor("tool_calls", PharosToolCallsSchema())
					d.SourceSQL = toolCallDataset.querySourceSQL(d.Schema)
					result, err = d.executeFixtureIn(ctx, tx, nil, &q, qt.PlanOptions{Now: time.Now()})
					release()
					if err != nil {
						t.Fatal(err)
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

// TestUsagePerformanceFilteredMetrics times Usage's chart and summary metrics
// over 20,000 daily and 60,000 hourly rows across 10 repositories, models and
// providers, unfiltered and under typical filters. It logs the warm median
// latency and process CPU of each request answered in memory (as /metrics
// answers it), staged with the native SQL path (as filtered requests were
// answered before Query Table's in-memory executor), and staged through the
// adapter alone. Hourly rows are selected once per filter, outside the timing,
// as every path shares that step.

func processCPU() time.Duration {
	var usage syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

// TestUsagePerformanceSQLHost compares the V2 SQL accelerator and ordinary
// execution, and measures V3 after removal of the host pre-reducer. It includes
// dependency discovery, source projection, leases and HTTP
// serialization. The synthetic authored-message projection has 50,000 rows and
// nine integer fields; fixture preparation is outside the measured requests.
// Writing messages uses no tool cubes. Native SQL and ordinary Query Table use
// the same catalog, indexes, filters and authorization through the real routes.

// TestQueryTextPerformance times substring filters on the tool ledger's
// command, answered by its trigram index: rows, metrics the host reduces and
// metrics it leaves to Query Table, and a /query-v3 grouping, each with a
// selective needle, a broad one and the broad one negated. The synthetic
// ledger has 200,000 calls with commands of 60 to 500 bytes:
//
//	PHAROS_TEST_PROCESSES=1 go test -tags probe -run '^TestQueryTextPerformance$' -v -count=1 ./internal/archive
func TestQueryTextPerformance(t *testing.T) {
	c, config := testCatalog(t)
	override(t, &queryTextIndexBatch, 50_000)
	const population = 200_000
	seedToolCalls(t, c, population)
	tx := seedTx(t, c.DB)
	if _, err := tx.Exec(`UPDATE tool_calls SET command=printf('cd /work/repo%d && %s --flag-%d %s',rowid%50,
		CASE rowid%7 WHEN 0 THEN 'go test ./...' WHEN 1 THEN 'git status' WHEN 2 THEN 'rg -n pattern' WHEN 3 THEN 'npm --prefix web run check'
		WHEN 4 THEN 'sed -n 1,80p file.go' WHEN 5 THEN 'python3 tools/run.py' ELSE 'ls -la' END,rowid%1000,hex(randomblob(20+rowid%200)))`); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	publishProjections(t, c)
	c.maintainQueryTextIndexes(t.Context())
	s := testServer(t, config, c)
	measure := func(t *testing.T, path string, body any) {
		t.Helper()
		var expected string
		var wall []time.Duration
		for run := range 11 {
			start := time.Now()
			response := apiCall(t, s, config.APIToken, http.MethodPost, path, body)
			elapsed := time.Since(start)
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"error":"`) {
				t.Fatal(response.Code, response.Body.String())
			}
			if run == 0 {
				expected = response.Body.String()
				continue
			}
			if response.Body.String() != expected {
				t.Fatal("responses differ between runs")
			}
			wall = append(wall, elapsed)
		}
		t.Logf("%s wall=%v", t.Name(), medianDuration(wall).Round(10*time.Microsecond))
	}
	for _, needle := range []struct {
		name    string
		value   string
		negated bool
	}{{"selective", "--flag-42 ", false}, {"broad", "git status", false}, {"excluded", "git status", true}} {
		where := []qt.WhereTerm{{Field: "command", Op: "contains", Value: needle.value, Negated: needle.negated}}
		t.Run(needle.name+"/rows", func(t *testing.T) {
			q := queryFixtureRows{Version: 2, Profile: qt.SQLiteExpressionProfile,
				WireQuery: qt.WireQuery{Select: []string{"id", "command", "started_at"}, Where: where, Limit: 50}}
			measure(t, "/api/query/tool_calls/rows-v2", q)
		})
		t.Run(needle.name+"/host-metrics", func(t *testing.T) {
			q := metricRequest(qt.AggSpec{ID: "calls", Op: "count", GroupBy: []string{"tool_name"}},
				qt.AggSpec{ID: "duration", Op: "sum", Field: "duration_ms", GroupBy: []string{"tool_name"}})
			q.Where = where
			measure(t, "/api/query/tool_calls/metrics", q)
		})
		t.Run(needle.name+"/adapter-metrics", func(t *testing.T) {
			q := metricRequest(qt.AggSpec{ID: "calls", Op: "count", GroupBy: []string{"tool_name"}},
				qt.AggSpec{ID: "duration", Op: "avg", Field: "duration_ms", GroupBy: []string{"tool_name"}})
			q.Where = where
			measure(t, "/api/query/tool_calls/metrics", q)
		})
		t.Run(needle.name+"/v3-grouped", func(t *testing.T) {
			q := groupRequest("tool_name", qt.SummaryColumn{ID: "calls", Op: "count"}, qt.SummaryColumn{ID: "mean", Op: "avg", Field: "duration_ms"})
			q.Source = qt.WireQuery{Where: where}
			measure(t, "/api/query/tool_calls/query-v3", q)
		})
	}
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
	c.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
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

// A captured /metrics request diagnoses the exact narrow SQL plan without
// changing the isolated catalog. Set PHAROS_USAGE_REQUEST to its JSON body.
