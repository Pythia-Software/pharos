package labeler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Manifest is a model directory's manifest.json, the parts the labeler reads
// (docs/local-labeler.md, "The model artifact").
type Manifest struct {
	Name            string `json:"name"`
	BaseModel       string `json:"base_model"`
	FramingContract string `json:"framing_contract"`
	// InputContract names the model's inputs and how they are made
	// (InputContracts). Exports before the field (kestrel-68m) are
	// ids-attention-v1.
	InputContract string `json:"input_contract"`
	// Inputs, when given, name the model's inputs for the contract's roles;
	// otherwise the contract's default names are the model's.
	Inputs []InputSpec `json:"inputs"`
	// Padding.Multiple, when given, is the padded length's multiple the
	// model needs; the runtime pads to a multiple of it and the contract's.
	Padding struct {
		Multiple int `json:"multiple"`
	} `json:"padding"`
	Files           map[string]string `json:"files"`
	SHA256          map[string]string `json:"sha256"`
	TokenizerSHA256 string            `json:"tokenizer_sha256"`
	Labels          struct {
		Order     []string            `json:"order"`
		Parent    []*string           `json:"parent"`
		Ancestors map[string][]string `json:"ancestors"`
		Taxonomy  string              `json:"taxonomy"`
	} `json:"labels"`
	Heads  []string `json:"heads"`
	Scorer struct {
		Definition string `json:"definition"`
	} `json:"scorer"`
	Segments   []string             `json:"segments"`
	Thresholds map[string][]float64 `json:"thresholds"`
	ONNX       struct {
		// Outputs names the model's outputs: a map of name to description
		// or a list of names.
		Outputs json.RawMessage `json:"outputs"`
	} `json:"onnx"`
	// MaxSeq is the longest sequence the model takes (the framing's
	// maximum when absent: 512 for kestrel-framing-v1); PadMultiple a
	// padding multiple it needs beyond its contract's.
	MaxSeq      int `json:"max_seq"`
	PadMultiple int `json:"pad_multiple"`
	// MemoryMB estimates one worker's memory for this model, for the pool's
	// budget (default 1,600 MB, what kestrel-68m int8 peaks at with 16k-token
	// batches).
	MemoryMB int `json:"memory_mb"`
}

// Model is a verified model directory: its manifest, the chosen variant's
// file and SHA-256, the tokenizer, and the decision rule's tables.
type Model struct {
	Dir, Variant, Path string
	// SHA256 is the model file's, TokenizerSHA256 the tokenizer's: together
	// with the framing contract they identify what produced a label.
	SHA256, TokenizerSHA256 string
	Manifest                Manifest
	Tokenizer               *Tokenizer
	// InputContract and Inputs are what every run request carries;
	// PadMultiple is what every batch's length is padded to.
	InputContract string
	Inputs        []InputSpec
	PadMultiple   int
	// MaxSeq is the longest sequence the model takes; Outputs its declared
	// output names (nil when the manifest names none); MemoryMB one worker's
	// estimated memory.
	MaxSeq     int
	Outputs    []string
	MemoryMB   int
	thresholds map[string][]float32
	// closure[j] is the bit mask of label j and its ancestors.
	closure []uint64
}

// ErrModelUnavailable reports a model directory that is missing or fails
// verification; labeling is disabled until it is fixed.
var ErrModelUnavailable = errors.New("labeler model unavailable")

// LoadModel reads dir/manifest.json and verifies the variant's model file
// ("int8" or "fp32") and tokenizer.json against its SHA-256s, then loads the
// tokenizer. A manifest for a framing contract other than this build's is
// refused.
func LoadModel(dir, variant string) (*Model, error) {
	return LoadFramedModel(dir, variant, KestrelFraming)
}

// Framing is what loading a model needs to know of the framing contract its
// caller implements: its name, the longest sequence it builds, and whether
// its models label (labels, thresholds and the decision rule).
type Framing struct {
	Contract  string
	MaxTokens int
	Labels    bool
}

// KestrelFraming is the message labeler's framing, kestrel-framing-v1.
var KestrelFraming = Framing{Contract: FramingContract, MaxTokens: MaxTokens, Labels: true}

