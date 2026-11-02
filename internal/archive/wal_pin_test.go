package archive

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// walIndexBytes builds a -shm header as SQLite lays it out.
func walIndexBytes(mxFrame, nBackfill uint32, marks [walReadSlots]uint32) []byte {
	b := make([]byte, walIndexHeaderBytes)
	order := binary.NativeEndian
	order.PutUint32(b, 3007000) // iVersion
	b[12] = 1                   // isInit
	order.PutUint32(b[16:], mxFrame)
	var s1, s2 uint32
	for i := 0; i < 40; i += 8 {
		s1 += order.Uint32(b[i:]) + s2
		s2 += order.Uint32(b[i+4:]) + s1
	}
	order.PutUint32(b[40:], s1)
	order.PutUint32(b[44:], s2)
	copy(b[48:96], b[:48])
	order.PutUint32(b[96:], nBackfill)
	for i, mark := range marks {
		order.PutUint32(b[100+4*i:], mark)
	}
	return b
}

func TestWALIndexHeaderParse(t *testing.T) {
	header, err := parseWALIndexHeader(walIndexBytes(3_400_000, 1045, [walReadSlots]uint32{0, 1045, walReadMarkUnused, 3_400_000, 2_000_000}))
	if err != nil {
		t.Fatal(err)
	}
	if header.MxFrame != 3_400_000 || header.NBackfill != 1045 || header.ReadMarks[1] != 1045 || header.ReadMarks[2] != walReadMarkUnused {
		t.Fatalf("header: %+v", header)
	}
	// Only a slot marked where the checkpoint stopped is blocking it; slot 0
	// reads the database alone.
	if got := header.blocking(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("blocking slots: %v", got)
	}
	header.NBackfill = header.MxFrame
	if got := header.blocking(); len(got) != 0 {
		t.Fatalf("a fully copied log has blocking slots: %v", got)
	}
	if _, err := parseWALIndexHeader(make([]byte, 100)); err == nil {
		t.Fatal("a short header parsed")
	}
	if _, err := parseWALIndexHeader(make([]byte, walIndexHeaderBytes)); err == nil {
		t.Fatal("an uninitialized header parsed")
	}
	// A header caught mid-update: the copies differ, or the checksum fails.
	changing := walIndexBytes(10, 5, [walReadSlots]uint32{})
	changing[48+16]++
	if _, err := parseWALIndexHeader(changing); err == nil {
		t.Fatal("differing header copies parsed")
	}
	changing = walIndexBytes(10, 5, [walReadSlots]uint32{})
	changing[16]++
	changing[48+16]++
	if _, err := parseWALIndexHeader(changing); err == nil {
		t.Fatal("a header with a bad checksum parsed")
	}
}

func TestProcArgsParse(t *testing.T) {
	raw := binary.NativeEndian.AppendUint32(nil, 4)
	raw = append(raw, "/usr/local/bin/pharos\x00\x00\x00\x00pharos\x00labeling\x00prepare\x00--dry-run\x00HOME=/Users/x\x00"...)
	if got := parseProcArgs(raw); got != "pharos labeling prepare --dry-run" {
		t.Fatalf("command: %q", got)
	}
	if got := parseProcArgs([]byte{1}); got != "" {
		t.Fatalf("short: %q", got)
	}
}

