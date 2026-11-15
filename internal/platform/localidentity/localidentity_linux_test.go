//go:build linux && (amd64 || arm64) && !localidentity_unsupported

package localidentity

import (
	"fmt"
	"syscall"
	"testing"
)

// Every bit of Linux's 64-bit dev_t and inode takes part.
func TestFullWidthLinuxValues(t *testing.T) {
	stat := syscall.Stat_t{Dev: 1<<63 + 3, Ino: 1<<63 + 7}
	key := fromStat(&stat)
	text, err := key.DeviceInode()
	if want := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino); err != nil || text != want || text != "9223372036854775811:9223372036854775815" {
		t.Fatalf("text %q, %v; want %q", text, err, want)
	}
	if key == fromStat(&syscall.Stat_t{Dev: 3, Ino: stat.Ino}) || key == fromStat(&syscall.Stat_t{Dev: stat.Dev, Ino: 7}) {
		t.Fatal("high bits were dropped")
	}
}
