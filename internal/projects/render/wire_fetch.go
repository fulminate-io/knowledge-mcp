// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// GraphCaller is the narrow base render seam this package needs. Mirrors
// cmd/knowledge/internal/tools.GraphCaller exactly — kept local so render/ does
// not import cmd/knowledge/internal/tools (a cycle: tools/intercept_assemble.go
// imports render/); concrete implementations satisfy it by structural typing.
// Execute is the base seam. The wire-fetch helpers (FetchNode / FetchNodeIn /
// IterEdges / IterEdgesIn) decode RAW ExecuteResponse carriers (nodes_json /
// edges_json) over this seam.
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// Executor is the Execute seam the wire-fetch helpers type-assert for. It is
// identical to GraphCaller (both require only Execute); the alias is kept so the
// asExecutor upgrade-or-loud-error path and the tools-side narrowing stay
// expressed in one place.
type Executor interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// MarshalQueryByID returns the JSON arguments for a `query` MCP call that
// resolves a single node by ID, including tombstones. Exported for the
// backend_lookup.go reuse site; the wire-fetch helpers below build QueryPlans
// directly for the Execute carrier path.
func MarshalQueryByID(id string) json.RawMessage {
	payload := struct {
		ID                string `json:"id"`
		IncludeTombstones bool   `json:"include_tombstones"`
	}{
		ID:                id,
		IncludeTombstones: true,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// asExecutor upgrades a GraphCaller to an Executor or returns a typed error.
func asExecutor(gc GraphCaller) (Executor, error) {
	ex, ok := gc.(Executor)
	if !ok {
		return nil, fmt.Errorf("render wire-fetch requires an Execute-capable graph client")
	}
	return ex, nil
}

// graphTarget builds the GraphSelector for a cross-graph fetch. The MCP wire
// keys each graph family by its typed selector field: `language` for practice,
// `repo` for code, `account` for cloud and cicd, and `name` for the rest (logs,
// checks, …). Routing an instance name through the wrong field is not a
// soft failure — the server's resolver rejects the selector before any graph
// lookup (the code resolver REQUIRES sel.Repo, the cloud/cicd resolver REQUIRES
// sel.Account), so a wrongly-keyed selector fails every cross-graph fetch for
// that family. That per-family mapping is owned by graphsel.InstanceField, the
// single switch in the client; this helper delegates to it rather than carrying
// a second hand-maintained copy that can drift a family at a time.
// omitDefaultName is false here: a caller supplying an explicit graph name means
// it, including the literal "default". Empty graphType → nil (the
// knowledge/default graph).
func graphTarget(graphType, graphName string) *knowledgev1.GraphSelector {
	if graphType == "" {
		return nil
	}
	return graphsel.GraphSelectorFor(kgtypes.GraphType(graphType), graphName, false)
}

// decodeCarrierNodes reads the typed Nodes carrier (the same carrier the engine
// decodeNodes reads) — T5 deleted the nodes_json blob, so this is now a
// direct field read of []*knowledgev1.Node. Empty carrier → nil.
func decodeCarrierNodes(resp *knowledgev1.ExecuteResponse) []*knowledgev1.Node {
	return resp.GetNodes()
}

// decodeCarrierEdges decodes the typed edges carrier (RETURN_MODE_EDGES) into
// []knowledgev1.Edge via the shared engine.EdgesFromProto. Empty carrier → nil.
func decodeCarrierEdges(resp *knowledgev1.ExecuteResponse) []knowledgev1.Edge {
	return engine.EdgesFromProto(resp.GetEdges())
}

// FetchNode resolves a single node by ID against the knowledge/default graph via
// the Execute carrier path: one ByID Execute (include_tombstones) read from the
// typed Nodes carrier directly as *knowledgev1.Node (T5 dropped the
// store.Node wrapper). Returns nil (no error) for not-found / tombstoned-without-
// flag so callers can branch on node == nil.
func FetchNode(ctx context.Context, gc GraphCaller, nodeID string) (*knowledgev1.Node, error) {
	return FetchNodeIn(ctx, gc, nodeID, "", "")
}

// FetchNodeIn is FetchNode with an explicit cross-graph Target (graphType +
// graphName). Empty graphType targets the knowledge/default graph. Returns nil
// (no error) when the node is not found.
//
// ABSENCE ARRIVES TWO WAYS AND BOTH MEAN nil. A by-id read can answer with an
// empty node carrier, or the engine can answer NOT_FOUND — which arm a backend
// takes is its own business, and callers are written to the documented contract
// rather than to either spelling. Every one of this helper's callers branches on
// node == nil; none inspects the error to ask whether the id existed. So a
// leaked NOT_FOUND is not a caller-visible distinction, it is a second dialect
// of "absent" that each caller would have to learn separately — and the one that
// did not learn it read a create-shaped upsert as a hard failure.
//
// A TRANSPORT OR PERMISSION FAILURE IS STILL AN ERROR. Only CodeNotFound is
// folded into nil; everything else propagates, because "the read did not happen"
// and "the read happened and found nothing" are the distinction this helper
// exists to preserve.
func FetchNodeIn(ctx context.Context, gc GraphCaller, nodeID, graphType, graphName string) (*knowledgev1.Node, error) {
	if gc == nil || nodeID == "" {
		return nil, nil
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, err
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: nodeID, IncludeTombstones: true}},
		Target: graphTarget(graphType, graphName),
	})
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch node %q: %w", nodeID, err)
	}
	nodes := decodeCarrierNodes(resp)
	if len(nodes) == 0 {
		return nil, nil
	}
	return nodes[0], nil
}

