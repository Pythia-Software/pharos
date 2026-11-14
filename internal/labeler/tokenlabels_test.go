package labeler

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tokenLabelFixture is testdata/labeler/token-labels-parity.json.gz: what the
// NUTHATCH reference (span_framing.py, the tokenizers library and ONNX
// Runtime) makes of invented texts. tools/engagement_parity_fixture.py
// writes it.
type tokenLabelFixture struct {
	Labels          []string `json:"labels"`
	Floor           float32  `json:"floor"`
	ModelSHA256     string   `json:"model_sha256"`
	TokenizerSHA256 string   `json:"tokenizer_sha256"`
	MaxSeq          int      `json:"max_seq_tokens"`
	MaxContext      int      `json:"max_context_tokens"`
	Overlap         int      `json:"window_overlap_tokens"`
	Cls             int32    `json:"cls_id"`
	Sep             int32    `json:"sep_id"`
	Small           struct {
		MaxSeq     int `json:"max_seq"`
		MaxContext int `json:"max_context"`
		Overlap    int `json:"overlap"`
	} `json:"small"`
	Cases []tokenLabelCase `json:"cases"`
}

type tokenLabelCase struct {
	Name         string              `json:"name"`
	Context      string              `json:"context"`
	Text         string              `json:"text"`
	ContextIDs   []int32             `json:"context_ids"`
	TextIDs      []int32             `json:"text_ids"`
	ByteOffsets  [][2]int            `json:"byte_offsets"`
	Windows      []tokenLabelWindow  `json:"windows"`
	SmallWindows []tokenLabelWindow  `json:"small_windows"`
	Entries      [][3]float64        `json:"entries"`
	Spans        []tokenLabelRefSpan `json:"spans"`
}

