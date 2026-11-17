// Package credentialstore stores installation secrets outside libraries.
// The host adapter is deliberately narrow, so a native Keychain-backed store
// can replace the protected-file implementation without changing the caller.
package credentialstore

import "errors"

var ErrUnsafe = errors.New("credential storage is unavailable or unsafe")
var ErrExists = errors.New("credential already exists; reconcile or disconnect it first")

// Store may be replaced by a platform credential manager. Secret bytes are
// opaque and must never be logged or returned to a browser.
type Store interface {
	Save(name string, secret []byte) error
	Load(name string) ([]byte, error)
	Replace(name string, expected, secret []byte) error
	Delete(name string, expected []byte) error
}
