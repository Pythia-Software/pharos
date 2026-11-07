package archive

// directoryVolumeID is an opaque device observation, comparable only within
// the running OS/process. It is not a persistent volume ID and must not be stored.
type directoryVolumeID struct {
	device uint64
}
