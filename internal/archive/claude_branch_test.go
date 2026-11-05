package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	// A transcript another tool wrote, whose turns name no parents, is one
	// branch.
	flat := parseJSONL(t, "claude", filepath.Join(t.TempDir(), "flat.jsonl"),
		claudeTurn("s", "p1", "", "user", "List the files", nil),
		claudeReply("s", "a1", "", "msg_1", "req_1", text("Two files"), usage(10, 1)),
		claudeTurn("s", "u2", "", "user", "Delete them", nil),
		claudeReply("s", "a2", "", "msg_2", "req_2", text("Deleted"), usage(20, 2)))
	for entry, on := range entrySelected(t, flat) {
		if !on {
			t.Errorf("%s of a transcript without parents is not selected", entry)
		}
	}
}

func TestClaudeCountsAdvisorIterationsUnderTheirModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	withAdvisor := map[string]any{"input_tokens": 100, "output_tokens": 10, "iterations": []any{
		map[string]any{"type": "message", "input_tokens": 100, "output_tokens": 10},
		map[string]any{"type": "advisor_message", "model": "claude-advisor", "input_tokens": 50, "output_tokens": 5},
	}}
	// Replies without a message ID have advisors of their own too.
	anonymous := map[string]any{"input_tokens": 100, "output_tokens": 10, "iterations": []any{
		map[string]any{"type": "advisor_message", "model": "claude-advisor", "input_tokens": 60, "output_tokens": 5},
	}}
	messages := parseJSONL(t, "claude", path,
		claudeTurn("s", "p1", "", "user", "Plan it", nil),
		claudeReply("s", "a1", "p1", "msg_1", "req_1", text("Planned"), withAdvisor),
		claudeTurn("s", "p2", "a1", "user", "And this?", nil),
		claudeReply("s", "a2", "p2", "", "req_2", text("Planned"), anonymous),
		claudeTurn("s", "p3", "a2", "user", "And that?", nil),
		claudeReply("s", "a3", "p3", "", "req_3", text("Planned"), anonymous))
	byModel := map[string]float64{}
	requests, _ := buildToolLedger(messages, "claude-test")
	for _, request := range requests {
		byModel[request.Model] += request.Counts["total_tokens"]
	}
	if byModel["claude-test"] != 330 || byModel["claude-advisor"] != 55+65+65 || len(byModel) != 2 {
		t.Fatalf("tokens by model = %v, want claude-test 330 and claude-advisor 185", byModel)
	}
	if total := conversationTokenCounts(messages)["total_tokens"]; total != 330+185 {
		t.Fatalf("total tokens = %v, want %d", total, 330+185)
	}
	// The reader counts a usage message ID once.
	advisors := map[string]bool{}
	for _, message := range messages {
		if claudeAdvisorRequest(message) {
			value := map[string]any{}
			json.Unmarshal([]byte(message.Text), &value)
			advisors[firstString(value["usage_message_id"])] = true
		}
	}
	if len(advisors) != 3 {
		t.Fatalf("advisor usage message IDs = %v, want one for each reply", advisors)
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
	original := []string{
		claudeTurn("s0", "p1", "", "user", "Investigate", nil),
		claudeReply("s0", "a1", "p1", "msg_1", "req_1", text("Looking"), usage(100, 10)),
	}
	// s1 resumes s0, then forks a subagent.
	parent := []string{
		strings.Replace(strings.ReplaceAll(original[0], `"sessionId":"s0"`, `"sessionId":"s1"`), "2026-09-24T17:00:00Z", "2026-09-24T18:00:00Z", 1),
		strings.ReplaceAll(original[1], `"sessionId":"s0"`, `"sessionId":"s1"`),
	}
	// A forked subagent starts with a copy of its parent's conversation; a
	// sidechain replays it under new request IDs, and its copy can be the
	// fuller record of the reply, which then counts in neither s0 nor s1.
	for _, output := range []int{10, 20} {
		catalog, _ := testCatalog(t)
		fork := []string{
			claudeReply("s1", "a1", "p1", "msg_1", "req_1b", text("Looking"), usage(100, output)),
			claudeReply("s1", "f1", "a1", "msg_2", "req_2", text("Found it"), usage(200, 20)),
		}
		source := claudeSessionFiles(t, map[string][]string{"s0.jsonl": original, "s1.jsonl": parent, "s1/subagents/agent-f.jsonl": withSidechain(fork)})
		ingestSource(t, catalog, source)
		want := int64(100 + output + 220)
		wantLibraryTokens(t, catalog, want)
		unsettle(t, catalog)
		if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 2 {
			t.Fatalf("repair = %d, %v", repaired, err)
		}
		wantLibraryTokens(t, catalog, want)
	}
}

