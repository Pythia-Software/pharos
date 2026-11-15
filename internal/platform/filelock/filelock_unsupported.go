//go:build !darwin && !linux

package filelock

import "os"

func Lock(file *os.File, mode Mode, wait WaitMode) error {
	if err := validateLock(file, mode, wait); err != nil {
		return err
	}
	return ErrUnsupported
}

func Unlock(file *os.File) error {
	if err := validateFile(file); err != nil {
		return err
	}
	return ErrUnsupported
}
