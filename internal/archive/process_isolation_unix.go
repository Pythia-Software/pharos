//go:build darwin || linux

package archive

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// processKill signals a process or, given a negative pid, a process group.
// Tests replace it to observe signals without a real target.
var processKill = syscall.Kill

// configureAskProcess puts an Ask CLI in its own process group so canceling
// it also kills children holding its pipes. Descendants that leave the group
// escape this; it is not a process-tree guarantee.
func configureAskProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := processKill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// configureLoginShellProcess starts the shell in a new session without a
// terminal, so it cannot stop on terminal I/O, and kills its process group on
// cancellation. exec calls Cancel only after a successful Start.
func configureLoginShellProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return processKill(-cmd.Process.Pid, syscall.SIGKILL) }
	return nil
}
