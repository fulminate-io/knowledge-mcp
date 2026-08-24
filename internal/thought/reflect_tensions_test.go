// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// tensionFake is a stateful Caller for the ReflectTensions predicate tests,
// modeled on scoped_equivalence_test.go's equivalenceFake (same Execute shape +
// cloneNode hydration) but extended to (a) filter the RETURN_MODE_EDGES read by
// the requested edge-type set so a tensionEdgeTypes read and an EdgeChargedBy
// read get different answers, and (b) hydrate charge nodes so the real
// computePropertiesFromCharges derivation runs. It serves exactly the reads
// fetchTensionEdges issues (the hydrate + charge join now happen inside it):
//
//   - the type=charge universe seed browse (fetchTensionUniverseNodes inverts the
//     universe onto the charged set) → the seeded charge nodes; any other
//     Selection.NodeType browse still serves the matching seeded claim nodes;
//   - a RETURN_MODE_EDGES read filtered to tensionEdgeTypes → tensionEdges;
//   - a RETURN_MODE_EDGES read filtered to EdgeChargedBy → chargeEdges
//     (thought→charge), driving the per-thought valence;
//   - an Ids hydration → the requested thought / charge nodes.
type tensionFake struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
	charges  map[string]*knowledgev1.Node // charge-node id → node (weight + polarity)

	// explicit thought↔thought reasoning edges (the tension predicate input).
	tensionEdges []*knowledgev1.Edge
	// thought→charge EdgeChargedBy edges (FromId thought, ToId charge).
	chargeEdges []*knowledgev1.Edge
}

func (f *tensionFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// Branch on the requested edge-type set: a charged-by read returns the
		// thought→charge edges; any other (tensionEdgeTypes) read returns the
		// explicit reasoning edges. fetchTensionEdges NEVER reads
		// EdgeKGContains (the session-sibling expansion is gone), so there is no
		// session read to answer here — that absence is the predicate fix.
		wantCharged := false
		requested := map[string]bool{}
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				requested[et] = true
				if et == string(kgtypes.EdgeChargedBy) {
					wantCharged = true
				}
			}
		}
		if wantCharged {
			return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.chargeEdges, q)}, nil
		}
		// Honor the requested edge-type set exactly as the real
		// fetchEdgesForNodeSet does: it writes the tensionEdgeTypes strings into
		// Selection.EdgeTypes (wire.go), so the fake must return ONLY edges whose
		// Type is in that set. This is what makes the next-only test
		// fix-sensitive — after EdgeNext/EdgeBranchesFrom were dropped from
		// tensionEdgeTypes, a "next" edge is no longer requested and the fake
		// omits it. An empty requested set (no Selection) is a passthrough,
		// preserving the cases that read with no edge-type filter.
		if len(requested) == 0 {
			return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.tensionEdges, q)}, nil
		}
		filtered := make([]*knowledgev1.Edge, 0, len(f.tensionEdges))
		for _, e := range f.tensionEdges {
			if requested[e.GetType()] {
				filtered = append(filtered, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(filtered, q)}, nil
	}

	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := f.thoughts[id]; ok {
				nodes = append(nodes, cloneNode(n))
			} else if c, ok := f.charges[id]; ok {
				nodes = append(nodes, cloneNode(c))
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// A type browse (first page), answered by the requested Selection.NodeType.
	// fetchTensionUniverseNodes seeds from the CHARGE set, so the charge browse is
	// the one the cold (nil-src) universe build takes; it is served in chargeEdges
	// order for determinism. The claim-type browses remain answerable so a fixture
	// that still drains one behaves as before.
	wantType := ""
	if sel := q.GetSelection(); sel != nil {
		wantType = sel.GetNodeType()
	}
	if wantType == string(kgtypes.NodeCharge) {
		return &knowledgev1.ExecuteResponse{Nodes: f.chargeBrowse()}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range f.order {
		n := f.thoughts[id]
		if wantType != "" && n.Type != wantType {
			continue
		}
		nodes = append(nodes, cloneNode(n))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// chargeBrowse serves the seeded charge nodes in chargeEdges order (deterministic,
// unlike a map walk), each cloned exactly as the hydrate arm does.
func (f *tensionFake) chargeBrowse() []*knowledgev1.Node {
	var out []*knowledgev1.Node
	seen := map[string]bool{}
	for _, e := range f.chargeEdges {
		if seen[e.GetToId()] {
			continue
		}
		seen[e.GetToId()] = true
		if c, ok := f.charges[e.GetToId()]; ok {
			out = append(out, cloneNode(c))
		}
	}
	return out
}

// newTensionFake builds a two-thought corpus A (one positive charge) and B (one
// negative charge), both magnitude≥0.5 and |Δvalence|=2. tensionEdges carries
// the explicit thought↔thought edges under test (nil for the no-pair case).
func newTensionFake(tensionEdges []*knowledgev1.Edge) *tensionFake {
	mkThought := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	}
	mkCharge := func(id, polarity string) *knowledgev1.Node {
		n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
		kgtypes.SetValue(n, "polarity", polarity)
		kgtypes.SetValue(n, "weight", "7")
		return n
	}
	return &tensionFake{
		thoughts: map[string]*knowledgev1.Node{"A": mkThought("A"), "B": mkThought("B")},
		order:    []string{"A", "B"},
		charges: map[string]*knowledgev1.Node{
			"cA": mkCharge("cA", "positive"), // A → valence +1
			"cB": mkCharge("cB", "negative"), // B → valence -1
		},
		tensionEdges: tensionEdges,
		chargeEdges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeChargedBy), FromId: "A", ToId: "cA"},
			{Type: string(kgtypes.EdgeChargedBy), FromId: "B", ToId: "cB"},
		},
	}
}

