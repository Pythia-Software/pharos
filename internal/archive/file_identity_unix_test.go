//go:build darwin || linux

package archive

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestNativeFileIdentity(t *testing.T) {
	if _, err := observeFile(identityFileInfo{name: "a", sys: (*syscall.Stat_t)(nil)}); !errors.Is(err, errFileIdentityUnavailable) {
		t.Fatal("a nil status:", err)
	}
	// Distinct devices may reuse an inode number.
	first, second := &syscall.Stat_t{Ino: 42, Nlink: 2}, &syscall.Stat_t{Ino: 42, Nlink: 2}
	second.Dev = first.Dev + 1
	keyA, okA := linkKey(identityFileInfo{name: "a", sys: first})
	keyB, okB := linkKey(identityFileInfo{name: "a", sys: second})
	if !okA || !okB || keyA == keyB || keyA.inode != keyB.inode {
		t.Fatalf("inode 42 on two devices: %+v %+v", keyA, keyB)
	}
	if observed, err := observeFile(identityFileInfo{name: "a", sys: first}); err != nil || observed.links != 2 {
		t.Fatalf("links: %+v %v", observed, err)
	}
	// Persisted plans hold the unsigned device and inode as they always
	// have, with no width lost.
	for _, fixture := range legacyRootIdentityFixtures() {
		identity, err := rootIdentity(identityFileInfo{name: "captures", sys: fixture.stat})
		if err != nil || identity != fixture.want || identity != fmt.Sprintf("%d:%d", uint64(fixture.stat.Dev), uint64(fixture.stat.Ino)) {
			t.Errorf("root identity of %+v = %q, %v; want %q", fixture.stat, identity, err, fixture.want)
		}
	}
}

type legacyRootIdentityFixture struct {
	stat *syscall.Stat_t
	want string
}
