package labeler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Tokenizer encodes text the way the HuggingFace tokenizers library (Rust,
// Python package 0.23.3) encodes it with add_special_tokens=False, for the
// tokenizer.json shape the labeler's model uses: an optional NFC normalizer,
// the GPT-2 ByteLevel pre-tokenizer and a byte-level BPE model, plus the
// added-token vocabulary. LoadTokenizer refuses any other configuration rather
// than encode it differently.
//
// Encoding follows the library step by step:
//
//  1. Added tokens with normalized=false are split out of the raw text.
//  2. Each remaining piece is normalized (NFC), then added tokens with
//     normalized=true are split out of it.
//  3. Each remaining piece is split by the GPT-2 regex (preTokenLen) and
//     every pre-token's UTF-8 bytes go through BPE (merge).
//
// A Tokenizer is safe for concurrent use.
type Tokenizer struct {
	vocabSize int
	nfc       bool

	// byteID is the vocabulary id of each single byte's symbol, or -1 when the
	// vocabulary lacks it (BPE without an unknown token drops such bytes).
	byteID [256]int32
	// merges maps a pair of ids (left<<32 | right) to its merge.
	merges map[uint64]bpeMerge
	// whole maps a pre-token's bytes to its id when ignore_merges is set.
	whole map[string]int32

	added    map[int32]addedToken
	rawTrie  *tokenTrie // added tokens matched in the raw text
	normTrie *tokenTrie // added tokens matched in the normalized text

	cache bpeCache
}

type bpeMerge struct {
	rank int32
	id   int32
}

type addedToken struct {
	Content    string `json:"content"`
	SingleWord bool   `json:"single_word"`
	LStrip     bool   `json:"lstrip"`
	RStrip     bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
	Special    bool   `json:"special"`
}

type tokenizerFile struct {
	Truncation   json.RawMessage `json:"truncation"`
	Padding      json.RawMessage `json:"padding"`
	AddedTokens  []addedTokenID  `json:"added_tokens"`
	Normalizer   json.RawMessage `json:"normalizer"`
	PreTokenizer json.RawMessage `json:"pre_tokenizer"`
	Model        struct {
		Type                    string           `json:"type"`
		Dropout                 *float64         `json:"dropout"`
		UnkToken                *string          `json:"unk_token"`
		ContinuingSubwordPrefix *string          `json:"continuing_subword_prefix"`
		EndOfWordSuffix         *string          `json:"end_of_word_suffix"`
		ByteFallback            bool             `json:"byte_fallback"`
		IgnoreMerges            bool             `json:"ignore_merges"`
		Vocab                   map[string]int32 `json:"vocab"`
		Merges                  json.RawMessage  `json:"merges"`
	} `json:"model"`
}

type addedTokenID struct {
	ID int32 `json:"id"`
	addedToken
}

func isNull(m json.RawMessage) bool {
	m = bytes.TrimSpace(m)
	return len(m) == 0 || string(m) == "null"
}

// LoadTokenizer parses a tokenizer.json.
func LoadTokenizer(data []byte) (*Tokenizer, error) {
	var f tokenizerFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("tokenizer: %w", err)
	}
	if !isNull(f.Truncation) || !isNull(f.Padding) {
		return nil, errors.New("tokenizer: truncation and padding are not supported")
	}
	t := &Tokenizer{}

	if !isNull(f.Normalizer) {
		var n struct{ Type string }
		if err := json.Unmarshal(f.Normalizer, &n); err != nil || n.Type != "NFC" {
			return nil, fmt.Errorf("tokenizer: unsupported normalizer %s", f.Normalizer)
		}
		t.nfc = true
	}

	var pre struct {
		Type           string `json:"type"`
		AddPrefixSpace bool   `json:"add_prefix_space"`
		UseRegex       *bool  `json:"use_regex"`
	}
	if err := json.Unmarshal(f.PreTokenizer, &pre); err != nil || pre.Type != "ByteLevel" ||
		pre.AddPrefixSpace || (pre.UseRegex != nil && !*pre.UseRegex) {
		return nil, fmt.Errorf("tokenizer: unsupported pre_tokenizer %s", f.PreTokenizer)
	}

	m := &f.Model
	switch {
	case m.Type != "BPE":
		return nil, fmt.Errorf("tokenizer: unsupported model %q", m.Type)
	case m.Dropout != nil && *m.Dropout != 0:
		return nil, errors.New("tokenizer: BPE dropout is not supported")
	case m.UnkToken != nil, m.ByteFallback:
		return nil, errors.New("tokenizer: BPE unk_token and byte_fallback are not supported")
	case m.ContinuingSubwordPrefix != nil && *m.ContinuingSubwordPrefix != "",
		m.EndOfWordSuffix != nil && *m.EndOfWordSuffix != "":
		return nil, errors.New("tokenizer: BPE subword prefixes and suffixes are not supported")
	case len(m.Vocab) == 0:
		return nil, errors.New("tokenizer: empty vocabulary")
	}
	for _, id := range m.Vocab {
		if id < 0 {
			return nil, fmt.Errorf("tokenizer: negative id %d", id)
		}
		t.vocabSize = max(t.vocabSize, int(id)+1)
	}

	for b := range 256 {
		id, ok := m.Vocab[string(byteChar[b])]
		if !ok {
			id = -1
		}
		t.byteID[b] = id
	}
	pairs, err := parseMerges(m.Merges)
	if err != nil {
		return nil, err
	}
	t.merges = make(map[uint64]bpeMerge, len(pairs))
	for rank, p := range pairs {
		a, okA := m.Vocab[p[0]]
		b, okB := m.Vocab[p[1]]
		ab, okAB := m.Vocab[p[0]+p[1]]
		if !okA || !okB || !okAB {
			return nil, fmt.Errorf("tokenizer: merge %d (%q %q) is out of the vocabulary", rank, p[0], p[1])
		}
		// A repeated pair keeps its last rank, as the library's map does.
		t.merges[uint64(a)<<32|uint64(b)] = bpeMerge{rank: int32(rank), id: ab}
	}
	if m.IgnoreMerges {
		t.whole = make(map[string]int32, len(m.Vocab))
		for s, id := range m.Vocab {
			if raw, ok := byteLevelDecode(s); ok {
				t.whole[raw] = id
			}
		}
	}

	if err := t.loadAddedTokens(f.AddedTokens, m.Vocab); err != nil {
		return nil, err
	}
	t.cache.init()
	return t, nil
}

