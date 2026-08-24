// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// leaf_attach_equivalence_test.go is the correctness gate for the zero-wire vector
// design, and the twin of corpus_equivalence_test.go for the vector half: ONE seeded
// corpus, two runs differing in exactly one input — where the member vectors came
// from — and an assertion that nothing about the outcome differs.
//
// No float canonicalization is needed here, unlike the DeGroot surface: BitSimilarity
// is an integer popcount over fixed 32-byte vectors, so the two arms are exactly
// equal or genuinely different.

// equivLeafFake answers the ONE bulk provenance edge read with a fixed edge set, so
// both arms classify provenance identically and any divergence must come from the
// vectors.
type equivLeafFake struct{ edges []*knowledgev1.Edge }

func (f *equivLeafFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.edges, q)}, nil
}

// equivLeafCorpus seeds the five cases the equivalence claim has to survive:
//
//	CA = {a1, a2} and CB = {b1, b2} — two multi-member targets with DISTINCT centroids.
//	one     — reachable to CA only, above the linked (0.60) gate: must attach to CA.
//	both    — reachable to BOTH, closer to CB: must attach to CB by max similarity,
//	          so a resolve set that dropped a reachable cluster would flip it.
//	session — reachable to CA with NO backing edge (session-sibling by elimination):
//	          must clear the higher 0.80 tier.
//	novec   — has no vector in EITHER source: the vectorless case.
func equivLeafCorpus() (communityOf map[string]string, commSize map[string]int, adj map[string][]string, vectors map[string][]byte, edges []*knowledgev1.Edge) {
	centroidA := vec(11)
	centroidB := vec(200)

	communityOf = map[string]string{
		"a1": "CA", "a2": "CA",
		"b1": "CB", "b2": "CB",
		"one": "one", "both": "both", "session": "session", "novec": "novec",
	}
	commSize = map[string]int{"CA": 2, "CB": 2, "one": 1, "both": 1, "session": 1, "novec": 1}
	adj = map[string][]string{
		"a1": {"a2", "one", "both", "session"}, "a2": {"a1"},
		"b1": {"b2", "both"}, "b2": {"b1"},
		"one": {"a1"}, "both": {"a1", "b1"}, "session": {"a1"}, "novec": {"a1"},
	}
	vectors = map[string][]byte{
		"a1": centroidA, "a2": centroidA,
		"b1": centroidB, "b2": centroidB,
		// sim(one, CA) = 1 - 40/256 ≈ 0.844 → clears the linked gate.
		"one": vecBitsFrom(centroidA, 40),
		// sim(both, CB) = 1 - 20/256 ≈ 0.922 beats sim(both, CA), so CB must win.
		"both": vecBitsFrom(centroidB, 20),
		// sim(session, CA) = 1 - 30/256 ≈ 0.883 → clears even the 0.80 session tier.
		"session": vecBitsFrom(centroidA, 30),
		// "novec" is deliberately absent from both sources.
	}
	edges = []*knowledgev1.Edge{
		{Type: "relates-to", FromId: "one", ToId: "a1"},
		{Type: "relates-to", FromId: "both", ToId: "a1"},
		{Type: "relates-to", FromId: "both", ToId: "b1"},
		{Type: "relates-to", FromId: "novec", ToId: "a1"},
		// NO edge for "session" → session-sibling by elimination.
	}
	return communityOf, commSize, adj, vectors, edges
}

