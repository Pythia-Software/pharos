//go:build darwin || linux

package archive

import (
	"fmt"
	"os"
	"syscall"
)

func nativeStat(info os.FileInfo) (*syscall.Stat_t, error) {
	if info == nil {
		return nil, fmt.Errorf("%w: no file metadata", errFileObservationUnavailable)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return nil, fmt.Errorf("%w: %T is not native stat metadata", errFileObservationUnavailable, info.Sys())
	}
	return stat, nil
}

// libraryDirectoryInode is the value v1 descriptors persist as
// directory_inode: the full-width native inode.
func libraryDirectoryInode(info os.FileInfo) (uint64, error) {
	stat, err := nativeStat(info)
	if err != nil {
		return 0, err
	}
	return uint64(stat.Ino), nil
}

// placementDevice widens the native device to uint64. Darwin's int32 is
// sign-extended, as the drive cache and lookupVolumeIdentity always have.
func placementDevice(info os.FileInfo) (uint64, error) {
	stat, err := nativeStat(info)
	if err != nil {
		return 0, err
	}
	return uint64(stat.Dev), nil
}

// legacyDeviceLabel formats the native device as it is, so Darwin's pinned
// volume_id fallbacks stay signed ("device:-1") and Linux's full width.
func legacyDeviceLabel(info os.FileInfo) (string, error) {
	stat, err := nativeStat(info)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("device:%d", stat.Dev), nil
}

func hardlinkKey(info os.FileInfo) ([2]uint64, error) {
	stat, err := nativeStat(info)
	if err != nil {
		return [2]uint64{}, err
	}
	return [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}, nil
}
