// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// assembleNode is a recursive tree node for JSON assemble output.
// Verbatim port of cmd/knowledge-server/tools/tools_assemble_json.go:15.
type assembleNode struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      string            `json:"status,omitempty"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Children    []assembleNode    `json:"children,omitempty"`
}

// assembleJSON builds a recursive tree from any node and returns it
// as JSON. Universal JSON path for assemble — walks EdgeKGContains
// children recursively (same hierarchy as RenderTree) and includes
// linked research/decisions as extra top-level fields.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_json.go:29
// with the store reads swapped for wire-shape FetchNode + IterEdges.
func assembleJSON(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	tree := buildAssembleTree(ctx, gc, node, 0, 5)
	result := map[string]any{"root": tree}

	research, decisions := collectLinkedNodes(ctx, gc, node.Id)
	if len(research) > 0 {
		result["research"] = research
	}
	if len(decisions) > 0 {
		result["decisions"] = decisions
	}

	b, err := json.Marshal(result)
	if err != nil {
		return kgtools.ErrorResult("json marshal: " + err.Error())
	}
	return kgtools.TextResult(string(b))
}

// buildAssembleTree recursively builds a tree of assembleNode from
// contains edges.
func buildAssembleTree(ctx context.Context, gc GraphCaller, node *knowledgev1.Node, depth, maxDepth int) assembleNode {
	an := assembleNode{
		ID:          node.Id,
		Name:        node.SymbolName,
		Type:        node.Type,
		Status:      node.Status,
		Description: node.Description,
		Metadata:    nonEmptyMeta(node),
	}

	if depth >= maxDepth {
		return an
	}

	childEdges, err := IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, kgtypes.EdgeKGContains)
	if err != nil {
		slog.Warn("assembleJSON: children query failed", "id", node.Id, "error", err)
		return an
	}

	for _, e := range childEdges {
		cn, cerr := FetchNode(ctx, gc, e.ToId)
		if cerr != nil || cn == nil {
			continue
		}
		an.Children = append(an.Children, buildAssembleTree(ctx, gc, cn, depth+1, maxDepth))
	}

	return an
}

// collectLinkedNodes finds research and decision nodes linked to
// nodeID via EdgeInformedBy (outgoing for research, incoming for
// decisions). Verbatim port of tools_assemble_json.go:79 with the
// store reads swapped for wire-shape calls.
func collectLinkedNodes(ctx context.Context, gc GraphCaller, nodeID string) ([]assembleNode, []assembleNode) {
	var research, decisions []assembleNode

	outEdges, _ := IterEdges(ctx, gc, nodeID, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy)
	for _, e := range outEdges {
		n, err := FetchNode(ctx, gc, e.ToId)
		if err != nil || n == nil {
			continue
		}
		if kgtypes.NodeType(n.Type) == kgtypes.NodeResearch {
			research = append(research, nodeToAssembleNode(n))
		}
	}

	inEdges, _ := IterEdges(ctx, gc, nodeID, kgwire.IncomingEdges, kgtypes.EdgeInformedBy)
	for _, e := range inEdges {
		n, err := FetchNode(ctx, gc, e.FromId)
		if err != nil || n == nil {
			continue
		}
		if kgtypes.NodeType(n.Type) == kgtypes.NodeDecision {
			decisions = append(decisions, nodeToAssembleNode(n))
		}
	}

	return research, decisions
}

// nodeToAssembleNode converts a wire node to a flat assembleNode
// (no children). Verbatim port of tools_assemble_json.go:120.
func nodeToAssembleNode(n *knowledgev1.Node) assembleNode {
	return assembleNode{
		ID:          n.Id,
		Name:        n.SymbolName,
		Type:        n.Type,
		Status:      n.Status,
		Description: n.Description,
		Metadata:    nonEmptyMeta(n),
	}
}

// nonEmptyMeta returns the node's metadata map only if it has
// entries. Verbatim port of tools_assemble_json.go:132.
func nonEmptyMeta(n *knowledgev1.Node) map[string]string {
	if len(n.Metadata) == 0 {
		return nil
	}
	return n.Metadata
}
