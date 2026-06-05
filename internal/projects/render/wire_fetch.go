// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
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
// uses `language` for practice-graph name selection and `name` for other graphs;
// route per graph type so the server's resolveTargetDB dispatch lands. Empty
// graphType → nil (the knowledge/default graph).
func graphTarget(graphType, graphName string) *knowledgev1.GraphSelector {
	if graphType == "" {
		return nil
	}
	sel := &knowledgev1.GraphSelector{Graph: graphType}
	if graphType == "practice" {
		sel.Language = graphName
	} else {
		sel.Name = graphName
	}
	return sel
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
		return nil, fmt.Errorf("fetch node %q: %w", nodeID, err)
	}
	nodes := decodeCarrierNodes(resp)
	if len(nodes) == 0 {
		return nil, nil
	}
	return nodes[0], nil
}

// IterEdges fetches a node's edges via the RETURN_MODE_EDGES carrier (one
// Execute, both directions) and applies the caller's direction + edgeTypes
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
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			ById:              nodeID,
			ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
			IncludeTombstones: true,
		}},
		Target: graphTarget(graphType, graphName),
	})
	if err != nil {
		return nil, fmt.Errorf("iter edges %q: %w", nodeID, err)
	}
	rawEdges := decodeCarrierEdges(resp)
	return filterEdges(rawEdges, nodeID, direction, edgeTypes), nil
}

// filterEdges applies the direction + edge-type filters over the raw edges. An
// edge's direction relative to nodeID: FromId==nodeID → outgoing, else incoming.
// Returns pointers into the rawEdges backing array (stable for the slice's
// lifetime) so no knowledgev1.Edge value is copied.
func filterEdges(rawEdges []knowledgev1.Edge, nodeID string, direction kgwire.EdgeDirection, edgeTypes []kgtypes.EdgeType) []*knowledgev1.Edge {
	typeFilter := make(map[kgtypes.EdgeType]struct{}, len(edgeTypes))
	for _, et := range edgeTypes {
		typeFilter[et] = struct{}{}
	}
	out := make([]*knowledgev1.Edge, 0, len(rawEdges))
	for i := range rawEdges {
		e := &rawEdges[i]
		outgoing := e.FromId == nodeID
		switch direction {
		case kgwire.OutgoingEdges:
			if !outgoing {
				continue
			}
		case kgwire.IncomingEdges:
			if outgoing {
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
