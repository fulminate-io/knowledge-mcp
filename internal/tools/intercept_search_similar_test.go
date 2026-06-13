// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fakeVectorResolver is a scripted SegmentVectorResolver: it records the id it was
// asked for and returns a canned (vec, ok) — or an error when err is set. Satisfies
// tools.SegmentVectorResolver.
type fakeVectorResolver struct {
	calls    atomic.Int64
	lastID   string
	vec      []byte
	ok       bool
	resolveE error
}

func (f *fakeVectorResolver) VectorByID(
	_ context.Context, _ kgtypes.GraphType, _, externalID string,
) ([]byte, bool, error) {
	f.calls.Add(1)
	f.lastID = externalID
	if f.resolveE != nil {
		return nil, false, f.resolveE
	}
	return f.vec, f.ok, nil
}

// TestInterceptSearch_SimilarMode: search mode=similar + node_id resolves the
// node's STORED vector, drives the CLIENT engine with an EMPTY query text + that
// vector + k+1, EXCLUDES the self hit, returns at most k results, and dispatches
// NO server search.
func TestInterceptSearch_SimilarMode(t *testing.T) {
	var execHits atomic.Int64
	// The hydrate read serves the two neighbor nodes (NOT the self node — it is
	// dropped before hydrate). If self-exclusion regressed, n_self would be ranked
	// #1 and the test would need its node here; its absence is part of the proof.
	gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "NeighborOne"},
		&knowledgev1.Node{Id: "n2", Type: "finding", SymbolName: "NeighborTwo"},
	))
	storedVec := []byte{0xAB, 0xCD, 0xEF, 0x01}
	res := &fakeVectorResolver{vec: storedVec, ok: true}
	// The engine returns the self hit (n_self) ranked #1 plus two real neighbors —
	// self-exclusion must drop n_self from the rendered output.
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{
		{ID: "n_self", Score: 1.0},
		{ID: "n1", Score: 0.9},
		{ID: "n2", Score: 0.8},
	}}
	deps := &interceptDeps{gc: gc, segMgr: mgr, segRes: res}

	k := 2
	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "similar", "node_id": "n_self", "limit": k,
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "result is not an error: %v", engine.FirstTextContent(out))

	// The resolver was queried for the node id.
	require.Equal(t, int64(1), res.calls.Load(), "stored-vector resolver was queried")
	require.Equal(t, "n_self", res.lastID)

	// The client engine ran with EMPTY query text + the resolved vector + k+1.
	require.Equal(t, int64(1), mgr.calls.Load(), "client engine drove the similar arm")
	require.Equal(t, kgtypes.GraphKnowledge, mgr.lastGT)
	require.Equal(t, knowledgeDefaultName, mgr.lastName)
	require.Empty(t, mgr.lastText, "empty query text → stored-vector space, no fresh embed")
	require.Equal(t, storedVec, mgr.lastVec, "the resolved STORED vector seeded the search")
	require.Equal(t, k+1, mgr.lastK, "k+1 leaves room for self-exclusion")

	// No SERVER search dispatch — only the hydrate Ids[] read went to the wire.
	require.False(t, dispatchedAServerSearch(handler.recordedReqs()), "similar mode must NOT dispatch a server search")

	// Self hit excluded; both neighbors present in rank order; at most k rows.
	body := engine.FirstTextContent(out)
	assert.NotContains(t, body, "n_self", "the query node must be excluded from its own similar results")
	assert.Contains(t, body, "NeighborOne")
	assert.Contains(t, body, "NeighborTwo")
}

// TestInterceptSearch_SimilarMode_RequiresNodeID: empty node_id → handled=true
// with a loud errorResult naming node_id, NOT a fall-through to text search.
func TestInterceptSearch_SimilarMode_RequiresNodeID(t *testing.T) {
	res := &fakeVectorResolver{vec: []byte{0x01}, ok: true}
	mgr := &fakeSegmentSearcher{}
	deps := &interceptDeps{gc: newInterceptHarness(t, new(atomic.Int64), cannedNodesResp()), segMgr: mgr, segRes: res}

	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "similar", "node_id": "",
	}))
	require.True(t, handled, "empty node_id must be CLAIMED (loud-error), not fall through")
	require.True(t, out.IsError, "empty node_id must surface an error result")
	assert.Contains(t, engine.FirstTextContent(out), "node_id")
	require.Equal(t, int64(0), mgr.calls.Load(), "no client search on the missing-node_id error path")
}

