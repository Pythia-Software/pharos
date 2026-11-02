package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

	// The live reader gets the message's events at the threshold, with ranges
	// in UTF-16 code units (the ’ is three bytes and one unit); a shared
	// export gets none.
	for _, clip := range []bool{true, false} {
		conversation, err := catalog.workConversation("w", "c", clip)
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		for _, message := range conversation["messages"].([]map[string]any) {
			if message["id"] == "m1" && message["agent_events"] != nil {
				got = message["agent_events"].([]map[string]any)
			}
		}
		if !clip {
			if got != nil || conversation["agent_events"] != nil {
				t.Fatalf("a shared export carries events: %v", got)
			}
			continue
		}
		start := len([]rune(text[:first+1])) // every character here is one UTF-16 unit
		end := len([]rune(text))
		if len(got) != 1 || got[0]["kind"] != "workaround" || got[0]["sentence"] != [2]int{start, end} ||
			!reflect.DeepEqual(got[0]["subject"], [][2]int{{0, start - 1}}) || got[0]["group"] != "Choices you may not see" {
			t.Fatalf("reader events = %v", got)
		}
		if summary, _ := conversation["agent_events"].(map[string]any); summary["messages_read"] != 1 || summary["messages_with_events"] != 1 {
			t.Fatalf("reader summary = %v", conversation["agent_events"])
		}
	}

	// A second import of the same message replaces its events.
	lines = lines[:2]
	lines = append(lines, map[string]any{"kind": "event", "message_id": "m1", "message_sha256": sum(text), "family": "obstacle", "probability": 0.8,
		"sentence": [2]int{0, first}, "subject": [][2]int{{0, first}}, "response": [][2]int{}})
	// Blank lines anywhere, the end included, are not lines.
	write := func(lines []map[string]any) {
		t.Helper()
		var body strings.Builder
		for index, line := range lines {
			data, _ := json.Marshal(line)
			body.Write(data)
			body.WriteString("\n")
			if index == 0 {
				body.WriteString("\n  \n")
			}
		}
		body.WriteString("\n\n")
		if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(lines)
	if _, err := catalog.ImportAgentEvents(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	var families string
	if err := catalog.DB.QueryRow(`SELECT group_concat(family) FROM agent_events`).Scan(&families); err != nil || families != "obstacle" {
		t.Fatalf("families after reimport = %q, %v", families, err)
	}

	// Once the message's text changes, importing it again (scored on the old
	// text) leaves none of its earlier events or its row: they described text
	// it no longer has.
	execTx(t, catalog.DB, `UPDATE messages SET text='Rewritten entirely.' WHERE id='m1'`)
	write(lines)
	changed, err := catalog.ImportAgentEvents(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var left int
	if err := catalog.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_events WHERE message_id='m1')+(SELECT COUNT(*) FROM agent_event_messages WHERE message_id='m1')`).Scan(&left); err != nil ||
		left != 0 || changed.SkippedChanged != 1 {
		t.Fatalf("after the text changed: %d rows left, import %+v (%v)", left, changed, err)
	}
}

// agentEventFamilies is the one definition of the reported kinds. The
// schemas' kind and group options, and testdata/agent-event-kinds.json, which
// the dashboard's catalog and the reader's colours are checked against
// (web/tests/unit/dashboards/agent-events.test.mjs), must follow it.
// PHAROS_UPDATE_AGENT_EVENT_KINDS=1 rewrites the fixture.
func TestAgentEventKindsAgreeEverywhere(t *testing.T) {
	type kind struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Group string `json:"group"`
	}
	want := struct {
		Groups []string `json:"groups"`
		Kinds  []kind   `json:"kinds"`
	}{}
	for _, family := range agentEventFamilies {
		if !slices.Contains(want.Groups, family.Group) {
			want.Groups = append(want.Groups, family.Group)
		}
		want.Kinds = append(want.Kinds, kind{family.Key, family.Label, family.Group})
	}
	data, _ := json.MarshalIndent(want, "", "  ")
	data = append(data, '\n')
	path := filepath.Join("..", "..", "testdata", "agent-event-kinds.json")
	if os.Getenv("PHAROS_UPDATE_AGENT_EVENT_KINDS") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if stored, err := os.ReadFile(path); err != nil || string(stored) != string(data) {
		t.Fatalf("%s is not agentEventFamilies; run with PHAROS_UPDATE_AGENT_EVENT_KINDS=1 (%v)", path, err)
	}
	var schema struct {
		Fields []struct {
			Name   string `json:"name"`
			Filter struct {
				Values struct {
					Options []struct{ Value, Label string } `json:"options"`
				} `json:"values"`
			} `json:"filter"`
		} `json:"fields"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "agent_events.schema.json"))
	if err != nil || json.Unmarshal(raw, &schema) != nil {
		t.Fatalf("read the schema: %v", err)
	}
	for _, field := range schema.Fields {
		var got, expected []string
		for _, option := range field.Filter.Values.Options {
			got = append(got, option.Value+"="+option.Label)
		}
		switch field.Name {
		case "family":
			for _, k := range want.Kinds {
				expected = append(expected, k.Key+"="+k.Label)
			}
		case "family_group":
			for _, group := range want.Groups {
				expected = append(expected, group+"="+group)
			}
		default:
			continue
		}
		if !slices.Equal(got, expected) {
			t.Errorf("schema %s options %v, want %v", field.Name, got, expected)
		}
	}
}
