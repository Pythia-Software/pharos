package localidentity

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestFailedObservationsAreNotIdentities(t *testing.T) {
	if ErrUnsupported == nil || ErrUnavailable == nil || errors.Is(ErrUnsupported, ErrUnavailable) {
		t.Fatal("sentinels must be distinct and non-nil")
	}
	var zero Key
	if zero.Valid() {
		t.Fatal("zero Key is valid")
	}
	if text, err := zero.DeviceInode(); text != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("zero Key encoded as %q, %v", text, err)
	}
	if key, err := Of(nil); !errors.Is(err, os.ErrInvalid) || key.Valid() {
		t.Fatalf("nil info: %+v, %v", key, err)
	}
	key, err := Stat(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) || key.Valid() || key != (Key{}) {
		t.Fatalf("missing path: %+v, %v", key, err)
	}
}
