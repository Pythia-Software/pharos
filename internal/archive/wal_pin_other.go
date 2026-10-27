//go:build !darwin

package archive

import "errors"

// walReadLockHolders needs F_GETLK on the -shm file; only macOS, where
// Pharos runs, is supported.
func walReadLockHolders(string, []int) ([]walReader, error) {
	return nil, errors.New("cannot read the catalog's read locks on this platform")
}
