package archive

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// A WAL checkpoint copies frames into the database only up to the oldest
// snapshot a reader still uses. One read transaction held for hours (a
// findings pass waiting on the writer, another process reading the live
// catalog) stops it there: the WAL grows by everything written meanwhile,
// and every page fetch searches a wal-index that grows with it. boundWAL
// cannot fix that, so once the WAL is large and the checkpoint stays busy it
// says who holds it: the snapshot's process, and for this process its
// running operations that started before the stall. It cancels nothing.

var (
	// walPinBytes is the WAL size from which a busy bound checkpoint is
	// diagnosed. Tests lower it.
	walPinBytes int64 = 1 << 30
	// walPinDiagnoseEvery bounds how often the lock probe and the log line
	// run while the stall lasts.
	walPinDiagnoseEvery = time.Minute
	// readWALIndex and probeWALReaders read the -shm file; tests replace
	// them.
	readWALIndex    = readWALIndexHeader
	probeWALReaders = walReadLockHolders
)

// The wal-index header at the start of <catalog>-shm (SQLite's wal.c): two
// copies of WalIndexHdr (48 bytes each), then WalCkptInfo, all in native
// byte order. WalIndexHdr.mxFrame is the last valid frame; nBackfill is how
// far checkpoints have copied; aReadMark[i] is the frame a reader holding
// read lock i reads up to. The read locks are bytes of the -shm file:
// WALINDEX_LOCK_OFFSET (120) + WAL_READ_LOCK(i) (3+i).
const (
	walIndexHeaderBytes = 136
	walReadSlots        = 5
	walReadMarkUnused   = 0xffffffff
	walReadLockByte     = 120 + 3
)

type walIndexHeader struct {
	MxFrame, NBackfill uint32
	ReadMarks          [walReadSlots]uint32
}

func parseWALIndexHeader(b []byte) (walIndexHeader, error) {
	var h walIndexHeader
	if len(b) < walIndexHeaderBytes {
		return h, fmt.Errorf("wal-index header is %d bytes, want %d", len(b), walIndexHeaderBytes)
	}
	if b[12] == 0 { // isInit
		return h, errors.New("wal-index header is not initialized")
	}
	order := binary.NativeEndian
	h.MxFrame = order.Uint32(b[16:])
	h.NBackfill = order.Uint32(b[96:])
	for i := range h.ReadMarks {
		h.ReadMarks[i] = order.Uint32(b[100+4*i:])
	}
	return h, nil
}

// pinning lists the read slots whose mark is below the last frame: a reader
// holding one keeps the checkpoint from passing its mark. Slot 0 reads the
// database alone and never pins frames.
func (h walIndexHeader) pinning() []int {
	slots := []int{}
	for i := 1; i < walReadSlots; i++ {
		if mark := h.ReadMarks[i]; mark != walReadMarkUnused && mark < h.MxFrame {
			slots = append(slots, i)
		}
	}
	return slots
}

// walShmFiles keeps one descriptor per -shm file open for the life of the
// process. Closing any descriptor of a file drops every POSIX lock this
// process holds on it, SQLite's read locks included, so these are never
// closed. A file replaced since (the last connection deleted it, the
// library reopened) gets a new descriptor; the old one stays open.
var walShmFiles struct {
	mu      sync.Mutex
	files   map[string]*os.File
	retired []*os.File
}

