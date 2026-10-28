package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSubagentHandbackIngestion(t *testing.T) {
	const report = "Final report.\n\nThe marigold conclusion is ready for the parent."
	path := filepath.Join(t.TempDir(), "root-session", "subagents", "agent-one.jsonl")
	lines := []string{
		jsonText(map[string]any{"sessionId": "root-session", "uuid": "assignment", "type": "user", "timestamp": "2026-10-23T12:00:00Z", "message": map[string]any{"role": "user", "content": "Review the handout"}}),
		jsonText(map[string]any{"sessionId": "root-session", "uuid": "interim", "type": "assistant", "timestamp": "2026-10-23T12:00:01Z", "message": map[string]any{"role": "assistant", "content": "Reviewing the handout"}}),
		jsonText(map[string]any{"sessionId": "root-session", "uuid": "handback-call", "type": "assistant", "timestamp": "2026-10-23T12:00:02Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call-1", "name": "SubagentHandback", "input": map[string]any{"message": report}}}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 3}}}),
		jsonText(map[string]any{"sessionId": "root-session", "uuid": "ack", "type": "user", "timestamp": "2026-10-23T12:00:03Z", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": "Report delivered successfully."}}}}),
	}
	messages := parseJSONL(t, "claude", path, lines...)
	callID := "handback-call:block:0"
	derivedID := callID + ":handback"
	byID := byNativeID(messages)
	if got := byID[derivedID]; got.Kind != "message" || got.Role != "assistant" || got.Text != report || got.RawText != "" || got.CallID != "" || got.EvidenceLocator != path+":3:block:0:input.message" {
		t.Fatalf("derived handback = %+v", got)
	}
	if byID[callID].Kind != "tool_call" || byID["ack:block:0"].Kind != "tool_result" || byID["handback-call:usage"].Kind != "metadata" {
		t.Fatalf("source events changed: %v", byID)
	}
	if _, outcome := agentPurposeOutcome(messages); outcome != report {
		t.Fatalf("parser outcome = %q", outcome)
	}
	if again := normalizeHandbackMessages(messages); len(again) != len(messages) {
		t.Fatalf("repeated normalization changed %d messages to %d", len(messages), len(again))
	}
	withoutReport := []MessageRecord{}
	for _, message := range messages {
		if message.NativeID != derivedID {
			withoutReport = append(withoutReport, message)
		}
	}
	beforeRequests, beforeCalls := buildToolLedger(withoutReport, "claude")
	afterRequests, afterCalls := buildToolLedger(messages, "claude")
	if len(beforeRequests) != len(afterRequests) || len(beforeCalls) != len(afterCalls) || jsonText(conversationTokenCounts(withoutReport)) != jsonText(conversationTokenCounts(messages)) {
		t.Fatal("handback projection changed request, tool or token accounting")
	}
	for _, tc := range []struct {
		name   string
		change func([]MessageRecord) []MessageRecord
	}{
		{"missing acknowledgement", func(items []MessageRecord) []MessageRecord {
			kept := items[:0]
			for _, item := range items {
				if item.Kind != "tool_result" {
					kept = append(kept, item)
				}
			}
			return kept
		}},
		{"failed acknowledgement", func(items []MessageRecord) []MessageRecord {
			for i := range items {
				if items[i].Kind == "tool_result" {
					items[i].Text = `{"content":"Delivery failed","is_error":true}`
				}
			}
			return items
		}},
		{"failed acknowledgement without flag", func(items []MessageRecord) []MessageRecord {
			for i := range items {
				if items[i].Kind == "tool_result" {
					items[i].Text = `{"content":"Report not delivered"}`
				}
			}
			return items
		}},
		{"undelivered acknowledgement", func(items []MessageRecord) []MessageRecord {
			for i := range items {
				if items[i].Kind == "tool_result" {
					items[i].Text = `{"content":"Report was undelivered"}`
				}
			}
			return items
		}},
		{"unsuccessful structured result", func(items []MessageRecord) []MessageRecord {
			for i := range items {
				if items[i].Kind == "tool_result" {
					items[i].Text = `{"content":[{"type":"text","text":"{\"success\":false,\"message\":\"Caller unavailable\"}"}]}`
				}
			}
			return items
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := append([]MessageRecord(nil), messages...)
			raw = tc.change(raw)
			withoutDerived := raw[:0]
			for _, m := range raw {
				if m.NativeID != derivedID {
					withoutDerived = append(withoutDerived, m)
				}
			}
			if got := byNativeID(normalizeHandbackMessages(withoutDerived))[derivedID]; got.NativeID != "" {
				t.Fatalf("unexpected report: %+v", got)
			}
		})
	}

	c, _ := testCatalog(t)
	record := WorkspaceRecord{SourceKind: "claude", SourceID: "root-session", Account: "local", Conversations: []ConversationRecord{{NativeID: "root-session:subagent:agent-one", Provider: "claude", Account: "local", AgentDepth: 1, Messages: messages}}}
	tx := seedTx(t, c.DB)
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	var conversationID string
	if err := c.DB.QueryRow("SELECT id FROM conversations WHERE native_id=?", record.Conversations[0].NativeID).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	derivedMessageID := stableID("message", conversationID, derivedID)
	overview, err := c.conversationOverview(map[string]any{"conversation_id": conversationID})
	if err != nil || firstString(overview["last_response"].(map[string]any)["message_id"]) != derivedMessageID || !strings.Contains(firstString(overview["last_response"].(map[string]any)["text"]), "marigold conclusion") {
		t.Fatalf("overview = %v, %v", overview, err)
	}
	read, err := c.conversationMessages(map[string]any{"conversation_id": conversationID, "limit": 12})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range read["items"].([]map[string]any) {
		if firstString(item["message_id"]) == derivedMessageID && firstString(item["text"]) == report {
			found = true
		}
	}
	if !found {
		t.Fatalf("default messages omit report: %v", read)
	}
	for _, surface := range []string{"assistant", "prose", "tools"} {
		result, err := c.SearchText(t.Context(), TextSearchOptions{Query: "marigold conclusion", Mode: "literal", Surface: surface, ConversationID: conversationID})
		if err != nil {
			t.Fatal(err)
		}
		want := derivedMessageID
		if surface == "tools" {
			want = stableID("message", conversationID, callID)
		}
		if !slices.Equal(resultIDs(result), []string{want}) {
			t.Fatalf("%s search = %v, want %s", surface, resultIDs(result), want)
		}
	}
	var calls, results, derived, rawUsage int
	if err := c.DB.QueryRow(`SELECT
		SUM(kind='tool_call'), SUM(kind='tool_result'), SUM(native_id=?),
		SUM(native_id='handback-call:usage' AND raw_text IS NOT NULL)
		FROM messages WHERE conversation_id=?`, derivedID, conversationID).Scan(&calls, &results, &derived, &rawUsage); err != nil || calls != 1 || results != 1 || derived != 1 || rawUsage != 1 {
		t.Fatalf("event/accounting counts = %d %d %d %d, %v", calls, results, derived, rawUsage, err)
	}
	var membership int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_messages am
		JOIN agent_sessions a ON a.id=am.agent_session_id
		WHERE a.conversation_id=? AND am.message_id=? AND a.native_id='main'`, conversationID, derivedMessageID).Scan(&membership); err != nil || membership != 1 {
		t.Fatalf("handback session membership = %d, %v", membership, err)
	}
	// Recover an old catalog's missing derived report without resetting its
	// reconciled usage, request evidence or existing source message IDs.
	tx = seedTx(t, c.DB)
	if err := deleteMessageFTS(tx, derivedMessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM messages WHERE id=?", derivedMessageID); err != nil {
		t.Fatal(err)
	}
	if err := upsertConversationDocument(tx, conversationID); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	sessions, _ := queryMaps(c.DB, "SELECT * FROM agent_sessions ORDER BY id")
	usage, _ := queryMaps(c.DB, "SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model")
	requests, _ := queryMaps(c.DB, "SELECT * FROM model_requests ORDER BY id")
	if done, err := c.BackfillMessageSearch(t.Context(), nil); err != nil || done != 1 {
		t.Fatalf("handback upgrade: done=%d err=%v", done, err)
	}
	for _, before := range []struct {
		table string
		rows  []map[string]any
	}{{"agent_sessions", sessions}, {"agent_session_usage", usage}, {"model_requests", requests}} {
		order := "id"
		if before.table == "agent_session_usage" {
			order = "agent_session_id,usage_hour,model"
		}
		after, err := queryMaps(c.DB, "SELECT * FROM "+before.table+" ORDER BY "+order)
		if err != nil || jsonText(before.rows) != jsonText(after) {
			t.Fatalf("handback upgrade changed %s: %v", before.table, err)
		}
	}
	overview, err = c.conversationOverview(map[string]any{"conversation_id": conversationID})
	if err != nil || firstString(overview["last_response"].(map[string]any)["message_id"]) != derivedMessageID {
		t.Fatalf("upgraded outcome: %v %v", overview, err)
	}
}

func textFixture(t *testing.T) *Catalog {
	c, _ := testCatalog(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Search tests','2026-10-12'),('other','claude','local','other','Other work','2026-10-12')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','c'),('other','other','claude','local','other')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('a','c','a','user','message','cache race condition timeout retry foo::bar café','a','2026-10-11T12:00:00Z'),
 ('b','c','b','assistant','message','buffer race condition redis migration','b','2026-10-12T12:00:00Z'),
 ('r','c','r','assistant','reasoning','timeout then retry','r','2026-10-12'),
 ('t','c','t','tool','tool_result','timeout secret-only 0123456789abcdef0123456789abcdef0123456789','t','2026-10-12'),
 ('o','other','o','user','message','CACHE race condition foo--bar','o','2026-10-12'),
 ('span','c','span','user','message','typed cache. pasted secret redis','span','2026-10-12')`,
		`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages ORDER BY rowid`,
		`INSERT INTO message_fts_rows SELECT message_id,rowid FROM messages_fts`,
		`INSERT INTO messages_trigram(rowid,text) SELECT f.rowid,m.text FROM messages_fts f JOIN messages m ON m.id=f.message_id WHERE m.kind IN (` + substringKinds + `)`,
		`INSERT INTO message_authorship_spans(message_id,position,category,rule,start_byte,end_byte,chars,words) VALUES('span',0,'typed','typed',0,12,12,2),('span',1,'pasted','pasted',13,32,19,3)`,
	} {
		if _, e := c.DB.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	for {
		more, e := c.indexTextSearchBatch(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		if !more {
			break
		}
	}
	return c
}
func resultIDs(r textSearchResult) []string {
	ids := []string{}
	for _, item := range r.Items {
		ids = append(ids, item.MessageID)
	}
	slices.Sort(ids)
	return ids
}

func TestTextSearchEnvelopeWhitespace(t *testing.T) {
	c, _ := testCatalog(t)
	tx := seedTx(t, c.DB)
	if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','claude','local','w','JSON search','2026-10-23');
	 INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','claude','local','c')`); err != nil {
		t.Fatal(err)
	}
	types := []string{"tool_call", "tool_result", "delegation", "delegation_result", "context"}
	bodies := map[string]string{}
	for _, kind := range types {
		body := jsonText(map[string]any{"tool": "fixture", "content": "Prior paragraph\n\nThe handout is accurate.\tThe answer follows.\rDone."})
		id, _, err := upsertMessage(tx, "c", MessageRecord{NativeID: kind, Kind: kind, Role: "assistant", Text: body, EvidenceLocator: "fixture:" + kind, Selected: true})
		if err != nil {
			t.Fatal(err)
		}
		bodies[id] = body
		if err := replaceMessageFTS(tx, id); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	for _, test := range []struct {
		query   string
		options TextSearchOptions
	}{
		{`"The handout is accurate"`, TextSearchOptions{}},
		{`"The handout is accurate"`, TextSearchOptions{CaseSensitive: true}},
		{`"The handout is accurate"`, TextSearchOptions{Separators: true}},
		{`The PRE/0 handout`, TextSearchOptions{}},
		{`"The answer follows"`, TextSearchOptions{}},
		{`Done`, TextSearchOptions{}},
	} {
		o := test.options
		o.Query, o.Surface, o.ConversationID = test.query, "all", "c"
		result, err := c.SearchText(t.Context(), o)
		if err != nil || len(result.Items) != len(types) || !result.Complete {
			t.Fatalf("%s: %+v %v", test.query, result, err)
		}
		for _, item := range result.Items {
			body := bodies[item.MessageID]
			if item.StartByte < 0 || item.EndByte > len(body) || item.EndByte <= item.StartByte || !strings.Contains(body[item.StartByte:item.EndByte], "The") && test.query != "Done" {
				t.Fatalf("range does not address original evidence: %+v", item)
			}
		}
		// The word index holds every envelope but tool output.
		legacy, err := c.searchMessages(map[string]any{"query": test.query, "conversation_id": "c"})
		if !strings.Contains(test.query, "PRE/") && (err != nil || integer(legacy["total"]) != int64(len(types)-1)) {
			t.Fatalf("legacy %s: %v %v", test.query, legacy, err)
		}
	}
	// Projection is lexical only: original JSON and literal backslashes remain
	// queryable, and quoted user examples are never rewritten.
	for _, kind := range []string{"tool_call", "context", "message", "metadata"} {
		for _, body := range []string{`{"text":"literal \\nThe token"}`, `not-json \nThe token`, `{"text":"The token"}`} {
			if got := lexicalSnippet(messageLexicalText(kind, body)); got != body {
				t.Fatalf("rewrote literal text for %s: %q -> %q", kind, body, got)
			}
		}
	}
	if got := messageLexicalText("message", `{"text":"before\nThe token"}`); got != `{"text":"before\nThe token"}` {
		t.Fatalf("rewrote a quoted user example: %s", got)
	}
	literal, err := c.SearchText(t.Context(), TextSearchOptions{Query: `\n\nThe handout`, Mode: "literal", Surface: "all", ConversationID: "c"})
	if err != nil || len(literal.Items) != len(types) {
		t.Fatalf("literal envelope lookup changed: %+v %v", literal, err)
	}
	// JSON strings remain independent phrase units, including when the left
	// block ends in an encoded newline. Boolean queries may use both blocks.
	// A tool call is word-indexed, so both the scoped scan and the index see it.
	boundaryBody := `{"content":["orchid\n","petal"],"other":"cedar","birch":"suffix"}`
	writeEnvelope := func(native, body string) string {
		t.Helper()
		tx := seedTx(t, c.DB)
		id, _, err := upsertMessage(tx, "c", MessageRecord{NativeID: native, Kind: "tool_call", Role: "assistant", Text: body, Selected: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := replaceMessageFTS(tx, id); err != nil {
			t.Fatal(err)
		}
		commitSeed(t, tx)
		return id
	}
	boundaryID := writeEnvelope("boundaries", boundaryBody)
	for _, query := range []string{`"orchid petal"`, `"cedar birch"`} {
		for _, options := range []TextSearchOptions{{}, {CaseSensitive: true}, {Separators: true}, {workspaceRows: true}} {
			options.Query, options.Surface, options.ConversationID = query, "tools", "c"
			result, err := c.SearchText(t.Context(), options)
			if err != nil || len(result.Items) != 0 || !result.Complete {
				t.Fatalf("phrase crossed JSON fields: %s %+v %v", query, result, err)
			}
		}
		legacy, err := c.searchMessages(map[string]any{"query": query, "conversation_id": "c"})
		if err != nil || integer(legacy["total"]) != 0 {
			t.Fatalf("legacy phrase crossed JSON fields: %s %v %v", query, legacy, err)
		}
	}
	result, err := c.SearchText(t.Context(), TextSearchOptions{Query: "orchid AND petal", Surface: "tools", ConversationID: "c"})
	if err != nil || !slices.Equal(resultIDs(result), []string{boundaryID}) {
		t.Fatalf("Boolean search lost independent fields: %+v %v", result, err)
	}
	// An actual match after the rejected boundary must have original offsets,
	// and no index-only field barriers may appear in snippets.
	validBody := `{"content":["orchid\n","petal","orchid petal"],"extra":"escaped \"quote\"\tThe blossom opens"}`
	validID := writeEnvelope("valid-boundary", validBody)
	for _, options := range []TextSearchOptions{{}, {CaseSensitive: true}, {Separators: true}} {
		options.Query, options.Surface, options.ConversationID = `"orchid petal"`, "tools", "c"
		result, err := c.SearchText(t.Context(), options)
		if err != nil || !slices.Equal(resultIDs(result), []string{validID}) {
			t.Fatalf("within-field phrase: %+v %v", result, err)
		}
		item := result.Items[0]
		if validBody[item.StartByte:item.EndByte] != "orchid petal" || strings.ContainsRune(item.Snippet, '\ue000') {
			t.Fatalf("invalid source offsets or snippet: %+v", item)
		}
	}
	legacy, err := c.searchMessages(map[string]any{"query": `"orchid petal"`, "conversation_id": "c"})
	if err != nil || integer(legacy["total"]) != 1 || strings.ContainsRune(jsonText(legacy), '\ue000') {
		t.Fatalf("legacy snippet: %v %v", legacy, err)
	}
	result, err = c.SearchText(t.Context(), TextSearchOptions{Query: `"The blossom opens"`, Surface: "tools", ConversationID: "c"})
	if err != nil || !slices.Equal(resultIDs(result), []string{validID}) {
		t.Fatalf("escaped quote changed JSON boundaries: %+v %v", result, err)
	}
}

func TestMessageSearchUpgradeResumesAndPreservesEvidence(t *testing.T) {
	c, _ := testCatalog(t)
	tx := seedTx(t, c.DB)
	if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','claude','local','w','Old JSON search','2026-10-23');
	 INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c1','w','claude','local','c1'),('c2','w','claude','local','c2')`); err != nil {
		t.Fatal(err)
	}
	body := jsonText(map[string]any{"tool": "Read", "content": "Previous paragraph\nThe handout is accurate"})
	for _, cid := range []string{"c1", "c2"} {
		id, _, err := upsertMessage(tx, cid, MessageRecord{NativeID: "tool", Kind: "tool_call", Role: "assistant", Text: body, RawText: "original evidence", EvidenceLocator: "fixture:" + cid, Selected: true})
		if err != nil {
			t.Fatal(err)
		}
		// Simulate the old writer's raw JSON postings and their exact mapping.
		result, err := tx.Exec("INSERT INTO messages_fts(message_id,text) VALUES(?,?)", id, body)
		if err != nil {
			t.Fatal(err)
		}
		rowid, _ := result.LastInsertId()
		if _, err := tx.Exec("INSERT INTO message_fts_rows(message_id,fts_rowid) VALUES(?,?)", id, rowid); err != nil {
			t.Fatal(err)
		}
		// Its output, which the repair must not word-index.
		if _, _, err := upsertMessage(tx, cid, MessageRecord{NativeID: "output", Kind: "tool_result", Role: "tool", Text: body, Selected: true}); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	before, err := queryMaps(c.DB, "SELECT * FROM messages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	// Unscoped, so the word index answers (a scoped search scans stored text).
	options := TextSearchOptions{Query: `"The handout is accurate"`, Surface: "tools"}
	if result, err := c.SearchText(t.Context(), options); err != nil || len(result.Items) != 0 {
		t.Fatalf("fixture should require repair: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done, err := c.BackfillMessageSearch(ctx, func(done, total int) { cancel() })
	if done != 1 || err != context.Canceled {
		t.Fatalf("cancelled upgrade: done=%d err=%v", done, err)
	}
	if pending, err := c.pendingMessageSearch(t.Context()); err != nil || pending != 1 {
		t.Fatalf("committed checkpoint missing: pending=%d err=%v", pending, err)
	}
	if result, err := c.SearchText(t.Context(), options); err != nil || len(result.Items) != 1 {
		t.Fatalf("first conversation's repair: %+v %v", result, err)
	}
	if done, err := c.BackfillMessageSearch(t.Context(), nil); err != nil || done != 1 {
		t.Fatalf("resume: done=%d err=%v", done, err)
	}
	if done, err := c.BackfillMessageSearch(t.Context(), nil); err != nil || done != 0 {
		t.Fatalf("replayed completed work: done=%d err=%v", done, err)
	}
	if result, err := c.SearchText(t.Context(), options); err != nil || len(result.Items) != 2 {
		t.Fatalf("repair missed decoded boundary: %+v %v", result, err)
	}
	var indexedOutput int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM message_fts_rows r JOIN messages m ON m.id=r.message_id WHERE m.kind='tool_result'`).Scan(&indexedOutput); err != nil || indexedOutput != 0 {
		t.Fatalf("repair word-indexed tool output: %d %v", indexedOutput, err)
	}
	after, err := queryMaps(c.DB, "SELECT * FROM messages ORDER BY id")
	if err != nil || jsonText(before) != jsonText(after) {
		t.Fatalf("lexical upgrade changed original evidence: %v", err)
	}
}

// A catalog written before tool output left the word index removes its
// postings in the background, a bounded batch a transaction, resuming from its
// cursor, then merges them away. Searches answer the same throughout.
func TestToolOutputLeavesTheWordIndex(t *testing.T) {
	c, _ := testCatalog(t)
	override(t, &toolOutputScan, 3)
	override(t, &toolOutputMergePages, 1)
	tx := seedTx(t, c.DB)
	if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','claude','local','w','Legacy postings','2026-10-28');
	 INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','claude','local','c')`); err != nil {
		t.Fatal(err)
	}
	// The old writer indexed every message with text, in rowid order.
	for i := range 8 {
		kind, role, text := "tool_result", "tool", fmt.Sprintf(`{"content":"quokka output %d"}`, i)
		if i%3 == 0 {
			kind, role, text = "message", "user", fmt.Sprintf("quokka request %d", i)
		}
		id, _, err := upsertMessage(tx, "c", MessageRecord{NativeID: fmt.Sprint(i), Kind: kind, Role: role, Text: text, Selected: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := tx.Exec("INSERT INTO messages_fts(message_id,text) SELECT id,pharos_lexical_text(kind,text) FROM messages WHERE id=?", id)
		if err != nil {
			t.Fatal(err)
		}
		rowid, _ := result.LastInsertId()
		if _, err := tx.Exec("INSERT INTO message_fts_rows(message_id,fts_rowid) VALUES(?,?)", id, rowid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec("DELETE FROM meta WHERE key IN (?,?)", toolOutputRemovedKey, toolOutputMergedKey); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	before, err := queryMaps(c.DB, "SELECT * FROM messages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	searches := func() string {
		t.Helper()
		unscoped, err := c.SearchText(t.Context(), TextSearchOptions{Query: "quokka", Surface: "all"})
		if err != nil {
			t.Fatal(err)
		}
		scoped, err := c.SearchText(t.Context(), TextSearchOptions{Query: "quokka", Surface: "all", ConversationID: "c"})
		if err != nil {
			t.Fatal(err)
		}
		legacy, err := c.searchMessages(map[string]any{"query": "quokka"})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(len(unscoped.Items), len(scoped.Items), legacy["total"])
	}
	// Three requests through the word index; all eight messages by scan.
	if got := searches(); got != "3 8 3" {
		t.Fatalf("before removal: %s", got)
	}
	generation, err := c.textSearchGeneration(t.Context(), TextSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchText(t.Context(), TextSearchOptions{Query: "quokka", Surface: "all", Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	progress, err := c.toolOutputIndexProgress(t.Context())
	if err != nil || progress.Ready || progress.Phase != "removing" || progress.Done != 0 || progress.Total != 8 {
		t.Fatalf("progress before: %+v %v", progress, err)
	}
	// get_search_capabilities describes the index and the removal.
	described := func() map[string]any {
		t.Helper()
		capabilities, err := c.textSearchCapabilities(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		index := capabilities["word_index"].(map[string]any)
		if !slices.Equal(index["unindexed_kinds"].([]string), []string{"tool_result"}) || index["tool_output"] != toolOutputNotIndexed {
			t.Fatalf("capabilities: %v", index)
		}
		return index
	}
	if removal := described()["tool_output_removal"].(toolOutputProgress); removal.Ready {
		t.Fatalf("capabilities call removal done: %+v", removal)
	}
	// One batch checks three rows and commits; the next resumes after them.
	if more, err := c.removeToolOutputBatch(t.Context()); err != nil || !more {
		t.Fatalf("first batch: %v %v", more, err)
	}
	var left int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM message_fts_rows r JOIN messages m ON m.id=r.message_id WHERE m.kind='tool_result'`).Scan(&left); err != nil || left != 3 {
		t.Fatalf("after one batch %d tool results are left, %v; want 3", left, err)
	}
	if progress, err = c.toolOutputIndexProgress(t.Context()); err != nil || progress.Done != 3 {
		t.Fatalf("progress after a batch: %+v %v", progress, err)
	}
	if got := searches(); got != "3 8 3" {
		t.Fatalf("during removal: %s", got)
	}
	c.maintainToolOutputIndex(t.Context())
	if progress, err = c.toolOutputIndexProgress(t.Context()); err != nil || !progress.Ready || !described()["tool_output_removal"].(toolOutputProgress).Ready {
		t.Fatalf("progress after: %+v %v", progress, err)
	}
	var postings, mapped int
	if err := c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'output'),(SELECT COUNT(*) FROM message_fts_rows)`).Scan(&postings, &mapped); err != nil || postings != 0 || mapped != 3 {
		t.Fatalf("after removal: %d tool postings, %d mapped rows, %v", postings, mapped, err)
	}
	if _, err := c.DB.Exec("INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')"); err != nil {
		t.Fatalf("word index integrity: %v", err)
	}
	if got := searches(); got != "3 8 3" {
		t.Fatalf("after removal: %s", got)
	}
	// Removal reorders no remaining match: open cursors continue.
	if after, err := c.textSearchGeneration(t.Context(), TextSearchOptions{}); err != nil || after != generation {
		t.Fatalf("generation moved from %s to %s: %v", generation, after, err)
	}
	if next, err := c.SearchText(t.Context(), TextSearchOptions{Query: "quokka", Surface: "all", Limit: 1, Cursor: page.NextCursor}); err != nil || len(next.Items) != 1 {
		t.Fatalf("cursor across removal: %+v %v", next, err)
	}
	after, err := queryMaps(c.DB, "SELECT * FROM messages ORDER BY id")
	if err != nil || jsonText(before) != jsonText(after) {
		t.Fatalf("removal changed messages: %v", err)
	}
	// Done stays done.
	if more, err := c.toolOutputIndexStep(t.Context()); err != nil || more {
		t.Fatalf("finished migration ran again: %v %v", more, err)
	}
}

func TestTextSearchReasoningBackfill(t *testing.T) {
	for _, name := range []string{"root reply", "embedded child reply", "new delegation", "redacted first block", "empty first block"} {
		t.Run(name, func(t *testing.T) {
			c, _ := testCatalog(t)
			tx := seedTx(t, c.DB)
			if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','claude','local','w','Reasoning','2026-10-18');
			 INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,agent_depth,started_at) VALUES('c','w','claude','local','c','opus',2,'2026-10-18T17:00:00Z'),('linked','w','claude','local','linked','opus',3,'2026-10-18T17:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Now I understand"},{"type":"text","text":"Reply"}],"usage":{"input_tokens":10,"output_tokens":4}}}`
			messages := []MessageRecord{{NativeID: "event", Role: "assistant", Kind: "message", Text: "Reply", RawText: body, Selected: true}}
			if name == "embedded child reply" {
				messages[0].ParentNativeID = "spawn"
				messages = append([]MessageRecord{{NativeID: "spawn", Role: "assistant", Kind: "delegation", CallID: "child", Text: `{"tool":"Task","input":{"prompt":"Investigate"}}`}}, messages...)
			} else if name == "new delegation" {
				messages[0].Kind, messages[0].Text, messages[0].RawText = "metadata", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Now I understand"},{"type":"tool_use","id":"child","name":"Task","input":{"prompt":"Investigate"}}]}}`, ""
				messages = append(messages, MessageRecord{NativeID: "child-reply", ParentNativeID: "child", Role: "assistant", Kind: "message", Text: "Found it"})
			} else if name == "redacted first block" || name == "empty first block" {
				block := `{"type":"redacted_thinking","data":"secret"}`
				if name == "empty first block" {
					block = `{"type":"thinking","thinking":" "}`
				}
				messages[0].Kind, messages[0].Text, messages[0].RawText = "metadata", `{"type":"assistant","message":{"role":"assistant","content":[`+block+`,{"type":"text","text":"Reply"}],"usage":{"input_tokens":10,"output_tokens":4}}}`, ""
			}
			for i, message := range messages {
				message.SourceOrder = i
				id, _, err := upsertMessage(tx, "c", message)
				if err != nil {
					t.Fatal(err)
				}
				if err := replaceMessageFTS(tx, id); err != nil {
					t.Fatal(err)
				}
			}
			conversation := ConversationRecord{Provider: "claude", Model: "opus", AgentDepth: 2, StartedAt: "2026-10-18T17:00:00Z", Messages: messages}
			if err := replaceAgentSessions(tx, "w", "c", conversation); err != nil {
				t.Fatal(err)
			}
			// Reconciled group usage and linked children must survive a text upgrade.
			if err := insertSessionRemainder(tx, stableID("agent-session", "c", "main"), usageHour(conversation.StartedAt), "opus", tokenCounts{"input_tokens": 20, "output_tokens": 2, "total_tokens": 22}); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE agent_sessions SET child_conversation_id='linked',usage_status='in-child-conversation' WHERE conversation_id='c' AND native_id='child'`); err != nil {
				t.Fatal(err)
			}
			commitSeed(t, tx)
			beforeSessions, err := queryMaps(c.DB, `SELECT * FROM agent_sessions WHERE conversation_id='c' ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			beforeUsage, err := queryMaps(c.DB, `SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model,effort,attribution`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.BackfillReasoning(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			for _, before := range beforeSessions {
				after, err := queryMaps(c.DB, `SELECT * FROM agent_sessions WHERE id=?`, before["id"])
				if err != nil || len(after) != 1 || jsonText(before) != jsonText(after[0]) {
					t.Fatalf("backfill changed reconciled session: before=%v after=%v err=%v", before, after, err)
				}
			}
			afterUsage, err := queryMaps(c.DB, `SELECT * FROM agent_session_usage ORDER BY agent_session_id,usage_hour,model,effort,attribution`)
			if err != nil || jsonText(beforeUsage) != jsonText(afterUsage) {
				t.Fatalf("backfill changed reconciled usage: before=%v after=%v err=%v", beforeUsage, afterUsage, err)
			}
			var missing int
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM messages m LEFT JOIN agent_session_messages am ON am.message_id=m.id WHERE m.conversation_id='c' AND (am.message_id IS NULL OR am.source_order<>m.source_order)`).Scan(&missing); err != nil || missing != 0 {
				t.Fatalf("backfill left missing/stale session membership: %d %v", missing, err)
			}
			owner, wantDepth := "main", 2
			if name == "embedded child reply" || name == "new delegation" {
				owner, wantDepth = "child", 3
			}
			var nativeID string
			var depth int
			kind := "reasoning"
			if name == "new delegation" {
				kind = "message"
			} else if name == "redacted first block" || name == "empty first block" {
				kind = "metadata"
				var unavailable, count int
				if err := c.DB.QueryRow(`SELECT unavailable_count,(SELECT COUNT(*) FROM messages WHERE conversation_id='c') FROM reasoning_extraction_state WHERE conversation_id='c'`).Scan(&unavailable, &count); err != nil || unavailable != 1 || count != 2 {
					t.Fatalf("unavailable reasoning counted more than once: unavailable=%d messages=%d %v", unavailable, count, err)
				}
			}
			if err := c.DB.QueryRow(`SELECT a.native_id,a.depth FROM messages m JOIN agent_session_messages am ON am.message_id=m.id JOIN agent_sessions a ON a.id=am.agent_session_id WHERE m.conversation_id='c' AND m.kind=?`, kind).Scan(&nativeID, &depth); err != nil || nativeID != owner || depth != wantDepth {
				t.Fatalf("wrong recovered ownership: %s depth=%d %v", nativeID, depth, err)
			}
			if done, err := c.BackfillReasoning(t.Context(), nil); err != nil || done != 0 {
				t.Fatalf("backfill replay changed completed work: %d %v", done, err)
			}
		})
	}
	c := textFixture(t)
	thought := "I'm puzzled about missing mobile output. Actually I think I understand now: the desktop assertion stopped the run."
	body := jsonText(map[string]any{"type": "assistant", "message": map[string]any{"id": "request", "role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": thought},
		map[string]any{"type": "text", "text": "An ordinary reply"},
		map[string]any{"type": "redacted_thinking", "data": "unavailable secret"},
	}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4}}})
	tx := seedTx(t, c.DB)
	oldID, _, err := upsertMessage(tx, "c", MessageRecord{NativeID: "old-thinking", Role: "assistant", Kind: "metadata", Text: body,
		CreatedAt: "2026-10-18T17:10:35Z", SourceOrder: 10, EvidenceLocator: "fixture:328", Selected: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceMessageFTS(tx, oldID); err != nil {
		t.Fatal(err)
	}
	for _, item := range []MessageRecord{
		{NativeID: "quoted-user", Role: "user", Kind: "message", Text: body},
		{NativeID: "quoted-tool", Role: "tool", Kind: "tool_result", Text: body},
		{NativeID: "quoted-input", Role: "assistant", Kind: "tool_call", Text: body},
	} {
		id, _, err := upsertMessage(tx, "c", item)
		if err != nil {
			t.Fatal(err)
		}
		if err := replaceMessageFTS(tx, id); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	options := TextSearchOptions{Query: `"I understand now"`, Surface: "reasoning"}
	before, err := c.SearchText(t.Context(), options)
	if err != nil || len(before.Items) != 0 || before.ReasoningCoverage["normalization_complete"] != false {
		t.Fatalf("legacy coverage: %+v %v", before, err)
	}
	cursor, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cache", Limit: 1})
	if err != nil || cursor.NextCursor == "" {
		t.Fatalf("cursor: %+v %v", cursor, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done, err := c.BackfillReasoning(ctx, func(done, total int) { cancel() })
	if done != 1 || err != context.Canceled {
		t.Fatalf("backfill did not stop between conversations: %d %v", done, err)
	}
	if pending, err := c.pendingReasoning(t.Context()); err != nil || pending != 1 {
		t.Fatalf("checkpoint lost: pending=%d %v", pending, err)
	}
	find := LibraryFindOptions{Query: options.Query, Search: TextSearchOptions{Surface: "reasoning"}}
	cached, err := c.libraryFindMatches(t.Context(), find)
	if err != nil || cached.search.ReasoningCoverage["normalization_complete"] != false {
		t.Fatalf("Library pending coverage: %+v %v", cached, err)
	}
	if done, err := c.BackfillReasoning(t.Context(), nil); err != nil || done != 1 {
		t.Fatalf("resume repeated completed work: %d %v", done, err)
	}
	refreshed, err := c.libraryFindMatches(t.Context(), find)
	if err != nil || refreshed.search.ReasoningCoverage["normalization_complete"] != true {
		t.Fatalf("Library kept stale extraction coverage: %+v %v", refreshed, err)
	}
	result, err := c.searchTextMCP(t.Context(), map[string]any{"query": options.Query, "surface": "reasoning", "max_output_tokens": 4000})
	if err != nil || len(result.Items) != 1 || result.Items[0].MessageID != oldID || result.Items[0].ReasoningFormat != "text" || !result.Complete || result.ReasoningCoverage["normalization_complete"] != true {
		t.Fatalf("reasoning search: %+v %v", result, err)
	}
	if !strings.Contains(result.Items[0].Snippet, "I understand now") || result.Items[0].EvidenceLocator != "fixture:328:block:0" {
		t.Fatalf("wrong decoded snippet or source: %+v", result.Items[0])
	}
	literal, err := c.SearchText(t.Context(), TextSearchOptions{Query: "I understand now", Mode: "literal", Surface: "reasoning"})
	start := strings.Index(thought, "I understand now")
	if err != nil || len(literal.Items) != 1 || literal.Items[0].StartByte != start || literal.Items[0].EndByte != start+len("I understand now") {
		t.Fatalf("decoded byte offsets: %+v %v", literal, err)
	}
	stored, err := storedMessages(t.Context(), c.reader(), "c")
	if err != nil {
		t.Fatal(err)
	}
	foundRaw := false
	for _, message := range stored {
		if message.NativeID == "old-thinking" {
			foundRaw = message.RawText == body && message.Text == thought
		}
	}
	if !foundRaw || conversationTokenCounts(stored)["total_tokens"] != 14 {
		t.Fatalf("accounting or original envelope changed: %+v", stored)
	}
	for _, query := range []string{`"ordinary reply"`, `"unavailable secret"`, `"thinking"`} {
		r, err := c.SearchText(t.Context(), TextSearchOptions{Query: query, Surface: "reasoning"})
		if err != nil || len(r.Items) != 0 {
			t.Fatalf("non-reasoning matched %s: %+v %v", query, r, err)
		}
	}
	if _, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cache", Cursor: cursor.NextCursor, Limit: 1}); err == nil {
		t.Fatal("backfill preserved a cursor across changed search positions")
	}
	if done, err := c.BackfillReasoning(t.Context(), nil); err != nil || done != 0 {
		t.Fatalf("completed upgrade repeated: %d %v", done, err)
	}
	coverage, err := c.reasoningCoverage(t.Context())
	if err != nil || integer(coverage["retained_records"]) != 2 {
		t.Fatalf("coverage counts: %v %v", coverage, err)
	}
	for _, source := range coverage["sources"].([]map[string]any) {
		if source["source"] == "codex" && integer(source["unavailable_records"]) != 1 {
			t.Fatalf("unavailable coverage: %v", source)
		}
	}
}

func TestTextSearchReasoningIngest(t *testing.T) {
	c, config := testCatalog(t)
	thought := "I'm puzzled.\n\nActually I think I understand now: desktop failed first."
	body := jsonText(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": thought}, map[string]any{"type": "text", "text": "Checking the test."},
	}}})
	record := WorkspaceRecord{SourceKind: "canonical", SourceID: "reasoning", Account: "local", Conversations: []ConversationRecord{{
		NativeID: "reasoning", Provider: "claude", Account: "local", Messages: []MessageRecord{
			{NativeID: "thinking", Role: "assistant", Kind: "metadata", Text: body, EvidenceLocator: "fixture:1", Selected: true},
			{NativeID: "quoted", Role: "user", Kind: "message", Text: body, Selected: true},
			{NativeID: "reply", Role: "assistant", Kind: "message", Text: "I understand now", Selected: true},
			{NativeID: "summary", Role: "assistant", Kind: "reasoning", ReasoningFormat: "summary", Text: "A shorter account of the analysis", Selected: true},
			{NativeID: "encrypted", Role: "assistant", Kind: "metadata", Text: `{"type":"reasoning","encrypted_content":"opaque"}`, Selected: true},
		},
	}}}
	tx := seedTx(t, c.DB)
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	var conversation string
	if err := c.DB.QueryRow("SELECT id FROM conversations").Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	request := httptest.NewRequest(http.MethodPost, "/api/search/text", strings.NewReader(`{"query":"I understand now","mode":"literal","surface":"reasoning","conversation_id":"`+conversation+`"}`))
	request.Header.Set("Authorization", "Bearer "+config.APIToken)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var result textSearchResult
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Items) != 1 || result.Items[0].ReasoningFormat != "text" || result.ReasoningCoverage["normalization_complete"] != true {
		t.Fatalf("HTTP reasoning ingest: %d %s", response.Code, response.Body.String())
	}
	capabilities, err := c.textSearchCapabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	coverage := capabilities["reasoning_extraction"].(map[string]any)
	sources := coverage["sources"].([]map[string]any)
	if len(sources) != 1 || integer(sources[0]["retained_records"]) != 2 || integer(sources[0]["summary_records"]) != 1 || integer(sources[0]["unavailable_records"]) != 1 {
		t.Fatalf("ingest coverage: %v", coverage)
	}
	var quoted string
	if err := c.DB.QueryRow("SELECT text FROM messages WHERE native_id='quoted'").Scan(&quoted); err != nil || quoted != body {
		t.Fatalf("user quotation changed: %q %v", quoted, err)
	}
}
func TestTextSearchQueries(t *testing.T) {
	c := textFixture(t)
	cases := []struct {
		o    TextSearchOptions
		want []string
	}{
		{TextSearchOptions{Query: `(cache OR buffer) AND "race condition" NOT redis`}, []string{"a", "o"}},
		{TextSearchOptions{Query: `cache "race condition"`}, []string{"a", "o"}},
		{TextSearchOptions{Query: `migrat*`}, []string{"b"}},
		{TextSearchOptions{Query: `"migrat"*`}, []string{"b"}},
		{TextSearchOptions{Query: `timeout NEAR/0 retry`}, []string{"a"}},
		{TextSearchOptions{Query: `timeout NEAR/1 retry`}, []string{"a", "r"}},
		{TextSearchOptions{Query: `timeout PRE/1 retry`}, []string{"a", "r"}},
		{TextSearchOptions{Query: `retry PRE/1 timeout`}, []string{}},
		{TextSearchOptions{Query: `"race condition" PRE/10 cafe`}, []string{"a"}},
		{TextSearchOptions{Query: `cache NOT (cafe PRE/10 timeout)`}, []string{"a", "o", "span"}},
		{TextSearchOptions{Query: `cache cafe`, Separators: true}, []string{"a"}},
		{TextSearchOptions{Query: `foo::bar`, Mode: "literal", Surface: "user"}, []string{"a"}},
		{TextSearchOptions{Query: `foo[:]+bar`, Mode: "regex", Surface: "user"}, []string{"a"}},
		{TextSearchOptions{Query: `cache NOT CACHE`, CaseSensitive: true}, []string{"a", "span"}},
		{TextSearchOptions{Query: `cafe`}, []string{"a"}},
		{TextSearchOptions{Query: `"foo::bar"`, Separators: true}, []string{"a"}},
		{TextSearchOptions{Query: `secret`, Surface: "typed"}, []string{}},
		{TextSearchOptions{Query: `cache NOT redis`, Surface: "typed"}, []string{"span"}},
		{TextSearchOptions{Query: `secret`, Surface: "pasted"}, []string{"span"}},
		{TextSearchOptions{Query: `timeout`, Surface: "reasoning"}, []string{"r"}},
		// Tool output is not word-indexed: the fixture's posting for t is one a
		// catalog still removing them has, and an unscoped search leaves it out.
		// A scoped one scans stored text, tool output included.
		{TextSearchOptions{Query: `secret`, Surface: "tools"}, []string{}},
		{TextSearchOptions{Query: `secret`, Surface: "tools", ConversationID: "c"}, []string{"t"}},
		{TextSearchOptions{Query: `secret`, Surface: "all", WorkspaceIDs: []string{"w", "other"}}, []string{"span", "t"}},
		{TextSearchOptions{Query: `timeout NOT retry`, Surface: "all", WorkspaceID: "w"}, []string{"t"}},
		{TextSearchOptions{Query: `"secret only"`, Surface: "all", ConversationID: "c", Kinds: []string{"tool_result"}}, []string{"t"}},
		{TextSearchOptions{Query: `secret`, Surface: "tools", ConversationID: "c", Kinds: []string{"tool_call"}}, []string{}},
		{TextSearchOptions{Query: `race`, From: "2026-10-12", Provider: "codex"}, []string{"b"}},
		{TextSearchOptions{Query: `cache OR buffer`, Mode: "terms"}, []string{}},
		{TextSearchOptions{Query: `(?s)timeout.*retry`, Mode: "regex", Surface: "reasoning"}, []string{"r"}},
		{TextSearchOptions{Query: `secret`, WorkspaceIDs: []string{}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%s/%s", tc.o.Query, tc.o.Mode, tc.o.Surface), func(t *testing.T) {
			r, e := c.SearchText(t.Context(), tc.o)
			if e != nil {
				t.Fatal(e)
			}
			if !slices.Equal(resultIDs(r), tc.want) || !r.Complete {
				t.Fatalf("got %v complete=%v stop=%s; want %v", resultIDs(r), r.Complete, r.StopReason, tc.want)
			}
			// The workspace table establishes the same full population regardless
			// of message pagination/work budgets, retaining one hit and an exact
			// message count for each matching workspace.
			options := tc.o
			options.workspaceRows = true
			options.Limit, options.MaxCandidates, options.MaxBytes = 1, 1, 64
			population, err := c.SearchText(t.Context(), options)
			want := map[string]int{}
			for _, id := range tc.want {
				workspace := "w"
				if id == "o" {
					workspace = "other"
				}
				want[workspace]++
			}
			if err != nil || !population.Complete || population.Count != len(tc.want) || len(population.Items) != len(want) {
				t.Fatalf("workspace population: %+v err=%v; want %v", population, err, want)
			}
			for _, item := range population.Items {
				if item.Count != want[item.WorkspaceID] {
					t.Fatalf("workspace %s: %d matching messages; want %d", item.WorkspaceID, item.Count, want[item.WorkspaceID])
				}
			}
		})
	}
	// Each says what it covered.
	for _, test := range []struct {
		o        TextSearchOptions
		strategy string
		says     string
	}{
		{TextSearchOptions{Query: "secret", Surface: "tools"}, "word_index", toolOutputNotIndexed},
		{TextSearchOptions{Query: "secret", Surface: "all", Kinds: []string{"tool_call", "tool_result"}}, "word_index", toolOutputNotIndexed},
		{TextSearchOptions{Query: "secret", Surface: "tools", ConversationID: "c"}, "scoped_scan", "scanned the stored text"},
		{TextSearchOptions{Query: "secret", Surface: "all", Kinds: []string{"message"}}, "word_index", ""},
		{TextSearchOptions{Query: "secret"}, "word_index", ""},
	} {
		result, err := c.SearchText(t.Context(), test.o)
		if err != nil || result.Strategy != test.strategy || (test.says != "") != strings.Contains(result.Coverage, "not word-indexed") || !strings.Contains(result.Coverage, test.says) {
			t.Fatalf("%+v: strategy %s, coverage %q, %v", test.o, result.Strategy, result.Coverage, err)
		}
	}
	// Asking the word index for tool output alone is an error that says how
	// to search it, not an empty result.
	if _, err := c.SearchText(t.Context(), TextSearchOptions{Query: "secret", Surface: "tools", Kinds: []string{"tool_result"}}); err == nil || !strings.Contains(err.Error(), "conversation_id") {
		t.Fatalf("unscoped tool-output search: %v", err)
	}
}

func TestTextSearchEditorValidationDoesNotSearch(t *testing.T) {
	config := defaultConfig(t.TempDir() + "/config.toml")
	config.APIToken = "test-token"
	// Validation needs no catalog: a malformed draft never starts a text scan.
	server := testServer(t, config, nil)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"query":"(cache OR buffer) NOT redis"}`, 200},
		{`{"where":{"op":"term","text":"OR"}}`, 200},
		{`{"query":"cache OR"}`, 400},
		{`{"query":"(?<=foo)bar","mode":"regex"}`, 400},
		{`{"where":{"op":"and","children":[]}}`, 400},
		{`{"query":"cache","from":"2026-10-12","to":"2026-10-01"}`, 400},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/search/text/validate", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, response.Code, response.Body.String())
		}
		if tc.status == 200 && !strings.Contains(response.Body.String(), `"where"`) {
			t.Fatal(response.Body.String())
		}
	}
}

func TestTextSearchVisualLibraryRequests(t *testing.T) {
	catalog, config := libraryFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	where := `{"op":"and","children":[{"op":"or","children":[{"op":"term","text":"parser"},{"op":"term","text":"tokenizer"}]},{"op":"phrase","text":"fix the"}]}`
	for _, endpoint := range []string{"/api/query/library", "/api/query/library/rows-v2"} {
		body := `{"select":["title"],"limit":10}`
		if strings.Contains(endpoint, "v2") {
			body = `{"version":2,"profile":"qt-sqlite-v1",` + body[1:]
		}
		req := httptest.NewRequest(http.MethodPost, endpoint+"?find_fuzzy=0&find_where="+url.QueryEscape(where), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || result["total"] != float64(1) || result["find"] == nil {
			t.Fatalf("visual query ignored: %d %s", response.Code, response.Body.String())
		}
	}
	for _, bad := range []string{"null", "{", strings.Repeat("x", 32001)} {
		_, _, err := libraryFindRequest(url.Values{"find_where": []string{bad}})
		if err == nil {
			t.Fatal("malformed visual query silently ignored")
		}
	}
	options, err := catalog.textSearchEditorOptions(t.Context())
	if err != nil || !slices.Equal(options["providers"].([]string), []string{"claude", "codex", "tl1"}) {
		t.Fatal(options, err)
	}
}
func TestTextSearchCursorAndBudgets(t *testing.T) {
	c := textFixture(t)
	o := TextSearchOptions{Query: "cache OR timeout", Limit: 1}
	seen := map[string]bool{}
	var last textSearchResult
	for i := 0; i < 20; i++ {
		r, e := c.SearchText(t.Context(), o)
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range r.Items {
			if seen[item.MessageID] {
				t.Fatal("duplicate result")
			}
			seen[item.MessageID] = true
		}
		last = r
		if r.NextCursor == "" {
			break
		}
		o.Cursor = r.NextCursor
	}
	if len(seen) != 4 || !last.Complete || last.Count != 4 {
		t.Fatalf("pagination: %#v %#v", seen, last)
	}
	r, e := c.SearchText(t.Context(), TextSearchOptions{Query: "n.ma.ch", Mode: "regex", Surface: "user", MaxCandidates: 1})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete || r.NextCursor == "" || r.StopReason != "candidate_budget" {
		t.Fatalf("empty scan must resume: %#v", r)
	}
	if _, e := c.SearchText(t.Context(), TextSearchOptions{Query: "different", Mode: "regex", Surface: "user", Cursor: r.NextCursor}); e == nil {
		t.Fatal("changed query accepted cursor")
	}
	if _, e := c.DB.Exec(`UPDATE messages SET text=text||' edit' WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	if _, e := c.SearchText(t.Context(), TextSearchOptions{Query: "n.ma.ch", Mode: "regex", Surface: "user", Cursor: r.NextCursor}); e == nil {
		t.Fatal("stale cursor accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := c.SearchText(ctx, TextSearchOptions{Query: "cache"}); e == nil {
		t.Fatal("cancellation ignored")
	}
	if _, e := c.SearchText(t.Context(), TextSearchOptions{Query: ".*", Mode: "regex", Surface: "all"}); e == nil {
		t.Fatal("unscoped all-text regex accepted")
	}
	if _, e := c.DB.Exec(`UPDATE messages SET text=? WHERE id='a'`, strings.Repeat("x", (1<<20)+1)); e != nil {
		t.Fatal(e)
	}
	r, e = c.SearchText(t.Context(), TextSearchOptions{Query: "x", Mode: "regex", Surface: "user"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete || r.SkippedOversize != 1 || r.CountRelation != "lower_bound" {
		t.Fatalf("oversize: %#v", r)
	}
}
func TestTextSearchSyntaxErrors(t *testing.T) {
	for _, q := range []string{`"unclosed`, `x OR`, `NOT x`, `(x OR y`, `x NEAR/101 y`, `x NEAR/2 (a OR b)`, `col?r`, `*suffix`, `x AND OR y`} {
		if _, e := parseTextQuery(q, "advanced"); e == nil {
			t.Errorf("accepted %q", q)
		}
	}
}

func TestTextSearchFlatBooleanQueries(t *testing.T) {
	for _, separator := range []string{" ", " AND ", " OR "} {
		query := strings.TrimSuffix(strings.Repeat("cache"+separator, 40), separator)
		node, err := parseTextQuery(query, "advanced")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		if err := validateTextTree(node, 0, &count); err != nil {
			t.Fatalf("flat expression consumed nesting budget: %v", err)
		}
		if count != 41 {
			t.Fatalf("flat expression has %d nodes, want 41", count)
		}
	}
	if _, err := parseTextQuery(strings.Repeat("(", 17)+"cache"+strings.Repeat(")", 17), "advanced"); err == nil {
		t.Fatal("accepted excessive group nesting")
	}
}

func TestTextSearchSurfaceIndexMaintenance(t *testing.T) {
	c := textFixture(t)
	if _, e := c.DB.Exec(`UPDATE messages SET role='assistant',kind='analysis',text='changed' WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	var surface string
	var bytes int
	if e := c.DB.QueryRow(`SELECT surface,byte_length FROM text_search_documents WHERE message_id='a'`).Scan(&surface, &bytes); e != nil {
		t.Fatal(e)
	}
	if surface != "reasoning" || bytes != 7 {
		t.Fatal(surface, bytes)
	}
	if _, e := c.DB.Exec(`DELETE FROM messages WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := c.DB.QueryRow(`SELECT count(*) FROM text_search_documents WHERE message_id='a'`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	// SQLite REPLACE need not fire delete triggers and can assign a new rowid.
	// The insert trigger must replace old metadata even when the new surface
	// no longer belongs in the small-surface index.
	for _, kind := range []string{"message", "tool_result"} {
		if _, e := c.DB.Exec(`INSERT OR REPLACE INTO messages(id,conversation_id,native_id,role,kind,text,content_hash)
		 VALUES('b','c','b','assistant',?,'replacement','replacement')`, kind); e != nil {
			t.Fatal(e)
		}
		var count int
		if e := c.DB.QueryRow(`SELECT count(*) FROM text_search_documents d JOIN messages m ON m.rowid=d.message_rowid
		 WHERE d.message_id='b' AND m.id=d.message_id AND d.byte_length=11`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		want := 0
		if kind == "message" {
			want = 1
		}
		if count != want {
			t.Fatalf("REPLACE kind=%s has %d consistent metadata rows, want %d", kind, count, want)
		}
		if e := c.DB.QueryRow(`SELECT count(*) FROM text_search_documents WHERE message_id='b'`).Scan(&count); e != nil || count != want {
			t.Fatalf("REPLACE left stale metadata: count=%d err=%v", count, e)
		}
	}
}

func TestTextSearchRangeKinds(t *testing.T) {
	c := textFixture(t)
	r, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cafe", Separators: true})
	if err != nil || len(r.Items) != 1 || r.Items[0].RangeKind != "excerpt" {
		t.Fatalf("lexical fallback was labeled an exact match: %+v %v", r, err)
	}
	r, err = c.SearchText(t.Context(), TextSearchOptions{Query: "^", Mode: "regex", Surface: "user"})
	if err != nil || len(r.Items) == 0 {
		t.Fatal(r, err)
	}
	for _, item := range r.Items {
		if item.RangeKind != "match" || item.StartByte != 0 || item.EndByte != 0 {
			t.Fatalf("zero-width regex range changed: %+v", item)
		}
	}
}

func TestTextSearchMCPOutputContinuation(t *testing.T) {
	c := textFixture(t)
	if _, err := c.DB.Exec(`UPDATE messages SET text=? WHERE id='a'`, strings.Repeat("x", (1<<20)+1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("output-%d", i)
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash)
		 VALUES(?,'c',?,'user','message','small result','hash')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"query": "^", "mode": "regex", "surface": "user", "limit": 100, "max_output_tokens": 1200}
	seen := map[string]bool{}
	var last textSearchResult
	for page := 0; page < 50; page++ {
		result, err := c.searchTextMCP(t.Context(), args)
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 && (result.StopReason != "output_budget" || result.SkippedOversize != 0) {
			t.Fatalf("output cursor did not rewind counts: %+v", result)
		}
		for _, item := range result.Items {
			if seen[item.MessageID] {
				t.Fatalf("MCP continuation repeated %s", item.MessageID)
			}
			seen[item.MessageID] = true
		}
		last = result
		if result.NextCursor == "" {
			break
		}
		args["cursor"] = result.NextCursor
	}
	if len(seen) != 22 || last.Count != 22 || last.SkippedOversize != 1 || last.NextCursor != "" || last.Complete {
		t.Fatalf("MCP output lost results or skip coverage: seen=%d result=%+v", len(seen), last)
	}
}

func TestTextSearchHTTPAndMCPParity(t *testing.T) {
	c := textFixture(t)
	config := defaultConfig(t.TempDir() + "/config.toml")
	config.APIToken = "test-token"
	server := testServer(t, config, c)
	req := httptest.NewRequest(http.MethodPost, "/api/search/text", strings.NewReader(`{"query":"cache NOT redis","selected_only":true}`))
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var httpResult textSearchResult
	if e := json.Unmarshal(response.Body.Bytes(), &httpResult); e != nil {
		t.Fatal(e)
	}
	mcp, e := c.searchTextMCP(t.Context(), map[string]any{"query": "cache NOT redis", "selected_only": true})
	if e != nil {
		t.Fatal(e)
	}
	if !slices.Equal(resultIDs(httpResult), resultIDs(mcp)) {
		t.Fatalf("HTTP/MCP differ: %v %v", resultIDs(httpResult), resultIDs(mcp))
	}
	hits, _, e := c.libraryFindHits(t.Context(), LibraryFindOptions{Query: "cache NOT redis", Search: TextSearchOptions{SelectedOnly: true}})
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]int{}
	for _, h := range hits {
		got[h.ConversationID] = h.Count
	}
	want := map[string]int{}
	for _, i := range mcp.Items {
		want[i.ConversationID]++
	}
	if !same(got, want) {
		t.Fatalf("Library/MCP differ: %#v %#v", got, want)
	}
}

func TestTextSearchTrigramSupersetAndByteBudget(t *testing.T) {
	c := textFixture(t)
	for i, body := range []string{"ſearch quality", "Kelvin quality", "search\nquality", "abx", "abz", "search unrelated", "quality unrelated", "nomatch foo", "foo::bar", "SEARCH QUALITY", "needle quality", "λ search quality"} {
		id := fmt.Sprintf("unicode%d", i)
		if _, e := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c',?,'user','message',?,'hash')`, id, id, body); e != nil {
			t.Fatal(e)
		}
		if _, e := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES(?,?)`, id, body); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := c.DB.Exec(`INSERT OR REPLACE INTO message_fts_rows SELECT message_id,rowid FROM messages_fts;DELETE FROM messages_trigram;INSERT INTO messages_trigram(rowid,text) SELECT f.rowid,m.text FROM messages_fts f JOIN messages m ON m.id=f.message_id WHERE m.kind IN ('message','result','delegation','delegation_result')`); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`(?s)search.*quality`, `(?i)kelvin`, `ab(x|z)`, `(?i:search)|kelvin`, `(?s)search.*quality|nomatch`, `search.*(?:quality|inadequate)`, `(?:search|needle).*quality`, `(?:search|ab).*quality`, `(?:search.*)?quality`, `(?:search.*){0,2}quality`, `(?:search.*){1,2}quality`, `(?-i:SEARCH).*QUALITY`, `(?:foo::bar|nomatch)`, `(?:search|quality|)`, `search.*[^x]*quality`} {
		options := TextSearchOptions{Query: q, Mode: "regex", Surface: "user"}
		r, e := c.SearchText(t.Context(), options)
		if e != nil {
			t.Fatal(e)
		}
		pattern, e := regexp.Compile("(?i)" + q)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := queryMapsContext(t.Context(), c.DB, `SELECT id,text FROM messages WHERE role='user' AND kind='message'`)
		if e != nil {
			t.Fatal(e)
		}
		want := []string{}
		for _, row := range rows {
			if pattern.MatchString(firstString(row["text"])) {
				want = append(want, firstString(row["id"]))
			}
		}
		slices.Sort(want)
		if !slices.Equal(resultIDs(r), want) {
			t.Fatalf("unsafe candidate filter %q got %v want %v", q, resultIDs(r), want)
		}
	}
	r, e := c.SearchText(t.Context(), TextSearchOptions{Query: "a", Mode: "literal", Surface: "user", MaxBytes: 4})
	if e != nil {
		t.Fatal(e)
	}
	if r.Complete || r.StopReason != "byte_budget" || r.NextCursor == "" || r.VerifiedBytes > 4 {
		t.Fatalf("byte budget: %#v", r)
	}
}

func TestTextSearchSpanBoundariesAndRegexWork(t *testing.T) {
	c := textFixture(t)
	r, e := c.SearchText(t.Context(), TextSearchOptions{Query: `(?P<word>cache)`, Mode: "regex", Surface: "typed"})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Items) != 1 || strings.Contains(r.Items[0].Snippet, "secret") || len(r.Items[0].Captures) != 1 || r.Items[0].Captures[0].StartByte != 6 || r.Items[0].Captures[0].Name != "word" {
		t.Fatalf("span capture leaked/corrupted: %#v", r.Items)
	}
	if _, e = c.DB.Exec(`UPDATE messages SET text=? WHERE id='a'`, strings.Repeat("a", 1<<16)); e != nil {
		t.Fatal(e)
	}
	r, e = c.SearchText(t.Context(), TextSearchOptions{Query: `(?:a?){300}b`, Mode: "regex", Surface: "user"})
	if e != nil {
		t.Fatal(e)
	}
	if r.SkippedOversize < 1 || r.Complete {
		t.Fatalf("complex regex was not byte bounded: %#v", r)
	}
	if _, e = c.SearchText(t.Context(), TextSearchOptions{Query: `(?:a?){1000}`, Mode: "regex", Surface: "user"}); e == nil {
		t.Fatal("unbounded regex program accepted")
	}
}

func TestTextSearchExcerptFindsMatchingAlternative(t *testing.T) {
	c := textFixture(t)
	body := strings.Repeat("preface ", 40) + "redis useful context"
	if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES('long','c','long','user','message',?,'hash')`, body); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES('long',?)`, body); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"zzz OR redis", "(zzz AND buffer) OR redis", "(buffer NOT migration) OR redis", "(zzz OR redis) NOT missing"} {
		t.Run(query, func(t *testing.T) {
			result, err := c.SearchText(t.Context(), TextSearchOptions{Query: query})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(resultIDs(result), []string{"b", "long", "span"}) {
				t.Fatalf("unexpected hits: %v", resultIDs(result))
			}
			for _, item := range result.Items {
				var text string
				if err := c.DB.QueryRow(`SELECT text FROM messages WHERE id=?`, item.MessageID).Scan(&text); err != nil {
					t.Fatal(err)
				}
				start := strings.Index(text, "redis")
				if item.StartByte != start || item.EndByte != start+len("redis") || !strings.Contains(item.Snippet, "redis") {
					t.Errorf("%s: expected redis at %d:%d, got %+v", item.MessageID, start, start+len("redis"), item)
				}
			}
		})
	}
	// Normalized FTS tokens may have no strict spelling location. Fall back to
	// another positive clause, never to words that only occur in the exclusion.
	for _, tc := range []struct{ body, query, match string }{
		{"café then redis", "cafe AND redis", "redis"},
		{"prefix redis suffix", "missing NOT redis", ""},
	} {
		node, err := parseTextQuery(tc.query, "advanced")
		if err != nil {
			t.Fatal(err)
		}
		lo, hi := lexicalExcerpt(tc.body, node)
		if tc.match == "" {
			if lo != 0 || hi != len(tc.body) {
				t.Fatalf("exclusion became the excerpt: %d:%d", lo, hi)
			}
		} else if tc.body[lo:hi] != tc.match {
			t.Fatalf("wrong positive excerpt: %q", tc.body[lo:hi])
		}
	}
}

func TestTextSearchFlatExclusions(t *testing.T) {
	c := textFixture(t)
	for _, separator := range []string{" NOT ", " AND NOT "} {
		for _, mixed := range []bool{false, true} {
			query := "cache"
			for i := 0; i < 18; i++ {
				query += separator + fmt.Sprintf("absent%d", i)
				if mixed {
					query += " AND cache"
				}
			}
			result, err := c.SearchText(t.Context(), TextSearchOptions{Query: query})
			if err != nil {
				t.Fatalf("flat exclusions (mixed=%t): %v", mixed, err)
			}
			if !slices.Equal(resultIDs(result), []string{"a", "o", "span"}) {
				t.Fatalf("exclusions changed hits: %v", resultIDs(result))
			}
			result, err = c.SearchText(t.Context(), TextSearchOptions{Query: query + " NOT cache"})
			if err != nil || len(result.Items) != 0 {
				t.Fatalf("last exclusion ignored: %+v %v", result, err)
			}
		}
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"cache NOT (redis NOT secret)", []string{"a", "o", "span"}},
		{"cache NOT redis NOT secret", []string{"a", "o"}},
		{"(cache NOT redis) NOT secret", []string{"a", "o"}},
		{"cache NOT redis AND timeout OR buffer", []string{"a", "b"}},
	} {
		result, err := c.SearchText(t.Context(), TextSearchOptions{Query: tc.query})
		if err != nil || !slices.Equal(resultIDs(result), tc.want) {
			t.Fatalf("%s: %v %v", tc.query, resultIDs(result), err)
		}
	}
	// TestTextSearchFlatBooleanQueries rejects excessive nesting in the parser.
	if _, err := c.SearchText(t.Context(), TextSearchOptions{Query: "cache" + strings.Repeat(" NOT absent", 128)}); err == nil {
		t.Fatal("query limit bypassed: 128 exclusions")
	}
}

func TestTextSearchFuzzyCaseSensitiveExpansionBoundaries(t *testing.T) {
	c := textFixture(t)
	for _, sample := range []struct{ id, body string }{{"plural", "migrations"}, {"upper", "Migrations"}} {
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c',?,'user','message',?,'hash')`, sample.id, sample.id, sample.body); err != nil {
			t.Fatal(err)
		}
		if _, err := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES(?,?)`, sample.id, sample.body); err != nil {
			t.Fatal(err)
		}
	}
	for _, sensitive := range []bool{false, true} {
		result, err := c.SearchText(t.Context(), TextSearchOptions{Query: "migration", Fuzzy: true, CaseSensitive: sensitive})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"b", "plural", "upper"}
		if sensitive {
			want = want[:2]
		}
		if !slices.Equal(resultIDs(result), want) {
			t.Errorf("case_sensitive=%t: got %v, want %v", sensitive, resultIDs(result), want)
		}
		for _, item := range result.Items {
			if item.MessageID == "plural" && (item.StartByte != 0 || item.EndByte != len("migrations")) {
				t.Errorf("case_sensitive=%t: plural excerpt %d:%d", sensitive, item.StartByte, item.EndByte)
			}
		}
	}
	node := &textQuery{Op: "term", Text: "migration", expansions: []string{"migration", "migrations"}}
	body := "premigrations migrationsx migrations migration"
	for _, match := range node.strictLocations(body, true, false, 0) {
		if word := body[match[0]:match[1]]; word != "migration" && word != "migrations" {
			t.Fatalf("non-token expansion matched: %q", word)
		}
	}
	if matches := node.strictLocations(body, true, false, 0); len(matches) != 2 {
		t.Fatalf("expected exactly two whole tokens: %v", matches)
	}
	if start, end := lexicalExcerpt("prefix migrations suffix", node); start != 7 || end != 17 {
		t.Fatalf("plural excerpt missed the expansion: %d:%d", start, end)
	}
}

func TestTextSearchByteBudgetAlwaysAdvances(t *testing.T) {
	c := textFixture(t)
	for _, body := range []string{"cache", "cache larger than the page budget"} {
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES(?,'c',?,'user','message',?,'hash')`, body, body, body); err != nil {
			t.Fatal(err)
		}
		if _, err := c.DB.Exec(`INSERT INTO messages_fts(message_id,text) VALUES(?,?)`, body, body); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"terms", "literal", "regex"} {
		t.Run(mode, func(t *testing.T) {
			options := TextSearchOptions{Query: "cache", Mode: mode, MaxBytes: 10, MaxCandidates: 1}
			var last textSearchResult
			var before int64 = 1<<63 - 1
			seen := []string{}
			for page := 0; page < 20; page++ {
				result, err := c.SearchText(t.Context(), options)
				if err != nil {
					t.Fatal(err)
				}
				if result.VerifiedBytes > options.MaxBytes {
					t.Fatalf("byte limit exceeded: %+v", result)
				}
				seen = append(seen, resultIDs(result)...)
				last = result
				if result.NextCursor == "" {
					break
				}
				var cursor textSearchCursor
				data, err := base64Cursor(result.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &cursor); err != nil {
					t.Fatal(err)
				}
				if cursor.Before >= before {
					t.Fatalf("page %d did not advance past candidate %d: %+v", page, before, result)
				}
				before, options.Cursor = cursor.Before, result.NextCursor
			}
			if last.NextCursor != "" || last.Complete || last.CountRelation != "lower_bound" || last.SkippedOversize == 0 || !slices.Equal(seen, []string{"cache"}) {
				t.Fatalf("scan must finish, count oversized records, and return smaller matches: seen=%v result=%+v", seen, last)
			}
		})
	}
}

func TestLibraryFindMetadataIgnoresScopedCursor(t *testing.T) {
	catalog, config := libraryFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refreshAllLibrary(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A one-hit page gives the same scoped cursor as the Library's larger batch.
	page, err := catalog.SearchText(t.Context(), TextSearchOptions{Query: "parser", WorkspaceIDs: []string{"c"}, SelectedOnly: true, Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("scoped cursor: %+v %v", page, err)
	}
	server := testServer(t, config, catalog)
	params := url.Values{"find": {"parser"}, "find_fuzzy": {"0"}, "find_cursor": {page.NextCursor}}
	request := func(t *testing.T, method, path, body string) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	// Workspace rows and metrics cannot resume a message batch: a cursor would
	// silently exclude earlier workspaces before global filtering/sorting.
	where := `[{"field":"title","op":"=","value":"Charlie"}]`
	for endpoint, body := range map[string]string{
		"rows-v2": `{"version":2,"profile":"qt-sqlite-v1","select":["title"],"where":` + where + `,"limit":10}`,
		"metrics": `{"version":2,"profile":"qt-sqlite-v1","where":` + where + `,"metrics":[{"id":"n","op":"count","groupBy":[]}]}`,
	} {
		response := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/library/"+endpoint+"?"+params.Encode(), body)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "without a message cursor") {
			t.Fatalf("workspace search accepted a message cursor: %d %s", response.Code, response.Body.String())
		}
	}
	for _, endpoint := range []string{"distinct?field=title", "field-stats?fields=title"} {
		t.Run(endpoint, func(t *testing.T) {
			path := "/api/query/library/" + endpoint + "&" + params.Encode()
			got := request(t, http.MethodGet, path, "")
			withoutCursor := strings.Replace(path, "&find_cursor="+url.QueryEscape(page.NextCursor), "", 1)
			want := request(t, http.MethodGet, withoutCursor, "")
			if got != want || strings.Contains(got, "Bravo") {
				t.Fatalf("metadata changed with cursor: got %s, want %s", got, want)
			}
		})
	}
}

func TestTextSearchMetadataPlanningPreservesMatches(t *testing.T) {
	c := textFixture(t)
	execTx(t, c.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('offset','c','offset','user','message','cache','offset','2026-10-12T00:30:00+01:00'),
 ('space','c','space','assistant','message','cache','space','2026-10-11 23:45:00'),
 ('system-reasoning','c','system-reasoning','system','reasoning','cache','system','2026-10-12T00:00:00Z')`,
		`INSERT INTO messages_fts(message_id,text) SELECT id,text FROM messages WHERE id IN ('offset','space','system-reasoning')`,
		`INSERT OR REPLACE INTO message_fts_rows SELECT message_id,rowid FROM messages_fts`)
	cases := []struct {
		o    TextSearchOptions
		want []string
	}{
		{TextSearchOptions{Query: "cache", From: "2100-01-01"}, []string{}},
		{TextSearchOptions{Query: "cache", Roles: []string{"missing"}}, []string{}},
		{TextSearchOptions{Query: "cache", Roles: []string{"system"}}, []string{"system-reasoning"}},
		{TextSearchOptions{Query: "cache", Kinds: []string{"reasoning"}}, []string{"system-reasoning"}},
		{TextSearchOptions{Query: "cache", From: "2026-10-11T23:00:00Z", To: "2026-10-11"}, []string{"offset", "space"}},
		{TextSearchOptions{Query: "cache", Surface: "reasoning", To: "2026-10-12"}, []string{"system-reasoning"}},
		{TextSearchOptions{Query: "cache", Surface: "user", Roles: []string{"assistant"}}, []string{}},
	}
	for _, maxRows := range []int{256, 1} {
		override(t, &textSearchPlanMaxRows, maxRows)
		for _, tc := range cases {
			result, err := c.SearchText(t.Context(), tc.o)
			if err != nil || !result.Complete || !slices.Equal(resultIDs(result), tc.want) {
				t.Fatalf("limit=%d options=%+v ids=%v complete=%v err=%v", maxRows, tc.o, resultIDs(result), result.Complete, err)
			}
		}
	}
	// Legacy/partial mappings must still find their unmapped eligible messages.
	execTx(t, c.DB, `DELETE FROM message_fts_rows WHERE message_id IN ('offset','space')`)
	result, err := c.SearchText(t.Context(), cases[4].o)
	if err != nil || !slices.Equal(resultIDs(result), cases[4].want) {
		t.Fatalf("partial mapping: %v %v", resultIDs(result), err)
	}
}

func TestTextSearchASCIIPrefilterIsConservative(t *testing.T) {
	verifier, err := newTextVerifier(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.db.Close()
	for _, query := range []string{"cache", `"foo bar"`, "foo*", "cache NOT redis", "(missing AND cache) OR retry", "timeout NEAR/8 retry", "retry PRE/8 timeout", "café", "cache café"} {
		node, err := parseTextQuery(query, "advanced")
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{"cache", "cache redis", "foo_bar", "foobar", "prefixcache cachex cache", "timeout retry", "retry then timeout", "cafe", "café", "cache cafe", "unrelated", "foo\x00bar"} {
			_, _, match, err := verifier.match(t.Context(), node, body, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if match && !node.asciiCandidate(body) {
				t.Fatalf("prefilter discarded %q in %q", query, body)
			}
		}
	}
}

func TestSearchSnippetBoundsUnicode(t *testing.T) {
	body := strings.Repeat("é", 200) + "needle" + strings.Repeat("🦉", 200)
	got := searchSnippet(body, 400, 406)
	want := "…" + strings.Repeat("é", 90) + "needle" + strings.Repeat("🦉", 130) + "…"
	if got != want {
		t.Fatalf("snippet changed rune boundaries: %q", got)
	}
	// The display truncation may land inside a multibyte character.
	got = searchSnippet(body, 400, 409)
	if !utf8.ValidString(got) {
		t.Fatal("snippet split a UTF-8 rune")
	}
}
