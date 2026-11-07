package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

const personalAgentSecret = "personal-agent-credential-do-not-capture"

// These are projections of the native schemas, including the optional fields
// whose presence varies between released versions. They intentionally share
// each database with private state which is outside the capture allowlist.
const openClawTestSchema = `
CREATE TABLE schema_meta(meta_key TEXT PRIMARY KEY,role TEXT,schema_version INTEGER,agent_id TEXT,app_version TEXT,created_at INTEGER,updated_at INTEGER);
CREATE TABLE session_nodes(session_key TEXT PRIMARY KEY,current_session_id TEXT,entry_json TEXT,spawned_by TEXT,parent_session_key TEXT,created_via TEXT,created_actor_type TEXT,updated_at INTEGER);
CREATE TABLE session_windows(session_id TEXT PRIMARY KEY,session_key TEXT,previous_session_id TEXT,reason TEXT,created_at INTEGER,updated_at INTEGER,started_at INTEGER,ended_at INTEGER,model TEXT,channel TEXT,parent_session_key TEXT,spawned_by TEXT,display_name TEXT);
CREATE TABLE transcript_events(session_id TEXT,seq INTEGER,event_json TEXT,event_zstd BLOB,created_at INTEGER,navigation_json TEXT,PRIMARY KEY(session_id,seq));
CREATE TABLE session_transcript_index_state(session_id TEXT PRIMARY KEY,leaf_event_id TEXT,needs_rebuild INTEGER);
CREATE TABLE session_transcript_archives(session_id TEXT,generation TEXT,session_key TEXT,reason TEXT,encoding TEXT,archive_blob BLOB,archive_sha256 TEXT,archive_name TEXT,created_at INTEGER,published_at INTEGER,publish_attempts INTEGER DEFAULT 0,last_publish_attempt_at INTEGER,last_publish_error TEXT,PRIMARY KEY(session_id,generation));
CREATE TABLE session_transcript_cold_archives(session_id TEXT PRIMARY KEY,generation TEXT,archive_name TEXT,archive_sha256 TEXT,event_count INTEGER,raw_bytes INTEGER,archive_bytes INTEGER,last_seq INTEGER,archived_at INTEGER,storage TEXT,archive_blob BLOB);
CREATE TABLE session_transcript_cold_archive_chunks(session_id TEXT,chunk_index INTEGER,archive_name TEXT,archive_sha256 TEXT,first_seq INTEGER,last_seq INTEGER,event_count INTEGER,raw_bytes INTEGER,archive_bytes INTEGER,archive_blob BLOB,PRIMARY KEY(session_id,chunk_index));
CREATE TABLE auth_credentials(secret TEXT);
INSERT INTO schema_meta VALUES('schema','agent',1,'main','2026.11.1',1793720000000,1793720000000);
`

const hermesTestSchema = `
CREATE TABLE sessions(id TEXT PRIMARY KEY,source TEXT,created_source TEXT,session_key TEXT,chat_type TEXT,model TEXT,model_config TEXT,parent_session_id TEXT,started_at REAL,ended_at REAL,end_reason TEXT,input_tokens INTEGER DEFAULT 0,output_tokens INTEGER DEFAULT 0,cache_read_tokens INTEGER DEFAULT 0,cache_write_tokens INTEGER DEFAULT 0,reasoning_tokens INTEGER DEFAULT 0,cwd TEXT,git_branch TEXT,git_repo_root TEXT,actual_cost_usd REAL,estimated_cost_usd REAL,title TEXT);
CREATE TABLE messages(id INTEGER PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,tool_call_id TEXT,tool_calls TEXT,tool_name TEXT,timestamp REAL,token_count INTEGER,reasoning TEXT,reasoning_content TEXT,reasoning_details TEXT,_compressed_summary INTEGER DEFAULT 0,active INTEGER DEFAULT 1,compacted INTEGER DEFAULT 0,display_kind TEXT,display_metadata TEXT,display_identity BLOB,message_uid TEXT,tool_call_uids TEXT,tool_call_uid TEXT);
CREATE TABLE session_model_usage(session_id TEXT,model TEXT,billing_provider TEXT,task TEXT,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER,actual_cost_usd REAL,estimated_cost_usd REAL,first_seen REAL,last_seen REAL);
CREATE TABLE credentials(secret TEXT);
`

func personalTestDB(t *testing.T, file, schema string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	db := openWritableSQLite(t, file)
	execSQL(t, db, schema)
	return db
}

func personalInsert(t *testing.T, db *sql.DB, table string, row map[string]any) {
	t.Helper()
	var keys, placeholders []string
	var values []any
	for key, value := range row {
		keys = append(keys, `"`+key+`"`)
		placeholders = append(placeholders, "?")
		values = append(values, value)
	}
	if _, err := db.Exec("INSERT INTO "+table+" ("+strings.Join(keys, ",")+") VALUES ("+strings.Join(placeholders, ",")+")", values...); err != nil {
		t.Fatal(err)
	}
}

func personalZstd(t *testing.T, data []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	return encoder.EncodeAll(data, nil)
}

func openClawTestEvents() []map[string]any {
	return []map[string]any{
		{"type": "session", "id": "root", "cwd": "/work/app", "timestamp": "2026-11-03T18:13:20Z"},
		{"type": "model_change", "id": "model", "parentId": nil, "modelId": "claude-sonnet-4-5", "timestamp": "2026-11-03T18:13:20Z"},
		{"type": "thinking_level_change", "id": "effort", "parentId": "model", "thinkingLevel": "high", "timestamp": "2026-11-03T18:13:20Z"},
		{"type": "message", "id": "user", "parentId": "effort", "timestamp": "2026-11-03T18:13:21Z", "message": map[string]any{"role": "user", "content": "Count the words"}},
		{"type": "message", "id": "answer", "parentId": "user", "timestamp": "2026-11-03T18:13:22Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "Read the file first."}, map[string]any{"type": "toolCall", "id": "call1", "name": "exec", "arguments": map[string]any{"command": "wc -w story.txt"}}}, "usage": map[string]any{"input": 100, "output": 20, "cacheRead": 40, "cacheWrite": 10}}},
		{"type": "message", "id": "result", "parentId": "answer", "timestamp": "2026-11-03T18:13:23Z", "message": map[string]any{"role": "toolResult", "toolCallId": "call1", "toolName": "exec", "content": []any{map[string]any{"type": "text", "text": "74 story.txt"}}}},
		{"type": "message", "id": "done", "parentId": "result", "timestamp": "2026-11-03T18:13:24Z", "message": map[string]any{"role": "assistant", "content": "There are 74 words."}},
	}
}

func openClawFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "agents", "main", "agent", "openclaw-agent.sqlite")
	db := personalTestDB(t, file, openClawTestSchema)
	defer db.Close()
	execSQL(t, db, "INSERT INTO auth_credentials VALUES('"+personalAgentSecret+"')")
	for _, id := range []string{"root", "child"} {
		meta := map[string]any{"label": "Word count", "model": "claude-sonnet-4-5", "thinkingLevel": "high"}
		row := map[string]any{"session_key": "agent:main:" + id, "current_session_id": id, "entry_json": jsonText(meta)}
		if id == "child" {
			row["spawned_by"] = "agent:main:root"
		}
		personalInsert(t, db, "session_nodes", row)
		personalInsert(t, db, "session_windows", map[string]any{"session_id": id, "session_key": "agent:main:" + id, "reason": "initial", "created_at": 1793720000000, "updated_at": 1793720004000, "started_at": 1793720000000, "channel": "telegram"})
		events := openClawTestEvents()
		if id == "child" {
			events = []map[string]any{{"type": "session", "id": "child", "cwd": "/work/app"}, {"type": "message", "id": "c1", "message": map[string]any{"role": "user", "content": "Review the count"}}}
		}
		for seq, event := range events {
			row := map[string]any{"session_id": id, "seq": seq, "event_json": jsonText(event), "created_at": 1793720000000}
			if seq == 4 {
				row["event_zstd"] = personalZstd(t, []byte(jsonText(event)))
				row["event_json"] = nil
			}
			personalInsert(t, db, "transcript_events", row)
		}
	}
	personalInsert(t, db, "session_transcript_archives", openClawReclaimedRow(t, "reclaimed", "generation-one", "zstd", "deleted"))
	writeSourceFile(t, filepath.Join(root, "openclaw.json"), `{"apiKey":"`+personalAgentSecret+`"}`)
	return root
}

