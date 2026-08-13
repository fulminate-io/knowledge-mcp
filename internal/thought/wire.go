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

// reflectProbe is the narrow package-local seam the quiet-tick reflection probe
// uses to read the reflect dirty-gen over PipelineScan — deliberately NARROWER
// than Caller (which every wire helper takes) so the loop can reach PipelineScan
// without widening Caller or importing graphclient. The bootstrap
// routedWireClient (which already wraps PipelineScan via router.Backend) satisfies
// it; the loop type-asserts p.gc.(reflectProbe) at probe time, and a probe-less
// caller (Execute-only) simply skips the quiet-tick gate (never skips the pass).
type reflectProbe interface {
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
}

// PipelineScanner is the package-local PipelineScan seam the lever-time member
// vector drain pages the segment_rebuild axis through. Kept package-local (a
// twin of the narrower reflectProbe) so the thought package reaches PipelineScan
// without importing the higher-level tools package or its identically-shaped
// PipelineScanner — the wire contract is the generated proto, not a shared Go
// type. The bootstrap routedWireClient satisfies it.
type PipelineScanner interface {
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
}

// drainVectorPageSize is the segment_rebuild scan page size — mirrors the
// rebuild_segments driver's 2048 id-cursor page (a separate const because the
// tools package's value is unexported and this is a different package home).
const drainVectorPageSize = 2048

