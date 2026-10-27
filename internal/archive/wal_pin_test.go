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
	// Slot 0 reads the database alone, an unused slot pins nothing, and a
	// reader at the last frame keeps nothing from the checkpoint.
	if got := header.pinning(); len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("pinning slots: %v", got)
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

// A busy bound checkpoint of a large WAL says how far the checkpoint got,
// since when, and who holds it; the probe runs at most once a minute, the
// stall restarts when the checkpoint advances, and a success clears it.
func TestWALPinBookkeeping(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	override(t, &walPinBytes, 1000)
	header := walIndexHeader{MxFrame: 5000, NBackfill: 100, ReadMarks: [walReadSlots]uint32{0, 100, walReadMarkUnused, walReadMarkUnused, walReadMarkUnused}}
	override(t, &readWALIndex, func(string) (walIndexHeader, error) { return header, nil })
	probes := 0
	readers := []walReader{{Slot: 1, PID: 4242, Command: "pharos labeling prepare --dry-run"}}
	override(t, &probeWALReaders, func(_ string, slots []int) ([]walReader, error) {
		probes++
		if len(slots) != 1 || slots[0] != 1 {
			t.Errorf("probed slots %v, want the pinning one", slots)
		}
		return readers, nil
	})
	start := time.Now()
	catalog.wal.mu.Lock()
	defer catalog.wal.mu.Unlock()

	if pin := catalog.noteWALBusy(999, start); pin != nil || catalog.walPinned() != nil || probes != 0 {
		t.Fatalf("a WAL under the threshold was diagnosed: %+v", pin)
	}
	pin := catalog.noteWALBusy(2000, start)
	if pin == nil || probes != 1 || pin.Pending != 4900 || !pin.Since.Equal(start) || !strings.Contains(pin.Holder, "pid 4242 (pharos labeling prepare --dry-run)") {
		t.Fatalf("first diagnosis: %+v, %d probes", pin, probes)
	}
	if !strings.Contains(pin.String(), "frames_pending=4,900") {
		t.Fatalf("log line: %s", pin)
	}
	// Within a minute: the size updates, the probe does not run again.
	if pin := catalog.noteWALBusy(3000, start.Add(30*time.Second)); probes != 1 || pin.WALBytes != 3000 || pin.Holder == "" || !pin.Since.Equal(start) {
		t.Fatalf("rate limit: %+v, %d probes", pin, probes)
	}
	// No other process holds the slot: it is this process, and its
	// operations already running when the stall began are the suspects.
	readers = nil
	_, old := server.begin(context.Background(), OperationSpec{Kind: "analysis.findings", Trigger: triggerUser})
	defer old.Finish(nil)
	pin = catalog.noteWALBusy(4000, start.Add(61*time.Second))
	if probes != 2 || !strings.HasPrefix(pin.Holder, "this process") || strings.Contains(pin.Holder, "analysis.findings") {
		t.Fatalf("an operation that began after the stall is a suspect: %+v", pin)
	}
	later := start.Add(time.Hour)
	header.NBackfill = 200 // the checkpoint advanced: the stall starts again
	pin = catalog.noteWALBusy(4000, later.Add(time.Hour))
	if !pin.Since.Equal(later.Add(time.Hour)) || pin.Pending != 4800 {
		t.Fatalf("an advancing checkpoint is still stalled since %s: %+v", start, pin)
	}
	catalog.wal.stall.backfillSince, catalog.wal.stall.diagnosedAt = later, time.Time{}
	pin = catalog.noteWALBusy(4000, later.Add(2*time.Hour))
	if !strings.Contains(pin.Holder, "analysis.findings for") {
		t.Fatalf("this process's suspects: %+v", pin)
	}
	// A probe that cannot run says so rather than blame this process.
	override(t, &probeWALReaders, func(string, []int) ([]walReader, error) { return nil, errors.New("unsupported") })
	catalog.wal.stall.diagnosedAt = time.Time{}
	if pin := catalog.noteWALBusy(4000, later.Add(3*time.Hour)); !strings.HasPrefix(pin.Holder, "a reader in this or another process") {
		t.Fatalf("unknown holder: %+v", pin)
	}
	catalog.noteWALCheckpointed()
	if catalog.walPinned() != nil {
		t.Fatal("a checkpoint did not clear the diagnosis")
	}
}

// End to end in one process: a read snapshot on another connection keeps
// the bound checkpoint busy; the skipped checkpoint carries the diagnosis,
// naming this process; the next checkpoint after the reader ends clears it.
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
	catalog.boundWALAt(context.Background(), -1, priorityBackground)
	pin := catalog.walPinned()
	if pin == nil || pin.Pending <= 0 || !strings.HasPrefix(pin.Holder, "this process") && !strings.HasPrefix(pin.Holder, "a reader in this or another process") {
		t.Fatalf("pinned WAL: %+v", pin)
	}
	if !strings.Contains(pin.Holder, "index.changes.manual for") {
		t.Fatalf("the running index is not a suspect: %+v", pin)
	}
	skipped := false
	for _, view := range server.ops.views() {
		skipped = skipped || view.Spec.Kind == "maintenance.checkpoint" && view.State == opSkipped && view.Reason == "catalog-busy" && strings.Contains(view.Detail, "frames_pending=")
	}
	if !skipped {
		t.Fatal("the skipped checkpoint has no diagnosis")
	}
	if item := firstItem(server.attention(context.Background()), "wal-pinned"); item == nil || item.Severity != "warning" || !strings.Contains(item.Detail, "this process") {
		t.Fatalf("attention: %+v", item)
	}
	reader.Rollback()
	catalog.wal.retryAt = time.Time{}
	catalog.boundWALAt(context.Background(), -1, priorityBackground)
	if catalog.walPinned() != nil {
		t.Fatal("a successful checkpoint left the diagnosis")
	}
	if item := firstItem(server.attention(context.Background()), "wal-pinned"); item != nil {
		t.Fatalf("attention after the checkpoint: %+v", item)
	}
}
