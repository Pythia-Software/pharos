package archive

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// zedSecret stands for whatever a later release keeps beside the threads; no
// capture may hold it.
const zedSecret = "zed-secret-do-not-capture"

const (
	zedThreadID   = "d0000000-0000-4000-8000-000000000001"
	zedSubagentID = "d0000000-0000-4000-8000-000000000002"
	zedLegacyID   = "d0000000-0000-4000-8000-000000000003"
	zedPromptID   = "e0000000-0000-4000-8000-000000000001"
	zedFollowUpID = "e0000000-0000-4000-8000-000000000002"
)

// zedRow is a threads row.
type zedRow struct {
	id, parent, summary, updated, dataType, folders string
	data                                            map[string]any
}

// zedRows are the threads in the shapes Zed writes: its agent's thread,
// compressed, with two turns (thinking, a failed command, an edit, a
// subagent, a redacted thought, each turn's usage and the thread's sum); the
// subagent's thread, uncompressed; and a thread of releases before the
// agent, whose messages have roles.
func zedRows() []zedRow {
	text := func(value string) map[string]any { return map[string]any{"Text": value} }
	use := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"ToolUse": map[string]any{"id": id, "name": name, "raw_input": jsonText(input), "input": input, "is_input_complete": true}}
	}
	result := func(id, name, content string, failed bool) map[string]any {
		return map[string]any{"tool_use_id": id, "tool_name": name, "is_error": failed, "content": map[string]any{"Text": content}}
	}
	// A release writes a result's content as a list.
	listed := func(result map[string]any) map[string]any {
		result["content"] = []any{result["content"]}
		return result
	}
	usage := func(input, output, read, created int) map[string]any {
		return map[string]any{"input_tokens": input, "output_tokens": output, "cache_read_input_tokens": read, "cache_creation_input_tokens": created}
	}
	thread := map[string]any{
		"title": "Fix the build",
		"messages": []any{
			map[string]any{"User": map[string]any{"id": zedPromptID, "content": []any{text("Fix the build in [@main.go](file:///work/app/main.go)"),
				map[string]any{"Mention": map[string]any{"uri": map[string]any{"File": map[string]any{"abs_path": "/work/app/main.go"}}, "content": "package main"}}}}},
			map[string]any{"Agent": map[string]any{
				"content": []any{
					map[string]any{"Thinking": map[string]any{"text": "Run make first.", "signature": "sig"}},
					text("Running make."),
					use("call1", "terminal", map[string]any{"command": "make", "cd": "app"}),
					// A release tags a call's input with its type.
					use("call2", "edit_file", map[string]any{"type": "json", "value": map[string]any{"path": "app/main.go"}}),
					use("call3", "spawn_agent", map[string]any{"prompt": "Check the tests"}),
				},
				"tool_results": map[string]any{
					"call1": listed(result("call1", "terminal", "Command \"make\" failed with exit code 2.\n\n```\nmain.go:3: error\n```", false)),
					"call2": result("call2", "edit_file", "Edited app/main.go", false),
					"call3": result("call3", "spawn_agent", "Tests pass.", false),
				},
			}},
			"Resume",
			map[string]any{"Agent": map[string]any{"content": []any{map[string]any{"RedactedThinking": "opaque"}, text("Fixed.")}, "tool_results": map[string]any{}}},
			map[string]any{"User": map[string]any{"id": zedFollowUpID, "content": []any{text("Now the docs")}}},
			map[string]any{"Agent": map[string]any{"content": []any{use("call4", "terminal", map[string]any{"command": "ls docs"})},
				"tool_results": map[string]any{"call4": result("call4", "terminal", "```\nREADME.md\n```", false)}}},
			map[string]any{"Agent": map[string]any{"content": []any{text("Done.")}, "tool_results": map[string]any{}}},
			// Compacting by hand adds an empty prompt, then the summary.
			map[string]any{"User": map[string]any{"id": "e0000000-0000-4000-8000-000000000009", "content": []any{}}},
			map[string]any{"Compaction": map[string]any{"Summary": "Earlier work summary"}},
		},
		"updated_at":               "2026-11-04T16:20:00Z",
		"model":                    map[string]any{"provider": "anthropic", "model": "claude-sonnet-4-5"},
		"profile":                  "write",
		"request_token_usage":      map[string]any{zedPromptID: usage(100, 50, 10, 5), zedFollowUpID: usage(80, 20, 0, 0)},
		"cumulative_token_usage":   usage(300, 120, 30, 5),
		"initial_project_snapshot": map[string]any{"worktree_snapshots": []any{map[string]any{"worktree_path": "/work/app"}}},
		"version":                  "0.3.0",
	}
	subagent := map[string]any{
		"title": "Check the tests",
		"messages": []any{
			map[string]any{"User": map[string]any{"id": "e0000000-0000-4000-8000-000000000003", "content": []any{text("Check the tests")}}},
			map[string]any{"Agent": map[string]any{"content": []any{text("Tests pass.")}, "tool_results": map[string]any{}}},
		},
		"updated_at":       "2026-11-04T16:19:00Z",
		"subagent_context": map[string]any{"parent_thread_id": zedThreadID},
		"version":          "0.3.0",
	}
	legacy := map[string]any{
		"version": "0.2.0", "summary": "Old question",
		"messages": []any{
			map[string]any{"id": 0, "role": "user", "segments": []any{map[string]any{"type": "text", "text": "What does a.go do?"}}, "tool_uses": []any{}, "tool_results": []any{}},
			map[string]any{"id": 1, "role": "assistant", "segments": []any{map[string]any{"type": "thinking", "text": "Read it.", "signature": nil}, map[string]any{"type": "RedactedThinking", "data": "opaque"},
				map[string]any{"type": "text", "text": "It parses flags."}},
				"tool_uses":    []any{map[string]any{"id": "old1", "name": "read_file", "input": map[string]any{"path": "a.go"}}},
				"tool_results": []any{map[string]any{"tool_use_id": "old1", "is_error": false, "content": "package a"}}},
			// The oldest threads keep a message's text whole.
			map[string]any{"id": 2, "role": "assistant", "text": "It also reads env."},
		},
		"request_token_usage":    []any{map[string]any{}, usage(40, 10, 0, 0)},
		"cumulative_token_usage": usage(40, 10, 0, 0),
	}
	return []zedRow{
		{id: zedThreadID, summary: "Fix the build", updated: "2026-11-04T16:20:00.000000+00:00", dataType: "zstd", folders: "/work/app", data: thread},
		{id: zedSubagentID, parent: zedThreadID, summary: "Check the tests", updated: "2026-11-04T16:19:00.000000+00:00", dataType: "json", folders: "/work/app", data: subagent},
		{id: zedLegacyID, summary: "Old question", updated: "2025-06-01T09:00:00.000000+00:00", dataType: "json", data: legacy},
	}
}

