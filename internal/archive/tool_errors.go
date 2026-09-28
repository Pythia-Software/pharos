package archive

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	toolErrorTag       = regexp.MustCompile(`</?(?:tool_use_error|error)>`)
	toolWrapperLine    = regexp.MustCompile(`^(?:Chunk ID:|Wall time:?|Process exited with code |Original token count:|Output:|Script (?:completed|failed|running with cell ID)|Exit code:? [0-9]+$|<persisted-output>)`)
	toolExceptionLine  = regexp.MustCompile(`^\w[\w.]*(?:Error|Exception):`)
	toolFileLine       = regexp.MustCompile(`^\S+\.(?:go|rs|ts|py|js):\d+(?::\d+)?: `)
	toolFailureLine    = regexp.MustCompile(`^(?:panic:|--- FAIL: \S+|FAIL\t|(?:error|Error|ERROR|fatal)[:\[]|E  |FAILED |usage:)|command not found|No such file or directory`)
	toolTestMarker     = regexp.MustCompile(`(?m)(?:--- FAIL:|^FAIL\t|test result: FAILED|^FAILED |={2,} FAILURES ={2,}|\b[1-9]\d* failed\b|Tests:.*[1-9]\d* failed|\*\* TEST FAILED \*\*)`)
	toolTestSubcommand = regexp.MustCompile(`(?i)(^|[\s:/_-])tests?($|[\s:/_-])`)
	toolCodecNumber    = regexp.MustCompile(`(?i)utf-\d+`)
	toolHexByte        = regexp.MustCompile(`0x[0-9a-fA-F]{4,}`)
	toolNoiseLine      = regexp.MustCompile(`^(?:[\W_]*|[~/.]\S*|at \S.*|node:internal/\S*|\^+|~+)$`)
	toolLabelLine      = regexp.MustCompile(`^\S+(?: \S+){0,2}:$`)
	toolLineNumber     = regexp.MustCompile(`(?i)(\(eval\)|zsh|bash|sh):\d+:|:[0-9]+(?::[0-9]+)?:`)
)

func unwrapToolOutput(content string) string {
	content = toolErrorTag.ReplaceAllString(content, "")
	// Some result envelopes were retained as JSON rather than flattened text.
	if strings.HasPrefix(strings.TrimSpace(content), "{") {
		var value map[string]any
		if json.Unmarshal([]byte(content), &value) == nil {
			if text, _ := toolResultText(value["content"]); text != "" {
				content = text
			}
		}
	}
	lines := []string{}
	for _, line := range strings.Split(content, "\n") {
		if !toolWrapperLine.MatchString(strings.TrimSpace(line)) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func toolErrorSignature(content string) string {
	content = unwrapToolOutput(content)
	if len(content) > 32768 {
		content = content[:16384] + "\n" + content[len(content)-16384:]
	}
	lines := strings.Split(content, "\n")
	// Lines that name no cause: bare punctuation such as a JSON "{", a lone
	// path, JavaScript stack frames and carets, and Node's rethrow lines.
	choose := func(line string) bool {
		line = strings.TrimSpace(line)
		return line != "" && !toolNoiseLine.MatchString(line) && !strings.Contains(line, "const err = new Error(message)") &&
			!strings.Contains(line, "triggerUncaughtException(") && !toolWrapperLine.MatchString(line)
	}
	selected := -1
	for index, line := range lines {
		if toolExceptionLine.MatchString(strings.TrimSpace(line)) && choose(line) {
			selected = index
		}
	}
	if selected < 0 {
		for _, pattern := range []*regexp.Regexp{regexp.MustCompile(`^(?:panic:|--- FAIL: \S+|FAIL\t)`), toolFileLine, toolFailureLine} {
			for index, line := range lines {
				if pattern.MatchString(strings.TrimSpace(line)) && choose(line) {
					selected = index
					break
				}
			}
			if selected >= 0 {
				break
			}
		}
	}
	if selected < 0 {
		for index, line := range lines {
			if choose(line) {
				selected = index
				break
			}
		}
	}
	if selected < 0 {
		return ""
	}
	// A short label such as "usage:" or "Validation failed:" names its cause on
	// the next line.
	chosen := strings.TrimSpace(lines[selected])
	if toolLabelLine.MatchString(chosen) {
		for _, line := range lines[selected+1:] {
			if choose(line) {
				chosen += " " + strings.TrimSpace(line)
				break
			}
		}
	}
	chosen = toolLineNumber.ReplaceAllStringFunc(chosen, func(s string) string {
		if strings.HasSuffix(s, ":") {
			return strings.Split(s, ":")[0] + ":<n>:"
		}
		return s
	})
	chosen = toolCodecNumber.ReplaceAllString(chosen, "utf-<n>")
	chosen = toolHexByte.ReplaceAllString(chosen, "0x<id>")
	return tl1Signature(strings.TrimSpace(chosen))
}

func toolTestFailure(call *toolCall, content string) bool {
	test := call.CommandCategory == "test"
	for _, command := range call.Commands {
		if command.Category == "test" || toolTestSubcommand.MatchString(command.Subcommand) {
			test = true
			break
		}
	}
	if !test {
		return false
	}
	return call.Status == "error" && call.ExitCode != nil && *call.ExitCode != 0 || toolTestMarker.MatchString(content)
}
