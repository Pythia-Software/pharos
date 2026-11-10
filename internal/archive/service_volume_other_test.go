//go:build !darwin && !linux

package archive

import (
	"errors"
	"testing"
)

func TestReadDirectoryVolumeIdentityUnsupported(t *testing.T) {
	if _, err := readDirectoryVolumeIdentity(t.TempDir()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("identity error = %v; want unsupported platform", err)
	}
}
