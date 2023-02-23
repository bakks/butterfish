# Butterfish Embedding Module

A goal of Butterfish is to make it easy to create and manage embeddings. Embeddings are a semantic vector representation of a block of text - they enable you to transform text into a convenient format such that they can be searched and compared. Butterfish's solution is to index local files using an embedding API, then cache the embedding vectors in the same directory for later searches and prompt injection. This module, however, can be used independently to manage embeddings on disk.

How are embeddings cached? When you index a file or a directory, a `.butterfish_index` cache file will be written to that directory. The cache files are binary files written using the protobuf schema in `../proto/butterfish.proto`.

The vector search algorithm is currently very naive, it's just a brute-force cosine similarity between the search vector and cached vectors.

### Example

See the Butterfish help for how to use this module through the CLI. If you'd like to use it directly in Go, an example is below:

```go
import "fmt"
import "github.com/bakks/butterfish/embedding"

func main() {
  // create an embedder which implements the embedding.Embedder interface
  embedder := ...

  // create the in-memory index
  index := embedding.NewDiskCachedEmbeddingIndex(embedder, out)

  // lets use the current directory as an index for now
  path := "."
  paths := []string{path}

  // load any existing cached embeddings
  ctx := context.Background()
  err := index.LoadPaths(ctx, paths)
  if err != nil {
    panic(err)
  }

  // index the current directory (recursively)
  force := false      // skip over cached embeddings
  chunkSize := 512    // size in bytes to split file into
  maxChunks := 128    // maximum number of chunks to embed per file
  err = index.IndexPaths(ctx, paths, force, chunkSize, maxChunks)
  if err != nil {
    panic(err)
  }

  // embed the search string and compare against cached embeddings, get 5 results
  numResults := 5
  results, err := index.Search(ctx, "This is the search string", numResults)
  if err != nil {
    panic(err)
  }

  // print the filenames, comparison scores (1 == exact match, 0 == orthogonal),
  // and the results themselves
  for _, result := range results {
    fmt.Printf("%s : %0.4f\n", result.FilePath, result.Score)
    fmt.Printf("%s\n", result.Content)
  }
}
```

The embedding module will call into an implementor of the `Embedding` interface, shown below. You can wire this into something that calls OpenAI (as implemented in Butterfish), or any other embedding service. Embedding length is flexible.

```go
type Embedder interface {
  CalculateEmbeddings(ctx context.Context, content []string) ([][]float64, error)
}
```

### Examining cache files directly

Cache files are written in binary format, but can be examined. If you check out this repo you can then inspect specific index files with a command like:

```bash
protoc --decode DirectoryIndex butterfish/proto/butterfish.proto < .butterfish_index
```
