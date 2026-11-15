//go:build linux

package archive

import (
	"os"
	"syscall"
	"testing"
)

func syntheticDevice(device int32) os.FileInfo {
	return directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: uint64(device)}}
}

// Linux's dev_t is a uint64 whose high bits carry major numbers above 4095;
// every observation and spelling keeps them.
func TestLinuxDeviceObservationsKeepFullWidth(t *testing.T) {
	const wide = 1<<32 | 7
	info := directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: wide, Ino: 1<<63 | 5}}
	device, err := placementDevice(info)
	if err != nil || device != wide {
		t.Fatalf("placement device = %d, %v", device, err)
	}
	if label, err := legacyDeviceLabel(info); err != nil || label != "device:4294967303" {
		t.Fatalf("legacy label = %q, %v", label, err)
	}
	if key, err := hardlinkKey(info); err != nil || key != [2]uint64{wide, 1<<63 | 5} {
		t.Fatalf("hardlink key = %v, %v", key, err)
	}
	if inode, err := libraryDirectoryInode(info); err != nil || inode != 1<<63|5 {
		t.Fatalf("directory inode = %d, %v", inode, err)
	}
	dir := t.TempDir()
	stubDevices(t).set(dir, info)
	if value, ok := lookupVolumeIdentity(dir); ok || value != "device:4294967303" {
		t.Fatalf("lookup fallback = %q, %v", value, ok)
	}
	// Without diskutil, volumeIdentity answers from its legacy fallback.
	if value := volumeIdentity(dir); value != "device:4294967303" {
		t.Fatalf("volume identity = %q", value)
	}
}
