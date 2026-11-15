//go:build (darwin || linux) && !localidentity_unsupported

package localidentity

import (
	"io/fs"
	"syscall"
)

func of(info fs.FileInfo) (Key, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return Key{}, ErrUnavailable
	}
	return fromStat(stat), nil
}
