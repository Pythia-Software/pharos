package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Human authorship estimates how much of the text sent as user turns a person
// typed (or dictated). A user turn often carries more than that: harness
// instructions, one-click prompts, attachment references, agent output copied
// from another chat, logs, and prompts a script sent. Each turn is split into
// spans, each labelled with a category and the reason it was assigned, so the
// totals are explainable message by message. Definitions are in
// docs/human-authorship.md.

// authorshipVersion names the classification rules; a new version rebuilds.
const authorshipVersion = "authorship-v8"

const (
	// authorshipWindow bounds how far back agent output is looked for. Your
	// own earlier messages are compared however old they are, since saved
	// prompts are often re-sent weeks apart.
	authorshipWindow = 7 * 24 * time.Hour
	// shingleWords is the length of the word runs compared between texts.
	shingleWords = 8
	// quoteMinWords is the shortest matching run treated as copied.
	quoteMinWords = 12
	// A candidate contains enough consecutive shingles to establish a copy
	// on its own. Short excerpts cannot replace a complete candidate.
	copyShingles = quoteMinWords - shingleWords + 1
	// Prompts with the same text in this many sessions on this many days are
	// templates (buttons, saved prompts) rather than text typed each time.
	templateSessions, templateDays, templateMinChars = 5, 3, 10
	// A line of at least lineMinWords words already sent in lineConversations
	// other conversations is output or boilerplate, not something retyped.
	lineMinWords, lineConversations = 4, 2
	// machineBlockGap is how many prose lines may interrupt pasted output
	// (a traceback's source lines, a log's messages) without ending it.
	machineBlockGap = 2
	// typingCharsPerSecond is faster than anyone types or dictates.
	typingCharsPerSecond = 20
	// typingCheckMinChars keeps the speed check off short replies.
	typingCheckMinChars = 600
	// A line of at least identifierLineMin words, identifierLineShare of them
	// identifiers, is output or code; so is a longer line of at least
	// identifierProseMin words with identifierProseShare identifiers.
	identifierLineMin, identifierLineShare   = 4, 0.5
	identifierProseMin, identifierProseShare = 8, 0.35
	// identifierRunChars is the shortest run of consecutive identifiers
	// (paths, URLs, IDs, hashes) treated as pasted within a typed sentence.
	identifierRunChars = 20
	// identifierLongChars is the length past which any token is an ID.
	identifierLongChars = 40
)

// Span categories. Typed is what remains after every other rule. Pasted is
// the one inferred category: its rules are heuristics, so the typed total is
// reported as a range from typed to typed plus pasted.
const (
	spanTyped      = "typed"
	spanHarness    = "harness"
	spanAutomated  = "automated"
	spanTemplate   = "template"
	spanAttachment = "attachment"
	spanQuoted     = "quoted"
	spanResent     = "resent"
	spanPasted     = "pasted"
)

// authorshipCategories orders the categories message_authorship stores a
// <category>_chars and <category>_words column for.
var authorshipCategories = []string{spanTyped, spanHarness, spanAutomated, spanTemplate, spanAttachment, spanQuoted, spanResent, spanPasted}

// authorshipColumns lists each category's count columns, in category order.
func authorshipColumns() []string {
	columns := []string{}
	for _, category := range authorshipCategories {
		columns = append(columns, category+"_chars", category+"_words")
	}
	return columns
}

// authorSpan is one labelled byte range of a user message.
type authorSpan struct {
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Category string `json:"category"`
	Reason   string `json:"reason,omitempty"`
	// Source and Message name the conversation and message holding the text
	// a quoted or resent span matches.
	Source  string `json:"source,omitempty"`
	Message string `json:"message,omitempty"`
}

// Rules name what claimed a span without its particulars (how long ago,
// how many sessions), so spans and messages can be filtered and grouped by
// them. Text no rule claimed is typed and has ruleTyped.
const ruleTyped = "No other rule matched"

// spanReason is a reason the classifier gives a span. A reason that carries
// particulars renders them between prefix and suffix, and its rule leaves
// them out; a reason without a prefix is fixed text and is its own rule.
type spanReason struct{ rule, prefix, suffix, particulars string }

// spanReasons is the catalog of every reason the classifier can give a span.
// The writing_messages schema offers each one's rule as a Rules option, and
// TestSpanRulesMatchTheSchema checks that it does. Declare reasons with
// fixedReason and reasonTemplate, which add them here; renaming one orphans
// the rule already stored for it.
var spanReasons []spanReason

func fixedReason(rule string) spanReason {
	return reasonTemplate(rule, "", "")
}

func reasonTemplate(rule, prefix, suffix string) spanReason {
	reason := spanReason{rule: rule, prefix: prefix, suffix: suffix}
	spanReasons = append(spanReasons, reason)
	return reason
}

// with returns the reason carrying particulars.
func (r spanReason) with(particulars string) spanReason {
	r.particulars = particulars
	return r
}

// text returns the reason as a span records it.
func (r spanReason) text() string {
	if r.prefix == "" {
		return r.rule
	}
	return r.prefix + r.particulars + r.suffix
}

// The catalog, in the order the Rules filter offers it.
var (
	reasonCodeBlock       = fixedReason("Code block")
	reasonMachineOutput   = fixedReason("Log, trace, code, or data")
	reasonTable           = fixedReason("Table")
	reasonAgentFormat     = fixedReason("Formatted like agent output")
	reasonIdentifierLine  = fixedReason("Mostly identifiers, paths, or code")
	reasonIdentifier      = fixedReason("Path, URL, or ID")
	reasonFasterThanTyped = reasonTemplate("Sent faster than typing", "Sent ", " after the previous message, faster than typing")
	reasonPageFeedback    = fixedReason("Generated page feedback")
	reasonFeedbackComment = fixedReason("Human feedback comment")
	reasonQuoted          = fixedReason("Quoted with >")
	reasonAgentOutput     = reasonTemplate("Matches recent agent output", "Matches agent output from ", " earlier")
	reasonAlreadySent     = reasonTemplate("Already sent earlier", "Already sent ", " earlier")
	reasonLineSent        = fixedReason("Line already sent in other conversations")
	reasonRepeatedPrompt  = reasonTemplate("Repeated prompt", "Repeated prompt: ", "")
	reasonSlashCommand    = fixedReason("Slash command")
	reasonAttachment      = fixedReason("Attachment reference")
	// harnessReasons names the harness blocks leadingHarnessBlocks finds by
	// source; any other block is reasonHarness.
	harnessReasons = map[string]spanReason{
		"conductor_instruction":            fixedReason("Conductor system instruction"),
		"system_reminder":                  fixedReason("System reminder"),
		"environments.environment_context": fixedReason("Environment context"),
		"user_instructions":                fixedReason("Project instructions"),
		"plugins.recommendations":          fixedReason("Plugin recommendations"),
	}
	reasonHarness          = fixedReason("Harness context")
	reasonLaunched         = reasonTemplate("Launched by a command", "Launched with ", " by a command")
	reasonClaudePrint      = fixedReason("Sent by a headless claude -p run")
	reasonCodexExec        = fixedReason("Sent by a codex exec run")
	reasonAgyPrint         = fixedReason("Sent by a headless agy -p run")
	reasonAutomation       = reasonTemplate("Sent by automation", "Sent by automation ", "")
	reasonAgentSession     = fixedReason("Sent by another agent session")
	reasonTL1              = fixedReason("Sent by TL1 orchestration")
	reasonSubAgent         = fixedReason("Sub-agent conversation")
	reasonUnrecordedClaude = fixedReason("Sent by a headless claude -p or SDK run Conductor did not start")
)

// spanRule returns the rule that claimed span.
func spanRule(span authorSpan) string {
	if span.Reason == "" {
		return ruleTyped
	}
	for _, reason := range spanReasons {
		if reason.prefix != "" && strings.HasPrefix(span.Reason, reason.prefix) && strings.HasSuffix(span.Reason, reason.suffix) {
			return reason.rule
		}
	}
	return span.Reason
}

