package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
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

func TestQueryV3LibraryDefersDisplayFieldsUntilAfterPaging(t *testing.T) {
	c, config := libraryFixture(t)
	s := testServer(t, config, c)
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Result: qt.WireQuery{Select: []string{"id", "title", "tool_error_count", "compaction_count"}, OrderBy: []qt.OrderBy{{Field: "id", Dir: "desc"}}, Limit: 1}}
	read := func() qt.QueryResultV3 {
		t.Helper()
		got, _, err := s.executeQueryV3(t.Context(), "library", url.Values{}, PharosLibrarySchema(), q)
		if err != nil || len(got.Rows) != 1 || got.Rows[0]["id"] != "c" {
			t.Fatalf("paged Library: %+v %v", got, err)
		}
		return got
	}
	got := read()
	if got.Total != 2 || got.Rows[0]["title"] != "Charlie" || integer(got.Rows[0]["tool_error_count"]) != 1 || integer(got.Rows[0]["compaction_count"]) != 2 {
		t.Fatalf("display fields were not filled for the page: %+v", got)
	}
	for _, row := range c.library.rows {
		for _, field := range []string{"title", "tool_error_count", "compaction_count"} {
			if _, ok := row[field]; ok {
				t.Fatalf("display-only %s was derived for every workspace", field)
			}
		}
	}
	// Host keyword restrictions still need their native source field before
	// paging, even if it is otherwise used only for display.
	record, _ := json.Marshal(recordSearchSpec{Field: "title", Query: "Charlie"})
	matched, _, err := s.executeQueryV3(t.Context(), "library", url.Values{"record_search": {string(record)}}, PharosLibrarySchema(), q)
	if err != nil || matched.Total != 1 || len(matched.Rows) != 1 || matched.Rows[0]["id"] != "c" {
		t.Fatalf("record keyword lost its source field: %+v %v", matched, err)
	}
	// A displayed formula still needs its base inputs before execution.
	column, err := (qt.SQLiteComputedColumnStore{DB: c.DB}).Save(t.Context(), queryTableScope, "pharos_library", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
		ID: "tokens", Label: "Tokens", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[token_count]"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	q.Result.Select = []string{"id", "@computed/tokens"}
	q.ExpectedRevisions = map[string]string{column.ID: column.Revision}
	if row := read().Rows[0]; integer(row["@computed/tokens"]) != 500 {
		t.Fatalf("displayed formula lost its dependency: %+v", row)
	}
	// Native filters, sorts and metrics also need their complete population.
	q.Result.Where = []qt.WhereTerm{{Field: "tool_error_count", Op: ">", Value: "0"}}
	q.Result.OrderBy = []qt.OrderBy{{Field: "compaction_count", Dir: "desc"}}
	q.Metrics = []qt.AggSpec{{ID: "errors", Op: "sum", Field: "tool_error_count"}}
	got = read()
	if got.Total != 1 || integer(got.Metrics[0].Buckets[0].Value) != 1 {
		t.Fatalf("deferring display fields changed query membership or totals: %+v", got)
	}
}

func TestQueryV3ReleasesAdmissionAfterExecutionError(t *testing.T) {
	gate := &queryAdmission{limit: 1}
	override(t, &heavyQueries, gate)
	override(t, &queryV3MaxGroups, 1)
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 3)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	q := groupRequest("id", qt.SummaryColumn{ID: "calls", Op: "count"})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, _, err := server.executeQueryV3(ctx, "tool_calls", nil, PharosToolCallsSchema(), q); err == nil || !strings.Contains(err.Error(), "tool_calls query:") {
		t.Fatalf("expected an execution-time group budget error: %v", err)
	}
	gate.mu.Lock()
	active, queued := gate.active, gate.wait.Len()
	gate.mu.Unlock()
	if active != 0 || queued != 0 || catalog.reader().Stats().InUse != 0 {
		t.Fatalf("failed execution retained a slot or connection: active=%d queued=%d readers=%d", active, queued, catalog.reader().Stats().InUse)
	}
	queryV3MaxGroups = 3
	if result, _, err := server.executeQueryV3(ctx, "tool_calls", nil, PharosToolCallsSchema(), q); err != nil || result.Total != 3 {
		t.Fatalf("all groups within the budget: %+v, %v", result, err)
	}
	q.Source.Where = []qt.WhereTerm{{Field: "id", Op: "=", Value: "call-0"}}
	if _, _, err := server.executeQueryV3(ctx, "tool_calls", nil, PharosToolCallsSchema(), q); err != nil {
		t.Fatalf("query after a failed execution could not use admission: %v", err)
	}
}

func TestQueryV3LargeSourceHavingMetricsAndPaging(t *testing.T) {
	override(t, &queryV3MaxRows, 100)
	// A source larger than the row budget.
	sourceRows := queryV3MaxRows + 31
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, sourceRows)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	q := groupRequest("tool_name", qt.SummaryColumn{ID: "calls", Op: "count"}, qt.SummaryColumn{ID: "time", Op: "sum", Field: "duration_ms"})
	q.Result.Where = []qt.WhereTerm{{Field: "calls", Op: ">", Value: "1"}}
	q.Result.OrderBy = []qt.OrderBy{{Field: "calls", Dir: "desc"}}
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

