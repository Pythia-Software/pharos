package archive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
	schema, err := server.queryTableSchema("tool_calls")
	if err != nil {
		t.Fatal(err)
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
// up to the accelerator's own; past that the metric fails, not truncates.
func TestMachineTimeMetricsBeyondTheGenericGroupBudget(t *testing.T) {
	override(t, &maxSQLBuckets, 5)
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 60)
	server := testServer(t, config, catalog)
	groups := []string{"repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}
	metrics := func(spent qt.AggSpec) qt.SQLiteMetricsV2Result {
		t.Helper()
		response := apiCall(t, server, config.APIToken, "POST", "/api/query/tool_calls/metrics", metricRequest(spent, qt.AggSpec{ID: "calls", Op: "count"}))
		var result qt.SQLiteMetricsV2Result
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Metrics) != 2 || result.Metrics[1].Error != "" {
			t.Fatal(response.Code, response.Body.String())
		}
		return result
	}
	spent := qt.AggSpec{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: groups}
	cube := metrics(spent).Metrics[0]
	var calls int64
	for _, bucket := range cube.Buckets {
		calls += bucket.Count
	}
	if cube.Error != "" || len(cube.Buckets) <= maxSQLBuckets || cube.GroupCount != int64(len(cube.Buckets)) || calls != 60 || cube.ProcessedRows != 60 {
		t.Fatalf("accelerated cube stopped at the generic budget: %d groups, %d calls, %q", len(cube.Buckets), calls, cube.Error)
	}
	// A shape the accelerator declines keeps the generic budget.
	limited := spent
	limited.GroupLimit = 1000
	if generic := metrics(limited).Metrics[0]; !strings.Contains(generic.Error, "group population exceeds host budget") {
		t.Fatalf("generic metric past its budget: %+v", generic)
	}
	override(t, &machineTimeMaxGroups, len(cube.Buckets)-1)
	if over := metrics(spent).Metrics[0]; !strings.Contains(over.Error, "group population exceeds the "+thousands(machineTimeMaxGroups)+"-group host budget") || len(over.Buckets) != 0 {
		t.Fatalf("accelerated cube past its budget: %+v", over)
	}
}

func TestMachineTimeCompleteAggregationBeyondPreviewLimit(t *testing.T) {
	override(t, &maxSQLBuckets, 20)
	calls := maxSQLBuckets + 100
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, calls)
	document, err := querySchemaDocument("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	request := querytable.AggregationRequest{Aggregations: []querytable.Aggregation{{ID: "calls", Op: "count", GroupBy: []string{"id"}}}}
	preview, err := toolCallDataset.Aggregate(context.Background(), catalog.DB, request, schema)
	if err != nil || len(preview.Metrics[0].Buckets) != maxSQLBuckets {
		t.Fatalf("ordinary metric preview: %v", err)
	}
	request.RequireComplete = true
	complete, err := toolCallDataset.Aggregate(context.Background(), catalog.DB, request, schema)
	if err != nil || len(complete.Metrics[0].Buckets) != calls {
		t.Fatalf("complete analytical read must not silently stop at preview size: %v", err)
	}
	payload, _ := json.Marshal(request)
	response := queryExplorationCall(testServer(t, config, catalog), config.APIToken, "POST", "/api/query/tool_calls/aggregations", string(payload))
	if response.Code != 200 {
		t.Fatalf("complete aggregation HTTP response: %d %s", response.Code, response.Body.String())
	}
	var received querytable.AggregationResult
	if err := json.Unmarshal(response.Body.Bytes(), &received); err != nil || len(received.Metrics[0].Buckets) != calls {
		t.Fatalf("HTTP must preserve the complete-read request: %v", err)
	}
}
