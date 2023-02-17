// Use of this source code is governed by a Apache-style
// license that can be found in the LICENSE file.

#ifndef onnx_capi_h_
#define onnx_capi_h_

#include <stddef.h>
#include <stdint.h>

#include <onnxruntime/core/session/onnxruntime_c_api.h>

#define MAX_IN 16
#define MAX_OUT 64

#define MODE_CPU 0
#define MODE_CUDA 1
#define MODE_TENSOR_RT 2
#define MODE_COREML 3

// Declare execution provider symbols, depending on the onnxruntime build
// these may or may not be available in the dynamic library.
ORT_API_STATUS(OrtSessionOptionsAppendExecutionProvider_CUDA, _In_ OrtSessionOptions* options, int device_id);
ORT_API_STATUS(OrtSessionOptionsAppendExecutionProvider_Tensorrt, _In_ OrtSessionOptions* options, int device_id);
ORT_API_STATUS(OrtSessionOptionsAppendExecutionProvider_CoreML, _In_ OrtSessionOptions* options, uint32_t coreml_flags);

typedef struct {
  OrtEnv* env;
  OrtSessionOptions* session_options;
  OrtSession* session;
  OrtMemoryInfo *memory_info;
  char* input_names[MAX_IN];
  char* output_names[MAX_OUT];
  size_t input_names_len;
  size_t output_names_len;
  size_t input_shape_len;
} OnnxEnv;

OnnxEnv* OnnxNewOrtSession(const char* model_path, int mode);

void OnnxDeleteOrtSession(OnnxEnv* env);

void SetupExecutionProvider(OrtSessionOptions* session_options, int mode);

OrtValue** OnnxRunInference(OnnxEnv* env, OrtValue** input_tensors, OrtValue** output_tensors);

OrtValue* OnnxCreateTensorInt64(OnnxEnv* env, int64_t* data, size_t data_len, int64_t* dims, size_t dims_len);
OrtValue* OnnxCreateTensorFloat32(OnnxEnv* env, float* data, size_t data_len, int64_t* dims, size_t dims_len);

void OnnxReleaseTensor(OrtValue* tensor);

size_t OnnxTensorNumDims(OrtValue*  tensor);

int64_t OnnxTensorDim(OrtValue*  tensor, int index);

void OnnxTensorCopyToBuffer(OrtValue*  tensor, void * value, size_t size);

// Array helper
static void FreeCharArray(char **a, size_t size);

float** MakeFloatArray(int size);
void SetFloatArray(float **a, float *s, int n);
void FreeFloatArray(float **a);

//utils
void EuclideanDistance512(float **d, float *res, int ai, int bi, int end);

#endif // onnx_capi_h_
