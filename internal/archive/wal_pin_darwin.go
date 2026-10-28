//go:build darwin

package archive

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// probeWALShm reads the -shm header and asks F_GETLK which other process
// holds each lock byte. It opens nothing on the library's volume: closing a
// descriptor of a file drops every POSIX lock this process holds on it,
// SQLite's read locks included, and a descriptor kept open would keep the
// volume busy after a release. It borrows the descriptor SQLite already has
// open on the file (shmDescriptor), reads with pread and F_GETLK, which
// change nothing, and checks the descriptor still names the file after.
func probeWALShm(shm string) (walProbe, error) {
	fd, err := shmDescriptor(shm)
	if err != nil {
		return walProbe{}, err
	}
	var probe walProbe
	b := make([]byte, walIndexHeaderBytes)
	if n, err := unix.Pread(fd, b, 0); err != nil || n < len(b) {
		probe.HeaderErr = errors.New("could not read the wal-index header")
	} else {
		probe.Header, probe.HeaderErr = parseWALIndexHeader(b)
	}
	commands := map[int]string{}
	for at := walWriteLockByte; at < walReadLockByte+walReadSlots; at++ {
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: int64(at), Len: 1}
		if err := unix.FcntlFlock(uintptr(fd), unix.F_GETLK, &lock); err != nil {
			return walProbe{}, err
		}
		if lock.Type == unix.F_UNLCK || lock.Pid <= 0 {
			continue
		}
		pid := int(lock.Pid)
		if _, ok := commands[pid]; !ok {
			commands[pid] = processCommand(pid)
		}
		probe.Locks = append(probe.Locks, walLock{Byte: at, PID: pid, Command: commands[pid]})
	}
	// SQLite may have closed the descriptor meanwhile and the number been
	// reused: then what was read is not the catalog's.
	if again, err := shmDescriptor(shm); err != nil || again != fd {
		return walProbe{}, errors.New("the catalog's shared-memory file changed while it was read")
	}
	return probe, nil
}

// shmDescriptor finds this process's open descriptor of the file at path
// by listing /dev/fd and comparing device and inode; nothing is opened on
// the file itself.
func shmDescriptor(path string) (int, error) {
	var want unix.Stat_t
	if err := unix.Stat(path, &want); err != nil {
		return -1, err
	}
	for _, fd := range openDescriptors() {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) == nil && st.Dev == want.Dev && st.Ino == want.Ino {
			return fd, nil
		}
	}
	return -1, errors.New("SQLite has no open descriptor of the catalog's shared-memory file")
}

// openDescriptors lists this process's open descriptors.
func openDescriptors() []int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return nil
	}
	fds := []int{}
	for _, entry := range entries {
		if fd, err := strconv.Atoi(entry.Name()); err == nil {
			fds = append(fds, fd)
		}
	}
	return fds
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
