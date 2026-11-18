//go:build darwin

package archive

import (
	"golang.org/x/sys/unix"
	"os"
)

func teamsBirthSyncFile(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return err
}
