//go:build !cgo

package main

import "errors"

// openORT needs cgo: the worker calls ONNX Runtime's C API. A build without
// cgo still speaks the protocol and refuses every hello with code "model".
func openORT(library string, cfg sessionConfig) (session, error) {
	return nil, errors.New("this pharos-labeler-ort was built without cgo; rebuild it with CGO_ENABLED=1 to run models")
}
