//go:build (darwin || linux) && !fsinfo_unsupported

package fsinfo

import (
	"io/fs"

	"golang.org/x/sys/unix"
)

// statfs is the one system call both operations make; tests replace it.
var statfs = unix.Statfs

func observe(op, path string) (unix.Statfs_t, error) {
	var stat unix.Statfs_t
	if err := validatePath(op, path); err != nil {
		return stat, err
	}
	if err := statfs(path, &stat); err != nil {
		return stat, &fs.PathError{Op: "statfs", Path: path, Err: err}
	}
	return stat, nil
}

// Capacity reports Blocks × Bsize as TotalBytes and Bavail × Bsize, the
// blocks an unprivileged caller may use, as AvailableBytes.
func Capacity(path string) (Space, error) {
	stat, err := observe("capacity", path)
	if err != nil {
		return Space{}, err
	}
	size, err := blockSize(&stat)
	if err == nil {
		var space Space
		if space, err = spaceFromBlocks(size, uint64(stat.Blocks), uint64(stat.Bavail)); err == nil {
			return space, nil
		}
	}
	return Space{}, &fs.PathError{Op: "capacity", Path: path, Err: err}
}

// ReadOnlyMount reports the file system's native read-only mount flag. It
// does not look at file modes or try a write.
func ReadOnlyMount(path string) (bool, error) {
	stat, err := observe("mount flags", path)
	if err != nil {
		return false, err
	}
	readOnly, err := mountReadOnly(&stat)
	if err != nil {
		return false, &fs.PathError{Op: "mount flags", Path: path, Err: err}
	}
	return readOnly, nil
}
