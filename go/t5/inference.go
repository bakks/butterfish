package t5

import (
	"fmt"

	"github.com/bakks/butterfish/go/onnx"
)

func InferT5(tokens []int) {
	input := make([]int64, len(tokens))
	for i, t := range tokens {
		input[i] = int64(t)
	}
	//
	//	hiddenState := EncoderInference(input)
	EncoderInference(input)

	blankState := make([]float32, 16*512)
	DecoderInference(input, blankState)
}

func EncoderInference(tokens []int64) []float32 {
	encoderPath := "/Users/bakks/butterfish/flan/onnx/encoder_model.onnx"

	numTokens := len(tokens)

	input := make([]int64, numTokens)
	for i, t := range tokens {
		input[i] = int64(t)
	}

	encoderInputNames := []string{"input_ids", "attention_mask"}
	encoderOutputNames := []string{"last_hidden_state"}
	encoderModel := onnx.NewModel(encoderPath, encoderInputNames, encoderOutputNames, onnx.CPU)

	inputDims := []int64{1, int64(numTokens)}
	inputIdsTensor := encoderModel.NewInt64Tensor(inputDims, input)

	// create a slice of 1s that is the same langth as the tokens slice
	attentionMask := make([]int64, numTokens)
	for i := range attentionMask {
		attentionMask[i] = 1
	}

	attentionMaskTensor := encoderModel.NewInt64Tensor(inputDims, attentionMask)

	encoderInputs := map[string]*onnx.Tensor{
		"input_ids":      inputIdsTensor,
		"attention_mask": attentionMaskTensor,
	}

	outputs := encoderModel.RunInference(encoderInputs)
	fmt.Printf("encoder output shape: %v\n", outputs[0].Shape())

	// 1 batch, numTokens sequence length, 512 hidden size
	numValues := 1 * numTokens * 512
	data := make([]float32, numValues)
	outputs[0].CopyToBuffer(data, numValues*4) // 4 bytes per float32
	//fmt.Printf("data: %v\n", data)

	encoderModel.Delete()

	return data
}

func DecoderInference(tokens []int64, hiddenState []float32) {
	decoderPath := "/Users/bakks/butterfish/flan/onnx/decoder_model.onnx"

	decoderInputNames := []string{
		"encoder_attention_mask",
		"input_ids",
		"encoder_hidden_states"}
	decoderOutputNames := []string{
		"logits",
		"present.0.decoder.key", "present.0.decoder.value",
		"present.0.encoder.key", "present.0.encoder.value",
		"present.1.decoder.key", "present.1.decoder.value",
		"present.1.encoder.key", "present.1.encoder.value",
		"present.2.decoder.key", "present.2.decoder.value",
		"present.2.encoder.key", "present.2.encoder.value",
		"present.3.decoder.key", "present.3.decoder.value",
		"present.3.encoder.key", "present.3.encoder.value",
		"present.4.decoder.key", "present.4.decoder.value",
		"present.4.encoder.key", "present.4.encoder.value",
		"present.5.decoder.key", "present.5.decoder.value",
		"present.5.encoder.key", "present.5.encoder.value",
		"present.6.decoder.key", "present.6.decoder.value",
		"present.6.encoder.key", "present.6.encoder.value",
		"present.7.decoder.key", "present.7.decoder.value",
		"present.7.encoder.key", "present.7.encoder.value",
		"encoder_last_hidden_state",
	}

	model := onnx.NewModel(decoderPath, decoderInputNames, decoderOutputNames, onnx.CPU)
	numTokens := len(tokens)
	hiddenStatesDims := []int64{1, int64(numTokens), 512}
	inputDims := []int64{1, int64(numTokens)}

	// create a slice of 1s that is the same langth as the tokens slice
	attentionMask := make([]int64, numTokens)
	for i := range attentionMask {
		attentionMask[i] = 1
	}

	inputIdsTensor := model.NewInt64Tensor(inputDims, tokens)
	attentionMaskTensor := model.NewInt64Tensor(inputDims, attentionMask)
	hiddenStatesTensor := model.NewFloat32Tensor(hiddenStatesDims, hiddenState)

	decoderInputs := map[string]*onnx.Tensor{
		"encoder_attention_mask": attentionMaskTensor,
		"input_ids":              inputIdsTensor,
		"encoder_hidden_states":  hiddenStatesTensor,
	}
	decoderOutputs := model.RunInference(decoderInputs)
	fmt.Printf("decoder output shape: %v\n", decoderOutputs[0].Shape())
}
