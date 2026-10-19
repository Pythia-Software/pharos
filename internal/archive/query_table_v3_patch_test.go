package archive

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// These tests guard Pharos's local patches to Query Table's Go backend
// (third_party/query-table-go/README.pharos.md): fused group summaries and
// representative rows materialized once. They are the only tests of that
// path in go test ./...; tools/test-query-table-fork.sh runs upstream's whole
// suite against the patched copy.

func patchQueryV3Fixture(t *testing.T) (*sql.DB, qt.QueryDatasetV3) {
	catalog, _ := testCatalog(t)
	db := catalog.DB
	_, err := db.Exec(`CREATE TABLE grouped(id TEXT,k TEXT,n REAL,at TEXT,label TEXT);
 INSERT INTO grouped VALUES('1','a',1,'2026-11-01T05:15:00.000Z','old'),('2','a',3,'2026-11-01T05:45:00.000Z',NULL),('3','a',3,'2026-11-01T06:15:00.000Z','tie'),('4','b',10,'2026-11-01T07:15:00.000Z','b'),('5','',2,NULL,'empty'),('6',NULL,4,NULL,'null');`)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	schema := qt.Schema{Name: "grouped", IDField: "id", Fields: map[string]qt.FieldSpec{
		"id":    {Name: "id", Kind: qt.FieldText, Expr: "r.id", Sortable: true, ServerFilter: true},
		"k":     {Name: "k", Kind: qt.FieldText, Expr: "r.k", Sortable: true, ServerFilter: true},
		"n":     {Name: "n", Kind: qt.FieldNumber, Expr: "r.n", ExpressionNumeric: true, Sortable: true, ServerFilter: true, Groupable: &yes},
		"at":    {Name: "at", Kind: qt.FieldDatetime, Expr: "r.at", SQLiteDatetimeFormat: "utc-millis", Sortable: true, ServerFilter: true, Groupable: &yes},
		"label": {Name: "label", Kind: qt.FieldText, Expr: "r.label", Sortable: true, ServerFilter: true},
	}}
	return db, qt.QueryDatasetV3{Schema: schema, SourceSQL: "SELECT * FROM grouped", Dialect: "sqlite", Scope: "one", Dataset: "runs"}
}

func patchGroupedRequest() qt.QueryRequestV3 {
	return qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), Rows: true,
		Source:   qt.WireQuery{Limit: 1, Offset: 2},
		Grouping: &qt.GroupingDefinition{Keys: []string{"k"}, Summaries: []qt.SummaryColumn{{ID: "num", Op: "count"}, {ID: "total", Op: "sum", Field: "n"}, {ID: "average", Op: "avg", Field: "n"}, {ID: "median", Op: "formula", Expression: "MEDIAN([n])"}, {ID: "example", Op: "row", Field: "label"}, {ID: "example_id", Op: "row", Field: "id"}}, Representative: &qt.RepresentativeRow{OrderBy: []qt.OrderBy{{Field: "n", Dir: "desc", Nulls: "last"}}}},
		Result:   qt.WireQuery{Select: []string{"k", "num", "total", "average", "median", "example", "example_id"}, OrderBy: qt.OrderBys{{Field: "total", Dir: "desc"}}, Limit: 10},
	}
}

