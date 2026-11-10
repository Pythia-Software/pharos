package archive

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
//   - Concurrent requests for a missing value wait for one computation. Every
//     caller can cancel its wait, including the one that started it. A build
//     that no request waits for stops after derivedIdleGrace, so a reload can
//     reattach but an abandoned page does not keep a reader connection and its
//     snapshot for minutes. Builds keep their own budgets otherwise, and stop
//     on catalog close. Warm-up builds run to completion.
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
	// While the build runs, waiters counts the callers waiting for it. When
	// the last leaves, idle waits derivedIdleGrace and then abandons the build:
	// cancels it and discards whatever it returns. keep marks a build a warm-up
	// started or joined; it is never abandoned.
	waiters   int
	keep      bool
	idle      *time.Timer
	abandoned bool
}

// derivedIdleGrace is how long a build that no request waits for keeps
// running, so that a reload of the same page reattaches to it.
var derivedIdleGrace = 3 * time.Second

// leave records that a waiter stopped waiting for entry, with mu held.
func (cache *derivedCache) leave(entry *derivedEntry) {
	entry.waiters--
	if entry.waiters > 0 || entry.keep || entry.idle != nil {
		return
	}
	select {
	case <-entry.ready:
		return
	default:
	}
	var idle *time.Timer
	idle = time.AfterFunc(derivedIdleGrace, func() {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if entry.idle == idle {
			cache.abandon(entry)
		}
	})
	entry.idle = idle
}

// abandon cancels a running build nobody waits for, with mu held. Its result
// is discarded, so callers that arrive meanwhile wait for it to stop and then
// start another: one build per key at a time.
func (cache *derivedCache) abandon(entry *derivedEntry) {
	if entry.idle != nil {
		entry.idle.Stop()
		entry.idle = nil
	}
	if entry.keep || entry.waiters > 0 {
		return
	}
	if cancel := cache.builds[entry]; cancel != nil {
		entry.abandoned = true
		cancel()
	}
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
	if timing := timingFrom(ctx); timing != nil {
		// The computation is shared by every request that asks, so it is no
		// one request's own work: a request counts only how long it waited.
		started, counted, inner := time.Now(), timing.pause(), compute
		compute = func(ctx context.Context) (any, error) { return inner(withoutRequestTiming(ctx)) }
		defer func() {
			counted()
			timing.derivedValue(key, time.Since(started))
		}()
	}
	if parent, _ := ctx.Value(derivedAdmissionKey{}).(*derivedAdmission); parent != nil {
		// Do not hold a heavy slot while waiting for a shared dependency. A
		// queue of parents waiting on queued children would otherwise deadlock.
		ran := parent.lease.yield()
		ctx = context.WithValue(ctx, derivedAdmissionKey{}, (*derivedAdmission)(nil))
		value, err := c.derivedValueUnadmitted(ctx, key, compute, request)
		// The builder resumes as the work it has been: time it ran before
		// yielding still counts towards how long its kind takes.
		lease, admissionErr := parent.gate.lease(ctx, parent.kind, ran)
		if admissionErr != nil {
			return nil, admissionErr
		}
		parent.lease = lease
		return value, err
	}
	return c.derivedValueUnadmitted(ctx, key, compute, request)
}

