//go:build (darwin || linux) && !filesync_unsupported

package filesync

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Each descriptor mode the archive flushes through: new writes, rewrites,
// appends, and the read-only handle a snapshot is reopened with.
func TestFlushRealFiles(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []struct {
		name string
		flag int
	}{
		{"write-only", os.O_WRONLY | os.O_CREATE | os.O_TRUNC},
		{"read-write", os.O_RDWR | os.O_CREATE},
		{"append", os.O_WRONLY | os.O_CREATE | os.O_APPEND},
		{"read-only", os.O_RDONLY},
	} {
		t.Run(mode.name, func(t *testing.T) {
			path := filepath.Join(dir, mode.name)
			if err := os.WriteFile(path, []byte("tiny"), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, mode.flag, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if mode.flag != os.O_RDONLY {
				if _, err := file.WriteString("+more"); err != nil {
					t.Fatal(err)
				}
			}
			if err := Flush(file); err != nil {
				t.Fatalf("flush: %v", err)
			}
			// The caller still owns an open file: it can use it, flush it
			// again, and close it itself.
			if _, err := file.Stat(); err != nil {
				t.Fatalf("file unusable after flush: %v", err)
			}
			if mode.flag != os.O_RDONLY {
				if _, err := file.WriteString("!"); err != nil {
					t.Fatalf("file unwritable after flush: %v", err)
				}
			}
			if err := Flush(file); err != nil {
				t.Fatalf("second flush: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Fatalf("caller's close: %v", err)
			}
		})
	}
}

func TestFlushBadDescriptor(t *testing.T) {
	// Far above any descriptor this process has open.
	file := os.NewFile(1<<30, "bad")
	defer file.Close()
	if err := Flush(file); err != syscall.EBADF {
		t.Fatalf("bad descriptor: %#v, want the EBADF errno itself", err)
	}
}

// Flush returns the system's error unchanged and calls fsync once, even for
// EINTR.
func TestFlushReturnsTheSystemErrorWithoutRetrying(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := fsync
	t.Cleanup(func() { fsync = previous })
	for _, want := range []error{syscall.EINTR, syscall.EIO, syscall.EACCES, syscall.ENOTSUP} {
		calls := 0
		fsync = func(fd int) error {
			calls++
			if fd != int(file.Fd()) {
				t.Errorf("fsync on descriptor %d, want %d", fd, file.Fd())
			}
			return want
		}
		if err := Flush(file); err != want {
			t.Fatalf("got %#v, want %#v", err, want)
		}
		if calls != 1 {
			t.Fatalf("%v: fsync called %d times, want once", want, calls)
		}
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("file unusable after a failed flush: %v", err)
	}
	fsync = previous
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fsync = func(int) error { calls++; return nil }
	if err := Flush(file); !errors.Is(err, os.ErrClosed) || calls != 0 {
		t.Fatalf("closed file reached fsync: %v, %d calls", err, calls)
	}
}
