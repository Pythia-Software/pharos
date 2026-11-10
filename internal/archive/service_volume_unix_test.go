//go:build darwin || linux

package archive

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type directoryVolumeFileInfo struct {
	os.FileInfo
	metadata any
}

func (info directoryVolumeFileInfo) Sys() any { return info.metadata }

func TestDirectoryVolumeIdentityFromInfo(t *testing.T) {
	for _, tc := range []struct {
		name string
		info os.FileInfo
	}{
		{"nil file info", nil},
		{"nil system metadata", directoryVolumeFileInfo{}},
		{"unsupported system metadata", directoryVolumeFileInfo{metadata: struct{}{}}},
		{"non-pointer stat", directoryVolumeFileInfo{metadata: syscall.Stat_t{}}},
		{"typed-nil stat", directoryVolumeFileInfo{metadata: (*syscall.Stat_t)(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := directoryVolumeIdentityFromInfo(tc.info); !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("identity error = %v; want unsupported metadata", err)
			}
		})
	}
	id, err := directoryVolumeIdentityFromInfo(directoryVolumeFileInfo{metadata: &syscall.Stat_t{Dev: 0}})
	if err != nil || id != (directoryVolumeID{device: 0}) {
		t.Fatalf("zero device identity = %v, %v; want valid zero", id, err)
	}
}

func TestReadDirectoryVolumeIdentity(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	var start directoryVolumeID
	for i, path := range []string{dir, file, link} {
		id, err := readDirectoryVolumeIdentity(path)
		if err != nil {
			t.Fatalf("identity of %s: %v", path, err)
		}
		if i == 0 {
			start = id
		} else if id != start {
			t.Fatalf("identity of %s = %v; want same device as directory: %v", path, id, start)
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, link} {
		_, err := readDirectoryVolumeIdentity(path)
		var statErr *os.PathError
		if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &statErr) || statErr.Op != "stat" || statErr.Path != path {
			t.Fatalf("identity of missing path %s: %v; want original stat error", path, err)
		}
	}
}
