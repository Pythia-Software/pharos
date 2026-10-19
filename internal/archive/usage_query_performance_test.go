package archive

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func TestUsageQueriesLoadLineageOnlyWhenConsumed(t *testing.T) {
	c, _ := testCatalog(t)
	ingestUsageFixture(t, c)
	s := &Server{Catalog: c}
	ctx := context.Background()
	base, err := s.queryTableRows(ctx, "usage", nil, queryFieldsFor("usage", "provider", "total_tokens"))
	if err != nil || len(base) == 0 {
		t.Fatal(err)
	}
	if _, present := base[0]["root_conversation_id"]; present {
		t.Fatal("ordinary token queries resolved the full lineage graph")
	}
	full, err := s.queryTableRows(ctx, "usage", nil, queryFieldsFor("usage", "root_conversation_id", "total_tokens"))
	if err != nil || full[0]["root_conversation_id"] == nil {
		t.Fatal("root queries lost attribution", err)
	}
	if _, present := base[0]["root_conversation_id"]; present {
		t.Fatal("lineage enrichment mutated shared base facts")
	}
	for i, row := range base {
		if row["total_tokens"] != full[i]["total_tokens"] {
			t.Fatal("enrichment changed token totals")
		}
	}
}

func TestLegacyUsageRowsIncludeSelectedLineage(t *testing.T) {
	c, cfg := testCatalog(t)
	ingestUsageFixture(t, c)
	server := testServer(t, cfg, c)
	full, err := c.localUsageRows(context.Background())
	if err != nil || len(full) == 0 {
		t.Fatal("missing usage fixture", err)
	}
	want := map[string]map[string]any{}
	for _, row := range full {
		if row["root_conversation_id"] == nil || row["run_role"] == nil {
			t.Fatal("fixture must exercise populated lineage", row)
		}
		want[firstString(row["id"])] = row
	}
	selected := []string{"id", "root_conversation_id", "run_role", "session_role", "delegated_session_id"}
	for _, tc := range []struct {
		name         string
		selectFields []string
		where        []querytable.WhereTerm
		order        []querytable.OrderBy
	}{
		{"filtered", selected, []querytable.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}, nil},
		{"sorted", selected, nil, []querytable.OrderBy{{Field: "total_tokens", Dir: "desc"}}},
		{"filtered-and-sorted", selected, []querytable.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}, []querytable.OrderBy{{Field: "total_tokens", Dir: "desc"}}},
		{"complete-filtered-rows", nil, []querytable.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(querytable.Query{Select: tc.selectFields, Where: tc.where, OrderBy: tc.order, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			response := queryExplorationCall(server, cfg.APIToken, "POST", "/api/query/usage", string(body))
			var result querytable.Result
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Rows) != len(full) {
				t.Fatal(response.Code, response.Body.String())
			}
			for _, row := range result.Rows {
				for _, field := range selected {
					value, present := row[field]
					if !present || !reflect.DeepEqual(value, want[firstString(row["id"])][field]) {
						t.Fatalf("selected lineage %s missing or changed: %#v", field, row)
					}
				}
			}
		})
	}
}

