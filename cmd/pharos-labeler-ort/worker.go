package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/gbdubs/pharos/internal/labeler"
)

// The protocol loop, written against session so that it runs without ONNX
// Runtime in tests. The spec is "Worker protocol" in docs/local-labeler.md;
// internal/labeler/protocol.go is the encoder and decoder.

// session runs one loaded model. Run is called from one goroutine at a time.
type session interface {
	// Info describes the runtime and the model's inputs and outputs.
	Info() modelInfo
	// Run runs one batch. inputs holds every model input, row-major
	// [batch, seq]; outputs names the model outputs to return, in that
	// order, and is never empty.
	Run(inputs []modelInput, batch, seq int, outputs []string) ([]labeler.Tensor, error)
	// Close releases the model.
	Close() error
}

// modelInput is one model input for Run, by the model's name for it. Int64
// holds its data when the model takes int64, Int32 when it takes int32.
type modelInput struct {
	Name  string
	Int64 []int64
	Int32 []int32
}

type modelInfo struct {
	Runtime   string // for example "onnxruntime 1.31.1"
	Device    string
	Providers []string
	Inputs    []labeler.TensorInfo
	Outputs   []labeler.TensorInfo
}

type sessionConfig struct {
	Model   string
	Threads int
}

// opener loads a model. Its errors are reported with code "model".
type opener func(sessionConfig) (session, error)

// Limits used when the hello sets none.
const (
	defaultMaxBatch = 256
	defaultMaxSeq   = 512
	// runQueue bounds the run requests read but not yet run. Pharos keeps two
	// in flight; past this the reader waits, and so do pings.
	runQueue = 32
)

// Exit statuses of serve.
const (
	exitOK       = 0
	exitFatal    = 1 // the model failed to load, an internal error, or stdout broke
	exitProtocol = 2 // broken framing or a protocol violation
)

// fatalError is a fatal reply and the status to exit with after sending it.
type fatalError struct {
	reply labeler.ErrorReply
	exit  int
}

func (e *fatalError) Error() string { return e.reply.Error() }

func fatal(code, format string, args ...any) *fatalError {
	exit := exitFatal
	if code == labeler.ErrCodeBadFrame || code == labeler.ErrCodeProtocol {
		exit = exitProtocol
	}
	return &fatalError{reply: labeler.ErrorReply{Code: code, Message: fmt.Sprintf(format, args...), Fatal: true}, exit: exit}
}

// replies serializes frames to stdout from the loop and the runner. The first
// write error closes broken; the stream is unusable after it.
type replies struct {
	mu     sync.Mutex
	w      *bufio.Writer
	err    error
	broken chan struct{}
}

func newReplies(w io.Writer) *replies {
	return &replies{w: bufio.NewWriterSize(w, 1<<16), broken: make(chan struct{})}
}

func (r *replies) frame(kind byte, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	err := labeler.WriteFrame(r.w, kind, payload)
	if err == nil {
		err = r.w.Flush()
	}
	if err != nil {
		r.err = err
		close(r.broken)
	}
}

func (r *replies) json(kind byte, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		// Only the worker's own types are marshalled here.
		panic(err)
	}
	r.frame(kind, payload)
}

func (r *replies) error(id uint64, code, format string, args ...any) {
	r.json(labeler.FrameError, labeler.ErrorReply{ID: id, Code: code, Message: fmt.Sprintf(format, args...)})
}

// job is a validated run request waiting for the runner. inputs are the
// request's tensors in the model's input order.
type job struct {
	id      uint64
	batch   int
	seq     int
	inputs  []labeler.Tensor
	outputs []string
}

type worker struct {
	open  opener
	out   *replies
	log   io.Writer
	hello *labeler.Hello
	sess  session
	info  modelInfo
	ready labeler.Ready
	queue chan job
	done  chan struct{} // closed when the runner has drained queue
}

type frame struct {
	kind    byte
	payload []byte
	err     error
}

