package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// A read snapshot that outlives its work pins the WAL (wal_pin.go), and once
// it has, nothing in the process says who took it: a leaked cursor has no
// goroutine left, and the operation that opened it may have ended hours ago.
// So the catalog's own pools open connections through catalogConnector,
// which notes on each connection when its current read began, the statement
// or transaction that began it, the operation the context carried, and the
// goroutine's stack at that moment. A stall this process holds names the
// reads open since before it (catalogReads) and writes their stacks to a
// report (walPinReport).
//
// A connection is reading from its first query or BEGIN until its last
// cursor closes outside a transaction, or its transaction ends. A COMMIT or
// ROLLBACK that fails leaves the read marked open, with the error, since
// SQLite may have kept the transaction. Only these pools are seen: a
// transfer's or an adoption's snapshot of the catalog opens its own, and is
// named by its operation. database/sql's Raw gets a *catalogConn.

// catalogReadFrames bounds the stack kept per read.
const catalogReadFrames = 48

type catalogRead struct {
	Pool      string
	Kind      string // "query", "transaction" or "BEGIN" (a raw BEGIN statement)
	Began     time.Time
	Statement string
	Op        string
	EndError  string // a COMMIT or ROLLBACK that failed while the read was open
	stack     []uintptr
}

// catalogConnTrace is one connection's read, guarded by mu.
type catalogConnTrace struct {
	path, pool string
	mu         sync.Mutex
	read       catalogRead // Began is zero while the connection is idle
	cursors    int         // open cursors outside a transaction
	tx         bool
}

var catalogConns = struct {
	sync.Mutex
	all map[*catalogConnTrace]struct{}
}{all: map[*catalogConnTrace]struct{}{}}

// sqliteDriver is the registered "sqlite" driver, so the catalog's pools get
// the functions and hooks registered on it.
var sqliteDriver = sync.OnceValue(func() driver.Driver {
	db, _ := sql.Open("sqlite", "")
	defer db.Close()
	return db.Driver()
})

// openCatalogDB opens a pool on the catalog at path whose connections note
// their reads; pool names it in a diagnosis.
func openCatalogDB(path, dsn, pool string) *sql.DB {
	return sql.OpenDB(catalogConnector{path: filepath.Clean(path), dsn: dsn, pool: pool})
}

type catalogConnector struct{ path, dsn, pool string }

func (c catalogConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := sqliteDriver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	inner, ok := conn.(sqliteConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("the SQLite driver's connection is a %T", conn)
	}
	trace := &catalogConnTrace{path: c.path, pool: c.pool}
	catalogConns.Lock()
	catalogConns.all[trace] = struct{}{}
	catalogConns.Unlock()
	return &catalogConn{sqliteConn: inner, trace: trace}, nil
}

func (c catalogConnector) Driver() driver.Driver { return sqliteDriver() }

// sqliteConn is what database/sql uses of modernc's connection.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

type catalogConn struct {
	sqliteConn
	trace *catalogConnTrace
}

func (c *catalogConn) Close() error {
	catalogConns.Lock()
	delete(catalogConns.all, c.trace)
	catalogConns.Unlock()
	return c.sqliteConn.Close()
}

func (c *catalogConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.sqliteConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.trace.beginTx(ctx, "transaction", "")
	return &catalogTx{Tx: tx, trace: c.trace}, nil
}

func (c *catalogConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.sqliteConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	inner, ok := stmt.(sqliteStmt)
	if !ok {
		stmt.Close()
		return nil, fmt.Errorf("the SQLite driver's statement is a %T", stmt)
	}
	return &catalogStmt{sqliteStmt: inner, query: query, trace: c.trace}, nil
}

func (c *catalogConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.sqliteConn.ExecContext(ctx, query, args)
	c.trace.exec(ctx, query, err)
	return result, err
}

func (c *catalogConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.trace.query(ctx, query, func() (driver.Rows, error) { return c.sqliteConn.QueryContext(ctx, query, args) })
}

type sqliteStmt interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
}

type catalogStmt struct {
	sqliteStmt
	query string
	trace *catalogConnTrace
}

func (s *catalogStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	result, err := s.sqliteStmt.ExecContext(ctx, args)
	s.trace.exec(ctx, s.query, err)
	return result, err
}

func (s *catalogStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.trace.query(ctx, s.query, func() (driver.Rows, error) { return s.sqliteStmt.QueryContext(ctx, args) })
}

type sqliteRows interface {
	driver.Rows
	driver.RowsColumnTypeDatabaseTypeName
	driver.RowsColumnTypeLength
	driver.RowsColumnTypeNullable
	driver.RowsColumnTypePrecisionScale
	driver.RowsColumnTypeScanType
}

type catalogRows struct {
	sqliteRows
	once  sync.Once
	trace *catalogConnTrace
}

func (r *catalogRows) Close() error {
	err := r.sqliteRows.Close()
	r.once.Do(r.trace.closeCursor)
	return err
}

type catalogTx struct {
	driver.Tx
	trace *catalogConnTrace
}

func (t *catalogTx) Commit() error {
	err := t.Tx.Commit()
	t.trace.endTx("COMMIT", err)
	return err
}

func (t *catalogTx) Rollback() error {
	err := t.Tx.Rollback()
	t.trace.endTx("ROLLBACK", err)
	return err
}

