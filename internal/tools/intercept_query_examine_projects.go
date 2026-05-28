// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryExamineProjects ports the server-side
// handleInspectNode handler client-side for the 11 project-domain
// types. Claims query(mode:"examine") when the target node type is
// in the project-domain set AND the graph is unset / "knowledge".
//
// Non-knowledge graphs fall through (return false). Non-project-domain
// types (e.g. NodeFile, NodeCloudResource, NodeThought) also fall
// through to the server's existing handleInspectNode path which
// renders the generic header/ancestry/edges view (or delegates to
// handleExamine for thoughts).
//
// FUL-251b Phase 4: must be wired BEFORE Phase 5 introduces the
// project-domain type-switch in handleInspectNode that returns
// the relocated client-side intercepts.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// projectDomainTypes is the set of node types the client-side
// intercept claims. Mirrors the type-switch Phase 5 introduces in
// handleInspectNode.
var projectDomainTypes = map[kgtypes.NodeType]struct{}{
	kgtypes.NodeProject:   {},
	kgtypes.NodeTicket:    {},
	kgtypes.NodePlan:      {},
	kgtypes.NodePhase:     {},
	kgtypes.NodeStep:      {},
	kgtypes.NodeCriterion: {},
	kgtypes.NodeResearch:  {},
	kgtypes.NodeQuestion:  {},
	kgtypes.NodeFinding:   {},
	kgtypes.NodeDecision:  {},
	kgtypes.NodeRule:      {},
}

// InterceptQueryExamineProjects claims query(mode:"examine") for
// project-domain types on the knowledge graph.
func InterceptQueryExamineProjects(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "examine" || a.ID == "" {
		return false, kgtools.ToolResult{}
	}
	// Graph gate: handleInspectNode is hard-coded to store.Store()
	// (knowledge graph). For non-knowledge graphs, the server's
	// routeQueryByMode at tools_query.go:172-174 falls through to
	// handleGenericGraphQuery which reads from the correct per-source
	// DB via resolveGraphDB. This intercept only claims when graph is
	// unset or "knowledge" to preserve that fall-through path.
	if a.Graph != "" && a.Graph != "knowledge" {
		return false, kgtools.ToolResult{}
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return false, kgtools.ToolResult{}
	}

	ctx := context.Background()
	node, err := render.FetchNode(ctx, gc, a.ID)
	if err != nil || node == nil {
		// Let the server emit its canonical not-found error.
		return false, kgtools.ToolResult{}
	}
	if _, ok := projectDomainTypes[kgtypes.NodeType(node.Type)]; !ok {
		// Non-project-domain types (NodeFile, NodeThought, etc.) stay
		// on the server-side handleInspectNode path.
		return false, kgtools.ToolResult{}
	}

	if a.Format == "json" {
		return true, jsonResult(buildExamineJSON(ctx, gc, node))
	}
	var sb strings.Builder
	renderExamineHeader(&sb, node)
	renderExamineAncestry(ctx, gc, &sb, node.Id)
	renderExamineEdges(ctx, gc, &sb, node.Id)
	return true, kgtools.TextResult(sb.String())
}

// renderExamineHeader ports renderInspectHeader at
// cmd/knowledge-server/tools/tools_query_inspect.go:132-152.
func renderExamineHeader(sb *strings.Builder, node *knowledgev1.Node) {
	name := node.SymbolName
	if name == "" {
		name = node.Id
	}
	fmt.Fprintf(sb, "# Inspect: %s\n\n", name)
	fmt.Fprintf(sb, "## Composite View\n")
	fmt.Fprintf(sb, "- **ID:** %s\n", node.Id)
	fmt.Fprintf(sb, "- **Type:** %s\n", node.Type)
	fmt.Fprintf(sb, "- **Status:** %s\n", node.Status)
	if node.Source != "" {
		fmt.Fprintf(sb, "- **Source:** %s\n", node.Source)
	}
	if node.CreatedAt != 0 {
		fmt.Fprintf(sb, "- **Created:** %s\n", time.Unix(0, node.CreatedAt).Format("2006-01-02 15:04"))
	}
	if node.UpdatedAt != 0 {
		fmt.Fprintf(sb, "- **Updated:** %s\n", time.Unix(0, node.UpdatedAt).Format("2006-01-02 15:04"))
	}
	sb.WriteString("\n")
}

// renderExamineAncestry ports renderInspectAncestry at
// tools_query_inspect.go:155-183.
func renderExamineAncestry(ctx context.Context, gc GraphCaller, sb *strings.Builder, id string) {
	fmt.Fprintf(sb, "## Ancestry\n")
	current := id
	depth := 0
	for depth < 5 {
		edges, perr := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGContains)
		if perr != nil || len(edges) == 0 {
			break
		}
		parent, gerr := render.FetchNode(ctx, gc, edges[0].FromId)
		if gerr != nil || parent == nil {
			break
		}
		depth++
		pName := parent.SymbolName
		if pName == "" {
			pName = parent.Id
		}
		indent := strings.Repeat("  ", depth-1)
		fmt.Fprintf(sb, "%s← [%s] %s (status: %s, id: %s)\n", indent, parent.Type, pName, parent.Status, parent.Id[:12])
		current = parent.Id
	}
	if depth == 0 {
		sb.WriteString("(no parent — orphan node)\n")
	}
	sb.WriteString("\n")
}

