// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAssemble_UnresolvedEdgeTargets_SkipAndRawLine pins two OPPOSITE
// behaviours that rest on the same underlying condition, which is exactly why
// one bulk rewrite can break both in a single edit.
//
// The condition used to be a per-target FetchNode returning an error or a nil
// node; it is now a miss in the bulk-hydrated map. On that condition a ticket's
// unresolvable uses or audits target is SKIPPED — the unresolved ids are
// reported separately through the ticket's own metadata keys — while the
// fallback arm's unresolvable peer still renders its shorter raw-id line.
func TestAssemble_UnresolvedEdgeTargets_SkipAndRawLine(t *testing.T) {
	t.Run("a ticket's unresolvable uses and audits targets are skipped", func(t *testing.T) {
		ticket := &knowledgev1.Node{
			Id: "tkt", Type: string(kgtypes.NodeTicket), SymbolName: "a-ticket",
			Status: kgtypes.StatusOpen,
		}
		kgtypes.SetValue(ticket, "unresolved_pattern_ids", "ghost-pattern")
		kgtypes.SetValue(ticket, "unresolved_language_patterns", "ghost-lang")
		resolved := &knowledgev1.Node{
			Id: "pat-real", Type: string(kgtypes.NodePattern), SymbolName: "RealPattern", Status: "active",
		}
		f := newGraphFixture().
			addKnowledgeNode(ticket).
			addKnowledgeNode(resolved).
			addKnowledgeEdge("tkt", "pat-real", kgtypes.EdgeUses).
			// Neither target below exists in the fixture, so both are map misses.
			addKnowledgeEdge("tkt", "ghost-pattern", kgtypes.EdgeUses).
			addKnowledgeEdge("tkt", "ghost-lang", kgtypes.EdgeAudits)

		out, err := callRender(context.Background(), f, map[string]any{"id": "tkt"})
		require.NoError(t, err)

		// Known positive: the RESOLVED pattern must render, so a render that
		// dropped everything cannot satisfy the absence assertions below.
		assert.Contains(t, out, "RealPattern", "the resolvable target must still render")
		assert.NotContains(t, out, "- [pattern] ghost-pattern",
			"an unresolvable uses target is skipped, not rendered as a bullet")
		assert.NotContains(t, out, "ghost-lang —",
			"an unresolvable audits target is skipped, not rendered as a bullet")
	})

	t.Run("the fallback arm renders an unresolvable peer as a raw-id line", func(t *testing.T) {
		f := newGraphFixture().
			addKnowledgeNode(&knowledgev1.Node{
				Id: "doc", Type: string(kgtypes.NodeDocument), SymbolName: "a-doc",
			}).
			addKnowledgeNode(&knowledgev1.Node{
				Id: "peer-real", Type: string(kgtypes.NodeFinding), SymbolName: "RealPeer",
			}).
			addKnowledgeEdge("doc", "peer-real", kgtypes.EdgeRelatesTo).
			addKnowledgeEdge("doc", "peer-ghost", kgtypes.EdgeRelatesTo).
			addKnowledgeEdge("peer-ghost-in", "doc", kgtypes.EdgeRelatesTo)

		out, err := callRender(context.Background(), f, map[string]any{"id": "doc"})
		require.NoError(t, err)

		// The resolved shape is the control: both line forms must be present in
		// the same render, or "the raw line appeared" could just mean nothing
		// resolved at all.
		assert.Contains(t, out, "- [relates-to] → [finding] RealPeer (ID: peer-real)",
			"a resolved peer renders the long form")
		assert.Contains(t, out, "- [relates-to] → peer-ghost",
			"an unresolved outgoing peer renders the shorter raw-id form")
		assert.Contains(t, out, "- [relates-to] ← peer-ghost-in",
			"an unresolved incoming peer renders the shorter raw-id form")
	})
}

