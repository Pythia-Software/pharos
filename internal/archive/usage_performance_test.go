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
	"maps"
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

// TestUsagePerformanceFilteredMetrics times Usage's chart and summary metrics
// over 20,000 daily and 60,000 hourly rows across 10 repositories, models and
// providers, unfiltered and under typical filters. It logs the warm median
// latency and process CPU of each request answered in memory (as /metrics
// answers it), staged with the native SQL path (as filtered requests were
// answered before Query Table's in-memory executor), and staged through the
// adapter alone. Hourly rows are selected once per filter, outside the timing,
// as every path shares that step.
func TestUsagePerformanceFilteredMetrics(t *testing.T) {
	catalog, config := testCatalog(t)
	s := testServer(t, config, catalog)
	ctx := context.Background()
	schema := PharosUsageSchema()
	origin := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	var daily, hourly []map[string]any
	for i := range 20_000 {
		day := origin.AddDate(0, 0, i%180)
		row := map[string]any{
			"agent_session_id": fmt.Sprint("session-", i/5), "day": day.Format("2006-01-02"), "week": day.AddDate(0, 0, -int(day.Weekday()+6)%7).Format("2006-01-02"),
			"month": day.Format("2006-01"), "repository_name": fmt.Sprint("repository-", i%10), "provider": []string{"claude", "codex", "antigravity"}[i%3],
			"model": fmt.Sprint("model-", i%10), "model_family": fmt.Sprint("family-", i%5), "effort": nil,
			"first_usage_at": day.Add(9 * time.Hour).Format(time.RFC3339), "last_usage_at": day.Add(11 * time.Hour).Format(time.RFC3339),
		}
		row["id"] = usageDailyID(row)
		totals := map[string]int64{}
		var cost float64
		for h := range 3 {
			hour := maps.Clone(row)
			hour["hour"] = day.Add(time.Duration(9+h) * time.Hour).Format(time.RFC3339)
			values := map[string]int64{"uncached_input_tokens": int64(100 + (i*7+h)%900), "cache_read_input_tokens": int64(1000 + (i*13+h)%9000),
				"cache_creation_input_tokens": int64((i + h) % 300), "output_tokens": int64(20 + (i*3+h)%400), "unclassified_tokens": 0}
			var total int64
			for field, value := range values {
				hour[field], totals[field], total = value, totals[field]+value, total+value
			}
			hour["total_tokens"], totals["total_tokens"] = total, totals["total_tokens"]+total
			hour["cost_usd"] = float64(total) * 0.0000031
			cost += hour["cost_usd"].(float64)
			hourly = append(hourly, hour)
		}
		for field, value := range totals {
			row[field] = value
		}
		row["cost_usd"] = cost
		daily = append(daily, row)
	}
	sums := func(fields []string, groups ...string) []qt.AggSpec {
		specs := []qt.AggSpec{}
		for _, field := range fields {
			specs = append(specs, qt.AggSpec{ID: field, Op: "sum", Field: field, GroupBy: groups})
		}
		return specs
	}
	types := []string{"uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "unclassified_tokens"}
	summary := append(sums([]string{"total_tokens", "cost_usd", "uncached_input_tokens", "cache_read_input_tokens", "output_tokens"}),
		qt.AggSpec{ID: "agent_sessions", Op: "count_distinct", Field: "agent_session_id"})
	for _, surface := range []struct {
		name  string
		specs []qt.AggSpec
	}{
		{"token summary", summary},
		{"weekly cost by provider", sums([]string{"cost_usd"}, "week", "provider")},
		{"daily token types", sums(types, "day")},
		{"hourly cost by provider", sums([]string{"cost_usd"}, "hour", "provider")},
	} {
		for _, filter := range []struct {
			name  string
			where []qt.WhereTerm
		}{
			{"unfiltered", nil},
			{"repository", []qt.WhereTerm{{Field: "repository_name", Op: "=", Value: "repository-3"}}},
			{"model", []qt.WhereTerm{{Field: "model", Op: "=", Value: "model-5"}}},
			{"provider", []qt.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}},
			{"90 days", []qt.WhereTerm{{Field: "first_usage_at", Op: ">=", Value: origin.AddDate(0, 0, 90).Format(time.RFC3339)}}},
			{"repository contains", []qt.WhereTerm{{Field: "repository_name", Op: "contains", Value: "ITORY-1"}}},
		} {
			q := metricRequest(surface.specs...)
			q.Where = filter.where
			options := qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name, Now: time.Now()}
			names, err := queryV2SourceFields(ctx, catalog.reader(), schema, nil, &q, options.Now)
			if err != nil {
				t.Fatal(err)
			}
			hours, err := selectUsageHours(daily, hourly, q.Where)
			if err != nil {
				t.Fatal(err)
			}
			line := fmt.Sprintf("%-24s %-20s", surface.name, filter.name)
			var answers []qt.SQLiteV2ExecutionResult
			for _, path := range []string{"in memory", "staged", "adapter"} {
				override(t, &nativeMetrics, path != "adapter")
				var elapsed, cpu []time.Duration
				var result queryV2ExecutionResult
				for range 7 {
					start, startCPU := time.Now(), processCPU()
					d := queryV2Dataset("usage", schema)
					if path == "in memory" {
						var handled bool
						result.SQLiteV2ExecutionResult, handled, err = s.metricsWithoutStaging(ctx, "usage", sqlDataset{}, d, q, options, daily, hours, false)
						if err == nil && !handled {
							t.Fatalf("%s: not answered in memory", line)
						}
					} else {
						// The staged paths, without the in-memory executor ahead of them.
						err = s.executeStagedQueryV2(ctx, "usage", sqlDataset{}, false, d, nil, &q, options, nil, daily, hours, names, &result)
					}
					if err != nil {
						t.Fatal(line, path, err)
					}
					elapsed, cpu = append(elapsed, time.Since(start)), append(cpu, processCPU()-startCPU)
				}
				answers = append(answers, result.SQLiteV2ExecutionResult)
				line += fmt.Sprintf("  %s %8v (cpu %8v)", path, medianDuration(elapsed[1:]).Round(10*time.Microsecond), medianDuration(cpu[1:]).Round(10*time.Microsecond))
			}
			for _, answer := range answers[1:] {
				if !identical(answer, answers[0]) {
					t.Fatalf("%s: paths disagree", line)
				}
			}
			t.Log(line)
		}
	}
}

