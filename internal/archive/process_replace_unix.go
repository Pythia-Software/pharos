//go:build darwin || linux

package archive

import "syscall"

// replaceProcessNative runs executable in place of this process, keeping its
// PID and open stdio. It returns only on failure.
func replaceProcessNative(executable string, argv, env []string) error {
	return syscall.Exec(executable, argv, env)
}
