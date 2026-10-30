package labeler

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

type parityFixture struct {
	FramingContract string `json:"framing_contract"`
	TokenizerSHA256 string `json:"tokenizer_sha256"`
	Cases           []struct {
		Case string `json:"case"`
		Item struct {
			Segment string `json:"segment"`
			Cohort  string `json:"cohort"`
			Sender  string `json:"sender"`
			Context string `json:"context"`
			Text    string `json:"text"`
		} `json:"item"`
		InputIDs []int32 `json:"input_ids"`
	} `json:"cases"`
}

func loadParityFixture(t testing.TB) *parityFixture {
	t.Helper()
	f, err := os.Open("../../testdata/labeler/kestrel-68m-parity.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var fx parityFixture
	if err := json.NewDecoder(zr).Decode(&fx); err != nil {
		t.Fatal(err)
	}
	return &fx
}

// TestFrameParity checks Frame against the ids the Python framing produced for
// the export's parity fixture.
func TestFrameParity(t *testing.T) {
	tok := testTokenizer(t)
	fx := loadParityFixture(t)
	if fx.FramingContract != FramingContract {
		t.Fatalf("fixture contract %q, want %q", fx.FramingContract, FramingContract)
	}
	if fx.TokenizerSHA256 != kestrelTokenizerSHA256 {
		t.Fatalf("fixture tokenizer %s, want %s", fx.TokenizerSHA256, kestrelTokenizerSHA256)
	}
	if len(fx.Cases) != 187 {
		t.Fatalf("%d cases, want 187", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		got := tok.Frame(FrameInput{
			Segment: c.Item.Segment, Cohort: c.Item.Cohort, Sender: c.Item.Sender,
			Context: c.Item.Context, Text: c.Item.Text,
		})
		if !slices.Equal(got, c.InputIDs) {
			t.Errorf("%s: got %d ids %v\nwant %d ids %v", c.Case, len(got), got, len(c.InputIDs), c.InputIDs)
		}
	}
}

func TestFrameBudget(t *testing.T) {
	tok := testTokenizer(t)
	long := strings.Repeat("lorem ", 600) // 601 tokens, under the code point cap
	n := func(s string) int { return len(tok.Encode(s)) }
	budget := MaxTokens - 3 - n("human user :")
	tests := []struct {
		name      string
		in        FrameInput
		ctx, text int // expected context and text token counts
		truncated bool
	}{
		{"short text, short context", FrameInput{"H-typed+ctx", "human", "user", "lorem ipsum", "dolor"},
			n("lorem ipsum"), n("dolor"), false},
		{"long text keeps 64 context tokens", FrameInput{"H-typed+ctx", "human", "user", long, long},
			64, budget - 64, true},
		{"no context", FrameInput{"H-typed", "human", "user", "", long}, 0, budget, true},
		{"non-human context is cut to 512 code points", FrameInput{"AR", "agent_reply", "assistant", long, "dolor"},
			n(long[len(long)-512:]), n("dolor"), false},
		{"human context is cut to its last tokens", FrameInput{"H-typed+ctx", "human", "user", long, "dolor"},
			budget - n("dolor"), n("dolor"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tok.Frame(tc.in)
			tag := tok.Encode(senderTag(tc.in.Cohort, tc.in.Sender))
			sep := slices.Index(got, SepID)
			if got[0] != ClsID || got[len(got)-1] != SepID || sep < 1+len(tag) || len(got) > MaxTokens {
				t.Fatalf("bad shape %v", got)
			}
			if !slices.Equal(got[1:1+len(tag)], tag) {
				t.Fatalf("tag %v, want %v", got[1:1+len(tag)], tag)
			}
			ctx, text := got[1+len(tag):sep], got[sep+1:len(got)-1]
			if len(ctx) != tc.ctx || len(text) != tc.text {
				t.Fatalf("context %d text %d tokens, want %d and %d", len(ctx), len(text), tc.ctx, tc.text)
			}
			if full := tok.Encode(tc.in.Context); len(ctx) > 0 && !slices.Equal(ctx, full[len(full)-len(ctx):]) &&
				HumanSegment(tc.in.Segment) {
				t.Fatal("context is not its last tokens")
			}
			if full := tok.Encode(tc.in.Text); tc.truncated {
				h := int(float64(len(text)) * 0.7)
				if !slices.Equal(text[:h], full[:h]) || !slices.Equal(text[h:], full[len(full)-(len(text)-h):]) {
					t.Fatal("truncated text is not head + tail")
				}
			} else if !slices.Equal(text, full) {
				t.Fatal("text was cut")
			}
		})
	}
}

func TestFrameHeadFraction(t *testing.T) {
	// Python's int(tb * 0.7): 90 * 0.7 is 62.99999999999999 in float64, so 62.
	for tb, want := range map[int]int{0: 0, 1: 0, 7: 4, 10: 7, 90: 62, 170: 118, 180: 125, 445: 311, 509: 356} {
		if got := int(float64(tb) * frameHeadFrac); got != want {
			t.Errorf("int(%d * 0.7) = %d, want %d", tb, got, want)
		}
	}
}

func TestFrameCodePoints(t *testing.T) {
	tok := testTokenizer(t)
	// 4,097 code points of 2 or 4 bytes each: the cut counts code points.
	text := strings.Repeat("é", 2000) + strings.Repeat("😀", 2097)
	cut := strings.Repeat("é", 2000) + strings.Repeat("😀", 48) + " … " + strings.Repeat("😀", 2048)
	in := FrameInput{Segment: "TH", Cohort: "thinking", Sender: "assistant", Text: text}
	want := tok.Frame(FrameInput{Segment: "TH", Cohort: "thinking", Sender: "assistant", Text: cut})
	if got := tok.Frame(in); !slices.Equal(got, want) {
		t.Fatal("text cut does not count code points")
	}
	if got, want := tailRunes("aé😀b", 3), "é😀b"; got != want {
		t.Errorf("tailRunes = %q, want %q", got, want)
	}
	if got, want := headRunes("aé😀b", 3), "aé😀"; got != want {
		t.Errorf("headRunes = %q, want %q", got, want)
	}
}

func TestSenderTag(t *testing.T) {
	for _, tc := range []struct{ cohort, sender, want string }{
		{"human", "user", "human user :"},
		{"delegation", "tool", "delegation tool :"},
		{"human", "", "human :"},
		{"human", "account:someone", "human account :"},
		{"thinking", "\x1f", "thinking :"},
	} {
		if got := senderTag(tc.cohort, tc.sender); got != tc.want {
			t.Errorf("senderTag(%q, %q) = %q, want %q", tc.cohort, tc.sender, got, tc.want)
		}
	}
	if !HumanSegment("H-resent") || HumanSegment("AIUR") || HumanSegment("human") {
		t.Error("HumanSegment")
	}
}

func BenchmarkFrame(b *testing.B) {
	tok := testTokenizer(b)
	short := FrameInput{Segment: "H-typed", Cohort: "human", Sender: "user", Text: "fix the flaky login test please"}
	var sb strings.Builder
	for sb.Len() < 2000 {
		sb.WriteString("Refactor the parser so `parseHeader()` returns (name, value, err); " +
			"then update 3 call sites in pkg/http2 and re-run the 12 failing tests.\n")
	}
	long := FrameInput{Segment: "AR", Cohort: "agent_reply", Sender: "assistant",
		Context: "Previous user request: please tidy up the header parsing code", Text: sb.String()}
	for _, bc := range []struct {
		name string
		in   FrameInput
	}{{"short", short}, {"2000chars", long}} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				tok.Frame(bc.in)
			}
		})
	}
}

