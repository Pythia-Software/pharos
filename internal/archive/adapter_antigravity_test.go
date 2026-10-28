package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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
	// Transcripts last written a month ago are cold (compressed below).
	transcripts, _ := filepath.Glob(filepath.Join(dir, "brain", "*", filepath.FromSlash(antigravityLogs), "transcript*.jsonl"))
	if len(transcripts) == 0 {
		t.Fatal("fixture has no transcripts")
	}
	for _, transcript := range transcripts {
		old := time.Now().AddDate(0, -1, 0)
		if err := os.Chtimes(transcript, old, old); err != nil {
			t.Fatal(err)
		}
	}
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

	// Cold transcripts the index recorded are stored compressed; another
	// index of them, forced, reads them through the capture view.
	override(t, &compressMinBytes, 1)
	runCapture(t, config)
	compressed := 0
	if err := compressCaptures(t.Context(), config, other, func(compressedGeneration) { compressed++ }); err != nil || compressed == 0 {
		t.Fatalf("compressed %d of %d transcripts: %v", compressed, len(transcripts), err)
	}
	forced, _ := testCatalog(t)
	targets, err := captureTargets(config.CaptureRoot, []string{"host-a"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if result := forced.IndexCapture(withIngestMode(t.Context(), ingestMode{force: true}), target, nil); result.Error != nil {
			t.Fatal(result.Error)
		}
	}
	if a, b := countRows(t, catalog, "SELECT COUNT(*) FROM messages"), countRows(t, forced, "SELECT COUNT(*) FROM messages"); a != b {
		t.Fatalf("messages live %d, from compressed capture %d", a, b)
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

// The CLI logs each run, and a run with -p names its conversation there; the
// transcripts do not record it. A capture keeps the logs the CLI rotates away.
func TestAntigravityPrintModeRunsAreHeadless(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	dir := antigravityFixture(t)
	source := SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: dir, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	sender := func(catalog *Catalog, native string) string {
		t.Helper()
		rows, err := queryMaps(catalog.DB, `SELECT COALESCE(m.sender,'') sender FROM messages m JOIN conversations c ON c.id=m.conversation_id
			WHERE c.native_id=? AND m.kind='message' AND m.role IN ('user','agent')`, native)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s prompts = %v %v", native, rows, err)
		}
		return firstString(rows[0]["sender"])
	}
	ingestSource(t, catalog, source)
	if got := sender(catalog, antigravityRoot); got != "" {
		t.Fatalf("sender before any log = %q", got)
	}
	logs := []string{filepath.Join(dir, "log", "cli-20260928_080412.log"), filepath.Join(dir, "log", "cli-20260928_090000.log")}
	probePut(t, logs[0], "I0928 08:03:00.100000 1 printmode.go:181] Print mode: starting (promptLength=13)\n"+
		"I0928 08:03:01.200000 1 session.go:180] Print mode: conversation="+antigravityRoot+", sending message\n")
	probePut(t, logs[1], "I0928 09:00:00.100000 1 server.go:1584] Starting language server process\n")
	parses := countParses(t)
	if result := ingestSource(t, catalog, source); result.Workspaces != 1 || parses.Load() != 2 {
		t.Fatalf("after the log: %+v, %d parses", result, parses.Load())
	}
	if root, child, other := sender(catalog, antigravityRoot), sender(catalog, antigravityChild), sender(catalog, antigravityOther); root != "automation:agy-print" || child != "" || other != "" {
		t.Fatalf("senders = %q %q %q", root, child, other)
	}
	plan, err := planCapture(source)
	if err != nil {
		t.Fatal(err)
	}
	captured := map[string]bool{}
	for _, item := range plan.files {
		captured[item.rel] = true
	}
	if !captured["log/cli-20260928_080412.log"] || !captured["log/cli-20260928_090000.log"] {
		t.Fatalf("capture plan = %v", plan.files)
	}
	runCapture(t, config)
	for _, log := range logs {
		if err := os.Remove(log); err != nil {
			t.Fatal(err)
		}
	}
	runCapture(t, config)
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got := sender(other, antigravityRoot); got != "automation:agy-print" {
		t.Fatalf("sender from a capture the CLI rotated its log out of = %q", got)
	}

	// Once the transcripts are cold and stored compressed, print-mode
	// detection, which reads transcripts' start times on every plan, reads
	// their original bytes, and a normal index decodes nothing.
	override(t, &compressMinBytes, 1)
	transcripts, _ := filepath.Glob(filepath.Join(config.CaptureRoot, "host-a", "antigravity-cli", "files", "brain", "*", filepath.FromSlash(antigravityLogs), "transcript*.jsonl"))
	old := time.Now().AddDate(0, -1, 0)
	for _, transcript := range transcripts {
		if err := os.Chtimes(transcript, old, old); err != nil {
			t.Fatal(err)
		}
	}
	lease := func() *antigravityAdapter {
		t.Helper()
		target := captureTarget{Host: currentHost(), Source: "antigravity-cli", Dir: filepath.Join(config.CaptureRoot, "host-a", "antigravity-cli")}
		input, err := leaseCaptureForIndex(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(input.Close)
		return input.Adapter.(*antigravityAdapter)
	}
	plain := lease()
	starts := map[string]time.Time{}
	plainTranscripts, err := plain.transcripts()
	if err != nil {
		t.Fatal(err)
	}
	for id, path := range plainTranscripts {
		if starts[id], err = plain.files().readAntigravityStarted(path); err != nil {
			t.Fatal(err)
		}
	}
	// The capture keeps source mtimes: make its copies cold by recording them
	// in the manifest as the source's.
	manifest, err := loadCaptureManifest(filepath.Join(config.CaptureRoot, "host-a", "antigravity-cli"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		if slices.ContainsFunc(transcripts, func(path string) bool {
			return strings.HasSuffix(path, filepath.FromSlash(strings.TrimPrefix(file.Captured, "files/")))
		}) {
			file.MTimeNS, file.MTime = old.UnixNano(), old.UTC().Format(time.RFC3339Nano)
		}
	}
	if err = writeCaptureManifest(filepath.Join(config.CaptureRoot, "host-a", "antigravity-cli"), manifest); err != nil {
		t.Fatal(err)
	}
	reindexed, _ := testCatalog(t)
	indexCaptures(t, reindexed, config.CaptureRoot, "host-a")
	compressed := 0
	if err := compressCaptures(t.Context(), config, reindexed, func(compressedGeneration) { compressed++ }); err != nil || compressed == 0 {
		t.Fatal("no cold transcript was compressed", compressed, err)
	}
	var decodes atomic.Int64
	override(t, &capturedContentHook, func(string) { decodes.Add(1) })
	indexCaptures(t, reindexed, config.CaptureRoot, "host-a")
	if got := sender(reindexed, antigravityRoot); decodes.Load() != 0 || got != "automation:agy-print" {
		t.Fatalf("normal index over compressed transcripts: %d decodes, sender %q", decodes.Load(), got)
	}
	// With nothing cached, the start times read from the encoded transcripts
	// are the original ones.
	headCacheMu.Lock()
	headCache = map[headKey]any{}
	headCacheMu.Unlock()
	encoded := lease()
	encodedTranscripts, err := encoded.transcripts()
	if err != nil {
		t.Fatal(err)
	}
	for id, path := range encodedTranscripts {
		if got, err := encoded.files().antigravityStarted(path); err != nil || !got.Equal(starts[id]) || got.IsZero() {
			t.Fatalf("start of %s from its compressed transcript = %v, want %v", id, got, starts[id])
		}
	}
}

// Runs started in the same second share a log and overwrite each other's
// lines, losing a run's "Print mode: conversation=" line or every line naming
// its conversation. Any line naming it in a print-mode log, or starting just
// after a print-mode run did when no log names it, shows it ran with -p.
func TestAntigravityPrintModeSurvivesSharedRunLogs(t *testing.T) {
	dir := antigravityFixture(t)
	other := time.Date(2026, 9, 28, 14, 30, 2, 0, time.UTC)
	antigravityTranscript(t, dir, antigravityOther, "transcript.jsonl", antigravitySteps(
		map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "hello", "created_at": other.Format(time.RFC3339)},
	))
	adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: dir}}}
	printed := func() map[string]bool {
		t.Helper()
		transcripts, err := adapter.transcripts()
		if err != nil {
			t.Fatal(err)
		}
		printed, err := adapter.printModeConversations(transcripts)
		if err != nil {
			t.Fatal(err)
		}
		return printed
	}
	logName := func(started time.Time) string {
		return filepath.Join(dir, "log", "cli-"+started.In(time.Local).Format("20060102_150405")+".log")
	}
	shared := logName(other.Add(-2 * time.Second))
	probePut(t, shared, "I0928 08:30:00.100000 1 printmode.go:202] Print mode: starting (promptLength=13)\n"+
		"ger shutting down\nI0928 08:30:09.100000 500 server.go:1305] Stream goroutine exited for "+antigravityRoot+", sending completion signal\n")
	if got := printed(); !got[antigravityRoot] || !got[antigravityOther] {
		t.Fatalf("print-mode conversations of a shared log = %v", got)
	}
	// A conversation an interactive run's log names is not a print-mode run's,
	// and nor is one that started too long after a print-mode run.
	probePut(t, logName(other.Add(-time.Second)), "I0928 08:30:01.000000 1 server.go:1584] Streaming conversation "+antigravityOther+"\n")
	if got := printed(); !got[antigravityRoot] || got[antigravityOther] {
		t.Fatalf("print-mode conversations with an interactive log = %v", got)
	}
	if err := os.Remove(logName(other.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(shared, logName(other.Add(-antigravityLaunchWindow-time.Second))); err != nil {
		t.Fatal(err)
	}
	if got := printed(); !got[antigravityRoot] || got[antigravityOther] {
		t.Fatalf("print-mode conversations with a log started long before = %v", got)
	}
}

// TestAntigravityUnreadableRequestWAL: a conversation whose request database
// has a WAL that cannot be read fails discovery, rather than being taken for
// one with no requests. It is neither checked nor emitted, and a healthy
// sibling still is. Once the WAL is readable, discovery recovers.
func TestAntigravityUnreadableRequestWAL(t *testing.T) {
	directory := t.TempDir()
	content := antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "request metadata"})
	antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", content)
	antigravityTranscript(t, directory, antigravityOther, "transcript_full.jsonl", content)
	antigravityRequestRows(t, directory, antigravityRoot)
	// A WAL that links to itself cannot be opened.
	wal := filepath.Join(directory, "conversations", antigravityRoot+".db-wal")
	if err := os.Symlink(filepath.Base(wal), wal); err != nil {
		t.Fatal(err)
	}
	adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity", Kind: "antigravity", Path: directory}}}
	var checked, emitted []string
	err := adapter.discoverParts(func(parts []sourcePart) bool {
		checked = append(checked, parts[0].item)
		return false
	}, func(record *WorkspaceRecord, _ []sourcePart) error {
		emitted = append(emitted, record.SourceID)
		return nil
	})
	// The one group checked is the sibling, which was emitted.
	if err == nil || len(checked) != 1 || !slices.Equal(emitted, []string{antigravityOther}) {
		t.Fatalf("an unreadable WAL advanced its group or aborted its sibling: checked=%v emitted=%v error=%v", checked, emitted, err)
	}
	if err := os.Remove(wal); err != nil {
		t.Fatal(err)
	}
	if err := adapter.discoverParts(func([]sourcePart) bool { return true }, func(*WorkspaceRecord, []sourcePart) error { return nil }); err != nil {
		t.Fatalf("discovery did not recover after the WAL became readable: %v", err)
	}
}

