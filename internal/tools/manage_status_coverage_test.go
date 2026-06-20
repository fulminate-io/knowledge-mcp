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
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// coverageFake is a statsRPC that (a) records every StatsRequest it receives so
// the test can assert IncludeCoverage was set (the T2 gate trigger), (b) serves
// per-graph GraphStats keyed by the resolved instance label, and (c) serves a
// RETURN_MODE_GRAPH_NAMES enumeration that returns a named "code" repo AND a named
// "practice" language (a NON-code embeddable builtin), and an
// EMPTY name for knowledge (mirroring the real drop of empty names) — proving the
// knowledge row is rendered via the explicit empty-name selector, not enumeration,
// and a non-code embeddable graph now renders a real segment-coverage cell.
type coverageFake struct {
	reqs       []*knowledgev1.StatsRequest
	statsByKey map[string]*knowledgev1.GraphStats
}

func (f *coverageFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// The code graph reports a named repo and the practice graph a named language (a
	// NON-code embeddable builtin renderLLMCoverage now enumerates); everything else
	// (incl the default knowledge graph) enumerates empty — which listGraphNamesOfType
	// drops.
	switch req.GetTarget().GetGraph() {
	case "code":
		return &knowledgev1.ExecuteResponse{GraphNames: []*knowledgev1.GraphInfo{{Name: "myrepo"}}}, nil
	case "practice":
		return &knowledgev1.ExecuteResponse{GraphNames: []*knowledgev1.GraphInfo{{Name: "go"}}}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *coverageFake) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.reqs = append(f.reqs, req)
	// Resolve the row label the renderer would use: empty Graph → knowledge; a code
	// graph carries the repo field, a practice graph the language field.
	sel := req.GetTarget()
	key := "knowledge"
	switch sel.GetGraph() {
	case "code":
		key = "code/" + sel.GetRepo()
	case "practice":
		key = "practice/" + sel.GetLanguage()
	}
	st := f.statsByKey[key]
	if st == nil {
		st = &knowledgev1.GraphStats{}
	}
	return &knowledgev1.StatsResponse{GraphStats: st}, nil
}

// coverageSegReader is a SegmentCoverageReader stub: it serves a per-graph covered
// doc count AND live resident doc count keyed by (graphType, name), so the
// renderer's segment-coverage column reads real numbers — shipped covered and live
// resident — for the segment-bearing graphs (knowledge + code).
type coverageSegReader struct {
	coveredByKey  map[string]int
	residentByKey map[string]int
}

func (r *coverageSegReader) segKey(gt kgtypes.GraphType, name string) string {
	key := string(gt)
	if name != "" {
		key += "/" + name
	}
	return key
}

func (r *coverageSegReader) ShippedSegmentDocCount(
	_ context.Context, gt kgtypes.GraphType, name string,
) (int, bool, error) {
	return r.coveredByKey[r.segKey(gt, name)], false, nil
}

func (r *coverageSegReader) ResidentDocCount(gt kgtypes.GraphType, name string) int {
	return r.residentByKey[r.segKey(gt, name)]
}

// coverageDeps is the minimal ClientDeps whose GraphCaller is the coverageFake and
// whose SegmentCoverage seam is an optional coverageSegReader stub (nil when the
// test does not exercise the segment column).
type coverageDeps struct {
	gc     GraphCaller
	segCov SegmentCoverageReader
}