// A busy checkpoint of a large WAL is a stall only when frames are left and
// the checkpoint did not advance between two checks; the probe runs at most
// once a minute during one stall, again at once for a new one, and the
// diagnosis clears when the checkpoint advances, the log is small, or
// everything was copied.
func TestWALPinBookkeeping(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	override(t, &walPinBytes, 1000)
	override(t, &walPinReportsKept, 2)
	override(t, &openCatalogReads, func(string, time.Time) []catalogRead { return nil })
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	reports := func() []string {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(walPinReportDir(), walPinReportPrefix+"*"))
		return names
	}
	header := walIndexHeader{MxFrame: 5000, NBackfill: 100, ReadMarks: [walReadSlots]uint32{0, 100, walReadMarkUnused, walReadMarkUnused, walReadMarkUnused}}
	probes := 0
	locks := []walLock{{Byte: walReadLockByte + 1, PID: 4242, Command: "pharos labeling prepare --dry-run"}}
	var probeErr error
	override(t, &probeWAL, func(string) (walProbe, error) {
		probes++
		return walProbe{Header: header, Locks: locks, After: header}, probeErr
	})
	start := time.Now()
	catalog.wal.mu.Lock()
	defer catalog.wal.mu.Unlock()
	busy := func(size, backfilled int64, at time.Duration) *walPin {
		t.Helper()
		return catalog.noteWALBusy(size, 5000, backfilled, start.Add(at))
	}

	// A stall under the threshold is tracked but not diagnosed; once the
	// log crosses it, the stall dates from when the checkpoint stopped.
	busy(999, 100, -time.Hour)
	if pin := busy(999, 100, -time.Minute); pin != nil || probes != 0 || catalog.walPinned() != nil {
		t.Fatalf("a WAL under the threshold was diagnosed: %+v", pin)
	}
	if pin := busy(2000, 100, 0); pin == nil || !pin.Since.Equal(start.Add(-time.Hour)) {
		t.Fatalf("a stall that began under the threshold: %+v", pin)
	}
	if pin := catalog.noteWALBusy(2000, 5000, 5000, start); pin != nil || catalog.walPinned() != nil {
		t.Fatalf("a log whose frames were all copied is not stalled: %+v", pin)
	}
	probes = 0
	if pin := busy(2000, 100, 0); pin != nil || catalog.walPinned() != nil {
		t.Fatalf("one busy check is not a stall: %+v", pin)
	}
	pin := busy(2000, 100, 30*time.Second)
	if pin == nil || probes != 1 || pin.Pending != 4900 || !pin.Since.Equal(start) || !pin.Known ||
		pin.Holder != "another process: pid 4242 (pharos labeling prepare --dry-run) reading up to frame 100 of 5,000" {
		t.Fatalf("first diagnosis: %+v, %d probes", pin, probes)
	}
	if pin.Report != "" || len(reports()) != 0 {
		t.Fatalf("a stall another process holds wrote a report: %+v", pin)
	}
	if !strings.Contains(pin.String(), "frames_pending=4,900") {
		t.Fatalf("log line: %s", pin)
	}
	// A check whose truncating pass reported no counts (it did not get the
	// checkpoint lock) changes nothing.
	if pin := catalog.noteWALBusy(2000, -1, -1, start.Add(45*time.Second)); pin == nil || !pin.Since.Equal(start) || catalog.walPinned() == nil || probes != 1 {
		t.Fatalf("unknown counts cleared the stall: %+v", pin)
	}
	// Within a minute: the size updates, the probe does not run again.
	if pin := busy(3000, 100, 60*time.Second); probes != 1 || pin.WALBytes != 3000 || !pin.Since.Equal(start) {
		t.Fatalf("rate limit: %+v, %d probes", pin, probes)
	}
	// The checkpoint advanced: the old holder is dropped, and the next stall
	// is probed at once.
	locks = []walLock{{Byte: walWriteLockByte, PID: 77, Command: "sqlite3 catalog.sqlite3"}}
	if pin := busy(3000, 200, 70*time.Second); pin != nil || catalog.walPinned() != nil {
		t.Fatalf("an advancing checkpoint is still diagnosed: %+v", pin)
	}
	// Neither the writer nor a reader at the last frame stops the copy:
	// with no other process on a blocking slot, it is this process's slot,
	// and the others are only mentioned.
	header.NBackfill, header.ReadMarks[1], header.ReadMarks[2] = 200, 200, 5000
	locks = append(locks, walLock{Byte: walReadLockByte + 2, PID: 78, Command: "pharos mcp"}, walLock{Byte: walCheckpointByte + 1, PID: 79})
	pin = busy(3000, 200, 80*time.Second)
	// One probe names this process only probably: the slot's reader may
	// just have ended.
	if probes != 2 || !pin.Since.Equal(start.Add(70*time.Second)) || pin.Known || !strings.HasPrefix(pin.Holder, "probably this process") ||
		!strings.HasSuffix(pin.Holder, "also open in pid 77 (sqlite3 catalog.sqlite3) writing, pid 78 (pharos mcp) reading, pid 79 recovering the log") {
		t.Fatalf("a new stall: %+v, %d probes", pin, probes)
	}
	// A stall this process may hold writes one report.
	if names := reports(); len(names) != 1 || pin.Report != names[0] || !strings.Contains(pin.String(), "report="+pin.Report) {
		t.Fatalf("report: %+v, %v", pin, names)
	}
	if again := busy(3000, 200, 85*time.Second); again.Report != pin.Report || len(reports()) != 1 {
		t.Fatalf("a stall reported again: %+v", again)
	}
	header.ReadMarks[2] = walReadMarkUnused
	// The header cannot be read: a reader elsewhere may hold it, but that is
	// not known.
	catalog.noteWALCheckpointed()
	locks = []walLock{{Byte: walReadLockByte + 3, PID: 80}}
	var headerErr error = errors.New("short read")
	after := func(h walIndexHeader) walIndexHeader { return h }
	override(t, &probeWAL, func(string) (walProbe, error) {
		probes++
		return walProbe{Header: header, HeaderErr: headerErr, Locks: locks, After: after(header)}, probeErr
	})
	busy(3000, 200, 90*time.Second)
	if pin := busy(3000, 200, 100*time.Second); pin.Known || !strings.Contains(pin.Holder, "possibly another process: pid 80 reading") {
		t.Fatalf("an unreadable header: %+v", pin)
	}
	headerErr = nil
	// Another process on slot 0 while frames are left: it started reading
	// when the log was fully copied (just after a truncation), and nothing
	// is copied while it reads.
	catalog.noteWALCheckpointed()
	locks = []walLock{{Byte: walReadLockByte, PID: 81, Command: "pharos labeling prepare --dry-run"}}
	busy(3000, 200, 110*time.Second)
	if pin := busy(3000, 200, 120*time.Second); !pin.Known || pin.Holder != "another process: pid 81 (pharos labeling prepare --dry-run) reading the database since the log was last fully copied, at frame 200" {
		t.Fatalf("a slot 0 reader: %+v", pin)
	}
	// The header moved between the reads around the locks: the lock may
	// belong to a reader that came after the one that stopped the copy.
	after = func(h walIndexHeader) walIndexHeader { h.MxFrame++; return h }
	catalog.noteWALCheckpointed()
	busy(3000, 200, 130*time.Second)
	if pin := busy(3000, 200, 140*time.Second); pin.Known || !strings.Contains(pin.Holder, "unknown (the wal-index changed while it was read); possibly another process: pid 81") {
		t.Fatalf("an unsteady header: %+v", pin)
	}
	after = func(h walIndexHeader) walIndexHeader { return h }

	// No other process: a slot marked where the checkpoint stopped is this
	// process's, and only operations running since before the stall are
	// suspects.
	locks = nil
	_, queued := server.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser, Queued: true})
	defer queued.Finish(nil)
	_, old := server.begin(context.Background(), OperationSpec{Kind: "analysis.findings", Trigger: triggerUser})
	defer old.Finish(nil)
	catalog.noteWALCheckpointed()
	later := time.Now().Add(time.Hour)
	catalog.noteWALBusy(3000, 5000, 200, later)
	if pin := catalog.noteWALBusy(3000, 5000, 200, later.Add(time.Second)); pin.Known || !strings.HasPrefix(pin.Holder, "probably this process") {
		t.Fatalf("one probe: %+v", pin)
	}
	// A second probe agrees: now it is this process.
	pin = catalog.noteWALBusy(3000, 5000, 200, later.Add(time.Minute+time.Second))
	if !pin.Known || !strings.HasPrefix(pin.Holder, "this process") || !strings.Contains(pin.Holder, "analysis.findings for") || strings.Contains(pin.Holder, "index.changes.manual") {
		t.Fatalf("this process's suspects: %+v", pin)
	}
	// An operation that began after the stall is no suspect.
	catalog.noteWALCheckpointed()
	early := start.Add(-time.Hour)
	catalog.noteWALBusy(3000, 5000, 200, early)
	if pin := catalog.noteWALBusy(3000, 5000, 200, early.Add(time.Second)); strings.Contains(pin.Holder, "analysis.findings") {
		t.Fatalf("an operation that began after the stall is a suspect: %+v", pin)
	}
	// No slot is marked where the checkpoint stopped (only the restart was
	// refused, or the marks moved): unknown, not this process.
	header.ReadMarks[1] = 4000
	catalog.noteWALCheckpointed()
	catalog.noteWALBusy(3000, 5000, 200, later)
	if pin := catalog.noteWALBusy(3000, 5000, 200, later.Add(time.Second)); pin.Known || !strings.HasPrefix(pin.Holder, "unknown") || !strings.Contains(pin.Holder, "analysis.findings") {
		t.Fatalf("an unexplained stall blamed a holder: %+v", pin)
	}
	// A probe that cannot run says so, and still lists this process's
	// suspects.
	probeErr = errors.New("unsupported")
	catalog.noteWALCheckpointed()
	catalog.noteWALBusy(3000, 5000, 200, later)
	if pin := catalog.noteWALBusy(3000, 5000, 200, later.Add(time.Second)); pin.Known || !strings.HasPrefix(pin.Holder, "unknown (unsupported); this process has been running since before the stall: analysis.findings for") {
		t.Fatalf("unknown holder: %+v", pin)
	}
	// Only the newest reports are kept.
	if names := reports(); len(names) != walPinReportsKept {
		t.Fatalf("reports kept: %v", names)
	}
	// A log that is small again clears it.
	if pin := catalog.noteWALBusy(500, 5000, 200, later.Add(2*time.Second)); pin != nil || catalog.walPinned() != nil {
		t.Fatal("a small log kept the diagnosis")
	}
}

