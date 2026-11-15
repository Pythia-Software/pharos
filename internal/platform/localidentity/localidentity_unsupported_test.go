//go:build (!darwin && !linux) || localidentity_unsupported

package localidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// go test -tags localidentity_unsupported runs this on darwin and linux too.
func TestUnsupportedAdapter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := Stat(path)
	if !errors.Is(err, ErrUnsupported) || key.Valid() || key != (Key{}) {
		t.Fatalf("unsupported stat: %+v, %v", key, err)
	}
	if text, err := key.DeviceInode(); text != "" || err == nil {
		t.Fatalf("unsupported key encoded as %q", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if key, err := Of(info); !errors.Is(err, ErrUnsupported) || key.Valid() {
		t.Fatalf("unsupported info: %+v, %v", key, err)
	}
}
