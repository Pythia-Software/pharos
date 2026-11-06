package archive

import (
	"context"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Two-sided agent events: an observation and the earlier text it is about.
//
// Many events point back at something: an assumption about what the person
// meant, confusion about an instruction, a mistake in an earlier reply, a
// stale doc. findAgentEventTargets gives each event its most likely target,
// in order of preference:
//
//	quote    the event quotes words that appear verbatim in an earlier
//	         message: the person's own words first, then the nearest earlier
//	         message. A code quote (`name`) only matches the person's prompts:
//	         mentioning an identifier the agent wrote is not being about it.
//	overlap  the event shares specific words (weighted by how rare they are
//	         among this conversation's prompt sentences) with a sentence of
//	         the prompt that started its turn.
//	file     a stale-guidance event names a doc or instruction file.
//
// Targets are computed when a view reads them, not stored: the work is one
// conversation's events against that conversation's earlier messages, the
// reader already holds that text, and the other views read it only for
// conversations that have two-sided events, each message capped at
// agentEventLinkTextLimit. A library-wide pass would only have to be redone
// whenever a prompt or event changed. Ported from the Agent Experience lab's
// links.py.

// agentEventTwoSided are the families whose sentence can point back at
// earlier text. Friction like a failed command or a missing tool is about the
// environment, not something said earlier.
var agentEventTwoSided = map[string]bool{"assumption": true, "ambiguous_request": true, "confusion": true, "surprise": true,
	"own_mistake": true, "pushback": true, "stale_guidance": true, "unverified": true, "discovery": true, "unblocking_insight": true}

// agentEventLinkTextLimit bounds how much of each earlier message the
// fingerprint and timeline search (bytes, from the start). The reader
// searches the text it already has.
var agentEventLinkTextLimit = 64 << 10

// agentEventOverlapScore is the weighted overlap (sum of idf) a prompt
// sentence needs, with at least two shared words.
const agentEventOverlapScore = 4.0

type eventLinkMessage struct {
	ID, Role, Kind, Text string
	At                   any // created_at, for the timeline
	Bytes                int // the whole text's length, for position
}

type eventLinkEvent struct {
	ID                          string
	MessageID, Family           string
	Sentence, Subject, Response string
}

// agentEventTarget is where an event points. Start and End are byte offsets
// into the target message's text.
type agentEventTarget struct {
	Method     string
	MessageID  string
	Role, Kind string
	Start, End int
	Text       string
	Quote      string
	Strong     bool
	Shared     []string
	Score      float64
	Files      []string
}

var (
	agentEventQuote       = regexp.MustCompile("\"([^\"\\n]{3,80})\"|“([^”\\n]{3,80})”|`([^`\\n]{3,60})`")
	agentEventSingleQuote = regexp.MustCompile(`[‘']([^‘’'\n]{3,80})[’']`)
	agentEventQuoteWord   = regexp.MustCompile(`[a-z][a-z'-]{2,}`)
	agentEventToken       = regexp.MustCompile(`[a-z0-9][a-z0-9_./:-]*[a-z0-9]|[a-z0-9]`)
	agentEventHarness     = regexp.MustCompile(`(?is)(?:<system_instruction>.*?</system_instruction>|<system-reminder>.*?</system-reminder>|<environment_context>.*?</environment_context>|<user_instructions>.*?</user_instructions>)\s*`)
	agentEventDocFile     = regexp.MustCompile(`(?i)\b((?:[\w.-]+/)*[\w.-]*(?:CLAUDE|AGENTS|HANDOFF|README|CHECKPOINT|PLAN|BRIEF|STATUS|NOTES|TODO|MEMORY|SKILL|REPORT)[\w.-]*(?:\.md)?|[\w./-]+\.md)\b`)
	// An assumption is often about the code; it is about the request when its
	// sentence refers to the person or what they asked.
	agentEventRequestRef = regexp.MustCompile(`(?i)\b(user|you|your|asked|ask|requested|request|instructions?|brief|prompt|meant|means|meaning|wants?|wanted|intended|intent|spec|wording|said|says|phrase|interpret\w*)\b`)
	agentEventNumberWord = map[string]string{"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10"}
	// Common words, discourse words ("root cause", "turns out") and the
	// lexicon's own cue words, which every event of a kind shares.
	agentEventStop = func() map[string]bool {
		words := `a an the and or but if then else of to in on at by for with from into onto as is are was were be been being it its
this that these those there here i we you they he she me my our your their them us so not no yes do does did done doing
have has had having can could should would will shall may might must just also only very more most less least much many
some any all each every both either neither than too again still yet already now later soon ever never always often
about above below over under after before between through during without within while because since until unless
which who whom whose what when where why how let lets let's i'm i'll i've it's that's there's don't doesn't didn't isn't
aren't wasn't weren't won't can't couldn't shouldn't wouldn't one two first second next last new old same other another
need needs needed want wants make makes made use used using get gets got go goes going see seems seem look looks like
way thing things something anything nothing actually really probably maybe likely sure think thought know knew
check checking checked run running ran test tests file files code change changes work working
please could would should will just also want need make sure let me lets thing things you're we're
root cause causes caused found find finds finding confirmed confirm confirms confirming identified
traced trace traces tracing turns turned issue issues problem problems bug bugs fix fixed fixes fixing failed fail fails
failing failure failures error errors pass passed passes passing works worked broken break breaks real actual actually
clear clearly same case cases explains explain explained reason reasons why means meaning instead correct correctly
wrong properly expected expect behavior behaviour result results happens happened happening rather already currently
doesn isn wasn didn remains remain remaining pending blocked unavailable missing couldn could cannot unable able seems appears likely
probably here there this that these those because since though although however still just also only
assume assumes assumed assuming assumption unclear ambiguous confused confusing surprised surprising mistake realize realized
realizing notice noticed noticing user user's asked request requested`
		stop := map[string]bool{}
		for _, w := range strings.Fields(words) {
			stop[w] = true
		}
		return stop
	}()
)

// asciiLower lowers ASCII letters only, so byte offsets in the result are
// offsets in s.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// agentEventOwnSpans are the byte ranges of a prompt outside harness
// wrappers (Conductor's system instruction and the like): the person's words.
func agentEventOwnSpans(text string) [][2]int {
	spans, at := [][2]int{}, 0
	for _, m := range agentEventHarness.FindAllStringIndex(text, -1) {
		if m[0] > at {
			spans = append(spans, [2]int{at, m[0]})
		}
		at = m[1]
	}
	if at < len(text) {
		spans = append(spans, [2]int{at, len(text)})
	}
	return spans
}

// agentEventLinkSentences splits text into sentences: a line break, or a
// '.', '!' or '?' followed by space or the end. A dot inside "fast-path.md" or
// "1.5" does not end one.
func agentEventLinkSentences(text string) [][2]int {
	out, start := [][2]int{}, 0
	flush := func(end int) {
		if end > start {
			out = append(out, [2]int{start, end})
		}
	}
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\n':
			flush(i)
			start = i + 1
		case c == '.' || c == '!' || c == '?':
			if i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t' || text[i+1] == '\n' || text[i+1] == '\r' {
				flush(i + 1)
				start = i + 1
			}
		}
	}
	flush(len(text))
	return out
}

