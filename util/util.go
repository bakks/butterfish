package util

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/quick"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/afero"
)

// We define types for calling LLM APIs here because I don't want the internal
// interfaces to depend on OpenAI-specific types.
type CompletionRequest struct {
	Ctx           context.Context
	Prompt        string
	Model         string
	MaxTokens     int
	Temperature   float32
	HistoryBlocks []HistoryBlock
	SystemMessage string
	Functions     []FunctionDefinition
	Verbose       bool
}

type CompletionResponse struct {
	Completion         string
	FunctionName       string
	FunctionParameters string
}

type FunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

type HistoryBlock struct {
	Type           int
	Content        string
	FunctionName   string
	FunctionParams string
}

func (this HistoryBlock) String() string {
	// marshal HistoryBlock to JSON
	str, err := json.Marshal(this)
	if err != nil {
		panic(err)
	}
	return string(str)
}

func HistoryBlocksToString(blocks []HistoryBlock) string {
	// marshal HistoryBlock to JSON
	str, err := json.Marshal(blocks)
	if err != nil {
		panic(err)
	}
	return string(str)
}

// Read a file, break into chunks of a given number of bytes, up to a maximum
// number of chunks, and call the callback for each chunk
func ChunkFile(
	fs afero.Fs,
	path string,
	chunkSize int,
	maxChunks int,
	callback func(int, []byte) error) error {

	f, err := fs.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return ChunkFromReader(f, chunkSize, maxChunks, callback)
}

func ChunkFromReader(
	reader io.Reader,
	chunkSize int,
	maxChunks int,
	callback func(int, []byte) error) error {

	buf := make([]byte, chunkSize)

	for i := 0; i < maxChunks || maxChunks == -1; i++ {
		n, err := reader.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}

		err = callback(i, buf[:n])
		if err != nil {
			return err
		}
	}

	return nil
}

// Given a filesystem, a path, a chunk size, and maximum number of chunks,
// return a list of chunks of the file at the given path
func GetFileChunks(ctx context.Context, fs afero.Fs, path string,
	chunkSize int, maxChunks int) ([][]byte, error) {
	chunks := make([][]byte, 0)

	err := ChunkFile(fs, path, chunkSize, maxChunks, func(i int, chunk []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		chunks = append(chunks, chunk)
		return nil
	})

	return chunks, err
}

func GetChunks(reader io.Reader, chunkSize int, maxChunks int) ([][]byte, error) {
	chunks := make([][]byte, 0)
	err := ChunkFromReader(reader, chunkSize, maxChunks, func(i int, chunk []byte) error {
		chunks = append(chunks, chunk)
		return nil
	})
	return chunks, err
}

// Cast an array of byte arrays to an array of strings
func ByteToString(b [][]byte) []string {
	var s []string
	for _, v := range b {
		s = append(s, string(v))
	}
	return s
}

