package filesync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidArguments(t *testing.T) {
	if ErrUnsupported == nil {
		t.Fatal("ErrUnsupported must be a non-nil sentinel")
	}
	if err := Flush(nil); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("nil file: %v", err)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Flush(file); err == nil || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed file: %v", err)
	}
}
