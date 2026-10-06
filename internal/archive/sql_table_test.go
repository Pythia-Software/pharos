package archive

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestSQLArrayIncludesCaseSensitivity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE items(id TEXT, tags TEXT);
		INSERT INTO items VALUES ('a','["Bug","ui"]'),('b','["bug"]'),('c',NULL),('d',''),('e','[]'),('f','not json')`); err != nil {
		t.Fatal(err)
	}
	table := sqlDataset{from: "FROM items", columns: map[string]string{"id": "id", "tags": "tags"}}
	schema := querytable.Schema{Name: "items", IDField: "id", Fields: map[string]querytable.Field{
		"id":   {Name: "id", Kind: querytable.Text, Filterable: true, Sortable: true},
		"tags": {Name: "tags", Kind: querytable.TextArray, Filterable: true},
	}}
	ids := func(schema querytable.Schema, term querytable.WhereTerm) []string {
		t.Helper()
		result, err := table.Rows(context.Background(), db, querytable.Query{Where: []querytable.WhereTerm{term}, OrderBy: []querytable.OrderBy{{Field: "id", Dir: "asc"}}, Limit: 10}, schema)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, row := range result.Rows {
			out = append(out, firstString(row["id"]))
		}
		return out
	}
	if got := ids(schema, querytable.WhereTerm{Field: "tags", Op: "includes", Value: "BUG"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("default includes = %v", got)
	}
	tags := schema.Fields["tags"]
	tags.ArrayCaseSensitive = true
	schema.Fields["tags"] = tags
	if got := ids(schema, querytable.WhereTerm{Field: "tags", Op: "includes", Value: "Bug"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("case-sensitive includes = %v", got)
	}
	if got := ids(schema, querytable.WhereTerm{Field: "tags", Op: "includes", Value: "Bug", Negated: true}); !reflect.DeepEqual(got, []string{"b", "f"}) {
		t.Fatalf("negated includes = %v", got)
	}
	if got := ids(schema, querytable.WhereTerm{Field: "tags", Op: "is_null"}); !reflect.DeepEqual(got, []string{"c", "d", "e"}) {
		t.Fatalf("is_null = %v", got)
	}
}

func TestSQLPendingNumberFilter(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items(id TEXT, subagent_count INTEGER);
		INSERT INTO items VALUES ('a',0),('b',2)`); err != nil {
		t.Fatal(err)
	}
	table := sqlDataset{from: "FROM items", columns: map[string]string{"id": "id", "subagent_count": "subagent_count"}}
	schema := querytable.Schema{Name: "items", IDField: "id", Fields: map[string]querytable.Field{
		"id":             {Name: "id", Kind: querytable.Text, Filterable: true, Sortable: true},
		"subagent_count": {Name: "subagent_count", Kind: querytable.Number, Filterable: true},
	}}
	for _, op := range []string{"=", "!=", ">"} {
		result, err := table.Rows(context.Background(), db, querytable.Query{Where: []querytable.WhereTerm{{Field: "subagent_count", Op: op, Value: ""}}, Limit: 10}, schema)
		if err != nil || result.Total != 2 {
			t.Fatalf("pending %s filter: result=%#v err=%v", op, result, err)
		}
	}
	result, err := table.Rows(context.Background(), db, querytable.Query{Where: []querytable.WhereTerm{{Field: "subagent_count", Op: "=", Value: "0"}}, Limit: 10}, schema)
	if err != nil || result.Total != 1 || result.Rows[0]["id"] != "a" {
		t.Fatalf("zero filter: result=%#v err=%v", result, err)
	}
}

