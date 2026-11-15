//go:build darwin || linux

package archive

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// rusageCPU is getrusage(who): its user plus system seconds and the reading.
func rusageCPU(who int) (float64, syscall.Rusage, bool) {
	var usage syscall.Rusage
	if syscall.Getrusage(who, &usage) != nil {
		return 0, usage, false
	}
	return float64(usage.Utime.Nano()+usage.Stime.Nano()) / 1e9, usage, true
}

// processResourceUsage is this process's getrusage: its own CPU and that of
// its completed, reaped children, each available only when its call
// succeeded, and the Unix block counts (ru_inblock, ru_oublock) of both,
// available only when both calls succeeded.
func processResourceUsage() processResources {
	result := processResources{}
	own, self, ownOK := rusageCPU(syscall.RUSAGE_SELF)
	child, children, childOK := rusageCPU(syscall.RUSAGE_CHILDREN)
	if ownOK {
		result.cpu, result.known = own, result.known|counterOwnCPU
	}
	if childOK {
		result.childCPU, result.known = child, result.known|counterChildCPU
	}
	if ownOK && childOK {
		result.read, result.written = int64(self.Inblock+children.Inblock), int64(self.Oublock+children.Oublock)
		result.known |= counterBlocks
	}
	return result
}

// processRSSFromPS is this process's resident set size as ps -o rss reports
// it. A failed or unreadable ps is unavailable, not zero.
func processRSSFromPS() (int64, bool) {
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, false
	}
	kilobytes, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || kilobytes < 0 {
		return 0, false
	}
	return kilobytes * 1024, true
}
