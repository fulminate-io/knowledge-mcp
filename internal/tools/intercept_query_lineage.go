// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryLineage ports the server-side
// handleTraceLineage + buildLineageJSON + formatLineageNode handlers
// client-side. Claims query(mode:"lineage").
//
// Walks the provenance chain upward from a node via three edge
// probes per step (reverse contains, reverse implements, forward
// informed-by). Up to depth 10. Markdown + JSON formats both
// preserved.
//
// FUL-251b Phase 3: must be wired BEFORE Phase 5 deletes the
// server-side lineage shortcut.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// InterceptQueryLineage claims query(mode:"lineage"). Returns
// (true, result) on match.
func InterceptQueryLineage(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "lineage" {
		return false, kgtools.ToolResult{}
	}
	if a.ID == "" {
		return true, errorResult("lineage mode requires 'id' parameter")
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("lineage: graph caller unavailable")
	}

	ctx := context.Background()
	node, err := render.FetchNode(ctx, gc, a.ID)
	if err != nil {
		return true, errorResult(fmt.Sprintf("query failed: %s", err))
	}
	if node == nil {
		return true, errorResult(fmt.Sprintf("node %s not found", a.ID))
	}

	if a.Format == "json" {
		return true, jsonResult(buildLineageJSON(ctx, gc, node))
	}
	return true, kgtools.TextResult(renderLineageMarkdown(ctx, gc, node))
}

// renderLineageMarkdown ports handleTraceLineage's markdown branch
// at tools_knowledge_query.go:296-355.
func renderLineageMarkdown(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Lineage for: %s (%s)\n", node.SymbolName, node.Type)
	if node.Summary != "" {
		fmt.Fprintf(&sb, "  %s\n", node.Summary)
	}
	fmt.Fprintf(&sb, "  ID: %s\n", node.Id)

	current := node.Id
	depth := 0
	seen := map[string]bool{current: true}
	for depth < 10 {
		depth++
		parentID, edgeType := nextLineageParent(ctx, gc, current)
		if parentID == "" || seen[parentID] {
			break
		}
		seen[parentID] = true
		parent, ferr := render.FetchNode(ctx, gc, parentID)
		if ferr != nil || parent == nil {
			break
		}
		formatLineageNode(&sb, parent, depth, edgeType)
		current = parentID
	}
	if depth == 1 {
		sb.WriteString("\n  (no lineage found — this is a root node)")
	}
	return sb.String()
}

// nextLineageParent attempts the three edge probes the server-side
// handler uses (reverse contains → reverse implements → forward
// informed-by) and returns the first hit (parentID, edgeType). Empty
// strings = no parent on this hop.
func nextLineageParent(ctx context.Context, gc GraphCaller, current string) (string, string) {
	if edges, _ := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGContains); len(edges) > 0 {
		return edges[0].FromId, "contains"
	}
	if edges, _ := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGImplements); len(edges) > 0 {
		return edges[0].FromId, "implements"
	}
	if edges, _ := render.IterEdges(ctx, gc, current, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy); len(edges) > 0 {
		return edges[0].ToId, "informed-by"
	}
	return "", ""
}

// formatLineageNode ports formatLineageNode at
// tools_knowledge_query.go:359-371.
func formatLineageNode(sb *strings.Builder, parent *knowledgev1.Node, depth int, edgeType string) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(sb, "\n%s↑ %s\n", indent, edgeType)
	fmt.Fprintf(sb, "%s%s (%s)", indent, parent.SymbolName, parent.Type)
	if parent.Status != "" {
		fmt.Fprintf(sb, " [%s]", parent.Status)
	}
	sb.WriteString("\n")
	if parent.Summary != "" {
		fmt.Fprintf(sb, "%s  %s\n", indent, parent.Summary)
	}
	fmt.Fprintf(sb, "%s  ID: %s\n", indent, parent.Id)
}

// lineageRow mirrors the server-side JSON payload at
// tools_knowledge_query.go:193-200.
type lineageRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Summary  string `json:"summary,omitempty"`
	EdgeType string `json:"edge_type"`
	Depth    int    `json:"depth"`
}

// buildLineageJSON ports buildLineageJSON
// (tools_knowledge_query.go:192-246) with three store.Store() calls
// replaced by render.IterEdges.
func buildLineageJSON(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) map[string]any {
	out := map[string]any{
		"start": map[string]any{
			"id":      node.Id,
			"name":    node.SymbolName,
			"type":    node.Type,
			"summary": node.Summary,
		},
	}
	chain := make([]lineageRow, 0, 10)
	current := node.Id
	depth := 0
	seen := map[string]bool{current: true}
	for depth < 10 {
		depth++
		parentID, edgeType := nextLineageParent(ctx, gc, current)
		if parentID == "" || seen[parentID] {
			break
		}
		seen[parentID] = true
		parent, ferr := render.FetchNode(ctx, gc, parentID)
		if ferr != nil || parent == nil {
			break
		}
		chain = append(chain, lineageRow{
			ID: parent.Id, Name: parent.SymbolName, Type: parent.Type,
			Summary: parent.Summary, EdgeType: edgeType, Depth: depth,
		})
		current = parentID
	}
	out["lineage"] = chain
	return out
}
