package archive

import "testing"

func TestLabelingQuoteMapping(t *testing.T) {
	// Two target pieces with an untargeted space between them in the
	// visible text: a quote across it is not the person's words.
	item := labelingItemSource{id: "i", text: "Hello world", message: "Hello world",
		targets: []labelingTargetPiece{{start: 0, end: 5, visibleStart: 0, visibleEnd: 5}, {start: 6, end: 11, visibleStart: 6, visibleEnd: 11}}}
	if got := locateLabelingQuote(item, "L", "Hello world").Status; got != "outside_target" {
		t.Fatalf("across pieces = %s", got)
	}
	if got := locateLabelingQuote(item, "L", "world").Status; got != "valid" {
		t.Fatalf("inside a piece = %s", got)
	}
	// A later chunk: offsets map through its visible start, and a quote
	// crossing into the previous chunk is not in this item.
	chunk := labelingItemSource{id: "i1", text: "second part", message: "first part second part", visibleStart: 11,
		targets: []labelingTargetPiece{{start: 0, end: 22, visibleStart: 0, visibleEnd: 22}}}
	evidence := locateLabelingQuote(chunk, "L", "second")
	if evidence.Status != "valid" || evidence.Occurrences[0].Source[0]["start"] != 11 {
		t.Fatalf("chunk = %+v", evidence)
	}
	if got := locateLabelingQuote(chunk, "L", "part second").Status; got != "not_found" {
		t.Fatalf("across chunks = %s", got)
	}
	// A piece whose mapping lands inside a character is rejected.
	split := labelingItemSource{id: "i2", text: "\xa9b", message: "a\xc3\xa9b",
		targets: []labelingTargetPiece{{start: 2, end: 4, visibleStart: 0, visibleEnd: 2}}}
	if got := locateLabelingQuote(split, "L", "\xa9b").Status; got != "not_on_character_boundary" {
		t.Fatalf("off boundary = %s", got)
	}
}
