// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakePruner records the parallel (graphTypes, names) + execute it was called with
// and returns a canned report, so the handler test asserts enumeration + the
// Execute gate + rendering without a live segment engine.
type fakePruner struct {
	gotTypes   []kgtypes.GraphType
	gotNames   []string
	gotExecute bool
	report     PruneCacheReport
	err        error
}

func (p *fakePruner) PruneCache(
	_ context.Context, graphTypes []kgtypes.GraphType, names []string, execute bool,
) (PruneCacheReport, error) {
	p.gotTypes = graphTypes
	p.gotNames = names
	p.gotExecute = execute
	return p.report, p.err
}

// pruneCacheTestDeps embeds interceptTestDeps (PipelineReady()==true, GraphCaller()
// returns gc) and overrides SegmentPruner() so the handler reaches the fake. A nil
// pruner exercises the degraded-client rejection.
type pruneCacheTestDeps struct {
	interceptTestDeps
	pruner         SegmentPruner
	pipelineNotRdy bool
}

func (d pruneCacheTestDeps) SegmentPruner() SegmentPruner { return d.pruner }
func (d pruneCacheTestDeps) PipelineReady() bool          { return !d.pipelineNotRdy }

// pruneCacheCall builds a deps wired with the given pruner + a graph-caller seeded
// with the given code repos, and invokes the handler with the given args.
func pruneCacheCall(t *testing.T, pruner SegmentPruner, codeRepos []string, args string, pipelineNotReady bool) kgtools.ToolResult {
	t.Helper()
	gc := &fakeGraphCaller{}
	if len(codeRepos) > 0 {
		body, err := json.Marshal(graphNamesSeed(codeRepos))
		require.NoError(t, err)
		gc.listGraphsResult = &kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(body)}}}
	}
	deps := pruneCacheTestDeps{
		interceptTestDeps: interceptTestDeps{gc: gc},
		pruner:            pruner,
		pipelineNotRdy:    pipelineNotReady,
	}
	var a manageArgs
	require.NoError(t, json.Unmarshal([]byte(args), &a))
	return handleClientPruneCache(context.Background(), deps, a)
}

// graphNamesSeed builds the {graphs:[{graph_type,graph_name}]} body the
// fakeGraphCaller decodes for a RETURN_MODE_GRAPH_NAMES code-repo enumeration.
func graphNamesSeed(repos []string) map[string]any {
	graphs := make([]map[string]string, 0, len(repos))
	for _, r := range repos {
		graphs = append(graphs, map[string]string{"graph_type": string(kgtypes.GraphCode), "graph_name": r})
	}
	return map[string]any{"graphs": graphs}
}

// TestPruneCache_RejectsNotReady asserts the bind-first readiness gate fires before
// any enumeration or prune.
func TestPruneCache_RejectsNotReady(t *testing.T) {
	p := &fakePruner{}
	res := pruneCacheCall(t, p, nil, `{"operation":"prune-cache"}`, true)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "daemon still starting")
	assert.Nil(t, p.gotTypes, "no PruneCache call when not ready")
}

// TestPruneCache_RejectsDegraded asserts a nil SegmentPruner (degraded client) is
// rejected with a clear message and never prunes.
func TestPruneCache_RejectsDegraded(t *testing.T) {
	res := pruneCacheCall(t, nil, nil, `{"operation":"prune-cache"}`, false)
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "segment engine unavailable")
}

// TestPruneCache_EnumeratesKnowledgeAndCodeRepos asserts the handler builds the
// parallel (graphTypes, names) slices = knowledge/default + every code repo, in
// that order.
func TestPruneCache_EnumeratesKnowledgeAndCodeRepos(t *testing.T) {
	p := &fakePruner{}
	res := pruneCacheCall(t, p, []string{"repoA", "repoB"}, `{"operation":"prune-cache"}`, false)
	require.False(t, res.IsError, "prune-cache: %s", toolResultText(res))

	require.Equal(t, []kgtypes.GraphType{kgtypes.GraphKnowledge, kgtypes.GraphCode, kgtypes.GraphCode}, p.gotTypes)
	require.Equal(t, []string{"default", "repoA", "repoB"}, p.gotNames)
	assert.False(t, p.gotExecute, "default is preview (execute=false)")
}

// TestPruneCache_PreviewByDefault asserts execute=false renders the DRY RUN preview
// with the per-pool would-remove counts and never sets the Execute flag.
func TestPruneCache_PreviewByDefault(t *testing.T) {
	p := &fakePruner{report: PruneCacheReport{
		Graphs: []PruneCacheGraphReport{
			{GraphType: kgtypes.GraphKnowledge, Name: "default", Format: "hnsw", Orphans: []string{"o1", "o2"}, Bytes: 4096},
		},
	}}
	res := pruneCacheCall(t, p, nil, `{"operation":"prune-cache"}`, false)
	require.False(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "DRY RUN")
	assert.Contains(t, body, "would remove 2 orphaned segments (4096 bytes)")
	assert.Contains(t, body, "execute:true")
	assert.Contains(t, body, "knowledge/default [hnsw]: 2 orphan(s)")
	assert.False(t, p.gotExecute)
}

