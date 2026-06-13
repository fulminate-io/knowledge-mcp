// SPDX-License-Identifier: Apache-2.0

package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func containsEdge(from, to string) *knowledgev1.Edge {
	return &knowledgev1.Edge{FromId: from, ToId: to, Type: string(kgtypes.EdgeKGContains)}
}

func node(id string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, SymbolName: id, Type: string(kgtypes.NodeStep)}
}

// TestBuildChildIndex_MissingChildSkipped asserts an edge whose target is not in
// the node set is silently skipped while a sibling real child still appears.
func TestBuildChildIndex_MissingChildSkipped(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("real")}
	edges := []*knowledgev1.Edge{
		containsEdge("root", "missing"), // "missing" has no node → skipped
		containsEdge("root", "real"),
	}

	childIndex, byID := BuildChildIndex("root", nodes, edges)

	require.Len(t, childIndex["root"], 1, "only the real child is indexed")
	assert.Equal(t, "real", childIndex["root"][0].Id)
	_, hasMissing := byID["missing"]
	assert.False(t, hasMissing, "missing node never entered byID")
}

// TestBuildChildIndex_DiamondDedup asserts a child reached by two contains edges
// is placed under exactly one parent (the first edge), never both, and byID holds
// every node.
func TestBuildChildIndex_DiamondDedup(t *testing.T) {
	nodes := []*knowledgev1.Node{node("root"), node("a"), node("b"), node("c")}
	edges := []*knowledgev1.Edge{
		containsEdge("root", "a"),
		containsEdge("root", "b"),
		containsEdge("a", "c"), // first edge reaching c
		containsEdge("b", "c"), // diamond — must be dropped
	}

	childIndex, byID := BuildChildIndex("root", nodes, edges)

	inA := len(childIndex["a"])
	inB := len(childIndex["b"])
	assert.Equal(t, 1, inA+inB, "c appears under exactly one parent")
	assert.Equal(t, 1, inA, "c is placed under a (the first contains edge), not b")
	assert.Equal(t, 0, inB)

	for _, id := range []string{"root", "a", "b", "c"} {
		_, ok := byID[id]
		assert.True(t, ok, "byID must contain %s", id)
	}
}

// TestBuildChildIndex_PreservesEdgeOrder asserts children within a parent keep
// the order their edges arrived in.
func TestBuildChildIndex_PreservesEdgeOrder(t *testing.T) {
	nodes := []*knowledgev1.Node{node("parent"), node("a"), node("b")}
	edges := []*knowledgev1.Edge{
		containsEdge("parent", "b"),
		containsEdge("parent", "a"),
	}

	childIndex, _ := BuildChildIndex("parent", nodes, edges)

	require.Len(t, childIndex["parent"], 2)
	assert.Equal(t, "b", childIndex["parent"][0].Id)
	assert.Equal(t, "a", childIndex["parent"][1].Id)
}
