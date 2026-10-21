package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func groupRequest(key string, summaries ...qt.SummaryColumn) qt.QueryRequestV3 {
	selects := []string{key}
	for _, summary := range summaries {
		selects = append(selects, summary.ID)
	}
	return qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Source:   qt.WireQuery{Limit: 1, Offset: 100},
		Grouping: &qt.GroupingDefinition{Keys: []string{key}, Summaries: summaries},
		Result:   qt.WireQuery{Select: selects, Limit: 50}}
}

func TestQueryV3LargeSourceHavingMetricsAndPaging(t *testing.T) {
	override(t, &queryV3MaxRows, 100)
	// A source larger than the row budget.
	sourceRows := queryV3MaxRows + 31
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, sourceRows)
	server := testServer(t, config, catalog)
	q := groupRequest("tool_name", qt.SummaryColumn{ID: "calls", Op: "count"}, qt.SummaryColumn{ID: "time", Op: "sum", Field: "duration_ms"})
	q.Result.Where = []qt.WhereTerm{{Field: "calls", Op: ">", Value: "1"}}
	q.Result.OrderBy = qt.OrderBys{{Field: "calls", Dir: "desc"}}
	q.Result.Limit = 1
	q.Metrics = []qt.AggSpec{{ID: "groups", Op: "count"}, {ID: "shown", Op: "sum", Field: "calls", Scope: "shownRows"}}
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result qt.QueryResultV3
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SourceTotal != int64(sourceRows) || len(result.Rows) != 1 || result.Total < 2 || len(result.Metrics) != 2 {
		t.Fatal(result)
	}
	if result.Metrics[0].Buckets[0].Value != float64(result.Total) || result.Metrics[1].Buckets[0].Value != result.Rows[0]["calls"] {
		t.Fatal(result.Metrics, result.Rows)
	}
	q.Result.Offset = 1
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	var next qt.QueryResultV3
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &next) != nil || next.Total != result.Total || next.Rows[0][qt.GroupIdentityField] == result.Rows[0][qt.GroupIdentityField] {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestQueryV3NativeGroupsMatchGeneralExecution(t *testing.T) {
	c, config := testCatalog(t)
	seedToolCalls(t, c, 40)
	if _, err := c.DB.Exec("UPDATE tool_calls SET started_at=NULL WHERE id='call-0'; UPDATE tool_calls SET started_at='invalid' WHERE id='call-1'"); err != nil {
		t.Fatal(err)
	}
	c.maintainQueryTextIndexes(t.Context())
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	for _, dataset := range []string{"tool_calls", "tools"} {
		field := "duration_ms"
		if dataset == "tools" {
			field = "total_duration_ms"
		}
		for _, key := range []string{"provider", "tool_name", "repository_name", "day"} {
			wheres := [][]qt.WhereTerm{nil, {{Field: "provider", Op: "=", Value: "codex"}}, {{Field: "tool_name", Op: "contains", Value: "absent"}}}
			if dataset == "tool_calls" {
				wheres = append(wheres, []qt.WhereTerm{{Field: "command", Op: "contains", Value: "test", Negated: true}},
					[]qt.WhereTerm{{Field: "command", Op: "contains", Value: "absent", Negated: true}, {Field: "command", Op: "contains", Value: "git", Negated: true}},
					[]qt.WhereTerm{{Field: "day", Op: "is_null"}})
			}
			for _, where := range wheres {
				q := groupRequest(key, qt.SummaryColumn{ID: "n", Op: "count"}, qt.SummaryColumn{ID: "total", Op: "sum", Field: field}, qt.SummaryColumn{ID: "mean", Op: "avg", Field: field}, qt.SummaryColumn{ID: "known", Op: "count", Field: field})
				q.Source.Where = where
				q.Result.OrderBy = qt.OrderBys{{Field: "n", Dir: "desc"}, {Field: key, Dir: "asc"}}
				q.Result.Limit = 2
				q.Metrics = []qt.AggSpec{{ID: "groups", Op: "count"}, {ID: "shown", Op: "sum", Field: "n", Scope: "shownRows"}}
				schema, _ := sqliteQuerySchema(dataset)
				nativeMetrics = true
				got, _, err := server.executeQueryV3(t.Context(), dataset, nil, schema, q)
				nativeMetrics = false
				if _, e := c.DB.Exec("DELETE FROM meta WHERE key='tool_query_facts_ready'"); e != nil {
					t.Fatal(e)
				}
				want, _, e := server.executeQueryV3(t.Context(), dataset, nil, schema, q)
				if _, e := c.DB.Exec("INSERT INTO meta(key,value) VALUES('tool_query_facts_ready',?)", toolQueryFacts.version()); e != nil {
					t.Fatal(e)
				}
				nativeMetrics = true
				if err != nil || e != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s/%s/%v: native=%+v general=%+v errors=%v/%v", dataset, key, where, got, want, err, e)
				}
			}
		}
	}
}

