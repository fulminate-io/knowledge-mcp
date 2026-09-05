// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// composeRecipeBody chains every major rule in one pipeline:
//
//	select section where matches kind=pattern → drops the non-pattern section
//	filter matches symbol_name=Router         → keeps only the Router-named subset
//	bind $fam := metadata.family              → per-row derived value
//	traverse detail out as $t                 → expands each section to its detail para
//	group_by metadata.layer                   → collapses the two paras (same layer) to one
//	emit pattern { ... } as $a                → emits one pattern (rep = first para)
//	lookup pattern by symbol_name as $b       → resolves the just-emitted StableID
//	link $a --[relates-to]--> $b              → lands one structural edge
const composeRecipeBody = `select section where {"matches": {"of": "node.metadata.kind", "regex": "pattern"}}
filter {"matches": {"of": "section.symbol_name", "regex": "Router"}}
bind $fam := node.metadata.family
traverse detail out as $t
group_by node.metadata.layer
emit pattern {
    type := "pattern"
    name := node.symbol_name
    fam := $fam
} as $a
lookup pattern by node.symbol_name as $b
link $a --[relates-to]--> $b`

// composeCaller seeds a source graph rich enough that each compose stage has a
// deterministic, non-trivial effect (see composeRecipeBody for the per-stage
// intent).
func composeCaller() *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphWebRaw): {
				// kept by where (kind=pattern), kept by filter (Router) → has detail p_a.
				{Id: "sec_a", Type: "section", SymbolName: "Router Alpha", Metadata: map[string]string{"kind": "pattern", "family": "routing"}},
				// kept by where, kept by filter (Router) → has detail p_b.
				{Id: "sec_b", Type: "section", SymbolName: "Router Beta", Metadata: map[string]string{"kind": "pattern", "family": "routing"}},
				// kept by where but DROPPED by filter (no "Router").
				{Id: "sec_c", Type: "section", SymbolName: "Aggregator Gamma", Metadata: map[string]string{"kind": "pattern", "family": "routing"}},
				// DROPPED by where (kind != pattern).
				{Id: "sec_d", Type: "section", SymbolName: "Router Delta", Metadata: map[string]string{"kind": "other", "family": "misc"}},
				// detail paras share the same layer so group_by collapses them to one.
				{Id: "p_a", Type: "paragraph", SymbolName: "alpha-detail", Metadata: map[string]string{"layer": "L1"}},
				{Id: "p_b", Type: "paragraph", SymbolName: "beta-detail", Metadata: map[string]string{"layer": "L1"}},
			},
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{
			string(kgtypes.GraphWebRaw): {
				{FromId: "sec_a", ToId: "p_a", Type: "detail"},
				{FromId: "sec_b", ToId: "p_b", Type: "detail"},
			},
		},
	}
}

// TestRunRecipe_MultiRuleCompose_ReturnsExpectedRows drives one recipe that
// chains select-where → filter → bind → traverse → group_by → emit → lookup →
// link through RunRecipe and asserts the EXACT extracted result: the emitted
// row set, its bound field, its deterministic identity, and the hand-computed
// Stats. This pins cross-stage composition (filter-then-bind-then-group_by
// ordering), not just per-rule behavior.
//
// IT WAS "ShipsExpectedResult" AND ASSERTED ON THE SHIPPED CollectResult. The
// write path is gone, so the shipped-set assertions moved onto the rows an
// extract returns; the composition property they were really about is
// unchanged, which is why the test was re-pointed rather than retired with the
// mechanism that used to carry its output.
func TestRunRecipe_MultiRuleCompose_ReturnsExpectedRows(t *testing.T) {
	caller := composeCaller()
	const slug = "hohpe-eip"

	res, err := runInline(t, caller, composeRecipeBody)
	require.NoError(t, err)
	require.NotNil(t, res.Extract)

	// EXACT emitted row set: group_by collapsed the two same-layer detail paras
	// to one representative (the first, p_a → "alpha-detail").
	var gotNames []string
	for _, row := range res.Extract.Rows {
		gotNames = append(gotNames, row.Fields["name"])
	}
	assert.ElementsMatch(t, []string{"alpha-detail"}, gotNames,
		"where+filter+traverse+group_by compose down to the single representative")

	// The non-recognized 'fam' field carried the per-row bind value that
	// survived traverse+group_by.
	require.Len(t, res.Extract.Rows, 1)
	assert.Equal(t, "routing", res.Extract.Rows[0].Fields["fam"],
		"the $fam bind survived through to the emit")
	assert.Equal(t, "p_a", res.Extract.Rows[0].SourceNodeID,
		"the row anchors at the representative source row")
	assert.Equal(t, "pattern", res.Extract.Rows[0].Type)

	// The emit's deterministic id is still computed from the in-run sentinel
	// target, which is what the lookup and both link endpoints resolved against.
	target := TargetSpec{GraphType: extractSentinelGraphType, Name: slug}
	assert.Equal(t, StableID(TargetKey(target), slug, "pattern", "alpha-detail"),
		res.Nodes[0].GetId(), "the identity the lookup resolved is the emit's StableID")

	// Exactly one cross-emit link landed, and exactly one lineage edge for the
	// single emit.
	var linkEdges, lineageEdges int
	for _, e := range res.Edges {
		if e.Type == kgtypes.EdgeType("relates-to") {
			linkEdges++
		}
	}
	for _, e := range res.Lineage {
		if e.Type == kgtypes.EdgeTranslatedFrom {
			lineageEdges++
			assert.Equal(t, slug, SourceFromEvidence(e.Evidence))
			assert.Equal(t, "p_a", e.ToID, "lineage anchors at the representative source row")
		}
	}
	assert.Equal(t, 1, linkEdges, "exactly one cross-emit link landed")
	assert.Equal(t, 1, lineageEdges, "exactly one lineage edge for the single emit")

	// Hand-computed Stats.
	assert.Equal(t, 1, res.Stats.LookupsResolved)
	assert.Zero(t, res.Stats.LookupMisses)
	assert.Zero(t, res.Stats.LinkMisses)
	assert.Zero(t, res.Stats.SkippedChunks)
}