// TestInterceptSearch_SimilarMode_RequiresStoredVector: a resolver returning
// (nil,false) → handled=true with a loud errorResult naming the node and the
// no-stored-vector reason + rebuild guidance, NEVER an empty success.
func TestInterceptSearch_SimilarMode_RequiresStoredVector(t *testing.T) {
	res := &fakeVectorResolver{ok: false} // not embedded yet
	mgr := &fakeSegmentSearcher{}
	deps := &interceptDeps{gc: newInterceptHarness(t, new(atomic.Int64), cannedNodesResp()), segMgr: mgr, segRes: res}

	handled, out := InterceptSearch(deps, searchParams(t, map[string]any{
		"graph": "knowledge", "mode": "similar", "node_id": "ghost",
	}))
	require.True(t, handled)
	require.True(t, out.IsError, "absent stored vector must surface an error, not empty success")
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, "ghost", "the error names the node id")
	assert.Contains(t, body, "stored vector", "the error names the no-stored-vector reason")
	// Guidance must NOT name manage(rebuild_segments): that op supports only code
	// and registered custom graph types and refuses the builtin knowledge graph
	// (caught by the first live smoke of this mode).
	assert.Contains(t, body, "pipeline to finish embedding", "the error guides toward the embed-pipeline/segment-ship remedy")
	assert.NotContains(t, body, "rebuild_segments", "must not suggest an op that refuses the knowledge graph")
	require.Equal(t, int64(0), mgr.calls.Load(), "no client search once the vector resolve fails")
}

// TestInterceptSearch_SimilarMode_RequiresDeps: nil SegmentManager / nil
// SegmentVectorResolver each → handled=true with a loud errorResult rather than a
// fall-through to the server text-search path.
func TestInterceptSearch_SimilarMode_RequiresDeps(t *testing.T) {
	gc := newInterceptHarness(t, new(atomic.Int64), cannedNodesResp())

	cases := []struct {
		name string
		deps *interceptDeps
	}{
		{"nil resolver", &interceptDeps{gc: gc, segMgr: &fakeSegmentSearcher{}, segRes: nil}},
		{"nil manager", &interceptDeps{gc: gc, segMgr: nil, segRes: &fakeVectorResolver{vec: []byte{0x01}, ok: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handled, out := InterceptSearch(tc.deps, searchParams(t, map[string]any{
				"graph": "knowledge", "mode": "similar", "node_id": "n1",
			}))
			require.True(t, handled, "nil deps must be CLAIMED (loud-error), not fall through to server search")
			require.True(t, out.IsError, "nil deps must surface a loud error result")
		})
	}
}

// TestInterceptSearch_SimilarMode_NonKnowledgeGraphNotClaimed asserts mode=similar
// over a NON-knowledge graph is NOT claimed by the similar arm (it is out of scope
// — the stored-vector resolver targets GraphKnowledge/default), so the call flows
// past to the existing reducible-graph arms instead of loud-erroring as similar.
func TestInterceptSearch_SimilarMode_NonKnowledgeGraphNotClaimed(t *testing.T) {
	res := &fakeVectorResolver{}
	deps := &interceptDeps{gc: newInterceptHarness(t, new(atomic.Int64), cannedNodesResp()), segMgr: &fakeSegmentSearcher{}, segRes: res}

	// graph=cloud + mode=similar: the similar claim's isKnowledgeDefaultGraph gate is
	// false, so the resolver is NEVER queried (the reducible-graph arm handles cloud).
	handled, _ := InterceptSearch(deps, searchParams(t, map[string]any{
		"graph": "cloud", "mode": "similar", "node_id": "n1", "account": "acct",
	}))
	require.True(t, handled, "graph=cloud is claimed by the reducible-graph arm")
	require.Equal(t, int64(0), res.calls.Load(), "the similar resolver is NOT consulted for a non-knowledge graph")
}
