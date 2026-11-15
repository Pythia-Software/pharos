//go:build !darwin

package archive

var platformGPUClients func() (gpuSnapshot, error)
