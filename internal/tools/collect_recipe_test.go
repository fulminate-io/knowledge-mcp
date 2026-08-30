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
}

func (c *recipeRoutingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
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

func (d *recipeDeps) LocalLiveness() LocalLiveness    { return nil }
func (d *recipeDeps) Sink() collector.Sink            { return d.sink }
func (d *recipeDeps) RootDir() string                 { return "" }
func (d *recipeDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

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

func recipeHandlerCaller() *recipeRoutingCaller {
	return &recipeRoutingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphTransformers): {{
				Id: "rec1", Type: "recipe", SymbolName: "eip", Content: recipeHandlerBody, UpdatedAt: 1,
				Metadata: map[string]string{
					"source_graph_type": string(kgtypes.GraphWebRaw),
					"target_graph_type": string(kgtypes.GraphPractice),
					"target_name":       "design-patterns",
				},
			}},
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
		"recipe":      "eip",
	})
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "collect", Arguments: args}
}

// TestInterceptCollect_RecipeWeb_RunsClientSide proves the recipe-via-collect
// dispatch runs RunRecipe and ships to the target — and NEVER returns (false,_).
func TestInterceptCollect_RecipeWeb_RunsClientSide(t *testing.T) {
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: recipeHandlerCaller()}

	handled, res := InterceptCollect(opCtx(), deps, recipeCollectParams(t, "web"))
	require.True(t, handled, "recipe collect must be handled client-side, never forwarded as (false,_)")
	require.False(t, res.IsError, "expected a successful recipe run, got: %s", resultText(res))
	require.Len(t, sink.results, 1, "the projected practice graph shipped to the Sink")
	assert.Equal(t, kgtypes.GraphPractice, sink.results[0].GraphType)
	assert.Equal(t, "design-patterns", sink.results[0].GraphName)
}

// TestInterceptCollect_RecipeTypeMismatch_Errors proves a pdf collect against a
// web-source recipe returns a mismatch error naming both types, still handled.
func TestInterceptCollect_RecipeTypeMismatch_Errors(t *testing.T) {
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: recipeHandlerCaller()}

	handled, res := InterceptCollect(opCtx(), deps, recipeCollectParams(t, "pdf"))
	require.True(t, handled, "a mismatch is still handled client-side, not forwarded")
	require.True(t, res.IsError, "type mismatch must surface an error")
	msg := resultText(res)
	assert.Contains(t, msg, "pdf")
	assert.Contains(t, msg, "web")
	assert.Empty(t, sink.results, "a mismatch writes nothing")
}

// extractCollectParams builds a collect payload with the extract params merged
// over the working recipe payload.
func extractCollectParams(t *testing.T, extra map[string]any) kgtools.CallToolParams {
	t.Helper()
	args := map[string]any{
		"type":        "web",
		"id":          "hohpe-eip",
		"transformer": "recipe",
		"recipe":      "eip",
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

// TestInterceptCollect_Extract_Inline runs an inline body with NO recipe node
// present, which also exercises the inline manifest the dispatch builds.
func TestInterceptCollect_Extract_Inline(t *testing.T) {
	sink := &recipeCaptureSink{}
	// A caller serving the source graph only — no transformers bucket at all.
	caller := &recipeRoutingCaller{nodesByGraph: map[string][]*knowledgev1.Node{
		string(kgtypes.GraphWebRaw): {{Id: "s1", Type: "section", SymbolName: "Message Router"}},
	}}
	deps := &recipeDeps{sink: sink, gc: caller}

	handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{
		"extract": true, "recipe": "", "recipe_body": recipeHandlerBody,
	}))
	require.True(t, handled)
	require.False(t, res.IsError, "expected a successful inline extract, got: %s", resultText(res))

	body := resultText(res)
	assert.Contains(t, body, "recipe=inline", "the header names the inline body")
	assert.Contains(t, body, "Message Router")
	assert.Empty(t, sink.results, "inline extract must write nothing")
}

// TestInterceptCollect_Extract_RefusesForce proves the refusal surfaces as an
// error result and is still handled, never forwarded as (false, _).
func TestInterceptCollect_Extract_RefusesForce(t *testing.T) {
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: recipeHandlerCaller()}

	handled, res := InterceptCollect(opCtx(), deps, extractCollectParams(t, map[string]any{
		"extract": true, "force": true,
	}))
	require.True(t, handled, "a refusal is still handled client-side")
	require.True(t, res.IsError)
	msg := resultText(res)
	assert.Contains(t, msg, "force")
	assert.Contains(t, msg, "extract")
	assert.Empty(t, sink.results)
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
