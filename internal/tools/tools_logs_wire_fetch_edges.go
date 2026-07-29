// SPDX-License-Identifier: Apache-2.0

// Package tools — bulk edge wire-fetch for log graphs.
//
// fetchAllLogEdges issues ONE Execute (the match-all edges shape)
// against the server and returns every edge in the resolved log graph
// matching the requested edge types. No start node, no node
// enumeration, no per-node N+1 loop — the caller indexes the response
// into the logState OutEdges / InEdges maps in O(edges).

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fetchAllLogEdges issues ONE match-all RETURN_MODE_EDGES read over the Execute
// carrier seam: a plan with no pivot discriminant, which the engine reads as
// "every edge of the graph", filtered to the requested edge types. The
// RETURN_MODE_EDGES carrier (engine.DecodeEdges) carries full edge metadata
// (weight/confidence/method/evidence/last_validated), so the round-trip preserves
// it. ONE bounded Execute regardless of node/edge cardinality — no per-node N+1,
// and no node enumeration either: the preceding all-node-ids read existed only to
// spell "all" as a pivot set, and a match-all plan says it directly. Caller
// indexes into logState locally.
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

	// The logs graph uses UPPERCASE edge types (canonicalize per the
	// client-owns-casing rule), used AS-GIVEN by the engine.
	edgeStrs := make([]string, len(edgeTypes))
	for i, t := range edgeTypes {
		edgeStrs[i] = string(t)
	}
	edgesResp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection:  &knowledgev1.Selection{EdgeTypes: edgeStrs},
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		}},
		Target: &knowledgev1.GraphSelector{Graph: "logs", Name: graphName},
	})
	if err != nil {
		return nil, fmt.Errorf("graph-wide edge read: %w", err)
	}
	return engine.DecodeEdges(edgesResp)
}
