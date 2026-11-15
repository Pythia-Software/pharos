//go:build darwin || linux

package archive

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// labelingKill is kill(2), overridable by tests to observe destinations
// without a real target.
var labelingKill = syscall.Kill

// configureLabelingProcess makes the CLI lead a new process group (Setpgid),
// which its children join unless they leave it themselves.
func configureLabelingProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// labelingGroupTarget is kill(2)'s destination for the group pid leads. A
// pid of 0 or 1 would address the caller's own group or every process, so
// it is refused rather than signaled.
func labelingGroupTarget(pid int) (int, error) {
	if pid <= 1 {
		return 0, fmt.Errorf("refusing to address process group %d", pid)
	}
	return -pid, nil
}

func observeLabelingGroup(pid int) (labelingGroupState, error) {
	target, err := labelingGroupTarget(pid)
	if err != nil {
		return labelingGroupUnknown, err
	}
	switch err := labelingKill(target, 0); {
	case err == nil:
		return labelingGroupPresent, nil
	case errors.Is(err, syscall.ESRCH):
		return labelingGroupAbsent, err
	default:
		// EPERM and anything else: the group may well run.
		return labelingGroupUnknown, err
	}
}

func labelingSignal(action labelingAction) (syscall.Signal, error) {
	switch action {
	case labelingTerminate:
		return syscall.SIGTERM, nil
	case labelingForceKill:
		return syscall.SIGKILL, nil
	}
	return 0, fmt.Errorf("unknown labeling action %d", action)
}

func signalLabelingGroup(pid int, action labelingAction) error {
	target, err := labelingGroupTarget(pid)
	if err != nil {
		return err
	}
	signal, err := labelingSignal(action)
	if err != nil {
		return err
	}
	if err := labelingKill(target, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("%w: %w", errLabelingGroupAbsent, err)
		}
		return err
	}
	return nil
}

// labelingActionLabel is how a run's cleanup names a signal it sent
// ("terminated", "killed"): Go's description of the native signal, as saved
// attempts have always recorded it.
func labelingActionLabel(action labelingAction) string {
	if signal, err := labelingSignal(action); err == nil {
		return signal.String()
	}
	return fmt.Sprintf("action %d", action)
}

// labelingActionName is the native signal's name ("SIGKILL"), which
// recovery's cleanup records.
func labelingActionName(action labelingAction) string {
	if signal, err := labelingSignal(action); err == nil {
		return unix.SignalName(signal)
	}
	return fmt.Sprintf("action %d", action)
}

// readLabelingProcessStart is `ps -o lstart=` for pid, trimmed: the encoding
// attempts have always stored, so earlier records still compare equal. It
// is not processStartTime, the resource sampler's kernel timestamp.
func readLabelingProcessStart(pid int) (string, error) {
	output, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return "", fmt.Errorf("ps -o lstart= -p %d: %w", pid, err)
	}
	start := strings.TrimSpace(string(output))
	if start == "" {
		return "", fmt.Errorf("ps reported no start time for process %d", pid)
	}
	return start, nil
}
