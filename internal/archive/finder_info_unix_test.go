//go:build darwin || linux

package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// setFinderInfo writes a Finder info attribute, which a filesystem without
// that attribute namespace (Linux) refuses.
func setFinderInfo(path string, info []byte) error {
	return unix.Setxattr(path, "com.apple.FinderInfo", info, 0)
}

func TestFinderInfoNativeReadsTheAttribute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := finderInfoNative(path); err == nil || info != nil {
		t.Fatalf("a file without Finder info read %v, %v", info, err)
	}
	if _, err := finderInfoNative(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing file read Finder info")
	}
	want := make([]byte, 32)
	want[8] = 0x80
	if err := setFinderInfo(path, want); err != nil {
		t.Skipf("this filesystem refuses Finder info: %v", err)
	}
	if info, err := finderInfoNative(path); err != nil || !bytes.Equal(info, want) || !finderInfoAlias(info) {
		t.Fatalf("Finder info %v, %v", info, err)
	}
}