func TestSQLTextLength(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items(id TEXT, name TEXT);
		INSERT INTO items VALUES ('a',''),('b','é!'),('c',NULL),('d','abcd')`); err != nil {
		t.Fatal(err)
	}
	table := sqlDataset{from: "FROM items", columns: map[string]string{"id": "id", "name": "name"}}
	schema := querytable.Schema{Name: "items", IDField: "id", Fields: map[string]querytable.Field{
		"id":   {Name: "id", Kind: querytable.Text, Filterable: true, Sortable: true},
		"name": {Name: "name", Kind: querytable.Text, Filterable: true},
	}}
	ids := func(term querytable.WhereTerm) ([]string, error) {
		result, err := table.Rows(context.Background(), db, querytable.Query{Where: []querytable.WhereTerm{term}, Limit: 10}, schema)
		out := []string{}
		for _, row := range result.Rows {
			out = append(out, firstString(row["id"]))
		}
		return out, err
	}
	for _, test := range []struct {
		term querytable.WhereTerm
		want []string
	}{
		{querytable.WhereTerm{Field: "name", Op: "length_eq", Value: "0"}, []string{"a"}},
		{querytable.WhereTerm{Field: "name", Op: "length_gt", Value: "0"}, []string{"b", "d"}},
		{querytable.WhereTerm{Field: "name", Op: "length_eq", Value: "2"}, []string{"b"}},
		{querytable.WhereTerm{Field: "name", Op: "length_lt", Value: "3"}, []string{"a", "b"}},
		{querytable.WhereTerm{Field: "name", Op: "length_lt", Value: "3", Negated: true}, []string{"d"}},
		{querytable.WhereTerm{Field: "name", Op: "length_gt", Value: ""}, []string{"a", "b", "c", "d"}},
	} {
		if got, err := ids(test.term); err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%+v = %v (%v), want %v", test.term, got, err, test.want)
		}
	}
	if _, err := ids(querytable.WhereTerm{Field: "name", Op: "length_gt", Value: "01"}); err == nil {
		t.Error("length with a leading zero accepted")
	}
}

// TestSQLSetFilterModes runs the predicates query-table's set editor emits for
// the keys a and b (see its docs/set-filters.md) against SQLite.
func TestSQLSetFilterModes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items(id TEXT, tags TEXT);
		INSERT INTO items VALUES ('empty','[]'),('a','["a"]'),('b','["b"]'),('ab','["a","b"]'),('c','["c"]'),('A','["A"]'),('missing',NULL)`); err != nil {
		t.Fatal(err)
	}
	table := sqlDataset{from: "FROM items", columns: map[string]string{"id": "id", "tags": "tags"}}
	schema := querytable.Schema{Name: "items", IDField: "id", Fields: map[string]querytable.Field{
		"id":   {Name: "id", Kind: querytable.Text, Filterable: true, Sortable: true},
		"tags": {Name: "tags", Kind: querytable.TextArray, Filterable: true},
	}}
	includes := func(value string, negated bool) querytable.WhereClause {
		return querytable.WhereClause{Field: "tags", Op: "includes", Value: value, Negated: negated}
	}
	empty := querytable.WhereClause{Field: "tags", Op: "is_null"}
	for _, test := range []struct {
		mode  string
		where []querytable.WhereTerm
		want  []string
	}{
		{"any", []querytable.WhereTerm{{Any: []querytable.WhereClause{includes("a", false), includes("b", false)}}}, []string{"A", "a", "ab", "b"}},
		{"all", []querytable.WhereTerm{{Field: "tags", Op: "includes", Value: "a"}, {Field: "tags", Op: "includes", Value: "b"}}, []string{"ab"}},
		{"none", []querytable.WhereTerm{{Any: []querytable.WhereClause{empty, includes("a", true)}}, {Any: []querytable.WhereClause{empty, includes("b", true)}}}, []string{"c", "empty", "missing"}},
		{"empty", []querytable.WhereTerm{{Field: "tags", Op: "is_null"}}, []string{"empty", "missing"}},
	} {
		result, err := table.Rows(context.Background(), db, querytable.Query{Where: test.where, OrderBy: []querytable.OrderBy{{Field: "id", Dir: "asc"}}, Limit: 10}, schema)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, row := range result.Rows {
			got = append(got, firstString(row["id"]))
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s = %v, want %v", test.mode, got, test.want)
		}
	}
}

// TestSQLSortsByExtractedValue checks that SQLite tables order by a regex
// extraction exactly as querytable.Apply does.
func TestSQLSortsByExtractedValue(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items(id TEXT, name TEXT, size INTEGER);
		INSERT INTO items VALUES ('a','v10-beta',310),('b','v9-alpha',29),('c','none',NULL),('d','v2-gamma',1100),('e',NULL,5)`); err != nil {
		t.Fatal(err)
	}
	table := sqlDataset{from: "FROM items", columns: map[string]string{"id": "id", "name": "name", "size": "size"}}
	schema := querytable.Schema{Name: "items", IDField: "id", Fields: map[string]querytable.Field{
		"id":   {Name: "id", Kind: querytable.Text, Filterable: true, Sortable: true},
		"name": {Name: "name", Kind: querytable.Text, Filterable: true, Sortable: true},
		"size": {Name: "size", Kind: querytable.Number, Filterable: true, Sortable: true},
	}}
	memory, err := queryMapsContext(context.Background(), db, "SELECT id,name,size FROM items")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		order querytable.OrderBy
		want  []string
	}{
		// The capture group, as text: "10" < "2" < "9".
		{querytable.OrderBy{Field: "name", Dir: "asc", Extract: &querytable.RegexExtract{Regex: `v(\d+)`}}, []string{"a", "d", "b", "c", "e"}},
		// The whole match without a group, nulls first.
		{querytable.OrderBy{Field: "name", Dir: "desc", Nulls: "first", Extract: &querytable.RegexExtract{Regex: `-[a-z]+$`}}, []string{"c", "e", "d", "a", "b"}},
		// A number field's extraction orders numerically: 5 < 29 < 310 < 1100.
		{querytable.OrderBy{Field: "size", Dir: "asc", Extract: &querytable.RegexExtract{Regex: `^(\d+)`}}, []string{"e", "b", "a", "d", "c"}},
		// An invalid pattern extracts nothing, leaving the ID order.
		{querytable.OrderBy{Field: "name", Dir: "asc", Extract: &querytable.RegexExtract{Regex: `(`}}, []string{"a", "b", "c", "d", "e"}},
	} {
		query := querytable.Query{OrderBy: []querytable.OrderBy{test.order, {Field: "id", Dir: "asc"}}, Limit: 10}
		result, err := table.Rows(context.Background(), db, query, schema)
		if err != nil {
			t.Fatal(err)
		}
		applied, err := querytable.Apply(memory, query, schema)
		if err != nil {
			t.Fatal(err)
		}
		for name, rows := range map[string][]map[string]any{"sql": result.Rows, "memory": applied.Rows} {
			got := []string{}
			for _, row := range rows {
				got = append(got, firstString(row["id"]))
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("%s %+v = %v, want %v", name, *test.order.Extract, got, test.want)
			}
		}
	}
}
