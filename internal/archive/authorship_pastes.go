package archive

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var (
	reasonConversationQuote = fixedReason("Pasted conversation")
	reasonReportedOutput    = fixedReason("Reported output")
	reviewItemPattern       = regexp.MustCompile(`^(?:[-*] |[0-9]+[.)] )?\*{0,2}\[?P[0-3]\]?:?\*{0,2}:?\s+(.+)$`)
	reviewLinkPattern       = regexp.MustCompile(`\[[^\]\n]*\]\([^\)\n]*\)`)
	reviewLocationPattern   = regexp.MustCompile(`(?i)\s+(?:Location|Code|[\w./-]+\.[A-Za-z]{1,8}):[0-9]+\.?\s*$`)
	transcriptSpeaker       = regexp.MustCompile(`(?i)^(?:ME|USER|HUMAN|CLAUDE|ASSISTANT|CHATGPT|CODEX|GEMINI|AGENT):(?:\s|$)`)
	transcriptIntro         = regexp.MustCompile(`(?i)\b(?:from (?:claude|chatgpt|codex|gemini|another chat)|copied|pasted|forwarded|(?:here is|below is|here’s) (?:a |the )?(?:conversation|transcript|exchange)\s*:)`)
	outputIntro             = regexp.MustCompile(`(?i)(?:(?:this|the following) (?:error|bug|output|message)\s*:?|\bfails?\b[^\n]*\bwith\s*:?|\b(?:error|output|message|exception)\s*:)\s*$`)
	literalQuote            = regexp.MustCompile(`"([^"\n]+)"|“([^”\n]+)”`)
	existingTextIntro       = regexp.MustCompile(`(?i)\b(?:says|reads|existing|current|getting rid of|remove|error message)\b`)
	newTextIntro            = regexp.MustCompile(`(?i)\b(?:should (?:now )?(?:say|read|be)|(?:rename|change|update).{0,40}\bto|replace.{0,40}\bwith|use|write|say|display|show|label it)\s*$`)
	humanParagraph          = regexp.MustCompile(`(?i)^(?:please\b|also\b|can you\b|could you\b|i\b|we\b|my\b|our\b|thanks\b)`)
	outputFailure           = regexp.MustCompile(`(?i)^(?:no\b|cannot\b|unable to\b|failed to\b|error\b)`)
	configurationReport     = regexp.MustCompile(`(?i)^.{1,100} configuration plan [—-] (?:cannot be applied|can be applied|requires approval)$`)
	reportField             = regexp.MustCompile(`^[a-z][a-z_ ]{0,40}:`)
	reportSection           = regexp.MustCompile(`^[A-Z][A-Z ]{3,}(?::.*)?$`)
	explicitDocumentIntro   = regexp.MustCompile(`(?i)\b(?:pasted|copied|forwarded)\b`)
)

func reviewItemKey(line string) string {
	line = strings.TrimSpace(line)
	m := reviewItemPattern.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	line = reviewLinkPattern.ReplaceAllString(m[1], "")
	line = fileLinePattern.ReplaceAllString(line, "")
	line = reviewLocationPattern.ReplaceAllString(strings.TrimSpace(line), "")
	tokens := wordTokens(line)
	if len(tokens) < 4 {
		return ""
	}
	words := make([]string, len(tokens))
	for i, t := range tokens {
		words[i] = t.word
	}
	return strings.Join(words, " ")
}

func reviewHeaderKey(line string) string {
	line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*"))
	if !strings.HasSuffix(line, ":") {
		return ""
	}
	tokens := wordTokens(line)
	if len(tokens) < 2 || len(tokens) >= quoteMinWords {
		return ""
	}
	words := make([]string, len(tokens))
	for i, t := range tokens {
		words[i] = t.word
	}
	return strings.Join(words, " ")
}

// Short findings only qualify inside a report already anchored to one source
// by a full-length match. Each short item must then match that same source.
func claimCopiedReviewItems(l *labeler, text string, runs []copiedRun, at time.Time) {
	sources := map[*shingleBatch]bool{}
	matched := map[*shingleBatch]int{}
	for _, run := range runs {
		if len(run.batch.reviews) > 0 {
			sources[run.batch] = true
		}
	}
	if len(sources) == 0 {
		return
	}
	for _, line := range textLines(text) {
		key := reviewItemKey(text[line[0]:line[1]])
		if key == "" {
			continue
		}
		var chosen *shingleBatch
		for source := range sources {
			if source.reviews[key] && (chosen == nil || source.at > chosen.at || source.at == chosen.at && source.origin.message < chosen.origin.message) {
				chosen = source
			}
		}
		if chosen != nil {
			matched[chosen]++
			reason := reasonAgentOutput
			if chosen.tool {
				reason = reasonAuthoredTool
			}
			l.claim(line[0], line[1], spanQuoted, reason.with(ago(at, chosen.at)), chosen.origin)
		}
	}
	for _, line := range textLines(text) {
		key := reviewHeaderKey(text[line[0]:line[1]])
		if key == "" {
			continue
		}
		var chosen *shingleBatch
		for source, count := range matched {
			if count >= 2 && source.headers[key] && (chosen == nil || source.at > chosen.at || source.at == chosen.at && source.origin.message < chosen.origin.message) {
				chosen = source
			}
		}
		if chosen != nil {
			reason := reasonAgentOutput
			if chosen.tool {
				reason = reasonAuthoredTool
			}
			l.claim(line[0], line[1], spanQuoted, reason.with(ago(at, chosen.at)), chosen.origin)
		}
	}
}

