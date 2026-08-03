// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_examine.go is the client-side claim for query(mode:examine)
// over an arbitrary node — the get-by-id + edge-neighborhood + CONTAINS-backward
// ancestry recipe. Composed over the routed deps.GraphCaller().Execute with a
// BOUNDED number of calls:
//
//   - subject ByID                                       (1 Execute)
//   - edge-neighborhood RETURN_MODE_EDGES over the id     (1 Execute)
//   - ancestry: up to 5 single-hop CONTAINS-backward      (≤5 Execute, bounded)
//     traversals collecting parent IDs in order
//   - ONE bulk ids[] hydrate over peers ∪ ancestors       (1 Execute)
//
// The bulk hydrate is the no-N+1 guarantee: peer + ancestor names/types are
// resolved in a SINGLE Execute regardless of edge/ancestor cardinality. The
// per-hop ancestry walk is bounded at 5 (parity with the server) — that bound
// is the design, not an N+1 over edges.
//
// examine targets the knowledge/default graph (the server used store.Store()).
// Self-contained per the pinned decision: the composer builds its own QueryPlans
// rather than exporting the engine's unexported bulkHydratePeers/composeEdgeSummary.

const examineAncestryMaxHops = 5

// InterceptQueryExamine claims query(mode:examine). Returns (false,_) for any
// other tool or mode so the next chain step takes over with the original params.
func InterceptQueryExamine(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "examine" {
		return false, kgtools.ToolResult{}
	}
	// Graph gate: the server handleInspectNode is hard-coded to store.Store()
	// (knowledge graph). For non-knowledge graphs the server routeQueryByMode
	// falls through to handleGenericGraphQuery against the per-source DB — keep
	// that fall-through by only claiming knowledge/default here. This mirrors the
	// InterceptQueryExamineProjects graph gate (it runs first for project-domain
	// node types; this general intercept claims every OTHER knowledge node type).
	if a.Graph != "" && a.Graph != "knowledge" {
		return false, kgtools.ToolResult{}
	}
	id := a.ID
	if id == "" {
		return true, errorResult("examine: 'id' is required")
	}
	if err := accountQueryParams(armExamine, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("examine: graph client unavailable")
	}

	data, found, err := composeInspectData(ctx, gc.Execute, id)
	if err != nil {
		return true, errorResult("examine failed: " + err.Error())
	}
	if !found {
		return true, errorResult("node " + id + " not found")
	}

	if a.Format == "json" {
		return true, jsonResult(buildInspectJSON(data))
	}
	return true, textResult(engine.RenderInspectNode(data))
}

// composeInspectData runs the bounded composition described on
// InterceptQueryExamine. Returns found=false when the subject node does not
// resolve. The knowledge/default graph is the target (nil GraphSelector — the
// server resolves it to store.Store()).
func composeInspectData(ctx context.Context, exec engine.ExecuteFn, id string) (engine.InspectData, bool, error) {
	// (1) Subject node.
	subjResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: id}},
	})
	if err != nil {
		return engine.InspectData{}, false, err
	}
	subjNodes, err := examineDecodeNodes(subjResp)
	if err != nil {
		return engine.InspectData{}, false, err
	}
	if len(subjNodes) == 0 {
		return engine.InspectData{}, false, nil
	}
	data := engine.InspectData{Node: subjNodes[0]}

	// (2) Edge neighborhood — RETURN_MODE_EDGES over the subject (both directions).
	edgesResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			ById:       id,
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		}},
	})
	if err != nil {
		return engine.InspectData{}, false, err
	}
	rawEdges := examineDecodeEdges(edgesResp)

	// (3) Ancestry — up to 5 single-hop CONTAINS-backward traversals collecting
	// parent IDs in depth order.
	ancestorIDs, err := walkAncestry(ctx, exec, id)
	if err != nil {
		return engine.InspectData{}, false, err
	}

	// Collect every id that needs name/type resolution: edge peers + ancestors.
	idSet := make(map[string]struct{}, len(rawEdges)+len(ancestorIDs))
	for i := range rawEdges {
		idSet[edgePeer(&rawEdges[i], id)] = struct{}{}
	}
	for _, aid := range ancestorIDs {
		idSet[aid] = struct{}{}
	}

	// (4) ONE bulk ids[] hydrate over the combined set.
	peers, err := examineBulkHydrate(ctx, exec, idSet)
	if err != nil {
		return engine.InspectData{}, false, err
	}

	data.Edges = shapeInspectEdges(rawEdges, id, peers)
	data.Ancestry = shapeInspectAncestry(ancestorIDs, peers)
	return data, true, nil
}

// walkAncestry issues up to examineAncestryMaxHops single-hop CONTAINS-backward
// traversals, returning the ordered parent IDs (closest-first). Stops at the
// first hop with no parent (root reached) — the orphan case returns nil.
func walkAncestry(ctx context.Context, exec engine.ExecuteFn, id string) ([]string, error) {
	var ancestorIDs []string
	current := id
	for range examineAncestryMaxHops {
		f := false // backward (in) along CONTAINS.
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection:  &knowledgev1.Selection{FromId: []string{current}, EdgeTypes: []string{string(kgtypes.EdgeKGContains)}},
				Forward:    &f,
				MaxHops:    1,
				ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
			}},
		})
		if err != nil {
			return nil, err
		}
		results, derr := examineDecodeTraversal(resp)
		if derr != nil {
			return nil, derr
		}
		parentID := firstParentID(results, current)
		if parentID == "" {
			break
		}
		ancestorIDs = append(ancestorIDs, parentID)
		current = parentID
	}
	return ancestorIDs, nil
}