// parseMerges accepts both merges formats: [["a", "b"], ...] and the older
// ["a b", ...] (where lines starting with "#version" are skipped).
func parseMerges(raw json.RawMessage) ([][2]string, error) {
	var pairs [][2]string
	if err := json.Unmarshal(raw, &pairs); err == nil {
		return pairs, nil
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, fmt.Errorf("tokenizer: merges: %w", err)
	}
	pairs = make([][2]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "#version") {
			continue
		}
		a, b, ok := strings.Cut(line, " ")
		if !ok || strings.Contains(b, " ") {
			return nil, fmt.Errorf("tokenizer: bad merge %d: %q", len(pairs)+1, line)
		}
		pairs = append(pairs, [2]string{a, b})
	}
	return pairs, nil
}

// loadAddedTokens reproduces AddedVocabulary::add_tokens: a token whose
// content is in the vocabulary (or already added) keeps that id, any other
// takes the next id past the vocabulary. A file whose ids disagree is refused
// (the library only warns, then uses its own ids).
func (t *Tokenizer) loadAddedTokens(tokens []addedTokenID, vocab map[string]int32) error {
	t.added = make(map[int32]addedToken, len(tokens))
	byContent := make(map[string]int32, len(tokens))
	next := int32(len(vocab))
	for _, tok := range tokens {
		if tok.Content == "" {
			continue
		}
		if id, ok := byContent[tok.Content]; ok && t.added[id] == tok.addedToken {
			continue
		}
		id, ok := byContent[tok.Content]
		if !ok {
			id, ok = vocab[tok.Content]
		}
		if !ok {
			id = next
			next++
		}
		if id != tok.ID {
			return fmt.Errorf("tokenizer: added token %q has id %d, expected %d", tok.Content, tok.ID, id)
		}
		byContent[tok.Content] = id
		t.added[id] = tok.addedToken
		t.vocabSize = max(t.vocabSize, int(id)+1)
	}
	t.rawTrie, t.normTrie = &tokenTrie{}, &tokenTrie{}
	t.rawTrie.init()
	t.normTrie.init()
	for id, tok := range t.added {
		trie, pattern := t.rawTrie, tok.Content
		if tok.Normalized {
			trie, pattern = t.normTrie, t.normalize(pattern)
		}
		if !trie.add(pattern, id) {
			return fmt.Errorf("tokenizer: added token %q is a duplicate pattern", tok.Content)
		}
	}
	return nil
}