func openClawReclaimedRow(t *testing.T, id, generation, encoding, reason string) map[string]any {
	t.Helper()
	events := openClawTestEvents()
	events[0]["id"] = id
	mapValue(events[3]["message"])["content"] = "Archived task " + generation
	var lines []string
	for _, event := range events {
		lines = append(lines, jsonText(event))
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	name := id + ".jsonl." + reason + "." + generation
	if encoding == "zstd" {
		data = personalZstd(t, data)
		name += ".zst"
	}
	return map[string]any{"session_id": id, "generation": generation, "session_key": "agent:main:" + id, "reason": reason, "encoding": encoding, "archive_blob": data, "archive_sha256": hashBytes(data), "archive_name": name, "created_at": 1793720004000}
}

func hermesFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	db := personalTestDB(t, filepath.Join(root, "state.db"), hermesTestSchema)
	defer db.Close()
	execSQL(t, db, "INSERT INTO credentials VALUES('"+personalAgentSecret+"')")
	for _, id := range []string{"root", "continuation", "child", "fork", "reset"} {
		row := map[string]any{"id": id, "source": "cli", "created_source": "cli", "model": "claude-sonnet-4-5", "model_config": `{"reasoning_effort":"high"}`, "started_at": 1793720000, "ended_at": 1793720004, "cwd": "/work/app", "git_branch": "main", "title": "Word count"}
		if id == "root" {
			row["end_reason"] = "compression"
			row["input_tokens"] = 180
			row["output_tokens"] = 25
			row["cache_read_tokens"] = 40
			row["cache_write_tokens"] = 10
			row["reasoning_tokens"] = 5
			row["actual_cost_usd"] = 0.12
		}
		if id != "root" {
			row["parent_session_id"] = "root"
		}
		switch id {
		case "continuation":
			row["started_at"] = 1793720005
			row["ended_at"] = nil
		case "child":
			row["source"] = "tool"
			row["model_config"] = `{"_delegate_from":"root"}`
		case "fork":
			row["model_config"] = `{"_branched_from":"root"}`
		case "reset":
			row["model_config"] = `{"_reset_from":"root"}`
		}
		personalInsert(t, db, "sessions", row)
	}
	rows := []map[string]any{
		{"id": 1, "session_id": "root", "role": "user", "content": "Count the words", "timestamp": 1793720000, "message_uid": "u1", "active": 0, "compacted": 0},
		{"id": 2, "session_id": "root", "role": "assistant", "content": "Checking the file.", "reasoning_content": "Read the file first.", "tool_calls": `[{"id":"call1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"wc -w story.txt\"}"}}]`, "tool_call_uids": `{"call1":"occurrence-one"}`, "timestamp": 1793720001},
		{"id": 3, "session_id": "root", "role": "tool", "tool_call_id": "call1", "tool_call_uid": "occurrence-one", "tool_name": "terminal", "content": "74 story.txt", "timestamp": 1793720002},
		{"id": 4, "session_id": "continuation", "role": "user", "content": "Count the words", "timestamp": 1793720000, "message_uid": "u1"},
		{"id": 5, "session_id": "continuation", "role": "assistant", "content": "There are 74 words.", "timestamp": 1793720006},
		{"id": 6, "session_id": "child", "role": "user", "content": "Review the count", "timestamp": 1793720002},
	}
	for _, row := range rows {
		personalInsert(t, db, "messages", row)
	}
	for _, usage := range []struct {
		model, task                           string
		input, output, read, write, reasoning int
	}{{"claude-sonnet-4-5", "", 100, 20, 40, 10, 5}, {"gpt-5", "voice_chat", 50, 2, 0, 0, 0}, {"gpt-5", "compression", 30, 3, 0, 0, 0}} {
		personalInsert(t, db, "session_model_usage", map[string]any{"session_id": "root", "model": usage.model, "billing_provider": "test", "task": usage.task, "input_tokens": usage.input, "output_tokens": usage.output, "cache_read_tokens": usage.read, "cache_write_tokens": usage.write, "reasoning_tokens": usage.reasoning, "last_seen": 1793720004})
	}
	writeSourceFile(t, filepath.Join(root, "auth.json"), `{"token":"`+personalAgentSecret+`"}`)
	return root
}

func TestPersonalAgentTranscripts(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			root := openClawFixture
			if kind == "hermes" {
				root = hermesFixture
			}
			records := discoverAll(t, conformanceSource(kind, root(t)))
			ns := "main"
			if kind == "hermes" {
				ns = "default"
			}
			record, ok := records[ns+":root"]
			if !ok {
				t.Fatalf("missing root: %v", records)
			}
			cv := record.Conversations[0]
			if record.Location != "/work/app" || cv.Effort != "high" {
				t.Errorf("context: %+v", record)
			}
			counts := conversationTokenCounts(cv.Messages)
			input, output := 150.0, 20.0
			if kind == "hermes" {
				input, output = 260, 28
			}
			if counts["input_tokens"] != input || counts["output_tokens"] != output {
				t.Errorf("usage: %v", counts)
			}
			if counts["cache_read_input_tokens"] != 40 || counts["cache_creation_input_tokens"] != 10 {
				t.Errorf("cache usage: %v", counts)
			}
			child := records[ns+":child"].Conversations[0]
			if child.ParentNativeID != cv.NativeID || child.Messages[0].Role != agentRole {
				t.Errorf("delegation: %+v", child)
			}
			if kind == "hermes" {
				if len(records) != 4 {
					t.Errorf("compression produced %d workspaces; want 4", len(records))
				}
				for _, id := range []string{"fork", "reset"} {
					if records[ns+":"+id].Conversations[0].ParentNativeID != "" {
						t.Errorf("%s classified as delegation", id)
					}
				}
				n := 0
				for _, m := range cv.Messages {
					if m.Text == "Count the words" {
						n++
						if !m.Selected {
							t.Error("live compaction copy inherited the removed original's selection")
						}
					}
				}
				if n != 1 {
					t.Errorf("compaction copied prompt %d times", n)
				}
				for _, r := range conversationTokenReports(cv.Messages) {
					if r.counts != nil && r.scope == "request" {
						t.Error("aggregate usage became a model request")
					}
				}
			}
		})
	}
}

func TestOpenClawLegacyBranches(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "session.jsonl")
	events := openClawTestEvents()
	events = append(events, map[string]any{"type": "message", "id": "other", "parentId": "user", "timestamp": "2026-11-03T18:13:25Z", "message": map[string]any{"role": "assistant", "content": "Another answer", "usage": map[string]any{"input": 4, "output": 2}}})
	events = append(events, map[string]any{"type": "message", "id": "rewrite", "parentId": "result", "supersedesEntryId": "answer", "message": events[4]["message"]})
	var lines []string
	for _, event := range events {
		lines = append(lines, jsonText(event))
	}
	writeSourceFile(t, file, strings.Join(lines, "\n")+"\n"+`{"type":"message","id":`)
	writeSourceFile(t, filepath.Join(root, "sessions.json"), `{"agent:main:root":{"sessionId":"root","label":"Legacy title"}}`)
	record := discoverAll(t, conformanceSource("openclaw", file))["main:root"]
	if record.Title != "Legacy title" {
		t.Fatal(record.Title)
	}
	selected := map[string]bool{}
	for _, m := range record.Conversations[0].Messages {
		selected[m.NativeID] = m.Selected
	}
	if selected["other"] || selected["answer"] || !selected["rewrite"] || !selected["result"] {
		t.Errorf("branch selection: %v", selected)
	}
	if record.Outcome != "" {
		t.Fatalf("discarded branch supplied outcome %q", record.Outcome)
	}
	counts := conversationTokenCounts(record.Conversations[0].Messages)
	if counts["input_tokens"] != 154 || counts["output_tokens"] != 22 {
		t.Errorf("rewrite counted copied usage twice: %v", counts)
	}
	writeSourceFile(t, file, strings.Join(lines, "\n")+"\n{bad}\n")
	a, _ := MakeAdapter(conformanceSource("openclaw", file))
	if err := a.Discover(func(WorkspaceRecord) error { return nil }); err == nil {
		t.Error("accepted malformed complete JSONL line")
	}
}

