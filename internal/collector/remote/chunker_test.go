// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBatchNodes_RespectsSizeBoundAndOrder(t *testing.T) {
	// Each node serializes to ~50 bytes; maxBytes=200 must produce multiple
	// chunks, each (bar a lone oversized node) under the bound.
	//
	// EVERY NODE CARRIES ITS OWN DISTINCT FILE PATH. BatchNodes packs whole file
	// groups, so a fixture of file-less nodes would be ONE group that lands in a
	// single chunk however small the bound — the multi-chunk expectation below
	// would then be asserting nothing. Distinct paths make each node its own
	// group, which is what keeps this test about the BYTE BOUND and the ORDER.
	nodes := make([]*knowledgev1.Node, 10)
	for i := range nodes {
		nodes[i] = &knowledgev1.Node{
			Id:         string(rune('a'+i)) + "-node",
			Type:       string(kgtypes.NodeFile),
			FilePath:   "pkg/" + string(rune('a'+i)) + ".go",
			SymbolName: string(rune('a' + i)),
			Content:    "the content goes here for batching",
		}
	}
	const maxBytes = 200
	chunks := BatchNodes(nodes, maxBytes)
	require.GreaterOrEqual(t, len(chunks), 2, "10 nodes at a 200-byte bound must produce multiple chunks")

	// Order is preserved across the flattened chunks, and every multi-node
	// chunk stays under the byte bound.
	var flattened []*knowledgev1.Node
	for _, chunk := range chunks {
		var chunkBytes int
		for _, n := range chunk {
			chunkBytes += proto.Size(n) + 16
		}
		if len(chunk) > 1 {
			assert.LessOrEqual(t, chunkBytes, maxBytes, "multi-node chunk must stay under the byte bound")
		}
		flattened = append(flattened, chunk...)
	}
	require.Len(t, flattened, len(nodes))
	for i := range nodes {
		assert.Equal(t, nodes[i].Id, flattened[i].Id, "node order must be preserved across chunks")
	}
}

func TestBatchNodes_DefaultWhenMaxBytesZero(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "x", SymbolName: "x.go"},
	}
	chunks := BatchNodes(nodes, 0)
	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 1)
	assert.Equal(t, "x", chunks[0][0].Id)
}

