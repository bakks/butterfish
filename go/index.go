package main

import (
	"fmt"
	"sort"

	"github.com/drewlanenga/govector"
)

type AnnotatedVector struct {
	Note   string
	Vector *govector.Vector
}

func NewAnnotatedVector(note string, v []float64) (AnnotatedVector, error) {
	vector, err := govector.AsVector(v)
	if err != nil {
		return AnnotatedVector{}, err
	}
	return AnnotatedVector{note, &vector}, nil
}

func (this *AnnotatedVector) String() string {
	fullvec := fmt.Sprintf("%v", this.Vector)
	return fmt.Sprintf("AnnotatedVector: %s, %s",
		this.Note, fullvec[:min(len(fullvec), 40)])
}

type VectorIndex struct {
	index []AnnotatedVector
}

type ScoredVector struct {
	Score  float64
	Vector AnnotatedVector
}

func NewVectorIndex() *VectorIndex {
	return &VectorIndex{
		index: make([]AnnotatedVector, 0),
	}
}

func (this *VectorIndex) AddAnnotatedVector(av AnnotatedVector) {
	this.index = append(this.index, av)
}

func (this *VectorIndex) AddAnnotatedVectors(av []AnnotatedVector) {
	this.index = append(this.index, av...)
}

func (this *VectorIndex) AddVector(note string, v []float64) error {
	vector, err := govector.AsVector(v)
	if err != nil {
		return err
	}
	this.index = append(this.index, AnnotatedVector{note, &vector})
	return nil
}

func (this *VectorIndex) AddGoVector(note string, v *govector.Vector) {
	this.index = append(this.index, AnnotatedVector{note, v})
}

func (this *VectorIndex) SearchRaw(query []float64, k int) ([]ScoredVector, error) {
	queryVector, err := govector.AsVector(query)
	if err != nil {
		return nil, err
	}
	return this.Search(&queryVector, k)
}

// Super naive vector search operation.
// - First we brute first search by iterating over all stored vectors
//     and calculating cosine distance
// - Next we sort based on score
func (this *VectorIndex) Search(query *govector.Vector, k int) ([]ScoredVector, error) {
	scored := make([]ScoredVector, len(this.index))

	for i, v := range this.index {
		distance, err := govector.Cosine(*query, *v.Vector)
		if err != nil {
			return nil, err
		}
		scored[i] = ScoredVector{distance, v}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	results := scored[:min(len(scored), k)]

	return results, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
