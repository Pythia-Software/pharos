// Package localidentity observes which local filesystem object a path or a
// stat result names, so that a cache can notice the object was replaced even
// when its size and modification time match.
//
// A Key is a local, in-process observation of one object: on Darwin and
// Linux, the full-width device and inode the stat reported. It is not a
// durable source-record identity, a cross-host identity, a canonical
// pathname, a filesystem UUID or a library format, and it must not be used as
// one. Other platforms report ErrUnsupported rather than an identity derived
// from something else, such as a hash of the pathname.
//
// A failed observation returns the zero Key, which is never Valid and never
// encodes; callers must not store or compare it as an identity.
package localidentity

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
)

// ErrUnsupported identifies a platform without a local identity adapter.
var ErrUnsupported = errors.New("local file identity unsupported on this platform")

// ErrUnavailable identifies file information that carries no local identity,
// such as a stat result that did not come from the operating system, or a
// zero Key asked for its encoding.
var ErrUnavailable = errors.New("local file identity unavailable")

// Key is an opaque, comparable identity of one observed local object. The
// zero Key is invalid. Two valid Keys are equal exactly when they name the
// same device and inode.
type Key struct {
	device, inode uint64
	valid         bool
}

// Valid reports whether k came from a successful observation.
func (k Key) Valid() bool { return k.valid }

// DeviceInode is k's legacy decimal "device:inode" text, which stored
// capture states and source preflight hashes depend on byte for byte. An
// invalid Key has no text and reports ErrUnavailable.
func (k Key) DeviceInode() (string, error) {
	if !k.valid {
		return "", ErrUnavailable
	}
	return formatDevice(k.device) + ":" + strconv.FormatUint(k.inode, 10), nil
}

// Stat observes the object path names, following symbolic links as os.Stat
// does. Its error is os.Stat's, or ErrUnsupported or ErrUnavailable.
func Stat(path string) (Key, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Key{}, err
	}
	return Of(info)
}

// Of is the identity of the object info observed, taken from that same stat
// result so that it never mixes metadata from different observations.
func Of(info fs.FileInfo) (Key, error) {
	if info == nil {
		return Key{}, os.ErrInvalid
	}
	return of(info)
}
