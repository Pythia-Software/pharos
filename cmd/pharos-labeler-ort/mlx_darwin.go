//go:build darwin && arm64 && cgo

package main

/*
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>
#include "../../macos/labeler-mlx/engine.h"
static const pharos_mlx_api* load_mlx(const char* path, char** err) {
 if (__builtin_available(macOS 26.2, *)) {
  // Supported OS: dlopen below performs the remaining capability checks.
 } else {
  *err = strdup("MLX 0.32.4 requires macOS 26.2 or later"); return NULL;
 }
 void* lib = dlopen(path, RTLD_NOW | RTLD_LOCAL);
 if (!lib) { *err = strdup(dlerror()); return NULL; }
 const pharos_mlx_api* (*get)(void) = dlsym(lib, "pharos_mlx_api_v1");
 if (!get) { *err = strdup("MLX engine has no pharos_mlx_api_v1"); dlclose(lib); return NULL; }
 return get(); // Remain loaded for the process lifetime, like ORT.
}
static void* mlx_open(const pharos_mlx_api* api, const char* p, const char* t, const char* s, char** e) { return api->open(p,t,s,e); }
static int mlx_run(const pharos_mlx_api* api, void* m, const int32_t* i, const int32_t* a, const int32_t* p, const int32_t* s, int b, int q, float* o, char** e) { return api->run(m,i,a,p,s,b,q,o,e); }
static void mlx_close(const pharos_mlx_api* api, void* m) { api->close(m); }
*/
import "C"

import (
	"fmt"
	"github.com/gbdubs/pharos/internal/labeler"
	"runtime"
	"strings"
	"unsafe"
)

type mlxSession struct {
	api   *C.pharos_mlx_api
	model unsafe.Pointer
	info  modelInfo
}

func openMLX(library, path string, artifact labeler.MLXArtifact) (session, error) {
	lib, weights, task, source := C.CString(library), C.CString(path), C.CString(artifact.Task), C.CString(artifact.SourceSHA256)
	defer C.free(unsafe.Pointer(lib))
	defer C.free(unsafe.Pointer(weights))
	defer C.free(unsafe.Pointer(task))
	defer C.free(unsafe.Pointer(source))
	var msg *C.char
	api := C.load_mlx(lib, &msg)
	if api == nil {
		return nil, mlxError(msg)
	}
	model := C.mlx_open(api, weights, task, source, &msg)
	if model == nil {
		unavailable := strings.HasPrefix(C.GoString(msg), "unavailable:")
		err := mlxError(msg)
		if unavailable {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", errMLXModel, err)
	}
	s := &mlxSession{api: api, model: model}
	s.info = modelInfo{MaxBatch: C.PHAROS_MLX_MAX_BATCH, MaxTokens: 16384, MaxSeq: 512, Runtime: "mlx 0.32.4 (pharos-mlx-v1)", Device: "gpu", Providers: []string{"Metal"}}
	contract := labeler.InputContractIDsAttentionPool
	if artifact.Task == "jayhawk" {
		contract = labeler.InputContractIDsAttentionPoolSegments
		s.info.MaxSeq = 768
	}
	for _, input := range labeler.InputContracts[contract].Inputs {
		s.info.Inputs = append(s.info.Inputs, labeler.TensorInfo{Name: input.Name, DType: "int32", Shape: []any{"batch", "seq"}})
	}
	if artifact.Task == "kestrel" {
		s.info.Outputs = []labeler.TensorInfo{{Name: "logits", DType: "float32", Shape: []any{"batch", 5, 54}}, {Name: "probs", DType: "float32", Shape: []any{"batch", 54}}, {Name: "target_tokens", DType: "float32", Shape: []any{"batch"}}}
	} else {
		s.info.Outputs = []labeler.TensorInfo{{Name: "verdict", DType: "float32", Shape: []any{"batch", 3}}, {Name: "families", DType: "float32", Shape: []any{"batch", 24}}, {Name: "sentences", DType: "float32", Shape: []any{"batch", 16, 2}}}
	}
	return s, nil
}
func mlxError(msg *C.char) error {
	if msg == nil {
		return fmt.Errorf("MLX engine failed without a diagnostic")
	}
	defer C.free(unsafe.Pointer(msg))
	return fmt.Errorf("MLX: %s", C.GoString(msg))
}
func (s *mlxSession) Info() modelInfo { return s.info }
func (s *mlxSession) Run(inputs []modelInput, batch, seq int, outputs []string) ([]labeler.Tensor, error) {
	if s.model == nil || batch < 1 || batch > s.info.MaxBatch || seq < 1 || seq > s.info.MaxSeq || batch*seq > 16384 {
		return nil, fmt.Errorf("invalid MLX batch [%d,%d] (limit 16384 padded tokens)", batch, seq)
	}
	var data [4]*C.int32_t
	var pin runtime.Pinner
	defer pin.Unpin()
	for _, in := range inputs {
		index := -1
		for i, spec := range s.info.Inputs {
			if spec.Name == in.Name {
				index = i
				break
			}
		}
		if index < 0 || data[index] != nil || len(in.Int32) != batch*seq {
			return nil, fmt.Errorf("invalid MLX input %s", in.Name)
		}
		data[index] = (*C.int32_t)(unsafe.Pointer(&in.Int32[0]))
		pin.Pin(data[index])
	}
	for i := range s.info.Inputs {
		if data[i] == nil {
			return nil, fmt.Errorf("missing MLX input %s", s.info.Inputs[i].Name)
		}
	}
	all := make(map[string]labeler.Tensor)
	count := 0
	for _, out := range s.info.Outputs {
		dims := []int{batch}
		n := batch
		for _, d := range out.Shape[1:] {
			dims = append(dims, d.(int))
			n *= d.(int)
		}
		all[out.Name] = labeler.Tensor{Name: out.Name, DType: labeler.DTypeFloat32, Dims: dims}
		count += n
	}
	result := make([]float32, count)
	var msg *C.char
	if C.mlx_run(s.api, s.model, data[0], data[1], data[2], data[3], C.int(batch), C.int(seq), (*C.float)(unsafe.Pointer(&result[0])), &msg) != 0 {
		return nil, mlxError(msg)
	}
	offset := 0
	for _, out := range s.info.Outputs {
		t := all[out.Name]
		n := 1
		for _, d := range t.Dims {
			n *= d
		}
		t.Float32 = result[offset : offset+n]
		offset += n
		all[out.Name] = t
	}
	tensors := make([]labeler.Tensor, len(outputs))
	for i, name := range outputs {
		t, ok := all[name]
		if !ok {
			return nil, fmt.Errorf("unknown MLX output %q", name)
		}
		tensors[i] = t
	}
	return tensors, nil
}
func (s *mlxSession) Close() error {
	if s.model != nil {
		C.mlx_close(s.api, s.model)
		s.model = nil
	}
	return nil
}
