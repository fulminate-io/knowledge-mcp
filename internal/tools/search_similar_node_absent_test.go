// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// seedProbeCaller is a GraphCaller that models the SERVER's ids[] read semantics
// for the seed-liveness probe, so these tests exercise the composer against the
// contract the real server honors rather than against a convenient stub:
//
//   - a seeded id is served; an unseeded id yields NO row (server:
//     query_executor.go executeByIDs skips an id that resolves to nothing);
//   - a TOMBSTONED row (TombstonedAt != 0) is served ONLY when the plan carries
//     include_tombstones — the same gate executeByIDs applies with
//     `if IsTombstoned(best) && !q.includeTombstones { continue }`.
//
// The fixture models the server; the assertion that ties it to the real one is
// plansCarryingTombstones below, which pins that the composer actually SETS
// include_tombstones on the probe plan. Without that flag on the real wire the
// deleted case would come back indistinguishable from the absent case, so the
// flag assertion — not the fake's behavior — is what makes the deleted branch
// trustworthy in production.
type seedProbeCaller struct {
	nodes map[string]*knowledgev1.Node
	// execErr, when set, fails EVERY Execute — used to drive the probe-failure
	// branch (the composer must not classify on a read it did not get).
	execErr error

	mu    sync.Mutex
	plans []*knowledgev1.QueryPlan
}

func (c *seedProbeCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	c.mu.Lock()
	c.plans = append(c.plans, q)
	c.mu.Unlock()
	if c.execErr != nil {
		return nil, c.execErr
	}
	out := make([]*knowledgev1.Node, 0, len(q.GetIds()))
	for _, id := range q.GetIds() {
		n, ok := c.nodes[id]
		if !ok {
			continue
		}
		if n.GetTombstonedAt() != 0 && !q.GetIncludeTombstones() {
			continue
		}
		out = append(out, n)
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}, nil
}

// recordedPlans returns a copy of every QueryPlan the caller saw.
func (c *seedProbeCaller) recordedPlans() []*knowledgev1.QueryPlan {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*knowledgev1.QueryPlan, len(c.plans))
	copy(out, c.plans)
	return out
}

// plansCarryingTombstones counts the recorded plans that requested tombstones
// for exactly the given id — the shape the seed-liveness probe must emit for the
// server to disclose a deleted node at all.
func plansCarryingTombstones(plans []*knowledgev1.QueryPlan, id string) int {
	n := 0
	for _, p := range plans {
		if p.GetIncludeTombstones() && len(p.GetIds()) == 1 && p.GetIds()[0] == id {
			n++
		}
	}
	return n
}

// tombstonedNode builds a seeded row that is present in the store but DELETED
// (TombstonedAt is proto field 19 on Node; 0 = live, non-zero = deleted).
func tombstonedNode(id string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "finding", TombstonedAt: 1_700_000_000_000_000_000}
}

// TestSimilarNode_DeletedSeedIsNamedAsDeleted is the RED-FIRST case for the
// follow-on gap: with VectorByID liveness-aware, a DELETED node resolves no
// stored vector, so similar-mode lands on the absent-vector branch. Its message
// must not promise an arrival that never comes — a deleted node's vector is gone
// and no amount of waiting for the embed pipeline brings it back.
func TestSimilarNode_DeletedSeedIsNamedAsDeleted(t *testing.T) {
	const id = "n_deleted"
	gc := &seedProbeCaller{nodes: map[string]*knowledgev1.Node{id: tombstonedNode(id)}}
	mgr := &fakeSegmentSearcher{}
	res := &fakeVectorResolver{ok: false} // liveness-aware VectorByID declines a deleted member.

	out := composeSimilarNodeSearch(opCtx(), gc, mgr, res, id, 5, "", nil)

	require.True(t, out.IsError, "a deleted seed must surface an error, not an empty success")
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, id, "the error names the node id")
	assert.Contains(t, body, "deleted", "the error states the node was deleted")
	assert.NotContains(t, body, "wait for the client pipeline",
		"a deleted node's vector never arrives — the wait-for-embedding guidance is wrong here")
	assert.NotContains(t, body, "yet", "'not embedded YET' promises an arrival a deleted node never gets")

	// The probe that PROVES the deletion must have asked for tombstones; without
	// this flag the real server hides the row and the branch would be a guess.
	require.Equal(t, 1, plansCarryingTombstones(gc.recordedPlans(), id),
		"the deleted verdict rides one ids[]+include_tombstones read of the seed id")
	require.Equal(t, int64(0), mgr.calls.Load(), "no client search once the seed is known deleted")
}