// A connection of the catalog's pools is reading from its first query or
// BEGIN until its cursors close or its transaction ends; a COMMIT that fails
// leaves the read open.
func TestCatalogReadsFollowConnections(t *testing.T) {
	catalog, _ := testCatalog(t)
	ctx := context.Background()
	open := func(statement string) *catalogRead {
		t.Helper()
		for _, read := range catalogReads(catalog.Path, time.Now()) {
			if strings.Contains(read.Statement, statement) {
				return &read
			}
		}
		return nil
	}

	rows, err := catalog.reader().QueryContext(ctx, "SELECT 'cursor' FROM meta")
	if err != nil {
		t.Fatal(err)
	}
	read := open("'cursor'")
	if read == nil || read.Kind != "query" || read.Pool != "reader" || !strings.Contains(read.summary(time.Now()), "from archive.TestCatalogReadsFollowConnections (wal_pin_test.go:") {
		t.Fatalf("an open cursor: %+v", read)
	}
	if reads := catalogReads(catalog.Path, read.Began.Add(-time.Nanosecond)); slices.ContainsFunc(reads, func(r catalogRead) bool { return r.Statement == read.Statement }) {
		t.Fatal("a read begun after since is listed")
	}
	rows.Close()
	if read := open("'cursor'"); read != nil {
		t.Fatalf("a closed cursor: %+v", read)
	}

	tx, err := catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := tx.QueryRowContext(ctx, "SELECT 'in a transaction'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if read := open("'in a transaction'"); read == nil || read.Kind != "transaction" || read.Pool != "catalog" {
		t.Fatalf("a transaction after its first read: %+v", read)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if read := open("'in a transaction'"); read != nil {
		t.Fatalf("a committed transaction: %+v", read)
	}

	conn, err := catalog.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT 'after BEGIN'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if read := open("BEGIN IMMEDIATE"); read == nil || read.Kind != "BEGIN" {
		t.Fatalf("a raw BEGIN: %+v", read)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if read := open("BEGIN IMMEDIATE"); read != nil {
		t.Fatalf("a raw COMMIT: %+v", read)
	}

	trace := &catalogConnTrace{pool: "catalog"}
	trace.beginTx(ctx, "transaction", "")
	trace.endTx("COMMIT", errors.New("disk I/O error"))
	if trace.read.Began.IsZero() || !strings.Contains(trace.read.summary(time.Now()), "(COMMIT: disk I/O error)") {
		t.Fatalf("a failed COMMIT: %+v", trace.read)
	}
	trace.beginTx(ctx, "transaction", "")
	if trace.read.EndError != "" {
		t.Fatalf("a BEGIN after a failed COMMIT: %+v", trace.read)
	}
	// A ROLLBACK SQLite refuses because it already rolled back ends it.
	trace.endTx("ROLLBACK", errors.New("cannot rollback - no transaction is active"))
	if !trace.read.Began.IsZero() || trace.tx {
		t.Fatalf("a ROLLBACK with no transaction left: %+v", trace.read)
	}
	trace.exec(ctx, "BEGIN", nil)
	trace.exec(ctx, "ROLLBACK\nTO before", nil)
	if trace.read.Began.IsZero() {
		t.Fatal("a ROLLBACK TO a savepoint ended the transaction")
	}
}

// cancelWALTestQuery is the query pharos_cancel_query() cancels, while
// SQLite computes its first row.
var cancelWALTestQuery atomic.Pointer[context.CancelFunc]

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("pharos_cancel_query", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		if cancel := cancelWALTestQuery.Load(); cancel != nil {
			(*cancel)()
		}
		time.Sleep(20 * time.Millisecond) // the driver's interrupt sees the cancellation before the step returns
		return int64(1), nil
	})
}

