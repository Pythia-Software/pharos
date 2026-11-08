//go:build darwin || linux

package archive

import (
	"errors"
	"fmt"
	"syscall"
)

// processLivenessKill is kill(2), overridable by tests so that the mapping
// is checked without addressing a real process.
var processLivenessKill = syscall.Kill

// observeNativeProcess sends signal 0 to pid: success proves it present and
// ESRCH proves it absent. EPERM (it runs under another user) and every other
// failure leave it unknown with the native cause.
func observeNativeProcess(pid int) (processPresence, error) {
	if err := checkProcessID(pid); err != nil {
		return processUnknown, err
	}
	switch err := processLivenessKill(pid, 0); {
	case err == nil:
		return processPresent, nil
	case errors.Is(err, syscall.ESRCH):
		return processAbsent, nil
	default:
		return processUnknown, fmt.Errorf("observe process %d: %w", pid, err)
	}
}