// Call a callback for each subdirectory in a given path
func ForEachSubdir(fs afero.Fs, path string,
	callback func(path string) error) error {

	stats, err := afero.ReadDir(fs, path)
	if err != nil {
		return err
	}

	for _, info := range stats {
		if info.IsDir() {
			p := filepath.Join(path, info.Name())
			err := callback(p)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Returns true if there is piped stdin data that can be read
func IsPipedStdin() bool {
	fi, _ := os.Stdin.Stat()

	if (fi.Mode() & os.ModeCharDevice) == 0 {
		return true
	}
	return false
}

// A io.Writer that caches bytes written and forwards writes to another writer
type CacheWriter struct {
	cache   []byte
	forward io.Writer
}

func NewCacheWriter(forward io.Writer) *CacheWriter {
	return &CacheWriter{
		cache:   make([]byte, 0),
		forward: forward,
	}
}

func (this *CacheWriter) Write(p []byte) (n int, err error) {
	this.cache = append(this.cache, p...)
	return this.forward.Write(p)
}

func (this *CacheWriter) GetCache() []byte {
	return this.cache
}

func (this *CacheWriter) GetLastN(n int) []byte {
	if len(this.cache) < n {
		return this.cache
	}
	return this.cache[len(this.cache)-n:]
}

type StyleCodeblocksWriter struct {
	Writer      io.Writer
	normalColor string
	state       int
	langSuffix  *bytes.Buffer
	blockBuffer *bytes.Buffer
}

const (
	STATE_NORMAL = iota
	STATE_NEWLINE
	STATE_ONE_TICK
	STATE_TWO_TICKS
	STATE_THREE_TICKS
	STATE_IN_BLOCK
	STATE_IN_BLOCK_NEWLINE
	STATE_IN_BLOCK_ONE_TICK
	STATE_IN_BLOCK_TWO_TICKS
	STATE_IN_BLOCK_THREE_TICKS
)

// This writer receives bytes in a stream and looks for markdown code
// blocks (```) and renders them with syntax highlighting.
// The hard part is the stream splits the input into chunks, so we need
// to buffer the input in places.
func (this *StyleCodeblocksWriter) Write(p []byte) (n int, err error) {
	toWrite := new(bytes.Buffer)

	for _, char := range p {

		switch this.state {
		case STATE_NORMAL:
			if char == '\n' {
				this.state = STATE_NEWLINE
			}
			toWrite.WriteByte(char)

		case STATE_NEWLINE, STATE_ONE_TICK, STATE_TWO_TICKS:
			if char == '`' {
				this.state++
			} else if char == '\n' {
				this.state = STATE_NEWLINE
				toWrite.WriteByte(char)
			} else {
				this.state = STATE_NORMAL
				toWrite.WriteByte(char)
			}

		case STATE_THREE_TICKS:
			if char == '\n' {
				this.state = STATE_IN_BLOCK_NEWLINE
				this.blockBuffer = new(bytes.Buffer)
			} else {
				// append to suffix
				if this.langSuffix == nil {
					this.langSuffix = new(bytes.Buffer)
				}
				this.langSuffix.WriteByte(char)
			}

		case STATE_IN_BLOCK:
			if char == '\n' {
				this.state = STATE_IN_BLOCK_NEWLINE
				this.EndOfCodeLine(toWrite)
			} else {
				toWrite.WriteByte(char)
			}
			this.blockBuffer.WriteByte(char)

		case STATE_IN_BLOCK_NEWLINE, STATE_IN_BLOCK_ONE_TICK, STATE_IN_BLOCK_TWO_TICKS:
			if char == '`' {
				this.state++
			} else if char == '\n' {
				this.state = STATE_IN_BLOCK_NEWLINE
				this.EndOfCodeLine(toWrite)
				this.blockBuffer.WriteByte(char)
			} else {
				this.state = STATE_IN_BLOCK
				this.blockBuffer.WriteByte(char)
				toWrite.WriteByte(char)
			}

		case STATE_IN_BLOCK_THREE_TICKS:
			if char == '\n' {
				//this.EndOfCodeBlock(toWrite)
				if this.langSuffix != nil {
					this.langSuffix.Reset()
				}

				toWrite.Write([]byte(this.normalColor))

				this.blockBuffer = nil
				this.state = STATE_NEWLINE
			}
		}
	}

	return this.Writer.Write(toWrite.Bytes())
}

func lastLine(buff *bytes.Buffer) []byte {
	// iterate backwards until we find a newline
	bb := buff.Bytes()
	for i := buff.Len() - 1; i >= 0; i-- {
		if bb[i] == '\n' {
			return bb[i+1:]
		}
	}
	return bb
}

func (this *StyleCodeblocksWriter) EndOfCodeLine(w io.Writer) error {
	temp := new(bytes.Buffer)
	err := quick.Highlight(temp, this.blockBuffer.String(),
		this.langSuffix.String(), "terminal256", "monokai")
	if err != nil {
		log.Printf("error highlighting code block: %s", err)
	}

	last := lastLine(temp)
	w.Write([]byte("\r"))
	w.Write(last)
	w.Write([]byte("\n"))
	return nil
}

func (this *StyleCodeblocksWriter) EndOfCodeBlock(w io.Writer) error {
	// render block
	err := quick.Highlight(w, this.blockBuffer.String(), this.langSuffix.String(), "terminal256", "monokai")
	if err != nil {
		log.Printf("error highlighting code block: %s", err)
	}
	return err
}

func NewStyleCodeblocksWriter(writer io.Writer, normalColor string) *StyleCodeblocksWriter {
	return &StyleCodeblocksWriter{
		Writer:      writer,
		state:       STATE_NEWLINE,
		normalColor: normalColor,
	}
}

// A Writer implementation that allows you to string replace the content
// flowing through
type ReplaceWriter struct {
	Writer io.Writer
	From   string
	To     string
}

func (this *ReplaceWriter) Write(p []byte) (n int, err error) {
	s := strings.Replace(string(p), this.From, this.To, -1)
	return this.Writer.Write([]byte(s))
}

func NewReplaceWriter(writer io.Writer, from string, to string) *ReplaceWriter {
	return &ReplaceWriter{
		Writer: writer,
		From:   from,
		To:     to,
	}
}

type ColorWriter struct {
	Color  string
	Writer io.Writer
}

func NewColorWriter(writer io.Writer, color string) *ColorWriter {
	return &ColorWriter{
		Color:  color,
		Writer: writer,
	}
}

func (this *ColorWriter) Write(p []byte) (n int, err error) {
	return this.Writer.Write([]byte(this.Color + string(p) + "\x1b[0m"))
}

// An implementation of io.Writer that renders output with a lipgloss style
// and filters out the special token "NOOP". This is specially handled -
// we seem to get "NO" as a separate token from GPT.
type StyledWriter struct {
	Writer    io.Writer
	Style     lipgloss.Style
	cache     []byte
	seenInput bool
}

// Lipgloss is a little tricky - if you render a string with newlines it
// turns it into a "block", i.e. each line will be padding to be the same
// length. This is not what we want, so we split on newlines and render
// each line separately.
func MultilineLipglossRender(style lipgloss.Style, str string) string {
	strBuilder := strings.Builder{}
	for i, line := range strings.Split(str, "\n") {
		if i > 0 {
			strBuilder.WriteString("\n")
		}

		if len(line) > 0 {
			rendered := style.Render(line)
			strBuilder.WriteString(rendered)
		}
	}

	return strBuilder.String()
}

// Writer for StyledWriter
// This is a bit insane but it's a dumb way to filter out NOOP split into
// two tokens, should probably be rewritten
func (this *StyledWriter) Write(input []byte) (int, error) {
	if !this.seenInput && unicode.IsSpace(rune(input[0])) {
		return len(input), nil
	}
	this.seenInput = true

	if string(input) == "NOOP" {
		// This doesn't seem to actually happen since it gets split into two
		// tokens? but let's code defensively
		return len(input), nil
	}

	if string(input) == "NO" {
		this.cache = input
		return len(input), nil
	}
	if string(input) == "OP" && this.cache != nil {
		// We have a NOOP, discard it
		this.cache = nil
		return len(input), nil
	}

	if this.cache != nil {
		input = append(this.cache, input...)
		this.cache = nil
	}

	str := string(input)
	rendered := MultilineLipglossRender(this.Style, str)
	renderedBytes := []byte(rendered)

	_, err := this.Writer.Write(renderedBytes)
	if err != nil {
		return 0, err
	}
	// use len(input) rather than len(renderedBytes) because it would be unexpected to get
	// a different number of bytes written than were passed in, (lipgloss
	// render adds ANSI codes)
	return len(input), nil
}

func NewStyledWriter(writer io.Writer, style lipgloss.Style) *StyledWriter {
	adjustedStyle := style.
		UnsetPadding().
		UnsetMargins().
		UnsetWidth().
		UnsetHeight().
		UnsetMaxWidth().
		UnsetMaxHeight().
		UnsetBorderStyle().
		UnsetWidth()

	return &StyledWriter{
		Writer: writer,
		Style:  adjustedStyle,
	}
}
