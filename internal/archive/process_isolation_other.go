//go:build !darwin && !linux

package archive

import "os/exec"

// Without a validated process-group mechanism, refuse rather than run with
// cancellation that reaches only the direct child.
func configureAskProcess(*exec.Cmd) error { return errProcessIsolationUnsupported }

func configureLoginShellProcess(*exec.Cmd) error { return errProcessIsolationUnsupported }
