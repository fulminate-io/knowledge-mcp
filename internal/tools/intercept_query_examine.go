// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_examine.go is the client-side claim for query(mode:examine)
// over an arbitrary node — the get-by-id + edge-neighborhood + CONTAINS-backward
// ancestry recipe. Composed over the routed deps.GraphCaller().Execute with a
// BOUNDED number of calls:
//
//   - subject ByID                                       (1 Execute)
//   - edge-neighborhood RETURN_MODE_EDGES over the id     (bounded pages)
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
// other tool or mode so the next chain step takes over with the original
// params. A non-knowledge GRAPH is claimed too, and answered with the refusal
// below — see the graph gate for why that is not a decline.
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
	// Graph gate: this composer targets the knowledge/default graph, so it claims
	// only graph unset / "knowledge". Every other value is REFUSED here rather
	// than declined back to the chain. There is NO server-side examine handler
	// behind a decline — examine is a specialized mode, and per engine's
	// reducibleQueryModes a specialized mode is either claimed by a client
	// intercept or meets the generic engine deny (TestDefaultDeny_SpecializedShapes
	// pins that floor). Declining therefore answered a plausible command with
	// "not a recognized engine-reducible shape", which names neither examine, nor
	// the graphs examine serves, nor a way forward.
	//
	// WHICH GRAPHS REACH THIS LINE is narrower than the condition reads, and the
	// message is worded for them. cloud, cicd and linkage are claimed by
	// InterceptQueryCloudCICD / InterceptQueryPracticeLinkage, code by
	// InjectRepoIfCodeGraph, and logs by InterceptLogsQuery — all of which run
	// EARLIER in bootstrap/dream.go than the rendering cluster that holds this
	// arm, so an examine naming one of those never arrives. What does arrive is
	// practice, web, pdf, and any other graph string — a custom graph name or an
	// unrecognized one, neither of which any arm claims for a mode-bearing call.
	// Driven end-to-end in
	// TestQueryDispatchParity_ExamineNonKnowledgeGraphIsRefusedByName.
	//
	// THE REFUSAL IS A CLAIM, so chain order is load-bearing: this arm runs AFTER
	// InterceptQueryExamineProjects, which must keep winning for project-domain
	// types. It does — the refusal fires only on graph values that arm already
	// declines, and TestQueryDispatchParity_ExamineProjectDomainStillWins is the
	// control that the claim did not move in front of it.
	if a.Graph != "" && a.Graph != "knowledge" {
		return true, errorResult(examineGraphRefusal(a.Graph))
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

	// examine is an intercept, so it never reaches engine.Render and picks up no
	// notice there. Both arms disclose for themselves on the hydrate's verdict: the
	// JSON payload carries the `truncated` key and both formats carry the trailing
	// prose block. The two artifacts answer different questions and both stay.
	// The EDGE read contributes nothing here — it is complete-or-error, so a notice
	// keyed on it would be a false statement about a complete union.
	if a.Format == "json" {
		return true, engine.WithTruncationNoticeFor(
			jsonResult(buildInspectJSON(data)), data.Truncated, len(data.Edges))
	}
	return true, engine.WithTruncationNoticeFor(
		textResult(engine.RenderInspectNode(data)), data.Truncated, len(data.Edges))
}

