package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Captured files may be stored encoded at rest (docs/capture-compression.md).
// The manifest keeps naming, sizing and hashing the original bytes; an encoded
// entry adds encoding and stored_size. Everything here reads or writes one
// captured file and knows nothing about why it is encoded, so later phases
// (cold files, idle snapshots) reuse it unchanged.

const captureEncodingZstd = "zstd"

// captureDecoderMemory caps a decoder's window, and with it the memory one
// decode can take whatever a damaged or hostile frame header claims.
const captureDecoderMemory = 64 << 20

// zstdMagic opens every zstd frame; SQLite databases and text never start so.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// errCapturedContentMismatch is a decoded stream that is not the bytes the
// manifest recorded.
var errCapturedContentMismatch = errors.New("decoded capture does not match its manifest")

// capturedContentHook counts decodes in tests (a decode materializes or
// streams the original bytes of an encoded entry).
var capturedContentHook func(file string)

// storedSize is the size of the file on disk: stored_size for an encoded
// entry, the original size otherwise.
func (v capturedVersion) storedSize() int64 {
	if v.Encoding != "" {
		return v.StoredSize
	}
	return v.Size
}

// encodedVersions reports whether any entry of m is stored encoded; only then
// does a writer stamp the version older builds refuse.
func (m *captureManifest) encodedVersions() bool {
	for _, file := range m.Files {
		for _, version := range file.Previous {
			if version.Encoding != "" {
				return true
			}
		}
	}
	for _, snapshot := range m.Snapshots {
		for _, generation := range snapshot.Generations {
			if generation.Encoding != "" {
				return true
			}
		}
	}
	return false
}

// writerVersion is the manifest version a writer stamps: the newest when an
// entry is encoded, so a build that cannot decode refuses the capture rather
// than hand zstd to SQLite, and the previous one otherwise, so libraries
// without encoded entries stay readable by older builds.
func (m *captureManifest) writerVersion() int {
	if m.encodedVersions() {
		return captureManifestVersion
	}
	return capturePlainManifestVersion
}

// version finds the superseded entry (a snapshot generation or an earlier
// file version) captured at rel.
func (m *captureManifest) version(rel string) (*capturedVersion, bool) {
	for _, snapshot := range m.Snapshots {
		for i := range snapshot.Generations {
			if snapshot.Generations[i].Captured == rel {
				return &snapshot.Generations[i], true
			}
		}
	}
	for _, file := range m.Files {
		for i := range file.Previous {
			if file.Previous[i].Captured == rel {
				return &file.Previous[i], true
			}
		}
	}
	return nil, false
}

// encodingOf is how rel is stored ("" for the original bytes), from the
// inventory this writer holds; see storedVersion.
func (c *sourceCapture) encodingOf(rel string) string {
	v, _ := c.storedVersion(rel)
	return v.Encoding
}

// storedVersion is the superseded entry captured at rel when it is stored
// encoded, from this writer's inventory (see storedAs).
func (c *sourceCapture) storedVersion(rel string) (capturedVersion, bool) {
	var found *capturedVersion
	for _, snapshot := range c.snapshots {
		for i := range snapshot.Generations {
			if snapshot.Generations[i].Captured == rel {
				found = &snapshot.Generations[i]
			}
		}
	}
	if found == nil && c.manifest != nil {
		found, _ = c.manifest.version(rel)
	}
	if found == nil {
		return capturedVersion{}, false
	}
	v := storedAs(filepath.Join(c.dir, filepath.FromSlash(rel)), *found)
	return v, v.Encoding != ""
}

// storedAs is v as its file is actually stored. Compression commits the
// manifest naming the encoded file (and with it the version older builds
// refuse) before it renames that file over the name, so the gate never lags
// the bytes. Killed between the two, the entry says zstd while the file still
// holds its original bytes: the original size, and no zstd frame (an encoded
// file is always smaller). That file is read as it is; the next compression
// pass corrects the entry. Any other mismatch is left for the caller to
// reject.
func storedAs(file string, v capturedVersion) capturedVersion {
	if v.Encoding == "" {
		return v
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() != v.Size || info.Size() == v.StoredSize || zstdHeader(file) {
		return v
	}
	v.Encoding, v.StoredSize = "", 0
	return v
}

// zstdHeader reports whether file opens with a zstd frame.
func zstdHeader(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(zstdMagic))
	_, err = io.ReadFull(f, head)
	return err == nil && bytes.Equal(head, zstdMagic)
}

func newCaptureDecoder(r io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(captureDecoderMemory))
}

// capturedContent is an open captured file's original bytes.
type capturedContent struct {
	io.Reader
	file    *os.File
	decoder *zstd.Decoder
}

func (c *capturedContent) Close() error {
	if c.decoder != nil {
		c.decoder.Close()
	}
	return c.file.Close()
}

