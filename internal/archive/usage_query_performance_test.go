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
	before, err := c.machineTimeRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE tool_call_cube SET duration_ms_sum=123456; INSERT INTO meta(key,value) VALUES('tool_rollup_publication','next-publication') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		t.Fatal(err)
	}
	after, err := c.machineTimeRows(ctx)
	if err != nil || reflect.DeepEqual(before, after) {
		t.Fatal("cached facts survived a new cube publication", err)
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
			schema, err := (&Server{Catalog: c}).queryTableSchema("tool_calls")
			if err != nil {
				t.Fatal(err)
			}
			request := querytable.AggregationRequest{Where: []querytable.WhereTerm{
				{Field: "started_at", Op: ">=", Value: time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)},
				{Field: "started_at", Op: "<", Value: time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local).Format(time.RFC3339)},
			}, Aggregations: []querytable.Aggregation{{ID: "calls", Op: "count"}}}
			// Warm the projection before invalidation: cached facts cannot bypass guards.
			if _, handled, err := c.machineTimeAggregations(ctx, request, schema); !handled || err != nil {
				t.Fatal(handled, err)
			}
			tx, err := c.reader().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			total, fresh, err := c.freshToolCubeRangeCount(ctx, tx, request.Where)
			tx.Rollback()
			if err != nil || !fresh || total != 4 {
				t.Fatal("range count", total, fresh, err)
			}

			if _, err := c.DB.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, handled, err := c.machineTimeAggregations(ctx, request, schema); handled || err != nil {
				t.Fatal("stale cube used for raw timestamp range", handled, err)
			}
			tx, err = c.reader().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, fresh, err = c.freshToolCubeRangeCount(ctx, tx, request.Where)
			tx.Rollback()
			if err != nil || fresh {
				t.Fatal("stale range count", fresh, err)
			}

			request.Where = nil
			if _, handled, err := c.machineTimeAggregations(ctx, request, schema); !handled || err != nil {
				t.Fatal("published cube queries changed", handled, err)
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
	schema, err := (&Server{Catalog: c}).queryTableSchema("writing_messages")
	if err != nil {
		t.Fatal(err)
	}
	for _, where := range [][]querytable.WhereTerm{nil, {{Field: "provider", Op: "=", Value: "missing"}}, {{Field: "provider", Op: "=", Value: "codex"}}} {
		request := querytable.AggregationRequest{Where: where, Aggregations: []querytable.Aggregation{{ID: "messages", Op: "count"}, {ID: "text", Op: "count", Field: "text"}}}
		for _, category := range authorshipCategories {
			request.Aggregations = append(request.Aggregations, querytable.Aggregation{ID: category, Op: "sum", Field: category + "_words"})
		}
		want, err := writingMessageDataset.Aggregate(context.Background(), c.reader(), request, schema)
		if err != nil {
			t.Fatal(err)
		}
		got, handled, err := writingMessageDataset.writingMessageSummary(context.Background(), c.reader(), request, schema)
		if err != nil || !handled || !reflect.DeepEqual(want, got) {
			t.Fatalf("summary differs for %v: %v, want=%#v got=%#v", where, err, want, got)
		}
	}

	s := testServer(t, cfg, c)
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
