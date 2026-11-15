package labeler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Token-label models (input contract ids-attention-token-labels-v1) give
// every token a logit per label; spans of text are read off the tokens. The
// model directory is the export's own (model.json, not a manifest.json):
// LoadTokenLabelModel reads its label list, limits and special ids, so a
// retrained model with another label list is another directory and nothing
// else. The framing is the export's FRAMING.md (nuthatch-span-framing-v1),
// reproduced by FrameWindows, MergeWindows and DecodeSpans; span_framing.py
// in the study is the reference they are tested against.

// InputContractIDsAttentionTokenLabels is input_ids and attention_mask in,
// logits [batch, tokens, labels] out, one independent sigmoid per label.
const InputContractIDsAttentionTokenLabels = "ids-attention-token-labels-v1"

// TokenLabelFraming names the framing this build implements for the contract.
const TokenLabelFraming = "nuthatch-span-framing-v1"

// TokenLabelOutput is the contract's output.
const TokenLabelOutput = "logits"

// TokenLabelModel is a verified token-label model directory.
type TokenLabelModel struct {
	// Model is what a Runner needs: the variant's file and SHA-256, the
	// tokenizer, the inputs and the limits.
	*Model
	// Name identifies the export (model.json's name, else its source
	// directory's); Framing and Codebook are the export's.
	Name, Framing, Codebook string
	// Labels are the output's labels, in order; Thresholds each label's
	// decision threshold (0.5 unless the export tunes them).
	Labels     []string
	Thresholds []float32
	// MaxContext context tokens are kept, and consecutive windows share
	// Overlap text tokens. Cls, Sep are the special ids of the framing.
	MaxContext, Overlap int
	Cls, Sep            int32
}

// tokenLabelFile is model.json, the parts read here.
type tokenLabelFile struct {
	Name        string   `json:"name"`
	SourceModel string   `json:"source_model"`
	Contract    string   `json:"contract"`
	Framing     string   `json:"framing"`
	Codebook    string   `json:"codebook"`
	Labels      []string `json:"labels"`
	MaxSeq      int      `json:"max_seq_tokens"`
	MaxContext  int      `json:"max_context_tokens"`
	Overlap     int      `json:"window_overlap_tokens"`
	Cls         *int32   `json:"cls_id"`
	Sep         *int32   `json:"sep_id"`
	Pad         *int32   `json:"pad_id"`
	MemoryMB    int      `json:"memory_mb"`
	Files       map[string]struct {
		SHA256 string `json:"sha256"`
	} `json:"files"`
	Variants map[string]string `json:"variants"`
	// Thresholds, when given, are per label: a list in label order or a map
	// by label (labels it leaves out decide at 0.5).
	Thresholds json.RawMessage `json:"thresholds"`
}

// TokenLabelThreshold is a label's threshold when the export tunes none.
const TokenLabelThreshold = 0.5

