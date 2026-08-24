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

// tensionCorpus is a configurable Caller for the ReflectTensions collapse + cap
// tests. Each thought carries one charge of the given polarity (weight 7 → |v|=1,
// magnitude ≈ 2.08 ≥ 0.5) and an optional cluster_id; tensionEdges are the
// explicit human reasoning edges. It serves the same read shapes as tensionFake
// (type=charge seed browse, type=thought browse, tensionEdgeTypes read,
// EdgeChargedBy read, Ids hydrate)
// but over an arbitrary node/edge set so a clique + multi-cluster corpus can be
// expressed.
type tensionCorpus struct {
	thoughts     map[string]*knowledgev1.Node
	order        []string
	charges      map[string]*knowledgev1.Node
	chargeOf     map[string]string // thoughtID → its charge node id
	tensionEdges []*knowledgev1.Edge
}

func newTensionCorpus() *tensionCorpus {
	return &tensionCorpus{
		thoughts: map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string]string{},
	}
}

// addThought adds a thought with one charge of the given polarity and an optional
// cluster_id (empty = unassigned/singleton).
func (c *tensionCorpus) addThought(id, polarity, clusterID string) {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	if clusterID != "" {
		kgtypes.SetValue(n, "cluster_id", clusterID)
	}
	c.thoughts[id] = n
	c.order = append(c.order, id)

	chID := "c-" + id
	ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
	kgtypes.SetValue(ch, "polarity", polarity)
	kgtypes.SetValue(ch, "weight", "7")
	c.charges[chID] = ch
	c.chargeOf[id] = chID
}

// addEdge adds an explicit human relates-to reasoning edge between two thoughts.
func (c *tensionCorpus) addEdge(from, to string) {
	c.tensionEdges = append(c.tensionEdges, &knowledgev1.Edge{
		Type:   string(kgtypes.EdgeRelatesTo),
		FromId: from,
		ToId:   to,
	})
}

func (c *tensionCorpus) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		wantCharged := false
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				if et == string(kgtypes.EdgeChargedBy) {
					wantCharged = true
				}
			}
		}
		if wantCharged {
			var ce []*knowledgev1.Edge
			for _, tid := range c.order {
				ce = append(ce, &knowledgev1.Edge{
					Type: string(kgtypes.EdgeChargedBy), FromId: tid, ToId: c.chargeOf[tid],
				})
			}
			return &knowledgev1.ExecuteResponse{Edges: bandNarrow(ce, q)}, nil
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(c.tensionEdges, q)}, nil
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
	// The charged-universe seed browses type=charge; every other type browse keeps
	// serving the thought corpus. Both walk c.order, so both are deterministic.
	if sel := q.GetSelection(); sel != nil && sel.GetNodeType() == string(kgtypes.NodeCharge) {
		var charges []*knowledgev1.Node
		for _, id := range c.order {
			charges = append(charges, cloneNode(c.charges[c.chargeOf[id]]))
		}
		return &knowledgev1.ExecuteResponse{Nodes: charges}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range c.order {
		nodes = append(nodes, cloneNode(c.thoughts[id]))
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// TestReflectTensions_CliqueCollapsesPerClusterPair (FAILS-WHEN-ABSENT) proves the
// cluster-pair collapse: one hub thought human-edged to many same-cluster-pair
// siblings (a clique) plus a few genuine opposite-valence pairs across two clusters
// collapses to ONE representative per cluster-pair carrying PairCount, not one row
// per sibling. Goes red if the collapse is dropped (one row per edge returns).
func TestReflectTensions_CliqueCollapsesPerClusterPair(t *testing.T) {
	c := newTensionCorpus()
	// Cluster X: a positive hub edged to four negative siblings — four opposite-
	// valence candidate pairs, all in the SAME cluster-pair (X|Y).
	c.addThought("hub", "positive", "X")
	for i := range 4 {
		sib := fmt.Sprintf("y%d", i)
		c.addThought(sib, "negative", "Y")
		c.addEdge("hub", sib)
	}
	// A separate genuine cross-cluster pair in a DIFFERENT cluster-pair (P|Q).
	c.addThought("p", "positive", "P")
	c.addThought("qn", "negative", "Q")
	c.addEdge("p", "qn")

	tensions, err := ReflectTensions(context.Background(), c, nil)
	require.NoError(t, err)
	require.Len(t, tensions, 2,
		"the four X↔Y clique pairs collapse to ONE representative; the P↔Q pair is a second — two cluster-pairs, two rows")

	byPairCount := map[int]TensionReport{}
	for _, tr := range tensions {
		byPairCount[tr.PairCount] = tr
	}
	require.Contains(t, byPairCount, 4, "the X|Y clique representative carries PairCount=4")
	require.Contains(t, byPairCount, 1, "the P|Q pair representative carries PairCount=1")
}

// TestReflectTensions_CapsAtConst (FAILS-WHEN-ABSENT) proves the output caps at
// tensionReportCap representatives even when distinct cluster-pairs exceed the cap.
// Goes red if the cap is dropped.
func TestReflectTensions_CapsAtConst(t *testing.T) {
	c := newTensionCorpus()
	// Build tensionReportCap+10 distinct cluster-pairs, each a single opposite-
	// valence pair in its own (Pn, Qn) cluster-pair.
	pairs := tensionReportCap + 10
	for i := range pairs {
		pos := fmt.Sprintf("pos%d", i)
		neg := fmt.Sprintf("neg%d", i)
		c.addThought(pos, "positive", fmt.Sprintf("P%d", i))
		c.addThought(neg, "negative", fmt.Sprintf("Q%d", i))
		c.addEdge(pos, neg)
	}
	tensions, err := ReflectTensions(context.Background(), c, nil)
	require.NoError(t, err)
	assert.Len(t, tensions, tensionReportCap,
		"output is capped at tensionReportCap even when candidate cluster-pairs exceed it")
}

// TestIsMachineTensionMethod (FAILS-WHEN-ABSENT) asserts the machine-edge
// predicate classifies all four writer-const provenances as machine and an empty
// or arbitrary human tag as non-machine. The cases reference the writer consts
// (treeLinkMethod/densifyMethod/topicSimilarityMethod/artifactLinkMethod) so the
// test rides the same single source of truth the predicate does — a rename of any
// writer const breaks BOTH the predicate and this test at compile time.
func TestIsMachineTensionMethod(t *testing.T) {
	machine := []string{treeLinkMethod, densifyMethod, topicSimilarityMethod, artifactLinkMethod}
	for _, m := range machine {
		assert.Truef(t, isMachineTensionMethod(m),
			"writer-const method %q must classify as machine (excluded from tensions)", m)
	}

	assert.False(t, isMachineTensionMethod(""),
		"an empty Method (human-authored mutate(link)) is NOT machine")
	assert.False(t, isMachineTensionMethod("contradicts-by-hand"),
		"an arbitrary human tag is NOT machine")
}
