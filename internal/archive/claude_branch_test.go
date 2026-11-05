package archive

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// claudeEntry is one synthetic Claude Code transcript line.
type claudeEntry map[string]any

func claudeTurn(session, uuid, parent, kind string, content any, extra claudeEntry) string {
	entry := claudeEntry{"sessionId": session, "uuid": uuid, "type": kind, "timestamp": "2026-09-24T17:00:00Z", "cwd": "/p"}
	if parent != "" {
		entry["parentUuid"] = parent
	}
	message := map[string]any{"role": kind, "content": content}
	for key, value := range extra {
		if key == "message" {
			for k, v := range value.(map[string]any) {
				message[k] = v
			}
			continue
		}
		entry[key] = value
	}
	if kind == "user" || kind == "assistant" {
		entry["message"] = message
	}
	return jsonText(entry)
}

func claudeReply(session, uuid, parent, id, request string, content any, usage map[string]any) string {
	extra := claudeEntry{"message": map[string]any{"id": id, "model": "claude-test", "usage": usage}}
	if request != "" {
		extra["requestId"] = request
	}
	return claudeTurn(session, uuid, parent, "assistant", content, extra)
}

func skillNote(uuid, parent string) string {
	return jsonText(claudeEntry{"sessionId": "s", "uuid": uuid, "parentUuid": parent, "type": "attachment", "timestamp": "2026-09-24T17:00:00Z",
		"attachment": map[string]any{"type": "invoked_skills", "skills": []any{"review"}}})
}

func toolUse(id string) []any {
	return []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "ls"}}}
}

func toolResult(id string) []any {
	return []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "listing"}}
}

func text(value string) []any { return []any{map[string]any{"type": "text", "text": value}} }

func usage(input, output int) map[string]any {
	return map[string]any{"input_tokens": input, "output_tokens": output}
}

func entrySelected(t *testing.T, messages []MessageRecord) map[string]bool {
	t.Helper()
	selected := map[string]bool{}
	for _, message := range messages {
		entry, _, _ := strings.Cut(message.NativeID, ":")
		if prior, ok := selected[entry]; ok && prior != message.Selected {
			t.Fatalf("entry %s has selected and unselected records: %#v", entry, messages)
		}
		selected[entry] = message.Selected
	}
	return selected
}

func TestClaudeSelectsTheActiveBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		claudeTurn("s", "p1", "", "user", "List the files", nil),
		// One reply over two lines; the conversation goes on from its tool
		// call's result, under the line of the call, past the reply's other line.
		claudeReply("s", "a1a", "p1", "msg_1", "req_1", toolUse("t1"), usage(10, 1)),
		claudeReply("s", "a1b", "a1a", "msg_1", "req_1", text("Listing"), usage(10, 1)),
		claudeTurn("s", "r1", "a1a", "user", toolResult("t1"), nil),
		// Parallel tool calls, one on a line without a message ID; the second
		// result is written under the first.
		claudeReply("s", "a5a", "r1", "msg_5", "req_5", toolUse("t2"), usage(10, 1)),
		claudeReply("s", "a5b", "a5a", "", "", toolUse("t3"), nil),
		claudeTurn("s", "r2", "a5a", "user", toolResult("t2"), nil),
		claudeTurn("s", "r3", "r2", "user", toolResult("t3"), nil),
		// Parallel tool calls whose results are each under their call's line;
		// the conversation goes on from the first.
		claudeReply("s", "a6a", "r3", "msg_6", "req_6", toolUse("t4"), usage(10, 1)),
		claudeReply("s", "a6b", "a6a", "msg_6", "req_6", toolUse("t5"), usage(10, 1)),
		claudeTurn("s", "r4", "a6a", "user", toolResult("t4"), nil),
		claudeTurn("s", "r5", "a6b", "user", toolResult("t5"), nil),
		skillNote("k1", "a1b"),
		claudeReply("s", "a2", "r4", "msg_2", "req_2", text("Two files"), usage(20, 2)),
		claudeTurn("s", "u2", "a2", "user", "Delete them", nil),
		claudeReply("s", "a3", "u2", "msg_3", "req_3", text("Deleted"), usage(30, 3)),
		skillNote("k2", "a3"),
		// The user rewound to a2 and asked something else, after a compaction
		// that starts a new chain.
		jsonText(claudeEntry{"sessionId": "s", "uuid": "c1", "type": "system", "subtype": "compact_boundary", "parentUuid": nil, "logicalParentUuid": "a2", "timestamp": "2026-09-24T17:00:00Z"}),
		claudeTurn("s", "u3", "c1", "user", "Count them", nil),
		claudeReply("s", "a4", "u3", "msg_4", "req_4", text("2"), usage(40, 4)),
	}
	messages := parseJSONL(t, "claude", path, lines...)
	selected := entrySelected(t, messages)
	for _, entry := range []string{"p1", "a1a", "a1b", "r1", "a5a", "a5b", "r2", "r3", "a6a", "a6b", "r4", "r5", "k1", "a2", "u3", "a4"} {
		if !selected[entry] {
			t.Errorf("%s is on the active branch but not selected", entry)
		}
	}
	for _, entry := range []string{"u2", "a3", "k2"} {
		if on, ok := selected[entry]; !ok || on {
			t.Errorf("abandoned %s selected=%v present=%v", entry, on, ok)
		}
	}
	// An abandoned reply was still sent: its usage counts.
	total := conversationTokenCounts(messages)["total_tokens"]
	if total != 11+11+11+22+33+44 {
		t.Fatalf("total tokens = %v, want %d", total, 11+11+11+22+33+44)
	}
}

