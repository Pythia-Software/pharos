package archive

import (
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var (
	reasonAuthoredTool = reasonTemplate("Matches agent-written content", "Matches agent-written content from ", " earlier")
	reasonQuestionForm = fixedReason("Generated question form")
	authoredHeredoc    = regexp.MustCompile(`<<(-?)[ \t]*('([^'\r\n]+)'|"([^"\r\n]+)"|([A-Za-z_][A-Za-z0-9_]*))`)
)

type authorshipQuestion struct {
	title   string
	options []string
	origin  textOrigin
}

// questionAnswer is a prewritten option a message submitted to a question
// recognized in its conversation: the byte ranges of the question title and
// the answer line, the option as offered, and the tool call that asked it.
// Classification does not store it; labeling records it as a human action.
type questionAnswer struct {
	Title, Option        string
	TitleStart, TitleEnd int
	Start, End           int
	Origin               textOrigin
}

func authorshipToolName(name string) string {
	name = strings.ToLower(name)
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// authoredToolText returns only newly authored payloads, not whole tool calls
// (which include paths, removed text, quoted context and read-only commands).
func authoredToolText(name string, input any) []string {
	var result []string
	for _, call := range authorshipToolCalls(name, input) {
		result = append(result, authoredDirectToolText(call.name, call.input)...)
	}
	return result
}

func authoredDirectToolText(name string, input any) []string {
	fields := mapValue(input)
	switch authorshipToolName(name) {
	case "write", "write_file", "create_file", "write_to_file":
		if fields == nil {
			return nil
		}
		if body := firstString(fields["content"], fields["contents"], fields["file_text"], fields["Content"]); body != "" {
			return []string{body}
		}
	case "edit", "edit_file", "str_replace_editor", "replace_file_content":
		if fields == nil {
			return nil
		}
		if body := firstString(fields["new_string"], fields["new_str"], fields["replacement"], fields["ReplacementContent"]); body != "" {
			return []string{body}
		}
	case "multiedit", "multi_replace_file_content":
		var result []string
		for _, key := range []string{"edits", "ReplacementChunks"} {
			edits, _ := fields[key].([]any)
			for _, edit := range edits {
				result = append(result, authoredToolText("edit", edit)...)
			}
		}
		return result
	case "apply_patch", "applypatch":
		patch := firstString(input)
		if fields != nil {
			patch = firstString(fields["patch"], fields["input"])
		}
		var result []string
		var added []string
		flush := func() {
			if len(added) > 0 {
				result = append(result, strings.Join(added, "\n"))
				added = nil
			}
		}
		for _, line := range strings.Split(patch, "\n") {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				added = append(added, line[1:])
			} else {
				flush()
			}
		}
		flush()
		return result
	case "exec", "exec_command", "run", "run_command", "bash", "shell", "terminal":
		if command := shellCommandText(input); command != "" {
			return authoredHeredocText(command)
		}
	}
	return nil
}

func authoredHeredocText(command string) []string {
	if !strings.Contains(command, "<<") {
		return nil
	}
	lines := strings.Split(command, "\n")
	var result []string
	for i := 0; i < len(lines); i++ {
		header := lines[i]
		matches := authoredHeredocInputs(header)
		if len(matches) == 0 {
			continue
		}
		// Only a literal file-writing command establishes authored file content.
		// A Python/shell script, SQL input, or read-only cat is not a document.
		for _, match := range matches {
			m := match.parts
			delimiter := firstString(m[3], m[4], m[5])
			start := i + 1
			end := start
			for end < len(lines) {
				line := lines[end]
				if m[1] == "-" {
					line = strings.TrimLeft(line, "\t")
				}
				if line == delimiter {
					break
				}
				end++
			}
			if end == len(lines) {
				return result
			} // incomplete literal: no provenance
			if match.writer {
				body := append([]string(nil), lines[start:end]...)
				if m[1] == "-" {
					for j := range body {
						body[j] = strings.TrimLeft(body[j], "\t")
					}
				}
				result = append(result, strings.Join(body, "\n"))
			}
			i = end
		}
	}
	return result
}

type authoredHeredocInput struct {
	parts  []string
	writer bool
}

// A heredoc belongs to its simple command, not a redirect or quoted operator
// elsewhere in a pipeline. Only stdout-to-file or tee with a file operand
// establishes a newly written document.
func authoredHeredocInputs(header string) []authoredHeredocInput {
	if !strings.Contains(header, "<<") {
		return nil
	}
	var inputs []authoredHeredocInput
	for _, segment := range parseShellCommand(header).Segments {
		text := segment.Text
		unquoted := authorshipShellSyntax(text)
		stdin := -1
		for i := 0; i < len(text); i++ {
			if text[i] == '<' && unquoted[i] && !(i > 0 && text[i-1] == '<' && unquoted[i-1]) && authorshipRedirectFD(text, i, unquoted, "0") == "0" {
				stdin = i
			}
		}
		words := authorshipShellArguments(text, unquoted)
		writer := false
		if len(words) > 0 && (words[0] == "cat" || words[0] == "tee") {
			writer = authorshipOutputFile(text, unquoted)
			if words[0] == "cat" {
				for _, word := range words[1:] {
					if word != "-" && word != "--" {
						writer = false // File operands bypass the heredoc input.
					}
				}
			} else {
				options := true
				for _, word := range words[1:] {
					if options && word == "--" {
						options = false
						continue
					}
					if (!options || !strings.HasPrefix(word, "-")) && authorshipFileDestination(word) {
						writer = true
					}
				}
			}
		}
		for _, loc := range authoredHeredoc.FindAllStringSubmatchIndex(text, -1) {
			if !unquoted[loc[0]] || loc[0] > 0 && text[loc[0]-1] == '<' || strings.HasPrefix(text[loc[0]:], "<<<") {
				continue
			}
			parts := make([]string, len(loc)/2)
			for i := range parts {
				if loc[2*i] >= 0 {
					parts[i] = text[loc[2*i]:loc[2*i+1]]
				}
			}
			inputs = append(inputs, authoredHeredocInput{parts, writer && loc[0] == stdin})
		}
	}
	return inputs
}

// Mark shell syntax outside quotes, escapes, comments and substitutions.
func authorshipShellSyntax(text string) []bool {
	syntax := make([]bool, len(text))
	var quote byte
	for i := 0; i < len(text); i++ {
		char := text[i]
		if quote != 0 {
			if quote == '"' && (char == '`' || strings.HasPrefix(text[i:], "$(") || strings.HasPrefix(text[i:], "${")) {
				if end := matchSubstitution(text, i); end > i {
					i = end - 1
					continue
				}
			}
			if char == '\\' && quote != '\'' {
				i++
			} else if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\\' {
			i++
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == '`' || strings.HasPrefix(text[i:], "$(") || strings.HasPrefix(text[i:], "${") {
			if end := matchSubstitution(text, i); end > i {
				i = end - 1
				continue
			}
		}
		if char == '#' && (i == 0 || strings.ContainsRune(" \t", rune(text[i-1]))) {
			break
		}
		syntax[i] = true
	}
	return syntax
}

func authorshipFileDestination(value string) bool {
	return value != "" && value != "-" && value != "/dev/null" && value != "/dev/stdout" && value != "/dev/stderr" &&
		!strings.HasPrefix(value, "/dev/fd/") && !strings.HasPrefix(value, "/proc/self/fd/")
}

// Remove redirection operators and their operands before reading argv; cat
// file operands and tee destinations may occur after the heredoc delimiter.
func authorshipShellArguments(text string, syntax []bool) []string {
	command := []byte(text)
	for i := 0; i < len(text); i++ {
		if !syntax[i] || text[i] != '<' && text[i] != '>' {
			continue
		}
		start := i
		for start > 0 && syntax[start-1] && text[start-1] >= '0' && text[start-1] <= '9' {
			start--
		}
		if start > 0 && !strings.ContainsRune(" \t", rune(text[start-1])) {
			start = i
		}
		if start == i && i > 0 && text[i-1] == '&' && syntax[i-1] {
			start--
		}
		for i+1 < len(text) && syntax[i+1] && strings.ContainsRune("<>|&", rune(text[i+1])) {
			i++
		}
		if text[i] == '<' && i+1 < len(text) && text[i+1] == '-' {
			i++
		}
		i++
		for i < len(text) && strings.ContainsRune(" \t", rune(text[i])) {
			i++
		}
		for i < len(text) && !(syntax[i] && strings.ContainsRune(" \t<>", rune(text[i]))) {
			i++
		}
		for j := start; j < i; j++ {
			command[j] = ' '
		}
		i--
	}
	return shellWords(string(command))
}

func authorshipRedirectFD(text string, at int, syntax []bool, fallback string) string {
	start := at
	for start > 0 && text[start-1] >= '0' && text[start-1] <= '9' && syntax[start-1] {
		start--
	}
	if start < at && (start == 0 || strings.ContainsRune(" \t", rune(text[start-1]))) {
		return text[start:at]
	}
	return fallback
}

func authorshipOutputFile(text string, syntax []bool) bool {
	writes := false
	for i := 0; i < len(text); i++ {
		if text[i] != '>' || !syntax[i] || i > 0 && text[i-1] == '>' && syntax[i-1] {
			continue
		}
		if authorshipRedirectFD(text, i, syntax, "1") != "1" {
			continue // Redirecting stderr does not write the heredoc body.
		}
		end := i + 1
		if end < len(text) && text[end] == '>' {
			end++
		}
		rest := strings.TrimLeft(text[end:], " \t")
		if strings.HasPrefix(rest, "&") {
			writes = false
			continue // Descriptor duplication is not a file destination.
		}
		words := shellWords(rest)
		writes = len(words) > 0 && authorshipFileDestination(words[0])
	}
	return writes
}

// addAgentText keeps report items with the same bounded source batch as its
// shingle sequence; eviction releases both together.
func (c *classifier) addAgentText(at time.Time, origin textOrigin, text string, tool bool) {
	digest := sha256.Sum256([]byte(text))
	prior := c.agentText.byText[digest]
	// Simultaneous native/Conductor mirrors carry identical prose and file
	// bodies. Keep one source, and share immutable hashes on later repeats.
	if prior != nil && at.Equal(prior.sentAt) {
		return
	}
	var hashes []uint64
	if prior != nil {
		hashes = prior.hashes
	} else {
		hashes = shingles(wordTokens(text))
	}
	if len(hashes) == 0 {
		return
	}
	c.agentText.add(at, origin, hashes)
	batch := c.agentText.queue[len(c.agentText.queue)-1]
	batch.tool, batch.digest = tool, digest
	c.agentText.byText[digest] = batch
	if prior != nil {
		batch.reviews, batch.headers = prior.reviews, prior.headers
		return
	}
	beforeItems := true
	for _, line := range textLines(text) {
		value := text[line[0]:line[1]]
		if key := reviewItemKey(value); key != "" {
			beforeItems = false
			if batch.reviews == nil {
				batch.reviews = map[string]bool{}
			}
			batch.reviews[key] = true
		} else if key := reviewHeaderKey(value); beforeItems && key != "" {
			if batch.headers == nil {
				batch.headers = map[string]bool{}
			}
			batch.headers[key] = true
		}
	}
}

func (c *classifier) addAuthorshipTool(at time.Time, origin textOrigin, text string) {
	var envelope map[string]any
	if json.Unmarshal([]byte(text), &envelope) != nil {
		return
	}
	name, input := firstString(envelope["tool"]), envelope["input"]
	for _, call := range authorshipToolCalls(name, input) {
		c.addAuthorshipCall(at, origin, call.name, call.input)
	}
}

func (c *classifier) addAuthorshipCall(at time.Time, origin textOrigin, name string, input any) {
	for _, body := range authoredDirectToolText(name, input) {
		c.addAgentText(at, origin, body, true)
	}
	switch authorshipToolName(name) {
	case "request_user_input", "request_user_input_async", "askuserquestion", "ask_question":
		fields := mapValue(input)
		questions, _ := fields["questions"].([]any)
		for _, raw := range questions {
			q := mapValue(raw)
			title := firstString(q["question"], q["title"])
			if title == "" {
				continue
			}
			question := authorshipQuestion{title: title, origin: origin}
			options, _ := q["options"].([]any)
			for _, raw := range options {
				option := firstString(raw)
				if f := mapValue(raw); f != nil {
					option = firstString(f["label"])
				}
				if option != "" {
					question.options = append(question.options, option)
				}
			}
			c.questions[origin.conversation] = append(c.questions[origin.conversation], question)
		}
	}
}

// A historical flattened answer is recognized only together with the exact
// question in this conversation. Option words alone ("Yes", "Search") never
// establish provenance. Free-text answers remain available to other rules.
func (c *classifier) claimQuestionForms(l *labeler, input authorInput, bodyStart int) {
	lines := textLines(input.Text)
	// One answer per answer line: questions are in asking order, so the most
	// recent matching one replaces an earlier one with the same title.
	answers := map[int]questionAnswer{}
	defer func() {
		for _, line := range lines {
			if answer, ok := answers[line[0]]; ok {
				c.answers = append(c.answers, answer)
			}
		}
	}()
	for _, q := range c.questions[input.ConversationID] {
		for i, line := range lines {
			if line[0] < bodyStart || strings.TrimSpace(input.Text[line[0]:line[1]]) != q.title || i+1 >= len(lines) {
				continue
			}
			answer := lines[i+1]
			value := strings.TrimSpace(input.Text[answer[0]:answer[1]])
			if value == "" {
				continue
			}
			l.claim(line[0], line[1], spanHarness, reasonQuestionForm, q.origin)
			generated := value == "(Skipped)"
			for _, option := range q.options {
				trim := func(s string) string {
					return strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(s, " (Recommended)"), " (recommended)"))
				}
				if trim(option) == trim(value) {
					generated = true
					answers[answer[0]] = questionAnswer{Title: q.title, Option: option, TitleStart: line[0], TitleEnd: line[1],
						Start: answer[0], End: answer[1], Origin: q.origin}
					break
				}
			}
			if generated {
				l.claim(answer[0], answer[1], spanHarness, reasonQuestionForm, q.origin)
			}
		}
	}
}

func maskGeneratedAuthorship(text string, l *labeler) string {
	masked := []byte(text)
	for i, owner := range l.owner {
		if owner >= 0 && l.details[owner].Category == spanHarness && masked[i] != '\n' && masked[i] != '\r' {
			masked[i] = ' '
		}
	}
	return string(masked)
}
