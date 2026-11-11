package archive

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeAIExport is a Claude.ai export in the shape claude.ai writes: the
// first conversation's second prompt was edited, so the newest branch is the
// active one; the second names its current leaf, an older branch.
const claudeAIExport = `[
{"uuid":"conv-1","name":"Trip","summary":"Planning","created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-01T10:09:00Z","account":{"uuid":"acct"},"chat_messages":[
 {"uuid":"m1","text":"Plan a trip","sender":"human","created_at":"2026-10-01T10:00:00Z","parent_message_uuid":"00000000-0000-4000-8000-000000000000","files":[{"file_uuid":"f1","file_name":"notes.pdf"}],
  "attachments":[{"file_name":"dates.txt","file_size":12,"file_type":"text/plain","extracted_content":"June 3 to 9"}],
  "content":[{"type":"injected_prompt_block","injection_source":"date_note","prompt":"Today is October 1."},{"type":"text","text":"Plan a trip"}]},
 {"uuid":"m2","text":"Here is a plan","sender":"assistant","created_at":"2026-10-01T10:01:00Z","parent_message_uuid":"m1","attachments":[],"files":[],
  "content":[{"type":"thinking","thinking":"","summaries":[{"summary":"Weighing destinations"}]},
   {"type":"tool_use","id":"tu1","name":"WebSearch","input":{"query":"quiet beaches"}},
   {"type":"tool_result","id":"tr1","tool_use_id":"tu1","content":[{"type":"text","text":"Results"}],"is_error":false},
   {"type":"text","text":"Here is a plan"}]},
 {"uuid":"m3","text":"Make it cheaper","sender":"human","created_at":"2026-10-01T10:02:00Z","parent_message_uuid":"m2","attachments":[],"files":[],"content":[{"type":"text","text":"Make it cheaper"}]},
 {"uuid":"m3b","text":"Make it shorter","sender":"human","created_at":"2026-10-01T10:05:00Z","parent_message_uuid":"m2","attachments":[],"files":[],"content":[{"type":"text","text":"Make it shorter"}]},
 {"uuid":"m4","text":"A shorter plan","sender":"assistant","created_at":"2026-10-01T10:06:00Z","parent_message_uuid":"m3b","attachments":[],"files":[],"content":[{"type":"thinking","thinking":"","summaries":[]},{"type":"text","text":"A shorter plan"}]}]},
{"uuid":"conv-2","name":"","created_at":"2026-10-02T10:00:00Z","updated_at":"2026-10-02T10:03:00Z","current_leaf_message_uuid":"n2","account":{"uuid":"acct"},"chat_messages":[
 {"uuid":"n1","text":"Hello","sender":"human","created_at":"2026-10-02T10:00:00Z","parent_message_uuid":"00000000-0000-4000-8000-000000000000","attachments":[],"files":[],"content":[{"type":"text","text":"Hello"}]},
 {"uuid":"n2","text":"Hi","sender":"assistant","created_at":"2026-10-02T10:01:00Z","parent_message_uuid":"n1","attachments":[],"files":[],"content":[{"type":"text","text":"Hi"}]},
 {"uuid":"n2b","text":"Hi again","sender":"assistant","created_at":"2026-10-02T10:02:00Z","parent_message_uuid":"n1","attachments":[],"files":[],"content":[{"type":"text","text":"Hi again"}]}]}
]`

func discoverClaudeAI(t *testing.T, path string) []WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: path, Account: "me"})
	if err != nil {
		t.Fatal(err)
	}
	records := []WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return records
}

