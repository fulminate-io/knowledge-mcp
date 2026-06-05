// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// countingShuffleCaller is a GraphCaller fake that (1) counts Execute calls so a
// test can prove there is no N+1, and (2) returns the requested ids' nodes in a
// DELIBERATELY SHUFFLED order (reversed vs the request) so a hydrator that joined
// by response position instead of by id-map would mis-pair rows and fail.
type countingShuffleCaller struct {
	calls    int
	lastIDs  []string
	nodesFor map[string]*knowledgev1.Node
}

func (c *countingShuffleCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.calls++
	ids := req.GetQuery().GetIds()
	c.lastIDs = ids
	// Emit the nodes in REVERSE request order — a position-zip hydrator breaks.
	out := make([]*knowledgev1.Node, 0, len(ids))
	for _, id := range slices.Backward(ids) {
		if n, ok := c.nodesFor[id]; ok {
			out = append(out, n)
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}, nil
}

// TestHydrateEngineHits is Phase 2 Step 2's criterion: the hydrator issues ONE
// RETURN_MODE_NODES read and joins id→node by MAP (not response position),
// rendering rows in RRF-rank order with the per-ID fused score. The fake returns
// shuffled (reversed) nodes to prove the id-map join.
func TestHydrateEngineHits(t *testing.T) {
	hits := []searchengine.Hit{
		{ID: "n1", Score: 0.9},
		{ID: "n2", Score: 0.8},
		{ID: "n3", Score: 0.7},
	}
	caller := &countingShuffleCaller{nodesFor: map[string]*knowledgev1.Node{
		"n1": {Id: "n1", SymbolName: "one"},
		"n2": {Id: "n2", SymbolName: "two"},
		"n3": {Id: "n3", SymbolName: "three"},
	}}

	results, err := hydrateEngineHits(context.Background(), caller, hydrateSelector{Graph: "knowledge"}, hits)
	require.NoError(t, err)

	// No N+1: exactly ONE wire call regardless of result count.
	require.Equal(t, 1, caller.calls, "hydrator issues exactly one ids[] wire read")
	require.Equal(t, []string{"n1", "n2", "n3"}, caller.lastIDs, "the single read carries the full ranked id list")

	// Rows are in RRF-rank order (NOT the shuffled response order) — proves the
	// id-map join — and carry the per-id FUSED score.
	require.Len(t, results, 3)
	require.Equal(t, "n1", results[0].Node.GetId())
	require.Equal(t, "n2", results[1].Node.GetId())
	require.Equal(t, "n3", results[2].Node.GetId())
	require.InDelta(t, 0.9, results[0].Score, 1e-12)
	require.InDelta(t, 0.8, results[1].Score, 1e-12)
	require.InDelta(t, 0.7, results[2].Score, 1e-12)
}

// TestHydrateEngineHitsSkipsMissing asserts a ranked id absent from the response
// (tombstoned/deleted between rank and hydrate) is skipped, not zipped — and the
// surviving rows keep rank order + fused scores.
func TestHydrateEngineHitsSkipsMissing(t *testing.T) {
	hits := []searchengine.Hit{
		{ID: "a", Score: 0.5},
		{ID: "gone", Score: 0.4},
		{ID: "c", Score: 0.3},
	}
	caller := &countingShuffleCaller{nodesFor: map[string]*knowledgev1.Node{
		"a": {Id: "a"},
		"c": {Id: "c"},
		// "gone" deliberately absent.
	}}

	results, err := hydrateEngineHits(context.Background(), caller, hydrateSelector{}, hits)
	require.NoError(t, err)
	require.Equal(t, 1, caller.calls)
	require.Len(t, results, 2, "missing id is skipped, not position-zipped")
	require.Equal(t, "a", results[0].Node.GetId())
	require.Equal(t, "c", results[1].Node.GetId())
	require.InDelta(t, 0.5, results[0].Score, 1e-12)
	require.InDelta(t, 0.3, results[1].Score, 1e-12)
}

// TestHydrateEngineHitsEmpty asserts empty input issues NO wire call.
func TestHydrateEngineHitsEmpty(t *testing.T) {
	caller := &countingShuffleCaller{nodesFor: map[string]*knowledgev1.Node{}}
	results, err := hydrateEngineHits(context.Background(), caller, hydrateSelector{}, nil)
	require.NoError(t, err)
	require.Nil(t, results)
	require.Equal(t, 0, caller.calls, "empty hits → zero wire calls")
}
