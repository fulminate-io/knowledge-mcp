// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const simpleEmitRecipeBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`

// simpleEmitCaller seeds a routingCaller whose recipe is a plain select→emit
// (one pattern per section) so the deterministic StableIDs are easy to compute
// for round-trip assertions.
func simpleEmitCaller() *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphTransformers): {{
				Id: "rec-simple", Type: "recipe", SymbolName: "eip", Content: simpleEmitRecipeBody, UpdatedAt: 1,
				Metadata: map[string]string{
					"source_graph_type": string(kgtypes.GraphWebRaw),
					"target_graph_type": string(kgtypes.GraphPractice),
					"target_name":       "design-patterns",
				},
			}},
			string(kgtypes.GraphWebRaw): {
				{Id: "s1", Type: "section", SymbolName: "Message Router"},
				{Id: "s2", Type: "section", SymbolName: "Message Channel"},
			},
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{},
	}
}

// TestRunRecipe_ForceReRun_DeletesPriorBeforeReEmit pins the second half of the
// Force contract (beyond the single-pass test): a Force run over a target that
// already holds the prior run's SAME-slug emissions hard-deletes exactly those
// ids, then re-emits the same deterministic StableIDs — so the re-emit lands on
// the very ids it just deleted without colliding.
//
// NOTE on the fake: routingCaller records the DELETE mutation but does NOT model
// tombstone persistence — it never actually removes nodes from its in-memory set,
// so a soft tombstone and a hard delete are indistinguishable in its post-state.
// This test therefore asserts the two OBSERVABLE halves of Force-correctness —
// (1) correct doomed-id targeting and (2) deterministic re-emit onto those same
// ids — and relies on the separate HardDelete:true assertion (here and in
// TestForceDeleteBySource) for the soft-vs-hard distinction.
func TestRunRecipe_ForceReRun_DeletesPriorBeforeReEmit(t *testing.T) {
	caller := simpleEmitCaller()
	target := TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"}
	const slug = "hohpe-eip"

	// The deterministic StableIDs this recipe produces for the two sections.
	idRouter := StableID(TargetKey(target), slug, "pattern", "Message Router")
	idChannel := StableID(TargetKey(target), slug, "pattern", "Message Channel")

	// Prior emissions already live in the target graph under THOSE ids, each with
	// a translated-from edge carrying the current slug — exactly what a previous
	// run of this recipe would have written.
	caller.nodesByGraph[string(kgtypes.GraphPractice)] = []*knowledgev1.Node{
		{Id: idRouter, Type: "pattern", SymbolName: "Message Router"},
		{Id: idChannel, Type: "pattern", SymbolName: "Message Channel"},
	}
	caller.edgesByGraph[string(kgtypes.GraphPractice)] = []*knowledgev1.Edge{
		{FromId: idRouter, ToId: "s1", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor(slug)},
		{FromId: idChannel, ToId: "s2", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor(slug)},
	}

	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest(slug, "eip"), Force: true}
	res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)

	// Half 1 — correct doomed-id targeting: exactly one HARD delete over exactly
	// the two prior-emission ids.
	require.Len(t, caller.mutations, 1, "one batched delete, not N per-id")
	m := caller.mutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
	assert.True(t, m.GetHardDelete(), "force-delete must hard-delete to avoid tombstone collision")
	assert.ElementsMatch(t, []string{idRouter, idChannel}, m.GetSelection().GetIds())
	assert.Equal(t, 2, res.Stats.ForceDeleted)

	// Half 2 — deterministic re-emit onto those same ids: the freshly shipped
	// nodes carry exactly the StableIDs that were just deleted.
	require.Equal(t, 1, sink.calls, "the fresh emit still shipped")
	var reEmittedIDs []string
	for _, n := range sink.results[0].Nodes {
		reEmittedIDs = append(reEmittedIDs, n.Id)
	}
	assert.ElementsMatch(t, []string{idRouter, idChannel}, reEmittedIDs,
		"re-emit reproduces the just-deleted StableIDs — a hard delete makes that non-colliding")
}

