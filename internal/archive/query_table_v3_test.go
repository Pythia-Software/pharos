package archive

import (
	"context"
	"encoding/json"
	"fmt"
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
	publishProjections(t, catalog)
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
			q.Result.OrderBy = qt.OrderBys{{Field: "n", Dir: "desc"}, {Field: key, Dir: "asc"}}
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
	if w.Code != 400 || !strings.Contains(w.Body.String(), "definition_changed") {
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
			Execution qt.SQLiteComputedExecution `json:"computedExecution"`
			Query     qt.QueryCapabilitiesV3     `json:"queryCapabilities"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &capabilities) != nil || capabilities.Query.Version != 3 || !capabilities.Execution.Fields["@computed/tokens"].Filter {
			t.Fatal(w.Code, w.Body.String())
		}
		q := qt.QueryRequestV3{Version: 3, Profile: capabilities.Execution.Profile, Now: time.Now().UnixMilli(), Rows: true,
			ExpectedRevisions: capabilities.Execution.ResolvedRevisions, PlanToken: capabilities.Execution.PlanToken,
			Result: qt.WireQuery{Select: []string{"id"}, Where: []qt.WhereTerm{{Field: "@computed/tokens", Op: ">", Value: "200"}},
				OrderBy: qt.OrderBys{{Field: "@computed/tokens", Dir: "desc"}}, Limit: 1, Offset: 1}}
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
		if w.Code != 400 || !strings.Contains(w.Body.String(), "definition_changed") {
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
		Result:  qt.WireQuery{Select: []string{"id", "tool_name"}, OrderBy: qt.OrderBys{{Field: "id", Dir: "asc"}}, Limit: 50},
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
		response := apiCall(t, s, config.APIToken, http.MethodPost, path+"?"+search.Encode(), map[string]any{})
		if response.Code != 400 || !strings.Contains(response.Body.String(), "requires query-v3") {
			t.Fatalf("older protocol ignored keywords: %d %s", response.Code, response.Body.String())
		}
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
