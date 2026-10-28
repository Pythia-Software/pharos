//go:build !darwin

package archive

// renameExclusiveNative has no exclusive rename to call on this platform.
var renameExclusiveNative = renameChecked