// Fused summaries over a computed group key keep one identity per group,
// including the group whose key is an error.
func TestVendoredPatchFormulaGroupErrorIdentities(t *testing.T) {
	db, d := patchQueryV3Fixture(t)
	if _, err := db.Exec("DELETE FROM grouped; INSERT INTO grouped(id,n,label) VALUES('null',NULL,'null label'),('error',0,'error label'),('valid',2,'valid label')"); err != nil {
		t.Fatal(err)
	}
	q := patchGroupedRequest()
	q.ExpectedRevisions = map[string]string{"inv": "1"}
	q.Grouping = &qt.GroupingDefinition{Keys: []string{"@computed/inv"}, Summaries: []qt.SummaryColumn{{ID: "num", Op: "count"}, {ID: "total", Op: "sum", Field: "n"}, {ID: "example", Op: "row", Field: "label"}}, Representative: &qt.RepresentativeRow{OrderBy: qt.OrderBys{{Field: "id", Dir: "asc"}}}}
	q.Result = qt.WireQuery{Select: []string{"@computed/inv", "num", "total", "example"}, Limit: 10}
	d.ComputedGroupable = map[string]bool{"inv": true}
	options := qt.PlanOptions{Resolver: qt.DefinitionResolverFunc(func(_ context.Context, id string) (qt.ComputedColumn, error) {
		return qt.ComputedColumn{ID: id, Label: "Inverse", Revision: "1", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "1/[n]"}}, nil
	})}
	out, err := d.Execute(context.Background(), db, q, options)
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 3 || len(out.Rows) != 3 {
		t.Fatal(out)
	}
	identities := map[any]bool{}
	for _, row := range out.Rows {
		identity := row[qt.GroupIdentityField]
		if identities[identity] {
			t.Fatalf("duplicate identity: %#v", out.Rows)
		}
		identities[identity] = true
		if row["example"] != "error label" && row["num"] != float64(1) {
			t.Fatal(row)
		}
		switch row["example"] {
		case "null label":
			if row["@computed/inv"] != nil || row["total"] != nil {
				t.Fatal(row)
			}
		case "error label":
			if !reflect.DeepEqual(row["@computed/inv"], map[string]any{"computedError": "divide_by_zero"}) || !reflect.DeepEqual(row["total"], map[string]any{"computedError": "divide_by_zero"}) || !reflect.DeepEqual(row["num"], map[string]any{"computedError": "divide_by_zero"}) {
				t.Fatal(row)
			}
		case "valid label":
			if row["@computed/inv"] != float64(.5) || row["total"] != float64(2) {
				t.Fatal(row)
			}
		default:
			t.Fatal(row)
		}
	}
}

// Hidden and default sorts, a field sorted by another, and enum kinds survive
// the patched source plan and its representative rows.
func TestVendoredPatchHiddenAndDefaultSort(t *testing.T) {
	db, d := patchQueryV3Fixture(t)
	q := patchGroupedRequest()
	q.Grouping = nil
	q.Result = qt.WireQuery{Select: []string{"id"}, Limit: 2, OrderBy: qt.OrderBys{{Field: "n", Dir: "desc"}}}
	plan, err := d.SourcePlan(context.Background(), q, qt.PlanOptions{})
	if err != nil || !reflect.DeepEqual(plan.Fields, []string{"id", "n"}) {
		t.Fatal(plan.Fields, err)
	}
	out, err := d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil || out.Rows[0]["id"] != "4" {
		t.Fatal(out, err)
	}
	d.Schema.DefaultSort = q.Result.OrderBy
	q.Result.OrderBy = nil
	out, err = d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil || out.Rows[0]["id"] != "4" {
		t.Fatal(out, err)
	}
	label := d.Schema.Fields["label"]
	label.SQLiteSortField, label.SortExpr = "n", "r.n"
	d.Schema.Fields["label"] = label
	q.Result.OrderBy = qt.OrderBys{{Field: "label", Dir: "desc"}}
	out, err = d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil || out.Rows[0]["id"] != "4" {
		t.Fatal(out, err)
	}
	k := d.Schema.Fields["k"]
	k.Kind = qt.FieldEnum
	d.Schema.Fields["k"] = k
	q = patchGroupedRequest()
	q.Grouping.Summaries = []qt.SummaryColumn{{ID: "chosen", Op: "row", Field: "k"}, {ID: "smallest", Op: "min", Field: "k"}}
	q.Result = qt.WireQuery{Select: []string{"k", "chosen", "smallest"}, Limit: 10}
	plan, err = d.SourcePlan(context.Background(), q, qt.PlanOptions{})
	if err != nil || plan.ResultSchema.Fields["chosen"].Kind != qt.FieldEnum || plan.ResultSchema.Fields["smallest"].Kind != qt.FieldEnum {
		t.Fatal(plan, err)
	}
	out, err = d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range out.Schema.Fields {
		if field.Name != qt.GroupIdentityField && field.Type != "enum" {
			t.Fatal(out.Schema)
		}
	}
}
