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
	// Pieces adjacent in the visible text but apart in the message (dropped
	// text between them) cannot be quoted as one.
	seam := labelingItemSource{id: "i3", text: "Helloworld", message: "Hello<x>world",
		targets: []labelingTargetPiece{{start: 0, end: 5, visibleStart: 0, visibleEnd: 5}, {start: 8, end: 13, visibleStart: 5, visibleEnd: 10}}}
	if got := locateLabelingQuote(seam, "L", "lowor").Status; got != "spans_pieces" {
		t.Fatalf("across a seam = %s", got)
	}
	joined := labelingItemSource{id: "i4", text: "Helloworld", message: "Helloworld",
		targets: []labelingTargetPiece{{start: 0, end: 5, visibleStart: 0, visibleEnd: 5}, {start: 5, end: 10, visibleStart: 5, visibleEnd: 10}}}
	if evidence := locateLabelingQuote(joined, "L", "lowor"); evidence.Status != "valid" || len(evidence.Occurrences[0].Source) != 2 {
		t.Fatalf("contiguous pieces = %+v", evidence)
	}
	// A quote in target text and again inside a marker: ambiguous_mixed,
	// which supports nothing; every occurrence recorded with its status.
	marker := "⟦quoted text, context only, 6 chars: \"fix it\"⟧"
	text := "fix it " + marker
	mixed := labelingItemSource{id: "i5", text: text, message: "fix it > fix it", markers: [][2]int{{7, len(text)}},
		targets: []labelingTargetPiece{{start: 0, end: 7, visibleStart: 0, visibleEnd: 7}}}
	evidence = locateLabelingQuote(mixed, "L", "fix it")
	if evidence.Status != "ambiguous_mixed" || !evidence.Ambiguous || len(evidence.Occurrences) != 2 || evidence.Occurrences[0].Status != "valid" || evidence.Occurrences[1].Status != "in_marker" {
		t.Fatalf("mixed = %+v", evidence)
	}
	// The same holds for a quote also in the context.
	context := labelingItemSource{id: "i6", text: "fix it", message: "fix it", context: "please fix it",
		targets: []labelingTargetPiece{{start: 0, end: 6, visibleStart: 0, visibleEnd: 6}}}
	if got := locateLabelingQuote(context, "L", "fix it").Status; got != "ambiguous_mixed" {
		t.Fatalf("also in context = %s", got)
	}
}
