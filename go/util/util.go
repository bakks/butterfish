package util

import (
	"context"
	"io"
	"os"
	"path/filepath"

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

func ChunkFromReader(reader io.Reader, chunkSize uint64, maxChunks int, callback func(int, []byte) error) error {
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
