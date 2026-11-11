package archive

import (
	"os"
	"path"
	"path/filepath"
	"time"
)

// A projection is already filtered by its provider's strict allowlist. The raw
// input must never be copied to the library. Immutable, content-addressed
// projection names let a disappearing cache leave its observations intact.
type captureProjection struct {
	rel      string
	data     []byte
	observed time.Time
}

func (c *sourceCapture) captureProjections(items []captureProjection, present map[string]bool) error {
	for _, item := range items {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		rel := path.Join(captureFilesDir, item.rel)
		present[rel] = true
		c.result.FilesSeen++
		digest := hashBytes(item.data)
		previous := c.files[rel]
		if previous != nil && previous.SHA256 == digest && c.intactFile(previous) {
			previous.MissingSince = ""
			c.result.FilesUnchanged++
			continue
		}
		size := int64(len(item.data))
		c.result.FilesPlanned++
		c.result.BytesPlanned += size
		if err := c.ensureSpace(size); err != nil {
			return err
		}
		if err := reserveCaptureAcquisition(c.dir, size); err != nil {
			return err
		}
		dest := filepath.Join(c.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return toDestination(err)
		}
		if err := writeFileDurably(dest, item.data); err != nil {
			return toDestination(err)
		}
		if err := os.Chtimes(dest, item.observed, item.observed); err != nil {
			return toDestination(err)
		}
		// The original name is the projection identity, not a raw cache filename
		// that Claude can reuse for a completely different observation later.
		c.files[rel] = &capturedFile{Path: filepath.Join(c.source.Path, filepath.FromSlash(item.rel)), Captured: rel, Size: size, MTime: item.observed.UTC().Format(time.RFC3339Nano), MTimeNS: item.observed.UnixNano(), SHA256: digest, CapturedAt: now(), RecoveryID: c.session.recoveryID}
		c.result.FilesCopied++
		c.result.BytesCopied += size
		c.report()
		if err := c.maybeCheckpoint(); err != nil {
			return err
		}
	}
	return nil
}
