package archive

import (
	"context"
	"database/sql"
	"errors"
	qt "github.com/Pythia-Software/query-table/backends/go"
	"reflect"
	"testing"
	"time"
)

func reviewQueryV3Fixture(t *testing.T) (*sql.DB, qt.QueryDatasetV3) {
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
func reviewGroupedRequest() qt.QueryRequestV3 {
	return qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), Rows: true,
		Source:   qt.WireQuery{Limit: 1, Offset: 2},
		Grouping: &qt.GroupingDefinition{Keys: []string{"k"}, Summaries: []qt.SummaryColumn{{ID: "num", Op: "count"}, {ID: "total", Op: "sum", Field: "n"}, {ID: "average", Op: "avg", Field: "n"}, {ID: "median", Op: "formula", Expression: "MEDIAN([n])"}, {ID: "example", Op: "row", Field: "label"}, {ID: "example_id", Op: "row", Field: "id"}}, Representative: &qt.RepresentativeRow{OrderBy: []qt.OrderBy{{Field: "n", Dir: "desc", Nulls: "last"}}}},
		Result:   qt.WireQuery{Select: []string{"k", "num", "total", "average", "median", "example", "example_id"}, OrderBy: qt.OrderBys{{Field: "total", Dir: "desc"}}, Limit: 10},
	}
}
func TestVendoredV3UngroupedComputedErrorCells(t *testing.T) {
	db, d := reviewQueryV3Fixture(t)
	q := reviewGroupedRequest()
	q.Grouping = nil
	q.Result = qt.WireQuery{Select: []string{"id", "@computed/inv"}, OrderBy: qt.OrderBys{{Field: "id", Dir: "asc"}}, Limit: 10}
	q.ExpectedRevisions = map[string]string{"inv": "1"}
	o := qt.PlanOptions{Resolver: qt.DefinitionResolverFunc(func(_ context.Context, id string) (qt.ComputedColumn, error) {
		return qt.ComputedColumn{ID: id, Label: "Inverse", Revision: "1", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "1/([n]-1)"}}, nil
	})}
	out, err := d.Execute(context.Background(), db, q, o)
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 6 || out.SourceTotal != 6 || len(out.Rows) != 6 {
		t.Fatal(out)
	}
	for i, want := range []any{map[string]any{"computedError": "divide_by_zero"}, float64(.5), float64(.5), float64(1) / 9, float64(1), float64(1) / 3} {
		if !reflect.DeepEqual(out.Rows[i]["@computed/inv"], want) {
			t.Fatalf("row %d: got %#v want %#v", i, out.Rows[i], want)
		}
	}
	// A typed base-field failure must still reject the complete request.
	field := d.Schema.Fields["n"]
	field.ValueErrorExpr = "CASE WHEN r.n=1 THEN 'invalid_number' ELSE NULL END"
	d.Schema.Fields["n"] = field
	q.Result.Select = append(q.Result.Select, "n")
	var diag *qt.PlanDiagnostic
	if _, err = d.Execute(context.Background(), db, q, o); !errors.As(err, &diag) || diag.Code != "invalid_source_value" {
		t.Fatalf("base-field failure: %v", err)
	}
}

func TestVendoredV3FormulaGroupErrorIdentities(t *testing.T) {
	db, d := reviewQueryV3Fixture(t)
	if _, err := db.Exec("DELETE FROM grouped; INSERT INTO grouped(id,n,label) VALUES('null',NULL,'null label'),('error',0,'error label'),('valid',2,'valid label')"); err != nil {
		t.Fatal(err)
	}
	q := reviewGroupedRequest()
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
func TestVendoredV3HiddenAndDefaultSort(t *testing.T) {
	db, d := reviewQueryV3Fixture(t)
	q := reviewGroupedRequest()
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
	q = reviewGroupedRequest()
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

func TestVendoredV3CalendarQueryRangeIgnoresClearedFilters(t *testing.T) {
	db, d := reviewQueryV3Fixture(t)
	q := reviewGroupedRequest()
	q.Grouping = nil
	q.Result = qt.WireQuery{Select: []string{"id"}, Limit: 10, Where: []qt.WhereTerm{{Field: "at", Op: ">=", Value: "2026-11-01"}, {Field: "at", Op: "<=", Value: "2026-11-05"}, {Field: "at", Op: ">", Value: ""}}}
	q.Metrics = []qt.AggSpec{{ID: "days", Op: "count", GroupBy: []string{"at"}, DateGrouping: &qt.MetricDateGrouping{Field: "at", Interval: "day", TimeZone: "UTC", Range: "query", IncludeEmpty: true}}}
	out, err := d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil || len(out.Metrics) != 1 || len(out.Metrics[0].Buckets) != 5 {
		t.Fatalf("cleared date: %#v, %v", out, err)
	}
	q.Result.Where = q.Result.Where[:2]
	want, err := d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil || !reflect.DeepEqual(out.Rows, want.Rows) || !reflect.DeepEqual(out.Metrics, want.Metrics) {
		t.Fatalf("empty predicate changed results: %#v / %#v, %v", out, want, err)
	}
}

func TestVendoredV3CalendarShownRowsPreservesHiddenSort(t *testing.T) {
	db, d := reviewQueryV3Fixture(t)
	d.Schema.DefaultSort = []qt.OrderBy{{Field: "n", Dir: "desc"}}
	q := reviewGroupedRequest()
	q.Grouping = nil
	q.Rows = false
	q.Result = qt.WireQuery{Select: []string{"id"}, Limit: 2}
	q.Metrics = []qt.AggSpec{{ID: "shown", Op: "sum", Field: "n", Scope: "shownRows", GroupBy: []string{"at"}, DateGrouping: &qt.MetricDateGrouping{Field: "at", Interval: "day", TimeZone: "UTC", Range: "observed"}}}
	out, err := d.Execute(context.Background(), db, q, qt.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sum := 0.0
	for _, b := range out.Metrics[0].Buckets {
		sum += b.Value.(float64)
	}
	if sum != 14 || out.Metrics[0].ProcessedRows != 2 {
		t.Fatal(out)
	}
}
