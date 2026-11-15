//go:build !darwin && !linux

package archive

// finderInfoNative has no extended-attribute read to call on this platform.
func finderInfoNative(string) ([]byte, error) { return nil, errFinderInfoUnsupported }