// zedPut writes threads rows: data compressed when the row says so.
func zedPut(t *testing.T, db *sql.DB, rows ...zedRow) {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	for _, row := range rows {
		data := []byte(jsonText(row.data))
		if row.dataType == "zstd" {
			data = encoder.EncodeAll(data, nil)
		}
		var parent any
		if row.parent != "" {
			parent = row.parent
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO threads(id, summary, updated_at, data_type, data, parent_id, folder_paths, folder_paths_order, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			row.id, row.summary, row.updated, row.dataType, data, parent, row.folders, "0", "2026-11-04T16:17:00.000000+00:00"); err != nil {
			t.Fatal(err)
		}
	}
}

// zedFixture writes Zed's data folder: the threads database, with a table a
// later release might add beside it, and Zed's settings.
func zedFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Zed")
	if err := os.MkdirAll(filepath.Join(root, "threads"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, filepath.FromSlash(zedDatabase)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, summary TEXT NOT NULL, updated_at TEXT NOT NULL, data_type TEXT NOT NULL, data BLOB NOT NULL,
		parent_id TEXT, folder_paths TEXT, folder_paths_order TEXT, created_at TEXT);
		CREATE TABLE credentials (key TEXT, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO credentials VALUES('token', ?)`, zedSecret); err != nil {
		t.Fatal(err)
	}
	zedPut(t, db, zedRows()...)
	writeSourceFile(t, filepath.Join(root, "settings.json"), `{"token":"`+zedSecret+`"}`)
	return root
}

func zedSource(root string) SourceConfig {
	return SourceConfig{Name: "zed", Kind: "zed", Path: root, Account: "local", Enabled: true}
}

func zedRecords(t *testing.T, root string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(zedSource(root))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

// zedWrite changes the fixture's threads database.
func zedWrite(t *testing.T, root string, write func(db *sql.DB) error) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, filepath.FromSlash(zedDatabase)))
	if err == nil {
		err = write(db)
		db.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestZedThreadKeepsItsTurnsToolsAndSubagents(t *testing.T) {
	records := zedRecords(t, zedFixture(t))
	if len(records) != 2 {
		t.Fatalf("records = %v", records)
	}
	record := records[zedThreadID]
	if record.SourceKind != "zed" || record.Title != "Fix the build" || record.Location != "/work/app" || record.Outcome != "Done." || record.Metadata["profile"] != "write" || len(record.Conversations) != 2 {
		t.Fatalf("record = %+v", record)
	}
	thread := record.Conversations[0]
	if thread.NativeID != zedThreadID || thread.Provider != "zed" || thread.Harness != "zed" || thread.Model != "claude-sonnet-4-5" || thread.NativeContext["model_provider"] != "anthropic" ||
		thread.StartedAt != "2026-11-04T16:17:00.000Z" || thread.EndedAt != "2026-11-04T16:20:00.000Z" {
		t.Fatalf("thread = %+v", thread)
	}
	// What a mention brought in is not the prompt; "Resume" is not a turn.
	want := "user:message assistant:reasoning assistant:message assistant:tool_call tool:tool_result assistant:tool_call tool:tool_result assistant:delegation tool:delegation_result assistant:metadata assistant:message system:metadata " +
		"user:message assistant:tool_call tool:tool_result assistant:message system:metadata system:metadata system:metadata"
	if got := cursorKinds(thread.Messages); got != want {
		t.Fatalf("kinds = %s\nwant    %s", got, want)
	}
	messages := thread.Messages
	if messages[0].Text != "Fix the build in [@main.go](file:///work/app/main.go)" || messages[0].NativeID != zedPromptID {
		t.Fatalf("prompt = %+v", messages[0])
	}
	failed, edited, listed := mapValueDefault(parseJSONValue(messages[4].Text)), mapValueDefault(parseJSONValue(messages[6].Text)), mapValueDefault(parseJSONValue(messages[14].Text))
	if jsonText(failed["details"]) != `{"exit_code":2}` || messages[4].CallID != "call1" || failed["tool"] != "terminal" {
		t.Fatalf("failed command = %s", messages[4].Text)
	}
	if edited["details"] != nil || jsonText(listed["details"]) != `{"exit_code":0}` || !strings.Contains(messages[5].Text, `"input":{"path":"app/main.go"}`) {
		t.Fatalf("edit = %s, listing = %s", messages[6].Text, messages[14].Text)
	}
	// Each turn's last request, then what the thread's sum adds.
	usage := func(message MessageRecord) string {
		return jsonText(mapValueDefault(parseJSONValue(message.Text))["usage"])
	}
	if usage(messages[11]) != `{"cache_creation_input_tokens":5,"cache_read_input_tokens":10,"input_tokens":100,"output_tokens":50}` ||
		usage(messages[16]) != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":80,"output_tokens":20}` ||
		usage(messages[18]) != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":20,"input_tokens":120,"output_tokens":50}` {
		t.Fatalf("usage = %s / %s / %s", messages[11].Text, messages[16].Text, messages[18].Text)
	}
	// A compaction is a boundary that keeps its summary.
	if compaction := mapValueDefault(parseJSONValue(messages[17].Text)); !isCompactionMarker(messages[17]) || compaction["summary"] != "Earlier work summary" {
		t.Fatalf("compaction = %+v", messages[17])
	}
	// The subagent's prompt is its parent's.
	subagent := record.Conversations[1]
	if subagent.NativeID != zedSubagentID || subagent.ParentNativeID != zedThreadID || subagent.AgentDepth != 1 || subagent.Messages[0].Role != agentRole {
		t.Fatalf("subagent = %+v", subagent)
	}
	// A thread of releases before the agent.
	legacy := records[zedLegacyID]
	if got := cursorKinds(legacy.Conversations[0].Messages); legacy.Title != "Old question" || got != "user:message assistant:reasoning assistant:metadata assistant:message assistant:tool_call tool:tool_result system:metadata assistant:message" {
		t.Fatalf("legacy thread = %s: %s", legacy.Title, got)
	}
	// Release 0.1.0 keeps a call's results on the user message after it.
	older := (&zedAdapter{}).conversation("zed", "older", map[string]any{}, map[string]any{"version": "0.1.0", "messages": []any{
		map[string]any{"id": 0, "role": "user", "segments": []any{map[string]any{"type": "text", "text": "Read a.go"}}},
		map[string]any{"id": 1, "role": "assistant", "segments": []any{}, "tool_uses": []any{map[string]any{"id": "old2", "name": "read_file", "input": map[string]any{"path": "a.go"}}}},
		map[string]any{"id": 2, "role": "user", "segments": []any{map[string]any{"type": "text", "text": ""}}, "tool_results": []any{map[string]any{"tool_use_id": "old2", "is_error": false, "content": "package a"}}},
		map[string]any{"id": 3, "role": "assistant", "segments": []any{map[string]any{"type": "text", "text": "It is package a."}}},
	}})
	if got := cursorKinds(older.Messages); got != "user:message assistant:tool_call tool:tool_result assistant:message" || !strings.Contains(older.Messages[2].Text, "package a") {
		t.Fatalf("0.1.0 thread = %s: %+v", got, older.Messages)
	}
	// A provider can give two calls the same ID: each result is its own
	// message's, and a call left without one has none.
	read := func(id int, path, content string) map[string]any {
		message := map[string]any{"id": id, "role": "assistant", "segments": []any{}, "tool_uses": []any{map[string]any{"id": "call_1", "name": "read_file", "input": map[string]any{"path": path}}}}
		if content != "" {
			message["tool_results"] = []any{map[string]any{"tool_use_id": "call_1", "is_error": false, "content": content}}
		}
		return message
	}
	reused := (&zedAdapter{}).conversation("zed", "reused", map[string]any{}, map[string]any{"version": "0.2.0", "messages": []any{read(0, "a.go", "package a"), read(1, "b.go", ""), read(2, "c.go", "package c")}})
	if got := cursorKinds(reused.Messages); got != "assistant:tool_call tool:tool_result assistant:tool_call assistant:tool_call tool:tool_result" ||
		!strings.Contains(reused.Messages[1].Text, "package a") || !strings.Contains(reused.Messages[4].Text, "package c") {
		t.Fatalf("reused IDs = %s: %+v", got, reused.Messages)
	}
	// A command's result that isn't text says nothing of how it ended.
	var unread MessageRecord
	(&zedAdapter{}).tool(MessageRecord{NativeID: "call5"}, "call5", "terminal", nil, map[string]any{"content": map[string]any{"Image": map[string]any{}}}, func(message MessageRecord) { unread = message })
	if strings.Contains(unread.Text, "exit_code") {
		t.Fatalf("a result without text = %s", unread.Text)
	}
	// Only Zed's own tags are taken off an input.
	if input := jsonText(zedToolInput(map[string]any{"type": "integer", "value": 7})); input != `{"type":"integer","value":7}` {
		t.Fatalf("an untagged input = %s", input)
	}
}

// A terminal command's outcome is the header Zed writes before its output,
// which Zed saves as a result that did not fail; the output may say anything.
func TestZedTerminalOutcomeIsZedsHeader(t *testing.T) {
	for _, test := range []struct {
		name, content, status, errorType string
		exit                             int64
		input                            map[string]any
	}{
		{"failed", "Command \"make\" failed with exit code 2.\n\n```\nmain.go:3: error\n```", "error", "", 2, nil},
		{"failed with a note", "Note: this command ran WITHOUT an OS sandbox.\n\nCommand \"make\" failed with exit code 1.", "error", "", 1, nil},
		{"output quoting a failure", "```\nCommand \"make\" failed with exit code 2.\n```", "ok", "", 0, nil},
		{"output after a note", "Note: this command ran WITHOUT an OS sandbox because one could not be created (setup timed out. bwrap failed with exit code 1.).\n\n```\nCommand \"make\" failed with exit code 2.\n```", "ok", "", 0, nil},
		{"a fence in the command", "Command \"cat <<'EOF'\n```\nEOF\nexit 7\" failed with exit code 7.\n\n```\n```\n```", "error", "", 7, map[string]any{"command": "cat <<'EOF'\n```\nEOF\nexit 7"}},
		{"failed, its command unknown", "Command \"make\" failed with exit code 2.\n\n```\nmain.go:3: error\n```", "error", "", 2, map[string]any{}},
		{"timed out, its command unknown", "Command \"make\" timed out. No output was captured.", "error", "timeout", -1, map[string]any{}},
		{"no output", "Command executed successfully.", "ok", "", 0, nil},
		{"timed out", "Command \"sleep 9\" timed out. Output captured before timeout:\n\n```\nwaiting: permission denied\n```", "error", "timeout", -1, map[string]any{"command": "sleep 9", "timeout_ms": 9000}},
		{"timed out without a limit", "Command \"sleep 9\" timed out. No output was captured.", "error", "timeout", -1, map[string]any{"command": "sleep 9"}},
		{"stopped after a note", "Note: this command ran WITHOUT an OS sandbox.\n\nThe user stopped this command. No output was captured before stopping.", "error", "interrupted", -1, nil},
		{"stopped", "The user stopped this command. No output was captured before stopping.\n\nSince the user intentionally interrupted this command, ask them what they would like to do next.", "error", "interrupted", -1, nil},
		{"terminated", "Command terminated unexpectedly. Output captured:\n\n```\nhalf\n```", "error", "", -1, nil},
		{"an older release's failure", "Command failed or was interrupted.\nPartial output captured:\n\n```\nhalf\n```", "error", "", -1, nil},
	} {
		messages := []MessageRecord{}
		add := func(message MessageRecord) { messages = append(messages, message) }
		input := test.input
		if input == nil {
			input = map[string]any{"command": "make", "timeout_ms": 9000}
		}
		(&zedAdapter{}).tool(MessageRecord{NativeID: "call"}, "call", "terminal", input,
			map[string]any{"tool_name": "terminal", "is_error": false, "content": test.content}, add)
		_, calls := buildToolLedger(messages, "test")
		if len(calls) != 1 {
			t.Fatalf("%s: calls = %+v", test.name, calls)
		}
		call := calls[0]
		exit := int64(-1)
		if call.ExitCode != nil {
			exit = *call.ExitCode
		}
		if call.Status != test.status || exit != test.exit || (test.errorType != "" && call.ErrorType != test.errorType) {
			t.Errorf("%s: status %s, exit %d, error %s (%s)", test.name, call.Status, exit, call.ErrorType, messages[1].Text)
		}
	}
}

// A thread's usage is its sum of every request, once.
func TestZedUsageCountsTheThreadOnce(t *testing.T) {
	c, _ := testCatalog(t)
	ingestSource(t, c, zedSource(zedFixture(t)))
	var output, read int
	if err := c.DB.QueryRow(`SELECT COALESCE(SUM(u.output_tokens),0), COALESCE(SUM(u.cache_read_input_tokens),0) FROM agent_session_usage u
		JOIN agent_sessions s ON s.id=u.agent_session_id JOIN conversations v ON v.id=s.conversation_id WHERE v.native_id=?`, zedThreadID).Scan(&output, &read); err != nil {
		t.Fatal(err)
	}
	if output != 120 || read != 30 {
		t.Fatalf("output %d, cache reads %d", output, read)
	}
}

func TestZedReadsOnlyTheThreadThatChanged(t *testing.T) {
	c, _ := testCatalog(t)
	root := zedFixture(t)
	source := zedSource(root)
	ingestSource(t, c, source)
	if parsed := ingestSource(t, c, source).Parsed; parsed != 0 {
		t.Fatalf("unchanged: %d threads parsed", parsed)
	}
	// Zed saves a thread whole, with the time it saved it.
	legacy := zedRows()[2]
	legacy.updated = "2025-06-02T09:00:00.000000+00:00"
	legacy.data["messages"] = append(legacy.data["messages"].([]any), map[string]any{"id": 3, "role": "user", "segments": []any{map[string]any{"type": "text", "text": "And b.go?"}}})
	zedWrite(t, root, func(db *sql.DB) error { zedPut(t, db, legacy); return nil })
	parsed := ingestSource(t, c, source).Parsed
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='And b.go?'`); n != 1 || parsed != 1 {
		t.Fatalf("after a new message: %d copies, %d groups parsed", n, parsed)
	}
	// A rename keeps the thread's time, and here its size.
	zedWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE threads SET summary='Old answers?' WHERE id=?`, zedLegacyID)
		return err
	})
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM workspaces WHERE source_kind='zed' AND title='Old answers?'`); n != 1 {
		t.Fatal("a renamed thread kept its title")
	}
}

// A thread that cannot be decoded is reported, and the others are read.
func TestZedDamagedThreadLeavesTheOthers(t *testing.T) {
	root := zedFixture(t)
	damage := func(id string) {
		zedWrite(t, root, func(db *sql.DB) error {
			_, err := db.Exec(`UPDATE threads SET data=X'28B52FFD00' WHERE id=?`, id)
			return err
		})
	}
	damage(zedThreadID)
	adapter, err := MakeAdapter(zedSource(root))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string][]string{}
	err = adapter.Discover(func(record WorkspaceRecord) error {
		for _, conversation := range record.Conversations {
			records[record.SourceID] = append(records[record.SourceID], conversation.NativeID)
		}
		return nil
	})
	// Its subagent is still read, in its workspace.
	if err == nil || len(records[zedLegacyID]) != 1 || !slices.Equal(records[zedThreadID], []string{zedSubagentID}) {
		t.Fatalf("read %v, error %v", records, err)
	}

	// A subagent that can't be read leaves the thread that started it, and
	// the one it started, at its depth, and is reported each time until it
	// can be read.
	root = zedFixture(t)
	grandchild := zedRows()[1]
	grandchild.id, grandchild.parent = "d0000000-0000-4000-8000-000000000009", zedSubagentID
	zedWrite(t, root, func(db *sql.DB) error { zedPut(t, db, grandchild); return nil })
	damage(zedSubagentID)
	c, _ := testCatalog(t)
	indexed := func(id string) int {
		return countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE provider='zed' AND native_id=?`, id)
	}
	for range 2 {
		adapter, err := MakeAdapter(zedSource(root))
		if err != nil {
			t.Fatal(err)
		}
		if result := c.Ingest(adapter, nil); result.Error == nil || indexed(zedThreadID) != 1 || indexed(zedSubagentID) != 0 {
			t.Fatalf("with its subagent damaged: error %v, thread %d, subagent %d", result.Error, indexed(zedThreadID), indexed(zedSubagentID))
		}
	}
	if depth := countRows(t, c, `SELECT agent_depth FROM conversations WHERE native_id=?`, grandchild.id); depth != 2 {
		t.Fatalf("the subagent's own subagent is at depth %d", depth)
	}
}