// authorshipState tracks the in-process rebuild.
type authorshipState struct {
	mu        sync.Mutex
	running   bool
	lastError string
	// deferred records a follow-up a rebuild skipped while this process
	// indexed; the ingest's end runs it (ingestEnded).
	deferred atomic.Bool
}

var (
	attachmentPattern = regexp.MustCompile(`@⟦[^⟧\n]*⟧\([^)\n]*\)|(?:/[^\s]*)?\.context/attachments/(?:[A-Za-z0-9_-]+/)?[^\n]*?\.(?:md|txt|png|jpe?g|gif|webp|pdf|json|csv|log|html|zip)\b|\(image attachment\)`)
	slashPattern      = regexp.MustCompile(`^/[A-Za-z][\w:-]*`)
	fencePattern      = regexp.MustCompile("(?s)```.*?(?:```|$)")
	timestampPattern  = regexp.MustCompile(`^\[?\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}|^\[?\d{1,2}:\d{2}:\d{2}`)
	fileLinePattern   = regexp.MustCompile(`\S+\.[A-Za-z]{1,5}:\d+`)
	levelPattern      = regexp.MustCompile(`^\[?(?:DEBUG|INFO|WARN|WARNING|ERROR|FATAL|TRACE|debug|info|warn|error)\b[\]:]?`)
	// Program output: "[deploy] …" tags, shell prompts, error headers,
	// key=value pairs, and rule or underline lines.
	tagPrefixPattern   = regexp.MustCompile(`^\[[\w .:/-]{1,24}\]\s`)
	shellPromptPattern = regexp.MustCompile(`^(?:\S+@\S+ \S+ ?[%$#]|PS [A-Za-z]:\\[^>]*>|>>> )`)
	errorHeadPattern   = regexp.MustCompile(`^(?:[\w.]*(?:Error|Exception):|error(?:\[\w+\])?:|fatal:|npm (?:ERR|WARN)!|Caused by:|\+ (?:CategoryInfo|FullyQualifiedErrorId)|At line:\d)`)
	keyValuePattern    = regexp.MustCompile(`\b[\w.-]+=\S+`)
	ruleLinePattern    = regexp.MustCompile(`^[\s^~─━═│├└┌┐┘╰╭>×✓✗✔✘•=*#-]{4,}$`)
	// Source code: declarations, imports, and lines ending the way statements do.
	codeStartPattern = regexp.MustCompile(`^(?://|#!|#include\b|(?:const|let|var) [\w{}\[\], ]+ =|(?:def|func|fn|pub fn|function|class) \w*.*[({:]\s*$|from [\w.]+ import |import .*['"]\s*;?$)`)
	codeEndPattern   = regexp.MustCompile(`(?:[;{}(\[,]|=>|\)\s*\{)\s*$`)
	// indentedPattern is text indented as a block, not a nested list item.
	indentedPattern = regexp.MustCompile(`^(?: {4,}|\t)(?:[^-*•\d\s]|\d+[^.)\d])`)
	// Tokens people type that look like identifiers: short labels (M1, PR-2,
	// p03), quantities (27s, 10k), and abbreviations (e.g, i.e).
	shortLabelPattern      = regexp.MustCompile(`^(?:[A-Za-z]{1,3}-?\d{1,3}|\d+[A-Za-z]{1,3}|(?:[A-Za-z]\.)+[A-Za-z]?)$`)
	identifierShapePattern = regexp.MustCompile(`[_\\=@#$%^&|~<>{}\[\]\d]|[a-z][A-Z]|\.\w|://`)
	identifierTrimChars    = ".,;:!?()[]{}\"'`*<>“”‘’"
)

// harnessTags wrap text a harness prepends to what a person typed, inside the
// same message. Conductor prefixes <system_instruction> blocks to a session's
// first turn and to turns that carry attachments. The message keeps this text,
// since it is part of the prompt; authorship only counts it apart.
var harnessTags = []struct{ tag, source string }{
	{"system_instruction", "conductor_instruction"},
	{"system-reminder", "system_reminder"},
	{"environment_context", "environments.environment_context"},
	{"user_instructions", "user_instructions"},
	{"recommended_plugins", "plugins.recommendations"},
}

// harnessBlock is one harness-tagged block found at the start of a message.
type harnessBlock struct {
	Start, End int
	Source     string
}

// leadingHarnessBlocks finds the harness-tagged blocks that open text. Only
// leading blocks count: the same tags later in a message are more likely text
// a person pasted or wrote about.
func leadingHarnessBlocks(text string) []harnessBlock {
	blocks := []harnessBlock{}
	offset := 0
	for {
		rest := text[offset:]
		start := offset + len(rest) - len(strings.TrimLeft(rest, " \t\r\n"))
		matched := false
		for _, known := range harnessTags {
			open, close := "<"+known.tag+">", "</"+known.tag+">"
			if !strings.HasPrefix(text[start:], open) {
				continue
			}
			end := strings.Index(text[start:], close)
			if end < 0 {
				return blocks
			}
			end += start + len(close)
			blocks = append(blocks, harnessBlock{Start: start, End: end, Source: known.source})
			offset, matched = end, true
			break
		}
		if !matched {
			return blocks
		}
	}
}

// splitHarnessText separates leading harness blocks from the text after them.
func splitHarnessText(text string) ([]harnessBlock, string) {
	blocks := leadingHarnessBlocks(text)
	if len(blocks) == 0 {
		return nil, text
	}
	return blocks, strings.TrimSpace(text[blocks[len(blocks)-1].End:])
}

// authorInput is one user message and what is known about who sent it.
// Unclaimed is set when the Mac's Conductor is read and records no session
// for the message's conversation, and Launch when a command in another
// conversation started it.
type authorInput struct {
	ID, ConversationID, WorkspaceID, Text, Sender, SourceKind, Harness string
	SubAgent, Unclaimed                                                bool
	Launch                                                             *conversationLaunch
	SentAt                                                             time.Time
}

// authorResult is the classification of one message.
type authorResult struct {
	Input authorInput
	Spans []authorSpan
}

// textOrigin names the message a span's text was found in.
type textOrigin struct {
	conversation, message string
}

// shingleRef records where and when a word run was last seen.
type shingleRef struct {
	batch  *shingleBatch
	offset int
}

// shingleIndex holds the word runs of texts seen within the window.
type shingleIndex struct {
	seen   map[uint64]shingleRef
	queue  []*shingleBatch
	byText map[[32]byte]*shingleBatch
}

type shingleBatch struct {
	sentAt  time.Time
	at      int64
	hashes  []uint64
	origin  textOrigin
	reviews map[string]bool
	headers map[string]bool
	digest  [32]byte
	tool    bool
}

func newShingleIndex() *shingleIndex {
	return &shingleIndex{seen: map[uint64]shingleRef{}, byText: map[[32]byte]*shingleBatch{}}
}

func (index *shingleIndex) add(at time.Time, origin textOrigin, hashes []uint64) {
	if len(hashes) == 0 {
		return
	}
	stamp := at.Unix()
	batch := &shingleBatch{sentAt: at, at: stamp, hashes: hashes, origin: origin}
	for offset := 0; offset+copyShingles <= len(hashes); offset++ {
		index.seen[copyCandidate(hashes[offset:])] = shingleRef{batch: batch, offset: offset}
	}
	index.queue = append(index.queue, batch)
}

// evict forgets runs last seen before cutoff. Batches arrive in time order.
func (index *shingleIndex) evict(cutoff time.Time) {
	drop := 0
	for drop < len(index.queue) && index.queue[drop].sentAt.Before(cutoff) {
		batch := index.queue[drop]
		if index.byText[batch.digest] == batch {
			delete(index.byText, batch.digest)
		}
		for offset := 0; offset+copyShingles <= len(batch.hashes); offset++ {
			hash := copyCandidate(batch.hashes[offset:])
			if ref, ok := index.seen[hash]; ok && ref.batch.sentAt.Before(cutoff) {
				delete(index.seen, hash)
			}
		}
		drop++
	}
	if drop > 0 {
		remaining := copy(index.queue, index.queue[drop:])
		clear(index.queue[remaining:])
		index.queue = index.queue[:remaining]
	}
}

