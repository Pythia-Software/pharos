//go:build cgo && !unix

package main

import (
	"fmt"
	"runtime"
	"unsafe"
)

// loadRuntime is not written for this system yet. On Windows it would load
// onnxruntime.dll with LoadLibraryW and find OrtGetApiBase with
// GetProcAddress; ortSession's CreateSession call would then need the model
// path as UTF-16, since ORTCHAR_T is wchar_t there.
func loadRuntime(path string) (unsafe.Pointer, error) {
	return nil, fmt.Errorf("loading the ONNX Runtime library is not implemented on %s", runtime.GOOS)
}
