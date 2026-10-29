package querytable

import (
	"reflect"
	"testing"
	"time"
)

func TestRelativeDatetimeMapFiltering(t *testing.T) {
	schema := testSchema()
	schema.Fields["at"] = Field{Name: "at", Kind: Datetime, Filterable: true, Sortable: true}
	clock := time.Now().UTC()
	where := []WhereTerm{{Any: []WhereClause{{Field: "at", Op: ">=", Value: "-1h"}, {Field: "at", Op: "<=", Value: "-1d"}}}}
	original := []WhereTerm{{Any: append([]WhereClause{}, where[0].Any...)}}
	resolved, err := ResolveRelativeWhere(where, schema, clock)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Any[0].Value != clock.Add(-time.Hour).Format(time.RFC3339Nano) || !reflect.DeepEqual(where, original) {
		t.Fatal("clock or saved operands changed", where, resolved)
	}
	rows := []map[string]any{{"id": "recent", "at": clock.Add(-time.Minute).Format(time.RFC3339Nano)}, {"id": "middle", "at": clock.Add(-2 * time.Hour).Format(time.RFC3339Nano)}, {"id": "old", "at": clock.Add(-48 * time.Hour).Format(time.RFC3339Nano)}}
	filtered, err := Filter(rows, where, schema)
	if err != nil || len(filtered) != 2 || filtered[0]["id"] != "recent" || filtered[1]["id"] != "old" {
		t.Fatal(filtered, err)
	}
	branch := []WhereTerm{{Any: []WhereClause{{All: []WhereClause{{Field: "at", Op: ">=", Value: "-1h"}, {Field: "id", Op: "=", Value: "recent"}}}, {Field: "id", Op: "=", Value: "old"}}}}
	resolved, err = ResolveRelativeWhere(branch, schema, clock)
	if err != nil || resolved[0].Any[0].All[0].Value != clock.Add(-time.Hour).Format(time.RFC3339Nano) || branch[0].Any[0].All[0].Value != "-1h" {
		t.Fatal("AND branch literals were not resolved against the clock", resolved, err)
	}
	if filtered, err := Filter(rows, branch, schema); err != nil || len(filtered) != 2 || filtered[0]["id"] != "recent" || filtered[1]["id"] != "old" {
		t.Fatal(filtered, err)
	}
	where[0].Any[0].Value = "-bad"
	if _, err := Filter(rows, where, schema); err == nil {
		t.Fatal("invalid relative operand accepted")
	}
}