// LoadTokenLabelModel reads dir/model.json and verifies the variant's file
// ("fp32" when empty) and tokenizer.json against its SHA-256s. A directory
// for another contract or framing is refused.
func LoadTokenLabelModel(dir, variant string) (*TokenLabelModel, error) {
	if variant == "" {
		variant = "fp32"
	}
	unavailable := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrModelUnavailable}, args...)...)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "model.json"))
	if err != nil {
		return nil, unavailable("%v", err)
	}
	var file tokenLabelFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, unavailable("model.json: %v", err)
	}
	switch {
	case file.Contract != InputContractIDsAttentionTokenLabels:
		return nil, unavailable("the model has contract %q; this loader reads %q", file.Contract, InputContractIDsAttentionTokenLabels)
	case file.Framing != TokenLabelFraming:
		return nil, unavailable("the model needs framing %q; this build implements %q", file.Framing, TokenLabelFraming)
	case len(file.Labels) == 0 || len(file.Labels) > 4096:
		return nil, unavailable("model.json lists %d labels", len(file.Labels))
	case file.Cls == nil || file.Sep == nil || file.Pad == nil:
		return nil, unavailable("model.json lacks cls_id, sep_id or pad_id")
	case *file.Pad != PadID:
		// The ids role pads with PadID.
		return nil, unavailable("the model pads with id %d; this build pads with %d", *file.Pad, PadID)
	case file.MaxSeq < 8 || file.MaxSeq > 8192:
		return nil, unavailable("max_seq_tokens %d is outside 8..8192", file.MaxSeq)
	case file.MaxContext < 0 || file.Overlap < 0 || file.MaxSeq-3-file.MaxContext-file.Overlap <= 0:
		return nil, unavailable("windows of %d tokens leave no room after %d context and %d overlap tokens", file.MaxSeq, file.MaxContext, file.Overlap)
	}
	seen := map[string]bool{}
	for _, label := range file.Labels {
		if label == "" || seen[label] {
			return nil, unavailable("model.json's label %q is empty or repeated", label)
		}
		seen[label] = true
	}
	name := file.Variants[variant]
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, unavailable("model.json names no %s variant", variant)
	}
	contract := InputContracts[InputContractIDsAttentionTokenLabels]
	m := &TokenLabelModel{Name: file.Name, Framing: file.Framing, Codebook: file.Codebook, Labels: file.Labels,
		MaxContext: file.MaxContext, Overlap: file.Overlap, Cls: *file.Cls, Sep: *file.Sep,
		Model: &Model{Dir: dir, Variant: variant, Path: filepath.Join(dir, name), InputContract: InputContractIDsAttentionTokenLabels,
			Inputs: contract.Inputs, PadMultiple: contract.PadMultiple, MaxSeq: file.MaxSeq, Outputs: []string{TokenLabelOutput}, MemoryMB: file.MemoryMB}}
	if m.Name == "" {
		m.Name = filepath.Base(file.SourceModel)
	}
	if m.Name == "" || m.Name == "." || m.Name == "/" {
		m.Name = filepath.Base(dir)
	}
	m.Manifest.Name, m.Manifest.FramingContract, m.Manifest.InputContract = m.Name, file.Framing, file.Contract
	if m.MemoryMB <= 0 {
		// An fp32 68M encoder with 16k-token batches of 104 logits peaks
		// near 2 GB (docs/engagement-labels.md).
		m.MemoryMB = 2400
	}
	if m.SHA256, err = verifyFile(m.Path, file.Files[name].SHA256); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, unavailable("%v", err)
	}
	sum := sha256.Sum256(data)
	if m.TokenizerSHA256 = hex.EncodeToString(sum[:]); m.TokenizerSHA256 != file.Files["tokenizer.json"].SHA256 {
		return nil, unavailable("tokenizer.json has SHA-256 %s, model.json %q", m.TokenizerSHA256, file.Files["tokenizer.json"].SHA256)
	}
	if m.Tokenizer, err = LoadTokenizer(data); err != nil {
		return nil, unavailable("%v", err)
	}
	if m.Thresholds, err = tokenLabelThresholds(file.Thresholds, file.Labels); err != nil {
		return nil, unavailable("%v", err)
	}
	return m, nil
}

func tokenLabelThresholds(raw json.RawMessage, labels []string) ([]float32, error) {
	out := make([]float32, len(labels))
	for i := range out {
		out[i] = TokenLabelThreshold
	}
	if isNull(raw) {
		return out, nil
	}
	var list []float64
	var named map[string]float64
	switch {
	case json.Unmarshal(raw, &list) == nil:
		if len(list) != len(labels) {
			return nil, fmt.Errorf("model.json has %d thresholds for %d labels", len(list), len(labels))
		}
		for i, value := range list {
			out[i] = float32(value)
		}
	case json.Unmarshal(raw, &named) == nil:
		for label, value := range named {
			i := slices.Index(labels, label)
			if i < 0 {
				return nil, fmt.Errorf("model.json has a threshold for unknown label %q", label)
			}
			out[i] = float32(value)
		}
	default:
		return nil, fmt.Errorf("model.json's thresholds are neither a list nor a map by label")
	}
	for i, value := range out {
		if !(value > 0 && value < 1) {
			return nil, fmt.Errorf("the threshold of %s, %g, is outside (0,1)", labels[i], value)
		}
	}
	return out, nil
}

// Identity names what produced a span: the export, its file, the contract
// and the framing.
func (m *TokenLabelModel) Identity() string {
	return fmt.Sprintf("%s/%s/%s/%s", m.Name, m.SHA256[:16], m.InputContract, m.Framing)
}

// TokenWindow is one model input of an item: IDs is
// [CLS] context [SEP] text[Start:End] [SEP], and TextPos the position of text
// token Start in it.
type TokenWindow struct {
	IDs                 []int32
	TextPos, Start, End int
}

