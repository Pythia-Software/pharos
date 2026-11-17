package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	_ "embed"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// A message's typed words (the authorship category "typed": the person's own
// words) were either typed on a keyboard or dictated through speech-to-text
// (Conductor's microphone, Wispr Flow, ChatGPT or macOS dictation). Sources
// rarely record which. The estimate here reads the text: dictation tools write
// correctly spelled, capitalised, punctuated prose, so a typed mark (something
// they never produce) is strong evidence of the keyboard, and its absence is
// evidence of dictation that grows with the length of the text. See
// docs/human-authorship.md#dictated-or-typed.

// dictationVersion names the rules, word list and constants below. Rows of
// another version are recomputed by refreshDictation, which touches only
// message_dictation.
const dictationVersion = "dictation-v1"

const (
	// dictationMinWords: shorter typed text carries too little evidence.
	dictationMinWords = 8
	// dictationPrior is the share of messages assumed dictated before reading
	// one. It is deliberately conservative: a typed-only history then has
	// about 2.5% of its typed words estimated dictated.
	dictationPrior = 0.2
	// dictationCleanDictated is how often dictated text has no typed mark.
	dictationCleanDictated = 0.95
	// dictationThreshold: at or above, a message counts as dictated.
	dictationThreshold = 0.5
)

// dictationCleanTyped is how often typed text has no typed mark, by its
// length in typed words, measured on 5,351 messages written before any
// dictation tool was in use.
var dictationCleanTyped = []struct {
	minWords int
	share    float64
}{{100, 0.0816}, {50, 0.2473}, {25, 0.3594}, {dictationMinWords, 0.4623}}

// Input methods, as stored in message_dictation.input_method.
const (
	inputDictation = "dictation"
	inputKeyboard  = "keyboard"
	inputUncertain = "uncertain"
	inputUnknown   = "unknown"
)

// dictationEstimate is one message's row of message_dictation.
type dictationEstimate struct {
	typedWords  int
	probability *float64
	method      string
	marks       []string
	evidence    string
}

// estimateDictation reads a message's typed spans. voice reports that the
// source recorded the message as a voice transcript (a ChatGPT voice
// conversation, a Claude voice note), which decides it.
func estimateDictation(text string, spans []authorSpan, typedWords int, voice bool, spelling *spellingCounts) dictationEstimate {
	estimate := dictationEstimate{typedWords: typedWords, evidence: "text"}
	if voice {
		one := 1.0
		estimate.probability, estimate.method, estimate.evidence = &one, inputDictation, "source"
		return estimate
	}
	if typedWords < dictationMinWords {
		estimate.method = inputUnknown
		return estimate
	}
	estimate.marks = typedMarks(dictationTypedText(text, spans), spelling)
	clean := 0.0
	for _, bin := range dictationCleanTyped {
		if typedWords >= bin.minWords {
			clean = bin.share
			break
		}
	}
	var p float64
	if len(estimate.marks) == 0 {
		p = dictationPrior * dictationCleanDictated / (dictationPrior*dictationCleanDictated + (1-dictationPrior)*clean)
	} else {
		p = dictationPrior * (1 - dictationCleanDictated) / (dictationPrior*(1-dictationCleanDictated) + (1-dictationPrior)*(1-clean))
	}
	p = math.Round(p*1000) / 1000
	estimate.probability = &p
	switch {
	case p >= dictationThreshold:
		estimate.method = inputDictation
	case len(estimate.marks) > 0:
		estimate.method = inputKeyboard
	default:
		estimate.method = inputUncertain
	}
	return estimate
}

// dictationTypedText is the message's typed spans, each other span replaced
// by a space.
func dictationTypedText(text string, spans []authorSpan) string {
	var b strings.Builder
	gap := false
	for _, span := range spans {
		if span.Start < 0 || span.End > len(text) || span.Start > span.End {
			continue
		}
		if span.Category == spanTyped {
			b.WriteString(text[span.Start:span.End])
			gap = false
		} else if !gap {
			b.WriteString(" ")
			gap = true
		}
	}
	return strings.TrimSpace(b.String())
}

