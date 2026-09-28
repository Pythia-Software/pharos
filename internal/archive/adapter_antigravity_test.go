package archive

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	antigravityRoot  = "aaaaaaaa-0000-4000-8000-000000000001"
	antigravityChild = "aaaaaaaa-0000-4000-8000-000000000002"
	antigravityOther = "aaaaaaaa-0000-4000-8000-000000000003"
)

func antigravitySteps(steps ...map[string]any) string {
	lines := ""
	for index, step := range steps {
		step["step_index"] = index
		if step["status"] == nil {
			step["status"] = "DONE"
		}
		if step["created_at"] == nil {
			step["created_at"] = "2026-09-28T14:03:00Z"
		}
		lines += jsonText(step) + "\n"
	}
	return lines
}

func antigravityTranscript(t *testing.T, dir, id, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, "brain", id, ".system_generated", "logs", name)
	writeSourceFile(t, path, content)
	return path
}

// antigravitySummaryRows writes conversation_summaries.db with the columns
// the adapter reads: id, title, workspaces, parent, depth, agent.
func antigravitySummaryRows(t *testing.T, dir string, rows ...[]any) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, antigravitySummaries))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id text PRIMARY KEY, title text NOT NULL DEFAULT "",
		workspace_uris text NOT NULL DEFAULT "", status text NOT NULL DEFAULT "", project_id text NOT NULL DEFAULT "", agent_name text NOT NULL DEFAULT "",
		parent_conversation_id text NOT NULL DEFAULT "", nesting_depth integer NOT NULL DEFAULT 0, app_data_dir text NOT NULL DEFAULT "")`); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT OR REPLACE INTO conversation_summaries(conversation_id,title,workspace_uris,parent_conversation_id,nesting_depth,agent_name,app_data_dir)
			VALUES(?,?,?,?,?,?,'antigravity-cli')`, row...); err != nil {
			t.Fatal(err)
		}
	}
}

func protoVarint(field int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field)<<3), value)
}

func protoMessage(field int, parts ...[]byte) []byte {
	body := []byte{}
	for _, part := range parts {
		body = append(body, part...)
	}
	return append(binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field)<<3|2), uint64(len(body))), body...)
}

func protoString(field int, value string) []byte { return protoMessage(field, []byte(value)) }

// antigravityRequestRows writes conversations/<id>.db with a gen_metadata row
// per request: the steps it generated, its model, and its usage (input,
// output, cache read, thinking), plus a retry that repeats the usage.
func antigravityRequestRows(t *testing.T, dir, id string, requests ...[]any) {
	t.Helper()
	path := filepath.Join(dir, "conversations", id+".db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS gen_metadata (idx integer PRIMARY KEY, data blob, size integer NOT NULL DEFAULT 0)"); err != nil {
		t.Fatal(err)
	}
	for index, request := range requests {
		steps, model := request[0].([]uint64), request[1].(string)
		input, output, cacheRead, thinking := request[2].(int), request[3].(int), request[4].(int), request[5].(int)
		usage := [][]byte{protoVarint(1, 1318), protoVarint(2, uint64(input)), protoVarint(3, uint64(output)), protoVarint(6, 24),
			protoVarint(9, uint64(thinking)), protoVarint(10, uint64(output-thinking)), protoString(11, fmt.Sprintf("response-%s-%d", id[:8], index))}
		if cacheRead > 0 {
			usage = append(usage, protoVarint(5, uint64(cacheRead)))
		}
		packed := []byte{}
		for _, step := range steps {
			packed = binary.AppendUvarint(packed, step)
		}
		chat := protoMessage(1, protoVarint(3, 1318), protoMessage(4, usage...), protoMessage(17, protoMessage(2, usage...)), protoString(19, model))
		data := append(append(chat, protoMessage(2, packed)...), protoString(4, "execution")...)
		if _, err := db.Exec("INSERT OR REPLACE INTO gen_metadata(idx,data,size) VALUES(?,?,?)", index, data, len(data)); err != nil {
			t.Fatal(err)
		}
	}
}

func antigravityFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "antigravity-cli")
	antigravityTranscript(t, dir, antigravityRoot, "transcript_full.jsonl", antigravitySteps(
		map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "<USER_REQUEST>\nFix the build\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-09-28T08:03:00-06:00.\n</ADDITIONAL_METADATA>\n<USER_SETTINGS_CHANGE>\nThe user changed setting `Model Selection` from None to Gemini 3.8 Flash (High). No need to comment on this change.\n</USER_SETTINGS_CHANGE>",
			"media": []any{map[string]any{"mime_type": "image/png", "uri": "/tmp/shot.png"}}},
		map[string]any{"source": "MODEL", "type": "PLANNER_RESPONSE", "thinking": "private reasoning", "content": "Looking.", "tool_calls": []any{
			map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "go build ./...", "Cwd": "/repo"}},
			map[string]any{"name": "view_file", "args": map[string]any{"AbsolutePath": "/repo/main.go"}},
		}},
		map[string]any{"source": "MODEL", "type": "GENERIC", "content": "The command exited with code 1."},
		map[string]any{"source": "MODEL", "type": "GENERIC", "status": "ERROR", "error": "permission denied", "content": "Encountered error"},
		map[string]any{"source": "MODEL", "type": "PLANNER_RESPONSE", "tool_calls": []any{map[string]any{"name": "invoke_subagent", "args": map[string]any{"Prompt": "check tests"}}}},
		map[string]any{"source": "MODEL", "type": "GENERIC", "content": "Subagent finished."},
		map[string]any{"source": "SYSTEM", "type": "CHECKPOINT", "content": "saved"},
		map[string]any{"source": "MODEL", "type": "PLANNER_RESPONSE", "content": "Fixed.", "created_at": "2026-09-28T14:09:00Z"},
	))
	antigravityTranscript(t, dir, antigravityChild, "transcript_full.jsonl", antigravitySteps(
		map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "<USER_REQUEST>\ncheck tests\n</USER_REQUEST>"},
		map[string]any{"source": "MODEL", "type": "PLANNER_RESPONSE", "content": "Tests pass."},
	))
	// Only a truncated transcript, and a title only in its annotation.
	antigravityTranscript(t, dir, antigravityOther, "transcript.jsonl", antigravitySteps(
		map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "hello"},
	))
	probePut(t, filepath.Join(dir, "annotations", antigravityOther+".pbtxt"), `title:"Say \"hi\"" last_user_view_time:{seconds:1}`)
	// Requests answered steps 1, 4 and 7; the one for step 7 read the cache.
	antigravityRequestRows(t, dir, antigravityRoot,
		[]any{[]uint64{1, 2, 3}, "gemini-3.8-flash", 1000, 300, 0, 200},
		[]any{[]uint64{4, 5}, "gemini-3.8-flash", 1500, 50, 0, 10},
		[]any{[]uint64{7}, "gemini-3.8-flash", 200, 100, 1800, 40})
	antigravitySummaryRows(t, dir,
		[]any{antigravityRoot, "Fix the build", `["file:///repo"]`, "", 0, ""},
		[]any{antigravityChild, "Check tests", `["file:///repo"]`, antigravityRoot, 1, "tester"})
	return dir
}

