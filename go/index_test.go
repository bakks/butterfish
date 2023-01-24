package main

import (
	"context"
	"os"
	"testing"

	pb "github.com/bakks/butterfish/proto"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
)

// A basic check to make sure vector comparisons are working
func TestSearch(t *testing.T) {
	index := &VectorIndex{
		index: map[string]*pb.DirectoryIndex{
			"/path/foo": {
				Files: map[string]*pb.FileEmbeddings{
					"test.txt": {
						Embeddings: []*pb.AnnotatedEmbedding{
							{
								Start:  0,
								End:    1,
								Vector: []float64{1, 0, 0, 0, 0},
							},
							{
								Start:  1,
								End:    2,
								Vector: []float64{0, 1, 0, 0, 0},
							},
							{
								Start:  2,
								End:    3,
								Vector: []float64{0, 0, 1, 0, 0},
							},
							{
								Start:  3,
								End:    4,
								Vector: []float64{0, 0, 0, 1, 0},
							},
							{
								Start:  4,
								End:    5,
								Vector: []float64{0, 0, 0, 0, 1},
							},
						},
					},
				},
			},
		},
	}

	results, err := index.SearchWithVector([]float64{1, 0.5, 0, 0, 0}, 3)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(results))
	assert.Equal(t, "/path/foo/test.txt", results[0].AbsPath)
	// The first and second vectors should be the closest matches
	assert.Equal(t, uint64(0), results[0].Embedding.Start)
}

// A mock embedder that implements the Embedder interface
type mockEmbedder struct {
	Calls int
}

func (this *mockEmbedder) CalculateEmbeddings(ctx context.Context, content []string) ([][]float64, error) {
	embeddings := make([][]float64, len(content))
	for i, str := range content {
		// create a fake embedding of the ascii values of the first 5 chars
		embeddings[i] = make([]float64, 5)
		for j := 0; j < 5 && j < len(str); j++ {
			embeddings[i][j] = float64(str[j])
		}
	}

	this.Calls++

	return embeddings, nil
}

func getVirtualVectorIndex() (*VectorIndex, *mockEmbedder) {
	appFS := afero.NewMemMapFs()
	// create test files and directories
	appFS.MkdirAll("/a", 0755)
	afero.WriteFile(appFS, "/a/one", []byte("111111"), 0644)
	afero.WriteFile(appFS, "/a/two", []byte("222222"), 0644)
	afero.WriteFile(appFS, "/a/b/three", []byte("333333"), 0644)
	afero.WriteFile(appFS, "/a/b/c/d/four", []byte("444444"), 0644)

	embedder := &mockEmbedder{}

	vectorIndex := &VectorIndex{
		index:     map[string]*pb.DirectoryIndex{},
		embedder:  embedder,
		out:       os.Stdout,
		verbosity: 2,
		fs:        appFS,
	}

	return vectorIndex, embedder
}

// The goal here is to test index caching on disk, we use a mock filesystem
// and mock out the embedding function
func TestFileCaching(t *testing.T) {
	index, embedder := getVirtualVectorIndex()
	ctx := context.Background()

	err := index.IndexPath(ctx, "/a/b/c/", false)
	assert.NoError(t, err)
	assert.Equal(t, 1, embedder.Calls)

	scored, err := index.Search(ctx, "444", 1)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scored))
	assert.Equal(t, "/a/b/c/d/four", scored[0].AbsPath)

}
