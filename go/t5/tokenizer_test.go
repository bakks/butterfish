package t5

import (
	"fmt"
	"testing"

	"github.com/bakks/butterfish/go/onnx"
	"github.com/stretchr/testify/assert"
)

// Load the tokenizer.json file in the current directory, attempt to tokenize the
// string 'a quick brown fox jumped over the lazy dog', and compare the output
func TestTokenizer(t *testing.T) {
	path := "./tokenizer.json"
	config := LoadTokenizerConfig(path)
	tokenizer := NewTokenizer(config)
	input := "a quick brown fox jumped over the lazy dog"
	encoded := tokenizer.Encode(input)
	decoded := tokenizer.Decode(encoded, true)

	assert.Equal(t, input, decoded)
}

const encoderPath = "/Users/bakks/butterfish/flan/onnx/encoder_model.onnx"
const decoderPath = "/Users/bakks/butterfish/flan/onnx/decoder_model.onnx"

// Test inference on a T5 model
func TestInference(t *testing.T) {
	path := "./tokenizer.json"
	config := LoadTokenizerConfig(path)
	tokenizer := NewTokenizer(config)
	input := "Translate English to German: How are you?"
	fmt.Printf("input: %s\n", input)

	encoded := tokenizer.Encode(input)

	output := T5TokensToTokensInference(encoded, 32, func(tokenId int) {
		decoded := tokenizer.Decode([]int{tokenId}, true)
		fmt.Println(decoded)
	}, encoderPath, decoderPath, onnx.ModeCPU)

	decoded := tokenizer.Decode(int64ToInt(output), true)
	fmt.Println(decoded)
}
