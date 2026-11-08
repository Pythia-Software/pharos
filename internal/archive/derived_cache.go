package archive

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Several pages show data derived from the whole catalog: the Settings health
// cards, the Usage table (every session's usage per day and model, priced),
// the Writing and TL1 tables, and the per-workspace costs behind the Library's
// cost columns. Each takes up to a couple of seconds to compute on a large
// catalog, and a page asks for it several times at once (rows, chart,
// metrics), so derivedCache keeps each value until its source generation changes
// (see dataVersionMonitor). Projection, audit and progress writes do not evict it:
//
//   - A key names the value and whatever else it depends on, such as the local
//     date for costs at today's prices.
//   - Concurrent requests for a missing value wait for one computation. It
//     can finish after the initiating request is abandoned. Every caller can
//     cancel its wait; builders keep their own budgets and stop on catalog close.
//   - After source changes, warmCaches recomputes the values pages used recently,
//     so the next visit does not wait.
//
// Cached values are shared between requests and must never be modified.
// Workspace delegation snapshots use a bounded, idle-expiring LRU and are
// rebuilt on demand rather than warmed after commits.
type derivedCache struct {
	mu       sync.Mutex
	versions dataVersionMonitor
	// generation advances whenever the monitor reconnects, so a value
	// computed against the previous connection's versions is not stored.
	generation int64
	// version is the latest observation on this monitor, including reads that
	// happen while a computation is running outside mu.
	version int64
	entries map[string]*derivedEntry
	// Builds have a lifetime independent of any one waiter. Close
	// cancels and joins them before the catalog's connections are closed.
	builds  map[*derivedEntry]context.CancelFunc
	workers sync.WaitGroup
	closed  bool
	// A service exits immediately on mapped-memory faults, even after every
	// waiter has left. Direct MCP calls receive the original panic instead.
	onFault func(any)
	fault   any
}

type derivedEntry struct {
	version int64
	ready   chan struct{} // closed once value and err are set
	value   any
	err     error
	fault   any
	// compute and usedAt let warmCaches recompute a value still in use.
	compute func(context.Context) (any, error)
	usedAt  time.Time
}

// recentUse is how long after its last request a value is kept warm.
const recentUse = 30 * time.Minute

const (
	delegationCacheLimit = 4
	delegationCacheIdle  = 5 * time.Minute
)

// pruneDelegationEntries runs with mu held. Pending computations remain shared
// by their callers, even if they temporarily exceed the completed-value limit.
// Removing a completed entry does not invalidate callers holding its snapshot.
func (cache *derivedCache) pruneDelegationEntries(version int64, now time.Time) {
	completed := map[string]*derivedEntry{}
	for key, entry := range cache.entries {
		if !strings.HasPrefix(key, "delegation:") {
			continue
		}
		select {
		case <-entry.ready:
		default:
			continue
		}
		if entry.version != version || now.Sub(entry.usedAt) >= delegationCacheIdle {
			delete(cache.entries, key)
			continue
		}
		completed[key] = entry
	}
	for len(completed) > delegationCacheLimit {
		oldest := ""
		for key, entry := range completed {
			if oldest == "" || entry.usedAt.Before(completed[oldest].usedAt) {
				oldest = key
			}
		}
		delete(cache.entries, oldest)
		delete(completed, oldest)
	}
}

// Thinking shares one full population. Bound its fits, native selections and
// numeric validation separately to four completed cohorts each. Pending work
// remains shared; old generations and idle cohorts are discarded.
func (cache *derivedCache) pruneThinkingEntries(version int64, now time.Time) {
	for _, prefix := range []string{"thinking:fit:", "thinking:selection:", "thinking:validation:"} {
		completed := map[string]*derivedEntry{}
		for key, e := range cache.entries {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			select {
			case <-e.ready:
			default:
				continue
			}
			if e.version != version || now.Sub(e.usedAt) >= delegationCacheIdle {
				delete(cache.entries, key)
				continue
			}
			completed[key] = e
		}
		for len(completed) > 4 {
			oldest := ""
			for k, e := range completed {
				if oldest == "" || e.usedAt.Before(completed[oldest].usedAt) {
					oldest = k
				}
			}
			delete(cache.entries, oldest)
			delete(completed, oldest)
		}
	}
}

func (cache *derivedCache) close() {
	cache.closed = true
	for _, cancel := range cache.builds {
		cancel()
	}
	if cache.fault == nil {
		cache.versions.close()
	}
	cache.entries = nil
	cache.generation++
	cache.version = 0
}

func (c *Catalog) closeDerivedCache() {
	c.derived.mu.Lock()
	c.derived.close()
	c.derived.mu.Unlock()
	c.derived.workers.Wait()
	c.derived.repanicFault()
}

func (cache *derivedCache) repanicFault() {
	cache.mu.Lock()
	fault := cache.fault
	cache.mu.Unlock()
	if fault != nil {
		panic(fault) // SQLite is unusable; never close or checkpoint it after a fault.
	}
}

