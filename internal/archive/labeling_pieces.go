package archive

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Library labeling prepares conversation text for an offline teacher model
// (see docs/library-labeling.md). These are the versioned contracts a prepared
// dataset records; changing what one of them produces needs a new version.
const (
	labelingUnitsVersion   = "labeling-units-v1"
	humanPiecesVersion     = "human-pieces-v1"
	labelingContextVersion = "context-v1"
	labelingChunkVersion   = "chunking-v1"
	labelingSplitsVersion  = "splits-v1"
	// labelingMarker stands in a human unit's model-visible text for material
	// that is not the person's: what it was and how many characters (code
	// points) it held. Pieces are never joined without one.
	labelingMarker = "⟦excluded: %s, %d chars⟧"
	// labelingChunkChars is the longest item, in code points. Longer target
	// text is split, never truncated.
	labelingChunkChars = 6000
	// feedbackJSONTransform decodes a JSON-encoded page-feedback comment; the
	// piece's byte range still names the encoded bytes.
	feedbackJSONTransform = "json-string-v1"
)

// Piece roles: a target may receive labels about its author; context is
// shown to interpret it but supplies no evidence; dropped material is left
// out of the model-visible text, with a marker unless it is a leading
// harness block.
const (
	pieceTarget  = "target"
	pieceContext = "context"
	pieceDropped = "dropped"
)

// labelingPiece is one classified byte range of a message. Visible ranges are
// byte offsets in the unit's model-visible text, -1 when not shown.
type labelingPiece struct {
	Role, Status, Category, Reason    string
	Start, End                        int
	VisibleStart, VisibleEnd          int
	Transform                         string
	SourceConversation, SourceMessage string
}

// humanUnit is human-pieces-v1 applied to one user message: its pieces in
// order, the text a teacher model sees, or the reason it is excluded.
type humanUnit struct {
	Pieces  []labelingPiece
	Visible string
	Exclude string
}

// leadingHarnessReasons are the reasons of harness blocks that open a message,
// which are dropped without a marker: they are never the person's and say
// nothing about the message.
var leadingHarnessReasons = func() map[string]bool {
	reasons := map[string]bool{reasonHarness.text(): true}
	for _, reason := range harnessReasons {
		reasons[reason.text()] = true
	}
	return reasons
}()

// pieceRole maps an authorship span to its human-pieces-v1 role and status.
func pieceRole(span authorSpan) (role, status string) {
	switch span.Category {
	case spanTyped:
		if span.Reason == reasonFeedbackComment.text() {
			return pieceTarget, "feedback_comment"
		}
		return pieceTarget, "estimated_typed"
	case spanResent:
		return pieceTarget, "reused_own_message"
	case spanTemplate:
		return pieceTarget, "reused_template"
	case spanQuoted:
		return pieceContext, "quoted"
	case spanPasted:
		return pieceContext, "likely_pasted"
	case spanAttachment:
		return pieceContext, "attachment"
	}
	return pieceDropped, span.Category
}

// markerLabel names a piece's material in a marker.
func markerLabel(piece labelingPiece) string {
	switch piece.Category {
	case spanQuoted:
		return "quoted text"
	case spanPasted:
		return "likely pasted text"
	case spanAttachment:
		return "attachment reference"
	case spanHarness:
		switch piece.Reason {
		case reasonQuestionForm.text():
			return "question form"
		case reasonPageFeedback.text():
			return "generated page feedback"
		}
		return "harness text"
	}
	return piece.Category
}

// humanPieces applies human-pieces-v1 to a user message the classifier did
// not attribute to automation. Target pieces keep their exact byte ranges and
// appear in order in the visible text; each run of other material between,
// before, or after them becomes one marker. A unit whose targets hold no
// letter, number, or symbol is kept when that is the whole message (a "?" or
// "+1" sent on purpose) and excluded as residual punctuation when material
// around it was excluded.
func humanPieces(text string, spans []authorSpan) humanUnit {
	pieces := mergeFeedbackJSON(text, spans)
	var visible strings.Builder
	run := []int{}
	marked, targets := false, 0
	flush := func(leading, final bool) {
		if len(run) == 0 {
			return
		}
		defer func() { run = run[:0] }()
		chars, silent, labels := 0, leading, []string{}
		for _, index := range run {
			piece := pieces[index]
			if strings.TrimSpace(text[piece.Start:piece.End]) == "" {
				continue
			}
			chars += utf8.RuneCountInString(text[piece.Start:piece.End])
			silent = silent && piece.Category == spanHarness && leadingHarnessReasons[piece.Reason]
			if label := markerLabel(piece); !slices.Contains(labels, label) {
				labels = append(labels, label)
			}
		}
		if chars == 0 {
			// Only whitespace: nothing was excluded, so keep it between targets.
			if !leading && !final {
				for _, index := range run {
					visible.WriteString(text[pieces[index].Start:pieces[index].End])
				}
			}
			return
		}
		if !silent {
			marked = true
			visible.WriteString(fmt.Sprintf(labelingMarker, strings.Join(labels, " + "), chars))
		}
	}
	for index := range pieces {
		piece := &pieces[index]
		piece.VisibleStart, piece.VisibleEnd = -1, -1
		if piece.Role != pieceTarget || strings.TrimSpace(text[piece.Start:piece.End]) == "" {
			run = append(run, index)
			continue
		}
		flush(targets == 0, false)
		targets++
		value := text[piece.Start:piece.End]
		if piece.Transform == feedbackJSONTransform {
			var decoded string
			_ = json.Unmarshal([]byte(`"`+value+`"`), &decoded)
			value = decoded
		}
		piece.VisibleStart = visible.Len()
		visible.WriteString(value)
		piece.VisibleEnd = visible.Len()
	}
	if targets == 0 {
		return humanUnit{Pieces: pieces, Exclude: "no_target_text"}
	}
	// Trailing excluded material is marked too.
	flush(false, true)
	unit := humanUnit{Pieces: pieces, Visible: visible.String()}
	trimLabelingEdges(text, &unit)
	if !meaningfulText(targetText(text, unit.Pieces)) && marked {
		unit.Exclude = "residual_punctuation"
	}
	return unit
}

