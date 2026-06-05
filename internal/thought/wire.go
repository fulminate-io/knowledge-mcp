// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/graph"
)

// Caller is the narrow Execute-only seam every thought-package wire helper takes.
// Both *graphclient.GraphClient (always-local) and *graphclient.Router (routing-
// aware) satisfy this implicitly, so the helpers route per-call without
// dragging the concrete client type into the function signatures. Mirrors the
// tools.GraphCaller interface, kept package-local so the thought package stays
// import-clean of the higher-level tools package.
type Caller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// executeViaEngine compiles a generic tool call (query / traverse / search) to a
// declarative ExecuteRequest and runs it through the GraphClient.Execute carrier
// seam — the same Compile→Execute path the bootstrap chokepoint
// and wire_persist use. Returns a typed error when the args are not reducible
// (should not happen for the fixed internal shapes the thought helpers build).
func executeViaEngine(ctx context.Context, gc Caller, tool string, args json.RawMessage) (*knowledgev1.ExecuteResponse, error) {
	req, ok := engine.Compile(tool, args)
	if !ok {
		return nil, fmt.Errorf("thought: %s args not reducible to an ExecuteRequest", tool)
	}
	return gc.Execute(ctx, req)
}

// fetchChargesFor composes the per-thought charge map CLIENT-SIDE, reproducing
// the server's handleChargesFor (a PURE read: per-thought outgoing-EdgeChargedBy
// walk + charge hydration, zero compute). Two Execute calls regardless of
// |thoughtIDs| — strictly better than the server's N IterEdges + 1 IterateAll:
//
//  1. ONE bulk RETURN_MODE_EDGES read over the thought-id node set filtered to
//     EdgeChargedBy; collect the ToID charge IDs per thought (FromID is the
//     thought, ToID the charge — EdgeChargedBy is thought_parent→charge).
//  2. ONE bulk fetchNodesByIDs hydrate of the collected charge IDs.
//
// Join in caller order, omitting thoughts with no charges (matching
// handleChargesFor lines 86-97). Empty input → empty map.
func fetchChargesFor(ctx context.Context, gc Caller, thoughtIDs []string) map[string][]*knowledgev1.Node {
	out := map[string][]*knowledgev1.Node{}
	if gc == nil || len(thoughtIDs) == 0 {
		return out
	}
	inSet := make(map[string]bool, len(thoughtIDs))
	for _, tid := range thoughtIDs {
		inSet[tid] = true
	}

	edges, err := fetchEdgesForNodeSet(ctx, gc, thoughtIDs, []kgtypes.EdgeType{kgtypes.EdgeChargedBy})
	if err != nil {
		slog.Warn("thought: fetchChargesFor: bulk edges failed", "err", err)
		return out
	}

	// Collect charge IDs per thought (only edges whose FromID is a requested
	// thought — the outgoing-EdgeChargedBy direction the server walks).
	thoughtToChargeIDs := make(map[string][]string, len(thoughtIDs))
	var allChargeIDs []string
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeChargedBy || !inSet[e.FromId] {
			continue
		}
		thoughtToChargeIDs[e.FromId] = append(thoughtToChargeIDs[e.FromId], e.ToId)
		allChargeIDs = append(allChargeIDs, e.ToId)
	}
	if len(allChargeIDs) == 0 {
		return out
	}

	chargeByID := fetchNodesByIDs(ctx, gc, allChargeIDs)

	// Join in caller order; omit thoughts with no (hydratable) charges. Missing
	// charge IDs (tombstoned/deleted) are silently dropped, matching the server.
	for _, tid := range thoughtIDs {
		chargeIDs := thoughtToChargeIDs[tid]
		charges := make([]*knowledgev1.Node, 0, len(chargeIDs))
		for _, cid := range chargeIDs {
			if c, ok := chargeByID[cid]; ok {
				charges = append(charges, c)
			}
		}
		if len(charges) > 0 {
			out[tid] = charges
		}
	}
	return out
}

