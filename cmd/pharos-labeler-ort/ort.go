//go:build cgo

package main

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include <stdlib.h>
#include <string.h>
#include "onnxruntime_c_api.h"

// Go cannot call the OrtApi function pointers itself, so each call goes
// through a small helper here. Every helper returns the OrtStatus* it got
// (NULL for success); Go turns it into an error with pharos_status_message.

typedef const OrtApiBase* (*pharos_get_api_base)(void);

static uint32_t pharos_api_version(void) { return ORT_API_VERSION; }

// pharos_api asks the runtime for the API version of the vendored header;
// NULL means the runtime is older than the header.
static const OrtApi* pharos_api(void* get_api_base, const char** version) {
	const OrtApiBase* base = ((pharos_get_api_base)get_api_base)();
	if (base == NULL) {
		*version = "unknown";
		return NULL;
	}
	*version = base->GetVersionString();
	return base->GetApi(ORT_API_VERSION);
}

// pharos_status_message returns status's message, malloc'd, and releases status.
static char* pharos_status_message(const OrtApi* api, OrtStatus* status) {
	char* msg = strdup(api->GetErrorMessage(status));
	api->ReleaseStatus(status);
	return msg;
}

static OrtStatus* pharos_create_env(const OrtApi* api, OrtEnv** env) {
	return api->CreateEnv(ORT_LOGGING_LEVEL_WARNING, "pharos-labeler-ort", env);
}

// pharos_create_session loads the model with default session options except
// the intra-op thread count (0 keeps the runtime's default), the same options
// as the Python reference. The CPU execution provider is the default.
static OrtStatus* pharos_create_session(const OrtApi* api, const OrtEnv* env, const char* path, int threads, OrtSession** out) {
	OrtSessionOptions* options = NULL;
	OrtStatus* status = api->CreateSessionOptions(&options);
	if (status == NULL && threads > 0) status = api->SetIntraOpNumThreads(options, threads);
	// ORTCHAR_T is char except on Windows.
	if (status == NULL) status = api->CreateSession(env, (const ORTCHAR_T*)path, options, out);
	if (options != NULL) api->ReleaseSessionOptions(options);
	return status;
}

static OrtStatus* pharos_create_cpu_memory(const OrtApi* api, OrtMemoryInfo** out) {
	return api->CreateCpuMemoryInfo(OrtDeviceAllocator, OrtMemTypeDefault, out);
}

static OrtStatus* pharos_io_count(const OrtApi* api, const OrtSession* s, int output, size_t* n) {
	return output ? api->SessionGetOutputCount(s, n) : api->SessionGetInputCount(s, n);
}

// pharos_io_name returns the name of input or output i, malloc'd.
static OrtStatus* pharos_io_name(const OrtApi* api, const OrtSession* s, int output, size_t i, char** out) {
	OrtAllocator* alloc = NULL;
	char* name = NULL;
	OrtStatus* status = api->GetAllocatorWithDefaultOptions(&alloc);
	if (status == NULL) status = output ? api->SessionGetOutputName(s, i, alloc, &name) : api->SessionGetInputName(s, i, alloc, &name);
	if (status == NULL) {
		*out = strdup(name);
		OrtStatus* freed = api->AllocatorFree(alloc, name);
		if (freed != NULL) api->ReleaseStatus(freed);
	}
	return status;
}

#define PHAROS_MAX_RANK 8

typedef struct {
	int tensor;
	int element_type;
	size_t rank;
	int64_t dims[PHAROS_MAX_RANK];
	char* dim_names[PHAROS_MAX_RANK]; // malloc'd, or NULL
} pharos_io_info;

