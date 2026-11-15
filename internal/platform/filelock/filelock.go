// Package filelock provides advisory locks on caller-owned open files.
// Callers are responsible for opening, retaining and closing the file. Locks
// last until Unlock or closure of the final descriptor referring to the same
// open-file description. Callers must not concurrently close the file or change
// its lock mode. This package never creates, writes, removes, duplicates or
// closes files.
package filelock

import (
	"errors"
	"os"
)

// Mode selects a shared or exclusive lock.
type Mode uint8

const (
	Shared Mode = iota
	Exclusive
)

// WaitMode selects nonblocking or blocking acquisition.
type WaitMode uint8

const (
	Try WaitMode = iota
	Wait
)

var (
	// ErrContended identifies nonblocking contention, alongside its OS error.
	ErrContended = errors.New("file lock contended")
	// ErrUnsupported identifies an unimplemented platform adapter.
	ErrUnsupported = errors.New("file locking unsupported on this platform")
)

func validateLock(file *os.File, mode Mode, wait WaitMode) error {
	if mode != Shared && mode != Exclusive || wait != Try && wait != Wait {
		return os.ErrInvalid
	}
	return validateFile(file)
}

func validateFile(file *os.File) error {
	if file == nil {
		return os.ErrInvalid
	}
	if file.Fd() == ^uintptr(0) {
		return os.ErrClosed
	}
	return nil
}
