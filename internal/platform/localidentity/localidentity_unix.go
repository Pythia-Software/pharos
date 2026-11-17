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

func acquisition(info fs.FileInfo) (AcquisitionStamp, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Ino == 0 {
		return AcquisitionStamp{}, ErrUnavailable
	}
	sec, nsec := changeTime(stat)
	if nsec < 0 || nsec >= 1e9 {
		return AcquisitionStamp{}, ErrUnavailable
	}
	key := fromStat(stat)
	return AcquisitionStamp{Device: key.device, Inode: key.inode, ChangeSeconds: sec, ChangeNanos: nsec, Size: info.Size(), MTimeNS: info.ModTime().UnixNano()}, nil
}