// openCapturedContent streams a captured file's original bytes: the file
// itself, or its decoded stream for an encoded entry. Callers that need a
// path (SQLite, random access) use materializeCaptured instead.
func openCapturedContent(file, encoding string) (*capturedContent, error) {
	switch encoding {
	case "", captureEncodingZstd:
	default:
		return nil, fmt.Errorf("%s has unsupported capture encoding %q; upgrade Pharos", file, encoding)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	if encoding == "" {
		return &capturedContent{Reader: f, file: f}, nil
	}
	if capturedContentHook != nil {
		capturedContentHook(file)
	}
	decoder, err := newCaptureDecoder(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		f.Close()
		return nil, err
	}
	return &capturedContent{Reader: decoder, file: f, decoder: decoder}, nil
}

// capturedSHA256 hashes a captured file's original bytes, decoding an encoded
// entry as it streams: the digest and length describe what the manifest's
// sha256 and size describe.
func capturedSHA256(ctx context.Context, file, encoding string) (string, int64, error) {
	if encoding == "" {
		return fileSHA256Context(ctx, file)
	}
	content, err := openCapturedContent(file, encoding)
	if err != nil {
		return "", 0, err
	}
	defer content.Close()
	digest := newRecordHash()
	n, err := io.Copy(digest, &recordCountReader{r: content, ctx: ctx})
	return hex.EncodeToString(digest.Sum(nil)), n, err
}

// materializeCaptured writes the original bytes of the encoded file src to
// dst, at the mtime src carries (compression keeps the original's), through a
// temporary beside dst. The result is checked against v's size and, when
// recorded, its SHA-256. dst may be src itself: a reader lease's link to the
// encoded file is replaced by the decoded copy, and the capture keeps the
// encoded file. dst must be inside a reader lease, so that a crash leaves
// nothing a capture would mistake for evidence.
func materializeCaptured(ctx context.Context, src, dst string, v capturedVersion) error {
	content, err := openCapturedContent(src, v.Encoding)
	if err != nil {
		return err
	}
	defer content.Close()
	info, err := content.file.Stat()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err = ensureFree(filepath.Dir(dst), v.Size+4096); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dst), ".decode-*"+captureTempSuffix)
	if err != nil {
		return err
	}
	abandon := func(err error) error {
		out.Close()
		os.Remove(out.Name())
		return err
	}
	digest := sha256.New()
	// One byte more than recorded reveals a longer stream; reading to the end
	// otherwise makes the decoder check the frame's own checksum.
	n, err := io.Copy(io.MultiWriter(out, digest), &recordCountReader{r: io.LimitReader(content, v.Size+1), ctx: ctx})
	if err != nil {
		return abandon(err)
	}
	if n != v.Size || v.SHA256 != "" && hex.EncodeToString(digest.Sum(nil)) != v.SHA256 {
		return abandon(fmt.Errorf("%w: %s", errCapturedContentMismatch, src))
	}
	if err = out.Close(); err != nil {
		os.Remove(out.Name())
		return err
	}
	if err = os.Chtimes(out.Name(), info.ModTime(), info.ModTime()); err != nil {
		os.Remove(out.Name())
		return err
	}
	if err = os.Rename(out.Name(), dst); err != nil {
		os.Remove(out.Name())
		return err
	}
	return nil
}

// openReaderLease makes a temporary reader directory under a capture
// directory, held by its flock: sweeps remove only abandoned ones.
func openReaderLease(dir string) (string, func(), error) {
	lease, lock, err := lockedLease(dir)
	if err != nil {
		return "", nil, err
	}
	return lease, func() { lock.Close(); _ = os.RemoveAll(lease) }, nil
}

// leaseStagingPrefix names a reader lease before its flock is held. A lease
// is published as .reader-* only once locked, so a sweep that does not hold
// the capture lock never mistakes a new lease for an abandoned one.
const leaseStagingPrefix = ".lease-"

// lockedLease makes a reader lease directory and returns it with its lock
// file, flocked. The caller closes the lock and removes the directory.
func lockedLease(dir string) (string, *os.File, error) {
	staged, err := os.MkdirTemp(dir, leaseStagingPrefix+"*"+captureTempSuffix)
	if err != nil {
		return "", nil, err
	}
	lock, err := os.OpenFile(filepath.Join(staged, ".reader.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(staged)
		return "", nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		_ = os.RemoveAll(staged)
		return "", nil, err
	}
	lease := filepath.Join(dir, ".reader-"+strings.TrimPrefix(filepath.Base(staged), leaseStagingPrefix))
	if err = os.Rename(staged, lease); err != nil {
		lock.Close()
		_ = os.RemoveAll(staged)
		return "", nil, err
	}
	return lease, lock, nil
}

// sweepAbandonedLeases removes the reader leases no process holds under
// every host's captured sources: decoded copies a crashed reader left (a
// forced reindex of another Mac's capture, say) are otherwise only swept by a
// capture of that same source. Preservation-pin readers keep their own
// bookkeeping and are left to the capture.
func sweepAbandonedLeases(root string) {
	hosts, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, host := range hosts {
		if !host.IsDir() || !validCaptureName(host.Name()) {
			continue
		}
		sources, err := os.ReadDir(filepath.Join(root, host.Name()))
		if err != nil {
			continue
		}
		for _, source := range sources {
			if !source.IsDir() || !validCaptureName(source.Name()) {
				continue
			}
			dir := filepath.Join(root, host.Name(), source.Name())
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				staged := strings.HasPrefix(name, leaseStagingPrefix)
				if !entry.IsDir() || !strings.HasSuffix(name, captureTempSuffix) || strings.HasPrefix(name, ".reader-pin-") || !staged && !strings.HasPrefix(name, ".reader-") {
					continue
				}
				lease := filepath.Join(dir, name)
				if staged {
					// Not yet locked by its maker if it is new.
					if info, err := entry.Info(); err != nil || time.Since(info.ModTime()) < time.Hour {
						continue
					}
				}
				lock, err := os.OpenFile(filepath.Join(lease, ".reader.lock"), os.O_RDWR|os.O_CREATE, 0o600)
				if err != nil {
					continue
				}
				if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
					_ = os.RemoveAll(lease)
				}
				lock.Close()
			}
		}
	}
}