// IterEdges fetches a node's edges via the RETURN_MODE_EDGES carrier (bounded
// pages, both directions) and applies the caller's direction + edgeTypes
// filters client-side over the decoded knowledgev1.Edge slice. The edge's
// FromId/ToId relative to nodeID determines its direction. Returns
// []*knowledgev1.Edge — knowledgev1.Edge value-embeds the proto MessageState,
// so pointer elements let callers range without copylocks-flagged value copies.
func IterEdges(
	ctx context.Context,
	gc GraphCaller,
	nodeID string,
	direction kgwire.EdgeDirection,
	edgeTypes ...kgtypes.EdgeType,
) ([]*knowledgev1.Edge, error) {
	return IterEdgesIn(ctx, gc, nodeID, "", "", direction, edgeTypes...)
}

// IterEdgesIn is IterEdges with an explicit cross-graph Target.
func IterEdgesIn(
	ctx context.Context,
	gc GraphCaller,
	nodeID, graphType, graphName string,
	direction kgwire.EdgeDirection,
	edgeTypes ...kgtypes.EdgeType,
) ([]*knowledgev1.Edge, error) {
	if gc == nil || nodeID == "" {
		return nil, nil
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, err
	}
	// The plan Limit and the drain's edgeCap are the same number twice on
	// purpose: the Limit is what the server enforces, the cap is what the drain
	// uses to notice it was enforced. One without the other yields a drain that
	// never detects truncation, or one that splits on a threshold nobody applies.
	rawEdges, err := paging.DrainPivotEdges([]string{nodeID}, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			resp, rerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:               idPage,
					ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					IncludeTombstones: true,
					Limit:             int32(engine.CorrelationsEdgeScanCap),
					EdgeFromBand:      paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
				}},
				Target: graphTarget(graphType, graphName),
			})
			if rerr != nil {
				return nil, false, fmt.Errorf("iter edges %q: %w", nodeID, rerr)
			}
			return decodeCarrierEdges(resp), resp.GetTruncated(), nil
		})
	if err != nil {
		return nil, err
	}
	return filterEdges(rawEdges, nodeID, direction, edgeTypes), nil
}

// filterEdges applies the direction + edge-type filters over the raw edges for a
// SINGLE pivot: FromId==nodeID → outgoing, ToId==nodeID → incoming. It delegates
// to the set form with a one-element pivot set so there is ONE direction rule in
// this file rather than two that can drift apart. The drain that feeds it
// returns only edges incident to nodeID, so "not outgoing" and "incoming" name
// the same edges — apart from a self-edge, which the set rule correctly reports
// as both.
func filterEdges(rawEdges []knowledgev1.Edge, nodeID string, direction kgwire.EdgeDirection, edgeTypes []kgtypes.EdgeType) []*knowledgev1.Edge {
	return filterEdgesForSet(rawEdges, map[string]struct{}{nodeID: {}}, direction, edgeTypes)
}

