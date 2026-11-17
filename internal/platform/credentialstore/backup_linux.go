//go:build linux

package credentialstore

// The application currently supports macOS. This test/development lane does
// not claim integration with a Linux backup service.
func excludeBackup(int) error { return nil }
