//go:build darwin && !fsinfo_unsupported

package fsinfo

import (
	"testing"

	"golang.org/x/sys/unix"
)

func statfsOf(size uint32, blocks, available uint64, readOnly bool) unix.Statfs_t {
	stat := unix.Statfs_t{Bsize: size, Blocks: blocks, Bfree: available, Bavail: available, Flags: unix.MNT_LOCAL}
	if readOnly {
		stat.Flags |= unix.MNT_RDONLY
	}
	return stat
}

// Darwin always fills f_flags, so there is no state of unknown flags.
func withoutMountFlags(*unix.Statfs_t) {}

// Since macOS 10.15 the system volume at / is mounted read-only.
func TestSystemVolumeIsReadOnly(t *testing.T) {
	readOnly, err := ReadOnlyMount("/")
	if err != nil {
		t.Fatal(err)
	}
	if !readOnly {
		t.Skip("this Mac's system volume is mounted writable")
	}
}
