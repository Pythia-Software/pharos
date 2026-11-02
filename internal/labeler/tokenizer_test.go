package labeler

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// kestrelTokenizerSHA256 is the SHA-256 of kestrel-68m's tokenizer.json
// (uncompressed), as the model export records it.
const kestrelTokenizerSHA256 = "6c8aaa9a542084f2457eab775d4eeb51f92a70c0fd9de28d5edb0ddec3c08d30"

var (
	tokenizerOnce sync.Once
	tokenizerJSON []byte
	tokenizerErr  error
	tokenizerVal  *Tokenizer
)

// kestrelTokenizerJSON returns the committed tokenizer.json, uncompressed.
func kestrelTokenizerJSON(t testing.TB) []byte {
	t.Helper()
	tokenizerOnce.Do(func() {
		var f *os.File
		if f, tokenizerErr = os.Open("../../testdata/labeler/kestrel-68m-tokenizer.json.gz"); tokenizerErr != nil {
			return
		}
		defer f.Close()
		var zr *gzip.Reader
		if zr, tokenizerErr = gzip.NewReader(f); tokenizerErr != nil {
			return
		}
		if tokenizerJSON, tokenizerErr = io.ReadAll(zr); tokenizerErr != nil {
			return
		}
		tokenizerVal, tokenizerErr = LoadTokenizer(tokenizerJSON)
	})
	if tokenizerErr != nil {
		t.Fatal(tokenizerErr)
	}
	return tokenizerJSON
}

// testTokenizer returns kestrel-68m's tokenizer, loaded once per process.
func testTokenizer(t testing.TB) *Tokenizer {
	t.Helper()
	kestrelTokenizerJSON(t)
	return tokenizerVal
}

func TestLoadTokenizer(t *testing.T) {
	data := kestrelTokenizerJSON(t)
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != kestrelTokenizerSHA256 {
		t.Fatalf("tokenizer SHA-256 %s, want %s", got, kestrelTokenizerSHA256)
	}
	tok := testTokenizer(t)
	if got := tok.VocabSize(); got != 50368 {
		t.Errorf("VocabSize = %d, want 50368", got)
	}
	for _, s := range []string{"", "  ", "[CLS]"} {
		_ = tok.Encode(s)
	}
	if got := tok.Encode(""); len(got) != 0 {
		t.Errorf("Encode(\"\") = %v", got)
	}
}

