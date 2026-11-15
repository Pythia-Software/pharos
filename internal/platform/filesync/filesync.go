// Package filesync flushes the data and metadata a caller-owned open file has
// buffered in the operating system to its storage device, with one fsync(2).
//
// Flush borrows the file: the caller opens, retains and closes it. This
// package never opens paths, closes, renames, retries, or syncs directories.
// A flush is a plain buffer flush. It does not ask the device to empty its own
// write cache (macOS F_FULLFSYNC), so it makes no claim of durability across
// power loss; callers that need that use a separate, stronger operation.
package filesync

import (
	"errors"
	"os"
)

// ErrUnsupported identifies an unimplemented platform adapter.
var ErrUnsupported = errors.New("file flushing unsupported on this platform")

func validateFile(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	if file.Fd() == ^uintptr(0) {
		return os.ErrClosed
	}
	return nil
}
