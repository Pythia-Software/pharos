//go:build !darwin || !arm64 || !cgo

package main

import (
	"errors"
	"github.com/gbdubs/pharos/internal/labeler"
)

func openMLX(library, path string, artifact labeler.MLXArtifact) (session, error) {
	return nil, errors.New("MLX requires Apple silicon, macOS 26.2 or later, and a worker built with cgo")
}

func probeMLX(library string) (*bool, error) {
	available := false
	return &available, errors.New("MLX requires Apple silicon, macOS 26.2 or later, and a worker built with cgo")
}