// Expected ids come from tokenizers 0.23.3:
// Tokenizer.from_file(...).encode(s, add_special_tokens=False).ids.
var encodeCases = []struct {
	name, s string
	want    []int32
}{
	{"words", "hello world", []int32{25521, 1533}},
	{"two spaces are an added token", "a  b", []int32{66, 50276, 67}},
	{"three spaces", "a   b", []int32{66, 50275, 67}},
	{"25 spaces take the longest token first", "x" + strings.Repeat(" ", 25) + "y", []int32{89, 50254, 340}},
	{"trailing spaces", "end  ", []int32{423, 50276}},
	{"spaces around a special token", "  [SEP]  ", []int32{50276, 50282, 50276}},
	{"spaces and newlines", "  \n  x", []int32{50276, 187, 50276, 89}},
	{"newline run", "\n\n\n", []int32{2756}},
	{"tab and CRLF", "a\tb\r\nc", []int32{66, 186, 67, 190, 187, 68}},
	{"space before NBSP", "a  b", []int32{66, 209, 575, 67}},
	{"line separator", "a b", []int32{66, 40702, 67}},
	{"next line", "a\u0085b", []int32{66, 16643, 67}},
	{"zero-width space is not whitespace", "zero​width", []int32{10528, 12882, 3429}},
	{"NFC composes", "café", []int32{68, 2320, 860}},
	{"precomposed", "café", []int32{68, 2320, 860}},
	{"NFC reorders marks", "ȩ́", []int32{132, 104, 136, 212}},
	{"NFC composes Hangul jamo", "각", []int32{26532, 212}},
	{"special token", "[CLS]x", []int32{50281, 89}},
	{"lstrip takes the spaces before [MASK]", " [MASK] ", []int32{50284, 209}},
	{"lstrip after a word", "a   [MASK]b", []int32{66, 50284, 67}},
	{"normalized added token", "|||IP_ADDRESS|||:80", []int32{0, 27, 1438}},
	{"padding token inside text", "x<|padding|>y", []int32{89, 1, 90}},
	{"unused tokens", "[unused3] [unused12]", []int32{50288, 209, 50297}},
	{"end of text", "<|endoftext|>", []int32{50279}},
	{"contractions", "don't they'll we're I'M it's", []int32{9903, 626, 597, 1833, 359, 1472, 309, 8, 46, 352, 434}},
	{"apostrophes run as punctuation", "''s", []int32{6267, 84}},
	{"space apostrophe", " 's", []int32{686, 84}},
	{"numbers", "123 4567 x2", []int32{10683, 5329, 2251, 1269, 19}},
	{"other numbers", "½Ⅷ", []int32{8313, 158, 216, 102}},
	{"math alphanumerics", "\U0001d400\U0001d401", []int32{172, 240, 227, 211, 172, 240, 227, 212}},
	{"emoji ZWJ and flag", "\U0001f468‍\U0001f469‍\U0001f467 \U0001f1ef\U0001f1f5",
		[]int32{14931, 228, 103, 325, 224, 14931, 228, 104, 325, 224, 14931, 228, 102, 22692, 218, 109, 14931, 218, 115}},
	{"CJK", "你好世界", []int32{24553, 34439, 42848, 45261}},
	{"Hebrew", "שלום", []int32{26344, 20770, 12859, 240}},
	{"Hangul", "한국어", []int32{39742, 166, 115, 244, 39907}},
	{"markers", "⟦context⟧", []int32{158, 242, 101, 8882, 158, 242, 102}},
	{"punctuation run", "!!!???", []int32{15844, 33186}},
	// The library's Unicode data: Oniguruma's (16.0) for the regex, older data
	// for NFC.
	{"letter new in Unicode 15.1", "x\U0002ebf0's", []int32{89, 172, 108, 109, 110, 434}},
	{"letter new in Unicode 17", "x\U00018d0c's", []int32{89, 172, 235, 114, 223, 8, 84}},
	{"mark unknown to the library's NFC is not reordered", "a\u1ac1\u0316", []int32{66, 157, 106, 212, 136, 233}},
	{"composition unknown to the library's NFC", "\U00011935\U00011930", []int32{172, 228, 99, 115, 172, 228, 99, 110}},
	{"composite unknown to the library's NFC", "\U00011938", []int32{172, 228, 99, 118}},
	{"40 marks are reordered without U+034F", "e" + strings.Repeat("\u0301\u0316", 20),
		append(append([]int32{860}, slices.Repeat([]int32{136, 233}, 20)...), slices.Repeat([]int32{136, 212}, 19)...)},
	{"decomposition into two marks", "a\u0f73b", []int32{66, 49233, 111, 49233, 112, 67}},
}

// TestUnicodeVersions guards the tables that pin character data to the
// library's (onigClassFix, nfcUnknown). They were checked against the library
// with these versions of Go's unicode package and of x/text's norm tables
// (which x/text picks by Go release: 15.0.0 before Go 1.27, 17.0.0 from it)
// and need regenerating with the MERLIN tokcheck probes for any other.
func TestUnicodeVersions(t *testing.T) {
	switch unicode.Version {
	case "15.0.0", "17.0.0":
	default:
		t.Errorf("unicode.Version %s: regenerate onigClassFix", unicode.Version)
	}
	switch norm.Version {
	case "15.0.0", "17.0.0":
	default:
		t.Errorf("norm.Version %s: regenerate nfcUnknown", norm.Version)
	}
}

func TestEncode(t *testing.T) {
	tok := testTokenizer(t)
	for _, tc := range encodeCases {
		// Twice: the second run is served from the BPE cache.
		for range 2 {
			if got := tok.Encode(tc.s); !slices.Equal(got, tc.want) {
				t.Errorf("%s: Encode(%q) = %v, want %v", tc.name, tc.s, got, tc.want)
			}
		}
	}
}

