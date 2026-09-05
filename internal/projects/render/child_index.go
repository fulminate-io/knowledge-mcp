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
// selection already does this). THE CALLER'S TYPE FILTER is what makes
// an edge part of the containment relation — this function never
// consults the edge type itself.
//
// SIBLING ORDER within a parent is the child's POSITION where one is
// carried (see child_order.go) and edge arrival order otherwise. The
// sort is stable and ranks an unpositioned edge equal to its unpositioned
// peers, so a set carrying no position is bit-for-bit unchanged. The
// text path's depends-on topo-sort is a SEPARATE ordering still applied
// by the caller, and it OVERRIDES this one where a depends-on chain
// exists.
//
// Two tolerances mirror the per-node walk this replaces:
//
//   - Missing child: an edge whose ToId is not present in the node set
//     (tombstone-filtered, or genuinely absent) is skipped with the
//     same "skipping missing child node" warning the per-node walker
//     logged.
//   - Diamond dedup: a `placed` visited-set ensures a child reached by
//     more than one contains edge is attached under exactly ONE parent
//     — the first edge that reaches it in SORTED order. Later edges to
//     an already-placed child are skipped, so a node with two contains
//     parents renders once. This is the guard the recursive per-node
//     walk lacked. WHICH parent wins is defined rather than incidental:
//     two POSITIONED edges are decided by position, and with neither
//     positioned the first edge in traversal order still wins.
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
	// Sibling ordering is applied HERE, before the append loop, so that every
	// consumer of the index inherits it: the two tree-rendering paths, the
	// plan_tree json branch, the json assemble, the ticket/project renderers and
	// the status-cascade partitioner all read childIndex and only some of them
	// call a tree renderer. The sort is stable and ranks an unpositioned edge
	// equal to its unpositioned peers, so an edge set carrying no position — every
	// tree written before positions existed — comes through untouched.
	//
	// IT ALSO DECIDES THE DIAMOND. The `placed` guard below attaches a child to
	// the FIRST edge that reaches it, so ordering the edges redefines which parent
	// that is: for a child reached by two POSITIONED containment edges,
	// attribution follows POSITION by definition rather than traversal order. With
	// neither edge positioned the stable sort leaves the arrival order alone and
	// the first edge still wins, exactly as before.
	for _, e := range sortStructureEdgesByPosition(byID, structureEdges) {
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