// drainVectorIndex pages the segment_rebuild PipelineScan axis by the stable
// after_id id-cursor and returns a map of nodeID → stored 256-bit binary vector
// for the named graph. It mirrors scanRebuildSegments (the rebuild_segments
// driver) but returns a vector index keyed by nodeID rather than building
// segments, discarding the BM25 fields the axis also ships. Termination is on an
// EMPTY page (the segment_rebuild set is stable, so a full final page is normal;
// only a zero-item page signals exhaustion).
//
// HONEST PAYLOAD COST: the segment_rebuild axis ships the full BM25 text per node
// alongside the 32-byte vector, so this drain pulls far more than 32 bytes/node.
// It now has TWO callers: the similarity lever, which calls it unconditionally on
// demand, and leaf attachment, which reaches it ONLY as the fallback for a
// segment pool the coverage gate declined (degenerate, unmeasured, or not yet
// wired). The cost is accepted because the hourly path normally resolves member
// vectors from the client's RESIDENT segment engines with zero RPC and never
// reaches this drain at all — not because the payload is small. A vectors_only
// scan param would avoid the BM25 payload but is a server/proto change, out of
// scope.
//
// A nil scanner returns a clear degraded-mode error (the caller is running with
// no segment engine wired). A cold graph (empty first page) returns a non-nil
// empty map with no error. The drain is always over the knowledge graph's
// "default" graph — the thought corpus.
func drainVectorIndex(ctx context.Context, scanner PipelineScanner) (map[string][]byte, error) {
	if scanner == nil {
		return nil, fmt.Errorf("thought: drainVectorIndex degraded — no PipelineScanner wired (segment engine absent)")
	}
	out := make(map[string][]byte)
	afterID := ""
	for {
		resp, err := scanner.PipelineScan(ctx, &knowledgev1.PipelineScanRequest{
			GraphType: string(kgtypes.GraphKnowledge),
			GraphName: "default",
			Axis:      "segment_rebuild",
			Limit:     drainVectorPageSize,
			AfterId:   afterID,
		})
		if err != nil {
			return nil, err
		}
		page := resp.GetItems()
		if len(page) == 0 {
			break // empty page = scan exhausted
		}
		for _, it := range page {
			out[it.GetNodeId()] = it.GetBinaryVector()
		}
		// Advance the cursor to the LAST item's id (the scan returns id-ascending).
		afterID = page[len(page)-1].GetNodeId()
	}
	return out, nil
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

// executeReflectInertMutate compiles a mutate call and marks the resulting
// MutationPlan reflect-inert BEFORE executing — the reflection pass's OWN
// metadata writeback (cluster_id via persistClusterAssignments,
// propagated_valence/propagated_magnitude via bulkPersistMetadata) must NOT
// advance the reflect dirty-gen, or it re-triggers the hourly pass forever (the
// T1-1 self-trigger loop).
//
// The flag is set PROGRAMMATICALLY on the compiled proto, never via a mutate arg:
// the bulk_update_metadata args carry no reflect_inert_writeback key, so the flag
// is set only here, only on the reflection writeback, and is UNFORGEABLE through
// the user mutate tool surface (the LLM supplies args, never proto fields) —
// identical security posture to postpopulate's SystemManagedCreate.
func executeReflectInertMutate(ctx context.Context, gc Caller, args json.RawMessage) error {
	req, ok := engine.Compile("mutate", args)
	if !ok {
		return fmt.Errorf("thought: mutate args not reducible to an ExecuteRequest")
	}
	if mp := req.GetMutation(); mp != nil {
		mp.ReflectInertWriteback = true
	}
	_, err := gc.Execute(ctx, req)
	return err
}

// fetchChargesFor composes the per-thought charge map CLIENT-SIDE, reproducing
// the server's handleChargesFor (a PURE read: per-thought outgoing-EdgeChargedBy
// walk + charge hydration, zero compute).
//
// src is the per-pass read memo: every consumer of the thought-pivot charge map in
// one pass is served ONE composition through memoCharges, so the five stages that
// each built their own map now share it. A nil/non-memo src composes the map on the
// spot, exactly as before.
func fetchChargesFor(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string][]*knowledgev1.Node {
	return memoCharges(ctx, gc, thoughtIDs, src)
}

// fetchChargesUncached is the composition itself — the read memoCharges falls back
// to on a miss. AT MOST two Execute calls regardless of |thoughtIDs|, and no hydrate
// at all when the resident charge snapshot covers the set:
//
//  1. ONE bulk RETURN_MODE_EDGES read over the thought-id node set filtered to
//     EdgeChargedBy; collect the ToID charge IDs per thought (FromID is the
//     thought, ToID the charge — EdgeChargedBy is thought_parent→charge).
//  2. ONE memoCorpusNodes hydrate of the collected charge IDs — served from the
//     resident charge snapshot with a residual-only wire read.
//
// Join in caller order, omitting thoughts with no charges (matching
// handleChargesFor lines 86-97). Empty input → empty map. The error return exists
// so the memo can tell a FAILED edge read from a genuinely charge-free corpus and
// decline to memoize the former; every other caller reads it through
// fetchChargesFor, which keeps the best-effort map-only contract.
func fetchChargesUncached(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) (map[string][]*knowledgev1.Node, error) {
	out := map[string][]*knowledgev1.Node{}
	if gc == nil || len(thoughtIDs) == 0 {
		return out, nil
	}
	inSet := make(map[string]bool, len(thoughtIDs))
	for _, tid := range thoughtIDs {
		inSet[tid] = true
	}

	edges, err := fetchEdgesForNodeSet(ctx, gc, thoughtIDs, []kgtypes.EdgeType{kgtypes.EdgeChargedBy})
	if err != nil {
		slog.Warn("thought: fetchChargesFor: bulk edges failed", "err", err)
		return out, err
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
		return out, nil
	}

	chargeByID := memoCorpusNodes(ctx, gc, allChargeIDs, src)

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
	return out, nil
}

// fetchNodesByIDs hydrates a slice of node IDs in one Execute round-trip
// (query{ids:} → the typed Nodes carrier). Returns a map; missing IDs are absent.
// Best-effort: a failed read is logged and yields an empty map. A caller that must
// tell a FAILED read from a genuinely absent id — the per-pass memo, which must
// never memoize a failure — takes fetchNodesByIDsErr instead.
func fetchNodesByIDs(ctx context.Context, gc Caller, ids []string) map[string]*knowledgev1.Node {
	out, _ := fetchNodesByIDsErr(ctx, gc, ids)
	return out
}

// fetchNodesByIDsErr is fetchNodesByIDs' error-surfacing core: the same single
// Execute round-trip, with the failure the wrapper swallows returned alongside the
// (empty) map.
func fetchNodesByIDsErr(ctx context.Context, gc Caller, ids []string) (map[string]*knowledgev1.Node, error) {
	out := map[string]*knowledgev1.Node{}
	if gc == nil || len(ids) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(map[string]any{"ids": ids})
	if err != nil {
		slog.Warn("thought: fetchNodesByIDs: marshal failed", "err", err)
		return out, err
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		slog.Warn("thought: fetchNodesByIDs: execute failed", "err", err)
		return out, err
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		slog.Warn("thought: fetchNodesByIDs: decode failed", "err", derr)
		return out, derr
	}
	for _, n := range nodes {
		out[n.Id] = n
	}
	return out, nil
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
// underlying helper directly. Its signature is deliberately UNCHANGED: an
// on-demand intercept holds no propagation pass, so it passes a nil source and
// takes the uncached read.
func FetchChargesFor(ctx context.Context, gc Caller, thoughtIDs []string) map[string][]*knowledgev1.Node {
	return fetchChargesFor(ctx, gc, thoughtIDs, nil)
}

// FetchEdgesForNodeSet is the exported bulk-edge wrapper around
// fetchEdgesForNodeSet for cmd/knowledge/internal/tools/ — the context-pack
// composer needs the ONE-round-trip both-direction edge read over a node set
// (the N+1-avoidance bulk read) without re-exposing the unexported helper.
func FetchEdgesForNodeSet(ctx context.Context, gc Caller, ids []string, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	return fetchEdgesForNodeSet(ctx, gc, ids, edgeTypes)
}

// FetchNodesByIDs is the exported bulk-hydrate wrapper around fetchNodesByIDs
// for cmd/knowledge/internal/tools/ — the context-pack composer needs the
// one-Execute ids[] hydrate to turn collected peer IDs into nodes.
func FetchNodesByIDs(ctx context.Context, gc Caller, ids []string) map[string]*knowledgev1.Node {
	return fetchNodesByIDs(ctx, gc, ids)
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
	if gc == nil || nodeID == "" {
		// An edges plan with no pivot means "every edge of the graph" — never what
		// a single node's directional read wants.
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
