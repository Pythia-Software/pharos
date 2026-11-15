package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gbdubs/pharos/internal/labeler"
)

var errMLXModel = errors.New("invalid MLX model")

// nil availability means an older engine cannot report its capabilities.
type labelerProbe struct {
	Available *bool  `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func resolveMLXRuntime(value string) string {
	if value != "" {
		return value
	}
	if value = os.Getenv("PHAROS_MLX_LIBRARY"); value != "" {
		return value
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "mlx", "libpharos_mlx.dylib")
	}
	return "libpharos_mlx.dylib"
}

// Auto falls back only at startup, before Ready, using the same ONNX file.
// Artifact corruption is always fatal. A running GPU session never silently
// changes device after its Ready metadata has been sent.
func backendOpener(backend, ort, mlx string, log io.Writer) opener {
	return func(cfg sessionConfig) (session, error) {
		if backend == "cpu" {
			return openORT(ort, cfg)
		}
		sum, err := fileSHA256(cfg.Model)
		if err != nil {
			return nil, err
		}
		artifact, path, err := labeler.VerifyMLX(cfg.Model, sum)
		if err != nil {
			return nil, err
		}
		if artifact != nil {
			sess, err := openMLX(mlx, path, *artifact)
			if err == nil {
				return sess, nil
			}
			if backend == "mlx" || errors.Is(err, errMLXModel) {
				return nil, err
			}
			fmt.Fprintf(log, "MLX unavailable; using FP32 ONNX CPU: %v\n", err)
		} else if backend == "mlx" {
			return nil, fmt.Errorf("MLX needs a verified FP32 companion in manifest.json; export it with tools/export_labeler_mlx.py")
		}
		return openORT(ort, cfg)
	}
}
