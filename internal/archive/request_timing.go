package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A page that takes ten seconds to answer says nothing about why: it may
// have waited for the catalog writer behind a sync, for a connection from
// the read pool, for a derived value another request is computing, or for
// its own queries on a drive that background work keeps busy. requestTiming
// keeps that split for one HTTP request or MCP tool call, and a request that
// was slow or that its client gave up on leaves one line in the service's
// log naming where the time went (slowRequestLine).
//
// The line names routes, tools, operations and the code that ran the slowest
// statement. It never holds a query string, an argument or SQL text.

var (
	// slowRequestLine is how long a request runs before its breakdown is
	// logged; cancelledRequestLine is the same for one its client abandoned.
	slowRequestLine      = time.Second
	cancelledRequestLine = 250 * time.Millisecond
	// slowStatement is the least time for which a statement's caller is kept.
	slowStatement = 20 * time.Millisecond
)

type requestTiming struct {
	catalog *Catalog
	// name is the method and route, without the query string.
	name string
	// interactive marks a request someone is waiting on to see data: a page's
	// data read or an MCP tool call. Only these start a contention report.
	interactive bool
	started     time.Time
	goroutine   uint64
	pool        sql.DBStats
	// stream is set once the response is an event stream, open by design.
	stream atomic.Bool
	report *time.Timer

	mu sync.Mutex
	// tool is the MCP tool the request called, if any.
	tool string
	// paused counts reasons the request's own statements are not counted:
	// time already attributed to a wait for the writer or a derived value.
	paused       int
	statements   int
	database     time.Duration
	slowest      time.Duration
	slowestStack []uintptr
	writes       int
	writerWait   time.Duration
	// writerLongest is the longest single wait; writerBehind who held the
	// writer during it.
	writerLongest time.Duration
	writerBehind  string
	// writerSince is when the request began its current wait for the writer.
	writerSince    time.Time
	derived        int
	derivedWait    time.Duration
	derivedSlowest time.Duration
	derivedKey     string
	// queueWait is the time spent queued for a heavy-query slot (heavyQueries).
	queueWait time.Duration
}

// mcpRPCPath is the route an agent's MCP process forwards tool calls to.
const mcpRPCPath = "/api/mcp/rpc"

type requestTimingKey struct{}

// requestsTimed are the requests being answered. A statement finds its
// request through its context, or, since much of the catalog's read code
// takes none, through the goroutine that began the request.
var requestsTimed = struct {
	sync.Mutex
	active      atomic.Int32
	byGoroutine map[uint64]*requestTiming
}{byGoroutine: map[uint64]*requestTiming{}}

// goroutineID is the calling goroutine's number, from the header of its own
// stack ("goroutine 123 [running]:"). The runtime offers no other way to
// reach per-goroutine state from a database/sql driver call that was given
// context.Background.
func goroutineID() uint64 {
	var buffer [40]byte
	header := buffer[:runtime.Stack(buffer[:], false)]
	var id uint64
	for _, digit := range header[min(len("goroutine "), len(header)):] {
		if digit < '0' || digit > '9' {
			break
		}
		id = id*10 + uint64(digit-'0')
	}
	return id
}

// beginRequestTiming starts timing a request on the calling goroutine.
// The returned context carries it; finish ends it and returns what it took.
func beginRequestTiming(ctx context.Context, c *Catalog, name string, interactive bool) (context.Context, *requestTiming) {
	timing := &requestTiming{catalog: c, name: name, interactive: interactive, started: time.Now(), goroutine: goroutineID()}
	if c != nil && c.DB != nil {
		timing.pool = c.reader().Stats()
	}
	requestsTimed.Lock()
	requestsTimed.byGoroutine[timing.goroutine] = timing
	requestsTimed.active.Store(int32(len(requestsTimed.byGoroutine)))
	requestsTimed.Unlock()
	if interactive && c != nil {
		// A data read still unanswered now is what someone sees as a page
		// that never loads: capture what the process is doing while it is
		// still doing it.
		timing.report = time.AfterFunc(contentionReportAfter, func() {
			if !timing.stream.Load() {
				c.contentionReport(fmt.Sprintf("%s has run for %s", timing.label(), elapsedText(time.Since(timing.started))), time.Now())
			}
		})
	}
	return context.WithValue(ctx, requestTimingKey{}, timing), timing
}

