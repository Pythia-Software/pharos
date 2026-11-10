package archive

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

// livenessAnswer is what a stubbed observeProcess reports for one ID.
type livenessAnswer struct {
	state processPresence
	err   error
}

// livenessCases are the observations each caller must handle, by name. None
// addresses a real process: stubProcessLiveness answers in its place.
func livenessCases(pid int) map[string]livenessAnswer {
	return map[string]livenessAnswer{
		"present":     {processPresent, nil},
		"absent":      {processAbsent, nil},
		"permission":  {processUnknown, fmt.Errorf("observe process %d: %w", pid, os.ErrPermission)},
		"invalid":     {processUnknown, fmt.Errorf("observe process %d: %w", pid, errProcessIDInvalid)},
		"unsupported": {processUnknown, fmt.Errorf("observe process %d: %w", pid, errProcessLivenessUnsupported)},
	}
}

// processObservations records the IDs a stubbed observeProcess was asked
// about, and announces each on seen when it is set.
type processObservations struct {
	mu   sync.Mutex
	pids []int
	seen chan int
}

func (o *processObservations) observed() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int(nil), o.pids...)
}

// stubProcessLiveness answers observeProcess from answers until t ends. Like
// the platform, it refuses an ID that is not a single process; an ID with no
// answer is unknown.
func stubProcessLiveness(t testing.TB, answers map[int]livenessAnswer) *processObservations {
	t.Helper()
	o := &processObservations{}
	override(t, &observeProcess, func(pid int) (processPresence, error) {
		o.mu.Lock()
		o.pids = append(o.pids, pid)
		seen := o.seen
		o.mu.Unlock()
		if seen != nil {
			seen <- pid
		}
		if err := checkProcessID(pid); err != nil {
			return processUnknown, err
		}
		if answer, ok := answers[pid]; ok {
			return answer.state, answer.err
		}
		return processUnknown, fmt.Errorf("observe process %d: no stubbed answer", pid)
	})
	return o
}

// Zero and negative IDs name process groups or every process, so the
// mechanism refuses them on every platform before any platform call.
func TestProcessLivenessRefusesInvalidIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -4242} {
		if state, err := observeNativeProcess(pid); state != processUnknown || !errors.Is(err, errProcessIDInvalid) {
			t.Fatalf("observe %d = %v, %v", pid, state, err)
		}
	}
	if err := checkProcessID(1); err != nil {
		t.Fatalf("a positive ID was refused: %v", err)
	}
}
