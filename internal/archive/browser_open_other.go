//go:build !darwin

package archive

const browserOpenSupported = false

// browserOpenNative has no reviewed default-browser launcher on this platform.
func browserOpenNative(string) error { return errBrowserOpenUnsupported }

func browserProfileOpenNative(string, string, string) error { return errBrowserOpenUnsupported }

func browserProfilesNative() []browserProfile { return nil }
