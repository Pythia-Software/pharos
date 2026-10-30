package archive

import (
	"context"
	"fmt"
	qt "github.com/Pythia-Software/query-table/backends/go"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestToolCommandSearchIndex(t *testing.T) {
	override(t, &toolSearchBatch, 20)
	catalog, _ := testCatalog(t)
	seedToolCalls(t, catalog, toolSearchBatch+10) // more than one backfill batch
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

func TestQueryTextIndexesMatchTypedSubstringFilters(t *testing.T) {
	for _, dataset := range []string{"tool_calls", "writing_messages"} {
		t.Run(dataset, func(t *testing.T) {
			var c *Catalog
			index := toolQueryText
			if dataset == "tool_calls" {
				c, _ = testCatalog(t)
				seedToolCalls(t, c, 30)
			} else {
				c = textFixture(t)
				index = humanQueryText
				if _, err := c.DB.Exec(`UPDATE messages SET selected=1; INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json) SELECT m.id,m.conversation_id,cv.workspace_id,m.created_at,substr(m.created_at,1,10),length(m.text),'[]' FROM messages m JOIN conversations cv ON cv.id=m.conversation_id WHERE m.role='user' AND m.kind='message'`); err != nil {
					t.Fatal(err)
				}
			}
			table, _ := sqlDatasetFor(dataset)
			schema, _ := sqliteQuerySchema(dataset)
			// Include ASCII case, Unicode casefolding in the haystack, quotes,
			// punctuation, whitespace, short bodies, and null/empty bodies.
			if dataset == "tool_calls" {
				tx := seedTx(t, c.DB)
				for i, text := range []any{"A TEST\nwith \"quotes\" KELVIN İ", "  ", "a", "", nil} {
					if _, err := tx.Exec("UPDATE tool_calls SET command=? WHERE id=?", text, fmt.Sprint("call-", i)); err != nil {
						t.Fatal(err)
					}
				}
				commitSeed(t, tx)
				publishProjections(t, c)
			}
			// Rows take the host's indexed window, the averages the adapter
			// and the grouping /query-v3, each over sources the index narrows.
			// Every answer must match ordinary typed execution, which is what
			// runs while the index is not ready.
			number := map[string]string{"tool_calls": "duration_ms", "writing_messages": "words"}[dataset]
			server := &Server{Catalog: c}
			setReady := func(ready bool) {
				t.Helper()
				statement := "DELETE FROM meta WHERE key=?"
				if ready {
					statement = "INSERT INTO meta(key,value) VALUES(?,'1')"
				}
				if _, err := c.DB.Exec(statement, index.table+"_ready"); err != nil {
					t.Fatal(err)
				}
			}
			execute := func(where []qt.WhereTerm) (any, error) {
				t.Helper()
				request := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{Select: []string{"id"}, Where: where, Limit: 100}}
				rows, err := server.executeQueryV2(t.Context(), dataset, nil, schema, &request, nil)
				if err != nil {
					return nil, err
				}
				metrics := metricRequest(qt.AggSpec{ID: "rows", Op: "count", GroupBy: []string{"conversation_id"}},
					qt.AggSpec{ID: "mean", Op: "avg", Field: number, GroupBy: []string{"conversation_id"}},
					qt.AggSpec{ID: "filtered", Op: "count", Where: where})
				metrics.Where = where
				reduced, err := server.executeQueryV2(t.Context(), dataset, nil, schema, nil, &metrics)
				if err != nil {
					return nil, err
				}
				grouping := groupRequest("conversation_id", qt.SummaryColumn{ID: "calls", Op: "count"}, qt.SummaryColumn{ID: "mean", Op: "avg", Field: number})
				grouping.Source = qt.WireQuery{Where: where}
				grouping.Result.OrderBy = qt.OrderBys{{Field: "conversation_id", Dir: "asc"}}
				grouped, _, err := server.executeQueryV3(t.Context(), dataset, nil, schema, grouping)
				return []any{rows.Rows.Total, rows.Rows.Rows, rows.Rows.Computed, reduced.Metrics, grouped.Rows, grouped.Total, grouped.SourceTotal}, err
			}
			contains := func(value string, negated bool) qt.WhereClause {
				return qt.WhereClause{Field: index.field, Op: "contains", Value: value, Negated: negated}
			}
			check := func() {
				t.Helper()
				filters := [][]qt.WhereTerm{
					// Either branch indexed, and a branch that is not: the term
					// then keeps its typed predicates.
					{{Any: []qt.WhereClause{contains("test", false), contains("quotes", false)}}},
					{{Any: []qt.WhereClause{contains("test", false), contains("a", false)}}},
					{{Any: []qt.WhereClause{{All: []qt.WhereClause{contains("test", false), contains("updated", true)}}, contains("foo", false)}}},
					{{Any: []qt.WhereClause{{All: []qt.WhereClause{contains("test", false), {Field: "conversation_id", Op: "is_not_null"}}}, contains("zzz", false)}}},
				}
				for _, needle := range []string{"TEST", "foo", "bar", "quotes", "kelvin", "a", "absent_82f14c", "\"qu", "  ", "\nwi", "   ", "İ"} {
					for _, negated := range []bool{false, true} {
						filters = append(filters, []qt.WhereTerm{{Field: index.field, Op: "contains", Value: needle, Negated: negated}})
					}
				}
				for _, where := range filters {
					if !index.term(contains("TEST", false)) {
						t.Fatal("eligible term rejected")
					}
					setReady(false)
					want, err := execute(where)
					if err != nil {
						t.Fatal(err)
					}
					setReady(true)
					got, err := execute(where)
					if err != nil || !identical(got, want) {
						t.Fatalf("%+v: got=%+v want=%+v err=%v", where, got, want, err)
					}
				}
			}
			// Exercise the upgrade path as well as triggers; readiness stays false
			// until every old row has been backfilled.
			if _, err := c.DB.Exec("DELETE FROM " + index.table); err != nil {
				t.Fatal(err)
			}
			for {
				more, err := c.indexQueryTextBatch(t.Context(), index)
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					break
				}
			}
			if dataset == "tool_calls" {
				for {
					more, err := c.indexQueryTextBatch(t.Context(), toolQueryFacts)
					if err != nil {
						t.Fatal(err)
					}
					if !more {
						break
					}
				}
			}
			indexed, err := c.queryTextDataset(t.Context(), dataset, table)
			if err != nil || indexed.queryText == nil {
				t.Fatal("missing complete index", err)
			}
			check()
			if dataset == "tool_calls" {
				// Upgrade a previous facts version in bounded batches, retaining
				// scalar availability until the local calendar is complete.
				if _, err := c.DB.Exec("UPDATE tool_query_facts SET day=NULL; UPDATE meta SET value=? WHERE key='tool_query_facts_ready'", toolRollupVersion); err != nil {
					t.Fatal(err)
				}
				prior, err := c.queryTextDataset(t.Context(), dataset, table)
				if err != nil || !prior.scalarFacts || prior.calendarFacts {
					t.Fatal("calendar upgrade readiness", prior.scalarFacts, prior.calendarFacts, err)
				}
				for {
					more, err := c.indexQueryTextBatch(t.Context(), toolQueryFacts)
					if err != nil {
						t.Fatal(err)
					}
					if !more {
						break
					}
				}
				if _, err := c.DB.Exec(`UPDATE tool_calls SET command='UPDATED test',tool_category='command',command_category='test',program='go',duration_ms=123,started_at='2026-10-20T01:00:00Z' WHERE id='call-1'; DELETE FROM tool_calls WHERE id='call-2'`); err != nil {
					t.Fatal(err)
				}
				var mismatched int
				if err := c.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls t LEFT JOIN tool_query_facts f ON f.rowid=t.rowid
 WHERE f.id IS NOT t.id OR f.workspace_id IS NOT t.workspace_id OR f.started_at IS NOT t.started_at OR f.duration_ms IS NOT t.duration_ms OR f.provider IS NOT t.provider OR f.tool_category IS NOT t.tool_category
 OR f.activity IS NOT machine_activity(t.tool_category,t.command_category,t.tool_name,t.mcp_server,t.program)
 OR f.day IS NOT date(t.started_at,'localtime')`).Scan(&mismatched); err != nil || mismatched != 0 {
					t.Fatalf("scalar projection drift: %d, %v", mismatched, err)
				}
			} else {
				if _, err := c.DB.Exec(`UPDATE messages SET text='UPDATED test' WHERE id='a'; DELETE FROM message_authorship WHERE message_id='b'`); err != nil {
					t.Fatal(err)
				}
			}
			check()
		})
	}
}
