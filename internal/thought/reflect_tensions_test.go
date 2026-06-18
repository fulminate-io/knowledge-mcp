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
// fetchTensionEdges + fetchChargesFor + fetchNodesByIDs issue:
//
//   - the type=thought browse (fetchAllThoughtNodes) → every thought node;
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
			return &knowledgev1.ExecuteResponse{Edges: f.chargeEdges}, nil
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
			return &knowledgev1.ExecuteResponse{Edges: f.tensionEdges}, nil
		}
		filtered := make([]*knowledgev1.Edge, 0, len(f.tensionEdges))
		for _, e := range f.tensionEdges {
			if requested[e.GetType()] {
				filtered = append(filtered, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: filtered}, nil
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
	// the type=thought browse (first page): every thought node.
	var nodes []*knowledgev1.Node
	for _, id := range f.order {
		nodes = append(nodes, cloneNode(f.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
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
	tensions, err := ReflectTensions(context.Background(), f)
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
	tensions, err := ReflectTensions(context.Background(), f)
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
	tensions, err := ReflectTensions(context.Background(), f)
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
	tensions, err := ReflectTensions(context.Background(), f)
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
	tensions, err := ReflectTensions(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, tensions, 1,
		"a human relates-to edge between opposite-valence thoughts must yield exactly one tension")
	assert.InDelta(t, 2.0, tensions[0].ValenceDelta, 1e-9,
		"the tension's valence delta is the full +1/-1 swing")
}
