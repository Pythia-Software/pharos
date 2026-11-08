//go:build darwin || linux

package archive

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// Signal 0 goes to the positive ID itself: success is present, ESRCH absent,
// and any other failure unknown with its native cause. Invalid IDs never
// reach kill(2). The seam stands in for the kernel, except for this process.
func TestProcessLivenessNative(t *testing.T) {
	type call struct {
		pid    int
		signal syscall.Signal
	}
	for _, tc := range []struct {
		name   string
		pid    int
		result error
		state  processPresence
		calls  []call
	}{
		{"running", 4242, nil, processPresent, []call{{4242, 0}}},
		{"gone", 4242, syscall.ESRCH, processAbsent, []call{{4242, 0}}},
		{"another user's", 4242, syscall.EPERM, processUnknown, []call{{4242, 0}}},
		{"failed", 4242, syscall.EINVAL, processUnknown, []call{{4242, 0}}},
		{"init", 1, syscall.EPERM, processUnknown, []call{{1, 0}}},
		{"own group", 0, nil, processUnknown, nil},
		{"every process", -1, nil, processUnknown, nil},
		{"a group", -4242, nil, processUnknown, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []call
			override(t, &processLivenessKill, func(pid int, signal syscall.Signal) error {
				calls = append(calls, call{pid, signal})
				return tc.result
			})
			state, err := observeNativeProcess(tc.pid)
			if state != tc.state || len(calls) != len(tc.calls) || (len(calls) == 1 && calls[0] != tc.calls[0]) {
				t.Fatalf("observe %d = %v, %v; kill calls %v; want %v, %v", tc.pid, state, err, calls, tc.state, tc.calls)
			}
			switch {
			case tc.pid <= 0:
				if !errors.Is(err, errProcessIDInvalid) {
					t.Fatalf("invalid ID error = %v", err)
				}
			case tc.state == processUnknown:
				if !errors.Is(err, tc.result) || errors.Is(err, syscall.ESRCH) {
					t.Fatalf("unknown lost its native cause: %v", err)
				}
			case err != nil:
				t.Fatalf("a definite observation carried an error: %v", err)
			}
		})
	}
	// The real kernel, for the one process a test may safely name: this one.
	if state, err := observeNativeProcess(os.Getpid()); state != processPresent || err != nil {
		t.Fatalf("this process = %v, %v", state, err)
	}
}