// agentEventContent is the set of specific words in text: no stop or
// discourse words, longer than three letters, plurals and number words
// folded, plain hyphenated words split.
func agentEventContent(text string) map[string]bool {
	out := map[string]bool{}
	for _, token := range agentEventToken.FindAllString(strings.ToLower(text), -1) {
		parts := []string{token}
		if strings.Contains(token, "-") && strings.IndexFunc(token, func(r rune) bool { return r != '-' && !unicode.IsLetter(r) }) < 0 {
			parts = strings.Split(token, "-")
		}
		for _, w := range parts {
			if n, ok := agentEventNumberWord[w]; ok {
				w = n
			}
			if agentEventStop[w] || len(w) <= 3 || strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
				continue
			}
			if len(w) > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && strings.IndexFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) < 0 {
				w = w[:len(w)-1]
			}
			out[w] = true
		}
	}
	return out
}

type agentEventQuoted struct {
	at   int
	text string
	code bool
}

// agentEventQuotes are the quoted phrases of an event's sentence (and its
// subject), in order. One short word ("ok") is not a quote.
func agentEventQuotes(text string) []agentEventQuoted {
	found := []agentEventQuoted{}
	taken := [][2]int{}
	add := func(at, end int, q string, code bool) {
		q = strings.Trim(q, " ,.")
		words := 0
		for _, w := range agentEventQuoteWord.FindAllString(strings.ToLower(q), -1) {
			if !agentEventStop[w] {
				words++
			}
		}
		if words == 0 || (len(strings.Fields(q)) == 1 && utf8.RuneCountInString(q) < 5) {
			return
		}
		found = append(found, agentEventQuoted{at, q, code})
		taken = append(taken, [2]int{at, end})
	}
	for _, m := range agentEventQuote.FindAllStringSubmatchIndex(text, -1) {
		for g := 1; g <= 3; g++ {
			if m[2*g] >= 0 {
				add(m[0], m[1], text[m[2*g]:m[2*g+1]], g == 3)
			}
		}
	}
	// Single quotes need a non-word character on both sides, so apostrophes
	// ("don't … it's") are not quotes. RE2 has no lookaround: check by hand.
	word := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	for at := 0; at < len(text); {
		m := agentEventSingleQuote.FindStringSubmatchIndex(text[at:])
		if m == nil {
			break
		}
		start, end := at+m[0], at+m[1]
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		inside := slices.ContainsFunc(taken, func(r [2]int) bool { return start < r[1] && end > r[0] })
		if (start == 0 || !word(before)) && (end == len(text) || !word(after)) && !inside {
			add(start, end, text[at+m[2]:at+m[3]], false)
			at = end
			continue
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		at = start + size
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].at < found[j].at })
	return found
}

