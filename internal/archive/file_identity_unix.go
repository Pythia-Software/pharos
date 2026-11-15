//go:build darwin || linux

package archive

import (
	"fmt"
	"io/fs"
	"syscall"
)

func observeNativeFile(info fs.FileInfo) (fileObservation, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return fileObservation{}, fmt.Errorf("%w: %T carries no native file status", errFileIdentityUnavailable, info.Sys())
	}
	// Darwin's Dev is an int32: converted as it always was, so a persisted
	// root identity keeps its value.
	return fileObservation{key: fileKey{uint64(stat.Dev), uint64(stat.Ino)}, links: uint64(stat.Nlink)}, nil
}
