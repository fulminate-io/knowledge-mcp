// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// influenceRenderCaller serves the reads ReflectInfluence issues (type=thought
// browse, adjacency edge read, EdgeChargedBy read, EdgeKGContains read, Ids
// hydrate) so handleReflectInfluence can be driven end-to-end. Thoughts may carry
// a charge (weight 5) or NO charge (the zero-charge backfill case).
type influenceRenderCaller struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
	charges  map[string]*knowledgev1.Node
	chargeOf map[string][]string
	adjEdges []*knowledgev1.Edge
}

func newInfluenceRenderCaller() *influenceRenderCaller {
	return &influenceRenderCaller{
		thoughts: map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string][]string{},
	}
}

func (c *influenceRenderCaller) addCharged(id, name string) {
	n := &knowledgev1.Node{Id: id, SymbolName: name, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	c.thoughts[id] = n
	c.order = append(c.order, id)
	chID := "c-" + id
	ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
	kgtypes.SetValue(ch, "polarity", "positive")
	kgtypes.SetValue(ch, "weight", "5")
	c.charges[chID] = ch
	c.chargeOf[id] = []string{chID}
}

func (c *influenceRenderCaller) addUncharged(id, name string) {
	n := &knowledgev1.Node{Id: id, SymbolName: name, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	c.thoughts[id] = n
	c.order = append(c.order, id)
}

func (c *influenceRenderCaller) addEdge(from, to string) {
	c.adjEdges = append(c.adjEdges, &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: from, ToId: to})
}

func (c *influenceRenderCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
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

// TestHandleReflectInfluence (FAILS-WHEN-ABSENT) asserts the default influence
// render annotates every row with its charge count, and that a zero-charge thought
// renders the loud backfill marker. Goes red if the charge annotation is dropped.
func TestHandleReflectInfluence(t *testing.T) {
	c := newInfluenceRenderCaller()
	c.addCharged("H", "Hub")      // charged
	c.addUncharged("L1", "Leaf1") // zero charges → backfill candidate
	c.addEdge("H", "L1")

	deps := interceptTestDeps{gc: c}
	res := handleReflectInfluence(context.Background(), deps, queryReflectArgs{})
	text := resultText(res)

	assert.Contains(t, text, "charges", "every influence row is annotated with its charge count")
	assert.Contains(t, text, "0 charges — backfill candidate",
		"a zero-charge thought renders the loud backfill marker")
}

// TestHandleReflectInfluence_TwoSection (FAILS-WHEN-ABSENT) asserts the
// evidence-aware two-section render: the charged thought appears under the
// evidenced header, the zero-charge structural hub appears under the labeled
// backfill header (NOT the evidenced one), and the backfill marker is retained on
// the hub row. Goes red if the surface collapses back to a single flat section.
func TestHandleReflectInfluence_TwoSection(t *testing.T) {
	c := newInfluenceRenderCaller()
	// Zero-charge hub at the structural center of three charged leaves: the hub
	// wins raw influence but is unevidenced; the charged leaves are evidenced.
	c.addUncharged("H", "Hub")
	c.addCharged("A", "LeafA")
	c.addCharged("B", "LeafB")
	c.addCharged("D", "LeafD")
	c.addEdge("H", "A")
	c.addEdge("H", "B")
	c.addEdge("H", "D")
	c.addEdge("A", "B") // a non-hub edge so the topology isn't a pure star

	deps := interceptTestDeps{gc: c}
	res := handleReflectInfluence(context.Background(), deps, queryReflectArgs{})
	text := resultText(res)

	evidencedHdr := "# Most Influential Thoughts (evidenced"
	backfillHdr := "## Influential but unevidenced (backfill candidates)"
	require.Contains(t, text, evidencedHdr, "the evidenced section header is rendered")
	require.Contains(t, text, backfillHdr, "the labeled backfill section header is rendered")

	evidencedIdx := strings.Index(text, evidencedHdr)
	backfillIdx := strings.Index(text, backfillHdr)
	require.Less(t, evidencedIdx, backfillIdx, "the evidenced section precedes the backfill section")

	evidencedBody := text[evidencedIdx:backfillIdx]
	backfillBody := text[backfillIdx:]

	// The charged leaves render in the evidenced body; the zero-charge hub renders
	// in the backfill body with the retained marker.
	assert.Contains(t, evidencedBody, "LeafA", "a charged thought renders under the evidenced header")
	assert.Contains(t, backfillBody, "Hub", "the zero-charge hub renders under the backfill header")
	assert.NotContains(t, evidencedBody, "Hub", "the zero-charge hub is NOT in the evidenced section")
	assert.Contains(t, backfillBody, "0 charges — backfill candidate",
		"the backfill marker is retained on the hub row")
}
