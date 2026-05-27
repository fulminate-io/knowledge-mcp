// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot returns the repository root by walking up from the test file.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

func TestChunkFulminateRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	root := repoRoot(t)
	chunker := NewChunker()
	defer chunker.Close()

	var (
		goFiles   []string
		tsFiles   []string
		totalTime time.Duration
	)

	// Collect Go files.
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			t.Logf("skipping inaccessible path: %s: %v", path, walkErr)
			return nil // intentionally skip walk errors
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == "node_modules" || base == ".git" || base == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go":
			goFiles = append(goFiles, path)
		case ".ts", ".tsx":
			tsFiles = append(tsFiles, path)
		}
		return nil
	})
	require.NoError(t, err)

	t.Logf("Found %d Go files, %d TS/TSX files", len(goFiles), len(tsFiles))
	assert.Greater(t, len(goFiles), 100, "expected >100 Go files, got %d", len(goFiles))

	var (
		totalChunks int
		totalEdges  int
		errors      int
	)

	allFiles := append(goFiles, tsFiles...)

	for _, path := range allFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			errors++
			continue
		}

		start := time.Now()
		result, err := chunker.ChunkFile(context.Background(), path, src)
		totalTime += time.Since(start)

		if err != nil {
			errors++
			continue
		}

		for _, chunk := range result.Chunks {
			assert.NotEmpty(t, chunk.Content, "chunk in %s has empty content", path)
			assert.Positive(t, chunk.StartLine, "chunk in %s has invalid start line", path)
			assert.GreaterOrEqual(t, chunk.EndLine, chunk.StartLine, "chunk in %s has end < start", path)
		}

		totalChunks += len(result.Chunks)
		totalEdges += len(result.Edges)
	}

	t.Logf("Processed %d files in %v", len(allFiles), totalTime)
	t.Logf("Total chunks: %d, Total edges: %d, Errors: %d", totalChunks, totalEdges, errors)

	// Performance: should process all files reasonably fast.
	filesPerSec := float64(len(allFiles)) / totalTime.Seconds()
	t.Logf("Performance: %.0f files/sec", filesPerSec)

	// Should have extracted meaningful data.
	assert.Greater(t, totalChunks, 1000, "expected >1000 chunks, got %d", totalChunks)
	assert.Greater(t, totalEdges, 1000, "expected >1000 edges, got %d", totalEdges)
	assert.Equal(t, 0, errors, "no files should cause errors")
}

func TestChunkQualitySpot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	root := repoRoot(t)
	chunker := NewChunker()
	defer chunker.Close()

	t.Run("GoFile_MemoryWriter", func(t *testing.T) {
		path := filepath.Join(root, "core", "domains", "memory", "writer.go")
		src, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("file not found: %s", path)
		}

		result, err := chunker.ChunkFile(context.Background(), path, src)
		require.NoError(t, err)

		// Should find CreateMemory — either as a named method chunk or as a split
		// chunk containing the signature (large methods get split at AST boundaries).
		found := false
		for _, chunk := range result.Chunks {
			if chunk.Name == "CreateMemory" || strings.Contains(chunk.Content, "func") && strings.Contains(chunk.Content, "CreateMemory") {
				found = true
				break
			}
		}
		assert.True(t, found, "should find CreateMemory chunk (named or split) in writer.go")

		// Should have CALLS edges.
		callEdges := filterEdges(result.Edges, EdgeCalls)
		assert.NotEmpty(t, callEdges, "should have CALLS edges in writer.go")

		// Should have CONTAINS edges.
		containsEdges := filterEdges(result.Edges, EdgeContains)
		assert.NotEmpty(t, containsEdges, "should have CONTAINS edges in writer.go")

		t.Logf("writer.go: %d chunks, %d edges", len(result.Chunks), len(result.Edges))
		for _, chunk := range result.Chunks {
			t.Logf("  [%s] %s (lines %d-%d)", chunk.ChunkType, chunk.Name, chunk.StartLine, chunk.EndLine)
		}
	})
}

func BenchmarkChunkFile(b *testing.B) {
	root := repoRoot(b)

	// Read a representative Go file.
	path := filepath.Join(root, "core", "domains", "memory", "writer.go")
	src, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("file not found: %s", path)
	}

	chunker := NewChunker()
	defer chunker.Close()

	for b.Loop() {
		_, err := chunker.ChunkFile(context.Background(), path, src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
