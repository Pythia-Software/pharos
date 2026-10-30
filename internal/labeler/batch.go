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

// BatchTensors is BatchInputs for DefaultInputs: input_ids and
// attention_mask.
func BatchTensors(seqs [][]int32, padMult int) []Tensor {
	rows := make([]Framed, len(seqs))
	for i, seq := range seqs {
		rows[i].IDs = seq
	}
	return BatchInputs(DefaultInputs, rows, padMult)
}

// Framed is one framed item: its token ids, where kestrel-framing put its
// components, and any further spans another framing contract builds (by
// name, for its roles' fillers: sentence spans, say).
type Framed struct {
	IDs   []int32
	Spans FrameSpans
	Extra map[string][]Span
}

// BatchInputs makes a run request's inputs for rows, by the model's input
// contract: each input's role (see Role) fills its row; every input is
// padded to the longest row rounded up to padMult, with zeros (PadID for
// ids).
func BatchInputs(inputs []InputSpec, rows []Framed, padMult int) []Tensor {
	longest := 0
	for _, row := range rows {
		longest = max(longest, len(row.IDs))
	}
	width := PadLength(longest, padMult)
	dims := []int{len(rows), width}
	tensors := make([]Tensor, 0, len(inputs))
	for _, input := range inputs {
		values := make([]int32, len(rows)*width)
		if role, ok := lookupRole(input.Role); ok && role.Fill != nil {
			for r, row := range rows {
				role.Fill(row, values[r*width:(r+1)*width])
			}
		}
		tensors = append(tensors, Tensor{Name: input.Name, DType: DTypeInt32, Dims: dims, Int32: values})
	}
	return tensors
}
