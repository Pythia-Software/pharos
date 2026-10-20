package archive

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Feedback exports mix human comments with generated context. Keep byte ranges
// in the original message so the ledger and highlighted reader agree. These
// formats come from Agentation's generate-output.ts and our feedbackMarkdown().
type feedbackFormat int

const (
	feedbackUnknown feedbackFormat = iota
	feedbackStandardFormat
	feedbackCompactFormat
	feedbackEmbeddedFormat
)

type feedbackReport struct {
	start, end int
	notes      [][2]int
}

var (
	feedbackHeader    = regexp.MustCompile(`## Page Feedback:|^# .+ UI feedback$`)
	feedbackStandard  = regexp.MustCompile(`^### ([0-9]+)\. `)
	feedbackEmbedded  = regexp.MustCompile(`^## Annotation ([0-9]+)$`)
	feedbackCompact   = regexp.MustCompile(`^([0-9]+)\. \*\*.+?\*\*(?: \([^\n]*\))?: `)
	feedbackSelection = regexp.MustCompile(` \(re: "[^\n]*"\)$`)
	feedbackField     = regexp.MustCompile(`^(?:\*\*[^*\n]+:\*\*|- [A-Za-z][A-Za-z ]*: )`)
)

func pageFeedbackReports(text string, bodyStart int) []feedbackReport {
	if !strings.Contains(text, "Page Feedback:") && !strings.Contains(text, "UI feedback") {
		return nil
	}
	lines := textLines(text)
	var reports []feedbackReport
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(text[lines[i][0]:lines[i][1]])
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
		}
		header := feedbackHeader.FindStringIndex(line)
		if inFence || lines[i][0] < bodyStart || header == nil {
			continue
		}
		report := feedbackReport{start: lines[i][0] + strings.Index(text[lines[i][0]:lines[i][1]], line) + header[0], end: lines[i][1]}
		annotation, context := false, false
		format, number := feedbackUnknown, 0
		if strings.HasPrefix(line, "# ") {
			format = feedbackEmbeddedFormat
		}
		last := i
		for j := i + 1; j < len(lines); j++ {
			value := strings.TrimSpace(text[lines[j][0]:lines[j][1]])
			if feedbackHeader.MatchString(value) {
				break
			}
			isAnnotation := false
			nextFormat := format
			if format == feedbackUnknown {
				if feedbackStandard.MatchString(value) {
					nextFormat = feedbackStandardFormat
				} else if feedbackCompact.MatchString(value) {
					nextFormat = feedbackCompactFormat
				}
			}
			nextNumber, prefix := feedbackAnnotationNumber(value, nextFormat)
			if nextNumber > 0 {
				if nextNumber != number+1 || nextFormat == feedbackStandardFormat && !standardFeedbackContext(text, lines, j) {
					break
				}
				format, number, annotation, isAnnotation = nextFormat, nextNumber, true, true
			} else if headingLine(value) || feedbackCompact.MatchString(value) {
				break // another format or unrelated prose is outside this report
			}
			if isAnnotation && format == feedbackCompactFormat {
				context = true
				start := lines[j][0] + strings.Index(text[lines[j][0]:lines[j][1]], value) + prefix
				end, next := feedbackCommentEnd(text, lines, j, format, number)
				if selection := feedbackSelection.FindStringIndex(text[start:end]); selection != nil {
					end = start + selection[0]
				}
				report.notes = append(report.notes, [2]int{start, end})
				j = next
			} else if format == feedbackEmbeddedFormat && strings.HasPrefix(value, "- Feedback: ") {
				start := lines[j][0] + strings.Index(text[lines[j][0]:lines[j][1]], "- Feedback: ") + len("- Feedback: ")
				// Our exporter serializes the comment as a JSON string. Its
				// quotes and whitespace escapes are tooling, not human words.
				var decoded string
				end := lines[j][1] - len(text[start:lines[j][1]]) + len(strings.TrimRight(text[start:lines[j][1]], " \t\r"))
				if json.Unmarshal([]byte(text[start:end]), &decoded) == nil {
					report.notes = append(report.notes, feedbackJSONNotes(text, start+1, end-1)...)
				} else {
					report.notes = append(report.notes, [2]int{start, lines[j][1]})
				}
			} else if format == feedbackStandardFormat && strings.HasPrefix(value, "**Feedback:**") {
				start := lines[j][0] + strings.Index(text[lines[j][0]:lines[j][1]], "**Feedback:**") + len("**Feedback:**")
				end, next := feedbackCommentEnd(text, lines, j, format, number)
				report.notes = append(report.notes, [2]int{start, end})
				j = next
			} else if strings.HasPrefix(value, "**") && feedbackField.MatchString(value) {
				// Selected/nearby page text is also interpolated verbatim,
				// and can continue onto additional lines. It remains generated.
				_, j = feedbackCommentEnd(text, lines, j, feedbackUnknown, 0)
			}
			if strings.HasPrefix(value, "**Location:**") || strings.HasPrefix(value, "**Full DOM Path:**") || strings.HasPrefix(value, "- Selector:") {
				context = true
			}
			if value != "" && (isAnnotation || feedbackField.MatchString(value) || strings.HasPrefix(value, "Generated:") || strings.HasPrefix(value, "Please address each annotation") || value == "---" || value == "*Forensic data shown for first element of selection*") {
				report.end, last = lines[j][1], j
			} else if value != "" {
				break
			}
		}
		// Two independent signatures keep ordinary discussion of feedback
		// from being classified as a generated report.
		if annotation && context {
			reports = append(reports, report)
			i = last
		}
	}
	return reports
}

