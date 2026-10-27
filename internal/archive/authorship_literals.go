package archive

import (
	"encoding/json"
	"strings"
)

type authorshipToolCall struct {
	name  string
	input any
}

// authorshipToolCalls unwraps literal calls in code-mode envelopes. This is
// static recovery, never JavaScript execution; computed values are excluded.
func authorshipToolCalls(name string, input any) []authorshipToolCall {
	var calls []authorshipToolCall
	var add func(string, any, int)
	add = func(name string, input any, depth int) {
		if depth > 8 {
			return
		}
		calls = append(calls, authorshipToolCall{name, input})
		if script, ok := input.(string); ok && authorshipToolName(name) == "exec" {
			for _, call := range literalAuthorshipCalls(script) {
				add(call.name, call.input, depth+1)
			}
		}
	}
	add(name, input, 0)
	return calls
}

// The small literal reader accepts strings, arrays, ordinary object literals,
// JSON scalars, and immutable string bindings. It deliberately rejects calls,
// interpolation, spread, getters, and computed object keys.
type authorshipLiteralReader struct {
	text     string
	at       int
	bindings map[string]string
}

func (r *authorshipLiteralReader) space() {
	for r.at < len(r.text) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(r.text[r.at])):
			r.at++
		case strings.HasPrefix(r.text[r.at:], "//"):
			end := strings.IndexByte(r.text[r.at:], '\n')
			if end < 0 {
				r.at = len(r.text)
			} else {
				r.at += end
			}
		case strings.HasPrefix(r.text[r.at:], "/*"):
			end := strings.Index(r.text[r.at+2:], "*/")
			if end < 0 {
				r.at = len(r.text)
			} else {
				r.at += end + 4
			}
		default:
			return
		}
	}
}

func (r *authorshipLiteralReader) take(char byte) bool {
	r.space()
	if r.at < len(r.text) && r.text[r.at] == char {
		r.at++
		return true
	}
	return false
}

func authorshipJSIdentifier(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' || char == '$' || char >= '0' && char <= '9'
}

func (r *authorshipLiteralReader) identifier() string {
	r.space()
	start := r.at
	for r.at < len(r.text) && authorshipJSIdentifier(r.text[r.at]) {
		r.at++
	}
	return r.text[start:r.at]
}

func (r *authorshipLiteralReader) quoted() (string, bool) {
	quote := r.text[r.at]
	r.at++
	var encoded strings.Builder
	valid := true
	for r.at < len(r.text) {
		char := r.text[r.at]
		r.at++
		if char == quote {
			var value string
			err := json.Unmarshal([]byte(`"`+encoded.String()+`"`), &value)
			return value, valid && err == nil
		}
		if quote == '`' && char == '$' && r.at < len(r.text) && r.text[r.at] == '{' {
			// A template expression may itself contain quotes/templates. Stop
			// recovery conservatively instead of reading its contents as calls.
			r.at = len(r.text)
			return "", false
		}
		if char == '\\' && r.at < len(r.text) {
			escaped := r.text[r.at]
			r.at++
			switch escaped {
			case '\n':
				continue
			case '\r':
				if r.at < len(r.text) && r.text[r.at] == '\n' {
					r.at++
				}
				continue
			case '\'', '`':
				encoded.WriteByte(escaped)
			case 'x':
				if r.at+2 > len(r.text) {
					return "", false
				}
				encoded.WriteString(`\u00` + r.text[r.at:r.at+2])
				r.at += 2
			case 'v':
				encoded.WriteString(`\u000b`)
			default:
				encoded.WriteByte('\\')
				encoded.WriteByte(escaped)
			}
			continue
		}
		switch char {
		case '"':
			encoded.WriteString(`\"`)
		case '\n':
			valid = valid && quote == '`'
			encoded.WriteString(`\n`)
		case '\r':
			valid = valid && quote == '`'
			encoded.WriteString(`\r`)
		case '\t':
			encoded.WriteString(`\t`)
		default:
			encoded.WriteByte(char)
		}
	}
	return "", false
}

func (r *authorshipLiteralReader) value(depth int) (any, bool) {
	r.space()
	if depth > 16 || r.at == len(r.text) {
		return nil, false
	}
	switch r.text[r.at] {
	case '\'', '"', '`':
		return r.quoted()
	case '{':
		r.at++
		value := map[string]any{}
		if r.take('}') {
			return value, true
		}
		for {
			r.space()
			var key string
			if r.at < len(r.text) && (r.text[r.at] == '\'' || r.text[r.at] == '"') {
				var ok bool
				key, ok = r.quoted()
				if !ok {
					return nil, false
				}
			} else {
				key = r.identifier()
			}
			if key == "" {
				return nil, false
			}
			if r.take(':') {
				item, ok := r.value(depth + 1)
				if !ok {
					return nil, false
				}
				value[key] = item
			} else if item, ok := r.bindings[key]; ok {
				value[key] = item
			} else {
				return nil, false
			}
			if r.take('}') {
				return value, true
			}
			if !r.take(',') {
				return nil, false
			}
			if r.take('}') {
				return value, true
			}
		}
	case '[':
		r.at++
		value := []any{}
		if r.take(']') {
			return value, true
		}
		for {
			item, ok := r.value(depth + 1)
			if !ok {
				return nil, false
			}
			value = append(value, item)
			if r.take(']') {
				return value, true
			}
			if !r.take(',') {
				return nil, false
			}
			if r.take(']') {
				return value, true
			}
		}
	default:
		start := r.at
		for r.at < len(r.text) && (authorshipJSIdentifier(r.text[r.at]) || strings.ContainsRune(".+-", rune(r.text[r.at]))) {
			r.at++
		}
		literal := r.text[start:r.at]
		if value, ok := r.bindings[literal]; ok {
			return value, true
		}
		var value any
		err := json.Unmarshal([]byte(literal), &value)
		return value, err == nil
	}
}

func literalAuthorshipCalls(script string) []authorshipToolCall {
	r := authorshipLiteralReader{text: script, bindings: map[string]string{}}
	var calls []authorshipToolCall
	for r.at < len(script) {
		r.space()
		if r.at == len(script) {
			break
		}
		switch script[r.at] {
		case '\'', '"', '`':
			r.quoted() // Calls mentioned inside strings are data.
			continue
		case '/':
			// Skip regex literals too. Ambiguous division expressions are left
			// unsupported rather than treating regex contents as tool calls.
			r.at++
			inClass := false
			for r.at < len(script) && script[r.at] != '\n' {
				char := script[r.at]
				r.at++
				if char == '\\' && r.at < len(script) {
					r.at++
				} else if char == '[' {
					inClass = true
				} else if char == ']' {
					inClass = false
				} else if char == '/' && !inClass {
					break
				}
			}
			continue
		}
		if !authorshipJSIdentifier(script[r.at]) {
			r.at++
			continue
		}
		name := r.identifier()
		if name == "const" {
			binding := r.identifier()
			if r.take('=') {
				value, ok := r.value(0)
				end := r.at
				r.space()
				terminated := r.at == len(script) || script[r.at] == ';' ||
					strings.ContainsAny(script[end:r.at], "\r\n") && !strings.ContainsRune("+.[(?", rune(script[r.at]))
				if value, isString := value.(string); ok && isString && terminated {
					r.bindings[binding] = value
				}
			}
		} else if name == "tools" && r.take('.') {
			tool := r.identifier()
			if tool != "" && r.take('(') {
				input, ok := r.value(0)
				if ok && r.take(')') {
					calls = append(calls, authorshipToolCall{tool, input})
				}
			}
		}
	}
	return calls
}
