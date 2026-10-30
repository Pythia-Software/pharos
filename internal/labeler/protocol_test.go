package labeler

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunRoundTrip(t *testing.T) {
	req := &RunRequest{ID: 42, Outputs: []string{"probs"}, Inputs: []Tensor{
		{Name: "input_ids", DType: DTypeInt32, Dims: []int{2, 3}, Int32: []int32{50281, 7, 50282, 50281, 50282, 50283}},
		{Name: "attention_mask", DType: DTypeInt32, Dims: []int{2, 3}, Int32: []int32{1, 1, 1, 1, 1, 0}},
	}}
	payload, err := EncodeRun(req)
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if err := WriteFrame(&stream, FrameRun, payload); err != nil {
		t.Fatal(err)
	}
	kind, body, err := ReadFrame(&stream, 0)
	if err != nil || kind != FrameRun {
		t.Fatalf("frame %q %v", kind, err)
	}
	got, err := DecodeRun(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || len(got.Inputs) != 2 || got.Outputs[0] != "probs" || got.Inputs[1].Int32[5] != 0 || got.Inputs[0].Dims[1] != 3 {
		t.Fatalf("decoded %+v", got)
	}
	if err := ValidateRun(got, 50368, 8, 512); err != nil {
		t.Fatal(err)
	}
	res := &RunResult{ID: 42, Outputs: []Tensor{{Name: "probs", DType: DTypeFloat32, Dims: []int{2, 2}, Float32: []float32{0.5, -1.25, 3e-9, 1}}}}
	payload, err = EncodeResult(res)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeResult(payload)
	if err != nil || back.Tensor("probs").Float32[1] != -1.25 || back.Tensor("probs").Float32[2] != 3e-9 {
		t.Fatalf("result %+v %v", back, err)
	}
	if _, _, err := ReadFrame(&stream, 0); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream: %v", err)
	}
}

func TestMalformedFramesAndRequests(t *testing.T) {
	// A length of zero or past the limit cannot be resynchronized.
	for _, raw := range [][]byte{{0, 0, 0, 0}, {0xff, 0xff, 0xff, 0x7f, 'R'}} {
		if _, _, err := ReadFrame(bytes.NewReader(raw), 0); !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("%v: %v", raw, err)
		}
	}
	if _, _, err := ReadFrame(bytes.NewReader([]byte{5, 0, 0, 0, 'R', 1}), 0); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated frame: %v", err)
	}
	good := func() *RunRequest {
		return &RunRequest{ID: 7, Inputs: []Tensor{
			{Name: "input_ids", DType: DTypeInt32, Dims: []int{1, 2}, Int32: []int32{1, 2}},
			{Name: "attention_mask", DType: DTypeInt32, Dims: []int{1, 2}, Int32: []int32{1, 1}},
		}}
	}
	payload, _ := EncodeRun(good())
	// Every truncation of a valid payload fails to decode, never panics, and
	// keeps the id once its eight bytes are there.
	for n := 0; n < len(payload); n++ {
		req, err := DecodeRun(payload[:n])
		if err == nil {
			t.Fatalf("truncated to %d bytes decoded", n)
		}
		if n >= 8 && req.ID != 7 {
			t.Fatalf("id lost at %d bytes", n)
		}
	}
	if _, err := DecodeRun(append(payload, 0)); err == nil {
		t.Fatal("trailing byte accepted")
	}
	// Dims claiming more elements than the payload holds are refused before
	// anything is allocated.
	huge := append([]byte{}, payload[:8]...)
	huge = append(huge, 1, 0, 1, 'x', DTypeInt32, 2, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	if _, err := DecodeRun(huge); err == nil {
		t.Fatal("oversized dims accepted")
	}
	cases := map[string]func(*RunRequest){
		"id out of vocab": func(r *RunRequest) { r.Inputs[0].Int32[1] = 60000 },
		"negative id":     func(r *RunRequest) { r.Inputs[0].Int32[0] = -1 },
		"mask value":      func(r *RunRequest) { r.Inputs[1].Int32[0] = 2 },
		"empty row":       func(r *RunRequest) { r.Inputs[1].Int32 = []int32{0, 0} },
		"missing mask":    func(r *RunRequest) { r.Inputs = r.Inputs[:1] },
		"unknown input":   func(r *RunRequest) { r.Inputs[1].Name = "token_type_ids" },
		"shape mismatch":  func(r *RunRequest) { r.Inputs[1].Dims = []int{2, 1} },
		"float ids": func(r *RunRequest) {
			r.Inputs[0] = Tensor{Name: "input_ids", DType: DTypeFloat32, Dims: []int{1, 2}, Float32: []float32{1, 2}}
		},
		"over batch limit": func(r *RunRequest) {},
		"duplicate input":  func(r *RunRequest) { r.Inputs = append(r.Inputs, r.Inputs[0]) },
		"duplicate output": func(r *RunRequest) { r.Outputs = []string{"probs", "probs"} },
	}
	for name, mutate := range cases {
		req := good()
		mutate(req)
		limit := 8
		if name == "over batch limit" {
			limit = 0
			req.Inputs[0].Dims, req.Inputs[1].Dims = []int{2, 1}, []int{2, 1}
		}
		maxBatch := limit
		if name == "over batch limit" {
			maxBatch = 1
		}
		if err := ValidateRun(req, 50368, maxBatch, 512); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
