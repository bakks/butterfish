package butterfish

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A golang test for ShellBuffer
func TestShellBuffer(t *testing.T) {
	buffer := NewShellBuffer()
	buffer.Write("hello")
	assert.Equal(t, "hello", buffer.String())
	buffer.Write(" world")
	assert.Equal(t, "hello world", buffer.String())
	buffer.Write("!")
	assert.Equal(t, "hello world!", buffer.String())
	buffer.Write("\x1b[D")
	assert.Equal(t, "hello world!", buffer.String())

	buffer = NewShellBuffer()
	buffer.Write("~/butterfish ᐅ")
	assert.Equal(t, "~/butterfish ᐅ", buffer.String())

	// test weird ansii escape sequences
	red := "\x1b[31m"
	buffer = NewShellBuffer()
	buffer.Write("foo")
	buffer.Write(red)
	buffer.Write("bar")
	assert.Equal(t, "foo"+red+"bar", buffer.String())

	// test shell control characters
	buffer = NewShellBuffer()
	buffer.Write(string([]byte{0x6c, 0x08, 0x6c, 0x73, 0x20}))
	assert.Equal(t, "ls ", buffer.String())

	// test left cursor, backspace, and then insertion
	buffer = NewShellBuffer()
	buffer.Write("hello world")
	buffer.Write("\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D")
	buffer.Write("foo   ")
	buffer.Write("\x08\x7f") // backspace
	assert.Equal(t, "hello foo world", buffer.String())
}

// function to test shell history using golang testing tools
func TestShellHistory(t *testing.T) {
	history := NewShellHistory()

	history.Append(historyTypePrompt, "prompt1")
	history.Append(historyTypeShellInput, "shell1")
	history.Append(historyTypeShellOutput, "output1")
	history.Append(historyTypeLLMOutput, "llm1")

	output := HistoryBlocksToString(history.GetLastNBytes(256))
	assert.Equal(t, "prompt1\nshell1\noutput1\nllm1", output)

	history.Append(historyTypePrompt, "prompt2 xxxxxxxxxxxxxxxxxxxxxxxxxxxxx       xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx             xxxxxxxxxxxxxxxxxxxxxxxxxxxxx         xxxxxxxxxx         xxxxxxxxxxxxxxxxxxx               xxxxxxxxxxxxxxxxxxxxx")
	history.Append(historyTypeLLMOutput, "llm2")

	output = HistoryBlocksToString(history.GetLastNBytes(14))
	assert.Equal(t, "llm2", output)

	history.Append(historyTypeLLMOutput, "more llm ᐅ")
	output = HistoryBlocksToString(history.GetLastNBytes(24))
	assert.Equal(t, "llm2more llm ᐅ", output)
}
