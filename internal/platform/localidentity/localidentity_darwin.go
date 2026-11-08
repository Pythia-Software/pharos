//go:build darwin && !localidentity_unsupported

package localidentity

import (
	"strconv"
	"syscall"
)

// Darwin's dev_t is a signed 32-bit value; the Key keeps its 32 bits and the
// legacy text prints it signed, as fmt printed the Stat_t field.
func fromStat(stat *syscall.Stat_t) Key {
	return Key{device: uint64(uint32(stat.Dev)), inode: stat.Ino, valid: true}
}

func formatDevice(device uint64) string {
	return strconv.FormatInt(int64(int32(uint32(device))), 10)
}
