//go:build darwin

package credentialstore

import "golang.org/x/sys/unix"

func renameNoReplace(root int, from, to string) error {
	return unix.RenameatxNp(root, from, root, to, unix.RENAME_EXCL)
}
