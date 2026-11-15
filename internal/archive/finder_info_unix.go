//go:build darwin || linux

package archive

import "golang.org/x/sys/unix"

// finderInfoNative reads a file's com.apple.FinderInfo attribute, at most its
// 32 bytes. Linux attempts the same attribute name; its filesystems usually
// refuse a name outside the user/system/security/trusted namespaces, and
// that error, like any other, leaves the file unmarked.
func finderInfoNative(path string) ([]byte, error) {
	info := make([]byte, 32)
	n, err := unix.Getxattr(path, "com.apple.FinderInfo", info)
	if err != nil {
		return nil, err
	}
	return info[:n], nil
}
