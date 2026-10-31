package labeler

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// modelFixture is a parity fixture of invented strings with the reference's
// token ids, text spans (pooling models), logits, probabilities and labels:
// testdata/labeler/kestrel-68m-parity.json.gz, or a model directory's own
// parity-fixture.json.
type modelFixture struct {
	InputContract string `json:"input_contract"`
	Cases         []struct {
		Case     string     `json:"case"`
		Item     FrameInput `json:"item"`
		InputIDs []int32    `json:"input_ids"`
		NTokens  int        `json:"n_tokens"`
		TextSpan []int      `json:"text_span"`
		Status   string     `json:"status"`
		Int8     struct {
			Logits [][]float32 `json:"logits"`
			Probs  []float32   `json:"probs"`
			Labels []string    `json:"labels"`
		} `json:"int8"`
	} `json:"cases"`
}

func loadModelFixture(t *testing.T, dir string) modelFixture {
	t.Helper()
	data := gunzipFile(t, filepath.Join("..", "..", "testdata", "labeler", "kestrel-68m-parity.json.gz"))
	if raw, err := os.ReadFile(filepath.Join(dir, "parity-fixture.json")); err == nil {
		data = raw
	}
	var fixture modelFixture
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	for i := range fixture.Cases {
		if c := &fixture.Cases[i]; c.NTokens > 0 && c.NTokens < len(c.InputIDs) {
			c.InputIDs = c.InputIDs[:c.NTokens]
		}
	}
	return fixture
}

// The real model through a real worker gives the reference's numbers and
// labels for every fixture case, batched as Pharos batches, with the inputs
// and padding the model's input contract declares: kestrel-68m
// (ids-attention-v1) and a target-pooling model (ids-attention-pool-v1)
// alike. It needs a model directory, a worker and its runtime, so it is
// skipped without them.
func TestModelParityWithWorker(t *testing.T) {
	dir, worker := os.Getenv("PHAROS_LABELER_MODEL_DIR"), os.Getenv("PHAROS_LABELER_WORKER")
	if dir == "" || worker == "" {
		t.Skip("set PHAROS_LABELER_MODEL_DIR (a model directory), PHAROS_LABELER_WORKER and PHAROS_ORT_LIBRARY to run the model parity test")
	}
	model, err := LoadModel(dir, "int8")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{}
	if runtime := os.Getenv("PHAROS_ORT_LIBRARY"); runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	pool, err := NewPool(context.Background(), PoolConfig{Worker: WorkerConfig{Path: worker, Args: args, Model: model.Path, ModelSHA256: model.SHA256,
		Threads: 4, VocabSize: model.Tokenizer.VocabSize(), MaxBatch: 64, MaxSeq: MaxTokens, Inputs: model.Inputs}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fixture := loadModelFixture(t, dir)
	rows := []Framed{}
	cases := []int{}
	empty := 0
	for i, c := range fixture.Cases {
		ids, spans := model.Tokenizer.FrameWithSpans(c.Item)
		if !slices.Equal(ids, c.InputIDs) {
			t.Errorf("%s: token ids differ from the reference's", c.Case)
		}
		if c.TextSpan != nil && (spans.Text.Start != c.TextSpan[0] || spans.Text.End != c.TextSpan[1]) {
			t.Errorf("%s: text span %+v, reference %v", c.Case, spans.Text, c.TextSpan)
		}
		if model.Pools() && spans.Text.Len() == 0 {
			// Nothing to pool: never sent (status empty_target).
			if c.Status != "empty_target" {
				t.Errorf("%s: an empty text span the reference labels", c.Case)
			}
			empty++
			continue
		}
		rows, cases = append(rows, Framed{IDs: ids, Spans: spans}), append(cases, i)
	}
	lengths := make([]int, len(rows))
	for i, row := range rows {
		lengths[i] = len(row.IDs)
	}
	maxProb, maxLogit := 0.0, 0.0
	for _, batch := range PackBatches(lengths, 16384, 32, model.PadMultiple) {
		framed := []Framed{}
		for _, index := range batch {
			framed = append(framed, rows[index])
		}
		result, err := pool.Run(context.Background(), BatchInputs(model.Inputs, framed, model.PadMultiple), []string{"probs", "logits"})
		if err != nil {
			t.Fatal(err)
		}
		probs, logits := result.Tensor("probs"), result.Tensor("logits")
		labels := model.Labels()
		for row, index := range batch {
			c := fixture.Cases[cases[index]]
			got := probs.Float32[row*labels : (row+1)*labels]
			for j, p := range got {
				maxProb = math.Max(maxProb, math.Abs(float64(p-c.Int8.Probs[j])))
			}
			heads := len(c.Int8.Logits)
			for h := range heads {
				for j := range labels {
					maxLogit = math.Max(maxLogit, math.Abs(float64(logits.Float32[(row*heads+h)*labels+j]-c.Int8.Logits[h][j])))
				}
			}
			names := model.LabelNames(model.Decide(got, c.Item.Segment))
			if !slices.Equal(names, c.Int8.Labels) {
				// Allowed only where a probability sits within 1e-4 of its threshold.
				row := model.Thresholds(c.Item.Segment)
				for j, p := range got {
					if (p >= row[j]) != (c.Int8.Probs[j] >= row[j]) && math.Abs(float64(p-row[j])) > 1e-4 {
						t.Errorf("%s: labels %v, reference %v", c.Case, names, c.Int8.Labels)
						break
					}
				}
			}
		}
	}
	t.Logf("%s, %s: %d cases (%d empty targets): max |Δp| %.2g, max |Δlogit| %.2g", model.Manifest.Name, model.InputContract, len(fixture.Cases), empty, maxProb, maxLogit)
	if maxProb > 1e-4 || maxLogit > 1e-3 {
		t.Fatalf("max |Δp| %.3g (tolerance 1e-4), max |Δlogit| %.3g (1e-3)", maxProb, maxLogit)
	}
}
