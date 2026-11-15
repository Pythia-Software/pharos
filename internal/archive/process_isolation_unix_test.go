//go:build darwin || linux

package archive

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Each configuration's Cancel signals the child's process group, and Ask and
// the login shell report an already-reaped group differently. No child runs:
// the signal seam stands in for kill(2).
func TestProcessIsolationSignals(t *testing.T) {
	type signal struct {
		pid int
		sig syscall.Signal
	}
	var sent []signal
	var result error
	override(t, &processKill, func(pid int, sig syscall.Signal) error {
		sent = append(sent, signal{pid, sig})
		return result
	})
	configured := func(configure func(*exec.Cmd) error) *exec.Cmd {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "/bin/true")
		if err := configure(cmd); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	ask := configured(configureAskProcess)
	if attr := ask.SysProcAttr; attr == nil || !attr.Setpgid || attr.Setsid {
		t.Fatalf("Ask process attributes = %+v", attr)
	}
	if err := ask.Cancel(); err != nil || len(sent) != 0 {
		t.Fatalf("Ask Cancel without a process = %v, signals %v", err, sent)
	}
	shell := configured(configureLoginShellProcess)
	if attr := shell.SysProcAttr; attr == nil || !attr.Setsid || attr.Setpgid {
		t.Fatalf("login shell process attributes = %+v", attr)
	}

	ask.Process = &os.Process{Pid: 4242}
	shell.Process = &os.Process{Pid: 4343}
	for _, step := range []struct {
		name       string
		cmd        *exec.Cmd
		result     error
		want       error
		wantSignal signal
	}{
		{"Ask signaled", ask, nil, nil, signal{-4242, syscall.SIGKILL}},
		{"Ask reaped group", ask, syscall.ESRCH, os.ErrProcessDone, signal{-4242, syscall.SIGKILL}},
		{"Ask refused", ask, syscall.EPERM, syscall.EPERM, signal{-4242, syscall.SIGKILL}},
		{"shell signaled", shell, nil, nil, signal{-4343, syscall.SIGKILL}},
		{"shell reaped group", shell, syscall.ESRCH, syscall.ESRCH, signal{-4343, syscall.SIGKILL}},
		{"shell refused", shell, syscall.EPERM, syscall.EPERM, signal{-4343, syscall.SIGKILL}},
	} {
		sent, result = nil, step.result
		// Compare identities: Ask maps ESRCH to done; the shell keeps it raw.
		err := step.cmd.Cancel()
		if err != step.want || len(sent) != 1 || sent[0] != step.wantSignal {
			t.Fatalf("%s: Cancel = %v, signals %v; want %v, %v", step.name, err, sent, step.want, step.wantSignal)
		}
	}
}

// askCommand keeps its WaitDelay and environment around the configured process.
func TestAskCommandConfiguration(t *testing.T) {
	cmd, err := askCommand(t.Context(), "/bin/true", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cmd.WaitDelay != 2*time.Second || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("WaitDelay %s, attributes %+v", cmd.WaitDelay, cmd.SysProcAttr)
	}
}
