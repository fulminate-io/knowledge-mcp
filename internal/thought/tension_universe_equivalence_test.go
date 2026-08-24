// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// tension_universe_equivalence_test.go is the correctness gate for the
// charged-universe inversion: ReflectTensions now derives its universe from the
// CHARGE set (charges -> EdgeChargedBy parents -> claim-type filter) instead of
// draining the whole thought+finding+research corpus. These tests pin BOTH halves of
// that claim — the report is identical to the one the old universe produced, and the
// claim corpus is never drained. Written against the widened (ctx, gc, src)
// signature, so they compile only once the inversion has landed.
//
// It lives beside corpus_equivalence_test.go rather than inside it because that file
// sits at 490 lines, against the project's 500-line cap; the fake it defines
// (reflectEquivFake) is reused here in full.

// tensionEquivFake wraps reflectEquivFake with the three things the tension universe
// needs and the base fake does not model: claim nodes of MIXED type (finding /
// research, not just thought), a per-edge Method (so a machine-provenance edge can be
// seeded), and a per-plan tally of browses + Execute calls so a test can assert which
// reads were issued. Node hydration, charge storage and node-set edge scoping are all
// delegated to the embedded fake.
type tensionEquivFake struct {
	*reflectEquivFake
	chargeIDs   []string // deterministic charge-browse order
	edgeMethods map[[2]string]string

	browsedTypes []string
	execCalls    int
}

func (f *tensionEquivFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execCalls++
	q := req.GetQuery()
	// The edges branch comes FIRST: an edges read is a node-SET query, so it carries
	// Ids and would otherwise fall through to the hydrate delegation below and never
	// get its Method stamped.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		edges := f.reflectEquivFake.edgesFor(q)
		for _, e := range edges {
			if m, ok := f.edgeMethods[[2]string{e.GetFromId(), e.GetToId()}]; ok {
				e.Method = m
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(edges, q)}, nil
	}
	if q == nil || q.GetById() != "" || len(q.GetIds()) > 0 {
		return f.reflectEquivFake.Execute(ctx, req)
	}
	// A type browse. Record the type, then serve it: charges in seeded order, claim
	// nodes filtered by the requested type (so the OLD three-type universe drain is
	// answerable too, which is what the golden below replays).
	wantType := q.GetSelection().GetNodeType()
	f.browsedTypes = append(f.browsedTypes, wantType)
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil // single-page corpus.
	}
	if wantType == string(kgtypes.NodeCharge) {
		out := make([]*knowledgev1.Node, 0, len(f.chargeIDs))
		for _, id := range f.chargeIDs {
			out = append(out, cloneNode(f.charges[id]))
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
	out := make([]*knowledgev1.Node, 0, len(f.thoughtIDs))
	for _, id := range f.thoughtIDs {
		if n := f.thoughts[id]; n.GetType() == wantType {
			out = append(out, cloneNode(n))
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}, nil
}

// chargeNodesSlice returns the seeded charge nodes in stable order — the resident
// cache feed for the warm-source runs.
func (f *tensionEquivFake) chargeNodesSlice() []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(f.chargeIDs))
	for _, id := range f.chargeIDs {
		out = append(out, cloneNode(f.charges[id]))
	}
	return out
}

