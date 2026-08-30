// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// walkChild captures a child node + its single depends-on dependency
// ID for the topological-sort step. Mirrors the server-side type at
// cmd/knowledge-server/tools/tools_walk.go:92-95. Only one dependsOn
// ID is recorded because plan-tree dependencies are linear within a
// phase by construction.
type walkChild struct {
	node      *knowledgev1.Node
	dependsOn string // ID of node this depends on (for ordering)
}

// RenderTreeFromIndex renders a node and its contains-children as an
// indented tree, reading children from a prebuilt parent→child index
// (render.BuildChildIndex) and sibling ordering from a prebuilt dependsOn
// map (render.FetchDependsOnEdges), so it issues no per-node fetches. The
// whole subtree is fetched up front in one traversal by AssembleSubtree;
// this walk is pure in-memory.
//
// It is the ONLY tree renderer in the shipped binary. Its per-node line
// format (proxy/plain line, description truncate, ID line, depth cutoff)
// and its depends-on topoSort are byte-identical to the per-node walk it
// replaced, which now survives only in the test binary as the parity
// oracle that keeps proving that equivalence — see
// render/parity_oracle_test.go.
//
// The missing-child / diamond-dedup tolerances are enforced upstream in
// BuildChildIndex (an edge to an absent node, or a second edge to an
// already-placed node, never enters childIndex), so this walk has no
// per-child fetch to guard.
func RenderTreeFromIndex(
	sb *strings.Builder,
	node *knowledgev1.Node,
	depth, maxDepth int,
	childIndex map[string][]*knowledgev1.Node,
	dependsOn map[string]string,
) {
	indent := strings.Repeat("  ", depth)
	status := ""
	if node.Status != "" {
		status = " [" + node.Status + "]"
	}
	proxyLabel := proxyAnnotation(node)
	if proxyLabel != "" {
		fmt.Fprintf(sb, "%s%s (%s)%s %s\n", indent, node.SymbolName, node.Type, status, proxyLabel)
	} else {
		fmt.Fprintf(sb, "%s%s (%s)%s\n", indent, node.SymbolName, node.Type, status)
	}
	if node.Description != "" && depth > 0 {
		fmt.Fprintf(sb, "%s  %s\n", indent, truncate(node.Description, 120))
	}
	fmt.Fprintf(sb, "%s  ID: %s%s\n", indent, node.Id, updatedSuffix(node))

	if depth >= maxDepth {
		return
	}

	indexed := childIndex[node.Id]
	children := make([]walkChild, 0, len(indexed))
	for _, c := range indexed {
		children = append(children, walkChild{node: c, dependsOn: dependsOn[c.Id]})
	}
	// Topological sort by depends-on edges — shared with RenderTree.
	children = topoSort(children)

	for _, c := range children {
		RenderTreeFromIndex(sb, c.node, depth+1, maxDepth, childIndex, dependsOn)
	}
}

// topoSort orders children by depends-on edges (nodes with no deps
// first). Byte-identical port of cmd/knowledge-server/tools/
// tools_walk.go:158-215 — pure function over the walkChild slice.
func topoSort(children []walkChild) []walkChild {
	if len(children) <= 1 {
		return children
	}

	// Build adjacency: who depends on whom.
	idIdx := make(map[string]int, len(children))
	for i, c := range children {
		idIdx[c.node.Id] = i
	}

	// Kahn's algorithm.
	inDegree := make([]int, len(children))
	for i, c := range children {
		if _, ok := idIdx[c.dependsOn]; ok {
			inDegree[i]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	var sorted []walkChild
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		sorted = append(sorted, children[idx])

		// Find nodes that depend on this one.
		for i, c := range children {
			if c.dependsOn == children[idx].node.Id {
				inDegree[i]--
				if inDegree[i] == 0 {
					queue = append(queue, i)
				}
			}
		}
	}

	// Append any remaining (cycles or unlinked).
	if len(sorted) < len(children) {
		seen := make(map[int]bool)
		for _, s := range sorted {
			seen[idIdx[s.node.Id]] = true
		}
		for i, c := range children {
			if !seen[i] {
				sorted = append(sorted, c)
			}
		}
	}

	return sorted
}