func processCPU() time.Duration {
	var usage syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

// TestUsagePerformanceSQLHost measures the cost of removing the general SQL
// bypasses, including dependency discovery, source projection, leases and HTTP
// serialization. The synthetic authored-message projection has 50,000 rows and
// nine integer fields; fixture preparation is outside the measured requests.
// Writing messages uses no tool cubes. Native SQL and ordinary Query Table use
// the same catalog, indexes, filters and authorization through the real routes.
func TestUsagePerformanceSQLHost(t *testing.T) {
	c, config := testCatalog(t)
	if err := c.RebuildAuthorship(t.Context(), "probe"); err != nil {
		t.Fatal(err)
	}
	tx := seedTx(t, c.DB)
	for i := range 5 {
		id := fmt.Sprintf("sql-probe-%d", i)
		provider := []string{"codex", "claude"}[i%2]
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at)
			VALUES(?,'codex','local',?,'SQL probe','2026-09-01T00:00:00Z')`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id)
			VALUES(?,?,?,'local',?)`, id, id, provider, id); err != nil {
			t.Fatal(err)
		}
	}
	const population = 50_000
	if _, err := tx.Exec(`WITH RECURSIVE samples(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM samples WHERE n+1<?)
		INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,source_order,created_at)
		SELECT 'sql-message-'||n,'sql-probe-'||(n%5),CAST(n AS TEXT),'user','message','Synthetic SQL probe','probe',n,
		printf('2026-09-%02dT12:00:00.000Z',1+n%5) FROM samples`, population); err != nil {
		t.Fatal(err)
	}
	columns := []string{"message_id", "conversation_id", "workspace_id", "sent_at", "day", "total_chars", "spans_json"}
	values := []string{"id", "conversation_id", "conversation_id", "created_at", "substr(created_at,1,10)", "200", "'[]'"}
	var words []string
	for i, category := range authorshipCategories {
		columns = append(columns, category+"_words")
		value := fmt.Sprintf("((source_order+%d)%%7)", i)
		values = append(values, value)
		words = append(words, value)
	}
	columns, values = append(columns, "words"), append(values, strings.Join(words, "+"))
	if _, err := tx.Exec("INSERT INTO message_authorship(" + strings.Join(columns, ",") + ") SELECT " + strings.Join(values, ",") + " FROM messages"); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	s := testServer(t, config, c)
	override(t, &nativeMetrics, nativeMetrics)
	measure := func(t *testing.T, path string, body any) {
		t.Helper()
		var expected any
		for _, enabled := range []bool{true, false} {
			nativeMetrics = enabled
			var wall, cpu []time.Duration
			for run := range 4 {
				start, startCPU := time.Now(), processCPU()
				response := apiCall(t, s, config.APIToken, http.MethodPost, path, body)
				elapsed, usedCPU := time.Since(start), processCPU()-startCPU
				if response.Code != http.StatusOK {
					t.Fatal(response.Code, response.Body.String())
				}
				var result any
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				metrics, ok := result.(map[string]any)["metrics"].([]any)
				if !ok || len(metrics) == 0 {
					t.Fatal("missing metrics", response.Body.String())
				}
				for _, metric := range metrics {
					m := metric.(map[string]any)
					if firstString(m["error"]) != "" || len(m["buckets"].([]any)) == 0 {
						t.Fatal("metric failed", m)
					}
					for _, bucket := range m["buckets"].([]any) {
						if firstString(bucket.(map[string]any)["error"]) != "" {
							t.Fatal("bucket failed", bucket)
						}
					}
				}
				if expected == nil {
					expected = result
				} else if !identical(expected, result) {
					t.Fatalf("host and adapter responses differ: %s", response.Body.String())
				}
				if run > 0 {
					wall, cpu = append(wall, elapsed), append(cpu, usedCPU)
				}
			}
			t.Logf("native=%v rows=%d wall=%v cpu=%v", enabled, population,
				medianDuration(wall).Round(10*time.Microsecond), medianDuration(cpu).Round(10*time.Microsecond))
		}
	}
	for _, shape := range []struct {
		name   string
		groups []string
		where  []qt.WhereTerm
	}{
		{"scalar", nil, nil},
		{"grouped", []string{"day"}, nil},
		{"filtered", []string{"day"}, []qt.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}},
	} {
		t.Run("v2/"+shape.name, func(t *testing.T) {
			q := metricRequest(qt.AggSpec{ID: "messages", Op: "count", GroupBy: shape.groups},
				qt.AggSpec{ID: "words", Op: "sum", Field: "words", GroupBy: shape.groups})
			for _, category := range authorshipCategories {
				q.Metrics = append(q.Metrics, qt.AggSpec{ID: category, Op: "sum", Field: category + "_words", GroupBy: shape.groups})
			}
			q.Where = shape.where
			measure(t, "/api/query/writing_messages/metrics", q)
		})
	}
	t.Run("v3/grouped", func(t *testing.T) {
		q := groupRequest("day", qt.SummaryColumn{ID: "messages", Op: "count"},
			qt.SummaryColumn{ID: "words_total", Op: "sum", Field: "words"}, qt.SummaryColumn{ID: "mean", Op: "avg", Field: "words"})
		q.Result.OrderBy = qt.OrderBys{{Field: "day", Dir: "asc"}}
		q.Result.Limit = 2
		q.Metrics = []qt.AggSpec{{ID: "all_groups", Op: "count"}, {ID: "shown", Op: "sum", Field: "messages", Scope: "shownRows"}}
		measure(t, "/api/query/writing_messages/query-v3", q)
	})
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

