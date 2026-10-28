package archive

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

// walIndexBytes builds a -shm header as SQLite lays it out.
func walIndexBytes(mxFrame, nBackfill uint32, marks [walReadSlots]uint32) []byte {
	b := make([]byte, walIndexHeaderBytes)
	order := binary.NativeEndian
	for _, copyAt := range []int{0, 48} {
		order.PutUint32(b[copyAt:], 3007000) // iVersion
		b[copyAt+12] = 1                     // isInit
		order.PutUint32(b[copyAt+16:], mxFrame)
	}
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
	header := walIndexHeader{MxFrame: 5000, NBackfill: 100, ReadMarks: [walReadSlots]uint32{0, 100, walReadMarkUnused, walReadMarkUnused, walReadMarkUnused}}
	probes := 0
	locks := []walLock{{Byte: walReadLockByte + 1, PID: 4242, Command: "pharos labeling prepare --dry-run"}}
	var probeErr error
	override(t, &probeWAL, func(string) (walProbe, error) {
		probes++
		return walProbe{Header: header, Locks: locks}, probeErr
	})
	start := time.Now()
	catalog.wal.mu.Lock()
	defer catalog.wal.mu.Unlock()
	busy := func(size, backfilled int64, at time.Duration) *walPin {
		t.Helper()
		return catalog.noteWALBusy(size, 5000, backfilled, start.Add(at))
	}

	if pin := busy(999, 100, 0); pin != nil || probes != 0 {
		t.Fatalf("a WAL under the threshold was diagnosed: %+v", pin)
	}
	if pin := busy(2000, 100, 0); pin != nil || catalog.walPinned() != nil {
		t.Fatalf("one busy check is not a stall: %+v", pin)
	}
	if pin := catalog.noteWALBusy(2000, 5000, 5000, start); pin != nil || catalog.walPinned() != nil {
		t.Fatalf("a log whose frames were all copied is not stalled: %+v", pin)
	}
	busy(2000, 100, 0)
	pin := busy(2000, 100, 30*time.Second)
	if pin == nil || probes != 1 || pin.Pending != 4900 || !pin.Since.Equal(start) || !pin.Known ||
		pin.Holder != "another process: pid 4242 (pharos labeling prepare --dry-run) reading up to frame 100 of 5,000" {
		t.Fatalf("first diagnosis: %+v, %d probes", pin, probes)
	}
	if !strings.Contains(pin.String(), "frames_pending=4,900") {
		t.Fatalf("log line: %s", pin)
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
	header.NBackfill, header.ReadMarks[1] = 200, 200
	if pin := busy(3000, 200, 80*time.Second); probes != 2 || pin.Holder != "another process: pid 77 (sqlite3 catalog.sqlite3) writing" || !pin.Since.Equal(start.Add(70*time.Second)) {
		t.Fatalf("a new stall kept the old diagnosis: %+v, %d probes", pin, probes)
	}

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
	pin = catalog.noteWALBusy(3000, 5000, 200, later.Add(time.Second))
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
	// A probe that cannot run says so.
	probeErr = errors.New("unsupported")
	catalog.noteWALCheckpointed()
	catalog.noteWALBusy(3000, 5000, 200, later)
	if pin := catalog.noteWALBusy(3000, 5000, 200, later.Add(time.Second)); pin.Known || pin.Holder != "unknown (unsupported)" {
		t.Fatalf("unknown holder: %+v", pin)
	}
	// A log that is small again clears it.
	if pin := catalog.noteWALBusy(500, 5000, 200, later.Add(2*time.Second)); pin != nil || catalog.walPinned() != nil {
		t.Fatal("a small log kept the diagnosis")
	}
}

// End to end in one process: a read snapshot on another connection keeps
// the bound checkpoint from advancing; the second busy check diagnoses it,
// the skipped checkpoint carries the diagnosis, and the next checkpoint
// after the reader ends clears it.
func TestBoundWALNamesTheReaderThatPinsIt(t *testing.T) {
	useDurableCatalog(t)
	override(t, &checkpointWaitMS, 50)
	override(t, &walPinBytes, 1)
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	_, index := server.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser})
	defer index.Finish(nil)
	other := otherProcess(t, catalog)
	writeRows(t, catalog.DB, "before-")
	reader, err := other.DB.Begin()
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
	pin := catalog.walPinned()
	if pin == nil || pin.Pending <= 0 || !strings.Contains(pin.Holder, "index.changes.manual for") {
		t.Fatalf("pinned WAL: %+v", pin)
	}
	if probe, err := probeWAL(catalog.Path + "-shm"); err == nil && (!pin.Known || !strings.HasPrefix(pin.Holder, "this process") || len(probe.Locks) != 0) {
		t.Fatalf("a reader in this process: %+v, probe %+v", pin, probe)
	}
	skipped := false
	for _, view := range server.ops.views() {
		skipped = skipped || view.Spec.Kind == "maintenance.checkpoint" && view.State == opSkipped && view.Reason == "catalog-busy" && strings.Contains(view.Detail, "frames_pending=")
	}
	if !skipped {
		t.Fatal("the skipped checkpoint has no diagnosis")
	}
	if item := firstItem(server.attention(context.Background()), "wal-pinned"); item == nil || item.Severity != "warning" || !strings.Contains(item.Detail, "index.changes.manual") {
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
