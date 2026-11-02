package labeler

import (
	"slices"
	"testing"
)

// A model's contract can add a role without touching the batcher or the
// validator: here a sentence-number input, filled from a framing's extra
// spans and checked by composed validators.
func TestRegisteredRoleFillsAndValidates(t *testing.T) {
	RegisterRole("test-sentence", Role{
		Fill: func(row Framed, values []int32) {
			for k, span := range row.Extra["sentences"] {
				for col := span.Start; col < span.End; col++ {
					values[col] = int32(k + 1)
				}
			}
		},
		Validate: AllOf(ValuesIn(0, 16), ZeroWithoutAttention, ContiguousRuns),
	})
	t.Cleanup(func() {
		rolesMu.Lock()
		delete(roles, "test-sentence")
		rolesMu.Unlock()
	})
	inputs := append(slices.Clone(DefaultInputs), InputSpec{Name: "sentence_ids", Role: "test-sentence"})
	if err := ValidateInputs(inputs); err != nil {
		t.Fatal(err)
	}
	row := Framed{IDs: []int32{ClsID, 5, 6, 7, 8, SepID}, Extra: map[string][]Span{"sentences": {{1, 3}, {3, 5}}}}
	tensors := BatchInputs(inputs, []Framed{row}, 8)
	if !slices.Equal(tensors[2].Int32, []int32{0, 1, 1, 2, 2, 0, 0, 0}) {
		t.Fatalf("sentence ids %v", tensors[2].Int32)
	}
	if err := ValidateRun(&RunRequest{Inputs: tensors}, inputs, 50368, 8, 512); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][]int32{
		"value out of range": {0, 1, 1, 17, 17, 0, 0, 0},
		"on padding":         {0, 1, 1, 2, 2, 0, 3, 0},
		"split run":          {0, 1, 2, 1, 2, 0, 0, 0},
	} {
		bad := BatchInputs(inputs, []Framed{row}, 8)
		bad[2].Int32 = values
		if ValidateRun(&RunRequest{Inputs: bad}, inputs, 50368, 8, 512) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ValidateInputs(append(slices.Clone(DefaultInputs), InputSpec{Name: "x", Role: "unregistered"})) == nil {
		t.Error("an unregistered role was accepted")
	}
}

// The segment role numbers a framing's sentences by their place in its
// window, so a sentence left out leaves its number unused.
func TestSegmentRoleNumbersSentences(t *testing.T) {
	inputs := InputContracts[InputContractIDsAttentionPoolSegments].Inputs
	row := Framed{IDs: []int32{ClsID, 5, 6, 7, 8, 9, SepID}, Spans: FrameSpans{Text: Span{3, 5}},
		Extra: map[string][]Span{SegmentSpans: {{1, 3}, {}, {3, 5}, {5, 6}}}}
	tensors := BatchInputs(inputs, []Framed{row}, 8)
	if !slices.Equal(tensors[3].Int32, []int32{0, 1, 1, 3, 3, 4, 0, 0}) || !slices.Equal(tensors[2].Int32, []int32{0, 0, 0, 1, 1, 0, 0, 0}) {
		t.Fatalf("segments %v, pool %v", tensors[3].Int32, tensors[2].Int32)
	}
	if err := ValidateRun(&RunRequest{Inputs: tensors}, inputs, 50368, 8, 768); err != nil {
		t.Fatal(err)
	}
	tensors[3].Int32[1] = MaxSegments + 1
	if ValidateRun(&RunRequest{Inputs: tensors}, inputs, 50368, 8, 768) == nil {
		t.Fatal("a segment number past MaxSegments was accepted")
	}
}