// VocabSize is one more than the largest id, over the vocabulary and the added
// tokens.
func (t *Tokenizer) VocabSize() int { return t.vocabSize }

type piece struct {
	s  string
	id int32 // the added token's id, or -1 for text still to encode
}

// Encode returns the token ids of s without special tokens, like
// tokenizer.encode(s, add_special_tokens=False).ids. Invalid UTF-8 is first
// replaced by U+FFFD (a Python str cannot hold it).
func (t *Tokenizer) Encode(s string) []int32 {
	s = validUTF8(s)
	ids := make([]int32, 0, len(s)/3+4)
	var raw, normed [16]piece
	for _, p := range t.rawTrie.split(s, t.added, raw[:0]) {
		if p.id >= 0 {
			ids = append(ids, p.id)
			continue
		}
		for _, q := range t.normTrie.split(t.normalize(p.s), t.added, normed[:0]) {
			if q.id >= 0 {
				ids = append(ids, q.id)
				continue
			}
			ids = t.encodePiece(q.s, ids)
		}
	}
	return ids
}

func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// encodePiece pre-tokenizes one piece of normalized text and appends the BPE
// ids of every pre-token.
func (t *Tokenizer) encodePiece(s string, ids []int32) []int32 {
	for len(s) > 0 {
		n := preTokenLen(s)
		ids = t.bpe(s[:n], ids)
		s = s[n:]
	}
	return ids
}

// Character classes of the GPT-2 split regex as Oniguruma evaluates it: \s is
// White_Space, \p{L} letters and \p{N} numbers; everything else is "other".
const (
	classOther = iota
	classSpace
	classLetter
	classNumber
)

var asciiClass = func() (c [utf8.RuneSelf]uint8) {
	for b := range utf8.RuneSelf {
		switch {
		case b == ' ' || (b >= '\t' && b <= '\r'):
			c[b] = classSpace
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
			c[b] = classLetter
		case b >= '0' && b <= '9':
			c[b] = classNumber
		}
	}
	return
}()

func classAt(s string, i int) (class uint8, size int) {
	if b := s[i]; b < utf8.RuneSelf {
		return asciiClass[b], 1
	}
	r, size := utf8.DecodeRuneInString(s[i:])
	if r >= onigClassFix[0].lo {
		cmp := func(c classRange, r rune) int { return rangeCmp(c.lo, c.hi, r) }
		if i, ok := slices.BinarySearchFunc(onigClassFix, r, cmp); ok {
			return onigClassFix[i].class, size
		}
	}
	switch {
	case unicode.IsSpace(r):
		return classSpace, size
	case unicode.IsLetter(r):
		return classLetter, size
	case unicode.IsNumber(r):
		return classNumber, size
	}
	return classOther, size
}

// runEnd is the end of the run of class c starting at i.
func runEnd(s string, i int, c uint8) int {
	for i < len(s) {
		ci, n := classAt(s, i)
		if ci != c {
			break
		}
		i += n
	}
	return i
}

// preTokenLen is the length in bytes of the first pre-token of s (non-empty):
// the first match of the GPT-2 pattern
//
//	's|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+
//
// with Oniguruma's backtracking (the first alternative that matches wins).
// Every character matches some alternative, so the pre-tokens tile the text.
func preTokenLen(s string) int {
	if s[0] == '\'' && len(s) > 1 {
		switch s[1] {
		case 's', 't', 'm', 'd':
			return 2
		case 'r', 'v':
			if len(s) > 2 && s[2] == 'e' {
				return 3
			}
		case 'l':
			if len(s) > 2 && s[2] == 'l' {
				return 3
			}
		}
	}
	start := 0
	if s[0] == ' ' && len(s) > 1 {
		start = 1 // " ?" takes the space when a letter, number or other follows
	}
	c, n := classAt(s, start)
	if c != classSpace {
		return runEnd(s, start+n, c)
	}
	// \s+(?!\S): the whole run at the end of the text, else the run less its
	// last character; a single space before a non-space falls to \s+.
	end, last := 0, 0
	for end < len(s) {
		c, n := classAt(s, end)
		if c != classSpace {
			break
		}
		last = end
		end += n
	}
	if end == len(s) || last == 0 {
		return end
	}
	return last
}

