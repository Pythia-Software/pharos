//go:build !darwin && !linux

package archive

// Machine load is optional timing context. No validated reader exists here;
// in particular, an unrelated /proc/loadavg must not be read on this host.
func machineLoad() *float64 { return nil }
