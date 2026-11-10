//go:build !darwin && !linux

package archive

import "errors"

func readDirectoryVolumeIdentity(path string) (directoryVolumeID, error) {
	return directoryVolumeID{}, errors.ErrUnsupported
}
