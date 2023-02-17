package t5

import (
	"testing"

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

// Test inference on a T5 model
func TestInference(t *testing.T) {
	path := "./tokenizer.json"
	config := LoadTokenizerConfig(path)
	tokenizer := NewTokenizer(config)
	input := "Translate English to German: How are you?"

	encoded := tokenizer.Encode(input)

	InferT5(encoded, func(tokenId int) {
		decoded := tokenizer.Decode([]int{tokenId}, true)
		println(decoded)
	})
}