// pharos_io_type describes input or output i. Ranks over PHAROS_MAX_RANK
// leave dims unset.
static OrtStatus* pharos_io_type(const OrtApi* api, const OrtSession* s, int output, size_t i, pharos_io_info* info) {
	OrtTypeInfo* type = NULL;
	enum ONNXType kind = ONNX_TYPE_UNKNOWN;
	const OrtTensorTypeAndShapeInfo* tensor = NULL;
	OrtStatus* status = output ? api->SessionGetOutputTypeInfo(s, i, &type) : api->SessionGetInputTypeInfo(s, i, &type);
	if (status == NULL) status = api->GetOnnxTypeFromTypeInfo(type, &kind);
	info->tensor = kind == ONNX_TYPE_TENSOR;
	if (status == NULL && info->tensor) status = api->CastTypeInfoToTensorInfo(type, &tensor);
	if (status == NULL && tensor != NULL) {
		ONNXTensorElementDataType element = ONNX_TENSOR_ELEMENT_DATA_TYPE_UNDEFINED;
		status = api->GetTensorElementType(tensor, &element);
		info->element_type = element;
		if (status == NULL) status = api->GetDimensionsCount(tensor, &info->rank);
		if (status == NULL && info->rank <= PHAROS_MAX_RANK) {
			const char* names[PHAROS_MAX_RANK] = {0};
			status = api->GetDimensions(tensor, info->dims, info->rank);
			if (status == NULL) status = api->GetSymbolicDimensions(tensor, names, info->rank);
			for (size_t k = 0; status == NULL && k < info->rank; k++) {
				info->dim_names[k] = names[k] != NULL && names[k][0] != 0 ? strdup(names[k]) : NULL;
			}
		}
	}
	if (type != NULL) api->ReleaseTypeInfo(type);
	return status;
}

// pharos_run runs one batch of inputs [batch, seq], each int64 or int32 by
// input_types, over the caller's buffers, and stores the requested outputs in
// values. The input values over the buffers are released before it returns.
static OrtStatus* pharos_run(const OrtApi* api, OrtSession* s, const OrtMemoryInfo* memory,
		char** input_names, void** input_data, int* input_types, size_t inputs, int64_t batch, int64_t seq,
		char** output_names, size_t outputs, OrtValue** values) {
	int64_t shape[2] = {batch, seq};
	OrtValue** in = calloc(inputs, sizeof(OrtValue*));
	if (in == NULL) return api->CreateStatus(ORT_FAIL, "out of memory for the run's inputs");
	OrtStatus* status = NULL;
	for (size_t k = 0; status == NULL && k < inputs; k++) {
		size_t element = input_types[k] == ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64 ? sizeof(int64_t) : sizeof(int32_t);
		status = api->CreateTensorWithDataAsOrtValue(memory, input_data[k], (size_t)(batch * seq) * element, shape, 2,
			(ONNXTensorElementDataType)input_types[k], &in[k]);
	}
	if (status == NULL) {
		status = api->Run(s, NULL, (const char* const*)input_names, (const OrtValue* const*)in, inputs,
			(const char* const*)output_names, outputs, values);
	}
	for (size_t k = 0; k < inputs; k++) {
		if (in[k] != NULL) api->ReleaseValue(in[k]);
	}
	free(in);
	return status;
}

// pharos_output describes an output tensor and points data at its elements.
static OrtStatus* pharos_output(const OrtApi* api, OrtValue* value, int* element_type, size_t* rank, int64_t* dims, size_t* count, void** data) {
	OrtTensorTypeAndShapeInfo* info = NULL;
	ONNXTensorElementDataType element = ONNX_TENSOR_ELEMENT_DATA_TYPE_UNDEFINED;
	OrtStatus* status = api->GetTensorTypeAndShape(value, &info);
	if (status == NULL) status = api->GetTensorElementType(info, &element);
	*element_type = element;
	if (status == NULL) status = api->GetDimensionsCount(info, rank);
	if (status == NULL && *rank <= PHAROS_MAX_RANK) status = api->GetDimensions(info, dims, *rank);
	if (status == NULL) status = api->GetTensorShapeElementCount(info, count);
	if (status == NULL) status = api->GetTensorMutableData(value, data);
	if (info != NULL) api->ReleaseTensorTypeAndShapeInfo(info);
	return status;
}

static void pharos_release_value(const OrtApi* api, OrtValue* value) { api->ReleaseValue(value); }

static void pharos_release(const OrtApi* api, OrtSession* s, OrtMemoryInfo* memory, OrtEnv* env) {
	if (s != NULL) api->ReleaseSession(s);
	if (memory != NULL) api->ReleaseMemoryInfo(memory);
	if (env != NULL) api->ReleaseEnv(env);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/gbdubs/pharos/internal/labeler"
)

