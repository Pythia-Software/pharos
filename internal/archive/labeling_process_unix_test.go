//go:build darwin || linux

package archive

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// The native helpers address the group (a negative pid) with SIGTERM or
// SIGKILL, keep ESRCH apart from other failures, and never address the
// caller's own group or every process. No child runs: the kill(2) seam
// stands in for the kernel.
func TestLabelingProcessSignals(t *testing.T) {
	type call struct {
		pid int
		sig syscall.Signal
	}
	var calls []call
	var result error
	override(t, &labelingKill, func(pid int, sig syscall.Signal) error {
		calls = append(calls, call{pid, sig})
		return result
	})
	cmd := exec.Command("unused")
	if err := configureLabelingProcess(cmd); err != nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Setsid {
		t.Fatalf("configure = %v, attributes %+v", err, cmd.SysProcAttr)
	}

	for _, step := range []struct {
		name   string
		result error
		want   labelingGroupState
	}{{"running", nil, labelingGroupPresent}, {"gone", syscall.ESRCH, labelingGroupAbsent},
		{"not permitted", syscall.EPERM, labelingGroupUnknown}, {"other", syscall.EINVAL, labelingGroupUnknown}} {
		calls, result = nil, step.result
		state, err := observeLabelingGroup(4242)
		if state != step.want || !errors.Is(err, step.result) || len(calls) != 1 || calls[0] != (call{-4242, 0}) {
			t.Fatalf("observe %s = %v, %v; calls %v", step.name, state, err, calls)
		}
	}

	for _, step := range []struct {
		name   string
		action labelingAction
		result error
		sig    syscall.Signal
		absent bool
	}{{"terminate", labelingTerminate, nil, syscall.SIGTERM, false}, {"force", labelingForceKill, nil, syscall.SIGKILL, false},
		{"terminate gone", labelingTerminate, syscall.ESRCH, syscall.SIGTERM, true},
		{"force not permitted", labelingForceKill, syscall.EPERM, syscall.SIGKILL, false}} {
		calls, result = nil, step.result
		err := signalLabelingGroup(4242, step.action)
		if len(calls) != 1 || calls[0] != (call{-4242, step.sig}) || errors.Is(err, errLabelingGroupAbsent) != step.absent ||
			(step.result == nil) != (err == nil) || (step.result != nil && !errors.Is(err, step.result)) {
			t.Fatalf("signal %s = %v; calls %v", step.name, err, calls)
		}
	}

	calls, result = nil, nil
	for _, pid := range []int{1, 0, -4242} {
		if state, err := observeLabelingGroup(pid); state != labelingGroupUnknown || err == nil {
			t.Fatalf("observe %d = %v, %v", pid, state, err)
		}
		if err := signalLabelingGroup(pid, labelingForceKill); err == nil || errors.Is(err, errLabelingGroupAbsent) {
			t.Fatalf("signal %d = %v", pid, err)
		}
	}
	if err := signalLabelingGroup(4242, labelingAction(99)); err == nil || len(calls) != 0 {
		t.Fatalf("unknown action = %v; calls %v", err, calls)
	}

	// Saved attempts compare these labels: Go's descriptions in a run's
	// cleanup, the signal name in recovery's.
	if got := []string{labelingActionLabel(labelingTerminate), labelingActionLabel(labelingForceKill), labelingActionName(labelingForceKill)}; fmt.Sprint(got) != "[terminated killed SIGKILL]" {
		t.Fatalf("labels = %v", got)
	}
}

// The start identity is ps's lstart text, trimmed, stable across reads of
// the same process, and an error (not an empty match) for a process that
// has exited.
func TestLabelingProcessStartIdentity(t *testing.T) {
	output, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	first, err := readLabelingProcessStart(os.Getpid())
	second, _ := readLabelingProcessStart(os.Getpid())
	if err != nil || first == "" || first != strings.TrimSpace(string(output)) || first != second || labelingProcessStart(os.Getpid()) != first {
		t.Fatalf("start = %q, %v; again %q; ps %q", first, err, second, output)
	}
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	if start, err := readLabelingProcessStart(child.Process.Pid); err == nil {
		t.Fatalf("an exited process has start %q", start)
	}
	if labelingProcessStart(child.Process.Pid) != "" {
		t.Fatal("the recording wrapper kept a start for an exited process")
	}
}
