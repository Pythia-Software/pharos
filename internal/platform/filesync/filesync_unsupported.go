//go:build (!darwin && !linux) || filesync_unsupported

package filesync

import "os"

// Flush validates file and reports ErrUnsupported. It does not fall back to
// os.File.Sync, whose strength differs by platform.
func Flush(file *os.File) error {
	if err := validateFile(file); err != nil {
		return err
	}
	return ErrUnsupported
}
