package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Compressing captured files at rest (docs/capture-compression.md). Phase 1:
// superseded snapshot generations, read again only by a forced reindex,
// salvage of sessions deleted at the source, record exports, history
// backfill, pins and transfer, all of which decode them first. Phase 2:
// earlier versions of captured files, and cold latest transcripts that the
// index has recorded and reads again only when they change; adapters read
// them through the capture view (capture_fs.go). Phase 3: idle latest
// database snapshots their adapter does not open on every index
// (snapshotReader). Each is stored zstd-compressed under its own name.
//
// Order, for one file:
//
//  1. encode it into a flocked reader lease of its own (which no capture's
//     sweep removes while it is held), hashing the original bytes as read;
//  2. flush the encoded file to the drive (F_FULLFSYNC);
//  3. verify it by decoding and hashing;
//  4. under the host's capture lock, re-check that the manifest still names
//     the same, unpinned, indexed generation, and replace the manifest
//     (encoding, stored_size, sha256; version 6) after a full device flush;
//  5. still under the lock, rename the encoded file over the name.
//
// The manifest commits first so that whenever the name may hold zstd, the
// manifest already has the version older builds refuse. A kill before the
// manifest leaves the original and an abandoned lease that sweeps remove. A
// kill between the manifest and the rename leaves an entry marked encoded
// over the original bytes, which readers recognise (storedAs) and the next
// pass corrects. Encoding runs without the capture lock, so a capture is
// never kept waiting for minutes; a pass waits for a running capture before
// its commit instead of throwing the encoding away. Readers holding the old
// inode through a lease keep reading the original bytes.

var (
	// captureCompressInterval spaces the daemon's passes; the first runs
	// captureCompressDelay after the service starts.
	captureCompressInterval = 15 * time.Minute
	captureCompressDelay    = 2 * time.Minute
	// captureCompressThreads bounds the encoder's goroutines.
	captureCompressThreads = 4
	// captureCompressAttempts is how many failed verifications of one
	// generation a pass tolerates before it stops trying.
	captureCompressAttempts = 3
)

// captureCompressLevel is zstd level 3: about 400 MB/s here, so the drive,
// not the CPU, bounds it.
const captureCompressLevel = 3

// compressedGeneration is one generation a pass compressed.
type compressedGeneration struct {
	Source       string
	Path         string
	Size, Stored int64
}

// Outcomes a pass remembers, so that it does not read a large generation
// again every 15 minutes to reach the same answer.
var (
	errCompressIncompressible = errors.New("generation does not shrink")
	errCompressMismatch       = errors.New("generation does not match its manifest digest")
	errCompressVerify         = errors.New("encoded generation failed verification")
)

