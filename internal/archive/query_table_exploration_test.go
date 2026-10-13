package archive

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestQueryExplorationHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 31)
	server := NewServer(config, catalog)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return queryExplorationCall(server, config.APIToken, method, path, body)
	}
	for _, dataset := range []string{"library", "activity", "usage", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts"} {
		t.Run(dataset, func(t *testing.T) {
			w := call("POST", "/api/query/"+dataset+"/population", `{"where":[]}`)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var result struct {
				Rows     []map[string]any `json:"rows"`
				Total    int              `json:"total"`
				Complete bool             `json:"complete"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Complete || len(result.Rows) != result.Total {
				t.Fatalf("incomplete: %s %v", w.Body.String(), err)
			}
			if len(result.Rows) > 0 {
				id := result.Rows[0]["id"]
				item := call("GET", fmt.Sprintf("/api/query/%s/item?id=%v&find=absent", dataset, id), "")
				if item.Code != 200 || strings.Contains(item.Body.String(), `"row":null`) {
					t.Fatal(item.Code, item.Body.String())
				}
			}
		})
	}
	for _, body := range []string{`null`, `[]`, `{"where":[]} {}`, `{"where":[],"limit":1}`, `{"where":[{"field":"not_a_field","op":"=","value":"x"}]}`} {
		if w := call("POST", "/api/query/tool_calls/population", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/query/tool_calls/item?id=missing", "/api/query/library/item?id=missing"} {
		if w := call("GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"row":null`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := queryExplorationCall(server, "", "POST", "/api/query/tool_calls/population", `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestQueryExplorationNeverReturnsAPage(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, queryExplorationRows+1)
	server := NewServer(config, catalog)
	w := queryExplorationCall(server, config.APIToken, "POST", "/api/query/tool_calls/population", `{"where":[]}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Narrow") || strings.Contains(w.Body.String(), `"rows"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = queryExplorationCall(server, config.APIToken, "POST", "/api/query/tool_calls/population", `{"where":[{"field":"tool_name","op":"=","value":"shell_command"}]}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestQuerySnapshotsPharosHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
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
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
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

func TestQueryObjectReferencesHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 4)
	server := NewServer(config, catalog)
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
