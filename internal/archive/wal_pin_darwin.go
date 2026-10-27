//go:build darwin

package archive

import "golang.org/x/sys/unix"

// walReadLockHolders asks, for each read slot, whether another process
// holds a lock that a write lock on the slot's byte would conflict with.
// F_GETLK reports one such holder per byte, with its pid; it never reports
// this process's own locks. It opens nothing but the -shm descriptor
// openWALShm keeps, and forks nothing.
func walReadLockHolders(shm string, slots []int) ([]walReader, error) {
	file, err := openWALShm(shm)
	if err != nil {
		return nil, err
	}
	readers := []walReader{}
	commands := map[int]string{}
	for _, slot := range slots {
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: int64(walReadLockByte + slot), Len: 1}
		if err := unix.FcntlFlock(file.Fd(), unix.F_GETLK, &lock); err != nil {
			return nil, err
		}
		if lock.Type == unix.F_UNLCK || lock.Pid <= 0 {
			continue
		}
		pid := int(lock.Pid)
		if _, ok := commands[pid]; !ok {
			commands[pid] = processCommand(pid)
		}
		readers = append(readers, walReader{Slot: slot, PID: pid, Command: commands[pid]})
	}
	return readers, nil
}

// processCommand is pid's command line from kern.procargs2 (argc, the
// executable path, padding, then argv), or "" when it cannot be read.
func processCommand(pid int) string {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return ""
	}
	return parseProcArgs(raw)
}
