package labeler

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// FramingContract names the input construction below. It is the training
// framing of the KESTREL encoders, stated in the export's FRAMING.md and
// kframe.py; Frame reproduces kframe.encode token for token.
const FramingContract = "kestrel-framing-v1"

// Special ids and the sequence limit of the framing.
const (
	ClsID     = 50281
	SepID     = 50282
	PadID     = 50283
	MaxTokens = 512 // [CLS] tag context [SEP] text [SEP]
)

const (
	frameCtxMin      = 64   // the context keeps at least this many tokens, if it has them
	frameTextCharCap = 4096 // longer texts keep their first and last 2,048 code points
	frameCtxCharCap  = 512  // non-human segments tokenize only the context's last 512 code points
	frameCtxTokenCap = 512  // human segments keep only the context's last 512 tokens
	frameHeadFrac    = 0.7  // a truncated text keeps int(0.7 * budget) head tokens
)

// FrameInput is one item to label. Sender is the unit's sender_role (user,
// assistant, agent, tool), not its sender identity.
type FrameInput struct {
	Segment string
	Cohort  string
	Sender  string
	Context string
	Text    string
}

// HumanSegment reports whether segment is one of the human segments, whose
// context is cut by tokens rather than by code points.
func HumanSegment(segment string) bool {
	switch segment {
	case "H-typed", "H-typed+ctx", "H-template", "H-resent", "H-feedback":
		return true
	}
	return false
}

// Frame returns the model input for one item, unpadded:
// [CLS] tag context [SEP] text [SEP]. Lengths and cuts of the strings count
// code points, as Python does.
func (t *Tokenizer) Frame(in FrameInput) []int32 {
	text, ctx := validUTF8(in.Text), validUTF8(in.Context)
	if utf8.RuneCountInString(text) > frameTextCharCap {
		text = headRunes(text, frameTextCharCap/2) + " … " + tailRunes(text, frameTextCharCap/2)
	}
	human := HumanSegment(in.Segment)
	if !human {
		ctx = tailRunes(ctx, frameCtxCharCap)
	}
	tag := t.Encode(senderTag(in.Cohort, in.Sender))
	txt := t.Encode(text)
	var c []int32
	if ctx != "" {
		c = t.Encode(ctx)
	}
	if human && len(c) > frameCtxTokenCap {
		c = c[len(c)-frameCtxTokenCap:]
	}

	budget := MaxTokens - 3 - len(tag)
	cb := min(len(c), max(frameCtxMin, budget-len(txt)))
	tb := budget - cb
	head, tail := txt, []int32(nil)
	if len(txt) > tb {
		// Python's int(tb * 0.7): float64 product, truncated toward zero.
		h := int(float64(tb) * frameHeadFrac)
		if tb-h > 0 {
			head, tail = txt[:h], txt[len(txt)-(tb-h):]
		} else {
			head = pyHead(txt, tb)
		}
	}
	c = c[len(c)-cb:]

	out := make([]int32, 0, 3+len(tag)+len(c)+len(head)+len(tail))
	out = append(out, ClsID)
	out = append(out, tag...)
	out = append(out, c...)
	out = append(out, SepID)
	out = append(out, head...)
	out = append(out, tail...)
	return append(out, SepID)
}

// senderTag is f"{cohort} {sender}".strip() + " :", with an account:* sender
// shown as account.
func senderTag(cohort, sender string) string {
	if strings.HasPrefix(sender, "account:") {
		sender = "account"
	}
	return strings.TrimFunc(cohort+" "+sender, pyIsSpace) + " :"
}

// pyIsSpace is Python's str.isspace for one character: White_Space plus the
// information separators U+001C..U+001F.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1C && r <= 0x1F)
}

// pyHead is Python's s[:n], where a negative n counts from the end. Only a
// tag longer than the budget makes n negative.
func pyHead(s []int32, n int) []int32 {
	if n < 0 {
		n = max(0, len(s)+n)
	}
	return s[:min(n, len(s))]
}

// headRunes is s[:n] in code points.
func headRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// tailRunes is s[-n:] in code points.
func tailRunes(s string, n int) string {
	i := len(s)
	for ; n > 0 && i > 0; n-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:]
}