// classRange gives the class of the characters lo..hi.
type classRange struct {
	lo, hi rune
	class  uint8
}

// rangeCmp orders a range of characters lo..hi against r for a binary search.
func rangeCmp(lo, hi, r rune) int {
	switch {
	case hi < r:
		return -1
	case lo > r:
		return 1
	}
	return 0
}

// onigClassFix pins the regex classes to the Unicode 16.0 data of the
// library's Oniguruma wherever Go's unicode tables (15.0 in Go 1.26, 17.0 in
// Go 1.27) disagree: letters and numbers new in 15.1 and 16.0, and characters
// new in 17.0, which Oniguruma sees as unassigned ("other"). Generated by
// probing the library's pre-tokenizer on every code point; TestUnicodeVersions
// fails when a Go or x/text upgrade needs it regenerated.
var onigClassFix = []classRange{
	{0x88f, 0x88f, classOther},
	{0xc5c, 0xc5c, classOther},
	{0xcdc, 0xcdc, classOther},
	{0x1c89, 0x1c8a, classLetter},
	{0xa7cb, 0xa7cd, classLetter},
	{0xa7ce, 0xa7cf, classOther},
	{0xa7d2, 0xa7d2, classOther},
	{0xa7d4, 0xa7d4, classOther},
	{0xa7da, 0xa7dc, classLetter},
	{0xa7f1, 0xa7f1, classOther},
	{0x105c0, 0x105f3, classLetter},
	{0x10940, 0x10959, classOther},
	{0x10d40, 0x10d49, classNumber},
	{0x10d4a, 0x10d65, classLetter},
	{0x10d6f, 0x10d85, classLetter},
	{0x10ec2, 0x10ec4, classLetter},
	{0x10ec5, 0x10ec7, classOther},
	{0x11380, 0x11389, classLetter},
	{0x1138b, 0x1138b, classLetter},
	{0x1138e, 0x1138e, classLetter},
	{0x11390, 0x113b5, classLetter},
	{0x113b7, 0x113b7, classLetter},
	{0x113d1, 0x113d1, classLetter},
	{0x113d3, 0x113d3, classLetter},
	{0x116d0, 0x116e3, classNumber},
	{0x11bc0, 0x11be0, classLetter},
	{0x11bf0, 0x11bf9, classNumber},
	{0x11db0, 0x11ddb, classOther},
	{0x11de0, 0x11de9, classOther},
	{0x13460, 0x143fa, classLetter},
	{0x16100, 0x1611d, classLetter},
	{0x16130, 0x16139, classNumber},
	{0x16d40, 0x16d6c, classLetter},
	{0x16d70, 0x16d79, classNumber},
	{0x16ea0, 0x16eb8, classOther},
	{0x16ebb, 0x16ed3, classOther},
	{0x16ff2, 0x16ff6, classOther},
	{0x187f8, 0x187ff, classOther},
	{0x18cff, 0x18cff, classLetter},
	{0x18d09, 0x18d1e, classOther},
	{0x18d80, 0x18df2, classOther},
	{0x1ccf0, 0x1ccf9, classNumber},
	{0x1e5d0, 0x1e5ed, classLetter},
	{0x1e5f0, 0x1e5f0, classLetter},
	{0x1e5f1, 0x1e5fa, classNumber},
	{0x1e6c0, 0x1e6de, classOther},
	{0x1e6e0, 0x1e6e2, classOther},
	{0x1e6e4, 0x1e6e5, classOther},
	{0x1e6e7, 0x1e6ed, classOther},
	{0x1e6f0, 0x1e6f4, classOther},
	{0x1e6fe, 0x1e6ff, classOther},
	{0x2b73a, 0x2b73f, classOther},
	{0x2cea2, 0x2cead, classOther},
	{0x2ebf0, 0x2ee5d, classLetter},
	{0x323b0, 0x33479, classOther},
}

