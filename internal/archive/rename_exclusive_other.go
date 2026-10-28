//go:build !darwin

package archive

import (
	"errors"
	"os"
)

// renameExclusive renames from to to unless something is already at to.
// Without an exclusive rename the check is best effort.
func renameExclusive(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}
