// SPDX-License-Identifier: Apache-2.0

// Package tools — bulk edge wire-fetch for log graphs.
//
// fetchAllLogEdges returns every edge in the resolved log graph matching the
// requested edge types, read in BOUNDED pages: a keyset drain over the graph's
// node ids, then a pivot-paged edge drain over those ids. No start node and no
// per-node N+1 loop — the caller indexes the response into the logState
// OutEdges / InEdges maps in O(edges).

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fetchAllLogEdges reads the log graph's edges in TWO bounded drains over the
// Execute carrier seam: a keyset drain (paging.DrainKeysetIDs) enumerates the
// graph's node ids a page at a time, then a pivot drain (paging.DrainPivotEdges)
// reads the edges incident to those ids a page at a time and unions them. The
// RETURN_MODE_EDGES carrier (engine.DecodeEdges) carries full edge metadata
// (weight/confidence/method/evidence/last_validated), so the round trip preserves
// it. Caller indexes into logState locally.
//
// It used to be ONE match-all Execute carrying no pivot discriminant, and the
// id-enumeration pass below was deleted as redundant when that landed. Both are
// restored deliberately: the single read was unbounded regardless of node/edge
// cardinality, which is the denial-of-service surface a user-reachable read may
// not offer. The cost is O(N/page) round trips in place of one.
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

	ids, err := paging.DrainKeysetIDs(func(afterID string) ([]string, error) {
		cursor := afterID
		resp, rerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection:  &knowledgev1.Selection{},
				ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_IDS,
				Limit:      int32(paging.BrowsePageSize),
				// SET on every page including the first, where the value is
				// empty: presence is what selects the keyset browse.
				AfterId:   &cursor,
				SkipTotal: true,
			}},
			Target: target,
		})
		if rerr != nil {
			return nil, fmt.Errorf("graph-wide node id enumeration: %w", rerr)
		}
		return resp.GetIds(), nil
	}, paging.BrowsePageSize)
	if err != nil {
		return nil, err
	}

	// The logs graph uses UPPERCASE edge types (canonicalize per the
	// client-owns-casing rule), used AS-GIVEN by the engine.
	edgeStrs := make([]string, len(edgeTypes))
	for i, t := range edgeTypes {
		edgeStrs[i] = string(t)
	}
	return paging.DrainPivotEdges(ids, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string) ([]knowledgev1.Edge, error) {
			edgesResp, rerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:        idPage,
					Selection:  &knowledgev1.Selection{EdgeTypes: edgeStrs},
					ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					Limit:      int32(engine.CorrelationsEdgeScanCap),
				}},
				Target: target,
			})
			if rerr != nil {
				return nil, fmt.Errorf("graph-wide edge read: %w", rerr)
			}
			return engine.DecodeEdges(edgesResp)
		})
}
