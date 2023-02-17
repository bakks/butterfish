// Use of this source code is governed by a Apache-style
// license that can be found in the LICENSE file.

#include "onnx_capi.h"

#include <stdio.h>
#include <math.h>
#include <assert.h>

#define ORT_ABORT_ON_ERROR(expr)                             \
  do {                                                       \
    OrtStatus* onnx_status = (expr);                         \
    if (onnx_status != NULL) {                               \
      const char* msg = g_ort->GetErrorMessage(onnx_status); \
      printf("%s\n", msg);                                   \
      g_ort->ReleaseStatus(onnx_status);                     \
      abort();                                               \
    }                                                        \
  } while (0);


const OrtApi* g_ort = NULL;


OnnxEnv* OnnxNewOrtSession(const char* model_path, int mode) {
  int ret = 0;

  if(g_ort == NULL){
    g_ort = OrtGetApiBase()->GetApi(ORT_API_VERSION);
    if (!g_ort) {
      printf("runtime init error!\n");
      return NULL;
    }
  }

  OnnxEnv* onnx_env = (OnnxEnv*)malloc(sizeof(OnnxEnv));

  ORT_ABORT_ON_ERROR(g_ort->CreateEnv(ORT_LOGGING_LEVEL_WARNING, "infer", &onnx_env->env));

  ORT_ABORT_ON_ERROR(g_ort->CreateSessionOptions(&onnx_env->session_options));

  SetupExecutionProvider(onnx_env->session_options, mode);

  ORT_ABORT_ON_ERROR(g_ort->CreateSession(onnx_env->env, model_path, onnx_env->session_options, &onnx_env->session));

  OrtMemoryInfo* memory_info;
  ORT_ABORT_ON_ERROR(g_ort->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, &memory_info));
  onnx_env->memory_info = memory_info;

  return onnx_env;
}

void SetupExecutionProvider(OrtSessionOptions* session_options, int mode) {
  if(mode == MODE_CUDA){
#ifdef CUDA
    int device_id = 0;
    ORT_ABORT_ON_ERROR(OrtSessionOptionsAppendExecutionProvider_CUDA(session_options, device_id));
#else
    printf("CUDA is not supported in this build.\n");
#endif
  }

   if (mode == MODE_TENSOR_RT) {
#ifdef TENSOR_RT
    int device_id = 0;
    ORT_ABORT_ON_ERROR(OrtSessionOptionsAppendExecutionProvider_Tensorrt(session_options, device_id));
#else
    printf("TensorRT is not supported in this build.\n");
#endif
  }

  if (mode == MODE_COREML) {
#ifdef COREML
    uint32_t coreml_flags = 0;
    ORT_ABORT_ON_ERROR(OrtSessionOptionsAppendExecutionProvider_CoreML(session_options, coreml_flags));
#else
    printf("CoreML is not supported in this build.\n");
#endif
  }
}

void OnnxDeleteOrtSession(OnnxEnv* env){
  if(g_ort){
    g_ort->ReleaseSessionOptions(env->session_options);
    g_ort->ReleaseSession(env->session);
    g_ort->ReleaseEnv(env->env);
    FreeCharArray(env->input_names, env->input_names_len);
    FreeCharArray(env->output_names, env->output_names_len);
    free(env);
  }
}

OrtValue* OnnxCreateTensorInt64(OnnxEnv* env, int64_t* data, size_t data_len, int64_t* dims, size_t dims_len) {

  OrtValue* input_tensor = NULL;
  ORT_ABORT_ON_ERROR(g_ort->CreateTensorWithDataAsOrtValue(
        env->memory_info, data, data_len, dims, dims_len,
        ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64, &input_tensor));

  int is_tensor;
  ORT_ABORT_ON_ERROR(g_ort->IsTensor(input_tensor, &is_tensor));
  assert(is_tensor);

  return input_tensor;
}

OrtValue* OnnxCreateTensorFloat32(OnnxEnv* env, float* data, size_t data_len, int64_t* dims, size_t dims_len) {

  OrtValue* input_tensor = NULL;
  ORT_ABORT_ON_ERROR(g_ort->CreateTensorWithDataAsOrtValue(
        env->memory_info, data, data_len, dims, dims_len,
        ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT, &input_tensor));

  int is_tensor;
  ORT_ABORT_ON_ERROR(g_ort->IsTensor(input_tensor, &is_tensor));
  assert(is_tensor);

  return input_tensor;
}

OrtValue** OnnxRunInference(
    OnnxEnv* env,
    OrtValue** input_tensors,
    OrtValue** output_tensors) {

  ORT_ABORT_ON_ERROR(g_ort->Run(
        env->session,
        NULL,
        (const char *const *)env->input_names,
        (const OrtValue *const *)input_tensors,
        env->input_names_len,
        (const char *const *)env->output_names,
        env->output_names_len,
        output_tensors));

  // iterate through output_tensors to check them
  for (size_t i = 0; i < env->output_names_len; i++) {
    int is_tensor;
    ORT_ABORT_ON_ERROR(g_ort->IsTensor(output_tensors[i], &is_tensor));
    assert(is_tensor);
  }

  return output_tensors;
}

void OnnxReleaseTensor(OrtValue* tensor){
  g_ort->ReleaseValue(tensor);
}

size_t OnnxTensorNumDims(OrtValue* tensor){
  struct OrtTensorTypeAndShapeInfo* shape_info;
  ORT_ABORT_ON_ERROR(g_ort->GetTensorTypeAndShape(tensor, &shape_info));

  size_t dim_count;
    ORT_ABORT_ON_ERROR(g_ort->GetDimensionsCount(shape_info, &dim_count));
  return dim_count;
}

int64_t OnnxTensorDim(OrtValue* tensor, int index){
  struct OrtTensorTypeAndShapeInfo* shape_info;
    ORT_ABORT_ON_ERROR(g_ort->GetTensorTypeAndShape(tensor, &shape_info));

  size_t dim_count;
    ORT_ABORT_ON_ERROR(g_ort->GetDimensionsCount(shape_info, &dim_count));

  int64_t* dims = (int64_t*)malloc(dim_count*sizeof(int64_t));
    ORT_ABORT_ON_ERROR(g_ort->GetDimensions(shape_info, dims, dim_count));
  int64_t ret = *(dims+index);
  free(dims);
  return ret;
}

void OnnxTensorCopyToBuffer(OrtValue* tensor, void * value, size_t size){
  float* f;
    ORT_ABORT_ON_ERROR(g_ort->GetTensorMutableData(tensor, (void**)&f));
  memcpy(value, f, size);
}

static void FreeCharArray(char **a, size_t size) {
  int i;
  for (i = 0; i < size; i++){
    free(a[i]);
  }
}

float** MakeFloatArray(int size) {
  return calloc(sizeof(float*), size);
}

void SetFloatArray(float **a, float *s, int n) {
  a[n] = s;
}

void FreeFloatArray(float **a) {
  free(a);
}

void EuclideanDistance512(float **d, float *res, int ai, int bi, int end) {
  float s,t;
  float *left = d[ai];
  int c = 0;
  for (int j = bi; j < end; j++ ){
    s = 0;
    float *right = d[j];
    for (int i = 0; i < 512; i++) {
      t = left[i] - right[i];
      s += t * t;
    }
    res[c++] = (float)sqrt(s);
  }
}
