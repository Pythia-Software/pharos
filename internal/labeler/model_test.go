package labeler

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func gunzipFile(t testing.TB, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeModelDir writes a model directory with three labels, P > P.request
// and C, the committed tokenizer, and a stand-in model file.
func writeModelDir(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	dir := t.TempDir()
	tokenizer := gunzipFile(t, filepath.Join("..", "..", "testdata", "labeler", "kestrel-68m-tokenizer.json.gz"))
	model := []byte("not really onnx")
	for name, data := range map[string][]byte{"tokenizer.json": tokenizer, "model.int8.onnx": model} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"name": "tiny", "framing_contract": FramingContract,
		"files":            map[string]string{"onnx_int8": "model.int8.onnx", "tokenizer": "tokenizer.json"},
		"sha256":           map[string]string{"model.int8.onnx": sha(model), "tokenizer.json": sha(tokenizer)},
		"tokenizer_sha256": sha(tokenizer),
		"labels": map[string]any{"order": []string{"P", "P.request", "C"}, "parent": []any{nil, "P", nil},
			"ancestors": map[string][]string{"P": {}, "P.request": {"P"}, "C": {}}},
		"thresholds": map[string][]float64{"default": {0.8, 0.62, 0.5}, "AR": {0.9, 0.9, 0.1}},
	}
	if mutate != nil {
		mutate(manifest)
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestModelDecidesClosedLabelSets(t *testing.T) {
	model, err := LoadModel(writeModelDir(t, nil), "int8")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		probs   []float32
		segment string
		want    []string
	}{
		// A child that is on adds its parent, even below the parent's threshold.
		{[]float32{0.1, 0.62, 0.2}, "H-typed", []string{"P", "P.request"}},
		// Exactly at a threshold, compared in float32 as the reference does.
		{[]float32{float32(0.8), 0, float32(0.5)}, "DEL", []string{"P", "C"}},
		{[]float32{0.85, 0.7, 0.2}, "AR", []string{"C"}},
		{[]float32{0, 0, 0}, "TH", []string{}},
	} {
		if got := model.LabelNames(model.Decide(c.probs, c.segment)); !slices.Equal(got, c.want) {
			t.Errorf("%v %s: %v, want %v", c.probs, c.segment, got, c.want)
		}
	}
}

func TestModelRefusesWhatDoesNotVerify(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"model checksum":     func(m map[string]any) { m["sha256"].(map[string]string)["model.int8.onnx"] = sha([]byte("other")) },
		"tokenizer checksum": func(m map[string]any) { m["tokenizer_sha256"] = sha([]byte("other")) },
		"framing contract":   func(m map[string]any) { m["framing_contract"] = "kestrel-framing-v2" },
		"missing variant":    func(m map[string]any) { delete(m["files"].(map[string]string), "onnx_int8") },
		"short thresholds":   func(m map[string]any) { m["thresholds"] = map[string][]float64{"default": {0.5}} },
		"input contract":     func(m map[string]any) { m["input_contract"] = "ids-attention-pool-v9" },
	} {
		if _, err := LoadModel(writeModelDir(t, mutate), "int8"); !errors.Is(err, ErrModelUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := LoadModel(t.TempDir(), ""); !errors.Is(err, ErrModelUnavailable) {
		t.Errorf("empty directory: %v", err)
	}
}

func TestPackBatchesKeepsTheTokenBudget(t *testing.T) {
	lengths := []int{512, 13, 40, 300, 13, 200, 90, 512, 64, 7}
	batches := PackBatches(lengths, 1024, 4, 8)
	seen := map[int]bool{}
	for _, batch := range batches {
		longest := 0
		for k, index := range batch {
			if seen[index] {
				t.Fatalf("index %d packed twice", index)
			}
			seen[index] = true
			if k > 0 && lengths[index] < lengths[batch[k-1]] {
				t.Fatalf("batch %v is not in length order", batch)
			}
			longest = max(longest, lengths[index])
		}
		if len(batch) > 4 || len(batch) > 1 && len(batch)*PadLength(longest, 8) > 1024 {
			t.Fatalf("batch %v breaks the budget", batch)
		}
	}
	if len(seen) != len(lengths) {
		t.Fatalf("packed %d of %d", len(seen), len(lengths))
	}
	tensors := BatchTensors([][]int32{{ClsID, 5, SepID}, {ClsID, SepID}}, 8)
	if tensors[0].Dims[1] != 8 || tensors[0].Int32[11] != PadID || tensors[1].Int32[2] != 1 || tensors[1].Int32[10] != 0 {
		t.Fatalf("tensors %+v", tensors)
	}
	// A pooling input covers each row's text span and nothing else.
	inputs := append(slices.Clone(DefaultInputs), InputSpec{Name: "pooling_mask", Role: RolePool})
	pooled := BatchInputs(inputs, []Framed{{IDs: []int32{ClsID, 9, SepID, 5, 6, SepID}, Spans: FrameSpans{Tag: Span{1, 2}, Context: Span{2, 2}, Text: Span{3, 5}}}}, 8)
	if !slices.Equal(pooled[2].Int32, []int32{0, 0, 0, 1, 1, 0, 0, 0}) || ValidateRun(&RunRequest{Inputs: pooled}, inputs, 50368, 8, 512) != nil {
		t.Fatalf("pooling mask %v", pooled[2].Int32)
	}
}
