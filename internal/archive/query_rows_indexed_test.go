package archive

import (
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Indexed candidates replace each literal in place, so an OR of AND branches
// keeps its meaning: an exact text term leaves the residual filter and must
// still AND within a branch.
func TestQueryCandidatesKeepOrOfAndBranches(t *testing.T) {
	c, _ := testCatalog(t)
	seedToolCalls(t, c, 80)
	c.maintainQueryTextIndexes(t.Context())
	ctx := t.Context()
	schema := PharosToolCallsSchema()
	table, err := c.queryTextDataset(ctx, "tool_calls", toolCallDataset)
	if err != nil || table.queryText == nil {
		t.Fatal("text index not ready", err)
	}
	contains := func(value string) qt.WhereClause {
		return qt.WhereClause{Field: "command", Op: "contains", Value: value}
	}
	for _, where := range [][]qt.WhereTerm{
		{{Any: []qt.WhereClause{{All: []qt.WhereClause{contains("git"), contains("test")}}, contains("zzz")}}},
		{{Any: []qt.WhereClause{{All: []qt.WhereClause{contains("go "), contains("test")}}, contains("status")}}},
		{{Any: []qt.WhereClause{{All: []qt.WhereClause{contains("git"), {Field: "provider", Op: "=", Value: "codex"}}}, {Field: "tool_name", Op: "=", Value: "Read"}}}},
	} {
		if len(table.residualWhere(where)) == len(where) && where[0].Predicates()[1].Field == "command" {
			t.Fatalf("%+v: an all-text term should be answered by the index alone", where)
		}
		tx, err := c.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		count := func(source string, args []any, terms []qt.WhereTerm) int64 {
			d := queryV3Dataset(schema)
			d.SourceSQL = source
			d.SourceArgs = args
			out, err := d.ExecuteIn(ctx, tx, qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Result: qt.WireQuery{Where: terms, Limit: 1}}, qt.PlanOptions{})
			if err != nil {
				t.Fatal(err)
			}
			n := out.Total
			return n
		}
		source, args := table.indexedQuerySource(schema, where)
		got := count(source, args, table.residualWhere(where))
		want := count(toolCallDataset.querySourceSQL(schema), nil, where)
		_ = tx.Rollback()
		if got != want {
			t.Fatalf("%+v: indexed %d, typed %d", where, got, want)
		}
	}
}

// The page's filter follows the source's numbered arguments; question marks
// in literals and identifiers are not parameters.
