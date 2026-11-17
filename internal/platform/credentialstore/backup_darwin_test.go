//go:build darwin

package credentialstore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinCredentialBackupExclusion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	name := strings.Repeat("a", 64)
	if err := New(root).Save(name, []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, name)} {
		b := make([]byte, 1024)
		n, err := unix.Getxattr(path, "com.apple.metadata:com_apple_backup_excludeItem", b)
		if err != nil || !bytes.Equal(b[:n], backupExclusion) {
			t.Fatalf("backup exclusion %s: %v", path, err)
		}
	}
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}
