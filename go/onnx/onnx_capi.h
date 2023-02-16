// Use of this source code is governed by a Apache-style
// license that can be found in the LICENSE file.

#ifndef onnx_capi_h_
#define onnx_capi_h_

#include <stddef.h>
#include <stdint.h>

#include <onnxruntime/core/session/onnxruntime_c_api.h>

#define MAX_IN 10
#define MAX_OUT 10
#define MAX_SHAEP 10

#define MODE_CUDA 1
#define MODE_ROCM 2
#define MODE_ARMNN 3
#define MODE_TENSOR_RT 4

typedef struct {
	OrtEnv* env;
	OrtSessionOptions* session_options;
	OrtSession* session;
	char* input_names[MAX_IN];
	char* output_names[MAX_OUT];
	int64_t input_shape[MAX_SHAEP];
	size_t input_names_len;
	size_t output_names_len;
	size_t input_shape_len;
} OnnxEnv;

OnnxEnv* OnnxNewOrtSession(const char* model_path, int mode);

void OnnxDeleteOrtSession(OnnxEnv* env);

OrtValue* OnnxRunInference(OnnxEnv* env, float* model_input, size_t model_input_len);
OrtValue* OnnxRunInference2(OnnxEnv* env, OrtValue** input_tensors, size_t input_tensors_len);

OrtValue* OnnxCreateTensorInt64(OnnxEnv* env, int64_t* data, size_t data_len, int64_t* dims, size_t dims_len);

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