func writeClaudeAIZip(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("conversations.json")
	if err == nil {
		_, err = entry.Write([]byte(claudeAIExport))
	}
	if err == nil {
		err = archive.Close()
	}
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAIExportKeepsTheActiveBranchAndWhatEachBlockIs(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), claudeAIExport)
	records := discoverClaudeAI(t, dir)
	if len(records) != 2 {
		t.Fatalf("records = %d", len(records))
	}
	trip := records[0]
	conversation := trip.Conversations[0]
	if trip.SourceKind != "claude-ai" || trip.SourceID != "conv-1" || trip.Title != "Trip" || conversation.Provider != "claude" || conversation.Harness != "claude-ai-export" || conversation.Origin != "user-export" {
		t.Fatalf("record = %+v / %+v", trip, conversation)
	}
	// The person's words, not the injected date note, and the active branch's reply.
	if trip.Purpose != "Plan a trip" || trip.Outcome != "A shorter plan" {
		t.Fatalf("purpose %q outcome %q", trip.Purpose, trip.Outcome)
	}
	byID := map[string]MessageRecord{}
	for _, message := range conversation.Messages {
		byID[message.NativeID] = message
	}
	if byID["conv-1:export"].CreatedAt != "" {
		t.Fatal("an untimed export summary must not extend the session's clock span")
	}
	want := map[string]struct{ role, kind string }{
		"conv-1:export": {"system", "metadata"}, "m1:block:0": {"system", "context"}, "m1": {"user", "message"}, "m1:attachment:0": {"user", "attachment"}, "m1:files": {"user", "attachment"},
		"m2:block:0": {"assistant", "reasoning"}, "m2:block:1": {"assistant", "tool_call"}, "m2:block:2": {"tool", "tool_result"}, "m2": {"assistant", "message"},
		"m3": {"user", "message"}, "m3b": {"user", "message"}, "m4:block:0": {"assistant", "metadata"}, "m4": {"assistant", "message"},
	}
	if len(byID) != len(want) {
		t.Fatalf("messages = %v", byID)
	}
	for id, expected := range want {
		if got := byID[id]; got.Role != expected.role || got.Kind != expected.kind || got.Selected != (id != "m3") {
			t.Fatalf("%s = %+v, want %v selected %v", id, got, expected, id != "m3")
		}
	}
	if reasoning := byID["m2:block:0"]; reasoning.Text != "Weighing destinations" || reasoning.ReasoningFormat != "summary" {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if call, result := byID["m2:block:1"], byID["m2:block:2"]; call.CallID != "tu1" || result.CallID != "tu1" || result.Text != `{"content":[{"text":"Results","type":"text"}],"is_error":false,"tool":"WebSearch"}` {
		t.Fatalf("call %+v result %+v", call, result)
	}
	if byID["m3b"].PreviousNativeID != "m2" || byID["m1"].PreviousNativeID != "" {
		t.Fatal("parents are not kept")
	}
	// An explicit current leaf wins over the newest one.
	second := records[1].Conversations[0].Messages
	if len(second) != 4 || !second[2].Selected || second[3].Selected || records[1].Title != "Claude.ai conv-2" {
		t.Fatalf("second = %+v", records[1])
	}

	// An export from before tree links holds every turn, in order; a turn
	// spoken as a voice note is the person's words.
	older := discoverClaudeAI(t, writeSourceFileAt(t, `[{"uuid":"conv-5","name":"Old","chat_messages":[
 {"uuid":"o1","text":"","sender":"human","created_at":"2025-01-02T10:00:00Z","content":[{"type":"voice_note","title":"Voice note","text":"Name a fruit"}]},
 {"uuid":"o2","text":"Apple","sender":"assistant","created_at":"2025-01-02T10:01:00Z","content":[{"type":"text","text":"Apple"}]},
 {"uuid":"o3","text":"Another","sender":"human","created_at":"2025-01-02T10:02:00Z","content":[{"type":"text","text":"Another"}]},
 {"uuid":"o4","text":"Pear","sender":"assistant","created_at":"2025-01-02T10:03:00Z","content":[{"type":"text","text":"Pear"}]}]}]`))
	turns := older[0].Conversations[0].Messages
	if len(turns) != 5 || older[0].Purpose != "Name a fruit" || older[0].Outcome != "Pear" || turns[1].NativeID != "o1" || turns[1].Role != "user" {
		t.Fatalf("older export = %+v", older[0])
	}
	for _, turn := range turns {
		if !turn.Selected {
			t.Fatalf("%s is not selected", turn.NativeID)
		}
	}

	// The ZIP the export arrives in reads the same, apart from where.
	zipped := filepath.Join(t.TempDir(), "conversations-000.zip")
	writeClaudeAIZip(t, zipped)
	located := func(records []WorkspaceRecord, file string) string {
		return strings.ReplaceAll(jsonText(records), file, "EXPORT")
	}
	if got := discoverClaudeAI(t, zipped); located(got, zipped) != located(records, filepath.Join(dir, "conversations.json")) {
		t.Fatal("the ZIP read differently")
	}
}

