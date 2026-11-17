package archive

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// conductorEventLogDDL is Conductor's session_events table and indexes as its
// migrations 151 and 152 create them.
var conductorEventLogDDL = []string{
	"CREATE TABLE session_events (session_id TEXT, session_index INTEGER, event_id TEXT, event_type TEXT, content TEXT, received_at TEXT, reverted_at TEXT)",
	"CREATE UNIQUE INDEX idx_session_events_session_index ON session_events (session_id, session_index)",
	"CREATE UNIQUE INDEX idx_session_events_event_id ON session_events (session_id, event_id)",
}

type conductorTestEvent struct {
	id, kind, at, reverted string
	content                map[string]any
}

func conductorAgentEvent(id, at string, payload map[string]any) conductorTestEvent {
	return conductorTestEvent{id: id, kind: "agent", at: at, content: map[string]any{"eventId": id, "type": "agent", "rawPayload": payload, "turnId": "turn-1"}}
}

func conductorUserEvent(id, messageID, text, state, at string) conductorTestEvent {
	return conductorTestEvent{id: id, kind: "userMessage", at: at, content: map[string]any{"eventId": id, "type": "userMessage", "senderId": "local", "id": messageID, "message": text, "state": state, "turnId": "turn-1", "config": map[string]any{"model": "opus"}}}
}

// conductorAgentSentEvent is a user message another agent session sent
// through Conductor's control API, with the harness's instructions first.
func conductorAgentSentEvent(id, messageID, text, at string) conductorTestEvent {
	event := conductorUserEvent(id, messageID, text, "sent", at)
	event.content["senderSessionId"] = "parent-session"
	return event
}

