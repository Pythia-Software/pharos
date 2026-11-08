//go:build windows && !fsinfo_unsupported

package fsinfo

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

type diskFreeResult struct {
	available, total, free uint64
	err                    error
}

// fakeDiskFree replaces GetDiskFreeSpaceExW and records the directory names
// it was given.
func fakeDiskFree(t *testing.T, result diskFreeResult) *[]string {
	t.Helper()
	names := []string{}
	previous := getDiskFreeSpaceEx
	getDiskFreeSpaceEx = func(name *uint16, available, total, free *uint64) error {
		if name == nil {
			t.Fatal("a nil directory name asks for the current drive")
		}
		names = append(names, windows.UTF16PtrToString(name))
		if result.err != nil {
			return result.err
		}
		*available, *total, *free = result.available, result.total, result.free
		return nil
	}
	t.Cleanup(func() { getDiskFreeSpaceEx = previous })
	return &names
}

func TestCapacityOfAUnicodeDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Bibliothèque 図書館 📚")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	space, err := Capacity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if space.TotalBytes == 0 || space.AvailableBytes > space.TotalBytes {
		t.Fatalf("%+v", space)
	}
	if readOnly, err := ReadOnlyMount(dir); readOnly || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("mount flags: %v, %v", readOnly, err)
	}
}

func TestMissingAndInvalidPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "deeper")
	_, err := Capacity(missing)
	var pathErr *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Path != missing {
		t.Fatalf("missing directory: %v", err)
	}
	names := fakeDiskFree(t, diskFreeResult{available: 1, total: 1})
	for _, path := range []string{"", "C:\\a\x00b"} {
		if _, err := Capacity(path); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Capacity(%q): %v", path, err)
		}
	}
	if len(*names) != 0 {
		t.Fatalf("invalid paths reached the API: %q", *names)
	}
}

func TestCapacityMapsCallerQuotaAnd64BitValues(t *testing.T) {
	// A quota: the caller may use less than the volume's free space, and
	// sees a quota-limited total.
	fakeDiskFree(t, diskFreeResult{available: 3 << 30, total: 10 << 30, free: 900 << 30})
	if space, err := Capacity(`C:\Users\me`); err != nil || space != (Space{TotalBytes: 10 << 30, AvailableBytes: 3 << 30}) {
		t.Fatalf("quota: %+v, %v", space, err)
	}
	fakeDiskFree(t, diskFreeResult{available: math.MaxUint64 - 1, total: math.MaxUint64, free: math.MaxUint64})
	if space, err := Capacity(`D:\`); err != nil || space.TotalBytes != math.MaxUint64 || space.AvailableBytes != math.MaxUint64-1 {
		t.Fatalf("64-bit values: %+v, %v", space, err)
	}
	fakeDiskFree(t, diskFreeResult{available: 0, total: 1 << 40, free: 1 << 30})
	if space, err := Capacity(`E:\full`); err != nil || space.AvailableBytes != 0 {
		t.Fatalf("nothing left for the caller: %+v, %v", space, err)
	}
}

func TestNativeErrorIsPreserved(t *testing.T) {
	for _, native := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_BAD_NETPATH, windows.ERROR_NOT_READY} {
		fakeDiskFree(t, diskFreeResult{err: native})
		space, err := Capacity(`\\server\share\dir`)
		var pathErr *fs.PathError
		if !errors.Is(err, native) || !errors.As(err, &pathErr) || space != (Space{}) {
			t.Errorf("%v: %+v, %v", native, space, err)
		}
	}
}

func TestVolumeRootSpelling(t *testing.T) {
	for path, want := range map[string]string{
		`C:`:                    `C:\`,
		`C:\`:                   `C:\`,
		`C:\Pharos`:             `C:\Pharos`,
		`C:\Pharos\`:            `C:\Pharos\`,
		`\\server\share`:        `\\server\share\`,
		`\\server\share\`:       `\\server\share\`,
		`\\server\share\Pharos`: `\\server\share\Pharos`,
		`//server/share`:        `//server/share\`,
		`\\?\C:`:                `\\?\C:\`,
		`\\?\UNC\server\share`:  `\\?\UNC\server\share\`,
		`\\?\C:\Pharos`:         `\\?\C:\Pharos`,
		`relative\dir`:          `relative\dir`,
	} {
		names := fakeDiskFree(t, diskFreeResult{available: 1, total: 2})
		if _, err := Capacity(path); err != nil || len(*names) != 1 || (*names)[0] != want {
			t.Errorf("%q was asked as %q (%v); want %q", path, *names, err, want)
		}
	}
}