func TestClaudeAIExportZipIsCapturedAndIndexedElsewhere(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	zipped := filepath.Join(t.TempDir(), "conversations-000.zip")
	writeClaudeAIZip(t, zipped)
	source := SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: zipped, Account: "me", Enabled: true}
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	var conversations, messages int
	if err := other.DB.QueryRow(`SELECT COUNT(DISTINCT c.id), COUNT(m.id) FROM conversations c JOIN messages m ON m.conversation_id=c.id WHERE c.provider='claude'`).Scan(&conversations, &messages); err != nil {
		t.Fatal(err)
	}
	if conversations != 2 || messages != 17 {
		t.Fatalf("captured export: %d conversations, %d messages", conversations, messages)
	}
}

func TestClaudeAIExportReadsOnlyWhatItHolds(t *testing.T) {
	// Usage a person pasted is their words, not a request's tokens.
	c, _ := testCatalog(t)
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), `[{"uuid":"conv-3","name":"Paste","chat_messages":[
 {"uuid":"p1","text":"","sender":"human","created_at":"2026-10-03T10:00:00Z","parent_message_uuid":"00000000-0000-4000-8000-000000000000","content":[{"type":"text","text":"{\"usage\":{\"input_tokens\":1000,\"output_tokens\":200}}"}]},
 {"uuid":"p2","text":"{\"usage\":{\"input_tokens\":5,\"output_tokens\":5}}","sender":"assistant","created_at":"2026-10-03T10:01:00Z","parent_message_uuid":"p1","content":[]}]}]`)
	ingestSource(t, c, SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: dir, Account: "me", Enabled: true})
	var messages, tokens int
	if err := c.DB.QueryRow(`SELECT COUNT(*), (SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage) FROM messages`).Scan(&messages, &tokens); err != nil {
		t.Fatal(err)
	}
	if messages != 3 || tokens != 0 {
		t.Fatalf("%d messages counted %d tokens", messages, tokens)
	}

	// A damaged export fails rather than replacing conversations with less.
	complete := `{"uuid":"conv-4","name":"","chat_messages":[]}`
	for name, export := range map[string]string{
		"truncated":        "[" + complete + ",",
		"cut after one":    "[" + complete,
		"two arrays":       "[" + complete + "] [" + complete + "]",
		"no messages list": `[{"uuid":"conv-4","name":""}]`,
		"null messages":    `[{"uuid":"conv-4","name":"","chat_messages":null}]`,
	} {
		path := filepath.Join(t.TempDir(), "conversations.json")
		writeSourceFile(t, path, export)
		adapter, err := MakeAdapter(SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Discover(func(WorkspaceRecord) error { return nil }); err == nil {
			t.Errorf("%s export read without error", name)
		}
	}
	if records := discoverClaudeAI(t, writeSourceFileAt(t, `[`+complete+`]`)); len(records) != 1 || len(records[0].Conversations[0].Messages) != 1 {
		t.Fatalf("empty conversation = %+v", records)
	}
}

