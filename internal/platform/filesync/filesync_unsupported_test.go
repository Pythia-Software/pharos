//go:build (!darwin && !linux) || filesync_unsupported

package filesync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// go test -tags filesync_unsupported runs this on darwin and linux too.
func TestUnsupportedAdapter(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := Flush(file); !errors.Is(err, ErrUnsupported) || err != ErrUnsupported {
		t.Fatalf("unsupported flush: %v", err)
	}
	if _, err := file.WriteString("still open"); err != nil {
		t.Fatalf("adapter closed its borrowed file: %v", err)
	}
	if err := Flush(nil); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("unsupported nil flush: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Flush(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("unsupported closed flush: %v", err)
	}
}
