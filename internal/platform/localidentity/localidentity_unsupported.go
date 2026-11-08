//go:build (!darwin && !linux) || localidentity_unsupported

package localidentity

import (
	"io/fs"
	"strconv"
)

// of reports ErrUnsupported. It does not substitute a pathname hash or a
// platform file ID whose width and volume scope have no agreed contract.
func of(fs.FileInfo) (Key, error) {
	return Key{}, ErrUnsupported
}

// No Key is valid here, so formatDevice is never reached.
func formatDevice(device uint64) string {
	return strconv.FormatUint(device, 10)
}
