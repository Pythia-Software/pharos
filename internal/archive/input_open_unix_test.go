//go:build darwin || linux

package archive

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// withoutBlocking fails the test if call has not returned within a generous
// bound; a blocking open on a FIFO would never return.
func withoutBlocking(t *testing.T, call func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); call() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked on input")
	}
}

func TestInputOpensWithoutBlocking(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "data"), "setup")
	writeSourceFile(t, filepath.Join(outside, "secret"), "outside")
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(dir, "locked"), "locked")
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for kind, open := range map[string]func(string) (*os.File, error){
		"path": func(name string) (*os.File, error) { return openInputNonblockingNative(filepath.Join(dir, name)) },
		"root": func(name string) (*os.File, error) { return openInputNonblockingInNative(root, name) },
	} {
		t.Run(kind, func(t *testing.T) {
			// The caller owns the returned file: it stays open until the
			// caller closes it, once. Symlinks inside are followed as before.
			for _, name := range []string{"data", "link"} {
				file, err := open(name)
				if err != nil {
					t.Fatal(err)
				}
				if data, err := io.ReadAll(file); err != nil || string(data) != "setup" {
					t.Fatalf("%s read %q %v", name, data, err)
				}
				if err := file.Close(); err != nil || !errors.Is(file.Close(), os.ErrClosed) {
					t.Fatalf("%s close: %v", name, err)
				}
			}
			// A FIFO without a writer opens at once, and its handle is
			// not regular, which callers check before reading.
			withoutBlocking(t, func() {
				file, err := open("pipe")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				if info, err := file.Stat(); err != nil || info.Mode().IsRegular() {
					t.Errorf("FIFO handle: %v %v", info, err)
				}
			})
			if _, err := open("missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing input: %v", err)
			}
			if _, err := open("locked"); os.Geteuid() != 0 && !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("unreadable input: %v", err)
			}
		})
	}
	// Only the rooted variant stays inside its folder.
	for _, name := range []string{"escape", "../" + filepath.Base(outside) + "/secret"} {
		if file, err := openInputNonblockingInNative(root, name); err == nil {
			file.Close()
			t.Fatalf("rooted open of %s left the folder", name)
		}
	}
}

func TestCloudInputsRefuseNonregularHandles(t *testing.T) {
	// A setup file replaced by a FIFO after its size check is refused by
	// the opened handle, without blocking.
	setup := t.TempDir()
	draft := filepath.Join(setup, "destination-draft.json")
	writeSourceFile(t, draft, `{}`)
	override(t, &openInputNonblockingIn, func(root *os.Root, name string) (*os.File, error) {
		if err := os.Remove(draft); err != nil {
			return nil, err
		}
		if err := syscall.Mkfifo(draft, 0o600); err != nil {
			return nil, err
		}
		return openInputNonblockingInNative(root, name)
	})
	withoutBlocking(t, func() {
		if raw, err := readCloudSetup(setup, "destination-draft.json"); err == nil {
			t.Errorf("FIFO setup file read %q", raw)
		}
	})

	c, config, _, run, archive := cloudFixture(t, false)
	fifo := filepath.Join(t.TempDir(), "archive")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	withoutBlocking(t, func() {
		if err := c.finishCloudVerification(context.Background(), config, &run, fifo, nil); err == nil || run.Verified {
			t.Errorf("FIFO archive verified: %v", err)
		}
		folder := filepath.Join(t.TempDir(), "recovered")
		if err := runCloudRestoreCLI([]string{fifo, folder}); err == nil {
			t.Error("FIFO archive restored")
		}
		if _, err := os.Lstat(folder); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a refused restore created its output folder: %v", err)
		}
	})
	if os.Geteuid() == 0 {
		return
	}
	locked := filepath.Join(t.TempDir(), "archive")
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locked, data, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.finishCloudVerification(context.Background(), config, &run, locked, nil); !errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) || run.Verified {
		t.Fatalf("unreadable archive: %v", err)
	}
}
