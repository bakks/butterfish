package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pb "github.com/bakks/butterfish/proto"
	"github.com/drewlanenga/govector"
	"github.com/golang/protobuf/proto"
	"golang.org/x/tools/godoc/util"
	"golang.org/x/tools/godoc/vfs"
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
	EmbedFile(ctx context.Context, path string) ([]*AnnotatedVector, error)
}

type VectorIndex struct {
	index     map[string]*pb.DirectoryIndex
	embedder  Embedder
	out       io.Writer
	verbosity int
}

func NewVectorIndex() *VectorIndex {
	return &VectorIndex{
		index: make(map[string]*pb.DirectoryIndex),
		out:   os.Stdout,
	}
}

func (this *VectorIndex) SetEmbedder(embedder Embedder) {
	this.embedder = embedder
}

func (this *VectorIndex) SetOutput(out io.Writer) {
	this.out = out
	this.verbosity = 2
}

func (this *VectorIndex) SetVerbosity(verbosity int) {
	this.verbosity = verbosity
}

func (this *VectorIndex) Search(query []float64, k int) ([]*ScoredEmbedding, error) {
	queryVector, err := govector.AsVector(query)
	if err != nil {
		return nil, err
	}
	return this.SearchWithVector(&queryVector, k)
}

// Super naive vector search operation.
// - First we brute force search by iterating over all stored vectors
//     and calculating cosine distance
// - Next we sort based on score
func (this *VectorIndex) SearchWithVector(query *govector.Vector, k int) ([]*ScoredEmbedding, error) {
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

	if this.verbosity >= 2 {
		fmt.Fprintf(this.out, "VectorIndex.LoadDotfile(%s)\n", dotfile)
	}

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

	// put the loaded info in the memory index
	this.index[dotfile] = &dirIndex

	if this.verbosity >= 1 {
		fmt.Fprintf(this.out, "Loaded index cache at %s\n", dotfile)
	}
	return nil
}

const dotfileName = ".butterfish_index"

func (this *VectorIndex) SavePaths(paths []string) error {
	for _, path := range paths {
		err := this.SavePath(path)
		if err != nil {
			return err
		}
	}
	return nil
}

func (this *VectorIndex) SavePath(path string) error {
	if this.verbosity >= 2 {
		fmt.Fprintf(this.out, "VectorIndex.SavePath(%s)\n", path)
	}

	path = filepath.Clean(path)

	// Marshal the index into a buffer, i.e. serialize in-memory protobuf
	// to the byte representation
	dirIndex, ok := this.index[path]
	if !ok {
		return fmt.Errorf("No index found for %s", path)
	}

	buf, err := proto.Marshal(dirIndex)
	if err != nil {
		return err
	}

	dotfilePath := filepath.Join(path, dotfileName)

	// Write the buffer to the dotfile
	err = ioutil.WriteFile(dotfilePath, buf, 0644)
	if err != nil {
		return err
	}

	if this.verbosity >= 1 {
		fmt.Fprintf(this.out, "Saved index cache to %s\n", dotfilePath)
	}
	return nil
}

func (this *VectorIndex) LoadPath(ctx context.Context, path string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if this.verbosity >= 2 {
		fmt.Fprintf(this.out, "VectorIndex.LoadPath(%s)\n", path)
	}

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
	dotfilePath := filepath.Join(dirPath, dotfileName)
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
	err = filepath.Walk(dirPath, func(childPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// filepath.Walk will call this function with the root path
		if childPath == path {
			return nil
		}

		if info.IsDir() {
			return this.LoadPath(ctx, childPath)
		}
		return nil
	})

	return nil
}

func (this *VectorIndex) LoadPaths(ctx context.Context, paths []string) error {
	for _, path := range paths {
		err := this.LoadPath(ctx, path)
		if err != nil {
			return err
		}
	}

	return nil
}

func (this *VectorIndex) UpdatePaths(ctx context.Context, paths []string, force bool) error {
	for _, path := range paths {
		err := this.UpdatePath(ctx, path, force)
		if err != nil {
			return err
		}
	}

	return nil
}

func (this *VectorIndex) ShouldFilterPath(path string) bool {
	// Ignore dotfiles/hidden files
	if filepath.Base(path)[0] == '.' {
		return true
	}

	// Ignore files that are not text based on file name
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if !strings.HasPrefix(mimeType, "text/") {
		return true
	}

	// Ignore files that are not text based on a content check
	if !util.IsTextFile(vfs.OS("/"), path) {
		return true
	}

	return false
}

func (this *VectorIndex) Clear(path string) {
	if path == "" {
		this.index = make(map[string]*pb.DirectoryIndex)
	} else {
		path = filepath.Clean(path)
		delete(this.index, path)
	}
}

func (this *VectorIndex) ShowIndexed() []string {
	var paths []string
	for path := range this.index {
		paths = append(paths, path)
	}
	return paths
}

// Force means that we will re-index the file even if the target file hasn't
// changed since the last index
func (this *VectorIndex) UpdatePath(ctx context.Context, path string, force bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if this.verbosity >= 2 {
		fmt.Fprintf(this.out, "VectorIndex.UpdatePath(%s)\n", path)
	}

	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}

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

		err := filepath.Walk(path, func(childPath string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if path == childPath {
				return nil
			}

			// Skip filtered files
			if this.ShouldFilterPath(childPath) {
				if this.verbosity >= 2 {
					fmt.Fprintf(this.out, "Filtering path %s from update\n", childPath)
				}
				return nil
			}

			if !info.IsDir() {
				// if this is just a file then add it to the update list
				toUpdate = append(toUpdate, childPath)
				stats = append(stats, info)
			} else {
				// recursively update subdirectories
				this.UpdatePath(ctx, childPath, force)
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
					if this.verbosity >= 1 {
						fmt.Fprintf(this.out, "Skipping %s, no changes since last update\n", path)
					}
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
		embeddings, err := this.embedFile(ctx, path)
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

	this.SavePath(dirPath)

	return nil
}

func (this *VectorIndex) embedFile(ctx context.Context, path string) ([]*AnnotatedVector, error) {
	if this.embedder == nil {
		return nil, fmt.Errorf("No embedder set")
	}
	if this.verbosity >= 1 {
		fmt.Fprintf(this.out, "Embedding %s\n", path)
	}
	return this.embedder.EmbedFile(ctx, path)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