// A cold navigation row has no message text. Its verified archive is required
// even when hot events from the same session are still present.
func TestOpenClawColdArchives(t *testing.T) {
	for _, storage := range []string{"file", "sqlite"} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunks=%t", storage, chunked), func(t *testing.T) {
				root := t.TempDir()
				file := filepath.Join(root, "agent", "openclaw-agent.sqlite")
				db := personalTestDB(t, file, openClawTestSchema)
				personalInsert(t, db, "session_windows", map[string]any{"session_id": "root", "session_key": "agent:main:root"})
				events := openClawTestEvents()
				size := len(events)
				if chunked {
					size = 3
				}
				var manifest []any
				var totalRaw, totalArchive int64
				for start, index := 0, 0; start < len(events); start, index = start+size, index+1 {
					end := min(start+size, len(events))
					lines := []string{jsonText(map[string]any{"kind": "header", "version": 1, "sessionId": "root", "generation": "g1"})}
					raw := int64(-1)
					for seq := start; seq < end; seq++ {
						text := jsonText(events[seq])
						raw += int64(len(text) + 1)
						lines = append(lines, jsonText(map[string]any{"kind": "event", "row": map[string]any{"seq": seq, "event_json": text}}))
						personalInsert(t, db, "transcript_events", map[string]any{"session_id": "root", "seq": seq, "navigation_json": `{"type":"message"}`})
					}
					data := personalZstd(t, []byte(strings.Join(lines, "\n")+"\n"))
					hash := hashBytes(data)
					name := hash + ".jsonl.zst"
					row := map[string]any{"session_id": "root", "archive_name": name, "archive_sha256": hash, "event_count": end - start, "raw_bytes": raw, "archive_bytes": len(data), "last_seq": end - 1}
					if storage == "sqlite" {
						row["archive_blob"] = data
					} else {
						writeSourceFile(t, filepath.Join(root, "sessions", "cold", name), string(data))
					}
					if chunked {
						row["chunk_index"] = index
						row["first_seq"] = start
						personalInsert(t, db, "session_transcript_cold_archive_chunks", row)
						manifest = append(manifest, []any{index, name, hash, start, end - 1, end - start, raw, len(data)})
					} else {
						row["generation"] = "g1"
						row["storage"] = storage
						personalInsert(t, db, "session_transcript_cold_archives", row)
					}
					totalRaw += raw
					totalArchive += int64(len(data))
				}
				if chunked {
					hash := hashBytes([]byte(jsonText(manifest)))
					personalInsert(t, db, "session_transcript_cold_archives", map[string]any{"session_id": "root", "generation": "g1", "storage": storage, "archive_name": hash + ".jsonl.zst", "archive_sha256": hash, "event_count": len(events), "raw_bytes": totalRaw + int64(len(manifest)-1), "archive_bytes": totalArchive, "last_seq": len(events) - 1})
				}
				db.Close()
				source := conformanceSource("openclaw", file)
				record := discoverAll(t, source)["main:root"]
				if counts := conversationTokenCounts(record.Conversations[0].Messages); counts["input_tokens"] != 150 {
					t.Fatal(counts)
				}
				config := captureTestConfig(t, source)
				if summary := runCapture(t, config); !summary.OK {
					t.Fatalf("cold capture: %+v", summary)
				}
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
				lease, err := leaseCaptureForIndex(t.Context(), target)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				n := 0
				if err := lease.Adapter.Discover(func(r WorkspaceRecord) error {
					n++
					if len(r.Conversations[0].Messages) == 0 {
						t.Error("empty cold transcript")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Fatalf("replayed %d cold records", n)
				}
			})
		}
	}
}