func (t *requestTiming) finish() time.Duration {
	if t.report != nil {
		t.report.Stop()
	}
	requestsTimed.Lock()
	if requestsTimed.byGoroutine[t.goroutine] == t {
		delete(requestsTimed.byGoroutine, t.goroutine)
	}
	requestsTimed.active.Store(int32(len(requestsTimed.byGoroutine)))
	requestsTimed.Unlock()
	return time.Since(t.started)
}

// withoutRequestTiming is ctx for work a request starts but does not own,
// such as a derived value's computation that other requests share.
func withoutRequestTiming(ctx context.Context) context.Context {
	if ctx.Value(requestTimingKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, requestTimingKey{}, (*requestTiming)(nil))
}

// timingFrom returns the request ctx or the calling goroutine belongs to,
// or nil. Background operations carry their own context and are never a
// request's.
func timingFrom(ctx context.Context) *requestTiming {
	if requestsTimed.active.Load() == 0 {
		return nil
	}
	if ctx != nil {
		if value := ctx.Value(requestTimingKey{}); value != nil {
			timing, _ := value.(*requestTiming)
			return timing
		}
		if opFrom(ctx) != nil {
			return nil
		}
	}
	id := goroutineID()
	requestsTimed.Lock()
	defer requestsTimed.Unlock()
	return requestsTimed.byGoroutine[id]
}

func (t *requestTiming) setTool(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.tool = name
	t.mu.Unlock()
}

// label names the request: its route, and its MCP tool if it called one.
func (t *requestTiming) label() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tool != "" {
		return t.name + " (" + t.tool + ")"
	}
	return t.name
}

// pause stops counting the request's statements until the returned function
// runs: the time is being counted as a wait instead.
func (t *requestTiming) pause() func() {
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	t.paused++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.paused--
		t.mu.Unlock()
	}
}

// statement counts one statement that spent elapsed in SQLite. The caller of
// the slowest is kept, resolved only if the request is logged.
func (t *requestTiming) statement(elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.paused > 0 {
		return
	}
	t.statements++
	t.database += elapsed
	if elapsed > t.slowest {
		t.slowest = elapsed
		if elapsed >= slowStatement {
			stack := make([]uintptr, catalogReadFrames)
			t.slowestStack = stack[:runtime.Callers(2, stack)]
		}
	}
}

// writerQueued notes that the request began waiting for the catalog writer.
func (t *requestTiming) writerQueued(at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.writerSince = at
	t.mu.Unlock()
}

// writerWaited adds a finished wait for the writer and who it was behind.
func (t *requestTiming) writerWaited(waited time.Duration, behind string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writerSince = time.Time{}
	t.writes++
	t.writerWait += waited
	if waited > t.writerLongest && behind != "" {
		t.writerLongest, t.writerBehind = waited, behind
	}
}

// derivedKeyFamily is a derived value's kind without its parameters.
func derivedKeyFamily(key string) string {
	if end := strings.IndexAny(key, ":#"); end >= 0 {
		return key[:end]
	}
	return key
}

func (t *requestTiming) derivedValue(key string, waited time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.derived++
	t.derivedWait += waited
	if waited >= t.derivedSlowest {
		t.derivedSlowest, t.derivedKey = waited, derivedKeyFamily(key)
	}
}

// queryQueued records a wait for a heavy-query slot. A derived value's build
// queues inside the wait its request already counts as derived_wait.
func (t *requestTiming) queryQueued(waited time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.paused > 0 {
		return
	}
	t.queueWait += waited
}

// codeCaller names the innermost of stack's frames in this package's own
// code, below database/sql and the query helpers: who ran a statement.
func codeCaller(stack []uintptr) string {
	if len(stack) == 0 {
		return ""
	}
	iter := runtime.CallersFrames(stack)
	for {
		frame, more := iter.Next()
		name, file := shortFunction(frame.Function), filepath.Base(frame.File)
		if strings.HasPrefix(name, "archive.") && file != "catalog_reads.go" && file != "request_timing.go" &&
			!strings.HasPrefix(name, "archive.queryMaps") && !strings.HasPrefix(name, "archive.beginCatalogWrite") {
			return fmt.Sprintf("%s (%s:%d)", name, file, frame.Line)
		}
		if !more {
			return ""
		}
	}
}

