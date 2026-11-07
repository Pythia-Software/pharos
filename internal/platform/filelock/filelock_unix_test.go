//go:build darwin || linux

package filelock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const initialJSON = `{"step":1}` + "\n"
const updatedJSON = `{"step":2}` + "\n"

// The child opens the parent's synthetic file independently. Commands and
// acknowledgements use pipes, including an acknowledgement before blocking.
func TestFilelockHelperProcess(t *testing.T) {
	path := os.Getenv("PHAROS_FILELOCK_HELPER")
	if path == "" {
		t.Skip("subprocess helper")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reply := func(value string) { fmt.Println(value) }
	result := func(err error) {
		switch {
		case err == nil:
			reply("ok")
		case errors.Is(err, ErrContended):
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				t.Fatalf("contention lost its errno: %v", err)
			}
			reply("contended")
		default:
			t.Fatal(err)
		}
	}
	reply("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "shared":
			result(Lock(file, Shared, Try))
		case "exclusive":
			result(Lock(file, Exclusive, Try))
		case "wait":
			if err := Lock(file, Exclusive, Try); !errors.Is(err, ErrContended) {
				t.Fatalf("expected contention before waiting: %v", err)
			}
			reply("waiting")
			result(Lock(file, Exclusive, Wait))
		case "unlock":
			result(Unlock(file))
		case "close":
			result(file.Close())
		case "read":
			data, err := io.ReadAll(io.NewSectionReader(file, 0, 1024))
			if err != nil || !json.Valid(data) {
				t.Fatalf("read JSON: %q, %v", data, err)
			}
			reply(string(bytes.TrimSpace(data)))
		case "write":
			if err := file.Truncate(0); err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte(updatedJSON), 0); err != nil {
				t.Fatal(err)
			}
			reply("ok")
		case "exit":
			return
		default:
			t.Fatalf("unknown command: %s", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type lockChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	lines  chan string
	stderr bytes.Buffer
	exited chan struct{}
	err    error // read only after exited closes
}

func startLockChild(t *testing.T, path string) *lockChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	child := &lockChild{lines: make(chan string, 16), exited: make(chan struct{})}
	child.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFilelockHelperProcess$")
	child.cmd.Env = append(os.Environ(), "PHAROS_FILELOCK_HELPER="+path)
	child.cmd.Stderr = &child.stderr
	var err error
	child.input, err = child.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := child.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := child.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			child.lines <- scanner.Text()
		}
		close(child.lines)
		child.err = child.cmd.Wait()
		close(child.exited)
	}()
	t.Cleanup(func() {
		child.input.Close()
		cancel()
		<-child.exited
	})
	child.expect(t, "ready")
	return child
}

func (c *lockChild) send(t *testing.T, command string) {
	t.Helper()
	if _, err := fmt.Fprintln(c.input, command); err != nil {
		t.Fatal(err)
	}
}

