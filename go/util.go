package main

import (
	"context"
	"io"
	"os"

	"github.com/spf13/afero"
)

// Read a file, break into chunks of a given number of bytes, up to a maximum
// number of chunks, and call the callback for each chunk
func chunkFile(
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

	buf := make([]byte, chunkSize)
	for i := 0; i < maxChunks || maxChunks == -1; i++ {
		n, err := f.Read(buf)
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
func getFileChunks(ctx context.Context, fs afero.Fs, path string,
	chunkSize uint64, maxChunks int) ([][]byte, error) {
	var chunks [][]byte
	err := chunkFile(fs, path, chunkSize, maxChunks, func(i int, chunk []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return chunks, nil
}

// Cast an array of byte arrays to an array of strings
func byteToString(b [][]byte) []string {
	var s []string
	for _, v := range b {
		s = append(s, string(v))
	}
	return s
}

// Call a callback for each subdirectory in a given path
func forEachSubdir(ctx context.Context, fs afero.Fs, path string,
	fn func(ctx context.Context, path string) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	return afero.Walk(fs, path, func(childPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Walk produces back the path we started with, so we need to skip it
		if childPath == path {
			return nil
		}
		// If we find a directory, call the callback
		if info.IsDir() {
			return fn(ctx, childPath)
		}
		return nil
	})
}

// Return a list of filenames and FileInfos for a given directory path
func listFiles(ctx context.Context, fs afero.Fs, path string) ([]string,
	[]os.FileInfo, error) {
	var files []string
	var stats []os.FileInfo

	err := afero.Walk(fs, path, func(childPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !info.IsDir() {
			files = append(files, childPath)
			stats = append(stats, info)
		}
		return nil
	})
	return files, stats, err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
