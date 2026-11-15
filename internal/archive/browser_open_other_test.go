//go:build !darwin

package archive

import (
	"errors"
	"testing"
)

func TestBrowserOpenIsUnsupported(t *testing.T) {
	if err := browserOpenNative("https://claude.ai/"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
	if browserOpenAvailable() {
		t.Fatal("an unsupported platform reports a browser launcher")
	}
}