// byteChar is GPT-2's bytes_to_unicode table: the character standing for
// each byte in byte-level vocabulary strings.
var byteChar = func() (t [256]rune) {
	n := rune(0)
	for b := range 256 {
		if (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF) {
			t[b] = rune(b)
		} else {
			t[b] = 256 + n
			n++
		}
	}
	return
}()

// charByte inverts byteChar: the byte each character stands for, or -1.
var charByte = func() (inv [324]int16) {
	for i := range inv {
		inv[i] = -1
	}
	for b, r := range byteChar {
		inv[r] = int16(b)
	}
	return
}()

// byteLevelDecode maps a byte-level vocabulary string back to its bytes.
func byteLevelDecode(s string) (string, bool) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 0 || int(r) >= len(charByte) || charByte[r] < 0 {
			return "", false
		}
		out = append(out, byte(charByte[r]))
	}
	return string(out), true
}

// bpe appends the ids of one pre-token, from the cache when it can.
func (t *Tokenizer) bpe(s string, ids []int32) []int32 {
	if len(s) == 1 {
		if id := t.byteID[s[0]]; id >= 0 {
			ids = append(ids, id)
		}
		return ids
	}
	if t.whole != nil {
		if id, ok := t.whole[s]; ok {
			return append(ids, id)
		}
	}
	if len(s) < cacheMaxLen {
		if hit, ok := t.cache.get(s); ok {
			return append(ids, hit...)
		}
	}
	n := len(ids)
	ids = t.merge(s, ids)
	if len(s) < cacheMaxLen {
		t.cache.put(s, ids[n:len(ids):len(ids)])
	}
	return ids
}

type bpeSymbol struct {
	id         int32
	prev, next int32
	gone       bool
}

type bpeCandidate struct {
	rank, pos, id int32
}

// merge is Word::merge_all: a min-heap of candidate merges ordered by rank,
// then position, with stale candidates skipped when the pair at their position
// no longer merges into the same id.
func (t *Tokenizer) merge(s string, ids []int32) []int32 {
	syms := make([]bpeSymbol, 0, len(s))
	for i := range len(s) {
		if id := t.byteID[s[i]]; id >= 0 {
			syms = append(syms, bpeSymbol{id: id, prev: int32(len(syms)) - 1, next: int32(len(syms)) + 1})
		}
	}
	if len(syms) == 0 {
		return ids
	}
	syms[len(syms)-1].next = -1
	var h bpeHeap
	for i := 0; i+1 < len(syms); i++ {
		if m, ok := t.merges[uint64(syms[i].id)<<32|uint64(syms[i+1].id)]; ok {
			h = append(h, bpeCandidate{m.rank, int32(i), m.id})
		}
	}
	h.init()
	for len(h) > 0 {
		top := h.pop()
		cur := &syms[top.pos]
		if cur.gone || cur.next < 0 {
			continue
		}
		right := syms[cur.next]
		if m, ok := t.merges[uint64(cur.id)<<32|uint64(right.id)]; !ok || m.id != top.id {
			continue
		}
		cur.id = top.id
		syms[cur.next].gone = true
		cur.next = right.next
		if right.next >= 0 {
			syms[right.next].prev = top.pos
		}
		if cur.prev >= 0 {
			if m, ok := t.merges[uint64(syms[cur.prev].id)<<32|uint64(cur.id)]; ok {
				h.push(bpeCandidate{m.rank, cur.prev, m.id})
			}
		}
		if cur.next >= 0 {
			if m, ok := t.merges[uint64(cur.id)<<32|uint64(syms[cur.next].id)]; ok {
				h.push(bpeCandidate{m.rank, top.pos, m.id})
			}
		}
	}
	for i := range syms {
		if !syms[i].gone {
			ids = append(ids, syms[i].id)
		}
	}
	return ids
}

type bpeHeap []bpeCandidate

func (h bpeHeap) less(i, j int) bool {
	return h[i].rank < h[j].rank || (h[i].rank == h[j].rank && h[i].pos < h[j].pos)
}

func (h bpeHeap) init() {
	for i := len(h)/2 - 1; i >= 0; i-- {
		h.down(i)
	}
}