// ortSession is a model loaded in ONNX Runtime through its C API.
type ortSession struct {
	api    *C.OrtApi
	env    *C.OrtEnv
	sess   *C.OrtSession
	memory *C.OrtMemoryInfo
	// inputNames and outputNames hold the model's input and output names
	// as C strings for Run.
	inputNames  map[string]*C.char
	outputNames map[string]*C.char
	info        modelInfo
}

// openORT loads the runtime library and the model.
func openORT(library string, cfg sessionConfig) (session, error) {
	getAPIBase, err := loadRuntime(library)
	if err != nil {
		return nil, err
	}
	var cversion *C.char
	api := C.pharos_api(getAPIBase, &cversion)
	version := C.GoString(cversion)
	if api == nil {
		return nil, fmt.Errorf("ONNX Runtime %s at %s does not provide C API version %d; this worker needs ONNX Runtime 1.%d or newer",
			version, library, C.pharos_api_version(), C.pharos_api_version())
	}
	s := &ortSession{api: api, inputNames: map[string]*C.char{}, outputNames: map[string]*C.char{}}
	s.info.Runtime = "onnxruntime " + version
	s.info.Device = "cpu"
	s.info.Providers = []string{"CPUExecutionProvider"}
	if err := s.open(cfg); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *ortSession) open(cfg sessionConfig) error {
	if err := s.check(C.pharos_create_env(s.api, &s.env)); err != nil {
		return fmt.Errorf("creating the ONNX Runtime environment: %w", err)
	}
	path := C.CString(cfg.Model)
	defer C.free(unsafe.Pointer(path))
	if err := s.check(C.pharos_create_session(s.api, s.env, path, C.int(cfg.Threads), &s.sess)); err != nil {
		return fmt.Errorf("loading %s: %w", cfg.Model, err)
	}
	if err := s.check(C.pharos_create_cpu_memory(s.api, &s.memory)); err != nil {
		return err
	}
	var err error
	if s.info.Inputs, err = s.describe(false); err != nil {
		return err
	}
	if s.info.Outputs, err = s.describe(true); err != nil {
		return err
	}
	for _, in := range s.info.Inputs {
		s.inputNames[in.Name] = C.CString(in.Name)
	}
	for _, out := range s.info.Outputs {
		s.outputNames[out.Name] = C.CString(out.Name)
	}
	return nil
}

