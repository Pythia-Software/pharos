//go:build !darwin && !linux

package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedAdapter(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, mode := range []Mode{Shared, Exclusive} {
		for _, wait := range []WaitMode{Try, Wait} {
			if err := Lock(file, mode, wait); !errors.Is(err, ErrUnsupported) || errors.Is(err, ErrContended) {
				t.Fatalf("unsupported lock (%v, %v): %v", mode, wait, err)
			}
		}
	}
	if err := Unlock(file); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported unlock: %v", err)
	}
	if _, err := file.WriteString("still open"); err != nil {
		t.Fatalf("adapter closed its borrowed file: %v", err)
	}
}
