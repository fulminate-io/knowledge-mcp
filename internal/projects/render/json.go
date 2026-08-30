// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// assembleNode is a recursive tree node for JSON assemble output.
// Ported from cmd/knowledge-server/tools/tools_assemble_json.go:15,
// plus UpdatedAt: the JSON counterpart of the text renders'
// updatedSuffix, following the by-id convention (raw int64 unix nanos,
// key omitted when zero — cmd/knowledge/internal/tools/
// intercept_query_examine.go:299-304).
type assembleNode struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      string            `json:"status,omitempty"`
	Description string            `json:"description,omitempty"`
	UpdatedAt   int64             `json:"updated_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Children    []assembleNode    `json:"children,omitempty"`
}

// assembleJSON builds a recursive tree from any node and returns it
// as JSON. Universal JSON path for assemble — walks EdgeKGContains
// children recursively (same hierarchy as the text renders) and includes
// linked research/decisions as extra top-level fields.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_json.go:29
// with the store reads swapped for wire-shape calls; the whole subtree now
// arrives from one AssembleSubtree traversal, so the recursion below issues no
// wire call at all.
//
// THE ARM DISCLOSES TRUNCATION TWICE, AND BOTH ARE INTENTIONAL. The `truncated`
// key goes on the ENVELOPE ROOT, unconditionally for both true and false,
// because truncation is a property of the READ and not of a node — a per-row
// key would say nothing and would inflate exactly the large payloads where
// truncation matters most, and an absent key is indistinguishable from an old
// binary. The prose notice rides alongside as its own block. The key is what a
// machine reads; the block is what a caller reads.
func assembleJSON(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) kgtools.ToolResult {
	childIndex, byID, _, truncated := AssembleSubtree(ctx, gc, node.Id, 5)
	tree := buildAssembleTree(node, 0, 5, childIndex)
	result := map[string]any{"root": tree, "truncated": truncated}

	research, decisions, linkedTruncated := collectLinkedNodes(ctx, gc, node.Id)
	truncated = truncated || linkedTruncated
	result["truncated"] = truncated
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
	return AppendTruncationNotice(kgtools.TextResult(string(b)), truncated, len(byID))
}

// buildAssembleTree recursively builds a tree of assembleNode from the
// prefetched parent→child index. It takes no graph caller, which is the
// structural guarantee that the recursion cannot issue a wire call.
//
// CHILDREN-KEY CONTRACT: Children is `omitempty`, so a node with no children
// emits NO children key at all. The index path preserves that naturally —
// childIndex holds no entry for a childless parent, so nothing is appended and
// omitempty drops the key.
func buildAssembleTree(
	node *knowledgev1.Node,
	depth, maxDepth int,
	childIndex map[string][]*knowledgev1.Node,
) assembleNode {
	an := assembleNode{
		ID:          node.Id,
		Name:        node.SymbolName,
		Type:        node.Type,
		Status:      node.Status,
		Description: node.Description,
		UpdatedAt:   node.UpdatedAt,
		Metadata:    nonEmptyMeta(node),
	}

	if depth >= maxDepth {
		return an
	}

	for _, cn := range childIndex[node.Id] {
		an.Children = append(an.Children, buildAssembleTree(cn, depth+1, maxDepth, childIndex))
	}

	return an
}

// collectLinkedNodes finds research and decision nodes linked to
// nodeID via EdgeInformedBy (outgoing for research, incoming for
// decisions). Ported from tools_assemble_json.go:79; both sides share ONE bulk
// hydrate, and each renders by walking its own EDGE slice so the emitted arrays
// keep edge order rather than the hydrated map's undefined one. The third
// return is the hydrate's truncation verdict.
func collectLinkedNodes(ctx context.Context, gc GraphCaller, nodeID string) ([]assembleNode, []assembleNode, bool) {
	var research, decisions []assembleNode

	outEdges, _ := IterEdges(ctx, gc, nodeID, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy)
	inEdges, _ := IterEdges(ctx, gc, nodeID, kgwire.IncomingEdges, kgtypes.EdgeInformedBy)

	peerIDs := make([]string, 0, len(outEdges)+len(inEdges))
	for _, e := range outEdges {
		peerIDs = append(peerIDs, e.ToId)
	}
	for _, e := range inEdges {
		peerIDs = append(peerIDs, e.FromId)
	}
	peers, truncated, _ := FetchNodesByIDs(ctx, gc, peerIDs)

	for _, e := range outEdges {
		if n, ok := peers[e.ToId]; ok && kgtypes.NodeType(n.Type) == kgtypes.NodeResearch {
			research = append(research, nodeToAssembleNode(n))
		}
	}
	for _, e := range inEdges {
		if n, ok := peers[e.FromId]; ok && kgtypes.NodeType(n.Type) == kgtypes.NodeDecision {
			decisions = append(decisions, nodeToAssembleNode(n))
		}
	}

	return research, decisions, truncated
}

// nodeToAssembleNode converts a wire node to a flat assembleNode
// (no children). Ported from tools_assemble_json.go:120, plus the
// UpdatedAt carry that buildAssembleTree also performs.
func nodeToAssembleNode(n *knowledgev1.Node) assembleNode {
	return assembleNode{
		ID:          n.Id,
		Name:        n.SymbolName,
		Type:        n.Type,
		Status:      n.Status,
		Description: n.Description,
		UpdatedAt:   n.UpdatedAt,
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
