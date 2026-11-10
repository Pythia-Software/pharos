package archive

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
)

// SQLite reports extended busy and locked codes with the primary code in the
// low byte. Retry only these conflicts, never arbitrary database failures.
func catalogBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code()&0xff == 5 || sqliteErr.Code()&0xff == 6)
}

const catalogWriteAttempts = 3

// retryCatalogWrite retries a whole atomic operation. The caller must roll
// back a failed transaction before returning, and must not count its result
// until this function succeeds.
func (c *Catalog) retryCatalogWrite(ctx context.Context, operation string, waiting func(), run func() error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := run()
		if err == nil {
			return nil
		}
		if !catalogBusy(err) || attempt >= catalogWriteAttempts {
			if catalogBusy(err) {
				return fmt.Errorf("catalog writer unavailable during %s after %d attempts: %w; %s", operation, attempt, err, c.writerSummary())
			}
			return err
		}
		delay := time.Duration(attempt*150+rand.Intn(150)) * time.Millisecond
		fmt.Fprintf(os.Stderr, "Catalog write busy: operation=%s attempt=%d/%d retry_in=%s %s: %v\n", operation, attempt, catalogWriteAttempts, delay, c.writerSummary(), err)
		if waiting != nil {
			waiting()
		}
		timer := time.NewTimer(delay)
		resume := opFrom(ctx).waitFor("catalog-busy", "the catalog writer")
		select {
		case <-ctx.Done():
			timer.Stop()
			resume()
			return ctx.Err()
		case <-timer.C:
		}
		resume()
	}
}

type catalogWriter struct {
	label string
	start time.Time
}

type catalogWriteTracker struct {
	mu     sync.Mutex
	next   uint64
	active map[uint64]catalogWriter
	recent []string
	gate   chan struct{}
	// waiting counts writers blocked in acquireWriteGate, so the journal's
	// low-priority lane can give the gate back to them (§6.2).
	waiting atomic.Int32
	// holder is who has the gate, with an empty label while it is free.
	holder catalogWriter
	// waiters are the writers queued for the gate. Each release adds the
	// time its holder kept a waiter waiting, so a wait can name who it was
	// behind even though they are gone by the time it ends.
	waiters map[uint64]*writeWaiter
}

type writeWaiter struct {
	label string
	// request is the request the write belongs to, or nil for background work.
	request *requestTiming
	since   time.Time
	blocked map[string]time.Duration
}

// writeWait is what a writer's turn at the gate cost it.
type writeWait struct {
	waited time.Duration
	// behind lists the holders that kept it waiting, longest first.
	behind string
	// ahead is how many other writers were queued when it joined.
	ahead int
}

const unnamedWriter = "an unnamed writer"

// contentionReportAfter is how long an interactive request waits, for the
// writer or for its answer, before the process's state is written to a
// contention report.
var contentionReportAfter = 5 * time.Second

func (tracker *catalogWriteTracker) writeGate() chan struct{} {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.gate == nil {
		tracker.gate = make(chan struct{}, 1)
	}
	return tracker.gate
}

// hold records label as the gate's holder and returns its release.
func (tracker *catalogWriteTracker) hold(label string, gate chan struct{}) func() {
	tracker.mu.Lock()
	tracker.holder = catalogWriter{label: label, start: time.Now()}
	tracker.mu.Unlock()
	return func() {
		tracker.mu.Lock()
		tracker.chargeWaiters(time.Now())
		tracker.holder = catalogWriter{}
		tracker.mu.Unlock()
		<-gate
	}
}

// chargeWaiters adds, to each waiter, the time the holder has kept it
// waiting since it was last charged. Call it with tracker.mu held.
func (tracker *catalogWriteTracker) chargeWaiters(now time.Time) {
	held := tracker.holder
	if held.label == "" {
		return
	}
	for _, waiter := range tracker.waiters {
		from := held.start
		if waiter.since.After(from) {
			from = waiter.since
		}
		if now.After(from) {
			waiter.blocked[held.label] += now.Sub(from)
		}
	}
	tracker.holder.start = now
}

