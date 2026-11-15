// Package fsinfo makes two independent observations of the file system that
// holds an existing path: the capacity it offers the caller, and whether it
// is mounted read-only.
//
// Each operation either succeeds or returns an error; an error is never a
// zero result. Zero available bytes is a real observation of a full file
// system. Neither operation depends on the other, creates directories, walks
// to a parent, reserves space, tests permissions or identifies a volume.
package fsinfo

import (
	"errors"
	"io/fs"
	"math/bits"
	"strings"
)

// ErrUnsupported identifies an observation this platform does not implement.
var ErrUnsupported = errors.New("file system observation unsupported on this platform")

// ErrRange identifies a native quantity that is invalid or does not fit in
// 64 bits, so no capacity is reported rather than a wrapped one.
var ErrRange = errors.New("file system quantity out of range")

// Space is the capacity a file system offers the caller. AvailableBytes is
// what the caller may use, which can be less than the unused space on the
// device (reserved blocks, quotas). TotalBytes is the size the file system
// reports to the caller; on Windows it may be limited by a quota, so it is not
// necessarily the physical size of the disk.
type Space struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

// validatePath refuses a path no native call should see: an empty one, which
// some APIs treat as the current drive, or one with a NUL, which C strings
// would truncate.
func validatePath(op, path string) error {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return &fs.PathError{Op: op, Path: path, Err: fs.ErrInvalid}
	}
	return nil
}

// blockBytes multiplies a block count by a block size, refusing a product
// that does not fit in 64 bits.
func blockBytes(blocks, size uint64) (uint64, error) {
	hi, lo := bits.Mul64(blocks, size)
	if hi != 0 {
		return 0, ErrRange
	}
	return lo, nil
}

// spaceFromBlocks converts statfs(2) block counts, all in units of blockSize,
// to bytes. A zero block size is invalid: it would report a full file system.
func spaceFromBlocks(blockSize, blocks, available uint64) (Space, error) {
	if blockSize == 0 {
		return Space{}, ErrRange
	}
	total, err := blockBytes(blocks, blockSize)
	if err != nil {
		return Space{}, err
	}
	free, err := blockBytes(available, blockSize)
	if err != nil {
		return Space{}, err
	}
	return Space{TotalBytes: total, AvailableBytes: free}, nil
}
