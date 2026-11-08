//go:build windows && !fsinfo_unsupported

package fsinfo

import (
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// getDiskFreeSpaceEx is GetDiskFreeSpaceExW; tests replace it to supply
// quota-limited and 64-bit results.
var getDiskFreeSpaceEx = windows.GetDiskFreeSpaceEx

// Capacity reports GetDiskFreeSpaceExW's lpFreeBytesAvailableToCaller as
// AvailableBytes and lpTotalNumberOfBytes as TotalBytes. Both are what the
// calling user may use, so a disk quota can make them smaller than the disk;
// TotalBytes is not the physical size of the volume.
//
// path must name an existing directory. A path that is only a volume, such
// as C: or \\server\share, is given the trailing separator that makes it the
// volume's root; nothing else is rewritten, and no parent is tried.
func Capacity(path string) (Space, error) {
	if err := validatePath("capacity", path); err != nil {
		return Space{}, err
	}
	name, err := windows.UTF16PtrFromString(directoryName(path))
	if err != nil {
		return Space{}, &fs.PathError{Op: "capacity", Path: path, Err: err}
	}
	var available, total, free uint64
	if err := getDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return Space{}, &fs.PathError{Op: "GetDiskFreeSpaceExW", Path: path, Err: err}
	}
	return Space{TotalBytes: total, AvailableBytes: available}, nil
}

// ReadOnlyMount is not implemented on Windows: a volume flag read through a
// separately resolved path is not proof that a particular file cannot change.
func ReadOnlyMount(path string) (bool, error) {
	if err := validatePath("mount flags", path); err != nil {
		return false, err
	}
	return false, ErrUnsupported
}

// directoryName spells a bare volume as its root directory. C: alone names
// the current directory on drive C, and GetDiskFreeSpaceExW requires a UNC
// share to end with a backslash.
func directoryName(path string) string {
	if volume := filepath.VolumeName(path); volume == path && !strings.HasSuffix(path, `\`) && !strings.HasSuffix(path, `/`) {
		return path + `\`
	}
	return path
}
