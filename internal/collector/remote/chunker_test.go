// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestBatchNodes_RespectsSizeBound(t *testing.T) {
	// Each node's JSON is ~100 bytes. maxBytes=250 should produce 2-3
	// nodes per batch.
	nodes := make([]*knowledgev1.Node, 10)
	for i := range nodes {
		nodes[i] = &knowledgev1.Node{
			Id:         string(rune('a'+i)) + "-node",
			Type:       string(kgtypes.NodeFile),
			SymbolName: string(rune('a' + i)),
			Content:    "the content goes here for batching",
		}
	}
	batches, hashes, err := BatchNodes(nodes, 250)
	require.NoError(t, err)
	require.Len(t, hashes, len(nodes))
	require.GreaterOrEqual(t, len(batches), 2, "10 nodes at 250-byte bound must produce multiple batches")

	// Reassemble: hash slice ordering must match concatenated batch order.
	var reassembled []string
	for _, b := range batches {
		for _, c := range b.Chunks {
			reassembled = append(reassembled, c.Hash)
		}
	}
	assert.Equal(t, hashes, reassembled, "hashes slice must align with batch envelope order")
}

func TestBatchNodes_DefaultWhenMaxBytesZero(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "x", SymbolName: "x.go"},
	}
	batches, hashes, err := BatchNodes(nodes, 0)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	require.Len(t, hashes, 1)
	assert.Equal(t, hashes[0], batches[0].Chunks[0].Hash)
}
