//go:build darwin || linux

package archive

import (
	"os"
	"syscall"
)

// openInputNonblockingNative opens path read-only with O_NONBLOCK, so a FIFO
// or device put where a regular file was expected cannot block the open. It
// follows symlinks as os.OpenFile does. The caller owns the file: it closes
// it and checks that the opened handle is regular before reading.
func openInputNonblockingNative(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// openInputNonblockingInNative is openInputNonblockingNative for a name
// inside root, which keeps the open, symlinks included, within it.
func openInputNonblockingInNative(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
