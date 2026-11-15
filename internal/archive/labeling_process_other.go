//go:build !darwin && !linux

package archive

import (
	"fmt"
	"os/exec"
)

// Without a validated process-group mechanism, a labeling CLI is never
// started, and recovery can neither prove an earlier group gone nor
// identify its leader, so it signals nothing.

func configureLabelingProcess(*exec.Cmd) error { return errLabelingProcessUnsupported }

func observeLabelingGroup(int) (labelingGroupState, error) {
	return labelingGroupUnknown, errLabelingProcessUnsupported
}

func signalLabelingGroup(int, labelingAction) error { return errLabelingProcessUnsupported }

func readLabelingProcessStart(int) (string, error) { return "", errLabelingProcessUnsupported }

func labelingActionLabel(action labelingAction) string {
	switch action {
	case labelingTerminate:
		return "terminate"
	case labelingForceKill:
		return "force kill"
	}
	return fmt.Sprintf("action %d", action)
}

func labelingActionName(action labelingAction) string { return labelingActionLabel(action) }
