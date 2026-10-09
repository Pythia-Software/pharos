package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func queryV2ReviewCall(t testing.TB, server *Server, token, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func TestQueryV2ComputedCatalogueRecovery(t *testing.T) {
	catalog, config := testCatalog(t)
	seedToolCalls(t, catalog, 3)
	server := NewServer(config, catalog)
	store := qt.SQLiteComputedColumnStore{DB: catalog.DB}
	columns := map[string]qt.ComputedColumn{}
	// Metadata left by an older schema can no longer compile. Include two
	// unrelated failures so the UI must be able to repair them incrementally.
	for id, source := range map[string]string{"good": "[duration_ms]/1000", "good_child": "[@computed/good]*2", "broken": "[removed_field]", "broken_child": "[@computed/broken]*2", "other": "[another_removed_field]"} {
		column, err := store.Save(context.Background(), queryTableScope, "pharos_tool_calls", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: id, Label: id, Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: source}}})
		if err != nil {
			t.Fatal(err)
		}
		columns[id] = column
	}
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		return queryV2ReviewCall(t, server, config.APIToken, method, path, value)
	}
	capabilities := func() (qt.SQLiteComputedExecution, map[string]*qt.PlanDiagnostic) {
		t.Helper()
		w := call(http.MethodGet, "/api/query/tool_calls/capabilities", nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct {
			Execution   qt.SQLiteComputedExecution    `json:"computedExecution"`
			Unavailable map[string]*qt.PlanDiagnostic `json:"computedDiagnostics"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Execution, response.Unavailable
	}
	execution, unavailable := capabilities()
	if len(execution.Fields) != 2 || len(execution.ResolvedRevisions) != 2 || len(unavailable) != 3 {
		t.Fatalf("partial handshake: %+v, %+v", execution, unavailable)
	}
	for _, id := range []string{"broken", "broken_child", "other"} {
		if unavailable[id] == nil || execution.ResolvedRevisions[id] != "" {
			t.Fatalf("invalid graph advertised: %s", id)
		}
	}
	w := call(http.MethodGet, "/api/query-computed-columns?dataset=pharos_tool_calls", nil)
	var listed []qt.ComputedColumn
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 5 {
		t.Fatal("repair catalogue lost definitions", w.Code, w.Body.String())
	}
	rows := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, ExpectedRevisions: execution.ResolvedRevisions, WireQuery: qt.WireQuery{Select: []string{"id", "@computed/good_child"}, Limit: 2}}
	if w = call(http.MethodPost, "/api/query/tool_calls/rows-v2", rows); w.Code != 200 {
		t.Fatal("valid rows blocked by unrelated formula", w.Code, w.Body.String())
	}
	save := func(id, source string) *httptest.ResponseRecorder {
		column := columns[id]
		column.Expression.Source = source
		w := call(http.MethodPut, "/api/query-computed-columns?dataset=pharos_tool_calls", qt.SaveComputedColumnRequest{Column: column, ExpectedRevision: &column.Revision})
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &column); err != nil {
				t.Fatal(err)
			}
			columns[id] = column
		}
		return w
	}
	// A valid replacement may not invalidate an already valid dependent.
	if w = save("good", `"text"`); w.Code != 400 {
		t.Fatal("new dependent failure committed", w.Code, w.Body.String())
	}
	execution, _ = capabilities()
	if execution.ResolvedRevisions["good"] != "1" {
		t.Fatal("rejected edit changed revision", execution)
	}
	if w = save("broken", "[duration_ms]*2"); w.Code != 200 {
		t.Fatal("unrelated invalid definition prevented repair", w.Code, w.Body.String())
	}
	execution, unavailable = capabilities()
	if len(unavailable) != 1 || unavailable["other"] == nil || execution.ResolvedRevisions["broken"] != "2" || !execution.Fields["@computed/broken_child"].Select {
		t.Fatalf("repair did not restore dependent graph: %+v, %+v", execution, unavailable)
	}
	metrics := metricRequest(qt.AggSpec{ID: "restored", Expression: "SUM([@computed/broken_child])"})
	metrics.ExpectedRevisions = execution.ResolvedRevisions
	if w = call(http.MethodPost, "/api/query/tool_calls/metrics", metrics); w.Code != 200 {
		t.Fatal("restored graph unusable", w.Code, w.Body.String())
	}
	if w = save("other", "[missing_again]"); w.Code != 400 {
		t.Fatal("repair accepted another invalid formula", w.Code, w.Body.String())
	}
	if w = save("other", "[duration_ms]"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, unavailable = capabilities()
	if len(unavailable) != 0 {
		t.Fatal("repaired definitions still unavailable", unavailable)
	}
}

func TestQueryV2LibraryCompactRequests(t *testing.T) {
	catalog, config := libraryFixture(t)
	catalog.DB.SetMaxOpenConns(1)
	server := NewServer(config, catalog)
	rowRequest := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"title"}, Limit: 1}}
	w := queryV2ReviewCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/rows-v2", rowRequest)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page qt.SQLiteRowsV2Result
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Rows) != 1 {
		t.Fatal("page", err, w.Body.String())
	}
	for _, name := range []string{"purpose", "outcome", "pr_details", "cost_usd", "first_input"} {
		if _, found := page.Rows[0][name]; !found {
			t.Fatalf("page enrichment lost %s", name)
		}
	}
	w = queryV2ReviewCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/metrics", metricRequest(qt.AggSpec{ID: "count", Expression: "COUNT()"}))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, row := range catalog.library.rows {
		for _, name := range append(slices.Clone(libraryLargeFields), libraryCostFields...) {
			if _, found := row[name]; found {
				t.Fatalf("compact v2 request loaded %s for the whole population", name)
			}
		}
	}
}

func TestQueryV2LibraryDependencies(t *testing.T) {
	catalog, _ := libraryFixture(t)
	schema, _ := sqliteQuerySchema("library")
	store := qt.SQLiteComputedColumnStore{DB: catalog.DB}
	revisions := map[string]string{}
	for id, source := range map[string]string{"text": "COALESCE([purpose],[outcome])", "nested": "[@computed/text]", "number": "[token_count]*2"} {
		column, err := store.Save(context.Background(), queryTableScope, schema.Name, qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: id, Label: id, Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: source}}})
		if err != nil {
			t.Fatal(err)
		}
		revisions[id] = column.Revision
	}
	// Keep a wide cache: staging must still omit unused values from these maps.
	wide, err := catalog.searchRows(SearchOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rowQuery := func(selects []string, where []qt.WhereTerm, order qt.OrderBys) *qt.ServerQueryV2 {
		return &qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, ExpectedRevisions: revisions, WireQuery: qt.WireQuery{Select: selects, Where: where, OrderBy: order, Limit: 2}}
	}
	metricQuery := func(spec qt.AggSpec, where []qt.WhereTerm, order []qt.OrderBy) *qt.MetricQuery {
		q := metricRequest(spec)
		q.ExpectedRevisions, q.Where, q.OrderBy = revisions, where, order
		return &q
	}
	for _, sample := range []struct {
		name    string
		rows    *qt.ServerQueryV2
		metrics *qt.MetricQuery
		fields  []string
	}{
		{"default row order", rowQuery([]string{"title"}, nil, nil), nil, []string{"activity_at", "id", "title"}},
		{"OR filter and cost sort", rowQuery([]string{"outcome"}, []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "purpose", Op: "contains", Value: "purpose"}, {Field: "changed_files", Op: "contains", Value: "parser"}}}}, qt.OrderBys{{Field: "cost_usd", Dir: "desc"}}), nil, []string{"changed_files", "cost_usd", "id", "outcome", "purpose"}},
		{"transitive computed select and sort", rowQuery([]string{"@computed/number"}, nil, qt.OrderBys{{Field: "@computed/nested", Dir: "asc"}}), nil, []string{"id", "outcome", "purpose", "token_count"}},
		{"ratio and grouping", nil, metricQuery(qt.AggSpec{ID: "ratio", Expression: "SUM([token_count])/NULLIF(SUM([tool_use_count]),0)", GroupBy: []string{"source_kind"}}, []qt.WhereTerm{{Field: "purpose", Op: "contains", Value: "purpose"}}, nil), []string{"id", "purpose", "source_kind", "token_count", "tool_use_count"}},
		{"paired shown and computed grouping", nil, metricQuery(qt.AggSpec{ID: "paired", Expression: "AVG([token_count])", ExpressionY: "SUM([cost_usd])", GroupBy: []string{"@computed/number"}, Scope: "shownRows"}, nil, []qt.OrderBy{{Field: "@computed/nested", Dir: "desc"}}), []string{"cost_usd", "id", "outcome", "purpose", "token_count"}},
		{"distribution input", nil, metricQuery(qt.AggSpec{ID: "hist", Distribution: &qt.MetricDistribution{Kind: "histogram", Input: `IF([purpose]="Summarized purpose",[token_count],0)`, Bins: 4}, GroupBy: []string{"source_kind"}, Scope: "shownRows"}, nil, nil), []string{"activity_at", "id", "purpose", "source_kind", "token_count"}},
		{"count", nil, metricQuery(qt.AggSpec{ID: "count", Expression: "COUNT()"}, nil, nil), []string{"id"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			clock := time.Now()
			fields, err := queryV2LibraryFields(context.Background(), catalog.DB, schema, sample.rows, sample.metrics, clock)
			if err != nil || !reflect.DeepEqual(fields, sample.fields) {
				t.Fatalf("fields=%v, want=%v, err=%v", fields, sample.fields, err)
			}
			tx, release, err := beginQueryV2(context.Background(), catalog.DB)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			d := queryV2Dataset("library", schema)
			d.ComputedGroupable = map[string]bool{"text": true, "nested": true, "number": true}
			d.SourceSQL, err = stageQueryRows(context.Background(), tx, "wide_rows", wide, schema)
			if err != nil {
				t.Fatal(err)
			}
			full, err := d.ExecuteV2In(context.Background(), tx, sample.rows, sample.metrics, qt.PlanOptions{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			d.SourceSQL, err = stageQueryRows(context.Background(), tx, "narrow_rows", wide, schema, fields)
			if err != nil {
				t.Fatal(err)
			}
			columns, err := tx.Query("SELECT name FROM pragma_table_info('narrow_rows') ORDER BY name")
			if err != nil {
				t.Fatal(err)
			}
			var staged []string
			for columns.Next() {
				var name string
				if err := columns.Scan(&name); err != nil {
					t.Fatal(err)
				}
				staged = append(staged, name)
			}
			if err := columns.Err(); err != nil {
				t.Fatal(err)
			}
			columns.Close()
			if !reflect.DeepEqual(staged, fields) {
				t.Fatalf("staged unused values: %v, want %v", staged, fields)
			}
			narrow, err := d.ExecuteV2In(context.Background(), tx, sample.rows, sample.metrics, qt.PlanOptions{Now: clock})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(full.Metrics, narrow.Metrics) || full.Rows != nil && (full.Rows.Total != narrow.Rows.Total || !reflect.DeepEqual(full.Rows.Rows, narrow.Rows.Rows) || !reflect.DeepEqual(full.Rows.Computed, narrow.Rows.Computed)) {
				t.Fatalf("selective staging changed results: full=%+v narrow=%+v", full, narrow)
			}
		})
	}
}

func BenchmarkQueryV2LibraryStaging(b *testing.B) {
	catalog, _ := testCatalog(b)
	schema, _ := sqliteQuerySchema("library")
	rows := make([]map[string]any, 10_000)
	large := strings.Repeat("long library text ", 512)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprint(i), "title": "Work", "activity_at": "2026-10-08T00:00:00Z", "purpose": large, "outcome": large, "changed_files": large}
	}
	for _, selective := range []bool{false, true} {
		b.Run(fmt.Sprintf("selective_%t", selective), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tx, release, err := beginQueryV2(context.Background(), catalog.DB)
				if err != nil {
					b.Fatal(err)
				}
				var fields [][]string
				if selective {
					fields = [][]string{{"id", "title", "activity_at"}}
				}
				_, err = stageQueryRows(context.Background(), tx, "library_bench", rows, schema, fields...)
				releaseError := release()
				if err != nil || releaseError != nil {
					b.Fatal(err, releaseError)
				}
			}
		})
	}
}