func TestQueryV3UsageFiltersSessionsAndCalendar(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog)
	server := testServer(t, config, catalog)
	q := groupRequest("provider", qt.SummaryColumn{ID: "tokens", Op: "sum", Field: "total_tokens"}, qt.SummaryColumn{ID: "sessions", Op: "count_distinct", Field: "agent_session_id"})
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/usage/query-v3", q)
	var result qt.QueryResultV3
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Rows) != 1 || result.Rows[0]["tokens"] != float64(3050) || result.Rows[0]["sessions"] != float64(1) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, field := range result.Schema.Fields {
		if field.Name == "provider" && field.Type != "enum" {
			t.Fatal("group keys must preserve the host enum schema", field)
		}
	}
	q.Source.Where = []qt.WhereTerm{{Field: "last_usage_at", Op: ">=", Value: "2026-09-21T00:00:00Z"}}
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/usage/query-v3", q)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Rows[0]["tokens"] != float64(2030) {
		t.Fatal("source filter outside the selected projection was lost", w.Code, w.Body.String())
	}
	q.Source.Where = []qt.WhereTerm{{Field: "hour", Op: "=", Value: "2026-09-21T01:00:00Z"}}
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/usage/query-v3", q)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Rows[0]["tokens"] != float64(2030) {
		t.Fatal(w.Code, w.Body.String())
	}
	q.Source.Where = nil
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/usage/query-v3", q)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Rows[0]["tokens"] != float64(3050) {
		t.Fatal("hour query mutated the cached daily population", w.Code, w.Body.String())
	}
	calendar := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Result: qt.WireQuery{Select: []string{"id"}, Limit: 50},
		Metrics: []qt.AggSpec{{ID: "days", Op: "sum", Field: "total_tokens", GroupBy: []string{"first_usage_at"}, DateGrouping: &qt.MetricDateGrouping{Field: "first_usage_at", Interval: "day", TimeZone: "UTC", Range: "observed"}}}}
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/usage/query-v3", calendar)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Metrics) != 1 || len(result.Metrics[0].Buckets) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestQueryV3AuthorizationAndValidation(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	q := groupRequest("provider", qt.SummaryColumn{ID: "records", Op: "count"})
	if w := apiCall(t, server, "", http.MethodPost, "/api/query/usage/query-v3", q); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, change := range []func(*qt.QueryRequestV3){
		func(q *qt.QueryRequestV3) { q.Grouping.Keys = []string{"missing"} },
		func(q *qt.QueryRequestV3) {
			q.Result.Where = []qt.WhereTerm{{Field: "total_tokens", Op: ">", Value: "0"}}
		},
		func(q *qt.QueryRequestV3) { q.Grouping.Summaries[0].ID = "provider" },
		func(q *qt.QueryRequestV3) { q.Profile = "other" },
	} {
		copy := groupRequest("provider", qt.SummaryColumn{ID: "records", Op: "count"})
		change(&copy)
		if w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/usage/query-v3", copy); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`null`, `{}`, `{"version":3,"sourceSQL":"SELECT 1"}`, `{} {}`} {
		w := queryExplorationCall(server, config.APIToken, "POST", "/api/query/usage/query-v3", body)
		if w.Code != 400 || strings.Contains(w.Body.String(), `"rows"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := server.executeQueryV3(ctx, "usage", nil, PharosUsageSchema(), q); err == nil {
		t.Fatal("cancelled query executed")
	}
}

func TestQueryV3ComputedSourceAndRepresentative(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 31)
	store := qt.SQLiteComputedColumnStore{DB: catalog.DB}
	column, err := store.Save(context.Background(), queryTableScope, "pharos_tool_calls", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
		ID: "double-duration", Label: "Twice the duration", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[duration_ms]*2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	q := groupRequest("tool_name", qt.SummaryColumn{ID: "time", Op: "sum", Field: "@computed/double-duration"},
		qt.SummaryColumn{ID: "example", Op: "row", Field: "id"}, qt.SummaryColumn{ID: "example_duration", Op: "row", Field: "duration_ms"})
	q.ExpectedRevisions = map[string]string{column.ID: column.Revision}
	q.Source.Where = []qt.WhereTerm{{Field: "@computed/double-duration", Op: ">", Value: "0"}}
	q.Grouping.Representative = &qt.RepresentativeRow{OrderBy: []qt.OrderBy{{Field: "duration_ms", Dir: "desc", Nulls: "last"}}}
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	var result qt.QueryResultV3
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Rows) == 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, group := range result.Rows {
		var duration float64
		if err = catalog.DB.QueryRow("SELECT duration_ms FROM tool_calls WHERE id=?", group["example"]).Scan(&duration); err != nil || group["example_duration"] != duration {
			t.Fatal(group, duration, err)
		}
	}
	q.ExpectedRevisions[column.ID] = "stale"
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "definition_changed") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestQueryV3IncompleteLibrarySearchWithFormula(t *testing.T) {
	catalog, config := libraryFixture(t)
	ctx := context.Background()
	if err := catalog.refreshAllLibrary(ctx); err != nil {
		t.Fatal(err)
	}
	column, err := (qt.SQLiteComputedColumnStore{DB: catalog.DB}).Save(ctx, queryTableScope, "pharos_library", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
		ID: "constant", Label: "Constant", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "1+1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	for _, scenario := range []string{"limited", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			params := url.Values{"find": {"parser"}, "find_fuzzy": {"0"}}
			options, _, err := libraryFindRequest(params)
			if err != nil {
				t.Fatal(err)
			}
			options.WorkspaceRows = true
			options.Search.WorkspaceIDs = []string{"b", "c"}
			encoded, _ := json.Marshal(options)
			generation, err := catalog.libraryFindVersion(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce both search stop conditions without generating thousands of
			// duplicate messages: incomplete populations must not be sorted or counted.
			set := &libraryFindSet{matches: map[string]*libraryFindMatch{"c": {Count: 1}}, limited: scenario == "limited"}
			if scenario == "incomplete" {
				set.search = textSearchResult{Strategy: "indexed", Complete: false, StopReason: "candidate_limit"}
			}
			catalog.find.mu.Lock()
			set.cacheVersion = generation
			catalog.find.entries = map[string]*libraryFindSet{string(encoded): set}
			catalog.find.mu.Unlock()
			q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
				Source: qt.WireQuery{}, Result: qt.WireQuery{Select: []string{"title", "@computed/constant"}, Limit: 10}, ExpectedRevisions: map[string]string{column.ID: column.Revision}}
			if _, _, err := server.executeQueryV3(ctx, "library", params, PharosLibrarySchema(), q); err == nil || !strings.Contains(err.Error(), "search is incomplete") {
				t.Fatalf("incomplete workspace search: %v", err)
			}
			q.Grouping = &qt.GroupingDefinition{Keys: []string{"source_kind"}, Summaries: []qt.SummaryColumn{{ID: "records", Op: "count"}}}
			q.Result = qt.WireQuery{Select: []string{"source_kind", "records"}, Limit: 10}
			if _, _, err = server.executeQueryV3(ctx, "library", params, PharosLibrarySchema(), q); err == nil || !strings.Contains(err.Error(), "search is incomplete") {
				t.Fatalf("incomplete grouped search: %v", err)
			}
		})
	}
}
