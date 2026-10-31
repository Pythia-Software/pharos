package archive

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// Agent event candidates and their text (framing contract
// jayhawk-window-v1): which sentences of an agent message the event verifier
// reads, for which kinds, and the window of sentences it reads them in. The
// model's directory carries the contract (FRAMING.md), its Python reference
// and an invented-string fixture, testdata/agent-event-parity.json, which
// these functions must reproduce exactly. Tokens and the budget come with the
// labeler's tokenizer (docs/local-labeler.md).

// The cue lexicon (jayhawk-lexicon-2), the product kinds only. It is the
// model's lexicon.json, unchanged.
//
//go:embed agent_event_lexicon.json
var agentEventLexiconJSON []byte

type agentEventCue struct {
	Text    string
	pattern *regexp.Regexp
	// Every match contains one of these; nil when nothing is certain.
	literals []string
}

type agentEventKindCues struct {
	Kind string
	Cues []agentEventCue
}

var agentEventLexicon = sync.OnceValues(func() (string, []agentEventKindCues) {
	var lexicon struct {
		Version  string `json:"version"`
		Families []struct {
			Key  string   `json:"key"`
			Cues []string `json:"cues"`
		} `json:"families"`
	}
	if err := json.Unmarshal(agentEventLexiconJSON, &lexicon); err != nil {
		panic(fmt.Sprintf("agent event lexicon: %v", err))
	}
	kinds := make([]agentEventKindCues, 0, len(lexicon.Families))
	for _, family := range lexicon.Families {
		kind := agentEventKindCues{Kind: family.Key}
		for _, cue := range family.Cues {
			pattern := agentEventGoPattern(agentEventCompileCue(cue))
			parsed, err := syntax.Parse(pattern, syntax.Perl)
			if err != nil {
				panic(fmt.Sprintf("agent event cue %q: %v", cue, err))
			}
			kind.Cues = append(kind.Cues, agentEventCue{cue, regexp.MustCompile(pattern), agentEventLiterals(parsed)})
		}
		kinds = append(kinds, kind)
	}
	return lexicon.Version, kinds
})

// agentEventCompileCue is the reference's compile_cue: word boundaries at a
// cue's alphanumeric ends, and '*' after a letter for any word characters.
// The result is a Python pattern; agentEventGoPattern adapts it.
func agentEventCompileCue(cue string) string {
	anchored := strings.HasPrefix(cue, "^")
	body := strings.TrimPrefix(cue, "^")
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '*' && i > 0 && body[i-1] >= 'a' && body[i-1] <= 'z' && i+1 < len(body) && strings.IndexByte("|) ", body[i+1]) >= 0 {
			b.WriteString(`\w*`)
			continue
		}
		b.WriteByte(body[i])
	}
	body = b.String()
	alnum := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
	if strings.HasSuffix(body, "*") && !strings.HasSuffix(body, `\*`) {
		body = body[:len(body)-1] + `\w*`
	} else if body != "" && (alnum(body[len(body)-1]) || body[len(body)-1] == '\'') {
		body += `\b`
	}
	if !anchored && body != "" && alnum(body[0]) {
		body = `\b` + body
	}
	if anchored {
		return "^" + body
	}
	return body
}

