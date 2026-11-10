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
	// background counts the running and waiting work that only improves
	// later answers (backgroundQuery), which interactive() leaves out.
	background int
	// attending counts requests waiting for a derived value, whoever is
	// computing it (attend).
	attending int
	// lasted is how long each kind of work took the last time it ran to
	// completion or was given up on.
	lasted map[string]time.Duration
}

type queryWaiter struct {
	ready   chan struct{}
	granted bool
	kind    string
	ran     time.Duration
	slow    bool // as admitted; set with granted
}

type backgroundQueryKey struct{}

// backgroundQuery marks ctx's work as competing with nobody: admission
// still queues and counts it, but it does not make the service look busy
// to other background work deciding whether to start.
func backgroundQuery(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundQueryKey{}, true)
}

// queryLease is one admitted execution's slot.
type queryLease struct {
	gate    *queryAdmission
	ctx     context.Context
	kind    string
	slow    bool
	ran     time.Duration // by earlier leases of the same work
	started time.Time
	once    sync.Once
	// background is 1 for work marked backgroundQuery, else 0.
	background int
}

// acquire admits work of no particular kind, which is never held back as slow.
func (a *queryAdmission) acquire(ctx context.Context) (func(), error) {
	return a.acquireKind(ctx, "")
}

// acquireKind admits work of kind, a stable name for requests expected to
// cost about the same (a dataset and the shape of its query, or a derived
// value). The returned release also records how long the work ran.
func (a *queryAdmission) acquireKind(ctx context.Context, kind string) (func(), error) {
	l, err := a.lease(ctx, kind, 0)
	if err != nil {
		return nil, err
	}
	return l.release, nil
}

// slowLimit is how many slow executions may run at once.
func (a *queryAdmission) slowLimit() int { return max(1, a.limit-1) }

// isSlow reports whether work of kind that has already run for ran counts as
// slow now, with mu held. A waiter is judged when it could start, not when it
// arrived: its kind may have been learned slow while it queued.
func (a *queryAdmission) isSlow(kind string, ran time.Duration) bool {
	return kind != "" && max(a.lasted[kind], ran) >= slowQuery
}

// admissible reports whether a waiter may start now, with mu held.
func (a *queryAdmission) admissible(slow bool) bool {
	return a.active < a.limit && (!slow || a.slow < a.slowLimit())
}

// lease admits work of kind that has already run for ran under earlier
// leases (a derived build that yielded its slot to a dependency).
func (a *queryAdmission) lease(ctx context.Context, kind string, ran time.Duration) (*queryLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l := &queryLease{gate: a, ctx: ctx, kind: kind, ran: ran}
	if ctx.Value(backgroundQueryKey{}) != nil {
		l.background = 1
	}
	a.mu.Lock()
	a.background += l.background
	l.slow = a.isSlow(kind, ran)
	if a.admissible(l.slow) && !a.blockedBehind(l.slow) {
		a.active++
		if l.slow {
			a.slow++
		}
		a.mu.Unlock()
	} else {
		w := &queryWaiter{ready: make(chan struct{}), kind: kind, ran: ran}
		e := a.wait.PushBack(w)
		a.mu.Unlock()
		// The wait is the request's own, even when it gives up.
		queued := time.Now()
		defer func() { timingFrom(ctx).queryQueued(time.Since(queued)) }()
		select {
		case <-w.ready:
			l.slow = w.slow
		case <-ctx.Done():
			a.mu.Lock()
			if w.granted {
				a.releaseLocked(w.slow)
			} else {
				a.wait.Remove(e)
			}
			a.background -= l.background
			a.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	l.started = time.Now()
	// If cancellation raced with a grant, give the slot back before returning.
	if err := ctx.Err(); err != nil {
		l.release()
		return nil, err
	}
	return l, nil
}

// release returns the slot and records how long the work ran in all.
func (l *queryLease) release() { l.end(true) }

// yield returns the slot while the work waits for something else, and
// reports how long the work has run so far, for the lease that resumes it.
// The kind's history is left alone: a part of the work is not its duration.
func (l *queryLease) yield() time.Duration { return l.end(false) }

func (l *queryLease) end(record bool) time.Duration {
	ran := l.ran
	l.once.Do(func() {
		a := l.gate
		a.mu.Lock()
		defer a.mu.Unlock()
		ran += time.Since(l.started)
		l.ran = ran
		if record && l.kind != "" {
			if a.lasted == nil || len(a.lasted) >= queryKindsRemembered {
				a.lasted = map[string]time.Duration{}
			}
			// Work given up on ran at least this long: it does not make a
			// slow kind look quick.
			lasted := ran
			if l.ctx.Err() != nil {
				lasted = max(lasted, a.lasted[l.kind])
			}
			a.lasted[l.kind] = lasted
		}
		a.releaseLocked(l.slow)
		a.background -= l.background
	})
	return ran
}

// blockedBehind reports whether an arrival must queue behind earlier
// waiters, with mu held: quick work passes only waiters that are held back
// as slow.
func (a *queryAdmission) blockedBehind(slow bool) bool {
	for e := a.wait.Front(); e != nil; e = e.Next() {
		w := e.Value.(*queryWaiter)
		if slow || !a.isSlow(w.kind, w.ran) || a.admissible(true) {
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
		w := e.Value.(*queryWaiter)
		if waits := a.isSlow(w.kind, w.ran); a.admissible(waits) {
			a.wait.Remove(e)
			a.active++
			if waits {
				a.slow++
			}
			w.granted, w.slow = true, waits
			close(w.ready)
		}
		e = next
	}
}

// busy reports whether work is waiting for a slot or none is free: a request
// that can be answered without computing should be.
func (a *queryAdmission) busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wait.Len() > 0 || a.active >= a.limit
}

// interactive is how much work is running or waiting that someone is
// waiting for: zero means that background work competes with nothing. Work
// is counted from when it asks for a slot, not while it prepares to.
func (a *queryAdmission) interactive() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active + a.wait.Len() - a.background + a.attending
}

// attend counts a request as waiting for work from now until the returned
// function is called. A request that needs a derived value may be waiting
// for a build that background work started, which holds a background
// lease: the service is not idle while someone waits for it.
func (a *queryAdmission) attend() func() {
	a.mu.Lock()
	a.attending++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.attending--
			a.mu.Unlock()
		})
	}
}

type derivedAdmissionKey struct{}

// A derived builder owns its lease on its goroutine. When it asks for another
// cached value, derivedValue yields this slot until the dependency is ready.
// Nested builders get their own leases: an independent shared build can outlive
// its waiter and must not inherit a slot that the waiter can release.
type derivedAdmission struct {
	gate  *queryAdmission
	kind  string
	lease *queryLease
}

// computeDerived runs one derived value's build under admission. key is the
// value's cache key; builds of one family (rhythm, parallelism, a workspace's
// delegation) count as one kind of work.
func computeDerived(ctx context.Context, key string, compute func(context.Context) (any, error)) (any, error) {
	a := &derivedAdmission{gate: heavyQueries, kind: "derived:" + derivedKeyFamily(key)}
	var err error
	a.lease, err = a.gate.lease(ctx, a.kind, 0)
	if err != nil {
		return nil, err
	}
	defer func() { a.lease.release() }()
	return compute(context.WithValue(ctx, derivedAdmissionKey{}, a))
}
