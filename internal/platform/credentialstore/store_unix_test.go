//go:build darwin || linux

package credentialstore

import (
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	s := New(root)
	name := strings.Repeat("a", 64)
	secret := []byte("synthetic credential")
	if err := s.Save(name, secret); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(name)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("load: %v", err)
	}
	if err = s.Save(name, []byte("replacement")); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
	if err = s.Replace(name, []byte("wrong"), []byte("new")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unexpected replacement: %v", err)
	}
	if err = s.Replace(name, secret, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Load(name); err != nil || string(got) != "new" {
		t.Fatalf("replaced: %v", err)
	}
	if err = s.Delete(name, []byte("wrong")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unexpected deletion: %v", err)
	}
	if err = s.Delete(name, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(name, secret); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(root, name), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(name); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unsafe file mode: %v", err)
	}
	if err = os.Chmod(filepath.Join(root, name), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(root, name), filepath.Join(root, "second")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(name); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("hard link: %v", err)
	}
}

func TestProtectedFilesRecoverTemporaryAndRejectFIFO(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, ".0123456789abcdef0123456789abcdef.tmp")
	if err := os.WriteFile(stale, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("c", 64)
	s := New(root)
	if err := s.Save(name, []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary remains: %v", err)
	}
	fifo := strings.Repeat("d", 64)
	if err := unix.Mkfifo(filepath.Join(root, fifo), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(fifo); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("FIFO: %v", err)
	}
}
func TestProtectedFilesRejectSymlinks(t *testing.T) {
	base := t.TempDir()
	actual := filepath.Join(base, "real")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("b", 64)
	if err := New(link).Save(name, []byte("synthetic")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("directory symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(actual, "other"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(actual, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(actual).Load(name); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("file symlink: %v", err)
	}
	if err := os.Chmod(actual, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(actual).Load(name); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("directory mode: %v", err)
	}
}

func TestSaveUnderSymlinkedExistingAncestor(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "support")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	store := NewDurable(filepath.Join(link, "credentials", "records"))
	name := strings.Repeat("a", 64)
	if err := store.Save(name, []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Load(name); err != nil || string(value) != "synthetic" {
		t.Fatalf("load: %v", err)
	}
}