func secondsText(d time.Duration) string { return fmt.Sprintf("%.2fs", d.Seconds()) }

// breakdown says where elapsed went: waiting for the catalog writer, waiting
// for derived values, queued for a heavy-query slot, in the request's own
// statements, and the rest (computing, encoding, waiting for a connection or
// the processor).
func (t *requestTiming) breakdown(elapsed time.Duration) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := []string{"total=" + secondsText(elapsed)}
	writer := "writer_wait=" + secondsText(t.writerWait)
	if t.writes > 0 {
		writer += fmt.Sprintf(" writes=%d", t.writes)
	}
	if t.writerBehind != "" {
		writer += " behind=[" + t.writerBehind + "]"
	}
	parts = append(parts, writer)
	derived := "derived_wait=" + secondsText(t.derivedWait)
	if t.derived > 0 {
		derived += fmt.Sprintf(" derived_values=%d slowest_derived=%s:%s", t.derived, t.derivedKey, secondsText(t.derivedSlowest))
	}
	parts = append(parts, derived, "query_queue="+secondsText(t.queueWait),
		fmt.Sprintf("database=%s statements=%d", secondsText(t.database), t.statements))
	if caller := codeCaller(t.slowestStack); caller != "" {
		parts = append(parts, fmt.Sprintf("slowest_statement=%s at %q", secondsText(t.slowest), caller))
	}
	parts = append(parts, "other="+secondsText(max(0, elapsed-t.writerWait-t.derivedWait-t.queueWait-t.database)))
	if c := t.catalog; c != nil && c.DB != nil {
		// The pool's counters cover every request, not only this one: waits
		// here mean requests queued for a read connection while it ran.
		now := c.reader().Stats()
		parts = append(parts, fmt.Sprintf("read_pool=%d/%d in use, %d waits (%s) meanwhile", now.InUse, now.MaxOpenConnections,
			now.WaitCount-t.pool.WaitCount, secondsText(now.WaitDuration-t.pool.WaitDuration)))
		// Every page read searches the WAL's index, which grows with it.
		if info, err := os.Stat(c.Path + "-wal"); err == nil {
			parts = append(parts, fmt.Sprintf("wal_bytes=%d", info.Size()))
		}
	}
	return strings.Join(parts, " ")
}

// logSlowRequest writes the request's line when it was slow or abandoned.
// status is the HTTP status, or 0 for a tool call answered without HTTP.
func (t *requestTiming) logSlowRequest(elapsed time.Duration, status int, cancelled bool) {
	if t.stream.Load() || !(elapsed >= slowRequestLine || cancelled && elapsed >= cancelledRequestLine) {
		return
	}
	outcome := fmt.Sprintf("status=%d", status)
	if cancelled {
		outcome += " cancelled_by_client=true"
	}
	fmt.Fprintf(os.Stderr, "Slow request: %s pid=%d %s %s\n", t.label(), os.Getpid(), outcome, t.breakdown(elapsed))
}

// requestsInFlight lists the requests being answered, oldest first, for a
// contention report.
func requestsInFlight(now time.Time) []string {
	requestsTimed.Lock()
	timings := make([]*requestTiming, 0, len(requestsTimed.byGoroutine))
	for _, timing := range requestsTimed.byGoroutine {
		timings = append(timings, timing)
	}
	requestsTimed.Unlock()
	slices.SortFunc(timings, func(a, b *requestTiming) int { return a.started.Compare(b.started) })
	lines := []string{}
	for _, timing := range timings {
		if timing.stream.Load() {
			continue
		}
		line := fmt.Sprintf("%s for %s (goroutine %d)", timing.label(), elapsedText(now.Sub(timing.started)), timing.goroutine)
		timing.mu.Lock()
		if !timing.writerSince.IsZero() {
			line += fmt.Sprintf(", waiting for the catalog writer for %s", elapsedText(now.Sub(timing.writerSince)))
		}
		line += fmt.Sprintf("; so far writer_wait=%s derived_wait=%s database=%s statements=%d", secondsText(timing.writerWait), secondsText(timing.derivedWait), secondsText(timing.database), timing.statements)
		timing.mu.Unlock()
		lines = append(lines, line)
	}
	return lines
}
