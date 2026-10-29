package archive

import (
	"errors"
	"os"
	"syscall"
)

// renameExclusive renames from to to, failing with an EEXIST-like error
// rather than replacing anything already at to; a plain rename on macOS
// silently replaces an empty directory. Where the volume has no exclusive
// rename (exFAT answers ENOTSUP), it checks first and renames, which is
// exclusive unless another process creates to in between: reader leases,
// its only caller, retry under a fresh random name on a clash, and their
// names are random, so that window costs nothing.
func renameExclusive(from, to string) error {
	err := renameExclusiveNative(from, to)
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return renameChecked(from, to)
	}
	return err
}

// renameChecked renames from to to unless something is already at to.
func renameChecked(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}
