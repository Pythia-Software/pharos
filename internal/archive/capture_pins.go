package archive

// Pins and all record-store publication use the per-host capture flock. Lock
// order is index lock -> capture lock -> catalog transaction. A parser never
// holds the capture lock. Pins have no time based expiry.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const preservationPinsName = "preservation-pins.json"

type preservationPin struct {
	ReaderLease  string   `json:"reader_lease,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	ClosureBytes int64    `json:"closure_bytes"`
	ID           string   `json:"id"`
	Owner        string   `json:"owner"`
	Purpose      string   `json:"purpose"`
	Path         string   `json:"path"`
	SHA256       string   `json:"sha256"`
	Size         int64    `json:"size"`
	// Encoding is how the pinned file is stored; SHA256 and Size describe its
	// original bytes, so a check decodes it (capturedSHA256). Compression
	// never rewrites a pinned file, so this holds for the pin's lifetime.
	Encoding   string `json:"encoding,omitempty"`
	CreatedAt  string `json:"created_at"`
	ReleasedAt string `json:"released_at,omitempty"`
}
type preservationPins struct {
	pendingReaders []string
	Version        int               `json:"version"`
	Pins           []preservationPin `json:"pins"`
	// An optional job-owned capacity gate stops all new source acquisitions.
	// Backfill can instead reserve a finite remaining byte allowance. Spending
	// is persisted before acquisition, so a crash can waste allowance, never
	// manufacture capacity. Only the owning job may remove its gate.
	Gates map[string]int64 `json:"acquisition_allowances,omitempty"`
	// Job-owned native-only pauses compose without changing the operator schedule.
	NativePauses map[string]string `json:"native_snapshot_pauses,omitempty"`
}

func loadPreservationPins(dir string) (preservationPins, error) {
	p := preservationPins{Version: 1}
	b, err := os.ReadFile(filepath.Join(dir, preservationPinsName))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Version != 1 {
		return p, fmt.Errorf("unsupported preservation pins version %d; upgrade Pharos", p.Version)
	}
	live := p.Pins[:0]
	pending := map[string]bool{}
	for _, pin := range p.Pins {
		for _, dependency := range pin.Dependencies {
			if !safeRetainedPath(dependency) {
				return p, errors.New("unsafe pin dependency")
			}
		}
		if !safeRetainedPath(pin.Path) || pin.Size < 0 || !validRecordHash(pin.SHA256) || pin.Owner == "" {
			return p, errors.New("invalid preservation pin; capture stopped")
		}
		if pin.ReaderLease != "" {
			active, err := preservationReaderActive(dir, pin.ReaderLease)
			if err != nil {
				return p, err
			}
			if !active {
				if !pending[pin.Owner] {
					p.pendingReaders = append(p.pendingReaders, pin.Owner)
					pending[pin.Owner] = true
				}
				continue
			}
		}
		live = append(live, pin)
	}
	p.Pins = live
	return p, nil
}

func preservationReaderActive(dir, lease string) (bool, error) {
	parent := filepath.Dir(lease)
	if filepath.Base(lease) != ".reader.lock" || parent != filepath.Base(parent) || !strings.HasPrefix(parent, ".reader-pin-") || !strings.HasSuffix(parent, captureTempSuffix) {
		return false, errors.New("invalid preservation reader lease")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	lock, err := root.OpenFile(lease, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer lock.Close()
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}

// Caller owns the capture lock. Closed/crashed reader leases form a durable
// cleanup queue until their pin metadata has been rewritten successfully.
func cleanupPreservationReaders(dir string) error {
	pins, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	if len(pins.pendingReaders) > 0 {
		if err := savePreservationPins(dir, pins); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".reader-pin-") || !strings.HasSuffix(entry.Name(), captureTempSuffix) {
			continue
		}
		active, err := preservationReaderActive(dir, filepath.Join(entry.Name(), ".reader.lock"))
		if err != nil {
			return err
		}
		if !active {
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// pinReader publishes every reader kind with an OS-backed lifetime. Close is
// nonblocking during capture/backup contention; the next writer/replay retries
// cleanup, and status exposes any inactive owners still queued on disk.
// Caller owns the capture lock while creating the reader.
func (c *sourceCapture) pinReader(refs []recordObject) (func(), error) {
	if err := cleanupPreservationReaders(c.dir); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(c.dir, ".reader-pin-*"+captureTempSuffix)
	if err != nil {
		return nil, err
	}
	lease, err := os.OpenFile(filepath.Join(dir, ".reader.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		err = syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		if lease != nil {
			lease.Close()
		}
		os.RemoveAll(dir)
		return nil, err
	}
	owner := "reader:" + randomToken()
	previous := c.readerLease
	c.readerLease = filepath.Join(filepath.Base(dir), ".reader.lock")
	defer func() { c.readerLease = previous }()
	for _, ref := range refs {
		if err = newRecordStore(c).pinRevision(owner, ref); err != nil {
			lease.Close()
			_ = releasePreservationOwner(c.dir, owner)
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			lease.Close()
			lock, err := lockCaptureTarget(context.Background(), c.dir)
			if err == nil {
				defer releaseLockFile(lock)
				err = releasePreservationOwner(c.dir, owner)
			}
			if err == nil {
				err = os.RemoveAll(dir)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Pharos: reader pin cleanup deferred for %s: %v; the next capture or replay will retry\n", c.dir, err)
			}
		})
	}, nil
}
func safeRetainedPath(p string) bool {
	return filepath.ToSlash(filepath.Clean(p)) == p && !strings.ContainsAny(p, "\\\x00") && (strings.HasPrefix(p, "history/") || strings.HasPrefix(p, "records/v1/")) && !strings.Contains(p, "/../")
}
func savePreservationPins(dir string, p preservationPins) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := ensureFree(dir, int64(len(b))+4096); err != nil {
		return err
	}
	return writeFileDurably(filepath.Join(dir, preservationPinsName), b)
}
func pinnedPath(dir, rel string) (bool, error) {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return true, err
	}
	// A native pause protects the selected source inventory as a whole too.
	// Unreadable/corrupt inputs cannot acquire a hash pin, but must never be
	// swept out of history while their job still owns this durable overlay.
	if len(p.NativePauses) > 0 {
		return true, nil
	}
	for _, pin := range p.Pins {
		if pin.ReleasedAt == "" {
			if pin.Path == rel {
				return true, nil
			}
			for _, dependency := range pin.Dependencies {
				if dependency == rel {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
func fileSHA256(file string) (string, int64, error) {
	return fileSHA256Context(context.Background(), file)
}
func fileSHA256Context(ctx context.Context, file string) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := newRecordHash()
	n, err := io.Copy(h, &recordCountReader{r: f, ctx: ctx})
	return fmt.Sprintf("%x", h.Sum(nil)), n, err
}

// pinVersion must be called under the host lock. A latest path is first made
// immutable, even with capture_snapshot_generations=0. Overlapping owners
// get independent IDs and releases are idempotent.
func (c *sourceCapture) pinVersion(owner, purpose string, v capturedVersion, dependencies ...string) (preservationPin, error) {
	if owner == "" || purpose == "" {
		return preservationPin{}, errors.New("pin owner and purpose are required")
	}
	p, err := loadPreservationPins(c.dir)
	if err != nil {
		return preservationPin{}, err
	}
	// How the input is stored comes from this writer's inventory, not the
	// caller: a plan made before a compression still pins the right bytes. A
	// latest file keeps its encoding when its pin links it into history.
	encoding := c.encodingOf(v.Captured)
	if strings.HasPrefix(v.Captured, "files/") {
		digest, size, err := capturedSHA256(c.ctx, filepath.Join(c.dir, filepath.FromSlash(v.Captured)), encoding)
		if err != nil {
			return preservationPin{}, err
		}
		if size != v.Size || v.SHA256 != "" && digest != v.SHA256 {
			return preservationPin{}, errors.New("latest pin input changed; reconcile inventory")
		}
		v.SHA256 = digest
		v.Encoding = encoding
		latest := v.Captured
		v, err = c.pinHistoryVersion(v)
		if err != nil {
			return preservationPin{}, err
		}
		if err = c.recordPinnedCopy(latest, v); err != nil {
			return preservationPin{}, err
		}
	}
	if !safeRetainedPath(v.Captured) {
		return preservationPin{}, errors.New("pin input is not immutable")
	}
	file := filepath.Join(c.dir, filepath.FromSlash(v.Captured))
	digest, size, err := capturedSHA256(c.ctx, file, encoding)
	if err != nil {
		return preservationPin{}, err
	}
	if size != v.Size || v.SHA256 != "" && digest != v.SHA256 {
		return preservationPin{}, errors.New("pin input changed; reconcile inventory")
	}
	stored, err := os.Stat(file)
	if err != nil {
		return preservationPin{}, err
	}
	pin := preservationPin{ID: hashBytes([]byte(owner + "\x00" + v.Captured + "\x00" + digest)), Owner: owner, Purpose: purpose, Path: v.Captured, SHA256: digest, Size: size, Encoding: encoding, CreatedAt: now(), ReaderLease: c.readerLease}
	// The closure is what the pin keeps on disk.
	pin.ClosureBytes = stored.Size()
	seen := map[string]bool{v.Captured: true}
	for _, dependency := range dependencies {
		if seen[dependency] {
			continue
		}
		seen[dependency] = true
		if !safeRetainedPath(dependency) {
			return preservationPin{}, errors.New("unsafe pin dependency")
		}
		info, e := os.Stat(filepath.Join(c.dir, filepath.FromSlash(dependency)))
		if e != nil || !info.Mode().IsRegular() {
			return preservationPin{}, errors.New("pin dependency missing; reconcile inventory")
		}
		pin.Dependencies = append(pin.Dependencies, dependency)
		pin.ClosureBytes += info.Size()
	}
	sort.Strings(pin.Dependencies)
	for _, old := range p.Pins {
		if old.ID == pin.ID && old.ReleasedAt == "" {
			return old, nil
		}
	}
	// The manifest writer version makes earlier writers refuse the source.
	if err = c.upgradePreservationWriter(); err != nil {
		return preservationPin{}, err
	}
	p.Pins = append(p.Pins, pin)
	err = savePreservationPins(c.dir, p)
	return pin, err
}
func releasePreservationOwner(dir, owner string) error {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	for i := range p.Pins {
		if p.Pins[i].Owner == owner && p.Pins[i].ReleasedAt == "" {
			p.Pins[i].ReleasedAt = now()
		}
	}
	// Reader leases have no audit value after close. Keep operator/job release
	// receipts, but do not accumulate every historical reader's full closure.
	p.Pins = slices.DeleteFunc(p.Pins, func(pin preservationPin) bool {
		return strings.HasPrefix(pin.Owner, "reader:") && pin.ReleasedAt != ""
	})
	return savePreservationPins(dir, p)
}
func reserveCaptureAcquisition(dir string, bytes int64) error {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	for _, left := range p.Gates {
		if left < bytes {
			return fmt.Errorf("capture pending: preservation job capacity exhausted (needs %d bytes)", bytes)
		}
	}
	if len(p.Gates) == 0 {
		return nil
	}
	for owner, left := range p.Gates {
		p.Gates[owner] = left - bytes
	}
	return savePreservationPins(dir, p)
}

// lockCaptureTarget also works off-host. Never call it from a captureSession:
// that session already owns this flock.
func lockCaptureTarget(ctx context.Context, dir string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(dir), captureLockName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errCaptureBusy
		}
		return nil, err
	}
	opFrom(ctx).holdLock(f)
	return f, nil
}

// waitCaptureLock retries acquire while a capture holds the lock, until ctx
// ends; automatic runs do not wait. A wait reports its phase, naming the Mac
// whose capture holds the lock, so a run that waits says so instead of
// looking stuck; cancelling ctx (Stop, or a release) ends the wait.
func waitCaptureLock(ctx context.Context, host string, acquire func() (*os.File, error)) (*os.File, error) {
	waiting := false
	op := opFrom(ctx)
	for {
		lock, err := acquire()
		if !errors.Is(err, errCaptureBusy) || modeOf(ctx).automatic {
			if waiting {
				op.Unblock()
			}
			return lock, err
		}
		if !waiting {
			waiting = true
			reportWork(ctx, captureWaitPhase(host), "", 0, -1)
			// After the report, which mirrors a new step into op.
			op.Block("capture-running", "the capture on "+defaultString(host, "another Mac"))
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			op.Unblock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// captureWaitPhase is the phase of work waiting for host's capture.
func captureWaitPhase(host string) string {
	return "waiting for the capture on " + defaultString(host, "another Mac") + " to finish"
}

func lockCaptureReader(ctx context.Context, target captureTarget) (*os.File, error) {
	return waitCaptureLock(ctx, defaultString(target.Host.Label, target.Host.ID), func() (*os.File, error) { return lockCaptureTarget(ctx, target.Dir) })
}

func beginInputCapture(ctx context.Context, config Config) (*captureSession, error) {
	var session *captureSession
	_, err := waitCaptureLock(ctx, currentHost().Label, func() (*os.File, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		session, err = beginCapture(config)
		if err != nil {
			return nil, err
		}
		opFrom(ctx).holdLock(session.lock)
		return session.lock, nil
	})
	return session, err
}

// Reader/job bookkeeping must not claim a new capture observation or overwrite
// its last-run diagnostics. Only the writer compatibility version changes.
func (c *sourceCapture) upgradePreservationWriter() error {
	if c.manifest.Version >= c.manifest.writerVersion() {
		return nil
	}
	c.manifest.Version = c.manifest.writerVersion()
	data, e := json.Marshal(c.manifest)
	if e != nil {
		return e
	}
	return writeRecordMetadata(filepath.Join(c.dir, captureManifestName), data)
}

var errNativeSnapshotPaused = errors.New("native snapshot pending: historical preservation owns a pause; resume or inspect the backfill job")

func checkNativeSnapshotPause(dir string) error {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	if len(p.NativePauses) > 0 {
		return errNativeSnapshotPaused
	}
	return nil
}

// recordPinnedCopy lists in the manifest the history copy a pin made of an
// encoded latest file or snapshot, as an earlier version or a generation with
// its encoding, stored size and digest. Unlisted, it would be an orphan
// whose raw bytes no planner recognises, and the manifest's version could
// fall below the one that describes it. A plain copy needs no entry: its
// bytes are the original's. Listed last, it changes no retention order; the
// next rotation of the same capture lands on the same name, and is not
// listed twice.
func (c *sourceCapture) recordPinnedCopy(latest string, v capturedVersion) error {
	if v.Encoding == "" {
		return nil
	}
	info, err := os.Stat(filepath.Join(c.dir, filepath.FromSlash(v.Captured)))
	if err != nil {
		return err
	}
	v.StoredSize = info.Size()
	listed := false
	if snapshot := c.snapshots[latest]; snapshot != nil {
		v.Source = &snapshot.Source
		snapshot.Generations, listed = appendVersion(snapshot.Generations, v)
	} else if file := c.files[latest]; file != nil {
		v.Source = nil
		file.Previous, listed = appendVersion(file.Previous, v)
	}
	if !listed {
		return nil
	}
	return writeCaptureManifest(c.dir, c.manifest)
}

// appendVersion appends v unless versions already names its path.
func appendVersion(versions []capturedVersion, v capturedVersion) ([]capturedVersion, bool) {
	for _, existing := range versions {
		if existing.Captured == v.Captured {
			return versions, false
		}
	}
	return append(versions, v), true
}

// withoutVersion drops the entries naming path (a pinned copy that a
// rotation now lists in its own place).
func withoutVersion(versions []capturedVersion, path string) []capturedVersion {
	kept := versions[:0:0]
	for _, version := range versions {
		if version.Captured != path {
			kept = append(kept, version)
		}
	}
	return kept
}

// Pinning cannot rename the latest input out from under its manifest on a
// filesystem without hard links (notably exFAT). Copy and flush instead.
var pinLinkFile = os.Link

func (c *sourceCapture) pinHistoryVersion(v capturedVersion) (capturedVersion, error) {
	stamp := v.CapturedAt
	if parsed, e := time.Parse(time.RFC3339Nano, stamp); e == nil {
		stamp = parsed.UTC().Format(captureStampLayout)
	}
	rel := path.Join(captureHistoryDir, stamp, strings.TrimPrefix(v.Captured, captureFilesDir+"/"))
	from, to := filepath.Join(c.dir, filepath.FromSlash(v.Captured)), filepath.Join(c.dir, filepath.FromSlash(rel))
	if e := os.MkdirAll(filepath.Dir(to), 0700); e != nil {
		return v, e
	}
	if e := pinLinkFile(from, to); e != nil && !errors.Is(e, os.ErrExist) {
		if e = c.ensureSpace(v.Size); e != nil {
			return v, e
		}
		input, e := os.Open(from)
		if e != nil {
			return v, e
		}
		defer input.Close()
		temp := to + captureTempSuffix
		out, e := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return v, e
		}
		_, e = io.Copy(out, &recordCountReader{r: input, ctx: c.ctx})
		if e == nil {
			e = out.Sync()
		}
		if closeErr := out.Close(); e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(temp, to)
		}
		if e != nil {
			_ = os.Remove(temp)
			return v, e
		}
	}
	hash, size, e := capturedSHA256(c.ctx, to, v.Encoding)
	if e != nil {
		return v, e
	}
	if size != v.Size || v.SHA256 != "" && hash != v.SHA256 {
		return v, errors.New("immutable pin copy disagrees with plan; reconcile inputs")
	}
	if e = c.carrySQLiteExportReceipt(v.Captured, rel); e != nil {
		return v, e
	}
	v.Captured = rel
	v.SupersededAt = now()
	return v, nil
}
