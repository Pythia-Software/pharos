package labeler

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// kestrelFixture is testdata/labeler/kestrel-68m-parity.json.gz: invented
// strings with the reference's token ids, logits, probabilities and labels.
type kestrelFixture struct {
	Cases []struct {
		Case     string     `json:"case"`
		Item     FrameInput `json:"item"`
		InputIDs []int32    `json:"input_ids"`
		Int8     struct {
			Logits [][]float32 `json:"logits"`
			Probs  []float32   `json:"probs"`
			Labels []string    `json:"labels"`
		} `json:"int8"`
	} `json:"cases"`
}

func loadKestrelFixture(t *testing.T) kestrelFixture {
	t.Helper()
	var fixture kestrelFixture
	if err := json.Unmarshal(gunzipFile(t, filepath.Join("..", "..", "testdata", "labeler", "kestrel-68m-parity.json.gz")), &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// The real model through a real worker gives the reference's numbers and
// labels for every fixture case, batched as Pharos batches. It needs the
// model directory, a worker and its runtime, so it is skipped without them.
func TestKestrelParityWithWorker(t *testing.T) {
	dir, worker := os.Getenv("PHAROS_LABELER_MODEL_DIR"), os.Getenv("PHAROS_LABELER_WORKER")
	if dir == "" || worker == "" {
		t.Skip("set PHAROS_LABELER_MODEL_DIR (kestrel-68m), PHAROS_LABELER_WORKER and PHAROS_ORT_LIBRARY to run the model parity test")
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
		Threads: 4, VocabSize: model.Tokenizer.VocabSize(), MaxBatch: 64, MaxSeq: MaxTokens}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fixture := loadKestrelFixture(t)
	seqs := make([][]int32, len(fixture.Cases))
	lengths := make([]int, len(fixture.Cases))
	for i, c := range fixture.Cases {
		seqs[i] = model.Tokenizer.Frame(c.Item)
		lengths[i] = len(seqs[i])
	}
	maxProb, maxLogit := 0.0, 0.0
	for _, batch := range PackBatches(lengths, 16384, 32, 8) {
		rows := [][]int32{}
		for _, index := range batch {
			rows = append(rows, seqs[index])
		}
		result, err := pool.Run(context.Background(), BatchTensors(rows, 8), nil)
		if err != nil {
			t.Fatal(err)
		}
		probs, logits := result.Tensor("probs"), result.Tensor("logits")
		labels := model.Labels()
		for row, index := range batch {
			c := fixture.Cases[index]
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
	t.Logf("%d cases: max |Δp| %.2g, max |Δlogit| %.2g", len(fixture.Cases), maxProb, maxLogit)
	if maxProb > 1e-4 || maxLogit > 1e-3 {
		t.Fatalf("max |Δp| %.3g (tolerance 1e-4), max |Δlogit| %.3g (1e-3)", maxProb, maxLogit)
	}
}
