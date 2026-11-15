package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/gbdubs/pharos/internal/platform/filesync"
)

// fileSyncFailure is a plain flush failure, and the error a caller must
// still report as itself.
type fileSyncFailure struct {
	name    string
	err, is error
}

func fileSyncFailures() []fileSyncFailure {
	return []fileSyncFailure{
		{"EIO", syscall.EIO, syscall.EIO},
		{"wrapped EIO", fmt.Errorf("flush: %w", syscall.EIO), syscall.EIO},
		{"joined EIO", errors.Join(errors.New("drive reported"), syscall.EIO), syscall.EIO},
		{"EINTR", syscall.EINTR, syscall.EINTR},
		{"permission", &os.PathError{Op: "fsync", Path: "file", Err: syscall.EACCES}, os.ErrPermission},
		{"unsupported", filesync.ErrUnsupported, filesync.ErrUnsupported},
	}
}

// failFileSync makes syncFileData fail for the files fails accepts, once
// each, and flush others for real. It returns the number of failed calls.
func failFileSync(t *testing.T, failure error, fails func(*os.File) bool) *int {
	t.Helper()
	calls := new(int)
	override(t, &syncFileData, func(file *os.File) error {
		if fails(file) {
			*calls++
			return failure
		}
		return filesync.Flush(file)
	})
	return calls
}

// syncPath flushes through the read-only handle it opens, then reports what
// that handle sees; it never reports a file it could not flush.
func TestSyncPathFlushesItsReadOnlyHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot")
	if err := os.WriteFile(path, []byte("snapshot"), 0o400); err != nil {
		t.Fatal(err)
	}
	var flushed *os.File
	override(t, &syncFileData, func(file *os.File) error {
		flushed = file
		if _, err := file.Write([]byte("x")); err == nil {
			t.Error("syncPath flushed a writable handle")
		}
		return filesync.Flush(file)
	})
	info, err := syncPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if want, err := os.Stat(path); err != nil || !os.SameFile(info, want) || info.Size() != int64(len("snapshot")) || !info.ModTime().Equal(want.ModTime()) {
		t.Fatalf("syncPath reported %v, want the file's own info %v (%v)", info, want, err)
	}
	if flushed == nil || flushed.Name() != path {
		t.Fatalf("syncPath flushed %v", flushed)
	}
	if _, err := flushed.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("syncPath left its handle open: %v", err)
	}
	for _, failure := range fileSyncFailures() {
		t.Run(failure.name, func(t *testing.T) {
			flushed = nil
			calls := 0
			override(t, &syncFileData, func(file *os.File) error {
				calls++
				flushed = file
				return failure.err
			})
			info, err := syncPath(path)
			if !errors.Is(err, failure.is) || info != nil || calls != 1 {
				t.Fatalf("syncPath = %v, %v after %d flushes; want %v once and no info", info, err, calls, failure.is)
			}
			if _, err := flushed.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("syncPath left its handle open after a failed flush: %v", err)
			}
		})
	}
	if _, err := syncPath(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}
