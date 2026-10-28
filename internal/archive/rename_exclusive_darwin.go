//go:build darwin

package archive

import "golang.org/x/sys/unix"

// renameExclusive renames from to to, failing (EEXIST) rather than replacing
// anything already at to; a plain rename on macOS silently replaces an empty
// directory.
func renameExclusive(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