func (h *bpeHeap) push(c bpeCandidate) {
	*h = append(*h, c)
	for i := len(*h) - 1; i > 0; {
		p := (i - 1) / 2
		if !h.less(i, p) {
			break
		}
		(*h)[i], (*h)[p] = (*h)[p], (*h)[i]
		i = p
	}
}

func (h *bpeHeap) pop() bpeCandidate {
	old := *h
	top := old[0]
	n := len(old) - 1
	old[0] = old[n]
	*h = old[:n]
	h.down(0)
	return top
}

func (h bpeHeap) down(i int) {
	for {
		l := 2*i + 1
		if l >= len(h) {
			return
		}
		m := l
		if r := l + 1; r < len(h) && h.less(r, l) {
			m = r
		}
		if !h.less(m, i) {
			return
		}
		h[i], h[m] = h[m], h[i]
		i = m
	}
}

// bpeCache holds the ids of recent pre-tokens. Each shard is cleared when it
// fills, which keeps it bounded and lets the common pre-tokens back in.
const (
	cacheShards   = 32
	cacheShardCap = 4096
	cacheMaxLen   = 256 // bytes; the library does not cache longer words either
)

type bpeCache struct {
	seed   maphash.Seed
	shards [cacheShards]struct {
		mu sync.RWMutex
		m  map[string][]int32
	}
}

func (c *bpeCache) init() {
	c.seed = maphash.MakeSeed()
	for i := range c.shards {
		c.shards[i].m = make(map[string][]int32)
	}
}

func (c *bpeCache) shard(s string) int { return int(maphash.String(c.seed, s) % cacheShards) }

func (c *bpeCache) get(s string) ([]int32, bool) {
	sh := &c.shards[c.shard(s)]
	sh.mu.RLock()
	v, ok := sh.m[s]
	sh.mu.RUnlock()
	return v, ok
}

func (c *bpeCache) put(s string, ids []int32) {
	v := append([]int32(nil), ids...)
	sh := &c.shards[c.shard(s)]
	sh.mu.Lock()
	if len(sh.m) >= cacheShardCap {
		clear(sh.m)
	}
	sh.m[strings.Clone(s)] = v
	sh.mu.Unlock()
}

// tokenTrie finds added tokens in text with leftmost-longest matching, like
// the library's Aho-Corasick automaton: the earliest position where any token
// starts, and the longest token there; the search resumes after the match.
type tokenTrie struct {
	first [256]bool
	nodes []trieNode
}

type trieNode struct {
	next []trieEdge
	id   int32 // the token ending here, or -1
}

type trieEdge struct {
	b  byte
	to int32
}

func (tr *tokenTrie) init() { tr.nodes = []trieNode{{id: -1}} }

func (tr *tokenTrie) add(pattern string, id int32) bool {
	if pattern == "" {
		return true
	}
	tr.first[pattern[0]] = true
	n := int32(0)
	for i := range len(pattern) {
		b := pattern[i]
		to := int32(-1)
		for _, e := range tr.nodes[n].next {
			if e.b == b {
				to = e.to
				break
			}
		}
		if to < 0 {
			to = int32(len(tr.nodes))
			tr.nodes = append(tr.nodes, trieNode{id: -1})
			tr.nodes[n].next = append(tr.nodes[n].next, trieEdge{b, to})
		}
		n = to
	}
	if tr.nodes[n].id >= 0 {
		return false
	}
	tr.nodes[n].id = id
	return true
}

// find returns the leftmost-longest match at or after from.
func (tr *tokenTrie) find(s string, from int) (start, end int, id int32) {
	for i := from; i < len(s); i++ {
		if !tr.first[s[i]] {
			continue
		}
		n, end, id := int32(0), -1, int32(-1)
		for j := i; j < len(s); j++ {
			to := int32(-1)
			for _, e := range tr.nodes[n].next {
				if e.b == s[j] {
					to = e.to
					break
				}
			}
			if to < 0 {
				break
			}
			n = to
			if tr.nodes[n].id >= 0 {
				end, id = j+1, tr.nodes[n].id
			}
		}
		if end >= 0 {
			return i, end, id
		}
	}
	return -1, -1, -1
}

