package archive

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Library labeling prepares conversation text for an offline teacher model
// (see docs/library-labeling.md). These are the versioned contracts a prepared
// dataset records; changing what one of them produces needs a new version.
const (
	labelingUnitsVersion    = "labeling-units-v1"
	humanPiecesVersion      = "human-pieces-v1"
	labelingContextVersion  = "context-v3"
	labelingChunkVersion    = "chunking-v1"
	labelingSplitsVersion   = "splits-v1"
	labelingSentenceVersion = "sentences-v1"
	// Markers stand in a unit's model-visible text for material that is not
	// the target: what it was and how many characters (code points) it held.
	// Context (quotes, pastes, attachments) shows a bounded excerpt so the
	// teacher sees what the person reacts to; harness and secrets never do.
	// Pieces are never joined without a marker between them.
	labelingMarker        = "⟦excluded: %s, %d chars⟧"
	labelingContextMarker = "⟦%s, context only, %d chars: \"%s\"⟧"
	// labelingExcerptChars bounds one marker's excerpt (head…tail), and
	// labelingExcerptBudget all excerpts of a unit.
	labelingExcerptChars  = 300
	labelingExcerptBudget = 1200
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
	// SourceStart and SourceEnd are the byte range a re-sent piece matched
	// in its source message (SourceEnd 0 when there is none).
	SourceStart, SourceEnd int
}

// humanUnit is human-pieces-v1 applied to one message: its pieces in order,
// the text a teacher model sees with the visible byte ranges of its markers,
// or the reason it is excluded.
type humanUnit struct {
	Pieces  []labelingPiece
	Visible string
	Markers []labelingChunk
	// Copied reports a marker for copied material (quoted, pasted, or reused
	// without verified human origin), which can leave punctuation behind.
	Copied  bool
	Exclude string
	// Secrets counts redacted secret ranges; Unrecognized, leading blocks of
	// harness tags authorship does not know.
	Secrets, Unrecognized int
}

// leadingHarnessReasons are the reasons of harness blocks that open a message,
// which are dropped without a marker: they are never the person's and say
// nothing about the message.
var leadingHarnessReasons = func() map[string]bool {
	reasons := map[string]bool{reasonHarness.text(): true, unrecognizedHarnessReason: true}
	for _, reason := range harnessReasons {
		reasons[reason.text()] = true
	}
	return reasons
}()

// unrecognizedHarnessTags are app-supplied blocks that open user messages but
// that authorship-v8 does not recognize, so it counts them as typed.
// Labeling drops them like harness.
var unrecognizedHarnessTags = []string{"in-app-browser-context"}

const unrecognizedHarnessReason = "unrecognized_harness_tag"

// labelingHarnessBlocks returns the harness blocks that open text, those
// authorship knows and the unrecognized tags, the latter flagged.
func labelingHarnessBlocks(text string) (blocks []harnessBlock, unrecognized []bool) {
	offset := 0
	for {
		known := leadingHarnessBlocks(text[offset:])
		for _, block := range known {
			blocks = append(blocks, harnessBlock{Start: offset + block.Start, End: offset + block.End, Source: block.Source})
			unrecognized = append(unrecognized, false)
		}
		if len(known) > 0 {
			offset += known[len(known)-1].End
		}
		start := offset + len(text[offset:]) - len(strings.TrimLeft(text[offset:], " \t\r\n"))
		found := false
		for _, tag := range unrecognizedHarnessTags {
			rest := text[start:]
			if !strings.HasPrefix(rest, "<"+tag) || len(rest) == len(tag)+1 || !strings.ContainsRune("> \n", rune(rest[len(tag)+1])) {
				continue
			}
			end := strings.Index(rest, "</"+tag+">")
			if end < 0 {
				return blocks, unrecognized
			}
			end = start + end + len("</"+tag+">")
			blocks = append(blocks, harnessBlock{Start: start, End: end, Source: tag})
			unrecognized = append(unrecognized, true)
			offset, found = end, true
		}
		if !found {
			return blocks, unrecognized
		}
	}
}

