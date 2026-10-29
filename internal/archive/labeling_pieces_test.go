package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkHumanUnit asserts human-pieces-v2's invariants: pieces tile the
// message in order, each visible target slices the original text exactly
// (or decodes it, for a transformed one), and visible targets separated by
// excluded material have a marker between them.
func checkHumanUnit(t *testing.T, text string, unit humanUnit) {
	t.Helper()
	end, previous := 0, -1
	for index, piece := range unit.Pieces {
		if piece.Start < end || piece.End < piece.Start || piece.End > len(text) || !utf8.ValidString(text[piece.Start:piece.End]) {
			t.Fatalf("piece %d range %d-%d breaks order or UTF-8: %+v", index, piece.Start, piece.End, unit.Pieces)
		}
		end = piece.End
		if piece.VisibleStart < 0 {
			continue
		}
		if piece.Role != pieceTarget {
			t.Fatalf("non-target piece is visible: %+v", piece)
		}
		shown := unit.Visible[piece.VisibleStart:piece.VisibleEnd]
		if piece.Transform == "" && shown != text[piece.Start:piece.End] {
			t.Fatalf("visible %q is not the original %q", shown, text[piece.Start:piece.End])
		}
		if previous >= 0 {
			gap := unit.Visible[unit.Pieces[previous].VisibleEnd:piece.VisibleStart]
			excluded := false
			for _, between := range unit.Pieces[previous+1 : index] {
				excluded = excluded || strings.TrimSpace(text[between.Start:between.End]) != ""
			}
			if excluded != strings.Contains(gap, "⟦") {
				t.Fatalf("pieces joined without a marker, or a marker without excluded text: %q", unit.Visible)
			}
		}
		previous = index
	}
	for _, marker := range unit.Markers {
		value := unit.Visible[marker.Start:marker.End]
		if !strings.HasPrefix(value, "⟦") || !strings.HasSuffix(value, "⟧") || strings.Count(value, "⟦") != 1 {
			t.Fatalf("marker range %+v holds %q", marker, value)
		}
	}
}

func classifyOne(c *classifier, conversation, text string, minutes int) []authorSpan {
	return c.classify(authorInput{ID: conversation + "-message", ConversationID: conversation, Text: text, SentAt: authorAt(minutes)})
}

func pieceWithStatus(unit humanUnit, status string) labelingPiece {
	for _, piece := range unit.Pieces {
		if piece.Status == status {
			return piece
		}
	}
	return labelingPiece{Start: -1}
}