func openWALShm(path string) (*os.File, error) {
	walShmFiles.mu.Lock()
	defer walShmFiles.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if file := walShmFiles.files[path]; file != nil {
		if open, err := file.Stat(); err == nil && os.SameFile(open, info) {
			return file, nil
		}
		walShmFiles.retired = append(walShmFiles.retired, file)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if walShmFiles.files == nil {
		walShmFiles.files = map[string]*os.File{}
	}
	walShmFiles.files[path] = file
	return file, nil
}

func readWALIndexHeader(shm string) (walIndexHeader, error) {
	file, err := openWALShm(shm)
	if err != nil {
		return walIndexHeader{}, err
	}
	b := make([]byte, walIndexHeaderBytes)
	if _, err := file.ReadAt(b, 0); err != nil {
		return walIndexHeader{}, err
	}
	return parseWALIndexHeader(b)
}

// walReader is another process holding a read slot.
type walReader struct {
	Slot    int
	PID     int
	Command string
}

// walPin is what is known about a checkpoint that cannot advance.
type walPin struct {
	Since     time.Time // when the checkpoint last advanced, as far as this process saw
	WALBytes  int64
	Pending   int64 // frames not yet checkpointed; -1 when the header could not be read
	Holder    string
	Diagnosed time.Time
}

func (p walPin) String() string {
	pending := "unknown"
	if p.Pending >= 0 {
		pending = formatCount(int(p.Pending))
	}
	return fmt.Sprintf("wal_bytes=%d frames_pending=%s stalled_since=%s holder=%s", p.WALBytes, pending, p.Since.UTC().Format(time.RFC3339), p.Holder)
}

// walStall is boundWAL's bookkeeping, guarded by walBound.mu.
type walStall struct {
	backfill      uint32
	backfillSince time.Time
	diagnosedAt   time.Time
	holder        string
}

// noteWALBusy records a busy bound checkpoint of a WAL of walBytes at now,
// and returns the diagnosis once the WAL is over walPinBytes. The lock
// probe and the log line run at most once per walPinDiagnoseEvery. Call it
// with c.wal.mu held.
func (c *Catalog) noteWALBusy(walBytes int64, now time.Time) *walPin {
	if walBytes < walPinBytes {
		return nil
	}
	stall := &c.wal.stall
	header, headerErr := readWALIndex(c.Path + "-shm")
	if stall.backfillSince.IsZero() || headerErr == nil && header.NBackfill != stall.backfill {
		stall.backfill, stall.backfillSince = header.NBackfill, now
	}
	pin := walPin{Since: stall.backfillSince, WALBytes: walBytes, Pending: -1}
	if headerErr == nil {
		pin.Pending = int64(header.MxFrame) - int64(header.NBackfill)
	}
	diagnose := stall.diagnosedAt.IsZero() || now.Sub(stall.diagnosedAt) >= walPinDiagnoseEvery
	if diagnose {
		stall.diagnosedAt, stall.holder = now, c.walHolder(header, headerErr, stall.backfillSince, now)
	}
	pin.Diagnosed, pin.Holder = stall.diagnosedAt, stall.holder
	if diagnose {
		fmt.Fprintf(os.Stderr, "Catalog WAL pinned: catalog=%s pid=%d %s\n", c.Path, os.Getpid(), pin)
	}
	c.wal.pin.Store(&pin)
	return &pin
}

// noteWALCheckpointed clears the diagnosis once a checkpoint succeeds.
func (c *Catalog) noteWALCheckpointed() {
	c.wal.stall = walStall{}
	c.wal.pin.Store(nil)
}

// walPinned is the current diagnosis, or nil.
func (c *Catalog) walPinned() *walPin {
	return c.wal.pin.Load()
}

// walHolder names who keeps the checkpoint from advancing: other processes
// holding a pinning read slot, by pid and command, or else this process and
// its operations that were already running when the stall began.
func (c *Catalog) walHolder(header walIndexHeader, headerErr error, since, now time.Time) string {
	slots := header.pinning()
	if headerErr != nil || len(slots) == 0 {
		// Without marks, or when the checkpoint is only kept from restarting
		// the log, any reader counts.
		slots = []int{0, 1, 2, 3, 4}
	}
	readers, probeErr := probeWALReaders(c.Path+"-shm", slots)
	others := []string{}
	seen := map[int]bool{}
	for _, reader := range readers {
		if seen[reader.PID] {
			continue
		}
		seen[reader.PID] = true
		name := fmt.Sprintf("pid %d", reader.PID)
		if reader.Command != "" {
			name += " (" + reader.Command + ")"
		}
		others = append(others, name)
	}
	if len(others) > 0 {
		return "another process: " + strings.Join(others, ", ")
	}
	suspects := c.walSuspects(since, now)
	// POSIX locks never conflict with their own process, so a pinning slot
	// no other process holds is held by this one.
	holder := fmt.Sprintf("this process (pid %d)", os.Getpid())
	if probeErr != nil {
		holder = "a reader in this or another process (" + probeErr.Error() + ")"
	}
	if len(suspects) > 0 {
		holder += "; running since before the stall: " + strings.Join(suspects, ", ")
	}
	return holder
}

// walSuspects lists this process's running operations that started before
// since, oldest first, by kind and age.
func (c *Catalog) walSuspects(since, now time.Time) []string {
	views := []opView{}
	for _, op := range c.ops.running() {
		view := op.view()
		if view.Spec.Kind == "maintenance.checkpoint" || view.Started.After(since) {
			continue
		}
		views = append(views, view)
	}
	slices.SortFunc(views, func(a, b opView) int {
		return cmp.Or(a.Started.Compare(b.Started), strings.Compare(string(a.ID), string(b.ID)))
	})
	suspects := make([]string, 0, len(views))
	for _, view := range views {
		suspects = append(suspects, fmt.Sprintf("%s for %s", view.Spec.Kind, now.Sub(view.Started).Round(time.Second)))
	}
	return suspects
}

// parseProcArgs reads kern.procargs2's layout: argc, the executable path,
// padding, then argv.
func parseProcArgs(raw []byte) string {
	if len(raw) < 4 {
		return ""
	}
	argc := int(binary.NativeEndian.Uint32(raw))
	rest := raw[4:]
	// Skip the executable path and the NULs that pad it.
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return ""
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := []string{}
	for len(args) < argc && len(rest) > 0 {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			end = len(rest)
		}
		args = append(args, string(rest[:end]))
		rest = rest[min(end+1, len(rest)):]
	}
	return strings.Join(args, " ")
}
