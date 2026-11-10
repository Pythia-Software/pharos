//go:build !darwin && !linux

package archive

import (
	"errors"
	"os/exec"
	"testing"
)

// Unsupported targets refuse before execution and leave the command as built.
func TestProcessIsolationUnsupported(t *testing.T) {
	for _, configure := range []func(*exec.Cmd) error{configureAskProcess, configureLoginShellProcess} {
		cmd := exec.Command("unused")
		if err := configure(cmd); !errors.Is(err, errProcessIsolationUnsupported) || cmd.SysProcAttr != nil {
			t.Fatalf("configure = %v, attributes %+v", err, cmd.SysProcAttr)
		}
	}
}
