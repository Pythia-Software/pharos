package archive

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	toolErrorTag      = regexp.MustCompile(`</?(?:tool_use_error|error)>`)
	toolWrapperLine   = regexp.MustCompile(`^(?:Chunk ID:|Wall time:?|Process exited with code |Original token count:|Output:|Script (?:completed|failed|running with cell ID)|Exit code:? [0-9]+$|<persisted-output>)`)
	toolExceptionLine = regexp.MustCompile(`^\w[\w.]*(?:Error|Exception):`)
	toolFileLine      = regexp.MustCompile(`^\S+\.(?:go|rs|ts|py|js):\d+(?::\d+)?: `)
	toolFailureLine   = regexp.MustCompile(`^(?:panic:|--- FAIL: \S+|FAIL\t|(?:error|Error|ERROR|fatal)[:\[]|E  |FAILED |usage:)|command not found|No such file or directory`)
	toolTestMarker    = regexp.MustCompile(`(?m)(?:--- FAIL:|^FAIL\t|test result: FAILED|^FAILED |={2,} FAILURES ={2,}|\b[1-9]\d* failed\b|Tests:.*[1-9]\d* failed|\*\* TEST FAILED \*\*)`)
	toolCodecNumber   = regexp.MustCompile(`(?i)utf-\d+`)
	toolHexByte       = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	toolLineNumber    = regexp.MustCompile(`(?i)(\(eval\)|zsh|bash|sh):\d+:|:[0-9]+(?::[0-9]+)?:`)
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
	choose := func(line string) bool {
		line = strings.TrimSpace(line)
		return line != "" && !strings.Contains(line, "const err = new Error(message)") && !strings.Contains(line, "triggerUncaughtException(") && !toolWrapperLine.MatchString(line)
	}
	selected := ""
	for _, line := range lines {
		if toolExceptionLine.MatchString(strings.TrimSpace(line)) && choose(line) {
			selected = line
		}
	}
	if selected == "" {
		for _, pattern := range []*regexp.Regexp{regexp.MustCompile(`^(?:panic:|--- FAIL: \S+|FAIL\t)`), toolFileLine, toolFailureLine} {
			for _, line := range lines {
				if pattern.MatchString(strings.TrimSpace(line)) && choose(line) {
					selected = line
					break
				}
			}
			if selected != "" {
				break
			}
		}
	}
	if selected == "" {
		for _, line := range lines {
			if choose(line) {
				selected = line
				break
			}
		}
	}
	if selected == "" {
		return ""
	}
	selected = toolLineNumber.ReplaceAllStringFunc(selected, func(s string) string {
		if strings.HasSuffix(s, ":") {
			return strings.Split(s, ":")[0] + ":<n>:"
		}
		return s
	})
	selected = toolCodecNumber.ReplaceAllString(selected, "utf-<n>")
	selected = toolHexByte.ReplaceAllString(selected, "0x<id>")
	return tl1Signature(strings.TrimSpace(selected))
}

func toolTestFailure(call *toolCall, content string) bool {
	test := call.CommandCategory == "test"
	for _, command := range call.Commands {
		if command.Category == "test" || strings.Contains(strings.ToLower(command.Subcommand), "test") {
			test = true
			break
		}
	}
	if !test {
		return false
	}
	return call.Status == "error" && call.ExitCode != nil && *call.ExitCode != 0 || toolTestMarker.MatchString(content)
}