func TestClaudeAIExportRetainsSourceFieldsAndExplicitUsage(t *testing.T) {
	// This is a synthetic compatibility case. The inspected November export
	// has no usage or model fields; never infer them from prose or budgets.
	export := `[{"uuid":"audit","name":"Research","future":{"flag":true},"chat_messages":[
 {"uuid":"ask","sender":"human","text":"Question","updated_at":"2026-11-01T10:00:01Z","future":9007199254740993,"content":[{"type":"text","text":"Question"}]},
 {"uuid":"reply","sender":"assistant","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":2},"content":[
 {"type":"token_budget","remaining":null},
 {"type":"tool_use","id":"call","name":"Search","input":["one","two"],"integration_name":"Library"},
 {"type":"tool_result","tool_use_id":"call","content":[],"structured_content":{"answer":42},"display_content":{"type":"rich_link","link":{"url":"https://example.com"}}},
 {"type":"text","text":"Answer","citations":[{"details":{"url":"https://example.com","type":"web_search_citation"}}],"stop_timestamp":"2026-11-01T10:01:00Z"}]}]}]`
	records := discoverClaudeAI(t, writeSourceFileAt(t, export))
	conversation := records[0].Conversations[0]
	byID := map[string]MessageRecord{}
	for _, m := range conversation.Messages {
		byID[m.NativeID] = m
	}
	if !strings.Contains(byID["audit:export"].RawText, `"future":{"flag":true}`) || !strings.Contains(byID["ask"].RawText, `9007199254740993`) || !strings.Contains(byID["ask"].RawText, `updated_at`) {
		t.Fatal("conversation or message envelope was lost")
	}
	if !strings.Contains(byID["reply:block:1"].Text, `["one","two"]`) || !strings.Contains(byID["reply:block:2"].Text, `structured_content`) || !strings.Contains(byID["reply"].RawText, `citations`) {
		t.Fatal("structured tool data or citations were lost")
	}
	counts := conversationTokenCounts(conversation.Messages)
	if counts["total_tokens"] != 12 || counts["input_tokens"] != 10 || byID["reply:usage"].Model != "claude-sonnet-4-5" {
		t.Fatalf("explicit request usage = %v", counts)
	}
}