// carvePieces gives the bytes [start, end) to replacement, splitting the
// target and context pieces it overlaps. Dropped pieces keep their bytes.
func carvePieces(pieces []labelingPiece, start, end int, replacement labelingPiece) []labelingPiece {
	result := make([]labelingPiece, 0, len(pieces)+2)
	for _, piece := range pieces {
		if piece.Role == pieceDropped || piece.End <= start || piece.Start >= end {
			result = append(result, piece)
			continue
		}
		piece.Transform = "" // a carved piece shows its bytes literally
		if piece.Start < start {
			before := piece
			before.End = start
			result = append(result, before)
		}
		inner := replacement
		inner.Start, inner.End = max(piece.Start, start), min(piece.End, end)
		result = append(result, inner)
		if piece.End > end {
			after := piece
			after.Start = end
			result = append(result, after)
		}
	}
	return result
}

// secretRanges finds what the context redaction patterns would redact.
func secretRanges(text string) [][2]int {
	ranges := [][2]int{}
	for _, secret := range labelingSecrets {
		for _, match := range secret.pattern.FindAllStringSubmatchIndex(text, -1) {
			start := match[0]
			if len(match) > 2 && match[3] > 0 {
				start = match[3] // keep the prefix group ("Bearer ", "token=")
			}
			ranges = append(ranges, [2]int{start, match[1]})
		}
	}
	return ranges
}

// cleanPieces applies the labeling-side rules to a message's pieces: leading
// blocks of unrecognized harness tags are dropped silently, and each secret
// becomes a dropped piece, so no secret is ever visible or quotable.
func cleanPieces(text string, pieces []labelingPiece) ([]labelingPiece, int, int) {
	blocks, unrecognized := labelingHarnessBlocks(text)
	tags := 0
	for index, block := range blocks {
		if unrecognized[index] {
			tags++
			pieces = carvePieces(pieces, block.Start, block.End, labelingPiece{Role: pieceDropped, Status: spanHarness, Category: spanHarness, Reason: unrecognizedHarnessReason})
		}
	}
	secrets := secretRanges(text)
	for _, secret := range secrets {
		pieces = carvePieces(pieces, secret[0], secret[1], labelingPiece{Role: pieceDropped, Status: "redacted_secret", Category: "secret", Reason: "redacted_secret"})
	}
	return pieces, tags, len(secrets)
}

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
		if strings.HasPrefix(piece.Reason, "Matches") {
			return "quoted agent text"
		}
		return "quoted text"
	case spanPasted:
		return "likely pasted text"
	case spanAttachment:
		return "attachment reference"
	case spanResent:
		return "re-sent text of unverified origin"
	case "secret":
		return "redacted secret"
	case spanHarness:
		switch piece.Reason {
		case reasonQuestionForm.text():
			return "question form"
		case reasonPageFeedback.text():
			return "generated page feedback"
		case unrecognizedHarnessReason:
			return "unrecognized harness block"
		}
		return "harness text"
	}
	return piece.Category
}

// markerExcerpt shortens text to at most limit code points, keeping its head and
// tail, and removes marker brackets and secrets from it.
func markerExcerpt(text string, limit int) string {
	text = strings.TrimSpace(text)
	for _, secret := range labelingSecrets {
		text = secret.pattern.ReplaceAllString(text, secret.replacement)
	}
	text = strings.NewReplacer("⟦", "[", "⟧", "]").Replace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	head := (limit - 1) / 2
	return firstRunes(text, head) + "…" + lastRunes(text, limit-1-head)
}

// humanPieces applies human-pieces-v1 to a user message the classifier did
// not attribute to automation. A unit whose targets hold no letter, number,
// or symbol is kept when that is the whole message (a "?" or "+1" sent on
// purpose) and excluded as residual punctuation when material around it was
// excluded.
func humanPieces(text string, spans []authorSpan, reuse labelingReuse) humanUnit {
	pieces, tags, secrets := cleanPieces(text, resolveReuse(text, mergeFeedbackJSON(text, spans), reuse))
	unit := renderPieces(text, pieces)
	unit.Secrets, unit.Unrecognized = secrets, tags
	if unit.Exclude == "" && !meaningfulText(targetText(text, unit.Pieces)) && unit.Copied {
		unit.Exclude = "residual_punctuation"
	}
	return unit
}

