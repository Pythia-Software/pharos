//go:build linux

package archive

import (
	"syscall"
	"testing"
)

func TestDirectoryVolumeIdentityPreservesLinuxDeviceWidth(t *testing.T) {
	low, err := directoryVolumeIdentityFromInfo(directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: 7}})
	if err != nil {
		t.Fatal(err)
	}
	high, err := directoryVolumeIdentityFromInfo(directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: 1<<32 | 7}})
	if err != nil {
		t.Fatal(err)
	}
	if low == high || low.device != 7 || high.device != 1<<32|7 {
		t.Fatalf("device identities lost high bits: low=%v high=%v", low, high)
	}
}
