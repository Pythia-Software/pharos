//go:build cgo

package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/labeler"
)

// These tests run the real ONNX Runtime on the kestrel-68m export. They need
// PHAROS_LABELER_MODEL_DIR (the export directory: model.int8.onnx and
// manifest.json) and PHAROS_ORT_LIBRARY (libonnxruntime, for example from
// macos/fetch-onnxruntime.sh).

func ortEnv(t *testing.T) (dir, library string) {
	dir, library = os.Getenv("PHAROS_LABELER_MODEL_DIR"), os.Getenv("PHAROS_ORT_LIBRARY")
	if dir == "" || library == "" {
		t.Skip("set PHAROS_LABELER_MODEL_DIR to the kestrel-68m export and PHAROS_ORT_LIBRARY to libonnxruntime " +
			"(macos/fetch-onnxruntime.sh prints its directory; the library is lib/libonnxruntime.dylib) to run against ONNX Runtime")
	}
	return dir, library
}

type parityCase struct {
	Case     string  `json:"case"`
	InputIDs []int32 `json:"input_ids"`
	Int8     struct {
		Logits [][]float64 `json:"logits"`
		Probs  []float64   `json:"probs"`
	} `json:"int8"`
}

func loadParity(t *testing.T) []parityCase {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "labeler", "kestrel-68m-parity.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []parityCase `json:"cases"`
	}
	if err := json.NewDecoder(z).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("the parity fixture has no cases")
	}
	return fixture.Cases
}

// kestrelHello is the hello Pharos sends for the int8 model, with its
// checksum from the export's manifest.
func kestrelHello(t *testing.T, dir string) labeler.Hello {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SHA256 map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return labeler.Hello{Protocol: []int{1}, Model: filepath.Join(dir, "model.int8.onnx"), ModelSHA256: manifest.SHA256["model.int8.onnx"],
		Threads: 4, VocabSize: 50368, MaxBatch: 32, MaxSeq: 512}
}