// newTensionEquivFake seeds the six-case corpus the inversion has to preserve
// EXACTLY. Charges carry UpdatedAt=0, so every recency scalar is a now-independent
// 0.5 and each magnitude below is ln(1 + 0.5*totalWeight):
//
//	a. th-a1 <-> th-a2 — charged thought pair, opposing valence, both magnitudes
//	   well over 0.5 (0.5*7 -> ln(4.5) = 1.50). MUST appear.
//	b. f-b1 <-> f-b2 — charged FINDING pair. MUST appear: this is the case a
//	   thought-seeded inversion would silently drop. f-b1 carries two positive
//	   charges, so this pair also carries the most distinct evidence (3) and ranks
//	   first.
//	c. th-c1 <-> r-c2 — charged thought <-> charged RESEARCH (mixed types). MUST
//	   appear. th-c1 is mixed-polarity (valence +0.714), so its evidence-weighted
//	   delta is distinct from every other pair and the ranking is total.
//	d. th-d1 (weight 2 -> 0.5*2 = 1.0 -> magnitude ln(2) = 0.693, QUALIFIES) <->
//	   th-d2 (weight 1 -> 0.5 -> magnitude ln(1.5) = 0.405, BELOW the 0.5 gate).
//	   MUST NOT appear — proving the magnitude gate, not the universe, is the
//	   discriminator.
//	e. th-e1 <-> th-e2 — UNCHARGED pair joined by a tension edge. MUST NOT appear.
//	f. th-f1 <-> th-f2 — charged, opposing, but joined by a densify-Method
//	   (machine) relates-to edge. MUST NOT appear: isMachineTensionMethod survives.
//
// No claim node carries a cluster_id, so every qualifying pair is its own per-id
// singleton group (PairCount 1) and none collapse into one another.
func newTensionEquivFake() *tensionEquivFake {
	spec := equivCorpusSpec{
		thoughts: []equivThoughtSpec{
			{"th-a1", "", ""}, {"th-a2", "", ""},
			{"f-b1", "", ""}, {"f-b2", "", ""},
			{"th-c1", "", ""}, {"r-c2", "", ""},
			{"th-d1", "", ""}, {"th-d2", "", ""},
			{"th-e1", "", ""}, {"th-e2", "", ""},
			{"th-f1", "", ""}, {"th-f2", "", ""},
		},
		charges: []equivChargeSpec{
			{"c-a1", "th-a1", "positive", "7"},
			{"c-a2", "th-a2", "negative", "7"},
			{"c-b1x", "f-b1", "positive", "5"}, {"c-b1y", "f-b1", "positive", "3"},
			{"c-b2", "f-b2", "negative", "5"},
			{"c-c1p", "th-c1", "positive", "6"}, {"c-c1n", "th-c1", "negative", "1"},
			{"c-c2", "r-c2", "negative", "6"},
			{"c-d1", "th-d1", "positive", "2"},
			{"c-d2", "th-d2", "negative", "1"},
			{"c-f1", "th-f1", "positive", "7"},
			{"c-f2", "th-f2", "negative", "7"},
		},
		edges: [][2]string{
			{"th-a1", "th-a2"},
			{"f-b1", "f-b2"},
			{"th-c1", "r-c2"},
			{"th-d1", "th-d2"},
			{"th-e1", "th-e2"},
			{"th-f1", "th-f2"},
		},
	}
	inner := newReflectEquivFake(spec)
	// Retype the claim nodes the base spec can only express as thoughts. The
	// universe admits all three chargeable claim types; the pair in (b) is a
	// finding pair and (c) crosses thought and research.
	inner.thoughts["f-b1"].Type = string(kgtypes.NodeFinding)
	inner.thoughts["f-b2"].Type = string(kgtypes.NodeFinding)
	inner.thoughts["r-c2"].Type = string(kgtypes.NodeResearch)

	f := &tensionEquivFake{
		reflectEquivFake: inner,
		edgeMethods:      map[[2]string]string{{"th-f1", "th-f2"}: densifyMethod},
	}
	for _, cs := range spec.charges {
		f.chargeIDs = append(f.chargeIDs, cs.id)
	}
	return f
}

// oldUniverseTensions replays the PRE-INVERSION algorithm as an independent oracle:
// drain all three claim types, read the tension edges over that whole universe, drop
// machine edges, hydrate every node and fetch every charge, then run the UNCHANGED
// downstream pairing/collapse/rank helpers. Any divergence between this and
// ReflectTensions is a real behavior change, not a refactor.
func oldUniverseTensions(t *testing.T, gc Caller) []TensionReport {
	t.Helper()
	ctx := context.Background()

	var nodes []*knowledgev1.Node
	for _, nt := range []kgtypes.NodeType{kgtypes.NodeThought, kgtypes.NodeFinding, kgtypes.NodeResearch} {
		got, err := drainThoughtBrowse(ctx, gc, string(nt), browsePageSize)
		require.NoError(t, err)
		nodes = append(nodes, got...)
	}
	nodeIDs := make([]string, 0, len(nodes))
	idSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, n.Id)
		idSet[n.Id] = true
	}

	edges, err := fetchEdgesForNodeSet(ctx, gc, nodeIDs, tensionEdgeTypes)
	require.NoError(t, err)
	humanEdges := make([]*knowledgev1.Edge, 0, len(edges))
	for i := range edges {
		if isMachineTensionMethod(edges[i].GetMethod()) {
			continue
		}
		humanEdges = append(humanEdges, &edges[i])
	}

	nodeByID := fetchNodesByIDs(ctx, gc, nodeIDs)
	charges := fetchChargesFor(ctx, gc, nodeIDs, nil)
	now := time.Now()
	propsCache := make(map[string]ThoughtProperties, len(nodeIDs))
	for _, id := range nodeIDs {
		propsCache[id] = computePropertiesFromCharges(charges[id], now)
	}
	candidates := buildTensionCandidates(humanEdges, idSet, propsCache, nodeByID, charges)
	return rankAndCapTensions(collapseTensionsByCluster(candidates, nodeByID))
}

// pairKeys renders each report as an unordered endpoint-id pair, so two runs compare
// on WHICH pairs surfaced independently of which endpoint landed in NodeA.
func pairKeys(reports []TensionReport) []string {
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		a, b := r.NodeA.GetId(), r.NodeB.GetId()
		if a > b {
			a, b = b, a
		}
		out = append(out, a+"|"+b)
	}
	return out
}