func TestMachineTimeProjectionMatchesCubeAndInvalidatesOnPublication(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 80)
	if _, err := c.DB.Exec(`UPDATE tool_calls SET program=' '||char(10)||'git',model=' null ',mcp_server=' null ' WHERE id='call-0';
 UPDATE tool_calls SET program='null',subcommand=' status ' WHERE id='call-1'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	schema, err := (&Server{Catalog: c}).queryTableSchema("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	for _, groups := range [][]string{{}, {"repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}, {"provider", "model_family", "month"}} {
		for _, where := range [][]querytable.WhereTerm{nil, {{Field: "provider", Op: "=", Value: "codex"}}, {{Field: "provider", Op: "=", Value: "absent"}}, {
			{Field: "started_at", Op: ">=", Value: time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)},
			{Field: "started_at", Op: "<", Value: time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)},
		}} {
			request := querytable.AggregationRequest{Where: where, RequireComplete: true, Aggregations: []querytable.Aggregation{
				{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: groups},
				{ID: "timed", Op: "count", Field: "duration_ms", GroupBy: groups},
				{ID: "calls", Op: "count", GroupBy: groups},
			}}
			want, err := toolCallDataset.Aggregate(ctx, c.reader(), request, schema)
			if err != nil {
				t.Fatal(err)
			}
			got, handled, err := c.machineTimeAggregations(ctx, request, schema)
			// The legacy SQL post-sort interleaves NULL sums by key, without a
			// stable total ordering. Compare the complete typed bucket population.
			for _, result := range []*querytable.AggregationResult{&want, &got} {
				for i := range result.Metrics {
					sort.Slice(result.Metrics[i].Buckets, func(a, b int) bool {
						left, _ := json.Marshal(result.Metrics[i].Buckets[a].Keys)
						right, _ := json.Marshal(result.Metrics[i].Buckets[b].Keys)
						return string(left) < string(right)
					})
				}
			}
			left, _ := json.Marshal(want)
			right, _ := json.Marshal(got)
			if err != nil || !handled || string(left) != string(right) {
				t.Fatalf("projection changed %v/%v: %v", groups, where, err)
			}
		}
	}
	for _, request := range []querytable.AggregationRequest{
		{Aggregations: []querytable.Aggregation{{ID: "ids", Op: "count", GroupBy: []string{"id"}}}},
		{Where: []querytable.WhereTerm{{Field: "duration_ms", Op: ">", Value: "100"}}, Aggregations: []querytable.Aggregation{{ID: "time", Op: "sum", Field: "duration_ms"}}},
		{Where: []querytable.WhereTerm{{Field: "started_at", Op: ">=", Value: time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local).Format(time.RFC3339)}, {Field: "started_at", Op: "<", Value: time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)}}, Aggregations: []querytable.Aggregation{{ID: "calls", Op: "count"}}},
	} {
		if _, handled, err := c.machineTimeAggregations(ctx, request, schema); handled || err != nil {
			t.Fatal("raw-call query incorrectly used grouped facts", handled, err)
		}
	}
	// /metrics answers the same shapes from the projection exactly as the
	// generic executor answers them from the calls, in the same order.
	v2Schema := PharosToolCallsSchema()
	generic := queryV2Dataset("tool_calls", v2Schema)
	generic.SourceSQL = toolCallDataset.v2Source(v2Schema)
	accelerated := func(q qt.MetricQuery) (qt.SQLiteV2ExecutionResult, bool, error) {
		tx, release, err := beginQueryV2(ctx, c.reader())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		return c.machineTimeMetricsV2(ctx, tx, queryV2Dataset("tool_calls", v2Schema), q, qt.PlanOptions{Now: time.Now()})
	}
	midnight := func(year int, month time.Month, day int) string {
		return time.Date(year, month, day, 0, 0, 0, 0, time.Local).Format(time.RFC3339)
	}
	for _, groups := range [][]string{{}, {"repository_name", "activity", "day", "status", "operation_key", "operation_name", "tool_name", "mcp_server"},
		{"repository_name", "activity", "month", "status", "operation_key", "operation_name", "tool_name", "mcp_server"}, {"provider", "model_family", "month"},
		{"model", "effort", "week"}, {"model_effort", "program", "subcommand", "tool_category", "command_category"}} {
		for _, where := range [][]qt.WhereTerm{nil, {{Field: "provider", Op: "=", Value: "codex"}}, {{Field: "provider", Op: "=", Value: "absent"}},
			{{Field: "started_at", Op: ">=", Value: midnight(2026, 1, 1)}, {Field: "started_at", Op: "<", Value: midnight(2027, 1, 1)}},
			{{Field: "started_at", Op: ">=", Value: midnight(2026, 8, 31)}, {Field: "started_at", Op: "<", Value: midnight(2026, 9, 2)}, {Field: "status", Op: "=", Value: "ok"}},
			{{Any: []qt.WhereClause{{Field: "day", Op: "=", Value: "2026-08-31"}, {Field: "day", Op: "=", Value: "2026-09-02"}}}},
			{{Any: []qt.WhereClause{{Field: "repository_name", Op: "is_null"}, {Field: "repository_name", Op: "="}}}},
			{{Field: "repository_name", Op: "="}}, {{Field: "repository_name", Op: "!="}}, {{Field: "mcp_server", Op: "!=", Value: "pharos"}},
			{{Field: "tool_name", Op: "=", Value: "Bash", Negated: true}, {Field: "program", Op: "is_not_null"}},
			{{Field: "model", Op: "is_null"}}, {{Field: "activity", Op: "=", Value: "git"}, {Field: "operation_name", Op: "!=", Value: "git status"}},
		} {
			q := metricRequest(qt.AggSpec{ID: "time", Op: "sum", Field: "duration_ms", GroupBy: groups}, qt.AggSpec{ID: "timed", Op: "count", Field: "duration_ms", GroupBy: groups},
				qt.AggSpec{ID: "calls", Op: "count", GroupBy: groups}, qt.AggSpec{ID: "population", Op: "count"})
			q.Where = where
			got, handled, err := accelerated(q)
			if err != nil || !handled {
				t.Fatalf("accelerator declined %v/%v: %v", groups, where, err)
			}
			want, err := generic.ExecuteV2(ctx, c.reader(), nil, &q, qt.PlanOptions{})
			if err != nil || !same(got, want) {
				left, _ := json.Marshal(got)
				right, _ := json.Marshal(want)
				t.Fatalf("accelerated metrics differ for %v/%v: %v\n got %s\nwant %s", groups, where, err, left, right)
			}
		}
	}
	for _, q := range []qt.MetricQuery{
		metricRequest(qt.AggSpec{ID: "ids", Op: "count", GroupBy: []string{"id"}}),
		metricRequest(qt.AggSpec{ID: "tokens", Op: "sum", Field: "result_tokens"}),
		metricRequest(qt.AggSpec{ID: "formula", Expression: "COUNT()", GroupBy: []string{"day"}}),
		metricRequest(qt.AggSpec{ID: "top", Op: "count", GroupBy: []string{"tool_name"}, GroupLimit: 2}),
		metricRequest(qt.AggSpec{ID: "shown", Op: "count", GroupBy: []string{"tool_name"}, Scope: "shownRows"}),
		metricRequest(qt.AggSpec{ID: "filtered", Op: "count", GroupBy: []string{"tool_name"}, Where: []qt.WhereTerm{{Field: "status", Op: "=", Value: "ok"}}}),
		{Version: 2, Limit: 50, Where: []qt.WhereTerm{{Field: "duration_ms", Op: ">", Value: "100"}}, Metrics: []qt.AggSpec{{ID: "time", Op: "sum", Field: "duration_ms"}}},
		{Version: 2, Limit: 50, Where: []qt.WhereTerm{{Field: "tool_name", Op: "contains", Value: "Ba"}}, Metrics: []qt.AggSpec{{ID: "calls", Op: "count"}}},
		{Version: 2, Limit: 50, Where: []qt.WhereTerm{{Field: "started_at", Op: ">=", Value: time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local).Format(time.RFC3339)}, {Field: "started_at", Op: "<", Value: midnight(2027, 1, 1)}}, Metrics: []qt.AggSpec{{ID: "calls", Op: "count"}}},
		{Version: 2, Limit: 50, Where: []qt.WhereTerm{{Field: "started_at", Op: ">=", Value: midnight(2026, 1, 1)}}, Metrics: []qt.AggSpec{{ID: "calls", Op: "count"}}},
	} {
		if _, handled, err := accelerated(q); handled || err != nil {
			t.Fatal("accelerator answered a raw-call shape", q, handled, err)
		}
	}
	projection := func() []map[string]any {
		t.Helper()
		tx, err := c.reader().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		rows, err := c.machineTimeRowsIn(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := projection()
	if _, err = c.DB.Exec(`UPDATE tool_call_cube SET duration_ms_sum=123456; INSERT INTO meta(key,value) VALUES('tool_rollup_publication','next-publication') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		t.Fatal(err)
	}
	if after := projection(); reflect.DeepEqual(before, after) {
		t.Fatal("cached facts survived a new cube publication")
	}
	c.derived.mu.Lock()
	defer c.derived.mu.Unlock()
	count := 0
	for key := range c.derived.entries {
		if len(key) >= 13 && key[:13] == "machine-time:" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("old cube projections retained", count)
	}
}

