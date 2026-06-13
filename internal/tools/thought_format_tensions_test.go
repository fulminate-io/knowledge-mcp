// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// tensionRenderCaller is an Execute fake serving the reads ReflectTensions issues
// (type=thought browse, tensionEdgeTypes edge read, EdgeChargedBy edge read, Ids
// hydrate). Each thought carries one charge (weight 7 → |v|=1, magnitude ≈ 2.08)
// and a cluster_id; edges are explicit human relates-to reasoning edges. It lets
// the tools-package handler test drive handleReflectTensions end-to-end.
type tensionRenderCaller struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
	charges  map[string]*knowledgev1.Node
	chargeOf map[string]string
	edges    []*knowledgev1.Edge
}

func newTensionRenderCaller() *tensionRenderCaller {
	return &tensionRenderCaller{
		thoughts: map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string]string{},
	}
}

func (c *tensionRenderCaller) addThought(id, name, polarity, clusterID string) {
	n := &knowledgev1.Node{Id: id, SymbolName: name, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	kgtypes.SetValue(n, "cluster_id", clusterID)
	c.thoughts[id] = n
	c.order = append(c.order, id)

	chID := "c-" + id
	ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
	kgtypes.SetValue(ch, "polarity", polarity)
	kgtypes.SetValue(ch, "weight", "7")
	c.charges[chID] = ch
	c.chargeOf[id] = chID
}

func (c *tensionRenderCaller) addEdge(from, to string) {
	c.edges = append(c.edges, &knowledgev1.Edge{
		Type:   string(kgtypes.EdgeRelatesTo),
		FromId: from,
		ToId:   to,
	})
}

func (c *tensionRenderCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
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
				ce = append(ce, &knowledgev1.Edge{Type: string(kgtypes.EdgeChargedBy), FromId: tid, ToId: c.chargeOf[tid]})
			}
			return &knowledgev1.ExecuteResponse{Edges: ce}, nil
		}
		return &knowledgev1.ExecuteResponse{Edges: c.edges}, nil
	}
	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := c.thoughts[id]; ok {
				nodes = append(nodes, n)
			} else if ch, ok := c.charges[id]; ok {
				nodes = append(nodes, ch)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range c.order {
		nodes = append(nodes, c.thoughts[id])
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// TestHandleReflectTensions (FAILS-WHEN-ABSENT) asserts the tensions text render
// carries the totals header naming candidate count, cluster-pair count, and shown
// count, plus per-row provenance (via <edgeType>[...]) + per-thought charge
// counts, and a collapse line when PairCount>1. Builds an uncapped corpus where a
// clique collapse makes candidate count exceed cluster-pair count, so all three
// header numbers are distinct.
func TestHandleReflectTensions(t *testing.T) {
	c := newTensionRenderCaller()
	// Cluster-pair X|Y: a positive hub edged to three negative siblings — three
	// candidate pairs collapsing to ONE representative (PairCount=3).
	c.addThought("hub", "Hub thought", "positive", "X")
	for i := range 3 {
		sib := fmt.Sprintf("y%d", i)
		c.addThought(sib, fmt.Sprintf("Sibling %d", i), "negative", "Y")
		c.addEdge("hub", sib)
	}
	// A second, distinct cluster-pair P|Q with a single genuine pair.
	c.addThought("p", "P thought", "positive", "P")
	c.addThought("qn", "Q thought", "negative", "Q")
	c.addEdge("p", "qn")

	deps := interceptTestDeps{gc: c}
	res := handleReflectTensions(context.Background(), deps, queryReflectArgs{})
	text := resultText(res)

	// Header: 4 candidate tensions (3 collapsed + 1), 2 cluster-pairs, top 2.
	assert.Contains(t, text, "4 candidate tensions", "header names the candidate count (sum of PairCount)")
	assert.Contains(t, text, "across 2 cluster-pairs", "header names the cluster-pair count")
	assert.Contains(t, text, "showing top 2", "header names the shown count")

	// Rows: provenance + charge counts + collapse line.
	assert.Contains(t, text, "via relates-to[human]", "row shows linking-edge provenance")
	assert.Contains(t, text, "charges)", "row shows per-thought charge counts")
	assert.Contains(t, text, "collapses 3 similar pairs", "the clique representative shows its PairCount collapse")
}
