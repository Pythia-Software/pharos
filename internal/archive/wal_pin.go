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
	"time"
)

// A WAL checkpoint copies frames into the database only up to the oldest
// snapshot a reader still uses. One read transaction held for hours (a
// findings pass waiting on the writer, another process reading the live
// catalog) stops it there: the WAL grows by everything written meanwhile,
// and every page fetch searches a wal-index that grows with it. boundWAL
// cannot fix that, so once the WAL is large and its checkpoint has stopped
// advancing it says who holds it. It cancels nothing.
//
// A busy truncating checkpoint alone is not a stall: it is also busy when
// every frame was copied but a reader or the writer kept it from restarting
// the log. A stall is frames left to copy and nBackfill unchanged across two
// busy checks in a row (boundWAL retries every walRetryDelay).

var (
	// walPinBytes is the WAL size from which a stalled checkpoint is
	// diagnosed. Tests lower it.
	walPinBytes int64 = 1 << 30
	// walPinDiagnoseEvery bounds how often the lock probe and the log line
	// run while one stall lasts.
	walPinDiagnoseEvery = time.Minute
	// probeWAL reads the -shm header and its locks; tests replace it.
	probeWAL = probeWALShm
)

// The wal-index header at the start of <catalog>-shm (SQLite's wal.c): two
// copies of WalIndexHdr (48 bytes each), then WalCkptInfo, all in native
// byte order. WalIndexHdr.mxFrame is the last valid frame; nBackfill is how
// far checkpoints have copied; aReadMark[i] is the frame a reader holding
// read lock i reads up to. The locks are bytes of the -shm file from
// WALINDEX_LOCK_OFFSET (120): the write lock, the checkpoint lock, the
// recovery lock, then read lock i at 123+i.
const (
	walIndexHeaderBytes = 136
	walReadSlots        = 5
	walReadMarkUnused   = 0xffffffff
	walWriteLockByte    = 120
	walCheckpointByte   = 121
	walReadLockByte     = 123
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

// blocking lists the read slots whose mark is where the checkpoint stopped.
// A checkpoint moves every unheld slot's mark past what it copies, so after
// one, a slot still marked at nBackfill (below the last frame) is held by
// the reader that stopped it.
func (h walIndexHeader) blocking() []int {
	slots := []int{}
	if h.NBackfill >= h.MxFrame {
		return slots
	}
	for i := 1; i < walReadSlots; i++ {
		if h.ReadMarks[i] == h.NBackfill {
			slots = append(slots, i)
		}
	}
	return slots
}

// walLock is a lock another process holds on a byte of the -shm file.
type walLock struct {
	Byte    int
	PID     int
	Command string
}

// walProbe is what probeWAL read: the header, and the locks other
// processes hold (POSIX locks never report the caller's own).
type walProbe struct {
	Header    walIndexHeader
	HeaderErr error
	Locks     []walLock
}

// walPin is what is known about a checkpoint that stopped advancing.
type walPin struct {
	Since     time.Time // the first busy check that saw the checkpoint at this frame
	WALBytes  int64
	Pending   int64 // frames not yet copied into the database
	Holder    string
	Known     bool // Holder names who holds the log, not only what is running
	Diagnosed time.Time
}

func (p walPin) String() string {
	return fmt.Sprintf("wal_bytes=%d frames_pending=%s stalled_since=%s holder=%s", p.WALBytes, formatCount(int(p.Pending)), p.Since.UTC().Format(time.RFC3339), p.Holder)
}

// walStall is boundWAL's bookkeeping, guarded by walBound.mu.
type walStall struct {
	backfill      int64
	backfillSince time.Time
	diagnosedAt   time.Time
	holder        string
	known         bool
	// selfProbes counts probes in a row that found this process holding
	// the log: it is named for certain only once two agree.
	selfProbes int
}

// noteWALBusy records a busy bound checkpoint of a WAL of walBytes whose
// truncating pass reported frames in the log and backfilled of them copied
// (-1 when unknown). The checkpoint's progress is tracked at any size, so a
// stall's start is when the checkpoint stopped, not when the log crossed
// walPinBytes; only the diagnosis waits for that size. The probe and the
// log line run at most once per walPinDiagnoseEvery during one stall. Call
// it with c.wal.mu held.
func (c *Catalog) noteWALBusy(walBytes, frames, backfilled int64, now time.Time) *walPin {
	if frames < 0 || backfilled < 0 {
		// The truncating pass did not get the checkpoint lock (another
		// connection was checkpointing) and reported nothing: no evidence
		// either way, so the bookkeeping stays as it is.
		return c.walPinned()
	}
	pending := frames - backfilled
	if pending <= 0 {
		// Everything was copied; only the restart of the log was refused.
		c.noteWALCheckpointed()
		return nil
	}
	stall := &c.wal.stall
	if stall.backfillSince.IsZero() || backfilled != stall.backfill {
		// First sight, or the checkpoint advanced: a new stall, if any,
		// starts here, and whoever held the old one may be gone.
		c.noteWALCheckpointed()
		stall.backfill, stall.backfillSince = backfilled, now
		return nil
	}
	if walBytes < walPinBytes {
		c.wal.pin.Store(nil)
		return nil
	}
	pin := walPin{Since: stall.backfillSince, WALBytes: walBytes, Pending: pending}
	diagnose := stall.diagnosedAt.IsZero() || now.Sub(stall.diagnosedAt) >= walPinDiagnoseEvery
	if diagnose {
		var self bool
		stall.diagnosedAt = now
		stall.holder, stall.known, self = c.walHolder(stall.backfillSince, now)
		if self {
			stall.selfProbes++
		} else {
			stall.selfProbes = 0
		}
		if self && stall.selfProbes < 2 {
			// The blocking slot's reader may have just ended, or the slot
			// kept a mark nobody holds: say so until a later probe agrees.
			stall.holder, stall.known = "probably "+stall.holder, false
		}
	}
	pin.Diagnosed, pin.Holder, pin.Known = stall.diagnosedAt, stall.holder, stall.known
	if diagnose {
		fmt.Fprintf(os.Stderr, "Catalog WAL pinned: catalog=%s pid=%d %s\n", c.Path, os.Getpid(), pin)
	}
	c.wal.pin.Store(&pin)
	return &pin
}

// noteWALCheckpointed clears the diagnosis and the stall: a checkpoint
// succeeded, the log is small again, or the checkpoint advanced.
func (c *Catalog) noteWALCheckpointed() {
	c.wal.stall = walStall{}
	c.wal.pin.Store(nil)
}

// walPinned is the current diagnosis, or nil.
func (c *Catalog) walPinned() *walPin {
	return c.wal.pin.Load()
}

// walHolder names who keeps the checkpoint from advancing, and whether it
// could tell. Only a read lock on a blocking slot (marked where the
// checkpoint stopped, below the last frame) stops the copy: another process
// holding one is the holder. With none, this process holds it when a slot is
// blocking (POSIX locks never report the caller's own), and its running
// operations that started before the stall are the suspects. Otherwise, or
// when the probe fails, the holder is unknown and the suspects are listed
// as context. Other locks (the writer, a checkpoint, a recovery, a reader at
// the last frame) cannot stop the copy and are only mentioned.
// self reports that the holder named is this process.
func (c *Catalog) walHolder(since, now time.Time) (string, bool, bool) {
	suspects := c.walSuspects(since, now)
	withSuspects := func(holder, label string) string {
		if len(suspects) > 0 {
			holder += "; " + label + ": " + strings.Join(suspects, ", ")
		}
		return holder
	}
	probe, err := probeWAL(c.Path + "-shm")
	if err != nil {
		return withSuspects("unknown ("+err.Error()+")", "this process has been running since before the stall"), false, false
	}
	blocking := map[int]bool{}
	if probe.HeaderErr == nil {
		for _, slot := range probe.Header.blocking() {
			blocking[slot] = true
		}
	}
	holders, possible, context := []string{}, []string{}, []string{}
	add := func(list *[]string, name string) {
		if !slices.Contains(*list, name) {
			*list = append(*list, name)
		}
	}
	for _, lock := range probe.Locks {
		name := fmt.Sprintf("pid %d", lock.PID)
		if lock.Command != "" {
			name += " (" + lock.Command + ")"
		}
		slot := lock.Byte - walReadLockByte
		switch {
		case lock.Byte == walWriteLockByte:
			add(&context, name+" writing")
		case lock.Byte == walCheckpointByte:
			add(&context, name+" checkpointing")
		case slot < 0 || slot >= walReadSlots:
			add(&context, name+" recovering the log")
		case probe.HeaderErr != nil:
			add(&possible, name+" reading")
		case blocking[slot]:
			add(&holders, fmt.Sprintf("%s reading up to frame %s of %s", name, formatCount(int(probe.Header.ReadMarks[slot])), formatCount(int(probe.Header.MxFrame))))
		default:
			add(&context, name+" reading")
		}
	}
	also := func(holder string) string {
		if len(context) > 0 {
			holder += "; also open in " + strings.Join(context, ", ")
		}
		return holder
	}
	switch {
	case len(holders) > 0:
		return also("another process: " + strings.Join(holders, ", ")), true, false
	case len(blocking) > 0:
		holder := withSuspects(fmt.Sprintf("this process (pid %d)", os.Getpid()), "running since before the stall")
		return also(holder), true, true
	case len(possible) > 0:
		holder := "unknown (the wal-index header could not be read); possibly another process: " + strings.Join(possible, ", ")
		return also(withSuspects(holder, "this process has been running since before the stall")), false, false
	}
	holder := "unknown (no read slot is marked where the checkpoint stopped)"
	return also(withSuspects(holder, "this process has been running since before the stall")), false, false
}

// walSuspects lists this process's running operations that started before
// since, oldest first, by kind and age. Queued and blocked operations hold
// no snapshot.
func (c *Catalog) walSuspects(since, now time.Time) []string {
	views := []opView{}
	for _, op := range c.ops.running() {
		view := op.view()
		if view.State != opRunning && view.State != opStopping || view.Spec.Kind == "maintenance.checkpoint" || view.Started.After(since) {
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
