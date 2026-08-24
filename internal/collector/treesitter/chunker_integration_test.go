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

	t.Run("GoFile_ChunkerIdentity", func(t *testing.T) {
		// THIS TEST WAS DEAD AND SILENTLY SO, exactly like BenchmarkChunkFile
		// below and for the same reason: it read core/domains/memory/writer.go,
		// a pre-migration path that exists nowhere in this repository, so every
		// run hit a Skipf while `go test` still exited 0. A quality spot-check
		// that silently checks nothing is worse than no spot-check, because it
		// reads as coverage. It now reads a real, declaration-dense file inside
		// this module, and a miss is FATAL rather than a skip.
		path := filepath.Join(root, "internal", "collector", "treesitter", "chunker_identity.go")
		src, err := os.ReadFile(path)
		require.NoErrorf(t, err, "spot-check input not found: %s", path)

		result, err := chunker.ChunkFile(context.Background(), path, src)
		require.NoError(t, err)

		// Should find containerName — either as a named chunk or as a split
		// chunk containing the signature (large declarations get split at AST
		// boundaries).
		found := false
		for _, chunk := range result.Chunks {
			if chunk.Name == "containerName" || strings.Contains(chunk.Content, "func") && strings.Contains(chunk.Content, "containerName") {
				found = true
				break
			}
		}
		assert.True(t, found, "should find containerName chunk (named or split) in chunker_identity.go")

		// Should have CALLS edges.
		callEdges := filterEdges(result.Edges, EdgeCalls)
		assert.NotEmpty(t, callEdges, "should have CALLS edges in chunker_identity.go")

		// Should have CONTAINS edges.
		containsEdges := filterEdges(result.Edges, EdgeContains)
		assert.NotEmpty(t, containsEdges, "should have CONTAINS edges in chunker_identity.go")

		t.Logf("chunker_identity.go: %d chunks, %d edges", len(result.Chunks), len(result.Edges))
		for _, chunk := range result.Chunks {
			t.Logf("  [%s] %s (lines %d-%d)", chunk.ChunkType, chunk.Name, chunk.StartLine, chunk.EndLine)
		}
	})
}

func BenchmarkChunkFile(b *testing.B) {
	root := repoRoot(b)

	// A representative, declaration-dense Go file that lives INSIDE this
	// module, so repoRoot's walk to the nearest go.mod — which lands on
	// cmd/knowledge — actually reaches it.
	//
	// THIS BENCHMARK WAS DEAD AND SILENTLY SO. It previously read
	// core/domains/memory/writer.go, a pre-migration path that exists nowhere
	// in this repository, so every run hit the skip below while `go test` still
	// exited 0 — which makes any perf guard built on it permanently vacuous.
	// The skip is now a FATAL for exactly that reason: a benchmark that quietly
	// measures nothing is the defect, not a tolerable degradation.
	path := filepath.Join(root, "internal", "collector", "treesitter", "chunker_identity.go")
	src, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("benchmark input not found: %s: %v", path, err)
	}

	// BOTH SIDES ARE MEASURED IN ONE INVOCATION, and that is the whole point of
	// the sub-benchmark shape. The chunk-time guard previously compared two
	// files in /tmp that the criterion READ but never PRODUCED, so a leftover
	// pair from an earlier attempt scored as the current run — and the two
	// sides could be captured from different trees without anything noticing.
	// Regenerating both here makes the comparison self-contained: there is no
	// window in which a stale artifact can be read as a fresh measurement.
	b.Run("arm_off", func(b *testing.B) {
		UnregisterQualifierTypes(LangGo)
		UnregisterTypeFacts(LangGo)
		// RESTORE THE PRODUCTION ARMS. An arm left unregistered would silently
		// disarm the feature for every later test in this binary — the hazard
		// UnregisterBindsResolver documents.
		b.Cleanup(func() {
			RegisterQualifierTypes(LangGo, goQualifierTypes)
			RegisterTypeFacts(LangGo, goTypeFacts)
		})
		// KNOWN-POSITIVE CONTROL: without this, an unregister that silently did
		// nothing would make both sides measure the SAME code, and the ratio
		// would sit at 1.0 forever while proving nothing at all.
		if _, ok := qualifierTypeResolvers[LangGo]; ok {
			b.Fatal("control: the Go qualifier-type arm is still registered, so this side is not the arm-off measurement")
		}
		if _, ok := typeFactsResolvers[LangGo]; ok {
			b.Fatal("control: the Go type-facts arm is still registered, so this side is not the arm-off measurement")
		}
		benchmarkChunkPath(b, path, src)
	})

	b.Run("arm_on", func(b *testing.B) {
		// The production registration, restored by the arm_off cleanup above.
		// BOTH registries are checked, mirroring arm_off exactly: arm_off turns
		// two arms off, so a one-registry check here would let a cleanup that
		// restored only the qualifier arm pass while this side silently
		// measured a half-armed chunker.
		if _, ok := qualifierTypeResolvers[LangGo]; !ok {
			b.Fatal("control: the Go qualifier-type arm is NOT registered, so this side is not the arm-on measurement")
		}
		if _, ok := typeFactsResolvers[LangGo]; !ok {
			b.Fatal("control: the Go type-facts arm is NOT registered, so this side is not the arm-on measurement")
		}
		benchmarkChunkPath(b, path, src)
	})
}

// benchmarkChunkPath is the measured loop both sides share, so the only
// difference between them is the registry state.
func benchmarkChunkPath(b *testing.B, path string, src []byte) {
	b.Helper()
	chunker := NewChunker()
	defer chunker.Close()

	for b.Loop() {
		_, err := chunker.ChunkFile(context.Background(), path, src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
