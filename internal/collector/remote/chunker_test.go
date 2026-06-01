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
