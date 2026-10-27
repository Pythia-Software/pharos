package archive

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func agentationFeedback(note string) string {
	return "## Page Feedback: /settings\n**Viewport:** 1440×900\n\n### 1. button \"Save\"\n**Location:** main > button.save\n**Selected text:** \"Save your changes\"\n**Feedback:** " + note
}

func embeddedFeedback(app, note string) string {
	encoded, _ := json.Marshal(note)
	return "# " + app + " UI feedback\n\nGenerated: 2026-10-13T12:00:00Z\n\nPlease address each annotation below and verify the result in the rendered UI.\n\n## Annotation 1\n\n- Feedback: " + string(encoded) + "\n- View: Settings (`#settings`)\n- Element: Save (`<button>`)\n- Selector: `#save`\n- Bounds: x=10, y=20, width=100, height=50\n- Element text: \"Save your changes\"\n- Selected text: \"Generated page text\"\n- Computed styles: `{\"display\":\"block\"}`"
}

func TestAuthorshipFeedbackFormats(t *testing.T) {
	note := "Make this button larger and easier to read"
	cases := map[string]string{
		"standard":            agentationFeedback(note),
		"multiline page text": strings.Replace(agentationFeedback(note), "Save your changes\"\n**Feedback", "Save your changes\nGenerated page continuation\"\n**Feedback", 1),
		"CRLF":                strings.ReplaceAll(embeddedFeedback("Pharos", note), "\n", "\r\n"),
		"detailed":            strings.Replace(agentationFeedback(note), "**Selected text:**", "**Classes:** save primary\n**Position:** 10px, 20px (100×50px)\n**Selected text:**", 1),
		"forensic":            "## Page Feedback: /settings\n\n**Environment:**\n- Viewport: 1440×900\n- User Agent: browser text\n\n---\n\n### 1. button\n**Full DOM Path:** body > main > button\n**Accessibility:** role button focusable\n**Nearby Elements:** generated surrounding text\n**Feedback:** " + note,
		"compact":             "## Page Feedback: /settings\n\n1. **button \"Save\"** (src/button.tsx:12): " + note + " (re: \"Save your changes\")",
		"compact CRLF":        "## Page Feedback: /settings\r\n\r\n1. **button \"Save\"** (src/button.tsx:12): " + note + " (re: \"Save your changes\")\r\n",
		"Pharos":              embeddedFeedback("Pharos", note),
		"Alexandria":          embeddedFeedback("Alexandria", note),
		"AI Work Archive":     embeddedFeedback("AI Work Archive", note),
		"custom embedded":     embeddedFeedback("Another project", note),
		"design preview":      designFeedback(note),
		"design CRLF":         strings.ReplaceAll(designFeedback(note), "\n", "\r\n"),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
			if got := categoryText(text, spans, spanTyped); got != note {
				t.Fatalf("typed = %q; spans = %#v", got, spans)
			}
			counts := countSpans(text, spans)
			if counts.words[spanTyped] != 8 || counts.words[spanHarness] == 0 || counts.words[spanPasted] != 0 {
				t.Fatalf("counts = %#v", counts)
			}
			end := 0
			for _, span := range spans {
				if span.Start != end || span.End < span.Start {
					t.Fatalf("invalid coverage: %#v", spans)
				}
				end = span.End
			}
			if end != len(text) {
				t.Fatalf("coverage ends at %d of %d", end, len(text))
			}
		})
	}
}

func TestAuthorshipFeedbackDoesNotSwallowFollowingAgentOutput(t *testing.T) {
	note := "Make this larger"
	for name, tail := range map[string]string{
		"compact list after standard":     "1. **Root cause**: The layout needs changes.\n2. **Proposed fix**: Update mainPanel.saveButton.\n3. **Verification**: Run all the tests.",
		"restarted standard headings":     "### 1. Plan\nUpdate the layout.\n### 2. Checks\nRun the tests.",
		"next number without context":     "### 2. Plan\nUpdate the layout.\n### 3. Checks\nRun the tests.",
		"other heading before annotation": "## Pasted plan\nChange the layout.\n\n### 2. button\n**Location:** main > button\n**Feedback:** This belongs to the pasted plan.",
	} {
		t.Run(name, func(t *testing.T) {
			text := agentationFeedback(note) + "\n\n" + tail
			spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
			if got := categoryText(text, spans, spanTyped); got != note {
				t.Fatalf("typed = %q; spans = %#v", got, spans)
			}
			baseline := newClassifier(nil).classify(authorInput{ID: "tail", ConversationID: "d", Text: tail, SentAt: authorAt(0)})
			if got, want := categoryText(text, spans, spanPasted), categoryText(tail, baseline, spanPasted); got != want || want == "" {
				t.Fatalf("pasted tail = %q; without report = %q", got, want)
			}
		})
	}
}

