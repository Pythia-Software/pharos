//go:build (darwin || linux) && !localidentity_unsupported

package localidentity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The text is the decimal "device:inode" that fmt printed from Stat_t, which
// stored capture states and preflight hashes depend on.
func TestKeyMatchesTheObservedStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, dir} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		key, err := Of(info)
		if err != nil || !key.Valid() {
			t.Fatalf("%s: %+v, %v", target, key, err)
		}
		text, err := key.DeviceInode()
		if want := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino); err != nil || text != want {
			t.Fatalf("%s: legacy text %q, %v; want %q", target, text, err, want)
		}
		again, err := Stat(target)
		if err != nil || again != key {
			t.Fatalf("%s: repeated observation %+v, %v; want %+v", target, again, err, key)
		}
	}
}

func TestReplacementWithIdenticalSizeAndTimeChangesKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	write := func(name string) {
		if err := os.WriteFile(name, []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write(path)
	before, err := Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the original open so its inode cannot be reused by the replacement.
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	write(path + ".new")
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4 || !info.ModTime().Equal(stamp) {
		t.Fatalf("fixture did not keep size and time: %d %v", info.Size(), info.ModTime())
	}
	after, err := Of(info)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("replacement kept the same identity")
	}
}

type fakeInfo struct{ fs.FileInfo }

func (fakeInfo) Sys() any { return nil }

func TestInfoWithoutStatIsUnavailable(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range []fs.FileInfo{fakeInfo{info}, typedNilStat{info}} {
		if key, err := Of(info); err != ErrUnavailable || key.Valid() {
			t.Fatalf("%T: %+v, %v", info, key, err)
		}
	}
	if errors.Is(ErrUnavailable, fs.ErrNotExist) {
		t.Fatal("an unavailable identity must not read as a missing file")
	}
}

type typedNilStat struct{ fs.FileInfo }

func (typedNilStat) Sys() any { return (*syscall.Stat_t)(nil) }