func TestMachineTimeTimestampRangesRequireFreshCube(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE meta SET value='older/" + toolRollupVersion + "' WHERE key='tool_rollup_generation'",
		"UPDATE meta SET value='different' WHERE key='tool_rollup_built_full_revision'",
		"UPDATE meta SET value='1900-01-01' WHERE key='tool_rollup_day'",
		"UPDATE meta SET value='different' WHERE key='tool_rollup_timezone'",
		"INSERT INTO tool_rollup_dirty(workspace_id,revision) VALUES('pending','test')",
	} {
		t.Run(mutation, func(t *testing.T) {
			c, _ := testCatalog(t)
			seedToolCalls(t, c, 4)
			ctx := context.Background()
			if err := c.ensureToolRollup(ctx); err != nil {
				t.Fatal(err)
			}
			where := []qt.WhereTerm{
				{Field: "started_at", Op: ">=", Value: time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)},
				{Field: "started_at", Op: "<", Value: time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)},
			}
			accelerated := func(where []qt.WhereTerm) bool {
				t.Helper()
				tx, release, err := beginQueryV2(ctx, c.reader())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				q := metricRequest(qt.AggSpec{ID: "calls", Op: "count", GroupBy: []string{"tool_name"}})
				q.Where = where
				_, handled, err := c.machineTimeMetricsV2(ctx, tx, queryV2Dataset("tool_calls", PharosToolCallsSchema()), q, qt.PlanOptions{Now: time.Now()})
				if err != nil {
					t.Fatal(err)
				}
				return handled
			}
			rangeCount := func() (int64, bool) {
				t.Helper()
				tx, err := c.reader().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				total, fresh, err := c.freshToolCubeRangeCount(ctx, tx, where)
				if err != nil {
					t.Fatal(err)
				}
				return total, fresh
			}
			// Warm the projection before invalidation: cached facts cannot bypass guards.
			if !accelerated(where) {
				t.Fatal("fresh cube declined a whole-day range")
			}
			if total, fresh := rangeCount(); !fresh || total != 4 {
				t.Fatal("range count", total, fresh)
			}
			if _, err := c.DB.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if accelerated(where) {
				t.Fatal("stale cube used for raw timestamp range")
			}
			if _, fresh := rangeCount(); fresh {
				t.Fatal("stale range count")
			}
			if !accelerated(nil) {
				t.Fatal("published cube queries changed")
			}
		})
	}
}

