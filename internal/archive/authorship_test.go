package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var authorshipStart = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func authorAt(minutes int) time.Time {
	return authorshipStart.Add(time.Duration(minutes) * time.Minute)
}

// categoryText joins the text of every span in one category.
func categoryText(text string, spans []authorSpan, category string) string {
	parts := []string{}
	for _, span := range spans {
		if span.Category == category {
			parts = append(parts, strings.TrimSpace(text[span.Start:span.End]))
		}
	}
	return strings.Join(parts, "|")
}

func TestAuthorshipSeparatesHarnessAttachmentsAndSlashCommands(t *testing.T) {
	c := newClassifier(nil)
	text := "<system_instruction>\nYou are working inside Conductor.\n</system_instruction>\n\nPlease fix the login bug @⟦pasted_text_2026-09-01.txt⟧(.context%2Fattachments%2Fab%2Fpasted_text_2026-09-01.txt)"
	spans := c.classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanHarness); !strings.HasPrefix(got, "<system_instruction>") || !strings.HasSuffix(got, "</system_instruction>") {
		t.Fatalf("harness = %q", got)
	}
	if got := categoryText(text, spans, spanTyped); got != "Please fix the login bug" {
		t.Fatalf("typed = %q", got)
	}
	if got := categoryText(text, spans, spanAttachment); !strings.HasPrefix(got, "@⟦pasted_text") {
		t.Fatalf("attachment = %q", got)
	}
	path := "Create a PR .context/attachments/6oe65N/PR instructions.md"
	if got := categoryText(path, c.classify(authorInput{ID: "p", ConversationID: "d", Text: path, SentAt: authorAt(1)}), spanAttachment); got != ".context/attachments/6oe65N/PR instructions.md" {
		t.Fatalf("attachment path = %q", got)
	}
	slash := "/loop 5m check the deploy"
	spans = c.classify(authorInput{ID: "s", ConversationID: "e", Text: slash, SentAt: authorAt(2)})
	if categoryText(slash, spans, spanTemplate) != "/loop" || categoryText(slash, spans, spanTyped) != "5m check the deploy" {
		t.Fatalf("slash spans = %#v", spans)
	}
}

func TestAuthorshipAttributesSenders(t *testing.T) {
	c := newClassifier(nil)
	for _, input := range []authorInput{
		{Sender: "automation:claude-sdk-cli"}, {Sender: "automation:codex-exec"}, {Sender: "agent:session"},
		{SourceKind: "tl1"}, {SubAgent: true},
	} {
		input.ID, input.ConversationID, input.Text, input.SentAt = "m", "c", "Review this candidate carefully", authorAt(0)
		spans := c.classify(input)
		if len(spans) != 1 || spans[0].Category != spanAutomated || spans[0].Reason == "" {
			t.Fatalf("%#v spans = %#v", input, spans)
		}
	}
	person := authorInput{ID: "p", ConversationID: "c", Sender: "account:me", Text: "Review this candidate carefully", SentAt: authorAt(1)}
	if spans := c.classify(person); len(spans) != 1 || spans[0].Category != spanTyped {
		t.Fatalf("person spans = %#v", spans)
	}
}

func TestAuthorshipAutomationHeartbeats(t *testing.T) {
	heartbeat := "<heartbeat>\n  <automation_id>hourly-progress</automation_id>\n  <current_time_iso>2026-10-29T16:03:58.670Z</current_time_iso>\n  <instructions>\nReport progress and continue the scheduled checks while preserving the original experiment plans and pending decisions.\n  </instructions>\n</heartbeat>"
	for name, text := range map[string]string{
		"scheduled":            heartbeat,
		"CRLF":                 strings.ReplaceAll(heartbeat, "\n", "\r\n"),
		"harness":              "<system_instruction>Generated instructions</system_instruction>\n" + heartbeat,
		"literal instructions": strings.Replace(heartbeat, "Report progress and continue the scheduled checks while preserving the original experiment plans and pending decisions.", "Compare a < b & c > d, and inspect <status> values.", 1),
	} {
		t.Run(name, func(t *testing.T) {
			input := authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)}
			c := newClassifier([]authorInput{input})
			if len(c.templates) != 0 {
				t.Fatal("heartbeat seeded prompt templates")
			}
			spans := c.classify(input)
			if len(spans) != 1 || spans[0].Start != 0 || spans[0].End != len(text) || spans[0].Category != spanAutomated || spans[0].Reason != reasonAutomation.with("hourly-progress").text() {
				t.Fatalf("heartbeat spans = %#v", spans)
			}
			if counts := countSpans(text, spans); counts.words[spanTyped] != 0 || counts.words[spanPasted] != 0 || counts.words[spanAutomated] == 0 {
				t.Fatalf("counts = %#v", counts)
			}
		})
	}
	for name, text := range map[string]string{
		"ordinary prose":     "Please explain how a heartbeat automation works.",
		"fenced example":     "```xml\n" + heartbeat + "\n```",
		"human prefix":       "Please review this scheduler payload.\n" + heartbeat,
		"human suffix":       heartbeat + "\nPlease change the schedule.",
		"missing ID":         strings.Replace(heartbeat, "hourly-progress", "", 1),
		"missing timestamp":  strings.Replace(heartbeat, "<current_time_iso>2026-10-29T16:03:58.670Z</current_time_iso>", "", 1),
		"invalid timestamp":  strings.Replace(heartbeat, "2026-10-29T16:03:58.670Z", "not a timestamp", 1),
		"empty instructions": strings.Replace(heartbeat, "Report progress and continue the scheduled checks while preserving the original experiment plans and pending decisions.", "", 1),
		"incomplete":         strings.TrimSuffix(heartbeat, "</heartbeat>"),
	} {
		t.Run(name, func(t *testing.T) {
			spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
			if got := categoryText(text, spans, spanAutomated); got != "" {
				t.Fatalf("non-heartbeat counted as automated: %q", got)
			}
		})
	}
	// Scheduled instructions must not seed the person's re-send index.
	c := newClassifier(nil)
	c.classify(authorInput{ID: "scheduled", ConversationID: "c", Text: heartbeat, SentAt: authorAt(0)})
	followup := "Report progress and continue the scheduled checks while preserving the original experiment plans and pending decisions."
	if got := categoryText(followup, c.classify(authorInput{ID: "human", ConversationID: "c", Text: followup, SentAt: authorAt(1)}), spanTyped); got != followup {
		t.Fatalf("heartbeat seeded re-send matching: %q", got)
	}
}

func TestAuthorshipDetectsTemplatesButNotOneDayBroadcasts(t *testing.T) {
	inputs := []authorInput{}
	for day := 0; day < 5; day++ {
		inputs = append(inputs, authorInput{ID: "t" + string(rune('a'+day)), ConversationID: "c" + string(rune('a'+day)),
			Text: "Please review the changes in this workspace .context/attachments/x" + string(rune('a'+day)) + "/Review request.md", SentAt: authorAt(day * 24 * 60)})
	}
	broadcast := "Draft a migration plan for the billing tables and list risks"
	for session := 0; session < 6; session++ {
		inputs = append(inputs, authorInput{ID: "b" + string(rune('a'+session)), ConversationID: "s" + string(rune('a'+session)), Text: broadcast, SentAt: authorAt(10 * 24 * 60)})
	}
	c := newClassifier(inputs)
	for _, input := range inputs[:5] {
		if spans := c.classify(input); categoryText(input.Text, spans, spanTyped) != "" || !strings.Contains(spans[0].Reason, "5 sessions on 5 days") {
			t.Fatalf("template spans = %#v", spans)
		}
	}
	if spans := c.classify(inputs[5]); categoryText(broadcast, spans, spanTyped) != broadcast {
		t.Fatalf("first broadcast copy = %#v", spans)
	}
}

func TestAuthorshipFindsCopiedTextWithinWindow(t *testing.T) {
	c := newClassifier(nil)
	reply := "Here are the notable gaps in what this telemetry can answer with high confidence today and what it would take to close them"
	c.agentText.add(authorAt(0), textOrigin{"other-chat", "reply"}, shingles(wordTokens(reply)))
	text := "Consider these gaps and whether we could add telemetry.\n\n" + reply
	spans := c.classify(authorInput{ID: "q", ConversationID: "c", Text: text, SentAt: authorAt(30)})
	if got := categoryText(text, spans, spanTyped); got != "Consider these gaps and whether we could add telemetry." {
		t.Fatalf("typed = %q", got)
	}
	var quoted authorSpan
	for _, span := range spans {
		if span.Category == spanQuoted {
			quoted = span
		}
	}
	if quoted.Source != "other-chat" || !strings.Contains(quoted.Reason, "30m") {
		t.Fatalf("quoted = %#v", quoted)
	}
	// A Friday-to-Monday copy remains covered, including the exact boundary.
	for _, when := range []time.Duration{3 * 24 * time.Hour, authorshipWindow} {
		in := authorInput{ID: "week", ConversationID: "week", Text: reply, SentAt: authorAt(0).Add(when)}
		if got := categoryText(reply, c.classify(in), spanQuoted); got != reply {
			t.Fatalf("within seven days: %q", got)
		}
	}
	// Agent output leaves the window; your own messages never do.
	fresh := newClassifier(nil)
	fresh.agentText.add(authorAt(0), textOrigin{"other-chat", "reply"}, shingles(wordTokens(reply)))
	late := authorInput{ID: "late", ConversationID: "c", Text: reply, SentAt: authorAt(169 * 60)}
	fresh.agentText.evict(late.SentAt.Add(-authorshipWindow))
	if spans := fresh.classify(late); categoryText(reply, spans, spanTyped) != reply {
		t.Fatalf("text from outside the window = %#v", spans)
	}
	again := authorInput{ID: "again", ConversationID: "d", Text: reply, SentAt: authorAt(10 * 24 * 60)}
	if spans := fresh.classify(again); len(spans) != 1 || spans[0].Category != spanResent || spans[0].Source != "c" || !strings.Contains(spans[0].Reason, "2d") {
		t.Fatalf("resent = %#v", spans)
	}
}

func TestAuthorshipIgnoresHarnessWhenMatchingResentText(t *testing.T) {
	c := newClassifier(nil)
	harness := "<system_instruction>\nIf the user asks for help with Conductor, you can ask them to go to Help then Send Feedback to get in touch with our team.\n</system_instruction>\n\n"
	c.classify(authorInput{ID: "a", ConversationID: "c", Text: harness + "When I open the jobs page the counts stop updating", SentAt: authorAt(0)})
	text := harness + "When trying to drain the coordinator the status stops updating"
	spans := c.classify(authorInput{ID: "b", ConversationID: "d", Text: text, SentAt: authorAt(10)})
	if got := categoryText(text, spans, spanTyped); got != "When trying to drain the coordinator the status stops updating" {
		t.Fatalf("typed = %q (%#v)", got, spans)
	}
}