func TestORTParity(t *testing.T) {
	dir, library := ortEnv(t)
	cases := loadParity(t)
	h := newHarness(t).start(func(cfg sessionConfig) (session, error) { return openORT(library, cfg) })
	hello := kestrelHello(t, dir)
	h.sendJSON(labeler.FrameHello, hello)
	kind, payload := h.recv()
	var ready labeler.Ready
	if kind != labeler.FrameReady || json.Unmarshal(payload, &ready) != nil {
		t.Fatalf("frame %q %s", kind, payload)
	}
	t.Logf("ready: %s", payload)
	if ready.Model.SHA256 != hello.ModelSHA256 || len(ready.Model.Inputs) != 2 || len(ready.Model.Outputs) != 2 {
		t.Fatalf("ready %+v", ready)
	}
	for _, in := range ready.Model.Inputs {
		if in.DType != "int64" || len(in.Shape) != 2 {
			t.Fatalf("input %+v", in)
		}
	}
	shapes := map[string][]any{"logits": {5.0, 54.0}, "probs": {54.0}}
	for _, out := range ready.Model.Outputs {
		want, ok := shapes[out.Name]
		if !ok || out.DType != "float32" || len(out.Shape) != len(want)+1 {
			t.Fatalf("output %+v", out)
		}
		for i, d := range want {
			if out.Shape[i+1] != d {
				t.Fatalf("output %+v", out)
			}
		}
	}

	// The fixture's outputs are the Python reference's, which pads each batch
	// to a multiple of 32 (kframe.pad). The int8 model's outputs depend on the
	// padded length when it is not a multiple of 4 (in Python as well), so
	// the strict checks pad the same way, and the unpadded run is reported.
	var id uint64
	check := func(name string, batch []parityCase, mult int, strict bool, outputs ...string) (logitDelta, probDelta float64) {
		t.Helper()
		rows := make([][]int32, len(batch))
		for i, c := range batch {
			rows[i] = c.InputIDs
		}
		id++
		h.send(labeler.FrameRun, paddedRunPayload(t, id, rows, mult, outputs...))
		res := h.expectResult()
		if res.ID != id {
			t.Fatalf("%s: result %d, want %d", name, res.ID, id)
		}
		logits, probs := res.Tensor("logits"), res.Tensor("probs")
		if probs == nil || len(probs.Float32) != 54*len(batch) || (logits == nil) != (len(outputs) == 1) {
			t.Fatalf("%s: outputs %+v", name, res.Outputs)
		}
		for r, c := range batch {
			for k, want := range c.Int8.Probs {
				probDelta = max(probDelta, math.Abs(float64(probs.Float32[54*r+k])-want))
			}
			if logits == nil {
				continue
			}
			for head, row := range c.Int8.Logits {
				for k, want := range row {
					logitDelta = max(logitDelta, math.Abs(float64(logits.Float32[270*r+54*head+k])-want))
				}
			}
		}
		if strict && (logitDelta > 1e-3 || probDelta > 1e-4) {
			t.Errorf("%s: max |Δ| logits %.3g, probs %.3g over the tolerance 1e-3, 1e-4", name, logitDelta, probDelta)
		}
		return logitDelta, probDelta
	}
	pass := func(what string, size, mult int, strict bool, cases []parityCase) {
		t.Helper()
		var worst [2]float64
		start := time.Now()
		for i := 0; i < len(cases); i += size {
			l, p := check(what+" at "+cases[i].Case, cases[i:min(i+size, len(cases))], mult, strict)
			worst = [2]float64{max(worst[0], l), max(worst[1], p)}
		}
		t.Logf("%s: %d cases in %v, max |Δ| logits %.3g, probs %.3g", what, len(cases), time.Since(start).Round(time.Millisecond), worst[0], worst[1])
	}
	pass("one at a time, padded to 32", 1, 32, true, cases)
	byLength := slices.Clone(cases)
	slices.SortStableFunc(byLength, func(a, b parityCase) int { return len(a.InputIDs) - len(b.InputIDs) })
	pass("batches of 16 by length, padded to 32", 16, 32, true, byLength)
	pass("batches of 16 in fixture order, padded to 32", 16, 32, true, cases)
	pass("one at a time, unpadded (not checked)", 1, 1, false, cases)
	check("probs only", cases[:4], 32, true, "probs")
	id++
	h.send(labeler.FrameRun, runPayload(t, id, [][]int32{cases[0].InputIDs}, "hidden_states"))
	h.expectError(id, labeler.ErrCodeBadRequest, false)

	h.send(labeler.FrameQuit, nil)
	if kind, _ := h.recv(); kind != labeler.FrameGoodbye {
		t.Fatalf("frame %q, want goodbye", kind)
	}
	h.expectExit(exitOK)
}

