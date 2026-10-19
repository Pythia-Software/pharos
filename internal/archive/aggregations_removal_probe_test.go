//go:build probe

// A comparison of the legacy /aggregations route with /metrics on a synthetic
// library, for the change that removes the legacy route:
//
//	go test -tags probe -run 'TestAggregationsRemovalProbe$' -v -timeout 60m ./internal/archive
//
// PHAROS_PROBE_CALLS sets the number of tool calls (default 300,000), and
// PHAROS_PROBE_ONLY keeps the surfaces whose names contain it. It prints one
// row per page surface: group counts; for each route its first call and the
// medians of nine more of wall and process CPU time, the two routes taking
// turns; the generic V2 executor without the Machine Time accelerator; and
// whether every bucket matches. On a loaded machine CPU time is the steadier
// comparison.
//
// TestAggregationsRemovalProbeScaling times single /metrics requests on the
// same data with few tool calls: Human Words' message mix with 1, 2 and 9
// metrics, the hourly and weekly Token chart, and Usage's per-request
// staging alone. PHAROS_PROBE_ONLY picks requests; PHAROS_PROBE_REPEAT
// multiplies the ten timed runs.

package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAggregationsRemovalProbe(t *testing.T) {
	calls := 300_000
	if raw := os.Getenv("PHAROS_PROBE_CALLS"); raw != "" {
		calls, _ = strconv.Atoi(raw)
	}
	catalog, config := testCatalog(t)
	now := time.Now()
	start := time.Now()
	seedProbeLibrary(t, catalog, calls, now)
	t.Logf("seeded %d calls in %v", calls, time.Since(start))
	start = time.Now()
	if err := catalog.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var cubeRows int
	catalog.DB.QueryRow("SELECT COUNT(*) FROM tool_call_cube").Scan(&cubeRows)
	t.Logf("tool rollup in %v (%d cube rows)", time.Since(start), cubeRows)
	start = time.Now()
	if err := catalog.RebuildAuthorship(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}
	t.Logf("authorship in %v", time.Since(start))
	server := testServer(t, config, catalog)

	// The process's CPU time: on a loaded machine, wall time also measures
	// waiting for a core.
	cpu := func() time.Duration {
		var usage syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
		return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	}
	var spent time.Duration
	post := func(path string, body any) (map[string]any, time.Duration, int) {
		t.Helper()
		started, before := time.Now(), cpu()
		response := apiCall(t, server, config.APIToken, "POST", path, body)
		elapsed := time.Since(started)
		spent = cpu() - before
		var decoded map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &decoded)
		if response.Code != 200 {
			decoded = map[string]any{"error": fmt.Sprint(decoded["error"])}
		}
		return decoded, elapsed, response.Code
	}
	// Both routes run in turn, so that load on a shared machine falls on
	// both alike: the first call of each, then the median of the next nine.
	type timing struct {
		body            map[string]any
		cold, warm, cpu time.Duration
	}
	timed := func(paths []string, bodies []any) []timing {
		t.Helper()
		out := make([]timing, len(paths))
		samples, cpus := make([][]time.Duration, len(paths)), make([][]time.Duration, len(paths))
		for round := range 10 {
			for i := range paths {
				body, elapsed, _ := post(paths[i], bodies[i])
				if round == 0 {
					out[i].body, out[i].cold = body, elapsed
				} else {
					samples[i] = append(samples[i], elapsed)
					cpus[i] = append(cpus[i], spent)
				}
			}
		}
		for i := range out {
			slices.Sort(samples[i])
			slices.Sort(cpus[i])
			out[i].warm, out[i].cpu = samples[i][len(samples[i])/2], cpus[i][len(cpus[i])/2]
		}
		return out
	}
	type metric struct {
		ID      string `json:"id"`
		Error   string `json:"error"`
		Buckets []struct {
			Keys  []any `json:"keys"`
			Value any   `json:"value"`
			Count int64 `json:"count"`
		} `json:"buckets"`
	}
	read := func(body map[string]any) ([]metric, string) {
		if body["error"] != nil {
			return nil, fmt.Sprint(body["error"])
		}
		encoded, _ := json.Marshal(body["metrics"])
		var metrics []metric
		_ = json.Unmarshal(encoded, &metrics)
		for _, m := range metrics {
			if m.Error != "" {
				return metrics, m.ID + ": " + m.Error
			}
		}
		return metrics, ""
	}
	// Compare every bucket's value and count, whatever the order.
	compare := func(name string, legacy, current []metric) string {
		if len(legacy) != len(current) {
			return fmt.Sprintf("%d metrics against %d", len(legacy), len(current))
		}
		for i := range legacy {
			want := map[string][2]float64{}
			for _, bucket := range legacy[i].Buckets {
				value, _ := number(bucket.Value)
				encoded, _ := json.Marshal(bucket.Keys)
				want[string(encoded)] = [2]float64{value, float64(bucket.Count)}
			}
			if len(want) != len(current[i].Buckets) {
				return fmt.Sprintf("%s: %d groups against %d", legacy[i].ID, len(want), len(current[i].Buckets))
			}
			for _, bucket := range current[i].Buckets {
				value, _ := number(bucket.Value)
				encoded, _ := json.Marshal(bucket.Keys)
				expected, ok := want[string(encoded)]
				if !ok || math.Abs(expected[0]-value) > 1e-6*math.Max(1, math.Abs(value)) || expected[1] != float64(bucket.Count) {
					return fmt.Sprintf("%s %s: legacy %v, metrics value %v count %d", legacy[i].ID, encoded, expected, value, bucket.Count)
				}
			}
		}
		return ""
	}
	groups := func(metrics []metric) int {
		most := 0
		for _, m := range metrics {
			most = max(most, len(m.Buckets))
		}
		return most
	}
	type surface struct {
		name, dataset string
		where         []map[string]any
		specs         []map[string]any
	}
	machine := func(period string) []map[string]any {
		keys := []string{"repository_name", "activity", period, "status", "operation_key", "operation_name", "tool_name", "mcp_server"}
		return []map[string]any{{"id": "time", "op": "sum", "field": "duration_ms", "groupBy": keys}, {"id": "timed", "op": "count", "field": "duration_ms", "groupBy": keys}, {"id": "population", "op": "count", "groupBy": []string{}}}
	}
	window := func(days int) []map[string]any {
		end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		begin := end.AddDate(0, 0, -days)
		if days > 30 {
			return []map[string]any{{"field": "started_at", "op": ">=", "value": begin.UTC().Format(time.RFC3339Nano)}, {"field": "started_at", "op": "<", "value": end.UTC().Format(time.RFC3339Nano)}}
		}
		selected := []map[string]any{}
		for day := begin; day.Before(end); day = day.AddDate(0, 0, 1) {
			selected = append(selected, map[string]any{"field": "day", "op": "=", "value": day.Format("2006-01-02")})
		}
		return []map[string]any{{"any": selected}}
	}
	sum := func(field string, groupBy ...string) map[string]any {
		return map[string]any{"id": field + ":" + strings.Join(groupBy, ","), "op": "sum", "field": field, "groupBy": append([]string{}, groupBy...)}
	}
	types := func(period string) []map[string]any {
		out := []map[string]any{}
		for _, field := range []string{"uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "unclassified_tokens"} {
			out = append(out, sum(field, period))
		}
		return out
	}
	mix := []map[string]any{{"id": "messages", "op": "count", "groupBy": []string{}}}
	for _, category := range authorshipCategories {
		mix = append(mix, map[string]any{"id": category, "op": "sum", "field": category + "_words", "groupBy": []string{}})
	}
	surfaces := []surface{
		{"Machine Time 7 days", "tool_calls", window(7), machine("day")},
		{"Machine Time 30 days", "tool_calls", window(30), machine("day")},
		{"Machine Time 90 days", "tool_calls", window(90), machine("day")},
		{"Machine Time all time", "tool_calls", []map[string]any{}, machine("month")},
		{"Machine card: activity by day, 30 days", "tool_calls", window(30), []map[string]any{{"id": "time", "op": "sum", "field": "duration_ms", "groupBy": []string{"activity", "day"}}, {"id": "timed", "op": "count", "field": "duration_ms", "groupBy": []string{"activity", "day"}}, {"id": "population", "op": "count", "groupBy": []string{}}}},
		{"Tool call trend, 30 days", "tool_calls", window(30), []map[string]any{{"id": "calls", "op": "count", "groupBy": []string{"day"}}, {"id": "timed", "op": "count", "field": "duration_ms", "groupBy": []string{"day"}}, {"id": "time", "op": "sum", "field": "duration_ms", "groupBy": []string{"day"}}}},
		{"Token chart hourly × type", "usage", []map[string]any{}, types("hour")},
		{"Token chart hourly × repository", "usage", []map[string]any{}, []map[string]any{sum("total_tokens", "hour", "repository_name")}},
		{"Token chart hourly × model, 7 days", "usage", []map[string]any{{"field": "last_usage_at", "op": ">=", "value": now.AddDate(0, 0, -7).UTC().Format(time.RFC3339)}}, []map[string]any{sum("total_tokens", "hour", "model_family")}},
		{"Token chart weekly × type", "usage", []map[string]any{}, types("week")},
		{"Token chart weekly × repository", "usage", []map[string]any{}, []map[string]any{sum("total_tokens", "week", "repository_name")}},
		{"Token chart weekly cost × provider", "usage", []map[string]any{}, []map[string]any{sum("cost_usd", "week", "provider")}},
		{"Token chart hourly cost × provider", "usage", []map[string]any{}, []map[string]any{sum("cost_usd", "hour", "provider")}},
		{"Token summary", "usage", []map[string]any{}, []map[string]any{sum("total_tokens"), sum("cost_usd"), sum("uncached_input_tokens"), sum("cache_read_input_tokens"), sum("output_tokens"), {"id": "agent_sessions", "op": "count_distinct", "field": "agent_session_id", "groupBy": []string{}}}},
		{"MessageMix", "writing_messages", []map[string]any{}, mix},
	}
	report := []string{"| Surface | Groups | Legacy cold / warm / CPU | /metrics cold / warm / CPU | Warm ratio | CPU ratio | Generic V2 (no accelerator) | Values |", "|---|---|---|---|---|---|---|---|"}
	for _, s := range surfaces {
		if only := os.Getenv("PHAROS_PROBE_ONLY"); only != "" && !strings.Contains(s.name, only) {
			continue
		}
		timings := timed([]string{"/api/query/" + s.dataset + "/aggregations", "/api/query/" + s.dataset + "/metrics"}, []any{
			map[string]any{"where": s.where, "aggregations": s.specs, "requireComplete": true},
			map[string]any{"version": 2, "where": s.where, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": s.specs}})
		legacyBody, legacyCold, legacyWarm, legacyCPU := timings[0].body, timings[0].cold, timings[0].warm, timings[0].cpu
		currentBody, currentCold, currentWarm, currentCPU := timings[1].body, timings[1].cold, timings[1].warm, timings[1].cpu
		legacy, legacyErr := read(legacyBody)
		current, currentErr := read(currentBody)
		verdict := "identical"
		switch {
		case legacyErr != "" || currentErr != "":
			verdict = fmt.Sprintf("legacy error %q, metrics error %q", legacyErr, currentErr)
		default:
			if difference := compare(s.name, legacy, current); difference != "" {
				verdict = "DIFFERENT: " + difference
				t.Errorf("%s: %s", s.name, difference)
			}
		}
		generic := "n/a"
		if s.dataset == "tool_calls" {
			q := metricRequest()
			raw, _ := json.Marshal(map[string]any{"version": 2, "profile": "qt-sqlite-v1", "where": s.where, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": s.specs})
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			// A caller's own population budget sends the request to the generic executor.
			result, err := server.executeQueryV2(context.Background(), "tool_calls", nil, PharosToolCallsSchema(), nil, &q, queryTablePopulation)
			elapsed := time.Since(started)
			switch {
			case err != nil:
				generic = fmt.Sprintf("fails in %v: %v", elapsed.Round(time.Millisecond), err)
			default:
				generic = fmt.Sprint(elapsed.Round(time.Millisecond))
				for _, m := range result.Metrics.Metrics {
					if m.Error != "" {
						generic = fmt.Sprintf("%s fails in %v: %s", m.ID, elapsed.Round(time.Millisecond), m.Error)
						break
					}
				}
			}
		}
		round := func(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }
		line := fmt.Sprintf("| %s | %d / %d | %v / %v / %v | %v / %v / %v | %.2f× | %.2f× | %s | %s |", s.name, groups(legacy), groups(current),
			round(legacyCold), round(legacyWarm), round(legacyCPU), round(currentCold), round(currentWarm), round(currentCPU),
			float64(currentWarm)/float64(legacyWarm), float64(currentCPU)/float64(max(legacyCPU, time.Microsecond)), generic, verdict)
		report = append(report, line)
		t.Log(line)
	}
	sort.Strings(report[2:])
	fmt.Println(strings.Join(report, "\n"))
}

func TestAggregationsRemovalProbeScaling(t *testing.T) {
	catalog, config := testCatalog(t)
	seedProbeLibrary(t, catalog, 1000, time.Now())
	if err := catalog.RebuildAuthorship(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	repeat, _ := strconv.Atoi(os.Getenv("PHAROS_PROBE_REPEAT"))
	repeat = max(repeat, 1)
	mix := []map[string]any{{"id": "messages", "op": "count", "groupBy": []string{}}}
	for _, category := range authorshipCategories {
		mix = append(mix, map[string]any{"id": category, "op": "sum", "field": category + "_words", "groupBy": []string{}})
	}
	bodies := map[string]map[string]any{
		"mix":    {"version": 2, "where": []any{}, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": mix},
		"mix1":   {"version": 2, "where": []any{}, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": mix[:1]},
		"mix2":   {"version": 2, "where": []any{}, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": mix[:2]},
		"hourly": {"version": 2, "where": []any{}, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": []map[string]any{{"id": "t", "op": "sum", "field": "total_tokens", "groupBy": []string{"hour", "repository_name"}}}},
		"weekly": {"version": 2, "where": []any{}, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": []map[string]any{{"id": "t", "op": "sum", "field": "total_tokens", "groupBy": []string{"week"}}}},
	}
	types := []map[string]any{}
	for _, field := range []string{"uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "unclassified_tokens"} {
		types = append(types, map[string]any{"id": field, "op": "sum", "field": field, "groupBy": []string{"hour"}})
	}
	bodies["hourtypes"] = map[string]any{"version": 2, "where": []any{}, "orderBy": []any{}, "limit": 1, "offset": 0, "metrics": types}
	names := []string{"mix1", "mix2", "mix", "hourly", "weekly", "hourtypes"}
	if only := os.Getenv("PHAROS_PROBE_ONLY"); only != "" {
		names = strings.Split(only, ",")
	}
	for _, name := range names {
		dataset := "writing_messages"
		if name == "hourly" || name == "weekly" || name == "hourtypes" {
			dataset = "usage"
		}
		apiCall(t, server, config.APIToken, "POST", "/api/query/"+dataset+"/metrics", bodies[name])
		start := time.Now()
		for range 10 * repeat {
			if response := apiCall(t, server, config.APIToken, "POST", "/api/query/"+dataset+"/metrics", bodies[name]); response.Code != 200 {
				t.Fatal(response.Body.String())
			}
		}
		t.Logf("%s: %v per request", name, time.Since(start)/time.Duration(10*repeat))
	}
	// /metrics over Usage first copies the matching rows into a temporary
	// table; time that copy of the hourly rows, and one native grouping of it.
	hours, err := catalog.usageRowsByPeriod(time.Local, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Now()
	for range 10 * repeat {
		tx, release, err := beginQueryV2(ctx, catalog.reader())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stageQueryRows(ctx, tx, "probe_hours", hours, PharosUsageSchema(), []string{"hour", "id", "repository_name", "total_tokens"}); err != nil {
			t.Fatal(err)
		}
		release()
	}
	staged := time.Since(start) / time.Duration(10*repeat)
	start = time.Now()
	for range 10 * repeat {
		tx, release, err := beginQueryV2(ctx, catalog.reader())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stageQueryRows(ctx, tx, "probe_hours", hours, PharosUsageSchema(), []string{"hour", "id", "repository_name", "total_tokens"}); err != nil {
			t.Fatal(err)
		}
		if _, err := queryMapsContext(ctx, tx, `SELECT hour,repository_name,SUM(total_tokens),COUNT(*) FROM probe_hours GROUP BY 1,2`); err != nil {
			t.Fatal(err)
		}
		release()
	}
	t.Logf("staging %d hourly rows: %v; with one native GROUP BY: %v", len(hours), staged, time.Since(start)/time.Duration(10*repeat))
}

// seedProbeLibrary writes calls tool calls over the 120 days before now,
// skewed the way a real library is across 25 repositories, about 150
// operations and 8 MCP servers; hourly token usage over 90 days for 10
// repositories and several models; and about 20,000 user messages.
func seedProbeLibrary(t *testing.T, catalog *Catalog, calls int, now time.Time) {
	t.Helper()
	random := rand.New(rand.NewSource(17))
	zipf := func(n int) func() int {
		z := rand.NewZipf(random, 1.3, 1, uint64(n-1))
		return func() int { return int(z.Uint64()) }
	}
	tx := seedTx(t, catalog.DB)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatal(statement, err)
		}
	}
	const repositories, workspacesPerRepository = 25, 8
	for r := range repositories {
		exec("INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES(?,?,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')", fmt.Sprint("repo-", r), fmt.Sprintf("org/repository-%02d", r))
		for w := range workspacesPerRepository {
			id := fmt.Sprintf("ws-%d-%d", r, w)
			exec("INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES(?,?,?,?,?,?,?)", id, []string{"claude", "codex"}[w%2], "local", id, fmt.Sprint("repo-", r), "Work "+id, "2026-01-01T00:00:00.000Z")
			exec("INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES(?,?,?,?,?)", "conv-"+id, id, []string{"claude", "codex"}[w%2], "local", "native-"+id)
			exec("INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model,depth) VALUES(?,?,?,?,?,?,?,?)", "session-"+id, id, "conv-"+id, "main", "root", []string{"claude", "codex"}[w%2], []string{"claude-opus-5-5", "gpt-6-sol", "claude-sonnet-5", "claude-haiku-4-5"}[w%4], 0)
		}
	}
	type operation struct{ tool, category, server, program, subcommand, commandCategory string }
	operations := []operation{}
	programs := []struct{ name, category string }{{"git", "vcs"}, {"go", "test"}, {"npm", "build"}, {"rg", "search"}, {"ls", "read"}, {"cat", "read"}, {"python3", "process"}, {"make", "build"}, {"docker", "process"}, {"sed", "file-write"}, {"curl", "network"}, {"jq", "read"}, {"node", "process"}, {"cargo", "build"}, {"pytest", "test"}, {"gh", "network"}, {"find", "search"}, {"swift", "build"}}
	subcommands := []string{"status", "test", "run", "diff", "log", "build", "install", ""}
	for _, program := range programs {
		for _, subcommand := range subcommands[:4+len(program.name)%5] {
			operations = append(operations, operation{"Bash", "command", "", program.name, subcommand, program.category})
		}
	}
	for _, tool := range []struct{ name, category string }{{"Read", "read"}, {"Edit", "edit"}, {"Write", "edit"}, {"Grep", "search"}, {"Glob", "search"}, {"WebFetch", "web"}, {"WebSearch", "web"}, {"Task", "agent"}, {"TodoWrite", "plan"}} {
		operations = append(operations, operation{tool.name, tool.category, "", "", "", ""})
	}
	for s := range 8 {
		for _, method := range []string{"search", "query", "get", "list"} {
			operations = append(operations, operation{fmt.Sprintf("mcp__server%d__%s", s, method), "mcp", fmt.Sprintf("server%d", s), "", "", ""})
		}
	}
	// Most calls are a few common operations in a few busy repositories.
	random.Shuffle(len(operations), func(i, j int) { operations[i], operations[j] = operations[j], operations[i] })
	pickRepository, pickOperation := zipf(repositories), zipf(len(operations))
	insert, err := tx.Prepare(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,model,effort,kind,tool_name,tool_category,
		mcp_server,command,program,subcommand,command_category,started_at,duration_ms,duration_source,status,error_type,result_tokens,output_tokens)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer insert.Close()
	for index := range calls {
		r, w := pickRepository(), random.Intn(workspacesPerRepository)
		id := fmt.Sprintf("ws-%d-%d", r, w)
		op := operations[pickOperation()]
		started := now.Add(-time.Duration(random.Int63n(int64(120 * 24 * time.Hour))))
		var duration any = int64(math.Exp(random.NormFloat64()*1.5 + 7))
		if random.Intn(10) == 0 {
			duration = nil
		}
		status, errorType := "ok", any(nil)
		if roll := random.Intn(100); roll < 7 {
			status, errorType = "error", []string{"nonzero_exit", "timeout", "user_rejected"}[roll%3]
		} else if roll < 9 {
			status = "no_result"
		}
		command := any(nil)
		if op.program != "" {
			command = strings.TrimSpace(op.program + " " + op.subcommand)
		}
		if _, err := insert.Exec(fmt.Sprint("call-", index), id, "conv-"+id, "session-"+id, index, []string{"claude", "codex"}[w%2],
			[]string{"claude-opus-5-5", "gpt-6-sol", "claude-sonnet-5", "claude-haiku-4-5"}[w%4], []string{"high", "xhigh", "low", ""}[random.Intn(4)], "tool_call",
			op.tool, op.category, nilIfEmpty(op.server), command, nilIfEmpty(op.program), nilIfEmpty(op.subcommand), nilIfEmpty(op.commandCategory),
			formatTime(started), duration, "reported", status, errorType, random.Intn(4000), random.Float64()*300); err != nil {
			t.Fatal(err)
		}
	}
	// Hourly usage: one busy session per repository every hour, and a second
	// model in each repository a third of the hours.
	usage, err := tx.Prepare(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,input_tokens,uncached_input_tokens,cache_read_input_tokens,
		cache_creation_input_tokens,output_tokens,total_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	first := now.Truncate(time.Hour).Add(-90 * 24 * time.Hour)
	for r := range 10 {
		for hour := first; hour.Before(now); hour = hour.Add(time.Hour) {
			for w := range 2 {
				if w == 1 && random.Intn(3) != 0 {
					continue
				}
				uncached, read, write, output := int64(random.Intn(20000)), int64(random.Intn(200000)), int64(random.Intn(10000)), int64(random.Intn(5000))
				if _, err := usage.Exec(fmt.Sprintf("session-ws-%d-%d", r, w), hour.UTC().Format("2006-01-02T15:04:05.000Z"), []string{"claude-opus-5-5", "gpt-6-sol", "claude-sonnet-5", "claude-haiku-4-5"}[(r+w)%4],
					[]string{"high", "xhigh"}[w], "request", uncached+read+write, uncached, read, write, output, uncached+read+write+output); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	commitSeed(t, tx)
	// User messages for Human Words, ingested as a source would write them.
	ingest := seedTx(t, catalog.DB)
	texts := []string{
		"Please fix the failing test in the parser and keep the error message.",
		"```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
		"Continue.",
		"Run the full suite again and summarize what changed since the last run, with the numbers.",
		"<command-name>/review</command-name>",
		"Error: exit status 1\n    at runTests (/repo/test.js:10:3)\n    at main (/repo/index.js:4:1)",
	}
	for workspace := range 150 {
		messages := []MessageRecord{}
		for index := range 140 {
			at := now.Add(-time.Duration(random.Int63n(int64(90 * 24 * time.Hour))))
			messages = append(messages, MessageRecord{NativeID: fmt.Sprintf("m-%d-%d", workspace, index), Role: "user", Kind: "message", Text: texts[random.Intn(len(texts))] + fmt.Sprint(" ", index), CreatedAt: formatTime(at), Selected: true})
		}
		record := WorkspaceRecord{SourceID: fmt.Sprint("writing-", workspace), SourceKind: "codex", Account: "local", Title: fmt.Sprint("Writing ", workspace),
			Conversations: []ConversationRecord{{NativeID: fmt.Sprint("writing-", workspace), Provider: "codex", Account: "local", Coverage: "complete", Messages: messages}}}
		if _, _, err := ingestWorkspace(ingest, record, false); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, ingest)
}