func TestAuthorshipFeedbackCompactSequence(t *testing.T) {
	text := "## Page Feedback: /settings\n\n1. **button**: Make this larger\n\n2. **paragraph**: Use darker text\n\n" +
		"1. **Root cause**: The layout needs changes.\n2. **Proposed fix**: Update mainPanel.saveButton.\n3. **Verification**: Run all the tests."
	spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanTyped); got != "Make this larger|Use darker text" {
		t.Fatalf("typed = %q; spans = %#v", got, spans)
	}
}

func TestAuthorshipFeedbackBoundaries(t *testing.T) {
	text := "<system_instruction>\nHarness instructions\n</system_instruction>\n\nPlease fix these.\n\n" + agentationFeedback("Make this larger\nKeep the label readable") + "\n\nAlso check mobile.\n\n" + embeddedFeedback("Pharos", "Move this left") + "\n\nThanks again."
	spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanTyped); got != "Please fix these.|Make this larger\nKeep the label readable|Also check mobile.|Move this left|Thanks again." {
		t.Fatalf("typed = %q; spans = %#v", got, spans)
	}
	inline := "Please address this. " + agentationFeedback("Move left")
	inlineSpans := newClassifier(nil).classify(authorInput{ID: "inline", ConversationID: "c", Text: inline, SentAt: authorAt(0)})
	if got := categoryText(inline, inlineSpans, spanTyped); got != "Please address this.|Move left" {
		t.Fatalf("inline header typed = %q", got)
	}
	// Multiple annotations must retain all notes, without their numbered titles.
	text = agentationFeedback("Make this larger\n\nKeep the label readable") + "\n\n### 2. paragraph\n**Location:** main > p\n**Feedback:** Use darker text"
	spans = newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanTyped); got != "Make this larger\n\nKeep the label readable|Use darker text" {
		t.Fatalf("typed = %q", got)
	}
}

func TestAuthorshipFeedbackJSONEscapes(t *testing.T) {
	text := embeddedFeedback("Pharos", "Café looks good\nMove\tthis left\n\nKeep it \"readable\".")
	spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := countSpans(text, spans).words[spanTyped]; got != 9 {
		t.Fatalf("typed words = %d; spans = %#v", got, spans)
	}
	for _, span := range spans {
		if span.Category == spanTyped && (strings.Contains(text[span.Start:span.End], `\n`) || strings.Contains(text[span.Start:span.End], `\t`)) {
			t.Fatalf("whitespace escapes counted: %#v", span)
		}
	}
}

func TestAuthorshipFeedbackProtectsHumanFields(t *testing.T) {
	c := newClassifier(nil)
	c.classify(authorInput{ID: "before", ConversationID: "c", Text: "Start", SentAt: authorAt(0)})
	note := strings.Repeat("Please make the label readable and increase its contrast. ", 20) + "Use mainPanel.saveButton for this."
	text := embeddedFeedback("Pharos", note)
	spans := c.classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanTyped); got != note {
		t.Fatalf("human note lost to heuristics: %q", got)
	}
	for _, span := range spans {
		if span.Category == spanTyped && span.Reason != "Human feedback comment" {
			t.Fatalf("reason = %q", span.Reason)
		}
	}
}

