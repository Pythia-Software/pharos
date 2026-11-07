//go:build darwin || linux

package archive

import (
	"errors"
	"os"
	"syscall"
)

func readDirectoryVolumeIdentity(path string) (directoryVolumeID, error) {
	info, err := os.Stat(path)
	if err != nil {
		return directoryVolumeID{}, err
	}
	return directoryVolumeIdentityFromInfo(info)
}

func directoryVolumeIdentityFromInfo(info os.FileInfo) (directoryVolumeID, error) {
	if info == nil {
		return directoryVolumeID{}, errors.ErrUnsupported
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return directoryVolumeID{}, errors.ErrUnsupported
	}
	return directoryVolumeID{device: uint64(stat.Dev)}, nil
}
