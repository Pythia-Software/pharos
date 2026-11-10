//go:build (darwin || linux) && !filesync_unsupported

package filesync

import (
	"os"
	"runtime"
	"syscall"
)

// fsync is the one system call Flush makes; tests count its calls.
var fsync = syscall.Fsync

// Flush makes one fsync(2) on file's descriptor and returns its error as the
// system reported it, without retrying EINTR.
func Flush(file *os.File) error {
	if err := validateFile(file); err != nil {
		return err
	}
	err := fsync(int(file.Fd()))
	runtime.KeepAlive(file)
	return err
}