// serve speaks pharos-labeler/1 on in and out until a quit, the end of in, or
// a fatal error, and returns the exit status. Diagnostics go to logw.
func serve(in io.Reader, out io.Writer, logw io.Writer, open opener) int {
	w := &worker{open: open, out: newReplies(out), log: logw}
	frames := make(chan frame)
	go readFrames(labeler.NewFrameReader(in), frames)
	for {
		var f frame
		select {
		case f = <-frames:
		case <-w.out.broken:
			fmt.Fprintf(w.log, "pharos-labeler-ort: writing replies: %v\n", w.out.err)
			return exitFatal
		}
		status, done := w.handle(f)
		if done {
			return status
		}
	}
}

// readFrames sends every frame of r to frames, then one frame carrying the
// error that ended the stream (io.EOF at a clean end).
func readFrames(r io.Reader, frames chan<- frame) {
	for {
		kind, payload, err := labeler.ReadFrame(r, labeler.MaxFrameBytes)
		frames <- frame{kind: kind, payload: payload, err: err}
		if err != nil {
			return
		}
	}
}

// handle acts on one frame read from the stream. done reports that serve
// should return status.
func (w *worker) handle(f frame) (status int, done bool) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(w.log, "pharos-labeler-ort: panic: %v\n%s", p, debug.Stack())
			reply := labeler.ErrorReply{Code: labeler.ErrCodeInternal, Message: fmt.Sprintf("panic: %v", p), Fatal: true}
			w.out.json(labeler.FrameError, reply)
			status, done = exitFatal, true
		}
	}()
	if f.err != nil {
		if errors.Is(f.err, io.EOF) {
			// End of input means shut down. Runs still queued or running are
			// abandoned: nobody is left to ask for them.
			return exitOK, true
		}
		if errors.Is(f.err, labeler.ErrFrameTooLarge) || errors.Is(f.err, io.ErrUnexpectedEOF) {
			return w.fail(fatal(labeler.ErrCodeBadFrame, "%v", f.err))
		}
		fmt.Fprintf(w.log, "pharos-labeler-ort: reading requests: %v\n", f.err)
		return exitFatal, true
	}
	switch f.kind {
	case labeler.FrameHello:
		if w.hello != nil {
			w.out.error(0, labeler.ErrCodeProtocol, "hello already received; a worker serves one model")
			return 0, false
		}
		if err := w.start(f.payload); err != nil {
			return w.fail(err)
		}
		w.out.json(labeler.FrameReady, w.ready)
	case labeler.FrameRun:
		if w.hello == nil {
			return w.fail(fatal(labeler.ErrCodeProtocol, "run request before hello"))
		}
		w.enqueue(f.payload)
	case labeler.FramePing:
		if _, err := labeler.DecodeNonce(f.payload); err != nil {
			w.out.error(0, labeler.ErrCodeBadRequest, "ping: %v", err)
			return 0, false
		}
		w.out.frame(labeler.FramePong, f.payload)
	case labeler.FrameQuit:
		if w.queue != nil {
			close(w.queue)
			select {
			case <-w.done:
			case <-w.out.broken:
				return exitFatal, true
			}
			if err := w.sess.Close(); err != nil {
				fmt.Fprintf(w.log, "pharos-labeler-ort: closing the model: %v\n", err)
			}
		}
		w.out.frame(labeler.FrameGoodbye, nil)
		return exitOK, true
	default:
		w.out.error(0, labeler.ErrCodeUnsupported, "unknown frame type %q (%d payload bytes skipped)", f.kind, len(f.payload))
	}
	return 0, false
}

func (w *worker) fail(err error) (int, bool) {
	var f *fatalError
	if !errors.As(err, &f) {
		f = fatal(labeler.ErrCodeInternal, "%v", err)
	}
	fmt.Fprintf(w.log, "pharos-labeler-ort: %s: %s\n", f.reply.Code, f.reply.Message)
	w.out.json(labeler.FrameError, f.reply)
	return f.exit, true
}

