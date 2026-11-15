//go:build !darwin && !linux

package archive

import (
	"errors"
	"os"
	"testing"
)

func TestReplaceProcessNativeIsUnsupported(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceProcessNative(executable, os.Args, os.Environ()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("replacing the process: %v", err)
	}
	// restartMCP reports it through the restart-failure path.
	if err := restartMCP(executable, []byte("pending")); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("restart: %v", err)
	}
}
