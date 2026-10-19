package querytable

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCompileArrayNullity(t *testing.T) {
	schema := Schema{Fields: map[string]FieldSpec{"tags": {Name: "tags", Kind: FieldTextArray, Expr: "tags", ServerFilter: true}}}
	for _, test := range []struct {
		op      string
		negated bool
		want    string
	}{
		{"is_null", false, "COALESCE(cardinality(tags), 0) = 0"},
		{"is_not_null", false, "COALESCE(cardinality(tags), 0) > 0"},
		{"includes", true, "AND COALESCE(cardinality(tags), 0) > 0"},
	} {
		compiled, _, err := Compile(WireQuery{Where: []WhereTerm{{Field: "tags", Op: test.op, Value: "a", Negated: test.negated}}}, schema, 1)
		if err != nil || !strings.Contains(compiled.WhereSQL, test.want) {
			t.Fatalf("%s: sql=%q, err=%v", test.op, compiled.WhereSQL, err)
		}
	}
}

func TestSetFilterPostgres(t *testing.T) {
	if os.Getenv("QUERY_TABLE_TEST_POSTGRES") != "1" {
		t.Skip("set QUERY_TABLE_TEST_POSTGRES=1 and PGHOST/PGPORT/PGDATABASE to execute the PostgreSQL matrix")
	}
	schema := Schema{Fields: map[string]FieldSpec{"tags": {Name: "tags", Kind: FieldTextArray, Expr: "tags", ServerFilter: true, ArrayCaseSensitive: true}}}
	includesA := WhereClause{Field: "tags", Op: "includes", Value: "a"}
	includesB := WhereClause{Field: "tags", Op: "includes", Value: "b"}
	notA, notB := includesA, includesB
	notA.Negated, notB.Negated = true, true
	empty := WhereClause{Field: "tags", Op: "is_null", Value: ""}
	cases := []struct {
		mode  string
		where []WhereTerm
		want  string
	}{
		{"any", []WhereTerm{{Any: []WhereClause{includesA, includesB}}}, "1\n2\n3"},
		{"all", []WhereTerm{{Field: "tags", Op: "includes", Value: "a"}, {Field: "tags", Op: "includes", Value: "b"}}, "3"},
		{"none", []WhereTerm{{Any: []WhereClause{empty, notA}}, {Any: []WhereClause{empty, notB}}}, "0\n4\n5\n6"},
		{"empty", []WhereTerm{{Field: "tags", Op: "is_null", Value: ""}}, "0\n6"},
		{"nonempty", []WhereTerm{{Field: "tags", Op: "is_not_null", Value: ""}}, "1\n2\n3\n4\n5"},
		{"not_includes", []WhereTerm{{Field: "tags", Op: "includes", Value: "a", Negated: true}, {Field: "tags", Op: "includes", Value: "b", Negated: true}}, "4\n5"},
	}
	for _, test := range cases {
		t.Run(test.mode, func(t *testing.T) {
			compiled, _, err := Compile(WireQuery{Where: test.where}, schema, 1)
			if err != nil {
				t.Fatal(err)
			}
			parameters := ""
			execution := ""
			if len(compiled.Args) > 0 {
				if !reflect.DeepEqual(compiled.Args, []any{"a", "b"}) {
					t.Fatalf("unexpected bound keys: %#v", compiled.Args)
				}
				parameters = "(text, text)"
				execution = "('a', 'b')"
			}
			sql := fmt.Sprintf("CREATE TEMP TABLE qt_set_rows(id integer, tags text[]); INSERT INTO qt_set_rows VALUES (0, '{}'), (1, '{a}'), (2, '{b}'), (3, '{a,b}'), (4, '{c}'), (5, '{A}'), (6, NULL); PREPARE qt_set%s AS SELECT id FROM qt_set_rows WHERE %s ORDER BY id; EXECUTE qt_set%s;", parameters, compiled.WhereSQL, execution)
			command := exec.Command("psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1")
			command.Stdin = strings.NewReader(sql)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("PostgreSQL execution: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != test.want {
				t.Fatalf("rows = %q, want %q", got, test.want)
			}
		})
	}
}