// start handles the hello: it negotiates the version, checks and loads the
// model, and fills w.ready. Every error it returns is fatal.
func (w *worker) start(payload []byte) error {
	var hello labeler.Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return fatal(labeler.ErrCodeProtocol, "hello: %v", err)
	}
	if !slices.Contains(hello.Protocol, labeler.ProtocolVersion) {
		return fatal(labeler.ErrCodeProtocol, "hello offers protocol versions %v; this worker speaks %d", hello.Protocol, labeler.ProtocolVersion)
	}
	if err := labeler.ValidateInputs(hello.RunInputs()); err != nil {
		return fatal(labeler.ErrCodeProtocol, "hello inputs: %v", err)
	}
	if hello.Model == "" {
		return fatal(labeler.ErrCodeModel, "hello names no model")
	}
	sum, err := fileSHA256(hello.Model)
	if err != nil {
		return fatal(labeler.ErrCodeModel, "%v", err)
	}
	if want := hello.ModelSHA256; want != "" && !strings.EqualFold(want, sum) {
		return fatal(labeler.ErrCodeModel, "%s has SHA-256 %s, not the expected %s", hello.Model, sum, want)
	}
	sess, err := w.open(sessionConfig{Model: hello.Model, Threads: hello.Threads})
	if err != nil {
		return fatal(labeler.ErrCodeModel, "%v", err)
	}
	info := sess.Info()
	if err := checkModel(info, hello.RunInputs()); err != nil {
		sess.Close()
		return fatal(labeler.ErrCodeModel, "%s: %v", hello.Model, err)
	}
	w.hello, w.sess, w.info = &hello, sess, info

	r := &w.ready
	r.Protocol = labeler.ProtocolVersion
	r.Worker = labeler.WorkerInfo{Name: workerName, Version: workerVersion, Runtime: info.Runtime, Device: info.Device, Providers: info.Providers}
	r.Model.Path = hello.Model
	r.Model.SHA256 = sum
	r.Model.Inputs = info.Inputs
	r.Model.Outputs = info.Outputs
	r.Limits.MaxBatch, r.Limits.MaxSeq = limits(&hello, len(info.Inputs), info.Outputs)
	r.Limits.MaxFrameBytes = labeler.MaxFrameBytes
	r.Capabilities = []string{"ping"}

	w.queue = make(chan job, runQueue)
	w.done = make(chan struct{})
	go w.runner()
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkModel requires the model's inputs to be exactly the ones the hello
// declares, each int64 or int32 [batch, seq] (the worker converts the wire's
// int32 to the model's type), and float32 outputs, the only output type on
// the wire.
func checkModel(info modelInfo, declared []labeler.InputSpec) error {
	var model, hello []string
	for _, in := range info.Inputs {
		model = append(model, in.Name)
	}
	for _, in := range declared {
		hello = append(hello, in.Name)
	}
	mismatch := len(model) != len(hello)
	for _, name := range hello {
		mismatch = mismatch || !slices.Contains(model, name)
	}
	if mismatch {
		return fmt.Errorf("the hello declares inputs %v but the model's inputs are %v", hello, model)
	}
	for _, in := range info.Inputs {
		if (in.DType != "int64" && in.DType != "int32") || len(in.Shape) != 2 {
			return fmt.Errorf("model input %s is %s rank %d, not int64 or int32 [batch, seq]", in.Name, in.DType, len(in.Shape))
		}
	}
	if len(info.Outputs) == 0 {
		return errors.New("model has no outputs")
	}
	for _, out := range info.Outputs {
		if out.DType != "float32" {
			return fmt.Errorf("model output %s is %s, not float32", out.Name, out.DType)
		}
	}
	return nil
}

// limits is the hello's batch and sequence limits, or the defaults, lowered
// so that the largest request and result each fit in one frame.
func limits(hello *labeler.Hello, inputs int, outputs []labeler.TensorInfo) (maxBatch, maxSeq int) {
	maxBatch, maxSeq = hello.MaxBatch, hello.MaxSeq
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatch
	}
	if maxSeq <= 0 {
		maxSeq = defaultMaxSeq
	}
	// Generous room for the ids, counts, names and dims around the elements.
	const room = labeler.MaxFrameBytes - 4096
	// A request row is seq int32 values for each input.
	inputs = max(inputs, 1)
	maxSeq = min(maxSeq, room/(4*inputs))
	perRow := 4 * inputs * maxSeq
	// A result row is every output's elements past the batch dimension; an
	// unknown dimension is assumed to be as long as the sequence.
	resultRow := 0
	for _, out := range outputs {
		n := 4
		for i, dim := range out.Shape {
			if i == 0 {
				continue
			}
			if d, ok := dim.(int); ok && d > 0 {
				n *= d
			} else {
				n *= maxSeq
			}
		}
		resultRow += n
	}
	perRow = max(perRow, resultRow)
	maxBatch = max(1, min(maxBatch, room/perRow))
	return maxBatch, maxSeq
}

