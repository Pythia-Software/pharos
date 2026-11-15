package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidArguments(t *testing.T) {
	if ErrContended == nil || ErrUnsupported == nil || errors.Is(ErrContended, ErrUnsupported) {
		t.Fatal("lock sentinel errors must be non-nil and distinct")
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, tc := range []struct {
		name string
		file *os.File
		mode Mode
		wait WaitMode
	}{
		{"nil", nil, Shared, Try},
		{"mode", file, Mode(255), Try},
		{"wait", file, Exclusive, WaitMode(255)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Lock(tc.file, tc.mode, tc.wait); !errors.Is(err, os.ErrInvalid) || errors.Is(err, ErrContended) {
				t.Fatalf("invalid lock arguments: %v", err)
			}
		})
	}
	if err := Unlock(nil); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("nil unlock: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{Shared, Exclusive} {
		for _, wait := range []WaitMode{Try, Wait} {
			if err := Lock(file, mode, wait); !errors.Is(err, os.ErrClosed) || errors.Is(err, ErrContended) {
				t.Fatalf("closed lock (%v, %v): %v", mode, wait, err)
			}
		}
	}
	if err := Unlock(file); !errors.Is(err, os.ErrClosed) || errors.Is(err, ErrContended) {
		t.Fatalf("closed unlock: %v", err)
	}
}
