package archive

import (
	"context"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestWritingRowsFoldSubagentWorkAndFollowFilters(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, record := range []WorkspaceRecord{
		{SourceID: "parser", SourceKind: "claude", Account: "local", Title: "Parser", Conversations: []ConversationRecord{{
			NativeID: "parser", Provider: "claude", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "p1", Role: "user", Kind: "message", Text: "Fix the parser for nested tables", CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true},
				{NativeID: "p2", Role: "user", Kind: "message", Text: "Now add a fixture for empty rows", CreatedAt: "2026-09-08T12:00:00.000Z", Selected: true},
			}}}},
		{SourceID: "review", SourceKind: "codex", Account: "local", Title: "Review", Conversations: []ConversationRecord{{
			NativeID: "review", Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "r1", Role: "user", Kind: "message", Text: "Review the grammar change", CreatedAt: "2026-09-02T12:00:00.000Z", Selected: true},
			}}}},
		{SourceID: "review-child", SourceKind: "codex", Account: "local", Title: "Review helper", Conversations: []ConversationRecord{{
			NativeID: "child", Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "c1", Role: "user", Kind: "message", Text: "Check every fixture file for regressions", CreatedAt: "2026-09-02T13:10:00.000Z", Selected: true},
			}}}},
	} {
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// Codex stores a spawned thread as its own work, linked by parent_id.
	if _, err := catalog.DB.Exec(`UPDATE conversations SET parent_id=(SELECT id FROM conversations WHERE native_id='review') WHERE native_id='child'`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	rows, _, err := catalog.writingData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]map[string]any{}
	for _, row := range rows {
		byTitle[firstString(row["title"])] = row
	}
	if len(rows) != 2 || byTitle["Review helper"] != nil {
		t.Fatalf("rows = %#v", rows)
	}
	review := byTitle["Review"]
	if integer(review["user_turns"]) != 2 || integer(review["typed_words"]) != 4 || integer(review["automated_words"]) != 6 || integer(review["subagent_works"]) != 1 {
		t.Fatalf("review = %#v", review)
	}
	parser := byTitle["Parser"]
	if integer(parser["typed_turns"]) != 2 || integer(parser["typed_words"]) != 13 || integer(parser["longest_typed_words"]) != 7 || parser["typed_share"] != float64(100) {
		t.Fatalf("parser = %#v", parser)
	}

	document, err := querySchemaDocument("writing")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	series, err := catalog.writingSeries(context.Background(), writingSeriesRequest{Where: []querytable.WhereTerm{{Field: "title", Op: "=", Value: "Parser"}}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	daily := series["daily"].([]map[string]any)
	totals := series["totals"].(map[string]any)
	if series["works"] != 1 || len(daily) != 2 || integer(totals["typed_words"]) != 13 || integer(totals["automated_words"]) != 0 || integer(daily[1]["typed_words"]) != 7 {
		t.Fatalf("series = %#v", series)
	}
	all, err := catalog.writingSeries(context.Background(), writingSeriesRequest{}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if integer(all["totals"].(map[string]any)["messages"]) != 4 {
		t.Fatalf("all = %#v", all["totals"])
	}
	// A time filter keeps only the days in its window, and a split sums words
	// per day and row value.
	window, err := catalog.writingSeries(context.Background(), writingSeriesRequest{From: "2026-09-02", To: "2026-09-08", Split: "source_kind"}, schema)
	if err != nil {
		t.Fatal(err)
	}
	split := window["split"].([]map[string]any)
	if window["works"] != 2 || len(window["daily"].([]map[string]any)) != 2 || len(split) != 2 ||
		split[0]["day"] != "2026-09-02" || split[0]["value"] != "codex" || integer(split[0]["typed_words"]) != 4 ||
		split[1]["value"] != "claude" || integer(split[1]["typed_words"]) != 7 {
		t.Fatalf("window = %#v", window)
	}
	// Hourly charts preserve folded work, category counts, splits, and exact hour bounds.
	hourRequest := writingSeriesRequest{Hourly: true, Split: "source_kind", Where: []querytable.WhereTerm{{Field: "title", Op: "=", Value: "Review"}}}
	hourly, err := catalog.writingSeries(context.Background(), hourRequest, schema)
	if err != nil {
		t.Fatal(err)
	}
	hours := hourly["hourly"].([]map[string]any)
	if len(hours) != 2 || hours[0]["day"] != "2026-09-02T12:00:00Z" || hours[1]["day"] != "2026-09-02T13:00:00Z" || len(hourly["daily"].([]map[string]any)) != 1 || hourly["works"] != 1 {
		t.Fatalf("hourly folded work: %#v", hourly)
	}
	if integer(hourly["totals"].(map[string]any)["typed_words"]) != 4 || integer(hourly["totals"].(map[string]any)["automated_words"]) != 6 || len(hourly["split"].([]map[string]any)) != 2 {
		t.Fatalf("hourly counts: %#v", hourly)
	}
	hourRequest.Start, hourRequest.End = "2026-09-02T12:00:00Z", "2026-09-02T13:00:00Z"
	windowed, err := catalog.writingSeries(context.Background(), hourRequest, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(windowed["hourly"].([]map[string]any)) != 1 || integer(windowed["totals"].(map[string]any)["automated_words"]) != 0 {
		t.Fatalf("exclusive hourly range: %#v", windowed)
	}
	if _, err := catalog.writingSeries(context.Background(), writingSeriesRequest{Split: "title"}, schema); err == nil {
		t.Fatal("split by title was accepted")
	}
}
