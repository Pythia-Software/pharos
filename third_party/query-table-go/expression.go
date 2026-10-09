package querytable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const ServerExpressionProfile = "qt-postgres-v1"
const MaxExpressionBytes = 10000
const MaxExpressionNodes = 512

// PlanDiagnostic is a machine-readable capability or validation failure. Offset
// is a UTF-8 byte offset into canonical source, not a browser UTF-16 position.
type PlanDiagnostic struct {
	Code, Message, Field string
	Offset               int
}

func (d *PlanDiagnostic) Error() string     { return d.Code + ": " + d.Message }
func diagnostic(code, message string) error { return &PlanDiagnostic{Code: code, Message: message} }

type exprNode struct {
	op, text string
	value    any
	args     []*exprNode
	pos      int
}
type exprToken struct {
	kind, text string
	pos        int
}
type exprParser struct {
	tokens           []exprToken
	at, depth, nodes int
}

func letter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
func digit(c byte) bool  { return c >= '0' && c <= '9' }
func parseExpression(source string) (*exprNode, error) {
	if !utf8.ValidString(source) {
		return nil, diagnostic("encoding", "source must be valid UTF-8")
	}
	if len(source) == 0 || len(source) > MaxExpressionBytes {
		return nil, diagnostic("source_limit", "expression must contain 1–10000 bytes")
	}
	ts := []exprToken{}
	for i := 0; i < len(source); {
		c := source[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			i++
			continue
		}
		start := i
		t := exprToken{pos: i}
		switch {
		case c == '[':
			i++
			var b strings.Builder
			closed := false
			for i < len(source) {
				if source[i] == ']' {
					i++
					if i < len(source) && source[i] == ']' {
						b.WriteByte(']')
						i++
						continue
					}
					closed = true
					break
				}
				b.WriteByte(source[i])
				i++
			}
			if !closed {
				return nil, diagnostic("syntax", "unclosed field reference")
			}
			t.kind = "field"
			t.text = b.String()
		case c == '"':
			i++
			for i < len(source) && source[i] != '"' {
				if source[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(source) {
				return nil, diagnostic("syntax", "unclosed string")
			}
			i++
			if err := json.Unmarshal([]byte(source[start:i]), &t.text); err != nil {
				return nil, diagnostic("syntax", "invalid JSON string")
			}
			if strings.ContainsRune(t.text, 0) {
				return nil, diagnostic("unsupported_literal", "PostgreSQL text cannot contain NUL")
			}
			if err := validJSONSurrogates(source[start:i]); err != nil {
				return nil, err
			}
			t.kind = "string"
		case digit(c) || c == '.':
			for i < len(source) && digit(source[i]) {
				i++
			}
			if i < len(source) && source[i] == '.' {
				i++
				for i < len(source) && digit(source[i]) {
					i++
				}
			}
			if i < len(source) && (source[i] == 'e' || source[i] == 'E') {
				i++
				if i < len(source) && (source[i] == '+' || source[i] == '-') {
					i++
				}
				for i < len(source) && digit(source[i]) {
					i++
				}
			}
			t.kind = "number"
			t.text = source[start:i]
		case letter(c):
			i++
			for i < len(source) && (letter(source[i]) || digit(source[i])) {
				i++
			}
			t.kind = "word"
			t.text = strings.ToUpper(source[start:i])
		default:
			if !strings.ContainsRune("+-*/=<>!(),", rune(c)) {
				return nil, diagnostic("unsupported_syntax", fmt.Sprintf("unexpected character at %d", i))
			}
			i++
			if i < len(source) && ((source[i] == '=' && strings.ContainsRune("<>!", rune(c))) || (c == '<' && source[i] == '>')) {
				i++
			}
			t.kind = "op"
			t.text = source[start:i]
		}
		ts = append(ts, t)
		if len(ts) > 2000 {
			return nil, diagnostic("complexity_limit", "too many tokens")
		}
	}
	ts = append(ts, exprToken{kind: "end", pos: len(source)})
	p := exprParser{tokens: ts}
	n, e := p.parse(0)
	if e == nil && p.peek().kind != "end" {
		e = diagnostic("syntax", "unexpected trailing token")
	}
	return n, e
}
func (p *exprParser) peek() exprToken { return p.tokens[p.at] }
func (p *exprParser) take() exprToken {
	t := p.peek()
	if t.kind != "end" {
		p.at++
	}
	return t
}
func (p *exprParser) expect(s string) error {
	t := p.take()
	if t.kind != "op" || t.text != s {
		return diagnostic("syntax", "expected "+s)
	}
	return nil
}
func priority(s string) int {
	switch s {
	case "OR":
		return 1
	case "AND":
		return 2
	case "=", "!=", "<>", "<", ">", "<=", ">=":
		return 3
	case "+", "-":
		return 4
	case "*", "/":
		return 5
	}
	return -1
}
func (p *exprParser) parse(min int) (*exprNode, error) {
	p.depth++
	defer func() { p.depth-- }()
	p.nodes++
	if p.depth > 50 || p.nodes > MaxExpressionNodes {
		return nil, diagnostic("complexity_limit", "expression depth/node budget exceeded")
	}
	t := p.take()
	n := &exprNode{pos: t.pos, text: t.text}
	switch {
	case t.kind == "number":
		v, e := strconv.ParseFloat(t.text, 64)
		if e != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, diagnostic("syntax", "number must be finite")
		}
		n.op = "literal"
		n.value = v
	case t.kind == "string":
		n.op = "literal"
		n.value = t.text
	case t.kind == "field":
		n.op = "field"
	case t.kind == "word" && (t.text == "TRUE" || t.text == "FALSE" || t.text == "NULL"):
		n.op = "literal"
		if t.text != "NULL" {
			n.value = t.text == "TRUE"
		}
	case t.kind == "op" && t.text == "(":
		var e error
		n, e = p.parse(0)
		if e != nil {
			return nil, e
		}
		if e = p.expect(")"); e != nil {
			return nil, e
		}
	case (t.kind == "word" && t.text == "NOT") || (t.kind == "op" && (t.text == "-" || t.text == "+")):
		prec := 6
		if t.text == "NOT" {
			prec = 3
		}
		a, e := p.parse(prec)
		if e != nil {
			return nil, e
		}
		n.op = "unary" + t.text
		n.args = []*exprNode{a}
	case t.kind == "word":
		n.op = t.text
		if e := p.expect("("); e != nil {
			return nil, e
		}
		if p.peek().kind != "op" || p.peek().text != ")" {
			for {
				a, e := p.parse(0)
				if e != nil {
					return nil, e
				}
				n.args = append(n.args, a)
				if p.peek().kind != "op" || p.peek().text != "," {
					break
				}
				p.take()
			}
		}
		if e := p.expect(")"); e != nil {
			return nil, e
		}
	default:
		return nil, diagnostic("syntax", "expected value, field or function")
	}
	for (p.peek().kind == "op" || (p.peek().kind == "word" && (p.peek().text == "AND" || p.peek().text == "OR"))) && priority(p.peek().text) >= min {
		op := p.take()
		right, e := p.parse(priority(op.text) + 1)
		if e != nil {
			return nil, e
		}
		p.nodes++
		if p.nodes > MaxExpressionNodes {
			return nil, diagnostic("complexity_limit", "node budget exceeded")
		}
		n = &exprNode{op: op.text, args: []*exprNode{n, right}, pos: op.pos}
	}
	return n, nil
}

// encoding/json replaces unpaired UTF-16 surrogates. Decline them explicitly so
// canonical source never silently changes meaning between JS and Go.
func validJSONSurrogates(raw string) error {
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		v, e := strconv.ParseUint(raw[i+1:i+5], 16, 16)
		if e != nil {
			return diagnostic("syntax", "invalid Unicode escape")
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return diagnostic("unsupported_literal", "unpaired Unicode surrogate")
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return diagnostic("unsupported_literal", "unpaired Unicode surrogate")
			}
			low, e := strconv.ParseUint(raw[i+3:i+7], 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return diagnostic("unsupported_literal", "unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}

func expressionKey(n *exprNode) string {
	parts := []any{n.op, n.text, n.value}
	for _, a := range n.args {
		parts = append(parts, expressionKey(a))
	}
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