// compressCaptures compresses the eligible snapshot generations of this
// Mac's captures. Other Macs' captures are left to their own Macs: a manifest
// with an encoded entry stops any build that cannot decode it, and only the
// capturing Mac's own build is known to be new enough to keep capturing.
// A library that keeps pending evidence for delivery is left alone. Every
// pass first removes abandoned reader leases under all hosts' captures.
func compressCaptures(ctx context.Context, config Config, catalog *Catalog, done func(compressedGeneration)) error {
	if config.CaptureRoot == "" {
		return nil
	}
	sweepAbandonedLeases(config.CaptureRoot)
	if config.PendingCapture {
		return nil
	}
	if d, err := readLibraryDescriptor(config); err == nil && d.RetainPendingEvidence {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	host := currentHost()
	if host.Fallback || !validCaptureName(host.ID) {
		return nil
	}
	policy := compressPolicy{afterDays: config.CaptureCompressAfterDays, now: time.Now()}
	adapters := map[string]Adapter{}
	policy.readEveryIndex = func(source, rel string) bool {
		target := captureTarget{Host: host, Source: source, Dir: filepath.Join(config.CaptureRoot, host.ID, source)}
		adapter, ok := adapters[source]
		if !ok {
			adapter, _, _, _ = openCaptureAdapter(target)
			adapters[source] = adapter
		}
		reader, ok := adapter.(snapshotReader)
		return !ok || reader.readsEveryIndex(filepath.Join(target.Dir, filepath.FromSlash(rel)))
	}
	if catalog != nil {
		// Recorded by the current extractor: after an extractor change the
		// next index parses every part again, so nothing is cold until then.
		extractors := map[string]string{}
		extractor := func(source string) string {
			if value, ok := extractors[source]; ok {
				return value
			}
			value := ""
			target := captureTarget{Host: host, Source: source, Dir: filepath.Join(config.CaptureRoot, host.ID, source)}
			if adapter, _, _, err := openCaptureAdapter(target); err == nil {
				value, _ = catalog.effectiveIndexVersion(host.ID, source, adapter)
			}
			extractors[source] = value
			return value
		}
		policy.indexed = func(source, item string, size, version int64) bool {
			current := extractor(source)
			if current == "" {
				return false
			}
			own, offHost := extractorVariants(current)
			var found int
			err := catalog.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM source_item_states WHERE host_id=? AND source_name=? AND item=? AND extractor IN (?,?) AND size=? AND version=?", host.ID, source, item, own, offHost, size, version).Scan(&found)
			return err == nil && found > 0
		}
	}
	hostDir := filepath.Join(config.CaptureRoot, host.ID)
	entries, err := os.ReadDir(hostDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !validCaptureName(entry.Name()) {
			continue
		}
		dir := filepath.Join(hostDir, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, captureManifestName)); err != nil {
			continue
		}
		if err := compressSourceFiles(ctx, captureTarget{Host: host, Source: entry.Name(), Dir: dir}, policy, done); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

// compressPolicy decides which latest files are cold (phase 2).
type compressPolicy struct {
	// afterDays is capture_compress_after_days; 0 leaves latest files alone.
	afterDays int
	now       time.Time
	// indexed reports that source_item_states holds this exact version of a
	// part, so the next index will not read it; nil (no catalog) is never.
	indexed func(source, item string, size, version int64) bool
	// readEveryIndex reports that the source's adapter opens the captured
	// database at rel whenever its capture is indexed, changed or not
	// (snapshotReader); nil keeps every latest snapshot uncompressed.
	readEveryIndex func(source, rel string) bool
}

// snapshotReader is how an adapter says which of its captured databases its
// index opens on every run, whether or not they changed: those must stay
// uncompressed, or every index would decode them (phase 3). An adapter
// without it is taken to read them all.
type snapshotReader interface {
	readsEveryIndex(path string) bool
}

// extractorVariants is an index version as this Mac's index records it and
// as another Mac's index of the same capture does (with "@offhost", before
// any "@generation:N"). Either one recording a part's exact size and version
// makes the file cold: both ran the same parser over the same bytes. An
// "@offhost" record alone does not stop this Mac's own index of the capture
// from parsing the file once more (its tracker matches extractors exactly,
// to fill in what only this Mac's Git checkouts know), which then decodes it
// once; most files another Mac indexed are never indexed here again.
func extractorVariants(version string) (own, offHost string) {
	head, generation := version, ""
	if index := strings.Index(version, "@generation:"); index >= 0 {
		head, generation = version[:index], version[index:]
	}
	head = strings.TrimSuffix(head, "@offhost")
	return head + generation, head + "@offhost" + generation
}

// compressMinBytes is the smallest file worth compressing.
var compressMinBytes int64 = 64 << 10

// compressCandidate is one captured file a pass may store encoded: a snapshot
// generation, an earlier version of a file, or a cold latest file.
type compressCandidate struct {
	kind     string // generation, version or file
	database string // a generation's database
	v        capturedVersion
}

// compressCandidates lists the entries of manifest a pass considers.
func compressCandidates(manifest *captureManifest) []compressCandidate {
	candidates := []compressCandidate{}
	for _, snapshot := range manifest.Snapshots {
		candidates = append(candidates, compressCandidate{kind: "snapshot", database: snapshot.Path, v: snapshot.version()})
		for _, generation := range snapshot.Generations {
			candidates = append(candidates, compressCandidate{kind: "generation", database: snapshot.Path, v: generation})
		}
	}
	for _, file := range manifest.Files {
		candidates = append(candidates, compressCandidate{kind: "file", v: file.version()})
		for _, version := range file.Previous {
			candidates = append(candidates, compressCandidate{kind: "version", v: version})
		}
	}
	return candidates
}

// locate finds the candidate's entry in manifest, as it is now, and how to
// record its encoding there.
func (c compressCandidate) locate(manifest *captureManifest) (capturedVersion, func(encoding string, stored int64, hash string), bool) {
	switch c.kind {
	case "generation":
		if entry := currentGeneration(manifest, c.database, c.v.Captured); entry != nil {
			return *entry, func(encoding string, stored int64, hash string) {
				entry.Encoding, entry.StoredSize, entry.SHA256 = encoding, stored, hash
			}, true
		}
	case "version":
		if entry, ok := manifest.version(c.v.Captured); ok && !strings.HasPrefix(c.v.Captured, captureFilesDir+"/") {
			return *entry, func(encoding string, stored int64, hash string) {
				entry.Encoding, entry.StoredSize, entry.SHA256 = encoding, stored, hash
			}, true
		}
	case "file":
		for _, file := range manifest.Files {
			if file.Captured == c.v.Captured {
				return file.version(), func(encoding string, stored int64, hash string) {
					file.Encoding, file.StoredSize = encoding, stored
				}, true
			}
		}
	case "snapshot":
		for _, snapshot := range manifest.Snapshots {
			if snapshot.Captured == c.v.Captured && snapshot.Path == c.database {
				return snapshot.version(), func(encoding string, stored int64, hash string) {
					snapshot.Encoding, snapshot.StoredSize, snapshot.SHA256 = encoding, stored, hash
				}, true
			}
		}
	}
	return capturedVersion{}, nil, false
}

// unchanged reports that now is the entry that was encoded.
func (c compressCandidate) unchanged(now capturedVersion) bool {
	return now.Encoding == "" && now.Size == c.v.Size && now.CapturedAt == c.v.CapturedAt && now.MTimeNS == c.v.MTimeNS && (c.v.SHA256 == "" || now.SHA256 == c.v.SHA256)
}

// eligible is whether the candidate may be stored encoded now.
func (c compressCandidate) eligible(dir string, manifest *captureManifest, policy compressPolicy) bool {
	switch c.kind {
	case "generation":
		return compressible(dir, manifest, c.database, c.v)
	case "version":
		// Superseded, so cold by definition; no parse reads it again.
		return c.v.Encoding == "" && strings.HasPrefix(c.v.Captured, captureHistoryDir+"/") && c.v.Size >= compressMinBytes && !pinnedOrUnknown(dir, c.v.Captured)
	case "file":
		var file *capturedFile
		for _, f := range manifest.Files {
			if f.Captured == c.v.Captured {
				file = f
			}
		}
		return file != nil && coldLatest(dir, manifest, file, policy)
	case "snapshot":
		for _, snapshot := range manifest.Snapshots {
			if snapshot.Captured == c.v.Captured && snapshot.Path == c.database {
				return idleSnapshot(dir, manifest, snapshot, policy)
			}
		}
	}
	return false
}

// lastRunSaw reports a finished last capture that did not fail on the source
// file at path: one whose errors name other files only. An error naming no
// file (the drive, the whole source) may have stopped it before path.
func lastRunSaw(run *captureRunRecord, path string) bool {
	if run == nil || run.FinishedAt == "" {
		return false
	}
	for _, failure := range run.Errors {
		if failure.Path == "" || failure.Path == path {
			return false
		}
	}
	return true
}

// idleSnapshot is whether a latest database snapshot is idle: its recorded
// source state (database and WAL mtimes) is older than policy.afterDays, the
// last capture found it unchanged (or it left its source), the index marker
// covers it, it is not pinned, and its adapter does not open it on every
// index (snapshotReader). SQLite-record sources are left out: their capture
// exports the latest snapshot on every run.
func idleSnapshot(dir string, manifest *captureManifest, snapshot *capturedSnapshot, policy compressPolicy) bool {
	if policy.afterDays <= 0 || policy.readEveryIndex == nil || manifest.SQLiteRecordCapture || snapshot.Encoding != "" || snapshot.Size < compressMinBytes {
		return false
	}
	changed := snapshot.Source.version()
	if changed <= 0 || time.Unix(0, changed).After(policy.now.AddDate(0, 0, -policy.afterDays)) {
		return false
	}
	if snapshot.MissingSince == "" {
		run := manifest.LastRun
		if run == nil || run.FinishedAt == "" {
			return false
		}
		captured, ok := parseTime(snapshot.CapturedAt)
		started, ok2 := parseTime(run.StartedAt)
		if !ok || !ok2 || !captured.Before(started) {
			return false // taken by the last capture: not yet seen unchanged
		}
	}
	if marker := loadIndexMarker(dir); !marker.exists || !marker.has(snapshot.Path, snapshot.CapturedAt) {
		return false
	}
	if pinnedOrUnknown(dir, snapshot.Captured) {
		return false
	}
	return !policy.readEveryIndex(manifest.Source.Name, snapshot.Captured)
}

func pinnedOrUnknown(dir, rel string) bool {
	pinned, err := pinnedPath(dir, rel)
	return err != nil || pinned
}

// coldLatest is whether a latest file is cold: a transcript kind the index
// parses only when it changes (compressibleLatest), whose source has not
// changed for policy.afterDays, that the last capture found unchanged (or
// that left its source), and whose current version the index has recorded,
// so the next index will not read it. Sources with retained-input capture
// are left out: their durable revisions keep the original inode anyway.
func coldLatest(dir string, manifest *captureManifest, file *capturedFile, policy compressPolicy) bool {
	if policy.afterDays <= 0 || policy.indexed == nil || manifest.RetainedInputCapture || file.Encoding != "" || file.Size < compressMinBytes || !compressibleLatest(manifest.Source.Kind, file.Captured) {
		return false
	}
	if time.Unix(0, file.MTimeNS).After(policy.now.AddDate(0, 0, -policy.afterDays)) {
		return false
	}
	if file.MissingSince == "" {
		run := manifest.LastRun
		if !lastRunSaw(run, file.Path) {
			return false
		}
		captured, ok := parseTime(file.CapturedAt)
		started, ok2 := parseTime(run.StartedAt)
		if !ok || !ok2 || !captured.Before(started) {
			return false // copied by the last capture: not yet seen unchanged
		}
	}
	if pinnedOrUnknown(dir, file.Captured) {
		return false
	}
	return policy.indexed(manifest.Source.Name, file.Path, file.Size, file.MTimeNS)
}

// compressibleLatest is the allowlist of latest files that may be stored
// encoded: transcripts the index parses only when their own size or mtime
// changes. Index and name files every plan reads (Claude sessions-index.json,
// Codex session_index.jsonl), run logs and annotations stay plain, as do TL1
// transcripts, which its index parses whenever the database changes.
func compressibleLatest(kind, rel string) bool {
	name := path.Base(rel)
	if !strings.HasPrefix(rel, captureFilesDir+"/") || !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	switch strings.ToLower(kind) {
	case "claude":
		return true
	case "codex":
		return strings.HasPrefix(name, "rollout-")
	case "antigravity":
		return name == "transcript_full.jsonl" || name == "transcript.jsonl"
	}
	return false
}

// compressSourceFiles corrects what a killed pass left, then compresses each
// eligible entry of one captured source. A failure of one entry does not stop
// the others; each is reported.
func compressSourceFiles(ctx context.Context, target captureTarget, policy compressPolicy, done func(compressedGeneration)) error {
	if err := correctUnrenamedEntries(ctx, target); err != nil && !errors.Is(err, errCaptureBusy) {
		return err
	}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return err
	}
	if manifest.Version == 0 || manifest.Host.ID != target.Host.ID || manifest.Source.Name != target.Source {
		return nil
	}
	outcomes := loadCompressionOutcomes(target.Dir)
	var failures []error
	for _, candidate := range compressCandidates(manifest) {
		if err := ctx.Err(); err != nil {
			return err
		}
		generation := candidate.v
		if outcomes.settled(target.Dir, generation) || !candidate.eligible(target.Dir, manifest, policy) {
			continue
		}
		stored, err := compressEntry(ctx, target, manifest, candidate, policy, outcomes)
		switch {
		case err == nil:
			if stored > 0 && done != nil {
				done(compressedGeneration{Source: target.Source, Path: generation.Captured, Size: generation.Size, Stored: stored})
			}
		case errors.Is(err, errCompressIncompressible):
			outcomes.record(generation, "incompressible", "")
		case errors.Is(err, errCompressMismatch):
			// Evidence that disagrees with its manifest is reported once,
			// not compressed and not read again for this purpose.
			outcomes.record(generation, "mismatch", err.Error())
			failures = append(failures, err)
		case errors.Is(err, errCompressVerify):
			outcomes.failed(target.Dir, generation, err.Error())
			failures = append(failures, err)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			failures = append(failures, fmt.Errorf("%s: %w", generation.Captured, err))
		}
	}
	if err := outcomes.save(target.Dir, manifest); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// compressible is whether a generation may be stored encoded: one that
// retention does not treat as the only copy of anything still to be read.
// That is a superseded, plain generation that is not pinned (a pin, or a
// backfill's native pause, holds its exact bytes) and that the index marker
// covers when the source has one. A SQLite-record source also needs the
// generation's export verified (compressGeneration checks that).
func compressible(dir string, manifest *captureManifest, database string, generation capturedVersion) bool {
	if generation.Encoding != "" || !strings.HasPrefix(generation.Captured, captureHistoryDir+"/") || generation.Size <= 0 {
		return false
	}
	if marker := loadIndexMarker(dir); marker.exists && !marker.has(database, generation.CapturedAt) {
		return false
	}
	if pinned, err := pinnedPath(dir, generation.Captured); err != nil || pinned {
		return false
	}
	return true
}

// compressEntry stores one captured file encoded, in the order above. It
// returns the encoded size, or 0 when the file was left as it was (it
// changed, was pruned or pinned meanwhile, or awaits a verified export).
func compressEntry(ctx context.Context, target captureTarget, manifest *captureManifest, candidate compressCandidate, policy compressPolicy, outcomes *compressionOutcomes) (int64, error) {
	generation := candidate.v
	rel := generation.Captured
	file := filepath.Join(target.Dir, filepath.FromSlash(rel))
	fault := func(stage string) error {
		if captureFault != nil {
			return captureFault(stage, rel)
		}
		return nil
	}
	if manifest.SQLiteRecordCapture && candidate.kind == "generation" {
		c, err := sourceCaptureForTarget(ctx, target)
		if err != nil {
			return 0, err
		}
		if _, verified := c.verifiedSnapshotReceipt(rel, rel); !verified {
			if receiptStamp(target.Dir, rel) == noReceipt {
				// No export yet: checked again once a receipt appears.
				outcomes.unverified(target.Dir, generation)
				return 0, nil
			}
			// A receipt that does not verify may be a passing failure (a
			// timeout, a read error): retried like a failed verification.
			return 0, fmt.Errorf("%w: %s: its SQLite export receipt does not verify", errCompressVerify, rel)
		}
	}
	source, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	defer source.Close()
	original, err := source.Stat()
	if err != nil {
		return 0, err
	}
	if !original.Mode().IsRegular() || original.Size() != generation.Size {
		return 0, nil // not what the manifest says; validation reports that
	}
	lease, leaseLock, err := lockedLease(target.Dir)
	if err != nil {
		return 0, err
	}
	// A kill releases the lease's flock, as this deferred close does when a
	// test simulates one, and leaves the directory for a sweep. Every other
	// way out removes it.
	defer leaseLock.Close()
	abandon := func(err error) (int64, error) {
		_ = os.RemoveAll(lease)
		return 0, err
	}
	encoded := filepath.Join(lease, filepath.Base(file))
	out, err := os.OpenFile(encoded, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return abandon(err)
	}
	writer := &spaceCheckedWriter{w: out, dir: target.Dir}
	encoder, err := zstd.NewWriter(writer, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(captureCompressLevel)), zstd.WithEncoderConcurrency(captureCompressThreads))
	if err != nil {
		out.Close()
		return abandon(err)
	}
	digest := sha256.New()
	n, err := io.Copy(encoder, io.TeeReader(&recordCountReader{r: source, ctx: ctx}, digest))
	if err == nil {
		err = fault("compress-encode")
	}
	if err == nil {
		err = encoder.Close()
	} else {
		encoder.Close()
	}
	if err == nil && n != generation.Size {
		err = errors.New("generation changed while it was compressed")
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	if err == nil && generation.SHA256 != "" && hash != generation.SHA256 {
		err = fmt.Errorf("%w: %s", errCompressMismatch, rel)
	}
	if err == nil {
		err = os.Chtimes(encoded, original.ModTime(), original.ModTime())
	}
	if err == nil {
		// F_FULLFSYNC: the encoded bytes are on the drive before the rename
		// can replace the only copy of the original.
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return abandon(err)
	}
	if err = fault("compress-verify"); err != nil {
		return abandon(err)
	}
	stored, err := os.Stat(encoded)
	if err != nil {
		return abandon(err)
	}
	if stored.Size() >= generation.Size {
		return abandon(fmt.Errorf("%w: %s", errCompressIncompressible, rel))
	}
	verified, size, err := capturedSHA256(ctx, encoded, captureEncodingZstd)
	if err != nil && ctx.Err() == nil {
		err = fmt.Errorf("%w: %s: %v", errCompressVerify, rel, err)
	}
	if err == nil && (size != generation.Size || verified != hash) {
		err = fmt.Errorf("%w: %s", errCompressVerify, rel)
	}
	if err != nil {
		return abandon(err)
	}

	// A running capture holds the lock for its whole run: wait for it rather
	// than discard the encoding.
	lock, err := waitCaptureLock(ctx, defaultString(target.Host.Label, target.Host.ID), func() (*os.File, error) { return lockCaptureTarget(ctx, target.Dir) })
	if err != nil {
		return abandon(err)
	}
	defer releaseLockFile(lock)
	// What was encoded must still be what the manifest names: a capture may
	// have pruned, replaced or pinned it, or a backfill paused it, meanwhile.
	current, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return abandon(err)
	}
	now, record, ok := candidate.locate(current)
	if !ok || !candidate.unchanged(now) || !(compressCandidate{kind: candidate.kind, database: candidate.database, v: now}).eligible(target.Dir, current, policy) {
		return abandon(nil)
	}
	if same, err := os.Stat(file); err != nil || !os.SameFile(same, original) {
		return abandon(nil)
	}
	record(captureEncodingZstd, stored.Size(), hash)
	if err = fault("compress-manifest"); err != nil {
		return abandon(err)
	}
	if err = writeCaptureManifest(target.Dir, current); err != nil {
		return abandon(err)
	}
	if err = fault("compress-rename"); err != nil {
		return abandon(err)
	}
	if err = os.Rename(encoded, file); err != nil {
		return abandon(err)
	}
	syncDir(filepath.Dir(file))
	_ = os.RemoveAll(lease)
	return stored.Size(), nil
}

// correctUnrenamedEntries returns to plain the entries a killed pass marked
// encoded but never renamed: their files still hold the original bytes
// (storedAs). The check under the lock is a stat and a header read; the entry
// keeps the digest the pass recorded, which describes those bytes.
func correctUnrenamedEntries(ctx context.Context, target captureTarget) error {
	unrenamed := func(manifest *captureManifest) bool {
		found := false
		plain := func(v capturedVersion) (capturedVersion, bool) {
			stored := storedAs(filepath.Join(target.Dir, filepath.FromSlash(v.Captured)), v)
			return stored, stored.Encoding != v.Encoding
		}
		for _, snapshot := range manifest.Snapshots {
			if stored, changed := plain(snapshot.version()); changed {
				snapshot.Encoding, snapshot.StoredSize, found = stored.Encoding, stored.StoredSize, true
			}
			for i, generation := range snapshot.Generations {
				if stored, changed := plain(generation); changed {
					snapshot.Generations[i], found = stored, true
				}
			}
		}
		for _, file := range manifest.Files {
			if stored, changed := plain(file.version()); changed {
				file.Encoding, file.StoredSize, found = stored.Encoding, stored.StoredSize, true
			}
			for i, version := range file.Previous {
				if stored, changed := plain(version); changed {
					file.Previous[i], found = stored, true
				}
			}
		}
		return found
	}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil || !unrenamed(manifest) {
		return err
	}
	lock, err := lockCaptureTarget(ctx, target.Dir)
	if err != nil {
		return err
	}
	defer releaseLockFile(lock)
	if manifest, err = loadCaptureManifest(target.Dir); err != nil || !unrenamed(manifest) {
		return err
	}
	return writeCaptureManifest(target.Dir, manifest)
}

func currentGeneration(manifest *captureManifest, database, rel string) *capturedVersion {
	for _, snapshot := range manifest.Snapshots {
		if snapshot.Path != database {
			continue
		}
		for i := range snapshot.Generations {
			if snapshot.Generations[i].Captured == rel {
				return &snapshot.Generations[i]
			}
		}
	}
	return nil
}

// writeCaptureManifest replaces a manifest that this caller (holding the
// capture lock) changed only in its entries' storage. It keeps the run
// record and times, which describe captures, not this bookkeeping.
func writeCaptureManifest(dir string, manifest *captureManifest) error {
	manifest.Version = manifest.writerVersion()
	data, err := json.MarshalIndent(manifest, "", " ")
	if err != nil {
		return err
	}
	if err = ensureFree(dir, int64(len(data))+4096); err != nil {
		return err
	}
	return writeFileDurably(filepath.Join(dir, captureManifestName), append(data, '\n'))
}

// compressionOutcomesName, in a captured source's directory, remembers the
// generations a pass decided not to compress, keyed by captured path and
// identified by captured_at and size. Only the compression pass writes it;
// losing it costs one more read of each generation it named.
const compressionOutcomesName = "compression.json"

type compressionOutcome struct {
	CapturedAt string `json:"captured_at"`
	Size       int64  `json:"size"`
	// Outcome is incompressible, mismatch (the bytes disagree with the
	// manifest's digest), failed (verification; Failures counts), or
	// unverified (a SQLite-record generation with no export receipt yet;
	// Receipt is "none"). A receipt that does not verify counts as failed; a
	// failed entry records the receipt's digest ("none" without one) and is
	// tried again once it changes: digests, since HFS+ mtimes are whole seconds.
	Outcome  string `json:"outcome"`
	Failures int    `json:"failures,omitempty"`
	Receipt  string `json:"receipt_sha256,omitempty"`
	Detail   string `json:"detail,omitempty"`
	At       string `json:"at"`
}

type compressionOutcomes struct {
	Version  int                           `json:"version"`
	Entries  map[string]compressionOutcome `json:"generations"`
	modified bool
}

func loadCompressionOutcomes(dir string) *compressionOutcomes {
	outcomes := &compressionOutcomes{}
	if data, err := os.ReadFile(filepath.Join(dir, compressionOutcomesName)); err == nil {
		_ = json.Unmarshal(data, outcomes)
	}
	if outcomes.Version != 1 || outcomes.Entries == nil {
		outcomes.Version, outcomes.Entries = 1, map[string]compressionOutcome{}
	}
	return outcomes
}

func (o *compressionOutcomes) lookup(generation capturedVersion) (compressionOutcome, bool) {
	entry, ok := o.Entries[generation.Captured]
	return entry, ok && entry.CapturedAt == generation.CapturedAt && entry.Size == generation.Size
}

// settled reports a generation whose last outcome still stands.
func (o *compressionOutcomes) settled(dir string, generation capturedVersion) bool {
	entry, ok := o.lookup(generation)
	switch {
	case !ok:
		return false
	case entry.Outcome == "failed":
		// A new export receipt is a new chance (it may verify now).
		return entry.Failures >= captureCompressAttempts && receiptStamp(dir, generation.Captured) == entry.Receipt
	case entry.Outcome == "unverified":
		// Settled only while there is still no receipt to verify.
		return entry.Receipt == noReceipt && receiptStamp(dir, generation.Captured) == noReceipt
	}
	return true
}

func (o *compressionOutcomes) record(generation capturedVersion, outcome, detail string) {
	entry, _ := o.lookup(generation)
	entry.CapturedAt, entry.Size, entry.Outcome, entry.Detail, entry.At = generation.CapturedAt, generation.Size, outcome, detail, now()
	o.Entries[generation.Captured] = entry
	o.modified = true
}

// failed counts a failed verification, afresh once the generation's export
// receipt changes.
func (o *compressionOutcomes) failed(dir string, generation capturedVersion, detail string) {
	stamp := receiptStamp(dir, generation.Captured)
	entry, ok := o.lookup(generation)
	failures := 1
	if ok && entry.Outcome == "failed" && entry.Receipt == stamp {
		failures = entry.Failures + 1
	}
	o.record(generation, "failed", detail)
	entry = o.Entries[generation.Captured]
	entry.Failures, entry.Receipt = failures, stamp
	o.Entries[generation.Captured] = entry
}

func (o *compressionOutcomes) unverified(dir string, generation capturedVersion) {
	o.record(generation, "unverified", "")
	entry := o.Entries[generation.Captured]
	entry.Receipt = receiptStamp(dir, generation.Captured)
	o.Entries[generation.Captured] = entry
}

// receiptStamp identifies rel's SQLite export receipt by its digest, noReceipt
// without one.
func receiptStamp(dir, rel string) string {
	data, err := os.ReadFile(filepath.Join(dir, recordDir, "receipts", hashBytes([]byte(rel))+".json"))
	if err != nil {
		return noReceipt
	}
	return hashBytes(data)
}

// noReceipt stands for a generation without an export receipt.
const noReceipt = "none"

// save keeps only the generations the manifest still names.
func (o *compressionOutcomes) save(dir string, manifest *captureManifest) error {
	for rel := range o.Entries {
		if v, ok := manifest.entry(rel); !ok || v.Encoding != "" {
			delete(o.Entries, rel)
			o.modified = true
		}
	}
	if !o.modified {
		return nil
	}
	data, err := json.MarshalIndent(o, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, compressionOutcomesName), append(data, '\n'), 0o600)
}