func claimExplicitPastes(l *labeler, text string, bodyStart int) {
	lines := textLines(text)
	claimPastedConversations(l, text, lines, bodyStart)
	for i, line := range lines {
		if line[0] < bodyStart {
			continue
		}
		value := strings.TrimSpace(text[line[0]:line[1]])
		if value == "" {
			continue
		}
		if outputIntro.MatchString(strings.NewReplacer("_", "", "*", "", "`", "").Replace(value)) {
			start := i + 1
			for start < len(lines) && strings.TrimSpace(text[lines[start][0]:lines[start][1]]) == "" {
				start++
			}
			if start < len(lines) && !humanParagraph.MatchString(strings.TrimSpace(text[lines[start][0]:lines[start][1]])) {
				end := start
				for end+1 < len(lines) && strings.TrimSpace(text[lines[end+1][0]:lines[end+1][1]]) != "" {
					end++
				}
				// UI error reports often have a short title, a blank line and
				// one failure paragraph. Extend only this explicit shape; a
				// later request or human explanation keeps its own classification.
				if end == start && len(wordTokens(text[lines[start][0]:lines[start][1]])) <= 6 {
					next := end + 1
					for next < len(lines) && strings.TrimSpace(text[lines[next][0]:lines[next][1]]) == "" {
						next++
					}
					if next < len(lines) && outputFailure.MatchString(strings.TrimSpace(text[lines[next][0]:lines[next][1]])) {
						end = next
						for end+1 < len(lines) && strings.TrimSpace(text[lines[end+1][0]:lines[end+1][1]]) != "" {
							end++
						}
					}
				}
				l.claim(lines[start][0], lines[end][1], spanPasted, reasonReportedOutput, textOrigin{})
			}
		}
		if configurationReport.MatchString(value) {
			end, signatures := i, 0
			for j := i + 1; j < len(lines); j++ {
				raw := text[lines[j][0]:lines[j][1]]
				v := strings.TrimSpace(raw)
				if v == "" {
					continue
				}
				if reportField.MatchString(v) || reportSection.MatchString(v) {
					signatures++
					end = j
					continue
				}
				if strings.HasPrefix(raw, "  ") || strings.HasPrefix(raw, "\t") || json.Valid([]byte(v)) {
					end = j
					continue
				}
				break
			}
			if signatures >= 3 {
				l.claim(line[0], lines[end][1], spanPasted, reasonReportedOutput, textOrigin{})
			}
		}
		// Explicitly refer to existing literal UI/output wording. New copy in a
		// request such as `Use "..."` is not excluded just for quotation marks.
		for _, loc := range literalQuote.FindAllStringSubmatchIndex(value, -1) {
			if loc[0] == 0 || (!existingTextIntro.MatchString(value[max(0, loc[0]-100):loc[0]]) || newTextIntro.MatchString(value[max(0, loc[0]-100):loc[0]])) {
				continue
			}
			a, b := loc[2], loc[3]
			if a < 0 {
				a, b = loc[4], loc[5]
			}
			if len(wordTokens(value[a:b])) < 8 {
				continue
			}
			start := line[0] + strings.Index(text[line[0]:line[1]], value)
			l.claim(start+loc[0], start+loc[1], spanQuoted, reasonConversationQuote, textOrigin{})
		}
	}
}

// The introduction establishes one conversation block. Subsequent speaker
// turns inherit it regardless of length; a blank followed by unlabelled prose
// ends the block so the person's follow-up remains eligible as typed text.
func claimPastedConversations(l *labeler, text string, lines [][2]int, bodyStart int) {
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line[0] < bodyStart || !transcriptSpeaker.MatchString(strings.TrimSpace(text[line[0]:line[1]])) ||
			!transcriptIntro.MatchString(text[max(bodyStart, line[0]-512):line[0]]) {
			continue
		}
		for {
			end := i
			for end+1 < len(lines) {
				next := strings.TrimSpace(text[lines[end+1][0]:lines[end+1][1]])
				if next == "" || transcriptSpeaker.MatchString(next) {
					break
				}
				end++
			}
			l.claim(lines[i][0], lines[end][1], spanQuoted, reasonConversationQuote, textOrigin{})
			next := end + 1
			for next < len(lines) && strings.TrimSpace(text[lines[next][0]:lines[next][1]]) == "" {
				next++
			}
			if next == len(lines) || !transcriptSpeaker.MatchString(strings.TrimSpace(text[lines[next][0]:lines[next][1]])) {
				i = end
				break
			}
			i = next
		}
	}
}

// When the introduction explicitly identifies a pasted document, its final
// heading owns the following paragraph even with a conventional blank line.
// A later free-standing human follow-up remains outside this bounded claim.
func pastedDocumentTail(text string, lines [][2]int, first, last int) int {
	if !headingLine(text[lines[last][0]:lines[last][1]]) || !explicitDocumentIntro.MatchString(text[:lines[first][0]]) {
		return last
	}
	start := last + 1
	for start < len(lines) && strings.TrimSpace(text[lines[start][0]:lines[start][1]]) == "" {
		start++
	}
	if start == len(lines) || humanParagraph.MatchString(strings.TrimSpace(text[lines[start][0]:lines[start][1]])) {
		return last
	}
	end := start
	for end+1 < len(lines) && strings.TrimSpace(text[lines[end+1][0]:lines[end+1][1]]) != "" {
		end++
	}
	return end
}
