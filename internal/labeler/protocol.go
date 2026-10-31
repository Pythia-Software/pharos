// Package labeler is Pharos's local message labeler outside the catalog: the
// tokenizer and input framing the model was trained with, the model manifest,
// and the worker protocol that runs the model in a separate process. It uses
// only the standard library (and golang.org/x/text for NFC), so the main pharos
// binary stays pure Go; inference itself happens in a worker binary such as
// cmd/pharos-labeler-ort. See docs/local-labeler.md.
package labeler

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
)

// Worker protocol pharos-labeler/1 (docs/local-labeler.md, "Worker protocol").
//
// Every message in either direction is one frame:
//
//	u32 little-endian N   bytes that follow (type + payload), 1 <= N <= MaxFrameBytes
//	u8  type
//	N-1 bytes payload
//
// The main process writes requests to the worker's stdin and reads replies from
// its stdout; the worker's stderr is free-form diagnostics.
const (
	ProtocolName    = "pharos-labeler"
	ProtocolVersion = 1
	// MaxFrameBytes bounds one frame. A length outside 1..MaxFrameBytes cannot
	// be resynchronized, so it is fatal for the stream.
	MaxFrameBytes = 64 << 20
)

// Frame types. Requests (main process to worker) are upper case, replies
// lower case.
const (
	FrameHello    byte = 'H' // JSON Hello
	FrameRun      byte = 'R' // binary RunRequest
	FramePing     byte = 'P' // u64 nonce
	FrameQuit     byte = 'Q' // empty
	FrameReady    byte = 'h' // JSON Ready
	FrameResult   byte = 'r' // binary RunResult
	FrameError    byte = 'e' // JSON ErrorReply
	FramePong     byte = 'p' // u64 nonce, echoed
	FrameGoodbye  byte = 'q' // empty
	frameTypeList      = "HRPQhrepq"
)

// Error codes a worker reports.
const (
	ErrCodeBadFrame    = "bad_frame"   // framing is broken: fatal
	ErrCodeProtocol    = "protocol"    // no common protocol version, or hello missing: fatal
	ErrCodeUnsupported = "unsupported" // unknown frame type or option: the frame is skipped
	ErrCodeBadRequest  = "bad_request" // a run request is malformed or out of limits
	ErrCodeRunFailed   = "run_failed"  // the runtime failed on a well-formed request
	ErrCodeModel       = "model"       // the model cannot be loaded: fatal
	ErrCodeInternal    = "internal"
)

// Hello is the main process's first frame: which protocol versions it speaks,
// which model file to load and how to run it.
type Hello struct {
	Protocol []int  `json:"protocol"`
	Model    string `json:"model"`
	// ModelSHA256, when set, is the expected SHA-256 of the model file; a
	// worker that checks it refuses a mismatch with code "model".
	ModelSHA256 string `json:"model_sha256,omitempty"`
	// Threads is the intra-op thread count (0: the runtime's default).
	Threads int `json:"threads,omitempty"`
	// VocabSize bounds token ids: every id must be in [0, VocabSize).
	VocabSize int `json:"vocab_size"`
	// Inputs are the int32 [batch, seq] tensors every run request carries,
	// by the model's input names, with their roles (the model's input
	// contract). A hello without them means DefaultInputs.
	Inputs []InputSpec `json:"inputs,omitempty"`
	// MaxBatch and MaxSeq are the largest batch and sequence the main process
	// will send; a worker may lower them in Ready.
	MaxBatch int `json:"max_batch,omitempty"`
	MaxSeq   int `json:"max_seq,omitempty"`
	// Options are worker-specific settings (for example a provider name).
	// A worker ignores options it does not know.
	Options map[string]any `json:"options,omitempty"`
}

// Input roles. Every input is int32 [batch, seq], right-padded:
//   - ids: token ids, each in [0, vocab_size); exactly one input has it;
//   - attention: 1 on real tokens, 0 on padding, at least one 1 per row;
//     exactly one input has it;
//   - pool: 1 on the tokens the model pools, a non-empty subset of the
//     attention mask's per row (for example the target text only).
const (
	RoleIDs       = "ids"
	RoleAttention = "attention"
	RolePool      = "pool"
)

// InputSpec is one input a run request carries.
type InputSpec struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// DefaultInputs are input_ids and attention_mask: what a hello that names
// no inputs means, and input contract ids-attention-v1.
var DefaultInputs = []InputSpec{{Name: "input_ids", Role: RoleIDs}, {Name: "attention_mask", Role: RoleAttention}}

// RunInputs are the inputs a hello declares, or DefaultInputs.
func (h *Hello) RunInputs() []InputSpec {
	if len(h.Inputs) == 0 {
		return DefaultInputs
	}
	return h.Inputs
}

