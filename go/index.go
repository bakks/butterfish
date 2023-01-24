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
	"github.com/spf13/afero"
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
	AbsPath   string
	Embedding *pb.AnnotatedEmbedding
}

type Embedder interface {
	CalculateEmbeddings(ctx context.Context, content []string) ([][]float64, error)
}

type VectorIndex struct {
	// maps absolute path of directory to a directory index
	index     map[string]*pb.DirectoryIndex
	embedder  Embedder
	out       io.Writer
	verbosity int
	fs        afero.Fs
}

func NewVectorIndex() *VectorIndex {
	return &VectorIndex{
		index: make(map[string]*pb.DirectoryIndex),
		out:   os.Stdout,
		fs:    afero.NewOsFs(),
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

func (this *VectorIndex) Search(ctx context.Context, query string, numResults int) ([]*ScoredEmbedding, error) {
	results, err := this.embedder.CalculateEmbeddings(ctx, []string{query})
	if err != nil {
		return nil, err
	}

	return this.SearchWithVector(results[0], numResults)
}

// Super naive vector search operation.
// - First we brute force search by iterating over all stored vectors
//     and calculating cosine distance
// - Next we sort based on score
func (this *VectorIndex) SearchWithVector(queryVector []float64, k int) ([]*ScoredEmbedding, error) {
	query, err := govector.AsVector(queryVector)
	if err != nil {
		return nil, err
	}

	scored := []*ScoredEmbedding{}

	for dirIndexAbsPath, dirIndex := range this.index {
		for filename, fileIndex := range dirIndex.Files {
			for _, embedding := range fileIndex.Embeddings {
				govec, err := govector.AsVector(embedding.Vector)

				distance, err := govector.Cosine(query, govec)
				if err != nil {
					return nil, err
				}

				absPath := filepath.Join(dirIndexAbsPath, filename)
				scoredEmbedding := &ScoredEmbedding{distance, absPath, embedding}
				scored = append(scored, scoredEmbedding)
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
	file, err := this.fs.Open(dotfile)
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
	fileInfo, err := this.fs.Stat(path)
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
	_, err = this.fs.Stat(dotfilePath)
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

func (this *VectorIndex) IndexPaths(ctx context.Context, paths []string, force bool) error {
	for _, path := range paths {
		err := this.IndexPath(ctx, path, force)
		if err != nil {
			return err
		}
	}

	return nil
}

// This is a bit of glue to make afero filesystems work with the vfs interface
type vfsOpener struct {
	fs afero.Fs
}

func (this *vfsOpener) Open(path string) (vfs.ReadSeekCloser, error) {
	return this.fs.Open(path)
}

// Return true if this is a file we want to index/embed. We use several
// predicates to determine this.
// 1. The file must be a non-hidden file (i.e. not starting with a dot)
// 2. The file must not be a directory (handled separately)
// 3. The file must be text, not binary, checked by extension/mime-type and
//    by checking the first few bytes of the file if the extension check passes
// 4. The file must have been updated since the last indexing, unless forceUpdate is true
func (this *VectorIndex) IndexableFile(path string, file os.FileInfo, forceUpdate bool, previousEmbeddings *pb.FileEmbeddings) bool {
	// Ignore dotfiles/hidden files
	name := file.Name()
	if name[0] == '.' {
		return false
	}

	// Ignore files that are not text based on file name
	mimeType := mime.TypeByExtension(filepath.Ext(name))
	if mimeType != "" && !strings.HasPrefix(mimeType, "text/") {
		return false
	}

	// Ignore files that are not text based on a content check
	opener := &vfsOpener{this.fs}
	if !util.IsTextFile(opener, filepath.Join(path, name)) {
		return false
	}

	if !forceUpdate && previousEmbeddings != nil {
		// Ignore files that have not changed since the last indexing
		if previousEmbeddings.UpdatedAt.AsTime().Unix() >= file.ModTime().Unix() {
			return false
		}
	}

	return true
}

func (this *VectorIndex) FilterUnindexablefiles(path string, files []os.FileInfo, forceUpdate bool, dirIndex *pb.DirectoryIndex) []os.FileInfo {
	var filteredFiles []os.FileInfo
	for _, file := range files {
		previousEmbeddings := dirIndex.Files[file.Name()]
		if this.IndexableFile(path, file, forceUpdate, previousEmbeddings) {
			filteredFiles = append(filteredFiles, file)
		}
	}
	return filteredFiles
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

func NewDirectoryIndex() *pb.DirectoryIndex {
	return &pb.DirectoryIndex{
		Files: make(map[string]*pb.FileEmbeddings),
	}
}

// Force means that we will re-index the file even if the target file hasn't
// changed since the last index
func (this *VectorIndex) IndexPath(ctx context.Context, path string, forceUpdate bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if this.verbosity >= 2 {
		fmt.Fprintf(this.out, "VectorIndex.IndexPath(%s)\n", path)
	}

	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	fileInfo, err := this.fs.Stat(path)
	if err != nil {
		return err
	}

	var files []os.FileInfo
	var dirPath string

	if !fileInfo.IsDir() {
		// if the path is a specific file then we only update that file
		dirPath = filepath.Dir(path)
		files = []os.FileInfo{fileInfo}
	} else {
		// if the path is a directory then we add all files to update list
		dirPath = path

		// call UpdatePath recursively for each subdirectory
		err = forEachSubdir(this.fs, path, func(path string) error {
			return this.IndexPath(ctx, path, forceUpdate)
		})

		// get each non-directory file and stat in the path
		files, err = afero.ReadDir(this.fs, path)
		if err != nil {
			return nil
		}
	}

	// Fetch directory index, create a new one if none found
	dirIndex, ok := this.index[dirPath]
	if !ok {
		dirIndex = NewDirectoryIndex()
		this.index[dirPath] = dirIndex
	}

	files = this.FilterUnindexablefiles(dirPath, files, forceUpdate, dirIndex)

	// Update the index for each file
	for _, file := range files {
		name := file.Name()
		path := filepath.Join(dirPath, file.Name())
		fileEmbeddings, err := this.EmbedFile(ctx, path)
		if err != nil {
			return err
		}

		dirIndex.Files[name] = fileEmbeddings
	}

	// TODO remove files that have been deleted

	if len(dirIndex.Files) > 0 {
		this.SavePath(dirPath)
	}

	return nil
}

func (this *VectorIndex) EmbedFile(ctx context.Context, path string) (*pb.FileEmbeddings, error) {
	timestamp := time.Now()
	embeddings, err := this.GetEmbeddedVectors(ctx, path)
	if err != nil {
		return nil, err
	}

	protoEmbeddings := AnnotatedVectorsInternalToProto(embeddings)
	fileEmbeddings := &pb.FileEmbeddings{
		Path:       path,
		UpdatedAt:  timestamppb.New(timestamp),
		Embeddings: protoEmbeddings,
	}

	return fileEmbeddings, nil
}

// EmbedFile takes a path to a file, splits the file into chunks, and calls
// the embedding API for each chunk
func (this *VectorIndex) GetEmbeddedVectors(ctx context.Context, path string) ([]*AnnotatedVector, error) {
	if this.embedder == nil {
		return nil, fmt.Errorf("No embedder set")
	}
	if this.verbosity >= 1 {
		fmt.Fprintf(this.out, "Embedding %s\n", path)
	}

	const chunkSize uint64 = 768
	const chunksPerCall = 8
	const maxChunks = 8 * 128

	annotatedVectors := []*AnnotatedVector{}

	// first we chunk the file
	chunks, err := getFileChunks(ctx, this.fs, path, chunkSize, maxChunks)
	if err != nil {
		return nil, err
	}
	stringChunks := byteToString(chunks)

	// then we call the embedding API for each block of chunks
	for i := 0; i < len(chunks); i += chunksPerCall {
		// check if we should bail out
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		callChunks := stringChunks[i:min(i+chunksPerCall, len(chunks))]
		newEmbeddings, err := this.embedder.CalculateEmbeddings(ctx, callChunks)
		if err != nil {
			return nil, err
		}

		// iterate through response, create an annotation, and create an annotated vector
		for j, embedding := range newEmbeddings {
			rangeStart := uint64(i+j) * chunkSize
			rangeEnd := rangeStart + uint64(len(callChunks[j]))
			absPath, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}

			av := NewAnnotatedVector(absPath, rangeStart, rangeEnd, embedding)
			annotatedVectors = append(annotatedVectors, av)
		}
	}

	return annotatedVectors, nil
}
