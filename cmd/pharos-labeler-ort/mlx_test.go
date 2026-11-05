//go:build darwin && arm64 && cgo

package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gbdubs/pharos/internal/labeler"
)

// Optional native parity test. Point PHAROS_MLX_TEST_MODELS at a colon-separated
// list of converted model directories and set PHAROS_MLX_LIBRARY and
// PHAROS_ORT_LIBRARY. Normal tests need neither model downloads nor a GPU.
func TestMLXNativeParity(t *testing.T) {
	dirs, engine, ort := os.Getenv("PHAROS_MLX_TEST_MODELS"), os.Getenv("PHAROS_MLX_LIBRARY"), os.Getenv("PHAROS_ORT_LIBRARY")
	if dirs == "" || engine == "" || ort == "" {
		t.Skip("set PHAROS_MLX_TEST_MODELS, PHAROS_MLX_LIBRARY and PHAROS_ORT_LIBRARY for native GPU parity")
	}
	if available, err := probeMLX(engine); err != nil || available == nil || !*available {
		t.Fatalf("native GPU availability: %v %v", available, err)
	}
	for _, dir := range filepath.SplitList(dirs) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var man labeler.Manifest
			if err = json.Unmarshal(raw, &man); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(dir, man.Files["onnx_fp32"])
			sum, err := fileSHA256(source)
			if err != nil {
				t.Fatal(err)
			}
			artifact, path, err := labeler.VerifyMLX(source, sum)
			if err != nil || artifact == nil {
				t.Fatalf("MLX companion: %v", err)
			}
			bad := *artifact
			bad.SourceSHA256 = "incorrect source"
			if sess, err := openMLX(engine, path, bad); !errors.Is(err, errMLXModel) {
				if sess != nil {
					sess.Close()
				}
				t.Fatalf("native metadata mismatch was not refused: %v", err)
			}
			gpu, err := openMLX(engine, path, *artifact)
			if err != nil {
				t.Fatal(err)
			}
			defer gpu.Close()
			cpu, err := openORT(ort, sessionConfig{Model: source, Threads: 4})
			if err != nil {
				t.Fatal(err)
			}
			defer cpu.Close()
			info := gpu.Info()
			if info.MaxBatch != 256 || info.Device != "gpu" || !slices.Contains(info.Providers, "Metal") {
				t.Fatalf("wrong device: %+v", info)
			}
			names := []string{}
			for _, out := range info.Outputs {
				names = append(names, out.Name)
			}
			slices.Reverse(names)
			maxSeq := 512
			if artifact.Task == "jayhawk" {
				maxSeq = 768
			}
			for _, seq := range []int{8, maxSeq} {
				// Mixed lengths exercise padded queries, target-only pooling, and empty
				// sentence slots, including the last numbered sentence.
				batch := 3
				input := make([]modelInput, len(info.Inputs))
				for k, spec := range info.Inputs {
					values := make([]int32, batch*seq)
					for row := range batch {
						length := seq - row*2
						for j := range seq {
							i := row*seq + j
							switch spec.Name {
							case "input_ids":
								values[i] = pad
								if j < length {
									values[i] = int32(100 + j%30)
								}
							case "attention_mask":
								if j < length {
									values[i] = 1
								}
							case "pooling_mask":
								if j >= 1 && j < length-1 {
									values[i] = 1
								}
							case "segment_ids":
								if j >= 1 && j < length/2 {
									values[i] = 1
								} else if j >= length/2 && j < length-1 {
									values[i] = 16
								}
							}
						}
					}
					input[k] = modelInput{Name: spec.Name, Int32: values}
				}
				got, err := gpu.Run(input, batch, seq, names)
				if err != nil {
					t.Fatal(err)
				}
				cpuInput := make([]modelInput, len(input))
				for i, v := range input {
					cpuInput[i] = modelInput{Name: v.Name, Int64: widen(v.Int32)}
				}
				want, err := cpu.Run(cpuInput, batch, seq, names)
				if err != nil {
					t.Fatal(err)
				}
				for k, out := range got {
					if out.Name != names[k] || out.Name != want[k].Name || !slices.Equal(out.Dims, want[k].Dims) || len(out.Float32) != len(want[k].Float32) {
						t.Fatalf("output contract: %+v", out)
					}
					tolerance := 1e-4
					if out.Name == "logits" {
						tolerance = 1e-3
					}
					delta := 0.0
					for i, v := range out.Float32 {
						if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
							t.Fatalf("non-finite %s[%d]", out.Name, i)
						}
						delta = max(delta, math.Abs(float64(v-want[k].Float32[i])))
					}
					if delta > tolerance {
						t.Errorf("%s at sequence %d: max difference %g exceeds %g", out.Name, seq, delta, tolerance)
					}
					t.Logf("%s seq=%d max difference %.3g", out.Name, seq, delta)
				}
			}
		})
	}
}