func TestZedCaptureHoldsOnlyTheThreads(t *testing.T) {
	useHost(t, "host-a")
	root := zedFixture(t)
	source := zedSource(root)
	plan, err := planCapture(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range append(plan.databases, plan.files...) {
		got = append(got, item.rel)
	}
	if strings.Join(got, ",") != zedDatabase {
		t.Fatalf("capture plan = %v", got)
	}
	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	stopped := 0
	override(t, &captureFault, func(stage, rel string) error {
		if stage != "snapshot-written" {
			return nil
		}
		stopped++
		if strings.Contains(capturedText(t, config.CaptureRoot), zedSecret) {
			t.Errorf("a capture stopped at %s holds what Zed keeps beside its threads", rel)
		}
		return errors.New("stopped")
	})
	runCapture(t, config)
	captureFault = nil
	if stopped == 0 {
		t.Fatal("no snapshot was written")
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if strings.Contains(capturedText(t, config.CaptureRoot), zedSecret) {
		t.Fatal("the capture holds what Zed keeps beside its threads")
	}
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='zed'`)
	}
	live := messages(c)
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live || live == 0 {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}

	// With rows exported from each snapshot as well, and copied before
	// indexing: the two read the same, so the capture indexes on its own
	// host.
	useHost(t, "host-a")
	recorded, recordedConfig := testCatalog(t)
	source.Options = map[string]any{"sqlite_record_capture": true}
	recordedConfig.Sources = []SourceConfig{source}
	ingestSource(t, recorded, source)
	if result := runCapture(t, recordedConfig); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	ingestSource(t, recorded, source)
	indexCaptures(t, recorded, recordedConfig.CaptureRoot, "host-a")
	if strings.Contains(capturedText(t, recordedConfig.CaptureRoot), zedSecret) {
		t.Fatal("the rows exported from a capture hold what Zed keeps beside its threads")
	}
	useHost(t, "host-b")
	exported, _ := testCatalog(t)
	indexCaptures(t, exported, recordedConfig.CaptureRoot, "host-a")
	if got := messages(exported); got != live {
		t.Fatalf("from a capture with rows exported: %d messages, live %d", got, live)
	}
}

// A thread deleted in Zed before its last capture was indexed is indexed from
// an older capture, and one indexed newer is not rolled back.
func TestZedThreadDeletedBeforeIndexingIsKept(t *testing.T) {
	useHost(t, "host-a")
	root := zedFixture(t)
	source := zedSource(root)
	config := captureTestConfig(t, source)
	runCapture(t, config)
	zedWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM threads WHERE id IN (?, ?)`, zedLegacyID, zedSubagentID)
		return err
	})
	runCapture(t, config)
	useHost(t, "host-b")
	c, _ := testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	live, _ := testCatalog(t)
	ingestSource(t, live, zedSource(zedFixture(t)))
	if got, want := cursorShape(t, c), cursorShape(t, live); got != want {
		t.Fatalf("salvaged %s, read live %s", got, want)
	}
	if n := countRows(t, c, `SELECT COUNT(DISTINCT workspace_id) FROM conversations WHERE native_id IN (?, ?)`, zedThreadID, zedSubagentID); n != 1 {
		t.Fatalf("the subagent left its thread's workspace: %d", n)
	}

	useHost(t, "host-a")
	root = zedFixture(t)
	source = zedSource(root)
	config = captureTestConfig(t, source)
	runCapture(t, config)
	legacy := zedRows()[2]
	legacy.updated = "2025-06-02T09:00:00.000000+00:00"
	legacy.data["messages"] = append(legacy.data["messages"].([]any), map[string]any{"id": 3, "role": "user", "segments": []any{map[string]any{"type": "text", "text": "And b.go?"}}})
	zedWrite(t, root, func(db *sql.DB) error { zedPut(t, db, legacy); return nil })
	c, _ = testCatalog(t)
	ingestSource(t, c, source)
	zedWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM threads WHERE id=?`, zedLegacyID)
		return err
	})
	runCapture(t, config)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='And b.go?'`); n != 1 {
		t.Fatalf("the thread was rolled back: %d", n)
	}
}

