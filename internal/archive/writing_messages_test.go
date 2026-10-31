package archive

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func TestWritingMessagesFilterBySpanAndSplitText(t *testing.T) {
	catalog, config := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	pasted := "Why does this fail?\n```\npanic: runtime error: index out of range\n```\nPlease fix it."
	for _, record := range []WorkspaceRecord{
		{SourceID: "parser", SourceKind: "claude", Account: "local", Title: "Parser", Conversations: []ConversationRecord{{
			Initiation: &InitiationRecord{Channel: "desktop", Machine: &MachineRecord{"laptop", "Laptop", "device"}, ExecutionMachine: &MachineRecord{"laptop", "Laptop", "device"}},
			NativeID:   "parser", Provider: "claude", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "p1", Role: "user", Kind: "message", Text: "Fix the parser for nested tables", CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true},
				{NativeID: "p2", Role: "user", Kind: "message", Text: pasted, CreatedAt: "2026-09-08T12:00:00.000Z", Selected: true},
			}}}},
		{SourceID: "review-child", SourceKind: "codex", Account: "local", Title: "Review helper", Conversations: []ConversationRecord{{
			Initiation: &InitiationRecord{ExecutionMachine: &MachineRecord{"remote", "Remote worker", "cloud"}},
			NativeID:   "child", Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{
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
	if _, err := catalog.DB.Exec(`UPDATE conversations SET parent_id=(SELECT id FROM conversations WHERE native_id='parser'),agent_depth=1,agent_path='/root/worker' WHERE native_id='child'`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ensureOrigins(context.Background()); err != nil {
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
	if len(code) != 1 || code[0]["text"] != pasted || integer(code[0]["typed_words"]) != 7 || integer(code[0]["pasted_words"]) != 7 {
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
	server := testServer(t, config, catalog)
	for _, sample := range []struct {
		field, value string
		count        int
	}{{"channel", "desktop", 3}, {"channels", "desktop", 3}, {"machines", "laptop", 3}, {"execution_machines", "remote", 1}, {"execution_machine_id", "remote", 1}} {
		result, err := writingMessageDataset.Rows(context.Background(), catalog.DB, querytable.Query{
			Select: []string{"id", sample.field}, Where: []querytable.WhereTerm{{Field: sample.field, Op: "=", Value: sample.value}}, Limit: 10,
		}, schema)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != sample.count || len(result.Rows) != sample.count {
			t.Fatalf("%s=%s lost known message matches: %#v", sample.field, sample.value, result)
		}
		for _, row := range result.Rows {
			if row[sample.field] != sample.value {
				t.Fatalf("message %s attribution: %#v", sample.field, row)
			}
		}
		grouped := groupRequest(sample.field, qt.SummaryColumn{ID: "messages", Op: "count"})
		grouped.Source.Where = []qt.WhereTerm{{Field: sample.field, Op: "=", Value: sample.value}}
		groups, _, err := server.executeQueryV3(t.Context(), "writing_messages", nil, PharosWritingMessagesSchema(), grouped)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups.Rows) != 1 || groups.Rows[0][sample.field] != sample.value || integer(groups.Rows[0]["messages"]) != int64(sample.count) {
			t.Fatalf("message %s grouping lost known membership: %#v", sample.field, groups)
		}
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
	// Every reason in the catalog renders to its own rule, and every rule
	// spanRule can produce is a Rules option, and every option a rule.
	rules := []string{spanRule(authorSpan{Category: spanTyped})}
	for _, reason := range spanReasons {
		text := reason.with("3 sample particulars").text()
		if rule := spanRule(authorSpan{Category: spanPasted, Reason: text}); rule != reason.rule {
			t.Errorf("reason %q has rule %q, want %q", text, rule, reason.rule)
		}
		rules = append(rules, reason.rule)
	}
	for _, rule := range rules {
		if !slices.Contains(options, rule) {
			t.Errorf("rule %q is not a Rules option", rule)
		}
	}
	for _, option := range options {
		if !slices.Contains(rules, option) {
			t.Errorf("Rules option %q is no classifier rule", option)
		}
	}
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