// leaseDecoded materializes the encoded entry v of the capture in dir into a
// new reader lease at the same relative path, for a reader that holds no
// lease of its own. A plain entry is read in place.
func leaseDecoded(ctx context.Context, dir string, v capturedVersion) (file, leaseDir string, release func(), err error) {
	file = filepath.Join(dir, filepath.FromSlash(v.Captured))
	v = storedAs(file, v)
	if v.Encoding == "" {
		return file, dir, func() {}, nil
	}
	leaseDir, release, err = openReaderLease(dir)
	if err != nil {
		return "", "", nil, err
	}
	decoded := filepath.Join(leaseDir, filepath.FromSlash(v.Captured))
	if err = materializeCaptured(ctx, file, decoded, v); err != nil {
		release()
		return "", "", nil, err
	}
	return decoded, leaseDir, release, nil
}

// leaseCapturedVersion is leaseDecoded for a reader that holds no capture
// lock: under the lock it reads the current entry rel and links its file into
// a new reader lease, so neither a compression nor pruning can change what it
// reads; then, without the lock, it decodes an encoded file in the lease.
func leaseCapturedVersion(ctx context.Context, target captureTarget, rel string) (file, leaseDir string, release func(), err error) {
	lock, err := lockCaptureReader(ctx, target)
	if err != nil {
		return "", "", nil, err
	}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil {
		releaseLockFile(lock)
		return "", "", nil, err
	}
	entry, ok := manifest.version(rel)
	if !ok {
		releaseLockFile(lock)
		return "", "", nil, fmt.Errorf("%s: %w", rel, os.ErrNotExist)
	}
	v := storedAs(filepath.Join(target.Dir, filepath.FromSlash(rel)), *entry)
	if leaseDir, release, err = openReaderLease(target.Dir); err != nil {
		releaseLockFile(lock)
		return "", "", nil, err
	}
	size, hash := v.Size, v.SHA256
	if v.Encoding != "" {
		size, hash = v.StoredSize, ""
	}
	err = freezeRetainedFile(ctx, target.Dir, leaseDir, rel, size, hash)
	releaseLockFile(lock)
	if err != nil {
		release()
		return "", "", nil, err
	}
	file = filepath.Join(leaseDir, filepath.FromSlash(rel))
	if v.Encoding != "" {
		if err = materializeCaptured(ctx, file, file, v); err != nil {
			release()
			return "", "", nil, err
		}
	}
	return file, leaseDir, release, nil
}

// materialize returns a path at which the captured entry rel can be opened
// by path (SQLite, ReadAt): the leased file, or for an encoded entry its
// decoded copy, which replaces the lease's link at the same relative path so
// that every path translation of the view still holds. Only a reader lease's
// view decodes; a view over the capture directory itself never writes there.
func (v *captureView) materialize(ctx context.Context, rel string) (string, error) {
	file := filepath.Join(v.dir, filepath.FromSlash(rel))
	entry, ok := v.manifest.version(rel)
	if !ok || entry.Encoding == "" || v.decoded[rel] {
		return file, nil
	}
	if !v.lease {
		return "", fmt.Errorf("%s is stored %s-encoded and this reader holds no lease to decode it into", rel, entry.Encoding)
	}
	if err := materializeCaptured(ctx, file, file, *entry); err != nil {
		return "", err
	}
	if v.decoded == nil {
		v.decoded = map[string]bool{}
	}
	v.decoded[rel] = true
	return file, nil
}

// open streams the original bytes of the captured entry rel for a reader that
// only reads forward; nothing is written.
func (v *captureView) open(rel string) (io.ReadCloser, error) {
	file := filepath.Join(v.dir, filepath.FromSlash(rel))
	encoding := ""
	if entry, ok := v.manifest.version(rel); ok && !v.decoded[rel] {
		encoding = entry.Encoding
	}
	return openCapturedContent(file, encoding)
}