// agentEventGoPattern rewrites \d for agentEventMatchForm's digit stand-in.
// Cues are lower-case ASCII, so a stand-in never matches a cue's literal.
func agentEventGoPattern(pattern string) string {
	var b strings.Builder
	class := false
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '\\' && i+1 < len(pattern):
			i++
			switch {
			case pattern[i] == 'd' && class:
				b.WriteString("0-9D")
			case pattern[i] == 'd':
				b.WriteString("[0-9D]")
			default:
				b.WriteByte('\\')
				b.WriteByte(pattern[i])
			}
		case c == '[':
			class = true
			b.WriteByte(c)
		case c == ']':
			class = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// agentEventLiterals is a set of strings one of which every match of re
// contains, or nil when there is none to rely on. A cue's pattern runs only
// on a sentence that has one: Go's regexp engine is slow on these patterns'
// bounded repeats, and most sentences have none of a cue's words.
func agentEventLiterals(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase == 0 {
			return []string{string(re.Rune)}
		}
	case syntax.OpCapture, syntax.OpPlus:
		return agentEventLiterals(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return agentEventLiterals(re.Sub[0])
		}
	case syntax.OpConcat:
		// The part whose shortest literal is longest rules out the most.
		var best []string
		shortest := func(set []string) int {
			return len(slices.MinFunc(set, func(a, b string) int { return len(a) - len(b) }))
		}
		for _, sub := range re.Sub {
			if set := agentEventLiterals(sub); set != nil && (best == nil || shortest(set) > shortest(best)) {
				best = set
			}
		}
		return best
	case syntax.OpAlternate:
		var all []string
		for _, sub := range re.Sub {
			set := agentEventLiterals(sub)
			if set == nil {
				return nil
			}
			all = append(all, set...)
		}
		return all
	}
	return nil
}

// agentEventNormalize is the reference's normalize: curly quotes and dashes
// to ASCII, Python's lower case, no leading whitespace, list or markdown
// marks, and single spaces (Python's whitespace throughout).
func agentEventNormalize(sentence string) string {
	var b strings.Builder
	for _, r := range sentence {
		switch r {
		case '’', '‘':
			b.WriteByte('\'')
		case '“', '”':
			b.WriteByte('"')
		case '—', '–':
			b.WriteString(" - ")
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.FieldsFunc(trimAgentEventLead(pythonLower(b.String())), pythonSpace), " ")
}

// trimAgentEventLead removes what ^(?:\s|[-*+>#•·]|\d{1,3}[.)]|\*\*|__|`)+
// matches in Python, where \d is any decimal digit.
func trimAgentEventLead(s string) string {
	for s != "" {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case pythonSpace(r) || strings.ContainsRune("-*+>#•·`", r):
			s = s[size:]
		case unicode.IsDigit(r):
			at, digits := 0, 0
			for digits < 3 && at < len(s) {
				d, n := utf8.DecodeRuneInString(s[at:])
				if !unicode.IsDigit(d) {
					break
				}
				at, digits = at+n, digits+1
			}
			if at >= len(s) || s[at] != '.' && s[at] != ')' {
				return s
			}
			s = s[at+1:]
		case strings.HasPrefix(s, "__"):
			s = s[2:]
		default:
			return s
		}
	}
	return s
}

// pythonLower is Python's str.lower: Go's simple mapping plus Python's full
// mapping of U+0130 and its final sigma.
func pythonLower(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= utf8.RuneSelf || r >= 'A' && r <= 'Z' }) {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		switch r {
		case 'İ':
			b.WriteString("i\u0307")
		case 'Σ':
			if finalSigma(runes, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// finalSigma is Unicode's Final_Sigma condition, as CPython tests it: a
// cased letter before the sigma and none after, skipping case-ignorables.
func finalSigma(runes []rune, at int) bool {
	before := at - 1
	for before >= 0 && caseIgnorable(runes[before]) {
		before--
	}
	if before < 0 || !cased(runes[before]) {
		return false
	}
	after := at + 1
	for after < len(runes) && caseIgnorable(runes[after]) {
		after++
	}
	return after == len(runes) || !cased(runes[after])
}

func cased(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// caseIgnorable is Unicode's Case_Ignorable: marks, format characters,
// modifiers, and the word-break MidLetter, MidNumLet and Single_Quote
// characters.
func caseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) ||
		strings.ContainsRune("'.:\u00b7\u0387\u055f\u05f4\u2018\u2019\u2024\u2027\ufe13\ufe52\ufe55\uff07\uff0e\uff1a", r)
}

// agentEventMatchForm stands in for non-ASCII word characters so that Go's
// ASCII \b and \w agree with Python's Unicode ones: D for a decimal digit
// (also \d in Python), X for any other letter or number. Lengths in code
// points stay the same.
func agentEventMatchForm(normalized string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < utf8.RuneSelf:
			return r
		case unicode.IsDigit(r):
			return 'D'
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			return 'X'
		}
		return r
	}, normalized)
}