// examineGraphRefusal is the message the graph gate returns for a graph this
// arm does not serve. It follows the promote_metadata graph refusals
// (intercept_manage_promote.go), which name the accepted vocabulary and say
// plainly what is not supported here rather than leaving the caller with a
// message about engine internals.
//
// The alternative it recommends was DRIVEN, not assumed: dropping the mode and
// issuing a by-id read reaches the engine's ById lowering for practice (with
// and without language), web, pdf and an arbitrary graph name alike — one
// Execute RPC each, against the bootstrap parity fixture.
func examineGraphRefusal(graph string) string {
	return fmt.Sprintf(
		"examine: graph %q is not supported. query(mode:\"examine\") composes the ancestry and "+
			"edge neighborhood of a KNOWLEDGE-graph node only (graph unset or \"knowledge\"). "+
			"To read a node in another graph, drop the mode and ask for it by id: "+
			"query({\"graph\":%q,\"id\":\"<id>\"}) — adding that graph's own selector where it "+
			"takes one (language for practice, name for web/pdf/logs, account for cloud/cicd) — "+
			"and traverse({\"graph\":%q,\"start\":\"<id>\"}) for its edges.",
		graph, graph, graph)
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

	// (2) Edge neighborhood — RETURN_MODE_EDGES over the subject (both
	// directions), drained in bounded pages. The plan Limit and the drain's
	// edgeCap are the same number twice on purpose: the Limit is what the server
	// enforces, the cap is what the drain uses to notice it was enforced. One
	// without the other yields a drain that never detects truncation, or one
	// that splits on a threshold nobody applies.
	// THE EDGE READ IS COMPLETE OR THIS CALL FAILS, which is why it contributes no
	// truncation verdict. paging.DrainPivotEdges never ACCEPTS a saturated page: it
	// halves the pivot set, re-reads a single pivot as a from_id band tiling,
	// splits a saturating band at its median interior id, and only a pivot no band
	// can divide returns an error naming the pivot and the ceiling — which
	// propagates out of this function and renders as an error result. Capturing an
	// intermediate page's flag and reporting it as "this result may be incomplete"
	// would be a FALSE statement about a provably complete union.
	//
	// THAT IS TRUE OF THE EDGES AND NOT OF THE COMPOSITION. Step (4) below ends in
	// one unbounded bulk hydrate over this union's peer set, which the server DOES
	// clamp — see examineBulkHydrate. Its verdict is what InspectData.Truncated
	// carries. An earlier revision of this comment certified the whole examine read
	// complete-or-loud on the strength of the edge drain alone.
	rawEdges, err := paging.DrainPivotEdges([]string{id}, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			edgesResp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:          idPage,
					ReturnMode:   knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					Limit:        int32(engine.CorrelationsEdgeScanCap),
					EdgeFromBand: paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
				}},
			})
			if rerr != nil {
				return nil, false, rerr
			}
			return examineDecodeEdges(edgesResp), edgesResp.GetTruncated(), nil
		})
	if err != nil {
		return engine.InspectData{}, false, err
	}

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
	peers, peersTruncated, err := examineBulkHydrate(ctx, exec, idSet)
	if err != nil {
		return engine.InspectData{}, false, err
	}
	data.Truncated = peersTruncated

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
//
// THE SECOND RETURN IS THE SERVER'S TRUNCATION VERDICT, and it is the one read in
// this composition that can actually be clamped. The plan carries NO Limit, and
// the id set is unbounded by construction: it is the peer set of a drained edge
// union, and the drain's band split exists precisely to serve a pivot above the
// 50,000-row edge cap. executeTruncation flags
// `len(p.GetIds()) > maxExecuteNodeRows` (10,000) on the REQUEST alone. Every
// unreturned id then renders with an empty name/type — for an edge peer,
// indistinguishable from a peer that genuinely has none; for an ancestry row, an
// empty rung in the chain.
func examineBulkHydrate(ctx context.Context, exec engine.ExecuteFn, idSet map[string]struct{}) (map[string]*knowledgev1.Node, bool, error) {
	if len(idSet) == 0 {
		return map[string]*knowledgev1.Node{}, false, nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids}},
	})
	if err != nil {
		return nil, false, err
	}
	nodes, derr := examineDecodeNodes(resp)
	if derr != nil {
		return nil, false, derr
	}
	out := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		out[n.Id] = n
	}
	return out, resp.GetTruncated(), nil
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
	// UNCONDITIONAL, so a consumer never special-cases this arm: an absent key is
	// indistinguishable from an old binary, and `truncated: false` is a positive
	// statement of completeness.
	//
	// THE VALUE IS LIVE, read from the composition's own reads rather than pinned.
	// An earlier revision emitted a constant false on the grounds that the edge
	// drain is complete-or-loud. That is true of the EDGE read and was never true
	// of the composition: the bulk peer+ancestor hydrate is an unbounded
	// QueryPlan{Ids} the server clamps above 10,000 ids, and a clamped hydrate
	// leaves peers and ancestry rows with empty names — the exact silent narrowing
	// this key exists to make visible. InspectData.Truncated carries that verdict.
	out["truncated"] = data.Truncated

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
