package archive

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func seedMessagesTable(t *testing.T) (*Catalog, Config) {
	t.Helper()
	c, config := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES
 ('messages-work','codex','local','native-work','Message work','2026-10-01'),
 ('messages-mirror','codex','local','native-mirror','Mirrored work','2026-10-01');
 INSERT INTO conversations(id,workspace_id,provider,model,effort,account,native_id,coverage,started_at) VALUES
 ('messages-conv','messages-work','codex','conversation-model','high','local','native-conv','complete','2026-10-01T09:00:00.000Z'),
 ('messages-mirror-conv','messages-mirror','codex','conversation-model','high','local','native-mirror-conv','complete','2026-10-01T09:00:00.000Z');
 INSERT INTO tool_mirror_workspaces(workspace_id) VALUES('messages-mirror');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,model,effort,text,content_hash,created_at,source_order,call_id,selected) VALUES
 ('m-user','messages-conv','native-user','user','message',NULL,NULL,'Please inspect the parser','user','2026-10-01T09:00:00.000Z',1,NULL,1),
 ('m-unclassified','messages-conv','native-unclassified','user','message',NULL,NULL,'Imported without authorship','unclassified','2026-10-01T09:01:00.000Z',2,NULL,1),
 ('m-assistant','messages-conv','native-assistant','assistant','message','gpt-recorded','medium','I will inspect it','assistant','2026-10-01T09:02:00.000Z',3,NULL,1),
 ('m-thinking','messages-conv','native-thinking','assistant','thinking','gpt-recorded','medium','Reasoning about the parser','thinking','2026-10-01T09:03:00.000Z',4,NULL,1),
 ('m-tool','messages-conv','native-tool','tool','tool_result',NULL,NULL,'Parser output','tool','2026-10-01T09:04:00.000Z',5,'call-1',1),
 ('m-unselected','messages-conv','native-unselected','system','metadata',NULL,NULL,'Stored source metadata','unselected','2026-10-01T09:05:00.000Z',6,NULL,0),
 ('m-mirror','messages-mirror-conv','native-mirror','assistant','message',NULL,NULL,'Mirrored copy','mirror','2026-10-01T09:06:00.000Z',1,NULL,1);
 INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,typed_words,words,spans_json,main_category,categories,rules)
 VALUES('m-user','messages-conv','messages-work','2026-10-01T09:00:00.000Z','2026-10-01',25,4,4,'[]','typed','["typed"]','["No other rule matched"]')`)
	return c, config
}

func TestMessagesTableRetainsAllRolesAndNullableAuthorship(t *testing.T) {
	c, _ := seedMessagesTable(t)
	document, err := querySchemaDocument("messages")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	result, err := messagesDataset.Rows(t.Context(), c.reader(), querytable.Query{Limit: 20, OrderBy: []querytable.OrderBy{{Field: "sent_at", Dir: "asc"}}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 6 || len(result.Rows) != 6 {
		t.Fatalf("canonical message population = %d/%d, want 6", result.Total, len(result.Rows))
	}
	if err := c.attachMessageText(t.Context(), result.Rows); err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, role, kind string }{
		{"m-user", "user", "message"}, {"m-unclassified", "user", "message"},
		{"m-assistant", "assistant", "message"}, {"m-thinking", "assistant", "thinking"},
		{"m-tool", "tool", "tool_result"}, {"m-unselected", "system", "metadata"},
	}
	for i, expected := range want {
		row := result.Rows[i]
		if row["id"] != expected.id || row["role"] != expected.role || row["kind"] != expected.kind || row["workspace_id"] != "messages-work" || row["conversation_id"] != "messages-conv" || row["conversation_native_id"] != "native-conv" || row["workspace_source_id"] != "native-work" {
			t.Fatalf("message identity or role changed: %+v", row)
		}
		if row["native_id"] != "native-"+strings.TrimPrefix(expected.id, "m-") {
			t.Fatalf("source message identity missing: %+v", row)
		}
		if i == 0 {
			if row["has_authorship"] != true || integer(row["typed_words"]) != 4 || row["main_category"] != "typed" {
				t.Fatalf("classified message lost authorship: %+v", row)
			}
		} else if row["has_authorship"] != false || row["typed_words"] != nil || row["words"] != nil || row["main_category"] != nil || row["categories"] != nil || row["typed_message"] != nil || row["work_id"] != nil {
			t.Fatalf("unclassified message gained authorship: %+v", row)
		}
		if i != 2 && i != 3 && (row["model"] != nil || row["effort"] != nil) {
			t.Fatalf("conversation-level model was assigned to message: %+v", row)
		}
	}
	if result.Rows[2]["model"] != "gpt-recorded" || result.Rows[2]["effort"] != "medium" || result.Rows[4]["call_id"] != "call-1" || result.Rows[5]["selected"] != false {
		t.Fatalf("recorded message fields changed: %+v", result.Rows)
	}
	role, err := messagesDataset.Rows(t.Context(), c.reader(), querytable.Query{Where: []querytable.WhereTerm{{Field: "role", Op: "=", Value: "assistant"}}, Limit: 10}, schema)
	if err != nil || role.Total != 2 {
		t.Fatalf("assistant filter: %d, %v", role.Total, err)
	}
	page, err := messagesDataset.Rows(t.Context(), c.reader(), querytable.Query{Limit: 2, Offset: 2, OrderBy: []querytable.OrderBy{{Field: "sent_at", Dir: "asc"}}}, schema)
	if err != nil || page.Total != 6 || len(page.Rows) != 2 || page.Rows[0]["id"] != "m-assistant" || page.Rows[1]["id"] != "m-thinking" {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestMessagesAuthorshipContextMatchesWritingMessages(t *testing.T) {
	c, _ := seedMessagesTable(t)
	execTx(t, c.DB, `INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES
 ('messages-source-repo','Source repository','2026-10-01','2026-10-01'),
 ('messages-writing-repo','Writing repository','2026-10-01','2026-10-01');
 UPDATE workspaces SET repository_id='messages-source-repo' WHERE id='messages-work';
 INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at)
 VALUES('messages-writing-work','claude','local','writing-work','messages-writing-repo','Attributed writing','2026-10-01');
 UPDATE messages SET created_at='2026-10-15T09:00:00.000Z' WHERE id='m-user';
 UPDATE message_authorship SET work_id='messages-writing-work',sent_at='2026-09-27T23:15:00.000Z',day='2026-09-27'
 WHERE message_id='m-user'`)

	load := func(name string) querytable.Schema {
		t.Helper()
		document, err := querySchemaDocument(name)
		if err != nil {
			t.Fatal(err)
		}
		schema, err := querytable.LoadSchema(document)
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}
	old, err := writingMessageDataset.Rows(t.Context(), c.reader(), querytable.Query{Limit: 20}, load("writing_messages"))
	if err != nil || old.Total != 1 || len(old.Rows) != 1 {
		t.Fatalf("writing rows = %+v, %v", old, err)
	}
	current, err := messagesDataset.Rows(t.Context(), c.reader(), querytable.Query{Limit: 20}, load("messages"))
	if err != nil || current.Total != 6 || len(current.Rows) != 6 {
		t.Fatalf("message rows = %+v, %v", current, err)
	}
	seen := map[string]bool{}
	for _, row := range current.Rows {
		id := firstString(row["id"])
		if seen[id] {
			t.Fatalf("duplicate message %q: %+v", id, current.Rows)
		}
		seen[id] = true
		if id != "m-user" {
			for _, field := range []string{"authorship_sent_at", "authorship_day", "authorship_week", "authorship_month", "authorship_title", "authorship_repository_name", "authorship_source_kind"} {
				if row[field] != nil {
					t.Fatalf("message %q has %s without authorship: %+v", id, field, row)
				}
			}
			continue
		}
		if row["native_id"] != "native-user" || row["conversation_id"] != "messages-conv" || row["workspace_id"] != "messages-work" ||
			row["sent_at"] != "2026-10-15T09:00:00.000Z" || row["title"] != "Message work" ||
			row["repository_name"] != "Source repository" || row["source_kind"] != "codex" {
			t.Fatalf("source message fields changed: %+v", row)
		}
		if row["day"] == row["authorship_day"] || row["week"] == row["authorship_week"] || row["month"] == row["authorship_month"] {
			t.Fatalf("source calendar was replaced by writing calendar: %+v", row)
		}
		if row["work_id"] != "messages-writing-work" {
			t.Fatalf("writing attribution workspace changed: %+v", row)
		}
		for alias, source := range map[string]string{
			"authorship_sent_at": "sent_at", "authorship_day": "day", "authorship_week": "week",
			"authorship_month": "month", "authorship_title": "title",
			"authorship_repository_name": "repository_name", "authorship_source_kind": "source_kind",
		} {
			if row[alias] != old.Rows[0][source] {
				t.Fatalf("%s = %v, old writing %s = %v", alias, row[alias], source, old.Rows[0][source])
			}
		}
	}
	if len(seen) != 6 || !seen["m-user"] {
		t.Fatalf("message identities changed: %+v", seen)
	}
}

func TestMessagesQueryV3GroupsSearchesAndBoundsText(t *testing.T) {
	c, config := seedMessagesTable(t)
	longText := strings.Repeat("a", authoredTextLimit-1) + "é needle beyond limit"
	if _, err := c.DB.ExecContext(t.Context(), `UPDATE messages SET text=? WHERE id='m-assistant'`, longText); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, config, c)
	capabilities := apiCall(t, s, config.APIToken, http.MethodGet, "/api/query/messages/capabilities", nil)
	if capabilities.Code != 200 || !strings.Contains(capabilities.Body.String(), `"queryCapabilities"`) {
		t.Fatalf("messages capabilities: %d %s", capabilities.Code, capabilities.Body.String())
	}
	group := groupRequest("role", qt.SummaryColumn{ID: "messages", Op: "count"})
	group.Source.Where = []qt.WhereTerm{{Field: "kind", Op: "=", Value: "message"}}
	got, _, err := s.executeQueryV3(t.Context(), "messages", nil, PharosMessagesSchema(), group)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, row := range got.Rows {
		counts[firstString(row["role"])] = integer(row["messages"])
	}
	if counts["user"] != 2 || counts["assistant"] != 1 || len(counts) != 2 {
		t.Fatalf("grouped roles: %+v", got.Rows)
	}
	if _, _, err := s.executeQueryV3(t.Context(), "messages", nil, PharosMessagesSchema(), groupRequest("text", qt.SummaryColumn{ID: "messages", Op: "count"})); err == nil {
		t.Fatal("grouped text would expose unbounded message bodies")
	}
	textMetric := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(),
		Metrics: []qt.AggSpec{{ID: "first_text", Op: "min", Field: "text"}}}
	if _, _, err := s.executeQueryV3(t.Context(), "messages", nil, PharosMessagesSchema(), textMetric); err == nil {
		t.Fatal("text metric would expose unbounded message bodies")
	}
	search, _ := json.Marshal(recordSearchSpec{Field: "text", Query: "needle beyond limit", Mode: "phrase"})
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
		Result: qt.WireQuery{Select: []string{"id", "text", "role"}, Limit: 10}}
	got, _, err = s.executeQueryV3(t.Context(), "messages", url.Values{"record_search": {string(search)}}, PharosMessagesSchema(), q)
	if err != nil || got.Total != 1 || len(got.Rows) != 1 || got.Rows[0]["id"] != "m-assistant" {
		t.Fatalf("full-body host search = %+v, %v", got, err)
	}
	if body := got.Rows[0]["text"].(string); len(body) != authoredTextLimit-1 || !utf8.ValidString(body) || strings.Contains(body, "needle") || got.Rows[0]["text_truncated"] != true {
		t.Fatalf("body was not bounded: %d bytes, truncated=%v", len(body), got.Rows[0]["text_truncated"])
	}
	item := apiCall(t, s, config.APIToken, http.MethodGet, "/api/query/messages/item?id=m-assistant", nil)
	if item.Code != 200 || strings.Contains(item.Body.String(), "needle beyond limit") {
		t.Fatalf("item leaked full body: %d %s", item.Code, item.Body.String())
	}
	var decoded struct {
		Row map[string]any `json:"row"`
	}
	if err := json.Unmarshal(item.Body.Bytes(), &decoded); err != nil || len(firstString(decoded.Row["text"])) != authoredTextLimit-1 || decoded.Row["id"] != "m-assistant" {
		t.Fatalf("item identity or limit: %+v, %v", decoded, err)
	}
	stats := apiCall(t, s, config.APIToken, http.MethodGet, "/api/query/messages/field-stats?fields=text", nil)
	if stats.Code != 200 || strings.Contains(stats.Body.String(), "needle beyond limit") {
		t.Fatalf("text statistics exposed full values: %d %s", stats.Code, stats.Body.String())
	}
	distinctValues := apiCall(t, s, config.APIToken, http.MethodGet, "/api/query/messages/distinct?field=text", nil)
	if distinctValues.Code != http.StatusBadRequest {
		t.Fatalf("text distinct exposed full values: %d", distinctValues.Code)
	}
	distinct := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Distinct: &qt.DistinctRequestV3{Field: "text", Limit: 10}}
	response := apiCall(t, s, config.APIToken, http.MethodPost, "/api/query/messages/query-v3", distinct)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "needle beyond limit") {
		t.Fatalf("V3 text distinct exposed full values: %d %s", response.Code, response.Body.String())
	}
	// Both text enrichment paths must retain byte offsets and literal output.
	// Exercise classified writing, unclassified prompts, replies and tool results.
	for _, id := range []string{"m-user", "m-unclassified", "m-assistant", "m-tool"} {
		for _, body := range []string{"  indented code\n", "null", "\t \n"} {
			if _, err := c.DB.ExecContext(t.Context(), `UPDATE messages SET text=? WHERE id=?`, body, id); err != nil {
				t.Fatal(err)
			}
			q.Result.Where = []qt.WhereTerm{{Field: "id", Op: "=", Value: id}}
			rows, _, err := s.executeQueryV3(t.Context(), "messages", nil, PharosMessagesSchema(), q)
			if err != nil || len(rows.Rows) != 1 {
				t.Fatalf("message %s rows: %+v, %v", id, rows, err)
			}
			item := apiCall(t, s, config.APIToken, http.MethodGet, "/api/query/messages/item?id="+id, nil)
			if item.Code != http.StatusOK {
				t.Fatalf("message %s item: %d %s", id, item.Code, item.Body.String())
			}
			if err := json.Unmarshal(item.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			for _, row := range []map[string]any{rows.Rows[0], decoded.Row} {
				if row["text"] != body || row["text_truncated"] != false {
					t.Fatalf("message %s text changed: got %q (%v), want %q", id, row["text"], row["text_truncated"], body)
				}
				encoded, _ := json.Marshal(row["segments"])
				var segments []struct{ Text string }
				if err := json.Unmarshal(encoded, &segments); err != nil {
					t.Fatal(err)
				}
				var joined strings.Builder
				for _, segment := range segments {
					joined.WriteString(segment.Text)
				}
				if joined.String() != body {
					t.Fatalf("message %s segments changed: got %q, want %q", id, joined.String(), body)
				}
			}
		}
	}
}
