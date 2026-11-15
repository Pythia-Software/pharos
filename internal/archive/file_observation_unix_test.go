//go:build darwin || linux

package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestFileObservationsReadOneNativeStat(t *testing.T) {
	observations := map[string]func(os.FileInfo) error{
		"directory inode":  func(info os.FileInfo) error { _, err := libraryDirectoryInode(info); return err },
		"placement device": func(info os.FileInfo) error { _, err := placementDevice(info); return err },
		"legacy label":     func(info os.FileInfo) error { _, err := legacyDeviceLabel(info); return err },
		"hardlink key":     func(info os.FileInfo) error { _, err := hardlinkKey(info); return err },
	}
	real, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		info os.FileInfo
	}{
		{"nil file info", nil},
		{"nil system metadata", directoryVolumeFileInfo{}},
		{"foreign system metadata", foreignMetadataInfo{real}},
		{"non-pointer stat", directoryVolumeFileInfo{metadata: syscall.Stat_t{}}},
		{"typed-nil stat", directoryVolumeFileInfo{metadata: (*syscall.Stat_t)(nil)}},
	} {
		for name, observe := range observations {
			if err := observe(tc.info); !errors.Is(err, errFileObservationUnavailable) {
				t.Errorf("%s of %s: %v; want unavailable", name, tc.name, err)
			}
		}
	}
	// A native zero is a value, told apart from no observation by err.
	zero := directoryVolumeFileInfo{metadata: &syscall.Stat_t{}}
	for name, observe := range observations {
		if err := observe(zero); err != nil {
			t.Errorf("%s of a zero stat: %v", name, err)
		}
	}
	if label, _ := legacyDeviceLabel(zero); label != "device:0" {
		t.Errorf("zero legacy label %q", label)
	}
	// The real directory: v1 directory_inode is the native inode, and the
	// device and inode pair comes from that same stat.
	stat := real.Sys().(*syscall.Stat_t)
	inode, err := libraryDirectoryInode(real)
	if err != nil || inode != uint64(stat.Ino) {
		t.Fatalf("directory inode %d, %v; native %d", inode, err, stat.Ino)
	}
	if key, err := hardlinkKey(real); err != nil || key != [2]uint64{uint64(stat.Dev), uint64(stat.Ino)} {
		t.Fatalf("hardlink key %v, %v", key, err)
	}
}

func TestSameDeviceNeedsBothObservations(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if same, err := observeSameDevice(a, b); err != nil || !same || !sameDevice(a, b) {
		t.Fatalf("two temporary directories: %v, %v", same, err)
	}
	devices := stubDevices(t)
	devices.set(a, syntheticDevice(1))
	// One unobservable side is uncertainty, not a different device, but
	// sameDevice never turns it into same-volume placement.
	if _, err := observeSameDevice(a, b); !errors.Is(err, errFileObservationUnavailable) || sameDevice(a, b) {
		t.Fatalf("unobservable device: %v", err)
	}
	if _, err := observeSameDevice(filepath.Join(a, "missing"), a); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path: %v", err)
	}
	devices.set(b, syntheticDevice(2))
	if same, err := observeSameDevice(a, b); err != nil || same || sameDevice(a, b) {
		t.Fatalf("different devices: %v, %v", same, err)
	}
	devices.set(b, syntheticDevice(1))
	if !sameDevice(a, b) {
		t.Fatal("same synthetic device")
	}
	// Adoption's preserved/ plan copies rather than calling the legacy
	// archive_root already on the library's volume.
	library, root := filepath.Join(a, "library"), filepath.Join(b, "preserved")
	writeSized(t, filepath.Join(root, "receipt"), 5)
	devices.set(a, nil)
	plan, err := planPreserved(root, library)
	if err != nil || plan.SameVolume || plan.Real == "" || plan.Bytes != 5 {
		t.Fatalf("plan with an unobservable library volume: %+v, %v", plan, err)
	}
}

