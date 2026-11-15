package archive

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// identityFileInfo is a FileInfo whose Sys is whatever a test gives it.
type identityFileInfo struct {
	name string
	sys  any
}

func (info identityFileInfo) Name() string       { return info.name }
func (info identityFileInfo) Size() int64        { return 8 }
func (info identityFileInfo) Mode() fs.FileMode  { return 0o600 }
func (info identityFileInfo) ModTime() time.Time { return time.Unix(1_700_000_000, 0) }
func (info identityFileInfo) IsDir() bool        { return false }
func (info identityFileInfo) Sys() any           { return info.sys }

// hideIdentities makes the objects for which hidden reports true
// unidentifiable, as a destination without inode numbers would be.
func hideIdentities(t *testing.T, hidden func(fileObservation) bool) {
	t.Helper()
	observe := observeFile
	override(t, &observeFile, func(info fs.FileInfo) (fileObservation, error) {
		observed, err := observe(info)
		if err == nil && hidden(observed) {
			return fileObservation{}, errFileIdentityUnavailable
		}
		return observed, err
	})
}

// identityOf is the identity of the object at path, or skips the test where
// the platform reports none.
func identityOf(t *testing.T, path string) fileKey {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := observeFile(info)
	if errors.Is(err, errFileIdentityUnsupported) {
		t.Skip("this platform reports no file identity")
	} else if err != nil {
		t.Fatal(err)
	}
	return observed.key
}

// No FileInfo, or one from another file system implementation, has no
// identity: never a panic, and never a zero key that groups or keeps files.
func TestFileIdentityOfMissingOrForeignInfoIsUnavailable(t *testing.T) {
	for name, info := range map[string]fs.FileInfo{"nil": nil, "no sys": identityFileInfo{name: "a"}, "foreign sys": identityFileInfo{name: "a", sys: struct{ Dev, Ino uint64 }{1, 2}}} {
		_, err := observeFile(info)
		native := runtime.GOOS == "darwin" || runtime.GOOS == "linux"
		if !errors.Is(err, errFileIdentityUnavailable) && (native || info == nil || !errors.Is(err, errFileIdentityUnsupported)) {
			t.Errorf("%s: %v", name, err)
		}
		if key, ok := keyOf(info); ok || key != (fileKey{}) {
			t.Errorf("%s: keyOf = %v, %v", name, key, ok)
		}
		if info != nil {
			if key, ok := linkKey(info); ok || key != (backupLinkKey{}) {
				t.Errorf("%s: linkKey = %v, %v", name, key, ok)
			}
		}
		if identity, err := rootIdentity(info); identity != "" || err == nil {
			t.Errorf("%s: rootIdentity = %q, %v", name, identity, err)
		}
	}
}

func TestFileIdentityReadsTheObservedObject(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeSourceFile(t, a, "one")
	writeSourceFile(t, b, "two")
	keyA := identityOf(t, a)
	if keyB := identityOf(t, b); keyA == keyB {
		t.Fatal("two files share an identity", keyA)
	}
	linked := filepath.Join(dir, "linked")
	if err := os.Link(a, linked); err != nil {
		t.Skip("no hard links here:", err)
	}
	info, err := os.Lstat(linked)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := observeFile(info)
	if err != nil || observed.key != keyA || observed.links != 2 {
		t.Fatalf("hard link: %+v %v", observed, err)
	}
	// Renaming another file to a's name does not change what an earlier
	// FileInfo of a describes.
	before, err := os.Lstat(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(b, a); err != nil {
		t.Fatal(err)
	}
	if again, err := observeFile(before); err != nil || again.key != keyA {
		t.Fatalf("after the name moved: %+v %v", again, err)
	}
}
