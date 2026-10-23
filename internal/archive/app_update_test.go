package archive

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAppUpdateLease(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Portable Library", "Pharos.app")
	executable := filepath.Join(app, "Contents", "MacOS", "pharos")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	app, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	support := filepath.Join(root, "support")
	base := filepath.Join(support, "updates", fmt.Sprintf("%x", sha256.Sum256([]byte(app))))
	first, err := acquireAppUpdateLease(executable, support)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireAppUpdateLease(executable, support)
	if err != nil {
		t.Fatalf("concurrent readers: %v", err)
	}
	second.Close()
	installer, err := os.OpenFile(base+".lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer installer.Close()
	if err := syscall.Flock(int(installer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("installation allowed while a CLI still holds its lease")
	}
	if err := os.WriteFile(base+".pending", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if lease, err := acquireAppUpdateLease(executable, support); err == nil {
		lease.Close()
		t.Fatal("a reconnect passed the pending marker")
	}
	first.Close()
	if err := syscall.Flock(int(installer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(base + ".pending"); err != nil {
		t.Fatal(err)
	}
	if lease, err := acquireAppUpdateLease(executable, support); err == nil {
		lease.Close()
		t.Fatal("a CLI passed the installer's exclusive lease")
	}
	installer.Close()
	// Install-on-quit need not reopen the GUI. The changed bundle version
	// allows MCP/CLI reconnects, but never overrides an exclusive installer.
	plist := filepath.Join(app, "Contents", "Info.plist")
	if err := os.WriteFile(plist, []byte(`<plist><dict><key>CFBundleVersion</key><string>0.7.3</string></dict></plist>`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		marker  string
		allowed bool
	}{
		{`{"version":"0.7.2"}`, true},
		{`{"version":"0.7.3"}`, false},
		{`{}`, false},
		{`broken`, false},
	} {
		if err := os.WriteFile(base+".pending", []byte(state.marker), 0o600); err != nil {
			t.Fatal(err)
		}
		lease, err := acquireAppUpdateLease(executable, support)
		if (err == nil) != state.allowed {
			t.Fatalf("marker %s: %v", state.marker, err)
		}
		if lease != nil {
			lease.Close()
		}
		if state.allowed {
			if _, err := os.Stat(base + ".pending"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completed handoff would block a later GUI launch: %v", err)
			}
		}
	}
	if err := os.WriteFile(base+".pending", []byte(`{"version":"0.7.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installer, err = os.Open(base + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(installer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if lease, err := acquireAppUpdateLease(executable, support); err == nil {
		lease.Close()
		t.Fatal("a changed version bypassed an exclusive installer")
	}
	installer.Close()
	if err := os.Remove(base + ".pending"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base+".lock", 0o400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "pharos")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireAppUpdateLease(link, support)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if lease, err := acquireAppUpdateLease(filepath.Join(root, "standalone"), support); err != nil || lease != nil {
		t.Fatalf("standalone CLI acquired an app lease: %v, %v", lease, err)
	}
}

func TestCompletedAppUpdateMarkerReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.pending")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := removeCompletedAppUpdate(old, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new handoff"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second reader still holding the old inode must not remove the new
	// GUI marker, even though both observed the same completed old version.
	if err := removeCompletedAppUpdate(old, path); err == nil {
		t.Fatal("stale reader accepted a replacement marker")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new handoff" {
		t.Fatalf("new marker was removed: %q, %v", data, err)
	}
}

func TestAppUpdateLockErrors(t *testing.T) {
	for _, cause := range []error{syscall.EWOULDBLOCK, syscall.EACCES, syscall.ENOTSUP} {
		err := appUpdateLockError(cause)
		if !errors.Is(err, cause) {
			t.Fatalf("lost cause: %v", err)
		}
		if strings.Contains(err.Error(), "Pharos is updating") != errors.Is(cause, syscall.EWOULDBLOCK) {
			t.Fatalf("misleading lock failure: %v", err)
		}
	}
}
