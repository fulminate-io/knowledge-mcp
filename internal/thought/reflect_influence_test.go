// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// influenceCorpus serves every read ReflectInfluence issues (type=thought browse,
// adjacency edge read, EdgeChargedBy edge read, EdgeKGContains session read, Ids
// hydrate) over a fixed topology + charge set, so the eigenvector influence order
// and the influence×magnitude composite order are both deterministic.
type influenceCorpus struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
	charges  map[string]*knowledgev1.Node
	chargeOf map[string][]string // thoughtID → its charge node ids
	adjEdges []*knowledgev1.Edge // relates-to topology edges
}

func newInfluenceCorpus() *influenceCorpus {
	return &influenceCorpus{
		thoughts: map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string][]string{},
	}
}

// addThought adds a thought with one positive charge of the given weight (weight
// drives magnitude = log(1+weight)).
func (c *influenceCorpus) addThought(id, name, weight string) {
	n := &knowledgev1.Node{Id: id, SymbolName: name, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	c.thoughts[id] = n
	c.order = append(c.order, id)
	chID := "c-" + id
	ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
	kgtypes.SetValue(ch, "polarity", "positive")
	kgtypes.SetValue(ch, "weight", weight)
	c.charges[chID] = ch
	c.chargeOf[id] = []string{chID}
}

// addUnchargedThought adds a thought with NO charge (zero-charge structural hub
// case). Mirrors addThought but skips the charge node + chargeOf entry; the
// Execute fake already handles a thought with an empty chargeOf slice.
func (c *influenceCorpus) addUnchargedThought(id, name string) {
	n := &knowledgev1.Node{Id: id, SymbolName: name, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	c.thoughts[id] = n
	c.order = append(c.order, id)
}

func (c *influenceCorpus) addEdge(from, to string) {
	c.adjEdges = append(c.adjEdges, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: from, ToId: to})
}

func (c *influenceCorpus) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// UNION, not first-match: the wire returns every edge matching ANY requested
		// type (an empty set means every type). Branching to a single bucket was
		// adequate only while each caller requested one type at a time; the unified
		// pivot read requests seven at once, and a first-match fake would answer it
		// with the charge edges alone and starve the adjacency. kg-contains
		// contributes nothing here (no sessions → no sibling expansion).
		want := map[string]bool{}
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				want[et] = true
			}
		}
		keep := func(t string) bool { return len(want) == 0 || want[t] }

		var out []*knowledgev1.Edge
		if keep(string(kgtypes.EdgeChargedBy)) {
			for _, tid := range c.order {
				for _, chID := range c.chargeOf[tid] {
					out = append(out, &knowledgev1.Edge{Type: string(kgtypes.EdgeChargedBy), FromId: tid, ToId: chID})
				}
			}
		}
		for _, e := range c.adjEdges {
			if keep(e.Type) {
				out = append(out, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(out, q)}, nil
	}
	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := c.thoughts[id]; ok {
				nodes = append(nodes, cloneNode(n))
			} else if ch, ok := c.charges[id]; ok {
				nodes = append(nodes, cloneNode(ch))
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range c.order {
		nodes = append(nodes, cloneNode(c.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// influenceOrder returns the thought IDs of a report slice in order.
func influenceOrder(reports []InfluenceReport) []string {
	out := make([]string, len(reports))
	for i, r := range reports {
		out[i] = r.ThoughtID
	}
	return out
}

// TestReflectInfluence_Sort (FAILS-WHEN-ABSENT, rewritten for evidence-aware
// selection) pins the two NEW invariants over ranking.Evidenced:
//
//	(i)  DEFAULT order is the evidence-weighted rank influence×(1+chargeWeight)
//	     — NOT pure eigenvector. In this fixture L1 carries chargeWeight=60 vs
//	     H=2, so even though H has the highest raw eigenvector influence, the
//	     evidence-weighted product lifts the heavy-charge leaf L1 to the front.
//	     Default Evidenced[0]=="L1" and the order is monotonic non-increasing in
//	     influence×(1+chargeWeight).
//	(ii) composite is a within-evidenced display reorder by influence×Magnitude:
//	     the SAME Evidenced set, reordered only, monotonic non-increasing in
//	     influence×Magnitude.
//
// All 3 fixture thoughts are charged, so all land in Evidenced and
// BackfillCandidates is empty. Topology: a center hub H linked to two leaves
// L1,L2 — H has the highest eigenvector influence; L1 carries a far larger
// charge weight than H or L2.
func TestReflectInfluence_Sort(t *testing.T) {
	build := func() *influenceCorpus {
		c := newInfluenceCorpus()
		c.addThought("H", "Hub", "2")     // center — highest influence, small charge weight
		c.addThought("L1", "Leaf1", "60") // leaf — low influence, large charge weight
		c.addThought("L2", "Leaf2", "2")  // leaf — low influence, small charge weight
		c.addEdge("H", "L1")
		c.addEdge("H", "L2")
		return c
	}

	// Default order: evidence-weighted influence×(1+chargeWeight) — the
	// heavy-charge leaf L1 leads even though H wins raw eigenvector influence.
	defRanking, err := ReflectInfluence(context.Background(), build(), 10, nil, "")
	require.NoError(t, err)
	require.Len(t, defRanking.Evidenced, 3, "all 3 charged thoughts land in Evidenced")
	require.Empty(t, defRanking.BackfillCandidates, "no zero-charge thoughts → empty backfill")
	defOrder := influenceOrder(defRanking.Evidenced)
	assert.Equal(t, "L1", defOrder[0],
		"default (evidence-weighted) order puts the heavy-charge leaf first")
	// Default order is monotonic non-increasing in influence×(1+chargeWeight).
	for i := 1; i < len(defRanking.Evidenced); i++ {
		prev := defRanking.Evidenced[i-1]
		cur := defRanking.Evidenced[i]
		wPrev := prev.InfluenceScore * (1 + prev.Properties.PositiveWeight + prev.Properties.NegativeWeight)
		wCur := cur.InfluenceScore * (1 + cur.Properties.PositiveWeight + cur.Properties.NegativeWeight)
		assert.GreaterOrEqual(t, wPrev, wCur,
			"default order is monotonic non-increasing in influence×(1+chargeWeight)")
	}

	// Composite order: within-evidenced reorder by influence×Magnitude — same set.
	compRanking, err := ReflectInfluence(context.Background(), build(), 10, nil, "composite")
	require.NoError(t, err)
	require.Len(t, compRanking.Evidenced, 3, "composite must NOT change the selected set — same 3 thoughts")
	compOrder := influenceOrder(compRanking.Evidenced)
	assert.ElementsMatch(t, defOrder, compOrder,
		"composite reorders within the SAME evidenced set — it never widens or narrows the set")
	// Composite order is monotonic non-increasing in influence×Magnitude.
	for i := 1; i < len(compRanking.Evidenced); i++ {
		pPrev := compRanking.Evidenced[i-1].InfluenceScore * compRanking.Evidenced[i-1].Properties.Magnitude
		pCur := compRanking.Evidenced[i].InfluenceScore * compRanking.Evidenced[i].Properties.Magnitude
		assert.GreaterOrEqual(t, pPrev, pCur,
			"composite order is monotonic non-increasing in influence×Magnitude")
	}
}

// TestReflectInfluence_EvidenceAwareSelection (FAILS-WHEN-ABSENT) is the core
// acceptance: a high-centrality ZERO-CHARGE structural hub must NOT crowd a
// charged peripheral thought out of the evidenced section. The hub H wins raw
// eigenvector influence (it is the center of four leaves) but carries no charges;
// a single charged peripheral leaf P carries a heavy charge. Default selection
// must put the charged thought in Evidenced and relegate the hub to backfill.
func TestReflectInfluence_EvidenceAwareSelection(t *testing.T) {
	c := newInfluenceCorpus()
	c.addUnchargedThought("H", "Hub") // zero-charge structural center — wins raw influence
	c.addUnchargedThought("A", "LeafA")
	c.addUnchargedThought("B", "LeafB")
	c.addUnchargedThought("D", "LeafD")
	c.addThought("P", "Peripheral", "40") // charged, structurally peripheral
	// H is the hub of A,B,D (high centrality); P hangs off a single leaf (peripheral).
	c.addEdge("H", "A")
	c.addEdge("H", "B")
	c.addEdge("H", "D")
	c.addEdge("A", "P")

	ranking, err := ReflectInfluence(context.Background(), c, 10, nil, "")
	require.NoError(t, err)

	require.NotEmpty(t, ranking.Evidenced, "the charged peripheral must produce a non-empty evidenced section")
	assert.Equal(t, "P", ranking.Evidenced[0].ThoughtID,
		"the charged thought leads Evidenced, not the zero-charge hub")
	for _, r := range ranking.Evidenced {
		assert.Positive(t, r.Properties.ChargeCount,
			"every Evidenced entry is charged")
	}
	assert.Contains(t, influenceOrder(ranking.BackfillCandidates), "H",
		"the zero-charge structural hub is a backfill candidate, not evidenced")
	assert.NotContains(t, influenceOrder(ranking.Evidenced), "H",
		"the zero-charge hub never appears in Evidenced")
}

// TestReflectInfluence_AdversarialHubDominance (FAILS-WHEN-ABSENT) pins the exact
// live-corpus failure shape: the influence head is saturated by MANY zero-charge
// hubs (≥4×limit) that all outrank a few charged peripheral thoughts by raw
// influence. Under the REJECTED headroom-by-influence design the charged thoughts
// fall outside the influence window and Evidenced comes back empty; under
// charge-aware-partition-before-cut they are retained. This is the discriminator.
func TestReflectInfluence_AdversarialHubDominance(t *testing.T) {
	const limit = 10
	c := newInfluenceCorpus()
	// A dense recurrent zero-charge hub mesh: 5×limit spoke hubs, each wired both
	// ways to a center C and forward to the next hub. Influence (stationary) mass
	// stays concentrated among the hubs, so the entire 4×limit raw-influence head
	// is zero-charge hubs.
	const hubN = 5 * limit
	c.addUnchargedThought("C", "Center")
	c.addEdge("C", "C")
	for i := range hubN {
		id := fmt.Sprintf("hub-%d", i)
		c.addUnchargedThought(id, id)
		c.addEdge("C", id)
		c.addEdge(id, "C")
		c.addEdge(id, fmt.Sprintf("hub-%d", (i+1)%hubN))
	}
	// Charged but structurally peripheral thoughts that DRAIN outward into the
	// mesh (P→C): their own stationary mass flows away to the hubs and nothing
	// points back, so their raw influence ranks BELOW the 4×limit hub window. Under
	// the rejected headroom-by-influence design they fall outside the window and
	// Evidenced comes back empty; charge-aware partition retains them.
	chargedIDs := []string{"P0", "P1", "P2"}
	for _, pid := range chargedIDs {
		c.addThought(pid, pid, "25")
		c.addEdge(pid, "C")
	}

	ranking, err := ReflectInfluence(context.Background(), c, limit, nil, "")
	require.NoError(t, err)

	require.NotEmpty(t, ranking.Evidenced,
		"charged peripherals must survive even when ≥4×limit zero-charge hubs dominate raw influence")
	evidencedIDs := influenceOrder(ranking.Evidenced)
	for _, pid := range chargedIDs {
		assert.Contains(t, evidencedIDs, pid,
			"every charged peripheral is retained in Evidenced — none dropped by the hub head")
	}
	for _, r := range ranking.Evidenced {
		assert.Positive(t, r.Properties.ChargeCount, "Evidenced holds only charged thoughts")
	}
	assert.NotEmpty(t, ranking.BackfillCandidates,
		"the zero-charge hubs populate the backfill section")
}
