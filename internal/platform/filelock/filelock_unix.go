//go:build darwin || linux

package filelock

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

// Lock borrows file and takes a shared or exclusive flock. Wait makes one
// blocking syscall, without retrying EINTR. Try returns ErrContended together
// with the underlying errno only for nonblocking EWOULDBLOCK/EAGAIN.
func Lock(file *os.File, mode Mode, wait WaitMode) error {
	if err := validateLock(file, mode, wait); err != nil {
		return err
	}
	op := syscall.LOCK_SH
	if mode == Exclusive {
		op = syscall.LOCK_EX
	}
	if wait == Try {
		op |= syscall.LOCK_NB
	}
	err := syscall.Flock(int(file.Fd()), op)
	runtime.KeepAlive(file)
	if wait == Try && (errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
		return errors.Join(ErrContended, err)
	}
	return err
}

// Unlock releases file's flock without closing the borrowed file.
func Unlock(file *os.File) error {
	if err := validateFile(file); err != nil {
		return err
	}
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	runtime.KeepAlive(file)
	return err
}
