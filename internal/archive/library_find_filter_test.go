package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func TestLibraryQueryTableCombinesKeywordSearchWithFilters(t *testing.T) {
	catalog, config := libraryFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshAllLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
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
	metrics := request(http.MethodPost, "/api/query/library/metrics?find=parser", `{"version":2,"where":[],"orderBy":[],"limit":1,"offset":0,"metrics":[{"id":"n","op":"count","groupBy":[]}]}`)
	if encoded, _ := json.Marshal(metrics["metrics"]); !strings.Contains(string(encoded), `"value":1`) || metrics["find"] == nil {
		t.Fatalf("metrics ignore the keyword search: %s", encoded)
	}
	distinct := request(http.MethodGet, "/api/query/library/distinct?field=title&q=&find=parser", "")
	if encoded, _ := json.Marshal(distinct); strings.Contains(string(encoded), "Bravo") || !strings.Contains(string(encoded), "Charlie") {
		t.Fatalf("distinct values ignore the keyword search: %s", encoded)
	}
}

func TestLibraryFindFiltersBeforeGlobalCandidateLimit(t *testing.T) {
	override(t, &libraryFindTextLimit, 20)
	catalog, config := libraryFixture(t)
	tx, e := catalog.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < libraryFindTextLimit+100; i++ {
		id := fmt.Sprintf("unrelated-%05d", i)
		if _, e = tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'cb',?,'user','message','parser common words',?)`, id, id, id); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = catalog.refreshAllLibrary(t.Context()); e != nil {
		t.Fatal(e)
	}
	server := testServer(t, config, catalog)
	for _, endpoint := range []string{"/api/query/library", "/api/query/library/rows-v2"} {
		body := `{"select":["title"],"where":[{"field":"title","op":"=","value":"Charlie"}],"limit":10}`
		if strings.Contains(endpoint, "v2") {
			body = `{"version":2,"profile":"qt-sqlite-v1",` + body[1:]
		}
		req := httptest.NewRequest(http.MethodPost, endpoint+"?find=parser&find_fuzzy=0", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var result map[string]any
		if e = json.Unmarshal(response.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if result["total"] != float64(1) || result["find"].(map[string]any)["limited"] != false {
			t.Fatalf("early scope failed: %s", response.Body.String())
		}
	}
}

func TestLibraryFindLimitAppliesToSortedWorkspaces(t *testing.T) {
	slowTest(t)
	override(t, &libraryFindTextLimit, 2)
	catalog, config := libraryFixture(t)
	execTx(t, catalog.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES
 ('b1','cb','b1','user','message','parser common words','b1'),
 ('b2','cb','b2','user','message','parser common words','b2'),
 ('b3','cb','b3','user','message','parser common words','b3')`,
		`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`)
	if err := catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	// Each search used to stop inside Bravo's newest messages, missing Charlie,
	// which sorts first. The message-page limit must not truncate this table.
	for _, search := range []string{"find=parser", "find=parser&find_case=1", "find=parser&find_mode=regex"} {
		params, _ := url.ParseQuery(search + "&find_fuzzy=0")
		for _, endpoint := range []string{"/api/query/library", "/api/query/library/rows-v2", "/api/query/library/query-v3"} {
			for offset, want := range []string{"c", "b"} {
				wire := qt.WireQuery{Select: []string{"title"}, OrderBy: qt.OrderBys{{Field: "title", Dir: "desc"}}, Limit: 1, Offset: offset}
				var body any = wire
				if strings.Contains(endpoint, "v2") {
					body = qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: wire}
				} else if strings.Contains(endpoint, "v3") {
					body = qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: wire}
				}
				response := apiCall(t, server, config.APIToken, http.MethodPost, endpoint+"?"+params.Encode(), body)
				var page struct {
					Rows  []map[string]any
					Total int
					Find  libraryFindSummary
				}
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Rows) != 1 || page.Rows[0]["id"] != want || page.Total != 2 {
					t.Fatalf("%s %s offset=%d: %d %s", endpoint, search, offset, response.Code, response.Body.String())
				}
				if page.Find.Limited || page.Find.Search == nil || !page.Find.Search.Complete || page.Find.Search.Count != 4 || page.Find.Search.NextCursor != "" {
					t.Fatalf("incomplete population: %+v", page.Find)
				}
				count := float64(1)
				if want == "b" {
					count = 3
				}
				if page.Rows[0]["find_match_count"] != count || page.Rows[0]["find_hit"].(map[string]any)["snippet"] == nil {
					t.Fatalf("missing page evidence: %#v", page.Rows[0])
				}
			}
		}
		metrics := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/metrics?"+params.Encode(), `{"version":2,"limit":1,"offset":1,"metrics":[{"id":"n","op":"count","groupBy":[]}]}`)
		if metrics.Code != 200 || !strings.Contains(metrics.Body.String(), `"value":2`) {
			t.Fatalf("metrics must count the full workspace population: %d %s", metrics.Code, metrics.Body.String())
		}
	}
	column, err := (qt.SQLiteComputedColumnStore{DB: catalog.DB}).Save(t.Context(), queryTableScope, "pharos_library", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
		ID: "title_copy", Label: "Title copy", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[title]"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{2, 3} {
		wire := qt.WireQuery{Select: []string{"title"}, OrderBy: qt.OrderBys{{Field: "@computed/title_copy", Dir: "desc"}}, Limit: 1}
		wantTotal := 2
		if version == 3 {
			wire.Where = []qt.WhereTerm{{Field: "@computed/title_copy", Op: "=", Value: "Charlie"}}
			wantTotal = 1
		}
		var body any = qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: wire, ExpectedRevisions: map[string]string{column.ID: column.Revision}}
		endpoint := "rows-v2"
		if version == 3 {
			body = qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: wire, ExpectedRevisions: map[string]string{column.ID: column.Revision}}
			endpoint = "query-v3"
		}
		response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/"+endpoint+"?find=parser&find_fuzzy=0", body)
		var page struct {
			Rows  []map[string]any
			Total int
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Rows) != 1 || page.Rows[0]["id"] != "c" || page.Total != wantTotal {
			t.Fatalf("computed filter/sort v%d: %d %s", version, response.Code, response.Body.String())
		}
	}
	grouped := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Grouping: &qt.GroupingDefinition{Keys: []string{"source_kind"}, Summaries: []qt.SummaryColumn{{ID: "records", Op: "count"}}},
		Result:   qt.WireQuery{Select: []string{"source_kind", "records"}, Limit: 1},
	}
	response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/query-v3?find=parser&find_fuzzy=0", grouped)
	var groups qt.QueryResultV3
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &groups) != nil || len(groups.Rows) != 1 || groups.Total != 2 || groups.SourceTotal != 2 {
		t.Fatalf("group Limit truncated source population: %d %s", response.Code, response.Body.String())
	}
}