func TestAuthorshipFeedbackKeepsCopyEvidence(t *testing.T) {
	note := "Please increase the contrast of the text so it is easier to read in sunlight"
	first := authorInput{ID: "first", ConversationID: "c", Text: agentationFeedback(note), SentAt: authorAt(0)}
	c := newClassifier(nil)
	c.classify(first)
	second := authorInput{ID: "second", ConversationID: "d", Text: embeddedFeedback("Pharos", note), SentAt: authorAt(1)}
	spans := c.classify(second)
	if got := categoryText(second.Text, spans, spanResent); got != note || countSpans(second.Text, spans).words[spanTyped] != 0 {
		t.Fatalf("re-sent = %q; spans = %#v", got, spans)
	}
	for _, span := range spans {
		if span.Category == spanResent && (span.Source != "c" || span.Message != "first") {
			t.Fatalf("origin = %#v", span)
		}
	}
	c = newClassifier(nil)
	c.agentText.add(authorAt(0), textOrigin{"agent", "reply"}, shingles(wordTokens(note)))
	spans = c.classify(second)
	if got := categoryText(second.Text, spans, spanQuoted); got != note {
		t.Fatalf("quoted = %q", got)
	}
	// Generated prose never seeds later copy/re-send matches.
	c = newClassifier(nil)
	c.classify(first)
	metadata := "Save your changes and review all settings before continuing to the next page"
	c.classify(authorInput{ID: "meta", ConversationID: "c", Text: strings.Replace(agentationFeedback("Move left"), "Save your changes", metadata, 1), SentAt: authorAt(2)})
	spans = c.classify(authorInput{ID: "human", ConversationID: "e", Text: metadata, SentAt: authorAt(3)})
	if got := categoryText(metadata, spans, spanTyped); got != metadata {
		t.Fatalf("metadata contaminated copy evidence: %#v", spans)
	}
}

func TestAuthorshipFeedbackDoesNotClaimExamplesOrAutomatedNotes(t *testing.T) {
	for name, text := range map[string]string{
		"fenced example":   "Please review this example:\n```markdown\n" + agentationFeedback("Move left") + "\n```",
		"ordinary heading": "## Page Feedback: discussion\nI prefer larger buttons.",
		"ordinary label":   "Feedback: please fix the button\nSelector: I do not know it",
	} {
		t.Run(name, func(t *testing.T) {
			spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, SentAt: authorAt(0)})
			if countSpans(text, spans).words[spanHarness] != 0 {
				t.Fatalf("example claimed as report: %#v", spans)
			}
		})
	}
	for _, sender := range []string{"automation:codex-exec", "agent:session"} {
		text := agentationFeedback("Move this left")
		spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "c", Text: text, Sender: sender, SentAt: authorAt(0)})
		if len(spans) != 1 || spans[0].Category != spanAutomated {
			t.Fatalf("automated feedback = %#v", spans)
		}
	}
}

func TestAuthorshipFeedbackLedger(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	text := embeddedFeedback("Pharos", "Move this left")
	record := WorkspaceRecord{SourceID: "feedback", SourceKind: "codex", Account: "local", Title: "Feedback", Conversations: []ConversationRecord{{NativeID: "feedback", Provider: "codex", Messages: []MessageRecord{{NativeID: "m", Role: "user", Kind: "message", Text: text, CreatedAt: "2026-09-01T12:00:00Z", Selected: true}}}}}
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
	if err := catalog.RebuildAuthorship(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMaps(catalog.DB, "SELECT typed_words,harness_words,pasted_words,spans_json FROM message_authorship")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if integer(rows[0]["typed_words"]) != 3 || integer(rows[0]["harness_words"]) == 0 || integer(rows[0]["pasted_words"]) != 0 {
		t.Fatalf("counts = %v", rows)
	}
	if !strings.Contains(fmt.Sprint(rows[0]["spans_json"]), "Human feedback comment") {
		t.Fatalf("missing explanation: %v", rows)
	}
}

func designFeedback(note string) string {
	return "# Ask Pharos design feedback\n\n## 1. Destination cards\n\n" + note + "\n\n- Element: `#composer`\n- State: Opted in · AI · light · Claude · Low\n- Source: .context/sketch.html\n- Revision: abcdef\n\n```json\n{\"state\":\"preview\"}\n```"
}

func TestAuthorshipDesignFeedbackBoundaries(t *testing.T) {
	text := designFeedback("Make the composer larger") + "\n\nAlso check mobile."
	spans := newClassifier(nil).classify(authorInput{ID: "m", ConversationID: "chat", Text: text, SentAt: authorAt(0)})
	if got := categoryText(text, spans, spanTyped); got != "Make the composer larger|Also check mobile." {
		t.Fatalf("typed=%q", got)
	}
	example := "Please review this example.\n```markdown\n" + designFeedback("This is an example comment") + "\n```"
	if got := countSpans(example, newClassifier(nil).classify(authorInput{ID: "example", Text: example, SentAt: authorAt(0)})).words[spanHarness]; got != 0 {
		t.Fatalf("fenced example recognized as real feedback: %d", got)
	}
}
