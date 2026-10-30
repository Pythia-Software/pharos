// Package labelertest is a stand-in labeler worker for tests: a test binary
// that finds FakeWorkerEnv set runs FakeWorker instead of its tests, so tests
// exercise the real process, protocol and crash handling without a model.
package labelertest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gbdubs/pharos/internal/labeler"
)

// FakeWorkerEnv names the fake's mode; FakeCountEnv the file a crashing fake
// counts its results in.
const (
	FakeWorkerEnv = "PHAROS_LABELER_FAKE_WORKER"
	FakeCountEnv  = "PHAROS_LABELER_FAKE_COUNT"
)

// FakeProb is the fake model: (sum of the row's real token ids + j) mod 100
// over 100.
func FakeProb(ids []int32, j int) float32 {
	sum := 0
	for _, id := range ids {
		sum += int(id)
	}
	return float32((sum+j)%100) / 100
}

func fakeLabels(model string) int {
	var manifest struct {
		Labels struct {
			Order []string `json:"order"`
		} `json:"labels"`
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(model), "manifest.json")); err == nil && json.Unmarshal(data, &manifest) == nil && len(manifest.Labels.Order) > 0 {
		return len(manifest.Labels.Order)
	}
	return 4
}

// FakeWorker speaks pharos-labeler/1 with a stand-in model: probs[j] of a row
// is FakeProb of its real tokens (those its pool input marks, when the hello
// declares one), over the labels of the manifest.json beside
// the hello's model file (4 without one). Modes:
// "ok"; "crash-after:N" exits after N results (counted in the file named by
// FakeCountEnv, so a restarted worker carries on counting);
// "crash-on-id:K" exits on any batch holding token K; "refuse" fails the
// hello; "slow" sleeps 200 ms per run.
func FakeWorker(mode string) int {
	reader := labeler.NewFrameReader(os.Stdin)
	var writeMu sync.Mutex
	write := func(kind byte, payload []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = labeler.WriteFrame(os.Stdout, kind, payload)
	}
	writeJSON := func(kind byte, value any) {
		payload, _ := json.Marshal(value)
		write(kind, payload)
	}
	kind, payload, err := labeler.ReadFrame(reader, 0)
	if err != nil || kind != labeler.FrameHello {
		return 2
	}
	var hello labeler.Hello
	_ = json.Unmarshal(payload, &hello)
	if mode == "refuse" {
		fmt.Fprintln(os.Stderr, "cannot open the model file")
		writeJSON(labeler.FrameError, labeler.ErrorReply{Code: labeler.ErrCodeModel, Message: "no model at " + hello.Model, Fatal: true})
		return 3
	}
	ready := labeler.Ready{Protocol: labeler.ProtocolVersion, Worker: labeler.WorkerInfo{Name: "fake", Version: "1", Runtime: "none"}}
	ready.Limits.MaxBatch, ready.Limits.MaxSeq = 64, 512
	writeJSON(labeler.FrameReady, ready)
	crashAfter, crashOn := -1, int32(-1)
	if value, ok := strings.CutPrefix(mode, "crash-after:"); ok {
		crashAfter, _ = strconv.Atoi(value)
	}
	if value, ok := strings.CutPrefix(mode, "crash-on-id:"); ok {
		id, _ := strconv.Atoi(value)
		crashOn = int32(id)
	}
	counter := os.Getenv(FakeCountEnv)
	labels := fakeLabels(hello.Model)
	for {
		kind, payload, err := labeler.ReadFrame(reader, 0)
		if err != nil {
			return 0
		}
		switch kind {
		case labeler.FrameQuit:
			write(labeler.FrameGoodbye, nil)
			return 0
		case labeler.FramePing:
			write(labeler.FramePong, payload)
		case labeler.FrameRun:
			req, err := labeler.DecodeRun(payload)
			if err == nil {
				err = labeler.ValidateRun(req, hello.RunInputs(), hello.VocabSize, 64, 512)
			}
			if err != nil {
				writeJSON(labeler.FrameError, labeler.ErrorReply{ID: req.ID, Code: labeler.ErrCodeBadRequest, Message: err.Error()})
				continue
			}
			if mode == "slow" {
				time.Sleep(200 * time.Millisecond)
			}
			// The fake pools what a pool input marks, else every real token.
			var ids, mask *labeler.Tensor
			for _, input := range hello.RunInputs() {
				for i := range req.Inputs {
					if req.Inputs[i].Name != input.Name {
						continue
					}
					switch input.Role {
					case labeler.RoleIDs:
						ids = &req.Inputs[i]
					case labeler.RoleAttention:
						if mask == nil {
							mask = &req.Inputs[i]
						}
					case labeler.RolePool:
						mask = &req.Inputs[i]
					}
				}
			}
			batch, seq := ids.Dims[0], ids.Dims[1]
			probs := make([]float32, batch*labels)
			for row := range batch {
				sum := 0
				for col := range seq {
					if mask.Int32[row*seq+col] == 1 {
						if ids.Int32[row*seq+col] == crashOn {
							os.Exit(4)
						}
						sum += int(ids.Int32[row*seq+col])
					}
				}
				for j := range labels {
					probs[row*labels+j] = float32((sum+j)%100) / 100
				}
			}
			if crashAfter >= 0 {
				done := 0
				if data, err := os.ReadFile(counter); err == nil {
					done, _ = strconv.Atoi(string(data))
				}
				if done >= crashAfter {
					_ = os.WriteFile(counter, []byte("0"), 0o600)
					os.Exit(5)
				}
				_ = os.WriteFile(counter, []byte(strconv.Itoa(done+1)), 0o600)
			}
			out, _ := labeler.EncodeResult(&labeler.RunResult{ID: req.ID, Outputs: []labeler.Tensor{{Name: "probs", DType: labeler.DTypeFloat32, Dims: []int{batch, labels}, Float32: probs}}})
			write(labeler.FrameResult, out)
		default:
			writeJSON(labeler.FrameError, labeler.ErrorReply{Code: labeler.ErrCodeUnsupported, Message: fmt.Sprintf("frame %q", kind)})
		}
	}
}