func TestOpenClawReclaimedArchives(t *testing.T) {
	t.Run("legacy-files", func(t *testing.T) {
		root := t.TempDir()
		generation := "0123456789abcdef0123456789abcdef"
		registered := openClawReclaimedRow(t, "root", generation, "zstd", "deleted")
		registered["archive_name"] = "root.jsonl.deleted.2026-11-03T18-13-24.000Z." + generation + ".zst"
		rows := []map[string]any{
			openClawReclaimedRow(t, "root", "one", "identity", "reset"),
			openClawReclaimedRow(t, "root", "two", "zstd", "reset"),
			openClawReclaimedRow(t, "root", "one", "identity", "deleted"),
			registered,
		}
		for _, row := range rows {
			writeSourceFile(t, filepath.Join(root, "sessions", firstString(row["archive_name"])), string(personalBytes(row["archive_blob"])))
		}
		// A compressed derivative of the same legacy generation has one identity.
		writeSourceFile(t, filepath.Join(root, "sessions", "root.jsonl.reset.one.zst"), string(personalZstd(t, personalBytes(rows[0]["archive_blob"]))))
		writeSourceFile(t, filepath.Join(root, "sessions", "root.jsonl"), string(personalBytes(rows[0]["archive_blob"])))
		writeSourceFile(t, filepath.Join(root, "sessions", "sessions.json"), `{"agent:main:root":{"sessionId":"root","label":"Live title","spawnedBy":"agent:main:parent"},"agent:main:parent":{"sessionId":"parent"}}`)
		source := conformanceSource("openclaw", root)
		records := discoverAll(t, source)
		if len(records) != 5 || records["main:root"].Title != "Live title" {
			t.Fatalf("lost legacy generations: %v", records)
		}
		for _, id := range []string{"reset.one", "reset.two", "deleted.one", generation} {
			r, ok := records["main:"+openClawArchivedID("root", id)]
			if !ok || !strings.HasPrefix(r.Purpose, "Archived task ") || r.Title == "Live title" || r.Conversations[0].ParentNativeID != "" {
				t.Fatalf("lost or mislabeled archive %s: %+v", id, r)
			}
			if counts := conversationTokenCounts(r.Conversations[0].Messages); counts["input_tokens"] != 150 {
				t.Fatal(counts)
			}
		}
		a := &personalAgentAdapter{baseAdapter{config: source}}
		inputs, err := a.inputs()
		if err != nil {
			t.Fatal(err)
		}
		if stats, err := a.personalProbeStats(inputs, time.Now().Add(5*time.Second)); err != nil || stats.sessions != 5 {
			t.Fatalf("legacy generation probe: %+v %v", stats, err)
		}
		// The same published file and SQLite registry row must name the
		// same conversation when the canonical database becomes available.
		db := personalTestDB(t, filepath.Join(root, "agent", "openclaw-agent.sqlite"), openClawTestSchema)
		personalInsert(t, db, "session_transcript_archives", registered)
		db.Close()
		withDatabase := discoverAll(t, source)
		if len(withDatabase) != len(records) || withDatabase["main:"+openClawArchivedID("root", generation)].Purpose != records["main:"+openClawArchivedID("root", generation)].Purpose {
			t.Fatal("published generation identity changed with canonical SQLite input")
		}
		config := captureTestConfig(t, source)
		if result := runCapture(t, config); !result.OK {
			t.Fatalf("legacy archive capture: %+v", result)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
		input, err := leaseCaptureForIndex(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		replayed := map[string]WorkspaceRecord{}
		if err := input.Adapter.Discover(func(r WorkspaceRecord) error { replayed[r.SourceID] = r; return nil }); err != nil {
			t.Fatal(err)
		}
		if jsonText(withDatabase) != jsonText(replayed) {
			t.Fatal("legacy archive generations changed in capture replay")
		}
	})
	for _, encoding := range []string{"identity", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "agent", "openclaw-agent.sqlite")
			db := personalTestDB(t, file, openClawTestSchema)
			// The generations have no live window: their database blobs are
			// their only canonical copies until file publication completes.
			for _, generation := range []string{"reset-generation", "deleted-generation"} {
				reason := "reset"
				if strings.HasPrefix(generation, "deleted") {
					reason = "deleted"
				}
				row := openClawReclaimedRow(t, "root", generation, encoding, reason)
				personalInsert(t, db, "session_transcript_archives", row)
				if reason == "deleted" {
					// A published derivative must not become a third conversation
					// or replace the canonical SQLite generation.
					writeSourceFile(t, filepath.Join(root, "sessions", firstString(row["archive_name"])), "stale derivative\n")
				}
			}
			db.Close()
			source := conformanceSource("openclaw", root)
			records := discoverAll(t, source)
			if len(records) != 2 {
				t.Fatalf("retained %d generations, want 2", len(records))
			}
			for _, generation := range []string{"reset-generation", "deleted-generation"} {
				id := personalNativeID("main", openClawArchivedID("root", generation))
				record, ok := records[id]
				if !ok || record.Purpose != "Archived task "+generation {
					t.Fatalf("lost generation %s: %+v", generation, record)
				}
				if counts := conversationTokenCounts(record.Conversations[0].Messages); counts["input_tokens"] != 150 {
					t.Fatal(counts)
				}
			}
			// Reusing the physical session ID for a live window must keep
			// both archived generations and the live conversation.
			db = openWritableSQLite(t, file)
			personalInsert(t, db, "session_windows", map[string]any{"session_id": "root", "session_key": "agent:main:root"})
			personalInsert(t, db, "transcript_events", map[string]any{"session_id": "root", "seq": 0, "event_json": jsonText(map[string]any{"type": "message", "id": "live", "message": map[string]any{"role": "user", "content": "Live task"}})})
			db.Close()
			records = discoverAll(t, source)
			if len(records) != 3 || records["main:root"].Purpose != "Live task" {
				t.Fatal("live session overwrote an archived generation")
			}
			config := captureTestConfig(t, source)
			if result := runCapture(t, config); !result.OK {
				t.Fatalf("reclaimed capture: %+v", result)
			}
			if files := conformanceHolding(t, config.CaptureRoot, "stale derivative"); len(files) > 0 {
				t.Fatalf("captured unused derivative: %v", files)
			}
			// Bad canonical data is reported even when a derived file exists.
			db = openWritableSQLite(t, file)
			execSQL(t, db, "UPDATE session_transcript_archives SET archive_sha256='bad'")
			db.Close()
			a, _ := MakeAdapter(source)
			if err := a.Discover(func(WorkspaceRecord) error { return nil }); err == nil || !strings.Contains(err.Error(), "hash") {
				t.Fatalf("archive hash mismatch: %v", err)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
			input, err := leaseCaptureForIndex(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			replayed := map[string]WorkspaceRecord{}
			if err := input.Adapter.Discover(func(record WorkspaceRecord) error { replayed[record.SourceID] = record; return nil }); err != nil {
				t.Fatal(err)
			}
			if jsonText(records) != jsonText(replayed) {
				t.Fatal("capture lost or changed reclaimed generations after source removal")
			}
		})
	}
}

func TestPersonalAgentProfiles(t *testing.T) {
	root := hermesFixture(t)
	file := filepath.Join(root, "profiles", "work", "state.db")
	db := personalTestDB(t, file, hermesTestSchema)
	personalInsert(t, db, "sessions", map[string]any{"id": "root", "source": "cli", "started_at": 1793720000})
	db.Close()
	records := discoverAll(t, conformanceSource("hermes", root))
	if _, ok := records["work:root"]; !ok {
		t.Fatal("profile identity collided with default")
	}
	if _, ok := records["default:root"]; !ok {
		t.Fatal("default identity lost")
	}
}

func TestPersonalAgentQuotedUsage(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			build := openClawFixture
			native := "main:root"
			want := 150.0
			if kind == "hermes" {
				build = hermesFixture
				native = "default:root"
				want = 260
			}
			root := build(t)
			file := filepath.Join(root, "agents", "main", "agent", "openclaw-agent.sqlite")
			if kind == "hermes" {
				file = filepath.Join(root, "state.db")
			}
			db := openWritableSQLite(t, file)
			quote := `{"type":"token_usage","usage":{"input_tokens":999999,"output_tokens":100}}`
			if kind == "hermes" {
				if _, err := db.Exec("UPDATE messages SET content=? WHERE id=5", quote); err != nil {
					t.Fatal(err)
				}
			} else {
				event := openClawTestEvents()[6]
				mapValue(event["message"])["content"] = quote
				if _, err := db.Exec("UPDATE transcript_events SET event_json=? WHERE session_id='root' AND seq=6", jsonText(event)); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			record := discoverAll(t, conformanceSource(kind, root))[native]
			if counts := conversationTokenCounts(record.Conversations[0].Messages); counts["input_tokens"] != want {
				t.Fatalf("quoted prose counted as usage: %v", counts)
			}
		})
	}
}

func TestOpenClawMissingColdPayload(t *testing.T) {
	root := openClawFixture(t)
	file := filepath.Join(root, "agents", "main", "agent", "openclaw-agent.sqlite")
	db := openWritableSQLite(t, file)
	execSQL(t, db, "UPDATE transcript_events SET event_json=NULL,event_zstd=NULL WHERE session_id='root' AND seq=4")
	db.Close()
	a, _ := MakeAdapter(conformanceSource("openclaw", root))
	err := a.Discover(func(WorkspaceRecord) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no payload") {
		t.Fatalf("missing archive: %v", err)
	}
}