func agentEventAboutRequest(e eventLinkEvent) bool {
	switch e.Family {
	case "ambiguous_request", "pushback":
		return true
	case "assumption", "confusion", "own_mistake":
		return agentEventRequestRef.MatchString(e.Sentence)
	}
	return false
}

// agentEventSubject is the event's subject when it adds to the sentence.
func agentEventSubject(e eventLinkEvent) string {
	if e.Subject != "" && e.Subject != e.Sentence {
		return e.Subject
	}
	return ""
}

var agentEventLinkKinds = map[string]bool{"message": true, "reasoning": true, "thinking": true}

// findAgentEventTargets returns each event's target (nil for none), keyed by
// event ID. messages are the conversation's messages in reading order.
func findAgentEventTargets(messages []eventLinkMessage, events []eventLinkEvent) map[string]*agentEventTarget {
	index := map[string]int{}
	lower := make([]string, len(messages))
	prompt := make([]int, len(messages)) // the prompt that started each message's turn, -1 for none
	last := -1
	for i, m := range messages {
		index[m.ID] = i
		if m.Role == "user" && m.Kind == "message" {
			last = i
		}
		prompt[i] = last
	}
	// idf over this conversation's prompt sentences, so an overlap rests on
	// words specific to it.
	df, n := map[string]int{}, 0
	for _, m := range messages {
		if m.Role != "user" || m.Kind != "message" {
			continue
		}
		for _, span := range agentEventOwnSpans(m.Text) {
			for _, s := range agentEventLinkSentences(m.Text[span[0]:span[1]]) {
				for w := range agentEventContent(m.Text[span[0]+s[0] : span[0]+s[1]]) {
					df[w]++
				}
				n++
			}
		}
	}
	idf := func(w string) float64 { return math.Log(float64(1+n) / float64(1+df[w])) }
	out := map[string]*agentEventTarget{}
	for _, e := range events {
		pos, placed := index[e.MessageID]
		var target *agentEventTarget
		about := e.Sentence + " " + agentEventSubject(e)
		// 1. A verbatim quote of an earlier message.
		if agentEventTwoSided[e.Family] && placed {
			for _, q := range agentEventQuotes(about) {
				needle := asciiLower(q.text)
				type hit struct{ message, at int }
				var first, user *hit
				for j := pos - 1; j >= 0 && user == nil; j-- {
					m := messages[j]
					if !agentEventLinkKinds[m.Kind] || (q.code && m.Role != "user") {
						continue
					}
					if lower[j] == "" && m.Text != "" {
						lower[j] = asciiLower(m.Text)
					}
					if at := strings.Index(lower[j], needle); at >= 0 {
						h := &hit{j, at}
						if first == nil {
							first = h
						}
						// The person's words win: an agent's earlier text that
						// quotes the prompt is a copy, not the source.
						if m.Role == "user" {
							user = h
						}
					}
				}
				chosen := user
				if chosen == nil {
					chosen = first
				}
				if chosen != nil {
					m := messages[chosen.message]
					target = &agentEventTarget{Method: "quote", MessageID: m.ID, Role: m.Role, Kind: m.Kind, Start: chosen.at, End: chosen.at + len(needle),
						Text: m.Text[chosen.at : chosen.at+len(needle)], Quote: q.text,
						// One quoted word in an agent's earlier text is often
						// just a shared identifier.
						Strong: m.Role == "user" || len(strings.Fields(q.text)) >= 2}
					break
				}
			}
		}
		// 2. A sentence of the turn's prompt, by weighted shared words.
		if target == nil && agentEventTwoSided[e.Family] && placed && prompt[pos] >= 0 && prompt[pos] < pos &&
			(e.Family != "assumption" || agentEventAboutRequest(e)) {
			p := messages[prompt[pos]]
			words := agentEventContent(about)
			for _, span := range agentEventOwnSpans(p.Text) {
				own := p.Text[span[0]:span[1]]
				for _, s := range agentEventLinkSentences(own) {
					sentence := own[s[0]:s[1]]
					trimmed := strings.TrimSpace(sentence)
					if len(trimmed) < 12 {
						continue
					}
					shared := []string{}
					score := 0.0
					for w := range agentEventContent(sentence) {
						if words[w] {
							shared = append(shared, w)
							score += idf(w)
						}
					}
					if len(shared) < 2 || score < agentEventOverlapScore || (target != nil && score <= target.Score) {
						continue
					}
					sort.Slice(shared, func(i, j int) bool {
						if a, b := idf(shared[i]), idf(shared[j]); a != b {
							return a > b
						}
						return shared[i] < shared[j]
					})
					lead := len(sentence) - len(strings.TrimLeftFunc(sentence, unicode.IsSpace))
					start := span[0] + s[0] + lead
					target = &agentEventTarget{Method: "overlap", MessageID: p.ID, Role: p.Role, Kind: p.Kind, Start: start, End: start + len(trimmed),
						Text: trimmed, Shared: shared[:min(6, len(shared))], Score: math.Round(score*10) / 10}
				}
			}
		}
		// 3. A named doc or instruction file.
		if target == nil && e.Family == "stale_guidance" {
			names := []string{}
			for _, name := range agentEventDocFile.FindAllString(e.Sentence+" "+e.Response, -1) {
				names = append(names, name[strings.LastIndex(name, "/")+1:])
			}
			slices.Sort(names)
			if names = slices.Compact(names); len(names) > 0 {
				target = &agentEventTarget{Method: "file", Files: names}
			}
		}
		out[e.ID] = target
	}
	return out
}

