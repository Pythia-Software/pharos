package archive

import (
	"strings"
	"testing"
)

// The expected hashes come from the measured implementation,
// experiment.py prompt(rows, 'questions', 'short') and
// json.dumps(schema(), ensure_ascii=False, indent=2) + "\n", run once on these
// invented rows. The rows use the historical shape: role is the sender,
// channel_source the channel, previous_text the full saved context.
func TestLabelingPromptReproducesTheMeasuredPrompt(t *testing.T) {
	type historical struct{ id, role, channel, previous, text string }
	rows := []historical{
		{"inv_1", "user", "unspecified", "", "Please fix the \"Save\" button \u2014 it vanishes.\nThanks!"},
		{"inv_2", "assistant", "analysis", "Previous user request:\nDo it", "Path C:\\Users\\x and tab\there; caf\u00e9 \u65e5\u672c\u8a9e \U0001f680\U0001f389"},
		{"inv_3", "agent", "unspecified", strings.Repeat("ab\U0001f642", 250), "ctrl \x07 bell \x1f unit \u2028 sep \x7f del \r\n end"},
		{"inv_4", "tool", "unspecified", strings.Repeat("x", 600), "exactly six hundred"},
		{"inv_5", "user", "unspecified", strings.Repeat("y", 600) + "\U0001f600", "\u27e6excluded: quoted text, 12 chars\u27e7 ok"},
	}
	promptRows := []labelingPromptRow{}
	for _, row := range rows {
		context, excerpt := labelingHistoricalContext(row.previous)
		promptRows = append(promptRows, labelingPromptRow{ID: row.id, Sender: row.role, Channel: row.channel, Context: context, Excerpt: excerpt, Text: row.text})
	}
	for _, check := range []struct {
		rows int
		want string
	}{
		{5, "6d636c76772c2345e1fac727ea885d40154a05a101bd7ec3caaad5f1359d8dde"},
		{1, "ea83cf34aa57b6e04a2c58a6fdbd9def44fd5f9896bedefa036d127f946510c6"},
	} {
		prompt, err := buildLabelingPrompt(labelingPromptReproduction, promptRows[:check.rows])
		if err != nil {
			t.Fatal(err)
		}
		if got := hashBytes([]byte(prompt)); got != check.want {
			t.Fatalf("prompt of %d rows hashes to %s, want %s", check.rows, got, check.want)
		}
		pieces, err := buildLabelingPrompt(labelingPromptPieces, promptRows[:check.rows])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Replace(pieces, labelingPiecesSentence, "", 1) != prompt || !strings.Contains(pieces, "⟦excluded: ") {
			t.Fatal("the pieces prompt differs from the reproduction by more than its marker sentence")
		}
	}
	schema, err := labelingSchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := hashBytes([]byte(schema)); got != "a99323fed4797f8002b80e51038b3e44779dcf312da10b6b5f28d686210ee10c" {
		t.Fatalf("schema hashes to %s", got)
	}
	if _, err := buildLabelingPrompt("prompt-unknown", promptRows); err == nil {
		t.Fatal("an unknown prompt version was accepted")
	}
}
