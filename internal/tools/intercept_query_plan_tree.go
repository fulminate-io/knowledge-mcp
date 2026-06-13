// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryPlanTree dispatches client-side
// query(mode:plan_tree) calls to a single subtree traversal, then
// assembles the rendered text or json client-side from the flat
// node+edge result via render.BuildChildIndex. The root node is fetched
// once for the exact not-found error and the root row; the rest of the
// subtree rides one traversal RPC, so the cost is O(depth) RPCs rather
// than O(nodes).

package tools

import (
	"context"
	"encoding/json"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

	// One subtree traversal serves BOTH formats: it returns the whole
	// descendant node set plus the subtree's contains edges, from which
	// the parent→child index is assembled client-side with no per-node
	// fetch. edgeTypes[0] is the single structure edge type (the wire
	// EdgeType field is singular — see the fallback comment above).
	nodes, structureEdges, terr := TraverseDescendantsWithEdges(ctx, gc, a.ID, edgeTypes[0], depth)
	if terr != nil {
		return true, errorResult("plan_tree: " + terr.Error())
	}
	childIndex, _ := render.BuildChildIndex(a.ID, nodes, structureEdges)

	if a.Format == "json" {
		return true, jsonResult(buildPlanTreeJSON(node, 0, depth, childIndex))
	}

	// Text path needs depends-on ordering. Fetch every node's depends-on
	// edge in one batched RPC. A failed fetch degrades to no ordering
	// (best-effort, mirroring the per-node firstDependsOn that ignored
	// its error), rendering children in structure-edge order rather than
	// erroring the whole tree.
	allIDs := make([]string, 0, len(nodes)+1)
	allIDs = append(allIDs, a.ID)
	for _, n := range nodes {
		allIDs = append(allIDs, n.Id)
	}
	dependsOn, derr := render.FetchDependsOnEdges(ctx, gc, allIDs)
	if derr != nil {
		dependsOn = map[string]string{}
	}

	var sb strings.Builder
	render.RenderTreeFromIndex(&sb, node, 0, depth, childIndex, dependsOn)
	return true, kgtools.TextResult(sb.String())
}

// buildPlanTreeJSON renders the recursive
// {id, name, type, status, description, children} payload the server's
// handleWalk emits for format=json, reading children from a prebuilt
// parent→child index (render.BuildChildIndex) instead of a per-node
// edge+node fetch. The whole subtree is fetched in one traversal up
// front, so this recursion issues zero RPCs.
//
// Children-key contract: the children key is present only when the
// index has children for node.Id; a node with no indexed children
// returns a row WITHOUT a children key. (The previous per-node port
// emitted "children":[] in one corner case — a parent whose every
// child edge dangled to a node that failed to fetch. That case can
// only arise from a contains edge to a hard-deleted, never-tombstoned
// node; the index path produces no entry for such a parent and omits
// the key, which is the accepted contract since the dangling target
// renders nowhere either way. A tombstoned child never reaches here:
// its structure edge is dropped server-side before the index is built.)
func buildPlanTreeJSON(
	node *knowledgev1.Node,
	depth, maxDepth int,
	childIndex map[string][]*knowledgev1.Node,
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
	children := childIndex[node.Id]
	if len(children) == 0 {
		return row
	}
	rows := make([]map[string]any, 0, len(children))
	for _, child := range children {
		rows = append(rows, buildPlanTreeJSON(child, depth+1, maxDepth, childIndex))
	}
	row["children"] = rows
	return row
}
