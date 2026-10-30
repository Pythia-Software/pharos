package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/labeler"
)

// wait bounds every blocking step, so that a worker that never answers fails
// the test instead of hanging it.
const wait = 10 * time.Second

const pad = 50283

// fakeCall is one Run of a fakeSession. ids and mask are its input_ids and
// attention_mask as int64, whichever type the model takes.
type fakeCall struct {
	inputs     []modelInput
	ids, mask  []int64
	batch, seq int
	outputs    []string
}

// values is the named input's data as int64, or nil.
func (c fakeCall) values(name string) []int64 {
	for _, in := range c.inputs {
		if in.Name != name {
			continue
		}
		if in.Int64 != nil {
			return in.Int64
		}
		return widen(in.Int32)
	}
	return nil
}

// fakeSession is a model with outputs logits [batch, 2, 3] and probs
// [batch, 3] whose values encode each row's ids (see fakeOutputs).
type fakeSession struct {
	info   modelInfo
	run    func(fakeCall) ([]labeler.Tensor, error) // nil: fakeOutputs
	calls  atomic.Int32
	closed atomic.Bool
}

func fakeInfo() modelInfo {
	return modelInfo{
		Runtime: "fake 1", Device: "cpu", Providers: []string{"FakeProvider"},
		Inputs: []labeler.TensorInfo{
			{Name: "input_ids", DType: "int64", Shape: []any{"batch", "seq"}},
			{Name: "attention_mask", DType: "int64", Shape: []any{"batch", "seq"}},
		},
		Outputs: []labeler.TensorInfo{
			{Name: "logits", DType: "float32", Shape: []any{"batch", 2, 3}},
			{Name: "probs", DType: "float32", Shape: []any{"batch", 3}},
		},
	}
}

func (f *fakeSession) Info() modelInfo { return f.info }
func (f *fakeSession) Close() error    { f.closed.Store(true); return nil }

func (f *fakeSession) Run(inputs []modelInput, batch, seq int, outputs []string) ([]labeler.Tensor, error) {
	f.calls.Add(1)
	call := fakeCall{inputs: inputs, batch: batch, seq: seq, outputs: outputs}
	call.ids, call.mask = call.values("input_ids"), call.values("attention_mask")
	if f.run != nil {
		return f.run(call)
	}
	return fakeOutputs(call), nil
}

// rowKey is what the fake outputs for a row: the sum of its masked ids plus
// 1000 times its token count.
func rowKey(ids, mask []int64) float32 {
	var key int64
	for i := range ids {
		key += mask[i] * (ids[i] + 1000)
	}
	return float32(key)
}

// fakeOutputs gives logits[r][h][k] = key + 10h + k and probs[r][k] = key + k.
func fakeOutputs(c fakeCall) []labeler.Tensor {
	var out []labeler.Tensor
	for _, name := range c.outputs {
		t := labeler.Tensor{Name: name, DType: labeler.DTypeFloat32}
		for r := 0; r < c.batch; r++ {
			key := rowKey(c.ids[r*c.seq:(r+1)*c.seq], c.mask[r*c.seq:(r+1)*c.seq])
			if name == "logits" {
				for h := 0; h < 2; h++ {
					for k := 0; k < 3; k++ {
						t.Float32 = append(t.Float32, key+float32(10*h+k))
					}
				}
			} else {
				for k := 0; k < 3; k++ {
					t.Float32 = append(t.Float32, key+float32(k))
				}
			}
		}
		t.Dims = []int{c.batch, 3}
		if name == "logits" {
			t.Dims = []int{c.batch, 2, 3}
		}
		out = append(out, t)
	}
	return out
}

// harness runs serve over pipes.
type harness struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	status chan int
	model  string // a model file, for the hello
	sum    string // its SHA-256
	opened chan sessionConfig
}

// startWorker serves with an opener that returns sess.
func startWorker(t *testing.T, sess *fakeSession) *harness {
	t.Helper()
	h := newHarness(t)
	return h.start(func(cfg sessionConfig) (session, error) {
		h.opened <- cfg
		return sess, nil
	})
}

// newHarness makes a model file for the hello; start starts the worker.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, status: make(chan int, 1), opened: make(chan sessionConfig, 1)}
	h.model = filepath.Join(t.TempDir(), "model.onnx")
	content := []byte("not really a model")
	if err := os.WriteFile(h.model, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	h.sum = hex.EncodeToString(digest[:])
	return h
}

func (h *harness) start(open opener) *harness {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h.in, h.out = inW, labeler.NewFrameReader(outR)
	go func() {
		status := serve(inR, outW, &testLog{h.t}, open)
		inR.CloseWithError(errors.New("worker exited"))
		outW.Close()
		h.status <- status
	}()
	h.t.Cleanup(func() {
		inW.Close()
		outR.Close()
	})
	return h
}

type testLog struct{ t *testing.T }