// TestReflectTensions_ChargedUniverseEquivalence proves the inversion is exact: the
// tension report from the charged universe equals the report the OLD full-corpus
// universe produced, from BOTH vector sources (a nil source that drains a type=charge
// browse, and a warm resident cache that serves the charges in-process).
func TestReflectTensions_ChargedUniverseEquivalence(t *testing.T) {
	ctx := context.Background()

	golden := oldUniverseTensions(t, newTensionEquivFake())

	// Non-vacuity: the oracle must actually produce the three qualifying pairs and
	// exclude the three disqualified ones, or the equality below proves nothing.
	require.Len(t, golden, 3, "the old universe yields exactly the three qualifying pairs")
	// Evidence-weighted delta (delta x (1+DistinctEvidence)): finding pair 2x4=8.0,
	// mixed-type pair 1.714x4=6.86, thought pair 2x3=6.0 — a total order, so the
	// ranking is not a tie-break artifact. pairKeys sorts each pair's endpoints.
	assert.Equal(t, []string{"f-b1|f-b2", "r-c2|th-c1", "th-a1|th-a2"}, pairKeys(golden),
		"ranked by evidence-weighted delta: the two-charge finding pair, then the mixed-type pair, then the thought pair")

	drained, err := ReflectTensions(ctx, newTensionEquivFake(), nil)
	require.NoError(t, err)
	require.Equal(t, golden, drained,
		"the charged universe (cold, type=charge drain) must reproduce the old universe's report exactly")

	warmFake := newTensionEquivFake()
	warm := equivLoop(warmFake.reflectEquivFake, cacheFromNodes(warmFake.chargeNodesSlice()))
	warmed, err := ReflectTensions(ctx, warmFake, warm)
	require.NoError(t, err)
	require.Equal(t, golden, warmed,
		"the charged universe served from the resident cache must reproduce the same report")

	// The three exclusions, named individually so a regression says WHICH one broke.
	got := pairKeys(warmed)
	assert.NotContains(t, got, "th-d1|th-d2", "a below-magnitude endpoint still disqualifies its pair")
	assert.NotContains(t, got, "th-e1|th-e2", "an uncharged pair cannot qualify (and never enters the universe)")
	assert.NotContains(t, got, "th-f1|th-f2", "a machine-Method edge is still not a tension")
}

// TestReflectTensions_NoClaimCorpusDrain proves the cost half of the inversion: on
// the warm path the claim corpus is NEVER browsed, and the whole pass costs a
// bounded, enumerable set of reads — (1) the EdgeChargedBy read over the charge ids,
// (2) the charged-parent hydrate, (3) fetchTensionEdges' own tensionEdgeTypes read,
// which the inversion leaves untouched. The cold path browses exactly one type: charge.
//
// READ (1) IS NOW A BANDED SWEEP, so it costs paging.EdgeBandCount Executes rather
// than one, and the ceiling is EdgeBandCount+2 rather than 3. The bound is DERIVED
// from that structure, not fitted to an observation: reads (2) and (3) are still one
// Execute each and neither is banded. Expressed against the constant so a change to
// the band count moves this ceiling with it instead of silently breaking the test.
func TestReflectTensions_NoClaimCorpusDrain(t *testing.T) {
	ctx := context.Background()

	warmFake := newTensionEquivFake()
	warm := equivLoop(warmFake.reflectEquivFake, cacheFromNodes(warmFake.chargeNodesSlice()))
	tensions, err := ReflectTensions(ctx, warmFake, warm)
	require.NoError(t, err)
	require.NotEmpty(t, tensions, "the warm run must still produce tensions — a zero-read pass over an empty universe would pass vacuously")

	for _, bt := range warmFake.browsedTypes {
		assert.NotContains(t,
			[]string{string(kgtypes.NodeThought), string(kgtypes.NodeFinding), string(kgtypes.NodeResearch)}, bt,
			"the warm path must never browse the claim corpus")
	}
	assert.Empty(t, warmFake.browsedTypes, "a warm charge source issues no browse at all")
	assert.LessOrEqual(t, warmFake.execCalls, paging.EdgeBandCount+2,
		"warm pass = the BANDED charged-by sweep (EdgeBandCount Executes) + parent hydrate + "+
			"tension edges, and nothing else")

	coldFake := newTensionEquivFake()
	coldTensions, err := ReflectTensions(ctx, coldFake, nil)
	require.NoError(t, err)
	require.NotEmpty(t, coldTensions, "the cold run must still produce tensions")
	assert.Equal(t, []string{string(kgtypes.NodeCharge)}, coldFake.browsedTypes,
		"the cold fallback browses the charge seed and nothing else")
}
