//go:build linux

package archive

import "golang.org/x/sys/unix"

// exportRenameNoReplace is renameat2 with RENAME_NOREPLACE; tests replace it.
var exportRenameNoReplace = func(fromFD int, from string, toFD int, to string) error {
	return unix.Renameat2(fromFD, from, toFD, to, unix.RENAME_NOREPLACE)
}