// enqueue decodes and checks a run request, answering a bad one at once, and
// queues a good one for the runner. Replies may therefore leave out of order;
// Pharos matches them by id.
func (w *worker) enqueue(payload []byte) {
	req, err := labeler.DecodeRun(payload)
	if err != nil {
		w.out.error(req.ID, labeler.ErrCodeBadRequest, "%v", err)
		return
	}
	if err := labeler.ValidateRun(req, w.hello.RunInputs(), w.hello.VocabSize, w.ready.Limits.MaxBatch, w.ready.Limits.MaxSeq); err != nil {
		w.out.error(req.ID, labeler.ErrCodeBadRequest, "%v", err)
		return
	}
	outputs, err := w.outputs(req.Outputs)
	if err != nil {
		w.out.error(req.ID, labeler.ErrCodeBadRequest, "%v", err)
		return
	}
	// ValidateRun found every declared input once, and checkModel that the
	// declared inputs are the model's.
	j := job{id: req.ID, outputs: outputs}
	for _, in := range w.info.Inputs {
		i := slices.IndexFunc(req.Inputs, func(t labeler.Tensor) bool { return t.Name == in.Name })
		j.inputs = append(j.inputs, req.Inputs[i])
	}
	j.batch, j.seq = j.inputs[0].Dims[0], j.inputs[0].Dims[1]
	select {
	case w.queue <- j:
	case <-w.out.broken:
	}
}

// outputs resolves a request's output names: every model output when it names
// none, or the named ones in the order asked.
func (w *worker) outputs(names []string) ([]string, error) {
	if len(names) == 0 {
		all := make([]string, len(w.info.Outputs))
		for i, out := range w.info.Outputs {
			all[i] = out.Name
		}
		return all, nil
	}
	for i, name := range names {
		known := slices.ContainsFunc(w.info.Outputs, func(t labeler.TensorInfo) bool { return t.Name == name })
		if !known {
			return nil, fmt.Errorf("unknown output %q", name)
		}
		if slices.Contains(names[:i], name) {
			return nil, fmt.Errorf("output %q requested twice", name)
		}
	}
	return names, nil
}

// runner runs queued jobs one at a time until the queue is closed. Running
// one batch at a time leaves the intra-op threads to that batch.
func (w *worker) runner() {
	defer close(w.done)
	for j := range w.queue {
		w.run(j)
	}
}

func (w *worker) run(j job) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(w.log, "pharos-labeler-ort: panic in run %d: %v\n%s", j.id, p, debug.Stack())
			w.out.error(j.id, labeler.ErrCodeInternal, "panic: %v", p)
		}
	}()
	inputs := make([]modelInput, len(j.inputs))
	for i, t := range j.inputs {
		inputs[i].Name = t.Name
		if w.info.Inputs[i].DType == "int32" {
			inputs[i].Int32 = t.Int32
		} else {
			inputs[i].Int64 = widen(t.Int32)
		}
	}
	tensors, err := w.sess.Run(inputs, j.batch, j.seq, j.outputs)
	if err != nil {
		w.out.error(j.id, labeler.ErrCodeRunFailed, "%v", err)
		return
	}
	if len(tensors) != len(j.outputs) {
		w.out.error(j.id, labeler.ErrCodeInternal, "the runtime returned %d outputs for %d requested", len(tensors), len(j.outputs))
		return
	}
	payload, err := labeler.EncodeResult(&labeler.RunResult{ID: j.id, Outputs: tensors})
	if err == nil && len(payload)+1 > labeler.MaxFrameBytes {
		err = fmt.Errorf("result of %d bytes exceeds the frame limit", len(payload))
	}
	if err != nil {
		w.out.error(j.id, labeler.ErrCodeInternal, "%v", err)
		return
	}
	w.out.frame(labeler.FrameResult, payload)
}

// widen converts the wire's int32 inputs to the model's int64.
func widen(values []int32) []int64 {
	out := make([]int64, len(values))
	for i, v := range values {
		out[i] = int64(v)
	}
	return out
}
