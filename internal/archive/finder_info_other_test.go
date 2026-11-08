//go:build !darwin && !linux

package archive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setFinderInfo(string, []byte) error { return errFinderInfoUnsupported }

func TestFinderInfoNativeIsUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := finderInfoNative(path); info != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Finder info %v, %v", info, err)
	}
}