// trimLabelingEdges removes whitespace at the start of the first target and
// the end of the last, adjusting byte ranges, so visible text never starts or
// ends with it.
func trimLabelingEdges(text string, unit *humanUnit) {
	first, last := -1, -1
	for index, piece := range unit.Pieces {
		if piece.VisibleStart >= 0 {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if piece := &unit.Pieces[first]; piece.VisibleStart == 0 && piece.Transform == "" {
		trim := len(text[piece.Start:piece.End]) - len(strings.TrimLeftFunc(text[piece.Start:piece.End], unicode.IsSpace))
		piece.Start += trim
		unit.Visible = unit.Visible[trim:]
		for index := range unit.Pieces {
			if unit.Pieces[index].VisibleStart >= 0 {
				if index != first {
					unit.Pieces[index].VisibleStart -= trim
				}
				unit.Pieces[index].VisibleEnd -= trim
			}
		}
	}
	if piece := &unit.Pieces[last]; piece.VisibleEnd == len(unit.Visible) && piece.Transform == "" {
		trim := len(text[piece.Start:piece.End]) - len(strings.TrimRightFunc(text[piece.Start:piece.End], unicode.IsSpace))
		piece.End -= trim
		piece.VisibleEnd -= trim
		unit.Visible = unit.Visible[:len(unit.Visible)-trim]
	}
}

// targetText joins the target pieces' original text, only to judge it.
func targetText(text string, pieces []labelingPiece) string {
	var parts strings.Builder
	for _, piece := range pieces {
		if piece.Role == pieceTarget && piece.VisibleStart >= 0 {
			parts.WriteString(text[piece.Start:piece.End])
		}
	}
	return parts.String()
}

// meaningfulText reports whether text holds a letter, number, or symbol
// (emoji are symbols), rather than only punctuation and space.
func meaningfulText(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSymbol(r) {
			return true
		}
	}
	return false
}

// mergeFeedbackJSON converts spans to pieces. The comment of an embedded
// feedback report's `- Feedback: "..."` line is a JSON string the classifier
// splits at encoded whitespace; it becomes one piece over the encoded bytes,
// shown decoded.
func mergeFeedbackJSON(text string, spans []authorSpan) []labelingPiece {
	pieces := make([]labelingPiece, 0, len(spans))
	for index := 0; index < len(spans); index++ {
		span := spans[index]
		role, status := pieceRole(span)
		piece := labelingPiece{Role: role, Status: status, Category: span.Category, Reason: span.Reason, Start: span.Start, End: span.End,
			SourceConversation: span.Source, SourceMessage: span.Message}
		if status == "feedback_comment" {
			if end, last, ok := feedbackJSONString(text, spans, index); ok {
				piece.End, piece.Transform = end, feedbackJSONTransform
				index = last
			}
		}
		pieces = append(pieces, piece)
	}
	return pieces
}

// feedbackJSONString reports whether spans[first], a feedback comment, starts
// the JSON string of a `- Feedback: "..."` line whose spans are all comment
// or encoded whitespace, and returns where the comment ends and its last span.
func feedbackJSONString(text string, spans []authorSpan, first int) (int, int, bool) {
	lineStart := strings.LastIndexByte(text[:spans[first].Start], '\n') + 1
	lineEnd := len(text)
	if at := strings.IndexByte(text[spans[first].Start:], '\n'); at >= 0 {
		lineEnd = spans[first].Start + at
	}
	line := strings.TrimRight(text[lineStart:lineEnd], " \t\r")
	open := strings.Index(line, `- Feedback: "`)
	if open < 0 || strings.TrimSpace(line[:open]) != "" || lineStart+open+len(`- Feedback: "`) != spans[first].Start || !strings.HasSuffix(line, `"`) {
		return 0, 0, false
	}
	closing := lineStart + len(line) - 1
	last := first
	for index := first; index < len(spans) && spans[index].End <= closing; index++ {
		span := spans[index]
		if span.Reason != reasonFeedbackComment.text() && span.Reason != reasonPageFeedback.text() {
			return 0, 0, false
		}
		if span.Reason == reasonFeedbackComment.text() {
			last = index
		}
	}
	end := spans[last].End
	var decoded string
	if json.Unmarshal([]byte(`"`+text[spans[first].Start:end]+`"`), &decoded) != nil {
		return 0, 0, false
	}
	return end, last, true
}

// labelingChunk is one item's byte range of a unit's visible text.
type labelingChunk struct{ Start, End int }

// chunkLabelingText splits text into chunks of at most limit code points
// (chunking-v1): at the last paragraph break in the second half of the
// window, else the last sentence end there, else the last whitespace, else
// between characters, never inside a UTF-8 sequence or before a combining
// mark. The chunks concatenate to text exactly.
func chunkLabelingText(text string, limit int) []labelingChunk {
	chunks := []labelingChunk{}
	for start := 0; start < len(text); {
		end, count := start, 0
		for end < len(text) && count < limit {
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
			count++
		}
		if end == len(text) {
			chunks = append(chunks, labelingChunk{start, end})
			break
		}
		window := text[start:end]
		cut := 0
		if at := strings.LastIndex(window, "\n\n"); at >= len(window)/2 {
			cut = at + 2
		}
		if cut == 0 {
			for _, match := range sentenceEnd.FindAllStringIndex(window, -1) {
				if match[1] >= len(window)/2 {
					cut = match[1]
				}
			}
		}
		if cut == 0 {
			if at := strings.LastIndexFunc(window, unicode.IsSpace); at > 0 {
				_, size := utf8.DecodeRuneInString(window[at:])
				cut = at + size
			}
		}
		if cut == 0 {
			cut = len(window)
			for cut > 0 && joinsPrevious(window, cut) {
				_, size := utf8.DecodeLastRuneInString(window[:cut])
				cut -= size
			}
			if cut == 0 {
				cut = len(window)
			}
		}
		chunks = append(chunks, labelingChunk{start, start + cut})
		start += cut
	}
	return chunks
}

var sentenceEnd = regexp.MustCompile(`[.!?…。！？]["')\]]*\s+|\n`)

// joinsPrevious reports whether the character at text[at:] attaches to the
// one before it: a combining mark, joiner, variation selector, or emoji
// modifier, or anything after a zero-width joiner.
func joinsPrevious(text string, at int) bool {
	next, _ := utf8.DecodeRuneInString(text[at:])
	previous, _ := utf8.DecodeLastRuneInString(text[:at])
	return unicode.Is(unicode.M, next) || next == 0x200D || previous == 0x200D || next >= 0xFE00 && next <= 0xFE0F ||
		next >= 0xE0100 && next <= 0xE01EF || next >= 0x1F3FB && next <= 0x1F3FF || next >= 0xE0020 && next <= 0xE007F
}

// The historical extractor's secret redaction, applied to context.
var labelingSecrets = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~-]{16,}`), "${1}[REDACTED_TOKEN]"},
	{regexp.MustCompile(`(?i)([?&](?:token|api_key|access_token|key)=)[^\s&#"<>]+`), "${1}[REDACTED_TOKEN]"},
}

