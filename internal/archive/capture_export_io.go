package archive

import (
	"errors"
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

// subPath opens the directory rel below d (d itself for "").
func (d *exportDir) subPath(rel string) (*exportDir, error) {
	if rel == "" {
		return d.sub(".")
	}
	parent, name, err := d.walkParent(rel)
	if err != nil {
		return nil, err
	}
	if parent != d {
		defer parent.Close()
	}
	return parent.sub(name)
}

// removeTree removes name, below d, and everything under it, without
// following links. It removes only what the export staged itself.
func (d *exportDir) removeTree(name string) error {
	st, ok, err := d.stat(name)
	if err != nil || !ok {
		return err
	}
	if st.isDir() {
		sub, err := d.sub(name)
		if err != nil {
			return err
		}
		names, err := sub.entries()
		for _, child := range names {
			if err == nil {
				err = sub.removeTree(child)
			}
		}
		sub.Close()
		if err != nil {
			return err
		}
		return d.unlink(name, true)
	}
	return d.unlink(name, false)
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