// ValidateInputs checks a hello's inputs: unique names, registered roles,
// and each required role (ids, attention) exactly once.
func ValidateInputs(inputs []InputSpec) error {
	seen, counts := map[string]bool{}, map[string]int{}
	for _, input := range inputs {
		if input.Name == "" || len(input.Name) > 255 || seen[input.Name] {
			return fmt.Errorf("input name %q is empty, too long or repeated", input.Name)
		}
		seen[input.Name] = true
		if _, ok := lookupRole(input.Role); !ok {
			return fmt.Errorf("input %s has unknown role %q", input.Name, input.Role)
		}
		counts[input.Role]++
	}
	for _, role := range requiredRoles() {
		if counts[role] != 1 {
			return fmt.Errorf("inputs need exactly one %s input", role)
		}
	}
	return nil
}

// TensorInfo describes one model input or output.
type TensorInfo struct {
	Name  string `json:"name"`
	DType string `json:"dtype"`
	Shape []any  `json:"shape,omitempty"`
}

// WorkerInfo identifies a worker build and its runtime.
type WorkerInfo struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Runtime   string   `json:"runtime"`
	Device    string   `json:"device,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// Ready is the worker's reply to Hello once the model is loaded.
type Ready struct {
	Protocol int        `json:"protocol"`
	Worker   WorkerInfo `json:"worker"`
	Model    struct {
		Path    string       `json:"path"`
		SHA256  string       `json:"sha256,omitempty"`
		Inputs  []TensorInfo `json:"inputs"`
		Outputs []TensorInfo `json:"outputs"`
	} `json:"model"`
	Limits struct {
		MaxBatch      int `json:"max_batch"`
		MaxSeq        int `json:"max_seq"`
		MaxFrameBytes int `json:"max_frame_bytes"`
	} `json:"limits"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// ErrorReply reports a failure. ID is the run request it answers, or 0 for one
// not tied to a request. Fatal means the worker exits after sending it.
type ErrorReply struct {
	ID      uint64 `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal,omitempty"`
}

func (e *ErrorReply) Error() string {
	return fmt.Sprintf("labeler worker: %s: %s", e.Code, e.Message)
}

// Tensor dtypes on the wire.
const (
	DTypeInt32   byte = 1
	DTypeFloat32 byte = 2
)

// Tensor is a named row-major tensor. Exactly one of Int32 and Float32 holds
// its data, by DType.
type Tensor struct {
	Name    string
	DType   byte
	Dims    []int
	Int32   []int32
	Float32 []float32
}

// Elements is the product of the tensor's dims.
func (t *Tensor) Elements() int {
	n := 1
	for _, d := range t.Dims {
		n *= d
	}
	return n
}

// RunRequest asks for one batch: inputs input_ids and attention_mask, int32
// [batch, seq], right-padded. Outputs names the outputs wanted; empty means
// every output.
type RunRequest struct {
	ID      uint64
	Inputs  []Tensor
	Outputs []string
}

// RunResult answers a RunRequest with float32 output tensors.
type RunResult struct {
	ID      uint64
	Outputs []Tensor
}

// Tensor returns the tensor named name, or nil.
func (r *RunResult) Tensor(name string) *Tensor {
	for i := range r.Outputs {
		if r.Outputs[i].Name == name {
			return &r.Outputs[i]
		}
	}
	return nil
}

// ErrFrameTooLarge is returned for a frame length outside 1..max.
var ErrFrameTooLarge = errors.New("labeler protocol: frame length out of range")

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload)+1 > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	var header [5]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(len(payload)+1))
	header[4] = kind
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one frame of at most max bytes (MaxFrameBytes when max is
// 0). io.EOF means the stream ended cleanly between frames.
func ReadFrame(r io.Reader, max int) (byte, []byte, error) {
	if max <= 0 {
		max = MaxFrameBytes
	}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, fmt.Errorf("labeler protocol: truncated frame header: %w", err)
		}
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n < 1 || n > uint32(max) {
		return 0, nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, fmt.Errorf("labeler protocol: truncated frame: %w", err)
	}
	return body[0], body[1:], nil
}

// WriteJSON writes a frame whose payload is value as JSON.
func WriteJSON(w io.Writer, kind byte, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return WriteFrame(w, kind, payload)
}

// EncodeNonce and DecodeNonce are the payload of ping and pong.
func EncodeNonce(nonce uint64) []byte {
	return binary.LittleEndian.AppendUint64(nil, nonce)
}

func DecodeNonce(payload []byte) (uint64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("labeler protocol: nonce of %d bytes", len(payload))
	}
	return binary.LittleEndian.Uint64(payload), nil
}