// wordToken is one normalized word and its byte range in the source text.
type wordToken struct {
	start, end int
	word       string
}

// wordTokens splits text into lowercase letter-and-digit words.
func wordTokens(text string) []wordToken {
	tokens := []wordToken{}
	start := -1
	for offset, r := range text {
		inWord := unicode.IsLetter(r) || unicode.IsDigit(r)
		if inWord && start < 0 {
			start = offset
		} else if !inWord && start >= 0 {
			tokens = append(tokens, wordToken{start, offset, strings.ToLower(text[start:offset])})
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, wordToken{start, len(text), strings.ToLower(text[start:])})
	}
	return tokens
}

// shingles hashes every run of shingleWords consecutive words.
func shingles(tokens []wordToken) []uint64 {
	if len(tokens) < shingleWords {
		return nil
	}
	hashes := make([]uint64, 0, len(tokens)-shingleWords+1)
	for index := 0; index+shingleWords <= len(tokens); index++ {
		hash := fnv.New64a()
		for _, token := range tokens[index : index+shingleWords] {
			hash.Write([]byte(token.word))
			hash.Write([]byte{0})
		}
		hashes = append(hashes, hash.Sum64())
	}
	return hashes
}

// copiedRun is a run of words matching text an index saw earlier.
type copiedRun struct {
	start, end int
	origin     textOrigin
	at         int64
	batch      *shingleBatch
}

// copyCandidate indexes a complete minimum-length copy, represented by its
// overlapping shingles. The source sequence is verified after lookup.
func copyCandidate(hashes []uint64) uint64 {
	hash := uint64(14695981039346656037)
	for _, part := range hashes[:copyShingles] {
		hash = (hash ^ part) * 1099511628211
	}
	return hash
}

// copiedRuns verifies fixed-size candidates from individual sources. Every
// position takes constant work, even for repetitive text; coalescing verified
// coverage keeps the labeler from rescanning overlapping prefixes.
func copiedRuns(tokens []wordToken, hashes []uint64, index *shingleIndex, cutoff time.Time) []copiedRun {
	runs := []copiedRun{}
	for position := 0; position+copyShingles <= len(hashes); position++ {
		ref, ok := index.seen[copyCandidate(hashes[position:])]
		if !ok || ref.batch.sentAt.Before(cutoff) {
			continue
		}
		if !slices.Equal(hashes[position:position+copyShingles], ref.batch.hashes[ref.offset:ref.offset+copyShingles]) {
			continue
		}
		start, end := tokens[position].start, tokens[position+quoteMinWords-1].end
		if len(runs) > 0 {
			last := &runs[len(runs)-1]
			if last.batch == ref.batch && start <= last.end {
				last.end = end
				continue
			}
			// Earlier candidates already established this prefix. Preserve their
			// source attribution and add only the newly verified suffix.
			start = max(start, last.end)
		}
		runs = append(runs, copiedRun{start: start, end: end, origin: ref.batch.origin, at: ref.batch.at, batch: ref.batch})
	}
	return runs
}

// labeler assigns each byte of a message to at most one category; the first
// rule to claim a byte keeps it.
type labeler struct {
	text    string
	owner   []int16
	details []authorSpan
}

func newLabeler(text string) *labeler {
	owner := make([]int16, len(text))
	for index := range owner {
		owner[index] = -1
	}
	return &labeler{text: text, owner: owner}
}

func (l *labeler) claim(start, end int, category string, reason spanReason, origin textOrigin) {
	start, end = max(start, 0), min(end, len(l.text))
	if start >= end {
		return
	}
	id := int16(len(l.details))
	claimed := false
	for index := start; index < end; index++ {
		if l.owner[index] < 0 {
			l.owner[index] = id
			claimed = true
		}
	}
	if claimed {
		l.details = append(l.details, authorSpan{Category: category, Reason: reason.text(), Source: origin.conversation, Message: origin.message})
	}
}

// unclaimed returns the length of text no rule has claimed.
func (l *labeler) unclaimed() int {
	count := 0
	for _, owner := range l.owner {
		if owner < 0 {
			count++
		}
	}
	return count
}

// spans merges labelled bytes into ordered spans. Unclaimed bytes are typed,
// except whitespace-only gaps, which join the span before them.
func (l *labeler) spans() []authorSpan {
	result := []authorSpan{}
	for index := 0; index < len(l.owner); {
		end := index
		for end < len(l.owner) && l.owner[end] == l.owner[index] {
			end++
		}
		span := authorSpan{Start: index, End: end, Category: spanTyped}
		if owner := l.owner[index]; owner >= 0 {
			detail := l.details[owner]
			span.Category, span.Reason, span.Source, span.Message = detail.Category, detail.Reason, detail.Source, detail.Message
		} else if strings.TrimSpace(l.text[index:end]) == "" && len(result) > 0 {
			result[len(result)-1].End = end
			index = end
			continue
		}
		if len(result) > 0 && result[len(result)-1].Category == span.Category && result[len(result)-1].Reason == span.Reason && result[len(result)-1].Message == span.Message {
			result[len(result)-1].End = end
		} else {
			result = append(result, span)
		}
		index = end
	}
	return result
}