func TestQueryV3GroupsMatchWithoutToolProjection(t *testing.T) {
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
	// Grouping must agree on the stored projection and its live-join fallback.
	dataset, field := "tool_calls", "duration_ms"
	for _, key := range []string{"provider", "tool_name", "repository_name", "day"} {
		wheres := [][]qt.WhereTerm{nil, {{Field: "provider", Op: "=", Value: "codex"}}, {{Field: "tool_name", Op: "contains", Value: "absent"}}}
		wheres = append(wheres, []qt.WhereTerm{{Field: "command", Op: "contains", Value: "test", Negated: true}},
			[]qt.WhereTerm{{Field: "command", Op: "contains", Value: "absent", Negated: true}, {Field: "command", Op: "contains", Value: "git", Negated: true}},
			[]qt.WhereTerm{{Field: "day", Op: "is_null"}})
		for _, where := range wheres {
			q := groupRequest(key,
				qt.SummaryColumn{ID: "n", Op: "count"},
				qt.SummaryColumn{ID: "total", Op: "sum", Field: field},
				qt.SummaryColumn{ID: "mean", Op: "avg", Field: field},
				qt.SummaryColumn{ID: "known", Op: "count", Field: field},
				qt.SummaryColumn{ID: "min", Op: "min", Field: field},
				qt.SummaryColumn{ID: "max", Op: "max", Field: field},
				qt.SummaryColumn{ID: "distinct", Op: "count_distinct", Field: field})
			q.Source.Where = where
			q.Result.OrderBy = []qt.OrderBy{{Field: "n", Dir: "desc"}, {Field: key, Dir: "asc"}}
			q.Result.Limit = 2
			q.Metrics = []qt.AggSpec{{ID: "groups", Op: "count"}, {ID: "shown", Op: "sum", Field: "n", Scope: "shownRows"}}
			schema, _ := sqliteQuerySchema(dataset)
			got, _, err := server.executeQueryV3(t.Context(), dataset, nil, schema, q)
			if _, e := c.DB.Exec("DELETE FROM meta WHERE key='tool_query_facts_ready'"); e != nil {
				t.Fatal(e)
			}
			want, _, e := server.executeQueryV3(t.Context(), dataset, nil, schema, q)
			if _, e := c.DB.Exec("INSERT INTO meta(key,value) VALUES('tool_query_facts_ready',?)", toolQueryFacts.version()); e != nil {
				t.Fatal(e)
			}
			if err != nil || e != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s/%s/%v: projected=%+v joined=%+v errors=%v/%v", dataset, key, where, got, want, err, e)
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
	publishProjections(t, catalog)
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
	if w.Code != 409 || !strings.Contains(w.Body.String(), "definition_changed") {
		t.Fatal(w.Code, w.Body.String())
	}

	t.Run("Library selection through a computed filter", func(t *testing.T) {
		catalog, config := libraryFixture(t)
		execTx(t, catalog.DB,
			`INSERT INTO metrics(workspace_id,name,value,unit,status,extractor_version,observed_at) VALUES('b','total_tokens',900,'tokens','observed','test','x')`,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('d','canonical','local','d','Small work','x')`,
			`INSERT INTO metrics(workspace_id,name,value,unit,status,extractor_version,observed_at) VALUES('d','total_tokens',100,'tokens','observed','test','x')`)
		if err := catalog.refreshAllLibrary(t.Context()); err != nil {
			t.Fatal(err)
		}
		column, err := (qt.SQLiteComputedColumnStore{DB: catalog.DB}).Save(t.Context(), queryTableScope, "pharos_library", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
			ID: "tokens", Label: "Tokens", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[token_count]"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		server := testServer(t, config, catalog)
		w := apiCall(t, server, config.APIToken, http.MethodGet, "/api/query/library/capabilities", nil)
		var capabilities struct {
			Execution qt.ComputedExecution   `json:"computedExecution"`
			Query     qt.QueryCapabilitiesV3 `json:"queryCapabilities"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &capabilities) != nil || capabilities.Query.Version != 3 || !capabilities.Execution.Fields["@computed/tokens"].Filter {
			t.Fatal(w.Code, w.Body.String())
		}
		q := qt.QueryRequestV3{Version: 3, Profile: capabilities.Execution.Profile, Now: time.Now().UnixMilli(), Rows: true,
			ExpectedRevisions: capabilities.Execution.ResolvedRevisions, PlanToken: capabilities.Execution.PlanToken,
			Result: qt.WireQuery{Select: []string{"id"}, Where: []qt.WhereTerm{{Field: "@computed/tokens", Op: ">", Value: "200"}},
				OrderBy: []qt.OrderBy{{Field: "@computed/tokens", Dir: "desc"}}, Limit: 1, Offset: 1}}
		for _, selection := range []bool{false, true} {
			if selection {
				q.Result.Limit, q.Result.Offset = 200, 0
			}
			w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/query-v3", q)
			var result qt.QueryResultV3
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total != 2 {
				t.Fatal(w.Code, w.Body.String())
			}
			ids := []any{}
			for _, row := range result.Rows {
				ids = append(ids, row["id"])
			}
			want := []any{"c"}
			if selection {
				want = []any{"b", "c"}
			}
			if !reflect.DeepEqual(ids, want) || result.Execution.ResolvedRevisions[column.ID] != column.Revision {
				t.Fatalf("selection=%v: rows=%v execution=%+v", selection, result.Rows, result.Execution)
			}
		}
		q.ExpectedRevisions[column.ID] = "stale"
		w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/query-v3", q)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "definition_changed") {
			t.Fatal(w.Code, w.Body.String())
		}
	})
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

func TestQueryV3RecordKeywordsUseIndividualRecordsBeforeGrouping(t *testing.T) {
	c, config := testCatalog(t)
	seedToolCalls(t, c, 5)
	commands := []string{"cache migration", "buffer migration", "cache obsolete", "migration only", "CAFÉ deploy"}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		if _, err := tx.Exec("UPDATE tool_calls SET command=?,tool_name=?,status='ok',workspace_id='ws-0' WHERE id=?", command, []string{"A", "B", "C", "D", "E"}[i], fmt.Sprint("call-", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	publishProjections(t, c)
	s := testServer(t, config, c)
	schema, _ := sqliteQuerySchema("tool_calls")
	params := func(spec recordSearchSpec) url.Values {
		t.Helper()
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		return url.Values{"record_search": {string(raw)}}
	}
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Result:  qt.WireQuery{Select: []string{"id", "tool_name"}, OrderBy: []qt.OrderBy{{Field: "id", Dir: "asc"}}, Limit: 50},
		Metrics: []qt.AggSpec{{ID: "n", Op: "count"}}}
	for _, test := range []struct {
		query string
		want  []string
	}{
		{`(cache OR buffer) NOT obsolete`, []string{"call-0", "call-1"}},
		{`cache AND migration`, []string{"call-0"}},
		{`"cache migration"`, []string{"call-0"}},
		{`migrat*`, []string{"call-0", "call-1", "call-3"}},
		{`cache PRE/0 migration`, []string{"call-0"}},
		{`migration NEAR/0 buffer`, []string{"call-1"}},
		{`cafe`, []string{"call-4"}},
		{`no-such-record`, []string{}},
	} {
		t.Run(test.query, func(t *testing.T) {
			got, _, err := s.executeQueryV3(t.Context(), "tool_calls", params(recordSearchSpec{Field: "command", Query: test.query}), schema, q)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, row := range got.Rows {
				ids = append(ids, firstString(row["id"]))
			}
			if !reflect.DeepEqual(ids, test.want) || got.Total != int64(len(test.want)) || got.Metrics[0].Buckets[0].Value != float64(len(test.want)) {
				t.Fatalf("rows=%v total=%d metrics=%+v", ids, got.Total, got.Metrics)
			}
		})
	}
	search := params(recordSearchSpec{Field: "command", Query: `(cache OR buffer) NOT obsolete`})
	q.Result.Limit = 1
	got, _, err := s.executeQueryV3(t.Context(), "tool_calls", search, schema, q)
	if err != nil || len(got.Rows) != 1 || got.Total != 2 || got.Metrics[0].Buckets[0].Value != float64(2) {
		t.Fatalf("limit changed population: %+v %v", got, err)
	}
	grouped := groupRequest("status", qt.SummaryColumn{ID: "calls", Op: "count"})
	grouped.Source.Where = []qt.WhereTerm{{Field: "tool_name", Op: "!=", Value: "B"}}
	grouped.Result.Where = []qt.WhereTerm{{Field: "calls", Op: ">", Value: "0"}}
	got, _, err = s.executeQueryV3(t.Context(), "tool_calls", search, schema, grouped)
	if err != nil || len(got.Rows) != 1 || got.Rows[0]["calls"] != float64(1) {
		t.Fatalf("source WHERE/grouping/HAVING: %+v %v", got, err)
	}
	w := apiCall(t, s, config.APIToken, http.MethodGet, "/api/query/tool_calls/distinct?field=tool_name&"+search.Encode(), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"A"`) || !strings.Contains(w.Body.String(), `"B"`) || strings.Contains(w.Body.String(), `"C"`) {
		t.Fatalf("metadata ignored search: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/query/tool_calls", "/api/query/tool_calls/rows-v2", "/api/query/tool_calls/metrics"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path+"?"+search.Encode(), strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+config.APIToken)
		s.ServeHTTP(response, request)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "requires query-v3") {
			t.Fatalf("older protocol ignored keywords: %d %s", response.Code, response.Body.String())
		}
	}
	// Analyze's requests carry the keyword's definition as host query state;
	// record_search, compiled from it, must agree with it.
	declared := q
	declared.HostState = json.RawMessage(`{"version":1,"params":{"record_tool_calls":["{\"field\":\"command\"}"]}}`)
	if got, _, err := s.executeQueryV3(t.Context(), "tool_calls", search, schema, declared); err != nil || got.Total != 2 {
		t.Fatalf("host state with its compiled search: %+v %v", got, err)
	}
	none := q
	none.HostState = json.RawMessage(`{"version":1,"params":{}}`)
	for _, mismatch := range []struct {
		request qt.QueryRequestV3
		params  url.Values
	}{{declared, url.Values{}}, {none, search}, {qt.QueryRequestV3{HostState: json.RawMessage(`{"version":"x"}`)}, search}} {
		if _, _, err := s.executeQueryV3(t.Context(), "tool_calls", mismatch.params, schema, mismatch.request); err == nil {
			t.Fatalf("ran a keyword definition and search that disagree: %s %v", mismatch.request.HostState, mismatch.params)
		}
	}
	if got, _, err := s.executeQueryV3(t.Context(), "tool_calls", url.Values{}, schema, none); err != nil || got.Total != 5 {
		t.Fatalf("host state without a keyword: %+v %v", got, err)
	}

	for _, spec := range []recordSearchSpec{{Field: "command", Query: "[", Mode: "regex"}, {Field: "duration_ms", Query: "cache"}, {Field: "command", Query: "cache", Where: &textQuery{Op: "term", Text: "cache"}}} {
		if _, _, err := s.executeQueryV3(t.Context(), "tool_calls", params(spec), schema, q); err == nil {
			t.Fatalf("accepted invalid record search: %+v", spec)
		}
	}
	t.Run("excluded commands do not consume regex budget", func(t *testing.T) {
		override(t, &recordSearchMaxRegexWork, 1024)
		tx := seedTx(t, c.DB)
		if _, err := tx.Exec("UPDATE tool_calls SET command=?,status='error',duration_ms=0 WHERE id='call-1'", strings.Repeat("a", 256)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("UPDATE tool_calls SET command='a0123456789cache',duration_ms=10 WHERE id='call-0'"); err != nil {
			t.Fatal(err)
		}
		commitSeed(t, tx)
		column, err := (qt.SQLiteComputedColumnStore{DB: c.DB}).Save(t.Context(), queryTableScope, schema.Name, qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{
			ID: "duration", Label: "Duration", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[duration_ms]"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		search := params(recordSearchSpec{Field: "command", Query: `a.{10}cache`, Mode: "regex"})
		for _, filter := range []struct {
			name  string
			where []qt.WhereTerm
		}{
			{"status", []qt.WhereTerm{{Field: "status", Op: "=", Value: "ok"}}},
			{"indexed source parameters", []qt.WhereTerm{{Field: "workspace_id", Op: "=", Value: "ws-0"}, {Field: "status", Op: "=", Value: "ok"}}},
			{"OR", []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "status", Op: "=", Value: "ok"}, {Field: "tool_name", Op: "=", Value: "absent"}}}}},
			{"negated", []qt.WhereTerm{{Field: "status", Op: "=", Value: "error", Negated: true}}},
			{"computed", []qt.WhereTerm{{Field: "@computed/duration", Op: ">", Value: "0"}}},
		} {
			t.Run(filter.name, func(t *testing.T) {
				rows := q
				rows.Result.Where = filter.where
				rows.ExpectedRevisions = map[string]string{column.ID: column.Revision}
				got, _, err := s.executeQueryV3(t.Context(), "tool_calls", search, schema, rows)
				if err != nil || got.Total != 1 || len(got.Rows) != 1 || got.Rows[0]["id"] != "call-0" || got.Metrics[0].Buckets[0].Value != float64(1) {
					t.Fatalf("filtered rows and metrics: %+v %v", got, err)
				}
				rows.Rows = false
				got, _, err = s.executeQueryV3(t.Context(), "tool_calls", search, schema, rows)
				if err != nil || got.Total != 1 || len(got.Rows) != 0 || got.Metrics[0].Buckets[0].Value != float64(1) {
					t.Fatalf("filtered metrics without rows: %+v %v", got, err)
				}
				groups := groupRequest("status", qt.SummaryColumn{ID: "calls", Op: "count"})
				groups.Source.Where = filter.where
				groups.ExpectedRevisions = rows.ExpectedRevisions
				groups.Result.Where = []qt.WhereTerm{{Field: "calls", Op: ">", Value: "0"}}
				groups.Metrics = []qt.AggSpec{{ID: "n", Op: "sum", Field: "calls"}}
				got, _, err = s.executeQueryV3(t.Context(), "tool_calls", search, schema, groups)
				if err != nil || got.SourceTotal != 1 || got.Total != 1 || len(got.Rows) != 1 || got.Rows[0]["calls"] != float64(1) || got.Metrics[0].Buckets[0].Value != float64(1) {
					t.Fatalf("filtered groups and metrics: %+v %v", got, err)
				}
			})
		}
		if _, _, err := s.executeQueryV3(t.Context(), "tool_calls", search, schema, q); err == nil || !strings.Contains(err.Error(), "search work budget") {
			t.Fatalf("included command must still enforce regex budget: %v", err)
		}
	})
}

func TestQueryV3RecordKeywordsOnMapRowsAndMatchingOptions(t *testing.T) {
	c, config := testCatalog(t)
	if _, err := c.DB.Exec(`INSERT INTO mcp_calls(called_at,tool_name,status,duration_ms,response_bytes,estimated_output_tokens) VALUES
	 ('2026-10-01','Search_Cache','ok',0,0,0),('2026-10-01','cache migration','ok',0,0,0),('2026-10-01','cache-migration','ok',0,0,0)`); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, config, c)
	v3Schema, err := sqliteQuerySchema("mcp_calls")
	if err != nil {
		t.Fatal(err)
	}
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: qt.WireQuery{Select: []string{"tool_name"}, Limit: 50}}
	for _, test := range []struct {
		spec  recordSearchSpec
		count int64
	}{
		{recordSearchSpec{Field: "tool_name", Query: "search_cache", Mode: "literal"}, 1},
		{recordSearchSpec{Field: "tool_name", Query: "search_cache", Mode: "literal", CaseSensitive: true}, 0},
		{recordSearchSpec{Field: "tool_name", Query: `cache.migration`, Mode: "regex"}, 2},
		{recordSearchSpec{Field: "tool_name", Query: `"cache migration"`}, 2},
		{recordSearchSpec{Field: "tool_name", Query: `"cache migration"`, Separators: true}, 1},
	} {
		raw, _ := json.Marshal(test.spec)
		result, _, err := s.executeQueryV3(t.Context(), "mcp_calls", url.Values{"record_search": {string(raw)}}, v3Schema, q)
		if err != nil || result.Total != test.count {
			t.Fatalf("spec=%+v result=%+v err=%v", test.spec, result, err)
		}
	}
	t.Run("excluded map rows do not consume text budget", func(t *testing.T) {
		override(t, &recordSearchMaxTextBytes, 64)
		if _, err := c.DB.Exec("INSERT INTO mcp_calls(called_at,tool_name,status,duration_ms,response_bytes,estimated_output_tokens) VALUES('2026-10-01',?,'error',0,0,0)", strings.Repeat("cache ", 16)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(recordSearchSpec{Field: "tool_name", Query: "cache"})
		search := url.Values{"record_search": {string(raw)}}
		rows := q
		rows.Result.Where = []qt.WhereTerm{{Field: "status", Op: "=", Value: "ok"}}
		rows.Result.Limit = 1
		rows.Metrics = []qt.AggSpec{{ID: "n", Op: "count"}}
		got, _, err := s.executeQueryV3(t.Context(), "mcp_calls", search, v3Schema, rows)
		if err != nil || got.Total != 3 || len(got.Rows) != 1 || got.Metrics[0].Buckets[0].Value != float64(3) {
			t.Fatalf("filtered map rows and metrics: %+v %v", got, err)
		}
		groups := groupRequest("status", qt.SummaryColumn{ID: "calls", Op: "count"})
		groups.Source.Where = rows.Result.Where
		got, _, err = s.executeQueryV3(t.Context(), "mcp_calls", search, v3Schema, groups)
		if err != nil || got.SourceTotal != 3 || got.Total != 1 || len(got.Rows) != 1 || got.Rows[0]["calls"] != float64(3) {
			t.Fatalf("filtered map groups: %+v %v", got, err)
		}
		if _, _, err := s.executeQueryV3(t.Context(), "mcp_calls", search, v3Schema, q); err == nil || !strings.Contains(err.Error(), "text exceeds") {
			t.Fatalf("included map row must still enforce text budget: %v", err)
		}
	})
}

// Pharos advertises the library's capabilities as they are, so every shape
// they promise must run on its own datasets: hour-of-day and day-of-week
// cycles, calendar grouping keys, distinct and summed minimums, and OR-of-AND
// source filters, over a SQL dataset and over staged map rows.
func TestQueryV3RunsEveryAdvertisedShape(t *testing.T) {
	// Even one slot must handle cold staged sources and hour-dependent Usage
	// metrics: their cache dependencies cannot wait inside an admitted query.
	override(t, &heavyQueries, &queryAdmission{limit: 1})
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 40)
	ingestUsageFixture(t, catalog)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	for _, sample := range []struct{ dataset, at, day, key, field, nullable string }{
		{"tool_calls", "started_at", "day", "tool_name", "duration_ms", "mcp_server"},
		{"usage", "first_usage_at", "day", "provider", "total_tokens", "repository_name"},
	} {
		w := apiCall(t, server, config.APIToken, http.MethodGet, "/api/query/"+sample.dataset+"/capabilities", nil)
		var handshake struct {
			QueryCapabilities qt.QueryCapabilitiesV3 `json:"queryCapabilities"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &handshake) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		capabilities := handshake.QueryCapabilities
		if !capabilities.CalendarCycles || !capabilities.CalendarGroupKeys || !capabilities.MetricMinCountMeasures {
			t.Fatalf("%s: %+v", sample.dataset, capabilities)
		}
		run := func(body string) qt.QueryResultV3 {
			t.Helper()
			w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/"+sample.dataset+"/query-v3", body)
			var result qt.QueryResultV3
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatalf("%s: %d %s\n%s", sample.dataset, w.Code, w.Body.String(), body)
			}
			return result
		}
		total := func(metric qt.MetricResult) (n int64) {
			if metric.Error != "" {
				t.Fatalf("%s: metric %s: %s", sample.dataset, metric.ID, metric.Error)
			}
			for _, bucket := range metric.Buckets {
				n += bucket.Count
			}
			return n
		}
		head := fmt.Sprintf(`{"version":3,"profile":%q,"now":%d,"rows":false,"source":{},"result":{"select":["id"],"limit":1}`, qt.SQLiteExpressionProfile, time.Now().UnixMilli())
		cycle := func(interval string) string {
			return fmt.Sprintf(`{"id":%q,"op":"count","groupBy":[%q],"dateGrouping":{"field":%q,"interval":%q,"timeZone":"UTC","includeEmpty":true,"range":"observed"}}`, interval, sample.at, sample.at, interval)
		}
		cycles := run(head + `,"metrics":[{"id":"all","op":"count"},` + cycle("hourOfDay") + `,` + cycle("dayOfWeek") + `]}`)
		population := total(cycles.Metrics[0])
		if population == 0 || len(cycles.Metrics[1].Buckets) != 24 || len(cycles.Metrics[2].Buckets) != 7 || total(cycles.Metrics[1]) != population || total(cycles.Metrics[2]) != population {
			t.Fatalf("%s: cycles %+v", sample.dataset, cycles.Metrics)
		}
		// A calendar key groups as the dataset's own local-day field does.
		grouped := func(key string) map[string]float64 {
			result := run(fmt.Sprintf(`{"version":3,"profile":%q,"now":%d,"rows":true,"source":{"limit":1},"grouping":{"keys":[%s],"summaries":[{"id":"n","op":"count"}],"representative":null},"result":{"select":["n"],"limit":100}}`,
				qt.SQLiteExpressionProfile, time.Now().UnixMilli(), key))
			counts := map[string]float64{}
			for _, row := range result.Rows {
				counts[fmt.Sprint(row["n"])]++
			}
			return counts
		}
		if calendar, plain := grouped(fmt.Sprintf(`{"field":%q,"interval":"day","timeZone":"UTC"}`, sample.at)), grouped(fmt.Sprintf("%q", sample.day)); !reflect.DeepEqual(calendar, plain) {
			t.Fatalf("%s: calendar-day groups %v, day groups %v", sample.dataset, calendar, plain)
		}
		// Minimums over distinct values and sums hide groups and say so.
		minimums := run(head + fmt.Sprintf(`,"metrics":[{"id":"distinct","op":"count","groupBy":[%q],"minCount":{"min":1000,"distinct":"id"}},{"id":"summed","op":"count","groupBy":[%q],"minCount":{"min":1,"sum":%q}}]}`, sample.key, sample.key, sample.field))
		if hidden := minimums.Metrics[0].HiddenGroups; hidden == nil || *hidden == 0 || len(minimums.Metrics[0].Buckets) != 0 || minimums.Metrics[1].Error != "" {
			t.Fatalf("%s: minimums %+v", sample.dataset, minimums.Metrics)
		}
		// An OR of AND branches selects the union of its branches: here only
		// its second, where a flat OR of the same literals keeps every row.
		filtered := func(where string) int64 {
			return total(run(fmt.Sprintf(`{"version":3,"profile":%q,"now":%d,"rows":false,"source":{},"result":{"select":["id"],"where":[%s],"limit":1},"metrics":[{"id":"n","op":"count"}]}`,
				qt.SQLiteExpressionProfile, time.Now().UnixMilli(), where)).Metrics[0])
		}
		null := fmt.Sprintf(`{"field":%q,"op":"is_null","value":""}`, sample.nullable)
		present := fmt.Sprintf(`{"field":%q,"op":"is_not_null","value":""}`, sample.nullable)
		branches := filtered(`{"any":[{"all":[` + present + `,` + null + `]},` + present + `]}`)
		if keyed := filtered(present); branches != keyed || keyed == population || filtered(null) == 0 {
			t.Fatalf("%s: OR of AND branches kept %d of %d rows, plain %d", sample.dataset, branches, population, keyed)
		}
	}
}

func TestQueryV3KeywordSourceBudgetRefusesPartialMembership(t *testing.T) {
	override(t, &queryV3MaxPopulation, 2)
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 5)
	execTx(t, catalog.DB, "UPDATE tool_calls SET command='noise'", "UPDATE tool_calls SET command='wanted' WHERE id='call-0'")
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	raw, _ := json.Marshal(recordSearchSpec{Field: "command", Query: "wanted", Mode: "literal"})
	params := url.Values{"record_search": {string(raw)}}
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: qt.WireQuery{Select: []string{"id"}, Where: []qt.WhereTerm{{Field: "command", Op: "is_not_null"}}, Limit: 1}}
	_, _, err := server.executeQueryV3(t.Context(), "tool_calls", params, PharosToolCallsSchema(), q)
	var diagnostic *qt.PlanDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic.Code != qt.ErrorCodeResourceLimit {
		t.Fatalf("searched a partial eligible identity set: %v", err)
	}
	q.Result.Where = []qt.WhereTerm{{Field: "id", Op: "=", Value: "call-0"}}
	out, _, err := server.executeQueryV3(t.Context(), "tool_calls", params, PharosToolCallsSchema(), q)
	if err != nil || out.Total != 1 || len(out.Rows) != 1 || out.Rows[0]["id"] != "call-0" {
		t.Fatalf("narrowed keyword search: %+v %v", out, err)
	}
}

