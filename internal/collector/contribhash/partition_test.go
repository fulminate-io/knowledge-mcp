// SPDX-License-Identifier: Apache-2.0

package contribhash

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// partition_test.go — the catcher for FROM-node edge ownership.
//
// The two tests below are SEPARATE TOP-LEVEL TESTS deliberately. Folded behind
// one PASS line, an implementation satisfying only the cross-file half still
// shows one green parent; and the same-file half is exactly what a fixture suite
// built from same-file cases would never contain.

// receiverEdge models the shape parser/edges.go resolveContainment emits for a Go
// method whose container is its receiver TYPE: the edge's FROM node is the
// RECEIVER's node, resolved at PACKAGE scope, so it can live in a different file
// from the method that produced the edge.
func receiverEdge(receiverNodeID, methodNodeID string) kgwire.BatchEdge {
	return kgwire.BatchEdge{
		FromIdx: -1, ToIdx: -1,
		FromID: receiverNodeID, ToID: methodNodeID, Type: kgtypes.EdgeContains,
	}
}

func fileNode(id, path string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeFile), SymbolName: id, FilePath: path,
		Language: "go", StartLine: 1, EndLine: 2, Content: "x",
	}
}

// TestPartitionByOwningFile_ReceiverContainmentRidesReceiverFile is the half a
// reference-site partition fails: the receiver TYPE is declared in a DIFFERENT
// file from the method, and the edge must ride the RECEIVER's file.
func TestPartitionByOwningFile_ReceiverContainmentRidesReceiverFile(t *testing.T) {
	const receiverFile, methodFile = "pkg/types.go", "pkg/methods.go"
	receiver := fileNode("pkg.Thing", receiverFile)
	method := fileNode("pkg.Thing.Do", methodFile)
	edge := receiverEdge(receiver.GetId(), method.GetId())

	byFile, fileless := PartitionByOwningFile(
		[]*knowledgev1.Node{receiver, method}, []kgwire.BatchEdge{edge})

	require.Len(t, byFile[receiverFile].Edges, 1,
		"the containment edge is owned by the file its FROM node — the receiver type — is declared in")
	require.Equal(t, edge.FromID, byFile[receiverFile].Edges[0].FromID)
	require.Empty(t, byFile[methodFile].Edges,
		"the method's file must NOT own the edge: a reference-site partition would ride it here, "+
			"its true owner would never first-land, and the stale predecessor would never be cleared")
	require.Empty(t, fileless.Edges, "both endpoints carry file paths, so nothing is fileless")

	// Known-positive control on the node side, so the edge assertions above are
	// read against a partition that demonstrably did group something.
	require.Len(t, byFile[receiverFile].Nodes, 1)
	require.Len(t, byFile[methodFile].Nodes, 1)
}

// TestPartitionByOwningFile_SameFileReceiverIsUnaffected is the known-negative:
// the same edge shape with the receiver in the SAME file must land in that one
// file's group and appear in no other. It is what an OVER-corrected partition
// fails — one that routes every containment edge somewhere fixed, or duplicates
// it into two groups, passes the cross-file case alone.
func TestPartitionByOwningFile_SameFileReceiverIsUnaffected(t *testing.T) {
	const sameFile, otherFile = "pkg/all.go", "pkg/other.go"
	receiver := fileNode("pkg.Thing", sameFile)
	method := fileNode("pkg.Thing.Do", sameFile)
	bystander := fileNode("pkg.Other", otherFile)
	edge := receiverEdge(receiver.GetId(), method.GetId())

	byFile, fileless := PartitionByOwningFile(
		[]*knowledgev1.Node{receiver, method, bystander}, []kgwire.BatchEdge{edge})

	require.Len(t, byFile[sameFile].Edges, 1, "the edge lands in its one owning file")
	require.Empty(t, byFile[otherFile].Edges, "and in no other file's group")
	require.Empty(t, fileless.Edges)

	// The edge must appear EXACTLY once across every group — a duplicating
	// partition would still satisfy both assertions above if it also wrote the
	// edge into a third group.
	total := 0
	for _, g := range byFile {
		total += len(g.Edges)
	}
	require.Equal(t, 1, total+len(fileless.Edges), "the edge is owned exactly once, by exactly one group")
}

// TestPartitionByOwningFile_FilelessNodesAndTheirEdges pins the fileless class:
// hierarchy package nodes and the repo root carry no file path, so they and the
// edges they own ride the fileless group — which always uploads and is never an
// entry in the manifest.
func TestPartitionByOwningFile_FilelessNodesAndTheirEdges(t *testing.T) {
	pkgNode := &knowledgev1.Node{Id: "pkg", Type: string(kgtypes.NodePackage)}
	root := &knowledgev1.Node{Id: ".", Type: string(kgtypes.NodePackage)}
	filed := fileNode("pkg.Thing", "pkg/a.go")
	// A hierarchy CONTAINS edge is FROM the package node, so it rides fileless
	// with its source.
	hierEdge := kgwire.BatchEdge{
		FromIdx: -1, ToIdx: -1, FromID: "pkg", ToID: "pkg/a.go", Type: kgtypes.EdgeContains,
	}
	// A language edge is FROM the SYMBOL, which does carry a file path, so it is
	// file-owned rather than fileless.
	langEdge := kgwire.BatchEdge{
		FromIdx: -1, ToIdx: -1, FromID: "pkg.Thing", ToID: "lang:repo:go", Type: kgtypes.EdgeLanguage,
	}

	byFile, fileless := PartitionByOwningFile(
		[]*knowledgev1.Node{pkgNode, root, filed}, []kgwire.BatchEdge{hierEdge, langEdge})

	require.Len(t, fileless.Nodes, 2, "the package node and the repo root are fileless")
	require.Len(t, fileless.Edges, 1, "the hierarchy edge rides fileless with its package source")
	require.Len(t, byFile["pkg/a.go"].Edges, 1, "the language edge is FROM the symbol, so it is file-owned")
	require.NotContains(t, byFile, "", "a fileless node must never create an empty-path group")
}