// TestBatchNodes_EachFileInExactlyOneChunk pins the invariant the server's
// per-chunk node reclaim depends on: every owning file appears in EXACTLY ONE
// chunk. It is the node-side twin of TestBatchEdgesProto_EachSourceInExactlyOneChunk
// below and mirrors it part for part, because each of that test's parts stops a
// distinct vacuity.
//
// THE PROPERTY IS "EXACTLY ONE CHUNK", NOT "A RUN IS NEVER SPLIT". Production
// node order is not grouped by file to begin with: the parser appends a file-less
// language node per chunk loop (ensureLangNode) and roughly 150 more file-less
// nodes arrive at the tail, so a fixture whose files were already contiguous
// would pass against a chunker that does no grouping at all.
//
// WHY IT IS LOAD-BEARING: the server reclaims an uploaded file's live nodes that
// the chunk did not carry. If a file spanned two chunks, the second chunk's
// reclaim would compute the first chunk's freshly-landed nodes as uncarried and
// delete them.
func TestBatchNodes_EachFileInExactlyOneChunk(t *testing.T) {
	node := func(id, path string) *knowledgev1.Node {
		return &knowledgev1.Node{
			Id: id, Type: string(kgtypes.NodeFile), FilePath: path,
			SymbolName: id, Content: "content payload sized to force several chunks",
		}
	}
	files := []string{"pkg/a.go", "pkg/b.go", "pkg/c.go", "pkg/d.go", "pkg/e.go"}

	// PRODUCTION-SHAPED INPUT: region 1 is one symbol per file across the whole
	// repository with a FILE-LESS language node appended mid-walk, region 2 is
	// every later symbol appended after all of them, and the file-less hierarchy
	// nodes land at the tail. No file's nodes are contiguous.
	var nodes []*knowledgev1.Node
	for i, f := range files {
		nodes = append(nodes, node(f+":Sym0", f))
		if i == 2 {
			nodes = append(nodes, node("lang:go", ""))
		}
	}
	for round := range 3 {
		for _, f := range files {
			nodes = append(nodes, node(f+":Sym"+strconv.Itoa(round+1), f))
		}
	}
	nodes = append(nodes, node("pkg", ""), node("repo-root", ""))

	// FIXTURE CONTROL: assert the input really is non-contiguous and really does
	// interleave file-less nodes both mid-slice and at the tail, so a later edit
	// that "tidies" the fixture into grouped order cannot silently make this test
	// vacuous.
	require.NotEqual(t, nodes[0].GetFilePath(), nodes[1].GetFilePath(),
		"fixture must be production-shaped: a file's nodes must NOT be contiguous in the input")
	require.Empty(t, nodes[3].GetFilePath(),
		"a file-less node must sit MID-SLICE, the shape ensureLangNode produces")
	require.Empty(t, nodes[len(nodes)-1].GetFilePath(),
		"file-less nodes must also sit at the TAIL, the shape the hierarchy append produces")

	const maxBytes = 600
	chunks := BatchNodes(nodes, maxBytes)
	require.GreaterOrEqual(t, len(chunks), 2,
		"the fixture must span several chunks, or the invariant is untested")

	// THE FILE-LESS NODES ARE THEIR OWN GROUP and must not be split either, so
	// they are keyed here under the empty path rather than skipped.
	chunkOf := map[string]int{}
	for ci, chunk := range chunks {
		seenHere := map[string]bool{}
		for _, n := range chunk {
			p := n.GetFilePath()
			if seenHere[p] {
				continue
			}
			seenHere[p] = true
			if prev, ok := chunkOf[p]; ok {
				t.Errorf("file %q appears in chunk %d AND chunk %d — a per-chunk reclaim "+
					"would delete the nodes the other chunk landed", p, prev, ci)
				continue
			}
			chunkOf[p] = ci
		}
	}
	assert.Len(t, chunkOf, len(files)+1, "every file, plus the file-less group, must be accounted for exactly once")

	var flattened int
	for _, c := range chunks {
		flattened += len(c)
	}
	assert.Equal(t, len(nodes), flattened, "no node may be dropped by grouping")

	// THE OVERSIZE CASE IS PART OF THE PROPERTY. A single file whose own nodes
	// exceed the byte cap must still land in ONE chunk carrying only that file —
	// the exactly-one-chunk invariant outranks the soft byte budget, and the
	// largest file is precisely where a split would corrupt.
	var fat []*knowledgev1.Node
	for i := range 40 {
		fat = append(fat, node("pkg/fat.go:Sym"+strconv.Itoa(i), "pkg/fat.go"))
	}
	fat = append(fat, node("pkg/small.go:Sym", "pkg/small.go"))
	fatChunks := BatchNodes(fat, maxBytes)
	fatChunkIdx := -1
	for ci, chunk := range fatChunks {
		for _, n := range chunk {
			if n.GetFilePath() != "pkg/fat.go" {
				continue
			}
			if fatChunkIdx >= 0 && fatChunkIdx != ci {
				t.Fatalf("the oversize file was split across chunks %d and %d", fatChunkIdx, ci)
			}
			fatChunkIdx = ci
		}
	}
	require.GreaterOrEqual(t, fatChunkIdx, 0, "the oversize file must appear somewhere")
	for _, n := range fatChunks[fatChunkIdx] {
		assert.Equal(t, "pkg/fat.go", n.GetFilePath(),
			"an oversize file's chunk must carry only that file")
	}
}

func TestBatchEdgesProto_RespectsSizeBoundAndOrder(t *testing.T) {
	// Each edge serializes to ~60 bytes; a small maxBytes must produce multiple
	// groups, each (bar a lone oversized edge) under the bound, and the flattened
	// groups must reassemble exactly the input edges in order — none dropped.
	edges := make([]*knowledgev1.BatchEdge, 10)
	for i := range edges {
		edges[i] = &knowledgev1.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromId:   string(rune('a'+i)) + "-from-node",
			ToId:     string(rune('a'+i)) + "-to-node",
			Type:     "relates-to",
			Evidence: "evidence payload for edge batching",
		}
	}
	const maxBytes = 200
	chunks := BatchEdgesProto(edges, maxBytes)
	require.GreaterOrEqual(t, len(chunks), 2, "10 edges at a 200-byte bound must produce multiple chunks")

	var flattened []*knowledgev1.BatchEdge
	for _, chunk := range chunks {
		var chunkBytes int
		for _, e := range chunk {
			chunkBytes += proto.Size(e) + 16
		}
		if len(chunk) > 1 {
			assert.LessOrEqual(t, chunkBytes, maxBytes, "multi-edge chunk must stay under the byte bound")
		}
		flattened = append(flattened, chunk...)
	}
	require.Len(t, flattened, len(edges), "every edge must survive the split (full reassembly)")
	for i := range edges {
		assert.Equal(t, edges[i].FromId, flattened[i].FromId, "edge order must be preserved across chunks")
		assert.Equal(t, edges[i].ToId, flattened[i].ToId, "edge order must be preserved across chunks")
	}
}

