// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeAssembleDeps is a minimal ClientDeps that exposes a controllable
// GraphCaller. Mirrors fakeDeps from collect_logs_e2e_test.go but with
// the GraphCaller field actually populated.
type fakeAssembleDeps struct {
	gc GraphCaller
}

func (d *fakeAssembleDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *fakeAssembleDeps) Sink() collector.Sink                         { return nil }
func (d *fakeAssembleDeps) RootDir() string                              { return "" }
func (d *fakeAssembleDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d *fakeAssembleDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *fakeAssembleDeps) WorkerReady() bool                            { return true }
func (d *fakeAssembleDeps) PropReady() bool                              { return true }
func (d *fakeAssembleDeps) PipelineReady() bool                          { return true }
func (d *fakeAssembleDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *fakeAssembleDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *fakeAssembleDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *fakeAssembleDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *fakeAssembleDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *fakeAssembleDeps) BackendResolver() BackendResolver             { return nil }
func (d *fakeAssembleDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *fakeAssembleDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *fakeAssembleDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *fakeAssembleDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *fakeAssembleDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *fakeAssembleDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d *fakeAssembleDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d *fakeAssembleDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d *fakeAssembleDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d *fakeAssembleDeps) PipelineScanner() PipelineScanner         { return nil }

func (d *fakeAssembleDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d *fakeAssembleDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d *fakeAssembleDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d *fakeAssembleDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *fakeAssembleDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *fakeAssembleDeps) TensionsProvider() TensionsProvider   { return nil }

// scriptGcAssemble is a tiny GraphCaller that answers query(id:)
// calls with canned node JSON keyed by ID. Sufficient for the
// fallback (rule/document) path that the integration test
// exercises — no edges needed.
type scriptGcAssemble struct {
	nodes map[string]string
	calls int
}

func (s *scriptGcAssemble) Call(_ context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	s.calls++
	if tool != "query" {
		return kgtools.ErrorResult("unexpected tool: " + tool), nil
	}
	var req struct {
		ID           string `json:"id"`
		IncludeEdges bool   `json:"include_edges"`
		Graph        string `json:"graph"`
	}
	_ = json.Unmarshal(args, &req)
	// list-graphs path used by resolveAssembleNode practice fallback;
	// answer with an empty graphs list so the fallback short-circuits.
	if req.Graph == "practice" {
		return kgtools.TextResult(`{"graphs":[]}`), nil
	}
	if req.IncludeEdges {
		return kgtools.TextResult(`{"edges":[]}`), nil
	}
	if body, ok := s.nodes[req.ID]; ok {
		return kgtools.TextResult(body), nil
	}
	return kgtools.TextResult(""), nil
}

// Execute satisfies render.Executor: render.FetchNode /
// IterEdges now ride Execute. Answers ByID from the seeded node JSON (decoded
// into a nodes_json carrier) and RETURN_MODE_EDGES with no edges. The practice
// list-graphs fallback still rides the legacy Call above.
func (s *scriptGcAssemble) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	s.calls++
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	body, ok := s.nodes[q.GetById()]
	if !ok {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var n knowledgev1.Node
	if uerr := json.Unmarshal([]byte(body), &n); uerr != nil {
		return &knowledgev1.ExecuteResponse{}, nil //nolint:nilerr // malformed seed → not found
	}
	resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&n}...)
	return resp, nil
}

// TestInterceptAssemble_NonAssembleCallFallsThrough confirms the
// intercept gates strictly on params.Name. Non-assemble calls return
// (false, _) so the chain continues to the next intercept.
func TestInterceptAssemble_NonAssembleCallFallsThrough(t *testing.T) {
	deps := &fakeAssembleDeps{gc: &scriptGcAssemble{}}
	handled, _ := InterceptAssemble(opCtx(), deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{}`),
	})
	assert.False(t, handled, "non-assemble call must not be claimed")
}

// TestInterceptAssemble_NilGraphCallerErrorsCleanly pins the
// graph-client-unavailable error path. Without a graph caller the
// render package cannot make wire calls, so the intercept must
// surface a structured error instead of panicking.
func TestInterceptAssemble_NilGraphCallerErrorsCleanly(t *testing.T) {
	deps := &fakeAssembleDeps{gc: nil}
	handled, res := InterceptAssemble(opCtx(), deps, kgtools.CallToolParams{
		Name:      "assemble",
		Arguments: json.RawMessage(`{"id":"x"}`),
	})
	require.True(t, handled, "assemble call with nil gc must still be claimed")
	require.True(t, res.IsError)
	assert.Contains(t, resultTextLocal(res), "graph client unavailable")
}

// TestInterceptAssemble_NoArgsErrors confirms the no-args recovery
// branch removal: Handle errors when id+type+name are all empty.
func TestInterceptAssemble_NoArgsErrors(t *testing.T) {
	deps := &fakeAssembleDeps{gc: &scriptGcAssemble{nodes: map[string]string{}}}
	handled, res := InterceptAssemble(opCtx(), deps, kgtools.CallToolParams{
		Name:      "assemble",
		Arguments: json.RawMessage(`{}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, resultTextLocal(res), "provide id")
}

// TestInterceptAssemble_RenderRoundTrip verifies the intercept
// routes an assemble(id:) call through render.Handle which then
// fetches the node via gc.Call and renders the fallback (NodeRule)
// shape. End-to-end shape: caller → InterceptAssemble → render.Handle
// → FetchNode → scriptGcAssemble → rendered markdown.
func TestInterceptAssemble_RenderRoundTrip(t *testing.T) {
	const id = "r1"
	nodeJSON := `{
		"id": "r1",
		"type": "rule",
		"symbol_name": "fixture-rule",
		"description": "rule desc",
		"status": "active"
	}`
	deps := &fakeAssembleDeps{gc: &scriptGcAssemble{nodes: map[string]string{id: nodeJSON}}}
	handled, res := InterceptAssemble(opCtx(), deps, kgtools.CallToolParams{
		Name:      "assemble",
		Arguments: json.RawMessage(`{"id":"r1"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "render: %s", resultTextLocal(res))

	out := resultTextLocal(res)
	assert.Contains(t, out, "# rule: fixture-rule")
	assert.Contains(t, out, "rule desc")
	assert.Contains(t, out, "ID: r1")
}

// resultTextLocal mirrors the server-side resultText helper for
// client-side tests; kept local to avoid widening the import surface.
func resultTextLocal(r kgtools.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