// LoadFramedModel is LoadModel for a caller that implements framing: a
// manifest for any other framing contract is refused, and a model whose
// framing does not label needs no labels or thresholds.
func LoadFramedModel(dir, variant string, framing Framing) (*Model, error) {
	if variant == "" {
		variant = "int8"
	}
	m := &Model{Dir: dir, Variant: variant}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	if err := json.Unmarshal(raw, &m.Manifest); err != nil {
		return nil, fmt.Errorf("%w: manifest.json: %v", ErrModelUnavailable, err)
	}
	man := &m.Manifest
	if man.FramingContract != framing.Contract {
		return nil, fmt.Errorf("%w: the model needs framing %q; this build implements %q", ErrModelUnavailable, man.FramingContract, framing.Contract)
	}
	m.InputContract = man.InputContract
	if m.InputContract == "" {
		m.InputContract = InputContractIDsAttention
	}
	contract, ok := InputContracts[m.InputContract]
	if !ok {
		return nil, fmt.Errorf("%w: the model needs input contract %q; this build implements %v", ErrModelUnavailable, m.InputContract, slices.Sorted(maps.Keys(InputContracts)))
	}
	m.Inputs = contract.Inputs
	if len(man.Inputs) > 0 {
		roles := func(inputs []InputSpec) []string {
			list := []string{}
			for _, input := range inputs {
				list = append(list, input.Role)
			}
			slices.Sort(list)
			return list
		}
		if err := ValidateInputs(man.Inputs); err != nil || !slices.Equal(roles(man.Inputs), roles(contract.Inputs)) {
			return nil, fmt.Errorf("%w: the manifest's inputs %v do not have the roles of %s %v", ErrModelUnavailable, man.Inputs, m.InputContract, contract.Inputs)
		}
		m.Inputs = man.Inputs
	}
	m.PadMultiple = contract.PadMultiple
	for _, multiple := range []int{man.Padding.Multiple, man.PadMultiple} {
		if multiple < 0 || multiple > 64 {
			return nil, fmt.Errorf("%w: padding multiple %d is outside 1..64", ErrModelUnavailable, multiple)
		}
		if multiple > 0 {
			m.PadMultiple = lcm(m.PadMultiple, multiple)
		}
	}
	m.MaxSeq = framing.MaxTokens
	if man.MaxSeq != 0 {
		if man.MaxSeq < framing.MaxTokens || man.MaxSeq > 8192 {
			return nil, fmt.Errorf("%w: max_seq %d: %s frames up to %d tokens, and at most 8192 are allowed", ErrModelUnavailable, man.MaxSeq, framing.Contract, framing.MaxTokens)
		}
		m.MaxSeq = man.MaxSeq
	}
	if m.MemoryMB = man.MemoryMB; m.MemoryMB <= 0 {
		m.MemoryMB = 1600
	}
	if outputs := man.ONNX.Outputs; len(outputs) > 0 {
		var named map[string]any
		var listed []string
		switch {
		case json.Unmarshal(outputs, &named) == nil:
			m.Outputs = slices.Sorted(maps.Keys(named))
		case json.Unmarshal(outputs, &listed) == nil:
			m.Outputs = slices.Sorted(slices.Values(listed))
		default:
			return nil, fmt.Errorf("%w: onnx.outputs is neither a map nor a list of names", ErrModelUnavailable)
		}
	}
	file := man.Files["onnx_"+variant]
	if file == "" || strings.ContainsAny(file, `/\`) {
		return nil, fmt.Errorf("%w: the manifest names no %s model file", ErrModelUnavailable, variant)
	}
	m.Path = filepath.Join(dir, file)
	if m.SHA256, err = verifyFile(m.Path, man.SHA256[file]); err != nil {
		return nil, err
	}
	tokenizerFile := man.Files["tokenizer"]
	if tokenizerFile == "" {
		tokenizerFile = "tokenizer.json"
	}
	want := man.TokenizerSHA256
	if want == "" {
		want = man.SHA256[tokenizerFile]
	}
	data, err := os.ReadFile(filepath.Join(dir, tokenizerFile))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	sum := sha256.Sum256(data)
	if m.TokenizerSHA256 = hex.EncodeToString(sum[:]); want == "" || m.TokenizerSHA256 != want {
		return nil, fmt.Errorf("%w: %s has SHA-256 %s, the manifest %q", ErrModelUnavailable, tokenizerFile, m.TokenizerSHA256, want)
	}
	if m.Tokenizer, err = LoadTokenizer(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	if !framing.Labels {
		return m, nil
	}
	if err := m.prepareDecisions(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	return m, nil
}

// InputContract is what a model's input contract requires: its inputs (by
// default name and role) and the multiple its padded length must be.
type InputContract struct {
	Inputs      []InputSpec
	PadMultiple int
}

// Model input contracts.
//   - ids-attention-v1: input_ids and attention_mask, the framed tokens and
//     their mask; mean pooling over every real token happens in the model.
//   - ids-attention-pool-v1: the same plus pooling_mask, 1 on the framing's
//     text span (FrameSpans.Text): the model averages only the target's
//     vectors while attending to the whole input. An item whose text yields
//     no tokens cannot be labeled and is never sent.
//   - ids-attention-pool-segments-v1: the same plus segment_ids, which
//     number the framing's sentences (Framed.Extra[SegmentSpans]) so the
//     model can pool each one, as the agent event verifier's window does.
//
// Both pad to a multiple of 8: the int8 models' outputs change when the
// padded length is not a multiple of 4 (PadMultiple).
const (
	InputContractIDsAttention             = "ids-attention-v1"
	InputContractIDsAttentionPool         = "ids-attention-pool-v1"
	InputContractIDsAttentionPoolSegments = "ids-attention-pool-segments-v1"
)

// InputContracts are the input contracts this build implements.
var InputContracts = map[string]InputContract{
	InputContractIDsAttention:     {Inputs: DefaultInputs, PadMultiple: PadMultiple},
	InputContractIDsAttentionPool: {Inputs: append(slices.Clone(DefaultInputs), InputSpec{Name: "pooling_mask", Role: RolePool}), PadMultiple: PadMultiple},
	InputContractIDsAttentionPoolSegments: {Inputs: append(slices.Clone(DefaultInputs), InputSpec{Name: "pooling_mask", Role: RolePool},
		InputSpec{Name: "segment_ids", Role: RoleSegment}), PadMultiple: PadMultiple},
}

// Pools reports whether the model pools a role-pool input, so an item with
// an empty text span cannot be labeled.
func (m *Model) Pools() bool {
	return slices.ContainsFunc(m.Inputs, func(input InputSpec) bool { return input.Role == RolePool })
}

func lcm(a, b int) int {
	x, y := a, b
	for y != 0 {
		x, y = y, x%y
	}
	return a / x * b
}

// verifyFile hashes path and compares it with want.
func verifyFile(path, want string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if want == "" || got != want {
		return "", fmt.Errorf("%w: %s has SHA-256 %s, the manifest %q", ErrModelUnavailable, filepath.Base(path), got, want)
	}
	return got, nil
}

// HasOutputs reports whether the model declares every one of names (true
// when it declares none: nothing to check against).
func (m *Model) HasOutputs(names []string) error {
	if m.Outputs == nil {
		return nil
	}
	for _, name := range names {
		if !slices.Contains(m.Outputs, name) {
			return fmt.Errorf("model %s has no output %q (it declares %v)", m.Manifest.Name, name, m.Outputs)
		}
	}
	return nil
}

// Labels is the label count (the probs width).
func (m *Model) Labels() int { return len(m.Manifest.Labels.Order) }

func (m *Model) prepareDecisions() error {
	order := m.Manifest.Labels.Order
	if len(order) == 0 || len(order) > 64 {
		return fmt.Errorf("the manifest has %d labels; the labeler stores at most 64", len(order))
	}
	index := map[string]int{}
	for j, label := range order {
		index[label] = j
	}
	m.closure = make([]uint64, len(order))
	for j, label := range order {
		m.closure[j] = 1 << j
		for _, ancestor := range m.Manifest.Labels.Ancestors[label] {
			k, ok := index[ancestor]
			if !ok {
				return fmt.Errorf("label %s has unknown ancestor %s", label, ancestor)
			}
			m.closure[j] |= 1 << k
		}
	}
	if m.Manifest.Thresholds["default"] == nil {
		return errors.New("the manifest has no default thresholds")
	}
	m.thresholds = map[string][]float32{}
	for segment, values := range m.Manifest.Thresholds {
		if len(values) != len(order) {
			return fmt.Errorf("segment %s has %d thresholds for %d labels", segment, len(values), len(order))
		}
		row := make([]float32, len(values))
		for j, value := range values {
			// The reference compares float32 probabilities with float32
			// thresholds (numpy), so these are rounded the same way.
			row[j] = float32(value)
		}
		m.thresholds[segment] = row
	}
	return nil
}

// Thresholds is the threshold row a segment uses: its own, else "default".
func (m *Model) Thresholds(segment string) []float32 {
	if row := m.thresholds[segment]; row != nil {
		return row
	}
	return m.thresholds["default"]
}

// Decide turns one item's probabilities into its closed label set, as a bit
// mask in label order: label j is on when probs[j] reaches the segment's
// threshold, and every label that is on adds its ancestors.
func (m *Model) Decide(probs []float32, segment string) uint64 {
	row := m.Thresholds(segment)
	var mask uint64
	for j, p := range probs[:len(row)] {
		if p >= row[j] {
			mask |= m.closure[j]
		}
	}
	return mask
}

// LabelNames lists the labels a mask holds, in label order.
func (m *Model) LabelNames(mask uint64) []string {
	names := []string{}
	for j, label := range m.Manifest.Labels.Order {
		if mask&(1<<j) != 0 {
			names = append(names, label)
		}
	}
	return names
}
