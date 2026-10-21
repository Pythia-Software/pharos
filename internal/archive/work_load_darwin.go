package archive

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func machineLoad() *float64 {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(raw) < 24 {
		return nil
	}
	// Darwin aligns the 64-bit fscale after three 32-bit load values.
	scale := binary.NativeEndian.Uint64(raw[16:24])
	if scale == 0 {
		return nil
	}
	load := float64(binary.NativeEndian.Uint32(raw[:4])) / float64(scale)
	return &load
}
