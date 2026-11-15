//go:build darwin && !fsinfo_unsupported

package fsinfo

import "golang.org/x/sys/unix"

// blockSize is statfs(2)'s f_bsize, the unit of f_blocks and f_bavail.
func blockSize(stat *unix.Statfs_t) (uint64, error) {
	return uint64(stat.Bsize), nil
}

// mountReadOnly reads MNT_RDONLY from f_flags.
func mountReadOnly(stat *unix.Statfs_t) (bool, error) {
	return stat.Flags&unix.MNT_RDONLY != 0, nil
}
