package archive

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func incrementalFixture(count int) WorkspaceRecord {
	conversation := ConversationRecord{NativeID: "session", Provider: "codex", Account: "local", Origin: "/fixture/rollout.jsonl", Coverage: "fixture", Model: "gpt-5", StartedAt: "2026-10-01T10:00:00Z"}
	for index := range count {
		conversation.Messages = append(conversation.Messages, MessageRecord{NativeID: fmt.Sprintf("message-%d", index), Role: "user", Kind: "message", Text: fmt.Sprintf("Searchable fixture message %d about incremental rebuilding and catalog verification.", index), Selected: true})
	}
	return WorkspaceRecord{SourceID: "session", SourceKind: "codex", Account: "local", Title: "Incremental fixture", ActivityAt: "2026-10-01T10:00:00Z", Conversations: []ConversationRecord{conversation}}
}

func writeIncrementalFixture(t testing.TB, catalog *Catalog, record WorkspaceRecord) int64 {
	t.Helper()
	tx, err := catalog.beginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before, after int64
	if err := tx.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestCopy(tx, record, false, currentHost().ID, "fixture", false); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return after - before
}

func writerSearchSnapshot(t testing.TB, db *sql.DB) string {
	t.Helper()
	rows, err := queryMaps(db, `SELECT m.native_id,m.role,m.kind,m.model,m.text,m.raw_text,m.source_order,m.created_at,m.sender,m.selected,
		f.text AS search_text,tr.text AS substring_text FROM messages m
		LEFT JOIN message_fts_rows r ON r.message_id=m.id LEFT JOIN messages_fts f ON f.rowid=r.fts_rowid
		LEFT JOIN messages_trigram tr ON tr.rowid=r.fts_rowid ORDER BY m.native_id`)
	if err != nil {
		t.Fatal(err)
	}
	return jsonText(rows)
}

func TestIncrementalWriterSearchParity(t *testing.T) {
	for _, scenario := range []string{"append", "text", "metadata", "kind", "usage", "prune", "order", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			catalog, _ := testCatalog(t)
			reference, _ := testCatalog(t)
			record := incrementalFixture(12)
			writeIncrementalFixture(t, catalog, record)
			var oldRowID int64
			catalog.DB.QueryRow(`SELECT r.fts_rowid FROM message_fts_rows r JOIN messages m ON m.id=r.message_id WHERE m.native_id='message-1'`).Scan(&oldRowID)
			messages := record.Conversations[0].Messages
			switch scenario {
			case "append":
				messages = append(messages, MessageRecord{NativeID: "new", Role: "assistant", Kind: "message", Text: "new zebraquartz content", Selected: true})
			case "text":
				messages[0].Text = "corrected zebraquartz content"
			case "metadata":
				messages[0].Model, messages[0].Sender, messages[0].Selected = "new-model", "agent:other", false
			case "kind":
				messages[0].Kind = "tool_result"
			case "usage":
				messages[0].RawText = `{"usage":{"input_tokens":42,"output_tokens":7}}`
			case "prune":
				messages = messages[1:]
			case "order":
				messages[0], messages[11] = messages[11], messages[0]
			case "empty":
				messages[0].Text = ""
			}
			record.Conversations[0].Messages = messages
			writeIncrementalFixture(t, catalog, record)
			writeIncrementalFixture(t, reference, record)
			if actual, expected := writerSearchSnapshot(t, catalog.DB), writerSearchSnapshot(t, reference.DB); actual != expected {
				t.Fatalf("incremental search differs from full ingest\nactual %s\nexpected %s", actual, expected)
			}
			var rowID, activities int64
			catalog.DB.QueryRow(`SELECT r.fts_rowid FROM message_fts_rows r JOIN messages m ON m.id=r.message_id WHERE m.native_id='message-1'`).Scan(&rowID)
			if rowID != oldRowID {
				t.Fatal("an unchanged message's search row was rewritten")
			}
			catalog.DB.QueryRow("SELECT COUNT(*) FROM activity_events").Scan(&activities)
			if activities != 1 {
				t.Fatalf("duplicate activity: %d", activities)
			}
		})
	}
}

func TestUpsertMessageCompleteNoop(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := incrementalFixture(1)
	writeIncrementalFixture(t, catalog, record)
	tx, err := catalog.beginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var conversation string
	tx.QueryRow("SELECT id FROM conversations").Scan(&conversation)
	var before, after int64
	tx.QueryRow("SELECT total_changes()").Scan(&before)
	if _, changed, err := upsertMessage(tx, conversation, record.Conversations[0].Messages[0]); err != nil || changed {
		t.Fatalf("noop changed=%v error=%v", changed, err)
	}
	tx.QueryRow("SELECT total_changes()").Scan(&after)
	if after != before {
		t.Fatalf("noop wrote %d rows", after-before)
	}
}

func BenchmarkIncrementalWriter(b *testing.B) {
	for _, workload := range []string{"initial", "unchanged", "append", "metadata"} {
		b.Run(workload, func(b *testing.B) {
			catalog, err := OpenCatalog(filepath.Join(incrementalBenchmarkDir(b), "catalog.sqlite3"))
			if err != nil {
				b.Fatal(err)
			}
			defer catalog.Close()
			record := incrementalFixture(2000)
			if workload != "initial" {
				writeIncrementalFixture(b, catalog, record)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var writes int64
			for index := range b.N {
				if workload == "initial" {
					record.SourceID = fmt.Sprintf("session-%d", index)
					record.Conversations[0].NativeID = record.SourceID
				} else if workload == "append" {
					record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{NativeID: fmt.Sprintf("append-%d", index), Role: "assistant", Kind: "message", Text: "appended incremental content", Selected: true})
				} else if workload == "metadata" {
					record.Title = fmt.Sprintf("title %d", index)
				}
				writes += writeIncrementalFixture(b, catalog, record)
			}
			b.ReportMetric(float64(writes)/float64(b.N), "rows/op")
		})
	}
}

func incrementalBenchmarkDir(b *testing.B) string {
	b.Helper()
	root := os.Getenv("PHAROS_BENCH_ROOT")
	if root == "" {
		return b.TempDir()
	}
	directory, err := os.MkdirTemp(root, ".pharos-incremental-benchmark-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

func TestIncrementalWriterRowBudget(t *testing.T) {
	catalog, _ := testCatalog(t)
	record := incrementalFixture(2000)
	writeIncrementalFixture(t, catalog, record)
	if rows := writeIncrementalFixture(t, catalog, record); rows > 40 {
		t.Fatalf("unchanged writer exceeded 40-row budget: %d", rows)
	}
	record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{NativeID: "appended", Role: "user", Kind: "message", Text: "appended searchable message", Selected: true})
	if rows := writeIncrementalFixture(t, catalog, record); rows > 80 {
		t.Fatalf("single append exceeded 80-row budget: %d", rows)
	}
}
