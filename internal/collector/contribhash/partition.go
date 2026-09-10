// SPDX-License-Identifier: Apache-2.0

package contribhash

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// partition.go — the owning-file partition the per-file hash and the diff both
// consume.

// FileGroup is one owning file's share of a collect result.
type FileGroup struct {
	Nodes []*knowledgev1.Node
	Edges []kgwire.BatchEdge
}

// PartitionByOwningFile splits a collect result's nodes and edges by the file
// that OWNS them, returning the per-file groups plus the fileless remainder.
//
// THE INPUT IS THE CollectResult'S TWO SLICES, not the parser's PopulateResult:
// codesync's collector mutates the parser's return after it comes back (the
// hierarchy append), so the CALLS edges partitioned here are the tree-sitter ones
// the server actually stores, and the package and repo-root nodes ARE present in
// the node slice.
//
// EDGE OWNERSHIP KEYS ON THE FROM NODE — docs/collect-contribution-hash.md
// section E, cited rather than restated. Concretely: a Go receiver-containment
// edge's FromID is the RECEIVER TYPE's node, resolved at PACKAGE scope, so the
// receiver's declaration may live in a different file from the method that
// produced the edge. Partitioning on the reference SITE would ride that edge on
// the wrong file's upload, its true owner would never first-land, and the stale
// predecessor would never be cleared. (The reference-site fields do not survive
// into kgwire.BatchEdge at all, which makes the wrong partition hard to write by
// accident — but a future carrier could reintroduce them, which is why the
// criterion forbids the tokens outright.)
//
// AN EDGE WHOSE FromID RESOLVES TO NO NODE in the slice, or to a node with an
// empty file path, joins the FILELESS group.
//
// THE FILELESS GROUP ALWAYS UPLOADS (spec section H) and never enters the
// manifest. It holds the hierarchy package nodes and the repo root — both built
// with Id and Type only — and the language node. The hierarchy CONTAINS edges are
// FromID-owned by a package node, so they ride the fileless group with their
// source and need no special case. The LANGUAGE edges do NOT: they are built with
// FromId = the symbol's node id, which does carry a file path.
//
// FILELESS DOES NOT MEAN UNDELETABLE. Package and repo-root nodes are deleted by
// NAMING THEIR DIRECTORY on the deletion carrier, never by inference; the naming
// rule and its server-side validation live elsewhere, and rest on the identity a
// package node's Id IS its directory path.
//
// PROXY ROWS ARE NOT THIS FUNCTION'S CONCERN: the client never holds one. The
// fileless group here is the CLIENT-EMITTED no-file-path class only; the
// manifest's proxy exclusion is a server-side population rule with no upload
// obligation attached.
//
// PERF SHAPE: one O(nodes) pass building an id -> file-path map, then one
// O(edges) pass assigning owners. No per-edge search.
func PartitionByOwningFile(
	nodes []*knowledgev1.Node, edges []kgwire.BatchEdge,
) (byFile map[string]FileGroup, fileless FileGroup) {
	fileByID := make(map[string]string, len(nodes))
	byFile = make(map[string]FileGroup, len(nodes))
	for _, n := range nodes {
		path := n.GetFilePath()
		fileByID[n.GetId()] = path
		if path == "" {
			fileless.Nodes = append(fileless.Nodes, n)
			continue
		}
		g := byFile[path]
		g.Nodes = append(g.Nodes, n)
		byFile[path] = g
	}
	for _, e := range edges {
		path, ok := fileByID[e.FromID]
		if !ok || path == "" {
			fileless.Edges = append(fileless.Edges, e)
			continue
		}
		g := byFile[path]
		g.Edges = append(g.Edges, e)
		byFile[path] = g
	}
	return byFile, fileless
}

// PartitionByOwningNode splits a collect result by the NODE that owns each row —
// the partition a NODE-KEYED diff hashes, for a graph family whose collector
// emits records rather than files.
//
// IT IS THE SAME OWNERSHIP RULE AS PartitionByOwningFile, READ ONE LEVEL IN.
// Section E says an edge is owned by its FROM node; the file partition then
// takes that node's FILE as the key, and this one takes the NODE ITSELF. So the
// two are the same rule with and without the final projection, which is what
// makes an edge ride with its source under either key.
//
// EVERY NODE IS A KEY, INCLUDING A FILELESS ONE, and that is the whole reason
// this partition exists. A registered collector's node carries an optional file
// path and usually none, so the file partition puts its entire graph in the
// fileless remainder — a group that is outside the manifest by construction and
// therefore re-uploads on every collect forever.
//
// AN EDGE WHOSE FromID NAMES NO NODE IN THE SLICE IS UNOWNED and is returned
// separately rather than being folded into some node's group. It belongs to no
// key, so no manifest entry can describe it and no diff can ever mark it
// changed; the upload filter carries it on every collect for exactly that
// reason. Folding it into an arbitrary group instead would make that group's
// hash depend on a row the server's own aggregate cannot attribute to it, and
// the group would read CHANGED forever.
//
// PERF SHAPE: one O(nodes) pass building the id set and the singleton groups,
// then one O(edges) pass assigning owners. No per-edge search.
func PartitionByOwningNode(
	nodes []*knowledgev1.Node, edges []kgwire.BatchEdge,
) (byNode map[string]FileGroup, unowned FileGroup) {
	byNode = make(map[string]FileGroup, len(nodes))
	for _, n := range nodes {
		id := n.GetId()
		if id == "" {
			// A NODE WITH NO ID HAS NO KEY. It cannot be diffed, declined or named as
			// a deletion, so it joins the unowned remainder and uploads every time.
			// The server assigns ids to id-less rows, so this is not an error here;
			// it is a row whose identity does not exist until after it lands.
			unowned.Nodes = append(unowned.Nodes, n)
			continue
		}
		g := byNode[id]
		g.Nodes = append(g.Nodes, n)
		byNode[id] = g
	}
	for _, e := range edges {
		g, ok := byNode[e.FromID]
		if !ok {
			unowned.Edges = append(unowned.Edges, e)
			continue
		}
		g.Edges = append(g.Edges, e)
		byNode[e.FromID] = g
	}
	return byNode, unowned
}
