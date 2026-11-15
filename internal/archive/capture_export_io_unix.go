//go:build darwin || linux

package archive

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const captureExportSupported = true

// exportOpenRoot opens the directory a person named. Its own path may pass
// through links (a resolved alias); nothing below it does.
func exportOpenRoot(path string) (*exportDir, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &exportDir{fd: fd, path: path}, nil
}

func (d *exportDir) Close() error {
	if d == nil || d.fd < 0 {
		return nil
	}
	err := unix.Close(d.fd)
	d.fd = -1
	return err
}

func (d *exportDir) child(name string) string {
	if name == "." {
		return d.path
	}
	return d.path + string(os.PathSeparator) + name
}

// sub opens the directory name below d; a link, file or special node there
// fails rather than being followed or opened.
func (d *exportDir) sub(name string) (*exportDir, error) {
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%s is not a directory", d.child(name))
		}
		return nil, &os.PathError{Op: "open", Path: d.child(name), Err: err}
	}
	return &exportDir{fd: fd, path: d.child(name)}, nil
}

// stat describes name below d without following a link; ok is false when
// nothing is there.
func (d *exportDir) stat(name string) (exportStat, bool, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return exportStat{}, false, nil
		}
		return exportStat{}, false, &os.PathError{Op: "stat", Path: d.child(name), Err: err}
	}
	return exportStatOf(&st), true, nil
}

// exportStatPath describes the resolved path name, which needs no read
// permission on it.
func exportStatPath(name string) (exportStat, error) {
	var st unix.Stat_t
	if err := unix.Stat(name, &st); err != nil {
		return exportStat{}, &os.PathError{Op: "stat", Path: name, Err: err}
	}
	return exportStatOf(&st), nil
}

// self describes d's own directory.
func (d *exportDir) self() (exportStat, error) {
	var st unix.Stat_t
	if err := unix.Fstat(d.fd, &st); err != nil {
		return exportStat{}, &os.PathError{Op: "stat", Path: d.path, Err: err}
	}
	return exportStatOf(&st), nil
}

// list opens a listing of d's names through a handle of its own, so
// listing never moves another reader's position.
func (d *exportDir) list() (*exportLister, error) {
	fd, err := unix.Openat(d.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: d.path, Err: err}
	}
	return &exportLister{f: os.NewFile(uintptr(fd), d.path)}, nil
}

// openRead opens name below d to read it: without following a link, and
// without blocking on a FIFO, so that anything but a regular file is
// classified from its own handle and refused before a byte is read.
func (d *exportDir) openRead(name string) (*os.File, exportStat, error) {
	return d.openLeaf(name, d.child(name))
}

func (d *exportDir) openLeaf(name, display string) (*os.File, exportStat, error) {
	fd, err := unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, exportStat{}, fmt.Errorf("%s is a symbolic link", display)
		}
		return nil, exportStat{}, &os.PathError{Op: "open", Path: display, Err: err}
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, exportStat{}, &os.PathError{Op: "stat", Path: display, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, exportStat{}, fmt.Errorf("%s is not a regular file", display)
	}
	// A regular file reads the same either way; a blocking handle keeps it
	// off the runtime poller.
	if err = unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, exportStat{}, &os.PathError{Op: "fcntl", Path: display, Err: err}
	}
	return os.NewFile(uintptr(fd), display), exportStatOf(&st), nil
}

// openLock opens an existing lock file below d for flock, never creating
// or truncating it; file names it for the operation registry.
func (d *exportDir) openLock(name, file string) (*os.File, error) {
	f, _, err := d.openLeaf(name, file)
	return f, err
}

func fstatExport(f *os.File) (exportStat, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return exportStat{}, &os.PathError{Op: "stat", Path: f.Name(), Err: err}
	}
	return exportStatOf(&st), nil
}

// mkdir makes a private directory name below d.
func (d *exportDir) mkdir(name string) error {
	if err := unix.Mkdirat(d.fd, name, 0o700); err != nil {
		return &os.PathError{Op: "mkdir", Path: d.child(name), Err: err}
	}
	return nil
}

// create makes a new private file name below d; anything already there,
// a link included, fails.
func (d *exportDir) create(name string) (*os.File, error) {
	fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: d.child(name), Err: err}
	}
	return os.NewFile(uintptr(fd), d.child(name)), nil
}

func (d *exportDir) unlink(name string, dir bool) error {
	flags := 0
	if dir {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(d.fd, name, flags); err != nil {
		return &os.PathError{Op: "remove", Path: d.child(name), Err: err}
	}
	return nil
}

// renameNoReplace renames from, below d, to name below to, failing with
// os.ErrExist rather than replacing anything there, an empty directory
// included. A filesystem without an exclusive rename is unsupported: there
// is no checked-rename fallback.
func (d *exportDir) renameNoReplace(from string, to *exportDir, name string) error {
	err := exportRenameNoReplace(d.fd, from, to.fd, name)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY):
		return fmt.Errorf("%s: %w", to.child(name), os.ErrExist)
	case errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return fmt.Errorf("%w: %s has no exclusive rename (%v)", errCaptureExportUnsupported, to.path, err)
	}
	return &os.LinkError{Op: "rename", Old: d.child(from), New: to.child(name), Err: err}
}

func exportStatOf(st *unix.Stat_t) exportStat {
	return exportStat{Dev: uint64(st.Dev), Ino: st.Ino, Type: exportTypeOf(uint32(st.Mode)), Size: st.Size, MTimeNS: st.Mtim.Nano()}
}

func exportTypeOf(mode uint32) uint32 {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return exportTypeRegular
	case unix.S_IFDIR:
		return exportTypeDir
	case unix.S_IFLNK:
		return exportTypeSymlink
	}
	return exportTypeOther
}