// A subagent whose thread Zed deleted stays in that thread's workspace, which
// keeps its name.
func TestZedSubagentKeepsItsDeletedThreadsWork(t *testing.T) {
	c, _ := testCatalog(t)
	root := zedFixture(t)
	source := zedSource(root)
	ingestSource(t, c, source)
	var workspace string
	if err := c.DB.QueryRow(`SELECT workspace_id FROM conversations WHERE native_id=?`, zedSubagentID).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	zedWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`DELETE FROM threads WHERE id=?`, zedThreadID)
		return err
	})
	if record := zedRecords(t, root)[zedThreadID]; record.Title != "Zed "+short(zedThreadID) || record.Purpose != "" || len(record.Conversations) != 1 {
		t.Fatalf("without its thread: %+v", record)
	}
	// The subagent goes on.
	subagent := zedRows()[1]
	subagent.updated = "2026-11-04T16:30:00.000000+00:00"
	subagent.data["messages"] = append(subagent.data["messages"].([]any), map[string]any{"Agent": map[string]any{"content": []any{map[string]any{"Text": "And the docs build."}}, "tool_results": map[string]any{}}})
	zedWrite(t, root, func(db *sql.DB) error { zedPut(t, db, subagent); return nil })
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE native_id=? AND workspace_id=?`, zedSubagentID, workspace); n != 1 {
		t.Fatal("the subagent left its thread's workspace")
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM messages WHERE text='And the docs build.'`); n != 1 {
		t.Fatal("the subagent was not read again")
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM workspace_names WHERE workspace_id=? AND source_title='Fix the build'`, workspace); n != 1 {
		t.Fatal("the work lost its name")
	}
}

func TestZedThreadIsVerifiedLive(t *testing.T) {
	c, _ := testCatalog(t)
	source := zedSource(zedFixture(t))
	ingestSource(t, c, source)
	if result := c.auditUnit(t.Context(), firstSyncUnit(t, c, source), "manual", 0); result.Outcome != "passed" {
		t.Fatalf("verification: %+v", result)
	}
}

// Zed is found and counted by the probe, offered to a Mac set up before
// Pharos read it, and chosen in guided setup; a data folder without threads
// is not offered.
func TestZedIsFoundAndSetUp(t *testing.T) {
	home := probeHome(t)
	useHost(t, "host-a")
	probePut(t, filepath.Join(home, ".claude", "projects", "-r", probeID1+".jsonl"), "{}\n")
	probePut(t, filepath.Join(home, filepath.FromSlash(zedUserDir), "settings.json"), "{}")
	for _, candidate := range probeSources(home, Config{}, nil).Candidates {
		if candidate.Kind == "zed" {
			t.Fatalf("a folder without threads was offered: %+v", candidate)
		}
	}
	if tools := probeTools(home, Config{}); strings.Contains(jsonText(tools), `"zed"`) {
		t.Fatalf("a folder without threads was found: %v", tools)
	}
	fixture := zedFixture(t)
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(zedUserDir), "threads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(fixture, filepath.FromSlash(zedDatabase)), filepath.Join(home, filepath.FromSlash(zedUserDir), filepath.FromSlash(zedDatabase))); err != nil {
		t.Fatal(err)
	}
	candidate := candidateNamed(t, probeSources(home, Config{}, nil), "zed")
	if candidate.Kind != "zed" || candidate.Status != "found" || candidate.Count != 3 || candidate.Unit != "session" || !candidate.Recommended {
		t.Fatalf("candidate = %+v", candidate)
	}
	server := probeServer(t, probeLibrary(t, ""))
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"accept":["claude"]}`); code != 200 {
		t.Fatalf("accept = %d %v", code, result)
	}
	if _, status := probeCall(t, server, http.MethodGet, "/api/probe/status", ""); status["new_sources"] != float64(1) {
		t.Fatalf("status of a Mac set up before Zed = %v", status)
	}
	if code, result := probeCall(t, server, http.MethodPost, "/api/probe/accept", `{"kinds":["zed"],"accept":["zed"]}`); code != 200 || jsonText(result["added"]) != `["zed"]` {
		t.Fatalf("accept Zed = %d %v", code, result)
	}
}
