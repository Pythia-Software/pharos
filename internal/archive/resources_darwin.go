//go:build darwin

package archive

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// proc_pid_rusage lives in libproc, which Go does not link without cgo, and
// the release build does not rely on cgo: macos/build-app.sh builds with
// whatever `go env CGO_ENABLED` says, and the package has no cgo today. The
// libproc call is a thin wrapper over the proc_info system call:
//
//	proc_pid_rusage(pid, flavor, buffer) = __proc_info(PROC_INFO_CALL_PIDRUSAGE, pid, flavor, 0, buffer, 0)
//
// so the sampler makes that system call itself, as capture_iopolicy_darwin.go
// does for iopolicysys. It forks nothing.
const (
	sysProcInfo           = 336 // SYS_proc_info
	procInfoCallPIDRusage = 9   // PROC_INFO_CALL_PIDRUSAGE
	rusageInfoV4          = 4   // RUSAGE_INFO_V4
)

// rusageInfoV4Struct is struct rusage_info_v4 (<sys/resource.h>), 296 bytes.
// Times are in Mach absolute time units, which are nanoseconds on Intel and
// 1/24 µs on Apple silicon; hw.tbfrequency converts them.
type rusageInfoV4Struct struct {
	UUID                     [16]byte
	UserTime                 uint64
	SystemTime               uint64
	PkgIdleWkups             uint64
	InterruptWkups           uint64
	Pageins                  uint64
	WiredSize                uint64
	ResidentSize             uint64
	PhysFootprint            uint64
	ProcStartAbstime         uint64
	ProcExitAbstime          uint64
	ChildUserTime            uint64
	ChildSystemTime          uint64
	ChildPkgIdleWkups        uint64
	ChildInterruptWkups      uint64
	ChildPageins             uint64
	ChildElapsedAbstime      uint64
	DiskioBytesread          uint64
	DiskioByteswritten       uint64
	CPUTimeQoS               [7]uint64
	BilledSystemTime         uint64
	ServicedSystemTime       uint64
	LogicalWrites            uint64
	LifetimeMaxPhysFootprint uint64
	Instructions             uint64
	Cycles                   uint64
	BilledEnergy             uint64
	ServicedEnergy           uint64
	IntervalMaxPhysFootprint uint64
	RunnableTime             uint64
}

// timebaseHz is how many Mach absolute time units make a second.
var timebaseHz = sync.OnceValue(func() float64 {
	if hz, err := unix.SysctlUint64("hw.tbfrequency"); err == nil && hz > 0 {
		return float64(hz)
	}
	if runtime.GOARCH == "arm64" {
		return 24e6
	}
	return 1e9
})

// resourceBasis names how processUsageOf measures.
const resourceBasis = "proc_pid_rusage"

// processUsageOf reads pid's CPU time (its own and its exited children's)
// and memory. It works for this user's processes; others fail with EPERM.
func processUsageOf(pid int) (processUsage, error) {
	var info rusageInfoV4Struct
	if _, _, errno := syscall.Syscall6(sysProcInfo, procInfoCallPIDRusage, uintptr(pid), rusageInfoV4, 0, uintptr(unsafe.Pointer(&info)), 0); errno != 0 {
		return processUsage{}, errno
	}
	seconds := func(ticks uint64) float64 { return float64(ticks) / timebaseHz() }
	return processUsage{
		CPUSeconds:      seconds(info.UserTime + info.SystemTime),
		ChildCPUSeconds: seconds(info.ChildUserTime + info.ChildSystemTime),
		Footprint:       info.PhysFootprint,
		Resident:        info.ResidentSize,
		Started:         info.ProcStartAbstime,
	}, nil
}
