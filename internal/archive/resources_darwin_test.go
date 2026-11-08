//go:build darwin

package archive

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The kernel's reading of this process agrees with getrusage (the timebase
// conversion), reads another of this user's processes, and its resident
// size is the figure ps reports, so the index runs' RSS samples stay
// comparable with those taken by forking ps.
func TestProcessUsageMatchesTheKernel(t *testing.T) {
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
	}
	usage, err := processUsageOf(os.Getpid())
	var self syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	getrusage := float64(self.Utime.Nano()+self.Stime.Nano()) / 1e9
	if err != nil || usage.CPUSeconds < getrusage*0.9 || usage.CPUSeconds > getrusage*1.1+0.05 {
		t.Fatalf("own CPU %.3f s, getrusage %.3f s (%v)", usage.CPUSeconds, getrusage, err)
	}
	if usage.Footprint == 0 || usage.Resident == 0 || usage.Started == 0 {
		t.Fatalf("memory and start time: %+v", usage)
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Wait()
	defer child.Process.Kill()
	found := false
	for _, process := range expandResourceProcesses([]resourceProcess{{PID: os.Getpid(), Role: "service"}}, resourceProcessTable()) {
		if process.PID == child.Process.Pid {
			found = process.Parent == os.Getpid() && strings.HasPrefix(process.Role, "worker:")
		}
	}
	if !found {
		t.Fatal("live subprocess missing from resource rows")
	}
	if other, err := processUsageOf(child.Process.Pid); err != nil || other.Footprint == 0 {
		t.Fatalf("another of this user's processes: %+v %v", other, err)
	}
	if _, err := processUsageOf(1); err == nil && os.Geteuid() != 0 {
		t.Fatal("launchd's usage should not be readable by this user")
	}
	if usage.Known != counterOwnCPU|counterChildCPU|counterMemory|counterResident|counterDisk {
		t.Fatalf("a kernel reading has every counter: %b", usage.Known)
	}
	kernel, kernelOK := processRSS()
	ps, psOK := processRSSFromPS()
	if !kernelOK || !psOK || kernel <= 0 || ps <= 0 || float64(kernel) < float64(ps)*0.8 || float64(kernel) > float64(ps)*1.25 {
		t.Fatalf("resident size %d, ps %d", kernel, ps)
	}
}