// templateKey normalizes a message for template counting: attachment
// references become a placeholder and whitespace and case are ignored.
func templateKey(text string) string {
	_, text = splitHarnessText(text)
	text = attachmentPattern.ReplaceAllString(text, "@")
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// templateUse counts the sessions and days a template key was sent in.
type templateUse struct {
	sessions, days map[string]bool
}

func (use templateUse) template() bool {
	return len(use.sessions) >= templateSessions && len(use.days) >= templateDays
}

// automatedReason explains why a message came from a program or agent, and
// reports false when a person sent it.
func automatedReason(input authorInput) (spanReason, bool) {
	switch {
	case input.Launch != nil:
		return reasonLaunched.with(headlessLabels[input.Launch.program]), true
	case input.Sender == "automation:claude-sdk-cli":
		return reasonClaudePrint, true
	case input.Sender == "automation:codex-exec":
		return reasonCodexExec, true
	case input.Sender == "automation:agy-print":
		return reasonAgyPrint, true
	case strings.HasPrefix(input.Sender, "automation:"):
		return reasonAutomation.with(strings.TrimPrefix(input.Sender, "automation:")), true
	case strings.HasPrefix(input.Sender, "agent:"):
		return reasonAgentSession, true
	case input.SourceKind == "tl1" || input.SourceKind == "tl1-export":
		return reasonTL1, true
	case input.SubAgent:
		// Forked sub-agents also carry the parent's history, already counted there.
		return reasonSubAgent, true
	case input.Unclaimed && headlessRun("claude", input.Harness) != "":
		// Conductor runs Claude through the SDK too, but records each
		// session it starts; a claude -p started under another Claude
		// session inherits its sdk-ts entrypoint.
		return reasonUnrecordedClaude, true
	}
	return spanReason{}, false
}

// lineUse records the first message a line was sent in and the distinct
// conversations it was sent in, up to one more than lineConversations.
type lineUse struct {
	origin        textOrigin
	conversations []string
}

// classifier holds the corpus state classification needs: templates, the
// agent output of the window before the current message, and every earlier
// message's text and lines.
type classifier struct {
	templates    map[string]*templateUse
	agentText    *shingleIndex
	userText     *shingleIndex
	lines        map[uint64]*lineUse
	lastUserSent map[string]time.Time
	questions    map[string][]authorshipQuestion
}

func newClassifier(inputs []authorInput) *classifier {
	templates := map[string]*templateUse{}
	for _, input := range inputs {
		if _, automated := automatedReason(input); automated {
			continue
		}
		key := templateKey(input.Text)
		if len(key) < templateMinChars {
			continue
		}
		use := templates[key]
		if use == nil {
			use = &templateUse{sessions: map[string]bool{}, days: map[string]bool{}}
			templates[key] = use
		}
		use.sessions[input.ConversationID] = true
		use.days[input.SentAt.Local().Format("2006-01-02")] = true
	}
	return &classifier{templates: templates, agentText: newShingleIndex(), userText: newShingleIndex(), lines: map[uint64]*lineUse{}, lastUserSent: map[string]time.Time{}, questions: map[string][]authorshipQuestion{}}
}

// classify labels one message. Agent output sent before it must already be
// in agentText; classify adds the message's own text to userText.
func (c *classifier) classify(input authorInput) []authorSpan {
	text := input.Text
	l := newLabeler(text)
	previous, hasPrevious := c.lastUserSent[input.ConversationID]
	c.lastUserSent[input.ConversationID] = input.SentAt
	if reason, automated := automatedReason(input); automated {
		origin := textOrigin{}
		if input.Launch != nil {
			origin = textOrigin{input.Launch.parent, input.Launch.message}
		}
		l.claim(0, len(text), spanAutomated, reason, origin)
		return l.spans()
	}
	bodyStart := 0
	for _, block := range leadingHarnessBlocks(text) {
		reason, known := harnessReasons[block.Source]
		if !known {
			reason = reasonHarness
		}
		l.claim(block.Start, block.End, spanHarness, reason, textOrigin{})
		bodyStart = block.End
	}
	bodyStart += len(text[bodyStart:]) - len(strings.TrimLeft(text[bodyStart:], " \t\r\n"))
	body := strings.TrimSpace(text[bodyStart:])
	feedback := pageFeedbackReports(text, bodyStart)
	claimFeedbackMetadata(l, feedback)
	c.claimQuestionForms(l, input, bodyStart)
	if use := c.templates[templateKey(text)]; use != nil && use.template() {
		l.claim(0, len(text), spanTemplate, reasonRepeatedPrompt.with(fmt.Sprintf("sent in %d sessions on %d days", len(use.sessions), len(use.days))), textOrigin{})
	}
	if match := slashPattern.FindStringIndex(body); match != nil && !strings.Contains(body, "\n") {
		l.claim(bodyStart+match[0], bodyStart+match[1], spanTemplate, reasonSlashCommand, textOrigin{})
	}
	for _, match := range attachmentPattern.FindAllStringIndex(text, -1) {
		l.claim(match[0], match[1], spanAttachment, reasonAttachment, textOrigin{})
	}
	comparisonText := maskGeneratedAuthorship(maskFeedback(text, feedback, false), l)
	heuristicText := maskGeneratedAuthorship(maskFeedback(text, feedback, true), l)
	lines := textLines(text)
	// Harness text is left out of word runs, or its closing words and the
	// first words typed after it would match every earlier session's.
	tokens := wordTokens(comparisonText)
	for len(tokens) > 0 && tokens[0].start < bodyStart {
		tokens = tokens[1:]
	}
	hashes := shingles(tokens)
	agentRuns := copiedRuns(tokens, hashes, c.agentText, input.SentAt.Add(-authorshipWindow))
	for _, run := range agentRuns {
		reason := reasonAgentOutput
		if run.batch.tool {
			reason = reasonAuthoredTool
		}
		l.claim(run.start, run.end, spanQuoted, reason.with(ago(input.SentAt, run.at)), run.origin)
	}
	claimCopiedReviewItems(l, comparisonText, agentRuns, input.SentAt)
	for _, run := range copiedRuns(tokens, hashes, c.userText, time.Time{}) {
		l.claim(run.start, run.end, spanResent, reasonAlreadySent.with(ago(input.SentAt, run.at)), run.origin)
	}
	for _, match := range fencePattern.FindAllStringIndex(heuristicText, -1) {
		l.claim(match[0], match[1], spanPasted, reasonCodeBlock, textOrigin{})
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(heuristicText[line[0]:line[1]], " "), ">") {
			l.claim(line[0], line[1], spanQuoted, reasonQuoted, textOrigin{})
		}
	}
	claimExplicitPastes(l, heuristicText, bodyStart)
	// A reused line counts as re-sent on its own only when it is too long to
	// be a phrase typed again by chance; shorter ones only mark pasted output.
	keys, repeated := c.repeatedLines(input.ConversationID, comparisonText, lines, bodyStart)
	for index, line := range lines {
		value := comparisonText[line[0]:line[1]]
		if use := c.lines[keys[index]]; repeated[index] && use != nil && len(wordTokens(value)) >= shingleWords && !machineLine(value) {
			l.claim(line[0], line[1], spanResent, reasonLineSent, use.origin)
		}
	}
	for _, report := range feedback {
		for _, note := range report.notes {
			l.claim(note[0], note[1], spanTyped, reasonFeedbackComment, textOrigin{})
		}
	}
	claimStructuredBlocks(l, heuristicText, lines, repeated)
	if remaining := l.unclaimed(); hasPrevious && remaining >= typingCheckMinChars {
		seconds := input.SentAt.Sub(previous).Seconds()
		if seconds <= 0 || float64(remaining)/seconds > typingCharsPerSecond {
			l.claim(0, len(text), spanPasted, reasonFasterThanTyped.with(roundDuration(input.SentAt.Sub(previous))), textOrigin{})
		}
	}
	// After the speed check, so pasted identifiers still count as text that
	// arrived too fast to type.
	claimIdentifiers(l, heuristicText, lines, bodyStart)
	c.userText.add(input.SentAt, textOrigin{input.ConversationID, input.ID}, hashes)
	c.rememberLines(textOrigin{input.ConversationID, input.ID}, keys)
	return l.spans()
}

// lineKey identifies a line by its words, with numbers, IDs, and hashes
// collapsed so the same log line from another run still matches. Lines shorter
// than lineMinWords get no key.
func lineKey(line string) (uint64, bool) {
	tokens := wordTokens(line)
	if len(tokens) < lineMinWords {
		return 0, false
	}
	hash := fnv.New64a()
	for _, token := range tokens {
		word := token.word
		if strings.ContainsAny(word, "0123456789") {
			word = "0"
		}
		hash.Write([]byte(word))
		hash.Write([]byte{0})
	}
	return hash.Sum64(), true
}

// repeatedLines keys each line after the harness and reports which ones
// appear twice in this message or were sent in lineConversations other
// conversations before it. People rarely retype a line word for word, so
// such lines are program output or reused text.
func (c *classifier) repeatedLines(conversation, text string, lines [][2]int, bodyStart int) ([]uint64, []bool) {
	keys, repeated := make([]uint64, len(lines)), make([]bool, len(lines))
	within := map[uint64]int{}
	for index, line := range lines {
		if line[1] <= bodyStart {
			continue
		}
		if key, ok := lineKey(text[max(line[0], bodyStart):line[1]]); ok {
			keys[index] = key
			within[key]++
		}
	}
	for index, key := range keys {
		if key == 0 {
			continue
		}
		others := 0
		if use := c.lines[key]; use != nil {
			for _, seen := range use.conversations {
				if seen != conversation {
					others++
				}
			}
		}
		repeated[index] = within[key] > 1 || others >= lineConversations
	}
	return keys, repeated
}

// rememberLines records the conversation each keyed line was sent in.
func (c *classifier) rememberLines(origin textOrigin, keys []uint64) {
	for _, key := range keys {
		if key == 0 {
			continue
		}
		use := c.lines[key]
		if use == nil {
			use = &lineUse{origin: origin}
			c.lines[key] = use
		}
		if len(use.conversations) <= lineConversations && !slices.Contains(use.conversations, origin.conversation) {
			use.conversations = append(use.conversations, origin.conversation)
		}
	}
}

// textLines returns the byte range of every line, without its newline.
func textLines(text string) [][2]int {
	lines := [][2]int{}
	start := 0
	for index := 0; index <= len(text); index++ {
		if index == len(text) || text[index] == '\n' {
			lines = append(lines, [2]int{start, index})
			start = index + 1
		}
	}
	return lines
}