func discoverAll(t *testing.T, source SourceConfig) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestAntigravityTranscriptsBecomeConversations(t *testing.T) {
	dir := antigravityFixture(t)
	records := discoverAll(t, SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: dir, Account: "local"})
	if len(records) != 2 {
		t.Fatalf("records = %d, want the root (with its subagent) and the other conversation", len(records))
	}
	root := records[antigravityRoot]
	if root.SourceKind != "antigravity" || root.Title != "Fix the build" || root.Location != "/repo" || root.Metadata["app"] != "antigravity-cli" || root.Purpose != "Fix the build" || root.Outcome != "Fixed." || root.ActivityAt != "2026-09-28T14:09:00.000Z" {
		t.Fatalf("root workspace = %+v", root)
	}
	if len(root.Conversations) != 2 {
		t.Fatalf("root conversations = %d", len(root.Conversations))
	}
	main, child := root.Conversations[0], root.Conversations[1]
	if main.Provider != "antigravity" || main.Model != "gemini-3.8-flash" || root.Metadata["model_selection"] != "Gemini 3.8 Flash (High)" || main.ParentNativeID != "" || main.Origin != filepath.Join(dir, "brain", antigravityRoot, ".system_generated", "logs", "transcript_full.jsonl") {
		t.Fatalf("main conversation = %+v", main)
	}
	got := []string{}
	for _, message := range main.Messages {
		got = append(got, message.NativeID+" "+message.Role+" "+message.Kind+" "+message.CallID)
	}
	want := []string{
		"step-0 user message ", "step-0:attachments user attachment ", "step-0:context:0 system context ", "step-0:context:1 system context ",
		"step-1 assistant message ", "step-1:call:0 assistant tool_call step-1:call:0", "step-1:call:1 assistant tool_call step-1:call:1", "step-1:usage system metadata ",
		"step-2 tool tool_result step-1:call:0", "step-3 tool tool_result step-1:call:1",
		"step-4:call:0 assistant delegation step-4:call:0", "step-4:usage system metadata ", "step-5 tool delegation_result step-4:call:0",
		"step-6 system metadata ", "step-7 assistant message ", "step-7:usage system metadata ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	byID := map[string]MessageRecord{}
	for _, message := range main.Messages {
		byID[message.NativeID] = message
	}
	if text := byID["step-0"].Text; text != "Fix the build" {
		t.Fatalf("the person's words = %q", text)
	}
	if !strings.Contains(byID["step-0:context:0"].Text, `"source":"antigravity_additional_metadata"`) || !strings.Contains(byID["step-0:context:1"].Text, "Model Selection") {
		t.Fatalf("injected context = %q %q", byID["step-0:context:0"].Text, byID["step-0:context:1"].Text)
	}
	var failed map[string]any
	if err := json.Unmarshal([]byte(byID["step-3"].Text), &failed); err != nil || failed["tool"] != "view_file" || failed["is_error"] != true || mapValue(failed["details"])["error"] != "permission denied" {
		t.Fatalf("failed tool result = %s", byID["step-3"].Text)
	}
	if strings.Contains(jsonText(main.Messages), "private reasoning") {
		t.Fatal("model reasoning was kept")
	}
	if byID["step-0"].Model != "Gemini 3.8 Flash (High)" || byID["step-1"].Model != "gemini-3.8-flash" || byID["step-7:usage"].EvidenceLocator != filepath.Join(dir, "conversations", antigravityRoot+".db")+":gen_metadata:2" {
		t.Fatalf("models %q %q, usage evidence %q", byID["step-0"].Model, byID["step-1"].Model, byID["step-7:usage"].EvidenceLocator)
	}
	// Input counts cached input; output counts thinking; the retry that
	// repeats each request's usage is not counted again.
	metrics := map[string]any{}
	for _, metric := range root.Metrics {
		metrics[firstString(metric["name"])] = metric["value"]
	}
	for name, want := range map[string]float64{"input_tokens": 4500, "uncached_input_tokens": 2700, "cache_read_input_tokens": 1800, "output_tokens": 450, "reasoning_output_tokens": 250, "total_tokens": 4950} {
		if metrics[name] != want {
			t.Fatalf("%s = %v, want %v (metrics %v)", name, metrics[name], want, metrics)
		}
	}
	if child.NativeID != antigravityChild || child.ParentNativeID != antigravityRoot || child.AgentDepth != 1 || child.AgentNickname != "tester" || child.Messages[0].Role != agentRole {
		t.Fatalf("subagent = %+v", child)
	}
	other := records[antigravityOther]
	if other.Title != `Say "hi"` || len(other.Conversations) != 1 || other.Conversations[0].Messages[0].Text != "hello" || other.Metadata["app"] != "antigravity-cli" {
		t.Fatalf("conversation without a summary = %+v", other)
	}
}

func TestAntigravityIngestIsIncrementalAndCaptured(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	dir := antigravityFixture(t)
	source := SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: dir, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	parses := countParses(t)
	if result := ingestSource(t, catalog, source); result.Workspaces != 2 || parses.Load() != 3 {
		t.Fatalf("first ingest: %+v, %d parses", result, parses.Load())
	}
	if n := countRows(t, catalog, "SELECT COUNT(*) FROM conversations WHERE provider='antigravity'"); n != 3 {
		t.Fatalf("conversations = %d", n)
	}
	if result := ingestSource(t, catalog, source); !result.SkippedUnchanged {
		t.Fatalf("unchanged source was scanned: %+v", result)
	}
	// A retitle changes only the summaries database.
	parses.Store(0)
	antigravitySummaryRows(t, dir, []any{antigravityRoot, "Fix the Go build", `["file:///repo"]`, "", 0, ""})
	if result := ingestSource(t, catalog, source); result.Workspaces != 1 || parses.Load() != 2 || result.Unchanged != 1 {
		t.Fatalf("retitled: %+v, %d parses", result, parses.Load())
	}
	if n := countRows(t, catalog, "SELECT COUNT(*) FROM workspaces WHERE source_kind='antigravity' AND title='Fix the Go build'"); n != 1 {
		t.Fatal("the new title was not indexed")
	}
	// Usage recorded after the transcript was last written.
	parses.Store(0)
	before := countRows(t, catalog, "SELECT COUNT(*) FROM messages WHERE text LIKE '%\"output_tokens\":999%'")
	antigravityRequestRows(t, dir, antigravityRoot, []any{[]uint64{1}, "gemini-3.8-flash", 1000, 999, 0, 200})
	if result := ingestSource(t, catalog, source); result.Workspaces != 1 || parses.Load() != 2 {
		t.Fatalf("new usage: %+v, %d parses", result, parses.Load())
	}
	if after := countRows(t, catalog, "SELECT COUNT(*) FROM messages WHERE text LIKE '%\"output_tokens\":999%'"); before != 0 || after != 1 {
		t.Fatalf("new usage indexed: before %d, after %d", before, after)
	}

	// A capture holds everything the adapter reads, so another catalog can
	// index it without the original folder.
	runCapture(t, config)
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	for _, query := range []string{
		"SELECT COUNT(*) FROM workspaces WHERE source_kind='antigravity' AND title='Fix the Go build' AND location='/repo'",
		"SELECT COUNT(*) FROM workspaces WHERE title='Say \"hi\"'",
	} {
		if countRows(t, other, query) != 1 {
			t.Fatalf("capture index: %s found nothing", query)
		}
	}
	if a, b := countRows(t, catalog, "SELECT COUNT(*) FROM messages"), countRows(t, other, "SELECT COUNT(*) FROM messages"); a != b || a == 0 {
		t.Fatalf("messages live %d, from capture %d", a, b)
	}
}