func TestClaudeAIExportSupplementaryDocumentsAndCloudTranscripts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "export.zip")
	attachment := filepath.Join(t.TempDir(), "download.zip")
	chatGPTZip(t, attachment, map[string]string{"report.md": "A downloadable report."})
	attachmentBytes, err := os.ReadFile(attachment)
	if err != nil {
		t.Fatal(err)
	}
	chatGPTZip(t, file, map[string]string{
		"conversations.json":                                 `[{"uuid":"chat","chat_messages":[]}]`,
		"projects/p.json":                                    `{"uuid":"p","name":"Research notes","docs":[{"filename":"report.md","content":"An exported report\nwith two lines."}]}`,
		"memories/account.json":                              `{"memory_files":[{"path":"profile.md","content":"Recorded preferences"}]}`,
		"reflections/account.json":                           `{"reflections":[{"content":"Recorded reflection"}],"feedback":[]}`,
		"artifacts/a/artifact.json":                          `{"id":"a","active_version":"v1","versions":[{"id":"v1","title":"A retained document"}]}`,
		"artifacts/a/versions/v1/index.html":                 `<h1>Archived document</h1><script>neverExecute()</script>`,
		"artifacts/a/download.zip":                           string(attachmentBytes),
		"artifacts/b/download.ZIP":                           string(attachmentBytes),
		"artifacts/a/versions/v1/photo.webp":                 "RIFF\x00binary-image",
		"claude_code_cloud/channel_session_transcripts.json": `{"claude_code_cloud":[{"session_id":"cloud","role":"user","content":[{"type":"text","text":"Cloud question"}]},{"session_id":"cloud","role":"assistant","content":[{"type":"tool_use","tool_use_id":"tool","name":"Search","input":"{\"query\":\"history\"}"},{"type":"tool_result","tool_use_id":"tool","content":"A cloud result"},{"type":"text","text":"Cloud answer"}]}]}`,
		"users.json":                                         `[{"email":"account-only@example.com"}]`,
	})
	records := discoverClaudeAI(t, file)
	var files, calls, results int
	for _, r := range records {
		if strings.Contains(jsonText(r), "account-only@example.com") {
			t.Fatal("account profile entered transcripts")
		}
		for _, m := range r.Conversations[0].Messages {
			if m.Kind == "attachment" {
				files++
			}
			if m.Kind == "tool_call" && m.CallID == "tool" {
				calls++
			}
			if m.Kind == "tool_result" && m.CallID == "tool" {
				results++
			}
		}
	}
	if len(records) != 8 || files != 9 || calls != 1 || results != 1 {
		t.Fatalf("records=%d files=%d calls=%d results=%d", len(records), files, calls, results)
	}
	// Unzipping and capturing the same export keeps all files of an artifact
	// together, even though source discovery visits them separately.
	unzipped := t.TempDir()
	archive, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		writeSourceFile(t, filepath.Join(unzipped, filepath.FromSlash(entry.Name)), string(data))
	}
	archive.Close()
	if expanded := discoverClaudeAI(t, unzipped); len(expanded) != len(records) {
		t.Fatalf("unzipped export: %d records, want %d", len(expanded), len(records))
	} else {
		for _, record := range expanded {
			if record.SourceID == "claude-export:artifacts/a" && len(record.Conversations[0].Messages) != 4 {
				t.Fatal("artifact files were split across replacement records")
			}
		}
	}

	// Nested archives are attachments, not additional export containers. Their
	// identity and bytes must match before/after unzipping the outer export.
	for _, input := range []string{file, unzipped} {
		found := map[string]bool{}
		for _, record := range discoverClaudeAI(t, input) {
			for _, message := range record.Conversations[0].Messages {
				var payload map[string]any
				if message.Kind != "attachment" {
					continue
				}
				if err := json.Unmarshal([]byte(message.Text), &payload); err != nil {
					t.Fatal(err)
				}
				name := firstString(payload["file_name"])
				if name != "artifacts/a/download.zip" && name != "artifacts/b/download.ZIP" {
					continue
				}
				if payload["sha256"] != hashBytes(attachmentBytes) || integer(payload["file_size"]) != int64(len(attachmentBytes)) || payload["extracted_content"] != nil {
					t.Fatalf("archive attachment changed: %v", payload)
				}
				found[name] = true
			}
		}
		if len(found) != 2 {
			t.Fatalf("%s retained %d archive attachments, want 2", input, len(found))
		}
	}
	useHost(t, "export-host")
	catalog, config := testCatalog(t)
	source := SourceConfig{Name: "full-export", Kind: "claude-ai", Path: unzipped, Enabled: true}
	config.Sources = []SourceConfig{source}
	ingestSource(t, catalog, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture: %+v", result)
	}
	useHost(t, "reader-host")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "export-host")
	var artifactFiles int
	if err := other.DB.QueryRow(`SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id='claude-export:artifacts/a'`).Scan(&artifactFiles); err != nil || artifactFiles != 4 {
		t.Fatalf("captured artifact files: %d, %v", artifactFiles, err)
	}
	// Every supplementary JSON is validated before publishing any record.
	chatGPTZip(t, file, map[string]string{"conversations.json": claudeAIExport, "projects/bad.json": `{`})
	adapter, _ := MakeAdapter(SourceConfig{Kind: "claude-ai", Path: file})
	emitted := 0
	if err := adapter.Discover(func(WorkspaceRecord) error { emitted++; return nil }); err == nil || emitted != 0 {
		t.Fatalf("damaged supplementary data published %d records: %v", emitted, err)
	}
}

func writeSourceFileAt(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	writeSourceFile(t, path, content)
	return path
}

func TestClaudeAIExportValidatesAllShardsBeforePublishing(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "conversations-000.json")
	second := filepath.Join(dir, "conversations-001.json")
	writeSourceFile(t, first, claudeAIExport)
	writeSourceFile(t, second, `[{"uuid":"bad","chat_messages":[]},`)
	adapter, err := MakeAdapter(SourceConfig{Name: "export", Kind: "claude-ai", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	var emitted int
	if err := adapter.Discover(func(WorkspaceRecord) error { emitted++; return nil }); err == nil || emitted != 0 {
		t.Fatal("damaged late shard published records", emitted, err)
	}
	// Conflicting copies in one snapshot are different from old/new snapshots.
	writeSourceFile(t, second, strings.Replace(claudeAIExport, "Trip", "Changed title", 1))
	if err := adapter.Discover(func(WorkspaceRecord) error { emitted++; return nil }); err == nil || emitted != 0 {
		t.Fatal("conflicting shards published records", emitted, err)
	}
	writeSourceFile(t, second, `[{"uuid":"empty","chat_messages":[]}]`)
	if err := adapter.Discover(func(WorkspaceRecord) error { emitted++; return nil }); err != nil || emitted != 3 {
		t.Fatal(emitted, err)
	}
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "export", Kind: "claude-ai", Path: dir})
	writeSourceFile(t, first, `[{"uuid":"conv-1","name":"Damaged update","chat_messages":[]}]`)
	writeSourceFile(t, second, `[{`)
	result := c.Ingest(adapter, nil)
	if result.Error == nil {
		t.Fatal("damaged export appeared successful")
	}
	var messages int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil || messages != 18 {
		t.Fatal("previous messages changed", messages, err)
	}
}

