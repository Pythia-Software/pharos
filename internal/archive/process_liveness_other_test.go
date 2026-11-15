//go:build !darwin && !linux

package archive

import (
	"errors"
	"os"
	"testing"
)

// Without a validated mechanism every observation, this process's included,
// is unknown and says why, so no caller mistakes it for absence.
func TestProcessLivenessUnsupported(t *testing.T) {
	for _, pid := range []int{4242, os.Getpid()} {
		state, err := observeNativeProcess(pid)
		if state != processUnknown || !errors.Is(err, errProcessLivenessUnsupported) {
			t.Fatalf("observe %d = %v, %v", pid, state, err)
		}
	}
}