func TestProbeOffersAntigravityToMacsAlreadySetUp(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	for _, app := range []string{"antigravity", "antigravity-cli"} {
		antigravityTranscript(t, filepath.Join(home, ".gemini", app), probeID2, "transcript_full.jsonl", "{}\n")
	}
	// Not an Antigravity app directory: no brain folder.
	probePut(t, filepath.Join(home, ".gemini", "antigravity-updater", "state"), "")
	report := probeSources(home, Config{}, nil)
	for _, name := range []string{"antigravity", "antigravity-cli"} {
		candidate := candidateNamed(t, report, name)
		if candidate.Kind != "antigravity" || candidate.Status != "found" || candidate.Count != 1 || candidate.Unit != "conversation" || !candidate.Recommended || candidate.Path != filepath.Join(home, ".gemini", name) {
			t.Fatalf("%s = %+v", name, candidate)
		}
	}
	for _, candidate := range report.Candidates {
		if strings.Contains(candidate.Path, "updater") {
			t.Fatalf("probe offered %+v", candidate)
		}
	}

	path := probeLibrary(t, "")
	server := probeServer(t, path)
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["needs_onboarding"] != true || status["new_sources"] != float64(0) {
		t.Fatalf("a Mac not yet set up is onboarded, not offered new sources: %v", status)
	}
	// Set up before Pharos read Antigravity: only Claude was decided on.
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["needs_onboarding"] != false || status["new_sources"] != float64(2) {
		t.Fatalf("status of a Mac set up before Antigravity = %v", status)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["antigravity-cli"],"decline":["antigravity"]}`); code != 200 || jsonText(result["added"]) != `["antigravity-cli"]` {
		t.Fatalf("accept antigravity = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(0) {
		t.Fatalf("decided sources are still offered: %v", status)
	}
	host := probeReadText(t, filepath.Join(filepath.Dir(path), "hosts", "host-a.toml"))
	if !strings.Contains(host, "name = \"antigravity-cli\"\nkind = \"antigravity\"\npath = \"~/.gemini/antigravity-cli\"\nenabled = true") {
		t.Fatalf("host file:\n%s", host)
	}
}

func TestAntigravityCapturePlanListsWhatTheAdapterReads(t *testing.T) {
	dir := antigravityFixture(t)
	probePut(t, filepath.Join(dir, "brain", antigravityRoot, "scratch", "notes.txt"), "not a transcript")
	plan, err := planCapture(SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	files := []string{}
	for _, item := range plan.files {
		files = append(files, item.rel)
	}
	want := []string{
		"annotations/" + antigravityOther + ".pbtxt",
		"brain/" + antigravityRoot + "/.system_generated/logs/transcript_full.jsonl",
		"brain/" + antigravityChild + "/.system_generated/logs/transcript_full.jsonl",
		"brain/" + antigravityOther + "/.system_generated/logs/transcript.jsonl",
	}
	databases := []string{}
	for _, item := range plan.databases {
		databases = append(databases, item.rel)
	}
	if strings.Join(files, "\n") != strings.Join(want, "\n") || strings.Join(databases, ",") != antigravitySummaries+",conversations/"+antigravityRoot+".db" {
		t.Fatalf("files = %v, databases = %v", files, databases)
	}
	if _, err := os.Stat(filepath.Join(dir, antigravitySummaries)); err != nil {
		t.Fatal(err)
	}
}