func TestAuthorshipFlagsInterruptedOutputAndReusedLines(t *testing.T) {
	c := newClassifier(nil)
	trace := "TL1 hits this at some point:\n\nTraceback (most recent call last):\n" +
		"  File \"/tl1/supervisor/pool.py\", line 90, in _manage_loop\n    self._reconcile()\n    ~~~~~~~~~~~~~~~^^\n\n" +
		"  File \"/tl1/supervisor/pool.py\", line 184, in _reconcile\n    self._spawn_worker()\n" +
		"OSError: [Errno 24] Too many open files\n\nPlease find out how to prevent it."
	spans := c.classify(authorInput{ID: "t", ConversationID: "c", Text: trace, SentAt: authorAt(0)})
	if got := categoryText(trace, spans, spanTyped); got != "TL1 hits this at some point:|Please find out how to prevent it." {
		t.Fatalf("traceback typed = %q (%#v)", got, spans)
	}
	deploy := "Deploy seems broken.\n\n[deploy] Running preflight checks...\n[deploy] Preflight checks passed for the project\n" +
		"[deploy] Verifying the runtime service account exists\n[deploy] Runtime service account exists"
	spans = c.classify(authorInput{ID: "d", ConversationID: "c", Text: deploy, SentAt: authorAt(60)})
	if got := categoryText(deploy, spans, spanTyped); got != "Deploy seems broken." {
		t.Fatalf("deploy typed = %q", got)
	}
	// A saved line sent in two other conversations is re-sent in the third;
	// a short phrase anyone might type again is not.
	saved := "Use sub-agents to parallelize work and keep context small."
	for index, page := range []string{"Settings header clips", "Billing totals drift", "Profile avatar broken"} {
		text := page + ".\n" + saved
		spans = c.classify(authorInput{ID: page, ConversationID: page, Text: text, SentAt: authorAt(120 + index*24*60)})
		c.classify(authorInput{ID: page + "-ok", ConversationID: page, Text: "Great, please do so.", SentAt: authorAt(121 + index*24*60)})
		if index < 2 {
			continue
		}
		if categoryText(text, spans, spanTyped) != "Profile avatar broken." || categoryText(text, spans, spanResent) != saved {
			t.Fatalf("reused line spans = %#v", spans)
		}
	}
	short := authorInput{ID: "short", ConversationID: "other", Text: "Great, please do so.", SentAt: authorAt(5000)}
	if spans := c.classify(short); len(spans) != 1 || spans[0].Category != spanTyped {
		t.Fatalf("short phrase spans = %#v", spans)
	}
	// Indented list items and ordinary prose stay typed.
	list := "Some notes:\n    - the header is too tall\n    - the footer overlaps the chart\n    - the legend repeats colors\nThanks!"
	if spans := c.classify(authorInput{ID: "n", ConversationID: "h", Text: list, SentAt: authorAt(9000)}); len(spans) != 1 || spans[0].Category != spanTyped {
		t.Fatalf("list spans = %#v", spans)
	}
}

func TestAuthorshipFlagsPastedStructure(t *testing.T) {
	c := newClassifier(nil)
	log := "I'm failing to set up firebase with this error - what permission am I missing?\n\n" +
		"[debug] [2026-04-16T13:26:00.356Z] > authorizing via signed-in user\n" +
		"[debug] [2026-04-16T13:26:00.357Z] > command requires scopes: [\"email\"]\n" +
		"[debug] [2026-04-16T13:26:01.002Z] <<< HTTP RESPONSE 403"
	spans := c.classify(authorInput{ID: "l", ConversationID: "c", Text: log, SentAt: authorAt(0)})
	if got := categoryText(log, spans, spanTyped); got != "I'm failing to set up firebase with this error - what permission am I missing?" {
		t.Fatalf("typed = %q (%#v)", got, spans)
	}
	plan := "Please critique this plan.\n\n# Plan: Run a task once\n## Context\nToday tasks only run in the pool.\n## Steps\n- **Queue** add a flag\n- **Worker** honor it"
	spans = c.classify(authorInput{ID: "p", ConversationID: "c", Text: plan, SentAt: authorAt(60)})
	if got := categoryText(plan, spans, spanTyped); got != "Please critique this plan." {
		t.Fatalf("plan typed = %q", got)
	}
	code := "Why does this fail?\n```go\nfunc main() { panic(1) }\n```"
	spans = c.classify(authorInput{ID: "k", ConversationID: "c", Text: code, SentAt: authorAt(120)})
	if categoryText(code, spans, spanTyped) != "Why does this fail?" || !strings.HasPrefix(categoryText(code, spans, spanPasted), "```go") {
		t.Fatalf("code spans = %#v", spans)
	}
	table := "Please check these results.\n\nPR\tTitle\tStatus\n123\tPreserve the selected scope\topen\n124\tRefresh cached records\tclosed\n\nAlso check mobile."
	spans = c.classify(authorInput{ID: "table", ConversationID: "table", Text: table, SentAt: authorAt(180)})
	if got := categoryText(table, spans, spanTyped); got != "Please check these results.|Also check mobile." {
		t.Fatalf("TSV table leaked or human follow-up lost: %q", got)
	}
}

func TestAuthorshipFlagsIdentifiers(t *testing.T) {
	c := newClassifier(nil)
	path := "Please debug job_other_06F7MZRNW2VMSJ6VAVF9CDFHH4 and look at the worker logs"
	spans := c.classify(authorInput{ID: "p", ConversationID: "c", Text: path, SentAt: authorAt(0)})
	if got := categoryText(path, spans, spanPasted); got != "job_other_06F7MZRNW2VMSJ6VAVF9CDFHH4" {
		t.Fatalf("pasted = %q (%#v)", got, spans)
	}
	if got := categoryText(path, spans, spanTyped); got != "Please debug|and look at the worker logs" {
		t.Fatalf("typed = %q", got)
	}
	selector := "This button is misaligned\n- Selector: `#pharosDrivePanel > dl.pharos-drive-space > dt:nth-of-type(2)`"
	spans = c.classify(authorInput{ID: "s", ConversationID: "c", Text: selector, SentAt: authorAt(60)})
	if got := categoryText(selector, spans, spanTyped); got != "This button is misaligned" {
		t.Fatalf("selector typed = %q (%#v)", got, spans)
	}
	// Short labels, quantities, abbreviations, and compounds are typed.
	prose := "Great, please fix M1, M2, and PR-3 next (i.e. the 2m timeouts) in the frontend/backend split"
	spans = c.classify(authorInput{ID: "t", ConversationID: "c", Text: prose, SentAt: authorAt(120)})
	if len(spans) != 1 || spans[0].Category != spanTyped {
		t.Fatalf("prose spans = %#v", spans)
	}
}

func TestAuthorshipTypingSpeedOnlyFlagsImpossibleRates(t *testing.T) {
	c := newClassifier(nil)
	c.classify(authorInput{ID: "first", ConversationID: "c", Text: "Start the migration", SentAt: authorAt(0)})
	long := strings.Repeat("this sentence is plain prose that someone could type ", 20)
	fast := authorInput{ID: "fast", ConversationID: "c", Text: long, SentAt: authorAt(0).Add(10 * time.Second)}
	if spans := c.classify(fast); len(spans) != 1 || spans[0].Category != spanPasted || !strings.Contains(spans[0].Reason, "faster than typing") {
		t.Fatalf("fast spans = %#v", spans)
	}
	slow := authorInput{ID: "slow", ConversationID: "c", Text: strings.ReplaceAll(long, "plain", "calm"), SentAt: authorAt(20)}
	if spans := c.classify(slow); len(spans) != 1 || spans[0].Category != spanTyped {
		t.Fatalf("slow spans = %#v", spans)
	}
}

func TestAuthorshipRebuildCountsRepresentativeWorkOnce(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	reply := "The parser now accepts nested tables and every fixture passes with the new grammar rules enabled"
	for _, record := range []WorkspaceRecord{
		{SourceID: "claude-session", SourceKind: "claude", Account: "local", Title: "Parser", Conversations: []ConversationRecord{{
			NativeID: "session", Provider: "claude", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "u1", Role: "user", Kind: "message", Text: "<system_instruction>\nConductor\n</system_instruction>\n\nFix the parser for nested tables", CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true},
				{NativeID: "a1", Role: "assistant", Kind: "message", Text: reply, CreatedAt: "2026-09-01T12:05:00.000Z", Selected: true},
			}}}},
		{SourceID: "other", SourceKind: "codex", Account: "local", Title: "Review", Conversations: []ConversationRecord{{
			NativeID: "other", Provider: "codex", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "u2", Role: "user", Kind: "message", Text: "Check this claim: " + reply, CreatedAt: "2026-09-01T13:00:00.000Z", Selected: true},
				{NativeID: "u3", Role: "user", Kind: "message", Text: "Audit the grammar", CreatedAt: "2026-09-01T13:30:00.000Z", Selected: true, Sender: "automation:codex-exec"},
			}}}},
	} {
		tx, err := catalog.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	stats, err := catalog.AuthorshipStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	totals := stats["totals"].(map[string]any)
	if integer(totals["messages"]) != 3 || integer(totals["typed_words"]) != 9 || integer(totals["automated_chars"]) != int64(len("Audit the grammar")) ||
		integer(totals["quoted_chars"]) != int64(len(reply)) || integer(totals["harness_chars"]) == 0 {
		encoded, _ := json.Marshal(stats)
		t.Fatalf("stats = %s", encoded)
	}
	daily, _ := stats["daily"].([]map[string]any)
	if len(daily) != 1 || daily[0]["day"] != "2026-09-01" && daily[0]["day"] != "2026-09-02" || integer(daily[0]["quoted_words"]) != 16 ||
		integer(daily[0]["automated_words"]) != 3 || integer(totals["quoted_words"]) != 16 {
		t.Fatalf("daily = %#v, totals = %#v", daily, totals)
	}
	rows, err := queryMaps(catalog.DB, "SELECT w.id FROM workspaces w WHERE w.source_id='other'")
	if err != nil || len(rows) != 1 {
		t.Fatalf("workspace rows = %v %v", rows, err)
	}
	detail, err := catalog.WorkDetail(firstString(rows[0]["id"]))
	if err != nil {
		t.Fatal(err)
	}
	messages := detail["conversations"].([]map[string]any)[0]["messages"].([]map[string]any)
	authorship, _ := messages[0]["authorship"].(map[string]any)
	spans, _ := authorship["spans"].([]map[string]any)
	if len(spans) != 2 || spans[1]["category"] != spanQuoted || !strings.HasPrefix(firstString(spans[1]["excerpt"]), "The parser now accepts") {
		t.Fatalf("detail authorship = %#v", authorship)
	}
	// A copied span links to the message it was copied from.
	source, _ := spans[1]["source"].(map[string]any)
	claude, err := queryMaps(catalog.DB, "SELECT m.id message_id,c.id conversation_id,c.workspace_id FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE m.native_id='a1'")
	if err != nil || len(claude) != 1 {
		t.Fatalf("source rows = %v %v", claude, err)
	}
	if source["workspace_id"] != claude[0]["workspace_id"] || source["conversation_id"] != claude[0]["conversation_id"] || source["message_id"] != claude[0]["message_id"] || source["title"] != "Parser" {
		t.Fatalf("source = %#v, want %#v", source, claude[0])
	}
}