// labelingReuse resolves a re-sent piece against its source message: it
// returns the piece split into what was target in the source (kept as
// reused_own_message) and the rest (context, reused_unverified). Nil, or a
// source it cannot verify, leaves the whole piece unverified.
type labelingReuse func(text string, piece labelingPiece) []labelingPiece

// resolveReuse applies reuse to every re-sent piece. Reuse inherits the
// source's attribution: re-sending a pasted log or old agent output does not
// make it the person's words.
func resolveReuse(text string, pieces []labelingPiece, reuse labelingReuse) []labelingPiece {
	result := make([]labelingPiece, 0, len(pieces))
	for _, piece := range pieces {
		if piece.Status != "reused_own_message" {
			result = append(result, piece)
			continue
		}
		var parts []labelingPiece
		if reuse != nil {
			parts = reuse(text, piece)
		}
		if parts == nil {
			piece.Role, piece.Status = pieceContext, "reused_unverified"
			parts = []labelingPiece{piece}
		}
		result = append(result, parts...)
	}
	return result
}

// copiedStatuses are the context statuses whose markers mean copied
// material, after which leftover punctuation is residue.
var copiedStatuses = map[string]bool{"quoted": true, "likely_pasted": true, "reused_unverified": true}

// renderPieces builds the visible text: target pieces in order, exactly as
// stored (or decoded, for a transformed piece), and one marker for each run of
// other material with the same label, except leading harness blocks, which
// are dropped silently. Whitespace between targets is kept; edges are trimmed.
func renderPieces(text string, pieces []labelingPiece) humanUnit {
	var visible strings.Builder
	markers := []labelingChunk{}
	budget, targets := labelingExcerptBudget, 0
	copied := false
	run := []int{}
	flush := func(leading, final bool) {
		defer func() { run = run[:0] }()
		groups := [][]int{}
		for _, index := range run {
			piece := pieces[index]
			// A stored envelope (a delegation's JSON) is never shown or marked.
			if strings.TrimSpace(text[piece.Start:piece.End]) == "" || piece.Category == "envelope" {
				continue
			}
			if leading && piece.Category == spanHarness && leadingHarnessReasons[piece.Reason] {
				continue
			}
			leading = false
			if last := len(groups) - 1; last >= 0 && markerLabel(pieces[groups[last][0]]) == markerLabel(piece) {
				groups[last] = append(groups[last], index)
			} else {
				groups = append(groups, []int{index})
			}
		}
		if len(groups) == 0 {
			// Only whitespace (or silent harness): keep whitespace between targets.
			if targets > 0 && !final {
				for _, index := range run {
					if pieces[index].Role != pieceDropped {
						visible.WriteString(text[pieces[index].Start:pieces[index].End])
					}
				}
			}
			return
		}
		for _, group := range groups {
			first := pieces[group[0]]
			chars := 0
			var shown strings.Builder
			for _, index := range group {
				chars += utf8.RuneCountInString(text[pieces[index].Start:pieces[index].End])
				shown.WriteString(text[pieces[index].Start:pieces[index].End])
			}
			start := visible.Len()
			copied = copied || copiedStatuses[first.Status]
			limit := min(labelingExcerptChars, budget)
			if first.Role == pieceContext && limit >= 40 {
				value := markerExcerpt(shown.String(), limit)
				budget -= utf8.RuneCountInString(value)
				visible.WriteString(fmt.Sprintf(labelingContextMarker, markerLabel(first), chars, value))
			} else {
				visible.WriteString(fmt.Sprintf(labelingMarker, markerLabel(first), chars))
			}
			markers = append(markers, labelingChunk{start, visible.Len()})
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
	flush(false, true)
	unit := humanUnit{Pieces: pieces, Visible: visible.String(), Markers: markers, Copied: copied}
	trimLabelingEdges(text, &unit)
	return unit
}

// trimLabelingEdges removes whitespace at the start of the first target and
// the end of the last when they open or close the visible text, adjusting
// byte ranges, so visible text never starts or ends with it.
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
		for index := range unit.Markers {
			unit.Markers[index].Start -= trim
			unit.Markers[index].End -= trim
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
// between characters. A cut never falls inside a UTF-8 sequence, before a
// character that joins the previous one (combining marks, joiners, variation
// selectors, emoji modifiers, the second of a flag's regional indicators), or
// inside a protected range (a marker). The chunks concatenate to text exactly.
func chunkLabelingText(text string, limit int, protected []labelingChunk) []labelingChunk {
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
		}
		for _, r := range protected {
			if r.Start < start+cut && start+cut < r.End {
				cut = r.Start - start
				if cut <= 0 {
					cut = r.End - start
				}
			}
		}
		// Judged against the whole text: the next character may lie past the window.
		for cut > 0 && joinsPrevious(text, start+cut) {
			_, size := utf8.DecodeLastRuneInString(text[:start+cut])
			cut -= size
		}
		if cut == 0 {
			cut = len(window)
		}
		chunks = append(chunks, labelingChunk{start, start + cut})
		start += cut
	}
	return chunks
}

var sentenceEnd = regexp.MustCompile(`[.!?…。！？]["')\]]*\s+|\n`)

// joinsPrevious reports whether the character at text[at:] attaches to the
// one before it: a combining mark, joiner, variation selector, emoji modifier
// or tag, anything after a zero-width joiner, or a regional indicator that
// completes a flag.
func joinsPrevious(text string, at int) bool {
	if at <= 0 || at >= len(text) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(text[at:])
	previous, _ := utf8.DecodeLastRuneInString(text[:at])
	if regionalIndicator(next) && regionalIndicator(previous) {
		count := 0
		for at > 0 {
			r, size := utf8.DecodeLastRuneInString(text[:at])
			if !regionalIndicator(r) {
				break
			}
			count++
			at -= size
		}
		return count%2 == 1
	}
	return unicode.Is(unicode.M, next) || next == 0x200D || previous == 0x200D || next >= 0xFE00 && next <= 0xFE0F ||
		next >= 0xE0100 && next <= 0xE01EF || next >= 0x1F3FB && next <= 0x1F3FF || next >= 0xE0020 && next <= 0xE007F
}

func regionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// labelingSentences splits the bytes [start, end) of text into sentences
// (sentences-v1): after sentence-ending punctuation followed by space, and at
// line breaks, without surrounding whitespace.
func labelingSentences(text string, start, end int) [][2]int {
	sentences := [][2]int{}
	add := func(from, to int) {
		value := text[from:to]
		trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
		from += len(value) - len(trimmed)
		to = from + len(strings.TrimRightFunc(trimmed, unicode.IsSpace))
		if to > from {
			sentences = append(sentences, [2]int{from, to})
		}
	}
	from := start
	for _, match := range sentenceEnd.FindAllStringIndex(text[start:end], -1) {
		add(from, start+match[1])
		from = start + match[1]
	}
	add(from, end)
	return sentences
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

// labelingContext builds context-v3 (v2, with each part redacted before it is
// cut, so no cut can expose a token): the measured construction, except that
// the previous request is the previous request unit, a person's or an
// agent's (the historical extractor used human typed text only): its last
// 1,400 and the previous agent reply's last 800 characters
// under fixed headings, stripped, redacted and capped at its first 2,200
// characters; a prompt shows its last 600 (excerpt reports that it cut).
// Characters are code points, as in the Python that defined it.
func labelingContext(priorUser, priorAssistant string) (full, prompt string, excerpt bool) {
	// Each part is redacted whole before its tail is taken: a cut could
	// leave a token without the prefix the patterns need ("Bearer ", "sk-").
	redact := func(text string) string {
		for _, secret := range labelingSecrets {
			text = secret.pattern.ReplaceAllString(text, secret.replacement)
		}
		return text
	}
	previous := ""
	if priorUser != "" {
		previous = "Previous user request:\n" + lastRunes(redact(priorUser), 1400)
	}
	if priorAssistant != "" {
		previous += "\nPrevious assistant message:\n" + lastRunes(redact(priorAssistant), 800)
	}
	previous = redact(strings.TrimFunc(previous, pythonSpace))
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
