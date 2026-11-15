//go:build !darwin && !linux

package archive

import (
	"errors"
	"os"
)

// Off macOS and Linux no OS process counter is implemented: CPU, resident
// memory, disk bytes and block counts are unavailable, never zero. The Go
// runtime's own memory figure is still measured, as an approximation that
// is not a resident set size.
const resourceBasis = "go-runtime"

// platformMeasurementLimits completes syncMeasurementLimits.
const platformMeasurementLimits = "On this platform Pharos reads no OS CPU, RSS or block counters; they are unavailable, and only Go allocation and heap figures are measured."

var errOtherProcessUsage = errors.New("only this process's usage can be read on this platform")

func processUsageOf(pid int) (processUsage, error) {
	if pid != os.Getpid() {
		return processUsage{}, errOtherProcessUsage
	}
	return goRuntimeUsage(), nil
}

// processResourceUsage has no OS counters to read here.
func processResourceUsage() processResources { return processResources{} }

// processRSS has no resident set size to read here.
func processRSS() (int64, bool) { return 0, false }

func resourceProcessTable() []processNode { return nil }
