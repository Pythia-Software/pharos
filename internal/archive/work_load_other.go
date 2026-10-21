//go:build !darwin

package archive

import (
	"os"
	"strconv"
	"strings"
)

func machineLoad() *float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return nil
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil
	}
	return &load
}
