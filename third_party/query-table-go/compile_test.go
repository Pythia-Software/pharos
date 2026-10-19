package querytable

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const schemaJSON = `{
  "name": "runs",
  "idField": "id",
  "tiebreakSort": [{"field": "id", "dir": "desc"}],
  "fields": [
    {"name": "id", "label": "ID", "type": "number", "bindings": {"postgres": {"expr": "r.id"}}},
    {"name": "case_name", "label": "Case", "type": "text", "bindings": {"postgres": {"expr": "r.case_name"}}},
    {"name": "restricted_name", "label": "Restricted", "type": "text", "filter": {"ops": ["="]},
      "bindings": {"postgres": {"expr": "r.restricted_name"}}},
    {"name": "overall", "label": "Overall", "type": "enum", "bindings": {"postgres": {"expr": "r.overall"}}},
    {"name": "total_ms", "label": "Total", "type": "number", "bindings": {"postgres": {"expr": "r.total_ms"}}},
    {"name": "is_starred", "label": "Star", "type": "bool", "sort": {"field": "is_starred"},
      "bindings": {"postgres": {"expr": "(w.tags ? 'x')", "synthetic": true}}},
    {"name": "function_names", "label": "Fns", "type": "textarray", "bindings": {"postgres": {"expr": "w.fn_names"}}},
    {"name": "error_codes", "label": "Errors", "type": "textarray", "filter": {"pushdown": false},
      "bindings": {"postgres": {"expr": "r.error_codes"}}},
    {"name": "details", "label": "D", "type": "text", "source": "derived"}
  ]
}`

func mustSchema(t *testing.T) Schema {
	t.Helper()
	s, err := LoadSchema([]byte(schemaJSON))
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	return s
}

func TestLoadSchema_skipsDerived(t *testing.T) {
	s := mustSchema(t)
	if _, ok := s.Fields["details"]; ok {
		t.Error("derived field should be absent from the backend schema")
	}
	if len(s.Fields) != 8 {
		t.Errorf("want 8 backend fields, got %d", len(s.Fields))
	}
	if s.Fields["error_codes"].ServerFilter {
		t.Error("error_codes has pushdown:false → ServerFilter should be false")
	}
	if !reflect.DeepEqual(s.Fields["restricted_name"].FilterOps, []string{"="}) {
		t.Errorf("restricted_name FilterOps = %#v", s.Fields["restricted_name"].FilterOps)
	}
}