// TestSimilarNode_LiveUnembeddedSeedKeepsWaitGuidance is the CONTROL: a live
// node that simply has not been embedded yet keeps the existing wait-for-the-
// pipeline guidance, which for it is true. It is what proves the deleted branch
// did not over-trigger and swallow the case the message was written for.
func TestSimilarNode_LiveUnembeddedSeedKeepsWaitGuidance(t *testing.T) {
	const id = "n_live_unembedded"
	gc := &seedProbeCaller{nodes: map[string]*knowledgev1.Node{
		id: {Id: id, Type: "finding"}, // TombstonedAt zero → live.
	}}
	mgr := &fakeSegmentSearcher{}
	res := &fakeVectorResolver{ok: false}

	out := composeSimilarNodeSearch(opCtx(), gc, mgr, res, id, 5, "", nil)

	require.True(t, out.IsError)
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, id)
	assert.Contains(t, body, "no stored vector yet", "a live node's vector genuinely is still coming")
	assert.Contains(t, body, "wait for the client pipeline")
	assert.NotContains(t, body, "was deleted", "a live node must not be reported as deleted")
	assert.NotContains(t, body, "no such node", "a live node must not be reported as absent")
	require.Equal(t, int64(0), mgr.calls.Load())
}

// TestSimilarNode_NonexistentSeedIsNamedAsAbsent: an id that resolves to no row
// at all — with tombstones REQUESTED, so absence cannot be a hidden tombstone —
// is reported as no such node, not as an embedding that is still on its way.
func TestSimilarNode_NonexistentSeedIsNamedAsAbsent(t *testing.T) {
	const id = "n_never_existed"
	gc := &seedProbeCaller{nodes: map[string]*knowledgev1.Node{}}
	mgr := &fakeSegmentSearcher{}
	res := &fakeVectorResolver{ok: false}

	out := composeSimilarNodeSearch(opCtx(), gc, mgr, res, id, 5, "", nil)

	require.True(t, out.IsError)
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, id)
	assert.Contains(t, body, "no such node", "an absent id is named as absent")
	assert.NotContains(t, body, "wait for the client pipeline",
		"there is nothing to wait for when the node does not exist")
	assert.NotContains(t, body, "was deleted", "absent is not the same claim as deleted")
	require.Equal(t, 1, plansCarryingTombstones(gc.recordedPlans(), id),
		"the absent verdict is only sound because the probe asked for tombstones too")
	require.Equal(t, int64(0), mgr.calls.Load())
}

// TestSimilarNode_SeedProbeFailureIsNotClassified: when the liveness probe itself
// fails, the composer must say the vector is absent AND that it could not tell
// which cause — never pick one of the three verdicts on a read it did not get.
func TestSimilarNode_SeedProbeFailureIsNotClassified(t *testing.T) {
	const id = "n_probe_broken"
	gc := &seedProbeCaller{execErr: errors.New("wire is down")}
	mgr := &fakeSegmentSearcher{}
	res := &fakeVectorResolver{ok: false}

	out := composeSimilarNodeSearch(opCtx(), gc, mgr, res, id, 5, "", nil)

	require.True(t, out.IsError)
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, id)
	assert.Contains(t, body, "wire is down", "the underlying probe failure is surfaced, not swallowed")
	assert.NotContains(t, body, "was deleted", "an unread probe must not manufacture a deleted verdict")
	assert.NotContains(t, body, "no such node", "an unread probe must not manufacture an absent verdict")
	assert.NotContains(t, body, "wait for the client pipeline",
		"an unread probe must not manufacture a still-embedding verdict")
	require.Equal(t, int64(0), mgr.calls.Load())
}

// TestSimilarNode_LiveEmbeddedSeedStillSearches is the KNOWN-POSITIVE: the
// happy path is untouched — a resolvable stored vector still drives the client
// engine and renders neighbors, and the liveness probe never fires (so the new
// branch costs the successful search nothing and cannot mis-claim on it).
func TestSimilarNode_LiveEmbeddedSeedStillSearches(t *testing.T) {
	const id = "n_self"
	gc := &seedProbeCaller{nodes: map[string]*knowledgev1.Node{
		"n1": {Id: "n1", Type: "finding", SymbolName: "NeighborOne"},
		"n2": {Id: "n2", Type: "finding", SymbolName: "NeighborTwo"},
	}}
	storedVec := []byte{0xAB, 0xCD, 0xEF, 0x01}
	res := &fakeVectorResolver{vec: storedVec, ok: true}
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{
		{ID: id, Score: 1.0},
		{ID: "n1", Score: 0.9},
		{ID: "n2", Score: 0.8},
	}}

	out := composeSimilarNodeSearch(opCtx(), gc, mgr, res, id, 2, "", nil)

	require.False(t, out.IsError, "a resolvable stored vector still searches: %v", engine.FirstTextContent(out))
	body := engine.FirstTextContent(out)
	assert.Contains(t, body, "NeighborOne")
	assert.Contains(t, body, "NeighborTwo")
	assert.NotContains(t, body, id, "self-exclusion is unchanged")

	require.Equal(t, int64(1), mgr.calls.Load(), "the client engine still ran")
	plans := gc.recordedPlans()
	require.Len(t, plans, 1, "exactly one wire read on the happy path — the neighbor hydrate")
	require.Equal(t, 0, plansCarryingTombstones(plans, id),
		"the seed-liveness probe is error-path only: a successful search must not pay for it")
}
