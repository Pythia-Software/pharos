// Stable C boundary: the Go worker loads this API only on supported Macs.
#pragma once
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
enum { PHAROS_MLX_MAX_BATCH = 256 };
typedef struct {
    // Errors are malloc'd strings, freed by the caller. No exceptions cross C.
    // open errors prefixed "unavailable:" allow same-model CPU fallback.
    void* (*open)(const char* weights, const char* task, const char* source_sha, char** error);
    int (*run)(void* model, const int32_t* ids, const int32_t* attention,
               const int32_t* pooling, const int32_t* segments, int batch, int seq,
               float* outputs, char** error);
    void (*close)(void* model);
} pharos_mlx_api;
const pharos_mlx_api* pharos_mlx_api_v1(void);
#ifdef __cplusplus
}
#endif
