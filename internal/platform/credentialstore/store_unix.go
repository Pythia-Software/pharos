//go:build darwin || linux

package credentialstore

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxSecretBytes = 64 << 10

type protectedFiles struct{ root string }

func New(root string) Store { return &protectedFiles{root: root} }
func validName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// openRoot checks the actual opened directory descriptor, not a path followed
// after checking. O_NOFOLLOW refuses a symlink at the credential directory.
func (s *protectedFiles) openRoot(create bool) (int, error) {
	if !filepath.IsAbs(s.root) || filepath.Clean(s.root) != s.root {
		return -1, ErrUnsafe
	}
	if create {
		if err := os.MkdirAll(s.root, 0700); err != nil {
			return -1, ErrUnsafe
		}
	}
	parent, err := unix.Open(filepath.Dir(s.root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if !create && errors.Is(err, unix.ENOENT) {
			return -1, os.ErrNotExist
		}
		return -1, ErrUnsafe
	}
	defer unix.Close(parent)
	var parentStat unix.Stat_t
	if unix.Fstat(parent, &parentStat) != nil || parentStat.Mode&unix.S_IFMT != unix.S_IFDIR || parentStat.Uid != uint32(os.Geteuid()) || parentStat.Mode&0022 != 0 {
		return -1, ErrUnsafe
	}
	fd, err := unix.Openat(parent, filepath.Base(s.root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if !create && errors.Is(err, unix.ENOENT) {
			return -1, os.ErrNotExist
		}
		return -1, ErrUnsafe
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || stat.Mode&0700 != 0700 {
		unix.Close(fd)
		return -1, ErrUnsafe
	}
	return fd, nil
}
func checkFile(fd int) error {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0077 != 0 || st.Mode&0600 != 0600 {
		return ErrUnsafe
	}
	return nil
}

func lockRoot(root int) (*os.File, error) {
	fd, err := unix.Openat(root, ".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "credential lock")
	if err = checkFile(fd); err != nil {
		f.Close()
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}
func sweepTemps(root int) error {
	dup, err := unix.Dup(root)
	if err != nil {
		return ErrUnsafe
	}
	dir := os.NewFile(uintptr(dup), "credential directory")
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return ErrUnsafe
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) == 37 && strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") {
			if _, err := hex.DecodeString(name[1:33]); err == nil {
				if err = unix.Unlinkat(root, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
					return ErrUnsafe
				}
			}
		}
	}
	return nil
}
func readAt(root int, name string) ([]byte, error) {
	fd, err := unix.Openat(root, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "credential")
	defer f.Close()
	if err = checkFile(fd); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSecretBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxSecretBytes {
		return nil, ErrUnsafe
	}
	return b, nil
}
func writeTemp(root int, secret []byte) (string, error) {
	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", err
	}
	tmp := "." + hex.EncodeToString(nonce[:]) + ".tmp"
	fd, err := unix.Openat(root, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "credential temporary")
	if err = excludeBackup(fd); err != nil {
		f.Close()
		_ = unix.Unlinkat(root, tmp, 0)
		return "", err
	}
	if _, err = f.Write(secret); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = unix.Unlinkat(root, tmp, 0)
		return "", errors.New("could not store credential")
	}
	return tmp, nil
}
func (s *protectedFiles) Save(name string, secret []byte) error {
	if !validName(name) || len(secret) == 0 || len(secret) > maxSecretBytes {
		return ErrUnsafe
	}
	root, err := s.openRoot(true)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	if err = excludeBackup(root); err != nil {
		return err
	}
	lock, err := lockRoot(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = sweepTemps(root); err != nil {
		return err
	}
	tmp, err := writeTemp(root, secret)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(root, tmp, 0)
	if err = renameNoReplace(root, tmp, name); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrExists
		}
		return ErrUnsafe
	}
	if err = unix.Fsync(root); err != nil {
		return errors.New("credential publication could not be confirmed")
	}
	return nil
}
func (s *protectedFiles) Load(name string) ([]byte, error) {
	if !validName(name) {
		return nil, ErrUnsafe
	}
	root, err := s.openRoot(false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	return readAt(root, name)
}
func (s *protectedFiles) Replace(name string, expected, secret []byte) error {
	if !validName(name) || len(expected) == 0 || len(secret) == 0 || len(secret) > maxSecretBytes {
		return ErrUnsafe
	}
	root, err := s.openRoot(false)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	if err = excludeBackup(root); err != nil {
		return err
	}
	lock, err := lockRoot(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = sweepTemps(root); err != nil {
		return err
	}
	old, err := readAt(root, name)
	if err != nil {
		return err
	}
	defer clear(old)
	if subtle.ConstantTimeCompare(old, expected) != 1 {
		return ErrUnsafe
	}
	tmp, err := writeTemp(root, secret)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(root, tmp, 0)
	if err = unix.Renameat(root, tmp, root, name); err != nil {
		return ErrUnsafe
	}
	if err = unix.Fsync(root); err != nil {
		return errors.New("credential replacement could not be confirmed")
	}
	return nil
}
func (s *protectedFiles) Delete(name string, expected []byte) error {
	if !validName(name) || len(expected) == 0 {
		return ErrUnsafe
	}
	root, err := s.openRoot(false)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	lock, err := lockRoot(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = sweepTemps(root); err != nil {
		return err
	}
	old, err := readAt(root, name)
	if err != nil {
		return err
	}
	defer clear(old)
	if subtle.ConstantTimeCompare(old, expected) != 1 {
		return ErrUnsafe
	}
	if err = unix.Unlinkat(root, name, 0); err != nil {
		return ErrUnsafe
	}
	if err = unix.Fsync(root); err != nil {
		return errors.New("credential deletion could not be confirmed")
	}
	return nil
}