// TestPruneCache_ExecuteRendersRemoved asserts execute=true passes the flag through
// and renders the removed-N result.
func TestPruneCache_ExecuteRendersRemoved(t *testing.T) {
	p := &fakePruner{report: PruneCacheReport{
		Graphs:       []PruneCacheGraphReport{{GraphType: kgtypes.GraphCode, Name: "repoA", Format: "bm25", Orphans: []string{"o1"}, Bytes: 1024}},
		Removed:      1,
		RemovedBytes: 1024,
	}}
	res := pruneCacheCall(t, p, []string{"repoA"}, `{"operation":"prune-cache","execute":true}`, false)
	require.False(t, res.IsError)
	assert.True(t, p.gotExecute, "execute:true must reach the pruner")
	body := toolResultText(res)
	assert.Contains(t, body, "prune-cache complete: removed 1 orphaned segments (1024 bytes)")
}

// TestPruneCache_SurfacesAbortedPools asserts a List(0) subset-aborted pool is
// surfaced loudly in the rendered report (never silently skipped).
func TestPruneCache_SurfacesAbortedPools(t *testing.T) {
	p := &fakePruner{report: PruneCacheReport{
		Graphs: []PruneCacheGraphReport{
			{GraphType: kgtypes.GraphCode, Name: "repoA", Format: "hnsw", Aborted: true, AbortReason: "live set not a subset of List(0) — incomplete, skipping"},
		},
	}}
	res := pruneCacheCall(t, p, []string{"repoA"}, `{"operation":"prune-cache"}`, false)
	require.False(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "SKIPPED")
	assert.Contains(t, body, "subset of List(0)")
}

// TestPruneCache_AbortsOnEnumerationError asserts a code-repo enumeration failure
// aborts the WHOLE op (knowledge/default not pruned either) — never a silent
// scope-down.
func TestPruneCache_AbortsOnEnumerationError(t *testing.T) {
	p := &fakePruner{}
	gc := &fakeGraphCaller{execErr: errors.New("boom")}
	deps := pruneCacheTestDeps{interceptTestDeps: interceptTestDeps{gc: gc}, pruner: p}
	var a manageArgs
	require.NoError(t, json.Unmarshal([]byte(`{"operation":"prune-cache"}`), &a))
	res := handleClientPruneCache(context.Background(), deps, a)

	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "could not enumerate code repos")
	assert.Contains(t, body, "knowledge/default was not pruned either")
	assert.Nil(t, p.gotTypes, "no PruneCache call when enumeration fails (all-or-nothing)")
}

// TestPruneCache_JSONFormat asserts format=json returns the structured report.
func TestPruneCache_JSONFormat(t *testing.T) {
	p := &fakePruner{report: PruneCacheReport{
		Graphs:       []PruneCacheGraphReport{{GraphType: kgtypes.GraphKnowledge, Name: "default", Format: "hnsw", Orphans: []string{"o1"}, Bytes: 512}},
		Removed:      1,
		RemovedBytes: 512,
	}}
	res := pruneCacheCall(t, p, nil, `{"operation":"prune-cache","execute":true,"format":"json"}`, false)
	require.False(t, res.IsError)

	var decoded PruneCacheReport
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &decoded))
	require.Equal(t, 1, decoded.Removed)
	require.EqualValues(t, 512, decoded.RemovedBytes)
	require.Len(t, decoded.Graphs, 1)
	assert.Equal(t, "hnsw", decoded.Graphs[0].Format)
}

// TestInterceptManage_PruneCache_Dispatches asserts InterceptManage routes
// operation:"prune-cache" to the handler (handled=true). The default interceptTestDeps
// has a nil SegmentPruner, so the handler returns the degraded-client error — but the
// dispatch arm is exercised (handled=true), which is what this asserts.
func TestInterceptManage_PruneCache_Dispatches(t *testing.T) {
	handled, res := manageCall(t, &fakeIndexer{}, `{"operation":"prune-cache"}`)
	require.True(t, handled, "prune-cache must dispatch to the client handler")
	assert.True(t, res.IsError, "nil SegmentPruner → degraded error (but still handled)")
	assert.Contains(t, toolResultText(res), "segment engine unavailable")
}

// TestInterceptManage_UnknownOp_FallsThrough asserts an unknown operation is NOT
// handled (returns false, {}) so the dispatch addition did not swallow other ops.
func TestInterceptManage_UnknownOp_FallsThrough(t *testing.T) {
	handled, res := manageCall(t, &fakeIndexer{}, `{"operation":"definitely-not-an-op"}`)
	assert.False(t, handled, "an unknown operation must fall through unhandled")
	assert.False(t, res.IsError)
	assert.Empty(t, toolResultText(res))
}
