//go:build darwin || linux

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type teamsBirthStage struct {
	parent, stage, catalog int
	stageName, leafName    string
	dirStat, catalogStat   unix.Stat_t
}

func (s *teamsBirthStage) close() {
	for _, fd := range []int{s.catalog, s.stage, s.parent} {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
}
func teamsBirthWriteAt(dir int, name string, data []byte) error {
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	_, err = f.Write(data)
	if err == nil {
		err = teamsBirthSyncFile(f)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}

// teamsStageBirth creates a private sibling without following or replacing the
// destination. The caller retains these descriptors until publication.
func teamsStageBirth(path, name string, image []byte, d LibraryDescriptor, identity func(string) string) (*teamsBirthStage, error) {
	parentPath, leaf := filepath.Dir(path), filepath.Base(path)
	p, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	s := &teamsBirthStage{parent: p, stage: -1, catalog: -1, leafName: leaf}
	ok := false
	defer func() {
		if !ok {
			s.close()
		}
	}()
	var st unix.Stat_t
	if e := unix.Fstatat(p, leaf, &st, unix.AT_SYMLINK_NOFOLLOW); e == nil {
		return nil, os.ErrExist
	} else if !errors.Is(e, unix.ENOENT) {
		return nil, e
	}
	nonce, err := teamsCheckpointRandom()
	if err != nil {
		return nil, err
	}
	s.stageName = ".pharos-birth-" + nonce
	if err = unix.Mkdirat(p, s.stageName, 0700); err != nil {
		return nil, err
	}
	s.stage, err = unix.Openat(p, s.stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = unix.Fstat(s.stage, &s.dirStat); err != nil {
		return nil, err
	}
	if err = unix.Mkdirat(s.stage, "catalog", 0700); err != nil {
		return nil, err
	}
	s.catalog, err = unix.Openat(s.stage, "catalog", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = teamsBirthWriteAt(s.catalog, "catalog.sqlite3", image); err != nil {
		return nil, err
	}
	if err = unix.Fstatat(s.catalog, "catalog.sqlite3", &s.catalogStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || s.catalogStat.Mode&unix.S_IFMT != unix.S_IFREG || s.catalogStat.Nlink != 1 {
		return nil, errTeamsProtectionContinuity
	}
	pin := "# No stable volume identity was found.\nvolume_id = \"\""
	if volume := identity(parentPath); strings.HasPrefix(volume, "uuid:") {
		pin = fmt.Sprintf("volume_id = %q", volume)
	}
	config := fmt.Sprintf(libraryConfigTemplate, pin, randomToken(), libraryPort, defaultLibrarySettings)
	if err = teamsBirthWriteAt(s.stage, libraryConfigName, []byte(config)); err != nil {
		return nil, err
	}
	d.Name = name
	d.Inode = s.dirStat.Ino
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = teamsBirthWriteAt(s.stage, libraryDescriptorName, append(raw, '\n')); err != nil {
		return nil, err
	}
	if err = unix.Fsync(s.catalog); err != nil {
		return nil, err
	}
	if err = unix.Fsync(s.stage); err != nil {
		return nil, err
	}
	if err = unix.Fsync(s.parent); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}
func (s *teamsBirthStage) publish() error {
	if err := teamsRenameBirthExclusive(s.parent, s.stageName, s.leafName); err != nil {
		return err
	}
	return unix.Fsync(s.parent)
}
func teamsVerifyBirthImage(path string, r teamsBirthReservation) error {
	dir, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var ds unix.Stat_t
	if unix.Fstat(dir, &ds) != nil || uint64(ds.Dev) != r.DirectoryDevice || ds.Ino != r.DirectoryInode {
		return errTeamsProtectionContinuity
	}
	cat, err := unix.Openat(dir, "catalog", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(cat)
	for _, side := range []string{"catalog.sqlite3-wal", "catalog.sqlite3-shm", "catalog.sqlite3-journal"} {
		var st unix.Stat_t
		if e := unix.Fstatat(cat, side, &st, unix.AT_SYMLINK_NOFOLLOW); e == nil {
			return errTeamsProtectionContinuity
		} else if !errors.Is(e, unix.ENOENT) {
			return e
		}
	}
	fd, err := unix.Openat(cat, "catalog.sqlite3", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "catalog")
	defer f.Close()
	var cs unix.Stat_t
	if unix.Fstat(fd, &cs) != nil || cs.Mode&unix.S_IFMT != unix.S_IFREG || cs.Nlink != 1 || uint64(cs.Dev) != r.CatalogDevice || cs.Ino != r.CatalogInode || cs.Size <= 0 || cs.Size > teamsFreshImageLimit {
		return errTeamsProtectionContinuity
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, teamsFreshImageLimit+1)); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != r.ImageSHA256 {
		return errTeamsProtectionContinuity
	}
	return nil
}

// teamsVerifyBirthLocation uses only stat-style operations; it must not open
// and close the main file while another SQLite connection holds POSIX locks.
func teamsVerifyBirthLocation(path string, r teamsBirthReservation) error {
	var d, c unix.Stat_t
	if unix.Lstat(path, &d) != nil || d.Mode&unix.S_IFMT != unix.S_IFDIR || d.Uid != uint32(os.Geteuid()) || d.Ino != r.DirectoryInode {
		return errTeamsProtectionContinuity
	}
	cat := filepath.Join(path, "catalog")
	var cd unix.Stat_t
	if unix.Lstat(cat, &cd) != nil || cd.Mode&unix.S_IFMT != unix.S_IFDIR || cd.Uid != uint32(os.Geteuid()) {
		return errTeamsProtectionContinuity
	}
	main := filepath.Join(cat, "catalog.sqlite3")
	if unix.Lstat(main, &c) != nil || c.Mode&unix.S_IFMT != unix.S_IFREG || c.Nlink != 1 || c.Uid != uint32(os.Geteuid()) || c.Ino != r.CatalogInode {
		return errTeamsProtectionContinuity
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		var side unix.Stat_t
		err := unix.Lstat(main+suffix, &side)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil || side.Mode&unix.S_IFMT != unix.S_IFREG || side.Nlink != 1 || side.Uid != uint32(os.Geteuid()) {
			return errTeamsProtectionContinuity
		}
	}
	return nil
}

// teamsEnsureManagedParent durably establishes only the app-owned parent;
// the destination leaf remains create-only and is reserved separately.
func teamsEnsureManagedParent(path string) error {
	ancestor := path
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return errTeamsProtectionContinuity
		}
		ancestor = next
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		fd, err := unix.Open(current, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		err = unix.Fsync(fd)
		_ = unix.Close(fd)
		if err != nil {
			return err
		}
		if current == ancestor {
			break
		}
	}
	return nil
}
