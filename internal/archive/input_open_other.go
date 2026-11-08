//go:build !darwin && !linux

package archive

import "os"

// Without a validated nonblocking open, refuse before opening input rather
// than risk an ordinary open blocking on a FIFO or device. A stat first
// would not help: the file can be replaced between the check and the open.
func openInputNonblockingNative(string) (*os.File, error) {
	return nil, errNonblockingOpenUnsupported
}

func openInputNonblockingInNative(*os.Root, string) (*os.File, error) {
	return nil, errNonblockingOpenUnsupported
}