func TestHumanPiecesSeparateTheirMaterial(t *testing.T) {
	reply := "The flaky test fails because the cache is shared between workers and nobody resets it before each case runs"
	cases := []struct {
		name, text, visible string
		prepare             func(*classifier)
		// verified treats re-sent text as the person's, as its source was.
		verified bool
		check    func(t *testing.T, text string, unit humanUnit)
	}{{
		name:    "mixed harness, typed and pasted code",
		text:    "<system-reminder>\nKeep answers short.\n</system-reminder>\nWhy does this fail?\n```go\nfunc main() { panic(\"boom\") }\n```\nIt panics on start.",
		visible: "Why does this fail?\n⟦likely pasted text, context only, 39 chars: \"```go\nfunc main() { panic(\"boom\") }\n```\"⟧\nIt panics on start.",
	}, {
		name:    "quoted agent claim and the person's criticism",
		text:    reply + "\n\nThat is wrong: the cache is per worker already.",
		visible: "⟦quoted agent text, context only, 107 chars: \"" + reply + "\"⟧\n\nThat is wrong: the cache is per worker already.",
		prepare: func(c *classifier) {
			c.agentText.add(authorAt(0), textOrigin{"agent-chat", "agent-reply"}, shingles(wordTokens(reply)))
		},
		check: func(t *testing.T, text string, unit humanUnit) {
			quote := pieceWithStatus(unit, "quoted")
			if quote.Role != pieceContext || quote.SourceMessage != "agent-reply" || !strings.HasPrefix(text[quote.Start:quote.End], "The flaky test") {
				t.Fatalf("quote = %+v", quote)
			}
		},
	}, {
		name:    "a re-sent message of the person's own",
		text:    "Please rerun the migration against staging and compare the row counts with production afterwards",
		visible: "Please rerun the migration against staging and compare the row counts with production afterwards",
		prepare: func(c *classifier) {
			classifyOne(c, "earlier-chat", "Please rerun the migration against staging and compare the row counts with production afterwards", 0)
		},
		verified: true,
		check: func(t *testing.T, text string, unit humanUnit) {
			if piece := pieceWithStatus(unit, "reused_own_message"); piece.Role != pieceTarget || piece.SourceConversation != "earlier-chat" {
				t.Fatalf("re-sent = %+v", unit.Pieces)
			}
		},
	}, {
		name:    "a template button",
		text:    "/compact",
		visible: "/compact",
		check: func(t *testing.T, text string, unit humanUnit) {
			if piece := pieceWithStatus(unit, "reused_template"); piece.Role != pieceTarget {
				t.Fatalf("template = %+v", unit.Pieces)
			}
		},
	}, {
		name:    "a feedback report's human comment",
		text:    embeddedFeedback("Pharos", "Make the \"Save\" button larger.\nIt is hard to read."),
		visible: "⟦excluded: generated page feedback, 165 chars⟧Make the \"Save\" button larger.\nIt is hard to read.⟦excluded: generated page feedback, 240 chars⟧",
		check: func(t *testing.T, text string, unit humanUnit) {
			piece := pieceWithStatus(unit, "feedback_comment")
			if piece.Transform != feedbackJSONTransform || text[piece.Start:piece.End] != `Make the \"Save\" button larger.\nIt is hard to read.` {
				t.Fatalf("comment piece = %+v (%q)", piece, text[piece.Start:piece.End])
			}
			if strings.Contains(unit.Visible, "Selector") || strings.Contains(unit.Visible, "Save your changes") {
				t.Fatalf("generated fields are visible: %q", unit.Visible)
			}
		},
	}, {
		name:    "a secret in typed text",
		text:    "Use the key sk-abcdefghijklmnopqrstuv for staging",
		visible: "Use the key ⟦excluded: redacted secret, 25 chars⟧ for staging",
		check: func(t *testing.T, text string, unit humanUnit) {
			if unit.Secrets != 1 || pieceWithStatus(unit, "redacted_secret").Role != pieceDropped {
				t.Fatalf("secret pieces = %+v", unit.Pieces)
			}
		},
	}, {
		name:    "an app block authorship does not recognize",
		text:    "<in-app-browser-context source=\"ambient-ui-state\">\nThe browser shows /settings\n</in-app-browser-context>\nMake the header sticky",
		visible: "Make the header sticky",
		check: func(t *testing.T, text string, unit humanUnit) {
			if unit.Unrecognized != 1 {
				t.Fatalf("unrecognized = %d", unit.Unrecognized)
			}
		},
	}, {
		// Authorship stops at the unrecognized tag and counts what follows
		// as typed; the harness block after it is dropped all the same.
		name: "a known harness block after an unrecognized one",
		text: "<in-app-browser-context source=\"ambient-ui-state\">\nThe browser shows /settings\n</in-app-browser-context>\n" +
			"<system-reminder>\nBe brief.\n</system-reminder>\n<in-app-browser-context>\nStill /settings\n</in-app-browser-context>\nMake the header sticky",
		visible: "Make the header sticky",
		check: func(t *testing.T, text string, unit humanUnit) {
			if unit.Unrecognized != 2 || targetText(text, unit.Pieces) != "Make the header sticky" {
				t.Fatalf("unrecognized = %d, targets %q", unit.Unrecognized, targetText(text, unit.Pieces))
			}
		},
	}, {
		name:    "re-sent text whose source is unknown",
		text:    "Please rerun the migration against staging and compare the row counts with production afterwards\n\nand hurry",
		visible: "⟦re-sent text of unverified origin, context only, 96 chars: \"Please rerun the migration against staging and compare the row counts with production afterwards\"⟧\n\nand hurry",
		prepare: func(c *classifier) {
			classifyOne(c, "earlier-chat", "Please rerun the migration against staging and compare the row counts with production afterwards", 0)
		},
	}, {
		name:    "an attachment and a question mark",
		text:    "@⟦screenshot.png⟧(.context%2Fattachments%2Fab%2Fscreenshot.png) ?",
		visible: "⟦attachment reference, context only, 63 chars: \"@[screenshot.png](.context%2Fattachments%2Fab%2Fscreenshot.png)\"⟧ ?",
	}, {
		name: "deliberate short replies", text: "+1", visible: "+1",
	}, {
		name: "a lone question mark", text: "?", visible: "?",
	}, {
		name: "an emoji", text: "👍", visible: "👍",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClassifier(nil)
			if tc.prepare != nil {
				tc.prepare(c)
			}
			spans := classifyOne(c, "chat", tc.text, 30)
			var reuse labelingReuse
			if tc.verified {
				reuse = func(_ string, piece labelingPiece) []labelingPiece { return []labelingPiece{piece} }
			}
			unit := humanPieces(tc.text, spans, reuse)
			if unit.Exclude != "" || unit.Visible != tc.visible {
				t.Fatalf("visible = %q (excluded %q), want %q; spans %+v", unit.Visible, unit.Exclude, tc.visible, spans)
			}
			checkHumanUnit(t, tc.text, unit)
			if tc.check != nil {
				tc.check(t, tc.text, unit)
			}
		})
	}
}