// TestReflectTensions_NoExplicitEdge_NoPair (FAILS-WHEN-ABSENT) proves the
// predicate fix: two opposite-valence thoughts in the same session, but with NO
// explicit thought↔thought reasoning edge between them, yield ZERO tensions.
// fetchTensionEdges never reads session membership, so bare co-session
// co-membership can no longer pair them — the old fetchAdjacency("all") would
// have paired them via deriveSessionSiblings.
func TestReflectTensions_NoExplicitEdge_NoPair(t *testing.T) {
	f := newTensionFake(nil) // no explicit edge between A and B
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	assert.Empty(t, tensions,
		"opposite-valence thoughts with no explicit reasoning edge must NOT pair (co-session membership alone is not a tension)")
}

// TestReflectTensions_ContradictsEdge_PairsOnce (FAILS-WHEN-ABSENT) proves an
// explicit `contradicts` edge between the same two opposite-valence thoughts DOES
// pair them — exactly one TensionReport with ValenceDelta ~2.0.
func TestReflectTensions_ContradictsEdge_PairsOnce(t *testing.T) {
	f := newTensionFake([]*knowledgev1.Edge{
		{Type: "contradicts", FromId: "A", ToId: "B"},
	})
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	require.Len(t, tensions, 1,
		"an explicit contradicts edge between opposite-valence thoughts must yield exactly one tension")
	assert.InDelta(t, 2.0, tensions[0].ValenceDelta, 1e-9,
		"the tension's valence delta is the full +1/-1 swing")
}

// TestReflectTensions_MachineEdge_NoPair (FAILS-WHEN-ABSENT) proves the
// machine-edge pre-filter inside fetchTensionEdges: two opposite-valence
// thoughts joined ONLY by a topic-densify (machine-Method) relates-to edge yield
// ZERO tensions. Goes red if the isMachineTensionMethod pre-filter is dropped —
// EdgeRelatesTo is in tensionEdgeTypes, so without the Method filter this edge
// would pair them.
func TestReflectTensions_MachineEdge_NoPair(t *testing.T) {
	f := newTensionFake([]*knowledgev1.Edge{
		{Type: string(kgtypes.EdgeRelatesTo), FromId: "A", ToId: "B", Method: densifyMethod},
	})
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	assert.Empty(t, tensions,
		"a machine topic-densify relates-to edge must NOT pair thoughts (machine links are clustering signal, not tension)")
}