// FrameWindows is frame_windows: the windows of one item, from the ids of its
// context and its text, each tokenized on its own without special tokens.
// Only the last MaxContext context ids are kept; a window holds at most
// MaxSeq ids; consecutive windows share Overlap text tokens, and the last is
// the first to reach the end of the text. An empty text gives one window
// with no text tokens.
func (m *TokenLabelModel) FrameWindows(context, text []int32) []TokenWindow {
	return frameWindows(context, text, m.Cls, m.Sep, m.MaxSeq, m.MaxContext, m.Overlap)
}

func frameWindows(context, text []int32, cls, sep int32, maxSeq, maxContext, overlap int) []TokenWindow {
	if len(context) > maxContext {
		context = context[len(context)-maxContext:]
	}
	limit := maxSeq - 3 - len(context)
	step := limit - overlap
	head := make([]int32, 0, len(context)+2)
	head = append(append(append(head, cls), context...), sep)
	windows := []TokenWindow{}
	for start := 0; ; start += step {
		end := min(start+limit, len(text))
		ids := make([]int32, 0, len(head)+end-start+1)
		ids = append(append(append(ids, head...), text[start:end]...), sep)
		windows = append(windows, TokenWindow{IDs: ids, TextPos: len(head), Start: start, End: end})
		if end >= len(text) {
			return windows
		}
	}
}

// TokenLabel is one probability: of Label (an index into the model's labels)
// at Token. From a worker, Token is a position in a batch row; after
// MergeWindows, a text token of the item.
type TokenLabel struct {
	Token, Label int32
	Prob         float32
}

// MergeWindows is merge_windows over sparse entries: entries[k] are window
// k's (by position in the window), and the result is the item's, by text
// token, sorted by label and then token. Positions outside a window's text
// (its [CLS], context and [SEP]) are dropped, and a token two windows hold
// keeps the larger probability.
func MergeWindows(windows []TokenWindow, entries [][]TokenLabel) []TokenLabel {
	merged := []TokenLabel{}
	for k, window := range windows {
		for _, entry := range entries[k] {
			token := int(entry.Token) - window.TextPos
			if token < 0 || token >= window.End-window.Start {
				continue
			}
			merged = append(merged, TokenLabel{Token: int32(window.Start + token), Label: entry.Label, Prob: entry.Prob})
		}
	}
	sort.Slice(merged, func(a, b int) bool {
		if merged[a].Label != merged[b].Label {
			return merged[a].Label < merged[b].Label
		}
		if merged[a].Token != merged[b].Token {
			return merged[a].Token < merged[b].Token
		}
		return merged[a].Prob > merged[b].Prob
	})
	return slices.CompactFunc(merged, func(a, b TokenLabel) bool { return a.Label == b.Label && a.Token == b.Token })
}

// LabelSpan is a decoded span: Label's index, its bytes in the text, and its
// score, the largest probability among its tokens (as decode_spans reports;
// a mean would fall as a span grows over weaker tokens).
type LabelSpan struct {
	Label      int
	Start, End int
	Score      float32
}

// DecodeSpans is decode_spans over sparse entries (MergeWindows' order): for
// each label, each maximal run of consecutive text tokens at or above the
// label's threshold is one span, from the first token's start to the last
// token's end, with white space trimmed from both ends (a token carries the
// space before its word); empty spans are dropped. Spans come in label order,
// then text order.
func DecodeSpans(text string, offsets []ByteRange, entries []TokenLabel, thresholds []float32) []LabelSpan {
	spans := []LabelSpan{}
	for i := 0; i < len(entries); {
		first := entries[i]
		if int(first.Label) >= len(thresholds) || int(first.Token) >= len(offsets) || first.Prob < thresholds[first.Label] {
			i++
			continue
		}
		last, best := first, first.Prob
		for i++; i < len(entries) && entries[i].Label == first.Label && entries[i].Token == last.Token+1 &&
			int(entries[i].Token) < len(offsets) && entries[i].Prob >= thresholds[first.Label]; i++ {
			last, best = entries[i], max(best, entries[i].Prob)
		}
		start, end := offsets[first.Token].Start, offsets[last.Token].End
		start, end = TrimSpaceRange(text, start, end)
		if end > start {
			spans = append(spans, LabelSpan{Label: int(first.Label), Start: start, End: end, Score: best})
		}
	}
	return spans
}

