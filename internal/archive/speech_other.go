//go:build !darwin

package archive

import "context"

const speechSupported = false

// speechNative has no reviewed speech synthesizer on this platform.
func speechNative(context.Context, string, string) error { return errSpeechUnsupported }
