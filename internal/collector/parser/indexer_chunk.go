// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// withChunkRecover wraps fn with a deferred recover so a tree-sitter panic
// (CGO segfault on malformed source, internal grammar bug, etc.) is logged
// instead of crashing the whole knowledge-server process.
func withChunkRecover(site string, fn func()) func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("parser goroutine panic",
					"site", site,
					"err", r,
					"stack", string(debug.Stack()))
			}
		}()
		fn()
	}
}

// ChunkFiles uses tree-sitter to chunk all discovered files.
func ChunkFiles(ctx context.Context, repoDir string, files []string) ([]*treesitter.Result, error) {
	chunker := treesitter.NewChunker()
	defer chunker.Close()

	var results []*treesitter.Result
	for _, relPath := range files {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		absPath := filepath.Join(repoDir, relPath)
		src, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("failed to read file", "path", relPath, "error", err)
			continue
		}
		result, err := chunker.ChunkFile(ctx, relPath, src)
		if err != nil {
			slog.Warn("failed to chunk file", "path", relPath, "error", err)
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

// maxChunkWorkers caps the chunker fan-out. Each worker keeps a parsed
// tree + the file's source bytes live while it works, so on a many-core
// box an uncapped NumCPU fan-out multiplies the chunker's in-flight memory
// for little throughput gain (the work is short and I/O-bound per file).
// 8 keeps the parallel speedup while bounding the footprint.
const maxChunkWorkers = 8

// ChunkFilesParallel uses tree-sitter to chunk files with up to
// maxChunkWorkers parallel workers. Each worker has its own Chunker since
// the tree-sitter parser is not thread-safe.
func ChunkFilesParallel(ctx context.Context, repoDir string, files []string) ([]*treesitter.Result, error) {
	workers := min(runtime.NumCPU(), len(files), maxChunkWorkers)
	if workers <= 1 {
		return ChunkFiles(ctx, repoDir, files)
	}

	fileCh := make(chan string, workers)
	go withChunkRecover("ChunkFilesParallel.feeder", func() {
		for _, f := range files {
			select {
			case fileCh <- f:
			case <-ctx.Done():
				close(fileCh)
				return
			}
		}
		close(fileCh)
	})()

	var mu sync.Mutex
	var results []*treesitter.Result
	var wg sync.WaitGroup

	for range workers {
		wg.Go(withChunkRecover("ChunkFilesParallel.worker", func() {
			chunker := treesitter.NewChunker()
			defer chunker.Close()

			for relPath := range fileCh {
				if ctx.Err() != nil {
					return
				}
				absPath := filepath.Join(repoDir, relPath)
				src, err := os.ReadFile(absPath)
				if err != nil {
					slog.Warn("failed to read file", "path", relPath, "error", err)
					continue
				}
				result, err := chunker.ChunkFile(ctx, relPath, src)
				if err != nil {
					slog.Warn("failed to chunk file", "path", relPath, "error", err)
					continue
				}
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}
		}))
	}

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return results, nil
}

// ChunkNodeID creates a unique node ID for a chunk.
func ChunkNodeID(chunk treesitter.Chunk) string {
	if chunk.Name != "" {
		if chunk.ParentName != "" {
			return fmt.Sprintf("%s:%s.%s", chunk.FilePath, chunk.ParentName, chunk.Name)
		}
		return fmt.Sprintf("%s:%s", chunk.FilePath, chunk.Name)
	}
	return fmt.Sprintf("%s:L%d-%d", chunk.FilePath, chunk.StartLine, chunk.EndLine)
}

// DeduplicateChunks detects node ID collisions within each file's chunks and
// appends the PathHash to disambiguate. Chunks with unique IDs are unchanged.
func DeduplicateChunks(results []*treesitter.Result) {
	for _, result := range results {
		// Count occurrences of each ID.
		idCount := make(map[string]int, len(result.Chunks))
		for i := range result.Chunks {
			id := ChunkNodeID(result.Chunks[i])
			idCount[id]++
		}

		// For any ID that appears more than once, append #PathHash to all
		// instances so every node gets a unique ID.
		for i := range result.Chunks {
			id := ChunkNodeID(result.Chunks[i])
			if idCount[id] > 1 && result.Chunks[i].PathHash != "" {
				result.Chunks[i].Name += "#" + result.Chunks[i].PathHash
			}
		}
	}
}

// ConvertEdges converts treesitter edges to typed wire edges, propagating the
// per-edge Weight signal so weighted analyzers (e.g. pagerank_weighted) can
// see the tree-sitter call counts.
func ConvertEdges(tsEdges []treesitter.Edge) []*knowledgev1.Edge {
	edges := make([]*knowledgev1.Edge, len(tsEdges))
	for i := range tsEdges {
		e := &tsEdges[i]
		edges[i] = &knowledgev1.Edge{
			FromId: e.FromID,
			ToId:   e.ToID,
			Type:   string(e.Type),
			Weight: e.Weight,
		}
	}
	return edges
}