// behind lists up to three of the holders a waiter waited for, longest first.
func (waiter *writeWaiter) behind() string {
	labels := make([]string, 0, len(waiter.blocked))
	for label := range waiter.blocked {
		labels = append(labels, label)
	}
	slices.SortFunc(labels, func(a, b string) int {
		return cmp.Or(cmp.Compare(waiter.blocked[b], waiter.blocked[a]), strings.Compare(a, b))
	})
	parts := []string{}
	for _, label := range labels[:min(len(labels), 3)] {
		parts = append(parts, fmt.Sprintf("%s %s", label, waiter.blocked[label].Round(time.Millisecond)))
	}
	if len(labels) > 3 {
		parts = append(parts, fmt.Sprintf("%d more", len(labels)-3))
	}
	return strings.Join(parts, ", ")
}

// acquireWriteGate queues this process's tracked writers for the catalog
// writer in arrival order. SQLite's busy handler polls with growing sleeps
// rather than queueing, so a loop that commits and begins again at once (the
// Library refresh after an update marks every workspace dirty) takes the lock
// back before a waiting writer's next poll, and that writer fails with
// SQLITE_BUSY after busy_timeout. A Go channel hands its slot to the longest
// waiter. Writers in other processes still meet only busy_timeout.
func (c *Catalog) acquireWriteGate(ctx context.Context) (func(), error) {
	release, _, err := c.queueWriteGate(ctx, unnamedWriter)
	return release, err
}

// queueWriteGate is acquireWriteGate for a writer named label, and reports
// what the wait cost and who it was behind, also when it gave up.
func (c *Catalog) queueWriteGate(ctx context.Context, label string) (func(), writeWait, error) {
	tracker := &c.writers
	gate := tracker.writeGate()
	select {
	case gate <- struct{}{}:
		return tracker.hold(label, gate), writeWait{}, nil
	default:
	}
	request := timingFrom(ctx)
	waiter := &writeWaiter{label: label, request: request, since: time.Now(), blocked: map[string]time.Duration{}}
	tracker.mu.Lock()
	if tracker.waiters == nil {
		tracker.waiters = map[uint64]*writeWaiter{}
	}
	tracker.next++
	id := tracker.next
	wait := writeWait{ahead: len(tracker.waiters)}
	tracker.waiters[id] = waiter
	tracker.mu.Unlock()
	tracker.waiting.Add(1)
	request.writerQueued(waiter.since)
	resume := opFrom(ctx).waitFor("catalog-busy", "the catalog writer")
	// Someone is waiting on a request: if its write is still queued after
	// contentionReportAfter, say what the process is doing.
	var report <-chan time.Time
	if request != nil {
		timer := time.NewTimer(contentionReportAfter)
		defer timer.Stop()
		report = timer.C
	}
	var release func()
	var err error
	for release == nil && err == nil {
		select {
		case gate <- struct{}{}:
			release = tracker.hold(label, gate)
		case <-ctx.Done():
			err = ctx.Err()
		case <-report:
			report = nil
			c.contentionReport(fmt.Sprintf("%s (%s) has waited %s for the catalog writer", request.label(), label, elapsedText(time.Since(waiter.since))), time.Now())
		}
	}
	resume()
	tracker.waiting.Add(-1)
	tracker.mu.Lock()
	delete(tracker.waiters, id)
	if err != nil {
		// Whoever holds the gate now is who this writer gave up on.
		now := time.Now()
		if held := tracker.holder; held.label != "" && now.After(waiter.since) {
			from := held.start
			if waiter.since.After(from) {
				from = waiter.since
			}
			waiter.blocked[held.label] += now.Sub(from)
		}
	}
	wait.behind = waiter.behind()
	tracker.mu.Unlock()
	wait.waited = time.Since(waiter.since)
	request.writerWaited(wait.waited, wait.behind)
	return release, wait, err
}

// tryWriteGate takes the writer gate only if it is free at once: the
// journal's low-priority lane never queues ahead of real work.
func (c *Catalog) tryWriteGate() (func(), bool) {
	tracker := &c.writers
	gate := tracker.writeGate()
	select {
	case gate <- struct{}{}:
		return tracker.hold("a low-priority write", gate), true
	default:
		return nil, false
	}
}

// writersWaiting reports tracked writers queued for the gate.
func (c *Catalog) writersWaiting() bool { return c.writers.waiting.Load() > 0 }

// writerActive reports a tracked write transaction in flight in this process.
func (c *Catalog) writerActive() bool {
	c.writers.mu.Lock()
	defer c.writers.mu.Unlock()
	return len(c.writers.active) > 0
}

