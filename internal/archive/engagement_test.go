package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// engagementTestSettings are the fake worker (labelsTestSettings) and a
// token-label model directory for it: the committed tokenizer, a stand-in
// model file and a model.json like the export's. The fake's probability for
// a token and label is labelertest.FakeTokenProb.
func engagementTestSettings(t *testing.T, mode string, labels ...string) (LabelerSettings, EngagementSettings) {
	t.Helper()
	labeler := labelsTestSettings(t, mode)
	if len(labels) == 0 {
		labels = []string{"intent.task", "bounds.prohibition", "bounds.permission", "acceptance.verification", "register.thanks"}
	}
	dir := t.TempDir()
	tokenizer, err := os.ReadFile(filepath.Join(labeler.ModelDir, "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := []byte("stand-in span model " + strings.Join(labels, ","))
	sum := func(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
	meta, _ := json.Marshal(map[string]any{
		"contract": labelrt.InputContractIDsAttentionTokenLabels, "framing": labelrt.TokenLabelFraming, "source_model": "models/fake-spans",
		"labels": labels, "max_seq_tokens": 1024, "max_context_tokens": 128, "window_overlap_tokens": 128,
		"cls_id": labelrt.ClsID, "sep_id": labelrt.SepID, "pad_id": labelrt.PadID, "variants": map[string]string{"fp32": "model.onnx"},
		"files": map[string]any{"model.onnx": map[string]any{"sha256": sum(model)}, "tokenizer.json": map[string]any{"sha256": sum(tokenizer)}},
	})
	for name, data := range map[string][]byte{"tokenizer.json": tokenizer, "model.onnx": model, "model.json": meta} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return labeler, EngagementSettings{Enabled: true, ModelDir: dir}
}

// The engagement fixture's texts. engagementPaste stands for pasted text in
// the middle of what the person typed.
const (
	engagementPaste  = "ERROR importer.go:42 unexpected column"
	engagementTyped1 = "Don't open it yet. "
	engagementTyped2 = " First show me the diff — the café test 🙂 failed."
)

// engagementCatalog seeds one top-level conversation: a long agent message,
// a typed message with a paste in the middle, a tool call, a short agent
// message, a typed message, a typed message with no letter and a pasted one;
// and a sub-agent conversation whose typed message the model never reads.
func engagementCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, _ := testCatalog(t)
	tx := seedTx(t, catalog.DB)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('ws-e','claude','local','ws-e','ws-e','2026-10-01')`)
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at,agent_depth) VALUES
		('c-e','ws-e','claude','local','c-e','2026-10-25T12:00:00.000Z',0),('c-sub','ws-e','claude','local','c-sub','2026-10-25T12:30:00.000Z',1)`)
	message := func(id, conversation, role, kind, text string, order int) {
		exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order) VALUES(?,?,?,?,?,?,?,?,1,?)`,
			id, conversation, id, role, kind, text, "h-"+id, "2026-10-25T12:00:0"+string(rune('0'+order))+".000Z", order)
	}
	authored := func(id, conversation string, order, typedWords int, spans ...authorSpan) {
		raw, _ := json.Marshal(spans)
		exec(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,typed_words,spans_json) VALUES(?,?,'ws-e',?,'2026-10-25',0,?,?)`,
			id, conversation, "2026-10-25T12:00:0"+string(rune('0'+order))+".000Z", typedWords, string(raw))
	}
	u1 := engagementTyped1 + engagementPaste + engagementTyped2
	message("e-a1", "c-e", "assistant", "message", strings.Repeat("I updated one more file. ", 80)+"Shall I open the pull request?", 1)
	message("e-u1", "c-e", "user", "message", u1, 2)
	authored("e-u1", "c-e", 2, 12, authorSpan{Start: 0, End: len(engagementTyped1), Category: "typed"},
		authorSpan{Start: len(engagementTyped1), End: len(engagementTyped1) + len(engagementPaste), Category: "pasted"},
		authorSpan{Start: len(engagementTyped1) + len(engagementPaste), End: len(u1), Category: "typed"})
	message("e-t1", "c-e", "assistant", "tool_call", `{"command":"git diff"}`, 3)
	message("e-a2", "c-e", "assistant", "message", "Here is the diff.", 4)
	message("e-u2", "c-e", "user", "message", "ok thanks, merge it", 5)
	authored("e-u2", "c-e", 5, 4, authorSpan{Start: 0, End: 19, Category: "typed"})
	message("e-u3", "c-e", "user", "message", "12345 ???", 6)
	authored("e-u3", "c-e", 6, 1, authorSpan{Start: 0, End: 9, Category: "typed"})
	message("e-u4", "c-e", "user", "message", engagementPaste, 7)
	authored("e-u4", "c-e", 7, 0, authorSpan{Start: 0, End: len(engagementPaste), Category: "pasted"})
	message("e-s1", "c-sub", "user", "message", "Review the importer and report back.", 1)
	authored("e-s1", "c-sub", 1, 6, authorSpan{Start: 0, End: 36, Category: "typed"})
	commitSeed(t, tx)
	return catalog
}