// renderExamineEdges ports renderInspectEdges at
// tools_query_inspect.go:186-224.
func renderExamineEdges(ctx context.Context, gc GraphCaller, sb *strings.Builder, id string) {
	fmt.Fprintf(sb, "## Edges\n")
	outEdges, _ := render.IterEdges(ctx, gc, id, kgwire.OutgoingEdges)
	inEdges, _ := render.IterEdges(ctx, gc, id, kgwire.IncomingEdges)
	if len(outEdges) == 0 && len(inEdges) == 0 {
		sb.WriteString("(no edges)\n")
	}
	for _, e := range outEdges {
		target, qerr := render.FetchNode(ctx, gc, e.ToId)
		if qerr != nil || target == nil {
			fmt.Fprintf(sb, "  → [%s] [missing] %s (dangling edge)\n", e.Type, e.ToId[:min(12, len(e.ToId))])
			continue
		}
		tName := target.SymbolName
		if tName == "" {
			tName = e.ToId[:min(12, len(e.ToId))]
		}
		fmt.Fprintf(sb, "  → [%s] [%s] %s\n", e.Type, target.Type, tName)
	}
	for _, e := range inEdges {
		source, qerr := render.FetchNode(ctx, gc, e.FromId)
		if qerr != nil || source == nil {
			fmt.Fprintf(sb, "  ← [%s] [missing] %s (dangling edge)\n", e.Type, e.FromId[:min(12, len(e.FromId))])
			continue
		}
		sName := source.SymbolName
		if sName == "" {
			sName = e.FromId[:min(12, len(e.FromId))]
		}
		fmt.Fprintf(sb, "  ← [%s] [%s] %s\n", e.Type, source.Type, sName)
	}
	sb.WriteString("\n")
}

// examineEdgeRow / examineAncestor mirror the server-side JSON
// payload shape at tools_query_inspect.go:80-110.
type examineAncestor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	DepthAbove int    `json:"depth_above"`
}

type examineEdgeRow struct {
	Direction string `json:"direction"`
	Type      string `json:"type"`
	Peer      string `json:"peer"`
	PeerType  string `json:"peer_type,omitempty"`
	PeerName  string `json:"peer_name,omitempty"`
}

// buildExamineJSON ports buildInspectJSON at
// tools_query_inspect.go:57-129. Pure wire-fetch substitutions.
func buildExamineJSON(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) map[string]any {
	out := map[string]any{
		"id":     node.Id,
		"name":   node.SymbolName,
		"type":   node.Type,
		"status": node.Status,
		"source": node.Source,
	}
	if node.CreatedAt != 0 {
		out["created_at"] = node.CreatedAt
	}
	if node.UpdatedAt != 0 {
		out["updated_at"] = node.UpdatedAt
	}
	if node.Description != "" {
		out["description"] = node.Description
	}
	if len(node.Metadata) > 0 {
		out["metadata"] = node.Metadata
	}

	var ancestry []examineAncestor
	current := node.Id
	for depth := 1; depth <= 5; depth++ {
		edges, perr := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGContains)
		if perr != nil || len(edges) == 0 {
			break
		}
		parent, gerr := render.FetchNode(ctx, gc, edges[0].FromId)
		if gerr != nil || parent == nil {
			break
		}
		ancestry = append(ancestry, examineAncestor{
			ID: parent.Id, Name: parent.SymbolName, Type: parent.Type,
			Status: parent.Status, DepthAbove: depth,
		})
		current = parent.Id
	}
	out["ancestry"] = ancestry

	var edges []examineEdgeRow
	outE, _ := render.IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges)
	for _, e := range outE {
		row := examineEdgeRow{Direction: "out", Type: e.Type, Peer: e.ToId}
		if peer, perr := render.FetchNode(ctx, gc, e.ToId); perr == nil && peer != nil {
			row.PeerType = peer.Type
			row.PeerName = peer.SymbolName
		}
		edges = append(edges, row)
	}
	inE, _ := render.IterEdges(ctx, gc, node.Id, kgwire.IncomingEdges)
	for _, e := range inE {
		row := examineEdgeRow{Direction: "in", Type: e.Type, Peer: e.FromId}
		if peer, perr := render.FetchNode(ctx, gc, e.FromId); perr == nil && peer != nil {
			row.PeerType = peer.Type
			row.PeerName = peer.SymbolName
		}
		edges = append(edges, row)
	}
	out["edges"] = edges
	return out
}
