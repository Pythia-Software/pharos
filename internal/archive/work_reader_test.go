package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedReaderWorkspace(t *testing.T, catalog *Catalog) {
	t.Helper()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := catalog.DB.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES ('work','claude','local','work','Long work','2026-09-28T00:00:00Z')`)
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES ('main','work','claude','local','main','2026-09-28T01:00:00Z')`)
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,started_at) VALUES ('agent','work','claude','local','agent','main','2026-09-28T02:00:00Z')`)
	output := strings.Repeat("line of build output\n", 2000)
	usage := `{"type":"token_usage","usage":{"input_tokens":1000,"output_tokens":200},"usage_message_id":"request-1"}`
	raw := jsonText(map[string]any{"cwd": "/repo", "message": map[string]any{"id": "request-1", "usage": map[string]any{"input_tokens": 1000, "output_tokens": 200}, "content": []any{map[string]any{"type": "thinking", "signature": strings.Repeat("s", 10000)}}}})
	messages := []struct{ id, conversation, role, kind, text, raw, call string }{
		{"m1", "main", "user", "message", "Build it; see https://github.com/acme/tool/pull/12", "", ""},
		{"m2", "main", "assistant", "tool_call", `{"tool":"Bash","input":{"command":"make"}}`, "", "c1"},
		{"m3", "main", "tool", "tool_result", jsonText(map[string]any{"content": output, "is_error": true, "exit_code": 2}), "", "c1"},
		{"m4", "main", "system", "metadata", usage, raw, ""},
		{"m5", "main", "assistant", "metadata", jsonText(map[string]any{"type": "agent_message", "message": strings.Repeat("A long reply. ", 400)}), "", ""},
		{"m6", "agent", "agent", "message", "Audit the saffron config", "", ""},
		{"m7", "agent", "assistant", "tool_call", `{"tool":"Read","input":{"file_path":"/repo/Config.toml"}}`, "", "c2"},
	}
	for index, m := range messages {
		exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,raw_text,source_order,call_id,content_hash) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			m.id, m.conversation, m.id, m.role, m.kind, m.text, nilIfEmpty(m.raw), index, nilIfEmpty(m.call), m.id)
	}
	for index, call := range []struct{ id, conversation, message, path string }{{"t1", "agent", "m7", "/repo/Config.toml"}, {"t2", "main", "m2", ""}} {
		exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,call_message_id,sequence,provider,kind,tool_name,tool_category,status,file_path) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			call.id, "work", call.conversation, call.message, index, "claude", "tool_call", "Read", "read", "ok", nilIfEmpty(call.path))
	}
}

func TestWorkOverviewOmitsMessagesAndCarriesWhatThePageScannedFor(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	overview, err := catalog.WorkOverview("work")
	if err != nil {
		t.Fatal(err)
	}
	conversations := overview["conversations"].([]map[string]any)
	if len(conversations) != 2 || conversations[0]["messages"] != nil {
		t.Fatalf("overview must list conversations without messages: %#v", conversations)
	}
	if conversations[0]["first_prompt"] != "Build it; see https://github.com/acme/tool/pull/12" || conversations[1]["first_prompt"] != "Audit the saffron config" {
		t.Fatalf("picker labels come from the first user or agent prompt: %v / %v", conversations[0]["first_prompt"], conversations[1]["first_prompt"])
	}
	if integer(conversations[0]["message_count"]) != 5 {
		t.Fatalf("message count: %v", conversations[0]["message_count"])
	}
	if jsonText(overview["discovered_prs"]) != `[{"host":"github.com","number":12,"url":"https://github.com/acme/tool/pull/12"}]` {
		t.Fatalf("pull requests: %s", jsonText(overview["discovered_prs"]))
	}
	if jsonText(overview["files"]) != `[{"conversation_id":"agent","path":"/repo/Config.toml"}]` {
		t.Fatalf("files: %s", jsonText(overview["files"]))
	}
	for _, key := range []string{"metric_ledger", "agent_sessions", "sightings"} {
		if _, ok := overview[key]; ok {
			t.Fatalf("overview carries unused %s", key)
		}
	}
}

func TestWorkConversationShortensOutputsButKeepsWhatTheReaderInterprets(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	if _, err := catalog.DB.Exec(`UPDATE messages SET kind='reasoning',reasoning_format='summary' WHERE id='m5'`); err != nil {
		t.Fatal(err)
	}
	conversation, err := catalog.WorkConversation("work", "main")
	if err != nil {
		t.Fatal(err)
	}
	if missing, _ := catalog.WorkConversation("other", "main"); missing != nil {
		t.Fatal("a conversation is served only under its own workspace")
	}
	byID := map[string]map[string]any{}
	for _, message := range conversation["messages"].([]map[string]any) {
		byID[firstString(message["id"])] = message
	}
	result := byID["m3"]
	if result["text_clipped"] != true || integer(result["text_bytes"]) <= readerBodyBytes || len(firstString(result["text"])) > 4*readerBodyBytes {
		t.Fatalf("a long tool output is shortened and says so: clipped=%v bytes=%v length=%d", result["text_clipped"], result["text_bytes"], len(firstString(result["text"])))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(firstString(result["text"])), &payload); err != nil || payload["is_error"] != true || payload["exit_code"] != float64(2) || !strings.HasSuffix(firstString(payload["content"]), "…") {
		t.Fatalf("shortened JSON keeps its structure and flags: %v %#v", err, payload)
	}
	usage := byID["m4"]
	if usage["text_clipped"] != nil || usage["raw_clipped"] != true {
		t.Fatalf("a short event keeps its text; its long source record is shortened: %#v", usage)
	}
	var source map[string]any
	if err := json.Unmarshal([]byte(firstString(usage["raw_text"])), &source); err != nil || source["cwd"] != "/repo" || jsonText(source["message"].(map[string]any)["usage"]) != `{"input_tokens":1000,"output_tokens":200}` {
		t.Fatalf("token usage and working directory survive in the shortened record: %v %s", err, firstString(usage["raw_text"]))
	}
	if byID["m5"]["text_clipped"] != nil {
		t.Fatal("an event the reader renders as prose must arrive whole")
	}
	if byID["m5"]["reasoning_format"] != "summary" {
		t.Fatalf("reader lost reasoning format: %#v", byID["m5"])
	}
	if byID["m2"]["raw_text"] != nil || byID["m2"]["text_clipped"] != nil {
		t.Fatalf("a record identical to its text is sent once: %#v", byID["m2"])
	}
	if usage := conversation["token_usage"].(tokenCounts); usage["total_tokens"] != 1200 {
		t.Fatalf("token usage is counted from whole records: %#v", usage)
	}
	original, err := catalog.MessageOriginal("m3")
	if err != nil || firstString(original["text"]) != jsonText(map[string]any{"content": strings.Repeat("line of build output\n", 2000), "is_error": true, "exit_code": 2}) {
		t.Fatalf("the original is available in full: %v", err)
	}
}

func TestWorkConversationUsesReconciledSessionUsage(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	for _, session := range []struct {
		id, status string
		tokens     int
	}{
		{"root", "reported-reconciled", 12000},
		{"embedded", "reported-reconciled", 350},
		{"alias", "in-child-conversation", 9000},
	} {
		if _, err := catalog.DB.Exec(`INSERT INTO agent_sessions
			(id,workspace_id,conversation_id,native_id,kind,provider,usage_status,total_tokens)
			VALUES(?,'work','main',?,'main','claude',?,?)`, session.id, session.id, session.status, session.tokens); err != nil {
			t.Fatal(err)
		}
	}
	// Authorship offsets refer to the full source body, even when a streamed
	// preview drops that part of a long output.
	original, err := catalog.MessageOriginal("m3")
	if err != nil {
		t.Fatal(err)
	}
	text := firstString(original["text"])
	spans := jsonText([]authorSpan{{Start: len(text) - 100, End: len(text) - 2, Category: spanPasted}})
	if _, err := catalog.DB.Exec(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,typed_words,pasted_words,spans_json)
	 VALUES('m3','main','work','2026-09-28','2026-09-28',?,0,20,?)`, len(text), spans); err != nil {
		t.Fatal(err)
	}
	for _, clip := range []bool{true, false} {
		conversation, err := catalog.workConversation("work", "main", clip)
		if err != nil {
			t.Fatal(err)
		}
		if usage := conversation["token_usage"].(tokenCounts); usage["total_tokens"] != 12350 {
			t.Fatalf("clip=%v: use reconciled usage and exclude child aliases: %v", clip, usage)
		}
		for _, message := range conversation["messages"].([]map[string]any) {
			if message["id"] != "m3" {
				continue
			}
			authorship := message["authorship"].(map[string]any)
			parts := authorship["spans"].([]map[string]any)
			if len(parts) != 1 || parts[0]["excerpt"] != excerpt(text[len(text)-100:len(text)-2], 120) {
				t.Fatalf("clip=%v: authorship must use the original text: %v", clip, parts)
			}
			if (message["text_clipped"] == true) != clip {
				t.Fatalf("clip=%v: preview markers: %v", clip, message["text_clipped"])
			}
			if !clip && message["text"] != text {
				t.Fatal("exports keep whole output")
			}
		}
	}
}