// cachedValue returns key's value while the catalog is unchanged, and
// otherwise computes it once for every caller that asks meanwhile.
func cachedValue[T any](ctx context.Context, c *Catalog, key string, compute func(context.Context) (T, error)) (T, error) {
	value, err := c.derivedValue(ctx, key, func(ctx context.Context) (any, error) { return compute(ctx) }, true)
	if err != nil {
		var zero T
		return zero, err
	}
	return value.(T), nil
}

// warmOnlyKey marks a context whose reads take only values already
// computed: derivedValue returns errDerivedCold rather than computing, waiting
// for a computation, or starting one. browse_site's frequent values read
// this way, so a request never blocks on or kicks off a derived rebuild.
type warmOnlyKey struct{}

var errDerivedCold = errors.New("the derived value is not computed yet")

func warmOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, warmOnlyKey{}, true)
}

func isWarmOnly(ctx context.Context) bool { return ctx.Value(warmOnlyKey{}) == true }

// derivedValue is cachedValue without a type. A request marks the value as
// used, which keeps it warm.
func (c *Catalog) derivedValue(ctx context.Context, key string, compute func(context.Context) (any, error), request bool) (any, error) {
	if parent, _ := ctx.Value(derivedAdmissionKey{}).(*derivedAdmission); parent != nil {
		// Do not hold a heavy slot while waiting for a shared dependency. A
		// queue of parents waiting on queued children would otherwise deadlock.
		parent.release()
		ctx = context.WithValue(ctx, derivedAdmissionKey{}, (*derivedAdmission)(nil))
		value, err := c.derivedValueUnadmitted(ctx, key, compute, request)
		release, admissionErr := parent.gate.acquire(ctx)
		if admissionErr != nil {
			return nil, admissionErr
		}
		parent.release = release
		return value, err
	}
	return c.derivedValueUnadmitted(ctx, key, compute, request)
}

func (c *Catalog) derivedValueUnadmitted(ctx context.Context, key string, compute func(context.Context) (any, error), request bool) (any, error) {
	// Attribution is published by writers (see ensureOrigins); publication
	// advances the generation, so values computed from older origins are
	// recomputed.
	cache := &c.derived
	for {
		if err := ctx.Err(); err != nil {
			cache.repanicFault()
			return nil, err
		}
		cache.mu.Lock()
		if cache.fault != nil {
			fault := cache.fault
			cache.mu.Unlock()
			panic(fault)
		}
		if cache.closed {
			cache.mu.Unlock()
			return nil, context.Canceled
		}
		version, err := c.cacheVersion(ctx, &cache.versions)
		if err != nil {
			cache.entries = nil
			cache.generation++
			cache.mu.Unlock()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if isWarmOnly(ctx) {
				return nil, errDerivedCold
			}
			return computeDerived(ctx, compute)
		}
		cache.version = version
		if isWarmOnly(ctx) {
			entry := cache.entries[key]
			cache.mu.Unlock()
			if entry == nil || entry.version != version {
				return nil, errDerivedCold
			}
			select {
			case <-entry.ready:
				cache.repanicFault()
				if entry.fault != nil {
					panic(entry.fault)
				}
				return entry.value, entry.err
			default:
				return nil, errDerivedCold
			}
		}
		cache.pruneDelegationEntries(version, time.Now())
		cache.pruneThinkingEntries(version, time.Now())
		// A published tool cube changes independently of the input generation.
		// Retain only its current completed Machine Time projection; in-flight
		// readers continue to own their immutable snapshots after eviction.
		if strings.HasPrefix(key, "machine-time:") {
			for previous, value := range cache.entries {
				if previous == key || !strings.HasPrefix(previous, "machine-time:") {
					continue
				}
				select {
				case <-value.ready:
					delete(cache.entries, previous)
				default:
				}
			}
		}
		// Usage keys end with their carbon assumptions (usageCarbonSuffix), which
		// change without a new generation. Keep only the current settings' copy.
		if family, _, ok := strings.Cut(key, "#carbon="); ok {
			for previous, value := range cache.entries {
				if previous == key || !strings.HasPrefix(previous, family+"#carbon=") {
					continue
				}
				select {
				case <-value.ready:
					delete(cache.entries, previous)
				default:
				}
			}
		}
		entry := cache.entries[key]
		if entry != nil && entry.version != version {
			select {
			case <-entry.ready:
			default:
				// Finish the existing build before starting the latest generation.
				// Several commits during a slow read must not launch several whole-
				// catalog scans for the same value. New callers still get fresh data.
				cache.mu.Unlock()
				select {
				case <-entry.ready:
					continue
				case <-ctx.Done():
					cache.repanicFault()
					return nil, ctx.Err()
				}
			}
		}
		if entry == nil || entry.version != version {
			usedAt := time.Time{}
			if entry != nil {
				usedAt = entry.usedAt
			}
			entry = &derivedEntry{version: version, ready: make(chan struct{}), compute: compute, usedAt: usedAt}
			if cache.entries == nil {
				cache.entries = map[string]*derivedEntry{}
			}
			cache.entries[key] = entry
			if request {
				entry.usedAt = time.Now()
			}
			generation := cache.generation
			work, cancel := context.WithCancel(context.WithoutCancel(ctx))
			onFault := cache.onFault
			if cache.builds == nil {
				cache.builds = map[*derivedEntry]context.CancelFunc{}
			}
			cache.builds[entry] = cancel
			cache.workers.Add(1)
			go func() {
				defer cache.workers.Done()
				defer cancel()
				debug.SetPanicOnFault(true)
				var value any
				var err error
				var fault any
				defer func() {
					if failure := recover(); failure != nil {
						if _, mapped := failure.(interface{ Addr() uintptr }); mapped {
							fault = failure
							// The service exits before any cleanup or cache locking can
							// touch an unusable catalog. Direct MCP relays the fault.
							if onFault != nil {
								onFault(fault)
							}
							cache.mu.Lock()
							cache.fault = fault
							for _, stop := range cache.builds {
								stop()
							}
							cache.mu.Unlock()
						} else {
							err = fmt.Errorf("building cached %s: %v", key, failure)
						}
					}
					cache.mu.Lock()
					defer cache.mu.Unlock()
					entry.value, entry.err, entry.fault = value, err, fault
					delete(cache.builds, entry)
					if isThinkingCohortKey(key) {
						entry.compute = nil
					}
					if (err != nil || cache.generation != generation) && cache.entries[key] == entry {
						delete(cache.entries, key)
					}
					close(entry.ready)
					cache.pruneDelegationEntries(cache.version, time.Now())
					cache.pruneThinkingEntries(cache.version, time.Now())
				}()
				value, err = computeDerived(work, compute)
			}()
		} else if request {
			entry.usedAt = time.Now()
		}
		cache.mu.Unlock()
		select {
		case <-entry.ready:
		case <-ctx.Done():
			cache.repanicFault()
			return nil, ctx.Err()
		}
		cache.repanicFault()
		if entry.fault != nil {
			panic(entry.fault)
		}
		return entry.value, entry.err
	}
}

