# Butterfish Embedding Module

A goal of Butterfish is to make it easy to create and manage embeddings. Embeddings are a semantic vector representation of a block of text - they enable you to transform text into a convenient format such that they can be searched and compared. Butterfish's solution is to index local files using an embedding API, then cache the embedding vectors in the same directory for later searches and prompt injection. This module, however, can be used independently to manage embeddings on disk.

How are embeddings cached? When you index a file or a directory, a `.butterfish_index` cache file will be written to that directory. The cache files are binary files written using the protobuf schema in `../proto/butterfish.proto`.

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

  // index the current directory (recursively), but skip over cached embeddings
  force := false
  err = index.IndexPaths(ctx, paths, force)
  if err != nil {
    panic(err)
  }

  numResults := 5
  results, err := index.Search(ctx, "This is the search string", numResults)
  if err != nil {
    panic(err)
  }

  for _, result := range results {
    fmt.Printf(, "%s : %0.4f\n", result.FilePath, result.Score)
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
