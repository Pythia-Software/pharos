package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLibraryQueryTableCombinesKeywordSearchWithFilters(t *testing.T) {
	catalog, config := libraryFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := NewServer(config, catalog)
	request := func(method, path, body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	page := request(http.MethodPost, "/api/query/library?find=parser", `{"select":["title"],"limit":10,"offset":0}`)
	rows := page["rows"].([]any)
	if page["total"] != float64(1) || len(rows) != 1 {
		t.Fatalf("keyword search page: %#v", page)
	}
	row := rows[0].(map[string]any)
	if row["id"] != "c" || row["find_match_count"] != float64(1) || row["find_hit"].(map[string]any)["message_id"] != "m1" {
		t.Fatalf("keyword match not attached: %#v", row)
	}
	if find := page["find"].(map[string]any); find["workspaces"] != float64(1) || find["limited"] != false {
		t.Fatalf("keyword summary: %#v", find)
	}
	v2 := request(http.MethodPost, "/api/query/library/rows-v2?find=parser", `{"version":2,"profile":"qt-sqlite-v1","select":["title"],"limit":10}`)
	v2Row := v2["rows"].([]any)[0].(map[string]any)
	if v2["total"] != page["total"] || v2Row["id"] != row["id"] || !same(v2Row["find_hit"], row["find_hit"]) || v2Row["find_match_count"] != row["find_match_count"] || !same(v2["find"], page["find"]) {
		t.Fatalf("v2 search evidence differs: %#v", v2)
	}
	// Filters narrow the keyword matches further.
	if page := request(http.MethodPost, "/api/query/library?find=parser", `{"select":["title"],"where":[{"field":"source_kind","op":"=","value":"codex"}],"limit":10,"offset":0}`); page["total"] != float64(0) {
		t.Fatalf("filter ignored with keyword search: %#v", page)
	}
	if page := request(http.MethodPost, "/api/query/library?find=README&find_kind=file", `{"select":["title"],"where":[{"field":"title","op":"=","value":"Charlie"}],"limit":10,"offset":0}`); page["total"] != float64(1) {
		t.Fatalf("file keyword search with filter: %#v", page)
	}
	if page := request(http.MethodPost, "/api/query/library?find=Parser&find_case=1&find_fuzzy=0", `{"select":["title"],"limit":10,"offset":0}`); page["total"] != float64(0) {
		t.Fatalf("case-sensitive keyword search: %#v", page)
	}
	// Without a keyword search the response keeps its plain shape.
	if page := request(http.MethodPost, "/api/query/library", `{"select":["title"],"limit":10,"offset":0}`); page["total"] != float64(2) || page["find"] != nil {
		t.Fatalf("unfiltered page: %#v", page)
	}
	metrics := request(http.MethodPost, "/api/query/library/aggregations?find=parser", `{"aggregations":[{"id":"n","op":"count","groupBy":[]}]}`)
	if encoded, _ := json.Marshal(metrics); !strings.Contains(string(encoded), `"value":1`) {
		t.Fatalf("metrics ignore the keyword search: %s", encoded)
	}
	distinct := request(http.MethodGet, "/api/query/library/distinct?field=title&q=&find=parser", "")
	if encoded, _ := json.Marshal(distinct); strings.Contains(string(encoded), "Bravo") || !strings.Contains(string(encoded), "Charlie") {
		t.Fatalf("distinct values ignore the keyword search: %s", encoded)
	}
}
