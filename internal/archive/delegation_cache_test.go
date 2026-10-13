package archive

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegationCacheBoundsCompletedSnapshotsAndRefreshesLRU(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	calls := map[string]int{}
	read := func(key string) {
		t.Helper()
		value, err := cachedValue(ctx, catalog, key, func(context.Context) (int, error) {
			calls[key]++
			return 42, nil
		})
		if err != nil || value != 42 {
			t.Fatalf("read %s: %d, %v", key, value, err)
		}
	}
	read("health:answer")
	for i := range delegationCacheLimit {
		key := fmt.Sprintf("delegation:%d", i)
		read(key)
		catalog.derived.mu.Lock()
		catalog.derived.entries[key].usedAt = time.Now().Add(time.Duration(i-delegationCacheLimit) * time.Second)
		catalog.derived.mu.Unlock()
	}
	read("delegation:0") // Touch the oldest before adding another workspace.
	read(fmt.Sprintf("delegation:%d", delegationCacheLimit))
	catalog.derived.mu.Lock()
	_, evicted := catalog.derived.entries["delegation:1"]
	_, retained := catalog.derived.entries["delegation:0"]
	catalog.derived.mu.Unlock()
	if evicted || !retained {
		t.Fatal("did not evict the least recently requested workspace")
	}
	read("delegation:0")
	read("delegation:1")
	if calls["delegation:0"] != 1 || calls["delegation:1"] != 2 {
		t.Fatalf("retained/reloaded computations: %v", calls)
	}
	for i := delegationCacheLimit + 1; i < 20; i++ {
		read(fmt.Sprintf("delegation:%d", i))
	}
	read("health:answer")
	catalog.derived.mu.Lock()
	defer catalog.derived.mu.Unlock()
	count := 0
	for key := range catalog.derived.entries {
		if strings.HasPrefix(key, "delegation:") {
			count++
		}
	}
	if count != delegationCacheLimit || calls["health:answer"] != 1 {
		t.Fatalf("retained %d snapshots; unrelated cache calls %d", count, calls["health:answer"])
	}
}

func TestDelegationCacheExpiresWithoutAnotherCommit(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	calls := 0
	compute := func(context.Context) (int, error) { calls++; return 42, nil }
	read := func() {
		t.Helper()
		if _, err := cachedValue(ctx, catalog, "delegation:idle", compute); err != nil {
			t.Fatal(err)
		}
	}
	age := func() {
		catalog.derived.mu.Lock()
		catalog.derived.entries["delegation:idle"].usedAt = time.Now().Add(-delegationCacheIdle)
		catalog.derived.mu.Unlock()
	}
	read()
	version, err := catalog.catalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog.quietVersion, catalog.warmVersion = version, version
	age()
	catalog.warmCaches(ctx) // Expire even though the warming version is unchanged.
	catalog.derived.mu.Lock()
	_, retained := catalog.derived.entries["delegation:idle"]
	catalog.derived.mu.Unlock()
	if retained || calls != 1 {
		t.Fatal("idle maintenance retained or recomputed an expired snapshot")
	}
	read()
	age()
	read() // Requests also expire entries, without waiting for maintenance.
	if calls != 3 {
		t.Fatalf("expired snapshots computed %d times, want 3", calls)
	}
}

func TestDelegationCacheDoesNotWarmAfterCommit(t *testing.T) {
	catalog, _ := libraryFixture(t)
	other := otherProcess(t, catalog)
	ctx := context.Background()
	var snapshotCalls, globalCalls int
	snapshot := func(context.Context) (int, error) { snapshotCalls++; return 42, nil }
	global := func(context.Context) (int, error) { globalCalls++; return 1, nil }
	for _, key := range []string{"delegation:one", "delegation:two"} {
		if _, err := cachedValue(ctx, catalog, key, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cachedValue(ctx, catalog, "health:answer", global); err != nil {
		t.Fatal(err)
	}
	version, err := catalog.catalogVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog.quietVersion, catalog.warmVersion = version, version
	if _, err := other.DB.Exec(`UPDATE workspaces SET title='Renamed' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	catalog.warmCaches(ctx)
	catalog.warmCaches(ctx)
	if snapshotCalls != 2 || globalCalls != 2 {
		t.Fatalf("snapshot/global computations after warming: %d/%d, want 2/2", snapshotCalls, globalCalls)
	}
	catalog.derived.mu.Lock()
	for key := range catalog.derived.entries {
		if strings.HasPrefix(key, "delegation:") {
			t.Errorf("outdated snapshot retained: %s", key)
		}
	}
	catalog.derived.mu.Unlock()
	if _, err := cachedValue(ctx, catalog, "delegation:one", snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshotCalls != 3 {
		t.Fatal("the next request did not rebuild its snapshot")
	}
}

func TestDelegationCacheKeepsInFlightComputationSharedDuringEviction(t *testing.T) {
	catalog, _ := libraryFixture(t)
	ctx := context.Background()
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	compute := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return 42, nil
	}
	results := make(chan error, 2)
	read := func() {
		value, err := cachedValue(ctx, catalog, "delegation:pending", compute)
		if err == nil && value != 42 {
			err = fmt.Errorf("unexpected value: %d", value)
		}
		results <- err
	}
	go read()
	<-started
	for i := range delegationCacheLimit + 2 {
		if _, err := cachedValue(ctx, catalog, fmt.Sprintf("delegation:%d", i), func(context.Context) (int, error) { return i, nil }); err != nil {
			t.Fatal(err)
		}
	}
	catalog.derived.mu.Lock()
	pending := catalog.derived.entries["delegation:pending"]
	if pending == nil {
		catalog.derived.mu.Unlock()
		t.Fatal("an in-flight computation was evicted")
	}
	pending.usedAt = time.Time{}
	catalog.derived.mu.Unlock()
	go read()
	deadline := time.After(5 * time.Second)
	for {
		catalog.derived.mu.Lock()
		joined := !pending.usedAt.IsZero()
		catalog.derived.mu.Unlock()
		if calls.Load() != 1 {
			t.Fatal("concurrent request recomputed the pending snapshot")
		}
		if joined {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second request did not join the pending computation")
		case <-time.After(time.Millisecond):
		}
	}
	unblock()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	catalog.derived.mu.Lock()
	defer catalog.derived.mu.Unlock()
	if len(catalog.derived.entries) != delegationCacheLimit {
		t.Fatal("completed computations did not restore the cache bound")
	}
}

func TestDelegationCacheOldCompletionKeepsNewerSnapshots(t *testing.T) {
	catalog, _ := libraryFixture(t)
	other := otherProcess(t, catalog)
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	done := make(chan error, 1)
	go func() {
		_, err := cachedValue(ctx, catalog, "delegation:old", func(context.Context) (int, error) {
			close(started)
			<-release
			return 42, nil
		})
		done <- err
	}()
	<-started
	if _, err := other.DB.Exec(`UPDATE workspaces SET title='Renamed' WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	read := func() {
		t.Helper()
		if _, err := cachedValue(ctx, catalog, "delegation:new", func(context.Context) (int, error) { calls++; return 1, nil }); err != nil {
			t.Fatal(err)
		}
	}
	read()
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	catalog.derived.mu.Lock()
	_, retained := catalog.derived.entries["delegation:old"]
	catalog.derived.mu.Unlock()
	if retained {
		t.Fatal("outdated in-flight completion remained cached")
	}
	read()
	if calls != 1 {
		t.Fatal("an outdated completion evicted the newer snapshot")
	}
}