func TestPreTokens(t *testing.T) {
	split := func(s string) []string {
		var out []string
		for len(s) > 0 {
			n := preTokenLen(s)
			out = append(out, s[:n])
			s = s[n:]
		}
		return out
	}
	for _, tc := range []struct {
		s    string
		want []string
	}{
		{"Hello world", []string{"Hello", " world"}},
		{"it's I'll 'S", []string{"it", "'s", " I", "'ll", " '", "S"}},
		{"a  b", []string{"a", " ", " b"}},
		{"a \t b", []string{"a", " \t", " b"}},
		{"x   ", []string{"x", "   "}},
		{"x\n", []string{"x", "\n"}},
		{" x", []string{" ", "x"}},
		{"12ab!?", []string{"12", "ab", "!?"}},
		{" 12 !? é", []string{" 12", " !?", " é"}},
		{"é", []string{"e", "́"}},
		{"'", []string{"'"}},
		{" ", []string{" "}},
	} {
		if got := split(tc.s); !slices.Equal(got, tc.want) {
			t.Errorf("pre-tokens of %q = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// TestLegacyMerges loads the tokenizer with merges as "a b" strings, the
// older format, and checks it encodes the same.
func TestLegacyMerges(t *testing.T) {
	var file map[string]any
	if err := json.Unmarshal(kestrelTokenizerJSON(t), &file); err != nil {
		t.Fatal(err)
	}
	model := file["model"].(map[string]any)
	pairs := model["merges"].([]any)
	lines := []any{"#version: 0.2"}
	for _, p := range pairs {
		ab := p.([]any)
		lines = append(lines, ab[0].(string)+" "+ab[1].(string))
	}
	model["merges"] = lines
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := LoadTokenizer(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range encodeCases {
		if got := legacy.Encode(tc.s); !slices.Equal(got, tc.want) {
			t.Errorf("%s: legacy Encode(%q) = %v, want %v", tc.name, tc.s, got, tc.want)
		}
	}

	model["merges"] = []any{"a b c"}
	data, _ = json.Marshal(file)
	if _, err := LoadTokenizer(data); err == nil {
		t.Error("a merge of three parts loaded")
	}
}

func TestLoadTokenizerRefuses(t *testing.T) {
	base := kestrelTokenizerJSON(t)
	for name, edit := range map[string]func(map[string]any){
		"other normalizer": func(f map[string]any) { f["normalizer"] = map[string]any{"type": "NFKC"} },
		"prefix space": func(f map[string]any) {
			f["pre_tokenizer"].(map[string]any)["add_prefix_space"] = true
		},
		"unk token":     func(f map[string]any) { f["model"].(map[string]any)["unk_token"] = "[UNK]" },
		"truncation":    func(f map[string]any) { f["truncation"] = map[string]any{"max_length": 8} },
		"wrong id":      func(f map[string]any) { f["added_tokens"].([]any)[2].(map[string]any)["id"] = 7 },
		"unknown merge": func(f map[string]any) { f["model"].(map[string]any)["merges"] = []any{[]any{"Ġ", "nope"}} },
	} {
		var f map[string]any
		if err := json.Unmarshal(base, &f); err != nil {
			t.Fatal(err)
		}
		edit(f)
		data, _ := json.Marshal(f)
		if _, err := LoadTokenizer(data); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestEncodeInvalidUTF8(t *testing.T) {
	tok := testTokenizer(t)
	if got, want := tok.Encode("a\xffb"), tok.Encode("a\uFFFDb"); !slices.Equal(got, want) {
		t.Errorf("invalid UTF-8: %v, want %v", got, want)
	}
}

func TestEncodeConcurrent(t *testing.T) {
	tok := testTokenizer(t)
	var buf bytes.Buffer
	for i := range 300 {
		buf.WriteString(strings.Repeat("x", i%7) + " token" + strings.Repeat("y", i%11) + " ")
	}
	text := buf.String()
	want := tok.Encode(text)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if got := tok.Encode(text); !slices.Equal(got, want) {
					t.Error("concurrent Encode differs")
					return
				}
			}
		})
	}
	wg.Wait()
}
