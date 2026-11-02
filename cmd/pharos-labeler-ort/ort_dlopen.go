//go:build cgo && unix

package main

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

// pharos_ort_symbol opens the library and finds OrtGetApiBase. On failure it
// returns NULL and a malloc'd message in *err.
static void* pharos_ort_symbol(const char* path, char** err) {
	void* lib = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (lib == NULL) {
		const char* msg = dlerror();
		*err = strdup(msg ? msg : "dlopen failed");
		return NULL;
	}
	dlerror();
	void* sym = dlsym(lib, "OrtGetApiBase");
	if (sym == NULL) {
		const char* msg = dlerror();
		*err = strdup(msg ? msg : "OrtGetApiBase not found");
		dlclose(lib);
		return NULL;
	}
	return sym;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// loadRuntime opens the ONNX Runtime shared library at path (a bare name is
// searched for by the dynamic loader) and returns its OrtGetApiBase. The
// library stays loaded for the life of the process. A Windows variant would
// use LoadLibraryW and GetProcAddress in a file beside this one.
func loadRuntime(path string) (unsafe.Pointer, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var cerr *C.char
	sym := C.pharos_ort_symbol(cpath, &cerr)
	if sym == nil {
		defer C.free(unsafe.Pointer(cerr))
		return nil, fmt.Errorf("loading the ONNX Runtime library: %s", C.GoString(cerr))
	}
	return sym, nil
}
