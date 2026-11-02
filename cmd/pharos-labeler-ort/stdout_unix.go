//go:build unix

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reserveStdout moves the reply stream off file descriptor 1 and points
// descriptor 1 at stderr, so that a native library printing to stdout cannot
// corrupt the frames. It returns the reply stream, or os.Stdout if it cannot.
func reserveStdout() *os.File {
	fd, err := unix.Dup(1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pharos-labeler-ort: keeping replies on stdout: %v\n", err)
		return os.Stdout
	}
	unix.CloseOnExec(fd)
	if err := unix.Dup2(2, 1); err != nil {
		unix.Close(fd)
		fmt.Fprintf(os.Stderr, "pharos-labeler-ort: keeping replies on stdout: %v\n", err)
		return os.Stdout
	}
	return os.NewFile(uintptr(fd), "replies")
}
