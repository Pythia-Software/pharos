package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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
	// A full rebuild refines every message; a quick estimate stays
	// provisional until the next refinement, and a breakdown splits on it.
	if refined := query(querytable.WhereTerm{Field: "estimate", Op: "=", Value: "refined"}); len(refined) != 3 {
		t.Fatalf("refined rows = %d", len(refined))
	}
	if _, err := catalog.DB.Exec(`UPDATE authorship_message_state SET provisional=1 WHERE message_id=?`, code[0]["id"]); err != nil {
		t.Fatal(err)
	}
	if provisional := query(querytable.WhereTerm{Field: "estimate", Op: "=", Value: "provisional"}); len(provisional) != 1 || provisional[0]["id"] != code[0]["id"] || provisional[0]["estimate"] != "provisional" {
		t.Fatalf("provisional rows = %#v", provisional)
	}
	server := testServer(t, config, catalog)
	split := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Result: qt.WireQuery{Select: []string{"id"}, Limit: 1},
		Metrics: []qt.AggSpec{{ID: "split", Op: "sum", Field: "typed_words", GroupBy: []string{"estimate"}, GroupLimit: 10}}}
	var splitResult qt.QueryResultV3
	if w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/writing_messages/query-v3", split); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &splitResult) != nil || len(splitResult.Metrics) != 1 {
		t.Fatalf("estimate breakdown: %d %s", w.Code, w.Body.String())
	}
	byEstimate := map[string]float64{}
	for _, bucket := range splitResult.Metrics[0].Buckets {
		value, _ := bucket.Value.(float64)
		byEstimate[fmt.Sprint(bucket.Keys[0])] = value
	}
	if byEstimate["provisional"] != 7 || len(byEstimate) != 2 {
		t.Fatalf("typed words by estimate = %v", byEstimate)
	}
	// The Human Words heatmap first asks for each conversation's words per
	// local day. Exercise the real V3 calendar grouping used by the chart.
	heatmap := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(),
		Source: qt.WireQuery{Where: []qt.WhereTerm{{Field: "conversation_id", Op: "is_not_null"}, {Field: "conversation_id", Op: "!=", Value: ""}}},
		Result: qt.WireQuery{Select: []string{"id"}, Limit: 1},
		Metrics: []qt.AggSpec{{ID: "trend", Op: "sum", Field: "typed_words", GroupBy: []string{"sent_at", "conversation_id"}, GroupLimit: 10000,
			DateGrouping: &qt.MetricDateGrouping{Field: "sent_at", Interval: "day", TimeZone: "America/Denver", Range: "query"}}}}
	w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/writing_messages/query-v3", heatmap)
	var heatResult qt.QueryResultV3
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &heatResult) != nil || len(heatResult.Metrics) != 1 || len(heatResult.Metrics[0].Buckets) != 3 {
		t.Fatalf("conversation-day heatmap inputs: %d %s", w.Code, w.Body.String())
	}
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
	// The Human Words distribution groups by conversation before producing a
	// histogram. Exercise the budget with a small, complete population.
	override(t, &queryV3MaxGroups, 2)
	q := groupRequest("conversation_id", qt.SummaryColumn{ID: "analysis_value", Op: "sum", Field: "typed_words"})
	q.Rows = false
	q.Result.Where = []qt.WhereTerm{{Field: "analysis_value", Op: "is_not_null"}}
	q.Metrics = []qt.AggSpec{{ID: "analysis", Op: "count", Field: "analysis_value", Distribution: &qt.MetricDistribution{Kind: "histogram", Input: "[analysis_value]", Bins: 10}}}
	groups, _, err := server.executeQueryV3(t.Context(), "writing_messages", nil, PharosWritingMessagesSchema(), q)
	if err != nil || len(groups.Metrics) != 1 || groups.Metrics[0].Error != "" || len(groups.Metrics[0].Buckets) != 1 {
		t.Fatalf("conversation distribution: %+v, %v", groups.Metrics, err)
	}
	histogram, ok := groups.Metrics[0].Buckets[0].Distribution.(*qt.MetricHistogramDistribution)
	if !ok || histogram.N != 2 {
		t.Fatalf("conversation distribution omitted a group: %+v", groups.Metrics[0])
	}
	queryV3MaxGroups = 1
	if _, _, err := server.executeQueryV3(t.Context(), "writing_messages", nil, PharosWritingMessagesSchema(), q); err == nil || !strings.Contains(err.Error(), "group population exceeds host budget") {
		t.Fatalf("conversation distribution beyond the budget: %v", err)
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

func TestPeopleMessageLabelsChooseHighestConfidenceAndKeepMissingCoverage(t *testing.T) {
	c, config := testCatalog(t)
	c.now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('people-work','claude','local','people-work','People work','2026-09-01');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage,started_at,ended_at) VALUES('people-conv','people-work','claude','local','people-conv','complete','2026-09-01T12:00:00Z','2026-09-01T12:40:00Z');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('people-a','people-conv','a','user','message','Please fix the importer for quoted lines','a','2026-09-01T12:00:00Z'),
 ('people-reply','people-conv','reply','assistant','message','I will inspect the parser','r','2026-09-01T12:01:00Z'),
 ('people-b','people-conv','b','user','message','Please verify the old fixtures as well','b','2026-09-01T12:20:00Z'),
 ('people-c','people-conv','c','user','message','Keep the public file format unchanged','c','2026-09-01T12:40:00Z'),
 ('people-machine','people-conv','machine','user','message','<system_instruction>Only harness instructions here.</system_instruction>','m','2026-09-01T12:41:00Z')`)
	if err := c.RebuildAuthorship(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	before, err := c.peopleMessageLabelRows(t.Context(), fieldSet("message_id", "label_status"), []querytable.WhereTerm{{Field: "people_scope", Op: "=", Value: "human"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 4 || c.hasLabelsLedger() {
		t.Fatalf("reading missing labels changed coverage or started inference: %v", before)
	}
	if err := c.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('people-test','["P","P.request","P.request.a","P.request.b","P.control","P.control.redirect","P.frame"]');
 INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES
 ('people-a','people-conv','H-typed',63,x'00000000',1,1,'a','people-test','test','2026-09-01'),
 ('people-b','people-conv','H-typed',0,x'00000000',1,1,'b','people-test','test','2026-09-01');
 INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('people-conv','test',4,'2026-09-01');
 DELETE FROM message_labels_dirty`)
	stampExecutionDigests(t, c)
	srv := testServer(t, config, c)
	query := func(scope string, extra ...qt.WhereTerm) qt.QueryResultV3 {
		t.Helper()
		result, _, err := srv.executeQueryV3(t.Context(), "people_message_labels", nil, PharosPeopleMessageLabelsSchema(), qt.QueryRequestV3{
			Version: 3, Profile: qt.SQLiteExpressionProfile, Now: c.clock().UnixMilli(), Result: qt.WireQuery{Limit: 50, Where: append([]qt.WhereTerm{{Field: "people_scope", Op: "=", Value: scope}}, extra...)},
			Metrics: []qt.AggSpec{{ID: "families", Op: "count", Field: "message_id", GroupBy: []string{"family"}}, {ID: "messages", Op: "count", Field: "message_id", GroupBy: []string{}}, {ID: "labels", Op: "count", Field: "message_id", GroupBy: []string{"label"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, sample := range []struct {
		name, label, family string
		probs               []float32
	}{
		// Ancestors and the unaccepted frame label have higher scores;
		// only accepted most-specific labels may win.
		{"request", "P.request.b", "P.request", []float32{1, .999, .7, .9, .99, .8, 1}},
		{"control", "P.control.redirect", "P.control", []float32{1, .999, .7, .8, .99, .9, 1}},
		{"tied", "P.control.redirect", "P.control", []float32{1, .999, .9, .9, .99, .9, 1}},
		{"missing scores", "P.control.redirect", "P.control", nil},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if _, err := c.DB.ExecContext(t.Context(), `UPDATE message_labels SET probs=? WHERE message_id='people-a'`, encodeLabelProbs(sample.probs)); err != nil {
				t.Fatal(err)
			}
			for _, scope := range []string{"human", "activity", "habits"} {
				result := query(scope)
				want := int64(3)
				if scope == "human" {
					want = 4
				}
				if integer(result.Metrics[1].Buckets[0].Value) != want || result.SourceTotal != want {
					t.Fatalf("%s message population changed: %+v", scope, result)
				}
				for _, dimension := range []int{0, 2} {
					counts, sum := map[string]int64{}, int64(0)
					for _, bucket := range result.Metrics[dimension].Buckets {
						value := integer(bucket.Value)
						counts[firstString(bucket.Keys[0])] = value
						sum += value
					}
					winner := sample.family
					if dimension == 2 {
						winner = sample.label
					}
					if counts[winner] != 1 || counts["?"] != want-1 || sum != want {
						t.Fatalf("%s groups did not partition messages under %s: %v", scope, winner, counts)
					}
				}
			}
		})
	}
	filtered := query("human", qt.WhereTerm{Field: "text", Op: "contains", Value: "quoted"})
	if integer(filtered.Metrics[1].Buckets[0].Value) != 1 {
		t.Fatalf("message text filter changed population: %+v", filtered.Metrics)
	}
	rows, err := c.peopleMessageLabelRows(t.Context(), nil, []querytable.WhereTerm{{Field: "people_scope", Op: "=", Value: "human"}})
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, row := range rows {
		status[firstString(row["message_id"])] = firstString(row["label_status"])
		if row["source_conversation_id"] != "people-conv" {
			t.Fatalf("lost source: %+v", row)
		}
	}
	if status["people-b"] != "no_match" || status["people-c"] != "unlabeled" {
		t.Fatalf("missing coverage disappeared: %v", status)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('people-conv','changed')`)
	rows, err = c.peopleMessageLabelRows(t.Context(), nil, []querytable.WhereTerm{{Field: "people_scope", Op: "=", Value: "human"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["label_status"] != "unlabeled" {
			t.Fatalf("stale classification stayed accepted: %+v", row)
		}
	}
	t.Run("bounded V3 sources", func(t *testing.T) {
		c, config := testCatalog(t)
		c.now = func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) }
		execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('bounded','claude','local','bounded','Bounded messages','2026-09-11');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage,started_at,ended_at) VALUES('bounded','bounded','claude','local','bounded','complete','2026-08-01T12:00:00Z','2026-09-10T12:40:00Z');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('old-a','bounded','old-a','user','message','Please inspect the previous importer implementation','a','2026-08-01T12:00:00Z'),
 ('old-b','bounded','old-b','user','message','Preserve the file format from the previous implementation','b','2026-08-02T12:00:00Z'),
 ('recent','bounded','recent','user','message','Please check the parser and preserve its existing output','c','2026-09-10T12:30:00Z')`)
		if err := c.RebuildAuthorship(t.Context(), "test"); err != nil {
			t.Fatal(err)
		}
		// Both unrelated scopes and old messages exceed this budget. A
		// narrow request must prune them before the engine stages rows.
		override(t, &queryV3MaxPopulation, 1)
		server := testServer(t, config, c)
		for _, scope := range []string{"human", "activity", "habits"} {
			field := map[string]string{"human": "sent_at", "activity": "event_at", "habits": "started_at"}[scope]
			where := []qt.WhereTerm{{Field: "people_scope", Op: "=", Value: scope}, {Field: field, Op: ">=", Value: "-24h"}, {Field: field, Op: "<", Value: "+0s"}}
			if scope == "habits" {
				// A message sent at 12:30 belongs to the 12:00 hour.
				where[2] = qt.WhereTerm{Field: field, Op: "<=", Value: "2026-09-10T12:00:00Z"}
			}
			for _, mode := range []string{"metrics", "grouped", "distinct"} {
				t.Run(scope+"/"+mode, func(t *testing.T) {
					q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: c.clock().UnixMilli(), Result: qt.WireQuery{Where: where, Limit: 1}}
					switch mode {
					case "metrics":
						q.Metrics = []qt.AggSpec{{ID: "messages", Op: "count", Field: "message_id"}}
					case "grouped":
						q.Rows, q.Source.Where, q.Result.Where = true, where, nil
						q.Grouping = &qt.GroupingDefinition{Keys: []string{"family"}, Summaries: []qt.SummaryColumn{{ID: "message_total", Op: "count"}}}
						q.Result.Select = []string{"family", "message_total"}
					case "distinct":
						q.Source.Where, q.Result.Where = where, nil
						q.Distinct = &qt.DistinctRequestV3{Field: "family", Limit: 1}
					}
					result, _, err := server.executeQueryV3(t.Context(), "people_message_labels", nil, PharosPeopleMessageLabelsSchema(), q)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "distinct" {
						if result.Distinct == nil || len(result.Distinct.Values) != 1 {
							t.Fatalf("missing scoped distinct values: %+v", result)
						}
					} else if result.SourceTotal != 1 {
						t.Fatalf("scope/window changed the message population: %+v", result)
					}
				})
			}
		}
	})
}