// firstParentID extracts the nearest CONTAINS-parent ID from a single-hop
// traversal result set, skipping the start node itself (a depth-0 self entry).
func firstParentID(results []engine.TraversalResult, start string) string {
	for _, r := range results {
		if r.Node.Id == "" || r.Node.Id == start {
			continue
		}
		return r.Node.Id
	}
	return ""
}

// edgePeer returns the peer ID for an edge relative to the subject node: ToID
// when the subject is the source (outgoing), else FromID (incoming).
func edgePeer(e *knowledgev1.Edge, subjectID string) string {
	if e.FromId == subjectID {
		return e.ToId
	}
	return e.FromId
}

// shapeInspectEdges shapes raw edges into ordered InspectEdges (outgoing first,
// then incoming), resolving peer type/name from the bulk-hydrate map. Mirrors
// the server buildInspectJSON edge ordering (out edges, then in edges).
func shapeInspectEdges(rawEdges []knowledgev1.Edge, subjectID string, peers map[string]*knowledgev1.Node) []engine.InspectEdge {
	var out, in []engine.InspectEdge
	for i := range rawEdges {
		e := &rawEdges[i]
		peerID := edgePeer(e, subjectID)
		row := engine.InspectEdge{Type: e.Type, Peer: peerID}
		if peer, ok := peers[peerID]; ok {
			row.PeerType = peer.Type
			row.PeerName = peer.SymbolName
		}
		if e.FromId == subjectID {
			row.Direction = "out"
			out = append(out, row)
		} else {
			row.Direction = "in"
			in = append(in, row)
		}
	}
	return append(out, in...)
}

// shapeInspectAncestry resolves the ordered ancestor IDs into depth-tagged
// InspectAncestors via the bulk-hydrate map (DepthAbove 1 = direct parent).
func shapeInspectAncestry(ancestorIDs []string, peers map[string]*knowledgev1.Node) []engine.InspectAncestor {
	if len(ancestorIDs) == 0 {
		return nil
	}
	ancestry := make([]engine.InspectAncestor, 0, len(ancestorIDs))
	for i, aid := range ancestorIDs {
		anc := engine.InspectAncestor{ID: aid, DepthAbove: i + 1}
		if p, ok := peers[aid]; ok {
			anc.Name = p.SymbolName
			anc.Type = p.Type
			anc.Status = p.Status
		}
		ancestry = append(ancestry, anc)
	}
	return ancestry
}

// examineBulkHydrate issues ONE Execute (QueryPlan{Ids:...}) over the combined
// peer+ancestor id set and returns an id→Node map. Empty set → no Execute.
func examineBulkHydrate(ctx context.Context, exec engine.ExecuteFn, idSet map[string]struct{}) (map[string]*knowledgev1.Node, error) {
	if len(idSet) == 0 {
		return map[string]*knowledgev1.Node{}, nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids}},
	})
	if err != nil {
		return nil, err
	}
	nodes, derr := examineDecodeNodes(resp)
	if derr != nil {
		return nil, derr
	}
	out := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		out[n.Id] = n
	}
	return out, nil
}

// examineDecodeNodes / examineDecodeEdges / examineDecodeTraversal decode the
// ExecuteResponse carriers. Local, self-contained (the engine's decoders are
// unexported); examineDecodeNodes reads the typed Nodes carrier directly (T5/
// T5 deleted the nodes_json blob). The error return is always nil now that
// the blob json.Unmarshal is gone, but is retained for the symmetric (nodes,
// err) call shape both callers use alongside examineDecodeTraversal.
//
//nolint:unparam // symmetric decode-family signature; error retained for parity with examineDecodeTraversal
func examineDecodeNodes(resp *knowledgev1.ExecuteResponse) ([]*knowledgev1.Node, error) {
	return resp.GetNodes(), nil
}

func examineDecodeEdges(resp *knowledgev1.ExecuteResponse) []knowledgev1.Edge {
	return engine.EdgesFromProto(resp.GetEdges())
}

func examineDecodeTraversal(resp *knowledgev1.ExecuteResponse) ([]engine.TraversalResult, error) {
	return engine.DecodeTraversal(resp)
}

// buildInspectJSON reproduces the server buildInspectJSON map[string]any payload
// (tools_query_inspect.go) from the composed InspectData: the node header keys,
// the depth-tagged ancestry list, and the direction-tagged edge list.
func buildInspectJSON(data engine.InspectData) map[string]any {
	node := data.Node
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

	type ancestorRow struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Status     string `json:"status"`
		DepthAbove int    `json:"depth_above"`
	}
	ancestry := make([]ancestorRow, 0, len(data.Ancestry))
	for _, a := range data.Ancestry {
		ancestry = append(ancestry, ancestorRow{
			ID: a.ID, Name: a.Name, Type: a.Type, Status: a.Status, DepthAbove: a.DepthAbove,
		})
	}
	out["ancestry"] = ancestry

	type edgeRow struct {
		Direction string `json:"direction"`
		Type      string `json:"type"`
		Peer      string `json:"peer"`
		PeerType  string `json:"peer_type,omitempty"`
		PeerName  string `json:"peer_name,omitempty"`
	}
	edges := make([]edgeRow, 0, len(data.Edges))
	for _, e := range data.Edges {
		edges = append(edges, edgeRow{
			Direction: e.Direction, Type: e.Type, Peer: e.Peer, PeerType: e.PeerType, PeerName: e.PeerName,
		})
	}
	out["edges"] = edges
	return out
}
