package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func queryExplorationCall(server *Server, token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

var queryDatasets = []string{"library", "activity", "usage", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts", "findings", "parallelism"}

// TestQueryDatasetsHTTP calls each query endpoint for every dataset, on one
// catalog: capabilities and V3 budgets, typed rows, an item, metrics over
// every numeric field, and a grouped V3 query. Then it checks the endpoints
// that are not per dataset: retired population downloads, missing items,
// and computed columns.
func TestQueryDatasetsHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 31)
	ingestUsageFixture(t, catalog)
	server := testServer(t, config, catalog)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		return apiCall(t, server, config.APIToken, method, path, body)
	}
	for _, dataset := range queryDatasets {
		t.Run(dataset, func(t *testing.T) {
			schema, err := sqliteQuerySchema(dataset)
			if err != nil {
				t.Fatal(err)
			}
			w := call(http.MethodGet, "/api/query/"+dataset+"/capabilities", nil)
			var handshake struct {
				MetricCapabilities qt.SQLiteMetricCapabilities `json:"metricCapabilities"`
				QueryCapabilities  qt.QueryCapabilitiesV3      `json:"queryCapabilities"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &handshake) != nil || !handshake.QueryCapabilities.GroupedRows || !handshake.QueryCapabilities.CalendarGrouping {
				t.Fatal("capabilities", w.Code, w.Body.String())
			}
			limits := handshake.QueryCapabilities
			groups := 10_000
			if dataset == "usage" {
				groups = 100_000
			}
			if limits.MaxRows != 10_000 || limits.MaxGroups != groups || limits.MaxPopulation != 10_000_000 {
				t.Fatalf("incorrect advertised V3 budgets: %+v", limits)
			}
			// Usage charts its compact facts by period and split with V2 metrics.
			if groups = 2_000; dataset == "usage" {
				groups = 100_000
			}
			if handshake.MetricCapabilities.Version != 2 || handshake.MetricCapabilities.MaxGroups != groups {
				t.Fatalf("incorrect advertised V2 budgets: %+v", handshake.MetricCapabilities)
			}

			w = call(http.MethodPost, "/api/query/"+dataset+"/rows-v2", `{"version":2,"profile":"qt-sqlite-v1","select":["id"],"limit":1}`)
			var page struct {
				Rows  []map[string]any `json:"rows"`
				Total int              `json:"total"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Rows) > 1 {
				t.Fatal("rows", w.Code, w.Body.String())
			}
			if len(page.Rows) > 0 {
				item := call(http.MethodGet, fmt.Sprintf("/api/query/%s/item?id=%s&find=absent", dataset, url.QueryEscape(fmt.Sprint(page.Rows[0]["id"]))), nil)
				if item.Code != 200 || strings.Contains(item.Body.String(), `"row":null`) {
					t.Fatal("item", item.Code, item.Body.String())
				}
			}

			selection := []string{}
			specs := []qt.AggSpec{{ID: "n", Expression: "COUNT()"}}
			for name, field := range schema.Fields {
				if field.Expr != "" {
					selection = append(selection, name)
				}
				if field.Kind == qt.FieldNumber && field.Expr != "" && (field.AggregateOps == nil || len(field.AggregateOps) > 0) {
					specs = append(specs, qt.AggSpec{ID: name, Expression: "SUM([" + name + "])"})
				}
			}
			w = call(http.MethodPost, "/api/query/"+dataset+"/rows-v2", qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: selection, Limit: 10}})
			if w.Code != 200 {
				t.Fatal("typed rows", w.Code, w.Body.String())
			}
			for start := 0; start < len(specs); start += 20 {
				if w = call(http.MethodPost, "/api/query/"+dataset+"/metrics", metricRequest(specs[start:min(start+20, len(specs))]...)); w.Code != 200 {
					t.Fatal("metrics", w.Code, w.Body.String())
				}
			}

			// Every dataset supports a non-item group identity and ordinary summaries.
			w = call(http.MethodPost, "/api/query/"+dataset+"/query-v3", groupRequest(schema.IDField, qt.SummaryColumn{ID: "records", Op: "count"}))
			var result qt.QueryResultV3
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Version != 3 || result.Schema.IDField != qt.GroupIdentityField {
				t.Fatal("query-v3", w.Code, w.Body.String())
			}
			for _, row := range result.Rows {
				if row[qt.GroupIdentityField] == nil || row["records"] != float64(1) {
					t.Fatal(row)
				}
			}
		})
	}

	// Complete-population downloads are gone, whatever the dataset or body.
	if w := call(http.MethodPost, "/api/query/tool_calls/population", `{"where":[]}`); w.Code != http.StatusGone || strings.Contains(w.Body.String(), `"rows"`) || !strings.Contains(w.Body.String(), "server query execution") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiCall(t, server, "", http.MethodPost, "/api/query/tool_calls/population", `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	// So is the legacy grouped route: a page from before an upgrade is told to
	// reload, for SQL and in-memory datasets alike, before any body is read.
	for _, dataset := range []string{"tool_calls", "writing_messages", "usage", "library"} {
		if w := call(http.MethodPost, "/api/query/"+dataset+"/aggregations", `not json`); w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "reload the page") {
			t.Fatal(dataset, w.Code, w.Body.String())
		}
	}
	if w := apiCall(t, server, "", http.MethodPost, "/api/query/usage/aggregations", `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/api/query/tool_calls/item?id=missing", "/api/query/library/item?id=missing"} {
		if w := call(http.MethodGet, path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"row":null`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}

	body := `{"column":{"id":"duration_s","label":"Seconds","expression":{"language":"qt-expr","version":1,"source":"[duration_ms]/1000"}},"expectedRevision":null}`
	w := call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var column qt.ComputedColumn
	if err := json.Unmarshal(w.Body.Bytes(), &column); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", body); w.Code != 409 {
		t.Fatal("create conflict", w.Code)
	}
	w = call(http.MethodPost, "/api/query/tool_calls/rows-v2", `{"version":2,"profile":"qt-sqlite-v1","expectedRevisions":{"duration_s":"`+column.Revision+`"},"select":["id","@computed/duration_s"],"orderBy":[{"field":"@computed/duration_s","dir":"desc"}],"limit":5}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, formula := range []string{"SUM([duration_ms])", "[missing]", "[@computed/bad]", "[duration_ms]+"} {
		invalid := qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: "bad", Label: "Bad", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: formula}}}
		if w = call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", invalid); w.Code != 400 {
			t.Fatal("invalid definition persisted", formula, w.Code, w.Body.String())
		}
	}
	if w = call(http.MethodGet, "/api/query/tool_calls/capabilities", nil); w.Code != 200 {
		t.Fatal("catalogue could not load after rejected edit", w.Body.String())
	}
	for _, path := range []string{"/api/query/tool_calls/metrics", "/api/query/tool_calls/rows-v2", "/api/query/tool_calls"} {
		if w = call(http.MethodPost, path, `{"version":2,"limit":1,"metrics":[]} {}`); w.Code != 400 {
			t.Fatal("bad envelope accepted", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+config.APIToken)
	r.Header.Set("Origin", "https://other.example")
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin write accepted", w.Code)
	}
}

func TestQuerySnapshotsPharosHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	code := "abcdefghijklmnopqrstuv"
	token := "2j." + base64.RawURLEncoding.EncodeToString([]byte(`{"s":[["tool_name"]]}`))
	path := "/api/query-snapshots?dataset=pharos_tool_calls&code=" + code
	body := fmt.Sprintf(`{"version":1,"code":%q,"token":%q,"createdAt":0}`, code, token)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return queryExplorationCall(server, config.APIToken, method, path, body)
	}
	for i := 0; i < 2; i++ {
		if w := call("PUT", path, body); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call("GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), token) {
		t.Fatal(w.Code, w.Body.String())
	}
	other := strings.Replace(body, token, "2j."+base64.RawURLEncoding.EncodeToString([]byte(`{"l":1}`)), 1)
	if w := call("PUT", path, other); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", strings.Replace(path, "pharos_tool_calls", "pharos_library", 1), ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", strings.Replace(path, "pharos_tool_calls", "tool_calls", 1), ""); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := queryExplorationCall(server, "", "GET", path, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("PUT", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+config.APIToken)
	r.Header.Set("Origin", "http://evil.test")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
	for _, bad := range []string{body + ` {}`, strings.Replace(body, token, "invalid", 1)} {
		if w := call("PUT", path, bad); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestQuerySnapshotQuotaPreservesExistingLinks(t *testing.T) {
	override(t, &querySnapshotCount, 5)
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	_, err := catalog.DB.Exec(`WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM seq WHERE n+1<?) INSERT INTO query_table_query_snapshots SELECT 'local','pharos_tool_calls',printf('%022d',n),'2j.e30',1 FROM seq`, querySnapshotCount)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/query-snapshots?dataset=pharos_tool_calls&code=aaaaaaaaaaaaaaaaaaaaaa"
	body := `{"version":1,"code":"aaaaaaaaaaaaaaaaaaaaaa","token":"2j.e30","createdAt":0}`
	w := queryExplorationCall(server, config.APIToken, "PUT", path, body)
	if w.Code != 507 {
		t.Fatal(w.Code, w.Body.String())
	}
	old := "0000000000000000000000"
	w = queryExplorationCall(server, config.APIToken, "GET", strings.Replace(path, "aaaaaaaaaaaaaaaaaaaaaa", old, 1), "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = queryExplorationCall(server, config.APIToken, "PUT", strings.Replace(path, "aaaaaaaaaaaaaaaaaaaaaa", old, 1), strings.Replace(body, "aaaaaaaaaaaaaaaaaaaaaa", old, 1))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestQuerySnapshotDirectWritesValidateBeforeStorage(t *testing.T) {
	catalog, _ := testCatalog(t)
	store := pharosQuerySnapshotStore{Catalog: catalog}
	for _, snapshot := range []qt.QuerySnapshot{
		{Version: 2, Code: "aaaaaaaaaaaaaaaaaaaaaa", Token: "2j.e30"},
		{Version: 1, Code: "short", Token: "2j.e30"},
		{Version: 1, Code: "aaaaaaaaaaaaaaaaaaaaaa", Token: "2j.invalid!"},
		{Version: 1, Code: "aaaaaaaaaaaaaaaaaaaaaa", Token: "2j.bnVsbA"},
		{Version: 1, Code: "aaaaaaaaaaaaaaaaaaaaaa", Token: "2d.invalid!"},
	} {
		if _, err := store.Save(context.Background(), queryTableScope, "pharos_tool_calls", snapshot); err == nil {
			t.Fatalf("invalid direct snapshot committed: %+v", snapshot)
		}
	}
	var count int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM query_table_query_snapshots").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid snapshots reached storage", count, err)
	}
}

func TestQueryObjectReferencesHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 4)
	server := testServer(t, config, catalog)
	body := `{"references":[{"objectType":"pharos_library","key":{"id":"ws-0"}},{"objectType":"pharos_conversations","key":{"id":"conv-0"}},{"objectType":"pharos_library","key":{"id":"missing"}},{"objectType":"pharos_library","key":{"id":"ws-0"}}]}`
	w := queryExplorationCall(server, config.APIToken, "POST", "/api/query-object-references", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var refs []queryResolvedReference
	if err := json.Unmarshal(w.Body.Bytes(), &refs); err != nil || len(refs) != 3 || refs[0].Href != "/work/ws-0" || refs[1].Href != "/work/ws-0/conversation/conv-0" || refs[2].Href != "" {
		t.Fatal(w.Body.String(), err)
	}
	for _, body := range []string{`{"references":[{"objectType":"arbitrary_table","key":{"id":"a"}}]}`, `{"references":[{"objectType":"pharos_library","key":{"id":"x","other":"y"}}]}`, `{} {}`} {
		w = queryExplorationCall(server, config.APIToken, "POST", "/api/query-object-references", body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w = queryExplorationCall(server, "", "POST", "/api/query-object-references", body); w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestConversationLinkRedirectsToItsWork(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 4)
	server := testServer(t, config, catalog)
	w := queryExplorationCall(server, config.APIToken, "GET", "/conversation/conv-0?message=m-1", "")
	if w.Code != 302 || w.Header().Get("Location") != "/work/ws-0/conversation/conv-0?message=m-1" {
		t.Fatal(w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if w = queryExplorationCall(server, config.APIToken, "GET", "/conversation/missing", ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = queryExplorationCall(server, "", "GET", "/conversation/conv-0", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