func TestQueryV3DistinctAndIdentityHTTP(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 5)
	publishProjections(t, catalog)
	server := testServer(t, config, catalog)
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Result: qt.WireQuery{Limit: 1}, Distinct: &qt.DistinctRequestV3{Field: "tool_name", Limit: 2}}
	response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	var out qt.QueryResultV3
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.Distinct == nil || len(out.Distinct.Values) == 0 {
		t.Fatal(response.Code, response.Body.String())
	}
	q.Source.Where = []qt.WhereTerm{{Field: "id", Op: "=", Value: "call-0"}}
	response = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.Distinct == nil || len(out.Distinct.Values) != 1 {
		t.Fatal("scoped distinct", response.Code, response.Body.String())
	}
	q.Distinct = nil
	q.Rows = true
	q.Result = qt.WireQuery{Select: []string{"id", "command"}, Where: []qt.WhereTerm{{Field: "id", Op: "=", Value: "call-0"}}, Limit: 2}
	response = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/tool_calls/query-v3", q)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.Total != 1 || len(out.Rows) != 1 || out.Rows[0]["id"] != "call-0" {
		t.Fatal(response.Code, response.Body.String())
	}
}

func ungroupedRowsRequest(now time.Time, where ...qt.WhereTerm) qt.QueryRequestV3 {
	return qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: now.UnixMilli(), Rows: true,
		Source: qt.WireQuery{Limit: 1}, Result: qt.WireQuery{Select: []string{"id"}, Where: where, Limit: 3}}
}