func TestOpenClawDamagedSessionIsolation(t *testing.T) {
	for _, damage := range []string{"missing-cold", "bad-cold", "no-payload", "bad-reclaimed"} {
		for _, retained := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained=%t", damage, retained), func(t *testing.T) {
				useHost(t, "host-a")
				root := openClawFixture(t)
				file := filepath.Join(root, "agents", "main", "agent", "openclaw-agent.sqlite")
				db := openWritableSQLite(t, file)
				personalInsert(t, db, "session_transcript_archives", openClawReclaimedRow(t, "valid-archive", "g", "identity", "deleted"))
				var coldPath string
				if damage == "missing-cold" || damage == "bad-cold" {
					event := jsonText(openClawTestEvents()[4])
					data := personalZstd(t, []byte(jsonText(map[string]any{"kind": "header", "version": 1, "sessionId": "root", "generation": "g"})+"\n"+jsonText(map[string]any{"kind": "event", "row": map[string]any{"seq": 4, "event_json": event}})+"\n"))
					hash := hashBytes(data)
					storage := "sqlite"
					var blob any = data
					if damage == "missing-cold" {
						storage = "file"
						blob = nil
						coldPath = filepath.Join(root, "agents", "main", "sessions", "cold", hash+".jsonl.zst")
						writeSourceFile(t, coldPath, string(data))
					}
					personalInsert(t, db, "session_transcript_cold_archives", map[string]any{"session_id": "root", "generation": "g", "archive_name": hash + ".jsonl.zst", "archive_sha256": hash, "storage": storage, "archive_blob": blob, "event_count": 1, "raw_bytes": len(event), "archive_bytes": len(data), "last_seq": 4})
					execSQL(t, db, "UPDATE transcript_events SET event_json=NULL,event_zstd=NULL WHERE session_id='root' AND seq=4")
				}
				db.Close()
				workerFile := filepath.Join(root, "agents", "worker", "agent", "openclaw-agent.sqlite")
				worker := personalTestDB(t, workerFile, openClawTestSchema)
				execSQL(t, worker, "UPDATE schema_meta SET agent_id='worker'")
				personalInsert(t, worker, "session_windows", map[string]any{"session_id": "work", "session_key": "agent:worker:work"})
				workerEvent := map[string]any{"type": "message", "id": "work-user", "message": map[string]any{"role": "user", "content": "Worker task"}}
				personalInsert(t, worker, "transcript_events", map[string]any{"session_id": "work", "seq": 0, "event_json": jsonText(workerEvent)})
				worker.Close()
				legacyFile := filepath.Join(root, "legacy.jsonl")
				legacy := func(text string) string {
					return jsonText(map[string]any{"type": "session", "id": "legacy"}) + "\n" + jsonText(map[string]any{"type": "message", "id": "legacy-user", "message": map[string]any{"role": "user", "content": text}}) + "\n"
				}
				writeSourceFile(t, legacyFile, legacy("Legacy task"))
				source := conformanceSource("openclaw", root)
				catalog, _ := testCatalog(t)
				config := captureTestConfig(t, source)
				ingest := func() IngestResult {
					if !retained {
						adapter, _ := MakeAdapter(source)
						return catalog.Ingest(adapter, nil)
					}
					if result := runCapture(t, config); !result.OK {
						t.Fatalf("capture blocked unrelated inputs: %+v", result)
					}
					target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
					input, err := leaseCaptureForIndex(t.Context(), target)
					if err != nil {
						t.Fatal(err)
					}
					defer input.Close()
					return catalog.Ingest(input.Adapter, nil)
				}
				a, _ := MakeAdapter(source)
				if result := ingest(); result.Error != nil {
					t.Fatalf("initial import: %+v", result)
				}
				brokenID := "main:root"
				if damage == "bad-reclaimed" {
					brokenID = "main:" + openClawArchivedID("reclaimed", "generation-one")
				}
				priorBrokenMessages := countRows(t, catalog, "SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id=?", brokenID)
				db = openWritableSQLite(t, file)
				switch damage {
				case "missing-cold":
					if err := os.Remove(coldPath); err != nil {
						t.Fatal(err)
					}
				case "bad-cold":
					execSQL(t, db, "UPDATE session_transcript_cold_archives SET archive_blob=x'00' WHERE session_id='root'")
				case "no-payload":
					execSQL(t, db, "UPDATE transcript_events SET event_json=NULL,event_zstd=NULL WHERE session_id='root' AND seq=4")
				case "bad-reclaimed":
					execSQL(t, db, "UPDATE session_transcript_archives SET archive_sha256='bad' WHERE session_id='reclaimed'")
				}
				child := map[string]any{"type": "message", "id": "c1", "message": map[string]any{"role": "user", "content": "Healthy sibling update"}}
				if _, err := db.Exec("UPDATE transcript_events SET event_json=? WHERE session_id='child' AND seq=1", jsonText(child)); err != nil {
					t.Fatal(err)
				}
				db.Close()
				worker = openWritableSQLite(t, workerFile)
				mapValue(workerEvent["message"])["content"] = "Healthy agent update"
				if _, err := worker.Exec("UPDATE transcript_events SET event_json=?", jsonText(workerEvent)); err != nil {
					t.Fatal(err)
				}
				worker.Close()
				writeSourceFile(t, legacyFile, legacy("Healthy legacy update"))
				a, _ = MakeAdapter(source)
				if _, err := a.Fingerprint(); err != nil {
					t.Fatalf("preflight blocked healthy history: %v", err)
				}
				if _, err := planCapture(t.Context(), source); err != nil {
					t.Fatalf("capture planning blocked healthy history: %v", err)
				}
				records := map[string]WorkspaceRecord{}
				err := a.Discover(func(r WorkspaceRecord) error { records[r.SourceID] = r; return nil })
				if err == nil {
					t.Fatal("damaged session was not reported")
				}
				for _, id := range []string{"main:child", "main:legacy", "worker:work", "main:" + openClawArchivedID("valid-archive", "g")} {
					if _, ok := records[id]; !ok {
						t.Errorf("damaged session hid %s", id)
					}
				}
				result := ingest()
				if result.Error == nil && !(retained && damage == "missing-cold") {
					t.Fatal("ingest did not report damaged history")
				}
				for _, text := range []string{"Healthy sibling update", "Healthy agent update", "Healthy legacy update"} {
					if n := countRows(t, catalog, "SELECT COUNT(*) FROM messages WHERE text=?", text); n != 1 {
						t.Errorf("healthy update %q indexed %d times: %+v", text, n, result)
					}
				}
				if n := countRows(t, catalog, "SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id=?", brokenID); n != priorBrokenMessages || n == 0 {
					t.Error("previously indexed history of the damaged session was changed")
				}
			})
		}
	}
}

func TestOpenClawColdInventoryDoesNotReadBlobs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "inventory.sqlite")
	db := openWritableSQLite(t, file)
	defer db.Close()
	// Selecting a payload column raises an overflow. Metadata enumeration
	// must work for both embedded archives and file-backed chunks anyway.
	execSQL(t, db, `CREATE TABLE parents(session_id TEXT,storage TEXT,archive_name TEXT);
CREATE TABLE chunks(session_id TEXT,archive_name TEXT);
CREATE VIEW session_transcript_cold_archives AS SELECT *,abs(-9223372036854775808) AS archive_blob FROM parents;
CREATE VIEW session_transcript_cold_archive_chunks AS SELECT *,abs(-9223372036854775808) AS archive_blob FROM chunks;`)
	plain := hashBytes([]byte("plain")) + ".jsonl.zst"
	chunk := hashBytes([]byte("chunk")) + ".jsonl.zst"
	personalInsert(t, db, "parents", map[string]any{"session_id": "plain", "storage": "file", "archive_name": plain})
	personalInsert(t, db, "parents", map[string]any{"session_id": "embedded", "storage": "sqlite", "archive_name": hashBytes([]byte("embedded")) + ".jsonl.zst"})
	personalInsert(t, db, "parents", map[string]any{"session_id": "chunked", "storage": "file", "archive_name": hashBytes([]byte("manifest")) + ".jsonl.zst"})
	personalInsert(t, db, "chunks", map[string]any{"session_id": "chunked", "archive_name": chunk})
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	paths, err := openClawColdPaths(tx, map[string]bool{"session_transcript_cold_archives": true, "session_transcript_cold_archive_chunks": true}, file)
	if err != nil {
		t.Fatalf("inventory read archive payloads: %v", err)
	}
	want := map[string]bool{filepath.Join(filepath.Dir(file), "cold", plain): true, filepath.Join(filepath.Dir(file), "cold", chunk): true}
	if len(paths) != len(want) {
		t.Fatalf("paths: %v", paths)
	}
	for _, path := range paths {
		if !want[path] {
			t.Fatalf("unexpected cold path %s", path)
		}
	}
}

func TestOpenClawLegacyIndexPerPass(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "sessions.json")
	writeIndex := func(one, two string) {
		writeSourceFile(t, index, jsonText(map[string]any{"one": map[string]any{"sessionId": "one", "label": one}, "two": map[string]any{"sessionId": "two", "label": two}}))
	}
	writeIndex("Original one", "Original two")
	for _, id := range []string{"one", "two"} {
		writeSourceFile(t, filepath.Join(root, id+".jsonl"), jsonText(map[string]any{"type": "session", "id": id})+"\n")
	}
	a := &personalAgentAdapter{baseAdapter{config: conformanceSource("openclaw", root), ctx: context.WithValue(t.Context(), openClawLegacyIndexKey{}, map[string]openClawLegacyIndex{})}}
	lookup := func(id string) map[string]any {
		meta, err := a.openClawLegacyMetadata(personalAgentInput{path: filepath.Join(root, id+".jsonl"), namespace: "main"}, id)
		if err != nil {
			t.Fatal(err)
		}
		return meta
	}
	if lookup("one")["label"] != "Original one" {
		t.Fatal("missing first index entry")
	}
	writeIndex("Updated one", "Updated two")
	if lookup("two")["label"] != "Original two" {
		t.Fatal("legacy index was decoded again within a pass")
	}
	lookup("one")["label"] = "caller edit"
	if lookup("one")["label"] != "Original one" {
		t.Fatal("one transcript mutated the shared index")
	}
	titles := map[string]string{}
	if err := a.Discover(func(r WorkspaceRecord) error { titles[r.SourceID] = r.Title; return nil }); err != nil {
		t.Fatal(err)
	}
	if titles["main:one"] != "Updated one" || titles["main:two"] != "Updated two" {
		t.Fatalf("later discovery reused stale index: %v", titles)
	}
}

