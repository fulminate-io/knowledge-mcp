// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
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

// RenderTree recursively renders a node and its contains-children as
// an indented tree. Ported from cmd/knowledge-server/tools/
// tools_walk.go:97-155 with the server-side store reads swapped for
// wire-shape FetchNode + IterEdges calls and resolveProxyIfNeeded
// degraded to proxyAnnotation (client-side rendering cannot resolve
// proxies — see render/helpers.go package doc).
//
// edgeTypes is variadic for caller convenience: pass nothing → walk
// contains edges; pass a slice → walk those edge types. Mirrors the
// server-side variadic-slice signature exactly.
func RenderTree(
	ctx context.Context,
	gc GraphCaller,
	sb *strings.Builder,
	node *knowledgev1.Node,
	depth, maxDepth int,
	edgeTypes ...[]kgtypes.EdgeType,
) {
	followEdges := []kgtypes.EdgeType{kgtypes.EdgeKGContains}
	if len(edgeTypes) > 0 && len(edgeTypes[0]) > 0 {
		followEdges = edgeTypes[0]
	}
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
	fmt.Fprintf(sb, "%s  ID: %s\n", indent, node.Id)

	if depth >= maxDepth {
		return
	}

	children := walkChildrenForTree(ctx, gc, node.Id, followEdges)
	// Topological sort by depends-on edges.
	children = topoSort(children)

	for _, c := range children {
		RenderTree(ctx, gc, sb, c.node, depth+1, maxDepth, followEdges)
	}
}

// walkChildrenForTree fetches the children of nodeID along followEdges
// and pairs each with its single depends-on dependency ID. Extracted
// from RenderTree to keep gocognit under the limit.
func walkChildrenForTree(
	ctx context.Context,
	gc GraphCaller,
	nodeID string,
	followEdges []kgtypes.EdgeType,
) []walkChild {
	childEdges, qErr := IterEdges(ctx, gc, nodeID, kgwire.OutgoingEdges, followEdges...)
	if qErr != nil {
		slog.Warn("failed to get children", "id", nodeID, "error", qErr)
		return nil
	}

	var children []walkChild
	for _, e := range childEdges {
		childNode, err := FetchNode(ctx, gc, e.ToId)
		if err != nil {
			slog.Warn("skipping missing child node", "id", e.ToId, "error", err)
			continue
		}
		if childNode == nil {
			continue
		}
		// Check if this child has a depends-on edge (for ordering).
		depID := firstDependsOn(ctx, gc, e.ToId)
		children = append(children, walkChild{node: childNode, dependsOn: depID})
	}
	return children
}

// firstDependsOn returns the first outgoing depends-on target ID for
// the given child node, or empty string if none. The server-side
// renderer treats depends-on as a linear chain within a phase, so a
// single ID is the documented contract.
func firstDependsOn(ctx context.Context, gc GraphCaller, childID string) string {
	depEdges, _ := IterEdges(ctx, gc, childID, kgwire.OutgoingEdges, kgtypes.EdgeDependsOn)
	if len(depEdges) == 0 {
		return ""
	}
	return depEdges[0].ToId
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
