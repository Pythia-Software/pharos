package archive

import (
	"errors"
	"fmt"
)

var errNonblockingOpenUnsupported = fmt.Errorf("opening input without blocking is not supported on this platform: %w", errors.ErrUnsupported)

// Cloud setup files and downloaded archives open through these, which tests
// replace to observe failures the filesystem cannot produce here.
var (
	openInputNonblocking   = openInputNonblockingNative
	openInputNonblockingIn = openInputNonblockingInNative
)