func (c *Catalog) trackWriter(label string) func() {
	tracker := &c.writers
	tracker.mu.Lock()
	if tracker.active == nil {
		tracker.active = make(map[uint64]catalogWriter)
	}
	tracker.next++
	id := tracker.next
	started := time.Now()
	tracker.active[id] = catalogWriter{label: label, start: started}
	tracker.mu.Unlock()
	return func() {
		elapsed := time.Since(started)
		tracker.mu.Lock()
		delete(tracker.active, id)
		if elapsed >= time.Second {
			entry := fmt.Sprintf("%s held %s", label, elapsed.Round(time.Millisecond))
			tracker.recent = append(tracker.recent, entry)
			if len(tracker.recent) > 4 {
				tracker.recent = tracker.recent[len(tracker.recent)-4:]
			}
		}
		tracker.mu.Unlock()
		if elapsed >= time.Second {
			fmt.Fprintf(os.Stderr, "Catalog writer: operation=%s pid=%d held=%s\n", label, os.Getpid(), elapsed.Round(time.Millisecond))
		}
	}
}

// beginTrackedWrite measures acquisition separately from lock hold time. The
// active list covers this process; an absent holder can be in another process
// or on another Mac sharing the catalog.
func (c *Catalog) beginTrackedWrite(ctx context.Context, label string) (*sql.Tx, func(), error) {
	return c.beginTrackedWriteUsing(ctx, label, c.DB)
}

// A retained connection can prepare TEMP data before acquiring the writer.
func (c *Catalog) beginTrackedWriteUsing(ctx context.Context, label string, db catalogTransactionStarter) (*sql.Tx, func(), error) {
	started := time.Now()
	request := timingFrom(ctx)
	release, wait, err := c.queueWriteGate(ctx, label)
	var tx *sql.Tx
	var sqliteWait time.Duration
	if err == nil {
		// Past the gate, only a writer outside this process's queue (another
		// process, another Mac, an untracked statement) can hold SQLite's lock.
		resume := opFrom(ctx).waitFor("catalog-busy", "the catalog writer")
		counted := request.pause()
		began := time.Now()
		tx, err = beginCatalogWrite(ctx, db)
		sqliteWait = time.Since(began)
		counted()
		resume()
		if sqliteWait >= 100*time.Millisecond {
			request.writerWaited(sqliteWait, "a writer outside this process's queue")
		}
	}
	waited := time.Since(started)
	if waited >= time.Second || err != nil {
		class := "background"
		if op := opFrom(ctx); op != nil {
			class = "background:" + op.spec.Kind
		}
		if request != nil {
			class = fmt.Sprintf("request:%q", request.label())
		}
		fmt.Fprintf(os.Stderr, "Catalog writer acquisition: operation=%s pid=%d waited=%s for=%s queue_wait=%s behind=[%s] queued_ahead=%d sqlite_wait=%s %s error=%v\n",
			label, os.Getpid(), waited.Round(time.Millisecond), class, wait.waited.Round(time.Millisecond), wait.behind, wait.ahead, sqliteWait.Round(time.Millisecond), c.writerSummary(), err)
	}
	if err != nil {
		if release != nil {
			release()
		}
		return nil, nil, err
	}
	untrack := c.trackWriter(label)
	return tx, func() { untrack(); release() }, nil
}

// writeTransaction runs one atomic unit of work in a tracked write
// transaction, retrying the whole unit when the catalog stays busy. run must
// not keep results from a failed attempt.
func (c *Catalog) writeTransaction(ctx context.Context, label string, run func(tx *sql.Tx) error) error {
	return c.retryCatalogWrite(ctx, label, nil, func() error {
		tx, finish, err := c.beginTrackedWrite(ctx, label)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(); finish() }()
		if err := run(tx); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (c *Catalog) writerSummary() string {
	tracker := &c.writers
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	active := make([]string, 0, len(tracker.active))
	for _, writer := range tracker.active {
		active = append(active, fmt.Sprintf("%s (%s)", writer.label, time.Since(writer.start).Round(time.Millisecond)))
	}
	if len(active) == 0 {
		active = append(active, "none tracked in this process; an untracked operation, another process, or another Mac may hold the lock")
	}
	return fmt.Sprintf("catalog=%s pid=%d active_writers=[%s] recent_long_writers=[%s]", c.Path, os.Getpid(), strings.Join(active, ", "), strings.Join(tracker.recent, ", "))
}
