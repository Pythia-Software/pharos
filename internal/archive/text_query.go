package archive

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// textQuery is deliberately smaller than raw FTS syntax. No user input becomes
// a SQL identifier. Operators are uppercase; adjacent atoms mean AND.
type textQuery struct {
	Op         string       `json:"op"`
	Text       string       `json:"text,omitempty"`
	Distance   int          `json:"distance,omitempty"`
	Children   []*textQuery `json:"children,omitempty"`
	expansions []string
	patterns   map[uint8]*regexp.Regexp
}

// Cached search populations are shared between page requests. Verification
// lazily compiles patterns, so each page needs its own writable pattern maps.
func (n *textQuery) verificationCopy() *textQuery {
	if n == nil {
		return nil
	}
	copy := *n
	copy.patterns = maps.Clone(n.patterns)
	copy.Children = make([]*textQuery, len(n.Children))
	for i, child := range n.Children {
		copy.Children[i] = child.verificationCopy()
	}
	return &copy
}

type textLexeme struct {
	text   string
	at     int
	quoted bool
}
type textParser struct {
	tokens []textLexeme
	pos    int
}

func queryError(at int, message string) error {
	return fmt.Errorf("search at byte %d: %s", at+1, message)
}

func parseTextQuery(query, mode string) (*textQuery, error) {
	if len(query) > 2000 {
		return nil, fmt.Errorf("search query exceeds 2,000 bytes")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if mode == "literal" || mode == "regex" {
		return &textQuery{Op: mode, Text: query}, nil
	}
	if mode == "phrase" {
		if len(auditQueryWords(query)) == 0 {
			return nil, fmt.Errorf("phrase needs a searchable word")
		}
		return &textQuery{Op: "phrase", Text: query}, nil
	}
	if mode != "terms" && mode != "advanced" {
		return nil, fmt.Errorf("mode must be terms, advanced, phrase, literal, or regex")
	}
	var tokens []textLexeme
	for i := 0; i < len(query); {
		r, n := utf8.DecodeRuneInString(query[i:])
		if unicode.IsSpace(r) {
			i += n
			continue
		}
		start := i
		if query[i] == '"' {
			i++
			var b strings.Builder
			closed := false
			for i < len(query) {
				if query[i] == '"' {
					i++
					closed = true
					break
				}
				if query[i] == '\\' && i+1 < len(query) && (query[i+1] == '"' || query[i+1] == '\\') {
					i++
				}
				b.WriteByte(query[i])
				i++
			}
			if !closed {
				return nil, queryError(start, "unclosed quote")
			}
			if len(auditQueryWords(b.String())) == 0 {
				return nil, queryError(start, "phrase needs a word; use literal mode for punctuation")
			}
			tokens = append(tokens, textLexeme{b.String(), start, true})
		} else if mode == "advanced" && (query[i] == '(' || query[i] == ')') {
			tokens = append(tokens, textLexeme{query[i : i+1], i, false})
			i++
		} else {
			for i < len(query) {
				r, n = utf8.DecodeRuneInString(query[i:])
				if unicode.IsSpace(r) || query[i] == '"' || (mode == "advanced" && (query[i] == '(' || query[i] == ')')) {
					break
				}
				i += n
			}
			tokens = append(tokens, textLexeme{query[start:i], start, false})
		}
		if len(tokens) > 128 {
			return nil, queryError(start, "too many clauses (maximum 128)")
		}
	}
	if mode == "terms" {
		nodes := []*textQuery{}
		for _, t := range tokens {
			if len(auditQueryWords(t.text)) == 0 {
				continue
			}
			op := "term"
			if t.quoted {
				op = "phrase"
			}
			nodes = append(nodes, &textQuery{Op: op, Text: t.text})
		}
		if len(nodes) == 0 {
			return nil, fmt.Errorf("query needs searchable words; use literal mode for punctuation")
		}
		if len(nodes) == 1 {
			return nodes[0], nil
		}
		return &textQuery{Op: "and", Children: nodes}, nil
	}
	p := textParser{tokens: tokens}
	node, err := p.parseOr(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(tokens) {
		return nil, queryError(tokens[p.pos].at, "unexpected "+tokens[p.pos].text)
	}
	return node, nil
}
func (p *textParser) is(s string) bool {
	return p.pos < len(p.tokens) && !p.tokens[p.pos].quoted && p.tokens[p.pos].text == s
}
func (p *textParser) parseOr(depth int) (*textQuery, error) {
	left, err := p.parseAnd(depth)
	if err != nil {
		return nil, err
	}
	for p.is("OR") {
		p.pos++
		right, e := p.parseAnd(depth)
		if e != nil {
			return nil, e
		}
		left = combineTextQuery("or", left, right)
	}
	return left, nil
}
func (p *textParser) parseAnd(depth int) (*textQuery, error) {
	left, err := p.parseNear(depth)
	if err != nil {
		return nil, err
	}
	var excluded *textQuery
	for p.pos < len(p.tokens) && !p.is(")") && !p.is("OR") {
		op := "and"
		if p.is("AND") {
			p.pos++
		}
		if p.is("NOT") {
			op = "not"
			p.pos++
		}
		right, e := p.parseNear(depth)
		if e != nil {
			return nil, e
		}
		// AND and binary NOT share precedence. Collect flat exclusions so
		// a NOT b AND c NOT d becomes (a AND c) NOT (b OR d), not a deep tree.
		if op == "not" {
			if excluded == nil {
				excluded = right
			} else {
				excluded = combineTextQuery("or", excluded, right)
			}
		} else {
			left = combineTextQuery("and", left, right)
		}
	}
	if excluded != nil {
		left = combineTextQuery("not", left, excluded)
	}
	return left, nil
}

// Flat associative expressions should not consume the nesting budget. Keep
// NOT binary, folding only its left side: (a NOT b) NOT c is a NOT (b OR c).
// A NOT on the right (a NOT (b NOT c)) must retain its different meaning.
func combineTextQuery(op string, left, right *textQuery) *textQuery {
	if op == "not" && left.Op == "not" {
		return combineTextQuery("not", left.Children[0], combineTextQuery("or", left.Children[1], right))
	}
	children := []*textQuery{left, right}
	if op == "and" || op == "or" {
		children = nil
		for _, child := range []*textQuery{left, right} {
			if child.Op == op {
				children = append(children, child.Children...)
			} else {
				children = append(children, child)
			}
		}
	}
	return &textQuery{Op: op, Children: children}
}

func (p *textParser) parseNear(depth int) (*textQuery, error) {
	left, err := p.atom(depth)
	if err != nil {
		return nil, err
	}
	for p.pos < len(p.tokens) && !p.tokens[p.pos].quoted && (strings.HasPrefix(p.tokens[p.pos].text, "NEAR/") || strings.HasPrefix(p.tokens[p.pos].text, "PRE/")) {
		token := p.tokens[p.pos]
		p.pos++
		op := "near"
		if strings.HasPrefix(token.text, "PRE/") {
			op = "pre"
		}
		gap, e := strconv.Atoi(token.text[strings.IndexByte(token.text, '/')+1:])
		if e != nil || gap < 0 || gap > 100 {
			return nil, queryError(token.at, "NEAR distance must be 0–100")
		}
		right, e := p.atom(depth)
		if e != nil {
			return nil, e
		}
		if (left.Op != "term" && left.Op != "phrase" && left.Op != "prefix") || (right.Op != "term" && right.Op != "phrase" && right.Op != "prefix") {
			return nil, queryError(token.at, "NEAR requires two words or phrases")
		}
		left = &textQuery{Op: op, Distance: gap, Children: []*textQuery{left, right}}
	}
	return left, nil
}
func (p *textParser) atom(depth int) (*textQuery, error) {
	if depth > 16 {
		return nil, fmt.Errorf("search groups exceed depth 16")
	}
	if p.pos >= len(p.tokens) {
		return nil, fmt.Errorf("search ends with an operator")
	}
	t := p.tokens[p.pos]
	p.pos++
	if !t.quoted && t.text == "(" {
		node, e := p.parseOr(depth + 1)
		if e != nil {
			return nil, e
		}
		if !p.is(")") {
			return nil, queryError(t.at, "unclosed group")
		}
		p.pos++
		return node, nil
	}
	if !t.quoted && (t.text == "AND" || t.text == "OR" || t.text == "NOT" || t.text == ")" || strings.HasPrefix(t.text, "NEAR/") || strings.HasPrefix(t.text, "PRE/")) {
		return nil, queryError(t.at, "expected a word or phrase; NOT needs a positive left operand")
	}
	op := "term"
	word := t.text
	if t.quoted {
		op = "phrase"
		if p.is("*") {
			op = "prefix"
			p.pos++
		}
	} else if strings.HasSuffix(word, "*") {
		op = "prefix"
		word = strings.TrimSuffix(word, "*")
	}
	if !t.quoted && strings.ContainsAny(word, "*?") {
		return nil, queryError(t.at, "only a trailing * token prefix is supported; use regex mode for other wildcards")
	}
	if len(auditQueryWords(word)) == 0 {
		return nil, queryError(t.at, "expected searchable words; use literal mode for punctuation")
	}
	return &textQuery{Op: op, Text: word}, nil
}
func (n *textQuery) fts(positiveOnly bool) string {
	switch n.Op {
	case "term", "phrase", "prefix":
		parts := append([]string(nil), n.expansions...)
		if len(parts) == 0 {
			parts = []string{n.Text}
		}
		for i := range parts {
			parts[i] = quoteFTS(parts[i])
			if n.Op == "prefix" {
				parts[i] += "*"
			}
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	case "near", "pre":
		return fmt.Sprintf("NEAR(%s %s, %d)", n.Children[0].phraseFTS(), n.Children[1].phraseFTS(), n.Distance)
	case "and", "or", "not":
		if n.Op == "not" && (positiveOnly || n.needsOrderedVerifier()) {
			return n.Children[0].fts(true)
		}
		parts := []string{}
		for _, child := range n.Children {
			parts = append(parts, child.fts(positiveOnly))
		}
		return "(" + strings.Join(parts, " "+strings.ToUpper(n.Op)+" ") + ")"
	}
	return ""
}
func (n *textQuery) phraseFTS() string {
	q := quoteFTS(n.Text)
	if n.Op == "prefix" {
		q += "*"
	}
	return q
}
func (c *Catalog) expandTextQuery(ctx context.Context, n *textQuery, o TextSearchOptions, out map[string][]string) error {
	if n.Op == "term" && o.Fuzzy && len(libraryWords.FindAllString(n.Text, -1)) == 1 {
		terms, err := c.fuzzyTermsWith(ctx, n.Text, o.FuzzyPrefixLength, o.FuzzyMaxEdits, o.FuzzyMaxExpansions)
		if err != nil {
			return err
		}
		n.expansions = terms
		if len(terms) > 1 {
			out[n.Text] = append([]string(nil), terms...)
		}
	}
	// NEAR has precise token semantics; expanding either operand would change it.
	if n.Op != "near" && n.Op != "pre" {
		for _, child := range n.Children {
			if err := c.expandTextQuery(ctx, child, o, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// Strict verification applies only when the caller requests exact spelling /
// separators. Ordinary lexical matching delegates Unicode61 normalization to
// SQLite. Span searches use the same FTS tokenizer in an isolated small index.
func (n *textQuery) strictMatch(body string, caseSensitive, separators bool) (int, int, bool) {
	switch n.Op {
	case "and", "or", "not":
		start, end, ok := n.Children[0].strictMatch(body, caseSensitive, separators)
		if n.Op == "not" {
			if !ok {
				return start, end, false
			}
			_, _, no := n.Children[1].strictMatch(body, caseSensitive, separators)
			return start, end, ok && !no
		}
		for _, child := range n.Children[1:] {
			if (n.Op == "or" && ok) || (n.Op == "and" && !ok) {
				break
			}
			s, e, yes := child.strictMatch(body, caseSensitive, separators)
			if n.Op == "or" {
				if yes && !ok {
					start, end = s, e
				}
				ok = ok || yes
			} else {
				ok = ok && yes
			}
		}
		return start, end, ok
	case "near", "pre":
		a := n.Children[0].strictLocations(body, caseSensitive, separators, 0)
		b := n.Children[1].strictLocations(body, caseSensitive, separators, 0)
		for i, j := 0, 0; i < len(a) && j < len(b); {
			x, y := a[i], b[j]
			lo, hi := min(x[1], y[1]), max(x[0], y[0])
			gap := 0
			if hi > lo {
				gap = len(libraryWords.FindAllString(body[lo:hi], n.Distance+1))
			}
			if gap <= n.Distance && (n.Op != "pre" || y[0] >= x[1]) {
				return min(x[0], y[0]), max(x[1], y[1]), true
			}
			if n.Op == "pre" && y[0] < x[1] {
				j++
			} else if x[0] < y[0] {
				i++
			} else {
				j++
			}
		}
		return 0, 0, false
	default:
		loc := n.strictLocations(body, caseSensitive, separators, 1)
		if len(loc) == 0 {
			return 0, 0, false
		}
		return loc[0][0], loc[0][1], true
	}
}
func (n *textQuery) strictPattern(caseSensitive, separators bool) *regexp.Regexp {
	key := uint8(0)
	if caseSensitive {
		key |= 1
	}
	if separators {
		key |= 2
	}
	if re := n.patterns[key]; re != nil {
		return re
	}
	words := libraryWords.FindAllString(n.Text, -1)
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = regexp.QuoteMeta(w)
	}
	pattern := strings.Join(parts, `[^\pL\pN]+`)
	if n.Op == "phrase" && separators {
		pattern = regexp.QuoteMeta(n.Text)
	}
	if len(n.expansions) > 0 {
		alts := []string{}
		for _, term := range n.expansions {
			if !caseSensitive || sameCasing(term, n.Text) {
				alts = append(alts, regexp.QuoteMeta(term))
			}
		}
		if caseSensitive {
			alts = append(alts, regexp.QuoteMeta(n.Text))
		}
		// FindAll does not retry another alternative when our token-boundary
		// check rejects a shorter prefix (migration before migrations).
		slices.SortStableFunc(alts, func(a, b string) int { return len(b) - len(a) })
		pattern = "(?:" + strings.Join(alts, "|") + ")"
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	if pattern == "" {
		return nil
	}
	re := regexp.MustCompile(pattern)
	if n.patterns == nil {
		n.patterns = map[uint8]*regexp.Regexp{}
	}
	n.patterns[key] = re
	return re
}

func (n *textQuery) strictLocations(body string, caseSensitive, separators bool, limit int) [][2]int {
	re := n.strictPattern(caseSensitive, separators)
	if re == nil {
		return nil
	}
	result := [][2]int{}
	for offset := 0; offset < len(body); {
		m := re.FindStringIndex(body[offset:])
		if m == nil {
			break
		}
		m[0] += offset
		m[1] += offset
		offset = m[1]
		// Token phrases and words require token boundaries. A prefix omits only
		// the right boundary; punctuation-sensitive quoted strings stay verbatim.
		literal := n.Op == "phrase" && separators
		before, _ := utf8.DecodeLastRuneInString(body[:m[0]])
		after, _ := utf8.DecodeRuneInString(body[m[1]:])
		if !literal && ((m[0] > 0 && (unicode.IsLetter(before) || unicode.IsNumber(before))) || (n.Op != "prefix" && m[1] < len(body) && (unicode.IsLetter(after) || unicode.IsNumber(after)))) {
			continue
		}
		result = append(result, [2]int{m[0], m[1]})
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

// Reject only demonstrably impossible ASCII candidates. For non-ASCII text
// or query words, leave normalization to the exact unicode61 verifier. NOT
// supplies no negative pruning: its excluded text can live outside this span.
func (n *textQuery) asciiCandidate(body string) bool {
	for i := 0; i < len(body); i++ {
		if body[i] >= 128 {
			return true
		}
	}
	return n.asciiPositive(body)
}

func (n *textQuery) asciiPositive(body string) bool {
	switch n.Op {
	case "and", "near", "pre":
		for _, child := range n.Children {
			if !child.asciiPositive(body) {
				return false
			}
		}
		return true
	case "or":
		for _, child := range n.Children {
			if child.asciiPositive(body) {
				return true
			}
		}
		return false
	case "not":
		return n.Children[0].asciiPositive(body)
	default:
		for _, term := range append([]string{n.Text}, n.expansions...) {
			for i := 0; i < len(term); i++ {
				if term[i] >= 128 {
					return true
				}
			}
		}
		return len(n.strictLocations(body, false, false, 1)) > 0
	}
}
