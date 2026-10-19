package archive

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

// Exercise the exact aggregate and source-call shapes used by Machine Time.
// In particular, a count(duration_ms) must not count missing timing, and the
// source-call projection must contain only fields exposed by the query schema.
func TestMachineTimeQueries(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedToolCalls(t, catalog, 80)
	ctx := context.Background()
	if err := catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	document, err := querySchemaDocument("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, groups := range [][]string{
		{"repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server"},
		{"repository_name", "activity", "month", "status", "operation_key", "operation_name", "tool_name", "mcp_server"},
	} {
		result, err := toolCallDataset.Aggregate(ctx, catalog.DB, querytable.AggregationRequest{
			Aggregations: []querytable.Aggregation{
				{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: groups},
				{ID: "timed", Op: "count", Field: "duration_ms", GroupBy: groups},
			},
		}, schema)
		if err != nil {
			t.Fatalf("grouping %v: %v", groups, err)
		}
		if len(result.Metrics) != 2 || len(result.Metrics[0].Buckets) == 0 {
			t.Fatalf("grouping %v returned no time aggregates", groups)
		}
		var calls, timed int64
		for _, bucket := range result.Metrics[0].Buckets {
			calls += int64(bucket.Count)
		}
		for _, bucket := range result.Metrics[1].Buckets {
			timed += integer(bucket.Value)
		}
		if calls != 80 || timed <= 0 || timed >= calls {
			t.Fatalf("grouping %v: %d timed of %d calls; missing durations must remain separate", groups, timed, calls)
		}
		direct := toolCallDataset
		direct.cube, direct.countFrom, direct.buckets = nil, "", nil
		directResult, err := direct.Aggregate(ctx, catalog.DB, querytable.AggregationRequest{Aggregations: []querytable.Aggregation{
			{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: groups},
			{ID: "timed", Op: "count", Field: "duration_ms", GroupBy: groups},
		}}, schema)
		if err != nil || !reflect.DeepEqual(result, directResult) {
			t.Fatalf("operation aggregates must match raw calls for %v: %v", groups, err)
		}
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