// describe lists the model's inputs or outputs.
func (s *ortSession) describe(output bool) ([]labeler.TensorInfo, error) {
	which := C.int(0)
	if output {
		which = 1
	}
	var n C.size_t
	if err := s.check(C.pharos_io_count(s.api, s.sess, which, &n)); err != nil {
		return nil, err
	}
	infos := make([]labeler.TensorInfo, 0, int(n))
	for i := C.size_t(0); i < n; i++ {
		var cname *C.char
		if err := s.check(C.pharos_io_name(s.api, s.sess, which, i, &cname)); err != nil {
			return nil, err
		}
		name := C.GoString(cname)
		C.free(unsafe.Pointer(cname))
		var t C.pharos_io_info
		err := s.check(C.pharos_io_type(s.api, s.sess, which, i, &t))
		info := labeler.TensorInfo{Name: name, DType: "non-tensor"}
		if t.tensor != 0 {
			info.DType = dtypeName(t.element_type)
			if t.rank <= C.PHAROS_MAX_RANK {
				info.Shape = make([]any, int(t.rank))
				for k := range info.Shape {
					// A dimension is its size, its symbolic name, or null.
					switch dimName := t.dim_names[k]; {
					case t.dims[k] >= 0:
						info.Shape[k] = int(t.dims[k])
					case dimName != nil:
						info.Shape[k] = C.GoString(dimName)
					}
					C.free(unsafe.Pointer(t.dim_names[k]))
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("describing %s: %w", name, err)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// dtypeName names an ONNX element type as numpy does.
func dtypeName(t C.int) string {
	switch t {
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT:
		return "float32"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_DOUBLE:
		return "float64"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT16:
		return "float16"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_BFLOAT16:
		return "bfloat16"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT8:
		return "int8"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT16:
		return "int16"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT32:
		return "int32"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64:
		return "int64"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_UINT8:
		return "uint8"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_UINT16:
		return "uint16"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_UINT32:
		return "uint32"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_UINT64:
		return "uint64"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_BOOL:
		return "bool"
	case C.ONNX_TENSOR_ELEMENT_DATA_TYPE_STRING:
		return "string"
	}
	return fmt.Sprintf("onnx-type-%d", int(t))
}

func (s *ortSession) Info() modelInfo { return s.info }

func (s *ortSession) Run(inputs []modelInput, batch, seq int, outputs []string) ([]labeler.Tensor, error) {
	if batch < 1 || seq < 1 || len(inputs) == 0 || len(outputs) == 0 {
		return nil, fmt.Errorf("run of %d inputs [%d, %d] and %d outputs", len(inputs), batch, seq, len(outputs))
	}
	// The data pointers are Go memory handed to C inside a Go array, so
	// they are pinned for the call; the C helper releases the input values
	// over them before it returns.
	var pinner runtime.Pinner
	defer pinner.Unpin()
	inputNames := make([]*C.char, len(inputs))
	data := make([]unsafe.Pointer, len(inputs))
	types := make([]C.int, len(inputs))
	for i, in := range inputs {
		if inputNames[i] = s.inputNames[in.Name]; inputNames[i] == nil {
			return nil, fmt.Errorf("unknown input %q", in.Name)
		}
		switch {
		case len(in.Int64) == batch*seq && in.Int32 == nil:
			data[i], types[i] = unsafe.Pointer(&in.Int64[0]), C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64
		case len(in.Int32) == batch*seq && in.Int64 == nil:
			data[i], types[i] = unsafe.Pointer(&in.Int32[0]), C.ONNX_TENSOR_ELEMENT_DATA_TYPE_INT32
		default:
			return nil, fmt.Errorf("input %s has %d int64 and %d int32 values for [%d, %d]", in.Name, len(in.Int64), len(in.Int32), batch, seq)
		}
		pinner.Pin(data[i])
	}
	outputNames := make([]*C.char, len(outputs))
	for i, name := range outputs {
		if outputNames[i] = s.outputNames[name]; outputNames[i] == nil {
			return nil, fmt.Errorf("unknown output %q", name)
		}
	}
	values := make([]*C.OrtValue, len(outputs))
	status := C.pharos_run(s.api, s.sess, s.memory,
		&inputNames[0], &data[0], &types[0], C.size_t(len(inputs)), C.int64_t(batch), C.int64_t(seq),
		&outputNames[0], C.size_t(len(outputNames)), &values[0])
	defer func() {
		for _, v := range values {
			if v != nil {
				C.pharos_release_value(s.api, v)
			}
		}
	}()
	if err := s.check(status); err != nil {
		return nil, err
	}
	tensors := make([]labeler.Tensor, len(outputs))
	for i, v := range values {
		var (
			element C.int
			rank    C.size_t
			dims    [C.PHAROS_MAX_RANK]C.int64_t
			count   C.size_t
			data    unsafe.Pointer
		)
		if err := s.check(C.pharos_output(s.api, v, &element, &rank, &dims[0], &count, &data)); err != nil {
			return nil, fmt.Errorf("reading output %s: %w", outputs[i], err)
		}
		if element != C.ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT || rank > C.PHAROS_MAX_RANK {
			return nil, fmt.Errorf("output %s is %s of rank %d, not float32 of rank at most %d", outputs[i], dtypeName(element), rank, C.PHAROS_MAX_RANK)
		}
		t := labeler.Tensor{Name: outputs[i], DType: labeler.DTypeFloat32, Dims: make([]int, int(rank)), Float32: make([]float32, int(count))}
		for k := range t.Dims {
			t.Dims[k] = int(dims[k])
		}
		if count > 0 {
			copy(t.Float32, unsafe.Slice((*float32)(data), int(count)))
		}
		tensors[i] = t
	}
	return tensors, nil
}

func (s *ortSession) Close() error {
	C.pharos_release(s.api, s.sess, s.memory, s.env)
	s.sess, s.memory, s.env = nil, nil, nil
	for _, names := range []map[string]*C.char{s.inputNames, s.outputNames} {
		for name, c := range names {
			C.free(unsafe.Pointer(c))
			delete(names, name)
		}
	}
	return nil
}

// check turns a status into an error and releases it.
func (s *ortSession) check(status *C.OrtStatus) error {
	if status == nil {
		return nil
	}
	msg := C.pharos_status_message(s.api, status)
	defer C.free(unsafe.Pointer(msg))
	return errors.New(C.GoString(msg))
}