func TestDriveCacheRequiresAnObservedIdentity(t *testing.T) {
	quietDriveCache(t)
	t.Cleanup(func() { quietDriveCache(t) })
	dir := t.TempDir()
	config := Config{Library: true, Path: filepath.Join(dir, "library.toml")}
	devices := stubDevices(t)
	collected := func() *driveState {
		key, _, err := driveKey(config)
		if err != nil {
			t.Fatal(err)
		}
		return &driveState{identity: key, checked: time.Now()}
	}
	idle := func(step string) {
		t.Helper()
		drives.mu.Lock()
		defer drives.mu.Unlock()
		if drives.refreshing || drives.key != "" || drives.state != nil {
			t.Fatalf("%s: cache keeps %q %v (refreshing %v)", step, drives.key, drives.state, drives.refreshing)
		}
	}

	// Unobservable: no key, no state and no refresh, however often asked.
	for range 2 {
		if state, refresh, err := drives.cached(config); !errors.Is(err, errFileObservationUnavailable) || state != nil || refresh {
			t.Fatalf("unobservable drive: %v %v %v", state, refresh, err)
		}
		if drives.peek(config) != nil {
			t.Fatal("peek answered for an unobservable drive")
		}
	}
	idle("unobservable")

	// Observed: the normal cycle, and the result is reused.
	devices.set(dir, syntheticDevice(1))
	if state, refresh, err := drives.cached(config); err != nil || state != nil || !refresh {
		t.Fatalf("first observed poll: %v %v %v", state, refresh, err)
	}
	first := collected()
	drives.publish(config, first)
	if drives.peek(config) != first {
		t.Fatal("published state not visible")
	}
	drives.store(config, nil)
	if state, refresh, err := drives.cached(config); err != nil || state != first || refresh {
		t.Fatalf("fresh state not reused: %v %v %v", state, refresh, err)
	}

	// Losing the observation drops the state; observing the same device
	// again does not bring the last-known one back.
	devices.set(dir, nil)
	if drives.peek(config) != nil {
		t.Fatal("peek kept a state for an unobservable drive")
	}
	if _, _, err := drives.cached(config); err == nil {
		t.Fatal("cached answered for an unobservable drive")
	}
	idle("lost observation")
	devices.set(dir, syntheticDevice(1))
	if state, refresh, _ := drives.cached(config); state != nil || !refresh {
		t.Fatalf("last-known state revived: %v %v", state, refresh)
	}

	// The device changes during the refresh: its result describes the old
	// one, so it is published under neither, even after a poll re-keys the
	// cache to the new device while the refresh is still in flight.
	stale := collected()
	devices.set(dir, syntheticDevice(2))
	drives.publish(config, stale)
	if drives.peek(config) != nil {
		t.Fatal("result published under a changed device")
	}
	if state, refresh, err := drives.cached(config); err != nil || state != nil || refresh {
		t.Fatalf("poll during the refresh: %v %v %v", state, refresh, err)
	}
	drives.publish(config, stale)
	drives.store(config, stale)
	if drives.peek(config) != nil {
		t.Fatal("old device's result kept under the new one")
	}
	// Completion was not stranded: the next poll refreshes.
	if state, refresh, err := drives.cached(config); err != nil || state != nil || !refresh {
		t.Fatalf("after the refresh finished: %v %v %v", state, refresh, err)
	}

	// The observation fails during the refresh: polls meanwhile answer
	// nothing, the refresh still completes, and nothing is kept.
	current := collected()
	devices.set(dir, nil)
	if _, refresh, err := drives.cached(config); err == nil || refresh {
		t.Fatalf("poll while unobservable: %v %v", refresh, err)
	}
	drives.publish(config, current)
	drives.store(config, current)
	idle("refresh finished unobservable")
	devices.set(dir, syntheticDevice(2))
	if state, refresh, err := drives.cached(config); err != nil || state != nil || !refresh {
		t.Fatalf("after recovery: %v %v %v", state, refresh, err)
	}
	drives.store(config, collected())
	if state, refresh, err := drives.cached(config); err != nil || state == nil || refresh {
		t.Fatalf("recovered state not reused: %v %v %v", state, refresh, err)
	}
}