func (d *coverageDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *coverageDeps) Sink() collector.Sink                         { return nil }
func (d *coverageDeps) RootDir() string                              { return "" }
func (d *coverageDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *coverageDeps) WorkerReady() bool                            { return true }
func (d *coverageDeps) PropReady() bool                              { return true }
func (d *coverageDeps) PipelineReady() bool                          { return true }
func (d *coverageDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *coverageDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *coverageDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *coverageDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *coverageDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *coverageDeps) BackendResolver() BackendResolver             { return nil }
func (d *coverageDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *coverageDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *coverageDeps) RepoResolver() *RepoResolver                  { return nil }
func (d *coverageDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *coverageDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *coverageDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *coverageDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *coverageDeps) SegmentCoverage() SegmentCoverageReader       { return d.segCov }
func (d *coverageDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *coverageDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *coverageDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *coverageDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *coverageDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *coverageDeps) TensionsProvider() TensionsProvider   { return nil }

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
		// practice/go: a NON-code embeddable builtin — 20 nodes, 12 embedded. Its
		// segment coverage now surfaces as a real cell instead of "—".
		"practice/go": {NonProxyNodeCount: 20, SummarizedCount: 20, BinaryVectorCount: 12},
	}}
	// Segment-coverage stub: code/myrepo's segments cover 6 of its 8 embedded docs
	// (a degenerate-looking pool, the lever-3 operator signal) and the live engine is
	// resident with all 6 (a healthy live≈covered row); knowledge has 0 embedded so
	// its segment cell is "0 of 0 (live 0)". practice/go has 0 segment coverage of its
	// 12 embedded docs (a never-shipped non-code graph) — zero shown as a real number,
	// "0 of 12 (live 0)", not "—".
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge": 0, "code/myrepo": 6, "practice/go": 0},
		residentByKey: map[string]int{"knowledge": 0, "code/myrepo": 6, "practice/go": 0},
	}
	deps := &coverageDeps{gc: fake, segCov: seg}

	out := renderLLMCoverage(context.Background(), deps)

	// Header + caption (T3-1).
	assert.Contains(t, out, "LLM coverage")
	assert.Contains(t, out, "deterministic auto-summaries",
		"the summarized-semantics caption must be present")
	// Segment-coverage column header (lever 3).
	assert.Contains(t, out, "segment coverage", "the segment-coverage column header must be present")

	// Knowledge row present despite empty enumerated name (T3-2).
	assert.Contains(t, out, "| knowledge |", "knowledge row must render via the explicit empty-name selector")
	// Code row present via enumeration.
	assert.Contains(t, out, "| code/myrepo |")

	// 0-of-N distinct from N-of-N: knowledge is "0 of 10", code is "8 of 8".
	assert.Contains(t, out, "0 of 10", "never-summarized knowledge graph renders 0 of N")
	assert.Contains(t, out, "8 of 8", "fully-covered code graph renders N of N")

	// Segment coverage renders real covered-of-embedded WITH the live resident suffix
	// for the code graph (6 of 8 (live 6), the same BinaryVectorCount denominator
	// lever 2 uses); a healthy row shows live≈covered.
	assert.Contains(t, out, "6 of 8 (live 6)", "code graph renders segment-covered of embedded with live resident")

	// lever-3 surface: a NON-code embeddable builtin (practice/go) renders a
	// REAL segment-coverage cell — zero coverage shown as "0 of 12 (live 0)", not "—"
	// or an omitted row. segCoveredFor now gates on HasRebuildableSegments, so
	// practice/cloud/cicd report coverage.
	assert.Contains(t, out, "| practice/go |", "a non-code embeddable graph renders its own row")
	assert.Contains(t, out, "0 of 12 (live 0)",
		"practice graph renders zero segment coverage as a real number, not the — placeholder")

	// T2: every issued StatsRequest set IncludeCoverage.
	require.NotEmpty(t, fake.reqs, "renderer must issue at least one Stats RPC")
	for i, r := range fake.reqs {
		assert.True(t, r.GetIncludeCoverage(), "StatsRequest %d must set IncludeCoverage (the coverage trigger)", i)
	}
}

// TestRenderLLMCoverage_LiveResidentCollapse is the masking-fix criterion: a graph
// whose server-shipped corpus is intact (covered=N) but whose LIVE engine resident
// has collapsed to 0 renders a cell surfacing both — "N of N (live 0)" — so the
// post-restart collapse is visible instead of masked behind the intact shipped
// figure. Dropping the live-resident suffix makes the "live 0" assertion fail.
func TestRenderLLMCoverage_LiveResidentCollapse(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge":   {NonProxyNodeCount: 10, SummarizedCount: 10, BinaryVectorCount: 10},
		"code/myrepo": {NonProxyNodeCount: 80, SummarizedCount: 80, BinaryVectorCount: 80},
	}}
	// code/myrepo: server holds the full corpus (covered=80) but the live searchable
	// pool has collapsed (resident=0) — the masked post-restart incident.
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge": 0, "code/myrepo": 80},
		residentByKey: map[string]int{"knowledge": 0, "code/myrepo": 0},
	}
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake, segCov: seg})

	assert.Contains(t, out, "80 of 80 (live 0)",
		"a collapsed live pool surfaces live 0 against the intact shipped corpus — the masking fix")
}

// TestRenderLLMCoverage_EmptyGraph pins the (empty graph) rendering for a
// zero-denominator graph — visibly distinct from a covered graph — and that the
// empty row keeps the 7-column alignment after the segment-coverage column was
// added (a trailing empty cell, not a short row).
func TestRenderLLMCoverage_EmptyGraph(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge": {NonProxyNodeCount: 0},
	}}
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake})
	assert.Contains(t, out, "(empty graph)", "a zero-denominator graph renders (empty graph)")
	// 7-column alignment: label + (empty graph) + 5 empty cells = 8 pipes.
	assert.Contains(t, out, "| knowledge | (empty graph) | | | | | |",
		"the empty-graph row keeps the segment-coverage column's alignment")
}

// TestRenderLLMCoverage_SegmentPlaceholder pins the "—" placeholder: when the
// SegmentCoverage seam is unwired (degraded headless mode), a segment-bearing
// graph's segment-coverage cell renders the placeholder rather than a number or a
// crash.
func TestRenderLLMCoverage_SegmentPlaceholder(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge":   {NonProxyNodeCount: 10, SummarizedCount: 3, BinaryVectorCount: 4},
		"code/myrepo": {NonProxyNodeCount: 8, SummarizedCount: 8, BinaryVectorCount: 8},
	}}
	// segCov nil — the degraded/headless path.
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake})
	assert.Contains(t, out, "—", "an unwired segment seam renders the placeholder, not a number")
}
