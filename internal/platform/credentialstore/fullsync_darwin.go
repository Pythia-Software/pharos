//go:build darwin

package credentialstore

import "golang.org/x/sys/unix"

func durableFileSync(fd int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_FULLFSYNC, 0)
	return err
}