// machineLine reports whether a line looks like program output, code, or
// data rather than prose: log lines, stack frames, diffs, JSON, shell
// sessions, error reports, source code, and file:line lists.
func machineLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	switch {
	case timestampPattern.MatchString(trimmed), levelPattern.MatchString(trimmed):
		return true
	case strings.HasPrefix(trimmed, "at ") && (strings.Contains(trimmed, "(") || strings.Contains(trimmed, ":")):
		return true
	case strings.HasPrefix(trimmed, "File \""), strings.HasPrefix(trimmed, "Traceback"), strings.HasPrefix(trimmed, "goroutine "),
		strings.HasPrefix(trimmed, "panic:"), strings.HasPrefix(trimmed, "@@"), strings.HasPrefix(trimmed, "+++ "), strings.HasPrefix(trimmed, "--- "),
		strings.HasPrefix(trimmed, "$ "), strings.HasPrefix(trimmed, "❯ "):
		return true
	case (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "\"")) && strings.Contains(trimmed, "\":"):
		return true
	case strings.HasPrefix(trimmed, "}") || strings.HasPrefix(trimmed, "]") || trimmed == "[" || trimmed == "{":
		return true
	case fileLinePattern.MatchString(trimmed) && len(strings.Fields(trimmed)) <= 6:
		return true
	case tagPrefixPattern.MatchString(trimmed), shellPromptPattern.MatchString(trimmed), errorHeadPattern.MatchString(trimmed),
		ruleLinePattern.MatchString(trimmed), codeStartPattern.MatchString(trimmed), len(keyValuePattern.FindAllStringIndex(trimmed, 3)) >= 2:
		return true
	case strings.ContainsAny(trimmed, "│├└╰╭━═"), strings.ContainsRune("▸·✖✔✘✓✗●○◆▶►→⚠❌✅×", first):
		return true
	case codeEndPattern.MatchString(trimmed) && codePunctuation(trimmed) >= 2:
		return true
	}
	letters := 0
	for _, r := range trimmed {
		if unicode.IsLetter(r) || r == ' ' {
			letters++
		}
	}
	return utf8.RuneCountInString(trimmed) >= 20 && float64(letters)/float64(utf8.RuneCountInString(trimmed)) < 0.6
}

// codePunctuation counts the brackets, semicolons, and equals signs in text.
func codePunctuation(text string) int {
	count := 0
	for _, r := range text {
		if strings.ContainsRune("(){}[];=", r) {
			count++
		}
	}
	return count
}

func tableLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) > 1 && strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") || strings.Count(trimmed, "\t") >= 2
}

func headingLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "#") && strings.HasPrefix(strings.TrimLeft(trimmed, "#"), " ")
}

func boldBulletLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	trimmed = strings.TrimLeft(trimmed, "-*0123456789.)")
	return strings.HasPrefix(trimmed, " **")
}

// indentedLine reports whether a line is indented as a block (a pasted
// terminal message or code) rather than as a nested list item. Pastes often
// indent with non-breaking spaces.
func indentedLine(line string) bool {
	return indentedPattern.MatchString(strings.ReplaceAll(line, "\u00a0", " "))
}

// claimMachineBlocks marks pasted output: at least three machine lines, with
// at most machineBlockGap prose lines between any two of them and no more
// prose than machine lines overall. Tracebacks and logs mix source lines and
// messages into their frames, so strictly consecutive runs miss them.
func claimMachineBlocks(l *labeler, text string, lines [][2]int, machine []bool) {
	for index := 0; index < len(lines); {
		if !machine[index] {
			index++
			continue
		}
		last, machines, prose, gap := index, 0, 0, 0
		for next := index; next < len(lines) && gap <= machineBlockGap; next++ {
			switch {
			case machine[next]:
				machines, prose, gap, last = machines+1, prose+gap, 0, next
			case strings.TrimSpace(text[lines[next][0]:lines[next][1]]) != "":
				gap++
			}
		}
		if machines >= 3 && machines >= prose {
			l.claim(lines[index][0], lines[last][1], spanPasted, reasonMachineOutput, textOrigin{})
		}
		index = last + 1
	}
}