func TestHumanPiecesExcludeResidueAndEmptyTargets(t *testing.T) {
	reply := "The flaky test fails because the cache is shared between workers and nobody resets it before each case runs"
	c := newClassifier(nil)
	c.agentText.add(authorAt(0), textOrigin{"agent-chat", "agent-reply"}, shingles(wordTokens(reply)))
	text := reply + "\n\n?"
	if unit := humanPieces(text, classifyOne(c, "chat", text, 10), nil); unit.Exclude != "residual_punctuation" {
		t.Fatalf("punctuation left after a quote = %+v", unit)
	}
	pasted := "```\nlog line one\nlog line two\n```"
	if unit := humanPieces(pasted, classifyOne(c, "other", pasted, 20), nil); unit.Exclude != "no_target_text" {
		t.Fatalf("a wholly pasted message = %+v", unit)
	}
}

func TestQuestionFormAnswersAreActions(t *testing.T) {
	c := newClassifier(nil)
	c.addAuthorshipTool(authorAt(0), textOrigin{"chat", "question"},
		`{"tool":"AskUserQuestion","input":{"questions":[{"question":"Which mode?","options":[{"label":"Records (Recommended)"},{"label":"Text"}]},{"question":"Anything else?"}]}}`)
	text := "Which mode?\nRecords\n\nAnything else?\nAlso check mobile"
	spans := classifyOne(c, "chat", text, 5)
	if len(c.answers) != 1 {
		t.Fatalf("answers = %+v", c.answers)
	}
	answer := c.answers[0]
	if answer.Title != "Which mode?" || answer.Option != "Records (Recommended)" || text[answer.Start:answer.End] != "Records" ||
		text[answer.TitleStart:answer.TitleEnd] != "Which mode?" || answer.Origin.message != "question" {
		t.Fatalf("answer = %+v", answer)
	}
	// Asked again: the one answer line belongs to the most recent asking.
	c.addAuthorshipTool(authorAt(1), textOrigin{"chat", "asked-again"},
		`{"tool":"AskUserQuestion","input":{"questions":[{"question":"Which mode?","options":[{"label":"Records"},{"label":"Text"}]}]}}`)
	if again := classifyOne(c, "chat", "Which mode?\nText", 6); len(again) == 0 || len(c.answers) != 1 || c.answers[0].Origin.message != "asked-again" || c.answers[0].Option != "Text" {
		t.Fatalf("answers to a repeated question = %+v", c.answers)
	}
	unit := humanPieces(text, spans, nil)
	if !strings.HasSuffix(unit.Visible, "Also check mobile") || strings.Contains(unit.Visible, "Records") {
		t.Fatalf("visible = %q", unit.Visible)
	}
	checkHumanUnit(t, text, unit)
}

