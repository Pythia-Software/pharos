package querytable

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRelativeDurationFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/relative-time.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Valid []struct {
			Value    string
			OffsetMs int64
		}
		Invalid []string
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixture.Valid {
		got, err := parseRelativeDuration(f.Value)
		if err != nil || got != f.OffsetMs {
			t.Errorf("%q: %d, %v", f.Value, got, err)
		}
	}
	for _, value := range fixture.Invalid {
		if _, err := parseRelativeDuration(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestCompileRelativeDuration(t *testing.T) {
	// Preserve server clock nanoseconds rather than rounding to milliseconds.
	now := time.Date(2026, 3, 8, 10, 0, 0, 123456789, time.UTC)
	schema := Schema{Fields: map[string]FieldSpec{
		"at":   {Expr: "e.at", Kind: FieldDatetime, ServerFilter: true},
		"id":   {Expr: "e.id", Kind: FieldNumber, ServerFilter: true},
		"text": {Expr: "e.text", Kind: FieldText, ServerFilter: true},
	}}
	q := WireQuery{Where: []WhereTerm{{Field: "at", Op: ">=", Value: "-1h"}, {Any: []WhereClause{{Field: "at", Op: "<", Value: "+0s"}, {Field: "at", Op: "<=", Value: "+8d2h10m", Negated: true}}}}}
	res, next, err := CompileAt(q, schema, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "(e.at >= $4) AND ((e.at < $5) OR (NOT (e.at <= $6) AND e.at IS NOT NULL))"
	if res.WhereSQL != want || next != 7 {
		t.Fatalf("%s, next %d", res.WhereSQL, next)
	}
	expected := []any{now.Add(-time.Hour), now, now.Add((8 * 24 * time.Hour) + (2 * time.Hour) + (10 * time.Minute))}
	if !reflect.DeepEqual(res.Args, expected) {
		t.Fatalf("args %#v", res.Args)
	}
	for _, op := range []string{"=", "!=", ">", ">=", "<", "<="} {
		result, next, err := CompileAt(WireQuery{Where: []WhereTerm{{Field: "at", Op: op, Value: "+1ms"}}}, schema, 1, now)
		if err != nil || next != 2 || !result.Args[0].(time.Time).Equal(now.Add(time.Millisecond)) {
			t.Fatalf("%s: %+v %v", op, result, err)
		}
	}
	for _, value := range []string{"+", "-", "+1h-2m", "+1h; DROP TABLE events", "+8640000000000000ms1ms", "last 7d", "now", "1h from now"} {
		if _, _, err := CompileAt(WireQuery{Where: []WhereTerm{{Field: "at", Op: ">=", Value: value}}}, schema, 1, now); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	text, _, err := CompileAt(WireQuery{Where: []WhereTerm{{Field: "text", Op: "=", Value: "-1h"}}}, schema, 1, now)
	if err != nil || text.Args[0] != "-1h" {
		t.Fatal("duration coercion affected a text field")
	}
	if _, _, err := CompileAt(WireQuery{Where: []WhereTerm{{Field: "id", Op: ">=", Value: "-1h"}}}, schema, 1, now); err == nil {
		t.Fatal("duration accepted on a number field")
	}
	if _, _, err := CompileAt(WireQuery{Where: []WhereTerm{{Field: "at", Op: ">=", Value: "+1ms"}}}, schema, 1, time.UnixMilli(MaxRelativeTimeMilliseconds)); err == nil {
		t.Fatal("accepted timestamp overflow")
	}
	// Absolute comparisons retain their existing coercion.
	absolute, _, err := CompileAt(WireQuery{Where: []WhereTerm{{Field: "at", Op: "=", Value: "2026-03-08T03:00:00-07:00"}}}, schema, 1, now)
	if err != nil || !absolute.Args[0].(time.Time).Equal(time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("absolute datetime changed")
	}
}

func TestCompileUsesOneServerClock(t *testing.T) {
	schema := Schema{Fields: map[string]FieldSpec{"at": {Expr: "e.at", Kind: FieldDatetime, ServerFilter: true}}}
	q := WireQuery{Where: []WhereTerm{{Field: "at", Op: ">=", Value: "+0s"}, {Field: "at", Op: "<=", Value: "+0s"}}}
	before := time.Now()
	result, _, err := Compile(q, schema, 1)
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	first, second := result.Args[0].(time.Time), result.Args[1].(time.Time)
	if !first.Equal(second) || first.Before(before) || first.After(after) {
		t.Fatalf("clocks: %s, %s", first, second)
	}
	if strings.Contains(result.WhereSQL, "+0s") {
		t.Fatal("raw duration entered SQL")
	}
}