func TestClaudeCountsAdvisorIterationsUnderTheirModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	withAdvisor := map[string]any{"input_tokens": 100, "output_tokens": 10, "iterations": []any{
		map[string]any{"type": "message", "input_tokens": 100, "output_tokens": 10},
		map[string]any{"type": "advisor_message", "model": "claude-advisor", "input_tokens": 50, "output_tokens": 5},
	}}
	messages := parseJSONL(t, "claude", path,
		claudeTurn("s", "p1", "", "user", "Plan it", nil),
		claudeReply("s", "a1", "p1", "msg_1", "req_1", text("Planned"), withAdvisor))
	byModel := map[string]float64{}
	requests, _ := buildToolLedger(messages, "claude-test")
	for _, request := range requests {
		byModel[request.Model] += request.Counts["total_tokens"]
	}
	if byModel["claude-test"] != 110 || byModel["claude-advisor"] != 55 || len(byModel) != 2 {
		t.Fatalf("tokens by model = %v, want claude-test 110 and claude-advisor 55", byModel)
	}
}

// claudeSessionFiles writes a Claude projects folder holding the given
// transcripts, by path below the folder.
func claudeSessionFiles(t *testing.T, files map[string][]string) SourceConfig {
	t.Helper()
	root := filepath.Join(t.TempDir(), "claude")
	for name, lines := range files {
		writeSourceFile(t, filepath.Join(root, "-p", name), strings.Join(lines, "\n")+"\n")
	}
	return SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local", Enabled: true}
}