// split is AddedVocabulary::find_matches with split_with_indices: it cuts s
// into added tokens and the text between them, honoring single_word, lstrip
// and rstrip, and drops empty pieces.
func (tr *tokenTrie) split(s string, added map[int32]addedToken, out []piece) []piece {
	if s == "" {
		return out
	}
	if len(tr.nodes) <= 1 {
		return append(out, piece{s, -1})
	}
	offset := 0
	for from := 0; ; {
		start, stop, id := tr.find(s, from)
		if id < 0 {
			break
		}
		from = stop
		tok := added[id]
		if tok.SingleWord && ((start > 0 && endsWithWord(s[:start])) || (stop < len(s) && startsWithWord(s[stop:]))) {
			continue
		}
		if tok.LStrip {
			start = max(trailingSpaceStart(s[:start]), offset)
		}
		if tok.RStrip {
			stop += leadingSpaceEnd(s[stop:])
		}
		if offset < start {
			out = append(out, piece{s[offset:start], -1})
		}
		if start < stop {
			out = append(out, piece{s[start:stop], id})
		}
		offset = stop
	}
	if offset < len(s) {
		out = append(out, piece{s[offset:], -1})
	}
	return out
}

// isWordChar is Rust regex's Unicode \w.
func isWordChar(r rune) bool {
	return unicode.In(r, unicode.L, unicode.Nl, unicode.Other_Alphabetic, unicode.M, unicode.Nd,
		unicode.Pc, unicode.Join_Control)
}

func endsWithWord(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return isWordChar(r)
}

func startsWithWord(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return isWordChar(r)
}

func trailingSpaceStart(s string) int {
	return len(strings.TrimRightFunc(s, unicode.IsSpace))
}

func leadingSpaceEnd(s string) int {
	return len(s) - len(strings.TrimLeftFunc(s, unicode.IsSpace))
}

// NFC. The library normalizes with unicode-normalization-alignments, whose
// Unicode data predates golang.org/x/text's (15.0 before Go 1.27, 17.0 from
// it): it does not know the combining marks and compositions in nfcUnknown,
// so it treats them as unassigned characters that never reorder or compose.
// It also does not apply the Stream-Safe Text Format, where x/text inserts
// U+034F after 30 non-starters in a row. Text with either goes through
// nfcSlow, which runs the normalization algorithm with x/text's data and
// those characters left alone; all other text uses x/text directly.

type runeRange struct{ lo, hi rune }

func nfcIsUnknown(r rune) bool {
	if r < nfcUnknown[0].lo {
		return false
	}
	_, ok := slices.BinarySearchFunc(nfcUnknown, r, func(u runeRange, r rune) int { return rangeCmp(u.lo, u.hi, r) })
	return ok
}

// nfcUnknown lists the characters whose NFC differs between x/text and the
// library, found by probing both on every character with a combining class,
// a decomposition or a composition (tokcheck/nfcgo in the MERLIN study).
var nfcUnknown = []runeRange{
	{0x7fd, 0x7fd},
	{0x897, 0x89f},
	{0x8ca, 0x8d3},
	{0x9fe, 0x9fe},
	{0xc3c, 0xc3c},
	{0xd3b, 0xd3c},
	{0xeba, 0xeba},
	{0x1715, 0x1715},
	{0x1abf, 0x1add},
	{0x1ae0, 0x1aeb},
	{0x1df6, 0x1dfa},
	{0xa82c, 0xa82c},
	{0x105c9, 0x105c9},
	{0x105d2, 0x105d2},
	{0x105da, 0x105da},
	{0x105e4, 0x105e4},
	{0x10d24, 0x10d27},
	{0x10d69, 0x10d6d},
	{0x10eab, 0x10eac},
	{0x10efa, 0x10efb},
	{0x10efd, 0x10eff},
	{0x10f46, 0x10f50},
	{0x10f82, 0x10f85},
	{0x11070, 0x11070},
	{0x1133b, 0x1133b},
	{0x11382, 0x11385},
	{0x1138b, 0x1138b},
	{0x1138e, 0x1138e},
	{0x11390, 0x11391},
	{0x113b8, 0x113b8},
	{0x113bb, 0x113bb},
	{0x113c2, 0x113c2},
	{0x113c5, 0x113c5},
	{0x113c7, 0x113c9},
	{0x113ce, 0x113d0},
	{0x1145e, 0x1145e},
	{0x11839, 0x1183a},
	{0x11930, 0x11930},
	{0x11935, 0x11935},
	{0x11938, 0x11938},
	{0x1193d, 0x1193e},
	{0x11943, 0x11943},
	{0x119e0, 0x119e0},
	{0x11a34, 0x11a34},
	{0x11a47, 0x11a47},
	{0x11a99, 0x11a99},
	{0x11d42, 0x11d42},
	{0x11d44, 0x11d45},
	{0x11d97, 0x11d97},
	{0x11f41, 0x11f42},
	{0x1611e, 0x16129},
	{0x1612f, 0x1612f},
	{0x16d63, 0x16d63},
	{0x16d67, 0x16d6a},
	{0x16ff0, 0x16ff1},
	{0x1e08f, 0x1e08f},
	{0x1e130, 0x1e136},
	{0x1e2ae, 0x1e2ae},
	{0x1e2ec, 0x1e2ef},
	{0x1e4ec, 0x1e4ef},
	{0x1e5ee, 0x1e5ef},
	{0x1e6e3, 0x1e6e3},
	{0x1e6e6, 0x1e6e6},
	{0x1e6ee, 0x1e6ef},
	{0x1e6f5, 0x1e6f5},
}