// Annotation numbers must continue in the format established by the first
// annotation. A numbered heading or bold list in surrounding prose is not a
// compact annotation in a standard report.
func feedbackAnnotationNumber(value string, format feedbackFormat) (int, int) {
	var pattern *regexp.Regexp
	switch format {
	case feedbackStandardFormat:
		pattern = feedbackStandard
	case feedbackCompactFormat:
		pattern = feedbackCompact
	case feedbackEmbeddedFormat:
		pattern = feedbackEmbedded
	default:
		return 0, 0
	}
	match := pattern.FindStringSubmatchIndex(value)
	if match == nil {
		return 0, 0
	}
	number, _ := strconv.Atoi(value[match[2]:match[3]])
	return number, match[1]
}

// Standard, detailed and forensic annotations identify the element before the
// comment. Require that context for each heading, including look-ahead, so a
// pasted plan numbered in sequence cannot extend a feedback comment.
func standardFeedbackContext(text string, lines [][2]int, index int) bool {
	context := false
	for j := index + 1; j < len(lines); j++ {
		value := strings.TrimSpace(text[lines[j][0]:lines[j][1]])
		if headingLine(value) || feedbackCompact.MatchString(value) {
			break
		}
		if strings.HasPrefix(value, "**Location:**") || strings.HasPrefix(value, "**Full DOM Path:**") {
			context = true
		}
		if strings.HasPrefix(value, "**Feedback:**") {
			return context
		}
	}
	return false
}

// Agentation emits comments last. Only the next annotation in the same format
// and sequence delimits an interior comment across blank paragraphs. Otherwise
// the first blank line, field, or heading ends the final comment.
func feedbackCommentEnd(text string, lines [][2]int, index int, format feedbackFormat, number int) (int, int) {
	if format == feedbackStandardFormat || format == feedbackCompactFormat {
		for j := index + 1; j < len(lines); j++ {
			value := strings.TrimSpace(text[lines[j][0]:lines[j][1]])
			next, _ := feedbackAnnotationNumber(value, format)
			if next == number+1 && (format == feedbackCompactFormat || standardFeedbackContext(text, lines, j)) {
				last := j - 1
				for last > index && strings.TrimSpace(text[lines[last][0]:lines[last][1]]) == "" {
					last--
				}
				return feedbackLineEnd(text, lines[last]), last
			}
			if headingLine(value) || feedbackCompact.MatchString(value) || feedbackHeader.MatchString(value) || feedbackField.MatchString(value) {
				break
			}
		}
	}
	last := index
	for j := index + 1; j < len(lines); j++ {
		value := strings.TrimSpace(text[lines[j][0]:lines[j][1]])
		if value == "" || feedbackField.MatchString(value) || headingLine(value) || feedbackCompact.MatchString(value) {
			break
		}
		last = j
	}
	return feedbackLineEnd(text, lines[last]), last
}

// textLines excludes LF but retains CR. Exclude CR from a field's value too,
// so anchored compact selected-text suffixes match both LF and CRLF exports.
func feedbackLineEnd(text string, line [2]int) int {
	end := line[1]
	if end > line[0] && text[end-1] == '\r' {
		end--
	}
	return end
}

func feedbackJSONNotes(text string, start, end int) [][2]int {
	var notes [][2]int
	part := start
	for i := start; i < end; i++ {
		if text[i] != '\\' || i+1 >= end {
			continue
		}
		next := i + 2
		whitespace := strings.ContainsRune("nrt", rune(text[i+1]))
		if text[i+1] == 'u' && i+6 <= end {
			next = i + 6
			var decoded string
			if json.Unmarshal([]byte(`"`+text[i:next]+`"`), &decoded) == nil {
				for _, r := range decoded {
					whitespace = unicode.IsSpace(r)
				}
			}
		}
		if whitespace {
			if part < i {
				notes = append(notes, [2]int{part, i})
			}
			part = next
		}
		i = next - 1
	}
	if part < end {
		notes = append(notes, [2]int{part, end})
	}
	return notes
}

// Claim only generated parts now. The comment ranges stay available to the
// copied/re-sent checks, then get protected from general formatting heuristics.
func claimFeedbackMetadata(l *labeler, reports []feedbackReport) {
	for _, report := range reports {
		start := report.start
		for _, note := range report.notes {
			l.claim(start, note[0], spanHarness, reasonPageFeedback, textOrigin{})
			start = note[1]
		}
		l.claim(start, report.end, spanHarness, reasonPageFeedback, textOrigin{})
	}
}

// maskFeedback blanks generated fields for copy comparisons, or the entire
// report for formatting heuristics. It preserves byte offsets and newlines.
func maskFeedback(text string, reports []feedbackReport, includeComments bool) string {
	if len(reports) == 0 {
		return text
	}
	masked := []byte(text)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			if masked[i] != '\n' && masked[i] != '\r' {
				masked[i] = ' '
			}
		}
	}
	for _, report := range reports {
		if includeComments {
			blank(report.start, report.end)
			continue
		}
		start := report.start
		for _, note := range report.notes {
			blank(start, note[0])
			start = note[1]
		}
		blank(start, report.end)
	}
	return string(masked)
}