type agentEventKindMatch struct {
	Kind string
	Cues []string
}

// agentEventMatch is every kind with a cue in the sentence, with its cues,
// in lexicon order.
func agentEventMatch(sentence string) []agentEventKindMatch {
	_, kinds := agentEventLexicon()
	text := agentEventMatchForm(agentEventNormalize(sentence))
	var out []agentEventKindMatch
	for _, kind := range kinds {
		match := agentEventKindMatch{Kind: kind.Kind}
		for _, cue := range kind.Cues {
			if (cue.literals == nil || slices.ContainsFunc(cue.literals, func(literal string) bool { return strings.Contains(text, literal) })) &&
				cue.pattern.MatchString(text) {
				match.Cues = append(match.Cues, cue.Text)
			}
		}
		if match.Cues != nil {
			out = append(out, match)
		}
	}
	return out
}

// agentEventInCode reports which sentences are fenced code: one that starts
// with ``` and those after it until a sentence with an odd number of ```
// closes the fence. A sentence that opens a fence after its start is read.
func agentEventInCode(sentences []string) []bool {
	out := make([]bool, len(sentences))
	inside := false
	for i, sentence := range sentences {
		odd := strings.Count(sentence, "```")%2 == 1
		if inside || strings.HasPrefix(strings.TrimLeftFunc(sentence, pythonSpace), "```") {
			inside = inside != odd
			out[i] = true
			continue
		}
		inside = odd
	}
	return out
}

type agentEventCandidate struct {
	Sentence int
	Kind     string
	Cues     []string
}

// agentEventCandidates is every (sentence, kind) the verifier scores, in
// sentence then lexicon order, skipping fenced code.
func agentEventCandidates(sentences []string) []agentEventCandidate {
	var out []agentEventCandidate
	for i, code := range agentEventInCode(sentences) {
		if code {
			continue
		}
		for _, match := range agentEventMatch(sentences[i]) {
			out = append(out, agentEventCandidate{i, match.Kind, match.Cues})
		}
	}
	return out
}

// agentEventSentences cuts an agent reply or thinking block into sentences
// as labeling prepare does for the study's data: the text without its outer
// ASCII whitespace, unrecognized harness blocks and secrets left out, then
// sentences-v1. Ranges are bytes of text.
func agentEventSentences(text string) [][2]int {
	start := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
	end := len(strings.TrimRight(text, " \t\r\n"))
	if start >= end {
		return nil
	}
	pieces, _, _ := cleanPieces(text, []labelingPiece{{Role: pieceTarget, Status: "agent_text", Start: start, End: end}}, false)
	rendered := renderPieces(text, pieces)
	if rendered.Exclude != "" {
		return nil
	}
	var out [][2]int
	for _, piece := range rendered.Pieces {
		if piece.Role != pieceTarget || piece.VisibleStart < 0 {
			continue
		}
		if piece.Transform != "" {
			out = append(out, [2]int{piece.Start, piece.End})
			continue
		}
		out = append(out, labelingSentences(text, piece.Start, piece.End)...)
	}
	return out
}

// The window around a candidate and how much of each sentence it shows.
const (
	agentEventWindowBefore  = 6
	agentEventWindowAfter   = 2
	agentEventSentenceChars = 420
	agentEventHeadChars     = 300
	agentEventTailChars     = 100
)

var agentEventSourceLabels = map[string]string{"reply": "agent visible reply", "thinking": "agent thinking (private reasoning)"}

// agentEventWindow is [lo, hi) of the sentences shown around the anchor.
func agentEventWindow(sentences, anchor int) (lo, hi int) {
	return max(0, anchor-agentEventWindowBefore), min(sentences, anchor+agentEventWindowAfter+1)
}

