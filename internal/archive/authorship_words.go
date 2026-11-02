package archive

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var authorshipListNumber = regexp.MustCompile(`^[0-9]+[.)]$`)
var authorshipJSONSpace = regexp.MustCompile(`\\(?:[nrt]|u[0-9a-fA-F]{4})`)

// authorshipWordCounts is the shared definition used by the ledger, summaries
// and reader. A word is a whitespace field containing a letter or digit;
// contractions and compounds stay together. A field crossing classifications
// counts once, in the span holding most of its letters/digits (first on ties).
// Generated JSON whitespace in forms is a separator in the logical text,
// while every offset continues to address the original bytes.
func authorshipWordCounts(text string, spans []authorSpan) []int {
	counts := make([]int, len(spans))
	if len(spans) == 0 {
		return counts
	}
	logical := text
	var masked []byte
	for _, span := range spans {
		if span.Start < 0 || span.End > len(text) || span.End < span.Start {
			continue
		}
		if span.Category != spanHarness || (span.Reason != reasonPageFeedback.text() && span.Reason != reasonQuestionForm.text()) {
			continue
		}
		// Compact JSON has no whitespace between an answer and its generated
		// field names. The enclosing quotes delimit words as well as fields.
		if span.Reason == reasonQuestionForm.text() && span.Start < span.End {
			for _, at := range []int{span.Start, span.End - 1} {
				if text[at] == '"' {
					if masked == nil {
						masked = []byte(text)
					}
					masked[at] = ' '
				}
			}
		}
		for _, loc := range authorshipJSONSpace.FindAllStringIndex(text[span.Start:span.End], -1) {
			a, b := span.Start+loc[0], span.Start+loc[1]
			var decoded string
			if json.Unmarshal([]byte(`"`+text[a:b]+`"`), &decoded) != nil || strings.IndexFunc(decoded, unicode.IsSpace) < 0 {
				continue
			}
			if masked == nil {
				masked = []byte(text)
			}
			for i := a; i < b; i++ {
				masked[i] = ' '
			}
		}
	}
	if masked != nil {
		logical = string(masked)
	}
	fields := fieldSpans(logical)
	cursor := 0
	for fi, field := range fields {
		value := logical[field[0]:field[1]]
		// A numbered list marker needs following text on its line. A numeric
		// answer on its own, including "1.", remains a word.
		if authorshipListNumber.MatchString(value) && fi+1 < len(fields) &&
			!strings.Contains(logical[field[1]:fields[fi+1][0]], "\n") &&
			strings.TrimSpace(logical[strings.LastIndexByte(logical[:field[0]], '\n')+1:field[0]]) == "" {
			continue
		}
		for cursor < len(spans) && spans[cursor].End <= field[0] {
			cursor++
		}
		best, weight := -1, 0
		for i := cursor; i < len(spans) && spans[i].Start < field[1]; i++ {
			a, b := max(field[0], spans[i].Start), min(field[1], spans[i].End)
			if a < 0 || b > len(text) || a >= b || !utf8.ValidString(logical[a:b]) {
				continue
			}
			n := 0
			for _, r := range logical[a:b] {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					n++
				}
			}
			if n > weight {
				best, weight = i, n
			}
		}
		if best >= 0 {
			counts[best]++
		}
	}
	return counts
}
