package archive

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A sealed pack is immutable and named by its physical byte digest. Sharing
// its inode with a capture retains independent path lifetimes without another
// payload allocation. Unsupported links use the ordinary verified copy path.
func (c *sourceCapture) linkClaudeSourcePack(item captureItem, source *os.File, info fs.FileInfo, previous *capturedFile) (*capturedFile, bool, error) {
	if path.Dir(item.rel) != "objects" || !strings.HasSuffix(item.rel, ".pack") {
		return nil, false, nil
	}
	expected := strings.TrimSuffix(path.Base(item.rel), ".pack")
	if !validRecordHash(expected) {
		return nil, true, errors.New("invalid immutable Claude pack name")
	}
	if previous != nil && previous.SHA256 != expected {
		return nil, true, errors.New("Claude immutable capture path conflicts with its recorded identity")
	}
	digest := c.session.newHash()
	defer digest.close()
	if _, err := c.copyN(io.Discard, digest, source, info.Size()); err != nil {
		return nil, true, err
	}
	if digest.sum() != expected {
		return nil, true, errors.New("Claude pack failed its content-address verification")
	}
	destination := filepath.Join(c.dir, filepath.FromSlash(path.Join(captureFilesDir, item.rel)))
	temporary := destination + captureTempSuffix
	if err := os.Remove(temporary); err != nil && !os.IsNotExist(err) {
		return nil, true, toDestination(err)
	}
	if err := pinLinkFile(item.path, temporary); err != nil {
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			return nil, true, err
		}
		return nil, false, nil
	}
	linked, err := os.Stat(temporary)
	after, afterErr := source.Stat()
	if err != nil || afterErr != nil || !os.SameFile(info, linked) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		os.Remove(temporary)
		return nil, true, errCaptureSourceChanged
	}
	if err := os.Rename(temporary, destination); err != nil {
		os.Remove(temporary)
		return nil, true, toDestination(err)
	}
	entry := &capturedFile{Path: item.path, Captured: path.Join(captureFilesDir, item.rel), Size: info.Size(), MTime: formatTime(info.ModTime()), MTimeNS: info.ModTime().UnixNano(), SHA256: expected, CapturedAt: now(), RecoveryID: c.session.recoveryID}
	if previous != nil {
		entry.Previous = previous.Previous
		entry.ObservedNS = previous.ObservedNS
	}
	return entry, true, nil
}