func insertConductorEvents(t *testing.T, db *sql.DB, sid string, first int, events ...conductorTestEvent) {
	t.Helper()
	tx := seedTx(t, db)
	for index, event := range events {
		var reverted any
		if event.reverted != "" {
			reverted = event.reverted
		}
		if _, err := tx.Exec("INSERT INTO session_events VALUES (?,?,?,?,?,?,?)", sid, first+index, event.id, event.kind, jsonText(event.content), event.at, reverted); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func claudeAssistant(uuid, text string) map[string]any {
	return map[string]any{"type": "assistant", "uuid": uuid, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}
}

// conductorEventLogFixture writes a Conductor database in today's layout: a
// Claude session whose rows project some of its events, and a Codex session.
// Each stored row is one Conductor's client would write for its event.
func conductorEventLogFixture(t *testing.T, dir string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openWritableSQLite(t, filepath.Join(dir, "conductor.db"))
	execSQL(t, db, append([]string{
		"CREATE TABLE sessions (id TEXT PRIMARY KEY, status TEXT, created_at TEXT, updated_at TEXT, title TEXT, model TEXT, agent_type TEXT, closed_at TEXT)",
		"CREATE TABLE session_messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, created_at TEXT, sent_at TEXT, full_message TEXT, cancelled_at TEXT, model TEXT, turn_id TEXT, sender_id TEXT)",
		"CREATE INDEX idx_session_messages_sent_at ON session_messages(session_id, sent_at)",
		"INSERT INTO sessions VALUES ('claude-1','idle','2026-11-16T10:00:00.000Z','2026-11-16T10:05:00.000Z','Event log','opus','claude',NULL)",
		"INSERT INTO sessions VALUES ('codex-1','idle','2026-11-16T11:00:00.000Z','2026-11-16T11:05:00.000Z','Codex events','gpt','codex',NULL)",
	}, conductorEventLogDDL...)...)
	rows := [][]any{
		{"u1", "claude-1", "user", "Tidy the formatter", "2026-11-16T10:00:00.000Z", "opus"},
		{"cloud-feed:claude-1:e-reply:0", "claude-1", "assistant", jsonText(claudeAssistant("uuid-reply", "Looking at it.")), "2026-11-16T10:00:05.000Z", nil},
		// A projected payload with no UUID: only the deterministic ID ties
		// the row to its event.
		{"cloud-feed:claude-1:e-done:0", "claude-1", "assistant", jsonText(map[string]any{"type": "result", "subtype": "success", "result": "Done."}), "2026-11-16T10:00:50.000Z", nil},
		// A row stored under an ID other than the event's (Conductor's local
		// abort and pre-log rows): its payload UUID still names the event.
		{"local-copy", "claude-1", "assistant", jsonText(claudeAssistant("uuid-copied", "Copied elsewhere.")), "2026-11-16T10:00:40.000Z", nil},
		{"cu1", "codex-1", "user", "Check codex", "2026-11-16T11:00:00.000Z", "gpt"},
		{"cloud-feed:codex-1:x-done:0", "codex-1", "assistant", jsonText(claudeAssistant("", "Codex reply.")), "2026-11-16T11:00:05.000Z", nil},
	}
	tx := seedTx(t, db)
	for _, row := range rows {
		if _, err := tx.Exec("INSERT INTO session_messages (id,session_id,role,content,created_at,sent_at,model) VALUES (?,?,?,?,?,?,?)", row[0], row[1], row[2], row[3], row[4], row[4], row[5]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	insertConductorEvents(t, db, "claude-1", 0,
		conductorUserEvent("e-user", "u1", "Tidy the formatter", "sent", "2026-11-16T10:00:00.100Z"),
		conductorAgentEvent("e-reply", "2026-11-16T10:00:05.000Z", claudeAssistant("uuid-reply", "Looking at it.")),
		conductorTestEvent{id: "e-checkpoint", kind: "checkpointCreated", at: "2026-11-16T10:00:05.500Z", content: map[string]any{"type": "checkpointCreated", "checkpointId": "c1"}},
		conductorAgentEvent("e-rate", "2026-11-16T10:00:06.000Z", map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed"}}),
		conductorAgentEvent("e-progress", "2026-11-16T10:00:07.000Z", map[string]any{"type": "tool_progress", "elapsed_time_seconds": 3}),
		conductorAgentEvent("e-task-progress", "2026-11-16T10:00:07.500Z", map[string]any{"type": "system", "subtype": "task_progress"}),
		conductorAgentEvent("e-image-note", "2026-11-16T10:00:08.000Z", map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[Image: original 2104x646]"}}}}),
		// Events Conductor projects whose rows are missing.
		conductorAgentEvent("e-lost", "2026-11-16T10:00:10.000Z", map[string]any{"type": "assistant", "uuid": "uuid-lost", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call-1", "name": "Bash", "input": map[string]any{"command": "go test"}}}}}),
		conductorAgentEvent("e-result", "2026-11-16T10:00:11.000Z", map[string]any{"type": "user", "uuid": "uuid-result", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": "ok"}}}}),
		conductorAgentEvent("e-command", "2026-11-16T10:00:12.000Z", map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<local-command-stdout>Compacted</local-command-stdout>"}}),
		// Reverted: the user rewound past it.
		conductorTestEvent{id: "e-reverted", kind: "agent", at: "2026-11-16T10:00:20.000Z", reverted: "2026-11-16T10:01:00.000Z", content: map[string]any{"type": "agent", "rawPayload": claudeAssistant("uuid-reverted", "Withdrawn.")}},
		conductorAgentSentEvent("e-sent", "u2", "<system_instruction>\nWork in the repo.\n</system_instruction>\n\nAnd the tests", "2026-11-16T10:00:30.000Z"),
		conductorUserEvent("e-queued", "u3", "Not yet sent", "queued", "2026-11-16T10:00:31.000Z"),
		conductorAgentEvent("e-copied", "2026-11-16T10:00:40.000Z", claudeAssistant("uuid-copied", "Copied elsewhere.")),
		conductorAgentEvent("e-done", "2026-11-16T10:00:50.000Z", map[string]any{"type": "result", "subtype": "success", "result": "Done."}),
	)
	insertConductorEvents(t, db, "codex-1", 0,
		conductorUserEvent("x-user", "cu1", "Check codex", "sent", "2026-11-16T11:00:00.100Z"),
		conductorAgentEvent("x-started", "2026-11-16T11:00:04.000Z", map[string]any{"event": map[string]any{"type": "item.started", "item": map[string]any{"type": "agentMessage", "text": ""}}}),
		conductorAgentEvent("x-done", "2026-11-16T11:00:05.000Z", map[string]any{"event": map[string]any{"type": "item.completed", "item": map[string]any{"type": "agentMessage", "text": "Codex reply."}}}),
		conductorAgentEvent("x-unmapped", "2026-11-16T11:00:06.000Z", map[string]any{"event": map[string]any{"type": "item.completed", "item": map[string]any{"type": "agentMessage", "text": "Pharos does not translate Codex items."}}}),
	)
	return db
}

func conductorRecords(t *testing.T, dir string) map[string]WorkspaceRecord {
	t.Helper()
	adapter, err := MakeAdapter(SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]WorkspaceRecord{}
	if err := adapter.Discover(func(record WorkspaceRecord) error { records[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	return records
}

func nativeIDs(record WorkspaceRecord) []string {
	ids := []string{}
	for _, message := range record.Conversations[0].Messages {
		ids = append(ids, message.NativeID)
	}
	return ids
}

func TestConductorEventLogAddsOnlyUnprojectedEvents(t *testing.T) {
	dir := t.TempDir()
	db := conductorEventLogFixture(t, dir)
	records := conductorRecords(t, dir)
	claude := records["claude-1"]
	want := []string{
		"u1",
		"cloud-feed:claude-1:e-reply:0:block:0",
		"cloud-feed:claude-1:e-lost:0:block:0",
		"cloud-feed:claude-1:e-result:0:block:0",
		"cloud-feed:claude-1:e-command:0:block:0",
		"u2",
		"local-copy:block:0",
		"cloud-feed:claude-1:e-done:0",
	}
	if got := nativeIDs(claude); !slices.Equal(got, want) {
		t.Fatalf("Claude messages:\n got %q\nwant %q", got, want)
	}
	byID := map[string]MessageRecord{}
	for _, message := range claude.Conversations[0].Messages {
		byID[message.NativeID] = message
	}
	if call := byID["cloud-feed:claude-1:e-lost:0:block:0"]; call.Kind != "tool_call" || call.CallID != "call-1" || call.CreatedAt != "2026-11-16T10:00:10.000Z" || !strings.HasSuffix(call.EvidenceLocator, ":session_events:claude-1:content:7") {
		t.Fatalf("rebuilt tool call: %+v", call)
	}
	if result := byID["cloud-feed:claude-1:e-result:0:block:0"]; result.Kind != "tool_result" || result.CallID != "call-1" {
		t.Fatalf("rebuilt tool result: %+v", result)
	}
	if output := byID["cloud-feed:claude-1:e-command:0:block:0"]; output.Role != "assistant" || output.Text != "Compacted" {
		t.Fatalf("local command output is stored as assistant text: %+v", output)
	}
	if sent := byID["u2"]; sent.Role != "user" || sent.Text != "And the tests" || sent.Model != "opus" || sent.Sender != "agent:parent-session" {
		t.Fatalf("a sent message only the log has: %+v", sent)
	}
	// Codex events are never translated; its rows are its messages.
	if got := nativeIDs(records["codex-1"]); !slices.Equal(got, []string{"cu1", "cloud-feed:codex-1:x-done:0:block:0"}) {
		t.Fatalf("Codex messages: %q", got)
	}

	// Every message a row projects parses exactly as it does without the
	// event log, as in a database from before it.
	execSQL(t, db, "DROP TABLE session_events")
	legacy := conductorRecords(t, dir)
	for _, sid := range []string{"claude-1", "codex-1"} {
		for _, message := range legacy[sid].Conversations[0].Messages {
			var found bool
			for _, merged := range records[sid].Conversations[0].Messages {
				if merged.NativeID == message.NativeID {
					found = true
					if jsonText(merged) != jsonText(message) {
						t.Errorf("%s changed with the event log:\n got %s\nwant %s", message.NativeID, jsonText(merged), jsonText(message))
					}
				}
			}
			if !found {
				t.Errorf("%s lost with the event log", message.NativeID)
			}
		}
	}
	if got := nativeIDs(legacy["claude-1"]); len(got) != 4 {
		t.Fatalf("legacy Claude messages: %q", got)
	}
}

// When Conductor later stores the row for an event Pharos rebuilt, the row
// is the same message, not a second one.
func TestConductorEventLogRowArrivingLaterIsTheSameMessage(t *testing.T) {
	dir := t.TempDir()
	db := conductorEventLogFixture(t, dir)
	before := nativeIDs(conductorRecords(t, dir)["claude-1"])
	lost := map[string]any{"type": "assistant", "uuid": "uuid-lost", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call-1", "name": "Bash", "input": map[string]any{"command": "go test"}}}}}
	if _, err := db.Exec("INSERT INTO session_messages (id,session_id,role,content,created_at,sent_at) VALUES ('cloud-feed:claude-1:e-lost:0','claude-1','assistant',?,'2026-11-16T10:00:10.000Z','2026-11-16T10:00:10.000Z')", jsonText(lost)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_messages (id,session_id,role,content,created_at,sent_at,model) VALUES ('u2','claude-1','user','And the tests','2026-11-16T10:00:30.000Z','2026-11-16T10:00:30.000Z','opus')"); err != nil {
		t.Fatal(err)
	}
	after := conductorRecords(t, dir)["claude-1"]
	if got := nativeIDs(after); !slices.Equal(got, before) {
		t.Fatalf("stored rows changed the messages:\n got %q\nwant %q", got, before)
	}
	for _, message := range after.Conversations[0].Messages {
		if strings.Contains(message.EvidenceLocator, ":session_events:") && (message.NativeID == "u2" || strings.Contains(message.NativeID, "e-lost")) {
			t.Fatalf("%s still reads the event log once its row is stored: %s", message.NativeID, message.EvidenceLocator)
		}
	}
}

func TestConductorEventLogPreservedBySessionAcquisition(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	db := conductorEventLogFixture(t, dir)
	db.Close()
	live := conductorRecords(t, dir)["claude-1"]
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
	config := captureTestConfig(t, source)
	var acquired WorkspaceRecord
	if err := acquireConductorSession(t.Context(), config.CaptureRoot, source, filepath.Join(dir, "conductor.db"), "claude-1", func(record WorkspaceRecord) error { acquired = record; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, want := nativeIDs(acquired), nativeIDs(live); !slices.Equal(got, want) {
		t.Fatalf("acquired messages:\n got %q\nwant %q", got, want)
	}
	store := recordTestStore(t, config, "conductor")
	revisions, err := store.revisions()
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions %d %v", len(revisions), err)
	}
	revision := revisions[0].Revision
	for _, table := range revision.Tables {
		if table.Name == conductorEventTable && (table.Coverage != "selected" || table.Rows != 15) {
			t.Fatalf("session acquisition kept %d events as %q", table.Rows, table.Coverage)
		}
	}
	os.RemoveAll(dir)
	events, err := store.conductorEventRows(revision, "claude-1")
	if err != nil || len(events) != 15 || firstString(events[0]["event_id"]) != "e-user" {
		t.Fatalf("retained event log: %d %v", len(events), err)
	}
	var lost MessageRecord
	for _, message := range acquired.Conversations[0].Messages {
		if message.NativeID == "cloud-feed:claude-1:e-lost:0:block:0" {
			lost = message
		}
	}
	resolved, err := store.resolveLocator(acquired.EvidenceRevision, lost.EvidenceLocator)
	if err != nil {
		t.Fatalf("rebuilt message's locator %q: %v", lost.EvidenceLocator, err)
	}
	value, err := resolved.Value.decode()
	if err != nil || resolved.Table != conductorEventTable || resolved.Ordinal != 7 || !strings.Contains(asString(value), "uuid-lost") {
		t.Fatalf("rebuilt message's locator: %+v %v", resolved, err)
	}
	useHost(t, "host-b")
	catalog, _ := testCatalog(t)
	if results := indexCaptures(t, catalog, config.CaptureRoot, "host-a"); len(results) != 1 || conversationMessages(t, catalog, "conductor:claude-1") != len(live.Conversations[0].Messages) {
		t.Fatalf("replay after the session left Conductor: %+v", results)
	}
}

func TestConductorUnprojectedEventsSkipRepeatsAndEmptyMessages(t *testing.T) {
	event := func(id, kind string, content map[string]any) map[string]any {
		return map[string]any{"event_id": id, "event_type": kind, "content": jsonText(content), "received_at": "2026-11-16T10:00:00.000Z"}
	}
	lost := event("e1", "agent", map[string]any{"rawPayload": claudeAssistant("uuid-1", "Only in the log.")})
	inputs := conductorUnprojectedEvents("s", "claude", nil, []map[string]any{
		lost, lost,
		event("e2", "userMessage", map[string]any{"id": "u1", "state": "sent"}),
		event("e3", "userMessage", map[string]any{"id": "u2", "state": "sent", "message": map[string]any{"text": "not a string"}}),
	})
	if len(inputs) != 1 || inputs[0].row["id"] != "cloud-feed:s:e1:0" {
		t.Fatalf("rebuilt rows: %+v", inputs)
	}
}

func TestConductorUserTextDropsLeadingInstructions(t *testing.T) {
	for message, want := range map[string]string{
		"Plain request": "Plain request",
		"  <system_instruction>a</system_instruction>\n<system_instruction>b</system_instruction>\n Ask": "Ask",
		"Ask <system_instruction>kept</system_instruction>":                                              "Ask <system_instruction>kept</system_instruction>",
		"<system_instruction>unterminated":                                                               "<system_instruction>unterminated",
	} {
		if got := conductorUserText(message); got != want {
			t.Errorf("conductorUserText(%q) = %q, want %q", message, got, want)
		}
	}
}
