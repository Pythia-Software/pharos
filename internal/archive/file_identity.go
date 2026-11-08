package archive

import (
	"errors"
	"fmt"
	"io/fs"
)

// File identity: the device, inode and link count of the one object a
// FileInfo describes. It is read from that FileInfo alone, never by opening
// its name again, so a key never pairs one object's identity with another's
// size or modification time.
//
// Copy optimizations (backup hard-link reuse, adoption's hard links) take the
// bool wrapper keyOf and copy independently without one. Deletion and safety
// checks (prune retention, history-root guards) take the error, which keeps
// "this platform reports none" (errFileIdentityUnsupported) apart from "this
// FileInfo carries none" (errFileIdentityUnavailable) and from I/O errors.

// fileKey is one file system object: its device and inode, widened to uint64
// exactly as Darwin and Linux report them. Persisted history plans store it as
// "%d:%d" of these two fields (see rootIdentity).
type fileKey struct{ device, inode uint64 }

type fileObservation struct {
	key   fileKey
	links uint64 // hard links to the object, 0 where it is not reported
}

var (
	errFileIdentityUnsupported = errors.New("file identity is not observed on this platform")
	errFileIdentityUnavailable = errors.New("file identity is unavailable")
)

// observeFile reads the identity of the object info describes. Tests replace
// it to make identities unavailable.
var observeFile = func(info fs.FileInfo) (fileObservation, error) {
	if info == nil {
		return fileObservation{}, fmt.Errorf("%w: no file information", errFileIdentityUnavailable)
	}
	return observeNativeFile(info)
}

// keyOf is the optional form: false means copy without sharing, and the
// zero key it returns then is never to be stored.
func keyOf(info fs.FileInfo) (fileKey, bool) {
	observed, err := observeFile(info)
	if err != nil {
		return fileKey{}, false
	}
	return observed.key, true
}

// rootIdentity is a history plan's capture_root_identity, in the encoding
// persisted plans and jobs already hold: unsigned device and inode. It is not
// localidentity.DeviceInode's, which formats Darwin's device signed.
func rootIdentity(info fs.FileInfo) (string, error) {
	observed, err := observeFile(info)
	if err != nil {
		return "", fmt.Errorf("capture root identity: %w", err)
	}
	return fmt.Sprintf("%d:%d", observed.key.device, observed.key.inode), nil
}
