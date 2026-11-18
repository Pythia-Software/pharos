//go:build linux

package archive

import "os"

func teamsBirthSyncFile(f *os.File) error { return f.Sync() }