func TestOpenClawResetTail(t *testing.T) {
	for _, kept := range []string{"", "done", "missing"} {
		t.Run("kept="+kept, func(t *testing.T) {
			root := t.TempDir()
			events := openClawTestEvents()
			reset := map[string]any{"type": "reset", "id": "reset", "parentId": "done", "reason": "reset"}
			if kept != "" {
				reset["firstKeptEntryId"] = kept
			}
			events = append(events, reset, map[string]any{"type": "message", "id": "new", "parentId": "reset", "message": map[string]any{"role": "user", "content": "Continue"}})
			var lines []string
			for _, event := range events {
				lines = append(lines, jsonText(event))
			}
			file := filepath.Join(root, "s.jsonl")
			writeSourceFile(t, file, strings.Join(lines, "\n"))
			a, _ := MakeAdapter(conformanceSource("openclaw", file))
			var record WorkspaceRecord
			err := a.Discover(func(r WorkspaceRecord) error { record = r; return nil })
			if kept == "missing" {
				if err == nil {
					t.Fatal("accepted missing retained tail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			selected := map[string]bool{}
			for _, m := range record.Conversations[0].Messages {
				selected[m.NativeID] = m.Selected
			}
			if selected["done"] != (kept != "") || selected["result"] || selected["user"] || !selected["new"] {
				t.Fatal(selected)
			}
			if record.Purpose != "Continue" || record.Title != "Continue" {
				t.Fatalf("discarded reset prompt supplied summary: purpose=%q title=%q", record.Purpose, record.Title)
			}
			if kept == "" && record.Outcome != "" {
				t.Fatalf("discarded reset reply supplied outcome %q", record.Outcome)
			}
		})
	}
}

func TestOpenClawCrossAgentParent(t *testing.T) {
	root := openClawFixture(t)
	file := filepath.Join(root, "agents", "helper", "agent", "openclaw-agent.sqlite")
	db := personalTestDB(t, file, openClawTestSchema)
	execSQL(t, db, "UPDATE schema_meta SET agent_id='helper'")
	personalInsert(t, db, "session_nodes", map[string]any{"session_key": "agent:helper:run", "current_session_id": "helper-session", "entry_json": "{}", "spawned_by": "agent:main:root"})
	personalInsert(t, db, "session_windows", map[string]any{"session_id": "helper-session", "session_key": "agent:helper:run"})
	personalInsert(t, db, "transcript_events", map[string]any{"session_id": "helper-session", "seq": 0, "event_json": jsonText(map[string]any{"type": "message", "id": "task", "message": map[string]any{"role": "user", "content": "Check the count"}})})
	db.Close()
	records := discoverAll(t, conformanceSource("openclaw", root))
	cv := records["helper:helper-session"].Conversations[0]
	if cv.ParentNativeID != "main:root" || cv.Messages[0].Role != agentRole {
		t.Fatalf("cross-agent parent: %+v", cv)
	}
	catalog, _ := testCatalog(t)
	a, _ := MakeAdapter(conformanceSource("openclaw", root))
	if result := catalog.Ingest(a, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	if n := countRows(t, catalog, `SELECT COUNT(*) FROM conversations c JOIN conversations p ON p.id=c.parent_id WHERE c.native_id='helper:helper-session' AND p.native_id='main:root'`); n != 1 {
		t.Fatal("cross-agent parent not linked on first import")
	}
}

func TestPersonalAgentDirectSourceCapture(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "session.jsonl")
			if kind == "openclaw" {
				var lines []string
				for _, event := range openClawTestEvents() {
					lines = append(lines, jsonText(event))
				}
				writeSourceFile(t, file, strings.Join(lines, "\n"))
				writeSourceFile(t, filepath.Join(root, "sessions.json"), `{"agent:main:root":{"sessionId":"root","label":"Captured legacy title"}}`)
			} else {
				root = hermesFixture(t)
				file = filepath.Join(root, "state.db")
			}
			source := conformanceSource(kind, file)
			config := captureTestConfig(t, source)
			if result := runCapture(t, config); !result.OK {
				t.Fatalf("direct source capture: %+v", result)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
			input, err := leaseCaptureForIndex(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			n := 0
			err = input.Adapter.Discover(func(r WorkspaceRecord) error {
				n++
				if kind == "openclaw" && r.Title != "Captured legacy title" {
					t.Fatal("legacy index not retained")
				}
				return nil
			})
			if err != nil || n == 0 {
				t.Fatalf("direct source replay: %d %v", n, err)
			}
		})
	}
}

func TestPersonalAgentUpdates(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			build := openClawFixture
			nativeID := "main:root"
			if kind == "hermes" {
				build = hermesFixture
				nativeID = "default:root"
			}
			root := build(t)
			file := filepath.Join(root, "agents", "main", "agent", "openclaw-agent.sqlite")
			if kind == "hermes" {
				file = filepath.Join(root, "state.db")
			}
			source := conformanceSource(kind, root)
			catalog, _ := testCatalog(t)
			a, _ := MakeAdapter(source)
			before, err := a.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if result := catalog.Ingest(a, nil); result.Error != nil {
				t.Fatal(result.Error)
			}
			if n := countRows(t, catalog, `SELECT COUNT(*) FROM conversations c JOIN conversations p ON p.id=c.parent_id WHERE p.native_id=?`, nativeID); n != 1 {
				t.Fatal("native parent was not linked on first import")
			}
			db := openWritableSQLite(t, file)
			if kind == "hermes" {
				execSQL(t, db, "UPDATE sessions SET input_tokens=input_tokens+10 WHERE id='root'", "UPDATE messages SET content='Updated reply' WHERE id=5")
			} else {
				event := openClawTestEvents()[6]
				mapValue(event["message"])["content"] = "Updated reply"
				if _, err := db.Exec("UPDATE transcript_events SET event_json=? WHERE session_id='root' AND seq=6", jsonText(event)); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			a, _ = MakeAdapter(source)
			after, err := a.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatal("native history update did not change fingerprint")
			}
			if result := catalog.Ingest(a, nil); result.Error != nil || result.SkippedUnchanged {
				t.Fatalf("reindex: %+v", result)
			}
			if n := countRows(t, catalog, `SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id=? AND m.text='Updated reply'`, nativeID); n != 1 {
				t.Fatalf("updated reply indexed %d times", n)
			}
			if kind == "hermes" && countRows(t, catalog, "SELECT COUNT(*) FROM model_requests") != 0 {
				t.Fatal("Hermes totals created individual model requests")
			}
		})
	}
}

func TestPersonalAgentDiscovery(t *testing.T) {
	home := probeHome(t)
	for _, kind := range []string{"openclaw", "hermes"} {
		build := openClawFixture
		variable := "OPENCLAW_STATE_DIR"
		if kind == "hermes" {
			build = hermesFixture
			variable = "HERMES_HOME"
		}
		root := build(t)
		dest := filepath.Join(home, "."+kind)
		if err := os.Rename(root, dest); err != nil {
			t.Fatal(err)
		}
		r := candidateNamed(t, probeSources(home, Config{}, nil), kind)
		if r.Status != "found" || r.Count == 0 {
			t.Fatalf("%s default probe: %+v", kind, r)
		}
		moved := filepath.Join(home, kind+"-work")
		if err := os.Rename(dest, moved); err != nil {
			t.Fatal(err)
		}
		t.Setenv(variable, moved)
		r = candidateNamed(t, probeSources(home, Config{}, nil), kind)
		if r.Status != "found" || r.Count == 0 {
			t.Fatalf("%s environment probe: %+v", kind, r)
		}
		if n := newPersonalAgentSources(home, Config{}, kind, "."+kind, variable); n != 1 {
			t.Fatalf("%s new sources: %d", kind, n)
		}
		config := Config{Sources: []SourceConfig{conformanceSource(kind, moved)}}
		if n := newPersonalAgentSources(home, config, kind, "."+kind, variable); n != 0 {
			t.Fatalf("%s configured source offered again", kind)
		}
	}
}