func TestClaudeKeepsHarnessTextAndMarksHeadlessRuns(t *testing.T) {
	prompt := "<system_instruction>\nYou are working inside Conductor.\n</system_instruction>\n\nShip the fix"
	messages := parseJSONL(t, "claude", filepath.Join(t.TempDir(), "session.jsonl"),
		`{"sessionId":"s","uuid":"first","type":"user","entrypoint":"sdk-ts","message":{"role":"user","content":"<system_instruction>\nYou are working inside Conductor.\n</system_instruction>\n\nShip the fix"}}`,
		`{"sessionId":"s","uuid":"headless","type":"user","entrypoint":"sdk-cli","message":{"role":"user","content":"Review the candidate"}}`,
	)
	// The prompt keeps harness text: it is part of what the model read.
	if turns := humanTurns(messages); strings.Join(turns, "|") != prompt+"|Review the candidate" {
		t.Fatalf("human turns = %q", turns)
	}
	found := byNativeID(messages)
	if found["first"].Sender != "" || found["headless"].Sender != "automation:claude-sdk-cli" {
		t.Fatalf("senders = %q %q", found["first"].Sender, found["headless"].Sender)
	}
}

func TestCodexKeepsHarnessTextAndMarksExecRuns(t *testing.T) {
	messages := parseJSONL(t, "codex", filepath.Join(t.TempDir(), "rollout.jsonl"),
		`{"type":"session_meta","payload":{"id":"root","originator":"codex_exec","source":"exec"}}`,
		`{"type":"response_item","payload":{"type":"message","id":"prompt","role":"user","content":[{"type":"input_text","text":"<system_instruction>\nConductor\n</system_instruction>\n\nPlease review the changes"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text"]}}}`,
	)
	if turns := humanTurns(messages); strings.Join(turns, "|") != "<system_instruction>\nConductor\n</system_instruction>\n\nPlease review the changes" {
		t.Fatalf("human turns = %q", turns)
	}
	if sender := byNativeID(messages)["prompt"].Sender; sender != "automation:codex-exec" {
		t.Fatalf("sender = %q", sender)
	}
}

func TestConductorSender(t *testing.T) {
	for want, row := range map[string]map[string]any{
		"agent:s1":      {"sender_session_id": "s1", "sender_id": "u"},
		"automation:ci": {"sender_api_key_name": "ci", "sender_id": "u"},
		"account:u":     {"sender_id": "u"},
		"":              {},
	} {
		if got := conductorSender(row); got != want {
			t.Fatalf("conductorSender(%v) = %q, want %q", row, got, want)
		}
	}
}

func TestAuthorshipUpgradeAddsWordColumns(t *testing.T) {
	catalog, _ := testCatalog(t)
	if _, err := catalog.DB.Exec("DROP TABLE IF EXISTS message_authorship"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`CREATE TABLE message_authorship (message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		sent_at TEXT NOT NULL, day TEXT NOT NULL, total_chars INTEGER NOT NULL, typed_chars INTEGER NOT NULL, typed_words INTEGER NOT NULL,
		harness_chars INTEGER NOT NULL, automated_chars INTEGER NOT NULL, template_chars INTEGER NOT NULL, attachment_chars INTEGER NOT NULL,
		quoted_chars INTEGER NOT NULL, resent_chars INTEGER NOT NULL, pasted_chars INTEGER NOT NULL, pasted_words INTEGER NOT NULL, spans_json TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.AuthorshipStats(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// An index or sync rebuilds authorship when messages changed; a page builds
// it only when it is missing or classified by older rules.
func TestAuthorshipRebuildsAfterIndex(t *testing.T) {
	catalog, _ := testCatalog(t)
	started := []func(context.Context){}
	catalog.background = func(_ OperationSpec, work func(context.Context, *Op) error) bool {
		started = append(started, func(ctx context.Context) { _ = work(ctx, nil) })
		return true
	}
	check := func(step string, want int, running bool) {
		t.Helper()
		if len(started) != want || catalog.authorshipRunning() != running {
			t.Fatalf("%s: %d rebuilds started, running=%v; want %d, running=%v", step, len(started), catalog.authorshipRunning(), want, running)
		}
	}
	if !catalog.ensureAuthorship() || !catalog.ensureAuthorship() {
		t.Fatal("a missing ledger should be stale")
	}
	check("a page with no ledger", 1, true)
	// An index writes while the rebuild runs, and asks for a rebuild.
	if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
		t.Fatal(err)
	}
	catalog.refreshAuthorship()
	check("an index during the rebuild", 1, true)
	started[0](context.Background())
	check("after the rebuild", 2, true)
	started[1](context.Background())
	check("after the second rebuild", 2, false)
	if catalog.ensureAuthorship() {
		t.Fatal("the ledger should be current")
	}
	if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
		t.Fatal(err)
	}
	if !catalog.ensureAuthorship() {
		t.Fatal("the ledger should lag the index")
	}
	check("a page after an index elsewhere", 2, false)
	catalog.refreshAuthorship()
	check("the next index", 3, true)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	started[2](stopped)
	check("a stopped rebuild", 3, false)

	// Older rules rebuild on a page, unless that rebuild just failed.
	if _, err := catalog.DB.Exec("UPDATE meta SET value='1/authorship-v0' WHERE key='authorship_generation'"); err != nil {
		t.Fatal(err)
	}
	catalog.authorship.lastError = "disk full"
	catalog.ensureAuthorship()
	check("a page after a failed rebuild", 3, false)
	catalog.authorship.lastError = ""
	catalog.ensureAuthorship()
	check("a page with older rules", 4, true)
	started[3](context.Background())
	check("after the page's rebuild", 4, false)

	// While this process indexes, a rebuild a page started does not start
	// again for what the index wrote meanwhile: the index rebuilds at its end.
	if _, err := catalog.DB.Exec("UPDATE meta SET value='1/authorship-v0' WHERE key='authorship_generation'"); err != nil {
		t.Fatal(err)
	}
	catalog.ingestStarted()
	catalog.ensureAuthorship()
	check("a page with older rules during an index", 5, true)
	if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
		t.Fatal(err)
	}
	started[4](context.Background())
	check("the page's rebuild during an index", 5, false)
	if !catalog.ensureAuthorship() {
		t.Fatal("the ledger should lag the index")
	}
	check("a page during the index", 5, false)
	// The index's analysis chain still follows up on its own rebuild.
	chain := context.Background()
	catalog.refreshAuthorshipInContext(chain)
	check("the index's chain", 6, true)
	if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
		t.Fatal(err)
	}
	started[5](chain)
	check("the chain's follow-up", 7, true)
	started[6](chain)
	check("after the chain", 7, false)
	if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
		t.Fatal(err)
	}
	// The page's rebuild skipped its follow-up, so the index's end runs it
	// (here the chain's rebuild left it current only until the last bump).
	catalog.ingestEnded(false)
	check("the index's end", 8, true)
	if catalog.authorship.deferred.Load() {
		t.Fatal("the deferred follow-up stayed deferred after it ran")
	}
	started[7](context.Background())
	check("after that refresh", 8, false)

	// A scheduled index runs no analysis chain: a page's rebuild that
	// skipped its follow-up during it runs one once the index ends, and
	// only once.
	if _, err := catalog.DB.Exec("UPDATE meta SET value='1/authorship-v0' WHERE key='authorship_generation'"); err != nil {
		t.Fatal(err)
	}
	catalog.ingestStarted()
	catalog.ensureAuthorship()
	check("a page during a scheduled index", 9, true)
	if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
		t.Fatal(err)
	}
	started[8](context.Background())
	check("the page's rebuild skipped its follow-up", 9, false)
	if !catalog.authorship.deferred.Load() {
		t.Fatal("the skipped follow-up was not deferred")
	}
	// A stopped index, or one the service's stop ended, starts nothing and
	// keeps the follow-up for later.
	catalog.ingestEnded(true)
	check("a stopped index ended", 9, false)
	catalog.ingestStarted()
	registry := catalog.ops
	stopping := newOpRegistry("service")
	stopping.releasing = func() bool { return true }
	catalog.ops = stopping
	catalog.ingestEnded(false)
	check("an index ended by the service stopping", 9, false)
	catalog.ops = registry
	if !catalog.authorship.deferred.Load() {
		t.Fatal("a stop dropped the deferred follow-up")
	}
	catalog.ingestStarted()
	catalog.ingestEnded(false)
	check("the next index that ends normally", 10, true)
	started[9](context.Background())
	catalog.ingestStarted()
	catalog.ingestEnded(false)
	check("a later index with nothing deferred", 10, false)
}

func TestAuthorshipManualRefreshHonorsRunCancellation(t *testing.T) {
	for _, builtGeneration := range []string{"", "1/authorship-v0"} {
		t.Run("ledger="+builtGeneration, func(t *testing.T) {
			catalog, config := testCatalog(t)
			server := testServer(t, config, catalog)
			if builtGeneration != "" {
				if _, err := catalog.DB.Exec(`INSERT INTO meta(key,value) VALUES('authorship_generation',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, builtGeneration); err != nil {
					t.Fatal(err)
				}
			}
			var rebuilds []func(context.Context)
			catalog.background = func(_ OperationSpec, work func(context.Context, *Op) error) bool {
				rebuilds = append(rebuilds, func(ctx context.Context) { _ = work(ctx, nil) })
				return true
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			catalog.refreshAuthorshipInContext(ctx)
			if len(rebuilds) != 1 || !catalog.authorshipRunning() {
				t.Fatal("manual refresh did not start")
			}
			cancel()
			rebuilds[0](t.Context())
			if catalog.authorshipRunning() || catalog.authorship.lastError != "" {
				t.Fatalf("stopped refresh: running=%v, error=%q", catalog.authorshipRunning(), catalog.authorship.lastError)
			}
			if built, err := catalog.metaValue(t.Context(), "authorship_generation"); err != nil || built != builtGeneration {
				t.Fatalf("stopped classification was published: %q %v", built, err)
			}
			stats, err := catalog.AuthorshipStats(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if stats["error"] != nil || stats["stale"] != true || len(rebuilds) != 2 || !catalog.authorshipRunning() {
				t.Fatalf("page did not retry interrupted refresh: stats=%v, rebuilds=%d", stats, len(rebuilds))
			}
			rebuilds[1](t.Context())
			if catalog.authorshipRunning() || catalog.ensureAuthorship() {
				t.Fatal("page retry did not publish a current ledger")
			}
			// A coordinated retry must receive the worker's actual denominators.
			execTx(t, catalog.DB,
				`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
				`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','c')`,
				`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('m','c','m','user','message','Fix the parser','hash','2026-09-01T12:00:00Z')`)
			if err := bumpToolLedgerGeneration(catalog.DB); err != nil {
				t.Fatal(err)
			}
			run := server.startRunNamed(indexRunKind, nil)
			meter := &runWorkMeter{server: server, runID: run.ID, kind: "index", model: "authorship"}
			defer meter.release(false)
			catalog.refreshAuthorshipInContext(withWorkProgress(t.Context(), meter.report))
			if len(rebuilds) != 3 {
				t.Fatalf("coordinated retry did not start: %d", len(rebuilds))
			}
			rebuilds[2](t.Context())
			stages := meter.finish(true)
			for _, phase := range []string{"classifying authorship", "saving authorship"} {
				found := false
				for _, stage := range stages {
					if stage.Phase == phase && stage.TotalKnown && stage.Total > 0 && stage.Done == stage.Total && stage.Completed {
						found = true
					}
				}
				if !found {
					t.Fatalf("worker lost %s progress: %+v", phase, stages)
				}
			}

		})
	}
}

