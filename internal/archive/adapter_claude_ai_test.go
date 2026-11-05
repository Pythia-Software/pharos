package archive

import (
	"archive/zip"
	"context"
	"encoding/json"
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
	want := map[string]struct{ role, kind string }{
		"m1:block:0": {"system", "context"}, "m1": {"user", "message"}, "m1:attachment:0": {"user", "attachment"}, "m1:files": {"user", "attachment"},
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
	if len(second) != 3 || !second[1].Selected || second[2].Selected || records[1].Title != "Claude.ai conv-2" {
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
	if len(turns) != 4 || older[0].Purpose != "Name a fruit" || older[0].Outcome != "Pear" || turns[0].NativeID != "o1" || turns[0].Role != "user" {
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
	if conversations != 2 || messages != 15 {
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
	if messages != 2 || tokens != 0 {
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
	if records := discoverClaudeAI(t, writeSourceFileAt(t, `[`+complete+`]`)); len(records) != 1 || len(records[0].Conversations[0].Messages) != 0 {
		t.Fatalf("empty conversation = %+v", records)
	}
}

func writeSourceFileAt(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	writeSourceFile(t, path, content)
	return path
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
				if all, selected := count(c); all != 12 || selected != 11 {
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
			if all, selected := count(c); all != 12 || selected != 11 {
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
