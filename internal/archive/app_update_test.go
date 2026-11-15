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

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

func appUpdateTestBundle(t *testing.T) (app, executable, support, base string) {
	t.Helper()
	root := t.TempDir()
	app = filepath.Join(root, "Portable Library", "Pharos.app")
	executable = filepath.Join(app, "Contents", "MacOS", "pharos")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	app = resolved
	support = filepath.Join(root, "support")
	base = filepath.Join(support, "updates", fmt.Sprintf("%x", sha256.Sum256([]byte(app))))
	return
}

func TestAppUpdateLease(t *testing.T) {
	app, executable, support, base := appUpdateTestBundle(t)
	root := filepath.Dir(support)
	first, err := acquireAppUpdateLease(executable, support)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireAppUpdateLease(executable, support)
	if err != nil {
		t.Fatalf("concurrent readers: %v", err)
	}
	defer second.Close()
	second.Close()
	installer, err := os.OpenFile(base+".lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer installer.Close()
	if err := syscall.Flock(int(installer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("installation must wait while a CLI still holds its lease: %v", err)
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
	if lease, err := acquireAppUpdateLease(executable, support); lease != nil || !errors.Is(err, filelock.ErrContended) || !errors.Is(err, syscall.EWOULDBLOCK) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("a CLI must respect the installer's exclusive lease: lease=%v err=%v", lease, err)
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
		} else if data, err := os.ReadFile(base + ".pending"); err != nil || string(data) != state.marker {
			t.Fatalf("refused handoff changed: %q, %v", data, err)
		}
	}
	if err := os.WriteFile(base+".pending", []byte(`{"version":"0.7.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installer, err = os.Open(base + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer installer.Close()
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
	for _, executable := range []string{
		filepath.Join(root, "standalone"),
		filepath.Join(root, "source", "Contents", "MacOS", "pharos"),
		filepath.Join(app, "Contents", "MacOS", "PharosApp"),
	} {
		if lease, err := acquireAppUpdateLease(executable, support); err != nil || lease != nil {
			if lease != nil {
				lease.Close()
			}
			t.Fatalf("non-bundled CLI acquired an app lease: %v, %v", lease, err)
		}
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
	stale, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	if err := removeCompletedAppUpdate(old, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new handoff"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A cleaner holding the old inode makes another reader's cleanup benign.
	if err := removeCompletedAppUpdate(stale, path); err != nil {
		t.Fatalf("concurrent completed-marker cleanup: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	// Once the first cleaner releases its lock, the stale reader must still
	// leave the fresh GUI handoff alone.
	if err := removeCompletedAppUpdate(stale, path); err == nil {
		t.Fatal("stale reader accepted a replacement marker")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new handoff" {
		t.Fatalf("new marker was removed: %q, %v", data, err)
	}
}

func TestAppUpdateLockErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"wrapped contention", fmt.Errorf("adapter: %w", errors.Join(filelock.ErrContended, syscall.EWOULDBLOCK))},
		{"joined contention", errors.Join(errors.New("adapter context"), filelock.ErrContended, syscall.EAGAIN)},
		{"unsupported", fmt.Errorf("adapter: %w", filelock.ErrUnsupported)},
		{"permission", fmt.Errorf("adapter: %w", syscall.EACCES)},
		{"io", fmt.Errorf("adapter: %w", syscall.EIO)},
		{"filesystem unsupported", fmt.Errorf("adapter: %w", syscall.ENOTSUP)},
		{"bare errno", syscall.EWOULDBLOCK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contended := errors.Is(tc.cause, filelock.ErrContended)
			err := appUpdateLockError(tc.cause)
			if !errors.Is(err, tc.cause) {
				t.Fatalf("lost cause: %v", err)
			}
			if strings.Contains(err.Error(), "Pharos is updating") != contended {
				t.Fatalf("misleading lock failure: %v", err)
			}
			for _, mode := range []filelock.Mode{filelock.Shared, filelock.Exclusive} {
				name := "acquire"
				if mode == filelock.Exclusive {
					name = "cleanup"
				}
				t.Run(name, func(t *testing.T) {
					app, executable, support, base := appUpdateTestBundle(t)
					contents := filepath.Join(app, "Contents")
					if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(`<plist><dict><key>CFBundleVersion</key><string>0.7.3</string></dict></plist>`), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
						t.Fatal(err)
					}
					const marker = `{"version":"0.7.2","pid":123,"created":456}`
					if err := os.WriteFile(base+".pending", []byte(marker), 0o600); err != nil {
						t.Fatal(err)
					}
					var opened []*os.File
					injected := false
					override(t, &lockAppUpdate, func(file *os.File, gotMode filelock.Mode, wait filelock.WaitMode) error {
						opened = append(opened, file)
						t.Cleanup(func() { file.Close() })
						wantMode := filelock.Shared
						if len(opened) == 2 {
							wantMode = filelock.Exclusive
						}
						if gotMode != wantMode || wait != filelock.Try {
							t.Fatalf("update lock: mode=%v wait=%v", gotMode, wait)
						}
						if gotMode == mode {
							injected = true
							return tc.cause
						}
						return filelock.Lock(file, gotMode, wait)
					})
					lease, err := acquireAppUpdateLease(executable, support)
					if lease != nil {
						t.Cleanup(func() { lease.Close() })
					}
					allowed := mode == filelock.Exclusive && contended
					if !injected || (err == nil) != allowed || (lease != nil) != allowed {
						t.Fatalf("lock failure: injected=%v lease=%v err=%v", injected, lease, err)
					}
					if !allowed && !errors.Is(err, tc.cause) {
						t.Fatalf("lock failure lost its cause: %v", err)
					}
					if !allowed && strings.Contains(err.Error(), "Pharos is updating") != (mode == filelock.Shared && contended) {
						t.Fatalf("misleading lock failure: %v", err)
					}
					if data, err := os.ReadFile(base + ".pending"); err != nil || string(data) != marker {
						t.Fatalf("lock failure changed pending state: %q, %v", data, err)
					}
					for _, file := range opened {
						if file != lease {
							if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
								t.Fatalf("lock failure leaked descriptor %s: %v", file.Name(), err)
							}
						}
					}
					installer, err := os.Open(base + ".lock")
					if err != nil {
						t.Fatal(err)
					}
					defer installer.Close()
					// Raw flock remains the native installer's side of the protocol.
					if allowed {
						if err := syscall.Flock(int(installer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
							t.Fatalf("cleanup contention lost the CLI lease: %v", err)
						}
						if err := lease.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if err := syscall.Flock(int(installer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
						t.Fatalf("CLI lease was not released: %v", err)
					}
				})
			}
		})
	}
}
