//go:build linux && !fsinfo_unsupported

package fsinfo

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func statfsOf(size int64, blocks, available uint64, readOnly bool) unix.Statfs_t {
	stat := unix.Statfs_t{Blocks: blocks, Bfree: available, Bavail: available, Flags: stValid}
	setField(&stat.Bsize, size)
	if readOnly {
		stat.Flags |= unix.ST_RDONLY
	}
	return stat
}

// setField stores v in a field whose width and sign vary by architecture.
func setField[T ~int32 | ~int64 | ~uint32](field *T, v int64) { *field = T(v) }

func withoutMountFlags(stat *unix.Statfs_t) { stat.Flags &^= stValid }

func TestLinuxFlagsAndSizes(t *testing.T) {
	stat := statfsOf(4096, 100, 30, true)
	withoutMountFlags(&stat)
	fakeStatfs(t, stat, nil)
	// ST_RDONLY without ST_VALID is not evidence.
	if readOnly, err := ReadOnlyMount("/x"); err == nil || readOnly || !errors.Is(err, errFlagsUnavailable) {
		t.Fatalf("flags the kernel did not set: %v, %v", readOnly, err)
	}
	if negative := statfsOf(-4096, 100, 30, false); negative.Bsize <= 0 { // unsigned on some architectures
		fakeStatfs(t, negative, nil)
		if _, err := Capacity("/x"); !errors.Is(err, ErrRange) {
			t.Fatalf("negative block size: %v", err)
		}
	}
}
