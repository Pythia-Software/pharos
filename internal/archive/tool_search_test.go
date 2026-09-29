package archive

import (
	"context"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestToolCommandSearchIndex(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedToolCalls(t, catalog, 650) // more than one backfill batch
	ctx := context.Background()
	document, err := querySchemaDocument("tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	indexed := toolCallDataset
	indexed.toolSearchIndexed = true
	direct := toolCallDataset
	direct.cube = nil
	filters := []querytable.WhereTerm{
		{Field: "command", Op: "contains", Value: "TEST ./"},
		{Field: "command", Op: "matches_regex", Value: `go.*test`},
		{Field: "program", Op: "contains", Value: "git"},
		{Field: "subcommand", Op: "matches_regex", Value: `stat.*`},
		{Field: "command_name", Op: "contains", Value: "git stat"},
	}
	check := func() {
		t.Helper()
		for _, filter := range filters {
			query := querytable.Query{Where: []querytable.WhereTerm{filter}, Limit: 500}
			got, err := indexed.Rows(ctx, catalog.DB, query, schema)
			if err != nil {
				t.Fatalf("indexed %v: %v", filter, err)
			}
			want, err := direct.Rows(ctx, catalog.DB, query, schema)
			if err != nil {
				t.Fatal(err)
			}
			if !same(got, want) {
				t.Fatalf("%v: indexed total %d, direct %d", filter, got.Total, want.Total)
			}
			request := querytable.AggregationRequest{Where: query.Where, Aggregations: []querytable.Aggregation{{ID: "calls", Op: "count"}, {ID: "by_tool", Op: "count", GroupBy: []string{"tool_name"}}}}
			gotMetrics, err := indexed.Aggregate(ctx, catalog.DB, request, schema)
			if err != nil {
				t.Fatal(err)
			}
			wantMetrics, err := direct.Aggregate(ctx, catalog.DB, request, schema)
			if err != nil || !same(gotMetrics, wantMetrics) {
				t.Fatalf("%v: indexed metrics %v, direct %v: %v", filter, gotMetrics, wantMetrics, err)
			}
		}
	}
	check() // insert triggers on a new catalog
	where, args, err := indexed.where([]querytable.WhereTerm{filters[0]}, schema)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := queryMapsContext(ctx, catalog.DB, "EXPLAIN QUERY PLAN SELECT COUNT(*) FROM tool_calls t"+where, args...)
	if err != nil {
		t.Fatal(err)
	}
	usesIndex := false
	for _, row := range plan {
		usesIndex = usesIndex || strings.Contains(firstString(row["detail"]), "VIRTUAL TABLE INDEX")
	}
	if !usesIndex {
		t.Fatalf("command search did not use the trigram index: %v", plan)
	}
	if _, err := catalog.DB.Exec("UPDATE tool_calls SET command='go test ./changed',subcommand='status' WHERE id='call-0'"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("DELETE FROM tool_calls WHERE id='call-1'"); err != nil {
		t.Fatal(err)
	}
	check()

	// Simulate upgrading a catalog whose calls predate the index.
	if _, err := catalog.DB.Exec(`DELETE FROM meta WHERE key IN ('tool_command_fts_version','tool_command_fts_cursor')`); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("DELETE FROM tool_command_fts"); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := catalog.indexToolSearchBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	ready, err := catalog.toolSearchReady(ctx)
	if err != nil || !ready {
		t.Fatalf("index ready=%v: %v", ready, err)
	}
	check()
}
