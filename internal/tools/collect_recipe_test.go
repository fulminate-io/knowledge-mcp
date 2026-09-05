// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// recipeRoutingCaller routes recipe-collect Execute calls to per-graph node/edge
// sets, so the collect handler can drive recipe.RunRecipe through client fakes
// only — NEVER a store.
type recipeRoutingCaller struct {
	nodesByGraph map[string][]*knowledgev1.Node
	edgesByGraph map[string][]*knowledgev1.Edge

	// execCalls counts wire reads. An admitted run's first act is an Execute
	// that loads the SOURCE graph, so a zero here is what proves the refusal
	// fired ahead of recipe.RunRecipe rather than somewhere inside it.
	execCalls int
}

func (c *recipeRoutingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.execCalls++
	if req.GetMutation() != nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	g := req.GetTarget().GetGraph()
	resp := &knowledgev1.ExecuteResponse{}
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		resp.Edges = c.edgesByGraph[g]
		return resp, nil
	}
	resp.Nodes = c.nodesByGraph[g]
	return resp, nil
}

// recipeCaptureSink records WriteResult calls for the recipe handler test.
type recipeCaptureSink struct {
	results []*collectorwire.CollectResult
}

func (s *recipeCaptureSink) WriteResult(_ context.Context, _ string, r *collectorwire.CollectResult) error {
	s.results = append(s.results, r)
	return nil
}

// recipeDeps is a minimal ClientDeps serving a recipe GraphCaller + capturing Sink.
type recipeDeps struct {
	sink collector.Sink
	gc   GraphCaller
}

func (d *recipeDeps) LocalLiveness() LocalLiveness          { return nil }
func (d *recipeDeps) Sink() collector.Sink                  { return d.sink }
func (d *recipeDeps) SubgraphFetcher() CloudSubgraphFetcher { return nil }
func (d *recipeDeps) RootDir() string                       { return "" }
func (d *recipeDeps) UsageAnalyzer() UsageAnalyzerAPI       { return nil }

func (d *recipeDeps) PropReady() bool     { return true }
func (d *recipeDeps) PipelineReady() bool { return true }

func (d *recipeDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *recipeDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *recipeDeps) BackendResolver() BackendResolver             { return nil }
func (d *recipeDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *recipeDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *recipeDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *recipeDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *recipeDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *recipeDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *recipeDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *recipeDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *recipeDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *recipeDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *recipeDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *recipeDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *recipeDeps) SimilarityForcer() SimilarityForcer       { return nil }
func (d *recipeDeps) BlindSpotProvider() BlindSpotProvider     { return nil }
func (d *recipeDeps) ClusterProvider() ClusterProvider         { return nil }
func (d *recipeDeps) TensionsProvider() TensionsProvider       { return nil }

const recipeHandlerBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`

// recipeHandlerCaller serves the SOURCE graph and nothing else. There is no
// recipe bucket to seed: the body rides the payload.
func recipeHandlerCaller() *recipeRoutingCaller {
	return &recipeRoutingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphWebRaw): {
				{Id: "s1", Type: "section", SymbolName: "Message Router"},
			},
		},
	}
}

func recipeCollectParams(t *testing.T, collectType string) kgtools.CallToolParams {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"type":        collectType,
		"id":          "hohpe-eip",
		"transformer": "recipe",
		"recipe_body": recipeHandlerBody,
		"extract":     true,
	})
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "collect", Arguments: args}
}

// extractCollectParams builds a collect payload with the extract params merged
// over the working recipe payload.
func extractCollectParams(t *testing.T, extra map[string]any) kgtools.CallToolParams {
	t.Helper()
	args := map[string]any{
		"type":        "web",
		"id":          "hohpe-eip",
		"transformer": "recipe",
		"recipe_body": recipeHandlerBody,
		"extract":     true,
	}
	maps.Copy(args, extra)
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "collect", Arguments: raw}
}

// TestInterceptCollect_Extract_Rows proves a successful extract returns rows in
// the response and writes nothing.
func TestInterceptCollect_Extract_Rows(t *testing.T) {
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: recipeHandlerCaller()}

	handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{"extract": true}))
	require.True(t, handled)
	require.False(t, res.IsError, "expected a successful extract, got: %s", resultText(res))

	body := resultText(res)
	assert.Contains(t, body, "extract:", "the response leads with the extract header")
	assert.Contains(t, body, "Message Router", "the emitted field value is in the response")
	assert.Empty(t, sink.results, "extract must write nothing")
}

// TestInterceptCollect_Extract_Inline runs an inline body and exercises the
// inline manifest the dispatch builds.
func TestInterceptCollect_Extract_Inline(t *testing.T) {
	sink := &recipeCaptureSink{}
	caller := &recipeRoutingCaller{nodesByGraph: map[string][]*knowledgev1.Node{
		string(kgtypes.GraphWebRaw): {{Id: "s1", Type: "section", SymbolName: "Message Router"}},
	}}
	deps := &recipeDeps{sink: sink, gc: caller}

	handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{
		"recipe_body": recipeHandlerBody,
	}))
	require.True(t, handled)
	require.False(t, res.IsError, "expected a successful inline extract, got: %s", resultText(res))

	body := resultText(res)
	assert.Contains(t, body, "recipe=inline", "the header names the inline body")
	assert.Contains(t, body, "Message Router")
	assert.Empty(t, sink.results, "inline extract must write nothing")
}

// TestInterceptCollect_Recipe_RefusesForce proves a PLAIN recipe collect
// carrying force:true is refused — no extract param involved. It replaces the
// former extract-plus-force test, whose subject was the contradiction between
// two params rather than force itself.
//
// The refusal surfaces as an error result and is still handled, never forwarded
// as (false, _). The zero-Execute assertion is the load-bearing one: it proves
// the refusal fires ahead of recipe.RunRecipe rather than somewhere inside it,
// which is the whole reason this guard lands before force stops being honored.
//
// ITS KNOWN-POSITIVE IS TestInterceptCollect_SavedRecipeName_RefusedInlineBodyStillRuns,
// whose inline leg drives execCalls POSITIVE on the same fake in the same suite
// run. Without one, a counter that never moves would satisfy the zero below
// whether or not the refusal fired.
func TestInterceptCollect_Recipe_RefusesForce(t *testing.T) {
	sink := &recipeCaptureSink{}
	caller := recipeHandlerCaller()
	deps := &recipeDeps{sink: sink, gc: caller}

	handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{
		"force": true,
	}))
	require.True(t, handled, "a refusal is still handled client-side")
	require.True(t, res.IsError)
	msg := resultText(res)
	assert.Contains(t, msg, "force", "the refusal names the offending param")
	assert.Contains(t, msg, "writes nothing", "the refusal states why there is nothing for force to bypass")
	assert.Empty(t, sink.results, "a refused run writes nothing")
	assert.Zero(t, caller.execCalls,
		"RunRecipe must never be reached — its first act is an Execute loading the recipe node")
}

// TestInterceptCollect_Extract_ParamsNeedRecipe proves each of the four params
// is refused BY NAME when supplied without transformer=recipe, rather than
// accepted and dropped.
func TestInterceptCollect_Extract_ParamsNeedRecipe(t *testing.T) {
	for name, value := range map[string]any{
		"extract":     true,
		"recipe_body": "select section",
		"max_rows":    5,
		"max_bytes":   1024,
	} {
		t.Run(name, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{
				"type": "web", "id": "hohpe-eip", name: value,
			})
			require.NoError(t, err)

			sink := &recipeCaptureSink{}
			deps := &recipeDeps{sink: sink, gc: recipeHandlerCaller()}
			handled, res := InterceptCollect(opCtx(), deps,
				kgtools.CallToolParams{Name: "collect", Arguments: args})
			require.True(t, handled)
			require.True(t, res.IsError, "%s without transformer=recipe must be refused, not dropped", name)
			assert.Contains(t, resultText(res), name, "the refusal must name the offending param")
			assert.Empty(t, sink.results)
		})
	}
}

// TestInterceptCollect_SavedRecipeName_RefusedInlineBodyStillRuns is the
// property pair for the saved-recipe retirement, driven END TO END through
// InterceptCollect rather than at the helper.
//
// THE ZERO-EXECUTE LEG IS THE PLACEMENT CLAIM. A refusal raised inside
// recipe.RunRecipe would satisfy every message assertion here while still having
// paid for a source read first. Zero wire Executes is the only observable that
// proves the refusal fires ahead of the run.
//
// THE INLINE LEG IS THE KNOWN-POSITIVE FOR THAT ZERO, and it is deliberately in
// the same test rather than a sibling: the same fake, the same suite run, the
// same counter driven POSITIVE. Without it, a counter nobody wired and a
// genuinely-refused call are indistinguishable — and it is also the
// known-positive TestInterceptCollect_Recipe_RefusesForce cites for its own zero.
func TestInterceptCollect_SavedRecipeName_RefusedInlineBodyStillRuns(t *testing.T) {
	t.Run("a saved recipe name is refused and the refusal names the removal", func(t *testing.T) {
		sink := &recipeCaptureSink{}
		caller := recipeHandlerCaller()
		deps := &recipeDeps{sink: sink, gc: caller}

		args, err := json.Marshal(map[string]any{
			"type": "web", "id": "hohpe-eip", "transformer": "recipe",
			"recipe": "eip", "extract": true,
		})
		require.NoError(t, err)

		handled, res := InterceptCollect(opCtx(), deps,
			kgtools.CallToolParams{Name: "collect", Arguments: args})

		require.True(t, handled, "a refusal is still handled client-side, never forwarded as (false,_)")
		require.True(t, res.IsError, "naming a saved recipe must be refused")
		msg := resultText(res)
		assert.Contains(t, msg, "recipe", "the refusal names the offending param")
		assert.Contains(t, msg, "transformers",
			"the refusal names the FAMILY that was removed — 'saved recipes are gone' leaves a caller no way to know why")
		assert.Contains(t, msg, "recipe_body", "and names the surviving path")
		assert.Empty(t, sink.results, "a refused run writes nothing")
		assert.Zero(t, caller.execCalls,
			"THE PLACEMENT LEG: the refusal must fire ahead of recipe.RunRecipe, so no source read is paid for")
	})

	t.Run("an inline body still runs and returns rows", func(t *testing.T) {
		sink := &recipeCaptureSink{}
		caller := recipeHandlerCaller()
		deps := &recipeDeps{sink: sink, gc: caller}

		handled, res := InterceptCollect(opCtx(), deps, recipeCollectParams(t, "web"))

		require.True(t, handled)
		require.False(t, res.IsError, "expected a successful inline extract, got: %s", resultText(res))
		body := resultText(res)
		assert.Contains(t, body, "extract:", "the response leads with the extract header")
		assert.Contains(t, body, "Message Router", "the extracted row is in the response")
		assert.Positive(t, caller.execCalls,
			"THE KNOWN-POSITIVE: the same counter moves off zero, so the zero above is a decision rather than a dead wire")
	})
}

// TestInterceptCollect_RecipeWritesNothingAndDryRunIsRefused is the behavioral
// gate on the write path's removal.
//
// IT DOES NOT ASSERT THAT A SYMBOL IS GONE, which the compiler settles. It
// asserts what a caller observes: every admitted run emits rows and ships
// NOTHING to any sink, and dry_run — which meant "compute the projection but
// skip the write" — is refused by name now that there is no write to skip.
//
// THE SINK IS THE SUBJECT. A wrong-but-compiling re-wire that restored a
// WriteResult call into the extract render path builds clean and passes every
// message assertion; the empty sink across every input shape is what catches it.
// Each shape pairs a ZERO sink with a NON-EMPTY extracted row, so the zero is
// never the emptiness of a run that did nothing.
func TestInterceptCollect_RecipeWritesNothingAndDryRunIsRefused(t *testing.T) {
	t.Run("every admitted recipe run emits rows and ships nothing", func(t *testing.T) {
		shapes := map[string]map[string]any{
			"plain":           {},
			"with a row cap":  {"max_rows": 5},
			"with a byte cap": {"max_bytes": 4096},
		}
		for name, extra := range shapes {
			t.Run(name, func(t *testing.T) {
				sink := &recipeCaptureSink{}
				caller := recipeHandlerCaller()
				deps := &recipeDeps{sink: sink, gc: caller}

				handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, extra))

				require.True(t, handled)
				require.False(t, res.IsError, "expected a successful run, got: %s", resultText(res))
				assert.Contains(t, resultText(res), "Message Router",
					"the run really did produce a row — the empty sink below is not the emptiness of a no-op")
				assert.Empty(t, sink.results, "a recipe run ships NOTHING to any sink")
			})
		}

		t.Run("with an offset", func(t *testing.T) {
			// The offset path renders through a different disclosure branch, so it
			// gets its own shape rather than riding the loop above.
			sink := &recipeCaptureSink{}
			deps := &recipeDeps{sink: sink, gc: recipeHandlerCaller()}

			handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{"offset": 0}))

			require.True(t, handled)
			require.False(t, res.IsError, "expected a successful run, got: %s", resultText(res))
			assert.Contains(t, resultText(res), "Message Router")
			assert.Empty(t, sink.results, "a recipe run ships NOTHING to any sink")
		})
	})

	t.Run("dry run is refused by name", func(t *testing.T) {
		sink := &recipeCaptureSink{}
		caller := recipeHandlerCaller()
		deps := &recipeDeps{sink: sink, gc: caller}

		handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{"dry_run": true}))

		require.True(t, handled, "a refusal is still handled client-side")
		require.True(t, res.IsError, "dry_run must be refused, not accepted and dropped")
		msg := resultText(res)
		assert.Contains(t, msg, "dry_run", "the refusal names the offending param")
		assert.Contains(t, msg, "writes nothing",
			"and states the mechanical reason: there is no write for dry_run to skip")
		assert.Empty(t, sink.results)
		assert.Zero(t, caller.execCalls,
			"the refusal fires ahead of the run, so no source read is paid for")
	})
}