func (l *testLog) Write(p []byte) (int, error) {
	l.t.Logf("worker: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// within runs f, failing the test if it takes longer than wait.
func (h *harness) within(what string, f func()) {
	h.t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(wait):
		h.t.Fatalf("timed out: %s", what)
	}
}

func (h *harness) writeRaw(b []byte) {
	h.t.Helper()
	var err error
	h.within("writing to the worker", func() { _, err = h.in.Write(b) })
	if err != nil {
		h.t.Fatalf("writing to the worker: %v", err)
	}
}

func (h *harness) send(kind byte, payload []byte) {
	h.t.Helper()
	var err error
	h.within("writing to the worker", func() { err = labeler.WriteFrame(h.in, kind, payload) })
	if err != nil {
		h.t.Fatalf("writing to the worker: %v", err)
	}
}

func (h *harness) sendJSON(kind byte, value any) {
	h.t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(kind, payload)
}

func (h *harness) recv() (byte, []byte) {
	h.t.Helper()
	var (
		kind    byte
		payload []byte
		err     error
	)
	h.within("reading a reply", func() { kind, payload, err = labeler.ReadFrame(h.out, 0) })
	if err != nil {
		h.t.Fatalf("reading a reply: %v", err)
	}
	return kind, payload
}

func (h *harness) expectError(id uint64, code string, fatal bool) labeler.ErrorReply {
	h.t.Helper()
	kind, payload := h.recv()
	var e labeler.ErrorReply
	if kind != labeler.FrameError || json.Unmarshal(payload, &e) != nil {
		h.t.Fatalf("want error %s for %d, got frame %q %s", code, id, kind, payload)
	}
	if e.ID != id || e.Code != code || e.Fatal != fatal || e.Message == "" {
		h.t.Fatalf("want error {id %d code %s fatal %v}, got %+v", id, code, fatal, e)
	}
	return e
}

func (h *harness) expectResult() *labeler.RunResult {
	h.t.Helper()
	kind, payload := h.recv()
	if kind != labeler.FrameResult {
		h.t.Fatalf("want a result, got frame %q %s", kind, payload)
	}
	res, err := labeler.DecodeResult(payload)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) expectPong(nonce uint64) {
	h.t.Helper()
	kind, payload := h.recv()
	got, err := labeler.DecodeNonce(payload)
	if kind != labeler.FramePong || err != nil || got != nonce {
		h.t.Fatalf("want pong %d, got frame %q %v %v", nonce, kind, payload, err)
	}
}

func (h *harness) expectExit(want int) {
	h.t.Helper()
	select {
	case got := <-h.status:
		if got != want {
			h.t.Fatalf("exit status %d, want %d", got, want)
		}
	case <-time.After(wait):
		h.t.Fatalf("worker did not exit")
	}
}

func (h *harness) hello() labeler.Hello {
	return labeler.Hello{Protocol: []int{1}, Model: h.model, ModelSHA256: h.sum, Threads: 3, VocabSize: 50368, MaxBatch: 8, MaxSeq: 16}
}

// handshake sends the default hello and returns the ready.
func (h *harness) handshake() labeler.Ready {
	h.t.Helper()
	h.sendJSON(labeler.FrameHello, h.hello())
	kind, payload := h.recv()
	var ready labeler.Ready
	if kind != labeler.FrameReady || json.Unmarshal(payload, &ready) != nil {
		h.t.Fatalf("want ready, got frame %q %s", kind, payload)
	}
	return ready
}

// runPayload encodes rows right-padded to the longest, mask 0 on padding.
func runPayload(t *testing.T, id uint64, rows [][]int32, outputs ...string) []byte {
	t.Helper()
	return paddedRunPayload(t, id, rows, 1, outputs...)
}

// paddedRunPayload pads rows to the longest rounded up to a multiple of mult.
func paddedRunPayload(t *testing.T, id uint64, rows [][]int32, mult int, outputs ...string) []byte {
	t.Helper()
	seq := 0
	for _, r := range rows {
		seq = max(seq, len(r))
	}
	seq = (seq + mult - 1) / mult * mult
	ids := labeler.Tensor{Name: "input_ids", DType: labeler.DTypeInt32, Dims: []int{len(rows), seq}}
	mask := labeler.Tensor{Name: "attention_mask", DType: labeler.DTypeInt32, Dims: []int{len(rows), seq}}
	for _, r := range rows {
		for i := 0; i < seq; i++ {
			if i < len(r) {
				ids.Int32 = append(ids.Int32, r[i])
				mask.Int32 = append(mask.Int32, 1)
			} else {
				ids.Int32 = append(ids.Int32, pad)
				mask.Int32 = append(mask.Int32, 0)
			}
		}
	}
	return encodeRun(t, &labeler.RunRequest{ID: id, Inputs: []labeler.Tensor{ids, mask}, Outputs: outputs})
}

func encodeRun(t *testing.T, req *labeler.RunRequest) []byte {
	t.Helper()
	payload, err := labeler.EncodeRun(req)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func key(row []int32) float32 {
	var k int64
	for _, id := range row {
		k += int64(id) + 1000
	}
	return float32(k)
}

func TestHandshake(t *testing.T) {
	h := startWorker(t, &fakeSession{info: fakeInfo()})
	hello := h.hello()
	hello.Protocol = []int{2, 1, 3}
	hello.ModelSHA256 = strings.ToUpper(h.sum)
	hello.Options = map[string]any{"unknown_option": true}
	h.sendJSON(labeler.FrameHello, hello)
	kind, payload := h.recv()
	var ready labeler.Ready
	if kind != labeler.FrameReady || json.Unmarshal(payload, &ready) != nil {
		t.Fatalf("frame %q %s", kind, payload)
	}
	cfg := <-h.opened
	if cfg.Model != h.model || cfg.Threads != 3 {
		t.Fatalf("opened %+v", cfg)
	}
	if ready.Protocol != 1 || ready.Worker.Name != "pharos-labeler-ort" || ready.Worker.Version == "" || ready.Worker.Runtime != "fake 1" ||
		ready.Worker.Device != "cpu" || !slices.Equal(ready.Worker.Providers, []string{"FakeProvider"}) {
		t.Fatalf("worker %+v", ready.Worker)
	}
	if ready.Model.Path != h.model || ready.Model.SHA256 != h.sum {
		t.Fatalf("model %s %s", ready.Model.Path, ready.Model.SHA256)
	}
	if len(ready.Model.Inputs) != 2 || ready.Model.Inputs[0].Name != "input_ids" || ready.Model.Inputs[1].DType != "int64" ||
		len(ready.Model.Outputs) != 2 || ready.Model.Outputs[0].Name != "logits" || ready.Model.Outputs[0].Shape[2] != float64(3) {
		t.Fatalf("model tensors %+v %+v", ready.Model.Inputs, ready.Model.Outputs)
	}
	if ready.Limits.MaxBatch != 8 || ready.Limits.MaxSeq != 16 || ready.Limits.MaxFrameBytes != labeler.MaxFrameBytes {
		t.Fatalf("limits %+v", ready.Limits)
	}
	if !slices.Contains(ready.Capabilities, "ping") {
		t.Fatalf("capabilities %v", ready.Capabilities)
	}
	// A second hello is refused, and the worker keeps serving the first.
	h.sendJSON(labeler.FrameHello, hello)
	h.expectError(0, labeler.ErrCodeProtocol, false)
	h.send(labeler.FrameRun, runPayload(t, 1, [][]int32{{50281, 50282}}))
	h.expectResult()
}

func TestHelloRefusals(t *testing.T) {
	floatInput := fakeInfo()
	floatInput.Inputs[1].DType = "float32"
	extraInput := fakeInfo()
	extraInput.Inputs = append(extraInput.Inputs, labeler.TensorInfo{Name: "token_type_ids", DType: "int64", Shape: []any{"batch", "seq"}})
	intOutput := fakeInfo()
	intOutput.Outputs[0].DType = "int64"
	cases := []struct {
		name   string
		info   modelInfo
		hello  func(*harness) []byte
		open   error
		code   string
		status int
	}{
		{name: "not JSON", hello: func(*harness) []byte { return []byte("{") }, code: labeler.ErrCodeProtocol, status: exitProtocol},
		{name: "no common version", hello: func(h *harness) []byte { x := h.hello(); x.Protocol = []int{2, 3}; return mustJSON(x) },
			code: labeler.ErrCodeProtocol, status: exitProtocol},
		{name: "no versions", hello: func(h *harness) []byte { x := h.hello(); x.Protocol = nil; return mustJSON(x) },
			code: labeler.ErrCodeProtocol, status: exitProtocol},
		{name: "no model", hello: func(h *harness) []byte { x := h.hello(); x.Model = ""; return mustJSON(x) },
			code: labeler.ErrCodeModel, status: exitFatal},
		{name: "missing model file", hello: func(h *harness) []byte { x := h.hello(); x.Model += ".missing"; return mustJSON(x) },
			code: labeler.ErrCodeModel, status: exitFatal},
		{name: "checksum mismatch", hello: func(h *harness) []byte { x := h.hello(); x.ModelSHA256 = strings.Repeat("0", 64); return mustJSON(x) },
			code: labeler.ErrCodeModel, status: exitFatal},
		{name: "runtime fails to load", open: errors.New("no runtime"), code: labeler.ErrCodeModel, status: exitFatal},
		{name: "float model input", info: floatInput, code: labeler.ErrCodeModel, status: exitFatal},
		{name: "model input not declared", info: extraInput, code: labeler.ErrCodeModel, status: exitFatal},
		{name: "declared input not in the model", hello: declare(append(slices.Clone(labeler.DefaultInputs), labeler.InputSpec{Name: "pooling_mask", Role: labeler.RolePool})),
			code: labeler.ErrCodeModel, status: exitFatal},
		{name: "declared input renamed", hello: declare([]labeler.InputSpec{{Name: "input_ids", Role: labeler.RoleIDs}, {Name: "mask", Role: labeler.RoleAttention}}),
			code: labeler.ErrCodeModel, status: exitFatal},
		{name: "declaration without attention", hello: declare([]labeler.InputSpec{{Name: "input_ids", Role: labeler.RoleIDs}, {Name: "attention_mask", Role: labeler.RolePool}}),
			code: labeler.ErrCodeProtocol, status: exitProtocol},
		{name: "declaration with an unknown role", hello: declare([]labeler.InputSpec{{Name: "input_ids", Role: labeler.RoleIDs}, {Name: "attention_mask", Role: labeler.RoleAttention}, {Name: "segment", Role: "type"}}),
			code: labeler.ErrCodeProtocol, status: exitProtocol},
		{name: "declaration naming an input twice", hello: declare([]labeler.InputSpec{{Name: "input_ids", Role: labeler.RoleIDs}, {Name: "attention_mask", Role: labeler.RoleAttention}, {Name: "input_ids", Role: labeler.RolePool}}),
			code: labeler.ErrCodeProtocol, status: exitProtocol},
		{name: "integer model output", info: intOutput, code: labeler.ErrCodeModel, status: exitFatal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := c.info
			if info.Inputs == nil {
				info = fakeInfo()
			}
			sess := &fakeSession{info: info}
			h := newHarness(t).start(func(sessionConfig) (session, error) { return sess, c.open })
			payload := mustJSON(h.hello())
			if c.hello != nil {
				payload = c.hello(h)
			}
			h.send(labeler.FrameHello, payload)
			e := h.expectError(0, c.code, true)
			t.Logf("%s", e.Message)
			h.expectExit(c.status)
			if c.info.Inputs != nil && !sess.closed.Load() {
				t.Fatal("a refused model was left open")
			}
		})
	}
	t.Run("run before hello", func(t *testing.T) {
		h := startWorker(t, &fakeSession{info: fakeInfo()})
		h.send(labeler.FrameRun, runPayload(t, 4, [][]int32{{1}}))
		h.expectError(0, labeler.ErrCodeProtocol, true)
		h.expectExit(exitProtocol)
	})
	t.Run("panic while loading", func(t *testing.T) {
		h := newHarness(t).start(func(sessionConfig) (session, error) { panic("loader bug") })
		h.sendJSON(labeler.FrameHello, h.hello())
		h.expectError(0, labeler.ErrCodeInternal, true)
		h.expectExit(exitFatal)
	})
}

// declare is a hello with inputs declared.
func declare(inputs []labeler.InputSpec) func(*harness) []byte {
	return func(h *harness) []byte {
		x := h.hello()
		x.Inputs = inputs
		return mustJSON(x)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestRun(t *testing.T) {
	var got []fakeCall
	sess := &fakeSession{info: fakeInfo()}
	sess.run = func(c fakeCall) ([]labeler.Tensor, error) {
		got = append(got, c)
		return fakeOutputs(c), nil
	}
	h := startWorker(t, sess)
	h.handshake()
	rows := [][]int32{{50281, 7, 50367, 50282}, {50281, 0, 50282}}

	h.send(labeler.FrameRun, runPayload(t, 11, rows))
	res := h.expectResult()
	if res.ID != 11 || len(res.Outputs) != 2 || res.Outputs[0].Name != "logits" || res.Outputs[1].Name != "probs" {
		t.Fatalf("result %+v", res)
	}
	logits, probs := res.Tensor("logits"), res.Tensor("probs")
	if !slices.Equal(logits.Dims, []int{2, 2, 3}) || !slices.Equal(probs.Dims, []int{2, 3}) {
		t.Fatalf("dims %v %v", logits.Dims, probs.Dims)
	}
	for r, row := range rows {
		if probs.Float32[3*r+2] != key(row)+2 || logits.Float32[6*r+5] != key(row)+12 {
			t.Fatalf("row %d: probs %v logits %v, want key %v", r, probs.Float32, logits.Float32, key(row))
		}
	}
	// The model saw the ids and mask as int64, padding included.
	c := got[0]
	if c.batch != 2 || c.seq != 4 || !slices.Equal(c.ids, []int64{50281, 7, 50367, 50282, 50281, 0, 50282, pad}) ||
		!slices.Equal(c.mask, []int64{1, 1, 1, 1, 1, 1, 1, 0}) {
		t.Fatalf("model call %+v", c)
	}

	h.send(labeler.FrameRun, runPayload(t, 12, rows, "probs"))
	res = h.expectResult()
	if res.ID != 12 || len(res.Outputs) != 1 || res.Outputs[0].Name != "probs" || !slices.Equal(got[1].outputs, []string{"probs"}) {
		t.Fatalf("probs only: %+v, model asked for %v", res, got[1].outputs)
	}
	h.send(labeler.FrameRun, runPayload(t, 13, rows, "probs", "logits"))
	res = h.expectResult()
	if res.Outputs[0].Name != "probs" || res.Outputs[1].Name != "logits" {
		t.Fatalf("outputs in the order asked: %+v", res)
	}
}

// TestDeclaredInputs runs a model with a third input, pooling_mask, that the
// hello declares (in another order than the model's), and an attention mask
// the model takes as int32.
func TestDeclaredInputs(t *testing.T) {
	info := fakeInfo()
	info.Inputs[1].DType = "int32"
	info.Inputs = append(info.Inputs, labeler.TensorInfo{Name: "pooling_mask", DType: "int64", Shape: []any{"batch", "seq"}})
	var got []fakeCall
	sess := &fakeSession{info: info}
	sess.run = func(c fakeCall) ([]labeler.Tensor, error) {
		got = append(got, c)
		return fakeOutputs(c), nil
	}
	h := startWorker(t, sess)
	hello := h.hello()
	hello.Inputs = []labeler.InputSpec{
		{Name: "pooling_mask", Role: labeler.RolePool},
		{Name: "attention_mask", Role: labeler.RoleAttention},
		{Name: "input_ids", Role: labeler.RoleIDs},
	}
	h.sendJSON(labeler.FrameHello, hello)
	if kind, payload := h.recv(); kind != labeler.FrameReady {
		t.Fatalf("frame %q %s", kind, payload)
	}
	tensor := func(name string, values ...int32) labeler.Tensor {
		return labeler.Tensor{Name: name, DType: labeler.DTypeInt32, Dims: []int{2, 3}, Int32: values}
	}
	ids := tensor("input_ids", 50281, 9, 50282, 50281, 50282, pad)
	mask := tensor("attention_mask", 1, 1, 1, 1, 1, 0)
	pool := tensor("pooling_mask", 0, 1, 0, 1, 1, 0)
	run := func(id uint64, inputs ...labeler.Tensor) []byte {
		return encodeRun(t, &labeler.RunRequest{ID: id, Inputs: inputs})
	}

	h.send(labeler.FrameRun, run(1, pool, ids, mask))
	if res := h.expectResult(); res.ID != 1 {
		t.Fatalf("result %d", res.ID)
	}
	c := got[0]
	var names []string
	for _, in := range c.inputs {
		names = append(names, in.Name)
	}
	if !slices.Equal(names, []string{"input_ids", "attention_mask", "pooling_mask"}) {
		t.Fatalf("the model got inputs %v, not in its own order", names)
	}
	if c.inputs[0].Int64 == nil || c.inputs[1].Int32 == nil || c.inputs[1].Int64 != nil || c.inputs[2].Int64 == nil {
		t.Fatalf("inputs not in the model's types: %+v", c.inputs)
	}
	if !slices.Equal(c.inputs[1].Int32, mask.Int32) || !slices.Equal(c.values("pooling_mask"), []int64{0, 1, 0, 1, 1, 0}) ||
		!slices.Equal(c.ids, []int64{50281, 9, 50282, 50281, 50282, pad}) {
		t.Fatalf("model call %+v", c)
	}

	bad := []struct {
		name   string
		inputs []labeler.Tensor
	}{
		{"declared input missing", []labeler.Tensor{ids, mask}},
		{"undeclared input", []labeler.Tensor{ids, mask, pool, tensor("token_type_ids", 0, 0, 0, 0, 0, 0)}},
		{"input twice", []labeler.Tensor{ids, mask, pool, pool}},
		{"pooling padding", []labeler.Tensor{ids, mask, tensor("pooling_mask", 0, 1, 0, 1, 1, 1)}},
		{"pooling nothing in a row", []labeler.Tensor{ids, mask, tensor("pooling_mask", 0, 1, 0, 0, 0, 0)}},
		{"pool value 2", []labeler.Tensor{ids, mask, tensor("pooling_mask", 0, 2, 0, 1, 1, 0)}},
	}
	for i, b := range bad {
		id := uint64(10 + i)
		h.send(labeler.FrameRun, run(id, b.inputs...))
		e := h.expectError(id, labeler.ErrCodeBadRequest, false)
		t.Logf("%s: %s", b.name, e.Message)
	}
	if len(got) != 1 {
		t.Fatalf("the model ran %d requests, want 1", len(got))
	}
	h.send(labeler.FrameRun, run(20, ids, mask, pool))
	if res := h.expectResult(); res.ID != 20 {
		t.Fatalf("result %d", res.ID)
	}
}

func TestBadRequests(t *testing.T) {
	sess := &fakeSession{info: fakeInfo()}
	h := startWorker(t, sess)
	ready := h.handshake()
	valid := func(id uint64) *labeler.RunRequest {
		return &labeler.RunRequest{ID: id, Inputs: []labeler.Tensor{
			{Name: "input_ids", DType: labeler.DTypeInt32, Dims: []int{2, 3}, Int32: []int32{50281, 9, 50282, 50281, 50282, pad}},
			{Name: "attention_mask", DType: labeler.DTypeInt32, Dims: []int{2, 3}, Int32: []int32{1, 1, 1, 1, 1, 0}},
		}}
	}
	tooLong := ready.Limits.MaxSeq + 1
	cases := []struct {
		name    string
		id      uint64
		payload func(id uint64) []byte
	}{
		{"id truncated", 0, func(uint64) []byte { return []byte{1, 2, 3} }},
		{"body truncated", 21, func(id uint64) []byte { p := encodeRun(t, valid(id)); return p[:len(p)-5] }},
		{"trailing bytes", 22, func(id uint64) []byte { return append(encodeRun(t, valid(id)), 0) }},
		{"unknown input", 23, func(id uint64) []byte {
			r := valid(id)
			r.Inputs = append(r.Inputs, labeler.Tensor{Name: "token_type_ids", DType: labeler.DTypeInt32, Dims: []int{2, 3}, Int32: make([]int32, 6)})
			return encodeRun(t, r)
		}},
		{"missing mask", 24, func(id uint64) []byte { r := valid(id); r.Inputs = r.Inputs[:1]; return encodeRun(t, r) }},
		{"float input", 25, func(id uint64) []byte {
			r := valid(id)
			r.Inputs[1] = labeler.Tensor{Name: "attention_mask", DType: labeler.DTypeFloat32, Dims: []int{2, 3}, Float32: []float32{1, 1, 1, 1, 1, 0}}
			return encodeRun(t, r)
		}},
		{"rank 3", 26, func(id uint64) []byte {
			r := valid(id)
			r.Inputs[0].Dims, r.Inputs[1].Dims = []int{1, 2, 3}, []int{1, 2, 3}
			return encodeRun(t, r)
		}},
		{"shapes differ", 27, func(id uint64) []byte { r := valid(id); r.Inputs[1].Dims = []int{3, 2}; return encodeRun(t, r) }},
		{"empty batch", 28, func(id uint64) []byte {
			r := valid(id)
			for i := range r.Inputs {
				r.Inputs[i].Dims, r.Inputs[i].Int32 = []int{0, 3}, nil
			}
			return encodeRun(t, r)
		}},
		{"negative token id", 29, func(id uint64) []byte { r := valid(id); r.Inputs[0].Int32[1] = -1; return encodeRun(t, r) }},
		{"token id past the vocabulary", 30, func(id uint64) []byte { r := valid(id); r.Inputs[0].Int32[1] = 50368; return encodeRun(t, r) }},
		{"mask value 2", 31, func(id uint64) []byte { r := valid(id); r.Inputs[1].Int32[1] = 2; return encodeRun(t, r) }},
		{"row with no tokens", 32, func(id uint64) []byte {
			r := valid(id)
			copy(r.Inputs[1].Int32[3:], []int32{0, 0, 0})
			return encodeRun(t, r)
		}},
		{"batch over the limit", 33, func(id uint64) []byte {
			rows := make([][]int32, ready.Limits.MaxBatch+1)
			for i := range rows {
				rows[i] = []int32{50281, 50282}
			}
			return runPayload(t, id, rows)
		}},
		{"sequence over the limit", 34, func(id uint64) []byte { return runPayload(t, id, [][]int32{make([]int32, tooLong)}) }},
		{"unknown output", 35, func(id uint64) []byte {
			r := valid(id)
			r.Outputs = []string{"probs", "hidden"}
			return encodeRun(t, r)
		}},
		{"output twice", 36, func(id uint64) []byte { r := valid(id); r.Outputs = []string{"probs", "probs"}; return encodeRun(t, r) }},
	}
	for _, c := range cases {
		h.send(labeler.FrameRun, c.payload(c.id))
		e := h.expectError(c.id, labeler.ErrCodeBadRequest, false)
		t.Logf("%s: %s", c.name, e.Message)
	}
	if n := sess.calls.Load(); n != 0 {
		t.Fatalf("the model ran %d bad requests", n)
	}
	h.send(labeler.FrameRun, encodeRun(t, valid(40)))
	if res := h.expectResult(); res.ID != 40 {
		t.Fatalf("result %d after bad requests", res.ID)
	}
}

// gatedSession blocks each run until the test releases it, and reports each
// run as it starts.
type gatedSession struct {
	fakeSession
	started chan uint64 // the first id of each run's first row
	release chan struct{}
}

func newGatedSession() *gatedSession {
	g := &gatedSession{fakeSession: fakeSession{info: fakeInfo()}, started: make(chan uint64, 16), release: make(chan struct{})}
	g.run = func(c fakeCall) ([]labeler.Tensor, error) {
		g.started <- uint64(c.ids[1])
		<-g.release
		return fakeOutputs(c), nil
	}
	return g
}

func (h *harness) expectStarted(g *gatedSession, marker uint64) {
	h.t.Helper()
	select {
	case got := <-g.started:
		if got != marker {
			h.t.Fatalf("run %d started, want %d", got, marker)
		}
	case <-time.After(wait):
		h.t.Fatalf("run %d did not start", marker)
	}
}

func TestRepliesMatchByID(t *testing.T) {
	g := newGatedSession()
	h := startWorker(t, &g.fakeSession)
	h.handshake()
	// Each request's second id marks it, so that the result shows which
	// request it came from.
	h.send(labeler.FrameRun, runPayload(t, 7, [][]int32{{50281, 7, 50282}}))
	h.expectStarted(g, 7)
	h.send(labeler.FrameRun, runPayload(t, 9, [][]int32{{50281, 9, 50282}}))
	h.send(labeler.FrameRun, runPayload(t, 3, [][]int32{{50281, -5, 50282}}))
	// The bad request is answered while 7 still runs, ahead of 7 and 9.
	h.expectError(3, labeler.ErrCodeBadRequest, false)
	close(g.release)
	for _, id := range []uint64{7, 9} {
		res := h.expectResult()
		if res.ID != id || res.Tensor("probs").Float32[0] != key([]int32{50281, int32(id), 50282}) {
			t.Fatalf("result %d with probs %v, want %d", res.ID, res.Tensor("probs").Float32, id)
		}
	}
}

func TestPing(t *testing.T) {
	g := newGatedSession()
	h := startWorker(t, &g.fakeSession)
	h.send(labeler.FramePing, labeler.EncodeNonce(1))
	h.expectPong(1)
	h.handshake()
	h.send(labeler.FrameRun, runPayload(t, 5, [][]int32{{50281, 5, 50282}}))
	h.expectStarted(g, 5)
	// Pings are answered while the run is still in progress.
	for _, nonce := range []uint64{0xfeedface, 1 << 63} {
		h.send(labeler.FramePing, labeler.EncodeNonce(nonce))
		h.expectPong(nonce)
	}
	h.send(labeler.FramePing, []byte{1, 2, 3})
	h.expectError(0, labeler.ErrCodeBadRequest, false)
	close(g.release)
	if res := h.expectResult(); res.ID != 5 {
		t.Fatalf("result %d", res.ID)
	}
}

func TestUnknownFrameType(t *testing.T) {
	h := startWorker(t, &fakeSession{info: fakeInfo()})
	for _, kind := range []byte{'X', labeler.FrameResult, 0} {
		h.send(kind, []byte("payload to skip"))
		h.expectError(0, labeler.ErrCodeUnsupported, false)
	}
	h.send('Z', nil)
	h.expectError(0, labeler.ErrCodeUnsupported, false)
	h.handshake()
	h.send(labeler.FrameRun, runPayload(t, 2, [][]int32{{50281, 50282}}))
	h.expectResult()
}

func TestBrokenFraming(t *testing.T) {
	header := func(n uint32) []byte { return binary.LittleEndian.AppendUint32(nil, n) }
	cases := []struct {
		name   string
		stream []byte
	}{
		{"zero length", header(0)},
		{"over 64 MiB", append(header(labeler.MaxFrameBytes+1), 'P')},
		{"ends inside a frame", append(header(100), labeler.FramePing, 1, 2, 3)},
		{"ends inside a header", []byte{9, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := startWorker(t, &fakeSession{info: fakeInfo()})
			h.handshake()
			h.writeRaw(c.stream)
			h.in.Close()
			h.expectError(0, labeler.ErrCodeBadFrame, true)
			h.expectExit(exitProtocol)
		})
	}
}

func TestQuit(t *testing.T) {
	t.Run("before hello", func(t *testing.T) {
		h := startWorker(t, &fakeSession{info: fakeInfo()})
		h.send(labeler.FrameQuit, nil)
		if kind, _ := h.recv(); kind != labeler.FrameGoodbye {
			t.Fatalf("frame %q", kind)
		}
		h.expectExit(exitOK)
	})
	t.Run("finishes runs already read", func(t *testing.T) {
		g := newGatedSession()
		h := startWorker(t, &g.fakeSession)
		h.handshake()
		h.send(labeler.FrameRun, runPayload(t, 1, [][]int32{{50281, 1, 50282}}))
		h.send(labeler.FrameRun, runPayload(t, 2, [][]int32{{50281, 2, 50282}}))
		h.expectStarted(g, 1)
		h.send(labeler.FrameQuit, nil)
		close(g.release)
		for _, id := range []uint64{1, 2} {
			if res := h.expectResult(); res.ID != id {
				t.Fatalf("result %d, want %d", res.ID, id)
			}
		}
		if kind, _ := h.recv(); kind != labeler.FrameGoodbye {
			t.Fatalf("frame %q, want goodbye", kind)
		}
		h.expectExit(exitOK)
		if !g.closed.Load() {
			t.Fatal("the model was not closed")
		}
	})
}

func TestEndOfInput(t *testing.T) {
	t.Run("before hello", func(t *testing.T) {
		h := startWorker(t, &fakeSession{info: fakeInfo()})
		h.in.Close()
		h.expectExit(exitOK)
	})
	t.Run("during a run", func(t *testing.T) {
		g := newGatedSession()
		defer close(g.release)
		h := startWorker(t, &g.fakeSession)
		h.handshake()
		h.send(labeler.FrameRun, runPayload(t, 1, [][]int32{{50281, 1, 50282}}))
		h.expectStarted(g, 1)
		// The worker shuts down without waiting for the run to end.
		h.in.Close()
		h.expectExit(exitOK)
	})
}

func TestRunFailures(t *testing.T) {
	sess := &fakeSession{info: fakeInfo()}
	sess.run = func(c fakeCall) ([]labeler.Tensor, error) {
		switch c.ids[1] {
		case 1:
			return nil, errors.New("runtime refused")
		case 2:
			panic("runtime bug")
		case 3:
			return fakeOutputs(c)[:1], nil
		}
		return fakeOutputs(c), nil
	}
	h := startWorker(t, sess)
	h.handshake()
	h.send(labeler.FrameRun, runPayload(t, 101, [][]int32{{50281, 1, 50282}}))
	if e := h.expectError(101, labeler.ErrCodeRunFailed, false); !strings.Contains(e.Message, "runtime refused") {
		t.Fatalf("message %q", e.Message)
	}
	h.send(labeler.FrameRun, runPayload(t, 102, [][]int32{{50281, 2, 50282}}))
	h.expectError(102, labeler.ErrCodeInternal, false)
	h.send(labeler.FrameRun, runPayload(t, 103, [][]int32{{50281, 3, 50282}}))
	h.expectError(103, labeler.ErrCodeInternal, false)
	h.send(labeler.FrameRun, runPayload(t, 104, [][]int32{{50281, 4, 50282}}))
	if res := h.expectResult(); res.ID != 104 {
		t.Fatalf("result %d", res.ID)
	}
}

func TestLimits(t *testing.T) {
	kestrel := []labeler.TensorInfo{
		{Name: "logits", DType: "float32", Shape: []any{"batch", 5, 54}},
		{Name: "probs", DType: "float32", Shape: []any{"batch", 54}},
	}
	perToken := []labeler.TensorInfo{{Name: "hidden", DType: "float32", Shape: []any{"batch", "seq", 768}}}
	cases := []struct {
		name               string
		hello              labeler.Hello
		inputs             int
		outputs            []labeler.TensorInfo
		wantBatch, wantSeq int // 0: only check that frames fit
	}{
		{"defaults", labeler.Hello{}, 2, kestrel, defaultMaxBatch, defaultMaxSeq},
		{"as asked", labeler.Hello{MaxBatch: 64, MaxSeq: 256}, 2, kestrel, 64, 256},
		{"batch lowered to fit a request", labeler.Hello{MaxBatch: 1 << 20, MaxSeq: 512}, 2, kestrel, 0, 512},
		{"batch lowered further for a third input", labeler.Hello{MaxBatch: 1 << 20, MaxSeq: 512}, 3, kestrel, 0, 512},
		{"sequence lowered to fit one row", labeler.Hello{MaxBatch: 4, MaxSeq: 1 << 30}, 2, kestrel, 1, 0},
		{"batch lowered to fit a result", labeler.Hello{MaxBatch: 1024, MaxSeq: 512}, 2, perToken, 0, 512},
	}
	for _, c := range cases {
		request := func(b, s int) int { return 4*c.inputs*b*s + 4096 }
		batch, seq := limits(&c.hello, c.inputs, c.outputs)
		if (c.wantBatch != 0 && batch != c.wantBatch) || (c.wantSeq != 0 && seq != c.wantSeq) {
			t.Errorf("%s: limits %d, %d, want %d, %d", c.name, batch, seq, c.wantBatch, c.wantSeq)
		}
		result := batch * 4 * seq * 768
		if batch < 1 || seq < 1 || request(batch, seq) > labeler.MaxFrameBytes || (len(c.outputs) == 1 && result > labeler.MaxFrameBytes) {
			t.Errorf("%s: limits %d, %d do not fit a frame", c.name, batch, seq)
		}
	}
}
