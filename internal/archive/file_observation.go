package archive

import (
	"errors"
	"os"
)

// File observations: what Pharos reads from a file's native stat metadata.
// They keep three meanings apart, each from one FileInfo's own stat result:
//
//   - libraryDirectoryInode is the v1 library descriptor's directory_inode,
//     a persisted identity that moves with a renamed directory and changes
//     with a copied one.
//   - placementDevice is the device a path is on, for same-volume decisions
//     and in-memory cache keys. legacyDeviceLabel is volumeIdentity's older
//     "device:N" fallback spelling of the same device.
//   - hardlinkKey is a device and inode pair, for counting a hard-linked
//     file once in informational storage accounting.
//
// Darwin and Linux read syscall.Stat_t (file_observation_unix.go). Missing or
// foreign metadata, and every other platform (file_observation_other.go),
// fail with errFileObservationUnavailable rather than return a zero that a
// real file could also have. A cache key, volume UUID, pathname or Windows
// file ID is no substitute for directory_inode.
var errFileObservationUnavailable = errors.New("native file identity is unavailable")

// statDevice is os.Stat outside tests, which observe synthetic devices.
var statDevice = os.Stat

// deviceOf reports the device holding path, following symlinks.
func deviceOf(path string) (uint64, error) {
	info, err := statDevice(path)
	if err != nil {
		return 0, err
	}
	return placementDevice(info)
}

// observeSameDevice reports whether a and b are on one device. It fails when
// either device cannot be observed: uncertainty is neither answer.
func observeSameDevice(a, b string) (bool, error) {
	deviceA, err := deviceOf(a)
	if err != nil {
		return false, err
	}
	deviceB, err := deviceOf(b)
	if err != nil {
		return false, err
	}
	return deviceA == deviceB, nil
}
