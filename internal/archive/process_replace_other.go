//go:build !darwin && !linux

package archive

// replaceProcessNative cannot keep the PID and stdio here. Starting another
// process on the same stdio would compete with this one for input, so refuse.
func replaceProcessNative(string, []string, []string) error {
	return errProcessReplaceUnsupported
}