func TestCompile_where(t *testing.T) {
	s := mustSchema(t)
	q := WireQuery{Where: []WhereTerm{
		{Field: "overall", Op: "=", Value: "FAIL"},
		{Field: "total_ms", Op: ">", Value: "100"},
		{Field: "function_names", Op: "includes", Value: "SUM"},
	}}
	res, next, err := Compile(q, s, 1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := "(r.overall = $1) AND (r.total_ms > $2) AND " +
		"(EXISTS (SELECT 1 FROM unnest(w.fn_names) AS _e WHERE LOWER(_e) = LOWER($3)))"
	if res.WhereSQL != want {
		t.Errorf("WhereSQL\n got: %s\nwant: %s", res.WhereSQL, want)
	}
	if next != 4 {
		t.Errorf("next idx = %d, want 4", next)
	}
	wantArgs := []any{"FAIL", float64(100), "SUM"}
	if !reflect.DeepEqual(res.Args, wantArgs) {
		t.Errorf("Args = %#v, want %#v", res.Args, wantArgs)
	}
}

func TestCompile_rejectsClientOnlyFilter(t *testing.T) {
	s := mustSchema(t)
	_, _, err := Compile(WireQuery{Where: []WhereTerm{{Field: "error_codes", Op: "includes", Value: "x"}}}, s, 1)
	if err == nil || !strings.Contains(err.Error(), "not server-filterable") {
		t.Errorf("want not-server-filterable error, got %v", err)
	}
}

func TestCompile_opMatrix(t *testing.T) {
	s := mustSchema(t)
	// '>' is invalid on an enum.
	if _, _, err := Compile(WireQuery{Where: []WhereTerm{{Field: "overall", Op: ">", Value: "x"}}}, s, 1); err == nil {
		t.Error("want error for '>' on enum")
	}
	// is_null is valid on bool (nullity on every type).
	res, _, err := Compile(WireQuery{Where: []WhereTerm{{Field: "is_starred", Op: "is_null", Value: ""}}}, s, 1)
	if err != nil || res.WhereSQL != "((w.tags ? 'x') IS NULL)" {
		t.Errorf("bool is_null: sql=%q err=%v", res.WhereSQL, err)
	}
}

func TestCompile_stringLength(t *testing.T) {
	s := mustSchema(t)
	q := WireQuery{Where: []WhereTerm{
		{Field: "case_name", Op: "length_gt", Value: "5"},
		{Field: "case_name", Op: "length_lt", Value: "10"},
		{Field: "case_name", Op: "length_eq", Value: "0"},
	}}
	res, next, err := Compile(q, s, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.WhereSQL != "(CHAR_LENGTH(r.case_name) > $2::bigint) AND (CHAR_LENGTH(r.case_name) < $3::bigint) AND (CHAR_LENGTH(r.case_name) = $4::bigint)" {
		t.Errorf("unexpected SQL: %s", res.WhereSQL)
	}
	if !reflect.DeepEqual(res.Args, []any{int64(5), int64(10), int64(0)}) || next != 5 {
		t.Errorf("args = %#v, next = %d", res.Args, next)
	}
	large, _, err := Compile(WireQuery{Where: []WhereTerm{{Field: "case_name", Op: "length_lt", Value: "3000000000"}}}, s, 1)
	if err != nil || large.WhereSQL != "(CHAR_LENGTH(r.case_name) < $1::bigint)" || !reflect.DeepEqual(large.Args, []any{int64(3000000000)}) {
		t.Errorf("large length: result = %#v, err = %v", large, err)
	}
	for _, value := range []string{"-1", "1.5", "abc", "01"} {
		_, _, err := Compile(WireQuery{Where: []WhereTerm{{Field: "case_name", Op: "length_gt", Value: value}}}, s, 1)
		if err == nil {
			t.Errorf("accepted invalid length %q", value)
		}
	}
	_, _, err = Compile(WireQuery{Where: []WhereTerm{{Field: "overall", Op: "length_gt", Value: "2"}}}, s, 1)
	if err == nil {
		t.Error("length comparison should be text-only")
	}
}

func TestCompile_enforcesPerFieldOperatorOverride(t *testing.T) {
	s := mustSchema(t)
	_, _, err := Compile(WireQuery{Where: []WhereTerm{{
		Field: "restricted_name", Op: "matches_regex", Value: "^item-",
	}}}, s, 1)
	if err == nil || !strings.Contains(err.Error(), `op "matches_regex" is not enabled`) {
		t.Fatalf("want per-field operator rejection, got %v", err)
	}

	if _, _, err := Compile(WireQuery{Where: []WhereTerm{{
		Field: "restricted_name", Op: "=", Value: "item-1",
	}}}, s, 1); err != nil {
		t.Fatalf("configured operator should compile: %v", err)
	}
}

func TestCompile_orGroup(t *testing.T) {
	s := mustSchema(t)
	q := WireQuery{Where: []WhereTerm{
		{Any: []WhereClause{{Field: "overall", Op: "=", Value: "PASS"}, {Field: "overall", Op: "=", Value: "FAIL"}}},
		{Field: "total_ms", Op: ">", Value: "100"},
	}}
	res, next, err := Compile(q, s, 1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := "((r.overall = $1) OR (r.overall = $2)) AND (r.total_ms > $3)"
	if res.WhereSQL != want {
		t.Errorf("WhereSQL\n got: %s\nwant: %s", res.WhereSQL, want)
	}
	if next != 4 {
		t.Errorf("next idx = %d, want 4", next)
	}
	if !reflect.DeepEqual(res.Args, []any{"PASS", "FAIL", float64(100)}) {
		t.Errorf("Args = %#v", res.Args)
	}
}

func TestCompile_negatedLiteralIsNullExclusive(t *testing.T) {
	s := mustSchema(t)
	q := WireQuery{Where: []WhereTerm{{Field: "case_name", Op: "contains", Value: "x", Negated: true}}}
	res, _, err := Compile(q, s, 1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := "(NOT (POSITION(LOWER($1) IN LOWER(r.case_name)) > 0) AND r.case_name IS NOT NULL)"
	if res.WhereSQL != want {
		t.Errorf("WhereSQL\n got: %s\nwant: %s", res.WhereSQL, want)
	}
	if !reflect.DeepEqual(res.Args, []any{"x"}) {
		t.Errorf("Args = %#v", res.Args)
	}
}

func TestCompile_alwaysTrueDisjunctDropsGroup(t *testing.T) {
	s := mustSchema(t)
	// An empty-value "contains" is a no-op; ORed with anything the group is
	// always true and must emit no SQL (mirrors applyQuery).
	q := WireQuery{Where: []WhereTerm{
		{Any: []WhereClause{{Field: "case_name", Op: "contains", Value: ""}, {Field: "overall", Op: "=", Value: "PASS"}}},
	}}
	res, next, err := Compile(q, s, 1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.WhereSQL != "" {
		t.Errorf("WhereSQL = %q, want empty (group dropped)", res.WhereSQL)
	}
	if next != 1 || len(res.Args) != 0 {
		t.Errorf("next=%d args=%#v, want no placeholders consumed", next, res.Args)
	}
}

func TestCompile_orGroupValidatesEveryMember(t *testing.T) {
	s := mustSchema(t)
	// An unknown field inside a group is still reported even though the group
	// would otherwise be dropped for the empty-value member.
	q := WireQuery{Where: []WhereTerm{
		{Any: []WhereClause{{Field: "case_name", Op: "contains", Value: ""}, {Field: "nope", Op: "=", Value: "x"}}},
	}}
	if _, _, err := Compile(q, s, 1); err == nil || !strings.Contains(err.Error(), "unknown filter field") {
		t.Errorf("want unknown-field error, got %v", err)
	}
}

func TestCompile_multiSortWithTiebreak(t *testing.T) {
	s := mustSchema(t)
	q := WireQuery{OrderBy: OrderBys{
		{Field: "total_ms", Dir: "desc"},
		{Field: "is_starred", Dir: "asc", Nulls: "first"},
	}}
	res, _, err := Compile(q, s, 1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := "r.total_ms DESC NULLS LAST, (w.tags ? 'x') ASC NULLS FIRST, r.id DESC NULLS LAST"
	if res.OrderSQL != want {
		t.Errorf("OrderSQL\n got: %s\nwant: %s", res.OrderSQL, want)
	}
}

func TestCompile_regexFilterAndExtractSort(t *testing.T) {
	s := mustSchema(t)
	q := WireQuery{
		Where: []WhereTerm{
			{Field: "case_name", Op: "matches_regex", Value: `^item-\d+$`},
			{Field: "case_name", Op: "not_matches_regex", Value: `draft$`},
		},
		OrderBy: OrderBys{{
			Field: "case_name", Dir: "asc", Nulls: "first",
			Extract: &RegexExtract{Regex: `item-(\d+)`},
		}},
	}
	res, next, err := Compile(q, s, 4)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	wantWhere := `((r.case_name)::text ~ $4) AND ((r.case_name)::text !~ $5)`
	if res.WhereSQL != wantWhere {
		t.Errorf("WhereSQL\n got: %s\nwant: %s", res.WhereSQL, wantWhere)
	}
	wantOrder := "substring((r.case_name)::text FROM $6) ASC NULLS FIRST, r.id DESC NULLS LAST"
	if res.OrderSQL != wantOrder {
		t.Errorf("OrderSQL\n got: %s\nwant: %s", res.OrderSQL, wantOrder)
	}
	wantArgs := []any{`^item-\d+$`, `draft$`, `item-(\d+)`}
	if !reflect.DeepEqual(res.Args, wantArgs) || next != 7 {
		t.Errorf("args=%#v next=%d, want %#v / 7", res.Args, next, wantArgs)
	}
}

func TestCompile_select(t *testing.T) {
	s := mustSchema(t)
	res, _, err := Compile(WireQuery{Select: []string{"overall", "is_starred"}}, s, 1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := []string{`r.overall AS "overall"`, `(w.tags ? 'x') AS "is_starred"`}
	if !reflect.DeepEqual(res.SelectExprs, want) {
		t.Errorf("SelectExprs = %#v, want %#v", res.SelectExprs, want)
	}
	if _, _, err := Compile(WireQuery{Select: []string{"nope"}}, s, 1); err == nil {
		t.Error("want error for unknown select field")
	}
}

func TestCompileAggregation_grandTotalCount(t *testing.T) {
	s := mustSchema(t)
	res, err := CompileAggregation(AggSpec{ID: "c", Op: "count"}, s)
	if err != nil {
		t.Fatalf("CompileAggregation: %v", err)
	}
	want := []string{`COUNT(*) AS "value"`, `COUNT(*) AS "count"`}
	if !reflect.DeepEqual(res.SelectExprs, want) {
		t.Errorf("SelectExprs = %#v, want %#v", res.SelectExprs, want)
	}
	if res.GroupBySQL != "" {
		t.Errorf("GroupBySQL = %q, want empty (grand total)", res.GroupBySQL)
	}
}

func TestCompileAggregation_avgByTwoGroups(t *testing.T) {
	s := mustSchema(t)
	res, err := CompileAggregation(AggSpec{
		ID:      "m",
		Op:      "avg",
		Field:   "total_ms",
		GroupBy: []string{"overall", "is_starred"},
	}, s)
	if err != nil {
		t.Fatalf("CompileAggregation: %v", err)
	}
	want := []string{
		`r.overall AS "g0"`,
		`(w.tags ? 'x') AS "g1"`,
		`AVG(r.total_ms) AS "value"`,
		`COUNT(*) AS "count"`,
	}
	if !reflect.DeepEqual(res.SelectExprs, want) {
		t.Errorf("SelectExprs\n got: %#v\nwant: %#v", res.SelectExprs, want)
	}
	if res.GroupBySQL != "r.overall, (w.tags ? 'x')" {
		t.Errorf("GroupBySQL = %q", res.GroupBySQL)
	}
}

func TestCompileAggregation_opMatrixAndValidation(t *testing.T) {
	s := mustSchema(t)
	// SUM on an enum is invalid (numeric-only).
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "sum", Field: "overall"}, s); err == nil {
		t.Error("want error for sum on enum field")
	}
	// MIN on a datetime/enum/text is allowed; MIN on overall (enum) is fine.
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "min", Field: "overall"}, s); err != nil {
		t.Errorf("min on enum should be allowed: %v", err)
	}
	// count_distinct on a textarray is NOT allowed (only count).
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "count_distinct", Field: "function_names"}, s); err == nil {
		t.Error("want error for count_distinct on textarray")
	}
	// Unknown op, measure field, and group field all error.
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "median", Field: "total_ms"}, s); err == nil {
		t.Error("want error for unknown op")
	}
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "avg", Field: "nope"}, s); err == nil {
		t.Error("want error for unknown measure field")
	}
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "count", GroupBy: []string{"nope"}}, s); err == nil {
		t.Error("want error for unknown group field")
	}
	// avg requires a measure field.
	if _, err := CompileAggregation(AggSpec{ID: "x", Op: "avg"}, s); err == nil {
		t.Error("want error for avg with no measure field")
	}
}

