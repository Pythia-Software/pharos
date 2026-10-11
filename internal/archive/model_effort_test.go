package archive

import (
	"context"
	"reflect"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func modelEffortFixture(t *testing.T) (*Catalog, Config, []string) {
	t.Helper()
	catalog, config := testCatalog(t)
	type setting struct{ model, effort string }
	opusLow := setting{"claude-opus-5-5", "low"}
	opusHigh := setting{"claude-opus-5-5", "high"}
	sonnetHigh := setting{"claude-sonnet-5-5", "high"}
	ids := []string{}
	for _, work := range []struct {
		name          string
		conversations [][]setting
	}{
		{"mixed-history", [][]setting{{opusLow, sonnetHigh}}},
		{"different-conversations", [][]setting{{opusLow}, {sonnetHigh}}},
		{"matching-pair", [][]setting{{opusHigh}}},
		{"unknown-effort", [][]setting{{{"claude-opus-5-5", ""}}}},
	} {
		record := WorkspaceRecord{SourceID: work.name, SourceKind: "canonical", Account: "local", Title: work.name}
		for index, settings := range work.conversations {
			native := work.name + firstString(index)
			last := settings[len(settings)-1]
			conversation := ConversationRecord{NativeID: native, Provider: "claude", Account: "local", Model: last.model, Effort: last.effort, StartedAt: "2026-09-20T12:00:00Z"}
			for turn, setting := range settings {
				id := native + firstString(turn)
				messages := []MessageRecord{
					{NativeID: id + ":request", Role: "assistant", Kind: "metadata", Text: jsonText(map[string]any{"type": "assistant", "message": map[string]any{"id": id, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}}})},
					skillTestCall(id+":call", "Read", map[string]any{"file_path": "/skills/pair/SKILL.md"}),
					skillTestResult(id+":call", "# Pair", false),
				}
				for i := range messages {
					messages[i].Model, messages[i].Effort = setting.model, setting.effort
					messages[i].CreatedAt = "2026-09-20T12:00:00Z"
				}
				conversation.Messages = append(conversation.Messages, messages...)
			}
			record.Conversations = append(record.Conversations, conversation)
		}
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = ingestWorkspace(tx, record, false)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := catalog.DB.QueryRow("SELECT id FROM workspaces WHERE source_id=?", work.name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return catalog, config, ids
}

func TestModelEffortSearchMatchesRecordedPairs(t *testing.T) {
	catalog, _, _ := modelEffortFixture(t)
	for _, fixture := range []struct {
		model, effort        string
		works, conversations int
	}{
		{"opus-5-5", " High ", 1, 1},
		{"opus-5-5", "low", 2, 2},
		{"opus-5-5", "", 4, 4},
		{"sonnet-5-5", "high", 2, 2},
		{"opus-5-5", "max", 0, 0},
	} {
		works, err := catalog.searchRows(SearchOptions{Model: fixture.model, Effort: fixture.effort}, nil)
		if err != nil || len(works) != fixture.works {
			t.Fatalf("work pair %q/%q: %v %v", fixture.model, fixture.effort, works, err)
		}
		conversations, err := catalog.searchConversations(map[string]any{"model": fixture.model, "effort": fixture.effort, "max_output_tokens": 4000})
		if err != nil || len(conversations["items"].([]map[string]any)) != fixture.conversations {
			t.Fatalf("conversation pair %q/%q: %v %v", fixture.model, fixture.effort, conversations, err)
		}
	}
	rows, err := catalog.cachedLibraryRows(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		pairs := firstString(row["model_efforts"])
		switch row["title"] {
		case "mixed-history", "different-conversations":
			if !strings.Contains(pairs, "claude-opus-5-5 · low") || !strings.Contains(pairs, "claude-sonnet-5-5 · high") || strings.Contains(pairs, "claude-opus-5-5 · high") {
				t.Fatalf("Library invented or lost a pair: %v", row)
			}
		case "unknown-effort":
			if pairs != "claude-opus-5-5 · Unknown effort" {
				t.Fatalf("Library invented an effort: %v", row)
			}
		}
	}
}

func TestModelEffortFilterSortAndGroupingAcrossDatasets(t *testing.T) {
	catalog, config, ids := modelEffortFixture(t)
	ctx := context.Background()
	if err := catalog.ensureToolRollup(ctx); err != nil {
		t.Fatal(err)
	}
	shared, _, err := catalog.sharedDatasets(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(config, catalog)
	for _, dataset := range []string{"usage", "tools", "tool_calls", "skill_usages"} {
		t.Run(dataset, func(t *testing.T) {
			rows := shared[dataset]
			if len(rows) < 4 {
				t.Fatalf("missing fixture or exported pairs: %v", rows)
			}
			document, err := querySchemaDocument(dataset)
			if err != nil {
				t.Fatal(err)
			}
			schema, err := querytable.LoadSchema(document)
			if err != nil {
				t.Fatal(err)
			}
			// Shares and the service support both independent keys and the combined key.
			for _, where := range [][]querytable.WhereTerm{
				{{Field: "model", Op: "=", Value: "claude-opus-5-5"}, {Field: "effort", Op: "=", Value: "high"}},
				{{Field: "model_effort", Op: "=", Value: "claude-opus-5-5 · high"}},
			} {
				query := querytable.Query{Where: where, OrderBy: []querytable.OrderBy{{Field: "model", Dir: "asc"}, {Field: "effort", Dir: "desc"}}, Limit: 50}
				result, err := querytable.Apply(rows, query, schema)
				if err != nil || len(result.Rows) != 1 || result.Rows[0]["model_effort"] != "claude-opus-5-5 · high" {
					t.Fatalf("share pair filter: %v %v", result, err)
				}
				if source, ok := sqlDatasetFor(dataset); ok {
					live, err := source.Rows(ctx, catalog.DB, query, schema)
					if err != nil || len(live.Rows) != 1 || live.Rows[0]["model_effort"] != "claude-opus-5-5 · high" {
						t.Fatalf("live pair filter: %v %v", live, err)
					}
				}
				v2Schema, err := sqliteQuerySchema(dataset)
				if err != nil {
					t.Fatal(err)
				}
				wire := qt.WireQuery{Select: []string{"id", "model", "effort", "model_effort"}, Limit: 50,
					OrderBy: qt.OrderBys{{Field: "model", Dir: "asc"}, {Field: "effort", Dir: "desc"}}}
				for _, term := range where {
					wire.Where = append(wire.Where, qt.WhereTerm{Field: term.Field, Op: term.Op, Value: term.Value})
				}
				v2 := qt.ServerQueryV2{WireQuery: wire, Version: 2, Profile: qt.SQLiteExpressionProfile}
				live, err := server.executeQueryV2(ctx, dataset, nil, v2Schema, &v2, nil)
				if err != nil || len(live.Rows.Rows) != 1 || live.Rows.Rows[0]["model_effort"] != "claude-opus-5-5 · high" {
					t.Fatalf("live V2 pair filter: %v %v", live, err)
				}
			}
			for _, grouping := range [][]string{{"model", "effort"}, {"model_effort"}} {
				request := querytable.AggregationRequest{Aggregations: []querytable.Aggregation{{ID: "pairs", Op: "count", GroupBy: grouping}}}
				result, err := querytable.Aggregate(rows, request, schema)
				if err != nil || len(result.Metrics[0].Buckets) != 4 {
					t.Fatalf("share pair grouping: %v %v", result, err)
				}
				v2Schema, err := sqliteQuerySchema(dataset)
				if err != nil {
					t.Fatal(err)
				}
				metrics := metricRequest(qt.AggSpec{ID: "pairs", Op: "count", GroupBy: grouping})
				live, err := server.executeQueryV2(ctx, dataset, nil, v2Schema, nil, &metrics)
				if err != nil || len(live.Metrics.Metrics[0].Buckets) != 4 {
					t.Fatalf("live V2 pair grouping: %v %v", live, err)
				}
			}
			for _, sort := range [][]querytable.OrderBy{
				{{Field: "model", Dir: "asc"}, {Field: "effort", Dir: "desc", Nulls: "last"}},
				{{Field: "model_effort", Dir: "asc"}},
			} {
				result, err := querytable.Apply(rows, querytable.Query{OrderBy: sort, Limit: 50}, schema)
				if err != nil {
					t.Fatal(err)
				}
				// Text effort order is lexical: low precedes high in descending order.
				if len(sort) == 2 && result.Rows[0]["model_effort"] != "claude-opus-5-5 · low" {
					t.Fatalf("joint sorting failed: %v", result.Rows)
				}
				if source, ok := sqlDatasetFor(dataset); ok {
					live, err := source.Rows(ctx, catalog.DB, querytable.Query{OrderBy: sort, Limit: 50}, schema)
					if err != nil || len(live.Rows) != len(result.Rows) {
						t.Fatalf("live pair sort: %v %v", live, err)
					}
					for i := range live.Rows {
						if live.Rows[i]["model_effort"] != result.Rows[i]["model_effort"] {
							t.Fatalf("pair sort disagrees with share: %v vs %v", live.Rows, result.Rows)
						}
					}
				}
				v2Schema, err := sqliteQuerySchema(dataset)
				if err != nil {
					t.Fatal(err)
				}
				wire := qt.WireQuery{Select: []string{"id", "model_effort"}, Limit: 50}
				for _, key := range sort {
					wire.OrderBy = append(wire.OrderBy, qt.OrderBy{Field: key.Field, Dir: key.Dir, Nulls: key.Nulls})
				}
				v2 := qt.ServerQueryV2{WireQuery: wire, Version: 2, Profile: qt.SQLiteExpressionProfile}
				live, err := server.executeQueryV2(ctx, dataset, nil, v2Schema, &v2, nil)
				if err != nil || len(live.Rows.Rows) != len(result.Rows) {
					t.Fatalf("live V2 pair sort: %v %v", live, err)
				}
				for i := range live.Rows.Rows {
					if live.Rows.Rows[i]["model_effort"] != result.Rows[i]["model_effort"] {
						t.Fatalf("V2 pair sort disagrees with share: %v vs %v", live.Rows.Rows, result.Rows)
					}
				}
			}
		})
	}
	// All six schemas expose the combined field for filters, sorting, and grouping.
	for _, dataset := range []string{"library", "usage", "tools", "tool_calls", "skill_usages", "tl1_attempts"} {
		document, _ := querySchemaDocument(dataset)
		schema, err := querytable.LoadSchema(document)
		if err != nil {
			t.Fatal(err)
		}
		field := "model_effort"
		if dataset == "library" {
			field = "model_efforts"
		}
		query := querytable.Query{Where: []querytable.WhereTerm{{Field: field, Op: "contains", Value: "opus"}}, OrderBy: []querytable.OrderBy{{Field: field, Dir: "asc"}}, Limit: 1}
		if err := schema.ValidateQuery(query); err != nil {
			t.Fatalf("%s combined field: %v", dataset, err)
		}
		if _, err := querytable.Aggregate(shared[dataset], querytable.AggregationRequest{Aggregations: []querytable.Aggregation{{ID: "pairs", Op: "count", GroupBy: []string{field}}}}, schema); err != nil {
			t.Fatalf("%s grouping combined field: %v", dataset, err)
		}
	}
}

func TestModelEffortUnknownLabels(t *testing.T) {
	for _, fixture := range []struct {
		model, effort string
		want          any
	}{
		{"claude-opus-5-5", " High ", "claude-opus-5-5 · high"},
		{"claude-opus-5-5", "", "claude-opus-5-5 · Unknown effort"},
		{"", "low", "Unknown model · low"},
		{"", "", nil},
	} {
		if got := modelEffort(fixture.model, fixture.effort); !reflect.DeepEqual(got, fixture.want) {
			t.Fatalf("pair %q/%q = %v, want %v", fixture.model, fixture.effort, got, fixture.want)
		}
	}
}

func TestModelEffortLookupsUseCoveringMessageIndex(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, fixture := range []struct{ model, effort string }{
		{"opus-5-5", "high"}, {"opus-5-5", ""}, {"", "high"},
	} {
		message, args := configurationPredicate("COALESCE(me.model,c.model)", "me.effort", fixture.model, fixture.effort)
		for name, query := range map[string]string{
			"work filter":         "SELECT c.workspace_id FROM messages me JOIN conversations c ON c.id=me.conversation_id WHERE (me.model IS NOT NULL OR me.effort IS NOT NULL) AND " + message,
			"conversation filter": "SELECT c.id FROM conversations c WHERE EXISTS(SELECT 1 FROM messages me WHERE me.conversation_id=c.id AND (me.model IS NOT NULL OR me.effort IS NOT NULL) AND " + message + ")",
		} {
			plan, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if text := jsonText(plan); !strings.Contains(text, "COVERING INDEX messages_conversation_model_effort_idx") {
				t.Fatalf("%s %q/%q reads message rows: %s", name, fixture.model, fixture.effort, text)
			}
		}
	}
	for _, column := range libraryColumns {
		if column.name != "model_efforts" {
			continue
		}
		plan, err := queryMaps(catalog.DB, "EXPLAIN QUERY PLAN SELECT "+column.expr+" FROM workspaces w WHERE w.id=?", "workspace")
		if err != nil {
			t.Fatal(err)
		}
		if text := jsonText(plan); !strings.Contains(text, "COVERING INDEX messages_conversation_model_effort_idx") {
			t.Fatalf("Library pairs read message rows: %s", text)
		}
	}
}