func TestAntigravityMovingGroupDoesNotAbortHealthySibling(t *testing.T) {
	for _, movement := range []string{"append", "remove", "remove-before-open", "remove-before-stat"} {
		t.Run(movement, func(t *testing.T) {
			directory := t.TempDir()
			content := antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "stable message"})
			rootPath := antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", content)
			childPath := antigravityTranscript(t, directory, antigravityChild, "transcript_full.jsonl", content)
			antigravityTranscript(t, directory, antigravityOther, "transcript_full.jsonl", content)
			antigravitySummaryRows(t, directory,
				[]any{antigravityRoot, "Root", "", "", 0, ""},
				[]any{antigravityChild, "Child", "", antigravityRoot, 1, ""},
				[]any{antigravityOther, "Sibling", "", "", 0, ""})
			adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity", Kind: "antigravity", Path: directory}}}
			previousHook := parseHook
			t.Cleanup(func() { parseHook = previousHook })
			parseHook = func(path string) {
				if path != rootPath {
					return
				}
				switch movement {
				case "append":
					appendSourceFile(t, rootPath, content)
				case "remove":
					if err := os.Remove(rootPath); err != nil {
						t.Fatal(err)
					}
				case "remove-before-open":
					if err := os.Remove(childPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			var emitted []string
			unchanged := func(parts []sourcePart) bool {
				if movement == "remove-before-stat" && parts[0].item == adapter.original(rootPath) {
					if err := os.Remove(filepath.Join(directory, "brain", antigravityOther, antigravityLogs, "transcript_full.jsonl")); err != nil {
						t.Fatal(err)
					}
				}
				return false
			}
			err := adapter.discoverParts(unchanged, func(record *WorkspaceRecord, _ []sourcePart) error {
				emitted = append(emitted, record.SourceID)
				return nil
			})
			want := []string{antigravityOther}
			if movement == "remove-before-stat" {
				want = []string{antigravityRoot}
			}
			if err != nil || !slices.Equal(emitted, want) {
				t.Fatalf("moving group aborted discovery or emitted partial state: emitted=%v error=%v", emitted, err)
			}
			parseHook = nil
			if movement == "append" {
				emitted = nil
				if err := adapter.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
					emitted = append(emitted, record.SourceID)
					return nil
				}); err != nil || !slices.Contains(emitted, antigravityRoot) {
					t.Fatalf("stable group was not eligible on retry: emitted=%v error=%v", emitted, err)
				}
			}
		})
	}
}