func TestChunkingKeepsEveryCharacterAtBoundaries(t *testing.T) {
	paragraph := strings.Repeat("A sentence about the work. ", 150) // 4,050 characters
	astral := strings.Repeat("😀", 7000)
	combining := strings.Repeat("é", 4000) // 8,000 code points, no spaces
	for name, text := range map[string]string{
		"paragraphs":      paragraph + "\n\n" + paragraph + "\n\n" + paragraph,
		"sentences":       strings.Repeat("Short one. ", 1200),
		"astral emoji":    astral,
		"combining marks": combining,
		"joined emoji":    strings.Repeat("👩‍💻", 2500),
		// One leading character puts each sequence across the 6,000 boundary.
		"misaligned combining marks": "x" + combining,
		"misaligned joined emoji":    "x" + strings.Repeat("\U0001F469\u200d\U0001F4BB", 2500),
		"misaligned flags":           "x" + strings.Repeat("\U0001F1EB\U0001F1F7", 4000),
		"variation selectors":        "x" + strings.Repeat("\u2764\ufe0f", 4000),
	} {
		t.Run(name, func(t *testing.T) {
			chunks := chunkLabelingText(text, labelingChunkChars, nil)
			if name == "misaligned flags" && utf8.RuneCountInString(text[:chunks[0].End])%2 != 1 {
				t.Fatalf("a flag was split at %d", chunks[0].End)
			}
			if len(chunks) < 2 {
				t.Fatalf("%d chunks", len(chunks))
			}
			var joined strings.Builder
			end := 0
			for _, chunk := range chunks {
				part := text[chunk.Start:chunk.End]
				if chunk.Start != end || !utf8.ValidString(part) || utf8.RuneCountInString(part) > labelingChunkChars || part == "" {
					t.Fatalf("chunk %+v of %d", chunk, len(text))
				}
				if chunk.Start > 0 && joinsPrevious(text, chunk.Start) {
					t.Fatalf("chunk starts inside a character sequence at %d", chunk.Start)
				}
				joined.WriteString(part)
				end = chunk.End
			}
			if joined.String() != text {
				t.Fatal("chunks lose text")
			}
		})
	}
	text := strings.Repeat("word ", 700) + "\n\n" + strings.Repeat("next ", 400)
	chunks := chunkLabelingText(text, labelingChunkChars, nil)
	if len(chunks) != 1 {
		t.Fatalf("text under the limit split: %+v", chunks)
	}
	// A cut never falls inside a marker.
	marker := "⟦excluded: quoted text, 400 chars⟧"
	text = strings.Repeat("a", 5990) + marker + strings.Repeat("b", 3000)
	at := strings.Index(text, marker)
	chunks = chunkLabelingText(text, labelingChunkChars, []labelingChunk{{at, at + len(marker)}})
	if chunks[0].End != at {
		t.Fatalf("cut inside the marker: %+v", chunks)
	}
	text = strings.Repeat("x", 3500) + "\n\n" + strings.Repeat("y", 3500)
	if chunks := chunkLabelingText(text, labelingChunkChars, nil); text[chunks[0].Start:chunks[0].End] != strings.Repeat("x", 3500)+"\n\n" {
		t.Fatalf("paragraph boundary not preferred: %+v", chunks)
	}
}

func TestLabelingContextReproducesTheMeasuredConstruction(t *testing.T) {
	user := strings.Repeat("u", 1500) + "é"
	assistant := strings.Repeat("a", 300) + "😀"
	full, prompt, excerpt := labelingContext(user, assistant)
	want := "Previous user request:\n" + lastRunes(user, 1400) + "\nPrevious assistant message:\n" + lastRunes(assistant, 800)
	if full != want || utf8.RuneCountInString(prompt) != 600 || !strings.HasSuffix(full, prompt) || !excerpt || !strings.HasSuffix(prompt, "a😀") {
		t.Fatalf("context = %d runes, prompt %d", utf8.RuneCountInString(full), utf8.RuneCountInString(prompt))
	}
	// Without a previous user request the assistant heading keeps its newline,
	// which strip() then removes, as in the Python construction.
	if full, _, excerpt := labelingContext("", "Done."); full != "Previous assistant message:\nDone." || excerpt {
		t.Fatalf("assistant only = %q", full)
	}
	if full, prompt, _ := labelingContext("", ""); full != "" || prompt != "" {
		t.Fatalf("no context = %q", full)
	}
	// Over 2,200 characters keeps the first 2,200 before the last 600.
	long := strings.Repeat("b", 1400)
	full, _, _ = labelingContext(long, strings.Repeat("c", 800)+" token=abcdef")
	if utf8.RuneCountInString(full) != 2200 || strings.Contains(full, "abcdef") {
		t.Fatalf("capped context = %d runes", utf8.RuneCountInString(full))
	}
	if full, _, _ := labelingContext("use sk-abcdefghijklmnopqrstuv now", ""); !strings.Contains(full, "[REDACTED_API_KEY]") {
		t.Fatalf("secret kept: %q", full)
	}
	// A token straddling the 800-character cut of the agent reply.
	token := "abcdefghijklmnopqrstuvwxyz0123"
	reply := "Use Authorization: Bearer " + token + strings.Repeat(" more text", 77)
	if full, _, _ := labelingContext("", reply); strings.Contains(full, token[5:]) {
		t.Fatalf("a token fragment survived the cut: %q", full[:80])
	}
}