func TestUsageLongMessagePresetKeepsBoundedTextAndFullFilters(t *testing.T) {
	c, cfg := testCatalog(t)
	text := strings.Repeat("long pasted input ", 10000) + " rare-tail-match"
	record := WorkspaceRecord{SourceID: "long-message", SourceKind: "codex", Account: "local", Title: "Long message", Conversations: []ConversationRecord{{NativeID: "long-message", Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{{NativeID: "long-message", Role: "user", Kind: "message", Text: text, CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true}}}}}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = c.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, cfg, c)
	// Human Words' message mix: a long message is counted, and its words are
	// all in its categories.
	for _, sample := range []struct {
		where    []qt.WhereTerm
		messages float64
	}{{nil, 1}, {[]qt.WhereTerm{{Field: "provider", Op: "=", Value: "missing"}}, 0}, {[]qt.WhereTerm{{Field: "provider", Op: "=", Value: "codex"}}, 1}} {
		mix := metricRequest(qt.AggSpec{ID: "messages", Op: "count"}, qt.AggSpec{ID: "words", Op: "sum", Field: "words"})
		for _, category := range authorshipCategories {
			mix.Metrics = append(mix.Metrics, qt.AggSpec{ID: category, Op: "sum", Field: category + "_words"})
		}
		mix.Where = sample.where
		result, err := s.executeQueryV2(context.Background(), "writing_messages", nil, PharosWritingMessagesSchema(), nil, &mix)
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]float64{}
		for _, metric := range result.Metrics.Metrics {
			value, _ := metric.Buckets[0].Value.(float64)
			if metric.Error != "" || len(metric.Buckets) != 1 || metric.Buckets[0].Error != "" {
				t.Fatalf("%v: %+v", sample.where, metric)
			}
			values[metric.ID] = value
		}
		categorized := 0.0
		for _, category := range authorshipCategories {
			categorized += values[category]
		}
		if values["messages"] != sample.messages || categorized != values["words"] || sample.messages > 0 && values["words"] < 30_000 {
			t.Fatalf("message mix for %v: %v", sample.where, values)
		}
	}

	q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "text", "typed_words"}, Where: []qt.WhereTerm{{Field: "text", Op: "contains", Value: "rare-tail-match"}}, OrderBy: qt.OrderBys{{Field: "typed_share", Dir: "asc"}, {Field: "words", Dir: "desc"}}, Limit: 50}}
	got, err := s.executeQueryV2(context.Background(), "writing_messages", nil, PharosWritingMessagesSchema(), &q, nil)
	if err != nil || len(got.Rows.Rows) != 1 {
		t.Fatal("long selected text rejected", err)
	}
	if len(firstString(got.Rows.Rows[0]["text"])) > authoredTextLimit || got.Rows.Rows[0]["text_truncated"] != true {
		t.Fatal("page text was not bounded", got.Rows.Rows[0]["text_truncated"])
	}
}

