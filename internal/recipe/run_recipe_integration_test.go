// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// routingCaller is a foundation.GraphCaller that routes each Execute to a
// per-graph-type node/edge set, so a full RunRecipe (recipe-bucket read + source
// graph read + optional target read/delete) can be driven against fakes only —
// NEVER a store. It records delete mutations for force-path assertions.
type routingCaller struct {
	nodesByGraph map[string][]*knowledgev1.Node
	edgesByGraph map[string][]*knowledgev1.Edge

	mutations []*knowledgev1.MutationPlan
}

func (c *routingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		c.mutations = append(c.mutations, m)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	g := req.GetTarget().GetGraph()
	q := req.GetQuery()
	resp := &knowledgev1.ExecuteResponse{}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		resp.Edges = c.edgesByGraph[g]
		return resp, nil
	}
	resp.Nodes = c.nodesByGraph[g]
	return resp, nil
}

const fullRecipeBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
} as $p
traverse relates-to out as $t
emit pattern {
    type := "pattern"
    name := $t.symbol_name
} as $rp
link $p --[relates-to]--> $rp`

func fullRecipeCaller() *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphTransformers): {{
				Id: "rec1", Type: "recipe", SymbolName: "eip", Content: fullRecipeBody, UpdatedAt: 1,
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
		edgesByGraph: map[string][]*knowledgev1.Edge{
			string(kgtypes.GraphWebRaw): {{FromId: "s1", ToId: "s2", Type: "relates-to"}},
		},
	}
}

func TestRunRecipe_FullRun_EmitsToSink(t *testing.T) {
	caller := fullRecipeCaller()
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}

	res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)
	require.NotNil(t, res)

	// Emitted patterns + lineage landed in the Sink at practice/design-patterns.
	require.Equal(t, 1, sink.calls)
	got := sink.results[0]
	assert.Equal(t, kgtypes.GraphPractice, got.GraphType)
	assert.Equal(t, "design-patterns", got.GraphName)
	assert.NotEmpty(t, got.Nodes, "emitted nodes shipped")
	// Edges carry the link edge + the translated-from lineage edges.
	var sawLineage, sawLink bool
	for _, e := range got.Edges {
		if e.Type == kgtypes.EdgeTranslatedFrom {
			sawLineage = true
		}
		if e.Type == kgtypes.EdgeType("relates-to") {
			sawLink = true
		}
	}
	assert.True(t, sawLineage, "lineage edges shipped to the Sink")
	assert.True(t, sawLink, "the cross-emit link edge shipped to the Sink")
	assert.Positive(t, res.Stats.NodesEmitted)
}

func TestRunRecipe_Idempotent_SameStableIDsAcrossRuns(t *testing.T) {
	// Force=false re-run over the same source produces identical node IDs —
	// StableID is deterministic, so a second run intends no duplicates.
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}

	run := func() []string {
		sink := &captureSink{}
		_, err := RunRecipe(context.Background(), fullRecipeCaller(), sink, "src-graph", kgtypes.GraphWebRaw, opts)
		require.NoError(t, err)
		require.Len(t, sink.results, 1)
		var ids []string
		for _, n := range sink.results[0].Nodes {
			ids = append(ids, n.Id)
		}
		return ids
	}

	first := run()
	second := run()
	require.NotEmpty(t, first)
	assert.ElementsMatch(t, first, second, "Force=false re-run must reproduce identical StableIDs")
}

func TestRunRecipe_DryRun_SkipsWriteReturnsStats(t *testing.T) {
	caller := fullRecipeCaller()
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip"), DryRun: true}

	res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)

	assert.Zero(t, sink.calls, "dry_run must NOT write to the Sink")
	assert.Positive(t, res.Stats.NodesEmitted, "dry_run still returns the projected Stats")
}

func TestRunRecipe_Force_DeletesOnlyCurrentSlugBeforeEmit(t *testing.T) {
	caller := fullRecipeCaller()
	// Two existing target nodes: old_alpha from THIS run's slug (hohpe-eip),
	// old_beta from a DIFFERENT slug. Force must delete only old_alpha.
	caller.nodesByGraph[string(kgtypes.GraphPractice)] = []*knowledgev1.Node{
		{Id: "old_alpha", Type: "pattern"},
		{Id: "old_beta", Type: "pattern"},
	}
	caller.edgesByGraph[string(kgtypes.GraphPractice)] = []*knowledgev1.Edge{
		{FromId: "old_alpha", ToId: "s_a", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor("hohpe-eip")},
		{FromId: "old_beta", ToId: "s_b", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor("other-slug")},
	}
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip"), Force: true}

	res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)

	// Force triggered exactly one HARD delete of only the current-slug node —
	// the other-slug node is untouched.
	require.Len(t, caller.mutations, 1)
	m := caller.mutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
	assert.True(t, m.GetHardDelete())
	assert.ElementsMatch(t, []string{"old_alpha"}, m.GetSelection().GetIds())
	assert.Equal(t, 1, res.Stats.ForceDeleted)
	// And the fresh emit still shipped.
	assert.Equal(t, 1, sink.calls)
}

func TestRunRecipe_RecipeNotFound(t *testing.T) {
	caller := fullRecipeCaller()
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "does-not-exist")}
	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRunRecipe_SourceTypeMismatch(t *testing.T) {
	caller := fullRecipeCaller()
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}
	// The recipe declares source_graph_type=web; a pdf collect must be rejected
	// with a typed error naming BOTH types — before any source read or write.
	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphPDFRaw, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pdf")
	assert.Contains(t, err.Error(), "web")
	assert.Zero(t, sink.calls, "a mismatch writes nothing")
}

// recipeMetadataKeys are the three keys RunRecipe requires on the recipe node;
// blanking any one must error before any write.
func TestRunRecipe_MissingMetadata(t *testing.T) {
	for _, key := range []string{"source_graph_type", "target_graph_type", "target_name"} {
		t.Run(key, func(t *testing.T) {
			caller := fullRecipeCaller()
			// Blank exactly one required metadata key on the recipe node.
			recNode := caller.nodesByGraph[string(kgtypes.GraphTransformers)][0]
			recNode.Metadata[key] = ""

			sink := &captureSink{}
			opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}
			_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
			require.Error(t, err, "a blank %q must error", key)
			assert.Contains(t, err.Error(), "missing required metadata")
			assert.Zero(t, sink.calls, "no write on a metadata error")
		})
	}
}

// TestRunRecipe_BodyParseError pins the parse-error seam: an unparseable recipe
// Content makes RunRecipe surface the parse error and write nothing. The
// astCache is cleared for this recipe's (id, UpdatedAt) first so a prior run's
// cached AST cannot mask the parse failure (cf. run_recipe_test.go astCache.Delete).
func TestRunRecipe_BodyParseError(t *testing.T) {
	caller := fullRecipeCaller()
	recNode := caller.nodesByGraph[string(kgtypes.GraphTransformers)][0]
	// `select` with no node type is rejected by parseSelect. Give the node a
	// distinct (id, UpdatedAt) and clear any cached AST under it.
	recNode.Id = "rec-parse-err"
	recNode.UpdatedAt = 99
	recNode.Content = "select"
	astCache.Delete(astCacheKey{id: recNode.Id, updatedAt: recNode.UpdatedAt})

	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}
	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.Error(t, err, "an unparseable body must surface a parse error")
	assert.Contains(t, err.Error(), "parse")
	assert.Zero(t, sink.calls, "a parse error writes nothing")
}

// TestRunRecipe_MalformedManifest pins the manifest-parse seam: a SourceManifest
// with no key=value structure errors out of ParseSourceManifest before any
// source read or write.
func TestRunRecipe_MalformedManifest(t *testing.T) {
	caller := fullRecipeCaller()
	sink := &captureSink{}
	opts := Options{SourceManifest: "garbage-no-equals"}
	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest")
	assert.Zero(t, sink.calls, "a malformed manifest writes nothing")
}

// TestRunRecipe_EmptySourceGraph_FailSoft pins the empty-source path: when the
// source graph has zero nodes the recipe selects nothing and emits nothing,
// returning no error. Observed behavior (confirmed first-hand): writeResult is
// invoked UNCONDITIONALLY on a non-DryRun run (run_recipe.go), so the sink is
// still called exactly once with an empty Nodes slice.
func TestRunRecipe_EmptySourceGraph_FailSoft(t *testing.T) {
	caller := fullRecipeCaller()
	// Drop every source node so loadSourceView materializes an empty graph.
	caller.nodesByGraph[string(kgtypes.GraphWebRaw)] = nil
	caller.edgesByGraph[string(kgtypes.GraphWebRaw)] = nil

	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}
	res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err, "an empty source graph is fail-soft, not an error")
	assert.Zero(t, res.Stats.NodesEmitted, "nothing to select means nothing to emit")

	// Pin the actual write behavior: non-DryRun always ships the (empty) Result.
	require.Equal(t, 1, sink.calls, "writeResult is invoked unconditionally on non-DryRun")
	assert.Empty(t, sink.results[0].Nodes, "the shipped Result carries zero emitted nodes")
}

// TestRunRecipe_SinkErrorPropagates pins the writeResult failure path: a sink
// that returns an error makes RunRecipe surface it wrapped. Drives the
// captureSink.err field.
func TestRunRecipe_SinkErrorPropagates(t *testing.T) {
	caller := fullRecipeCaller()
	sink := &captureSink{err: errSinkBoom}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}
	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
	require.ErrorIs(t, err, errSinkBoom, "the sink error is surfaced (wrapped) by RunRecipe")
	assert.Contains(t, err.Error(), "write target", "writeResult wraps the sink error")
}

// errSinkBoom is the sentinel a failing captureSink returns.
var errSinkBoom = errors.New("sink boom")

// installRecipeBody swaps the recipe node's Content/id/UpdatedAt on a
// routingCaller and clears any cached AST under the new key, so a fresh recipe
// body parses without a stale-cache mask.
func installRecipeBody(caller *routingCaller, id, body string, updatedAt int64) {
	recNode := caller.nodesByGraph[string(kgtypes.GraphTransformers)][0]
	recNode.Id = id
	recNode.Content = body
	recNode.UpdatedAt = updatedAt
	astCache.Delete(astCacheKey{id: id, updatedAt: updatedAt})
}

// TestRunRecipe_NonExistentEdgeAndField_FailSoft proves the interpreter's
// degrade-not-die contract end-to-end through RunRecipe: a recipe that traverses
// an edge type absent from the source (empty traversed rowset) or reads an
// absent field (resolves "") returns NO error, emits nothing, and never panics.
func TestRunRecipe_NonExistentEdgeAndField_FailSoft(t *testing.T) {
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}

	t.Run("NonExistentEdge", func(t *testing.T) {
		caller := fullRecipeCaller()
		// Traverse an edge type that does not exist → edgesFrom returns nil → the
		// post-traverse rowset is empty → the dependent emit produces zero nodes.
		installRecipeBody(caller, "rec-no-edge", `select section
traverse no_such_edge out as $t
emit pattern {
    name := $t
}`, 101)

		sink := &captureSink{}
		res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
		require.NoError(t, err, "a missing edge type degrades to empty, not an error")
		assert.Zero(t, res.Stats.NodesEmitted, "the empty traversed rowset emits nothing")
	})

	t.Run("AbsentField", func(t *testing.T) {
		caller := fullRecipeCaller()
		// Read an absent metadata key → readNodeField default → "" → the empty-name
		// skip path fires for every row (SkippedChunks), emitting nothing.
		installRecipeBody(caller, "rec-absent-field", `select section
emit pattern {
    name := node.metadata.absent
}`, 102)

		sink := &captureSink{}
		res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)
		require.NoError(t, err, "an absent field degrades to empty, not an error")
		assert.Zero(t, res.Stats.NodesEmitted, "every row resolves an empty name and is skipped")
		assert.Positive(t, res.Stats.SkippedChunks, "the empty-name rows count as skipped chunks")
	})
}