// TestLeafAttachment_ResidentDrainEquivalence asserts the partition and every stat
// counter are IDENTICAL whether member vectors were resolved from the resident
// engines or drained from the server — and, so the equality is not vacuous, that
// the two runs genuinely took different paths.
func TestLeafAttachment_ResidentDrainEquivalence(t *testing.T) {
	ctx := context.Background()

	// RESIDENT ARM: trustworthy gate + the resident fake. Its scanner is present but
	// must never be called.
	residentCommunityOf, residentCommSize, adj, vectors, edges := equivLeafCorpus()
	residentScanner := &leafScanFake{vectors: vectors}
	resident := &residentFake{vectors: vectors}
	residentLoop := (&PropagationLoop{gc: &equivLeafFake{edges: edges}, scanner: residentScanner}).
		WithVectorDeps(resident, &gateFakeVerdict{trustworthy: true, reason: "hnsw arm measured and non-degenerate"})
	residentLoop.runLeafAttachment(ctx, residentCommunityOf, residentCommSize, adj, nil, true)

	// DRAIN ARM: the SAME corpus and the SAME vectors, reached through the server
	// drain because the gate declines.
	drainCommunityOf, drainCommSize, adj2, vectors2, edges2 := equivLeafCorpus()
	drainScanner := &leafScanFake{vectors: vectors2}
	drainResident := &residentFake{vectors: vectors2}
	drainLoop := (&PropagationLoop{gc: &equivLeafFake{edges: edges2}, scanner: drainScanner}).
		WithVectorDeps(drainResident, &gateFakeVerdict{trustworthy: false, reason: "hnsw arm degenerate"})
	drainLoop.runLeafAttachment(ctx, drainCommunityOf, drainCommSize, adj2, nil, true)

	// NON-VACUITY: the arms took DIFFERENT paths. Without this, a gate that always
	// declined would make both runs the drain run and the equality below would prove
	// nothing at all.
	require.Zero(t, residentScanner.calls, "the resident arm must issue ZERO PipelineScan calls")
	require.Positive(t, resident.calls, "the resident arm resolved in-process")
	require.Positive(t, drainScanner.calls, "the drain arm went to the server")
	require.Zero(t, drainResident.calls, "the drain arm did not touch the resident engines")

	// THE EQUIVALENCE.
	assert.True(t, maps.Equal(residentCommunityOf, drainCommunityOf),
		"the partition must be identical from both vector sources:\nresident=%v\ndrain=%v", residentCommunityOf, drainCommunityOf)
	assert.True(t, maps.Equal(residentCommSize, drainCommSize),
		"community sizes must be identical:\nresident=%v\ndrain=%v", residentCommSize, drainCommSize)

	// The seed is only meaningful if it actually exercised each case, so pin the
	// outcomes rather than trusting that "equal" means "both did the right thing".
	assert.Equal(t, "CA", residentCommunityOf["one"], "the single-target leaf attached to CA")
	assert.Equal(t, "CB", residentCommunityOf["both"],
		"the two-target leaf chose its MAX-similarity target — a resolve set missing CB would have flipped this to CA")
	assert.Equal(t, "CA", residentCommunityOf["session"], "the session-sibling leaf cleared the higher 0.80 tier")
	assert.Equal(t, "novec", residentCommunityOf["novec"], "the vectorless leaf did not attach in either arm")
	assert.Equal(t, drainCommunityOf["novec"], residentCommunityOf["novec"])
}

// TestLeafAttachment_ResidentDrainStatsEquivalence compares the stat counters the
// two arms produce. It drives attachLeaves directly with each arm's resolved index
// (runLeafAttachment does not return stats), which is legitimate here because the
// claim under test is about the INDEXES the two sources produce, not about the
// scoping logic the sibling tests cover.
func TestLeafAttachment_ResidentDrainStatsEquivalence(t *testing.T) {
	ctx := context.Background()
	communityOf, commSize, adj, vectors, edges := equivLeafCorpus()
	candidates := singletonCandidates(communityOf, commSize, nil)

	residentLoop := (&PropagationLoop{gc: &equivLeafFake{edges: edges}}).
		WithVectorDeps(&residentFake{vectors: vectors}, &gateFakeVerdict{trustworthy: true, reason: "ok"})
	residentIndex, residentSource, _, err := residentLoop.resolveMemberVectors(ctx, candidates, communityOf, commSize, adj)
	require.NoError(t, err)
	require.Equal(t, vectorSourceResident, residentSource)

	drainLoop := (&PropagationLoop{gc: &equivLeafFake{edges: edges}, scanner: &leafScanFake{vectors: vectors}}).
		WithVectorDeps(&residentFake{vectors: vectors}, &gateFakeVerdict{trustworthy: false, reason: "degenerate"})
	drainIndex, drainSource, _, err := drainLoop.resolveMemberVectors(ctx, candidates, communityOf, commSize, adj)
	require.NoError(t, err)
	require.Equal(t, vectorSourceDrain, drainSource)

	// The drain index is a SUPERSET (it returns the whole graph), so equality of the
	// indexes is not expected — equality of the OUTCOME is.
	prov := buildLeafProvenance(ctx, &equivLeafFake{edges: edges}, candidates)

	co1, cs1, _, _, _ := equivLeafCorpus()
	statsResident := attachLeaves(co1, cs1, adj, residentIndex, prov, nil)
	co2, cs2, _, _, _ := equivLeafCorpus()
	statsDrain := attachLeaves(co2, cs2, adj, drainIndex, prov, nil)

	assert.Equal(t, statsDrain.candidates, statsResident.candidates)
	assert.Equal(t, statsDrain.attached, statsResident.attached)
	assert.Equal(t, statsDrain.gateVetoed, statsResident.gateVetoed)
	assert.Equal(t, statsDrain.vectorlessSkipped, statsResident.vectorlessSkipped)
	assert.Equal(t, statsDrain.vectorlessIDs, statsResident.vectorlessIDs)
	assert.Equal(t, statsDrain.byProvenance, statsResident.byProvenance,
		"the per-provenance tally must match — the vector source cannot change which gate tier applied")
	assert.Positive(t, statsResident.attached, "the fixture actually attached something (non-vacuity)")
	assert.Equal(t, 1, statsResident.vectorlessSkipped, "and actually exercised the vectorless case")
}
