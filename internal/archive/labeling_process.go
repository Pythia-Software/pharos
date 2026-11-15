package archive

import (
	"errors"
	"os"
)

// A labeling CLI runs in its own process group so that cleanup reaches the
// children holding its output too. The mechanisms (configuring the command,
// observing and signaling the group, reading the leader's start identity)
// are per platform; runLabelingProcess and labelingRunner.recover own the
// timers, policy and what is recorded.

// labelingAction is what a signal asks of a process group, independent of
// the platform's signal numbers.
type labelingAction int

const (
	// labelingTerminate asks the group to exit (SIGTERM on Unix).
	labelingTerminate labelingAction = iota + 1
	// labelingForceKill ends the group without its cooperation (SIGKILL).
	labelingForceKill
)

// labelingGroupState is what an observation of a process group showed.
// Unknown keeps a failed or unsupported observation apart from absence: only
// "no such process" proves the group gone.
type labelingGroupState int

const (
	labelingGroupUnknown labelingGroupState = iota
	labelingGroupPresent
	labelingGroupAbsent
)

// errLabelingProcessUnsupported refuses to start a labeling CLI where its
// process group cannot be created, observed and signaled. No substitute
// (parent-only Kill, taskkill, a console process group or a Job Object) has
// been validated for labeling.
var errLabelingProcessUnsupported = errors.New("this platform cannot run a labeling CLI in its own process group")

// errLabelingGroupAbsent marks a signal that found no process in the group.
// The platform's own cause (ESRCH) stays in the chain.
var errLabelingGroupAbsent = errors.New("the process group no longer runs")

// The mechanisms, overridable by tests. Each returns the platform's original
// cause on failure.
var (
	// labelingConfigureProcess prepares a command, before it starts, to lead
	// a new process group; an error refuses the launch.
	labelingConfigureProcess = configureLabelingProcess
	// labelingObserveGroup reports whether any process in the group led by
	// pid runs. The error explains an unknown state.
	labelingObserveGroup = observeLabelingGroup
	// labelingSignalGroup delivers an action to the group led by pid. An
	// error wrapping errLabelingGroupAbsent means nothing was there.
	labelingSignalGroup = signalLabelingGroup
	// labelingReadProcessStart reads the start identity of process pid (on
	// Darwin and Linux the trimmed `ps -o lstart=` text). Attempts store it
	// as process_start, so its encoding must not change.
	labelingReadProcessStart = readLabelingProcessStart
	// labelingPipe allocates the CLI's stdin and stdout pipes.
	labelingPipe = os.Pipe
)

// labelingProcessStart is the start identity to record for a CLI just
// started, or empty when it cannot be read; an empty record never
// authorizes recovery to signal the group.
func labelingProcessStart(pid int) string {
	start, err := labelingReadProcessStart(pid)
	if err != nil {
		return ""
	}
	return start
}
