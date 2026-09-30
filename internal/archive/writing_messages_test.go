package archive

import (
	"context"
	"encoding/json"
	"slices"
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
	reasons := []string{"", "Launched with claude -p by a command", "Sent by a headless claude -p run", "Sent by a codex exec run", "Sent by a headless agy -p run",
		"Sent by automation nightly", "Sent by another agent session", "Sent by TL1 orchestration", "Sub-agent conversation",
		"Sent by a headless claude -p or SDK run Conductor did not start", "Harness context", "Repeated prompt: sent in 6 sessions on 4 days", "Slash command",
		"Attachment reference", "Generated page feedback", "Code block", "Quoted with >", "Matches agent output from 3h earlier", "Already sent 2d earlier",
		"Line already sent in other conversations", "Sent 4s after the previous message, faster than typing", "Log, trace, code, or data", "Table",
		"Formatted like agent output", "Mostly identifiers, paths, or code", "Path, URL, or ID"}
	for _, label := range harnessLabels {
		reasons = append(reasons, label)
	}
	for _, reason := range reasons {
		if rule := spanRule(authorSpan{Category: spanTyped, Reason: reason}); !slices.Contains(options, rule) {
			t.Errorf("rule %q (reason %q) is not a Rules option", rule, reason)
		}
	}
}