// claimStructuredBlocks marks pasted output and tables, and the region of a
// message formatted the way agents format replies. Repeated lines count as
// machine lines.
func claimStructuredBlocks(l *labeler, text string, lines [][2]int, repeated []bool) {
	claimRuns := func(match func(string) bool, minimum int, reason spanReason) {
		for index := 0; index < len(lines); {
			end := index
			for end < len(lines) && match(text[lines[end][0]:lines[end][1]]) {
				end++
			}
			if end-index >= minimum {
				l.claim(lines[index][0], lines[end-1][1], spanPasted, reason, textOrigin{})
			}
			index = max(end, index+1)
		}
	}
	machine := make([]bool, len(lines))
	for index, line := range lines {
		value := text[line[0]:line[1]]
		machine[index] = repeated[index] || machineLine(value) || indentedLine(value)
	}
	claimMachineBlocks(l, text, lines, machine)
	claimRuns(tableLine, 2, reasonTable)
	first, last, headings, bullets := -1, -1, 0, 0
	for index, line := range lines {
		value := text[line[0]:line[1]]
		heading, bullet := headingLine(value), boldBulletLine(value)
		if heading {
			headings++
		}
		if bullet {
			bullets++
		}
		if heading || bullet {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first >= 0 && (headings >= 2 || bullets >= 3 || headings >= 1 && bullets >= 2) {
		end := pastedDocumentTail(text, lines, first, last)
		for end+1 < len(lines) && strings.TrimSpace(text[lines[end+1][0]:lines[end+1][1]]) != "" {
			end++
		}
		l.claim(lines[first][0], lines[end][1], spanPasted, reasonAgentFormat, textOrigin{})
	}
}

// tokenKind is how a whitespace-separated token reads: as a word, as an
// identifier (a path, URL, ID, hash, or code), or as neither (numbers,
// punctuation, short labels), which counts toward no share.
type tokenKind int8

const (
	tokenNeutral tokenKind = iota
	tokenWord
	tokenIdentifier
)

// classifyToken judges a token by its shape. People type words, including
// hyphenated and slashed compounds; digits mixed into letters, underscores,
// camelCase, inner dots, and symbols mark text copied from a program.
func classifyToken(token string) (tokenKind, int) {
	trimmed := strings.Trim(token, identifierTrimChars)
	length := utf8.RuneCountInString(trimmed)
	if strings.IndexFunc(trimmed, unicode.IsLetter) < 0 || shortLabelPattern.MatchString(trimmed) {
		return tokenNeutral, length
	}
	if length >= identifierLongChars || identifierShapePattern.MatchString(trimmed) {
		return tokenIdentifier, length
	}
	return tokenWord, length
}

// claimIdentifiers marks text made of identifiers rather than words: lines
// that are mostly identifiers (log lines, selectors, code), and within other
// lines, runs of consecutive identifiers long enough to have been pasted (a
// path or ID in a typed sentence). The words around a run stay typed.
func claimIdentifiers(l *labeler, text string, lines [][2]int, bodyStart int) {
	type field struct {
		start, end, length int
		kind               tokenKind
	}
	for _, line := range lines {
		if line[1] <= bodyStart {
			continue
		}
		start := max(line[0], bodyStart)
		fields, words, identifiers := []field{}, 0, 0
		for _, span := range fieldSpans(text[start:line[1]]) {
			kind, length := classifyToken(text[start+span[0] : start+span[1]])
			fields = append(fields, field{start + span[0], start + span[1], length, kind})
			switch kind {
			case tokenWord:
				words++
			case tokenIdentifier:
				identifiers++
			}
		}
		counted := words + identifiers
		share := float64(identifiers) / float64(max(counted, 1))
		if counted >= identifierLineMin && share >= identifierLineShare || counted >= identifierProseMin && share >= identifierProseShare {
			l.claim(start, line[1], spanPasted, reasonIdentifierLine, textOrigin{})
			continue
		}
		for index := 0; index < len(fields); {
			if fields[index].kind != tokenIdentifier {
				index++
				continue
			}
			end, last, chars := index, index, 0
			for ; end < len(fields) && fields[end].kind != tokenWord; end++ {
				if fields[end].kind == tokenIdentifier {
					last, chars = end, chars+fields[end].length
				}
			}
			if chars >= identifierRunChars {
				l.claim(fields[index].start, fields[last].end, spanPasted, reasonIdentifier, textOrigin{})
			}
			index = end
		}
	}
}

// fieldSpans returns the byte range of every whitespace-separated field.
func fieldSpans(text string) [][2]int {
	spans := [][2]int{}
	start := -1
	for offset, r := range text {
		if unicode.IsSpace(r) {
			if start >= 0 {
				spans = append(spans, [2]int{start, offset})
				start = -1
			}
		} else if start < 0 {
			start = offset
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(text)})
	}
	return spans
}

func ago(now time.Time, stamp int64) string {
	return roundDuration(now.Sub(time.Unix(stamp, 0)))
}

func roundDuration(value time.Duration) string {
	switch {
	case value < time.Minute:
		return fmt.Sprintf("%ds", max(int(value.Seconds()), 0))
	case value < time.Hour:
		return fmt.Sprintf("%dm", int(value.Minutes()))
	case value < 48*time.Hour:
		return fmt.Sprintf("%.1fh", value.Hours())
	default:
		return fmt.Sprintf("%dd", int(value.Hours()/24))
	}
}

// authorshipCounts sums characters and words per category.
type authorshipCounts struct {
	chars, words map[string]int
}

func countSpans(text string, spans []authorSpan) authorshipCounts {
	counts := authorshipCounts{chars: map[string]int{}, words: map[string]int{}}
	words := authorshipWordCounts(text, spans)
	for index, span := range spans {
		value := text[span.Start:span.End]
		counts.chars[span.Category] += utf8.RuneCountInString(strings.TrimSpace(value))
		counts.words[span.Category] += words[index]
	}
	return counts
}

// spanSummary is what a message's spans hold: the category with the most
// words, and JSON arrays of the categories and rules holding any text, most
// words first.
type spanSummary struct{ main, categories, rules string }

func summarizeSpans(text string, spans []authorSpan) spanSummary {
	wordCounts := authorshipWordCounts(text, spans)
	type weight struct {
		name         string
		words, chars int
	}
	tally := func(key func(authorSpan) string) []weight {
		weights := []weight{}
		index := map[string]int{}
		for position, span := range spans {
			value := text[span.Start:span.End]
			chars := utf8.RuneCountInString(strings.TrimSpace(value))
			if chars == 0 {
				continue
			}
			name := key(span)
			if _, ok := index[name]; !ok {
				index[name] = len(weights)
				weights = append(weights, weight{name: name})
			}
			entry := &weights[index[name]]
			entry.words += wordCounts[position]
			entry.chars += chars
		}
		sort.SliceStable(weights, func(i, j int) bool {
			if weights[i].words != weights[j].words {
				return weights[i].words > weights[j].words
			}
			return weights[i].chars > weights[j].chars
		})
		return weights
	}
	names := func(weights []weight) string {
		list := make([]string, len(weights))
		for index, entry := range weights {
			list[index] = entry.name
		}
		encoded, _ := json.Marshal(list)
		return string(encoded)
	}
	categories := tally(func(span authorSpan) string { return span.Category })
	summary := spanSummary{categories: names(categories), rules: names(tally(spanRule))}
	if len(categories) > 0 {
		summary.main = categories[0].name
	}
	return summary
}

// authorshipGenerations returns the generation message_authorship should be
// built for and the one it was built for. Record writes invalidate message
// classification even when they add no tools or model requests; the tool
// ledger generation also captures changes to identity links and launch data.
func (c *Catalog) authorshipGenerations() (want, built string, err error) {
	ctx := context.Background()
	if want, err = c.metaValue(ctx, "tool_ledger_generation"); err != nil {
		return "", "", err
	}
	inputs, err := c.metaValue(ctx, "authorship_inputs_generation")
	if err != nil {
		return "", "", err
	}
	built, err = c.metaValue(ctx, "authorship_generation")
	return want + ":" + inputs + "/" + authorshipVersion, built, err
}

// bumpAuthorshipInputs runs in the record writer's transaction, so readers
// see the invalidation with the messages and their classification metadata.
func bumpAuthorshipInputs(tx execer) error {
	_, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('authorship_inputs_generation','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`)
	return err
}

// refreshAuthorship starts a background rebuild when messages changed since
// the last one. An explicit index calls it, whether or not it wrote anything:
// scheduled incremental indexing deliberately leaves classification pending.
func (c *Catalog) refreshAuthorship() {
	c.refreshAuthorshipInContext(nil)
}

// A coordinated manual index cancels its own classification when stopped;
// page/source refreshes continue to use the service's background lifetime.
// It reports whether it started a rebuild.
func (c *Catalog) refreshAuthorshipInContext(parent context.Context) bool {
	if parent != nil && parent.Err() != nil {
		return false
	}
	if want, built, err := c.authorshipGenerations(); err == nil && want != built {
		return c.startAuthorshipInContext(want, parent)
	}
	return false
}

// ensureAuthorship is refreshAuthorship for a page. It builds a ledger that
// is missing or classified by older rules, unless that build just failed,
// and otherwise shows the last index's until the next. It reports whether
// messages changed since the ledger was built.
func (c *Catalog) ensureAuthorship() (stale bool) {
	want, built, err := c.authorshipGenerations()
	if err != nil || want == built {
		return false
	}
	c.authorship.mu.Lock()
	failed := c.authorship.lastError != ""
	c.authorship.mu.Unlock()
	if !strings.HasSuffix(built, "/"+authorshipVersion) && !failed {
		c.startAuthorship(want)
	}
	return true
}

// startAuthorship rebuilds for generation in the background, unless a
// rebuild is running already. A rebuild that succeeds checks again for
// messages an index wrote while it ran, except one a page started while this
// process indexes: the index writes on every record, so that rebuild would
// start again as soon as it ended, rewriting message_authorship and holding a
// read snapshot round after round. The skipped follow-up is remembered
// (authorshipFollowUp) and runs once when the last ingest ends, unless it was
// stopped or the service is stopping, in which case it waits for the next
// ingest that ends normally or an explicit refresh. A manual index's
// analysis chain has rebuilt by then (refreshAuthorship), so that run finds
// the ledger current. Pages show the last ledger meanwhile
// (ensureAuthorship).
func (c *Catalog) startAuthorship(generation string) {
	c.startAuthorshipInContext(generation, nil)
}

// authorshipFollowUp reports whether a rebuild that just succeeded should
// check again for inputs written while it ran.
// A skipped follow-up is remembered for the ingest's end: a scheduled
// index runs no analysis chain, and a page does not rebuild a ledger of the
// current rules, so without it authorship could stay stale until the next
// manual index.
func (c *Catalog) authorshipFollowUp(parent context.Context) bool {
	if parent != nil || c.ingestRuns.Load() == 0 {
		return true
	}
	c.authorship.deferred.Store(true)
	// The ingest may have ended between the check and the store, its end
	// finding nothing deferred: then follow up now.
	return c.ingestRuns.Load() == 0 && c.authorship.deferred.Swap(false)
}

// ingestStarted and ingestEnded bracket an ingest in this process. Once the
// last one ends, a follow-up rebuild deferred while it ran runs, once. A
// manual index's chain has rebuilt by then, so the refresh finds the
// ledger current and starts nothing. An ingest that was stopped, or that
// ends because the service is stopping, starts no work the stop no longer
// controls: the follow-up stays deferred for the next ingest to end
// normally, and the generation stays dirty for an explicit refresh.
func (c *Catalog) ingestStarted() { c.ingestRuns.Add(1) }

func (c *Catalog) ingestEnded(stopped bool) {
	if c.ingestRuns.Add(-1) != 0 || stopped || c.serviceStopping() {
		return
	}
	if c.authorship.deferred.Swap(false) {
		c.refreshAuthorship()
	}
}

func (c *Catalog) startAuthorshipInContext(generation string, parent context.Context) bool {
	state := &c.authorship
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.running {
		return false
	}
	state.running = true
	rebuild := func(ctx context.Context, op *Op) error {
		if parent != nil {
			workCtx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(parent, cancel)
			defer stop()
			defer cancel()
			if parent.Err() != nil {
				cancel()
			}
			ctx = workCtx
			if report, ok := parent.Value(workProgressKey{}).(func(activityWork)); ok {
				ctx = withWorkProgress(ctx, func(w activityWork) { op.mirrorWork(w); report(w) })
			} else {
				ctx = withWorkProgress(ctx, op.mirrorWork)
			}
		} else {
			ctx = withWorkProgress(ctx, op.mirrorWork)
		}
		err := c.RebuildAuthorship(ctx, generation)
		state.mu.Lock()
		state.running, state.lastError = false, ""
		// Cancellation interrupts a rebuild; it must not block a later page retry.
		if err != nil && ctx.Err() == nil {
			state.lastError = err.Error()
		}
		state.mu.Unlock()
		if err == nil && ctx.Err() == nil && c.authorshipFollowUp(parent) {
			c.refreshAuthorshipInContext(parent)
		}
		return err
	}
	// From a page (or the usage summary) the rebuild is a read-started write
	// (§1) that the page waits for, so foreground (§10); from an index's
	// analysis chain it is a child of that stage, in its class.
	spec := OperationSpec{Kind: "analysis.authorship", Trigger: triggerRead, Priority: priorityForeground}
	if parent != nil {
		spec.Priority = ""
		spec.Trigger = triggerChained
		spec.Parent = opFrom(parent).ID()
	}
	if !c.startBackground(spec, rebuild) {
		state.running = false // the service is stopping
	}
	return state.running
}

// authorshipRunning reports whether a rebuild is writing message_authorship.
func (c *Catalog) authorshipRunning() bool {
	c.authorship.mu.Lock()
	defer c.authorship.mu.Unlock()
	return c.authorship.running
}

// RebuildAuthorship links headless runs to the commands that launched them,
// classifies every user message of the work each mirror group is represented
// by, in send order, and replaces message_authorship and
// conversation_launches.
func (c *Catalog) RebuildAuthorship(ctx context.Context, generation string) error {
	reportWork(ctx, "reading authorship inputs", "", 0, -1)
	inputs, launches, err := c.authorshipInputs(ctx)
	if err != nil {
		return err
	}
	results, err := c.classifyInputs(ctx, inputs)
	if err != nil {
		return err
	}
	return c.storeAuthorship(ctx, results, launches, generation)
}

// The user messages to classify, and the agent replies, in send order, that
// they are compared with. The existing prose and tool-ledger time indexes
// stream both sides without scanning wide tool results or adding a new index.
const (
	authorshipInputsQuery = `SELECT m.id,m.conversation_id,c.workspace_id,m.text,m.created_at,COALESCE(m.sender,''),w.source_kind,c.parent_id IS NOT NULL,
			COALESCE(c.harness,''),COALESCE(c.origin_host_id,'')
		FROM messages m
		JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id
		WHERE m.kind='message' AND m.role='user' AND m.created_at IS NOT NULL`
	authorshipRepliesQuery = `SELECT id,conversation_id,created_at,text,kind FROM messages
		WHERE kind='message' AND role='assistant' AND created_at>=?1
		UNION ALL SELECT m.id,m.conversation_id,t.started_at,m.text,m.kind FROM tool_calls t
        JOIN messages m ON m.id=t.call_message_id
        WHERE t.started_at>=?1 AND t.tool_category IN ('command','edit','user','mcp','other')
        AND m.kind='tool_call' AND m.role='assistant' ORDER BY created_at`
)

// authorshipInputs links headless runs to the commands that launched them,
// and reads the user messages to classify.
func (c *Catalog) authorshipInputs(ctx context.Context) ([]authorInput, []conversationLaunch, error) {
	workspaces, err := queryMapsContext(ctx, c.DB, `SELECT w.id,w.source_kind,w.activity_at FROM workspaces w`)
	if err != nil {
		return nil, nil, err
	}
	// Not suppressMirrors: its cached links can predate a running ingest's
	// identity reconciliation (see cacheVersion), and these results are stored
	// as current for this generation.
	links, err := queryMapsContext(ctx, c.DB, identityLinkWorkspacesSQL)
	if err != nil {
		return nil, nil, err
	}
	representative := map[string]string{}
	for _, row := range suppressMirrorRows(workspaces, links) {
		if mirrors, ok := row["mirrored_workspace_ids"].([]string); ok {
			for _, id := range mirrors {
				representative[id] = firstString(row["id"])
			}
		}
	}
	claimed, err := c.appClaims(ctx)
	if err != nil {
		return nil, nil, err
	}
	launches, err := c.deriveLaunches(ctx, claimed, representative)
	if err != nil {
		return nil, nil, err
	}
	inputs, err := c.authorInputs(ctx, representative, claimed, launches)
	return inputs, launches, err
}

// appClaims returns the conversations an app records starting: those a
// Conductor session links to.
func (c *Catalog) appClaims(ctx context.Context) (map[string]bool, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT left_id FROM conversation_identity_links UNION SELECT right_id FROM conversation_identity_links`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claimed := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		claimed[id] = true
	}
	return claimed, rows.Err()
}

// authorInputs reads the user messages to classify, skipping mirrored works
// (the keys of representative).
func (c *Catalog) authorInputs(ctx context.Context, representative map[string]string, claimed map[string]bool, launches []conversationLaunch) ([]authorInput, error) {
	// Only a Mac whose Conductor is read can show a session was not its own.
	conductorHosts := map[string]bool{}
	hosts, err := c.DB.QueryContext(ctx, `SELECT DISTINCT c.origin_host_id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE w.source_kind='conductor' AND c.origin_host_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for hosts.Next() {
		var host string
		if err := hosts.Scan(&host); err != nil {
			hosts.Close()
			return nil, err
		}
		conductorHosts[host] = true
	}
	hosts.Close()
	if err := hosts.Err(); err != nil {
		return nil, err
	}
	launched := map[string]*conversationLaunch{}
	for index := range launches {
		launched[launches[index].child] = &launches[index]
	}
	rows, err := c.DB.QueryContext(ctx, authorshipInputsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []authorInput{}
	for rows.Next() {
		var input authorInput
		var sentAt, host string
		if err := rows.Scan(&input.ID, &input.ConversationID, &input.WorkspaceID, &input.Text, &sentAt, &input.Sender, &input.SourceKind, &input.SubAgent, &input.Harness, &host); err != nil {
			return nil, err
		}
		parsed, ok := parseTime(sentAt)
		if !ok || representative[input.WorkspaceID] != "" || strings.TrimSpace(input.Text) == "" {
			continue
		}
		input.SentAt = parsed
		input.Unclaimed = conductorHosts[host] && !claimed[input.ConversationID]
		input.Launch = launched[input.ConversationID]
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		if !inputs[i].SentAt.Equal(inputs[j].SentAt) {
			return inputs[i].SentAt.Before(inputs[j].SentAt)
		}
		return inputs[i].ID < inputs[j].ID
	})
	return inputs, nil
}

// classifyInputs walks user messages in send order while streaming agent
// replies in the same order, so the agent index always holds exactly the
// replies of the preceding window.
func (c *Catalog) classifyInputs(ctx context.Context, inputs []authorInput) ([]authorResult, error) {
	results := make([]authorResult, 0, len(inputs))
	if len(inputs) == 0 {
		return results, nil
	}
	rows, err := c.DB.QueryContext(ctx, authorshipRepliesQuery, formatTime(inputs[0].SentAt.Add(-authorshipWindow)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	classifier := newClassifier(inputs)
	var pending *struct {
		at     time.Time
		origin textOrigin
		text   string
		kind   string
	}
	next := func() error {
		pending = nil
		for rows.Next() {
			var id, conversation, createdAt, text, kind string
			if err := rows.Scan(&id, &conversation, &createdAt, &text, &kind); err != nil {
				return err
			}
			if at, ok := parseTime(createdAt); ok {
				pending = &struct {
					at     time.Time
					origin textOrigin
					text   string
					kind   string
				}{at, textOrigin{conversation, id}, text, kind}
				return nil
			}
		}
		return rows.Err()
	}
	if err := next(); err != nil {
		return nil, err
	}
	reportWork(ctx, "classifying authorship", "message", 0, len(inputs))
	for index, input := range inputs {
		cutoff := input.SentAt.Add(-authorshipWindow)
		for pending != nil && pending.at.Before(input.SentAt) {
			if pending.kind == "tool_call" {
				classifier.addAuthorshipTool(pending.at, pending.origin, pending.text)
			} else {
				classifier.addAgentText(pending.at, pending.origin, pending.text, false)
			}
			classifier.agentText.evict(cutoff)
			if err := next(); err != nil {
				return nil, err
			}
		}
		classifier.agentText.evict(cutoff)
		results = append(results, authorResult{Input: input, Spans: classifier.classify(input)})
		if index%100 == 0 || index+1 == len(inputs) {
			reportWork(ctx, "classifying authorship", "message", index+1, len(inputs))
		}
	}
	return results, nil
}

func (c *Catalog) storeAuthorship(ctx context.Context, results []authorResult, launches []conversationLaunch, generation string) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_launches"); err != nil {
		return err
	}
	for _, launch := range launches {
		if _, err := tx.ExecContext(ctx, "INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES(?,?,?,?)",
			launch.child, launch.parent, launch.toolCall, launch.program); err != nil {
			return err
		}
	}
	for _, table := range []string{"message_authorship", "message_authorship_spans"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	// Launches are in place, so works folded into another can be found.
	parents, _, err := foldedParents(ctx, tx)
	if err != nil {
		return err
	}
	columns := append([]string{"message_id", "conversation_id", "workspace_id", "sent_at", "day", "total_chars"}, authorshipColumns()...)
	columns = append(columns, "spans_json", "work_id", "words", "main_category", "categories", "rules")
	statement, err := tx.PrepareContext(ctx, "INSERT INTO message_authorship("+strings.Join(columns, ",")+") VALUES("+placeholders(len(columns))+")")
	if err != nil {
		return err
	}
	defer statement.Close()
	spanStatement, err := tx.PrepareContext(ctx, `INSERT INTO message_authorship_spans(message_id,position,category,rule,reason,start_byte,end_byte,chars,words,
		source_conversation_id,source_message_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer spanStatement.Close()
	reportWork(ctx, "saving authorship", "message", 0, len(results))
	for resultIndex, result := range results {
		input := result.Input
		counts := countSpans(input.Text, result.Spans)
		spans, _ := json.Marshal(result.Spans)
		values := []any{input.ID, input.ConversationID, input.WorkspaceID, formatTime(input.SentAt), input.SentAt.Local().Format("2006-01-02"),
			utf8.RuneCountInString(strings.TrimSpace(input.Text))}
		words := 0
		for _, category := range authorshipCategories {
			values = append(values, counts.chars[category], counts.words[category])
			words += counts.words[category]
		}
		summary := summarizeSpans(input.Text, result.Spans)
		values = append(values, string(spans), rootWorkspace(input.WorkspaceID, parents), words, nilIfEmpty(summary.main), summary.categories, summary.rules)
		if _, err := statement.ExecContext(ctx, values...); err != nil {
			return err
		}
		wordCounts := authorshipWordCounts(input.Text, result.Spans)
		for position, span := range result.Spans {
			value := input.Text[span.Start:span.End]
			if _, err := spanStatement.ExecContext(ctx, input.ID, position, span.Category, spanRule(span), nilIfEmpty(span.Reason), span.Start, span.End,
				utf8.RuneCountInString(strings.TrimSpace(value)), wordCounts[position], nilIfEmpty(span.Source), nilIfEmpty(span.Message)); err != nil {
				return err
			}
		}
		if resultIndex%100 == 0 || resultIndex+1 == len(results) {
			reportWork(ctx, "saving authorship", "message", resultIndex+1, len(results))
		}
	}
	for key, value := range map[string]string{"authorship_generation": generation, "authorship_built_at": now()} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	if err := cacheAuthoredWorkspaceNames(tx, results); err != nil {
		return err
	}
	return tx.Commit()
}

// AuthorshipStats summarizes user-turn text for Settings: all-time and
// last-30-day totals, and a daily series the page rolls up by week or month.
// Every total has characters and words per category.
func (c *Catalog) AuthorshipStats(ctx context.Context) (map[string]any, error) {
	stale := c.ensureAuthorship()
	builtAt, err := c.metaValue(ctx, "authorship_built_at")
	if err != nil {
		return nil, err
	}
	sums := []string{"COUNT(*) messages", "COALESCE(SUM(typed_words>0),0) typed_messages"}
	for _, column := range authorshipColumns() {
		sums = append(sums, fmt.Sprintf("COALESCE(SUM(%s),0) %s", column, column))
	}
	aggregate := strings.Join(sums, ",")
	totals, err := queryMapsContext(ctx, c.DB, "SELECT "+aggregate+",COALESCE(SUM(total_chars),0) total_chars,MIN(day) first_day,MAX(day) last_day FROM message_authorship")
	if err != nil {
		return nil, err
	}
	since := c.clock().AddDate(0, 0, -29).Format("2006-01-02")
	recent, err := queryMapsContext(ctx, c.DB, "SELECT "+aggregate+" FROM message_authorship WHERE day>=?", since)
	if err != nil {
		return nil, err
	}
	daily, err := queryMapsContext(ctx, c.DB, "SELECT day,"+aggregate+" FROM message_authorship GROUP BY day ORDER BY day")
	if err != nil {
		return nil, err
	}
	state := &c.authorship
	state.mu.Lock()
	defer state.mu.Unlock()
	return map[string]any{
		"version": authorshipVersion, "built_at": nilIfEmpty(builtAt), "running": state.running, "stale": stale, "error": nilIfEmpty(state.lastError),
		"window_hours": int(authorshipWindow.Hours()), "totals": totals[0], "last_30_days": recent[0], "daily": daily,
	}, nil
}

// attachAuthorship adds each classified user message's spans, with excerpts,
// so the transcript reader can say where its text came from and link to the
// conversation holding copied text.
func (c *Catalog) attachAuthorship(conversationID any, messages []map[string]any) error {
	attach, err := c.messageAuthorshipAttacher(conversationID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		attach(message)
	}
	return nil
}

// Prepare once so reader rows can attach spans before their bodies are clipped.
func (c *Catalog) messageAuthorshipAttacher(conversationID any) (func(map[string]any), error) {
	rows, err := queryMaps(c.DB, "SELECT message_id,typed_words,pasted_words,spans_json FROM message_authorship WHERE conversation_id=?", conversationID)
	if err != nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	decoded := map[string][]authorSpan{}
	sources := map[string]bool{}
	for _, row := range rows {
		id := firstString(row["message_id"])
		var spans []authorSpan
		if json.Unmarshal([]byte(firstString(row["spans_json"])), &spans) != nil {
			continue
		}
		byID[id], decoded[id] = row, spans
		for _, span := range spans {
			if span.Source != "" {
				sources[span.Source] = true
			}
		}
	}
	workspaces := map[string]map[string]any{}
	if len(sources) > 0 {
		ids := make([]any, 0, len(sources))
		for id := range sources {
			ids = append(ids, id)
		}
		found, err := queryMaps(c.DB, `SELECT c.id,c.workspace_id,w.title FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			WHERE c.id IN (`+placeholders(len(ids))+`)`, ids...)
		if err != nil {
			return nil, err
		}
		for _, row := range found {
			workspaces[firstString(row["id"])] = row
		}
	}
	return func(message map[string]any) {
		id := firstString(message["id"])
		row := byID[id]
		if row == nil {
			return
		}
		text := firstString(message["text"])
		parts := []map[string]any{}
		wordCounts := authorshipWordCounts(text, decoded[id])
		for position, span := range decoded[id] {
			if span.End > len(text) {
				break
			}
			value := strings.TrimSpace(text[span.Start:span.End])
			part := map[string]any{"category": span.Category, "reason": nilIfEmpty(span.Reason), "words": wordCounts[position], "excerpt": excerpt(value, 120)}
			if source := workspaces[span.Source]; source != nil {
				part["source"] = map[string]any{"workspace_id": source["workspace_id"], "conversation_id": span.Source, "message_id": nilIfEmpty(span.Message), "title": source["title"]}
			}
			parts = append(parts, part)
		}
		message["authorship"] = map[string]any{"typed_words": row["typed_words"], "pasted_words": row["pasted_words"], "spans": parts}
	}, nil
}

func excerpt(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}
