package archive

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestWritingMessagesFilterBySpanAndSplitText(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	pasted := "Why does this fail?\n```\npanic: runtime error: index out of range\n```\nPlease fix it."
	for _, record := range []WorkspaceRecord{
		{SourceID: "parser", SourceKind: "claude", Account: "local", Title: "Parser", Conversations: []ConversationRecord{{
			NativeID: "parser", Provider: "claude", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "p1", Role: "user", Kind: "message", Text: "Fix the parser for nested tables", CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true},
				{NativeID: "p2", Role: "user", Kind: "message", Text: pasted, CreatedAt: "2026-09-08T12:00:00.000Z", Selected: true},
			}}}},
		{SourceID: "review-child", SourceKind: "codex", Account: "local", Title: "Review helper", Conversations: []ConversationRecord{{
			NativeID: "child", Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "c1", Role: "user", Kind: "message", Text: "Check every fixture file for regressions", CreatedAt: "2026-09-02T12:10:00.000Z", Selected: true},
			}}}},
	} {
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// The child thread was spawned from the parser conversation, so its
	// prompt counts toward the Parser work.
	if _, err := catalog.DB.Exec(`UPDATE conversations SET parent_id=(SELECT id FROM conversations WHERE native_id='parser') WHERE native_id='child'`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	document, err := querySchemaDocument("writing_messages")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	query := func(where ...querytable.WhereTerm) []map[string]any {
		t.Helper()
		result, err := writingMessageDataset.Rows(context.Background(), catalog.DB, querytable.Query{Where: where, Limit: 10,
			OrderBy: []querytable.OrderBy{{Field: "sent_at", Dir: "asc"}}}, schema)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != len(result.Rows) {
			t.Fatalf("total %d for %d rows", result.Total, len(result.Rows))
		}
		if err := catalog.attachAuthoredText(context.Background(), result.Rows); err != nil {
			t.Fatal(err)
		}
		return result.Rows
	}
	all := query()
	if len(all) != 3 {
		t.Fatalf("rows = %#v", all)
	}
	for _, row := range all {
		if row["title"] != "Parser" {
			t.Fatalf("row counts toward %v, not Parser: %#v", row["title"], row)
		}
	}
	child := all[1]
	if child["subagent"] != true || child["main_category"] != spanAutomated || !slices.Equal(child["rules"].([]string), []string{"Sub-agent conversation"}) {
		t.Fatalf("child = %#v", child)
	}

	code := query(querytable.WhereTerm{Field: "rules", Op: "includes", Value: "Code block"})
	if len(code) != 1 || code[0]["text"] != pasted || integer(code[0]["typed_words"]) != 7 || integer(code[0]["pasted_words"]) != 9 {
		t.Fatalf("code = %#v", code)
	}
	if categories := code[0]["categories"].([]string); !slices.Equal(categories, []string{spanTyped, spanPasted}) && !slices.Equal(categories, []string{spanPasted, spanTyped}) {
		t.Fatalf("categories = %v", categories)
	}
	// The segments rebuild the text, each labelled.
	var rebuilt strings.Builder
	labels := []string{}
	for _, segment := range code[0]["segments"].([]map[string]any) {
		rebuilt.WriteString(segment["text"].(string))
		labels = append(labels, firstString(segment["category"])+":"+firstString(segment["rule"]))
	}
	if rebuilt.String() != pasted || !slices.Equal(labels, []string{"typed:" + ruleTyped, "pasted:Code block", "typed:" + ruleTyped}) {
		t.Fatalf("segments %q = %v", rebuilt.String(), labels)
	}
	if typed := query(querytable.WhereTerm{Field: "main_category", Op: "=", Value: spanTyped}, querytable.WhereTerm{Field: "typed_share", Op: "=", Value: "100"}); len(typed) != 1 || integer(typed[0]["words"]) != 6 {
		t.Fatalf("typed = %#v", typed)
	}
	if byWork := query(querytable.WhereTerm{Field: "work_id", Op: "=", Value: firstString(all[0]["work_id"])}); len(byWork) != 3 {
		t.Fatalf("work filter kept %d rows", len(byWork))
	}
}

func TestAuthoredTextIsClippedOnARuneBoundary(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("é", authoredTextLimit)
	if _, err := catalog.DB.Exec(`INSERT INTO message_authorship_spans(message_id,position,category,rule,start_byte,end_byte,chars,words) VALUES('m',0,'typed',?,0,?,0,1)`, ruleTyped, len(text)); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{{"id": "m", "text": text}}
	if err := catalog.attachAuthoredText(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	clipped := rows[0]["text"].(string)
	segments := rows[0]["segments"].([]map[string]any)
	if rows[0]["text_truncated"] != true || len(clipped) != authoredTextLimit || len(segments) != 1 || segments[0]["text"] != clipped {
		t.Fatalf("clipped to %d bytes in %d segments", len(clipped), len(segments))
	}
}

// Every rule a span can carry is offered by the Rules filter, so the page can
// list and explain each one.
func TestSpanRulesMatchTheSchema(t *testing.T) {
	document, err := querySchemaDocument("writing_messages")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Fields []struct {
			Name   string `json:"name"`
			Filter struct {
				Values struct {
					Options []json.RawMessage `json:"options"`
				} `json:"values"`
			} `json:"filter"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatal(err)
	}
	var options []string
	for _, field := range parsed.Fields {
		if field.Name != "rules" {
			continue
		}
		for _, raw := range field.Filter.Values.Options {
			var option string
			if err := json.Unmarshal(raw, &option); err != nil {
				t.Fatalf("rule option %s is not a string", raw)
			}
			options = append(options, option)
		}
	}
	reasons := classifierReasons(t)
	if len(reasons) < 20 {
		t.Fatalf("read only %d reasons from the classifier: %q", len(reasons), reasons)
	}
	// A typed span has no reason.
	for _, reason := range append(reasons, "") {
		if rule := spanRule(authorSpan{Category: spanTyped, Reason: reason}); !slices.Contains(options, rule) {
			t.Errorf("rule %q (reason %q) is not a Rules option", rule, reason)
		}
	}
}

// classifierReasons reads every reason the authorship classifier gives a span
// from its source: the reason argument of each labeler.claim call, following
// variables, parameters and helper functions to the strings they hold.
// Particulars, such as counts, durations and names, become "1". A reason it
// cannot follow fails the test, rather than going unchecked.
func classifierReasons(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("authorship*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	functions := map[string]*ast.FuncDecl{}
	literals := map[string]*ast.FuncLit{}
	tables := map[string]*ast.CompositeLit{}
	calls := []*ast.CallExpr{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if node.Recv == nil {
					functions[node.Name.Name] = node
				}
			case *ast.AssignStmt:
				for index, value := range node.Rhs {
					if literal, ok := value.(*ast.FuncLit); ok && index < len(node.Lhs) {
						literals[node.Lhs[index].(*ast.Ident).Name] = literal
					}
				}
			case *ast.ValueSpec:
				for index, value := range node.Values {
					if table, ok := value.(*ast.CompositeLit); ok {
						tables[node.Names[index].Name] = table
					}
				}
			case *ast.CallExpr:
				calls = append(calls, node)
			}
			return true
		})
	}
	callee := func(call *ast.CallExpr) string {
		switch function := call.Fun.(type) {
		case *ast.Ident:
			return function.Name
		case *ast.SelectorExpr:
			return function.Sel.Name
		}
		return ""
	}
	verb := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
	var values func(ast.Expr) []string
	values = func(expr ast.Expr) []string {
		switch expr := expr.(type) {
		case *ast.BasicLit:
			if value, err := strconv.Unquote(expr.Value); err == nil {
				return []string{value}
			}
		case *ast.ParenExpr:
			return values(expr.X)
		case *ast.BinaryExpr:
			if expr.Op == token.ADD {
				joined := []string{}
				for _, left := range values(expr.X) {
					for _, right := range values(expr.Y) {
						joined = append(joined, left+right)
					}
				}
				return joined
			}
		case *ast.IndexExpr:
			// A lookup in a table these files declare is one of its values.
			if name, ok := expr.X.(*ast.Ident); ok && tables[name.Name] != nil {
				found := []string{}
				for _, element := range tables[name.Name].Elts {
					found = append(found, values(element.(*ast.KeyValueExpr).Value)...)
				}
				return found
			}
			return []string{"1"}
		case *ast.CallExpr:
			switch name := callee(expr); {
			case name == "Sprintf":
				found := []string{}
				for _, format := range values(expr.Args[0]) {
					found = append(found, verb.ReplaceAllString(format, "1"))
				}
				return found
			case name == "defaultString":
				found := []string{}
				for _, argument := range expr.Args {
					found = append(found, values(argument)...)
				}
				return found
			case functions[name] != nil:
				found := []string{}
				ast.Inspect(functions[name].Body, func(node ast.Node) bool {
					if _, nested := node.(*ast.FuncLit); nested {
						return false
					}
					if result, ok := node.(*ast.ReturnStmt); ok && len(result.Results) > 0 {
						found = append(found, values(result.Results[0])...)
					}
					return true
				})
				return found
			}
			return []string{"1"}
		case *ast.Ident:
			if expr.Obj == nil {
				break
			}
			switch declaration := expr.Obj.Decl.(type) {
			case *ast.AssignStmt:
				for index, name := range declaration.Lhs {
					if name.(*ast.Ident).Name == expr.Name && index < len(declaration.Rhs) {
						return values(declaration.Rhs[index])
					}
				}
			case *ast.Field:
				// A parameter of a function literal holds what its callers pass.
				for variable, literal := range literals {
					position := 0
					for _, field := range literal.Type.Params.List {
						for range field.Names {
							if field == declaration {
								found := []string{}
								for _, call := range calls {
									if callee(call) == variable && position < len(call.Args) {
										found = append(found, values(call.Args[position])...)
									}
								}
								return found
							}
							position++
						}
					}
				}
			}
		}
		t.Fatalf("cannot follow the reason at %s; teach classifierReasons this form", fset.Position(expr.Pos()))
		return nil
	}
	reasons := []string{}
	for _, call := range calls {
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "claim" && len(call.Args) == 5 {
			for _, reason := range values(call.Args[3]) {
				if reason != "" && !slices.Contains(reasons, reason) {
					reasons = append(reasons, reason)
				}
			}
		}
	}
	return reasons
}

func TestSteeringCuesOnlyMatchAuthoredSpans(t *testing.T) {
	catalog, _ := testCatalog(t)
	type span struct {
		category   string
		start, end int
	}
	cases := []struct {
		name, text           string
		spans                []span
		typed                int
		correction, positive bool
	}{
		{"several cues count once", "YOU MISUNDERSTOOD; I meant the other path. Great work, thank you!", []span{{"typed", 0, 62}}, 12, true, true},
		{"negation remains literal", "I never said great work; that's not what I asked.", []span{{"typed", 0, 48}}, 10, true, true},
		{"copied cues cannot qualify", "Please read: I meant the index. Great work.", []span{{"typed", 0, 12}, {"pasted", 12, 41}}, 2, false, false},
		{"machine cues cannot qualify", "I meant the index. Great work.", []span{{"automated", 0, 29}}, 0, false, false},
		{"own cue in mixed message", "I meant the index.\n```great work```", []span{{"typed", 0, 17}, {"pasted", 17, 33}}, 4, true, false},
		{"utf8 prefix offsets are bytes", "🧭 café — YOU MISSED the index. Great work.", []span{{"pasted", 0, len("🧭 café — ")}, {"typed", len("🧭 café — "), len("🧭 café — YOU MISSED the index. Great work.")}}, 7, true, true},
		{"separate typed spans do not bridge", "I meant", []span{{"typed", 0, 2}, {"typed", 2, 7}}, 2, false, false},
		{"missing spans are unknown evidence", "I meant the index.", nil, 4, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Use byte-aligned test endpoints, with the last span covering remaining text.
			for i := range tc.spans {
				if i == len(tc.spans)-1 {
					tc.spans[i].end = len(tc.text)
				}
			}
			rows := []string{}
			args := []any{}
			for _, s := range tc.spans {
				rows = append(rows, "SELECT 'message' message_id, ? category, ? start_byte, ? end_byte")
				args = append(args, s.category, s.start, s.end)
			}
			if len(rows) == 0 {
				rows = append(rows, "SELECT 'message' message_id, '' category, 0 start_byte, 0 end_byte WHERE 0")
			}
			sql := "WITH message_authorship_spans AS (" + strings.Join(rows, " UNION ALL ") + "), ma AS (SELECT 'message' message_id, ? typed_words), m AS (SELECT ? text) SELECT " + writingMessageDataset.columns["correction_cue"] + "," + writingMessageDataset.columns["positive_cue"] + " FROM ma,m"
			args = append(args, tc.typed, tc.text)
			var correction, positive bool
			if err := catalog.DB.QueryRow(sql, args...).Scan(&correction, &positive); err != nil {
				t.Fatal(err)
			}
			if correction != tc.correction || positive != tc.positive {
				t.Fatalf("cues = (%v,%v), want (%v,%v)", correction, positive, tc.correction, tc.positive)
			}
		})
	}
}
