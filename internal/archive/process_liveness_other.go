//go:build !darwin && !linux

package archive

import "fmt"

// No liveness mechanism has been validated here. os.FindProcess succeeding,
// an OpenProcess refusal or a missing procfs entry would not prove presence
// or absence, so every observation is unknown.
func observeNativeProcess(pid int) (processPresence, error) {
	if err := checkProcessID(pid); err != nil {
		return processUnknown, err
	}
	return processUnknown, fmt.Errorf("observe process %d: %w", pid, errProcessLivenessUnsupported)
}
