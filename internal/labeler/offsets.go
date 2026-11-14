package labeler

import (
	"sort"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ByteRange is a half-open range of bytes in a string.
type ByteRange struct{ Start, End int }

// EncodeOffsets is Encode with each token's range in s, in bytes, as the
// library's offset_mapping reports them in code points:
//
//   - a token's range runs from the start of the character holding its first
//     byte to the end of the character holding its last, so the tokens of one
//     character outside the merged vocabulary (most emoji, "⟦") all report
//     that character, and ranges may overlap;
//   - the space before a word belongs to the word's token;
//   - an added token reports the text it matched;
//   - where NFC changed the text, a composed character reports the first
//     character it was composed from (the marks that joined it are in no
//     token's range unless a token runs past them), and the characters after
//     it in its normalization segment report those that follow, in order: the
//     library's alignments for composition and reordering.
//
// s must be valid UTF-8: invalid bytes are replaced first, as in Encode, and
// the ranges then refer to the replaced string.
func (t *Tokenizer) EncodeOffsets(s string) ([]int32, []ByteRange) {
	s = validUTF8(s)
	ids := make([]int32, 0, len(s)/3+4)
	offsets := make([]ByteRange, 0, cap(ids))
	var raw, normed [16]piece
	at := 0
	for _, p := range t.rawTrie.split(s, t.added, raw[:0]) {
		base := at
		at += len(p.s)
		if p.id >= 0 {
			ids, offsets = append(ids, p.id), append(offsets, ByteRange{base, at})
			continue
		}
		text := t.normalize(p.s)
		var align alignment
		if text != p.s {
			align = nfcAlignment(p.s, text)
		}
		pos := 0
		for _, q := range t.normTrie.split(text, t.added, normed[:0]) {
			start := pos
			pos += len(q.s)
			if q.id >= 0 {
				ids, offsets = append(ids, q.id), append(offsets, align.source(base, start, pos))
				continue
			}
			for rest := q.s; len(rest) > 0; {
				n := preTokenLen(rest)
				first := len(ids)
				ids = t.bpe(rest[:n], ids)
				total := 0
				for _, id := range ids[first:] {
					total += int(t.tokenBytes[id])
				}
				from := start
				for _, id := range ids[first:] {
					if total != n {
						// A byte the vocabulary lacks was dropped: the
						// pre-token's tokens cannot be told apart.
						offsets = append(offsets, align.source(base, start, start+n))
						continue
					}
					to := from + int(t.tokenBytes[id])
					lo, hi := from, to
					for lo > 0 && !utf8.RuneStart(text[lo]) {
						lo--
					}
					for hi < len(text) && !utf8.RuneStart(text[hi]) {
						hi++
					}
					offsets = append(offsets, align.source(base, lo, hi))
					from = to
				}
				start += n
				rest = rest[n:]
			}
		}
	}
	return ids, offsets
}

// alignment maps ranges of a normalized piece back to the piece it came
// from. nil is the identity.
type alignment []alignedSegment

// alignedSegment is one stretch of the normalized text and its source. Equal
// stretches map byte for byte; in a changed segment each normalized character
// is a stretch of its own that maps as a whole.
type alignedSegment struct {
	norm, source ByteRange
	same         bool
}

// source is the range of the original string (whose piece starts at base)
// that the normalized range [start, end) came from.
func (a alignment) source(base, start, end int) ByteRange {
	if a == nil || end <= start {
		return ByteRange{base + start, base + end}
	}
	find := func(pos int) alignedSegment {
		i := sort.Search(len(a), func(i int) bool { return a[i].norm.End > pos })
		return a[min(i, len(a)-1)]
	}
	first, last := find(start), find(end-1)
	out := ByteRange{base + first.source.Start, base + last.source.End}
	if first.same {
		out.Start += start - first.norm.Start
	}
	if last.same {
		out.End = base + last.source.Start + end - last.norm.Start
	}
	return out
}

// nfcAlignment aligns text, the normalized form of piece, with piece by
// normalization segments. When the tokenizer's NFC and x/text's disagree on
// the piece (nfcSlow), the whole piece is one changed segment.
func nfcAlignment(piece, text string) alignment {
	var out alignment
	var iter norm.Iter
	iter.InitString(norm.NFC, piece)
	at, pos := 0, 0
	for !iter.Done() {
		segment := iter.Next()
		next := iter.Pos()
		if pos+len(segment) > len(text) || text[pos:pos+len(segment)] != string(segment) {
			return alignment{{norm: ByteRange{0, len(text)}, source: ByteRange{0, len(piece)}}}
		}
		same := piece[at:next] == string(segment)
		switch n := len(out); {
		case same && n > 0 && out[n-1].same:
			out[n-1].norm.End, out[n-1].source.End = pos+len(segment), next
		case same:
			out = append(out, alignedSegment{norm: ByteRange{pos, pos + len(segment)}, source: ByteRange{at, next}, same: true})
		default:
			// The segment's first character takes its first source character
			// and absorbs the ones composition removed.
			sources := []ByteRange{}
			for i := at; i < next; {
				_, size := utf8.DecodeRuneInString(piece[i:next])
				sources = append(sources, ByteRange{i, i + size})
				i += size
			}
			removed := max(0, len(sources)-utf8.RuneCount(segment))
			for i, k := 0, 0; i < len(segment); k++ {
				_, size := utf8.DecodeRune(segment[i:])
				j := 0
				if k > 0 {
					j = min(k+removed, len(sources)-1)
				}
				out = append(out, alignedSegment{norm: ByteRange{pos + i, pos + i + size}, source: sources[j]})
				i += size
			}
		}
		at, pos = next, pos+len(segment)
	}
	if pos != len(text) || len(out) == 0 {
		return alignment{{norm: ByteRange{0, len(text)}, source: ByteRange{0, len(piece)}}}
	}
	return out
}
