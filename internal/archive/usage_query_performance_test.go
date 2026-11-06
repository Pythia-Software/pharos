package archive

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

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
		result, err := s.executeQueryFixture(context.Background(), "writing_messages", nil, PharosWritingMessagesSchema(), nil, &mix)
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

	q := queryFixtureRows{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id", "text", "typed_words"}, Where: []qt.WhereTerm{{Field: "text", Op: "contains", Value: "rare-tail-match"}}, OrderBy: []qt.OrderBy{{Field: "typed_share", Dir: "asc"}, {Field: "words", Dir: "desc"}}, Limit: 50}}
	got, err := s.executeQueryFixture(context.Background(), "writing_messages", nil, PharosWritingMessagesSchema(), &q, nil)
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
		want, err := s.executeQueryFixture(ctx, "usage", nil, PharosUsageSchema(), nil, &metric)
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
		q := queryFixtureRows{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id"}, Where: where, OrderBy: []qt.OrderBy{{Field: "started_at", Dir: "desc", Nulls: "last"}}, Limit: 9, Offset: 3}}
		got, err := s.executeQueryFixture(context.Background(), "tool_calls", nil, native, &q, nil)
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
