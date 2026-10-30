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
	InputContract   string            `json:"input_contract"`
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
		Outputs map[string]string `json:"outputs"`
	} `json:"onnx"`
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
	// InputContract and Inputs are what every run request carries.
	InputContract string
	Inputs        []InputSpec
	thresholds    map[string][]float32
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
	if man.FramingContract != FramingContract {
		return nil, fmt.Errorf("%w: the model needs framing %q; this build implements %q", ErrModelUnavailable, man.FramingContract, FramingContract)
	}
	m.InputContract = man.InputContract
	if m.InputContract == "" {
		m.InputContract = InputContractIDsAttention
	}
	inputs, ok := InputContracts[m.InputContract]
	if !ok {
		return nil, fmt.Errorf("%w: the model needs input contract %q; this build implements %v", ErrModelUnavailable, m.InputContract, slices.Sorted(maps.Keys(InputContracts)))
	}
	m.Inputs = inputs
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
	if err := m.prepareDecisions(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}
	return m, nil
}

// Model input contracts. ids-attention-v1 is input_ids and attention_mask:
// the framed tokens and their mask (mean pooling over every real token
// happens inside the model). A contract that pools only the target text
// adds an input with RolePool, made from the framing's text span
// (FrameSpans.Text); it is one more entry here, not a protocol change.
const InputContractIDsAttention = "ids-attention-v1"

// InputContracts are the input contracts this build implements.
var InputContracts = map[string][]InputSpec{InputContractIDsAttention: DefaultInputs}

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
