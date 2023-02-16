// Use of this source code is governed by a Apache-style
// license that can be found in the LICENSE file.

package onnx

/*
#cgo LDFLAGS: -lonnxruntime -lm
#cgo CFLAGS: -O3
#cgo arm64 CFLAGS: -DARMNN=1
#include "onnx_capi.h"
*/
import "C"
import (
	"math"
	"reflect"
	"unsafe"
)

type Model struct {
	env *C.OnnxEnv
}

type Tensor struct {
	t *C.OrtValue
}

type EP int

const (
	CPU EP = iota
	CUDA
	ROCM
	ARMNN
	TENSORRT
)

func NewModel(
	model_path string,
	shape []int64,
	inputNames []string,
	outputNames []string,
	mode EP) *Model {

	ptr := C.CString(model_path)
	defer C.free(unsafe.Pointer(ptr))

	session := C.OnnxNewOrtSession(ptr, C.int(mode))

	session.input_shape_len = C.size_t(len(shape))
	for i, s := range shape {
		session.input_shape[i] = C.int64_t(s)
	}

	session.input_names_len = C.size_t(len(inputNames))
	for i, s := range inputNames {
		session.input_names[i] = C.CString(s)
	}

	session.output_names_len = C.size_t(len(outputNames))
	for i, s := range outputNames {
		session.output_names[i] = C.CString(s)
	}

	return &Model{env: session}
}

func (this *Model) NewInt64Tensor(values []int64) *Tensor {
	dims := []int{1, int(len(values))}
	ortValue := C.OnnxCreateTensorInt64(
		this.env,
		(*C.int64_t)(unsafe.Pointer(&values[0])),
		C.size_t(len(values)*8),
		(*C.int64_t)(unsafe.Pointer(&dims[0])),
		C.size_t(len(dims)))
	return &Tensor{t: ortValue}
}

// Invoke the task.
func (m *Model) RunInference(data map[string]*Tensor) *Tensor {
	var inputs []*C.OrtValue
	for _, v := range data {
		inputs = append(inputs, v.t)
	}
	t := C.OnnxRunInference2(m.env,
		(**C.OrtValue)(unsafe.Pointer(&inputs[0])),
		C.size_t(len(inputs)*8),
	)
	return &Tensor{t: t}
}

func (m *Model) Delete() {
	if m != nil {
		C.OnnxDeleteOrtSession(m.env)
	}
}

func (t *Tensor) NumDims() int {
	return int(C.OnnxTensorNumDims(t.t))
}

// Dim return dimension of the element specified by index.
func (t *Tensor) Dim(index int) int64 {
	return int64(C.OnnxTensorDim(t.t, C.int32_t(index)))
}

// Shape return shape of the tensor.
func (t *Tensor) Shape() []int64 {
	shape := make([]int64, t.NumDims())
	for i := 0; i < t.NumDims(); i++ {
		shape[i] = t.Dim(i)
	}
	return shape
}

func (t *Tensor) Delete() {
	if t != nil {
		C.OnnxReleaseTensor(t.t)
	}
}

func (t *Tensor) CopyToBuffer(b interface{}, size int) {
	C.OnnxTensorCopyToBuffer(t.t, unsafe.Pointer(reflect.ValueOf(b).Pointer()), C.size_t(size))
}

var EuclideanDistance512 = func(d [][]float32, ai, bi, end int) []float32 {
	var (
		s, t float32
	)
	res := make([]float32, end-bi)
	c := 0
	for j := bi; j < end; j++ {
		s = 0
		t = 0
		for i := 0; i < 512; i++ {
			t = d[ai][i] - d[j][i]
			s += t * t
		}

		res[c] = float32(math.Sqrt(float64(s)))

		c++
	}
	return res
}

var EuclideanDistance512C = func(d [][]float32, ai, bi, end int) []float32 {
	res := make([]float32, end-bi)

	data := C.MakeFloatArray(C.int(len(d)))
	defer C.FreeFloatArray(data)
	for i, v := range d {
		C.SetFloatArray(data, (*C.float)(unsafe.Pointer(&v[0])), C.int(i))
	}

	C.EuclideanDistance512(
		data,
		(*C.float)(unsafe.Pointer(&res[0])),
		C.int(ai),
		C.int(bi),
		C.int(end),
	)
	return res
}
