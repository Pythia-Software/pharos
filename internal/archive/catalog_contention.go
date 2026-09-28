package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
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
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
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
}

// acquireWriteGate queues this process's tracked writers for the catalog
// writer in arrival order. SQLite's busy handler polls with growing sleeps
// rather than queueing, so a loop that commits and begins again at once (the
// Library refresh after an update marks every workspace dirty) takes the lock
// back before a waiting writer's next poll, and that writer fails with
// SQLITE_BUSY after busy_timeout. A Go channel hands its slot to the longest
// waiter. Writers in other processes still meet only busy_timeout.
func (c *Catalog) acquireWriteGate(ctx context.Context) (func(), error) {
	tracker := &c.writers
	tracker.mu.Lock()
	if tracker.gate == nil {
		tracker.gate = make(chan struct{}, 1)
	}
	gate := tracker.gate
	tracker.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
	started := time.Now()
	release, err := c.acquireWriteGate(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := c.beginWrite(ctx)
	waited := time.Since(started)
	if waited >= time.Second || err != nil {
		fmt.Fprintf(os.Stderr, "Catalog writer acquisition: operation=%s pid=%d waited=%s %s error=%v\n", label, os.Getpid(), waited.Round(time.Millisecond), c.writerSummary(), err)
	}
	if err != nil {
		release()
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