func (c *lockChild) expect(t *testing.T, want string) {
	t.Helper()
	select {
	case got, ok := <-c.lines:
		if !ok {
			<-c.exited
			t.Fatalf("helper exited: %v: %s", c.err, c.stderr.String())
		}
		if got != want {
			t.Fatalf("helper replied %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for helper reply %q", want)
	}
}

func (c *lockChild) command(t *testing.T, command, want string) {
	t.Helper()
	c.send(t, command)
	c.expect(t, want)
}

func lockFixture(t *testing.T) (*os.File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lock.json")
	if err := os.WriteFile(path, []byte(initialJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return openLockFile(t, path), path
}

func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func mustLock(t *testing.T, file *os.File, mode Mode, wait WaitMode) {
	t.Helper()
	if err := Lock(file, mode, wait); err != nil {
		t.Fatal(err)
	}
}

func TestSharedLocksCoexist(t *testing.T) {
	file, path := lockFixture(t)
	mustLock(t, file, Shared, Wait)
	child := startLockChild(t, path)
	child.command(t, "shared", "ok")
	other := openLockFile(t, path)
	if err := Lock(other, Exclusive, Try); !errors.Is(err, ErrContended) || !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("exclusive contender: %v", err)
	}
	child.command(t, "unlock", "ok")
	if err := Lock(other, Exclusive, Try); !errors.Is(err, ErrContended) {
		t.Fatalf("remaining shared owner lost its lock: %v", err)
	}
	if err := Unlock(file); err != nil {
		t.Fatal(err)
	}
	mustLock(t, other, Exclusive, Try)
	child.command(t, "read", `{"step":1}`) // Unlock did not close it.
}

func TestExclusiveLockContentionAndJSON(t *testing.T) {
	file, path := lockFixture(t)
	child := startLockChild(t, path)
	child.command(t, "exclusive", "ok")
	for _, mode := range []Mode{Shared, Exclusive} {
		if err := Lock(file, mode, Try); !errors.Is(err, ErrContended) || !errors.Is(err, syscall.EAGAIN) {
			t.Fatalf("contention (%v): %v", mode, err)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ command, contents string }{{"read", initialJSON}, {"write", updatedJSON}} {
		if tc.command == "write" {
			child.command(t, "write", "ok")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != tc.contents || !json.Valid(data) {
			t.Fatalf("JSON during ownership: %q, %v", data, err)
		}
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("JSON update replaced the file: %v", err)
	}
	if err := Lock(file, Shared, Try); !errors.Is(err, ErrContended) {
		t.Fatalf("updating JSON released the lock: %v", err)
	}
	child.command(t, "close", "ok")
	mustLock(t, file, Exclusive, Try)
}

func TestWaitAcquiresAfterRelease(t *testing.T) {
	for _, release := range []string{"unlock", "close"} {
		t.Run(release, func(t *testing.T) {
			file, path := lockFixture(t)
			// Interoperate with the flock protocol used by existing consumers.
			if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			child := startLockChild(t, path)
			child.command(t, "shared", "contended")
			child.command(t, "exclusive", "contended")
			child.send(t, "wait")
			child.expect(t, "waiting")
			var err error
			if release == "unlock" {
				err = Unlock(file)
			} else {
				err = file.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			child.expect(t, "ok")
			other := openLockFile(t, path)
			if err := syscall.Flock(int(other.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				t.Fatalf("waiting child did not acquire exclusive flock: %v", err)
			}
			child.command(t, "unlock", "ok")
			mustLock(t, other, Exclusive, Try)
		})
	}
}

func TestKilledOwnerReleasesLock(t *testing.T) {
	file, path := lockFixture(t)
	child := startLockChild(t, path)
	child.command(t, "exclusive", "ok")
	if err := Lock(file, Shared, Try); !errors.Is(err, ErrContended) {
		t.Fatalf("owner was not holding lock: %v", err)
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.exited:
		if child.err == nil {
			t.Fatal("killed helper exited successfully")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("killed helper did not exit")
	}
	mustLock(t, file, Exclusive, Try)
}

func TestOrdinaryExecDoesNotInheritLock(t *testing.T) {
	file, path := lockFixture(t)
	mustLock(t, file, Exclusive, Wait)
	child := startLockChild(t, path) // ordinary exec; no ExtraFiles
	child.command(t, "read", `{"step":1}`)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	mustLock(t, openLockFile(t, path), Exclusive, Try)
	// The child is still alive: it did not keep the parent's lock alive.
	child.command(t, "read", `{"step":1}`)
}

func TestFinalDescriptorCloseReleasesLock(t *testing.T) {
	file, path := lockFixture(t)
	mustLock(t, file, Exclusive, Wait)
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := os.NewFile(uintptr(fd), "duplicate")
	t.Cleanup(func() { duplicate.Close() })
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	other := openLockFile(t, path)
	if err := Lock(other, Shared, Try); !errors.Is(err, ErrContended) {
		t.Fatalf("closing one descriptor released the lock: %v", err)
	}
	if err := duplicate.Close(); err != nil {
		t.Fatal(err)
	}
	mustLock(t, other, Exclusive, Try)
}

func TestFilesystemErrorsAreNotContention(t *testing.T) {
	// A nonnegative but impossible descriptor reaches flock and produces EBADF,
	// without racing the runtime by closing an os.File's descriptor behind it.
	file := os.NewFile(1<<30, "invalid-descriptor")
	if file == nil {
		t.Fatal("could not construct invalid descriptor fixture")
	}
	defer file.Close()
	for _, wait := range []WaitMode{Try, Wait} {
		err := Lock(file, Exclusive, wait)
		if !errors.Is(err, syscall.EBADF) || errors.Is(err, ErrContended) || errors.Is(err, ErrUnsupported) {
			t.Fatalf("lock must preserve EBADF (%v): %v", wait, err)
		}
	}
	if err := Unlock(file); !errors.Is(err, syscall.EBADF) || errors.Is(err, ErrContended) {
		t.Fatalf("unlock must preserve EBADF: %v", err)
	}
}
