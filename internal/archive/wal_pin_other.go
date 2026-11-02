//go:build !darwin

package archive

import "errors"

// probeWALShm needs F_GETLK on SQLite's own -shm descriptor; only macOS,
// where Pharos runs, is supported, so the holder is reported as unknown.
func probeWALShm(string) (walProbe, error) {
	return walProbe{}, errors.New("cannot read the catalog's locks on this platform")
}
