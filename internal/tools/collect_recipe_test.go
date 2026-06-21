// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
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

func (d *recipeDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *recipeDeps) Sink() collector.Sink                         { return d.sink }
func (d *recipeDeps) RootDir() string                              { return "" }
func (d *recipeDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *recipeDeps) WorkerReady() bool                            { return true }
func (d *recipeDeps) PropReady() bool                              { return true }
func (d *recipeDeps) PipelineReady() bool                          { return true }
func (d *recipeDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *recipeDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *recipeDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *recipeDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *recipeDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *recipeDeps) BackendResolver() BackendResolver             { return nil }
func (d *recipeDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *recipeDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *recipeDeps) RepoResolver() *RepoResolver                  { return nil }
func (d *recipeDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *recipeDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *recipeDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *recipeDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *recipeDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *recipeDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *recipeDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *recipeDeps) SimilarityForcer() SimilarityForcer           { return nil }
func (d *recipeDeps) BlindSpotProvider() BlindSpotProvider         { return nil }
func (d *recipeDeps) ClusterProvider() ClusterProvider             { return nil }
func (d *recipeDeps) TensionsProvider() TensionsProvider           { return nil }

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

	handled, res := InterceptCollect(deps, recipeCollectParams(t, "web"))
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

	handled, res := InterceptCollect(deps, recipeCollectParams(t, "pdf"))
	require.True(t, handled, "a mismatch is still handled client-side, not forwarded")
	require.True(t, res.IsError, "type mismatch must surface an error")
	msg := resultText(res)
	assert.Contains(t, msg, "pdf")
	assert.Contains(t, msg, "web")
	assert.Empty(t, sink.results, "a mismatch writes nothing")
}