func TestWorkConversationPreservesSourceOrderAndNullTiebreakers(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	if _, err := catalog.DB.Exec(`UPDATE messages SET source_order=NULL,created_at=NULL;
		UPDATE messages SET source_order=2,created_at='2026-01-02' WHERE id='m1';
		UPDATE messages SET source_order=2 WHERE id='m2';
		UPDATE messages SET created_at='2026-01-01' WHERE id='m3';
		UPDATE messages SET source_order=-1 WHERE id='m5';
		INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,created_at,content_hash) VALUES
		('tie','main','tie','assistant','message','Tie',2,NULL,'tie'),
		('empty-date','main','empty-date','assistant','message','Empty date',NULL,'','empty-date')`); err != nil {
		t.Fatal(err)
	}
	want, err := queryMaps(catalog.DB, `SELECT id FROM messages WHERE conversation_id='main'
		ORDER BY source_order IS NULL,source_order,created_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := catalog.WorkConversation("work", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := conversation["messages"].([]map[string]any)
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, message := range got {
		if message["id"] != want[i]["id"] {
			t.Fatalf("event %d: got %v, want %v", i, message["id"], want[i]["id"])
		}
		if _, exists := message["source_order"]; exists {
			t.Fatal("sorting must preserve the reader response fields")
		}
	}
}

func TestClipReaderMessagePreservesProviderEnvelopes(t *testing.T) {
	long := strings.Repeat("text ", 1000)
	for _, tc := range []struct {
		name, kind, text string
		clipped          bool
	}{
		{"codex call in result", "tool_result", jsonText(map[string]any{"payload": map[string]any{"type": "function_call", "arguments": long}}), false},
		{"provider prose", "metadata", jsonText(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": long}}}}), false},
		{"thinking blocks", "metadata", jsonText([]any{map[string]any{"type": "thinking", "thinking": long}}), false},
		{"tool output blocks", "tool_result", jsonText(map[string]any{"content": []any{map[string]any{"type": "text", "text": long}}}), true},
		{"invalid JSON", "metadata", `{"content":"` + long, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := map[string]any{"kind": tc.kind, "text": tc.text, "raw_text": tc.text}
			clipReaderMessage(message)
			if clipped := message["text_clipped"] == true; clipped != tc.clipped {
				t.Fatalf("clipped=%v, want %v", clipped, tc.clipped)
			}
			if !tc.clipped && message["text"] != tc.text {
				t.Fatal("reader-interpreted content must arrive whole")
			}
			if tc.clipped && (message["raw_clipped"] != true || message["text_bytes"] != len(strings.TrimSpace(tc.text))) {
				t.Fatal("clipped text must retain lazy-original markers")
			}
		})
	}
}

