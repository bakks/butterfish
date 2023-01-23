package main

import (
	"testing"

	pb "github.com/bakks/butterfish/proto"
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

	results, err := index.Search([]float64{1, 0.5, 0, 0, 0}, 3)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(results))
	assert.Equal(t, "/path/foo/test.txt", results[0].AbsPath)
	// The first and second vectors should be the closest matches
	assert.Equal(t, uint64(0), results[0].Embedding.Start)
}
