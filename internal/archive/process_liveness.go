package archive

import (
	"errors"
	"fmt"
)

// Process liveness answers one question about one process: does a process
// with this ID run now? It says nothing about who owns that process, when it
// started, what it contains or whether an endpoint it published is genuine;
// those are separate contracts (processStartTime, the labeler's group
// control, endpoint tokens). Callers decide what each answer permits.

// processPresence is what an observation of a single process showed.
// Unknown keeps a refused, failed or unsupported observation apart from
// absence: only the platform's "no such process" proves a process gone.
type processPresence int

const (
	processUnknown processPresence = iota
	processPresent
	processAbsent
)

func (p processPresence) String() string {
	switch p {
	case processPresent:
		return "present"
	case processAbsent:
		return "absent"
	}
	return "unknown"
}

// errProcessLivenessUnsupported marks a platform without a validated way to
// observe a process. It is never absence.
var errProcessLivenessUnsupported = errors.New("this platform cannot observe whether a process runs")

// errProcessIDInvalid refuses a zero or negative ID, which kill(2) would read
// as a process group or every process rather than one process.
var errProcessIDInvalid = errors.New("not a single process ID")

// observeProcess reports whether process pid runs, overridable by tests.
// The error is non-nil exactly when the presence is unknown, and keeps the
// platform's cause (or errProcessIDInvalid, errProcessLivenessUnsupported)
// in its chain.
var observeProcess = observeNativeProcess

// checkProcessID refuses an ID that does not name a single process, before
// any platform call sees it.
func checkProcessID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("observe process %d: %w", pid, errProcessIDInvalid)
	}
	return nil
}
