// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAssemble_RPCCountConstant is this ticket's gate: it asserts that every
// assemble arm's wire cost is INVARIANT UNDER SUBTREE WIDTH, which is the
// property the whole change exists to establish.
//
// THE ASSERTION IS EQUALITY BETWEEN TWO WIDTHS, NOT EQUALITY TO A NUMBER. An
// absolute count would make this a change-detector that goes red whenever a
// section is added or removed; invariance under width goes red exactly when a
// per-node read comes back, and at nothing else. The observed counts ride the
// failure message so a regression names the numbers it drifted to.
//
// The counting fake is countingGc (wire_traverse_test.go), which wraps the
// package fixture and records every Execute. The fixture answers all four plan
// shapes assemble emits — a single ByID, a bulk Ids[] hydrate,
// RETURN_MODE_TRAVERSAL, RETURN_MODE_EDGES in both its single-pivot and
// node-SET forms, and RETURN_MODE_GRAPH_NAMES.
func TestAssemble_RPCCountConstant(t *testing.T) {
	// widths are deliberately far apart: a per-node read at width 3 might
	// coincide with a batched count, but it cannot at width 12 as well.
	const narrow, wide = 3, 12

	// seed builds a root of the given type with n children of childType, each
	// carrying one grandchild, plus n non-contains peers reached by peerEdge.
	// Both axes scale with width, so an N+1 on either the tree side or the
	// linked-nodes side moves the count.
	seed := func(rootType kgtypes.NodeType, childType, grandType kgtypes.NodeType, peerEdge kgtypes.EdgeType, peerType kgtypes.NodeType, n int) *graphFixture {
		f := newGraphFixture()
		root := &knowledgev1.Node{
			Id: "root", Type: string(rootType), SymbolName: "the-root", Status: kgtypes.StatusActive,
		}
		kgtypes.SetValue(root, "no_patterns_reason", "fixture")
		f.addKnowledgeNode(root)
		for i := range n {
			c := fmt.Sprintf("c-%02d", i)
			gch := fmt.Sprintf("g-%02d", i)
			p := fmt.Sprintf("p-%02d", i)
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: c, Type: string(childType), SymbolName: "child-" + c, Status: kgtypes.StatusPending,
			})
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: gch, Type: string(grandType), SymbolName: "grand-" + gch, Status: kgtypes.StatusPending,
			})
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: p, Type: string(peerType), SymbolName: "peer-" + p,
			})
			f.link("root", c)
			f.link(c, gch)
			f.addKnowledgeEdge("root", p, peerEdge)
		}
		return f
	}

	type row struct {
		name string
		// nodeType is the type Handle dispatches on. The fallback row carries a
		// type Handle does not recognize, which is what routes it to the
		// default arm.
		nodeType kgtypes.NodeType
		build    func(n int) *graphFixture
		args     func() map[string]any
	}

	simple := func(root, child, grand kgtypes.NodeType, peerEdge kgtypes.EdgeType, peer kgtypes.NodeType) func(int) *graphFixture {
		return func(n int) *graphFixture { return seed(root, child, grand, peerEdge, peer, n) }
	}
	byID := func() map[string]any { return map[string]any{"id": "root"} }

	rows := []row{
		{"plan", kgtypes.NodePlan,
			simple(kgtypes.NodePlan, kgtypes.NodePhase, kgtypes.NodeStep, kgtypes.EdgeInformedBy, kgtypes.NodeResearch), byID},
		{"project", kgtypes.NodeProject,
			simple(kgtypes.NodeProject, kgtypes.NodeTicket, kgtypes.NodePlan, kgtypes.EdgeRelatesTo, kgtypes.NodeFinding), byID},
		{"ticket", kgtypes.NodeTicket,
			simple(kgtypes.NodeTicket, kgtypes.NodePlan, kgtypes.NodePhase, kgtypes.EdgeInformedBy, kgtypes.NodeDecision), byID},
		{"test_plan", kgtypes.NodeTestPlan,
			simple(kgtypes.NodeTestPlan, kgtypes.NodeTestStep, kgtypes.NodeTestRun, kgtypes.EdgeRelatesTo, kgtypes.NodeFinding), byID},
		{"research", kgtypes.NodeResearch,
			simple(kgtypes.NodeResearch, kgtypes.NodeQuestion, kgtypes.NodeFinding, kgtypes.EdgeInformedBy, kgtypes.NodeDecision), byID},
		// tool_guide has no exported NodeType constant, so the literal stands in
		// for it here; the instruction arm's Tool Guides section does not filter
		// on the target's type, only on the uses edge.
		{"agent", kgtypes.NodeAgent,
			simple(kgtypes.NodeAgent, kgtypes.NodeSkill, kgtypes.NodeType("tool_guide"), kgtypes.EdgeUses, kgtypes.NodeSkill), byID},
		{"skill", kgtypes.NodeSkill,
			simple(kgtypes.NodeSkill, kgtypes.NodeType("tool_guide"), kgtypes.NodeType("tool_guide"), kgtypes.EdgeUses, kgtypes.NodeType("tool_guide")), byID},
		{"decision", kgtypes.NodeDecision,
			simple(kgtypes.NodeDecision, kgtypes.NodeFinding, kgtypes.NodeFinding, kgtypes.EdgeInformedBy, kgtypes.NodeResearch), byID},
		{"pattern", kgtypes.NodePattern,
			simple(kgtypes.NodePattern, kgtypes.NodeExample, kgtypes.NodeExample, kgtypes.EdgeReferences, kgtypes.NodeReference), byID},
		{"fallback", kgtypes.NodeType("document"),
			simple(kgtypes.NodeType("document"), kgtypes.NodeFinding, kgtypes.NodeFinding, kgtypes.EdgeRelatesTo, kgtypes.NodeFinding), byID},
		{"json", kgtypes.NodePlan,
			simple(kgtypes.NodePlan, kgtypes.NodePhase, kgtypes.NodeStep, kgtypes.EdgeInformedBy, kgtypes.NodeResearch),
			func() map[string]any { return map[string]any{"id": "root", "format": "json"} }},
	}

	covered := map[kgtypes.NodeType]bool{}
	for _, r := range rows {
		covered[r.nodeType] = true

		t.Run(r.name, func(t *testing.T) {
			counts := map[int]int{}
			sizes := map[int]int{}
			for _, w := range []int{narrow, wide} {
				gc := &countingGc{inner: r.build(w).gc()}
				raw, err := json.Marshal(r.args())
				require.NoError(t, err)
				res := Handle(context.Background(), gc, raw)
				require.False(t, res.IsError, "arm %s errored: %s", r.name, resultTextRender(res))
				counts[w] = gc.calls
				sizes[w] = len(resultTextRender(res))
			}

			// KNOWN POSITIVE, and it has to be one every arm can satisfy. The
			// arms render different sections — the project arm ignores
			// non-contains peers entirely, the decision arm renders no tree at
			// all — so no single marker string appears in all of them. What IS
			// universal is that a render which actually read the wider fixture
			// is LONGER than one that read the narrow one. Without this, "the
			// same count at both widths" would also be satisfied by an arm that
			// rendered nothing at either.
			require.Greater(t, sizes[wide], sizes[narrow],
				"arm %s rendered %d bytes at width %d and %d at width %d — it is not reading the "+
					"fixture, so the equal counts below would prove nothing",
				r.name, sizes[narrow], narrow, sizes[wide], wide)
			// Logged on success too, so the per-arm constant is readable from a
			// -v run rather than only from a failure. That number is what a
			// perf artifact cites; deriving it by breaking the code first would
			// be a worse way to learn it.
			t.Logf("arm %s: %d Execute calls, invariant across widths %d and %d", r.name, counts[wide], narrow, wide)
			assert.Equal(t, counts[narrow], counts[wide],
				"arm %s: wire cost must not scale with subtree width — %d Execute calls at width %d "+
					"but %d at width %d, so something is reading per node",
				r.name, counts[narrow], narrow, counts[wide], wide)
		})
	}

	// DISPATCH PARITY. Every node type Handle routes to a dedicated arm must be
	// exercised above. A Go switch's case set is unreadable at run time, so the
	// comparison is against handleDispatchNodeTypes, the slice declared beside
	// Handle for exactly this purpose and named in its doc comment.
	t.Run("the table covers every arm Handle dispatches", func(t *testing.T) {
		require.NotEmpty(t, handleDispatchNodeTypes)
		var missing []string
		for _, nt := range handleDispatchNodeTypes {
			if !covered[nt] {
				missing = append(missing, string(nt))
			}
		}
		assert.Empty(t, missing,
			"these dispatch arms have no row in the wire-cost table, so their cost is ungated: %v", missing)

		// The reverse direction: a row naming a type Handle does not dispatch
		// is either a typo or a stale arm. The one sanctioned exception is the
		// fallback row, whose whole point is a type the switch does not know.
		known := map[kgtypes.NodeType]bool{}
		for _, nt := range handleDispatchNodeTypes {
			known[nt] = true
		}
		for nt := range covered {
			if !known[nt] {
				assert.Equal(t, kgtypes.NodeType("document"), nt,
					"row type %q is neither a dispatch arm nor the declared fallback exclusion", nt)
			}
		}
	})
}
