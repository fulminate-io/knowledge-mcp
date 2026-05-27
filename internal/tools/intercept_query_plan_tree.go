// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryPlanTree dispatches client-side
// query(mode:plan_tree) calls to render.RenderTree (text) or a local
// buildPlanTreeJSON port (json). Routes through the GraphCaller wire shims
// (render.FetchNode + IterEdges) rather than store-resident reads.

package tools

import (
	"context"
	"encoding/json"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// InterceptQueryPlanTree claims query(mode:"plan_tree") and renders
// the tree client-side. Returns (handled, result). When the call is
// not a plan_tree query, returns (false, _) so the chain continues.
func InterceptQueryPlanTree(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Bad args — server will surface the canonical invalid-args
		// error; do not pre-empt.
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "plan_tree" {
		return false, kgtools.ToolResult{}
	}
	if a.ID == "" {
		// Mirror tools_query_shortcuts.go:55-57 error text exactly so
		// callers cannot distinguish server-side vs client-side
		// rejection.
		return true, errorResult("plan_tree mode requires 'id' parameter (the root plan/project/ticket ID)")
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("plan_tree: graph caller unavailable")
	}

	// Mirror tools_query_shortcuts.go:58-61: depth defaults to 10,
	// honored via a.Limit when caller supplied one.
	depth := 10
	if d := int(a.Limit); d > 0 {
		depth = d
	}

	// Mirror tools_walk.go:38-44 edge-types fallback. queryArgs's
	// singular EdgeType field (json:"edge_type") is the on-wire mirror
	// of the server-side struct — do NOT introduce an EdgeTypes alias.
	edgeTypes := []kgtypes.EdgeType{kgtypes.EdgeKGContains}
	if len(a.EdgeType) > 0 {
		edgeTypes = make([]kgtypes.EdgeType, len(a.EdgeType))
		for i, et := range a.EdgeType {
			edgeTypes[i] = kgtypes.EdgeType(et)
		}
	}

	ctx := context.Background()
	node, err := render.FetchNode(ctx, gc, a.ID)
	if err != nil {
		return true, errorResult("plan_tree: " + err.Error())
	}
	if node == nil {
		return true, errorResult("node " + a.ID + " not found")
	}

	if a.Format == "json" {
		return true, jsonResult(buildPlanTreeJSON(ctx, gc, node, 0, depth, edgeTypes))
	}

	var sb strings.Builder
	render.RenderTree(ctx, gc, &sb, node, 0, depth, edgeTypes)
	return true, kgtools.TextResult(sb.String())
}

// buildPlanTreeJSON is the local file-private port of
// cmd/knowledge-server/tools/tools_walk.go:65-90 with the store
// reads replaced by render.FetchNode + render.IterEdges. Returns the
// recursive {id, name, type, status, description, children} payload
// the server-side handleWalk emits for format=json.
func buildPlanTreeJSON(
	ctx context.Context,
	gc render.GraphCaller,
	node *knowledgev1.Node,
	depth, maxDepth int,
	edgeTypes []kgtypes.EdgeType,
) map[string]any {
	row := map[string]any{
		"id":          node.Id,
		"name":        node.SymbolName,
		"type":        node.Type,
		"status":      node.Status,
		"description": node.Description,
	}
	if depth >= maxDepth {
		return row
	}
	childEdges, err := render.IterEdges(ctx, gc, node.Id, kgwire.OutgoingEdges, edgeTypes...)
	if err != nil || len(childEdges) == 0 {
		return row
	}
	children := make([]map[string]any, 0, len(childEdges))
	for _, e := range childEdges {
		childNode, ferr := render.FetchNode(ctx, gc, e.ToId)
		if ferr != nil || childNode == nil {
			continue
		}
		children = append(children, buildPlanTreeJSON(ctx, gc, childNode, depth+1, maxDepth, edgeTypes))
	}
	row["children"] = children
	return row
}
