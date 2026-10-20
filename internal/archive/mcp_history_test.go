package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMCPRequestedArchiveSourceProjection(t *testing.T) {
	catalog, config := testCatalog(t)
	for name, args := range map[string]map[string]any{
		"claude": {"source": " claude ", "secret": "must not be logged"},
		"codex":  {"source": "codex"}, "blank": {"source": " "},
		"missing": {}, "number": {"source": 123}, "malformed": {},
	} {
		if err := catalog.RecordMCPCall(name, args, "ok", "", time.Millisecond, 10, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.DB.Exec(`UPDATE mcp_calls SET arguments_json='invalid' WHERE tool_name='malformed'`); err != nil {
		t.Fatal(err)
	}
	rows, err := catalog.mcpQueryRows(context.Background())
	if err != nil || len(rows) != 6 {
		t.Fatalf("projected rows: %d, %v", len(rows), err)
	}
	for _, row := range rows {
		name := firstString(row["tool_name"])
		if name == "claude" || name == "codex" {
			if row["query_source"] != name {
				t.Fatalf("explicit source %s: %#v", name, row["query_source"])
			}
		} else if row["query_source"] != nil {
			t.Fatalf("unscoped/invalid source %s became %#v", name, row["query_source"])
		}
		if strings.Contains(firstString(row["arguments_json"]), "must not be logged") {
			t.Fatal("source projection bypassed argument sanitization")
		}
	}
	server := testServer(t, config, catalog)
	request := httptest.NewRequest(http.MethodPost, "/api/query/mcp_calls", strings.NewReader(`{"select":["tool_name","query_source"],"where":[{"field":"query_source","op":"=","value":"claude"}],"limit":10}`))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var result struct {
		Total int              `json:"total"`
		Rows  []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Total != 1 || len(result.Rows) != 1 || result.Rows[0]["tool_name"] != "claude" {
		t.Fatalf("requested source filter: %d %s (%v)", response.Code, response.Body.String(), err)
	}
}

func TestMCPControlAndCallHistory(t *testing.T) {
	if !mcpResultTruncated(map[string]any{"items": []map[string]any{{"next_text_offset": 120}}}) {
		t.Fatal("message continuation was not classified as truncated output")
	}
	catalog, config := testCatalog(t)
	config.Executable = "/Applications/Pharos.app/Contents/MacOS/pharos"
	server := testServer(t, config, catalog)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	status := get("/api/mcp")
	if status.Code != 200 {
		t.Fatalf("MCP status: %d %s", status.Code, status.Body.String())
	}
	var settings map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["enabled"] != true || settings["transport"] != "stdio" || settings["command"] != config.Executable {
		t.Fatalf("unexpected MCP settings: %#v", settings)
	}

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		return handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args}})
	}
	call("search_conversations", map[string]any{"query": "parser", "limit": 2, "secret": "must not be logged"})
	if response := call("does_not_exist", map[string]any{}); response["result"].(map[string]any)["isError"] != true {
		t.Fatalf("unknown tool did not report error: %#v", response)
	}
	change := httptest.NewRequest(http.MethodPost, "/api/mcp/enabled", strings.NewReader(`{"enabled":false}`))
	change.Header.Set("Authorization", "Bearer test-token")
	change.Header.Set("Content-Type", "application/json")
	changed := httptest.NewRecorder()
	server.ServeHTTP(changed, change)
	if changed.Code != 200 {
		t.Fatalf("disable MCP: %d %s", changed.Code, changed.Body.String())
	}
	listed := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if len(listed["result"].(map[string]any)["tools"].([]any)) != 0 {
		t.Fatalf("disabled MCP still lists tools: %#v", listed)
	}
	if response := call("search_conversations", map[string]any{"query": "parser"}); response["result"].(map[string]any)["isError"] != true {
		t.Fatalf("disabled MCP accepted a call: %#v", response)
	}
	history := get("/api/mcp/calls?limit=10")
	if history.Code != 200 {
		t.Fatalf("MCP calls: %d %s", history.Code, history.Body.String())
	}
	if strings.Contains(history.Body.String(), "must not be logged") {
		t.Fatalf("history exposed unapproved arguments or response text: %s", history.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(history.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	items := data["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["status"] != "disabled" || data["stats"].(map[string]any)["failed_calls"] != float64(2) {
		t.Fatalf("unexpected call history: %#v", data)
	}
	if len(data["tool_stats"].([]any)) != 2 {
		t.Fatalf("missing per-tool history: %#v", data)
	}
	filtered := get("/api/mcp/calls?tool=search_conversations&status=disabled")
	var filteredData map[string]any
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredData); err != nil {
		t.Fatal(err)
	}
	if filteredData["filtered_total"] != float64(1) || len(filteredData["items"].([]any)) != 1 {
		t.Fatalf("MCP history filters did not narrow results: %#v", filteredData)
	}
	query := func(path, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("query %s: %d %s", path, response.Code, response.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	rows := query("/api/query/mcp_calls", `{"select":["id","tool_name","status","arguments_json","day","error_count","truncated"],"where":[{"field":"status","op":"=","value":"disabled"}],"orderBy":[{"field":"called_at","dir":"desc"}],"limit":10}`)
	if rows["total"] != float64(1) || len(rows["rows"].([]any)) != 1 {
		t.Fatalf("MCP query-table filter: %#v", rows)
	}
	row := rows["rows"].([]any)[0].(map[string]any)
	if row["error_count"] != float64(1) || row["truncated"] != false || row["day"] == nil {
		t.Fatalf("MCP query-table computed fields: %#v", row)
	}
	if strings.Contains(row["arguments_json"].(string), "must not be logged") {
		t.Fatal("MCP query exposed secret argument")
	}
	metrics := query("/api/query/mcp_calls/metrics", `{"version":2,"where":[],"orderBy":[],"limit":1,"offset":0,"metrics":[{"id":"calls_by_tool","op":"sum","field":"call_count","groupBy":["tool_name"]},{"id":"errors_by_tool","op":"sum","field":"error_count","groupBy":["tool_name"]}]}`)
	list, _ := metrics["metrics"].([]any)
	if len(list) != 2 {
		t.Fatalf("MCP query-table metrics: %#v", metrics)
	}
	for _, item := range list {
		if metric := item.(map[string]any); metric["error"] != nil || len(metric["buckets"].([]any)) == 0 {
			t.Fatalf("MCP query-table metric: %#v", metric)
		}
	}
	distinct := get("/api/query/mcp_calls/distinct?field=tool_name&q=search")
	if distinct.Code != 200 || !strings.Contains(distinct.Body.String(), "search_conversations") {
		t.Fatalf("MCP tool distinct values: %d %s", distinct.Code, distinct.Body.String())
	}
	if enabled, err := catalog.MCPEnabled(); err != nil || enabled {
		t.Fatalf("MCP toggle was not persisted: enabled=%v error=%v", enabled, err)
	}
}
