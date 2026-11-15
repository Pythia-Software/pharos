//go:build !darwin && !linux

package archive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Without a native observation every helper fails recognizably; none
// reports a zero identity, and no v1 writable library can be created or
// validated.
func TestFileObservationsUnsupported(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, errFileObservationUnavailable) || !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: %v; want unsupported observation", name, err)
		}
	}
	_, err = libraryDirectoryInode(info)
	unsupported("directory inode", err)
	_, err = placementDevice(info)
	unsupported("placement device", err)
	_, err = legacyDeviceLabel(info)
	unsupported("legacy label", err)
	_, err = hardlinkKey(info)
	unsupported("hardlink key", err)
	_, err = deviceOf(dir)
	unsupported("deviceOf", err)
	_, err = observeLibraryDirectory(dir)
	unsupported("library directory", err)
	if !errors.Is(err, errLibraryDirectoryUnobserved) || libraryDescriptorMissing(err) {
		t.Errorf("library directory: %v", err)
	}
	if sameDevice(dir, dir) {
		t.Error("an unsupported observation asserted same-device placement")
	}
	if got := volumeIdentity(filepath.Join(dir, "x")); got != "unavailable" {
		t.Errorf("volume identity = %q", got)
	}
}
