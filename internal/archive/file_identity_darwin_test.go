package archive

import "syscall"

// Darwin's device is an int32; a negative one was always widened with its
// sign, which localidentity.DeviceInode would print as -2 instead.
func legacyRootIdentityFixtures() []legacyRootIdentityFixture {
	return []legacyRootIdentityFixture{
		{&syscall.Stat_t{Dev: 16777231, Ino: 123456}, "16777231:123456"},
		{&syscall.Stat_t{Dev: -2, Ino: 1<<63 + 7}, "18446744073709551614:9223372036854775815"},
	}
}
