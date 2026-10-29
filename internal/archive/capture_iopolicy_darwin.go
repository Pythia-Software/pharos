//go:build darwin

package archive

import (
	"runtime"
	"syscall"
	"unsafe"
)

// setiopolicy_np lives in libc, which Go does not link; it is a thin wrapper
// over the iopolicysys system call, whose ABI (verified against libc on macOS
// 15) is below.
const (
	sysIOPolicy     = 322 // SYS_IOPOLICYSYS
	ioPolicyGet     = 1
	ioPolicySet     = 2
	ioPolicyDisk    = 0 // IOPOL_TYPE_DISK
	ioPolicyThread  = 1 // IOPOL_SCOPE_THREAD
	ioPolicyUtility = 4 // IOPOL_UTILITY
)

type ioPolicyParam struct{ scope, iotype, policy int32 }

func ioPolicy(command int, param *ioPolicyParam) error {
	if _, _, errno := syscall.Syscall(sysIOPolicy, uintptr(command), uintptr(unsafe.Pointer(param)), 0); errno != 0 {
		return errno
	}
	return nil
}

// currentIOPolicy reports the calling thread's disk I/O policy.
func currentIOPolicy() (int32, error) {
	param := ioPolicyParam{scope: ioPolicyThread, iotype: ioPolicyDisk}
	err := ioPolicy(ioPolicyGet, &param)
	return param.policy, err
}

// withCaptureIOPolicy runs fn with its disk I/O in the utility tier, which
// yields to the interactive work of whoever is using the Mac but, unlike the
// background (throttle) tier, still finishes promptly when someone is waiting
// to unplug. The policy is per thread so the service's own requests keep their
// priority; fn must do its I/O on the calling goroutine.
//
// The thread goes back to Go's pool only once its policy is restored, on a
// return or a panic alike. If restoring fails, the goroutine leaves the
// thread locked, and Go ends a thread whose goroutine exits locked rather
// than reuse it at the utility tier.
func withCaptureIOPolicy(fn func()) {
	runtime.LockOSThread()
	previous, err := currentIOPolicy()
	if err != nil || ioPolicy(ioPolicySet, &ioPolicyParam{scope: ioPolicyThread, iotype: ioPolicyDisk, policy: ioPolicyUtility}) != nil {
		defer runtime.UnlockOSThread()
		fn()
		return
	}
	defer func() {
		if restoreIOPolicy(previous) == nil {
			runtime.UnlockOSThread()
		}
	}()
	fn()
}

// restoreIOPolicy sets the calling thread's disk policy back; tests replace
// it to fail.
var restoreIOPolicy = func(policy int32) error {
	return ioPolicy(ioPolicySet, &ioPolicyParam{scope: ioPolicyThread, iotype: ioPolicyDisk, policy: policy})
}
