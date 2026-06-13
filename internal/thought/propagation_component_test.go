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

// propagationCorpus serves every read RunPropagationScoped issues: the type=thought
// browse (fetchAllThoughtNodes), the adjacency edge read (relates-to), the
// EdgeKGContains read (no sessions → empty), the EdgeChargedBy read + charge
// hydrate (fetchChargesFor / chargeMapForThoughts), node hydrate, and the bulk
// writeback mutation (accepted, no-op). It models a graph of disjoint components.
type propagationCorpus struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
	charges  map[string]*knowledgev1.Node
	chargeOf map[string][]string
	adjEdges []*knowledgev1.Edge
}

func newPropagationCorpus() *propagationCorpus {
	return &propagationCorpus{
		thoughts: map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string][]string{},
	}
}

func (c *propagationCorpus) addThought(id, polarity string) {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	c.thoughts[id] = n
	c.order = append(c.order, id)
	if polarity == "" {
		return
	}
	chID := "c-" + id
	ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
	kgtypes.SetValue(ch, "polarity", polarity)
	kgtypes.SetValue(ch, "weight", "7")
	c.charges[chID] = ch
	c.chargeOf[id] = []string{chID}
}

func (c *propagationCorpus) addEdge(from, to string) {
	c.adjEdges = append(c.adjEdges, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: from, ToId: to})
}

func (c *propagationCorpus) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		return &knowledgev1.ExecuteResponse{}, nil // accept the writeback no-op.
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		var wantCharged, wantContains bool
		if sel := q.GetSelection(); sel != nil {
			for _, et := range sel.GetEdgeTypes() {
				switch et {
				case string(kgtypes.EdgeChargedBy):
					wantCharged = true
				case string(kgtypes.EdgeKGContains):
					wantContains = true
				}
			}
		}
		if wantCharged {
			var ce []*knowledgev1.Edge
			for _, tid := range c.order {
				for _, chID := range c.chargeOf[tid] {
					ce = append(ce, &knowledgev1.Edge{Type: string(kgtypes.EdgeChargedBy), FromId: tid, ToId: chID})
				}
			}
			return &knowledgev1.ExecuteResponse{Edges: ce}, nil
		}
		if wantContains {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		return &knowledgev1.ExecuteResponse{Edges: c.adjEdges}, nil
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

// TestRunPropagation_PerComponentConvergence (FAILS-WHEN-ABSENT) proves per-component
// convergence reporting: among M components, a single long charge-driven path that
// does NOT converge within the iteration cap yields ComponentsConverged=M-1 and
// exactly one NonConverged entry carrying that component's size + a non-zero
// residual, while the converged components are counted, not masked. Goes red if the
// global converged-AND flag returns or the per-component detail is dropped.
func TestRunPropagation_PerComponentConvergence(t *testing.T) {
	c := newPropagationCorpus()

	// A long path component (p0..pN) with opposite-charge endpoints and uncharged
	// interior: valence diffuses one hop per iteration, so a path longer than the
	// iteration cap (defaultMaxIterations=100) cannot converge in time.
	pathLen := 160
	for i := range pathLen {
		pol := ""
		switch i {
		case 0:
			pol = "positive"
		case pathLen - 1:
			pol = "negative"
		}
		c.addThought(fmt.Sprintf("p%d", i), pol)
	}
	for i := 0; i < pathLen-1; i++ {
		c.addEdge(fmt.Sprintf("p%d", i), fmt.Sprintf("p%d", i+1))
	}

	// Several tiny converging components: isolated charged singletons (no edges →
	// each its own component, trivially converged at iteration 1).
	converged := 4
	for i := range converged {
		c.addThought(fmt.Sprintf("iso%d", i), "positive")
	}

	res, err := RunPropagation(context.Background(), c, nil, nil)
	require.NoError(t, err)

	require.Len(t, res.NonConverged, 1, "exactly one component (the long path) fails to converge")
	assert.Equal(t, pathLen, res.NonConverged[0].Size, "the non-converged entry carries the path component's size")
	assert.Greater(t, res.NonConverged[0].ValenceResidual, 0.0, "the non-converged entry carries a non-zero residual")

	assert.Equal(t, res.Components-1, res.ComponentsConverged,
		"every component except the long path converged (M-1)")
	assert.False(t, res.Converged, "Converged is false while any component is non-converged")
}

// TestRunPropagation_AllConverged (FAILS-WHEN-ABSENT) proves Converged derives true
// (len(NonConverged)==0) when every component converges.
func TestRunPropagation_AllConverged(t *testing.T) {
	c := newPropagationCorpus()
	for i := range 5 {
		c.addThought(fmt.Sprintf("iso%d", i), "positive") // isolated → trivially converges.
	}
	res, err := RunPropagation(context.Background(), c, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, res.NonConverged)
	assert.Equal(t, res.Components, res.ComponentsConverged)
	assert.True(t, res.Converged, "all components converged ⇒ Converged derives true")
}