func TestClaudeAIExportStreamsLargeConversationAndKeepsUnknownBlocks(t *testing.T) {
	content := strings.Repeat("long export content ", 10000)
	export := jsonText([]any{map[string]any{"uuid": "large", "chat_messages": []any{map[string]any{"uuid": "m", "sender": "human", "text": content, "content": []any{map[string]any{"type": "future_block", "payload": "preserve this"}}}}}})
	file := writeSourceFileAt(t, export)
	records := discoverClaudeAI(t, file)
	if len(records) != 1 {
		t.Fatal(records)
	}
	var keptText, keptBlock bool
	for _, message := range records[0].Conversations[0].Messages {
		keptText = keptText || message.Text == content
		keptBlock = keptBlock || strings.Contains(message.RawText, "future_block")
	}
	if !keptText || !keptBlock {
		t.Fatal("lost large text or an unknown block")
	}
}

func TestClaudeAIOlderExportLeavesNewerOneAlone(t *testing.T) {
	older := func(change func(conversation map[string]any)) string {
		var export []map[string]any
		if err := json.Unmarshal([]byte(claudeAIExport), &export); err != nil {
			t.Fatal(err)
		}
		change(export[0])
		return jsonText(export)
	}
	// Before the edited prompt and its reply, whether or not the export
	// records that the conversation changed since; and before anything.
	prefix := older(func(c map[string]any) { c["chat_messages"] = c["chat_messages"].([]any)[:3] })
	dated := older(func(c map[string]any) {
		c["chat_messages"], c["updated_at"] = c["chat_messages"].([]any)[:3], "2026-10-01T10:03:00Z"
	})
	empty := older(func(c map[string]any) { c["chat_messages"] = []any{}; delete(c, "updated_at") })
	// Exports from before content blocks hold each message's text alone.
	flat := older(func(c map[string]any) {
		for _, message := range c["chat_messages"].([]any) {
			delete(message.(map[string]any), "content")
		}
	})
	count := func(c *Catalog) (int, int) {
		var all, selected int
		if err := c.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(m.selected),0) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.native_id='conv-1'`).Scan(&all, &selected); err != nil {
			t.Fatal(err)
		}
		return all, selected
	}
	for name, export := range map[string]string{"prefix": prefix, "dated prefix": dated, "empty": empty, "before content blocks": flat} {
		for when, offset := range map[string]time.Duration{"older file": -48 * time.Hour, "copied since": 48 * time.Hour} {
			t.Run(name+", "+when, func(t *testing.T) {
				c, _ := testCatalog(t)
				newer, old := writeSourceFileAt(t, claudeAIExport), writeSourceFileAt(t, export)
				info, err := os.Stat(newer)
				if err != nil {
					t.Fatal(err)
				}
				at := info.ModTime().Add(offset)
				if err := os.Chtimes(old, at, at); err != nil {
					t.Fatal(err)
				}
				ingestSource(t, c, SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: newer, Account: "me", Enabled: true})
				ingestSource(t, c, SourceConfig{Name: "claude-ai-old", Kind: "claude-ai", Path: old, Account: "me", Enabled: true})
				if all, selected := count(c); all != 13 || selected != 12 {
					t.Fatalf("after the older export: %d messages, %d selected", all, selected)
				}
			})
		}
	}
	// A newer export read after an older one replaces it, branch included,
	// and adds no second copy of a message the older one held as text alone.
	for name, export := range map[string]string{"prefix": prefix, "before content blocks": flat} {
		t.Run("newer after "+name, func(t *testing.T) {
			c, _ := testCatalog(t)
			ingestSource(t, c, SourceConfig{Name: "claude-ai-old", Kind: "claude-ai", Path: writeSourceFileAt(t, export), Account: "me", Enabled: true})
			ingestSource(t, c, SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: writeSourceFileAt(t, claudeAIExport), Account: "me", Enabled: true})
			var unselected int
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE native_id='m3' AND selected=0`).Scan(&unselected); err != nil || unselected != 1 {
				t.Fatalf("the newer branch is not selected: %d %v", unselected, err)
			}
			if all, selected := count(c); all != 13 || selected != 12 {
				t.Fatalf("after the newer export: %d messages, %d selected", all, selected)
			}
		})
	}
}

