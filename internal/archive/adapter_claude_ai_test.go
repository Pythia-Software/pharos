package archive

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	// What the person attached points at the prompt it came with: the text the
	// export kept, and a named file whose bytes it did not.
	if assets := conversation.Assets; len(assets) != 2 || assets[0].MessageNativeID != "m1" || assets[0].AttachmentNativeID != "m1:attachment:0" || assets[0].Availability != assetTextOnly || assets[0].Name != "dates.txt" ||
		assets[1].Name != "notes.pdf" || assets[1].Availability != assetNotExported || assets[1].Relation != "uploaded" {
		t.Fatalf("assets = %+v", assets)
	}
	// What the reply made and drew on: a file rebuilt from the calls that wrote
	// it, a widget, a file nothing wrote, and the pages it read and cited.
	made := discoverClaudeAI(t, writeSourceFileAt(t, `[{"uuid":"conv-6","name":"Made","chat_messages":[
 {"uuid":"p1","text":"Write it","sender":"human","created_at":"2026-10-03T10:00:00Z","content":[{"type":"text","text":"Write it"}]},
 {"uuid":"p2","text":"Done","sender":"assistant","created_at":"2026-10-03T10:01:00Z","content":[
  {"type":"tool_use","id":"t1","name":"create_file","input":{"path":"/out/plan.html","file_text":"<p>draft</p>"}},
  {"type":"tool_use","id":"t2","name":"str_replace","input":{"path":"/out/plan.html","old_str":"draft","new_str":"final"}},
  {"type":"tool_use","id":"t3","name":"present_files","input":{"filepaths":["/out/plan.html","/out/lost.csv"]}},
  {"type":"tool_use","id":"t4","name":"visualize:show_widget","input":{"title":"cost_chart","widget_code":"<svg/>"}},
  {"type":"tool_use","id":"t5","name":"web_search","input":{"query":"fares"}},
  {"type":"tool_result","tool_use_id":"t5","content":[{"type":"knowledge","url":"https://fares.example/a","title":"Fares","text":"Cheap","metadata":{"site_name":"Fares"}},{"type":"knowledge","url":"https://other.example/b","title":"Other"}]},
  {"type":"tool_use","id":"t6","name":"image_search","input":{"query":"lake"}},
  {"type":"tool_result","tool_use_id":"t6","content":[{"type":"text","text":"Found 1"},{"type":"image_gallery","images":[{"url":"https://img.example/lake.jpg","thumbnail_url":"https://img.example/t.jpg","page_url":"https://img.example/p","source":"img.example","title":"Lake","width":800,"height":600}]}]},
  {"type":"tool_use","id":"t7","name":"link_preview_display_v0","input":{"links":[{"url":"https://paper.example/x","title":"Paper","domain":"Journal","snippet":"Measured"}]}},
  {"type":"text","text":"Done","citations":[{"details":{"url":"https://fares.example/a"}}]}]}]}]`))[0].Conversations[0]
	found := map[string]AssetRecord{}
	for _, asset := range made.Assets {
		if asset.Relation == "web" {
			// An image a search showed is an address until it is fetched.
			if asset.Availability != assetRemote || asset.AgentAuthored || asset.Details["url"] != "https://img.example/lake.jpg" || asset.Details["page_url"] != "https://img.example/p" || asset.Width != 800 {
				t.Fatalf("web image = %+v", asset)
			}
			continue
		}
		if asset.MessageNativeID != "p2" || !asset.AgentAuthored || asset.Relation != "generated" {
			t.Fatalf("asset = %+v", asset)
		}
		found[asset.Name] = asset
	}
	if len(made.Assets) != 4 {
		t.Fatalf("assets = %+v", made.Assets)
	}
	if plan, lost, widget := found["plan.html"], found["lost.csv"], found["cost chart.html"]; len(found) != 3 || plan.Content != "<p>final</p>" || plan.Availability != assetReconstructed || plan.MimeType != "text/html" ||
		lost.Availability != assetNotExported || widget.Availability != assetStored || widget.Content != "<svg/>" {
		t.Fatalf("assets = %+v", made.Assets)
	}
	if sources := made.Sources; len(sources) != 3 || sources[2].URL != "https://paper.example/x" || !sources[2].Cited || sources[2].Site != "Journal" || sources[0].URL != "https://fares.example/a" || !sources[0].Cited || sources[0].Site != "Fares" || sources[1].Cited || sources[0].MessageNativeID != "p2" {
		t.Fatalf("sources = %+v", sources)
	}
	// A screenshot stays on the tool result that took it, and a file the
	// reply kept rewriting is one card however often the message lists it.
	listed := discoverClaudeAI(t, writeSourceFileAt(t, `[{"uuid":"conv-7","name":"Listed","chat_messages":[
 {"uuid":"q1","text":"Look","sender":"human","created_at":"2026-10-04T10:00:00Z","content":[{"type":"text","text":"Look"}]},
 {"uuid":"q2","text":"Seen","sender":"assistant","created_at":"2026-10-04T10:01:00Z",
  "files":[{"file_uuid":"s1","file_name":"s1.jpg"},{"file_uuid":"d1","file_name":"page.html"},{"file_uuid":"d2","file_name":"page.html"},{"file_uuid":"d3","file_name":"page.html"}],"content":[
  {"type":"tool_use","id":"t1","name":"computer","input":{"action":"screenshot"}},
  {"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","file_uuid":"s1"}]},
  {"type":"text","text":"Seen"}]}]}]`))[0].Conversations[0].Assets
	if len(listed) != 2 || listed[0].Relation != "tool_output" || listed[0].Ref != "s1" || !slices.Equal(listed[0].Also, []string{"q2:files"}) || listed[1].Name != "page.html" || listed[1].Details["listed"] != int64(3) {
		t.Fatalf("listed files = %+v", listed)
	}
	// A file published as an artifact is the artifact: the card holds the
	// file as the calls that wrote it left it, and stands for the file's
	// line in the reply's list of files. Another file those calls wrote is
	// rebuilt the same way.
	published := discoverClaudeAI(t, writeSourceFileAt(t, `[{"uuid":"conv-8","name":"Published","chat_messages":[
 {"uuid":"r1","text":"Publish","sender":"human","created_at":"2026-10-05T10:00:00Z","content":[{"type":"text","text":"Publish"}]},
 {"uuid":"r2","text":"Published","sender":"assistant","created_at":"2026-10-05T10:01:00Z",
  "files":[{"file_uuid":"h1","file_name":"page.html"},{"file_uuid":"h2","file_name":"page.html"},{"file_uuid":"n1","file_name":"notes.md"},{"file_uuid":"n2","file_name":"notes.md"}],"content":[
  {"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"/home/claude/page.html","content":"<p>a a</p>"}},
  {"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"/home/claude/page.html","old_string":"a","new_string":"b","replace_all":true}},
  {"type":"tool_use","id":"t3","name":"Write","input":{"file_path":"/home/claude/notes.md","content":"# Note"}},
  {"type":"tool_use","id":"t5","name":"Edit","input":{"file_path":"/home/claude/notes.md","old_string":"Note","new_string":"Notes"}},
  {"type":"tool_use","id":"t4","name":"Artifact","input":{"action":"publish","description":"A page","file_path":"/home/claude/page.html"}},
  {"type":"text","text":"Published"}]}]}]`))[0].Conversations[0].Assets
	if len(published) != 2 || published[0].Kind != "artifact" || published[0].Content != "<p>b b</p>" || published[0].Availability != assetReconstructed || published[0].MimeType != "text/html" ||
		!slices.Contains(published[0].Also, "r2:files") || published[1].Name != "notes.md" || published[1].Content != "# Notes" || published[1].Availability != assetReconstructed || published[1].MimeType != "text/markdown" ||
		// Listed twice, it is one card that still says how it was written.
		published[1].Details["listed"] != int64(2) || published[1].Details["edits"] != 1 {
		t.Fatalf("published = %+v", published)
	}
	// An artifact first published by its title alone, and named by its
	// address in a later call that names no file, stays one card with what
	// was published and its file's line.
	identified := discoverClaudeAI(t, writeSourceFileAt(t, `[{"uuid":"conv-9","name":"Identified","chat_messages":[
 {"uuid":"s1","text":"Publish","sender":"human","created_at":"2026-10-06T10:00:00Z","content":[{"type":"text","text":"Publish"}]},
 {"uuid":"s2","text":"Published","sender":"assistant","created_at":"2026-10-06T10:01:00Z","files":[{"file_uuid":"k1","file_name":"deck.html"}],"content":[
  {"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"/home/claude/deck.html","content":"<p>deck</p>"}},
  {"type":"tool_use","id":"t2","name":"Artifact","input":{"action":"publish","title":"Deck","file_path":"/home/claude/deck.html"}},
  {"type":"text","text":"Published"}]},
 {"uuid":"s3","text":"Rename","sender":"human","created_at":"2026-10-06T10:02:00Z","content":[{"type":"text","text":"Rename"}]},
 {"uuid":"s4","text":"Renamed","sender":"assistant","created_at":"2026-10-06T10:03:00Z","content":[
  {"type":"tool_use","id":"t3","name":"Artifact","input":{"action":"update","title":"Deck","url":"https://claude.ai/artifact/11111111-2222-4333-8444-555555555555"}},
  {"type":"text","text":"Renamed"}]}]}]`))[0].Conversations[0].Assets
	if len(identified) != 1 || identified[0].Ref != claudeArtifactRef("11111111-2222-4333-8444-555555555555") || identified[0].MessageNativeID != "s4" || identified[0].Content != "<p>deck</p>" ||
		identified[0].Availability != assetReconstructed || identified[0].Details["file"] != "deck.html" || !slices.Contains(identified[0].Also, "s2:files") {
		t.Fatalf("identified = %+v", identified)
	}

	// What the export says of a reply beside its words: where each citation
	// stands, how much was read aloud, thinking that was cut off; the images
	// a search found and did not show, and a search whose images it no
	// longer holds; a file handed over, by where it was and its line in the
	// message's list of files.
	noted := discoverClaudeAI(t, writeSourceFileAt(t, `[{"uuid":"conv-10","name":"Noted","chat_messages":[
 {"uuid":"n1","text":"Find","sender":"human","created_at":"2026-10-07T10:00:00Z","content":[{"type":"text","text":"Find"}]},
 {"uuid":"n2","text":"Open","sender":"assistant","created_at":"2026-10-07T10:01:00Z","files":[{"file_uuid":"aaaaaaaa-1111-4222-8333-444444444444","file_name":"sheet.xlsx"}],"content":[
  {"type":"thinking","thinking":"Looking","cut_off":true,"truncated":false},
  {"type":"tool_use","id":"t1","name":"image_search","input":{"query":"lake"}},
  {"type":"tool_result","tool_use_id":"t1","content":[{"type":"image_gallery","images":[{"url":"https://img.example/a.jpg"}],"spare_images":[{"url":"https://img.example/b.jpg","title":"Spare"}]}]},
  {"type":"tool_use","id":"t2","name":"image_search","input":{"query":"old"}},
  {"type":"tool_result","tool_use_id":"t2","content":[{"type":"image_gallery","images":[],"is_expired":true}]},
  {"type":"tool_use","id":"t3","name":"SendUserFile","input":{"files":["/home/claude/sheet.xlsx"],"caption":"The tracker"}},
  {"type":"tool_result","tool_use_id":"t3","name":"SendUserFile","content":[{"type":"text","text":"1 file delivered to user.\n  sheet.xlsx → file_uuid: aaaaaaaa-1111-4222-8333-444444444444"}]},
  {"type":"text","text":"Café is open. Go.","voice_spoken_chars":5,"citations":[{"start_index":0,"end_index":13,"details":{"type":"web_search_citation","url":"https://cafe.example/hours"}}]}]}]}]`))[0].Conversations[0]
	byName := map[string]AssetRecord{}
	for _, asset := range noted.Assets {
		byName[asset.Name] = asset
	}
	if sheet, spare, expired := byName["sheet.xlsx"], byName["Spare"], byName["Image results"]; len(noted.Assets) != 4 || sheet.Ref != "aaaaaaaa-1111-4222-8333-444444444444" || sheet.Details["path"] != "/home/claude/sheet.xlsx" ||
		sheet.Details["description"] != "The tracker" || sheet.Availability != assetNotExported || !slices.Contains(sheet.Also, "n2:files") || spare.Details["spare"] != true || byName["Image"].Details["spare"] != nil ||
		expired.Details["expired"] != true || expired.Availability != assetNotExported {
		t.Fatalf("noted assets = %+v", noted.Assets)
	}
	if notes := noted.Notes; len(notes) != 2 || notes[0].MessageNativeID != "n2:block:0" || notes[0].Details["cut_off"] != true || notes[0].Details["truncated"] != nil || notes[1].MessageNativeID != "n2" || integer(notes[1].Details["spoken_chars"]) != 5 ||
		jsonText(notes[1].Details["citations"]) != `[{"end":13,"start":0,"url":"https://cafe.example/hours"}]` {
		t.Fatalf("notes = %+v", noted.Notes)
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
		"artifacts/a/artifact.json":                          `{"id":"a","active_version":"v1","versions":[{"id":"v1","title":"A retained document"},{"id":"v0","title":"A draft"},{"id":"gone","title":"Not kept"}]}`,
		"artifacts/a/versions/v1/index.html":                 `<link rel="stylesheet" href="style.css"><h1>Archived document</h1><img alt="" src="photo.webp"><a href="more/page.html">More</a><script>neverExecute()</script>`,
		"artifacts/a/versions/v1/style.css":                  `h1{color:teal}`,
		"artifacts/a/versions/v1/more/page.html":             `<link rel=stylesheet href='../style.css'><p>Second page</p><img src="https://elsewhere.example/x.png">`,
		"artifacts/a/versions/v0.html":                       `<h1>Draft</h1>`,
		"artifacts/a/comments.json":                          `[{"quoted_text":"Archived","resolved":true,"tab":"Main","comments":[{"body":"Say more here","created_at":"2026-09-21T20:18:59.774Z"}]},{"quoted_text":"empty","comments":[]}]`,
		"artifacts/a/download.zip":                           string(attachmentBytes),
		"artifacts/b/download.ZIP":                           string(attachmentBytes),
		"artifacts/a/versions/v1/photo.webp":                 "RIFF\x00binary-image",
		"claude_code_cloud/channel_session_transcripts.json": `{"claude_code_cloud":[{"session_id":"cloud","role":"user","content":[{"type":"text","text":"Cloud question"}]},{"session_id":"cloud","role":"assistant","content":[{"type":"tool_use","tool_use_id":"tool","name":"Search","input":"{\"query\":\"history\"}"},{"type":"tool_result","tool_use_id":"tool","content":"A cloud result"},{"type":"tool_use","tool_use_id":"publish","name":"Artifact","input":"{\"file_path\":\"/tmp/draft.html\"}"},{"type":"tool_result","tool_use_id":"publish","content":"Published /tmp/draft.html at https://claude.ai/artifact/AbC123 (Version 1, version id v0) Icon: \"globe\"."},{"type":"text","text":"Cloud answer"}]}]}`,
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
	if len(records) != 8 || files != 13 || calls != 1 || results != 1 {
		t.Fatalf("records=%d files=%d calls=%d results=%d", len(records), files, calls, results)
	}
	// An artifact is an asset with its versions, the pages of the current
	// one, what was said about it, and the pictures its pages show.
	for _, r := range records {
		if r.SourceID != "claude-export:artifacts/a" {
			continue
		}
		assets := r.Conversations[0].Assets
		if len(assets) != 2 || assets[0].Kind != "artifact" || assets[0].Availability != assetStored || assets[0].AttachmentNativeID != "artifacts/a/versions/v1/index.html" ||
			!reflect.DeepEqual(assets[0].Details["pages"], []string{"index.html", "more/page.html"}) || integer(assets[0].Details["files"]) != 4 ||
			len(assets[0].Versions) != 3 || !assets[0].Versions[0].Stored || !assets[0].Versions[1].Stored || assets[0].Versions[2].Stored {
			t.Fatalf("artifact = %+v", assets)
		}
		if threads := reasoningList(assets[0].Details["comments"]); len(threads) != 1 || mapValue(threads[0])["quoted_text"] != "Archived" || mapValue(threads[0])["resolved"] != true || mapValue(reasoningList(mapValue(threads[0])["comments"])[0])["body"] != "Say more here" {
			t.Fatalf("comments = %+v", assets[0].Details["comments"])
		}
		if photo := assets[1]; photo.Kind != "image" || photo.MessageNativeID != "artifacts/a/versions/v1/photo.webp" || photo.Content != "RIFF\x00binary-image" || photo.Availability != assetStored {
			t.Fatalf("picture = %+v", photo)
		}
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
			if record.SourceID == "claude-export:artifacts/a" && len(record.Conversations[0].Messages) != 8 {
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
	if err := other.DB.QueryRow(`SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id='claude-export:artifacts/a'`).Scan(&artifactFiles); err != nil || artifactFiles != 8 {
		t.Fatalf("captured artifact files: %d, %v", artifactFiles, err)
	}
	// A page of the artifact is served whole: what it loads from beside it
	// is put into it, since its frame may ask for nothing, and its links
	// are told to the reader. Each kept version and page can be asked for,
	// and nothing outside the version.
	var artifact string
	if err := other.DB.QueryRow(`SELECT id FROM message_assets WHERE kind='artifact'`).Scan(&artifact); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, other)
	page := func(query string) (int, string) {
		response := httptest.NewRecorder()
		server.asset(response, httptest.NewRequest("GET", "/api/assets/"+artifact+query, nil))
		return response.Code, response.Body.String()
	}
	if code, body := page(""); code != 200 || !strings.Contains(body, "<style>h1{color:teal}</style>") || !strings.Contains(body, `src="data:image/webp;base64,`+base64.StdEncoding.EncodeToString([]byte("RIFF\x00binary-image"))+`"`) ||
		strings.Contains(body, `href="style.css"`) || !strings.Contains(body, "pharosAssetLink") || !strings.Contains(body, `<a href="more/page.html">`) {
		t.Fatal(code, body)
	}
	if code, body := page("?file=more/page.html"); code != 200 || !strings.Contains(body, "<style>h1{color:teal}</style><p>Second page</p>") || !strings.Contains(body, `src="https://elsewhere.example/x.png"`) {
		t.Fatal(code, body)
	}
	if code, body := page("?version=v0"); code != 200 || !strings.Contains(body, "<h1>Draft</h1>") {
		t.Fatal(code, body)
	}
	for _, query := range []string{"?version=gone", "?file=../../artifact.json", "?file=missing.html", "?version=v1/../v0"} {
		if code, body := page(query); code != 404 {
			t.Fatal(query, code, body)
		}
	}
	// The session whose agent published a version of the artifact shows it
	// at that call, and the artifact's own document names the session.
	var session, sessionWorkspace, document, documentWorkspace string
	if err := other.DB.QueryRow(`SELECT id,workspace_id FROM conversations WHERE harness='claude-code-cloud-export'`).Scan(&session, &sessionWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := other.DB.QueryRow(`SELECT id,workspace_id FROM conversations WHERE native_id='claude-export:artifacts/a'`).Scan(&document, &documentWorkspace); err != nil {
		t.Fatal(err)
	}
	made, err := other.WorkConversation(sessionWorkspace, session)
	if err != nil {
		t.Fatal(err)
	}
	// It is shown on the reply that follows the call, which a reader sees.
	var publish string
	for _, message := range made["messages"].([]map[string]any) {
		shown := message["assets"]
		if message["kind"] == "tool_call" && message["call_id"] == "publish" {
			publish = firstString(message["id"])
		}
		if message["kind"] != "message" || message["role"] != "assistant" {
			if shown != nil {
				t.Fatalf("artifact shown away from the reply: %+v", message)
			}
			continue
		}
		if list, _ := shown.([]map[string]any); len(list) != 1 || list[0]["kind"] != "artifact" || list[0]["availability"] != assetStored || list[0]["url"] != "/api/assets/"+artifact ||
			list[0]["version"] != "v1" || mapValue(list[0]["document"])["conversation_id"] != document {
			t.Fatalf("published artifact = %+v", shown)
		}
	}
	held, err := other.WorkConversation(documentWorkspace, document)
	if err != nil {
		t.Fatal(err)
	}
	if origins, _ := held["made_in"].([]map[string]any); publish == "" || len(origins) != 1 || origins[0]["conversation_id"] != session || origins[0]["workspace_id"] != sessionWorkspace || origins[0]["message_id"] != publish {
		t.Fatalf("made in = %+v, publish %q", held["made_in"], publish)
	}
	// A document that is not an artifact was made by no conversation, and
	// an artifact no conversation in the library made says so.
	for native, want := range map[string]any{"claude-export:projects/p.json": nil, "claude-export:artifacts/b": nil} {
		var id, workspace string
		if err := other.DB.QueryRow(`SELECT id,workspace_id FROM conversations WHERE native_id=?`, native).Scan(&id, &workspace); err != nil {
			t.Fatal(native, err)
		}
		if read, err := other.WorkConversation(workspace, id); err != nil || read["made_in"] != want {
			t.Fatalf("%s made in = %+v, %v", native, read["made_in"], err)
		}
	}
	// The session is found as one that made an artifact, by the publish alone.
	if made, err := other.searchConversationsContext(context.Background(), map[string]any{"has": []any{"artifacts"}, "limit": 100}); err != nil || !strings.Contains(jsonText(made), session) || strings.Contains(jsonText(made), document) {
		t.Fatalf("conversations with artifacts = %v, %v", made, err)
	}
	// Lists of conversations leave out the document of an artifact a
	// conversation made, and keep it once nothing in the library made it.
	listed := func() (bool, bool) {
		t.Helper()
		rows, err := other.searchRows(SearchOptions{}, libraryCompactFields)
		if err != nil {
			t.Fatal(err)
		}
		workspaces := map[string]bool{}
		for _, row := range rows {
			workspaces[firstString(row["id"])] = true
		}
		if !workspaces[sessionWorkspace] {
			t.Fatal("the session that made the artifact is not listed")
		}
		found, err := other.searchConversationsContext(context.Background(), map[string]any{"limit": 100})
		if err != nil {
			t.Fatal(err)
		}
		return workspaces[documentWorkspace], strings.Contains(jsonText(found), document)
	}
	if rows, conversations := listed(); rows || conversations {
		t.Fatalf("placed artifact document listed: rows %v, conversations %v", rows, conversations)
	}
	execSQL(t, other.DB, `DELETE FROM tool_calls WHERE tool_name='Artifact'`)
	if rows, conversations := listed(); !rows || !conversations {
		t.Fatalf("artifact document with no conversation not listed: rows %v, conversations %v", rows, conversations)
	}
	if held, err := other.WorkConversation(documentWorkspace, document); err != nil || held["made_in"] == nil || len(held["made_in"].([]map[string]any)) != 0 {
		t.Fatalf("made in, with no conversation = %+v, %v", held["made_in"], err)
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
