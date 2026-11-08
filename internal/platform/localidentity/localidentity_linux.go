//go:build linux && !localidentity_unsupported

package localidentity

import (
	"strconv"
	"syscall"
)

// Linux's dev_t is unsigned and 64 bits wide on the common architectures.
func fromStat(stat *syscall.Stat_t) Key {
	return Key{device: uint64(stat.Dev), inode: uint64(stat.Ino), valid: true}
}

func formatDevice(device uint64) string {
	return strconv.FormatUint(device, 10)
}