// rememberedCounts returns the cached counts, and with set replaces each
// with set(count), as a stale or wrong count would look.
func rememberedCounts(c *Catalog, set func(int64) int64) map[queryCountKey]int64 {
	c.queryCounts.mu.Lock()
	defer c.queryCounts.mu.Unlock()
	if set != nil {
		for key, n := range c.queryCounts.counts {
			c.queryCounts.counts[key] = set(n)
		}
	}
	return maps.Clone(c.queryCounts.counts)
}

func TestQueryV3ReusesPopulationCountsWithinAGeneration(t *testing.T) {
	c, config := testCatalog(t)
	seedToolCalls(t, c, 80)
	c.maintainQueryTextIndexes(t.Context())
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	noon := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	run := func(dataset string, q qt.QueryRequestV3) int64 {
		t.Helper()
		schema, _ := sqliteQuerySchema(dataset)
		result, _, err := server.executeQueryV3(t.Context(), dataset, nil, schema, q)
		if err != nil {
			t.Fatal(err)
		}
		if result.SourceTotal != result.Total {
			t.Fatal("ungrouped totals differ", result.Total, result.SourceTotal)
		}
		return result.Total
	}
	// The cube cannot answer a duration filter: it is counted, then reused.
	slow := ungroupedRowsRequest(noon, qt.WhereTerm{Field: "duration_ms", Op: ">", Value: "100"})
	total := run("tool_calls", slow)
	if total == 0 || total == 80 || len(rememberedCounts(c, nil)) != 1 {
		t.Fatalf("total %d, remembered %v", total, rememberedCounts(c, nil))
	}
	// A reused count is answered, not recounted, while the generation holds.
	// Other pages of the population share its count.
	rememberedCounts(c, func(n int64) int64 { return n + 1000 })
	reshaped := slow
	reshaped.Result.Limit, reshaped.Result.Offset = 10, 5
	if got := run("tool_calls", reshaped); got != total+1000 || len(rememberedCounts(c, nil)) != 1 {
		t.Fatalf("reshaped page counted %d, want the remembered %d", got, total+1000)
	}
	if stats := c.queryCounts.snapshot(); stats.Counted != 1 || stats.Reused != 1 || stats.Fast != 0 || stats.Remembered != 1 {
		t.Fatalf("tallies after one count and one reuse: %+v", stats)
	}
	// Datasets, filters and relative clocks keep their own counts.
	before := len(rememberedCounts(c, nil))
	run("skill_usages", ungroupedRowsRequest(noon))
	run("tool_calls", ungroupedRowsRequest(noon, qt.WhereTerm{Field: "duration_ms", Op: ">", Value: "200"}))
	recent := ungroupedRowsRequest(noon, qt.WhereTerm{Field: "started_at", Op: ">=", Value: "-20d"})
	run("tool_calls", recent)
	run("tool_calls", recent)
	if got := len(rememberedCounts(c, nil)); got != before+3 {
		t.Fatalf("remembered %d counts, want %d", got, before+3)
	}
	later := recent
	later.Now = noon.Add(time.Hour).UnixMilli()
	run("tool_calls", later)
	if got := len(rememberedCounts(c, nil)); got != before+4 {
		t.Fatalf("a later clock reused a relative count: %d counts", got)
	}
	// Tool calls read no label or agent-event table, so the passes that write
	// those leave its counts in place; a source that may read them recounts.
	if err := c.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	run("agent_events", ungroupedRowsRequest(noon))
	held := len(rememberedCounts(c, nil))
	execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('labelled','t')`)
	if got := run("tool_calls", slow); got != total+1000 {
		t.Fatalf("a label write recounted tool calls: %d, want the remembered %d", got, total+1000)
	}
	run("agent_events", ungroupedRowsRequest(noon))
	if got := len(rememberedCounts(c, nil)); got != held {
		t.Fatalf("after a label write: %d counts, want %d (tool calls kept, agent events replaced)", got, held)
	}
	if stats := c.queryCounts.snapshot(); stats.Generations != 1 {
		t.Fatalf("a label write should move only the whole generation's counts: %+v", stats)
	}
	// A write advances the generation inside the next request's transaction.
	if _, err := c.DB.Exec("DELETE FROM tool_calls WHERE id=(SELECT id FROM tool_calls WHERE duration_ms>100 LIMIT 1)"); err != nil {
		t.Fatal(err)
	}
	if got := run("tool_calls", slow); got != total-1 {
		t.Fatalf("after a write counted %d, want %d", got, total-1)
	}
	if c.queryCounts.snapshot().Generations == 0 {
		t.Fatal("a newer generation did not clear the remembered counts")
	}
	total--
	// Local days are not in the statement's text: a timezone change counts again.
	rememberedCounts(c, func(n int64) int64 { return n + 1000 })
	override(t, &time.Local, time.FixedZone("ahead", 14*3600))
	if got := run("tool_calls", slow); got != total {
		t.Fatalf("another timezone reused %d, want %d", got, total)
	}
	// Parity with uncached execution, after a write and with a cold cache.
	for _, q := range []qt.QueryRequestV3{slow, recent, later, ungroupedRowsRequest(noon)} {
		cached := run("tool_calls", q)
		c.queryCounts = queryCountCache{}
		if cold := run("tool_calls", q); cold != cached {
			t.Fatalf("%v: cached %d, cold %d", q.Result.Where, cached, cold)
		}
	}
	override(t, &queryCountLimit, 2)
	for _, value := range []string{"10", "20", "30"} {
		run("tool_calls", ungroupedRowsRequest(noon, qt.WhereTerm{Field: "duration_ms", Op: ">", Value: value}))
	}
	if got := len(rememberedCounts(c, nil)); got > 2 {
		t.Fatalf("remembered %d counts over a limit of 2", got)
	}
	w := apiCall(t, server, config.APIToken, http.MethodGet, "/api/diagnostics/query-counts", nil)
	var stats queryCountStats
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &stats) != nil || stats != c.queryCounts.snapshot() || stats.Counted == 0 || stats.Generation == 0 {
		t.Fatalf("query count diagnostics: %d %s", w.Code, w.Body.String())
	}
}

// A Tool calls page sorted newest first reads the time index and counts
// over the unordered source (newestFirstScan): the same rows and total as
// the page in any other order.
func TestQueryV3NewestFirstToolCallsMatchOldestFirst(t *testing.T) {
	c, config := testCatalog(t)
	seedToolCalls(t, c, 40)
	c.maintainQueryTextIndexes(t.Context())
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	noon := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	page := func(dir string, where ...qt.WhereTerm) ([]string, int64) {
		t.Helper()
		q := ungroupedRowsRequest(noon, where...)
		q.Result.Select, q.Result.Limit = []string{"id", "started_at", "conversation_id"}, 100
		q.Result.OrderBy = []qt.OrderBy{{Field: "started_at", Dir: dir}}
		schema, _ := sqliteQuerySchema("tool_calls")
		result, _, err := server.executeQueryV3(t.Context(), "tool_calls", nil, schema, q)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for i, row := range result.Rows {
			if i > 0 && firstString(row["started_at"]) != firstString(result.Rows[i-1]["started_at"]) &&
				(firstString(row["started_at"]) > firstString(result.Rows[i-1]["started_at"])) == (dir == "desc") {
				t.Fatalf("%s page out of order at row %d", dir, i)
			}
			ids = append(ids, firstString(row["id"]))
		}
		slices.Sort(ids)
		return ids, result.Total
	}
	for _, where := range [][]qt.WhereTerm{nil, {{Field: "started_at", Op: ">=", Value: "-30d"}}} {
		c.queryCounts = queryCountCache{}
		newest, total := page("desc", where...)
		// Unfiltered, the cube answers; bounded, the unordered source does.
		if stats := c.queryCounts.snapshot(); stats.Fast != 1 || stats.Counted != 0 {
			t.Fatalf("a newest-first page counted over its ordered source: %+v for %v", stats, where)
		}
		c.queryCounts = queryCountCache{}
		oldest, plain := page("asc", where...)
		if len(newest) == 0 || total != plain || !slices.Equal(newest, oldest) {
			t.Fatalf("newest first %d rows of %d, oldest first %d of %d, for %v", len(newest), total, len(oldest), plain, where)
		}
	}
}

func TestQueryV3ToolCubeCountsMatchTheCalls(t *testing.T) {
	slowTest(t)
	c, config := testCatalog(t)
	seedToolCalls(t, c, 200)
	c.maintainQueryTextIndexes(t.Context())
	if err := c.ensureToolRollup(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	schema, _ := sqliteQuerySchema("tool_calls")
	cube := toolCubeCount(schema)
	answer := func(where []qt.WhereTerm) (int64, bool) {
		t.Helper()
		tx, err := c.reader().BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		n, ok, err := cube(t.Context(), tx, qt.PopulationCount{Where: where})
		if err != nil {
			t.Fatal(err)
		}
		return n, ok
	}
	count := func(where []qt.WhereTerm) int64 {
		t.Helper()
		result, _, err := server.executeQueryV3(t.Context(), "tool_calls", nil, schema, ungroupedRowsRequest(time.Now(), where...))
		if err != nil {
			t.Fatal(where, err)
		}
		return result.Total
	}
	// Every identity is present, so this term keeps the population while
	// declining the cube: the executor counts the calls themselves.
	counted := qt.WhereTerm{Field: "id", Op: "is_not_null"}
	answered := [][]qt.WhereTerm{
		nil,
		{{Field: "tool_name", Op: "=", Value: "Bash"}},
		{{Field: "model", Op: "=", Value: "claude-opus-5-5"}, {Field: "effort", Op: "=", Value: "high"}},
		{{Field: "model_effort", Op: "=", Value: "claude-opus-5-5 · low"}},
		{{Field: "status", Op: "=", Value: "error"}, {Field: "repository_name", Op: "is_not_null"}},
		{{Field: "host", Op: "is_not_null"}},
		{{Field: "search_query", Op: "is_not_null"}},
		{{Field: "hosts", Op: "is_null"}},
		{{Field: "model", Op: "is_null"}},
		{{Field: "day", Op: "=", Value: "2026-09-01"}, {Field: "provider", Op: "=", Value: "codex"}},
		{{Field: "week", Op: "=", Value: "2026-08-31"}, {Field: "session_kind", Op: "=", Value: "subagent"}},
		{{Field: "command_name", Op: "=", Value: "git status"}},
	}
	for _, where := range answered {
		n, ok := answer(where)
		want := count(append(slices.Clone(where), counted))
		if !ok || n != want {
			t.Fatalf("cube answered %d (%v) where %v; the calls count %d", n, ok, where, want)
		}
		if got := count(where); got != want {
			t.Fatalf("query counted %d where %v, want %d", got, where, want)
		}
	}
	for _, where := range [][]qt.WhereTerm{
		{{Field: "command", Op: "contains", Value: "test"}},
		{{Field: "duration_ms", Op: ">", Value: "100"}},
		{{Field: "program", Op: "=", Value: "git", Negated: true}},
		{{Field: "tool_name", Op: "=", Value: ""}},
		{{Any: []qt.WhereClause{{Field: "tool_name", Op: "=", Value: "Bash"}, {Field: "tool_name", Op: "=", Value: "Read"}}}},
		{counted},
	} {
		if _, ok := answer(where); ok {
			t.Fatalf("cube answered a filter it does not keep: %v", where)
		}
	}
	// A ledger write leaves the cube stale until the rollup catches up.
	if _, err := c.DB.Exec("DELETE FROM tool_calls WHERE id='call-5'"); err != nil {
		t.Fatal(err)
	}
	if _, ok := answer(nil); ok {
		t.Fatal("a stale cube answered")
	}
	if got := count(nil); got != 199 {
		t.Fatalf("stale cube: counted %d calls, want 199", got)
	}
}