func TestAuthorshipInputsSeeMirrorLinksCachedBeforeARun(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, id := range []string{"native", "wrapper"} {
		if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,?,?,?,?,?)`, id, "codex", "local", id, id, "2026-10-18"); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES(?,?,?,?,?)`, "c-"+id, id, "codex", "local", id); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`, "m-"+id, "c-"+id, "m-"+id, "user", "message", "Fix the parser", "hash-"+id, "2026-10-18T12:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// A page caches the links, then a run reconciles a mirror within the hold.
	catalog.suppressMirrors(nil)
	catalog.ingestRuns.Add(1)
	defer catalog.ingestRuns.Add(-1)
	if _, err := catalog.DB.Exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('c-native','c-wrapper','mirror',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	inputs, _, err := catalog.authorshipInputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 {
		t.Fatalf("authorship classified a mirror the run just linked: %+v", inputs)
	}
}

func TestAuthorshipCountsWordsConsistently(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"- · => . # [ )", 0},
		{"don't re-enter frontend/backend 2m 1", 5},
		{"1. Ship it\n2) Keep it\n1", 5},
		{"Café déjà-vu 中文", 3},
		{"1.", 1},
	} {
		spans := []authorSpan{{Start: 0, End: len(tc.text), Category: spanTyped}}
		if got := countSpans(tc.text, spans).words[spanTyped]; got != tc.want {
			t.Errorf("%q: words=%d want %d", tc.text, got, tc.want)
		}
	}
	text := "Review finished."
	end := len(text) - 1
	spans := []authorSpan{{Start: 0, End: end, Category: spanQuoted}, {Start: end, End: len(text), Category: spanTyped}}
	counts := countSpans(text, spans)
	if counts.words[spanTyped] != 0 || counts.words[spanQuoted] != 2 {
		t.Fatalf("punctuation creates human words: %+v", counts)
	}
	// Partitioning a word into different explanation spans cannot create words.
	text = "Can't re-enter Café"
	for split := range text {
		if split == 0 {
			continue
		}
		spans := []authorSpan{{Start: 0, End: split, Category: spanTyped}, {Start: split, End: len(text), Category: spanTyped, Reason: "another reason"}}
		if got := countSpans(text, spans).words[spanTyped]; got != 3 {
			t.Fatalf("split %d creates %d words", split, got)
		}
	}
}

func TestAuthorshipCopyMatchesOneContiguousSource(t *testing.T) {
	text := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima"
	tokens := wordTokens(text)
	for _, source := range [][]string{
		{"alpha bravo charlie delta echo foxtrot golf hotel", "echo foxtrot golf hotel india juliet kilo lima"},
		{"alpha bravo charlie delta echo foxtrot golf hotel completely separate fragment echo foxtrot golf hotel india juliet kilo lima"},
	} {
		c := newClassifier(nil)
		for i, part := range source {
			c.agentText.add(authorAt(i), textOrigin{"other", "reply"}, shingles(wordTokens(part)))
		}
		if got := copiedRuns(tokens, shingles(tokens), c.agentText, authorAt(-1)); len(got) != 0 {
			t.Fatalf("stitched fragments counted as copy: %+v", got)
		}
	}
	c := newClassifier(nil)
	c.agentText.add(authorAt(0), textOrigin{"other", "full"}, shingles(tokens))
	c.agentText.add(authorAt(1), textOrigin{"other", "fragment"}, shingles(tokens[:8]))
	if got := copiedRuns(tokens, shingles(tokens), c.agentText, authorAt(-1)); len(got) != 1 || got[0].origin.message != "full" {
		t.Fatalf("a later shared phrase hides the full source: %+v", got)
	}
	c.agentText.add(authorAt(2), textOrigin{"other", "prefix"}, shingles(tokens[:10]))
	c.agentText.add(authorAt(3), textOrigin{"other", "suffix"}, shingles(tokens[2:]))
	if got := copiedRuns(tokens, shingles(tokens), c.agentText, authorAt(-1)); len(got) != 1 || got[0].origin.message != "full" || got[0].start != 0 || got[0].end != len(text) {
		t.Fatalf("later excerpts hid the complete copy: %+v", got)
	}
	// Repeated bodies share source data, but a later timestamp must retain
	// its own seven-day lifetime, even for mirrors less than a second apart.
	c = newClassifier(nil)
	c.addAgentText(authorAt(0), textOrigin{"first", "first"}, text, false)
	later := authorAt(0).Add(500 * time.Millisecond)
	c.addAgentText(later, textOrigin{"later", "later"}, text, false)
	cutoff := authorAt(0).Add(250 * time.Millisecond)
	c.agentText.evict(cutoff)
	if got := copiedRuns(tokens, shingles(tokens), c.agentText, cutoff); len(got) != 1 || got[0].origin.message != "later" {
		t.Fatalf("deduplicated body lost its later source lifetime: %+v", got)
	}
}

func TestAuthorshipRepeatedCopiesHaveCoalescedCoverage(t *testing.T) {
	for _, size := range []int{1024, 32768} {
		text := strings.Repeat("repeat ", size-1) + "repeat"
		tokens := wordTokens(text)
		hashes := shingles(tokens)
		index := newShingleIndex()
		index.add(authorAt(0), textOrigin{"source", "reply"}, hashes)
		runs := copiedRuns(tokens, hashes, index, authorAt(-1))
		if len(runs) != 1 || runs[0].start != 0 || runs[0].end != len(text) {
			t.Fatalf("%d repeated words have fragmented or incomplete coverage: %d runs", size, len(runs))
		}
	}
}

func BenchmarkAuthorshipRepeatedCopy(b *testing.B) {
	for _, size := range []int{4096, 8192, 16384, 32768} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			tokens := wordTokens(strings.Repeat("repeat ", size))
			hashes := shingles(tokens)
			index := newShingleIndex()
			index.add(authorAt(0), textOrigin{"source", "reply"}, hashes)
			for b.Loop() {
				if runs := copiedRuns(tokens, hashes, index, authorAt(-1)); len(runs) != 1 {
					b.Fatalf("fragmented coverage: %d runs", len(runs))
				}
			}
		})
	}
}

func TestAuthorshipAgentWrittenPayloads(t *testing.T) {
	body := "Write the workbook result outside both named input ranges so that the calculation is no longer circular.\n\nRegenerate the fixture and preserve the human comment after this handoff."
	patch := "*** Begin Patch\n*** Update File: docs/handoff.md\n old context remains human material\n-removed text is not newly authored\n+" + strings.ReplaceAll(body, "\n", "\n+") + "\n*** End Patch"
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"Write", map[string]any{"content": body}, body},
		{"Edit", map[string]any{"old_string": "old context remains human material", "new_string": body}, body},
		{"apply_patch", patch, body},
		{"exec", "await tools.apply_patch(" + strconv.Quote(patch) + ")", body},
		{"exec", "await tools.apply_patch(`" + patch + "`)", body},
		{"exec", "const patch = " + strconv.Quote(patch) + "; await tools.apply_patch(patch)", body},
		{"exec", "await tools.Write({content: " + strconv.Quote(body) + ", path: 'handoff.md',})", body},
		{"exec_command", map[string]any{"cmd": "cat > docs/handoff.md <<'EOF'\n" + body + "\nEOF\n"}, body},
		{"exec_command", map[string]any{"cmd": "cat >> 'docs/handoff text.md' <<'EOF'\n" + body + "\nEOF\n"}, body},
		{"exec_command", map[string]any{"cmd": "cat <<'EOF' 1>docs/handoff.md\n" + body + "\nEOF\n"}, body},
		{"exec_command", map[string]any{"cmd": "cat &>docs/handoff.md <<'EOF'\n" + body + "\nEOF\n"}, body},
		{"exec_command", map[string]any{"cmd": "tee -a docs/handoff.md <<'EOF'\n" + body + "\nEOF\n"}, body},
		{"exec_command", map[string]any{"cmd": "tee <<'EOF' docs/handoff.md\n" + body + "\nEOF\n"}, body},
		{"exec_command", map[string]any{"cmd": "mkdir -p docs && cat > docs/handoff.md <<'EOF'\n" + body + "\nEOF\n"}, body},
		{"exec", "await tools.exec_command({cmd:" + strconv.Quote("cat > docs/handoff.md <<'EOF'\n"+body+"\nEOF\n") + "})", body},
		{"exec", "const cmd = `cat > docs/handoff.md <<'EOF'\n" + body + "\nEOF\n`; await tools.exec_command({cmd})", body},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+fmt.Sprint(len(fmt.Sprint(tc.input))), func(t *testing.T) {
			parts := authoredToolText(tc.name, tc.input)
			if strings.Join(parts, "\n") != tc.want {
				t.Fatalf("authored payload=%q", parts)
			}
			c := newClassifier(nil)
			payload, _ := json.Marshal(map[string]any{"tool": tc.name, "input": tc.input})
			c.addAuthorshipTool(authorAt(0), textOrigin{"writer", "write-event"}, string(payload))
			text := body + "\n\nPlease also check mobile."
			spans := c.classify(authorInput{ID: "m", ConversationID: "reader", Text: text, SentAt: authorAt(3)})
			if got := categoryText(text, spans, spanTyped); strings.TrimLeft(got, ". \n") != "Please also check mobile." {
				t.Fatalf("copied handoff leaked or follow-up lost: %q", got)
			}
			if countSpans(text, spans).words[spanTyped] != 4 {
				t.Fatalf("typed words=%v", countSpans(text, spans).words)
			}
			for _, span := range spans {
				if span.Category == spanQuoted && (span.Message != "write-event" || spanRule(span) != reasonAuthoredTool.rule) {
					t.Fatalf("missing file provenance: %+v", span)
				}
			}
		})
	}
	for _, tc := range []struct {
		name  string
		input any
	}{
		{"Read", map[string]any{"content": body}},
		{"Edit", map[string]any{"old_string": body}},
		{"exec_command", map[string]any{"cmd": "cat <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat <<'EOF' | grep -E '>'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat '>' <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat \"$(printf '>')\" <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat <<'EOF' # > output.md\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat 2>errors.log <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat 1>&2 <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat >output.md >/dev/null <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat existing.md >output.md <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat <<'EOF' existing.md >output.md\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat <<'EOF' <existing.md >output.md\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "cat 3<<'EOF' >output.md\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "tee <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "tee -a <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "tee /dev/null <<'EOF'\n" + body + "\nEOF"}},
		{"exec_command", map[string]any{"cmd": "python3 - <<'PY'\n" + body + "\nPY"}},
		{"exec_command", map[string]any{"cmd": "cat > file <<'EOF'\n" + body}},
		{"exec", "// await tools.apply_patch(" + strconv.Quote(patch) + ")"},
		{"exec", "/* await tools.apply_patch(" + strconv.Quote(patch) + ") */"},
		{"exec", "const example = " + strconv.Quote("await tools.apply_patch("+strconv.Quote(patch)+")")},
		{"exec", `const example = /tools.Write({content:"` + strings.ReplaceAll(body, "\n", " ") + `"})/;`},
		{"exec", "await tools.apply_patch(makePatch())"},
		{"exec", "await tools.apply_patch(`" + patch + "${unknown}`)"},
		{"exec", "const unusedCommand = `cat > file <<'EOF'\n" + body + "\nEOF`;"},
	} {
		if parts := authoredToolText(tc.name, tc.input); len(parts) > 0 {
			t.Errorf("read/context/incomplete content treated as authored: %q", parts)
		}
	}
}

func TestAuthorshipQuestionForms(t *testing.T) {
	c := newClassifier(nil)
	source := `{"tool":"request_user_input_async","input":{"questions":[{"title":"Which mode should we use?","options":["Individual records (Recommended)","Conversation text"]},{"title":"Which name do you prefer?"},{"title":"Which color?"}]}}`
	c.addAuthorshipTool(authorAt(0), textOrigin{"chat", "question"}, source)
	text := "Which mode should we use?\nIndividual records\n\nWhich name do you prefer?\nMy new descriptive name\n\nWhich color?\n(Skipped)"
	spans := c.classify(authorInput{ID: "answer", ConversationID: "chat", Text: text, SentAt: authorAt(10 * 24 * 60)})
	if got := categoryText(text, spans, spanTyped); got != "My new descriptive name" {
		t.Fatalf("question text leaked or free answer lost: %q", got)
	}
	for _, input := range []authorInput{
		{ID: "plain", ConversationID: "chat", Text: "Individual records", SentAt: authorAt(2)},
		{ID: "other", ConversationID: "other", Text: "Which mode should we use?\nIndividual records", SentAt: authorAt(2)},
	} {
		if got := categoryText(input.Text, c.classify(input), spanTyped); got != input.Text {
			t.Fatalf("option/title alone establishes provenance: %q", got)
		}
	}
	for _, script := range []string{
		`await tools.request_user_input_async({questions:[{title:'Which records?',options:['Individual records (Recommended)','Conversations'],},]})`,
		`await tools.exec("await tools.request_user_input_async({questions:[{title:'Which records?',options:['Individual records','Conversations']}]})")`,
	} {
		c := newClassifier(nil)
		envelope, _ := json.Marshal(map[string]any{"tool": "exec", "input": script})
		c.addAuthorshipTool(authorAt(0), textOrigin{"chat", "question"}, string(envelope))
		text := "Which records?\nIndividual records\n\nPlease also check mobile."
		spans := c.classify(authorInput{ID: "answer", ConversationID: "chat", Text: text, SentAt: authorAt(1)})
		if got := categoryText(text, spans, spanTyped); got != "Please also check mobile." {
			t.Fatalf("nested question form leaked or follow-up lost: %q", got)
		}
	}
}

func TestAuthorshipStructuredQuestionReplies(t *testing.T) {
	wrap := func(body string) string {
		return "<send_user_message_question_reply>\n" + body + "\n</send_user_message_question_reply>"
	}
	body := `[{"questionItemId":"[\"request_user_input_async\",\"call_123\",0]","question":"Which scope should we support?","answer":"Café rooms and chairs"},{"answer":"Keep it flexible","question":"How much editing?","questionItemId":"second","metadata":{"answer":"Generated nested text"}}]`
	for name, text := range map[string]string{
		"compact":  wrap(body),
		"indented": "  " + wrap(body),
		"multiple": wrap(body) + "\n\n" + wrap(`[{"questionItemId":"third","question":"Last question","answer":"One more answer"}]`),
		"pretty": func() string {
			var v any
			_ = json.Unmarshal([]byte(body), &v)
			encoded, _ := json.MarshalIndent(v, "", "  ")
			return wrap(string(encoded))
		}(),
		"CRLF":              strings.ReplaceAll(wrap(body), "\n", "\r\n"),
		"harness and prose": "<system_instruction>Generated instructions</system_instruction>\nBefore this form.\n" + wrap(body) + "\nAfter this form.",
	} {
		t.Run(name, func(t *testing.T) {
			spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
			want := "Café rooms and chairs|Keep it flexible"
			if name == "multiple" {
				want += "|One more answer"
			}
			if name == "harness and prose" {
				want = "Before this form.|" + want + "|After this form."
			}
			if got := categoryText(text, spans, spanTyped); got != want {
				t.Fatalf("typed = %q; want %q; spans = %#v", got, want, spans)
			}
			wantWords := 7
			if name == "multiple" {
				wantWords += 3
			}
			if name == "harness and prose" {
				wantWords += 6
			}
			if counts := countSpans(text, spans); counts.words[spanTyped] != wantWords || counts.words[spanPasted] != 0 || counts.words[spanHarness] == 0 {
				t.Fatalf("counts = %#v", counts)
			}
		})
	}
	text := wrap(`[{"questionItemId":"first","question":"Generated prompt","answer":"Café\nrooms\tand\u0020chairs \"please\""},{"questionItemId":"second","question":"Another prompt","answer":""}]`)
	spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := countSpans(text, spans).words[spanTyped]; got != 5 {
		t.Fatalf("escaped answer words = %d; spans = %#v", got, spans)
	}
	for _, text := range []string{body, wrap(`[{"question":"Which scope?","answer":"Some rooms"}]`), wrap(`[{"questionItemId":"id","question":"Which scope?","answer":42}]`), wrap(body)[:len(wrap(body))-1], "```xml\n" + wrap(body) + "\n```"} {
		spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
		for _, span := range spans {
			if spanRule(span) == reasonQuestionForm.rule {
				t.Fatalf("unrecognized/example form claimed: %q", text)
			}
		}
	}
	// A form can carry a long, formatted human answer all at once. Generated
	// fields do not seed re-send matching; the answer still can be copied.
	answer := strings.Repeat("Please keep the room design flexible and easy to edit. ", 20) + "Use mainPanel.saveButton"
	encoded, _ := json.Marshal(answer)
	text = wrap(`[{"questionItemId":"id","question":"Which scope should we support when users want to model rooms and furniture together?","answer":` + string(encoded) + `}]`)
	c := newClassifier(nil)
	c.classify(authorInput{ID: "before", ConversationID: "c", Text: "Start", SentAt: authorAt(0)})
	spans = c.classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanTyped); got != answer {
		t.Fatalf("answer lost to heuristics: %q", got)
	}
	followup := "Which scope should we support when users want to model rooms and furniture together?"
	if got := categoryText(followup, c.classify(authorInput{ID: "next", ConversationID: "other", Text: followup, SentAt: authorAt(1)}), spanTyped); got != followup {
		t.Fatalf("generated question seeded matching: %q", got)
	}
	spans = c.classify(authorInput{ID: "copy", ConversationID: "third", Text: text, SentAt: authorAt(2)})
	if got := categoryText(text, spans, spanResent); got != answer {
		t.Fatalf("answer re-send lost: %q", got)
	}
	c = newClassifier(nil)
	c.addAgentText(authorAt(0), textOrigin{"agent", "reply"}, answer, false)
	spans = c.classify(authorInput{ID: "copy", ConversationID: "c", Text: text, SentAt: authorAt(1)})
	if got := categoryText(text, spans, spanQuoted); got != answer {
		t.Fatalf("copied answer lost: %q", got)
	}
}

func TestAuthorshipExplicitPastesPreserveHumanFollowup(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"The current label should say \"This is brand new descriptive copy for our application welcome screen\".", "The current label should say \"This is brand new descriptive copy for our application welcome screen\"."},
		{"User: Sends requests\nAgent: Returns helpful answers", "User: Sends requests\nAgent: Returns helpful answers"},
		{"This is the following error:\n\nI think the header needs more spacing.", "This is the following error:\n\nI think the header needs more spacing."},

		{"From Claude recently:\n\nME: Please explain this.\nCLAUDE: The result includes the previously selected scope.\n\nCan you improve the API?", "From Claude recently:|Can you improve the API?"},
		{"From Claude:\n\nUSER: " + strings.Repeat("Long conversation context. ", 40) + "\n\nASSISTANT: This later answer belongs to the same exchange.\nUSER: Please explain the final detail.\n\nASSISTANT: The final answer also belongs to that exchange.\n\nThat clarifies the background; please improve our API.", "From Claude:|That clarifies the background; please improve our API."},
		{"The operation fails with\n\nThe last checkpoint failed.\nSaved revision is abc12345\n\nPlease investigate this.", "The operation fails with|Please investigate this."},
		{"The operation _fails_ with\n\nThe last checkpoint failed.\nSaved revision is abc12345\n\nPlease investigate this.", "The operation _fails_ with|Please investigate this."},
		{"The page is broken with this error:\n\nMachine Parallelism\n\nNo captured timing snapshot is available for this library.\n\nPlease fix", "The page is broken with this error:|Please fix"},
		{"The page shows this error:\n\nMachine Parallelism\n\nThe heading needs more spacing.", "The page shows this error:|The heading needs more spacing."},
		{"Please review this pasted plan.\n\n# Plan\n## Context\nSome earlier text.\n\n## Requested change\n\nRegenerate the fixture and calculate the workbook again.\n\nAlso check mobile.", "Please review this pasted plan.|Also check mobile."},
		{"I wrote this plan.\n\n# Plan\n## Context\nSome earlier text.\n\n## Next steps\n\nThis final paragraph is my own writing.", "I wrote this plan.|This final paragraph is my own writing."},
		{"Please use \"This is brand new descriptive copy for our application welcome screen\".", "Please use \"This is brand new descriptive copy for our application welcome screen\"."},
		{"The current description reads \"Manage your library, choose what Pharos reads, and customize how the app behaves.\"\n\nPlease replace it.", "The current description reads|Please replace it."},
		{"Auth configuration plan — cannot be applied\ntitle: Allow limited access\nverdict: may_change\n\nFAILING OPERATION\n  operation 1 failed\n\nPLAN JSON\n{\"version\":1}\n\nPlease fix it.", "Please fix it."},
	} {
		spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "chat", Text: tc.text, SentAt: authorAt(0)})
		if got := categoryText(tc.text, spans, spanTyped); got != tc.want {
			t.Errorf("%q\ntyped=%q want=%q", tc.text, got, tc.want)
		}
	}
}

func TestAuthorshipShortReviewItemsNeedTheSameSource(t *testing.T) {
	c := newClassifier(nil)
	long := "New source bytes can be permanently skipped after a rewrite with unchanged size and timestamp."
	source := "Posted findings:\n1. **P1:** " + long + " [Code](/capture.go:10)\n2. **P2:** Mandatory hard links break indexing on external drives. [Location](/capture.go:20)"
	c.addAgentText(authorAt(0), textOrigin{"review", "reply"}, source, false)
	text := "Possibly stale review:\n\nP1: " + long + " Location:10\nP2: Mandatory hard links break indexing on external drives. Location:20\nP2: This is my newly written finding about correctness."
	spans := c.classify(authorInput{ID: "m", ConversationID: "chat", Text: text, SentAt: authorAt(3 * 24 * 60)})
	if got := categoryText(text, spans, spanTyped); got != "Possibly stale review:|P2: This is my newly written finding about correctness." {
		t.Fatalf("short report leak or new item lost: %q", got)
	}
	alone := "P2: Mandatory hard links break indexing on external drives."
	if got := categoryText(alone, c.classify(authorInput{ID: "alone", ConversationID: "other", Text: alone, SentAt: authorAt(4 * 24 * 60)}), spanQuoted); got != "" {
		t.Fatalf("unanchored short phrase matched: %q", got)
	}
	c = newClassifier(nil)
	source = "Posted five inline findings:\n- **[P2] Conversation-day sample counts messages.** [human-word-recipes.ts:47](/human-word-recipes.ts:47) omits the trend count and counts every message in the conversation instead.\n- **[P1] Offline rebuild reads live data.** [capture.go:707](/capture.go:707) invokes enrichment against records that may change after capture."
	c.addAgentText(authorAt(0), textOrigin{"review", "inline"}, source, false)
	text = "Posted five inline findings:\nP2] Conversation-day sample counts messages. human-word-recipes.ts:47 omits the trend count and counts every message in the conversation instead.\nP1 — Offline rebuild reads live data. capture.go:707 invokes enrichment against records that may change after capture.\n\nPlease fix both issues."
	spans = c.classify(authorInput{ID: "edited", ConversationID: "chat", Text: text, SentAt: authorAt(3 * 24 * 60)})
	if got := categoryText(text, spans, spanTyped); got != "Please fix both issues." {
		t.Fatalf("edited review headings leaked or follow-up lost: %q", got)
	}
}

func TestAuthorshipRebuildUsesToolSourcesAndSharedCounts(t *testing.T) {
	catalog, _ := testCatalog(t)
	body := "Place the calculation result outside the named input ranges and regenerate the workbook with a fresh oracle."
	old := "The previously generated document describes an entirely different workbook and its separate calculation policy and verification procedure."
	envelope := func(name string, input any) string {
		b, _ := json.Marshal(map[string]any{"tool": name, "input": input})
		return string(b)
	}
	record := WorkspaceRecord{SourceID: "authored-tools", SourceKind: "codex", Account: "local", Title: "Authored tools", Conversations: []ConversationRecord{{NativeID: "chat", Provider: "codex", Messages: []MessageRecord{
		{NativeID: "write", Role: "assistant", Kind: "tool_call", Text: envelope("exec", "await tools.apply_patch("+strconv.Quote("*** Begin Patch\n*** Add File: handoff.md\n+"+body+"\n*** End Patch")+")"), CreatedAt: "2026-09-01T12:00:00Z", Selected: true},
		{NativeID: "old-write", Role: "assistant", Kind: "tool_call", Text: envelope("Write", map[string]any{"content": old}), CreatedAt: "2026-09-01T12:01:00Z", Selected: true},
		{NativeID: "question", Role: "assistant", Kind: "tool_call", Text: envelope("exec", `await tools.request_user_input_async({questions:[{title:'Which records?',options:['Individual records','Conversations']}]})`), CreatedAt: "2026-09-01T12:02:00Z", Selected: true},
		{NativeID: "copied", Role: "user", Kind: "message", Text: body + "\n\nPlease also check mobile.", CreatedAt: "2026-09-04T12:00:00Z", Selected: true},
		{NativeID: "form", Role: "user", Kind: "message", Text: "Which records?\nIndividual records", CreatedAt: "2026-09-04T12:01:00Z", Selected: true},
		{NativeID: "late", Role: "user", Kind: "message", Text: old, CreatedAt: "2026-09-10T12:00:00Z", Selected: true},
	}}}}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ingestWorkspace(tx, record, false); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(catalog.DB, `SELECT m.native_id,a.message_id,a.conversation_id,a.typed_words,a.quoted_words FROM message_authorship a JOIN messages m ON m.id=a.message_id ORDER BY a.sent_at`)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
	if integer(rows[0]["typed_words"]) != 4 || integer(rows[0]["quoted_words"]) != 17 || integer(rows[1]["typed_words"]) != 0 || integer(rows[2]["typed_words"]) == 0 || integer(rows[2]["quoted_words"]) != 0 {
		t.Fatalf("counts/window=%v", rows)
	}
	var source string
	if err := catalog.DB.QueryRow(`SELECT source_message_id FROM message_authorship_spans WHERE message_id=? AND category='quoted'`, rows[0]["message_id"]).Scan(&source); err != nil {
		t.Fatal(err)
	}
	var native string
	if err := catalog.DB.QueryRow(`SELECT native_id FROM messages WHERE id=?`, source).Scan(&native); err != nil || native != "write" {
		t.Fatalf("source=%s %v", native, err)
	}
	for _, row := range rows {
		var words int64
		if err := catalog.DB.QueryRow(`SELECT SUM(words) FROM message_authorship_spans WHERE message_id=? AND category='typed'`, row["message_id"]).Scan(&words); err != nil {
			// A fully generated form has no typed span at all.
			if integer(row["typed_words"]) != 0 {
				t.Fatal(err)
			}
		}
		if words != integer(row["typed_words"]) {
			t.Fatalf("span and ledger words differ: %v vs %d", row, words)
		}
	}
	var text string
	if err := catalog.DB.QueryRow(`SELECT text FROM messages WHERE id=?`, rows[0]["message_id"]).Scan(&text); err != nil {
		t.Fatal(err)
	}
	message := map[string]any{"id": rows[0]["message_id"], "text": text}
	attach, err := catalog.messageAuthorshipAttacher(rows[0]["conversation_id"])
	if err != nil {
		t.Fatal(err)
	}
	attach(message)
	parts := message["authorship"].(map[string]any)["spans"].([]map[string]any)
	typed := int64(0)
	for _, part := range parts {
		if part["category"] == spanTyped {
			typed += integer(part["words"])
		}
	}
	if typed != 4 {
		t.Fatalf("reader words=%d", typed)
	}
}

func TestAuthorshipDiffStoreAndInterruptedChunks(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('c','w','codex','local','c','codex')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES
   ('m1','c','m1','user','message','Please inspect the parser carefully','2026-09-01T12:00:00Z','one'),
   ('m2','c','m2','user','message','Fix the second grammar problem','2026-09-01T13:00:00Z','two')`)
	if err := c.RebuildAuthorship(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `CREATE TABLE authorship_write_probe(table_name TEXT)`)
	for _, table := range []string{"message_authorship", "message_authorship_spans", "conversation_launches", "workspace_names"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			execSQL(t, c.DB, fmt.Sprintf(`CREATE TRIGGER probe_%s_%s AFTER %s ON %s BEGIN INSERT INTO authorship_write_probe VALUES('%s'); END`, table, event, event, table, table))
		}
	}
	// A second explicit full rebuild still computes the same classification;
	// none of the output or trigram maintenance triggers should run.
	if err := c.RebuildAuthorship(t.Context(), "second"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM authorship_write_probe`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unchanged writes=%d (%v)", n, err)
	}
	execSQL(t, c.DB, `UPDATE messages SET text='Review this changed input in detail',content_hash='changed' WHERE id='m2'`)
	override(t, &authorshipWriteBatch, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = withWorkProgress(ctx, func(work activityWork) {
		if work.Phase == "saving authorship" && work.Done > 0 {
			cancel()
		}
	})
	if err := c.RebuildAuthorship(ctx, "third"); err == nil {
		t.Fatal("interruption succeeded")
	}
	built, err := c.metaValue(t.Context(), "authorship_generation")
	if err != nil || built != "second" {
		t.Fatalf("partial generation=%q (%v)", built, err)
	}
	execSQL(t, c.DB, `DELETE FROM authorship_write_probe`)
	if err := c.RebuildAuthorship(t.Context(), "third"); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(c.DB, `SELECT a.message_id,a.spans_json FROM message_authorship a ORDER BY message_id`)
	if err != nil || len(rows) != 2 {
		t.Fatalf("after retry: %v %v", rows, err)
	}
	inputs, launches, err := c.authorshipInputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	results, err := c.classifyInputs(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := c.planAuthorship(t.Context(), results, launches)
	if err != nil || len(plan) != 0 {
		t.Fatalf("retry differs: %v (%v)", plan, err)
	}
	// Deletions are per key, including surplus span positions.
	execSQL(t, c.DB, `DELETE FROM messages WHERE id='m2'`)
	if err := c.RebuildAuthorship(t.Context(), "fourth"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"message_authorship", "message_authorship_spans"} {
		if err := c.DB.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE message_id='m2'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("surplus %s=%d (%v)", table, n, err)
		}
	}
}

func TestAuthorshipReplyPagesReleaseReadStatements(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('c','w','codex','local','c','codex')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES
   ('a','c','a','assistant','message','These exact words in the reply should be counted as copied output when sent again','2026-09-01T12:00:00Z','a'),
   ('b','c','b','assistant','message','A second reply at the same instant has its own source','2026-09-01T12:00:00Z','b'),
   ('d','c','d','assistant','message','A reply on a later day crosses the window boundary','2026-09-02T12:00:00Z','d')`)
	inputs := []authorInput{{ID: "u", ConversationID: "user", Text: "These exact words in the reply should be counted as copied output when sent again", SentAt: authorAt(25 * 60)}}
	baseline, err := classifyAuthorshipUnpaged(t.Context(), c.DB, inputs, newClassifier(inputs))
	if err != nil {
		t.Fatal(err)
	}
	override(t, &authorshipReplyBatch, 1)
	// A callback can acquire the sole connection while classification is in
	// progress; the old streaming statement held it until the loop ended.
	c.DB.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = withWorkProgress(ctx, func(work activityWork) {
		var n int
		if err := c.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
			t.Error(err)
		}
	})
	got, err := c.classifyInputs(ctx, inputs)
	if err != nil || jsonText(got) != jsonText(baseline) {
		t.Fatalf("paged classification differs: %v (%v)", got, err)
	}
}

