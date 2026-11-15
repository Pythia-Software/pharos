//go:build !darwin && !linux

package archive

import (
	"errors"
	"os/exec"
	"testing"
)

// Unsupported targets refuse before launch and never report a group absent,
// so recovery cannot mistake the refusal for completed cleanup.
func TestLabelingProcessUnsupported(t *testing.T) {
	cmd := exec.Command("unused")
	if err := configureLabelingProcess(cmd); !errors.Is(err, errLabelingProcessUnsupported) || cmd.SysProcAttr != nil {
		t.Fatalf("configure = %v, attributes %+v", err, cmd.SysProcAttr)
	}
	if state, err := observeLabelingGroup(4242); state != labelingGroupUnknown || !errors.Is(err, errLabelingProcessUnsupported) {
		t.Fatalf("observe = %v, %v", state, err)
	}
	for _, action := range []labelingAction{labelingTerminate, labelingForceKill} {
		if err := signalLabelingGroup(4242, action); !errors.Is(err, errLabelingProcessUnsupported) || errors.Is(err, errLabelingGroupAbsent) {
			t.Fatalf("signal %v = %v", action, err)
		}
	}
	if start, err := readLabelingProcessStart(4242); start != "" || !errors.Is(err, errLabelingProcessUnsupported) {
		t.Fatalf("start = %q, %v", start, err)
	}
	if got := labelingRecoverGroup(4242, "Thu Jan  1 00:00:00 1970"); len(got) != 1 || got[0]["reason"] != "group_status_unknown" {
		t.Fatalf("recovery = %v", got)
	}
}
