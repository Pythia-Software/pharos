//go:build darwin || linux

package archive

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceProcessNativeReturnsOnlyOnFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := replaceProcessNative(missing, []string{missing}, os.Environ()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacing with a missing executable: %v", err)
	}
}