// Compare every stored table to the actual full classifier after each split.
// The final, ordered suffix also exercises loading historical re-sent text,
// lines, last-turn times and question provenance from the persisted checkpoint.
func TestAuthorshipIncrementalParity(t *testing.T) {
	type event struct {
		id, conversation, role, kind, text string
		at                                 time.Time
		tool                               bool
	}
	copyText := "The parser now accepts nested tables and every fixture passes with the new grammar rules enabled for all supported inputs"
	events := []event{
		{id: "reply", conversation: "c0", role: "assistant", kind: "message", text: copyText, at: authorAt(0)},
		{id: "first", conversation: "c0", role: "user", kind: "message", text: "Please carefully inspect the behavior of this unusual parser and explain how the nested grammar should work across all these inputs", at: authorAt(1)},
		{id: "resent", conversation: "c1", role: "user", kind: "message", text: "Please carefully inspect the behavior of this unusual parser and explain how the nested grammar should work across all these inputs", at: authorAt(2)},
		{id: "launch", conversation: "c0", role: "assistant", kind: "tool_call", text: `{"tool":"Bash","input":{"command":"claude -p 'inspect grammar'"}}`, at: authorAt(3), tool: true},
		{id: "headless", conversation: "run", role: "user", kind: "message", text: "Inspect the nested grammar", at: authorAt(4)},
		{id: "child", conversation: "child", role: "user", kind: "message", text: "Check grammar from the child session", at: authorAt(5)},
		{id: "question", conversation: "c0", role: "assistant", kind: "tool_call", text: `{"tool":"request_user_input","input":{"questions":[{"header":"Deploy","id":"deploy","question":"Should the parser be deployed now?","options":[{"label":"Deploy the current parser","description":"Use the current build"},{"label":"Wait for another review","description":"Wait"}]}]}}`, at: authorAt(6), tool: true},
		{id: "edge", conversation: "c2", role: "user", kind: "message", text: copyText, at: authorAt(7 * 24 * 60)},
		{id: "outside", conversation: "c3", role: "user", kind: "message", text: copyText + " today", at: authorAt(7 * 24 * 60).Add(time.Second)},
		{id: "answer", conversation: "c0", role: "user", kind: "message", text: "Should the parser be deployed now?\nDeploy the current parser", at: authorAt(20 * 24 * 60)},
	}
	for i := 0; i < 5; i++ {
		events = append(events, event{id: fmt.Sprintf("template%d", i), conversation: fmt.Sprintf("c%d", i), role: "user", kind: "message", text: fmt.Sprintf("Please review the changes in this workspace .context/attachments/x%d/Review request.md", i), at: authorAt((i + 1) * 24 * 60)})
		events = append(events, event{id: fmt.Sprintf("line%d", i), conversation: fmt.Sprintf("c%d", i), role: "user", kind: "message", text: fmt.Sprintf("Task %d needs attention.\nwe should preserve this repeated line across several different conversations\nPlease investigate", i), at: authorAt((i+1)*24*60 + 10)})
	}
	orders := [][]int{}
	ordered := make([]int, len(events))
	for i := range ordered {
		ordered[i] = i
	}
	sort.SliceStable(ordered, func(i, j int) bool { return events[ordered[i]].at.Before(events[ordered[j]].at) })
	orders = append(orders, ordered)
	adversarial := append([]int{}, ordered...)
	for i, id := range adversarial {
		if events[id].id == "template4" {
			adversarial = append(append(adversarial[:i:i], adversarial[i+1:]...), id)
			break
		}
	}
	orders = append(orders, adversarial)
	for _, seed := range []int64{7, 29, 83} {
		order := rand.New(rand.NewSource(seed)).Perm(len(events))
		orders = append(orders, order)
	}
	for split, order := range orders {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			c, _ := testCatalog(t)
			tx := seedTx(t, c.DB)
			for _, id := range []string{"c0", "c1", "c2", "c3", "c4", "run", "child"} {
				work := id
				if id == "child" {
					work = "child-work"
				}
				if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,location) VALUES(?,'claude','local',?,?,'2026-09-01','/project')`, work, work, work); err != nil {
					t.Fatal(err)
				}
				var parent any
				harness := "claude-code/cli"
				if id == "child" {
					parent = "c0"
				}
				if id == "run" {
					harness = "claude-code/sdk-cli"
				}
				if _, err := tx.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness,parent_id,started_at) VALUES(?,?,'claude','local',?,?,?,?)`, id, work, id, harness, parent, formatTime(authorAt(3).Add(time.Second))); err != nil {
					t.Fatal(err)
				}
			}
			commitSeed(t, tx)
			suffixes := 0
			for start := 0; start < len(order); {
				stop := min(start+1+start%3, len(order))
				tx := seedTx(t, c.DB)
				for _, index := range order[start:stop] {
					e := events[index]
					if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES(?,?,?,?,?,?,?,?)`, e.id, e.conversation, e.id, e.role, e.kind, e.text, formatTime(e.at), hashBytes([]byte(e.text))); err != nil {
						t.Fatal(err)
					}
					if e.tool {
						category, program := "other", ""
						if e.id == "launch" {
							category, program = "command", "claude"
						}
						if _, err := tx.Exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,program,command,call_message_id,started_at,ended_at,status) VALUES(?,?,?,1,'claude','tool_call','Bash',?,?,?,?,?,?,'success')`, e.id, e.conversation, e.conversation, category, nilIfEmpty(program), "claude -p 'inspect grammar'", e.id, formatTime(e.at), formatTime(e.at.Add(2*time.Minute))); err != nil {
							t.Fatal(err)
						}
						if program != "" {
							if _, err := tx.Exec(`INSERT INTO tool_commands(tool_call_id,position,program,subcommand,command) VALUES(?,0,'claude','-p',?)`, e.id, "claude -p 'inspect grammar'"); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				commitSeed(t, tx)
				want, _, err := c.authorshipGenerations()
				if err != nil {
					t.Fatal(err)
				}
				ctx := withWorkProgress(t.Context(), func(work activityWork) {
					if work.Phase == "authorship mode: suffix" {
						suffixes++
					}
				})
				if err := c.RebuildAuthorship(ctx, want); err != nil {
					t.Fatal(err)
				}
				inputs, launches, err := c.authorshipInputs(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				results, err := c.classifyInputs(t.Context(), inputs)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := c.planAuthorship(t.Context(), results, launches)
				if err != nil || len(plan) != 0 {
					t.Fatalf("split %d at %d differs from full pass: changes=%v err=%v", split, stop, plan, err)
				}
				start = stop
			}
			// A new turn in an existing conversation after the graph is stable must
			// classify just this suffix and find the latest historical copy.
			text := events[1].text
			if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('append','c0','append','user','message',?,?,?)`, text, formatTime(authorAt(21*24*60)), hashBytes([]byte(text))); err != nil {
				t.Fatal(err)
			}
			want, _, _ := c.authorshipGenerations()
			classified := 0
			ctx := withWorkProgress(t.Context(), func(work activityWork) {
				if work.Phase == "authorship mode: suffix" {
					suffixes++
				}
				if work.Phase == "classifying authorship" {
					classified = int(work.Total)
				}
			})
			if err := c.RebuildAuthorship(ctx, want); err != nil {
				t.Fatal(err)
			}
			if classified != 1 || suffixes == 0 {
				t.Fatalf("append reclassified %d messages; suffix runs %d", classified, suffixes)
			}
			inputs, launches, err := c.authorshipInputs(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			results, err := c.classifyInputs(t.Context(), inputs)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.planAuthorship(t.Context(), results, launches)
			if err != nil || len(plan) != 0 {
				t.Fatalf("append differs: %v (%v)", plan, err)
			}
		})
	}
}

func TestAuthorshipIncrementalFallbacks(t *testing.T) {
	for _, name := range []string{"version", "identity", "automation", "backfill", "same-time", "suffix-limit"} {
		t.Run(name, func(t *testing.T) {
			c, _ := testCatalog(t)
			execTx(t, c.DB,
				`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
				`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('c','w','codex','local','c','codex'),('other','w','codex','local','other','codex')`,
				`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('m','c','m','user','message','Please inspect the first parser carefully','2026-09-01T12:00:00.000Z','hash')`)
			want, _, _ := c.authorshipGenerations()
			if err := c.RebuildAuthorship(t.Context(), want); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "version":
				if _, err := c.DB.Exec(`UPDATE meta SET value=? WHERE key='authorship_state_version'`, "authorship-state-v1/"+authorshipVersion); err != nil {
					t.Fatal(err)
				}
			case "identity":
				execSQL(t, c.DB, `INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('c','other','native-alias',1,'{}')`)
			case "automation":
				execSQL(t, c.DB, `UPDATE conversations SET harness='codex/codex_exec' WHERE id='c'`)
			case "backfill":
				execSQL(t, c.DB, `UPDATE messages SET text='Please recheck the changed parser' WHERE id='m'`)
			case "same-time":
				execSQL(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('same','c','same','user','message','A turn at the previous watermark','2026-09-01T12:00:00.000Z','same')`)
			case "suffix-limit":
				override(t, &authorshipSuffixLimit, 1)
				execTx(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('n1','c','n1','user','message','A new turn','2026-09-02T12:00:00.000Z','n1'),('n2','c','n2','user','message','Another new turn','2026-09-02T13:00:00.000Z','n2')`)
			}
			want, _, _ = c.authorshipGenerations()
			mode := ""
			ctx := withWorkProgress(t.Context(), func(work activityWork) {
				if strings.HasPrefix(work.Phase, "authorship mode:") {
					mode = work.Phase
				}
			})
			if err := c.RebuildAuthorship(ctx, want); err != nil {
				t.Fatal(err)
			}
			if mode == "" || mode == "authorship mode: suffix" || mode == "authorship mode: unchanged" {
				t.Fatalf("missing full fallback: %s", mode)
			}
			inputs, launches, err := c.authorshipInputs(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			results, err := c.classifyInputs(t.Context(), inputs)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.planAuthorship(t.Context(), results, launches)
			if err != nil || len(plan) != 0 {
				t.Fatalf("fallback differs: %v (%v)", plan, err)
			}
		})
	}
}

