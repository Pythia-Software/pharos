//go:build darwin && !localidentity_unsupported

package localidentity

import (
	"fmt"
	"syscall"
	"testing"
)

// Every bit of Darwin's 32-bit signed dev_t and 64-bit inode takes part, and
// the legacy text keeps fmt's signed device.
func TestFullWidthDarwinValues(t *testing.T) {
	stat := syscall.Stat_t{Dev: -2, Ino: 1<<63 + 7}
	key := fromStat(&stat)
	text, err := key.DeviceInode()
	if want := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino); err != nil || text != want || text != "-2:9223372036854775815" {
		t.Fatalf("text %q, %v; want %q", text, err, want)
	}
	if key == fromStat(&syscall.Stat_t{Dev: -2, Ino: 7}) || key == fromStat(&syscall.Stat_t{Dev: 0x7ffffffe, Ino: stat.Ino}) {
		t.Fatal("high bits were dropped")
	}
}