type tokenLabelWindow struct {
	Tokens  int    `json:"tokens"`
	TextPos int    `json:"text_pos"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	IDsSHA  string `json:"ids_sha"`
}

type tokenLabelRefSpan struct {
	Label     string  `json:"label"`
	StartByte int     `json:"start_byte"`
	EndByte   int     `json:"end_byte"`
	Score     float32 `json:"score"`
}

func loadTokenLabelFixture(t testing.TB) *tokenLabelFixture {
	t.Helper()
	file, err := os.Open("../../testdata/labeler/token-labels-parity.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var fixture tokenLabelFixture
	if err := json.NewDecoder(zr).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.TokenizerSHA256 != kestrelTokenizerSHA256 {
		t.Fatalf("the fixture was made with tokenizer %s; the committed tokenizer is %s", fixture.TokenizerSHA256, kestrelTokenizerSHA256)
	}
	return &fixture
}

func (c tokenLabelCase) entries() []TokenLabel {
	out := make([]TokenLabel, len(c.Entries))
	for i, entry := range c.Entries {
		out[i] = TokenLabel{Token: int32(entry[0]), Label: int32(entry[1]), Prob: float32(entry[2])}
	}
	// The fixture lists them by label, then token: MergeWindows' order.
	return out
}

func windowsMatch(got []TokenWindow, want []tokenLabelWindow) error {
	if len(got) != len(want) {
		return fmt.Errorf("%d windows, reference %d", len(got), len(want))
	}
	for k, window := range got {
		parts := make([]string, len(window.IDs))
		for i, id := range window.IDs {
			parts[i] = fmt.Sprint(id)
		}
		sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
		ref := want[k]
		if len(window.IDs) != ref.Tokens || window.TextPos != ref.TextPos || window.Start != ref.Start || window.End != ref.End || hex.EncodeToString(sum[:])[:16] != ref.IDsSHA {
			return fmt.Errorf("window %d is %d tokens, text at %d, tokens %d..%d; reference %+v (or its ids differ)", k, len(window.IDs), window.TextPos, window.Start, window.End, ref)
		}
	}
	return nil
}

// The tokenizer's ids and byte offsets, the windows and the decoded spans
// are the reference's on every fixture case: emoji, markers, leading and
// repeated spaces, added tokens, text that is not NFC, and texts of several
// windows.
func TestTokenLabelParity(t *testing.T) {
	fixture := loadTokenLabelFixture(t)
	tok := testTokenizer(t)
	thresholds := make([]float32, len(fixture.Labels))
	for i := range thresholds {
		thresholds[i] = TokenLabelThreshold
	}
	several := 0
	for _, c := range fixture.Cases {
		ids, offsets := tok.EncodeOffsets(c.Text)
		if !slices.Equal(ids, c.TextIDs) || !slices.Equal(ids, tok.Encode(c.Text)) {
			t.Errorf("%s: text ids differ from the reference's", c.Name)
			continue
		}
		for i, offset := range offsets {
			if want := c.ByteOffsets[i]; offset.Start != want[0] || offset.End != want[1] {
				t.Errorf("%s: token %d (%q) covers bytes %d..%d, reference %d..%d", c.Name, i, c.Text[want[0]:want[1]], offset.Start, offset.End, want[0], want[1])
				break
			}
		}
		context := tok.Encode(c.Context)
		if !slices.Equal(context, c.ContextIDs) {
			t.Errorf("%s: context ids differ from the reference's", c.Name)
		}
		windows := frameWindows(context, ids, fixture.Cls, fixture.Sep, fixture.MaxSeq, fixture.MaxContext, fixture.Overlap)
		if err := windowsMatch(windows, c.Windows); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
		if len(windows) > 1 {
			several++
		}
		small := frameWindows(context, ids, fixture.Cls, fixture.Sep, fixture.Small.MaxSeq, fixture.Small.MaxContext, fixture.Small.Overlap)
		if err := windowsMatch(small, c.SmallWindows); err != nil {
			t.Errorf("%s with %d-token windows: %v", c.Name, fixture.Small.MaxSeq, err)
		}
		spans := DecodeSpans(c.Text, offsets, c.entries(), thresholds)
		if len(spans) != len(c.Spans) {
			t.Errorf("%s: %d spans, reference %d", c.Name, len(spans), len(c.Spans))
			continue
		}
		for i, span := range spans {
			want := c.Spans[i]
			if fixture.Labels[span.Label] != want.Label || span.Start != want.StartByte || span.End != want.EndByte || math.Abs(float64(span.Score-want.Score)) > 1e-6 {
				t.Errorf("%s: span %d is %s %d..%d %.4f, reference %+v", c.Name, i, fixture.Labels[span.Label], span.Start, span.End, span.Score, want)
			}
		}
	}
	if several < 2 {
		t.Fatalf("the fixture has %d cases of several windows", several)
	}
}

// Windows merge by the larger probability, and positions outside a window's
// text never reach the item.
func TestMergeWindows(t *testing.T) {
	windows := []TokenWindow{{TextPos: 3, Start: 0, End: 5}, {TextPos: 3, Start: 3, End: 7}}
	merged := MergeWindows(windows, [][]TokenLabel{
		{{Token: 1, Label: 0, Prob: 0.9}, {Token: 6, Label: 2, Prob: 0.4}, {Token: 7, Label: 2, Prob: 0.8}, {Token: 8, Label: 2, Prob: 0.9}},
		{{Token: 2, Label: 2, Prob: 0.9}, {Token: 3, Label: 2, Prob: 0.7}, {Token: 4, Label: 2, Prob: 0.6}, {Token: 5, Label: 2, Prob: 0.6}, {Token: 6, Label: 1, Prob: 0.5}, {Token: 7, Label: 1, Prob: 0.9}},
	})
	want := []TokenLabel{{Token: 6, Label: 1, Prob: 0.5}, {Token: 3, Label: 2, Prob: 0.7}, {Token: 4, Label: 2, Prob: 0.8}, {Token: 5, Label: 2, Prob: 0.6}}
	if !slices.Equal(merged, want) {
		t.Fatalf("merged %v, want %v", merged, want)
	}
}

// A span is a run of adjacent tokens at or above the label's own threshold,
// trimmed of white space; its score is the run's largest probability.
func TestDecodeSpans(t *testing.T) {
	text := "do not  push yet"
	offsets := []ByteRange{{0, 2}, {2, 6}, {6, 7}, {7, 12}, {12, 16}}
	entries := []TokenLabel{
		{Token: 0, Label: 0, Prob: 0.6}, {Token: 1, Label: 0, Prob: 0.95}, {Token: 2, Label: 0, Prob: 0.7}, {Token: 4, Label: 0, Prob: 0.55},
		{Token: 2, Label: 1, Prob: 0.99}, {Token: 3, Label: 1, Prob: 0.45}, {Token: 4, Label: 1, Prob: 0.3},
	}
	spans := DecodeSpans(text, offsets, entries, []float32{0.5, 0.4})
	want := []LabelSpan{{Label: 0, Start: 0, End: 6, Score: 0.95}, {Label: 0, Start: 13, End: 16, Score: 0.55}, {Label: 1, Start: 8, End: 12, Score: 0.99}}
	if !slices.Equal(spans, want) {
		t.Fatalf("spans %+v, want %+v", spans, want)
	}
}

// The sparse form keeps attended positions at or above the floor, and
// round-trips through the wire's tensor.
func TestSparseOutputs(t *testing.T) {
	option, err := SparseOptionFrom(map[string]any{"sparse": map[string]any{"output": "logits", "activation": "sigmoid", "floor": 0.3}})
	if err != nil || option == nil {
		t.Fatal(option, err)
	}
	if none, err := SparseOptionFrom(map[string]any{"provider": "cpu"}); none != nil || err != nil {
		t.Fatal(none, err)
	}
	if _, err := SparseOptionFrom(map[string]any{"sparse": map[string]any{"output": "logits", "activation": "softmax"}}); err == nil {
		t.Fatal("an unknown activation was accepted")
	}
	// 2 rows × 3 tokens × 2 labels; the last token of row 1 is padding.
	dense := Tensor{Name: "logits", DType: DTypeFloat32, Dims: []int{2, 3, 2}, Float32: []float32{
		-5, 0, 2, -5, -0.9, -0.8,
		3, 3, -5, -5, 9, 9}}
	sparse, err := option.Sparsify(dense, []int32{1, 1, 1, 1, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeResult(&RunResult{ID: 1, Outputs: []Tensor{sparse}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(payload)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := SparseRows(result.Tensor("logits"), 2)
	if err != nil {
		t.Fatal(err)
	}
	sigmoid := func(z float64) float32 { return float32(1 / (1 + math.Exp(-z))) }
	want := [][]TokenLabel{
		{{Token: 0, Label: 1, Prob: 0.5}, {Token: 1, Label: 0, Prob: sigmoid(2)}, {Token: 2, Label: 1, Prob: sigmoid(-0.8)}},
		{{Token: 0, Label: 0, Prob: sigmoid(3)}, {Token: 0, Label: 1, Prob: sigmoid(3)}},
	}
	for row := range want {
		if !slices.Equal(rows[row], want[row]) {
			t.Fatalf("row %d: %v, want %v", row, rows[row], want[row])
		}
	}
	if _, err := option.Sparsify(Tensor{Name: "probs", DType: DTypeFloat32, Dims: []int{2, 4}, Float32: make([]float32, 8)}, make([]int32, 6)); err == nil {
		t.Fatal("a rank-2 output was reduced")
	}
}

// writeTokenLabelModel writes a model directory around the committed
// tokenizer and a stand-in model file.
func writeTokenLabelModel(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	dir := t.TempDir()
	tokenizer := kestrelTokenizerJSON(t)
	model := []byte("not an ONNX file")
	sum := sha256.Sum256(model)
	meta := map[string]any{
		"contract": InputContractIDsAttentionTokenLabels, "framing": TokenLabelFraming, "source_model": "models/tiny",
		"labels": []string{"bounds.prohibition", "intent.task"}, "max_seq_tokens": 1024, "max_context_tokens": 128, "window_overlap_tokens": 128,
		"cls_id": ClsID, "sep_id": SepID, "pad_id": PadID,
		"variants": map[string]string{"fp32": "model.onnx"},
		"files":    map[string]any{"model.onnx": map[string]any{"sha256": hex.EncodeToString(sum[:])}, "tokenizer.json": map[string]any{"sha256": kestrelTokenizerSHA256}},
	}
	if edit != nil {
		edit(meta)
	}
	raw, _ := json.Marshal(meta)
	for name, data := range map[string][]byte{"model.json": raw, "model.onnx": model, "tokenizer.json": tokenizer} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The label list, limits and thresholds come from model.json; a directory
// for another contract, framing or file is refused.
func TestLoadTokenLabelModel(t *testing.T) {
	model, err := LoadTokenLabelModel(writeTokenLabelModel(t, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(model.Labels, []string{"bounds.prohibition", "intent.task"}) || !slices.Equal(model.Thresholds, []float32{0.5, 0.5}) ||
		model.Name != "tiny" || model.MaxSeq != 1024 || model.MaxContext != 128 || model.Overlap != 128 || model.InputContract != InputContractIDsAttentionTokenLabels {
		t.Fatalf("%+v", model)
	}
	if !strings.HasPrefix(model.Identity(), "tiny/") || !strings.HasSuffix(model.Identity(), "/"+InputContractIDsAttentionTokenLabels+"/"+TokenLabelFraming) {
		t.Fatal(model.Identity())
	}
	tuned, err := LoadTokenLabelModel(writeTokenLabelModel(t, func(meta map[string]any) { meta["thresholds"] = map[string]float64{"intent.task": 0.35} }), "fp32")
	if err != nil || !slices.Equal(tuned.Thresholds, []float32{0.5, 0.35}) {
		t.Fatal(tuned, err)
	}
	for name, edit := range map[string]func(map[string]any){
		"contract": func(meta map[string]any) { meta["contract"] = "ids-attention-v1" },
		"framing":  func(meta map[string]any) { meta["framing"] = "nuthatch-span-framing-v2" },
		"labels":   func(meta map[string]any) { meta["labels"] = []string{} },
		"sha": func(meta map[string]any) {
			meta["files"].(map[string]any)["model.onnx"] = map[string]any{"sha256": "00"}
		},
		"windows":   func(meta map[string]any) { meta["window_overlap_tokens"] = 900 },
		"threshold": func(meta map[string]any) { meta["thresholds"] = []float64{0.5} },
		"pad":       func(meta map[string]any) { meta["pad_id"] = 0 },
	} {
		if _, err := LoadTokenLabelModel(writeTokenLabelModel(t, edit), ""); err == nil {
			t.Errorf("a model.json with a wrong %s loaded", name)
		}
	}
	if _, err := LoadTokenLabelModel(writeTokenLabelModel(t, nil), "int8"); err == nil {
		t.Error("a variant model.json does not name loaded")
	}
}

// TestTokenLabelParityWithWorker runs the fixture through a real worker and
// compares with the reference: every probability at or above the floor
// within 1e-3, and the same spans. It needs the model the fixture was made
// with, so it is skipped without it:
//
//	PHAROS_ENGAGEMENT_MODEL_DIR=… PHAROS_LABELER_WORKER=build/pharos-labeler-ort PHAROS_ORT_LIBRARY=… go test -run TokenLabelParityWithWorker ./internal/labeler
func TestTokenLabelParityWithWorker(t *testing.T) {
	dir, worker := os.Getenv("PHAROS_ENGAGEMENT_MODEL_DIR"), os.Getenv("PHAROS_LABELER_WORKER")
	if dir == "" || worker == "" {
		t.Skip("set PHAROS_ENGAGEMENT_MODEL_DIR (a token-label model directory), PHAROS_LABELER_WORKER and PHAROS_ORT_LIBRARY to run the token-label parity test")
	}
	fixture := loadTokenLabelFixture(t)
	model, err := LoadTokenLabelModel(dir, os.Getenv("PHAROS_ENGAGEMENT_VARIANT"))
	if err != nil {
		t.Fatal(err)
	}
	exact := model.SHA256 == fixture.ModelSHA256
	if !slices.Equal(model.Labels, fixture.Labels) {
		t.Skipf("model %s has another label list than the fixture's", model.Name)
	}
	args := []string{}
	if runtime := os.Getenv("PHAROS_ORT_LIBRARY"); runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	sparse := map[string]any{"sparse": SparseOption{Output: TokenLabelOutput, Activation: "sigmoid", Floor: 0.25}}
	runner, err := NewRunner(context.Background(), model.Model, RunnerConfig{Worker: worker, Args: args, Threads: 4, MaxBatch: 32, Options: sparse})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Pool().Close()
	rows := []Framed{}
	type item struct {
		offsets []ByteRange
		windows []TokenWindow
		first   int
	}
	items := []item{}
	for _, c := range fixture.Cases {
		ids, offsets := model.Tokenizer.EncodeOffsets(c.Text)
		it := item{offsets: offsets, windows: model.FrameWindows(model.Tokenizer.Encode(c.Context), ids), first: len(rows)}
		for _, window := range it.windows {
			rows = append(rows, Framed{IDs: window.IDs})
		}
		items = append(items, it)
	}
	// A small budget, so that rows of unlike lengths share batches and
	// padding is exercised.
	entries, err := runner.RunSparse(context.Background(), rows, TokenLabelOutput, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	worst := 0.0
	for k, c := range fixture.Cases {
		it := items[k]
		merged := MergeWindows(it.windows, entries[it.first:it.first+len(it.windows)])
		got := map[[2]int32]float32{}
		for _, entry := range merged {
			got[[2]int32{entry.Token, entry.Label}] = entry.Prob
		}
		for _, want := range c.entries() {
			diff := math.Abs(float64(got[[2]int32{want.Token, want.Label}] - want.Prob))
			worst = math.Max(worst, diff)
			if exact && diff > 1e-3 {
				t.Errorf("%s: token %d label %s is %.5f, reference %.5f", c.Name, want.Token, fixture.Labels[want.Label], got[[2]int32{want.Token, want.Label}], want.Prob)
			}
		}
		for _, entry := range merged {
			if entry.Prob >= fixture.Floor+1e-3 && !slices.ContainsFunc(c.entries(), func(want TokenLabel) bool { return want.Token == entry.Token && want.Label == entry.Label }) {
				t.Errorf("%s: token %d label %s is %.5f; the reference has it under %.2f", c.Name, entry.Token, fixture.Labels[entry.Label], entry.Prob, fixture.Floor)
			}
		}
		if !exact {
			continue
		}
		spans := DecodeSpans(c.Text, it.offsets, merged, model.Thresholds)
		same := len(spans) == len(c.Spans)
		for i := 0; same && i < len(spans); i++ {
			want := c.Spans[i]
			same = model.Labels[spans[i].Label] == want.Label && spans[i].Start == want.StartByte && spans[i].End == want.EndByte && math.Abs(float64(spans[i].Score-want.Score)) <= 1e-3
		}
		if !same {
			t.Errorf("%s: spans %+v, reference %+v", c.Name, spans, c.Spans)
		}
	}
	t.Logf("%d cases, %d windows; largest probability difference %.2g (model %s, exact reference: %t)", len(fixture.Cases), len(rows), worst, model.Identity(), exact)
}