func (t *Tokenizer) normalize(s string) string {
	if !t.nfc {
		return s
	}
	if needsSlowNFC(s) {
		return nfcSlow(s)
	}
	return norm.NFC.String(s)
}

// needsSlowNFC reports whether s has a character of nfcUnknown or a run of
// non-starters long enough that x/text might break it up. A character
// decomposes into at most 3 non-starters, so 8 characters in a row are a
// safe trigger.
func needsSlowNFC(s string) bool {
	run := 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			run = 0
			i++
			continue
		}
		p := norm.NFC.PropertiesString(s[i:])
		if r, _ := utf8.DecodeRuneInString(s[i:]); nfcIsUnknown(r) {
			return true
		}
		switch {
		case p.CCC() != 0 || p.LeadCCC() != 0:
			run++
		case p.TrailCCC() != 0:
			run = 1
		default:
			run = 0
		}
		if run >= 8 {
			return true
		}
		i += max(p.Size(), 1)
	}
	return false
}

type nfcChar struct {
	r     rune
	ccc   uint8
	fixed bool // in nfcUnknown: a starter that never composes
}

// nfcSlow is canonical decomposition, canonical ordering and canonical
// composition (UAX #15) with x/text's data, except that the characters of
// nfcUnknown stay as they are, and no U+034F is inserted.
func nfcSlow(s string) string {
	buf := make([]nfcChar, 0, len(s))
	for _, r := range s {
		if unknown := nfcIsUnknown(r); unknown || r < utf8.RuneSelf {
			buf = append(buf, nfcChar{r: r, fixed: unknown})
			continue
		}
		for _, d := range norm.NFD.String(string(r)) {
			buf = append(buf, nfcChar{r: d, ccc: norm.NFD.PropertiesString(string(d)).CCC()})
		}
	}
	// A stable sort of each run of non-starters by combining class.
	for i := 1; i < len(buf); i++ {
		for j := i; j > 0 && buf[j].ccc != 0 && buf[j-1].ccc > buf[j].ccc; j-- {
			buf[j], buf[j-1] = buf[j-1], buf[j]
		}
	}
	out := buf[:0]
	starter, last := -1, -1 // the last starter, and the class of the last character kept after it
	for _, c := range buf {
		if starter >= 0 && !c.fixed && !out[starter].fixed && (last < 0 || (c.ccc != 0 && last < int(c.ccc))) {
			if r, ok := composePair(out[starter].r, c.r); ok {
				out[starter].r = r
				continue
			}
		}
		if c.ccc == 0 {
			starter, last = len(out), -1
		} else {
			last = int(c.ccc)
		}
		out = append(out, c)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range out {
		b.WriteRune(c.r)
	}
	return b.String()
}

// composePair is the primary composite of a and b, if there is one.
func composePair(a, b rune) (rune, bool) {
	s := norm.NFC.String(string(a) + string(b))
	r, n := utf8.DecodeRuneInString(s)
	return r, n == len(s) && r != utf8.RuneError
}
