package fsinfo

import (
	"errors"
	"io/fs"
	"math"
	"testing"
)

func TestBlockArithmetic(t *testing.T) {
	if ErrUnsupported == nil || ErrRange == nil || errors.Is(ErrUnsupported, ErrRange) {
		t.Fatal("sentinels must be distinct and non-nil")
	}
	for _, c := range []struct {
		size, blocks, available uint64
		want                    Space
		err                     error
	}{
		{4096, 10, 3, Space{40960, 12288}, nil},
		// A full file system is a real observation, not a failure.
		{4096, 10, 0, Space{40960, 0}, nil},
		// Full width: well past 2^32 blocks and 2^63 bytes.
		{1 << 20, 1 << 43, 1 << 42, Space{1 << 63, 1 << 62}, nil},
		{1, math.MaxUint64, math.MaxUint64, Space{math.MaxUint64, math.MaxUint64}, nil},
		{1 << 12, 1 << 52, 1, Space{}, ErrRange},
		{2, math.MaxUint64, 1, Space{}, ErrRange},
		{4096, 1, 1 << 53, Space{}, ErrRange},
		{0, 10, 3, Space{}, ErrRange},
	} {
		got, err := spaceFromBlocks(c.size, c.blocks, c.available)
		if got != c.want || !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
			t.Errorf("%d × (%d, %d): %+v, %v; want %+v, %v", c.size, c.blocks, c.available, got, err, c.want, c.err)
		}
	}
}

func TestInvalidPaths(t *testing.T) {
	for _, path := range []string{"", "dir\x00name"} {
		if _, err := Capacity(path); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Capacity(%q): %v", path, err)
		}
		if readOnly, err := ReadOnlyMount(path); readOnly || !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("ReadOnlyMount(%q): %v, %v", path, readOnly, err)
		}
	}
}