// filterEdgesForSet applies the direction + edge-type filters over the raw edges
// for a SET of pivots.
//
// DIRECTION IS DECIDED PER PIVOT, NOT ONCE GLOBALLY. An edge is OUTGOING when
// its FromId is a pivot and INCOMING when its ToId is a pivot, evaluated
// independently. That independence is the whole point of the set form: an edge
// joining TWO pivots is genuinely both — it leaves one and enters the other — so
// a rule that decided direction once and for all would report one of those two
// nodes' incoming edge as its outgoing one.
//
// The two consequences worth naming, because both are asserted in the tests.
// A two-pivot edge appears under BOTH direction filters. And a self-edge, whose
// FromId and ToId are the same pivot, is likewise both rather than outgoing
// only — the single-pivot rule this replaces called it outgoing purely because
// it tested one endpoint.
//
// Returns pointers into the rawEdges backing array (stable for the slice's
// lifetime) so no knowledgev1.Edge value is copied.
func filterEdgesForSet(
	rawEdges []knowledgev1.Edge,
	pivots map[string]struct{},
	direction kgwire.EdgeDirection,
	edgeTypes []kgtypes.EdgeType,
) []*knowledgev1.Edge {
	typeFilter := make(map[kgtypes.EdgeType]struct{}, len(edgeTypes))
	for _, et := range edgeTypes {
		typeFilter[et] = struct{}{}
	}
	out := make([]*knowledgev1.Edge, 0, len(rawEdges))
	for i := range rawEdges {
		e := &rawEdges[i]
		_, leavesPivot := pivots[e.FromId]
		_, entersPivot := pivots[e.ToId]
		switch direction {
		case kgwire.OutgoingEdges:
			if !leavesPivot {
				continue
			}
		case kgwire.IncomingEdges:
			if !entersPivot {
				continue
			}
		case kgwire.BothEdges:
			// keep all
		}
		if len(typeFilter) > 0 {
			if _, ok := typeFilter[kgtypes.EdgeType(e.Type)]; !ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// FetchDependsOnEdges fetches the depends-on edges among nodeIDs through the
// bounded pivot drain and returns a map from each dependent's ID to its first
// depends-on target. It batches the per-child firstDependsOn lookup the tree
// renderer otherwise does node-by-node.
//
// The plan pivots on the node-SET (QueryPlan.Ids) and sets Forward=&true
// so the server unions only each pivot's OUTGOING depends-on edges. That
// outgoing-only scoping is load-bearing: an unset Forward maps to
// both-direction iteration, which would also surface a node's INCOMING
// depends-on edge (a peer depending on it) and miscount it as the node's
// own dependency. With outgoing-only, FromId is always the dependent and
// ToId its dependency target, so no client-side direction filtering is
// needed. When a node has more than one outgoing depends-on edge, the
// first encountered wins — matching firstDependsOn's depEdges[0] contract.
func FetchDependsOnEdges(ctx context.Context, gc GraphCaller, nodeIDs []string) (map[string]string, error) {
	if gc == nil || len(nodeIDs) == 0 {
		return nil, nil
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, err
	}
	fwd := true
	// The plan Limit and the drain's edgeCap are the same number twice on
	// purpose: the Limit is what the server enforces, the cap is what the drain
	// uses to notice it was enforced. One without the other yields a drain that
	// never detects truncation, or one that splits on a threshold nobody applies.
	edges, err := paging.DrainPivotEdges(nodeIDs, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			resp, rerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:               idPage,
					Forward:           &fwd,
					ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					IncludeTombstones: true,
					Limit:             int32(engine.CorrelationsEdgeScanCap),
					Selection:         &knowledgev1.Selection{EdgeTypes: []string{string(kgtypes.EdgeDependsOn)}},
					EdgeFromBand:      paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
				}},
			})
			if rerr != nil {
				return nil, false, fmt.Errorf("fetch depends-on edges: %w", rerr)
			}
			return decodeCarrierEdges(resp), resp.GetTruncated(), nil
		})
	if err != nil {
		return nil, err
	}
	dependsOn := make(map[string]string, len(edges))
	for i := range edges {
		e := &edges[i]
		if _, seen := dependsOn[e.FromId]; seen {
			continue // keep the first outgoing depends-on target per node
		}
		dependsOn[e.FromId] = e.ToId
	}
	return dependsOn, nil
}

// IterEdgesFor is the node-SET form of IterEdges: one bounded pivot drain over
// every id in nodeIDs, with the direction and edge-type filters applied over the
// union. It replaces a per-node IterEdges loop.
//
// DIRECTION IS COMPUTED PER PIVOT, which is the one thing this cannot borrow
// from the single-node form. An edge joining two pivots is OUTGOING for the
// pivot it leaves and INCOMING for the pivot it enters; a set-form read that
// reused the single-pivot rule would classify such an edge once, globally, and
// so report one of the two nodes' incoming edge as its outgoing one.
func IterEdgesFor(
	ctx context.Context,
	gc GraphCaller,
	nodeIDs []string,
	direction kgwire.EdgeDirection,
	edgeTypes ...kgtypes.EdgeType,
) ([]*knowledgev1.Edge, error) {
	if gc == nil || len(nodeIDs) == 0 {
		return nil, nil
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, err
	}
	// The plan Limit and the drain's edgeCap are the same number twice on
	// purpose: the Limit is what the server enforces, the cap is what the drain
	// uses to notice it was enforced.
	rawEdges, err := paging.DrainPivotEdges(nodeIDs, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			resp, rerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:               idPage,
					ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					IncludeTombstones: true,
					Limit:             int32(engine.CorrelationsEdgeScanCap),
					EdgeFromBand:      paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
				}},
			})
			if rerr != nil {
				return nil, false, fmt.Errorf("iter edges for %d pivots: %w", len(idPage), rerr)
			}
			return decodeCarrierEdges(resp), resp.GetTruncated(), nil
		})
	if err != nil {
		return nil, err
	}
	pivots := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		pivots[id] = struct{}{}
	}
	return filterEdgesForSet(rawEdges, pivots, direction, edgeTypes), nil
}
