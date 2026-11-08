package archive

import (
	"errors"
	"io"
	"os"
)

// The capture export reads a capture and writes its outbox only through the
// handles below (capture_export_io_unix.go): every name is resolved one
// component at a time from a pinned directory handle without following a
// symbolic link, and every leaf is classified from the handle it is read
// through. Path-based helpers (os.Open after an Lstat, os.Root, copyVerified)
// cannot promise that, so these stay local to the export.

// errCaptureExportUnsupported is a platform or filesystem without the
// confined reads, exclusive rename or locking the export relies on.
var errCaptureExportUnsupported = errors.New("capture export is not supported here")

// exportStat is what the export compares before and after it reads a file:
// its identity, type, size and modification time.
type exportStat struct {
	Dev, Ino uint64
	Type     uint32
	Size     int64
	MTimeNS  int64
}

func (s exportStat) isDir() bool     { return s.Type == exportTypeDir }
func (s exportStat) isRegular() bool { return s.Type == exportTypeRegular }

// sameFile reports whether s and t describe the same unchanged file.
func (s exportStat) sameFile(t exportStat) bool {
	return s.Dev == t.Dev && s.Ino == t.Ino && s.Type == t.Type && s.Size == t.Size && s.MTimeNS == t.MTimeNS
}

const (
	exportTypeOther uint32 = iota
	exportTypeRegular
	exportTypeDir
	exportTypeSymlink
)

// exportDir is an open directory handle; path names it in messages and for
// the path-based flushes of a tree the export itself created.
type exportDir struct {
	fd   int
	path string
}

// openReadFile opens rel, a slash path below d, confined as openRead is.
func (d *exportDir) openReadFile(rel string) (*os.File, exportStat, error) {
	parent, name, err := d.walkParent(rel)
	if err != nil {
		return nil, exportStat{}, err
	}
	if parent != d {
		defer parent.Close()
	}
	return parent.openRead(name)
}

// statPath stats rel, a slash path below d, without following links.
func (d *exportDir) statPath(rel string) (exportStat, bool, error) {
	parent, name, err := d.walkParent(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return exportStat{}, false, nil
		}
		return exportStat{}, false, err
	}
	if parent != d {
		defer parent.Close()
	}
	return parent.stat(name)
}

// walkParent opens the directory holding rel's last component, which it
// returns with that component's name; it is d itself for a single name.
func (d *exportDir) walkParent(rel string) (*exportDir, string, error) {
	parts := splitSlash(rel)
	if len(parts) == 0 {
		return nil, "", errors.New("empty path")
	}
	current := d
	for _, part := range parts[:len(parts)-1] {
		next, err := current.sub(part)
		if current != d {
			current.Close()
		}
		if err != nil {
			return nil, "", err
		}
		current = next
	}
	return current, parts[len(parts)-1], nil
}

// exportListBatch is how many names one directory read returns at most.
const exportListBatch = 256

// exportLister reads a directory's names a batch at a time, so that no
// listing holds every name of a directory at once.
type exportLister struct{ f *os.File }

// next returns up to n more names, and io.EOF once there are none.
func (l *exportLister) next(n int) ([]string, error) { return l.f.Readdirnames(n) }

func (l *exportLister) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}

// removeTree removes name, below d, and everything under it, without
// following links. It removes only what the export staged itself, one
// batch of names at a time from a fresh listing, so removing never relies
// on a listing that the removal itself changes.
func (d *exportDir) removeTree(name string) error {
	st, ok, err := d.stat(name)
	if err != nil || !ok {
		return err
	}
	if !st.isDir() {
		return d.unlink(name, false)
	}
	sub, err := d.sub(name)
	if err != nil {
		return err
	}
	defer sub.Close()
	for {
		lister, err := sub.list()
		if err != nil {
			return err
		}
		names, err := lister.next(exportListBatch)
		lister.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(names) == 0 {
			break
		}
		for _, child := range names {
			if err := sub.removeTree(child); err != nil {
				return err
			}
		}
	}
	return d.unlink(name, true)
}

func splitSlash(rel string) []string {
	if rel == "" {
		return nil
	}
	parts := []string{}
	start := 0
	for i := 0; i <= len(rel); i++ {
		if i == len(rel) || rel[i] == '/' {
			parts = append(parts, rel[start:i])
			start = i + 1
		}
	}
	return parts
}
