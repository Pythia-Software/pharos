package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Process leases (docs/background-work.md §6.5). Every Pharos process other
// than the service that runs a structural operation holds an exclusive flock
// on its own lease file for as long as it runs it. The flock is the liveness
// signal: a probe that cannot take a shared lock has found a live process,
// whatever its pid. A lease is created and updated in place and never
// replaced by a rename, since a new inode at the path would be unlocked.

const leaseDirName = ".pharos-leases"

// leaseInfo is a lease file's contents.
type leaseInfo struct {
	Instance string   `json:"instance"`
	PID      int      `json:"pid"`
	HostID   string   `json:"host_id"`
	Process  string   `json:"process"`
	Kinds    []string `json:"kinds"`
	Holds    []Hold   `json:"holds"`
	// Locks are the capture and index locks its operations hold now.
	Locks      []Hold   `json:"locks,omitempty"`
	StartedAt  string   `json:"started_at"`
	Operations []string `json:"operations,omitempty"`
	// Libraries are the configurations of libraries an app-level command
	// (a transfer, a recovery) writes.
	Libraries []string                `json:"libraries,omitempty"`
	Outcomes  map[string]leaseOutcome `json:"outcomes,omitempty"`
	// volatile leases are not fsynced while live: the flock is the liveness,
	// and a crash leaves a file the service treats as dead whatever it holds.
	// Outcomes, written when abandoning, are fsynced regardless.
	volatile bool
}

// leaseOutcome is an operation's terminal state, recorded in the lease when
// the process's final journal round could not write it.
type leaseOutcome struct {
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Error   string `json:"error,omitempty"`
	EndedAt string `json:"ended_at"`
}

// leaseDirs are the directories the service lists: beside the catalog, for
// commands that open it, and under the capture root, for those that never
// do (capture, backup).
func leaseDirs(config Config) []string {
	dirs := []string{filepath.Join(filepath.Dir(config.CatalogPath), leaseDirName)}
	if config.CaptureRoot != "" {
		if dir := filepath.Join(config.CaptureRoot, leaseDirName); dir != dirs[0] {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// appLeaseDir is where app-level Terminal commands (a library transfer or
// recovery) also take their lease, for the broker to list them.
func appLeaseDir() string {
	return filepath.Join(pharosSupportDir(), leaseDirName)
}

func leasePath(dir, instance string) string {
	return filepath.Join(dir, instance+".json")
}

// processLease is a lease this process holds.
type processLease struct {
	mu   sync.Mutex
	path string
	file *os.File
	info leaseInfo
}

// acquireProcessLease creates and locks dir/<instance>.json and writes info
// into it: create, LOCK_EX, check that the locked file is still the one at
// the path (a deleter may have unlinked it in between), then write in place.
func acquireProcessLease(dir string, info leaseInfo) (*processLease, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := leasePath(dir, info.Instance)
	for attempt := 0; attempt < 10; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		// Blocking is fine: only a deleter holds it, briefly.
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
			file.Close()
			return nil, err
		}
		if !sameOpenFile(file, path) {
			file.Close()
			continue
		}
		lease := &processLease{path: path, file: file, info: info}
		if err := lease.writeLocked(); err != nil {
			_ = os.Remove(path)
			file.Close()
			return nil, err
		}
		return lease, nil
	}
	return nil, fmt.Errorf("lease %s kept changing while it was created", path)
}

// sameOpenFile reports whether the open file is the one at path (same device and
// inode).
func sameOpenFile(file *os.File, path string) bool {
	opened, err := file.Stat()
	if err != nil {
		return false
	}
	current, err := os.Stat(path)
	return err == nil && os.SameFile(opened, current)
}

func (l *processLease) writeLocked() error {
	data, err := json.Marshal(l.info)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.WriteAt(data, 0); err != nil {
		return err
	}
	if l.info.volatile && len(l.info.Outcomes) == 0 {
		return nil
	}
	return l.file.Sync()
}

// update rewrites the lease in place.
func (l *processLease) update(change func(*leaseInfo)) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return os.ErrClosed
	}
	change(&l.info)
	return l.writeLocked()
}

