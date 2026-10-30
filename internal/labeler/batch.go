package labeler

import "sort"

// Batching: sequences are grouped by length so a batch wastes little on
// padding, and each batch's padded size (rows × padded length) stays under a
// token budget, so a batch of long items is small and one of short items is
// large.

// PadMultiple is the multiple every batch is padded to. Padding is not free
// of effect for the int8 model: its outputs change (by up to 0.05 in a
// probability) when the padded length is not a multiple of 4, as in the
// Python reference with the same runtime; at any multiple of 4 they are the
// fixture's to 5e-7. kestrel-framing-v1 says padding does not matter, which
// holds for fp32 only. Pharos pads to 8.
const PadMultiple = 8

// PadLength rounds n up to a multiple of mult.
func PadLength(n, mult int) int {
	if mult <= 1 {
		return n
	}
	return (n + mult - 1) / mult * mult
}

// PackBatches groups sequences (by their lengths) into batches: each a list
// of indices into lengths, in ascending length, with at most maxBatch rows
// and rows × padded length ≤ budget (a single sequence over the budget gets a
// batch of its own).
func PackBatches(lengths []int, budget, maxBatch, padMult int) [][]int {
	order := make([]int, len(lengths))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return lengths[order[a]] < lengths[order[b]] })
	batches := [][]int{}
	var current []int
	for _, index := range order {
		// Ascending order: this sequence sets the batch's padded length.
		padded := PadLength(lengths[index], padMult)
		if len(current) > 0 && ((len(current)+1)*padded > budget || maxBatch > 0 && len(current) >= maxBatch) {
			batches = append(batches, current)
			current = nil
		}
		current = append(current, index)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// BatchTensors right-pads sequences with PadID to the longest one (rounded up
// to padMult) and returns the run request's input_ids and attention_mask.
func BatchTensors(seqs [][]int32, padMult int) []Tensor {
	longest := 0
	for _, seq := range seqs {
		longest = max(longest, len(seq))
	}
	width := PadLength(longest, padMult)
	ids := make([]int32, len(seqs)*width)
	mask := make([]int32, len(seqs)*width)
	for row, seq := range seqs {
		at := row * width
		copy(ids[at:], seq)
		for col := range width {
			if col < len(seq) {
				mask[at+col] = 1
			} else {
				ids[at+col] = PadID
			}
		}
	}
	dims := []int{len(seqs), width}
	return []Tensor{
		{Name: "input_ids", DType: DTypeInt32, Dims: dims, Int32: ids},
		{Name: "attention_mask", DType: DTypeInt32, Dims: dims, Int32: mask},
	}
}