// agentEventClip collapses Python whitespace and keeps the head and tail of
// a long sentence, counting code points.
func agentEventClip(text string) string {
	clipped := strings.Join(strings.FieldsFunc(text, pythonSpace), " ")
	if utf8.RuneCountInString(clipped) <= agentEventSentenceChars {
		return clipped
	}
	return firstRunes(clipped, agentEventHeadChars) + " […] " + lastRunes(clipped, agentEventTailChars)
}

// agentEventLines is the verifier's text for a candidate before tokens: the
// header naming the kind under test (with the model's definition) and the
// source, one line per window sentence with the anchor marked, and the
// anchor's index among the lines.
func agentEventLines(definitions map[string]string, kind, source string, sentences []string, anchor int) (header string, lines []string, at int) {
	lo, hi := agentEventWindow(len(sentences), anchor)
	family := "Family under test: " + kind
	if definition, ok := definitions[kind]; ok {
		family += " — " + definition
	}
	header = family + "\nSource: " + agentEventSourceLabels[source] + "\nSentences:\n"
	for k, text := range sentences[lo:hi] {
		if lo+k == anchor {
			lines = append(lines, fmt.Sprintf("[%d] >>> %s <<<\n", k+1, agentEventClip(text)))
		} else {
			lines = append(lines, fmt.Sprintf("[%d] %s\n", k+1, agentEventClip(text)))
		}
	}
	return header, lines, anchor - lo
}

// agentEventFraming is the verifier's framing contract, jayhawk-window-v1.
var agentEventFraming = labelrt.Framing{Contract: "jayhawk-window-v1", MaxTokens: 768}

// agentEventFrame is the verifier's input for a candidate: [CLS], the
// header's tokens, the window lines that fit, [SEP]. Lines are tokenized
// one by one and kept whole within maxSeq, the anchor first, then by
// distance from it, the earlier line first; a line that does not fit is
// left out and a shorter, farther one may still fit. The anchor is always
// kept, cut to the budget when it alone exceeds it. Spans.Text is the
// anchor's tokens (the pool role); Extra[labelrt.SegmentSpans] has each
// window line's tokens, empty for a line left out (the segment role).
func agentEventFrame(tok *labelrt.Tokenizer, definitions map[string]string, kind, source string, sentences []string, anchor, maxSeq int) labelrt.Framed {
	header, lines, at := agentEventLines(definitions, kind, source, sentences, anchor)
	headerIDs := tok.Encode(header)
	lineIDs := make([][]int32, len(lines))
	for k, line := range lines {
		lineIDs[k] = tok.Encode(line)
	}
	budget := maxSeq - 2 - len(headerIDs)
	keep := make([]bool, len(lines))
	used := 0
	order := []int{at}
	for distance := 1; distance < len(lines); distance++ {
		for _, k := range []int{at - distance, at + distance} {
			if k >= 0 && k < len(lines) {
				order = append(order, k)
			}
		}
	}
	for _, k := range order {
		if used+len(lineIDs[k]) > budget {
			if k != at {
				continue
			}
			lineIDs[k] = lineIDs[k][:budget]
		}
		keep[k] = true
		used += len(lineIDs[k])
	}
	ids := append(append(make([]int32, 0, 2+len(headerIDs)+used), labelrt.ClsID), headerIDs...)
	segments := make([]labelrt.Span, len(lines))
	for k, kept := range keep {
		if kept {
			segments[k] = labelrt.Span{Start: len(ids), End: len(ids) + len(lineIDs[k])}
			ids = append(ids, lineIDs[k]...)
		}
	}
	ids = append(ids, labelrt.SepID)
	return labelrt.Framed{IDs: ids, Spans: labelrt.FrameSpans{Text: segments[at]}, Extra: map[string][]labelrt.Span{labelrt.SegmentSpans: segments}}
}