// fetchNodesByIDs hydrates a slice of node IDs in one Execute round-trip
// (query{ids:} → the typed Nodes carrier). Returns a map; missing IDs are absent.
func fetchNodesByIDs(ctx context.Context, gc Caller, ids []string) map[string]*knowledgev1.Node {
	out := map[string]*knowledgev1.Node{}
	if gc == nil || len(ids) == 0 {
		return out
	}
	raw, err := json.Marshal(map[string]any{"ids": ids})
	if err != nil {
		slog.Warn("thought: fetchNodesByIDs: marshal failed", "err", err)
		return out
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		slog.Warn("thought: fetchNodesByIDs: execute failed", "err", err)
		return out
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		slog.Warn("thought: fetchNodesByIDs: decode failed", "err", derr)
		return out
	}
	for _, n := range nodes {
		out[n.Id] = n
	}
	return out
}

// fetchNode is a single-ID convenience wrapper around fetchNodesByIDs.
func fetchNode(ctx context.Context, gc Caller, id string) (*knowledgev1.Node, bool) {
	m := fetchNodesByIDs(ctx, gc, []string{id})
	n, ok := m[id]
	return n, ok
}

// FetchNode is the exported single-ID wrapper used by
// cmd/knowledge/internal/tools/ when an intercept needs to peek at a node
// without owning the wire helper. Mirrors FetchThoughtAdjacency above.
func FetchNode(ctx context.Context, gc Caller, id string) (*knowledgev1.Node, bool) {
	return fetchNode(ctx, gc, id)
}

// FetchChargesFor is the exported single-thought wrapper around
// fetchChargesFor for cmd/knowledge/internal/tools/ — handleChargeClient
// needs the bulk-charge wire after a mutate(create, type:charge) so it
// can compute thought properties locally without re-exposing the
// underlying helper directly.
func FetchChargesFor(ctx context.Context, gc Caller, thoughtIDs []string) map[string][]*knowledgev1.Node {
	return fetchChargesFor(ctx, gc, thoughtIDs)
}

// fetchAllThoughtNodes returns every NodeThought in the graph. One Execute
// round-trip: a type=thought browse whose typed Nodes carrier already carries the
// full node payloads (the carrier path eliminates the old ID-only projection +
// separate bulk-hydration round-trip).
func fetchAllThoughtNodes(ctx context.Context, gc Caller) ([]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, nil
	}
	raw, err := json.Marshal(map[string]any{
		"type":  string(kgtypes.NodeThought),
		"limit": 0,
	})
	if err != nil {
		return nil, err
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return nil, err
	}
	return engine.DecodeNodes(resp)
}

// fetchOutgoingTargets returns peer IDs reachable from nodeID over any
// outgoing edge at depth 1. One Execute traverse round-trip → the typed
// traversal-results carrier ([]engine.TraversalResult, each carrying a full
// Node); we project to the peer IDs (skipping the start node itself).
func fetchOutgoingTargets(ctx context.Context, gc Caller, nodeID string) ([]string, error) {
	return fetchTraversalPeerIDs(ctx, gc, nodeID, "out", nil)
}

// fetchEdgeNeighborsTyped wraps a typed-edge traverse: one Execute round-trip
// per (edgeType, direction) pair. forward=true walks outgoing edges, false walks
// incoming. Returns peer IDs only — call fetchNodesByIDs to hydrate.
func fetchEdgeNeighborsTyped(ctx context.Context, gc Caller, fromID string, edgeType kgtypes.EdgeType, forward bool) ([]string, error) {
	direction := "out"
	if !forward {
		direction = "in"
	}
	return fetchTraversalPeerIDs(ctx, gc, fromID, direction, []string{string(edgeType)})
}