// begin marks the connection reading, unless it already is. Call it with
// t.mu held.
func (t *catalogConnTrace) begin(ctx context.Context, kind, statement string) {
	if !t.read.Began.IsZero() {
		return
	}
	stack := make([]uintptr, catalogReadFrames)
	stack = stack[:runtime.Callers(3, stack)]
	t.read = catalogRead{Pool: t.pool, Kind: kind, Began: time.Now(), Statement: statement, stack: stack}
	if op := opFrom(ctx); op != nil {
		t.read.Op = op.spec.Kind
	}
}

// settle marks the connection idle once nothing keeps its read open. Call
// it with t.mu held.
func (t *catalogConnTrace) settle() {
	if t.cursors == 0 && !t.tx {
		t.read = catalogRead{}
	}
}

func (t *catalogConnTrace) beginTx(ctx context.Context, kind, statement string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tx && t.read.EndError != "" {
		// The transaction a failed COMMIT or ROLLBACK left is gone, or this
		// BEGIN would have failed.
		t.read = catalogRead{}
	}
	t.tx = true
	t.begin(ctx, kind, statement)
}

func (t *catalogConnTrace) endTx(statement string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// After an interrupt or some errors SQLite has already rolled back, and
	// says so; otherwise the transaction may still be open.
	if err != nil && !strings.Contains(err.Error(), "no transaction is active") {
		t.read.EndError = statement + ": " + err.Error()
		return
	}
	t.tx = false
	t.read.EndError = ""
	t.settle()
}

func (t *catalogConnTrace) query(ctx context.Context, query string, run func() (driver.Rows, error)) (driver.Rows, error) {
	t.mu.Lock()
	t.cursors++
	t.begin(ctx, "query", query)
	if t.read.Statement == "" {
		t.read.Statement = query // a transaction's first read
	}
	t.mu.Unlock()
	rows, err := run()
	if err == nil {
		inner, ok := rows.(sqliteRows)
		if ok {
			return &catalogRows{sqliteRows: inner, trace: t}, nil
		}
		rows.Close()
		err = fmt.Errorf("the SQLite driver's rows are a %T", rows)
	}
	t.closeCursor()
	return nil, err
}

func (t *catalogConnTrace) closeCursor() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cursors--
	t.settle()
}

// exec notes a raw BEGIN, COMMIT or ROLLBACK, which database/sql does not
// see as a transaction (a *sql.Conn that runs BEGIN IMMEDIATE, say). Other
// statements finish within the call.
func (t *catalogConnTrace) exec(ctx context.Context, query string, err error) {
	statement := strings.TrimSpace(query)
	end := strings.IndexFunc(statement, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == ';' })
	if end < 0 {
		end = len(statement)
	}
	verb := strings.ToUpper(statement[:end])
	if verb != "BEGIN" && verb != "COMMIT" && verb != "END" && verb != "ROLLBACK" ||
		strings.Contains(strings.TrimRight(statement, "; \t\n"), ";") {
		return // a batch of statements ends whatever it begins
	}
	switch {
	case verb == "BEGIN":
		if err == nil {
			t.beginTx(ctx, "BEGIN", query)
		}
	case verb != "ROLLBACK" || !slices.Contains(strings.Fields(strings.ToUpper(statement)), "TO"):
		t.mu.Lock()
		tx := t.tx
		t.mu.Unlock()
		if tx {
			t.endTx(verb, err)
		}
	}
}

// catalogReads lists this process's open reads on the catalog at path that
// began at or before since, oldest first.
func catalogReads(path string, since time.Time) []catalogRead {
	path = filepath.Clean(path)
	reads := []catalogRead{}
	catalogConns.Lock()
	for trace := range catalogConns.all {
		if trace.path != path {
			continue
		}
		trace.mu.Lock()
		read := trace.read
		trace.mu.Unlock()
		if !read.Began.IsZero() && !read.Began.After(since) {
			reads = append(reads, read)
		}
	}
	catalogConns.Unlock()
	slices.SortFunc(reads, func(a, b catalogRead) int { return a.Began.Compare(b.Began) })
	return reads
}

// frames returns the read's stack below database/sql and this file.
func (r catalogRead) frames() []runtime.Frame {
	frames := []runtime.Frame{}
	iter := runtime.CallersFrames(r.stack)
	for {
		frame, more := iter.Next()
		if !strings.HasPrefix(frame.Function, "database/sql.") && filepath.Base(frame.File) != "catalog_reads.go" {
			frames = append(frames, frame)
		}
		if !more {
			return frames
		}
	}
}

// shortFunction drops the module path from a function's name.
func shortFunction(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

// summary says what the read is, for how long it has been open, and the
// three innermost callers outside the query helpers.
func (r catalogRead) summary(now time.Time) string {
	summary := fmt.Sprintf("a %s on the %s pool for %s", r.Kind, r.Pool, now.Sub(r.Began).Round(time.Second))
	if r.Op != "" {
		summary += " in " + r.Op
	}
	if r.EndError != "" {
		summary += " (" + r.EndError + ")"
	}
	callers := []string{}
	for _, frame := range r.frames() {
		name := shortFunction(frame.Function)
		if strings.HasPrefix(name, "archive.queryMaps") {
			continue
		}
		callers = append(callers, fmt.Sprintf("%s (%s:%d)", name, filepath.Base(frame.File), frame.Line))
		if len(callers) == 3 {
			break
		}
	}
	if len(callers) > 0 {
		summary += " from " + strings.Join(callers, " ← ")
	}
	return summary
}

// traceback is the read's stack in the layout of a goroutine dump.
func (r catalogRead) traceback() string {
	var b strings.Builder
	for _, frame := range r.frames() {
		fmt.Fprintf(&b, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
	}
	return b.String()
}