// TestORTWorkerProcess runs the built binary: start-up, a ping during a run,
// a refused checksum, and SIGTERM during a run.
func TestORTWorkerProcess(t *testing.T) {
	dir, library := ortEnv(t)
	cases := loadParity(t)
	goTool, err := exec.LookPath("go")
	if err != nil {
		goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	binary := filepath.Join(t.TempDir(), "pharos-labeler-ort")
	build := exec.Command(goTool, "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	start := func(t *testing.T, library string) (*harness, *exec.Cmd) {
		cmd := exec.Command(binary, "--runtime", library)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = &testLog{t}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		h := &harness{t: t, status: make(chan int, 1), out: labeler.NewFrameReader(stdout)}
		pipeIn, pipeWriter := io.Pipe()
		h.in = pipeWriter
		go func() { io.Copy(stdin, pipeIn); stdin.Close() }()
		t.Cleanup(func() {
			pipeWriter.Close()
			cmd.Process.Kill()
		})
		return h, cmd
	}
	waitExit := func(t *testing.T, cmd *exec.Cmd) *os.ProcessState {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
			return cmd.ProcessState
		case <-time.After(wait):
			t.Fatal("the worker did not exit")
			return nil
		}
	}
	// The longest cases make a run that takes a while.
	var long [][]int32
	for _, c := range cases {
		if len(c.InputIDs) == 512 && len(long) < 32 {
			long = append(long, c.InputIDs)
		}
	}

	t.Run("serves", func(t *testing.T) {
		h, cmd := start(t, library)
		began := time.Now()
		h.sendJSON(labeler.FrameHello, kestrelHello(t, dir))
		if kind, payload := h.recv(); kind != labeler.FrameReady {
			t.Fatalf("frame %q %s", kind, payload)
		}
		t.Logf("ready after %v", time.Since(began).Round(time.Millisecond))
		h.send(labeler.FrameRun, runPayload(t, 1, long))
		sent := time.Now()
		h.send(labeler.FramePing, labeler.EncodeNonce(77))
		var pong, result time.Duration
		for pong == 0 || result == 0 {
			switch kind, payload := h.recv(); kind {
			case labeler.FramePong:
				pong = time.Since(sent)
			case labeler.FrameResult:
				result = time.Since(sent)
			default:
				t.Fatalf("frame %q %s", kind, payload)
			}
		}
		t.Logf("batch of %d × 512: pong after %v, result after %v", len(long), pong.Round(time.Microsecond), result.Round(time.Millisecond))
		h.send(labeler.FrameQuit, nil)
		if kind, _ := h.recv(); kind != labeler.FrameGoodbye {
			t.Fatalf("frame %q", kind)
		}
		if state := waitExit(t, cmd); state.ExitCode() != 0 {
			t.Fatalf("exit %v", state)
		}
	})
	t.Run("refuses a wrong checksum", func(t *testing.T) {
		h, cmd := start(t, library)
		hello := kestrelHello(t, dir)
		hello.ModelSHA256 = "d95ab6139845f135f534b4d76ae215304d1481226e7ab08b276a72daf187850e"
		h.sendJSON(labeler.FrameHello, hello)
		h.expectError(0, labeler.ErrCodeModel, true)
		if state := waitExit(t, cmd); state.ExitCode() != exitFatal {
			t.Fatalf("exit %v", state)
		}
	})
	t.Run("refuses an input the model lacks", func(t *testing.T) {
		h, cmd := start(t, library)
		hello := kestrelHello(t, dir)
		hello.Inputs = append(slices.Clone(labeler.DefaultInputs), labeler.InputSpec{Name: "pooling_mask", Role: labeler.RolePool})
		h.sendJSON(labeler.FrameHello, hello)
		e := h.expectError(0, labeler.ErrCodeModel, true)
		t.Logf("%s", e.Message)
		if state := waitExit(t, cmd); state.ExitCode() != exitFatal {
			t.Fatalf("exit %v", state)
		}
	})
	t.Run("stops on SIGTERM during a run", func(t *testing.T) {
		h, cmd := start(t, library)
		h.sendJSON(labeler.FrameHello, kestrelHello(t, dir))
		h.recv()
		h.send(labeler.FrameRun, runPayload(t, 1, long))
		h.send(labeler.FramePing, labeler.EncodeNonce(1))
		h.expectPong(1)
		sent := time.Now()
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		state := waitExit(t, cmd)
		status, ok := state.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("exit %v, want SIGTERM", state)
		}
		t.Logf("exited %v after SIGTERM", time.Since(sent).Round(time.Millisecond))
	})
	t.Run("missing runtime", func(t *testing.T) {
		h, cmd := start(t, filepath.Join(t.TempDir(), "libonnxruntime.dylib"))
		h.sendJSON(labeler.FrameHello, kestrelHello(t, dir))
		e := h.expectError(0, labeler.ErrCodeModel, true)
		t.Logf("missing runtime: %s", e.Message)
		if state := waitExit(t, cmd); state.ExitCode() != exitFatal {
			t.Fatalf("exit %v", state)
		}
	})
}