// labelingContext builds context-v1, the measured construction: the previous
// human unit's last 1,400 and the previous agent reply's last 800 characters
// under fixed headings, stripped, redacted and capped at its first 2,200
// characters; a prompt shows its last 600 (excerpt reports that it cut).
// Characters are code points, as in the Python that defined it.
func labelingContext(priorUser, priorAssistant string) (full, prompt string, excerpt bool) {
	previous := ""
	if priorUser != "" {
		previous = "Previous user request:\n" + lastRunes(priorUser, 1400)
	}
	if priorAssistant != "" {
		previous += "\nPrevious assistant message:\n" + lastRunes(priorAssistant, 800)
	}
	previous = strings.TrimFunc(previous, pythonSpace)
	for _, secret := range labelingSecrets {
		previous = secret.pattern.ReplaceAllString(previous, secret.replacement)
	}
	full = firstRunes(previous, 2200)
	prompt = lastRunes(full, 600)
	return full, prompt, prompt != full
}

// pythonSpace is Python's str.isspace, which also counts \x1c-\x1f.
func pythonSpace(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }

func lastRunes(text string, n int) string {
	at := len(text)
	for count := 0; count < n && at > 0; count++ {
		_, size := utf8.DecodeLastRuneInString(text[:at])
		at -= size
	}
	return text[at:]
}

func firstRunes(text string, n int) string {
	at := 0
	for count := 0; count < n && at < len(text); count++ {
		_, size := utf8.DecodeRuneInString(text[at:])
		at += size
	}
	return text[:at]
}