// unsettle makes catalog a library indexed before copies were settled, whose
// transcripts Claude Code has since removed.
func unsettle(t *testing.T, catalog *Catalog) {
	t.Helper()
	for _, statement := range []string{
		`UPDATE messages SET raw_text=json_extract(raw_text,'$.copied') WHERE json_valid(raw_text) AND json_extract(raw_text,'$.type')='usage_copy'`,
		"DELETE FROM claude_usage_copies", "DELETE FROM usage_attribution_state",
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeRepairRestoresAReplyWhoseLinesDisagree(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	// One reply streamed over two lines; the second carries its final usage.
	original := []string{
		claudeTurn("s1", "p1", "", "user", "Investigate", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Looking"), usage(100, 1)),
		claudeReply("s1", "a2", "a1", "msg_1", "req_1", text("Found"), usage(100, 10)),
	}
	resumed := []string{
		strings.Replace(strings.ReplaceAll(original[0], `"sessionId":"s1"`, `"sessionId":"s2"`), "2026-09-24T17:00:00Z", "2026-09-24T18:00:00Z", 1),
		strings.ReplaceAll(original[1], `"sessionId":"s1"`, `"sessionId":"s2"`),
		strings.ReplaceAll(original[2], `"sessionId":"s1"`, `"sessionId":"s2"`),
	}
	ingestSource(t, catalog, claudeSessionFiles(t, map[string][]string{"s1.jsonl": original, "s2.jsonl": resumed}))
	wantLibraryTokens(t, catalog, 110)
	// A record of the reply's second line in s1 was left a copy.
	var raw string
	if err := catalog.DB.QueryRow(`SELECT m.raw_text FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE c.native_id='s1' AND m.native_id='a2:usage'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`UPDATE messages SET raw_text=? WHERE native_id='a2:usage' AND conversation_id=(SELECT id FROM conversations WHERE native_id='s1')`, wrapUsageCopy(raw, "s2")); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("DELETE FROM usage_attribution_state"); err != nil {
		t.Fatal(err)
	}
	if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 2 {
		t.Fatalf("repair = %d, %v", repaired, err)
	}
	wantLibraryTokens(t, catalog, 110)
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
	// A session resumed and left at once holds nothing but copies.
	copyOnly := []string{
		strings.Replace(strings.ReplaceAll(original[0], `"sessionId":"s1"`, `"sessionId":"s3"`), "2026-09-24T17:00:00Z", "2026-09-24T19:00:00Z", 1),
		strings.ReplaceAll(original[1], `"sessionId":"s1"`, `"sessionId":"s3"`),
	}
	// A gateway that names every reply alike, without a request ID, is not
	// matched across sessions.
	gateway := func(session string) []string {
		return []string{
			claudeTurn(session, "g-"+session, "", "user", "Hi", nil),
			claudeReply(session, "ga-"+session, "g-"+session, "gateway-reply", "", text("Hello"), usage(1000, 0)),
		}
	}
	want := int64(110 + 44 + 220 + 2000)
	for _, order := range [][]string{{"s1", "s2", "s3"}, {"s3", "s2", "s1"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			useHost(t, "host-a")
			catalog, _ := testCatalog(t)
			files := map[string][]string{"s1.jsonl": original, "s2.jsonl": resumed, "s3.jsonl": copyOnly}
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

			// A library indexed before copies were settled is settled from
			// what it stored.
			unsettle(t, catalog)
			if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 5 {
				t.Fatalf("repair = %d, %v", repaired, err)
			}
			wantLibraryTokens(t, catalog, want)
		})
	}
	// s2, saved from a catalog that never held s1, counted s1's reply there,
	// and a catalog indexed before advisor records left out its advisor's
	// tokens. Recovered, it counts as if indexed here.
	for _, advisors := range []bool{true, false} {
		t.Run(fmt.Sprintf("recovered with advisor records %v", advisors), func(t *testing.T) {
			useHost(t, "host-a")
			earlier, _ := testCatalog(t)
			ingestSource(t, earlier, claudeSessionFiles(t, map[string][]string{"s2.jsonl": resumed}))
			tx := seedTx(t, earlier.DB)
			var workspaceID string
			if err := tx.QueryRow("SELECT id FROM workspaces").Scan(&workspaceID); err != nil {
				t.Fatal(err)
			}
			if !advisors {
				if _, err := tx.Exec("DELETE FROM messages WHERE native_id LIKE '%:usage:advisor:%'"); err != nil {
					t.Fatal(err)
				}
				conversations, ids, err := storedWorkspaceConversations(t.Context(), tx, workspaceID)
				if err != nil {
					t.Fatal(err)
				}
				if err := rebuildWorkspaceUsage(tx, workspaceID, conversations, ids, map[string]bool{ids[0]: true}); err != nil {
					t.Fatal(err)
				}
			}
			envelope, err := exportHistoryRecovery(t.Context(), tx, historyUnit{Kind: "catalog-workspace", Original: workspaceID}, "")
			tx.Rollback()
			if err != nil {
				t.Fatal(err)
			}
			requests := func(catalog *Catalog) string {
				rows, err := queryMaps(catalog.DB, `SELECT c.native_id conversation,r.native_id,r.sequence,r.total_tokens,r.context_growth_tokens
					FROM model_requests r JOIN conversations c ON c.id=r.conversation_id ORDER BY 1,2`)
				if err != nil {
					t.Fatal(err)
				}
				return jsonText(rows)
			}
			for _, beside := range []map[string][]string{{}, {"s1.jsonl": original}} {
				catalog, _ := testCatalog(t)
				if len(beside) > 0 {
					ingestSource(t, catalog, claudeSessionFiles(t, beside))
				}
				if outcome, err := catalog.applyHistoryRecovery(t.Context(), envelope); err != nil || outcome != "catalog-derived" {
					t.Fatal(outcome, err)
				}
				wantLibraryTokens(t, catalog, 110+44+220)
				indexed, _ := testCatalog(t)
				beside["s2.jsonl"] = resumed
				ingestSource(t, indexed, claudeSessionFiles(t, beside))
				if got, want := requests(catalog), requests(indexed); got != want {
					t.Fatalf("recovered requests %s, want %s", got, want)
				}
			}
		})
	}
}

func TestClaudeGatewayReplySharingAnIDStaysOnItsBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// A gateway names every reply alike and leaves the request ID empty.
	gatewayReply := func(uuid, parent, reply string) string {
		return claudeTurn("s", uuid, parent, "assistant", text(reply), claudeEntry{"requestId": "", "message": map[string]any{"id": "reply", "model": "claude-test", "usage": usage(1, 1)}})
	}
	messages := parseJSONL(t, "claude", path,
		claudeTurn("s", "p1", "", "user", "Hi", nil),
		gatewayReply("g1", "p1", "Hello"),
		claudeTurn("s", "u2", "g1", "user", "Tell me a joke", nil),
		gatewayReply("g2", "u2", "A joke"),
		claudeTurn("s", "u3", "g1", "user", "Tell me a fact", nil),
		gatewayReply("g3", "u3", "A fact"))
	selected := entrySelected(t, messages)
	if !selected["g1"] || !selected["g3"] || selected["u2"] || selected["g2"] {
		t.Fatalf("selected = %v, want p1, g1, u3 and g3 alone", selected)
	}
}

func claudeUsageMessage(uuid, id, request string, input, output int) MessageRecord {
	counts := usage(input, output)
	event := map[string]any{"type": "assistant", "uuid": uuid, "requestId": request, "message": map[string]any{"id": id, "model": "claude-test", "usage": counts}}
	return MessageRecord{NativeID: uuid + ":usage", Role: "system", Kind: "metadata", Model: "claude-test", Selected: true, RawText: jsonText(event),
		Text: jsonText(map[string]any{"type": "token_usage", "usage": counts, "usage_message_id": id})}
}

func claudeSessionRecord(kind, native, started, origin string, messages ...MessageRecord) WorkspaceRecord {
	return WorkspaceRecord{SourceID: native, SourceKind: kind, Account: "local", Conversations: []ConversationRecord{{
		NativeID: native, Provider: "claude", Model: "claude-test", Account: "local", Origin: origin, StartedAt: started, Messages: messages}}}
}

func TestClaudeCopyFromAnotherMacKeepsWhatIsStored(t *testing.T) {
	catalog, _ := testCatalog(t)
	r1, r2 := claudeUsageMessage("a1", "msg_1", "req_1", 100, 10), claudeUsageMessage("a2", "msg_2", "req_2", 200, 20)
	ingestAs(t, catalog, "host-a", newCopyFixture(t, claudeSessionRecord("claude", "s1", "2026-09-20T10:00:00Z", "/a/s1.jsonl", r1, r2)))
	ingestAs(t, catalog, "host-a", newCopyFixture(t, claudeSessionRecord("claude", "s2", "2026-09-20T11:00:00Z", "/a/s2.jsonl",
		r1, r2, claudeUsageMessage("b1", "msg_3", "req_3", 400, 40))))
	wantLibraryTokens(t, catalog, 110+220+440)
	// Another Mac's older copy of s1 lacks a reply s1 still holds.
	if result := ingestAs(t, catalog, "host-b", newCopyFixture(t, claudeSessionRecord("claude", "s1", "2026-09-20T10:00:00Z", "/b/s1.jsonl", r1))); result.Workspaces != 1 {
		t.Fatalf("the copy was not processed: %+v", result)
	}
	wantLibraryTokens(t, catalog, 110+220+440)
}

func TestClaudeUnchangedSessionWhoseReplyMovedIsCountedAgain(t *testing.T) {
	catalog, _ := testCatalog(t)
	r1 := claudeUsageMessage("a1", "msg_1", "req_1", 100, 10)
	s1 := claudeSessionRecord("claude", "s1", "2026-09-20T12:00:00Z", "/a/s1.jsonl", r1, claudeUsageMessage("a2", "msg_2", "req_2", 200, 20))
	ingestAs(t, catalog, "host-a", newCopyFixture(t, s1))
	ingestAs(t, catalog, "host-a", newCopyFixture(t, claudeSessionRecord("claude", "s2", "2026-09-20T11:00:00Z", "/a/s2.jsonl", r1)))
	wantLibraryTokens(t, catalog, 110+220)
	// Another Mac's older copy of s1 starts earlier, so s1 counts the reply;
	// then s1 gains a subagent, and its own unchanged transcript starts later
	// again.
	ingestAs(t, catalog, "host-b", newCopyFixture(t, claudeSessionRecord("claude", "s1", "2026-09-20T10:00:00Z", "/b/s1.jsonl", r1)))
	wantLibraryTokens(t, catalog, 110+220)
	s1.Conversations = append(s1.Conversations, ConversationRecord{NativeID: "s1:subagent:x", ParentNativeID: "s1", AgentDepth: 1, Provider: "claude", Model: "claude-test",
		Account: "local", Origin: "/a/s1/subagents/agent-x.jsonl", StartedAt: "2026-09-20T12:01:00Z", Messages: []MessageRecord{claudeUsageMessage("x1", "msg_9", "req_9", 50, 5)}})
	ingestAs(t, catalog, "host-a", newCopyFixture(t, s1))
	wantLibraryTokens(t, catalog, 110+220+55)
}

func TestClaudeMirrorsKeepTheirReplies(t *testing.T) {
	catalog, _ := testCatalog(t)
	r1 := claudeUsageMessage("a1", "msg_1", "req_1", 100, 10)
	ingestAs(t, catalog, "host-a", newCopyFixture(t, claudeSessionRecord("claude", "s1", "2026-09-20T10:00:00Z", "/a/s1.jsonl", r1)))
	// TL1 holds the session too; Library counts one of the two.
	ingestAs(t, catalog, "host-a", newCopyFixture(t, claudeSessionRecord("tl1", "tl1-run", "2026-09-20T11:00:00Z", "/a/run.jsonl", r1)))
	copies, err := queryMaps(catalog.DB, `SELECT native_id FROM messages WHERE raw_text LIKE '{"copied":%'`)
	if err != nil || len(copies) != 0 {
		t.Fatalf("copies = %v, %v; a mirror's replies are its own", copies, err)
	}
}

func TestClaudeClaimLeavesCopiedRepliesUncounted(t *testing.T) {
	catalog, _ := testCatalog(t)
	reply := claudeUsageMessage("a1", "msg_1", "req_1", 100, 10)
	copied := reply
	copied.RawText = wrapUsageCopy(reply.RawText, "s1")
	// The process's claim can count what its subagent was given as well as
	// what it sent.
	claim := MessageRecord{NativeID: "claim", Kind: "metadata", Text: `{"type":"cost-state","modelUsage":{"claude-test":{"input_tokens":400,"output_tokens":40}}}`}
	record := claudeSessionRecord("claude", "s1", "2026-09-20T10:00:00Z", "/a/s1.jsonl", reply, claim)
	record.Conversations = append(record.Conversations, ConversationRecord{NativeID: "s1:subagent:agent-f", ParentNativeID: "s1", Provider: "claude", Model: "claude-test",
		Account: "local", AgentDepth: 1, StartedAt: "2026-09-20T10:01:00Z", Messages: []MessageRecord{copied, claudeUsageMessage("f1", "msg_2", "req_2", 200, 20)}})
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var sessions int64
	if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 110+220 {
		t.Fatalf("sessions = %d, want %d", sessions, 110+220)
	}
}

func TestClaudeFullerRecordOfAReplyCounts(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	// s1 was read before the reply's last line was written; the session that
	// resumed it holds the reply whole.
	original := []string{
		claudeTurn("s1", "p1", "", "user", "Investigate", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Looking"), usage(100, 1)),
	}
	resumed := []string{
		strings.Replace(strings.ReplaceAll(original[0], `"sessionId":"s1"`, `"sessionId":"s2"`), "2026-09-24T17:00:00Z", "2026-09-24T18:00:00Z", 1),
		strings.ReplaceAll(original[1], `"sessionId":"s1"`, `"sessionId":"s2"`),
		claudeReply("s2", "a2", "a1", "msg_1", "req_1", text("Found"), usage(100, 10)),
	}
	ingestSource(t, catalog, claudeSessionFiles(t, map[string][]string{"s1.jsonl": original, "s2.jsonl": resumed}))
	wantLibraryTokens(t, catalog, 110)
}

func TestClaudeRepairAddsAdvisorUsageFromStoredReplies(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	advised := map[string]any{"input_tokens": 100, "output_tokens": 10, "iterations": []any{
		map[string]any{"type": "advisor_message", "model": "claude-advisor", "input_tokens": 50, "output_tokens": 5},
	}}
	original := []string{
		claudeTurn("s1", "p1", "", "user", "Plan it", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Planned"), advised),
	}
	resumed := []string{
		strings.Replace(strings.ReplaceAll(original[0], `"sessionId":"s1"`, `"sessionId":"s2"`), "2026-09-24T17:00:00Z", "2026-09-24T18:00:00Z", 1),
		strings.ReplaceAll(original[1], `"sessionId":"s1"`, `"sessionId":"s2"`),
	}
	ingestSource(t, catalog, claudeSessionFiles(t, map[string][]string{"s1.jsonl": original, "s2.jsonl": resumed}))
	// TL1 runs Claude Code too, and its stored replies are all that is left
	// of them.
	run := claudeUsageMessage("t1", "msg_7", "req_7", 100, 10)
	run.RawText = jsonText(map[string]any{"type": "assistant", "uuid": "t1", "requestId": "req_7", "message": map[string]any{"id": "msg_7", "model": "claude-test", "usage": advised}})
	ingestAs(t, catalog, "host-a", newCopyFixture(t, claudeSessionRecord("tl1", "tl1-run", "2026-09-24T19:00:00Z", "/a/run.jsonl", run)))
	wantLibraryTokens(t, catalog, 165+110)
	// A library indexed before advisor iterations were counted.
	unsettle(t, catalog)
	if _, err := catalog.DB.Exec("DELETE FROM messages WHERE native_id LIKE '%:advisor:%'"); err != nil {
		t.Fatal(err)
	}
	// The run, saved before its advisor records were, and recovered.
	tx := seedTx(t, catalog.DB)
	var workspaceID string
	if err := tx.QueryRow("SELECT id FROM workspaces WHERE source_kind='tl1'").Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	envelope, err := exportHistoryRecovery(t.Context(), tx, historyUnit{Kind: "catalog-workspace", Original: workspaceID}, "")
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if repaired, err := catalog.RepairUsageAttribution(context.Background(), nil); err != nil || repaired != 3 {
		t.Fatalf("repair = %d, %v", repaired, err)
	}
	wantLibraryTokens(t, catalog, 165+165)
	recovered, _ := testCatalog(t)
	if outcome, err := recovered.applyHistoryRecovery(t.Context(), envelope); err != nil || outcome != "catalog-derived" {
		t.Fatal(outcome, err)
	}
	wantLibraryTokens(t, recovered, 165)
}

func TestClaudeSessionCountsAReplyOnceWhateverItsDepths(t *testing.T) {
	reply := claudeUsageMessage("a1", "msg_1", "req_1", 100, 10)
	record := WorkspaceRecord{SourceKind: "claude", Conversations: []ConversationRecord{
		{NativeID: "s1", Provider: "claude", Account: "local"},
		{NativeID: "s1:subagent:agent-a", Provider: "claude", Account: "local", AgentDepth: 1, Messages: []MessageRecord{reply}},
		{NativeID: "s1:subagent:agent-z", Provider: "claude", Account: "local", AgentDepth: 1, Messages: []MessageRecord{reply}},
	}}
	counted := func() int64 {
		total := int64(0)
		for _, conversation := range record.Conversations {
			total += integer(conversationTokenCounts(conversation.Messages)["total_tokens"])
		}
		return total
	}
	dedupeClaudeGroupUsage(&record)
	// The lineage found later puts agent-a under agent-z; deciding again
	// from what the first decision left still counts the reply once.
	record.Conversations[1].AgentDepth = 2
	dedupeClaudeGroupUsage(&record)
	if got := counted(); got != 110 {
		t.Fatalf("counted %d, want 110", got)
	}
}

func TestClaudeForkHoldingTheWholeReplyCountsIt(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	parent := []string{
		claudeTurn("s1", "p1", "", "user", "Investigate", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Looking"), usage(100, 1)),
	}
	fork := withSidechain([]string{claudeReply("s1", "a1", "p1", "msg_1", "req_1b", text("Looking"), usage(100, 10))})
	ingestSource(t, catalog, claudeSessionFiles(t, map[string][]string{"s1.jsonl": parent, "s1/subagents/agent-f.jsonl": fork}))
	wantLibraryTokens(t, catalog, 110)
}

func TestClaudeSubagentWhoseTranscriptIsGoneIsSettled(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	parent := []string{
		claudeTurn("s1", "p1", "", "user", "Investigate", nil),
		claudeReply("s1", "a1", "p1", "msg_1", "req_1", text("Looking"), usage(100, 10)),
	}
	fork := withSidechain([]string{
		claudeReply("s1", "a1", "p1", "msg_1", "req_1b", text("Looking"), usage(100, 10)),
		claudeReply("s1", "f1", "a1", "msg_2", "req_2", text("Found it"), usage(200, 20)),
	})
	source := claudeSessionFiles(t, map[string][]string{"s1.jsonl": parent, "s1/subagents/agent-f.jsonl": fork})
	ingestSource(t, catalog, source)
	wantLibraryTokens(t, catalog, 110+220)
	// A library indexed before copies were settled; Claude Code then removed
	// the subagent's transcript, and the session's grew.
	unsettle(t, catalog)
	if err := os.Remove(filepath.Join(source.Path, "-p", "s1", "subagents", "agent-f.jsonl")); err != nil {
		t.Fatal(err)
	}
	appendSourceFile(t, filepath.Join(source.Path, "-p", "s1.jsonl"), claudeTurn("s1", "p2", "a1", "user", "Thanks", nil)+"\n")
	ingestSource(t, catalog, source)
	if conversationMessages(t, catalog, "s1:subagent:agent-f") == 0 {
		t.Fatal("the subagent's stored conversation is gone; the test needs it kept")
	}
	wantLibraryTokens(t, catalog, 110+220)
}

func TestClaudeAdvisorRequestLeavesTheContextAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	advised := map[string]any{"input_tokens": 1000, "output_tokens": 10, "iterations": []any{
		map[string]any{"type": "advisor_message", "model": "claude-advisor", "input_tokens": 50, "output_tokens": 5},
	}}
	messages := parseJSONL(t, "claude", path,
		claudeTurn("s", "p1", "", "user", "Plan it", nil),
		claudeReply("s", "a1", "p1", "msg_1", "req_1", toolUse("t1"), advised),
		claudeTurn("s", "r1", "a1", "user", toolResult("t1"), nil),
		claudeReply("s", "a2", "r1", "msg_2", "req_2", text("Done"), usage(1020, 10)))
	requests, _ := buildToolLedger(messages, "claude-test")
	found := false
	for _, request := range requests {
		if request.NativeID == "msg_2" {
			found = true
			if request.ContextGrowth == nil || *request.ContextGrowth != 10 {
				t.Fatalf("msg_2 context growth = %v, want 10", request.ContextGrowth)
			}
		}
	}
	if !found {
		t.Fatalf("no request for msg_2 in %+v", requests)
	}
}

func TestClaudeShallowerSubagentCountsAReplyItsChildCopied(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	reply := withSidechain([]string{claudeReply("s1", "z2", "z1", "msg_9", "req_9", text("Half done"), usage(300, 30))})[0]
	source := claudeSessionFiles(t, map[string][]string{
		"s1.jsonl": {
			claudeTurn("s1", "p1", "", "user", "Investigate", nil),
			claudeTurn("s1", "l1", "p1", "assistant", []any{map[string]any{"type": "tool_use", "id": "root-call", "name": "Agent", "input": map[string]any{"prompt": "Child assignment"}}}, nil),
			claudeTurn("s1", "l2", "l1", "user", toolResult("root-call"), claudeEntry{"toolUseResult": map[string]any{"agentId": "z"}}),
		},
		// agent-z launches agent-a, whose name sorts first, with a copy of
		// its conversation.
		"s1/subagents/agent-z.jsonl": withSidechain([]string{
			claudeTurn("s1", "z1", "", "user", "Child assignment", nil),
			reply,
			claudeTurn("s1", "z3", "z2", "assistant", []any{map[string]any{"type": "tool_use", "id": "child-call", "name": "Agent", "input": map[string]any{"prompt": "Grandchild assignment"}}}, nil),
			claudeTurn("s1", "z4", "z3", "user", []any{map[string]any{"type": "tool_result", "tool_use_id": "child-call", "content": []any{map[string]any{"type": "text", "text": "Async agent launched successfully.\nagentId: a (internal ID - do not mention to user.)"}}}}, nil),
		}),
		"s1/subagents/agent-a.jsonl": withSidechain([]string{claudeTurn("s1", "a0", "", "user", "Grandchild assignment", nil), reply}),
	})
	ingestSource(t, catalog, source)
	wantLibraryTokens(t, catalog, 330)
	counted, err := queryMaps(catalog.DB, `SELECT c.native_id,c.agent_depth FROM messages m JOIN conversations c ON c.id=m.conversation_id
		WHERE m.native_id LIKE '%:usage' AND m.raw_text NOT LIKE '{"copied":%'`)
	if err != nil || len(counted) != 1 || counted[0]["native_id"] != "s1:subagent:agent-z" || integer(counted[0]["agent_depth"]) != 1 {
		t.Fatalf("counted in %v, %v; want agent-z at depth 1", counted, err)
	}
}
