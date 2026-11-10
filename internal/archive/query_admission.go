package archive

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// SQLite's darwin build shares a page-cache mutex across connections. More
// simultaneous scans increase contention, so admit three heavy executions per
// process and leave the rest of the eight-connection reader pool for light reads.
// This is a driver constraint, not a user preference.
var heavyQueries = &queryAdmission{limit: 3}

// slowQuery is how long a kind of work last ran for it to count as slow.
// Slow work never takes the last free slot: a page's quick cards are then
// answered while its slowest ones (or a rebuild of derived data) run, instead
// of queueing behind them. Tests lower it.
var slowQuery = 3 * time.Second

// queryKindsRemembered bounds the kinds whose last duration is kept.
const queryKindsRemembered = 4096

// queryAdmission admits queued work in arrival order, except that work whose
// kind last ran slowly waits while it would take the last slot. Waiting never
// leases a database connection, and cancellation removes a waiter immediately.
type queryAdmission struct {
	mu     sync.Mutex
	limit  int
	active int
	slow   int // active executions admitted as slow
	wait   list.List
	// lasted is how long each kind of work took the last time it ran to
	// completion or was given up on.
	lasted map[string]time.Duration
}

type queryWaiter struct {
	ready   chan struct{}
	granted bool
	slow    bool
}

// acquire admits work of no particular kind, which is never held back as slow.
func (a *queryAdmission) acquire(ctx context.Context) (func(), error) {
	return a.acquireKind(ctx, "")
}

// slowLimit is how many slow executions may run at once.
func (a *queryAdmission) slowLimit() int { return max(1, a.limit-1) }

// admissible reports whether a waiter may start now, with mu held.
func (a *queryAdmission) admissible(slow bool) bool {
	return a.active < a.limit && (!slow || a.slow < a.slowLimit())
}

// acquireKind admits work of kind, a stable name for requests expected to
// cost about the same (a dataset and the shape of its query, or a derived
// value). The returned release also records how long the work ran.
func (a *queryAdmission) acquireKind(ctx context.Context, kind string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	slow := kind != "" && a.lasted[kind] >= slowQuery
	if a.admissible(slow) && !a.blockedBehind(slow) {
		a.active++
		if slow {
			a.slow++
		}
		a.mu.Unlock()
	} else {
		w := &queryWaiter{ready: make(chan struct{}), slow: slow}
		e := a.wait.PushBack(w)
		a.mu.Unlock()
		// The wait is the request's own, even when it gives up.
		queued := time.Now()
		defer func() { timingFrom(ctx).queryQueued(time.Since(queued)) }()
		select {
		case <-w.ready:
		case <-ctx.Done():
			a.mu.Lock()
			if w.granted {
				a.releaseLocked(slow)
			} else {
				a.wait.Remove(e)
			}
			a.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	started := time.Now()
	var once sync.Once
	release := func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if kind != "" {
				if a.lasted == nil || len(a.lasted) >= queryKindsRemembered {
					a.lasted = map[string]time.Duration{}
				}
				a.lasted[kind] = time.Since(started)
			}
			a.releaseLocked(slow)
		})
	}
	// If cancellation raced with a grant, give the slot back before returning.
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// blockedBehind reports whether an arrival must queue behind earlier
// waiters, with mu held: quick work passes only waiters that are held back
// as slow.
func (a *queryAdmission) blockedBehind(slow bool) bool {
	for e := a.wait.Front(); e != nil; e = e.Next() {
		if slow || !e.Value.(*queryWaiter).slow || a.admissible(true) {
			return true
		}
	}
	return false
}

// releaseLocked returns a slot and admits every waiter that may now start,
// oldest first.
func (a *queryAdmission) releaseLocked(slow bool) {
	a.active--
	if slow {
		a.slow--
	}
	for e := a.wait.Front(); e != nil; {
		next := e.Next()
		if w := e.Value.(*queryWaiter); a.admissible(w.slow) {
			a.wait.Remove(e)
			a.active++
			if w.slow {
				a.slow++
			}
			w.granted = true
			close(w.ready)
		}
		e = next
	}
}

type derivedAdmissionKey struct{}

// A derived builder owns its lease on its goroutine. When it asks for another
// cached value, derivedValue yields this slot until the dependency is ready.
// Nested builders get their own leases: an independent shared build can outlive
// its waiter and must not inherit a slot that the waiter can release.
type derivedAdmission struct {
	gate    *queryAdmission
	kind    string
	release func()
}

// computeDerived runs one derived value's build under admission. key is the
// value's cache key; builds of one family (rhythm, parallelism, a workspace's
// delegation) count as one kind of work.
func computeDerived(ctx context.Context, key string, compute func(context.Context) (any, error)) (any, error) {
	a := &derivedAdmission{gate: heavyQueries, kind: "derived:" + derivedKeyFamily(key)}
	var err error
	a.release, err = a.gate.acquireKind(ctx, a.kind)
	if err != nil {
		return nil, err
	}
	defer func() { a.release() }()
	return compute(context.WithValue(ctx, derivedAdmissionKey{}, a))
}