// agentEventTargetJSON is a target as the views read it: offsets in UTF-16
// code units of the target message's text, JavaScript's string indexes.
func agentEventTargetJSON(t *agentEventTarget, texts map[string]string) map[string]any {
	if t == nil {
		return nil
	}
	if t.Method == "file" {
		return map[string]any{"method": "file", "files": t.Files}
	}
	units, ok := utf16Offsets(texts[t.MessageID], []int{t.Start, t.End})
	if !ok {
		return nil
	}
	out := map[string]any{"method": t.Method, "message_id": t.MessageID, "role": t.Role, "kind": t.Kind,
		"start": units[t.Start], "end": units[t.End], "text": t.Text, "strong": t.Method == "quote" && t.Strong}
	if t.Method == "overlap" {
		out["shared"], out["score"] = t.Shared, t.Score
	}
	return out
}

// agentEventLinks reads one conversation's events (at or above the
// threshold) with their targets, for the fingerprint and the timeline. Each
// event carries its message, kind, group, a short sentence and its target;
// the text of earlier messages is read only when the conversation has a
// two-sided event, and only the first agentEventLinkTextLimit bytes of each.
func (c *Catalog) agentEventLinks(ctx context.Context, conversationID string) ([]map[string]any, error) {
	return c.agentEventLinksForMessages(ctx, conversationID, nil)
}

