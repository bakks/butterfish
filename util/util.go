package util

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/afero"
)

// Read a file, break into chunks of a given number of bytes, up to a maximum
// number of chunks, and call the callback for each chunk
func ChunkFile(
	fs afero.Fs,
	path string,
	chunkSize uint64,
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
	chunkSize uint64,
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
	chunkSize uint64, maxChunks int) ([][]byte, error) {
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

func GetChunks(reader io.Reader, chunkSize uint64, maxChunks int) ([][]byte, error) {
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

// An implementation of io.Writer that renders output with a lipgloss style
// and filters out the special token "NOOP". This is specially handled -
// we seem to get "NO" as a separate token from GPT.
type StyledWriter struct {
	Writer io.Writer
	Style  lipgloss.Style
	cache  []byte
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