// release ends the lease: unlinked while still locked, then closed.
func (l *processLease) release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	_ = os.Remove(l.path)
	l.file.Close()
	l.file = nil
}

// abandon records outcomes in the lease, then releases the lock without
// unlinking, so the outcomes survive for the service's reconcile pass.
func (l *processLease) abandon(outcomes map[string]leaseOutcome) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return os.ErrClosed
	}
	l.info.Outcomes = outcomes
	err := l.writeLocked()
	l.file.Close()
	l.file = nil
	return err
}

// leaseProbe is what a probe found at one lease file.
type leaseProbe struct {
	Path string
	Live bool
	Info *leaseInfo
	// Err is set when the file could not be opened, probed or read: the
	// verdict cannot vouch for whatever it describes.
	Err error
}

// probeLease opens path read-only and tries a shared, non-blocking lock:
// held means live. It writes nothing and deletes nothing.
func probeLease(path string) leaseProbe {
	probe := leaseProbe{Path: path}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			probe.Err = os.ErrNotExist
		} else {
			probe.Err = err
		}
		return probe
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			probe.Err = err
			return probe
		}
		probe.Live = true
	} else {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}
	// A live lease may be mid-update (truncated, then written); read again.
	var info leaseInfo
	for attempt := 0; ; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &info) == nil && info.Instance != "" {
			probe.Info = &info
			return probe
		}
		if attempt >= 4 {
			if err == nil {
				err = fmt.Errorf("lease %s is not readable", filepath.Base(path))
			}
			if errors.Is(err, os.ErrNotExist) && !probe.Live {
				probe.Err = os.ErrNotExist
			} else {
				probe.Err = err
			}
			return probe
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// probeLeases lists dirs and probes every lease file in them.
func probeLeases(dirs []string) []leaseProbe {
	probes := []leaseProbe{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue // no process has taken a lease here
		}
		if err != nil {
			// A directory that exists but cannot be listed may hold a live
			// lease: the verdict cannot vouch for it (fail closed).
			probes = append(probes, leaseProbe{Path: dir, Err: err})
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			probe := probeLease(filepath.Join(dir, entry.Name()))
			if errors.Is(probe.Err, os.ErrNotExist) {
				continue // deleted since the listing
			}
			probes = append(probes, probe)
		}
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].Path < probes[j].Path })
	return probes
}

// findLease probes instance's lease in dirs: found is false when no file
// exists in any of them.
func findLease(dirs []string, instance string) (leaseProbe, bool) {
	for _, dir := range dirs {
		probe := probeLease(leasePath(dir, instance))
		if !errors.Is(probe.Err, os.ErrNotExist) {
			return probe, true
		}
	}
	return leaseProbe{}, false
}

// claimDeadLease takes path's lock without waiting. ok is false for a live
// lease (or one that cannot be opened); otherwise the caller holds the lock
// and must call done, with remove true to unlink the file while holding it.
func claimDeadLease(path string) (info *leaseInfo, done func(remove bool), ok bool) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, false
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, nil, false
	}
	if !sameOpenFile(file, path) {
		file.Close()
		return nil, nil, false
	}
	var contents leaseInfo
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &contents) == nil {
		info = &contents
	}
	return info, func(remove bool) {
		if remove {
			_ = os.Remove(path)
		}
		file.Close()
	}, true
}

// leaseWatch caches lease probes for snapshot reads, for at most a second
// (§6.5); the release path probes afresh.
type leaseWatch struct {
	mu     sync.Mutex
	dirs   func() []string
	at     time.Time
	probes []leaseProbe
}

func (w *leaseWatch) probe(fresh bool) []leaseProbe {
	if w.dirs == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if fresh || time.Since(w.at) > time.Second {
		w.probes, w.at = probeLeases(w.dirs()), time.Now()
	}
	return w.probes
}
