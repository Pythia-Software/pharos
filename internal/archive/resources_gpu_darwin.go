//go:build darwin

package archive

import (
	"context"
	"os/exec"
	"time"
)

// Include the accelerator's immediate clients and their properties (-l).
// AppUsage GPU times are nanoseconds, unlike proc_pid_rusage's Mach ticks.
// This needs no root, profiler, cgo or persistent helper; the driver keys are
// best-effort rather than a stable Apple API.
var platformGPUClients = func() (gpuSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-a", "-l", "-r", "-d", "2", "-c", "IOAccelerator").Output()
	if err != nil {
		return gpuSnapshot{}, err
	}
	return gpuClientsFromPlist(output)
}