func TestLabelingSplitIsDeterministicWithAFreshHoldout(t *testing.T) {
	counts := map[string]int{}
	for index := 0; index < 2000; index++ {
		family := labelingID("f_", labelingSplitsVersion, string(rune('a'+index%26))+strings.Repeat("x", index))
		split := labelingSplit(family, "2026-01-01T00:00:00.000Z", "2026-10-13T00:00:00.000Z")
		if split != labelingSplit(family, "2026-01-01T00:00:00.000Z", "2026-10-13T00:00:00.000Z") {
			t.Fatal("split is not deterministic")
		}
		counts[split]++
	}
	if counts["train"] < 1500 || counts["dev"] < 120 || counts["test"] < 120 || counts["fresh"] != 0 {
		t.Fatalf("splits = %v", counts)
	}
	if split := labelingSplit("f_any", "2026-10-20T00:00:00.000Z", "2026-10-13T00:00:00.000Z"); split != "fresh" {
		t.Fatalf("recent family = %s", split)
	}
}

func TestLabelingTaxonomyIsTheMeasuredFile(t *testing.T) {
	sum := sha256.Sum256(labelingTaxonomy)
	if hex.EncodeToString(sum[:]) != "23b30c325065f3727c8b2f58f81ebab60a47d61ed99e9350895401e1bb67ff30" {
		t.Fatal("the embedded taxonomy differs from the measured candidate-0.3 file")
	}
	var taxonomy struct {
		Version string `json:"version"`
		Rows    []any  `json:"rows"`
	}
	if err := json.Unmarshal(labelingTaxonomy, &taxonomy); err != nil || taxonomy.Version != labelingTaxonomyVersion || len(taxonomy.Rows) != 54 {
		t.Fatalf("taxonomy %q with %d rows: %v", taxonomy.Version, len(taxonomy.Rows), err)
	}
}

func TestPackLabelingRequests(t *testing.T) {
	// Overhead 1,000 tokens, budget 3,000: items may add 2,000 per request.
	sizes := []float64{100, 200, 300, 400, 500, 2500, 50, 1000, 1000, 1000}
	chars := make([]int, len(sizes))
	got, oversize := packLabelingRequests(sizes, chars, 4, 1000, 3000, 0)
	if want := "[[0 1 2 3] [4 6 7] [8 9]]"; fmt.Sprint(got) != want || fmt.Sprint(oversize) != "[5]" {
		t.Fatalf("requests = %v (oversize %v), want %s and [5]", got, oversize, want)
	}
	if got, _ := packLabelingRequests(sizes, chars, 2, 0, 1_000_000, 0); len(got) != 5 || len(got[0]) != 2 {
		t.Fatalf("two per request = %v", got)
	}
	// The character cap binds at its boundary: 600+400 fits 1,000, one more does not.
	got, oversize = packLabelingRequests([]float64{1, 1, 1, 1}, []int{600, 400, 1, 1001}, 4, 0, 1_000_000, 1000)
	if fmt.Sprint(got) != "[[0 1] [2]]" || fmt.Sprint(oversize) != "[3]" {
		t.Fatalf("character cap = %v (oversize %v)", got, oversize)
	}
}

