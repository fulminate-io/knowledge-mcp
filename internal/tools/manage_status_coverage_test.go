// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
)

// coverageFake is a statsRPC that (a) records every StatsRequest it receives so
// the test can assert IncludeCoverage was set (the T2 gate trigger), (b) serves
// per-graph GraphStats keyed by the resolved instance label, and (c) serves a
// RETURN_MODE_GRAPH_NAMES enumeration that returns ONE named "code" graph and an
// EMPTY name for knowledge (mirroring the real drop of empty names) — proving the
// knowledge row is rendered via the explicit empty-name selector, not enumeration.
type coverageFake struct {
	reqs       []*knowledgev1.StatsRequest
	statsByKey map[string]*knowledgev1.GraphStats
}

func (f *coverageFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Only the code graph reports a named instance; everything else (incl the
	// default knowledge graph) enumerates empty — which listGraphNamesOfType drops.
	if req.GetTarget().GetGraph() == "code" {
		return &knowledgev1.ExecuteResponse{GraphNames: []*knowledgev1.GraphInfo{{Name: "myrepo"}}}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *coverageFake) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.reqs = append(f.reqs, req)
	// Resolve the row label the renderer would use: empty Graph → knowledge;
	// otherwise the repo/name field carries the instance.
	sel := req.GetTarget()
	key := "knowledge"
	if sel.GetGraph() == "code" {
		key = "code/" + sel.GetRepo()
	}
	st := f.statsByKey[key]
	if st == nil {
		st = &knowledgev1.GraphStats{}
	}
	return &knowledgev1.StatsResponse{GraphStats: st}, nil
}

// coverageDeps is the minimal ClientDeps whose GraphCaller is the coverageFake.
type coverageDeps struct{ gc GraphCaller }

func (d *coverageDeps) LocalLiveness() LocalLiveness     { return nil }
func (d *coverageDeps) Sink() collector.Sink             { return nil }
func (d *coverageDeps) RootDir() string                  { return "" }
func (d *coverageDeps) WorkerRuntime() WorkerRuntimeAPI  { return nil }
func (d *coverageDeps) WorkerCRUD() WorkerCRUDAPI        { return nil }
func (d *coverageDeps) GraphTypeCRUD() GraphTypeCRUDAPI  { return nil }
func (d *coverageDeps) Embedder() embed.BinaryEmbedder   { return nil }
func (d *coverageDeps) BackendResolver() BackendResolver { return nil }
func (d *coverageDeps) GraphCaller() GraphCaller         { return d.gc }
func (d *coverageDeps) LocalGraphCaller() GraphCaller    { return d.gc }
func (d *coverageDeps) RepoResolver() *RepoResolver      { return nil }
func (d *coverageDeps) SegmentManager() SegmentSearcher  { return nil }
func (d *coverageDeps) SegmentShipper() SegmentShipper   { return nil }
func (d *coverageDeps) PipelineScanner() PipelineScanner { return nil }

// TestRenderLLMCoverage_Table pins the per-graph coverage rendering:
//   - the knowledge row is present even though its enumerated name is empty (T3-2)
//   - a fully-covered code graph renders distinctly from a 0-of-N knowledge graph
//   - the auto-summary caption is present (T3-1)
//   - every StatsRequest the renderer issued carried IncludeCoverage==true (T2)
func TestRenderLLMCoverage_Table(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		// knowledge: 10 nodes, 0 summarized → "0 of 10" (never-ran-on-code shape)
		"knowledge": {NonProxyNodeCount: 10, SummarizedCount: 0, BinaryVectorCount: 0},
		// code/myrepo: fully covered 8 of 8 + 8 embedded, no failures
		"code/myrepo": {NonProxyNodeCount: 8, SummarizedCount: 8, BinaryVectorCount: 8},
	}}
	deps := &coverageDeps{gc: fake}

	out := renderLLMCoverage(context.Background(), deps)

	// Header + caption (T3-1).
	assert.Contains(t, out, "LLM coverage")
	assert.Contains(t, out, "deterministic auto-summaries",
		"the summarized-semantics caption must be present")

	// Knowledge row present despite empty enumerated name (T3-2).
	assert.Contains(t, out, "| knowledge |", "knowledge row must render via the explicit empty-name selector")
	// Code row present via enumeration.
	assert.Contains(t, out, "| code/myrepo |")

	// 0-of-N distinct from N-of-N: knowledge is "0 of 10", code is "8 of 8".
	assert.Contains(t, out, "0 of 10", "never-summarized knowledge graph renders 0 of N")
	assert.Contains(t, out, "8 of 8", "fully-covered code graph renders N of N")

	// T2: every issued StatsRequest set IncludeCoverage.
	require.NotEmpty(t, fake.reqs, "renderer must issue at least one Stats RPC")
	for i, r := range fake.reqs {
		assert.True(t, r.GetIncludeCoverage(), "StatsRequest %d must set IncludeCoverage (the coverage trigger)", i)
	}
}

// TestRenderLLMCoverage_EmptyGraph pins the (empty graph) rendering for a
// zero-denominator graph — visibly distinct from a covered graph.
func TestRenderLLMCoverage_EmptyGraph(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge": {NonProxyNodeCount: 0},
	}}
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake})
	assert.Contains(t, out, "(empty graph)", "a zero-denominator graph renders (empty graph)")
	assert.Contains(t, out, "| knowledge | (empty graph)")
}
