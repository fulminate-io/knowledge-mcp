// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// loop_leaf_scope_test.go covers the dirty-seed scoping of leaf attachment. Every
// test here drives p.runLeafAttachment, NEVER attachLeaves directly: the seed
// derivation, the pendingLeafRetry union, the candidates-first early return and the
// resident/drain choice ALL live in runLeafAttachment, so a test calling
// attachLeaves would exercise none of it and would pass against an unscoped
// implementation.

// scopeCorpus is a two-cluster fixture with TWO independent singleton leaves, so a
// seed naming one of them is distinguishable from no seed at all:
//
//	cluster c1 = {m1, m2}, leafA adjacent to m1
//	cluster c2 = {n1, n2}, leafB adjacent to n1
func scopeCorpus() (communityOf map[string]string, commSize map[string]int, adj map[string][]string, vectors map[string][]byte) {
	v := bitVec(0, 1, 2, 3, 4, 5, 6, 7)
	communityOf = map[string]string{
		"m1": "c1", "m2": "c1", "leafA": "leafA",
		"n1": "c2", "n2": "c2", "leafB": "leafB",
	}
	commSize = map[string]int{"c1": 2, "c2": 2, "leafA": 1, "leafB": 1}
	adj = map[string][]string{
		"leafA": {"m1"}, "m1": {"leafA", "m2"}, "m2": {"m1"},
		"leafB": {"n1"}, "n1": {"leafB", "n2"}, "n2": {"n1"},
	}
	vectors = map[string][]byte{
		"m1": v, "m2": v, "leafA": v,
		"n1": v, "n2": v, "leafB": v,
	}
	return communityOf, commSize, adj, vectors
}

// scopeProvenanceFake gives both leaves a real relates-to edge to their neighbor so
// each gates at the linked (0.60) tier.
type scopeProvenanceFake struct{}

func (scopeProvenanceFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Edges: []*knowledgev1.Edge{
		{Type: "relates-to", FromId: "leafA", ToId: "m1"},
		{Type: "relates-to", FromId: "leafB", ToId: "n1"},
	}}, nil
}

// TestLeafAttachment_NoCandidates_ZeroScans is the incrementality gate: a warm pass
// whose dirty seed contains no singleton does ZERO vector work of either kind —
// no PipelineScan calls AND no resident lookups — because candidates are computed
// BEFORE any vector resolution. Before the ordering fix the pass drained the whole
// vector index first and only then discovered it had nothing to do.
func TestLeafAttachment_NoCandidates_ZeroScans(t *testing.T) {
	communityOf, commSize, adj, vectors := scopeCorpus()
	resident := &residentFake{vectors: vectors}
	gate := &gateFakeVerdict{trustworthy: true, reason: "ok"}
	scanner := &leafScanFake{vectors: vectors}

	p := (&PropagationLoop{gc: scopeProvenanceFake{}, scanner: scanner}).
		WithVectorDeps(resident, gate)

	// A warm pass (isFull=false) whose dirty seed names only CLUSTERED nodes: no
	// singleton is in it, so there is nothing to attach.
	p.runLeafAttachment(context.Background(), communityOf, commSize, adj,
		map[string]bool{"m1": true, "n2": true}, false)

	assert.Equal(t, 0, scanner.calls, "a pass with no candidates must issue ZERO PipelineScan calls")
	assert.Equal(t, 0, resident.calls, "and ZERO resident lookups — the vector work is skipped entirely, not merely redirected")
	assert.Equal(t, 0, gate.calls, "the coverage gate is not even probed when there is nothing to attach")
	assert.Equal(t, "leafA", communityOf["leafA"], "no leaf moved")
	assert.Equal(t, "leafB", communityOf["leafB"])
}

// TestLeafAttachment_DirtySeedScopesCandidates proves the seed actually NARROWS the
// work: a warm pass seeded with one leaf attaches only that leaf, while a full pass
// (nil seed) attaches both. Without the scoping the warm pass would attach both and
// this test goes red.
func TestLeafAttachment_DirtySeedScopesCandidates(t *testing.T) {
	ctx := context.Background()

	t.Run("warm pass attaches only the seeded singleton", func(t *testing.T) {
		communityOf, commSize, adj, vectors := scopeCorpus()
		resident := &residentFake{vectors: vectors}
		gate := &gateFakeVerdict{trustworthy: true, reason: "ok"}

		p := (&PropagationLoop{gc: scopeProvenanceFake{}}).WithVectorDeps(resident, gate)
		p.runLeafAttachment(ctx, communityOf, commSize, adj, map[string]bool{"leafA": true}, false)

		assert.Equal(t, "c1", communityOf["leafA"], "the seeded leaf attached")
		assert.Equal(t, "leafB", communityOf["leafB"],
			"the UNSEEDED leaf was not even considered — that is the scoping")
	})

	t.Run("full pass attaches both", func(t *testing.T) {
		communityOf, commSize, adj, vectors := scopeCorpus()
		resident := &residentFake{vectors: vectors}
		gate := &gateFakeVerdict{trustworthy: true, reason: "ok"}

		p := (&PropagationLoop{gc: scopeProvenanceFake{}}).WithVectorDeps(resident, gate)
		p.runLeafAttachment(ctx, communityOf, commSize, adj, nil, true)

		assert.Equal(t, "c1", communityOf["leafA"])
		assert.Equal(t, "c2", communityOf["leafB"],
			"a full pass carries a nil seed, so every singleton is a candidate again — the backstop that bounds the scoping approximation")
	})
}

// TestLeafAttachment_VectorlessLeafRetriedNextPass is the freshness net. A leaf
// whose vector has not shipped yet is skipped, and — critically — it does NOT
// re-enter the dirty seed when the vector later arrives, because the embed
// writeback does not re-dirty the node. Without pendingLeafRetry it would wait for
// the 24h full-pass backstop; with it, the very next warm pass attaches it even
// though the seed does not name it.
func TestLeafAttachment_VectorlessLeafRetriedNextPass(t *testing.T) {
	ctx := context.Background()
	communityOf, commSize, adj, vectors := scopeCorpus()

	// PASS 1: leafA has no vector yet (it is absent from the resident index).
	pass1Vectors := map[string][]byte{}
	for id, v := range vectors {
		if id == "leafA" {
			continue
		}
		pass1Vectors[id] = v
	}
	resident := &residentFake{vectors: pass1Vectors}
	gate := &gateFakeVerdict{trustworthy: true, reason: "ok"}
	p := (&PropagationLoop{gc: scopeProvenanceFake{}}).WithVectorDeps(resident, gate)

	p.runLeafAttachment(ctx, communityOf, commSize, adj, map[string]bool{"leafA": true}, false)
	require.Equal(t, "leafA", communityOf["leafA"], "a vectorless leaf cannot be gated, so it does not attach")
	require.True(t, p.pendingLeafRetry["leafA"], "it is recorded for retry instead of being forgotten")

	// PASS 2: the vector has shipped. The dirty seed is EMPTY — the embed writeback
	// did not re-dirty the node — so only pendingLeafRetry can bring it back.
	resident.vectors = vectors
	p.runLeafAttachment(ctx, communityOf, commSize, adj, map[string]bool{}, false)

	assert.Equal(t, "c1", communityOf["leafA"],
		"the retried leaf attached on the next warm pass, without appearing in the dirty seed")
	assert.Empty(t, p.pendingLeafRetry,
		"and it dropped out of the retry set — the set is REPLACED each pass, so it cannot grow without bound")
}