// collectDrive measures only under an observed identity, and records it.
func TestCollectDriveRecordsItsIdentity(t *testing.T) {
	quietDriveCache(t)
	dir := t.TempDir()
	config := Config{Library: true, Path: filepath.Join(dir, "library.toml"), CatalogPath: filepath.Join(dir, "catalog.sqlite3")}
	writeSized(t, config.CatalogPath, 40)
	devices := stubDevices(t)
	if state := collectDrive(t.Context(), config); state.identity != "" || state.sizes.LibraryBytes != 0 {
		t.Fatalf("unobservable drive measured: %+v", state)
	}
	devices.set(dir, syntheticDevice(3))
	devices.set(config.CatalogPath, syntheticDevice(3))
	key, _, _ := driveKey(config)
	if state := collectDrive(t.Context(), config); state.identity != key || state.sizes.CatalogBytes != 40 {
		t.Fatalf("observed drive: %+v", state)
	}
}

func TestCachedVolumeIdentityKeysOnObservedDevice(t *testing.T) {
	path := t.TempDir()
	t.Cleanup(func() {
		volumeIdentities.Lock()
		for key := range volumeIdentities.values {
			if strings.HasPrefix(key, path+"\x00") {
				delete(volumeIdentities.values, key)
			}
		}
		volumeIdentities.Unlock()
	})
	entries := func() int {
		volumeIdentities.Lock()
		defer volumeIdentities.Unlock()
		n := 0
		for key := range volumeIdentities.values {
			if strings.HasPrefix(key, path+"\x00") {
				n++
			}
		}
		return n
	}
	var lookups atomic.Int32
	var gate sync.Mutex // held to keep a lookup in flight
	previous := driveTool
	driveTool = func(context.Context, string, ...string) ([]byte, error) {
		gate.Lock()
		defer gate.Unlock()
		lookups.Add(1)
		return []byte(euclidPlist), nil
	}
	t.Cleanup(func() { driveTool = previous })
	if _, _, err := volumeMount(path); err != nil {
		t.Skip("no volume lookup to cache on this platform:", err)
	}
	devices := stubDevices(t)
	const uuid = "uuid:642C2C39-5926-4831-ABE1-34642F37B103"

	// Unobservable: every read is a fresh lookup and nothing is cached.
	for range 2 {
		if got := cachedVolumeIdentity(path); got != uuid {
			t.Fatalf("unobservable path: %q", got)
		}
	}
	if lookups.Load() != 2 || entries() != 0 {
		t.Fatalf("unobservable path: %d lookups, %d entries", lookups.Load(), entries())
	}

	// Observed: one lookup serves both reads.
	devices.set(path, syntheticDevice(1))
	cachedVolumeIdentity(path)
	cachedVolumeIdentity(path)
	if lookups.Load() != 3 || entries() != 1 {
		t.Fatalf("observed path: %d lookups, %d entries", lookups.Load(), entries())
	}

	// The device changes while a lookup is in flight: its waiter gets the
	// answer, but it is not kept for either device.
	devices.set(path, syntheticDevice(2))
	gate.Lock()
	done := make(chan string)
	go func() { done <- cachedVolumeIdentity(path) }()
	deadline := time.Now().Add(10 * time.Second)
	for entries() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("lookup never started")
		}
		time.Sleep(time.Millisecond)
	}
	devices.set(path, syntheticDevice(4))
	gate.Unlock()
	if got := <-done; got != uuid {
		t.Fatalf("waiter got %q", got)
	}
	devices.set(path, syntheticDevice(2))
	cachedVolumeIdentity(path)
	if lookups.Load() != 5 {
		t.Fatalf("a lookup made under another device was reused: %d lookups", lookups.Load())
	}
}