func TestLabelingItemTokensCountDenseScripts(t *testing.T) {
	for _, tc := range []struct {
		text string
		want float64
	}{
		{strings.Repeat("plain words ", 100), 1200 / labelingCharsPerToken},
		{strings.Repeat("漢字", 500), 1500}, // 1,000 characters, 3,000 bytes
		{strings.Repeat("😀", 300), 600},   // 300 characters, 1,200 bytes
	} {
		if got := labelingItemTokens(tc.text, labelingCharsPerToken); got != tc.want {
			t.Errorf("%q…: %.1f tokens, want %.1f", tc.text[:8], got, tc.want)
		}
	}
}

func TestLabelingCopyStableSeesChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.sqlite3")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := map[string]any{"files": labelingCopyFiles(path)}
	if err := labelingCopyStable(path, access); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-shm", []byte("opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := labelingCopyStable(path, access); err == nil || !strings.Contains(err.Error(), "shm") {
		t.Fatalf("a new -shm = %v", err)
	}
	if err := os.WriteFile(path+"-wal", []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := labelingCopyStable(path, access); err == nil || !strings.Contains(err.Error(), "write-ahead") {
		t.Fatalf("a new WAL = %v", err)
	}
}

func TestReuseNeedsOneAttributionInItsSource(t *testing.T) {
	phrase := "please rerun the whole migration against staging and compare every row count"
	source := phrase + "\n\n" + phrase
	p := &labelingPass{targetCache: map[string][]bool{}, sessionKind: map[string]string{}, authorship: map[string]*authorResult{
		"source": {Input: authorInput{ID: "source", Text: source}, Spans: []authorSpan{
			{Start: 0, End: len(phrase) + 2, Category: spanQuoted, Reason: "Quoted with >"},
			{Start: len(phrase) + 2, End: len(source), Category: spanTyped}}},
	}}
	piece := labelingPiece{Role: pieceTarget, Status: "reused_own_message", Start: 0, End: len(phrase), SourceMessage: "source"}
	if parts := p.reusePieces(phrase, piece); parts != nil {
		t.Fatalf("a phrase quoted once and typed once resolved as %+v", parts)
	}
	// Split by the source's attribution, each part names its own source bytes.
	mixed := "first words were typed here and then " + phrase
	p.authorship["mixed"] = &authorResult{Input: authorInput{ID: "mixed", Text: mixed}, Spans: []authorSpan{
		{Start: 0, End: len(mixed) - len(phrase), Category: spanTyped},
		{Start: len(mixed) - len(phrase), End: len(mixed), Category: spanQuoted, Reason: "Quoted with >"}}}
	copied := "  " + mixed
	split := p.reusePieces(copied, labelingPiece{Role: pieceTarget, Status: "reused_own_message", Start: 0, End: len(copied), SourceMessage: "mixed"})
	if len(split) != 2 || split[0].SourceStart != 0 || split[0].SourceEnd != len(mixed)-len(phrase) ||
		split[1].SourceStart != len(mixed)-len(phrase) || split[1].SourceEnd != len(mixed) || split[1].Status != "reused_unverified" {
		t.Fatalf("split parts = %+v", split)
	}
	p.authorship["source"].Spans = []authorSpan{{Start: 0, End: len(source), Category: spanTyped}}
	p.targetCache = map[string][]bool{}
	parts := p.reusePieces(phrase, piece)
	if len(parts) != 1 || parts[0].Role != pieceTarget || parts[0].SourceStart != 0 || parts[0].SourceEnd != len(phrase) {
		t.Fatalf("typed twice = %+v", parts)
	}
}

