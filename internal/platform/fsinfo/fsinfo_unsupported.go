//go:build (!darwin && !linux && !windows) || fsinfo_unsupported

package fsinfo

// Capacity validates path and reports ErrUnsupported, never a guessed size.
func Capacity(path string) (Space, error) {
	if err := validatePath("capacity", path); err != nil {
		return Space{}, err
	}
	return Space{}, ErrUnsupported
}

// ReadOnlyMount validates path and reports ErrUnsupported, never false as
// though the mount were known to be writable.
func ReadOnlyMount(path string) (bool, error) {
	if err := validatePath("mount flags", path); err != nil {
		return false, err
	}
	return false, ErrUnsupported
}
