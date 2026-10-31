package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// An import keeps only events it can place: the message must exist with the
// text it was scored on, its ranges must fall on that text's UTF-8 boundaries,
// and its family must be one the product reports. Every message read counts
// toward the denominator; the datasets show events at or above the threshold.
func TestImportAgentEventsChecksMessagesAndRanges(t *testing.T) {
	catalog, _ := testCatalog(t)
	text := "Hmm, the endpoint doesn’t support pagination. I'll work around that by fetching one date range at a time."
	other := "Updated the parser."
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-10-21T00:00:00.000Z')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','claude','local','c')`,
	)
	tx := seedTx(t, catalog.DB)
	for id, body := range map[string]string{"m1": text, "m2": other} {
		if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES(?,?,?,?,?,?,?,?)`,
			id, "c", id, "assistant", "message", body, "2026-10-21T15:00:00.000Z", id); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	sum := func(s string) string { digest := sha256.Sum256([]byte(s)); return hex.EncodeToString(digest[:]) }
	first := len("Hmm, the endpoint doesn’t support pagination.")
	curly := len("Hmm, the endpoint doesn") + 1 // inside the three-byte ’
	lines := []map[string]any{
		{"kind": "header", "format": "pharos-agent-events-v1", "model": "test-model", "lexicon": "test-lexicon"},
		{"kind": "message", "message_id": "m1", "message_sha256": sum(text), "source": "reply", "candidates": 3},
		{"kind": "message", "message_id": "m2", "message_sha256": sum("Updated the parser!"), "source": "thinking", "candidates": 0},
		{"kind": "message", "message_id": "missing", "message_sha256": sum("x"), "source": "reply", "candidates": 1},
		{"kind": "event", "message_id": "m1", "message_sha256": sum(text), "family": "workaround", "probability": 0.92,
			"sentence": [2]int{first + 1, len(text)}, "subject": [][2]int{{0, first}}, "response": [][2]int{{first + 1, len(text)}}},
		{"kind": "event", "message_id": "m1", "message_sha256": sum(text), "family": "obstacle", "probability": 0.4,
			"sentence": [2]int{0, first}, "subject": [][2]int{{0, first}}, "response": [][2]int{}},
		{"kind": "event", "message_id": "m1", "message_sha256": sum(text), "family": "surprise", "probability": 0.9,
			"sentence": [2]int{0, curly}, "subject": [][2]int{}, "response": [][2]int{}},
		{"kind": "event", "message_id": "m1", "message_sha256": sum(text), "family": "risky_action", "probability": 0.99,
			"sentence": [2]int{0, first}, "subject": [][2]int{}, "response": [][2]int{}},
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, line := range lines {
		if err := encoder.Encode(line); err != nil {
			t.Fatal(err)
		}
	}
	file.Close()

	result, err := catalog.ImportAgentEvents(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages != 1 || result.Events != 2 || result.EventsAtCutoff != 1 || result.SkippedChanged != 1 || result.SkippedMissing != 1 ||
		result.SkippedRange != 1 || result.SkippedFamily != 1 {
		t.Fatalf("import = %+v", result)
	}
	var sentence, subject, day string
	var events int
	if err := catalog.DB.QueryRow(`SELECT sentence, subject, day FROM agent_events WHERE family='workaround'`).Scan(&sentence, &subject, &day); err != nil {
		t.Fatal(err)
	}
	if sentence != "I'll work around that by fetching one date range at a time." || subject != "Hmm, the endpoint doesn’t support pagination." || day == "" {
		t.Fatalf("event = %q / %q / %q", sentence, subject, day)
	}
	if err := catalog.DB.QueryRow(`SELECT events FROM agent_event_messages WHERE message_id='m1'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("message events = %d, %v", events, err)
	}

	// A second import of the same message replaces its events.
	lines = lines[:2]
	lines = append(lines, map[string]any{"kind": "event", "message_id": "m1", "message_sha256": sum(text), "family": "obstacle", "probability": 0.8,
		"sentence": [2]int{0, first}, "subject": [][2]int{{0, first}}, "response": [][2]int{}})
	file, _ = os.Create(path)
	encoder = json.NewEncoder(file)
	for _, line := range lines {
		encoder.Encode(line)
	}
	file.Close()
	if _, err := catalog.ImportAgentEvents(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	var families string
	if err := catalog.DB.QueryRow(`SELECT group_concat(family) FROM agent_events`).Scan(&families); err != nil || families != "obstacle" {
		t.Fatalf("families after reimport = %q, %v", families, err)
	}
}
