package archive

import (
	"container/list"
	"context"
	"sync"
)

// SQLite's darwin build shares a page-cache mutex across connections. More
// simultaneous scans increase contention, so admit three heavy executions per
// process and leave the rest of the eight-connection reader pool for light reads.
// This is a driver constraint, not a user preference.
var heavyQueries = &queryAdmission{limit: 3}

// queryAdmission admits queued work in arrival order. Waiting never leases a
// database connection, and cancellation removes a waiter immediately.
type queryAdmission struct {
	mu     sync.Mutex
	limit  int
	active int
	wait   list.List
}

type queryWaiter struct {
	ready   chan struct{}
	granted bool
}

func (a *queryAdmission) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.active < a.limit && a.wait.Len() == 0 {
		a.active++
		a.mu.Unlock()
	} else {
		w := &queryWaiter{ready: make(chan struct{})}
		e := a.wait.PushBack(w)
		a.mu.Unlock()
		select {
		case <-w.ready:
		case <-ctx.Done():
			a.mu.Lock()
			if w.granted {
				a.releaseLocked()
			} else {
				a.wait.Remove(e)
			}
			a.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.releaseLocked()
		})
	}
	// If cancellation raced with a grant, give the slot back before returning.
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (a *queryAdmission) releaseLocked() {
	if e := a.wait.Front(); e != nil {
		a.wait.Remove(e)
		w := e.Value.(*queryWaiter)
		w.granted = true
		close(w.ready)
	} else {
		a.active--
	}
}

type derivedAdmissionKey struct{}

// A derived builder owns its lease on its goroutine. When it asks for another
// cached value, derivedValue yields this slot until the dependency is ready.
// Nested builders get their own leases: an independent shared build can outlive
// its waiter and must not inherit a slot that the waiter can release.
type derivedAdmission struct {
	gate    *queryAdmission
	release func()
}

func computeDerived(ctx context.Context, compute func(context.Context) (any, error)) (any, error) {
	a := &derivedAdmission{gate: heavyQueries}
	var err error
	a.release, err = a.gate.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { a.release() }()
	return compute(context.WithValue(ctx, derivedAdmissionKey{}, a))
}