func TestParseQuestionAnswers(t *testing.T) {
	call := `{"tool":"AskUserQuestion","input":{"questions":[{"question":"Which mode?","options":[{"label":"Records"},{"label":"Text"}]},` +
		`{"question":"Which checks?","multiSelect":true,"options":[{"label":"Lint"},{"label":"Tests"}]},{"question":"Name?","options":[{"label":"Keep"}]}]}}`
	result := `{"content":"User has answered your questions: \"Which mode?\"=\"Records\", \"Which checks?\"=\"Lint, Tests\", \"Name?\"=\"Call it Atlas\". You can now continue with the user's answers in mind.","tool":"AskUserQuestion"}`
	got := []string{}
	for _, answer := range parseQuestionAnswers(call, result) {
		got = append(got, answer.kind+":"+answer.question+"="+answer.option+answer.text)
	}
	want := "[submitted_option:Which mode?=Records submitted_option:Which checks?=Lint submitted_option:Which checks?=Tests free_text_answer:Name?=Call it Atlas]"
	if fmt.Sprint(got) != want {
		t.Fatalf("answers = %v", got)
	}
	codexCall := `{"tool":"request_user_input","input":{"questions":[{"id":"mode","question":"Which mode?","options":[{"label":"Records"}]}]}}`
	codexResult := `{"content":"{\"answers\":{\"mode\":{\"answers\":[\"Records\"]}}}","tool":"request_user_input"}`
	if answers := parseQuestionAnswers(codexCall, codexResult); len(answers) != 1 || answers[0].option != "Records" {
		t.Fatalf("codex answers = %+v", answers)
	}
	conductor := `{"content":[{"type":"text","text":"User responses:\n1. Text\n2. Lint\n3. Something else"}],"is_error":false}`
	got = got[:0]
	for _, answer := range parseQuestionAnswers(call, conductor) {
		got = append(got, answer.kind+":"+answer.question+"="+answer.option+answer.text)
	}
	if fmt.Sprint(got) != "[submitted_option:Which mode?=Text submitted_option:Which checks?=Lint free_text_answer:Name?=Something else]" {
		t.Fatalf("numbered answers = %v", got)
	}
	if answers := parseQuestionAnswers(call, `{"content":"Records","tool":"AskUserQuestion"}`); len(answers) != 0 {
		t.Fatalf("an unrecognized result was parsed: %+v", answers)
	}
}

func TestLabelingSentencesKeepOriginalRanges(t *testing.T) {
	text := "  First one. Second? Third!\nLast line without end  "
	got := []string{}
	for _, r := range labelingSentences(text, 0, len(text)) {
		got = append(got, text[r[0]:r[1]])
	}
	if fmt.Sprint(got) != "[First one. Second? Third! Last line without end]" {
		t.Fatalf("sentences = %q", got)
	}
}

func TestMarkerChunksAttachToTheirNeighbors(t *testing.T) {
	// A 6,000-character target ending exactly at the cut, then a quote marker.
	target := strings.Repeat("a", labelingChunkChars)
	marker := `⟦quoted agent text, context only, 30 chars: "the quote"⟧`
	visible := target + marker
	pieces := []labelingPiece{{Role: pieceTarget, Start: 0, End: len(target), VisibleStart: 0, VisibleEnd: len(target)}}
	chunks, attached := attachMarkerChunks(chunkLabelingText(visible, labelingChunkChars, []labelingChunk{{len(target), len(visible)}}), pieces)
	if attached != 1 || len(chunks) != 1 || visible[chunks[0].Start:chunks[0].End] != visible {
		t.Fatalf("trailing marker: %+v (%d attached)", chunks, attached)
	}
	// A leading marker joins the item after it.
	visible = marker + target
	pieces = []labelingPiece{{Role: pieceTarget, Start: 0, End: len(target), VisibleStart: len(marker), VisibleEnd: len(visible)}}
	chunks, attached = attachMarkerChunks([]labelingChunk{{0, len(marker)}, {len(marker), len(visible)}}, pieces)
	if attached != 1 || len(chunks) != 1 || chunks[0].Start != 0 || chunks[0].End != len(visible) {
		t.Fatalf("leading marker: %+v", chunks)
	}
}

// Known harness blocks after an unrecognized tag are dropped only in
// user-role messages; agent cohorts drop the unrecognized tag alone, as
// before.
func TestCleanPiecesDropsLaterHarnessOnlyForUserRoles(t *testing.T) {
	text := "<in-app-browser-context>\nThe browser shows /settings\n</in-app-browser-context>\n<system-reminder>\nBe brief.\n</system-reminder>\nShip it"
	for _, userRole := range []bool{true, false} {
		pieces, tags, _ := cleanPieces(text, []labelingPiece{{Role: pieceTarget, Status: "agent_text", Start: 0, End: len(text)}}, userRole)
		target := strings.TrimSpace(targetText(text, renderPieces(text, pieces).Pieces))
		want := "Ship it"
		if !userRole {
			want = "<system-reminder>\nBe brief.\n</system-reminder>\nShip it"
		}
		if tags != 1 || target != want {
			t.Fatalf("user role %v: tags %d, target %q", userRole, tags, target)
		}
	}
}