func TestClaudeAIExportsOfBothShapesFromTwoMacsKeepEachBlockOnce(t *testing.T) {
	var export []map[string]any
	if err := json.Unmarshal([]byte(claudeAIExport), &export); err != nil {
		t.Fatal(err)
	}
	for _, message := range export[0]["chat_messages"].([]any) {
		delete(message.(map[string]any), "content")
	}
	// The text-only export on one Mac, then the full one on another: the
	// full one cannot replace the first Mac's copy, so it only adds.
	useHost(t, "host-a")
	c, _ := testCatalog(t)
	ingestSource(t, c, SourceConfig{Name: "claude-ai-old", Kind: "claude-ai", Path: writeSourceFileAt(t, jsonText(export)), Account: "me", Enabled: true})
	useHost(t, "host-b")
	ingestSource(t, c, SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: writeSourceFileAt(t, claudeAIExport), Account: "me", Enabled: true})
	var replies, reasoning int
	if err := c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages WHERE text='Here is a plan'), (SELECT COUNT(*) FROM messages WHERE native_id='m2:block:0' AND kind='reasoning')`).Scan(&replies, &reasoning); err != nil {
		t.Fatal(err)
	}
	if replies != 1 || reasoning != 1 {
		t.Fatalf("the reply is held %d times and its thinking %d", replies, reasoning)
	}
}

func TestClaudeAINewestLeafComparesTimesNotText(t *testing.T) {
	// "10:00:00Z" sorts after "10:00:00.500Z" as text, though it is earlier.
	selected := claudeAISelected(claudeAIConversation{Messages: []map[string]any{
		{"uuid": "root", "parent_message_uuid": claudeAIRoot, "created_at": "2026-10-01T09:59:00Z"},
		{"uuid": "first", "parent_message_uuid": "root", "created_at": "2026-10-01T10:00:00Z"},
		{"uuid": "second", "parent_message_uuid": "root", "created_at": "2026-10-01T10:00:00.500Z"},
	}})
	if !selected["second"] || selected["first"] || !selected["root"] {
		t.Fatalf("selected = %v", selected)
	}
}

func TestClaudeAIExportReadLiveAfterItsCapture(t *testing.T) {
	c, config := testCatalog(t)
	export := writeSourceFileAt(t, claudeAIExport)
	source := SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: export, Account: "me", Enabled: true}
	config.Sources = []SourceConfig{source}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	indexCaptures(t, c, config.CaptureRoot, currentHost().ID)
	// The same export read live, as a rebuild does, is not taken for an older copy.
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	result := c.IngestContext(withIngestMode(context.Background(), ingestMode{force: true}), adapter, nil)
	if result.Error != nil || result.Older != 0 || result.SkippedCurrent != 0 {
		t.Fatalf("live read after capture = %+v", result)
	}
}

func TestClaudeAIExportToolTimesAndStoredReplay(t *testing.T) {
	const export = `[{"uuid":"timing","name":"Timing","chat_messages":[
 {"uuid":"prompt","sender":"human","created_at":"2026-10-01T10:00:00Z","content":[{"type":"text","text":"Check"}]},
 {"uuid":"reply","sender":"assistant","created_at":"2026-10-01T10:30:00Z","content":[
  {"type":"tool_use","id":"missing","name":"Missing end","start_timestamp":"2026-10-01T10:00:01Z","stop_timestamp":"2026-10-01T10:00:02Z"},
  {"type":"tool_result","tool_use_id":"missing","start_timestamp":null,"stop_timestamp":null,"content":"ok"},
  {"type":"tool_use","id":"known","name":"Known","start_timestamp":"2026-10-01T10:00:04Z"},
  {"type":"tool_result","tool_use_id":"known","start_timestamp":"2026-10-01T10:00:06Z","content":"ok"},
  {"type":"tool_use","id":"nostart","name":"Missing start"},
  {"type":"tool_result","tool_use_id":"nostart","start_timestamp":"2026-10-01T10:00:08Z","content":"ok"},
  {"type":"tool_use","id":"stop","name":"Result stop","start_timestamp":"2026-10-01T10:00:10Z"},
  {"type":"tool_result","tool_use_id":"stop","stop_timestamp":"2026-10-01T10:00:11Z","content":"ok"},
  {"type":"text","text":"Done"}]}]}]`
	path := writeSourceFileAt(t, export)
	catalog, _ := testCatalog(t)
	ingestSource(t, catalog, SourceConfig{Name: "claude-ai", Kind: "claude-ai", Path: path, Account: "me", Enabled: true})
	check := func(stage string) {
		t.Helper()
		rows, err := queryMaps(catalog.DB, `SELECT tool_name,started_at,ended_at,duration_ms,duration_source FROM tool_calls ORDER BY sequence`)
		if err != nil || len(rows) != 4 {
			t.Fatalf("%s: calls %v: %v", stage, rows, err)
		}
		want := []struct {
			name, start, end string
			duration         any
		}{
			{"Missing end", "2026-10-01T10:00:01.000Z", "", nil},
			{"Known", "2026-10-01T10:00:04.000Z", "2026-10-01T10:00:06.000Z", int64(2000)},
			{"Missing start", "", "2026-10-01T10:00:08.000Z", nil},
			{"Result stop", "2026-10-01T10:00:10.000Z", "2026-10-01T10:00:11.000Z", int64(1000)},
		}
		for i, expected := range want {
			row := rows[i]
			if row["tool_name"] != expected.name || firstString(row["started_at"]) != expected.start || firstString(row["ended_at"]) != expected.end || row["duration_ms"] != expected.duration {
				t.Errorf("%s: call %d = %v, want %+v", stage, i, row, expected)
			}
		}
	}
	check("new ingestion")
	// Simulate the old adapter's enclosing-message fallback and old ledger;
	// replay must repair the retained messages as well as their derived calls.
	if _, err := catalog.DB.Exec(`UPDATE messages SET created_at='2026-10-01T10:30:00.000Z' WHERE kind IN ('tool_call','tool_result');
 UPDATE tool_calls SET started_at='2026-10-01T10:30:00.000Z',ended_at='2026-10-01T10:30:00.000Z',duration_ms=0,duration_source='timestamps';
 UPDATE tool_ledger_state SET version='tools-v14-code-mode-patches'`); err != nil {
		t.Fatal(err)
	}
	if built, err := catalog.BackfillToolLedger(t.Context(), nil); err != nil || built != 1 {
		t.Fatalf("replay: %d %v", built, err)
	}
	check("stored replay")
	rows, err := queryMaps(catalog.DB, `SELECT kind,created_at,raw_text FROM messages WHERE call_id='missing' ORDER BY source_order`)
	if err != nil || len(rows) != 2 || firstString(rows[0]["created_at"]) != "2026-10-01T10:00:01.000Z" || rows[1]["created_at"] != nil || !strings.Contains(firstString(rows[1]["raw_text"]), `"type":"tool_result"`) {
		t.Fatalf("retained evidence after replay: %v %v", rows, err)
	}
	if built, err := catalog.BackfillToolLedger(t.Context(), nil); err != nil || built != 0 {
		t.Fatalf("second replay: %d %v", built, err)
	}
	for _, raw := range []string{"", `{"type":"tool_result"`, `{"type":"text"}`} {
		if _, ok := claudeAIRetainedToolTime(MessageRecord{Kind: "tool_result", RawText: raw, CreatedAt: "keep"}); ok {
			t.Fatalf("unrecognized evidence %q must remain untouched", raw)
		}
	}
}