func (c *Catalog) derivedValueUnadmitted(ctx context.Context, key string, compute func(context.Context) (any, error), request bool) (any, error) {
	if request && ctx.Value(backgroundQueryKey{}) == nil {
		defer heavyQueries.attend()()
	}
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
			return computeDerived(ctx, key, compute)
		}
		cache.version = version
		// The value is current while nothing it reads has changed, which
		// for most is less than every write (derivedKeyGeneration).
		version = cache.versions.generations.of(derivedKeyGeneration(key))
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
		cache.pruneDelegationEntries(cache.versions.generations.of(derivedKeyGeneration("delegation:")), time.Now())
		cache.pruneThinkingEntries(cache.versions.generations.of(derivedKeyGeneration("thinking:")), time.Now())
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
		if entry != nil && (entry.version != version || entry.abandoned) {
			select {
			case <-entry.ready:
			default:
				// Finish the existing build before starting the latest generation.
				// Several commits during a slow read must not launch several whole-
				// catalog scans for the same value. New callers still get fresh data.
				// A superseded build serves only the callers already waiting for it,
				// so without them it stops now.
				if entry.waiters == 0 {
					cache.abandon(entry)
				}
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
		if entry == nil || entry.version != version || entry.abandoned {
			usedAt := time.Time{}
			if entry != nil {
				usedAt = entry.usedAt
			}
			entry = &derivedEntry{version: version, ready: make(chan struct{}), compute: compute, usedAt: usedAt, keep: !request}
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
					if entry.idle != nil {
						entry.idle.Stop()
						entry.idle = nil
					}
					if isThinkingCohortKey(key) {
						entry.compute = nil
					}
					if (err != nil || entry.abandoned || cache.generation != generation) && cache.entries[key] == entry {
						delete(cache.entries, key)
					}
					close(entry.ready)
					cache.pruneDelegationEntries(cache.versions.generations.of(derivedKeyGeneration("delegation:")), time.Now())
					cache.pruneThinkingEntries(cache.versions.generations.of(derivedKeyGeneration("thinking:")), time.Now())
				}()
				value, err = computeDerived(work, key, compute)
			}()
		} else if request {
			entry.usedAt = time.Now()
		} else {
			entry.keep = true
		}
		entry.waiters++
		if entry.idle != nil {
			entry.idle.Stop()
			entry.idle = nil
		}
		cache.mu.Unlock()
		select {
		case <-entry.ready:
		case <-ctx.Done():
			cache.mu.Lock()
			cache.leave(entry)
			cache.mu.Unlock()
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

// derivedCoreFamilies are the derived values (by derivedKeyFamily) computed
// from sources, usage and tool evidence alone: no statement of theirs reads
// a label or agent-event table (queryCacheDomains), so a write to one leaves
// them current. A value that starts reading one must leave this list, or it
// would keep showing what that table held when it was computed.
var derivedCoreFamilies = map[string]bool{
	"rhythm": true, "parallelism": true, "parallelism-threads": true, "session-shape": true,
	"usage": true, "usage-hourly": true, "usage-base": true, "usage-ledger": true, "usage-roots": true, "workspace-usage": true,
	"thinking": true, "writing": true, "writing-hourly": true, "conversation-activity": true,
}

// derivedKeyGeneration is the generation (cacheGenerations.of) whose advance
// makes key's value stale.
func derivedKeyGeneration(key string) string {
	switch family := derivedKeyFamily(key); {
	case derivedCoreFamilies[family]:
		return generationCore
	case family == "delegation":
		// A workspace's snapshot counts its agent events and reads no label.
		return generationEvents
	}
	return generationAll
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

// storedGeneration is scope's generation (cacheGenerations.of) as the
// catalog counts it, for a stamp kept across restarts. It fails on a catalog
// that keeps no count.
func (c *Catalog) storedGeneration(ctx context.Context, scope string) (int64, error) {
	if _, err := c.catalogVersion(ctx); err != nil {
		return 0, err
	}
	cache := &c.derived
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cache.versions.counted {
		return 0, errors.New("catalog keeps no cached-view generation")
	}
	return cache.versions.generations.of(scope), nil
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
		cache.pruneDelegationEntries(cache.versions.generations.of(derivedKeyGeneration("delegation:")), time.Now())
		cache.pruneThinkingEntries(cache.versions.generations.of(derivedKeyGeneration("thinking:")), time.Now())
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
	c.recomputeStaleDerived(ctx, nil, nil)
}

// derivedKeyDay finds the calendar day a key was computed for.
var derivedKeyDay = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// recomputeStaleDerived recomputes the derived values requested recently
// that a change has left behind, such as the Usage rows, so that a request
// finds them ready instead of computing them itself. wanted, when given,
// selects the generations (derivedKeyGeneration) whose values to recompute,
// and proceed is asked before each value whether to go on. It reports
// whether every selected value was recomputed.
func (c *Catalog) recomputeStaleDerived(ctx context.Context, wanted func(generation string) bool, proceed func() bool) bool {
	today := c.clock().In(time.Local).Format("2006-01-02")
	cache := &c.derived
	cache.mu.Lock()
	stale := map[string]func(context.Context) (any, error){}
	for key, entry := range cache.entries {
		if strings.HasPrefix(key, "delegation:") || strings.HasPrefix(key, "machine-time:") || isThinkingCohortKey(key) {
			continue
		}
		// A value computed for another day is not asked for again.
		if day := derivedKeyDay.FindString(key); day != "" && day != today {
			continue
		}
		generation := derivedKeyGeneration(key)
		if wanted != nil && !wanted(generation) {
			continue
		}
		if entry.version != cache.versions.generations.of(generation) && time.Since(entry.usedAt) < recentUse {
			stale[key] = entry.compute
		}
	}
	cache.mu.Unlock()
	for key, compute := range stale {
		if ctx.Err() != nil || proceed != nil && !proceed() {
			return false
		}
		if _, err := c.derivedValue(ctx, key, compute, false); err != nil && ctx.Err() != nil {
			return false
		}
	}
	return true
}

// settledGenerations reads the catalog's generations for work that follows
// changes, without the side effects of a request's read: a failed read
// leaves the cache as it is (the next request finds the failure itself),
// and nothing is reported while an ingest runs, when the version read is
// the one from before it (cacheVersion).
func (c *Catalog) settledGenerations(ctx context.Context) (cacheGenerations, bool) {
	if c.ingestRuns.Load() > 0 {
		return cacheGenerations{}, false
	}
	cache := &c.derived
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.closed || cache.fault != nil {
		return cacheGenerations{}, false
	}
	if _, err := c.cacheVersion(ctx, &cache.versions); err != nil {
		return cacheGenerations{}, false
	}
	return cache.versions.generations, true
}

func isThinkingCohortKey(key string) bool {
	return strings.HasPrefix(key, "thinking:fit:") || strings.HasPrefix(key, "thinking:selection:") || strings.HasPrefix(key, "thinking:validation:")
}
