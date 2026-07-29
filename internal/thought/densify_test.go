// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// existingEdgesFake serves a single RETURN_MODE_EDGES read returning the seeded
// relates-to edges (the idempotency pre-read) and nothing else.
type existingEdgesFake struct {
	edges []*knowledgev1.Edge
}

func (f *existingEdgesFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// densifyWriteFake records every create_batch Execute that carries densify edges
// (Method="topic-densify") so a test can count the batch-write round-trips and
// inspect the stamped edge metadata.
type densifyWriteFake struct {
	batchExecuteCalls int                          // create_batch Executes carrying densify edges
	wroteEdges        []*knowledgev1.BatchEdgeSpec // every densify edge across all batches
}

func (f *densifyWriteFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		var densifyEdges []*knowledgev1.BatchEdgeSpec
		for _, e := range m.GetEdges() {
			if e.GetMethod() == "topic-densify" {
				densifyEdges = append(densifyEdges, e)
			}
		}
		if len(densifyEdges) > 0 {
			f.batchExecuteCalls++
			f.wroteEdges = append(f.wroteEdges, densifyEdges...)
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestDensifyEdgeProvenance (FAILS-WHEN-ABSENT) asserts every emitted densify edge
// carries Type=relates-to, Method="topic-densify", Confidence=densifyEdgeConfidence
// (0.25) — distinct from authored (Confidence 0 / no Method) and the medoid links
// (Method "topic-similarity").
func TestDensifyEdgeProvenance(t *testing.T) {
	fake := &densifyWriteFake{}
	pairs := []densifyCandidate{
		{A: "x1", B: "x2", Score: 0.99},
		{A: "y1", B: "y2", Score: 0.97},
	}
	n, err := writeDensifyEdges(context.Background(), fake, pairs)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "two pairs → two edges written")
	require.Len(t, fake.wroteEdges, 2)
	for _, e := range fake.wroteEdges {
		assert.Equal(t, string(kgtypes.EdgeRelatesTo), e.GetType(), "densify edges are relates-to")
		assert.Equal(t, "topic-densify", e.GetMethod(), "densify edges carry the topic-densify Method")
		assert.InDelta(t, densifyEdgeConfidence, e.GetConfidence(), 1e-9, "densify edges carry the low Confidence")
		assert.NotEqual(t, "topic-similarity", e.GetMethod(), "distinct from the medoid links")
	}
}

// TestDensifyBatchSingleExecute (FAILS-WHEN-ABSENT) asserts all densify edges across
// all topics ride exactly ONE mutate(create_batch) Execute — no per-edge loop — for a
// multi-edge multi-topic input.
func TestDensifyBatchSingleExecute(t *testing.T) {
	fake := &densifyWriteFake{}
	// Four edges spanning two logical topics, written in one pass.
	pairs := []densifyCandidate{
		{A: "a1", B: "a2", Score: 0.99},
		{A: "a2", B: "a3", Score: 0.98},
		{A: "b1", B: "b2", Score: 0.97},
		{A: "b2", B: "b3", Score: 0.96},
	}
	n, err := writeDensifyEdges(context.Background(), fake, pairs)
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, 1, fake.batchExecuteCalls,
		"all densify edges across all topics must ride exactly ONE create_batch Execute")
	assert.Len(t, fake.wroteEdges, 4, "the single batch carries every edge")

	// An empty pair set is a no-op (no Execute).
	empty := &densifyWriteFake{}
	n0, err := writeDensifyEdges(context.Background(), empty, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n0)
	assert.Equal(t, 0, empty.batchExecuteCalls, "an empty pair set writes nothing")
}

// TestDensifyEdgeIsNotTensionEligible (FAILS-WHEN-ABSENT) asserts the tension
// EXCLUSION stance: a densify relates-to edge joining two
// co-topic near-duplicate thoughts with OPPOSING charged valences (|Δ|≥0.5,
// magnitudes ≥0.5) now yields ZERO tensions, because fetchTensionEdges pre-filters
// every machine-Method edge (isMachineTensionMethod) out of the tension predicate.
// A topic-densify link is clustering/near-duplicate signal, NOT propositional
// disagreement, so it must never read as a tension. Goes red if the machine-edge
// pre-filter is dropped (EdgeRelatesTo is in tensionEdgeTypes, so without the
// Method filter this edge would pair them). The joining edge still carries
// Method="topic-densify" — provenance is what the filter keys on.
func TestDensifyEdgeIsNotTensionEligible(t *testing.T) {
	// The densify edge under test: a topic-densify relates-to edge A↔B. EdgeRelatesTo
	// is in tensionEdgeTypes, but the machine-Method pre-filter drops it.
	densifyEdge := &knowledgev1.Edge{
		Type:   string(kgtypes.EdgeRelatesTo),
		FromId: "A",
		ToId:   "B",
		Method: "topic-densify",
	}
	f := newTensionFake([]*knowledgev1.Edge{densifyEdge})

	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	assert.Empty(t, tensions,
		"a densify (machine) relates-to edge must NOT surface a tension — it is clustering signal, not disagreement")

	// Provenance is what the pre-filter keys on: the edge carries the machine tag.
	assert.Equal(t, "topic-densify", densifyEdge.GetMethod(),
		"the densify edge's Method tag is the machine provenance the tension filter excludes on")
}

// vecDifferingBy returns a 32-byte vector equal to base but with the first n of the
// given flip-bit indices toggled, so BitSimilarity(base, result) = (256-n)/256.
func vecDifferingBy(base []byte, flipBits ...int) []byte {
	v := make([]byte, len(base))
	copy(v, base)
	for _, i := range flipBits {
		v[i/8] ^= 1 << uint(i%8)
	}
	return v
}

// TestDensifyKNN (FAILS-WHEN-ABSENT) covers the per-member kNN selector: with A
// within threshold of B and C but D below threshold of everything, kNN(k=2) selects
// {A-B, A-C} and nothing touching D; a vectorless member is skipped; the candidate
// set is canonical/undirected (A picks B and B picks A → one pair); deterministic.
func TestDensifyKNN(t *testing.T) {
	// Base vector A; B differs by 2 bits (sim 254/256≈0.992), C by 4 bits
	// (252/256≈0.984), D by 40 bits (216/256≈0.844 — below a 0.95 threshold).
	a := bitVec(0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	b := vecDifferingBy(a, 100, 101)
	c := vecDifferingBy(a, 100, 101, 102, 103)
	dFlips := make([]int, 0, 40)
	for i := 100; i < 140; i++ {
		dFlips = append(dFlips, i)
	}
	d := vecDifferingBy(a, dFlips...)

	vectorIndex := map[string][]byte{"A": a, "B": b, "C": c, "D": d}
	members := []string{"A", "B", "C", "D", "E"} // E has no vector → skipped

	const threshold = 0.95
	cands := selectTopicKNN(members, vectorIndex, 2, threshold)

	// Collect canonical pair keys.
	got := map[string]bool{}
	for _, p := range cands {
		assert.LessOrEqual(t, p.A, p.B, "every candidate pair is canonical (A <= B)")
		got[p.A+"|"+p.B] = true
	}

	// A is within threshold of B and C → both selected as A's top-2.
	assert.True(t, got["A|B"], "A-B (sim ~0.992) must be selected")
	assert.True(t, got["A|C"], "A-C (sim ~0.984) must be selected")
	// D is below threshold of everything → no pair touches D.
	for key := range got {
		assert.NotContains(t, key, "D", "no candidate may touch the below-threshold member D")
	}
	// E is vectorless → never appears.
	for key := range got {
		assert.NotContains(t, key, "E", "a vectorless member must be skipped")
	}

	// Undirected dedup: A picks B and B picks A, but the pair appears ONCE.
	count := 0
	for _, p := range cands {
		if (p.A == "A" && p.B == "B") || (p.A == "B" && p.B == "A") {
			count++
		}
	}
	assert.Equal(t, 1, count, "the A-B undirected pair must appear exactly once")

	// Determinism: a second run yields an identical ordered candidate slice.
	cands2 := selectTopicKNN(members, vectorIndex, 2, threshold)
	require.Equal(t, cands, cands2, "selectTopicKNN must be deterministic across runs")
}

// TestDensifyIdempotent (FAILS-WHEN-ABSENT) covers the any-provenance idempotency
// pre-read: a candidate already present as a relates-to edge in EITHER direction and
// ANY provenance is dropped, and a re-run over a topic whose kNN edges already exist
// emits zero new edges. Seeds a B→A relates-to edge and asserts candidate A-B is
// suppressed (the directional-edge-but-undirected-dedup case).
func TestDensifyIdempotent(t *testing.T) {
	// Seed an existing relates-to edge in the B→A direction (opposite the canonical
	// A-B candidate key) to prove direction-insensitive dedup.
	fake := &existingEdgesFake{edges: []*knowledgev1.Edge{
		{Type: string(kgtypes.EdgeRelatesTo), FromId: "B", ToId: "A"},
	}}
	existing, err := fetchExistingPairs(context.Background(), fake, []string{"A", "B", "C"})
	require.NoError(t, err)
	assert.True(t, existing[unorderedPairKey("A", "B")],
		"a seeded B→A edge must register the canonical A-B pair as existing")

	// Candidate set including the already-present A-B and a fresh A-C.
	cands := []densifyCandidate{
		{A: "A", B: "B", Score: 0.99},
		{A: "A", B: "C", Score: 0.97},
	}
	survivors := dropExisting(cands, existing)
	require.Len(t, survivors, 1, "the already-present A-B pair must be dropped")
	assert.Equal(t, "C", survivors[0].B, "only the fresh A-C pair survives")

	// Idempotent top-up: if BOTH candidates already exist, a re-run emits zero.
	existingAll := map[string]bool{
		unorderedPairKey("A", "B"): true,
		unorderedPairKey("A", "C"): true,
	}
	assert.Empty(t, dropExisting(cands, existingAll),
		"a re-run over a topic whose kNN edges already exist must emit zero new edges")
}

// nearVecs builds n mutually-near 32-byte vectors: vector i flips bit (200+i) off a
// shared base, so any two differ by exactly 2 bits → BitSimilarity 254/256≈0.992,
// comfortably above a 0.95 densify threshold (every pair is a kNN candidate).
func nearVecs(ids []string) map[string][]byte {
	base := bitVec(0, 1, 2, 3, 4)
	out := make(map[string][]byte, len(ids))
	for i, id := range ids {
		out[id] = vecDifferingBy(base, 200+i)
	}
	return out
}

// TestDensifyComponentEstimate (FAILS-WHEN-ABSENT) covers the cheap in-pass
// before/after structural component-count estimate: a topic whose members form 3
// disjoint relates-to components BEFORE collapses to 1 AFTER the new kNN edges, and
// the per-topic stat reports beforeComponents=3 / afterComponents=1.
func TestDensifyComponentEstimate(t *testing.T) {
	members := []string{"m1", "m2", "m3", "m4", "m5", "m6"}
	vectorIndex := nearVecs(members) // all mutually near → kNN connects them

	// BEFORE: three disjoint existing relates-to pairs → 3 components.
	existing := map[string]bool{
		unorderedPairKey("m1", "m2"): true,
		unorderedPairKey("m3", "m4"): true,
		unorderedPairKey("m5", "m6"): true,
	}

	topic := Topic{PrimaryClusterID: "c1", MedoidID: "m1", MemberThoughtIDs: members}
	// k high enough that the kNN graph is connected across the three existing pairs.
	res := computeDensifyEdges([]Topic{topic}, vectorIndex, existing, DensifyParams{Threshold: 0.95, K: 5, EdgeBudget: 1000})

	require.Len(t, res.PerTopic, 1, "the single touched topic must produce one stat")
	st := res.PerTopic[0]
	assert.Equal(t, 3, st.BeforeComponents, "three disjoint existing relates-to pairs → 3 components before")
	assert.Equal(t, 1, st.AfterComponents, "the new kNN edges must fuse the members into 1 component after")
	assert.False(t, res.BudgetHit, "a 1000-edge budget is not hit by a 6-member topic")
}

// TestDensifyBudget (FAILS-WHEN-ABSENT) covers the per-run edge budget cap: with the
// budget below the total candidate count, exactly budget edges emit, BudgetHit is
// true, and the deterministic survivor-keyed topic order makes truncation
// reproducible; with the budget at/above the total, all emit and BudgetHit is false.
func TestDensifyBudget(t *testing.T) {
	// Two topics, each a mutually-near pair → each yields exactly 1 candidate edge.
	taMembers := []string{"a1", "a2"}
	tbMembers := []string{"b1", "b2"}
	vi := map[string][]byte{}
	maps.Copy(vi, nearVecs(taMembers))
	maps.Copy(vi, nearVecs(tbMembers))
	// PrimaryClusterID orders the topics: "ca" before "cb".
	topics := []Topic{
		{PrimaryClusterID: "cb", MedoidID: "b1", MemberThoughtIDs: tbMembers},
		{PrimaryClusterID: "ca", MedoidID: "a1", MemberThoughtIDs: taMembers},
	}

	// Budget 1 with 2 total candidates → exactly 1 emits, BudgetHit true. The
	// deterministic order processes "ca" first, so the surviving edge is a1-a2.
	capped := computeDensifyEdges(topics, vi, map[string]bool{}, DensifyParams{Threshold: 0.95, K: 2, EdgeBudget: 1})
	require.Len(t, capped.Edges, 1, "budget=1 with 2 candidates → exactly 1 edge")
	assert.True(t, capped.BudgetHit, "the budget was hit")
	assert.Equal(t, "a1", capped.Edges[0].A, "the survivor-keyed order writes topic 'ca' (a1-a2) first")
	assert.Equal(t, "a2", capped.Edges[0].B)
	assert.Equal(t, 1, capped.StarvedTopics, "the second topic (cb) is starved")

	// Determinism: a re-run truncates identically.
	capped2 := computeDensifyEdges(topics, vi, map[string]bool{}, DensifyParams{Threshold: 0.95, K: 2, EdgeBudget: 1})
	require.Equal(t, capped.Edges, capped2.Edges, "budget truncation must be reproducible")

	// Budget 2 with exactly 2 candidates → all emit, BudgetHit false.
	full := computeDensifyEdges(topics, vi, map[string]bool{}, DensifyParams{Threshold: 0.95, K: 2, EdgeBudget: 2})
	assert.Len(t, full.Edges, 2, "budget=2 with 2 candidates → all emit")
	assert.False(t, full.BudgetHit, "candidates == budget → BudgetHit false")
	assert.Equal(t, 0, full.StarvedTopics, "no topic starves when all emit")
}
