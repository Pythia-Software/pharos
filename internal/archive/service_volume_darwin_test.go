//go:build darwin

package archive

import (
	"syscall"
	"testing"
)

func TestDirectoryVolumeIdentityComparesNegativeDarwinDevices(t *testing.T) {
	read := func(device int32) directoryVolumeID {
		t.Helper()
		id, err := directoryVolumeIdentityFromInfo(directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: device}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	negative := read(-1)
	if negative.device != ^uint64(0) || negative != read(-1) || negative == read(-2) || negative == read(1) {
		t.Fatalf("negative device identity is inconsistent: %v", negative)
	}
}
