//go:build !darwin && !linux

package archive

import "io/fs"

// Elsewhere no identity is read yet: backups and adoption copy without hard
// links, prune keeps what it cannot identify, and history planning refuses.
func observeNativeFile(fs.FileInfo) (fileObservation, error) {
	return fileObservation{}, errFileIdentityUnsupported
}
