//go:build (darwin || linux) && !fsinfo_unsupported

package fsinfo

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func fakeStatfs(t *testing.T, stat unix.Statfs_t, err error) *[]string {
	t.Helper()
	calls := []string{}
	previous := statfs
	statfs = func(path string, into *unix.Statfs_t) error {
		calls = append(calls, path)
		*into = stat
		return err
	}
	t.Cleanup(func() { statfs = previous })
	return &calls
}

func TestCapacityObservesTheFileSystem(t *testing.T) {
	dir := t.TempDir()
	space, err := Capacity(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		t.Fatal(err)
	}
	if want := uint64(stat.Blocks) * uint64(stat.Bsize); space.TotalBytes != want || space.TotalBytes == 0 {
		t.Fatalf("total %d, statfs says %d", space.TotalBytes, want)
	}
	if space.AvailableBytes > space.TotalBytes {
		t.Fatalf("available %d exceeds total %d", space.AvailableBytes, space.TotalBytes)
	}
	missing := filepath.Join(dir, "missing")
	_, err = Capacity(missing)
	var pathErr *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Path != missing {
		t.Fatalf("missing path: %v", err)
	}
	if _, err := Capacity(dir); err != nil {
		t.Fatal("a failed observation left state behind:", err)
	}
}

func TestCapacityCountsCallerAvailableBlocks(t *testing.T) {
	stat := statfsOf(4096, 100, 30, false)
	stat.Bfree = 70 // reserved blocks the caller cannot use
	calls := fakeStatfs(t, stat, nil)
	space, err := Capacity("/library")
	if err != nil || space != (Space{TotalBytes: 409600, AvailableBytes: 122880}) {
		t.Fatalf("%+v, %v", space, err)
	}
	if len(*calls) != 1 || (*calls)[0] != "/library" {
		t.Fatalf("statfs calls %q", *calls)
	}
	fakeStatfs(t, statfsOf(4096, 100, 0, false), nil)
	if space, err := Capacity("/full"); err != nil || space.AvailableBytes != 0 || space.TotalBytes != 409600 {
		t.Fatalf("a full file system: %+v, %v", space, err)
	}
	fakeStatfs(t, statfsOf(1<<20, 1<<44, 1, false), nil)
	if space, err := Capacity("/huge"); !errors.Is(err, ErrRange) || space != (Space{}) {
		t.Fatalf("overflowing total: %+v, %v", space, err)
	}
	fakeStatfs(t, statfsOf(0, 100, 30, false), nil)
	if _, err := Capacity("/zero"); !errors.Is(err, ErrRange) {
		t.Fatalf("zero block size: %v", err)
	}
}

func TestNativeErrorsArePreserved(t *testing.T) {
	calls := fakeStatfs(t, statfsOf(4096, 100, 30, true), unix.EACCES)
	if space, err := Capacity("/denied"); !errors.Is(err, unix.EACCES) || space != (Space{}) {
		t.Fatalf("capacity: %+v, %v", space, err)
	}
	if readOnly, err := ReadOnlyMount("/denied"); !errors.Is(err, unix.EACCES) || readOnly {
		t.Fatalf("mount flags: %v, %v", readOnly, err)
	}
	for _, path := range []string{"", "a\x00b"} {
		Capacity(path)
		ReadOnlyMount(path)
	}
	if len(*calls) != 2 {
		t.Fatalf("invalid paths reached statfs: %q", *calls)
	}
}

func TestReadOnlyMountReadsTheMountFlag(t *testing.T) {
	fakeStatfs(t, statfsOf(4096, 100, 30, true), nil)
	if readOnly, err := ReadOnlyMount("/ro"); err != nil || !readOnly {
		t.Fatalf("read-only mount: %v, %v", readOnly, err)
	}
	fakeStatfs(t, statfsOf(4096, 100, 30, false), nil)
	if readOnly, err := ReadOnlyMount("/rw"); err != nil || readOnly {
		t.Fatalf("writable mount: %v, %v", readOnly, err)
	}
	if readOnly, err := ReadOnlyMount(t.TempDir()); err != nil || readOnly {
		t.Fatalf("the temporary directory: %v, %v", readOnly, err)
	}
}

// Each observation stands without the other: an invalid block size does not
// hide the mount flag, and unusable mount flags do not hide the capacity.
func TestObservationsAreIndependent(t *testing.T) {
	fakeStatfs(t, statfsOf(0, 100, 30, true), nil)
	if _, err := Capacity("/x"); !errors.Is(err, ErrRange) {
		t.Fatal(err)
	}
	if readOnly, err := ReadOnlyMount("/x"); err != nil || !readOnly {
		t.Fatalf("mount flags beside an invalid size: %v, %v", readOnly, err)
	}
	stat := statfsOf(4096, 100, 30, true)
	withoutMountFlags(&stat)
	fakeStatfs(t, stat, nil)
	if space, err := Capacity("/x"); err != nil || space.AvailableBytes != 122880 {
		t.Fatalf("capacity beside unknown flags: %+v, %v", space, err)
	}
}