// TestReflectTensions_NextEdge_NoPair (FAILS-WHEN-ABSENT) proves the temporal-edge
// exclusion: two opposite-valence thoughts joined ONLY by a `next` edge yield ZERO
// tensions. EdgeNext is auto-wired between consecutive same-session thoughts by
// creation order with zero semantic evaluation, so a same-session plan→blocker arc
// is a temporal sequence, not a propositional disagreement. Goes red if EdgeNext is
// re-added to tensionEdgeTypes: fetchTensionEdges would then request "next", the
// fake would return the edge, and the qualifying pair would surface.
func TestReflectTensions_NextEdge_NoPair(t *testing.T) {
	f := newTensionFake([]*knowledgev1.Edge{
		{Type: string(kgtypes.EdgeNext), FromId: "A", ToId: "B"},
	})
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	assert.Empty(t, tensions,
		"a next-only opposing-valence pair is a same-session temporal arc, not a tension — the temporal edge types are excluded from tensionEdgeTypes")
}

// TestReflectTensions_RelatesToEdge_PairsOnce (FAILS-WHEN-ABSENT) is the positive
// control matching the next-removal change: a human-authored `relates-to` edge
// (empty Method, so it survives isMachineTensionMethod) between the same two
// opposite-valence thoughts DOES pair them — exactly one TensionReport with
// ValenceDelta ~2.0. Proves a genuine semantic edge still surfaces while the
// temporal type does not.
func TestReflectTensions_RelatesToEdge_PairsOnce(t *testing.T) {
	f := newTensionFake([]*knowledgev1.Edge{
		{Type: string(kgtypes.EdgeRelatesTo), FromId: "A", ToId: "B"},
	})
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	require.Len(t, tensions, 1,
		"a human relates-to edge between opposite-valence thoughts must yield exactly one tension")
	assert.InDelta(t, 2.0, tensions[0].ValenceDelta, 1e-9,
		"the tension's valence delta is the full +1/-1 swing")
}

// newFindingTensionFake builds a two-FINDING corpus F1 (one positive charge) and
// F2 (one negative charge), both magnitude≥0.5 and |Δvalence|=2, joined by the
// supplied explicit thought↔thought reasoning edge. It is the cross-node-type
// analog of newTensionFake: the claim nodes carry NodeFinding type and reach the
// universe as the parents of their charges — proving the charge-seeded
// fetchTensionUniverseNodes admits findings, not just thoughts, and ReflectTensions
// pairs them.
func newFindingTensionFake(edges []*knowledgev1.Edge) *tensionFake {
	mkFinding := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeFinding), UpdatedAt: 1000}
	}
	mkCharge := func(id, polarity string) *knowledgev1.Node {
		n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
		kgtypes.SetValue(n, "polarity", polarity)
		kgtypes.SetValue(n, "weight", "7")
		return n
	}
	return &tensionFake{
		thoughts: map[string]*knowledgev1.Node{"F1": mkFinding("F1"), "F2": mkFinding("F2")},
		order:    []string{"F1", "F2"},
		charges: map[string]*knowledgev1.Node{
			"cF1": mkCharge("cF1", "positive"), // F1 → valence +1
			"cF2": mkCharge("cF2", "negative"), // F2 → valence -1
		},
		tensionEdges: edges,
		chargeEdges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeChargedBy), FromId: "F1", ToId: "cF1"},
			{Type: string(kgtypes.EdgeChargedBy), FromId: "F2", ToId: "cF2"},
		},
	}
}