// EncodeRun encodes a run request:
//
//	u64 id, u16 tensor count, tensors, u16 output-name count, names (u8 length + bytes)
func EncodeRun(req *RunRequest) ([]byte, error) {
	buf := binary.LittleEndian.AppendUint64(nil, req.ID)
	buf, err := appendTensors(buf, req.Inputs)
	if err != nil {
		return nil, err
	}
	if len(req.Outputs) > math.MaxUint16 {
		return nil, errors.New("labeler protocol: too many output names")
	}
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(req.Outputs)))
	for _, name := range req.Outputs {
		if len(name) == 0 || len(name) > 255 {
			return nil, fmt.Errorf("labeler protocol: output name %q", name)
		}
		buf = append(buf, byte(len(name)))
		buf = append(buf, name...)
	}
	return buf, nil
}

// DecodeRun decodes a run request. A request whose id could be read but whose
// body is malformed returns that id with the error, so the reply can name it.
func DecodeRun(payload []byte) (*RunRequest, error) {
	d := decoder{buf: payload}
	req := &RunRequest{ID: d.u64()}
	if d.err != nil {
		return req, d.err
	}
	req.Inputs = d.tensors()
	count := int(d.u16())
	for i := 0; i < count && d.err == nil; i++ {
		req.Outputs = append(req.Outputs, d.name())
	}
	if d.err == nil && len(d.buf) != d.at {
		d.fail("%d trailing bytes", len(d.buf)-d.at)
	}
	return req, d.err
}

// EncodeResult encodes a run result: u64 id, u16 tensor count, tensors.
func EncodeResult(res *RunResult) ([]byte, error) {
	buf := binary.LittleEndian.AppendUint64(nil, res.ID)
	return appendTensors(buf, res.Outputs)
}

// DecodeResult decodes a run result.
func DecodeResult(payload []byte) (*RunResult, error) {
	d := decoder{buf: payload}
	res := &RunResult{ID: d.u64()}
	if d.err != nil {
		return res, d.err
	}
	res.Outputs = d.tensors()
	if d.err == nil && len(d.buf) != d.at {
		d.fail("%d trailing bytes", len(d.buf)-d.at)
	}
	return res, d.err
}

// A tensor on the wire: u8 name length, name, u8 dtype, u8 rank, u32 dims,
// then the elements little-endian, row-major.
func appendTensors(buf []byte, tensors []Tensor) ([]byte, error) {
	if len(tensors) > math.MaxUint16 {
		return nil, errors.New("labeler protocol: too many tensors")
	}
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(tensors)))
	for i := range tensors {
		t := &tensors[i]
		if len(t.Name) == 0 || len(t.Name) > 255 {
			return nil, fmt.Errorf("labeler protocol: tensor name %q", t.Name)
		}
		if len(t.Dims) > 8 {
			return nil, fmt.Errorf("labeler protocol: tensor %s has rank %d", t.Name, len(t.Dims))
		}
		n := t.Elements()
		switch t.DType {
		case DTypeInt32:
			if len(t.Int32) != n {
				return nil, fmt.Errorf("labeler protocol: tensor %s has %d elements for dims %v", t.Name, len(t.Int32), t.Dims)
			}
		case DTypeFloat32:
			if len(t.Float32) != n {
				return nil, fmt.Errorf("labeler protocol: tensor %s has %d elements for dims %v", t.Name, len(t.Float32), t.Dims)
			}
		default:
			return nil, fmt.Errorf("labeler protocol: tensor %s has dtype %d", t.Name, t.DType)
		}
		buf = append(buf, byte(len(t.Name)))
		buf = append(buf, t.Name...)
		buf = append(buf, t.DType, byte(len(t.Dims)))
		for _, dim := range t.Dims {
			if dim < 0 || dim > math.MaxUint32 {
				return nil, fmt.Errorf("labeler protocol: tensor %s dim %d", t.Name, dim)
			}
			buf = binary.LittleEndian.AppendUint32(buf, uint32(dim))
		}
		at := len(buf)
		buf = append(buf, make([]byte, 4*n)...)
		if t.DType == DTypeInt32 {
			for k, v := range t.Int32 {
				binary.LittleEndian.PutUint32(buf[at+4*k:], uint32(v))
			}
		} else {
			for k, v := range t.Float32 {
				binary.LittleEndian.PutUint32(buf[at+4*k:], math.Float32bits(v))
			}
		}
	}
	return buf, nil
}

type decoder struct {
	buf []byte
	at  int
	err error
}

func (d *decoder) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("labeler protocol: "+format, args...)
	}
}

func (d *decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || len(d.buf)-d.at < n {
		d.fail("payload truncated at byte %d (want %d more)", d.at, n)
		return nil
	}
	out := d.buf[d.at : d.at+n]
	d.at += n
	return out
}