// TrimSpaceRange narrows [start, end) of text past white space at both ends,
// as Python's str.isspace sees it.
func TrimSpaceRange(text string, start, end int) (int, int) {
	for start < end {
		r, size := utf8.DecodeRuneInString(text[start:end])
		if !pyIsSpace(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if !pyIsSpace(r) {
			break
		}
		end -= size
	}
	return start, end
}

// Sparse outputs. A token-label output is [batch, tokens, labels] float32:
// 104 values per token, nearly all of them near zero. A worker with the
// "sparse" capability, asked by the hello's "sparse" option, replaces such
// an output by the entries that matter: a float32 tensor of the same name
// with dims [n, 4], each row (batch row, token position, label, value) for an
// attended position whose value reaches the floor, in row, position, label
// order. Indices are exact in float32 (all below 2^24).

// SparseCapability is what a worker announces in Ready.Capabilities.
const SparseCapability = "sparse"

// SparseOption is Hello.Options["sparse"].
type SparseOption struct {
	// Output is the rank-3 output to reduce.
	Output string `json:"output"`
	// Activation is applied before the floor: "sigmoid", or "" for none.
	Activation string `json:"activation,omitempty"`
	// Floor is the lowest value returned.
	Floor float64 `json:"floor"`
}

// SparseOptionFrom reads a hello's sparse option (nil when it has none).
func SparseOptionFrom(options map[string]any) (*SparseOption, error) {
	value, ok := options["sparse"]
	if !ok || value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var option SparseOption
	if err := json.Unmarshal(raw, &option); err != nil {
		return nil, fmt.Errorf("sparse option: %w", err)
	}
	if option.Output == "" || option.Activation != "" && option.Activation != "sigmoid" || math.IsNaN(option.Floor) {
		return nil, fmt.Errorf("sparse option %s names no output or an unknown activation", raw)
	}
	return &option, nil
}

// Sparsify reduces a dense [batch, tokens, labels] output to its sparse form.
// attention is the request's mask, [batch, tokens].
func (o *SparseOption) Sparsify(dense Tensor, attention []int32) (Tensor, error) {
	if dense.DType != DTypeFloat32 || len(dense.Dims) != 3 || len(attention) != dense.Dims[0]*dense.Dims[1] {
		return Tensor{}, fmt.Errorf("output %s has dims %v: a sparse output is float32 [batch, tokens, labels] over the request's tokens", dense.Name, dense.Dims)
	}
	seq, labels := dense.Dims[1], dense.Dims[2]
	floor := float32(o.Floor)
	sigmoid := o.Activation == "sigmoid"
	if sigmoid {
		// Compare logits with the floor's logit, and take the sigmoid only
		// of the few that pass.
		switch {
		case o.Floor <= 0:
			floor = float32(math.Inf(-1))
		case o.Floor >= 1:
			floor = float32(math.Inf(1))
		default:
			floor = float32(math.Log(o.Floor / (1 - o.Floor)))
		}
	}
	values := []float32{}
	for at, attended := range attention {
		if attended == 0 {
			continue
		}
		for label, value := range dense.Float32[at*labels : (at+1)*labels] {
			if value < floor {
				continue
			}
			if sigmoid {
				value = float32(1 / (1 + math.Exp(-float64(value))))
			}
			values = append(values, float32(at/seq), float32(at%seq), float32(label), value)
		}
	}
	return Tensor{Name: dense.Name, DType: DTypeFloat32, Dims: []int{len(values) / 4, 4}, Float32: values}, nil
}

// SparseRows splits a sparse output into each batch row's entries.
func SparseRows(sparse *Tensor, batch int) ([][]TokenLabel, error) {
	if sparse == nil || sparse.DType != DTypeFloat32 || len(sparse.Dims) != 2 || sparse.Dims[1] != 4 {
		return nil, fmt.Errorf("the worker returned no sparse [n, 4] output")
	}
	rows := make([][]TokenLabel, batch)
	for k := 0; k+3 < len(sparse.Float32); k += 4 {
		row := int(sparse.Float32[k])
		if row < 0 || row >= batch {
			return nil, fmt.Errorf("sparse output names row %d of %d", row, batch)
		}
		rows[row] = append(rows[row], TokenLabel{Token: int32(sparse.Float32[k+1]), Label: int32(sparse.Float32[k+2]), Prob: sparse.Float32[k+3]})
	}
	return rows, nil
}
