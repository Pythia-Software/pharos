//go:build linux && !fsinfo_unsupported

package fsinfo

import (
	"errors"

	"golang.org/x/sys/unix"
)

// stValid is ST_VALID from linux/statfs.h: the kernel sets it whenever it
// fills f_flags. Without it the flags are not evidence either way.
// golang.org/x/sys/unix does not name it.
const stValid = 0x0020

var errFlagsUnavailable = errors.New("statfs did not report mount flags")

// blockSize is statfs(2)'s f_bsize, the unit Pharos has always counted
// f_blocks and f_bavail in. A signed field that is not positive is invalid.
func blockSize(stat *unix.Statfs_t) (uint64, error) {
	if stat.Bsize <= 0 {
		return 0, ErrRange
	}
	return uint64(stat.Bsize), nil
}

// mountReadOnly reads ST_RDONLY from f_flags, when the kernel says it set them.
func mountReadOnly(stat *unix.Statfs_t) (bool, error) {
	if stat.Flags&stValid == 0 {
		return false, errFlagsUnavailable
	}
	return stat.Flags&unix.ST_RDONLY != 0, nil
}