func TestHermesOlderSchemaAndDisplayCopies(t *testing.T) {
	for _, identity := range []string{"stored", "unbackfilled"} {
		t.Run(identity, func(t *testing.T) {
			root := hermesFixture(t)
			file := filepath.Join(root, "state.db")
			db := openWritableSQLite(t, file)
			// Older model-usage tables have no task column, and protected-tail
			// copies can have display identity without a UID.
			execSQL(t, db, "DELETE FROM session_model_usage WHERE task<>''", "ALTER TABLE session_model_usage DROP COLUMN task", "UPDATE messages SET display_identity=x'010203',message_uid=NULL WHERE id IN (1,4)")
			if identity == "unbackfilled" {
				execSQL(t, db, "UPDATE messages SET display_identity=NULL WHERE id IN (1,4)")
			}
			personalInsert(t, db, "messages", map[string]any{"id": 7, "session_id": "root", "role": "user", "content": "Model-facing wrapper", "display_kind": "hidden", "timestamp": 1793720003})
			personalInsert(t, db, "messages", map[string]any{"id": 8, "session_id": "root", "role": "user", "content": "Count the words", "timestamp": 1793720004})
			personalInsert(t, db, "messages", map[string]any{"id": 9, "session_id": "root", "role": "user", "content": "\x00json:" + jsonText([]any{map[string]any{"type": "text", "text": "Describe this"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0"}}}), "timestamp": 1793720005})
			personalInsert(t, db, "messages", map[string]any{"id": 10, "session_id": "root", "role": "user", "content": "Merged model-only user turns", "timestamp": 1793720006, "display_metadata": `{"model_only":true}`, "display_identity": []byte{1, 2, 3}})
			db.Close()
			record := discoverAll(t, conformanceSource("hermes", root))["default:root"]
			n := 0
			modelContext := false
			for _, m := range record.Conversations[0].Messages {
				if m.Text == "Count the words" {
					n++
				}
				if m.Role == "user" && strings.Contains(m.Text, "wrapper") {
					t.Error("hidden context treated as human input")
				}
				if m.Role == "user" && strings.Contains(m.Text, "Merged model-only") {
					t.Error("model-only context treated as human input")
				}
				if m.Role == "system" && m.Kind == "metadata" && strings.Contains(m.Text, "Merged model-only") {
					modelContext = true
				}
				if m.Text == "Count the words" && m.CreatedAt == iso(1793720000) && !m.Selected {
					t.Error("live display copy inherited removed selection")
				}
			}
			if n != 2 {
				t.Fatalf("native display copies and repeated human input: %d", n)
			}
			if !modelContext {
				t.Error("model-only row was not retained as model context")
			}
			if !strings.Contains(record.Purpose, "Count the words") {
				t.Fatal(record.Purpose)
			}
			found := false
			for _, m := range record.Conversations[0].Messages {
				if m.Text == "Describe this" && m.Kind == "message" {
					found = true
				}
			}
			if !found {
				t.Fatal("structured Hermes text was not decoded")
			}
			if usage := conversationTokenCounts(record.Conversations[0].Messages); usage["input_tokens"] != 230 {
				t.Fatalf("older usage schema: %v", usage)
			}
			// A newer rewind must also win over an older selected representative.
			db = openWritableSQLite(t, file)
			execSQL(t, db, "UPDATE messages SET active=0,compacted=0 WHERE id=4", "UPDATE messages SET active=1 WHERE id=1")
			db.Close()
			record = discoverAll(t, conformanceSource("hermes", root))["default:root"]
			for _, m := range record.Conversations[0].Messages {
				if m.Text == "Count the words" && m.CreatedAt == iso(1793720000) && m.Selected {
					t.Error("rewinding the latest generation revived an older copy")
				}
			}
		})
	}
}

func TestHermesLegacyMessageIDs(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(fmt.Sprintf("continuation=%t", continuation), func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "state.db")
			db := personalTestDB(t, file, strings.Replace(hermesTestSchema, "id INTEGER PRIMARY KEY,", "id TEXT PRIMARY KEY,", 1))
			personalInsert(t, db, "sessions", map[string]any{"id": "legacy", "source": "cli", "started_at": 1793720000})
			for _, row := range []map[string]any{
				{"id": "old-user-id", "session_id": "legacy", "role": "user", "content": "Question", "timestamp": 1793720000},
				{"id": "old-assistant-id", "session_id": "legacy", "role": "assistant", "content": "Answer", "timestamp": 1793720001},
			} {
				personalInsert(t, db, "messages", row)
			}
			db.Close()
			source := conformanceSource("hermes", root)
			cv := discoverAll(t, source)["default:legacy"].Conversations[0]
			if len(cv.Messages) != 2 || cv.Messages[0].NativeID != "legacy:old-user-id" || cv.Messages[1].NativeID != "legacy:old-assistant-id" {
				t.Fatalf("legacy IDs collapsed: %+v", cv.Messages)
			}
			if cv.Messages[0].EvidenceLocator == cv.Messages[1].EvidenceLocator {
				t.Fatal("legacy evidence locators collapsed")
			}
			catalog, _ := testCatalog(t)
			a, _ := MakeAdapter(source)
			if result := catalog.Ingest(a, nil); result.Error != nil {
				t.Fatal(result.Error)
			}
			if n := countRows(t, catalog, "SELECT COUNT(*) FROM messages"); n != 2 {
				t.Fatalf("indexed %d legacy messages, want 2", n)
			}
			// Both copies share the original timestamp. Lexical IDs do not give
			// insertion order; the later removed copy must control selection.
			db = openWritableSQLite(t, file)
			personalInsert(t, db, "messages", map[string]any{"id": "a-later-copy", "session_id": "legacy", "role": "user", "content": "Question", "timestamp": 1793720000, "active": 0, "compacted": 0})
			db.Close()
			cv = discoverAll(t, source)["default:legacy"].Conversations[0]
			if len(cv.Messages) != 2 || cv.Messages[0].Selected {
				t.Fatalf("legacy generation order: %+v", cv.Messages)
			}
			// Native insertion order must survive a clock regression, even when
			// continuation sessions themselves sort before their parent by timestamp.
			db = openWritableSQLite(t, file)
			segment := "legacy"
			if continuation {
				segment = "continuation"
				execSQL(t, db, "UPDATE sessions SET end_reason='compression' WHERE id='legacy'")
				personalInsert(t, db, "sessions", map[string]any{"id": segment, "parent_session_id": "legacy", "source": "cli", "started_at": 1793719998, "input_tokens": 10})
			}
			for _, row := range []map[string]any{
				{"id": "z-second-question", "session_id": segment, "role": "user", "content": "Second question", "timestamp": 1793719998},
				{"id": "b-second-answer", "session_id": segment, "role": "assistant", "content": "Second answer", "timestamp": 1793719999},
			} {
				personalInsert(t, db, "messages", row)
			}
			// Keep the first question selected for the purpose assertion.
			execSQL(t, db, "UPDATE messages SET active=1 WHERE id='a-later-copy'", "UPDATE sessions SET input_tokens=20 WHERE id='legacy'")
			db.Close()
			record := discoverAll(t, source)["default:legacy"]
			cv = record.Conversations[0]
			var transcript []string
			for _, m := range cv.Messages {
				if m.Kind == "message" {
					transcript = append(transcript, m.Text)
				}
			}
			if strings.Join(transcript, "|") != "Question|Answer|Second question|Second answer" || record.Purpose != "Question" || record.Outcome != "Second answer" {
				t.Fatalf("clock regression changed order or summaries: %v, purpose=%q outcome=%q", transcript, record.Purpose, record.Outcome)
			}
			if cv.Messages[2].CreatedAt != iso(1793719998) {
				t.Fatal("original timestamp was changed to enforce message order")
			}
			// A selective snapshot reassigns hidden rowids. Their relative order,
			// native IDs and selection must still replay identically.
			config := captureTestConfig(t, source)
			if result := runCapture(t, config); !result.OK {
				t.Fatalf("legacy capture: %+v", result)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
			input, err := leaseCaptureForIndex(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if err := input.Adapter.Discover(func(r WorkspaceRecord) error {
				if jsonText(r.Conversations[0]) != jsonText(cv) {
					t.Error("legacy identity or order changed in capture replay")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHermesCompressionBoundaries(t *testing.T) {
	for _, marker := range []string{"_branched_from", "_reset_from", "_delegate_from"} {
		t.Run(marker, func(t *testing.T) {
			root := hermesFixture(t)
			db := openWritableSQLite(t, filepath.Join(root, "state.db"))
			execSQL(t, db, "UPDATE sessions SET end_reason='compression' WHERE id='fork'")
			if _, err := db.Exec("UPDATE sessions SET model_config=? WHERE id='fork'", jsonText(map[string]any{marker: "root"})); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"carried-one", "carried-two"} {
				parent := "fork"
				start := 1793720010
				if id == "carried-two" {
					parent = "carried-one"
					start++
				}
				personalInsert(t, db, "sessions", map[string]any{"id": id, "parent_session_id": parent, "source": "cli", "model_config": jsonText(map[string]any{marker: "root"}), "started_at": start, "end_reason": "compression"})
			}
			personalInsert(t, db, "messages", map[string]any{"id": 7, "session_id": "fork", "role": "user", "content": "Separate task", "timestamp": 1793720009, "message_uid": "fork-user"})
			personalInsert(t, db, "messages", map[string]any{"id": 8, "session_id": "carried-two", "role": "user", "content": "Separate task", "timestamp": 1793720009, "message_uid": "fork-user"})
			db.Close()
			records := discoverAll(t, conformanceSource("hermes", root))
			if _, ok := records["default:carried-one"]; ok {
				t.Error("inherited marker split first continuation")
			}
			if _, ok := records["default:carried-two"]; ok {
				t.Error("inherited marker split later continuation")
			}
			fork, ok := records["default:fork"]
			if !ok || len(fork.Conversations[0].Messages) != 1 {
				t.Fatalf("actual boundary was not preserved: %+v", fork)
			}
		})
	}
}

func TestHermesToolOccurrences(t *testing.T) {
	for _, mode := range []string{"native-uids", "uid-list", "uid-list-pending", "legacy-list", "out-of-order-mixed", "same-timestamp", "result-backfill-pending", "legacy-provider-ids"} {
		t.Run(mode, func(t *testing.T) {
			root := hermesFixture(t)
			db := openWritableSQLite(t, filepath.Join(root, "state.db"))
			execSQL(t, db, `UPDATE messages SET tool_call_uids='{"call1":"occurrence-one"}' WHERE id=2`, "UPDATE messages SET tool_call_uid='occurrence-one' WHERE id=3")
			calls := []any{map[string]any{"id": "call1", "type": "function", "function": map[string]any{"name": "terminal", "arguments": `{"command":"wc -w second.txt"}`}}}
			uids := map[string]any{"call1": "occurrence-two"}
			if strings.HasPrefix(mode, "uid-list") || mode == "legacy-list" || mode == "out-of-order-mixed" {
				calls = append(calls, map[string]any{"id": "call1", "type": "function", "function": map[string]any{"name": "terminal", "arguments": `{"command":"wc -w third.txt"}`}})
				uids["call1"] = []any{"occurrence-two", "occurrence-three"}
			}
			secondAt := 1793720010
			if mode == "same-timestamp" {
				secondAt = 1793720001
			}
			personalInsert(t, db, "messages", map[string]any{"id": 7, "session_id": "continuation", "role": "assistant", "content": "Check again", "tool_calls": jsonText(calls), "tool_call_uids": jsonText(uids), "timestamp": secondAt})
			personalInsert(t, db, "messages", map[string]any{"id": 8, "session_id": "continuation", "role": "tool", "tool_call_id": "call1", "tool_call_uid": "occurrence-two", "tool_name": "terminal", "content": "100 second.txt", "timestamp": 1793720011})
			if strings.HasPrefix(mode, "uid-list") || mode == "legacy-list" || mode == "out-of-order-mixed" {
				personalInsert(t, db, "messages", map[string]any{"id": 9, "session_id": "continuation", "role": "tool", "tool_call_id": "call1", "tool_call_uid": "occurrence-three", "tool_name": "terminal", "content": "150 third.txt", "timestamp": 1793720012})
			}
			if mode == "out-of-order-mixed" {
				// Resolve the later call explicitly, then consume the remaining
				// earlier occurrence through the legacy provider-ID fallback.
				execSQL(t, db, "UPDATE messages SET tool_call_uid='occurrence-three',content='150 third.txt' WHERE id=8", "UPDATE messages SET tool_call_uid=NULL,content='100 second.txt' WHERE id=9")
			}
			if mode == "result-backfill-pending" || mode == "uid-list-pending" {
				execSQL(t, db, "UPDATE messages SET tool_call_uid=NULL")
			}
			if mode == "legacy-provider-ids" || mode == "legacy-list" {
				execSQL(t, db, "UPDATE messages SET tool_call_uid=NULL,tool_call_uids=NULL")
			}
			db.Close()
			cv := discoverAll(t, conformanceSource("hermes", root))["default:root"].Conversations[0]
			_, ledger := buildToolLedger(cv.Messages, cv.Model)
			if len(ledger) != 1+len(calls) {
				t.Fatalf("imported %d tool executions, want %d", len(ledger), 1+len(calls))
			}
			byID := map[string]MessageRecord{}
			for _, m := range cv.Messages {
				byID[m.NativeID] = m
			}
			for index, call := range ledger {
				if mode == "legacy-provider-ids" || mode == "legacy-list" {
					result := []string{"74 story.txt", "100 second.txt", "150 third.txt"}[index]
					if !strings.Contains(byID[call.ResultNativeID].Text, result) {
						t.Fatalf("legacy call lost its result: %+v", call)
					}
					continue
				}
				switch call.CallID {
				case "occurrence-one":
					if !strings.Contains(byID[call.ResultNativeID].Text, "74 story.txt") {
						t.Fatal("first call lost its result")
					}
				case "occurrence-two":
					if !strings.Contains(byID[call.ResultNativeID].Text, "100 second.txt") {
						t.Fatal("second call lost its result")
					}
				case "occurrence-three":
					if !strings.Contains(byID[call.ResultNativeID].Text, "150 third.txt") {
						t.Fatal("third call lost its result")
					}
				default:
					t.Fatalf("lost native occurrence UID: %+v", call)
				}
			}
			thinking, reply := -1, -1
			for i, m := range cv.Messages {
				if m.NativeID == "root:2:reasoning" {
					thinking = i
				}
				if m.NativeID == "root:2" {
					reply = i
				}
			}
			if thinking < 0 || reply < 0 || thinking > reply {
				t.Fatal("reasoning was emitted after the reply")
			}
		})
	}
}

func TestOpenClawColdVerification(t *testing.T) {
	text := jsonText(map[string]any{"type": "message", "id": "m", "message": map[string]any{"role": "user", "content": "Hi"}})
	data := personalZstd(t, []byte(jsonText(map[string]any{"kind": "header", "version": 1, "sessionId": "s", "generation": "g"})+"\n"+jsonText(map[string]any{"kind": "event", "row": map[string]any{"seq": 1, "event_json": text}})+"\n"))
	for _, field := range []string{"archive_sha256", "archive_bytes", "generation", "session_id", "event_count", "raw_bytes", "last_seq", "first_seq"} {
		t.Run(field, func(t *testing.T) {
			archive := map[string]any{"storage": "sqlite", "archive_blob": data, "archive_sha256": hashBytes(data), "archive_bytes": len(data), "generation": "g", "session_id": "s", "event_count": 1, "raw_bytes": len(text), "last_seq": 1}
			switch field {
			case "archive_sha256", "generation", "session_id":
				archive[field] = "wrong"
			default:
				archive[field] = 99
			}
			a := &personalAgentAdapter{baseAdapter{config: conformanceSource("openclaw", t.TempDir())}}
			if _, _, err := a.openClawColdEvents("unused.sqlite", archive); err == nil {
				t.Fatalf("accepted mismatched %s", field)
			}
		})
	}
	if _, err := openClawColdPath("history.sqlite", "../../outside"); err == nil {
		t.Fatal("accepted archive path traversal")
	}
}

func TestOpenClawLegacyCompressedProbe(t *testing.T) {
	root := t.TempDir()
	events := openClawTestEvents()
	var lines []string
	for _, event := range events {
		lines = append(lines, jsonText(event))
	}
	file := filepath.Join(root, "old.jsonl.zst")
	writeSourceFile(t, file, string(personalZstd(t, []byte(strings.Join(lines, "\n")))))
	a := &personalAgentAdapter{baseAdapter{config: conformanceSource("openclaw", file)}}
	inputs, err := a.inputs()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := a.personalProbeStats(inputs, time.Now().Add(time.Second))
	if err != nil || stats.sessions != 1 {
		t.Fatalf("compressed probe: %+v %v", stats, err)
	}
	if got := discoverAll(t, a.config)["main:root"].Outcome; got != "There are 74 words." {
		t.Fatal(got)
	}
}