// A fingerprint requests only events on its bounded sample of messages.
// Targets are still resolved against the source conversation's earlier text.
// Nil messageIDs retains the full conversation behavior for timeline callers.
func (c *Catalog) agentEventLinksForMessages(ctx context.Context, conversationID string, messageIDs []string) ([]map[string]any, error) {
	var tables int
	if err := c.reader().QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='agent_events'").Scan(&tables); err != nil || tables == 0 {
		return nil, err
	}
	query := `SELECT 'e'||e.id id,e.message_id,e.family,e.probability,e.source,e.sentence,e.subject,e.response,e.sentence_start,e.sentence_end,
		(SELECT length(CAST(m.text AS BLOB)) FROM messages m WHERE m.id=e.message_id) message_bytes
		FROM agent_events e WHERE e.conversation_id=? AND e.probability>=?`
	args := []any{conversationID, agentEventThreshold}
	if messageIDs != nil {
		query += ` AND e.message_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonText(messageIDs))
	}
	rows, err := queryMapsContext(ctx, c.reader(), query+` ORDER BY e.message_id,e.sentence_start,e.probability DESC`, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	events := make([]eventLinkEvent, 0, len(rows))
	twoSided := false
	for _, row := range rows {
		e := eventLinkEvent{ID: firstString(row["id"]), MessageID: firstString(row["message_id"]), Family: firstString(row["family"]),
			Sentence: firstString(row["sentence"]), Subject: firstString(row["subject"]), Response: firstString(row["response"])}
		twoSided = twoSided || agentEventTwoSided[e.Family]
		events = append(events, e)
	}
	messages := []eventLinkMessage{}
	if twoSided {
		list, err := queryMapsContext(ctx, c.reader(), `SELECT id,role,kind,created_at,substr(text,1,?) text,length(CAST(text AS BLOB)) bytes FROM messages
			WHERE conversation_id=? AND kind IN ('message','reasoning','thinking')
			ORDER BY source_order IS NULL,source_order,created_at IS NULL,created_at,id`, agentEventLinkTextLimit, conversationID)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			messages = append(messages, eventLinkMessage{ID: firstString(m["id"]), Role: firstString(m["role"]), Kind: firstString(m["kind"]), Text: firstString(m["text"]), At: m["created_at"], Bytes: int(integer(m["bytes"]))})
		}
	}
	return agentEventLinkRows(rows, messages, events), nil
}

// agentEventPosition is how far into its message a byte offset sits, in
// [0, 1]: the timeline places an event (and an arc's target) at that share
// of the message's span, the way it places a message's text-origin parts.
func agentEventPosition(offset, size int) (float64, bool) {
	if size <= 0 || offset < 0 {
		return 0, false
	}
	return math.Round(math.Min(1, float64(offset)/float64(size))*10000) / 10000, true
}

// agentEventLinkRows joins events with their targets for the views.
func agentEventLinkRows(rows []map[string]any, messages []eventLinkMessage, events []eventLinkEvent) []map[string]any {
	targets := findAgentEventTargets(messages, events)
	texts, times, sizes := make(map[string]string, len(messages)), make(map[string]any, len(messages)), make(map[string]int, len(messages))
	for _, m := range messages {
		texts[m.ID], times[m.ID], sizes[m.ID] = m.Text, m.At, m.Bytes
	}
	out := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		family, known := agentEventFamilyByKey(events[i].Family)
		if !known {
			continue
		}
		probability, _ := row["probability"].(float64)
		sentence := events[i].Sentence
		if utf8.RuneCountInString(sentence) > 280 {
			sentence = string([]rune(sentence)[:279]) + "…"
		}
		target := agentEventTargetJSON(targets[events[i].ID], texts)
		if target != nil && target["message_id"] != nil {
			id := firstString(target["message_id"])
			target["at"] = times[id]
			if t := targets[events[i].ID]; t != nil {
				if position, ok := agentEventPosition(t.Start, sizes[id]); ok {
					target["position"] = position
				}
			}
		}
		item := map[string]any{"id": events[i].ID, "message_id": events[i].MessageID, "kind": family.Key, "label": family.Label, "group": family.Group,
			"probability": math.Round(probability*1000) / 1000, "source": firstString(row["source"]), "sentence": sentence,
			"target": target,
			// The sentence's byte range in its message: views merge events on
			// near-identical sentences (assets/event-spans.js) by it.
			"span": []int{int(integer(row["sentence_start"])), int(integer(row["sentence_end"]))}}
		if position, ok := agentEventPosition(int(integer(row["sentence_start"])), int(integer(row["message_bytes"]))); ok {
			item["position"] = position
		}
		out = append(out, item)
	}
	return out
}
