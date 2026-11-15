package archive

import (
	"bytes"
	"cmp"
	"database/sql"
	"fmt"
	"os"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"time"
)

// A slow-request line says where one request's time went once it is over.
// What it cannot say is what everything else was doing meanwhile, and by the
// time someone reads the log that is gone. So when an interactive request has
// waited contentionReportAfter, for the catalog writer or for its answer,
// the process writes down its state at that moment: the requests in flight,
// who holds the writer and who is queued for it, the reads open on the
// catalog, the running operations, the pools, the WAL, and every goroutine's
// stack. Reports sit beside the WAL pin reports and are bounded in number
// and in frequency. Nothing is cancelled.

var (
	// contentionReportEvery bounds how often a report is written.
	contentionReportEvery = 5 * time.Minute
	// contentionReportsKept bounds the reports kept; older ones are removed.
	contentionReportsKept = 20
)

const contentionReportPrefix = "catalog-contention-"

type contentionReports struct {
	mu   sync.Mutex
	last time.Time
}

// contentionReport writes a report for reason unless one was written within
// contentionReportEvery, and returns its path, or "".
func (c *Catalog) contentionReport(reason string, now time.Time) string {
	reports := &c.contention
	reports.mu.Lock()
	if !reports.last.IsZero() && now.Sub(reports.last) < contentionReportEvery {
		reports.mu.Unlock()
		return ""
	}
	reports.last = now
	reports.mu.Unlock()
	path, err := writeDiagnosticReport(contentionReportPrefix, contentionReportsKept, now, c.contentionReportBody(reason, now))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Catalog contention: reason=%q pid=%d could not write the report: %v\n", reason, os.Getpid(), err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "Catalog contention: reason=%q pid=%d %s report=%s\n", reason, os.Getpid(), c.writerQueue(now), path)
	return path
}

// writerQueue names the gate's holder and the writers queued behind it.
func (c *Catalog) writerQueue(now time.Time) string {
	tracker := &c.writers
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	holder := "none"
	if tracker.holder.label != "" {
		holder = fmt.Sprintf("%s (%s)", tracker.holder.label, now.Sub(tracker.holder.start).Round(time.Millisecond))
	}
	waiters := make([]*writeWaiter, 0, len(tracker.waiters))
	for _, waiter := range tracker.waiters {
		waiters = append(waiters, waiter)
	}
	slices.SortFunc(waiters, func(a, b *writeWaiter) int { return a.since.Compare(b.since) })
	queued := []string{}
	for _, waiter := range waiters {
		entry := fmt.Sprintf("%s (%s)", waiter.label, now.Sub(waiter.since).Round(time.Millisecond))
		if waiter.request != nil {
			entry = fmt.Sprintf("%s for %q (%s)", waiter.label, waiter.request.label(), now.Sub(waiter.since).Round(time.Millisecond))
		}
		queued = append(queued, entry)
	}
	return fmt.Sprintf("writer_gate_holder=[%s] writers_queued=[%s]", holder, strings.Join(queued, ", "))
}

func (c *Catalog) contentionReportBody(reason string, now time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Catalog contention\nreason: %s\ncatalog: %s\npid: %d\ntime: %s\n\n", reason, c.Path, os.Getpid(), now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Catalog writer: %s\n%s\n\n", c.writerQueue(now), c.writerSummary())
	if info, err := os.Stat(c.Path + "-wal"); err == nil {
		fmt.Fprintf(&b, "WAL: %d bytes", info.Size())
		if pin := c.walPinned(); pin != nil {
			fmt.Fprintf(&b, "; pinned: %s", pin)
		}
		b.WriteString("\n")
	}
	if c.DB != nil {
		fmt.Fprintf(&b, "Pool catalog: %s\n", poolText(c.DB.Stats()))
		if c.reads != nil {
			fmt.Fprintf(&b, "Pool reader: %s\n", poolText(c.reads.Stats()))
		}
	}
	requests := requestsInFlight(now)
	fmt.Fprintf(&b, "\nRequests in flight, oldest first: %d\n", len(requests))
	for _, request := range requests {
		fmt.Fprintf(&b, "%s\n", request)
	}
	views := []opView{}
	for _, op := range c.ops.running() {
		views = append(views, op.view())
	}
	slices.SortFunc(views, func(a, b opView) int {
		return cmp.Or(a.Started.Compare(b.Started), strings.Compare(string(a.ID), string(b.ID)))
	})
	fmt.Fprintf(&b, "\nOperations, oldest first: %d\n", len(views))
	for _, view := range views {
		line := fmt.Sprintf("%s %s for %s", view.Spec.Kind, view.State, now.Sub(view.Started).Round(time.Second))
		if view.Reason != "" {
			line += " (" + view.Reason + ")"
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	reads := catalogReads(c.Path, now)
	fmt.Fprintf(&b, "\nReads open on this catalog in this process, oldest first: %d\n\n", len(reads))
	for _, read := range reads {
		fmt.Fprintf(&b, "%s\nbegan: %s\n", read.summary(now), read.Began.UTC().Format(time.RFC3339Nano))
		if read.Statement != "" {
			fmt.Fprintf(&b, "statement: %s\n", clipText(strings.Join(strings.Fields(read.Statement), " "), 2000))
		}
		fmt.Fprintf(&b, "%s\n", read.traceback())
	}
	b.WriteString("Goroutines:\n\n")
	if err := pprof.Lookup("goroutine").WriteTo(&b, 2); err != nil {
		fmt.Fprintf(&b, "(%v)\n", err)
	}
	return b.Bytes()
}

func poolText(stats sql.DBStats) string {
	return fmt.Sprintf("%d of %d connections in use, %d idle; %d waits totalling %s since the catalog opened", stats.InUse, stats.MaxOpenConnections, stats.Idle, stats.WaitCount, stats.WaitDuration.Round(time.Millisecond))
}