func TestAuthorshipIncrementalRetainsQuestionsAcrossIdleGap(t *testing.T) {
	t.Run("no user suffix", func(t *testing.T) {
		c, _ := testCatalog(t)
		execTx(t, c.DB,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
			`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('c','w','codex','local','c','codex')`,
			`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('m','c','m','user','message','Please inspect the first parser carefully','2026-09-01T12:00:00.000Z','hash')`)
		rebuild := func() {
			t.Helper()
			want, _, _ := c.authorshipGenerations()
			if err := c.RebuildAuthorship(t.Context(), want); err != nil {
				t.Fatal(err)
			}
		}
		rebuild()
		text := `{"tool":"request_user_input","input":{"questions":[{"question":"Should we publish the fix?","options":[{"label":"Publish the fix now"},{"label":"Wait for review"}]}]}}`
		if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('q','c','q','assistant','tool_call',?,'2026-09-02T12:00:00.000Z','q')`, text); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,call_message_id,started_at,status) VALUES('q','w','c',1,'codex','tool_call','request_user_input','other','q','2026-09-02T12:00:00.000Z','ok')`)
		rebuild() // No user suffix; keep the dirty evidence until a user consumes it.
		execSQL(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('answer','c','answer','user','message','Should we publish the fix?
Publish the fix now','2026-10-02T12:00:00.000Z','answer')`)
		rebuild()
		inputs, launches, err := c.authorshipInputs(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		results, err := c.classifyInputs(t.Context(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.planAuthorship(t.Context(), results, launches)
		if err != nil || len(plan) != 0 {
			t.Fatalf("idle-gap answer differs: %v (%v)", plan, err)
		}
	})

	t.Run("another conversation advances the checkpoint", func(t *testing.T) {
		c, _ := testCatalog(t)
		execTx(t, c.DB,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
			`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('a','w','codex','local','a','codex'),('b','w','codex','local','b','codex')`)
		message := func(id, conversation, role, kind, text string, day int) {
			t.Helper()
			if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES(?,?,?,?,?,?,?,?)`, id, conversation, id, role, kind, text, formatTime(authorAt(day*24*60)), hashBytes([]byte(text))); err != nil {
				t.Fatal(err)
			}
		}
		question := func(id, title, option string, day int) {
			t.Helper()
			text := jsonText(map[string]any{"tool": "request_user_input", "input": map[string]any{"questions": []any{map[string]any{"question": title, "options": []any{map[string]any{"label": option}}}}}})
			message(id, "b", "assistant", "tool_call", text, day)
			if _, err := c.DB.Exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,call_message_id,started_at,status) VALUES(?,'w','b',1,'codex','tool_call','request_user_input','other',?,?,'ok')`, id, id, formatTime(authorAt(day*24*60))); err != nil {
				t.Fatal(err)
			}
		}
		rebuild := func() {
			t.Helper()
			want, _, err := c.authorshipGenerations()
			if err != nil {
				t.Fatal(err)
			}
			mode := ""
			ctx := withWorkProgress(t.Context(), func(work activityWork) {
				if strings.HasPrefix(work.Phase, "authorship mode:") {
					mode = work.Phase
				}
			})
			if err := c.RebuildAuthorship(ctx, want); err != nil {
				t.Fatal(err)
			}
			if mode != "authorship mode: suffix" {
				t.Fatalf("expected suffix replay, got %s", mode)
			}
		}
		message("first", "a", "user", "message", "Please inspect the parser", 0)
		question("old-question", "Should we publish the parser?", "Publish the parser now", 1)
		message("baseline", "a", "user", "message", "Inspect the second grammar rule", 2)
		want, _, _ := c.authorshipGenerations()
		if err := c.RebuildAuthorship(t.Context(), want); err != nil {
			t.Fatal(err)
		}

		// B still has no user turn. Preserve its old question as well as the
		// new one replayed while only A advances the global watermark.
		question("new-question", "Should we ship the formatter?", "Ship the formatter now", 3)
		message("other-activity", "a", "user", "message", "Review the next parser stage", 20)
		rebuild()
		message("later-activity", "a", "user", "message", "Check the remaining grammar", 40)
		rebuild()
		message("answers", "b", "user", "message", "Should we publish the parser?\nPublish the parser now\n\nShould we ship the formatter?\nShip the formatter now", 60)
		rebuild()
		inputs, launches, err := c.authorshipInputs(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		results, err := c.classifyInputs(t.Context(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.planAuthorship(t.Context(), results, launches)
		if err != nil || len(plan) != 0 {
			t.Fatalf("cross-conversation question history differs from full classifier: %v (%v)", plan, err)
		}
	})
}

func classifyAuthorshipUnpaged(ctx context.Context, q queryer, inputs []authorInput, classifier *classifier) ([]authorResult, error) {
	if len(inputs) == 0 {
		return []authorResult{}, nil
	}
	rows, err := q.QueryContext(ctx, authorshipRepliesQuery, formatTime(inputs[0].SentAt.Add(-authorshipWindow)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return walkAuthorshipInputs(ctx, inputs, classifier, func(context.Context) (*authorshipReply, error) {
		for rows.Next() {
			var id, conversation, created, text, kind string
			if err := rows.Scan(&id, &conversation, &created, &text, &kind); err != nil {
				return nil, err
			}
			if at, ok := parseTime(created); ok {
				return &authorshipReply{at: at, origin: textOrigin{conversation, id}, text: text, kind: kind}, nil
			}
		}
		return nil, rows.Err()
	})
}

func TestAuthorshipRebuildLease(t *testing.T) {
	c, _ := testCatalog(t)
	lease, err := fileLease(canonical(c.Path)+".authorship.lock", true)
	if err != nil {
		t.Fatal(err)
	}
	// A second process/handle must not read a checkpoint another builder can
	// be changing between chunks. A lease is independent of SQLite's writer.
	if err := c.RebuildAuthorship(t.Context(), "blocked"); err == nil {
		t.Fatal("concurrent checkpoint builder was admitted")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.RebuildAuthorship(t.Context(), "available"); err != nil {
		t.Fatal(err)
	}
	built, err := c.metaValue(t.Context(), "authorship_generation")
	if err != nil || built != "available" {
		t.Fatalf("lease was not released: %q %v", built, err)
	}
}

func TestAuthorshipIncrementalMirrorIngest(t *testing.T) {
	t.Run("native append", func(t *testing.T) {
		c, _ := testCatalog(t)
		native := WorkspaceRecord{SourceID: "native", SourceKind: "claude", Account: "local", Title: "Native", ActivityAt: formatTime(authorAt(0)), Conversations: []ConversationRecord{{NativeID: "native", Provider: "claude", Account: "local", Harness: "claude-code/cli", Coverage: "complete", StartedAt: formatTime(authorAt(0)), Messages: []MessageRecord{{NativeID: "first", Role: "user", Kind: "message", Text: "Please inspect the parser", CreatedAt: formatTime(authorAt(0)), Selected: true}}}}}
		wrapper := WorkspaceRecord{SourceID: "wrapper", SourceKind: "conductor", Account: "local", Title: "Wrapper", ActivityAt: formatTime(authorAt(0)), Conversations: []ConversationRecord{{NativeID: "conductor:wrapper", Provider: "claude", Account: "local", Harness: "conductor", Coverage: "complete", Aliases: []string{"native"}, StartedAt: formatTime(authorAt(0))}}}
		ingest := func(record WorkspaceRecord) {
			t.Helper()
			tx := seedTx(t, c.DB)
			if _, _, err := ingestWorkspace(tx, record, false); err != nil {
				t.Fatal(err)
			}
			commitSeed(t, tx)
		}
		ingest(native)
		ingest(wrapper)
		want, _, _ := c.authorshipGenerations()
		if err := c.RebuildAuthorship(t.Context(), want); err != nil {
			t.Fatal(err)
		}
		native.ActivityAt = formatTime(authorAt(20))
		native.Conversations[0].Messages = append(native.Conversations[0].Messages, MessageRecord{NativeID: "next", Role: "user", Kind: "message", Text: "Now inspect the second grammar rule", CreatedAt: formatTime(authorAt(20)), Selected: true})
		ingest(native)
		want, _, _ = c.authorshipGenerations()
		mode, classified := "", 0
		ctx := withWorkProgress(t.Context(), func(work activityWork) {
			if strings.HasPrefix(work.Phase, "authorship mode:") {
				mode = work.Phase
			}
			if work.Phase == "classifying authorship" {
				classified = int(work.Total)
			}
		})
		if err := c.RebuildAuthorship(ctx, want); err != nil {
			t.Fatal(err)
		}
		if mode != "authorship mode: suffix" || classified != 1 {
			t.Fatalf("native append in mirror: %s classified=%d", mode, classified)
		}
		inputs, launches, err := c.authorshipInputs(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		results, err := c.classifyInputs(t.Context(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.planAuthorship(t.Context(), results, launches)
		if err != nil || len(plan) != 0 {
			t.Fatalf("mirror ingest differs: %v (%v)", plan, err)
		}
	})

	t.Run("launch command belongs to the suppressed mirror", func(t *testing.T) {
		c, _ := testCatalog(t)
		execTx(t, c.DB,
			`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,location) VALUES
			 ('native','claude','local','native','Native','2026-09-01','/project'),
			 ('mirror','conductor','local','mirror','Mirror','2026-09-01','/project'),
			 ('run','claude','local','run','Run','2026-09-01','/project/run')`,
			`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness,origin_host_id,started_at) VALUES
			 ('native','native','claude','local','native','claude-code/cli','host','2026-09-01T12:00:00.000Z'),
			 ('mirror','mirror','claude','local','mirror','conductor','host','2026-09-01T12:00:00.000Z'),
			 ('run','run','claude','local','run','claude-code/sdk-cli','host','2026-09-01T12:02:00.000Z')`,
			`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('mirror','native','native-alias',1,'{}')`,
			`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES
			 ('first','native','first','user','message','Please inspect the parser','2026-09-01T12:00:00.000Z','first'),
			 ('launch','mirror','launch','assistant','tool_call','{"tool":"Bash","input":{"command":"claude -p inspect"}}','2026-09-01T12:01:00.000Z','launch'),
			 ('headless-first','run','headless-first','user','message','Inspect the parser stage','2026-09-01T12:03:00.000Z','headless-first')`,
			`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,program,command,call_message_id,started_at,ended_at,status)
			 VALUES('launch','mirror','mirror',1,'claude','tool_call','Bash','command','claude','claude -p inspect','launch','2026-09-01T12:01:00.000Z','2026-09-01T12:05:00.000Z','ok')`,
			`INSERT INTO tool_commands(tool_call_id,position,program,subcommand,command) VALUES('launch',0,'claude','-p','claude -p inspect')`)
		_, launches, err := c.authorshipInputs(t.Context())
		if err != nil || len(launches) != 1 || launches[0].parent != "native" || launches[0].message != "" {
			t.Fatalf("full classifier must attribute the mirror command to its representative without a message anchor: %+v (%v)", launches, err)
		}
		want, _, _ := c.authorshipGenerations()
		if err := c.RebuildAuthorship(t.Context(), want); err != nil {
			t.Fatal(err)
		}
		execSQL(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('next','run','next','user','message','Inspect the next parser stage','2026-09-01T12:10:00.000Z','next')`)
		want, _, _ = c.authorshipGenerations()
		mode := ""
		ctx := withWorkProgress(t.Context(), func(work activityWork) {
			if strings.HasPrefix(work.Phase, "authorship mode:") {
				mode = work.Phase
			}
		})
		if err := c.RebuildAuthorship(ctx, want); err != nil {
			t.Fatal(err)
		}
		if mode != "authorship mode: suffix" {
			t.Fatalf("expected suffix replay, got %s", mode)
		}
		inputs, launches, err := c.authorshipInputs(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		results, err := c.classifyInputs(t.Context(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := c.planAuthorship(t.Context(), results, launches)
		if err != nil || len(plan) != 0 {
			t.Fatalf("mirror launch attribution differs from full classifier: %v (%v)", plan, err)
		}
	})
}

// A Codex filename fork continues its root's execution, so the person's new
// messages there are theirs; a delegated child is still a sub-agent
// (authorship-v12, the rule labeling prepare shares).
func TestAuthorshipContinuationIsTheRootsExecution(t *testing.T) {
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	category := func(id string) string {
		var main string
		if err := catalog.DB.QueryRow(`SELECT COALESCE(main_category,'') FROM message_authorship WHERE message_id=?`, id).Scan(&main); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return main
	}
	if got := category("m-fork-3"); got == spanAutomated {
		t.Errorf("a continuation's new message is %s", got)
	}
	if got := category("m-c1"); got != spanAutomated {
		t.Errorf("a delegated child's prompt is %s, want %s", got, spanAutomated)
	}
}