// TestAssemble_SectionOrderDeterministic is the catcher for a bulk rewrite that
// ranges over the HYDRATED MAP instead of the edge slice. Go randomizes map
// iteration order, so such a rewrite reorders sections at random — passing most
// runs and failing occasionally, which is the worst failure mode a golden test
// can have.
//
// The iterations happen INSIDE the test rather than by re-invoking go test:
// re-running a cached binary would prove nothing, and this is a determinism
// check whose value comes from repeated renders within one process.
func TestAssemble_SectionOrderDeterministic(t *testing.T) {
	// Enough peers per section that a single map-order flip is overwhelmingly
	// likely to change the output: with n items, the chance a random shuffle
	// reproduces edge order is 1/n!.
	const peers = 8
	const iterations = 60

	buildTicket := func() *graphFixture {
		f := newGraphFixture()
		tkt := &knowledgev1.Node{
			Id: "det-tkt", Type: string(kgtypes.NodeTicket), SymbolName: "deterministic-ticket",
			Status: kgtypes.StatusOpen,
		}
		kgtypes.SetValue(tkt, "no_patterns_reason", "fixture")
		f.addKnowledgeNode(tkt)
		for i := range peers {
			d := string(rune('a' + i))
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: "dec-" + d, Type: string(kgtypes.NodeDecision), SymbolName: "decision-" + d,
			})
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: "fin-" + d, Type: string(kgtypes.NodeFinding), SymbolName: "finding-" + d,
			})
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: "pln-" + d, Type: string(kgtypes.NodePlan), SymbolName: "plan-" + d, Status: "active",
			})
			f.addKnowledgeEdge("det-tkt", "dec-"+d, kgtypes.EdgeInformedBy)
			f.addKnowledgeEdge("det-tkt", "fin-"+d, kgtypes.EdgeRelatesTo)
			f.link("det-tkt", "pln-"+d)
		}
		return f
	}

	buildFallback := func() *graphFixture {
		f := newGraphFixture().addKnowledgeNode(&knowledgev1.Node{
			Id: "det-doc", Type: string(kgtypes.NodeDocument), SymbolName: "deterministic-doc",
		})
		for i := range peers {
			d := string(rune('a' + i))
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: "out-" + d, Type: string(kgtypes.NodeFinding), SymbolName: "outgoing-" + d,
			})
			f.addKnowledgeNode(&knowledgev1.Node{
				Id: "in-" + d, Type: string(kgtypes.NodeFinding), SymbolName: "incoming-" + d,
			})
			f.addKnowledgeEdge("det-doc", "out-"+d, kgtypes.EdgeRelatesTo)
			f.addKnowledgeEdge("in-"+d, "det-doc", kgtypes.EdgeRelatesTo)
		}
		return f
	}

	for _, tc := range []struct {
		name    string
		fixture func() *graphFixture
		id      string
		// Section headers and per-peer name prefixes that must be present, so a
		// render that emitted nothing cannot pass by being trivially identical
		// to itself.
		wants  []string
		bodies []string
	}{
		{
			"ticket sections", buildTicket, "det-tkt",
			[]string{"## Plans", "## Linked Decisions", "## Linked Findings"},
			[]string{"decision-", "finding-", "plan-"},
		},
		{
			"fallback edge tables", buildFallback, "det-doc",
			[]string{"## Outgoing Edges", "## Incoming Edges"},
			[]string{"outgoing-", "incoming-"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := callRender(context.Background(), tc.fixture(), map[string]any{"id": tc.id})
			require.NoError(t, err)
			for _, w := range tc.wants {
				require.Contains(t, first, w,
					"the section must render at all — an empty render is identical to itself and proves nothing")
			}
			for _, b := range tc.bodies {
				require.Equal(t, peers, strings.Count(first, b),
					"every one of the %d peers must render under %q; a short section makes the "+
						"ordering assertion below weaker than it looks", peers, b)
			}

			for i := range iterations {
				again, err := callRender(context.Background(), tc.fixture(), map[string]any{"id": tc.id})
				require.NoError(t, err)
				require.Equal(t, first, again,
					"render %d differs from the first: a section is ranging over the hydrated map "+
						"instead of the edge slice, so its order is Go's map order rather than edge order", i)
			}
		})
	}
}
