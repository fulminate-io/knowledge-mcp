// SPDX-License-Identifier: Apache-2.0

// Package tools — bulk edge wire-fetch for log graphs.
//
// fetchAllLogEdges issues ONE traverse RPC (the graph-wide
// enumeration shape) against the server and returns every edge in the
// resolved log graph matching the requested edge types. No start node,
// no per-node N+1 loop — the caller indexes the response into the
// logState OutEdges / InEdges maps in O(edges).

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fetchAllLogEdges composes the graph-wide-edges enumeration over the Execute
// carrier seam: (1) one Match-all RETURN_MODE_NODES
// enumeration of every node in the resolved log graph, then (2) one
// RETURN_MODE_EDGES ids[]→union read over the full node-id set, filtered to the
// requested edge types. The RETURN_MODE_EDGES carrier (engine.DecodeEdges)
// carries full edge metadata (weight/confidence/method/evidence/last_validated),
// so the round-trip preserves it. Two bounded Execute calls regardless of
// node/edge cardinality — no per-node N+1. Caller indexes into logState locally.
func fetchAllLogEdges(
	ctx context.Context,
	gc GraphCaller,
	graphName string,
	edgeTypes []kgtypes.EdgeType,
) ([]knowledgev1.Edge, error) {
	if gc == nil {
		return nil, fmt.Errorf("fetchAllLogEdges: gc is nil")
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		return nil, fmt.Errorf("fetchAllLogEdges: %w", err)
	}
	target := &knowledgev1.GraphSelector{Graph: "logs", Name: graphName}

	// (1) Match-all node enumeration (Selection empty → Match(""), Limit 0 → no cap).
	nodesResp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}}},
		Target: target,
	})
	if err != nil {
		return nil, fmt.Errorf("graph-wide node enumeration: %w", err)
	}
	nodes, derr := engine.DecodeNodes(nodesResp)
	if derr != nil {
		return nil, fmt.Errorf("graph-wide node enumeration decode: %w", derr)
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	// (2) RETURN_MODE_EDGES ids[]→union read over every node id, edge-type
	// filtered. The logs graph uses UPPERCASE edge types (canonicalize per the
	// client-owns-casing rule), used AS-GIVEN by the engine.
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.Id
	}
	edgeStrs := make([]string, len(edgeTypes))
	for i, t := range edgeTypes {
		edgeStrs[i] = string(t)
	}
	edgesResp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection:  &knowledgev1.Selection{Ids: ids, EdgeTypes: edgeStrs},
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		}},
		Target: target,
	})
	if err != nil {
		return nil, fmt.Errorf("graph-wide edge union: %w", err)
	}
	return engine.DecodeEdges(edgesResp)
}