func TestAntigravityAuditExcludesUnrelatedRunLogEvidence(t *testing.T) {
	useHost(t, "host-a")
	directory := t.TempDir()
	antigravityTranscript(t, directory, antigravityRoot, "transcript_full.jsonl", antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "bounded real CLI evidence"}))
	writeSourceFile(t, filepath.Join(directory, "log", "cli-relevant.log"), "Print mode: conversation="+antigravityRoot+"\n")
	// Bigger than the automatic budget on its own.
	override(t, &automaticAuditBytes, 64<<10)
	writeSourceFile(t, filepath.Join(directory, "log", "cli-unrelated.log"), strings.Repeat("unrelated log data", 4<<10))
	source := SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: directory, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "cli-evidence", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("unrelated CLI logs defeated a bounded group audit: %+v", result)
	}
}

func TestAntigravityLogReaderHandlesChunkBoundariesAndLongLines(t *testing.T) {
	for _, offset := range []int{0, 32710, 32740, 32768, 65500, 65536} {
		path := filepath.Join(t.TempDir(), "cli.log")
		content := strings.Repeat("x", offset) + "Print mode: conversation=" + antigravityRoot + "\n" + strings.Repeat("x", 128*1024) + "Print mode: conversation=" + antigravityChild + "\nPrint mode: conversation=" + antigravityRoot
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		log, err := readAntigravityRunLog(t.Context(), path, make([]byte, 64*1024))
		if err != nil || !log.print || !slices.Equal(log.ids, []string{antigravityRoot, antigravityChild}) {
			t.Fatalf("offset %d: streaming print-mode IDs=%v error=%v", offset, log.ids, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := readAntigravityRunLog(ctx, path, make([]byte, 64*1024)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled log read did not stop: %v", err)
		}
	}
}

// A transcript's start time that could not be read (the file could not be
// opened, or its encoded bytes not decoded) is not cached as "no start
// time": print-mode attribution would be lost for the life of the process.
func TestAntigravityStartTimeFailuresAreNotCached(t *testing.T) {
	started := time.Date(2026, 9, 28, 14, 30, 2, 0, time.UTC)
	content := []byte(antigravitySteps(map[string]any{"type": "USER_INPUT", "content": "hello", "created_at": started.Format(time.RFC3339)}))
	dir := t.TempDir()

	// Open failure: a transcript that stats (so it has a cache key) but
	// cannot be opened, then can, unchanged.
	live := filepath.Join(dir, "live", "transcript.jsonl")
	writeSourceFile(t, live, string(content))
	if err := os.Chmod(live, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(live, 0o600) })
	if got, err := (sourceFiles{}).antigravityStarted(live); err == nil || !got.IsZero() {
		t.Fatal("start time of a transcript that cannot be opened", got, err)
	}
	if err := os.Chmod(live, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := (sourceFiles{}).antigravityStarted(live); err != nil || !got.Equal(started) {
		t.Fatalf("start time after an open failure = %v (%v), want %v", got, err, started)
	}

	// A plan whose print-mode detection cannot read a conversation's start
	// time leaves that group undiscovered, with an error, rather than
	// discovering it as an interactive run.
	source := antigravityFixture(t)
	probePut(t, filepath.Join(source, "log", "cli-20260928_080412.log"), "I0928 08:03:00.100000 1 printmode.go:181] Print mode: starting (promptLength=13)\n")
	adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: source}}}
	transcripts, err := adapter.transcripts()
	if err != nil {
		t.Fatal(err)
	}
	unreadable := transcripts[antigravityOther]
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o600) })
	plan, err := adapter.planParts()
	if err != nil {
		t.Fatal(err)
	}
	if plan.discoveryError == nil || !strings.Contains(plan.discoveryError.Error(), "start time") {
		t.Fatal("unreadable start time did not fail its group's discovery", plan.discoveryError)
	}
	for _, group := range plan.groups {
		for _, part := range group.parts {
			if part.item == unreadable {
				t.Fatal("the group whose start time is unreadable was planned")
			}
		}
	}

	// Decode failure: an encoded capture whose bytes do not decode, then the
	// real encoding under the same name, size and mtime (the same cache key).
	var encoded bytes.Buffer
	writer, err := zstd.NewWriter(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write(content)
	writer.Close()
	rel := "files/brain/x/transcript.jsonl"
	file := filepath.Join(dir, "capture", filepath.FromSlash(rel))
	corrupt := append([]byte{}, encoded.Bytes()...)
	for i := len(zstdMagic) + 8; i < len(corrupt); i++ {
		corrupt[i] ^= 0x5a
	}
	writeSourceFile(t, file, string(corrupt))
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	view := &captureView{host: Host{ID: "host-a"}, dir: filepath.Join(dir, "capture"), manifest: &captureManifest{Files: []*capturedFile{{Path: "/source/transcript.jsonl", Captured: rel, Size: int64(len(content)), Encoding: captureEncodingZstd, StoredSize: int64(encoded.Len())}}}}
	files := sourceFiles{view: view}
	if got, err := files.antigravityStarted(file); err == nil || !got.IsZero() {
		t.Fatal("start time from undecodable bytes", got, err)
	}
	if err := os.WriteFile(file, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if got, _ := files.antigravityStarted(file); !got.Equal(started) {
		t.Fatalf("start time after a decode failure = %v, want %v", got, started)
	}
}

// Idle latest snapshots: an Antigravity conversation's requests database
// whose source has not changed for capture_compress_after_days is stored
// compressed once indexed; the summaries database, which every plan reads,
// stays plain. A normal index decodes nothing, a forced reindex reads the
// compressed snapshot to the same catalog, pins, record exports and history
// planning read its original bytes, and a change at the source takes a new,
// uncompressed snapshot with the compressed one kept in history.
func TestAntigravityIdleSnapshotsAreCompressed(t *testing.T) {
	useHost(t, "host-a")
	override(t, &compressMinBytes, 1)
	catalog, config := testCatalog(t)
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	dir := antigravityFixture(t)
	old := time.Now().AddDate(0, -1, 0)
	databases, _ := filepath.Glob(filepath.Join(dir, "conversations", "*.db*"))
	databases = append(databases, filepath.Join(dir, antigravitySummaries))
	for _, database := range databases {
		if err := os.Chtimes(database, old, old); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	source := SourceConfig{Name: "antigravity-cli", Kind: "antigravity", Path: dir, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	runCapture(t, config)
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	runCapture(t, config)
	target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, "host-a", source.Name)}
	requests := "files/conversations/" + antigravityRoot + ".db"
	summaries := "files/" + antigravitySummaries

	compress := func() []string {
		t.Helper()
		compressed := []string{}
		if err := compressCaptures(t.Context(), config, catalog, func(g compressedGeneration) { compressed = append(compressed, g.Path) }); err != nil {
			t.Fatal(err)
		}
		return compressed
	}
	// Not while the next index must read everything again (a new index
	// version), nor after a capture that failed.
	if err := catalog.advanceSyncGeneration(t.Context(), "host-a", source.Name); err != nil {
		t.Fatal(err)
	}
	if got := compress(); slices.Contains(got, requests) {
		t.Fatal("compressed a snapshot the next index will read", got)
	}
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	failed := readCaptureManifest(t, config, source.Name)
	// The last capture failed on this database (and only on it).
	failed.LastRun.Errors = []captureError{{Path: filepath.Join(dir, "conversations", antigravityRoot+".db"), Error: "unreadable"}}
	if err := writeCaptureManifest(target.Dir, &failed); err != nil {
		t.Fatal(err)
	}
	if got := compress(); slices.Contains(got, requests) {
		t.Fatal("compressed a snapshot after a failed capture", got)
	}
	runCapture(t, config)
	compressed := compress()
	if !slices.Contains(compressed, requests) || slices.Contains(compressed, summaries) {
		t.Fatalf("compressed %v: want the idle requests database, not the summaries every plan reads", compressed)
	}
	manifest := readCaptureManifest(t, config, source.Name)
	var snapshot *capturedSnapshot
	for _, s := range manifest.Snapshots {
		if s.Captured == requests {
			snapshot = s
		}
	}
	if snapshot == nil || snapshot.Encoding != captureEncodingZstd || snapshot.SHA256 == "" || !zstdOnDisk(t, capturedPath(config, source.Name, requests)) || manifest.Version != captureSnapshotsVersion {
		t.Fatalf("idle snapshot: %+v", snapshot)
	}
	// A build that reads encoded files but not encoded snapshots (phase 2)
	// refuses the capture.
	captureManifestUnderstood = captureFilesVersion
	_, err := loadCaptureManifest(target.Dir)
	captureManifestUnderstood = captureManifestVersion
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("has version %d; this build understands up to %d", captureSnapshotsVersion, captureFilesVersion)) {
		t.Fatal("a files-level build accepted an encoded latest snapshot", err)
	}
	if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}

	var decodes atomic.Int64
	override(t, &capturedContentHook, func(string) { decodes.Add(1) })
	parses := countParses(t)
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	if decodes.Load() != 0 || parses.Load() != 0 {
		t.Fatalf("normal index over an idle compressed snapshot: %d decodes, %d parses", decodes.Load(), parses.Load())
	}
	forced, _ := testCatalog(t)
	if result := forced.IndexCapture(withIngestMode(t.Context(), ingestMode{force: true}), target, nil); result.Error != nil || decodes.Load() == 0 {
		t.Fatalf("forced reindex: %+v, %d decodes", result, decodes.Load())
	}
	messages := func(c *Catalog) string {
		t.Helper()
		rows, err := queryMaps(c.DB, "SELECT c.native_id,m.role,m.text,m.created_at FROM messages m JOIN conversations c ON c.id=m.conversation_id ORDER BY c.native_id,m.created_at,m.text")
		if err != nil {
			t.Fatal(err)
		}
		return jsonText(rows)
	}
	if messages(forced) != messages(catalog) {
		t.Fatal("forced reindex of a compressed snapshot built a different catalog")
	}
	if left := temporariesUnder(t, target.Dir); len(left) != 0 {
		t.Fatal("indexing left decoded copies", left)
	}

	// Pins and record exports read the original bytes.
	lock, err := lockCaptureTarget(t.Context(), target.Dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := sourceCaptureForTarget(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := c.pinVersion("job", "idle snapshot", snapshot.version())
	if err != nil || pin.Encoding != captureEncodingZstd || pin.SHA256 != snapshot.SHA256 || pin.Size != snapshot.Size {
		t.Fatal("pin of an idle compressed snapshot", pin, err)
	}
	ref, err := newRecordStore(c).exportSQLite(capturedPath(config, source.Name, requests), snapshot.Path, "database", snapshot.Source, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := newRecordStore(c).loadRevision(ref)
	if err != nil || revision.InputHash != snapshot.SHA256 || revision.Input != requests {
		t.Fatal("record export of an idle compressed snapshot", revision.Input, err)
	}
	// The pin links the encoded snapshot into history under a name the
	// manifest does not list: history backfill exports it from there.
	if ref, err = newRecordStore(c).exportSQLite(filepath.Join(target.Dir, filepath.FromSlash(pin.Path)), snapshot.Path, "database", snapshot.Source, "", nil); err != nil {
		t.Fatal("export of a pinned compressed snapshot", err)
	}
	if revision, err = newRecordStore(c).loadRevision(ref); err != nil || revision.InputHash != snapshot.SHA256 || revision.Input != pin.Path {
		t.Fatal("export of a pinned compressed snapshot", revision.Input, err)
	}
	releaseLockFile(lock)
	if err = releasePreservationOwner(target.Dir, "job"); err != nil {
		t.Fatal(err)
	}
	plan, err := planHistory(t.Context(), config, historyScope{Host: "host-a", Source: source.Name})
	if err != nil {
		t.Fatal(err)
	}
	planned, pinnedCopy := false, false
	for _, u := range plan.Units {
		if u.Path == requests {
			planned = u.Gap == "" && u.Version.Encoding == captureEncodingZstd && u.Version.SHA256 == snapshot.SHA256
		}
		// The pin's copy in history is listed, encoded, after its release:
		// planned as a generation, never as an unrecognised orphan.
		if u.Path == pin.Path {
			pinnedCopy = u.Kind == "database" && u.Gap == "" && u.Version.Encoding == captureEncodingZstd && u.Version.SHA256 == snapshot.SHA256
		}
		if u.Kind == "orphan-native" {
			t.Fatal("history planning found an orphan", u)
		}
	}
	if !planned || !pinnedCopy {
		t.Fatal("history planning did not read the idle compressed snapshot and its pinned copy", planned, pinnedCopy)
	}
	if listed, ok := loadManifestForTest(t, target.Dir).version(pin.Path); !ok || listed.Encoding != captureEncodingZstd {
		t.Fatal("the pinned copy is not listed in the manifest", listed)
	}

	// Turning on SQLite record capture, which exports every database on every
	// capture, restores the compressed snapshot to its original bytes once;
	// later captures decode nothing.
	config.Sources[0].Options = map[string]any{"sqlite_record_capture": true}
	decodes.Store(0)
	if run := runCapture(t, config); !run.OK || decodes.Load() == 0 {
		t.Fatal("record capture did not restore the compressed snapshot", run)
	}
	restored := readCaptureManifest(t, config, source.Name)
	for _, s := range restored.Snapshots {
		if s.Captured == requests && (s.Encoding != "" || zstdOnDisk(t, capturedPath(config, source.Name, requests))) {
			t.Fatalf("record capture left the snapshot compressed: %+v", s)
		}
	}
	decodes.Store(0)
	if run := runCapture(t, config); !run.OK || decodes.Load() != 0 {
		t.Fatalf("record capture decoded again: %d decodes, %+v", decodes.Load(), run)
	}
	if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}
	// Turned off again, the idle snapshot is compressed again.
	config.Sources[0].Options = nil
	runCapture(t, config)
	runCapture(t, config)
	if got := compress(); !slices.Contains(got, requests) {
		t.Fatal("idle snapshot not compressed again after record capture was turned off", got)
	}

	// The source changes again: a new uncompressed snapshot, and the
	// compressed one moves into history as it is.
	antigravityRequestRows(t, dir, antigravityRoot, []any{[]uint64{1}, "gemini-3.8-flash", 1000, 999, 0, 200})
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	manifest = readCaptureManifest(t, config, source.Name)
	for _, s := range manifest.Snapshots {
		if s.Captured != requests {
			continue
		}
		if s.Encoding != "" || len(s.Generations) == 0 || s.Generations[0].Encoding != captureEncodingZstd || s.Generations[0].SHA256 != snapshot.SHA256 {
			t.Fatalf("after a change at the source: %+v", s)
		}
	}
	if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}
}
