// SPDX-License-Identifier: Apache-2.0

package remote

import (
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
	nodes := make([]*knowledgev1.Node, 10)
	for i := range nodes {
		nodes[i] = &knowledgev1.Node{
			Id:         string(rune('a'+i)) + "-node",
			Type:       string(kgtypes.NodeFile),
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
