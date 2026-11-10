//go:build darwin

package archive

import (
	"os"
	"syscall"
	"testing"
)

func syntheticDevice(device int32) os.FileInfo {
	return directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: device}}
}

// Darwin's dev_t is a signed int32. Placement and cache keys have always
// widened it with sign extension, and lookupVolumeIdentity spells that uint64;
// volumeIdentity's legacy fallback spells the native signed value. Pinned
// volume_id values depend on both spellings staying as they are.
func TestDarwinDeviceObservationsKeepNativeFormatting(t *testing.T) {
	info := directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: -1, Ino: 1<<63 | 5}}
	device, err := placementDevice(info)
	if err != nil || device != ^uint64(0) {
		t.Fatalf("placement device = %d, %v; want sign-extended -1", device, err)
	}
	if label, err := legacyDeviceLabel(info); err != nil || label != "device:-1" {
		t.Fatalf("legacy label = %q, %v", label, err)
	}
	if key, err := hardlinkKey(info); err != nil || key != [2]uint64{^uint64(0), 1<<63 | 5} {
		t.Fatalf("hardlink key = %v, %v", key, err)
	}
	if inode, err := libraryDirectoryInode(info); err != nil || inode != 1<<63|5 {
		t.Fatalf("directory inode = %d, %v", inode, err)
	}
	dir := t.TempDir()
	stubDevices(t).set(dir, info)
	if value, ok := lookupVolumeIdentity(dir); ok || value != "device:18446744073709551615" {
		t.Fatalf("lookup fallback = %q, %v; want the unsigned spelling", value, ok)
	}
}
