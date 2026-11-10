// Package sourcepath handles recorded source names without consulting the
// indexing host's filesystem or path conventions.
package sourcepath

import (
	"path"
	"strings"
)

// CutLegacyPOSIX cuts a recorded name at an exact or slash-component prefix.
// It preserves legacy matching: exact matches come first, and otherwise only
// one trailing slash is removed from a nonempty root. It does not clean either
// input; backslashes and colons are ordinary characters.
//
// Matching establishes neither filesystem identity nor I/O containment.
func CutLegacyPOSIX(name, root string) (suffix string, ok bool) {
	if name == root {
		return "", true
	}
	if root != "" {
		prefix := strings.TrimSuffix(root, "/") + "/"
		if strings.HasPrefix(name, prefix) {
			return name[len(prefix):], true
		}
	}
	return "", false
}

// JoinLegacyPOSIX reconstructs a recorded source name with legacy path.Join
// cleaning. It does not establish filesystem identity or I/O containment.
func JoinLegacyPOSIX(root, suffix string) string {
	return path.Join(root, suffix)
}
