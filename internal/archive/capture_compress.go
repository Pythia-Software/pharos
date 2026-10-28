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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Compressing superseded snapshot generations at rest (docs/capture-compression.md,
// phase 1). A generation is only read again by a forced reindex, salvage of
// sessions deleted at the source, record exports, history backfill, pins and
// transfer, all of which decode it first; so once the index no longer needs
// it, it is stored zstd-compressed under its own name.
//
// Order, for one generation:
//
//  1. encode it to <name>.pharos-tmp, hashing the original bytes as read;
//  2. flush the encoded file to the drive (F_FULLFSYNC);
//  3. verify it by decoding and hashing;
//  4. under the host's capture lock, re-check that the manifest still names
//     the same, unpinned, indexed generation, rename over the name, and
//     replace the manifest (encoding, stored_size, sha256) after a full
//     device flush.
//
// A kill before the rename leaves the original and a temporary the next
// capture sweeps. A kill between the rename and the manifest leaves an
// encoded file under a plain entry, which readers recognise (adoptEncoded)
// and the next pass records. Encoding runs without the capture lock, so a
// capture is never kept waiting for minutes; it takes the lock only for the
// rename and manifest. Readers holding the old inode through a lease keep
// reading the original bytes.

var (
	// captureCompressInterval spaces the daemon's passes; the first runs
	// captureCompressDelay after the service starts.
	captureCompressInterval = 15 * time.Minute
	captureCompressDelay    = 2 * time.Minute
	// captureCompressThreads bounds the encoder's goroutines.
	captureCompressThreads = 4
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

// compressCaptures compresses the eligible snapshot generations of this
// Mac's captures. Other Macs' captures are left to their own Macs: a manifest
// with an encoded entry stops any build that cannot decode it, and only the
// capturing Mac's own build is known to be new enough to keep capturing.
// A library that keeps pending evidence for delivery is left alone.
func compressCaptures(ctx context.Context, config Config, done func(compressedGeneration)) error {
	if config.CaptureRoot == "" || config.PendingCapture {
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
		if err := compressSourceGenerations(ctx, captureTarget{Host: host, Source: entry.Name(), Dir: dir}, done); err != nil && !errors.Is(err, errCaptureBusy) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

// compressSourceGenerations records what an interrupted pass left encoded,
// then compresses each eligible generation of one captured source.
func compressSourceGenerations(ctx context.Context, target captureTarget, done func(compressedGeneration)) error {
	if err := recordEncodedGenerations(ctx, target); err != nil {
		return err
	}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return err
	}
	if manifest.Version == 0 || manifest.Host.ID != target.Host.ID || manifest.Source.Name != target.Source {
		return nil
	}
	for _, snapshot := range manifest.Snapshots {
		for _, generation := range snapshot.Generations {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !compressible(target.Dir, manifest, snapshot.Path, generation) {
				continue
			}
			stored, err := compressGeneration(ctx, target, snapshot.Path, generation)
			if err != nil {
				return err
			}
			if stored > 0 && done != nil {
				done(compressedGeneration{Source: target.Source, Path: generation.Captured, Size: generation.Size, Stored: stored})
			}
		}
	}
	return nil
}

// compressible is whether a generation may be stored encoded: one that
// retention does not treat as the only copy of anything still to be read.
// That is a superseded, plain generation that is not pinned (a pin, or a
// backfill's native pause, holds its exact bytes) and that the index marker
// covers when the source has one. A SQLite-record source also needs the
// generation's export verified, since retain keeps it until then.
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

// compressGeneration stores one generation encoded, in the order above. It
// returns the encoded size, or 0 when the generation was left as it was (it
// changed, was pruned or pinned meanwhile, or did not shrink).
func compressGeneration(ctx context.Context, target captureTarget, database string, generation capturedVersion) (int64, error) {
	rel := generation.Captured
	file := filepath.Join(target.Dir, filepath.FromSlash(rel))
	temporary := file + captureTempSuffix
	fault := func(stage string) error {
		if captureFault != nil {
			return captureFault(stage, rel)
		}
		return nil
	}
	// Removed explicitly rather than deferred: a kill leaves the temporary
	// behind exactly as a crash would, for the next capture to sweep.
	abandon := func(err error) (int64, error) {
		os.Remove(temporary)
		return 0, err
	}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return 0, err
	}
	if manifest.SQLiteRecordCapture {
		c, err := sourceCaptureForTarget(ctx, target)
		if err != nil {
			return 0, err
		}
		if _, verified := c.verifiedSnapshotReceipt(rel, rel); !verified {
			return 0, nil
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
		return 0, nil // not what the manifest says; nothing to compress
	}
	out, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
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
		err = fmt.Errorf("%s does not match its manifest; left uncompressed", rel)
	}
	if err == nil {
		err = os.Chtimes(temporary, original.ModTime(), original.ModTime())
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
	stored, err := os.Stat(temporary)
	if err != nil {
		return abandon(err)
	}
	if stored.Size() >= generation.Size {
		return abandon(nil) // incompressible: keep the original
	}
	verified, size, err := capturedSHA256(ctx, temporary, captureEncodingZstd)
	if err != nil {
		return abandon(err)
	}
	if size != generation.Size || verified != hash {
		return abandon(fmt.Errorf("%w: %s", errCapturedContentMismatch, temporary))
	}

	lock, err := lockCaptureTarget(ctx, target.Dir)
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
	entry := currentGeneration(current, database, rel)
	if entry == nil || entry.Encoding != "" || entry.Size != generation.Size || entry.CapturedAt != generation.CapturedAt || !compressible(target.Dir, current, database, *entry) {
		return abandon(nil)
	}
	if now, err := os.Stat(file); err != nil || !os.SameFile(now, original) {
		return abandon(nil)
	}
	if err = fault("compress-rename"); err != nil {
		return abandon(err)
	}
	if err = os.Rename(temporary, file); err != nil {
		return abandon(err)
	}
	syncDir(filepath.Dir(file))
	entry.Encoding, entry.StoredSize, entry.SHA256 = captureEncodingZstd, stored.Size(), hash
	if err = fault("compress-manifest"); err != nil {
		return 0, err
	}
	if err = writeCaptureManifest(target.Dir, current); err != nil {
		return 0, err
	}
	return stored.Size(), nil
}

// recordEncodedGenerations commits, under the capture lock, the manifest
// entries of generations a pass encoded and renamed but was killed before
// recording: each must decode to its recorded size (and digest, if known).
func recordEncodedGenerations(ctx context.Context, target captureTarget) error {
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		return err
	}
	pending := false
	for _, snapshot := range manifest.Snapshots {
		for _, generation := range snapshot.Generations {
			file := filepath.Join(target.Dir, filepath.FromSlash(generation.Captured))
			pending = pending || adoptEncoded(file, generation).Encoding != generation.Encoding
		}
	}
	if !pending {
		return nil
	}
	lock, err := lockCaptureTarget(ctx, target.Dir)
	if err != nil {
		return err
	}
	defer releaseLockFile(lock)
	if manifest, err = loadCaptureManifest(target.Dir); err != nil {
		return err
	}
	changed := false
	for _, snapshot := range manifest.Snapshots {
		for i, generation := range snapshot.Generations {
			file := filepath.Join(target.Dir, filepath.FromSlash(generation.Captured))
			adopted := adoptEncoded(file, generation)
			if adopted.Encoding == generation.Encoding {
				continue
			}
			hash, size, err := capturedSHA256(ctx, file, adopted.Encoding)
			if err != nil || size != adopted.Size || adopted.SHA256 != "" && hash != adopted.SHA256 {
				continue // not a compression's output: left for validation to report
			}
			adopted.SHA256 = hash
			snapshot.Generations[i] = adopted
			changed = true
		}
	}
	if !changed {
		return nil
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
			err = compressCaptures(passCtx, s.Config(), func(compressedGeneration) { pass.worked(1) })
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
