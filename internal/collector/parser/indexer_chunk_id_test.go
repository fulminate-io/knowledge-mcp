// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// indexer_chunk_id_test.go — the UNNAMED-chunk node id. The named branches are
// not this file's subject; every fixture here leaves Name empty on purpose,
// because Name is the single field that selects the branch under test AND the
// field whose emptiness keeps these nodes out of the NAMED branch of
// ChunkNodeID, where they would acquire a symbol name.

// unnamedChunk builds an unnamed chunk with the given content and line range.
// PathHash is deliberately EMPTY: the pre-existing collision mechanism is gated
// on it being non-empty, so leaving it empty keeps these tests measuring the id
// scheme rather than that fallback.
func unnamedChunk(path, content string, startLine, endLine int) treesitter.Chunk {
	return treesitter.Chunk{
		Content:   content,
		FilePath:  path,
		Language:  "go",
		ChunkType: "block",
		StartLine: startLine,
		EndLine:   endLine,
	}
}

// TestChunkNodeID_StableAcrossLineShift is the red-first case and the defect in
// one assertion: an unrelated edit ABOVE an unnamed chunk moves its line range,
// and the chunk's node id must NOT move with it.
//
// WHY IT IS THE WHOLE TICKET IN MINIATURE. A position-derived id turns every edit
// above a chunk into a new node id, so the server lands a new row and the old one
// is orphaned — on every collect, forever, for every file with unnamed chunks.
func TestChunkNodeID_StableAcrossLineShift(t *testing.T) {
	const body = "{\n\tdoSomething()\n}"
	before := ChunkNodeID(unnamedChunk("pkg/a.go", body, 10, 12))
	after := ChunkNodeID(unnamedChunk("pkg/a.go", body, 42, 44))

	assert.Equal(t, before, after,
		"an unnamed chunk's id must be derived from WHAT IT IS, not where it sits: "+
			"an edit above it moved the line range and the id followed, which is the orphan defect")
	assert.Contains(t, before, "pkg/a.go",
		"the id must still be file-scoped, or the per-file reclaim cannot key on it")
}

// TestChunkNodeID_IdenticalChunksStayDistinct pins the ORDINAL. Two byte-identical
// unnamed chunks in one file are ordinary — repeated literals, generated blocks —
// and a pure content digest gives them ONE id, so the second silently overwrites
// the first and the file loses a node.
//
// THE FIXTURE CARRIES NO PathHash, which is what makes this test about the new
// mechanism: DeduplicateChunks' pre-existing collision path is gated on a non-empty
// PathHash, so it cannot rescue this case.
//
// THE ORDINAL MUST NOT ARRIVE VIA Name. Appending to an empty Name would move the
// chunk to the NAMED branch of ChunkNodeID and give its node a non-empty
// SymbolName — which is exactly the condition that pulls it back into
// reindex.go's decl-key binder, collapsing the blast-radius bound this change
// rests on. So the test asserts the Name stays empty as well as the ids differing.
func TestChunkNodeID_IdenticalChunksStayDistinct(t *testing.T) {
	const body = "{\n\treturn 0\n}"
	result := &treesitter.Result{
		FilePath: "pkg/a.go",
		Chunks: []treesitter.Chunk{
			unnamedChunk("pkg/a.go", body, 10, 12),
			unnamedChunk("pkg/a.go", body, 30, 32),
		},
	}
	// CONTROL: byte-identical unnamed chunks collide BEFORE disambiguation. Without
	// this the test would pass against a scheme that never needed an ordinal at all.
	require.Equal(t, ChunkNodeID(result.Chunks[0]), ChunkNodeID(result.Chunks[1]),
		"fixture control: two byte-identical unnamed chunks must start out sharing one id")

	DeduplicateChunks([]*treesitter.Result{result})

	first, second := ChunkNodeID(result.Chunks[0]), ChunkNodeID(result.Chunks[1])
	assert.NotEqual(t, first, second,
		"two byte-identical unnamed chunks in one file must keep DISTINCT ids, "+
			"or the second overwrites the first and the file loses a node")
	assert.Empty(t, result.Chunks[0].Name, "disambiguation must not name an unnamed chunk")
	assert.Empty(t, result.Chunks[1].Name,
		"disambiguation must not name an unnamed chunk: a non-empty Name gives the node a "+
			"SymbolName and pulls it into the decl-key binder this change is bounded against")
}

// TestChunkNodeID_DifferentsNeverCollide is the KNOWN-NEGATIVE. A scheme can be
// wrong in the other direction — collapsing genuinely different chunks onto one
// id — and a positive-only pair of tests would not see it.
func TestChunkNodeID_DifferentsNeverCollide(t *testing.T) {
	chunks := []treesitter.Chunk{
		unnamedChunk("pkg/a.go", "{\n\treturn 0\n}", 10, 12),
		unnamedChunk("pkg/a.go", "{\n\treturn 1\n}", 10, 12),
		unnamedChunk("pkg/a.go", "{\n\treturn 0\n}\n", 10, 13),
		unnamedChunk("pkg/b.go", "{\n\treturn 0\n}", 10, 12),
	}
	seen := make(map[string]int, len(chunks))
	for i := range chunks {
		id := ChunkNodeID(chunks[i])
		if prev, dup := seen[id]; dup {
			t.Errorf("chunks %d and %d collided onto one id %q — different chunks must never share a node id",
				prev, i, id)
			continue
		}
		seen[id] = i
	}
	assert.Len(t, seen, len(chunks), "every distinct chunk must own a distinct id")
}
