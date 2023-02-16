package t5

import (
	"fmt"

	"github.com/bakks/butterfish/go/onnx"
)

func InferT5(tokens []int) {
	path := "/Users/bakks/butterfish/flan/onnx/encoder_model.onnx"

	input := make([]int64, len(tokens))
	for i, t := range tokens {
		input[i] = int64(t)
	}

	shape := []int64{1, int64(len(tokens))}
	inputNames := []string{"input_ids", "attention_mask"}
	outputNames := []string{"last_hidden_state"}
	model := onnx.NewModel(path, shape, inputNames, outputNames, onnx.CPU)

	inputIdsTensor := model.NewInt64Tensor(input)

	// create a slice of 1s that is the same langth as the tokens slice
	attentionMask := make([]int64, len(tokens))
	for i := range attentionMask {
		attentionMask[i] = 1
	}

	attentionMaskTensor := model.NewInt64Tensor(attentionMask)

	inputs := map[string]*onnx.Tensor{
		"input_ids":      inputIdsTensor,
		"attention_mask": attentionMaskTensor,
	}

	outputs := model.RunInference(inputs)
	fmt.Printf("outputs: %v\n", outputs.Shape())
	data := &[512 * 6]float32{}
	outputs.CopyToBuffer(data, 512*4*6)
	fmt.Printf("data: %v\n", data)
}
