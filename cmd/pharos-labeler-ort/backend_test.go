package main

import (
	"bytes"
	"encoding/json"
	"github.com/gbdubs/pharos/internal/labeler"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendSelectionAndFallback(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.fp32.onnx")
	weights := filepath.Join(dir, "model.mlx.safetensors")
	for _, path := range []string{model, weights} {
		if err := os.WriteFile(path, []byte("stand-in"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := fileSHA256(model)
	man := labeler.Manifest{InputContract: labeler.InputContractIDsAttentionPool, Files: map[string]string{"onnx_fp32": filepath.Base(model)}, SHA256: map[string]string{filepath.Base(weights): sum}, MLX: &labeler.MLXArtifact{Format: "pharos-mlx-v1", Architecture: "ettin-68m-v1", Task: "kestrel", Weights: filepath.Base(weights), SourceSHA256: sum}}
	save := func() {
		t.Helper()
		raw, _ := json.Marshal(man)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	missingORT, missingMLX := filepath.Join(dir, "missing-ort"), filepath.Join(dir, "missing-mlx")
	var log bytes.Buffer
	_, err := backendOpener("mlx", missingORT, missingMLX, &log)(sessionConfig{Model: model})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "mlx") || log.Len() != 0 {
		t.Fatalf("explicit MLX fell back: %v %s", err, log.String())
	}
	_, err = backendOpener("auto", missingORT, missingMLX, &log)(sessionConfig{Model: model})
	if err == nil || !strings.Contains(log.String(), "using FP32 ONNX CPU") {
		t.Fatalf("auto did not attempt same-model CPU fallback: %v %s", err, log.String())
	}
	log.Reset()
	man.MLX.SourceSHA256 = "wrong"
	save()
	_, err = backendOpener("auto", missingORT, missingMLX, &log)(sessionConfig{Model: model})
	if err == nil || !strings.Contains(err.Error(), "identity") || log.Len() != 0 {
		t.Fatalf("corrupt artifact silently fell back: %v %s", err, log.String())
	}
}

func TestResolveMLXRuntime(t *testing.T) {
	t.Setenv("PHAROS_MLX_LIBRARY", "/environment/engine")
	if got := resolveMLXRuntime("/flag/engine"); got != "/flag/engine" {
		t.Fatal(got)
	}
	if got := resolveMLXRuntime(""); got != "/environment/engine" {
		t.Fatal(got)
	}
}
