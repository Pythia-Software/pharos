package archive

import (
	"time"

	"golang.org/x/sys/unix"
)

// processStartTime is when process pid started, from the kernel's process
// table; ok is false when it is not running or cannot be read.
func processStartTime(pid int) (time.Time, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || info.Proc.P_pid != int32(pid) {
		return time.Time{}, false
	}
	started := info.Proc.P_starttime
	return time.Unix(int64(started.Sec), int64(started.Usec)*1000), true
}
