package butterfish

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

var benchmarkSinkInt int
var benchmarkSinkMsg *byteMsg
var benchmarkSinkString string

const perfChunkSize = 16 * 1024

func loadReplayTrace(b *testing.B) []byte {
	b.Helper()

	path := os.Getenv("BUTTERFISH_PERF_TRACE")
	if path == "" {
		return syntheticReplayTrace(1024)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("failed to read trace file %q: %v", path, err)
	}
	if len(data) == 0 {
		b.Fatalf("trace file is empty: %q", path)
	}

	return data
}

func syntheticReplayTrace(lines int) []byte {
	var builder strings.Builder

	for i := 0; i < lines; i++ {
		builder.WriteString(fmt.Sprintf("line=%05d payload=%s\n", i, strings.Repeat("x", 72)))
		// Add prompt markers and status codes so ParsePS1 does real work.
		builder.WriteString(PROMPT_PREFIX)
		builder.WriteString(EMOJI_DEFAULT)
		builder.WriteString(fmt.Sprintf(" %d", i%7))
		builder.WriteString(PROMPT_SUFFIX)
		builder.WriteByte(' ')
		builder.WriteString("cmd output continues\n")

		// Sprinkle ANSI styling to mimic noisy shells.
		builder.WriteString("\x1b[32mcolored\x1b[0m text ")
		builder.WriteString(strings.Repeat("z", 48))
		builder.WriteByte('\n')
	}

	return []byte(builder.String())
}

func splitBytes(data []byte, chunkSize int) [][]byte {
	chunks := make([][]byte, 0, (len(data)+chunkSize-1)/chunkSize)
	for start := 0; start < len(data); start += chunkSize {
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[start:end])
	}
	return chunks
}

func BenchmarkShellHistoryAppend4KB(b *testing.B) {
	b.ReportAllocs()
	chunk := strings.Repeat("x", 4096) + "\n"
	b.SetBytes(int64(len(chunk)))

	history := NewShellHistory()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Keep a bounded growth profile so runtime and memory stay stable.
		if i > 0 && i%64 == 0 {
			history = NewShellHistory()
		}
		history.Append(historyTypeShellOutput, chunk)
	}

	if len(history.Blocks) > 0 {
		benchmarkSinkInt = history.Blocks[len(history.Blocks)-1].Content.Size()
	}
}

func BenchmarkShellBufferWrite4KB(b *testing.B) {
	b.ReportAllocs()
	chunk := strings.Repeat("y", 4096)
	b.SetBytes(int64(len(chunk)))
	buffer := NewShellBuffer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i > 0 && i%64 == 0 {
			buffer = NewShellBuffer()
		}
		update := buffer.Write(chunk)
		benchmarkSinkInt += len(update)
	}
}

func BenchmarkParsePS1NoPrompt(b *testing.B) {
	b.ReportAllocs()
	input := strings.Repeat("plain output without prompt markers\n", 128)
	b.SetBytes(int64(len(input)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lastStatus, prompts, cleaned := ParsePS1(input, ps1FullRegex, EMOJI_DEFAULT)
		benchmarkSinkInt += lastStatus + prompts + len(cleaned)
	}
}

func BenchmarkParsePS1WithPrompt(b *testing.B) {
	b.ReportAllocs()
	var builder strings.Builder
	for i := 0; i < 128; i++ {
		builder.WriteString("before ")
		builder.WriteString(PROMPT_PREFIX)
		builder.WriteString(EMOJI_DEFAULT)
		builder.WriteString(fmt.Sprintf(" %d", i%3))
		builder.WriteString(PROMPT_SUFFIX)
		builder.WriteString(" after\n")
	}

	input := builder.String()
	b.SetBytes(int64(len(input)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lastStatus, prompts, cleaned := ParsePS1(input, ps1FullRegex, EMOJI_DEFAULT)
		benchmarkSinkInt += lastStatus + prompts + len(cleaned)
	}
}

func BenchmarkNewByteMsgCopy16KB(b *testing.B) {
	b.ReportAllocs()
	data := bytes.Repeat([]byte("a"), perfChunkSize)
	b.SetBytes(int64(len(data)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkSinkMsg = NewByteMsg(data)
	}
}

func BenchmarkShellOutputReplay(b *testing.B) {
	trace := loadReplayTrace(b)
	chunks := splitBytes(trace, perfChunkSize)
	b.SetBytes(int64(len(trace)))
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		history := NewShellHistory()
		statusSum := 0

		for _, chunk := range chunks {
			raw := string(chunk)

			lastStatus, prompts, childOutStr := ParsePS1(raw, ps1FullRegex, EMOJI_DEFAULT)
			statusSum += lastStatus + prompts

			if len(raw) > 0 && strings.HasPrefix(raw, "\x1b[1m") && ZSH_CLEAR_REGEX.MatchString(raw) {
				continue
			}

			history.Append(historyTypeShellOutput, childOutStr)
		}

		benchmarkSinkInt = statusSum + len(history.Blocks)
		if len(history.Blocks) > 0 {
			benchmarkSinkString = history.Blocks[len(history.Blocks)-1].Content.String()
		}
	}
}
