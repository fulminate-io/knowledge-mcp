// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// assemble_hub_scope_test.go — `source` on the assemble arm.
//
// WHY THE ARM CARRIES ONE AT ALL, since a by-id read resolves one node whatever
// the scope. The hub cannot change WHICH node comes back; it decides whether one
// comes back. That is the difference between assembling a node the caller
// believed belonged to a collection and being told it does not — the question
// the hub exists to answer, for the case it exists for: auditing what a failed
// or abandoned collection actually landed.

// hubProbeGc serves by-id reads out of a per-graph table and records which
// graphs were asked. It distinguishes the practice probe from the knowledge one
// by TARGET GRAPH rather than by language, which is the whole point: the
// combined practice graph is addressed with no instance field at all, so a fake
// that keys on language alone cannot tell the two apart.
type hubProbeGc struct {
	knowledge     map[string]*knowledgev1.Node
	practice      map[string]*knowledgev1.Node
	askedGraphs   []string
	listedGraphs  int
	practiceNames []string
}

func (g *hubProbeGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q, ok := req.GetPlan().(*knowledgev1.ExecuteRequest_Query)
	if !ok {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.Query.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		g.listedGraphs++
		infos := make([]*knowledgev1.GraphInfo, len(g.practiceNames))
		for i, n := range g.practiceNames {
			infos[i] = &knowledgev1.GraphInfo{Name: n}
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}
	// THE PROBE'S ADDRESS, RECORDED AS THE SELECTOR CARRIES IT. It used to append
	// the selector's `language`, because a practice probe named the graph it
	// walked; practice addresses no instance now, so a practice probe records the
	// bare family and the `name` is what an instance-addressed family carries.
	graph := req.GetTarget().GetGraph()
	if name := req.GetTarget().GetName(); name != "" {
		graph += "/" + name
	}
	g.askedGraphs = append(g.askedGraphs, graph)

	table := g.knowledge
	if req.GetTarget().GetGraph() == "practice" {
		table = g.practice
	}
	if n, found := table[q.Query.GetById()]; found {
		return enginetest.ResponseWithNode(n), nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// hubbedNode builds a practice node carrying a source hub.
func hubbedNode(id, hub string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: id, SymbolName: "Pattern " + id, Type: string(kgtypes.NodePattern),
		Metadata: map[string]string{kgtypes.MetaKeySourceHub: hub},
	}
}

func TestResolveAssembleNode_HubScopesTheResolve(t *testing.T) {
	newGc := func() *hubProbeGc {
		return &hubProbeGc{
			knowledge:     map[string]*knowledgev1.Node{},
			practice:      map[string]*knowledgev1.Node{"p1": hubbedNode("p1", "hub-1")},
			practiceNames: []string{"default"},
		}
	}

	t.Run("a node under the named hub resolves", func(t *testing.T) {
		gc := newGc()
		node, graphType, err := resolveAssembleNode(context.Background(), gc, "p1", "hub-1")
		require.NoError(t, err)
		require.NotNil(t, node)
		assert.Equal(t, "p1", node.GetId())
		assert.Equal(t, "practice", graphType)
	})

	// THE DISCRIMINATING ROW. The node EXISTS and the by-id read finds it; the
	// scope is what refuses it. A resolver that ignored the hub returns it here
	// and passes the row above unchanged.
	t.Run("a node under a different hub is refused, naming the hub", func(t *testing.T) {
		gc := newGc()
		_, _, err := resolveAssembleNode(context.Background(), gc, "p1", "hub-2")
		require.Error(t, err, "the node belongs to hub-1; a hub-2 read must not be served it")
		assert.Contains(t, err.Error(), "hub-2",
			"the error names the hub the caller asked for, so it is not read as 'no such node'")
	})

	// A HUB IS A FACT ABOUT THE COMBINED PRACTICE GRAPH, so a hub-scoped miss must
	// not reach past it: no catalog enumeration and no knowledge read, whose nodes
	// carry no hub at all. The practice probe itself DOES run — it is how the scope
	// is evaluated — so the assertions are on the two reads a scoped resolve must
	// skip rather than on every read it makes.
	t.Run("a_hub_scoped_read_never_falls_back_to_the_catalog", func(t *testing.T) {
		gc := newGc()
		_, _, err := resolveAssembleNode(context.Background(), gc, "absent", "hub-1")
		require.Error(t, err)
		assert.Zero(t, gc.listedGraphs, "no practice-graph enumeration for a hub-scoped read")
		assert.NotContains(t, gc.askedGraphs, "", "and no knowledge read: a knowledge node carries no hub")

		// THE CONTROL, same fixture: the UNSCOPED miss reads BOTH families, so the
		// assertions above are the scope short-circuiting rather than a resolver
		// that stopped probing.
		//
		// IT NO LONGER ENUMERATES. The catalog read was the legacy fallback's first
		// step, and the fallback is gone: one practice probe answers for the one
		// practice graph, so an unscoped miss reads knowledge and practice and
		// nothing else. `listedGraphs` staying zero is asserted here rather than
		// dropped, because a resolver that started enumerating again would be
		// paying a round trip for a catalog it has no use for.
		open := newGc()
		_, _, oerr := resolveAssembleNode(context.Background(), open, "absent", "")
		require.Error(t, oerr)
		assert.Zero(t, open.listedGraphs, "control: an unscoped miss needs no catalog read")
		assert.Contains(t, open.askedGraphs, "practice", "control: it probes the practice graph")
		assert.Contains(t, open.askedGraphs, "", "control: and the knowledge graph, which a scoped read skips")
	})
}
