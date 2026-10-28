//go:build darwin

package archive

import "golang.org/x/sys/unix"

// renameExclusiveNative is renamex_np with RENAME_EXCL: it fails (EEXIST)
// rather than replace anything already at to. Some volumes (exFAT) do not
// support it; renameExclusive falls back.
var renameExclusiveNative = func(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
