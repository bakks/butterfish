package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	pb "github.com/bakks/butterfish/proto"
	"github.com/drewlanenga/govector"
	"github.com/golang/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AnnotatedVector struct {
	Name   string
	Start  uint64
	End    uint64
	Vector []float64
}

func NewAnnotatedVector(name string, start uint64, end uint64, vector []float64) *AnnotatedVector {
	return &AnnotatedVector{
		Name:   name,
		Start:  start,
		End:    end,
		Vector: vector,
	}

}

func (this *AnnotatedVector) ToProto() *pb.AnnotatedEmbedding {
	return &pb.AnnotatedEmbedding{
		Name:   this.Name,
		Start:  this.Start,
		End:    this.End,
		Vector: this.Vector,
	}
}

func AnnotatedVectorsInternalToProto(internal []*AnnotatedVector) []*pb.AnnotatedEmbedding {
	proto := []*pb.AnnotatedEmbedding{}
	for _, vec := range internal {
		proto = append(proto, vec.ToProto())
	}
	return proto
}

type ScoredEmbedding struct {
	Score     float64
	Embedding *pb.AnnotatedEmbedding
}

type Embedder interface {
	EmbedFile(path string) ([]*AnnotatedVector, error)
}

type VectorIndex struct {
	index    map[string]*pb.DirectoryIndex
	embedder Embedder
}

func NewVectorIndex() *VectorIndex {
	return &VectorIndex{
		index: make(map[string]*pb.DirectoryIndex),
	}
}

func (this *VectorIndex) SetEmbedder(embedder Embedder) {
	this.embedder = embedder
}

func (this *VectorIndex) SearchRaw(query []float64, k int) ([]*ScoredEmbedding, error) {
	queryVector, err := govector.AsVector(query)
	if err != nil {
		return nil, err
	}
	return this.Search(&queryVector, k)
}

// Super naive vector search operation.
// - First we brute force search by iterating over all stored vectors
//     and calculating cosine distance
// - Next we sort based on score
func (this *VectorIndex) Search(query *govector.Vector, k int) ([]*ScoredEmbedding, error) {
	scored := []*ScoredEmbedding{}

	for _, dirIndex := range this.index {
		for _, fileIndex := range dirIndex.Files {
			for _, embedding := range fileIndex.Embeddings {
				govec, err := govector.AsVector(embedding.Vector)

				distance, err := govector.Cosine(*query, govec)
				if err != nil {
					return nil, err
				}
				scored = append(scored, &ScoredEmbedding{distance, embedding})
			}
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	results := scored[:min(len(scored), k)]

	return results, nil
}

// Assumes the path is a valid butterfish index file
func (this *VectorIndex) LoadDotfile(dotfile string) error {
	dotfile = filepath.Clean(dotfile)

	// Read the entire dotfile into a bytes buffer
	file, err := os.Open(dotfile)
	if err != nil {
		return nil
	}
	defer file.Close()

	// Read the entire file into a buffer
	buf, err := ioutil.ReadAll(file)
	if err != nil {
		return err
	}

	// Unmarshal the buffer into a DirectoryIndex
	var dirIndex pb.DirectoryIndex
	err = proto.Unmarshal(buf, &dirIndex)
	if err != nil {
		return err
	}

	this.index[dotfile] = &dirIndex
	return nil
}

func (this *VectorIndex) SaveDotfile(dotfile string) error {
	dotfile = filepath.Clean(dotfile)

	// Marshal the index into a buffer
	dirIndex, ok := this.index[dotfile]
	if !ok {
		return fmt.Errorf("No index found for %s", dotfile)
	}

	buf, err := proto.Marshal(dirIndex)
	if err != nil {
		return err
	}

	// Write the buffer to the dotfile
	err = ioutil.WriteFile(dotfile, buf, 0644)
	return err
}

func (this *VectorIndex) LoadPath(path string) error {
	// Check the path exists, bail out if not
	path = filepath.Clean(path)
	fileInfo, err := os.Stat(path)
	if err != nil {
		return err
	}

	// If the path is a file then find its parent directory
	dirPath := path
	if !fileInfo.IsDir() {
		dirPath = filepath.Dir(path)
	}

	// Check for a butterfish index file, if none found then we take no action,
	// otherwise we load the index
	dotfilePath := filepath.Join(dirPath, ".butterfish_index")
	_, err = os.Stat(dotfilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		err = this.LoadDotfile(dotfilePath)
		if err != nil {
			return nil
		}
	}

	// Iterate through files in the directory, if they are directories
	// then we load them recursively
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return this.LoadPath(path)
		}
		return nil
	})

	return nil
}

func (this *VectorIndex) LoadPaths(paths []string) error {
	for _, path := range paths {
		err := this.LoadPath(path)
		if err != nil {
			return err
		}
	}

	return nil
}

func (this *VectorIndex) UpdatePaths(paths []string, force bool) error {
	for _, path := range paths {
		err := this.UpdatePath(path, force)
		if err != nil {
			return err
		}
	}

	return nil
}

func (this *VectorIndex) UpdatePath(path string, force bool) error {
	path = filepath.Clean(path)
	fileInfo, err := os.Stat(path)
	if err != nil {
		return err
	}

	var toUpdate []string
	var stats []os.FileInfo
	var dirPath string

	if !fileInfo.IsDir() {
		// if the path is a specific file then we only update that file
		dirPath = filepath.Dir(path)
		toUpdate = []string{path}
		stats = []os.FileInfo{fileInfo}
	} else {
		// if the path is a directory then we add all files to update list
		dirPath = path
		toUpdate = []string{}

		err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() {
				toUpdate = append(toUpdate, p)
				stats = append(stats, info)
			} else {
				// recursively update subdirectories
				this.UpdatePath(p, force)
			}
			return nil
		})
		if err != nil {
			return nil
		}
	}

	dirIndex, ok := this.index[dirPath]
	if !ok {
		dirIndex = &pb.DirectoryIndex{
			Files: make(map[string]*pb.FileEmbeddings),
		}
		this.index[dirPath] = dirIndex
	}

	// Unless we're force-updating we only update files that have changed since
	// the last indexing
	if !force {
		confirmed := []string{}

		for i, path := range toUpdate {
			fileIndex, ok := dirIndex.Files[path]
			if ok {
				// if we found an index
				if fileIndex.UpdatedAt.AsTime().Unix() >= stats[i].ModTime().Unix() {
					// if the file has been updated since the last mod time we ignore
					continue
				}
				// otherwise fall through to line below
			}
			// didn't find already indexed file, add it to confirmed list
			confirmed = append(confirmed, path)
		}

		toUpdate = confirmed
	}

	// Update the index for each file
	for _, path := range toUpdate {
		timestamp := time.Now()
		embeddings, err := this.embedder.EmbedFile(path)
		if err != nil {
			return err
		}

		protoEmbeddings := AnnotatedVectorsInternalToProto(embeddings)
		fileEmbeddings := &pb.FileEmbeddings{
			Path:       path,
			UpdatedAt:  timestamppb.New(timestamp),
			Embeddings: protoEmbeddings,
		}

		dirIndex.Files[path] = fileEmbeddings
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
