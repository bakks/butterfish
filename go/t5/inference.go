package t5

import (
	"path/filepath"

	"github.com/bakks/butterfish/go/onnx"
)

func int64ToInt(a []int64) []int {
	b := make([]int, len(a))
	for i, v := range a {
		b[i] = int(v)
	}
	return b
}

func T5(prompt string, maxLength int, coreml bool, modelPath string, execPro onnx.ExecutionProvider, callback func(string)) string {
	tokenizerPath := filepath.Join(modelPath, "tokenizer.json")
	encoderPath := filepath.Join(modelPath, "encoder_model.onnx")
	decoderPath := filepath.Join(modelPath, "encoder_model.onnx")

	config := LoadTokenizerConfig(tokenizerPath)
	tokenizer := NewTokenizer(config)

	encoded := tokenizer.Encode(prompt)

	mode := onnx.ModeCPU
	if coreml {
		mode = onnx.ModeCoreML
	}

	output := T5TokensToTokensInference(encoded, maxLength, func(tokenId int) {
		decoded := tokenizer.Decode([]int{tokenId}, true)
		callback(decoded)
	}, encoderPath, decoderPath, mode)

	decoded := tokenizer.Decode(int64ToInt(output), true)
	return decoded
}

func T5TokensToTokensInference(tokens []int, maxLength int, progressCallback func(int), encoderModelPath, decoderModelPath string, execMode onnx.ExecutionProvider) []int64 {
	inputTokens := make([]int64, len(tokens))
	for i, t := range tokens {
		inputTokens[i] = int64(t)
	}

	//topK := 0
	startOfDecoderTokenId := 0
	endOfDecoderTokenId := 1
	outputTokenIds := []int64{int64(startOfDecoderTokenId)}
	numOutputTokens := 1
	maxOutputTokens := numOutputTokens + maxLength

	// call encoder
	hiddenState, hiddenStateShape := EncoderInference(encoderModelPath, execMode, inputTokens)

	for numOutputTokens < maxOutputTokens {
		// call decoder in loop
		logits, logitsShape := DecoderInference(decoderModelPath, execMode, outputTokenIds, len(tokens), hiddenState, hiddenStateShape)
		//fmt.Printf("logit shape: %v\n", logitsShape)

		newTokenId := SampleLogitsGreedily(logits, logitsShape)
		outputTokenIds = append(outputTokenIds, int64(newTokenId))
		numOutputTokens++

		progressCallback(newTokenId)

		if newTokenId == endOfDecoderTokenId {
			break
		}
	}

	return outputTokenIds
}

func EncoderInference(modelPath string, execPro onnx.ExecutionProvider, tokens []int64) ([]float32, []int64) {

	numTokens := len(tokens)

	encoderInputNames := []string{"input_ids", "attention_mask"}
	encoderOutputNames := []string{"last_hidden_state"}
	encoderModel := onnx.NewModel(modelPath, encoderInputNames, encoderOutputNames, execPro)

	inputDims := []int64{1, int64(numTokens)}
	inputIdsTensor := encoderModel.NewInt64Tensor(inputDims, tokens)

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

	//fmt.Printf("output shape: %v\n", outputs[0].Shape())
	// 1 batch, numTokens sequence length, 512 hidden size
	numValues := outputs[0].Size()
	data := make([]float32, numValues)
	outputs[0].CopyToBuffer(data, int(numValues)*4) // 4 bytes per float32
	//fmt.Printf("data: %v\n", data)

	encoderModel.Delete()

	return data, outputs[0].Shape()
}

func DecoderInference(modelPath string, execPro onnx.ExecutionProvider, tokens []int64, maskLen int, hiddenState []float32, hiddenStateDims []int64) ([]float32, []int64) {
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

	model := onnx.NewModel(modelPath, decoderInputNames, decoderOutputNames, execPro)
	numTokens := len(tokens)
	inputDims := []int64{1, int64(numTokens)}
	maskDims := []int64{1, int64(maskLen)}

	attentionMask := make([]int64, maskLen)
	for i := range attentionMask {
		attentionMask[i] = 1
	}

	inputIdsTensor := model.NewInt64Tensor(inputDims, tokens)
	attentionMaskTensor := model.NewInt64Tensor(maskDims, attentionMask)
	hiddenStatesTensor := model.NewFloat32Tensor(hiddenStateDims, hiddenState)

	decoderInputs := map[string]*onnx.Tensor{
		"encoder_attention_mask": attentionMaskTensor,
		"input_ids":              inputIdsTensor,
		"encoder_hidden_states":  hiddenStatesTensor,
	}

	decoderOutputs := model.RunInference(decoderInputs)

	// Pull logits out of the output tensors and return it
	logitsTensor := decoderOutputs[0]
	numValues := logitsTensor.Size()
	logits := make([]float32, numValues)
	logitsTensor.CopyToBuffer(logits, int(numValues)*4) // 4 bytes per float32
	return logits, logitsTensor.Shape()
}

// Shape is like []int{1 (not batched), 10 (sequence length), 32368 (vocab size)}
func SampleLogitsGreedily(logits []float32, shape []int64) int {
	if len(shape) != 3 {
		panic("shape must be []int{batchSize, seqLength, vocabSize}")
	}

	batchSize := int(shape[0])
	seqLength := int(shape[1])
	vocabSize := int(shape[2])
	n := batchSize * seqLength * vocabSize
	startIndex := n - vocabSize

	argmaxi := 0
	argmax := logits[startIndex+argmaxi]

	for i := 1; i < vocabSize; i++ {
		l := logits[startIndex+i]
		if l > argmax {
			argmaxi = i
			argmax = l
		}
	}

	return argmaxi
}
