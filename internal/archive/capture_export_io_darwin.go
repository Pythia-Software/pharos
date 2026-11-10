//go:build darwin

package archive

import "golang.org/x/sys/unix"

// exportRenameNoReplace is renameatx_np with RENAME_EXCL; tests replace it.
var exportRenameNoReplace = func(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_EXCL)
}
