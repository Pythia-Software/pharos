//go:build (!darwin && !linux && !windows) || fsinfo_unsupported

package fsinfo

import (
	"errors"
	"testing"
)

// go test -tags fsinfo_unsupported runs this on darwin, linux and windows too.
func TestUnsupportedAdapter(t *testing.T) {
	dir := t.TempDir()
	if space, err := Capacity(dir); err != ErrUnsupported || space != (Space{}) {
		t.Fatalf("capacity: %+v, %v", space, err)
	}
	if readOnly, err := ReadOnlyMount(dir); !errors.Is(err, ErrUnsupported) || readOnly {
		t.Fatalf("mount flags: %v, %v", readOnly, err)
	}
}