func TestWorkReaderRoutes(t *testing.T) {
	catalog, config := testCatalog(t)
	seedReaderWorkspace(t, catalog)
	if _, err := catalog.DB.Exec(`UPDATE messages SET kind='reasoning',reasoning_format='summary' WHERE id='m5'`); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	get := func(path string) (int, map[string]any) {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+config.APIToken)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var body map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return response.Code, body
	}
	if code, body := get("/api/work/work"); code != 200 || len(body["conversations"].([]any)) != 2 {
		t.Fatalf("overview: %d %v", code, body)
	}
	if code, body := get("/api/work/work/conversations/agent"); code != 200 || len(body["messages"].([]any)) != 2 {
		t.Fatalf("conversation: %d %v", code, body)
	}
	if code, body := get("/api/work/work/conversations/main"); code != 200 {
		t.Fatalf("main conversation: %d %v", code, body)
	} else if message := body["messages"].([]any)[4].(map[string]any); message["reasoning_format"] != "summary" {
		t.Fatalf("API lost reasoning format: %v", message)
	}
	if code, body := get("/api/work/work/find?q=config.toml&show=prompts,response"); code != 200 || jsonText(body["conversations"]) != `[{"hidden":1,"hidden_kinds":["reads"],"id":"agent","shown":0}]` {
		t.Fatalf("find: %d %v", code, body)
	}
	if code, body := get("/api/work/work/find?q=saffron&show="); code != 200 || jsonText(body["conversations"]) != `[{"hidden":1,"hidden_kinds":["prompts"],"id":"agent","shown":0}]` {
		t.Fatalf("an empty show shows no kind: %d %v", code, body)
	}
	if code, _ := get("/api/work/work/find?q=(&regex=1"); code != 400 {
		t.Fatalf("invalid find: %d", code)
	}
	if code, body := get("/api/messages/m3"); code != 200 || !strings.Contains(firstString(body["text"]), "build output") {
		t.Fatalf("original: %d", code)
	}
	for _, path := range []string{"/api/work/missing", "/api/work/work/conversations/missing", "/api/messages/missing", "/api/work/work/unknown"} {
		if code, _ := get(path); code != 404 {
			t.Fatalf("%s: %d", path, code)
		}
	}
}

func TestClipReaderTextKeepsDiffsAndCutsPlainText(t *testing.T) {
	diff := "--- a\n+++ b\n" + strings.Repeat("+added line\n", 400)
	clipped, ok := clipReaderText(jsonText(map[string]any{"diff": diff, "content": strings.Repeat("x", 5000)}))
	var value map[string]any
	if !ok || json.Unmarshal([]byte(clipped), &value) != nil || value["diff"] != diff {
		t.Fatalf("diffs are counted by the reader and stay whole: %v", ok)
	}
	plain, ok := clipReaderText(strings.Repeat("é", 3000))
	if !ok || !strings.HasSuffix(plain, "…") || !json.Valid([]byte(jsonText(plain))) || len(plain) > readerBodyBytes+len("…") {
		t.Fatalf("plain text keeps its start on a rune boundary: %d", len(plain))
	}
	if short, ok := clipReaderText("short"); ok || short != "short" {
		t.Fatal("short text is untouched")
	}
}