// A query cancelled after SQLite stepped to its first row returns no rows.
// The driver must still finalize that statement: modernc.org/sqlite before
// v1.41.0 dropped it, so the discarded connection lived on as a zombie that
// held its snapshot, and the WAL, until the process exited, with no read or
// goroutine left to name.
func TestCancelledQueryLeavesNoSnapshot(t *testing.T) {
	catalog, _ := testCatalog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWALTestQuery.Store(&cancel)
	defer cancelWALTestQuery.Store(nil)
	rows, err := catalog.reader().QueryContext(ctx, "SELECT pharos_cancel_query(), key FROM meta")
	if err == nil {
		rows.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled query: %v", err)
	}
	writeRows(t, catalog.DB, "after-")
	var busy, frames, copied int
	if err := catalog.DB.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &copied); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatalf("a cancelled query holds the WAL: busy=%d frames=%d copied=%d", busy, frames, copied)
	}
}

// The wrapper keeps every optional interface the driver's connection,
// statement and rows implement, so database/sql treats them the same after
// a driver upgrade adds one.
func TestCatalogConnKeepsTheDriversInterfaces(t *testing.T) {
	ctx := context.Background()
	raw, err := sqliteDriver().Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := catalogConnector{dsn: ":memory:", pool: "test"}.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	same := func(what string, inner, wrapped any, interfaces ...reflect.Type) {
		t.Helper()
		for _, iface := range interfaces {
			if reflect.TypeOf(inner).Implements(iface) && !reflect.TypeOf(wrapped).Implements(iface) {
				t.Errorf("the %s wrapper drops %s", what, iface)
			}
		}
	}
	of := func(pointer any) reflect.Type { return reflect.TypeOf(pointer).Elem() }
	same("connection", raw, conn, of((*driver.ConnBeginTx)(nil)), of((*driver.ConnPrepareContext)(nil)), of((*driver.ExecerContext)(nil)),
		of((*driver.QueryerContext)(nil)), of((*driver.Pinger)(nil)), of((*driver.SessionResetter)(nil)), of((*driver.Validator)(nil)),
		of((*driver.NamedValueChecker)(nil))) // Execer and Queryer only stand in for the context versions
	query := "SELECT 1"
	innerStmt, err := raw.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer innerStmt.Close()
	stmt, err := conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	same("statement", innerStmt, stmt, of((*driver.StmtExecContext)(nil)), of((*driver.StmtQueryContext)(nil)),
		of((*driver.NamedValueChecker)(nil)), of((*driver.ColumnConverter)(nil)))
	innerRows, err := raw.(driver.QueryerContext).QueryContext(ctx, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer innerRows.Close()
	rows, err := conn.(driver.QueryerContext).QueryContext(ctx, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	same("rows", innerRows, rows, of((*driver.RowsNextResultSet)(nil)), of((*driver.RowsColumnTypeDatabaseTypeName)(nil)),
		of((*driver.RowsColumnTypeLength)(nil)), of((*driver.RowsColumnTypeNullable)(nil)), of((*driver.RowsColumnTypePrecisionScale)(nil)),
		of((*driver.RowsColumnTypeScanType)(nil)))
}

// End to end in one process: a read snapshot on the catalog's reader pool
// keeps the bound checkpoint from advancing; the second busy check diagnoses
// it, names the read by the code that began it and writes a report with its
// stack, the skipped checkpoint carries the diagnosis, and the next
// checkpoint after the reader ends clears it.
func TestBoundWALNamesTheReaderThatPinsIt(t *testing.T) {
	useDurableCatalog(t)
	override(t, &checkpointWaitMS, 50)
	override(t, &walPinBytes, 1)
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	ctx, index := server.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser})
	defer index.Finish(nil)
	writeRows(t, catalog.DB, "before-")
	reader, err := catalog.reader().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var count int
	if err := reader.QueryRow("SELECT COUNT(*) FROM meta").Scan(&count); err != nil {
		t.Fatal(err)
	}
	writeRows(t, catalog.DB, "after-")
	check := func() {
		catalog.wal.retryAt = time.Time{}
		catalog.boundWALAt(context.Background(), -1, priorityBackground)
	}
	check()
	if pin := catalog.walPinned(); pin != nil {
		t.Fatalf("one busy check is a stall: %+v", pin)
	}
	check()
	override(t, &walPinDiagnoseEvery, 0)
	check() // a second probe confirms a holder in this process
	pin := catalog.walPinned()
	if pin == nil || pin.Pending <= 0 || !strings.Contains(pin.Holder, "index.changes.manual for") ||
		!strings.Contains(pin.Holder, "a transaction on the reader pool for") || !strings.Contains(pin.Holder, "in index.changes.manual from archive.TestBoundWALNamesTheReaderThatPinsIt (wal_pin_test.go:") {
		t.Fatalf("pinned WAL: %+v", pin)
	}
	report, err := os.ReadFile(pin.Report)
	if err != nil || filepath.Dir(pin.Report) != walPinReportDir() {
		t.Fatalf("report %q: %v", pin.Report, err)
	}
	for _, want := range []string{"Reads open on this catalog in this process", "(before the stall)", "archive.TestBoundWALNamesTheReaderThatPinsIt\n\t", "Goroutines:"} {
		if !strings.Contains(string(report), want) {
			t.Fatalf("the report has no %q:\n%s", want, report)
		}
	}
	if runtime.GOOS == "darwin" && (!pin.Known || !strings.HasPrefix(pin.Holder, "this process")) {
		t.Fatalf("a reader in this process: %+v", pin)
	}
	skipped := false
	for _, view := range server.ops.views() {
		skipped = skipped || view.Spec.Kind == "maintenance.checkpoint" && view.State == opSkipped && view.Reason == "catalog-busy" && strings.Contains(view.Detail, "frames_pending=")
	}
	if !skipped {
		t.Fatal("the skipped checkpoint has no diagnosis")
	}
	if item := firstItem(server.attention(context.Background()), "wal-pinned"); item == nil || item.Severity != "warning" || !strings.Contains(item.Detail, "index.changes.manual") || !strings.Contains(item.Detail, pin.Report) {
		t.Fatalf("attention: %+v", item)
	}
	reader.Rollback()
	check()
	if catalog.walPinned() != nil {
		t.Fatal("a successful checkpoint left the diagnosis")
	}
	if item := firstItem(server.attention(context.Background()), "wal-pinned"); item != nil {
		t.Fatalf("attention after the checkpoint: %+v", item)
	}
}
