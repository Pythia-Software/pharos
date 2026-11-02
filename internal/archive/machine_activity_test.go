package archive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

// Exercise the exact metric and source-call shapes used by Machine Time.
// In particular, a count(duration_ms) must not count missing timing, the
// accelerated cube must match the generic executor over raw calls, and the
// source-call projection must contain only fields exposed by the query schema.
func TestMachineTimeQueries(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 80)
	ctx := context.Background()
	if err := catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	v2Schema := PharosToolCallsSchema()
	generic := queryV2Dataset("tool_calls", v2Schema)
	generic.SourceSQL = toolCallDataset.v2Source(v2Schema)
	for _, groups := range [][]string{
		{"repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server"},
		{"repository_name", "activity", "month", "status", "operation_key", "operation_name", "tool_name", "mcp_server"},
	} {
		q := metricRequest(qt.AggSpec{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: groups}, qt.AggSpec{ID: "timed", Op: "count", Field: "duration_ms", GroupBy: groups}, qt.AggSpec{ID: "population", Op: "count"})
		result, err := server.executeQueryV2(ctx, "tool_calls", nil, v2Schema, nil, &q)
		if err != nil {
			t.Fatalf("grouping %v: %v", groups, err)
		}
		metrics := result.Metrics.Metrics
		if len(metrics) != 3 || len(metrics[0].Buckets) == 0 {
			t.Fatalf("grouping %v returned no time metrics", groups)
		}
		var calls, timed int64
		for _, bucket := range metrics[0].Buckets {
			calls += bucket.Count
		}
		for _, bucket := range metrics[1].Buckets {
			timed += int64(bucket.Value.(float64))
		}
		if calls != 80 || timed <= 0 || timed >= calls || metrics[2].Buckets[0].Value != float64(80) {
			t.Fatalf("grouping %v: %d timed of %d calls; missing durations must remain separate", groups, timed, calls)
		}
		want, err := generic.ExecuteV2(ctx, catalog.reader(), nil, &q, qt.PlanOptions{})
		if err != nil || !same(result.SQLiteV2ExecutionResult, want) {
			t.Fatalf("operation metrics must match raw calls for %v: %v", groups, err)
		}
	}
	// An OR of AND branches over dimensions, and minimums the cubes cannot
	// count, answer as the raw calls do.
	pairs := []qt.WhereTerm{{Any: []qt.WhereClause{{All: []qt.WhereClause{{Field: "provider", Op: "=", Value: "codex"}, {Field: "tool_name", Op: "=", Value: "Bash"}}}, {Field: "tool_name", Op: "=", Value: "Read"}}}}
	byTool := []string{"tool_name", "provider"}
	for _, spec := range []qt.AggSpec{
		{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: byTool},
		{ID: "distinct", Op: "sum", Field: "duration_ms", GroupBy: byTool, MinCount: qt.MetricMinCount{Min: 2, Distinct: "conversation_id"}},
		{ID: "summed", Op: "count", GroupBy: byTool, MinCount: qt.MetricMinCount{Min: 5000, Sum: "duration_ms"}},
	} {
		for _, where := range [][]qt.WhereTerm{nil, pairs} {
			q := metricRequest(spec)
			q.Where = where
			result, err := server.executeQueryV2(ctx, "tool_calls", nil, v2Schema, nil, &q)
			want, wantErr := generic.ExecuteV2(ctx, catalog.reader(), nil, &q, qt.PlanOptions{})
			if err != nil || wantErr != nil || !same(result.SQLiteV2ExecutionResult, want) {
				t.Fatalf("%s over %+v must match raw calls: %v %v", spec.ID, where, err, wantErr)
			}
		}
	}
	schema, err := server.queryTableSchema("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	count := metricRequest(qt.AggSpec{ID: "n", Op: "count"})
	count.Where = pairs
	counted, err := generic.ExecuteV2(ctx, catalog.reader(), nil, &count, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paired, err := toolCallDataset.Rows(ctx, catalog.DB, querytable.Query{Select: []string{"id"}, Where: pairs, Limit: 1000}, schema)
	if err != nil || float64(paired.Total) != counted.Metrics.Metrics[0].Buckets[0].Value {
		t.Fatalf("source rows for OR-of-AND pairs: %d, adapter %v (%v)", paired.Total, counted.Metrics.Metrics[0].Buckets[0].Value, err)
	}
	rows, err := toolCallDataset.Rows(ctx, catalog.DB, querytable.Query{
		Select:  []string{"id", "operation_key", "operation_name", "activity", "command", "tool_name", "duration_ms", "title", "workspace_id", "conversation_id", "status"},
		OrderBy: []querytable.OrderBy{{Field: "duration_ms", Dir: "desc"}},
		Limit:   8,
	}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Rows) != 8 {
		t.Fatalf("source calls: got %d rows, want 8", len(rows.Rows))
	}
}

func TestMachineOperationFamilies(t *testing.T) {
	family := func(command string) string {
		primary := parseShellCommand(command).primary()
		return machineOperationKey("exec_command", "", primary.Program, primary.Subcommand)
	}
	if family("cd /tmp/work && CI=1 timeout 30 go test ./a -run TestUnit") != family("go test ./b -race") {
		t.Fatal("wrappers and targets should not fragment the parsed go test family")
	}
	if family("npm run test:unit") == family("npm run test:integration") || family("python3 -m pytest tests") == family("python3 -m unittest tests") {
		t.Fatal("named package scripts and Python modules must remain distinct")
	}
	if machineOperationKey("search", "github", "", "") == machineOperationKey("search", "linear", "", "") {
		t.Fatal("different MCP servers must retain separate tool identities")
	}
	if machineActivity("mcp", "", "search", "github", "") != "tool:github / search" || machineActivity("mcp", "", "query", "pharos", "") != "tool:pharos / query" {
		t.Fatal("unclassified tools must remain individually named")
	}
}

// Machine Time's cubes keep every group past the generic executor's budget,
// up to the accelerator's own; past that the metric fails, not truncates. The
// page asks for a long period as two cubes, operations without days and days
// without operations, so a budget that crossing them would exceed still
// answers it.
func TestMachineTimeMetricsBeyondTheGenericGroupBudget(t *testing.T) {
	override(t, &queryV2MaxGroups, 5)
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 60)
	if err := catalog.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	metrics := func(where []qt.WhereTerm, specs ...qt.AggSpec) []qt.SQLiteMetricV2 {
		t.Helper()
		q := metricRequest(append(specs, qt.AggSpec{ID: "calls", Op: "count"})...)
		q.Where = where
		response := apiCall(t, server, config.APIToken, "POST", "/api/query/tool_calls/metrics", q)
		var result qt.SQLiteMetricsV2Result
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Metrics) != len(specs)+1 || result.Metrics[len(specs)].Error != "" {
			t.Fatal(response.Code, response.Body.String())
		}
		return result.Metrics[:len(specs)]
	}
	calls := func(metric qt.SQLiteMetricV2) (total int64) {
		for _, bucket := range metric.Buckets {
			total += bucket.Count
		}
		return total
	}
	spent := func(groups ...string) qt.AggSpec {
		return qt.AggSpec{ID: strings.Join(groups, "/"), Op: "sum", Field: "duration_ms", GroupBy: groups}
	}
	crossed := spent("repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server")
	cube := metrics(nil, crossed)[0]
	if cube.Error != "" || len(cube.Buckets) <= queryV2MaxGroups || cube.GroupCount != int64(len(cube.Buckets)) || calls(cube) != 60 || cube.ProcessedRows != 60 {
		t.Fatalf("accelerated cube stopped at the generic budget: %d groups, %d calls, %q", len(cube.Buckets), calls(cube), cube.Error)
	}
	// A shape the accelerator declines keeps the generic budget.
	limited := crossed
	limited.GroupLimit = 1000
	if generic := metrics(nil, limited)[0]; !strings.Contains(generic.Error, "group population exceeds host budget") {
		t.Fatalf("generic metric past its budget: %+v", generic)
	}
	override(t, &machineTimeMaxGroups, len(cube.Buckets)-1)
	if over := metrics(nil, crossed)[0]; !strings.Contains(over.Error, "group population exceeds the "+thousands(machineTimeMaxGroups)+"-group host budget") || len(over.Buckets) != 0 {
		t.Fatalf("accelerated cube past its budget: %+v", over)
	}
	midnight := func(month time.Month, day int) string {
		return time.Date(2026, month, day, 0, 0, 0, 0, time.Local).Format(time.RFC3339)
	}
	long := []qt.WhereTerm{{Field: "started_at", Op: ">=", Value: midnight(6, 1)}, {Field: "started_at", Op: "<", Value: midnight(9, 30)}}
	for _, metric := range metrics(long, spent("repository_name", "activity", "status", "operation_key", "operation_name", "tool_name", "mcp_server"), spent("day", "status")) {
		if metric.Error != "" || calls(metric) != 60 || metric.Coverage != "exact" {
			t.Fatalf("long period past a budget its crossed cube exceeds: %+v", metric)
		}
	}
}
