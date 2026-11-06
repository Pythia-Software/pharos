package labeler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MLXArtifact describes a companion to the canonical FP32 ONNX model. The
// ONNX SHA remains the inference identity, including when auto uses CPU.
type MLXArtifact struct {
	Format       string `json:"format"`
	Architecture string `json:"architecture"`
	Task         string `json:"task"`
	Weights      string `json:"weights"`
	SourceSHA256 string `json:"source_onnx_sha256"`
}

// VerifyMLX verifies the companion and its source identity. A nil artifact
// means this variant has no MLX companion. A declared but broken companion
// is an error, never a reason to silently run a different model.
func VerifyMLX(modelPath, sourceSHA string) (*MLXArtifact, string, error) {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(modelPath), "manifest.json"))
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return nil, "", err
	}
	a := man.MLX
	if a == nil || filepath.Base(modelPath) != man.Files["onnx_fp32"] {
		return nil, "", nil
	}
	if a.Format != "pharos-mlx-v1" || a.Architecture != "ettin-68m-v1" || (a.Task != "kestrel" && a.Task != "jayhawk") || a.SourceSHA256 != sourceSHA {
		return nil, "", fmt.Errorf("%w: incompatible MLX companion or source ONNX identity", ErrModelUnavailable)
	}
	if a.Weights == "" || a.Weights == "." || a.Weights == ".." || strings.ContainsAny(a.Weights, `/\`) {
		return nil, "", fmt.Errorf("%w: invalid MLX weights filename", ErrModelUnavailable)
	}
	contract := InputContractIDsAttentionPool
	if a.Task == "jayhawk" {
		contract = InputContractIDsAttentionPoolSegments
	}
	if man.InputContract != contract {
		return nil, "", fmt.Errorf("%w: MLX task does not match input contract", ErrModelUnavailable)
	}
	path := filepath.Join(filepath.Dir(modelPath), a.Weights)
	if _, err := verifyFile(path, man.SHA256[a.Weights]); err != nil {
		return nil, "", err
	}
	return a, path, nil
}

// MayUseGPU reserves GPU capacity conservatively before the worker starts.
// The worker's Ready reply reports whether auto actually selected MLX.
func (m *Model) MayUseGPU(backend string) bool {
	return m.MLXPath != "" && backend != "cpu" && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}
