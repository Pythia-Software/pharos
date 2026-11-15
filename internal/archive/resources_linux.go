//go:build linux

package archive

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// threadCPUTime is the CPU time the calling thread has used, and the
// thread's id. It means something only to a goroutine locked to its thread.
func threadCPUTime() (seconds float64, thread uint64, ok bool) {
	var used unix.Timespec
	if unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &used) != nil {
		return 0, 0, false
	}
	return float64(used.Nano()) / 1e9, uint64(unix.Gettid()), true
}

// resourceBasis names how processUsageOf measures: on Linux a process
// reports only itself, its getrusage CPU and the Go runtime's memory, which
// is approximate (it leaves out memory the runtime does not manage, so it is
// not a resident set size).
const resourceBasis = "self-reported"

// platformMeasurementLimits completes syncMeasurementLimits.
const platformMeasurementLimits = "On Linux, CPU and block counts come from getrusage and RSS from ps; a failed reading is unavailable."

var errOtherProcessUsage = errors.New("only this process's usage can be read on this platform")

func processUsageOf(pid int) (processUsage, error) {
	if pid != os.Getpid() {
		return processUsage{}, errOtherProcessUsage
	}
	usage := goRuntimeUsage()
	if seconds, _, ok := rusageCPU(syscall.RUSAGE_SELF); ok {
		usage.CPUSeconds, usage.Known = seconds, usage.Known|counterOwnCPU
	}
	if seconds, _, ok := rusageCPU(syscall.RUSAGE_CHILDREN); ok {
		usage.ChildCPUSeconds, usage.Known = seconds, usage.Known|counterChildCPU
	}
	return usage, nil
}

// processRSS is this process's resident set size, from ps.
func processRSS() (int64, bool) { return processRSSFromPS() }

func resourceProcessTable() []processNode { return nil }