// A captured /metrics request diagnoses the exact narrow SQL plan without
// changing the isolated catalog. Set PHAROS_USAGE_REQUEST to its JSON body.
func TestUsagePerformanceNativeMetrics(t *testing.T) {
	c := usagePerformanceCatalog(t)
	file := os.Getenv("PHAROS_USAGE_REQUEST")
	if file == "" {
		t.Skip("set PHAROS_USAGE_REQUEST")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var q qt.MetricQuery
	if err = json.Unmarshal(body, &q); err != nil {
		t.Fatal(err)
	}
	schema := PharosToolCallsSchema()
	d := queryV2Dataset("tool_calls", schema)
	d.MaxPopulation = 2_000_000
	table, err := c.queryTextDataset(t.Context(), "tool_calls", toolCallDataset)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("facts", table.scalarFacts, "text", table.queryText != nil)
	consumed := whereFields(table.residualWhere(q.Where))
	for _, m := range q.Metrics {
		consumed = append(consumed, m.GroupBy...)
		if m.Field != "" {
			consumed = append(consumed, m.Field)
		}
	}
	projected, covered := table.project(consumed).preferCoveringScan(q.Where)
	d.SourceSQL, d.SourceArgs = projected.v2WindowedSource(schema, q.Where, time.Now())
	t.Log("covered", covered, "facts source", strings.Contains(d.SourceSQL, "FROM tool_query_facts"))
	plan, err := queryMapsContext(t.Context(), c.DB, "EXPLAIN QUERY PLAN SELECT r.week,r.activity,COUNT(*),SUM(r.duration_ms) FROM ("+d.SourceSQL+") r GROUP BY 1,2", d.SourceArgs...)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range plan {
		t.Log(firstString(row["detail"]))
	}
	for i := 0; i < 2; i++ {
		tx, err := c.DB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		result, handled, err := c.nativeMetricsV2(t.Context(), tx, d, q, qt.PlanOptions{Now: time.Now()}, &table)
		_ = tx.Rollback()
		t.Log("native", time.Since(started), "handled", handled, "err", err, "metrics", len(result.Metrics.Metrics))
	}
}
