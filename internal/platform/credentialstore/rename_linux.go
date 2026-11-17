//go:build linux

package credentialstore

import "golang.org/x/sys/unix"

func renameNoReplace(root int, from, to string) error {
	return unix.Renameat2(root, from, root, to, unix.RENAME_NOREPLACE)
}