// composeRecipeBody chains every major rule in one pipeline:
//
//	select section where kind~=pattern   → drops the non-pattern section
//	filter symbol_name ~= Router         → keeps only the Router-named subset
//	bind $fam := metadata.family         → per-row derived value
//	traverse detail out as $t            → expands each section to its detail para
//	group_by metadata.layer              → collapses the two paras (same layer) to one
//	emit pattern { ... } as $a           → emits one pattern (rep = first para)
//	lookup pattern by symbol_name as $b  → resolves the just-emitted StableID
//	link $a --[relates-to]--> $b         → lands one structural edge
const composeRecipeBody = `select section where node.metadata.kind ~= /pattern/
filter section.symbol_name ~= /Router/
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
			string(kgtypes.GraphTransformers): {{
				Id: "rec-compose", Type: "recipe", SymbolName: "eip", Content: composeRecipeBody, UpdatedAt: 1,
				Metadata: map[string]string{
					"source_graph_type": string(kgtypes.GraphWebRaw),
					"target_graph_type": string(kgtypes.GraphPractice),
					"target_name":       "design-patterns",
				},
			}},
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

// TestRunRecipe_MultiRuleCompose_ShipsExpectedResult drives one recipe that
// chains select-where → filter → bind → traverse → group_by → emit → lookup →
// link through RunRecipe and asserts the EXACT final shipped CollectResult:
// the emitted node-name set, the link edge, the lineage edge + slug, and the
// hand-computed Stats. This pins cross-stage composition (filter-then-bind-then-
// group_by ordering), not just per-rule behavior.
func TestRunRecipe_MultiRuleCompose_ShipsExpectedResult(t *testing.T) {
	caller := composeCaller()
	target := TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"}
	const slug = "hohpe-eip"

	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest(slug, "eip")}
	res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)

	require.Equal(t, 1, sink.calls, "the composed run ships exactly one CollectResult")
	got := sink.results[0]
	assert.Equal(t, kgtypes.GraphPractice, got.GraphType)
	assert.Equal(t, "design-patterns", got.GraphName)

	// EXACT emitted node set: group_by collapsed the two same-layer detail paras
	// to one representative (the first, p_a → "alpha-detail").
	var gotNames []string
	for _, n := range got.Nodes {
		gotNames = append(gotNames, n.SymbolName)
	}
	assert.ElementsMatch(t, []string{"alpha-detail"}, gotNames,
		"where+filter+traverse+group_by compose down to the single representative")
	// The non-recognized 'fam' field folded into Metadata, carrying the per-row
	// bind value that survived traverse+group_by.
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "routing", got.Nodes[0].Metadata["fam"], "the $fam bind survived through to the emit")

	// The emit's deterministic id, reused by the lookup and both link endpoints.
	wantID := StableID(TargetKey(target), slug, "pattern", "alpha-detail")
	assert.Equal(t, wantID, got.Nodes[0].Id)

	// Edges carry exactly one relates-to link (the resolved lookup fed it) plus
	// one translated-from lineage edge with the run's slug.
	var linkEdges, lineageEdges int
	for _, e := range got.Edges {
		switch e.Type {
		case kgtypes.EdgeType("relates-to"):
			linkEdges++
			assert.Equal(t, wantID, e.FromID)
			assert.Equal(t, wantID, e.ToID)
		case kgtypes.EdgeTranslatedFrom:
			lineageEdges++
			assert.Equal(t, slug, SourceFromEvidence(e.Evidence))
			assert.Equal(t, "p_a", e.ToID, "lineage anchors at the representative source row")
		}
	}
	assert.Equal(t, 1, linkEdges, "exactly one cross-emit link landed")
	assert.Equal(t, 1, lineageEdges, "exactly one lineage edge for the single emit")

	// Hand-computed Stats.
	assert.Equal(t, 1, res.Stats.NodesEmitted)
	assert.Equal(t, 1, res.Stats.LookupsResolved)
	assert.Zero(t, res.Stats.LookupMisses)
	assert.Zero(t, res.Stats.LinkMisses)
	assert.Zero(t, res.Stats.SkippedChunks)
}