// TestReflectTensions_FindingFinding_CrossNodeType (FAILS-WHEN-ABSENT) is the
// headline cross-node-type behavior: two opposing-valence FINDING nodes joined by
// an explicit `contradicts` edge surface as exactly one cross-node-type tension. It
// goes red if the universe is ever narrowed back to thoughts only — whether by a
// thought-only drain or by seeding the charge walk from thought ids — since the
// findings would never enter it and no pair would form. PairCount==1 confirms
// graceful degradation for nodes that
// carry no cluster_id: each forms its own singleton group keyed by node id, never
// collapsed together.
func TestReflectTensions_FindingFinding_CrossNodeType(t *testing.T) {
	f := newFindingTensionFake([]*knowledgev1.Edge{
		{Type: "contradicts", FromId: "F1", ToId: "F2"},
	})
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	require.Len(t, tensions, 1,
		"two opposing-valence findings joined by a contradicts edge must yield exactly one cross-node-type tension")
	assert.InDelta(t, 2.0, tensions[0].ValenceDelta, 1e-9,
		"the tension's valence delta is the full +1/-1 swing")
	assert.Equal(t, 1, tensions[0].PairCount,
		"a finding pair with no cluster_id is a singleton group (PairCount==1) — graceful degradation, not collapsed")
	// The reported pair is the two finding nodes.
	ids := map[string]bool{tensions[0].NodeA.Id: true, tensions[0].NodeB.Id: true}
	assert.True(t, ids["F1"] && ids["F2"], "the tension reports the two finding nodes")
}

// TestReflectTensions_UnclusteredFindings_NotCollapsedTogether (FAILS-WHEN-ABSENT)
// proves graceful degradation for nodes lacking cluster_id: two INDEPENDENT
// finding pairs (F1↔F2, F3↔F4), none carrying a cluster_id, must surface as TWO
// separate representatives each with PairCount==1 — never wrongly collapsed into a
// single group. tensionGroupKey falls back to a per-id singleton key (the
// thought:<id> prefix is cosmetic — it keys on the node id regardless of node
// type) when clusterIDOf returns empty, and findings never get a cluster_id
// (clustering stays thought-only), so this per-id fallback is the path every
// finding tension takes. A regression that keyed unclustered nodes under a shared
// empty-cluster bucket would collapse these into one PairCount==2 row.
func TestReflectTensions_UnclusteredFindings_NotCollapsedTogether(t *testing.T) {
	mkFinding := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeFinding), UpdatedAt: 1000}
	}
	mkCharge := func(id, polarity string) *knowledgev1.Node {
		n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
		kgtypes.SetValue(n, "polarity", polarity)
		kgtypes.SetValue(n, "weight", "7")
		return n
	}
	f := &tensionFake{
		thoughts: map[string]*knowledgev1.Node{
			"F1": mkFinding("F1"), "F2": mkFinding("F2"),
			"F3": mkFinding("F3"), "F4": mkFinding("F4"),
		},
		order: []string{"F1", "F2", "F3", "F4"},
		charges: map[string]*knowledgev1.Node{
			"cF1": mkCharge("cF1", "positive"), "cF2": mkCharge("cF2", "negative"),
			"cF3": mkCharge("cF3", "positive"), "cF4": mkCharge("cF4", "negative"),
		},
		tensionEdges: []*knowledgev1.Edge{
			{Type: "contradicts", FromId: "F1", ToId: "F2"},
			{Type: "contradicts", FromId: "F3", ToId: "F4"},
		},
		chargeEdges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeChargedBy), FromId: "F1", ToId: "cF1"},
			{Type: string(kgtypes.EdgeChargedBy), FromId: "F2", ToId: "cF2"},
			{Type: string(kgtypes.EdgeChargedBy), FromId: "F3", ToId: "cF3"},
			{Type: string(kgtypes.EdgeChargedBy), FromId: "F4", ToId: "cF4"},
		},
	}
	tensions, err := ReflectTensions(context.Background(), f, nil)
	require.NoError(t, err)
	require.Len(t, tensions, 2,
		"two independent unclustered finding pairs must NOT collapse — each is its own per-id singleton group")
	for _, tn := range tensions {
		assert.Equal(t, 1, tn.PairCount,
			"each unclustered finding pair is a singleton group (PairCount==1)")
	}
}
