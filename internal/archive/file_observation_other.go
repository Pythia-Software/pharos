//go:build !darwin && !linux

package archive

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// This platform has no observation that means what the Unix ones do. In
// particular it cannot yet create or validate a v1 writable library, whose
// descriptor persists a Unix directory inode.
func unsupportedFileObservation() error {
	return fmt.Errorf("%w on %s: %w", errFileObservationUnavailable, runtime.GOOS, errors.ErrUnsupported)
}

func libraryDirectoryInode(os.FileInfo) (uint64, error) { return 0, unsupportedFileObservation() }

func placementDevice(os.FileInfo) (uint64, error) { return 0, unsupportedFileObservation() }

func legacyDeviceLabel(os.FileInfo) (string, error) { return "", unsupportedFileObservation() }

func hardlinkKey(os.FileInfo) ([2]uint64, error) { return [2]uint64{}, unsupportedFileObservation() }