func TestUsageFusedSummariesMatchIndependentMetrics(t *testing.T) {
	c, cfg := testCatalog(t)
	ingestUsageFixture(t, c)
	s := testServer(t, cfg, c)
	summaries := []qt.SummaryColumn{
		{ID: "tokens", Op: "sum", Field: "total_tokens"},
		{ID: "sessions", Op: "count_distinct", Field: "agent_session_id"},
		{ID: "cache", Op: "formula", Expression: "100*SUM([cache_read_input_tokens])/NULLIF(SUM([cache_read_input_tokens])+SUM([uncached_input_tokens]),0)"},
		{ID: "lazy", Op: "formula", Expression: "IF(TRUE,SUM([total_tokens]),SUM([total_tokens]/0))"},
		{ID: "maximum", Op: "max", Field: "last_usage_at"},
	}
	q := groupRequest("provider", summaries...)
	ctx := context.Background()
	got, _, err := s.executeQueryV3(ctx, "usage", nil, PharosUsageSchema(), q)
	if err != nil || len(got.Rows) != 1 {
		t.Fatal(err, got)
	}
	for _, summary := range summaries {
		spec := qt.AggSpec{ID: summary.ID, Op: summary.Op, Field: summary.Field, GroupBy: []string{"provider"}}
		if summary.Op == "formula" {
			spec.Expression = summary.Expression
			spec.Op = ""
		}
		metric := metricRequest(spec)
		want, err := s.executeQueryV2(ctx, "usage", nil, PharosUsageSchema(), nil, &metric)
		if err != nil {
			t.Fatal(summary.ID, err)
		}
		if !reflect.DeepEqual(got.Rows[0][summary.ID], want.Metrics.Metrics[0].Buckets[0].Value) {
			t.Fatal(summary.ID, got.Rows[0][summary.ID], want.Metrics.Metrics[0].Buckets[0].Value)
		}
	}
}

func TestMachineTimeCallDrilldownIndexParity(t *testing.T) {
	c, cfg := testCatalog(t)
	seedToolCalls(t, c, 80)
	if err := c.ensureToolRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	schema, err := (&Server{Catalog: c}).queryTableSchema("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	native, err := sqliteQuerySchema("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t, cfg, c)
	for _, term := range []querytable.WhereTerm{
		{Field: "activity", Op: "=", Value: "read"},
		{Field: "activity", Op: "=", Value: "missing"},
		{Field: "operation_key", Op: "=", Value: machineOperationKey("exec_command", "", "git", "status")},
	} {
		where := []querytable.WhereTerm{term}
		want, err := toolCallDataset.Rows(context.Background(), c.reader(), querytable.Query{Select: []string{"id"}, Where: where, OrderBy: []querytable.OrderBy{{Field: "started_at", Dir: "desc", Nulls: "last"}}, Limit: 9, Offset: 3}, schema)
		if err != nil {
			t.Fatal(err)
		}
		q := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id"}, Where: where, OrderBy: qt.OrderBys{{Field: "started_at", Dir: "desc", Nulls: "last"}}, Limit: 9, Offset: 3}}
		got, err := s.executeQueryV2(context.Background(), "tool_calls", nil, native, &q, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids := func(rows []map[string]any) []string {
			out := []string{}
			for _, row := range rows {
				out = append(out, firstString(row["id"]))
			}
			sort.Strings(out)
			return out
		}
		if got.Rows.Total != int64(want.Total) || !reflect.DeepEqual(ids(got.Rows.Rows), ids(want.Rows)) {
			t.Fatal("drilldown changed", term, want, got.Rows)
		}
	}
}