var (
	dictationCode         = regexp.MustCompile("`|[A-Za-z]+_[A-Za-z_]+|[a-z]+[A-Z][a-zA-Z]+|(?:^|\\s)--?[a-z][\\w-]+|\\b\\w+\\.(?:go|ts|tsx|js|py|md|json|sql|sh|swift|css|html)\\b|https?://|\\(\\)|\\b\\w+\\(\\)")
	dictationSlash        = regexp.MustCompile(`[A-Za-z]/[A-Za-z]`)
	dictationAbbreviation = regexp.MustCompile(`(?i)\b(?:e\.g\.|i\.e\.|etc\b|w/o?(?:\s|$)|pls\b|plz\b|idk\b|imo\b|tbh\b|btw\b|fwiw\b|lmk\b|thx\b|vs\.?\s)`)
	dictationInterjection = regexp.MustCompile(`(?i)\b(?:hmm+|hm+|ugh+|lol|haha+)\b`)
	dictationCaps         = regexp.MustCompile(`\b[A-Z]{3,}\b`)
	dictationApostrophe   = regexp.MustCompile(`(?i)\b(?:dont|doesnt|isnt|didnt|youre|theyre|thats|wouldnt|shouldnt|couldnt|wasnt|arent|havent|ive|im)\b`)
	dictationSymbol       = regexp.MustCompile(`->|=>|&|\?\?|!!|#\w|\w#`)
	dictationInlineList   = regexp.MustCompile(`(?:^|[\s.:;,])(?:\d{1,2}|[A-Za-z]\d{1,2}|[a-z])\s?[-)]\s`)
	// A token is a run of letters, digits and the characters that join a word
	// to a path, file name or contraction; only all-letter tokens are spelled.
	dictationToken = regexp.MustCompile(`[\p{L}\p{N}_'/.\-]+`)
)

// typedMarks lists what in the text a dictation tool would not have written.
func typedMarks(text string, spelling *spellingCounts) []string {
	marks := []string{}
	add := func(name string, found bool) {
		if found {
			marks = append(marks, name)
		}
	}
	add("typo", len(typos(text, spelling)) > 0)
	add("lowercase_start", lowercaseSentenceStart(text))
	add("lowercase_i", lowercaseI(text))
	add("code", dictationCode.MatchString(text))
	add("slash", dictationSlash.MatchString(text))
	add("abbreviation", dictationAbbreviation.MatchString(text))
	add("interjection", dictationInterjection.MatchString(text))
	caps := false
	for _, word := range dictationCaps.FindAllString(text, -1) {
		if spellingWords()[strings.ToLower(word)] {
			caps = true
			break
		}
	}
	add("caps", caps)
	add("no_apostrophe", dictationApostrophe.MatchString(text))
	add("symbol", dictationSymbol.MatchString(text))
	add("inline_list", dictationInlineList.MatchString(text))
	tail := strings.TrimRight(text, " \t\r\n\"'”’)*_")
	if last, _ := utf8.DecodeLastRuneInString(tail); tail != "" && !strings.ContainsRune(".!?…", last) {
		marks = append(marks, "no_end_punctuation")
	}
	return marks
}

// lowercaseSentenceStart reports a sentence, or line, whose first letter is
// lowercase. Dictation capitalises every sentence.
func lowercaseSentenceStart(text string) bool {
	for _, sentence := range strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '\n' }) {
		sentence = strings.TrimLeft(sentence, " \t\"'“‘*-•([")
		first, _ := utf8.DecodeRuneInString(sentence)
		if unicode.IsLetter(first) && unicode.IsLower(first) {
			return true
		}
	}
	return false
}

func lowercaseI(text string) bool {
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '\'' || r == '’')
	}) {
		switch strings.ReplaceAll(token, "’", "'") {
		case "i", "i'm", "i've", "i'll", "i'd":
			return true
		}
	}
	return false
}

// typos returns the words of the text that are keyboard slips: not English
// words, yet one edit (a dropped, added, swapped or wrong letter) from one,
// or two common words run together. A word the library itself uses often is
// a name or term, not a slip: it is a typo only when its nearest English
// word appears in at least 20 times as many messages. Only lowercase words,
// and capitalised words opening a sentence, are checked; other capitalised
// words are usually names.
func typos(text string, spelling *spellingCounts) []string {
	words := spellingWords()
	found := map[string]bool{}
	for _, loc := range dictationToken.FindAllStringIndex(text, -1) {
		token := text[loc[0]:loc[1]]
		if !isLowerASCII(token) {
			if len(token) < 3 || token[0] < 'A' || token[0] > 'Z' || !isLowerASCII(token[1:]) || !opensSentence(text, loc[0]) {
				continue
			}
			token = strings.ToLower(token)
		}
		if len(token) < 3 || knownWord(words, token) || found[token] {
			continue
		}
		if len(token) >= 5 && runTogether(words, token) && spelling.messages(token) < 50 {
			found[token] = true
			continue
		}
		best := int64(-1)
		for _, neighbour := range oneEditWords(words, token) {
			best = max(best, spelling.messages(neighbour))
		}
		if best >= 0 && spelling.messages(token)*20 <= best {
			found[token] = true
		}
	}
	out := make([]string, 0, len(found))
	for word := range found {
		out = append(out, word)
	}
	slices.Sort(out)
	return out
}

func isLowerASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// opensSentence reports whether the word at index starts the text or follows
// sentence-ending punctuation or a line break.
func opensSentence(text string, index int) bool {
	i := index
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		if r == '\n' {
			return true
		}
		if !unicode.IsSpace(r) {
			return r == '.' || r == '!' || r == '?'
		}
		i -= size
	}
	return true
}

