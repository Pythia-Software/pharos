//go:build !darwin

package archive

import "time"

// processStartTime is not known off macOS.
func processStartTime(int) (time.Time, bool) { return time.Time{}, false }
