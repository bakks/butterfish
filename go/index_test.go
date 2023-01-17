package main

import (
	"fmt"
	"testing"

	"github.com/drewlanenga/govector"
)

// A test for vector indexing
func TestSearch(t *testing.T) {
	vectors := [][]float64{
		{1, 0, 0, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 0, 1, 0},
		{0, 0, 0, 0, 1},
	}

	index := NewVectorIndex()
	for i, v := range vectors {
		vector, err := govector.AsVector(v)
		if err != nil {
			t.Error(err)
		}

		index.AddVector(fmt.Sprintf("%d", i), &vector)
	}

	query, err := govector.AsVector([]float64{1, 0.5, 0, 0, 0})
	if err != nil {
		t.Error(err)
	}

	results, err := index.Search(&query, 3)
	if err != nil {
		t.Error(err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}

	if results[0].Vector.Note != "0" {
		t.Errorf("Expected first result to be '0', got '%s'", results[0].Vector.Note)
	}

	if results[1].Vector.Note != "1" {
		t.Errorf("Expected second result to be '1', got '%s'", results[1].Vector.Note)
	}
}
