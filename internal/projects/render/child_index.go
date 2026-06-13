// SPDX-License-Identifier: Apache-2.0

package render

import (
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// BuildChildIndex turns a flat (nodes, structureEdges) result — the
// output of a single subtree traversal — into a parent→child index
// plus an id→node lookup, so a tree can be rendered with zero per-node
// fetches.
//
// structureEdges are the subtree's parent→child (contains) edges in
// their traversal discovery order; the caller is responsible for
// scoping them to the structure edge type (the traversal's EdgeTypes
// selection already does this). The returned childIndex preserves edge
// order within each parent; ordering concerns (the text path's
// depends-on topo-sort) are applied by the caller, not here.
//
// Two tolerances mirror the per-node walk this replaces:
//
//   - Missing child: an edge whose ToId is not present in the node set
//     (tombstone-filtered, or genuinely absent) is skipped with the
//     same "skipping missing child node" warning the per-node walker
//     logged.
//   - Diamond dedup: a `placed` visited-set ensures a child reached by
//     more than one contains edge is attached under exactly ONE parent
//     — the first edge that reaches it in traversal order. Later edges
//     to an already-placed child are skipped, so a node with two
//     contains parents renders once. This is the guard the recursive
//     per-node walk lacked.
func BuildChildIndex(
	rootID string,
	nodes []*knowledgev1.Node,
	structureEdges []*knowledgev1.Edge,
) (childIndex map[string][]*knowledgev1.Node, byID map[string]*knowledgev1.Node) {
	_ = rootID // accepted for caller symmetry; the index is keyed by edge endpoints.

	byID = make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		if n == nil || n.Id == "" {
			continue
		}
		byID[n.Id] = n
	}

	childIndex = make(map[string][]*knowledgev1.Node)
	placed := make(map[string]bool, len(nodes))
	for _, e := range structureEdges {
		if e == nil {
			continue
		}
		child, ok := byID[e.ToId]
		if !ok {
			// The contains edge dangles to a node absent from the
			// fetched set (tombstone-filtered or hard-deleted) — skip it,
			// matching the per-node walker's log-and-continue.
			slog.Warn("skipping missing child node", "id", e.ToId)
			continue
		}
		if placed[child.Id] {
			// Already attached under an earlier parent — diamond dedup.
			continue
		}
		placed[child.Id] = true
		childIndex[e.FromId] = append(childIndex[e.FromId], child)
	}
	return childIndex, byID
}