// The spans tile the sequence around its three special tokens, and each
// holds what was built for it.
func TestFrameSpans(t *testing.T) {
	tok := testTokenizer(t)
	for _, c := range loadParityFixture(t).Cases {
		in := FrameInput{Segment: c.Item.Segment, Cohort: c.Item.Cohort, Sender: c.Item.Sender, Context: c.Item.Context, Text: c.Item.Text}
		ids, spans := tok.FrameWithSpans(in)
		if !slices.Equal(ids, c.InputIDs) {
			t.Fatalf("%s: ids differ from Frame's", c.Case)
		}
		if spans.Tag.Start != 1 || spans.Tag.End != spans.Context.Start || spans.Context.End+1 != spans.Text.Start || spans.Text.End != len(ids)-1 {
			t.Fatalf("%s: spans %+v for %d ids", c.Case, spans, len(ids))
		}
		if !slices.Equal(ids[spans.Tag.Start:spans.Tag.End], tok.Encode(senderTag(in.Cohort, in.Sender))) {
			t.Errorf("%s: the tag span is not the tag", c.Case)
		}
		if in.Text != "" && spans.Text.Len() == 0 || in.Context == "" && spans.Context.Len() != 0 {
			t.Errorf("%s: spans %+v", c.Case, spans)
		}
		if text := tok.Encode(in.Text); len(text) <= spans.Text.Len() && !slices.Equal(ids[spans.Text.Start:spans.Text.End], text) {
			t.Errorf("%s: an untruncated text span is not the text", c.Case)
		}
	}
}
