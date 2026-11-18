//go:build linux

package credentialstore

import "golang.org/x/sys/unix"

func durableFileSync(fd int) error { return unix.Fsync(fd) }
