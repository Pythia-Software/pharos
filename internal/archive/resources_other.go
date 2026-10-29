//go:build !darwin

package archive

import (
	"errors"
	"os"
	"runtime/metrics"
	"syscall"
)

// resourceBasis names how processUsageOf measures: off macOS a process can
// report only itself, its getrusage CPU and the Go runtime's memory, which
// is approximate (it leaves out memory the runtime does not manage).
const resourceBasis = "self-reported"

var errOtherProcessUsage = errors.New("only this process's usage can be read on this platform")

func processUsageOf(pid int) (processUsage, error) {
	if pid != os.Getpid() {
		return processUsage{}, errOtherProcessUsage
	}
	usage := processUsage{}
	for who, total := range map[int]*float64{syscall.RUSAGE_SELF: &usage.CPUSeconds, syscall.RUSAGE_CHILDREN: &usage.ChildCPUSeconds} {
		var r syscall.Rusage
		if syscall.Getrusage(who, &r) == nil {
			*total = float64(r.Utime.Nano()+r.Stime.Nano()) / 1e9
		}
	}
	sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() == metrics.KindUint64 {
		usage.Footprint = sample[0].Value.Uint64()
		usage.Resident = usage.Footprint
	}
	return usage, nil
}