// TestBatchEdgesProto_EachSourceInExactlyOneChunk pins the invariant the server's
// per-chunk edge clear depends on: every distinct source appears in EXACTLY ONE
// chunk.
//
// THE PROPERTY IS "EXACTLY ONE CHUNK", NOT "A RUN IS NEVER SPLIT". A chunker that
// merely cut at from_id boundaries would satisfy the weaker reading and change
// nothing, because production edge order is not grouped to begin with: the parser
// emits one language edge per declaration while walking every file, then appends
// every resolved call edge after all of them, so each source has outbound edges in
// two regions far apart. The fixture below reproduces exactly that layout — a
// fixture of already-grouped input would pass against a chunker that does no
// grouping at all, which is the vacuity this test exists to avoid.
//
// WHY IT IS LOAD-BEARING: the server deletes a source's resident collector-owned
// edges that are absent from the chunk's incoming set. If a source spanned two
// chunks, the second would compute the first's freshly-landed edges as absent and
// delete them.
func TestBatchEdgesProto_EachSourceInExactlyOneChunk(t *testing.T) {
	edge := func(from, to string) *knowledgev1.BatchEdge {
		return &knowledgev1.BatchEdge{
			FromIdx: -1, ToIdx: -1, FromId: from, ToId: to,
			Type: "calls", Evidence: "evidence payload sized to force several chunks",
		}
	}
	sources := []string{"pkg/a.go:A", "pkg/b.go:B", "pkg/c.go:C", "pkg/d.go:D", "pkg/e.go:E"}

	// PRODUCTION-SHAPED INPUT: region 1 is one language edge per source across the
	// whole repository; region 2 is every resolved call edge, appended after all of
	// them. No source's edges are contiguous.
	var edges []*knowledgev1.BatchEdge
	for _, s := range sources {
		edges = append(edges, edge(s, "lang:go"))
	}
	for round := range 3 {
		for _, s := range sources {
			edges = append(edges, edge(s, s+"-callee-"+strconv.Itoa(round)))
		}
	}

	// FIXTURE CONTROL: assert the input really is non-contiguous, so a later edit
	// that "tidies" the fixture into grouped order cannot silently make this test
	// vacuous.
	require.NotEqual(t, edges[0].FromId, edges[1].FromId,
		"fixture must be production-shaped: a source's edges must NOT be contiguous in the input")

	const maxBytes = 260
	chunks := BatchEdgesProto(edges, maxBytes)
	require.GreaterOrEqual(t, len(chunks), 2,
		"the fixture must span several chunks, or the invariant is untested")

	chunkOf := map[string]int{}
	for ci, chunk := range chunks {
		seenHere := map[string]bool{}
		for _, e := range chunk {
			if seenHere[e.FromId] {
				continue
			}
			seenHere[e.FromId] = true
			if prev, ok := chunkOf[e.FromId]; ok {
				t.Errorf("source %q appears in chunk %d AND chunk %d — a per-chunk set "+
					"difference would delete the edges the other chunk landed", e.FromId, prev, ci)
				continue
			}
			chunkOf[e.FromId] = ci
		}
	}
	assert.Len(t, chunkOf, len(sources), "every source must be accounted for exactly once")

	var flattened int
	for _, c := range chunks {
		flattened += len(c)
	}
	assert.Equal(t, len(edges), flattened, "no edge may be dropped by grouping")

	// THE OVERSIZE CASE IS PART OF THE PROPERTY. A single source whose own bundle
	// exceeds the byte cap must still land in ONE chunk carrying only that source —
	// the exactly-one-chunk invariant outranks the soft byte budget, and the
	// highest-fan-out source is precisely where a split would corrupt.
	var fat []*knowledgev1.BatchEdge
	for i := range 40 {
		fat = append(fat, edge("pkg/fat.go:Fat", "callee-"+strconv.Itoa(i)))
	}
	fat = append(fat, edge("pkg/small.go:Small", "callee"))
	fatChunks := BatchEdgesProto(fat, maxBytes)
	var fatChunkIdx = -1
	for ci, chunk := range fatChunks {
		for _, e := range chunk {
			if e.FromId != "pkg/fat.go:Fat" {
				continue
			}
			if fatChunkIdx >= 0 && fatChunkIdx != ci {
				t.Fatalf("the oversize source was split across chunks %d and %d", fatChunkIdx, ci)
			}
			fatChunkIdx = ci
		}
	}
	require.GreaterOrEqual(t, fatChunkIdx, 0, "the oversize source must appear somewhere")
	for _, e := range fatChunks[fatChunkIdx] {
		assert.Equal(t, "pkg/fat.go:Fat", e.FromId,
			"an oversize source's chunk must carry only that source")
	}
}

func TestBatchEdgesProto_DefaultWhenMaxBytesZero(t *testing.T) {
	// maxBytes<=0 defaults to kgwire.MaxCloudRequestBytes (64 MiB), so a single
	// small edge lands in exactly one group.
	edges := []*knowledgev1.BatchEdge{
		{FromIdx: -1, ToIdx: -1, FromId: "x", ToId: "y", Type: "calls"},
	}
	chunks := BatchEdgesProto(edges, 0)
	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 1)
	assert.Equal(t, "x", chunks[0][0].FromId)
	assert.Equal(t, "y", chunks[0][0].ToId)

	// Empty input → nil.
	require.Nil(t, BatchEdgesProto(nil, 0))
}