func engagementRun(t *testing.T, catalog *Catalog, labels LabelerSettings, settings EngagementSettings, opts EngagementRunOptions) *EngagementRunStats {
	t.Helper()
	stats, err := catalog.RunEngagement(context.Background(), labels, settings, opts)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// engagementStates is each state row's outcome, by message.
func engagementStates(t *testing.T, catalog *Catalog) map[string]string {
	t.Helper()
	rows, err := queryMaps(catalog.DB, `SELECT message_id,outcome FROM engagement_messages`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, row := range rows {
		out[firstString(row["message_id"])] = firstString(row["outcome"])
	}
	return out
}

func engagementProcessedAt(t *testing.T, catalog *Catalog, id string) string {
	t.Helper()
	var at, hash string
	if err := catalog.DB.QueryRow(`SELECT processed_at,input_hash FROM engagement_messages WHERE message_id=?`, id).Scan(&at, &hash); err != nil {
		t.Fatal(err)
	}
	return at + "/" + hash
}

// The target text is the typed spans in order, with one marker per run of
// other spans of one category; trimmed, and cut at the code point limit.
func TestEngagementTargetText(t *testing.T) {
	text := "  fix this " + "PASTED-A" + "PASTED-B" + "<harness>" + "and keep the name\n" + "QUOTED" + " ok  "
	at := 0
	span := func(category, part string) authorSpan {
		if text[at:at+len(part)] != part {
			t.Fatalf("fixture: %q is not at %d", part, at)
		}
		at += len(part)
		return authorSpan{Start: at - len(part), End: at, Category: category}
	}
	spans := []authorSpan{span("typed", "  fix this "), span("pasted", "PASTED-A"), span("pasted", "PASTED-B"), span("harness", "<harness>"),
		span("typed", "and keep "), span("typed", "the name\n"), span("quoted", "QUOTED"), span("typed", " ok  ")}
	target := engagementTargetText(text, spans)
	if want := "fix this  ⟦pasted⟧  ⟦harness⟧ and keep the name\n ⟦quoted⟧  ok"; target.text != want {
		t.Fatalf("target %q, want %q", target.text, want)
	}
	find := func(part string) (int, int) {
		i := strings.Index(target.text, part)
		if i < 0 {
			t.Fatalf("%q is not in the target", part)
		}
		return i, i + len(part)
	}
	ranges := func(part string) []string {
		start, end := find(part)
		out := []string{}
		for _, r := range target.sourceRanges(text, start, end) {
			out = append(out, text[r.Start:r.End])
		}
		return out
	}
	for part, want := range map[string][]string{
		"fix this":                      {"fix this"},
		"this  ⟦pasted⟧  ⟦harness⟧ and": {"this", "and"}, // split at the markers, never covering them
		"⟦pasted⟧":                      {},
		"keep the name":                 {"keep the name"}, // two typed spans side by side are one range
		"name\n ⟦quoted⟧  ok":           {"name", "ok"},
		"asted⟧  ⟦harn":                 {},
	} {
		if got := ranges(part); !slices.Equal(got, want) {
			t.Errorf("%q maps to %q, want %q", part, got, want)
		}
	}
	// The hash follows the text, where its pieces lie, and the context.
	base := engagementInputHash(target, "context")
	shifted := engagementTargetText("x"+text, func() []authorSpan {
		moved := slices.Clone(spans)
		for i := range moved {
			moved[i].Start, moved[i].End = moved[i].Start+1, moved[i].End+1
		}
		return moved
	}())
	if shifted.text != target.text || engagementInputHash(shifted, "context") == base || engagementInputHash(target, "other") == base || engagementInputHash(target, "context") != base {
		t.Fatal("the input hash does not follow the text's place in the message and the context")
	}

	// Only typed text counts, and nothing typed leaves nothing to score.
	if pasted := engagementTargetText("PASTED", []authorSpan{{Start: 0, End: 6, Category: "pasted"}}); pasted.text != "⟦pasted⟧" || len(pasted.sourceRanges("PASTED", 0, len(pasted.text))) != 0 {
		t.Fatalf("a pasted message gives %q", pasted.text)
	}
	if digits := engagementTargetText("12345 ???", []authorSpan{{Start: 0, End: 9, Category: "typed"}}); digits.scorable() != "no_letters" {
		t.Fatalf("typed text with no letter is %q", digits.scorable())
	}
	if blank := engagementTargetText("   ", []authorSpan{{Start: 0, End: 3, Category: "typed"}}); blank.text != "" || blank.scorable() != "empty" {
		t.Fatalf("a blank message gives %q (%s)", blank.text, blank.scorable())
	}
	// The limit counts code points; the map still points at the right bytes.
	long := strings.Repeat("é", engagementTargetRunes+50)
	cut := engagementTargetText(long, []authorSpan{{Start: 0, End: len(long), Category: "typed"}})
	if utf8.RuneCountInString(cut.text) != engagementTargetRunes {
		t.Fatalf("the target keeps %d code points", utf8.RuneCountInString(cut.text))
	}
	if got := cut.sourceRanges(long, len(cut.text)-4, len(cut.text)); len(got) != 1 || got[0] != (labelrt.ByteRange{Start: len(cut.text) - 4, End: len(cut.text)}) {
		t.Fatalf("the end of a cut target maps to %v", got)
	}
	// A typed span that is not UTF-8 keeps its bytes' places.
	broken := "ab\xffcd"
	fixed := engagementTargetText(broken, []authorSpan{{Start: 0, End: len(broken), Category: "typed"}})
	if fixed.text != "ab�cd" {
		t.Fatalf("invalid bytes give %q", fixed.text)
	}
	if got := fixed.sourceRanges(broken, len("ab�"), len(fixed.text)); len(got) != 1 || broken[got[0].Start:got[0].End] != "cd" {
		t.Fatalf("text after an invalid byte maps to %v", got)
	}
}

func TestEngagementContext(t *testing.T) {
	if got := engagementContext("short reply", 11); got != "short reply" {
		t.Fatal(got)
	}
	if got := engagementContext("the tail", engagementContextRunes+1); got != "…the tail" {
		t.Fatal(got)
	}
	if got := engagementContext("", 0); got != "" {
		t.Fatal(got)
	}
}

// A run scores the typed messages of top-level conversations and stores
// spans that lie in typed text; later runs score only what changed: the
// message's text, its authorship spans, the agent message before it, or the
// model.
func TestEngagementPass(t *testing.T) {
	catalog := engagementCatalog(t)
	labels, settings := engagementTestSettings(t, "ok")
	ctx := context.Background()

	if dry := engagementRun(t, catalog, labels, settings, EngagementRunOptions{DryRun: true}); dry.Messages != 3 || dry.Windows != 2 || engagementPresent(ctx, catalog.DB) {
		t.Fatalf("a dry run: %+v (tables made: %t)", dry, engagementPresent(ctx, catalog.DB))
	}
	stats := engagementRun(t, catalog, labels, settings, EngagementRunOptions{})
	if stats.Candidates != 3 || stats.MessagesScored != 2 || stats.MessagesSkipped != 1 || !stats.Complete || stats.Spans == 0 {
		t.Fatalf("first run: %+v", stats)
	}
	if states := engagementStates(t, catalog); len(states) != 3 || states["e-u1"] != "scored" || states["e-u2"] != "scored" || states["e-u3"] != "no_letters" {
		t.Fatalf("state rows %v", states)
	}
	model, err := loadEngagementModel(settings)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(catalog.DB, `SELECT s.message_id,s.position,s.label,s.start_byte,s.end_byte,s.score,e.model,m.text FROM engagement_spans s
		JOIN engagement_messages e ON e.message_id=s.message_id JOIN messages m ON m.id=s.message_id ORDER BY s.message_id,s.position`)
	if err != nil {
		t.Fatal(err)
	}
	pasteStart, pasteEnd := len(engagementTyped1), len(engagementTyped1)+len(engagementPaste)
	perMessage := map[string]int{}
	for _, row := range rows {
		id, text := firstString(row["message_id"]), firstString(row["text"])
		start, end := int(integer(row["start_byte"])), int(integer(row["end_byte"]))
		score, _ := row["score"].(float64)
		switch {
		case int(integer(row["position"])) != perMessage[id]:
			t.Fatalf("%s: positions are not 0…n", id)
		case !slices.Contains(model.Labels, firstString(row["label"])) || firstString(row["model"]) != model.identity:
			t.Fatalf("%s: label %v by %v", id, row["label"], row["model"])
		case start < 0 || end > len(text) || start >= end || !utf8.ValidString(text[start:end]) || strings.TrimSpace(text[start:end]) != text[start:end]:
			t.Fatalf("%s: span %d..%d is not trimmed text of the message", id, start, end)
		case id == "e-u1" && start < pasteEnd && end > pasteStart:
			t.Fatalf("e-u1: span %d..%d (%q) covers pasted text", start, end, text[start:end])
		case score < labelrt.TokenLabelThreshold || score > 1:
			t.Fatalf("%s: score %v", id, score)
		}
		perMessage[id]++
	}
	if perMessage["e-u1"] == 0 || perMessage["e-u2"] == 0 || len(perMessage) != 2 {
		t.Fatalf("spans per message %v", perMessage)
	}

	// Nothing changed: the complete run left nothing to look at.
	if again := engagementRun(t, catalog, labels, settings, EngagementRunOptions{}); again.Messages != 0 || again.MessagesScored != 0 {
		t.Fatalf("second run: %+v", again)
	}
	if full := engagementRun(t, catalog, labels, settings, EngagementRunOptions{Full: true}); full.Messages != 3 || full.MessagesScored != 0 || full.MessagesUnchanged != 3 {
		t.Fatalf("a full run with nothing changed: %+v", full)
	}
	rescored := func(what string, want ...string) {
		t.Helper()
		before := map[string]string{}
		for _, id := range []string{"e-u1", "e-u2"} {
			before[id] = engagementProcessedAt(t, catalog, id)
		}
		stats := engagementRun(t, catalog, labels, settings, EngagementRunOptions{})
		changed := []string{}
		for _, id := range []string{"e-u1", "e-u2"} {
			if engagementProcessedAt(t, catalog, id) != before[id] {
				changed = append(changed, id)
			}
		}
		if !slices.Equal(changed, want) || stats.MessagesScored != len(want) {
			t.Fatalf("%s: scored again %v (%+v), want %v", what, changed, stats, want)
		}
		var dirty int
		if err := catalog.DB.QueryRow(`SELECT count(*) FROM engagement_dirty`).Scan(&dirty); err != nil || dirty != 0 {
			t.Fatalf("%s: %d conversations still marked (%v)", what, dirty, err)
		}
	}
	execTx(t, catalog.DB, `UPDATE messages SET text='ok thanks, merge it now' WHERE id='e-u2'`,
		`UPDATE message_authorship SET spans_json='[{"start":0,"end":23,"category":"typed"}]' WHERE message_id='e-u2'`)
	rescored("the message's text changed", "e-u2")
	execTx(t, catalog.DB, `UPDATE messages SET text='Here is the whole diff.' WHERE id='e-a2'`)
	rescored("the agent message before it changed", "e-u2")
	execTx(t, catalog.DB, `UPDATE messages SET text=text||' (rerun)' WHERE id='e-t1'`)
	rescored("a tool call changed")
	execTx(t, catalog.DB, `UPDATE message_authorship SET spans_json='[{"start":0,"end":`+"19"+`,"category":"typed"}]' WHERE message_id='e-u1'`)
	rescored("the message's typed spans changed", "e-u1")

	// Another model (another label list and file) scores everything again.
	_, other := engagementTestSettings(t, "ok", "intent.goal", "inquiry.why")
	replaced := engagementRun(t, catalog, labels, other, EngagementRunOptions{})
	otherModel, _ := loadEngagementModel(other)
	var foreign int
	if err := catalog.DB.QueryRow(`SELECT (SELECT count(*) FROM engagement_messages WHERE model<>?)+(SELECT count(*) FROM engagement_spans WHERE label NOT IN ('intent.goal','inquiry.why'))`, otherModel.identity).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if replaced.MessagesScored != 2 || replaced.MessagesSkipped != 1 || foreign != 0 {
		t.Fatalf("another model: %+v, %d rows of the first model left", replaced, foreign)
	}

	// A message that is no longer typed loses its rows; so does a deleted one.
	execTx(t, catalog.DB, `UPDATE message_authorship SET typed_words=0, spans_json='[{"start":0,"end":23,"category":"pasted"}]' WHERE message_id='e-u2'`, `DELETE FROM messages WHERE id='e-u3'`,
		`DELETE FROM message_authorship WHERE message_id='e-u3'`)
	removed := engagementRun(t, catalog, labels, other, EngagementRunOptions{})
	if states := engagementStates(t, catalog); removed.MessagesRemoved != 2 || len(states) != 1 || states["e-u1"] != "scored" {
		t.Fatalf("after removals: %+v, rows %v", removed, states)
	}
	var orphans int
	if err := catalog.DB.QueryRow(`SELECT count(*) FROM engagement_spans WHERE message_id NOT IN (SELECT message_id FROM engagement_messages)`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("%d spans without a state row (%v)", orphans, err)
	}
}

// A limited run stops between chunks and the next run carries on; a worker
// without sparse outputs is refused rather than read as dense.
func TestEngagementPassLimitAndOldWorker(t *testing.T) {
	catalog := engagementCatalog(t)
	labels, settings := engagementTestSettings(t, "dense")
	if _, err := catalog.RunEngagement(context.Background(), labels, settings, EngagementRunOptions{}); !errors.Is(err, labelrt.ErrModelUnavailable) {
		t.Fatalf("a worker without sparse outputs: %v", err)
	}
	if states := engagementStates(t, catalog); len(states) != 0 {
		t.Fatalf("rows stored from a refused worker: %v", states)
	}
	labels, settings = engagementTestSettings(t, "ok")
	override(t, &engagementChunkMessages, 1)
	first := engagementRun(t, catalog, labels, settings, EngagementRunOptions{Limit: 1})
	// Newest first: the message with no letter, then the newest scored one.
	if states := engagementStates(t, catalog); !first.Cut || first.Complete || first.MessagesScored != 1 || states["e-u2"] != "scored" || states["e-u1"] != "" {
		t.Fatalf("a limited run: %+v, rows %v", first, states)
	}
	rest := engagementRun(t, catalog, labels, settings, EngagementRunOptions{})
	if states := engagementStates(t, catalog); rest.Cut || !rest.Complete || rest.MessagesScored != 1 || states["e-u1"] != "scored" || len(states) != 3 {
		t.Fatalf("the run after it: %+v, rows %v", rest, states)
	}
	status, err := catalog.engagementStatus(context.Background(), Config{Labels: labels, Engagement: settings})
	if err != nil || status["pending"] != 0 || status["messages"] != 3 || status["typed_messages"] != 3 {
		t.Fatalf("status %v (%v)", status, err)
	}
}

// The reader gets each span with its dimension and its range in UTF-16 code
// units; spans that no longer fit the message's text are left out.
func TestEngagementReader(t *testing.T) {
	catalog := engagementCatalog(t)
	labels, settings := engagementTestSettings(t, "ok")
	if before, err := catalog.WorkConversation("ws-e", "c-e"); err != nil || before["engagement"] != nil {
		t.Fatalf("a library without the tables: %v (%v)", before["engagement"], err)
	}
	engagementRun(t, catalog, labels, settings, EngagementRunOptions{})
	// The tables alone show nothing: the reader is as it was until the
	// preview is on.
	if off, err := catalog.WorkConversation("ws-e", "c-e"); err != nil || off["engagement"] != nil {
		t.Fatalf("the setting off with the tables present: %v (%v)", off["engagement"], err)
	} else {
		for _, message := range off["messages"].([]map[string]any) {
			if message["engagement"] != nil {
				t.Fatalf("spans with the setting off: %v", message["id"])
			}
		}
	}
	catalog.EngagementEnabled = true
	// One known span past the emoji: bytes and UTF-16 units differ there.
	text := engagementTyped1 + engagementPaste + engagementTyped2
	start := strings.Index(text, "failed")
	execTx(t, catalog.DB, `DELETE FROM engagement_spans WHERE message_id='e-u1'`,
		`INSERT INTO engagement_spans(message_id,position,label,start_byte,end_byte,score) VALUES('e-u1',0,'bounds.prohibition',0,5,0.91234),('e-u1',1,'acceptance.verification',`+
			fmt.Sprintf("%d,%d", start, start+6)+`,0.75)`)
	conversation, err := catalog.WorkConversation("ws-e", "c-e")
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := conversation["engagement"].(map[string]any)
	if summary == nil || summary["messages_read"] != 3 || summary["pending"] != nil {
		t.Fatalf("summary %v", conversation["engagement"])
	}
	var spans []map[string]any
	for _, message := range conversation["messages"].([]map[string]any) {
		if message["id"] == "e-u1" {
			spans, _ = message["engagement"].([]map[string]any)
		} else if message["role"] != "user" && message["engagement"] != nil {
			t.Fatalf("an agent message carries spans: %v", message["id"])
		}
	}
	units := len(utf16.Encode([]rune(text[:start])))
	if len(spans) != 2 || spans[0]["label"] != "bounds.prohibition" || spans[0]["dimension"] != "bounds" || spans[0]["start"] != 0 || spans[0]["end"] != 5 || spans[0]["score"] != 0.912 ||
		spans[1]["dimension"] != "acceptance" || spans[1]["start"] != units || spans[1]["end"] != units+6 || units == start {
		t.Fatalf("spans %v (the second starts at UTF-16 unit %d, byte %d)", spans, units, start)
	}
	// The text changed under its spans: marked pending, and spans that do
	// not fit are not shown.
	execTx(t, catalog.DB, `UPDATE messages SET text='é' WHERE id='e-u1'`)
	conversation, err = catalog.WorkConversation("ws-e", "c-e")
	if err != nil {
		t.Fatal(err)
	}
	if summary := conversation["engagement"].(map[string]any); summary["pending"] != true {
		t.Fatalf("summary after a change: %v", summary)
	}
	for _, message := range conversation["messages"].([]map[string]any) {
		if message["id"] == "e-u1" && message["engagement"] != nil {
			t.Fatalf("spans of text the message no longer has: %v", message["engagement"])
		}
	}
}

// The Timeline's copy of what the pages display (engagementDisplayMinScore,
// engagementUndisplayed) is the one reader-model.js holds: both are compared
// with testdata/engagement-display.json.
func TestEngagementDisplayRules(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/engagement-display.json")
	if err != nil {
		t.Fatal(err)
	}
	var rules struct {
		MinScore    float64  `json:"min_score"`
		Undisplayed []string `json:"undisplayed"`
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	if rules.MinScore != engagementDisplayMinScore || !slices.Equal(rules.Undisplayed, slices.Sorted(maps.Keys(engagementUndisplayed))) {
		t.Fatalf("display rules %v, %v; the fixture has %+v", engagementDisplayMinScore, engagementUndisplayed, rules)
	}
	// An excerpt keeps each span with a little text on each side, in whole
	// characters; parts that meet are one; a long span keeps its two ends.
	text := "é" + strings.Repeat("a", 60) + " keep the tests green " + strings.Repeat("b", 200) + " and say so " + strings.Repeat("c", 60) + "é"
	span := func(of, part string) labelrt.ByteRange {
		start := strings.Index(of, part)
		return labelrt.ByteRange{Start: start, End: start + len(part)}
	}
	keep, say := span(text, "keep the tests green"), span(text, "say so")
	excerpt, pieces, ok := engagementLaneExcerpt(text, []labelrt.ByteRange{say, keep, span(text, "tests")})
	if !ok || len(pieces) != 2 || !utf8.ValidString(excerpt) || strings.Count(excerpt, engagementLaneGap) != 1 ||
		len(excerpt) != keep.End-keep.Start+say.End-say.Start+4*engagementLaneContextBytes+len(engagementLaneGap) ||
		excerpt[pieces[0].at+keep.Start-pieces[0].start:][:20] != "keep the tests green" || excerpt[pieces[1].at+say.Start-pieces[1].start:][:6] != "say so" {
		t.Fatalf("excerpt %q %v %t", excerpt, pieces, ok)
	}
	if excerpt, pieces, ok := engagementLaneExcerpt(text, []labelrt.ByteRange{{Start: 0, End: len(text)/2 + 1}, {Start: len(text) / 2, End: len(text)}}); !ok || len(text) > engagementLaneQuoteBytes || excerpt != text || len(pieces) != 1 {
		t.Fatalf("an excerpt of the whole text: %q %v %t", excerpt, pieces, ok)
	}
	if _, _, ok := engagementLaneExcerpt(text, []labelrt.ByteRange{{Start: 1, End: 5}}); ok {
		t.Fatal("a span that starts inside a character has an excerpt")
	}
	long := "x " + "start " + strings.Repeat("日本語 ", 200) + "end." + " y"
	excerpt, pieces, ok = engagementLaneExcerpt(long, []labelrt.ByteRange{{Start: 2, End: len(long) - 2}})
	if !ok || len(pieces) != 2 || len(excerpt) > engagementLaneQuoteBytes+len(engagementLaneGap)+2*engagementLaneContextBytes+8 || !utf8.ValidString(excerpt) ||
		!strings.HasPrefix(excerpt, "x start 日本語") || !strings.HasSuffix(excerpt, "end. y") || strings.Count(excerpt, engagementLaneGap) != 1 {
		t.Fatalf("a long span's excerpt %q %v %t", excerpt, pieces, ok)
	}
}

// The chain's engagement stage and its background item exist only where
// engagement_enabled is on; elsewhere the stage is acknowledged unrun and
// the library gets no tables.
func TestEngagementChainStage(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		catalog := engagementCatalog(t)
		labels, settings := engagementTestSettings(t, "ok")
		labels.Enabled, settings.Enabled = false, enabled
		config := Config{Labels: labels, Engagement: settings}
		server := testServer(t, config, catalog)
		if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
			t.Fatal(err)
		}
		if err := server.finishDeferredStages(t.Context(), findingsAuto, map[string]bool{"engagement": true}, nil); err != nil {
			t.Fatal(err)
		}
		schedule, _ := catalog.backgroundSettings(t.Context(), time.Now())
		planned := slices.ContainsFunc(backgroundPlan(schedule, server.backgroundInputsFor(t.Context(), nil), time.Now()), func(plan itemPlan) bool { return plan.ID == "engagement" })
		deferred, _ := catalog.freshnessDeferred(t.Context())
		if row := deferred["engagement"]; row.generation == 0 || row.generation > row.completed {
			t.Fatalf("enabled %t: the stage is still pending: %+v", enabled, row)
		}
		if present := engagementPresent(t.Context(), catalog.DB); present != enabled || planned != enabled {
			t.Fatalf("enabled %t: tables %t, item planned %t", enabled, present, planned)
		}
		if enabled {
			if states := engagementStates(t, catalog); states["e-u1"] != "scored" || states["e-u2"] != "scored" {
				t.Fatalf("the stage scored %v", states)
			}
		}
	}
}
