//go:build darwin

package archive

import "golang.org/x/sys/unix"

func teamsRenameBirthExclusive(parent int, from, to string) error {
	return unix.RenameatxNp(parent, from, parent, to, unix.RENAME_EXCL)
}