// catalogVersion reads the catalog's data version on the cache's monitor.
func (c *Catalog) catalogVersion(ctx context.Context) (int64, error) {
	cache := &c.derived
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.fault != nil {
		panic(cache.fault)
	}
	version, err := c.cacheVersion(ctx, &cache.versions)
	if err != nil {
		cache.entries = nil
		cache.generation++
	} else {
		cache.version = version
	}
	return version, err
}

// warmCaches recomputes, once the catalog has been quiet for a maintenance
// cycle after a change, the Library rows and derived values requested
// recently, so the first page after a sync or an MCP call finds them ready
// rather than waiting a second or more on a cold drive. After startup it
// warms the Library, the page the app opens on, the usage behind it, and the
// Tools rollup.
func (c *Catalog) warmCaches(ctx context.Context) {
	if c.projectionBatches.Load() > 0 {
		return
	}
	version, err := c.catalogVersion(ctx)
	if err == nil {
		cache := &c.derived
		cache.mu.Lock()
		cache.pruneDelegationEntries(cache.version, time.Now())
		cache.pruneThinkingEntries(cache.version, time.Now())
		cache.mu.Unlock()
	}
	if err != nil || version != c.quietVersion {
		c.quietVersion = version
		return
	}
	if version == c.warmVersion {
		return
	}
	first := c.warmVersion == 0
	c.warmVersion = version
	// The rows keep the fields recent requests needed, such as long text.
	if usedAt, fields := c.libraryUse(); first || time.Since(usedAt) < recentUse {
		if usedAt.IsZero() {
			fields = libraryFieldsFor("title", "repository_name")
		}
		_, _ = c.cachedLibraryRows(ctx, fields)
	}
	if first {
		// Usage, the Settings pricing card, and the Library's cost columns;
		// and the Tools rollup, which an upgrade must build before the page
		// can show it.
		_, _ = c.localUsageRows(ctx)
		_, _ = c.allWorkspaceUsage(ctx)
		_ = c.currentToolRollup(ctx)
	}
	cache := &c.derived
	cache.mu.Lock()
	stale := map[string]func(context.Context) (any, error){}
	for key, entry := range cache.entries {
		if strings.HasPrefix(key, "delegation:") || strings.HasPrefix(key, "machine-time:") || isThinkingCohortKey(key) {
			continue
		}
		if entry.version != version && time.Since(entry.usedAt) < recentUse {
			stale[key] = entry.compute
		}
	}
	cache.mu.Unlock()
	for key, compute := range stale {
		if ctx.Err() != nil {
			return
		}
		_, _ = c.derivedValue(ctx, key, compute, false)
	}
}

func isThinkingCohortKey(key string) bool {
	return strings.HasPrefix(key, "thinking:fit:") || strings.HasPrefix(key, "thinking:selection:") || strings.HasPrefix(key, "thinking:validation:")
}
