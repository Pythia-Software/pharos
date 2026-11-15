//go:build !darwin && !linux

package archive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInputOpensAreUnsupported(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "data"), "setup")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if file, err := openInputNonblockingNative(filepath.Join(dir, "data")); file != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("path open: %v %v", file, err)
	}
	if file, err := openInputNonblockingInNative(root, "data"); file != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("rooted open: %v %v", file, err)
	}
}
