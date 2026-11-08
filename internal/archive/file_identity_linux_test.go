package archive

import "syscall"

func legacyRootIdentityFixtures() []legacyRootIdentityFixture {
	return []legacyRootIdentityFixture{
		{&syscall.Stat_t{Dev: 2049, Ino: 123456}, "2049:123456"},
		{&syscall.Stat_t{Dev: 1<<63 + 2, Ino: 1<<63 + 7}, "9223372036854775810:9223372036854775815"},
	}
}