// spaceCheckedWriter keeps the capture free-space margin while an encoded
// file grows, checking every few megabytes rather than on every block.
type spaceCheckedWriter struct {
	w       io.Writer
	dir     string
	pending int64
}

func (s *spaceCheckedWriter) Write(b []byte) (int, error) {
	if s.pending += int64(len(b)); s.pending >= 64<<20 || s.pending == int64(len(b)) {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(s.dir, &stat); err != nil {
			return 0, err
		}
		if int64(stat.Bavail)*int64(stat.Bsize)-s.pending < captureFreeMargin {
			return 0, fmt.Errorf("capture drive: needs %d bytes free to compress", s.pending+captureFreeMargin)
		}
		s.pending = 0
	}
	return s.w.Write(b)
}

// compressCapturesLoop is the service daemon that runs compression passes.
// Each pass that compresses something is a maintenance.capture-compress
// operation at background priority (utility IO); one that finds nothing is
// forgotten. It never runs on a request.
func (s *Server) compressCapturesLoop(ctx context.Context) {
	wait := captureCompressDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = captureCompressInterval
		pass := s.Catalog.beginPass(ctx, "maintenance.capture-compress", triggerSchedule)
		passCtx := ctx
		if pass.op != nil {
			passCtx = pass.op.Context()
		}
		var err error
		pass.runIO(func() {
			err = compressCaptures(passCtx, s.Config(), s.Catalog, func(compressedGeneration) { pass.worked(1) })
		})
		if ctx.Err() != nil {
			err = nil
		}
		pass.end(err)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Capture compression: %v\n", err)
		}
	}
}
