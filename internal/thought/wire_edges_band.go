// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// wire_edges_band.go holds the BANDED bulk edge reader, a sibling of
// fetchEdgesForNodeSet (wire.go) rather than a modification of it. It is its own
// file because wire.go sits exactly at the 500-line staged-file cap, and because
// fetchEdgesForNodeSet must stay where it is: landed criteria from other plans read
// that declaration out of wire.go by path, and moving it would turn them red against
// correct work.

// fetchAllEdgesBanded returns every edge of the graph carrying one of edgeTypes,
// read as a tiling of half-open from_id bands and unioned into one deduped slice.
//
// THE ids ARE NOT PIVOTS. It takes the same argument list as fetchEdgesForNodeSet
// (wire.go) so a conversion is a one-token call-site change, but the two use `ids`
// for entirely different things: fetchEdgesForNodeSet SENDS them as the pivot set,
// while this function sends NO pivots at all and uses the ids only to derive the
// band boundaries and the split points a saturating band halves on. Said plainly
// because the identical signature is otherwise an invitation to misread it.
//
// WHAT THE CALLER GETS BACK IS A SUPERSET of what the pivot form would return: a
// match-all read filtered by type, not by id. Every caller converted to this reader
// must therefore apply its own id-membership test to the result. That is a
// REQUIREMENT ON CALLERS, not an observation about today's ones.
//
// THE PLAN LIMIT AND THE DRAIN'S edgeCap ARE THE SAME NUMBER TWICE, deliberately,
// exactly as fetchEdgesForNodeSet already explains for the pivot drain: the Limit is
// what the server enforces, the cap is what the drain uses to notice it was
// enforced. Note the consequence on THIS arm, because it is not obvious: the client
// asks for exactly the server's own edge ceiling, and the server's clamp only reports
// a substitution when the request is strictly GREATER than that ceiling. So the
// server's filled-to-the-ceiling test can never fire here, and the truncated flag the
// drain reads is carried entirely by the server's scan-side raw-row signal. Both
// detectors are wired; only one of them can actually fire on this path.
func fetchAllEdgesBanded(ctx context.Context, gc Caller, ids []string, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	if gc == nil || len(ids) == 0 {
		return nil, nil
	}
	boundaries := paging.EdgeBandBoundaries(ids, paging.EdgeBandCount)
	edges, err := paging.DrainBandedEdges(ids, boundaries, engine.CorrelationsEdgeScanCap,
		func(fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			plan := &knowledgev1.QueryPlan{
				ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
				IncludeTombstones: true,
				Limit:             int32(engine.CorrelationsEdgeScanCap),
				EdgeFromBand: &knowledgev1.EdgeFromBand{
					FromIdGte: fromIDGte,
					FromIdLt:  fromIDLt,
				},
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
				return nil, false, err
			}
			decoded, derr := engine.DecodeEdges(resp)
			return decoded, resp.GetTruncated(), derr
		})
	if err != nil {
		return nil, err
	}
	// THE SAME CENSUS LINE fetchEdgesForNodeSet EMITS, verbatim and for the same
	// reason: one line per LOGICAL bulk edge read, emitted AFTER the drain on the
	// completed union so a banded read counts ONCE rather than once per band. The
	// message string and its keys are load-bearing — a landed criterion pins that
	// literal inside the pivot reader, and the daemon-log read census was taken off
	// this exact line, so a second spelling would split the census in two.
	slog.Debug("thought: bulk edge read",
		"edge_types", edgeTypesLabel(edgeTypes),
		"pivots", len(ids),
		"edges", len(edges))
	return edges, nil
}

// edgeTypesLabel renders a requested edge-type filter for the census line: the types
// joined by "+", or the empty string when the filter is nil, which means every type.
//
// It lives HERE rather than in wire.go, which is where it was written and where its
// other caller still is (fetchEdgesForNodeSet's own census line). Two reasons, in
// order: wire.go sat at the 500-line staged-file cap and the band wiring pushed it
// over, and this is the cohesive home anyway — the label exists for the census line
// fetchAllEdgesBanded emits directly above. Same package, so neither caller changed.
func edgeTypesLabel(edgeTypes []kgtypes.EdgeType) string {
	if len(edgeTypes) == 0 {
		return ""
	}
	parts := make([]string, len(edgeTypes))
	for i, et := range edgeTypes {
		parts[i] = string(et)
	}
	return strings.Join(parts, "+")
}
