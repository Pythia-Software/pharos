package querytable

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sqliteTestSchema() Schema {
	fields := map[string]FieldSpec{}
	for name, kind := range map[string]FieldKind{"id": FieldText, "text": FieldText, "n": FieldNumber, "tags": FieldTextArray, "when": FieldDatetime, "bool": FieldBool} {
		fields[name] = FieldSpec{Name: name, Kind: kind, Expr: "r." + name, SortExpr: "r." + name, ServerFilter: true, Sortable: true}
	}
	return Schema{Name: "test", IDField: "id", Fields: fields}
}
func TestSQLiteCompilerGuards(t *testing.T) {
	s := sqliteTestSchema()
	bad := []WireQuery{
		{Where: []WhereTerm{{Field: "missing", Op: "=", Value: "x"}}},
		{Where: []WhereTerm{{Field: "n", Op: "contains", Value: "x"}}},
		{Where: []WhereTerm{{Field: "n", Op: "=", Value: "NaN"}}},
		{Where: []WhereTerm{{Field: "n", Op: "=", Value: "Inf"}}},
		{Where: []WhereTerm{{Field: "text", Op: "matches_regex", Value: "(?<=x)y"}}},
		{Where: []WhereTerm{{Field: "text", Op: "length_eq", Value: "01"}}},
		{Where: []WhereTerm{{Any: []WhereClause{}}}},
		{Where: []WhereTerm{{Field: "text", Any: []WhereClause{{Field: "text", Op: "=", Value: "x"}}}}},
		{OrderBy: []OrderBy{{Field: "text", Dir: "oops"}}},
		{OrderBy: []OrderBy{{Field: "text", Dir: "asc", Nulls: "oops"}}},
		{OrderBy: []OrderBy{{Field: "text", Dir: "asc", Extract: &RegexExtract{Regex: "["}}}},
		{Select: []string{"@computed/x"}},
	}
	for _, q := range bad {
		if _, e := CompileSQLite(q, s, SQLiteOptions{}); e == nil {
			t.Errorf("accepted %#v", q)
		}
	}
	f := s.Fields["text"]
	f.FilterOps = []string{"="}
	s.Fields["text"] = f
	if _, e := CompileSQLite(WireQuery{Where: []WhereTerm{{Field: "text", Op: "contains", Value: "x"}}}, s, SQLiteOptions{}); e == nil {
		t.Fatal("ignored filter policy")
	}
}
func TestSQLiteCompilerComposition(t *testing.T) {
	s := sqliteTestSchema()
	s.DefaultSort = []OrderBy{{Field: "text", Dir: "asc", Extract: &RegexExtract{Regex: "(x)"}}}
	payload := "' OR 1=1; DROP TABLE runs; --"
	q := WireQuery{Where: []WhereTerm{{Field: "text", Op: "ends_with", Value: payload}}, Select: []string{"id"}}
	c, e := CompileSQLite(q, s, SQLiteOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(c.WhereSQL, payload) || !reflect.DeepEqual(c.WhereArgs, []any{payload}) || !reflect.DeepEqual(c.OrderArgs, []any{"(x)"}) {
		t.Fatalf("unsafe or misordered fragments: %#v", c)
	}
	if !strings.HasSuffix(c.OrderSQL, "r.id COLLATE BINARY ASC NULLS LAST") {
		t.Fatal(c.OrderSQL)
	}
	q.Where = []WhereTerm{{Any: []WhereClause{{Field: "text", Op: "contains", Value: ""}, {Field: "n", Op: ">", Value: "12"}}}}
	c, e = CompileSQLite(q, s, SQLiteOptions{})
	if e != nil || c.WhereSQL != "" || len(c.WhereArgs) != 0 {
		t.Fatalf("OR clear input: %#v %v", c, e)
	}
	q.Where[0].Any[1].Field = "missing"
	if _, e = CompileSQLite(q, s, SQLiteOptions{}); e == nil {
		t.Fatal("skipped OR validation")
	}
}
func TestSQLiteSchemaDialect(t *testing.T) {
	doc := []byte(`{"name":"s","idField":"id","fields":[{"name":"id","type":"text","bindings":{"postgres":{"expr":"pg.id"},"sqlite":{"expr":"s.id"}}},{"name":"date","type":"datetime","bindings":{"sqlite":{"expr":"s.date","datetimeFormat":"utc-millis"}}}]}`)
	s, e := LoadSQLiteSchema(doc)
	if e != nil {
		t.Fatal(e)
	}
	if s.Fields["id"].Expr != "s.id" || s.Fields["date"].SQLiteDatetimeFormat != "utc-millis" {
		t.Fatal(s)
	}
	if _, e = LoadSQLiteSchema([]byte(schemaJSON)); e == nil {
		t.Fatal("fell back to Postgres")
	}
}
func TestSQLiteRelativeDatetime(t *testing.T) {
	s := sqliteTestSchema()
	now := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	c, e := CompileSQLite(WireQuery{Where: []WhereTerm{{Field: "when", Op: ">", Value: "-1d"}}}, s, SQLiteOptions{Now: now})
	if e != nil {
		t.Fatal(e)
	}
	if c.WhereArgs[0] != "2026-01-01T03:04:05.123456789Z" {
		t.Fatal(c.WhereArgs)
	}
}
func TestSQLiteModernMetricsRefused(t *testing.T) {
	for _, a := range []AggSpec{{ID: "x", Op: "sum", Field: "n", Expression: "SUM(n)"}, {ID: "x", Op: "sum", Field: "n", Scope: "shownRows"}, {ID: "x", Op: "sum", Field: "n", GroupLimit: 3}, {ID: "x", Op: "sum", Field: "n", Distribution: &MetricDistribution{Kind: "box"}}} {
		if _, e := CompileSQLiteAggregation(a, sqliteTestSchema()); e == nil {
			t.Fatal(a)
		}
	}
}
func TestSQLiteScalarFunctions(t *testing.T) {
	functions := map[string]SQLiteScalarFunction{}
	for _, f := range SQLiteFunctions() {
		functions[f.Name] = f
	}
	for _, test := range []struct {
		name string
		args []driver.Value
		want driver.Value
	}{
		{"qt_lower", []driver.Value{"ÉCOLE"}, "école"}, {"qt_length", []driver.Value{"a\x00😀"}, int64(3)},
		{"qt_regexp_extract", []driver.Value{"(x*)", "a"}, ""}, {"qt_regexp_extract", []driver.Value{"(x)?a", "a"}, nil},
		{"qt_regexp", []driver.Value{"x", nil}, nil}, {"qt_array_json", []driver.Value{`{"x":"y"}`}, "[]"},
		{"qt_array_json", []driver.Value{`[null]`}, "[]"}, {"qt_datetime", []driver.Value{"2026-01-01T00:00:00+02:00"}, "2025-12-31T22:00:00.000000000Z"},
	} {
		v, e := functions[test.name].Call(test.args)
		if e != nil || !reflect.DeepEqual(v, test.want) {
			t.Errorf("%s = %#v, %v", test.name, v, e)
		}
	}
	if _, e := functions["qt_regexp"].Call([]driver.Value{"[", "x"}); e == nil {
		t.Fatal("invalid regex accepted")
	}
	if _, e := functions["qt_number"].Call([]driver.Value{int64(9007199254740992)}); e == nil {
		t.Fatal("unsafe integer accepted")
	}
}

func TestSQLiteV2EnvelopeRefused(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"version":2}`, `{"profile":"qt-postgres-v1"}`, `{"snapshot":"x"}`, `{"expectedRevisions":{}}`} {
		if _, e := DecodeSQLiteQuery([]byte(body)); e == nil {
			t.Errorf("accepted %s", body)
		}
	}
	q, e := DecodeSQLiteQuery([]byte(`{"w":[{"field":"n","op":">","value":"2"}],"l":10}`))
	if e != nil || q.Limit != 10 || len(q.Where) != 1 {
		t.Fatal(q, e)
	}
}

func TestSQLiteAlternateSortUsesTargetStorage(t *testing.T) {
	for _, test := range []struct{ kind, format, want string }{
		{"datetime", "unix-millis", "r.timestamp ASC"},
		{"datetime", "rfc3339", "qt_datetime(r.timestamp) ASC"},
		{"datetime", "utc-millis", "r.timestamp ASC"},
		{"number", "", "r.timestamp ASC"},
	} {
		doc := `{"name":"s","idField":"id","fields":[{"name":"id","type":"text","bindings":{"sqlite":{"expr":"r.id"}}},{"name":"label","type":"text","sort":{"field":"timestamp"},"bindings":{"sqlite":{"expr":"r.label"}}},{"name":"timestamp","type":"` + test.kind + `","bindings":{"sqlite":{"expr":"r.timestamp","datetimeFormat":"` + test.format + `"}}}]}`
		schema, err := LoadSQLiteSchema([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := CompileSQLite(WireQuery{OrderBy: []OrderBy{{Field: "label", Dir: "asc"}}}, schema, SQLiteOptions{})
		if err != nil || !strings.Contains(compiled.OrderSQL, test.want) {
			t.Fatal(compiled, err)
		}
	}
}

func TestSQLiteSelectedArraysRequireArrayStorage(t *testing.T) {
	for _, value := range []string{"null", "{}", "[null]", "1", "broken"} {
		if _, err := sqliteOutput(value, FieldSpec{Kind: FieldTextArray}); err == nil {
			t.Fatalf("accepted %q as a selected array", value)
		}
	}
	if value, err := sqliteOutput("[]", FieldSpec{Kind: FieldTextArray}); err != nil || !reflect.DeepEqual(value, []string{}) {
		t.Fatal(value, err)
	}
}

func TestSQLiteRowEntryPointsRejectModernMetrics(t *testing.T) {
	body := []byte(`{"aggregations":[{"id":"x","op":"sum","field":"n","expression":"SUM(n)"}]}`)
	if _, err := DecodeSQLiteQuery(body); err == nil {
		t.Fatal("dropped modern row metric")
	}
	if _, err := CompileSQLite(WireQuery{Aggregations: []AggSpec{{ID: "x", Op: "sum", Field: "n", Expression: "SUM(n)"}}}, sqliteTestSchema(), SQLiteOptions{}); err == nil {
		t.Fatal("ignored modern row metric")
	}
}