func libraryTokens(t *testing.T, catalog *Catalog) (sessions, ledger, metrics, requests int64) {
	t.Helper()
	for query, into := range map[string]*int64{
		"SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions":                                         &sessions,
		"SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage":                                    &ledger,
		"SELECT COALESCE(SUM(value),0) FROM metrics WHERE name='total_tokens' AND conversation_id IS NULL": &metrics,
		"SELECT COALESCE(SUM(total_tokens),0) FROM model_requests":                                         &requests,
	} {
		if err := catalog.DB.QueryRow(query).Scan(into); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func wantLibraryTokens(t *testing.T, catalog *Catalog, want int64) {
	t.Helper()
	sessions, ledger, metrics, requests := libraryTokens(t, catalog)
	if sessions != want || ledger != want || metrics != want || requests != want {
		t.Fatalf("sessions=%d ledger=%d metrics=%d requests=%d, want %d", sessions, ledger, metrics, requests, want)
	}
}

func TestClaudeForkedSubagentCountsItsParentsRepliesOnce(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	parent := []string{
		claudeTurn("s1", "p1", "", "user", "Investigate", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Looking"), usage(100, 10)),
	}
	// A forked subagent starts with a copy of its parent's conversation; a
	// sidechain replays it under new request IDs.
	fork := []string{
		claudeReply("s1", "a1", "p1", "msg_1", "req_1b", text("Looking"), usage(100, 10)),
		claudeReply("s1", "f1", "a1", "msg_2", "req_2", text("Found it"), usage(200, 20)),
	}
	source := claudeSessionFiles(t, map[string][]string{"s1.jsonl": parent, "s1/subagents/agent-f.jsonl": withSidechain(fork)})
	ingestSource(t, catalog, source)
	wantLibraryTokens(t, catalog, 110+220)
}

func withSidechain(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.Replace(line, `{`, `{"isSidechain":true,`, 1)
	}
	return out
}

func TestClaudeResumedSessionCountsCopiedRepliesOnce(t *testing.T) {
	advised := map[string]any{"input_tokens": 100, "output_tokens": 10, "iterations": []any{
		map[string]any{"type": "advisor_message", "model": "claude-advisor", "input_tokens": 40, "output_tokens": 4},
	}}
	original := []string{
		claudeTurn("s1", "p1", "", "user", "Investigate", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Looking"), advised),
	}
	// Claude Code copies the replies into the resumed session's file, under
	// that session's ID, with nothing saying which is the original.
	resumed := []string{
		strings.ReplaceAll(original[0], `"sessionId":"s1"`, `"sessionId":"s2"`),
		strings.ReplaceAll(original[1], `"sessionId":"s1"`, `"sessionId":"s2"`),
		claudeTurn("s2", "p2", "a1", "user", "And then?", nil),
		claudeReply("s2", "a2", "p2", "msg_2", "req_2", text("Fixed"), usage(200, 20)),
	}
	resumed[0] = strings.Replace(resumed[0], "2026-09-24T17:00:00Z", "2026-09-24T18:00:00Z", 1)
	// A gateway that names every reply alike, without a request ID, is not
	// matched across sessions.
	gateway := func(session string) []string {
		return []string{
			claudeTurn(session, "g-"+session, "", "user", "Hi", nil),
			claudeReply(session, "ga-"+session, "g-"+session, "gateway-reply", "", text("Hello"), usage(1000, 0)),
		}
	}
	want := int64(110 + 44 + 220 + 2000)
	for _, order := range [][]string{{"s1", "s2"}, {"s2", "s1"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			useHost(t, "host-a")
			catalog, _ := testCatalog(t)
			files := map[string][]string{"s1.jsonl": original, "s2.jsonl": resumed}
			all := map[string][]string{"g1.jsonl": gateway("g1"), "g2.jsonl": gateway("g2")}
			for _, session := range order {
				all[session+".jsonl"] = files[session+".jsonl"]
				ingestSource(t, catalog, claudeSessionFiles(t, all))
			}
			wantLibraryTokens(t, catalog, want)
			owners, err := queryMaps(catalog.DB, `SELECT c.native_id FROM messages m JOIN conversations c ON c.id=m.conversation_id
				WHERE m.native_id LIKE '%:usage' AND m.raw_text LIKE '%"msg_1"%' AND m.raw_text NOT LIKE '%usage_copy%'`)
			if err != nil || len(owners) != 1 || owners[0]["native_id"] != "s1" {
				t.Fatalf("msg_1 counted by %v, %v; want s1 alone", owners, err)
			}

			// A library indexed before copies were settled, whose transcripts
			// Claude Code has since removed, is settled from what it stored.
			if _, err := catalog.DB.Exec(`UPDATE messages SET raw_text=json_extract(raw_text,'$.copied') WHERE json_valid(raw_text) AND json_extract(raw_text,'$.type')='usage_copy'`); err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.DB.Exec("DELETE FROM claude_usage_copies"); err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.DB.Exec("DELETE FROM usage_attribution_state"); err != nil {
				t.Fatal(err)
			}
			if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 4 {
				t.Fatalf("repair = %d, %v", repaired, err)
			}
			wantLibraryTokens(t, catalog, want)
		})
	}
}
