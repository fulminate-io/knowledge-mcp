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
	graph := req.GetTarget().GetGraph()
	if lang := req.GetTarget().GetLanguage(); lang != "" {
		graph += "/" + lang
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
			practiceNames: []string{"go", "python"},
		}
	}

	t.Run("a node under the named hub resolves", func(t *testing.T) {
		gc := newGc()
		node, graphType, _, err := resolveAssembleNode(context.Background(), gc, "p1", "hub-1")
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
		_, _, _, err := resolveAssembleNode(context.Background(), gc, "p1", "hub-2")
		require.Error(t, err, "the node belongs to hub-1; a hub-2 read must not be served it")
		assert.Contains(t, err.Error(), "hub-2",
			"the error names the hub the caller asked for, so it is not read as 'no such node'")
	})

	// A HUB IS A FACT ABOUT THE COMBINED GRAPH, so a hub-scoped miss must not fan
	// out over the pre-singleton graphs — they predate hubs and could only
	// produce a node that cannot satisfy the scope.
	t.Run("a hub-scoped read never falls back to the legacy graphs", func(t *testing.T) {
		gc := newGc()
		_, _, _, err := resolveAssembleNode(context.Background(), gc, "absent", "hub-1")
		require.Error(t, err)
		assert.Zero(t, gc.listedGraphs, "no practice-graph enumeration for a hub-scoped read")
		assert.NotContains(t, gc.askedGraphs, "practice/go", "and no legacy probe")
		assert.NotContains(t, gc.askedGraphs, "", "nor the knowledge read: a knowledge node carries no hub")

		// THE CONTROL, same fixture: the UNSCOPED miss does all three, so the
		// zeros above are the scope short-circuiting rather than a resolver that
		// stopped probing.
		open := newGc()
		_, _, _, oerr := resolveAssembleNode(context.Background(), open, "absent", "")
		require.Error(t, oerr)
		assert.Positive(t, open.listedGraphs, "control: an unscoped miss enumerates the legacy graphs")
		assert.Contains(t, open.askedGraphs, "practice/go", "control: and probes them")
	})
}