func TestDecodeWireQuery_aggregations(t *testing.T) {
	token := b64url(`{"g":[{"id":"a1","op":"avg","field":"total_ms","groupBy":["overall"]},{"id":"a2","op":"count"}]}`)
	q, err := DecodeWireQuery(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(q.Aggregations) != 2 {
		t.Fatalf("want 2 aggregations, got %#v", q.Aggregations)
	}
	if q.Aggregations[0].Op != "avg" || q.Aggregations[0].Field != "total_ms" ||
		!reflect.DeepEqual(q.Aggregations[0].GroupBy, []string{"overall"}) {
		t.Errorf("agg[0] = %#v", q.Aggregations[0])
	}
	if q.Aggregations[1].Op != "count" || q.Aggregations[1].Field != "" {
		t.Errorf("agg[1] = %#v", q.Aggregations[1])
	}
}

func TestDecodeWireQuery_legacyShapes(t *testing.T) {
	// legacy: o is a single object, c is a string[]
	token := b64url(`{"o":{"field":"total_ms","dir":"desc"},"c":["overall","total_ms"],"l":50,"w":[{"field":"overall","op":"=","value":"FAIL"}]}`)
	q, err := DecodeWireQuery(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(q.OrderBy) != 1 || q.OrderBy[0].Field != "total_ms" {
		t.Errorf("legacy orderBy not normalized: %#v", q.OrderBy)
	}
	if !reflect.DeepEqual(q.Select, []string{"overall", "total_ms"}) {
		t.Errorf("legacy select = %#v", q.Select)
	}
	if q.Limit != 50 || len(q.Where) != 1 {
		t.Errorf("limit/where wrong: %d %#v", q.Limit, q.Where)
	}
}

func TestDecodeWireQuery_tupleSelect(t *testing.T) {
	token := b64url(`{"s":[["overall"],["total_ms",240]],"o":[{"field":"id","dir":"asc"}]}`)
	q, err := DecodeWireQuery(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(q.Select, []string{"overall", "total_ms"}) {
		t.Errorf("tuple select = %#v", q.Select)
	}
	if len(q.OrderBy) != 1 || q.OrderBy[0].Field != "id" {
		t.Errorf("orderBy = %#v", q.OrderBy)
	}
}

func TestWireQueryJSON_acceptsServerQueryNamesAndValidates(t *testing.T) {
	var q WireQuery
	err := json.Unmarshal([]byte(`{
		"select":["overall"],
		"where":[{"field":"overall","op":"=","value":"FAIL"}],
		"orderBy":[{"field":"case_name","dir":"asc","extract":{"regex":"item-(\\d+)"}}],
		"limit":50,
		"offset":10,
		"aggregations":[{"id":"count","op":"count"}]
	}`), &q)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if q.Limit != 50 || q.Offset != 10 || len(q.Where) != 1 || len(q.OrderBy) != 1 || len(q.Aggregations) != 1 {
		t.Fatalf("unexpected query: %#v", q)
	}
	if q.OrderBy[0].Extract == nil || q.OrderBy[0].Extract.Regex != `item-(\d+)` {
		t.Fatalf("regex extract did not decode: %#v", q.OrderBy[0])
	}

	if err := json.Unmarshal([]byte(`{"limit":10000}`), &q); err != nil {
		t.Fatalf("unmarshal limit above 1000: %v", err)
	}
	if q.Limit != 10_000 {
		t.Fatalf("limit = %d, want 10000", q.Limit)
	}
}

func TestDecodeWireQuery_rejectsResourceLimitViolations(t *testing.T) {
	for name, payload := range map[string]string{
		"offset": `{"f":1000001}`,
		"value":  `{"w":[{"field":"overall","op":"=","value":"` + strings.Repeat("x", maxFilterValueBytes+1) + `"}]}`,
		"regex":  `{"o":[{"field":"case_name","dir":"asc","extract":{"regex":"` + strings.Repeat("x", maxRegexBytes+1) + `"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeWireQuery(b64url(payload)); err == nil {
				t.Fatal("want resource-limit error")
			}
		})
	}
	if _, err := DecodeWireQuery(strings.Repeat("x", maxQueryTokenBytes+1)); err == nil {
		t.Fatal("want oversized token error")
	}
}

func TestDecodeWireQuery_acceptsLimitAbove1000(t *testing.T) {
	q, err := DecodeWireQuery(b64url(`{"l":10000}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if q.Limit != 10_000 {
		t.Fatalf("limit = %d, want 10000", q.Limit)
	}
}

func TestCompile_rejectsResourceLimitViolations(t *testing.T) {
	s := mustSchema(t)
	if _, _, err := Compile(WireQuery{Offset: -1}, s, 1); err == nil {
		t.Fatal("want negative offset error")
	}
}

func TestCompileDistinct(t *testing.T) {
	s := mustSchema(t)
	dc, next, err := CompileDistinct("overall", "FA", s, 1)
	if err != nil {
		t.Fatalf("CompileDistinct: %v", err)
	}
	if dc.Expr != "r.overall" {
		t.Errorf("expr = %q", dc.Expr)
	}
	if dc.SearchSQL != "POSITION(LOWER($1) IN LOWER(r.overall::text)) > 0" {
		t.Errorf("searchSQL = %q", dc.SearchSQL)
	}
	if next != 2 || !reflect.DeepEqual(dc.Args, []any{"FA"}) {
		t.Errorf("next=%d args=%#v", next, dc.Args)
	}
}

func TestCompileDistinctHasNull(t *testing.T) {
	s := mustSchema(t)
	nh, err := CompileDistinctHasNull("overall", s)
	if err != nil {
		t.Fatalf("CompileDistinctHasNull: %v", err)
	}
	if nh.IsNullExpr != "r.overall IS NULL" {
		t.Errorf("expr = %q", nh.IsNullExpr)
	}

	_, err = CompileDistinctHasNull("missing", s)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("want unknown-field error, got %v", err)
	}
}

// b64url mimics @pythia-software/query-table-core encodeQuery's charset (base64url, no padding).
func b64url(json string) string {
	s := base64.StdEncoding.EncodeToString([]byte(json))
	s = strings.TrimRight(s, "=")
	s = strings.ReplaceAll(s, "+", "-")
	s = strings.ReplaceAll(s, "/", "_")
	return s
}

func TestCaseSensitiveArrayMembership(t *testing.T) {
	s, err := LoadSchema([]byte(strings.Replace(schemaJSON, `"name": "function_names",`, `"name": "function_names", "filter": {"arrayCaseSensitive": true},`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	spec := s.Fields["function_names"]
	if !spec.ArrayCaseSensitive {
		t.Fatal("schema dropped arrayCaseSensitive")
	}
	sql, args, _, err := compileWhere(spec, "includes", "Bug", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "LOWER") || !strings.Contains(sql, "_e = $1") || args[0] != "Bug" {
		t.Fatalf("%s %v", sql, args)
	}
}