// fetchTraversalPeerIDs issues one depth-1 traverse Execute (direction + optional
// edge-type filter) and returns the discovered peer IDs from the
// traversal_results_json carrier, skipping the start node. Shared by
// fetchOutgoingTargets / fetchEdgeNeighborsTyped.
func fetchTraversalPeerIDs(ctx context.Context, gc Caller, startID, direction string, edgeTypes []string) ([]string, error) {
	if gc == nil {
		return nil, nil
	}
	body := map[string]any{
		"start":     startID,
		"direction": direction,
		"depth":     1,
	}
	if len(edgeTypes) > 0 {
		body["edge_types"] = edgeTypes
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := executeViaEngine(ctx, gc, "traverse", raw)
	if err != nil {
		return nil, err
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, derr
	}
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.Node.Id == "" || r.Node.Id == startID {
			continue // skip the start node / empty rows.
		}
		out = append(out, r.Node.Id)
	}
	return out, nil
}

// fetchEdgesForNode returns outgoing + incoming edges for a single node,
// each as a slice of knowledgev1.Edge with Type/FromID/ToID populated. Two
// traverse wire calls (one per direction) to keep the parse simple — the
// caller routinely needs to distinguish the two directions for rendering.
func fetchEdgesForNode(ctx context.Context, gc Caller, nodeID string) (outgoing, incoming []knowledgev1.Edge, err error) {
	if gc == nil {
		return nil, nil, nil
	}
	outgoing, err = fetchEdgesOneDirection(ctx, gc, nodeID, true)
	if err != nil {
		return nil, nil, err
	}
	incoming, err = fetchEdgesOneDirection(ctx, gc, nodeID, false)
	if err != nil {
		return outgoing, nil, err
	}
	return outgoing, incoming, nil
}

// fetchEdgesOneDirection returns the node's edges in the requested direction via
// one Execute round-trip: a by-id RETURN_MODE_EDGES query → the typed edges
// carrier ([]knowledgev1.Edge, both directions) → direction filter client-side. An
// edge is outgoing when its FromID == nodeID, else incoming (the same relative-
// direction rule render.filterEdges uses).
func fetchEdgesOneDirection(ctx context.Context, gc Caller, nodeID string, forward bool) ([]knowledgev1.Edge, error) {
	if gc == nil {
		return nil, nil
	}
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			ById:              nodeID,
			ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
			IncludeTombstones: true,
		}},
	})
	if err != nil {
		return nil, err
	}
	rawEdges, derr := engine.DecodeEdges(resp)
	if derr != nil {
		return nil, derr
	}
	collected := make([]knowledgev1.Edge, 0, len(rawEdges))
	for i := range rawEdges {
		e := &rawEdges[i]
		outgoing := e.FromId == nodeID
		if outgoing != forward {
			continue // keep only the requested direction.
		}
		collected = append(collected, knowledgev1.Edge{Type: e.Type, FromId: e.FromId, ToId: e.ToId})
	}
	return collected, nil
}

// fetchEdgesForNodeSet returns every edge incident to ANY node in the ids set,
// in ONE Execute round-trip: a node-SET RETURN_MODE_EDGES query (ids[] +
// Forward=nil → both-direction union per the engine node-SET carrier,
// engine.proto:164-171), optionally filtered to edgeTypes. This is the
// N+1-avoidance the D1 composition relies on — ONE bulk edges read over the
// whole node set rather than N per-node traverses. Empty ids → no call.
func fetchEdgesForNodeSet(ctx context.Context, gc Caller, ids []string, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	if gc == nil || len(ids) == 0 {
		return nil, nil
	}
	plan := &knowledgev1.QueryPlan{
		Ids:               ids,
		ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		IncludeTombstones: true,
	}
	if len(edgeTypes) > 0 {
		ets := make([]string, len(edgeTypes))
		for i, et := range edgeTypes {
			ets[i] = string(et)
		}
		plan.Selection = &knowledgev1.Selection{EdgeTypes: ets}
	}
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: plan},
	})
	if err != nil {
		return nil, err
	}
	return engine.DecodeEdges(resp)
}

// runLeidenLocal wraps the relocated topology/graph RunLeiden so callers
// don't need to import the analyzer package directly.
func runLeidenLocal(nodeIDs []string, adj map[string][]string, gamma float64) map[string]string {
	return graph.RunLeiden(nodeIDs, adj, gamma)
}