var spellingSuffixes = [][2]string{{"s", ""}, {"es", ""}, {"ies", "y"}, {"ed", ""}, {"ed", "e"}, {"ing", ""}, {"ing", "e"}, {"ly", ""}, {"er", ""}, {"ers", ""}}

func knownWord(words map[string]bool, word string) bool {
	if words[word] {
		return true
	}
	for _, suffix := range spellingSuffixes {
		if stem, ok := strings.CutSuffix(word, suffix[0]); ok && words[stem+suffix[1]] {
			return true
		}
	}
	return false
}

var runTogetherParts = map[string]bool{}

func init() {
	for _, word := range strings.Fields("the a an and or but to of in on at for with from by is it be as this that we you i my our your can not no so if") {
		runTogetherParts[word] = true
	}
}

// runTogether reports two common short words typed without the space
// between them ("onthe", "ifyou").
func runTogether(words map[string]bool, word string) bool {
	for i := 2; i <= len(word)-2; i++ {
		if runTogetherParts[word[:i]] && runTogetherParts[word[i:]] && words[word[:i]] && words[word[i:]] {
			return true
		}
	}
	return false
}

// oneEditWords lists the English words (three letters or more) one deletion,
// transposition, substitution or insertion away from word.
func oneEditWords(words map[string]bool, word string) []string {
	seen := map[string]bool{}
	out := []string{}
	try := func(candidate string) {
		if len(candidate) >= 3 && candidate != word && !seen[candidate] && words[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	for i := 0; i <= len(word); i++ {
		left, right := word[:i], word[i:]
		if right != "" {
			try(left + right[1:])
		}
		if len(right) > 1 {
			try(left + right[1:2] + right[:1] + right[2:])
		}
		for c := byte('a'); c <= 'z'; c++ {
			if right != "" {
				try(left + string(c) + right[1:])
			}
			try(left + string(c) + right)
		}
	}
	slices.Sort(out)
	return out
}

//go:embed spelling/words.txt.gz
var spellingWordsGzip []byte

// spellingWords is the bundled English word list (spelling/README.md).
var spellingWords = sync.OnceValue(func() map[string]bool {
	reader, err := gzip.NewReader(bytes.NewReader(spellingWordsGzip))
	if err != nil {
		panic("spelling word list: " + err.Error())
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		panic("spelling word list: " + err.Error())
	}
	words := make(map[string]bool, 110000)
	for _, word := range strings.Split(string(data), "\n") {
		if word != "" {
			words[word] = true
		}
	}
	return words
})

// spellingCounts reads how many messages in the library use a word, from
// the word index (messages_vocab), remembering each answer for one pass. A
// library without the index counts every word as unused, which treats every
// near-miss of an English word as a typo.
type spellingCounts struct {
	ctx    context.Context
	q      rowQueryer
	cache  map[string]int64
	failed bool
}

func newSpellingCounts(ctx context.Context, q rowQueryer) *spellingCounts {
	return &spellingCounts{ctx: ctx, q: q, cache: map[string]int64{}}
}

func (s *spellingCounts) messages(word string) int64 {
	if s == nil {
		return 0
	}
	if count, ok := s.cache[word]; ok {
		return count
	}
	var count int64
	if s.q != nil && !s.failed {
		err := s.q.QueryRowContext(s.ctx, `SELECT doc FROM messages_vocab WHERE term=?`, word).Scan(&count)
		if err != nil && err != sql.ErrNoRows {
			s.failed = true
		}
	}
	s.cache[word] = count
	return count
}

// dictationRow is the stored form of an estimate.
func (e dictationEstimate) row(messageID, conversationID string) []any {
	var probability any
	if e.probability != nil {
		probability = *e.probability
	}
	var marks any
	if len(e.marks) > 0 {
		marks = jsonText(e.marks)
	}
	return []any{messageID, conversationID, e.typedWords, probability, e.method, marks, e.evidence, dictationVersion}
}

var dictationColumns = []string{"message_id", "conversation_id", "typed_words", "p_dictated", "input_method", "marks", "evidence", "version"}

// voiceTranscriptHarnesses are the sources that mark a message recorded by
// voice: ChatGPT voice conversations (audio_transcription parts) and Claude
// voice notes (voice_note blocks).
var voiceTranscriptHarnesses = map[string]bool{"chatgpt-export": true, "claude-ai-export": true}

func voiceTranscript(ctx context.Context, q rowQueryer, harness, messageID string) (bool, error) {
	if !voiceTranscriptHarnesses[harness] {
		return false, nil
	}
	var voice int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(instr(raw_text,'"audio_transcription"')>0 OR instr(raw_text,'"voice_note"')>0,0) FROM messages WHERE id=?`, messageID).Scan(&voice)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return voice != 0, err
}