func TestLibraryFindWithholdsUnfinishedWorkspacePopulation(t *testing.T) {
	override(t, &libraryFindWorkspaceTimeout, 50*time.Millisecond)
	catalog, config := libraryFixture(t)
	execTx(t, catalog.DB, `INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`)
	if err := catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	stalled := true
	hookTextSearch(t, catalog, func(ctx context.Context) error {
		if stalled {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, nil)
	server := testServer(t, config, catalog)
	params := url.Values{"find": {"parser"}, "find_fuzzy": {"0"}}
	for _, endpoint := range []string{"/api/query/library/rows-v2", "/api/query/library/metrics"} {
		body := `{"version":2,"profile":"qt-sqlite-v1","select":["title"],"limit":1}`
		if strings.HasSuffix(endpoint, "metrics") {
			body = `{"version":2,"metrics":[{"id":"n","op":"count","groupBy":[]}]}`
		}
		response := apiCall(t, server, config.APIToken, http.MethodPost, endpoint+"?"+params.Encode(), body)
		if response.Code == 200 || !strings.Contains(response.Body.String(), "search is incomplete") || strings.Contains(response.Body.String(), `"total"`) {
			t.Fatalf("unfinished search published a population: %d %s", response.Code, response.Body.String())
		}
	}
	stalled = false
	rows, set, err := server.libraryRows(t.Context(), params, libraryFieldsFor("title"), nil)
	if err != nil || len(rows) != 1 || !set.search.Complete {
		t.Fatalf("retry could not recover: %v %+v %v", rows, set, err)
	}
}

func TestLibraryFindCacheTracksSearchInputs(t *testing.T) {
	c := textFixture(t)
	calls := 0
	hookTextSearch(t, c, func(context.Context) error { calls++; return nil }, nil)
	options := LibraryFindOptions{Query: "cache"}
	first, err := c.libraryFindMatches(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	// Analysis/reindex projection commits advance the general cache version,
	// without changing the messages or scope of a text search.
	execTx(t, c.DB, `UPDATE query_cache_generation SET value=value+1 WHERE id=1`,
		`UPDATE message_authorship_spans SET rule='refreshed' WHERE category='typed'`)
	again, err := c.libraryFindMatches(t.Context(), options)
	if err != nil || again != first || calls != 1 {
		t.Fatalf("unrelated commit repeated search: calls=%d err=%v", calls, err)
	}
	if err := appendSearchMessage(c.DB); err != nil {
		t.Fatal(err)
	}
	appended, err := c.libraryFindMatches(t.Context(), options)
	if err != nil || appended == first || appended.search.Count != first.search.Count+1 || calls != 2 {
		t.Fatalf("append used stale batch: calls=%d err=%v", calls, err)
	}
	execTx(t, c.DB, `UPDATE messages SET text='unrelated' WHERE id='appended'`)
	changed, err := c.libraryFindMatches(t.Context(), options)
	if err != nil || changed == appended || calls != 3 {
		t.Fatalf("edit used stale batch: calls=%d err=%v", calls, err)
	}
	// Concurrent pages share the population, but localization may compile an
	// exclusion's pattern for the first time. Page evidence must stay independent.
	set, err := c.libraryFindMatches(t.Context(), LibraryFindOptions{Query: `"race condition" NOT missing`, WorkspaceRows: true})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 4)
	for range 4 {
		workers.Go(func() {
			<-start
			rows := []map[string]any{{"id": "w"}}
			if err := c.attachLibraryFind(t.Context(), set, rows); err != nil {
				results <- err
				return
			}
			if hit, ok := rows[0]["find_hit"].(libraryHit); !ok || !strings.Contains(hit.Snippet, "race condition") {
				results <- fmt.Errorf("missing localized page evidence: %#v", rows)
			}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}
}

func TestLibraryFindNoProgressCanRetry(t *testing.T) {
	c := textFixture(t)
	stalled := true
	hookTextSearch(t, c, func(ctx context.Context) error {
		if stalled {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, nil)
	options := LibraryFindOptions{Query: "cache", Search: TextSearchOptions{TimeoutMS: 100}}
	first, err := c.libraryFindMatches(t.Context(), options)
	if err != nil || first.search.StopReason != "time_budget_no_progress" {
		t.Fatalf("expected no progress: %+v %v", first, err)
	}
	stalled = false
	again, err := c.libraryFindMatches(t.Context(), options)
	if err != nil || again == first || len(again.matches) == 0 {
		t.Fatalf("transient timeout was cached: %+v %v", again, err)
	}
}

func TestLibraryFindPricesMatchingWorkspaces(t *testing.T) {
	c, config := libraryFixture(t)
	if err := c.replacePricing(testPricing(), "test"); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB,
		`UPDATE agent_sessions SET model='claude-opus-4-8' WHERE id='sc'`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,attribution,input_tokens,uncached_input_tokens,total_tokens) VALUES('sc','2026-09-23T02:00:00Z','claude-opus-4-8','request',1000000,1000000,1000000)`,
		`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`)
	if err := c.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	params := url.Values{"find": {"parser"}, "find_fuzzy": {"0"}}
	rows, _, err := server.libraryRows(t.Context(), params, libraryFieldsFor("title", "cost_usd"), nil)
	if err != nil || len(rows) != 1 || rows[0]["cost_usd"] != float64(5) {
		t.Fatalf("matching costs: %v %v", rows, err)
	}
	for _, row := range c.library.rows {
		if _, exists := row["cost_usd"]; exists {
			t.Fatal("pricing matches mutated shared compact rows")
		}
	}
	response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/rows-v2?"+params.Encode(), `{"version":2,"profile":"qt-sqlite-v1","select":["title","cost_usd"],"orderBy":[{"field":"cost_usd","dir":"desc"}],"limit":10}`)
	var page struct {
		Rows     []map[string]any
		Computed []struct {
			Values map[string]struct{ Value float64 }
		}
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Rows) != 1 || page.Rows[0]["cost_usd"] != float64(5) {
		t.Fatalf("priced result: %d %s", response.Code, response.Body.String())
	}
	column, err := (qt.SQLiteComputedColumnStore{DB: c.DB}).Save(t.Context(), queryTableScope, "pharos_library", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
		ID: "double_cost", Label: "Double cost", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[cost_usd] * 2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, computed := range []bool{false, true} {
		selected := []string{"title", "cost_usd"}
		order := "title"
		if computed {
			selected = append(selected, "@computed/double_cost")
			order = "@computed/double_cost"
		}
		wire := qt.WireQuery{Select: selected, OrderBy: qt.OrderBys{{Field: order, Dir: "desc"}}, Limit: 1}
		for _, version := range []int{2, 3} {
			var body any = qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: wire, ExpectedRevisions: map[string]string{column.ID: column.Revision}}
			endpoint := "rows-v2"
			if version == 3 {
				body = qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: wire, ExpectedRevisions: map[string]string{column.ID: column.Revision}}
				endpoint = "query-v3"
			}
			response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/"+endpoint+"?"+params.Encode(), body)
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Rows) != 1 || page.Rows[0]["cost_usd"] != float64(5) {
				t.Fatalf("v%d computed=%v cost dependency: %d %s", version, computed, response.Code, response.Body.String())
			}
			var computedValue any
			if version == 2 && len(page.Computed) > 0 {
				computedValue = page.Computed[0].Values["double_cost"].Value
			} else {
				computedValue = page.Rows[0]["@computed/double_cost"]
			}
			if computed && computedValue != float64(10) {
				t.Fatalf("v%d computed=%v cost dependency: %d %s", version, computed, response.Code, response.Body.String())
			}
		}
	}
	// Cost predicates still resolve the eligible population before keyword matching.
	rows, found, err := server.libraryRows(t.Context(), params, libraryFieldsFor("title", "cost_usd"), nil, []querytable.WhereTerm{{Field: "cost_usd", Op: ">", Value: "6"}})
	if err != nil || len(rows) != 0 || found.search.Count != 0 {
		t.Fatalf("cost filter applied after candidates: %v %+v %v", rows, found, err)
	}
}