func (d *decoder) u8() byte {
	b := d.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (d *decoder) u16() uint16 {
	b := d.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (d *decoder) u32() uint32 {
	b := d.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (d *decoder) u64() uint64 {
	b := d.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (d *decoder) name() string {
	n := int(d.u8())
	if d.err == nil && n == 0 {
		d.fail("empty name")
	}
	return string(d.take(n))
}

func (d *decoder) tensors() []Tensor {
	count := int(d.u16())
	tensors := make([]Tensor, 0, min(count, 16))
	for i := 0; i < count && d.err == nil; i++ {
		t := Tensor{Name: d.name(), DType: d.u8()}
		rank := int(d.u8())
		if rank > 8 {
			d.fail("tensor %s has rank %d", t.Name, rank)
			break
		}
		n := 1
		for k := 0; k < rank; k++ {
			dim := int(d.u32())
			t.Dims = append(t.Dims, dim)
			if dim > 0 && n > (len(d.buf)/4+1)/dim {
				d.fail("tensor %s dims %v exceed the payload", t.Name, t.Dims)
			}
			n *= dim
		}
		if d.err != nil {
			break
		}
		raw := d.take(4 * n)
		if d.err != nil {
			break
		}
		switch t.DType {
		case DTypeInt32:
			t.Int32 = make([]int32, n)
			for k := range t.Int32 {
				t.Int32[k] = int32(binary.LittleEndian.Uint32(raw[4*k:]))
			}
		case DTypeFloat32:
			t.Float32 = make([]float32, n)
			for k := range t.Float32 {
				t.Float32[k] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*k:]))
			}
		default:
			d.fail("tensor %s has dtype %d", t.Name, t.DType)
		}
		tensors = append(tensors, t)
	}
	return tensors
}

// ValidateRun checks a decoded run request against the inputs the hello
// declared and the limits the worker announced: each declared input exactly
// once and nothing else, all int32 [batch, seq] with the same dims, and each
// input's values as its role requires (see RoleIDs). Workers use it so
// every worker rejects the same malformed requests.
func ValidateRun(req *RunRequest, inputs []InputSpec, vocab, maxBatch, maxSeq int) error {
	if len(inputs) == 0 {
		inputs = DefaultInputs
	}
	byName := map[string]*Tensor{}
	for i := range req.Inputs {
		name := req.Inputs[i].Name
		if byName[name] != nil {
			return fmt.Errorf("input %q given twice", name)
		}
		byName[name] = &req.Inputs[i]
	}
	var attention *Tensor
	for _, input := range inputs {
		t := byName[input.Name]
		if t == nil {
			return fmt.Errorf("input %s is required", input.Name)
		}
		if input.Role == RoleAttention {
			attention = t
		}
	}
	if attention == nil {
		return errors.New("the inputs declare no attention input")
	}
	if len(byName) != len(inputs) {
		for name := range byName {
			if !slices.ContainsFunc(inputs, func(input InputSpec) bool { return input.Name == name }) {
				return fmt.Errorf("unknown input %q", name)
			}
		}
	}
	first := byName[inputs[0].Name]
	for _, input := range inputs {
		t := byName[input.Name]
		if t.DType != DTypeInt32 || len(t.Dims) != 2 {
			return fmt.Errorf("%s must be int32 [batch, seq]", t.Name)
		}
		if t.Dims[0] != first.Dims[0] || t.Dims[1] != first.Dims[1] {
			return fmt.Errorf("%s %v and %s %v differ in shape", first.Name, first.Dims, t.Name, t.Dims)
		}
	}
	batch, seq := first.Dims[0], first.Dims[1]
	if batch < 1 || seq < 1 {
		return fmt.Errorf("empty batch %v", first.Dims)
	}
	if maxBatch > 0 && batch > maxBatch {
		return fmt.Errorf("batch %d exceeds the limit %d", batch, maxBatch)
	}
	if maxSeq > 0 && seq > maxSeq {
		return fmt.Errorf("sequence %d exceeds the limit %d", seq, maxSeq)
	}
	for _, input := range inputs {
		role, ok := lookupRole(input.Role)
		if !ok {
			return fmt.Errorf("input %s has unknown role %q", input.Name, input.Role)
		}
		if role.Validate == nil {
			continue
		}
		if err := role.Validate(RoleCheck{Name: input.Name, Values: byName[input.Name].Int32, Attention: attention.Int32, Batch: batch, Seq: seq, Vocab: vocab}); err != nil {
			return err
		}
	}
	for i, name := range req.Outputs {
		if name == "" {
			return errors.New("empty output name")
		}
		for _, earlier := range req.Outputs[:i] {
			if earlier == name {
				return fmt.Errorf("output %q requested twice", name)
			}
		}
	}
	return nil
}

// NewFrameReader buffers a stream for ReadFrame.
func NewFrameReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, 1<<16) }
